//! DNSSEC answers from pre-signed zone data (RFC 4035 §3.1, RFC 5155 §7.2): RRSIGs follow every
//! signed RRset, and NSEC or NSEC3 records prove NXDOMAIN, NODATA, wildcard expansion and the
//! absence of DS at a delegation. Allocation-free.

use super::name::{KEY_BUF, canon_key_buf, label_offsets, lowercase_into};
use super::nsec3::{self, Params};
use super::writer::{Overflow, Section, Writer};
use super::zone::{Node, RRset, Zone};
use super::{T_NSEC, T_NSEC3, T_RRSIG};

/// Most proof records one step adds (NSEC3 NXDOMAIN: closest encloser, next closer, wildcard).
const PROOFS_PER_STEP: usize = 3;
/// Proof records one response can collect: every step of a CNAME/DNAME chain (query name plus
/// `answer::MAX_CHAIN` hops) can add a wildcard proof, the last one a full NXDOMAIN proof.
const MAX_PROOFS: usize = PROOFS_PER_STEP * 9;

pub enum Denial<'q, 'z> {
    /// No name `qname`; `closest_encloser` is its deepest existing ancestor.
    NxDomain {
        qname: &'q [u8],
        closest_encloser: &'z Node,
    },
    /// `node` (the node of `qname`, possibly an empty non-terminal) has no RRset of the type.
    NoData { node: &'z Node, qname: &'q [u8] },
    /// `qname` was synthesised from the wildcard below `closest_encloser`.
    WildcardAnswer {
        qname: &'q [u8],
        closest_encloser: &'z Node,
    },
    /// `qname` matched `wildcard`, which has no RRset of the type.
    WildcardNoData {
        qname: &'q [u8],
        closest_encloser: &'z Node,
        wildcard: &'z Node,
    },
    /// The delegation `cut` has no DS RRset.
    InsecureReferral { cut: &'z Node },
}

/// The NSEC/NSEC3 nodes a response proves with, deduplicated, written to the authority section
/// once the answer section is complete.
pub struct Proofs<'z> {
    nodes: [Option<&'z Node>; MAX_PROOFS],
    len: usize,
}

impl Default for Proofs<'_> {
    fn default() -> Self {
        Proofs {
            nodes: [None; MAX_PROOFS],
            len: 0,
        }
    }
}

impl<'z> Proofs<'z> {
    fn push(&mut self, node: Option<&'z Node>) {
        let Some(node) = node else { return };
        let present = self.nodes[..self.len]
            .iter()
            .any(|n| n.is_some_and(|n| std::ptr::eq(n, node)));
        if !present && self.len < MAX_PROOFS {
            self.nodes[self.len] = Some(node);
            self.len += 1;
        }
    }

    /// Writes every collected NSEC/NSEC3 RRset with its RRSIGs to authority and forgets them.
    pub fn write(&mut self, w: &mut Writer<'_>) -> Result<(), Overflow> {
        let len = std::mem::take(&mut self.len);
        for node in self.nodes[..len].iter().flatten() {
            if let Some(set) = node.get(T_NSEC).or_else(|| node.get(T_NSEC3)) {
                write_rrset_signed(w, Section::Authority, &node.owner, set, true)?;
            }
        }
        Ok(())
    }
}

/// Collects the proof records for `d` into `proofs`: NSEC when the zone has no NSEC3PARAM,
/// otherwise NSEC3 with the NSEC3PARAM hash parameters. Records the zone lacks (opt-out, unknown
/// hash algorithm) are left out.
pub fn add_denial<'z>(zone: &'z Zone, d: Denial<'_, 'z>, proofs: &mut Proofs<'z>) {
    match zone.nsec3_param() {
        None => nsec(zone, d, proofs),
        Some(rdata) => {
            if let Some(p) = Params::parse(rdata) {
                nsec3_denial(zone, p, d, proofs);
            }
        }
    }
}

/// Writes `set` under `owner`, followed by its RRSIGs when `dnssec`; RRSIGs carry the RRset TTL.
pub fn write_rrset_signed(
    w: &mut Writer<'_>,
    section: Section,
    owner: &[u8],
    set: &RRset,
    dnssec: bool,
) -> Result<(), Overflow> {
    write_rrset_signed_ttl(w, section, owner, set, set.ttl, dnssec)
}

