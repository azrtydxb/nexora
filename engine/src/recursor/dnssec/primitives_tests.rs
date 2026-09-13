#![allow(clippy::cloned_ref_to_slice_refs)] // plan-given test literals

use super::denial::*;
use super::testsign::{TestKey, nsec_chain, nsec3_chain};
use super::verify::*;
use hickory_proto::dnssec::{Algorithm, DigestType};
use hickory_proto::rr::{Name, RData, Record, RecordType, rdata::A};
use std::cmp::Ordering;
use std::net::Ipv4Addr;

const NOW: u32 = 1_800_000_000;

fn a_rrset(name: &str) -> Vec<Record> {
    vec![Record::from_rdata(
        Name::from_ascii(name).unwrap(),
        300,
        RData::A(A(Ipv4Addr::new(192, 0, 2, 1))),
    )]
}

#[test]
fn validity_window_with_skew_and_wraparound() {
    let inc = NOW - 86_400;
    let exp = NOW + 86_400; // span 2 days -> skew clamps to 3600
    assert_eq!(within_validity(inc, exp, NOW as u64), Validity::Valid);
    assert_eq!(
        within_validity(inc, exp, (exp + 3600) as u64),
        Validity::Valid
    );
    assert_eq!(
        within_validity(inc, exp, (exp + 3601) as u64),
        Validity::Expired
    );
    assert_eq!(
        within_validity(inc, exp, (inc - 3600) as u64),
        Validity::Valid
    );
    assert_eq!(
        within_validity(inc, exp, (inc - 3601) as u64),
        Validity::NotYetValid
    );
    // span 30 minutes -> 10% is 180 s, skew clamps up to 300
    assert_eq!(
        within_validity(NOW, NOW + 1800, (NOW + 2100) as u64),
        Validity::Valid
    );
    assert_eq!(
        within_validity(NOW, NOW + 1800, (NOW + 2101) as u64),
        Validity::Expired
    );
    // span 1 hour -> skew is 10% = 360
    assert_eq!(
        within_validity(NOW, NOW + 3600, (NOW + 3960) as u64),
        Validity::Valid
    );
    assert_eq!(
        within_validity(NOW, NOW + 3600, (NOW + 3961) as u64),
        Validity::Expired
    );
    // RFC 1982 wraparound: inception just before 2^32, expiration after
    assert_eq!(
        within_validity(0xFFFF_FF00, 0x0001_0000, 0x1_0000_0010),
        Validity::Valid
    );
    assert_eq!(
        within_validity(NOW + 10, NOW, NOW as u64),
        Validity::Malformed
    );
}

#[test]
#[allow(deprecated)]
fn only_algorithms_8_13_14_15_are_supported() {
    for (a, ok) in [
        (Algorithm::RSASHA256, true),
        (Algorithm::ECDSAP256SHA256, true),
        (Algorithm::ECDSAP384SHA384, true),
        (Algorithm::ED25519, true),
        (Algorithm::RSASHA1, false),
        (Algorithm::RSASHA512, false),
        (Algorithm::Unknown(16), false),
    ] {
        assert_eq!(algorithm_supported(a), ok, "{a:?}");
    }
    assert!(digest_supported(DigestType::SHA256));
    assert!(!digest_supported(DigestType::Unknown(3)));
}

#[test]
fn verifies_good_signature_and_rejects_tampering_expiry_and_wrong_key() {
    let zsk = TestKey::generate("example.", false);
    let other = TestKey::generate("example.", false);
    let rrset = a_rrset("www.example.");
    let sig = zsk.sign(&rrset, NOW - 3600, NOW + 86_400);
    let zone = Name::from_ascii("example.").unwrap();
    let v = verify_rrset(
        &rrset,
        &[sig.clone()],
        &[zsk.dnskey.clone()],
        &zone,
        NOW as u64,
    )
    .unwrap();
    assert!(!v.wildcard_expanded);
    let mut tampered = rrset.clone();
    tampered[0].data = RData::A(A(Ipv4Addr::new(6, 6, 6, 6)));
    assert_eq!(
        verify_rrset(
            &tampered,
            &[sig.clone()],
            &[zsk.dnskey.clone()],
            &zone,
            NOW as u64
        )
        .unwrap_err(),
        VerifyError::BadSignature
    );
    assert_eq!(
        verify_rrset(
            &rrset,
            &[sig.clone()],
            &[other.dnskey.clone()],
            &zone,
            NOW as u64
        )
        .unwrap_err(),
        VerifyError::NoMatchingKey
    );
    assert_eq!(
        verify_rrset(
            &rrset,
            &[sig.clone()],
            &[zsk.dnskey.clone()],
            &zone,
            (NOW + 86_400 + 7200) as u64
        )
        .unwrap_err(),
        VerifyError::Expired
    );
    assert_eq!(
        verify_rrset(&rrset, &[], &[zsk.dnskey.clone()], &zone, NOW as u64).unwrap_err(),
        VerifyError::NoRrsig
    );
    assert_eq!(
        verify_rrset(
            &rrset,
            &[sig],
            &[zsk.dnskey.clone()],
            &Name::from_ascii("other.").unwrap(),
            NOW as u64
        )
        .unwrap_err(),
        VerifyError::SignerMismatch
    );
}

