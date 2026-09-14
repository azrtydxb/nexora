//! Per-worker, per-upstream pool of connected UDP sockets on kernel-random ports.

use super::{IdSource, QueryBuf, Question, UpstreamError, Waiters};
use bytes::Bytes;
use crossbeam_utils::CachePadded;
use socket2::{Domain, Socket, Type};
use std::cell::{Cell, RefCell};
use std::net::{Ipv4Addr, Ipv6Addr, SocketAddr};
use std::rc::Rc;
use std::sync::Arc;
use std::sync::atomic::AtomicU64;
use std::time::Duration;
use tokio::net::UdpSocket;
use tokio::sync::Notify;

pub const POOL_SIZE: usize = 16;
pub const QUERIES_PER_SOCKET: u32 = 1024;
const HEADER_LEN: usize = 12;
const RECV_BUF: usize = 4096;

struct PooledSocket {
    sock: UdpSocket,
    waiters: Waiters,
    sent: Cell<u32>,
    retired: Cell<bool>,
    reader_started: Cell<bool>,
    /// Wakes the reader of a retired socket once its last waiter is gone.
    drained: Notify,
}

impl PooledSocket {
    fn open(addr: SocketAddr) -> std::io::Result<PooledSocket> {
        let s = Socket::new(
            Domain::for_address(addr),
            Type::DGRAM,
            Some(socket2::Protocol::UDP),
        )?;
        s.set_nonblocking(true)?;
        let any: SocketAddr = match addr {
            SocketAddr::V4(_) => (Ipv4Addr::UNSPECIFIED, 0).into(),
            SocketAddr::V6(_) => (Ipv6Addr::UNSPECIFIED, 0).into(),
        };
        s.bind(&any.into())?;
        s.connect(&addr.into())?;
        Ok(PooledSocket {
            sock: UdpSocket::from_std(s.into())?,
            waiters: Waiters::default(),
            sent: Cell::new(0),
            retired: Cell::new(false),
            reader_started: Cell::new(false),
            drained: Notify::new(),
        })
    }

    fn finished(&self) -> bool {
        self.retired.get() && self.waiters.is_empty()
    }
}

async fn read_loop(sock: Rc<PooledSocket>, mismatched: Arc<CachePadded<AtomicU64>>) {
    let mut buf = [0u8; RECV_BUF];
    while !sock.finished() {
        tokio::select! {
            r = sock.sock.recv(&mut buf) => {
                // Errors (e.g. ICMP port unreachable) surface to waiters as timeouts.
                if let Ok(n) = r {
                    sock.waiters.deliver(&buf[..n], &mismatched);
                }
            }
            () = sock.drained.notified() => {}
        }
    }
}

/// Removes a waiter whose exchange ended without a reply (timeout, error, cancel).
struct WaiterGuard<'a> {
    sock: &'a PooledSocket,
    id: u16,
}

impl Drop for WaiterGuard<'_> {
    fn drop(&mut self) {
        self.sock.waiters.cancel(self.id);
        if self.sock.finished() {
            self.sock.drained.notify_one();
        }
    }
}

pub struct UdpPool {
    addr: SocketAddr,
    mismatched: Arc<CachePadded<AtomicU64>>,
    slots: RefCell<Vec<Rc<PooledSocket>>>,
    next: Cell<usize>,
    created: Cell<u64>,
    ids: IdSource,
}

impl UdpPool {
    pub fn new(
        addr: SocketAddr,
        mismatched: Arc<CachePadded<AtomicU64>>,
    ) -> std::io::Result<UdpPool> {
        let slots = (0..POOL_SIZE)
            .map(|_| PooledSocket::open(addr).map(Rc::new))
            .collect::<std::io::Result<Vec<_>>>()?;
        Ok(UdpPool {
            addr,
            mismatched,
            slots: RefCell::new(slots),
            next: Cell::new(0),
            created: Cell::new(POOL_SIZE as u64),
            ids: IdSource::new(),
        })
    }

    /// The next socket round-robin, replacing it first when it has sent its quota.
    fn pick(&self) -> Result<Rc<PooledSocket>, UpstreamError> {
        let i = self.next.get();
        self.next.set((i + 1) % POOL_SIZE);
        let mut slots = self.slots.borrow_mut();
        if slots[i].sent.get() >= QUERIES_PER_SOCKET {
            let fresh = Rc::new(PooledSocket::open(self.addr)?);
            self.created.set(self.created.get() + 1);
            let old = std::mem::replace(&mut slots[i], fresh);
            old.retired.set(true);
            if old.finished() {
                old.drained.notify_one();
            }
        }
        let sock = slots[i].clone();
        sock.sent.set(sock.sent.get() + 1);
        if !sock.reader_started.replace(true) {
            tokio::task::spawn_local(read_loop(sock.clone(), self.mismatched.clone()));
        }
        Ok(sock)
    }

    /// Sends `query` under a random ID and waits for the matching reply, which
    /// is returned with the query's original ID.
    pub async fn exchange(
        &self,
        query: &[u8],
        question: &Question,
        timeout: Duration,
    ) -> Result<Bytes, UpstreamError> {
        if query.len() < HEADER_LEN {
            return Err(UpstreamError::Malformed);
        }
        let sock = self.pick()?;
        let (id, rx) = sock.waiters.register(query, question, &self.ids);
        let _guard = WaiterGuard { sock: &sock, id };
        let wire = QueryBuf::new(query, id);
        tokio::time::timeout(timeout, async {
            sock.sock.send(wire.as_slice()).await?;
            rx.await
                .map_err(|_| UpstreamError::Io(std::io::Error::other("udp reader stopped")))
        })
        .await
        .map_err(|_| UpstreamError::Timeout)?
    }

    pub fn local_ports(&self) -> Vec<u16> {
        self.slots
            .borrow()
            .iter()
            .filter_map(|s| s.sock.local_addr().ok().map(|a| a.port()))
            .collect()
    }

    pub fn addr(&self) -> SocketAddr {
        self.addr
    }

    /// Exchanges waiting for a reply across the pool's current sockets.
    pub fn pending(&self) -> usize {
        self.slots.borrow().iter().map(|s| s.waiters.len()).sum()
    }

    pub fn sockets_created(&self) -> u64 {
        self.created.get()
    }
}
