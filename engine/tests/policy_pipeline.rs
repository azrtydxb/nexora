//! Per-client policy through the query pipeline: groups never share cached
//! answers, and rewrites answer inline or through the miss pipeline.

use hickory_proto::op::{Message, MessageType, OpCode, Query, ResponseCode};
use hickory_proto::rr::rdata::{A, CNAME};
use hickory_proto::rr::{Name, RData, Record, RecordType};
use nexora_engine::edns::Transport;
use nexora_engine::proto::*;
use nexora_engine::server::rewrite::run_rewrite_job;
use nexora_engine::server::{FastOutcome, Shared, WorkerCtx, handle_packet, resolve_miss};
use nexora_engine::snapshot::{ApplyOutcome, DirBlobs, apply};
use std::net::{Ipv4Addr, SocketAddr, UdpSocket};
use std::rc::Rc;
use std::sync::Arc;
use std::sync::atomic::{AtomicUsize, Ordering};

const TRACKER_IP: Ipv4Addr = Ipv4Addr::new(192, 0, 2, 7);
const SAFE_IP: Ipv4Addr = Ipv4Addr::new(192, 0, 2, 99);

/// Answers `safe.test` with an A record; every other name with a CNAME to
/// `tracker.test` plus its A record (a CNAME-cloaked tracker).
fn fake_upstream(count: Arc<AtomicUsize>) -> SocketAddr {
    let s = UdpSocket::bind("127.0.0.1:0").unwrap();
    let addr = s.local_addr().unwrap();
    std::thread::spawn(move || {
        let mut buf = [0u8; 4096];
        loop {
            let (n, peer) = s.recv_from(&mut buf).unwrap();
            count.fetch_add(1, Ordering::SeqCst);
            let mut m = Message::from_vec(&buf[..n]).unwrap();
            m.metadata.message_type = MessageType::Response;
            m.metadata.recursion_available = true;
            let name = m.queries[0].name().clone();
            if name.to_lowercase().to_ascii() == "safe.test." {
                m.answers
                    .push(Record::from_rdata(name, 300, RData::A(A(SAFE_IP))));
            } else {
                let tracker = Name::from_ascii("tracker.test.").unwrap();
                m.answers.push(Record::from_rdata(
                    name,
                    300,
                    RData::CNAME(CNAME(tracker.clone())),
                ));
                m.answers
                    .push(Record::from_rdata(tracker, 300, RData::A(A(TRACKER_IP))));
            }
            let _ = s.send_to(&m.to_vec().unwrap(), peer);
        }
    });
    addr
}

fn setup(upstream: SocketAddr) -> Arc<Shared> {
    use sha2::Digest;
    let dir = tempfile::tempdir().unwrap().keep();
    let z = zstd::encode_all(&b"tracker.test\n"[..], 3).unwrap();
    let sha = hex::encode(sha2::Sha256::digest(&z));
    std::fs::write(dir.join(&sha), &z).unwrap();
    let shared = Shared::new(1);
    let snap = ConfigSnapshot {
        version: 1,
        resolver: Some(ResolverConfig {
            strategy: UpstreamStrategy::Ordered as i32,
        }),
        cache: Some(CacheConfig {
            max_bytes: 8 << 20,
            min_ttl: 0,
            max_ttl: 86400,
            negative_max_ttl: 3600,
            stale_window: 0,
        }),
        upstreams: vec![Upstream {
            id: "u".into(),
            name: "u".into(),
            protocol: UpstreamProtocol::Udp as i32,
            address: upstream.to_string(),
            timeout_ms: 1000,
            ..Default::default()
        }],
        acl_allow_cidrs: vec!["0.0.0.0/0".into()],
        filter: Some(FilterConfig {
            block_mode: BlockMode::NullIp as i32,
            block_ttl: 60,
            ..Default::default()
        }),
        telemetry: Some(TelemetryConfig::default()),
        policy_groups: vec![PolicyGroup {
            id: "strict".into(),
            name: "strict".into(),
            cidrs: vec!["10.0.0.0/8".into()],
            blocklists: vec![BlobRef {
                sha256: sha,
                size: z.len() as u64,
                name: "trackers".into(),
            }],
            rewrite_set_ids: vec!["custom:group:strict".into()],
            ..Default::default()
        }],
        rewrite_sets: vec![RewriteSet {
            id: "custom:group:strict".into(),
            label: "custom".into(),
            rules: vec![
                RewriteRule {
                    name: "nas.home.test".into(),
                    r#type: RewriteType::A as i32,
                    value: "192.168.1.50".into(),
                    ttl: 120,
                },
                RewriteRule {
                    name: "www.search.test".into(),
                    r#type: RewriteType::Cname as i32,
                    value: "safe.test".into(),
                    ttl: 120,
                },
            ],
        }],
        ..Default::default()
    };
    assert!(matches!(
        apply(&shared.runtime, snap, &DirBlobs { dir }, None),
        ApplyOutcome::Applied { .. }
    ));
    shared
}

fn query(name: &str) -> Vec<u8> {
    let mut m = Message::new(0x1234, MessageType::Query, OpCode::Query);
    m.metadata.recursion_desired = true;
    m.queries
        .push(Query::query(Name::from_ascii(name).unwrap(), RecordType::A));
    m.to_vec().unwrap()
}

/// How the pipeline answered: from the fast path (cache or policy) or after a miss.
enum Answered {
    Fast(Message),
    Miss(Message),
    Rewrite(Message),
}

