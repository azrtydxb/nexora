//! One-shot DNS-over-TCP exchange.

use super::{Question, UpstreamError};
use crate::wire;
use bytes::Bytes;
use rand::RngExt;
use std::net::SocketAddr;
use std::time::Duration;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::TcpStream;

const HEADER_LEN: usize = 12;

pub async fn exchange_tcp(
    addr: SocketAddr,
    query: &[u8],
    question: &Question,
    timeout: Duration,
) -> Result<Bytes, UpstreamError> {
    if query.len() < HEADER_LEN || query.len() > usize::from(u16::MAX) {
        return Err(UpstreamError::Malformed);
    }
    tokio::time::timeout(timeout, async {
        let mut stream = TcpStream::connect(addr).await?;
        stream.set_nodelay(true)?;
        let id: u16 = rand::rng().random();
        let mut frame = Vec::with_capacity(2 + query.len());
        frame.extend_from_slice(&(query.len() as u16).to_be_bytes());
        frame.extend_from_slice(query);
        frame[2..4].copy_from_slice(&id.to_be_bytes());
        stream.write_all(&frame).await?;
        let len = usize::from(stream.read_u16().await?);
        let mut reply = vec![0u8; len];
        stream.read_exact(&mut reply).await?;
        if !reply_matches(&reply, id, question) {
            return Err(UpstreamError::Malformed);
        }
        reply[..2].copy_from_slice(&query[..2]);
        Ok(Bytes::from(reply))
    })
    .await
    .map_err(|_| UpstreamError::Timeout)?
}

/// QR set, the sent ID, and the query's question (case-insensitively).
pub(crate) fn reply_matches(reply: &[u8], id: u16, question: &Question) -> bool {
    reply.len() >= HEADER_LEN
        && reply[2] & 0x80 != 0
        && u16::from_be_bytes([reply[0], reply[1]]) == id
        && wire::question_matches(reply, &question.key, question.qtype, question.qclass)
}
