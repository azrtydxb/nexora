//! The `nexora-telemetry` thread: drains the query-log ring into bounded
//! batches and exports them as OTLP logs and traces, plus periodic OTLP
//! metrics. Every queue is bounded and every loss is counted; nothing here
//! can block a worker.

use super::metrics::Signal;
use super::querylog::{
    ACL_RECURSION, DNSSEC_NAMES, FilterSource, NO_FILTER_LIST, NO_RPZ_ZONE, NO_RULE, QueryRecord,
    ROUTE_NAMES, RPZ_NAMES, name_at,
};
use crate::filter::index::ListMeta;
use crate::runtime::{Runtime, TelemetrySettings};
use crate::server::Shared;
use crate::wire;
use hickory_proto::rr::RecordType;
use opentelemetry_proto::tonic::collector::logs::v1::ExportLogsServiceRequest;
use opentelemetry_proto::tonic::collector::logs::v1::logs_service_client::LogsServiceClient;
use opentelemetry_proto::tonic::collector::metrics::v1::ExportMetricsServiceRequest;
use opentelemetry_proto::tonic::collector::metrics::v1::metrics_service_client::MetricsServiceClient;
use opentelemetry_proto::tonic::collector::trace::v1::ExportTraceServiceRequest;
use opentelemetry_proto::tonic::collector::trace::v1::trace_service_client::TraceServiceClient;
use opentelemetry_proto::tonic::common::v1::{AnyValue, InstrumentationScope, KeyValue, any_value};
use opentelemetry_proto::tonic::logs::v1::{LogRecord, ResourceLogs, ScopeLogs, SeverityNumber};
use opentelemetry_proto::tonic::metrics::v1::{
    AggregationTemporality, Gauge, Metric, NumberDataPoint, ResourceMetrics, ScopeMetrics, Sum,
    metric, number_data_point,
};
use opentelemetry_proto::tonic::resource::v1::Resource;
use opentelemetry_proto::tonic::trace::v1::{
    ResourceSpans, ScopeSpans, Span, Status, span, status,
};
use rand::RngExt;
use std::collections::VecDeque;
use std::sync::Arc;
use std::sync::atomic::Ordering;
use std::thread::JoinHandle;
use std::time::{Duration, Instant, SystemTime};
use tonic::transport::{Channel, Endpoint};

pub const BATCH_MAX: usize = 1000;
pub const BATCH_INTERVAL: Duration = Duration::from_secs(1);
pub const MAX_QUEUED_BATCHES: usize = 8;
pub const EXPORT_TIMEOUT: Duration = Duration::from_secs(2);
pub const METRICS_INTERVAL: Duration = Duration::from_secs(15);
const DRAIN_INTERVAL: Duration = Duration::from_millis(100);
const CONNECT_TIMEOUT: Duration = Duration::from_millis(500);
const SCOPE: &str = "nexora-engine";

/// Starts the telemetry thread with its own `current_thread` runtime.
pub fn spawn_telemetry_thread(shared: Arc<Shared>) -> JoinHandle<()> {
    std::thread::Builder::new()
        .name("nexora-telemetry".into())
        .spawn(move || {
            let runtime = tokio::runtime::Builder::new_current_thread()
                .enable_all()
                .build()
                .expect("telemetry runtime");
            runtime.block_on(run(shared));
        })
        .expect("spawn nexora-telemetry")
}

