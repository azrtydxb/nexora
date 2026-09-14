//! Iterative resolution from the root hints: delegations with bailiwick-checked glue, glueless
//! nameserver lookups, CNAME/DNAME chains, relaxed QNAME minimisation (RFC 9156) and per-query
//! work limits. Every outgoing query goes through the spoofing-hardened `Transport`.

use super::budget::{Dependencies, Dependency, Limit, WorkBudget};
use super::infra::{InfraCache, Plan, RACE_STAGGER};
use super::memory;
use super::metrics::RecursorMetrics;
use super::roothints::RootHints;
use super::rrcache::{Credibility, DnssecStatus, RrCache};
use super::trace::{self, Trace, TraceEvent};
use super::transport::{Exchange, ExchangeError, OutboundQuery, Transport};
use super::{
    DEFAULT_MAX_DELEGATION_DEPTH, DEFAULT_MAX_UPSTREAM_QUERIES, LocalBoxFuture, MAX_CNAME_DEPTH,
    RESOLUTION_DEADLINE, join_all,
};
use crate::{clock, proto};
use hickory_proto::dnssec::rdata::DNSSECRData;
use hickory_proto::op::{Message, ResponseCode};
use hickory_proto::rr::rdata::NULL;
use hickory_proto::rr::{Name, RData, Record, RecordType};
use hickory_proto::serialize::binary::{BinDecodable, BinEncodable};
use std::net::{IpAddr, SocketAddr};
use std::sync::Arc;
use std::sync::atomic::{AtomicBool, Ordering};
use std::task::Poll;
use std::time::{Duration, Instant};

/// Minimised queries answered inside one zone (the name exists but is not delegated: an empty
/// non-terminal or a record of another type) before the full name is sent to that zone's
/// servers. RFC 9156 §2.3 leaves this cap to the resolver; one step still keeps every label
/// below the zone's first from its parent's servers, and the full name only reaches servers
/// authoritative for an ancestor of it.
const MINIMISE_STEPS_PER_ZONE: u32 = 1;
/// Nameserver names without addresses resolved for one zone cut.
const MAX_GLUELESS_PER_CUT: usize = 3;
/// Destination used only to test for a usable IPv6 route (no packet is sent).
const IPV6_PROBE: &str = "[2001:500:2::c]:53";

pub struct RecursionParams {
    pub root_hints: Arc<RootHints>,
    pub qname_minimisation: bool,
    pub max_upstream_queries: u32,
    pub max_delegation_depth: u32,
    pub authority_port: u16,
    /// `RecursionConfig.cache_max_bytes` (0: the default budget).
    pub cache_max_bytes: u64,
}

impl RecursionParams {
    /// Zero values take the defaults; without a `RecursionConfig` the IANA hints and QNAME
    /// minimisation are used.
    pub fn from_config(c: Option<&proto::RecursionConfig>) -> Self {
        let or = |v: u32, d: u32| if v == 0 { d } else { v };
        match c {
            None => Self {
                root_hints: Arc::new(RootHints::iana()),
                qname_minimisation: true,
                max_upstream_queries: DEFAULT_MAX_UPSTREAM_QUERIES,
                max_delegation_depth: DEFAULT_MAX_DELEGATION_DEPTH,
                authority_port: 53,
                cache_max_bytes: 0,
            },
            Some(c) => Self {
                root_hints: Arc::new(RootHints::from_config(&c.root_hints)),
                qname_minimisation: c.qname_minimisation,
                max_upstream_queries: or(c.max_upstream_queries, DEFAULT_MAX_UPSTREAM_QUERIES),
                max_delegation_depth: or(c.max_delegation_depth, DEFAULT_MAX_DELEGATION_DEPTH),
                authority_port: u16::try_from(or(c.authority_port, 53)).unwrap_or(53),
                cache_max_bytes: c.cache_max_bytes,
            },
        }
    }
}

#[derive(Debug, Clone)]
pub struct Resolution {
    pub rcode: ResponseCode,
    /// CNAME/DNAME chain + final RRset, RRSIGs included.
    pub answers: Vec<Record>,
    /// SOA + NSEC/NSEC3 (+RRSIGs) for negative answers.
    pub authorities: Vec<Record>,
    /// Zone cut that produced the final answer.
    pub zone: Name,
    /// NS set of that zone (RPZ NSDNAME).
    pub ns_names: Vec<Name>,
    /// Addresses of that NS set (RPZ NSIP).
    pub ns_addrs: Vec<IpAddr>,
}

