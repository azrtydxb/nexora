use std::alloc::{GlobalAlloc, Layout, System};
use std::cell::Cell;

// Only allocations made by a thread that armed itself count, on that thread: libtest's main
// thread and the other tests allocate concurrently and must not fail (or reset) the guard.
struct Counting;
thread_local! {
    static ARMED: Cell<bool> = const { Cell::new(false) };
    static ALLOCS: Cell<u64> = const { Cell::new(0) };
    /// Armed allocations of at least 60,000 octets (stream buffers).
    static BIG: Cell<usize> = const { Cell::new(0) };
}
fn record(size: usize) {
    // try_with: the allocator runs during thread teardown, after TLS is destroyed.
    if ARMED.try_with(Cell::get).unwrap_or(false) {
        let _ = ALLOCS.try_with(|c| c.set(c.get() + 1));
        if size >= 60_000 {
            let _ = BIG.try_with(|c| c.set(c.get() + 1));
        }
    }
}
unsafe impl GlobalAlloc for Counting {
    unsafe fn alloc(&self, l: Layout) -> *mut u8 {
        record(l.size());
        unsafe { System.alloc(l) }
    }
    unsafe fn dealloc(&self, p: *mut u8, l: Layout) {
        unsafe { System.dealloc(p, l) }
    }
    unsafe fn realloc(&self, p: *mut u8, l: Layout, n: usize) -> *mut u8 {
        record(n);
        unsafe { System.realloc(p, l, n) }
    }
}
#[global_allocator]
static GLOBAL: Counting = Counting;

