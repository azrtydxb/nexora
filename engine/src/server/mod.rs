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

use crate::bootstrap::Bootstrap;
use crate::cache::{self, CacheKey, CachedResponse, Lookup, ServeMode};
use crate::clock;
use crate::edns::{self, CookieSecret, ReplyOpt, Transport};
use crate::filter::{EffectivePolicy, RewriteAnswer, Verdict};
use crate::inflight::{self, InFlight, Join, Resolution};
use crate::runtime::Runtime;
use crate::telemetry::metrics::{Metrics, WorkerCounters};
use crate::telemetry::querylog::{
    self, CacheOutcome, FilterOutcome, NO_POLICY_GROUP, QueryRecord, RING_CAPACITY,
};
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
}

/// The M1 pipeline (`handle_packet`, then `resolve_miss` on a miss) behind [`Answerer`].
pub struct WorkerAnswerer(pub Rc<WorkerCtx>);

impl Answerer for WorkerAnswerer {
    async fn answer(&self, client: ClientInfo, query: &[u8], out: &mut Vec<u8>) {
        let rt = self.0.shared.runtime.load_full();
        out.clear();
        out.resize(65535, 0);
        match handle_packet(&self.0, &rt, query, client.addr, client.transport, out) {
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
            policy_group: NO_POLICY_GROUP,
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

    let (policy, group) = rt.policy.select(client.ip());
    rec.policy_group = group.unwrap_or(NO_POLICY_GROUP);
    let verdict = policy.check(q.key.as_wire());
    rec.filter_us = micros(scope.started.elapsed());
    match verdict {
        Verdict::Pass => {}
        Verdict::Allowed => rec.filter = FilterOutcome::Allowed,
        Verdict::Blocked => {
            ctx.counters()
                .filter_blocked
                .fetch_add(1, Ordering::Relaxed);
            rec.filter = FilterOutcome::Blocked;
            let n = policy
                .filter()
                .write_block_reply(&q, &mut out[..limit], opt.as_ref());
            return reply(&scope, rec, out, n);
        }
        // Rewrite replies never enter the cache.
        Verdict::Rewrite(answer @ RewriteAnswer::Addrs { .. }) => {
            rec.filter = FilterOutcome::Rewritten;
            let bytes = rewrite::answer_inline(policy, packet, answer).unwrap_or_default();
            let bytes = rewrite::fit_limit(bytes, limit);
            out[..bytes.len()].copy_from_slice(&bytes);
            return reply(&scope, rec, out, bytes.len());
        }
        Verdict::Rewrite(RewriteAnswer::Cname { .. }) => {
            return FastOutcome::Rewrite(rewrite::RewriteJob {
                query: packet.into(),
                client,
                transport,
                limit,
                started: scope.started,
            });
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
    let (policy, group) = rt.policy.select(job.client.ip());
    let mut rec = scope.record(q.key, q.qtype);
    rec.policy_group = group.unwrap_or(NO_POLICY_GROUP);
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
                    let answer =
                        leader_answer(&ctx, &rt, policy, &q, job.key, fwd.response, &mut rec);
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

/// Checks a forwarded response for CNAME cloaking against the client's
/// policy and caches it in that policy's partition (`key`); `None` when the
/// response does not walk.
fn leader_answer(
    ctx: &WorkerCtx,
    rt: &Runtime,
    policy: &EffectivePolicy,
    q: &QueryView<'_>,
    key: CacheKey,
    response: Bytes,
    rec: &mut QueryRecord,
) -> Option<Bytes> {
    let info = wire::walk_response(&response, q).ok()?;
    if policy.filter().cloaked(&info.cname_targets) {
        ctx.counters()
            .filter_blocked
            .fetch_add(1, Ordering::Relaxed);
        rec.filter = FilterOutcome::Blocked;
        let mut buf = vec![0u8; HEADER_LEN + q.qname.len() + 4 + 16 + 12];
        let n = policy.filter().write_block_reply(q, &mut buf, None);
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
                    for listener in sockets.tcp {
                        local.spawn_local(tcp::run_tcp(ctx.clone(), listener));
                    }
                    let dot_proxy = dot_proxy.map(Rc::new);
                    for listener in sockets.dot {
                        let (acceptor, certs, proxy) =
                            (dot_acceptor.clone(), cert_store.clone(), dot_proxy.clone());
                        let answerer = Rc::new(WorkerAnswerer(ctx.clone()));
                        local.spawn_local(async move {
                            if let Some(listener) = from_std_listener(listener, "dot") {
                                dot::run_dot(listener, acceptor, certs, answerer, proxy).await;
                            }
                        });
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
                        local.spawn_local(async move {
                            if let Some(listener) = from_std_listener(listener, "doh") {
                                doh::run_doh(listener, acceptor, certs, answerer, proxy, path)
                                    .await;
                            }
                        });
                    }
                    for socket in sockets.doq {
                        let (config, certs) = (quic_config.clone(), cert_store.clone());
                        let answerer = Rc::new(WorkerAnswerer(ctx.clone()));
                        local.spawn_local(async move {
                            let addr = socket.local_addr();
                            match quinn::Endpoint::new(
                                doq::endpoint_config(&reset_key),
                                Some(config),
                                socket,
                                Arc::new(quinn::TokioRuntime),
                            ) {
                                Ok(endpoint) => doq::run_doq(endpoint, answerer, certs).await,
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
