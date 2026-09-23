//! Per-zone trigger lookup tables and cross-zone precedence.

use super::parse::{ParsedRpz, RpzAction, Trigger};
use crate::proto::RpzPolicyOverride;
use hickory_proto::rr::{Name, RData, Record};
use hickory_proto::serialize::binary::BinEncodable;
use ipnet::IpNet;
use rustc_hash::FxHashMap;
use std::net::IpAddr;
use std::sync::Arc;
use std::sync::atomic::{AtomicU64, Ordering};

type NameTable = FxHashMap<Box<[u8]>, RpzAction>;

#[derive(Default)]
struct IpTable {
    v4: FxHashMap<(u8, u32), RpzAction>,
    v6: FxHashMap<(u8, u128), RpzAction>,
    /// Present prefix lengths, longest first.
    v4_lens: Vec<u8>,
    v6_lens: Vec<u8>,
}

impl IpTable {
    fn insert(&mut self, net: IpNet, action: RpzAction) {
        let len = net.prefix_len();
        match net {
            IpNet::V4(n) => {
                self.v4
                    .entry((len, u32::from(n.network())))
                    .or_insert(action);
                if !self.v4_lens.contains(&len) {
                    self.v4_lens.push(len);
                    self.v4_lens.sort_unstable_by(|a, b| b.cmp(a));
                }
            }
            IpNet::V6(n) => {
                self.v6
                    .entry((len, u128::from(n.network())))
                    .or_insert(action);
                if !self.v6_lens.contains(&len) {
                    self.v6_lens.push(len);
                    self.v6_lens.sort_unstable_by(|a, b| b.cmp(a));
                }
            }
        }
    }

    fn is_empty(&self) -> bool {
        self.v4.is_empty() && self.v6.is_empty()
    }

    /// Longest matching prefix; no allocation.
    fn lookup(&self, ip: IpAddr) -> Option<&RpzAction> {
        match ip {
            IpAddr::V4(a) => {
                let a = u32::from(a);
                self.v4_lens
                    .iter()
                    .find_map(|&l| self.v4.get(&(l, a & (u32::MAX << (32 - l as u32)))))
            }
            IpAddr::V6(a) => {
                let a = match a.to_ipv4_mapped() {
                    Some(v4) => return self.lookup(IpAddr::V4(v4)),
                    None => u128::from(a),
                };
                self.v6_lens
                    .iter()
                    .find_map(|&l| self.v6.get(&(l, a & (u128::MAX << (128 - l as u32)))))
            }
        }
    }
}

fn lookup_name<'a>(
    exact: &'a NameTable,
    wild: &'a NameTable,
    wire: &[u8],
) -> Option<&'a RpzAction> {
    if let Some(a) = exact.get(wire) {
        return Some(a);
    }
    if wild.is_empty() {
        return None;
    }
    // Proper suffixes, deepest first.
    let mut off = 0usize;
    while off < wire.len() && wire[off] != 0 {
        off += 1 + wire[off] as usize;
        if let Some(a) = wire.get(off..).and_then(|s| wild.get(s)) {
            return Some(a);
        }
    }
    None
}

fn name_key(name: &Name) -> Option<Box<[u8]>> {
    name.to_lowercase()
        .to_bytes()
        .ok()
        .map(Vec::into_boxed_slice)
}

pub struct RpzZoneIndex {
    pub id: String,
    /// Preallocated telemetry identity; owns no rule tables or publication.
    pub identity: Arc<str>,
    pub origin: Name,
    pub soa: Record,
    pub serial: u32,
    pub records: u64,
    pub skipped: u64,
    pub policy_override: i32,
    pub hits: AtomicU64,
    qname_exact: NameTable,
    qname_wild: NameTable,
    nsdname_exact: NameTable,
    nsdname_wild: NameTable,
    client_ip: IpTable,
    resp_ip: IpTable,
    nsip: IpTable,
}

impl std::fmt::Debug for RpzZoneIndex {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("RpzZoneIndex")
            .field("id", &self.id)
            .field("origin", &self.origin)
            .field("serial", &self.serial)
            .finish_non_exhaustive()
    }
}

impl RpzZoneIndex {
    /// Builds the tables; the first rule for a trigger wins when a zone repeats one.
    pub fn build(id: &str, parsed: &ParsedRpz, policy_override: i32) -> Self {
        let mut z = RpzZoneIndex {
            id: id.to_owned(),
            identity: Arc::from(id),
            origin: parsed.origin.clone(),
            soa: parsed.soa.clone(),
            serial: parsed.serial,
            records: parsed.records,
            skipped: parsed.skipped,
            policy_override,
            hits: AtomicU64::new(0),
            qname_exact: Default::default(),
            qname_wild: Default::default(),
            nsdname_exact: Default::default(),
            nsdname_wild: Default::default(),
            client_ip: Default::default(),
            resp_ip: Default::default(),
            nsip: Default::default(),
        };
        for (trigger, action) in &parsed.rules {
            let action = action.clone();
            match trigger {
                Trigger::Qname { name, wildcard } | Trigger::Nsdname { name, wildcard } => {
                    let Some(key) = name_key(name) else { continue };
                    let table = match (matches!(trigger, Trigger::Qname { .. }), wildcard) {
                        (true, false) => &mut z.qname_exact,
                        (true, true) => &mut z.qname_wild,
                        (false, false) => &mut z.nsdname_exact,
                        (false, true) => &mut z.nsdname_wild,
                    };
                    table.entry(key).or_insert(action);
                }
                Trigger::ClientIp(net) => z.client_ip.insert(*net, action),
                Trigger::ResponseIp(net) => z.resp_ip.insert(*net, action),
                Trigger::Nsip(net) => z.nsip.insert(*net, action),
            }
        }
        z
    }

