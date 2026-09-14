#![allow(clippy::cloned_ref_to_slice_refs)] // plan-given test literals

use super::Ede;
use super::forward::{fetch_via, validation_query};
use super::testsign::{TestKey, nsec_chain};
use super::validator::*;
use crate::recursor::LocalBoxFuture;
use crate::recursor::metrics::RecursorMetrics;
use bytes::Bytes;
use hickory_proto::dnssec::rdata::{DNSSECRData, DS};
use hickory_proto::dnssec::{Algorithm, DigestType};
use hickory_proto::op::{Message, MessageType, ResponseCode};
use hickory_proto::rr::{
    Name, RData, Record, RecordType,
    rdata::{A, NS, SOA},
};
use std::cell::RefCell;
use std::collections::HashMap;
use std::net::Ipv4Addr;
use std::sync::Arc;

const NOW: u64 = 1_800_000_000;
const INC: u32 = (NOW - 3600) as u32;
const EXP: u32 = (NOW + 86_400) as u32;

fn n(s: &str) -> Name {
    Name::from_ascii(s).unwrap()
}
fn rec(name: &str, data: RData) -> Record {
    Record::from_rdata(n(name), 300, data)
}

#[derive(Default)]
struct Mock {
    sets: HashMap<(Name, RecordType), FetchedSet>,
    calls: RefCell<u32>,
}
impl Fetcher for Mock {
    fn fetch<'a>(
        &'a self,
        name: &'a Name,
        rtype: RecordType,
    ) -> LocalBoxFuture<'a, Result<FetchedSet, FetchError>> {
        *self.calls.borrow_mut() += 1;
        let r = self
            .sets
            .get(&(name.to_lowercase(), rtype))
            .map(|s| FetchedSet {
                rcode: s.rcode,
                answers: s.answers.clone(),
                authorities: s.authorities.clone(),
            })
            .ok_or(FetchError::Unreachable);
        Box::pin(async move { r })
    }
}

struct World {
    root: TestKey,
    example: TestKey,
    mock: Mock,
    trust: TrustPoints,
}

fn signed(key: &TestKey, set: Vec<Record>) -> Vec<Record> {
    let s = key.sign(&set, INC, EXP);
    let mut v = set;
    v.push(s);
    v
}

fn world() -> World {
    let root = TestKey::generate(".", true);
    let example = TestKey::generate("example.", true);
    let mut mock = Mock::default();
    let ok = |answers: Vec<Record>| FetchedSet {
        rcode: ResponseCode::NoError,
        answers,
        authorities: vec![],
    };
    mock.sets.insert(
        (n("."), RecordType::DNSKEY),
        ok(signed(&root, vec![root.dnskey_record()])),
    );
    mock.sets.insert(
        (n("example."), RecordType::DS),
        ok(signed(
            &root,
            vec![rec(
                "example.",
                RData::DNSSEC(DNSSECRData::DS(example.ds())),
            )],
        )),
    );
    mock.sets.insert(
        (n("example."), RecordType::DNSKEY),
        ok(signed(&example, vec![example.dnskey_record()])),
    );
    // plain.example. is an unsigned delegation: NODATA for DS with a signed NSEC proving NS without DS
    let chain = nsec_chain(
        "example.",
        &[
            (
                "example.",
                &[RecordType::SOA, RecordType::NS, RecordType::DNSKEY],
            ),
            ("plain.example.", &[RecordType::NS]),
            ("www.example.", &[RecordType::A]),
        ],
    );
    let soa = rec(
        "example.",
        RData::SOA(SOA::new(
            n("ns.example."),
            n("h.example."),
            1,
            3600,
            600,
            86400,
            300,
        )),
    );
    let mut auth = signed(&example, vec![soa.clone()]);
    let plain_nsec = rec(
        "plain.example.",
        RData::DNSSEC(DNSSECRData::NSEC(
            chain
                .iter()
                .find(|(o, _)| *o == n("plain.example."))
                .unwrap()
                .1
                .clone(),
        )),
    );
    auth.extend(signed(&example, vec![plain_nsec]));
    mock.sets.insert(
        (n("plain.example."), RecordType::DS),
        FetchedSet {
            rcode: ResponseCode::NoError,
            answers: vec![],
            authorities: auth,
        },
    );
    // www.example. is not a zone cut: NODATA for DS with an NSEC whose bitmap has no NS
    let mut www_auth = signed(&example, vec![soa.clone()]);
    let www_nsec = rec(
        "www.example.",
        RData::DNSSEC(DNSSECRData::NSEC(
            chain
                .iter()
                .find(|(o, _)| *o == n("www.example."))
                .unwrap()
                .1
                .clone(),
        )),
    );
    www_auth.extend(signed(&example, vec![www_nsec]));
    mock.sets.insert(
        (n("www.example."), RecordType::DS),
        FetchedSet {
            rcode: ResponseCode::NoError,
            answers: vec![],
            authorities: www_auth,
        },
    );
    // nodenial.example.: parent returns NODATA for DS without any NSEC
    mock.sets.insert(
        (n("nodenial.example."), RecordType::DS),
        FetchedSet {
            rcode: ResponseCode::NoError,
            answers: vec![],
            authorities: signed(&example, vec![soa]),
        },
    );
    let trust = TrustPoints {
        zones: vec![(
            Name::root(),
            TrustPoint {
                ds: vec![root.ds()],
                keys: vec![],
            },
        )],
    };
    World {
        root,
        example,
        mock,
        trust,
    }
}

