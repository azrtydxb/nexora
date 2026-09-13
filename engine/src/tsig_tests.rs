use crate::proto;
use crate::tsig::{
    KeyRing, StreamSigner, TsigAlg, TsigFailure, Verified, error_response, find_tsig,
    sign_response, verify_request,
};
use hickory_proto::rr::Name;

fn km(algorithm: proto::TsigAlgorithm) -> proto::KeyMaterial {
    proto::KeyMaterial {
        tsig_keys: vec![proto::TsigSecret {
            name: "xfr-key.".into(),
            algorithm: algorithm as i32,
            secret: vec![7u8; 32],
        }],
    }
}

#[test]
fn key_ring_replaces_the_set_skips_unknown_algorithms_and_redacts_debug() {
    let ring = KeyRing::default();
    ring.apply(km(proto::TsigAlgorithm::HmacSha256));
    let k = ring
        .get(b"\x07xfr-key\x00")
        .expect("key by lowercase wire name");
    assert_eq!(k.alg, TsigAlg::HmacSha256);
    assert_eq!(&k.secret[..], &[7u8; 32][..]);
    assert!(
        !format!("{k:?}").contains("7, 7"),
        "secret bytes must not appear in Debug output"
    );
    ring.apply(km(proto::TsigAlgorithm::HmacSha384));
    assert_eq!(
        ring.get(b"\x07xfr-key\x00").unwrap().alg,
        TsigAlg::HmacSha384
    );
    ring.apply(km(proto::TsigAlgorithm::None));
    assert!(
        ring.get(b"\x07xfr-key\x00").is_none(),
        "a key without a supported algorithm is skipped"
    );
    ring.apply(km(proto::TsigAlgorithm::HmacSha512));
    assert_eq!(ring.len(), 1);
    ring.apply(proto::KeyMaterial { tsig_keys: vec![] });
    assert!(
        ring.get(b"\x07xfr-key\x00").is_none(),
        "removed keys disappear"
    );
}

const Q: &[u8] = include_bytes!(concat!(
    env!("CARGO_MANIFEST_DIR"),
    "/../testdata/tsig/query-hmac-sha256.bin"
));
const R: &[u8] = include_bytes!(concat!(
    env!("CARGO_MANIFEST_DIR"),
    "/../testdata/tsig/response-hmac-sha256.bin"
));
const Q512: &[u8] = include_bytes!(concat!(
    env!("CARGO_MANIFEST_DIR"),
    "/../testdata/tsig/query-hmac-sha512.bin"
));
const T0: u64 = 1757750400;

pub(crate) fn test_ring() -> KeyRing {
    let ring = KeyRing::default();
    ring.apply(proto::KeyMaterial {
        tsig_keys: vec![
            proto::TsigSecret {
                name: "xfr-key.".into(),
                algorithm: proto::TsigAlgorithm::HmacSha256 as i32,
                secret: (0u8..32).collect(),
            },
            proto::TsigSecret {
                name: "sha512-key.".into(),
                algorithm: proto::TsigAlgorithm::HmacSha512 as i32,
                secret: (0u8..64).collect(),
            },
        ],
    });
    ring
}

#[test]
fn verifies_miekg_signed_queries() {
    for (msg, name) in [(Q, "xfr-key."), (Q512, "sha512-key.")] {
        match verify_request(msg, &test_ring(), T0 + 10) {
            Ok(Verified::Signed { key, .. }) => {
                assert_eq!(key.name, Name::from_ascii(name).unwrap())
            }
            Ok(Verified::Unsigned) => panic!("unsigned"),
            Err(_) => panic!("verification failed"),
        }
    }
}

#[test]
fn time_outside_fudge_is_badtime() {
    assert!(matches!(
        verify_request(Q, &test_ring(), T0 + 301),
        Err(TsigFailure::BadTime { .. })
    ));
    assert!(matches!(
        verify_request(Q, &test_ring(), T0 - 301),
        Err(TsigFailure::BadTime { .. })
    ));
}

