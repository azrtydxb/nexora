use super::answer::{Limits, Served, respond};
use super::msg::Question;
use super::set::AuthSet;
use super::zone::Zone;
use super::{nsec3, nzf};
use hickory_proto::dnssec::rdata::DNSSECRData;
use hickory_proto::op::{Edns, Message, MessageType, OpCode, Query, ResponseCode};
use hickory_proto::rr::{Name, RData, Record, RecordType};
use std::sync::Arc;

pub(crate) const NSEC: &[u8] = include_bytes!(concat!(
    env!("CARGO_MANIFEST_DIR"),
    "/../testdata/nzf/signed-nsec-full.nzf"
));
pub(crate) const NSEC3: &[u8] = include_bytes!(concat!(
    env!("CARGO_MANIFEST_DIR"),
    "/../testdata/nzf/signed-nsec3-full.nzf"
));

fn set(img: &[u8]) -> AuthSet {
    AuthSet::from_zones(vec![Arc::new(
        Zone::from_image(&nzf::parse(img).unwrap()).unwrap(),
    )])
    .unwrap()
}

fn ask(set: &AuthSet, name: &str, qtype: RecordType, dnssec_ok: bool) -> Message {
    let mut m = Message::new(9, MessageType::Query, OpCode::Query);
    m.add_query(Query::query(Name::from_ascii(name).unwrap(), qtype));
    let mut e = Edns::new();
    e.set_dnssec_ok(dnssec_ok);
    e.set_max_payload(4096);
    m.set_edns(e);
    let b = m.to_vec().unwrap();
    let q = Question::parse(&b).unwrap();
    let mut out = vec![0u8; 65535];
    match respond(
        set,
        &q,
        &mut out,
        Limits {
            max_len: 16384,
            recursion_available: false,
        },
    ) {
        Served::Done(n) => Message::from_vec(&out[..n]).unwrap(),
        Served::NotHosted => panic!("not hosted"),
    }
}

fn count(rrs: &[Record], t: RecordType) -> usize {
    rrs.iter().filter(|r| r.record_type() == t).count()
}

fn covered(rrs: &[Record]) -> Vec<RecordType> {
    rrs.iter()
        .filter_map(|r| match &r.data {
            RData::DNSSEC(DNSSECRData::RRSIG(s)) => Some(s.input().type_covered),
            _ => None,
        })
        .collect()
}

#[test]
fn rfc5155_appendix_a_hash_vector() {
    let h = nsec3::hash(b"\x07example\x00", 12, &[0xaa, 0xbb, 0xcc, 0xdd]);
    assert_eq!(&nsec3::b32hex(&h), b"0p9mhaveqvm6t7vbl5lop2u3t2rp3tom");
    assert_eq!(
        nsec3::decode_b32hex(b"0P9MHAVEQVM6T7VBL5LOP2U3T2RP3TOM"),
        Some(h)
    );
}

#[test]
fn positive_answer_carries_rrsig_only_with_do() {
    for img in [NSEC, NSEC3] {
        let s = set(img);
        let with = ask(&s, "www.example.test.", RecordType::A, true);
        assert_eq!(count(&with.answers, RecordType::A), 2);
        assert_eq!(covered(&with.answers), vec![RecordType::A]);
        let without = ask(&s, "www.example.test.", RecordType::A, false);
        assert_eq!(count(&without.answers, RecordType::A), 2);
        assert_eq!(count(&without.answers, RecordType::RRSIG), 0);
        assert_eq!(count(&without.authorities, RecordType::RRSIG), 0);
        let nx = ask(&s, "nope.example.test.", RecordType::A, false);
        assert_eq!(nx.metadata.response_code, ResponseCode::NXDomain);
        assert_eq!(count(&nx.authorities, RecordType::SOA), 1);
        assert_eq!(nx.authorities.len(), 1, "no proofs without DO");
    }
}

#[test]
fn nsec_nxdomain_and_nodata_proofs() {
    let s = set(NSEC);
    let nx = ask(&s, "nope.example.test.", RecordType::A, true);
    assert_eq!(nx.metadata.response_code, ResponseCode::NXDomain);
    let n = count(&nx.authorities, RecordType::NSEC);
    assert!(
        (1..=2).contains(&n),
        "NSEC covering qname and wildcard, got {n}"
    );
    assert!(covered(&nx.authorities).contains(&RecordType::SOA));
    assert_eq!(
        covered(&nx.authorities)
            .iter()
            .filter(|t| **t == RecordType::NSEC)
            .count(),
        n
    );

    let nodata = ask(&s, "www.example.test.", RecordType::MX, true);
    assert_eq!(nodata.metadata.response_code, ResponseCode::NoError);
    let nsecs: Vec<_> = nodata
        .authorities
        .iter()
        .filter(|r| r.record_type() == RecordType::NSEC)
        .collect();
    assert_eq!(nsecs.len(), 1);
    assert_eq!(nsecs[0].name.to_ascii(), "www.example.test.");
}

#[test]
fn nsec3_nxdomain_has_closest_encloser_proof() {
    let s = set(NSEC3);
    let nx = ask(&s, "x.nope.example.test.", RecordType::A, true);
    assert_eq!(nx.metadata.response_code, ResponseCode::NXDomain);
    let owners: Vec<String> = nx
        .authorities
        .iter()
        .filter(|r| r.record_type() == RecordType::NSEC3)
        .map(|r| r.name.to_ascii())
        .collect();
    assert!((2..=3).contains(&owners.len()), "{owners:?}");
    let apex_hash =
        String::from_utf8(nsec3::b32hex(&nsec3::hash(b"\x07example\x04test\x00", 0, &[])).to_vec())
            .unwrap();
    assert!(
        owners.iter().any(|o| o.starts_with(&apex_hash)),
        "closest encloser (apex) NSEC3 must match exactly: {owners:?}"
    );
}

