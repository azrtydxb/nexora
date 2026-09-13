use super::RpzState;
use super::manager::{RpzZoneConfig, zone_configs};
use super::transfer::*;
use super::tsig::*;
use crate::proto;
use crate::snapshot::DirBlobs;
use hickory_proto::op::{Message, OpCode, Query};
use hickory_proto::rr::{
    Name, RData, Record, RecordType,
    rdata::{CNAME, SOA},
};
use std::net::SocketAddr;
use std::sync::{Arc, Mutex};
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::TcpListener;
use zeroize::Zeroizing;

fn n(s: &str) -> Name {
    Name::from_ascii(s).unwrap()
}
fn key() -> TsigKey {
    TsigKey {
        name: n("rpz-key."),
        alg: TsigAlg::HmacSha256,
        secret: Zeroizing::new(vec![0x42; 32]),
    }
}
fn soa(serial: u32) -> Record {
    Record::from_rdata(
        n("rpz.test."),
        60,
        RData::SOA(SOA::new(
            n("ns.rpz.test."),
            n("h.rpz.test."),
            serial,
            2,
            1,
            30,
            60,
        )),
    )
}
fn block(name: &str) -> Record {
    Record::from_rdata(
        n(&format!("{name}.rpz.test.")),
        60,
        RData::CNAME(CNAME(Name::root())),
    )
}

/// Serves AXFR/IXFR/SOA over TCP from `zone` (serial, records); each AXFR is split into one message per record and TSIG-signed
/// on the first and last message only, exercising the unsigned-intermediate rule.
async fn primary(
    zone: Arc<Mutex<(u32, Vec<Record>)>>,
    signing: Option<TsigKey>,
) -> std::net::SocketAddr {
    let l = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = l.local_addr().unwrap();
    tokio::spawn(async move {
        loop {
            let (mut s, _) = l.accept().await.unwrap();
            let zone = zone.clone();
            let signing = signing.clone();
            tokio::spawn(async move {
                let len = s.read_u16().await.unwrap() as usize;
                let mut buf = vec![0u8; len];
                s.read_exact(&mut buf).await.unwrap();
                let req = Message::from_vec(&buf).unwrap();
                let request_mac = req_mac(&buf);
                let (serial, records) = zone.lock().unwrap().clone();
                let qt = req.queries[0].query_type();
                let mut bodies: Vec<Vec<Record>> = vec![];
                if qt == RecordType::SOA {
                    bodies.push(vec![soa(serial)]);
                } else {
                    bodies.push(vec![soa(serial)]);
                    for r in records {
                        bodies.push(vec![r]);
                    }
                    bodies.push(vec![soa(serial)]);
                }
                let mut prior = request_mac;
                let mut unsigned = Vec::new();
                let last = bodies.len() - 1;
                for (i, body) in bodies.into_iter().enumerate() {
                    let mut m = Message::response(req.metadata.id, OpCode::Query);
                    m.metadata.authoritative = true;
                    m.queries.push(Query::query(n("rpz.test."), qt));
                    m.answers = body;
                    let mut wire = m.to_vec().unwrap();
                    if let Some(k) = &signing {
                        if i == 0 || i == last {
                            prior = sign_response(&mut wire, k, now(), &prior, i == 0, &unsigned);
                            unsigned.clear();
                        } else {
                            unsigned.extend_from_slice(&wire);
                        }
                    }
                    s.write_u16(wire.len() as u16).await.unwrap();
                    s.write_all(&wire).await.unwrap();
                }
            });
        }
    });
    addr
}

fn now() -> u64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap()
        .as_secs()
}

/// MAC of the TSIG RR at the end of a signed request, empty when unsigned.
fn req_mac(wire: &[u8]) -> Vec<u8> {
    extract_mac(wire).unwrap_or_default()
}

#[test]
fn serial_arithmetic_rfc1982() {
    assert!(serial_gt(2, 1));
    assert!(serial_gt(0, 0xFFFF_FFFF));
    assert!(!serial_gt(1, 1));
    assert!(serial_gt(0x7FFF_FFFF, 0));
    assert!(
        !serial_gt(0x8000_0000, 0),
        "distance 2^31 is undefined and treated as not greater"
    );
    assert!(!serial_gt(1, 2));
}

#[test]
fn timers_respect_min_refresh() {
    assert_eq!(
        timers_from_soa(2, 1, 30, 60),
        Timers {
            refresh: 60,
            retry: 60,
            expire: 60
        }
    );
    assert_eq!(
        timers_from_soa(3600, 600, 86400, 60),
        Timers {
            refresh: 3600,
            retry: 600,
            expire: 86400
        }
    );
}

