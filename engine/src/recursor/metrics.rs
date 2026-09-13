//! Counters for recursion, DNSSEC validation and trust-anchor maintenance.

use std::sync::atomic::{AtomicU64, Ordering};

pub struct RecursorMetrics {
    pub upstream_queries: AtomicU64,
    pub upstream_timeouts: AtomicU64,
    pub mismatched_id: AtomicU64,
    pub mismatched_question: AtomicU64,
    pub mismatched_case: AtomicU64,
    pub malformed_replies: AtomicU64,
    pub tcp_fallbacks: AtomicU64,
    pub edns_fallbacks: AtomicU64,
    pub lame_marked: AtomicU64,
    pub limit_queries: AtomicU64,
    pub limit_delegation_depth: AtomicU64,
    pub limit_cname_depth: AtomicU64,
    pub cname_loops: AtomicU64,
    pub resolutions_recursive: AtomicU64,
    pub resolutions_forward_zone: AtomicU64,
    pub resolution_failures: AtomicU64,
    pub dnssec_secure: AtomicU64,
    pub dnssec_insecure: AtomicU64,
    pub dnssec_bogus: AtomicU64,
    pub dnssec_indeterminate: AtomicU64,
    /// Indexed by EDE INFO-CODE (RFC 8914 codes 0..=31).
    pub dnssec_bogus_by_ede: [AtomicU64; 32],
    pub dnssec_aggressive_synthesized: AtomicU64,
    pub trust_anchor_refresh_failures: AtomicU64,
}

impl Default for RecursorMetrics {
    fn default() -> Self {
        Self {
            upstream_queries: AtomicU64::new(0),
            upstream_timeouts: AtomicU64::new(0),
            mismatched_id: AtomicU64::new(0),
            mismatched_question: AtomicU64::new(0),
            mismatched_case: AtomicU64::new(0),
            malformed_replies: AtomicU64::new(0),
            tcp_fallbacks: AtomicU64::new(0),
            edns_fallbacks: AtomicU64::new(0),
            lame_marked: AtomicU64::new(0),
            limit_queries: AtomicU64::new(0),
            limit_delegation_depth: AtomicU64::new(0),
            limit_cname_depth: AtomicU64::new(0),
            cname_loops: AtomicU64::new(0),
            resolutions_recursive: AtomicU64::new(0),
            resolutions_forward_zone: AtomicU64::new(0),
            resolution_failures: AtomicU64::new(0),
            dnssec_secure: AtomicU64::new(0),
            dnssec_insecure: AtomicU64::new(0),
            dnssec_bogus: AtomicU64::new(0),
            dnssec_indeterminate: AtomicU64::new(0),
            dnssec_bogus_by_ede: std::array::from_fn(|_| AtomicU64::new(0)),
            dnssec_aggressive_synthesized: AtomicU64::new(0),
            trust_anchor_refresh_failures: AtomicU64::new(0),
        }
    }
}

impl RecursorMetrics {
    pub fn inc(c: &AtomicU64) {
        c.fetch_add(1, Ordering::Relaxed);
    }
}
