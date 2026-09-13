//! Per-worker cache-padded counters, summed at scrape time, and the
//! Prometheus endpoint that renders them.

use crate::clock;
use crate::edns::Transport;
use crate::proto::{DnssecStats, RecursionStats, Stats, TrustAnchorState, UpstreamStatus};
use crate::recursor::RecursorState;
use crate::runtime::Runtime;
use crate::server::Shared;
use bytes::Bytes;
use crossbeam_utils::CachePadded;
use http_body_util::Full;
use hyper::{Method, Request, Response, StatusCode};
use hyper_util::rt::{TokioIo, TokioTimer};
use prometheus_client::encoding::{EncodeMetric, MetricEncoder, NoLabelSet, text};
use prometheus_client::metrics::MetricType;
use prometheus_client::metrics::counter::{ConstCounter, Counter as PromCounter};
use prometheus_client::metrics::family::Family;
use prometheus_client::metrics::gauge::{ConstGauge, Gauge};
use prometheus_client::registry::Registry;
use std::array;
use std::convert::Infallible;
use std::net::SocketAddr;
use std::sync::Arc;
use std::sync::atomic::{AtomicBool, AtomicI64, AtomicU64, Ordering};
use std::time::{Duration, SystemTime};

pub const DURATION_BOUNDS_US: [u64; 15] = [
    50, 100, 250, 500, 1_000, 2_500, 5_000, 10_000, 25_000, 50_000, 100_000, 250_000, 500_000,
    1_000_000, 2_000_000,
];
/// Rcodes 0..=5, then "other".
pub const RCODE_SLOTS: usize = 7;
pub const TRANSPORT_SLOTS: usize = 5;
/// The `rcode` label per slot of `WorkerCounters::queries`.
pub const RCODE_LABELS: [&str; RCODE_SLOTS] = [
    "NOERROR", "FORMERR", "SERVFAIL", "NXDOMAIN", "NOTIMP", "REFUSED", "other",
];
const TRANSPORTS: [Transport; TRANSPORT_SLOTS] = [
    Transport::Udp,
    Transport::Tcp,
    Transport::Dot,
    Transport::Doh,
    Transport::Doq,
];
const SIGNALS: [(Signal, &str); 3] = [
    (Signal::Logs, "logs"),
    (Signal::Traces, "traces"),
    (Signal::Metrics, "metrics"),
];

type Counter = CachePadded<AtomicU64>;

fn counter() -> Counter {
    CachePadded::new(AtomicU64::new(0))
}

pub struct WorkerCounters {
    pub queries: [[Counter; RCODE_SLOTS]; TRANSPORT_SLOTS],
    /// Non-cumulative; slot 15 is +Inf.
    pub duration_buckets: [Counter; 16],
    pub duration_sum_us: Counter,
    pub cache_hits: Counter,
    pub cache_misses: Counter,
    pub stale_served: Counter,
    pub filter_blocked: Counter,
    pub mismatched_replies: Arc<Counter>,
}

impl WorkerCounters {
    fn new() -> WorkerCounters {
        WorkerCounters {
            queries: array::from_fn(|_| array::from_fn(|_| counter())),
            duration_buckets: array::from_fn(|_| counter()),
            duration_sum_us: counter(),
            cache_hits: counter(),
            cache_misses: counter(),
            stale_served: counter(),
            filter_blocked: counter(),
            mismatched_replies: Arc::new(counter()),
        }
    }

    pub fn observe(&self, t: Transport, rcode: u8, duration_us: u64) {
        let slot = usize::from(rcode).min(RCODE_SLOTS - 1);
        self.queries[t.slot()][slot].fetch_add(1, Ordering::Relaxed);
        let bucket = DURATION_BOUNDS_US
            .iter()
            .position(|&b| duration_us <= b)
            .unwrap_or(DURATION_BOUNDS_US.len());
        self.duration_buckets[bucket].fetch_add(1, Ordering::Relaxed);
        self.duration_sum_us
            .fetch_add(duration_us, Ordering::Relaxed);
    }
}