fn input<'a>(
    qname: &'a Name,
    rcode: ResponseCode,
    answers: &'a [Record],
    authorities: &'a [Record],
) -> ValidationInput<'a> {
    ValidationInput {
        qname,
        qtype: RecordType::A,
        rcode,
        answers,
        authorities,
    }
}

fn validator() -> Validator {
    Validator::new(Arc::new(RecursorMetrics::default()))
}

#[tokio::test(flavor = "current_thread")]
async fn secure_answer_validates() {
    let w = world();
    let v = validator();
    let q = n("www.example.");
    let answers = signed(
        &w.example,
        vec![rec(
            "www.example.",
            RData::A(A(Ipv4Addr::new(192, 0, 2, 10))),
        )],
    );
    let r = v
        .validate(
            &input(&q, ResponseCode::NoError, &answers, &[]),
            &w.trust,
            &[],
            &w.mock,
            NOW,
        )
        .await;
    assert_eq!(r.security, Security::Secure);
    assert_eq!(r.ttl_cap, Some(300));
    let _ = &w.root;
}

#[tokio::test(flavor = "current_thread")]
async fn broken_signature_is_bogus_ede_6() {
    let w = world();
    let metrics = Arc::new(RecursorMetrics::default());
    let v = Validator::new(metrics.clone());
    let q = n("www.example.");
    let mut answers = signed(
        &w.example,
        vec![rec(
            "www.example.",
            RData::A(A(Ipv4Addr::new(192, 0, 2, 10))),
        )],
    );
    answers[0].data = RData::A(A(Ipv4Addr::new(6, 6, 6, 6)));
    let r = v
        .validate(
            &input(&q, ResponseCode::NoError, &answers, &[]),
            &w.trust,
            &[],
            &w.mock,
            NOW,
        )
        .await;
    assert_eq!(
        r.security,
        Security::Bogus(Ede {
            code: 6,
            text: "bad signature for www.example. A".into()
        })
    );
    assert_eq!(
        metrics.dnssec_bogus_by_ede[6].load(std::sync::atomic::Ordering::Relaxed),
        1
    );
}

#[tokio::test(flavor = "current_thread")]
async fn expired_signature_is_bogus_ede_7() {
    let w = world();
    let v = validator();
    let q = n("www.example.");
    let set = vec![rec(
        "www.example.",
        RData::A(A(Ipv4Addr::new(192, 0, 2, 10))),
    )];
    let mut answers = set.clone();
    answers.push(w.example.sign(&set, INC - 90_000, INC - 86_400));
    let r = v
        .validate(
            &input(&q, ResponseCode::NoError, &answers, &[]),
            &w.trust,
            &[],
            &w.mock,
            NOW,
        )
        .await;
    assert!(
        matches!(r.security, Security::Bogus(Ede { code: 7, .. })),
        "{:?}",
        r.security
    );
}

