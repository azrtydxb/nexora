//! RFC 8198 aggressive use of validated NSEC/NSEC3 records: negative answers synthesised from
//! denial records cached by earlier secure responses.

use super::denial::{
    Denial, closest_encloser, nsec_proves_nodata, nsec_proves_nxdomain, nsec3_hash,
    nsec3_owner_hash, nsec3_proves_nodata, nsec3_proves_nxdomain,
};
use super::verify::rrsig;
use crate::recursor::memory::record_bytes;
use crate::recursor::metrics::RecursorMetrics;
use hickory_proto::dnssec::rdata::{DNSSECRData, NSEC, NSEC3};
use hickory_proto::op::ResponseCode;
use hickory_proto::rr::{Name, RData, Record, RecordType};
use parking_lot::Mutex;
use std::collections::{BTreeMap, HashMap};
use std::sync::Arc;
use std::sync::atomic::{AtomicU64, Ordering};

/// Estimated fixed bytes of one entry or zone beside its records.
const OVERHEAD_BYTES: u64 = 64;

/// One NSEC/NSEC3 record with its RRSIGs.
struct Entry<T> {
    owner: Name,
    rdata: T,
    records: Vec<Record>,
    expires: u64,
    /// Estimated bytes (`OVERHEAD_BYTES` + the records).
    bytes: u64,
}

fn entry_bytes(records: &[Record]) -> u64 {
    OVERHEAD_BYTES + records.iter().map(record_bytes).sum::<u64>()
}

#[derive(Default)]
struct ZoneDenials {
    soa: Vec<Record>,
    soa_expires: u64,
    /// Keyed by the owner's lowercase labels from the root: canonical order (RFC 4034 §6.1).
    nsec: BTreeMap<Vec<Vec<u8>>, Entry<NSEC>>,
    /// Keyed by the binary owner hash.
    nsec3: BTreeMap<Vec<u8>, Entry<NSEC3>>,
    inserted: u64,
    /// Estimated bytes of the SOA and every entry.
    bytes: u64,
}

struct Zones {
    map: HashMap<Name, ZoneDenials>,
    /// Sum of the zones' bytes.
    bytes: u64,
}

impl Zones {
    /// Removes whole zones, oldest first, until the cache fits `capacity`; `keep` is never removed.
    fn evict(&mut self, capacity: u64, keep: Option<&Name>) {
        while self.bytes > capacity {
            let Some(oldest) = self
                .map
                .iter()
                .filter(|(z, _)| Some(*z) != keep)
                .min_by_key(|(_, d)| d.inserted)
                .map(|(z, _)| z.clone())
            else {
                return;
            };
            if let Some(d) = self.map.remove(&oldest) {
                self.bytes -= d.bytes;
            }
        }
    }
}

/// Byte-bounded: a zone takes entries while its own bytes fit the capacity, and whole zones are
/// evicted oldest first when the cache as a whole exceeds it.
pub struct AggressiveNsecCache {
    zones: Mutex<Zones>,
    capacity: AtomicU64,
    metrics: Arc<RecursorMetrics>,
}

fn canonical_key(name: &Name) -> Vec<Vec<u8>> {
    name.iter().rev().map(|l| l.to_ascii_lowercase()).collect()
}

/// The entry at or before `key`, wrapping to the last entry (the zone's final NSEC).
fn at_or_before<'a, K: Ord, T>(map: &'a BTreeMap<K, Entry<T>>, key: &K) -> Option<&'a Entry<T>> {
    map.range(..=key)
        .next_back()
        .or_else(|| map.iter().next_back())
        .map(|(_, e)| e)
}

fn push_unique<'a, T>(v: &mut Vec<&'a Entry<T>>, e: Option<&'a Entry<T>>, now: u64) {
    if let Some(e) = e
        && e.expires > now
        && !v.iter().any(|x| std::ptr::eq(*x, e))
    {
        v.push(e);
    }
}

impl AggressiveNsecCache {
    pub fn new(capacity_bytes: u64, metrics: Arc<RecursorMetrics>) -> Self {
        AggressiveNsecCache {
            zones: Mutex::new(Zones {
                map: HashMap::new(),
                bytes: 0,
            }),
            capacity: AtomicU64::new(capacity_bytes),
            metrics,
        }
    }

    /// Sets the byte budget; zones above it are evicted at once, oldest first.
    pub fn set_capacity(&self, capacity_bytes: u64) {
        self.capacity.store(capacity_bytes, Ordering::Relaxed);
        self.zones.lock().evict(capacity_bytes, None);
    }

    pub fn clear(&self) {
        let mut zones = self.zones.lock();
        zones.map.clear();
        zones.bytes = 0;
    }

    /// Existing eviction weight, read under the cache lock without copying records.
    pub fn weight(&self) -> u64 {
        self.zones.lock().bytes
    }

