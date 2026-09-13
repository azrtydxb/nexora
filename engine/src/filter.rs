//! Blocklist/allowlist matching on wire-format name suffixes, and block replies.

use crate::edns::ReplyOpt;
use crate::proto::{ConfigSnapshot, RewriteSet, RewriteType};
use crate::snapshot::{BlobSource, SnapshotError};
use crate::wire::{self, NameKey, QueryView, SynthAnswer};
use hickory_proto::rr::Name;
use rustc_hash::{FxHashMap, FxHashSet};
use std::collections::HashMap;
use std::io::Read;
use std::net::{IpAddr, Ipv4Addr, Ipv6Addr};
use std::sync::Arc;

const TYPE_A: u16 = 1;
const TYPE_AAAA: u16 = 28;
const MAX_TEXT_LEN: usize = 253;
const MAX_LABEL_LEN: usize = 63;
/// Largest decompressed blob accepted; bounds memory against a zstd bomb.
const MAX_BLOB_BYTES: u64 = 512 << 20;

/// Discriminants equal the proto `BlockMode` values.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum BlockMode {
    NullIp = 1,
    NxDomain = 2,
    Refused = 3,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum FilterDecision {
    None,
    Blocked,
    Allowed,
}

#[derive(Debug, Default, PartialEq, Eq)]
pub struct ListStats {
    pub entries: usize,
    pub invalid_lines: usize,
}

pub struct FilterSet {
    pub mode: BlockMode,
    pub ttl: u32,
    blocked: FxHashSet<Box<[u8]>>,
    allowed: FxHashSet<Box<[u8]>>,
}

impl FilterSet {
    pub fn empty() -> FilterSet {
        FilterSet {
            mode: BlockMode::NullIp,
            ttl: 0,
            blocked: FxHashSet::default(),
            allowed: FxHashSet::default(),
        }
    }

    /// Builds from decompressed lists of one domain per line.
    pub fn build(
        blocklists: &[Vec<u8>],
        allowlists: &[Vec<u8>],
        mode: BlockMode,
        ttl: u32,
    ) -> (FilterSet, ListStats) {
        let mut stats = ListStats::default();
        let mut load = |lists: &[Vec<u8>], set: &mut FxHashSet<Box<[u8]>>| {
            for line in lists.iter().flat_map(|l| l.split(|&b| b == b'\n')) {
                if line.is_empty() {
                    continue;
                }
                match domain_to_wire(line) {
                    Some(name) => {
                        set.insert(name);
                    }
                    None => stats.invalid_lines += 1,
                }
            }
        };
        let mut set = FilterSet {
            mode,
            ttl,
            ..FilterSet::empty()
        };
        load(blocklists, &mut set.blocked);
        load(allowlists, &mut set.allowed);
        stats.entries = set.blocked.len() + set.allowed.len();
        (set, stats)
    }

    /// Walks the label suffixes of a lowercase wire name, longest first; the
    /// first allowlisted suffix wins, then the first blocklisted one.
    pub fn decide(&self, name_wire: &[u8]) -> FilterDecision {
        if self.blocked.is_empty() && self.allowed.is_empty() {
            return FilterDecision::None;
        }
        let mut pos = 0;
        let mut decision = FilterDecision::None;
        while let Some(&len) = name_wire.get(pos) {
            if len == 0 {
                break;
            }
            let suffix = &name_wire[pos..];
            if self.allowed.contains(suffix) {
                return FilterDecision::Allowed;
            }
            if decision == FilterDecision::None && self.blocked.contains(suffix) {
                if self.allowed.is_empty() {
                    return FilterDecision::Blocked;
                }
                // A shorter allowlisted suffix still beats this block.
                decision = FilterDecision::Blocked;
            }
            pos += 1 + usize::from(len);
        }
        decision
    }

    /// True when any CNAME target is blocked.
    pub fn cloaked(&self, cname_targets: &[NameKey]) -> bool {
        cname_targets
            .iter()
            .any(|t| self.decide(t.as_wire()) == FilterDecision::Blocked)
    }

