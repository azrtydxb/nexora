//! The query pipeline: an allocation-free fast path (ACL, filter, cache) run
//! inline by the listeners, miss resolution in `spawn_local` tasks, and the
//! per-core worker threads.

pub mod doh;
pub mod doq;
pub mod dot;
pub mod proxy;
pub mod rewrite;
pub mod stream;
pub mod tcp;
#[cfg(test)]
pub mod testutil;
pub mod tls;
pub mod udp;

use crate::authoritative::dispatch::{self as auth_dispatch, AuthOutcome};
use crate::authoritative::state::AuthState;
use crate::bootstrap::Bootstrap;
use crate::cache::{self, CacheKey, CachedResponse, Lookup, ServeMode};
use crate::clock;
use crate::edns::{self, CookieSecret, ReplyOpt, Transport};
use crate::filter::decisions::DecisionCache;
use crate::filter::{EffectivePolicy, ListHit, RewriteAnswer, Verdict};
use crate::inflight::{self, InFlight, Join, Resolution};
use crate::recursor::dispatch::{self, ForwardUpstream, MissQuery, RpzPending};
use crate::recursor::rpz::apply::{PolicyOutcome, apply_action};
use crate::recursor::rpz::index::QueryPhase;
use crate::recursor::rpz::parse::RpzAction;
use crate::recursor::{LocalBoxFuture, RecursorState};
use crate::runtime::Runtime;
use crate::telemetry::metrics::{Metrics, WorkerCounters};
use crate::telemetry::querylog::{
    self, ACL_NONE, CacheOutcome, FilterOutcome, FilterSource, NO_FILTER_LIST, NO_POLICY_GROUP,
    NO_RPZ_ZONE, NO_RULE, QueryRecord, RING_CAPACITY,
};
use crate::upstream::{self, Question, UpstreamSet, WorkerUpstreams};
use crate::wire::{self, NameKey, ParseError, QueryView};
use arc_swap::{ArcSwap, ArcSwapOption};
use bytes::Bytes;
use crossbeam_queue::ArrayQueue;
use hickory_proto::rr::{Name, RecordType};
use hickory_proto::serialize::binary::BinDecodable;
use rand::RngExt;
use std::cell::Cell;
use std::net::SocketAddr;
use std::rc::Rc;
use std::sync::Arc;
use std::sync::atomic::Ordering;
use std::time::{Duration, Instant, SystemTime};

/// The UDP payload size advertised in reply OPT records.
const ADVERTISED_UDP_SIZE: u16 = 1232;
const HEADER_LEN: usize = 12;

pub struct Shared {
    pub runtime: ArcSwap<Runtime>,
    pub inflight: InFlight,
    pub metrics: Metrics,
    pub querylog: ArrayQueue<QueryRecord>,
    pub cookie_secret: CookieSecret,
    /// Empty until the engine has enrolled.
    pub engine_id: ArcSwap<String>,
    pub node_name: ArcSwap<String>,
    /// The management-plane channel while the control stream is connected.
    pub mgmt_channel: ArcSwapOption<tonic::transport::Channel>,
    /// Recursion, DNSSEC and RPZ state that survives snapshot swaps.
    pub recursor: Arc<RecursorState>,
    /// Authoritative (M4) state that survives snapshot swaps.
    pub auth: Arc<AuthState>,
    /// Readiness and graceful shutdown.
    pub lifecycle: crate::lifecycle::Lifecycle,
}

impl Shared {
    /// With an in-memory recursor state (nothing persisted).
    pub fn new(workers: usize) -> Arc<Shared> {
        Shared::with_recursor(workers, RecursorState::new(None))
    }

    pub fn with_recursor(workers: usize, recursor: Arc<RecursorState>) -> Arc<Shared> {
        let mut secret = [0u8; 16];
        rand::rng().fill(&mut secret[..]);
        Arc::new(Shared {
            runtime: ArcSwap::from_pointee(Runtime::initial()),
            inflight: InFlight::new(),
            metrics: Metrics::new(workers),
            querylog: ArrayQueue::new(RING_CAPACITY),
            cookie_secret: CookieSecret(secret),
            engine_id: ArcSwap::from_pointee(String::new()),
            node_name: ArcSwap::from_pointee(String::new()),
            mgmt_channel: ArcSwapOption::empty(),
            recursor,
            auth: AuthState::new(),
            lifecycle: Default::default(),
        })
    }
}

/// The global upstreams of one worker as the recursor's forward route.
pub struct WorkerForward<'a> {
    pub set: &'a UpstreamSet,
    pub worker: &'a WorkerUpstreams,
    /// The upstream that answered last; `u8::MAX` while none did.
    pub upstream_index: Cell<u8>,
    /// Upstreams raced for the last answer (`Forwarded::raced`); 1 until one answered.
    pub raced: Cell<u8>,
}

impl ForwardUpstream for WorkerForward<'_> {
    fn forward<'a>(&'a self, query: &'a [u8]) -> LocalBoxFuture<'a, Result<Bytes, String>> {
        Box::pin(async move {
            let q = wire::parse_query(query).map_err(|e| e.to_string())?;
            let question = Question {
                key: q.key,
                qtype: q.qtype,
                qclass: q.qclass,
            };
            let fwd = upstream::forward(self.set, self.worker, query, &question)
                .await
                .map_err(|e| e.to_string())?;
            self.upstream_index
                .set(fwd.upstream_index.min(usize::from(u8::MAX - 1)) as u8);
            self.raced.set(fwd.raced);
            Ok(fwd.response)
        })
    }
}

