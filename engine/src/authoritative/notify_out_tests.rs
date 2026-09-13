use super::notify_out::{NotifyJob, NotifyResult, send_notify};
use std::time::Duration;
use tokio::net::UdpSocket;

fn soa_rr() -> Vec<u8> {
    let mut v = b"\x07example\x04test\x00".to_vec();
    v.extend_from_slice(&[0, 6, 0, 1, 0, 0, 0x0e, 0x10]);
    let rdata = b"\x03ns1\x07example\x04test\x00\x01h\x07example\x04test\x00\x00\x00\x00\x07\x00\x00\x1c\x20\x00\x00\x0e\x10\x00\x12\x75\x00\x00\x00\x01\x2c";
    v.extend_from_slice(&(rdata.len() as u16).to_be_bytes());
    v.extend_from_slice(rdata);
    v
}

#[tokio::test]
async fn retries_until_the_secondary_answers() {
    let secondary = UdpSocket::bind("127.0.0.1:0").await.unwrap();
    let target = secondary.local_addr().unwrap();
    let server = tokio::spawn(async move {
        let mut buf = [0u8; 1500];
        let (_, _) = secondary.recv_from(&mut buf).await.unwrap(); // drop the first attempt
        let (n, from) = secondary.recv_from(&mut buf).await.unwrap();
        assert_eq!((buf[2] >> 3) & 0x0f, 4, "opcode NOTIFY");
        assert_ne!(buf[2] & 0x04, 0, "AA set");
        let mut resp = buf[..n].to_vec();
        resp[2] |= 0x80; // QR
        resp[6..8].copy_from_slice(&[0, 0]); // no answer section
        let qend = 12 + 14 + 4;
        resp.truncate(qend);
        secondary.send_to(&resp, from).await.unwrap();
    });
    let job = NotifyJob {
        zone: b"\x07example\x04test\x00".to_vec().into(),
        soa_rr: soa_rr(),
        target,
        key: None,
    };
    match send_notify(job, Duration::from_millis(50), 5).await {
        NotifyResult::Acked { attempts } => assert_eq!(attempts, 2),
        _ => panic!("expected ack on the second attempt"),
    }
    server.await.unwrap();
}

#[tokio::test]
async fn gives_up_after_the_attempt_budget() {
    let silent = UdpSocket::bind("127.0.0.1:0").await.unwrap();
    let job = NotifyJob {
        zone: b"\x07example\x04test\x00".to_vec().into(),
        soa_rr: soa_rr(),
        target: silent.local_addr().unwrap(),
        key: None,
    };
    assert!(matches!(
        send_notify(job, Duration::from_millis(20), 3).await,
        NotifyResult::Timeout
    ));
}

fn key_material() -> crate::proto::KeyMaterial {
    use sha2::{Digest, Sha256};
    crate::proto::KeyMaterial {
        tsig_keys: vec![crate::proto::TsigSecret {
            name: "notify-key.".into(),
            algorithm: crate::proto::TsigAlgorithm::HmacSha256 as i32,
            secret: Sha256::digest(b"fixture-notify-key").to_vec(),
        }],
    }
}

#[tokio::test]
async fn key_ring_wait_sees_later_key_material_and_is_bounded() {
    use crate::tsig::KeyRing;
    use std::sync::Arc;
    let ring = Arc::new(KeyRing::default());
    let waiter = {
        let ring = ring.clone();
        tokio::spawn(async move {
            ring.wait_for(b"\x0anotify-key\x00", Duration::from_secs(5))
                .await
        })
    };
    tokio::time::sleep(Duration::from_millis(50)).await;
    ring.apply(crate::proto::KeyMaterial::default());
    tokio::time::sleep(Duration::from_millis(20)).await;
    assert!(!waiter.is_finished(), "key material without the key");
    ring.apply(key_material());
    let key = waiter.await.unwrap().expect("key delivered while waiting");
    assert_eq!(key.name.to_ascii(), "notify-key.");

    let started = std::time::Instant::now();
    assert!(
        ring.wait_for(b"\x05other\x00", Duration::from_millis(50))
            .await
            .is_none()
    );
    assert!(started.elapsed() >= Duration::from_millis(50));
}

