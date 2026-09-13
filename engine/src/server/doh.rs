//! DNS over HTTPS (RFC 8484): GET and POST on one path, HTTP/2 and HTTP/1.1 over TLS.

use crate::edns::Transport;
use crate::server::proxy::{self, ProxyPolicy};
use crate::server::{Answerer, ClientInfo, tls::CertStore};
use crate::telemetry::metrics::{ConnectionGuard, ENCRYPTED, HandshakeResult};
use base64::Engine as _;
use bytes::Bytes;
use http::header::{ALLOW, CACHE_CONTROL, CONTENT_TYPE};
use http::{Method, Request, Response, StatusCode};
use http_body_util::{BodyExt, Full, LengthLimitError, Limited};
use std::rc::Rc;
use std::sync::Arc;
use std::time::Duration;

pub const MAX_DNS_MESSAGE: usize = 65535;
const HANDSHAKE_TIMEOUT: Duration = Duration::from_secs(10);
const HEADER_READ_TIMEOUT: Duration = Duration::from_secs(10);
const H2_KEEP_ALIVE: Duration = Duration::from_secs(30);
const H2_MAX_STREAMS: u32 = 100;
/// Back-off after a failed accept (e.g. EMFILE) so the loop does not spin.
const ACCEPT_BACKOFF: Duration = Duration::from_millis(50);

fn status(code: StatusCode) -> Response<Full<Bytes>> {
    let mut b = Response::builder().status(code);
    if code == StatusCode::METHOD_NOT_ALLOWED {
        b = b.header(ALLOW, "GET, POST");
    }
    b.body(Full::new(Bytes::new())).expect("static response")
}

/// The smallest TTL over the answer and authority sections; 0 when there is none
/// or the message does not parse (used as the HTTP `max-age`).
pub fn min_ttl(response: &[u8]) -> u32 {
    match hickory_proto::op::Message::from_vec(response) {
        Ok(m) => m
            .answers
            .iter()
            .chain(m.authorities.iter())
            .map(|r| r.ttl)
            .min()
            .unwrap_or(0),
        Err(_) => 0,
    }
}

/// Answers one DoH request and counts it by method and status.
pub async fn handle<A, B>(
    answerer: &A,
    client: ClientInfo,
    doh_path: &str,
    req: Request<B>,
) -> Response<Full<Bytes>>
where
    A: Answerer,
    B: http_body::Body<Data = Bytes>,
    B::Error: Into<Box<dyn std::error::Error + Send + Sync>>,
{
    let method = req.method().clone();
    let resp = handle_inner(answerer, client, doh_path, req).await;
    ENCRYPTED.doh_request(&method, resp.status());
    resp
}

async fn handle_inner<A, B>(
    answerer: &A,
    client: ClientInfo,
    doh_path: &str,
    req: Request<B>,
) -> Response<Full<Bytes>>
where
    A: Answerer,
    B: http_body::Body<Data = Bytes>,
    B::Error: Into<Box<dyn std::error::Error + Send + Sync>>,
{
    if req.uri().path() != doh_path {
        return status(StatusCode::NOT_FOUND);
    }
    let query: Vec<u8> = match *req.method() {
        Method::GET => {
            let Some(qs) = req.uri().query() else {
                return status(StatusCode::BAD_REQUEST);
            };
            let Some(param) = qs.split('&').find_map(|kv| kv.strip_prefix("dns=")) else {
                return status(StatusCode::BAD_REQUEST);
            };
            match base64::engine::general_purpose::URL_SAFE_NO_PAD_INDIFFERENT.decode(param) {
                Ok(b) if b.len() <= MAX_DNS_MESSAGE => b,
                Ok(_) => return status(StatusCode::PAYLOAD_TOO_LARGE),
                Err(_) => return status(StatusCode::BAD_REQUEST),
            }
        }
        Method::POST => {
            let ct = req
                .headers()
                .get(CONTENT_TYPE)
                .and_then(|v| v.to_str().ok())
                .unwrap_or("");
            if !ct.eq_ignore_ascii_case("application/dns-message") {
                return status(StatusCode::UNSUPPORTED_MEDIA_TYPE);
            }
            match Limited::new(req.into_body(), MAX_DNS_MESSAGE)
                .collect()
                .await
            {
                Ok(c) => c.to_bytes().to_vec(),
                Err(e) if e.downcast_ref::<LengthLimitError>().is_some() => {
                    return status(StatusCode::PAYLOAD_TOO_LARGE);
                }
                Err(_) => return status(StatusCode::BAD_REQUEST),
            }
        }
        _ => return status(StatusCode::METHOD_NOT_ALLOWED),
    };
    if query.len() < 12 {
        return status(StatusCode::BAD_REQUEST);
    }
    let mut out = Vec::with_capacity(512);
    answerer.answer(client, &query, &mut out).await;
    if out.is_empty() {
        return status(StatusCode::BAD_REQUEST);
    }
    let max_age = min_ttl(&out);
    Response::builder()
        .status(StatusCode::OK)
        .header(CONTENT_TYPE, "application/dns-message")
        .header(CACHE_CONTROL, format!("max-age={max_age}"))
        .body(Full::new(Bytes::from(out)))
        .expect("static headers")
}