/// One OTLP log record with the attributes of `docs/architecture.md`; attribution attributes
/// (`nexora.filter.source`, `.rule`, `nexora.acl.refused`, `nexora.rpz_zone`,
/// `nexora.upstream_raced`) only when set.
pub fn log_record(
    r: &QueryRecord,
    upstream_name: &str,
    engine_id: &str,
    policy_group: &str,
    filter_list: Option<&ListMeta>,
    rpz_zone_id: &str,
) -> LogRecord {
    let name = presentation(r.name.as_wire());
    let mut record = LogRecord {
        time_unix_nano: r.unix_micros.saturating_mul(1000),
        severity_number: SeverityNumber::Info as i32,
        severity_text: "INFO".into(),
        body: Some(string(name.clone())),
        attributes: vec![
            kv("client.address", r.client.to_string()),
            kv("dns.question.name", name),
            kv("dns.question.type", qtype_name(r.qtype)),
            kv("dns.response.code", rcode_name(r.rcode)),
            kv("nexora.cache", r.cache.as_str()),
            kv("nexora.filter", r.filter.as_str()),
            kv("nexora.policy.group", policy_group),
            kv("nexora.upstream", upstream_name),
            KeyValue {
                key: "nexora.duration_us".into(),
                value: Some(AnyValue {
                    value: Some(any_value::Value::IntValue(i64::from(r.duration_us))),
                }),
                ..Default::default()
            },
            kv("nexora.transport", r.transport.as_str()),
            kv("nexora.engine.id", engine_id),
            kv("nexora.route", name_at(&ROUTE_NAMES, r.route)),
            kv("nexora.dnssec", name_at(&DNSSEC_NAMES, r.dnssec)),
            kv("nexora.rpz", name_at(&RPZ_NAMES, r.rpz_action)),
        ],
        ..Default::default()
    };
    if let Some(meta) = filter_list {
        record.attributes.extend([
            kv("nexora.filter.list_id", &*meta.id),
            kv("nexora.filter.category", &*meta.category),
        ]);
    }
    if r.filter_source != FilterSource::None {
        record
            .attributes
            .push(kv("nexora.filter.source", r.filter_source.as_str()));
    }
    if r.filter_rule_offset != NO_RULE
        && let Some(suffix) = r.name.as_wire().get(usize::from(r.filter_rule_offset)..)
        && !suffix.is_empty()
    {
        let rule = presentation(suffix);
        let prefix = if r.rewrite_wildcard { "*." } else { "" };
        record.attributes.push(kv(
            "nexora.filter.rule",
            format!("{prefix}{}", rule.trim_end_matches('.')),
        ));
    }
    if r.acl_refused != 0 {
        let acl = if r.acl_refused == ACL_RECURSION {
            "recursion"
        } else {
            "authoritative"
        };
        record.attributes.push(kv("nexora.acl.refused", acl));
    }
    if !rpz_zone_id.is_empty() {
        record.attributes.push(kv("nexora.rpz_zone", rpz_zone_id));
    }
    if r.upstream_raced > 1 {
        record.attributes.push(KeyValue {
            key: "nexora.upstream_raced".into(),
            value: Some(AnyValue {
                value: Some(any_value::Value::IntValue(i64::from(r.upstream_raced))),
            }),
            ..Default::default()
        });
    }
    record
}

/// SERVFAIL always; otherwise slower than a non-zero threshold, or every
/// `trace_sample_one_in`-th record.
pub fn should_trace(r: &QueryRecord, t: &TelemetrySettings, seq: u64) -> bool {
    r.rcode == wire::RCODE_SERVFAIL
        || (t.trace_slow_threshold_us > 0 && r.duration_us > t.trace_slow_threshold_us)
        || (t.trace_sample_one_in > 0 && seq.is_multiple_of(u64::from(t.trace_sample_one_in)))
}

