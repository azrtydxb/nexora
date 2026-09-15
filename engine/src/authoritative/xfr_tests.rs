use super::set::AuthSet;
use super::xfr::{MAX_XFR_MESSAGE, Plan, messages, plan_for_tests};
use super::zone::{DeltaRecords, Zone};
use super::zone_tests::{DELTA, FULL};
use super::{msg::Question, nzf};
use hickory_proto::op::{Message, MessageType, OpCode, Query, ResponseCode};
use hickory_proto::rr::rdata::SOA;
use hickory_proto::rr::{DNSClass, Name, RData, Record, RecordType};
use std::sync::Arc;

const BIG: &[u8] = include_bytes!(concat!(
    env!("CARGO_MANIFEST_DIR"),
    "/../testdata/nzf/big-full.nzf"
));

fn zone_after_delta() -> Arc<Zone> {
    let base = Zone::from_image(&nzf::parse(FULL).unwrap()).unwrap();
    let parsed = nzf::parse(DELTA).unwrap();
    let mut z = base.apply(&parsed).unwrap();
    z.deltas = vec![Arc::new(DeltaRecords::from_parsed(&parsed))];
    Arc::new(z)
}

fn query(name: &str, qtype: RecordType, client_serial: Option<u32>) -> Vec<u8> {
    let mut m = Message::new(0x5151, MessageType::Query, OpCode::Query);
    m.add_query(Query::query(Name::from_ascii(name).unwrap(), qtype));
    if let Some(s) = client_serial {
        let soa = SOA::new(
            Name::from_ascii("ns1.example.test.").unwrap(),
            Name::from_ascii("h.example.test.").unwrap(),
            s,
            0,
            0,
            0,
            0,
        );
        let mut r = Record::from_rdata(Name::from_ascii(name).unwrap(), 0, RData::SOA(soa));
        r.dns_class = DNSClass::IN;
        m.authorities.push(r);
    }
    m.to_vec().unwrap()
}

fn answers(plan: &Plan, raw: &[u8]) -> (Vec<RecordType>, usize) {
    let q = Question::parse(raw).unwrap();
    let msgs = messages(plan, raw, &q, None, 0);
    let mut types = Vec::new();
    for m in &msgs {
        assert!(m.len() <= MAX_XFR_MESSAGE, "message of {} octets", m.len());
        let parsed = Message::from_vec(m).unwrap();
        assert_eq!(parsed.metadata.response_code, ResponseCode::NoError);
        assert!(parsed.metadata.authoritative);
        types.extend(parsed.answers.iter().map(|r| r.record_type()));
    }
    (types, msgs.len())
}

#[test]
fn axfr_is_bracketed_by_soa() {
    let z = zone_after_delta();
    let raw = query("example.test.", RecordType::AXFR, None);
    let (types, _) = answers(&Plan::Full(z.clone()), &raw);
    assert_eq!(types.first(), Some(&RecordType::SOA));
    assert_eq!(types.last(), Some(&RecordType::SOA));
    assert_eq!(types.iter().filter(|t| **t == RecordType::SOA).count(), 2);
    assert_eq!(types.len(), z.records_sorted().len() + 1);
}

#[test]
fn ixfr_plans_follow_rfc1995() {
    let set = AuthSet::from_zones(vec![zone_after_delta()]).unwrap();
    let known = query("example.test.", RecordType::IXFR, Some(2026091301));
    assert!(matches!(
        plan_for_tests(&set, &known),
        Plan::Incremental(_, 0)
    ));
    let current = query("example.test.", RecordType::IXFR, Some(2026091302));
    assert!(matches!(plan_for_tests(&set, &current), Plan::UpToDate(_)));
    let unknown = query("example.test.", RecordType::IXFR, Some(2026090000));
    assert!(
        matches!(plan_for_tests(&set, &unknown), Plan::Full(_)),
        "history missing: AXFR-style fallback"
    );
}

#[test]
fn incremental_ixfr_sequence() {
    let set = AuthSet::from_zones(vec![zone_after_delta()]).unwrap();
    let raw = query("example.test.", RecordType::IXFR, Some(2026091301));
    let plan = plan_for_tests(&set, &raw);
    let (types, _) = answers(&plan, &raw);
    use RecordType::{A, SOA as S};
    assert_eq!(
        types,
        vec![S, S, A, S, A, S],
        "current SOA, old SOA, deletions, new SOA, additions, current SOA"
    );
}

#[test]
fn large_zone_is_split_into_multiple_messages() {
    let z = Arc::new(Zone::from_image(&nzf::parse(BIG).unwrap()).unwrap());
    let raw = query("big.test.", RecordType::AXFR, None);
    let (types, count) = answers(&Plan::Full(z), &raw);
    assert!(count > 1, "2002 records must not fit one 16 KiB message");
    assert_eq!(types.len(), 2003);
}

