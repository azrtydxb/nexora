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
             twons 300 IN NS ns.zzz.\ntwons 300 IN NS ns.provider.example.test.\n\
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
async fn minimisation_sends_the_full_name_after_one_empty_non_terminal() {
    // catches: walking every hidden label one query at a time (5 round trips for
    // deep.a.b.c.d.e.test at the test. servers), and dropping minimisation altogether
    let net = FakeNet::start(hierarchy("", vec![])).await;
    let r = Recursor::new(Arc::new(RecursorMetrics::default()));
    let res = resolve(&net, &r, "deep.a.b.c.d.e.test.", RecordType::A)
        .await
        .unwrap();
    assert_eq!(res.answers.last().unwrap().data, a([192, 0, 2, 77]));
    let names: Vec<Name> = net
        .queries
        .lock()
        .unwrap()
        .iter()
        .filter(|(ip, _, _)| *ip == Ipv4Addr::new(127, 0, 54, 2))
        .map(|(_, n, _)| n.to_lowercase())
        .collect();
    assert_eq!(
        names,
        vec![
            Name::from_ascii("e.test.").unwrap(),
            Name::from_ascii("deep.a.b.c.d.e.test.").unwrap()
        ]
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

/// Root delegating `race.` to a spoofer and an honest server that share the zone.
fn race_net() -> Vec<FakeZone> {
    vec![
        FakeZone {
            origin: ".",
            ip: Ipv4Addr::new(127, 0, 54, 1),
            behaviour: Behaviour::Normal,
            text: format!(
                "{SOA}race. 300 IN NS ns1.race.\nrace. 300 IN NS ns2.race.\n\
                 ns1.race. 300 IN A 127.0.54.10\nns2.race. 300 IN A 127.0.54.11\n"
            ),
        },
        FakeZone {
            origin: "race.",
            ip: Ipv4Addr::new(127, 0, 54, 10),
            behaviour: Behaviour::Spoof,
            text: String::new(),
        },
        FakeZone {
            origin: "race.",
            ip: Ipv4Addr::new(127, 0, 54, 11),
            behaviour: Behaviour::Normal,
            text: format!(
                "{SOA}@ 300 IN NS ns1\n@ 300 IN NS ns2\nns1 300 IN A 127.0.54.10\n\
                 ns2 300 IN A 127.0.54.11\nwwwwwwwwwwwwwwwwwwww 300 IN A 192.0.2.50\n"
            ),
        },
    ]
}

#[tokio::test(flavor = "current_thread")]
async fn racing_servers_keeps_spoofed_replies_out() {
    // catches: a race accepting the first datagram from either socket (the spoofer answers
    // instantly with a wrong ID and with broken 0x20 case) instead of the first matching reply
    let net = FakeNet::start(race_net()).await;
    let spoofer = Ipv4Addr::new(127, 0, 54, 10);
    let mut raced = 0;
    // the primary is chosen at random: 20 fresh resolvers all picking the honest one is 1 in 2^20
    for _ in 0..20 {
        let metrics = Arc::new(RecursorMetrics::default());
        let r = Recursor::new(metrics.clone());
        let res = resolve(&net, &r, "wwwwwwwwwwwwwwwwwwww.race.", RecordType::A)
            .await
            .unwrap();
        assert_eq!(res.rcode, ResponseCode::NoError);
        assert_eq!(res.answers.len(), 1, "{:?}", res.answers);
        assert_eq!(res.answers[0].data, a([192, 0, 2, 50]));
        let forged = metrics
            .mismatched_id
            .load(std::sync::atomic::Ordering::Relaxed);
        let cased = metrics
            .mismatched_case
            .load(std::sync::atomic::Ordering::Relaxed);
        assert_eq!(forged, cased, "each spoofed query sends one of each");
        raced += usize::from(forged > 0);
    }
    assert!(net.queries_to(spoofer) > 0, "the spoofer was never asked");
    assert!(raced > 0, "no resolution raced the spoofer");
}

#[tokio::test(flavor = "current_thread")]
async fn unmeasured_servers_race_and_the_winner_is_preferred() {
    // catches: racing that never starts the second server, or that keeps racing once the
    // honest server is measured as fast
    let net = FakeNet::start(race_net()).await;
    let r = Recursor::new(Arc::new(RecursorMetrics::default()));
    let res = resolve(&net, &r, "wwwwwwwwwwwwwwwwwwww.race.", RecordType::A)
        .await
        .unwrap();
    assert_eq!(res.answers[0].data, a([192, 0, 2, 50]));
    let honest: std::net::IpAddr = "127.0.54.11".parse().unwrap();
    let spoofer: std::net::IpAddr = "127.0.54.10".parse().unwrap();
    let zone = Name::from_ascii("race.").unwrap();
    let plan = r
        .infra
        .plan(&[honest, spoofer], &zone, 0, &mut rand::rng())
        .unwrap();
    assert_eq!(plan.primary, honest, "the fast honest server ranks first");
    assert!(plan.racers.is_empty(), "a fast known server is not raced");
    let before = net.queries_to(Ipv4Addr::new(127, 0, 54, 10));
    for i in 0..5 {
        let name = format!("nx{i}.race.");
        let res = resolve(&net, &r, &name, RecordType::A).await.unwrap();
        assert_eq!(res.rcode, ResponseCode::NXDomain);
    }
    assert_eq!(
        net.queries_to(Ipv4Addr::new(127, 0, 54, 10)),
        before,
        "no further queries to the slower server"
    );
}

#[tokio::test(flavor = "current_thread")]
async fn concurrent_lookups_on_one_budget_share_a_glueless_nameserver() {
    // catches: a dependency-cycle stack shared by the whole budget, where the second of two
    // concurrent lookups needing ns.provider.example.test sees the first one's lookup as a
    // cycle and fails with no reachable authority
    let net = FakeNet::start(hierarchy("", vec![])).await;
    let r = Recursor::new(Arc::new(RecursorMetrics::default()));
    let p = net.params(Ipv4Addr::new(127, 0, 54, 1));
    let b = WorkBudget::new(p.max_upstream_queries, p.max_delegation_depth);
    let www = Name::from_ascii("www.glueless.test.").unwrap();
    let apex = Name::from_ascii("glueless.test.").unwrap();
    let results = crate::recursor::join_all(vec![
        Box::pin(r.fetch(&www, RecordType::A, &p, &b)),
        Box::pin(r.fetch(&apex, RecordType::NS, &p, &b)),
    ])
    .await;
    for res in results {
        let res = res.unwrap();
        assert_eq!(res.rcode, ResponseCode::NoError);
        assert!(!res.answers.is_empty());
    }
}

#[tokio::test(flavor = "current_thread")]
async fn ipv6_authorities_are_skipped_without_an_ipv6_route() {
    // catches: sending to IPv6 authorities from a host without an IPv6 route (every such query
    // fails with network unreachable), and not noticing when detection said IPv6 works but the
    // network then reports it unreachable
    let net = FakeNet::start(hierarchy("", vec![])).await;
    let mut p = net.params(Ipv4Addr::new(127, 0, 54, 1));
    let doc_v6: std::net::IpAddr = "2001:db8::53".parse().unwrap();
    p.root_hints = Arc::new(super::roothints::RootHints {
        servers: vec![(
            Name::from_ascii("root.fake.").unwrap(),
            vec![doc_v6, "127.0.54.1".parse().unwrap()],
        )],
    });
    let name = Name::from_ascii("www.example.test.").unwrap();
    let r = Recursor::new(Arc::new(RecursorMetrics::default()));
    assert!(
        !r.ipv6.load(std::sync::atomic::Ordering::Relaxed),
        "off until detected"
    );
    let b = WorkBudget::new(p.max_upstream_queries, p.max_delegation_depth);
    let res = r.resolve(&name, RecordType::A, &p, &b).await.unwrap();
    assert_eq!(res.answers.last().unwrap().data, a([192, 0, 2, 10]));
    assert_eq!(
        r.infra.rto(doc_v6),
        std::time::Duration::from_millis(376),
        "never asked"
    );

    let r = Recursor::new(Arc::new(RecursorMetrics::default()));
    r.detect_ipv6();
    let routable = r.ipv6.load(std::sync::atomic::Ordering::Relaxed);
    r.ipv6.store(true, std::sync::atomic::Ordering::Relaxed);
    let mut v6_only = net.params(Ipv4Addr::new(127, 0, 54, 1));
    v6_only.root_hints = Arc::new(super::roothints::RootHints {
        servers: vec![(Name::from_ascii("root.fake.").unwrap(), vec![doc_v6])],
    });
    let b = WorkBudget::new(p.max_upstream_queries, p.max_delegation_depth);
    assert!(r.resolve(&name, RecordType::A, &v6_only, &b).await.is_err());
    if !routable {
        assert!(
            !r.ipv6.load(std::sync::atomic::Ordering::Relaxed),
            "network unreachable on an IPv6 authority turns IPv6 off"
        );
    }
}

#[tokio::test(flavor = "current_thread")]
async fn ds_from_a_referral_answers_a_later_ds_lookup() {
    // catches: asking the parent again for a DS RRset its referral already carried (one extra
    // round trip per signed delegation during validation)
    let net = FakeNet::start(vec![
        FakeZone {
            origin: ".",
            ip: Ipv4Addr::new(127, 0, 54, 1),
            behaviour: Behaviour::Normal,
            text: format!(
                "{SOA}signed. 300 IN NS ns.signed.\nns.signed. 300 IN A 127.0.54.6\n\
                 signed. 300 IN DS 12345 8 2 E06D44B80B8F1D39A95C0B0D7C65D08458E880409BBC683457104237C7F8EC8D\n"
            ),
        },
        FakeZone {
            origin: "signed.",
            ip: Ipv4Addr::new(127, 0, 54, 6),
            behaviour: Behaviour::Normal,
            text: format!("{SOA}@ 300 IN NS ns\nns 300 IN A 127.0.54.6\nwww 300 IN A 192.0.2.60\n"),
        },
    ])
    .await;
    let r = Recursor::new(Arc::new(RecursorMetrics::default()));
    let res = resolve(&net, &r, "www.signed.", RecordType::A)
        .await
        .unwrap();
    assert_eq!(res.answers.last().unwrap().data, a([192, 0, 2, 60]));
    let before = net.queries.lock().unwrap().len();
    let p = net.params(Ipv4Addr::new(127, 0, 54, 1));
    let b = WorkBudget::new(p.max_upstream_queries, p.max_delegation_depth);
    let ds = r
        .fetch(
            &Name::from_ascii("signed.").unwrap(),
            RecordType::DS,
            &p,
            &b,
        )
        .await
        .unwrap();
    assert_eq!(ds.answers.len(), 1, "{:?}", ds.answers);
    assert_eq!(
        net.queries.lock().unwrap().len(),
        before,
        "answered from the referral"
    );
}

#[tokio::test(flavor = "current_thread")]
async fn glueless_nameserver_under_a_known_delegation_is_resolved_first() {
    // catches: resolving nameserver names in listed order, walking a whole unknown hierarchy
    // (ns.zzz. from the root) while a name under an already cached zone needs one query
    let twons = FakeZone {
        origin: "twons.test.",
        ip: Ipv4Addr::new(127, 0, 54, 4),
        behaviour: Behaviour::Normal,
        text: format!(
            "{SOA}@ 300 IN NS ns.zzz.\n@ 300 IN NS ns.provider.example.test.\n\
             www 300 IN A 192.0.2.80\n"
        ),
    };
    let net = FakeNet::start(hierarchy("", vec![twons])).await;
    let r = Recursor::new(Arc::new(RecursorMetrics::default()));
    resolve(&net, &r, "www.example.test.", RecordType::A)
        .await
        .unwrap();
    let before = net.queries.lock().unwrap().len();
    let res = resolve(&net, &r, "www.twons.test.", RecordType::A)
        .await
        .unwrap();
    assert_eq!(res.answers.last().unwrap().data, a([192, 0, 2, 80]));
    let root_queries = net.queries.lock().unwrap()[before..]
        .iter()
        .filter(|(ip, _, _)| *ip == Ipv4Addr::new(127, 0, 54, 1))
        .count();
    assert_eq!(root_queries, 0, "ns.zzz. was looked up from the root");
}
