//! The authoritative stage of `server::handle_packet`: hosted names are answered here, before the
//! ACL, policy, RPZ, cache and resolution stages.

use super::answer::{self, Limits, Served};
use super::msg::{EdnsInfo, OPCODE_NOTIFY, OPCODE_UPDATE, Question};
use super::name::lowercase_into;
use super::notify_in::{self, NotifySink};
use super::state::AuthState;
use super::zone::Zone;
use super::{T_AXFR, T_IXFR};
use super::{update, xfr};
use crate::clock;
use crate::edns::{self, ReplyOpt, Transport};
use crate::proto;
use crate::runtime::Runtime;
use crate::server::{Shared, WorkerCtx};
use crate::telemetry::querylog::{ACL_AUTHORITATIVE, FilterSource, QueryRecord};
use crate::tsig::{self, Verified};
use crate::wire::{self, QueryView};
use std::net::SocketAddr;
use std::rc::Rc;
use std::sync::Arc;
use std::sync::atomic::Ordering;

const HEADER_LEN: usize = 12;
/// `WorkerCounters::auth_answers` slots.
const ANSWER: usize = 0;
const NODATA: usize = 1;
const NXDOMAIN: usize = 2;
const REFERRAL: usize = 3;
const SERVFAIL: usize = 4;
/// The UDP payload size advertised in reply OPT records.
const ADVERTISED_UDP_SIZE: u16 = 1232;
const UDP_NO_EDNS_LIMIT: usize = 512;
const UDP_MAX_LIMIT: usize = 1232;
/// TSIG RR octets besides the key name, algorithm name and MAC: type, class, TTL, RDLENGTH,
/// time signed, fudge, MAC size, original ID, error, other length.
const TSIG_FIXED_LEN: usize = 10 + 16;

pub enum AuthOutcome {
    NotHosted,
    /// The reply is in `out[..n]`.
    Reply(usize),
    /// Answered off the fast path (zone transfers, dynamic updates).
    Slow(SlowJob),
}

pub enum SlowKind {
    Transfer,
    Update,
}

/// `AuthCounters::notify_received` slots.
const NOTIFY_FORWARDED: usize = 0;
const NOTIFY_DROPPED: usize = 1;
const NOTIFY_REFUSED: usize = 2;

pub struct SlowJob {
    pub query: Box<[u8]>,
    pub client: SocketAddr,
    pub transport: Transport,
    pub kind: SlowKind,
}

/// Runs a slow job; returns the complete response messages in send order (empty: send nothing).
pub async fn run_slow(ctx: Rc<WorkerCtx>, rt: Arc<Runtime>, job: SlowJob) -> Vec<Vec<u8>> {
    match job.kind {
        SlowKind::Transfer => {
            // Large zones take long to encode; the blocking pool keeps the worker answering queries.
            let shared = ctx.shared.clone();
            tokio::task::spawn_blocking(move || build_transfer(&rt, &shared, &job))
                .await
                .unwrap_or_default()
        }
        SlowKind::Update => {
            let auth = ctx.shared.auth.clone();
            let (reply, slot) = update::handle_update(
                job.query.into(),
                job.client,
                rt,
                auth.keyring.clone(),
                auth,
                unix_now(),
            )
            .await;
            ctx.shared.metrics.auth.updates[slot].fetch_add(1, Ordering::Relaxed);
            if reply.is_empty() {
                Vec::new()
            } else {
                vec![reply]
            }
        }
    }
}

/// Authorises, plans and encodes a zone transfer over TCP/DoT; runs on the blocking pool.
fn build_transfer(rt: &Runtime, shared: &Shared, job: &SlowJob) -> Vec<Vec<u8>> {
    let Ok(q) = Question::parse(&job.query) else {
        let mut out = vec![0u8; job.query.len().max(HEADER_LEN)];
        return wire::write_error_reply(&job.query, wire::RCODE_FORMERR, &mut out)
            .map(|n| {
                out.truncate(n);
                vec![out]
            })
            .unwrap_or_default();
    };
    let now = unix_now();
    let (plan, signing) = xfr::authorize_and_plan(
        rt,
        &shared.auth.keyring,
        &job.query,
        &q,
        job.client.ip(),
        true,
        now,
    );
    xfr::count(&shared.metrics.auth, q.qtype, &plan);
    xfr::messages(&plan, &job.query, &q, signing, now)
}

