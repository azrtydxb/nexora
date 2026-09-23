//! M11 capacity reports use the M7 caches' maintained eviction weights.
use hickory_proto::dnssec::rdata::{DNSSECRData, NSEC};
use hickory_proto::rr::rdata::{A, SOA};
use hickory_proto::rr::{Name, RData, Record, RecordType};
use nexora_engine::proto::{RecursionConfig, Stats};
use nexora_engine::recursor::RecursorState;
use nexora_engine::recursor::dispatch::{ForwardZone, Mode};
use nexora_engine::recursor::iterate::RecursionParams;
use nexora_engine::recursor::memory::{DEFAULT_CACHE_MAX_BYTES, record_bytes};
use nexora_engine::recursor::rrcache::{Credibility, DnssecStatus};
use nexora_engine::runtime::Runtime;
use nexora_engine::server::Shared;
use prost::Message;
use std::alloc::{GlobalAlloc, Layout, System};
use std::cell::Cell;
use std::sync::Arc;
use std::time::Duration;

struct Counting;
thread_local! {
    static ARMED: Cell<bool> = const { Cell::new(false) };
    static ALLOCS: Cell<u64> = const { Cell::new(0) };
}
fn count() {
    if ARMED.try_with(Cell::get).unwrap_or(false) {
        let _ = ALLOCS.try_with(|n| n.set(n.get() + 1));
    }
}
unsafe impl GlobalAlloc for Counting {
    unsafe fn alloc(&self, layout: Layout) -> *mut u8 {
        count();
        unsafe { System.alloc(layout) }
    }
    unsafe fn dealloc(&self, ptr: *mut u8, layout: Layout) {
        unsafe { System.dealloc(ptr, layout) }
    }
    unsafe fn realloc(&self, ptr: *mut u8, layout: Layout, size: usize) -> *mut u8 {
        count();
        unsafe { System.realloc(ptr, layout, size) }
    }
}
#[global_allocator]
static ALLOCATOR: Counting = Counting;

fn populate(state: &RecursorState) -> (u64, u64, u64) {
    let name = Name::from_ascii("example.test.").unwrap();
    let record = Record::from_rdata(name.clone(), 300, RData::A(A::new(192, 0, 2, 1)));
    let rr_bytes = 64 + name.len() as u64 + record_bytes(&record);
    assert!(state.recursor.rrcache.insert(
        vec![record],
        vec![],
        Credibility::AnswerAa,
        DnssecStatus::Unchecked,
        1000,
    ));
    // Both the server cache and the name-dependent lame cache contribute their own weights.
    let ip = "192.0.2.53".parse().unwrap();
    state
        .recursor
        .infra
        .record_rtt(ip, Duration::from_millis(10));
    state.recursor.infra.mark_lame(ip, &name, 1000);
    let infra_bytes = 128 + 64 + name.len() as u64;
    let soa = Record::from_rdata(
        name.clone(),
        300,
        RData::SOA(SOA::new(
            name.clone(),
            name.clone(),
            1,
            3600,
            600,
            86400,
            300,
        )),
    );
    let denial = Record::from_rdata(
        name.clone(),
        300,
        RData::DNSSEC(DNSSECRData::NSEC(NSEC::new(
            Name::from_ascii("next.example.test.").unwrap(),
            [RecordType::SOA, RecordType::NSEC, RecordType::RRSIG],
        ))),
    );
    let nsec_bytes = 64 + record_bytes(&soa) + 64 + record_bytes(&denial);
    state
        .validator
        .nsec
        .insert_secure(&name, &[soa], &[denial], 1000);
    assert_eq!(state.recursor.rrcache.weight(), rr_bytes);
    assert_eq!(state.recursor.infra.weight(), infra_bytes);
    assert_eq!(state.validator.nsec.weight(), nsec_bytes);
    (rr_bytes, infra_bytes, nsec_bytes)
}

#[test]
fn recursor_cache_readout_uses_weights_without_allocating() {
    let state = RecursorState::new(None);
    assert_eq!(state.cache_bytes(), 0);
    let (rr, infra, nsec) = populate(&state);
    ALLOCS.with(|n| n.set(0));
    ARMED.with(|armed| armed.set(true));
    let mut observed = 0;
    for _ in 0..100 {
        observed = std::hint::black_box(state.cache_bytes());
    }
    ARMED.with(|armed| armed.set(false));
    assert_eq!(observed, rr + infra + nsec);
    assert_eq!(ALLOCS.with(Cell::get), 0, "readout allocated");
    state.validator.nsec.set_capacity(0);
    assert_eq!(state.cache_bytes(), rr + infra);
    state.recursor.rrcache.set_capacity(0);
    state.recursor.infra.set_capacity(0);
    assert_eq!(state.cache_bytes(), 0, "evictions update the readout");
}

