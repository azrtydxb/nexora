use super::update::{UpdateForwarder, handle_update_with_set};
use super::{answer_tests::basic_set, set::AuthSet};
use crate::proto;
use crate::tsig::{KeyRing, TsigVerifier, find_tsig, sign_request};
use crate::tsig_tests::test_ring;
use hickory_proto::op::{Message, MessageType, OpCode, Query, ResponseCode};
use hickory_proto::rr::{Name, RecordType};
use std::future::Future;
use std::pin::Pin;
use std::sync::{Arc, Mutex};

struct Fwd {
    seen: Mutex<Vec<proto::UpdateRequest>>,
    reply: Option<u32>,
}
impl UpdateForwarder for Fwd {
    fn forward(
        &self,
        req: proto::UpdateRequest,
    ) -> Pin<Box<dyn Future<Output = Option<proto::UpdateResult>> + Send + '_>> {
        let id = req.request_id.clone();
        self.seen.lock().unwrap().push(req);
        let reply = self.reply;
        Box::pin(async move {
            reply.map(|rcode| proto::UpdateResult {
                request_id: id,
                rcode,
                detail: String::new(),
            })
        })
    }
}

const NOW: u64 = 1757750400;

fn update_msg() -> Vec<u8> {
    let mut m = Message::new(0x2222, MessageType::Query, OpCode::Update);
    m.add_query(Query::query(
        Name::from_ascii("example.test.").unwrap(),
        RecordType::SOA,
    ));
    m.to_vec().unwrap()
}

fn set_allowing(key: &str) -> AuthSet {
    basic_set().with_update_keys("example.test.", vec![key.to_string()])
}

#[tokio::test]
async fn unsigned_update_is_refused_without_forwarding() {
    let fwd = Arc::new(Fwd {
        seen: Mutex::new(vec![]),
        reply: Some(0),
    });
    let resp = handle_update_with_set(
        update_msg(),
        "127.0.0.1:5353".parse().unwrap(),
        Arc::new(set_allowing("xfr-key.")),
        Arc::new(test_ring()),
        fwd.clone(),
        NOW,
    )
    .await;
    assert_eq!(
        Message::from_vec(&resp).unwrap().metadata.response_code,
        ResponseCode::Refused
    );
    assert!(fwd.seen.lock().unwrap().is_empty());
}

#[tokio::test]
async fn signed_update_is_forwarded_and_response_is_signed() {
    let ring = test_ring();
    let key = ring.get(b"\x07xfr-key\x00").unwrap();
    let mut msg = update_msg();
    let mac = sign_request(&mut msg, &key, NOW);
    let fwd = Arc::new(Fwd {
        seen: Mutex::new(vec![]),
        reply: Some(0),
    });
    let resp = handle_update_with_set(
        msg,
        "127.0.0.1:5353".parse().unwrap(),
        Arc::new(set_allowing("xfr-key.")),
        Arc::new(ring),
        fwd.clone(),
        NOW,
    )
    .await;
    let parsed = Message::from_vec(&resp).unwrap();
    assert_eq!(parsed.metadata.response_code, ResponseCode::NoError);
    assert_eq!(parsed.metadata.op_code, OpCode::Update);
    assert!(find_tsig(&resp).unwrap().is_some(), "response carries TSIG");
    TsigVerifier::new((*key).clone(), mac.clone())
        .verify(&resp, NOW)
        .expect("response TSIG verifies");
    let seen = fwd.seen.lock().unwrap();
    assert_eq!(seen.len(), 1);
    assert_eq!(seen[0].zone, "example.test.");
    assert_eq!(seen[0].tsig_key, "xfr-key.");
}

