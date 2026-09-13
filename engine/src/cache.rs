//! Wire-format response cache: entries hold upstream bytes ready to patch and copy.

use crate::edns::{self, ReplyOpt};
use crate::wire::{self, NameKey, QueryView};
use std::sync::Arc;

const HEADER_LEN: usize = 12;
const FLAG_TC: u8 = 0x02;
const FLAG_RD: u8 = 0x01;
const STALE_TTL: u32 = 30;

#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub struct CacheKey {
    pub name: NameKey,
    pub qtype: u16,
    pub qclass: u16,
    pub do_bit: bool,
    pub cd_bit: bool,
}

impl CacheKey {
    pub fn from_query(q: &QueryView<'_>) -> CacheKey {
        CacheKey {
            name: q.key,
            qtype: q.qtype,
            qclass: q.qclass,
            do_bit: q.do_bit(),
            cd_bit: q.cd(),
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq)]
pub struct CacheSettings {
    pub max_bytes: u64,
    pub min_ttl: u32,
    pub max_ttl: u32,
    pub negative_max_ttl: u32,
    pub stale_window: u32,
}

pub struct CachedResponse {
    /// Upstream response without its OPT RR, question name lowercased.
    pub wire: Box<[u8]>,
    pub question_name_len: usize,
    pub ttl_offsets: Box<[u16]>,
    pub inserted_at: u32,
    pub ttl: u32,
    pub stale_deadline: u32,
    pub rcode: u8,
}

pub enum Lookup {
    Fresh(Arc<CachedResponse>),
    Stale(Arc<CachedResponse>),
    Miss,
}

#[derive(Debug, PartialEq)]
pub enum InsertOutcome {
    Inserted { ttl: u32 },
    NotCacheable(&'static str),
}

#[derive(Clone)]
struct EntryWeighter;

impl quick_cache::Weighter<CacheKey, Arc<CachedResponse>> for EntryWeighter {
    fn weight(&self, _: &CacheKey, v: &Arc<CachedResponse>) -> u64 {
        (v.wire.len() + v.ttl_offsets.len() * 2 + 96) as u64
    }
}

pub struct Cache {
    settings: CacheSettings,
    inner: quick_cache::sync::Cache<CacheKey, Arc<CachedResponse>, EntryWeighter>,
}

impl Cache {
    pub fn new(settings: CacheSettings) -> Cache {
        let estimated_items = (settings.max_bytes / 256).max(1) as usize;
        let inner = quick_cache::sync::Cache::with_weighter(
            estimated_items,
            settings.max_bytes,
            EntryWeighter,
        );
        Cache { settings, inner }
    }

    pub fn settings(&self) -> CacheSettings {
        self.settings
    }

    pub fn lookup(&self, key: &CacheKey, now: u32) -> Lookup {
        match self.inner.get(key) {
            Some(e) if now < e.inserted_at.saturating_add(e.ttl) => Lookup::Fresh(e),
            Some(e) if now < e.stale_deadline => Lookup::Stale(e),
            _ => Lookup::Miss,
        }
    }

    /// Caches `upstream` (the response to `query`) when RFC 2308 / the settings allow it.
    pub fn insert(
        &self,
        key: CacheKey,
        upstream: &[u8],
        query: &QueryView<'_>,
        now: u32,
    ) -> InsertOutcome {
        let Ok(info) = wire::walk_response(upstream, query) else {
            return InsertOutcome::NotCacheable("malformed");
        };
        if info.tc {
            return InsertOutcome::NotCacheable("truncated");
        }
        let s = &self.settings;
        let ttl = match info.rcode {
            wire::RCODE_NOERROR if info.ancount > 0 => match info.min_ttl {
                Some(0) | None => return InsertOutcome::NotCacheable("ttl zero"),
                Some(t) => t.clamp(s.min_ttl, s.max_ttl),
            },
            wire::RCODE_NOERROR | wire::RCODE_NXDOMAIN => match info.negative_ttl {
                None => return InsertOutcome::NotCacheable("negative without soa"),
                Some(t) => t.min(s.negative_max_ttl),
            },
            _ => return InsertOutcome::NotCacheable("rcode"),
        };
        if ttl == 0 {
            return InsertOutcome::NotCacheable("ttl zero");
        }
        let mut bytes = match info.opt_range {
            None => upstream.to_vec(),
            // Only a trailing OPT is stripped, so no compression pointer or TTL offset moves.
            Some(r) if r.end == upstream.len() => {
                let mut b = upstream[..r.start].to_vec();
                let arcount = u16::from_be_bytes([b[10], b[11]]) - 1;
                b[10..12].copy_from_slice(&arcount.to_be_bytes());
                b
            }
            Some(_) => return InsertOutcome::NotCacheable("opt not last"),
        };
        let name = query.key.as_wire();
        bytes[HEADER_LEN..HEADER_LEN + name.len()].copy_from_slice(name);
        let entry = CachedResponse {
            wire: bytes.into_boxed_slice(),
            question_name_len: name.len(),
            ttl_offsets: info.ttl_offsets.into_boxed_slice(),
            inserted_at: now,
            ttl,
            stale_deadline: now.saturating_add(ttl).saturating_add(s.stale_window),
            rcode: info.rcode,
        };
        self.inner.insert(key, Arc::new(entry));
        InsertOutcome::Inserted { ttl }
    }

