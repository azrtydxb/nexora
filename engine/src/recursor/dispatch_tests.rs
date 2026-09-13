use super::dispatch::*;
use super::dnssec::testsign::TestKey;
use super::rpz::index::{RpzSet, RpzZoneIndex};
use super::rpz::parse::{RpzAction, parse_rpz_text};
use super::testnet::{Behaviour, FakeNet, FakeZone};
use super::{LocalBoxFuture, RecursorState};
use crate::proto;
use crate::snapshot::DirBlobs;
use bytes::Bytes;
use hickory_proto::dnssec::rdata::DNSSECRData;
use hickory_proto::op::{Message, MessageType, Query, ResponseCode};
use hickory_proto::rr::{Name, RData, Record, RecordType, rdata::A};
use hickory_proto::serialize::binary::BinEncodable;
use std::cell::Cell;
use std::collections::HashMap;
use std::net::Ipv4Addr;
use std::sync::Arc;

const SOA: &str = "@ 300 IN SOA ns hostmaster 1 3600 600 86400 300\n";

struct NoUpstream(Cell<u32>);
impl ForwardUpstream for NoUpstream {
    fn forward<'a>(&'a self, _query: &'a [u8]) -> LocalBoxFuture<'a, Result<Bytes, String>> {
        self.0.set(self.0.get() + 1);
        Box::pin(async { Err("no upstream in this test".to_string()) })
    }
}

fn no_blobs() -> DirBlobs {
    DirBlobs {
        dir: std::path::PathBuf::from("/nonexistent/nexora-test-blobs"),
    }
}

async fn net() -> FakeNet {
    FakeNet::start(vec![
        FakeZone {
            origin: ".",
            ip: Ipv4Addr::new(127, 0, 54, 1),
            behaviour: Behaviour::Normal,
            text: format!(
                "{SOA}@ 300 IN NS root.fake.\nroot.fake. 300 IN A 127.0.54.1\ncorp 300 IN NS ns.corp\nns.corp 300 IN A 127.0.54.3\npublic 300 IN NS ns.public\nns.public 300 IN A 127.0.54.3\n"
            ),
        },
        FakeZone {
            origin: "public.",
            ip: Ipv4Addr::new(127, 0, 54, 3),
            behaviour: Behaviour::Normal,
            text: format!("{SOA}@ 300 IN NS ns\nns 300 IN A 127.0.54.3\nwww 300 IN A 192.0.2.10\n"),
        },
        FakeZone {
            origin: "corp.",
            ip: Ipv4Addr::new(127, 0, 54, 7),
            behaviour: Behaviour::Normal,
            text: format!("{SOA}@ 300 IN NS ns\nwww 300 IN A 10.0.0.10\n"),
        },
    ])
    .await
}

fn snapshot(mode: proto::ResolutionMode, port: u16) -> proto::ConfigSnapshot {
    proto::ConfigSnapshot {
        resolution_mode: mode as i32,
        recursion: Some(proto::RecursionConfig {
            root_hints: vec![proto::RootHint {
                name: "root.fake.".into(),
                addresses: vec!["127.0.54.1".into()],
            }],
            qname_minimisation: true,
            aggressive_nsec: false,
            max_upstream_queries: 100,
            max_delegation_depth: 32,
            authority_port: u32::from(port),
        }),
        forward_zones: vec![proto::ForwardZone {
            domain: "corp.".into(),
            addresses: vec![format!("127.0.54.7:{port}")],
            validate: false,
        }],
        ..Default::default()
    }
}

fn query_wire(name: &str) -> Vec<u8> {
    let mut m = Message::query();
    m.metadata.recursion_desired = true;
    m.queries
        .push(Query::query(Name::from_ascii(name).unwrap(), RecordType::A));
    m.to_vec().unwrap()
}

fn q<'a>(name: &str, wire: &'a [u8], dnssec_ok: bool) -> MissQuery<'a> {
    MissQuery {
        qname: Name::from_ascii(name).unwrap(),
        qtype: RecordType::A,
        client_ip: "127.0.0.1".parse().unwrap(),
        dnssec_ok,
        checking_disabled: false,
        authentic_data: false,
        over_tcp: false,
        query: wire,
        rpz: RpzPending::None,
    }
}

fn wire(n: &str) -> Vec<u8> {
    Name::from_ascii(n)
        .unwrap()
        .to_lowercase()
        .to_bytes()
        .unwrap()
}

