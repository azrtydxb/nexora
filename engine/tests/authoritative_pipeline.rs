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
const SIGNED_NSEC: &[u8] = include_bytes!("../../testdata/nzf/signed-nsec-full.nzf");
const SIGNED_NSEC3: &[u8] = include_bytes!("../../testdata/nzf/signed-nsec3-full.nzf");
/// The KSK DNSKEY of the signed images, in presentation form (`testdata/nzf/gen-signed`).
const SIGNED_ANCHOR: &str = include_str!("../../testdata/nzf/signed-anchor.key");

fn start(acl: &str) -> SocketAddr {
    start_zone(acl, FULL)
}

/// An engine on a free loopback port serving `image` (an NZF1 full image of example.test. at
/// serial 2026091301).
fn start_zone(acl: &str, image: &[u8]) -> SocketAddr {
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
    let z = zstd::encode_all(image, 3).unwrap();
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
        acl_allow_cidrs: vec![acl.into()],
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

fn ask(server: SocketAddr, name: &str, cookie: bool) -> (Message, Vec<u8>) {
    let c = UdpSocket::bind("127.0.0.1:0").unwrap();
    c.set_read_timeout(Some(Duration::from_secs(3))).unwrap();
    let mut m = Message::new(0x4d34, MessageType::Query, OpCode::Query);
    m.metadata.recursion_desired = true;
    m.add_query(Query::query(Name::from_ascii(name).unwrap(), RecordType::A));
    let mut e = Edns::new();
    e.set_max_payload(1232);
    m.set_edns(e);
    let mut wire = m.to_bytes().unwrap();
    if cookie {
        // append a client cookie option (code 10, 8 octets) to the OPT RR at the end of the message
        let n = wire.len();
        let rdlen = u16::from_be_bytes([wire[n - 2], wire[n - 1]]) + 12;
        wire[n - 2..].copy_from_slice(&rdlen.to_be_bytes());
        wire.extend_from_slice(&[0, 10, 0, 8, 1, 2, 3, 4, 5, 6, 7, 8]);
    }
    c.send_to(&wire, server).unwrap();
    let mut buf = [0u8; 4096];
    let (n, _) = c.recv_from(&mut buf).unwrap();
    (Message::from_bytes(&buf[..n]).unwrap(), buf[..n].to_vec())
}

#[test]
fn hosted_names_answer_authoritatively_before_the_acl() {
    let srv = start("10.0.0.0/8");
    let (hosted, raw) = ask(srv, "www.example.test.", true);
    assert_eq!(hosted.metadata.response_code, ResponseCode::NoError);
    assert!(hosted.metadata.authoritative);
    assert!(
        !hosted.metadata.recursion_available,
        "the client is outside the ACL"
    );
    assert_eq!(hosted.answers.len(), 2);
    assert!(hosted.edns.is_some(), "OPT echoed");
    let cookie_option = [0u8, 10, 0, 24, 1, 2, 3, 4, 5, 6, 7, 8];
    assert!(
        raw.windows(cookie_option.len()).any(|w| w == cookie_option),
        "client + server cookie on authoritative answers"
    );
    let (forwarded, _) = ask(srv, "www.example.org.", false);
    assert_eq!(
        forwarded.metadata.response_code,
        ResponseCode::Refused,
        "names outside hosted zones still pass the ACL"
    );
}

/// BIND's validator accepts the engine's positive answers, NSEC/NSEC3 denials, wildcard proofs,
/// DS answers and CDS data for the pre-signed test zones, trusting only the zone's KSK.
#[test]
fn signed_answers_validate_with_delv() {
    let f: Vec<&str> = SIGNED_ANCHOR.split_whitespace().collect();
    assert_eq!(&f[..4], &["example.test.", "3600", "IN", "DNSKEY"]);
    let dir = tempfile::tempdir().unwrap();
    let anchors = dir.path().join("anchors.conf");
    std::fs::write(
        &anchors,
        format!(
            "trust-anchors {{ example.test. static-key {} {} {} \"{}\"; }};\n",
            f[4], f[5], f[6], f[7]
        ),
    )
    .unwrap();
    let positive = "; fully validated";
    let negative = "; negative response, fully validated";
    let cases = [
        ("www.example.test.", "A", positive),
        ("alias.example.test.", "A", positive),
        ("anything.wild.example.test.", "TXT", positive),
        ("sub.example.test.", "DS", positive),
        ("example.test.", "CDS", positive),
        ("example.test.", "CDNSKEY", positive),
        ("nope.example.test.", "A", negative),
        ("x.nope.example.test.", "A", negative),
        ("www.example.test.", "MX", negative),
        ("b.c.example.test.", "A", negative),
        ("anything.wild.example.test.", "A", negative),
        ("insecure.example.test.", "DS", negative),
    ];
    for (label, image) in [("nsec", SIGNED_NSEC), ("nsec3", SIGNED_NSEC3)] {
        let srv = start_zone("127.0.0.0/8", image);
        for (name, qtype, want) in cases {
            let out = std::process::Command::new("delv")
                .arg(format!("@{}", srv.ip()))
                .args(["-p", &srv.port().to_string()])
                .arg("-a")
                .arg(&anchors)
                .args(["+root=example.test", name, qtype])
                .output()
                .expect("delv (bind9-dnsutils) must be installed");
            let text = format!(
                "{}{}",
                String::from_utf8_lossy(&out.stdout),
                String::from_utf8_lossy(&out.stderr)
            );
            assert!(
                text.lines().any(|l| l == want),
                "{label} {name} {qtype}: want {want:?}, delv said:\n{text}"
            );
        }
    }
}
