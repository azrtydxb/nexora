use super::budget::{Dependencies, Limit, WorkBudget};
use super::infra::{InfraCache, Plan};
use super::roothints::RootHints;
use super::rrcache::{Credibility, DnssecStatus, RrCache};
use hickory_proto::rr::{
    Name, RData, Record, RecordType,
    rdata::{A, NS},
};
use rand::SeedableRng;
use std::net::{IpAddr, Ipv4Addr};
use std::time::Duration;

fn ip(last: u8) -> IpAddr {
    IpAddr::V4(Ipv4Addr::new(192, 0, 2, last))
}

#[test]
fn rto_follows_rfc6298_smoothing() {
    let c = InfraCache::new(1000);
    assert_eq!(c.rto(ip(1)), Duration::from_millis(376));
    c.record_rtt(ip(1), Duration::from_millis(100)); // srtt 100, rttvar 50 -> rto 300
    assert_eq!(c.rto(ip(1)), Duration::from_millis(300));
    c.record_rtt(ip(1), Duration::from_millis(100)); // rttvar 37.5 -> rto 250
    assert_eq!(c.rto(ip(1)), Duration::from_millis(250));
}

#[test]
fn three_timeouts_back_off_exponentially_and_rto_doubles() {
    let c = InfraCache::new(1000);
    c.record_rtt(ip(2), Duration::from_millis(100));
    c.record_timeout(ip(2), 1000);
    assert_eq!(c.rto(ip(2)), Duration::from_millis(600));
    assert!(!c.is_backed_off(ip(2), 1000));
    c.record_timeout(ip(2), 1000);
    c.record_timeout(ip(2), 1000);
    assert!(c.is_backed_off(ip(2), 1004));
    assert!(!c.is_backed_off(ip(2), 1005));
    c.record_timeout(ip(2), 1005); // 4th consecutive -> 10 s
    assert!(c.is_backed_off(ip(2), 1014));
    assert!(!c.is_backed_off(ip(2), 1015));
    c.record_rtt(ip(2), Duration::from_millis(80));
    assert!(!c.is_backed_off(ip(2), 1005));
}

#[test]
fn lame_marks_are_per_zone_and_expire() {
    let c = InfraCache::new(1000);
    let zone = Name::from_ascii("example.").unwrap();
    c.mark_lame(ip(3), &zone, 100);
    assert!(c.is_lame(ip(3), &zone, 999));
    assert!(!c.is_lame(ip(3), &Name::from_ascii("other.").unwrap(), 101));
    assert!(!c.is_lame(ip(3), &zone, 1000));
}

#[test]
fn select_skips_lame_and_backed_off_but_probes_when_all_are_down() {
    let c = InfraCache::new(1000);
    let zone = Name::from_ascii("example.").unwrap();
    let mut rng = rand::rngs::StdRng::seed_from_u64(1);
    c.mark_lame(ip(4), &zone, 0);
    for _ in 0..3 {
        c.record_timeout(ip(5), 0);
    }
    c.record_rtt(ip(6), Duration::from_millis(30));
    for _ in 0..20 {
        assert_eq!(
            c.select(&[ip(4), ip(5), ip(6)], &zone, 1, &mut rng),
            Some(ip(6))
        );
    }
    for _ in 0..3 {
        c.record_timeout(ip(6), 1);
    }
    assert_eq!(
        c.select(&[ip(4), ip(5), ip(6)], &zone, 2, &mut rng),
        Some(ip(5))
    );
    assert_eq!(c.select(&[ip(4)], &zone, 2, &mut rng), None);
}

#[test]
fn select_picks_the_fastest_measured_server() {
    // catches: a random choice among similar RTOs sending queries to a 60 ms server when a 20 ms
    // one is known
    let c = InfraCache::new(1000);
    let zone = Name::root();
    c.record_rtt(ip(7), Duration::from_millis(20));
    c.record_rtt(ip(8), Duration::from_millis(60));
    for _ in 0..10 {
        c.record_rtt(ip(9), Duration::from_millis(900));
    }
    let mut rng = rand::rngs::StdRng::seed_from_u64(7);
    for _ in 0..200 {
        assert_eq!(
            c.plan(&[ip(7), ip(8), ip(9)], &zone, 0, &mut rng),
            Some(Plan {
                primary: ip(7),
                racers: vec![]
            })
        );
    }
    // a timeout doubles the RTO (60 -> 120 ms), which then ranks the server
    c.record_timeout(ip(7), 0);
    assert_eq!(
        c.select(&[ip(7), ip(8), ip(9)], &zone, 0, &mut rng),
        Some(ip(8))
    );
    // an answer clears the penalty
    c.record_rtt(ip(7), Duration::from_millis(20));
    assert_eq!(
        c.select(&[ip(7), ip(8), ip(9)], &zone, 0, &mut rng),
        Some(ip(7))
    );
}