#[test]
fn route_prefers_longest_forward_zone_then_mode() {
    let mut s = snapshot(proto::ResolutionMode::Recursive, 53);
    s.forward_zones.push(proto::ForwardZone {
        domain: "lab.corp.".into(),
        addresses: vec!["127.0.0.9:53".into()],
        validate: true,
    });
    let rt = ResolutionRuntime::build(&s, &no_blobs()).unwrap();
    match rt.route(&wire("a.lab.corp.")) {
        Route::ForwardZone(z) => assert_eq!(z.domain, Name::from_ascii("lab.corp.").unwrap()),
        _ => panic!("expected lab.corp."),
    }
    match rt.route(&wire("corp.")) {
        Route::ForwardZone(z) => assert_eq!(z.domain, Name::from_ascii("corp.").unwrap()),
        _ => panic!("expected corp."),
    }
    assert!(matches!(rt.route(&wire("xcorp.")), Route::Recursive));
    let rt = ResolutionRuntime::build(
        &snapshot(proto::ResolutionMode::Unspecified, 53),
        &no_blobs(),
    )
    .unwrap();
    assert!(matches!(rt.route(&wire("www.public.")), Route::Forward));
    assert_ne!(
        config_key(&snapshot(proto::ResolutionMode::Forward, 53)),
        config_key(&snapshot(proto::ResolutionMode::Recursive, 53)),
        "a mode change must change the cache-invalidation key"
    );
}

#[tokio::test(flavor = "current_thread")]
async fn recursive_mode_resolves_without_upstreams_and_forward_zone_overrides() {
    let net = net().await;
    let dir = tempfile::tempdir().unwrap();
    let state = RecursorState::new(Some(dir.path()));
    let rt = ResolutionRuntime::build(
        &snapshot(proto::ResolutionMode::Recursive, net.port),
        &no_blobs(),
    )
    .unwrap();
    let up = NoUpstream(Cell::new(0));
    let w = query_wire("www.public.");
    let a = resolve_miss(&rt, &state, &up, &q("www.public.", &w, false)).await;
    let m = Message::from_vec(&a.wire).unwrap();
    assert_eq!(m.metadata.response_code, ResponseCode::NoError);
    assert!(m.metadata.recursion_available);
    assert_eq!(m.answers[0].data, RData::A(A(Ipv4Addr::new(192, 0, 2, 10))));
    assert_eq!(a.route, RouteTaken::Recursive);
    assert!(a.cacheable && !a.failed);
    let w = query_wire("www.corp.");
    let b = resolve_miss(&rt, &state, &up, &q("www.corp.", &w, false)).await;
    let m = Message::from_vec(&b.wire).unwrap();
    assert_eq!(
        m.answers[0].data,
        RData::A(A(Ipv4Addr::new(10, 0, 0, 10))),
        "forward zone beats recursion (root delegates corp. elsewhere)"
    );
    assert_eq!(b.route, RouteTaken::ForwardZone);
    assert_eq!(
        up.0.get(),
        0,
        "global upstreams never used in recursive mode"
    );
}

#[tokio::test(flavor = "current_thread")]
async fn forward_mode_sends_client_query_to_upstreams_and_failure_is_servfail_with_ede() {
    let state = RecursorState::new(None);
    let rt = ResolutionRuntime::build(&snapshot(proto::ResolutionMode::Forward, 53), &no_blobs())
        .unwrap();
    let up = NoUpstream(Cell::new(0));
    let w = query_wire("www.public.");
    let a = resolve_miss(&rt, &state, &up, &q("www.public.", &w, false)).await;
    assert_eq!(up.0.get(), 1);
    let m = Message::from_vec(&a.wire).unwrap();
    assert_eq!(m.metadata.response_code, ResponseCode::ServFail);
    assert_eq!(
        a.ede,
        Some(Ede {
            code: 22,
            text: "no reachable authority".into()
        })
    );
    assert!(a.failed && !a.cacheable);
    assert_eq!(a.route, RouteTaken::Forward);
}

#[tokio::test(flavor = "current_thread")]
async fn root_unreachable_is_servfail_ede_22() {
    let state = RecursorState::new(None);
    let mut s = snapshot(proto::ResolutionMode::Recursive, 1);
    s.recursion.as_mut().unwrap().root_hints[0].addresses = vec!["127.0.54.250".into()];
    let rt = ResolutionRuntime::build(&s, &no_blobs()).unwrap();
    let w = query_wire("www.public.");
    let a = resolve_miss(
        &rt,
        &state,
        &NoUpstream(Cell::new(0)),
        &q("www.public.", &w, false),
    )
    .await;
    assert_eq!(
        Message::from_vec(&a.wire).unwrap().metadata.response_code,
        ResponseCode::ServFail
    );
    assert_eq!(a.ede.unwrap().code, 22);
}

