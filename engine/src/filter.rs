//! Blocklist/allowlist matching on wire-format name suffixes, and block replies.

pub mod calibrate;
pub mod decisions;
pub mod index;
pub mod lists;
pub mod names;
#[doc(hidden)]
pub mod oracle;
pub mod prefetch;
pub mod storage;
pub mod synth;

use crate::edns::ReplyOpt;
use crate::proto::{ConfigSnapshot, RewriteSet, RewriteType};
use crate::wire::{self, QueryView, SynthAnswer};
use decisions::DecisionCache;
use hickory_proto::rr::Name;
use lists::SnapshotLists;
use rustc_hash::FxHashMap;
use std::collections::HashMap;
use std::io::Read;
use std::net::{IpAddr, Ipv4Addr, Ipv6Addr};
use std::sync::Arc;

const TYPE_A: u16 = 1;
const TYPE_AAAA: u16 = 28;
use names::MAX_TEXT_LEN;
/// Largest decompressed blob accepted; bounds memory against a zstd bomb.
const MAX_BLOB_BYTES: u64 = 512 << 20;

/// Discriminants equal the proto `BlockMode` values.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum BlockMode {
    NullIp = 1,
    NxDomain = 2,
    Refused = 3,
}

pub use index::{FilterDecision, FilterIndex, FilterView, ListHit};

/// The configured block response.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct BlockReply {
    pub mode: BlockMode,
    pub ttl: u32,
}

impl BlockReply {
    /// Writes the block reply; returns 0 when `out` is too small.
    pub fn write(&self, q: &QueryView<'_>, out: &mut [u8], opt: Option<&ReplyOpt>) -> usize {
        match self.mode {
            BlockMode::NullIp => {
                let answer = match q.qtype {
                    TYPE_A => Some(SynthAnswer::A([0; 4])),
                    TYPE_AAAA => Some(SynthAnswer::Aaaa([0; 16])),
                    _ => None,
                };
                wire::write_synth_reply(q, wire::RCODE_NOERROR, answer, self.ttl, out, opt)
            }
            BlockMode::NxDomain => wire::write_rcode_reply(q, wire::RCODE_NXDOMAIN, out, opt),
            BlockMode::Refused => wire::write_rcode_reply(q, wire::RCODE_REFUSED, out, opt),
        }
    }
}

/// The content size a blob's zstd frame declares, when it declares one of at most 512 MiB.
pub fn blob_content_size(zstd_bytes: &[u8]) -> Option<usize> {
    zstd::zstd_safe::get_frame_content_size(zstd_bytes)
        .ok()
        .flatten()
        .filter(|&n| n <= MAX_BLOB_BYTES)
        .map(|n| n as usize)
}

/// Decompresses a blob, refusing output larger than 512 MiB.
pub fn decode_blob(zstd_bytes: &[u8]) -> std::io::Result<Vec<u8>> {
    let mut out = Vec::new();
    zstd::stream::read::Decoder::new(zstd_bytes)?
        .take(MAX_BLOB_BYTES + 1)
        .read_to_end(&mut out)?;
    if out.len() as u64 > MAX_BLOB_BYTES {
        return Err(std::io::Error::new(
            std::io::ErrorKind::InvalidData,
            "blob decompresses past 512 MiB",
        ));
    }
    Ok(out)
}

/// The lowercase uncompressed wire name (with root byte) of a text domain made
/// of `[a-z0-9-_.]`; `None` for anything else.
pub fn domain_to_wire(domain: &[u8]) -> Option<Box<[u8]>> {
    if domain.is_empty() || domain.len() > MAX_TEXT_LEN {
        return None;
    }
    let mut out = Vec::with_capacity(domain.len() + 2);
    for label in domain.split(|&b| b == b'.') {
        if !names::valid_label(label) {
            return None;
        }
        out.push(label.len() as u8);
        out.extend(label.iter().map(u8::to_ascii_lowercase));
    }
    out.push(0);
    Some(out.into_boxed_slice())
}

/// Largest rewrite rule TTL accepted.
const MAX_REWRITE_TTL: u32 = 86400;