pub struct WorkerCtx {
    pub index: usize,
    pub shared: Arc<Shared>,
    pub upstreams: WorkerUpstreams,
    /// This worker's filter decisions for repeated names (valid across runtimes: keyed by index
    /// generation and view).
    pub filter_decisions: DecisionCache,
}

impl WorkerCtx {
    pub fn new(index: usize, shared: Arc<Shared>) -> WorkerCtx {
        let mismatched = shared.metrics.workers[index].mismatched_replies.clone();
        WorkerCtx {
            index,
            shared,
            upstreams: WorkerUpstreams::new(mismatched),
            filter_decisions: DecisionCache::default(),
        }
    }

    pub fn counters(&self) -> &WorkerCounters {
        &self.shared.metrics.workers[self.index]
    }
}

/// Where a query came from, as the listener resolved it (PROXY header source included).
#[derive(Clone, Copy, Debug)]
pub struct ClientInfo {
    pub addr: SocketAddr,
    pub transport: Transport,
}

/// The transport-agnostic answer path every stream, HTTP and QUIC listener feeds.
// Futures stay on the per-core `LocalSet`, so no `Send` bound is wanted on them.
#[allow(async_fn_in_trait)]
pub trait Answerer {
    /// Clears `out` and writes the full response; leaves `out` empty when the
    /// message must be dropped.
    async fn answer(&self, client: ClientInfo, query: &[u8], out: &mut Vec<u8>);

    /// Stream transports (TCP, DoT): every message of the reply in order (several for a zone
    /// transfer). Default: the one `answer` message, none when it is dropped.
    async fn answer_frames(&self, client: ClientInfo, query: &[u8], frames: &mut Vec<Vec<u8>>) {
        let mut out = Vec::new();
        self.answer(client, query, &mut out).await;
        if !out.is_empty() {
            frames.push(out);
        }
    }
}

/// The M1 pipeline (`handle_packet`, then `resolve_miss` on a miss) behind [`Answerer`].
pub struct WorkerAnswerer(pub Rc<WorkerCtx>);

impl WorkerAnswerer {
    /// Completes a fast-path outcome into `out` (whose first `n` octets hold a `Reply(n)`); a
    /// slow job contributes its first message.
    async fn complete(&self, rt: Arc<Runtime>, outcome: FastOutcome, out: &mut Vec<u8>) {
        match outcome {
            FastOutcome::Reply(n) => out.truncate(n),
            FastOutcome::Drop => out.clear(),
            FastOutcome::Miss(job) => {
                let reply = resolve_miss(self.0.clone(), rt, job).await;
                out.clear();
                out.extend_from_slice(&reply);
            }
            FastOutcome::Rewrite(job) => {
                let reply = rewrite::run_rewrite_job(self.0.clone(), rt, job).await;
                out.clear();
                out.extend_from_slice(&reply);
            }
            FastOutcome::Slow(job) => {
                let msgs = auth_dispatch::run_slow(self.0.clone(), rt, job).await;
                *out = msgs.into_iter().next().unwrap_or_default();
            }
        }
    }
}

impl Answerer for WorkerAnswerer {
    async fn answer(&self, client: ClientInfo, query: &[u8], out: &mut Vec<u8>) {
        let rt = self.0.shared.runtime.load_full();
        out.clear();
        out.resize(65535, 0);
        let outcome = handle_packet(&self.0, &rt, query, client.addr, client.transport, out);
        self.complete(rt, outcome, out).await;
    }

    async fn answer_frames(&self, client: ClientInfo, query: &[u8], frames: &mut Vec<Vec<u8>>) {
        let rt = self.0.shared.runtime.load_full();
        let mut out = vec![0u8; 65535];
        match handle_packet(&self.0, &rt, query, client.addr, client.transport, &mut out) {
            FastOutcome::Slow(job) => {
                frames.extend(auth_dispatch::run_slow(self.0.clone(), rt, job).await);
            }
            outcome => {
                self.complete(rt, outcome, &mut out).await;
                if !out.is_empty() {
                    frames.push(out);
                }
            }
        }
    }
}

#[allow(clippy::large_enum_variant)] // a miss job is built and moved once per miss
pub enum FastOutcome {
    Reply(usize),
    Drop,
    Miss(MissJob),
    /// A CNAME rewrite, chased off the fast path; never cached.
    Rewrite(rewrite::RewriteJob),
    /// Authoritative work off the fast path (zone transfers): `auth_dispatch::run_slow`.
    Slow(auth_dispatch::SlowJob),
}

pub struct MissJob {
    pub query: Box<[u8]>,
    pub client: SocketAddr,
    pub transport: Transport,
    pub key: CacheKey,
    pub question: Question,
    pub limit: usize,
    pub opt: Option<ReplyOpt>,
    pub started: std::time::Instant,
    pub filter_us: u32,
    pub cache_us: u32,
    /// An RPZ query-phase decision that needs resolution; such misses bypass in-flight
    /// coalescing and the cache.
    pub rpz: RpzPending,
}

/// Per-packet context for counting and logging a reply.
struct Scope<'a> {
    ctx: &'a WorkerCtx,
    rt: &'a Runtime,
    client: SocketAddr,
    transport: Transport,
    started: Instant,
}

