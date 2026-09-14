//! RPZ on the query path of running workers: query-phase QNAME/CLIENT-IP triggers with EDE,
//! response-phase IP triggers, and policy answers never cached.

use hickory_proto::op::{Edns, Message, MessageType, OpCode, Query, ResponseCode};
use hickory_proto::rr::rdata::opt::{EdnsCode, EdnsOption};
use hickory_proto::rr::{Name, RData, Record, RecordType, rdata::A};
use hickory_proto::serialize::binary::{BinDecodable, BinEncodable};
use nexora_engine::bootstrap::Bootstrap;
use nexora_engine::proto::*;
use nexora_engine::server::tls::CertStore;
use nexora_engine::server::{Shared, spawn_workers};
use nexora_engine::snapshot::{ApplyOutcome, DirBlobs, apply};
use std::net::{Ipv4Addr, SocketAddr, UdpSocket};
use std::sync::Arc;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::time::Duration;

const RPZ: &str = "$TTL 60
@ SOA ns.rpz.test. hostmaster.rpz.test. 1 60 60 86400 60
bad.example CNAME .
local.example A 10.9.9.9
32.66.2.0.192.rpz-ip CNAME *.
";

/// Answers every A query with 192.0.2.66 for names starting with `ip66`, else 192.0.2.1.
fn fake_upstream(count: Arc<AtomicUsize>) -> SocketAddr {
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
            let last = if name.to_ascii().starts_with("ip66") {
                66
            } else {
                1
            };
            m.add_answer(Record::from_rdata(
                name,
                300,
                RData::A(A::new(192, 0, 2, last)),
            ));
            let _ = s.send_to(&m.to_bytes().unwrap(), peer);
        }
    });
    addr
}

fn start_engine(upstream: SocketAddr) -> (SocketAddr, Arc<Shared>) {
    use sha2::Digest;
    let port = std::net::TcpListener::bind("127.0.0.1:0")
        .unwrap()
        .local_addr()
        .unwrap()
        .port();
    let dir = tempfile::tempdir().unwrap().keep();
    let boot: Bootstrap = toml::from_str(&format!(
        "node_name = \"t\"\nstate_dir = \"{}\"\nlisten_udp = [\"127.0.0.1:{port}\"]\nlisten_tcp = [\"127.0.0.1:{port}\"]\nworkers = 1\nstandalone_snapshot = \"x\"\n",
        dir.display()
    ))
    .unwrap();
    let z = zstd::encode_all(RPZ.as_bytes(), 3).unwrap();
    let sha = hex::encode(sha2::Sha256::digest(&z));
    std::fs::write(dir.join(&sha), &z).unwrap();
    let shared = Shared::new(1);
    let snap = ConfigSnapshot {
        version: 1,
        resolver: Some(ResolverConfig {
            strategy: UpstreamStrategy::Ordered as i32,
            ..Default::default()
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
        acl_allow_cidrs: vec!["127.0.0.0/8".into()],
        filter: Some(FilterConfig::default()),
        telemetry: Some(TelemetryConfig::default()),
        rpz_zones: vec![RpzZone {
            id: "z1".into(),
            name: "rpz.test.".into(),
            source: Some(rpz_zone::Source::File(RpzFileSource {
                blob: Some(BlobRef {
                    sha256: sha,
                    size: z.len() as u64,
                    name: "rpz".into(),
                }),
            })),
            policy_override: 0,
            refresh_nonce: 0,
        }],
        ..Default::default()
    };
    assert!(matches!(
        apply(&shared.runtime, snap, &DirBlobs { dir: dir.clone() }, None),
        ApplyOutcome::Applied { .. }
    ));
    shared.recursor.sync(&shared.runtime.load_full());
    spawn_workers(shared.clone(), &boot, Arc::new(CertStore::new())).unwrap();
    std::thread::sleep(Duration::from_millis(200));
    (format!("127.0.0.1:{port}").parse().unwrap(), shared)
}

fn ask(server: SocketAddr, name: &str) -> Message {
    let c = UdpSocket::bind("127.0.0.1:0").unwrap();
    c.set_read_timeout(Some(Duration::from_secs(3))).unwrap();
    let mut m = Message::new(0x5151, MessageType::Query, OpCode::Query);
    m.metadata.recursion_desired = true;
    m.add_query(Query::query(Name::from_ascii(name).unwrap(), RecordType::A));
    let mut e = Edns::new();
    e.set_max_payload(1232);
    m.set_edns(e);
    c.send_to(&m.to_bytes().unwrap(), server).unwrap();
    let mut buf = [0u8; 4096];
    let (n, _) = c.recv_from(&mut buf).unwrap();
    Message::from_bytes(&buf[..n]).unwrap()
}

fn ede_code(m: &Message) -> Option<u16> {
    match m.edns.as_ref()?.option(EdnsCode::Unknown(15))? {
        EdnsOption::Unknown(_, data) if data.len() >= 2 => {
            Some(u16::from_be_bytes([data[0], data[1]]))
        }
        _ => None,
    }
}

#[test]
fn rpz_query_and_response_triggers_answer_with_ede_and_are_not_cached() {
    let count = Arc::new(AtomicUsize::new(0));
    let (srv, shared) = start_engine(fake_upstream(count.clone()));

    let ok = ask(srv, "ok.example.");
    assert_eq!(ok.metadata.response_code, ResponseCode::NoError);
    assert_eq!(ok.answers[0].data, RData::A(A(Ipv4Addr::new(192, 0, 2, 1))));
    assert_eq!(ede_code(&ok), None);
    assert_eq!(
        count.load(Ordering::SeqCst),
        1,
        "unlisted names are forwarded"
    );

    for _ in 0..2 {
        let bad = ask(srv, "BAD.example.");
        assert_eq!(bad.metadata.response_code, ResponseCode::NXDomain);
        assert_eq!(bad.queries[0].name().to_ascii(), "BAD.example.");
        assert_eq!(ede_code(&bad), Some(15));
    }
    let local = ask(srv, "local.example.");
    assert_eq!(
        local.answers[0].data,
        RData::A(A(Ipv4Addr::new(10, 9, 9, 9)))
    );
    assert_eq!(ede_code(&local), Some(4));
    assert_eq!(
        count.load(Ordering::SeqCst),
        1,
        "query-phase hits never reach the upstream"
    );

    for i in 1..=2 {
        let rewritten = ask(srv, "ip66.example.");
        assert_eq!(rewritten.metadata.response_code, ResponseCode::NoError);
        assert!(
            rewritten.answers.is_empty(),
            "response IP trigger -> NODATA"
        );
        assert_eq!(ede_code(&rewritten), Some(15));
        assert_eq!(
            count.load(Ordering::SeqCst),
            1 + i,
            "RPZ-rewritten answers are not cached"
        );
    }
    let status = &shared.recursor.rpz.manager.status()[0];
    assert_eq!(status.hits, 5);
}
