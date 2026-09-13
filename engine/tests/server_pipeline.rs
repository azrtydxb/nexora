use hickory_proto::op::{Edns, Message, MessageType, OpCode, Query, ResponseCode};
use hickory_proto::rr::{Name, RData, Record, RecordType, rdata::A};
use hickory_proto::serialize::binary::{BinDecodable, BinEncodable};
use nexora_engine::bootstrap::Bootstrap;
use nexora_engine::proto::*;
use nexora_engine::server::tls::CertStore;
use nexora_engine::server::{Shared, spawn_workers};
use nexora_engine::snapshot::{DirBlobs, apply};
use std::net::{SocketAddr, UdpSocket};
use std::sync::Arc;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::time::Duration;

fn free_port() -> u16 {
    std::net::TcpListener::bind("127.0.0.1:0")
        .unwrap()
        .local_addr()
        .unwrap()
        .port()
}

fn fake_upstream(count: Arc<AtomicUsize>, answers: usize) -> SocketAddr {
    let s = UdpSocket::bind("127.0.0.1:0").unwrap();
    let addr = s.local_addr().unwrap();
    std::thread::spawn(move || {
        let mut buf = [0u8; 4096];
        loop {
            let (n, peer) = s.recv_from(&mut buf).unwrap();
            count.fetch_add(1, Ordering::SeqCst);
            let mut m = Message::from_bytes(&buf[..n]).unwrap();
            m.metadata.message_type = MessageType::Response;
            m.metadata.recursion_available = true;
            let name = m.queries[0].name().clone();
            for i in 0..answers {
                m.add_answer(Record::from_rdata(
                    name.clone(),
                    300,
                    RData::A(A::new(192, 0, (i / 250) as u8, (i % 250) as u8)),
                ));
            }
            let _ = s.send_to(&m.to_bytes().unwrap(), peer);
        }
    });
    addr
}

fn start_engine(
    upstream: SocketAddr,
    acl: &str,
    blocklist: Option<&str>,
) -> (SocketAddr, Arc<Shared>) {
    let port = free_port();
    let dir = tempfile::tempdir().unwrap().keep();
    let boot: Bootstrap = toml::from_str(&format!(
        "node_name = \"t\"\nstate_dir = \"{}\"\nlisten_udp = [\"127.0.0.1:{port}\"]\nlisten_tcp = [\"127.0.0.1:{port}\"]\nworkers = 2\nstandalone_snapshot = \"x\"\n",
        dir.display()
    ))
    .unwrap();
    let shared = Shared::new(2);
    let mut filter = FilterConfig {
        block_mode: BlockMode::NullIp as i32,
        block_ttl: 60,
        ..Default::default()
    };
    if let Some(text) = blocklist {
        use sha2::Digest;
        let z = zstd::encode_all(text.as_bytes(), 3).unwrap();
        let sha = hex::encode(sha2::Sha256::digest(&z));
        std::fs::write(dir.join(&sha), &z).unwrap();
        filter.blocklists.push(BlobRef {
            sha256: sha,
            size: z.len() as u64,
            name: "l".into(),
        });
    }
    let snap = ConfigSnapshot {
        version: 1,
        resolver: Some(ResolverConfig {
            strategy: UpstreamStrategy::Ordered as i32,
        }),
        cache: Some(CacheConfig {
            max_bytes: 8 << 20,
            min_ttl: 0,
            max_ttl: 86400,
            negative_max_ttl: 3600,
            stale_window: 0,
        }),
        upstreams: vec![Upstream {
            id: "u".into(),
            name: "u".into(),
            protocol: UpstreamProtocol::Udp as i32,
            address: upstream.to_string(),
            timeout_ms: 500,
            ..Default::default()
        }],
        acl_allow_cidrs: vec![acl.into()],
        filter: Some(filter),
        telemetry: Some(TelemetryConfig::default()),
        ..Default::default()
    };
    assert!(matches!(
        apply(&shared.runtime, snap, &DirBlobs { dir: dir.clone() }, None),
        nexora_engine::snapshot::ApplyOutcome::Applied { .. }
    ));
    spawn_workers(shared.clone(), &boot, Arc::new(CertStore::new())).unwrap();
    std::thread::sleep(Duration::from_millis(200));
    (format!("127.0.0.1:{port}").parse().unwrap(), shared)
}

fn ask(server: SocketAddr, name: &str, edns: Option<u16>) -> Message {
    let c = UdpSocket::bind("127.0.0.1:0").unwrap();
    c.set_read_timeout(Some(Duration::from_secs(3))).unwrap();
    let mut m = Message::new(rand_id(), MessageType::Query, OpCode::Query);
    m.metadata.recursion_desired = true;
    m.add_query(Query::query(Name::from_ascii(name).unwrap(), RecordType::A));
    if let Some(sz) = edns {
        let mut e = Edns::new();
        e.set_max_payload(sz);
        m.set_edns(e);
    }
    c.send_to(&m.to_bytes().unwrap(), server).unwrap();
    let mut buf = [0u8; 4096];
    let (n, _) = c.recv_from(&mut buf).unwrap();
    let r = Message::from_bytes(&buf[..n]).unwrap();
    assert_eq!(r.metadata.id, m.metadata.id);
    r
}
fn rand_id() -> u16 {
    (std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap()
        .subsec_nanos()
        & 0xffff) as u16
}

