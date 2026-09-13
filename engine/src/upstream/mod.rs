//! Forwarding transports, per-upstream health and the forward loop.

pub mod doh;
pub mod dot;
pub mod tcp;
pub mod udp;

use crate::clock;
use crate::wire::NameKey;
use bytes::Bytes;
use crossbeam_utils::CachePadded;
use rand::RngExt;
use rustc_hash::FxHashMap;
use std::cell::{Cell, RefCell};
use std::net::SocketAddr;
use std::rc::Rc;
use std::sync::Arc;
use std::sync::atomic::{AtomicBool, AtomicU32, AtomicU64, Ordering};
use std::time::{Duration, Instant};
use tokio::sync::oneshot;

pub const OVERALL_DEADLINE: Duration = Duration::from_millis(2000);
const DOWN_AFTER_FAILURES: u32 = 3;
const DOWN_SECS: u32 = 5;
const FLAG_TC: u8 = 0x02;

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub struct Question {
    pub key: NameKey,
    pub qtype: u16,
    pub qclass: u16,
}

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Protocol {
    Udp,
    Tcp,
    Dot,
    Doh,
}

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Strategy {
    Ordered,
    Fastest,
}

#[derive(Clone, Debug, PartialEq)]
pub struct UpstreamSpec {
    pub id: String,
    pub name: String,
    pub protocol: Protocol,
    pub addr: Option<SocketAddr>,
    pub tls_server_name: String,
    pub doh_url: String,
    pub timeout: Duration,
    pub ca_pem: String,
}

#[derive(Debug, thiserror::Error)]
pub enum UpstreamError {
    #[error("timeout")]
    Timeout,
    #[error("io: {0}")]
    Io(#[from] std::io::Error),
    #[error("tls: {0}")]
    Tls(String),
    #[error("http status {0}")]
    Http(u16),
    #[error("malformed reply")]
    Malformed,
    #[error("no upstream available")]
    NoneAvailable,
    #[error("deadline exceeded")]
    Deadline,
}

/// Shared across workers and carried across snapshots for an unchanged upstream id.
#[derive(Default, Debug)]
pub struct Health {
    pub consecutive_failures: AtomicU32,
    pub down_until: AtomicU32,
    pub probe_taken: AtomicBool,
    /// Smoothed RTT in microseconds; 0 means not yet measured.
    pub ewma_rtt_us: AtomicU32,
    pub queries: AtomicU64,
    pub failures: AtomicU64,
}

impl Health {
    pub fn is_up(&self, _now: u32) -> bool {
        self.consecutive_failures.load(Ordering::Relaxed) < DOWN_AFTER_FAILURES
    }

    /// True when a query may be sent: the upstream is up, or it is down, due a
    /// probe, and this caller won the single probe slot.
    pub fn admit(&self, now: u32) -> bool {
        self.is_up(now)
            || (now >= self.down_until.load(Ordering::Relaxed)
                && self
                    .probe_taken
                    .compare_exchange(false, true, Ordering::AcqRel, Ordering::Relaxed)
                    .is_ok())
    }

    pub fn record_success(&self, rtt: Duration) {
        self.consecutive_failures.store(0, Ordering::Relaxed);
        self.probe_taken.store(false, Ordering::Relaxed);
        let sample = u32::try_from(rtt.as_micros()).unwrap_or(u32::MAX).max(1);
        let old = self.ewma_rtt_us.load(Ordering::Relaxed);
        let new = if old == 0 {
            sample
        } else {
            ((u64::from(old) * 4 + u64::from(sample)) / 5) as u32
        };
        self.ewma_rtt_us.store(new, Ordering::Relaxed);
    }

    pub fn record_failure(&self, now: u32) {
        self.failures.fetch_add(1, Ordering::Relaxed);
        let failures = self.consecutive_failures.fetch_add(1, Ordering::Relaxed) + 1;
        if failures >= DOWN_AFTER_FAILURES {
            self.down_until.store(now + DOWN_SECS, Ordering::Relaxed);
            self.probe_taken.store(false, Ordering::Relaxed);
        }
    }
}

pub struct UpstreamSet {
    pub specs: Vec<UpstreamSpec>,
    pub health: Vec<Arc<Health>>,
    pub strategy: Strategy,
}

impl UpstreamSet {
    /// Builds a set, reusing `previous`'s health for upstreams with an equal id.
    pub fn new(
        specs: Vec<UpstreamSpec>,
        strategy: Strategy,
        previous: Option<&UpstreamSet>,
    ) -> UpstreamSet {
        let health = specs
            .iter()
            .map(|spec| {
                previous
                    .and_then(|p| {
                        p.specs
                            .iter()
                            .position(|s| s.id == spec.id)
                            .map(|i| p.health[i].clone())
                    })
                    .unwrap_or_default()
            })
            .collect();
        UpstreamSet {
            specs,
            health,
            strategy,
        }
    }