#[tokio::test(flavor = "current_thread")]
async fn missing_rrsig_in_signed_zone_is_bogus_ede_10() {
    let w = world();
    let v = validator();
    let q = n("www.example.");
    let answers = vec![rec(
        "www.example.",
        RData::A(A(Ipv4Addr::new(192, 0, 2, 10))),
    )];
    let r = v
        .validate(
            &input(&q, ResponseCode::NoError, &answers, &[]),
            &w.trust,
            &[],
            &w.mock,
            NOW,
        )
        .await;
    assert!(
        matches!(r.security, Security::Bogus(Ede { code: 10, .. })),
        "{:?}",
        r.security
    );
}

#[tokio::test(flavor = "current_thread")]
async fn proven_unsigned_delegation_is_insecure_and_unproven_is_bogus_ede_12() {
    let w = world();
    let v = validator();
    let q = n("www.plain.example.");
    let answers = vec![rec(
        "www.plain.example.",
        RData::A(A(Ipv4Addr::new(192, 0, 2, 20))),
    )];
    let r = v
        .validate(
            &input(&q, ResponseCode::NoError, &answers, &[]),
            &w.trust,
            &[],
            &w.mock,
            NOW,
        )
        .await;
    assert_eq!(r.security, Security::Insecure(None));
    let q = n("www.nodenial.example.");
    let answers = vec![rec(
        "www.nodenial.example.",
        RData::A(A(Ipv4Addr::new(192, 0, 2, 21))),
    )];
    let r = v
        .validate(
            &input(&q, ResponseCode::NoError, &answers, &[]),
            &w.trust,
            &[],
            &w.mock,
            NOW,
        )
        .await;
    assert!(
        matches!(r.security, Security::Bogus(Ede { code: 12, .. })),
        "{:?}",
        r.security
    );
}

#[tokio::test(flavor = "current_thread")]
async fn unsupported_ds_algorithm_is_insecure_ede_1() {
    let mut w = world();
    let bad = DS::new(
        4242,
        Algorithm::Unknown(200),
        DigestType::SHA256,
        vec![0; 32],
    );
    w.mock.sets.insert(
        (n("example."), RecordType::DS),
        FetchedSet {
            rcode: ResponseCode::NoError,
            answers: signed(
                &w.root,
                vec![rec("example.", RData::DNSSEC(DNSSECRData::DS(bad)))],
            ),
            authorities: vec![],
        },
    );
    let v = validator();
    let q = n("www.example.");
    let answers = signed(
        &w.example,
        vec![rec(
            "www.example.",
            RData::A(A(Ipv4Addr::new(192, 0, 2, 10))),
        )],
    );
    let r = v
        .validate(
            &input(&q, ResponseCode::NoError, &answers, &[]),
            &w.trust,
            &[],
            &w.mock,
            NOW,
        )
        .await;
    assert!(
        matches!(r.security, Security::Insecure(Some(Ede { code: 1, .. }))),
        "{:?}",
        r.security
    );
}

#[tokio::test(flavor = "current_thread")]
async fn negative_trust_anchor_makes_bogus_zone_insecure_without_fetching() {
    let w = world();
    let v = validator();
    let q = n("www.example.");
    let answers = vec![rec("www.example.", RData::A(A(Ipv4Addr::new(6, 6, 6, 6))))];
    let ntas = vec![(n("example."), (NOW + 60) as i64)];
    let r = v
        .validate(
            &input(&q, ResponseCode::NoError, &answers, &[]),
            &w.trust,
            &ntas,
            &w.mock,
            NOW,
        )
        .await;
    assert_eq!(r.security, Security::Insecure(None));
    assert_eq!(*w.mock.calls.borrow(), 0);
    assert!(
        !under_nta(&q, &ntas, NOW + 61),
        "expired NTA no longer applies"
    );
}