#[derive(Debug, Clone, PartialEq)]
pub enum RecursionError {
    Limit(Limit),
    CnameLoop,
    NoReachableAuthority,
    Deadline,
}

impl From<Limit> for RecursionError {
    fn from(l: Limit) -> Self {
        RecursionError::Limit(l)
    }
}

/// `owner` is `zone` or below it (case-insensitive).
pub fn in_bailiwick(zone: &Name, owner: &Name) -> bool {
    zone.zone_of(owner)
}

/// The target of a DNAME record (hickory-proto decodes DNAME as unknown RDATA).
pub fn dname_target(r: &Record) -> Option<Name> {
    match &r.data {
        RData::Unknown { code, rdata } if *code == RecordType::DNAME => {
            Name::from_bytes(&rdata.anything).ok()
        }
        _ => None,
    }
}

/// A DNAME record in the form hickory-proto decodes from the wire.
pub fn dname_record(owner: Name, ttl: u32, target: &Name) -> Record {
    let wire = target.to_bytes().expect("a parsed name encodes");
    Record::from_rdata(
        owner,
        ttl,
        RData::Unknown {
            code: RecordType::DNAME,
            rdata: NULL::with(wire),
        },
    )
}

fn label_count(n: &Name) -> usize {
    n.iter().count()
}

fn covered_type(r: &Record) -> Option<RecordType> {
    match &r.data {
        RData::DNSSEC(DNSSECRData::RRSIG(sig)) => Some(sig.input().type_covered),
        _ => None,
    }
}

fn now() -> u64 {
    clock::unix_now().max(0) as u64
}

/// A zone and the servers to ask for it.
struct Cut {
    zone: Name,
    names: Vec<Name>,
    addrs: Vec<IpAddr>,
    /// NS names with no known address yet.
    unresolved: Vec<Name>,
    glueless_tried: usize,
}

/// The question of one resolution step.
struct Attempt<'a> {
    qname: &'a Name,
    qtype: RecordType,
    /// A QNAME-minimised step (trace only).
    minimised: bool,
    port: u16,
    deps: Dependencies<'a>,
}

/// One finished exchange.
struct Sent {
    ip: IpAddr,
    edns: bool,
    started: Instant,
    elapsed: Duration,
    result: Result<Exchange, ExchangeError>,
}

impl Sent {
    fn outcome(&self) -> trace::Outcome {
        match &self.result {
            Ok(ex) if ex.via_tcp => trace::Outcome::Tcp,
            Ok(_) => trace::Outcome::Udp,
            Err(ExchangeError::Timeout) => trace::Outcome::Timeout,
            Err(_) => trace::Outcome::NetworkError,
        }
    }
}

/// The result of `Recursor::race`.
struct Race {
    /// The first reply.
    won: Option<Sent>,
    /// Exchanges that timed out or failed.
    failed: Vec<Sent>,
    /// Servers still unanswered when the race was won (server, start).
    lost: Vec<(IpAddr, Instant)>,
}

enum Class {
    Answer,
    Referral(Name),
    NoData,
    NxDomain,
    Lame,
}

fn classify(m: &Message, zone: &Name, qn: &Name, qt: RecordType) -> Class {
    let rc = m.metadata.response_code;
    if rc == ResponseCode::NXDomain {
        return Class::NxDomain;
    }
    if rc != ResponseCode::NoError {
        return Class::Lame;
    }
    let has_answer = m
        .answers
        .iter()
        .any(|r| r.name == *qn && (r.record_type() == qt || r.record_type() == RecordType::CNAME))
        || m.answers
            .iter()
            .any(|r| r.record_type() == RecordType::DNAME && r.name.zone_of(qn));
    if has_answer {
        return Class::Answer;
    }
    let ns_owner = m
        .authorities
        .iter()
        .find(|r| r.record_type() == RecordType::NS)
        .map(|r| r.name.to_lowercase());
    if let Some(owner) = ns_owner {
        // a referral must go strictly down, stay inside the queried zone and lead towards qname
        if !m.metadata.authoritative
            && in_bailiwick(zone, &owner)
            && owner != *zone
            && owner.zone_of(qn)
        {
            return Class::Referral(owner);
        }
        if !m.metadata.authoritative {
            return Class::Lame; // upward or sideways referral
        }
    }
    if m.metadata.authoritative {
        Class::NoData
    } else {
        Class::Lame
    }
}