#[test]
fn cache_hit_path_does_not_allocate() {
    use nexora_engine::proto::*;
    use nexora_engine::server::Shared;
    use nexora_engine::snapshot::{DirBlobs, apply};

    let shared = Shared::new(1);
    // A list selected globally and by group g1 (with an allowlist): the measured cache-hit client
    // walks a non-empty index with allow lists before its cache lookup.
    let tmp = tempfile::tempdir().unwrap();
    let z = zstd::encode_all(&b"ads.hot.test\n"[..], 3).unwrap();
    let sha = hex::encode(<sha2::Sha256 as sha2::Digest>::digest(&z));
    std::fs::write(tmp.path().join(&sha), &z).unwrap();
    let ads = BlobRef {
        sha256: sha,
        size: z.len() as u64,
        name: "ads".into(),
    };
    let ads_ref = FilterListRef {
        list_id: "ads".into(),
        category: "ads-tracking".into(),
        position: 1,
        blob: Some(ads.clone()),
    };
    let snap = ConfigSnapshot {
        version: 1,
        cache: Some(CacheConfig {
            max_bytes: 8 << 20,
            min_ttl: 0,
            max_ttl: 86400,
            negative_max_ttl: 3600,
            stale_window: 0,
        }),
        acl_allow_cidrs: vec!["127.0.0.0/8".into(), "10.0.0.0/8".into()],
        policy_groups: vec![PolicyGroup {
            id: "g1".into(),
            name: "g1".into(),
            cidrs: vec![
                "10.0.0.0/8".into(),
                "10.1.0.0/16".into(),
                "2001:db8::/32".into(),
            ],
            rewrite_set_ids: vec!["r".into()],
            blocklists: vec![ads.clone()],
            blocklist_refs: vec![ads_ref.clone()],
            allowlist: vec!["ok.ads.hot.test".into()],
        }],
        rewrite_sets: vec![RewriteSet {
            id: "r".into(),
            label: "r".into(),
            rules: vec![RewriteRule {
                name: "*.home.test".into(),
                r#type: RewriteType::A as i32,
                value: "192.168.1.1".into(),
                ttl: 60,
            }],
        }],
        filter: Some(FilterConfig {
            block_mode: BlockMode::NullIp as i32,
            block_ttl: 60,
            blocklists: vec![ads.clone()],
            blocklist_refs: vec![ads_ref.clone()],
            ..Default::default()
        }),
        telemetry: Some(TelemetryConfig::default()),
        resolver: Some(ResolverConfig {
            strategy: UpstreamStrategy::Parallel as i32,
            parallel_max: 2,
        }),
        authoritative_allow_cidrs: vec!["0.0.0.0/0".into(), "::/0".into()],
        authoritative_acl_set: true,
        // M8: the mDNS gateway and the ODoH target on leave the cache-hit path allocation-free.
        mdns: Some(MdnsConfig {
            enabled: true,
            interfaces: vec!["lo".into()],
            timeout_ms: 100,
            ..Default::default()
        }),
        odoh: Some(OdohConfig {
            target_enabled: true,
            ..Default::default()
        }),
        ..Default::default()
    };
    // Forward mode (M1/M2), then recursive mode with DNSSEC validation (M3): a cache hit takes the
    // same allocation-free path in both.
    let mut recursive = snap.clone();
    recursive.version = 2;
    recursive.resolution_mode = ResolutionMode::Recursive as i32;
    recursive.dnssec = Some(DnssecConfig {
        validation: true,
        rfc5011: true,
        ..Default::default()
    });
    for snap in [snap, recursive] {
        assert!(matches!(
            apply(
                &shared.runtime,
                snap,
                &DirBlobs {
                    dir: tmp.path().into(),
                },
                None,
            ),
            nexora_engine::snapshot::ApplyOutcome::Applied { .. }
        ));
        measure(&shared);
        measure_blocked(&shared);
    }
    // With RPZ query triggers loaded, a non-matching name still hits the cache without allocating.
    use nexora_engine::recursor::rpz::{index, parse};
    let origin = hickory_proto::rr::Name::from_ascii("rpz.test.").unwrap();
    let zone = parse::parse_rpz_text(
        &origin,
        "$TTL 60\n@ SOA ns h 1 60 60 60 60\nbad.example CNAME .\n32.9.0.0.10.rpz-client-ip CNAME .\n",
    )
    .unwrap();
    shared
        .recursor
        .rpz
        .publish(index::RpzSet::new(vec![std::sync::Arc::new(
            index::RpzZoneIndex::build("z", &zone, 0),
        )]));
    measure(&shared);
    measure_blocked(&shared);
    // A hosted zone behind the "any" authoritative ACL, and behind a list the client must match.
    use hickory_proto::rr::RecordType;
    let hosted = [
        ("www.example.test.", RecordType::A),
        ("nope.example.test.", RecordType::AAAA),
    ];
    let image = include_bytes!("../../testdata/nzf/basic-full.nzf");
    measure_auth(image, &hosted, false, Some(&["0.0.0.0/0", "::/0"]));
    measure_auth(image, &hosted, false, Some(&["10.0.0.0/8", "192.0.2.0/24"]));
}

