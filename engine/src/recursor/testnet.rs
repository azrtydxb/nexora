//! Fake authoritative servers for resolver tests: zones from zone text, each served on its own
//! `127.0.54.x` address, all sharing one port so `RecursionParams.authority_port` reaches them.

use super::iterate::{RecursionParams, dname_record, dname_target};
use super::roothints::RootHints;
use hickory_proto::op::{Message, OpCode, ResponseCode};
use hickory_proto::rr::{Name, RData, Record, RecordType, rdata::A, rdata::CNAME, rdata::NS};
use hickory_proto::serialize::txt::Parser;
use std::collections::{HashMap, HashSet};
use std::net::{IpAddr, Ipv4Addr, SocketAddr};
use std::sync::{Arc, Mutex};
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::{TcpListener, UdpSocket};

pub struct FakeZone {
    pub origin: &'static str,
    pub ip: Ipv4Addr,
    pub text: String,
    pub behaviour: Behaviour,
}

#[derive(Clone, Copy, PartialEq)]
pub enum Behaviour {
    Normal,
    Silent,
    Refused,
    /// Normal, but every positive answer also carries `victim.<origin> A 6.6.6.6`.
    AnswerStuffing,
    /// Off-path spoofer: answers every query at once with `A 6.6.6.6`, once with a wrong ID and
    /// once with the question in lowercase (breaking 0x20), never with a matching reply.
    Spoof,
}

pub struct FakeNet {
    pub port: u16,
    pub queries: Arc<Mutex<Vec<(Ipv4Addr, Name, RecordType)>>>,
}

struct ParsedZone {
    origin: Name,
    behaviour: Behaviour,
    rrsets: HashMap<(Name, RecordType), Vec<Record>>,
    /// NS owners strictly below the origin.
    cuts: Vec<Name>,
    /// Every owner name (lowercase).
    names: HashSet<Name>,
}

impl ParsedZone {
    fn parse(z: &FakeZone) -> ParsedZone {
        let origin = Name::from_ascii(z.origin).unwrap();
        let mut rrsets: HashMap<(Name, RecordType), Vec<Record>> = HashMap::new();
        if !matches!(z.behaviour, Behaviour::Silent | Behaviour::Spoof) && !z.text.is_empty() {
            // hickory's zone parser has no DNAME support: DNAME lines are built here.
            let mut text = String::new();
            for line in z.text.lines() {
                let f: Vec<&str> = line.split_whitespace().collect();
                if f.len() == 5 && f[3] == "DNAME" {
                    let owner = absolute(f[0], &origin);
                    let rec = dname_record(owner, f[1].parse().unwrap(), &absolute(f[4], &origin));
                    rrsets
                        .entry((rec.name.to_lowercase(), RecordType::DNAME))
                        .or_default()
                        .push(rec);
                } else {
                    text.push_str(line);
                    text.push('\n');
                }
            }
            let (_, sets) = Parser::new(text, None, Some(origin.clone()))
                .parse()
                .unwrap_or_else(|e| panic!("zone {}: {e}", z.origin));
            for set in sets.values() {
                for r in set.records_without_rrsigs() {
                    rrsets
                        .entry((r.name.to_lowercase(), r.record_type()))
                        .or_default()
                        .push(r.clone());
                }
            }
        }
        let cuts = rrsets
            .keys()
            .filter(|(n, t)| *t == RecordType::NS && *n != origin)
            .map(|(n, _)| n.clone())
            .collect();
        let names = rrsets.keys().map(|(n, _)| n.clone()).collect();
        ParsedZone {
            origin,
            behaviour: z.behaviour,
            rrsets,
            cuts,
            names,
        }
    }

    fn rrset(&self, name: &Name, t: RecordType) -> Vec<Record> {
        self.rrsets
            .get(&(name.to_lowercase(), t))
            .cloned()
            .unwrap_or_default()
    }
}

fn absolute(s: &str, origin: &Name) -> Name {
    match s {
        "@" => origin.clone(),
        _ if s.ends_with('.') => Name::from_ascii(s).unwrap(),
        _ => Name::from_ascii(s).unwrap().append_domain(origin).unwrap(),
    }
}

/// `name` and each ancestor down to (and including) the root, deepest first.
fn ancestors(name: &Name) -> Vec<Name> {
    let mut out = vec![name.clone()];
    let mut n = name.clone();
    while !n.is_root() {
        n = n.base_name();
        out.push(n.clone());
    }
    out
}