/// The label count of the next minimised query after `steps` answers inside the current zone.
fn next_labels(labels_now: usize, steps: u32, sname_labels: usize) -> usize {
    if steps >= MINIMISE_STEPS_PER_ZONE {
        sname_labels
    } else {
        labels_now + 1
    }
}

/// What a step's answer means for the CNAME/DNAME chain.
enum Step {
    Final,
    Redirect(Name),
    /// DNAME substitution overflowed the 255-octet limit (RFC 6672 §2.2: YXDOMAIN).
    TooLong,
}

fn next_step(answers: &[Record], sname: &Name, qtype: RecordType) -> Step {
    if answers
        .iter()
        .any(|r| r.name == *sname && r.record_type() == qtype)
    {
        return Step::Final;
    }
    if qtype != RecordType::DNAME {
        let dname = answers.iter().find_map(|r| {
            let t = dname_target(r)?;
            (r.name != *sname && r.name.zone_of(sname)).then(|| (r.name.clone(), t))
        });
        if let Some((owner, target)) = dname {
            let keep = label_count(sname) - label_count(&owner);
            return match Name::from_labels(sname.iter().take(keep))
                .and_then(|prefix| prefix.append_domain(&target))
            {
                Ok(n) => Step::Redirect(n.to_lowercase()),
                Err(_) => Step::TooLong,
            };
        }
    }
    if qtype != RecordType::CNAME {
        let cname = answers.iter().find_map(|r| match &r.data {
            RData::CNAME(c) if r.name == *sname => Some(c.0.to_lowercase()),
            _ => None,
        });
        if let Some(target) = cname {
            return Step::Redirect(target);
        }
    }
    Step::Final
}

/// Records of `answers` that belong to the step for `sname`: its own records and DNAMEs (with
/// their signatures) owned by an ancestor.
fn step_records<'a>(answers: &'a [Record], sname: &'a Name) -> impl Iterator<Item = &'a Record> {
    answers.iter().filter(move |r| {
        r.name == *sname
            || (r.name.zone_of(sname)
                && (r.record_type() == RecordType::DNAME
                    || covered_type(r) == Some(RecordType::DNAME)))
    })
}

/// The answer-section records that answer `qn`/`qt` inside `zone`: the CNAME/DNAME chain from
/// `qn` as far as this response carries it. Anything else a server stuffs into the answer section
/// is dropped before it can reach the cache.
fn relevant_answers(answers: &[Record], zone: &Name, qn: &Name, qt: RecordType) -> Vec<Record> {
    let mut out: Vec<Record> = Vec::new();
    let mut sname = qn.clone();
    for _ in 0..=MAX_CNAME_DEPTH {
        for r in step_records(answers, &sname) {
            if in_bailiwick(zone, &r.name) && !out.contains(r) {
                out.push(r.clone());
            }
        }
        match next_step(answers, &sname, qt) {
            Step::Redirect(t) if answers.iter().any(|r| r.name == t) => sname = t,
            _ => break,
        }
    }
    out
}

/// Caches every RRset of `records` (owner + type) with the RRSIGs covering it.
fn cache_rrsets(cache: &RrCache, records: &[Record], credibility: Credibility, now: u64) {
    let mut done: Vec<(Name, RecordType)> = Vec::new();
    for r in records {
        let t = r.record_type();
        if t == RecordType::RRSIG || done.contains(&(r.name.clone(), t)) {
            continue;
        }
        done.push((r.name.clone(), t));
        let set = records
            .iter()
            .filter(|x| x.name == r.name && x.record_type() == t)
            .cloned()
            .collect();
        let sigs = records
            .iter()
            .filter(|x| x.name == r.name && covered_type(x) == Some(t))
            .cloned()
            .collect();
        cache.insert(set, sigs, credibility, DnssecStatus::Unchecked, now);
    }
}

/// In-bailiwick SOA/NSEC/NSEC3 records and their signatures from the authority section.
fn negative_authorities(m: &Message, zone: &Name) -> Vec<Record> {
    let wanted =
        |t: RecordType| matches!(t, RecordType::SOA | RecordType::NSEC | RecordType::NSEC3);
    m.authorities
        .iter()
        .filter(|r| {
            in_bailiwick(zone, &r.name)
                && (wanted(r.record_type()) || covered_type(r).is_some_and(wanted))
        })
        .cloned()
        .collect()
}

pub struct Recursor {
    /// `recursor::transport::Transport`, not `edns::Transport`.
    pub transport: Transport,
    pub infra: InfraCache,
    pub rrcache: RrCache,
    pub metrics: Arc<RecursorMetrics>,
    pub ipv6: AtomicBool,
    pub trace: Trace,
}