/// A `dns.query` root span and its stage children, from the record's offsets.
pub fn spans_for(r: &QueryRecord, trace_id: [u8; 16], upstream_name: &str) -> Vec<Span> {
    // `unix_micros` is taken when the reply is written.
    let arrival_ns = r
        .unix_micros
        .saturating_sub(u64::from(r.duration_us))
        .saturating_mul(1000);
    let mut rng = rand::rng();
    let mut make = |name: &str, kind: span::SpanKind, from_us: u32, to_us: u32, parent: &[u8]| {
        let mut id = [0u8; 8];
        rng.fill(&mut id[..]);
        Span {
            trace_id: trace_id.to_vec(),
            span_id: id.to_vec(),
            parent_span_id: parent.to_vec(),
            name: name.into(),
            kind: kind as i32,
            start_time_unix_nano: arrival_ns + u64::from(from_us) * 1000,
            end_time_unix_nano: arrival_ns + u64::from(to_us.max(from_us)) * 1000,
            ..Default::default()
        }
    };
    let name = presentation(r.name.as_wire());
    let mut root = make("dns.query", span::SpanKind::Server, 0, r.duration_us, &[]);
    root.attributes = vec![
        kv("dns.question.name", name),
        kv("dns.question.type", qtype_name(r.qtype)),
        kv("dns.response.code", rcode_name(r.rcode)),
    ];
    if r.rcode == wire::RCODE_SERVFAIL {
        root.status = Some(Status {
            code: status::StatusCode::Error as i32,
            ..Default::default()
        });
    }
    let parent = root.span_id.clone();
    let filter = make(
        "nexora.filter",
        span::SpanKind::Internal,
        0,
        r.filter_us,
        &parent,
    );
    let cache_end = r.filter_us.saturating_add(r.cache_us);
    let cache = make(
        "nexora.cache",
        span::SpanKind::Internal,
        r.filter_us,
        cache_end,
        &parent,
    );
    let upstream_end = r.upstream_start_us.saturating_add(r.upstream_us);
    let mut upstream = make(
        "nexora.upstream",
        span::SpanKind::Client,
        r.upstream_start_us,
        upstream_end,
        &parent,
    );
    upstream.attributes = vec![kv("nexora.upstream", upstream_name)];
    vec![root, filter, cache, upstream]
}

pub fn resource(shared: &Shared) -> Resource {
    Resource {
        attributes: vec![
            kv("service.name", "nexora-engine"),
            kv("nexora.engine.id", shared.engine_id.load().as_str()),
            kv("host.name", shared.node_name.load().as_str()),
        ],
        ..Default::default()
    }
}

fn string(s: impl Into<String>) -> AnyValue {
    AnyValue {
        value: Some(any_value::Value::StringValue(s.into())),
    }
}

fn kv(key: &str, value: impl Into<String>) -> KeyValue {
    KeyValue {
        key: key.into(),
        value: Some(string(value)),
        ..Default::default()
    }
}

fn scope() -> Option<InstrumentationScope> {
    Some(InstrumentationScope {
        name: SCOPE.into(),
        version: env!("CARGO_PKG_VERSION").into(),
        ..Default::default()
    })
}

fn rcode_name(rcode: u8) -> String {
    match rcode {
        0..=5 => super::metrics::RCODE_LABELS[usize::from(rcode)].into(),
        _ => format!("RCODE{rcode}"),
    }
}

fn qtype_name(qtype: u16) -> String {
    match RecordType::from(qtype) {
        RecordType::Unknown(n) => format!("TYPE{n}"),
        t => t.to_string(),
    }
}

/// Presentation format of a validated wire name, with a trailing dot and
/// RFC 1035 escapes for dots, backslashes and non-printable octets.
fn presentation(wire: &[u8]) -> String {
    let mut out = String::with_capacity(wire.len() + 1);
    let mut i = 0;
    while let Some(&len) = wire.get(i) {
        if len == 0 {
            break;
        }
        for &b in wire
            .get(i + 1..i + 1 + usize::from(len))
            .unwrap_or_default()
        {
            match b {
                b'.' | b'\\' => {
                    out.push('\\');
                    out.push(char::from(b));
                }
                0x21..=0x7e => out.push(char::from(b)),
                _ => out.push_str(&format!("\\{b:03}")),
            }
        }
        out.push('.');
        i += 1 + usize::from(len);
    }
    if out.is_empty() {
        out.push('.');
    }
    out
}

