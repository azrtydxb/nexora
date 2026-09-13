use arc_swap::ArcSwap;
use nexora_engine::acl::Acl;
use nexora_engine::filter::{BlockMode, FilterDecision, FilterSet, domain_to_wire};
use nexora_engine::proto::*;
use nexora_engine::runtime::Runtime;
use nexora_engine::snapshot::{self, ApplyOutcome, DirBlobs};
use nexora_engine::wire::NameKey;
use sha2::{Digest, Sha256};
use std::sync::Arc;

fn base(version: u64) -> ConfigSnapshot {
    ConfigSnapshot {
        version,
        resolver: Some(ResolverConfig {
            strategy: UpstreamStrategy::Ordered as i32,
        }),
        cache: Some(CacheConfig {
            max_bytes: 4 << 20,
            min_ttl: 0,
            max_ttl: 86400,
            negative_max_ttl: 3600,
            stale_window: 60,
        }),
        upstreams: vec![Upstream {
            id: "u1".into(),
            name: "fx".into(),
            protocol: UpstreamProtocol::Udp as i32,
            address: "127.0.0.1:5353".into(),
            timeout_ms: 250,
            ..Default::default()
        }],
        acl_allow_cidrs: vec!["127.0.0.0/8".into(), "::1/128".into()],
        filter: Some(FilterConfig {
            block_mode: BlockMode::NullIp as i32,
            block_ttl: 60,
            ..Default::default()
        }),
        telemetry: Some(TelemetryConfig::default()),
        ..Default::default()
    }
}
fn blob(dir: &std::path::Path, text: &str) -> BlobRef {
    let z = zstd::encode_all(text.as_bytes(), 3).unwrap();
    let sha = hex::encode(Sha256::digest(&z));
    std::fs::write(dir.join(&sha), &z).unwrap();
    BlobRef {
        sha256: sha,
        size: z.len() as u64,
        name: "list".into(),
    }
}
fn outcome_reason(o: ApplyOutcome) -> String {
    match o {
        ApplyOutcome::Rejected { reason, .. } => reason,
        other => panic!("expected rejection, got {other:?}"),
    }
}

#[test]
fn valid_snapshot_applies_persists_and_reloads() {
    let dir = tempfile::tempdir().unwrap();
    let cur = ArcSwap::from_pointee(Runtime::initial());
    let blobs = DirBlobs {
        dir: dir.path().to_path_buf(),
    };
    assert_eq!(
        snapshot::apply(&cur, base(1), &blobs, Some(dir.path())),
        ApplyOutcome::Applied {
            version: 1,
            persist_error: None
        }
    );
    assert_eq!(cur.load().version, 1);
    assert!(dir.path().join("snapshot.binpb").exists());
    assert!(!dir.path().join("snapshot.binpb.tmp").exists());
    assert_eq!(snapshot::load(dir.path()).unwrap().unwrap(), base(1));
}

#[test]
fn invalid_snapshots_are_rejected_and_previous_runtime_kept() {
    let dir = tempfile::tempdir().unwrap();
    let cur = ArcSwap::from_pointee(Runtime::initial());
    let blobs = DirBlobs {
        dir: dir.path().to_path_buf(),
    };
    assert!(matches!(
        snapshot::apply(&cur, base(5), &blobs, None),
        ApplyOutcome::Applied { .. }
    ));
    let held = cur.load_full();

    assert!(outcome_reason(snapshot::apply(&cur, base(5), &blobs, None)).contains("version"));
    let mut s = base(6);
    s.acl_allow_cidrs.push("10.0.0.300/8".into());
    assert!(outcome_reason(snapshot::apply(&cur, s, &blobs, None)).contains("cidr"));
    let mut s = base(6);
    s.cache.as_mut().unwrap().max_bytes = 1048575;
    assert!(outcome_reason(snapshot::apply(&cur, s, &blobs, None)).contains("max_bytes"));
    let mut s = base(6);
    s.upstreams[0].timeout_ms = 49;
    assert!(outcome_reason(snapshot::apply(&cur, s, &blobs, None)).contains("timeout"));
    let mut s = base(6);
    s.upstreams[0].timeout_ms = 5001;
    assert!(outcome_reason(snapshot::apply(&cur, s, &blobs, None)).contains("timeout"));
    let mut s = base(6);
    s.upstreams[0] = Upstream {
        id: "d".into(),
        protocol: UpstreamProtocol::Doh as i32,
        doh_url: "http://x/dns-query".into(),
        timeout_ms: 250,
        ..Default::default()
    };
    assert!(outcome_reason(snapshot::apply(&cur, s, &blobs, None)).contains("https"));
    let mut s = base(6);
    s.upstreams[0].address = "not-an-address".into();
    assert!(outcome_reason(snapshot::apply(&cur, s, &blobs, None)).contains("address"));
    let mut s = base(6);
    s.filter.as_mut().unwrap().blocklists.push(BlobRef {
        sha256: "abc".into(),
        size: 1,
        name: "bad".into(),
    });
    assert!(outcome_reason(snapshot::apply(&cur, s, &blobs, None)).contains("hex"));
    let mut s = base(6);
    s.filter.as_mut().unwrap().blocklists.push(BlobRef {
        sha256: "a".repeat(64),
        size: 1,
        name: "missing".into(),
    });
    assert!(outcome_reason(snapshot::apply(&cur, s, &blobs, None)).contains("blob"));
    let mut s = base(6);
    let mut r = blob(dir.path(), "ads.example\n");
    std::fs::write(dir.path().join(&r.sha256), b"tampered").unwrap();
    r.size = 8;
    s.filter.as_mut().unwrap().blocklists.push(r);
    assert!(outcome_reason(snapshot::apply(&cur, s, &blobs, None)).contains("sha256"));

    assert_eq!(cur.load().version, 5, "previous runtime still serving");
    assert!(Arc::ptr_eq(&held, &cur.load_full()));
}

