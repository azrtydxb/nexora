//! NOTIFY (RFC 1996) to secondaries after a zone changed: one UDP message with the zone's SOA,
//! TSIG-signed when the target has a key, retried with doubling timeouts until acknowledged.

use crate::clock;
use crate::tsig::{self, TsigKey, TsigVerifier};
use rand::RngExt;
use std::net::{Ipv4Addr, Ipv6Addr, SocketAddr};
use std::sync::Arc;
use std::time::Duration;
use tokio::net::UdpSocket;
use tokio::time::{Instant, sleep_until, timeout_at};

const OPCODE_NOTIFY: u8 = 4;
const FLAG_AA: u8 = 0x04;
const T_SOA: u16 = 6;
const CLASS_IN: u16 = 1;

pub struct NotifyJob {
    /// Lowercase wire origin.
    pub zone: Box<[u8]>,
    /// The apex SOA RR: owner, type, class, TTL, RDLENGTH and RDATA, uncompressed.
    pub soa_rr: Vec<u8>,
    pub target: SocketAddr,
    pub key: Option<Arc<TsigKey>>,
}

#[derive(Debug, PartialEq, Eq)]
pub enum NotifyResult {
    Acked {
        attempts: u32,
    },
    Rejected(u8),
    Timeout,
    /// The target names a TSIG key the engine does not hold; nothing was sent.
    NoKey,
}

fn unix_now() -> u64 {
    clock::unix_now().max(0) as u64
}

/// The NOTIFY message for `job` and, when signed, its request MAC.
fn build(job: &NotifyJob) -> (Vec<u8>, Vec<u8>) {
    let id: u16 = rand::rng().random();
    let mut msg = Vec::with_capacity(64 + job.zone.len() + job.soa_rr.len());
    msg.extend_from_slice(&id.to_be_bytes());
    msg.push((OPCODE_NOTIFY << 3) | FLAG_AA);
    msg.push(0);
    msg.extend_from_slice(&[0, 1, 0, 1, 0, 0, 0, 0]); // QD 1, AN 1, NS 0, AR 0
    msg.extend_from_slice(&job.zone);
    msg.extend_from_slice(&T_SOA.to_be_bytes());
    msg.extend_from_slice(&CLASS_IN.to_be_bytes());
    msg.extend_from_slice(&job.soa_rr);
    let mac = match &job.key {
        Some(key) => tsig::sign_request(&mut msg, key, unix_now()),
        None => Vec::new(),
    };
    (msg, mac)
}

/// Sends `job.target` a NOTIFY and waits `first_timeout * 2^k` for the answer to attempt `k`,
/// for `attempts` attempts. Datagrams that are not the answer (other ID, not a NOTIFY
/// response, bad TSIG) are ignored.
pub async fn send_notify(job: NotifyJob, first_timeout: Duration, attempts: u32) -> NotifyResult {
    let local: SocketAddr = match job.target {
        SocketAddr::V4(_) => (Ipv4Addr::UNSPECIFIED, 0).into(),
        SocketAddr::V6(_) => (Ipv6Addr::UNSPECIFIED, 0).into(),
    };
    let Ok(sock) = UdpSocket::bind(local).await else {
        return NotifyResult::Timeout;
    };
    if sock.connect(job.target).await.is_err() {
        return NotifyResult::Timeout;
    }
    let (msg, request_mac) = build(&job);
    let id = [msg[0], msg[1]];
    let mut buf = [0u8; 4096];
    for k in 0..attempts {
        let deadline = Instant::now() + first_timeout * 2u32.saturating_pow(k);
        if sock.send(&msg).await.is_err() {
            sleep_until(deadline).await;
            continue;
        }
        loop {
            let n = match timeout_at(deadline, sock.recv(&mut buf)).await {
                Err(_) => break,
                Ok(Ok(n)) => n,
                // An ICMP error surfaces on the connected socket: wait out this attempt.
                Ok(Err(_)) => {
                    sleep_until(deadline).await;
                    break;
                }
            };
            let reply = &buf[..n];
            if n < 12
                || reply[..2] != id
                || reply[2] & 0x80 == 0
                || (reply[2] >> 3) & 0x0f != OPCODE_NOTIFY
            {
                continue;
            }
            if let Some(key) = &job.key {
                let mut v = TsigVerifier::new((**key).clone(), request_mac.clone());
                if v.verify(reply, unix_now()).is_err() {
                    continue;
                }
            }
            return match reply[3] & 0x0f {
                0 => NotifyResult::Acked { attempts: k + 1 },
                rcode => NotifyResult::Rejected(rcode),
            };
        }
    }
    NotifyResult::Timeout
}
