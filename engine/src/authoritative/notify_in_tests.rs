use super::name::from_ascii;
use super::notify_in::{NotifySink, handle_notify};
use super::nzf;
use super::set::AuthSet;
use super::zone::Zone;
use super::zone_tests::FULL;
use crate::proto;
use crate::tsig::{KeyRing, sign_request};
use crate::tsig_tests::test_ring;
use hickory_proto::op::{Message, MessageType, OpCode, Query, ResponseCode};
use hickory_proto::rr::{Name, RecordType};
use std::sync::{Arc, Mutex};

#[derive(Default)]
struct Captured(Mutex<Vec<proto::NotifyReceived>>);
impl NotifySink for Captured {
    fn notify(&self, ev: proto::NotifyReceived) -> bool {
        self.0.lock().unwrap().push(ev);
        true
    }
}

fn secondary_set(primary: &str) -> AuthSet {
    secondary_set_keyed(primary, None)
}

fn secondary_set_keyed(primary: &str, key: Option<&str>) -> AuthSet {
    let mut z = Zone::from_image(&nzf::parse(FULL).unwrap()).unwrap();
    let key = key.map(|k| from_ascii(k).unwrap().into_boxed_slice());
    z.set_secondary_primaries(vec![(primary.parse().unwrap(), key)]);
    AuthSet::from_zones(vec![Arc::new(z)]).unwrap()
}

fn notify_msg(zone: &str) -> Vec<u8> {
    let mut m = Message::new(0x7777, MessageType::Query, OpCode::Notify);
    m.metadata.authoritative = true;
    m.add_query(Query::query(
        Name::from_ascii(zone).unwrap(),
        RecordType::SOA,
    ));
    m.to_vec().unwrap()
}

#[test]
fn notify_from_primary_is_acked_and_forwarded() {
    let sink = Captured::default();
    let set = secondary_set("192.0.2.53:53");
    let resp = handle_notify(
        &notify_msg("example.test."),
        "192.0.2.53:40000".parse().unwrap(),
        &set,
        &KeyRing::default(),
        &sink,
        0,
    );
    let r = Message::from_vec(&resp).unwrap();
    assert_eq!(r.metadata.op_code, OpCode::Notify);
    assert_eq!(r.metadata.message_type, MessageType::Response);
    assert_eq!(r.metadata.response_code, ResponseCode::NoError);
    assert_eq!(r.metadata.id, 0x7777);
    let evs = sink.0.lock().unwrap();
    assert_eq!(evs.len(), 1);
    assert_eq!(evs[0].zone, "example.test.");
    assert_eq!(evs[0].source, "192.0.2.53:40000");
}

#[test]
fn notify_from_other_source_or_for_unknown_zone_is_refused() {
    let sink = Captured::default();
    let set = secondary_set("192.0.2.53:53");
    let ok = handle_notify(
        &notify_msg("example.test."),
        "192.0.2.53:1".parse().unwrap(),
        &set,
        &KeyRing::default(),
        &sink,
        0,
    );
    assert_eq!(
        Message::from_vec(&ok).unwrap().metadata.response_code,
        ResponseCode::NoError
    );
    let other = handle_notify(
        &notify_msg("example.test."),
        "198.51.100.1:53".parse().unwrap(),
        &set,
        &KeyRing::default(),
        &sink,
        0,
    );
    assert_eq!(
        Message::from_vec(&other).unwrap().metadata.response_code,
        ResponseCode::Refused
    );
    let unknown = handle_notify(
        &notify_msg("example.org."),
        "192.0.2.53:53".parse().unwrap(),
        &set,
        &KeyRing::default(),
        &sink,
        0,
    );
    assert_eq!(
        Message::from_vec(&unknown).unwrap().metadata.response_code,
        ResponseCode::NotAuth
    );
    assert_eq!(
        sink.0.lock().unwrap().len(),
        1,
        "only the accepted NOTIFY is forwarded"
    );
}

fn signed_notify(zone: &str, ring: &KeyRing, key: &[u8]) -> Vec<u8> {
    let mut msg = notify_msg(zone);
    sign_request(&mut msg, &ring.get(key).unwrap(), NOW);
    msg
}

const NOW: u64 = 1757750400;

#[test]
fn notify_from_keyed_primary_requires_that_key() {
    let ring = test_ring();
    let set = secondary_set_keyed("192.0.2.53:53", Some("xfr-key."));
    let from = "192.0.2.53:40000".parse().unwrap();
    let rcode = |msg: &[u8], sink: &Captured| {
        let resp = handle_notify(msg, from, &set, &ring, sink, NOW);
        Message::from_vec(&resp).unwrap().metadata.response_code
    };
    let sink = Captured::default();
    // positive first: the primary's own key is accepted and forwarded
    assert_eq!(
        rcode(
            &signed_notify("example.test.", &ring, b"\x07xfr-key\x00"),
            &sink
        ),
        ResponseCode::NoError
    );
    assert_eq!(sink.0.lock().unwrap().len(), 1);
    // another key that verifies, and no key at all, are refused from the same address
    let other = rcode(
        &signed_notify("example.test.", &ring, b"\x0asha512-key\x00"),
        &sink,
    );
    assert_eq!(other, ResponseCode::Refused);
    assert_eq!(
        rcode(&notify_msg("example.test."), &sink),
        ResponseCode::Refused
    );
    assert_eq!(
        sink.0.lock().unwrap().len(),
        1,
        "refused NOTIFYs are not forwarded"
    );
}
