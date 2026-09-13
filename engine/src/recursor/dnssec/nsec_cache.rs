//! RFC 8198 aggressive use of validated NSEC/NSEC3 records: negative answers synthesised from
//! denial records cached by earlier secure responses.

use super::denial::{
    Denial, closest_encloser, nsec_proves_nodata, nsec_proves_nxdomain, nsec3_hash,
    nsec3_owner_hash, nsec3_proves_nodata, nsec3_proves_nxdomain,
};
use super::verify::rrsig;
use crate::recursor::metrics::RecursorMetrics;
use hickory_proto::dnssec::rdata::{DNSSECRData, NSEC, NSEC3};
use hickory_proto::op::ResponseCode;
use hickory_proto::rr::{Name, RData, Record, RecordType};
use parking_lot::Mutex;
use std::collections::{BTreeMap, HashMap};
use std::sync::Arc;

/// debt: denial records kept per zone; a zone with more NSECs is only partly cached. Revisit
/// when synthesis hit rates on large signed zones matter.
const MAX_DENIALS_PER_ZONE: usize = 1024;

/// One NSEC/NSEC3 record with its RRSIGs.
struct Entry<T> {
    owner: Name,
    rdata: T,
    records: Vec<Record>,
    expires: u64,
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
}

pub struct AggressiveNsecCache {
    zones: Mutex<HashMap<Name, ZoneDenials>>,
    max_zones: usize,
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
    pub fn new(max_zones: usize, metrics: Arc<RecursorMetrics>) -> Self {
        AggressiveNsecCache {
            zones: Mutex::new(HashMap::new()),
            max_zones,
            metrics,
        }
    }

    pub fn clear(&self) {
        self.zones.lock().clear();
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
        let mut zones = self.zones.lock();
        if !zones.contains_key(&zone)
            && zones.len() >= self.max_zones
            && let Some(oldest) = zones
                .iter()
                .min_by_key(|(_, d)| d.inserted)
                .map(|(z, _)| z.clone())
        {
            zones.remove(&oldest);
        }
        let d = zones.entry(zone.clone()).or_default();
        d.inserted = now_unix;
        d.soa = soa.to_vec();
        d.soa_expires = now_unix + negative_ttl;
        d.nsec.retain(|_, e| e.expires > now_unix);
        d.nsec3.retain(|_, e| e.expires > now_unix);
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
                RData::DNSSEC(DNSSECRData::NSEC(n)) if d.nsec.len() < MAX_DENIALS_PER_ZONE => {
                    let mut records = vec![r.clone()];
                    records.extend(sigs());
                    let entry = Entry {
                        owner: r.name.clone(),
                        rdata: n.clone(),
                        records,
                        expires,
                    };
                    d.nsec.insert(canonical_key(&r.name), entry);
                }
                RData::DNSSEC(DNSSECRData::NSEC3(n))
                    if !n.opt_out() && d.nsec3.len() < MAX_DENIALS_PER_ZONE =>
                {
                    let Some(hash) = nsec3_owner_hash(&r.name) else {
                        continue;
                    };
                    let mut records = vec![r.clone()];
                    records.extend(sigs());
                    let entry = Entry {
                        owner: r.name.clone(),
                        rdata: n.clone(),
                        records,
                        expires,
                    };
                    d.nsec3.insert(hash, entry);
                }
                _ => {}
            }
        }
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
            zones.get(&z).map(|d| (z, d))
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
