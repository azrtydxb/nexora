//! One query/response exchange with an authoritative or forward-zone server, hardened against
//! off-path spoofing: random transaction ID, random source port on a connected socket, 0x20 case
//! randomisation with strict case matching, and ID + question matching on every reply.

use super::EDNS_BUFFER;
use super::metrics::RecursorMetrics;
use hickory_proto::op::{Edns, Message, MessageType, OpCode, Query};
use hickory_proto::rr::{DNSClass, Name, RecordType};
use rand::Rng;
use std::net::{IpAddr, Ipv4Addr, Ipv6Addr, SocketAddr};
use std::sync::Arc;
use std::time::Duration;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::{TcpStream, UdpSocket};
use tokio::time::{Instant, timeout_at};

/// Attempts at a random source port before letting the kernel choose one.
const PORT_ATTEMPTS: usize = 10;
/// The TCP retry after a truncated reply gets at least this long.
const MIN_TCP_BUDGET: Duration = Duration::from_millis(500);

pub struct OutboundQuery<'a> {
    pub server: SocketAddr,
    pub qname: &'a Name,
    pub qtype: RecordType,
    /// True only for forward-zone servers.
    pub recursion_desired: bool,
    /// OPT with payload `EDNS_BUFFER`.
    pub edns: bool,
    pub dnssec_ok: bool,
    pub checking_disabled: bool,
    pub use_0x20: bool,
    pub timeout: Duration,
}

pub struct Exchange {
    pub message: Message,
    /// From the first send to the accepted reply.
    pub rtt: Duration,
    pub via_tcp: bool,
}

#[derive(Debug)]
pub enum ExchangeError {
    Timeout,
    Network(std::io::ErrorKind),
    TcpFailed(String),
}

#[derive(Debug, PartialEq)]
pub enum Mismatch {
    Id,
    Question,
    Case,
    NotResponse,
}

pub struct Transport {
    metrics: Arc<RecursorMetrics>,
}

impl Transport {
    pub fn new(metrics: Arc<RecursorMetrics>) -> Self {
        Self { metrics }
    }

    pub async fn exchange(&self, q: &OutboundQuery<'_>) -> Result<Exchange, ExchangeError> {
        let m = &self.metrics;
        let mut rng = rand::rng();
        let wire_name = if q.use_0x20 {
            randomise_case(q.qname, &mut rng)
        } else {
            q.qname.clone()
        };
        let id = rng.next_u32() as u16;
        let query = encode_query(q, id, &wire_name)?;
        let socket = bind_random(q.server, &mut rng).await?;
        socket.connect(q.server).await.map_err(net)?;
        let start = Instant::now();
        let deadline = start + q.timeout;
        socket.send(&query).await.map_err(net)?;
        RecursorMetrics::inc(&m.upstream_queries);
        let mut buf = vec![0u8; usize::from(u16::MAX)];
        let reply = loop {
            let n = match timeout_at(deadline, socket.recv(&mut buf)).await {
                Err(_) => {
                    RecursorMetrics::inc(&m.upstream_timeouts);
                    return Err(ExchangeError::Timeout);
                }
                Ok(Err(e)) => return Err(net(e)),
                Ok(Ok(n)) => n,
            };
            if n < 12 {
                RecursorMetrics::inc(&m.malformed_replies);
                continue;
            }
            let Ok(reply) = Message::from_vec(&buf[..n]) else {
                RecursorMetrics::inc(&m.malformed_replies);
                continue;
            };
            match reply_matches(id, &wire_name, q.qtype, q.use_0x20, &reply) {
                Ok(()) => break reply,
                Err(Mismatch::Id) => RecursorMetrics::inc(&m.mismatched_id),
                Err(Mismatch::Question | Mismatch::NotResponse) => {
                    RecursorMetrics::inc(&m.mismatched_question)
                }
                Err(Mismatch::Case) => RecursorMetrics::inc(&m.mismatched_case),
            }
        };
        if !reply.metadata.truncation {
            return Ok(Exchange {
                message: reply,
                rtt: start.elapsed(),
                via_tcp: false,
            });
        }
        RecursorMetrics::inc(&m.tcp_fallbacks);
        let deadline = deadline.max(Instant::now() + MIN_TCP_BUDGET);
        let id = rng.next_u32() as u16;
        let query = encode_query(q, id, &wire_name)?;
        let reply = match timeout_at(deadline, self.tcp_exchange(q.server, &query)).await {
            Err(_) => {
                RecursorMetrics::inc(&m.upstream_timeouts);
                return Err(ExchangeError::Timeout);
            }
            Ok(r) => r?,
        };
        let Ok(reply) = Message::from_vec(&reply) else {
            RecursorMetrics::inc(&m.malformed_replies);
            return Err(ExchangeError::TcpFailed("malformed reply".into()));
        };
        if reply_matches(id, &wire_name, q.qtype, q.use_0x20, &reply).is_err() {
            return Err(ExchangeError::TcpFailed("reply mismatch".into()));
        }
        Ok(Exchange {
            message: reply,
            rtt: start.elapsed(),
            via_tcp: true,
        })
    }