fn upstream_name<'a>(rt: &'a Runtime, r: &QueryRecord) -> &'a str {
    // debt: the index is resolved against the runtime current at drain time
    // (at most 100 ms after the reply); a reload that reorders upstreams in
    // that window mislabels those records. Revisit if records carry names.
    rt.upstreams
        .specs
        .get(usize::from(r.upstream))
        .map_or("", |s| s.name.as_str())
}

#[derive(Default)]
struct Batch {
    logs: Vec<LogRecord>,
    spans: Vec<Span>,
    opened: Option<Instant>,
}

enum Dest {
    /// Nowhere configured: data is discarded without counting.
    Discard,
    /// Configured but unusable: data is dropped and counted.
    Unavailable,
    Channel(Channel),
}

struct Exporter {
    shared: Arc<Shared>,
    current: Batch,
    queue: VecDeque<Batch>,
    seq: u64,
    otlp: Option<(String, Option<Channel>)>,
    started_ns: u64,
}

async fn run(shared: Arc<Shared>) {
    let mut ex = Exporter {
        shared,
        current: Batch::default(),
        queue: VecDeque::with_capacity(MAX_QUEUED_BATCHES),
        seq: 0,
        otlp: None,
        started_ns: unix_nanos(),
    };
    let mut drain = tokio::time::interval(DRAIN_INTERVAL);
    drain.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Skip);
    let mut metrics = tokio::time::interval_at(
        tokio::time::Instant::now() + METRICS_INTERVAL,
        METRICS_INTERVAL,
    );
    let mut inflight: Option<tokio::task::JoinHandle<()>> = None;
    loop {
        tokio::select! {
            _ = drain.tick() => {}
            _ = metrics.tick() => ex.export_metrics(),
            _ = async { inflight.as_mut().expect("guarded by the precondition").await },
                if inflight.is_some() => {}
        }
        if inflight.as_ref().is_some_and(|h| h.is_finished()) {
            inflight = None;
        }
        ex.drain();
        // debt: one batch export at a time caps log throughput at BATCH_MAX
        // records per collector round trip; revisit if drops show at scale.
        if inflight.is_none()
            && let Some(batch) = ex.queue.pop_front()
        {
            inflight = Some(ex.export_batch(batch));
        }
    }
}

fn unix_nanos() -> u64 {
    SystemTime::now()
        .duration_since(SystemTime::UNIX_EPOCH)
        .map_or(0, |d| d.as_nanos() as u64)
}

impl Exporter {
    /// Pops up to `BATCH_MAX` records into the current batch and closes it
    /// when full or older than `BATCH_INTERVAL`.
    fn drain(&mut self) {
        let rt = self.shared.runtime.load();
        let t = &rt.telemetry;
        let keep_logs = t.querylog_to_management || !t.otlp_endpoint.is_empty();
        let keep_traces = !t.otlp_endpoint.is_empty();
        let engine_id = self.shared.engine_id.load();
        let rpz = self.shared.recursor.rpz.set.load();
        for _ in 0..BATCH_MAX {
            let Some(r) = self.shared.querylog.pop() else {
                break;
            };
            self.seq = self.seq.wrapping_add(1);
            if !keep_logs && !keep_traces {
                continue;
            }
            let upstream = upstream_name(&rt, &r);
            if keep_traces && should_trace(&r, t, self.seq) {
                let mut trace_id = [0u8; 16];
                rand::rng().fill(&mut trace_id[..]);
                self.current.spans.extend(spans_for(&r, trace_id, upstream));
            }
            if keep_logs {
                let group = rt.policy.group(r.policy_group).map_or("", |g| g.group_id());
                // Only a list of the index build that decided names the record's list.
                let list = (r.filter_list != NO_FILTER_LIST
                    && rt.filter_index.generation() == r.filter_generation)
                    .then(|| rt.filter_index.lists().get(usize::from(r.filter_list)))
                    .flatten();
                let rpz_zone = if r.rpz_zone == NO_RPZ_ZONE {
                    ""
                } else {
                    // debt: resolved against the RPZ set current at drain time, like upstream
                    // names; a publication that reorders zones in that window mislabels records.
                    rpz.zones
                        .get(usize::from(r.rpz_zone))
                        .map_or("", |z| z.id.as_str())
                };
                self.current
                    .logs
                    .push(log_record(&r, upstream, &engine_id, group, list, rpz_zone));
            }
            self.current.opened.get_or_insert_with(Instant::now);
            if self.current.logs.len() >= BATCH_MAX {
                self.close();
            }
        }
        if self
            .current
            .opened
            .is_some_and(|o| o.elapsed() >= BATCH_INTERVAL)
        {
            self.close();
        }
    }

