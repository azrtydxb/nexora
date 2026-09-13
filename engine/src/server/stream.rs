//! Length-prefixed (RFC 7766 framing) pipelined DNS over any byte stream: plain TCP and DoT.

use crate::server::{Answerer, ClientInfo};
use std::rc::Rc;
use std::time::Duration;
use tokio::io::{AsyncRead, AsyncReadExt, AsyncWrite, AsyncWriteExt};
use tokio::time::timeout;

/// Queries answered concurrently per stream; reading pauses while all are busy.
pub const MAX_PIPELINED: usize = 32;
const BODY_TIMEOUT: Duration = Duration::from_secs(5);

/// Reads length-prefixed queries and answers each in its own task, replies in
/// completion order (RFC 7766 section 6.2.1.1). Ends on EOF, a zero length, a read
/// idle for `idle`, a query body slower than 5 s, or a reply write stalled for `idle`.
pub async fn serve_dns_stream<A, S>(answerer: Rc<A>, io: S, client: ClientInfo, idle: Duration)
where
    A: Answerer + 'static,
    S: AsyncRead + AsyncWrite + 'static,
{
    let (mut rd, mut wr) = tokio::io::split(io);
    let (tx, mut rx) = tokio::sync::mpsc::channel::<Vec<u8>>(MAX_PIPELINED);
    let writer = tokio::task::spawn_local(async move {
        while let Some(frame) = rx.recv().await {
            if !matches!(timeout(idle, wr.write_all(&frame)).await, Ok(Ok(()))) {
                break;
            }
            if rx.is_empty() && !matches!(timeout(idle, wr.flush()).await, Ok(Ok(()))) {
                break;
            }
        }
        // Dropping `rx` here fails pending sends, so their permits free up and the
        // reader notices the closed channel.
        drop(rx);
        let _ = timeout(idle, wr.shutdown()).await;
    });
    let permits = std::sync::Arc::new(tokio::sync::Semaphore::new(MAX_PIPELINED));
    loop {
        let mut len = [0u8; 2];
        if !matches!(timeout(idle, rd.read_exact(&mut len)).await, Ok(Ok(_))) {
            break;
        }
        let n = usize::from(u16::from_be_bytes(len));
        if n == 0 {
            break;
        }
        // debt: one query buffer, one 64 KiB answer buffer and one frame per stream
        // query; pool them if stream transports show up in the perf gate.
        let mut msg = vec![0u8; n];
        if !matches!(
            timeout(BODY_TIMEOUT, rd.read_exact(&mut msg)).await,
            Ok(Ok(_))
        ) {
            break;
        }
        let Ok(Ok(permit)) = timeout(idle, permits.clone().acquire_owned()).await else {
            break;
        };
        if tx.is_closed() {
            break;
        }
        let answerer = answerer.clone();
        let tx = tx.clone();
        tokio::task::spawn_local(async move {
            let mut out = Vec::with_capacity(512);
            answerer.answer(client, &msg, &mut out).await;
            if !out.is_empty() && out.len() <= 65535 {
                let mut frame = Vec::with_capacity(out.len() + 2);
                frame.extend_from_slice(&(out.len() as u16).to_be_bytes());
                frame.extend_from_slice(&out);
                let _ = tx.send(frame).await;
            }
            drop(permit);
        });
    }
    drop(tx);
    let _ = writer.await;
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::edns::Transport;
    use crate::server::ClientInfo;
    use crate::server::testutil::{EchoAnswerer, test_query};
    use hickory_proto::op::Message;
    use hickory_proto::rr::{RData, rdata::A};
    use std::net::Ipv4Addr;
    use tokio::io::{AsyncReadExt, AsyncWriteExt};

    #[tokio::test(flavor = "current_thread")]
    async fn pipelined_queries_all_answered_on_one_stream() {
        tokio::task::LocalSet::new()
            .run_until(async {
                let (client_io, server_io) = tokio::io::duplex(64 * 1024);
                let client = ClientInfo {
                    addr: "192.0.2.9:5555".parse().unwrap(),
                    transport: Transport::Dot,
                };
                let server = tokio::task::spawn_local(serve_dns_stream(
                    Rc::new(EchoAnswerer),
                    server_io,
                    client,
                    Duration::from_secs(5),
                ));
                let (mut rd, mut wr) = tokio::io::split(client_io);
                let mut want = Vec::new();
                for i in 0..10u16 {
                    let q = test_query(0x1000 + i, "example.com.");
                    wr.write_all(&(q.len() as u16).to_be_bytes()).await.unwrap();
                    wr.write_all(&q).await.unwrap();
                    want.push(0x1000 + i);
                }
                let mut got = Vec::new();
                for _ in 0..10 {
                    let mut l = [0u8; 2];
                    rd.read_exact(&mut l).await.unwrap();
                    let mut b = vec![0u8; u16::from_be_bytes(l) as usize];
                    rd.read_exact(&mut b).await.unwrap();
                    let m = Message::from_vec(&b).unwrap();
                    assert_eq!(m.answers[1].data, RData::A(A(Ipv4Addr::new(192, 0, 2, 9))));
                    got.push(m.metadata.id);
                }
                got.sort();
                assert_eq!(got, want);
                // a short message is dropped without a reply and a zero length closes the stream
                wr.write_all(&[0, 3, 1, 2, 3, 0, 0]).await.unwrap();
                server.await.unwrap();
                let mut rest = Vec::new();
                rd.read_to_end(&mut rest).await.unwrap();
                assert!(rest.is_empty());
            })
            .await;
    }

    #[tokio::test(flavor = "current_thread", start_paused = true)]
    async fn idle_stream_is_closed() {
        tokio::task::LocalSet::new()
            .run_until(async {
                let (_client_io, server_io) = tokio::io::duplex(1024);
                let client = ClientInfo {
                    addr: "192.0.2.9:5555".parse().unwrap(),
                    transport: Transport::Dot,
                };
                let server = tokio::task::spawn_local(serve_dns_stream(
                    Rc::new(EchoAnswerer),
                    server_io,
                    client,
                    Duration::from_secs(30),
                ));
                tokio::time::advance(Duration::from_secs(31)).await;
                server.await.unwrap();
            })
            .await;
    }
}
