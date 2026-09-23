//! Identity clones/rejection/drop may release small metadata, never a rule generation.
use std::alloc::{GlobalAlloc, Layout, System};
use std::cell::Cell;
use std::sync::Arc;

struct Counting;
thread_local! {
    static ARMED: Cell<bool> = const { Cell::new(false) };
    static ALLOCS: Cell<usize> = const { Cell::new(0) };
    static FREED: Cell<usize> = const { Cell::new(0) };
}
unsafe impl GlobalAlloc for Counting {
    unsafe fn alloc(&self, layout: Layout) -> *mut u8 {
        if ARMED.try_with(Cell::get).unwrap_or(false) {
            let _ = ALLOCS.try_with(|n| n.set(n.get() + 1));
        }
        unsafe { System.alloc(layout) }
    }
    unsafe fn dealloc(&self, ptr: *mut u8, layout: Layout) {
        if ARMED.try_with(Cell::get).unwrap_or(false) {
            let _ = FREED.try_with(|n| n.set(n.get() + layout.size()));
        }
        unsafe { System.dealloc(ptr, layout) }
    }
}
#[global_allocator]
static GLOBAL: Counting = Counting;

#[test]
fn worker_capture_rejection_and_last_drop_own_only_small_identities() {
    use hickory_proto::op::{Message, MessageType, OpCode, Query};
    use hickory_proto::rr::{Name, RecordType};
    use hickory_proto::serialize::binary::BinEncodable;
    use nexora_engine::edns::Transport;
    use nexora_engine::filter::index::{IndexOptions, ListInput, ListKind};
    use nexora_engine::filter::{BlockMode, BlockReply, FilterIndex, PolicyTable};
    use nexora_engine::recursor::rpz::{
        index::{RpzSet, RpzZoneIndex},
        parse::parse_rpz_text,
    };
    use nexora_engine::runtime::Runtime;
    use nexora_engine::server::{FastOutcome, Shared, WorkerCtx, handle_packet};
    use nexora_engine::telemetry::{metrics::Signal, querylog::push};

    let shared = Shared::new(1);
    let text: String = (0..20_000)
        .map(|i| format!("n{i}.blocked.test\n"))
        .collect();
    let index = Arc::new(
        FilterIndex::build(
            &[ListInput {
                id: "large-list",
                category: "ads",
                category_slot: 1,
                kind: ListKind::Block,
                text: text.as_bytes(),
            }],
            &IndexOptions::new(64 << 20),
        )
        .unwrap(),
    );
    let retired = Arc::downgrade(&index);
    let mut rt = Runtime::initial();
    rt.acl = nexora_engine::acl::Acl::any();
    rt.policy = PolicyTable::global_only(
        Arc::new(index.view(&[0], &[])),
        BlockReply {
            mode: BlockMode::NxDomain,
            ttl: 60,
        },
    );
    rt.filter_index = index;
    shared.runtime.store(Arc::new(rt));
    let ctx = WorkerCtx::new(0, shared.clone());
    let mut q = Message::new(1, MessageType::Query, OpCode::Query);
    q.metadata.recursion_desired = true;
    q.add_query(Query::query(
        Name::from_ascii("n1.blocked.test.").unwrap(),
        RecordType::A,
    ));
    let wire = q.to_bytes().unwrap();
    let mut out = [0; 1232];
    let rt = shared.runtime.load_full();
    let mut query = || {
        handle_packet(
            &ctx,
            &rt,
            &wire,
            "127.0.0.1:53000".parse().unwrap(),
            Transport::Udp,
            &mut out,
        )
    };
    assert!(matches!(query(), FastOutcome::Reply(_))); // warm thread/cache state
    drop(shared.querylog.pop().unwrap());
    ARMED.with(|a| a.set(true));
    let result = query();
    ARMED.with(|a| a.set(false));
    assert!(matches!(result, FastOutcome::Reply(_)));
    assert_eq!(
        ALLOCS.with(Cell::get),
        0,
        "worker identity capture allocated"
    );
    let mut record = shared.querylog.pop().unwrap();
    let list_identity = Arc::downgrade(record.filter_identity.as_ref().unwrap());
    drop(rt);
    shared.runtime.store(Arc::new(Runtime::initial()));
    assert!(
        retired.upgrade().is_none(),
        "event retained the large filter generation"
    );

    let rules: String = (0..20_000)
        .map(|i| format!("n{i}.test CNAME .\n"))
        .collect();
    let parsed = parse_rpz_text(
        &Name::from_ascii("rpz.test.").unwrap(),
        &format!("$TTL 60\n@ SOA ns h 1 60 60 60 60\n{rules}"),
    )
    .unwrap();
    let zone = Arc::new(RpzZoneIndex::build("large-zone", &parsed, 0));
    let retired_zone = Arc::downgrade(&zone);
    let zone_identity = Arc::downgrade(&zone.identity);
    record.rpz_identity = Some(zone.identity.clone());
    shared.recursor.rpz.publish(RpzSet::new(vec![zone]));
    shared.recursor.rpz.publish(RpzSet::default());
    assert!(
        retired_zone.upgrade().is_none(),
        "event retained the large RPZ generation"
    );

    let ring = crossbeam_queue::ArrayQueue::new(1);
    ALLOCS.with(|n| n.set(0));
    FREED.with(|n| n.set(0));
    ARMED.with(|a| a.set(true));
    push(&ring, &shared.metrics, record.clone());
    push(&ring, &shared.metrics, record); // full: rejected record is dropped here
    drop(ring.pop()); // last owners: metadata alone is destroyed on this thread
    ARMED.with(|a| a.set(false));
    assert_eq!(ALLOCS.with(Cell::get), 0);
    assert!(
        FREED.with(Cell::get) < 4096,
        "drop released more than small identities"
    );
    assert_eq!(shared.metrics.dropped(Signal::Logs), 1);
    assert!(list_identity.upgrade().is_none());
    assert!(zone_identity.upgrade().is_none());
}
