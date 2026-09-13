//! DNS over TLS (RFC 7858): optional PROXY v2 header, TLS, then the shared stream path.

use crate::edns::Transport;
use crate::server::proxy::{self, ProxyPolicy};
use crate::server::{Answerer, ClientInfo, stream, tls::CertStore};
use crate::telemetry::metrics::{ConnectionGuard, ENCRYPTED, HandshakeResult};
use std::rc::Rc;
use std::sync::Arc;
use std::time::Duration;

pub const DOT_IDLE: Duration = Duration::from_secs(30);
const HANDSHAKE_TIMEOUT: Duration = Duration::from_secs(10);
/// Back-off after a failed accept (e.g. EMFILE) so the loop does not spin.
const ACCEPT_BACKOFF: Duration = Duration::from_millis(50);

pub async fn run_dot<A: Answerer + 'static>(
    listener: tokio::net::TcpListener,
    acceptor: tokio_rustls::TlsAcceptor,
    certs: Arc<CertStore>,
    answerer: Rc<A>,
    proxy_policy: Option<Rc<ProxyPolicy>>,
) {
    loop {
        let (mut tcp, peer) = match listener.accept().await {
            Ok(v) => v,
            Err(_) => {
                tokio::time::sleep(ACCEPT_BACKOFF).await;
                continue;
            }
        };
        let _ = tcp.set_nodelay(true);
        let (acceptor, certs, answerer, proxy_policy) = (
            acceptor.clone(),
            certs.clone(),
            answerer.clone(),
            proxy_policy.clone(),
        );
        tokio::task::spawn_local(async move {
            let addr = match proxy::resolve_client(proxy_policy.as_deref(), &mut tcp, peer).await {
                Ok(a) => a,
                Err(r) => {
                    ENCRYPTED.proxy_rejected(Transport::Dot, &r);
                    return;
                }
            };
            if certs.is_empty() {
                ENCRYPTED.handshake(Transport::Dot, HandshakeResult::NoCertificate);
                return;
            }
            let tls = match tokio::time::timeout(HANDSHAKE_TIMEOUT, acceptor.accept(tcp)).await {
                Ok(Ok(s)) => {
                    ENCRYPTED.handshake(Transport::Dot, HandshakeResult::Ok);
                    s
                }
                _ => {
                    ENCRYPTED.handshake(Transport::Dot, HandshakeResult::Failed);
                    return;
                }
            };
            let _guard = ConnectionGuard::new(Transport::Dot);
            stream::serve_dns_stream(
                answerer,
                tls,
                ClientInfo {
                    addr,
                    transport: Transport::Dot,
                },
                DOT_IDLE,
            )
            .await;
        });
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::server::testutil::{EchoAnswerer, test_query};
    use crate::server::{proxy::SIGNATURE, tls};
    use hickory_proto::op::Message;
    use hickory_proto::rr::{RData, rdata::A};
    use rustls::pki_types::ServerName;
    use std::net::Ipv4Addr;
    use tokio::io::{AsyncReadExt, AsyncWriteExt};

    #[tokio::test(flavor = "current_thread")]
    async fn dot_end_to_end_with_proxy_header() {
        tokio::task::LocalSet::new()
            .run_until(async {
                let ck = rcgen::generate_simple_self_signed(vec!["dns.test".to_string()]).unwrap();
                let store = Arc::new(CertStore::new());
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
                let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
                let addr = listener.local_addr().unwrap();
                let acceptor = tokio_rustls::TlsAcceptor::from(tls::stream_server_config(
                    store.clone(),
                    &[b"dot"],
                ));
                tokio::task::spawn_local(run_dot(
                    listener,
                    acceptor,
                    store,
                    Rc::new(EchoAnswerer),
                    Some(Rc::new(ProxyPolicy::new(&["127.0.0.0/8".into()]).unwrap())),
                ));

                let mut tcp = tokio::net::TcpStream::connect(addr).await.unwrap();
                let mut header = SIGNATURE.to_vec();
                header.extend_from_slice(&[
                    0x21, 0x11, 0x00, 0x0c, 198, 51, 100, 23, 127, 0, 0, 1, 0x9c, 0x40, 0x03, 0x55,
                ]);
                tcp.write_all(&header).await.unwrap();

                let mut roots = rustls::RootCertStore::empty();
                roots.add(ck.cert.der().clone()).unwrap();
                let mut cfg = rustls::ClientConfig::builder_with_provider(tls::provider())
                    .with_safe_default_protocol_versions()
                    .unwrap()
                    .with_root_certificates(roots)
                    .with_no_client_auth();
                cfg.alpn_protocols = vec![b"dot".to_vec()];
                let mut s = tokio_rustls::TlsConnector::from(Arc::new(cfg))
                    .connect(ServerName::try_from("dns.test").unwrap(), tcp)
                    .await
                    .unwrap();

                let q = test_query(0x4242, "example.com.");
                s.write_all(&(q.len() as u16).to_be_bytes()).await.unwrap();
                s.write_all(&q).await.unwrap();
                s.flush().await.unwrap();
                let mut len = [0u8; 2];
                s.read_exact(&mut len).await.unwrap();
                let mut body = vec![0u8; usize::from(u16::from_be_bytes(len))];
                s.read_exact(&mut body).await.unwrap();
                let m = Message::from_vec(&body).unwrap();
                assert_eq!(m.metadata.id, 0x4242);
                assert_eq!(
                    m.answers[1].data,
                    RData::A(A(Ipv4Addr::new(198, 51, 100, 23)))
                );
            })
            .await;
    }
}