fn answer(z: &ParsedZone, q: &Message) -> Message {
    let query = &q.queries[0];
    let qname = query.name().to_lowercase();
    let mut r = Message::response(q.metadata.id, OpCode::Query);
    r.queries.push(query.clone()); // echo the exact casing (0x20)
    if z.behaviour == Behaviour::Refused || !z.origin.zone_of(&qname) {
        r.metadata.response_code = ResponseCode::Refused;
        return r;
    }
    // delegation: deepest NS owner strictly below the origin that is an ancestor-or-self of qname
    if let Some(cut) = z
        .cuts
        .iter()
        .filter(|c| c.zone_of(&qname))
        .max_by_key(|c| c.num_labels())
        && !(query.query_type() == RecordType::DS && &qname == cut)
    {
        r.authorities.extend(z.rrset(cut, RecordType::NS));
        r.authorities.extend(z.rrset(cut, RecordType::DS));
        for ns in z.rrset(cut, RecordType::NS) {
            if let RData::NS(NS(target)) = &ns.data {
                // glue as written, even out of bailiwick
                r.additionals.extend(z.rrset(target, RecordType::A));
            }
        }
        return r;
    }
    r.metadata.authoritative = true;
    // DNAME at a proper ancestor inside the zone
    for anc in ancestors(&qname)
        .into_iter()
        .filter(|a| *a != qname && z.origin.zone_of(a))
    {
        if let Some(d) = z.rrset(&anc, RecordType::DNAME).first() {
            let target = dname_target(d).unwrap();
            r.answers.push(d.clone());
            let keep = qname.iter().count() - anc.iter().count();
            let synth = Name::from_labels(qname.iter().take(keep))
                .unwrap()
                .append_domain(&target)
                .unwrap();
            r.answers.push(Record::from_rdata(
                qname.clone(),
                0,
                RData::CNAME(CNAME(synth)),
            ));
            return r;
        }
    }
    let exact = z.rrset(&qname, query.query_type());
    if !exact.is_empty() {
        r.answers.extend(exact);
        if z.behaviour == Behaviour::AnswerStuffing {
            let victim = Name::from_ascii("victim")
                .unwrap()
                .append_domain(&z.origin)
                .unwrap();
            let evil = RData::A(A(Ipv4Addr::new(6, 6, 6, 6)));
            r.answers.push(Record::from_rdata(victim, 300, evil));
        }
        return r;
    }
    let cname = z.rrset(&qname, RecordType::CNAME);
    if !cname.is_empty() {
        r.answers.extend(cname);
        return r;
    }
    r.authorities.extend(z.rrset(&z.origin, RecordType::SOA));
    if !z.names.contains(&qname) && !z.names.iter().any(|n| qname.zone_of(n)) {
        r.metadata.response_code = ResponseCode::NXDomain;
    }
    r
}

type Log = Arc<Mutex<Vec<(Ipv4Addr, Name, RecordType)>>>;

/// Forged replies to `q` that a spoofing-hardened resolver must reject.
fn spoofed(q: &Message) -> Vec<Vec<u8>> {
    let query = &q.queries[0];
    let forged = |id: u16, name: Name| {
        let mut r = Message::response(id, OpCode::Query);
        r.metadata.authoritative = true;
        r.queries.push(hickory_proto::op::Query::query(
            name.clone(),
            query.query_type(),
        ));
        r.answers.push(Record::from_rdata(
            name,
            300,
            RData::A(A(Ipv4Addr::new(6, 6, 6, 6))),
        ));
        r.to_vec().unwrap()
    };
    vec![
        forged(q.metadata.id.wrapping_add(1), query.name().clone()),
        forged(q.metadata.id, query.name().to_lowercase()),
    ]
}

/// Decodes one query, logs it and builds the replies (none: stay silent).
fn handle(zones: &[ParsedZone], ip: Ipv4Addr, wire: &[u8], log: &Log) -> Vec<Vec<u8>> {
    handle_one(zones, ip, wire, log).unwrap_or_default()
}

