use nexora_engine::proto::{ConfigSnapshot, Upstream, UpstreamProtocol};
use prost::Message;

#[test]
fn config_snapshot_round_trips() {
    let snap = ConfigSnapshot {
        version: 3,
        upstreams: vec![Upstream {
            id: "u1".into(),
            name: "fixture".into(),
            protocol: UpstreamProtocol::Udp as i32,
            address: "127.0.0.1:5300".into(),
            timeout_ms: 250,
            ..Default::default()
        }],
        acl_allow_cidrs: vec!["127.0.0.0/8".into()],
        ..Default::default()
    };
    let bytes = snap.encode_to_vec();
    let back = ConfigSnapshot::decode(bytes.as_slice()).unwrap();
    assert_eq!(snap, back);
}
