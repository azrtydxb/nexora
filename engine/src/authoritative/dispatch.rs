//! The authoritative stage of `server::handle_packet`: hosted names are answered here, before the
//! ACL, policy, RPZ, cache and resolution stages.

use super::answer::{self, Limits, Served};
use super::msg::{EdnsInfo, Question};
use super::name::lowercase_into;
use super::xfr;
use super::{T_AXFR, T_IXFR};
use crate::clock;
use crate::edns::{self, ReplyOpt, Transport};
use crate::runtime::Runtime;
use crate::server::WorkerCtx;
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
    /// Answered off the fast path (zone transfers).
    Slow(SlowJob),
}

pub enum SlowKind {
    Transfer,
}

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
            // debt: the whole transfer is built on the worker thread; move it off the worker
            // (spawn_blocking) when hosted zones grow past ~100k records.
            let (plan, signing) = xfr::authorize_and_plan(
                &rt,
                &ctx.shared.auth.keyring,
                &job.query,
                &q,
                job.client.ip(),
                true,
                now,
            );
            xfr::count(&ctx.shared.metrics.auth, q.qtype, &plan);
            xfr::messages(&plan, &job.query, &q, signing, now)
        }
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

/// Answers a parsed query for a hosted name into `out` (already cut to the reply limit) and
/// appends `opt`. Allocation-free.
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
) -> AuthOutcome {
    let q = Question::from_query(view, packet);
    if rt
        .auth
        .find_for_query(view.key.as_wire(), q.qtype)
        .is_none()
    {
        return AuthOutcome::NotHosted;
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
/// queries). `None` keeps M1's FORMERR/NOTIMP reply. Transfers go to the slow path on TCP/DoT
/// and are answered inline on UDP; other TSIG-signed queries for hosted names are verified and
/// answered signed, for other names REFUSED. Tasks 10 and 11 add NOTIFY and UPDATE.
pub fn unparsed(
    ctx: &WorkerCtx,
    rt: &Runtime,
    packet: &[u8],
    client: SocketAddr,
    transport: Transport,
    out: &mut [u8],
) -> Option<AuthOutcome> {
    let q = Question::parse(packet).ok()?;
    let transfer = q.qtype == T_AXFR || q.qtype == T_IXFR;
    if q.opcode != 0 || (q.tsig_at.is_none() && !transfer) {
        return None;
    }
    let mut buf = [0u8; 255];
    let lower = lowercase_into(q.qname, &mut buf);
    if rt.auth.find_for_query(lower, q.qtype).is_none() {
        return q
            .tsig_at
            .map(|_| error_reply(packet, wire::RCODE_REFUSED, out));
    }
    if !transfer {
        return Some(signed_query(ctx, rt, packet, &q, client, transport, out));
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

/// A TSIG-signed query for a hosted name: verified, answered and signed (RFC 8945 §5.3), or a
/// TSIG error response.
fn signed_query(
    ctx: &WorkerCtx,
    rt: &Runtime,
    packet: &[u8],
    q: &Question<'_>,
    client: SocketAddr,
    transport: Transport,
    out: &mut [u8],
) -> AuthOutcome {
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
