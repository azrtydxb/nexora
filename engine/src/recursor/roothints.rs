//! Root server hints: the compiled-in IANA `named.root` set, or the snapshot's override.

use crate::proto;
use hickory_proto::rr::Name;
use std::net::IpAddr;

/// IANA `named.root` (2024): name, IPv4, IPv6.
const IANA: [(&str, &str, &str); 13] = [
    ("a.root-servers.net.", "198.41.0.4", "2001:503:ba3e::2:30"),
    ("b.root-servers.net.", "170.247.170.2", "2801:1b8:10::b"),
    ("c.root-servers.net.", "192.33.4.12", "2001:500:2::c"),
    ("d.root-servers.net.", "199.7.91.13", "2001:500:2d::d"),
    ("e.root-servers.net.", "192.203.230.10", "2001:500:a8::e"),
    ("f.root-servers.net.", "192.5.5.241", "2001:500:2f::f"),
    ("g.root-servers.net.", "192.112.36.4", "2001:500:12::d0d"),
    ("h.root-servers.net.", "198.97.190.53", "2001:500:1::53"),
    ("i.root-servers.net.", "192.36.148.17", "2001:7fe::53"),
    ("j.root-servers.net.", "192.58.128.30", "2001:503:c27::2:30"),
    ("k.root-servers.net.", "193.0.14.129", "2001:7fd::1"),
    ("l.root-servers.net.", "199.7.83.42", "2001:500:9f::42"),
    ("m.root-servers.net.", "202.12.27.33", "2001:dc3::35"),
];

#[derive(Clone, Debug)]
pub struct RootHints {
    pub servers: Vec<(Name, Vec<IpAddr>)>,
}

impl RootHints {
    pub fn iana() -> Self {
        let servers = IANA
            .iter()
            .map(|(name, v4, v6)| {
                let name = Name::from_ascii(name).expect("valid root server name");
                let addrs = vec![
                    v4.parse().expect("valid root IPv4"),
                    v6.parse().expect("valid root IPv6"),
                ];
                (name, addrs)
            })
            .collect();
        Self { servers }
    }

    /// The configured hints; an empty list means the IANA set. Snapshot validation has already
    /// rejected bad names and addresses, so unparseable entries are skipped here.
    pub fn from_config(hints: &[proto::RootHint]) -> Self {
        let servers: Vec<(Name, Vec<IpAddr>)> = hints
            .iter()
            .filter_map(|h| {
                let name = Name::from_ascii(&h.name).ok()?;
                let addrs = h.addresses.iter().filter_map(|a| a.parse().ok()).collect();
                Some((name, addrs))
            })
            .collect();
        if servers.is_empty() {
            return Self::iana();
        }
        Self { servers }
    }

    /// Every IPv4 address, plus the IPv6 addresses when `ipv6` is true.
    pub fn addresses(&self, ipv6: bool) -> Vec<IpAddr> {
        self.servers
            .iter()
            .flat_map(|(_, addrs)| addrs.iter().copied())
            .filter(|a| a.is_ipv4() || ipv6)
            .collect()
    }
}