/// What a rewrite rule set answers for one name.
#[derive(Clone)]
pub enum RewriteAnswer {
    Addrs {
        a: Box<[(Ipv4Addr, u32)]>,
        aaaa: Box<[(Ipv6Addr, u32)]>,
    },
    Cname {
        target: Name,
        ttl: u32,
    },
}

pub enum Verdict<'a> {
    Pass,
    Allowed,
    Blocked(ListHit),
    Rewrite(&'a RewriteAnswer),
}

/// Exact and `*.base` rewrites keyed by lowercase wire name (wildcards by base).
#[derive(Default)]
pub struct RewriteTable {
    exact: FxHashMap<Box<[u8]>, RewriteAnswer>,
    wildcard: FxHashMap<Box<[u8]>, RewriteAnswer>,
}

impl RewriteTable {
    /// The exact rewrite for `wire_name`, else the wildcard with the longest
    /// base that is a strict suffix of it.
    pub fn lookup(&self, wire_name: &[u8]) -> Option<&RewriteAnswer> {
        if self.exact.is_empty() && self.wildcard.is_empty() {
            return None;
        }
        if let Some(a) = self.exact.get(wire_name) {
            return Some(a);
        }
        if self.wildcard.is_empty() {
            return None;
        }
        let mut pos = 0usize;
        loop {
            let len = *wire_name.get(pos)? as usize;
            if len == 0 {
                return None;
            }
            pos += 1 + len;
            if *wire_name.get(pos)? == 0 {
                return None;
            }
            if let Some(a) = self.wildcard.get(&wire_name[pos..]) {
                return Some(a);
            }
        }
    }

    /// Adds every key of `other` not already present (earlier sets win).
    fn merge_from(&mut self, other: &RewriteTable) {
        for (k, v) in &other.exact {
            self.exact.entry(k.clone()).or_insert_with(|| v.clone());
        }
        for (k, v) in &other.wildcard {
            self.wildcard.entry(k.clone()).or_insert_with(|| v.clone());
        }
    }
}

/// The filter and rewrites that apply to one client.
pub struct EffectivePolicy {
    group_id: Box<str>,
    filter: Arc<FilterView>,
    block: BlockReply,
    rewrites: RewriteTable,
    /// Identifies `filter` among the runtime's distinct views (global = 0). Cached upstream
    /// answers are only valid for the view whose CNAME cloaking check admitted them, so it is part
    /// of the cache key.
    cache_partition: u16,
}

impl EffectivePolicy {
    pub fn group_id(&self) -> &str {
        &self.group_id
    }

    pub fn filter(&self) -> &FilterView {
        &self.filter
    }

    pub fn block_reply(&self) -> BlockReply {
        self.block
    }

    pub fn cache_partition(&self) -> u16 {
        self.cache_partition
    }

    /// The rewrite or filter verdict for `wire_name`, deciding without a decision cache.
    pub fn check(&self, wire_name: &[u8]) -> Verdict<'_> {
        self.verdict(wire_name, |name| self.filter.decide(name))
    }

    /// [`EffectivePolicy::check`] through the worker's decision cache (the query fast path).
    #[inline]
    pub fn check_cached(&self, cache: &DecisionCache, wire_name: &[u8]) -> Verdict<'_> {
        self.verdict(wire_name, |name| cache.decide(&self.filter, name))
    }

    #[inline(always)]
    fn verdict(
        &self,
        wire_name: &[u8],
        decide: impl FnOnce(&[u8]) -> FilterDecision,
    ) -> Verdict<'_> {
        if let Some(r) = self.rewrites.lookup(wire_name) {
            return Verdict::Rewrite(r);
        }
        match decide(wire_name) {
            FilterDecision::Blocked(hit) => Verdict::Blocked(hit),
            FilterDecision::Allowed => Verdict::Allowed,
            FilterDecision::None => Verdict::Pass,
        }
    }
}

/// Client address -> policy by longest matching CIDR prefix.
pub struct PolicyTable {
    groups: Box<[EffectivePolicy]>,
    global: EffectivePolicy,
    /// Per prefix length (descending): network bits -> group index.
    v4: Box<[(u8, FxHashMap<u32, u16>)]>,
    v6: Box<[(u8, FxHashMap<u128, u16>)]>,
    /// The filter key of every group cache partition, in partition order (1..).
    partition_keys: Box<[String]>,
    /// Memory of the global view and every distinct group view.
    views_memory_bytes: u64,
}