#[test]
fn tsig_sign_and_verify_detects_tampering_wrong_key_and_time() {
    let mut req = Message::query().to_vec().unwrap();
    let mac = sign_request(&mut req, &key(), 1_800_000_000);
    let mut resp = Message::response(0, OpCode::Query).to_vec().unwrap();
    sign_response(&mut resp, &key(), 1_800_000_000, &mac, true, &[]);
    let mut v = TsigVerifier::new(key(), mac.clone());
    assert_eq!(v.verify(&resp, 1_800_000_100), Ok(()));
    let mut tampered = resp.clone();
    tampered[3] ^= 0x01;
    assert_eq!(
        TsigVerifier::new(key(), mac.clone()).verify(&tampered, 1_800_000_100),
        Err(TsigError::BadSig)
    );
    let wrong = TsigKey {
        secret: Zeroizing::new(vec![0x43; 32]),
        ..key()
    };
    assert_eq!(
        TsigVerifier::new(wrong, mac.clone()).verify(&resp, 1_800_000_100),
        Err(TsigError::BadSig)
    );
    assert_eq!(
        TsigVerifier::new(key(), mac.clone()).verify(&resp, 1_800_000_301),
        Err(TsigError::BadTime)
    );
    let unsigned = Message::response(0, OpCode::Query).to_vec().unwrap();
    let mut v = TsigVerifier::new(key(), mac);
    assert_eq!(
        v.verify(&unsigned, 1_800_000_000),
        Err(TsigError::Missing),
        "first message must be signed"
    );
}

