//! Listeners on port 0 get one kernel-chosen port shared by every worker and by
//! UDP and TCP, and spawn_workers reports it.

use nexora_engine::bootstrap::Bootstrap;
use nexora_engine::server::{Shared, spawn_workers};
use std::net::{TcpStream, UdpSocket};

#[test]
fn port_zero_resolves_to_one_shared_udp_and_tcp_port() {
    let dir = tempfile::tempdir().unwrap();
    let boot: Bootstrap = toml::from_str(&format!(
        "node_name = \"t\"\nstate_dir = \"{}\"\nlisten_udp = [\"127.0.0.1:0\"]\nlisten_tcp = [\"127.0.0.1:0\"]\nworkers = 2\nstandalone_snapshot = \"x\"\n",
        dir.path().display()
    ))
    .unwrap();
    let workers = spawn_workers(Shared::new(2), &boot).unwrap();
    assert_eq!(workers.handles.len(), 2);
    let (udp, tcp) = (workers.udp[0], workers.tcp[0]);
    assert_ne!(udp.port(), 0, "UDP port not resolved");
    assert_eq!(udp, tcp, "UDP and TCP must share the kernel-chosen port");
    // Both workers hold the port, so nothing else can bind it.
    assert!(UdpSocket::bind(udp).is_err());
    TcpStream::connect(tcp).expect("TCP listener accepts");
}
