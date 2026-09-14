use nexora_engine::edns::Transport;
use nexora_engine::proto::*;
use nexora_engine::server::Shared;
use nexora_engine::snapshot::{DirBlobs, apply};
use nexora_engine::telemetry::metrics::{Signal, serve_metrics};
use nexora_engine::telemetry::otlp::{log_record, should_trace, spans_for, spawn_telemetry_thread};
use nexora_engine::telemetry::querylog::{
    CacheOutcome, FilterOutcome, NO_FILTER_LIST, QueryRecord, push,
};
use nexora_engine::wire::NameKey;
use opentelemetry_proto::tonic::collector::logs::v1::{
    ExportLogsServiceRequest, ExportLogsServiceResponse,
    logs_service_server::{LogsService, LogsServiceServer},
};
use opentelemetry_proto::tonic::collector::trace::v1::{
    ExportTraceServiceRequest, ExportTraceServiceResponse,
    trace_service_server::{TraceService, TraceServiceServer},
};
use std::sync::Arc;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::time::{Duration, Instant};

fn record(rcode: u8) -> QueryRecord {
    QueryRecord {
        unix_micros: 1_700_000_000_000_000,
        client: "127.0.0.1".parse().unwrap(),
        name: NameKey::from_wire_lowercase(b"\x07example\x03com\x00").unwrap(),
        qtype: 1,
        rcode,
        cache: CacheOutcome::Miss,
        filter: FilterOutcome::None,
        upstream: 0,
        config_version: 1,
        policy_group: u16::MAX,
        transport: Transport::Udp,
        filter_us: 3,
        cache_us: 5,
        upstream_start_us: 6,
        upstream_us: 900,
        duration_us: 950,
        route: 1,
        dnssec: 1,
        rpz_action: 0,
        filter_list: NO_FILTER_LIST,
        filter_generation: 0,
    }
}

#[derive(Default, Clone)]
struct Sink {
    logs: Arc<AtomicUsize>,
    spans: Arc<AtomicUsize>,
    /// Every `nexora.upstream` attribute of the exported log records, in arrival order.
    upstreams: Arc<parking_lot::Mutex<Vec<String>>>,
    /// How long each log export takes to answer.
    delay: Duration,
    /// Log exports in progress, and the most that ever overlapped.
    active: Arc<AtomicUsize>,
    peak: Arc<AtomicUsize>,
}
#[tonic::async_trait]
impl LogsService for Sink {
    async fn export(
        &self,
        req: tonic::Request<ExportLogsServiceRequest>,
    ) -> Result<tonic::Response<ExportLogsServiceResponse>, tonic::Status> {
        let active = self.active.fetch_add(1, Ordering::SeqCst) + 1;
        self.peak.fetch_max(active, Ordering::SeqCst);
        tokio::time::sleep(self.delay).await;
        let req = req.into_inner();
        let records = || {
            req.resource_logs
                .iter()
                .flat_map(|r| &r.scope_logs)
                .flat_map(|s| &s.log_records)
        };
        self.upstreams.lock().extend(
            records()
                .flat_map(|r| &r.attributes)
                .filter(|kv| kv.key == "nexora.upstream")
                .filter_map(|kv| match kv.value.as_ref()?.value.as_ref()? {
                    opentelemetry_proto::tonic::common::v1::any_value::Value::StringValue(v) => {
                        Some(v.clone())
                    }
                    _ => None,
                }),
        );
        self.logs.fetch_add(records().count(), Ordering::SeqCst);
        self.active.fetch_sub(1, Ordering::SeqCst);
        Ok(tonic::Response::new(ExportLogsServiceResponse::default()))
    }
}
#[tonic::async_trait]
impl TraceService for Sink {
    async fn export(
        &self,
        req: tonic::Request<ExportTraceServiceRequest>,
    ) -> Result<tonic::Response<ExportTraceServiceResponse>, tonic::Status> {
        let n: usize = req
            .into_inner()
            .resource_spans
            .iter()
            .flat_map(|r| &r.scope_spans)
            .map(|s| s.spans.len())
            .sum();
        self.spans.fetch_add(n, Ordering::SeqCst);
        Ok(tonic::Response::new(ExportTraceServiceResponse::default()))
    }
}