impl Scope<'_> {
    fn record(&self, name: NameKey, qtype: u16) -> QueryRecord {
        QueryRecord {
            unix_micros: 0,
            client: self.client.ip(),
            name,
            qtype,
            rcode: 0,
            cache: CacheOutcome::None,
            filter: FilterOutcome::None,
            upstream: u8::MAX,
            config_version: self.rt.version,
            policy_group: NO_POLICY_GROUP,
            transport: self.transport,
            filter_us: 0,
            cache_us: 0,
            upstream_start_us: 0,
            upstream_us: 0,
            duration_us: 0,
            route: 0,
            dnssec: 0,
            rpz_action: 0,
            filter_list: NO_FILTER_LIST,
            filter_generation: 0,
            filter_source: FilterSource::None,
            filter_rule_offset: NO_RULE,
            rewrite_wildcard: false,
            rpz_zone: NO_RPZ_ZONE,
            acl_refused: ACL_NONE,
            upstream_raced: 1,
        }
    }

    /// Counts and logs `reply`; allocation-free.
    fn finish(&self, mut r: QueryRecord, reply: &[u8]) {
        r.rcode = reply.get(3).map_or(wire::RCODE_SERVFAIL, |b| b & 0x0f);
        r.duration_us = micros(self.started.elapsed());
        r.unix_micros = SystemTime::now()
            .duration_since(SystemTime::UNIX_EPOCH)
            .map_or(0, |d| d.as_micros() as u64);
        self.ctx
            .counters()
            .observe(self.transport, r.rcode, u64::from(r.duration_us));
        self.ctx.counters().observe_record(&r);
        querylog::push(&self.ctx.shared.querylog, &self.ctx.shared.metrics, r);
    }
}

fn micros(d: Duration) -> u32 {
    u32::try_from(d.as_micros()).unwrap_or(u32::MAX)
}

