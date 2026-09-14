//! Iterative recursion, DNSSEC validation and RPZ (M3).

pub mod budget;
pub mod dispatch;
pub mod dnssec;
pub mod infra;
pub mod iterate;
pub mod memory;
pub mod metrics;
pub mod roothints;
pub mod rpz;
pub mod rrcache;
pub mod trace;
pub mod transport;

#[cfg(test)]
mod dispatch_tests;
#[cfg(test)]
mod infra_tests;
#[cfg(test)]
mod iterate_tests;
#[cfg(test)]
pub(crate) mod testnet;

use crate::clock;
use crate::runtime::Runtime;
use crate::server::{Shared, WorkerForward};
use crate::upstream::WorkerUpstreams;
use budget::WorkBudget;
use crossbeam_utils::CachePadded;
use dispatch::RoutedFetcher;
use dnssec::anchors::{TrustAnchorStore, refresh_zone};
use dnssec::validator::Validator;
use iterate::Recursor;
use metrics::RecursorMetrics;
use std::cell::Cell;
use std::path::Path;
use std::sync::Arc;
use std::sync::atomic::AtomicU64;
use std::time::Duration;

/// Deadline for resolving one client query in recursive mode.
pub const RESOLUTION_DEADLINE: Duration = Duration::from_millis(4000);
/// Advertised EDNS0 UDP payload size on outgoing queries.
pub const EDNS_BUFFER: u16 = 1232;
/// Maximum CNAME/DNAME chain length followed for one query.
pub const MAX_CNAME_DEPTH: u8 = 16;
/// `RecursionConfig.max_upstream_queries` when unset.
pub const DEFAULT_MAX_UPSTREAM_QUERIES: u32 = 100;
/// `RecursionConfig.max_delegation_depth` when unset.
pub const DEFAULT_MAX_DELEGATION_DEPTH: u32 = 32;
/// How often due RFC 5011 trust-anchor refreshes are looked for.
const ANCHOR_CHECK_INTERVAL: Duration = Duration::from_secs(60);

/// Resolution futures are `!Send` (they run on the per-worker `current_thread` runtimes).
pub type LocalBoxFuture<'a, T> = std::pin::Pin<Box<dyn std::future::Future<Output = T> + 'a>>;

/// Polls `main` to completion while polling the `background` futures alongside it; `refill`
/// runs after every poll of `main` and may add futures. Background futures still pending when
/// `main` completes stay in `background` (drop them to cancel).
pub async fn poll_with<'a, T>(
    background: &mut Vec<LocalBoxFuture<'a, ()>>,
    main: impl std::future::Future<Output = T>,
    mut refill: impl FnMut(&mut Vec<LocalBoxFuture<'a, ()>>),
) -> T {
    let mut main = std::pin::pin!(main);
    std::future::poll_fn(|cx| {
        let out = main.as_mut().poll(cx);
        refill(background);
        if out.is_pending() {
            background.retain_mut(|f| f.as_mut().poll(cx).is_pending());
        }
        out
    })
    .await
}

/// Runs `futures` concurrently on the current task; the outputs keep the input order.
pub async fn join_all<'a, T>(futures: Vec<LocalBoxFuture<'a, T>>) -> Vec<T> {
    let mut slots: Vec<(LocalBoxFuture<'a, T>, Option<T>)> =
        futures.into_iter().map(|f| (f, None)).collect();
    std::future::poll_fn(|cx| {
        let mut done = true;
        for (f, out) in slots.iter_mut().filter(|(_, out)| out.is_none()) {
            match f.as_mut().poll(cx) {
                std::task::Poll::Ready(v) => *out = Some(v),
                std::task::Poll::Pending => done = false,
            }
        }
        if done {
            std::task::Poll::Ready(())
        } else {
            std::task::Poll::Pending
        }
    })
    .await;
    slots.into_iter().filter_map(|(_, out)| out).collect()
}

/// An RFC 8914 Extended DNS Error (DNSSEC, resolution failures and RPZ). Only the INFO-CODE goes
/// on the wire; the text is for tests and logs.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Ede {
    pub code: u16,
    pub text: String,
}

