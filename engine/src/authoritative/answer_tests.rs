use super::answer::{Limits, Served, respond};
use super::msg::Question;
use super::set::AuthSet;
use super::zone::Zone;
use super::{nzf, zone_tests::FULL};
use hickory_proto::op::{Message, MessageType, OpCode, Query, ResponseCode};
use hickory_proto::rr::{Name, RData, Record, RecordType};
use std::sync::Arc;

pub(crate) fn basic_set() -> AuthSet {
    let z = Zone::from_image(&nzf::parse(FULL).unwrap()).unwrap();
    AuthSet::from_zones(vec![Arc::new(z)]).unwrap()
}

pub(crate) fn ask_with(set: &AuthSet, name: &str, qtype: RecordType, max_len: usize) -> Message {
    let mut m = Message::new(0x1234, MessageType::Query, OpCode::Query);
    m.metadata.recursion_desired = true;
    m.add_query(Query::query(Name::from_ascii(name).unwrap(), qtype));
    let bytes = m.to_vec().unwrap();
    let q = Question::parse(&bytes).unwrap();
    let mut out = vec![0u8; 65535];
    match respond(
        set,
        &q,
        &mut out,
        Limits {
            max_len,
            recursion_available: false,
        },
    ) {
        Served::Done(n) => Message::from_vec(&out[..n]).unwrap(),
        Served::NotHosted => panic!("{name} not hosted"),
    }
}

fn ask(set: &AuthSet, name: &str, qtype: RecordType) -> Message {
    ask_with(set, name, qtype, 4096)
}

pub(crate) fn types(rrs: &[Record]) -> Vec<RecordType> {
    rrs.iter().map(|r| r.record_type()).collect()
}

#[test]
fn exact_answer_is_authoritative_and_echoes_client_case() {
    let r = ask(&basic_set(), "WWW.Example.Test.", RecordType::A);
    assert!(r.metadata.authoritative);
    assert_eq!(r.metadata.response_code, ResponseCode::NoError);
    assert_eq!(r.metadata.id, 0x1234);
    assert_eq!(types(&r.answers), vec![RecordType::A, RecordType::A]);
    assert_eq!(r.queries[0].name().to_ascii(), "WWW.Example.Test.");
}

#[test]
fn nxdomain_has_soa_with_negative_ttl() {
    let r = ask(&basic_set(), "nope.example.test.", RecordType::A);
    assert_eq!(r.metadata.response_code, ResponseCode::NXDomain);
    assert!(r.metadata.authoritative);
    assert!(r.answers.is_empty());
    assert_eq!(types(&r.authorities), vec![RecordType::SOA]);
    assert_eq!(r.authorities[0].ttl, 300, "min(SOA TTL 3600, MINIMUM 300)");
}

#[test]
fn nodata_for_existing_name_and_empty_non_terminal() {
    for name in [
        "www.example.test.",
        "b.c.example.test.",
        "wild.example.test.",
    ] {
        let r = ask(&basic_set(), name, RecordType::MX);
        assert_eq!(r.metadata.response_code, ResponseCode::NoError, "{name}");
        assert!(r.metadata.authoritative, "{name}");
        assert!(r.answers.is_empty(), "{name}");
        assert_eq!(types(&r.authorities), vec![RecordType::SOA], "{name}");
    }
}

#[test]
fn nxdomain_below_empty_non_terminal_without_wildcard() {
    let r = ask(&basic_set(), "x.b.c.example.test.", RecordType::A);
    assert_eq!(r.metadata.response_code, ResponseCode::NXDomain);
}

#[test]
fn referral_below_apex_has_ns_and_glue_without_aa() {
    let r = ask(&basic_set(), "host.sub.example.test.", RecordType::A);
    assert!(!r.metadata.authoritative);
    assert_eq!(r.metadata.response_code, ResponseCode::NoError);
    assert!(r.answers.is_empty());
    assert_eq!(
        types(&r.authorities),
        vec![RecordType::NS],
        "no DS without DO"
    );
    assert_eq!(r.additionals.len(), 1);
    assert_eq!(r.additionals[0].name.to_ascii(), "ns.sub.example.test.");
}