impl PolicyTable {
    /// Only global policy: no groups and no rewrites.
    pub fn global_only(global: Arc<FilterView>, block: BlockReply) -> PolicyTable {
        PolicyTable {
            groups: Box::new([]),
            views_memory_bytes: global.memory_bytes(),
            global: EffectivePolicy {
                group_id: "".into(),
                filter: global,
                block,
                rewrites: RewriteTable::default(),
                cache_partition: 0,
            },
            v4: Box::new([]),
            v6: Box::new([]),
            partition_keys: Box::new([]),
        }
    }

    /// The client's policy and its group index (`None` = global). Allocation- and lock-free.
    pub fn select(&self, ip: IpAddr) -> (&EffectivePolicy, Option<u16>) {
        let ip = match ip {
            IpAddr::V6(v6) => v6
                .to_ipv4_mapped()
                .map(IpAddr::V4)
                .unwrap_or(IpAddr::V6(v6)),
            v4 => v4,
        };
        match ip {
            IpAddr::V4(v4) => {
                let bits = u32::from(v4);
                for (len, map) in self.v4.iter() {
                    let mask = u32::MAX.checked_shl(32 - u32::from(*len)).unwrap_or(0);
                    if let Some(&i) = map.get(&(bits & mask)) {
                        return (&self.groups[usize::from(i)], Some(i));
                    }
                }
            }
            IpAddr::V6(v6) => {
                let bits = u128::from(v6);
                for (len, map) in self.v6.iter() {
                    let mask = u128::MAX.checked_shl(128 - u32::from(*len)).unwrap_or(0);
                    if let Some(&i) = map.get(&(bits & mask)) {
                        return (&self.groups[usize::from(i)], Some(i));
                    }
                }
            }
        }
        (&self.global, None)
    }

    /// The group at `index`, as returned by [`PolicyTable::select`].
    pub fn group(&self, index: u16) -> Option<&EffectivePolicy> {
        self.groups.get(usize::from(index))
    }

    /// Changes whenever a group cache partition would map to a different filter set.
    pub fn partition_keys(&self) -> &[String] {
        &self.partition_keys
    }

    pub fn views_memory_bytes(&self) -> u64 {
        self.views_memory_bytes
    }

