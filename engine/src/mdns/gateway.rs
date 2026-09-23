//! One-shot multicast queries answering unicast DNS clients (RFC 6762 §5.1, §6.7).

use super::iface::IfaceTarget;
use hickory_proto::op::{Message, MessageType, OpCode, Query};
use hickory_proto::rr::{DNSClass, Name, Record, RecordType};
use rand::RngExt;
use socket2::{Domain, Protocol, Socket, Type};
use std::future::poll_fn;
use std::net::{Ipv4Addr, Ipv6Addr, SocketAddr, SocketAddrV4, SocketAddrV6};
use std::sync::atomic::{AtomicU64, AtomicUsize, Ordering};
use std::task::Poll;
use std::time::Duration;
use tokio::io::ReadBuf;
use tokio::net::UdpSocket;
use tokio::time::Instant;

/// Most gateway queries running at once; beyond it a query is `Busy`.
pub const MAX_INFLIGHT: usize = 64;
/// Answers are cached and served for at most this many seconds.
pub const TTL_CAP: u32 = 10;
pub const GROUP_V4: SocketAddr =
    SocketAddr::V4(SocketAddrV4::new(Ipv4Addr::new(224, 0, 0, 251), MDNS_PORT));
pub const GROUP_V6: Ipv6Addr = Ipv6Addr::new(0xff02, 0, 0, 0, 0, 0, 0, 0xfb);
const MDNS_PORT: u16 = 5353;

/// One multicast destination: the local address to send from, the group, and the interface.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Target {
    pub bind: SocketAddr,
    pub group: SocketAddr,
    pub if_index: u32,
}

impl Target {
    /// One target per address family the interface has an address in.
    pub fn for_iface(i: &IfaceTarget) -> Vec<Target> {
        let mut out = Vec::with_capacity(2);
        if let Some(v4) = i.v4 {
            out.push(Target {
                bind: SocketAddr::new(v4.into(), 0),
                group: GROUP_V4,
                if_index: i.index,
            });
        }
        if let Some(v6) = i.v6_link_local {
            out.push(Target {
                bind: SocketAddr::V6(SocketAddrV6::new(v6, 0, 0, i.index)),
                group: SocketAddr::V6(SocketAddrV6::new(GROUP_V6, MDNS_PORT, 0, i.index)),
                if_index: i.index,
            });
        }
        out
    }
}

pub enum Outcome {
    Answers(Vec<Record>),
    NoAnswer,
    Busy,
}

/// Types a single responder owns (RFC 6762 §6): the first answer is the answer.
pub fn unique_type(t: RecordType) -> bool {
    matches!(
        t,
        RecordType::A | RecordType::AAAA | RecordType::SRV | RecordType::TXT
    )
}

pub struct Counters {
    pub answered: AtomicU64,
    pub unanswered: AtomicU64,
    pub dropped: AtomicU64,
}

pub static COUNTERS: Counters = Counters {
    answered: AtomicU64::new(0),
    unanswered: AtomicU64::new(0),
    dropped: AtomicU64::new(0),
};

static INFLIGHT: AtomicUsize = AtomicUsize::new(0);

/// A slot of the process-wide in-flight cap, released on drop.
pub(crate) struct InflightGuard(());

impl InflightGuard {
    pub(crate) fn try_acquire() -> Option<InflightGuard> {
        INFLIGHT
            .fetch_update(Ordering::AcqRel, Ordering::Acquire, |n| {
                (n < MAX_INFLIGHT).then_some(n + 1)
            })
            .ok()
            .map(|_| InflightGuard(()))
    }
}

impl Drop for InflightGuard {
    fn drop(&mut self) {
        INFLIGHT.fetch_sub(1, Ordering::AcqRel);
    }
}

pub struct Gateway {
    targets: Vec<Target>,
    timeout: Duration,
}

impl Gateway {
    pub fn new(targets: Vec<Target>, timeout: Duration) -> Gateway {
        Gateway { targets, timeout }
    }

