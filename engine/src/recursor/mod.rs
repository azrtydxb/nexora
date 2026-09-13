//! Iterative recursion, DNSSEC validation and RPZ (M3).

pub mod budget;
pub mod dnssec;
pub mod infra;
pub mod iterate;
pub mod metrics;
pub mod roothints;
pub mod rpz;
pub mod rrcache;
pub mod transport;

#[cfg(test)]
mod infra_tests;
#[cfg(test)]
mod iterate_tests;
#[cfg(test)]
pub(crate) mod testnet;

use std::time::Duration;

/// Deadline for resolving one client query in recursive mode.
pub const RESOLUTION_DEADLINE: Duration = Duration::from_millis(4000);
/// Advertised EDNS0 UDP payload size on outgoing queries.
pub const EDNS_BUFFER: u16 = 1232;
/// Maximum CNAME/DNAME chain length followed for one query.
pub const MAX_CNAME_DEPTH: u8 = 16;
/// `RecursionConfig.max_upstream_queries` when unset.
pub const DEFAULT_MAX_UPSTREAM_QUERIES: u32 = 100;
/// `RecursionConfig.max_delegation_depth` when unset.
pub const DEFAULT_MAX_DELEGATION_DEPTH: u32 = 32;

/// Resolution futures are `!Send` (they run on the per-worker `current_thread` runtimes).
pub type LocalBoxFuture<'a, T> = std::pin::Pin<Box<dyn std::future::Future<Output = T> + 'a>>;
