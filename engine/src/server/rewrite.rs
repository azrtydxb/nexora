//! Rewrite answers: A/AAAA (or NODATA) synthesis inline, and CNAME rewrites
//! chased through the client's policy and the miss pipeline.

use super::{FastOutcome, Scope, WorkerCtx, handle_packet, resolve_miss};
use crate::edns::Transport;
use crate::filter::{BlockMode, EffectivePolicy, FilterSet, RewriteAnswer, Verdict};
use crate::runtime::Runtime;
use crate::telemetry::querylog::FilterOutcome;
use crate::wire;
use hickory_proto::op::{Edns, Message, OpCode, ResponseCode};
use hickory_proto::rr::rdata::{A, AAAA, CNAME};
use hickory_proto::rr::{Name, RData, Record, RecordType};
use std::future::Future;
use std::net::{Ipv4Addr, Ipv6Addr, SocketAddr};
use std::pin::pin;
use std::rc::Rc;
use std::sync::Arc;
use std::task::{Context, Poll, Waker};
use std::time::Instant;

pub const MAX_REWRITE_DEPTH: usize = 8;
const ADVERTISED_UDP_SIZE: u16 = 1232;

// Futures stay on the per-core `LocalSet`, so no `Send` bound is wanted on them.
#[allow(async_fn_in_trait)]
pub trait RewriteContext {
    fn policy(&self) -> &EffectivePolicy;
    async fn resolve(&self, name: &Name, qtype: RecordType) -> Result<Message, ()>;
    fn block(&self, owner: &Name, qtype: RecordType) -> (ResponseCode, Vec<Record>);
}

/// A CNAME rewrite handed from the fast path to a `spawn_local` task.
pub struct RewriteJob {
    pub query: Box<[u8]>,
    pub client: SocketAddr,
    pub transport: Transport,
    pub limit: usize,
    pub started: Instant,
}

fn skeleton(q: &Message) -> Message {
    let mut m = Message::response(q.metadata.id, OpCode::Query);
    m.metadata.recursion_desired = q.metadata.recursion_desired;
    m.metadata.recursion_available = true;
    m.metadata.checking_disabled = q.metadata.checking_disabled;
    m.queries = q.queries.clone();
    if q.edns.is_some() {
        let mut e = Edns::new();
        e.set_max_payload(ADVERTISED_UDP_SIZE);
        m.edns = Some(e);
    }
    m
}

fn addr_records(
    owner: &Name,
    a: &[(Ipv4Addr, u32)],
    aaaa: &[(Ipv6Addr, u32)],
    qtype: RecordType,
) -> Vec<Record> {
    match qtype {
        RecordType::A => a
            .iter()
            .map(|(ip, ttl)| Record::from_rdata(owner.clone(), *ttl, RData::A(A(*ip))))
            .collect(),
        RecordType::AAAA => aaaa
            .iter()
            .map(|(ip, ttl)| Record::from_rdata(owner.clone(), *ttl, RData::AAAA(AAAA(*ip))))
            .collect(),
        _ => Vec::new(),
    }
}

/// The client's full response for a rewritten question; empty when `query` does not decode.
pub async fn rewrite_response<C: RewriteContext>(
    ctx: &C,
    query: &[u8],
    first: &RewriteAnswer,
) -> Vec<u8> {
    let Ok(q) = Message::from_vec(query) else {
        return Vec::new();
    };
    let Some(question) = q.queries.first() else {
        return Vec::new();
    };
    let qtype = question.query_type();
    let mut resp = skeleton(&q);
    let mut owner = question.name().clone();
    let mut seen = vec![owner.to_lowercase()];
    let mut current = first;
    crate::telemetry::metrics::ENCRYPTED.rewritten();
    for hop in 0..=MAX_REWRITE_DEPTH {
        match current {
            RewriteAnswer::Addrs { a, aaaa } => {
                resp.answers.extend(addr_records(&owner, a, aaaa, qtype));
                break;
            }
            RewriteAnswer::Cname { target, ttl } => {
                resp.answers.push(Record::from_rdata(
                    owner.clone(),
                    *ttl,
                    RData::CNAME(CNAME(target.clone())),
                ));
                if qtype == RecordType::CNAME {
                    break;
                }
                let lower = target.to_lowercase();
                if seen.contains(&lower) || hop == MAX_REWRITE_DEPTH {
                    resp.answers.clear();
                    resp.metadata.response_code = ResponseCode::ServFail;
                    break;
                }
                let wire_name = name_wire(&lower);
                seen.push(lower);
                match ctx.policy().check(&wire_name) {
                    Verdict::Rewrite(next) => {
                        owner = target.clone();
                        current = next;
                    }
                    Verdict::Blocked => {
                        let (rcode, records) = ctx.block(target, qtype);
                        resp.answers.extend(records);
                        resp.metadata.response_code = rcode;
                        break;
                    }
                    Verdict::Pass | Verdict::Allowed => {
                        match ctx.resolve(target, qtype).await {
                            Ok(up) => {
                                resp.metadata.response_code = up.metadata.response_code;
                                resp.answers.extend(up.answers);
                                resp.authorities = up.authorities;
                            }
                            Err(()) => {
                                resp.answers.clear();
                                resp.metadata.response_code = ResponseCode::ServFail;
                            }
                        }
                        break;
                    }
                }
            }
        }
    }
    resp.to_vec().unwrap_or_default()
}