    fn close(&mut self) {
        let batch = std::mem::take(&mut self.current);
        if batch.logs.is_empty() && batch.spans.is_empty() {
            return;
        }
        if self.queue.len() >= MAX_QUEUED_BATCHES
            && let Some(oldest) = self.queue.pop_front()
        {
            self.count(Signal::Logs, oldest.logs.len());
            self.count(Signal::Traces, oldest.spans.len());
        }
        self.queue.push_back(batch);
    }

    fn count(&self, s: Signal, n: usize) {
        self.shared.metrics.export_dropped[s as usize].fetch_add(n as u64, Ordering::Relaxed);
    }

    /// The OTLP collector channel, rebuilt whenever the endpoint changes.
    fn otlp_dest(&mut self, endpoint: &str) -> Dest {
        if endpoint.is_empty() {
            return Dest::Discard;
        }
        if self.otlp.as_ref().is_none_or(|(e, _)| e != endpoint) {
            let channel = Endpoint::from_shared(endpoint.to_owned()).ok().map(|e| {
                e.connect_timeout(CONNECT_TIMEOUT)
                    .timeout(EXPORT_TIMEOUT)
                    .connect_lazy()
            });
            self.otlp = Some((endpoint.to_owned(), channel));
        }
        match &self.otlp {
            Some((_, Some(channel))) => Dest::Channel(channel.clone()),
            _ => Dest::Unavailable,
        }
    }

    fn export_batch(&mut self, batch: Batch) -> tokio::task::JoinHandle<()> {
        let rt = self.shared.runtime.load_full();
        let traces = self.otlp_dest(&rt.telemetry.otlp_endpoint);
        let logs = if rt.telemetry.querylog_to_management {
            match self.shared.mgmt_channel.load_full() {
                Some(channel) => Dest::Channel((*channel).clone()),
                None => Dest::Unavailable,
            }
        } else {
            self.otlp_dest(&rt.telemetry.otlp_endpoint)
        };
        let shared = self.shared.clone();
        tokio::spawn(async move {
            let res = resource(&shared);
            let drop = |s: Signal, n: usize| {
                shared.metrics.export_dropped[s as usize].fetch_add(n as u64, Ordering::Relaxed);
            };
            let n = batch.logs.len();
            if n > 0 {
                match logs {
                    Dest::Discard => {}
                    Dest::Unavailable => drop(Signal::Logs, n),
                    Dest::Channel(channel) => {
                        let req = ExportLogsServiceRequest {
                            resource_logs: vec![ResourceLogs {
                                resource: Some(res.clone()),
                                scope_logs: vec![ScopeLogs {
                                    scope: scope(),
                                    log_records: batch.logs,
                                    ..Default::default()
                                }],
                                ..Default::default()
                            }],
                        };
                        let mut client = LogsServiceClient::new(channel);
                        let call = client.export(req);
                        if !matches!(tokio::time::timeout(EXPORT_TIMEOUT, call).await, Ok(Ok(_))) {
                            drop(Signal::Logs, n);
                        }
                    }
                }
            }
            let n = batch.spans.len();
            if n > 0 {
                match traces {
                    Dest::Discard => {}
                    Dest::Unavailable => drop(Signal::Traces, n),
                    Dest::Channel(channel) => {
                        let req = ExportTraceServiceRequest {
                            resource_spans: vec![ResourceSpans {
                                resource: Some(res),
                                scope_spans: vec![ScopeSpans {
                                    scope: scope(),
                                    spans: batch.spans,
                                    ..Default::default()
                                }],
                                ..Default::default()
                            }],
                        };
                        let mut client = TraceServiceClient::new(channel);
                        let call = client.export(req);
                        if !matches!(tokio::time::timeout(EXPORT_TIMEOUT, call).await, Ok(Ok(_))) {
                            drop(Signal::Traces, n);
                        }
                    }
                }
            }
        })
    }

