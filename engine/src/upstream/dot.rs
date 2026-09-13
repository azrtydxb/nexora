//! DNS over TLS: one persistent, pipelined connection per worker per upstream.

use super::{IdSource, Question, UpstreamError, Waiters, client_tls_config, tls_io_error};
use bytes::Bytes;
use crossbeam_utils::CachePadded;
use rustls::pki_types::ServerName;
use std::cell::Cell;
use std::net::SocketAddr;
use std::rc::Rc;
use std::sync::Arc;
use std::sync::atomic::AtomicU64;
use std::time::Duration;
use tokio::io::{AsyncReadExt, AsyncWriteExt, ReadHalf, WriteHalf};
use tokio::net::TcpStream;
use tokio::sync::{Mutex, Notify};
use tokio_rustls::TlsConnector;
use tokio_rustls::client::TlsStream;

const HEADER_LEN: usize = 12;

struct DotConn {
    writer: Mutex<WriteHalf<TlsStream<TcpStream>>>,
    waiters: Waiters,
    dead: Cell<bool>,
    /// Stops the reader when the client is dropped.
    closed: Notify,
}

impl DotConn {
    fn kill(&self) {
        self.dead.set(true);
        self.waiters.clear();
    }
}

pub struct DotClient {
    addr: SocketAddr,
    server_name: ServerName<'static>,
    connector: TlsConnector,
    mismatched: Arc<CachePadded<AtomicU64>>,
    conn: Mutex<Option<Rc<DotConn>>>,
    opened: Cell<u64>,
    ids: IdSource,
}

impl DotClient {
    pub fn new(
        addr: SocketAddr,
        server_name: &str,
        ca_pem: &str,
        mismatched: Arc<CachePadded<AtomicU64>>,
    ) -> Result<DotClient, UpstreamError> {
        let server_name = ServerName::try_from(server_name.to_owned())
            .map_err(|e| UpstreamError::Tls(format!("server name: {e}")))?;
        Ok(DotClient {
            addr,
            server_name,
            connector: TlsConnector::from(client_tls_config(ca_pem)?),
            mismatched,
            conn: Mutex::new(None),
            opened: Cell::new(0),
            ids: IdSource::new(),
        })
    }

    /// The live connection, connecting first when there is none or it died.
    async fn connection(&self) -> Result<Rc<DotConn>, UpstreamError> {
        let mut slot = self.conn.lock().await;
        if let Some(conn) = slot.as_ref().filter(|c| !c.dead.get()) {
            return Ok(conn.clone());
        }
        let tcp = TcpStream::connect(self.addr).await?;
        tcp.set_nodelay(true)?;
        let tls = self
            .connector
            .connect(self.server_name.clone(), tcp)
            .await
            .map_err(tls_io_error)?;
        self.opened.set(self.opened.get() + 1);
        let (reader, writer) = tokio::io::split(tls);
        let conn = Rc::new(DotConn {
            writer: Mutex::new(writer),
            waiters: Waiters::default(),
            dead: Cell::new(false),
            closed: Notify::new(),
        });
        tokio::task::spawn_local(read_loop(conn.clone(), reader, self.mismatched.clone()));
        *slot = Some(conn.clone());
        Ok(conn)
    }

    /// Sends `query` on the shared connection under a random ID and waits for
    /// the matching reply, returned with the query's original ID.
    pub async fn exchange(
        &self,
        query: &[u8],
        question: &Question,
        timeout: Duration,
    ) -> Result<Bytes, UpstreamError> {
        if query.len() < HEADER_LEN || query.len() > usize::from(u16::MAX) {
            return Err(UpstreamError::Malformed);
        }
        tokio::time::timeout(timeout, async {
            let conn = self.connection().await?;
            let (id, rx) = conn.waiters.register(query, question, &self.ids);
            let _guard = WaiterGuard { conn: &conn, id };
            let mut frame = Vec::with_capacity(2 + query.len());
            frame.extend_from_slice(&(query.len() as u16).to_be_bytes());
            frame.extend_from_slice(query);
            frame[2..4].copy_from_slice(&id.to_be_bytes());
            {
                let mut writer = conn.writer.lock().await;
                let written = async {
                    writer.write_all(&frame).await?;
                    writer.flush().await
                };
                if let Err(e) = written.await {
                    conn.kill();
                    return Err(UpstreamError::Io(e));
                }
            }
            rx.await
                .map_err(|_| UpstreamError::Io(std::io::Error::other("dot connection closed")))
        })
        .await
        .map_err(|_| UpstreamError::Timeout)?
    }

    pub fn connections_opened(&self) -> u64 {
        self.opened.get()
    }
}

impl Drop for DotClient {
    fn drop(&mut self) {
        if let Some(conn) = self.conn.get_mut().take() {
            conn.kill();
            conn.closed.notify_one();
        }
    }
}

struct WaiterGuard<'a> {
    conn: &'a DotConn,
    id: u16,
}

impl Drop for WaiterGuard<'_> {
    fn drop(&mut self) {
        self.conn.waiters.cancel(self.id);
    }
}

async fn read_loop(
    conn: Rc<DotConn>,
    mut reader: ReadHalf<TlsStream<TcpStream>>,
    mismatched: Arc<CachePadded<AtomicU64>>,
) {
    let mut buf = vec![0u8; usize::from(u16::MAX)];
    loop {
        let frame = async {
            let len = usize::from(reader.read_u16().await?);
            reader.read_exact(&mut buf[..len]).await?;
            Ok::<_, std::io::Error>(len)
        };
        tokio::select! {
            r = frame => match r {
                Ok(len) => conn.waiters.deliver(&buf[..len], &mismatched),
                Err(_) => break,
            },
            () = conn.closed.notified() => break,
        }
    }
    conn.kill();
}