    /// Asks every target's group once (legacy unicast query from an ephemeral port,
    /// RFC 6762 §6.7) and collects matching answers until the timeout, or until the
    /// first answer for a unique type.
    pub async fn query(&self, qname: &Name, qtype: RecordType) -> Outcome {
        let Some(_slot) = InflightGuard::try_acquire() else {
            COUNTERS.dropped.fetch_add(1, Ordering::Relaxed);
            return Outcome::Busy;
        };
        let answers = self.collect(qname, qtype).await;
        if answers.is_empty() {
            COUNTERS.unanswered.fetch_add(1, Ordering::Relaxed);
            Outcome::NoAnswer
        } else {
            COUNTERS.answered.fetch_add(1, Ordering::Relaxed);
            Outcome::Answers(answers)
        }
    }

    async fn collect(&self, qname: &Name, qtype: RecordType) -> Vec<Record> {
        let deadline = Instant::now() + self.timeout;
        let id: u16 = rand::rng().random();
        let mut msg = Message::new(id, MessageType::Query, OpCode::Query);
        msg.add_query(Query::query(qname.clone(), qtype));
        let Ok(query) = msg.to_vec() else {
            return Vec::new();
        };
        let question = &query[12..];

        let mut sockets = Vec::with_capacity(self.targets.len());
        for t in &self.targets {
            let Ok(sock) = open(t) else { continue };
            if sock.send_to(&query, t.group).await.is_ok() {
                sockets.push(sock);
            }
        }
        let mut answers: Vec<Record> = Vec::new();
        if sockets.is_empty() {
            return answers;
        }
        let mut buf = [0u8; 9000];
        loop {
            let recv = poll_fn(|cx| {
                for s in &sockets {
                    let mut rb = ReadBuf::new(&mut buf);
                    if let Poll::Ready(r) = s.poll_recv_from(cx, &mut rb) {
                        return Poll::Ready(r.map(|_| rb.filled().len()));
                    }
                }
                Poll::Pending
            });
            let n = match tokio::time::timeout_at(deadline, recv).await {
                Err(_) => return answers,
                Ok(Err(_)) => continue,
                Ok(Ok(n)) => n,
            };
            let datagram = &buf[..n];
            if !matches_query(datagram, id, question) {
                continue;
            }
            let Ok(reply) = Message::from_vec(datagram) else {
                continue;
            };
            for mut rr in reply.answers {
                if rr.record_type() != qtype || !rr.name.eq_ignore_root(qname) {
                    continue;
                }
                rr.dns_class = DNSClass::from(u16::from(rr.dns_class) & 0x7fff);
                rr.ttl = rr.ttl.min(TTL_CAP);
                if !answers.iter().any(|a| a.data == rr.data) {
                    answers.push(rr);
                }
            }
            if !answers.is_empty() && unique_type(qtype) {
                return answers;
            }
        }
    }
}

/// A multicast sender on the target's interface; replies arrive on the same socket.
fn open(t: &Target) -> std::io::Result<UdpSocket> {
    let sock = Socket::new(
        Domain::for_address(t.bind),
        Type::DGRAM,
        Some(Protocol::UDP),
    )?;
    match t.bind {
        SocketAddr::V4(v4) => {
            sock.set_multicast_if_v4(v4.ip())?;
            sock.set_multicast_ttl_v4(255)?;
            sock.set_multicast_loop_v4(true)?;
        }
        SocketAddr::V6(_) => {
            sock.set_only_v6(true)?;
            sock.set_multicast_if_v6(t.if_index)?;
            sock.set_multicast_hops_v6(255)?;
            sock.set_multicast_loop_v6(true)?;
        }
    }
    sock.set_nonblocking(true)?;
    sock.bind(&t.bind.into())?;
    UdpSocket::from_std(sock.into())
}

/// A response to our query: QR set, our ID, one question equal to ours
/// (name case-insensitive, cache-flush/QU bit of the class ignored).
fn matches_query(m: &[u8], id: u16, question: &[u8]) -> bool {
    if m.len() < 12 + question.len()
        || m[2] & 0x80 == 0
        || m[0..2] != id.to_be_bytes()
        || m[4..6] != [0, 1]
    {
        return false;
    }
    let (name, tail) = question.split_at(question.len() - 4);
    let got = &m[12..12 + question.len()];
    got[..name.len()].eq_ignore_ascii_case(name)
        && got[name.len()..name.len() + 2] == tail[..2]
        && got[name.len() + 2] & 0x7f == tail[2] & 0x7f
        && got[name.len() + 3] == tail[3]
}