    /// Pushes the summed counters; bounded by `EXPORT_TIMEOUT`, well under
    /// `METRICS_INTERVAL`, so these tasks never pile up.
    fn export_metrics(&mut self) {
        let rt = self.shared.runtime.load_full();
        let Dest::Channel(channel) = self.otlp_dest(&rt.telemetry.otlp_endpoint) else {
            if !rt.telemetry.otlp_endpoint.is_empty() {
                self.count(Signal::Metrics, 1);
            }
            return;
        };
        let t = self.shared.metrics.totals();
        let (start, now) = (self.started_ns, unix_nanos());
        let point = |value: i64, attributes: Vec<KeyValue>| NumberDataPoint {
            attributes,
            start_time_unix_nano: start,
            time_unix_nano: now,
            value: Some(number_data_point::Value::AsInt(value)),
            ..Default::default()
        };
        let sum = |name: &str, value: u64| Metric {
            name: name.into(),
            data: Some(metric::Data::Sum(Sum {
                data_points: vec![point(value as i64, Vec::new())],
                aggregation_temporality: AggregationTemporality::Cumulative as i32,
                is_monotonic: true,
            })),
            ..Default::default()
        };
        let clock_now = crate::clock::now_secs();
        let up = Metric {
            name: "nexora.upstream.up".into(),
            data: Some(metric::Data::Gauge(Gauge {
                data_points: rt
                    .upstreams
                    .specs
                    .iter()
                    .zip(&rt.upstreams.health)
                    .map(|(spec, h)| {
                        point(
                            i64::from(h.is_up(clock_now)),
                            vec![kv("nexora.upstream", spec.name.as_str())],
                        )
                    })
                    .collect(),
            })),
            ..Default::default()
        };
        let req = ExportMetricsServiceRequest {
            resource_metrics: vec![ResourceMetrics {
                resource: Some(resource(&self.shared)),
                scope_metrics: vec![ScopeMetrics {
                    scope: scope(),
                    metrics: vec![
                        sum("nexora.queries", t.queries_total()),
                        sum("nexora.cache.hits", t.cache_hits),
                        sum("nexora.cache.misses", t.cache_misses),
                        sum("nexora.filter.blocked", t.filter_blocked),
                        up,
                    ],
                    ..Default::default()
                }],
                ..Default::default()
            }],
        };
        let shared = self.shared.clone();
        tokio::spawn(async move {
            let mut client = MetricsServiceClient::new(channel);
            let call = client.export(req);
            if !matches!(tokio::time::timeout(EXPORT_TIMEOUT, call).await, Ok(Ok(_))) {
                shared.metrics.export_dropped[Signal::Metrics as usize]
                    .fetch_add(1, Ordering::Relaxed);
            }
        });
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn presentation_escapes_and_roots() {
        assert_eq!(presentation(b"\x00"), ".");
        assert_eq!(presentation(b"\x07example\x03com\x00"), "example.com.");
        assert_eq!(presentation(b"\x03a.b\x02\\\x01\x00"), "a\\.b.\\\\\\001.");
        assert_eq!(rcode_name(2), "SERVFAIL");
        assert_eq!(rcode_name(9), "RCODE9");
        assert_eq!(qtype_name(28), "AAAA");
        assert_eq!(qtype_name(65280), "TYPE65280");
    }
}