#[derive(Clone, Copy)]
pub enum HandshakeResult {
    Ok = 0,
    Failed = 1,
    NoCertificate = 2,
}

const ENC_TRANSPORTS: [&str; 3] = ["dot", "doh", "doq"];
const HANDSHAKE_RESULTS: [&str; 3] = ["ok", "failed", "no_certificate"];
const DOH_METHODS: [&str; 3] = ["GET", "POST", "other"];
const DOH_STATUSES: [&str; 6] = ["200", "400", "404", "405", "413", "415"];
const PROXY_REASONS: [&str; 3] = ["untrusted_peer", "invalid_header", "timeout"];

/// Process-wide counters of the encrypted client transports; they sit off the
/// cache-hit path, so shared atomics are acceptable.
pub struct EncryptedMetrics {
    /// `[dot, doh, doq]` x `[ok, failed, no_certificate]`.
    handshakes: [[AtomicU64; 3]; 3],
    /// Open connections per `[dot, doh, doq]`.
    connections: [AtomicI64; 3],
    /// `[GET, POST, other]` x `[200, 400, 404, 405, 413, 415]`.
    doh_requests: [[AtomicU64; 6]; 3],
    doq_protocol_errors: AtomicU64,
    /// `[dot, doh]` x `[untrusted_peer, invalid_header, timeout]`.
    proxy_rejected: [[AtomicU64; 3]; 2],
    /// Unix seconds; 0 while no certificate was ever installed.
    tls_not_after: AtomicI64,
    /// Index 0 `applied`, 1 `rejected`.
    tls_updates: [AtomicU64; 2],
    rewritten: AtomicU64,
}

pub static ENCRYPTED: EncryptedMetrics = EncryptedMetrics {
    handshakes: [const { [const { AtomicU64::new(0) }; 3] }; 3],
    connections: [const { AtomicI64::new(0) }; 3],
    doh_requests: [const { [const { AtomicU64::new(0) }; 6] }; 3],
    doq_protocol_errors: AtomicU64::new(0),
    proxy_rejected: [const { [const { AtomicU64::new(0) }; 3] }; 2],
    tls_not_after: AtomicI64::new(0),
    tls_updates: [const { AtomicU64::new(0) }; 2],
    rewritten: AtomicU64::new(0),
};

/// Index into the `[dot, doh, doq]` arrays; plain transports never reach it.
fn tidx(t: Transport) -> usize {
    match t {
        Transport::Dot => 0,
        Transport::Doh => 1,
        _ => 2,
    }
}

/// Counts one open encrypted connection for as long as it lives.
pub struct ConnectionGuard(Transport);

impl ConnectionGuard {
    pub fn new(t: Transport) -> Self {
        ENCRYPTED.connections[tidx(t)].fetch_add(1, Ordering::Relaxed);
        Self(t)
    }
}

impl Drop for ConnectionGuard {
    fn drop(&mut self) {
        ENCRYPTED.connections[tidx(self.0)].fetch_sub(1, Ordering::Relaxed);
    }
}

impl EncryptedMetrics {
    pub fn handshake(&self, t: Transport, r: HandshakeResult) {
        self.handshakes[tidx(t)][r as usize].fetch_add(1, Ordering::Relaxed);
    }

    /// Counts a DoH response; statuses outside the known set count as `415`,
    /// the only other status the handler produces.
    pub fn doh_request(&self, m: &http::Method, s: http::StatusCode) {
        let mi = if m == http::Method::GET {
            0
        } else if m == http::Method::POST {
            1
        } else {
            2
        };
        let si = match s.as_u16() {
            200 => 0,
            400 => 1,
            404 => 2,
            405 => 3,
            413 => 4,
            _ => 5,
        };
        self.doh_requests[mi][si].fetch_add(1, Ordering::Relaxed);
    }

    pub fn doq_protocol_error(&self) {
        self.doq_protocol_errors.fetch_add(1, Ordering::Relaxed);
    }

