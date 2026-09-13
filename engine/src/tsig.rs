//! RFC 8945 TSIG over raw wire messages with HMAC-SHA256/384/512: request signing and
//! (multi-message) response verification for M3's RPZ transfers, and the server side for hosted
//! zones: request verification, response and stream signing, TSIG error responses and the
//! in-memory key ring fed by `KeyMaterial`.

use crate::proto;
use arc_swap::ArcSwap;
use hickory_proto::rr::Name;
use hickory_proto::serialize::binary::{BinDecodable, BinDecoder, BinEncodable};
use ring::hmac;
use rustc_hash::FxHashMap;
use std::sync::Arc;
use zeroize::Zeroizing;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum TsigAlg {
    HmacSha256,
    HmacSha384,
    HmacSha512,
}

impl TsigAlg {
    pub fn name(self) -> &'static str {
        match self {
            TsigAlg::HmacSha256 => "hmac-sha256.",
            TsigAlg::HmacSha384 => "hmac-sha384.",
            TsigAlg::HmacSha512 => "hmac-sha512.",
        }
    }

    fn ring(self) -> hmac::Algorithm {
        match self {
            TsigAlg::HmacSha256 => hmac::HMAC_SHA256,
            TsigAlg::HmacSha384 => hmac::HMAC_SHA384,
            TsigAlg::HmacSha512 => hmac::HMAC_SHA512,
        }
    }

    /// Full MAC length in octets.
    pub fn digest_len(self) -> usize {
        match self {
            TsigAlg::HmacSha256 => 32,
            TsigAlg::HmacSha384 => 48,
            TsigAlg::HmacSha512 => 64,
        }
    }

    /// `proto::TsigAlgorithm` as i32; `None` for NONE and unknown values.
    pub fn from_proto(a: i32) -> Option<TsigAlg> {
        match proto::TsigAlgorithm::try_from(a) {
            Ok(proto::TsigAlgorithm::HmacSha256) => Some(TsigAlg::HmacSha256),
            Ok(proto::TsigAlgorithm::HmacSha384) => Some(TsigAlg::HmacSha384),
            Ok(proto::TsigAlgorithm::HmacSha512) => Some(TsigAlg::HmacSha512),
            _ => None,
        }
    }
}

#[derive(Clone)]
pub struct TsigKey {
    pub name: Name,
    pub alg: TsigAlg,
    pub secret: Zeroizing<Vec<u8>>,
}

impl TsigKey {
    /// The key name as lowercase uncompressed wire octets.
    pub fn lower_wire_name(&self) -> Vec<u8> {
        name_wire(&self.name)
    }
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
const HEADER_LEN: usize = 12;
const RCODE_FORMERR: u8 = 1;
const RCODE_NOTAUTH: u8 = 9;
pub const TSIG_BADSIG: u16 = 16;
pub const TSIG_BADKEY: u16 = 17;
pub const TSIG_BADTIME: u16 = 18;

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

/// A TSIG RR located in a message.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct TsigRecord {
    /// Offset of the RR's owner name.
    pub start: usize,
    pub key_name: Name,
    pub alg_name: Name,
    pub time_signed: u64,
    pub fudge: u16,
    pub mac: Vec<u8>,
    pub original_id: u16,
    pub error: u16,
    pub other: Vec<u8>,
}

fn parse_tsig(b: &[u8], off: usize) -> Option<TsigRecord> {
    let mut d = BinDecoder::new(b);
    d.read_slice(off).ok()?;
    let key_name = Name::read(&mut d).ok()?;
    let fixed = d.read_slice(10).ok()?.unverified();
    let rdlen = u16::from_be_bytes([fixed[8], fixed[9]]) as usize;
    let rdata_start = d.index();
    let alg_name = Name::read(&mut d).ok()?;
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
    (d.index() - rdata_start == rdlen && d.index() == b.len()).then_some(TsigRecord {
        start: off,
        key_name,
        alg_name,
        time_signed,
        fudge,
        mac,
        original_id,
        error,
        other,
    })
}

