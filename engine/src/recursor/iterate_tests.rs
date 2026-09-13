use super::budget::{Limit, WorkBudget};
use super::iterate::{RecursionError, Recursor};
use super::metrics::RecursorMetrics;
use super::testnet::{Behaviour, FakeNet, FakeZone};
use hickory_proto::op::ResponseCode;
use hickory_proto::rr::{Name, RData, RecordType, rdata::A};
use std::net::Ipv4Addr;
use std::sync::Arc;

const SOA: &str = "@ 300 IN SOA ns hostmaster 1 3600 600 86400 300\n";

fn hierarchy(extra_example: &str, zones_extra: Vec<FakeZone>) -> Vec<FakeZone> {
    let mut v = vec![
        FakeZone {
            origin: ".",
            ip: Ipv4Addr::new(127, 0, 54, 1),
            behaviour: Behaviour::Normal,
            text: format!(
                "{SOA}@ 300 IN NS root.fake.\nroot.fake. 300 IN A 127.0.54.1\n\
             test. 300 IN NS ns1.test.\ntest. 300 IN NS ns2.test.\nns1.test. 300 IN A 127.0.54.9\nns2.test. 300 IN A 127.0.54.2\n"
            ),
        },
        FakeZone {
            origin: "test.",
            ip: Ipv4Addr::new(127, 0, 54, 2),
            behaviour: Behaviour::Normal,
            text: format!(
                "{SOA}@ 300 IN NS ns2\nns2 300 IN A 127.0.54.2\n\
             example 300 IN NS ns.example\nns.example 300 IN A 127.0.54.3\n\
             glueless 300 IN NS ns.provider.example.test.\n\
             poison 300 IN NS ns.poison\nns.poison 300 IN A 127.0.54.5\n\
             deep.a.b.c.d.e 300 IN A 192.0.2.77\n"
            ),
        },
        FakeZone {
            origin: "example.test.",
            ip: Ipv4Addr::new(127, 0, 54, 3),
            behaviour: Behaviour::Normal,
            text: format!(
                "{SOA}@ 300 IN NS ns\nns 300 IN A 127.0.54.3\nwww 300 IN A 192.0.2.10\nalias 300 IN CNAME www\n\
             ext 300 IN CNAME www.glueless.test.\nloop1 300 IN CNAME loop2\nloop2 300 IN CNAME loop1\n\
             old 300 IN DNAME example.test.\nns.provider 300 IN A 127.0.54.4\n{extra_example}"
            ),
        },
        FakeZone {
            origin: "glueless.test.",
            ip: Ipv4Addr::new(127, 0, 54, 4),
            behaviour: Behaviour::Normal,
            text: format!("{SOA}@ 300 IN NS ns.provider.example.test.\nwww 300 IN A 192.0.2.20\n"),
        },
        FakeZone {
            origin: "poison.test.",
            ip: Ipv4Addr::new(127, 0, 54, 5),
            behaviour: Behaviour::Normal,
            text: format!(
                "{SOA}@ 300 IN NS ns\nns 300 IN A 127.0.54.5\n\
             sub 300 IN NS ns.example.test.\nns.example.test. 300 IN A 127.0.54.66\n\
             www 300 IN A 192.0.2.30\n"
            ),
        },
        FakeZone {
            origin: "test.",
            ip: Ipv4Addr::new(127, 0, 54, 9),
            behaviour: Behaviour::Silent,
            text: String::new(),
        },
    ];
    v.extend(zones_extra);
    v
}

async fn resolve(
    net: &FakeNet,
    r: &Recursor,
    name: &str,
    t: RecordType,
) -> Result<super::iterate::Resolution, RecursionError> {
    let p = net.params(Ipv4Addr::new(127, 0, 54, 1));
    let b = WorkBudget::new(p.max_upstream_queries, p.max_delegation_depth);
    r.resolve(&Name::from_ascii(name).unwrap(), t, &p, &b).await
}

fn a(ip: [u8; 4]) -> RData {
    RData::A(A(Ipv4Addr::from(ip)))
}

