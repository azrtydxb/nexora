//! Zero-copy DNS query parsing, reply writers and the upstream response walker.

use crate::edns::{self, OptView, ReplyOpt};
use std::hash::{Hash, Hasher};
use std::ops::Range;

pub const RCODE_NOERROR: u8 = 0;
pub const RCODE_FORMERR: u8 = 1;
pub const RCODE_SERVFAIL: u8 = 2;
pub const RCODE_NXDOMAIN: u8 = 3;
pub const RCODE_NOTIMP: u8 = 4;
pub const RCODE_REFUSED: u8 = 5;

const HEADER_LEN: usize = 12;
const MAX_NAME_LEN: usize = 255;
const MAX_POINTER_HOPS: usize = 128;
const TYPE_A: u16 = 1;
const TYPE_CNAME: u16 = 5;
const TYPE_SOA: u16 = 6;
const TYPE_AAAA: u16 = 28;
const TYPE_OPT: u16 = 41;
const CLASS_IN: u16 = 1;
const FLAG_QR: u8 = 0x80;
const FLAG_RD: u8 = 0x01;
const FLAG_RA: u8 = 0x80;

/// A lowercase, uncompressed wire-format name stored inline.
#[derive(Clone, Copy)]
pub struct NameKey {
    len: u8,
    buf: [u8; 255],
}

impl NameKey {
    pub fn as_wire(&self) -> &[u8] {
        &self.buf[..self.len as usize]
    }

    /// Validates an uncompressed wire name spanning all of `wire` and lowercases it.
    pub fn from_wire_lowercase(wire: &[u8]) -> Option<NameKey> {
        match read_name(wire, 0, false) {
            Ok((key, end)) if end == wire.len() => Some(key),
            _ => None,
        }
    }
}

impl PartialEq for NameKey {
    fn eq(&self, other: &Self) -> bool {
        self.as_wire() == other.as_wire()
    }
}

impl Eq for NameKey {}

impl Hash for NameKey {
    fn hash<H: Hasher>(&self, state: &mut H) {
        self.as_wire().hash(state);
    }
}

impl std::fmt::Debug for NameKey {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(
            f,
            "NameKey({:?})",
            self.as_wire().escape_ascii().to_string()
        )
    }
}

#[derive(Debug)]
pub struct QueryView<'a> {
    pub id: u16,
    pub flags: u16,
    /// The question name as the client sent it (original casing).
    pub qname: &'a [u8],
    pub key: NameKey,
    pub qtype: u16,
    pub qclass: u16,
    pub question_end: usize,
    pub opt: Option<OptView<'a>>,
}

impl QueryView<'_> {
    pub fn rd(&self) -> bool {
        self.flags & 0x0100 != 0
    }

    pub fn cd(&self) -> bool {
        self.flags & 0x0010 != 0
    }

    pub fn do_bit(&self) -> bool {
        self.opt.is_some_and(|o| o.do_bit)
    }
}

#[derive(Debug, PartialEq, Eq, thiserror::Error)]
pub enum ParseError {
    #[error("short")]
    TooShort,
    #[error("formerr")]
    FormErr,
    #[error("notimp")]
    NotImp,
    #[error("response")]
    IsResponse,
}

pub fn parse_query(buf: &[u8]) -> Result<QueryView<'_>, ParseError> {
    if buf.len() < HEADER_LEN {
        return Err(ParseError::TooShort);
    }
    let flags = be16(buf, 2);
    if flags & 0x8000 != 0 {
        return Err(ParseError::IsResponse);
    }
    if (flags >> 11) & 0x0f != 0 {
        return Err(ParseError::NotImp);
    }
    let arcount = be16(buf, 10);
    if be16(buf, 4) != 1 || be16(buf, 6) != 0 || be16(buf, 8) != 0 || arcount > 1 {
        return Err(ParseError::FormErr);
    }
    let (key, name_end) = read_name(buf, HEADER_LEN, false)?;
    let question_end = name_end + 4;
    if buf.len() < question_end {
        return Err(ParseError::FormErr);
    }
    let opt = if arcount == 1 {
        let (opt, used) = edns::parse_opt(&buf[question_end..])?;
        if question_end + used != buf.len() {
            return Err(ParseError::FormErr);
        }
        Some(opt)
    } else if question_end != buf.len() {
        return Err(ParseError::FormErr);
    } else {
        None
    };
    Ok(QueryView {
        id: be16(buf, 0),
        flags,
        qname: &buf[HEADER_LEN..name_end],
        key,
        qtype: be16(buf, name_end),
        qclass: be16(buf, name_end + 2),
        question_end,
        opt,
    })
}

