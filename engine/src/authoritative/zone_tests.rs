use super::name::from_ascii;
use super::nzf::{self, NzfError};
use super::zone::{NODE_BELOW_CUT, NODE_WILDCARD_CHILD, Zone, ZoneError};

pub(crate) const FULL: &[u8] = include_bytes!(concat!(
    env!("CARGO_MANIFEST_DIR"),
    "/../testdata/nzf/basic-full.nzf"
));
pub(crate) const DELTA: &[u8] = include_bytes!(concat!(
    env!("CARGO_MANIFEST_DIR"),
    "/../testdata/nzf/basic-delta.nzf"
));
pub(crate) const AFTER: &[u8] = include_bytes!(concat!(
    env!("CARGO_MANIFEST_DIR"),
    "/../testdata/nzf/basic-after.nzf"
));

fn w(name: &str) -> Vec<u8> {
    from_ascii(name).unwrap()
}

#[test]
fn full_image_builds_empty_non_terminals_cuts_and_wildcards() {
    let z = Zone::from_image(&nzf::parse(FULL).unwrap()).unwrap();
    assert_eq!(z.serial(), 2026091301);
    assert_eq!(z.origin(), &w("example.test.")[..]);
    assert!(
        z.node(&w("b.c.example.test.")).unwrap().is_empty(),
        "b.c is an empty non-terminal"
    );
    assert!(
        z.node(&w("c.example.test.")).unwrap().is_empty(),
        "c is an empty non-terminal"
    );
    assert!(z.node(&w("sub.example.test.")).unwrap().is_cut());
    assert!(!z.apex().is_cut(), "apex NS is not a cut");
    assert_ne!(
        z.node(&w("ns.sub.example.test.")).unwrap().flags & NODE_BELOW_CUT,
        0
    );
    assert_ne!(
        z.node(&w("wild.example.test.")).unwrap().flags & NODE_WILDCARD_CHILD,
        0
    );
    assert!(z.node(&w("nothere.example.test.")).is_none());
    assert!(
        z.node(&w("WWW.EXAMPLE.TEST.")).is_some(),
        "lookup is case-insensitive"
    );
    assert!(!z.is_signed());
}

#[test]
fn delta_application_equals_after_image() {
    let base = Zone::from_image(&nzf::parse(FULL).unwrap()).unwrap();
    let applied = base.apply(&nzf::parse(DELTA).unwrap()).unwrap();
    let after = Zone::from_image(&nzf::parse(AFTER).unwrap()).unwrap();
    assert_eq!(applied.serial(), 2026091302);
    assert_eq!(applied.records_sorted(), after.records_sorted());
    assert_eq!(
        base.serial(),
        2026091301,
        "apply must not mutate the source zone"
    );
    assert!(base.node(&w("new.example.test.")).is_none());
}

#[test]
fn delta_from_wrong_serial_is_rejected() {
    let base = Zone::from_image(&nzf::parse(FULL).unwrap()).unwrap();
    let once = base.apply(&nzf::parse(DELTA).unwrap()).unwrap();
    match once.apply(&nzf::parse(DELTA).unwrap()) {
        Err(ZoneError::SerialMismatch { have, delta_from }) => {
            assert_eq!((have, delta_from), (2026091302, 2026091301))
        }
        other => panic!(
            "expected SerialMismatch, got {:?}",
            other.map(|z| z.serial())
        ),
    }
}

#[test]
fn parser_rejects_malformed_blobs() {
    let mut t = FULL.to_vec();
    t.truncate(t.len() - 1);
    assert!(matches!(nzf::parse(&t), Err(NzfError::Truncated)));
    let mut trailing = FULL.to_vec();
    trailing.push(0);
    assert!(matches!(nzf::parse(&trailing), Err(NzfError::Trailing)));
    let mut magic = FULL.to_vec();
    magic[0] = b'X';
    assert!(matches!(nzf::parse(&magic), Err(NzfError::BadMagic)));
    let mut comp = FULL.to_vec();
    let first_owner = 7 + comp[6] as usize + 16;
    comp[first_owner + 1] = 0xc0;
    assert!(matches!(nzf::parse(&comp), Err(NzfError::BadName)));
    assert!(nzf::decompress(b"not zstd", 1 << 20).is_err());
}

const BIG: &[u8] = include_bytes!(concat!(
    env!("CARGO_MANIFEST_DIR"),
    "/../testdata/nzf/big-full.nzf"
));

/// Builds an NZF1 buffer by hand: (owner, type, ttl, rdata) records for the a and b lists.
type Rec<'a> = (&'a str, u16, u32, Vec<u8>);

