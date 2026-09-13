//! Per-worker UDP listener: `recvmmsg` a batch, answer what the fast path can
//! inline, flush those replies with one `sendmmsg`, and spawn a task per miss.

use super::{FastOutcome, MissJob, WorkerCtx, handle_packet, resolve_miss};
use crate::edns::Transport;
use crate::runtime::Runtime;
use socket2::{Domain, Protocol, Socket, Type};
use std::io;
use std::mem::size_of;
use std::net::{Ipv4Addr, Ipv6Addr, SocketAddr, SocketAddrV4, SocketAddrV6, UdpSocket};
use std::os::fd::AsRawFd;
use std::rc::Rc;
use std::sync::Arc;
use tokio::io::unix::AsyncFd;

pub const BATCH: usize = 64;
const RECV_SIZE: usize = 4096;
/// Replies from the fast path never exceed the UDP cap of 1232 bytes.
const SEND_SIZE: usize = 1232;
const SOCKET_BUFFER: usize = 4 << 20;

pub fn bind_udp(addr: SocketAddr) -> io::Result<UdpSocket> {
    let s = Socket::new(Domain::for_address(addr), Type::DGRAM, Some(Protocol::UDP))?;
    s.set_reuse_port(true)?;
    if addr.is_ipv6() {
        s.set_only_v6(true)?;
    }
    // The kernel clamps these to net.core.[rw]mem_max.
    s.set_recv_buffer_size(SOCKET_BUFFER)?;
    s.set_send_buffer_size(SOCKET_BUFFER)?;
    s.set_nonblocking(true)?;
    s.bind(&addr.into())?;
    Ok(s.into())
}

/// `BATCH` all-zero C structs.
fn zeroed_array<T>() -> Box<[T]> {
    // SAFETY: only instantiated with plain libc structs, for which all-zero
    // bytes (null pointers, zero lengths) are valid values.
    (0..BATCH).map(|_| unsafe { std::mem::zeroed() }).collect()
}

/// Preallocated datagram buffers with their `mmsghdr`/`iovec`/address arrays.
struct Batch {
    size: usize,
    bufs: Box<[u8]>,
    names: Box<[libc::sockaddr_storage]>,
    iovs: Box<[libc::iovec]>,
    msgs: Box<[libc::mmsghdr]>,
}

impl Batch {
    fn new(size: usize) -> Batch {
        Batch {
            size,
            bufs: vec![0u8; BATCH * size].into_boxed_slice(),
            names: zeroed_array(),
            iovs: zeroed_array(),
            msgs: zeroed_array(),
        }
    }

    fn buf(&self, i: usize) -> &[u8] {
        &self.bufs[i * self.size..(i + 1) * self.size]
    }

    fn buf_mut(&mut self, i: usize) -> &mut [u8] {
        &mut self.bufs[i * self.size..(i + 1) * self.size]
    }

    /// Points the first `count` headers at their buffers and addresses. Called
    /// right before each syscall so no Rust reference outlives the pointers.
    fn wire_up(&mut self, count: usize, receive: bool) {
        let bufs = self.bufs.as_mut_ptr();
        let names = self.names.as_mut_ptr();
        let iovs = self.iovs.as_mut_ptr();
        for i in 0..count {
            let msg = &mut self.msgs[i];
            // SAFETY: i < BATCH, the length of every array.
            unsafe {
                (*iovs.add(i)).iov_base = bufs.add(i * self.size).cast();
                if receive {
                    (*iovs.add(i)).iov_len = self.size;
                }
                msg.msg_hdr.msg_name = names.add(i).cast();
                msg.msg_hdr.msg_iov = iovs.add(i);
            }
            msg.msg_hdr.msg_iovlen = 1;
            msg.msg_hdr.msg_control = std::ptr::null_mut();
            msg.msg_hdr.msg_controllen = 0;
            msg.msg_hdr.msg_flags = 0;
            if receive {
                msg.msg_hdr.msg_namelen = size_of::<libc::sockaddr_storage>() as libc::socklen_t;
                msg.msg_len = 0;
            }
        }
    }
}

