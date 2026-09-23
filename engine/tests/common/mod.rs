//! Shared helpers for handler-level tests: a static answerer, requests, DNS messages, an ODoH
//! client built on `odoh_rs`, and a full engine with an mDNS gateway injected.
// Each test binary uses its own subset of these helpers.
#![allow(dead_code)]

use bytes::Bytes;
use hickory_proto::op::{Message, MessageType, OpCode, Query};
use hickory_proto::rr::rdata::A;
use hickory_proto::rr::{DNSClass, Name, RData, Record, RecordType};
use http::{Request, Response};
use http_body_util::{BodyExt, Full};
use nexora_engine::bootstrap::Bootstrap;
use nexora_engine::edns::Transport;
use nexora_engine::mdns::gateway::Gateway;
use nexora_engine::proto;
use nexora_engine::recursor::dispatch::ResolutionRuntime;
use nexora_engine::runtime::Runtime;
use nexora_engine::server::tls::CertStore;
use nexora_engine::server::{Answerer, ClientInfo, Shared, spawn_workers};
use nexora_engine::snapshot::{self, ApplyOutcome, DirBlobs};
use odoh_rs::{
    ObliviousDoHConfigs, ObliviousDoHMessage, ObliviousDoHMessagePlaintext, OdohSecret, compose,
    decrypt_response, encrypt_query, parse,
};
use std::net::{Ipv4Addr, SocketAddr, UdpSocket};
use std::path::PathBuf;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::{Arc, Mutex, OnceLock};
use std::time::Duration;

/// Answers every query for `name` with one A record; other names get an empty NOERROR.
pub struct StaticAnswerer {
    pub name: Name,
    pub addr: Ipv4Addr,
    /// What `recursion_allowed` returns for every client.
    pub recursion: bool,
}

impl StaticAnswerer {
    pub fn a(name: &str, addr: &str) -> StaticAnswerer {
        StaticAnswerer {
            name: Name::from_ascii(name).unwrap(),
            addr: addr.parse().unwrap(),
            recursion: true,
        }
    }
}

impl Answerer for StaticAnswerer {
    async fn answer(&self, _client: ClientInfo, query: &[u8], out: &mut Vec<u8>) {
        out.clear();
        let Ok(q) = Message::from_vec(query) else {
            return;
        };
        let mut m = q.clone();
        m.metadata.message_type = MessageType::Response;
        if q.queries.first().map(|q| q.name()) == Some(&self.name) {
            m.answers.push(Record::from_rdata(
                self.name.clone(),
                60,
                RData::A(A(self.addr)),
            ));
        }
        *out = m.to_vec().unwrap();
    }

    fn recursion_allowed(&self, _client: ClientInfo) -> bool {
        self.recursion
    }
}

pub fn client_info(addr: &str) -> ClientInfo {
    ClientInfo {
        addr: addr.parse().unwrap(),
        transport: Transport::Doh,
    }
}

pub fn get(uri: &str) -> Request<Full<Bytes>> {
    Request::get(uri).body(Full::new(Bytes::new())).unwrap()
}

pub async fn body(resp: Response<Full<Bytes>>) -> Vec<u8> {
    resp.into_body()
        .collect()
        .await
        .unwrap()
        .to_bytes()
        .to_vec()
}

pub fn query(name: &str, qtype: u16) -> Vec<u8> {
    let mut m = Message::query();
    m.metadata.id = 0x4242;
    m.metadata.recursion_desired = true;
    m.add_query(Query::query(
        Name::from_ascii(name).unwrap(),
        RecordType::from(qtype),
    ));
    m.to_vec().unwrap()
}

pub fn has_a(response: &[u8], addr: &str) -> bool {
    let want: Ipv4Addr = addr.parse().unwrap();
    Message::from_vec(response)
        .unwrap()
        .answers
        .iter()
        .any(|r| r.data == RData::A(A(want)))
}

/// A TCP port on 127.0.0.1 nothing listens on: bound once, dropped, and returned on every call.
pub fn closed_port() -> u16 {
    static PORT: OnceLock<u16> = OnceLock::new();
    *PORT.get_or_init(|| {
        let l = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
        l.local_addr().unwrap().port()
    })
}