#[test]
fn ttl_is_capped_by_original_ttl_and_expiration() {
    let zsk = TestKey::generate("example.", false);
    let rrset = a_rrset("www.example.");
    let sig = zsk.sign(&rrset, NOW - 3600, NOW + 100);
    let v = verify_rrset(
        &rrset,
        &[sig],
        &[zsk.dnskey.clone()],
        &Name::from_ascii("example.").unwrap(),
        NOW as u64,
    )
    .unwrap();
    assert_eq!(capped_ttl(3600, &v, NOW as u64), 100);
    assert_eq!(capped_ttl(50, &v, NOW as u64), 50);
}

#[test]
fn ds_matching() {
    let ksk = TestKey::generate("example.", true);
    let other = TestKey::generate("example.", true);
    let zone = Name::from_ascii("example.").unwrap();
    match match_ds(
        &zone,
        &[ksk.dnskey.clone(), other.dnskey.clone()],
        &[ksk.ds()],
    ) {
        DsMatch::Matched(k) => assert_eq!(k, vec![ksk.dnskey.clone()]),
        m => panic!("{m:?}"),
    }
    assert!(matches!(
        match_ds(&zone, &[other.dnskey.clone()], &[ksk.ds()]),
        DsMatch::NoMatch
    ));
    let unsupported = hickory_proto::dnssec::rdata::DS::new(
        1,
        Algorithm::Unknown(200),
        DigestType::SHA256,
        vec![0; 32],
    );
    assert!(matches!(
        match_ds(&zone, &[ksk.dnskey.clone()], &[unsupported]),
        DsMatch::NoSupported
    ));
}

#[test]
fn canonical_order_rfc4034_section_6_1() {
    // hickory-proto's text parser rejects the control-character label, so labels are raw bytes
    let name = |first: &[u8], rest: &str| {
        let rest = Name::from_ascii(rest).unwrap();
        let mut labels: Vec<&[u8]> = vec![first];
        labels.extend(rest.iter());
        Name::from_labels(labels).unwrap()
    };
    let parsed = [
        Name::from_ascii("example.").unwrap(),
        name(b"a", "example."),
        name(b"yljkjljk", "a.example."),
        name(b"Z", "a.example."),
        name(b"zABC", "a.EXAMPLE."),
        name(b"z", "example."),
        name(b"\x01", "z.example."),
        name(b"*", "z.example."),
        name(b"\x80", "z.example."),
    ];
    for w in parsed.windows(2) {
        assert_eq!(
            canonical_cmp(&w[0], &w[1]),
            Ordering::Less,
            "{} < {}",
            w[0],
            w[1]
        );
    }
}

#[test]
fn nsec_nxdomain_and_nodata_proofs() {
    let chain = nsec_chain(
        "example.",
        &[
            ("example.", &[RecordType::SOA, RecordType::NS]),
            ("a.example.", &[RecordType::A]),
            ("d.example.", &[RecordType::A, RecordType::TXT]),
        ],
    );
    let q = Name::from_ascii("b.example.").unwrap();
    assert_eq!(nsec_proves_nxdomain(&q, &chain), Denial::Proven);
    assert_eq!(
        nsec_proves_nxdomain(&q, &chain[1..2]),
        Denial::NotProven("no NSEC covers the wildcard")
    );
    assert_eq!(
        nsec_proves_nxdomain(&Name::from_ascii("a.example.").unwrap(), &chain),
        Denial::NotProven("name exists")
    );
    assert_eq!(
        nsec_proves_nodata(
            &Name::from_ascii("d.example.").unwrap(),
            RecordType::AAAA,
            &chain
        ),
        Denial::Proven
    );
    assert_eq!(
        nsec_proves_nodata(
            &Name::from_ascii("d.example.").unwrap(),
            RecordType::TXT,
            &chain
        ),
        Denial::NotProven("type present in bitmap")
    );
}

#[test]
fn nsec3_nxdomain_nodata_opt_out_and_rfc9276_iterations() {
    let names: [(&str, &[RecordType]); 3] = [
        ("example.", &[RecordType::SOA, RecordType::NS]),
        ("a.example.", &[RecordType::A]),
        ("c.example.", &[RecordType::A]),
    ];
    let zone = Name::from_ascii("example.").unwrap();
    let q = Name::from_ascii("b.example.").unwrap();
    let chain = nsec3_chain("example.", &names, 0, false);
    assert_eq!(nsec3_proves_nxdomain(&q, &zone, &chain), Denial::Proven);
    assert_eq!(
        nsec3_proves_nodata(
            &Name::from_ascii("a.example.").unwrap(),
            RecordType::TXT,
            &zone,
            &chain
        ),
        Denial::Proven
    );
    assert_eq!(
        nsec3_proves_nodata(
            &Name::from_ascii("a.example.").unwrap(),
            RecordType::A,
            &zone,
            &chain
        ),
        Denial::NotProven("type present in bitmap")
    );
    let opt_out = nsec3_chain("example.", &names, 0, true);
    assert_eq!(
        nsec3_proves_nodata(
            &Name::from_ascii("sub.b.example.").unwrap(),
            RecordType::DS,
            &zone,
            &opt_out
        ),
        Denial::ProvenOptOut
    );
    assert_eq!(
        nsec3_proves_nxdomain(&q, &zone, &nsec3_chain("example.", &names, 51, false)),
        Denial::InsecureIterations(51)
    );
    assert_eq!(
        nsec3_proves_nxdomain(&q, &zone, &nsec3_chain("example.", &names, 151, false)),
        Denial::BogusIterations(151)
    );
}