fn handle_one(zones: &[ParsedZone], ip: Ipv4Addr, wire: &[u8], log: &Log) -> Option<Vec<Vec<u8>>> {
    let q = Message::from_vec(wire).ok()?;
    let query = q.queries.first()?;
    log.lock()
        .unwrap()
        .push((ip, query.name().clone(), query.query_type()));
    let qname = query.name().to_lowercase();
    // the deepest zone on this address that contains the name, else any zone (it refuses)
    let z = zones
        .iter()
        .filter(|z| z.origin.zone_of(&qname))
        .max_by_key(|z| z.origin.num_labels())
        .or(zones.first())?;
    match z.behaviour {
        Behaviour::Silent => None,
        Behaviour::Spoof => Some(spoofed(&q)),
        _ => Some(vec![answer(z, &q).to_vec().ok()?]),
    }
}

impl FakeNet {
    pub async fn start(zones: Vec<FakeZone>) -> FakeNet {
        let mut by_ip: Vec<(Ipv4Addr, Vec<ParsedZone>)> = Vec::new();
        for z in &zones {
            let parsed = ParsedZone::parse(z);
            match by_ip.iter_mut().find(|(ip, _)| *ip == z.ip) {
                Some((_, v)) => v.push(parsed),
                None => by_ip.push((z.ip, vec![parsed])),
            }
        }
        let (port, sockets) = bind_all(&by_ip.iter().map(|(ip, _)| *ip).collect::<Vec<_>>()).await;
        let queries: Log = Arc::default();
        for ((ip, parsed), (udp, tcp)) in by_ip.into_iter().zip(sockets) {
            let parsed = Arc::new(parsed);
            let (p, log) = (parsed.clone(), queries.clone());
            tokio::spawn(async move {
                let mut buf = [0u8; 4096];
                while let Ok((n, peer)) = udp.recv_from(&mut buf).await {
                    for reply in handle(&p, ip, &buf[..n], &log) {
                        let _ = udp.send_to(&reply, peer).await;
                    }
                }
            });
            let log = queries.clone();
            tokio::spawn(async move {
                while let Ok((mut s, _)) = tcp.accept().await {
                    let (p, log) = (parsed.clone(), log.clone());
                    tokio::spawn(async move {
                        let Ok(len) = s.read_u16().await else { return };
                        let mut buf = vec![0u8; usize::from(len)];
                        if s.read_exact(&mut buf).await.is_err() {
                            return;
                        }
                        if let Some(reply) = handle(&p, ip, &buf, &log).into_iter().next() {
                            let _ = s.write_u16(reply.len() as u16).await;
                            let _ = s.write_all(&reply).await;
                        }
                    });
                }
            });
        }
        FakeNet { port, queries }
    }

    pub fn params(&self, root_ip: Ipv4Addr) -> RecursionParams {
        RecursionParams {
            root_hints: Arc::new(RootHints {
                servers: vec![(
                    Name::from_ascii("root.fake.").unwrap(),
                    vec![IpAddr::V4(root_ip)],
                )],
            }),
            qname_minimisation: true,
            max_upstream_queries: 100,
            max_delegation_depth: 32,
            authority_port: self.port,
            cache_max_bytes: 0,
        }
    }

    pub fn queries_to(&self, ip: Ipv4Addr) -> usize {
        self.queries
            .lock()
            .unwrap()
            .iter()
            .filter(|(i, _, _)| *i == ip)
            .count()
    }
}

/// Binds UDP and TCP on every address at one port: the first address picks the port, the rest
/// follow; the whole set is retried when another process holds that port on a later address.
async fn bind_all(ips: &[Ipv4Addr]) -> (u16, Vec<(UdpSocket, TcpListener)>) {
    'attempt: for _ in 0..20 {
        let first = UdpSocket::bind(SocketAddr::new(IpAddr::V4(ips[0]), 0))
            .await
            .unwrap();
        let port = first.local_addr().unwrap().port();
        let mut out = Vec::new();
        let mut first = Some(first);
        for ip in ips {
            let addr = SocketAddr::new(IpAddr::V4(*ip), port);
            let udp = match first.take() {
                Some(s) => s,
                None => match UdpSocket::bind(addr).await {
                    Ok(s) => s,
                    Err(_) => continue 'attempt,
                },
            };
            let Ok(tcp) = TcpListener::bind(addr).await else {
                continue 'attempt;
            };
            out.push((udp, tcp));
        }
        return (port, out);
    }
    panic!("could not bind the fake network on one shared port");
}
