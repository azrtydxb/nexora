use std::alloc::{GlobalAlloc, Layout, System};
use std::cell::Cell;
use std::sync::atomic::{AtomicU64, Ordering};

// Only allocations made by a thread that armed itself count: libtest's main thread
// and any other thread allocate concurrently and must not fail the guard.
struct Counting;
static ALLOCS: AtomicU64 = AtomicU64::new(0);
thread_local! {
    static ARMED: Cell<bool> = const { Cell::new(false) };
}
fn record() {
    // try_with: the allocator runs during thread teardown, after TLS is destroyed.
    if ARMED.try_with(Cell::get).unwrap_or(false) {
        ALLOCS.fetch_add(1, Ordering::Relaxed);
    }
}
unsafe impl GlobalAlloc for Counting {
    unsafe fn alloc(&self, l: Layout) -> *mut u8 {
        record();
        unsafe { System.alloc(l) }
    }
    unsafe fn dealloc(&self, p: *mut u8, l: Layout) {
        unsafe { System.dealloc(p, l) }
    }
    unsafe fn realloc(&self, p: *mut u8, l: Layout, n: usize) -> *mut u8 {
        record();
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
        acl_allow_cidrs: vec!["127.0.0.0/8".into(), "10.0.0.0/8".into()],
        policy_groups: vec![PolicyGroup {
            id: "g1".into(),
            name: "g1".into(),
            cidrs: vec![
                "10.0.0.0/8".into(),
                "10.1.0.0/16".into(),
                "2001:db8::/32".into(),
            ],
            rewrite_set_ids: vec!["r".into()],
            ..Default::default()
        }],
        rewrite_sets: vec![RewriteSet {
            id: "r".into(),
            label: "r".into(),
            rules: vec![RewriteRule {
                name: "*.home.test".into(),
                r#type: RewriteType::A as i32,
                value: "192.168.1.1".into(),
                ttl: 60,
            }],
        }],
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
    let client: std::net::SocketAddr = "10.1.2.3:5353".parse().unwrap();
    {
        let rt = shared.runtime.load();
        let v = parse_query(&query).unwrap();
        let (policy, group) = rt.policy.select(client.ip());
        assert_eq!(group, Some(0), "the measured client is in policy group g1");
        rt.cache.insert(
            CacheKey::in_partition(&v, policy.cache_partition()),
            &upstream,
            &v,
            nexora_engine::clock::now_secs(),
        );
    }
    let ctx = WorkerCtx::new(0, shared.clone());
    let mut out = [0u8; 1232];
    for _ in 0..64 {
        let rt = shared.runtime.load();
        assert!(matches!(
            handle_packet(&ctx, &rt, &query, client, Transport::Udp, &mut out),
            FastOutcome::Reply(_)
        ));
    }
    ARMED.with(|a| a.set(true));
    for _ in 0..50_000 {
        let rt = shared.runtime.load();
        match handle_packet(&ctx, &rt, &query, client, Transport::Udp, &mut out) {
            FastOutcome::Reply(n) => assert!(n > 40),
            _ => panic!("expected cache hit"),
        }
    }
    ARMED.with(|a| a.set(false));
    assert_eq!(
        ALLOCS.load(Ordering::Relaxed),
        0,
        "cache-hit path allocated"
    );
    assert!(shared.metrics.sum_cache_hits() >= 50_000);
    assert!(!shared.querylog.is_empty(), "query records were pushed");
}