fn shared_with_endpoint(endpoint: &str) -> Arc<Shared> {
    let shared = Shared::new(1);
    let tmp = tempfile::tempdir().unwrap();
    let snap = ConfigSnapshot {
        version: 1,
        cache: Some(CacheConfig {
            max_bytes: 2 << 20,
            max_ttl: 86400,
            negative_max_ttl: 60,
            ..Default::default()
        }),
        upstreams: vec![Upstream {
            id: "u".into(),
            name: "fixture".into(),
            protocol: UpstreamProtocol::Udp as i32,
            address: "127.0.0.1:9".into(),
            timeout_ms: 100,
            ..Default::default()
        }],
        resolver: Some(ResolverConfig::default()),
        filter: Some(FilterConfig::default()),
        telemetry: Some(TelemetryConfig {
            otlp_endpoint: endpoint.into(),
            trace_sample_one_in: 0,
            trace_slow_threshold_us: 0,
            querylog_to_management: false,
        }),
        ..Default::default()
    };
    apply(
        &shared.runtime,
        snap,
        &DirBlobs {
            dir: tmp.path().into(),
        },
        None,
    );
    shared
}

/// Applies the snapshot of [`shared_with_endpoint`] (keeping its telemetry) at `version`, with one
/// upstream per name in that order.
fn apply_reordered(shared: &Shared, version: u64, names: &[&str]) {
    let tmp = tempfile::tempdir().unwrap();
    let current = shared.runtime.load_full();
    let snap = ConfigSnapshot {
        version,
        cache: Some(CacheConfig {
            max_bytes: 2 << 20,
            max_ttl: 86400,
            negative_max_ttl: 60,
            ..Default::default()
        }),
        upstreams: names
            .iter()
            .map(|name| Upstream {
                id: format!("u-{name}"),
                name: (*name).into(),
                protocol: UpstreamProtocol::Udp as i32,
                address: "127.0.0.1:9".into(),
                timeout_ms: 100,
                ..Default::default()
            })
            .collect(),
        resolver: Some(ResolverConfig::default()),
        filter: Some(FilterConfig::default()),
        telemetry: Some(TelemetryConfig {
            otlp_endpoint: current.telemetry.otlp_endpoint.clone(),
            trace_sample_one_in: 0,
            trace_slow_threshold_us: 0,
            querylog_to_management: false,
        }),
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
    assert_eq!(shared.runtime.load().version, version, "{outcome:?}");
}

fn serve(sink: Sink) -> (tokio::runtime::Runtime, std::net::SocketAddr) {
    let rt = tokio::runtime::Runtime::new().unwrap();
    let listener = rt
        .block_on(tokio::net::TcpListener::bind("127.0.0.1:0"))
        .unwrap();
    let addr = listener.local_addr().unwrap();
    rt.spawn(
        tonic::transport::Server::builder()
            .add_service(LogsServiceServer::new(sink.clone()))
            .add_service(TraceServiceServer::new(sink))
            .serve_with_incoming(tokio_stream::wrappers::TcpListenerStream::new(listener)),
    );
    (rt, addr)
}

#[test]
fn upstream_label_uses_the_runtime_that_answered() {
    let sink = Sink::default();
    let (_rt, addr) = serve(sink.clone());
    let shared = shared_with_endpoint(&format!("http://{addr}"));
    // Version 2 reorders the upstreams before the version-1 record is drained.
    push(&shared.querylog, &shared.metrics, record(0)); // upstream 0, config_version 1
    apply_reordered(&shared, 2, &["other", "fixture"]);
    let _t = spawn_telemetry_thread(shared.clone());
    let deadline = Instant::now() + Duration::from_secs(5);
    while sink.upstreams.lock().is_empty() && Instant::now() < deadline {
        std::thread::sleep(Duration::from_millis(50));
    }
    assert_eq!(*sink.upstreams.lock(), vec!["fixture".to_string()]);
}

#[test]
fn slow_collector_does_not_drop_with_concurrent_exports() {
    // 10 batches/s arrive. A 250 ms collector clears 4/s with one export at a time (the queue of 8
    // overflows within a few seconds) but 16/s with four concurrent exports, which leaves headroom
    // for a loaded CI host.
    let sink = Sink {
        delay: Duration::from_millis(250),
        ..Sink::default()
    };
    let (_rt, addr) = serve(sink.clone());
    let shared = shared_with_endpoint(&format!("http://{addr}"));
    let _t = spawn_telemetry_thread(shared.clone());
    for _ in 0..20 {
        for _ in 0..1000 {
            push(&shared.querylog, &shared.metrics, record(0));
        }
        std::thread::sleep(Duration::from_millis(100));
    }
    let deadline = Instant::now() + Duration::from_secs(15);
    while sink.logs.load(Ordering::SeqCst) + (shared.metrics.dropped(Signal::Logs) as usize)
        < 20_000
        && Instant::now() < deadline
    {
        std::thread::sleep(Duration::from_millis(50));
    }
    assert_eq!(
        shared.metrics.dropped(Signal::Logs),
        0,
        "logs dropped behind a 250 ms collector"
    );
    assert_eq!(sink.logs.load(Ordering::SeqCst), 20_000);
    assert!(
        sink.peak.load(Ordering::SeqCst) > 1,
        "exports never overlapped"
    );
}

#[test]
fn log_record_attributes_and_trace_rules() {
    let lr = log_record(&record(0), "fixture", "engine-uuid", "", None);
    let keys: Vec<&str> = lr.attributes.iter().map(|kv| kv.key.as_str()).collect();
    for k in [
        "client.address",
        "dns.question.name",
        "dns.question.type",
        "dns.response.code",
        "nexora.cache",
        "nexora.filter",
        "nexora.policy.group",
        "nexora.upstream",
        "nexora.duration_us",
        "nexora.transport",
        "nexora.engine.id",
    ] {
        assert!(keys.contains(&k), "missing {k}");
    }
    let t = nexora_engine::runtime::TelemetrySettings {
        otlp_endpoint: String::new(),
        trace_sample_one_in: 0,
        trace_slow_threshold_us: 0,
        querylog_to_management: false,
    };
    assert!(!should_trace(&record(0), &t, 1));
    assert!(should_trace(&record(2), &t, 1), "SERVFAIL always traced");
    let slow = nexora_engine::runtime::TelemetrySettings {
        trace_slow_threshold_us: 900,
        ..t
    };
    assert!(should_trace(&record(0), &slow, 1));
    let sampled = nexora_engine::runtime::TelemetrySettings {
        trace_sample_one_in: 10,
        trace_slow_threshold_us: 0,
        ..slow
    };
    assert!(should_trace(&record(0), &sampled, 20) && !should_trace(&record(0), &sampled, 21));
    let spans = spans_for(&record(2), [1; 16], "fixture");
    let names: Vec<&str> = spans.iter().map(|s| s.name.as_str()).collect();
    assert_eq!(
        names,
        vec![
            "dns.query",
            "nexora.filter",
            "nexora.cache",
            "nexora.upstream"
        ]
    );
    assert!(
        spans[1..]
            .iter()
            .all(|s| s.parent_span_id == spans[0].span_id)
    );
}

#[test]
fn logs_and_servfail_traces_reach_the_collector() {
    let rt = tokio::runtime::Runtime::new().unwrap();
    let sink = Sink::default();
    let listener = rt
        .block_on(tokio::net::TcpListener::bind("127.0.0.1:0"))
        .unwrap();
    let addr = listener.local_addr().unwrap();
    let s2 = sink.clone();
    rt.spawn(
        tonic::transport::Server::builder()
            .add_service(LogsServiceServer::new(s2.clone()))
            .add_service(TraceServiceServer::new(s2))
            .serve_with_incoming(tokio_stream::wrappers::TcpListenerStream::new(listener)),
    );
    let shared = shared_with_endpoint(&format!("http://{addr}"));
    for _ in 0..9 {
        push(&shared.querylog, &shared.metrics, record(0));
    }
    push(&shared.querylog, &shared.metrics, record(2));
    let _t = spawn_telemetry_thread(shared.clone());
    let deadline = Instant::now() + Duration::from_secs(5);
    while (sink.logs.load(Ordering::SeqCst) < 10 || sink.spans.load(Ordering::SeqCst) < 4)
        && Instant::now() < deadline
    {
        std::thread::sleep(Duration::from_millis(50));
    }
    assert_eq!(sink.logs.load(Ordering::SeqCst), 10);
    assert_eq!(sink.spans.load(Ordering::SeqCst), 4);
    assert_eq!(shared.metrics.dropped(Signal::Logs), 0);
}

#[test]
fn unreachable_collector_drops_with_counter_and_never_blocks_push() {
    let dead = std::net::TcpListener::bind("127.0.0.1:0")
        .unwrap()
        .local_addr()
        .unwrap();
    let shared = shared_with_endpoint(&format!("http://{dead}"));
    let _t = spawn_telemetry_thread(shared.clone());
    let start = Instant::now();
    for _ in 0..200_000 {
        push(&shared.querylog, &shared.metrics, record(0));
    }
    assert!(
        start.elapsed() < Duration::from_secs(1),
        "push blocked: {:?}",
        start.elapsed()
    );
    let deadline = Instant::now() + Duration::from_secs(10);
    while shared.metrics.dropped(Signal::Logs) == 0 && Instant::now() < deadline {
        std::thread::sleep(Duration::from_millis(50));
    }
    assert!(shared.metrics.dropped(Signal::Logs) > 0);
}

#[test]
fn metrics_endpoint_exposes_every_architecture_name() {
    use std::io::{Read, Write};
    let shared = shared_with_endpoint("");
    shared.metrics.workers[0].observe(Transport::Udp, 0, 120);
    let port = std::net::TcpListener::bind("127.0.0.1:0")
        .unwrap()
        .local_addr()
        .unwrap()
        .port();
    let addr: std::net::SocketAddr = format!("127.0.0.1:{port}").parse().unwrap();
    let rt = tokio::runtime::Runtime::new().unwrap();
    rt.spawn(serve_metrics(addr, shared.clone()));
    std::thread::sleep(Duration::from_millis(200));
    let mut s = std::net::TcpStream::connect(addr).unwrap();
    s.write_all(b"GET /metrics HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n")
        .unwrap();
    let mut body = String::new();
    s.read_to_string(&mut body).unwrap();
    assert!(body.starts_with("HTTP/1.1 200"));
    for name in [
        "nexora_queries_total{",
        "nexora_query_duration_seconds_bucket{",
        "nexora_cache_hits_total",
        "nexora_cache_misses_total",
        "nexora_cache_stale_served_total",
        "nexora_cache_entries",
        "nexora_cache_bytes",
        "nexora_filter_blocked_total{category=\"custom\"} 0",
        "nexora_filter_index_entries 0",
        "nexora_filter_index_bytes",
        "nexora_filter_index_max_bytes",
        "nexora_filter_index_build_seconds",
        "nexora_upstream_up{upstream=\"fixture\"}",
        "nexora_upstream_rtt_seconds{",
        "nexora_upstream_queries_total{",
        "nexora_upstream_failures_total{",
        "nexora_upstream_mismatched_replies_total",
        "nexora_export_dropped_total{signal=\"logs\"}",
        "nexora_config_version 1",
        "nexora_control_connected",
        "nexora_control_revoked 0",
        "nexora_control_cert_renewals_total 0",
    ] {
        assert!(body.contains(name), "missing {name}");
    }
    let stats = shared
        .metrics
        .stats(&shared.runtime.load(), &shared.recursor);
    assert_eq!(stats.queries_total, 1);
    assert_eq!(stats.duration_bucket_bounds_us.len(), 15);
}

#[test]
fn failed_export_counts_every_record_of_the_batch() {
    let dead = std::net::TcpListener::bind("127.0.0.1:0")
        .unwrap()
        .local_addr()
        .unwrap();
    let shared = shared_with_endpoint(&format!("http://{dead}"));
    for _ in 0..9 {
        push(&shared.querylog, &shared.metrics, record(0));
    }
    push(&shared.querylog, &shared.metrics, record(2));
    let _t = spawn_telemetry_thread(shared.clone());
    let deadline = Instant::now() + Duration::from_secs(10);
    while (shared.metrics.dropped(Signal::Logs) < 10 || shared.metrics.dropped(Signal::Traces) < 4)
        && Instant::now() < deadline
    {
        std::thread::sleep(Duration::from_millis(50));
    }
    assert_eq!(shared.metrics.dropped(Signal::Logs), 10);
    assert_eq!(shared.metrics.dropped(Signal::Traces), 4);
    let body = shared
        .metrics
        .render(&shared.runtime.load(), &shared.recursor);
    assert!(
        body.contains("nexora_export_dropped_total{signal=\"logs\"} 10"),
        "{body}"
    );
}

#[test]
fn blocked_records_carry_list_id_and_category() {
    use nexora_engine::filter::index::{ListKind, ListMeta};
    let meta = ListMeta {
        id: "0b6c3e2a-2d57-4a43-9a52-8f0e8bb3c1d1".into(),
        category: "gambling".into(),
        category_slot: 1,
        kind: ListKind::Block,
        invalid_lines: 0,
    };
    let mut r = record(0);
    r.filter = FilterOutcome::Blocked;
    r.filter_list = 0;
    let attrs = |lr: &opentelemetry_proto::tonic::logs::v1::LogRecord| {
        lr.attributes
            .iter()
            .filter(|kv| kv.key.starts_with("nexora.filter."))
            .map(|kv| (kv.key.clone(), format!("{:?}", kv.value)))
            .collect::<Vec<_>>()
    };
    let with = attrs(&log_record(&r, "fixture", "engine-uuid", "", Some(&meta)));
    assert_eq!(with.len(), 2, "{with:?}");
    assert!(
        with[0].0 == "nexora.filter.list_id"
            && with[0].1.contains("0b6c3e2a-2d57-4a43-9a52-8f0e8bb3c1d1"),
        "{with:?}"
    );
    assert!(
        with[1].0 == "nexora.filter.category" && with[1].1.contains("gambling"),
        "{with:?}"
    );
    assert!(
        attrs(&log_record(&record(0), "fixture", "engine-uuid", "", None)).is_empty(),
        "unblocked records carry no attribution"
    );
}