/// Blocked names never allocate: 256 names first decided by the index (group g1 with its
/// allowlist, then a global client), then answered from the warmed decision cache. The group's
/// allowlisted name is answered from the response cache without allocating either.
fn measure_blocked(shared: &std::sync::Arc<nexora_engine::server::Shared>) {
    use hickory_proto::op::{Message, MessageType, OpCode, Query};
    use hickory_proto::rr::{Name, RecordType};
    use hickory_proto::serialize::binary::BinEncodable;
    use nexora_engine::edns::Transport;
    use nexora_engine::server::{FastOutcome, WorkerCtx, handle_packet};

    let query = |name: &str| {
        let mut m = Message::new(2, MessageType::Query, OpCode::Query);
        m.add_query(Query::query(Name::from_ascii(name).unwrap(), RecordType::A));
        m.to_bytes().unwrap()
    };
    let warm = query("warm.ads.hot.test.");
    let ctx = WorkerCtx::new(0, shared.clone());
    let mut out = [0u8; 1232];
    for client in ["10.1.2.3:5353", "127.0.0.1:5353"] {
        let client: std::net::SocketAddr = client.parse().unwrap();
        let names: Vec<Vec<u8>> = (0..256)
            .map(|i| query(&format!("n{i}.{}.ads.hot.test.", client.ip())))
            .collect();
        let before = shared.metrics.totals().filter_blocked;
        for _ in 0..64 {
            let rt = shared.runtime.load();
            assert!(matches!(
                handle_packet(&ctx, &rt, &warm, client, Transport::Udp, &mut out),
                FastOutcome::Reply(_)
            ));
        }
        ALLOCS.with(|c| c.set(0));
        ARMED.with(|a| a.set(true));
        for i in 0..50_000 {
            let rt = shared.runtime.load();
            let q = &names[i % names.len()];
            match handle_packet(&ctx, &rt, q, client, Transport::Udp, &mut out) {
                FastOutcome::Reply(n) => assert!(n > 12),
                _ => panic!("expected a block reply"),
            }
        }
        ARMED.with(|a| a.set(false));
        assert_eq!(
            ALLOCS.with(Cell::get),
            0,
            "blocked reply path allocated for {client}"
        );
        assert!(
            shared.metrics.totals().filter_blocked >= before + 50_064,
            "the names were blocked for {client}"
        );
        assert!(
            ctx.filter_decisions.hits() > 40_000,
            "repeats came from the decision cache"
        );
    }

    // The group's allowlisted name: an allow decision, then a response-cache hit.
    let client: std::net::SocketAddr = "10.1.2.3:5353".parse().unwrap();
    let allowed = query("ok.ads.hot.test.");
    {
        use hickory_proto::rr::{RData, Record, rdata::A};
        use hickory_proto::serialize::binary::BinDecodable;
        let rt = shared.runtime.load();
        let v = nexora_engine::wire::parse_query(&allowed).unwrap();
        let (policy, _) = rt.policy.select(client.ip());
        let mut r = Message::from_bytes(&allowed).unwrap();
        r.metadata.message_type = MessageType::Response;
        r.add_answer(Record::from_rdata(
            Name::from_ascii("ok.ads.hot.test.").unwrap(),
            300,
            RData::A(A::new(192, 0, 2, 2)),
        ));
        rt.cache.insert(
            nexora_engine::cache::CacheKey::in_partition(&v, policy.cache_partition()),
            &r.to_bytes().unwrap(),
            &v,
            nexora_engine::clock::now_secs(),
        );
    }
    for _ in 0..64 {
        let rt = shared.runtime.load();
        assert!(matches!(
            handle_packet(&ctx, &rt, &allowed, client, Transport::Udp, &mut out),
            FastOutcome::Reply(_)
        ));
    }
    let before_hits = shared.metrics.sum_cache_hits();
    ALLOCS.with(|c| c.set(0));
    ARMED.with(|a| a.set(true));
    for _ in 0..50_000 {
        let rt = shared.runtime.load();
        match handle_packet(&ctx, &rt, &allowed, client, Transport::Udp, &mut out) {
            FastOutcome::Reply(n) => assert!(n > 12),
            _ => panic!("expected a cached allowed reply"),
        }
    }
    ARMED.with(|a| a.set(false));
    assert_eq!(
        ALLOCS.with(Cell::get),
        0,
        "allowlisted cache-hit path allocated"
    );
    assert!(
        shared.metrics.sum_cache_hits() >= before_hits + 50_000,
        "the allowlisted name came from the response cache"
    );
}