/// Runs hyper's connection tasks on the worker's `LocalSet`.
#[derive(Clone, Copy)]
pub struct LocalExec;

impl<F> hyper::rt::Executor<F> for LocalExec
where
    F: std::future::Future + 'static,
{
    fn execute(&self, fut: F) {
        tokio::task::spawn_local(fut);
    }
}

/// Accepts DoH connections: optional PROXY v2 header, TLS (ALPN `h2`/`http/1.1`),
/// then HTTP/2 or HTTP/1.1. The client address is the TCP peer or the PROXY
/// source; `X-Forwarded-For` and `Forwarded` are ignored.
pub async fn run_doh<A: Answerer + 'static>(
    listener: tokio::net::TcpListener,
    acceptor: tokio_rustls::TlsAcceptor,
    certs: Arc<CertStore>,
    answerer: Rc<A>,
    proxy_policy: Option<Rc<ProxyPolicy>>,
    path: Rc<str>,
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
        let (acceptor, certs, answerer, proxy_policy, path) = (
            acceptor.clone(),
            certs.clone(),
            answerer.clone(),
            proxy_policy.clone(),
            path.clone(),
        );
        tokio::task::spawn_local(async move {
            let addr = match proxy::resolve_client(proxy_policy.as_deref(), &mut tcp, peer).await {
                Ok(a) => a,
                Err(r) => {
                    ENCRYPTED.proxy_rejected(Transport::Doh, &r);
                    return;
                }
            };
            if certs.is_empty() {
                ENCRYPTED.handshake(Transport::Doh, HandshakeResult::NoCertificate);
                return;
            }
            let tls = match tokio::time::timeout(HANDSHAKE_TIMEOUT, acceptor.accept(tcp)).await {
                Ok(Ok(s)) => {
                    ENCRYPTED.handshake(Transport::Doh, HandshakeResult::Ok);
                    s
                }
                _ => {
                    ENCRYPTED.handshake(Transport::Doh, HandshakeResult::Failed);
                    return;
                }
            };
            let _guard = ConnectionGuard::new(Transport::Doh);
            let client = ClientInfo {
                addr,
                transport: Transport::Doh,
            };
            let service = hyper::service::service_fn(move |req: Request<hyper::body::Incoming>| {
                let answerer = answerer.clone();
                let path = path.clone();
                async move {
                    Ok::<_, std::convert::Infallible>(handle(&*answerer, client, &path, req).await)
                }
            });
            let mut builder = hyper_util::server::conn::auto::Builder::new(LocalExec);
            builder
                .http1()
                .timer(hyper_util::rt::TokioTimer::new())
                .header_read_timeout(HEADER_READ_TIMEOUT);
            builder
                .http2()
                .timer(hyper_util::rt::TokioTimer::new())
                .max_concurrent_streams(H2_MAX_STREAMS)
                .keep_alive_interval(Some(H2_KEEP_ALIVE));
            let _ = builder
                .serve_connection(hyper_util::rt::TokioIo::new(tls), service)
                .await;
        });
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::server::testutil::{EchoAnswerer, test_query};

    fn client() -> ClientInfo {
        ClientInfo {
            addr: "192.0.2.44:40000".parse().unwrap(),
            transport: Transport::Doh,
        }
    }

    async fn body(resp: Response<Full<Bytes>>) -> Vec<u8> {
        resp.into_body()
            .collect()
            .await
            .unwrap()
            .to_bytes()
            .to_vec()
    }

    #[tokio::test]
    async fn get_and_post_answer_with_dns_message_and_max_age() {
        let q = test_query(0, "example.com.");
        let url = format!(
            "/dns-query?ct&dns={}",
            base64::engine::general_purpose::URL_SAFE_NO_PAD.encode(&q)
        );
        let get = Request::get(url).body(Full::new(Bytes::new())).unwrap();
        let resp = handle(&EchoAnswerer, client(), "/dns-query", get).await;
        assert_eq!(resp.status(), StatusCode::OK);
        assert_eq!(resp.headers()[CONTENT_TYPE], "application/dns-message");
        assert_eq!(resp.headers()[CACHE_CONTROL], "max-age=60");
        let m = hickory_proto::op::Message::from_vec(&body(resp).await).unwrap();
        assert_eq!(m.answers.len(), 2);

        let post = Request::post("/dns-query")
            .header(CONTENT_TYPE, "application/dns-message")
            .body(Full::new(Bytes::from(q.clone())))
            .unwrap();
        let resp = handle(&EchoAnswerer, client(), "/dns-query", post).await;
        assert_eq!(resp.status(), StatusCode::OK);
        assert_eq!(
            hickory_proto::op::Message::from_vec(&body(resp).await)
                .unwrap()
                .answers
                .len(),
            2
        );
    }

    #[tokio::test]
    async fn error_statuses() {
        let q = test_query(0, "example.com.");
        let cases: Vec<(Request<Full<Bytes>>, StatusCode)> = vec![
            (
                Request::post("/dns-query")
                    .header(CONTENT_TYPE, "application/json")
                    .body(Full::new(Bytes::from(q.clone())))
                    .unwrap(),
                StatusCode::UNSUPPORTED_MEDIA_TYPE,
            ),
            (
                Request::get("/dns-query")
                    .body(Full::new(Bytes::new()))
                    .unwrap(),
                StatusCode::BAD_REQUEST,
            ),
            (
                Request::get("/dns-query?dns=***")
                    .body(Full::new(Bytes::new()))
                    .unwrap(),
                StatusCode::BAD_REQUEST,
            ),
            (
                Request::get("/dns-query?dns=AAAA")
                    .body(Full::new(Bytes::new()))
                    .unwrap(),
                StatusCode::BAD_REQUEST,
            ),
            (
                Request::put("/dns-query")
                    .body(Full::new(Bytes::from(q.clone())))
                    .unwrap(),
                StatusCode::METHOD_NOT_ALLOWED,
            ),
            (
                Request::get("/other")
                    .body(Full::new(Bytes::new()))
                    .unwrap(),
                StatusCode::NOT_FOUND,
            ),
            (
                Request::post("/dns-query")
                    .header(CONTENT_TYPE, "application/dns-message")
                    .body(Full::new(Bytes::from(vec![0u8; 65536])))
                    .unwrap(),
                StatusCode::PAYLOAD_TOO_LARGE,
            ),
        ];
        for (req, want) in cases {
            let method = req.method().clone();
            let resp = handle(&EchoAnswerer, client(), "/dns-query", req).await;
            assert_eq!(resp.status(), want, "{method}");
            if want == StatusCode::METHOD_NOT_ALLOWED {
                assert_eq!(resp.headers()[ALLOW], "GET, POST");
            }
        }
    }

    #[test]
    fn min_ttl_of_empty_answer_is_zero() {
        let q = test_query(7, "example.com.");
        let mut m = hickory_proto::op::Message::from_vec(&q).unwrap();
        m.metadata.message_type = hickory_proto::op::MessageType::Response;
        assert_eq!(min_ttl(&m.to_vec().unwrap()), 0);
    }

    #[tokio::test(flavor = "current_thread")]
    async fn http2_get_and_post_over_tls() {
        tokio::task::LocalSet::new()
            .run_until(async {
                let ck = rcgen::generate_simple_self_signed(vec!["dns.test".to_string()]).unwrap();
                let store = Arc::new(CertStore::new());
                store
                    .install_pem(
                        ck.cert.pem().as_bytes(),
                        ck.signing_key.serialize_pem().as_bytes(),
                        crate::clock::unix_now(),
                    )
                    .unwrap();
                let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
                let addr = listener.local_addr().unwrap();
                let acceptor = tokio_rustls::TlsAcceptor::from(
                    crate::server::tls::stream_server_config(store.clone(), &[b"h2", b"http/1.1"]),
                );
                tokio::task::spawn_local(run_doh(
                    listener,
                    acceptor,
                    store,
                    Rc::new(EchoAnswerer),
                    None,
                    Rc::from("/dns-query"),
                ));

                let mut roots = rustls::RootCertStore::empty();
                roots.add(ck.cert.der().clone()).unwrap();
                let mut tls =
                    rustls::ClientConfig::builder_with_provider(crate::server::tls::provider())
                        .with_safe_default_protocol_versions()
                        .unwrap()
                        .with_root_certificates(roots)
                        .with_no_client_auth();
                tls.alpn_protocols = vec![b"h2".to_vec()];
                let http = reqwest::Client::builder()
                    .tls_backend_preconfigured(tls)
                    .http2_prior_knowledge()
                    .resolve("dns.test", addr)
                    .build()
                    .unwrap();
                let url = format!("https://dns.test:{}/dns-query", addr.port());
                let q = test_query(0, "example.com.");
                let get = http
                    .get(format!(
                        "{url}?dns={}",
                        base64::engine::general_purpose::URL_SAFE_NO_PAD.encode(&q)
                    ))
                    .send()
                    .await
                    .unwrap();
                assert_eq!(get.version(), http::Version::HTTP_2);
                assert_eq!(get.status(), StatusCode::OK);
                let m = hickory_proto::op::Message::from_vec(&get.bytes().await.unwrap()).unwrap();
                assert_eq!(
                    m.answers[1].data,
                    hickory_proto::rr::RData::A(hickory_proto::rr::rdata::A(
                        std::net::Ipv4Addr::LOCALHOST
                    ))
                );
                let post = http
                    .post(&url)
                    .header(CONTENT_TYPE, "application/dns-message")
                    .body(q)
                    .send()
                    .await
                    .unwrap();
                assert_eq!(post.version(), http::Version::HTTP_2);
                assert_eq!(post.status(), StatusCode::OK);
                let m = hickory_proto::op::Message::from_vec(&post.bytes().await.unwrap()).unwrap();
                assert_eq!(m.answers.len(), 2);
            })
            .await;
    }
}