    pub fn has_query_triggers(&self) -> bool {
        !self.client_ip.is_empty() || !self.qname_exact.is_empty() || !self.qname_wild.is_empty()
    }

    /// Triggers that need the resolution result of the query name itself.
    pub fn has_response_triggers(&self) -> bool {
        !self.resp_ip.is_empty()
            || !self.nsdname_exact.is_empty()
            || !self.nsdname_wild.is_empty()
            || !self.nsip.is_empty()
    }

    fn has_qname_triggers(&self) -> bool {
        !self.qname_exact.is_empty() || !self.qname_wild.is_empty()
    }

    fn hit(&self) {
        self.hits.fetch_add(1, Ordering::Relaxed);
    }
}

pub struct RpzSet {
    pub zones: Vec<Arc<RpzZoneIndex>>,
    pub has_query_triggers: bool,
    /// Some zone needs a response-phase check: IP/NS triggers, or QNAME triggers that can match
    /// a CNAME target of the resolved chain.
    pub has_response_triggers: bool,
}

impl Default for RpzSet {
    fn default() -> Self {
        RpzSet::new(Vec::new())
    }
}

#[derive(Debug)]
pub enum QueryPhase<'a> {
    NoMatch,
    Hit {
        zone: usize,
        action: &'a RpzAction,
    },
    /// A query-phase hit in `zone`, but an earlier zone has response triggers that take
    /// precedence if they match the resolved answer.
    Deferred {
        zone: usize,
        action: &'a RpzAction,
    },
}

impl RpzSet {
    pub fn new(zones: Vec<Arc<RpzZoneIndex>>) -> Self {
        let has_query_triggers = zones.iter().any(|z| z.has_query_triggers());
        let has_response_triggers = zones
            .iter()
            .any(|z| z.has_response_triggers() || z.has_qname_triggers());
        RpzSet {
            zones,
            has_query_triggers,
            has_response_triggers,
        }
    }

    /// CLIENT-IP and QNAME triggers. `qname_wire_lower` is the uncompressed lowercase wire name.
    /// Allocation-free.
    pub fn check_query(&self, qname_wire_lower: &[u8], client: IpAddr) -> QueryPhase<'_> {
        for (i, z) in self.zones.iter().enumerate() {
            let hit = z
                .client_ip
                .lookup(client)
                .or_else(|| lookup_name(&z.qname_exact, &z.qname_wild, qname_wire_lower));
            if let Some(action) = hit {
                z.hit();
                return if self.zones[..i].iter().any(|e| e.has_response_triggers()) {
                    QueryPhase::Deferred { zone: i, action }
                } else {
                    QueryPhase::Hit { zone: i, action }
                };
            }
        }
        QueryPhase::NoMatch
    }

    /// Response-phase triggers over zones `0..upto_zone`: QNAME on CNAME targets (`chain[1..]`),
    /// response IP on A/AAAA answers, NSDNAME, NSIP.
    pub fn check_response(
        &self,
        upto_zone: usize,
        chain: &[Name],
        answers: &[Record],
        ns_names: &[Name],
        ns_addrs: &[IpAddr],
    ) -> Option<(usize, &RpzAction)> {
        let targets: Vec<Box<[u8]>> = chain.iter().skip(1).filter_map(name_key).collect();
        let ns: Vec<Box<[u8]>> = ns_names.iter().filter_map(name_key).collect();
        let ips: Vec<IpAddr> = answers
            .iter()
            .filter_map(|r| match &r.data {
                RData::A(a) => Some(IpAddr::V4(a.0)),
                RData::AAAA(a) => Some(IpAddr::V6(a.0)),
                _ => None,
            })
            .collect();
        for (i, z) in self.zones.iter().enumerate().take(upto_zone) {
            let hit = targets
                .iter()
                .find_map(|t| lookup_name(&z.qname_exact, &z.qname_wild, t))
                .or_else(|| ips.iter().find_map(|ip| z.resp_ip.lookup(*ip)))
                .or_else(|| {
                    ns.iter()
                        .find_map(|n| lookup_name(&z.nsdname_exact, &z.nsdname_wild, n))
                })
                .or_else(|| ns_addrs.iter().find_map(|ip| z.nsip.lookup(*ip)));
            if let Some(action) = hit {
                z.hit();
                return Some((i, action));
            }
        }
        None
    }

    /// Applies the zone's policy override; `None` when the zone is DISABLED (logged, no action).
    pub fn effective_action<'a>(&'a self, zone: usize, action: &'a RpzAction) -> Option<RpzAction> {
        let ov = self.zones.get(zone).map_or(0, |z| z.policy_override);
        match RpzPolicyOverride::try_from(ov).unwrap_or(RpzPolicyOverride::Given) {
            RpzPolicyOverride::Given => Some(action.clone()),
            RpzPolicyOverride::Disabled => None,
            RpzPolicyOverride::Nxdomain => Some(RpzAction::Nxdomain),
            RpzPolicyOverride::Nodata => Some(RpzAction::Nodata),
            RpzPolicyOverride::Passthru => Some(RpzAction::Passthru),
            RpzPolicyOverride::Drop => Some(RpzAction::Drop),
            RpzPolicyOverride::TcpOnly => Some(RpzAction::TcpOnly),
        }
    }
}
