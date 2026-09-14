//! A running engine (one worker) with rewrites, an optional RPZ zone and an OTLP log collector,
//! for tests that assert on exported query records.

use hickory_proto::op::{Message, MessageType, OpCode, Query};
use hickory_proto::rr::rdata::A;
use hickory_proto::rr::{Name, RData, Record, RecordType};
use nexora_engine::bootstrap::Bootstrap;
use nexora_engine::proto::*;
use nexora_engine::server::tls::CertStore;
use nexora_engine::server::{Shared, spawn_workers};
use nexora_engine::snapshot::{ApplyOutcome, DirBlobs, apply};
use nexora_engine::telemetry::otlp::spawn_telemetry_thread;
use opentelemetry_proto::tonic::collector::logs::v1::{
    ExportLogsServiceRequest, ExportLogsServiceResponse,
    logs_service_server::{LogsService, LogsServiceServer},
};
use opentelemetry_proto::tonic::common::v1::any_value;
use std::net::{SocketAddr, UdpSocket};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

pub const RPZ_ZONE_ID: &str = "attr-rpz";

/// The exported attributes of one query record the tests assert on (empty when absent).
#[derive(Clone, Debug, Default)]
pub struct Rec {
    pub name: String,
    pub source: String,
    pub rule: String,
    pub rpz_zone: String,
}

#[derive(Clone, Default)]
struct Sink(Arc<Mutex<Vec<Rec>>>);

#[tonic::async_trait]
impl LogsService for Sink {
    async fn export(
        &self,
        req: tonic::Request<ExportLogsServiceRequest>,
    ) -> Result<tonic::Response<ExportLogsServiceResponse>, tonic::Status> {
        let mut recs = self.0.lock().unwrap();
        for lr in req
            .into_inner()
            .resource_logs
            .iter()
            .flat_map(|r| &r.scope_logs)
            .flat_map(|s| &s.log_records)
        {
            let mut rec = Rec::default();
            for kv in &lr.attributes {
                let Some(any_value::Value::StringValue(v)) =
                    kv.value.as_ref().and_then(|v| v.value.as_ref())
                else {
                    continue;
                };
                let field = match kv.key.as_str() {
                    "dns.question.name" => &mut rec.name,
                    "nexora.filter.source" => &mut rec.source,
                    "nexora.filter.rule" => &mut rec.rule,
                    "nexora.rpz_zone" => &mut rec.rpz_zone,
                    _ => continue,
                };
                *field = v.clone();
            }
            recs.push(rec);
        }
        Ok(tonic::Response::new(ExportLogsServiceResponse::default()))
    }
}

#[derive(Default)]
pub struct RigOptions<'a> {
    /// `(name, type, value)` rewrite rules of the global rewrite set; type is `A`, `AAAA` or `CNAME`.
    pub rewrites: &'a [(&'a str, &'a str, &'a str)],
    /// An RPZ zone file (with its SOA) served as zone `rpz.attr.`, id [`RPZ_ZONE_ID`].
    pub rpz_file: Option<&'a str>,
}

pub struct Rig {
    server: SocketAddr,
    logs: Sink,
    _collector: tokio::runtime::Runtime,
}

/// Answers every query with an A record 192.0.2.1.
fn fake_upstream() -> SocketAddr {
    let s = UdpSocket::bind("127.0.0.1:0").unwrap();
    let addr = s.local_addr().unwrap();
    std::thread::spawn(move || {
        let mut buf = [0u8; 4096];
        loop {
            let (n, peer) = s.recv_from(&mut buf).unwrap();
            let mut m = Message::from_vec(&buf[..n]).unwrap();
            m.metadata.message_type = MessageType::Response;
            m.metadata.recursion_available = true;
            let name = m.queries[0].name().clone();
            m.answers.push(Record::from_rdata(
                name,
                300,
                RData::A(A::new(192, 0, 2, 1)),
            ));
            let _ = s.send_to(&m.to_vec().unwrap(), peer);
        }
    });
    addr
}