impl Recursor {
    pub fn new(metrics: Arc<RecursorMetrics>) -> Self {
        Self {
            transport: Transport::new(metrics.clone()),
            infra: InfraCache::new(memory::shares(0).infra),
            rrcache: RrCache::new(memory::shares(0).rrset),
            metrics,
            ipv6: AtomicBool::new(false),
            trace: Trace::default(),
        }
    }

    /// Enables IPv6 authorities when the host has an IPv6 route (no packet is sent).
    pub fn detect_ipv6(&self) {
        let usable = std::net::UdpSocket::bind("[::]:0")
            .and_then(|s| s.connect(IPV6_PROBE))
            .is_ok();
        self.ipv6.store(usable, Ordering::Relaxed);
    }

    fn ipv6(&self) -> bool {
        self.ipv6.load(Ordering::Relaxed)
    }

    /// Resolves `qname`/`qtype`, following CNAME and DNAME chains, within `RESOLUTION_DEADLINE`.
    pub async fn resolve(
        &self,
        qname: &Name,
        qtype: RecordType,
        p: &RecursionParams,
        budget: &WorkBudget,
    ) -> Result<Resolution, RecursionError> {
        tokio::time::timeout(
            RESOLUTION_DEADLINE,
            self.resolve_chain(qname, qtype, p, budget, Dependencies::default()),
        )
        .await
        .unwrap_or(Err(RecursionError::Deadline))
    }

    /// Resolves one name without chasing CNAME/DNAME (the validator's DS/DNSKEY lookups).
    pub async fn fetch(
        &self,
        qname: &Name,
        qtype: RecordType,
        p: &RecursionParams,
        budget: &WorkBudget,
    ) -> Result<Resolution, RecursionError> {
        let sname = qname.to_lowercase();
        tokio::time::timeout(
            RESOLUTION_DEADLINE,
            self.resolve_one(&sname, qtype, p, budget, Dependencies::default()),
        )
        .await
        .unwrap_or(Err(RecursionError::Deadline))
    }