mod authorization {
    use super::*;
    use crate::authoritative::name::from_ascii;
    use crate::authoritative::xfr::authorize_and_plan;
    use crate::runtime::Runtime;
    use crate::tsig::{TsigVerifier, sign_request};
    use crate::tsig_tests::test_ring;
    use std::net::IpAddr;

    const NOW: u64 = 1757750400;

    fn runtime() -> Runtime {
        let mut z = (*zone_after_delta()).clone();
        z.transfer_allow = vec!["127.0.0.1/32".parse().unwrap()];
        z.transfer_key = Some(from_ascii("xfr-key.").unwrap().into());
        let mut rt = Runtime::initial();
        rt.auth = Arc::new(AuthSet::from_zones(vec![Arc::new(z)]).unwrap());
        rt
    }

    fn signed(key: &str, raw: &[u8]) -> Vec<u8> {
        let ring = test_ring();
        let key = ring.get(&from_ascii(key).unwrap()).unwrap();
        let mut v = raw.to_vec();
        sign_request(&mut v, &key, NOW);
        v
    }

    fn plan(raw: &[u8], client: &str) -> Plan {
        let q = Question::parse(raw).unwrap();
        let client: IpAddr = client.parse().unwrap();
        authorize_and_plan(&runtime(), &test_ring(), raw, &q, client, true, NOW).0
    }

    #[test]
    fn acl_then_tsig_decide_before_the_plan() {
        let axfr = query("example.test.", RecordType::AXFR, None);
        let good = signed("xfr-key.", &axfr);
        assert!(
            matches!(plan(&good, "127.0.0.1"), Plan::Full(_)),
            "signed from an allowed address"
        );
        assert!(
            matches!(plan(&good, "192.0.2.1"), Plan::Refused(5)),
            "outside the ACL"
        );
        assert!(
            matches!(plan(&axfr, "127.0.0.1"), Plan::Refused(5)),
            "unsigned"
        );
        let other = signed("sha512-key.", &axfr);
        assert!(
            matches!(plan(&other, "127.0.0.1"), Plan::Refused(5)),
            "another key"
        );
        let mut tampered = good.clone();
        tampered[2] ^= 0x01;
        assert!(
            matches!(plan(&tampered, "127.0.0.1"), Plan::TsigError(_)),
            "bad MAC"
        );
        let sub = signed(
            "xfr-key.",
            &query("www.example.test.", RecordType::AXFR, None),
        );
        assert!(
            matches!(plan(&sub, "127.0.0.1"), Plan::Refused(9)),
            "not a zone apex"
        );
    }

    #[test]
    fn signed_transfer_stream_verifies_message_by_message() {
        let axfr = query("big.test.", RecordType::AXFR, None);
        let big = Arc::new(Zone::from_image(&nzf::parse(BIG).unwrap()).unwrap());
        let ring = test_ring();
        let key = ring.get(&from_ascii("xfr-key.").unwrap()).unwrap();
        let mut req = axfr.clone();
        let mac = sign_request(&mut req, &key, NOW);
        let q = Question::parse(&req).unwrap();
        let msgs = messages(
            &Plan::Full(big),
            &req,
            &q,
            Some((key.clone(), mac.clone())),
            NOW,
        );
        assert!(msgs.len() > 1);
        let mut verifier = TsigVerifier::new((*key).clone(), mac);
        for m in &msgs {
            verifier
                .verify(m, NOW)
                .expect("every message verifies in order");
        }
        verifier.finish().unwrap();
    }