/// The production NOTIFY sink with the forwarded/dropped count.
struct CountingSink<'a> {
    auth: &'a AuthState,
    counters: &'a [std::sync::atomic::AtomicU64; 3],
}

impl NotifySink for CountingSink<'_> {
    fn notify(&self, ev: proto::NotifyReceived) -> bool {
        let sent = self.auth.notify(ev);
        let slot = if sent {
            NOTIFY_FORWARDED
        } else {
            NOTIFY_DROPPED
        };
        self.counters[slot].fetch_add(1, Ordering::Relaxed);
        sent
    }
}

fn unix_now() -> u64 {
    clock::unix_now().max(0) as u64
}

impl<'a> Question<'a> {
    /// The authoritative view of a query `wire::parse_query` accepted (no IXFR SOA, no TSIG).
    pub fn from_query(view: &QueryView<'a>, _raw: &'a [u8]) -> Question<'a> {
        Question {
            id: view.id,
            opcode: ((view.flags >> 11) & 0x0f) as u8,
            flags: view.flags,
            qname: view.qname,
            qtype: view.qtype,
            qclass: view.qclass,
            question_end: view.question_end,
            edns: view.opt.map(|o| EdnsInfo {
                udp_size: o.udp_size,
                do_bit: o.do_bit,
            }),
            ixfr_serial: None,
            tsig_at: None,
        }
    }
}

/// Whether `client` may query `zone`: the zone's allow-query ACL, else the runtime's authoritative
/// ACL. A refusal is marked on `rec`. Allocation-free.
fn query_allowed(rt: &Runtime, zone: &Zone, client: SocketAddr, rec: &mut QueryRecord) -> bool {
    let acl = zone.allow_query.as_ref().unwrap_or(&rt.authoritative_acl);
    if acl.allows_all() || acl.allows(client.ip()) {
        return true;
    }
    rec.acl_refused = ACL_AUTHORITATIVE;
    rec.filter_source = FilterSource::Acl;
    false
}

/// Answers a parsed query for a hosted name into `out` (already cut to the reply limit) and
/// appends `opt`; clients outside the zone's allow-query ACL are REFUSED. Allocation-free.
#[allow(clippy::too_many_arguments)]
pub fn fast(
    ctx: &WorkerCtx,
    rt: &Runtime,
    view: &QueryView<'_>,
    packet: &[u8],
    client: SocketAddr,
    transport: Transport,
    out: &mut [u8],
    opt: Option<&ReplyOpt>,
    rec: &mut QueryRecord,
) -> AuthOutcome {
    let q = Question::from_query(view, packet);
    let Some(zone) = rt.auth.find_for_query(view.key.as_wire(), q.qtype) else {
        return AuthOutcome::NotHosted;
    };
    // Transfers keep their own transfer ACL and are not subject to allow-query.
    if q.qtype != T_AXFR && q.qtype != T_IXFR && !query_allowed(rt, zone, client, rec) {
        return AuthOutcome::Reply(wire::write_rcode_reply(view, wire::RCODE_REFUSED, out, opt));
    }
    if q.qtype == T_AXFR || q.qtype == T_IXFR {
        let rcode = match transport {
            Transport::Tcp | Transport::Dot => {
                return AuthOutcome::Slow(SlowJob {
                    query: packet.into(),
                    client,
                    transport,
                    kind: SlowKind::Transfer,
                });
            }
            // AXFR over UDP, or an IXFR without the client's SOA.
            Transport::Udp => wire::RCODE_FORMERR,
            Transport::Doh | Transport::Doq => wire::RCODE_REFUSED,
        };
        return AuthOutcome::Reply(wire::write_rcode_reply(view, rcode, out, opt));
    }
    let opt_len = opt.map_or(0, ReplyOpt::wire_len);
    let limits = Limits {
        max_len: out.len().saturating_sub(opt_len),
        recursion_available: rt.acl.allows(client.ip()),
    };
    match answer::respond(&rt.auth, &q, out, limits) {
        Served::Done(mut n) => {
            if n < HEADER_LEN {
                return AuthOutcome::Reply(0);
            }
            count_answer(ctx, &out[..n]);
            if let Some(o) = opt {
                n += edns::write_opt(&mut out[n..], o);
                let ar = u16::from_be_bytes([out[10], out[11]]) + 1;
                out[10..12].copy_from_slice(&ar.to_be_bytes());
            }
            AuthOutcome::Reply(n)
        }
        Served::NotHosted => AuthOutcome::NotHosted,
    }
}