    /// Validates and builds the policy section of `snap`: one view over `index` per distinct
    /// group list selection (shared by groups selecting the same lists), `global` for clients in
    /// no group, and `block` for every policy.
    pub fn build(
        snap: &ConfigSnapshot,
        lists: &SnapshotLists,
        index: &Arc<FilterIndex>,
        global: Arc<FilterView>,
        block: BlockReply,
    ) -> Result<PolicyTable, String> {
        let mut sets: HashMap<&str, RewriteTable> = HashMap::new();
        for set in &snap.rewrite_sets {
            let table = build_rewrite_set(set)?;
            if sets.insert(set.id.as_str(), table).is_some() {
                return Err(format!("rewrite set {}: duplicate id", set.id));
            }
        }
        let merged = |ids: &[String], unknown: &dyn Fn(&str) -> String| {
            let mut table = RewriteTable::default();
            for id in ids {
                let set = sets.get(id.as_str()).ok_or_else(|| unknown(id))?;
                table.merge_from(set);
            }
            Ok::<_, String>(table)
        };
        let global_rewrites = merged(&snap.global_rewrite_set_ids, &|id| {
            format!("unknown global rewrite set {id}")
        })?;
        if snap.policy_groups.len() >= usize::from(u16::MAX) {
            return Err(format!(
                "{} policy groups exceed {}",
                snap.policy_groups.len(),
                u16::MAX - 1
            ));
        }

        let mut views: HashMap<String, (Arc<FilterView>, u16)> = HashMap::new();
        let mut views_memory_bytes = global.memory_bytes();
        let mut partition_keys = Vec::new();
        let mut groups = Vec::with_capacity(snap.policy_groups.len());
        let mut v4: FxHashMap<(u8, u32), u16> = FxHashMap::default();
        let mut v6: FxHashMap<(u8, u128), u16> = FxHashMap::default();
        for (index_in_snapshot, g) in snap.policy_groups.iter().enumerate() {
            let group_index = index_in_snapshot as u16;
            let rewrites = merged(&g.rewrite_set_ids, &|id| {
                format!("policy group {}: unknown rewrite set {id}", g.id)
            })?;

            let key = lists.group_key(index_in_snapshot);
            let (filter, cache_partition) = match views.get(&key) {
                Some((f, p)) => (f.clone(), *p),
                None => {
                    let selected = &lists.groups[index_in_snapshot];
                    let view = Arc::new(index.view(
                        &lists.indexes(index, &selected.block),
                        &lists.indexes(index, selected.allow.as_slice()),
                    ));
                    views_memory_bytes += view.memory_bytes();
                    partition_keys.push(key.clone());
                    let partition = partition_keys.len() as u16;
                    views.insert(key, (view.clone(), partition));
                    (view, partition)
                }
            };

            for cidr in &g.cidrs {
                let net = cidr
                    .parse::<ipnet::IpNet>()
                    .ok()
                    .filter(|n| *n == n.trunc())
                    .ok_or_else(|| format!("policy group {}: invalid cidr {cidr}", g.id))?;
                let previous = match net {
                    ipnet::IpNet::V4(n) => {
                        v4.insert((n.prefix_len(), u32::from(n.network())), group_index)
                    }
                    ipnet::IpNet::V6(n) => {
                        v6.insert((n.prefix_len(), u128::from(n.network())), group_index)
                    }
                };
                if let Some(other) = previous {
                    return Err(format!(
                        "policy group {}: cidr {cidr} also in group {}",
                        g.id,
                        snap.policy_groups[usize::from(other)].id
                    ));
                }
            }
            groups.push(EffectivePolicy {
                group_id: g.id.as_str().into(),
                filter,
                block,
                rewrites,
                cache_partition,
            });
        }

        let mut table = PolicyTable::global_only(global, block);
        table.views_memory_bytes = views_memory_bytes;
        table.global.rewrites = global_rewrites;
        table.groups = groups.into_boxed_slice();
        table.v4 = by_prefix_len(v4);
        table.v6 = by_prefix_len(v6);
        table.partition_keys = partition_keys.into_boxed_slice();
        Ok(table)
    }
}

/// Groups `(len, bits) -> index` into per-length maps ordered longest first.
fn by_prefix_len<T: std::hash::Hash + Eq>(
    entries: FxHashMap<(u8, T), u16>,
) -> Box<[(u8, FxHashMap<T, u16>)]> {
    let mut by_len: Vec<(u8, FxHashMap<T, u16>)> = Vec::new();
    for ((len, bits), index) in entries {
        match by_len.iter_mut().find(|(l, _)| *l == len) {
            Some((_, map)) => {
                map.insert(bits, index);
            }
            None => by_len.push((len, FxHashMap::from_iter([(bits, index)]))),
        }
    }
    by_len.sort_unstable_by_key(|(len, _)| std::cmp::Reverse(*len));
    by_len.into_boxed_slice()
}

#[derive(Default)]
struct RuleAcc {
    a: Vec<(Ipv4Addr, u32)>,
    aaaa: Vec<(Ipv6Addr, u32)>,
    cname: Option<(Name, u32)>,
}

