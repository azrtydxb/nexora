//! Unsigned authoritative answers: AA, referrals with glue, wildcards, CNAME/DNAME chains inside
//! hosted data, NXDOMAIN/NODATA with the zone SOA, and minimal ANY (RFC 8482). Allocation-free.

use super::lookup::Lookup;
use super::msg::Question;
use super::name::{is_subdomain, lowercase_into};
use super::set::AuthSet;
use super::writer::{Section, Writer};
use super::zone::{Node, RRset, Zone};
use super::{T_A, T_AAAA, T_ANY, T_CNAME, T_DNAME, T_NS, T_SOA};
use crate::wire::{RCODE_NOERROR, RCODE_NXDOMAIN, RCODE_SERVFAIL};

const RCODE_YXDOMAIN: u8 = 6;
const CLASS_IN: u16 = 1;
/// CNAME/DNAME hops followed after the query name.
const MAX_CHAIN: usize = 8;

pub struct Limits {
    /// Largest message `respond` may write; the caller's OPT is already subtracted.
    pub max_len: usize,
    pub recursion_available: bool,
}

pub enum Served {
    /// Response length; 0 when not even the question fits.
    Done(usize),
    NotHosted,
}

/// Lowercase wire names already visited by the chain.
struct Seen {
    names: [([u8; 255], u8); MAX_CHAIN + 1],
    len: usize,
}

impl Seen {
    fn contains(&self, name: &[u8]) -> bool {
        self.names[..self.len]
            .iter()
            .any(|(b, l)| &b[..usize::from(*l)] == name)
    }

    /// False when full.
    fn push(&mut self, name: &[u8]) -> bool {
        if self.len == self.names.len() {
            return false;
        }
        let slot = &mut self.names[self.len];
        slot.0[..name.len()].copy_from_slice(name);
        slot.1 = name.len() as u8;
        self.len += 1;
        true
    }
}

enum Step {
    /// Continue the chain at the (uncompressed, any case) name of this length in the target buffer.
    Follow(usize),
    Stop(u8),
}

/// Answers `q` from the hosted zone that holds its name; header, question, answer, authority
/// and additional sections without OPT.
pub fn respond(set: &AuthSet, q: &Question<'_>, out: &mut [u8], limits: Limits) -> Served {
    let mut lower_buf = [0u8; 255];
    let qlower = lowercase_into(q.qname, &mut lower_buf);
    if q.qclass != CLASS_IN {
        return Served::NotHosted;
    }
    let Some(first_zone) = set.find_for_query(qlower, q.qtype) else {
        return Served::NotHosted;
    };
    let Some(mut w) = Writer::new(out, limits.max_len, q) else {
        return Served::Done(0);
    };
    let ra = limits.recursion_available;
    if first_zone.expired {
        return Served::Done(w.finish(RCODE_SERVFAIL, false, ra));
    }

    let mut seen = Seen {
        names: [([0u8; 255], 0); MAX_CHAIN + 1],
        len: 0,
    };
    seen.push(qlower);
    let mut cur = [0u8; 255];
    cur[..qlower.len()].copy_from_slice(qlower);
    let mut cur_len = qlower.len();
    let mut zone: &Zone = first_zone;
    let mut aa = true;
    let mut hop = 0usize;
    loop {
        let name = &cur[..cur_len];
        let mut target = [0u8; 255];
        let step = match step(&mut w, zone, name, q.qtype, hop == 0, &mut aa, &mut target) {
            Ok(s) => s,
            Err(()) => {
                w.truncate();
                return Served::Done(w.finish(RCODE_NOERROR, aa, ra));
            }
        };
        match step {
            Step::Stop(rcode) => return Served::Done(w.finish(rcode, aa, ra)),
            Step::Follow(len) => {
                let mut lower = [0u8; 255];
                let next = lowercase_into(&target[..len], &mut lower);
                if seen.contains(next) {
                    w.reset();
                    return Served::Done(w.finish(RCODE_SERVFAIL, aa, ra));
                }
                let Some(z) = set.find(next) else {
                    return Served::Done(w.finish(RCODE_NOERROR, aa, ra));
                };
                if !seen.push(next) {
                    return Served::Done(w.finish(RCODE_NOERROR, aa, ra));
                }
                if z.expired {
                    return Served::Done(w.finish(RCODE_NOERROR, aa, ra));
                }
                zone = z;
                cur[..next.len()].copy_from_slice(next);
                cur_len = next.len();
                hop += 1;
            }
        }
    }
}