#[tokio::test(flavor = "current_thread")]
async fn signed_nxdomain_validates_and_feeds_aggressive_cache() {
    let w = world();
    let v = validator();
    let chain = nsec_chain(
        "example.",
        &[
            (
                "example.",
                &[RecordType::SOA, RecordType::NS, RecordType::DNSKEY],
            ),
            ("plain.example.", &[RecordType::NS]),
            ("www.example.", &[RecordType::A]),
        ],
    );
    let soa = rec(
        "example.",
        RData::SOA(SOA::new(
            n("ns.example."),
            n("h.example."),
            1,
            3600,
            600,
            86400,
            300,
        )),
    );
    let mut auth = signed(&w.example, vec![soa]);
    for (owner, nsec) in &chain {
        auth.extend(signed(
            &w.example,
            vec![Record::from_rdata(
                owner.clone(),
                300,
                RData::DNSSEC(DNSSECRData::NSEC(nsec.clone())),
            )],
        ));
    }
    let q = n("abc.example.");
    let r = v
        .validate(
            &input(&q, ResponseCode::NXDomain, &[], &auth),
            &w.trust,
            &[],
            &w.mock,
            NOW,
        )
        .await;
    assert_eq!(r.security, Security::Secure);
    let (rcode, records) = v
        .nsec
        .synthesize(&n("abd.example."), RecordType::A, NOW)
        .expect("synthesised");
    assert_eq!(rcode, ResponseCode::NXDomain);
    assert!(records.iter().any(|r| r.record_type() == RecordType::SOA));
    assert!(
        v.nsec
            .synthesize(&n("www.example."), RecordType::A, NOW)
            .is_none(),
        "existing name is never denied"
    );
    let stripped: Vec<Record> = auth
        .iter()
        .filter(|r| r.record_type() != RecordType::NSEC)
        .cloned()
        .collect();
    let r = v
        .validate(
            &input(&n("abe.example."), ResponseCode::NXDomain, &[], &stripped),
            &w.trust,
            &[],
            &w.mock,
            NOW,
        )
        .await;
    assert!(
        matches!(r.security, Security::Bogus(Ede { code: 12, .. })),
        "{:?}",
        r.security
    );
    let _ = NS(n("x."));
}

/// A validating forwarder in front of the in-memory hierarchy: it answers any query carrying DO=1
/// and CD=1 from the mock's data, with the RRSIGs.
struct Forwarder<'a> {
    mock: &'a Mock,
    extra: HashMap<(Name, RecordType), Vec<Record>>,
}

impl Forwarder<'_> {
    fn answer(&self, query: &[u8]) -> Result<Bytes, String> {
        let q = Message::from_vec(query).map_err(|e| e.to_string())?;
        let edns = q.edns.as_ref().ok_or("no EDNS")?;
        assert!(
            edns.flags().dnssec_ok && q.metadata.checking_disabled && q.metadata.recursion_desired,
            "validation queries carry DO=1, CD=1, RD=1"
        );
        assert_eq!(edns.max_payload(), 1232);
        let question = &q.queries[0];
        let key = (question.name().to_lowercase(), question.query_type());
        let mut reply = Message::new(q.metadata.id, MessageType::Response, q.metadata.op_code);
        reply.queries = q.queries.clone();
        if let Some(a) = self.extra.get(&key) {
            reply.answers = a.clone();
        } else if let Some(s) = self.mock.sets.get(&key) {
            reply.metadata.response_code = s.rcode;
            reply.answers = s.answers.clone();
            reply.authorities = s.authorities.clone();
        } else {
            return Err("unreachable".into());
        }
        reply.to_vec().map(Bytes::from).map_err(|e| e.to_string())
    }
}

impl Fetcher for Forwarder<'_> {
    fn fetch<'a>(
        &'a self,
        name: &'a Name,
        rtype: RecordType,
    ) -> LocalBoxFuture<'a, Result<FetchedSet, FetchError>> {
        Box::pin(fetch_via(
            move |q: Vec<u8>| async move { self.answer(&q) },
            name,
            rtype,
        ))
    }
}