    /// Stores the SOA RRset and the denial records (with RRSIGs) of a validated negative
    /// response for `zone`. Opt-out NSEC3 records are never stored.
    pub fn insert_secure(
        &self,
        zone: &Name,
        soa: &[Record],
        denial_records: &[Record],
        now_unix: u64,
    ) {
        let Some((soa_ttl, minimum)) = soa.iter().find_map(|r| match &r.data {
            RData::SOA(s) => Some((r.ttl, s.minimum)),
            _ => None,
        }) else {
            return;
        };
        let negative_ttl = u64::from(soa_ttl.min(minimum));
        let zone = zone.to_lowercase();
        let capacity = self.capacity.load(Ordering::Relaxed);
        let mut guard = self.zones.lock();
        let zones = &mut *guard;
        let d = zones.map.entry(zone.clone()).or_default();
        let before = d.bytes;
        d.inserted = now_unix;
        // a zone's bytes: OVERHEAD_BYTES with its SOA, plus its entries
        d.bytes = d.bytes.saturating_sub(entry_bytes(&d.soa)) + entry_bytes(soa);
        d.soa = soa.to_vec();
        d.soa_expires = now_unix + negative_ttl;
        let mut freed = 0;
        d.nsec.retain(|_, e| {
            let keep = e.expires > now_unix;
            if !keep {
                freed += e.bytes;
            }
            keep
        });
        d.nsec3.retain(|_, e| {
            let keep = e.expires > now_unix;
            if !keep {
                freed += e.bytes;
            }
            keep
        });
        d.bytes -= freed;
        for r in denial_records {
            let expires = now_unix + u64::from(r.ttl).min(negative_ttl);
            let sigs = || -> Vec<Record> {
                denial_records
                    .iter()
                    .filter(|s| {
                        s.name == r.name
                            && rrsig(s).is_some_and(|x| x.input().type_covered == r.record_type())
                    })
                    .cloned()
                    .collect()
            };
            match &r.data {
                RData::DNSSEC(DNSSECRData::NSEC(n)) => {
                    let key = canonical_key(&r.name);
                    let mut records = vec![r.clone()];
                    records.extend(sigs());
                    let bytes = entry_bytes(&records);
                    let old = d.nsec.get(&key).map_or(0, |e| e.bytes);
                    if d.bytes - old + bytes > capacity {
                        continue;
                    }
                    let entry = Entry {
                        owner: r.name.clone(),
                        rdata: n.clone(),
                        records,
                        expires,
                        bytes,
                    };
                    d.nsec.insert(key, entry);
                    d.bytes = d.bytes - old + bytes;
                }
                RData::DNSSEC(DNSSECRData::NSEC3(n)) if !n.opt_out() => {
                    let Some(hash) = nsec3_owner_hash(&r.name) else {
                        continue;
                    };
                    let mut records = vec![r.clone()];
                    records.extend(sigs());
                    let bytes = entry_bytes(&records);
                    let old = d.nsec3.get(&hash).map_or(0, |e| e.bytes);
                    if d.bytes - old + bytes > capacity {
                        continue;
                    }
                    let entry = Entry {
                        owner: r.name.clone(),
                        rdata: n.clone(),
                        records,
                        expires,
                        bytes,
                    };
                    d.nsec3.insert(hash, entry);
                    d.bytes = d.bytes - old + bytes;
                }
                _ => {}
            }
        }
        zones.bytes = zones.bytes - before + d.bytes;
        zones.evict(capacity, Some(&zone));
    }

    /// A secure negative answer for `qname`/`qtype` from cached denial records: the rcode and the
    /// authority records (SOA, NSEC/NSEC3 and their RRSIGs).
    pub fn synthesize(
        &self,
        qname: &Name,
        qtype: RecordType,
        now_unix: u64,
    ) -> Option<(ResponseCode, Vec<Record>)> {
        let zones = self.zones.lock();
        let (zone, d) = (0..=qname.num_labels() as usize).rev().find_map(|l| {
            let z = qname.trim_to(l);
            zones.map.get(&z).map(|d| (z, d))
        })?;
        if d.soa_expires <= now_unix {
            return None;
        }
        let (rcode, used) = if !d.nsec.is_empty() {
            synthesize_nsec(d, qname, qtype, now_unix)?
        } else {
            synthesize_nsec3(d, &zone, qname, qtype, now_unix)?
        };
        let mut records = d.soa.clone();
        records.extend(used);
        for r in &mut records {
            r.ttl = r.ttl.min(d.soa_expires.saturating_sub(now_unix) as u32);
        }
        RecursorMetrics::inc(&self.metrics.dnssec_aggressive_synthesized);
        Some((rcode, records))
    }
}

