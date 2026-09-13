//! The miss path after the response cache: route selection (longest forward zone, else the
//! resolution mode), resolution, DNSSEC validation, RPZ policy and response building.

use super::budget::{Limit, WorkBudget};
use super::dnssec::DnssecRuntime;
use super::dnssec::forward::{fetch_via, fetched_set};
use super::dnssec::validator::{
    FetchError, FetchedSet, Fetcher, Security, ValidationInput, under_nta,
};
use super::iterate::{RecursionError, RecursionParams, Resolution};
use super::metrics::RecursorMetrics;
use super::rpz::apply::{EDE_FORGED, PolicyOutcome, apply_action};
use super::rpz::manager::{RpzZoneConfig, zone_configs};
use super::rpz::parse::RpzAction;
use super::transport::OutboundQuery;
use super::{EDNS_BUFFER, LocalBoxFuture, RecursorState};
use crate::edns::Transport;
use crate::snapshot::BlobSource;
use crate::wire::QueryView;
use crate::{clock, proto};
use bytes::Bytes;
use hickory_proto::op::{Edns, Message, MessageType, OpCode, Query, ResponseCode};
use hickory_proto::rr::{Name, RData, Record, RecordType};
use hickory_proto::serialize::binary::{BinDecodable, BinEncodable};
use rand::Rng;
use rustc_hash::FxHashMap;
use sha2::{Digest, Sha256};
use std::net::{IpAddr, SocketAddr};
use std::sync::Arc;
use std::time::Duration;

pub use super::Ede;
pub use super::rpz::RpzPending;

/// Per-server timeout of a forward-zone exchange (forward-zone servers recurse).
const FORWARD_ZONE_TIMEOUT: Duration = Duration::from_millis(2000);

#[derive(Clone, Copy, Debug, PartialEq, Eq, Default)]
pub enum Mode {
    #[default]
    Forward,
    Recursive,
}

#[derive(Debug)]
pub struct ForwardZone {
    /// Lowercase.
    pub domain: Name,
    pub servers: Vec<SocketAddr>,
    pub validate: bool,
}

#[derive(Default, Debug)]
pub struct ForwardZones {
    /// Uncompressed lowercase wire name -> index into `zones`.
    by_wire: FxHashMap<Box<[u8]>, usize>,
    pub zones: Vec<ForwardZone>,
}

impl ForwardZones {
    pub fn build(z: &[proto::ForwardZone]) -> Result<Self, String> {
        let mut out = ForwardZones::default();
        for (i, fz) in z.iter().enumerate() {
            let domain = Name::from_ascii(&fz.domain)
                .map_err(|e| format!("forward_zones[{i}].domain: {e}"))?
                .to_lowercase();
            let servers = fz
                .addresses
                .iter()
                .map(|a| {
                    a.parse::<SocketAddr>()
                        .map_err(|_| format!("forward_zones[{i}].addresses: not ip:port: {a}"))
                })
                .collect::<Result<Vec<_>, _>>()?;
            let key = domain
                .to_bytes()
                .map_err(|e| format!("forward_zones[{i}].domain: {e}"))?;
            out.by_wire.insert(key.into_boxed_slice(), out.zones.len());
            out.zones.push(ForwardZone {
                domain,
                servers,
                validate: fz.validate,
            });
        }
        Ok(out)
    }

    /// The deepest forward zone at or above the name; allocation-free.
    pub fn longest_match(&self, qname_wire_lower: &[u8]) -> Option<&ForwardZone> {
        if self.zones.is_empty() {
            return None;
        }
        let mut off = 0usize;
        loop {
            let rest = qname_wire_lower.get(off..)?;
            if let Some(&i) = self.by_wire.get(rest) {
                return Some(&self.zones[i]);
            }
            let len = usize::from(*rest.first()?);
            if len == 0 {
                return None;
            }
            off += 1 + len;
        }
    }
}

pub enum Route<'a> {
    Forward,
    Recursive,
    ForwardZone(&'a ForwardZone),
}