#[tokio::test(flavor = "current_thread")]
async fn forward_mode_validates_through_the_forwarder() {
    let w = world();
    let good = signed(
        &w.example,
        vec![rec(
            "www.example.",
            RData::A(A(Ipv4Addr::new(192, 0, 2, 10))),
        )],
    );
    let mut broken = signed(
        &w.example,
        vec![rec(
            "bad.example.",
            RData::A(A(Ipv4Addr::new(192, 0, 2, 11))),
        )],
    );
    broken[0].data = RData::A(A(Ipv4Addr::new(6, 6, 6, 6)));
    let fwd = Forwarder {
        mock: &w.mock,
        extra: HashMap::from([
            ((n("www.example."), RecordType::A), good),
            ((n("bad.example."), RecordType::A), broken),
        ]),
    };
    let v = validator();
    for (name, want_secure) in [("www.example.", true), ("bad.example.", false)] {
        let q = n(name);
        // the answer itself comes through the same forwarder, with a query built for validation
        let reply = fwd.answer(&validation_query(&q, RecordType::A)).unwrap();
        let m = Message::from_vec(&reply).unwrap();
        let r = v
            .validate(
                &input(&q, m.metadata.response_code, &m.answers, &m.authorities),
                &w.trust,
                &[],
                &fwd,
                NOW,
            )
            .await;
        if want_secure {
            assert_eq!(r.security, Security::Secure, "{name}");
        } else {
            assert!(
                matches!(r.security, Security::Bogus(Ede { code: 6, .. })),
                "{name}: {:?}",
                r.security
            );
        }
    }
    let unreachable = fetch_via(
        |_q: Vec<u8>| async { Err::<Bytes, String>("down".into()) },
        &n("example."),
        RecordType::DS,
    )
    .await;
    assert!(matches!(unreachable, Err(FetchError::Unreachable)));
}

#[tokio::test(flavor = "current_thread")]
async fn unreachable_authority_is_indeterminate() {
    let w = world();
    let v = validator();
    let empty = Mock::default();
    let q = n("www.example.");
    let answers = signed(
        &w.example,
        vec![rec(
            "www.example.",
            RData::A(A(Ipv4Addr::new(192, 0, 2, 10))),
        )],
    );
    let r = v
        .validate(
            &input(&q, ResponseCode::NoError, &answers, &[]),
            &w.trust,
            &[],
            &empty,
            NOW,
        )
        .await;
    assert!(
        matches!(r.security, Security::Indeterminate(Ede { code: 23, .. })),
        "{:?}",
        r.security
    );
    // the network failure is not cached: the zone validates once the authority is back
    let r = v
        .validate(
            &input(&q, ResponseCode::NoError, &answers, &[]),
            &w.trust,
            &[],
            &w.mock,
            NOW,
        )
        .await;
    assert_eq!(r.security, Security::Secure);
}

#[tokio::test(flavor = "current_thread")]
async fn wildcard_expansion_needs_a_next_closer_proof() {
    let w = world();
    let v = validator();
    let wild = vec![rec("*.example.", RData::A(A(Ipv4Addr::new(192, 0, 2, 30))))];
    let sig = w.example.sign(&wild, INC, EXP);
    let q = n("foo.example.");
    let mut answers = vec![wild[0].clone(), sig];
    for r in &mut answers {
        r.name = q.clone();
    }
    // foo.example. lies between example. and plain.example.
    let chain = nsec_chain(
        "example.",
        &[
            ("example.", &[RecordType::SOA, RecordType::NS]),
            ("*.example.", &[RecordType::A]),
            ("plain.example.", &[RecordType::NS]),
        ],
    );
    let (owner, nsec) = chain.iter().find(|(o, _)| *o == n("*.example.")).unwrap();
    let proof = signed(
        &w.example,
        vec![Record::from_rdata(
            owner.clone(),
            300,
            RData::DNSSEC(DNSSECRData::NSEC(nsec.clone())),
        )],
    );
    let r = v
        .validate(
            &input(&q, ResponseCode::NoError, &answers, &proof),
            &w.trust,
            &[],
            &w.mock,
            NOW,
        )
        .await;
    assert_eq!(r.security, Security::Secure);
    let r = v
        .validate(
            &input(&q, ResponseCode::NoError, &answers, &[]),
            &w.trust,
            &[],
            &w.mock,
            NOW,
        )
        .await;
    assert!(
        matches!(r.security, Security::Bogus(Ede { code: 12, .. })),
        "{:?}",
        r.security
    );
}

/// Answers from `inner` after a short delay, recording every fetch and the peak concurrency.
struct Delayed<'a> {
    inner: &'a Mock,
    fetches: RefCell<Vec<(Name, RecordType)>>,
    in_flight: std::cell::Cell<u32>,
    peak: std::cell::Cell<u32>,
}