#[test]
fn plan_races_unmeasured_servers_only_when_the_best_is_slow() {
    // catches: never exploring unmeasured servers (stuck on a 110 ms server when a 9 ms one
    // exists), and racing on every step even when a fast server is known
    let c = InfraCache::new(1000);
    let zone = Name::root();
    let mut rng = rand::rngs::StdRng::seed_from_u64(3);
    c.record_rtt(ip(20), Duration::from_millis(10));
    c.record_rtt(ip(21), Duration::from_millis(110));
    assert_eq!(
        c.plan(&[ip(20), ip(22)], &zone, 0, &mut rng),
        Some(Plan {
            primary: ip(20),
            racers: vec![]
        }),
        "fast known server: no race"
    );
    assert_eq!(
        c.plan(&[ip(21), ip(22)], &zone, 0, &mut rng),
        Some(Plan {
            primary: ip(21),
            racers: vec![ip(22)]
        }),
        "slow known server raced against the unmeasured one"
    );
    // nothing measured: three different unmeasured servers race
    for _ in 0..50 {
        let plan = c
            .plan(&[ip(22), ip(23), ip(24)], &zone, 0, &mut rng)
            .unwrap();
        assert_eq!(plan.racers.len(), 2, "cold: the primary and two racers");
        assert!(!plan.racers.contains(&plan.primary));
        assert_ne!(plan.racers[0], plan.racers[1]);
    }
    // a single candidate never races itself
    assert_eq!(
        c.plan(&[ip(22)], &zone, 0, &mut rng),
        Some(Plan {
            primary: ip(22),
            racers: vec![]
        })
    );
    // an unmeasured server that timed out is not raced while a fresh one exists
    c.record_timeout(ip(23), 0);
    for _ in 0..50 {
        let plan = c
            .plan(&[ip(21), ip(23), ip(24)], &zone, 0, &mut rng)
            .unwrap();
        assert_eq!(plan.racers, vec![ip(24)]);
    }
}

#[test]
fn lost_race_estimates_only_unmeasured_servers() {
    // catches: a loser staying unmeasured (raced again at every step) or a measured server's RTT
    // being overwritten by a lower-bound guess
    let c = InfraCache::new(1000);
    let zone = Name::root();
    let mut rng = rand::rngs::StdRng::seed_from_u64(5);
    c.record_lost(ip(30), Duration::from_millis(40));
    c.record_rtt(ip(31), Duration::from_millis(50));
    assert_eq!(
        c.plan(&[ip(30), ip(31)], &zone, 0, &mut rng),
        Some(Plan {
            primary: ip(31),
            racers: vec![]
        }),
        "the loser now counts as measured at twice its wait (80 ms)"
    );
    assert_eq!(
        c.rto(ip(30)),
        Duration::from_millis(376),
        "a lower-bound estimate never shortens the timeout below the unmeasured one"
    );
    c.record_lost(ip(31), Duration::from_millis(500));
    assert_eq!(
        c.select(&[ip(30), ip(31)], &zone, 0, &mut rng),
        Some(ip(31))
    );
}

#[test]
fn rrcache_credibility_ranking_and_expiry() {
    let c = RrCache::new(1000);
    let n = Name::from_ascii("ns.example.").unwrap();
    let glue = vec![Record::from_rdata(
        n.clone(),
        3600,
        RData::A(A(Ipv4Addr::new(6, 6, 6, 6))),
    )];
    let auth = vec![Record::from_rdata(
        n.clone(),
        60,
        RData::A(A(Ipv4Addr::new(192, 0, 2, 53))),
    )];
    assert!(c.insert(
        auth.clone(),
        vec![],
        Credibility::AnswerAa,
        DnssecStatus::Unchecked,
        100
    ));
    assert!(!c.insert(
        glue,
        vec![],
        Credibility::Glue,
        DnssecStatus::Unchecked,
        100
    ));
    assert_eq!(
        c.get(
            &Name::from_ascii("NS.Example.").unwrap(),
            RecordType::A,
            159
        )
        .unwrap()
        .records,
        auth
    );
    assert!(c.get(&n, RecordType::A, 160).is_none());
    let zone = Name::from_ascii("example.").unwrap();
    let ns = vec![Record::from_rdata(
        zone.clone(),
        0,
        RData::NS(NS(n.clone())),
    )];
    assert!(
        !c.insert(
            ns,
            vec![],
            Credibility::AuthorityAa,
            DnssecStatus::Unchecked,
            100
        ),
        "TTL 0 is never cached"
    );
}

#[test]
fn iana_root_hints_have_13_servers_with_v4_and_v6() {
    let h = RootHints::iana();
    assert_eq!(h.servers.len(), 13);
    assert_eq!(h.addresses(false).len(), 13);
    assert_eq!(h.addresses(true).len(), 26);
    assert!(h.addresses(false).contains(&"198.41.0.4".parse().unwrap()));
    assert!(h.addresses(true).contains(&"2001:7fd::1".parse().unwrap()));
}

#[test]
fn work_budget_limits_queries_depth_and_detects_cycles() {
    let b = WorkBudget::new(3, 2);
    assert_eq!(b.spend_query(), Ok(()));
    assert_eq!(b.spend_query(), Ok(()));
    assert_eq!(b.spend_query(), Ok(()));
    assert_eq!(b.spend_query(), Err(Limit::UpstreamQueries));
    assert_eq!(b.check_depth(2), Ok(()));
    assert_eq!(b.check_depth(3), Err(Limit::DelegationDepth));
    let n = Name::from_ascii("ns.loop.example.").unwrap();
    let top = Dependencies::default();
    let lookup = top.enter(&n, RecordType::A).expect("not a cycle");
    assert!(
        lookup
            .chain()
            .enter(
                &Name::from_ascii("NS.loop.example.").unwrap(),
                RecordType::A
            )
            .is_none(),
        "a lookup nested in itself is a cycle"
    );
    assert!(lookup.chain().enter(&n, RecordType::AAAA).is_some());
    // catches: a per-budget stack treating a concurrent lookup of the same name as a cycle
    assert!(
        top.enter(&n, RecordType::A).is_some(),
        "a sibling lookup on another call chain is not a cycle"
    );
}