fn build(kind: u8, origin: &str, serial: u32, from: u32, a: &[Rec<'_>], b: &[Rec<'_>]) -> Vec<u8> {
    let mut out = b"NZF1".to_vec();
    let o = w(origin);
    out.extend_from_slice(&[kind, 0, o.len() as u8]);
    out.extend_from_slice(&o);
    for v in [serial, from, a.len() as u32, b.len() as u32] {
        out.extend_from_slice(&v.to_be_bytes());
    }
    for (owner, t, ttl, rdata) in a.iter().chain(b) {
        let ow = w(owner);
        out.push(ow.len() as u8);
        out.extend_from_slice(&ow);
        out.extend_from_slice(&t.to_be_bytes());
        out.extend_from_slice(&1u16.to_be_bytes());
        out.extend_from_slice(&ttl.to_be_bytes());
        out.extend_from_slice(&(rdata.len() as u16).to_be_bytes());
        out.extend_from_slice(rdata);
    }
    out
}

fn soa(serial: u32) -> Vec<u8> {
    let mut r = w("ns1.example.test.");
    r.extend_from_slice(&w("hostmaster.example.test."));
    for v in [serial, 7200, 3600, 1209600, 300] {
        r.extend_from_slice(&v.to_be_bytes());
    }
    r
}

#[test]
fn canon_key_order_matches_go_goldens() {
    use super::name::canon_key;
    for blob in [FULL, AFTER, BIG] {
        let p = nzf::parse(blob).unwrap();
        let mut prev = Vec::new();
        for r in &p.a {
            let mut k = Vec::new();
            canon_key(r.owner, &mut k);
            assert!(prev <= k, "Go canonical order differs from canon_key");
            prev = k;
        }
    }
    let big = Zone::from_image(&nzf::parse(BIG).unwrap()).unwrap();
    assert_eq!(big.records_sorted().len(), 2002);
}

#[test]
fn name_helpers() {
    use super::name::{is_subdomain, label_offsets, lowercase_into};
    assert!(is_subdomain(&w("A.Example.TEST."), &w("example.test.")));
    assert!(is_subdomain(&w("example.test."), &w("example.test.")));
    assert!(!is_subdomain(&w("xexample.test."), &w("example.test.")));
    assert!(is_subdomain(&w("example.test."), &w(".")));
    assert!(!is_subdomain(&w("test."), &w("example.test.")));
    let mut offs = [0u16; 128];
    assert_eq!(
        label_offsets(&[5, b'a', 0], &mut offs),
        0,
        "label running past the buffer is not counted"
    );
    let mut buf = [0u8; 255];
    assert_eq!(
        lowercase_into(&w("WwW.Example."), &mut buf),
        &w("www.example.")[..]
    );
    assert!(from_ascii(&"a".repeat(64)).is_none());
}

#[test]
fn parser_bounds() {
    let good = build(
        1,
        "example.test.",
        1,
        0,
        &[("example.test.", 6, 60, soa(1))],
        &[],
    );
    assert!(nzf::parse(&good).is_ok());
    // Record count larger than the remaining octets can hold.
    let mut huge = good.clone();
    let counts = 7 + huge[6] as usize + 8;
    huge[counts..counts + 4].copy_from_slice(&u32::MAX.to_be_bytes());
    assert!(matches!(nzf::parse(&huge), Err(NzfError::TooLarge)));
    let out = build(
        1,
        "example.test.",
        1,
        0,
        &[("example.org.", 6, 60, soa(1))],
        &[],
    );
    assert!(matches!(nzf::parse(&out), Err(NzfError::OutOfZone)));
    let suffix = build(
        1,
        "example.test.",
        1,
        0,
        &[("xexample.test.", 1, 60, vec![1, 2, 3, 4])],
        &[],
    );
    assert!(matches!(nzf::parse(&suffix), Err(NzfError::OutOfZone)));
    let mut kind = good.clone();
    kind[4] = 3;
    assert!(matches!(nzf::parse(&kind), Err(NzfError::BadKind)));
    let mut class = good.clone();
    let class_at = good.len() - soa(1).len() - 2 - 4 - 2;
    class[class_at + 1] = 3;
    assert!(matches!(nzf::parse(&class), Err(NzfError::BadRecord)));
    let root = build(1, ".", 1, 0, &[], &[]);
    assert!(matches!(nzf::parse(&root), Err(NzfError::BadName)));
    // Owner length octet shorter than the name it declares.
    let mut short = good.clone();
    let owner_at = 7 + good[6] as usize + 16;
    short[owner_at] -= 1;
    assert!(nzf::parse(&short).is_err());
    for n in 0..good.len() {
        assert!(
            nzf::parse(&good[..n]).is_err(),
            "prefix of {n} octets accepted"
        );
    }
    let big = zstd::encode_all(FULL, 3).unwrap();
    assert_eq!(nzf::decompress(&big, FULL.len()).unwrap(), FULL);
    assert!(matches!(
        nzf::decompress(&big, FULL.len() - 1),
        Err(NzfError::TooLarge)
    ));
}

