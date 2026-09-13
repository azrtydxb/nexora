//! DNS over QUIC (RFC 9250): one bidirectional stream per query on per-worker endpoints.

use crate::edns::Transport;
use crate::server::{Answerer, ClientInfo, proxy::normalize_peer, tls::CertStore};
use crate::telemetry::metrics::{ConnectionGuard, ENCRYPTED, HandshakeResult};
use quinn::VarInt;
use std::net::SocketAddr;
use std::rc::Rc;
use std::sync::Arc;

pub const DOQ_NO_ERROR: u32 = 0x0;
pub const DOQ_INTERNAL_ERROR: u32 = 0x1;
pub const DOQ_PROTOCOL_ERROR: u32 = 0x2;
pub const DOQ_REQUEST_CANCELLED: u32 = 0x3;
pub const DOQ_EXCESSIVE_LOAD: u32 = 0x4;

/// The largest stream accepted: a 2-byte length prefix and a 65535-byte message.
const MAX_STREAM: usize = 2 + 65535;
const HEADER_LEN: usize = 12;

#[derive(Debug, PartialEq, Eq)]
pub enum DoqFrameError {
    Short,
    LengthMismatch,
    NonZeroId,
}

/// Checks one query stream: a length prefix matching the rest of the stream and
/// a message ID of 0 (RFC 9250 section 4.2.1); returns the DNS message.
pub fn decode_query(buf: &[u8]) -> Result<&[u8], DoqFrameError> {
    if buf.len() < 2 {
        return Err(DoqFrameError::Short);
    }
    let len = usize::from(u16::from_be_bytes([buf[0], buf[1]]));
    if len != buf.len() - 2 || len < HEADER_LEN {
        return Err(DoqFrameError::LengthMismatch);
    }
    let msg = &buf[2..];
    if msg[0] != 0 || msg[1] != 0 {
        return Err(DoqFrameError::NonZeroId);
    }
    Ok(msg)
}

/// Endpoint settings with a stateless-reset key shared by every worker's endpoint,
/// so any worker can reset a connection another worker served.
pub fn endpoint_config(reset_key: &[u8; 64]) -> quinn::EndpointConfig {
    let key = aws_lc_rs::hmac::Key::new(aws_lc_rs::hmac::HMAC_SHA256, reset_key);
    quinn::EndpointConfig::new(Arc::new(key))
}

/// A non-blocking SO_REUSEPORT UDP socket for one worker's DoQ endpoint; usable
/// outside a tokio runtime.
pub fn bind_doq_socket(addr: SocketAddr) -> std::io::Result<std::net::UdpSocket> {
    use socket2::{Domain, Protocol, Socket, Type};
    let socket = Socket::new(Domain::for_address(addr), Type::DGRAM, Some(Protocol::UDP))?;
    if addr.is_ipv6() {
        socket.set_only_v6(true)?;
    }
    socket.set_reuse_port(true)?;
    socket.set_nonblocking(true)?;
    socket.bind(&addr.into())?;
    Ok(socket.into())
}

/// Binds a DoQ server endpoint; must run inside a tokio runtime.
pub fn bind_doq(
    addr: SocketAddr,
    server: quinn::ServerConfig,
    endpoint: quinn::EndpointConfig,
) -> std::io::Result<quinn::Endpoint> {
    quinn::Endpoint::new(
        endpoint,
        Some(server),
        bind_doq_socket(addr)?,
        Arc::new(quinn::TokioRuntime),
    )
}