impl Ede {
    pub fn new(code: u16, text: impl Into<String>) -> Self {
        Ede {
            code,
            text: text.into(),
        }
    }
}

/// Recursor state that survives snapshot swaps: infrastructure and RRset caches, validated zone
/// states, trust anchors and RPZ zone data.
pub struct RecursorState {
    pub metrics: Arc<RecursorMetrics>,
    pub recursor: Recursor,
    pub validator: Validator,
    pub anchors: Arc<TrustAnchorStore>,
    pub rpz: rpz::RpzState,
}

impl RecursorState {
    /// `state_dir: None` persists nothing (tests, `Shared::new`).
    pub fn new(state_dir: Option<&Path>) -> Arc<Self> {
        let metrics = Arc::new(RecursorMetrics::default());
        Arc::new(RecursorState {
            recursor: Recursor::new(metrics.clone()),
            validator: Validator::new(metrics.clone()),
            anchors: TrustAnchorStore::open(state_dir.map(|d| d.join("trust-anchors.json"))),
            rpz: rpz::RpzState::new(state_dir),
            metrics,
        })
    }

    /// Takes the process-wide parts of an applied runtime: the cache memory budget, trust anchors
    /// (a persist failure is reported through the anchors' `last_error`, never rejecting the
    /// snapshot) and RPZ zones.
    pub fn sync(&self, rt: &Runtime) {
        let shares = memory::shares(rt.resolution.params.cache_max_bytes);
        self.recursor.rrcache.set_capacity(shares.rrset);
        self.recursor.infra.set_capacity(shares.infra);
        self.validator.nsec.set_capacity(shares.nsec);
        let before = self.anchors.trust_points();
        let d = &rt.resolution.dnssec;
        let _ = self
            .anchors
            .merge_config(&d.anchors, d.rfc5011, clock::unix_now());
        if *self.anchors.trust_points() != *before {
            self.validator.clear();
        }
        self.rpz.manager.apply_config(&self.rpz, &rt.resolution.rpz);
    }
}

/// Starts the `nexora-recursor` thread: RFC 5011 trust-anchor refreshes and RPZ transfers on a
/// `current_thread` runtime (resolution futures are `!Send`, like the workers').
pub fn spawn_background(shared: Arc<Shared>) -> std::io::Result<std::thread::JoinHandle<()>> {
    std::thread::Builder::new()
        .name("nexora-recursor".into())
        .spawn(move || {
            let runtime = tokio::runtime::Builder::new_current_thread()
                .enable_all()
                .build()
                .expect("recursor runtime");
            let local = tokio::task::LocalSet::new();
            local.spawn_local(refresh_loop(shared.clone()));
            local.block_on(&runtime, async move {
                let state = &shared.recursor;
                state.rpz.manager.run(&state.rpz, &shared.runtime).await;
            });
        })
}

/// Refreshes due trust anchors every minute while some route validates, fetching DNSKEY RRsets
/// on the route the zone resolves through.
async fn refresh_loop(shared: Arc<Shared>) {
    let upstreams = WorkerUpstreams::new(Arc::new(CachePadded::new(AtomicU64::new(0))));
    let mut every = tokio::time::interval(ANCHOR_CHECK_INTERVAL);
    loop {
        every.tick().await;
        let rt = shared.runtime.load_full();
        let state = &shared.recursor;
        if !rt.resolution.validates_any_route() {
            continue;
        }
        let now = clock::unix_now();
        let due = state.anchors.due(now);
        if due.is_empty() {
            continue;
        }
        let before = state.anchors.trust_points();
        let forward = WorkerForward {
            set: &rt.upstreams,
            worker: &upstreams,
            upstream_index: Cell::new(u8::MAX),
        };
        let p = &rt.resolution.params;
        let budget = WorkBudget::new(p.max_upstream_queries, p.max_delegation_depth);
        let fetcher = RoutedFetcher {
            rt: &rt.resolution,
            state,
            upstream: &forward,
            budget: &budget,
        };
        for zone in due {
            refresh_zone(&state.anchors, &zone, &fetcher, &state.metrics, now).await;
        }
        if *state.anchors.trust_points() != *before {
            state.validator.clear();
        }
    }
}