pub struct ResolutionRuntime {
    pub mode: Mode,
    pub params: Arc<RecursionParams>,
    /// RFC 8198 answers from validated NSEC/NSEC3 records.
    pub aggressive_nsec: bool,
    pub forward_zones: ForwardZones,
    pub dnssec: Arc<DnssecRuntime>,
    /// RPZ zones in precedence order, handed to the `RpzManager` by `RecursorState::sync`.
    pub rpz: Vec<RpzZoneConfig>,
    pub config_key: String,
}

impl Default for ResolutionRuntime {
    /// Forward mode, no forward zones, no validation, no RPZ.
    fn default() -> Self {
        ResolutionRuntime {
            mode: Mode::Forward,
            params: Arc::new(RecursionParams::from_config(None)),
            aggressive_nsec: false,
            forward_zones: ForwardZones::default(),
            dnssec: Arc::default(),
            rpz: Vec::new(),
            config_key: String::new(),
        }
    }
}

impl ResolutionRuntime {
    pub fn build(s: &proto::ConfigSnapshot, blobs: &dyn BlobSource) -> Result<Self, String> {
        let mode = match proto::ResolutionMode::try_from(s.resolution_mode) {
            Ok(proto::ResolutionMode::Recursive) => Mode::Recursive,
            _ => Mode::Forward,
        };
        Ok(ResolutionRuntime {
            mode,
            params: Arc::new(RecursionParams::from_config(s.recursion.as_ref())),
            aggressive_nsec: s.recursion.as_ref().is_some_and(|r| r.aggressive_nsec),
            forward_zones: ForwardZones::build(&s.forward_zones)?,
            dnssec: Arc::new(DnssecRuntime::build(s)?),
            rpz: zone_configs(s, blobs)?,
            config_key: config_key(s),
        })
    }

    pub fn route(&self, qname_wire_lower: &[u8]) -> Route<'_> {
        match self.forward_zones.longest_match(qname_wire_lower) {
            Some(z) => Route::ForwardZone(z),
            None if self.mode == Mode::Recursive => Route::Recursive,
            None => Route::Forward,
        }
    }

    /// Whether answers on `route` are DNSSEC-validated (for clients that do not set CD).
    pub fn validates(&self, route: &Route<'_>) -> bool {
        self.dnssec.validation
            && match route {
                Route::Recursive => true,
                Route::ForwardZone(z) => z.validate,
                Route::Forward => self.dnssec.validate_forwarded,
            }
    }

    /// Whether any route validates, i.e. whether trust anchors are in use.
    pub fn validates_any_route(&self) -> bool {
        self.dnssec.validation
            && (self.mode == Mode::Recursive
                || self.dnssec.validate_forwarded
                || self.forward_zones.zones.iter().any(|z| z.validate))
    }
}

/// SHA-256 hex over the prost encoding of resolution_mode, recursion, forward_zones, dnssec,
/// rpz_zones and dnssec_validate_forwarded: the cache-invalidation key of the M3 settings.
pub fn config_key(s: &proto::ConfigSnapshot) -> String {
    use prost::Message as _;
    fn part(h: &mut Sha256, bytes: &[u8]) {
        h.update((bytes.len() as u32).to_be_bytes());
        h.update(bytes);
    }
    let mut h = Sha256::new();
    h.update(s.resolution_mode.to_be_bytes());
    part(
        &mut h,
        &s.recursion
            .as_ref()
            .map(|r| r.encode_to_vec())
            .unwrap_or_default(),
    );
    for z in &s.forward_zones {
        part(&mut h, &z.encode_to_vec());
    }
    part(
        &mut h,
        &s.dnssec
            .as_ref()
            .map(|d| d.encode_to_vec())
            .unwrap_or_default(),
    );
    for z in &s.rpz_zones {
        part(&mut h, &z.encode_to_vec());
    }
    h.update([u8::from(s.dnssec_validate_forwarded)]);
    hex::encode(h.finalize())
}

/// Sends a complete DNS query (the client's bytes on the forward route) to the global upstreams.
pub trait ForwardUpstream {
    fn forward<'a>(&'a self, query_wire: &'a [u8]) -> LocalBoxFuture<'a, Result<Bytes, String>>;
}