#[test]
fn glue_is_not_served_as_authoritative_data() {
    let r = ask(&basic_set(), "ns.sub.example.test.", RecordType::A);
    assert!(
        !r.metadata.authoritative,
        "occluded name must produce a referral"
    );
    assert_eq!(types(&r.authorities), vec![RecordType::NS]);
}

#[test]
fn ds_at_cut_is_answered_from_parent_side() {
    let r = ask(&basic_set(), "sub.example.test.", RecordType::DS);
    assert!(r.metadata.authoritative);
    assert_eq!(types(&r.answers), vec![RecordType::DS]);
}

#[test]
fn wildcard_synthesis_uses_query_name() {
    let r = ask(&basic_set(), "anything.wild.example.test.", RecordType::TXT);
    assert!(r.metadata.authoritative);
    assert_eq!(types(&r.answers), vec![RecordType::TXT]);
    assert_eq!(r.answers[0].name.to_ascii(), "anything.wild.example.test.");
    let nodata = ask(&basic_set(), "anything.wild.example.test.", RecordType::A);
    assert_eq!(nodata.metadata.response_code, ResponseCode::NoError);
    assert_eq!(types(&nodata.authorities), vec![RecordType::SOA]);
}

#[test]
fn cname_chain_is_followed_inside_hosted_data() {
    let r = ask(&basic_set(), "alias.example.test.", RecordType::A);
    assert!(r.metadata.authoritative);
    assert_eq!(
        types(&r.answers),
        vec![RecordType::CNAME, RecordType::A, RecordType::A]
    );
    let c = ask(&basic_set(), "alias.example.test.", RecordType::CNAME);
    assert_eq!(types(&c.answers), vec![RecordType::CNAME]);
}

#[test]
fn cname_loop_is_servfail() {
    let r = ask(&basic_set(), "loop1.example.test.", RecordType::A);
    assert_eq!(r.metadata.response_code, ResponseCode::ServFail);
}

#[test]
fn dname_synthesises_cname_with_dname_ttl() {
    let r = ask(&basic_set(), "host.dn.example.test.", RecordType::A);
    assert!(r.metadata.authoritative);
    assert_eq!(
        types(&r.answers),
        vec![RecordType::DNAME, RecordType::CNAME]
    );
    match &r.answers[1].data {
        RData::CNAME(c) => assert_eq!(c.0.to_ascii(), "host.example.net."),
        other => panic!("{other:?}"),
    }
    assert_eq!(r.answers[1].ttl, 300);
    assert_eq!(r.answers[1].name.to_ascii(), "host.dn.example.test.");
}

#[test]
fn dname_owner_itself_is_not_rewritten() {
    let r = ask(&basic_set(), "dn.example.test.", RecordType::DNAME);
    assert_eq!(types(&r.answers), vec![RecordType::DNAME]);
}

#[test]
fn any_query_returns_a_single_rrset() {
    let r = ask(&basic_set(), "example.test.", RecordType::ANY);
    let t = types(&r.answers);
    assert!(!t.is_empty());
    assert!(
        t.iter().all(|x| *x == t[0]),
        "RFC 8482: one RRset, got {t:?}"
    );
}

#[test]
fn oversized_answer_is_truncated() {
    let r = ask_with(&basic_set(), "big.example.test.", RecordType::TXT, 1232);
    assert!(r.metadata.truncation);
    assert!(r.answers.is_empty());
    assert_eq!(r.queries.len(), 1);
    let full = ask_with(&basic_set(), "big.example.test.", RecordType::TXT, 65535);
    assert!(!full.metadata.truncation);
    assert_eq!(full.answers.len(), 40);
}