#[test]
fn ixfr_applies_deletions_and_additions() {
    let cur = ZoneData {
        serial: 1,
        records: vec![soa(1), block("a"), block("b")],
    };
    // IXFR: new SOA, old SOA, deleted..., new SOA, added..., new SOA
    let answers = vec![soa(2), soa(1), block("a"), soa(2), block("c"), soa(2)];
    let next = apply_ixfr(&cur, &answers).unwrap();
    assert_eq!(next.serial, 2);
    assert!(
        next.records.contains(&block("b"))
            && next.records.contains(&block("c"))
            && !next.records.contains(&block("a"))
    );
    // AXFR-style reply to an IXFR request replaces the zone
    let full = apply_ixfr(&cur, &[soa(3), block("z"), soa(3)]).unwrap();
    assert_eq!(full.records, vec![soa(3), block("z")]);
    assert!(
        apply_ixfr(&cur, &[soa(1)]).unwrap() == cur,
        "single SOA with current serial = up to date"
    );
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn axfr_with_tsig_then_incremental_update() {
    let zone = Arc::new(Mutex::new((5u32, vec![block("a"), block("b")])));
    let addr = primary(zone.clone(), Some(key())).await;
    let z = transfer(addr, &n("rpz.test."), None, Some(&key()))
        .await
        .unwrap();
    assert_eq!(z.serial, 5);
    assert_eq!(z.records.len(), 3);
    let unsigned_primary = primary(zone.clone(), None).await;
    assert!(
        transfer(unsigned_primary, &n("rpz.test."), None, Some(&key()))
            .await
            .unwrap_err()
            .contains("TSIG")
    );
    assert_eq!(
        soa_serial(addr, &n("rpz.test."), Some(&key()))
            .await
            .unwrap()
            .0,
        5
    );
    *zone.lock().unwrap() = (6, vec![block("a"), block("b"), block("c")]);
    let z2 = transfer(addr, &n("rpz.test."), Some(&z), Some(&key()))
        .await
        .unwrap();
    assert_eq!(z2.serial, 6);
    assert!(
        z2.records.contains(&block("c"))
            && z2.records.contains(&soa(6))
            && !z2.records.contains(&soa(5))
    );
}

fn transfer_snapshot(primary: SocketAddr, tsig: bool) -> proto::ConfigSnapshot {
    proto::ConfigSnapshot {
        rpz_zones: vec![proto::RpzZone {
            id: "z1".into(),
            name: "rpz.test.".into(),
            source: Some(proto::rpz_zone::Source::Transfer(
                proto::RpzTransferSource {
                    primary: primary.to_string(),
                    tsig_key_name: if tsig {
                        "rpz-key.".into()
                    } else {
                        String::new()
                    },
                    tsig_algorithm: if tsig {
                        proto::TsigAlgorithm::HmacSha256 as i32
                    } else {
                        0
                    },
                    min_refresh_seconds: 1,
                },
            )),
            policy_override: 0,
            refresh_nonce: 0,
        }],
        ..Default::default()
    }
}

fn resolution(s: &proto::ConfigSnapshot) -> Vec<RpzZoneConfig> {
    zone_configs(
        s,
        &DirBlobs {
            dir: "/nonexistent/nexora-test-blobs".into(),
        },
    )
    .unwrap()
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn manager_keeps_last_good_zone_when_primary_fails_and_reloads_from_disk() {
    let zone = Arc::new(Mutex::new((5u32, vec![block("a")])));
    let addr = primary(zone.clone(), None).await;
    let dir = tempfile::tempdir().unwrap();
    let state = RpzState::new(Some(dir.path()));
    state
        .manager
        .apply_config(&state, &resolution(&transfer_snapshot(addr, false)));
    assert!(
        state.manager.refresh_now(&state, "z1").await.unwrap(),
        "first transfer publishes"
    );
    assert_eq!(state.set.load().zones.len(), 1);
    assert!(dir.path().join("rpz/z1.zone").exists());
    assert!(metrics(&state).contains("nexora_rpz_zone_serial{zone=\"rpz.test.\"} 5"));
    let dead: SocketAddr = "127.0.0.1:1".parse().unwrap();
    state
        .manager
        .apply_config(&state, &resolution(&transfer_snapshot(dead, false)));
    assert!(state.manager.refresh_now(&state, "z1").await.is_err());
    assert_eq!(state.set.load().zones.len(), 1, "last good zone kept");
    let st = &state.manager.status()[0];
    assert!(!st.last_error.is_empty());
    assert_eq!(st.serial, 5);
    assert!(
        metrics(&state).contains("nexora_rpz_refresh_failures_total{zone=\"rpz.test.\"} 1"),
        "{}",
        metrics(&state)
    );
    let state2 = RpzState::new(Some(dir.path()));
    state2
        .manager
        .apply_config(&state2, &resolution(&transfer_snapshot(dead, false)));
    assert_eq!(
        state2.set.load().zones.len(),
        1,
        "persisted zone served after restart before any transfer"
    );
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn tsig_zone_waits_for_key_material_held_in_memory_only() {
    let zone = Arc::new(Mutex::new((9u32, vec![block("a")])));
    let addr = primary(zone.clone(), Some(key())).await;
    let dir = tempfile::tempdir().unwrap();
    let state = RpzState::new(Some(dir.path()));
    state
        .manager
        .apply_config(&state, &resolution(&transfer_snapshot(addr, true)));
    let err = state.manager.refresh_now(&state, "z1").await.unwrap_err();
    assert!(err.contains("tsig key material not received"), "{err}");
    state.set_tsig_keys(proto::RpzTsigKeys {
        keys: vec![proto::RpzTsigKey {
            zone_id: "z1".into(),
            key_name: "rpz-key.".into(),
            algorithm: proto::TsigAlgorithm::HmacSha256 as i32,
            secret: vec![0x42; 32],
        }],
    });
    assert!(state.manager.refresh_now(&state, "z1").await.unwrap());
    assert_eq!(state.manager.status()[0].serial, 9);
    for entry in walk(dir.path()) {
        let bytes = std::fs::read(&entry).unwrap();
        assert!(
            !bytes.windows(32).any(|w| w == [0x42u8; 32]),
            "{} holds the TSIG secret",
            entry.display()
        );
    }
}

fn walk(dir: &std::path::Path) -> Vec<std::path::PathBuf> {
    let mut out = Vec::new();
    for e in std::fs::read_dir(dir).unwrap() {
        let p = e.unwrap().path();
        if p.is_dir() {
            out.extend(walk(&p));
        } else {
            out.push(p);
        }
    }
    out
}

fn metrics(state: &RpzState) -> String {
    let mut reg = prometheus_client::registry::Registry::default();
    state.manager.register_metrics(&mut reg);
    let mut out = String::new();
    prometheus_client::encoding::text::encode(&mut out, &reg).unwrap();
    out
}

#[test]
fn file_zone_blob_is_published_on_apply_and_bad_zones_reject_the_snapshot() {
    use sha2::{Digest, Sha256};
    let dir = tempfile::tempdir().unwrap();
    let put = |text: &str| {
        let bytes = zstd::encode_all(text.as_bytes(), 3).unwrap();
        let sha = hex::encode(Sha256::digest(&bytes));
        std::fs::write(dir.path().join(&sha), &bytes).unwrap();
        proto::BlobRef {
            sha256: sha,
            size: bytes.len() as u64,
            name: "rpz.file.".into(),
        }
    };
    let snap = |id: &str, blob: proto::BlobRef| proto::ConfigSnapshot {
        rpz_zones: vec![proto::RpzZone {
            id: id.into(),
            name: "rpz.file.".into(),
            source: Some(proto::rpz_zone::Source::File(proto::RpzFileSource {
                blob: Some(blob),
            })),
            policy_override: 0,
            refresh_nonce: 0,
        }],
        ..Default::default()
    };
    let blobs = DirBlobs {
        dir: dir.path().into(),
    };
    let good = put("$TTL 60\n@ SOA ns h 3 60 60 60 60\nbad.example CNAME .\n");
    let state = RpzState::new(None);
    state.manager.apply_config(
        &state,
        &zone_configs(&snap("f1", good.clone()), &blobs).unwrap(),
    );
    assert_eq!(state.set.load().zones[0].serial, 3);
    assert!(
        state
            .query_triggers
            .load(std::sync::atomic::Ordering::Relaxed)
    );
    assert_eq!(state.manager.status()[0].id, "f1");
    let bad = put("$INCLUDE /etc/passwd\n");
    assert_eq!(
        zone_configs(&snap("f1", bad), &blobs).err().unwrap(),
        "rpz_zones[0]: $INCLUDE is not allowed in RPZ zones"
    );
    assert_eq!(
        zone_configs(&snap("../x", good), &blobs).err().unwrap(),
        "rpz_zones[0]: id must be letters, digits and '-'"
    );
    state.manager.apply_config(&state, &[]);
    assert!(
        state.set.load().zones.is_empty()
            && !state
                .query_triggers
                .load(std::sync::atomic::Ordering::Relaxed)
    );
}