#[test]
fn nsec3_nodata_at_empty_non_terminal() {
    let s = set(NSEC3);
    let r = ask(&s, "b.c.example.test.", RecordType::A, true);
    assert_eq!(r.metadata.response_code, ResponseCode::NoError);
    let ent = String::from_utf8(
        nsec3::b32hex(&nsec3::hash(b"\x01b\x01c\x07example\x04test\x00", 0, &[])).to_vec(),
    )
    .unwrap();
    let owners: Vec<String> = r
        .authorities
        .iter()
        .filter(|x| x.record_type() == RecordType::NSEC3)
        .map(|x| x.name.to_ascii())
        .collect();
    assert_eq!(owners.len(), 1);
    assert!(owners[0].starts_with(&ent));
}

#[test]
fn wildcard_answer_has_signature_with_fewer_labels_and_denial_of_qname() {
    for img in [NSEC, NSEC3] {
        let r = ask(
            &set(img),
            "anything.wild.example.test.",
            RecordType::TXT,
            true,
        );
        assert_eq!(count(&r.answers, RecordType::TXT), 1);
        let sig = r
            .answers
            .iter()
            .find_map(|x| match &x.data {
                RData::DNSSEC(DNSSECRData::RRSIG(s)) => Some(s.input().num_labels),
                _ => None,
            })
            .unwrap();
        assert_eq!(sig, 3, "labels of *.wild.example.test minus the wildcard");
        assert!(
            count(&r.authorities, RecordType::NSEC) + count(&r.authorities, RecordType::NSEC3) >= 1,
            "proof that the exact name does not exist"
        );
    }
}

#[test]
fn referrals_include_ds_or_proof_of_no_ds() {
    for img in [NSEC, NSEC3] {
        let s = set(img);
        let secure = ask(&s, "host.sub.example.test.", RecordType::A, true);
        assert!(!secure.metadata.authoritative);
        assert_eq!(count(&secure.authorities, RecordType::DS), 1);
        assert!(covered(&secure.authorities).contains(&RecordType::DS));
        assert_eq!(
            covered(&secure.authorities)
                .iter()
                .filter(|t| **t == RecordType::NS)
                .count(),
            0,
            "delegation NS is unsigned"
        );
        assert_eq!(
            count(&secure.additionals, RecordType::RRSIG),
            0,
            "glue is unsigned"
        );
        let insecure = ask(&s, "host.insecure.example.test.", RecordType::A, true);
        assert_eq!(count(&insecure.authorities, RecordType::NS), 1);
        assert_eq!(count(&insecure.authorities, RecordType::DS), 0);
        assert_eq!(
            count(&insecure.authorities, RecordType::NSEC)
                + count(&insecure.authorities, RecordType::NSEC3),
            1
        );
    }
}

#[test]
fn ds_query_at_an_unsigned_delegation_is_a_signed_nodata() {
    for img in [NSEC, NSEC3] {
        let s = set(img);
        let r = ask(&s, "insecure.example.test.", RecordType::DS, true);
        assert!(r.metadata.authoritative);
        assert_eq!(r.metadata.response_code, ResponseCode::NoError);
        assert!(r.answers.is_empty());
        assert!(covered(&r.authorities).contains(&RecordType::SOA));
        assert_eq!(
            count(&r.authorities, RecordType::NSEC) + count(&r.authorities, RecordType::NSEC3),
            1
        );
        let ds = ask(&s, "sub.example.test.", RecordType::DS, true);
        assert!(ds.metadata.authoritative);
        assert_eq!(count(&ds.answers, RecordType::DS), 1);
        assert_eq!(covered(&ds.answers), vec![RecordType::DS]);
    }
}

#[test]
fn cds_and_cdnskey_are_served_as_signed_data() {
    for img in [NSEC, NSEC3] {
        let s = set(img);
        for t in [RecordType::CDS, RecordType::CDNSKEY, RecordType::DNSKEY] {
            let r = ask(&s, "example.test.", t, true);
            assert!(r.metadata.authoritative);
            assert!(count(&r.answers, t) >= 1, "{t:?}");
            assert_eq!(covered(&r.answers), vec![t]);
        }
    }
}

#[test]
fn truncates_when_proofs_do_not_fit() {
    let s = set(NSEC3);
    let mut m = Message::new(9, MessageType::Query, OpCode::Query);
    m.add_query(Query::query(
        Name::from_ascii("x.nope.example.test.").unwrap(),
        RecordType::A,
    ));
    let mut e = Edns::new();
    e.set_dnssec_ok(true);
    m.set_edns(e);
    let b = m.to_vec().unwrap();
    let q = Question::parse(&b).unwrap();
    let mut out = vec![0u8; 65535];
    let Served::Done(n) = respond(
        &s,
        &q,
        &mut out,
        Limits {
            max_len: 300,
            recursion_available: false,
        },
    ) else {
        panic!("not hosted");
    };
    let r = Message::from_vec(&out[..n]).unwrap();
    assert!(r.metadata.truncation);
    assert!(r.authorities.is_empty());
}
