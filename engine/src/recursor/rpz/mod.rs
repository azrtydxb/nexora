//! Response Policy Zones (draft-vixie-dnsop-dns-rpz): parsing, trigger index, actions, zone
//! sources (snapshot blobs, AXFR/IXFR with TSIG) and the process-wide RPZ state.

pub mod apply;
pub mod index;
pub mod manager;
pub mod parse;
pub mod transfer;
pub use crate::tsig;

#[cfg(test)]
mod rpz_tests;
#[cfg(test)]
mod transfer_tests;

use crate::proto;
use arc_swap::ArcSwap;
use index::RpzSet;
use manager::RpzManager;
use parse::RpzAction;
use std::path::Path;
use std::sync::Arc;
use std::sync::atomic::{AtomicBool, Ordering};

/// The RPZ decision a miss carries from the query phase into resolution.
#[derive(Clone, Debug, Default)]
pub enum RpzPending {
    #[default]
    None,
    /// A query-phase hit that yields to earlier zones' response triggers.
    Deferred { zone: usize, action: RpzAction },
    /// A query-phase action that needs resolution (a local-data CNAME to chase).
    Apply { zone: usize, action: RpzAction },
}

/// RPZ state that survives snapshot swaps: the published zone set, the query-path switch and
/// the zone manager (which holds TSIG keys in memory only).
pub struct RpzState {
    pub set: ArcSwap<RpzSet>,
    /// `set.has_query_triggers`, readable with one atomic load on the cache-hit path.
    pub query_triggers: AtomicBool,
    pub manager: RpzManager,
}

impl RpzState {
    /// `state_dir: None` persists nothing (tests).
    pub fn new(state_dir: Option<&Path>) -> Self {
        RpzState {
            set: ArcSwap::from_pointee(RpzSet::default()),
            query_triggers: AtomicBool::new(false),
            manager: RpzManager::new(state_dir),
        }
    }

    /// Stores the set, then the query-trigger switch.
    pub fn publish(&self, set: RpzSet) {
        let query = set.has_query_triggers;
        self.set.store(Arc::new(set));
        self.query_triggers.store(query, Ordering::Relaxed);
    }

    /// Applies `ServerMessage.rpz_tsig_keys` (the complete key set).
    pub fn set_tsig_keys(&self, keys: proto::RpzTsigKeys) {
        self.manager.set_tsig_keys(keys);
    }
}
