//! The query pipeline: an allocation-free fast path (ACL, filter, cache) run
//! inline by the listeners, miss resolution in `spawn_local` tasks, and the
//! per-core worker threads.

pub mod tcp;
pub mod udp;

use crate::bootstrap::Bootstrap;
use crate::cache::{self, CacheKey, CachedResponse, Lookup, ServeMode};
use crate::clock;
use crate::edns::{self, CookieSecret, ReplyOpt, Transport};
use crate::filter::FilterDecision;
use crate::inflight::{self, InFlight, Join, Resolution};
use crate::runtime::Runtime;
use crate::telemetry::metrics::{Metrics, WorkerCounters};
use crate::telemetry::querylog::{self, CacheOutcome, FilterOutcome, QueryRecord, RING_CAPACITY};
use crate::upstream::{self, Question, WorkerUpstreams};
use crate::wire::{self, NameKey, ParseError, QueryView};
use arc_swap::{ArcSwap, ArcSwapOption};
use bytes::Bytes;
use crossbeam_queue::ArrayQueue;
use rand::RngExt;
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
}

impl Shared {
    pub fn new(workers: usize) -> Arc<Shared> {
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
        })
    }
}

pub struct WorkerCtx {
    pub index: usize,
    pub shared: Arc<Shared>,
    pub upstreams: WorkerUpstreams,
}

impl WorkerCtx {
    pub fn new(index: usize, shared: Arc<Shared>) -> WorkerCtx {
        let mismatched = shared.metrics.workers[index].mismatched_replies.clone();
        WorkerCtx {
            index,
            shared,
            upstreams: WorkerUpstreams::new(mismatched),
        }
    }

    fn counters(&self) -> &WorkerCounters {
        &self.shared.metrics.workers[self.index]
    }
}

#[allow(clippy::large_enum_variant)] // a miss job is built and moved once per miss
pub enum FastOutcome {
    Reply(usize),
    Drop,
    Miss(MissJob),
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
            transport: self.transport,
            filter_us: 0,
            cache_us: 0,
            upstream_start_us: 0,
            upstream_us: 0,
            duration_us: 0,
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
    });
    // debt: EDNS versions other than 0 are answered as version 0 instead of
    // BADVERS; revisit when a client that sends EDNS1 shows up.
    if !rt.acl.allows(client.ip()) {
        let n = wire::write_rcode_reply(&q, wire::RCODE_REFUSED, &mut out[..limit], opt.as_ref());
        return reply(&scope, rec, out, n);
    }
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

    let decision = rt.filter.decide(q.key.as_wire());
    rec.filter_us = micros(scope.started.elapsed());
    match decision {
        FilterDecision::None => {}
        FilterDecision::Allowed => rec.filter = FilterOutcome::Allowed,
        FilterDecision::Blocked => {
            ctx.counters()
                .filter_blocked
                .fetch_add(1, Ordering::Relaxed);
            rec.filter = FilterOutcome::Blocked;
            let n = rt
                .filter
                .write_block_reply(&q, &mut out[..limit], opt.as_ref());
            return reply(&scope, rec, out, n);
        }
    }

    let now = clock::now_secs();
    let key = CacheKey::from_query(&q);
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
    })
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
    let mut rec = scope.record(q.key, q.qtype);
    rec.filter_us = job.filter_us;
    rec.cache_us = job.cache_us;
    rec.cache = CacheOutcome::Miss;
    rec.upstream_start_us = micros(job.started.elapsed());
    let upstream_started = Instant::now();

    let answer = match ctx.shared.inflight.join(job.key) {
        Join::Leader(guard) => {
            match upstream::forward(&rt.upstreams, &ctx.upstreams, &job.query, &job.question).await
            {
                Ok(fwd) => {
                    rec.upstream = fwd.upstream_index.min(usize::from(u8::MAX - 1)) as u8;
                    let answer = leader_answer(&ctx, &rt, &q, job.key, fwd.response, &mut rec);
                    // The cache insert above happens before the in-flight entry is freed.
                    guard.complete(match &answer {
                        Some(bytes) => Resolution::Answer(bytes.clone()),
                        None => Resolution::ServFail,
                    });
                    answer
                }
                Err(_) => {
                    guard.complete(Resolution::ServFail);
                    None
                }
            }
        }
        Join::Follower(rx) => match inflight::wait(rx).await {
            Resolution::Answer(bytes) => Some(bytes),
            Resolution::ServFail => None,
        },
    };
    rec.upstream_us = micros(upstream_started.elapsed());

    let opt = job.opt.as_ref();
    let now = clock::now_secs();
    let lookup = rt.cache.lookup(&job.key, now);
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

