use nexora_engine::edns::Transport;
use nexora_engine::proto::*;
use nexora_engine::server::Shared;
use nexora_engine::snapshot::{DirBlobs, apply};
use nexora_engine::telemetry::metrics::{Signal, serve_metrics};
use nexora_engine::telemetry::otlp::{log_record, should_trace, spans_for, spawn_telemetry_thread};
use nexora_engine::telemetry::querylog::{
    CacheOutcome, FilterOutcome, FilterSource, NO_FILTER_LIST, NO_RPZ_ZONE, NO_RULE, QueryRecord,
    push,
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
        filter_identity: None,
        rpz_identity: None,
        filter_source: FilterSource::None,
        filter_rule_offset: NO_RULE,
        rewrite_wildcard: false,
        rpz_zone: NO_RPZ_ZONE,
        acl_refused: 0,
        upstream_raced: 1,
    }
}

#[derive(Default, Clone)]
struct Sink {
    logs: Arc<AtomicUsize>,
    spans: Arc<AtomicUsize>,
    /// Every `nexora.upstream` attribute of the exported log records, in arrival order.
    upstreams: Arc<parking_lot::Mutex<Vec<String>>>,
    /// Full string attributes for publication-bound attribution regressions.
    attributes: Arc<parking_lot::Mutex<Vec<std::collections::BTreeMap<String, String>>>>,
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
        self.attributes.lock().extend(records().map(|r| {
            r.attributes
                .iter()
                .filter_map(|kv| match kv.value.as_ref()?.value.as_ref()? {
                    opentelemetry_proto::tonic::common::v1::any_value::Value::StringValue(v) => {
                        Some((kv.key.clone(), v.clone()))
                    }
                    _ => None,
                })
                .collect()
        }));
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
    // Original M7 S-17/T9 criterion: 400 ms, 20,000 records, zero drops. Four concurrent
    // exports have no throughput headroom at 10 batches/s; scheduling delays must use the
    // existing bounded ring instead of evicting queued batches while it still has room.
    let sink = Sink {
        delay: Duration::from_millis(400),
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
        "logs dropped behind a 400 ms collector"
    );
    assert_eq!(sink.logs.load(Ordering::SeqCst), 20_000);
    let peak = sink.peak.load(Ordering::SeqCst);
    assert!(peak > 1, "exports never overlapped");
    assert!(
        peak <= nexora_engine::telemetry::otlp::MAX_INFLIGHT,
        "export concurrency exceeded its bound: {peak}"
    );
}

#[test]
fn slow_collector_250ms_does_not_drop_with_concurrent_exports() {
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
    let lr = log_record(&record(0), "fixture", "engine-uuid", "", None, "");
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
    let with = attrs(&log_record(
        &r,
        "fixture",
        "engine-uuid",
        "",
        Some(&meta),
        "",
    ));
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
        attrs(&log_record(
            &record(0),
            "fixture",
            "engine-uuid",
            "",
            None,
            ""
        ))
        .is_empty(),
        "unblocked records carry no attribution"
    );
}

