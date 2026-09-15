//! Recursion and authoritative access are separate: hosted names are checked against the zone's
//! allow-query ACL (or the snapshot default), everything else against the recursion ACL.

use hickory_proto::op::{Edns, Message, MessageType, OpCode, Query, ResponseCode};
use hickory_proto::rr::{Name, RecordType};
use hickory_proto::serialize::binary::{BinDecodable, BinEncodable};
use nexora_engine::bootstrap::Bootstrap;
use nexora_engine::proto::*;
use nexora_engine::server::tls::CertStore;
use nexora_engine::server::{Shared, spawn_workers};
use nexora_engine::snapshot::{ApplyOutcome, DirBlobs, apply};
use sha2::Digest;
use std::net::{SocketAddr, UdpSocket};
use std::sync::Arc;
use std::time::Duration;

const FULL: &[u8] = include_bytes!("../../testdata/nzf/basic-full.nzf");

/// An engine on a free loopback port serving example.test. with recursion ACL `recursion`, the
/// authoritative default `authoritative` (`None`: a pre-M6 snapshot without the list) and the zone
/// override `zone_allow`.
fn start_split(recursion: &str, authoritative: Option<&str>, zone_allow: &[&str]) -> SocketAddr {
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
    let z = zstd::encode_all(FULL, 3).unwrap();
    let sha = hex::encode(sha2::Sha256::digest(&z));
    std::fs::write(dir.join(&sha), &z).unwrap();
    let shared = Shared::new(2);
    let snap = ConfigSnapshot {
        version: 1,
        resolver: Some(ResolverConfig {
            strategy: UpstreamStrategy::Ordered as i32,
            ..Default::default()
        }),
        cache: Some(CacheConfig {
            max_bytes: 8 << 20,
            max_ttl: 86400,
            negative_max_ttl: 3600,
            ..Default::default()
        }),
        acl_allow_cidrs: vec![recursion.into()],
        authoritative_allow_cidrs: authoritative.map(|a| vec![a.into()]).unwrap_or_default(),
        authoritative_acl_set: authoritative.is_some(),
        filter: Some(FilterConfig {
            block_mode: BlockMode::NullIp as i32,
            block_ttl: 60,
            ..Default::default()
        }),
        telemetry: Some(TelemetryConfig::default()),
        auth_zones: vec![AuthZone {
            name: "example.test.".into(),
            kind: AuthZoneKind::Primary as i32,
            serial: 2026091301,
            image: Some(BlobRef {
                sha256: sha,
                size: z.len() as u64,
                name: "example.test.@2026091301".into(),
            }),
            image_serial: 2026091301,
            allow_query_cidrs: zone_allow.iter().map(|c| (*c).into()).collect(),
            ..Default::default()
        }],
        ..Default::default()
    };
    assert!(matches!(
        apply(&shared.runtime, snap, &DirBlobs { dir: dir.clone() }, None),
        ApplyOutcome::Applied { .. }
    ));
    spawn_workers(shared, &boot, Arc::new(CertStore::new())).unwrap();
    std::thread::sleep(Duration::from_millis(200));
    format!("127.0.0.1:{port}").parse().unwrap()
}

fn ask(server: SocketAddr, name: &str, _cookie: bool) -> (Message, Vec<u8>) {
    let c = UdpSocket::bind("127.0.0.1:0").unwrap();
    c.set_read_timeout(Some(Duration::from_secs(3))).unwrap();
    let mut m = Message::new(0x4d34, MessageType::Query, OpCode::Query);
    m.metadata.recursion_desired = true;
    m.add_query(Query::query(Name::from_ascii(name).unwrap(), RecordType::A));
    let mut e = Edns::new();
    e.set_max_payload(1232);
    m.set_edns(e);
    c.send_to(&m.to_bytes().unwrap(), server).unwrap();
    let mut buf = [0u8; 4096];
    let (n, _) = c.recv_from(&mut buf).unwrap();
    (Message::from_bytes(&buf[..n]).unwrap(), buf[..n].to_vec())
}

#[test]
fn authoritative_acl_order_and_ra() {
    // Client is 127.0.0.1. Recursion ACL excludes it; authoritative default is "any".
    let srv = start_split("10.0.0.0/8", Some("0.0.0.0/0"), &[]);
    let (hosted, _) = ask(srv, "www.example.test.", false);
    assert_eq!(hosted.metadata.response_code, ResponseCode::NoError);
    assert!(
        !hosted.metadata.recursion_available,
        "RA only for recursion clients"
    );
    let (other, _) = ask(srv, "www.example.org.", false);
    assert_eq!(
        other.metadata.response_code,
        ResponseCode::Refused,
        "no recursion for outside clients"
    );
    // Zone override excludes the client: hosted names are refused too.
    let srv = start_split("10.0.0.0/8", Some("0.0.0.0/0"), &["192.168.0.0/16"]);
    assert_eq!(
        ask(srv, "www.example.test.", false)
            .0
            .metadata
            .response_code,
        ResponseCode::Refused
    );
    // Inside client (recursion allowed) with a zone override that allows it: answer with RA.
    let srv = start_split("127.0.0.0/8", Some("192.0.2.0/24"), &["127.0.0.1/32"]);
    let (inside, _) = ask(srv, "www.example.test.", false);
    assert_eq!(inside.metadata.response_code, ResponseCode::NoError);
    assert!(inside.metadata.recursion_available);
}

#[test]
fn snapshot_without_authoritative_acl_answers_every_client() {
    let srv = start_split("10.0.0.0/8", None, &[]); // pre-M6 management: authoritative_acl_set false
    assert_eq!(
        ask(srv, "www.example.test.", false)
            .0
            .metadata
            .response_code,
        ResponseCode::NoError
    );
    let srv = start_split("10.0.0.0/8", Some("192.0.2.0/24"), &[]);
    assert_eq!(
        ask(srv, "www.example.test.", false)
            .0
            .metadata
            .response_code,
        ResponseCode::Refused,
        "the set flag applies the list"
    );
}
