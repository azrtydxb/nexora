//! Zone transfer client: SOA check, IXFR (RFC 1995) with AXFR fallback, RFC 1982 serials.

use super::tsig::{TsigKey, TsigVerifier};
use hickory_proto::op::{Message, OpCode, Query, ResponseCode};
use hickory_proto::rr::{Name, RData, Record, RecordType, rdata::SOA};
use rustc_hash::FxHashSet;
use std::net::SocketAddr;
use std::time::Duration;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::TcpStream;

/// Deadline for one SOA query or one complete transfer.
const SOA_DEADLINE: Duration = Duration::from_secs(10);
const TRANSFER_DEADLINE: Duration = Duration::from_secs(60);
/// Largest zone accepted from a primary.
const MAX_RECORDS: usize = 10_000_000;

#[derive(Clone, Debug, PartialEq)]
pub struct ZoneData {
    pub serial: u32,
    /// Every record including the apex SOA once (the transfer's trailing SOA is excluded).
    pub records: Vec<Record>,
}

/// RFC 1982: `a` is greater than `b`. The undefined distance 2^31 counts as not greater.
pub fn serial_gt(a: u32, b: u32) -> bool {
    a != b && (a.wrapping_sub(b) as i32) > 0
}

#[derive(Debug, PartialEq, Eq)]
pub struct Timers {
    pub refresh: u64,
    pub retry: u64,
    pub expire: u64,
}

/// SOA timers, each floored at `min_refresh` (0 -> 60 s).
pub fn timers_from_soa(refresh: u32, retry: u32, expire: u32, min_refresh: u32) -> Timers {
    let floor = if min_refresh == 0 { 60 } else { min_refresh };
    Timers {
        refresh: u64::from(refresh.max(floor)),
        retry: u64::from(retry.max(floor)),
        expire: u64::from(expire.max(floor)),
    }
}

fn soa_of(r: &Record) -> Option<&SOA> {
    match &r.data {
        RData::SOA(s) => Some(s),
        _ => None,
    }
}

#[cfg(test)]
thread_local! {
    /// Number of `record_key` calls on this thread, for the IXFR key-count test.
    pub(super) static KEYS_BUILT: std::cell::Cell<usize> = const { std::cell::Cell::new(0) };
}

/// Record identity for IXFR deletions (RFC 2136 equality: TTL ignored).
fn record_key(r: &Record) -> String {
    #[cfg(test)]
    KEYS_BUILT.with(|c| c.set(c.get() + 1));
    format!(
        "{} {} {} {}",
        r.name.to_lowercase(),
        r.dns_class,
        r.record_type(),
        r.data
    )
}

/// Applies an IXFR (or AXFR-style) answer sequence to `current`.
pub fn apply_ixfr(current: &ZoneData, answers: &[Record]) -> Result<ZoneData, String> {
    let first = answers
        .first()
        .and_then(soa_of)
        .ok_or("transfer does not start with SOA")?;
    let new_serial = first.serial;
    if answers.len() == 1 {
        return if new_serial == current.serial {
            Ok(current.clone())
        } else {
            Err("transfer is incomplete".into())
        };
    }
    let last = answers
        .last()
        .and_then(soa_of)
        .ok_or("transfer does not end with SOA")?;
    if last.serial != new_serial {
        return Err("transfer ends with a different SOA serial".into());
    }
    let second_is_old_soa =
        soa_of(&answers[1]).is_some_and(|s| s.serial != new_serial || answers.len() > 2);
    if !second_is_old_soa {
        // AXFR form: SOA, records..., SOA.
        return Ok(ZoneData {
            serial: new_serial,
            records: answers[..answers.len() - 1].to_vec(),
        });
    }
    // Incremental: (old SOA, deletions..., new SOA, additions...)+, final SOA.
    let body = &answers[1..answers.len() - 1];
    let mut records = current.records.clone();
    let mut serial = current.serial;
    let mut i = 0;
    while i < body.len() {
        let old = soa_of(&body[i]).ok_or("IXFR difference does not start with SOA")?;
        if old.serial != serial {
            return Err(format!(
                "IXFR difference starts at serial {} but the zone is at {serial}",
                old.serial
            ));
        }
        i += 1;
        let del_start = i;
        while i < body.len() && soa_of(&body[i]).is_none() {
            i += 1;
        }
        let deletions: FxHashSet<String> = body[del_start..i].iter().map(record_key).collect();
        // hickory's Name hashes and compares case-insensitively.
        let deleted_owners: FxHashSet<&Name> = body[del_start..i].iter().map(|r| &r.name).collect();
        let new_soa = body.get(i).ok_or("IXFR difference has no new SOA")?;
        serial = soa_of(new_soa)
            .ok_or("IXFR difference has no new SOA")?
            .serial;
        i += 1;
        let add_start = i;
        while i < body.len() && soa_of(&body[i]).is_none() {
            i += 1;
        }
        let before = records.len();
        let soas = records
            .iter()
            .filter(|r| r.record_type() == RecordType::SOA)
            .count();
        if deletions.is_empty() {
            records.retain(|r| r.record_type() != RecordType::SOA);
        } else {
            records.retain(|r| {
                r.record_type() != RecordType::SOA
                    && !(deleted_owners.contains(&r.name) && deletions.contains(&record_key(r)))
            });
        }
        if before - records.len() - soas < deletions.len() {
            return Err("IXFR deletes a record that is not in the zone".into());
        }
        records.insert(0, new_soa.clone());
        records.extend(body[add_start..i].iter().cloned());
        if records.len() > MAX_RECORDS {
            return Err("zone exceeds 10000000 records".into());
        }
    }
    if serial != new_serial {
        return Err("IXFR differences do not reach the announced serial".into());
    }
    Ok(ZoneData { serial, records })
}

