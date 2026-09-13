use super::loader::{LoadError, load, validate};
use super::set::AuthSet;
use super::zone_tests::{DELTA, FULL};
use crate::proto;
use crate::snapshot::{BlobSource, SnapshotError};
use sha2::{Digest, Sha256};
use std::cell::RefCell;
use std::collections::HashMap;

struct MapBlobs {
    blobs: HashMap<String, Vec<u8>>,
    read: RefCell<Vec<String>>,
}

impl MapBlobs {
    fn new(raws: &[&[u8]]) -> (Self, Vec<proto::BlobRef>) {
        let mut blobs = HashMap::new();
        let mut refs = Vec::new();
        for raw in raws {
            let data = zstd::bulk::compress(raw, 3).unwrap();
            let sha = hex::encode(Sha256::digest(&data));
            refs.push(proto::BlobRef {
                sha256: sha.clone(),
                size: data.len() as u64,
                name: String::new(),
            });
            blobs.insert(sha, data);
        }
        (
            MapBlobs {
                blobs,
                read: RefCell::new(Vec::new()),
            },
            refs,
        )
    }
}

impl BlobSource for MapBlobs {
    fn read(&self, r: &proto::BlobRef) -> Result<Vec<u8>, SnapshotError> {
        self.read.borrow_mut().push(r.sha256.clone());
        self.blobs
            .get(&r.sha256)
            .cloned()
            .ok_or_else(|| SnapshotError::Blob {
                sha256: r.sha256.clone(),
                reason: "blob not present".into(),
            })
    }
}

fn zone_msg(
    serial: u32,
    image: &proto::BlobRef,
    deltas: Vec<proto::ZoneDelta>,
    offset: u32,
) -> proto::AuthZone {
    proto::AuthZone {
        name: "example.test.".into(),
        kind: proto::AuthZoneKind::Primary as i32,
        serial,
        image: Some(image.clone()),
        image_serial: 2026091301,
        deltas,
        image_delta_offset: offset,
        ..Default::default()
    }
}

#[test]
fn second_version_is_applied_as_a_delta_without_reading_the_image() {
    let (blobs, refs) = MapBlobs::new(&[FULL, DELTA]);
    let v1 = vec![zone_msg(2026091301, &refs[0], vec![], 0)];
    validate(&v1).unwrap();
    let first = load(&AuthSet::empty(), &v1, &blobs).unwrap();
    assert_eq!((first.counts.full, first.counts.delta), (1, 0));
    assert_eq!(first.changed.len(), 1);

    let d = proto::ZoneDelta {
        from_serial: 2026091301,
        to_serial: 2026091302,
        blob: Some(refs[1].clone()),
    };
    let v2 = vec![zone_msg(2026091302, &refs[0], vec![d], 0)];
    validate(&v2).unwrap();
    blobs.read.borrow_mut().clear();
    let second = load(&first.set, &v2, &blobs).unwrap();
    assert_eq!((second.counts.full, second.counts.delta), (0, 1));
    assert_eq!(*blobs.read.borrow(), vec![refs[1].sha256.clone()]);
    let z = second.set.get(b"\x07example\x04test\x00").unwrap();
    assert_eq!(z.serial(), 2026091302);
    assert_eq!(z.deltas.len(), 1, "IXFR history retained");

    let third = load(&second.set, &v2, &blobs).unwrap();
    assert_eq!(third.counts.reused, 1);
    assert!(third.changed.is_empty());
}

#[test]
fn fresh_engine_builds_from_image_plus_deltas() {
    let (blobs, refs) = MapBlobs::new(&[FULL, DELTA]);
    let d = proto::ZoneDelta {
        from_serial: 2026091301,
        to_serial: 2026091302,
        blob: Some(refs[1].clone()),
    };
    let loaded = load(
        &AuthSet::empty(),
        &[zone_msg(2026091302, &refs[0], vec![d], 0)],
        &blobs,
    )
    .unwrap();
    assert_eq!(loaded.counts.full, 1);
    assert_eq!(
        loaded.set.get(b"\x07example\x04test\x00").unwrap().serial(),
        2026091302
    );
}

#[test]
fn missing_blob_and_broken_chain_are_rejected() {
    let (blobs, mut refs) = MapBlobs::new(&[FULL]);
    let good = refs[0].clone();
    refs[0].sha256 = "0".repeat(64);
    assert!(matches!(
        load(
            &AuthSet::empty(),
            &[zone_msg(2026091301, &refs[0], vec![], 0)],
            &blobs
        ),
        Err(LoadError::Blob(_))
    ));
    let gap = proto::ZoneDelta {
        from_serial: 2026091300,
        to_serial: 2026091302,
        blob: Some(good.clone()),
    };
    assert!(validate(&[zone_msg(2026091302, &good, vec![gap], 0)]).is_err());
    let mut dup = vec![zone_msg(2026091301, &good, vec![], 0)];
    dup.push(dup[0].clone());
    assert!(validate(&dup).is_err(), "duplicate zone names");
}

#[test]
fn transfer_policy_and_notify_targets_follow_the_snapshot_without_a_serial_change() {
    let (blobs, refs) = MapBlobs::new(&[FULL]);
    let v1 = vec![zone_msg(2026091301, &refs[0], vec![], 0)];
    let first = load(&AuthSet::empty(), &v1, &blobs).unwrap();
    let z1 = first.set.get(b"\x07example\x04test\x00").unwrap().clone();
    assert!(z1.transfer_allow.is_empty() && z1.notify.is_empty());

    let mut v2 = v1.clone();
    v2[0].transfer = Some(proto::TransferPolicy {
        allow_cidrs: vec!["127.0.0.1/32".into()],
        tsig_key: "xfr-key.".into(),
    });
    v2[0].notify = vec![proto::NotifyTarget {
        address: "127.0.0.1:5300".into(),
        tsig_key: String::new(),
    }];
    validate(&v2).unwrap();
    let second = load(&first.set, &v2, &blobs).unwrap();
    assert_eq!(second.counts.reused, 1);
    assert!(second.changed.is_empty(), "same serial: nothing to notify");
    let z2 = second.set.get(b"\x07example\x04test\x00").unwrap();
    assert_eq!(
        z2.transfer_allow,
        vec!["127.0.0.1/32".parse::<ipnet::IpNet>().unwrap()]
    );
    assert_eq!(z2.transfer_key.as_deref(), Some(&b"\x07xfr-key\x00"[..]));
    assert_eq!(z2.notify, vec![("127.0.0.1:5300".parse().unwrap(), None)]);

    let mut bad = v2.clone();
    bad[0].notify[0].tsig_key = "no-dot".into();
    assert!(validate(&bad).is_err());

    let mut v3 = v2.clone();
    v3[0].kind = proto::AuthZoneKind::Secondary as i32;
    v3[0].primaries = vec!["192.0.2.1:53".into(), "192.0.2.2:53".into()];
    v3[0].primary_tsig_keys = vec!["XFR-key.".into(), String::new()];
    validate(&v3).unwrap();
    let third = load(&second.set, &v3, &blobs).unwrap();
    let z3 = third.set.get(b"\x07example\x04test\x00").unwrap();
    assert_eq!(
        z3.primaries,
        vec![
            (
                "192.0.2.1:53".parse().unwrap(),
                Some(b"\x07xfr-key\x00".to_vec().into_boxed_slice())
            ),
            ("192.0.2.2:53".parse().unwrap(), None),
        ]
    );
    let mut uneven = v3.clone();
    uneven[0].primary_tsig_keys.pop();
    assert!(validate(&uneven).is_err(), "one key name per primary");
}