fn measure(shared: &std::sync::Arc<nexora_engine::server::Shared>) {
    use hickory_proto::op::{Edns, Message, MessageType, OpCode, Query};
    use hickory_proto::rr::{Name, RData, Record, RecordType, rdata::A, rdata::opt::EdnsOption};
    use hickory_proto::serialize::binary::{BinDecodable, BinEncodable};
    use nexora_engine::cache::CacheKey;
    use nexora_engine::edns::Transport;
    use nexora_engine::server::{FastOutcome, WorkerCtx, handle_packet};
    use nexora_engine::wire::parse_query;

    let mut m = Message::new(1, MessageType::Query, OpCode::Query);
    m.metadata.recursion_desired = true;
    m.add_query(Query::query(
        Name::from_ascii("Hot.Example.").unwrap(),
        RecordType::A,
    ));
    let mut e = Edns::new();
    e.set_max_payload(1232);
    e.options_mut().insert(EdnsOption::Unknown(10, vec![9; 8]));
    m.set_edns(e);
    let query = m.to_bytes().unwrap();
    let mut r = Message::from_bytes(&query).unwrap();
    r.metadata.message_type = MessageType::Response;
    r.add_answer(Record::from_rdata(
        Name::from_ascii("hot.example.").unwrap(),
        300,
        RData::A(A::new(192, 0, 2, 1)),
    ));
    let upstream = r.to_bytes().unwrap();
    let client: std::net::SocketAddr = "10.1.2.3:5353".parse().unwrap();
    {
        let rt = shared.runtime.load();
        let v = parse_query(&query).unwrap();
        let (policy, group) = rt.policy.select(client.ip());
        assert_eq!(group, Some(0), "the measured client is in policy group g1");
        rt.cache.insert(
            CacheKey::in_partition(&v, policy.cache_partition()),
            &upstream,
            &v,
            nexora_engine::clock::now_secs(),
        );
    }
    let ctx = WorkerCtx::new(0, shared.clone());
    let before_hits = shared.metrics.sum_cache_hits();
    let mut out = [0u8; 1232];
    for _ in 0..64 {
        let rt = shared.runtime.load();
        assert!(matches!(
            handle_packet(&ctx, &rt, &query, client, Transport::Udp, &mut out),
            FastOutcome::Reply(_)
        ));
    }
    ALLOCS.with(|c| c.set(0));
    ARMED.with(|a| a.set(true));
    for _ in 0..50_000 {
        let rt = shared.runtime.load();
        match handle_packet(&ctx, &rt, &query, client, Transport::Udp, &mut out) {
            FastOutcome::Reply(n) => assert!(n > 40),
            _ => panic!("expected cache hit"),
        }
    }
    ARMED.with(|a| a.set(false));
    assert_eq!(
        ALLOCS.with(Cell::get),
        0,
        "cache-hit path allocated (resolution mode {:?})",
        shared.runtime.load().resolution.mode
    );
    assert!(shared.metrics.sum_cache_hits() >= before_hits + 50_000);
    assert!(!shared.querylog.is_empty(), "query records were pushed");
}

#[test]
fn authoritative_answer_path_does_not_allocate() {
    use hickory_proto::rr::RecordType;
    // An answer through a CNAME, a referral with glue, a wildcard and an NXDOMAIN.
    measure_auth(
        include_bytes!("../../testdata/nzf/basic-full.nzf"),
        &[
            ("Alias.Example.Test.", RecordType::A),
            ("host.sub.example.test.", RecordType::A),
            ("x.wild.example.test.", RecordType::TXT),
            ("nope.example.test.", RecordType::AAAA),
        ],
        false,
        None,
    );
    // DO=1 on pre-signed zones: RRSIGs, NSEC and NSEC3 (hashing) proofs, DS at referrals.
    let signed = [
        ("WWW.example.test.", RecordType::A),
        ("alias.example.test.", RecordType::A),
        ("x.nope.example.test.", RecordType::A),
        ("anything.wild.example.test.", RecordType::TXT),
        ("b.c.example.test.", RecordType::A),
        ("host.sub.example.test.", RecordType::A),
        ("host.insecure.example.test.", RecordType::A),
    ];
    measure_auth(
        include_bytes!("../../testdata/nzf/signed-nsec-full.nzf"),
        &signed,
        true,
        None,
    );
    measure_auth(
        include_bytes!("../../testdata/nzf/signed-nsec3-full.nzf"),
        &signed,
        true,
        None,
    );
}