/// The lowercase uncompressed wire form of `name`.
fn name_wire(name: &Name) -> Vec<u8> {
    let mut out = Vec::with_capacity(64);
    for label in name.iter() {
        out.push(label.len() as u8);
        out.extend_from_slice(label);
    }
    out.push(0);
    out
}

/// The block records and rcode of `filter`'s block mode for `(owner, qtype)`.
fn block_records(
    filter: &FilterSet,
    owner: &Name,
    qtype: RecordType,
) -> (ResponseCode, Vec<Record>) {
    match filter.mode {
        BlockMode::NxDomain => (ResponseCode::NXDomain, Vec::new()),
        BlockMode::Refused => (ResponseCode::Refused, Vec::new()),
        BlockMode::NullIp => {
            let rdata = match qtype {
                RecordType::A => Some(RData::A(A(Ipv4Addr::UNSPECIFIED))),
                RecordType::AAAA => Some(RData::AAAA(AAAA(Ipv6Addr::UNSPECIFIED))),
                _ => None,
            };
            let records = rdata
                .map(|r| Record::from_rdata(owner.clone(), filter.ttl, r))
                .into_iter()
                .collect();
            (ResponseCode::NoError, records)
        }
    }
}

/// A context for answers that never chase a CNAME (`RewriteAnswer::Addrs`).
struct InlineCtx<'a>(&'a EffectivePolicy);

impl RewriteContext for InlineCtx<'_> {
    fn policy(&self) -> &EffectivePolicy {
        self.0
    }
    async fn resolve(&self, _: &Name, _: RecordType) -> Result<Message, ()> {
        Err(())
    }
    fn block(&self, owner: &Name, qtype: RecordType) -> (ResponseCode, Vec<Record>) {
        block_records(self.0.filter(), owner, qtype)
    }
}

/// Builds an `Addrs` rewrite reply synchronously: that answer never awaits,
/// so one poll completes it. `None` only if the future were to wait.
pub fn answer_inline(
    policy: &EffectivePolicy,
    query: &[u8],
    first: &RewriteAnswer,
) -> Option<Vec<u8>> {
    let ctx = InlineCtx(policy);
    let mut fut = pin!(rewrite_response(&ctx, query, first));
    match fut.as_mut().poll(&mut Context::from_waker(Waker::noop())) {
        Poll::Ready(reply) => Some(reply),
        Poll::Pending => None,
    }
}

/// `reply` when it fits `limit`, else the same header with TC=1 and no records
/// (empty when even that does not fit).
pub fn fit_limit(reply: Vec<u8>, limit: usize) -> Vec<u8> {
    if reply.len() <= limit {
        return reply;
    }
    let Ok(mut m) = Message::from_vec(&reply) else {
        return Vec::new();
    };
    m.metadata.truncation = true;
    m.answers.clear();
    m.authorities.clear();
    m.additionals.clear();
    m.to_vec()
        .ok()
        .filter(|b| b.len() <= limit)
        .unwrap_or_default()
}

/// Chases CNAME rewrites for one client of one runtime through the worker's miss pipeline.
pub struct WorkerRewriteCtx {
    pub ctx: Rc<WorkerCtx>,
    pub rt: Arc<Runtime>,
    pub client: SocketAddr,
}

impl RewriteContext for WorkerRewriteCtx {
    fn policy(&self) -> &EffectivePolicy {
        self.rt.policy.select(self.client.ip()).0
    }

