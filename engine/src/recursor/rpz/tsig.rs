//! RFC 8945 TSIG over raw wire messages: request signing and (multi-message) response
//! verification with HMAC-SHA256/512.

use hickory_proto::rr::Name;
use hickory_proto::serialize::binary::{BinDecodable, BinDecoder, BinEncodable};
use ring::hmac;
use zeroize::Zeroizing;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum TsigAlg {
    HmacSha256,
    HmacSha512,
}

impl TsigAlg {
    pub fn name(self) -> &'static str {
        match self {
            TsigAlg::HmacSha256 => "hmac-sha256.",
            TsigAlg::HmacSha512 => "hmac-sha512.",
        }
    }

    fn ring(self) -> hmac::Algorithm {
        match self {
            TsigAlg::HmacSha256 => hmac::HMAC_SHA256,
            TsigAlg::HmacSha512 => hmac::HMAC_SHA512,
        }
    }
}

#[derive(Clone)]
pub struct TsigKey {
    pub name: Name,
    pub alg: TsigAlg,
    pub secret: Zeroizing<Vec<u8>>,
}

impl std::fmt::Debug for TsigKey {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("TsigKey")
            .field("name", &self.name)
            .field("alg", &self.alg)
            .field("secret", &"<redacted>")
            .finish()
    }
}

#[derive(Debug, PartialEq, Eq)]
pub enum TsigError {
    Missing,
    BadKey,
    BadSig,
    BadTime,
    Malformed,
    TooManyUnsigned,
}

pub const FUDGE: u16 = 300;
const TYPE_TSIG: u16 = 250;
const CLASS_ANY: u16 = 255;
/// RFC 8945 §5.3.1: at most 99 unsigned messages between signed ones.
const MAX_UNSIGNED: u32 = 99;

pub fn skip_name(b: &[u8], mut i: usize) -> Option<usize> {
    loop {
        let len = *b.get(i)? as usize;
        if len & 0xC0 == 0xC0 {
            b.get(i + 1)?;
            return Some(i + 2);
        }
        if len & 0xC0 != 0 {
            return None;
        }
        i += 1;
        if len == 0 {
            return Some(i);
        }
        i += len;
        if i > b.len() {
            return None;
        }
    }
}

/// Offset of the last RR in the additional section when it is a TSIG RR.
fn tsig_offset(b: &[u8]) -> Option<usize> {
    if b.len() < 12 {
        return None;
    }
    let count = |o: usize| u16::from_be_bytes([b[o], b[o + 1]]) as usize;
    let (qd, an, ns, ar) = (count(4), count(6), count(8), count(10));
    if ar == 0 {
        return None;
    }
    let mut i = 12;
    for _ in 0..qd {
        i = skip_name(b, i)? + 4;
    }
    let mut last = i;
    for _ in 0..(an + ns + ar) {
        last = i;
        i = skip_name(b, i)?;
        let rdlen = u16::from_be_bytes([*b.get(i + 8)?, *b.get(i + 9)?]) as usize;
        i += 10 + rdlen;
        if i > b.len() {
            return None;
        }
    }
    let t = skip_name(b, last)?;
    (u16::from_be_bytes([*b.get(t)?, *b.get(t + 1)?]) == TYPE_TSIG).then_some(last)
}

struct ParsedTsig {
    owner: Name,
    alg: Name,
    time_signed: u64,
    fudge: u16,
    mac: Vec<u8>,
    original_id: u16,
    error: u16,
    other: Vec<u8>,
}

