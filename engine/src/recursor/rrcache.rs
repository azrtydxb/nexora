//! RRset cache for iterative resolution, ranked by RFC 2181 §5.4.1 credibility and carrying the
//! DNSSEC status the validator assigned.

use super::memory::record_bytes;
use hickory_proto::rr::{Name, Record, RecordType};
use quick_cache::Weighter;
use quick_cache::sync::Cache;
use std::sync::Arc;

/// Longest time any RRset stays cached, whatever its TTL.
const MAX_TTL: u64 = 86_400;
/// Longest time a bogus RRset stays cached (avoids re-validating it on every query).
const BOGUS_TTL: u64 = 60;

#[derive(Clone, Copy, Debug, PartialEq, Eq, PartialOrd, Ord)]
pub enum Credibility {
    Additional = 1,
    Glue = 2,
    AuthorityNonAa = 3,
    AnswerNonAa = 4,
    AuthorityAa = 5,
    AnswerAa = 6,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum DnssecStatus {
    Unchecked,
    Secure,
    Insecure,
    Bogus,
}

#[derive(Clone, Debug)]
pub struct CachedRrset {
    pub records: Vec<Record>,
    pub rrsigs: Vec<Record>,
    pub expires_at: u64,
    pub credibility: Credibility,
    pub dnssec: DnssecStatus,
}

/// Weighs an RRset by its estimated bytes.
#[derive(Clone)]
struct RrsetWeight;

impl Weighter<(Name, RecordType), Arc<CachedRrset>> for RrsetWeight {
    fn weight(&self, key: &(Name, RecordType), val: &Arc<CachedRrset>) -> u64 {
        let records: u64 = val
            .records
            .iter()
            .chain(&val.rrsigs)
            .map(record_bytes)
            .sum();
        64 + key.0.len() as u64 + records
    }
}

pub struct RrCache {
    /// Keyed by lowercase owner name and type.
    entries: Cache<(Name, RecordType), Arc<CachedRrset>, RrsetWeight>,
}

impl RrCache {
    /// A cache holding at most `capacity_bytes` of estimated RRset bytes.
    pub fn new(capacity_bytes: u64) -> Self {
        Self {
            entries: Cache::with_weighter(
                (capacity_bytes / 512).max(64) as usize,
                capacity_bytes,
                RrsetWeight,
            ),
        }
    }

    /// Caches one RRset (all `records` share owner and type). Returns false, caching nothing, for
    /// an empty set, a TTL of 0, or when an unexpired entry of higher credibility exists.
    pub fn insert(
        &self,
        records: Vec<Record>,
        rrsigs: Vec<Record>,
        credibility: Credibility,
        dnssec: DnssecStatus,
        now: u64,
    ) -> bool {
        let Some(first) = records.first() else {
            return false;
        };
        let ttl = records.iter().map(|r| u64::from(r.ttl)).min().unwrap_or(0);
        if ttl == 0 {
            return false;
        }
        let key = (first.name.to_lowercase(), first.record_type());
        if let Some(existing) = self.entries.get(&key)
            && now < existing.expires_at
            && existing.credibility > credibility
        {
            return false;
        }
        let cap = if dnssec == DnssecStatus::Bogus {
            BOGUS_TTL
        } else {
            MAX_TTL
        };
        let entry = CachedRrset {
            records,
            rrsigs,
            expires_at: now + ttl.min(cap),
            credibility,
            dnssec,
        };
        self.entries.insert(key, Arc::new(entry));
        true
    }

    /// An unexpired RRset usable as data: glue and additional-section records are never returned.
    pub fn get(&self, name: &Name, rtype: RecordType, now: u64) -> Option<Arc<CachedRrset>> {
        self.get_glue(name, rtype, now)
            .filter(|e| e.credibility > Credibility::Glue)
    }

    /// An unexpired RRset of any credibility (used for nameserver addresses).
    pub fn get_glue(&self, name: &Name, rtype: RecordType, now: u64) -> Option<Arc<CachedRrset>> {
        self.entries
            .get(&(name.to_lowercase(), rtype))
            .filter(|e| now < e.expires_at)
    }

    pub fn set_dnssec(&self, name: &Name, rtype: RecordType, status: DnssecStatus) {
        let key = (name.to_lowercase(), rtype);
        if let Some(existing) = self.entries.get(&key) {
            let mut updated = (*existing).clone();
            updated.dnssec = status;
            self.entries.insert(key, Arc::new(updated));
        }
    }

    pub fn len(&self) -> usize {
        self.entries.len()
    }

    /// Total weight of the cached RRsets (estimated bytes).
    pub fn weight(&self) -> u64 {
        self.entries.weight()
    }

    /// Sets the byte budget; entries above it are evicted at once.
    pub fn set_capacity(&self, capacity_bytes: u64) {
        self.entries.set_capacity(capacity_bytes)
    }

    pub fn is_empty(&self) -> bool {
        self.entries.is_empty()
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use hickory_proto::rr::{RData, rdata::A};

    fn a(owner: &str, i: u32) -> Record {
        Record::from_rdata(
            Name::from_ascii(owner).unwrap(),
            300,
            RData::A(A::from(std::net::Ipv4Addr::from(i))),
        )
    }

    #[test]
    fn rrset_cache_is_bounded_by_bytes() {
        let cache = RrCache::new(256 << 10);
        for i in 0..20_000u32 {
            cache.insert(
                vec![a(&format!("n{i}.example."), i)],
                vec![],
                Credibility::AnswerAa,
                DnssecStatus::Unchecked,
                1_000,
            );
        }
        assert!(
            cache.len() < 20_000,
            "entries are weighed in bytes, not counted: {}",
            cache.len()
        );
        assert!(
            cache.weight() <= 256 << 10,
            "weight {} above 256 KiB",
            cache.weight()
        );
        cache.set_capacity(64 << 10);
        assert!(cache.weight() <= 64 << 10, "set_capacity evicts at once");
    }
}