    pub fn proxy_rejected(&self, t: Transport, r: &crate::server::proxy::ProxyReject) {
        use crate::server::proxy::ProxyReject;
        let ri = match r {
            ProxyReject::UntrustedPeer => 0,
            ProxyReject::InvalidHeader => 1,
            ProxyReject::Timeout => 2,
        };
        self.proxy_rejected[usize::from(tidx(t) != 0)][ri].fetch_add(1, Ordering::Relaxed);
    }

    pub fn set_tls_not_after(&self, v: i64) {
        self.tls_not_after.store(v, Ordering::Relaxed);
    }

    /// Counts one `TlsMaterial` push by outcome.
    pub fn tls_update(&self, applied: bool) {
        self.tls_updates[usize::from(!applied)].fetch_add(1, Ordering::Relaxed);
    }

    pub fn rewritten(&self) {
        self.rewritten.fetch_add(1, Ordering::Relaxed);
    }

    fn register(&self, reg: &mut Registry) {
        let load = |c: &AtomicU64| c.load(Ordering::Relaxed);
        let not_after = self.tls_not_after.load(Ordering::Relaxed);
        if not_after != 0 {
            reg.register(
                "nexora_tls_certificate_not_after_seconds",
                "Expiry of the installed DNS serving certificate",
                ConstGauge::new(not_after),
            );
        }
        let updates = Family::<Labels, PromCounter>::default();
        for (i, result) in ["applied", "rejected"].into_iter().enumerate() {
            updates
                .get_or_create(&vec![("result", result.to_owned())])
                .inc_by(load(&self.tls_updates[i]));
        }
        reg.register(
            "nexora_tls_material_updates",
            "DNS serving certificates received over the control stream",
            updates,
        );

        let handshakes = Family::<Labels, PromCounter>::default();
        let connections = Family::<Labels, Gauge>::default();
        for (ti, transport) in ENC_TRANSPORTS.iter().enumerate() {
            for (ri, result) in HANDSHAKE_RESULTS.iter().enumerate() {
                handshakes
                    .get_or_create(&vec![
                        ("transport", (*transport).to_owned()),
                        ("result", (*result).to_owned()),
                    ])
                    .inc_by(load(&self.handshakes[ti][ri]));
            }
            connections
                .get_or_create(&vec![("transport", (*transport).to_owned())])
                .set(self.connections[ti].load(Ordering::Relaxed));
        }
        reg.register(
            "nexora_tls_handshakes",
            "TLS and QUIC handshakes on the encrypted client listeners",
            handshakes,
        );
        reg.register(
            "nexora_encrypted_connections",
            "Open encrypted client connections",
            connections,
        );

        let doh = Family::<Labels, PromCounter>::default();
        for (mi, method) in DOH_METHODS.iter().enumerate() {
            for (si, status) in DOH_STATUSES.iter().enumerate() {
                doh.get_or_create(&vec![
                    ("method", (*method).to_owned()),
                    ("status", (*status).to_owned()),
                ])
                .inc_by(load(&self.doh_requests[mi][si]));
            }
        }
        reg.register(
            "nexora_doh_requests",
            "DoH requests by method and status",
            doh,
        );

        reg.register(
            "nexora_doq_protocol_errors",
            "DoQ connections closed with DOQ_PROTOCOL_ERROR",
            ConstCounter::new(load(&self.doq_protocol_errors)),
        );

        let proxy = Family::<Labels, PromCounter>::default();
        for (ti, transport) in ENC_TRANSPORTS[..2].iter().enumerate() {
            for (ri, reason) in PROXY_REASONS.iter().enumerate() {
                proxy
                    .get_or_create(&vec![
                        ("transport", (*transport).to_owned()),
                        ("reason", (*reason).to_owned()),
                    ])
                    .inc_by(load(&self.proxy_rejected[ti][ri]));
            }
        }
        reg.register(
            "nexora_proxy_protocol_rejected",
            "Connections refused while reading the PROXY protocol header",
            proxy,
        );

        reg.register(
            "nexora_filter_rewritten",
            "Queries answered from a DNS rewrite",
            ConstCounter::new(load(&self.rewritten)),
        );
    }
}

#[derive(Clone, Copy)]
pub enum Signal {
    Logs = 0,
    Traces = 1,
    Metrics = 2,
}

