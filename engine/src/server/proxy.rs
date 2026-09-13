//! PROXY protocol v2 header parsing and the trusted-peer policy for stream listeners.

use std::net::{IpAddr, Ipv4Addr, Ipv6Addr, SocketAddr};
use std::time::Duration;
use tokio::io::{AsyncRead, AsyncReadExt};

pub const SIGNATURE: [u8; 12] = *b"\r\n\r\n\0\r\nQUIT\n";
pub const HEADER_TIMEOUT: Duration = Duration::from_secs(5);
/// Largest header body accepted: both unix addresses (216 bytes) plus room for TLVs.
const MAX_BODY: usize = 536;

#[derive(Debug, PartialEq, Eq)]
pub enum ProxyHeader {
    /// A health check or other connection the proxy made on its own behalf.
    Local,
    Proxied {
        source: SocketAddr,
        destination: SocketAddr,
    },
}

#[derive(Debug, PartialEq, Eq)]
pub enum ProxyError {
    BadSignature,
    Version,
    Command,
    Family,
    TooLong,
    Io,
}

#[derive(Debug, PartialEq, Eq)]
pub enum ProxyReject {
    UntrustedPeer,
    InvalidHeader,
    Timeout,
}

/// IPv4-mapped IPv6 addresses (from dual-stack sockets) become plain IPv4.
pub fn normalize_peer(addr: SocketAddr) -> SocketAddr {
    match addr.ip() {
        IpAddr::V6(v6) => match v6.to_ipv4_mapped() {
            Some(v4) => SocketAddr::new(IpAddr::V4(v4), addr.port()),
            None => addr,
        },
        IpAddr::V4(_) => addr,
    }
}

/// Reads exactly one PROXY v2 header, leaving the stream at the first payload byte.
/// Only TCP over IPv4/IPv6 is accepted (every listener using it is a stream); TLVs are ignored.
pub async fn read_proxy_v2<S: AsyncRead + Unpin>(io: &mut S) -> Result<ProxyHeader, ProxyError> {
    let mut fixed = [0u8; 16];
    io.read_exact(&mut fixed)
        .await
        .map_err(|_| ProxyError::Io)?;
    if fixed[..12] != SIGNATURE {
        return Err(ProxyError::BadSignature);
    }
    if fixed[12] >> 4 != 2 {
        return Err(ProxyError::Version);
    }
    let cmd = fixed[12] & 0x0f;
    let fam = fixed[13];
    let len = usize::from(u16::from_be_bytes([fixed[14], fixed[15]]));
    if len > MAX_BODY {
        return Err(ProxyError::TooLong);
    }
    let mut body = [0u8; MAX_BODY];
    io.read_exact(&mut body[..len])
        .await
        .map_err(|_| ProxyError::Io)?;
    match cmd {
        0x0 => return Ok(ProxyHeader::Local),
        0x1 => {}
        _ => return Err(ProxyError::Command),
    }
    let b = &body[..len];
    let port = |i: usize| u16::from_be_bytes([b[i], b[i + 1]]);
    match fam {
        0x00 => Ok(ProxyHeader::Local),
        // TCP over IPv4
        0x11 if len >= 12 => Ok(ProxyHeader::Proxied {
            source: SocketAddr::new(Ipv4Addr::new(b[0], b[1], b[2], b[3]).into(), port(8)),
            destination: SocketAddr::new(Ipv4Addr::new(b[4], b[5], b[6], b[7]).into(), port(10)),
        }),
        // TCP over IPv6
        0x21 if len >= 36 => {
            let src: [u8; 16] = b[0..16].try_into().expect("16 bytes");
            let dst: [u8; 16] = b[16..32].try_into().expect("16 bytes");
            Ok(ProxyHeader::Proxied {
                source: SocketAddr::new(Ipv6Addr::from(src).into(), port(32)),
                destination: SocketAddr::new(Ipv6Addr::from(dst).into(), port(34)),
            })
        }
        _ => Err(ProxyError::Family),
    }
}

/// The peers allowed to send PROXY headers; every other peer is refused.
#[derive(Clone)]
pub struct ProxyPolicy {
    trusted: Vec<ipnet::IpNet>,
}

impl ProxyPolicy {
    pub fn new(cidrs: &[String]) -> Result<Self, String> {
        if cidrs.is_empty() {
            return Err(
                "proxy_protocol_trusted_cidrs must not be empty when PROXY protocol is enabled"
                    .into(),
            );
        }
        let trusted = cidrs
            .iter()
            .map(|c| {
                c.parse::<ipnet::IpNet>()
                    .map_err(|_| format!("invalid proxy_protocol_trusted_cidrs entry {c:?}"))
            })
            .collect::<Result<Vec<_>, _>>()?;
        Ok(Self { trusted })
    }

    fn trusts(&self, ip: IpAddr) -> bool {
        self.trusted.iter().any(|n| n.contains(&ip))
    }
}