/// Header-only reply to a query that did not parse, echoing ID, opcode and RD,
/// plus the question when it walks. `None` for fewer than 12 bytes (drop) or
/// an `out` shorter than a header.
pub fn write_error_reply(query: &[u8], rcode: u8, out: &mut [u8]) -> Option<usize> {
    if query.len() < HEADER_LEN || out.len() < HEADER_LEN {
        return None;
    }
    out[..HEADER_LEN].fill(0);
    out[..2].copy_from_slice(&query[..2]);
    out[2] = FLAG_QR | (query[2] & 0x78) | (query[2] & FLAG_RD);
    out[3] = FLAG_RA | (rcode & 0x0f);
    let question = read_name(query, HEADER_LEN, false)
        .ok()
        .map(|(_, end)| HEADER_LEN..end + 4)
        .filter(|r| r.end <= query.len() && r.end <= out.len());
    let Some(question) = question else {
        return Some(HEADER_LEN);
    };
    let end = question.end;
    out[HEADER_LEN..end].copy_from_slice(&query[question]);
    out[5] = 1;
    Some(end)
}

/// Header + question (+ OPT) reply with `rcode`; returns 0 when `out` is too small.
pub fn write_rcode_reply(
    q: &QueryView<'_>,
    rcode: u8,
    out: &mut [u8],
    opt: Option<&ReplyOpt>,
) -> usize {
    write_synth_reply(q, rcode, None, 0, out, opt)
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum SynthAnswer {
    A([u8; 4]),
    Aaaa([u8; 16]),
}

/// Reply with an optional single synthesized A/AAAA answer; returns 0 when `out` is too small.
pub fn write_synth_reply(
    q: &QueryView<'_>,
    rcode: u8,
    answer: Option<SynthAnswer>,
    ttl: u32,
    out: &mut [u8],
    opt: Option<&ReplyOpt>,
) -> usize {
    let (rtype, rdata): (u16, &[u8]) = match &answer {
        Some(SynthAnswer::A(a)) => (TYPE_A, a),
        Some(SynthAnswer::Aaaa(a)) => (TYPE_AAAA, a),
        None => (0, &[]),
    };
    let question_end = HEADER_LEN + q.qname.len() + 4;
    let answer_len = if answer.is_some() {
        12 + rdata.len()
    } else {
        0
    };
    let total = question_end + answer_len + opt.map_or(0, ReplyOpt::wire_len);
    let Some(out) = out.get_mut(..total) else {
        return 0;
    };
    out[..2].copy_from_slice(&q.id.to_be_bytes());
    out[2] = FLAG_QR | ((q.flags >> 8) as u8 & (0x78 | FLAG_RD));
    out[3] = FLAG_RA | (rcode & 0x0f);
    out[4..6].copy_from_slice(&1u16.to_be_bytes());
    out[6..8].copy_from_slice(&u16::from(answer.is_some()).to_be_bytes());
    out[8..10].fill(0);
    out[10..12].copy_from_slice(&u16::from(opt.is_some()).to_be_bytes());
    let mut pos = HEADER_LEN + q.qname.len();
    out[HEADER_LEN..pos].copy_from_slice(q.qname);
    out[pos..pos + 2].copy_from_slice(&q.qtype.to_be_bytes());
    out[pos + 2..pos + 4].copy_from_slice(&q.qclass.to_be_bytes());
    pos += 4;
    if answer.is_some() {
        out[pos..pos + 2].copy_from_slice(&0xc00cu16.to_be_bytes());
        out[pos + 2..pos + 4].copy_from_slice(&rtype.to_be_bytes());
        out[pos + 4..pos + 6].copy_from_slice(&CLASS_IN.to_be_bytes());
        out[pos + 6..pos + 10].copy_from_slice(&ttl.to_be_bytes());
        out[pos + 10..pos + 12].copy_from_slice(&(rdata.len() as u16).to_be_bytes());
        out[pos + 12..pos + 12 + rdata.len()].copy_from_slice(rdata);
        pos += answer_len;
    }
    if let Some(opt) = opt {
        pos += edns::write_opt(&mut out[pos..], opt);
    }
    pos
}

/// What the cache and resolver need from an upstream response.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ResponseInfo {
    pub rcode: u8,
    pub tc: bool,
    pub min_ttl: Option<u32>,
    pub negative_ttl: Option<u32>,
    pub ttl_offsets: Vec<u16>,
    pub opt_range: Option<Range<usize>>,
    pub cname_targets: Vec<NameKey>,
    pub ancount: u16,
}