pub async fn run_udp(ctx: Rc<WorkerCtx>, sock: UdpSocket) {
    let fd = match AsyncFd::new(sock) {
        Ok(fd) => Rc::new(fd),
        Err(e) => {
            eprintln!("nexora-engine: udp listener: {e}");
            return;
        }
    };
    let mut rx = Batch::new(RECV_SIZE);
    let mut tx = Batch::new(SEND_SIZE);
    loop {
        let received = recv_batch(&fd, &mut rx).await;
        let mut queued = 0;
        for i in 0..received {
            let Some(client) = to_socket_addr(&rx.names[i]) else {
                continue;
            };
            let len = rx.msgs[i].msg_len as usize;
            let rt = ctx.shared.runtime.load();
            let outcome = handle_packet(
                &ctx,
                &rt,
                &rx.buf(i)[..len],
                client,
                Transport::Udp,
                tx.buf_mut(queued),
            );
            match outcome {
                FastOutcome::Reply(n) => {
                    tx.names[queued] = rx.names[i];
                    tx.msgs[queued].msg_hdr.msg_namelen = rx.msgs[i].msg_hdr.msg_namelen;
                    tx.iovs[queued].iov_len = n;
                    queued += 1;
                }
                FastOutcome::Drop => {}
                FastOutcome::Miss(job) => {
                    let rt = ctx.shared.runtime.load_full();
                    tokio::task::spawn_local(answer_miss(ctx.clone(), fd.clone(), rt, job));
                }
            }
        }
        if queued > 0 {
            send_batch(&fd, &mut tx, queued).await;
        }
    }
}

/// Waits for readiness and receives up to `BATCH` datagrams.
async fn recv_batch(fd: &AsyncFd<UdpSocket>, rx: &mut Batch) -> usize {
    loop {
        let Ok(mut guard) = fd.readable().await else {
            continue;
        };
        rx.wire_up(BATCH, true);
        let result = guard.try_io(|inner| {
            // SAFETY: `wire_up` pointed every header at live buffers of `rx`.
            let n = unsafe {
                libc::recvmmsg(
                    inner.as_raw_fd(),
                    rx.msgs.as_mut_ptr(),
                    BATCH as _,
                    libc::MSG_DONTWAIT,
                    std::ptr::null_mut(),
                )
            };
            if n < 0 {
                Err(io::Error::last_os_error())
            } else {
                Ok(n as usize)
            }
        });
        match result {
            Ok(Ok(n)) if n > 0 => return n,
            // Transient errors (EINTR, ENOMEM) and would-block: wait again.
            _ => {}
        }
    }
}

/// Sends the first `count` queued replies, waiting on `EAGAIN` and dropping a
/// datagram the kernel refuses outright.
async fn send_batch(fd: &AsyncFd<UdpSocket>, tx: &mut Batch, count: usize) {
    tx.wire_up(count, false);
    let mut sent = 0;
    while sent < count {
        let Ok(mut guard) = fd.writable().await else {
            return;
        };
        let result = guard.try_io(|inner| {
            // SAFETY: `wire_up` pointed headers 0..count at live buffers of `tx`.
            let n = unsafe {
                libc::sendmmsg(
                    inner.as_raw_fd(),
                    tx.msgs.as_mut_ptr().add(sent),
                    (count - sent) as _,
                    0,
                )
            };
            if n < 0 {
                Err(io::Error::last_os_error())
            } else {
                Ok(n as usize)
            }
        });
        match result {
            Ok(Ok(n)) => sent += n.max(1),
            Ok(Err(e)) if e.kind() == io::ErrorKind::Interrupted => {}
            Ok(Err(_)) => sent += 1,
            Err(_would_block) => {}
        }
    }
}

async fn answer_miss(
    ctx: Rc<WorkerCtx>,
    fd: Rc<AsyncFd<UdpSocket>>,
    rt: Arc<Runtime>,
    job: MissJob,
) {
    let client = job.client;
    let reply = resolve_miss(ctx, rt, job).await;
    if reply.is_empty() {
        return;
    }
    loop {
        let Ok(mut guard) = fd.writable().await else {
            return;
        };
        // Any error other than would-block drops the reply.
        if guard
            .try_io(|inner| inner.get_ref().send_to(&reply, client))
            .is_ok()
        {
            return;
        }
    }
}

fn to_socket_addr(s: &libc::sockaddr_storage) -> Option<SocketAddr> {
    match i32::from(s.ss_family) {
        libc::AF_INET => {
            // SAFETY: the family says the storage holds a sockaddr_in.
            let a = unsafe { &*(s as *const libc::sockaddr_storage).cast::<libc::sockaddr_in>() };
            Some(SocketAddr::V4(SocketAddrV4::new(
                Ipv4Addr::from(u32::from_be(a.sin_addr.s_addr)),
                u16::from_be(a.sin_port),
            )))
        }
        libc::AF_INET6 => {
            // SAFETY: the family says the storage holds a sockaddr_in6.
            let a = unsafe { &*(s as *const libc::sockaddr_storage).cast::<libc::sockaddr_in6>() };
            Some(SocketAddr::V6(SocketAddrV6::new(
                Ipv6Addr::from(a.sin6_addr.s6_addr),
                u16::from_be(a.sin6_port),
                a.sin6_flowinfo,
                a.sin6_scope_id,
            )))
        }
        _ => None,
    }
}
