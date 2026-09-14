//! Cold-cache recursion latency against the real internet: a standalone recursive resolver with
//! DNSSEC validation and QNAME minimisation (the kw settings) resolves each name through the
//! same miss path the engine serves (`dispatch::resolve_miss`), and every outgoing query is
//! traced.
//!   recursion_latency                      # default name list, 3 runs, fresh caches per run
//!   recursion_latency --runs 5 --isolated  # a fresh cache for every name (root included)
//!   recursion_latency --verbose www.bbc.co.uk
//! Per name: total ms, rcode, AD, answer shape, outgoing queries (resolve/validate), time from
//! the first to the last chain-of-trust query, QNAME-minimised steps, glueless lookups,
//! timeouts, network errors, TCP fallbacks and lost races. The summary is the per-name median
//! over the runs, then the median and p90 of those medians over the latency names.
use bytes::Bytes;
use clap::Parser;
use hickory_proto::op::{Message, ResponseCode};
use hickory_proto::rr::{Name, RecordType};
use hickory_proto::serialize::binary::BinDecodable;
use nexora_engine::recursor::dispatch::{
    ForwardUpstream, MissQuery, Mode, ResolutionRuntime, RpzPending, resolve_miss,
};
use nexora_engine::recursor::dnssec::DnssecRuntime;
use nexora_engine::recursor::trace::{Outcome, Phase, TraceEvent};
use nexora_engine::recursor::{LocalBoxFuture, RecursorState};
use nexora_engine::{clock, proto};
use std::sync::Arc;
use std::time::{Duration, Instant};

const LATENCY_NAMES: [&str; 16] = [
    "www.bbc.co.uk",
    "www.spiegel.de",
    "www.lemonde.fr",
    "www.nos.nl",
    "www.asahi.com",
    "www.abc.net.au",
    "www.elpais.com",
    "www.corriere.it",
    "google.com",
    "github.com",
    "wikipedia.org",
    "apple.com",
    "microsoft.com",
    "netflix.com",
    "www.isc.org",
    "www.nlnetlabs.nl",
];
/// Correctness probes: a bogus zone (SERVFAIL expected) after the latency names.
const CHECK_NAMES: [&str; 1] = ["dnssec-failed.org"];
const ROOT_DS: [&str; 2] = [
    "20326 8 2 E06D44B80B8F1D39A95C0B0D7C65D08458E880409BBC683457104237C7F8EC8D",
    "38696 8 2 683D2D0ACB8C9B712A1948B27F741219298D0A450D612C483AF444A4C0FB2B16",
];

#[derive(Parser)]
struct Args {
    #[arg(long, default_value_t = 3)]
    runs: usize,
    /// A fresh resolver state for every name instead of one per run.
    #[arg(long)]
    isolated: bool,
    /// Print every outgoing query.
    #[arg(long)]
    verbose: bool,
    #[arg(long)]
    no_validation: bool,
    #[arg(long)]
    no_qname_minimisation: bool,
    /// Names to resolve (A records) instead of the default list.
    names: Vec<String>,
}

struct NoUpstream;
impl ForwardUpstream for NoUpstream {
    fn forward<'a>(&'a self, _q: &'a [u8]) -> LocalBoxFuture<'a, Result<Bytes, String>> {
        Box::pin(async { Err("recursive mode only".to_string()) })
    }
}

#[derive(Clone, Default)]
struct Sample {
    ms: f64,
    outcome: String,
    queries: usize,
    validate_queries: usize,
    validate_span_ms: f64,
    minimised: usize,
    glueless: usize,
    timeouts: usize,
    net_errors: usize,
    tcp: usize,
    lost: usize,
}

fn runtime(args: &Args) -> ResolutionRuntime {
    let recursion = proto::RecursionConfig {
        qname_minimisation: !args.no_qname_minimisation,
        ..Default::default()
    };
    let mut rt = ResolutionRuntime {
        mode: Mode::Recursive,
        ..Default::default()
    };
    rt.params =
        Arc::new(nexora_engine::recursor::iterate::RecursionParams::from_config(Some(&recursion)));
    rt.dnssec = Arc::new(DnssecRuntime {
        validation: !args.no_validation,
        anchors: anchors(),
        ..Default::default()
    });
    rt
}