/// Encrypts `query` to the first supported config of `configs` (an RFC 9230 §6 body).
pub fn odoh_client_query(
    configs: &[u8],
    query: &[u8],
) -> (Vec<u8>, ObliviousDoHMessagePlaintext, OdohSecret) {
    let cfgs: ObliviousDoHConfigs = parse(&mut Bytes::copy_from_slice(configs)).unwrap();
    let cfg = cfgs
        .supported()
        .into_iter()
        .next()
        .expect("a supported config");
    let plain = ObliviousDoHMessagePlaintext::new(query, 0);
    let (msg, secret) = encrypt_query(&plain, &cfg.into(), &mut rand::rng()).unwrap();
    (compose(&msg).unwrap().to_vec(), plain, secret)
}

/// Decrypts the target's sealed response to the plaintext DNS message.
pub fn odoh_client_open(
    plain: &ObliviousDoHMessagePlaintext,
    secret: OdohSecret,
    sealed: &[u8],
) -> Vec<u8> {
    let msg: ObliviousDoHMessage = parse(&mut Bytes::copy_from_slice(sealed)).unwrap();
    decrypt_response(plain, &msg, secret)
        .unwrap()
        .into_msg()
        .to_vec()
}

/// A UDP upstream answering every query with one A record `addr` (TTL 300), counting queries.
pub fn fake_upstream_a(hits: Arc<AtomicUsize>, addr: &str) -> SocketAddr {
    let want: Ipv4Addr = addr.parse().unwrap();
    let s = UdpSocket::bind("127.0.0.1:0").unwrap();
    let local = s.local_addr().unwrap();
    std::thread::spawn(move || {
        let mut buf = [0u8; 4096];
        loop {
            let Ok((n, peer)) = s.recv_from(&mut buf) else {
                return;
            };
            hits.fetch_add(1, Ordering::SeqCst);
            let Ok(mut m) = Message::from_vec(&buf[..n]) else {
                continue;
            };
            m.metadata.message_type = MessageType::Response;
            m.metadata.recursion_available = true;
            m.edns = None;
            let name = m.queries[0].name().clone();
            m.answers
                .push(Record::from_rdata(name, 300, RData::A(A(want))));
            let _ = s.send_to(&m.to_vec().unwrap(), peer);
        }
    });
    local
}

/// A unicast stand-in for the mDNS group answering legacy queries (RFC 6762 §6.7) for the names
/// in `records` with one A record (cache-flush class, TTL 120); other names get no reply. Counts
/// every query.
pub fn unicast_mdns_responder(hits: Arc<AtomicUsize>, records: &[(&str, [u8; 4])]) -> SocketAddr {
    let records: Vec<(Name, Ipv4Addr)> = records
        .iter()
        .map(|(n, a)| (Name::from_ascii(n).unwrap(), Ipv4Addr::from(*a)))
        .collect();
    let s = UdpSocket::bind("127.0.0.1:0").unwrap();
    let local = s.local_addr().unwrap();
    std::thread::spawn(move || {
        let mut buf = [0u8; 1500];
        loop {
            let Ok((n, peer)) = s.recv_from(&mut buf) else {
                return;
            };
            hits.fetch_add(1, Ordering::SeqCst);
            let Ok(q) = Message::from_vec(&buf[..n]) else {
                continue;
            };
            let Some(question) = q.queries.first() else {
                continue;
            };
            let Some((name, addr)) = records
                .iter()
                .find(|(name, _)| name.eq_ignore_root(question.name()))
            else {
                continue;
            };
            let mut m = q.clone();
            m.metadata.message_type = MessageType::Response;
            m.metadata.authoritative = true;
            let mut rr = Record::from_rdata(name.clone(), 120, RData::A(A(*addr)));
            rr.dns_class = DNSClass::from(0x8001);
            m.answers.push(rr);
            let _ = s.send_to(&m.to_vec().unwrap(), peer);
        }
    });
    local
}

pub fn forward_zone(domain: &str, server: SocketAddr) -> proto::ForwardZone {
    proto::ForwardZone {
        domain: domain.into(),
        addresses: vec![server.to_string()],
        validate: false,
    }
}

/// The snapshot and blob directory each engine of `start_engine_with` applied, by `Shared` address.
static ENGINES: Mutex<Vec<(usize, proto::ConfigSnapshot, PathBuf)>> = Mutex::new(Vec::new());

