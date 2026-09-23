//! `.local` names through the full query pipeline: forward zones first, then the gateway.
use std::sync::Arc;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::time::Duration;

use hickory_proto::op::ResponseCode;
use hickory_proto::rr::{RData, RecordType};
use nexora_engine::mdns::gateway::{Gateway, Target};

mod common;

#[test]
fn local_names_route_to_the_gateway_after_forward_zones() {
    let upstream_hits = Arc::new(AtomicUsize::new(0));
    let upstream = common::fake_upstream_a(upstream_hits.clone(), "192.0.2.53");
    let responder_hits = Arc::new(AtomicUsize::new(0));
    let responder = common::unicast_mdns_responder(
        responder_hits.clone(),
        &[("printer.local.", [10, 254, 0, 9])],
    );
    let gw = Arc::new(Gateway::new(
        vec![Target {
            bind: "127.0.0.1:0".parse().unwrap(),
            group: responder,
            if_index: 0,
        }],
        Duration::from_millis(300),
    ));
    // Engine with global upstream `upstream`, a forward zone kw.local. -> `upstream`, and the gateway injected.
    let (srv, shared) = common::start_engine_with(upstream, "127.0.0.0/8", |snap| {
        snap.forward_zones
            .push(common::forward_zone("kw.local.", upstream));
    });
    common::inject_gateway(&shared, Some(gw.clone()));

    let r = common::ask(srv, "printer.local.", RecordType::A);
    assert_eq!(r.metadata.response_code, ResponseCode::NoError);
    assert!(
        matches!(&r.answers[0].data, RData::A(a) if a.0 == std::net::Ipv4Addr::new(10, 254, 0, 9))
    );
    assert!(r.answers[0].ttl <= 10);
    assert!(!r.metadata.authoritative);
    assert_eq!(
        upstream_hits.load(Ordering::SeqCst),
        0,
        "a .local name never reaches the upstream"
    );

    let r = common::ask(srv, "host.kw.local.", RecordType::A);
    assert!(
        matches!(&r.answers[0].data, RData::A(a) if a.0 == std::net::Ipv4Addr::new(192, 0, 2, 53)),
        "forward zone wins"
    );
    assert_eq!(
        responder_hits.load(Ordering::SeqCst),
        1,
        "the forward zone name did not reach the gateway"
    );

    for round in 0..2 {
        let r = common::ask(srv, "nothere.local.", RecordType::A);
        assert_eq!(
            r.metadata.response_code,
            ResponseCode::NXDomain,
            "round {round}"
        );
    }
    assert_eq!(
        responder_hits.load(Ordering::SeqCst),
        3,
        "NXDOMAIN without SOA is not cached"
    );

    common::inject_gateway(&shared, None);
    let r = common::ask(srv, "other.local.", RecordType::A);
    assert!(
        matches!(&r.answers[0].data, RData::A(a) if a.0 == std::net::Ipv4Addr::new(192, 0, 2, 53)),
        "without mDNS .local is forwarded"
    );
}