#[test]
fn modified_message_is_badsig() {
    let mut q = Q.to_vec();
    q[2] ^= 0x01; // flip RD
    assert!(matches!(
        verify_request(&q, &test_ring(), T0),
        Err(TsigFailure::BadSig)
    ));
}

#[test]
fn unknown_key_is_badkey() {
    assert!(matches!(
        verify_request(Q, &KeyRing::default(), T0),
        Err(TsigFailure::BadKey)
    ));
}

#[test]
fn unsigned_message_is_reported_unsigned() {
    let t = find_tsig(Q).unwrap().unwrap();
    let mut plain = Q[..t.start].to_vec();
    plain[11] -= 1;
    assert!(matches!(
        verify_request(&plain, &test_ring(), T0),
        Ok(Verified::Unsigned)
    ));
}

#[test]
fn tsig_not_last_or_twice_is_formerr() {
    let t = find_tsig(Q).unwrap().unwrap();
    let rr = Q[t.start..].to_vec();
    let mut twice = Q.to_vec();
    twice.extend_from_slice(&rr);
    twice[11] += 1;
    assert!(matches!(
        verify_request(&twice, &test_ring(), T0),
        Err(TsigFailure::FormErr)
    ));
    let mut class = Q.to_vec();
    let class_at = t.start + 9 + 2; // "\x07xfr-key\x00" (9) + type (2)
    class[class_at + 1] = 1;
    assert!(matches!(
        verify_request(&class, &test_ring(), T0),
        Err(TsigFailure::FormErr)
    ));
}

#[test]
fn response_signature_is_byte_identical_to_miekg() {
    let Ok(Verified::Signed { key, request_mac }) = verify_request(Q, &test_ring(), T0) else {
        panic!("verify")
    };
    let t = find_tsig(R).unwrap().unwrap();
    let mut unsigned = R[..t.start].to_vec();
    let ar = u16::from_be_bytes([unsigned[10], unsigned[11]]) - 1;
    unsigned[10..12].copy_from_slice(&ar.to_be_bytes());
    sign_response(&mut unsigned, &key, T0, &request_mac, true, &[]);
    assert_eq!(unsigned, R);
}

#[test]
fn stream_signer_chains_macs() {
    let Ok(Verified::Signed { key, request_mac }) = verify_request(Q, &test_ring(), T0) else {
        panic!("verify")
    };
    let t = find_tsig(R).unwrap().unwrap();
    let base = R[..t.start].to_vec();
    let mut s = StreamSigner::new(key.clone(), request_mac.clone());
    let (mut m1, mut m2) = (base.clone(), base.clone());
    for m in [&mut m1, &mut m2] {
        let ar = u16::from_be_bytes([m[10], m[11]]) - 1;
        m[10..12].copy_from_slice(&ar.to_be_bytes());
    }
    s.sign(&mut m1, T0);
    s.sign(&mut m2, T0);
    assert_eq!(m1, R, "first message is a normal response signature");
    assert_ne!(
        find_tsig(&m2).unwrap().unwrap().mac,
        find_tsig(&m1).unwrap().unwrap().mac,
        "second MAC covers the first"
    );
}

#[test]
fn error_responses_carry_notauth_and_the_tsig_error() {
    let bad = error_response(Q, &TsigFailure::BadKey, T0);
    assert_eq!(bad[3] & 0x0f, 9, "NOTAUTH");
    assert_ne!(bad[2] & 0x80, 0, "QR");
    let t = find_tsig(&bad).unwrap().unwrap();
    assert_eq!((t.error, t.mac.len()), (17, 0));

    let Err(failure) = verify_request(Q, &test_ring(), T0 + 1000) else {
        panic!("expected BADTIME")
    };
    let badtime = error_response(Q, &failure, T0 + 1000);
    let t = find_tsig(&badtime).unwrap().unwrap();
    assert_eq!(t.error, 18);
    assert_eq!(t.other, (T0 + 1000).to_be_bytes()[2..].to_vec());
    assert_eq!(t.mac.len(), 32, "BADTIME is signed");
}