fn now_unix() -> u64 {
    crate::clock::unix_now().max(0) as u64
}

fn request(
    zone: &Name,
    qtype: RecordType,
    current_soa: Option<&Record>,
    key: Option<&TsigKey>,
) -> (u16, Vec<u8>, Vec<u8>) {
    let id = rand::Rng::next_u32(&mut rand::rng()) as u16;
    let mut m = Message::query();
    m.metadata.id = id;
    m.metadata.op_code = OpCode::Query;
    m.queries.push(Query::query(zone.clone(), qtype));
    if let Some(soa) = current_soa {
        m.authorities.push(soa.clone());
    }
    let mut wire = m.to_vec().expect("query encodes");
    let mac = key
        .map(|k| super::tsig::sign_request(&mut wire, k, now_unix()))
        .unwrap_or_default();
    (id, wire, mac)
}

async fn send(stream: &mut TcpStream, wire: &[u8]) -> Result<(), String> {
    let len = u16::try_from(wire.len()).map_err(|_| "request too large".to_string())?;
    let mut framed = Vec::with_capacity(wire.len() + 2);
    framed.extend(len.to_be_bytes());
    framed.extend(wire);
    stream
        .write_all(&framed)
        .await
        .map_err(|e| format!("send: {e}"))
}

async fn recv(stream: &mut TcpStream) -> Result<Vec<u8>, String> {
    let len = stream.read_u16().await.map_err(|e| format!("read: {e}"))? as usize;
    let mut buf = vec![0u8; len];
    stream
        .read_exact(&mut buf)
        .await
        .map_err(|e| format!("read: {e}"))?;
    Ok(buf)
}

/// Decodes one reply message, checking ID, question and TSIG.
fn check_reply(
    wire: &[u8],
    id: u16,
    zone: &Name,
    qtype: RecordType,
    first: bool,
    verifier: &mut Option<TsigVerifier>,
) -> Result<Message, String> {
    if let Some(v) = verifier.as_mut() {
        v.verify(wire, now_unix())
            .map_err(|e| format!("TSIG verification failed: {e:?}"))?;
    }
    let m = Message::from_vec(wire).map_err(|e| format!("malformed reply: {e}"))?;
    if m.metadata.id != id {
        return Err("reply ID does not match the request".into());
    }
    if first || !m.queries.is_empty() {
        match m.queries.first() {
            Some(q) if q.name() == zone && q.query_type() == qtype => {}
            _ => return Err("reply question does not match the request".into()),
        }
    }
    Ok(m)
}

/// The primary's SOA for `zone` over TCP (TSIG-signed and verified when `key` is set).
pub async fn soa_serial(
    primary: SocketAddr,
    zone: &Name,
    key: Option<&TsigKey>,
) -> Result<(u32, SOA), String> {
    let work = async {
        let mut s = TcpStream::connect(primary)
            .await
            .map_err(|e| format!("connect {primary}: {e}"))?;
        let (id, wire, mac) = request(zone, RecordType::SOA, None, key);
        send(&mut s, &wire).await?;
        let reply = recv(&mut s).await?;
        let mut verifier = key.map(|k| TsigVerifier::new(k.clone(), mac));
        let m = check_reply(&reply, id, zone, RecordType::SOA, true, &mut verifier)?;
        if let Some(v) = &verifier {
            v.finish()
                .map_err(|e| format!("TSIG verification failed: {e:?}"))?;
        }
        if m.metadata.response_code != ResponseCode::NoError {
            return Err(format!("SOA query: {}", m.metadata.response_code));
        }
        m.answers
            .iter()
            .find(|r| r.name == *zone)
            .and_then(soa_of)
            .map(|s| (s.serial, s.clone()))
            .ok_or_else(|| "SOA query: no SOA in the answer".to_string())
    };
    tokio::time::timeout(SOA_DEADLINE, work)
        .await
        .map_err(|_| "SOA query timed out".to_string())?
}

