//! Per-worker DNS-over-TCP listener: length-framed queries, one task per connection.

use super::{FastOutcome, WorkerCtx, handle_packet, resolve_miss};
use crate::edns::Transport;
use socket2::{Domain, Protocol, Socket, Type};
use std::io;
use std::net::SocketAddr;
use std::rc::Rc;
use std::time::Duration;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::{TcpListener, TcpStream};
use tokio::time::timeout;

pub const IDLE_TIMEOUT: Duration = Duration::from_secs(10);
const BACKLOG: i32 = 1024;
const MAX_MESSAGE: usize = 65535;
/// Back-off after a failed accept (e.g. EMFILE) so the loop does not spin.
const ACCEPT_BACKOFF: Duration = Duration::from_millis(50);

pub fn bind_tcp(addr: SocketAddr) -> io::Result<std::net::TcpListener> {
    let s = Socket::new(Domain::for_address(addr), Type::STREAM, Some(Protocol::TCP))?;
    s.set_reuse_address(true)?;
    s.set_reuse_port(true)?;
    if addr.is_ipv6() {
        s.set_only_v6(true)?;
    }
    s.set_nonblocking(true)?;
    s.bind(&addr.into())?;
    s.listen(BACKLOG)?;
    Ok(s.into())
}

pub async fn run_tcp(ctx: Rc<WorkerCtx>, listener: std::net::TcpListener) {
    let listener = match TcpListener::from_std(listener) {
        Ok(l) => l,
        Err(e) => {
            eprintln!("nexora-engine: tcp listener: {e}");
            return;
        }
    };
    loop {
        match listener.accept().await {
            Ok((stream, peer)) => {
                tokio::task::spawn_local(serve_connection(ctx.clone(), stream, peer));
            }
            Err(_) => tokio::time::sleep(ACCEPT_BACKOFF).await,
        }
    }
}

/// Answers queries in order until EOF, a zero length, an unanswerable query,
/// or `IDLE_TIMEOUT` on any read or write.
async fn serve_connection(ctx: Rc<WorkerCtx>, mut stream: TcpStream, peer: SocketAddr) {
    let _ = stream.set_nodelay(true);
    // One allocation per connection: the query, then the length-prefixed reply.
    // debt: 131 KiB per open connection; pool or shrink buffers if idle
    // connection counts grow into the thousands.
    let mut buf = vec![0u8; MAX_MESSAGE + 2 + MAX_MESSAGE];
    loop {
        let (query, frame) = buf.split_at_mut(MAX_MESSAGE);
        let len = match timeout(IDLE_TIMEOUT, stream.read_u16()).await {
            Ok(Ok(len)) if len > 0 => usize::from(len),
            _ => return,
        };
        if !matches!(
            timeout(IDLE_TIMEOUT, stream.read_exact(&mut query[..len])).await,
            Ok(Ok(_))
        ) {
            return;
        }
        let outcome = {
            let rt = ctx.shared.runtime.load();
            handle_packet(
                &ctx,
                &rt,
                &query[..len],
                peer,
                Transport::Tcp,
                &mut frame[2..],
            )
        };
        let n = match outcome {
            FastOutcome::Reply(n) => n,
            FastOutcome::Drop => return,
            FastOutcome::Miss(job) => {
                let rt = ctx.shared.runtime.load_full();
                let reply = resolve_miss(ctx.clone(), rt, job).await;
                if reply.is_empty() || reply.len() > MAX_MESSAGE {
                    return;
                }
                frame[2..2 + reply.len()].copy_from_slice(&reply);
                reply.len()
            }
        };
        frame[..2].copy_from_slice(&(n as u16).to_be_bytes());
        if !matches!(
            timeout(IDLE_TIMEOUT, stream.write_all(&frame[..2 + n])).await,
            Ok(Ok(()))
        ) {
            return;
        }
    }
}