#[tokio::test(flavor = "current_thread")]
async fn follows_delegations_from_root_hints() {
    let net = FakeNet::start(hierarchy("", vec![])).await;
    let r = Recursor::new(Arc::new(RecursorMetrics::default()));
    let res = resolve(&net, &r, "www.example.test.", RecordType::A)
        .await
        .unwrap();
    assert_eq!(res.rcode, ResponseCode::NoError);
    assert_eq!(res.answers.last().unwrap().data, a([192, 0, 2, 10]));
    assert_eq!(res.zone, Name::from_ascii("example.test.").unwrap());
    assert_eq!(
        res.ns_addrs,
        vec!["127.0.54.3".parse::<std::net::IpAddr>().unwrap()]
    );
}

#[tokio::test(flavor = "current_thread")]
async fn silent_server_is_skipped_and_backed_off() {
    let net = FakeNet::start(hierarchy("", vec![])).await;
    let r = Recursor::new(Arc::new(RecursorMetrics::default()));
    for i in 0..8 {
        let name = format!("nx{i}.test.");
        let res = resolve(&net, &r, &name, RecordType::A).await.unwrap();
        assert_eq!(res.rcode, ResponseCode::NXDomain, "{name}");
    }
    assert!(
        net.queries_to(Ipv4Addr::new(127, 0, 54, 9)) <= 3,
        "silent server backed off after 3 timeouts"
    );
}

#[tokio::test(flavor = "current_thread")]
async fn glueless_delegation_resolves_ns_address() {
    let net = FakeNet::start(hierarchy("", vec![])).await;
    let r = Recursor::new(Arc::new(RecursorMetrics::default()));
    let res = resolve(&net, &r, "www.glueless.test.", RecordType::A)
        .await
        .unwrap();
    assert_eq!(res.answers.last().unwrap().data, a([192, 0, 2, 20]));
}

#[tokio::test(flavor = "current_thread")]
async fn out_of_bailiwick_glue_is_ignored() {
    let net = FakeNet::start(hierarchy("", vec![])).await;
    let r = Recursor::new(Arc::new(RecursorMetrics::default()));
    // poison.test's referral for sub.poison.test carries glue ns.example.test A 127.0.54.66 (outside poison.test)
    let _ = resolve(&net, &r, "www.sub.poison.test.", RecordType::A).await;
    assert!(
        net.queries_to(Ipv4Addr::new(127, 0, 54, 5)) > 0,
        "the poisoning referral was received"
    );
    let poisoned = |r: &Recursor| {
        r.rrcache
            .get_glue(
                &Name::from_ascii("ns.example.test.").unwrap(),
                RecordType::A,
                0,
            )
            .is_some_and(|s| s.records.iter().any(|rec| rec.data == a([127, 0, 54, 66])))
    };
    assert!(!poisoned(&r), "out-of-bailiwick glue was cached");
    let res = resolve(&net, &r, "ns.example.test.", RecordType::A)
        .await
        .unwrap();
    assert_eq!(res.answers.last().unwrap().data, a([127, 0, 54, 3]));
    assert!(
        r.rrcache
            .get_glue(
                &Name::from_ascii("ns.example.test.").unwrap(),
                RecordType::A,
                0
            )
            .map(|s| s.records.iter().all(|rec| rec.data != a([127, 0, 54, 66])))
            .unwrap_or(true)
    );
}

#[tokio::test(flavor = "current_thread")]
async fn chases_cname_across_zones_and_dname() {
    let net = FakeNet::start(hierarchy("", vec![])).await;
    let r = Recursor::new(Arc::new(RecursorMetrics::default()));
    let res = resolve(&net, &r, "ext.example.test.", RecordType::A)
        .await
        .unwrap();
    assert_eq!(res.answers.len(), 2);
    assert_eq!(res.answers[1].data, a([192, 0, 2, 20]));
    let res = resolve(&net, &r, "www.old.example.test.", RecordType::A)
        .await
        .unwrap();
    assert_eq!(res.answers.last().unwrap().data, a([192, 0, 2, 10]));
}