#[test]
fn miss_then_hit_with_decremented_ttl_and_one_upstream_query() {
    let count = Arc::new(AtomicUsize::new(0));
    let (srv, shared) = start_engine(fake_upstream(count.clone(), 1), "127.0.0.0/8", None);
    let first = ask(srv, "cache.example.", None);
    assert_eq!(first.metadata.response_code, ResponseCode::NoError);
    assert_eq!(first.answers[0].ttl, 300);
    std::thread::sleep(Duration::from_millis(2100));
    let second = ask(srv, "CACHE.example.", None);
    assert_eq!(second.queries[0].name().to_ascii(), "CACHE.example.");
    assert!(second.answers[0].ttl < 300);
    assert_eq!(count.load(Ordering::SeqCst), 1);
    assert!(shared.metrics.sum_cache_hits() >= 1);
}

#[test]
fn acl_refuses_and_blocklist_blocks() {
    let count = Arc::new(AtomicUsize::new(0));
    let (srv, _) = start_engine(
        fake_upstream(count.clone(), 1),
        "127.0.0.0/8",
        Some("ads.example\n"),
    );
    assert_eq!(
        ask(srv, "ok.example.", None).metadata.response_code,
        ResponseCode::NoError
    );
    let blocked = ask(srv, "x.ads.example.", None);
    match &blocked.answers[0].data {
        RData::A(a) => assert_eq!(a.0, std::net::Ipv4Addr::UNSPECIFIED),
        other => panic!("expected A, got {other:?}"),
    }
    let (srv2, _) = start_engine(fake_upstream(count, 1), "10.0.0.0/8", None);
    assert_eq!(
        ask(srv2, "ok.example.", None).metadata.response_code,
        ResponseCode::Refused
    );
}

#[test]
fn large_answer_truncates_over_udp_and_completes_over_tcp() {
    use std::io::{Read, Write};
    let count = Arc::new(AtomicUsize::new(0));
    let (srv, _) = start_engine(fake_upstream(count.clone(), 100), "127.0.0.0/8", None);
    let udp = ask(srv, "big.example.", Some(1232));
    assert!(udp.metadata.truncation);
    assert_eq!(udp.answers.len(), 0);
    let mut s = std::net::TcpStream::connect(srv).unwrap();
    s.set_read_timeout(Some(Duration::from_secs(3))).unwrap();
    let mut m = Message::new(9, MessageType::Query, OpCode::Query);
    m.metadata.recursion_desired = true;
    m.add_query(Query::query(
        Name::from_ascii("big.example.").unwrap(),
        RecordType::A,
    ));
    let q = m.to_bytes().unwrap();
    s.write_all(&(q.len() as u16).to_be_bytes()).unwrap();
    s.write_all(&q).unwrap();
    let mut len = [0u8; 2];
    s.read_exact(&mut len).unwrap();
    let mut body = vec![0u8; u16::from_be_bytes(len) as usize];
    s.read_exact(&mut body).unwrap();
    let full = Message::from_bytes(&body).unwrap();
    assert!(!full.metadata.truncation);
    assert_eq!(full.answers.len(), 100);
}

#[test]
fn malformed_and_notimp() {
    let count = Arc::new(AtomicUsize::new(0));
    let (srv, _) = start_engine(fake_upstream(count, 1), "127.0.0.0/8", None);
    let c = UdpSocket::bind("127.0.0.1:0").unwrap();
    c.set_read_timeout(Some(Duration::from_millis(500)))
        .unwrap();
    let mut buf = [0u8; 512];
    c.send_to(&[0xab, 0xcd, 0x01, 0x00, 0, 2, 0, 0, 0, 0, 0, 0], srv)
        .unwrap();
    let (n, _) = c.recv_from(&mut buf).unwrap();
    assert_eq!(&buf[0..2], &[0xab, 0xcd]);
    assert_eq!(buf[3] & 0x0f, 1, "FORMERR");
    assert!(n >= 12);
    c.send_to(&[0xab, 0xce, 0x20, 0x00, 0, 0, 0, 0, 0, 0, 0, 0], srv)
        .unwrap();
    let _ = c.recv_from(&mut buf).unwrap();
    assert_eq!(buf[3] & 0x0f, 4, "NOTIMP");
    c.send_to(&[1, 2, 3], srv).unwrap();
    assert!(c.recv_from(&mut buf).is_err(), "short packets are dropped");
}