#[test]
fn m6_attribution_attributes_are_emitted_only_when_set() {
    use nexora_engine::filter::index::{ListKind, ListMeta};
    use nexora_engine::telemetry::querylog::{ACL_AUTHORITATIVE, FilterSource};
    let attrs = |lr: &opentelemetry_proto::tonic::logs::v1::LogRecord| {
        lr.attributes
            .iter()
            .map(|kv| (kv.key.clone(), format!("{:?}", kv.value)))
            .collect::<std::collections::BTreeMap<_, _>>()
    };
    let plain = attrs(&log_record(&record(0), "fx", "e", "", None, ""));
    for k in [
        "nexora.filter.source",
        "nexora.filter.rule",
        "nexora.rpz_zone",
        "nexora.acl.refused",
        "nexora.upstream_raced",
    ] {
        assert!(!plain.contains_key(k), "{k} on an unfiltered record");
    }
    let meta = ListMeta {
        id: "allowlist".into(),
        category: "".into(),
        category_slot: 0,
        kind: ListKind::Allow,
        invalid_lines: 0,
    };
    let mut allowed = record(0);
    allowed.filter = FilterOutcome::Allowed;
    allowed.filter_source = FilterSource::Allowlist;
    allowed.filter_list = 0;
    allowed.filter_rule_offset = 8; // "\x07example\x03com" -> "com"
    let a = attrs(&log_record(&allowed, "fx", "e", "", Some(&meta), ""));
    assert!(a["nexora.filter.source"].contains("allowlist"), "{a:?}");
    assert!(a["nexora.filter.rule"].contains("\"com\""), "{a:?}");
    assert!(a["nexora.filter.list_id"].contains("allowlist"), "{a:?}");
    let mut rewrite = record(0);
    rewrite.filter = FilterOutcome::Rewritten;
    rewrite.filter_source = FilterSource::Rewrite;
    rewrite.filter_rule_offset = 8;
    rewrite.rewrite_wildcard = true;
    assert!(
        attrs(&log_record(&rewrite, "fx", "e", "", None, ""))["nexora.filter.rule"]
            .contains("*.com")
    );
    let mut refused = record(5);
    refused.filter_source = FilterSource::Acl;
    refused.acl_refused = ACL_AUTHORITATIVE;
    refused.upstream_raced = 3;
    let r = attrs(&log_record(&refused, "", "e", "", None, "rpz-zone-id"));
    assert!(
        r["nexora.acl.refused"].contains("authoritative")
            && r["nexora.filter.source"].contains("acl"),
        "{r:?}"
    );
    assert!(r["nexora.upstream_raced"].contains("IntValue(3)"), "{r:?}");
    assert!(r["nexora.rpz_zone"].contains("rpz-zone-id"), "{r:?}");
}

#[test]
fn m6_stats_fields_are_filled() {
    let shared = shared_with_endpoint("");
    let rt = shared.runtime.load();
    let s = shared.metrics.stats(&rt, &shared.recursor);
    assert_eq!(s.queries_by_rcode.len(), 7);
    assert_eq!(s.queries_by_transport.len(), 5);
    assert_eq!(s.answers_by_route.len(), 8);
    assert_eq!(
        s.miss_duration_bucket_counts.len(),
        s.duration_bucket_bounds_us.len()
    );
    assert_eq!(
        s.race_duration_bucket_counts.len(),
        s.duration_bucket_bounds_us.len()
    );
    assert_eq!(s.acl_refused.len(), 2);
    assert_eq!(s.open_connections.len(), 4);
    assert!(
        s.started_unix_ms > 0 && s.process_resident_bytes > 0 && s.process_cpu_seconds_total >= 0.0
    );
}

#[test]
fn m6_observe_record_counts_route_miss_latency_acl_and_rewrites() {
    let shared = shared_with_endpoint("");
    let w = &shared.metrics.workers[0];
    let mut hit = record(0);
    hit.cache = CacheOutcome::Hit;
    w.observe_record(&hit);
    w.observe_record(&record(0)); // miss, route 1: recursive, 950 us
    let mut rewritten = record(0);
    rewritten.cache = CacheOutcome::None;
    rewritten.filter = FilterOutcome::Rewritten;
    w.observe_record(&rewritten);
    let mut passthru = record(0);
    passthru.route = 0;
    passthru.rpz_action = 3;
    w.observe_record(&passthru);
    let mut rpz = record(0);
    rpz.rpz_action = 1;
    w.observe_record(&rpz);
    let mut refused = record(5);
    refused.cache = CacheOutcome::None;
    refused.acl_refused = nexora_engine::telemetry::querylog::ACL_RECURSION;
    w.observe_record(&refused);
    let s = shared
        .metrics
        .stats(&shared.runtime.load(), &shared.recursor);
    let route = |k: &str| s.answers_by_route[k];
    assert_eq!(
        (
            route("cache"),
            route("recursive"),
            route("rewritten"),
            route("forwarded"),
            route("rpz")
        ),
        (1, 1, 1, 1, 1),
        "{:?}",
        s.answers_by_route
    );
    assert_eq!(s.filter_rewritten_total, 1);
    assert_eq!(
        (s.acl_refused["recursion"], s.acl_refused["authoritative"]),
        (1, 0)
    );
    // Misses: the plain miss, the passthru miss and the rpz miss, all 950 us (bucket <= 1000).
    assert_eq!(s.miss_duration_bucket_counts[3], 0);
    assert_eq!(s.miss_duration_bucket_counts[4], 3);
}