#[test]
fn zone_rejects_inconsistent_soa_and_deletes() {
    let no_soa = build(
        1,
        "example.test.",
        1,
        0,
        &[("www.example.test.", 1, 60, vec![192, 0, 2, 1])],
        &[],
    );
    assert_eq!(
        Zone::from_image(&nzf::parse(&no_soa).unwrap()).unwrap_err(),
        ZoneError::NoSoa
    );
    let serial = build(
        1,
        "example.test.",
        2,
        0,
        &[("example.test.", 6, 60, soa(1))],
        &[],
    );
    assert_eq!(
        Zone::from_image(&nzf::parse(&serial).unwrap()).unwrap_err(),
        ZoneError::BadSoa
    );
    let two = build(
        1,
        "example.test.",
        1,
        0,
        &[
            ("example.test.", 6, 60, soa(1)),
            ("example.test.", 6, 60, soa(9)),
        ],
        &[],
    );
    assert_eq!(
        Zone::from_image(&nzf::parse(&two).unwrap()).unwrap_err(),
        ZoneError::NoSoa
    );
    let base = Zone::from_image(&nzf::parse(FULL).unwrap()).unwrap();
    let absent = build(
        2,
        "example.test.",
        2026091302,
        2026091301,
        &[("nothere.example.test.", 1, 60, vec![192, 0, 2, 1])],
        &[],
    );
    assert_eq!(
        base.apply(&nzf::parse(&absent).unwrap()).unwrap_err(),
        ZoneError::DeleteAbsent
    );
    let bad_sig = build(
        1,
        "example.test.",
        1,
        0,
        &[
            ("example.test.", 6, 60, soa(1)),
            ("example.test.", 46, 60, vec![0, 6]),
        ],
        &[],
    );
    assert_eq!(
        Zone::from_image(&nzf::parse(&bad_sig).unwrap()).unwrap_err(),
        ZoneError::BadRrsig
    );
    assert_eq!(
        base.apply(&nzf::parse(FULL).unwrap()).unwrap_err(),
        ZoneError::NotDelta
    );
    assert_eq!(
        Zone::from_image(&nzf::parse(DELTA).unwrap()).unwrap_err(),
        ZoneError::NotFull
    );
}

#[test]
fn nsec_and_nsec3_indexes() {
    use super::name::canon_key;
    let hash = [0x11u8; 20];
    let label = data_encoding::BASE32HEX_NOPAD
        .encode(&hash)
        .to_ascii_lowercase();
    let nsec3_owner = format!("{label}.example.test.");
    let z = build(
        1,
        "example.test.",
        1,
        0,
        &[
            ("example.test.", 6, 60, soa(1)),
            ("example.test.", 47, 60, w("m.example.test.")),
            ("example.test.", 51, 60, vec![1, 0, 0, 0]),
            ("m.example.test.", 47, 60, w("example.test.")),
            (
                "m.example.test.",
                46,
                60,
                [&[0u8, 47][..], &[0; 16], &w("example.test.")].concat(),
            ),
            (&nsec3_owner, 50, 60, vec![1, 0, 0, 0, 0]),
            ("sub.m.example.test.", 2, 60, w("ns.other.")),
            ("x.sub.m.example.test.", 1, 60, vec![192, 0, 2, 9]),
        ],
        &[],
    );
    let z = Zone::from_image(&nzf::parse(&z).unwrap()).unwrap();
    let key = |n: &str| {
        let mut k = Vec::new();
        canon_key(&w(n), &mut k);
        k
    };
    assert_eq!(
        &*z.nsec_covering(&key("b.example.test.")).unwrap().owner,
        &w("example.test.")[..]
    );
    assert_eq!(
        &*z.nsec_covering(&key("z.example.test.")).unwrap().owner,
        &w("m.example.test.")[..]
    );
    assert_eq!(
        &*z.nsec_covering(&key("m.example.test.")).unwrap().owner,
        &w("m.example.test.")[..]
    );
    assert_eq!(z.nsec3_param(), Some(&[1u8, 0, 0, 0][..]));
    assert!(z.nsec3_node(&hash).is_some());
    assert!(z.nsec3_covering(&[0x22; 20]).is_some());
    assert!(
        z.nsec3_covering(&[0x00; 20]).is_some(),
        "wraps to the last hash"
    );
    assert!(
        z.nsec3_covering(&hash).is_some(),
        "strictly less wraps to the last hash"
    );
    assert!(
        z.node(&w("m.example.test."))
            .unwrap()
            .get(47)
            .unwrap()
            .sigs
            .len()
            == 1
    );
    let below: Vec<_> = z
        .nodes_below(&key("m.example.test."))
        .map(|n| n.owner.to_vec())
        .collect();
    assert_eq!(
        below,
        vec![w("sub.m.example.test."), w("x.sub.m.example.test.")]
    );
    assert!(z.node(&w("x.sub.m.example.test.")).unwrap().flags & NODE_BELOW_CUT != 0);
    assert!(z.node(&w("m.example.test.")).unwrap().flags & NODE_BELOW_CUT == 0);
    assert_eq!(z.records_sorted().len(), 8);
}