fn anchors() -> Vec<proto::TrustAnchor> {
    ROOT_DS
        .iter()
        .map(|ds| proto::TrustAnchor {
            zone: ".".into(),
            ds: (*ds).into(),
        })
        .collect()
}

fn fresh_state() -> Arc<RecursorState> {
    let state = RecursorState::new(None);
    state.recursor.detect_ipv6();
    let _ = state
        .anchors
        .merge_config(&anchors(), false, clock::unix_now());
    state.recursor.trace.enable();
    state
}

fn describe(wire: &[u8], failed: bool) -> String {
    let Ok(m) = Message::from_bytes(wire) else {
        return "undecodable".into();
    };
    let rc = m.metadata.response_code;
    let ad = if m.metadata.authentic_data { " AD" } else { "" };
    let mut shape: Vec<String> = Vec::new();
    for r in &m.answers {
        let t = r.record_type();
        if t == RecordType::RRSIG {
            continue;
        }
        match shape.last_mut() {
            Some(last) if last.starts_with(&t.to_string()) => {
                let n: usize = last
                    .split('x')
                    .nth(1)
                    .and_then(|n| n.parse().ok())
                    .unwrap_or(1);
                *last = format!("{t}x{}", n + 1);
            }
            _ => shape.push(t.to_string()),
        }
    }
    let rc = if failed && rc == ResponseCode::ServFail {
        "SERVFAIL(unresolved)".to_string()
    } else {
        rc.to_string()
    };
    format!("{rc}{ad} {}", shape.join(","))
}

async fn resolve_one(
    rt: &ResolutionRuntime,
    state: &RecursorState,
    name: &str,
    verbose: bool,
) -> Sample {
    let mut qname = Name::from_ascii(name).expect("valid name").to_lowercase();
    qname.set_fqdn(true);
    let q = MissQuery {
        qname,
        qtype: RecordType::A,
        client_ip: "127.0.0.1".parse().expect("ip"),
        dnssec_ok: true,
        checking_disabled: false,
        authentic_data: false,
        over_tcp: false,
        query: &[],
        rpz: RpzPending::None,
    };
    let _ = state.recursor.trace.take();
    let start = Instant::now();
    let ans = resolve_miss(rt, state, &NoUpstream, &q).await;
    let ms = start.elapsed().as_secs_f64() * 1000.0;
    let events = state.recursor.trace.take();
    if verbose {
        print_events(name, start, &events);
    }
    summarise(ms, describe(&ans.wire, ans.failed), &events)
}

fn summarise(ms: f64, outcome: String, events: &[TraceEvent]) -> Sample {
    let count = |f: &dyn Fn(&TraceEvent) -> bool| events.iter().filter(|e| f(e)).count();
    let validate: Vec<&TraceEvent> = events
        .iter()
        .filter(|e| e.phase == Phase::Validate)
        .collect();
    let validate_span_ms = match (
        validate.iter().map(|e| e.start).min(),
        validate.iter().map(|e| e.start + e.elapsed).max(),
    ) {
        (Some(a), Some(b)) => (b - a).as_secs_f64() * 1000.0,
        _ => 0.0,
    };
    Sample {
        ms,
        outcome,
        queries: events.len(),
        validate_queries: validate.len(),
        validate_span_ms,
        minimised: count(&|e| e.minimised),
        glueless: count(&|e| e.glueless),
        timeouts: count(&|e| e.outcome == Outcome::Timeout),
        net_errors: count(&|e| e.outcome == Outcome::NetworkError),
        tcp: count(&|e| e.outcome == Outcome::Tcp),
        lost: count(&|e| e.outcome == Outcome::Lost),
    }
}