/// A forwarder over an in-memory signed hierarchy (`.` and `example.`), signatures valid now.
struct SignedForwarder {
    sets: HashMap<(Name, RecordType), Vec<Record>>,
    calls: Cell<u32>,
}

impl SignedForwarder {
    fn new(root: &TestKey, example: &TestKey) -> Self {
        let now = crate::clock::unix_now() as u32;
        let signed = |k: &TestKey, set: Vec<Record>| {
            let sig = k.sign(&set, now - 3600, now + 86_400);
            let mut v = set;
            v.push(sig);
            v
        };
        let a = |name: &str, ip: [u8; 4]| {
            Record::from_rdata(
                Name::from_ascii(name).unwrap(),
                300,
                RData::A(A(Ipv4Addr::from(ip))),
            )
        };
        let n = |s: &str| Name::from_ascii(s).unwrap();
        let mut sets = HashMap::new();
        sets.insert(
            (Name::root(), RecordType::DNSKEY),
            signed(root, vec![root.dnskey_record()]),
        );
        sets.insert(
            (n("example."), RecordType::DS),
            signed(
                root,
                vec![Record::from_rdata(
                    n("example."),
                    300,
                    RData::DNSSEC(DNSSECRData::DS(example.ds())),
                )],
            ),
        );
        sets.insert(
            (n("example."), RecordType::DNSKEY),
            signed(example, vec![example.dnskey_record()]),
        );
        sets.insert(
            (n("www.example."), RecordType::A),
            signed(example, vec![a("www.example.", [192, 0, 2, 10])]),
        );
        let mut broken = signed(example, vec![a("bad.example.", [192, 0, 2, 11])]);
        broken[0].data = RData::A(A(Ipv4Addr::new(6, 6, 6, 6)));
        sets.insert((n("bad.example."), RecordType::A), broken);
        SignedForwarder {
            sets,
            calls: Cell::new(0),
        }
    }

    fn answer(&self, query: &[u8]) -> Result<Bytes, String> {
        let q = Message::from_vec(query).map_err(|e| e.to_string())?;
        let question = &q.queries[0];
        let key = (question.name().to_lowercase(), question.query_type());
        let records = self.sets.get(&key).ok_or("unreachable")?;
        let mut reply = Message::new(q.metadata.id, MessageType::Response, q.metadata.op_code);
        reply.metadata.recursion_desired = q.metadata.recursion_desired;
        reply.metadata.recursion_available = true;
        reply.queries = q.queries.clone();
        reply.answers = records.clone();
        reply.to_vec().map(Bytes::from).map_err(|e| e.to_string())
    }
}

impl ForwardUpstream for SignedForwarder {
    fn forward<'a>(&'a self, query: &'a [u8]) -> LocalBoxFuture<'a, Result<Bytes, String>> {
        self.calls.set(self.calls.get() + 1);
        let r = self.answer(query);
        Box::pin(async move { r })
    }
}

fn ds_text(k: &TestKey) -> String {
    let ds = k.ds();
    format!(
        "{} {} {} {}",
        ds.key_tag(),
        u8::from(ds.algorithm()),
        u8::from(ds.digest_type()),
        hex::encode_upper(ds.digest())
    )
}