/// The `server_pipeline.rs` engine (forward mode to `upstream`, recursion ACL `acl`, UDP and TCP
/// on one port) with `hook` editing the snapshot before it is applied.
pub fn start_engine_with(
    upstream: SocketAddr,
    acl: &str,
    hook: impl FnOnce(&mut proto::ConfigSnapshot),
) -> (SocketAddr, Arc<Shared>) {
    use proto::*;
    let port = std::net::TcpListener::bind("127.0.0.1:0")
        .unwrap()
        .local_addr()
        .unwrap()
        .port();
    let dir = tempfile::tempdir().unwrap().keep();
    let boot: Bootstrap = toml::from_str(&format!(
        "node_name = \"t\"\nstate_dir = \"{}\"\nlisten_udp = [\"127.0.0.1:{port}\"]\nlisten_tcp = [\"127.0.0.1:{port}\"]\nworkers = 2\nstandalone_snapshot = \"x\"\n",
        dir.display()
    ))
    .unwrap();
    let shared = Shared::new(2);
    let mut snap = ConfigSnapshot {
        version: 1,
        resolver: Some(ResolverConfig {
            strategy: UpstreamStrategy::Ordered as i32,
            ..Default::default()
        }),
        cache: Some(CacheConfig {
            max_bytes: 8 << 20,
            min_ttl: 0,
            max_ttl: 86400,
            negative_max_ttl: 3600,
            stale_window: 0,
        }),
        upstreams: vec![Upstream {
            id: "u".into(),
            name: "u".into(),
            protocol: UpstreamProtocol::Udp as i32,
            address: upstream.to_string(),
            timeout_ms: 500,
            ..Default::default()
        }],
        acl_allow_cidrs: vec![acl.into()],
        filter: Some(FilterConfig {
            block_mode: BlockMode::NullIp as i32,
            block_ttl: 60,
            ..Default::default()
        }),
        telemetry: Some(TelemetryConfig::default()),
        ..Default::default()
    };
    hook(&mut snap);
    assert!(matches!(
        snapshot::apply(
            &shared.runtime,
            snap.clone(),
            &DirBlobs { dir: dir.clone() },
            None
        ),
        ApplyOutcome::Applied { .. }
    ));
    ENGINES
        .lock()
        .unwrap()
        .push((Arc::as_ptr(&shared) as usize, snap, dir));
    spawn_workers(shared.clone(), &boot, Arc::new(CertStore::new())).unwrap();
    std::thread::sleep(Duration::from_millis(200));
    (format!("127.0.0.1:{port}").parse().unwrap(), shared)
}

/// Replaces the engine's runtime with one built from the same snapshot whose resolution uses the
/// gateway `gw` (`None`: mDNS off).
pub fn inject_gateway(shared: &Arc<Shared>, gw: Option<Arc<Gateway>>) {
    let engines = ENGINES.lock().unwrap();
    let (_, snap, dir) = engines
        .iter()
        .find(|(p, _, _)| *p == Arc::as_ptr(shared) as usize)
        .expect("an engine of start_engine_with");
    let blobs = DirBlobs { dir: dir.clone() };
    let mut rt = Runtime::build(snap, &blobs, Some(&shared.runtime.load())).unwrap();
    let mut resolution = ResolutionRuntime::build(snap, &blobs).unwrap();
    resolution.mdns = gw;
    rt.resolution = Arc::new(resolution);
    shared.runtime.store(Arc::new(rt));
}

/// One UDP question (RD=1) to `server`; the response with the query's ID.
pub fn ask(server: SocketAddr, name: &str, qtype: RecordType) -> Message {
    let c = UdpSocket::bind("127.0.0.1:0").unwrap();
    c.set_read_timeout(Some(Duration::from_secs(3))).unwrap();
    let id: u16 = rand::random();
    let mut m = Message::new(id, MessageType::Query, OpCode::Query);
    m.metadata.recursion_desired = true;
    m.add_query(Query::query(Name::from_ascii(name).unwrap(), qtype));
    c.send_to(&m.to_vec().unwrap(), server).unwrap();
    let mut buf = [0u8; 4096];
    let (n, _) = c.recv_from(&mut buf).unwrap();
    let r = Message::from_vec(&buf[..n]).unwrap();
    assert_eq!(r.metadata.id, id);
    r
}
