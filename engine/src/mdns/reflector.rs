//! Multicast mDNS reflection between interfaces.

use super::iface::IfaceTarget;
use socket2::{Domain, Socket};
use std::collections::{BTreeMap, VecDeque};
use std::hash::{BuildHasher, RandomState};
use std::net::{IpAddr, Ipv4Addr, Ipv6Addr, SocketAddr, SocketAddrV4, SocketAddrV6};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};
use tokio::net::UdpSocket;
use tokio::task::JoinHandle;

/// A payload seen on any interface within this window is not reflected again.
pub const DEDUPE_WINDOW: Duration = Duration::from_secs(1);
/// Most digests remembered; the oldest is forgotten first.
pub const DEDUPE_ENTRIES: usize = 1024;
const MDNS_PORT: u16 = 5353;
const GROUP_V4: Ipv4Addr = Ipv4Addr::new(224, 0, 0, 251);
const GROUP_V6: Ipv6Addr = Ipv6Addr::new(0xff02, 0, 0, 0, 0, 0, 0, 0xfb);
const ERROR_LOG_EVERY: Duration = Duration::from_secs(60);

/// Recently reflected payload digests, so echoes between reflecting hosts die out.
pub struct Dedupe {
    hasher: RandomState,
    seen: VecDeque<(u64, Instant)>,
}

impl Default for Dedupe {
    fn default() -> Self {
        Dedupe::new()
    }
}

impl Dedupe {
    pub fn new() -> Dedupe {
        Dedupe {
            hasher: RandomState::new(),
            seen: VecDeque::with_capacity(DEDUPE_ENTRIES),
        }
    }

    /// True when `payload` was not seen within the window; it is then recorded.
    pub fn admit(&mut self, payload: &[u8], now: Instant) -> bool {
        while let Some(&(_, at)) = self.seen.front() {
            if now.saturating_duration_since(at) < DEDUPE_WINDOW {
                break;
            }
            self.seen.pop_front();
        }
        let digest = self.hasher.hash_one(payload);
        if self.seen.iter().any(|&(d, _)| d == digest) {
            return false;
        }
        if self.seen.len() == DEDUPE_ENTRIES {
            self.seen.pop_front();
        }
        self.seen.push_back((digest, now));
        true
    }

    pub fn len(&self) -> usize {
        self.seen.len()
    }

    pub fn is_empty(&self) -> bool {
        self.seen.is_empty()
    }
}

/// True when `src` is one of the reflecting interfaces' own addresses.
pub fn from_local(src: IpAddr, ifaces: &[IfaceTarget]) -> bool {
    ifaces.iter().any(|i| match src {
        IpAddr::V4(a) => i.v4 == Some(a),
        IpAddr::V6(a) => i.v6_link_local == Some(a),
    })
}

/// `nexora_mdns_reflected_packets_total{from,to}`, touched only by reflector tasks.
pub struct ReflectCounters {
    by_pair: Mutex<BTreeMap<(String, String), u64>>,
}

pub static REFLECTED: ReflectCounters = ReflectCounters {
    by_pair: Mutex::new(BTreeMap::new()),
};

impl ReflectCounters {
    fn count(&self, from: &str, to: &str) {
        let mut m = self.by_pair.lock().unwrap_or_else(|e| e.into_inner());
        *m.entry((from.to_owned(), to.to_owned())).or_default() += 1;
    }

    /// Appends the counter family in OpenMetrics text form.
    pub fn render(&self, out: &mut String) {
        use std::fmt::Write as _;
        out.push_str(
            "# HELP nexora_mdns_reflected_packets mDNS packets reflected between interfaces.\n",
        );
        out.push_str("# TYPE nexora_mdns_reflected_packets counter\n");
        let m = self.by_pair.lock().unwrap_or_else(|e| e.into_inner());
        for ((from, to), v) in m.iter() {
            let _ = writeln!(
                out,
                "nexora_mdns_reflected_packets_total{{from=\"{}\",to=\"{}\"}} {v}",
                label(from),
                label(to)
            );
        }
    }
}