fn parse_tsig(b: &[u8], off: usize) -> Option<ParsedTsig> {
    let mut d = BinDecoder::new(b);
    d.read_slice(off).ok()?;
    let owner = Name::read(&mut d).ok()?;
    let fixed = d.read_slice(10).ok()?.unverified();
    let rdlen = u16::from_be_bytes([fixed[8], fixed[9]]) as usize;
    let rdata_start = d.index();
    let alg = Name::read(&mut d).ok()?;
    let rd = d.read_slice(10).ok()?.unverified();
    let time_signed = u64::from_be_bytes([0, 0, rd[0], rd[1], rd[2], rd[3], rd[4], rd[5]]);
    let fudge = u16::from_be_bytes([rd[6], rd[7]]);
    let mac_len = u16::from_be_bytes([rd[8], rd[9]]) as usize;
    let mac = d.read_slice(mac_len).ok()?.unverified().to_vec();
    let tail = d.read_slice(6).ok()?.unverified();
    let original_id = u16::from_be_bytes([tail[0], tail[1]]);
    let error = u16::from_be_bytes([tail[2], tail[3]]);
    let other_len = u16::from_be_bytes([tail[4], tail[5]]) as usize;
    let other = d.read_slice(other_len).ok()?.unverified().to_vec();
    (d.index() - rdata_start == rdlen && d.index() == b.len()).then_some(ParsedTsig {
        owner,
        alg,
        time_signed,
        fudge,
        mac,
        original_id,
        error,
        other,
    })
}

/// MAC field of a trailing TSIG RR.
pub fn extract_mac(wire: &[u8]) -> Option<Vec<u8>> {
    parse_tsig(wire, tsig_offset(wire)?).map(|t| t.mac)
}

fn name_wire(n: &Name) -> Vec<u8> {
    n.to_lowercase().to_bytes().expect("a parsed name encodes")
}

fn variables(
    key: &TsigKey,
    time_signed: u64,
    error: u16,
    other: &[u8],
    timers_only: bool,
) -> Vec<u8> {
    let mut v = Vec::new();
    if !timers_only {
        v.extend(name_wire(&key.name));
        v.extend(CLASS_ANY.to_be_bytes());
        v.extend(0u32.to_be_bytes()); // TTL
        v.extend(name_wire(
            &Name::from_ascii(key.alg.name()).expect("static name"),
        ));
    }
    v.extend(&time_signed.to_be_bytes()[2..]); // 48-bit time
    v.extend(FUDGE.to_be_bytes());
    if !timers_only {
        v.extend(error.to_be_bytes());
        v.extend((other.len() as u16).to_be_bytes());
        v.extend(other);
    }
    v
}

fn append_tsig(wire: &mut Vec<u8>, key: &TsigKey, time_signed: u64, mac: &[u8], original_id: u16) {
    wire.extend(name_wire(&key.name));
    wire.extend(TYPE_TSIG.to_be_bytes());
    wire.extend(CLASS_ANY.to_be_bytes());
    wire.extend(0u32.to_be_bytes());
    let mut rdata = name_wire(&Name::from_ascii(key.alg.name()).expect("static name"));
    rdata.extend(&time_signed.to_be_bytes()[2..]);
    rdata.extend(FUDGE.to_be_bytes());
    rdata.extend((mac.len() as u16).to_be_bytes());
    rdata.extend(mac);
    rdata.extend(original_id.to_be_bytes());
    rdata.extend(0u16.to_be_bytes()); // error
    rdata.extend(0u16.to_be_bytes()); // other len
    wire.extend((rdata.len() as u16).to_be_bytes());
    wire.extend(rdata);
    let ar = u16::from_be_bytes([wire[10], wire[11]]).wrapping_add(1);
    wire[10..12].copy_from_slice(&ar.to_be_bytes());
}

fn mac(key: &TsigKey, input: &[u8]) -> Vec<u8> {
    hmac::sign(&hmac::Key::new(key.alg.ring(), &key.secret), input)
        .as_ref()
        .to_vec()
}

/// Signs a request in place (appends the TSIG RR) and returns its MAC.
pub fn sign_request(wire: &mut Vec<u8>, key: &TsigKey, time_signed: u64) -> Vec<u8> {
    let mut input = wire.clone();
    input.extend(variables(key, time_signed, 0, &[], false));
    let m = mac(key, &input);
    let id = u16::from_be_bytes([wire[0], wire[1]]);
    append_tsig(wire, key, time_signed, &m, id);
    m
}