enum Outcome {
    Data(ZoneData),
    /// NOTIMP/REFUSED/FORMERR to an IXFR request.
    IxfrRefused,
}

async fn transfer_once(
    primary: SocketAddr,
    zone: &Name,
    current: Option<&ZoneData>,
    key: Option<&TsigKey>,
) -> Result<Outcome, String> {
    let mut s = TcpStream::connect(primary)
        .await
        .map_err(|e| format!("connect {primary}: {e}"))?;
    let current_soa = current.and_then(|c| {
        c.records
            .iter()
            .find(|r| r.record_type() == RecordType::SOA)
    });
    let qtype = if current_soa.is_some() {
        RecordType::IXFR
    } else {
        RecordType::AXFR
    };
    let (id, wire, mac) = request(zone, qtype, current_soa, key);
    send(&mut s, &wire).await?;
    let mut verifier = key.map(|k| TsigVerifier::new(k.clone(), mac));
    let mut answers: Vec<Record> = Vec::new();
    let mut first = true;
    // Termination state: the announced serial, whether the stream is incremental, and for an
    // incremental stream whether the next SOA starts an addition section.
    let mut announced: Option<u32> = None;
    let mut incremental: Option<bool> = None;
    let mut in_additions = false;
    let mut running = 0u32;
    'stream: loop {
        let reply = recv(&mut s).await?;
        let m = check_reply(&reply, id, zone, qtype, first, &mut verifier)?;
        first = false;
        match m.metadata.response_code {
            ResponseCode::NoError => {}
            ResponseCode::NotImp | ResponseCode::Refused | ResponseCode::FormErr
                if qtype == RecordType::IXFR && answers.is_empty() =>
            {
                return Ok(Outcome::IxfrRefused);
            }
            rcode => return Err(format!("{qtype}: {rcode}")),
        }
        for r in m.answers {
            let soa_serial = soa_of(&r).map(|s| s.serial);
            answers.push(r);
            if answers.len() > MAX_RECORDS + 1 {
                return Err("zone exceeds 10000000 records".into());
            }
            let Some(n) = announced else {
                let n = soa_serial.ok_or("transfer does not start with SOA")?;
                announced = Some(n);
                if current.is_some_and(|c| c.serial == n) {
                    break 'stream; // up to date
                }
                continue;
            };
            match (incremental, soa_serial) {
                (None, Some(s)) if s == n => break 'stream, // AXFR of an empty zone
                (None, Some(s)) => {
                    incremental = Some(true);
                    running = s;
                    in_additions = false;
                }
                (None, None) => incremental = Some(false),
                (Some(false), Some(s)) if s == n => break 'stream,
                (Some(false), _) => {}
                (Some(true), Some(s)) if in_additions && running == n && s == n => break 'stream,
                (Some(true), Some(s)) => {
                    running = s;
                    in_additions = !in_additions;
                }
                (Some(true), None) => {}
            }
        }
    }
    if let Some(v) = &verifier {
        v.finish()
            .map_err(|e| format!("TSIG verification failed: {e:?}"))?;
    }
    let empty = ZoneData {
        serial: 0,
        records: Vec::new(),
    };
    apply_ixfr(current.unwrap_or(&empty), &answers).map(Outcome::Data)
}

/// Transfers `zone`: IXFR from `current` when given (AXFR when the primary refuses IXFR or the
/// increment does not apply), AXFR otherwise.
pub async fn transfer(
    primary: SocketAddr,
    zone: &Name,
    current: Option<&ZoneData>,
    key: Option<&TsigKey>,
) -> Result<ZoneData, String> {
    let work = async {
        if current.is_some() {
            match transfer_once(primary, zone, current, key).await {
                Ok(Outcome::Data(z)) => return Ok(z),
                Err(e) if e.starts_with("TSIG") || e.starts_with("connect") => return Err(e),
                Ok(Outcome::IxfrRefused) | Err(_) => {}
            }
        }
        match transfer_once(primary, zone, None, key).await? {
            Outcome::Data(z) => Ok(z),
            Outcome::IxfrRefused => unreachable!("AXFR request"),
        }
    };
    tokio::time::timeout(TRANSFER_DEADLINE, work)
        .await
        .map_err(|_| "transfer timed out".to_string())?
}