impl Rig {
    pub fn start(opts: RigOptions<'_>) -> Rig {
        use sha2::Digest;
        let collector = tokio::runtime::Runtime::new().unwrap();
        let logs = Sink::default();
        let listener = collector
            .block_on(tokio::net::TcpListener::bind("127.0.0.1:0"))
            .unwrap();
        let otlp = listener.local_addr().unwrap();
        collector.spawn(
            tonic::transport::Server::builder()
                .add_service(LogsServiceServer::new(logs.clone()))
                .serve_with_incoming(tokio_stream::wrappers::TcpListenerStream::new(listener)),
        );

        let port = std::net::TcpListener::bind("127.0.0.1:0")
            .unwrap()
            .local_addr()
            .unwrap()
            .port();
        let dir = tempfile::tempdir().unwrap().keep();
        let boot: Bootstrap = toml::from_str(&format!(
            "node_name = \"t\"\nstate_dir = \"{}\"\nlisten_udp = [\"127.0.0.1:{port}\"]\nlisten_tcp = [\"127.0.0.1:{port}\"]\nworkers = 1\nstandalone_snapshot = \"x\"\n",
            dir.display()
        ))
        .unwrap();
        let rpz_zones = opts
            .rpz_file
            .map(|text| {
                let z = zstd::encode_all(text.as_bytes(), 3).unwrap();
                let sha = hex::encode(sha2::Sha256::digest(&z));
                std::fs::write(dir.join(&sha), &z).unwrap();
                RpzZone {
                    id: RPZ_ZONE_ID.into(),
                    name: "rpz.attr.".into(),
                    source: Some(rpz_zone::Source::File(RpzFileSource {
                        blob: Some(BlobRef {
                            sha256: sha,
                            size: z.len() as u64,
                            name: "rpz".into(),
                        }),
                    })),
                    policy_override: 0,
                    refresh_nonce: 0,
                }
            })
            .into_iter()
            .collect();
        let rules = opts
            .rewrites
            .iter()
            .map(|(name, t, value)| RewriteRule {
                name: (*name).into(),
                r#type: match *t {
                    "A" => RewriteType::A,
                    "AAAA" => RewriteType::Aaaa,
                    "CNAME" => RewriteType::Cname,
                    other => panic!("rewrite type {other}"),
                } as i32,
                value: (*value).into(),
                ttl: 60,
            })
            .collect();
        let shared = Shared::new(1);
        let snap = ConfigSnapshot {
            version: 1,
            resolver: Some(ResolverConfig {
                strategy: UpstreamStrategy::Ordered as i32,
                ..Default::default()
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
                address: fake_upstream().to_string(),
                timeout_ms: 500,
                ..Default::default()
            }],
            acl_allow_cidrs: vec!["127.0.0.0/8".into()],
            filter: Some(FilterConfig::default()),
            telemetry: Some(TelemetryConfig {
                otlp_endpoint: format!("http://{otlp}"),
                ..Default::default()
            }),
            rewrite_sets: vec![RewriteSet {
                id: "custom:global".into(),
                label: "custom".into(),
                rules,
            }],
            global_rewrite_set_ids: vec!["custom:global".into()],
            rpz_zones,
            ..Default::default()
        };
        assert!(matches!(
            apply(&shared.runtime, snap, &DirBlobs { dir }, None),
            ApplyOutcome::Applied { .. }
        ));
        shared.recursor.sync(&shared.runtime.load_full());
        spawn_workers(shared.clone(), &boot, Arc::new(CertStore::new())).unwrap();
        spawn_telemetry_thread(shared.clone());
        std::thread::sleep(Duration::from_millis(200));
        Rig {
            server: format!("127.0.0.1:{port}").parse().unwrap(),
            logs,
            _collector: collector,
        }
    }

    /// Sends an A query for `name` over UDP and waits for the reply.
    pub fn query_a(&self, name: &str) -> Message {
        let c = UdpSocket::bind("127.0.0.1:0").unwrap();
        c.set_read_timeout(Some(Duration::from_secs(3))).unwrap();
        let mut m = Message::new(0x5151, MessageType::Query, OpCode::Query);
        m.metadata.recursion_desired = true;
        m.queries
            .push(Query::query(Name::from_ascii(name).unwrap(), RecordType::A));
        c.send_to(&m.to_vec().unwrap(), self.server).unwrap();
        let mut buf = [0u8; 4096];
        let (n, _) = c.recv_from(&mut buf).unwrap();
        Message::from_vec(&buf[..n]).unwrap()
    }

    /// The first `n` exported query records, waiting up to 10 s for them.
    pub fn records(&self, n: usize) -> Vec<Rec> {
        let deadline = Instant::now() + Duration::from_secs(10);
        loop {
            let recs = self.logs.0.lock().unwrap().clone();
            if recs.len() >= n || Instant::now() >= deadline {
                assert!(recs.len() >= n, "{} of {n} records: {recs:?}", recs.len());
                return recs;
            }
            std::thread::sleep(Duration::from_millis(50));
        }
    }

    pub fn rpz_zone_id(&self) -> &'static str {
        RPZ_ZONE_ID
    }
}