    /// Candidate indices, best first: up or due a probe, by position or by RTT.
    pub fn order(&self, now: u32, out: &mut Vec<usize>) {
        out.clear();
        out.extend(self.health.iter().enumerate().filter_map(|(i, h)| {
            (h.is_up(now) || now >= h.down_until.load(Ordering::Relaxed)).then_some(i)
        }));
        if self.strategy == Strategy::Fastest {
            // Unmeasured (0) sorts after measured; the sort is stable by position.
            out.sort_by_key(|&i| {
                let rtt = self.health[i].ewma_rtt_us.load(Ordering::Relaxed);
                (rtt == 0, rtt)
            });
        }
    }
}

const ID_ROUNDS: usize = 4;

/// Random query IDs that do not repeat within an epoch of 65536: a 16-bit
/// Feistel permutation whose round tables come from the OS-seeded CSPRNG,
/// applied to a counter and re-keyed when the counter wraps. Independent draws
/// would repeat ~12% of IDs across a pool's 16 x 1024 queries (birthday bound).
pub(crate) struct IdSource {
    tables: RefCell<[[u8; 256]; ID_ROUNDS]>,
    counter: Cell<u16>,
}

impl IdSource {
    pub(crate) fn new() -> IdSource {
        IdSource {
            tables: RefCell::new(Self::keys()),
            counter: Cell::new(0),
        }
    }

    fn keys() -> [[u8; 256]; ID_ROUNDS] {
        let mut tables = [[0u8; 256]; ID_ROUNDS];
        let mut rng = rand::rng();
        for t in &mut tables {
            rng.fill(&mut t[..]);
        }
        tables
    }

    pub(crate) fn next(&self) -> u16 {
        let counter = self.counter.get();
        if counter == 0 {
            *self.tables.borrow_mut() = Self::keys();
        }
        self.counter.set(counter.wrapping_add(1));
        let tables = self.tables.borrow();
        let [mut left, mut right] = counter.to_be_bytes();
        for table in tables.iter() {
            (left, right) = (right, left ^ table[usize::from(right)]);
        }
        u16::from_be_bytes([left, right])
    }
}

struct Waiter {
    question: Question,
    client_id: [u8; 2],
    tx: oneshot::Sender<Bytes>,
}

/// Queries in flight on one socket or connection, keyed by their random wire ID.
#[derive(Default)]
pub(crate) struct Waiters {
    map: RefCell<FxHashMap<u16, Waiter>>,
}

impl Waiters {
    /// Registers `query` under the next ID from `ids` not already pending.
    pub(crate) fn register(
        &self,
        query: &[u8],
        question: &Question,
        ids: &IdSource,
    ) -> (u16, oneshot::Receiver<Bytes>) {
        let mut map = self.map.borrow_mut();
        let id = loop {
            let id = ids.next();
            if !map.contains_key(&id) {
                break id;
            }
        };
        let (tx, rx) = oneshot::channel();
        map.insert(
            id,
            Waiter {
                question: *question,
                client_id: [query[0], query[1]],
                tx,
            },
        );
        (id, rx)
    }

    /// Hands `reply` to its waiter when it has QR=1, a pending ID and the
    /// waiter's question; anything else is counted in `mismatched`.
    pub(crate) fn deliver(&self, reply: &[u8], mismatched: &AtomicU64) {
        let mut map = self.map.borrow_mut();
        let id = match reply {
            [a, b, ..] => u16::from_be_bytes([*a, *b]),
            _ => u16::MAX,
        };
        let matched = map
            .get(&id)
            .is_some_and(|w| tcp::reply_matches(reply, id, &w.question));
        if !matched {
            mismatched.fetch_add(1, Ordering::Relaxed);
            return;
        }
        let waiter = map.remove(&id).expect("checked above");
        let mut owned = reply.to_vec();
        owned[..2].copy_from_slice(&waiter.client_id);
        let _ = waiter.tx.send(Bytes::from(owned));
    }