pub struct Metrics {
    pub workers: Box<[WorkerCounters]>,
    pub export_dropped: [Counter; 3],
    pub config_version: AtomicU64,
    pub control_connected: AtomicBool,
}

impl Metrics {
    pub fn new(workers: usize) -> Metrics {
        Metrics {
            workers: (0..workers.max(1)).map(|_| WorkerCounters::new()).collect(),
            export_dropped: array::from_fn(|_| counter()),
            config_version: AtomicU64::new(0),
            control_connected: AtomicBool::new(false),
        }
    }

    fn sum(&self, f: impl Fn(&WorkerCounters) -> u64) -> u64 {
        self.workers.iter().map(f).sum()
    }

    pub fn sum_queries(&self) -> u64 {
        self.sum(|w| {
            w.queries
                .iter()
                .flatten()
                .map(|c| c.load(Ordering::Relaxed))
                .sum()
        })
    }

    pub fn sum_cache_hits(&self) -> u64 {
        self.sum(|w| w.cache_hits.load(Ordering::Relaxed))
    }

    pub fn sum_cache_misses(&self) -> u64 {
        self.sum(|w| w.cache_misses.load(Ordering::Relaxed))
    }

    pub fn dropped(&self, s: Signal) -> u64 {
        self.export_dropped[s as usize].load(Ordering::Relaxed)
    }
}

/// Counters summed across workers at one instant.
pub struct Totals {
    pub queries: [[u64; RCODE_SLOTS]; TRANSPORT_SLOTS],
    /// Non-cumulative; slot 15 is +Inf.
    pub duration_buckets: [u64; 16],
    pub duration_sum_us: u64,
    pub cache_hits: u64,
    pub cache_misses: u64,
    pub stale_served: u64,
    pub filter_blocked: u64,
    pub mismatched_replies: u64,
}

impl Totals {
    pub fn queries_total(&self) -> u64 {
        self.queries.iter().flatten().sum()
    }
}

/// `nexora_query_duration_seconds`, encoded from summed bucket counts.
#[derive(Debug)]
struct DurationHistogram {
    sum_seconds: f64,
    count: u64,
    buckets: Vec<(f64, u64)>,
}

impl EncodeMetric for DurationHistogram {
    fn encode(&self, mut encoder: MetricEncoder) -> std::fmt::Result {
        encoder.encode_histogram::<NoLabelSet>(self.sum_seconds, self.count, &self.buckets, None)
    }

    fn metric_type(&self) -> MetricType {
        MetricType::Histogram
    }
}