/// Answers `packet` from the ACL, filter and cache into `out`, or hands back
/// a `MissJob`. Nothing before the `Miss` branch allocates, logs or locks.
pub fn handle_packet(
    ctx: &WorkerCtx,
    rt: &Runtime,
    packet: &[u8],
    client: SocketAddr,
    transport: Transport,
    out: &mut [u8],
) -> FastOutcome {
    let scope = Scope {
        ctx,
        rt,
        client,
        transport,
        started: Instant::now(),
    };
    let q = match wire::parse_query(packet) {
        Ok(q) => q,
        Err(ParseError::TooShort | ParseError::IsResponse) => return FastOutcome::Drop,
        Err(e) => {
            if !rt.auth.is_empty() {
                match auth_dispatch::unparsed(ctx, rt, packet, client, transport, out) {
                    None | Some(AuthOutcome::NotHosted) => {}
                    Some(AuthOutcome::Reply(n)) => {
                        if n < HEADER_LEN {
                            return FastOutcome::Drop;
                        }
                        scope.finish(scope.record(NameKey::ROOT, 0), &out[..n]);
                        return FastOutcome::Reply(n);
                    }
                    Some(AuthOutcome::Slow(job)) => return FastOutcome::Slow(job),
                }
            }
            let rcode = match e {
                ParseError::NotImp => wire::RCODE_NOTIMP,
                _ => wire::RCODE_FORMERR,
            };
            let Some(n) = wire::write_error_reply(packet, rcode, out) else {
                return FastOutcome::Drop;
            };
            scope.finish(scope.record(NameKey::ROOT, 0), &out[..n]);
            return FastOutcome::Reply(n);
        }
    };
    let mut rec = scope.record(q.key, q.qtype);
    let limit = edns::reply_limit(q.opt.as_ref(), transport).min(out.len());
    let mut opt = q.opt.map(|o| ReplyOpt {
        udp_size: ADVERTISED_UDP_SIZE,
        do_bit: o.do_bit,
        ext_rcode: 0,
        cookie: None,
        ede: None,
    });
    // debt: EDNS versions other than 0 are answered as version 0 instead of
    // BADVERS; revisit when a client that sends EDNS1 shows up.
    if q.opt.is_some_and(|o| o.bad_cookie_len) {
        let n = wire::write_rcode_reply(&q, wire::RCODE_FORMERR, &mut out[..limit], opt.as_ref());
        return reply(&scope, rec, out, n);
    }
    if let (Some(reply_opt), Some(client_cookie)) =
        (opt.as_mut(), q.opt.and_then(|o| o.client_cookie))
    {
        let server = edns::server_cookie(
            &ctx.shared.cookie_secret,
            &client_cookie,
            client.ip(),
            clock::now_secs(),
        );
        reply_opt.cookie = Some((client_cookie, server));
    }
    // Hosted zones answer every client: the ACL restricts recursion and forwarding only.
    if !rt.auth.is_empty() {
        match auth_dispatch::fast(
            ctx,
            rt,
            &q,
            packet,
            client,
            transport,
            &mut out[..limit],
            opt.as_ref(),
        ) {
            AuthOutcome::NotHosted => {}
            AuthOutcome::Reply(n) => {
                rec.cache = CacheOutcome::Auth;
                return reply(&scope, rec, out, n);
            }
            AuthOutcome::Slow(job) => return FastOutcome::Slow(job),
        }
    }
    if !rt.acl.allows(client.ip()) {
        let n = wire::write_rcode_reply(&q, wire::RCODE_REFUSED, &mut out[..limit], opt.as_ref());
        return reply(&scope, rec, out, n);
    }

    let (policy, group) = rt.policy.select(client.ip());
    rec.policy_group = group.unwrap_or(NO_POLICY_GROUP);
    let verdict = policy.check_cached(&ctx.filter_decisions, q.key.as_wire());
    rec.filter_us = micros(scope.started.elapsed());
    match verdict {
        Verdict::Pass => {}
        Verdict::Allowed(hit) => record_hit(ctx, policy, hit, true, &mut rec),
        Verdict::Blocked(hit) => {
            record_hit(ctx, policy, hit, false, &mut rec);
            let n = policy
                .block_reply()
                .write(&q, &mut out[..limit], opt.as_ref());
            return reply(&scope, rec, out, n);
        }
        // Rewrite replies never enter the cache.
        Verdict::Rewrite {
            answer: answer @ RewriteAnswer::Addrs { .. },
            offset,
            wildcard,
        } => {
            rec.filter = FilterOutcome::Rewritten;
            rec.filter_source = FilterSource::Rewrite;
            rec.filter_rule_offset = offset;
            rec.rewrite_wildcard = wildcard;
            let bytes = rewrite::answer_inline(policy, packet, answer).unwrap_or_default();
            let bytes = rewrite::fit_limit(bytes, limit);
            out[..bytes.len()].copy_from_slice(&bytes);
            return reply(&scope, rec, out, bytes.len());
        }
        Verdict::Rewrite {
            answer: RewriteAnswer::Cname { .. },
            ..
        } => {
            return FastOutcome::Rewrite(rewrite::RewriteJob {
                query: packet.into(),
                client,
                transport,
                limit,
                started: scope.started,
            });
        }
    }

    let recursor = &ctx.shared.recursor;
    if recursor.rpz.query_triggers.load(Ordering::Relaxed) {
        let set = recursor.rpz.set.load();
        let pending = match set.check_query(q.key.as_wire(), client.ip()) {
            QueryPhase::NoMatch => RpzPending::None,
            QueryPhase::Hit { zone, action } => match set.effective_action(zone, action) {
                None => {
                    rec.rpz_action = dispatch::RPZ_DISABLED;
                    note_rpz_zone(&mut rec, zone);
                    RpzPending::None
                }
                Some(RpzAction::Passthru) => {
                    rec.rpz_action = RpzAction::Passthru.log_code();
                    note_rpz_zone(&mut rec, zone);
                    RpzPending::None
                }
                Some(action) => RpzPending::Apply { zone, action },
            },
            QueryPhase::Deferred { zone, action } => RpzPending::Deferred {
                zone,
                action: action.clone(),
            },
        };
        match pending {
            RpzPending::None => {}
            RpzPending::Apply { zone, action } if !matches!(&action, RpzAction::LocalData(d) if d.cname.is_some()) =>
            {
                // allocates only on an RPZ hit
                let qname = Name::from_bytes(q.key.as_wire()).unwrap_or_else(|_| Name::root());
                rec.rpz_action = action.log_code();
                note_rpz_zone(&mut rec, zone);
                let over_tcp = transport != Transport::Udp;
                match apply_action(
                    &qname,
                    RecordType::from(q.qtype),
                    over_tcp,
                    &set.zones[zone],
                    &action,
                ) {
                    PolicyOutcome::Respond { wire, ede } => {
                        return rpz_reply(&scope, rec, &q, &wire, Some(ede.code), out, limit, opt);
                    }
                    PolicyOutcome::Truncate { wire } => {
                        return rpz_reply(&scope, rec, &q, &wire, None, out, limit, opt);
                    }
                    PolicyOutcome::Drop => return FastOutcome::Drop,
                    PolicyOutcome::Passthru | PolicyOutcome::ChaseCname { .. } => {}
                }
            }
            pending => {
                return FastOutcome::Miss(MissJob {
                    query: packet.into(),
                    client,
                    transport,
                    key: CacheKey::in_partition(&q, policy.cache_partition()),
                    question: Question {
                        key: q.key,
                        qtype: q.qtype,
                        qclass: q.qclass,
                    },
                    limit,
                    opt,
                    started: scope.started,
                    filter_us: rec.filter_us,
                    cache_us: 0,
                    rpz: pending,
                });
            }
        }
    }

    let now = clock::now_secs();
    let key = CacheKey::in_partition(&q, policy.cache_partition());
    let lookup = rt.cache.lookup(&key, now);
    rec.cache_us = micros(scope.started.elapsed()).saturating_sub(rec.filter_us);
    if let Lookup::Fresh(entry) = lookup {
        ctx.counters().cache_hits.fetch_add(1, Ordering::Relaxed);
        rec.cache = CacheOutcome::Hit;
        let n = cache::write_cached(&entry, &q, now, ServeMode::Fresh, out, limit, opt.as_ref());
        return reply(&scope, rec, out, n);
    }
    // Stale entries are served only when resolution fails (RFC 8767).
    ctx.counters().cache_misses.fetch_add(1, Ordering::Relaxed);
    FastOutcome::Miss(MissJob {
        query: packet.into(),
        client,
        transport,
        key,
        question: Question {
            key: q.key,
            qtype: q.qtype,
            qclass: q.qclass,
        },
        limit,
        opt,
        started: scope.started,
        filter_us: rec.filter_us,
        cache_us: rec.cache_us,
        rpz: RpzPending::None,
    })
}