impl<'a> Delayed<'a> {
    fn new(inner: &'a Mock) -> Self {
        Delayed {
            inner,
            fetches: RefCell::new(Vec::new()),
            in_flight: std::cell::Cell::new(0),
            peak: std::cell::Cell::new(0),
        }
    }
}

impl Fetcher for Delayed<'_> {
    fn fetch<'a>(
        &'a self,
        name: &'a Name,
        rtype: RecordType,
    ) -> LocalBoxFuture<'a, Result<FetchedSet, FetchError>> {
        Box::pin(async move {
            self.fetches.borrow_mut().push((name.to_lowercase(), rtype));
            self.in_flight.set(self.in_flight.get() + 1);
            self.peak.set(self.peak.get().max(self.in_flight.get()));
            tokio::time::sleep(std::time::Duration::from_millis(5)).await;
            self.in_flight.set(self.in_flight.get() - 1);
            self.inner.fetch(name, rtype).await
        })
    }
}

#[tokio::test(flavor = "current_thread")]
async fn memo_fetcher_shares_a_fetch_in_progress() {
    // catches: concurrent walks each sending their own query for the same RRset
    let w = world();
    let slow = Delayed::new(&w.mock);
    let memo = MemoFetcher::new(&slow);
    let root = Name::root();
    let example = n("example.");
    let results = crate::recursor::join_all(vec![
        memo.fetch(&root, RecordType::DNSKEY),
        memo.fetch(&root, RecordType::DNSKEY),
        memo.fetch(&example, RecordType::DS),
    ])
    .await;
    assert_eq!(
        *slow.fetches.borrow(),
        vec![
            (Name::root(), RecordType::DNSKEY),
            (n("example."), RecordType::DS)
        ]
    );
    assert_eq!(
        slow.peak.get(),
        2,
        "different RRsets are fetched concurrently"
    );
    let answers: Vec<usize> = results
        .iter()
        .map(|r| r.as_ref().unwrap().answers.len())
        .collect();
    assert_eq!(answers[0], answers[1]);
}

#[tokio::test(flavor = "current_thread")]
async fn chain_of_trust_is_fetched_concurrently_with_the_same_verdicts() {
    // catches: DS and DNSKEY lookups sent one after the other (a round trip per step), and
    // look-ahead fetches changing a verdict (secure, insecure delegation, bogus signature)
    let w = world();
    let v = validator();
    let slow = Delayed::new(&w.mock);
    let q = n("www.example.");
    let answers = signed(
        &w.example,
        vec![rec(
            "www.example.",
            RData::A(A(Ipv4Addr::new(192, 0, 2, 10))),
        )],
    );
    let r = v
        .validate(
            &input(&q, ResponseCode::NoError, &answers, &[]),
            &w.trust,
            &[],
            &slow,
            NOW,
        )
        .await;
    assert_eq!(r.security, Security::Secure);
    assert!(
        slow.peak.get() >= 2,
        "DNSKEY . and DS example. are fetched together (peak {})",
        slow.peak.get()
    );
    let fetched = slow.fetches.borrow().clone();
    for key in &fetched {
        assert_eq!(
            fetched.iter().filter(|k| *k == key).count(),
            1,
            "{key:?} fetched twice"
        );
    }
    // a fresh validator: the insecure delegation and a bogus signature keep their verdicts
    let v = validator();
    let q = n("www.plain.example.");
    let plain = vec![rec(
        "www.plain.example.",
        RData::A(A(Ipv4Addr::new(192, 0, 2, 20))),
    )];
    let r = v
        .validate(
            &input(&q, ResponseCode::NoError, &plain, &[]),
            &w.trust,
            &[],
            &Delayed::new(&w.mock),
            NOW,
        )
        .await;
    assert_eq!(r.security, Security::Insecure(None));
    let v = validator();
    let q = n("www.example.");
    let mut forged = answers.clone();
    forged[0].data = RData::A(A(Ipv4Addr::new(6, 6, 6, 6)));
    let r = v
        .validate(
            &input(&q, ResponseCode::NoError, &forged, &[]),
            &w.trust,
            &[],
            &Delayed::new(&w.mock),
            NOW,
        )
        .await;
    assert!(
        matches!(r.security, Security::Bogus(Ede { code: 6, .. })),
        "{:?}",
        r.security
    );
}
