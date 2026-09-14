//! Per-worker DNS-over-TCP listener: one `serve_dns_stream` task per connection.

use super::{ClientInfo, WorkerAnswerer, WorkerCtx, proxy, stream};
use crate::edns::Transport;
use crate::telemetry::metrics::ConnectionGuard;
use socket2::{Domain, Protocol, Socket, Type};
use std::io;
use std::net::SocketAddr;
use std::rc::Rc;
use std::time::Duration;
use tokio::net::TcpListener;

pub const IDLE_TIMEOUT: Duration = Duration::from_secs(10);
const BACKLOG: i32 = 1024;
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
    let answerer = Rc::new(WorkerAnswerer(ctx));
    loop {
        match listener.accept().await {
            Ok((tcp, peer)) => {
                let _ = tcp.set_nodelay(true);
                let answerer = answerer.clone();
                tokio::task::spawn_local(async move {
                    let _guard = ConnectionGuard::new(Transport::Tcp);
                    stream::serve_dns_stream(
                        answerer,
                        tcp,
                        ClientInfo {
                            addr: proxy::normalize_peer(peer),
                            transport: Transport::Tcp,
                        },
                        IDLE_TIMEOUT,
                    )
                    .await
                });
            }
            Err(_) => tokio::time::sleep(ACCEPT_BACKOFF).await,
        }
    }
}
