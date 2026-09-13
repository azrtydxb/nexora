//! Authenticated denial of existence: canonical ordering (RFC 4034 §6.1), NSEC proofs
//! (RFC 4035 §5.4, RFC 6840 §4.1) and NSEC3 proofs (RFC 5155 §8, RFC 9276 §3).
//!
//! The functions only evaluate records that the caller has already verified.

use hickory_proto::dnssec::rdata::{NSEC, NSEC3};
use hickory_proto::rr::{Name, RecordType};
use std::cmp::Ordering;

/// Above this many NSEC3 iterations a denial is treated as insecure (EDE 27).
pub const NSEC3_INSECURE_ITERATIONS: u16 = 50;
/// Above this many NSEC3 iterations a denial is bogus (EDE 27).
pub const NSEC3_BOGUS_ITERATIONS: u16 = 150;

#[derive(Debug, PartialEq, Eq, Clone, Copy)]
pub enum Denial {
    Proven,
    ProvenOptOut,
    NotProven(&'static str),
    InsecureIterations(u16),
    BogusIterations(u16),
}

pub fn canonical_cmp(a: &Name, b: &Name) -> Ordering {
    let al: Vec<Vec<u8>> = a.iter().map(|l| l.to_ascii_lowercase()).collect();
    let bl: Vec<Vec<u8>> = b.iter().map(|l| l.to_ascii_lowercase()).collect();
    for (x, y) in al.iter().rev().zip(bl.iter().rev()) {
        match x.cmp(y) {
            Ordering::Equal => continue,
            o => return o,
        }
    }
    al.len().cmp(&bl.len())
}

/// Whether the NSEC `owner` → `next` span covers `name` (strictly between them).
pub fn nsec_covers(owner: &Name, next: &Name, name: &Name) -> bool {
    if canonical_cmp(owner, next) == Ordering::Less {
        canonical_cmp(owner, name) == Ordering::Less && canonical_cmp(name, next) == Ordering::Less
    } else {
        // last NSEC in the zone wraps to the apex
        canonical_cmp(owner, name) == Ordering::Less || canonical_cmp(name, next) == Ordering::Less
    }
}

/// The longest common ancestor of `a` and `b`.
fn common_ancestor(a: &Name, b: &Name) -> Name {
    let shared = a
        .iter()
        .rev()
        .zip(b.iter().rev())
        .take_while(|(x, y)| x.eq_ignore_ascii_case(y))
        .count();
    a.trim_to(shared)
}

fn wildcard_of(ce: &Name) -> Option<Name> {
    Name::from_ascii("*").ok()?.append_domain(ce).ok()
}

/// An NSEC at the parent side of a cut (NS without SOA, or DNAME) proves nothing below its owner.
fn usable_below(owner: &Name, n: &NSEC, name: &Name) -> bool {
    !(n.is_ancestor_delegation() && owner.zone_of(name) && owner != name)
}

fn covering_nsec<'a>(name: &Name, nsecs: &'a [(Name, NSEC)]) -> Option<&'a (Name, NSEC)> {
    nsecs
        .iter()
        .find(|(o, n)| nsec_covers(o, n.next_domain_name(), name) && usable_below(o, n, name))
}

/// The closest encloser implied by a covering NSEC for `qname`.
pub fn closest_encloser(qname: &Name, owner: &Name, next: &Name) -> Name {
    let a = common_ancestor(qname, owner);
    let b = common_ancestor(qname, next);
    if a.num_labels() >= b.num_labels() {
        a
    } else {
        b
    }
}

pub fn nsec_proves_nxdomain(qname: &Name, nsecs: &[(Name, NSEC)]) -> Denial {
    if nsecs.iter().any(|(o, _)| o == qname) {
        return Denial::NotProven("name exists");
    }
    let Some((owner, n)) = covering_nsec(qname, nsecs) else {
        return Denial::NotProven("no NSEC covers qname");
    };
    let ce = closest_encloser(qname, owner, n.next_domain_name());
    let Some(wild) = wildcard_of(&ce) else {
        return Denial::NotProven("no NSEC covers the wildcard");
    };
    if covering_nsec(&wild, nsecs).is_none() {
        return Denial::NotProven("no NSEC covers the wildcard");
    }
    Denial::Proven
}

