//! Client allow list: queries from addresses outside every CIDR are REFUSED.

use ipnet::{IpNet, Ipv4Net, Ipv6Net};
use std::net::IpAddr;

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Acl {
    v4: Vec<Ipv4Net>,
    v6: Vec<Ipv6Net>,
    /// Both zero-length prefixes are present: every client matches.
    all: bool,
}

impl Acl {
    /// Every client.
    pub fn any() -> Acl {
        Acl {
            v4: vec![Ipv4Net::default()],
            v6: vec![Ipv6Net::default()],
            all: true,
        }
    }

    pub fn parse(cidrs: &[String]) -> Result<Acl, String> {
        let mut acl = Acl {
            v4: Vec::new(),
            v6: Vec::new(),
            all: false,
        };
        for c in cidrs {
            match c.parse::<IpNet>() {
                Ok(IpNet::V4(n)) => acl.v4.push(n.trunc()),
                Ok(IpNet::V6(n)) => acl.v6.push(n.trunc()),
                Err(e) => return Err(format!("invalid cidr {c}: {e}")),
            }
        }
        acl.all = acl.v4.iter().any(|n| n.prefix_len() == 0)
            && acl.v6.iter().any(|n| n.prefix_len() == 0);
        Ok(acl)
    }

    /// Contains 0.0.0.0/0 and ::/0.
    pub fn allows_all(&self) -> bool {
        self.all
    }

    /// Linear scan: M1 lists are short (the management plane seeds 8 CIDRs).
    /// debt: switch to a prefix trie if operators configure hundreds of CIDRs.
    pub fn allows(&self, ip: IpAddr) -> bool {
        match ip {
            IpAddr::V4(a) => self.v4.iter().any(|n| n.contains(&a)),
            IpAddr::V6(a) => match a.to_ipv4_mapped() {
                Some(v4) => self.v4.iter().any(|n| n.contains(&v4)),
                None => self.v6.iter().any(|n| n.contains(&a)),
            },
        }
    }
}
