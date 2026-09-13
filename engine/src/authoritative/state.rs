//! Process-wide authoritative state that survives snapshot swaps, and the hook run after each
//! applied snapshot.

use super::T_SOA;
use super::notify_in::NotifySink;
use super::notify_out::{NotifyJob, NotifyResult, send_notify};
use super::update::UpdateForwarder;
use crate::proto::{self, EngineMessage, engine_message::Msg};
use crate::runtime::Runtime;
use crate::server::Shared;
use crate::tsig::KeyRing;
use parking_lot::Mutex;
use rustc_hash::FxHashMap;
use std::future::Future;
use std::pin::Pin;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, OnceLock};
use std::time::Duration;
use tokio::sync::{mpsc, oneshot};

/// How long an UPDATE waits for the management plane's `UpdateResult`.
const UPDATE_TIMEOUT: Duration = Duration::from_secs(5);
/// Updates waiting for a result at once; further ones fail (SERVFAIL) without forwarding.
const MAX_PENDING_UPDATES: usize = 1024;

/// NOTIFY retry schedule: 2 s, doubling, 5 attempts.
const NOTIFY_FIRST_TIMEOUT: Duration = Duration::from_secs(2);
const NOTIFY_ATTEMPTS: u32 = 5;
/// How long a NOTIFY whose TSIG key the engine does not hold yet waits for `KeyMaterial` before it
/// counts as `nokey`.
const NOTIFY_KEY_WAIT: Duration = Duration::from_secs(60);
/// `AuthCounters::notify_sent` slots.
const NOTIFY_ACKED: usize = 0;
const NOTIFY_REJECTED: usize = 1;
const NOTIFY_TIMEOUT: usize = 2;
const NOTIFY_NOKEY: usize = 3;

pub struct AuthState {
    /// Hosted-zone TSIG keys from `KeyMaterial` (memory only).
    pub keyring: Arc<KeyRing>,
    /// The control runtime, for work spawned off the workers (NOTIFY, UPDATE forwarding).
    control: OnceLock<tokio::runtime::Handle>,
    /// The newest runtime version `after_apply` has handled.
    applied_version: AtomicU64,
    /// The newest runtime version whose changed zones were notified.
    notified_version: AtomicU64,
    /// The control stream's outbound queue while connected.
    control_tx: Mutex<Option<mpsc::Sender<EngineMessage>>>,
    /// Forwarded updates waiting for their `UpdateResult`, by request id.
    pending_updates: Mutex<FxHashMap<String, oneshot::Sender<proto::UpdateResult>>>,
}

impl AuthState {
    pub fn new() -> Arc<AuthState> {
        Arc::new(AuthState {
            keyring: Arc::new(KeyRing::default()),
            control: OnceLock::new(),
            applied_version: AtomicU64::new(0),
            notified_version: AtomicU64::new(0),
            control_tx: Mutex::new(None),
            pending_updates: Mutex::new(FxHashMap::default()),
        })
    }

    /// Attaches the control stream's outbound queue (`control::session`, after connecting).
    pub fn attach(&self, tx: mpsc::Sender<EngineMessage>) {
        *self.control_tx.lock() = Some(tx);
    }

    /// Detaches the queue when the stream ends; waiting updates fail at once.
    pub fn detach(&self) {
        *self.control_tx.lock() = None;
        self.pending_updates.lock().clear();
    }

    /// Delivers an `UpdateResult` to the update waiting for it (ignored when none is).
    pub fn complete_update(&self, r: proto::UpdateResult) {
        if let Some(waiter) = self.pending_updates.lock().remove(&r.request_id) {
            let _ = waiter.send(r);
        }
    }

    fn try_send(&self, msg: Msg) -> bool {
        self.control_tx
            .lock()
            .as_ref()
            .is_some_and(|tx| tx.try_send(EngineMessage { msg: Some(msg) }).is_ok())
    }

    /// Set once when the control runtime exists; later calls are ignored.
    pub fn set_control_runtime(&self, handle: tokio::runtime::Handle) {
        let _ = self.control.set(handle);
    }

    pub fn control_runtime(&self) -> Option<&tokio::runtime::Handle> {
        self.control.get()
    }
}