pub struct MissQuery<'a> {
    /// Lowercase.
    pub qname: Name,
    pub qtype: RecordType,
    pub client_ip: IpAddr,
    pub dnssec_ok: bool,
    pub checking_disabled: bool,
    pub authentic_data: bool,
    pub over_tcp: bool,
    /// The client's query bytes.
    pub query: &'a [u8],
    pub rpz: RpzPending,
}

impl<'a> MissQuery<'a> {
    /// `None` when the name does not decode.
    pub fn from_view(
        q: &QueryView<'_>,
        query: &'a [u8],
        client_ip: IpAddr,
        transport: Transport,
        rpz: RpzPending,
    ) -> Option<Self> {
        Some(MissQuery {
            qname: Name::from_bytes(q.key.as_wire()).ok()?,
            qtype: RecordType::from(q.qtype),
            client_ip,
            dnssec_ok: q.do_bit(),
            checking_disabled: q.cd(),
            authentic_data: q.flags & 0x0020 != 0,
            over_tcp: transport != Transport::Udp,
            query,
            rpz,
        })
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum RouteTaken {
    Forward = 0,
    Recursive = 1,
    ForwardZone = 2,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum SecurityTag {
    None = 0,
    Secure = 1,
    Insecure = 2,
    Bogus = 3,
    Indeterminate = 4,
}

pub struct MissAnswer {
    /// The full response: the upstream's bytes on the non-validating forward route, else ID 0, a
    /// lowercase question and no OPT.
    pub wire: Bytes,
    pub cacheable: bool,
    /// No usable answer: the caller serves stale data or SERVFAIL (`wire` is a SERVFAIL).
    pub failed: bool,
    pub ede: Option<Ede>,
    /// RPZ DROP: nothing is sent.
    pub drop: bool,
    pub route: RouteTaken,
    pub security: SecurityTag,
    /// 0 none, else `RpzAction::log_code`.
    pub rpz_action: u8,
}

/// Fetches the chain of trust on the route each name resolves through.
pub struct RoutedFetcher<'a> {
    pub rt: &'a ResolutionRuntime,
    pub state: &'a RecursorState,
    pub upstream: &'a dyn ForwardUpstream,
    pub budget: &'a WorkBudget,
}

impl Fetcher for RoutedFetcher<'_> {
    fn fetch<'a>(
        &'a self,
        name: &'a Name,
        rtype: RecordType,
    ) -> LocalBoxFuture<'a, Result<FetchedSet, FetchError>> {
        Box::pin(async move {
            let wire = name_wire(name);
            match self.rt.route(&wire) {
                Route::Recursive => {
                    match self
                        .state
                        .recursor
                        .fetch(name, rtype, &self.rt.params, self.budget)
                        .await
                    {
                        Ok(r) => Ok(FetchedSet {
                            rcode: r.rcode,
                            answers: r.answers,
                            authorities: r.authorities,
                        }),
                        Err(RecursionError::Limit(_)) => Err(FetchError::Limit),
                        Err(_) => Err(FetchError::Unreachable),
                    }
                }
                Route::ForwardZone(z) => forward_zone_exchange(self.state, z, name, rtype)
                    .await
                    .map(|m| FetchedSet {
                        rcode: m.metadata.response_code,
                        answers: m.answers,
                        authorities: m.authorities,
                    })
                    .map_err(|_| FetchError::Unreachable),
                Route::Forward => {
                    let up = self.upstream;
                    fetch_via(
                        move |q: Vec<u8>| async move { up.forward(&q).await },
                        name,
                        rtype,
                    )
                    .await
                }
            }
        })
    }
}

/// An answer before policy: sections decoded, or the upstream's bytes on the plain forward route.
struct Resolved {
    rcode: ResponseCode,
    answers: Vec<Record>,
    authorities: Vec<Record>,
    ns_names: Vec<Name>,
    ns_addrs: Vec<IpAddr>,
    raw: Option<Bytes>,
    route: RouteTaken,
    security: SecurityTag,
    ede: Option<Ede>,
}

impl Resolved {
    fn new(
        route: RouteTaken,
        rcode: ResponseCode,
        answers: Vec<Record>,
        auth: Vec<Record>,
    ) -> Self {
        Resolved {
            rcode,
            answers,
            authorities: auth,
            ns_names: Vec::new(),
            ns_addrs: Vec::new(),
            raw: None,
            route,
            security: SecurityTag::None,
            ede: None,
        }
    }