/// [`write_rrset_signed`] with an explicit TTL (the negative-answer SOA).
pub fn write_rrset_signed_ttl(
    w: &mut Writer<'_>,
    section: Section,
    owner: &[u8],
    set: &RRset,
    ttl: u32,
    dnssec: bool,
) -> Result<(), Overflow> {
    for d in &set.rdata {
        w.rr(section, owner, set.rtype, ttl, d)?;
    }
    if dnssec {
        for s in &set.sigs {
            w.rr(section, owner, T_RRSIG, ttl, s)?;
        }
    }
    Ok(())
}

/// `*.<encloser>` in lowercase wire form; `None` when longer than 255 octets.
fn wildcard_of<'b>(encloser: &[u8], buf: &'b mut [u8; 255]) -> Option<&'b [u8]> {
    let len = encloser.len() + 2;
    if len > buf.len() {
        return None;
    }
    buf[..2].copy_from_slice(b"\x01*");
    for (d, s) in buf[2..len].iter_mut().zip(encloser) {
        *d = s.to_ascii_lowercase();
    }
    Some(&buf[..len])
}

fn nsec<'z>(zone: &'z Zone, d: Denial<'_, 'z>, proofs: &mut Proofs<'z>) {
    let covering = |name: &[u8]| {
        let mut key = [0u8; KEY_BUF];
        let n = canon_key_buf(name, &mut key);
        zone.nsec_covering(&key[..n])
    };
    let mut buf = [0u8; 255];
    match d {
        Denial::NxDomain {
            qname,
            closest_encloser,
        } => {
            proofs.push(covering(qname));
            if let Some(wild) = wildcard_of(&closest_encloser.owner, &mut buf) {
                proofs.push(covering(wild));
            }
        }
        // The NSEC at the node, or for an empty non-terminal the NSEC whose span covers it.
        Denial::NoData { qname, .. } => proofs.push(covering(qname)),
        Denial::WildcardAnswer { qname, .. } => proofs.push(covering(qname)),
        Denial::WildcardNoData {
            qname, wildcard, ..
        } => {
            proofs.push(covering(qname));
            proofs.push(covering(&wildcard.owner));
        }
        Denial::InsecureReferral { cut } => proofs.push(covering(&cut.owner)),
    }
}

fn nsec3_denial<'z>(zone: &'z Zone, p: Params<'_>, d: Denial<'_, 'z>, proofs: &mut Proofs<'z>) {
    let hash = |name: &[u8]| {
        let mut lower = [0u8; 255];
        nsec3::hash(lowercase_into(name, &mut lower), p.iterations, p.salt)
    };
    let matching = |name: &[u8]| zone.nsec3_node(&hash(name));
    let covering = |name: &[u8]| zone.nsec3_covering(&hash(name));
    let mut buf = [0u8; 255];
    match d {
        Denial::NxDomain {
            qname,
            closest_encloser,
        } => {
            proofs.push(matching(&closest_encloser.owner));
            proofs.push(covering(next_closer(qname, &closest_encloser.owner)));
            if let Some(wild) = wildcard_of(&closest_encloser.owner, &mut buf) {
                proofs.push(covering(wild));
            }
        }
        Denial::NoData { qname, .. } => proofs.push(matching(qname)),
        Denial::WildcardAnswer {
            qname,
            closest_encloser,
        } => proofs.push(covering(next_closer(qname, &closest_encloser.owner))),
        Denial::WildcardNoData {
            qname,
            closest_encloser,
            wildcard,
        } => {
            proofs.push(matching(&closest_encloser.owner));
            proofs.push(covering(next_closer(qname, &closest_encloser.owner)));
            proofs.push(matching(&wildcard.owner));
        }
        Denial::InsecureReferral { cut } => proofs.push(matching(&cut.owner)),
    }
}

/// The ancestor of `qname` one label below `encloser` (RFC 5155 §1.3 "next closer name").
fn next_closer<'q>(qname: &'q [u8], encloser: &[u8]) -> &'q [u8] {
    let mut offs = [0u16; 128];
    let total = label_offsets(qname, &mut offs);
    let enc = label_offsets(encloser, &mut [0u16; 128]);
    match total.checked_sub(enc + 1) {
        Some(i) => &qname[offs[i] as usize..],
        None => qname,
    }
}