#[cfg(test)]
mod tests {
    use super::*;
    use hickory_proto::rr::{Name, RData, RecordType};
    use std::net::SocketAddr;
    use std::time::{Duration, Instant};
    use tokio::net::UdpSocket;

    /// The in-flight cap is process-wide: tests that query run one at a time, or
    /// `inflight_cap_gives_busy` holding every slot would starve the others.
    static SERIAL: tokio::sync::Mutex<()> = tokio::sync::Mutex::const_new(());

    /// `(name wire, type, class, ttl, rdata)`
    type WireAnswer = (Vec<u8>, u16, u16, u32, Vec<u8>);

    /// Wire reply: header with `id`, the question of `query`, and one answer per `WireAnswer`.
    fn reply(id: u16, query: &[u8], answers: &[WireAnswer]) -> Vec<u8> {
        let qend = 12 + query[12..].iter().position(|&b| b == 0).unwrap() + 1 + 4;
        let mut m = vec![
            (id >> 8) as u8,
            id as u8,
            0x84,
            0x00,
            0,
            1,
            0,
            answers.len() as u8,
            0,
            0,
            0,
            0,
        ];
        m.extend_from_slice(&query[12..qend]);
        for (name, t, class, ttl, rd) in answers {
            m.extend_from_slice(name);
            m.extend_from_slice(&t.to_be_bytes());
            m.extend_from_slice(&class.to_be_bytes());
            m.extend_from_slice(&ttl.to_be_bytes());
            m.extend_from_slice(&(rd.len() as u16).to_be_bytes());
            m.extend_from_slice(rd);
        }
        m
    }

    fn wire(name: &str) -> Vec<u8> {
        let mut out = Vec::new();
        for l in name.trim_end_matches('.').split('.') {
            out.push(l.len() as u8);
            out.extend_from_slice(l.as_bytes());
        }
        out.push(0);
        out
    }

    /// A unicast stand-in for the multicast group: `script(query)` gives the datagrams to send back, with delays.
    async fn responder<F>(script: F) -> SocketAddr
    where
        F: Fn(&[u8]) -> Vec<(u64, Vec<u8>)> + Send + 'static,
    {
        let sock = UdpSocket::bind("127.0.0.1:0").await.unwrap();
        let addr = sock.local_addr().unwrap();
        tokio::spawn(async move {
            let mut buf = [0u8; 1500];
            loop {
                let (n, from) = sock.recv_from(&mut buf).await.unwrap();
                for (delay_ms, d) in script(&buf[..n]) {
                    tokio::time::sleep(Duration::from_millis(delay_ms)).await;
                    sock.send_to(&d, from).await.unwrap();
                }
            }
        });
        addr
    }

    fn gateway(group: SocketAddr, timeout_ms: u64) -> Gateway {
        Gateway::new(
            vec![Target {
                bind: "127.0.0.1:0".parse().unwrap(),
                group,
                if_index: 0,
            }],
            Duration::from_millis(timeout_ms),
        )
    }

    #[tokio::test(flavor = "current_thread")]
    async fn reply_checks_and_ttl_cap() {
        let _serial = SERIAL.lock().await;
        let group = responder(|q| {
            let id = u16::from_be_bytes([q[0], q[1]]);
            let name = wire("printer.local.");
            let other = wire("scanner.local.");
            vec![
                (
                    0,
                    reply(
                        id ^ 1,
                        q,
                        &[(name.clone(), 1, 1, 120, vec![10, 254, 0, 66])],
                    ),
                ), // wrong ID
                (0, reply(id, q, &[(other, 1, 1, 120, vec![10, 254, 0, 77])])), // other owner
                (
                    0,
                    reply(id, q, &[(name, 1, 0x8001, 120, vec![10, 254, 0, 9])]),
                ), // cache-flush class
            ]
        })
        .await;
        let out = gateway(group, 500)
            .query(&Name::from_ascii("printer.local.").unwrap(), RecordType::A)
            .await;
        let Outcome::Answers(rrs) = out else {
            panic!("no answers")
        };
        assert_eq!(
            rrs.len(),
            1,
            "only the matching owner and ID count: {rrs:?}"
        );
        assert_eq!(rrs[0].ttl, TTL_CAP, "TTL capped at 10");
        assert_eq!(u16::from(rrs[0].dns_class), 1, "cache-flush bit cleared");
        assert!(
            matches!(&rrs[0].data, RData::A(a) if a.0 == std::net::Ipv4Addr::new(10, 254, 0, 9))
        );
    }