type Labels = Vec<(&'static str, String)>;

impl Metrics {
    pub fn totals(&self) -> Totals {
        let load = |c: &Counter| c.load(Ordering::Relaxed);
        Totals {
            queries: array::from_fn(|t| array::from_fn(|r| self.sum(|w| load(&w.queries[t][r])))),
            duration_buckets: array::from_fn(|b| self.sum(|w| load(&w.duration_buckets[b]))),
            duration_sum_us: self.sum(|w| load(&w.duration_sum_us)),
            cache_hits: self.sum(|w| load(&w.cache_hits)),
            cache_misses: self.sum(|w| load(&w.cache_misses)),
            stale_served: self.sum(|w| load(&w.stale_served)),
            filter_blocked: self.sum(|w| load(&w.filter_blocked)),
            mismatched_replies: self.sum(|w| load(&w.mismatched_replies)),
        }
    }

    /// The OpenMetrics text exposition, built from a fresh registry per scrape.
    pub fn render(&self, rt: &Runtime, recursor: &RecursorState) -> String {
        let t = self.totals();
        let mut reg = Registry::default();

        let queries = Family::<Labels, PromCounter>::default();
        for (ti, transport) in TRANSPORTS.iter().enumerate() {
            for (ri, rcode) in RCODE_LABELS.iter().enumerate() {
                queries
                    .get_or_create(&vec![
                        ("transport", transport.as_str().to_owned()),
                        ("rcode", (*rcode).to_owned()),
                    ])
                    .inc_by(t.queries[ti][ri]);
            }
        }
        reg.register("nexora_queries", "DNS replies sent", queries);

        let buckets = DURATION_BOUNDS_US
            .iter()
            .map(|&b| b as f64 / 1e6)
            .chain([f64::MAX])
            .zip(t.duration_buckets)
            .collect();
        reg.register(
            "nexora_query_duration_seconds",
            "Time from query arrival to reply",
            DurationHistogram {
                sum_seconds: t.duration_sum_us as f64 / 1e6,
                count: t.duration_buckets.iter().sum(),
                buckets,
            },
        );

        let counter = |v: u64| ConstCounter::new(v);
        reg.register(
            "nexora_cache_hits",
            "Replies served fresh from the cache",
            counter(t.cache_hits),
        );
        reg.register(
            "nexora_cache_misses",
            "Queries not answered from a fresh cache entry",
            counter(t.cache_misses),
        );
        reg.register(
            "nexora_cache_stale_served",
            "Stale cache entries served after resolution failed",
            counter(t.stale_served),
        );
        reg.register(
            "nexora_cache_entries",
            "Cached responses",
            ConstGauge::new(rt.cache.entries()),
        );
        reg.register(
            "nexora_cache_bytes",
            "Cached response bytes",
            ConstGauge::new(rt.cache.bytes()),
        );
        reg.register(
            "nexora_filter_blocked",
            "Queries blocked by the filter",
            counter(t.filter_blocked),
        );

        let now = clock::now_secs();
        let up = Family::<Labels, Gauge>::default();
        let rtt = Family::<Labels, Gauge<f64, AtomicU64>>::default();
        let upstream_queries = Family::<Labels, PromCounter>::default();
        let upstream_failures = Family::<Labels, PromCounter>::default();
        for (spec, health) in rt.upstreams.specs.iter().zip(&rt.upstreams.health) {
            let labels = vec![("upstream", spec.name.clone())];
            up.get_or_create(&labels).set(i64::from(health.is_up(now)));
            rtt.get_or_create(&labels)
                .set(f64::from(health.ewma_rtt_us.load(Ordering::Relaxed)) / 1e6);
            upstream_queries
                .get_or_create(&labels)
                .inc_by(health.queries.load(Ordering::Relaxed));
            upstream_failures
                .get_or_create(&labels)
                .inc_by(health.failures.load(Ordering::Relaxed));
        }
        reg.register(
            "nexora_upstream_up",
            "1 when the upstream is considered healthy",
            up,
        );
        reg.register(
            "nexora_upstream_rtt_seconds",
            "Smoothed upstream round-trip time",
            rtt,
        );
        reg.register(
            "nexora_upstream_queries",
            "Queries sent to the upstream",
            upstream_queries,
        );
        reg.register(
            "nexora_upstream_failures",
            "Failed upstream exchanges",
            upstream_failures,
        );
        reg.register(
            "nexora_upstream_mismatched_replies",
            "Upstream datagrams that matched no pending query",
            counter(t.mismatched_replies),
        );

        let dropped = Family::<Labels, PromCounter>::default();
        for (signal, name) in SIGNALS {
            dropped
                .get_or_create(&vec![("signal", name.to_owned())])
                .inc_by(self.dropped(signal));
        }
        reg.register(
            "nexora_export_dropped",
            "Telemetry items dropped before export",
            dropped,
        );
        reg.register(
            "nexora_config_version",
            "Applied configuration snapshot version",
            ConstGauge::new(rt.version),
        );
        reg.register(
            "nexora_control_connected",
            "1 while the management control stream is up",
            ConstGauge::new(i64::from(self.control_connected.load(Ordering::Relaxed))),
        );

        ENCRYPTED.register(&mut reg);
        register_recursor(&mut reg, rt, recursor);
        recursor.rpz.manager.register_metrics(&mut reg);

        let mut out = String::with_capacity(4096);
        text::encode(&mut out, &reg).expect("writing to a String cannot fail");
        out
    }

    /// The periodic `Stats` report, from the same sums as `render`.
    pub fn stats(&self, rt: &Runtime, recursor: &RecursorState) -> Stats {
        let t = self.totals();
        let now = clock::now_secs();
        let mut cumulative = 0;
        Stats {
            unix_ms: SystemTime::now()
                .duration_since(SystemTime::UNIX_EPOCH)
                .map_or(0, |d| d.as_millis() as i64),
            queries_total: t.queries_total(),
            cache_hits_total: t.cache_hits,
            cache_misses_total: t.cache_misses,
            cache_stale_served_total: t.stale_served,
            filter_blocked_total: t.filter_blocked,
            servfail_total: t.queries.iter().map(|q| q[2]).sum(),
            duration_bucket_bounds_us: DURATION_BOUNDS_US.to_vec(),
            duration_bucket_counts: t.duration_buckets[..DURATION_BOUNDS_US.len()]
                .iter()
                .map(|c| {
                    cumulative += c;
                    cumulative
                })
                .collect(),
            duration_sum_us: t.duration_sum_us,
            upstreams: rt
                .upstreams
                .specs
                .iter()
                .zip(&rt.upstreams.health)
                .map(|(spec, h)| UpstreamStatus {
                    id: spec.id.clone(),
                    name: spec.name.clone(),
                    up: h.is_up(now),
                    rtt_us: h.ewma_rtt_us.load(Ordering::Relaxed),
                    queries_total: h.queries.load(Ordering::Relaxed),
                    failures_total: h.failures.load(Ordering::Relaxed),
                })
                .collect(),
            export_dropped_total: SIGNALS
                .iter()
                .map(|&(s, name)| (name.to_owned(), self.dropped(s)))
                .collect(),
            cache_entries: rt.cache.entries(),
            cache_bytes: rt.cache.bytes(),
            recursion: Some(recursion_stats(recursor)),
            dnssec: Some(dnssec_stats(rt, recursor)),
            rpz_zones: recursor.rpz.manager.status(),
        }
    }
}

fn recursion_stats(recursor: &RecursorState) -> RecursionStats {
    let m = &recursor.metrics;
    let load = |c: &AtomicU64| c.load(Ordering::Relaxed);
    RecursionStats {
        upstream_queries: load(&m.upstream_queries),
        mismatched_replies: load(&m.mismatched_id)
            + load(&m.mismatched_question)
            + load(&m.mismatched_case)
            + load(&m.malformed_replies),
        tcp_fallbacks: load(&m.tcp_fallbacks),
        work_limit_exceeded: load(&m.limit_queries)
            + load(&m.limit_delegation_depth)
            + load(&m.limit_cname_depth),
        infra_entries: u32::try_from(recursor.recursor.infra.len()).unwrap_or(u32::MAX),
        lame_marked: load(&m.lame_marked),
    }
}

fn active_ntas(rt: &Runtime) -> u32 {
    let now = clock::unix_now();
    rt.resolution
        .dnssec
        .ntas
        .iter()
        .filter(|(_, expires)| *expires > now)
        .count() as u32
}

fn dnssec_stats(rt: &Runtime, recursor: &RecursorState) -> DnssecStats {
    let m = &recursor.metrics;
    let load = |c: &AtomicU64| c.load(Ordering::Relaxed);
    DnssecStats {
        secure: load(&m.dnssec_secure),
        insecure: load(&m.dnssec_insecure),
        bogus: load(&m.dnssec_bogus),
        indeterminate: load(&m.dnssec_indeterminate),
        trust_anchors: recursor.anchors.status(),
        active_negative_trust_anchors: active_ntas(rt),
    }
}

/// Recursion, DNSSEC and trust-anchor families; every label value is created even at zero.
fn register_recursor(reg: &mut Registry, rt: &Runtime, recursor: &RecursorState) {
    let m = &recursor.metrics;
    let load = |c: &AtomicU64| c.load(Ordering::Relaxed);
    let counter = |v: u64| ConstCounter::new(v);
    let family = |pairs: &[(&'static str, &str, u64)]| {
        let f = Family::<Labels, PromCounter>::default();
        for (label, value, n) in pairs {
            f.get_or_create(&vec![(*label, (*value).to_owned())])
                .inc_by(*n);
        }
        f
    };
    reg.register(
        "nexora_recursor_upstream_queries",
        "Queries sent to authoritative and forward-zone servers",
        counter(load(&m.upstream_queries)),
    );
    reg.register(
        "nexora_recursor_upstream_timeouts",
        "Authoritative exchanges that timed out",
        counter(load(&m.upstream_timeouts)),
    );
    reg.register(
        "nexora_recursor_mismatched_replies",
        "Replies dropped for not matching the query",
        family(&[
            ("reason", "id", load(&m.mismatched_id)),
            ("reason", "question", load(&m.mismatched_question)),
            ("reason", "case", load(&m.mismatched_case)),
            ("reason", "malformed", load(&m.malformed_replies)),
        ]),
    );
    reg.register(
        "nexora_recursor_tcp_fallback",
        "Truncated replies retried over TCP",
        counter(load(&m.tcp_fallbacks)),
    );
    reg.register(
        "nexora_recursor_edns_fallback",
        "Servers retried without EDNS",
        counter(load(&m.edns_fallbacks)),
    );
    reg.register(
        "nexora_recursor_lame_servers",
        "Servers marked lame for a zone",
        counter(load(&m.lame_marked)),
    );
    reg.register(
        "nexora_recursor_work_limit_exceeded",
        "Resolutions stopped by a work limit",
        family(&[
            ("limit", "upstream_queries", load(&m.limit_queries)),
            ("limit", "delegation_depth", load(&m.limit_delegation_depth)),
            ("limit", "cname_depth", load(&m.limit_cname_depth)),
        ]),
    );
    reg.register(
        "nexora_recursor_cname_loops",
        "Resolutions stopped by a CNAME/DNAME loop",
        counter(load(&m.cname_loops)),
    );
    reg.register(
        "nexora_resolutions",
        "Misses resolved without the global upstreams",
        family(&[
            ("route", "recursive", load(&m.resolutions_recursive)),
            ("route", "forward_zone", load(&m.resolutions_forward_zone)),
        ]),
    );
    reg.register(
        "nexora_resolution_failures",
        "Misses that got no usable answer",
        counter(load(&m.resolution_failures)),
    );
    reg.register(
        "nexora_recursor_infra_entries",
        "Servers in the infrastructure cache",
        ConstGauge::new(recursor.recursor.infra.len() as i64),
    );
    reg.register(
        "nexora_dnssec_validations",
        "DNSSEC validation results",
        family(&[
            ("result", "secure", load(&m.dnssec_secure)),
            ("result", "insecure", load(&m.dnssec_insecure)),
            ("result", "bogus", load(&m.dnssec_bogus)),
            ("result", "indeterminate", load(&m.dnssec_indeterminate)),
        ]),
    );
    let bogus = Family::<Labels, PromCounter>::default();
    for (code, c) in m.dnssec_bogus_by_ede.iter().enumerate() {
        let n = load(c);
        if n > 0 {
            bogus
                .get_or_create(&vec![("ede", code.to_string())])
                .inc_by(n);
        }
    }
    reg.register(
        "nexora_dnssec_bogus",
        "Bogus answers by EDE INFO-CODE",
        bogus,
    );
    reg.register(
        "nexora_dnssec_aggressive_synthesized",
        "Negative answers synthesised from validated NSEC/NSEC3 (RFC 8198)",
        counter(load(&m.dnssec_aggressive_synthesized)),
    );
    reg.register(
        "nexora_dnssec_trust_anchor_refresh_failures",
        "Failed RFC 5011 trust-anchor refreshes",
        counter(load(&m.trust_anchor_refresh_failures)),
    );

    let keys = Family::<Labels, Gauge>::default();
    let last = Family::<Labels, Gauge>::default();
    for a in recursor.anchors.status() {
        let state = match TrustAnchorState::try_from(a.state) {
            Ok(TrustAnchorState::Configured) => "configured",
            Ok(TrustAnchorState::AddPend) => "add_pend",
            Ok(TrustAnchorState::Valid) => "valid",
            Ok(TrustAnchorState::Missing) => "missing",
            Ok(TrustAnchorState::Revoked) => "revoked",
            _ => "unspecified",
        };
        keys.get_or_create(&vec![("zone", a.zone.clone()), ("state", state.to_owned())])
            .inc();
        last.get_or_create(&vec![("zone", a.zone)])
            .set(a.last_refresh_success_unix);
    }
    reg.register(
        "nexora_dnssec_trust_anchor_keys",
        "Trust-anchor keys by zone and RFC 5011 state",
        keys,
    );
    reg.register(
        "nexora_dnssec_trust_anchor_last_refresh_success_timestamp_seconds",
        "Last successful RFC 5011 refresh of the zone's trust anchors",
        last,
    );
    reg.register(
        "nexora_dnssec_negative_trust_anchors",
        "Negative trust anchors in effect",
        ConstGauge::new(i64::from(active_ntas(rt))),
    );
}

const SCRAPE_HEADER_TIMEOUT: Duration = Duration::from_secs(10);

/// Serves `GET /metrics` over HTTP/1.1 on `addr`; every other request is 404.
pub async fn serve_metrics(addr: SocketAddr, shared: Arc<Shared>) -> std::io::Result<()> {
    serve_metrics_on(tokio::net::TcpListener::bind(addr).await?, shared).await
}

/// [`serve_metrics`] on an already bound listener.
pub async fn serve_metrics_on(
    listener: tokio::net::TcpListener,
    shared: Arc<Shared>,
) -> std::io::Result<()> {
    loop {
        let stream = match listener.accept().await {
            Ok((stream, _)) => stream,
            Err(_) => {
                // Out of descriptors and the like: back off instead of spinning.
                tokio::time::sleep(Duration::from_millis(100)).await;
                continue;
            }
        };
        let shared = shared.clone();
        tokio::spawn(async move {
            let service = hyper::service::service_fn(move |req: Request<hyper::body::Incoming>| {
                let shared = shared.clone();
                async move { Ok::<_, Infallible>(scrape(&req, &shared)) }
            });
            let _ = hyper::server::conn::http1::Builder::new()
                .timer(TokioTimer::new())
                .header_read_timeout(SCRAPE_HEADER_TIMEOUT)
                .serve_connection(TokioIo::new(stream), service)
                .await;
        });
    }
}

fn scrape<B>(req: &Request<B>, shared: &Shared) -> Response<Full<Bytes>> {
    let mut resp = Response::new(Full::new(Bytes::new()));
    if req.method() == Method::GET && req.uri().path() == "/metrics" {
        let body = shared
            .metrics
            .render(&shared.runtime.load(), &shared.recursor);
        *resp.body_mut() = Full::new(Bytes::from(body));
        resp.headers_mut().insert(
            hyper::header::CONTENT_TYPE,
            hyper::header::HeaderValue::from_static(
                "application/openmetrics-text; version=1.0.0; charset=utf-8",
            ),
        );
    } else {
        *resp.status_mut() = StatusCode::NOT_FOUND;
    }
    resp
}

#[cfg(test)]
mod tests {
    use super::*;

    fn has_positive(text: &str, prefix: &str) -> bool {
        text.lines().any(|l| {
            l.strip_prefix(prefix)
                .and_then(|v| v.trim().parse::<f64>().ok())
                .is_some_and(|v| v >= 1.0)
        })
    }

    #[test]
    fn encrypted_metrics_render() {
        ENCRYPTED.handshake(Transport::Doh, HandshakeResult::NoCertificate);
        ENCRYPTED.doh_request(
            &http::Method::POST,
            http::StatusCode::UNSUPPORTED_MEDIA_TYPE,
        );
        let text = Metrics::new(1).render(&Runtime::initial(), &RecursorState::new(None));
        assert!(
            has_positive(
                &text,
                "nexora_tls_handshakes_total{transport=\"doh\",result=\"no_certificate\"} "
            ),
            "{text}"
        );
        assert!(
            has_positive(
                &text,
                "nexora_doh_requests_total{method=\"POST\",status=\"415\"} "
            ),
            "{text}"
        );
    }
}
