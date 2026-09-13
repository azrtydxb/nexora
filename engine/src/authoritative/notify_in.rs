//! NOTIFY received for hosted secondary zones (RFC 1996): the source (and TSIG, when present) is
//! checked, the NOTIFY is answered and forwarded to the management plane, which pulls the zone.

use super::T_SOA;
use super::msg::{self, OPCODE_NOTIFY, Question};
use super::name::{lowercase_into, to_ascii};
use super::set::AuthSet;
use crate::proto;
use crate::tsig::{self, KeyRing, TsigKey, Verified};
use crate::wire;
use std::net::SocketAddr;

const HEADER_LEN: usize = 12;
const CLASS_IN: u16 = 1;
const FLAG_QR: u8 = 0x80;
const FLAG_AA: u8 = 0x04;

/// Receives accepted NOTIFYs; `false` when the event could not be queued (disconnected or full).
pub trait NotifySink: Send + Sync {
    fn notify(&self, ev: proto::NotifyReceived) -> bool;
}

/// Answers one NOTIFY request and forwards it to `sink` when accepted.
pub fn handle_notify(
    raw: &[u8],
    client: SocketAddr,
    set: &AuthSet,
    ring: &KeyRing,
    sink: &dyn NotifySink,
    now: u64,
) -> Vec<u8> {
    let q = match Question::parse(raw) {
        Ok(q) if q.opcode == OPCODE_NOTIFY && q.qtype == T_SOA && q.qclass == CLASS_IN => q,
        _ => return formerr(raw),
    };
    let signed = match tsig::verify_request(raw, ring, now) {
        Ok(Verified::Unsigned) => None,
        Ok(Verified::Signed { key, request_mac }) => Some((key, request_mac)),
        Err(failure) => return tsig::error_response(raw, &failure, now),
    };
    let signing = signed.as_ref().map(|(k, m)| (&**k, &m[..]));
    let mut buf = [0u8; 255];
    let lower = lowercase_into(q.qname, &mut buf);
    let Some(zone) = set.get(lower) else {
        return reply(raw, &q, FLAG_QR, wire::RCODE_NOTAUTH, signing, now);
    };
    // A primary with a configured key accepts only NOTIFYs signed with that key; one without a key
    // accepts its address alone.
    let signer = signed.as_ref().map(|(k, _)| k.lower_wire_name());
    let from_primary = zone.primaries.iter().any(|(addr, key)| {
        addr.ip() == client.ip()
            && key
                .as_deref()
                .is_none_or(|want| signer.as_deref() == Some(want))
    });
    if !zone.secondary || !from_primary {
        return reply(raw, &q, FLAG_QR, wire::RCODE_REFUSED, signing, now);
    }
    let serial = msg::walk_rrs(raw, q.question_end, usize::from(be16(raw, 6)))
        .ok()
        .and_then(|(rrs, _)| {
            rrs.into_iter()
                .find(|r| r.rtype == T_SOA && r.rdata.len() >= 22)
                .map(|r| {
                    let s = r.rdata.end - 20;
                    u32::from_be_bytes([raw[s], raw[s + 1], raw[s + 2], raw[s + 3]])
                })
        });
    sink.notify(proto::NotifyReceived {
        zone: to_ascii(lower),
        source: client.to_string(),
        serial: serial.unwrap_or(0),
        has_serial: serial.is_some(),
    });
    reply(
        raw,
        &q,
        FLAG_QR | FLAG_AA,
        wire::RCODE_NOERROR,
        signing,
        now,
    )
}

fn be16(b: &[u8], at: usize) -> u16 {
    u16::from_be_bytes([b[at], b[at + 1]])
}

fn formerr(raw: &[u8]) -> Vec<u8> {
    let mut out = vec![0u8; raw.len().clamp(HEADER_LEN, 512)];
    match wire::write_error_reply(raw, wire::RCODE_FORMERR, &mut out) {
        Some(n) => {
            out.truncate(n);
            out
        }
        None => Vec::new(),
    }
}

/// A response echoing ID, opcode and the question with `flags` (QR, AA) and `rcode`, other
/// sections empty; signed with the request MAC when `signing` is set.
pub(super) fn reply(
    raw: &[u8],
    q: &Question<'_>,
    flags: u8,
    rcode: u8,
    signing: Option<(&TsigKey, &[u8])>,
    now: u64,
) -> Vec<u8> {
    let mut out = Vec::with_capacity(q.question_end + 256);
    out.extend_from_slice(&raw[..2]);
    out.push(flags | (q.opcode << 3));
    out.push(rcode & 0x0f);
    out.extend_from_slice(&[0, 1, 0, 0, 0, 0, 0, 0]);
    out.extend_from_slice(&raw[HEADER_LEN..q.question_end]);
    if let Some((key, mac)) = signing {
        tsig::sign_response(&mut out, key, now, mac, true, &[]);
    }
    out
}
