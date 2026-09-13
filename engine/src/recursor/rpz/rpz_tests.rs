use super::apply::*;
use super::index::*;
use super::parse::*;
use crate::proto::RpzPolicyOverride;
use hickory_proto::op::{Message, ResponseCode};
use hickory_proto::rr::{
    Name, RData, Record, RecordType,
    rdata::{A, CNAME},
};
use hickory_proto::serialize::binary::BinEncodable;
use std::net::{IpAddr, Ipv4Addr};
use std::sync::Arc;

const ZONE: &str = "$TTL 60
@ SOA ns.rpz. hostmaster.rpz. 7 60 60 86400 60
@ NS ns.rpz.
bad.example CNAME .
*.bad.example CNAME *.
pass.bad.example CNAME rpz-passthru.
nodata.example CNAME *.
drop.example CNAME rpz-drop.
tcp.example CNAME rpz-tcp-only.
local.example A 10.9.9.9
local.example TXT \"blocked\"
alias.example CNAME walled.garden.
wild.example CNAME *.garden.
32.66.2.0.192.rpz-ip CNAME .
48.zz.db8.2001.rpz-ip CNAME .
32.2.0.0.127.rpz-client-ip CNAME rpz-tcp-only.
ns.evil.rpz-nsdname CNAME .
24.0.113.0.203.rpz-nsip CNAME .
garbage.rpz-ip CNAME .
";

fn n(s: &str) -> Name {
    Name::from_ascii(s).unwrap()
}
fn w(s: &str) -> Vec<u8> {
    n(s).to_lowercase().to_bytes().unwrap()
}
fn client(last: u8) -> IpAddr {
    IpAddr::V4(Ipv4Addr::new(127, 0, 0, last))
}

fn zone(id: &str, origin: &str, text: &str, ov: RpzPolicyOverride) -> Arc<RpzZoneIndex> {
    let o = n(origin);
    Arc::new(RpzZoneIndex::build(
        id,
        &parse_rpz_text(&o, text).unwrap(),
        ov as i32,
    ))
}

#[test]
fn parses_triggers_actions_and_counts_skipped() {
    let p = parse_rpz_text(&n("rpz.local."), ZONE).unwrap();
    assert_eq!(p.serial, 7);
    assert_eq!(p.skipped, 1);
    assert!(p.rules.contains(&(
        Trigger::ResponseIp("2001:db8::/48".parse().unwrap()),
        RpzAction::Nxdomain
    )));
    assert!(p.rules.contains(&(
        Trigger::Qname {
            name: n("bad.example."),
            wildcard: true
        },
        RpzAction::Nodata
    )));
    assert!(p.rules.contains(&(
        Trigger::Nsdname {
            name: n("ns.evil."),
            wildcard: false
        },
        RpzAction::Nxdomain
    )));
    assert!(p.rules.contains(&(
        Trigger::Nsip("203.0.113.0/24".parse().unwrap()),
        RpzAction::Nxdomain
    )));
    assert!(p.rules.contains(&(
        Trigger::ClientIp("127.0.0.2/32".parse().unwrap()),
        RpzAction::TcpOnly
    )));
}

#[test]
fn include_directive_is_rejected() {
    let err = parse_rpz_text(
        &n("rpz.local."),
        "$INCLUDE /etc/passwd\n@ SOA a. b. 1 1 1 1 1\n",
    )
    .unwrap_err();
    assert_eq!(err, "$INCLUDE is not allowed in RPZ zones");
}

#[test]
fn ip_trigger_decoding() {
    let l = |s: &str| s.split('.').map(String::from).collect::<Vec<_>>();
    assert_eq!(
        decode_ip_trigger(&l("24.0.2.0.192")),
        Some("192.0.2.0/24".parse().unwrap())
    );
    assert_eq!(
        decode_ip_trigger(&l("128.1.zz.2001")),
        Some("2001::1/128".parse().unwrap())
    );
    assert_eq!(decode_ip_trigger(&l("33.1.2.0.192")), None);
    assert_eq!(decode_ip_trigger(&l("24.0.2.0.300")), None);
}