/// The client address for a new connection from `peer`: the peer itself without a
/// policy, otherwise the PROXY header source from a trusted peer.
pub async fn resolve_client<S: AsyncRead + Unpin>(
    policy: Option<&ProxyPolicy>,
    io: &mut S,
    peer: SocketAddr,
) -> Result<SocketAddr, ProxyReject> {
    let peer = normalize_peer(peer);
    let Some(policy) = policy else {
        return Ok(peer);
    };
    if !policy.trusts(peer.ip()) {
        return Err(ProxyReject::UntrustedPeer);
    }
    match tokio::time::timeout(HEADER_TIMEOUT, read_proxy_v2(io)).await {
        Err(_) => Err(ProxyReject::Timeout),
        Ok(Err(_)) => Err(ProxyReject::InvalidHeader),
        Ok(Ok(ProxyHeader::Local)) => Ok(peer),
        Ok(Ok(ProxyHeader::Proxied { source, .. })) => Ok(normalize_peer(source)),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use tokio::io::AsyncReadExt;

    fn header(cmd_ver: u8, fam: u8, body: &[u8]) -> Vec<u8> {
        let mut h = SIGNATURE.to_vec();
        h.push(cmd_ver);
        h.push(fam);
        h.extend_from_slice(&(body.len() as u16).to_be_bytes());
        h.extend_from_slice(body);
        h
    }

    #[tokio::test]
    async fn parses_tcp4_and_leaves_payload_unread() {
        let mut data = header(
            0x21,
            0x11,
            &[192, 0, 2, 7, 10, 0, 0, 1, 0xd4, 0x31, 0x03, 0x55],
        );
        data.extend_from_slice(b"\x00\x1d");
        let mut cur = std::io::Cursor::new(data);
        let h = read_proxy_v2(&mut cur).await.unwrap();
        assert_eq!(
            h,
            ProxyHeader::Proxied {
                source: "192.0.2.7:54321".parse().unwrap(),
                destination: "10.0.0.1:853".parse().unwrap(),
            }
        );
        let mut rest = Vec::new();
        cur.read_to_end(&mut rest).await.unwrap();
        assert_eq!(rest, b"\x00\x1d");
    }

    #[tokio::test]
    async fn parses_tcp6_with_tlvs() {
        let mut body = Vec::new();
        body.extend_from_slice(
            &"2001:db8::7"
                .parse::<std::net::Ipv6Addr>()
                .unwrap()
                .octets(),
        );
        body.extend_from_slice(
            &"2001:db8::1"
                .parse::<std::net::Ipv6Addr>()
                .unwrap()
                .octets(),
        );
        body.extend_from_slice(&[0x13, 0x88, 0x01, 0xbb]);
        body.extend_from_slice(&[0x04, 0x00, 0x01, 0xaa]); // NOOP TLV, ignored
        let mut cur = std::io::Cursor::new(header(0x21, 0x21, &body));
        assert_eq!(
            read_proxy_v2(&mut cur).await.unwrap(),
            ProxyHeader::Proxied {
                source: "[2001:db8::7]:5000".parse().unwrap(),
                destination: "[2001:db8::1]:443".parse().unwrap(),
            }
        );
    }

    #[tokio::test]
    async fn local_command_and_errors() {
        let mut cur = std::io::Cursor::new(header(0x20, 0x00, &[]));
        assert_eq!(read_proxy_v2(&mut cur).await.unwrap(), ProxyHeader::Local);
        let mut bad = header(0x21, 0x11, &[0; 12]);
        bad[0] = b'X';
        assert_eq!(
            read_proxy_v2(&mut std::io::Cursor::new(bad)).await,
            Err(ProxyError::BadSignature)
        );
        assert_eq!(
            read_proxy_v2(&mut std::io::Cursor::new(header(0x11, 0x11, &[0; 12]))).await,
            Err(ProxyError::Version)
        );
        assert_eq!(
            read_proxy_v2(&mut std::io::Cursor::new(header(0x22, 0x11, &[0; 12]))).await,
            Err(ProxyError::Command)
        );
        assert_eq!(
            read_proxy_v2(&mut std::io::Cursor::new(header(0x21, 0x12, &[0; 12]))).await,
            Err(ProxyError::Family)
        );
        assert_eq!(
            read_proxy_v2(&mut std::io::Cursor::new(header(0x21, 0x11, &[0; 4]))).await,
            Err(ProxyError::Family)
        );
        let truncated = header(0x21, 0x11, &[0; 12])[..20].to_vec();
        assert_eq!(
            read_proxy_v2(&mut std::io::Cursor::new(truncated)).await,
            Err(ProxyError::Io)
        );
    }

    #[tokio::test]
    async fn policy_rejects_untrusted_peer_and_uses_header_source() {
        let policy = ProxyPolicy::new(&["10.0.0.0/8".to_string()]).unwrap();
        let data = header(
            0x21,
            0x11,
            &[192, 0, 2, 7, 10, 0, 0, 1, 0xd4, 0x31, 0x03, 0x55],
        );
        let got = resolve_client(
            Some(&policy),
            &mut std::io::Cursor::new(data.clone()),
            "10.9.9.9:4000".parse().unwrap(),
        )
        .await;
        assert_eq!(got, Ok("192.0.2.7:54321".parse().unwrap()));
        let got = resolve_client(
            Some(&policy),
            &mut std::io::Cursor::new(data),
            "198.51.100.1:4000".parse().unwrap(),
        )
        .await;
        assert_eq!(got, Err(ProxyReject::UntrustedPeer));
        let got = resolve_client(
            None,
            &mut std::io::Cursor::new(Vec::new()),
            "[::ffff:198.51.100.1]:4000".parse().unwrap(),
        )
        .await;
        assert_eq!(got, Ok("198.51.100.1:4000".parse().unwrap()));
        assert!(ProxyPolicy::new(&[]).is_err());
    }
}