/// Messages `wire::parse_query` rejects (NOTIFY, UPDATE, IXFR with an authority SOA, TSIG-signed
/// queries). `None` keeps M1's FORMERR/NOTIMP reply. NOTIFY is answered inline and forwarded to
/// the management plane; UPDATE goes to the slow path on every transport. Transfers go to the
/// slow path on TCP/DoT and are answered inline on UDP; other TSIG-signed queries for hosted names
/// are verified and answered signed, for other names REFUSED.
pub fn unparsed(
    ctx: &WorkerCtx,
    rt: &Runtime,
    packet: &[u8],
    client: SocketAddr,
    transport: Transport,
    out: &mut [u8],
    rec: &mut QueryRecord,
) -> Option<AuthOutcome> {
    let opcode = (*packet.get(2)? >> 3) & 0x0f;
    if opcode == OPCODE_NOTIFY {
        let counters = &ctx.shared.metrics.auth.notify_received;
        let sink = CountingSink {
            auth: &ctx.shared.auth,
            counters,
        };
        let auth = &ctx.shared.auth;
        let reply =
            notify_in::handle_notify(packet, client, &rt.auth, &auth.keyring, &sink, unix_now());
        if reply
            .get(3)
            .is_some_and(|b| b & 0x0f != wire::RCODE_NOERROR)
        {
            counters[NOTIFY_REFUSED].fetch_add(1, Ordering::Relaxed);
        }
        return Some(copy_out(&reply, out));
    }
    if opcode == OPCODE_UPDATE {
        return Some(AuthOutcome::Slow(SlowJob {
            query: packet.into(),
            client,
            transport,
            kind: SlowKind::Update,
        }));
    }
    let q = Question::parse(packet).ok()?;
    let transfer = q.qtype == T_AXFR || q.qtype == T_IXFR;
    if q.opcode != 0 || (q.tsig_at.is_none() && !transfer) {
        return None;
    }
    let mut buf = [0u8; 255];
    let lower = lowercase_into(q.qname, &mut buf);
    let Some(zone) = rt.auth.find_for_query(lower, q.qtype) else {
        return q
            .tsig_at
            .map(|_| error_reply(packet, wire::RCODE_REFUSED, out));
    };
    if !transfer {
        return Some(signed_query(
            ctx, rt, packet, &q, zone, client, transport, out, rec,
        ));
    }
    Some(match transport {
        Transport::Tcp | Transport::Dot => AuthOutcome::Slow(SlowJob {
            query: packet.into(),
            client,
            transport,
            kind: SlowKind::Transfer,
        }),
        Transport::Udp => {
            let now = unix_now();
            let (plan, signing) = xfr::authorize_and_plan(
                rt,
                &ctx.shared.auth.keyring,
                packet,
                &q,
                client.ip(),
                false,
                now,
            );
            xfr::count(&ctx.shared.metrics.auth, q.qtype, &plan);
            let msgs = xfr::messages(&plan, packet, &q, signing, now);
            copy_out(msgs.first().map_or(&[][..], |m| &m[..]), out)
        }
        Transport::Doh | Transport::Doq => error_reply(packet, wire::RCODE_REFUSED, out),
    })
}