    #[tokio::test(flavor = "current_thread")]
    async fn signed_queries_and_transfers_through_the_pipeline() {
        use crate::edns::Transport;
        use crate::proto;
        use crate::server::{FastOutcome, Shared, WorkerCtx, handle_packet};
        use std::rc::Rc;

        let shared = Shared::new(1);
        shared.runtime.store(Arc::new(runtime()));
        shared.auth.keyring.apply(proto::KeyMaterial {
            tsig_keys: vec![proto::TsigSecret {
                name: "xfr-key.".into(),
                algorithm: proto::TsigAlgorithm::HmacSha256 as i32,
                secret: (0u8..32).collect(),
            }],
        });
        let ctx = Rc::new(WorkerCtx::new(0, shared.clone()));
        let rt = shared.runtime.load_full();
        let key = shared
            .auth
            .keyring
            .get(&from_ascii("xfr-key.").unwrap())
            .unwrap();
        let client = "127.0.0.1:5353".parse().unwrap();
        let now = crate::clock::unix_now() as u64;

        // A signed SOA query over UDP is answered and signed.
        let mut soa = query("example.test.", RecordType::SOA, None);
        let mac = sign_request(&mut soa, &key, now);
        let mut out = vec![0u8; 1232];
        let FastOutcome::Reply(n) =
            handle_packet(&ctx, &rt, &soa, client, Transport::Udp, &mut out)
        else {
            panic!("signed SOA query not answered inline")
        };
        let mut v = TsigVerifier::new((*key).clone(), mac);
        v.verify(&out[..n], now).expect("signed reply");
        let m = Message::from_vec(&out[..n]).unwrap();
        assert!(m.metadata.authoritative);
        assert_eq!(m.answers[0].record_type(), RecordType::SOA);

        // A signed AXFR over TCP goes to the slow path and streams signed messages.
        let mut axfr = query("example.test.", RecordType::AXFR, None);
        let mac = sign_request(&mut axfr, &key, now);
        let mut out = vec![0u8; 65535];
        let FastOutcome::Slow(job) =
            handle_packet(&ctx, &rt, &axfr, client, Transport::Tcp, &mut out)
        else {
            panic!("AXFR over TCP not handed to the slow path")
        };
        let msgs = crate::authoritative::dispatch::run_slow(ctx.clone(), rt.clone(), job).await;
        let mut v = TsigVerifier::new((*key).clone(), mac);
        for m in &msgs {
            v.verify(m, now).expect("signed transfer message");
        }
        v.finish().unwrap();
        assert_eq!(
            shared.metrics.auth.transfers[0][0].load(std::sync::atomic::Ordering::Relaxed),
            1
        );

        // The same AXFR without TSIG is refused.
        let plain = query("example.test.", RecordType::AXFR, None);
        let FastOutcome::Slow(job) =
            handle_packet(&ctx, &rt, &plain, client, Transport::Tcp, &mut out)
        else {
            panic!("unsigned AXFR over TCP not handed to the slow path")
        };
        let msgs = crate::authoritative::dispatch::run_slow(ctx.clone(), rt.clone(), job).await;
        assert_eq!(msgs.len(), 1);
        assert_eq!(msgs[0][3] & 0x0f, 5, "REFUSED");
    }

    /// The blocking pool has one thread, taken by a job only the worker's other task releases: the
    /// transfer completes only if the worker ran that task while the transfer was pending.
    #[test]
    fn transfer_is_built_off_the_worker() {
        use crate::edns::Transport;
        use crate::server::{FastOutcome, Shared, WorkerCtx, handle_packet};
        use std::cell::Cell;
        use std::rc::Rc;

        let shared = Shared::new(1);
        let mut z = Zone::from_image(&nzf::parse(BIG).unwrap()).unwrap();
        z.transfer_allow = vec!["127.0.0.1/32".parse().unwrap()];
        let mut rt = Runtime::initial();
        rt.auth = Arc::new(AuthSet::from_zones(vec![Arc::new(z)]).unwrap());
        shared.runtime.store(Arc::new(rt));
        let ctx = Rc::new(WorkerCtx::new(0, shared.clone()));
        let rt = shared.runtime.load_full();
        let client = "127.0.0.1:5353".parse().unwrap();
        let axfr = query("big.test.", RecordType::AXFR, None);
        let worker = tokio::runtime::Builder::new_current_thread()
            .max_blocking_threads(1)
            .build()
            .unwrap();
        let local = tokio::task::LocalSet::new();
        worker.block_on(local.run_until(async {
            for round in 0..20 {
                let mut out = vec![0u8; 65535];
                let FastOutcome::Slow(job) =
                    handle_packet(&ctx, &rt, &axfr, client, Transport::Tcp, &mut out)
                else {
                    panic!("AXFR over TCP not handed to the slow path")
                };
                let (release, released) = std::sync::mpsc::channel::<()>();
                let blocker = tokio::task::spawn_blocking(move || {
                    let _ = released.recv();
                });
                let ran = Rc::new(Cell::new(false));
                let flag = ran.clone();
                let other = tokio::task::spawn_local(async move {
                    flag.set(true);
                    let _ = release.send(());
                });
                let msgs =
                    crate::authoritative::dispatch::run_slow(ctx.clone(), rt.clone(), job).await;
                assert!(
                    msgs.len() > 1,
                    "the 2,002-record zone spans several messages"
                );
                assert!(
                    ran.get(),
                    "round {round}: nothing else ran on the worker while the transfer was built"
                );
                other.await.unwrap();
                blocker.await.unwrap();
            }
        }));
    }
}
