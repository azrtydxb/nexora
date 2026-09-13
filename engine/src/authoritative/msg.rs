//! A defensive DNS message reader for the authoritative slow paths and tests: one question, the
//! IXFR authority SOA, OPT and a trailing TSIG. The query fast path builds a `Question` from the
//! `wire::parse_query` view instead.

use super::{T_IXFR, T_OPT, T_SOA, T_TSIG};
use std::ops::Range;

const HEADER_LEN: usize = 12;
const OPCODE_NOTIFY: u8 = 4;
const OPCODE_UPDATE: u8 = 5;
const MAX_POINTER_JUMPS: usize = 64;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct EdnsInfo {
    pub udp_size: u16,
    pub do_bit: bool,
}

#[derive(Clone, Copy, Debug)]
pub struct Question<'a> {
    pub id: u16,
    pub opcode: u8,
    pub flags: u16,
    /// As received (client casing), uncompressed.
    pub qname: &'a [u8],
    pub qtype: u16,
    pub qclass: u16,
    /// Offset after the question section.
    pub question_end: usize,
    pub edns: Option<EdnsInfo>,
    /// SOA serial from the authority section of an IXFR query.
    pub ixfr_serial: Option<u32>,
    /// Offset of a trailing TSIG RR.
    pub tsig_at: Option<usize>,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, thiserror::Error)]
pub enum MsgError {
    #[error("message shorter than its header or sections")]
    Short,
    #[error("message is a response")]
    Header,
    #[error("malformed question name")]
    Name,
    #[error("section counts not allowed for this message")]
    Counts,
    #[error("malformed resource record")]
    Rr,
}

fn be16(b: &[u8], at: usize) -> u16 {
    u16::from_be_bytes([b[at], b[at + 1]])
}

impl<'a> Question<'a> {
    pub fn parse(msg: &'a [u8]) -> Result<Self, MsgError> {
        if msg.len() < HEADER_LEN {
            return Err(MsgError::Short);
        }
        let flags = be16(msg, 2);
        if flags & 0x8000 != 0 {
            return Err(MsgError::Header);
        }
        let opcode = ((flags >> 11) & 0x0f) as u8;
        let (qd, an, ns, ar) = (be16(msg, 4), be16(msg, 6), be16(msg, 8), be16(msg, 10));
        if qd != 1 {
            return Err(MsgError::Counts);
        }
        let name_end = uncompressed_name_end(msg, HEADER_LEN)?;
        let question_end = name_end + 4;
        if msg.len() < question_end {
            return Err(MsgError::Short);
        }
        let qtype = be16(msg, name_end);
        if an != 0 && opcode != OPCODE_NOTIFY && opcode != OPCODE_UPDATE {
            return Err(MsgError::Counts);
        }
        let ixfr = opcode == 0 && qtype == T_IXFR;
        if ns != 0 && opcode != OPCODE_UPDATE && !(ixfr && ns == 1) {
            return Err(MsgError::Counts);
        }
        let (_, after_an) = walk_rrs(msg, question_end, usize::from(an))?;
        let (auth, after_ns) = walk_rrs(msg, after_an, usize::from(ns))?;
        let ixfr_serial = if ixfr {
            let soa = auth.first().ok_or(MsgError::Counts)?;
            if soa.rtype != T_SOA || soa.rdata.len() < 22 {
                return Err(MsgError::Rr);
            }
            let s = soa.rdata.end - 20;
            Some(u32::from_be_bytes([
                msg[s],
                msg[s + 1],
                msg[s + 2],
                msg[s + 3],
            ]))
        } else {
            None
        };
        let (additional, end) = walk_rrs(msg, after_ns, usize::from(ar))?;
        if end != msg.len() {
            return Err(MsgError::Rr);
        }
        let mut edns = None;
        let mut tsig_at = None;
        for (i, rr) in additional.iter().enumerate() {
            match rr.rtype {
                T_OPT => {
                    if edns.is_some() || rr.name_end - rr.start != 1 || msg[rr.start] != 0 {
                        return Err(MsgError::Rr);
                    }
                    edns = Some(EdnsInfo {
                        udp_size: rr.class,
                        do_bit: rr.ttl & 0x8000 != 0,
                    });
                }
                T_TSIG => {
                    if i + 1 != additional.len() {
                        return Err(MsgError::Rr);
                    }
                    tsig_at = Some(rr.start);
                }
                _ if opcode == OPCODE_UPDATE => {}
                _ => return Err(MsgError::Counts),
            }
        }
        Ok(Question {
            id: be16(msg, 0),
            opcode,
            flags,
            qname: &msg[HEADER_LEN..name_end],
            qtype,
            qclass: be16(msg, name_end + 2),
            question_end,
            edns,
            ixfr_serial,
            tsig_at,
        })
    }