    fn from_recursion(r: Resolution) -> Self {
        Resolved {
            ns_names: r.ns_names,
            ns_addrs: r.ns_addrs,
            ..Resolved::new(RouteTaken::Recursive, r.rcode, r.answers, r.authorities)
        }
    }

    /// Decodes `raw` into the sections (RPZ response checks); false when it does not decode.
    fn decode(&mut self) -> bool {
        let Some(raw) = &self.raw else {
            return true;
        };
        match Message::from_vec(raw) {
            Ok(m) => {
                self.rcode = m.metadata.response_code;
                self.answers = m.answers;
                self.authorities = m.authorities;
                true
            }
            Err(_) => false,
        }
    }
}

enum Failure {
    /// No answer: stale data may be served.
    Unresolved(RouteTaken, Ede),
    /// Validation failed: SERVFAIL is the answer.
    Invalid(RouteTaken, SecurityTag, Ede),
}

pub async fn resolve_miss(
    rt: &ResolutionRuntime,
    state: &RecursorState,
    upstream: &dyn ForwardUpstream,
    q: &MissQuery<'_>,
) -> MissAnswer {
    let set = state.rpz.set.load_full();
    let default_route = route_taken(&rt.route(&name_wire(&q.qname)));
    // A query-phase action that still needs resolution (normally a local-data CNAME).
    if let RpzPending::Apply { zone, action } = &q.rpz
        && let Some(z) = set.zones.get(*zone)
    {
        match apply_action(&q.qname, q.qtype, q.over_tcp, z, action) {
            PolicyOutcome::Passthru => {}
            PolicyOutcome::ChaseCname { cname, target } => {
                let ede = Ede::new(EDE_FORGED, format!("rpz {}", z.origin));
                return chase(rt, state, upstream, q, *cname, &target, ede, action).await;
            }
            outcome => return policy_answer(outcome, action, default_route),
        }
    }
    let mut r = match resolve_name(rt, state, upstream, q, &q.qname, true).await {
        Ok(r) => r,
        Err(f) => return failure_answer(state, q, f),
    };
    let deferred = matches!(q.rpz, RpzPending::Deferred { .. });
    let mut rpz_action = 0;
    if (set.has_response_triggers || deferred) && !set.zones.is_empty() && r.decode() {
        let upto = match &q.rpz {
            RpzPending::Deferred { zone, .. } => *zone,
            _ => set.zones.len(),
        };
        let chain = chain_names(&q.qname, &r.answers);
        let hit = set
            .check_response(upto, &chain, &r.answers, &r.ns_names, &r.ns_addrs)
            .map(|(zone, action)| (zone, action.clone()))
            .or_else(|| match &q.rpz {
                RpzPending::Deferred { zone, action } => Some((*zone, action.clone())),
                _ => None,
            });
        if let Some((zone, action)) = hit
            && let Some(z) = set.zones.get(zone)
        {
            let Some(action) = set.effective_action(zone, &action) else {
                rpz_action = RPZ_DISABLED;
                return finish(q, r, rpz_action);
            };
            match apply_action(&q.qname, q.qtype, q.over_tcp, z, &action) {
                PolicyOutcome::Passthru => rpz_action = action.log_code(),
                PolicyOutcome::ChaseCname { cname, target } => {
                    let ede = Ede::new(EDE_FORGED, format!("rpz {}", z.origin));
                    return chase(rt, state, upstream, q, *cname, &target, ede, &action).await;
                }
                outcome => return policy_answer(outcome, &action, r.route),
            }
        }
    }
    finish(q, r, rpz_action)
}

/// `QueryRecord.rpz_action` of a hit in a zone whose policy override is DISABLED.
pub const RPZ_DISABLED: u8 = 7;