/// Serves a synthesised RPZ response (never cached) with its EDE.
#[allow(clippy::too_many_arguments)]
fn rpz_reply(
    scope: &Scope<'_>,
    rec: QueryRecord,
    q: &QueryView<'_>,
    wire: &[u8],
    ede: Option<u16>,
    out: &mut [u8],
    limit: usize,
    opt: Option<ReplyOpt>,
) -> FastOutcome {
    let Some(entry) = cache::prepare_uncached(wire, q) else {
        return FastOutcome::Drop;
    };
    let opt = opt.map(|o| ReplyOpt { ede, ..o });
    let n = cache::write_cached(&entry, q, 0, ServeMode::Fresh, out, limit, opt.as_ref());
    reply(scope, rec, out, n)
}

fn reply(scope: &Scope<'_>, rec: QueryRecord, out: &[u8], n: usize) -> FastOutcome {
    if n < HEADER_LEN {
        return FastOutcome::Drop;
    }
    scope.finish(rec, &out[..n]);
    FastOutcome::Reply(n)
}

/// Resolves a miss (leading or following the in-flight resolution for its
/// key) and returns the client's reply; empty only when nothing can be sent.
pub async fn resolve_miss(ctx: Rc<WorkerCtx>, rt: Arc<Runtime>, job: MissJob) -> Vec<u8> {
    let Ok(q) = wire::parse_query(&job.query) else {
        return Vec::new();
    };
    let scope = Scope {
        ctx: &ctx,
        rt: &rt,
        client: job.client,
        transport: job.transport,
        started: job.started,
    };
    let (policy, group) = rt.policy.select(job.client.ip());
    let mut rec = scope.record(q.key, q.qtype);
    rec.policy_group = group.unwrap_or(NO_POLICY_GROUP);
    rec.filter_us = job.filter_us;
    rec.cache_us = job.cache_us;
    rec.cache = CacheOutcome::Miss;
    rec.upstream_start_us = micros(job.started.elapsed());
    let upstream_started = Instant::now();

    let mut ede: Option<u16> = None;
    let mut dropped = false;
    let forward = WorkerForward {
        set: &rt.upstreams,
        worker: &ctx.upstreams,
        upstream_index: Cell::new(u8::MAX),
        raced: Cell::new(1),
    };
    let bypass = !matches!(job.rpz, RpzPending::None);
    let answer = if bypass {
        // The answer depends on this client's RPZ decision: no coalescing, never cached.
        match MissQuery::from_view(
            &q,
            &job.query,
            job.client.ip(),
            job.transport,
            job.rpz.clone(),
        ) {
            None => None,
            Some(mq) => {
                let ans =
                    dispatch::resolve_miss(&rt.resolution, &ctx.shared.recursor, &forward, &mq)
                        .await;
                note_answer(&mut rec, &ans, &forward);
                ede = ans.ede.as_ref().map(|e| e.code);
                dropped = ans.drop;
                if ans.failed || ans.drop {
                    None
                } else if ans.cacheable {
                    leader_answer(&ctx, &rt, policy, &q, job.key, ans.wire, &mut rec, false)
                } else {
                    Some(ans.wire)
                }
            }
        }
    } else {
        match ctx.shared.inflight.join(job.key) {
            Join::Leader(guard) => {
                let rpz_at_start = ctx.shared.recursor.rpz.set.load_full();
                let Some(mq) = MissQuery::from_view(
                    &q,
                    &job.query,
                    job.client.ip(),
                    job.transport,
                    RpzPending::None,
                ) else {
                    guard.complete(Resolution::ServFail);
                    return Vec::new();
                };
                let ans =
                    dispatch::resolve_miss(&rt.resolution, &ctx.shared.recursor, &forward, &mq)
                        .await;
                note_answer(&mut rec, &ans, &forward);
                ede = ans.ede.as_ref().map(|e| e.code);
                dropped = ans.drop;
                let answer = if ans.failed || ans.drop {
                    None
                } else if ans.cacheable {
                    // A snapshot or RPZ publication during resolution may have cleared the cache.
                    let insert = Arc::ptr_eq(&rt, &ctx.shared.runtime.load_full())
                        && Arc::ptr_eq(&rpz_at_start, &ctx.shared.recursor.rpz.set.load_full());
                    leader_answer(&ctx, &rt, policy, &q, job.key, ans.wire, &mut rec, insert)
                } else {
                    Some(ans.wire)
                };
                // The cache insert above happens before the in-flight entry is freed.
                guard.complete(match &answer {
                    Some(bytes) => Resolution::Answer(bytes.clone()),
                    None => Resolution::ServFail,
                });
                answer
            }
            Join::Follower(rx) => match inflight::wait(rx).await {
                Resolution::Answer(bytes) => Some(bytes),
                Resolution::ServFail => None,
            },
        }
    };
    if dropped {
        return Vec::new();
    }
    rec.upstream_us = micros(upstream_started.elapsed());

    let opt = job.opt.map(|o| ReplyOpt { ede, ..o });
    let opt = opt.as_ref();
    let now = clock::now_secs();
    let lookup = if bypass {
        Lookup::Miss
    } else {
        rt.cache.lookup(&job.key, now)
    };
    let reply = match (
        &lookup,
        answer.and_then(|a| cache::prepare_uncached(&a, &q)),
    ) {
        (Lookup::Fresh(entry), _) => serve(entry, &q, now, ServeMode::Fresh, job.limit, opt),
        (_, Some(uncached)) => serve(&uncached, &q, 0, ServeMode::Fresh, job.limit, opt),
        (Lookup::Stale(entry), None) => {
            ctx.counters().stale_served.fetch_add(1, Ordering::Relaxed);
            rec.cache = CacheOutcome::Stale;
            serve(entry, &q, now, ServeMode::Stale, job.limit, opt)
        }
        (Lookup::Miss, None) => {
            let mut buf =
                vec![0u8; HEADER_LEN + q.qname.len() + 4 + opt.map_or(0, ReplyOpt::wire_len)];
            let n = wire::write_rcode_reply(&q, wire::RCODE_SERVFAIL, &mut buf, opt);
            buf.truncate(n);
            buf
        }
    };
    scope.finish(rec, &reply);
    reply
}

