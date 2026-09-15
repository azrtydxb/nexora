//! Chain-of-trust validation of responses (RFC 4035 §5), with RFC 8914 EDE codes.

use super::Ede;
use super::denial::{
    Denial, NSEC3_INSECURE_ITERATIONS, nsec_proves_no_closer_match, nsec_proves_nodata,
    nsec_proves_nxdomain, nsec3_match, nsec3_proves_no_closer_match, nsec3_proves_nodata,
    nsec3_proves_nxdomain,
};
use super::nsec_cache::AggressiveNsecCache;
use super::verify::{
    DsMatch, VerifyError, algorithm_supported, capped_ttl, digest_supported, match_ds, rrsig,
    verify_rrset,
};
use crate::proto;
use crate::recursor::metrics::RecursorMetrics;
use crate::recursor::{LocalBoxFuture, join_all, poll_with};
use crate::snapshot_m3::parse_ds;
use hickory_proto::dnssec::rdata::{DNSKEY, DNSSECRData, DS, NSEC, NSEC3};
use hickory_proto::dnssec::{Algorithm, DigestType};
use hickory_proto::op::ResponseCode;
use hickory_proto::rr::{Name, RData, Record, RecordType};
use std::cell::RefCell;
use std::rc::Rc;
use std::sync::Arc;

/// Zone states cached between validations.
const ZONE_CACHE_CAPACITY: usize = 4096;
const MAX_ZONE_CACHE_SECS: u64 = 3600;
const BOGUS_CACHE_SECS: u64 = 60;
/// EDE 23 (Network Error) marks a chain walk that failed to fetch; it is reported as
/// indeterminate and never cached.
const EDE_NETWORK_ERROR: u16 = 23;

/// One lookup result used by the validator.
#[derive(Clone)]
pub struct FetchedSet {
    pub rcode: ResponseCode,
    pub answers: Vec<Record>,
    pub authorities: Vec<Record>,
}

#[derive(Debug, Clone)]
pub enum FetchError {
    Unreachable,
    Limit,
}

/// Fetches DS and DNSKEY RRsets (with RRSIGs and denial records) for the chain of trust.
pub trait Fetcher {
    fn fetch<'a>(
        &'a self,
        name: &'a Name,
        rtype: RecordType,
    ) -> LocalBoxFuture<'a, Result<FetchedSet, FetchError>>;

    /// Whether `name` is known to be a zone cut (its DNSKEY RRset is then worth fetching before
    /// the walk reaches it). Only a hint: a wrong answer costs one query, never correctness.
    fn known_cut(&self, _name: &Name) -> bool {
        false
    }
}

type FetchCell = tokio::sync::OnceCell<Result<FetchedSet, FetchError>>;
type FetchKey = (Name, RecordType);

/// A `Fetcher` that fetches each (name, type) at most once and shares a fetch in progress with
/// every concurrent caller. One per validation (or per client query): results are not kept
/// beyond it.
pub struct MemoFetcher<'f> {
    inner: &'f dyn Fetcher,
    cells: RefCell<Vec<(FetchKey, Rc<FetchCell>)>>,
}

impl<'f> MemoFetcher<'f> {
    pub fn new(inner: &'f dyn Fetcher) -> Self {
        MemoFetcher {
            inner,
            cells: RefCell::new(Vec::new()),
        }
    }

    fn cell(&self, name: &Name, rtype: RecordType) -> Rc<FetchCell> {
        let key = (name.to_lowercase(), rtype);
        let mut cells = self.cells.borrow_mut();
        if let Some((_, c)) = cells.iter().find(|(k, _)| *k == key) {
            return c.clone();
        }
        let c = Rc::new(FetchCell::new());
        cells.push((key, c.clone()));
        c
    }
}

impl Fetcher for MemoFetcher<'_> {
    fn fetch<'a>(
        &'a self,
        name: &'a Name,
        rtype: RecordType,
    ) -> LocalBoxFuture<'a, Result<FetchedSet, FetchError>> {
        Box::pin(async move {
            let cell = self.cell(name, rtype);
            cell.get_or_init(|| self.inner.fetch(name, rtype))
                .await
                .clone()
        })
    }

    fn known_cut(&self, name: &Name) -> bool {
        self.inner.known_cut(name)
    }
}

/// What is trusted at one zone: configured DS records and keys accepted by RFC 5011.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct TrustPoint {
    pub ds: Vec<DS>,
    pub keys: Vec<DNSKEY>,
}

impl TrustPoint {
    pub fn key_count(&self) -> usize {
        self.ds.len() + self.keys.len()
    }
}

