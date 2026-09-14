//! Fixed-size query records pushed lock-free by workers, drained by telemetry.

use super::metrics::{Metrics, Signal};
use crate::edns::Transport;
use crate::wire::NameKey;
use crossbeam_queue::ArrayQueue;
use std::net::IpAddr;
use std::sync::atomic::Ordering;

pub const RING_CAPACITY: usize = 65536;
/// `QueryRecord.policy_group` of a client in no policy group.
pub const NO_POLICY_GROUP: u16 = u16::MAX;
/// `QueryRecord.filter_list` of a query no list blocked.
pub const NO_FILTER_LIST: u16 = u16::MAX;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum CacheOutcome {
    Hit,
    Miss,
    Stale,
    None,
    /// Answered from a hosted zone.
    Auth,
}

impl CacheOutcome {
    pub fn as_str(self) -> &'static str {
        match self {
            CacheOutcome::Hit => "hit",
            CacheOutcome::Miss => "miss",
            CacheOutcome::Stale => "stale",
            CacheOutcome::None => "none",
            CacheOutcome::Auth => "auth",
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum FilterOutcome {
    None,
    Blocked,
    Allowed,
    Rewritten,
}

impl FilterOutcome {
    pub fn as_str(self) -> &'static str {
        match self {
            FilterOutcome::None => "none",
            FilterOutcome::Blocked => "blocked",
            FilterOutcome::Allowed => "allowed",
            FilterOutcome::Rewritten => "rewritten",
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
    /// Index of the client's policy group; `NO_POLICY_GROUP` for global clients.
    pub policy_group: u16,
    pub transport: Transport,
    pub filter_us: u32,
    pub cache_us: u32,
    pub upstream_start_us: u32,
    pub upstream_us: u32,
    pub duration_us: u32,
    /// `recursor::dispatch::RouteTaken`: 0 forward, 1 recursive, 2 forward zone.
    pub route: u8,
    /// `recursor::dispatch::SecurityTag`: 0 none, 1 secure, 2 insecure, 3 bogus, 4 indeterminate.
    pub dnssec: u8,
    /// 0 none, else `RpzAction::log_code`.
    pub rpz_action: u8,
    /// The blocking list's index in the filter index of `filter_generation`; `NO_FILTER_LIST`
    /// unless blocked.
    pub filter_list: u16,
    pub filter_generation: u64,
}

/// `QueryRecord.route` names.
pub const ROUTE_NAMES: [&str; 3] = ["forward", "recursive", "forward_zone"];
/// `QueryRecord.dnssec` names.
pub const DNSSEC_NAMES: [&str; 5] = ["none", "secure", "insecure", "bogus", "indeterminate"];
/// `QueryRecord.rpz_action` names; 7 is `disabled` (a hit in a zone whose override is DISABLED).
pub const RPZ_NAMES: [&str; 8] = [
    "none",
    "nxdomain",
    "nodata",
    "passthru",
    "drop",
    "tcp_only",
    "local_data",
    "disabled",
];

/// The name at `index`, or the first one when out of range.
pub fn name_at(names: &[&'static str], index: u8) -> &'static str {
    names.get(usize::from(index)).copied().unwrap_or(names[0])
}

/// Pushes `r`, counting it as a dropped log when the ring is full.
pub fn push(ring: &ArrayQueue<QueryRecord>, metrics: &Metrics, r: QueryRecord) {
    if ring.push(r).is_err() {
        metrics.export_dropped[Signal::Logs as usize].fetch_add(1, Ordering::Relaxed);
    }
}