    async fn resolve(&self, name: &Name, qtype: RecordType) -> Result<Message, ()> {
        let mut m = Message::query();
        m.metadata.recursion_desired = true;
        m.queries
            .push(hickory_proto::op::Query::query(name.clone(), qtype));
        let query = m.to_vec().map_err(|_| ())?;
        let mut buf = vec![0u8; 65535];
        let reply = match handle_packet(
            &self.ctx,
            &self.rt,
            &query,
            self.client,
            Transport::Tcp,
            &mut buf,
        ) {
            FastOutcome::Reply(n) => {
                buf.truncate(n);
                buf
            }
            FastOutcome::Miss(job) => resolve_miss(self.ctx.clone(), self.rt.clone(), job).await,
            FastOutcome::Rewrite(_) | FastOutcome::Drop => return Err(()),
        };
        Message::from_vec(&reply).map_err(|_| ())
    }

    fn block(&self, owner: &Name, qtype: RecordType) -> (ResponseCode, Vec<Record>) {
        block_records(self.policy().filter(), owner, qtype)
    }
}

/// Answers a CNAME rewrite job and logs it; empty only when nothing can be sent.
pub async fn run_rewrite_job(ctx: Rc<WorkerCtx>, rt: Arc<Runtime>, job: RewriteJob) -> Vec<u8> {
    let Ok(q) = wire::parse_query(&job.query) else {
        return Vec::new();
    };
    let (policy, group) = rt.policy.select(job.client.ip());
    let Verdict::Rewrite(first) = policy.check(q.key.as_wire()) else {
        return Vec::new();
    };
    let rewrite_ctx = WorkerRewriteCtx {
        ctx: ctx.clone(),
        rt: rt.clone(),
        client: job.client,
    };
    let reply = fit_limit(
        rewrite_response(&rewrite_ctx, &job.query, first).await,
        job.limit,
    );
    if reply.len() >= 12 {
        let scope = Scope {
            ctx: &ctx,
            rt: &rt,
            client: job.client,
            transport: job.transport,
            started: job.started,
        };
        let mut rec = scope.record(q.key, q.qtype);
        rec.filter = FilterOutcome::Rewritten;
        rec.policy_group = group.unwrap_or(crate::telemetry::querylog::NO_POLICY_GROUP);
        scope.finish(rec, &reply);
    }
    reply
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::filter::{BlockMode, FilterSet, PolicyTable};
    use crate::proto::{BlobRef, ConfigSnapshot, RewriteRule, RewriteSet, RewriteType};
    use crate::snapshot::{BlobSource, SnapshotError};
    use hickory_proto::op::{MessageType, Query};
    use hickory_proto::rr::rdata::{A, CNAME};
    use hickory_proto::serialize::binary::BinEncodable;
    use std::cell::Cell;
    use std::net::Ipv4Addr;
    use std::sync::Arc;

    struct Ctx {
        table: PolicyTable,
        upstream_calls: Cell<u32>,
    }
    impl RewriteContext for Ctx {
        fn policy(&self) -> &EffectivePolicy {
            self.table.select("192.0.2.1".parse().unwrap()).0
        }
        async fn resolve(&self, name: &Name, qtype: RecordType) -> Result<Message, ()> {
            self.upstream_calls.set(self.upstream_calls.get() + 1);
            let mut m = Message::response(1, OpCode::Query);
            if name.to_ascii() == "forcesafesearch.google.com." && qtype == RecordType::A {
                m.answers.push(Record::from_rdata(
                    name.clone(),
                    300,
                    RData::A(A(Ipv4Addr::new(216, 239, 38, 120))),
                ));
                Ok(m)
            } else {
                Err(())
            }
        }
        fn block(&self, owner: &Name, _q: RecordType) -> (ResponseCode, Vec<Record>) {
            (
                ResponseCode::NoError,
                vec![Record::from_rdata(
                    owner.clone(),
                    10,
                    RData::A(A(Ipv4Addr::UNSPECIFIED)),
                )],
            )
        }
    }
    struct NoBlobs;
    impl BlobSource for NoBlobs {
        fn read(&self, r: &BlobRef) -> Result<Vec<u8>, SnapshotError> {
            Err(SnapshotError::Blob {
                sha256: r.sha256.clone(),
                reason: "unused".into(),
            })
        }
    }
    fn ctx(rules: Vec<(&str, RewriteType, &str)>, blocked: &str) -> Ctx {
        let snap = ConfigSnapshot {
            rewrite_sets: vec![RewriteSet {
                id: "s".into(),
                label: "s".into(),
                rules: rules
                    .into_iter()
                    .map(|(n, t, v)| RewriteRule {
                        name: n.into(),
                        r#type: t as i32,
                        value: v.into(),
                        ttl: 300,
                    })
                    .collect(),
            }],
            global_rewrite_set_ids: vec!["s".into()],
            ..Default::default()
        };
        let global = Arc::new(
            FilterSet::build(&[blocked.as_bytes().to_vec()], &[], BlockMode::NullIp, 10).0,
        );
        Ctx {
            table: PolicyTable::build(&snap, global, &NoBlobs).unwrap(),
            upstream_calls: Cell::new(0),
        }
    }
    fn query(name: &str, t: RecordType) -> Vec<u8> {
        let mut m = Message::new(0xbeef, MessageType::Query, OpCode::Query);
        m.metadata.recursion_desired = true;
        m.queries
            .push(Query::query(Name::from_ascii(name).unwrap(), t));
        m.to_vec().unwrap()
    }
    async fn run(c: &Ctx, name: &str, t: RecordType) -> Message {
        let wire = Name::from_ascii(name)
            .unwrap()
            .to_lowercase()
            .to_bytes()
            .unwrap();
        let crate::filter::Verdict::Rewrite(first) = c.policy().check(&wire) else {
            panic!("no rewrite for {name}")
        };
        Message::from_vec(&rewrite_response(c, &query(name, t), first).await).unwrap()
    }

    #[tokio::test]
    async fn safe_search_cname_is_chased_and_keeps_client_casing() {
        let c = ctx(
            vec![(
                "www.google.com",
                RewriteType::Cname,
                "forcesafesearch.google.com",
            )],
            "",
        );
        let m = run(&c, "WWW.Google.com.", RecordType::A).await;
        assert_eq!(m.metadata.id, 0xbeef);
        assert_eq!(m.metadata.response_code, ResponseCode::NoError);
        assert_eq!(m.answers[0].name.to_ascii(), "WWW.Google.com.");
        assert_eq!(
            m.answers[0].data,
            RData::CNAME(CNAME(
                Name::from_ascii("forcesafesearch.google.com.").unwrap()
            ))
        );
        assert_eq!(
            m.answers[1].data,
            RData::A(A(Ipv4Addr::new(216, 239, 38, 120)))
        );
        let only = run(&c, "www.google.com.", RecordType::CNAME).await;
        assert_eq!(only.answers.len(), 1);
        assert_eq!(c.upstream_calls.get(), 1, "qtype CNAME does not chase");
    }

    #[tokio::test]
    async fn addrs_nodata_chains_loops_and_blocked_targets() {
        let c = ctx(
            vec![
                ("nas.home.test", RewriteType::A, "192.168.1.50"),
                ("x.home.test", RewriteType::Cname, "nas.home.test"),
                ("loop1.test", RewriteType::Cname, "loop2.test"),
                ("loop2.test", RewriteType::Cname, "loop1.test"),
                ("tracked.test", RewriteType::Cname, "ads.bad.test"),
                ("dead.test", RewriteType::Cname, "nowhere.test"),
            ],
            "bad.test\n",
        );
        let m = run(&c, "nas.home.test.", RecordType::A).await;
        assert_eq!(
            m.answers[0].data,
            RData::A(A(Ipv4Addr::new(192, 168, 1, 50)))
        );
        assert_eq!(m.answers[0].ttl, 300);
        let m = run(&c, "nas.home.test.", RecordType::MX).await;
        assert_eq!(
            (m.metadata.response_code, m.answers.len()),
            (ResponseCode::NoError, 0)
        );
        let m = run(&c, "x.home.test.", RecordType::A).await;
        assert_eq!(m.answers.len(), 2);
        assert_eq!(
            m.answers[1].data,
            RData::A(A(Ipv4Addr::new(192, 168, 1, 50)))
        );
        let m = run(&c, "loop1.test.", RecordType::A).await;
        assert_eq!(
            (m.metadata.response_code, m.answers.len()),
            (ResponseCode::ServFail, 0)
        );
        let m = run(&c, "tracked.test.", RecordType::A).await;
        assert_eq!(m.answers[1].data, RData::A(A(Ipv4Addr::UNSPECIFIED)));
        let m = run(&c, "dead.test.", RecordType::A).await;
        assert_eq!(m.metadata.response_code, ResponseCode::ServFail);
    }
}