/// `auth_acl`: the snapshot's authoritative ACL (`None`: a snapshot without one).
fn measure_auth(
    image: &[u8],
    names: &[(&str, hickory_proto::rr::RecordType)],
    dnssec_ok: bool,
    auth_acl: Option<&[&str]>,
) {
    use hickory_proto::op::{Edns, Message, MessageType, OpCode, Query};
    use hickory_proto::rr::Name;
    use hickory_proto::serialize::binary::BinEncodable;
    use nexora_engine::edns::Transport;
    use nexora_engine::proto::*;
    use nexora_engine::server::{FastOutcome, Shared, WorkerCtx, handle_packet};
    use nexora_engine::snapshot::{ApplyOutcome, DirBlobs, apply};
    use sha2::Digest;

    let tmp = tempfile::tempdir().unwrap();
    let z = zstd::encode_all(image, 3).unwrap();
    let sha = hex::encode(sha2::Sha256::digest(&z));
    std::fs::write(tmp.path().join(&sha), &z).unwrap();
    let shared = Shared::new(1);
    let snap = ConfigSnapshot {
        version: 1,
        cache: Some(CacheConfig {
            max_bytes: 8 << 20,
            max_ttl: 86400,
            negative_max_ttl: 3600,
            ..Default::default()
        }),
        acl_allow_cidrs: vec!["127.0.0.0/8".into()],
        filter: Some(FilterConfig::default()),
        telemetry: Some(TelemetryConfig::default()),
        resolver: Some(ResolverConfig::default()),
        authoritative_allow_cidrs: auth_acl
            .unwrap_or_default()
            .iter()
            .map(|c| (*c).into())
            .collect(),
        authoritative_acl_set: auth_acl.is_some(),
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
            ..Default::default()
        }],
        mdns: Some(MdnsConfig {
            enabled: true,
            interfaces: vec!["lo".into()],
            timeout_ms: 100,
            ..Default::default()
        }),
        odoh: Some(OdohConfig {
            target_enabled: true,
            ..Default::default()
        }),
        ..Default::default()
    };
    assert!(matches!(
        apply(
            &shared.runtime,
            snap,
            &DirBlobs {
                dir: tmp.path().into()
            },
            None
        ),
        ApplyOutcome::Applied { .. }
    ));
    let client: std::net::SocketAddr = "192.0.2.9:5353".parse().unwrap();
    let ctx = WorkerCtx::new(0, shared.clone());
    let queries: Vec<Vec<u8>> = names
        .iter()
        .map(|(name, t)| {
            let mut m = Message::new(7, MessageType::Query, OpCode::Query);
            m.add_query(Query::query(Name::from_ascii(name).unwrap(), *t));
            let mut e = Edns::new();
            e.set_max_payload(1232);
            e.set_dnssec_ok(dnssec_ok);
            m.set_edns(e);
            m.to_bytes().unwrap()
        })
        .collect();
    let mut out = [0u8; 1232];
    for q in &queries {
        let rt = shared.runtime.load();
        match handle_packet(&ctx, &rt, q, client, Transport::Udp, &mut out) {
            // Signed answers carry RRSIGs, and no reply was truncated.
            FastOutcome::Reply(n) => {
                assert!(n > 40);
                assert_eq!(out[2] & 0x02, 0, "TC");
                if dnssec_ok {
                    let rrsig = [0u8, 46, 0, 1];
                    assert!(out[..n].windows(4).any(|w| w == rrsig), "RRSIG present");
                }
            }
            _ => panic!("expected an authoritative reply"),
        }
    }
    ARMED.with(|a| a.set(true));
    ALLOCS.with(|c| c.set(0));
    for _ in 0..10_000 {
        for q in &queries {
            let rt = shared.runtime.load();
            match handle_packet(&ctx, &rt, q, client, Transport::Udp, &mut out) {
                FastOutcome::Reply(n) => assert!(n > 40),
                _ => panic!("expected an authoritative reply"),
            }
        }
    }
    ARMED.with(|a| a.set(false));
    assert_eq!(
        ALLOCS.with(Cell::get),
        0,
        "authoritative answer path allocated (DO={dnssec_ok})"
    );
}