#[derive(Debug, Clone, Default, PartialEq)]
pub struct TrustPoints {
    pub zones: Vec<(Name, TrustPoint)>,
}

impl TrustPoints {
    /// DS trust anchors from a validated snapshot (malformed entries are skipped).
    pub fn from_config(anchors: &[proto::TrustAnchor]) -> Self {
        let mut points = TrustPoints::default();
        for a in anchors {
            let (Ok(zone), Ok(ds)) = (Name::from_ascii(&a.zone), ds_from_text(&a.ds)) else {
                continue;
            };
            points.add_ds(&zone.to_lowercase(), ds);
        }
        points
    }

    pub fn add_ds(&mut self, zone: &Name, ds: DS) {
        self.point_mut(zone).ds.push(ds);
    }

    pub fn add_key(&mut self, zone: &Name, key: DNSKEY) {
        self.point_mut(zone).keys.push(key);
    }

    fn point_mut(&mut self, zone: &Name) -> &mut TrustPoint {
        let i = match self.zones.iter().position(|(z, _)| z == zone) {
            Some(i) => i,
            None => {
                self.zones.push((zone.clone(), TrustPoint::default()));
                self.zones.len() - 1
            }
        };
        &mut self.zones[i].1
    }

    /// The deepest trust point at or above `name`.
    pub fn closest(&self, name: &Name) -> Option<&(Name, TrustPoint)> {
        self.zones
            .iter()
            .filter(|(z, _)| z.zone_of(name))
            .max_by_key(|(z, _)| z.num_labels())
    }
}

/// Parses `"<key tag> <algorithm> <digest type> <hex digest>"`.
pub fn ds_from_text(text: &str) -> Result<DS, String> {
    let (tag, alg, dtype, digest) = parse_ds(text)?;
    Ok(DS::new(
        tag,
        Algorithm::from_u8(alg),
        DigestType::from(dtype),
        digest,
    ))
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Security {
    Secure,
    Insecure(Option<Ede>),
    Bogus(Ede),
    Indeterminate(Ede),
}

pub struct ValidationInput<'a> {
    pub qname: &'a Name,
    pub qtype: RecordType,
    pub rcode: ResponseCode,
    pub answers: &'a [Record],
    pub authorities: &'a [Record],
}

pub struct ValidationResult {
    pub security: Security,
    /// Upper bound for the TTLs of the validated records (RFC 4035 §5.3.3).
    pub ttl_cap: Option<u32>,
}

#[derive(Debug, Clone)]
pub enum ZoneState {
    Secure(Vec<DNSKEY>),
    Insecure(Option<Ede>),
    Bogus(Ede),
}

/// A cached chain-walk result for one name: its state, and the zone whose keys `Secure` holds.
struct CachedZone {
    state: ZoneState,
    key_zone: Name,
    expires: u64,
}

pub struct Validator {
    metrics: Arc<RecursorMetrics>,
    zones: quick_cache::sync::Cache<Name, Arc<CachedZone>>,
    pub nsec: AggressiveNsecCache,
}

/// An RRset of one owner and type with the RRSIGs covering it.
struct RrSet {
    name: Name,
    rtype: RecordType,
    records: Vec<Record>,
    sigs: Vec<Record>,
}

impl RrSet {
    fn ttl(&self) -> u32 {
        self.records.iter().map(|r| r.ttl).min().unwrap_or(0)
    }

    fn signer(&self) -> Option<Name> {
        self.sigs
            .iter()
            .filter_map(rrsig)
            .map(|s| s.input().signer_name.to_lowercase())
            .find(|s| s.zone_of(&self.name))
    }
}

fn group(records: &[Record]) -> Vec<RrSet> {
    let mut sets: Vec<RrSet> = Vec::new();
    for r in records
        .iter()
        .filter(|r| r.record_type() != RecordType::RRSIG)
    {
        match sets
            .iter_mut()
            .find(|s| s.name == r.name && s.rtype == r.record_type())
        {
            Some(s) => s.records.push(r.clone()),
            None => sets.push(RrSet {
                name: r.name.clone(),
                rtype: r.record_type(),
                records: vec![r.clone()],
                sigs: Vec::new(),
            }),
        }
    }
    for r in records {
        if let Some(sig) = rrsig(r)
            && let Some(s) = sets
                .iter_mut()
                .find(|s| s.name == r.name && s.rtype == sig.input().type_covered)
        {
            s.sigs.push(r.clone());
        }
    }
    sets
}

fn rrset_of(records: &[Record], name: &Name, rtype: RecordType) -> (Vec<Record>, Vec<Record>) {
    group(records)
        .into_iter()
        .find(|s| s.name == *name && s.rtype == rtype)
        .map(|s| (s.records, s.sigs))
        .unwrap_or_default()
}

