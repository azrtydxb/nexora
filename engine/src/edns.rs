//! EDNS(0) OPT parsing and writing (RFC 6891) and DNS cookies (RFC 7873, RFC 9018).

use crate::wire::ParseError;
use std::hash::Hasher;
use std::net::IpAddr;

/// Root owner (1) + TYPE (2) + CLASS (2) + TTL (4) + RDLENGTH (2).
pub const OPT_BASE_LEN: usize = 11;
/// COOKIE option header (4) + client cookie (8) + server cookie (16).
pub const OPT_COOKIE_LEN: usize = 4 + 24;

const TYPE_OPT: u16 = 41;
const OPTION_COOKIE: u16 = 10;
const UDP_NO_EDNS_LIMIT: usize = 512;
/// DNS flag day 2020 default; larger UDP replies fragment.
const UDP_MAX_LIMIT: usize = 1232;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Transport {
    Udp,
    Tcp,
}

/// A borrowed view of a query's OPT record.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct OptView<'a> {
    pub udp_size: u16,
    pub version: u8,
    pub do_bit: bool,
    pub client_cookie: Option<[u8; 8]>,
    pub server_cookie: Option<&'a [u8]>,
    pub bad_cookie_len: bool,
}

/// The OPT record to append to a reply.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct ReplyOpt {
    pub udp_size: u16,
    pub do_bit: bool,
    pub ext_rcode: u8,
    pub cookie: Option<([u8; 8], [u8; 16])>,
}

impl ReplyOpt {
    pub fn wire_len(&self) -> usize {
        OPT_BASE_LEN
            + if self.cookie.is_some() {
                OPT_COOKIE_LEN
            } else {
                0
            }
    }
}

pub struct CookieSecret(pub [u8; 16]);

/// Parses the OPT RR at the start of `rr`; returns the view and the RR length.
pub fn parse_opt(rr: &[u8]) -> Result<(OptView<'_>, usize), ParseError> {
    if rr.len() < OPT_BASE_LEN || rr[0] != 0 || be16(rr, 1) != TYPE_OPT {
        return Err(ParseError::FormErr);
    }
    let end = OPT_BASE_LEN + be16(rr, 9) as usize;
    let rdata = rr.get(OPT_BASE_LEN..end).ok_or(ParseError::FormErr)?;
    let mut view = OptView {
        udp_size: be16(rr, 3),
        version: rr[6],
        do_bit: rr[7] & 0x80 != 0,
        client_cookie: None,
        server_cookie: None,
        bad_cookie_len: false,
    };
    let mut pos = 0;
    while pos < rdata.len() {
        if rdata.len() - pos < 4 {
            return Err(ParseError::FormErr);
        }
        let code = be16(rdata, pos);
        let len = be16(rdata, pos + 2) as usize;
        let data = rdata
            .get(pos + 4..pos + 4 + len)
            .ok_or(ParseError::FormErr)?;
        if code == OPTION_COOKIE {
            match len {
                8 | 16..=40 => {
                    view.client_cookie = Some(data[..8].try_into().expect("length checked"));
                    view.server_cookie = (len > 8).then(|| &data[8..]);
                }
                _ => view.bad_cookie_len = true,
            }
        }
        pos += 4 + len;
    }
    Ok((view, end))
}

/// The largest reply the client accepts over `transport`.
pub fn reply_limit(opt: Option<&OptView<'_>>, transport: Transport) -> usize {
    match (transport, opt) {
        (Transport::Tcp, _) => 65535,
        (Transport::Udp, None) => UDP_NO_EDNS_LIMIT,
        (Transport::Udp, Some(o)) => (o.udp_size as usize).clamp(UDP_NO_EDNS_LIMIT, UDP_MAX_LIMIT),
    }
}

/// RFC 9018 interoperable server cookie: version 1, reserved, timestamp, SipHash-2-4.
pub fn server_cookie(
    secret: &CookieSecret,
    client_cookie: &[u8; 8],
    client: IpAddr,
    now_secs: u32,
) -> [u8; 16] {
    let mut out = [0u8; 16];
    out[0] = 1;
    out[4..8].copy_from_slice(&now_secs.to_be_bytes());
    let mut h = siphasher::sip::SipHasher24::new_with_key(&secret.0);
    h.write(client_cookie);
    h.write(&out[..8]);
    match client {
        IpAddr::V4(a) => h.write(&a.octets()),
        IpAddr::V6(a) => h.write(&a.octets()),
    }
    out[8..].copy_from_slice(&h.finish().to_le_bytes());
    out
}

