//! Zone transfers out (RFC 5936 AXFR, RFC 1995 IXFR) over TCP and DoT: authorisation by the
//! zone's transfer ACL and TSIG key, the transfer plan from the IXFR history the snapshot lists,
//! and the (TSIG-signed) message stream. Runs off the query fast path.

use super::msg::Question;
use super::name::lowercase_into;
use super::set::AuthSet;
use super::writer::{Section, Writer};
use super::zone::{DeltaRecords, OwnedRecord, Zone};
use super::{T_AXFR, T_SOA};
use crate::runtime::Runtime;
use crate::telemetry::metrics::AuthCounters;
use crate::tsig::{self, KeyRing, StreamSigner, TsigFailure, TsigKey, Verified};
use std::net::IpAddr;
use std::sync::Arc;
use std::sync::atomic::Ordering;

/// Largest transfer message before its TSIG RR (a single larger RR gets a message of its own).
pub const MAX_XFR_MESSAGE: usize = 16384;
const HEADER_LEN: usize = 12;
const CLASS_IN: u16 = 1;
const RCODE_FORMERR: u8 = 1;
const RCODE_SERVFAIL: u8 = 2;
const RCODE_REFUSED: u8 = 5;
const RCODE_NOTAUTH: u8 = 9;

#[derive(Debug)]
pub enum Plan {
    /// A single error response with this rcode.
    Refused(u8),
    /// A TSIG error response.
    TsigError(TsigFailure),
    /// A single message with the current SOA.
    UpToDate(Arc<Zone>),
    Full(Arc<Zone>),
    /// IXFR from `deltas[index]` to the current serial.
    Incremental(Arc<Zone>, usize),
}

/// The verified request key and MAC that sign the response stream.
pub type Signing = Option<(Arc<TsigKey>, Vec<u8>)>;

/// RFC 1982: `a` is less than `b`.
fn serial_less(a: u32, b: u32) -> bool {
    const HALF: u32 = 1 << 31;
    (a < b && b - a < HALF) || (a > b && a - b > HALF)
}

/// The hosted zone whose origin is the question name.
fn find_zone(set: &AuthSet, q: &Question<'_>) -> Result<Arc<Zone>, u8> {
    let mut buf = [0u8; 255];
    let lower = lowercase_into(q.qname, &mut buf);
    match set.get(lower) {
        Some(z) if q.qclass == CLASS_IN => {
            if z.expired {
                Err(RCODE_SERVFAIL)
            } else {
                Ok(z.clone())
            }
        }
        _ => Err(RCODE_NOTAUTH),
    }
}

fn has_soa(records: &[OwnedRecord]) -> bool {
    records.iter().any(|r| r.rtype == T_SOA)
}

/// Index of the delta starting at `client` when the history from there reaches the zone serial.
fn history_from(zone: &Zone, client: u32) -> Option<usize> {
    let i = zone.deltas.iter().position(|d| d.from_serial == client)?;
    let chain = &zone.deltas[i..];
    let contiguous = chain.windows(2).all(|w| w[0].to_serial == w[1].from_serial)
        && chain.last()?.to_serial == zone.serial();
    let complete = chain
        .iter()
        .all(|d| has_soa(&d.deleted) && has_soa(&d.added));
    (contiguous && complete).then_some(i)
}

/// The plan for an authorised transfer of `zone`.
fn plan_transfer(zone: Arc<Zone>, q: &Question<'_>, tcp: bool) -> Plan {
    if q.qtype == T_AXFR {
        return if tcp {
            Plan::Full(zone)
        } else {
            Plan::Refused(RCODE_FORMERR)
        };
    }
    let Some(client) = q.ixfr_serial else {
        return Plan::Refused(RCODE_FORMERR);
    };
    if !serial_less(client, zone.serial()) {
        return Plan::UpToDate(zone);
    }
    if !tcp {
        // RFC 1995 §2: a UDP reply that does not fit is the current SOA; the client uses TCP.
        return Plan::UpToDate(zone);
    }
    match history_from(&zone, client) {
        Some(i) => Plan::Incremental(zone, i),
        // History missing: an AXFR-style full response (RFC 1995 §4).
        None => Plan::Full(zone),
    }
}

/// Authorises an AXFR/IXFR `q` (raw message `raw`) from `client` and plans the response. Order:
/// zone hosted at the question name (NOTAUTH), not expired (SERVFAIL), client inside the
/// transfer ACL (REFUSED), TSIG (a failure answers with the TSIG error; the zone's key required
/// and missing or different: REFUSED), AXFR over UDP (FORMERR).
pub fn authorize_and_plan(
    rt: &Runtime,
    ring: &KeyRing,
    raw: &[u8],
    q: &Question<'_>,
    client: IpAddr,
    tcp: bool,
    now: u64,
) -> (Plan, Signing) {
    let verified = tsig::verify_request(raw, ring, now);
    let signing: Signing = match &verified {
        Ok(Verified::Signed { key, request_mac }) => Some((key.clone(), request_mac.clone())),
        _ => None,
    };
    let zone = match find_zone(&rt.auth, q) {
        Ok(z) => z,
        Err(rcode) => return (Plan::Refused(rcode), signing),
    };
    if !zone.transfer_allow.iter().any(|n| n.contains(&client)) {
        return (Plan::Refused(RCODE_REFUSED), signing);
    }
    match verified {
        Err(failure) => return (Plan::TsigError(failure), None),
        Ok(Verified::Unsigned) if zone.transfer_key.is_some() => {
            return (Plan::Refused(RCODE_REFUSED), None);
        }
        Ok(Verified::Signed { key, .. })
            if zone
                .transfer_key
                .as_deref()
                .is_some_and(|want| *want != *key.lower_wire_name()) =>
        {
            return (Plan::Refused(RCODE_REFUSED), signing);
        }
        _ => {}
    }
    (plan_transfer(zone, q, tcp), signing)
}