#[test]
fn in_flight_holders_keep_old_runtime_and_cache_survives_unchanged_settings() {
    let dir = tempfile::tempdir().unwrap();
    let cur = ArcSwap::from_pointee(Runtime::initial());
    let blobs = DirBlobs {
        dir: dir.path().to_path_buf(),
    };
    snapshot::apply(&cur, base(1), &blobs, None);
    let old = cur.load_full();
    snapshot::apply(&cur, base(2), &blobs, None);
    assert_eq!(old.version, 1);
    assert!(
        Arc::ptr_eq(&old.cache, &cur.load().cache),
        "same cache settings keep the cache"
    );
    let mut s = base(3);
    s.cache.as_mut().unwrap().max_bytes = 8 << 20;
    snapshot::apply(&cur, s, &blobs, None);
    assert!(!Arc::ptr_eq(&old.cache, &cur.load().cache));
}

#[test]
fn persist_failure_still_applies_and_reports() {
    let dir = tempfile::tempdir().unwrap();
    let not_a_dir = dir.path().join("file");
    std::fs::write(&not_a_dir, b"x").unwrap();
    let cur = ArcSwap::from_pointee(Runtime::initial());
    match snapshot::apply(
        &cur,
        base(1),
        &DirBlobs {
            dir: dir.path().to_path_buf(),
        },
        Some(&not_a_dir),
    ) {
        ApplyOutcome::Applied {
            version: 1,
            persist_error: Some(e),
        } => assert!(!e.is_empty()),
        other => panic!("{other:?}"),
    }
    assert_eq!(cur.load().version, 1);
}

#[test]
fn filter_subdomains_allowlist_invalid_lines_and_cloaking() {
    let block = b"ads.example\ntracker.example.net\nnot a domain\n-bad-.example\n".to_vec();
    let allow = b"good.ads.example\n".to_vec();
    let (f, stats) = FilterSet::build(&[block], &[allow], BlockMode::NxDomain, 60);
    assert_eq!(stats.entries, 3);
    assert_eq!(stats.invalid_lines, 2);
    let w = |s: &str| domain_to_wire(s.as_bytes()).unwrap();
    assert_eq!(f.decide(&w("ads.example")), FilterDecision::Blocked);
    assert_eq!(f.decide(&w("x.y.ads.example")), FilterDecision::Blocked);
    assert_eq!(f.decide(&w("good.ads.example")), FilterDecision::Allowed);
    assert_eq!(
        f.decide(&w("sub.good.ads.example")),
        FilterDecision::Allowed
    );
    assert_eq!(f.decide(&w("example")), FilterDecision::None);
    assert_eq!(f.decide(&w("notads.example")), FilterDecision::None);
    let target = NameKey::from_wire_lowercase(&w("cdn.tracker.example.net")).unwrap();
    assert!(f.cloaked(&[target]));
    assert!(!FilterSet::empty().cloaked(&[target]));
}

#[test]
fn acl_matches_v4_v6_and_mapped() {
    let acl = Acl::parse(&["10.0.0.0/8".into(), "fc00::/7".into()]).unwrap();
    assert!(acl.allows("10.1.2.3".parse().unwrap()));
    assert!(acl.allows("::ffff:10.1.2.3".parse().unwrap()));
    assert!(acl.allows("fd00::1".parse().unwrap()));
    assert!(!acl.allows("192.168.1.1".parse().unwrap()));
    assert!(Acl::parse(&["300.0.0.0/8".into()]).is_err());
    assert!(
        !Acl::parse(&[])
            .unwrap()
            .allows("127.0.0.1".parse().unwrap())
    );
}
