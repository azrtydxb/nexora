use std::alloc::{GlobalAlloc, Layout, System};
use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};

struct Counting;
static ALLOCS: AtomicU64 = AtomicU64::new(0);
static ARMED: AtomicBool = AtomicBool::new(false);
unsafe impl GlobalAlloc for Counting {
    unsafe fn alloc(&self, l: Layout) -> *mut u8 {
        if ARMED.load(Ordering::Relaxed) {
            ALLOCS.fetch_add(1, Ordering::Relaxed);
        }
        unsafe { System.alloc(l) }
    }
    unsafe fn dealloc(&self, p: *mut u8, l: Layout) {
        unsafe { System.dealloc(p, l) }
    }
    unsafe fn realloc(&self, p: *mut u8, l: Layout, n: usize) -> *mut u8 {
        if ARMED.load(Ordering::Relaxed) {
            ALLOCS.fetch_add(1, Ordering::Relaxed);
        }
        unsafe { System.realloc(p, l, n) }
    }
}
#[global_allocator]
static GLOBAL: Counting = Counting;

#[test]
fn cache_hit_path_does_not_allocate() {
    use hickory_proto::op::{Edns, Message, MessageType, OpCode, Query};
    use hickory_proto::rr::{Name, RData, Record, RecordType, rdata::A, rdata::opt::EdnsOption};
    use hickory_proto::serialize::binary::{BinDecodable, BinEncodable};
    use nexora_engine::cache::CacheKey;
    use nexora_engine::edns::Transport;
    use nexora_engine::proto::*;
    use nexora_engine::server::{FastOutcome, Shared, WorkerCtx, handle_packet};
    use nexora_engine::snapshot::{DirBlobs, apply};
    use nexora_engine::wire::parse_query;

    let shared = Shared::new(1);
    let snap = ConfigSnapshot {
        version: 1,
        cache: Some(CacheConfig {
            max_bytes: 8 << 20,
            min_ttl: 0,
            max_ttl: 86400,
            negative_max_ttl: 3600,
            stale_window: 0,
        }),
        acl_allow_cidrs: vec!["127.0.0.0/8".into()],
        filter: Some(FilterConfig {
            block_mode: BlockMode::NullIp as i32,
            block_ttl: 60,
            ..Default::default()
        }),
        telemetry: Some(TelemetryConfig::default()),
        resolver: Some(ResolverConfig::default()),
        ..Default::default()
    };
    let tmp = tempfile::tempdir().unwrap();
    apply(
        &shared.runtime,
        snap,
        &DirBlobs {
            dir: tmp.path().into(),
        },
        None,
    );

    let mut m = Message::new(1, MessageType::Query, OpCode::Query);
    m.metadata.recursion_desired = true;
    m.add_query(Query::query(
        Name::from_ascii("Hot.Example.").unwrap(),
        RecordType::A,
    ));
    let mut e = Edns::new();
    e.set_max_payload(1232);
    e.options_mut().insert(EdnsOption::Unknown(10, vec![9; 8]));
    m.set_edns(e);
    let query = m.to_bytes().unwrap();
    let mut r = Message::from_bytes(&query).unwrap();
    r.metadata.message_type = MessageType::Response;
    r.add_answer(Record::from_rdata(
        Name::from_ascii("hot.example.").unwrap(),
        300,
        RData::A(A::new(192, 0, 2, 1)),
    ));
    let upstream = r.to_bytes().unwrap();
    {
        let rt = shared.runtime.load();
        let v = parse_query(&query).unwrap();
        rt.cache.insert(
            CacheKey::from_query(&v),
            &upstream,
            &v,
            nexora_engine::clock::now_secs(),
        );
    }
    let ctx = WorkerCtx::new(0, shared.clone());
    let client: std::net::SocketAddr = "127.0.0.1:40000".parse().unwrap();
    let mut out = [0u8; 1232];
    for _ in 0..64 {
        let rt = shared.runtime.load();
        assert!(matches!(
            handle_packet(&ctx, &rt, &query, client, Transport::Udp, &mut out),
            FastOutcome::Reply(_)
        ));
    }
    ARMED.store(true, Ordering::Relaxed);
    for _ in 0..50_000 {
        let rt = shared.runtime.load();
        match handle_packet(&ctx, &rt, &query, client, Transport::Udp, &mut out) {
            FastOutcome::Reply(n) => assert!(n > 40),
            _ => panic!("expected cache hit"),
        }
    }
    ARMED.store(false, Ordering::Relaxed);
    assert_eq!(
        ALLOCS.load(Ordering::Relaxed),
        0,
        "cache-hit path allocated"
    );
    assert!(shared.metrics.sum_cache_hits() >= 50_000);
    assert!(!shared.querylog.is_empty(), "query records were pushed");
}