#[test]
fn qname_exact_wildcard_and_client_ip_precedence() {
    let set = RpzSet::new(vec![zone(
        "z",
        "rpz.local.",
        ZONE,
        RpzPolicyOverride::Given,
    )]);
    assert!(matches!(
        set.check_query(&w("bad.example."), client(1)),
        QueryPhase::Hit {
            zone: 0,
            action: RpzAction::Nxdomain
        }
    ));
    assert!(matches!(
        set.check_query(&w("x.y.bad.example."), client(1)),
        QueryPhase::Hit {
            action: RpzAction::Nodata,
            ..
        }
    ));
    assert!(matches!(
        set.check_query(&w("pass.bad.example."), client(1)),
        QueryPhase::Hit {
            action: RpzAction::Passthru,
            ..
        }
    ));
    assert!(matches!(
        set.check_query(&w("notbad.example."), client(1)),
        QueryPhase::NoMatch
    ));
    assert!(
        matches!(
            set.check_query(&w("bad.example."), client(2)),
            QueryPhase::Hit {
                action: RpzAction::TcpOnly,
                ..
            }
        ),
        "CLIENT-IP beats QNAME"
    );
}

#[test]
fn response_triggers_ip_before_nsdname_before_nsip() {
    let set = RpzSet::new(vec![zone(
        "z",
        "rpz.local.",
        ZONE,
        RpzPolicyOverride::Given,
    )]);
    let ans = vec![Record::from_rdata(
        n("www.example."),
        60,
        RData::A(A(Ipv4Addr::new(192, 0, 2, 66))),
    )];
    let chain = vec![n("www.example.")];
    assert!(matches!(
        set.check_response(1, &chain, &ans, &[], &[]),
        Some((0, RpzAction::Nxdomain))
    ));
    let ok = vec![Record::from_rdata(
        n("www.example."),
        60,
        RData::A(A(Ipv4Addr::new(192, 0, 2, 67))),
    )];
    assert!(set.check_response(1, &chain, &ok, &[], &[]).is_none());
    assert!(
        set.check_response(1, &chain, &ok, &[n("NS.evil.")], &[])
            .is_some()
    );
    assert!(
        set.check_response(1, &chain, &ok, &[], &["203.0.113.9".parse().unwrap()])
            .is_some()
    );
    let cname_chain = vec![n("www.example."), n("bad.example.")];
    assert!(
        matches!(
            set.check_response(1, &cname_chain, &ok, &[], &[]),
            Some((0, RpzAction::Nxdomain))
        ),
        "QNAME trigger on a CNAME target"
    );
}

#[test]
fn zone_order_decides_and_earlier_response_triggers_defer() {
    let allow = "@ SOA a. b. 1 60 60 60 60\nallow.example CNAME rpz-passthru.\n";
    let deny = "@ SOA a. b. 1 60 60 60 60\nallow.example CNAME .\nx.example CNAME .\n";
    let set = RpzSet::new(vec![
        zone("a", "allow.rpz.", allow, RpzPolicyOverride::Given),
        zone("d", "deny.rpz.", deny, RpzPolicyOverride::Given),
    ]);
    assert!(matches!(
        set.check_query(&w("allow.example."), client(1)),
        QueryPhase::Hit {
            zone: 0,
            action: RpzAction::Passthru
        }
    ));
    let set = RpzSet::new(vec![
        zone("d", "deny.rpz.", deny, RpzPolicyOverride::Given),
        zone("a", "allow.rpz.", allow, RpzPolicyOverride::Given),
    ]);
    assert!(matches!(
        set.check_query(&w("allow.example."), client(1)),
        QueryPhase::Hit {
            zone: 0,
            action: RpzAction::Nxdomain
        }
    ));
    let ipzone = "@ SOA a. b. 1 60 60 60 60\n32.66.2.0.192.rpz-ip CNAME *.\n";
    let set = RpzSet::new(vec![
        zone("i", "ip.rpz.", ipzone, RpzPolicyOverride::Given),
        zone("d", "deny.rpz.", deny, RpzPolicyOverride::Given),
    ]);
    assert!(matches!(
        set.check_query(&w("x.example."), client(1)),
        QueryPhase::Deferred { zone: 1, .. }
    ));
    let ans = vec![Record::from_rdata(
        n("x.example."),
        60,
        RData::A(A(Ipv4Addr::new(192, 0, 2, 66))),
    )];
    assert!(matches!(
        set.check_response(1, &[n("x.example.")], &ans, &[], &[]),
        Some((0, RpzAction::Nodata))
    ));
}

