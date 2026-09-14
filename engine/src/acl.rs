//! Client allow list: queries from addresses outside every CIDR are REFUSED.

use ipnet::IpNet;
use std::net::IpAddr;

pub struct Acl {
    /// Sorted, non-overlapping, non-adjacent inclusive ranges.
    v4: Vec<(u32, u32)>,
    v6: Vec<(u128, u128)>,
}

/// Sorts ranges and merges overlapping or adjacent ones. `succ` is `checked_add(1)`,
/// so a range ending at the family's last address absorbs everything after it.
fn merged<T: Copy + Ord>(mut r: Vec<(T, T)>, succ: impl Fn(T) -> Option<T>) -> Vec<(T, T)> {
    r.sort_unstable();
    let mut out: Vec<(T, T)> = Vec::with_capacity(r.len());
    for (s, e) in r {
        match out.last_mut() {
            Some(last) if succ(last.1).is_none_or(|n| s <= n) => last.1 = last.1.max(e),
            _ => out.push((s, e)),
        }
    }
    out
}

fn covered<T: Copy + Ord>(r: &[(T, T)], a: T) -> bool {
    let i = r.partition_point(|&(s, _)| s <= a);
    i > 0 && a <= r[i - 1].1
}

impl Acl {
    pub fn parse(cidrs: &[String]) -> Result<Acl, String> {
        let mut v4 = Vec::new();
        let mut v6 = Vec::new();
        for c in cidrs {
            match c.parse::<IpNet>() {
                Ok(IpNet::V4(n)) => {
                    let n = n.trunc();
                    v4.push((u32::from(n.network()), u32::from(n.broadcast())));
                }
                Ok(IpNet::V6(n)) => {
                    let n = n.trunc();
                    v6.push((u128::from(n.network()), u128::from(n.broadcast())));
                }
                Err(e) => return Err(format!("invalid cidr {c}: {e}")),
            }
        }
        Ok(Acl {
            v4: merged(v4, |x| x.checked_add(1)),
            v6: merged(v6, |x| x.checked_add(1)),
        })
    }

    pub fn v4_ranges(&self) -> usize {
        self.v4.len()
    }

    pub fn v6_ranges(&self) -> usize {
        self.v6.len()
    }

    /// Binary search over merged ranges: O(log n), allocation-free.
    pub fn allows(&self, ip: IpAddr) -> bool {
        match ip {
            IpAddr::V4(a) => covered(&self.v4, u32::from(a)),
            IpAddr::V6(a) => match a.to_ipv4_mapped() {
                Some(v4) => covered(&self.v4, u32::from(v4)),
                None => covered(&self.v6, u128::from(a)),
            },
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn next(seed: &mut u64) -> u64 {
        *seed = seed
            .wrapping_mul(6364136223846793005)
            .wrapping_add(1442695040888963407);
        *seed >> 11
    }

    #[test]
    fn overlapping_cidrs_merge_into_ranges() {
        let acl = Acl::parse(&[
            "10.0.0.0/8".into(),
            "10.1.0.0/16".into(),
            "11.0.0.0/8".into(),
            "192.168.1.7/24".into(),
            "fc00::/7".into(),
            "fd00::/8".into(),
        ])
        .unwrap();
        assert_eq!(
            acl.v4_ranges(),
            2,
            "10/8, 10.1/16 and the adjacent 11/8 merge"
        );
        assert_eq!(acl.v6_ranges(), 1);
        assert!(acl.allows("11.255.255.255".parse().unwrap()));
        assert!(
            acl.allows("192.168.1.200".parse().unwrap()),
            "host bits are truncated"
        );
        assert!(!acl.allows("12.0.0.0".parse().unwrap()));
        assert!(
            acl.allows("::ffff:10.9.9.9".parse().unwrap()),
            "v4-mapped uses the v4 table"
        );
        let all = Acl::parse(&["0.0.0.0/0".into(), "::/0".into()]).unwrap();
        assert!(
            all.allows("255.255.255.255".parse().unwrap())
                && all.allows("ffff::1".parse().unwrap())
        );
        assert!(
            !Acl::parse(&[])
                .unwrap()
                .allows("127.0.0.1".parse().unwrap())
        );
    }

    #[test]
    fn range_search_matches_linear_scan() {
        let mut seed = 7u64;
        let mut cidrs = Vec::new();
        for _ in 0..1000 {
            let a = std::net::Ipv4Addr::from(next(&mut seed) as u32);
            cidrs.push(format!("{a}/{}", 8 + next(&mut seed) % 25));
            let b = std::net::Ipv6Addr::from(
                ((next(&mut seed) as u128) << 64) | next(&mut seed) as u128,
            );
            cidrs.push(format!("{b}/{}", 16 + next(&mut seed) % 97));
        }
        let acl = Acl::parse(&cidrs).unwrap();
        let nets: Vec<IpNet> = cidrs
            .iter()
            .map(|c| c.parse::<IpNet>().unwrap().trunc())
            .collect();
        for i in 0..10_000u64 {
            let ip: IpAddr = if i % 2 == 0 {
                std::net::Ipv4Addr::from(next(&mut seed) as u32).into()
            } else {
                std::net::Ipv6Addr::from(
                    ((next(&mut seed) as u128) << 64) | next(&mut seed) as u128,
                )
                .into()
            };
            // Pick addresses inside a listed network half the time, so both answers are exercised.
            let ip = if i % 4 < 2 {
                nets[(i as usize) % nets.len()].network()
            } else {
                ip
            };
            let linear = nets.iter().any(|n| n.contains(&ip));
            assert_eq!(acl.allows(ip), linear, "{ip}");
        }
        assert!(acl.v4_ranges() < 1000, "overlapping random v4 CIDRs merge");
    }
}