fn dnskeys(records: &[Record]) -> Vec<DNSKEY> {
    records
        .iter()
        .filter_map(|r| match &r.data {
            RData::DNSSEC(DNSSECRData::DNSKEY(k)) => Some(k.clone()),
            _ => None,
        })
        .collect()
}

fn ds_records(records: &[Record]) -> Vec<DS> {
    records
        .iter()
        .filter_map(|r| match &r.data {
            RData::DNSSEC(DNSSECRData::DS(d)) => Some(d.clone()),
            _ => None,
        })
        .collect()
}

fn verify_ede(e: VerifyError, name: &Name, rtype: RecordType) -> Ede {
    match e {
        VerifyError::BadSignature => Ede::new(6, format!("bad signature for {name} {rtype}")),
        VerifyError::Expired => Ede::new(7, format!("signature expired for {name} {rtype}")),
        VerifyError::NotYetValid => {
            Ede::new(8, format!("signature not yet valid for {name} {rtype}"))
        }
        VerifyError::NoMatchingKey => Ede::new(9, format!("DNSKEY missing for {name} {rtype}")),
        VerifyError::NoRrsig => Ede::new(10, format!("RRSIGs missing for {name} {rtype}")),
        VerifyError::UnsupportedAlgorithm
        | VerifyError::SignerMismatch
        | VerifyError::LabelCount => Ede::new(6, format!("no usable signature for {name} {rtype}")),
    }
}

/// The state of a zone whose DS records are all unusable (RFC 4035 §5.2: insecure).
fn unsupported_ds(zone: &Name, ds: &[DS]) -> ZoneState {
    if ds.iter().all(|d| !digest_supported(d.digest_type())) {
        ZoneState::Insecure(Some(Ede::new(
            2,
            format!("unsupported DS digest type for {zone}"),
        )))
    } else {
        ZoneState::Insecure(Some(Ede::new(
            1,
            format!("unsupported DNSKEY algorithm for {zone}"),
        )))
    }
}

fn supported_ds(ds: &[DS]) -> bool {
    ds.iter()
        .any(|d| algorithm_supported(d.algorithm()) && digest_supported(d.digest_type()))
}

fn network_error(what: String) -> ZoneState {
    ZoneState::Bogus(Ede::new(EDE_NETWORK_ERROR, what))
}

/// Result of checking one part of a response.
enum Outcome {
    Secure(Option<u32>),
    Insecure(Option<Ede>),
    Bogus(Ede),
    Indeterminate(Ede),
}

impl From<ZoneState> for Outcome {
    fn from(s: ZoneState) -> Self {
        match s {
            ZoneState::Secure(_) => Outcome::Secure(None),
            ZoneState::Insecure(e) => Outcome::Insecure(e),
            ZoneState::Bogus(e) if e.code == EDE_NETWORK_ERROR => Outcome::Indeterminate(e),
            ZoneState::Bogus(e) => Outcome::Bogus(e),
        }
    }
}

fn denial_outcome(d: Denial) -> Outcome {
    match d {
        Denial::Proven => Outcome::Secure(None),
        Denial::ProvenOptOut => Outcome::Insecure(None),
        Denial::InsecureIterations(n) => Outcome::Insecure(Some(Ede::new(
            27,
            format!("NSEC3 iterations {n} above {NSEC3_INSECURE_ITERATIONS}"),
        ))),
        Denial::BogusIterations(n) => {
            Outcome::Bogus(Ede::new(27, format!("NSEC3 iterations {n} rejected")))
        }
        Denial::NotProven(r) => Outcome::Bogus(Ede::new(12, format!("NSEC missing: {r}"))),
    }
}

/// Folds outcomes: any bogus wins, then indeterminate, then insecure, else secure.
#[derive(Default)]
struct Combined {
    bogus: Option<Ede>,
    indeterminate: Option<Ede>,
    insecure: Option<Option<Ede>>,
    ttl_cap: Option<u32>,
}

impl Combined {
    fn add(&mut self, o: Outcome) {
        match o {
            Outcome::Secure(ttl) => {
                if let Some(t) = ttl {
                    self.ttl_cap = Some(self.ttl_cap.map_or(t, |c| c.min(t)));
                }
            }
            Outcome::Insecure(e) => match &mut self.insecure {
                None => self.insecure = Some(e),
                Some(None) => self.insecure = Some(e),
                Some(Some(_)) => {}
            },
            Outcome::Bogus(e) => {
                self.bogus.get_or_insert(e);
            }
            Outcome::Indeterminate(e) => {
                self.indeterminate.get_or_insert(e);
            }
        }
    }