fn label(v: &str) -> String {
    v.replace('\\', "\\\\").replace('"', "\\\"")
}

/// One interface's socket in one address family.
struct Leg {
    name: String,
    sock: UdpSocket,
    group: SocketAddr,
    last_error: Mutex<Option<Instant>>,
}

impl Leg {
    fn log_error(&self, what: &str, e: &std::io::Error) {
        let now = Instant::now();
        let mut last = self.last_error.lock().unwrap_or_else(|e| e.into_inner());
        if last.is_some_and(|at| now.duration_since(at) < ERROR_LOG_EVERY) {
            return;
        }
        *last = Some(now);
        crate::log_warn!("mdns reflector {what} on {}: {e}", self.name);
    }
}

/// Running reflection tasks; dropping it without `stop` leaves them running.
pub struct Reflector {
    tasks: Vec<JoinHandle<()>>,
}

impl Reflector {
    /// Opens one socket per interface and family with an address, then reflects every
    /// family that has at least two interfaces. Tasks run on `rt` (the control runtime).
    pub fn start(
        rt: &tokio::runtime::Handle,
        ifaces: Vec<IfaceTarget>,
    ) -> std::io::Result<Reflector> {
        let _enter = rt.enter();
        let v4: Vec<&IfaceTarget> = ifaces.iter().filter(|i| i.v4.is_some()).collect();
        let v6: Vec<&IfaceTarget> = ifaces
            .iter()
            .filter(|i| i.v6_link_local.is_some())
            .collect();
        let mut families: Vec<Vec<Arc<Leg>>> = Vec::with_capacity(2);
        if v4.len() >= 2 {
            families.push(
                v4.iter()
                    .map(|i| open_v4(i))
                    .collect::<std::io::Result<_>>()?,
            );
        }
        if v6.len() >= 2 {
            families.push(
                v6.iter()
                    .map(|i| open_v6(i))
                    .collect::<std::io::Result<_>>()?,
            );
        }
        let ifaces: Arc<[IfaceTarget]> = ifaces.into();
        let mut tasks = Vec::new();
        for legs in families {
            let legs: Arc<[Arc<Leg>]> = legs.into();
            let dedupe = Arc::new(Mutex::new(Dedupe::new()));
            for from in 0..legs.len() {
                tasks.push(rt.spawn(reflect(from, legs.clone(), dedupe.clone(), ifaces.clone())));
            }
        }
        Ok(Reflector { tasks })
    }

    pub fn stop(self) {
        for t in self.tasks {
            t.abort();
        }
    }
}

/// Receives on `legs[from]` and resends each admitted datagram, unchanged, to the group
/// on every other leg. The socket is bound to port 5353, so every datagram it receives
/// was addressed to the mDNS port.
async fn reflect(
    from: usize,
    legs: Arc<[Arc<Leg>]>,
    dedupe: Arc<Mutex<Dedupe>>,
    ifaces: Arc<[IfaceTarget]>,
) {
    let leg = &legs[from];
    let mut buf = vec![0u8; 9000];
    loop {
        let (n, src) = match leg.sock.recv_from(&mut buf).await {
            Ok(r) => r,
            Err(e) => {
                leg.log_error("receive", &e);
                tokio::time::sleep(Duration::from_millis(100)).await;
                continue;
            }
        };
        let payload = &buf[..n];
        if from_local(src.ip(), &ifaces) {
            continue;
        }
        let admitted = dedupe
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .admit(payload, Instant::now());
        if !admitted {
            continue;
        }
        for (i, to) in legs.iter().enumerate() {
            if i == from {
                continue;
            }
            match to.sock.send_to(payload, to.group).await {
                Ok(_) => REFLECTED.count(&leg.name, &to.name),
                Err(e) => to.log_error("send", &e),
            }
        }
    }
}

#[cfg(target_os = "linux")]
fn socket(domain: Domain, name: &str) -> std::io::Result<Socket> {
    let sock = Socket::new(domain, socket2::Type::DGRAM, Some(socket2::Protocol::UDP))?;
    sock.set_reuse_address(true)?;
    sock.set_reuse_port(true)?;
    sock.bind_device(Some(name.as_bytes()))?;
    sock.set_nonblocking(true)?;
    Ok(sock)
}