    pub fn entries(&self) -> u64 {
        self.inner.len() as u64
    }

    pub fn bytes(&self) -> u64 {
        self.inner.weight()
    }
}

pub enum ServeMode {
    Fresh,
    Stale,
}

/// Writes `entry` as the reply to `q` into `out`; allocation-free. Returns 0
/// when even the truncated reply does not fit `out`.
pub fn write_cached(
    entry: &CachedResponse,
    q: &QueryView<'_>,
    now: u32,
    mode: ServeMode,
    out: &mut [u8],
    limit: usize,
    opt: Option<&ReplyOpt>,
) -> usize {
    let opt_len = opt.map_or(0, ReplyOpt::wire_len);
    let question_end = HEADER_LEN + entry.question_name_len + 4;
    let wire = &entry.wire;
    let (mut pos, arcount) = if wire.len() + opt_len > limit.min(out.len()) {
        if question_end + opt_len > out.len() {
            return 0;
        }
        out[..question_end].copy_from_slice(&wire[..question_end]);
        out[2] |= FLAG_TC;
        out[6..12].fill(0);
        (question_end, 0)
    } else {
        out[..wire.len()].copy_from_slice(wire);
        let elapsed = now.saturating_sub(entry.inserted_at);
        for &off in entry.ttl_offsets.iter() {
            let off = off as usize;
            let ttl = match mode {
                ServeMode::Fresh => {
                    u32::from_be_bytes([out[off], out[off + 1], out[off + 2], out[off + 3]])
                        .saturating_sub(elapsed)
                }
                ServeMode::Stale => STALE_TTL,
            };
            out[off..off + 4].copy_from_slice(&ttl.to_be_bytes());
        }
        (wire.len(), u16::from_be_bytes([wire[10], wire[11]]))
    };
    out[..2].copy_from_slice(&q.id.to_be_bytes());
    out[2] = (out[2] & !FLAG_RD) | (q.flags >> 8) as u8 & FLAG_RD;
    out[HEADER_LEN..HEADER_LEN + q.qname.len()].copy_from_slice(q.qname);
    if let Some(opt) = opt {
        pos += edns::write_opt(&mut out[pos..], opt);
        out[10..12].copy_from_slice(&(arcount + 1).to_be_bytes());
    }
    pos
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::wire::parse_query;
    use hickory_proto::op::{Message, MessageType, OpCode, Query, ResponseCode};
    use hickory_proto::rr::rdata::{A, CNAME, SOA};
    use hickory_proto::rr::{Name, RData, Record, RecordType};
    use hickory_proto::serialize::binary::{BinDecodable, BinEncodable};

    fn settings() -> CacheSettings {
        CacheSettings {
            max_bytes: 1 << 20,
            min_ttl: 0,
            max_ttl: 86400,
            negative_max_ttl: 3600,
            stale_window: 60,
        }
    }
    fn q(name: &str, id: u16) -> Vec<u8> {
        let mut m = Message::new(id, MessageType::Query, OpCode::Query);
        m.metadata.recursion_desired = true;
        m.add_query(Query::query(Name::from_ascii(name).unwrap(), RecordType::A));
        m.to_bytes().unwrap()
    }
    fn answer(query: &[u8], ttls: &[u32], rcode: ResponseCode) -> Vec<u8> {
        let mut m = Message::from_bytes(query).unwrap();
        m.metadata.message_type = MessageType::Response;
        m.metadata.response_code = rcode;
        m.metadata.recursion_available = true;
        let name = m.queries[0].name().clone();
        for (i, t) in ttls.iter().enumerate() {
            m.add_answer(Record::from_rdata(
                name.clone(),
                *t,
                RData::A(A::new(192, 0, 2, i as u8 + 1)),
            ));
        }
        m.to_bytes().unwrap()
    }

    #[test]
    fn serves_with_patched_id_client_casing_and_decremented_ttl() {
        let c = Cache::new(settings());
        let q1 = q("example.com.", 1);
        let v1 = parse_query(&q1).unwrap();
        let resp = answer(&q1, &[300, 120], ResponseCode::NoError);
        assert_eq!(
            c.insert(CacheKey::from_query(&v1), &resp, &v1, 1000),
            InsertOutcome::Inserted { ttl: 120 }
        );
        let q2 = q("ExAmPlE.CoM.", 0x4242);
        let v2 = parse_query(&q2).unwrap();
        let Lookup::Fresh(e) = c.lookup(&CacheKey::from_query(&v2), 1030) else {
            panic!("expected fresh")
        };
        let mut out = [0u8; 1232];
        let n = write_cached(&e, &v2, 1030, ServeMode::Fresh, &mut out, 512, None);
        let m = Message::from_bytes(&out[..n]).unwrap();
        assert_eq!(m.metadata.id, 0x4242);
        assert_eq!(m.queries[0].name().to_ascii(), "ExAmPlE.CoM.");
        let ttls: Vec<u32> = m.answers.iter().map(|r| r.ttl).collect();
        assert_eq!(ttls, vec![270, 90]);
    }

    #[test]
    fn ttl_zero_servfail_and_tc_are_not_cached() {
        let c = Cache::new(settings());
        let q1 = q("zero.example.", 1);
        let v = parse_query(&q1).unwrap();
        let k = CacheKey::from_query(&v);
        assert!(matches!(
            c.insert(k, &answer(&q1, &[0], ResponseCode::NoError), &v, 10),
            InsertOutcome::NotCacheable(_)
        ));
        assert!(matches!(
            c.insert(k, &answer(&q1, &[], ResponseCode::ServFail), &v, 10),
            InsertOutcome::NotCacheable(_)
        ));
        let mut tc = answer(&q1, &[60], ResponseCode::NoError);
        tc[2] |= 0x02;
        assert!(matches!(
            c.insert(k, &tc, &v, 10),
            InsertOutcome::NotCacheable(_)
        ));
        assert!(matches!(c.lookup(&k, 10), Lookup::Miss));
    }

    #[test]
    fn negative_answer_uses_soa_minimum_and_no_soa_is_not_cached() {
        let c = Cache::new(settings());
        let q1 = q("nx.example.", 1);
        let v = parse_query(&q1).unwrap();
        let k = CacheKey::from_query(&v);
        assert!(matches!(
            c.insert(k, &answer(&q1, &[], ResponseCode::NXDomain), &v, 10),
            InsertOutcome::NotCacheable(_)
        ));
        let mut m = Message::from_bytes(&answer(&q1, &[], ResponseCode::NXDomain)).unwrap();
        let soa = SOA::new(
            Name::from_ascii("ns.example.").unwrap(),
            Name::from_ascii("h.example.").unwrap(),
            1,
            2,
            3,
            4,
            120,
        );
        m.add_authority(Record::from_rdata(
            Name::from_ascii("example.").unwrap(),
            900,
            RData::SOA(soa),
        ));
        assert_eq!(
            c.insert(k, &m.to_bytes().unwrap(), &v, 10),
            InsertOutcome::Inserted { ttl: 120 }
        );
    }

    #[test]
    fn expired_entry_is_stale_within_window_then_miss() {
        let c = Cache::new(settings());
        let q1 = q("stale.example.", 1);
        let v = parse_query(&q1).unwrap();
        let k = CacheKey::from_query(&v);
        c.insert(k, &answer(&q1, &[10], ResponseCode::NoError), &v, 100);
        assert!(matches!(c.lookup(&k, 105), Lookup::Fresh(_)));
        let Lookup::Stale(e) = c.lookup(&k, 111) else {
            panic!("expected stale")
        };
        let mut out = [0u8; 512];
        let n = write_cached(&e, &v, 111, ServeMode::Stale, &mut out, 512, None);
        let m = Message::from_bytes(&out[..n]).unwrap();
        assert_eq!(m.answers[0].ttl, 30);
        assert!(matches!(c.lookup(&k, 171), Lookup::Miss));
    }

    #[test]
    fn oversize_for_limit_sets_tc_with_empty_answer() {
        let c = Cache::new(settings());
        let q1 = q("big.example.", 1);
        let v = parse_query(&q1).unwrap();
        let ttls: Vec<u32> = (0..40).map(|_| 300).collect();
        let resp = answer(&q1, &ttls, ResponseCode::NoError);
        assert!(resp.len() > 512);
        c.insert(CacheKey::from_query(&v), &resp, &v, 1);
        let Lookup::Fresh(e) = c.lookup(&CacheKey::from_query(&v), 1) else {
            panic!()
        };
        let mut out = [0u8; 1232];
        let n = write_cached(&e, &v, 1, ServeMode::Fresh, &mut out, 512, None);
        let m = Message::from_bytes(&out[..n]).unwrap();
        assert!(m.metadata.truncation);
        assert_eq!(m.answers.len(), 0);
        let n = write_cached(&e, &v, 1, ServeMode::Fresh, &mut out, 1232, None);
        assert!(!Message::from_bytes(&out[..n]).unwrap().metadata.truncation);
    }

    #[test]
    fn keys_differ_by_do_and_cd_bits() {
        let q1 = q("k.example.", 1);
        let v = parse_query(&q1).unwrap();
        let a = CacheKey::from_query(&v);
        let b = CacheKey { do_bit: true, ..a };
        let d = CacheKey { cd_bit: true, ..a };
        assert_ne!(a, b);
        assert_ne!(a, d);
        let _ = CNAME(Name::root());
    }
}