    fn security(self) -> Security {
        match (self.bogus, self.indeterminate, self.insecure) {
            (Some(e), _, _) => Security::Bogus(e),
            (None, Some(e), _) => Security::Indeterminate(e),
            (None, None, Some(e)) => Security::Insecure(e),
            (None, None, None) => Security::Secure,
        }
    }
}

/// Denial records of a response that verified with the zone's keys.
#[derive(Default)]
struct VerifiedDenial {
    nsec: Vec<(Name, NSEC)>,
    nsec3: Vec<(Name, NSEC3)>,
    records: Vec<Record>,
    ttl_cap: Option<u32>,
}

/// Is `name` at or below a negative trust anchor that has not expired?
pub fn under_nta(name: &Name, ntas: &[(Name, i64)], now_unix: u64) -> bool {
    ntas.iter()
        .any(|(d, expires)| d.zone_of(name) && *expires > now_unix as i64)
}

enum Descend {
    /// `child` is a signed zone cut with these keys.
    Cut(Vec<DNSKEY>, u32),
    /// `child` is inside the current zone (no DS, no delegation).
    Inside,
    /// `child` does not exist, so nothing below it does.
    Nonexistent,
    Stop(ZoneState),
}

impl Validator {
    pub fn new(metrics: Arc<RecursorMetrics>) -> Self {
        Validator {
            nsec: AggressiveNsecCache::new(
                crate::recursor::memory::shares(0).nsec,
                metrics.clone(),
            ),
            metrics,
            zones: quick_cache::sync::Cache::new(ZONE_CACHE_CAPACITY),
        }
    }

    /// Forgets cached zone states and denials (trust anchors changed).
    pub fn clear(&self) {
        self.zones.clear();
        self.nsec.clear();
    }

    pub async fn zone_state(
        &self,
        zone: &Name,
        trust: &TrustPoints,
        fetcher: &dyn Fetcher,
        now_unix: u64,
    ) -> ZoneState {
        let memo = MemoFetcher::new(fetcher);
        self.walk(zone, trust, &memo, now_unix).await.0
    }

    pub async fn validate(
        &self,
        input: &ValidationInput<'_>,
        trust: &TrustPoints,
        ntas: &[(Name, i64)],
        fetcher: &dyn Fetcher,
        now_unix: u64,
    ) -> ValidationResult {
        let result = if under_nta(input.qname, ntas, now_unix) {
            ValidationResult {
                security: Security::Insecure(None),
                ttl_cap: None,
            }
        } else {
            let memo = MemoFetcher::new(fetcher);
            self.validate_response(input, trust, &memo, now_unix).await
        };
        let m = &self.metrics;
        match &result.security {
            Security::Secure => RecursorMetrics::inc(&m.dnssec_secure),
            Security::Insecure(_) => RecursorMetrics::inc(&m.dnssec_insecure),
            Security::Indeterminate(_) => RecursorMetrics::inc(&m.dnssec_indeterminate),
            Security::Bogus(e) => {
                RecursorMetrics::inc(&m.dnssec_bogus);
                let slot = if e.code < 32 { e.code as usize } else { 0 };
                RecursorMetrics::inc(&m.dnssec_bogus_by_ede[slot]);
            }
        }
        result
    }

    async fn validate_response(
        &self,
        input: &ValidationInput<'_>,
        trust: &TrustPoints,
        fetcher: &dyn Fetcher,
        now: u64,
    ) -> ValidationResult {
        let answers = group(input.answers);
        let sname = final_name(input.qname, input.qtype, input.answers);
        // every RRset and the denial are checked concurrently; the walks share the memo's fetches
        let mut checks: Vec<LocalBoxFuture<'_, Outcome>> = Vec::new();
        for set in &answers {
            // CNAMEs synthesised from a DNAME carry no RRSIG; the DNAME is validated instead
            let synthesised = set.rtype == RecordType::CNAME
                && set.sigs.is_empty()
                && answers.iter().any(|d| {
                    d.rtype == RecordType::DNAME && d.name.zone_of(&set.name) && d.name != set.name
                });
            if !synthesised {
                checks.push(Box::pin(self.check_rrset(
                    set,
                    input.authorities,
                    trust,
                    fetcher,
                    now,
                )));
            }
        }
        if matches!(input.rcode, ResponseCode::NoError | ResponseCode::NXDomain) {
            let positive = input
                .answers
                .iter()
                .any(|r| r.name == sname && r.record_type() == input.qtype);
            if input.rcode == ResponseCode::NXDomain || !positive {
                checks.push(Box::pin(
                    self.check_negative(&sname, input, trust, fetcher, now),
                ));
            }
        }
        let mut acc = Combined::default();
        for outcome in join_all(checks).await {
            acc.add(outcome);
        }
        let ttl_cap = acc.ttl_cap;
        ValidationResult {
            security: acc.security(),
            ttl_cap,
        }
    }