/// Walks every RR of an upstream response to `expect` (miss path; allocates).
/// The question must equal `expect`'s; compression pointers must point backwards.
pub fn walk_response(msg: &[u8], expect: &QueryView<'_>) -> Result<ResponseInfo, ParseError> {
    if msg.len() < HEADER_LEN {
        return Err(ParseError::TooShort);
    }
    let question_end =
        question_end(msg, &expect.key, expect.qtype, expect.qclass).ok_or(ParseError::FormErr)?;
    let ancount = be16(msg, 6);
    let nscount = be16(msg, 8);
    let arcount = be16(msg, 10);
    let mut info = ResponseInfo {
        rcode: msg[3] & 0x0f,
        tc: msg[2] & 0x02 != 0,
        min_ttl: None,
        negative_ttl: None,
        ttl_offsets: Vec::with_capacity(
            usize::from(ancount) + usize::from(nscount) + usize::from(arcount),
        ),
        opt_range: None,
        cname_targets: Vec::new(),
        ancount,
    };
    let negative = ancount == 0 || info.rcode == RCODE_NXDOMAIN;
    let mut pos = question_end;
    let sections = [(0u8, ancount), (1, nscount), (2, arcount)];
    for (section, count) in sections {
        for _ in 0..count {
            let start = pos;
            let (_, name_end) = read_name(msg, pos, true)?;
            let fixed = msg
                .get(name_end..name_end + 10)
                .ok_or(ParseError::FormErr)?;
            let rtype = be16(fixed, 0);
            // RFC 2181 section 8: a TTL with the top bit set is treated as zero.
            let ttl = match u32::from_be_bytes([fixed[4], fixed[5], fixed[6], fixed[7]]) {
                t if t > i32::MAX as u32 => 0,
                t => t,
            };
            let rdata_start = name_end + 10;
            let rdata_end = rdata_start + be16(fixed, 8) as usize;
            if rdata_end > msg.len() {
                return Err(ParseError::FormErr);
            }
            pos = rdata_end;
            if rtype == TYPE_OPT {
                if section != 2 || info.opt_range.is_some() {
                    return Err(ParseError::FormErr);
                }
                let ext = u16::from(fixed[5]);
                if ext != 0 {
                    info.rcode =
                        u8::try_from((ext << 4) | u16::from(info.rcode)).unwrap_or(u8::MAX);
                }
                info.opt_range = Some(start..rdata_end);
                continue;
            }
            info.ttl_offsets
                .push(u16::try_from(name_end + 4).map_err(|_| ParseError::FormErr)?);
            match (section, rtype) {
                (0, _) => {
                    info.min_ttl = Some(info.min_ttl.map_or(ttl, |m| m.min(ttl)));
                    if rtype == TYPE_CNAME {
                        let (target, end) = read_name(msg, rdata_start, true)?;
                        if end != rdata_end {
                            return Err(ParseError::FormErr);
                        }
                        info.cname_targets.push(target);
                    }
                }
                (1, TYPE_SOA) if negative => {
                    let (_, mname_end) = read_name(msg, rdata_start, true)?;
                    let (_, rname_end) = read_name(msg, mname_end, true)?;
                    if rname_end + 20 != rdata_end {
                        return Err(ParseError::FormErr);
                    }
                    let minimum = u32::from_be_bytes(
                        msg[rname_end + 16..rdata_end].try_into().expect("4 bytes"),
                    );
                    let neg = ttl.min(minimum);
                    info.negative_ttl = Some(info.negative_ttl.map_or(neg, |n| n.min(neg)));
                }
                _ => {}
            }
        }
    }
    if pos != msg.len() {
        return Err(ParseError::FormErr);
    }
    Ok(info)
}

