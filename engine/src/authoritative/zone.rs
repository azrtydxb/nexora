//! In-memory authoritative zone: nodes in canonical order, built from an NZF1 full image and
//! advanced by NZF1 deltas. Nodes are `Arc`-shared between versions, so applying a delta copies
//! only the nodes it touches.

use std::collections::{BTreeMap, BTreeSet};
use std::net::SocketAddr;
use std::ops::Bound;
use std::sync::Arc;

use super::name::{KEY_BUF, canon_key, canon_key_buf, label_offsets};
use super::nzf::{Kind, Parsed, RecordRef};
use super::{T_DNSKEY, T_NS, T_NSEC, T_NSEC3, T_NSEC3PARAM, T_RRSIG, T_SOA};

/// The node owns an NS RRset and is not the apex: a delegation point.
pub const NODE_CUT: u8 = 1;
/// A `*` child node exists.
pub const NODE_WILDCARD_CHILD: u8 = 2;
/// The node lies strictly below a delegation point (occluded, glue only).
pub const NODE_BELOW_CUT: u8 = 4;

/// RRSIG RDATA shorter than its fixed fields (type covered .. key tag) plus a root signer name.
const RRSIG_MIN_RDATA: usize = 19;

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct RRset {
    pub rtype: u16,
    pub ttl: u32,
    /// Empty for a signature-only placeholder (RRSIGs arrived before or outlived their RRset).
    pub rdata: Vec<Box<[u8]>>,
    /// RDATA of the RRSIG records covering this type.
    pub sigs: Vec<Box<[u8]>>,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Node {
    pub owner: Box<[u8]>,
    /// Sorted by type.
    pub rrsets: Vec<RRset>,
    pub flags: u8,
}

impl Node {
    /// The RRset of `rtype`, ignoring signature-only placeholders.
    pub fn get(&self, rtype: u16) -> Option<&RRset> {
        self.rrsets
            .iter()
            .find(|s| s.rtype == rtype && !s.rdata.is_empty())
    }

    pub fn is_cut(&self) -> bool {
        self.flags & NODE_CUT != 0
    }

    /// No RRset with data: an empty non-terminal (or a node holding only orphaned signatures).
    pub fn is_empty(&self) -> bool {
        self.rrsets.iter().all(|s| s.rdata.is_empty())
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct OwnedRecord {
    pub owner: Box<[u8]>,
    pub rtype: u16,
    pub ttl: u32,
    pub rdata: Box<[u8]>,
}

/// One journal step kept for IXFR: the owned records of an NZF1 delta.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct DeltaRecords {
    pub from_serial: u32,
    pub to_serial: u32,
    pub deleted: Vec<OwnedRecord>,
    pub added: Vec<OwnedRecord>,
}

impl DeltaRecords {
    /// Owned copies of a parsed NZF1 delta's deleted (`a`) and added (`b`) records.
    pub fn from_parsed(p: &Parsed<'_>) -> DeltaRecords {
        let own = |rs: &[RecordRef<'_>]| {
            rs.iter()
                .map(|r| OwnedRecord {
                    owner: r.owner.into(),
                    rtype: r.rtype,
                    ttl: r.ttl,
                    rdata: r.rdata.into(),
                })
                .collect()
        };
        DeltaRecords {
            from_serial: p.from_serial,
            to_serial: p.serial,
            deleted: own(&p.a),
            added: own(&p.b),
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
pub enum ZoneError {
    #[error("zone: expected a full image")]
    NotFull,
    #[error("zone: expected a delta")]
    NotDelta,
    #[error("zone: the apex must hold exactly one SOA record and no other node may hold one")]
    NoSoa,
    #[error("zone: SOA RDATA malformed or its serial differs from the header serial")]
    BadSoa,
    #[error("zone: delta origin differs from the zone origin")]
    OriginMismatch,
    #[error("zone: zone serial {have}, delta applies to {delta_from}")]
    SerialMismatch { have: u32, delta_from: u32 },
    #[error("zone: delta deletes a record that is not present")]
    DeleteAbsent,
    #[error("zone: malformed RRSIG RDATA")]
    BadRrsig,
}

#[derive(Debug, Clone)]
pub struct Zone {
    origin: Box<[u8]>,
    apex_key: Box<[u8]>,
    origin_labels: usize,
    serial: u32,
    nodes: BTreeMap<Box<[u8]>, Arc<Node>>,
    nsec: BTreeSet<Box<[u8]>>,
    nsec3: BTreeMap<[u8; 20], Box<[u8]>>,
    nsec3param: Option<Box<[u8]>>,
    /// IXFR history, set by the loader.
    pub deltas: Vec<Arc<DeltaRecords>>,
    /// Secondary zone past its SOA expire, set by the loader.
    pub expired: bool,
    /// Transfer ACL (empty refuses every client), set by the loader.
    pub transfer_allow: Vec<ipnet::IpNet>,
    /// Lowercase wire name of the TSIG key transfers require, set by the loader.
    pub transfer_key: Option<Box<[u8]>>,
    /// NOTIFY targets and the lowercase wire name of the key signing each, set by the loader.
    pub notify: Vec<(SocketAddr, Option<Box<[u8]>>)>,
    /// A secondary zone (pulled by the management plane), set by the loader.
    pub secondary: bool,
    /// Secondary: the primaries whose IPs are accepted as NOTIFY sources, each with the lowercase
    /// wire name of the TSIG key its NOTIFY must be signed with (when set), set by the loader.
    pub primaries: Vec<(SocketAddr, Option<Box<[u8]>>)>,
    /// Lowercase wire names of the TSIG keys allowed to UPDATE (empty refuses), set by the loader.
    pub update_keys: Vec<Box<[u8]>>,
    /// Clients allowed to query the zone (`None`: the runtime's authoritative ACL), set by the loader.
    pub allow_query: Option<crate::acl::Acl>,
    /// Sources allowed to send UPDATE (`None`: any; TSIG is still required), set by the loader.
    pub update_allow: Option<crate::acl::Acl>,
}

fn covered(r: &RecordRef<'_>) -> Result<(u16, bool), ZoneError> {
    if r.rtype != T_RRSIG {
        return Ok((r.rtype, false));
    }
    if r.rdata.len() < RRSIG_MIN_RDATA {
        return Err(ZoneError::BadRrsig);
    }
    Ok((u16::from_be_bytes([r.rdata[0], r.rdata[1]]), true))
}

fn insert(
    nodes: &mut BTreeMap<Box<[u8]>, Arc<Node>>,
    r: &RecordRef<'_>,
    key: &mut Vec<u8>,
) -> Result<(), ZoneError> {
    let (rtype, is_sig) = covered(r)?;
    key.clear();
    canon_key(r.owner, key);
    let slot = nodes.entry(key.as_slice().into()).or_insert_with(|| {
        Arc::new(Node {
            owner: r.owner.into(),
            rrsets: Vec::new(),
            flags: 0,
        })
    });
    let node = Arc::make_mut(slot);
    let idx = match node.rrsets.binary_search_by_key(&rtype, |s| s.rtype) {
        Ok(i) => i,
        Err(at) => {
            node.rrsets.insert(
                at,
                RRset {
                    rtype,
                    ttl: r.ttl,
                    rdata: Vec::new(),
                    sigs: Vec::new(),
                },
            );
            at
        }
    };
    let set = &mut node.rrsets[idx];
    let list = if is_sig {
        &mut set.sigs
    } else {
        set.ttl = r.ttl;
        &mut set.rdata
    };
    if !list.iter().any(|d| **d == *r.rdata) {
        list.push(r.rdata.into());
    }
    Ok(())
}

fn remove(
    nodes: &mut BTreeMap<Box<[u8]>, Arc<Node>>,
    r: &RecordRef<'_>,
    key: &mut Vec<u8>,
) -> Result<(), ZoneError> {
    let (rtype, is_sig) = covered(r)?;
    key.clear();
    canon_key(r.owner, key);
    let slot = nodes
        .get_mut(key.as_slice())
        .ok_or(ZoneError::DeleteAbsent)?;
    let node = Arc::make_mut(slot);
    let idx = node
        .rrsets
        .binary_search_by_key(&rtype, |s| s.rtype)
        .map_err(|_| ZoneError::DeleteAbsent)?;
    let set = &mut node.rrsets[idx];
    let list = if is_sig {
        &mut set.sigs
    } else {
        &mut set.rdata
    };
    let pos = list
        .iter()
        .position(|d| **d == *r.rdata)
        .ok_or(ZoneError::DeleteAbsent)?;
    list.remove(pos);
    if set.rdata.is_empty() && set.sigs.is_empty() {
        node.rrsets.remove(idx);
    }
    Ok(())
}

/// Length of the uncompressed wire name at the start of `b`, if well formed.
fn name_len(b: &[u8]) -> Option<usize> {
    let mut i = 0usize;
    loop {
        let l = *b.get(i)? as usize;
        if l == 0 {
            return (i < 255).then_some(i + 1);
        }
        if l >= 0x40 {
            return None;
        }
        i += l + 1;
    }
}

fn soa_serial(rdata: &[u8]) -> Option<u32> {
    let m = name_len(rdata)?;
    let r = name_len(&rdata[m..])?;
    let s = rdata.get(m + r..)?;
    (s.len() == 20).then(|| u32::from_be_bytes([s[0], s[1], s[2], s[3]]))
}

/// Decodes a 32-character RFC 4648 base32hex first label (case-insensitive) into a SHA-1 hash.
fn decode_b32hex_label(owner: &[u8]) -> Option<[u8; 20]> {
    if owner.first() != Some(&32) {
        return None;
    }
    super::nsec3::decode_b32hex(owner.get(1..33)?)
}

impl Zone {
    pub fn from_image(p: &Parsed<'_>) -> Result<Zone, ZoneError> {
        if p.kind != Kind::Full {
            return Err(ZoneError::NotFull);
        }
        let origin: Box<[u8]> = p.origin.to_ascii_lowercase().into();
        let mut apex_key = Vec::new();
        canon_key(&origin, &mut apex_key);
        let mut offs = [0u16; 128];
        let origin_labels = label_offsets(&origin, &mut offs);
        let mut zone = Zone {
            origin,
            apex_key: apex_key.into(),
            origin_labels,
            serial: p.serial,
            nodes: BTreeMap::new(),
            nsec: BTreeSet::new(),
            nsec3: BTreeMap::new(),
            nsec3param: None,
            deltas: Vec::new(),
            expired: false,
            transfer_allow: Vec::new(),
            transfer_key: None,
            notify: Vec::new(),
            secondary: false,
            primaries: Vec::new(),
            update_keys: Vec::new(),
            allow_query: None,
            update_allow: None,
        };
        let mut key = Vec::with_capacity(512);
        for r in &p.a {
            insert(&mut zone.nodes, r, &mut key)?;
        }
        zone.finalize()?;
        Ok(zone)
    }

    /// Returns the zone at the delta's target serial; `self` is unchanged. The result carries no
    /// IXFR history, transfer/update policy, primaries or NOTIFY targets and is not expired (the loader sets them).
    pub fn apply(&self, d: &Parsed<'_>) -> Result<Zone, ZoneError> {
        if d.kind != Kind::Delta {
            return Err(ZoneError::NotDelta);
        }
        if !d.origin.eq_ignore_ascii_case(&self.origin) {
            return Err(ZoneError::OriginMismatch);
        }
        if d.from_serial != self.serial {
            return Err(ZoneError::SerialMismatch {
                have: self.serial,
                delta_from: d.from_serial,
            });
        }
        let mut nodes = self.nodes.clone();
        let mut key = Vec::with_capacity(512);
        for r in &d.a {
            remove(&mut nodes, r, &mut key)?;
        }
        for r in &d.b {
            insert(&mut nodes, r, &mut key)?;
        }
        let mut zone = Zone {
            origin: self.origin.clone(),
            apex_key: self.apex_key.clone(),
            origin_labels: self.origin_labels,
            serial: d.serial,
            nodes,
            nsec: BTreeSet::new(),
            nsec3: BTreeMap::new(),
            nsec3param: None,
            deltas: Vec::new(),
            expired: false,
            transfer_allow: Vec::new(),
            transfer_key: None,
            notify: Vec::new(),
            secondary: false,
            primaries: Vec::new(),
            update_keys: Vec::new(),
            allow_query: None,
            update_allow: None,
        };
        zone.finalize()?;
        Ok(zone)
    }

    /// Drops nodes without data, adds empty non-terminals, checks the apex SOA, recomputes node
    /// flags and the NSEC/NSEC3 indexes.
    fn finalize(&mut self) -> Result<(), ZoneError> {
        self.nodes.retain(|_, n| {
            n.rrsets
                .iter()
                .any(|s| !s.rdata.is_empty() || !s.sigs.is_empty())
        });
        let owners: Vec<Box<[u8]>> = self.nodes.values().map(|n| n.owner.clone()).collect();
        let mut key = Vec::with_capacity(512);
        let mut offs = [0u16; 128];
        for owner in &owners {
            let n = label_offsets(owner, &mut offs);
            for &off in &offs[1..n.saturating_sub(self.origin_labels).max(1)] {
                let anc = &owner[off as usize..];
                key.clear();
                canon_key(anc, &mut key);
                if self.nodes.contains_key(key.as_slice()) {
                    continue;
                }
                self.nodes.insert(
                    key.as_slice().into(),
                    Arc::new(Node {
                        owner: anc.into(),
                        rrsets: Vec::new(),
                        flags: 0,
                    }),
                );
            }
        }
        let apex = self.nodes.get(&self.apex_key).ok_or(ZoneError::NoSoa)?;
        let soa = apex.get(T_SOA).ok_or(ZoneError::NoSoa)?;
        if soa.rdata.len() != 1 {
            return Err(ZoneError::NoSoa);
        }
        if soa_serial(&soa.rdata[0]) != Some(self.serial) {
            return Err(ZoneError::BadSoa);
        }

        // One ordered pass: every descendant's key has its ancestor's key as a prefix, and the
        // keys sharing a prefix are contiguous in the map, so the enclosing cut is the last one
        // whose key prefixes the current key.
        let mut flags = Vec::with_capacity(self.nodes.len());
        let mut cut: Option<&[u8]> = None;
        let mut wild = Vec::with_capacity(512);
        for (k, n) in &self.nodes {
            if n.get(T_SOA).is_some() && **k != *self.apex_key {
                return Err(ZoneError::NoSoa);
            }
            let mut f = 0u8;
            if cut.is_some_and(|c| k.len() > c.len() && k.starts_with(c)) {
                f |= NODE_BELOW_CUT;
            } else {
                cut = None;
            }
            if **k != *self.apex_key && n.get(T_NS).is_some() {
                f |= NODE_CUT;
                if cut.is_none() {
                    cut = Some(k);
                }
            }
            wild.clear();
            wild.extend_from_slice(k);
            wild.extend_from_slice(b"*\0");
            if self.nodes.contains_key(wild.as_slice()) {
                f |= NODE_WILDCARD_CHILD;
            }
            flags.push(f);
        }
        for (n, f) in self.nodes.values_mut().zip(flags) {
            if n.flags != f {
                Arc::make_mut(n).flags = f;
            }
        }

        self.nsec = self
            .nodes
            .iter()
            .filter(|(_, n)| n.get(T_NSEC).is_some())
            .map(|(k, _)| k.clone())
            .collect();
        self.nsec3 = self
            .nodes
            .iter()
            .filter(|(_, n)| n.get(T_NSEC3).is_some())
            .filter_map(|(k, n)| decode_b32hex_label(&n.owner).map(|h| (h, k.clone())))
            .collect();
        self.nsec3param = self
            .nodes
            .get(&self.apex_key)
            .and_then(|n| n.get(T_NSEC3PARAM))
            .map(|s| s.rdata[0].clone());
        Ok(())
    }

    /// Lowercase wire origin.
    /// Test setter: marks the zone secondary with `primaries` (address, required NOTIFY key wire
    /// name) as its NOTIFY sources.
    pub fn set_secondary_primaries(&mut self, primaries: Vec<(SocketAddr, Option<Box<[u8]>>)>) {
        self.secondary = true;
        self.primaries = primaries;
    }

    pub fn origin(&self) -> &[u8] {
        &self.origin
    }

    /// Labels of the origin, root excluded.
    pub fn origin_labels(&self) -> usize {
        self.origin_labels
    }

    pub fn serial(&self) -> u32 {
        self.serial
    }

    pub fn apex(&self) -> &Node {
        // finalize guarantees the apex node.
        &self.nodes[&self.apex_key]
    }

    pub fn soa_rdata(&self) -> &[u8] {
        // finalize guarantees exactly one SOA at the apex.
        &self.apex().get(T_SOA).expect("apex SOA").rdata[0]
    }

    /// Case-insensitive node lookup by wire name.
    pub fn node(&self, wire: &[u8]) -> Option<&Node> {
        let mut key = [0u8; KEY_BUF];
        let n = canon_key_buf(wire, &mut key);
        self.node_by_key(&key[..n])
    }

    /// Node lookup by canonical key (`name::canon_key`).
    pub fn node_by_key(&self, key: &[u8]) -> Option<&Node> {
        self.nodes.get(key).map(|n| &**n)
    }

    /// Every record in canonical order (owner key, type, RDATA); RRSIGs are records of type RRSIG
    /// with their RRset's TTL.
    pub fn records_sorted(&self) -> Vec<OwnedRecord> {
        let mut out = Vec::new();
        let mut node_recs: Vec<(u16, u32, &[u8])> = Vec::new();
        for n in self.nodes.values() {
            node_recs.clear();
            for s in &n.rrsets {
                node_recs.extend(s.rdata.iter().map(|d| (s.rtype, s.ttl, &**d)));
                node_recs.extend(s.sigs.iter().map(|d| (T_RRSIG, s.ttl, &**d)));
            }
            node_recs.sort_by(|a, b| (a.0, a.2).cmp(&(b.0, b.2)));
            out.extend(node_recs.iter().map(|&(rtype, ttl, rdata)| OwnedRecord {
                owner: n.owner.clone(),
                rtype,
                ttl,
                rdata: rdata.into(),
            }));
        }
        out
    }

    /// The apex holds a DNSKEY RRset.
    pub fn is_signed(&self) -> bool {
        self.apex().get(T_DNSKEY).is_some()
    }

    /// The NSEC node with the greatest key ≤ `key`, wrapping to the last NSEC node.
    pub fn nsec_covering(&self, key: &[u8]) -> Option<&Node> {
        let k = self
            .nsec
            .range::<[u8], _>((Bound::Unbounded, Bound::Included(key)))
            .next_back()
            .or_else(|| self.nsec.last())?;
        self.node_by_key(k)
    }

    /// NSEC3PARAM RDATA at the apex.
    pub fn nsec3_param(&self) -> Option<&[u8]> {
        self.nsec3param.as_deref()
    }

    /// The NSEC3 node whose owner hash equals `hash`.
    pub fn nsec3_node(&self, hash: &[u8; 20]) -> Option<&Node> {
        self.node_by_key(self.nsec3.get(hash)?)
    }

    /// The NSEC3 node with the greatest hash < `hash`, wrapping to the last one.
    pub fn nsec3_covering(&self, hash: &[u8; 20]) -> Option<&Node> {
        let (_, k) = self
            .nsec3
            .range::<[u8; 20], _>((Bound::Unbounded, Bound::Excluded(hash)))
            .next_back()
            .or_else(|| self.nsec3.last_key_value())?;
        self.node_by_key(k)
    }

    /// Nodes strictly below the name with canonical key `key`, in canonical order.
    pub fn nodes_below<'s>(&'s self, key: &'s [u8]) -> impl Iterator<Item = &'s Node> + 's {
        self.nodes
            .range::<[u8], _>((Bound::Excluded(key), Bound::Unbounded))
            .take_while(move |(k, _)| k.starts_with(key))
            .map(|(_, n)| &**n)
    }
}
