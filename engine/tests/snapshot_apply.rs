use arc_swap::ArcSwap;
use nexora_engine::acl::Acl;
use nexora_engine::filter::{FilterDecision, domain_to_wire};
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
            ..Default::default()
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

/// Two engines on one state directory (rolling update with maxSurge): concurrent persists never
/// leave a partial file, and a process persisting an older version never replaces a newer one.
#[test]
fn engines_sharing_a_state_dir_keep_the_newest_complete_snapshot() {
    let dir = tempfile::tempdir().unwrap();
    let barrier = Arc::new(std::sync::Barrier::new(2));
    let writers: Vec<_> = [0u64, 1]
        .into_iter()
        .map(|w| {
            let (state, barrier) = (dir.path().to_path_buf(), barrier.clone());
            std::thread::spawn(move || {
                barrier.wait();
                for v in 1..=200u64 {
                    // One process runs a version behind the other.
                    snapshot::persist(&state, &base(v * 2 + w)).unwrap();
                    let on_disk = snapshot::load(&state).unwrap().unwrap();
                    assert!(
                        on_disk.version >= v * 2 + w,
                        "a newer snapshot was replaced"
                    );
                }
            })
        })
        .collect();
    for w in writers {
        w.join().unwrap();
    }
    assert_eq!(snapshot::load(dir.path()).unwrap().unwrap(), base(401));
    snapshot::persist(dir.path(), &base(7)).unwrap();
    assert_eq!(snapshot::load(dir.path()).unwrap().unwrap().version, 401);
    let stray: Vec<_> = std::fs::read_dir(dir.path())
        .unwrap()
        .map(|e| e.unwrap().file_name().to_string_lossy().into_owned())
        .filter(|n| n.ends_with(".tmp"))
        .collect();
    assert!(stray.is_empty(), "temporary files left: {stray:?}");
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

/// Caches one positive answer in the current runtime's cache and returns the entry count.
fn cache_one_answer(cur: &ArcSwap<Runtime>) -> u64 {
    use hickory_proto::op::{Message, MessageType, OpCode, Query};
    use hickory_proto::rr::{Name, RData, Record, RecordType, rdata::A};
    use hickory_proto::serialize::binary::{BinDecodable, BinEncodable};
    use nexora_engine::cache::CacheKey;

    let mut m = Message::new(7, MessageType::Query, OpCode::Query);
    m.add_query(Query::query(
        Name::from_ascii("cached.example.").unwrap(),
        RecordType::A,
    ));
    let query = m.to_bytes().unwrap();
    let mut r = Message::from_bytes(&query).unwrap();
    r.metadata.message_type = MessageType::Response;
    r.add_answer(Record::from_rdata(
        Name::from_ascii("cached.example.").unwrap(),
        300,
        RData::A(A::new(192, 0, 2, 1)),
    ));
    let v = nexora_engine::wire::parse_query(&query).unwrap();
    let rt = cur.load();
    rt.cache.insert(
        CacheKey::from_query(&v),
        &r.to_bytes().unwrap(),
        &v,
        nexora_engine::clock::now_secs(),
    );
    rt.cache.entries()
}

#[test]
fn upstream_changes_clear_cached_answers() {
    // Breaks if answers resolved through upstreams a snapshot no longer lists (an engine moved
    // into an engine group with other upstreams) keep being served from the cache.
    let dir = tempfile::tempdir().unwrap();
    let cur = ArcSwap::from_pointee(Runtime::initial());
    let blobs = DirBlobs {
        dir: dir.path().to_path_buf(),
    };
    snapshot::apply(&cur, base(1), &blobs, None);
    assert_eq!(cache_one_answer(&cur), 1);
    snapshot::apply(&cur, base(2), &blobs, None);
    assert_eq!(
        cur.load().cache.entries(),
        1,
        "unchanged upstreams keep answers"
    );

    let mut s = base(3);
    s.upstreams[0].address = "127.0.0.1:5354".into();
    snapshot::apply(&cur, s, &blobs, None);
    assert_eq!(cur.load().version, 3);
    assert_eq!(
        cur.load().cache.entries(),
        0,
        "new upstream address clears answers"
    );

    let mut s = base(4);
    s.upstreams[0].address = "127.0.0.1:5354".into();
    assert_eq!(cache_one_answer(&cur), 1);
    s.resolver.as_mut().unwrap().strategy = UpstreamStrategy::Fastest as i32;
    snapshot::apply(&cur, s, &blobs, None);
    assert_eq!(cur.load().cache.entries(), 0, "new strategy clears answers");
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
    use nexora_engine::filter::index::{FilterIndex, IndexOptions, ListInput, ListKind};
    let block = b"ads.example\ntracker.example.net\nnot a domain\n-bad-.example\n".to_vec();
    let allow = b"good.ads.example\n".to_vec();
    let inputs = [
        ListInput {
            id: "b",
            category: "",
            category_slot: 0,
            kind: ListKind::Block,
            text: &block,
        },
        ListInput {
            id: "a",
            category: "",
            category_slot: 0,
            kind: ListKind::Allow,
            text: &allow,
        },
    ];
    let index = Arc::new(FilterIndex::build(&inputs, &IndexOptions::new(16 << 20)).unwrap());
    assert_eq!((index.entries(), index.invalid_lines()), (3, 2));
    let f = index.view(&[0], &[1]);
    let w = |s: &str| domain_to_wire(s.as_bytes()).unwrap();
    assert!(matches!(
        f.decide(&w("ads.example")),
        FilterDecision::Blocked(_)
    ));
    assert!(matches!(
        f.decide(&w("x.y.ads.example")),
        FilterDecision::Blocked(_)
    ));
    assert_eq!(f.decide(&w("good.ads.example")), FilterDecision::Allowed);
    assert_eq!(
        f.decide(&w("sub.good.ads.example")),
        FilterDecision::Allowed
    );
    assert_eq!(f.decide(&w("example")), FilterDecision::None);
    assert_eq!(f.decide(&w("notads.example")), FilterDecision::None);
    let target = NameKey::from_wire_lowercase(&w("cdn.tracker.example.net")).unwrap();
    assert!(f.cloaked(&[target]).is_some());
    assert!(
        Arc::new(FilterIndex::empty())
            .view(&[], &[])
            .cloaked(&[target])
            .is_none()
    );
}

fn with_lists(version: u64, dir: &std::path::Path, group_allow: &[&str]) -> ConfigSnapshot {
    let mut s = base(version);
    let ads = blob(dir, "ads.example\ntracker.example.net\n");
    let ads_ref = FilterListRef {
        list_id: "ads".into(),
        category: "ads-tracking".into(),
        position: 1,
        blob: Some(ads.clone()),
    };
    let f = s.filter.as_mut().unwrap();
    f.blocklists = vec![ads.clone()];
    f.blocklist_refs = vec![ads_ref.clone()];
    s.policy_groups = vec![PolicyGroup {
        id: "kids".into(),
        name: "kids".into(),
        cidrs: vec!["127.0.0.2/32".into()],
        blocklists: vec![ads],
        blocklist_refs: vec![ads_ref],
        allowlist: group_allow.iter().map(|x| x.to_string()).collect(),
        ..Default::default()
    }];
    s
}

#[test]
fn unchanged_lists_reuse_index() {
    let dir = tempfile::tempdir().unwrap();
    let cur = ArcSwap::from_pointee(Runtime::initial());
    let blobs = DirBlobs {
        dir: dir.path().to_path_buf(),
    };
    assert!(matches!(
        snapshot::apply(
            &cur,
            with_lists(1, dir.path(), &["ok.ads.example"]),
            &blobs,
            None
        ),
        ApplyOutcome::Applied { .. }
    ));
    let first = cur.load_full();
    let blocked = |rt: &Runtime| {
        rt.policy
            .select("127.0.0.1".parse().unwrap())
            .0
            .filter()
            .decide(&domain_to_wire(b"x.ads.example").unwrap())
    };
    assert!(
        matches!(blocked(&first), FilterDecision::Blocked(_)),
        "positive path: the list blocks"
    );
    assert!(first.filter_index.entries() >= 3);

    let mut same_lists = with_lists(2, dir.path(), &["ok.ads.example"]);
    same_lists.cache.as_mut().unwrap().max_bytes = 8 << 20;
    same_lists.upstreams[0].timeout_ms = 300;
    assert!(matches!(
        snapshot::apply(&cur, same_lists, &blobs, None),
        ApplyOutcome::Applied { .. }
    ));
    let second = cur.load_full();
    assert!(
        Arc::ptr_eq(&first.filter_index, &second.filter_index),
        "no list changed: the index is reused"
    );
    assert_eq!(
        first.filter_index.generation(),
        second.filter_index.generation()
    );

    assert!(matches!(
        snapshot::apply(
            &cur,
            with_lists(3, dir.path(), &["other.ads.example"]),
            &blobs,
            None
        ),
        ApplyOutcome::Applied { .. }
    ));
    let third = cur.load_full();
    assert!(
        !Arc::ptr_eq(&second.filter_index, &third.filter_index),
        "an allowlist edit rebuilds the index"
    );
    assert!(third.filter_index.generation() > second.filter_index.generation());
}

#[test]
fn filter_index_over_cap_rejects_snapshot_and_keeps_previous() {
    let dir = tempfile::tempdir().unwrap();
    let cur = ArcSwap::from_pointee(Runtime::initial());
    let blobs = DirBlobs {
        dir: dir.path().to_path_buf(),
    };
    assert!(matches!(
        snapshot::apply(&cur, with_lists(1, dir.path(), &[]), &blobs, None),
        ApplyOutcome::Applied { .. }
    ));
    let mut capped = with_lists(2, dir.path(), &["new.example"]);
    // Below the two 128-octet blocks of the smallest index.
    capped.filter_index_max_bytes = 256;
    let reason = outcome_reason(snapshot::apply(&cur, capped, &blobs, None));
    assert!(reason.contains("above the cap of 256 bytes"), "{reason}");
    let rt = cur.load();
    assert_eq!(rt.version, 1);
    let (p, _) = rt.policy.select("127.0.0.2".parse().unwrap());
    assert!(
        matches!(
            p.filter().decide(&domain_to_wire(b"ads.example").unwrap()),
            FilterDecision::Blocked(_)
        ),
        "previous index still active"
    );
}

#[test]
fn filter_list_refs_are_validated() {
    let dir = tempfile::tempdir().unwrap();
    let cur = ArcSwap::from_pointee(Runtime::initial());
    let blobs = DirBlobs {
        dir: dir.path().to_path_buf(),
    };
    let good = blob(dir.path(), "ads.example\n");
    let mut s = base(1);
    s.filter.as_mut().unwrap().blocklist_refs = vec![FilterListRef {
        list_id: "ads".into(),
        category: "ads-tracking".into(),
        position: 1,
        blob: Some(good.clone()),
    }];
    assert!(matches!(
        snapshot::apply(&cur, s, &blobs, None),
        ApplyOutcome::Applied { version: 1, .. }
    ));
    let mut bad = base(2);
    bad.filter.as_mut().unwrap().blocklist_refs = vec![FilterListRef {
        list_id: "ads".into(),
        category: "ads-tracking".into(),
        position: 1,
        blob: Some(BlobRef {
            sha256: "XYZ".into(),
            ..good
        }),
    }];
    assert_eq!(
        outcome_reason(snapshot::apply(&cur, bad, &blobs, None)),
        "invalid snapshot: sha256 XYZ must be 64 lowercase hex"
    );
    assert_eq!(cur.load().version, 1);
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

/// Catches: the build memory pre-check removed or skipped (the rebuild would run into the limit,
/// here the snapshot would apply), page cache (`inactive_file`) counted as used memory, a rejected
/// build replacing the previous runtime, and a limit reader that rejects every build.
#[test]
fn filter_rebuild_over_the_memory_limit_is_rejected_before_building() {
    use nexora_engine::filter::Verdict;
    use nexora_engine::filter::memory::BuildMemory;
    let dir = tempfile::tempdir().unwrap();
    let blobs = DirBlobs {
        dir: dir.path().to_path_buf(),
    };
    let cur = ArcSwap::from_pointee(Runtime::initial());
    let mut s = base(1);
    s.filter.as_mut().unwrap().blocklists = vec![blob(dir.path(), "old.example\n")];
    assert!(matches!(
        snapshot::apply(&cur, s, &blobs, None),
        ApplyOutcome::Applied { .. }
    ));
    let blocked = |name: &str| {
        let rt = cur.load();
        let wire = domain_to_wire(name.as_bytes()).unwrap();
        matches!(
            rt.policy
                .select("127.0.0.1".parse().unwrap())
                .0
                .check(&wire),
            Verdict::Blocked(_)
        )
    };
    assert!(blocked("old.example"));

    // 200,000 names: 4.4 MB of text, whose 4.8 MB of name records plus per-thread build slack
    // (at least 20 MB) do not fit the 16 MB left; the finished 5 MB index is far below the cap.
    let names: String = (0..200_000)
        .map(|i| format!("host{i}.big.example\n"))
        .collect();
    let big = blob(dir.path(), &names);
    let snap = |version| {
        let mut s = base(version);
        s.filter.as_mut().unwrap().blocklists = vec![big.clone()];
        s
    };
    let cgroup = tempfile::tempdir().unwrap();
    let limit: u64 = 256 << 20;
    let margin = BuildMemory::margin(limit);
    let set_usage = |working_set: u64| {
        let inactive_file: u64 = 64 << 20;
        std::fs::write(cgroup.path().join("memory.max"), format!("{limit}\n")).unwrap();
        std::fs::write(
            cgroup.path().join("memory.current"),
            format!("{}\n", working_set + inactive_file),
        )
        .unwrap();
        std::fs::write(
            cgroup.path().join("memory.stat"),
            format!("anon {working_set}\nfile {inactive_file}\ninactive_file {inactive_file}\n"),
        )
        .unwrap();
    };
    set_usage(limit - margin - (16 << 20));
    let memory = BuildMemory::cgroup(cgroup.path());
    let reason = outcome_reason(snapshot::apply_with(&cur, snap(2), &blobs, None, &memory));
    assert!(
        reason.contains("filter index build stopped before records")
            && reason.contains(&format!("memory limit of {limit} bytes")),
        "{reason}"
    );
    assert_eq!(cur.load().version, 1, "the previous runtime stays applied");
    assert!(blocked("old.example") && !blocked("host7.big.example"));

    // With 80 MB left the same snapshot applies; counting the 64 MB of page cache would leave 16.
    set_usage(limit - margin - (80 << 20));
    assert!(matches!(
        snapshot::apply_with(&cur, snap(3), &blobs, None, &memory),
        ApplyOutcome::Applied { version: 3, .. }
    ));
    assert!(blocked("host7.big.example") && !blocked("old.example"));
}