/// True when `msg` carries exactly one question equal (case-insensitively) to the given one.
pub fn question_matches(msg: &[u8], qname_key: &NameKey, qtype: u16, qclass: u16) -> bool {
    question_end(msg, qname_key, qtype, qclass).is_some()
}

/// The offset past the single question when it equals the given one.
fn question_end(msg: &[u8], qname_key: &NameKey, qtype: u16, qclass: u16) -> Option<usize> {
    if msg.len() < HEADER_LEN || be16(msg, 4) != 1 {
        return None;
    }
    let (key, end) = read_name(msg, HEADER_LEN, false).ok()?;
    let matches = msg.len() >= end + 4
        && key == *qname_key
        && be16(msg, end) == qtype
        && be16(msg, end + 2) == qclass;
    matches.then_some(end + 4)
}

/// Reads the name at `start`, lowercasing it into a `NameKey`. Returns the key
/// and the offset just past the name as it appears at `start`. Pointers are
/// rejected unless `compressed`, and then must point strictly backwards.
fn read_name(msg: &[u8], start: usize, compressed: bool) -> Result<(NameKey, usize), ParseError> {
    let mut key = NameKey {
        len: 0,
        buf: [0; MAX_NAME_LEN],
    };
    let mut len = 0usize;
    let mut pos = start;
    let mut end = None;
    let mut hops = 0;
    loop {
        let b = *msg.get(pos).ok_or(ParseError::FormErr)?;
        match b & 0xc0 {
            0x00 => {
                let label_len = b as usize;
                if len + 1 + label_len > MAX_NAME_LEN {
                    return Err(ParseError::FormErr);
                }
                let label = msg
                    .get(pos + 1..pos + 1 + label_len)
                    .ok_or(ParseError::FormErr)?;
                key.buf[len] = b;
                for (dst, src) in key.buf[len + 1..len + 1 + label_len].iter_mut().zip(label) {
                    *dst = src.to_ascii_lowercase();
                }
                len += 1 + label_len;
                pos += 1 + label_len;
                if label_len == 0 {
                    break;
                }
            }
            0xc0 if compressed => {
                let target = (usize::from(b & 0x3f) << 8)
                    | usize::from(*msg.get(pos + 1).ok_or(ParseError::FormErr)?);
                hops += 1;
                if target >= pos || hops > MAX_POINTER_HOPS {
                    return Err(ParseError::FormErr);
                }
                end.get_or_insert(pos + 2);
                pos = target;
            }
            _ => return Err(ParseError::FormErr),
        }
    }
    key.len = len as u8;
    Ok((key, end.unwrap_or(pos)))
}

fn be16(b: &[u8], at: usize) -> u16 {
    u16::from_be_bytes([b[at], b[at + 1]])
}

#[cfg(test)]
mod tests {
    use super::*;
    use hickory_proto::op::{Edns, Message, MessageType, OpCode, Query, ResponseCode};
    use hickory_proto::rr::{Name, RecordType};
    use hickory_proto::serialize::binary::{BinDecodable, BinEncodable};

    fn query(name: &str, rtype: RecordType) -> Vec<u8> {
        let mut m = Message::new(0xbeef, MessageType::Query, OpCode::Query);
        m.metadata.recursion_desired = true;
        m.add_query(Query::query(Name::from_ascii(name).unwrap(), rtype));
        m.to_bytes().unwrap()
    }