async fn ask(ctx: &Rc<WorkerCtx>, shared: &Shared, client: &str, name: &str) -> Answered {
    let rt = shared.runtime.load_full();
    let client: SocketAddr = client.parse().unwrap();
    let mut out = vec![0u8; 4096];
    match handle_packet(ctx, &rt, &query(name), client, Transport::Udp, &mut out) {
        FastOutcome::Reply(n) => Answered::Fast(Message::from_vec(&out[..n]).unwrap()),
        FastOutcome::Miss(job) => {
            Answered::Miss(Message::from_vec(&resolve_miss(ctx.clone(), rt, job).await).unwrap())
        }
        FastOutcome::Rewrite(job) => Answered::Rewrite(
            Message::from_vec(&run_rewrite_job(ctx.clone(), rt, job).await).unwrap(),
        ),
        FastOutcome::Drop => panic!("dropped {name} from {client}"),
    }
}

fn a_records(m: &Message) -> Vec<Ipv4Addr> {
    m.answers
        .iter()
        .filter_map(|r| match r.data {
            RData::A(A(ip)) => Some(ip),
            _ => None,
        })
        .collect()
}

const GLOBAL: &str = "192.0.2.10:5353";
const STRICT: &str = "10.1.2.3:5353";

#[tokio::test(flavor = "current_thread")]
async fn groups_never_see_each_others_cached_answers() {
    let count = Arc::new(AtomicUsize::new(0));
    let shared = setup(fake_upstream(count.clone()));
    let ctx = Rc::new(WorkerCtx::new(0, shared.clone()));
    tokio::task::LocalSet::new()
        .run_until(async {
            // Positive path: the global client resolves the cloaked name and then hits the cache.
            let Answered::Miss(m) = ask(&ctx, &shared, GLOBAL, "cloaked.test.").await else {
                panic!("first global query must miss");
            };
            assert_eq!(a_records(&m), vec![TRACKER_IP]);
            let Answered::Fast(m) = ask(&ctx, &shared, GLOBAL, "cloaked.test.").await else {
                panic!("second global query must hit the cache");
            };
            assert_eq!(a_records(&m), vec![TRACKER_IP]);
            assert_eq!(count.load(Ordering::SeqCst), 1);

            // The strict group must not be served the global group's cached answer.
            let Answered::Miss(m) = ask(&ctx, &shared, STRICT, "cloaked.test.").await else {
                panic!("strict client was served the global cache entry");
            };
            assert_eq!(
                a_records(&m),
                vec![Ipv4Addr::UNSPECIFIED],
                "cloaked tracker blocked"
            );
            let Answered::Miss(m) = ask(&ctx, &shared, STRICT, "cloaked.test.").await else {
                panic!("a cloaking block is never cached");
            };
            assert_eq!(a_records(&m), vec![Ipv4Addr::UNSPECIFIED]);

            // Reverse order: the strict client first, then the global client.
            let Answered::Miss(m) = ask(&ctx, &shared, STRICT, "other.test.").await else {
                panic!("first strict query must miss");
            };
            assert_eq!(a_records(&m), vec![Ipv4Addr::UNSPECIFIED]);
            let Answered::Miss(m) = ask(&ctx, &shared, GLOBAL, "other.test.").await else {
                panic!("global client must resolve on its own");
            };
            assert_eq!(a_records(&m), vec![TRACKER_IP]);
            // IPv4-mapped IPv6 clients select the IPv4 group.
            let Answered::Miss(m) = ask(&ctx, &shared, "[::ffff:10.9.9.9]:53", "other.test.").await
            else {
                panic!("mapped strict client was served the global cache entry");
            };
            assert_eq!(a_records(&m), vec![Ipv4Addr::UNSPECIFIED]);
        })
        .await;
}

#[tokio::test(flavor = "current_thread")]
async fn rewrites_apply_only_to_their_group() {
    let count = Arc::new(AtomicUsize::new(0));
    let shared = setup(fake_upstream(count.clone()));
    let ctx = Rc::new(WorkerCtx::new(0, shared.clone()));
    tokio::task::LocalSet::new()
        .run_until(async {
            let Answered::Fast(m) = ask(&ctx, &shared, STRICT, "NAS.home.test.").await else {
                panic!("an address rewrite answers on the fast path");
            };
            assert_eq!(m.metadata.id, 0x1234);
            assert_eq!(a_records(&m), vec![Ipv4Addr::new(192, 168, 1, 50)]);
            assert_eq!(m.answers[0].name.to_ascii(), "NAS.home.test.");
            assert_eq!(count.load(Ordering::SeqCst), 0);

            let Answered::Rewrite(m) = ask(&ctx, &shared, STRICT, "www.search.test.").await else {
                panic!("a CNAME rewrite is chased off the fast path");
            };
            assert_eq!(m.metadata.response_code, ResponseCode::NoError);
            assert_eq!(
                m.answers[0].data,
                RData::CNAME(CNAME(Name::from_ascii("safe.test.").unwrap()))
            );
            assert_eq!(a_records(&m), vec![SAFE_IP]);

            let Answered::Miss(m) = ask(&ctx, &shared, GLOBAL, "nas.home.test.").await else {
                panic!("global clients get no group rewrite");
            };
            assert_eq!(a_records(&m), vec![TRACKER_IP]);
            let records = std::iter::from_fn(|| shared.querylog.pop()).collect::<Vec<_>>();
            assert!(
                records
                    .iter()
                    .any(|r| r.filter.as_str() == "rewritten" && r.policy_group == 0)
            );
        })
        .await;
}