/// The TSIG RR of `msg`, if any. `TsigError::Malformed` when the message does not walk, a TSIG
/// RR is not the last RR of the additional section, appears twice, has class ≠ ANY, or does
/// not parse.
pub fn find_tsig(msg: &[u8]) -> Result<Option<TsigRecord>, TsigError> {
    if msg.len() < HEADER_LEN {
        return Err(TsigError::Malformed);
    }
    let count = |o: usize| u16::from_be_bytes([msg[o], msg[o + 1]]) as usize;
    let (qd, an, ns, ar) = (count(4), count(6), count(8), count(10));
    let mut i = HEADER_LEN;
    for _ in 0..qd {
        i = skip_name(msg, i).ok_or(TsigError::Malformed)? + 4;
    }
    let total = an + ns + ar;
    let mut found = None;
    for n in 0..total {
        let start = i;
        i = skip_name(msg, i).ok_or(TsigError::Malformed)?;
        let fixed = msg.get(i..i + 10).ok_or(TsigError::Malformed)?;
        let rtype = u16::from_be_bytes([fixed[0], fixed[1]]);
        let class = u16::from_be_bytes([fixed[2], fixed[3]]);
        let rdlen = u16::from_be_bytes([fixed[8], fixed[9]]) as usize;
        i += 10 + rdlen;
        if i > msg.len() {
            return Err(TsigError::Malformed);
        }
        if rtype == TYPE_TSIG {
            if found.is_some() || n + 1 != total || ar == 0 || class != CLASS_ANY {
                return Err(TsigError::Malformed);
            }
            found = Some(start);
        }
    }
    if i > msg.len() {
        return Err(TsigError::Malformed);
    }
    match found {
        None => Ok(None),
        Some(start) => parse_tsig(msg, start).map(Some).ok_or(TsigError::Malformed),
    }
}

/// MAC field of a trailing TSIG RR.
pub fn extract_mac(wire: &[u8]) -> Option<Vec<u8>> {
    parse_tsig(wire, tsig_offset(wire)?).map(|t| t.mac)
}

fn name_wire(n: &Name) -> Vec<u8> {
    n.to_lowercase().to_bytes().expect("a parsed name encodes")
}

fn alg_wire(alg: TsigAlg) -> Vec<u8> {
    name_wire(&Name::from_ascii(alg.name()).expect("static name"))
}

/// RFC 8945 §4.3.3 TSIG variables (or §5.3.1 timers only).
fn variables(
    key: &TsigKey,
    time_signed: u64,
    fudge: u16,
    error: u16,
    other: &[u8],
    timers_only: bool,
) -> Vec<u8> {
    let mut v = Vec::new();
    if !timers_only {
        v.extend(name_wire(&key.name));
        v.extend(CLASS_ANY.to_be_bytes());
        v.extend(0u32.to_be_bytes()); // TTL
        v.extend(alg_wire(key.alg));
    }
    v.extend(&time_signed.to_be_bytes()[2..]); // 48-bit time
    v.extend(fudge.to_be_bytes());
    if !timers_only {
        v.extend(error.to_be_bytes());
        v.extend((other.len() as u16).to_be_bytes());
        v.extend(other);
    }
    v
}

#[allow(clippy::too_many_arguments)]
fn append_tsig_rr(
    wire: &mut Vec<u8>,
    key_name: &[u8],
    alg_name: &[u8],
    time_signed: u64,
    mac: &[u8],
    original_id: u16,
    error: u16,
    other: &[u8],
) {
    wire.extend(key_name);
    wire.extend(TYPE_TSIG.to_be_bytes());
    wire.extend(CLASS_ANY.to_be_bytes());
    wire.extend(0u32.to_be_bytes());
    let mut rdata = alg_name.to_vec();
    rdata.extend(&time_signed.to_be_bytes()[2..]);
    rdata.extend(FUDGE.to_be_bytes());
    rdata.extend((mac.len() as u16).to_be_bytes());
    rdata.extend(mac);
    rdata.extend(original_id.to_be_bytes());
    rdata.extend(error.to_be_bytes());
    rdata.extend((other.len() as u16).to_be_bytes());
    rdata.extend(other);
    wire.extend((rdata.len() as u16).to_be_bytes());
    wire.extend(rdata);
    let ar = u16::from_be_bytes([wire[10], wire[11]]).wrapping_add(1);
    wire[10..12].copy_from_slice(&ar.to_be_bytes());
}

fn append_tsig(
    wire: &mut Vec<u8>,
    key: &TsigKey,
    time_signed: u64,
    mac: &[u8],
    error: u16,
    other: &[u8],
) {
    let id = u16::from_be_bytes([wire[0], wire[1]]);
    append_tsig_rr(
        wire,
        &name_wire(&key.name),
        &alg_wire(key.alg),
        time_signed,
        mac,
        id,
        error,
        other,
    );
}

fn mac(key: &TsigKey, input: &[u8]) -> Vec<u8> {
    hmac::sign(&hmac::Key::new(key.alg.ring(), &key.secret), input)
        .as_ref()
        .to_vec()
}