    #[tokio::test(flavor = "current_thread")]
    async fn first_answer_ends_unique_types() {
        let _serial = SERIAL.lock().await;
        let group = responder(|q| {
            let id = u16::from_be_bytes([q[0], q[1]]);
            let qtype = u16::from_be_bytes([q[q.len() - 4], q[q.len() - 3]]);
            if qtype == 1 {
                return vec![(
                    0,
                    reply(
                        id,
                        q,
                        &[(wire("printer.local."), 1, 1, 120, vec![10, 254, 0, 9])],
                    ),
                )];
            }
            if qtype != 12 {
                return vec![];
            }
            let owner = wire("_ipp._tcp.local.");
            let inst = |n: &str| wire(&format!("{n}._ipp._tcp.local."));
            vec![
                (
                    20,
                    reply(id, q, &[(owner.clone(), 12, 1, 4500, inst("one"))]),
                ),
                (
                    50,
                    reply(
                        id,
                        q,
                        &[
                            (owner.clone(), 12, 1, 4500, inst("two")),
                            (owner.clone(), 12, 1, 4500, inst("one")),
                        ],
                    ),
                ),
                (50, reply(id, q, &[(owner, 12, 1, 4500, inst("three"))])),
            ]
        })
        .await;
        let gw = gateway(group, 400);
        let started = Instant::now();
        assert!(matches!(
            gw.query(&Name::from_ascii("printer.local.").unwrap(), RecordType::A)
                .await,
            Outcome::Answers(_)
        ));
        assert!(
            started.elapsed() < Duration::from_millis(200),
            "a unique type returns on the first answer"
        );
        let started = Instant::now();
        let Outcome::Answers(ptrs) = gw
            .query(
                &Name::from_ascii("_ipp._tcp.local.").unwrap(),
                RecordType::PTR,
            )
            .await
        else {
            panic!("no PTR answers")
        };
        assert!(
            started.elapsed() >= Duration::from_millis(400),
            "PTR waits for the whole window"
        );
        assert_eq!(
            ptrs.len(),
            3,
            "three instances, duplicates dropped: {ptrs:?}"
        );
        let started = Instant::now();
        assert!(matches!(
            gw.query(
                &Name::from_ascii("nothere.local.").unwrap(),
                RecordType::TXT
            )
            .await,
            Outcome::NoAnswer
        ));
        assert!(started.elapsed() >= Duration::from_millis(400));
    }

    #[tokio::test(flavor = "current_thread")]
    async fn inflight_cap_gives_busy() {
        let _serial = SERIAL.lock().await;
        let group = responder(|_| vec![]).await;
        let gw = gateway(group, 200);
        let held: Vec<_> = (0..MAX_INFLIGHT)
            .map(|_| InflightGuard::try_acquire().unwrap())
            .collect();
        let before = COUNTERS.dropped.load(std::sync::atomic::Ordering::Relaxed);
        assert!(matches!(
            gw.query(&Name::from_ascii("printer.local.").unwrap(), RecordType::A)
                .await,
            Outcome::Busy
        ));
        assert_eq!(
            COUNTERS.dropped.load(std::sync::atomic::Ordering::Relaxed),
            before + 1
        );
        drop(held);
        assert!(matches!(
            gw.query(&Name::from_ascii("printer.local.").unwrap(), RecordType::A)
                .await,
            Outcome::NoAnswer
        ));
    }
}