impl NotifySink for AuthState {
    fn notify(&self, ev: proto::NotifyReceived) -> bool {
        self.try_send(Msg::NotifyReceived(ev))
    }
}

impl UpdateForwarder for AuthState {
    fn forward(
        &self,
        req: proto::UpdateRequest,
    ) -> Pin<Box<dyn Future<Output = Option<proto::UpdateResult>> + Send + '_>> {
        Box::pin(async move {
            let id = req.request_id.clone();
            let (tx, rx) = oneshot::channel();
            {
                let mut pending = self.pending_updates.lock();
                if pending.len() >= MAX_PENDING_UPDATES || pending.contains_key(&id) {
                    return None;
                }
                pending.insert(id.clone(), tx);
            }
            if !self.try_send(Msg::UpdateRequest(req)) {
                self.pending_updates.lock().remove(&id);
                return None;
            }
            match tokio::time::timeout(UPDATE_TIMEOUT, rx).await {
                Ok(Ok(r)) => Some(r),
                _ => {
                    self.pending_updates.lock().remove(&id);
                    None
                }
            }
        })
    }
}

/// Once per runtime version: adds `rt.auth_loads` to `shared.metrics.auth`, and (once the
/// control runtime is set) sends NOTIFY for every zone in `rt.auth_changed` to its targets. A
/// target whose TSIG key is not in the key ring yet is queued until `KeyMaterial` delivers it, for
/// at most `NOTIFY_KEY_WAIT`, then counted `nokey`.
pub fn after_apply(shared: &Shared, rt: &Runtime) {
    if shared
        .auth
        .applied_version
        .fetch_max(rt.version, Ordering::SeqCst)
        < rt.version
    {
        let loads = &shared.metrics.auth.loads;
        for (counter, n) in loads.iter().zip([
            rt.auth_loads.full,
            rt.auth_loads.delta,
            rt.auth_loads.reused,
        ]) {
            counter.fetch_add(n, Ordering::Relaxed);
        }
    }
    let Some(handle) = shared.auth.control_runtime() else {
        return;
    };
    if shared
        .auth
        .notified_version
        .fetch_max(rt.version, Ordering::SeqCst)
        >= rt.version
    {
        return;
    }
    let counters = &shared.metrics.auth.notify_sent;
    for (origin, _) in &rt.auth_changed {
        let Some(zone) = rt.auth.get(origin) else {
            continue;
        };
        let Some(soa) = zone.apex().get(T_SOA) else {
            continue;
        };
        let rdata = zone.soa_rdata();
        let mut soa_rr = Vec::with_capacity(origin.len() + 10 + rdata.len());
        soa_rr.extend_from_slice(origin);
        soa_rr.extend_from_slice(&T_SOA.to_be_bytes());
        soa_rr.extend_from_slice(&1u16.to_be_bytes());
        soa_rr.extend_from_slice(&soa.ttl.to_be_bytes());
        soa_rr.extend_from_slice(&(rdata.len() as u16).to_be_bytes());
        soa_rr.extend_from_slice(rdata);
        for (target, key_name) in &zone.notify {
            let (zone, soa_rr, target) = (origin.clone(), soa_rr.clone(), *target);
            let (key_name, keyring) = (key_name.clone(), shared.auth.keyring.clone());
            let counters = counters.clone();
            handle.spawn(async move {
                let key = match key_name {
                    None => None,
                    Some(name) => match keyring.wait_for(&name, NOTIFY_KEY_WAIT).await {
                        Some(k) => Some(k),
                        None => {
                            counters[NOTIFY_NOKEY].fetch_add(1, Ordering::Relaxed);
                            return;
                        }
                    },
                };
                let job = NotifyJob {
                    zone,
                    soa_rr,
                    target,
                    key,
                };
                let slot = match send_notify(job, NOTIFY_FIRST_TIMEOUT, NOTIFY_ATTEMPTS).await {
                    NotifyResult::Acked { .. } => NOTIFY_ACKED,
                    NotifyResult::Rejected(_) => NOTIFY_REJECTED,
                    NotifyResult::Timeout => NOTIFY_TIMEOUT,
                    NotifyResult::NoKey => NOTIFY_NOKEY,
                };
                counters[slot].fetch_add(1, Ordering::Relaxed);
            });
        }
    }
}
