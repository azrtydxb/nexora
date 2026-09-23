//! Interface lookup by name for multicast sockets.

use std::net::{Ipv4Addr, Ipv6Addr};

/// The addresses a multicast socket on one interface binds to.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct IfaceTarget {
    pub name: String,
    pub index: u32,
    pub v4: Option<Ipv4Addr>,
    pub v6_link_local: Option<Ipv6Addr>,
}

/// The interface `name` with its first IPv4 address and first IPv6 link-local
/// (`fe80::/10`) address; `None` when it does not exist or has neither address.
pub fn lookup(name: &str) -> Option<IfaceTarget> {
    let index = nix::net::if_::if_nametoindex(name).ok()?;
    let mut v4 = None;
    let mut v6_link_local = None;
    for ifa in nix::ifaddrs::getifaddrs().ok()? {
        if ifa.interface_name != name {
            continue;
        }
        let Some(addr) = ifa.address else { continue };
        if let Some(sin) = addr.as_sockaddr_in() {
            v4.get_or_insert(sin.ip());
        } else if let Some(sin6) = addr.as_sockaddr_in6() {
            let ip = sin6.ip();
            if ip.segments()[0] & 0xffc0 == 0xfe80 {
                v6_link_local.get_or_insert(ip);
            }
        }
    }
    if v4.is_none() && v6_link_local.is_none() {
        return None;
    }
    Some(IfaceTarget {
        name: name.to_owned(),
        index,
        v4,
        v6_link_local,
    })
}

#[cfg(test)]
mod tests {
    #[test]
    fn loopback_is_found_and_unknown_is_not() {
        let lo = super::lookup("lo").expect("lo exists");
        assert!(lo.index > 0);
        assert_eq!(lo.v4, Some(std::net::Ipv4Addr::LOCALHOST));
        assert!(super::lookup("nxnope0").is_none());
    }
}
