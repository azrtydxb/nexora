//! Opt-in record of every outgoing recursion query, for latency tooling
//! (`examples/recursion_latency.rs`). Disabled unless `Trace::enable` was called: the resolver
//! then pays one atomic load per outgoing query.

use hickory_proto::rr::{Name, RecordType};
use parking_lot::Mutex;
use std::net::IpAddr;
use std::sync::atomic::{AtomicBool, Ordering};
use std::time::{Duration, Instant};

/// Why a query was sent.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Default)]
pub enum Phase {
    /// Resolving the client's question (and its CNAME chain).
    #[default]
    Resolve,
    /// DS/DNSKEY lookups for the chain of trust.
    Validate,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Outcome {
    Udp,
    /// Truncated over UDP, answered over TCP.
    Tcp,
    Timeout,
    NetworkError,
    /// Abandoned because a parallel query to another server answered first.
    Lost,
}

#[derive(Clone, Debug)]
pub struct TraceEvent {
    pub start: Instant,
    pub elapsed: Duration,
    pub server: IpAddr,
    pub qname: Name,
    pub qtype: RecordType,
    pub phase: Phase,
    /// Sent while resolving a nameserver address (a glueless delegation).
    pub glueless: bool,
    /// A QNAME-minimised step (the query name is shorter than the name being resolved).
    pub minimised: bool,
    pub outcome: Outcome,
}

#[derive(Default)]
pub struct Trace {
    enabled: AtomicBool,
    events: Mutex<Vec<TraceEvent>>,
}

impl Trace {
    pub fn enable(&self) {
        self.enabled.store(true, Ordering::Relaxed);
    }

    pub fn enabled(&self) -> bool {
        self.enabled.load(Ordering::Relaxed)
    }

    pub fn record(&self, e: TraceEvent) {
        if self.enabled() {
            self.events.lock().push(e);
        }
    }

    /// The events recorded since the last call.
    pub fn take(&self) -> Vec<TraceEvent> {
        std::mem::take(&mut self.events.lock())
    }
}