    pub(crate) fn cancel(&self, id: u16) {
        self.map.borrow_mut().remove(&id);
    }

    pub(crate) fn is_empty(&self) -> bool {
        self.map.borrow().is_empty()
    }

    /// Drops every waiter; their receivers see the channel closed.
    pub(crate) fn clear(&self) {
        self.map.borrow_mut().clear();
    }
}

const INLINE_QUERY: usize = 1232;

/// A copy of a query with its ID replaced, inline for EDNS-sized queries.
#[allow(clippy::large_enum_variant)] // inline on purpose: no allocation per send
pub(crate) enum QueryBuf {
    Inline([u8; INLINE_QUERY], usize),
    Heap(Vec<u8>),
}

impl QueryBuf {
    /// `query` must be at least a header long.
    pub(crate) fn new(query: &[u8], id: u16) -> QueryBuf {
        let mut buf = if query.len() <= INLINE_QUERY {
            let mut inline = [0u8; INLINE_QUERY];
            inline[..query.len()].copy_from_slice(query);
            QueryBuf::Inline(inline, query.len())
        } else {
            QueryBuf::Heap(query.to_vec())
        };
        let bytes = match &mut buf {
            QueryBuf::Inline(b, len) => &mut b[..*len],
            QueryBuf::Heap(v) => v.as_mut_slice(),
        };
        bytes[..2].copy_from_slice(&id.to_be_bytes());
        buf
    }

    pub(crate) fn as_slice(&self) -> &[u8] {
        match self {
            QueryBuf::Inline(b, len) => &b[..*len],
            QueryBuf::Heap(v) => v,
        }
    }
}

/// TLS client settings for DoT/DoH: the Mozilla roots when `ca_pem` is empty,
/// otherwise only the certificates in `ca_pem`.
pub fn client_tls_config(ca_pem: &str) -> Result<Arc<rustls::ClientConfig>, UpstreamError> {
    use rustls::pki_types::{CertificateDer, pem::PemObject};
    let mut roots = rustls::RootCertStore::empty();
    if ca_pem.is_empty() {
        roots.extend(webpki_roots::TLS_SERVER_ROOTS.iter().cloned());
    } else {
        for cert in CertificateDer::pem_slice_iter(ca_pem.as_bytes()) {
            let cert = cert.map_err(|e| UpstreamError::Tls(format!("ca_pem: {e}")))?;
            roots
                .add(cert)
                .map_err(|e| UpstreamError::Tls(format!("ca_pem: {e}")))?;
        }
        if roots.is_empty() {
            return Err(UpstreamError::Tls("ca_pem holds no certificate".into()));
        }
    }
    let provider = Arc::new(rustls::crypto::ring::default_provider());
    let config = rustls::ClientConfig::builder_with_provider(provider)
        .with_safe_default_protocol_versions()
        .map_err(|e| UpstreamError::Tls(e.to_string()))?
        .with_root_certificates(roots)
        .with_no_client_auth();
    Ok(Arc::new(config))
}

/// Maps a TLS connect error to `Tls` when rustls rejected the handshake.
fn tls_io_error(e: std::io::Error) -> UpstreamError {
    match e
        .get_ref()
        .and_then(|inner| inner.downcast_ref::<rustls::Error>())
    {
        Some(tls) => UpstreamError::Tls(tls.to_string()),
        None => UpstreamError::Io(e),
    }
}

pub struct Forwarded {
    pub response: Bytes,
    pub upstream_index: usize,
    pub rtt: Duration,
}

#[allow(clippy::large_enum_variant)] // one per upstream per worker, behind an Rc
enum Transport {
    Udp(udp::UdpPool),
    Tcp(SocketAddr),
    Dot(dot::DotClient),
    Doh(doh::DohClient),
}

/// Worker-local transports, created on first use per upstream id.
pub struct WorkerUpstreams {
    mismatched: Arc<CachePadded<AtomicU64>>,
    transports: RefCell<FxHashMap<String, (UpstreamSpec, Rc<Transport>)>>,
}

impl WorkerUpstreams {
    pub fn new(mismatched: Arc<CachePadded<AtomicU64>>) -> WorkerUpstreams {
        WorkerUpstreams {
            mismatched,
            transports: RefCell::default(),
        }
    }