/// Records the route, DNSSEC state, RPZ action and upstream of a miss answer.
fn note_answer(rec: &mut QueryRecord, ans: &dispatch::MissAnswer, forward: &WorkerForward<'_>) {
    rec.upstream = forward.upstream_index.get();
    rec.upstream_raced = forward.raced.get();
    rec.route = ans.route as u8;
    rec.dnssec = ans.security as u8;
    if ans.rpz_action != 0 {
        rec.rpz_action = ans.rpz_action;
    }
    if ans.rpz_zone != NO_RPZ_ZONE {
        rec.rpz_zone = ans.rpz_zone;
        rec.filter_source = FilterSource::Rpz;
    }
}

/// Records the RPZ zone (index into the RPZ set) that decided a query.
fn note_rpz_zone(rec: &mut QueryRecord, zone: usize) {
    rec.rpz_zone = u16::try_from(zone).unwrap_or(NO_RPZ_ZONE);
    rec.filter_source = FilterSource::Rpz;
}

/// Checks a resolved response for CNAME cloaking against the client's
/// policy and, with `insert`, caches it in that policy's partition (`key`);
/// `None` when the response does not walk.
#[allow(clippy::too_many_arguments)]
fn leader_answer(
    ctx: &WorkerCtx,
    rt: &Runtime,
    policy: &EffectivePolicy,
    q: &QueryView<'_>,
    key: CacheKey,
    response: Bytes,
    rec: &mut QueryRecord,
    insert: bool,
) -> Option<Bytes> {
    let info = wire::walk_response(&response, q).ok()?;
    if let Some(hit) = policy.filter().cloaked(&info.cname_targets) {
        // The hit's offset points into the CNAME target, not the query name: no rule.
        let hit = ListHit {
            offset: NO_RULE,
            ..hit
        };
        record_hit(ctx, policy, hit, false, rec);
        let mut buf = vec![0u8; HEADER_LEN + q.qname.len() + 4 + 16 + 12];
        let n = policy.block_reply().write(q, &mut buf, None);
        buf.truncate(n);
        return Some(Bytes::from(buf));
    }
    if insert {
        rt.cache.insert(key, &response, q, clock::now_secs());
    }
    Some(response)
}

/// Records a list decision for the query log: the deciding list, its index build and the matched
/// suffix. A blocked query is also counted (in total and per category of the matching lists) and
/// attributed to `category` when its list has one, else `blocklist`. Allocation-free.
fn record_hit(
    ctx: &WorkerCtx,
    policy: &EffectivePolicy,
    hit: ListHit,
    allowed: bool,
    rec: &mut QueryRecord,
) {
    let index = policy.filter().index();
    rec.filter_list = hit.list;
    rec.filter_generation = index.generation();
    rec.filter_rule_offset = hit.offset;
    if allowed {
        rec.filter = FilterOutcome::Allowed;
        rec.filter_source = FilterSource::Allowlist;
        return;
    }
    let c = ctx.counters();
    c.filter_blocked.fetch_add(1, Ordering::Relaxed);
    c.count_categories(policy.filter().categories(hit));
    rec.filter = FilterOutcome::Blocked;
    // Uncategorised lists also set category slot 0 (`custom`), so the list itself decides.
    let categorised = index
        .lists()
        .get(usize::from(hit.list))
        .is_some_and(|l| !l.category.is_empty());
    rec.filter_source = if categorised {
        FilterSource::Category
    } else {
        FilterSource::Blocklist
    };
}

fn serve(
    entry: &CachedResponse,
    q: &QueryView<'_>,
    now: u32,
    mode: ServeMode,
    limit: usize,
    opt: Option<&ReplyOpt>,
) -> Vec<u8> {
    let mut buf = vec![0u8; limit.min(entry.wire.len() + opt.map_or(0, ReplyOpt::wire_len))];
    let n = cache::write_cached(entry, q, now, mode, &mut buf, limit, opt);
    buf.truncate(n);
    buf
}

/// The worker threads and the addresses their listeners bound.
pub struct Workers {
    pub handles: Vec<std::thread::JoinHandle<()>>,
    pub udp: Vec<SocketAddr>,
    pub tcp: Vec<SocketAddr>,
    pub dot: Vec<SocketAddr>,
    pub doh: Vec<SocketAddr>,
    pub doq: Vec<SocketAddr>,
}