fn print_events(name: &str, t0: Instant, events: &[TraceEvent]) {
    println!("--- {name}");
    for e in events {
        let at = e.start.duration_since(t0);
        println!(
            "  +{:>6.1} {:>6.1}ms {:<8} {:<9?} {:<40} {:<6} {:<39} {}{}",
            ms(at),
            ms(e.elapsed),
            format!("{:?}", e.outcome),
            e.phase,
            e.qname.to_string(),
            e.qtype.to_string(),
            e.server.to_string(),
            if e.minimised { "qmin " } else { "" },
            if e.glueless { "glueless" } else { "" },
        );
    }
}

fn ms(d: Duration) -> f64 {
    d.as_secs_f64() * 1000.0
}

fn median(v: &mut [f64]) -> f64 {
    v.sort_by(f64::total_cmp);
    if v.is_empty() {
        return 0.0;
    }
    v[v.len() / 2]
}

fn percentile(v: &mut [f64], p: f64) -> f64 {
    v.sort_by(f64::total_cmp);
    if v.is_empty() {
        return 0.0;
    }
    let i = ((v.len() as f64 * p).ceil() as usize).clamp(1, v.len()) - 1;
    v[i]
}

fn main() {
    let args = Args::parse();
    clock::start_ticker();
    let names: Vec<String> = if args.names.is_empty() {
        LATENCY_NAMES
            .iter()
            .chain(CHECK_NAMES.iter())
            .map(|s| s.to_string())
            .collect()
    } else {
        args.names.clone()
    };
    let latency_count = if args.names.is_empty() {
        LATENCY_NAMES.len()
    } else {
        names.len()
    };
    let rt = runtime(&args);
    let runtime = tokio::runtime::Builder::new_current_thread()
        .enable_all()
        .build()
        .expect("runtime");
    let local = tokio::task::LocalSet::new();
    let mut samples: Vec<Vec<Sample>> = vec![Vec::new(); names.len()];
    local.block_on(&runtime, async {
        for run in 0..args.runs {
            println!("run {}", run + 1);
            let mut state = fresh_state();
            for (i, name) in names.iter().enumerate() {
                if args.isolated {
                    state = fresh_state();
                }
                let s = resolve_one(&rt, &state, name, args.verbose).await;
                println!(
                    "  {name:<20} {:>7.1} ms  {:<28} q={:<3} val_q={:<3} val_span={:>6.1} qmin={:<2} glueless={:<2} to={} neterr={} tcp={} lost={}",
                    s.ms,
                    s.outcome,
                    s.queries,
                    s.validate_queries,
                    s.validate_span_ms,
                    s.minimised,
                    s.glueless,
                    s.timeouts,
                    s.net_errors,
                    s.tcp,
                    s.lost,
                );
                samples[i].push(s);
            }
        }
    });
    println!("\nmedian over {} runs", args.runs);
    let mut medians = Vec::new();
    for (i, name) in names.iter().enumerate() {
        let col =
            |f: &dyn Fn(&Sample) -> f64| median(&mut samples[i].iter().map(f).collect::<Vec<_>>());
        let m = col(&|s| s.ms);
        if i < latency_count {
            medians.push(m);
        }
        let outcomes: Vec<&str> = samples[i].iter().map(|s| s.outcome.as_str()).collect();
        println!(
            "  {name:<20} {m:>7.1} ms  q={:<4} val_q={:<4} val_span={:>6.1} qmin={:<4} glueless={:<4} to={:<3} {}",
            col(&|s| s.queries as f64),
            col(&|s| s.validate_queries as f64),
            col(&|s| s.validate_span_ms),
            col(&|s| s.minimised as f64),
            col(&|s| s.glueless as f64),
            col(&|s| s.timeouts as f64),
            outcomes.join(" | "),
        );
    }
    let mut all: Vec<f64> = samples[..latency_count]
        .iter()
        .flatten()
        .map(|s| s.ms)
        .collect();
    println!(
        "\noverall: median of medians {:.1} ms, p90 of medians {:.1} ms; all samples median {:.1} ms, p90 {:.1} ms",
        median(&mut medians.clone()),
        percentile(&mut medians, 0.9),
        median(&mut all.clone()),
        percentile(&mut all, 0.9),
    );
}