    /// Sends one length-prefixed query and reads one length-prefixed reply.
    async fn tcp_exchange(
        &self,
        server: SocketAddr,
        query: &[u8],
    ) -> Result<Vec<u8>, ExchangeError> {
        let tcp_err = |e: std::io::Error| ExchangeError::TcpFailed(e.to_string());
        let mut stream = TcpStream::connect(server).await.map_err(tcp_err)?;
        let mut framed = Vec::with_capacity(query.len() + 2);
        framed.extend_from_slice(&(query.len() as u16).to_be_bytes());
        framed.extend_from_slice(query);
        stream.write_all(&framed).await.map_err(tcp_err)?;
        RecursorMetrics::inc(&self.metrics.upstream_queries);
        let len = stream.read_u16().await.map_err(tcp_err)?;
        let mut reply = vec![0u8; usize::from(len)];
        stream.read_exact(&mut reply).await.map_err(tcp_err)?;
        Ok(reply)
    }
}

fn net(e: std::io::Error) -> ExchangeError {
    ExchangeError::Network(e.kind())
}

fn encode_query(
    q: &OutboundQuery<'_>,
    id: u16,
    wire_name: &Name,
) -> Result<Vec<u8>, ExchangeError> {
    let mut msg = Message::new(id, MessageType::Query, OpCode::Query);
    msg.metadata.recursion_desired = q.recursion_desired;
    msg.metadata.checking_disabled = q.checking_disabled;
    msg.queries.push(Query::query(wire_name.clone(), q.qtype));
    if q.edns {
        let mut edns = Edns::new();
        edns.set_max_payload(EDNS_BUFFER);
        edns.set_dnssec_ok(q.dnssec_ok);
        msg.edns = Some(edns);
    }
    msg.to_vec()
        .map_err(|_| ExchangeError::Network(std::io::ErrorKind::InvalidInput))
}

/// A UDP socket on a random source port in 1024-65535 (chosen here, not by the kernel), falling
/// back to a kernel-chosen port after `PORT_ATTEMPTS` collisions.
async fn bind_random(server: SocketAddr, rng: &mut impl Rng) -> Result<UdpSocket, ExchangeError> {
    let ip = match server {
        SocketAddr::V4(_) => IpAddr::V4(Ipv4Addr::UNSPECIFIED),
        SocketAddr::V6(_) => IpAddr::V6(Ipv6Addr::UNSPECIFIED),
    };
    for _ in 0..PORT_ATTEMPTS {
        let port = 1024 + (rng.next_u32() % 64512) as u16;
        if let Ok(s) = UdpSocket::bind(SocketAddr::new(ip, port)).await {
            return Ok(s);
        }
    }
    UdpSocket::bind(SocketAddr::new(ip, 0)).await.map_err(net)
}

/// `name` with every ASCII letter's case chosen by one random bit (DNS 0x20).
pub fn randomise_case(name: &Name, rng: &mut impl Rng) -> Name {
    let mut bits = 0u32;
    let mut left = 0;
    let labels: Vec<Vec<u8>> = name
        .iter()
        .map(|label| {
            label
                .iter()
                .map(|&b| {
                    if !b.is_ascii_alphabetic() {
                        return b;
                    }
                    if left == 0 {
                        bits = rng.next_u32();
                        left = 32;
                    }
                    let upper = bits & 1 == 1;
                    bits >>= 1;
                    left -= 1;
                    if upper {
                        b.to_ascii_uppercase()
                    } else {
                        b.to_ascii_lowercase()
                    }
                })
                .collect()
        })
        .collect();
    // Labels taken from a valid name are valid; keep the original if that ever changes.
    let Ok(mut out) = Name::from_labels(labels) else {
        return name.clone();
    };
    out.set_fqdn(name.is_fqdn());
    out
}

/// Whether `reply` answers the query sent with `sent_id`, `sent_name` and `sent_type`; with
/// `strict_case` the question name must repeat the sent 0x20 casing exactly.
pub fn reply_matches(
    sent_id: u16,
    sent_name: &Name,
    sent_type: RecordType,
    strict_case: bool,
    reply: &Message,
) -> Result<(), Mismatch> {
    if reply.metadata.message_type != MessageType::Response {
        return Err(Mismatch::NotResponse);
    }
    if reply.metadata.id != sent_id {
        return Err(Mismatch::Id);
    }
    let [question] = reply.queries.as_slice() else {
        return Err(Mismatch::Question);
    };
    if question.query_type() != sent_type
        || question.query_class() != DNSClass::IN
        || question.name().to_lowercase() != sent_name.to_lowercase()
    {
        return Err(Mismatch::Question);
    }
    if strict_case && !question.name().eq_case(sent_name) {
        return Err(Mismatch::Case);
    }
    Ok(())
}

#[cfg(test)]
mod tests;