#[test]
fn names_outside_hosted_zones_are_not_hosted() {
    let s = basic_set();
    let mut m = Message::new(1, MessageType::Query, OpCode::Query);
    m.add_query(Query::query(
        Name::from_ascii("example.org.").unwrap(),
        RecordType::A,
    ));
    let b = m.to_vec().unwrap();
    let q = Question::parse(&b).unwrap();
    let mut out = vec![0u8; 512];
    assert!(matches!(
        respond(
            &s,
            &q,
            &mut out,
            Limits {
                max_len: 512,
                recursion_available: true
            }
        ),
        Served::NotHosted
    ));
}

/// Header (id 7, opcode QUERY, counts) + question `example.test. <qtype> IN`.
fn raw_query(qtype: u16, ns: u16, ar: u16) -> Vec<u8> {
    let mut b = vec![0, 7, 0, 0, 0, 1, 0, 0];
    b.extend_from_slice(&ns.to_be_bytes());
    b.extend_from_slice(&ar.to_be_bytes());
    b.extend_from_slice(b"\x07example\x04test\x00");
    b.extend_from_slice(&qtype.to_be_bytes());
    b.extend_from_slice(&[0, 1]);
    b
}

#[test]
fn question_parser_reads_ixfr_serial_and_trailing_tsig() {
    use super::msg::MsgError;
    let mut ixfr = raw_query(251, 1, 0);
    // Authority SOA owned by a pointer to the question name: ". . serial 42 1 2 3 4".
    ixfr.extend_from_slice(&[0xc0, 12, 0, 6, 0, 1, 0, 0, 0, 0, 0, 22, 0, 0]);
    for v in [42u32, 1, 2, 3, 4] {
        ixfr.extend_from_slice(&v.to_be_bytes());
    }
    let q = Question::parse(&ixfr).unwrap();
    assert_eq!(q.ixfr_serial, Some(42));
    assert_eq!(q.qtype, 251);

    let tsig_rr = [0u8, 0, 250, 0, 255, 0, 0, 0, 0, 0, 0];
    let opt_rr = [0u8, 0, 41, 4, 208, 0, 0, 0x80, 0, 0, 0];
    let mut signed = raw_query(1, 0, 2);
    let at = signed.len() + opt_rr.len();
    signed.extend_from_slice(&opt_rr);
    signed.extend_from_slice(&tsig_rr);
    let q = Question::parse(&signed).unwrap();
    assert_eq!(q.tsig_at, Some(at));
    let e = q.edns.unwrap();
    assert_eq!((e.udp_size, e.do_bit), (1232, true));

    let mut misplaced = raw_query(1, 0, 2);
    misplaced.extend_from_slice(&tsig_rr);
    misplaced.extend_from_slice(&opt_rr);
    assert_eq!(Question::parse(&misplaced).err(), Some(MsgError::Rr));

    let mut forward = raw_query(251, 1, 0);
    let here = forward.len() as u8;
    forward.extend_from_slice(&[0xc0, here + 2, 0, 6, 0, 1, 0, 0, 0, 0, 0, 0]);
    assert_eq!(
        Question::parse(&forward).err(),
        Some(MsgError::Rr),
        "forward pointer"
    );
    assert_eq!(
        Question::parse(&raw_query(1, 1, 0)).err(),
        Some(MsgError::Counts)
    );
    assert_eq!(Question::parse(&[0u8; 11]).err(), Some(MsgError::Short));
}

#[test]
fn chains_stop_at_names_outside_hosted_zones_and_at_cuts() {
    let set = basic_set();
    let r = ask(&set, "host.dn.example.test.", RecordType::AAAA);
    assert_eq!(r.metadata.response_code, ResponseCode::NoError);
    assert_eq!(r.answers.len(), 2, "DNAME + CNAME, target not hosted");
    let any = ask(&set, "www.example.test.", RecordType::ANY);
    assert_eq!(types(&any.answers), vec![RecordType::A, RecordType::A]);
    let upper = ask(&set, "HOST.SUB.EXAMPLE.TEST.", RecordType::A);
    assert!(!upper.metadata.authoritative);
    assert_eq!(upper.additionals.len(), 1, "glue found case-insensitively");
}