/// One lookup of the chain; `Err` when the answer or authority section overflowed.
fn step(
    w: &mut Writer<'_>,
    zone: &Zone,
    name: &[u8],
    qtype: u16,
    first: bool,
    aa: &mut bool,
    next: &mut [u8; 255],
) -> Result<Step, ()> {
    let node = match zone.lookup(name, qtype) {
        Lookup::Delegation(cut) => {
            if first {
                *aa = false;
                referral(w, zone, cut)?;
            }
            return Ok(Step::Stop(RCODE_NOERROR));
        }
        Lookup::Dname(node) => {
            let dname = node.get(T_DNAME).expect("lookup returns DNAME nodes only");
            let target = &dname.rdata[0];
            write_set(w, Section::Answer, &node.owner, dname)?;
            let Some(len) = synth_dname(name, &node.owner, target, next) else {
                return Ok(Step::Stop(RCODE_YXDOMAIN));
            };
            w.rr(Section::Answer, name, T_CNAME, dname.ttl, &next[..len])
                .map_err(|_| ())?;
            return Ok(Step::Follow(len));
        }
        Lookup::NxDomain { .. } => {
            negative(w, zone)?;
            return Ok(Step::Stop(RCODE_NXDOMAIN));
        }
        Lookup::Exact(node) => node,
        Lookup::Wildcard { wildcard, .. } => wildcard,
    };
    if qtype == T_ANY
        && let Some(s) = node.rrsets.iter().find(|s| !s.rdata.is_empty())
    {
        write_set(w, Section::Answer, name, s)?;
        return Ok(Step::Stop(RCODE_NOERROR));
    }
    if qtype != T_CNAME
        && qtype != T_ANY
        && let Some(c) = node.get(T_CNAME)
    {
        write_set(w, Section::Answer, name, c)?;
        let target = &c.rdata[0];
        let len = target.len().min(255);
        next[..len].copy_from_slice(&target[..len]);
        return Ok(Step::Follow(len));
    }
    if let Some(s) = node.get(qtype) {
        write_set(w, Section::Answer, name, s)?;
        return Ok(Step::Stop(RCODE_NOERROR));
    }
    negative(w, zone)?;
    Ok(Step::Stop(RCODE_NOERROR))
}

fn write_set(w: &mut Writer<'_>, section: Section, owner: &[u8], s: &RRset) -> Result<(), ()> {
    for d in &s.rdata {
        w.rr(section, owner, s.rtype, s.ttl, d).map_err(|_| ())?;
    }
    Ok(())
}

/// The zone SOA in the authority section with TTL `min(SOA TTL, MINIMUM)` (RFC 2308).
fn negative(w: &mut Writer<'_>, zone: &Zone) -> Result<(), ()> {
    let soa = zone
        .apex()
        .get(T_SOA)
        .expect("finalize guarantees the apex SOA");
    let rdata = &soa.rdata[0];
    let m = rdata.len() - 4;
    let minimum = u32::from_be_bytes([rdata[m], rdata[m + 1], rdata[m + 2], rdata[m + 3]]);
    w.rr(
        Section::Authority,
        zone.origin(),
        T_SOA,
        soa.ttl.min(minimum),
        rdata,
    )
    .map_err(|_| ())
}

/// NS of the cut in authority; A then AAAA of in-zone NS targets in additional, as fits.
fn referral(w: &mut Writer<'_>, zone: &Zone, cut: &Node) -> Result<(), ()> {
    let Some(ns) = cut.get(T_NS) else {
        return Ok(());
    };
    write_set(w, Section::Authority, &cut.owner, ns)?;
    for target in &ns.rdata {
        if !is_subdomain(target, zone.origin()) {
            continue;
        }
        let Some(glue) = zone.node(target) else {
            continue;
        };
        for t in [T_A, T_AAAA] {
            if let Some(s) = glue.get(t)
                && write_set(w, Section::Additional, &glue.owner, s).is_err()
            {
                return Ok(());
            }
        }
    }
    Ok(())
}

/// qname = `<prefix>.<dname owner>` → `<prefix>.<dname target>`; `None` when > 255 octets.
fn synth_dname(qname: &[u8], owner: &[u8], target: &[u8], out: &mut [u8; 255]) -> Option<usize> {
    let prefix_len = qname.len().checked_sub(owner.len())?;
    let total = prefix_len + target.len();
    if total > 255 {
        return None;
    }
    out[..prefix_len].copy_from_slice(&qname[..prefix_len]);
    out[prefix_len..total].copy_from_slice(target);
    Some(total)
}