/// One worker's bound listeners.
struct WorkerSockets {
    udp: Vec<std::net::UdpSocket>,
    tcp: Vec<std::net::TcpListener>,
    dot: Vec<std::net::TcpListener>,
    doh: Vec<std::net::TcpListener>,
    doq: Vec<std::net::UdpSocket>,
}

/// Every worker's listeners and the addresses they resolved to.
struct Bound {
    sockets: Vec<WorkerSockets>,
    udp: Vec<SocketAddr>,
    tcp: Vec<SocketAddr>,
    dot: Vec<SocketAddr>,
    doh: Vec<SocketAddr>,
    doq: Vec<SocketAddr>,
}

/// How often a kernel-chosen UDP port is re-picked when its TCP twin is taken.
const PORT_ZERO_ATTEMPTS: usize = 16;

/// Binds every listener, then starts one `nexora-worker-{i}` thread per core,
/// each with a `current_thread` runtime running its listeners in a `LocalSet`.
/// DoT, DoH (and DoQ) handshakes resolve their certificate from `cert_store`.
///
/// Port 0 lets the kernel choose: the first worker's socket fixes the port and
/// the other workers share it. A TCP listener on port 0 takes the port chosen
/// for the first UDP listener on the same IP with port 0, since clients retry
/// truncated UDP answers over TCP at the same address.
pub fn spawn_workers(
    shared: Arc<Shared>,
    boot: &Bootstrap,
    cert_store: Arc<tls::CertStore>,
) -> std::io::Result<Workers> {
    let workers = boot.worker_count();
    if workers > shared.metrics.workers.len() {
        return Err(std::io::Error::new(
            std::io::ErrorKind::InvalidInput,
            format!(
                "{workers} workers but counters for {}",
                shared.metrics.workers.len()
            ),
        ));
    }
    let proxy_policy = |enabled: bool| -> std::io::Result<Option<proxy::ProxyPolicy>> {
        if !enabled {
            return Ok(None);
        }
        proxy::ProxyPolicy::new(&boot.proxy_protocol_trusted_cidrs)
            .map(Some)
            .map_err(|e| std::io::Error::new(std::io::ErrorKind::InvalidInput, e))
    };
    let dot_proxy = proxy_policy(boot.proxy_protocol_dot)?;
    let doh_proxy = proxy_policy(boot.proxy_protocol_doh)?;
    let dot_acceptor =
        tokio_rustls::TlsAcceptor::from(tls::stream_server_config(cert_store.clone(), &[b"dot"]));
    let doh_acceptor = tokio_rustls::TlsAcceptor::from(tls::stream_server_config(
        cert_store.clone(),
        &[b"h2", b"http/1.1"],
    ));
    let mut reset_key = [0u8; 64];
    aws_lc_rs::rand::fill(&mut reset_key)
        .map_err(|_| std::io::Error::other("DoQ stateless reset key: no randomness"))?;
    let quic_config = tls::quic_server_config(cert_store.clone());
    let any_zero = boot
        .listen_udp
        .iter()
        .chain(&boot.listen_tcp)
        .any(|a| a.port() == 0);
    let mut attempt = 1;
    let bound = loop {
        match bind_all(boot, workers) {
            Err(e)
                if any_zero
                    && e.kind() == std::io::ErrorKind::AddrInUse
                    && attempt < PORT_ZERO_ATTEMPTS =>
            {
                attempt += 1;
            }
            r => break r?,
        }
    };
    let handles = bound
        .sockets
        .into_iter()
        .enumerate()
        .map(|(index, sockets)| {
            let shared = shared.clone();
            let cert_store = cert_store.clone();
            let (dot_acceptor, doh_acceptor) = (dot_acceptor.clone(), doh_acceptor.clone());
            let (dot_proxy, doh_proxy) = (dot_proxy.clone(), doh_proxy.clone());
            let doh_path = boot.doh_path.clone();
            let quic_config = quic_config.clone();
            std::thread::Builder::new()
                .name(format!("nexora-worker-{index}"))
                .spawn(move || {
                    let runtime = tokio::runtime::Builder::new_current_thread()
                        .enable_all()
                        .build()
                        .expect("worker runtime");
                    let local = tokio::task::LocalSet::new();
                    let ctx = Rc::new(WorkerCtx::new(index, shared));
                    for sock in sockets.udp {
                        local.spawn_local(udp::run_udp(ctx.clone(), sock));
                    }
                    // Stream listeners end (and close) once shutdown stops accepting; their
                    // connections are separate tasks and keep being served until exit.
                    let until_stopped = |accept: std::pin::Pin<Box<dyn Future<Output = ()>>>| {
                        let stopped = ctx.shared.lifecycle.accepting_stopped();
                        async move {
                            tokio::select! {
                                _ = accept => {}
                                _ = stopped => {}
                            }
                        }
                    };
                    for listener in sockets.tcp {
                        local.spawn_local(until_stopped(Box::pin(tcp::run_tcp(
                            ctx.clone(),
                            listener,
                        ))));
                    }
                    let dot_proxy = dot_proxy.map(Rc::new);
                    for listener in sockets.dot {
                        let (acceptor, certs, proxy) =
                            (dot_acceptor.clone(), cert_store.clone(), dot_proxy.clone());
                        let answerer = Rc::new(WorkerAnswerer(ctx.clone()));
                        local.spawn_local(until_stopped(Box::pin(async move {
                            if let Some(listener) = from_std_listener(listener, "dot") {
                                dot::run_dot(listener, acceptor, certs, answerer, proxy).await;
                            }
                        })));
                    }
                    let doh_proxy = doh_proxy.map(Rc::new);
                    let doh_path: Rc<str> = Rc::from(doh_path.as_str());
                    for listener in sockets.doh {
                        let (acceptor, certs, proxy, path) = (
                            doh_acceptor.clone(),
                            cert_store.clone(),
                            doh_proxy.clone(),
                            doh_path.clone(),
                        );
                        let answerer = Rc::new(WorkerAnswerer(ctx.clone()));
                        local.spawn_local(until_stopped(Box::pin(async move {
                            if let Some(listener) = from_std_listener(listener, "doh") {
                                doh::run_doh(listener, acceptor, certs, answerer, proxy, path)
                                    .await;
                            }
                        })));
                    }
                    for socket in sockets.doq {
                        let (config, certs) = (quic_config.clone(), cert_store.clone());
                        let answerer = Rc::new(WorkerAnswerer(ctx.clone()));
                        let stopped = ctx.shared.lifecycle.accepting_stopped();
                        local.spawn_local(async move {
                            let addr = socket.local_addr();
                            match quinn::Endpoint::new(
                                doq::endpoint_config(&reset_key),
                                Some(config),
                                socket,
                                Arc::new(quinn::TokioRuntime),
                            ) {
                                Ok(endpoint) => {
                                    let handle = endpoint.clone();
                                    tokio::select! {
                                        _ = doq::run_doq(endpoint, answerer, certs) => {}
                                        // New connections are refused; open ones keep going.
                                        _ = stopped => handle.set_server_config(None),
                                    }
                                }
                                Err(e) => match addr {
                                    Ok(a) => eprintln!("nexora-engine: doq endpoint {a}: {e}"),
                                    Err(_) => eprintln!("nexora-engine: doq endpoint: {e}"),
                                },
                            }
                        });
                    }
                    local.block_on(&runtime, std::future::pending::<()>());
                })
        })
        .collect::<std::io::Result<Vec<_>>>()?;
    Ok(Workers {
        handles,
        udp: bound.udp,
        tcp: bound.tcp,
        dot: bound.dot,
        doh: bound.doh,
        doq: bound.doq,
    })
}

