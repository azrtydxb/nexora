use super::budget::{Limit, WorkBudget};
use super::infra::InfraCache;
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
fn select_picks_randomly_within_400ms_band() {
    let c = InfraCache::new(1000);
    let zone = Name::root();
    c.record_rtt(ip(7), Duration::from_millis(20));
    c.record_rtt(ip(8), Duration::from_millis(60));
    for _ in 0..10 {
        c.record_rtt(ip(9), Duration::from_millis(900));
    }
    let mut rng = rand::rngs::StdRng::seed_from_u64(7);
    let mut seen = std::collections::HashSet::new();
    for _ in 0..200 {
        seen.insert(
            c.select(&[ip(7), ip(8), ip(9)], &zone, 0, &mut rng)
                .unwrap(),
        );
    }
    assert!(seen.contains(&ip(7)) && seen.contains(&ip(8)));
    assert!(!seen.contains(&ip(9)));
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
    assert!(b.enter(&n, RecordType::A));
    assert!(!b.enter(
        &Name::from_ascii("NS.loop.example.").unwrap(),
        RecordType::A
    ));
    b.leave(&n, RecordType::A);
    assert!(b.enter(&n, RecordType::A));
}