fn finish(q: &MissQuery<'_>, mut r: Resolved, rpz_action: u8) -> MissAnswer {
    let wire = match r.raw.take() {
        Some(bytes) => bytes,
        None => Bytes::from(build_response(
            q,
            r.rcode,
            &r.answers,
            &r.authorities,
            r.security == SecurityTag::Secure,
        )),
    };
    MissAnswer {
        wire,
        cacheable: (rpz_action == 0 || rpz_action == RPZ_DISABLED)
            && matches!(r.rcode, ResponseCode::NoError | ResponseCode::NXDomain),
        failed: false,
        ede: r.ede,
        drop: false,
        route: r.route,
        security: r.security,
        rpz_action,
    }
}

/// Answers `cname` (RPZ local data) followed by the resolution of its target; never cached.
#[allow(clippy::too_many_arguments)]
async fn chase(
    rt: &ResolutionRuntime,
    state: &RecursorState,
    upstream: &dyn ForwardUpstream,
    q: &MissQuery<'_>,
    cname: Record,
    target: &Name,
    ede: Ede,
    action: &RpzAction,
) -> MissAnswer {
    let mut r = match resolve_name(rt, state, upstream, q, &target.to_lowercase(), false).await {
        Ok(r) => r,
        Err(f) => {
            let mut a = failure_answer(state, q, f);
            a.rpz_action = action.log_code();
            return a;
        }
    };
    r.decode();
    let mut answers = Vec::with_capacity(r.answers.len() + 1);
    answers.push(cname);
    answers.append(&mut r.answers);
    MissAnswer {
        // RPZ rewrites clear AD
        wire: Bytes::from(build_response(q, r.rcode, &answers, &r.authorities, false)),
        cacheable: false,
        failed: false,
        ede: Some(ede),
        drop: false,
        route: r.route,
        security: r.security,
        rpz_action: action.log_code(),
    }
}

/// Resolves `name`/`q.qtype` on its route and validates the answer when the route validates.
/// `client_bytes`: `name` is the client's question, so the plain forward route sends `q.query`.
async fn resolve_name(
    rt: &ResolutionRuntime,
    state: &RecursorState,
    upstream: &dyn ForwardUpstream,
    q: &MissQuery<'_>,
    name: &Name,
    client_bytes: bool,
) -> Result<Resolved, Failure> {
    let route = rt.route(&name_wire(name));
    let taken = route_taken(&route);
    let validating = rt.validates(&route) && !q.checking_disabled;
    let now = clock::unix_now().max(0) as u64;
    let m = &state.metrics;
    let unreachable = || Failure::Unresolved(taken, Ede::new(22, "no reachable authority"));
    if validating
        && rt.aggressive_nsec
        && !under_nta(name, &rt.dnssec.ntas, now)
        && let Some((rcode, authorities)) = state.validator.nsec.synthesize(name, q.qtype, now)
    {
        return Ok(Resolved {
            security: SecurityTag::Secure,
            ..Resolved::new(taken, rcode, Vec::new(), authorities)
        });
    }
    let p = &rt.params;
    let mut r = match &route {
        Route::Recursive => {
            RecursorMetrics::inc(&m.resolutions_recursive);
            let budget = WorkBudget::new(p.max_upstream_queries, p.max_delegation_depth);
            match state.recursor.resolve(name, q.qtype, p, &budget).await {
                Ok(res) => Resolved::from_recursion(res),
                Err(e) => return Err(Failure::Unresolved(taken, recursion_ede(m, e))),
            }
        }
        Route::ForwardZone(z) => {
            RecursorMetrics::inc(&m.resolutions_forward_zone);
            let msg = forward_zone_exchange(state, z, name, q.qtype)
                .await
                .map_err(|_| unreachable())?;
            Resolved::new(
                taken,
                msg.metadata.response_code,
                msg.answers,
                msg.authorities,
            )
        }
        Route::Forward if !validating && client_bytes => {
            let bytes = upstream.forward(q.query).await.map_err(|_| unreachable())?;
            Resolved {
                raw: Some(bytes),
                ..Resolved::new(taken, ResponseCode::NoError, Vec::new(), Vec::new())
            }
        }
        Route::Forward => {
            let query = if validating {
                super::dnssec::forward::validation_query(name, q.qtype)
            } else {
                plain_query(name, q.qtype, q.dnssec_ok)
            };
            let reply = upstream.forward(&query).await.map_err(|_| unreachable())?;
            let set = fetched_set(&reply, name, q.qtype).map_err(|_| unreachable())?;
            Resolved::new(taken, set.rcode, set.answers, set.authorities)
        }
    };
    if !validating {
        return Ok(r);
    }
    let trust = state.anchors.trust_points();
    let budget = WorkBudget::new(p.max_upstream_queries, p.max_delegation_depth);
    let fetcher = RoutedFetcher {
        rt,
        state,
        upstream,
        budget: &budget,
    };
    let input = ValidationInput {
        qname: name,
        qtype: q.qtype,
        rcode: r.rcode,
        answers: &r.answers,
        authorities: &r.authorities,
    };
    let v = state
        .validator
        .validate(&input, &trust, &rt.dnssec.ntas, &fetcher, now)
        .await;
    match v.security {
        Security::Secure => {
            r.security = SecurityTag::Secure;
            if let Some(cap) = v.ttl_cap {
                for rec in r.answers.iter_mut().chain(r.authorities.iter_mut()) {
                    rec.ttl = rec.ttl.min(cap);
                }
            }
        }
        Security::Insecure(ede) => {
            r.security = SecurityTag::Insecure;
            r.ede = ede;
        }
        Security::Bogus(ede) => return Err(Failure::Invalid(taken, SecurityTag::Bogus, ede)),
        Security::Indeterminate(ede) => {
            return Err(Failure::Invalid(taken, SecurityTag::Indeterminate, ede));
        }
    }
    Ok(r)
}

