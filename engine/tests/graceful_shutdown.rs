//! The engine binary's readiness endpoint and SIGTERM drain: not ready at once, every transport
//! served for `shutdown_drain_seconds`, then no new connections and a clean exit.

use nexora_engine::proto::*;
use prost::Message;
use std::io::{BufRead, BufReader, Read, Write};
use std::net::{SocketAddr, TcpStream, UdpSocket};
use std::process::{Child, Command, Stdio};
use std::time::{Duration, Instant};

const DRAIN: Duration = Duration::from_secs(3);

fn snapshot() -> ConfigSnapshot {
    ConfigSnapshot {
        version: 1,
        resolver: Some(ResolverConfig {
            strategy: UpstreamStrategy::Ordered as i32,
            ..Default::default()
        }),
        cache: Some(CacheConfig {
            max_bytes: 4 << 20,
            max_ttl: 86400,
            negative_max_ttl: 3600,
            ..Default::default()
        }),
        // Nothing listens there: forwarded queries end in SERVFAIL, which is still an answer.
        upstreams: vec![Upstream {
            id: "u1".into(),
            name: "none".into(),
            protocol: UpstreamProtocol::Udp as i32,
            address: "127.0.0.1:9".into(),
            timeout_ms: 100,
            ..Default::default()
        }],
        acl_allow_cidrs: vec!["127.0.0.0/8".into()],
        filter: Some(FilterConfig {
            block_mode: BlockMode::NullIp as i32,
            ..Default::default()
        }),
        telemetry: Some(TelemetryConfig::default()),
        ..Default::default()
    }
}

struct Engine {
    child: Child,
    udp: SocketAddr,
    tcp: SocketAddr,
    metrics: SocketAddr,
}

impl Drop for Engine {
    fn drop(&mut self) {
        let _ = self.child.kill();
        let _ = self.child.wait();
    }
}

fn start(dir: &std::path::Path) -> Engine {
    let snap = dir.join("snapshot.binpb");
    std::fs::write(&snap, snapshot().encode_to_vec()).unwrap();
    let config = dir.join("engine.toml");
    std::fs::write(
        &config,
        format!(
            "node_name = \"drain\"\nstate_dir = \"{0}/state\"\nlisten_udp = [\"127.0.0.1:0\"]\nlisten_tcp = [\"127.0.0.1:0\"]\nmetrics_listen = \"127.0.0.1:0\"\nworkers = 1\nstandalone_snapshot = \"{1}\"\nstandalone_blob_dir = \"{0}\"\nshutdown_drain_seconds = {2}\n",
            dir.display(),
            snap.display(),
            DRAIN.as_secs()
        ),
    )
    .unwrap();
    let mut child = Command::new(env!("CARGO_BIN_EXE_nexora-engine"))
        .arg("--config")
        .arg(&config)
        .stdout(Stdio::piped())
        .stderr(Stdio::null())
        .spawn()
        .unwrap();
    let mut line = String::new();
    BufReader::new(child.stdout.take().unwrap())
        .read_line(&mut line)
        .unwrap();
    let addr = |key: &str| -> SocketAddr {
        line.split_whitespace()
            .find_map(|kv| kv.strip_prefix(&format!("{key}=")))
            .unwrap_or_else(|| panic!("{key} missing in {line:?}"))
            .parse()
            .unwrap()
    };
    Engine {
        udp: addr("udp"),
        tcp: addr("tcp"),
        metrics: addr("metrics"),
        child,
    }
}

fn get(addr: SocketAddr, path: &str) -> (u16, String) {
    let mut s = TcpStream::connect_timeout(&addr, Duration::from_secs(1)).unwrap();
    s.set_read_timeout(Some(Duration::from_secs(2))).unwrap();
    write!(
        s,
        "GET {path} HTTP/1.1\r\nHost: e\r\nConnection: close\r\n\r\n"
    )
    .unwrap();
    let mut resp = String::new();
    s.read_to_string(&mut resp).unwrap();
    let status = resp[9..12].parse().unwrap();
    let body = resp.split("\r\n\r\n").nth(1).unwrap_or_default().to_owned();
    (status, body)
}

fn udp_answers(addr: SocketAddr, id: u16) -> bool {
    let sock = UdpSocket::bind("127.0.0.1:0").unwrap();
    sock.set_read_timeout(Some(Duration::from_secs(1))).unwrap();
    let mut q = id.to_be_bytes().to_vec();
    q.extend_from_slice(&[1, 0, 0, 1, 0, 0, 0, 0, 0, 0]);
    q.extend_from_slice(b"\x07example\x04test\x00\x00\x01\x00\x01");
    sock.send_to(&q, addr).unwrap();
    let mut buf = [0u8; 512];
    matches!(sock.recv(&mut buf), Ok(n) if n >= 12 && buf[..2] == id.to_be_bytes())
}

#[test]
fn sigterm_reports_not_ready_serves_through_the_drain_then_exits() {
    let dir = tempfile::tempdir().unwrap();
    let mut engine = start(dir.path());
    assert_eq!(get(engine.metrics, "/ready"), (200, "ready\n".into()));
    assert_eq!(get(engine.metrics, "/live").0, 200);
    assert!(udp_answers(engine.udp, 1));

    let term = Instant::now();
    nix::sys::signal::kill(
        nix::unistd::Pid::from_raw(engine.child.id() as i32),
        nix::sys::signal::Signal::SIGTERM,
    )
    .unwrap();
    std::thread::sleep(Duration::from_millis(300));
    assert_eq!(get(engine.metrics, "/ready"), (503, "draining\n".into()));
    assert_eq!(get(engine.metrics, "/live").0, 200);
    // Serving continues through the drain period, on UDP and new TCP connections alike.
    let mut id = 2;
    while term.elapsed() < DRAIN - Duration::from_millis(700) {
        assert!(
            udp_answers(engine.udp, id),
            "UDP query {id} unanswered while draining"
        );
        TcpStream::connect_timeout(&engine.tcp, Duration::from_secs(1))
            .expect("TCP connection refused while draining");
        id += 1;
        std::thread::sleep(Duration::from_millis(100));
    }

    let status = loop {
        if let Some(status) = engine.child.try_wait().unwrap() {
            break status;
        }
        assert!(
            term.elapsed() < DRAIN + Duration::from_secs(3),
            "engine still running after the drain"
        );
        std::thread::sleep(Duration::from_millis(20));
    };
    assert!(status.success(), "{status}");
    assert!(
        term.elapsed() >= DRAIN,
        "exited before the drain period ended"
    );
    assert!(TcpStream::connect_timeout(&engine.tcp, Duration::from_millis(500)).is_err());
}