#[test]
fn actions_synthesise_responses() {
    let z = zone("z", "rpz.local.", ZONE, RpzPolicyOverride::Given);
    let msg = |o: PolicyOutcome| match o {
        PolicyOutcome::Respond { wire, ede } => (Message::from_vec(&wire).unwrap(), ede),
        other => panic!("{other:?}"),
    };
    let (m, ede) = msg(apply_action(
        &n("bad.example."),
        RecordType::A,
        false,
        &z,
        &RpzAction::Nxdomain,
    ));
    assert_eq!(m.metadata.response_code, ResponseCode::NXDomain);
    assert_eq!(m.authorities[0].record_type(), RecordType::SOA);
    assert_eq!(ede.code, 15);
    let local = match parse_rpz_text(&n("rpz.local."), ZONE)
        .unwrap()
        .rules
        .into_iter()
        .find(|(t, _)| {
            *t == Trigger::Qname {
                name: n("local.example."),
                wildcard: false,
            }
        })
        .unwrap()
        .1
    {
        a @ RpzAction::LocalData(_) => a,
        other => panic!("{other:?}"),
    };
    let (m, ede) = msg(apply_action(
        &n("LOCAL.example."),
        RecordType::A,
        false,
        &z,
        &local,
    ));
    assert_eq!(m.answers[0].data, RData::A(A(Ipv4Addr::new(10, 9, 9, 9))));
    assert_eq!(m.answers[0].ttl, 60);
    assert_eq!(ede.code, 4);
    let (m, _) = msg(apply_action(
        &n("local.example."),
        RecordType::AAAA,
        false,
        &z,
        &local,
    ));
    assert_eq!(
        (m.metadata.response_code, m.answers.len()),
        (ResponseCode::NoError, 0)
    );
    assert!(matches!(
        apply_action(
            &n("tcp.example."),
            RecordType::A,
            false,
            &z,
            &RpzAction::TcpOnly
        ),
        PolicyOutcome::Truncate { .. }
    ));
    assert!(matches!(
        apply_action(
            &n("tcp.example."),
            RecordType::A,
            true,
            &z,
            &RpzAction::TcpOnly
        ),
        PolicyOutcome::Passthru
    ));
    assert!(matches!(
        apply_action(
            &n("drop.example."),
            RecordType::A,
            false,
            &z,
            &RpzAction::Drop
        ),
        PolicyOutcome::Drop
    ));
    let wild = parse_rpz_text(&n("rpz.local."), ZONE)
        .unwrap()
        .rules
        .into_iter()
        .find(|(t, _)| {
            *t == Trigger::Qname {
                name: n("wild.example."),
                wildcard: false,
            }
        })
        .unwrap()
        .1;
    match apply_action(&n("wild.example."), RecordType::A, false, &z, &wild) {
        PolicyOutcome::ChaseCname { cname, target } => {
            assert_eq!(target, n("wild.example.garden."));
            assert_eq!(cname.data, RData::CNAME(CNAME(n("wild.example.garden."))));
        }
        other => panic!("{other:?}"),
    }
}

#[test]
fn policy_override_replaces_or_disables_action() {
    let set = RpzSet::new(vec![zone(
        "z",
        "rpz.local.",
        ZONE,
        RpzPolicyOverride::Nodata,
    )]);
    assert_eq!(
        set.effective_action(0, &RpzAction::Nxdomain),
        Some(RpzAction::Nodata)
    );
    let set = RpzSet::new(vec![zone(
        "z",
        "rpz.local.",
        ZONE,
        RpzPolicyOverride::Disabled,
    )]);
    assert_eq!(set.effective_action(0, &RpzAction::Nxdomain), None);
}