#[tokio::test(flavor = "current_thread")]
async fn cname_loop_and_depth_limit_fail() {
    let mut chain = String::new();
    for i in 0..17 {
        chain.push_str(&format!("c{i} 300 IN CNAME c{}.example.test.\n", i + 1));
    }
    chain.push_str("c17 300 IN A 192.0.2.99\n");
    let net = FakeNet::start(hierarchy(&chain, vec![])).await;
    let r = Recursor::new(Arc::new(RecursorMetrics::default()));
    assert_eq!(
        resolve(&net, &r, "loop1.example.test.", RecordType::A)
            .await
            .unwrap_err(),
        RecursionError::CnameLoop
    );
    assert_eq!(
        resolve(&net, &r, "c0.example.test.", RecordType::A)
            .await
            .unwrap_err(),
        RecursionError::Limit(Limit::CnameDepth)
    );
    assert!(
        resolve(&net, &r, "c2.example.test.", RecordType::A)
            .await
            .is_ok(),
        "15 hops is within the limit"
    );
}

#[tokio::test(flavor = "current_thread")]
async fn qname_minimisation_hides_full_name_from_root_and_tld() {
    let net = FakeNet::start(hierarchy("", vec![])).await;
    let r = Recursor::new(Arc::new(RecursorMetrics::default()));
    let res = resolve(&net, &r, "deep.a.b.c.d.e.test.", RecordType::A)
        .await
        .unwrap();
    assert_eq!(res.answers.last().unwrap().data, a([192, 0, 2, 77]));
    let q = net.queries.lock().unwrap().clone();
    let root_names: Vec<_> = q
        .iter()
        .filter(|(ip, _, _)| *ip == Ipv4Addr::new(127, 0, 54, 1))
        .map(|(_, n, _)| n.to_lowercase())
        .collect();
    assert!(
        root_names
            .iter()
            .all(|n| n == &Name::from_ascii("test.").unwrap()),
        "root saw {root_names:?}"
    );
}

#[tokio::test(flavor = "current_thread")]
async fn query_budget_stops_amplification() {
    let net = FakeNet::start(hierarchy("", vec![])).await;
    let r = Recursor::new(Arc::new(RecursorMetrics::default()));
    let mut p = net.params(Ipv4Addr::new(127, 0, 54, 1));
    p.max_upstream_queries = 2;
    let b = WorkBudget::new(p.max_upstream_queries, p.max_delegation_depth);
    let err = r
        .resolve(
            &Name::from_ascii("www.glueless.test.").unwrap(),
            RecordType::A,
            &p,
            &b,
        )
        .await
        .unwrap_err();
    assert_eq!(err, RecursionError::Limit(Limit::UpstreamQueries));
    assert!(b.queries_used() <= 3);
}

#[tokio::test(flavor = "current_thread")]
async fn all_servers_refused_is_no_reachable_authority() {
    let net = FakeNet::start(vec![FakeZone {
        origin: ".",
        ip: Ipv4Addr::new(127, 0, 54, 1),
        behaviour: Behaviour::Refused,
        text: SOA.to_string(),
    }])
    .await;
    let r = Recursor::new(Arc::new(RecursorMetrics::default()));
    assert_eq!(
        resolve(&net, &r, "x.test.", RecordType::A)
            .await
            .unwrap_err(),
        RecursionError::NoReachableAuthority
    );
}

#[tokio::test(flavor = "current_thread")]
async fn unrelated_answer_records_are_not_cached() {
    let net = FakeNet::start(vec![
        FakeZone {
            origin: ".",
            ip: Ipv4Addr::new(127, 0, 54, 1),
            behaviour: Behaviour::Normal,
            text: format!("{SOA}stuff. 300 IN NS ns.stuff.\nns.stuff. 300 IN A 127.0.54.6\n"),
        },
        FakeZone {
            origin: "stuff.",
            ip: Ipv4Addr::new(127, 0, 54, 6),
            behaviour: Behaviour::AnswerStuffing,
            text: format!("{SOA}@ 300 IN NS ns\nns 300 IN A 127.0.54.6\nwww 300 IN A 192.0.2.40\n"),
        },
    ])
    .await;
    let r = Recursor::new(Arc::new(RecursorMetrics::default()));
    let res = resolve(&net, &r, "www.stuff.", RecordType::A)
        .await
        .unwrap();
    assert_eq!(res.answers.len(), 1, "{:?}", res.answers);
    assert_eq!(res.answers[0].data, a([192, 0, 2, 40]));
    let victim = Name::from_ascii("victim.stuff.").unwrap();
    assert!(
        r.rrcache.get_glue(&victim, RecordType::A, 0).is_none(),
        "stuffed record cached"
    );
}
