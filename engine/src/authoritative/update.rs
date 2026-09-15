//! RFC 2136 UPDATE received for hosted primary zones: parsed and authenticated with TSIG here,
//! forwarded to the management plane (which checks prerequisites and applies it), and answered
//! with the management plane's rcode, signed with the request's key.

use super::T_SOA;
use super::msg::{OPCODE_UPDATE, Question};
use super::name::{lowercase_into, to_ascii};
use super::notify_in::reply;
use super::set::AuthSet;
use crate::proto;
use crate::runtime::Runtime;
use crate::tsig::{self, KeyRing, Verified};
use crate::wire;
use std::future::Future;
use std::net::SocketAddr;
use std::ops::Deref;
use std::pin::Pin;
use std::sync::Arc;

const CLASS_IN: u16 = 1;
const FLAG_QR: u8 = 0x80;

/// `AuthCounters::updates` slots: the management plane answered NOERROR, answered another rcode,
/// the engine refused before forwarding (format, TSIG, zone, source or key policy), or no result
/// came.
pub const UPDATE_APPLIED: usize = 0;
pub const UPDATE_REJECTED: usize = 1;
pub const UPDATE_REFUSED: usize = 2;
pub const UPDATE_FAILED: usize = 3;

/// Sends an update to the management plane and waits for its result.
pub trait UpdateForwarder: Send + Sync {
    /// `None`: disconnected, queue full, or no result within the timeout.
    fn forward(
        &self,
        req: proto::UpdateRequest,
    ) -> Pin<Box<dyn Future<Output = Option<proto::UpdateResult>> + Send + '_>>;
}

/// Answers one UPDATE against the runtime's hosted zones; returns the response and its
/// `AuthCounters::updates` slot.
pub async fn handle_update(
    raw: Vec<u8>,
    client: SocketAddr,
    rt: Arc<Runtime>,
    ring: Arc<KeyRing>,
    fwd: Arc<dyn UpdateForwarder>,
    now: u64,
) -> (Vec<u8>, usize) {
    respond(raw, client, &rt.auth, &ring, &*fwd, now).await
}

/// [`handle_update`] over an explicit set, without the counter slot.
pub async fn handle_update_with_set(
    raw: Vec<u8>,
    client: SocketAddr,
    set: impl Deref<Target = AuthSet>,
    ring: Arc<KeyRing>,
    fwd: Arc<dyn UpdateForwarder>,
    now: u64,
) -> Vec<u8> {
    respond(raw, client, &set, &ring, &*fwd, now).await.0
}

async fn respond(
    raw: Vec<u8>,
    client: SocketAddr,
    set: &AuthSet,
    ring: &KeyRing,
    fwd: &dyn UpdateForwarder,
    now: u64,
) -> (Vec<u8>, usize) {
    let q = match Question::parse(&raw) {
        Ok(q) if q.opcode == OPCODE_UPDATE && q.qtype == T_SOA && q.qclass == CLASS_IN => q,
        _ => {
            let mut out = vec![0u8; raw.len().clamp(12, 512)];
            let n = wire::write_error_reply(&raw, wire::RCODE_FORMERR, &mut out).unwrap_or(0);
            out.truncate(n);
            return (out, UPDATE_REFUSED);
        }
    };
    let (key, mac) = match tsig::verify_request(&raw, ring, now) {
        Ok(Verified::Signed { key, request_mac }) => (Some(key), request_mac),
        Ok(Verified::Unsigned) => (None, Vec::new()),
        Err(failure) => return (tsig::error_response(&raw, &failure, now), UPDATE_REFUSED),
    };
    let signing = key.as_deref().map(|k| (k, &mac[..]));
    let answer = |rcode: u8| reply(&raw, &q, FLAG_QR, rcode, signing, now);
    let mut buf = [0u8; 255];
    let zname = lowercase_into(q.qname, &mut buf);
    let Some(zone) = set.get(zname) else {
        return (answer(wire::RCODE_NOTAUTH), UPDATE_REFUSED);
    };
    if zone
        .update_allow
        .as_ref()
        .is_some_and(|a| !a.allows(client.ip()))
    {
        return (answer(wire::RCODE_REFUSED), UPDATE_REFUSED);
    }
    let Some(key) = key.as_deref() else {
        return (answer(wire::RCODE_REFUSED), UPDATE_REFUSED);
    };
    let key_wire = key.lower_wire_name();
    if zone.secondary || !zone.update_keys.iter().any(|k| **k == key_wire[..]) {
        return (answer(wire::RCODE_REFUSED), UPDATE_REFUSED);
    }
    let req = proto::UpdateRequest {
        request_id: hex::encode(rand::random::<[u8; 16]>()),
        zone: to_ascii(zname),
        client: client.to_string(),
        message: raw.clone(),
        tsig_key: to_ascii(&key_wire),
    };
    match fwd.forward(req).await {
        None => (answer(wire::RCODE_SERVFAIL), UPDATE_FAILED),
        Some(r) => {
            let rcode = u8::try_from(r.rcode)
                .ok()
                .filter(|&c| c <= 0x0f)
                .unwrap_or(wire::RCODE_SERVFAIL);
            let slot = if rcode == wire::RCODE_NOERROR {
                UPDATE_APPLIED
            } else {
                UPDATE_REJECTED
            };
            (answer(rcode), slot)
        }
    }
}