    async fn check_rrset(
        &self,
        set: &RrSet,
        authorities: &[Record],
        trust: &TrustPoints,
        fetcher: &dyn Fetcher,
        now: u64,
    ) -> Outcome {
        let signer = set.signer();
        let at = signer.as_ref().unwrap_or(&set.name);
        let (state, key_zone) = self.walk(at, trust, fetcher, now).await;
        let keys = match state {
            ZoneState::Secure(keys) => keys,
            other => return other.into(),
        };
        let Some(signer) = signer else {
            return Outcome::Bogus(Ede::new(
                10,
                format!("RRSIGs missing for {} {}", set.name, set.rtype),
            ));
        };
        if key_zone != signer {
            return Outcome::Bogus(Ede::new(
                6,
                format!(
                    "signer {signer} of {} {} is not a zone apex",
                    set.name, set.rtype
                ),
            ));
        }
        let v = match verify_rrset(&set.records, &set.sigs, &keys, &signer, now) {
            Ok(v) => v,
            Err(e) => return Outcome::Bogus(verify_ede(e, &set.name, set.rtype)),
        };
        let ttl = capped_ttl(set.ttl(), &v, now);
        if v.wildcard_expanded {
            let labels = set
                .sigs
                .iter()
                .filter_map(rrsig)
                .find(|s| s.input().key_tag == v.key_tag)
                .map_or(0, |s| s.input().num_labels);
            let denial = match self.verified_denial(authorities, &signer, &keys, now) {
                Ok(d) => d,
                Err(e) => return Outcome::Bogus(e),
            };
            let proof = if denial.nsec3.is_empty() {
                nsec_proves_no_closer_match(&set.name, labels, &denial.nsec)
            } else {
                nsec3_proves_no_closer_match(&set.name, labels, &signer, &denial.nsec3)
            };
            if proof != Denial::Proven {
                return denial_outcome(proof);
            }
        }
        Outcome::Secure(Some(ttl))
    }

    async fn check_negative(
        &self,
        sname: &Name,
        input: &ValidationInput<'_>,
        trust: &TrustPoints,
        fetcher: &dyn Fetcher,
        now: u64,
    ) -> Outcome {
        let sets = group(input.authorities);
        let soa = sets.iter().find(|s| s.rtype == RecordType::SOA);
        let signer = soa.and_then(RrSet::signer).or_else(|| {
            sets.iter()
                .filter(|s| matches!(s.rtype, RecordType::NSEC | RecordType::NSEC3))
                .find_map(RrSet::signer)
        });
        let at = signer.as_ref().unwrap_or(sname);
        let (state, key_zone) = self.walk(at, trust, fetcher, now).await;
        let keys = match state {
            ZoneState::Secure(keys) => keys,
            other => return other.into(),
        };
        let Some(zone) = signer else {
            return Outcome::Bogus(Ede::new(12, "NSEC missing: no signed denial records"));
        };
        if key_zone != zone {
            return Outcome::Bogus(Ede::new(
                6,
                format!("signer {zone} of the denial for {sname} is not a zone apex"),
            ));
        }
        let mut ttl_cap = None;
        if let Some(soa) = soa {
            match verify_rrset(&soa.records, &soa.sigs, &keys, &zone, now) {
                Ok(v) => ttl_cap = Some(capped_ttl(soa.ttl(), &v, now)),
                Err(e) => return Outcome::Bogus(verify_ede(e, &soa.name, RecordType::SOA)),
            }
        }
        let denial = match self.verified_denial(input.authorities, &zone, &keys, now) {
            Ok(d) => d,
            Err(e) => return Outcome::Bogus(e),
        };
        let proof = match (input.rcode, denial.nsec3.is_empty()) {
            (ResponseCode::NXDomain, true) => nsec_proves_nxdomain(sname, &denial.nsec),
            (ResponseCode::NXDomain, false) => nsec3_proves_nxdomain(sname, &zone, &denial.nsec3),
            (_, true) => nsec_proves_nodata(sname, input.qtype, &denial.nsec),
            (_, false) => nsec3_proves_nodata(sname, input.qtype, &zone, &denial.nsec3),
        };
        if proof != Denial::Proven {
            return denial_outcome(proof);
        }
        if let Some(soa) = soa {
            let mut soa_records = soa.records.clone();
            soa_records.extend(soa.sigs.iter().cloned());
            self.nsec
                .insert_secure(&zone, &soa_records, &denial.records, now);
        }
        let ttl = match (ttl_cap, denial.ttl_cap) {
            (Some(a), Some(b)) => Some(a.min(b)),
            (a, b) => a.or(b),
        };
        Outcome::Secure(ttl)
    }