/// Writes the OPT RR; returns 0 when `out` is too small.
pub fn write_opt(out: &mut [u8], opt: &ReplyOpt) -> usize {
    let len = opt.wire_len();
    let Some(out) = out.get_mut(..len) else {
        return 0;
    };
    let flags: u16 = if opt.do_bit { 0x8000 } else { 0 };
    let rdlen = (len - OPT_BASE_LEN) as u16;
    out[0] = 0;
    out[1..3].copy_from_slice(&TYPE_OPT.to_be_bytes());
    out[3..5].copy_from_slice(&opt.udp_size.to_be_bytes());
    out[5] = opt.ext_rcode;
    out[6] = 0; // EDNS version 0
    out[7..9].copy_from_slice(&flags.to_be_bytes());
    out[9..11].copy_from_slice(&rdlen.to_be_bytes());
    if let Some((client, server)) = &opt.cookie {
        out[11..13].copy_from_slice(&OPTION_COOKIE.to_be_bytes());
        out[13..15].copy_from_slice(&24u16.to_be_bytes());
        out[15..23].copy_from_slice(client);
        out[23..39].copy_from_slice(server);
    }
    len
}

fn be16(b: &[u8], at: usize) -> u16 {
    u16::from_be_bytes([b[at], b[at + 1]])
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::net::{IpAddr, Ipv4Addr};

    #[test]
    fn reply_limit_rules() {
        assert_eq!(reply_limit(None, Transport::Udp), 512);
        let big = OptView {
            udp_size: 4096,
            version: 0,
            do_bit: false,
            client_cookie: None,
            server_cookie: None,
            bad_cookie_len: false,
        };
        assert_eq!(reply_limit(Some(&big), Transport::Udp), 1232);
        let small = OptView {
            udp_size: 100,
            ..big
        };
        assert_eq!(reply_limit(Some(&small), Transport::Udp), 512);
        let mid = OptView {
            udp_size: 1000,
            ..big
        };
        assert_eq!(reply_limit(Some(&mid), Transport::Udp), 1000);
        assert_eq!(reply_limit(None, Transport::Tcp), 65535);
    }

    #[test]
    fn server_cookie_is_deterministic_per_client_and_changes_with_ip() {
        let s = CookieSecret([7; 16]);
        let c = [1u8; 8];
        let a = server_cookie(
            &s,
            &c,
            IpAddr::V4(Ipv4Addr::new(10, 0, 0, 1)),
            1_700_000_000,
        );
        let b = server_cookie(
            &s,
            &c,
            IpAddr::V4(Ipv4Addr::new(10, 0, 0, 1)),
            1_700_000_000,
        );
        let d = server_cookie(
            &s,
            &c,
            IpAddr::V4(Ipv4Addr::new(10, 0, 0, 2)),
            1_700_000_000,
        );
        assert_eq!(a, b);
        assert_ne!(a, d);
        assert_eq!(a[0], 1, "RFC 9018 version byte");
        assert_eq!(&a[4..8], &1_700_000_000u32.to_be_bytes());
    }

    #[test]
    fn write_opt_with_cookie_has_expected_length() {
        let mut out = [0u8; 64];
        let n = write_opt(
            &mut out,
            &ReplyOpt {
                udp_size: 1232,
                do_bit: true,
                ext_rcode: 0,
                cookie: Some(([1; 8], [2; 16])),
            },
        );
        assert_eq!(n, OPT_BASE_LEN + OPT_COOKIE_LEN);
        assert_eq!(out[0], 0);
        assert_eq!(&out[1..3], &41u16.to_be_bytes());
        assert_eq!(&out[3..5], &1232u16.to_be_bytes());
        assert_eq!(out[7] & 0x80, 0x80, "DO bit");
        assert_eq!(
            write_opt(
                &mut out[..5],
                &ReplyOpt {
                    udp_size: 1232,
                    do_bit: false,
                    ext_rcode: 0,
                    cookie: None
                }
            ),
            0
        );
    }
}