#[tokio::test(flavor = "current_thread")]
async fn forward_mode_validates_when_enabled() {
    let root = TestKey::generate(".", true);
    let example = TestKey::generate("example.", true);
    let up = SignedForwarder::new(&root, &example);
    let anchors = vec![proto::TrustAnchor {
        zone: ".".into(),
        ds: ds_text(&root),
    }];
    let mut s = snapshot(proto::ResolutionMode::Forward, 53);
    s.dnssec = Some(proto::DnssecConfig {
        validation: true,
        trust_anchors: anchors.clone(),
        negative_trust_anchors: vec![],
        rfc5011: false,
    });
    s.dnssec_validate_forwarded = true;
    let rt = ResolutionRuntime::build(&s, &no_blobs()).unwrap();
    let state = RecursorState::new(None);
    state
        .anchors
        .merge_config(&anchors, false, crate::clock::unix_now())
        .unwrap();

    let w = query_wire("www.example.");
    let a = resolve_miss(&rt, &state, &up, &q("www.example.", &w, true)).await;
    let m = Message::from_vec(&a.wire).unwrap();
    assert_eq!(a.security, SecurityTag::Secure, "{:?}", a.ede);
    assert!(m.metadata.authentic_data, "AD for a DO client");
    assert_eq!(m.answers[0].data, RData::A(A(Ipv4Addr::new(192, 0, 2, 10))));
    assert!(a.cacheable && !a.failed);
    assert_eq!(a.route, RouteTaken::Forward);
    assert!(
        up.calls.get() >= 4,
        "answer, DNSKEY ., DS and DNSKEY example. through the forwarder"
    );

    let w = query_wire("bad.example.");
    let b = resolve_miss(&rt, &state, &up, &q("bad.example.", &w, true)).await;
    assert_eq!(
        Message::from_vec(&b.wire).unwrap().metadata.response_code,
        ResponseCode::ServFail
    );
    assert_eq!(b.security, SecurityTag::Bogus);
    assert_eq!(b.ede.as_ref().map(|e| e.code), Some(6));
    assert!(
        !b.failed && !b.cacheable,
        "bogus is answered, never stale-served"
    );

    s.dnssec_validate_forwarded = false;
    let rt = ResolutionRuntime::build(&s, &no_blobs()).unwrap();
    let a = resolve_miss(&rt, &state, &up, &q("bad.example.", &w, true)).await;
    assert_eq!(
        a.wire,
        up.answer(&w).unwrap(),
        "the upstream's bytes unchanged"
    );
    assert_eq!(a.security, SecurityTag::None);
}

fn publish_rpz(state: &RecursorState, text: &str) {
    let origin = Name::from_ascii("rpz.test.").unwrap();
    let parsed = parse_rpz_text(&origin, text).unwrap();
    state
        .rpz
        .publish(RpzSet::new(vec![Arc::new(RpzZoneIndex::build(
            "z", &parsed, 0,
        ))]));
}

#[tokio::test(flavor = "current_thread")]
async fn rpz_response_ip_trigger_and_local_data_cname_chase() {
    let root = TestKey::generate(".", true);
    let example = TestKey::generate("example.", true);
    let up = SignedForwarder::new(&root, &example);
    let rt = ResolutionRuntime::build(&snapshot(proto::ResolutionMode::Forward, 53), &no_blobs())
        .unwrap();
    let state = RecursorState::new(None);
    let w = query_wire("www.example.");
    let plain = resolve_miss(&rt, &state, &up, &q("www.example.", &w, false)).await;
    assert!(plain.cacheable && plain.rpz_action == 0, "no RPZ zone yet");

    publish_rpz(
        &state,
        "$TTL 60\n@ SOA ns h 1 60 60 60 60\n32.10.2.0.192.rpz-ip CNAME .\nalias.example CNAME www.example.\n",
    );
    let a = resolve_miss(&rt, &state, &up, &q("www.example.", &w, false)).await;
    let m = Message::from_vec(&a.wire).unwrap();
    assert_eq!(m.metadata.response_code, ResponseCode::NXDomain);
    assert_eq!(a.ede.as_ref().map(|e| e.code), Some(15));
    assert_eq!(a.rpz_action, RpzAction::Nxdomain.log_code());
    assert!(!a.cacheable);

    let set = state.rpz.set.load_full();
    let action = match set.check_query(&wire("alias.example."), "127.0.0.1".parse().unwrap()) {
        super::rpz::index::QueryPhase::Hit { action, .. } => action.clone(),
        other => panic!("{other:?}"),
    };
    publish_rpz(
        &state,
        "$TTL 60\n@ SOA ns h 1 60 60 60 60\nalias.example CNAME www.example.\n",
    );
    let w = query_wire("alias.example.");
    let mut mq = q("alias.example.", &w, false);
    mq.rpz = RpzPending::Apply { zone: 0, action };
    let c = resolve_miss(&rt, &state, &up, &mq).await;
    let m = Message::from_vec(&c.wire).unwrap();
    assert_eq!(m.answers.len(), 2, "CNAME then the target's A: {m:?}");
    assert_eq!(m.answers[0].record_type(), RecordType::CNAME);
    assert_eq!(m.answers[1].data, RData::A(A(Ipv4Addr::new(192, 0, 2, 10))));
    assert_eq!(c.ede.as_ref().map(|e| e.code), Some(4));
    assert!(!c.cacheable);
}