/// A runtime answering `hot.example. A` from its cache for clients in 127.0.0.0/8, and the query.
fn stream_setup() -> (std::sync::Arc<nexora_engine::server::Shared>, Vec<u8>) {
    use hickory_proto::op::{Message, MessageType, OpCode, Query};
    use hickory_proto::rr::{Name, RData, Record, RecordType, rdata::A};
    use hickory_proto::serialize::binary::{BinDecodable, BinEncodable};
    use nexora_engine::cache::CacheKey;
    use nexora_engine::proto::*;
    use nexora_engine::server::Shared;
    use nexora_engine::snapshot::{ApplyOutcome, DirBlobs, apply};
    use nexora_engine::wire::parse_query;

    let tmp = tempfile::tempdir().unwrap();
    let shared = Shared::new(1);
    let snap = ConfigSnapshot {
        version: 1,
        cache: Some(CacheConfig {
            max_bytes: 8 << 20,
            max_ttl: 86400,
            negative_max_ttl: 3600,
            ..Default::default()
        }),
        acl_allow_cidrs: vec!["127.0.0.0/8".into()],
        filter: Some(FilterConfig::default()),
        telemetry: Some(TelemetryConfig::default()),
        resolver: Some(ResolverConfig::default()),
        ..Default::default()
    };
    assert!(matches!(
        apply(
            &shared.runtime,
            snap,
            &DirBlobs {
                dir: tmp.path().into()
            },
            None
        ),
        ApplyOutcome::Applied { .. }
    ));
    let mut m = Message::new(1, MessageType::Query, OpCode::Query);
    m.metadata.recursion_desired = true;
    m.add_query(Query::query(
        Name::from_ascii("hot.example.").unwrap(),
        RecordType::A,
    ));
    let query = m.to_bytes().unwrap();
    let mut r = Message::from_bytes(&query).unwrap();
    r.metadata.message_type = MessageType::Response;
    r.add_answer(Record::from_rdata(
        Name::from_ascii("hot.example.").unwrap(),
        300,
        RData::A(A::new(192, 0, 2, 1)),
    ));
    let upstream = r.to_bytes().unwrap();
    let rt = shared.runtime.load();
    let v = parse_query(&query).unwrap();
    let (policy, _) = rt.policy.select("127.0.0.1".parse().unwrap());
    rt.cache.insert(
        CacheKey::in_partition(&v, policy.cache_partition()),
        &upstream,
        &v,
        nexora_engine::clock::now_secs(),
    );
    drop(rt);
    (shared, query)
}

/// Writes one length-prefixed query and reads its reply (a cache hit with one answer).
async fn stream_roundtrip<S: tokio::io::AsyncRead + tokio::io::AsyncWrite>(
    wr: &mut tokio::io::WriteHalf<S>,
    rd: &mut tokio::io::ReadHalf<S>,
    frame: &[u8],
    reply: &mut [u8],
) {
    use tokio::io::{AsyncReadExt, AsyncWriteExt};
    wr.write_all(frame).await.unwrap();
    let n = rd.read_u16().await.unwrap() as usize;
    rd.read_exact(&mut reply[..n]).await.unwrap();
    assert_eq!(u16::from_be_bytes([reply[6], reply[7]]), 1, "one answer");
}

#[test]
fn stream_answers_reuse_pooled_buffers() {
    use nexora_engine::edns::Transport;
    use nexora_engine::server::stream::serve_dns_stream;
    use nexora_engine::server::{ClientInfo, WorkerAnswerer, WorkerCtx};

    let (shared, query) = stream_setup();
    let rt = tokio::runtime::Builder::new_current_thread()
        .enable_all()
        .build()
        .unwrap();
    tokio::task::LocalSet::new().block_on(&rt, async {
        let (client_io, server_io) = tokio::io::duplex(1 << 20);
        let ctx = std::rc::Rc::new(WorkerCtx::new(0, shared.clone()));
        let client = ClientInfo {
            addr: "127.0.0.1:5555".parse().unwrap(),
            transport: Transport::Tcp,
        };
        tokio::task::spawn_local(serve_dns_stream(
            std::rc::Rc::new(WorkerAnswerer(ctx)),
            server_io,
            client,
            std::time::Duration::from_secs(30),
        ));
        let (mut rd, mut wr) = tokio::io::split(client_io);
        let mut frame = (query.len() as u16).to_be_bytes().to_vec();
        frame.extend_from_slice(&query);
        let mut reply = vec![0u8; 4096];
        for _ in 0..64 {
            stream_roundtrip(&mut wr, &mut rd, &frame, &mut reply).await;
        }
        BIG.with(|c| c.set(0));
        ARMED.with(|a| a.set(true));
        for _ in 0..1000 {
            stream_roundtrip(&mut wr, &mut rd, &frame, &mut reply).await;
        }
        ARMED.with(|a| a.set(false));
    });
    assert_eq!(
        BIG.with(Cell::get),
        0,
        "a stream query allocated a 64 KiB buffer"
    );
}