    pub fn rd(&self) -> bool {
        self.flags & 0x0100 != 0
    }
}

/// The end of an uncompressed name starting at `at`: labels ≤ 63 octets, name ≤ 255 octets.
fn uncompressed_name_end(msg: &[u8], at: usize) -> Result<usize, MsgError> {
    let mut i = at;
    loop {
        let l = *msg.get(i).ok_or(MsgError::Name)? as usize;
        if l >= 0x40 {
            return Err(MsgError::Name);
        }
        if i + 1 + l - at > 255 {
            return Err(MsgError::Name);
        }
        if l == 0 {
            return Ok(i + 1);
        }
        i += l + 1;
    }
}

/// One resource record located in a message; `rdata` indexes the message.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct RrAt {
    pub start: usize,
    pub name_end: usize,
    pub rtype: u16,
    pub class: u16,
    pub ttl: u32,
    pub rdata: Range<usize>,
}

/// The end (in place) of a possibly compressed name at `at`. Pointers must target an offset
/// strictly lower than the pointer itself, at most 64 jumps, decoded length ≤ 255 octets.
fn name_end(msg: &[u8], at: usize) -> Result<usize, MsgError> {
    let (mut i, mut end, mut jumps, mut total) = (at, None, 0usize, 0usize);
    loop {
        let l = *msg.get(i).ok_or(MsgError::Rr)?;
        match l {
            0 => return Ok(end.unwrap_or(i + 1)),
            0xc0..=0xff => {
                let lo = *msg.get(i + 1).ok_or(MsgError::Rr)?;
                let target = (usize::from(l & 0x3f) << 8) | usize::from(lo);
                jumps += 1;
                if target >= i || jumps > MAX_POINTER_JUMPS {
                    return Err(MsgError::Rr);
                }
                end.get_or_insert(i + 2);
                i = target;
            }
            0x40..=0xbf => return Err(MsgError::Rr),
            _ => {
                total += usize::from(l) + 1;
                if total > 254 {
                    return Err(MsgError::Rr);
                }
                i += usize::from(l) + 1;
            }
        }
    }
}

/// Walks `count` RRs from `from`; returns them and the offset after the last one.
pub fn walk_rrs(msg: &[u8], from: usize, count: usize) -> Result<(Vec<RrAt>, usize), MsgError> {
    // Every RR takes at least 11 octets: bound the allocation by what the message can hold.
    if count > msg.len().saturating_sub(from) / 11 {
        return Err(MsgError::Short);
    }
    let mut out = Vec::with_capacity(count);
    let mut pos = from;
    for _ in 0..count {
        let start = pos;
        let ne = name_end(msg, pos)?;
        let fixed = msg.get(ne..ne + 10).ok_or(MsgError::Short)?;
        let rdlen = usize::from(be16(fixed, 8));
        let rdata = ne + 10..ne + 10 + rdlen;
        if rdata.end > msg.len() {
            return Err(MsgError::Short);
        }
        out.push(RrAt {
            start,
            name_end: ne,
            rtype: be16(fixed, 0),
            class: be16(fixed, 2),
            ttl: u32::from_be_bytes([fixed[4], fixed[5], fixed[6], fixed[7]]),
            rdata: rdata.clone(),
        });
        pos = rdata.end;
    }
    Ok((out, pos))
}