fn failure_answer(state: &RecursorState, q: &MissQuery<'_>, f: Failure) -> MissAnswer {
    let servfail = Bytes::from(build_response(q, ResponseCode::ServFail, &[], &[], false));
    match f {
        Failure::Unresolved(route, ede) => {
            RecursorMetrics::inc(&state.metrics.resolution_failures);
            MissAnswer {
                wire: servfail,
                cacheable: false,
                failed: true,
                ede: Some(ede),
                drop: false,
                route,
                security: SecurityTag::None,
                rpz_action: 0,
            }
        }
        Failure::Invalid(route, security, ede) => MissAnswer {
            wire: servfail,
            cacheable: false,
            failed: false,
            ede: Some(ede),
            drop: false,
            route,
            security,
            rpz_action: 0,
        },
    }
}

/// The answer of an RPZ action that needs no resolution.
fn policy_answer(outcome: PolicyOutcome, action: &RpzAction, route: RouteTaken) -> MissAnswer {
    let (wire, ede, drop) = match outcome {
        PolicyOutcome::Respond { wire, ede } => (Bytes::from(wire), Some(ede), false),
        PolicyOutcome::Truncate { wire } => (Bytes::from(wire), None, false),
        _ => (Bytes::new(), None, true),
    };
    MissAnswer {
        wire,
        cacheable: false,
        failed: false,
        ede,
        drop,
        route,
        security: SecurityTag::None,
        rpz_action: action.log_code(),
    }
}

fn recursion_ede(m: &RecursorMetrics, e: RecursionError) -> Ede {
    match e {
        RecursionError::NoReachableAuthority => Ede::new(22, "no reachable authority"),
        RecursionError::Deadline => Ede::new(22, "resolution deadline exceeded"),
        RecursionError::Limit(l) => {
            if l == Limit::UpstreamQueries {
                RecursorMetrics::inc(&m.limit_queries);
            }
            Ede::new(0, "work limit exceeded")
        }
        RecursionError::CnameLoop => Ede::new(0, "CNAME loop"),
    }
}

fn route_taken(route: &Route<'_>) -> RouteTaken {
    match route {
        Route::Forward => RouteTaken::Forward,
        Route::Recursive => RouteTaken::Recursive,
        Route::ForwardZone(_) => RouteTaken::ForwardZone,
    }
}

/// The uncompressed lowercase wire form of `name`.
fn name_wire(name: &Name) -> Vec<u8> {
    // a decoded or parsed name always encodes
    name.to_lowercase().to_bytes().unwrap_or_else(|_| vec![0])
}