/// Signs a request in place (appends the TSIG RR) and returns its MAC.
pub fn sign_request(wire: &mut Vec<u8>, key: &TsigKey, time_signed: u64) -> Vec<u8> {
    let mut input = wire.clone();
    input.extend(variables(key, time_signed, FUDGE, 0, &[], false));
    let m = mac(key, &input);
    append_tsig(wire, key, time_signed, &m, 0, &[]);
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

/// Signs a response in place and returns its MAC. `first`: the first message of a response
/// (full variables); later messages of a stream cover the timers only.
pub fn sign_response(
    wire: &mut Vec<u8>,
    key: &TsigKey,
    time_signed: u64,
    prior_mac: &[u8],
    first: bool,
    unsigned_before: &[u8],
) -> Vec<u8> {
    let vars = variables(key, time_signed, FUDGE, 0, &[], !first);
    let input = response_input(prior_mac, unsigned_before, wire, &vars);
    let m = mac(key, &input);
    append_tsig(wire, key, time_signed, &m, 0, &[]);
    m
}

/// Signs a (single-message) TSIG error response carrying `error` and `other` (RFC 8945 §5.2.3
/// BADTIME: `other` is the server's 48-bit time); returns the MAC.
pub fn sign_response_error(
    wire: &mut Vec<u8>,
    key: &TsigKey,
    time_signed: u64,
    request_mac: &[u8],
    error: u16,
    other: &[u8],
) -> Vec<u8> {
    let vars = variables(key, time_signed, FUDGE, error, other, false);
    let input = response_input(request_mac, &[], wire, &vars);
    let m = mac(key, &input);
    append_tsig(wire, key, time_signed, &m, error, other);
    m
}

/// Why a request's TSIG was not accepted.
#[derive(Debug)]
pub enum TsigFailure {
    FormErr,
    BadKey,
    BadSig,
    BadTime {
        key: Arc<TsigKey>,
        request_mac: Vec<u8>,
        time_signed: u64,
    },
}

#[derive(Debug)]
pub enum Verified {
    Unsigned,
    Signed {
        key: Arc<TsigKey>,
        request_mac: Vec<u8>,
    },
}

/// Request MAC input: the message without its TSIG RR, ARCOUNT decremented, ID = original ID,
/// then the request's TSIG variables.
fn request_input(msg: &[u8], t: &TsigRecord, key: &TsigKey) -> Zeroizing<Vec<u8>> {
    let mut m = Zeroizing::new(msg[..t.start].to_vec());
    m[0..2].copy_from_slice(&t.original_id.to_be_bytes());
    let ar = u16::from_be_bytes([m[10], m[11]]) - 1;
    m[10..12].copy_from_slice(&ar.to_be_bytes());
    m.extend(variables(
        key,
        t.time_signed,
        t.fudge,
        t.error,
        &t.other,
        false,
    ));
    m
}

/// Verifies the TSIG of a request (RFC 8945 §5.2) against `ring` at Unix time `now`.
pub fn verify_request(msg: &[u8], ring: &KeyRing, now: u64) -> Result<Verified, TsigFailure> {
    let t = match find_tsig(msg) {
        Ok(None) => return Ok(Verified::Unsigned),
        Ok(Some(t)) => t,
        Err(_) => return Err(TsigFailure::FormErr),
    };
    let key = ring
        .get(&name_wire(&t.key_name))
        .ok_or(TsigFailure::BadKey)?;
    if name_wire(&t.alg_name) != alg_wire(key.alg) {
        return Err(TsigFailure::BadKey);
    }
    if t.mac.len() != key.alg.digest_len() {
        return Err(TsigFailure::BadSig);
    }
    let input = request_input(msg, &t, &key);
    hmac::verify(&hmac::Key::new(key.alg.ring(), &key.secret), &input, &t.mac)
        .map_err(|_| TsigFailure::BadSig)?;
    if now.abs_diff(t.time_signed) > u64::from(t.fudge) {
        return Err(TsigFailure::BadTime {
            key,
            request_mac: t.mac,
            time_signed: t.time_signed,
        });
    }
    Ok(Verified::Signed {
        key,
        request_mac: t.mac,
    })
}

/// The error response to a request whose TSIG failed: FORMERR without TSIG for a malformed
/// TSIG, otherwise NOTAUTH with a TSIG RR carrying BADSIG/BADKEY (empty MAC) or a signed BADTIME.
pub fn error_response(request: &[u8], failure: &TsigFailure, now: u64) -> Vec<u8> {
    let mut out = Vec::with_capacity(512);
    if request.len() < HEADER_LEN {
        return out;
    }
    let rcode = match failure {
        TsigFailure::FormErr => RCODE_FORMERR,
        _ => RCODE_NOTAUTH,
    };
    out.extend_from_slice(&request[..2]);
    out.push(0x80 | (request[2] & 0x79)); // QR, opcode, RD
    out.push(rcode);
    out.extend_from_slice(&[0; 8]);
    let qd = u16::from_be_bytes([request[4], request[5]]);
    if qd == 1
        && let Some(end) = skip_name(request, HEADER_LEN)
        && request.get(HEADER_LEN..end + 4).is_some()
        && end - HEADER_LEN <= 255
    {
        out.extend_from_slice(&request[HEADER_LEN..end + 4]);
        out[5] = 1;
    }
    match failure {
        TsigFailure::FormErr => {}
        TsigFailure::BadKey | TsigFailure::BadSig => {
            let Ok(Some(t)) = find_tsig(request) else {
                return out;
            };
            let error = if matches!(failure, TsigFailure::BadKey) {
                TSIG_BADKEY
            } else {
                TSIG_BADSIG
            };
            append_tsig_rr(
                &mut out,
                &name_wire(&t.key_name),
                &name_wire(&t.alg_name),
                t.time_signed,
                &[],
                t.original_id,
                error,
                &[],
            );
        }
        TsigFailure::BadTime {
            key,
            request_mac,
            time_signed,
        } => {
            sign_response_error(
                &mut out,
                key,
                *time_signed,
                request_mac,
                TSIG_BADTIME,
                &now.to_be_bytes()[2..],
            );
        }
    }
    out
}

/// Signs every message of one response stream (AXFR/IXFR), chaining each MAC into the next.
pub struct StreamSigner {
    key: Arc<TsigKey>,
    prev_mac: Vec<u8>,
    first: bool,
}

impl StreamSigner {
    pub fn new(key: Arc<TsigKey>, request_mac: Vec<u8>) -> Self {
        StreamSigner {
            key,
            prev_mac: request_mac,
            first: true,
        }
    }

    pub fn sign(&mut self, msg: &mut Vec<u8>, now: u64) {
        self.prev_mac = sign_response(msg, &self.key, now, &self.prev_mac, self.first, &[]);
        self.first = false;
    }
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
        if name_wire(&t.key_name) != name_wire(&self.key.name)
            || name_wire(&t.alg_name) != alg_wire(self.key.alg)
        {
            return Err(TsigError::BadKey);
        }
        match t.error {
            0 => {}
            TSIG_BADSIG => return Err(TsigError::BadSig),
            TSIG_BADKEY => return Err(TsigError::BadKey),
            TSIG_BADTIME => return Err(TsigError::BadTime),
            _ => return Err(TsigError::Malformed),
        }
        let mut message = wire[..off].to_vec();
        message[0..2].copy_from_slice(&t.original_id.to_be_bytes());
        let ar = u16::from_be_bytes([message[10], message[11]]) - 1;
        message[10..12].copy_from_slice(&ar.to_be_bytes());
        let vars = variables(
            &self.key,
            t.time_signed,
            t.fudge,
            t.error,
            &t.other,
            !self.first,
        );
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

/// The hosted-zone TSIG keys from `ServerMessage.key_material`, held in memory only.
#[derive(Default)]
pub struct KeyRing {
    /// Keyed by lowercase wire name.
    keys: ArcSwap<FxHashMap<Box<[u8]>, Arc<TsigKey>>>,
}

impl KeyRing {
    /// Replaces the whole set; keys with an unknown algorithm or a bad name are skipped.
    pub fn apply(&self, km: proto::KeyMaterial) {
        let mut map = FxHashMap::default();
        for k in km.tsig_keys {
            let (Some(alg), Ok(name)) =
                (TsigAlg::from_proto(k.algorithm), Name::from_ascii(&k.name))
            else {
                continue;
            };
            let Ok(wire) = name.to_lowercase().to_bytes() else {
                continue;
            };
            let key = TsigKey {
                name,
                alg,
                secret: Zeroizing::new(k.secret),
            };
            map.insert(wire.into_boxed_slice(), Arc::new(key));
        }
        self.keys.store(Arc::new(map));
    }

    pub fn get(&self, lower_wire_name: &[u8]) -> Option<Arc<TsigKey>> {
        self.keys.load().get(lower_wire_name).cloned()
    }

    pub fn len(&self) -> usize {
        self.keys.load().len()
    }

    pub fn is_empty(&self) -> bool {
        self.len() == 0
    }
}
