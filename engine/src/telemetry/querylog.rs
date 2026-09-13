//! Fixed-size query records pushed lock-free by workers, drained by telemetry.

use super::metrics::{Metrics, Signal};
use crate::edns::Transport;
use crate::wire::NameKey;
use crossbeam_queue::ArrayQueue;
use std::net::IpAddr;
use std::sync::atomic::Ordering;

pub const RING_CAPACITY: usize = 65536;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum CacheOutcome {
    Hit,
    Miss,
    Stale,
    None,
}

impl CacheOutcome {
    pub fn as_str(self) -> &'static str {
        match self {
            CacheOutcome::Hit => "hit",
            CacheOutcome::Miss => "miss",
            CacheOutcome::Stale => "stale",
            CacheOutcome::None => "none",
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum FilterOutcome {
    None,
    Blocked,
    Allowed,
}

impl FilterOutcome {
    pub fn as_str(self) -> &'static str {
        match self {
            FilterOutcome::None => "none",
            FilterOutcome::Blocked => "blocked",
            FilterOutcome::Allowed => "allowed",
        }
    }
}

/// Stage offsets are microseconds from the query's arrival.
#[derive(Clone, Copy)]
pub struct QueryRecord {
    pub unix_micros: u64,
    pub client: IpAddr,
    pub name: NameKey,
    pub qtype: u16,
    pub rcode: u8,
    pub cache: CacheOutcome,
    pub filter: FilterOutcome,
    /// Index into the runtime's upstreams; `u8::MAX` when none was used.
    pub upstream: u8,
    pub config_version: u64,
    pub transport: Transport,
    pub filter_us: u32,
    pub cache_us: u32,
    pub upstream_start_us: u32,
    pub upstream_us: u32,
    pub duration_us: u32,
}

/// Pushes `r`, counting it as a dropped log when the ring is full.
pub fn push(ring: &ArrayQueue<QueryRecord>, metrics: &Metrics, r: QueryRecord) {
    if ring.push(r).is_err() {
        metrics.export_dropped[Signal::Logs as usize].fetch_add(1, Ordering::Relaxed);
    }
}
