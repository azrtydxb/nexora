//! Process-wide authoritative state that survives snapshot swaps, and the hook run after each
//! applied snapshot.

use crate::runtime::Runtime;
use crate::server::Shared;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, OnceLock};

pub struct AuthState {
    /// The control runtime, for work spawned off the workers (NOTIFY, UPDATE forwarding).
    control: OnceLock<tokio::runtime::Handle>,
    /// The newest runtime version `after_apply` has handled.
    applied_version: AtomicU64,
}

impl AuthState {
    pub fn new() -> Arc<AuthState> {
        Arc::new(AuthState {
            control: OnceLock::new(),
            applied_version: AtomicU64::new(0),
        })
    }

    /// Set once when the control runtime exists; later calls are ignored.
    pub fn set_control_runtime(&self, handle: tokio::runtime::Handle) {
        let _ = self.control.set(handle);
    }

    pub fn control_runtime(&self) -> Option<&tokio::runtime::Handle> {
        self.control.get()
    }
}

/// Once per runtime version: adds `rt.auth_loads` to `shared.metrics.auth`.
pub fn after_apply(shared: &Shared, rt: &Runtime) {
    if shared
        .auth
        .applied_version
        .fetch_max(rt.version, Ordering::SeqCst)
        >= rt.version
    {
        return;
    }
    let loads = &shared.metrics.auth.loads;
    for (counter, n) in loads.iter().zip([
        rt.auth_loads.full,
        rt.auth_loads.delta,
        rt.auth_loads.reused,
    ]) {
        counter.fetch_add(n, Ordering::Relaxed);
    }
}