    fn transport(&self, spec: &UpstreamSpec) -> Result<Rc<Transport>, UpstreamError> {
        let mut map = self.transports.borrow_mut();
        if let Some((known, t)) = map.get(&spec.id)
            && known == spec
        {
            return Ok(t.clone());
        }
        let addr = || {
            spec.addr.ok_or_else(|| {
                std::io::Error::new(std::io::ErrorKind::InvalidInput, "upstream has no address")
            })
        };
        let t = Rc::new(match spec.protocol {
            Protocol::Udp => Transport::Udp(udp::UdpPool::new(addr()?, self.mismatched.clone())?),
            Protocol::Tcp => Transport::Tcp(addr()?),
            Protocol::Dot => Transport::Dot(dot::DotClient::new(
                addr()?,
                &spec.tls_server_name,
                &spec.ca_pem,
                self.mismatched.clone(),
            )?),
            Protocol::Doh => Transport::Doh(doh::DohClient::new(&spec.doh_url, &spec.ca_pem)?),
        });
        map.insert(spec.id.clone(), (spec.clone(), t.clone()));
        Ok(t)
    }
}

/// Records a failure if an admitted attempt is dropped before it resolves, so a
/// cancelled probe cannot hold the probe slot forever.
struct AttemptGuard<'a> {
    health: &'a Health,
    done: bool,
}

impl Drop for AttemptGuard<'_> {
    fn drop(&mut self) {
        if !self.done {
            self.health.record_failure(clock::now_secs());
        }
    }
}

fn remaining(start: Instant) -> Option<Duration> {
    OVERALL_DEADLINE
        .checked_sub(start.elapsed())
        .filter(|d| !d.is_zero())
}

/// Sends `query` to the set's upstreams in strategy order until one answers.
pub async fn forward(
    set: &UpstreamSet,
    worker: &WorkerUpstreams,
    query: &[u8],
    question: &Question,
) -> Result<Forwarded, UpstreamError> {
    let start = Instant::now();
    let now = clock::now_secs();
    let mut order = Vec::with_capacity(set.specs.len());
    set.order(now, &mut order);
    if order.is_empty() {
        return Err(UpstreamError::NoneAvailable);
    }
    let mut last = UpstreamError::NoneAvailable;
    for index in order {
        if remaining(start).is_none() {
            return Err(UpstreamError::Deadline);
        }
        let (spec, health) = (&set.specs[index], &*set.health[index]);
        if !health.admit(now) {
            continue;
        }
        let mut guard = AttemptGuard {
            health,
            done: false,
        };
        let attempt_start = Instant::now();
        let result = attempt(worker, spec, query, question, start).await;
        guard.done = true;
        match result {
            Ok(response) => {
                let rtt = attempt_start.elapsed();
                health.record_success(rtt);
                health.queries.fetch_add(1, Ordering::Relaxed);
                return Ok(Forwarded {
                    response,
                    upstream_index: index,
                    rtt,
                });
            }
            Err(e) => {
                health.record_failure(clock::now_secs());
                last = e;
            }
        }
    }
    Err(if remaining(start).is_none() {
        UpstreamError::Deadline
    } else {
        last
    })
}

async fn attempt(
    worker: &WorkerUpstreams,
    spec: &UpstreamSpec,
    query: &[u8],
    question: &Question,
    start: Instant,
) -> Result<Bytes, UpstreamError> {
    let timeout = || {
        remaining(start)
            .map(|r| spec.timeout.min(r))
            .ok_or(UpstreamError::Deadline)
    };
    let transport = worker.transport(spec)?;
    match &*transport {
        Transport::Udp(pool) => {
            let reply = pool.exchange(query, question, timeout()?).await?;
            if reply[2] & FLAG_TC == 0 {
                return Ok(reply);
            }
            let addr = spec.addr.ok_or(UpstreamError::Malformed)?;
            tcp::exchange_tcp(addr, query, question, timeout()?).await
        }
        Transport::Tcp(addr) => tcp::exchange_tcp(*addr, query, question, timeout()?).await,
        Transport::Dot(client) => client.exchange(query, question, timeout()?).await,
        Transport::Doh(client) => client.exchange(query, question, timeout()?).await,
    }
}
