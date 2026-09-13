use bytes::Bytes;
use nexora_engine::cache::CacheKey;
use nexora_engine::inflight::{InFlight, Join, Resolution, wait};
use nexora_engine::wire::NameKey;
use std::sync::Arc;

fn key(n: &[u8]) -> CacheKey {
    CacheKey {
        name: NameKey::from_wire_lowercase(n).unwrap(),
        qtype: 1,
        qclass: 1,
        do_bit: false,
        cd_bit: false,
        partition: 0,
    }
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn thousand_waiters_on_many_threads_all_get_the_answer() {
    let inf = Arc::new(InFlight::new());
    let Join::Leader(leader) = inf.join(key(b"\x04herd\x00")) else {
        panic!("first must lead")
    };
    let mut handles = Vec::new();
    for _ in 0..1000 {
        let inf = inf.clone();
        handles.push(tokio::spawn(async move {
            match inf.join(key(b"\x04herd\x00")) {
                Join::Follower(rx) => wait(rx).await,
                Join::Leader(_) => panic!("second leader"),
            }
        }));
    }
    tokio::time::sleep(std::time::Duration::from_millis(50)).await;
    leader.complete(Resolution::Answer(Bytes::from_static(b"answer")));
    for h in handles {
        match tokio::time::timeout(std::time::Duration::from_secs(2), h)
            .await
            .expect("waiter hung")
            .unwrap()
        {
            Resolution::Answer(b) => assert_eq!(&b[..], b"answer"),
            Resolution::ServFail => panic!("servfail"),
        }
    }
    assert_eq!(inf.len(), 0);
}

#[tokio::test(flavor = "current_thread")]
async fn dropped_leader_wakes_followers_with_servfail_and_frees_key() {
    let inf = InFlight::new();
    let Join::Leader(leader) = inf.join(key(b"\x04drop\x00")) else {
        panic!()
    };
    let Join::Follower(rx) = inf.join(key(b"\x04drop\x00")) else {
        panic!()
    };
    drop(leader);
    assert!(matches!(wait(rx).await, Resolution::ServFail));
    assert!(matches!(inf.join(key(b"\x04drop\x00")), Join::Leader(_)));
}

#[tokio::test(flavor = "current_thread")]
async fn follower_joining_after_value_sent_still_gets_it() {
    let inf = InFlight::new();
    let Join::Leader(leader) = inf.join(key(b"\x04late\x00")) else {
        panic!()
    };
    let Join::Follower(rx) = inf.join(key(b"\x04late\x00")) else {
        panic!()
    };
    leader.complete(Resolution::Answer(Bytes::from_static(b"x")));
    assert!(matches!(wait(rx).await, Resolution::Answer(_)));
}