    /// Verifies the NSEC/NSEC3 RRsets of `records` signed by `zone`; unsigned sets and sets of
    /// other signers are ignored, a set that fails verification makes the response bogus.
    fn verified_denial(
        &self,
        records: &[Record],
        zone: &Name,
        keys: &[DNSKEY],
        now: u64,
    ) -> Result<VerifiedDenial, Ede> {
        let mut out = VerifiedDenial::default();
        for set in group(records)
            .into_iter()
            .filter(|s| matches!(s.rtype, RecordType::NSEC | RecordType::NSEC3))
        {
            if set.signer().as_ref() != Some(zone) {
                continue;
            }
            let v = verify_rrset(&set.records, &set.sigs, keys, zone, now)
                .map_err(|e| verify_ede(e, &set.name, set.rtype))?;
            let ttl = capped_ttl(set.ttl(), &v, now);
            out.ttl_cap = Some(out.ttl_cap.map_or(ttl, |c| c.min(ttl)));
            for r in &set.records {
                match &r.data {
                    RData::DNSSEC(DNSSECRData::NSEC(n)) => {
                        out.nsec.push((r.name.clone(), n.clone()))
                    }
                    RData::DNSSEC(DNSSECRData::NSEC3(n)) => {
                        out.nsec3.push((r.name.clone(), n.clone()))
                    }
                    _ => {}
                }
            }
            out.records.extend(set.records);
            out.records.extend(set.sigs);
        }
        Ok(out)
    }

    fn cached(&self, name: &Name, now: u64) -> Option<Arc<CachedZone>> {
        self.zones.get(name).filter(|c| c.expires > now)
    }

    fn store(&self, name: &Name, state: &ZoneState, key_zone: &Name, ttl: u32, now: u64) {
        let secs = match state {
            ZoneState::Bogus(e) if e.code == EDE_NETWORK_ERROR => return,
            ZoneState::Bogus(_) => BOGUS_CACHE_SECS,
            _ => u64::from(ttl).min(MAX_ZONE_CACHE_SECS),
        };
        self.zones.insert(
            name.clone(),
            Arc::new(CachedZone {
                state: state.clone(),
                key_zone: key_zone.clone(),
                expires: now + secs,
            }),
        );
    }

    /// Walks the chain of trust from the closest trust point down to `zone`, one label at a
    /// time. Returns the state and the zone whose keys a `Secure` state holds.
    async fn walk(
        &self,
        zone: &Name,
        trust: &TrustPoints,
        fetcher: &dyn Fetcher,
        now: u64,
    ) -> (ZoneState, Name) {
        let zone = zone.to_lowercase();
        if let Some(c) = self.cached(&zone, now) {
            return (c.state.clone(), c.key_zone.clone());
        }
        let Some((tp_name, tp)) = trust.closest(&zone) else {
            return (ZoneState::Insecure(None), Name::root());
        };
        let tp_labels = tp_name.num_labels() as usize;
        // resume from the deepest cached ancestor
        let resume = (tp_labels..zone.num_labels() as usize).rev().find_map(|l| {
            let anc = zone.trim_to(l);
            self.cached(&anc, now).map(|c| (anc, c))
        });
        // Fetch the whole remaining chain at once: DS for every label below the resume point,
        // DNSKEY for the trust point and for known cuts. The walk below verifies step by step as
        // before and reads the same (memoised) fetches; whatever it no longer needs is dropped.
        let mut ahead: Vec<LocalBoxFuture<'_, ()>> = Vec::new();
        if resume
            .as_ref()
            .is_none_or(|(_, c)| matches!(c.state, ZoneState::Secure(_)))
        {
            let from = resume
                .as_ref()
                .map_or(tp_labels, |(anc, _)| anc.num_labels() as usize);
            let mut wanted: Vec<(Name, RecordType)> = Vec::new();
            if resume.is_none() {
                wanted.push((tp_name.clone(), RecordType::DNSKEY));
            }
            for l in from + 1..=zone.num_labels() as usize {
                let child = zone.trim_to(l);
                if fetcher.known_cut(&child) {
                    wanted.push((child.clone(), RecordType::DNSKEY));
                }
                wanted.push((child, RecordType::DS));
            }
            if wanted.len() > 1 {
                for (name, rtype) in wanted {
                    ahead.push(Box::pin(async move {
                        let _ = fetcher.fetch(&name, rtype).await;
                    }));
                }
            }
        }
        let walk = async {
            let (mut cur, mut key_zone, mut keys, mut ttl) = match resume {
                Some((anc, c)) => match &c.state {
                    ZoneState::Secure(k) => (
                        anc,
                        c.key_zone.clone(),
                        k.clone(),
                        MAX_ZONE_CACHE_SECS as u32,
                    ),
                    other => {
                        self.store(&zone, other, &c.key_zone, MAX_ZONE_CACHE_SECS as u32, now);
                        return (other.clone(), c.key_zone.clone());
                    }
                },
                None => match self.trust_point_keys(tp_name, tp, fetcher, now).await {
                    Ok((k, t)) => {
                        let state = ZoneState::Secure(k.clone());
                        self.store(tp_name, &state, tp_name, t, now);
                        (tp_name.clone(), tp_name.clone(), k, t)
                    }
                    Err(state) => {
                        self.store(tp_name, &state, tp_name, MAX_ZONE_CACHE_SECS as u32, now);
                        self.store(&zone, &state, tp_name, MAX_ZONE_CACHE_SECS as u32, now);
                        return (state, tp_name.clone());
                    }
                },
            };
            while cur.num_labels() < zone.num_labels() {
                let child = zone.trim_to(cur.num_labels() as usize + 1);
                match self.descend(&child, &key_zone, &keys, fetcher, now).await {
                    Descend::Cut(child_keys, t) => {
                        key_zone = child.clone();
                        keys = child_keys;
                        ttl = ttl.min(t);
                    }
                    Descend::Inside => {}
                    Descend::Nonexistent => break,
                    Descend::Stop(state) => {
                        self.store(&child, &state, &key_zone, ttl, now);
                        self.store(&zone, &state, &key_zone, ttl, now);
                        return (state, key_zone);
                    }
                }
                self.store(
                    &child,
                    &ZoneState::Secure(keys.clone()),
                    &key_zone,
                    ttl,
                    now,
                );
                cur = child;
            }
            let state = ZoneState::Secure(keys);
            self.store(&zone, &state, &key_zone, ttl, now);
            (state, key_zone)
        };
        poll_with(&mut ahead, walk, |_| {}).await
    }