/// Checks a forwarded response for CNAME cloaking and caches it; `None` when
/// the response does not walk.
fn leader_answer(
    ctx: &WorkerCtx,
    rt: &Runtime,
    q: &QueryView<'_>,
    key: CacheKey,
    response: Bytes,
    rec: &mut QueryRecord,
) -> Option<Bytes> {
    let info = wire::walk_response(&response, q).ok()?;
    if rt.filter.cloaked(&info.cname_targets) {
        ctx.counters()
            .filter_blocked
            .fetch_add(1, Ordering::Relaxed);
        rec.filter = FilterOutcome::Blocked;
        let mut buf = vec![0u8; HEADER_LEN + q.qname.len() + 4 + 16 + 12];
        let n = rt.filter.write_block_reply(q, &mut buf, None);
        buf.truncate(n);
        return Some(Bytes::from(buf));
    }
    rt.cache.insert(key, &response, q, clock::now_secs());
    Some(response)
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
}

type WorkerSockets = Vec<(Vec<std::net::UdpSocket>, Vec<std::net::TcpListener>)>;

/// How often a kernel-chosen UDP port is re-picked when its TCP twin is taken.
const PORT_ZERO_ATTEMPTS: usize = 16;

/// Binds every listener, then starts one `nexora-worker-{i}` thread per core,
/// each with a `current_thread` runtime running its listeners in a `LocalSet`.
///
/// Port 0 lets the kernel choose: the first worker's socket fixes the port and
/// the other workers share it. A TCP listener on port 0 takes the port chosen
/// for the first UDP listener on the same IP with port 0, since clients retry
/// truncated UDP answers over TCP at the same address.
pub fn spawn_workers(shared: Arc<Shared>, boot: &Bootstrap) -> std::io::Result<Workers> {
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
    let any_zero = boot
        .listen_udp
        .iter()
        .chain(&boot.listen_tcp)
        .any(|a| a.port() == 0);
    let mut attempt = 1;
    let (sockets, udp_addrs, tcp_addrs) = loop {
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
    let handles = sockets
        .into_iter()
        .enumerate()
        .map(|(index, (udp, tcp))| {
            let shared = shared.clone();
            std::thread::Builder::new()
                .name(format!("nexora-worker-{index}"))
                .spawn(move || {
                    let runtime = tokio::runtime::Builder::new_current_thread()
                        .enable_all()
                        .build()
                        .expect("worker runtime");
                    let local = tokio::task::LocalSet::new();
                    let ctx = Rc::new(WorkerCtx::new(index, shared));
                    for sock in udp {
                        local.spawn_local(udp::run_udp(ctx.clone(), sock));
                    }
                    for listener in tcp {
                        local.spawn_local(tcp::run_tcp(ctx.clone(), listener));
                    }
                    local.block_on(&runtime, std::future::pending::<()>());
                })
        })
        .collect::<std::io::Result<Vec<_>>>()?;
    Ok(Workers {
        handles,
        udp: udp_addrs,
        tcp: tcp_addrs,
    })
}

/// Binds `workers` copies of every configured listener, resolving port 0 as
/// described on [`spawn_workers`].
fn bind_all(
    boot: &Bootstrap,
    workers: usize,
) -> std::io::Result<(WorkerSockets, Vec<SocketAddr>, Vec<SocketAddr>)> {
    let mut udp_addrs = boot.listen_udp.clone();
    let mut tcp_addrs = boot.listen_tcp.clone();
    let mut sockets = Vec::with_capacity(workers);
    for w in 0..workers {
        let mut udp = Vec::with_capacity(udp_addrs.len());
        for addr in &mut udp_addrs {
            let sock = udp::bind_udp(*addr)?;
            *addr = sock.local_addr()?;
            udp.push(sock);
        }
        if w == 0 {
            for addr in tcp_addrs.iter_mut().filter(|a| a.port() == 0) {
                let twin = boot
                    .listen_udp
                    .iter()
                    .zip(&udp_addrs)
                    .find(|(conf, _)| conf.port() == 0 && conf.ip() == addr.ip());
                if let Some((_, bound)) = twin {
                    addr.set_port(bound.port());
                }
            }
        }
        let mut tcp = Vec::with_capacity(tcp_addrs.len());
        for addr in &mut tcp_addrs {
            let listener = tcp::bind_tcp(*addr)?;
            *addr = listener.local_addr()?;
            tcp.push(listener);
        }
        sockets.push((udp, tcp));
    }
    Ok((sockets, udp_addrs, tcp_addrs))
}
