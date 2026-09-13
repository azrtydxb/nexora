//! Listeners on port 0 get one kernel-chosen port shared by every worker and by
//! UDP and TCP, and spawn_workers reports it.

use nexora_engine::bootstrap::Bootstrap;
use nexora_engine::server::tls::CertStore;
use nexora_engine::server::{Shared, spawn_workers};
use std::net::{TcpStream, UdpSocket};
use std::sync::Arc;

#[test]
fn port_zero_resolves_to_one_shared_udp_and_tcp_port() {
    let dir = tempfile::tempdir().unwrap();
    let boot: Bootstrap = toml::from_str(&format!(
        "node_name = \"t\"\nstate_dir = \"{}\"\nlisten_udp = [\"127.0.0.1:0\"]\nlisten_tcp = [\"127.0.0.1:0\"]\nworkers = 2\nstandalone_snapshot = \"x\"\n",
        dir.path().display()
    ))
    .unwrap();
    let workers = spawn_workers(Shared::new(2), &boot, Arc::new(CertStore::new())).unwrap();
    assert_eq!(workers.handles.len(), 2);
    let (udp, tcp) = (workers.udp[0], workers.tcp[0]);
    assert_ne!(udp.port(), 0, "UDP port not resolved");
    assert_eq!(udp, tcp, "UDP and TCP must share the kernel-chosen port");
    // Both workers hold the port, so nothing else can bind it.
    assert!(UdpSocket::bind(udp).is_err());
    TcpStream::connect(tcp).expect("TCP listener accepts");
}

#[test]
fn encrypted_listeners_on_port_zero_are_bound_by_every_worker_and_reported() {
    let dir = tempfile::tempdir().unwrap();
    let boot: Bootstrap = toml::from_str(&format!(
        "node_name = \"t\"\nstate_dir = \"{}\"\nlisten_udp = [\"127.0.0.1:0\"]\nlisten_dot = [\"127.0.0.1:0\"]\nlisten_doh = [\"127.0.0.1:0\"]\nlisten_doq = [\"127.0.0.1:0\"]\nworkers = 2\nstandalone_snapshot = \"x\"\n",
        dir.path().display()
    ))
    .unwrap();
    let workers = spawn_workers(Shared::new(2), &boot, Arc::new(CertStore::new())).unwrap();
    for addrs in [&workers.dot, &workers.doh, &workers.doq] {
        assert_eq!(addrs.len(), 1);
        assert_ne!(addrs[0].port(), 0, "port not resolved");
    }
    TcpStream::connect(workers.dot[0]).expect("DoT listener accepts");
    TcpStream::connect(workers.doh[0]).expect("DoH listener accepts");
    assert!(UdpSocket::bind(workers.doq[0]).is_err(), "DoQ port is held");
}