/// The plan logic with an open ACL, no TSIG requirement and TCP.
#[cfg(test)]
pub fn plan_for_tests(set: &AuthSet, raw: &[u8]) -> Plan {
    let q = Question::parse(raw).expect("test query parses");
    match find_zone(set, &q) {
        Ok(z) => plan_transfer(z, &q, true),
        Err(rcode) => Plan::Refused(rcode),
    }
}

/// Counts a planned transfer in `nexora_auth_transfers_total`.
pub fn count(counters: &AuthCounters, qtype: u16, plan: &Plan) {
    let ty = usize::from(qtype != T_AXFR);
    let result = match plan {
        Plan::Full(_) => 0,
        Plan::Incremental(..) => 1,
        Plan::UpToDate(_) => 2,
        Plan::Refused(_) | Plan::TsigError(_) => 3,
    };
    counters.transfers[ty][result].fetch_add(1, Ordering::Relaxed);
}

fn apex_soa(zone: &Zone) -> OwnedRecord {
    OwnedRecord {
        owner: zone.origin().into(),
        rtype: T_SOA,
        ttl: zone.apex().get(T_SOA).map_or(0, |s| s.ttl),
        rdata: zone.soa_rdata().into(),
    }
}

/// One side of a delta with its SOA first (RFC 1995 §4 difference sequences).
fn soa_first(records: &[OwnedRecord]) -> impl Iterator<Item = &OwnedRecord> {
    let soa = records.iter().filter(|r| r.rtype == T_SOA);
    soa.chain(records.iter().filter(|r| r.rtype != T_SOA))
}

fn delta_records(d: &DeltaRecords) -> impl Iterator<Item = &OwnedRecord> {
    soa_first(&d.deleted).chain(soa_first(&d.added))
}

/// Writes one response carrying `records` in the answer section (AA, NOERROR).
fn write_message(q: &Question<'_>, records: &[&OwnedRecord]) -> Vec<u8> {
    let mut buf = vec![0u8; 65535];
    let n = match Writer::new(&mut buf, 65535, q) {
        Some(mut w) => {
            for r in records {
                // An RR that cannot fit even an otherwise empty message is not valid DNS data.
                let _ = w.rr(Section::Answer, &r.owner, r.rtype, r.ttl, &r.rdata);
            }
            w.finish(0, true, false)
        }
        None => 0,
    };
    buf.truncate(n);
    buf
}

/// Splits `records` into messages of at most `MAX_XFR_MESSAGE` octets (sizes counted
/// uncompressed, so compression only shrinks them); each message repeats the question
/// (RFC 5936 §2.2.1 allows it).
fn stream(q: &Question<'_>, records: &[&OwnedRecord]) -> Vec<Vec<u8>> {
    let base = HEADER_LEN + q.qname.len() + 4;
    let mut out = Vec::new();
    let (mut start, mut size) = (0usize, base);
    for (i, r) in records.iter().enumerate() {
        let n = r.owner.len() + 10 + r.rdata.len();
        if i > start && size + n > MAX_XFR_MESSAGE {
            out.push(write_message(q, &records[start..i]));
            (start, size) = (i, base);
        }
        size += n;
    }
    out.push(write_message(q, &records[start..]));
    out
}

/// The complete response messages of `plan` in send order, each signed when `signing` is set.
pub fn messages(
    plan: &Plan,
    raw: &[u8],
    q: &Question<'_>,
    signing: Signing,
    now: u64,
) -> Vec<Vec<u8>> {
    let mut msgs = match plan {
        Plan::TsigError(failure) => return vec![tsig::error_response(raw, failure, now)],
        Plan::Refused(rcode) => {
            let mut buf = vec![0u8; HEADER_LEN + q.qname.len() + 4];
            let n =
                Writer::new(&mut buf, usize::MAX, q).map_or(0, |w| w.finish(*rcode, false, false));
            buf.truncate(n);
            vec![buf]
        }
        Plan::UpToDate(z) => stream(q, &[&apex_soa(z)]),
        Plan::Full(z) => {
            let soa = apex_soa(z);
            let all = z.records_sorted();
            let mut recs: Vec<&OwnedRecord> = Vec::with_capacity(all.len() + 1);
            recs.push(&soa);
            recs.extend(all.iter().filter(|r| r.rtype != T_SOA));
            recs.push(&soa);
            stream(q, &recs)
        }
        Plan::Incremental(z, first) => {
            let soa = apex_soa(z);
            let mut recs: Vec<&OwnedRecord> = vec![&soa];
            for d in &z.deltas[*first..] {
                recs.extend(delta_records(d));
            }
            recs.push(&soa);
            stream(q, &recs)
        }
    };
    msgs.retain(|m| m.len() >= HEADER_LEN);
    if let Some((key, request_mac)) = signing {
        let mut signer = StreamSigner::new(key, request_mac);
        for m in &mut msgs {
            signer.sign(m, now);
        }
    }
    msgs
}
