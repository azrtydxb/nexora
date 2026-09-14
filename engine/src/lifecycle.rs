//! Readiness and graceful shutdown.
//!
//! An engine is ready once it serves an applied configuration (a persisted or received snapshot,
//! filter index built — `Runtime::build` builds it before the swap) and until shutdown starts.
//! On SIGTERM it reports not ready, keeps serving every transport for the drain period (the load
//! balancer moves clients to another endpoint meanwhile), stops accepting stream connections,
//! waits for in-flight resolutions and exits.

use crate::server::Shared;
use std::sync::atomic::{AtomicBool, Ordering};
use std::time::{Duration, Instant};
use tokio::sync::watch;

/// Upper bound on the wait for in-flight resolutions after the listeners closed; upstream
/// timeouts are at most 5 s.
pub const IN_FLIGHT_GRACE: Duration = Duration::from_secs(5);
const IN_FLIGHT_POLL: Duration = Duration::from_millis(20);

pub struct Lifecycle {
    draining: AtomicBool,
    stop_accepting: watch::Sender<bool>,
}

impl Default for Lifecycle {
    fn default() -> Self {
        Lifecycle {
            draining: AtomicBool::new(false),
            stop_accepting: watch::Sender::new(false),
        }
    }
}

impl Lifecycle {
    pub fn start_draining(&self) {
        self.draining.store(true, Ordering::Relaxed);
    }

    pub fn is_draining(&self) -> bool {
        self.draining.load(Ordering::Relaxed)
    }

    /// Ends every listener's accept loop (TCP, DoT, DoH, DoQ); UDP keeps answering until exit.
    pub fn stop_accepting(&self) {
        self.stop_accepting.send_replace(true);
    }

    /// Completes once [`Lifecycle::stop_accepting`] was called (also when already called).
    pub fn accepting_stopped(&self) -> impl Future<Output = ()> + 'static {
        let mut rx = self.stop_accepting.subscribe();
        async move {
            let _ = rx.wait_for(|stopped| *stopped).await;
        }
    }
}

/// Why the engine is not ready, or `None` when it is.
pub fn not_ready_reason(shared: &Shared) -> Option<&'static str> {
    if shared.lifecycle.is_draining() {
        Some("draining")
    } else if shared.runtime.load().version == 0 {
        Some("no configuration applied")
    } else {
        None
    }
}

/// The shutdown sequence after SIGTERM: not ready at once, serve for `drain`, stop accepting
/// connections, then wait (at most [`IN_FLIGHT_GRACE`]) until no resolution is in flight.
pub async fn drain(shared: &Shared, drain: Duration) {
    shared.lifecycle.start_draining();
    eprintln!(
        "nexora-engine: shutting down: not ready, serving for {} s",
        drain.as_secs()
    );
    tokio::time::sleep(drain).await;
    shared.lifecycle.stop_accepting();
    let deadline = Instant::now() + IN_FLIGHT_GRACE;
    while !shared.inflight.is_empty() && Instant::now() < deadline {
        tokio::time::sleep(IN_FLIGHT_POLL).await;
    }
    eprintln!(
        "nexora-engine: drained; exiting with {} resolutions in flight",
        shared.inflight.len()
    );
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::runtime::Runtime;

    #[tokio::test]
    async fn ready_only_with_a_configuration_and_until_draining() {
        let shared = Shared::new(1);
        assert_eq!(not_ready_reason(&shared), Some("no configuration applied"));
        let mut rt = Runtime::initial();
        rt.version = 3;
        shared.runtime.store(std::sync::Arc::new(rt));
        assert_eq!(not_ready_reason(&shared), None);

        let stopped = shared.lifecycle.accepting_stopped();
        let started = tokio::time::Instant::now();
        let sequence = drain(&shared, Duration::from_millis(200));
        tokio::pin!(sequence);
        tokio::select! {
            _ = &mut sequence => panic!("drain ended before the drain period"),
            _ = tokio::time::sleep(Duration::from_millis(50)) => {}
        }
        assert_eq!(not_ready_reason(&shared), Some("draining"));
        sequence.await;
        assert!(started.elapsed() >= Duration::from_millis(200));
        tokio::time::timeout(Duration::from_millis(10), stopped)
            .await
            .expect("accept loops told to stop");
        // A listener started after the stop sees it too.
        tokio::time::timeout(
            Duration::from_millis(10),
            shared.lifecycle.accepting_stopped(),
        )
        .await
        .unwrap();
    }
}