    /// The keys of a trust point's zone: its DNSKEY RRset, authorised by the configured DS or
    /// trusted keys and self-signed by one of them.
    async fn trust_point_keys(
        &self,
        zone: &Name,
        tp: &TrustPoint,
        fetcher: &dyn Fetcher,
        now: u64,
    ) -> Result<(Vec<DNSKEY>, u32), ZoneState> {
        if tp.keys.is_empty() {
            if tp.ds.is_empty() {
                return Err(ZoneState::Insecure(None));
            }
            if !supported_ds(&tp.ds) {
                return Err(unsupported_ds(zone, &tp.ds));
            }
        }
        let set = fetcher
            .fetch(zone, RecordType::DNSKEY)
            .await
            .map_err(|_| network_error(format!("network error fetching DNSKEY {zone}")))?;
        let (records, sigs) = rrset_of(&set.answers, zone, RecordType::DNSKEY);
        let fetched = dnskeys(&records);
        let mut authorised = match match_ds(zone, &fetched, &tp.ds) {
            DsMatch::Matched(k) => k,
            _ => Vec::new(),
        };
        authorised.extend(fetched.iter().filter(|k| tp.keys.contains(k)).cloned());
        if authorised.is_empty() {
            return Err(ZoneState::Bogus(Ede::new(
                9,
                format!("no DNSKEY matches trust anchor for {zone}"),
            )));
        }
        let v = verify_rrset(&records, &sigs, &authorised, zone, now)
            .map_err(|e| ZoneState::Bogus(verify_ede(e, zone, RecordType::DNSKEY)))?;
        let ttl = capped_ttl(records.iter().map(|r| r.ttl).min().unwrap_or(0), &v, now);
        Ok((fetched.into_iter().filter(DNSKEY::zone_key).collect(), ttl))
    }