#[test]
fn recursor_cache_bytes_change_when_an_rrset_grows_without_adding_entries() {
    let state = RecursorState::new(None);
    let (rr, infra, nsec) = populate(&state);
    let name = Name::from_ascii("example.test.").unwrap();
    let records = vec![
        Record::from_rdata(name.clone(), 300, RData::A(A::new(192, 0, 2, 1))),
        Record::from_rdata(name, 300, RData::A(A::new(192, 0, 2, 2))),
    ];
    let added_bytes = record_bytes(&records[1]);
    assert_eq!(state.recursor.rrcache.len(), 1);
    assert!(state.recursor.rrcache.insert(
        records,
        vec![],
        Credibility::AnswerAa,
        DnssecStatus::Unchecked,
        1000,
    ));
    assert_eq!(state.recursor.rrcache.len(), 1);
    assert_eq!(state.cache_bytes(), rr + infra + nsec + added_bytes);
}

#[test]
fn recursor_cache_stats_report_enabled_zero_and_effective_limit() {
    let shared = Shared::new(1);
    let mut rt = Runtime::initial();
    let stats = |rt: &Runtime| {
        let stats = shared.metrics.stats(rt, &shared.recursor);
        Stats::decode(stats.encode_to_vec().as_slice())
            .unwrap()
            .recursor_cache
            .expect("new engine must report presence even when disabled")
    };
    let disabled = stats(&rt);
    assert!(!disabled.enabled);
    assert_eq!(disabled.bytes, 0);
    assert_eq!(disabled.max_bytes, DEFAULT_CACHE_MAX_BYTES);
    Arc::get_mut(&mut rt.resolution).unwrap().mode = Mode::Recursive;
    let empty = stats(&rt);
    assert!(empty.enabled);
    assert_eq!(empty.bytes, 0);
    assert_eq!(empty.max_bytes, DEFAULT_CACHE_MAX_BYTES);
    let (rr, infra, nsec) = populate(&shared.recursor);
    for limit in [4 << 20, 128 << 20, 16 << 30, 0] {
        Arc::get_mut(&mut rt.resolution).unwrap().params =
            Arc::new(RecursionParams::from_config(Some(&RecursionConfig {
                cache_max_bytes: limit,
                ..Default::default()
            })));
        shared.recursor.sync(&rt);
        let report = stats(&rt);
        assert!(report.enabled);
        assert_eq!(report.bytes, rr + infra + nsec, "resize retains entries");
        assert_eq!(
            report.max_bytes,
            if limit == 0 {
                DEFAULT_CACHE_MAX_BYTES
            } else {
                limit
            }
        );
    }
    Arc::get_mut(&mut rt.resolution).unwrap().mode = Mode::Forward;
    let disabled = stats(&rt);
    assert!(!disabled.enabled);
    assert_eq!(
        disabled.bytes,
        rr + infra + nsec,
        "disabled does not fabricate zero bytes"
    );
    // DNSSEC validation of forwarding responses also uses the budgeted caches.
    let dnssec = Arc::make_mut(&mut Arc::get_mut(&mut rt.resolution).unwrap().dnssec);
    dnssec.validation = true;
    dnssec.validate_forwarded = true;
    assert!(stats(&rt).enabled);
    Arc::make_mut(&mut Arc::get_mut(&mut rt.resolution).unwrap().dnssec).validate_forwarded = false;
    assert!(!stats(&rt).enabled);
    Arc::get_mut(&mut rt.resolution)
        .unwrap()
        .forward_zones
        .zones
        .push(ForwardZone {
            domain: Name::from_ascii("example.test.").unwrap(),
            servers: vec!["192.0.2.53:53".parse().unwrap()],
            validate: true,
        });
    assert!(
        stats(&rt).enabled,
        "a validating forward zone uses the budget too"
    );
    Arc::make_mut(&mut Arc::get_mut(&mut rt.resolution).unwrap().dnssec).validation = false;
    assert!(!stats(&rt).enabled, "global DNSSEC disablement wins");
    assert!(Stats::decode(&[][..]).unwrap().recursor_cache.is_none());
}