/// One DoQ query on its own bidirectional stream; the reply is read into `reply`.
async fn doq_roundtrip(conn: &quinn::Connection, frame: &[u8], reply: &mut [u8]) {
    let (mut send, mut recv) = conn.open_bi().await.unwrap();
    send.write_all(frame).await.unwrap();
    send.finish().unwrap();
    let mut n = 0;
    while let Some(k) = recv.read(&mut reply[n..]).await.unwrap() {
        n += k;
    }
    assert_eq!(u16::from_be_bytes([reply[0], reply[1]]) as usize, n - 2);
    assert_eq!(u16::from_be_bytes([reply[8], reply[9]]), 1, "one answer");
}

#[test]
fn doq_answers_reuse_pooled_buffers() {
    use nexora_engine::server::tls::{CertStore, provider, quic_server_config};
    use nexora_engine::server::{WorkerAnswerer, WorkerCtx, doq};
    use std::sync::Arc;

    let (shared, mut query) = stream_setup();
    query[..2].copy_from_slice(&[0, 0]); // DoQ requires message ID 0
    let rt = tokio::runtime::Builder::new_current_thread()
        .enable_all()
        .build()
        .unwrap();
    tokio::task::LocalSet::new().block_on(&rt, async {
        let ck = rcgen::generate_simple_self_signed(vec!["dns.test".to_string()]).unwrap();
        let store = Arc::new(CertStore::new());
        let now = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_secs() as i64;
        store
            .install_pem(
                ck.cert.pem().as_bytes(),
                ck.signing_key.serialize_pem().as_bytes(),
                now,
            )
            .unwrap();
        let server = doq::bind_doq(
            "127.0.0.1:0".parse().unwrap(),
            quic_server_config(store.clone()),
            doq::endpoint_config(&[7u8; 64]),
        )
        .unwrap();
        let addr = server.local_addr().unwrap();
        let ctx = std::rc::Rc::new(WorkerCtx::new(0, shared.clone()));
        tokio::task::spawn_local(doq::run_doq(
            server,
            std::rc::Rc::new(WorkerAnswerer(ctx)),
            store,
        ));

        let mut roots = rustls::RootCertStore::empty();
        roots.add(ck.cert.der().clone()).unwrap();
        let mut tls = rustls::ClientConfig::builder_with_provider(provider())
            .with_protocol_versions(&[&rustls::version::TLS13])
            .unwrap()
            .with_root_certificates(roots)
            .with_no_client_auth();
        tls.alpn_protocols = vec![b"doq".to_vec()];
        let crypto = quinn::crypto::rustls::QuicClientConfig::try_from(tls).unwrap();
        let mut client = quinn::Endpoint::client("127.0.0.1:0".parse().unwrap()).unwrap();
        client.set_default_client_config(quinn::ClientConfig::new(Arc::new(crypto)));
        let conn = client.connect(addr, "dns.test").unwrap().await.unwrap();

        let mut frame = (query.len() as u16).to_be_bytes().to_vec();
        frame.extend_from_slice(&query);
        let mut reply = vec![0u8; 4096];
        for _ in 0..32 {
            doq_roundtrip(&conn, &frame, &mut reply).await;
        }
        BIG.with(|c| c.set(0));
        ARMED.with(|a| a.set(true));
        for _ in 0..200 {
            doq_roundtrip(&conn, &frame, &mut reply).await;
        }
        ARMED.with(|a| a.set(false));
        conn.close(0u32.into(), b"");
        client.wait_idle().await;
    });
    assert_eq!(
        BIG.with(Cell::get),
        0,
        "a DoQ query allocated a 64 KiB buffer"
    );
}