    fn edns_cookie_query() -> Vec<u8> {
        let mut m = Message::from_bytes(&query("example.com.", RecordType::A)).unwrap();
        let mut e = Edns::new();
        e.set_max_payload(4096).set_dnssec_ok(true);
        e.options_mut()
            .insert(hickory_proto::rr::rdata::opt::EdnsOption::Unknown(
                10,
                vec![1, 2, 3, 4, 5, 6, 7, 8],
            ));
        m.set_edns(e);
        m.to_bytes().unwrap()
    }

    #[test]
    fn parses_simple_query_and_lowercases_key() {
        let q = query("WwW.Example.COM.", RecordType::AAAA);
        let v = parse_query(&q).unwrap();
        assert_eq!(v.id, 0xbeef);
        assert_eq!(v.qtype, 28);
        assert_eq!(v.qclass, 1);
        assert_eq!(v.key.as_wire(), b"\x03www\x07example\x03com\x00");
        assert_eq!(&v.qname[1..4], b"WwW");
        assert!(v.rd());
        assert!(v.opt.is_none());
    }

    #[test]
    fn short_packet_is_too_short() {
        assert_eq!(parse_query(&[0u8; 11]).unwrap_err(), ParseError::TooShort);
    }

    #[test]
    fn response_bit_is_rejected() {
        let mut q = query("a.", RecordType::A);
        q[2] |= 0x80;
        assert_eq!(parse_query(&q).unwrap_err(), ParseError::IsResponse);
    }

    #[test]
    fn unknown_opcode_is_notimp() {
        let mut q = query("a.", RecordType::A);
        q[2] = (q[2] & 0x87) | (4 << 3); // NOTIFY
        assert_eq!(parse_query(&q).unwrap_err(), ParseError::NotImp);
    }

    #[test]
    fn compression_pointer_in_question_is_formerr() {
        let mut q = vec![0xbe, 0xef, 0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 0];
        q.extend_from_slice(&[0xc0, 0x0c, 0, 1, 0, 1]);
        assert_eq!(parse_query(&q).unwrap_err(), ParseError::FormErr);
    }

    #[test]
    fn label_over_63_and_name_over_255_are_formerr() {
        let mut q = vec![0xbe, 0xef, 0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 0, 64];
        q.extend_from_slice(&[b'a'; 64]);
        q.extend_from_slice(&[0, 0, 1, 0, 1]);
        assert_eq!(parse_query(&q).unwrap_err(), ParseError::FormErr);
        let mut q = vec![0xbe, 0xef, 0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 0];
        for _ in 0..5 {
            q.push(63);
            q.extend_from_slice(&[b'b'; 63]);
        }
        q.extend_from_slice(&[0, 0, 1, 0, 1]);
        assert_eq!(parse_query(&q).unwrap_err(), ParseError::FormErr);
    }

    #[test]
    fn counts_exceeding_packet_are_formerr() {
        let mut q = query("a.", RecordType::A);
        q[11] = 1; // ARCOUNT=1 but no additional record present
        assert_eq!(parse_query(&q).unwrap_err(), ParseError::FormErr);
        let mut q = query("a.", RecordType::A);
        q[5] = 2; // QDCOUNT=2
        assert_eq!(parse_query(&q).unwrap_err(), ParseError::FormErr);
    }

    #[test]
    fn escaped_dot_and_binary_label_round_trip_in_key() {
        let mut q = vec![0xbe, 0xef, 0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 0];
        q.extend_from_slice(&[3, b'a', b'.', 0xff, 3, b'c', b'o', b'm', 0, 0, 1, 0, 1]);
        let v = parse_query(&q).unwrap();
        assert_eq!(
            v.key.as_wire(),
            &[3, b'a', b'.', 0xff, 3, b'c', b'o', b'm', 0]
        );
    }

