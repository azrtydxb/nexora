//! The authoritative stage of `server::handle_packet`: hosted names are answered here, before the
//! ACL, policy, RPZ, cache and resolution stages.

use super::answer::{self, Limits, Served};
use super::msg::{EdnsInfo, Question};
use super::{T_AXFR, T_IXFR};
use crate::edns::{self, ReplyOpt, Transport};
use crate::runtime::Runtime;
use crate::server::WorkerCtx;
use crate::wire::{self, QueryView};
use std::net::SocketAddr;
use std::sync::atomic::Ordering;

const HEADER_LEN: usize = 12;
/// `WorkerCounters::auth_answers` slots.
const ANSWER: usize = 0;
const NODATA: usize = 1;
const NXDOMAIN: usize = 2;
const REFERRAL: usize = 3;
const SERVFAIL: usize = 4;

pub enum AuthOutcome {
    NotHosted,
    /// The reply is in `out[..n]`.
    Reply(usize),
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
    if q.qtype == T_AXFR && transport == Transport::Udp {
        return AuthOutcome::Reply(wire::write_rcode_reply(view, wire::RCODE_FORMERR, out, opt));
    }
    if q.qtype == T_AXFR || q.qtype == T_IXFR {
        // Zone transfers are not served yet (M4 Task 8 routes them to the transfer path).
        return AuthOutcome::Reply(wire::write_rcode_reply(view, wire::RCODE_REFUSED, out, opt));
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
/// queries). `None` keeps M1's FORMERR/NOTIMP reply; Tasks 7, 8, 10 and 11 answer them here.
pub fn unparsed(
    _ctx: &WorkerCtx,
    _rt: &Runtime,
    _packet: &[u8],
    _client: SocketAddr,
    _transport: Transport,
    _out: &mut [u8],
) -> Option<AuthOutcome> {
    None
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
