use bytes::Bytes;
use crossbeam_utils::CachePadded;
use hickory_proto::op::{Message, MessageType, OpCode, Query};
use hickory_proto::rr::{Name, RData, Record, RecordType, rdata::A};
use hickory_proto::serialize::binary::{BinDecodable, BinEncodable};
use nexora_engine::upstream::{Question, UpstreamError, doh::DohClient, dot::DotClient};
use nexora_engine::wire::parse_query;
use std::sync::Arc;
use std::sync::atomic::{AtomicU64, AtomicUsize, Ordering};
use std::time::Duration;
use tokio::io::{AsyncReadExt, AsyncWriteExt};

fn query(name: &str) -> (Vec<u8>, Question) {
    let mut m = Message::new(77, MessageType::Query, OpCode::Query);
    m.metadata.recursion_desired = true;
    m.add_query(Query::query(Name::from_ascii(name).unwrap(), RecordType::A));
    let b = m.to_bytes().unwrap();
    let v = parse_query(&b).unwrap();
    let q = Question {
        key: v.key,
        qtype: v.qtype,
        qclass: v.qclass,
    };
    (b, q)
}
fn answer(req: &[u8]) -> Vec<u8> {
    let mut m = Message::from_bytes(req).unwrap();
    m.metadata.message_type = MessageType::Response;
    let n = m.queries[0].name().clone();
    m.add_answer(Record::from_rdata(n, 60, RData::A(A::new(192, 0, 2, 53))));
    m.to_bytes().unwrap()
}
fn self_signed() -> (String, rustls::ServerConfig) {
    // rustls has both ring and aws-lc-rs compiled in (reqwest), so pick one explicitly.
    let _ = rustls::crypto::ring::default_provider().install_default();
    let ck = rcgen::generate_simple_self_signed(vec!["dns.test".into()]).unwrap();
    let pem = ck.cert.pem();
    let key = rustls::pki_types::PrivateKeyDer::Pkcs8(ck.signing_key.serialize_der().into());
    let mut cfg = rustls::ServerConfig::builder()
        .with_no_client_auth()
        .with_single_cert(vec![ck.cert.der().clone()], key)
        .unwrap();
    cfg.alpn_protocols = vec![b"h2".to_vec()];
    (pem, cfg)
}

#[tokio::test(flavor = "current_thread")]
async fn dot_pipelines_fifty_concurrent_queries_over_one_connection() {
    let (pem, cfg) = self_signed();
    let acceptor = tokio_rustls::TlsAcceptor::from(Arc::new(cfg));
    let l = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = l.local_addr().unwrap();
    let accepted = Arc::new(AtomicUsize::new(0));
    let acc2 = accepted.clone();
    tokio::spawn(async move {
        loop {
            let (s, _) = l.accept().await.unwrap();
            acc2.fetch_add(1, Ordering::SeqCst);
            let mut tls = acceptor.accept(s).await.unwrap();
            let mut batch = Vec::new();
            while batch.len() < 50 {
                let len = tls.read_u16().await.unwrap() as usize;
                let mut req = vec![0u8; len];
                tls.read_exact(&mut req).await.unwrap();
                batch.push(req);
            }
            for req in batch.iter().rev() {
                // answer out of order
                let r = answer(req);
                tls.write_u16(r.len() as u16).await.unwrap();
                tls.write_all(&r).await.unwrap();
            }
        }
    });
    tokio::task::LocalSet::new()
        .run_until(async move {
            let client = std::rc::Rc::new(
                DotClient::new(
                    addr,
                    "dns.test",
                    &pem,
                    Arc::new(CachePadded::new(AtomicU64::new(0))),
                )
                .unwrap(),
            );
            let mut tasks = Vec::new();
            for i in 0..50 {
                let c = client.clone();
                tasks.push(tokio::task::spawn_local(async move {
                    let (q, question) = query(&format!("n{i}.example."));
                    let r: Bytes = c
                        .exchange(&q, &question, Duration::from_secs(2))
                        .await
                        .unwrap();
                    assert_eq!(
                        Message::from_bytes(&r).unwrap().queries[0]
                            .name()
                            .to_ascii(),
                        format!("n{i}.example.")
                    );
                    assert_eq!(u16::from_be_bytes([r[0], r[1]]), 77);
                }));
            }
            for t in tasks {
                t.await.unwrap();
            }
            assert_eq!(client.connections_opened(), 1);
        })
        .await;
    assert_eq!(accepted.load(Ordering::SeqCst), 1);
}

#[tokio::test(flavor = "current_thread")]
async fn dot_rejects_untrusted_certificate() {
    let (_pem, cfg) = self_signed();
    let (other_pem, _) = self_signed();
    let acceptor = tokio_rustls::TlsAcceptor::from(Arc::new(cfg));
    let l = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = l.local_addr().unwrap();
    tokio::spawn(async move {
        let (s, _) = l.accept().await.unwrap();
        let _ = acceptor.accept(s).await;
    });
    tokio::task::LocalSet::new()
        .run_until(async move {
            let c = DotClient::new(
                addr,
                "dns.test",
                &other_pem,
                Arc::new(CachePadded::new(AtomicU64::new(0))),
            )
            .unwrap();
            let (q, question) = query("x.example.");
            assert!(matches!(
                c.exchange(&q, &question, Duration::from_secs(1)).await,
                Err(UpstreamError::Tls(_))
            ));
        })
        .await;
}

#[tokio::test(flavor = "current_thread")]
async fn doh_posts_dns_message_over_http2() {
    use http_body_util::{BodyExt, Full};
    use hyper::{Request, Response, service::service_fn};
    let (pem, cfg) = self_signed();
    let acceptor = tokio_rustls::TlsAcceptor::from(Arc::new(cfg));
    let l = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let port = l.local_addr().unwrap().port();
    tokio::spawn(async move {
        loop {
            let (s, _) = l.accept().await.unwrap();
            let tls = acceptor.accept(s).await.unwrap();
            tokio::spawn(async move {
                let svc = service_fn(|req: Request<hyper::body::Incoming>| async move {
                    assert_eq!(req.version(), hyper::Version::HTTP_2);
                    assert_eq!(req.method(), hyper::Method::POST);
                    assert_eq!(req.headers()["content-type"], "application/dns-message");
                    let body = req.into_body().collect().await.unwrap().to_bytes();
                    assert_eq!(&body[0..2], &[0, 0], "DoH queries use ID 0");
                    Ok::<_, std::convert::Infallible>(
                        Response::builder()
                            .header("content-type", "application/dns-message")
                            .body(Full::new(Bytes::from(answer(&body))))
                            .unwrap(),
                    )
                });
                hyper::server::conn::http2::Builder::new(hyper_util::rt::TokioExecutor::new())
                    .serve_connection(hyper_util::rt::TokioIo::new(tls), svc)
                    .await
                    .unwrap();
            });
        }
    });
    unsafe { std::env::set_var("NEXORA_DOH_RESOLVE", "dns.test=127.0.0.1") };
    let client = DohClient::new(&format!("https://dns.test:{port}/dns-query"), &pem).unwrap();
    let (q, question) = query("doh.example.");
    let r = client
        .exchange(&q, &question, Duration::from_secs(2))
        .await
        .unwrap();
    let m = Message::from_bytes(&r).unwrap();
    assert_eq!(m.metadata.id, 77);
    assert_eq!(m.answers.len(), 1);
}