#[test]
fn m6_parallel_strategy_maps_with_capped_max() {
    use nexora_engine::upstream::Strategy;
    let shared = shared_with_endpoint("");
    assert_eq!(shared.runtime.load().upstreams.strategy, Strategy::Ordered);
    let tmp = tempfile::tempdir().unwrap();
    let snap = ConfigSnapshot {
        version: 2,
        resolver: Some(ResolverConfig {
            strategy: UpstreamStrategy::Parallel as i32,
            parallel_max: 20,
        }),
        cache: Some(CacheConfig {
            max_bytes: 2 << 20,
            max_ttl: 86400,
            negative_max_ttl: 60,
            ..Default::default()
        }),
        filter: Some(FilterConfig::default()),
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
    assert_eq!(
        shared.runtime.load().upstreams.strategy,
        Strategy::Parallel { max: 8 }
    );
}

/// Exercise the actual worker decision capture; no exporter is running until after publication.
fn queue_query(shared: &Arc<Shared>, ctx: &nexora_engine::server::WorkerCtx, name: &str) {
    use hickory_proto::op::{Message, MessageType, OpCode, Query};
    use hickory_proto::rr::{Name, RecordType};
    use hickory_proto::serialize::binary::BinEncodable;
    use nexora_engine::server::{FastOutcome, handle_packet};
    let mut query = Message::new(1, MessageType::Query, OpCode::Query);
    query.metadata.recursion_desired = true;
    query.add_query(Query::query(Name::from_ascii(name).unwrap(), RecordType::A));
    let query = query.to_bytes().unwrap();
    let mut out = [0; 1232];
    assert!(matches!(
        handle_packet(
            ctx,
            &shared.runtime.load(),
            &query,
            "127.0.0.1:53000".parse().unwrap(),
            Transport::Udp,
            &mut out,
        ),
        FastOutcome::Reply(_)
    ));
}

fn exported(sink: &Sink, count: usize) -> Vec<std::collections::BTreeMap<String, String>> {
    let deadline = Instant::now() + Duration::from_secs(5);
    while sink.logs.load(Ordering::SeqCst) < count && Instant::now() < deadline {
        std::thread::sleep(Duration::from_millis(20));
    }
    assert_eq!(sink.logs.load(Ordering::SeqCst), count);
    sink.attributes.lock().clone()
}

#[test]
fn queued_rpz_identity_survives_reorder_remove_and_rebuild() {
    use nexora_engine::recursor::rpz::{
        index::{RpzSet, RpzZoneIndex},
        parse::parse_rpz_text,
    };
    let sink = Sink::default();
    let (_rt, addr) = serve(sink.clone());
    let shared = shared_with_endpoint(&format!("http://{addr}"));
    // The common telemetry fixture has no ACL clients; allow the local query fixture explicitly.
    let mut runtime = nexora_engine::runtime::Runtime::initial();
    runtime.acl = nexora_engine::acl::Acl::any();
    runtime.telemetry = shared.runtime.load().telemetry.clone();
    shared.runtime.store(Arc::new(runtime));
    let zone = |id: &str, serial: u32| {
        let parsed = parse_rpz_text(
            &hickory_proto::rr::Name::from_ascii("rpz.test.").unwrap(),
            &format!("$TTL 60\n@ SOA ns h {serial} 60 60 60 60\nblocked.test CNAME .\n"),
        )
        .unwrap();
        Arc::new(RpzZoneIndex::build(id, &parsed, 0))
    };
    let a = zone("original-zone", 1);
    let b = zone("replacement-zone", 1);
    let retired = Arc::downgrade(&a);
    let ctx = nexora_engine::server::WorkerCtx::new(0, shared.clone());
    shared
        .recursor
        .rpz
        .publish(RpzSet::new(vec![a.clone(), b.clone()]));
    queue_query(&shared, &ctx, "blocked.test.");
    shared.recursor.rpz.publish(RpzSet::new(vec![b, a]));
    queue_query(&shared, &ctx, "blocked.test.");
    // A same-id rebuild must not retain either old rule index, nor affect earlier records.
    shared
        .recursor
        .rpz
        .publish(RpzSet::new(vec![zone("original-zone", 2)]));
    queue_query(&shared, &ctx, "blocked.test.");
    shared.recursor.rpz.publish(RpzSet::default());
    assert!(
        retired.upgrade().is_none(),
        "query records pinned a retired RPZ index"
    );
    let _t = spawn_telemetry_thread(shared.clone());
    let records = exported(&sink, 3);
    let ids: Vec<_> = records
        .iter()
        .map(|r| r.get("nexora.rpz_zone").map(String::as_str))
        .collect();
    assert_eq!(
        ids,
        vec![
            Some("original-zone"),
            Some("replacement-zone"),
            Some("original-zone")
        ]
    );
    assert!(
        records
            .iter()
            .all(|r| r.get("nexora.filter.source").map(String::as_str) == Some("rpz"))
    );
    assert_eq!(shared.metrics.dropped(Signal::Logs), 0);
}

#[test]
fn queued_list_identity_survives_rebuild_and_history_eviction() {
    use nexora_engine::filter::index::{IndexOptions, ListInput, ListKind};
    use nexora_engine::filter::{BlockReply, FilterIndex, PolicyTable};
    use nexora_engine::runtime::Runtime;
    let sink = Sink::default();
    let (_rt, addr) = serve(sink.clone());
    let shared = shared_with_endpoint(&format!("http://{addr}"));
    let settings = shared.runtime.load().telemetry.clone();
    let ctx = nexora_engine::server::WorkerCtx::new(0, shared.clone());
    let mut retired = Vec::new();
    for (id, category) in [
        ("original-list", "ads"),
        ("replacement-list", "malware"),
        ("original-list", "phishing"),
    ] {
        let index = Arc::new(
            FilterIndex::build(
                &[ListInput {
                    id,
                    category,
                    category_slot: 1,
                    kind: ListKind::Block,
                    text: b"blocked.test\n",
                }],
                &IndexOptions::new(64 << 20),
            )
            .unwrap(),
        );
        retired.push(Arc::downgrade(&index));
        let mut runtime = Runtime::initial();
        runtime.acl = nexora_engine::acl::Acl::any();
        runtime.telemetry = settings.clone();
        runtime.policy = PolicyTable::global_only(
            Arc::new(index.view(&[0], &[])),
            BlockReply {
                mode: nexora_engine::filter::BlockMode::NxDomain,
                ttl: 60,
            },
        );
        runtime.filter_index = index;
        // The manually constructed fixture must not look like the empty snapshot's index key.
        runtime.filter_key = format!("fixture:{id}:{category}");
        shared.runtime.store(Arc::new(runtime));
        // Second decision goes through the decision cache as well.
        queue_query(&shared, &ctx, "blocked.test.");
        queue_query(&shared, &ctx, "blocked.test.");
    }
    for version in 2..=12 {
        apply_reordered(&shared, version, &["fixture"]);
    }
    assert!(
        retired.iter().all(|w| w.upgrade().is_none()),
        "query records pinned retired filter indexes"
    );
    let _t = spawn_telemetry_thread(shared.clone());
    let records = exported(&sink, 6);
    let expected = [
        ("original-list", "ads"),
        ("replacement-list", "malware"),
        ("original-list", "phishing"),
    ];
    for (r, (id, category)) in records
        .iter()
        .zip(expected.into_iter().flat_map(|e| [e, e]))
    {
        assert_eq!(r.get("nexora.filter.list_id").map(String::as_str), Some(id));
        assert_eq!(
            r.get("nexora.filter.category").map(String::as_str),
            Some(category)
        );
        assert_eq!(
            r.get("nexora.filter.source").map(String::as_str),
            Some("category")
        );
        assert_eq!(
            r.get("nexora.filter.rule").map(String::as_str),
            Some("blocked.test")
        );
    }
    assert_eq!(shared.metrics.dropped(Signal::Logs), 0);
}

#[test]
fn queued_allowlist_cname_and_response_identities_survive_publication() {
    use hickory_proto::op::{Message, MessageType, OpCode, Query};
    use hickory_proto::rr::{
        Name, RData, Record, RecordType,
        rdata::{A, CNAME},
    };
    use hickory_proto::serialize::binary::{BinDecodable, BinEncodable};
    use nexora_engine::filter::index::{IndexOptions, ListInput, ListKind};
    use nexora_engine::filter::{BlockReply, FilterIndex, PolicyTable};
    use nexora_engine::recursor::rpz::{
        index::{RpzSet, RpzZoneIndex},
        parse::parse_rpz_text,
    };
    use nexora_engine::runtime::Runtime;
    use nexora_engine::server::{FastOutcome, WorkerCtx, handle_packet, resolve_miss};
    use nexora_engine::upstream::{Protocol, Strategy, UpstreamSet, UpstreamSpec};
    use std::rc::Rc;

    let socket = std::net::UdpSocket::bind("127.0.0.1:0").unwrap();
    socket
        .set_read_timeout(Some(Duration::from_secs(5)))
        .unwrap();
    let upstream = socket.local_addr().unwrap();
    let responder = std::thread::spawn(move || {
        let mut buf = [0; 4096];
        // Allowlist miss, filter CNAME, response IP/QNAME, and query RPZ CNAME target.
        for _ in 0..5 {
            let (n, peer) = socket.recv_from(&mut buf).unwrap();
            let mut m = Message::from_bytes(&buf[..n]).unwrap();
            m.metadata.message_type = MessageType::Response;
            m.metadata.recursion_available = true;
            let name = m.queries[0].name().clone();
            if matches!(name.to_ascii().as_str(), "cloak.test." | "rpz-cloak.test.") {
                let target = if name.to_ascii() == "cloak.test." {
                    "blocked.test."
                } else {
                    "rpz-blocked.test."
                };
                m.add_answer(Record::from_rdata(
                    name,
                    60,
                    RData::CNAME(CNAME(Name::from_ascii(target).unwrap())),
                ));
            } else {
                let last = if name.to_ascii() == "response.test." {
                    66
                } else {
                    1
                };
                m.add_answer(Record::from_rdata(
                    name,
                    60,
                    RData::A(A::new(192, 0, 2, last)),
                ));
            }
            socket.send_to(&m.to_bytes().unwrap(), peer).unwrap();
        }
    });
    let sink = Sink::default();
    let (_collector, addr) = serve(sink.clone());
    let shared = shared_with_endpoint(&format!("http://{addr}"));
    let index = Arc::new(
        FilterIndex::build(
            &[
                ListInput {
                    id: "blocked-original",
                    category: "ads",
                    category_slot: 1,
                    kind: ListKind::Block,
                    text: b"blocked.test\n",
                },
                ListInput {
                    id: "allowed-original",
                    category: "",
                    category_slot: 0,
                    kind: ListKind::Allow,
                    text: b"ok.blocked.test\n",
                },
            ],
            &IndexOptions::new(64 << 20),
        )
        .unwrap(),
    );
    let retired_list = Arc::downgrade(&index);
    let mut runtime = Runtime::initial();
    runtime.acl = nexora_engine::acl::Acl::any();
    runtime.telemetry = shared.runtime.load().telemetry.clone();
    runtime.upstreams = Arc::new(UpstreamSet::new(
        vec![UpstreamSpec {
            id: "fixture".into(),
            name: "fixture".into(),
            protocol: Protocol::Udp,
            addr: Some(upstream),
            tls_server_name: String::new(),
            doh_url: String::new(),
            timeout: Duration::from_secs(1),
            ca_pem: String::new(),
        }],
        Strategy::Ordered,
        None,
    ));
    runtime.policy = PolicyTable::global_only(
        Arc::new(index.view(&[0], &[1])),
        BlockReply {
            mode: nexora_engine::filter::BlockMode::NxDomain,
            ttl: 60,
        },
    );
    runtime.filter_index = index;
    shared.runtime.store(Arc::new(runtime));
    let zone = |id: &str| {
        Arc::new(RpzZoneIndex::build(id, &parse_rpz_text(
        &Name::from_ascii("rpz.test.").unwrap(),
        "$TTL 60\n@ SOA ns h 1 60 60 60 60\n32.66.2.0.192.rpz-ip CNAME .\nrewrite.test CNAME target.test.\nrpz-blocked.test CNAME .\n",
    ).unwrap(), 0))
    };
    let original = zone("response-original");
    let retired_rpz = Arc::downgrade(&original);
    shared.recursor.rpz.publish(RpzSet::new(vec![original]));
    let ctx = Rc::new(WorkerCtx::new(0, shared.clone()));
    let executor = tokio::runtime::Builder::new_current_thread()
        .enable_all()
        .build()
        .unwrap();
    let local = tokio::task::LocalSet::new();
    executor.block_on(local.run_until(async {
        for name in [
            "ok.blocked.test.",
            "cloak.test.",
            "response.test.",
            "rpz-cloak.test.",
            "rewrite.test.",
        ] {
            let mut q = Message::new(1, MessageType::Query, OpCode::Query);
            q.metadata.recursion_desired = true;
            q.add_query(Query::query(Name::from_ascii(name).unwrap(), RecordType::A));
            let wire = q.to_bytes().unwrap();
            let rt = shared.runtime.load_full();
            let mut out = [0; 1232];
            let job = match handle_packet(
                &ctx,
                &rt,
                &wire,
                "127.0.0.1:53000".parse().unwrap(),
                Transport::Udp,
                &mut out,
            ) {
                FastOutcome::Miss(job) => job,
                _ => panic!("expected miss for {name}"),
            };
            // Change the RPZ publication between the query decision and its CNAME chase.
            if name == "rewrite.test." {
                shared
                    .recursor
                    .rpz
                    .publish(RpzSet::new(vec![zone("replacement")]));
            }
            assert!(!resolve_miss(ctx.clone(), rt, job).await.is_empty());
        }
    }));
    responder.join().unwrap();
    shared.recursor.rpz.publish(RpzSet::default());
    let mut replacement = Runtime::initial();
    replacement.telemetry = shared.runtime.load().telemetry.clone();
    shared.runtime.store(Arc::new(replacement));
    assert!(retired_list.upgrade().is_none());
    assert!(retired_rpz.upgrade().is_none());
    let _t = spawn_telemetry_thread(shared.clone());
    let records = exported(&sink, 5);
    let get = |i: usize, key: &str| records[i].get(key).map(String::as_str);
    assert_eq!(get(0, "nexora.filter.source"), Some("allowlist"));
    assert_eq!(get(0, "nexora.filter.list_id"), Some("allowed-original"));
    assert_eq!(get(0, "nexora.filter.rule"), Some("ok.blocked.test"));
    assert_eq!(get(1, "nexora.filter.source"), Some("category"));
    assert_eq!(get(1, "nexora.filter.list_id"), Some("blocked-original"));
    assert_eq!(get(1, "nexora.filter.category"), Some("ads"));
    assert_eq!(get(1, "nexora.filter.rule"), None);
    for i in [2, 3, 4] {
        assert_eq!(get(i, "nexora.filter.source"), Some("rpz"));
        assert_eq!(get(i, "nexora.rpz_zone"), Some("response-original"));
    }
    assert_eq!(shared.metrics.dropped(Signal::Logs), 0);
}
