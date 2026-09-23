//! DNS over HTTPS (RFC 8484): GET and POST on one path, HTTP/2 and HTTP/1.1 over TLS.

use crate::edns::Transport;
use crate::server::odoh::{self, OdohRuntime, OdohState};
use crate::server::proxy::{self, ProxyPolicy};
use crate::server::{Answerer, ClientInfo, Shared, tls::CertStore};
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

/// Answers one DoH request (RFC 8484, and RFC 9230 target and proxy when `odoh` enables them)
/// and counts it by method and status.
pub async fn handle<A, B>(
    answerer: &A,
    client: ClientInfo,
    doh_path: &str,
    odoh: &OdohRuntime,
    keys: &OdohState,
    req: Request<B>,
) -> Response<Full<Bytes>>
where
    A: Answerer,
    B: http_body::Body<Data = Bytes>,
    B::Error: Into<Box<dyn std::error::Error + Send + Sync>>,
{
    let method = req.method().clone();
    let resp = handle_inner(answerer, client, doh_path, odoh, keys, req).await;
    ENCRYPTED.doh_request(&method, resp.status());
    resp
}

/// Reads at most `limit` octets of the body: 413 above it, 400 on a body error.
async fn read_body<B>(body: B, limit: usize) -> Result<Bytes, StatusCode>
where
    B: http_body::Body<Data = Bytes>,
    B::Error: Into<Box<dyn std::error::Error + Send + Sync>>,
{
    match Limited::new(body, limit).collect().await {
        Ok(c) => Ok(c.to_bytes()),
        Err(e) if e.downcast_ref::<LengthLimitError>().is_some() => {
            Err(StatusCode::PAYLOAD_TOO_LARGE)
        }
        Err(_) => Err(StatusCode::BAD_REQUEST),
    }
}

fn content_type_is<B>(req: &Request<B>, want: &str) -> bool {
    req.headers()
        .get(CONTENT_TYPE)
        .and_then(|v| v.to_str().ok())
        .is_some_and(|ct| ct.eq_ignore_ascii_case(want))
}

fn has_targethost(query: Option<&str>) -> bool {
    query.is_some_and(|q| q.split('&').any(|kv| kv.starts_with("targethost=")))
}

/// `GET /.well-known/odohconfigs` of the target role.
fn odoh_configs(odoh: &OdohRuntime, keys: &OdohState) -> Response<Full<Bytes>> {
    if !odoh.target_enabled {
        return status(StatusCode::NOT_FOUND);
    }
    let resp = match keys.keyring().configs(crate::clock::unix_now()) {
        None => status(StatusCode::SERVICE_UNAVAILABLE),
        Some(configs) => Response::builder()
            .status(StatusCode::OK)
            .header(CONTENT_TYPE, "application/octet-stream")
            .header(CACHE_CONTROL, "max-age=300")
            .body(Full::new(configs))
            .expect("static headers"),
    };
    odoh::COUNTERS.count("target", resp.status());
    resp
}

/// A request carrying `targethost` on the DoH path: relayed to an allow-listed target.
async fn odoh_proxy<A, B>(
    answerer: &A,
    client: ClientInfo,
    odoh: &OdohRuntime,
    req: Request<B>,
) -> Response<Full<Bytes>>
where
    A: Answerer,
    B: http_body::Body<Data = Bytes>,
    B::Error: Into<Box<dyn std::error::Error + Send + Sync>>,
{
    let Some(proxy) = &odoh.proxy else {
        return odoh::proxy_error(StatusCode::FORBIDDEN, "http_request_denied");
    };
    let resp = async {
        if req.method() != Method::POST {
            return Response::builder()
                .status(StatusCode::METHOD_NOT_ALLOWED)
                .header(ALLOW, "POST")
                .body(Full::new(Bytes::new()))
                .expect("static response");
        }
        if !content_type_is(&req, odoh::CONTENT_TYPE) {
            return status(StatusCode::UNSUPPORTED_MEDIA_TYPE);
        }
        let Ok(Some((host, path))) = odoh::parse_proxy_params(req.uri().query()) else {
            return odoh::proxy_error(StatusCode::BAD_REQUEST, "http_request_error");
        };
        let Some(target) = proxy.allowed(&host) else {
            return odoh::proxy_error(StatusCode::FORBIDDEN, "http_request_denied");
        };
        if !answerer.recursion_allowed(client) {
            return odoh::proxy_error(StatusCode::FORBIDDEN, "http_request_denied");
        }
        match read_body(req.into_body(), odoh::MAX_BODY).await {
            Ok(body) => odoh::forward(target, &path, body, proxy.timeout).await,
            Err(code) => status(code),
        }
    }
    .await;
    odoh::COUNTERS.count("proxy", resp.status());
    resp
}