    #[test]
    fn edns_opt_with_cookie_is_parsed() {
        let bytes = edns_cookie_query();
        let v = parse_query(&bytes).unwrap();
        let opt = v.opt.unwrap();
        assert_eq!(opt.udp_size, 4096);
        assert!(opt.do_bit);
        assert_eq!(opt.client_cookie, Some([1, 2, 3, 4, 5, 6, 7, 8]));
    }

    #[test]
    fn formerr_reply_echoes_id() {
        let mut q = query("a.", RecordType::A);
        q[5] = 2;
        let mut out = [0u8; 512];
        let n = write_error_reply(&q, 1, &mut out).unwrap();
        assert!(n >= 12);
        assert_eq!(&out[0..2], &[0xbe, 0xef]);
        assert_eq!(out[2] & 0x80, 0x80);
        assert_eq!(out[3] & 0x0f, 1);
        assert_eq!(write_error_reply(&q[..11], 1, &mut out), None);
    }

    #[test]
    fn walk_response_collects_ttl_offsets_and_negative_ttl() {
        use hickory_proto::rr::rdata::SOA;
        use hickory_proto::rr::{RData, Record};
        let qb = query("nx.example.com.", RecordType::A);
        let qv = parse_query(&qb).unwrap();
        let mut r = Message::from_bytes(&qb).unwrap();
        r.metadata.message_type = MessageType::Response;
        r.metadata.response_code = ResponseCode::NXDomain;
        let soa = SOA::new(
            Name::from_ascii("ns.example.com.").unwrap(),
            Name::from_ascii("h.example.com.").unwrap(),
            1,
            2,
            3,
            4,
            300,
        );
        r.add_authority(Record::from_rdata(
            Name::from_ascii("example.com.").unwrap(),
            900,
            RData::SOA(soa),
        ));
        let bytes = r.to_bytes().unwrap();
        let info = walk_response(&bytes, &qv).unwrap();
        assert_eq!(info.rcode, 3);
        assert_eq!(info.negative_ttl, Some(300));
        assert_eq!(info.ttl_offsets.len(), 1);
        let off = info.ttl_offsets[0] as usize;
        assert_eq!(
            u32::from_be_bytes(bytes[off..off + 4].try_into().unwrap()),
            900
        );
    }

    #[test]
    fn walk_response_rejects_pointer_loop() {
        let qb = query("a.", RecordType::A);
        let qv = parse_query(&qb).unwrap();
        let mut r = qb.clone();
        r[2] |= 0x80;
        r[7] = 1; // ANCOUNT=1
        let loop_at = r.len();
        r.extend_from_slice(&[
            0xc0,
            loop_at as u8,
            0,
            1,
            0,
            1,
            0,
            0,
            0,
            60,
            0,
            4,
            1,
            2,
            3,
            4,
        ]);
        assert_eq!(walk_response(&r, &qv).unwrap_err(), ParseError::FormErr);
    }

    #[test]
    #[ignore]
    fn write_fuzz_seeds() {
        let dir = concat!(env!("CARGO_MANIFEST_DIR"), "/fuzz/corpus/parse_query/");
        std::fs::create_dir_all(dir).unwrap();
        let mut compressed = vec![0xbe, 0xef, 0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 0];
        compressed.extend_from_slice(&[0xc0, 0x0c, 0, 1, 0, 1]);
        let mut long = vec![0xbe, 0xef, 0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 0];
        for _ in 0..3 {
            long.push(63);
            long.extend_from_slice(&[b'l'; 63]);
        }
        long.push(61);
        long.extend_from_slice(&[b'l'; 61]);
        long.extend_from_slice(&[0, 0, 1, 0, 1]);
        assert_eq!(parse_query(&long).unwrap().key.as_wire().len(), 255);
        for (file, bytes) in [
            ("a.bin", query("example.com.", RecordType::A)),
            ("edns_cookie.bin", edns_cookie_query()),
            ("compressed_name.bin", compressed),
            ("long_name.bin", long),
        ] {
            std::fs::write(format!("{dir}{file}"), bytes).unwrap();
        }
    }
}
