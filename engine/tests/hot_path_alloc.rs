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
    use nexora_engine::proto::*;
    use nexora_engine::server::Shared;
    use nexora_engine::snapshot::{DirBlobs, apply};

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
    // Forward mode (M1/M2), then recursive mode with DNSSEC validation (M3): a cache hit takes the
    // same allocation-free path in both.
    let mut recursive = snap.clone();
    recursive.version = 2;
    recursive.resolution_mode = ResolutionMode::Recursive as i32;
    recursive.dnssec = Some(DnssecConfig {
        validation: true,
        rfc5011: true,
        ..Default::default()
    });
    for snap in [snap, recursive] {
        assert!(matches!(
            apply(
                &shared.runtime,
                snap,
                &DirBlobs {
                    dir: tmp.path().into(),
                },
                None,
            ),
            nexora_engine::snapshot::ApplyOutcome::Applied { .. }
        ));
        measure(&shared);
    }
    // With RPZ query triggers loaded, a non-matching name still hits the cache without allocating.
    use nexora_engine::recursor::rpz::{index, parse};
    let origin = hickory_proto::rr::Name::from_ascii("rpz.test.").unwrap();
    let zone = parse::parse_rpz_text(
        &origin,
        "$TTL 60\n@ SOA ns h 1 60 60 60 60\nbad.example CNAME .\n32.9.0.0.10.rpz-client-ip CNAME .\n",
    )
    .unwrap();
    shared
        .recursor
        .rpz
        .publish(index::RpzSet::new(vec![std::sync::Arc::new(
            index::RpzZoneIndex::build("z", &zone, 0),
        )]));
    measure(&shared);
}

fn measure(shared: &std::sync::Arc<nexora_engine::server::Shared>) {
    use hickory_proto::op::{Edns, Message, MessageType, OpCode, Query};
    use hickory_proto::rr::{Name, RData, Record, RecordType, rdata::A, rdata::opt::EdnsOption};
    use hickory_proto::serialize::binary::{BinDecodable, BinEncodable};
    use nexora_engine::cache::CacheKey;
    use nexora_engine::edns::Transport;
    use nexora_engine::server::{FastOutcome, WorkerCtx, handle_packet};
    use nexora_engine::wire::parse_query;

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
    let before_hits = shared.metrics.sum_cache_hits();
    let mut out = [0u8; 1232];
    for _ in 0..64 {
        let rt = shared.runtime.load();
        assert!(matches!(
            handle_packet(&ctx, &rt, &query, client, Transport::Udp, &mut out),
            FastOutcome::Reply(_)
        ));
    }
    ALLOCS.store(0, Ordering::Relaxed);
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
        "cache-hit path allocated (resolution mode {:?})",
        shared.runtime.load().resolution.mode
    );
    assert!(shared.metrics.sum_cache_hits() >= before_hits + 50_000);
    assert!(!shared.querylog.is_empty(), "query records were pushed");
}