#[tokio::test]
async fn update_source_cidrs_refuse_outside_senders() {
    let ring = test_ring();
    let key = ring.get(b"\x07xfr-key\x00").unwrap();
    let ring = Arc::new(ring);
    let with_update_allow = |cidr: &str| {
        let set = set_allowing("xfr-key.");
        let mut zone = (**set.get(b"\x07example\x04test\x00").unwrap()).clone();
        zone.update_allow = Some(crate::acl::Acl::parse(&[cidr.to_string()]).unwrap());
        Arc::new(AuthSet::from_zones(vec![Arc::new(zone)]).unwrap())
    };
    let send = |set: Arc<AuthSet>, fwd: Arc<Fwd>| {
        let mut msg = update_msg();
        sign_request(&mut msg, &key, NOW);
        handle_update_with_set(
            msg,
            "127.0.0.1:5353".parse().unwrap(),
            set,
            ring.clone(),
            fwd,
            NOW,
        )
    };
    let fwd = Arc::new(Fwd {
        seen: Mutex::new(vec![]),
        reply: Some(0),
    });
    let resp = send(with_update_allow("127.0.0.0/8"), fwd.clone()).await;
    assert_eq!(
        Message::from_vec(&resp).unwrap().metadata.response_code,
        ResponseCode::NoError,
        "a sender inside update_allow is forwarded"
    );
    assert_eq!(fwd.seen.lock().unwrap().len(), 1);

    let fwd = Arc::new(Fwd {
        seen: Mutex::new(vec![]),
        reply: Some(0),
    });
    let resp = send(with_update_allow("192.0.2.0/24"), fwd.clone()).await;
    assert_eq!(
        Message::from_vec(&resp).unwrap().metadata.response_code,
        ResponseCode::Refused,
        "a sender outside update_allow is refused"
    );
    assert!(fwd.seen.lock().unwrap().is_empty());
}

#[tokio::test]
async fn key_not_allowed_is_refused_and_timeout_is_servfail() {
    let ring = test_ring();
    let key = ring.get(b"\x0asha512-key\x00").unwrap();
    let mut msg = update_msg();
    sign_request(&mut msg, &key, NOW);
    let fwd = Arc::new(Fwd {
        seen: Mutex::new(vec![]),
        reply: Some(0),
    });
    let resp = handle_update_with_set(
        msg,
        "127.0.0.1:1".parse().unwrap(),
        Arc::new(set_allowing("xfr-key.")),
        Arc::new(test_ring()),
        fwd.clone(),
        NOW,
    )
    .await;
    assert_eq!(
        Message::from_vec(&resp).unwrap().metadata.response_code,
        ResponseCode::Refused
    );

    let key = ring.get(b"\x07xfr-key\x00").unwrap();
    let mut msg = update_msg();
    sign_request(&mut msg, &key, NOW);
    let silent = Arc::new(Fwd {
        seen: Mutex::new(vec![]),
        reply: None,
    });
    let resp = handle_update_with_set(
        msg.clone(),
        "127.0.0.1:1".parse().unwrap(),
        Arc::new(set_allowing("xfr-key.")),
        Arc::new(test_ring()),
        silent.clone(),
        NOW,
    )
    .await;
    assert_eq!(
        Message::from_vec(&resp).unwrap().metadata.response_code,
        ResponseCode::ServFail,
        "no UpdateResult within 5 s"
    );

    let resp = handle_update_with_set(
        msg,
        "127.0.0.1:1".parse().unwrap(),
        Arc::new(set_allowing("xfr-key.")),
        Arc::new(KeyRing::default()),
        silent,
        NOW,
    )
    .await;
    assert_eq!(
        Message::from_vec(&resp).unwrap().metadata.response_code,
        ResponseCode::NotAuth,
        "unknown key is BADKEY/NOTAUTH"
    );
}

#[tokio::test]
async fn auth_state_forwards_on_the_attached_stream_and_completes_by_request_id() {
    use crate::authoritative::state::AuthState;
    use crate::proto::engine_message::Msg;

    let state = AuthState::new();
    let req = |id: &str| proto::UpdateRequest {
        request_id: id.into(),
        zone: "example.test.".into(),
        ..Default::default()
    };
    assert!(
        state.forward(req("detached")).await.is_none(),
        "no stream: fails at once"
    );

    let (tx, mut rx) = tokio::sync::mpsc::channel(4);
    state.attach(tx);
    let waiter = tokio::spawn({
        let state = state.clone();
        async move { state.forward(req("r1")).await }
    });
    let Some(Msg::UpdateRequest(sent)) = rx.recv().await.unwrap().msg else {
        panic!("UpdateRequest not sent")
    };
    assert_eq!(sent.request_id, "r1");
    state.complete_update(proto::UpdateResult {
        request_id: "other".into(),
        rcode: 5,
        detail: String::new(),
    });
    state.complete_update(proto::UpdateResult {
        request_id: "r1".into(),
        rcode: 0,
        detail: String::new(),
    });
    assert_eq!(waiter.await.unwrap().unwrap().rcode, 0);

    let waiter = tokio::spawn({
        let state = state.clone();
        async move { state.forward(req("r2")).await }
    });
    rx.recv().await.unwrap();
    state.detach();
    assert!(
        waiter.await.unwrap().is_none(),
        "detaching fails waiting updates"
    );
}