/// An `application/oblivious-dns-message` POST to the target role: opened, answered through the
/// normal pipeline for the connecting peer, sealed.
async fn odoh_target<A, B>(
    answerer: &A,
    client: ClientInfo,
    keys: &OdohState,
    req: Request<B>,
) -> Response<Full<Bytes>>
where
    A: Answerer,
    B: http_body::Body<Data = Bytes>,
    B::Error: Into<Box<dyn std::error::Error + Send + Sync>>,
{
    let resp = async {
        let body = match read_body(req.into_body(), odoh::MAX_BODY).await {
            Ok(b) => b,
            Err(code) => return status(code),
        };
        let opened = match odoh::open(&keys.keyring(), &body, crate::clock::unix_now()) {
            Ok(o) => o,
            Err(r) => return odoh::reject_response(r),
        };
        if opened.query.len() < 12 {
            return status(StatusCode::BAD_REQUEST);
        }
        let mut out = Vec::with_capacity(512);
        answerer.answer(client, &opened.query, &mut out).await;
        if out.is_empty() {
            return status(StatusCode::BAD_REQUEST);
        }
        match opened.seal(&out) {
            Ok(sealed) => Response::builder()
                .status(StatusCode::OK)
                .header(CONTENT_TYPE, odoh::CONTENT_TYPE)
                .header(CACHE_CONTROL, "no-store")
                .body(Full::new(Bytes::from(sealed)))
                .expect("static headers"),
            Err(r) => odoh::reject_response(r),
        }
    }
    .await;
    odoh::COUNTERS.count("target", resp.status());
    resp
}

async fn handle_inner<A, B>(
    answerer: &A,
    client: ClientInfo,
    doh_path: &str,
    odoh: &OdohRuntime,
    keys: &OdohState,
    req: Request<B>,
) -> Response<Full<Bytes>>
where
    A: Answerer,
    B: http_body::Body<Data = Bytes>,
    B::Error: Into<Box<dyn std::error::Error + Send + Sync>>,
{
    if req.method() == Method::GET && req.uri().path() == odoh::CONFIGS_PATH {
        return odoh_configs(odoh, keys);
    }
    if req.uri().path() == doh_path {
        if has_targethost(req.uri().query()) {
            return odoh_proxy(answerer, client, odoh, req).await;
        }
        if req.method() == Method::POST && content_type_is(&req, odoh::CONTENT_TYPE) {
            if !odoh.target_enabled {
                return status(StatusCode::UNSUPPORTED_MEDIA_TYPE);
            }
            return odoh_target(answerer, client, keys, req).await;
        }
    }
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
            if !content_type_is(&req, "application/dns-message") {
                return status(StatusCode::UNSUPPORTED_MEDIA_TYPE);
            }
            match read_body(req.into_body(), MAX_DNS_MESSAGE).await {
                Ok(b) => b.to_vec(),
                Err(code) => return status(code),
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
/// source; `X-Forwarded-For` and `Forwarded` are ignored. The ODoH settings come from the
/// runtime loaded per request and the keys from `shared.odoh`.
#[allow(clippy::too_many_arguments)] // one listener's fixed wiring
pub async fn run_doh<A: Answerer + 'static>(
    listener: tokio::net::TcpListener,
    acceptor: tokio_rustls::TlsAcceptor,
    certs: Arc<CertStore>,
    shared: Arc<Shared>,
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
        let (acceptor, certs, shared, answerer, proxy_policy, path) = (
            acceptor.clone(),
            certs.clone(),
            shared.clone(),
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
                let (shared, answerer, path) = (shared.clone(), answerer.clone(), path.clone());
                async move {
                    let rt = shared.runtime.load_full();
                    let resp = handle(&*answerer, client, &path, &rt.odoh, &shared.odoh, req).await;
                    Ok::<_, std::convert::Infallible>(resp)
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

    async fn handle<B>(
        answerer: &EchoAnswerer,
        client: ClientInfo,
        doh_path: &str,
        req: Request<B>,
    ) -> Response<Full<Bytes>>
    where
        B: http_body::Body<Data = Bytes>,
        B::Error: Into<Box<dyn std::error::Error + Send + Sync>>,
    {
        super::handle(
            answerer,
            client,
            doh_path,
            &OdohRuntime::off(),
            &OdohState::default(),
            req,
        )
        .await
    }

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
                    Shared::new(1),
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