/// Checks the bitmap of an NSEC/NSEC3 whose owner matches `qname` exactly.
fn bitmap_denies(
    has: impl Fn(RecordType) -> bool,
    ancestor_delegation: bool,
    qtype: RecordType,
) -> Denial {
    if has(qtype) {
        return Denial::NotProven("type present in bitmap");
    }
    if has(RecordType::CNAME) {
        return Denial::NotProven("CNAME present in bitmap");
    }
    if qtype == RecordType::DS && has(RecordType::SOA) {
        return Denial::NotProven("DS denial from the child zone");
    }
    if qtype != RecordType::DS && ancestor_delegation {
        return Denial::NotProven("denial from the parent side of a delegation");
    }
    Denial::Proven
}

pub fn nsec_proves_nodata(qname: &Name, qtype: RecordType, nsecs: &[(Name, NSEC)]) -> Denial {
    if let Some((_, n)) = nsecs.iter().find(|(o, _)| o == qname) {
        return bitmap_denies(
            |t| n.type_set().contains(t),
            n.is_ancestor_delegation(),
            qtype,
        );
    }
    let Some((owner, n)) = covering_nsec(qname, nsecs) else {
        return Denial::NotProven("no matching NSEC");
    };
    // empty non-terminal: the next name is below qname
    if qname.zone_of(n.next_domain_name()) {
        return Denial::Proven;
    }
    // wildcard NODATA (RFC 4035 §3.1.3.4)
    let ce = closest_encloser(qname, owner, n.next_domain_name());
    if let Some(wild) = wildcard_of(&ce)
        && let Some((_, w)) = nsecs.iter().find(|(o, _)| *o == wild)
    {
        return bitmap_denies(
            |t| w.type_set().contains(t),
            w.is_ancestor_delegation(),
            qtype,
        );
    }
    Denial::NotProven("no matching NSEC")
}

/// A wildcard-expanded answer needs proof that the next closer name does not exist.
pub fn nsec_proves_no_closer_match(
    qname: &Name,
    rrsig_labels: u8,
    nsecs: &[(Name, NSEC)],
) -> Denial {
    let next_closer = qname.trim_to(rrsig_labels as usize + 1);
    if covering_nsec(&next_closer, nsecs).is_some() {
        Denial::Proven
    } else {
        Denial::NotProven("next closer name not covered")
    }
}

/// The NSEC3 hash of `name` under the salt and iterations of `n`.
pub fn nsec3_hash(name: &Name, n: &NSEC3) -> Option<Vec<u8>> {
    n.hash_algorithm()
        .hash(n.salt(), name, n.iterations())
        .ok()
        .map(|d| d.as_ref().to_vec())
}

/// The binary hash in the first label of an NSEC3 owner name.
pub fn nsec3_owner_hash(owner: &Name) -> Option<Vec<u8>> {
    let first = owner.iter().next()?;
    data_encoding::BASE32HEX_NOPAD
        .decode(&first.to_ascii_uppercase())
        .ok()
}

fn nsec3_covers_hash(owner_hash: &[u8], next: &[u8], h: &[u8]) -> bool {
    if owner_hash < next {
        owner_hash < h && h < next
    } else {
        owner_hash < h || h < next
    }
}

/// NSEC3 records of `zone` sharing the parameters of the first one, with their owner hashes.
struct Nsec3Set<'a> {
    records: Vec<(Vec<u8>, &'a NSEC3)>,
}

impl<'a> Nsec3Set<'a> {
    fn new(zone: &Name, nsec3s: &'a [(Name, NSEC3)]) -> Result<Self, Denial> {
        let max = nsec3s
            .iter()
            .map(|(_, n)| n.iterations())
            .max()
            .unwrap_or(0);
        if max > NSEC3_BOGUS_ITERATIONS {
            return Err(Denial::BogusIterations(max));
        }
        if max > NSEC3_INSECURE_ITERATIONS {
            return Err(Denial::InsecureIterations(max));
        }
        let first = nsec3s.iter().find(|(o, _)| o.base_name() == *zone);
        let records = nsec3s
            .iter()
            .filter(|(o, n)| {
                o.base_name() == *zone
                    && first.is_some_and(|(_, f)| {
                        f.salt() == n.salt() && f.iterations() == n.iterations()
                    })
            })
            .filter_map(|(o, n)| nsec3_owner_hash(o).map(|h| (h, n)))
            .collect();
        Ok(Nsec3Set { records })
    }

    fn hash(&self, name: &Name) -> Option<Vec<u8>> {
        self.records.first().and_then(|(_, n)| nsec3_hash(name, n))
    }