/// Registers a bound std listener with the current worker runtime.
fn from_std_listener(
    listener: std::net::TcpListener,
    kind: &str,
) -> Option<tokio::net::TcpListener> {
    match tokio::net::TcpListener::from_std(listener) {
        Ok(l) => Some(l),
        Err(e) => {
            eprintln!("nexora-engine: {kind} listener: {e}");
            None
        }
    }
}

/// Binds one TCP listener per address in `addrs`, fixing port 0 to the bound port.
fn bind_tcp_each(
    addrs: &mut [SocketAddr],
    kind: &str,
) -> std::io::Result<Vec<std::net::TcpListener>> {
    addrs
        .iter_mut()
        .map(|addr| {
            let listener = tcp::bind_tcp(*addr)
                .map_err(|e| std::io::Error::new(e.kind(), format!("bind {kind} {addr}: {e}")))?;
            *addr = listener.local_addr()?;
            Ok(listener)
        })
        .collect()
}

/// Binds `workers` copies of every configured listener, resolving port 0 as
/// described on [`spawn_workers`].
fn bind_all(boot: &Bootstrap, workers: usize) -> std::io::Result<Bound> {
    let mut bound = Bound {
        sockets: Vec::with_capacity(workers),
        udp: boot.listen_udp.clone(),
        tcp: boot.listen_tcp.clone(),
        dot: boot.listen_dot.clone(),
        doh: boot.listen_doh.clone(),
        doq: boot.listen_doq.clone(),
    };
    for w in 0..workers {
        let mut udp = Vec::with_capacity(bound.udp.len());
        for addr in &mut bound.udp {
            let sock = udp::bind_udp(*addr)?;
            *addr = sock.local_addr()?;
            udp.push(sock);
        }
        if w == 0 {
            for addr in bound.tcp.iter_mut().filter(|a| a.port() == 0) {
                let twin = boot
                    .listen_udp
                    .iter()
                    .zip(&bound.udp)
                    .find(|(conf, _)| conf.port() == 0 && conf.ip() == addr.ip());
                if let Some((_, b)) = twin {
                    addr.set_port(b.port());
                }
            }
        }
        let mut tcp = Vec::with_capacity(bound.tcp.len());
        for addr in &mut bound.tcp {
            let listener = tcp::bind_tcp(*addr)?;
            *addr = listener.local_addr()?;
            tcp.push(listener);
        }
        let dot = bind_tcp_each(&mut bound.dot, "dot")?;
        let doh = bind_tcp_each(&mut bound.doh, "doh")?;
        let mut doq = Vec::with_capacity(bound.doq.len());
        for addr in &mut bound.doq {
            let sock = doq::bind_doq_socket(*addr)
                .map_err(|e| std::io::Error::new(e.kind(), format!("bind doq {addr}: {e}")))?;
            *addr = sock.local_addr()?;
            doq.push(sock);
        }
        bound.sockets.push(WorkerSockets {
            udp,
            tcp,
            dot,
            doh,
            doq,
        });
    }
    Ok(bound)
}