// SO_BINDTODEVICE is Linux-only: elsewhere a leg cannot be pinned to its interface.
#[cfg(not(target_os = "linux"))]
fn socket(_domain: Domain, name: &str) -> std::io::Result<Socket> {
    Err(std::io::Error::new(
        std::io::ErrorKind::Unsupported,
        format!("the mDNS reflector on {name} needs Linux"),
    ))
}

fn open_v4(i: &IfaceTarget) -> std::io::Result<Arc<Leg>> {
    let addr = i.v4.expect("filtered to interfaces with IPv4");
    let sock = socket(Domain::IPV4, &i.name)?;
    sock.bind(&SocketAddr::V4(SocketAddrV4::new(Ipv4Addr::UNSPECIFIED, MDNS_PORT)).into())?;
    sock.join_multicast_v4(&GROUP_V4, &addr)?;
    sock.set_multicast_loop_v4(false)?;
    sock.set_multicast_ttl_v4(255)?;
    sock.set_ttl_v4(255)?;
    sock.set_multicast_if_v4(&addr)?;
    leg(
        i,
        sock,
        SocketAddr::V4(SocketAddrV4::new(GROUP_V4, MDNS_PORT)),
    )
}

fn open_v6(i: &IfaceTarget) -> std::io::Result<Arc<Leg>> {
    let sock = socket(Domain::IPV6, &i.name)?;
    sock.set_only_v6(true)?;
    sock.bind(&SocketAddr::V6(SocketAddrV6::new(Ipv6Addr::UNSPECIFIED, MDNS_PORT, 0, 0)).into())?;
    sock.join_multicast_v6(&GROUP_V6, i.index)?;
    sock.set_multicast_loop_v6(false)?;
    sock.set_multicast_hops_v6(255)?;
    sock.set_unicast_hops_v6(255)?;
    sock.set_multicast_if_v6(i.index)?;
    leg(
        i,
        sock,
        SocketAddr::V6(SocketAddrV6::new(GROUP_V6, MDNS_PORT, 0, i.index)),
    )
}

fn leg(i: &IfaceTarget, sock: Socket, group: SocketAddr) -> std::io::Result<Arc<Leg>> {
    Ok(Arc::new(Leg {
        name: i.name.clone(),
        sock: UdpSocket::from_std(sock.into())?,
        group,
        last_error: Mutex::new(None),
    }))
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::mdns::iface::IfaceTarget;
    use std::time::{Duration, Instant};

    #[test]
    fn dedupe_and_source_filter() {
        let mut d = Dedupe::new();
        let t0 = Instant::now();
        assert!(d.admit(b"packet-a", t0));
        assert!(
            !d.admit(b"packet-a", t0 + Duration::from_millis(900)),
            "an echo inside the window is dropped"
        );
        assert!(d.admit(b"packet-b", t0 + Duration::from_millis(900)));
        assert!(
            d.admit(b"packet-a", t0 + Duration::from_millis(2100)),
            "after the window the payload passes again"
        );
        for i in 0..(DEDUPE_ENTRIES * 2) {
            assert!(d.admit(format!("flood-{i}").as_bytes(), t0 + Duration::from_secs(3)));
        }
        assert!(d.len() <= DEDUPE_ENTRIES, "the table stays bounded");
        let ifaces = vec![
            IfaceTarget {
                name: "gwA".into(),
                index: 5,
                v4: Some("10.254.1.1".parse().unwrap()),
                v6_link_local: Some("fe80::1".parse().unwrap()),
            },
            IfaceTarget {
                name: "gwB".into(),
                index: 6,
                v4: Some("10.254.2.1".parse().unwrap()),
                v6_link_local: None,
            },
        ];
        assert!(from_local("10.254.2.1".parse().unwrap(), &ifaces));
        assert!(from_local("fe80::1".parse().unwrap(), &ifaces));
        assert!(!from_local("10.254.1.2".parse().unwrap(), &ifaces));
    }
}
