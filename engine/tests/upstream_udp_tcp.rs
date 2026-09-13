use bytes::Bytes;
use crossbeam_utils::CachePadded;
use hickory_proto::op::{Message, MessageType, OpCode, Query};
use hickory_proto::rr::{Name, RData, Record, RecordType, rdata::A};
use hickory_proto::serialize::binary::{BinDecodable, BinEncodable};
use nexora_engine::upstream::{
    self, Health, Protocol, Question, Strategy, UpstreamSet, UpstreamSpec, WorkerUpstreams, tcp,
    udp::UdpPool,
};
use nexora_engine::wire::parse_query;
use std::net::SocketAddr;
use std::sync::Arc;
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::Duration;
use tokio::net::UdpSocket;

fn query(name: &str) -> (Vec<u8>, Question) {
    let mut m = Message::new(1, MessageType::Query, OpCode::Query);
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
fn reply_to(req: &[u8], id: u16, name: Option<&str>) -> Vec<u8> {
    let mut m = Message::from_bytes(req).unwrap();
    m.metadata.id = id;
    m.metadata.message_type = MessageType::Response;
    if let Some(n) = name {
        m.queries[0].set_name(Name::from_ascii(n).unwrap());
    }
    let owner = m.queries[0].name().clone();
    m.add_answer(Record::from_rdata(
        owner,
        60,
        RData::A(A::new(192, 0, 2, 7)),
    ));
    m.to_bytes().unwrap()
}
fn counter() -> Arc<CachePadded<AtomicU64>> {
    Arc::new(CachePadded::new(AtomicU64::new(0)))
}
async fn local<F: std::future::Future>(f: F) -> F::Output {
    tokio::task::LocalSet::new().run_until(f).await
}

#[tokio::test(flavor = "current_thread")]
async fn spoofed_wrong_id_and_wrong_question_are_dropped_and_counted() {
    local(async {
        let server = UdpSocket::bind("127.0.0.1:0").await.unwrap();
        let addr = server.local_addr().unwrap();
        let mism = counter();
        let pool = UdpPool::new(addr, mism.clone()).unwrap();
        let (q, question) = query("spoof.example.");
        tokio::task::spawn_local(async move {
            let mut buf = [0u8; 1500];
            let (n, peer) = server.recv_from(&mut buf).await.unwrap();
            let req = buf[..n].to_vec();
            let id = u16::from_be_bytes([req[0], req[1]]);
            server
                .send_to(&reply_to(&req, id.wrapping_add(1), None), peer)
                .await
                .unwrap();
            server
                .send_to(&reply_to(&req, id, Some("evil.example.")), peer)
                .await
                .unwrap();
            server
                .send_to(&reply_to(&req, id, None), peer)
                .await
                .unwrap();
        });
        let resp = pool
            .exchange(&q, &question, Duration::from_millis(500))
            .await
            .unwrap();
        let m = Message::from_bytes(&resp).unwrap();
        assert_eq!(m.queries[0].name().to_ascii(), "spoof.example.");
        assert_eq!(mism.load(Ordering::Relaxed), 2);
    })
    .await;
}

#[tokio::test(flavor = "current_thread")]
async fn reply_from_other_source_port_never_accepted() {
    local(async {
        let server = UdpSocket::bind("127.0.0.1:0").await.unwrap();
        let attacker = UdpSocket::bind("127.0.0.1:0").await.unwrap();
        let pool = UdpPool::new(server.local_addr().unwrap(), counter()).unwrap();
        let (q, question) = query("port.example.");
        tokio::task::spawn_local(async move {
            let mut buf = [0u8; 1500];
            let (n, peer) = server.recv_from(&mut buf).await.unwrap();
            let id = u16::from_be_bytes([buf[0], buf[1]]);
            attacker
                .send_to(&reply_to(&buf[..n], id, None), peer)
                .await
                .unwrap();
        });
        let err = pool
            .exchange(&q, &question, Duration::from_millis(300))
            .await
            .unwrap_err();
        assert!(
            matches!(err, upstream::UpstreamError::Timeout),
            "got {err:?}"
        );
    })
    .await;
}

#[tokio::test(flavor = "current_thread")]
async fn ids_random_ports_spread_and_sockets_rotate() {
    local(async {
        let server = Arc::new(UdpSocket::bind("127.0.0.1:0").await.unwrap());
        let addr = server.local_addr().unwrap();
        let seen = Arc::new(parking_lot::Mutex::new((
            Vec::<u16>::new(),
            Vec::<SocketAddr>::new(),
        )));
        let (s2, seen2) = (server.clone(), seen.clone());
        tokio::task::spawn_local(async move {
            let mut buf = [0u8; 1500];
            loop {
                let (n, peer) = s2.recv_from(&mut buf).await.unwrap();
                let id = u16::from_be_bytes([buf[0], buf[1]]);
                {
                    let mut g = seen2.lock();
                    g.0.push(id);
                    g.1.push(peer);
                }
                s2.send_to(&reply_to(&buf[..n], id, None), peer)
                    .await
                    .unwrap();
            }
        });
        let pool = UdpPool::new(addr, counter()).unwrap();
        let (q, question) = query("rand.example.");
        for _ in 0..(udp_pool_total()) {
            pool.exchange(&q, &question, Duration::from_millis(500))
                .await
                .unwrap();
        }
        let g = seen.lock();
        let mut ids = g.0.clone();
        let sequential = ids
            .windows(2)
            .filter(|w| w[1] == w[0].wrapping_add(1))
            .count();
        assert!(sequential < 5, "ids look sequential");
        ids.sort();
        ids.dedup();
        assert!(ids.len() > (g.0.len() * 9) / 10, "ids repeat too often");
        let mut ports: Vec<u16> = g.1.iter().map(|p| p.port()).collect();
        ports.sort();
        ports.dedup();
        assert!(
            ports.len() > nexora_engine::upstream::udp::POOL_SIZE,
            "sockets were not rotated: {} ports",
            ports.len()
        );
        assert!(pool.sockets_created() > nexora_engine::upstream::udp::POOL_SIZE as u64);
    })
    .await;
}
fn udp_pool_total() -> usize {
    nexora_engine::upstream::udp::POOL_SIZE * 1024 + 64
}

#[tokio::test(flavor = "current_thread")]
async fn tcp_exchange_validates_question() {
    use tokio::io::{AsyncReadExt, AsyncWriteExt};
    let l = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = l.local_addr().unwrap();
    tokio::spawn(async move {
        for evil in [true, false] {
            let (mut s, _) = l.accept().await.unwrap();
            let len = s.read_u16().await.unwrap() as usize;
            let mut req = vec![0u8; len];
            s.read_exact(&mut req).await.unwrap();
            let id = u16::from_be_bytes([req[0], req[1]]);
            let r = reply_to(&req, id, if evil { Some("evil.example.") } else { None });
            s.write_u16(r.len() as u16).await.unwrap();
            s.write_all(&r).await.unwrap();
        }
    });
    let (q, question) = query("tcp.example.");
    assert!(matches!(
        tcp::exchange_tcp(addr, &q, &question, Duration::from_millis(500)).await,
        Err(upstream::UpstreamError::Malformed)
    ));
    let ok: Bytes = tcp::exchange_tcp(addr, &q, &question, Duration::from_millis(500))
        .await
        .unwrap();
    assert_eq!(Message::from_bytes(&ok).unwrap().answers.len(), 1);
}

#[test]
fn health_marks_down_after_three_failures_and_admits_one_probe() {
    let h = Health::default();
    for _ in 0..3 {
        assert!(h.is_up(100));
        h.record_failure(100);
    }
    assert!(!h.is_up(100));
    assert!(!h.admit(104));
    assert!(h.admit(105), "one probe after 5 s");
    assert!(!h.admit(105), "only one probe");
    h.record_success(Duration::from_millis(3));
    assert!(h.is_up(105));
}

#[test]
fn strategies_order_candidates() {
    let spec = |id: &str| UpstreamSpec {
        id: id.into(),
        name: id.into(),
        protocol: Protocol::Udp,
        addr: Some("127.0.0.1:53".parse().unwrap()),
        tls_server_name: String::new(),
        doh_url: String::new(),
        timeout: Duration::from_millis(250),
        ca_pem: String::new(),
    };
    let set = UpstreamSet::new(
        vec![spec("a"), spec("b"), spec("c")],
        Strategy::Fastest,
        None,
    );
    set.health[0].record_success(Duration::from_millis(40));
    set.health[1].record_success(Duration::from_millis(5));
    set.health[2].record_success(Duration::from_millis(20));
    let mut out = Vec::new();
    set.order(10, &mut out);
    assert_eq!(out, vec![1, 2, 0]);
    let ordered = UpstreamSet::new(
        vec![spec("a"), spec("b"), spec("c")],
        Strategy::Ordered,
        Some(&set),
    );
    assert!(
        Arc::ptr_eq(&ordered.health[1], &set.health[1]),
        "health carried across snapshots"
    );
    for _ in 0..3 {
        ordered.health[0].record_failure(10);
    }
    ordered.order(10, &mut out);
    assert_eq!(out, vec![1, 2]);
    let _ = WorkerUpstreams::new(counter());
}