    /// Writes the configured block reply; returns 0 when `out` is too small.
    pub fn write_block_reply(
        &self,
        q: &QueryView<'_>,
        out: &mut [u8],
        opt: Option<&ReplyOpt>,
    ) -> usize {
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
        let valid = !label.is_empty()
            && label.len() <= MAX_LABEL_LEN
            && label[0] != b'-'
            && label[label.len() - 1] != b'-'
            && label
                .iter()
                .all(|b| b.is_ascii_alphanumeric() || *b == b'-' || *b == b'_');
        if !valid {
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
    Blocked,
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
    filter: Arc<FilterSet>,
    rewrites: RewriteTable,
    /// Identifies `filter` among the runtime's distinct filter sets (global = 0).
    /// Cached upstream answers are only valid for the filter set whose CNAME
    /// cloaking check admitted them, so it is part of the cache key.
    cache_partition: u16,
}

impl EffectivePolicy {
    pub fn group_id(&self) -> &str {
        &self.group_id
    }

    pub fn filter(&self) -> &FilterSet {
        &self.filter
    }

    pub fn cache_partition(&self) -> u16 {
        self.cache_partition
    }

    pub fn check(&self, wire_name: &[u8]) -> Verdict<'_> {
        if let Some(r) = self.rewrites.lookup(wire_name) {
            return Verdict::Rewrite(r);
        }
        match self.filter.decide(wire_name) {
            FilterDecision::Blocked => Verdict::Blocked,
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
}

impl PolicyTable {
    /// Only global policy: no groups and no rewrites.
    pub fn global_only(global_filter: Arc<FilterSet>) -> PolicyTable {
        PolicyTable {
            groups: Box::new([]),
            global: EffectivePolicy {
                group_id: "".into(),
                filter: global_filter,
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

    /// Validates and builds the policy section of `snap`; `global_filter`
    /// (M1's `FilterConfig` selection) supplies the block mode and TTL.
    pub fn build(
        snap: &ConfigSnapshot,
        global_filter: Arc<FilterSet>,
        blobs: &dyn BlobSource,
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

        let mut filters: HashMap<String, (Arc<FilterSet>, u16)> = HashMap::new();
        let mut partition_keys = Vec::new();
        let mut groups = Vec::with_capacity(snap.policy_groups.len());
        let mut v4: FxHashMap<(u8, u32), u16> = FxHashMap::default();
        let mut v6: FxHashMap<(u8, u128), u16> = FxHashMap::default();
        for (index, g) in snap.policy_groups.iter().enumerate() {
            let index = index as u16;
            let rewrites = merged(&g.rewrite_set_ids, &|id| {
                format!("policy group {}: unknown rewrite set {id}", g.id)
            })?;

            let mut hashes: Vec<&str> = g.blocklists.iter().map(|b| b.sha256.as_str()).collect();
            hashes.sort_unstable();
            let key = format!("{}|{}", hashes.join(","), g.allowlist.join(","));
            let (filter, cache_partition) = match filters.get(&key) {
                Some((f, p)) => (f.clone(), *p),
                None => {
                    let decoded = g
                        .blocklists
                        .iter()
                        .map(|r| {
                            let bytes = blobs.read(r)?;
                            decode_blob(&bytes).map_err(|e| SnapshotError::Blob {
                                sha256: r.sha256.clone(),
                                reason: format!("zstd: {e}"),
                            })
                        })
                        .collect::<Result<Vec<_>, SnapshotError>>()
                        .map_err(|e| format!("policy group {}: {e}", g.id))?;
                    let allow = g.allowlist.join("\n").into_bytes();
                    let filter = Arc::new(
                        FilterSet::build(&decoded, &[allow], global_filter.mode, global_filter.ttl)
                            .0,
                    );
                    partition_keys.push(key.clone());
                    let partition = partition_keys.len() as u16;
                    filters.insert(key, (filter.clone(), partition));
                    (filter, partition)
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
                        v4.insert((n.prefix_len(), u32::from(n.network())), index)
                    }
                    ipnet::IpNet::V6(n) => {
                        v6.insert((n.prefix_len(), u128::from(n.network())), index)
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
                rewrites,
                cache_partition,
            });
        }

        let mut table = PolicyTable::global_only(global_filter);
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
    fn global() -> Arc<FilterSet> {
        Arc::new(
            FilterSet::build(
                &[b"ads.example.test\n".to_vec()],
                &[],
                BlockMode::NullIp,
                60,
            )
            .0,
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
        let t = PolicyTable::build(&snapshot(), global(), &blobs()).unwrap();
        let ads = wire("x.ads.example.test");
        let (p, g) = t.select("10.2.3.4".parse::<IpAddr>().unwrap());
        assert_eq!((p.group_id(), g), ("wide", Some(0)));
        assert!(matches!(p.check(&ads), Verdict::Blocked));
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
        assert!(matches!(p.check(&ads), Verdict::Blocked));
    }

    #[test]
    fn rewrite_precedence_exact_wildcard_and_set_order() {
        let t = PolicyTable::build(&snapshot(), global(), &blobs()).unwrap();
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
            PolicyTable::build(&s, global(), &blobs()).err().unwrap(),
            "policy group narrow: cidr 10.0.0.0/8 also in group wide"
        );
        let mut s = snapshot();
        s.policy_groups[0].cidrs = vec!["10.0.0.1/8".into()];
        assert_eq!(
            PolicyTable::build(&s, global(), &blobs()).err().unwrap(),
            "policy group wide: invalid cidr 10.0.0.1/8"
        );
        let mut s = snapshot();
        s.policy_groups[0].blocklists.push(BlobRef {
            sha256: "nope".into(),
            size: 1,
            name: "x".into(),
        });
        assert_eq!(
            PolicyTable::build(&s, global(), &blobs()).err().unwrap(),
            "policy group wide: blob nope: missing"
        );
        let mut s = snapshot();
        s.rewrite_sets[0]
            .rules
            .push(rule("nas.home.test", RewriteType::Cname, "other.test"));
        assert_eq!(
            PolicyTable::build(&s, global(), &blobs()).err().unwrap(),
            "rewrite set custom:global: nas.home.test has CNAME and other records"
        );
        let mut s = snapshot();
        s.rewrite_sets[0]
            .rules
            .push(rule("bad.test", RewriteType::A, "fd00::1"));
        assert_eq!(
            PolicyTable::build(&s, global(), &blobs()).err().unwrap(),
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
        let t = PolicyTable::build(&s, global(), &blobs()).unwrap();
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