    /// One step of the walk: is `child` (inside the zone `key_zone`) a signed cut, an unsigned
    /// cut, a name inside the zone, or nonexistent?
    async fn descend(
        &self,
        child: &Name,
        key_zone: &Name,
        keys: &[DNSKEY],
        fetcher: &dyn Fetcher,
        now: u64,
    ) -> Descend {
        let set = match fetcher.fetch(child, RecordType::DS).await {
            Ok(s) => s,
            Err(_) => {
                return Descend::Stop(network_error(format!("network error fetching DS {child}")));
            }
        };
        let (ds_set, ds_sigs) = rrset_of(&set.answers, child, RecordType::DS);
        if !ds_set.is_empty() {
            let v = match verify_rrset(&ds_set, &ds_sigs, keys, key_zone, now) {
                Ok(v) => v,
                Err(e) => {
                    return Descend::Stop(ZoneState::Bogus(verify_ede(e, child, RecordType::DS)));
                }
            };
            let ds_ttl = capped_ttl(ds_set.iter().map(|r| r.ttl).min().unwrap_or(0), &v, now);
            let ds = ds_records(&ds_set);
            if !supported_ds(&ds) {
                return Descend::Stop(unsupported_ds(child, &ds));
            }
            let kset = match fetcher.fetch(child, RecordType::DNSKEY).await {
                Ok(s) => s,
                Err(_) => {
                    return Descend::Stop(network_error(format!(
                        "network error fetching DNSKEY {child}"
                    )));
                }
            };
            let (krecords, ksigs) = rrset_of(&kset.answers, child, RecordType::DNSKEY);
            let fetched = dnskeys(&krecords);
            return match match_ds(child, &fetched, &ds) {
                DsMatch::Matched(authorised) => {
                    match verify_rrset(&krecords, &ksigs, &authorised, child, now) {
                        Ok(kv) => {
                            let kttl = capped_ttl(
                                krecords.iter().map(|r| r.ttl).min().unwrap_or(0),
                                &kv,
                                now,
                            );
                            Descend::Cut(
                                fetched.into_iter().filter(DNSKEY::zone_key).collect(),
                                ds_ttl.min(kttl),
                            )
                        }
                        Err(e) => Descend::Stop(ZoneState::Bogus(verify_ede(
                            e,
                            child,
                            RecordType::DNSKEY,
                        ))),
                    }
                }
                DsMatch::NoSupported => Descend::Stop(unsupported_ds(child, &ds)),
                DsMatch::NoMatch => Descend::Stop(ZoneState::Bogus(Ede::new(
                    9,
                    format!("no DNSKEY matches DS for {child}"),
                ))),
            };
        }
        if !matches!(set.rcode, ResponseCode::NoError | ResponseCode::NXDomain) {
            return Descend::Stop(network_error(format!("{} fetching DS {child}", set.rcode)));
        }
        let denial = match self.verified_denial(&set.authorities, key_zone, keys, now) {
            Ok(d) => d,
            Err(e) => return Descend::Stop(ZoneState::Bogus(e)),
        };
        let nxdomain = set.rcode == ResponseCode::NXDomain;
        let proof = match (nxdomain, denial.nsec3.is_empty()) {
            (true, true) => nsec_proves_nxdomain(child, &denial.nsec),
            (true, false) => nsec3_proves_nxdomain(child, key_zone, &denial.nsec3),
            (false, true) => nsec_proves_nodata(child, RecordType::DS, &denial.nsec),
            (false, false) => nsec3_proves_nodata(child, RecordType::DS, key_zone, &denial.nsec3),
        };
        match proof {
            Denial::Proven if nxdomain => Descend::Nonexistent,
            Denial::Proven => {
                let delegation = denial
                    .nsec
                    .iter()
                    .find(|(o, _)| o == child)
                    .map(|(_, n)| n.type_set().contains(RecordType::NS))
                    .or_else(|| {
                        nsec3_match(child, key_zone, &denial.nsec3)
                            .map(|n| n.type_set().contains(RecordType::NS))
                    })
                    .unwrap_or(false);
                if delegation {
                    Descend::Stop(ZoneState::Insecure(None))
                } else {
                    Descend::Inside
                }
            }
            Denial::ProvenOptOut => Descend::Stop(ZoneState::Insecure(None)),
            Denial::NotProven(_) => Descend::Stop(ZoneState::Bogus(Ede::new(
                12,
                format!("no proof of missing DS for {child}"),
            ))),
            other => match denial_outcome(other) {
                Outcome::Insecure(e) => Descend::Stop(ZoneState::Insecure(e)),
                Outcome::Bogus(e) => Descend::Stop(ZoneState::Bogus(e)),
                _ => Descend::Inside,
            },
        }
    }
}

/// The last name of the CNAME chain starting at `qname` (bounded by `MAX_CNAME_DEPTH`).
fn final_name(qname: &Name, qtype: RecordType, answers: &[Record]) -> Name {
    let mut sname = qname.clone();
    if qtype == RecordType::CNAME {
        return sname;
    }
    for _ in 0..crate::recursor::MAX_CNAME_DEPTH {
        let next = answers.iter().find_map(|r| match &r.data {
            RData::CNAME(c) if r.name == sname => Some(c.0.clone()),
            _ => None,
        });
        match next {
            Some(n) => sname = n,
            None => break,
        }
    }
    sname
}