/// `qname` followed by the CNAME targets of the chain in `answers` (RPZ QNAME triggers on
/// targets).
fn chain_names(qname: &Name, answers: &[Record]) -> Vec<Name> {
    let mut chain = vec![qname.to_lowercase()];
    for _ in 0..super::MAX_CNAME_DEPTH {
        let last = &chain[chain.len() - 1];
        let next = answers.iter().find_map(|r| match &r.data {
            RData::CNAME(c) if r.name == *last => Some(c.0.to_lowercase()),
            _ => None,
        });
        match next {
            Some(n) if !chain.contains(&n) => chain.push(n),
            _ => break,
        }
    }
    chain
}

/// A recursive query for a name other than the client's on the non-validating forward route.
fn plain_query(name: &Name, qtype: RecordType, dnssec_ok: bool) -> Vec<u8> {
    let mut msg = Message::new(
        rand::rng().next_u32() as u16,
        MessageType::Query,
        OpCode::Query,
    );
    msg.metadata.recursion_desired = true;
    msg.queries.push(Query::query(name.clone(), qtype));
    let mut edns = Edns::new();
    edns.set_max_payload(EDNS_BUFFER);
    edns.set_dnssec_ok(dnssec_ok);
    msg.edns = Some(edns);
    msg.to_vec().unwrap_or_default()
}

/// Asks the servers of forward zone `z` (RD=1, DO=1, CD=1, 0x20), choosing by infrastructure
/// cache RTO, until one answers NOERROR or NXDOMAIN.
async fn forward_zone_exchange(
    state: &RecursorState,
    z: &ForwardZone,
    name: &Name,
    qtype: RecordType,
) -> Result<Message, ()> {
    let r = &state.recursor;
    let mut tried: Vec<IpAddr> = Vec::new();
    for _ in 0..z.servers.len() {
        let now = clock::unix_now().max(0) as u64;
        let candidates: Vec<IpAddr> = z
            .servers
            .iter()
            .map(SocketAddr::ip)
            .filter(|ip| !tried.contains(ip))
            .collect();
        let Some(ip) = r
            .infra
            .select(&candidates, &z.domain, now, &mut rand::rng())
        else {
            break;
        };
        tried.push(ip);
        let Some(server) = z.servers.iter().find(|s| s.ip() == ip) else {
            break;
        };
        let oq = OutboundQuery {
            server: *server,
            qname: name,
            qtype,
            recursion_desired: true,
            edns: true,
            dnssec_ok: true,
            checking_disabled: true,
            use_0x20: true,
            timeout: FORWARD_ZONE_TIMEOUT,
        };
        match r.transport.exchange(&oq).await {
            Ok(ex) => {
                r.infra.record_rtt(ip, ex.rtt);
                if matches!(
                    ex.message.metadata.response_code,
                    ResponseCode::NoError | ResponseCode::NXDomain
                ) {
                    return Ok(ex.message);
                }
            }
            Err(_) => r.infra.record_timeout(ip, now),
        }
    }
    Err(())
}

/// A response built from decoded sections: ID 0, RA=1, RD=1, AA=0, CD copied, lowercase question
/// and no OPT. AD is set only for a secure answer to a client that set DO or AD; without DO,
/// DNSSEC records the client did not ask for are left out (RFC 4035 §3.2.1).
pub fn build_response(
    q: &MissQuery<'_>,
    rcode: ResponseCode,
    answers: &[Record],
    authorities: &[Record],
    secure: bool,
) -> Vec<u8> {
    let mut m = Message::response(0, OpCode::Query);
    m.metadata.recursion_available = true;
    m.metadata.recursion_desired = true;
    m.metadata.authoritative = false;
    m.metadata.authentic_data = secure && (q.dnssec_ok || q.authentic_data);
    m.metadata.checking_disabled = q.checking_disabled;
    m.metadata.response_code = rcode;
    m.queries
        .push(Query::query(q.qname.to_lowercase(), q.qtype));
    let keep = |r: &&Record| {
        let t = r.record_type();
        q.dnssec_ok
            || t == q.qtype
            || !matches!(t, RecordType::RRSIG | RecordType::NSEC | RecordType::NSEC3)
    };
    m.answers = answers.iter().filter(keep).cloned().collect();
    m.authorities = authorities.iter().filter(keep).cloned().collect();
    m.to_vec().unwrap_or_default()
}