    fn matching(&self, h: &[u8]) -> Option<&'a NSEC3> {
        self.records.iter().find(|(o, _)| o == h).map(|(_, n)| *n)
    }

    fn covering(&self, h: &[u8]) -> Option<&'a NSEC3> {
        self.records
            .iter()
            .find(|(o, n)| nsec3_covers_hash(o, n.next_hashed_owner_name(), h))
            .map(|(_, n)| *n)
    }

    /// RFC 5155 §8.3 closest encloser proof: (closest encloser, NSEC3 covering the next closer).
    fn closest_encloser_proof(
        &self,
        qname: &Name,
        zone: &Name,
    ) -> Result<(Name, &'a NSEC3), Denial> {
        let zone_labels = zone.num_labels() as usize;
        let mut labels = qname.num_labels() as usize;
        while labels > zone_labels {
            labels -= 1;
            let ce = qname.trim_to(labels);
            let Some(h) = self.hash(&ce) else { break };
            if let Some(m) = self.matching(&h) {
                if m.is_ancestor_delegation() && ce != *zone {
                    return Err(Denial::NotProven("closest encloser is a delegation"));
                }
                let nc = qname.trim_to(labels + 1);
                let covering = self
                    .hash(&nc)
                    .and_then(|h| self.covering(&h))
                    .ok_or(Denial::NotProven("next closer not covered"))?;
                return Ok((ce, covering));
            }
        }
        Err(Denial::NotProven("no closest encloser"))
    }
}

pub fn nsec3_proves_nxdomain(qname: &Name, zone: &Name, nsec3s: &[(Name, NSEC3)]) -> Denial {
    let set = match Nsec3Set::new(zone, nsec3s) {
        Ok(s) => s,
        Err(d) => return d,
    };
    if set.hash(qname).and_then(|h| set.matching(&h)).is_some() {
        return Denial::NotProven("name exists");
    }
    let (ce, covering) = match set.closest_encloser_proof(qname, zone) {
        Ok(p) => p,
        Err(d) => return d,
    };
    if covering.opt_out() {
        return Denial::ProvenOptOut;
    }
    match wildcard_of(&ce).and_then(|w| set.hash(&w)) {
        Some(h) if set.covering(&h).is_some() => Denial::Proven,
        _ => Denial::NotProven("wildcard not covered"),
    }
}

pub fn nsec3_proves_nodata(
    qname: &Name,
    qtype: RecordType,
    zone: &Name,
    nsec3s: &[(Name, NSEC3)],
) -> Denial {
    let set = match Nsec3Set::new(zone, nsec3s) {
        Ok(s) => s,
        Err(d) => return d,
    };
    if let Some(m) = set.hash(qname).and_then(|h| set.matching(&h)) {
        return bitmap_denies(
            |t| m.type_set().contains(t),
            m.is_ancestor_delegation(),
            qtype,
        );
    }
    let Ok((ce, covering)) = set.closest_encloser_proof(qname, zone) else {
        return Denial::NotProven("no matching NSEC3");
    };
    // RFC 5155 §8.6: no DS below an opt-out span
    if qtype == RecordType::DS && covering.opt_out() {
        return Denial::ProvenOptOut;
    }
    // RFC 5155 §8.7: wildcard NODATA
    if let Some(w) = wildcard_of(&ce)
        .and_then(|w| set.hash(&w))
        .and_then(|h| set.matching(&h))
    {
        return bitmap_denies(
            |t| w.type_set().contains(t),
            w.is_ancestor_delegation(),
            qtype,
        );
    }
    Denial::NotProven("no matching NSEC3")
}

/// NSEC3 variant of [`nsec_proves_no_closer_match`] for wildcard-expanded answers.
pub fn nsec3_proves_no_closer_match(
    qname: &Name,
    rrsig_labels: u8,
    zone: &Name,
    nsec3s: &[(Name, NSEC3)],
) -> Denial {
    let set = match Nsec3Set::new(zone, nsec3s) {
        Ok(s) => s,
        Err(d) => return d,
    };
    let next_closer = qname.trim_to(rrsig_labels as usize + 1);
    match set.hash(&next_closer).and_then(|h| set.covering(&h)) {
        Some(c) if c.opt_out() => Denial::ProvenOptOut,
        Some(_) => Denial::Proven,
        None => Denial::NotProven("next closer name not covered"),
    }
}

/// The NSEC3 record of `zone` whose owner hash matches `name`.
pub fn nsec3_match<'a>(name: &Name, zone: &Name, nsec3s: &'a [(Name, NSEC3)]) -> Option<&'a NSEC3> {
    let set = Nsec3Set::new(zone, nsec3s).ok()?;
    set.hash(name).and_then(|h| set.matching(&h))
}