/// RFC 8945 §5.3 response MAC input; `message` is the message without its TSIG RR, ARCOUNT
/// already excluding it and the ID set to the original ID.
fn response_input(
    prior_mac: &[u8],
    unsigned_before: &[u8],
    message: &[u8],
    variables: &[u8],
) -> Zeroizing<Vec<u8>> {
    let mut input = Zeroizing::new(Vec::with_capacity(
        prior_mac.len() + unsigned_before.len() + message.len() + 64,
    ));
    if !prior_mac.is_empty() {
        input.extend((prior_mac.len() as u16).to_be_bytes());
        input.extend(prior_mac);
    }
    input.extend(unsigned_before);
    input.extend(message);
    input.extend(variables);
    input
}

/// Signs a response in place (used by test primaries) and returns its MAC.
pub fn sign_response(
    wire: &mut Vec<u8>,
    key: &TsigKey,
    time_signed: u64,
    prior_mac: &[u8],
    first: bool,
    unsigned_before: &[u8],
) -> Vec<u8> {
    let vars = variables(key, time_signed, 0, &[], !first);
    let input = response_input(prior_mac, unsigned_before, wire, &vars);
    let m = mac(key, &input);
    let id = u16::from_be_bytes([wire[0], wire[1]]);
    append_tsig(wire, key, time_signed, &m, id);
    m
}

/// Verifies the messages of one response stream in order.
pub struct TsigVerifier {
    key: TsigKey,
    prior_mac: Vec<u8>,
    first: bool,
    unsigned: Vec<u8>,
    unsigned_count: u32,
}

impl TsigVerifier {
    pub fn new(key: TsigKey, request_mac: Vec<u8>) -> Self {
        TsigVerifier {
            key,
            prior_mac: request_mac,
            first: true,
            unsigned: Vec::new(),
            unsigned_count: 0,
        }
    }

    pub fn verify(&mut self, wire: &[u8], now: u64) -> Result<(), TsigError> {
        let Some(off) = tsig_offset(wire) else {
            if self.first {
                return Err(TsigError::Missing);
            }
            self.unsigned.extend_from_slice(wire);
            self.unsigned_count += 1;
            if self.unsigned_count > MAX_UNSIGNED {
                return Err(TsigError::TooManyUnsigned);
            }
            return Ok(());
        };
        let t = parse_tsig(wire, off).ok_or(TsigError::Malformed)?;
        let alg = Name::from_ascii(self.key.alg.name()).expect("static name");
        if t.owner.to_lowercase() != self.key.name.to_lowercase() || t.alg.to_lowercase() != alg {
            return Err(TsigError::BadKey);
        }
        match t.error {
            0 => {}
            16 => return Err(TsigError::BadSig),
            17 => return Err(TsigError::BadKey),
            18 => return Err(TsigError::BadTime),
            _ => return Err(TsigError::Malformed),
        }
        let mut message = wire[..off].to_vec();
        message[0..2].copy_from_slice(&t.original_id.to_be_bytes());
        let ar = u16::from_be_bytes([message[10], message[11]]) - 1;
        message[10..12].copy_from_slice(&ar.to_be_bytes());
        let vars = variables(&self.key, t.time_signed, t.error, &t.other, !self.first);
        let input = response_input(&self.prior_mac, &self.unsigned, &message, &vars);
        let key = hmac::Key::new(self.key.alg.ring(), &self.key.secret);
        hmac::verify(&key, &input, &t.mac).map_err(|_| TsigError::BadSig)?;
        if now.abs_diff(t.time_signed) > u64::from(t.fudge) {
            return Err(TsigError::BadTime);
        }
        self.prior_mac = t.mac;
        self.first = false;
        self.unsigned.clear();
        self.unsigned_count = 0;
        Ok(())
    }

    /// The stream must end on a signed message.
    pub fn finish(&self) -> Result<(), TsigError> {
        if self.first || self.unsigned_count > 0 {
            Err(TsigError::Missing)
        } else {
            Ok(())
        }
    }
}