fn synthesize_nsec(
    d: &ZoneDenials,
    qname: &Name,
    qtype: RecordType,
    now: u64,
) -> Option<(ResponseCode, Vec<Record>)> {
    let mut used: Vec<&Entry<NSEC>> = Vec::new();
    let first = at_or_before(&d.nsec, &canonical_key(qname));
    push_unique(&mut used, first, now);
    let first = used.first()?;
    let ce = closest_encloser(qname, &first.owner, first.rdata.next_domain_name());
    if let Some(wild) = Name::from_ascii("*")
        .ok()
        .and_then(|w| w.append_domain(&ce).ok())
    {
        push_unique(&mut used, at_or_before(&d.nsec, &canonical_key(&wild)), now);
    }
    let pairs: Vec<(Name, NSEC)> = used
        .iter()
        .map(|e| (e.owner.clone(), e.rdata.clone()))
        .collect();
    let rcode = if nsec_proves_nxdomain(qname, &pairs) == Denial::Proven {
        ResponseCode::NXDomain
    } else if nsec_proves_nodata(qname, qtype, &pairs) == Denial::Proven {
        ResponseCode::NoError
    } else {
        return None;
    };
    Some((
        rcode,
        used.iter()
            .flat_map(|e| e.records.iter().cloned())
            .collect(),
    ))
}

fn synthesize_nsec3(
    d: &ZoneDenials,
    zone: &Name,
    qname: &Name,
    qtype: RecordType,
    now: u64,
) -> Option<(ResponseCode, Vec<Record>)> {
    let params = &d.nsec3.values().next()?.rdata;
    let hash = |n: &Name| nsec3_hash(n, params);
    let mut used: Vec<&Entry<NSEC3>> = Vec::new();
    let h = hash(qname)?;
    push_unique(&mut used, d.nsec3.get(&h), now);
    let mut labels = qname.num_labels() as usize;
    while labels > zone.num_labels() as usize {
        labels -= 1;
        let ce = qname.trim_to(labels);
        let Some(ce_entry) = d.nsec3.get(&hash(&ce)?) else {
            continue;
        };
        push_unique(&mut used, Some(ce_entry), now);
        push_unique(
            &mut used,
            at_or_before(&d.nsec3, &hash(&qname.trim_to(labels + 1))?),
            now,
        );
        if let Some(wh) = Name::from_ascii("*")
            .ok()
            .and_then(|w| w.append_domain(&ce).ok())
            .and_then(|w| hash(&w))
        {
            push_unique(&mut used, d.nsec3.get(&wh), now);
            push_unique(&mut used, at_or_before(&d.nsec3, &wh), now);
        }
        break;
    }
    let pairs: Vec<(Name, NSEC3)> = used
        .iter()
        .map(|e| (e.owner.clone(), e.rdata.clone()))
        .collect();
    let rcode = if nsec3_proves_nxdomain(qname, zone, &pairs) == Denial::Proven {
        ResponseCode::NXDomain
    } else if nsec3_proves_nodata(qname, qtype, zone, &pairs) == Denial::Proven {
        ResponseCode::NoError
    } else {
        return None;
    };
    Some((
        rcode,
        used.iter()
            .flat_map(|e| e.records.iter().cloned())
            .collect(),
    ))
}

#[cfg(test)]
mod tests {
    use super::*;
    use hickory_proto::rr::rdata::SOA;

    #[test]
    fn aggressive_nsec_caches_more_than_1024_denials_of_one_zone() {
        let zone = Name::from_ascii("big.test.").unwrap();
        let cache = AggressiveNsecCache::new(64 << 20, Arc::new(RecursorMetrics::default()));
        let soa = vec![Record::from_rdata(
            zone.clone(),
            300,
            RData::SOA(SOA::new(
                Name::from_ascii("ns.big.test.").unwrap(),
                Name::from_ascii("h.big.test.").unwrap(),
                1,
                3600,
                600,
                86400,
                300,
            )),
        )];
        let owner = |i: usize| Name::from_ascii(format!("n{i:05}.big.test.")).unwrap();
        let nsec = |from: Name, to: Name| {
            Record::from_rdata(
                from,
                300,
                RData::DNSSEC(DNSSECRData::NSEC(NSEC::new(
                    to,
                    [RecordType::A, RecordType::RRSIG, RecordType::NSEC],
                ))),
            )
        };
        let mut denials = vec![nsec(zone.clone(), owner(0))];
        denials.extend((0..5000).map(|i| {
            nsec(
                owner(i),
                if i == 4999 {
                    zone.clone()
                } else {
                    owner(i + 1)
                },
            )
        }));
        cache.insert_secure(&zone, &soa, &denials, 1_000);
        // Between n03999 and n04000: covered by the 4,000th NSEC; *.big.test. by the apex NSEC.
        let q = Name::from_ascii("n03999a.big.test.").unwrap();
        let (rcode, _) = cache
            .synthesize(&q, RecordType::A, 1_001)
            .expect("NXDOMAIN synthesised");
        assert_eq!(rcode, ResponseCode::NXDomain);
    }
}
