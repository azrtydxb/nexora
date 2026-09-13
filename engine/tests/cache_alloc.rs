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
fn cache_lookup_and_serve_do_not_allocate() {
    use hickory_proto::op::{Message, MessageType, OpCode, Query};
    use hickory_proto::rr::rdata::A;
    use hickory_proto::rr::{Name, RData, Record, RecordType};
    use hickory_proto::serialize::binary::{BinDecodable, BinEncodable};
    use nexora_engine::cache::*;
    use nexora_engine::wire::parse_query;

    let mut m = Message::new(9, MessageType::Query, OpCode::Query);
    m.metadata.recursion_desired = true;
    m.add_query(Query::query(
        Name::from_ascii("hot.example.").unwrap(),
        RecordType::A,
    ));
    let query = m.to_bytes().unwrap();
    let mut r = Message::from_bytes(&query).unwrap();
    r.metadata.message_type = MessageType::Response;
    r.add_answer(Record::from_rdata(
        Name::from_ascii("hot.example.").unwrap(),
        300,
        RData::A(A::new(192, 0, 2, 1)),
    ));
    let resp = r.to_bytes().unwrap();

    let cache = Cache::new(CacheSettings {
        max_bytes: 1 << 20,
        min_ttl: 0,
        max_ttl: 86400,
        negative_max_ttl: 3600,
        stale_window: 0,
    });
    let v = parse_query(&query).unwrap();
    cache.insert(CacheKey::from_query(&v), &resp, &v, 1);
    let mut out = [0u8; 1232];
    for _ in 0..16 {
        // warm up quick_cache internals
        if let Lookup::Fresh(e) = cache.lookup(&CacheKey::from_query(&v), 2) {
            write_cached(&e, &v, 2, ServeMode::Fresh, &mut out, 512, None);
        }
    }
    ARMED.with(|a| a.set(true));
    for _ in 0..10_000 {
        let v = parse_query(&query).unwrap();
        let Lookup::Fresh(e) = cache.lookup(&CacheKey::from_query(&v), 2) else {
            panic!("miss")
        };
        let n = write_cached(&e, &v, 2, ServeMode::Fresh, &mut out, 512, None);
        assert!(n > 12);
    }
    ARMED.with(|a| a.set(false));
    assert_eq!(
        ALLOCS.load(Ordering::Relaxed),
        0,
        "cache hit path allocated"
    );
}
