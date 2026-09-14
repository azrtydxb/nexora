use crossbeam_utils::CachePadded;
use hickory_proto::op::{Message, MessageType, OpCode, Query};
use hickory_proto::rr::{Name, RecordType};
use hickory_proto::serialize::binary::BinEncodable;
use nexora_engine::upstream::{
    self, Protocol, Question, Strategy, UpstreamSet, UpstreamSpec, WorkerUpstreams,
};
use nexora_engine::wire::parse_query;
use std::net::SocketAddr;
use std::sync::Arc;
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::Duration;
use tokio::net::UdpSocket;

fn query(name: &str) -> (Vec<u8>, Question) {
    let mut m = Message::new(1, MessageType::Query, OpCode::Query);
    m.metadata.recursion_desired = true;
    m.add_query(Query::query(Name::from_ascii(name).unwrap(), RecordType::A));
    let b = m.to_bytes().unwrap();
    let v = parse_query(&b).unwrap();
    let q = Question {
        key: v.key,
        qtype: v.qtype,
        qclass: v.qclass,
    };
    (b, q)
}
fn counter() -> Arc<CachePadded<AtomicU64>> {
    Arc::new(CachePadded::new(AtomicU64::new(0)))
}
async fn local<F: std::future::Future>(f: F) -> F::Output {
    tokio::task::LocalSet::new().run_until(f).await
}

/// A UDP fake upstream: answers every query after `delay` with `rcode`, counting queries.
async fn fake(delay: Duration, rcode: u8) -> (SocketAddr, Arc<AtomicU64>) {
    let sock = Arc::new(UdpSocket::bind("127.0.0.1:0").await.unwrap());
    let addr = sock.local_addr().unwrap();
    let count = Arc::new(AtomicU64::new(0));
    let c = count.clone();
    tokio::task::spawn_local(async move {
        let mut buf = [0u8; 1500];
        loop {
            let Ok((n, peer)) = sock.recv_from(&mut buf).await else {
                return;
            };
            c.fetch_add(1, Ordering::Relaxed);
            let mut reply = buf[..n].to_vec();
            reply[2] |= 0x80; // QR
            reply[3] = (reply[3] & 0xf0) | rcode;
            let s = sock.clone();
            tokio::task::spawn_local(async move {
                tokio::time::sleep(delay).await;
                let _ = s.send_to(&reply, peer).await;
            });
        }
    });
    (addr, count)
}

fn spec(id: &str, addr: SocketAddr) -> UpstreamSpec {
    UpstreamSpec {
        id: id.into(),
        name: id.into(),
        protocol: Protocol::Udp,
        addr: Some(addr),
        tls_server_name: String::new(),
        doh_url: String::new(),
        timeout: Duration::from_millis(400),
        ca_pem: String::new(),
    }
}

#[tokio::test(flavor = "current_thread")]
async fn parallel_returns_first_valid_answer_and_updates_every_upstream() {
    local(async {
        let (fast, fast_n) = fake(Duration::from_millis(5), 0).await;
        let (slow, slow_n) = fake(Duration::from_millis(120), 0).await;
        let (fail, fail_n) = fake(Duration::from_millis(1), 2).await; // SERVFAIL, fastest of all
        let set = UpstreamSet::new(
            vec![spec("slow", slow), spec("fail", fail), spec("fast", fast)],
            Strategy::Parallel { max: 0 },
            None,
        );
        let worker = WorkerUpstreams::new(counter());
        let (q, question) = query("race.example.");
        let started = std::time::Instant::now();
        let got = upstream::forward(&set, &worker, &q, &question)
            .await
            .unwrap();
        assert_eq!(set.specs[got.upstream_index].id, "fast");
        assert_eq!(got.raced, 3);
        assert!(
            started.elapsed() < Duration::from_millis(100),
            "did not wait for the slow upstream"
        );
        tokio::time::sleep(Duration::from_millis(300)).await; // losers drain
        for (i, n) in [(0, &slow_n), (1, &fail_n), (2, &fast_n)] {
            assert_eq!(
                n.load(Ordering::Relaxed),
                1,
                "{} received the query",
                set.specs[i].id
            );
            assert!(
                set.health[i].queries.load(Ordering::Relaxed)
                    + set.health[i].failures.load(Ordering::Relaxed)
                    >= 1,
                "{} health updated",
                set.specs[i].id
            );
        }
        assert!(
            set.health[0].ewma_rtt_us.load(Ordering::Relaxed) >= 100_000,
            "the slow loser's RTT was recorded"
        );
        assert_eq!(set.health[2].race_wins.load(Ordering::Relaxed), 1);
        assert_eq!(worker.pending_waiters(), 0, "no waiter leaked");

        // Fast upstream gone: the slow NOERROR wins over the early SERVFAIL.
        let set2 = UpstreamSet::new(
            vec![spec("slow", slow), spec("fail", fail)],
            Strategy::Parallel { max: 0 },
            None,
        );
        let (q2, question2) = query("race2.example.");
        let got2 = upstream::forward(&set2, &worker, &q2, &question2)
            .await
            .unwrap();
        assert_eq!(set2.specs[got2.upstream_index].id, "slow");
        assert_eq!(got2.response[3] & 0x0f, 0);
        tokio::time::sleep(Duration::from_millis(50)).await;
        assert_eq!(worker.pending_waiters(), 0);
    })
    .await;
}

#[tokio::test(flavor = "current_thread")]
async fn parallel_max_limits_to_lowest_rtt() {
    local(async {
        let (a, a_n) = fake(Duration::from_millis(1), 0).await;
        let (b, b_n) = fake(Duration::from_millis(1), 0).await;
        let (c, c_n) = fake(Duration::from_millis(1), 0).await;
        let set = UpstreamSet::new(
            vec![spec("a", a), spec("b", b), spec("c", c)],
            Strategy::Parallel { max: 2 },
            None,
        );
        set.health[0].record_success(Duration::from_millis(40));
        set.health[1].record_success(Duration::from_millis(5));
        set.health[2].record_success(Duration::from_millis(20));
        let worker = WorkerUpstreams::new(counter());
        let (q, question) = query("max.example.");
        let got = upstream::forward(&set, &worker, &q, &question)
            .await
            .unwrap();
        assert_eq!(got.raced, 2);
        tokio::time::sleep(Duration::from_millis(50)).await;
        assert_eq!(
            (
                a_n.load(Ordering::Relaxed),
                b_n.load(Ordering::Relaxed),
                c_n.load(Ordering::Relaxed)
            ),
            (0, 1, 1)
        );
    })
    .await;
}