fn error_reply(packet: &[u8], rcode: u8, out: &mut [u8]) -> AuthOutcome {
    AuthOutcome::Reply(wire::write_error_reply(packet, rcode, out).unwrap_or(0))
}

/// Copies a reply built off the fast path into `out`; `Reply(0)` (dropped) when it does not fit.
fn copy_out(reply: &[u8], out: &mut [u8]) -> AuthOutcome {
    match out.get_mut(..reply.len()) {
        Some(dst) => {
            dst.copy_from_slice(reply);
            AuthOutcome::Reply(reply.len())
        }
        None => AuthOutcome::Reply(0),
    }
}

/// A TSIG-signed query for a hosted name in `zone`: REFUSED outside its allow-query ACL, else
/// verified, answered and signed (RFC 8945 §5.3), or a TSIG error response.
#[allow(clippy::too_many_arguments)]
fn signed_query(
    ctx: &WorkerCtx,
    rt: &Runtime,
    packet: &[u8],
    q: &Question<'_>,
    zone: &Zone,
    client: SocketAddr,
    transport: Transport,
    out: &mut [u8],
    rec: &mut QueryRecord,
) -> AuthOutcome {
    if !query_allowed(rt, zone, client, rec) {
        return error_reply(packet, wire::RCODE_REFUSED, out);
    }
    let now = unix_now();
    let (key, request_mac) = match tsig::verify_request(packet, &ctx.shared.auth.keyring, now) {
        Ok(Verified::Signed { key, request_mac }) => (key, request_mac),
        // Question::parse found a TSIG RR that find_tsig does not: malformed.
        Ok(Verified::Unsigned) => return error_reply(packet, wire::RCODE_FORMERR, out),
        Err(failure) => return copy_out(&tsig::error_response(packet, &failure, now), out),
    };
    let limit = match transport {
        Transport::Udp => q.edns.map_or(UDP_NO_EDNS_LIMIT, |e| {
            usize::from(e.udp_size).clamp(UDP_NO_EDNS_LIMIT, UDP_MAX_LIMIT)
        }),
        _ => 65535,
    }
    .min(out.len());
    let opt = q.edns.map(|e| ReplyOpt {
        udp_size: ADVERTISED_UDP_SIZE,
        do_bit: e.do_bit,
        ext_rcode: 0,
        cookie: None,
        ede: None,
    });
    let opt_len = opt.as_ref().map_or(0, ReplyOpt::wire_len);
    let tsig_len = key.lower_wire_name().len()
        + key.alg.name().len()
        + 1
        + TSIG_FIXED_LEN
        + key.alg.digest_len();
    let mut reply = vec![0u8; limit];
    let limits = Limits {
        max_len: limit.saturating_sub(opt_len + tsig_len),
        recursion_available: rt.acl.allows(client.ip()),
    };
    let mut n = match answer::respond(&rt.auth, q, &mut reply, limits) {
        Served::Done(n) if n >= HEADER_LEN => n,
        Served::Done(_) => return AuthOutcome::Reply(0),
        Served::NotHosted => return error_reply(packet, wire::RCODE_REFUSED, out),
    };
    count_answer(ctx, &reply[..n]);
    if let Some(o) = &opt {
        n += edns::write_opt(&mut reply[n..], o);
        let ar = u16::from_be_bytes([reply[10], reply[11]]) + 1;
        reply[10..12].copy_from_slice(&ar.to_be_bytes());
    }
    reply.truncate(n);
    tsig::sign_response(&mut reply, &key, now, &request_mac, true, &[]);
    copy_out(&reply, out)
}

fn count_answer(ctx: &WorkerCtx, reply: &[u8]) {
    let rcode = reply[3] & 0x0f;
    let slot = if rcode == wire::RCODE_SERVFAIL {
        SERVFAIL
    } else if rcode == wire::RCODE_NXDOMAIN {
        NXDOMAIN
    } else if reply[2] & 0x04 == 0 {
        REFERRAL
    } else if reply[6..8] == [0, 0] {
        NODATA
    } else {
        ANSWER
    };
    ctx.counters().auth_answers[slot].fetch_add(1, Ordering::Relaxed);
}