/// After a restart the persisted snapshot applies before the control stream delivers
/// `KeyMaterial`: the TSIG-signed NOTIFY waits for the key instead of counting `nokey`.
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn notify_needing_a_key_is_sent_once_key_material_arrives() {
    use crate::proto::{
        AuthZone, AuthZoneKind, BlobRef, CacheConfig, ConfigSnapshot, FilterConfig, NotifyTarget,
        ResolverConfig, TelemetryConfig,
    };
    use crate::server::Shared;
    use crate::snapshot::{ApplyOutcome, DirBlobs, apply};
    use crate::tsig::{KeyRing, Verified, sign_response, verify_request};
    use sha2::{Digest, Sha256};
    use std::sync::atomic::Ordering;

    let secondary = UdpSocket::bind("127.0.0.1:0").await.unwrap();
    let tmp = tempfile::tempdir().unwrap();
    let z = zstd::encode_all(super::zone_tests::FULL, 3).unwrap();
    let sha = hex::encode(Sha256::digest(&z));
    std::fs::write(tmp.path().join(&sha), &z).unwrap();
    let shared = Shared::new(1);
    let snap = ConfigSnapshot {
        version: 1,
        auth_zones: vec![AuthZone {
            name: "example.test.".into(),
            kind: AuthZoneKind::Primary as i32,
            serial: 2026091301,
            image: Some(BlobRef {
                sha256: sha,
                size: z.len() as u64,
                name: String::new(),
            }),
            image_serial: 2026091301,
            notify: vec![NotifyTarget {
                address: secondary.local_addr().unwrap().to_string(),
                tsig_key: "notify-key.".into(),
            }],
            ..Default::default()
        }],
        cache: Some(CacheConfig {
            max_bytes: 1 << 20,
            max_ttl: 86400,
            negative_max_ttl: 3600,
            ..Default::default()
        }),
        filter: Some(FilterConfig::default()),
        telemetry: Some(TelemetryConfig::default()),
        resolver: Some(ResolverConfig::default()),
        ..Default::default()
    };
    let outcome = apply(
        &shared.runtime,
        snap,
        &DirBlobs {
            dir: tmp.path().into(),
        },
        None,
    );
    assert!(
        matches!(outcome, ApplyOutcome::Applied { .. }),
        "{outcome:?}"
    );
    shared
        .auth
        .set_control_runtime(tokio::runtime::Handle::current());
    super::after_apply(&shared, &shared.runtime.load());
    let sent = &shared.metrics.auth.notify_sent;

    tokio::time::sleep(Duration::from_millis(200)).await;
    assert_eq!(
        sent[3].load(Ordering::Relaxed),
        0,
        "not counted nokey while waiting"
    );
    shared.auth.keyring.apply(key_material());

    let mut buf = [0u8; 1500];
    let (n, from) = tokio::time::timeout(Duration::from_secs(5), secondary.recv_from(&mut buf))
        .await
        .expect("NOTIFY sent after the key arrived")
        .unwrap();
    let ring = KeyRing::default();
    ring.apply(key_material());
    let now = crate::clock::unix_now().max(0) as u64;
    let Ok(Verified::Signed { key, request_mac }) = verify_request(&buf[..n], &ring, now) else {
        panic!("NOTIFY must be TSIG-signed with notify-key.");
    };
    let qend = 12 + b"\x07example\x04test\x00".len() + 4;
    let mut resp = buf[..qend].to_vec();
    resp[2] |= 0x80;
    resp[6..12].fill(0);
    sign_response(&mut resp, &key, now, &request_mac, true, &[]);
    secondary.send_to(&resp, from).await.unwrap();

    let deadline = std::time::Instant::now() + Duration::from_secs(5);
    while sent[0].load(Ordering::Relaxed) == 0 {
        assert!(std::time::Instant::now() < deadline, "NOTIFY not acked");
        tokio::time::sleep(Duration::from_millis(20)).await;
    }
    assert_eq!(sent[3].load(Ordering::Relaxed), 0);
}
