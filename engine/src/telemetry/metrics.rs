//! Per-worker cache-padded counters, summed at scrape time.

use crate::edns::Transport;
use crossbeam_utils::CachePadded;
use std::array;
use std::sync::Arc;
use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};

pub const DURATION_BOUNDS_US: [u64; 15] = [
    50, 100, 250, 500, 1_000, 2_500, 5_000, 10_000, 25_000, 50_000, 100_000, 250_000, 500_000,
    1_000_000, 2_000_000,
];
/// Rcodes 0..=5, then "other".
pub const RCODE_SLOTS: usize = 7;
pub const TRANSPORT_SLOTS: usize = 2;

type Counter = CachePadded<AtomicU64>;

fn counter() -> Counter {
    CachePadded::new(AtomicU64::new(0))
}

pub struct WorkerCounters {
    pub queries: [[Counter; RCODE_SLOTS]; TRANSPORT_SLOTS],
    /// Non-cumulative; slot 15 is +Inf.
    pub duration_buckets: [Counter; 16],
    pub duration_sum_us: Counter,
    pub cache_hits: Counter,
    pub cache_misses: Counter,
    pub stale_served: Counter,
    pub filter_blocked: Counter,
    pub mismatched_replies: Arc<Counter>,
}

impl WorkerCounters {
    fn new() -> WorkerCounters {
        WorkerCounters {
            queries: array::from_fn(|_| array::from_fn(|_| counter())),
            duration_buckets: array::from_fn(|_| counter()),
            duration_sum_us: counter(),
            cache_hits: counter(),
            cache_misses: counter(),
            stale_served: counter(),
            filter_blocked: counter(),
            mismatched_replies: Arc::new(counter()),
        }
    }

    pub fn observe(&self, t: Transport, rcode: u8, duration_us: u64) {
        let transport = match t {
            Transport::Udp => 0,
            Transport::Tcp => 1,
        };
        let slot = usize::from(rcode).min(RCODE_SLOTS - 1);
        self.queries[transport][slot].fetch_add(1, Ordering::Relaxed);
        let bucket = DURATION_BOUNDS_US
            .iter()
            .position(|&b| duration_us <= b)
            .unwrap_or(DURATION_BOUNDS_US.len());
        self.duration_buckets[bucket].fetch_add(1, Ordering::Relaxed);
        self.duration_sum_us
            .fetch_add(duration_us, Ordering::Relaxed);
    }
}

#[derive(Clone, Copy)]
pub enum Signal {
    Logs = 0,
    Traces = 1,
    Metrics = 2,
}

pub struct Metrics {
    pub workers: Box<[WorkerCounters]>,
    pub export_dropped: [Counter; 3],
    pub config_version: AtomicU64,
    pub control_connected: AtomicBool,
}

impl Metrics {
    pub fn new(workers: usize) -> Metrics {
        Metrics {
            workers: (0..workers.max(1)).map(|_| WorkerCounters::new()).collect(),
            export_dropped: array::from_fn(|_| counter()),
            config_version: AtomicU64::new(0),
            control_connected: AtomicBool::new(false),
        }
    }

    fn sum(&self, f: impl Fn(&WorkerCounters) -> u64) -> u64 {
        self.workers.iter().map(f).sum()
    }

    pub fn sum_queries(&self) -> u64 {
        self.sum(|w| {
            w.queries
                .iter()
                .flatten()
                .map(|c| c.load(Ordering::Relaxed))
                .sum()
        })
    }

    pub fn sum_cache_hits(&self) -> u64 {
        self.sum(|w| w.cache_hits.load(Ordering::Relaxed))
    }

    pub fn sum_cache_misses(&self) -> u64 {
        self.sum(|w| w.cache_misses.load(Ordering::Relaxed))
    }

    pub fn dropped(&self, s: Signal) -> u64 {
        self.export_dropped[s as usize].load(Ordering::Relaxed)
    }
}
