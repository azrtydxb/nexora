//! The recursor's memory budget (`RecursionConfig.cache_max_bytes`) and its split over the caches.

use hickory_proto::rr::Record;

pub const DEFAULT_CACHE_MAX_BYTES: u64 = 64 << 20;
pub const MIN_CACHE_MAX_BYTES: u64 = 4 << 20;
pub const MAX_CACHE_MAX_BYTES: u64 = 16 << 30;

/// Bytes of the budget given to each cache.
pub struct Shares {
    pub rrset: u64,
    pub nsec: u64,
    pub infra: u64,
}

/// 12/16 to RRsets, 3/16 to aggressive NSEC, 1/16 to the infrastructure cache; 0 is the default.
pub fn shares(cache_max_bytes: u64) -> Shares {
    let total = effective_max_bytes(cache_max_bytes);
    Shares {
        rrset: total / 16 * 12,
        nsec: total / 16 * 3,
        infra: total / 16,
    }
}

/// The configured byte budget after resolving the backwards-compatible default.
pub fn effective_max_bytes(cache_max_bytes: u64) -> u64 {
    if cache_max_bytes == 0 {
        DEFAULT_CACHE_MAX_BYTES
    } else {
        cache_max_bytes
    }
}

/// Estimated heap and inline bytes of one cached record: an estimate for eviction, not an exact count.
pub fn record_bytes(r: &Record) -> u64 {
    (std::mem::size_of::<Record>() + r.name.len() + 32) as u64
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn zero_takes_the_default_and_shares_add_up() {
        let s = shares(0);
        assert_eq!((s.rrset, s.nsec, s.infra), (48 << 20, 12 << 20, 4 << 20));
        let s = shares(MIN_CACHE_MAX_BYTES);
        assert_eq!(s.rrset + s.nsec + s.infra, MIN_CACHE_MAX_BYTES);
    }
}