/// Accepts DoQ connections and answers each bidirectional stream as one query.
/// A malformed query, an oversized stream or a unidirectional stream closes the
/// connection with `DOQ_PROTOCOL_ERROR`.
pub async fn run_doq<A: Answerer + 'static>(
    endpoint: quinn::Endpoint,
    answerer: Rc<A>,
    certs: Arc<CertStore>,
) {
    while let Some(incoming) = endpoint.accept().await {
        let answerer = answerer.clone();
        let no_cert = certs.is_empty();
        tokio::task::spawn_local(async move {
            let conn = match incoming.await {
                Ok(c) => {
                    ENCRYPTED.handshake(Transport::Doq, HandshakeResult::Ok);
                    c
                }
                Err(_) => {
                    let r = if no_cert {
                        HandshakeResult::NoCertificate
                    } else {
                        HandshakeResult::Failed
                    };
                    ENCRYPTED.handshake(Transport::Doq, r);
                    return;
                }
            };
            let _guard = ConnectionGuard::new(Transport::Doq);
            let client = ClientInfo {
                addr: normalize_peer(conn.remote_address()),
                transport: Transport::Doq,
            };
            let uni = conn.clone();
            tokio::task::spawn_local(async move {
                if uni.accept_uni().await.is_ok() {
                    ENCRYPTED.doq_protocol_error();
                    uni.close(
                        VarInt::from_u32(DOQ_PROTOCOL_ERROR),
                        b"unidirectional stream",
                    );
                }
            });
            while let Ok((mut send, mut recv)) = conn.accept_bi().await {
                let (answerer, conn) = (answerer.clone(), conn.clone());
                tokio::task::spawn_local(async move {
                    let buf = match recv.read_to_end(MAX_STREAM).await {
                        Ok(b) => b,
                        Err(quinn::ReadToEndError::TooLong) => {
                            ENCRYPTED.doq_protocol_error();
                            conn.close(VarInt::from_u32(DOQ_PROTOCOL_ERROR), b"message too long");
                            return;
                        }
                        Err(_) => {
                            let _ = send.reset(VarInt::from_u32(DOQ_REQUEST_CANCELLED));
                            return;
                        }
                    };
                    let Ok(query) = decode_query(&buf) else {
                        ENCRYPTED.doq_protocol_error();
                        conn.close(VarInt::from_u32(DOQ_PROTOCOL_ERROR), b"malformed query");
                        return;
                    };
                    // debt: one stream buffer, answer buffer and frame per DoQ query;
                    // pool them if DoQ shows up in the perf gate.
                    let mut out = Vec::with_capacity(512);
                    answerer.answer(client, query, &mut out).await;
                    if out.is_empty() || out.len() > 65535 {
                        let _ = send.reset(VarInt::from_u32(DOQ_INTERNAL_ERROR));
                        return;
                    }
                    let mut frame = Vec::with_capacity(out.len() + 2);
                    frame.extend_from_slice(&(out.len() as u16).to_be_bytes());
                    frame.extend_from_slice(&out);
                    if send.write_all(&frame).await.is_ok() {
                        let _ = send.finish();
                    }
                });
            }
        });
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::server::testutil::{EchoAnswerer, test_query};
    use rustls::pki_types::CertificateDer;

    fn framed(q: &[u8]) -> Vec<u8> {
        let mut f = (q.len() as u16).to_be_bytes().to_vec();
        f.extend_from_slice(q);
        f
    }

    #[test]
    fn framing_rules() {
        let q = test_query(0, "example.com.");
        assert_eq!(decode_query(&framed(&q)).unwrap(), &q[..]);
        assert_eq!(decode_query(&[0]), Err(DoqFrameError::Short));
        assert_eq!(
            decode_query(&framed(&q)[..10]),
            Err(DoqFrameError::LengthMismatch)
        );
        assert_eq!(
            decode_query(&framed(&test_query(9, "example.com."))),
            Err(DoqFrameError::NonZeroId)
        );
    }

    fn client_endpoint(root: CertificateDer<'static>) -> quinn::Endpoint {
        let mut roots = rustls::RootCertStore::empty();
        roots.add(root).unwrap();
        let mut tls = rustls::ClientConfig::builder_with_provider(crate::server::tls::provider())
            .with_protocol_versions(&[&rustls::version::TLS13])
            .unwrap()
            .with_root_certificates(roots)
            .with_no_client_auth();
        tls.alpn_protocols = vec![b"doq".to_vec()];
        let crypto = quinn::crypto::rustls::QuicClientConfig::try_from(tls).unwrap();
        let mut ep = quinn::Endpoint::client("127.0.0.1:0".parse().unwrap()).unwrap();
        ep.set_default_client_config(quinn::ClientConfig::new(Arc::new(crypto)));
        ep
    }

    #[tokio::test(flavor = "current_thread")]
    async fn one_stream_per_query_and_nonzero_id_closes_connection() {
        tokio::task::LocalSet::new()
            .run_until(async {
                let ck = rcgen::generate_simple_self_signed(vec!["dns.test".to_string()]).unwrap();
                let store = Arc::new(CertStore::new());
                let store_for_run = store.clone();
                let now = std::time::SystemTime::now()
                    .duration_since(std::time::UNIX_EPOCH)
                    .unwrap()
                    .as_secs() as i64;
                store
                    .install_pem(
                        ck.cert.pem().as_bytes(),
                        ck.signing_key.serialize_pem().as_bytes(),
                        now,
                    )
                    .unwrap();
                let server = bind_doq(
                    "127.0.0.1:0".parse().unwrap(),
                    crate::server::tls::quic_server_config(store),
                    endpoint_config(&[7u8; 64]),
                )
                .unwrap();
                let addr = server.local_addr().unwrap();
                tokio::task::spawn_local(run_doq(server, Rc::new(EchoAnswerer), store_for_run));

                let client = client_endpoint(ck.cert.der().clone());
                let conn = client.connect(addr, "dns.test").unwrap().await.unwrap();
                let mut tasks = Vec::new();
                for _ in 0..20 {
                    let conn = conn.clone();
                    tasks.push(tokio::task::spawn_local(async move {
                        let (mut send, mut recv) = conn.open_bi().await.unwrap();
                        send.write_all(&framed(&test_query(0, "example.com.")))
                            .await
                            .unwrap();
                        send.finish().unwrap();
                        let resp = recv.read_to_end(65537).await.unwrap();
                        let len = u16::from_be_bytes([resp[0], resp[1]]) as usize;
                        assert_eq!(len, resp.len() - 2);
                        let m = hickory_proto::op::Message::from_vec(&resp[2..]).unwrap();
                        assert_eq!(m.metadata.id, 0);
                        assert_eq!(m.answers.len(), 2);
                    }));
                }
                for t in tasks {
                    t.await.unwrap();
                }

                let (mut send, mut recv) = conn.open_bi().await.unwrap();
                send.write_all(&framed(&test_query(5, "example.com.")))
                    .await
                    .unwrap();
                send.finish().unwrap();
                let _ = recv.read_to_end(65537).await;
                match conn.closed().await {
                    quinn::ConnectionError::ApplicationClosed(c) => {
                        assert_eq!(c.error_code, quinn::VarInt::from_u32(DOQ_PROTOCOL_ERROR))
                    }
                    other => panic!("unexpected close: {other:?}"),
                }
            })
            .await;
    }
}