    fn resolve_chain<'a>(
        &'a self,
        qname: &'a Name,
        qtype: RecordType,
        p: &'a RecursionParams,
        budget: &'a WorkBudget,
        deps: Dependencies<'a>,
    ) -> LocalBoxFuture<'a, Result<Resolution, RecursionError>> {
        Box::pin(async move {
            let mut sname = qname.to_lowercase();
            let mut seen = vec![sname.clone()];
            let mut answers: Vec<Record> = Vec::new();
            let mut hops: u8 = 0;
            let mut pending: Option<Resolution> = None;
            loop {
                let mut res = match pending.take() {
                    Some(r) => r,
                    None => self.resolve_one(&sname, qtype, p, budget, deps).await?,
                };
                for r in step_records(&res.answers, &sname) {
                    if !answers.contains(r) {
                        answers.push(r.clone());
                    }
                }
                let target = match next_step(&res.answers, &sname, qtype) {
                    Step::Final => {
                        res.answers = answers;
                        return Ok(res);
                    }
                    Step::TooLong => {
                        res.rcode = ResponseCode::YXDomain;
                        res.answers = answers;
                        return Ok(res);
                    }
                    Step::Redirect(t) => t,
                };
                hops += 1;
                if hops > MAX_CNAME_DEPTH {
                    RecursorMetrics::inc(&self.metrics.limit_cname_depth);
                    return Err(Limit::CnameDepth.into());
                }
                if seen.contains(&target) {
                    RecursorMetrics::inc(&self.metrics.cname_loops);
                    return Err(RecursionError::CnameLoop);
                }
                seen.push(target.clone());
                // the same response already carries the target's records from the same zone
                if in_bailiwick(&res.zone, &target) && res.answers.iter().any(|r| r.name == target)
                {
                    pending = Some(res);
                }
                sname = target;
            }
        })
    }

    async fn resolve_one(
        &self,
        sname: &Name,
        stype: RecordType,
        p: &RecursionParams,
        budget: &WorkBudget,
        deps: Dependencies<'_>,
    ) -> Result<Resolution, RecursionError> {
        let now = now();
        let mut types = vec![stype];
        if stype != RecordType::CNAME {
            types.push(RecordType::CNAME);
        }
        for t in types {
            // A DS RRset from a referral comes from the parent, which is authoritative for it
            if let Some(hit) = self.rrcache.get(sname, t, now)
                && (hit.credibility >= Credibility::AnswerNonAa
                    || (t == RecordType::DS && hit.credibility >= Credibility::AuthorityNonAa))
            {
                let cut = self.closest_cut(sname, stype, p, now);
                let mut answers = hit.records.clone();
                answers.extend(hit.rrsigs.iter().cloned());
                return Ok(Resolution {
                    rcode: ResponseCode::NoError,
                    answers,
                    authorities: Vec::new(),
                    zone: cut.zone,
                    ns_names: cut.names,
                    ns_addrs: cut.addrs,
                });
            }
        }
        let mut cut = self.closest_cut(sname, stype, p, now);
        if !cut.zone.is_root() && deps.is_empty() {
            budget.note_cut(&cut.zone);
        }
        let sname_labels = label_count(sname);
        let mut minimise = p.qname_minimisation;
        let mut labels = if minimise {
            label_count(&cut.zone) + 1
        } else {
            sname_labels
        };
        let mut delegations = 0u32;
        let mut steps_in_zone = 0u32;
        // servers that failed the current step (timeout, lame, SERVFAIL)
        let mut failed: Vec<IpAddr> = Vec::new();
        loop {
            let labels_now = labels.min(sname_labels);
            let query_name = sname.trim_to(labels_now);
            let full = labels_now == sname_labels;
            // RFC 9156 §2.3: hidden labels are asked with QTYPE A
            let query_type = if full { stype } else { RecordType::A };
            let attempt = Attempt {
                qname: &query_name,
                qtype: query_type,
                minimised: !full,
                port: p.authority_port,
                deps,
            };
            let (msg, ip) = self.ask(&mut cut, &mut failed, &attempt, p, budget).await?;
            let now = self::now();
            match classify(&msg, &cut.zone, &query_name, query_type) {
                Class::Answer => {
                    let kept = relevant_answers(&msg.answers, &cut.zone, &query_name, query_type);
                    let credibility = if msg.metadata.authoritative {
                        Credibility::AnswerAa
                    } else {
                        Credibility::AnswerNonAa
                    };
                    cache_rrsets(&self.rrcache, &kept, credibility, now);
                    if !full {
                        steps_in_zone += 1;
                        labels = next_labels(labels_now, steps_in_zone, sname_labels);
                        failed.clear();
                        continue;
                    }
                    return Ok(Resolution {
                        rcode: ResponseCode::NoError,
                        answers: kept,
                        authorities: negative_authorities(&msg, &cut.zone),
                        zone: cut.zone,
                        ns_names: cut.names,
                        ns_addrs: cut.addrs,
                    });
                }
                Class::Referral(owner) => {
                    delegations += 1;
                    if let Err(l) = budget.check_depth(delegations) {
                        RecursorMetrics::inc(&self.metrics.limit_delegation_depth);
                        return Err(l.into());
                    }
                    cut = self.follow_referral(&msg, &cut.zone, owner, now);
                    if deps.is_empty() {
                        budget.note_cut(&cut.zone);
                    }
                    labels = if minimise {
                        label_count(&cut.zone) + 1
                    } else {
                        sname_labels
                    };
                    steps_in_zone = 0;
                    failed.clear();
                }
                Class::NoData if !full => {
                    // empty non-terminal
                    steps_in_zone += 1;
                    labels = next_labels(labels_now, steps_in_zone, sname_labels);
                    failed.clear();
                }
                Class::NxDomain if !full => {
                    // relaxed RFC 9156 §2.3: some servers answer NXDOMAIN for empty non-terminals
                    minimise = false;
                    labels = sname_labels;
                    failed.clear();
                }
                class @ (Class::NoData | Class::NxDomain) => {
                    let rcode = match class {
                        Class::NxDomain => ResponseCode::NXDomain,
                        _ => ResponseCode::NoError,
                    };
                    return Ok(Resolution {
                        rcode,
                        answers: Vec::new(),
                        authorities: negative_authorities(&msg, &cut.zone),
                        zone: cut.zone,
                        ns_names: cut.names,
                        ns_addrs: cut.addrs,
                    });
                }
                Class::Lame => {
                    // SERVFAIL is often transient: skip the server for this step without a lame mark
                    if msg.metadata.response_code != ResponseCode::ServFail {
                        self.infra.mark_lame(ip, &cut.zone, now);
                        RecursorMetrics::inc(&self.metrics.lame_marked);
                    }
                    failed.push(ip);
                }
            }
        }
    }

    /// Sends one query to the servers of `cut` that have not failed this step (see
    /// `InfraCache::plan`), resolving glueless nameserver addresses when none is left. Returns a
    /// NOERROR/NXDOMAIN/other reply to classify.
    async fn ask(
        &self,
        cut: &mut Cut,
        failed: &mut Vec<IpAddr>,
        a: &Attempt<'_>,
        p: &RecursionParams,
        budget: &WorkBudget,
    ) -> Result<(Message, IpAddr), RecursionError> {
        let mut edns_retried = false;
        loop {
            let now = now();
            let candidates: Vec<IpAddr> = cut
                .addrs
                .iter()
                .copied()
                .filter(|a| !failed.contains(a))
                .collect();
            let Some(plan) = self
                .infra
                .plan(&candidates, &cut.zone, now, &mut rand::rng())
            else {
                if self.resolve_ns_addresses(cut, p, budget, a.deps).await? {
                    continue;
                }
                return Err(RecursionError::NoReachableAuthority);
            };
            budget.spend_query()?;
            let race = self.race(plan, a, budget).await;
            for s in race.failed {
                self.note(a, budget, s.ip, s.started, s.elapsed, s.outcome());
                if let Err(ExchangeError::Network(std::io::ErrorKind::NetworkUnreachable)) =
                    s.result
                    && s.ip.is_ipv6()
                {
                    // debt: stays off until the next start (`detect_ipv6`); revisit if hosts
                    // whose IPv6 route comes up after start need IPv6-only authorities.
                    self.ipv6.store(false, Ordering::Relaxed);
                }
                self.infra.record_timeout(s.ip, now);
                failed.push(s.ip);
            }
            for (ip, started) in race.lost {
                let elapsed = started.elapsed();
                self.note(a, budget, ip, started, elapsed, trace::Outcome::Lost);
                self.infra.record_lost(ip, elapsed);
            }
            let Some(won) = race.won else {
                continue;
            };
            self.note(a, budget, won.ip, won.started, won.elapsed, won.outcome());
            let Ok(ex) = won.result else {
                continue;
            };
            self.infra.record_rtt(won.ip, ex.rtt);
            let rcode = ex.message.metadata.response_code;
            if won.edns
                && !edns_retried
                && matches!(rcode, ResponseCode::FormErr | ResponseCode::NotImp)
            {
                self.infra.set_no_edns(won.ip);
                RecursorMetrics::inc(&self.metrics.edns_fallbacks);
                edns_retried = true;
                continue;
            }
            return Ok((ex.message, won.ip));
        }
    }

    /// One exchange with `ip` (EDNS unless the server is known not to support it; the timeout
    /// is the server's RTO).
    async fn send(&self, a: &Attempt<'_>, ip: IpAddr) -> Sent {
        let edns = !self.infra.no_edns(ip);
        let q = OutboundQuery {
            server: SocketAddr::new(ip, a.port),
            qname: a.qname,
            qtype: a.qtype,
            recursion_desired: false,
            edns,
            dnssec_ok: true,
            checking_disabled: false,
            use_0x20: true,
            timeout: self.infra.rto(ip),
        };
        let started = Instant::now();
        let result = self.transport.exchange(&q).await;
        Sent {
            ip,
            edns,
            started,
            elapsed: started.elapsed(),
            result,
        }
    }

    /// Sends to `plan.primary`, then to each of `plan.racers` `RACE_STAGGER` after the previous
    /// server (at once when every exchange started so far has failed) until a reply arrives.
    /// The first reply wins and the other exchanges are dropped. Each exchange keeps its own
    /// socket, ID and 0x20 checks, so a spoofed reply to one never completes the race. The
    /// primary's query is already counted; a racer is sent only while the budget allows.
    async fn race(&self, plan: Plan, a: &Attempt<'_>, budget: &WorkBudget) -> Race {
        let mut running: Vec<(IpAddr, Instant, LocalBoxFuture<'_, Sent>)> = vec![(
            plan.primary,
            Instant::now(),
            Box::pin(self.send(a, plan.primary)),
        )];
        let mut waiting = plan.racers.into_iter();
        let mut next = waiting.next();
        let mut stagger = Box::pin(tokio::time::sleep(RACE_STAGGER));
        let mut failed = Vec::new();
        let won = std::future::poll_fn(|cx| {
            loop {
                let mut i = 0;
                while i < running.len() {
                    match running[i].2.as_mut().poll(cx) {
                        Poll::Ready(s) => {
                            drop(running.swap_remove(i));
                            if s.result.is_ok() {
                                return Poll::Ready(Some(s));
                            }
                            failed.push(s);
                        }
                        Poll::Pending => i += 1,
                    }
                }
                let Some(ip) = next else {
                    return if running.is_empty() {
                        Poll::Ready(None)
                    } else {
                        Poll::Pending
                    };
                };
                if !running.is_empty() && stagger.as_mut().poll(cx).is_pending() {
                    return Poll::Pending;
                }
                next = waiting.next();
                if budget.try_spend_query() {
                    running.push((ip, Instant::now(), Box::pin(self.send(a, ip))));
                    stagger
                        .as_mut()
                        .reset(tokio::time::Instant::now() + RACE_STAGGER);
                }
                // poll the new exchange and the reset timer before waiting
            }
        })
        .await;
        let lost = running
            .into_iter()
            .map(|(ip, started, _)| (ip, started))
            .collect();
        Race { won, failed, lost }
    }

    fn note(
        &self,
        a: &Attempt<'_>,
        budget: &WorkBudget,
        server: IpAddr,
        start: Instant,
        elapsed: Duration,
        outcome: trace::Outcome,
    ) {
        if self.trace.enabled() {
            self.trace.record(TraceEvent {
                start,
                elapsed,
                server,
                qname: a.qname.clone(),
                qtype: a.qtype,
                phase: budget.phase(),
                glueless: !a.deps.is_empty(),
                minimised: a.minimised,
                outcome,
            });
        }
    }

    /// The cut named by a referral from `parent`: NS names from the authority section and only
    /// glue inside `parent`'s bailiwick (anything else is discarded, never cached).
    fn follow_referral(&self, msg: &Message, parent: &Name, owner: Name, now: u64) -> Cut {
        let ns: Vec<Record> = msg
            .authorities
            .iter()
            .filter(|r| r.record_type() == RecordType::NS && r.name == owner)
            .cloned()
            .collect();
        let names: Vec<Name> = ns
            .iter()
            .filter_map(|r| match &r.data {
                RData::NS(n) => Some(n.0.to_lowercase()),
                _ => None,
            })
            .collect();
        let ipv6 = self.ipv6();
        let glue: Vec<Record> = msg
            .additionals
            .iter()
            .filter(|r| {
                (r.record_type() == RecordType::A || r.record_type() == RecordType::AAAA)
                    && names.contains(&r.name)
                    && in_bailiwick(parent, &r.name)
            })
            .cloned()
            .collect();
        self.rrcache.insert(
            ns,
            Vec::new(),
            Credibility::AuthorityNonAa,
            DnssecStatus::Unchecked,
            now,
        );
        cache_rrsets(&self.rrcache, &glue, Credibility::Glue, now);
        let delegation: Vec<Record> = msg
            .authorities
            .iter()
            .filter(|r| {
                // DS only for the delegated name; NSEC/NSEC3 (insecure-delegation proofs) anywhere
                // inside the parent. All of it stays Unchecked until the validator has seen it.
                let wanted = |t| match t {
                    RecordType::DS => r.name == owner,
                    RecordType::NSEC | RecordType::NSEC3 => true,
                    _ => false,
                };
                in_bailiwick(parent, &r.name)
                    && (wanted(r.record_type()) || covered_type(r).is_some_and(wanted))
            })
            .cloned()
            .collect();
        cache_rrsets(&self.rrcache, &delegation, Credibility::AuthorityNonAa, now);
        let (mut addrs, unresolved) = self.cached_addresses(&names, now);
        for r in &glue {
            let ip = match &r.data {
                RData::A(a) => IpAddr::V4(a.0),
                RData::AAAA(a) if ipv6 => IpAddr::V6(a.0),
                _ => continue,
            };
            if !addrs.contains(&ip) {
                addrs.push(ip);
            }
        }
        let unresolved = unresolved
            .into_iter()
            .filter(|n| !glue.iter().any(|g| g.name == *n))
            .collect();
        Cut {
            zone: owner,
            names,
            addrs,
            unresolved,
            glueless_tried: 0,
        }
    }

    /// Cached addresses of `names`, and the names that have none.
    fn cached_addresses(&self, names: &[Name], now: u64) -> (Vec<IpAddr>, Vec<Name>) {
        let mut addrs = Vec::new();
        let mut unresolved = Vec::new();
        let ipv6 = self.ipv6();
        for name in names {
            let before = addrs.len();
            if let Some(set) = self.rrcache.get_glue(name, RecordType::A, now) {
                addrs.extend(set.records.iter().filter_map(|r| match &r.data {
                    RData::A(a) => Some(IpAddr::V4(a.0)),
                    _ => None,
                }));
            }
            if ipv6 && let Some(set) = self.rrcache.get_glue(name, RecordType::AAAA, now) {
                addrs.extend(set.records.iter().filter_map(|r| match &r.data {
                    RData::AAAA(a) => Some(IpAddr::V6(a.0)),
                    _ => None,
                }));
            }
            if addrs.len() == before {
                unresolved.push(name.clone());
            }
        }
        addrs.dedup();
        (addrs, unresolved)
    }

    /// The deepest cached zone cut above `sname` with at least one known server address, else
    /// the root from the hints. DS lives in the parent, so its search starts one label up.
    fn closest_cut(&self, sname: &Name, stype: RecordType, p: &RecursionParams, now: u64) -> Cut {
        let mut n = if stype == RecordType::DS {
            sname.base_name()
        } else {
            sname.clone()
        };
        while !n.is_root() {
            if let Some(ns) = self.rrcache.get(&n, RecordType::NS, now) {
                let names: Vec<Name> = ns
                    .records
                    .iter()
                    .filter_map(|r| match &r.data {
                        RData::NS(t) => Some(t.0.to_lowercase()),
                        _ => None,
                    })
                    .collect();
                let (addrs, unresolved) = self.cached_addresses(&names, now);
                if !addrs.is_empty() {
                    return Cut {
                        zone: n.to_lowercase(),
                        names,
                        addrs,
                        unresolved,
                        glueless_tried: 0,
                    };
                }
            }
            n = n.base_name();
        }
        Cut {
            zone: Name::root(),
            names: p
                .root_hints
                .servers
                .iter()
                .map(|(n, _)| n.clone())
                .collect(),
            addrs: p.root_hints.addresses(self.ipv6()),
            unresolved: Vec::new(),
            glueless_tried: 0,
        }
    }

    /// The label count of the deepest cached delegation (NS RRset) at or above `name`.
    fn known_cut_labels(&self, name: &Name, now: u64) -> usize {
        let mut n = name.clone();
        while !n.is_root() {
            if self.rrcache.get(&n, RecordType::NS, now).is_some() {
                return label_count(&n);
            }
            n = n.base_name();
        }
        0
    }

    /// Resolves addresses for up to `MAX_GLUELESS_PER_CUT` nameserver names of `cut`, stopping at
    /// the first name that yields one; names under the deepest known delegation go first (then
    /// the fewest labels below it), and A and AAAA are resolved concurrently when IPv6 is usable. A lookup the current
    /// one depends on (a dependency cycle) is skipped; work limits propagate, other failures only
    /// skip the name.
    async fn resolve_ns_addresses(
        &self,
        cut: &mut Cut,
        p: &RecursionParams,
        budget: &WorkBudget,
        deps: Dependencies<'_>,
    ) -> Result<bool, RecursionError> {
        let mut types = vec![RecordType::A];
        if self.ipv6() {
            types.push(RecordType::AAAA);
        }
        if cut.glueless_tried == 0 {
            let now = now();
            cut.unresolved.sort_by_cached_key(|n| {
                let known = self.known_cut_labels(n, now);
                (std::cmp::Reverse(known), label_count(n) - known)
            });
        }
        while cut.glueless_tried < MAX_GLUELESS_PER_CUT && !cut.unresolved.is_empty() {
            let name = cut.unresolved.remove(0);
            cut.glueless_tried += 1;
            let lookups: Vec<Dependency<'_>> =
                types.iter().filter_map(|&t| deps.enter(&name, t)).collect();
            let results = join_all(
                lookups
                    .iter()
                    .map(|d| self.resolve_chain(&name, d.rtype(), p, budget, d.chain()))
                    .collect(),
            )
            .await;
            let mut found = false;
            for result in results {
                match result {
                    Ok(res) => {
                        for r in &res.answers {
                            let ip = match &r.data {
                                RData::A(a) => IpAddr::V4(a.0),
                                RData::AAAA(a) => IpAddr::V6(a.0),
                                _ => continue,
                            };
                            if !cut.addrs.contains(&ip) {
                                cut.addrs.push(ip);
                                found = true;
                            }
                        }
                    }
                    Err(RecursionError::Limit(l)) => return Err(l.into()),
                    Err(_) => {}
                }
            }
            if found {
                return Ok(true);
            }
        }
        Ok(false)
    }
}