/// Validates one set's rules and groups them per key.
fn build_rewrite_set(set: &RewriteSet) -> Result<RewriteTable, String> {
    let mut exact: HashMap<Box<[u8]>, RuleAcc> = HashMap::new();
    let mut wildcard: HashMap<Box<[u8]>, RuleAcc> = HashMap::new();
    for rule in &set.rules {
        let fail = |what: String| Err(format!("rewrite set {}: {what}", set.id));
        let name = rule.name.as_str();
        let (base, is_wildcard) = match name.strip_prefix("*.") {
            Some(base) => (base, true),
            None => (name, false),
        };
        let wire = domain_to_wire(base.as_bytes()).filter(|_| {
            name.len() <= MAX_TEXT_LEN && !base.bytes().any(|b| b.is_ascii_uppercase())
        });
        let Some(wire) = wire else {
            return fail(format!("invalid rule name {name}"));
        };
        if rule.ttl > MAX_REWRITE_TTL {
            return fail(format!("{name} ttl {} exceeds {MAX_REWRITE_TTL}", rule.ttl));
        }
        let map = if is_wildcard {
            &mut wildcard
        } else {
            &mut exact
        };
        let acc = map.entry(wire).or_default();
        let value = rule.value.as_str();
        match RewriteType::try_from(rule.r#type) {
            Ok(RewriteType::A) => match value.parse::<Ipv4Addr>() {
                Ok(ip) => acc.a.push((ip, rule.ttl)),
                Err(_) => return fail(format!("{name} A value {value} is not an IPv4 address")),
            },
            Ok(RewriteType::Aaaa) => match value.parse::<Ipv6Addr>() {
                Ok(ip) => acc.aaaa.push((ip, rule.ttl)),
                Err(_) => {
                    return fail(format!("{name} AAAA value {value} is not an IPv6 address"));
                }
            },
            Ok(RewriteType::Cname) => {
                let target = domain_to_wire(value.as_bytes())
                    .and_then(|_| Name::from_ascii(format!("{value}.")).ok());
                match target {
                    Some(t) if acc.cname.is_none() => acc.cname = Some((t, rule.ttl)),
                    Some(_) => return fail(format!("{name} has CNAME and other records")),
                    None => return fail(format!("{name} CNAME target {value} is invalid")),
                }
            }
            Ok(RewriteType::Unspecified) | Err(_) => {
                return fail(format!("{name} type {} is invalid", rule.r#type));
            }
        }
        if acc.cname.is_some() && !(acc.a.is_empty() && acc.aaaa.is_empty()) {
            return fail(format!("{name} has CNAME and other records"));
        }
    }
    let answer = |acc: RuleAcc| match acc.cname {
        Some((target, ttl)) => RewriteAnswer::Cname { target, ttl },
        None => RewriteAnswer::Addrs {
            a: acc.a.into_boxed_slice(),
            aaaa: acc.aaaa.into_boxed_slice(),
        },
    };
    Ok(RewriteTable {
        exact: exact.into_iter().map(|(k, acc)| (k, answer(acc))).collect(),
        wildcard: wildcard
            .into_iter()
            .map(|(k, acc)| (k, answer(acc)))
            .collect(),
    })
}

#[cfg(test)]
mod policy_tests {
    use super::*;
    use crate::proto::{
        BlobRef, ConfigSnapshot, PolicyGroup, RewriteRule, RewriteSet, RewriteType,
    };
    use crate::snapshot::{BlobSource, SnapshotError};
    use std::collections::HashMap;
    use std::net::IpAddr;
    use std::sync::Arc;

    fn wire(name: &str) -> Vec<u8> {
        let mut w = Vec::new();
        for l in name.trim_end_matches('.').split('.') {
            w.push(l.len() as u8);
            w.extend_from_slice(l.as_bytes());
        }
        w.push(0);
        w
    }
    fn rule(name: &str, t: RewriteType, v: &str) -> RewriteRule {
        RewriteRule {
            name: name.into(),
            r#type: t as i32,
            value: v.into(),
            ttl: 120,
        }
    }
    struct MapBlobs(HashMap<String, Vec<u8>>);
    impl BlobSource for MapBlobs {
        fn read(&self, r: &BlobRef) -> Result<Vec<u8>, SnapshotError> {
            self.0
                .get(&r.sha256)
                .cloned()
                .ok_or_else(|| SnapshotError::Blob {
                    sha256: r.sha256.clone(),
                    reason: "missing".into(),
                })
        }
    }
    fn ads_blob() -> (BlobRef, Vec<u8>) {
        let z = zstd::encode_all(&b"ads.example.test\ntracker.test\n"[..], 3).unwrap();
        (
            BlobRef {
                sha256: "a".repeat(64),
                size: z.len() as u64,
                name: "ads".into(),
            },
            z,
        )
    }
    fn blobs() -> MapBlobs {
        let (r, z) = ads_blob();
        MapBlobs(HashMap::from([(r.sha256, z)]))
    }
    fn table(s: &ConfigSnapshot) -> Result<PolicyTable, String> {
        let mut s = s.clone();
        let (r, _) = ads_blob();
        s.filter = Some(crate::proto::FilterConfig {
            blocklists: vec![BlobRef {
                name: "global".into(),
                ..r
            }],
            block_mode: 1,
            block_ttl: 60,
            ..Default::default()
        });
        let lists = SnapshotLists::collect(&s)?;
        let index = Arc::new(
            lists
                .build_index(&blobs(), 64 << 20, 1)
                .map_err(|e| e.to_string())?,
        );
        let global = Arc::new(index.view(
            &lists.indexes(&index, &lists.global_block),
            &lists.indexes(&index, &lists.global_allow),
        ));
        PolicyTable::build(
            &s,
            &lists,
            &index,
            global,
            BlockReply {
                mode: BlockMode::NullIp,
                ttl: 60,
            },
        )
    }
    fn group(
        id: &str,
        cidrs: &[&str],
        lists: &[BlobRef],
        allow: &[&str],
        sets: &[&str],
    ) -> PolicyGroup {
        PolicyGroup {
            id: id.into(),
            name: id.into(),
            cidrs: cidrs.iter().map(|s| s.to_string()).collect(),
            blocklists: lists.to_vec(),
            allowlist: allow.iter().map(|s| s.to_string()).collect(),
            rewrite_set_ids: sets.iter().map(|s| s.to_string()).collect(),
            ..Default::default()
        }
    }
    fn snapshot() -> ConfigSnapshot {
        ConfigSnapshot {
            policy_groups: vec![
                group(
                    "wide",
                    &["10.0.0.0/8"],
                    &[ads_blob().0],
                    &["ok.ads.example.test"],
                    &["custom:group:wide"],
                ),
                group(
                    "narrow",
                    &["10.1.0.0/16", "2001:db8::/32"],
                    &[],
                    &[],
                    &["custom:global"],
                ),
            ],
            rewrite_sets: vec![
                RewriteSet {
                    id: "custom:global".into(),
                    label: "custom".into(),
                    rules: vec![
                        rule("nas.home.test", RewriteType::A, "192.168.1.50"),
                        rule("nas.home.test", RewriteType::Aaaa, "fd00::50"),
                        rule("*.lab.home.test", RewriteType::Cname, "nas.home.test"),
                        rule("*.home.test", RewriteType::A, "192.168.1.1"),
                        rule("special.lab.home.test", RewriteType::A, "192.168.1.60"),
                    ],
                },
                RewriteSet {
                    id: "custom:group:wide".into(),
                    label: "custom".into(),
                    rules: vec![rule("nas.home.test", RewriteType::A, "10.9.9.9")],
                },
            ],
            global_rewrite_set_ids: vec!["custom:global".into()],
            ..Default::default()
        }
    }

    #[test]
    fn most_specific_cidr_wins_and_group_replaces_global() {
        let t = table(&snapshot()).unwrap();
        let ads = wire("x.ads.example.test");
        let (p, g) = t.select("10.2.3.4".parse::<IpAddr>().unwrap());
        assert_eq!((p.group_id(), g), ("wide", Some(0)));
        assert!(matches!(p.check(&ads), Verdict::Blocked(_)));
        assert!(matches!(
            p.check(&wire("ok.ads.example.test")),
            Verdict::Allowed
        ));
        let (p, _) = t.select("10.1.3.4".parse::<IpAddr>().unwrap());
        assert_eq!(p.group_id(), "narrow");
        assert!(
            matches!(p.check(&ads), Verdict::Pass),
            "narrow selects no lists"
        );
        let (p, _) = t.select("::ffff:10.1.0.9".parse::<IpAddr>().unwrap());
        assert_eq!(p.group_id(), "narrow");
        let (p, _) = t.select("2001:db8::1".parse::<IpAddr>().unwrap());
        assert_eq!(p.group_id(), "narrow");
        let (p, g) = t.select("192.0.2.1".parse::<IpAddr>().unwrap());
        assert_eq!((p.group_id(), g), ("", None));
        assert!(matches!(p.check(&ads), Verdict::Blocked(_)));
    }

    #[test]
    fn rewrite_precedence_exact_wildcard_and_set_order() {
        let t = table(&snapshot()).unwrap();
        let (global, _) = t.select("192.0.2.1".parse::<IpAddr>().unwrap());
        match global.check(&wire("nas.home.test")) {
            Verdict::Rewrite(RewriteAnswer::Addrs { a, aaaa }) => {
                assert_eq!(a.len(), 1);
                assert_eq!(aaaa.len(), 1);
            }
            _ => panic!("expected addrs"),
        }
        assert!(matches!(
            global.check(&wire("x.lab.home.test")),
            Verdict::Rewrite(RewriteAnswer::Cname { .. })
        ));
        assert!(matches!(
            global.check(&wire("a.b.lab.home.test")),
            Verdict::Rewrite(RewriteAnswer::Cname { .. })
        ));
        assert!(matches!(
            global.check(&wire("special.lab.home.test")),
            Verdict::Rewrite(RewriteAnswer::Addrs { .. })
        ));
        assert!(matches!(
            global.check(&wire("printer.home.test")),
            Verdict::Rewrite(RewriteAnswer::Addrs { .. })
        ));
        assert!(
            matches!(
                global.check(&wire("lab.home.test")),
                Verdict::Rewrite(RewriteAnswer::Addrs { .. })
            ),
            "*.home.test covers lab.home.test"
        );
        assert!(
            matches!(global.check(&wire("home.test")), Verdict::Pass),
            "wildcard never matches its base"
        );
        let (wide, _) = t.select("10.2.3.4".parse::<IpAddr>().unwrap());
        match wide.check(&wire("nas.home.test")) {
            Verdict::Rewrite(RewriteAnswer::Addrs { a, .. }) => {
                assert_eq!(a[0].0.to_string(), "10.9.9.9")
            }
            _ => panic!("expected group rewrite"),
        }
    }

    #[test]
    fn invalid_snapshots_are_rejected_with_reason() {
        let mut s = snapshot();
        s.policy_groups[1].cidrs.push("10.0.0.0/8".into());
        assert_eq!(
            table(&s).err().unwrap(),
            "policy group narrow: cidr 10.0.0.0/8 also in group wide"
        );
        let mut s = snapshot();
        s.policy_groups[0].cidrs = vec!["10.0.0.1/8".into()];
        assert_eq!(
            table(&s).err().unwrap(),
            "policy group wide: invalid cidr 10.0.0.1/8"
        );
        let mut s = snapshot();
        s.policy_groups[0].blocklists.push(BlobRef {
            sha256: "nope".into(),
            size: 1,
            name: "x".into(),
        });
        assert_eq!(table(&s).err().unwrap(), "blob nope: missing");
        let mut s = snapshot();
        s.rewrite_sets[0]
            .rules
            .push(rule("nas.home.test", RewriteType::Cname, "other.test"));
        assert_eq!(
            table(&s).err().unwrap(),
            "rewrite set custom:global: nas.home.test has CNAME and other records"
        );
        let mut s = snapshot();
        s.rewrite_sets[0]
            .rules
            .push(rule("bad.test", RewriteType::A, "fd00::1"));
        assert_eq!(
            table(&s).err().unwrap(),
            "rewrite set custom:global: bad.test A value fd00::1 is not an IPv4 address"
        );
    }

    #[test]
    fn groups_sharing_a_filter_share_a_cache_partition() {
        let mut s = snapshot();
        s.policy_groups.push(group(
            "twin",
            &["172.16.0.0/12"],
            &[ads_blob().0],
            &["ok.ads.example.test"],
            &[],
        ));
        let t = table(&s).unwrap();
        let part = |ip: &str| t.select(ip.parse::<IpAddr>().unwrap()).0.cache_partition();
        assert_eq!(part("192.0.2.1"), 0, "global");
        assert_ne!(
            part("10.2.3.4"),
            part("10.1.3.4"),
            "different filters, different partitions"
        );
        assert_ne!(part("10.1.3.4"), 0);
        assert_eq!(
            part("10.2.3.4"),
            part("172.16.0.1"),
            "same lists and allowlist share"
        );
        assert_eq!(t.partition_keys().len(), 2);
    }
}
