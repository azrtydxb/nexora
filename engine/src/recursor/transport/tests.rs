use super::*;
use hickory_proto::op::{Message, MessageType, OpCode, Query};
use hickory_proto::rr::{DNSClass, Name, RData, Record, RecordType, rdata::A};
use std::net::{Ipv4Addr, SocketAddr};
use std::sync::Arc;
use std::sync::atomic::Ordering;
use std::time::Duration;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::{TcpListener, UdpSocket};

fn answer(id: u16, name: &Name, ip: Ipv4Addr, tc: bool) -> Vec<u8> {
    let mut m = Message::response(id, OpCode::Query);
    m.metadata.authoritative = true;
    m.metadata.truncation = tc;
    m.queries.push(Query::query(name.clone(), RecordType::A));
    if !tc {
        m.answers
            .push(Record::from_rdata(name.clone(), 300, RData::A(A(ip))));
    }
    m.to_vec().unwrap()
}

fn outbound<'a>(server: SocketAddr, name: &'a Name) -> OutboundQuery<'a> {
    OutboundQuery {
        server,
        qname: name,
        qtype: RecordType::A,
        recursion_desired: false,
        edns: true,
        dnssec_ok: true,
        checking_disabled: false,
        use_0x20: true,
        timeout: Duration::from_millis(800),
    }
}

#[tokio::test(flavor = "current_thread")]
async fn spoofed_replies_are_dropped_and_real_reply_accepted() {
    let server = UdpSocket::bind("127.0.0.1:0").await.unwrap();
    let addr = server.local_addr().unwrap();
    tokio::spawn(async move {
        let mut buf = [0u8; 1500];
        let (n, peer) = server.recv_from(&mut buf).await.unwrap();
        let q = Message::from_vec(&buf[..n]).unwrap();
        let id = q.metadata.id;
        let sent = q.queries[0].name().clone();
        // wrong ID
        server
            .send_to(
                &answer(id.wrapping_add(1), &sent, Ipv4Addr::new(6, 6, 6, 6), false),
                peer,
            )
            .await
            .unwrap();
        // right ID, wrong question
        server
            .send_to(
                &answer(
                    id,
                    &Name::from_ascii("evil.example.").unwrap(),
                    Ipv4Addr::new(6, 6, 6, 6),
                    false,
                ),
                peer,
            )
            .await
            .unwrap();
        // right ID, 0x20 casing not preserved
        server
            .send_to(
                &answer(id, &sent.to_lowercase(), Ipv4Addr::new(6, 6, 6, 6), false),
                peer,
            )
            .await
            .unwrap();
        // genuine reply
        server
            .send_to(&answer(id, &sent, Ipv4Addr::new(192, 0, 2, 1), false), peer)
            .await
            .unwrap();
    });
    let metrics = Arc::new(RecursorMetrics::default());
    let t = Transport::new(metrics.clone());
    let name = Name::from_ascii("www.abcdefghijklmnop.example.").unwrap();
    let ex = t.exchange(&outbound(addr, &name)).await.unwrap();
    assert_eq!(
        ex.message.answers[0].data,
        RData::A(A(Ipv4Addr::new(192, 0, 2, 1)))
    );
    assert!(!ex.via_tcp);
    assert_eq!(metrics.mismatched_id.load(Ordering::Relaxed), 1);
    assert_eq!(metrics.mismatched_question.load(Ordering::Relaxed), 1);
    // the all-lowercase spoof differs from the randomised case unless the RNG produced all-lowercase,
    // which for 20 letters happens with probability 2^-20; the test name has 20 letters.
    assert_eq!(metrics.mismatched_case.load(Ordering::Relaxed), 1);
}

#[tokio::test(flavor = "current_thread")]
async fn truncated_udp_reply_retries_over_tcp() {
    let udp = UdpSocket::bind("127.0.0.1:0").await.unwrap();
    let addr = udp.local_addr().unwrap();
    let tcp = TcpListener::bind(addr).await.unwrap();
    tokio::spawn(async move {
        let mut buf = [0u8; 1500];
        let (n, peer) = udp.recv_from(&mut buf).await.unwrap();
        let q = Message::from_vec(&buf[..n]).unwrap();
        udp.send_to(
            &answer(
                q.metadata.id,
                q.queries[0].name(),
                Ipv4Addr::UNSPECIFIED,
                true,
            ),
            peer,
        )
        .await
        .unwrap();
    });
    tokio::spawn(async move {
        let (mut s, _) = tcp.accept().await.unwrap();
        let len = s.read_u16().await.unwrap() as usize;
        let mut buf = vec![0u8; len];
        s.read_exact(&mut buf).await.unwrap();
        let q = Message::from_vec(&buf).unwrap();
        let out = answer(
            q.metadata.id,
            q.queries[0].name(),
            Ipv4Addr::new(192, 0, 2, 2),
            false,
        );
        s.write_u16(out.len() as u16).await.unwrap();
        s.write_all(&out).await.unwrap();
    });
    let metrics = Arc::new(RecursorMetrics::default());
    let name = Name::from_ascii("big.example.").unwrap();
    let ex = Transport::new(metrics.clone())
        .exchange(&outbound(addr, &name))
        .await
        .unwrap();
    assert!(ex.via_tcp);
    assert_eq!(
        ex.message.answers[0].data,
        RData::A(A(Ipv4Addr::new(192, 0, 2, 2)))
    );
    assert_eq!(metrics.tcp_fallbacks.load(Ordering::Relaxed), 1);
}

#[tokio::test(flavor = "current_thread")]
async fn query_carries_random_id_edns_1232_do_and_rd_clear() {
    let server = UdpSocket::bind("127.0.0.1:0").await.unwrap();
    let addr = server.local_addr().unwrap();
    let (tx, rx) = tokio::sync::oneshot::channel::<(Message, SocketAddr)>();
    tokio::spawn(async move {
        let mut buf = [0u8; 1500];
        let (n, peer) = server.recv_from(&mut buf).await.unwrap();
        tx.send((Message::from_vec(&buf[..n]).unwrap(), peer))
            .unwrap();
    });
    let name = Name::from_ascii("example.").unwrap();
    let t = Transport::new(Arc::new(RecursorMetrics::default()));
    let mut q = outbound(addr, &name);
    q.timeout = Duration::from_millis(100);
    let _ = t.exchange(&q).await;
    let (m, peer) = rx.await.unwrap();
    assert_eq!(m.metadata.message_type, MessageType::Query);
    assert!(!m.metadata.recursion_desired);
    let edns = m.edns.expect("OPT present");
    assert_eq!(edns.max_payload(), 1232);
    assert!(edns.flags().dnssec_ok);
    assert_ne!(peer.port(), 0);
    assert_eq!(m.queries[0].query_class(), DNSClass::IN);
}

#[test]
fn reply_matching_rules() {
    let sent = Name::from_ascii("wWw.ExAmple.").unwrap();
    let ok = Message::from_vec(&answer(7, &sent, Ipv4Addr::LOCALHOST, false)).unwrap();
    assert_eq!(reply_matches(7, &sent, RecordType::A, true, &ok), Ok(()));
    assert_eq!(
        reply_matches(8, &sent, RecordType::A, true, &ok),
        Err(Mismatch::Id)
    );
    let lower =
        Message::from_vec(&answer(7, &sent.to_lowercase(), Ipv4Addr::LOCALHOST, false)).unwrap();
    assert_eq!(
        reply_matches(7, &sent, RecordType::A, true, &lower),
        Err(Mismatch::Case)
    );
    assert_eq!(
        reply_matches(7, &sent, RecordType::A, false, &lower),
        Ok(())
    );
    assert_eq!(
        reply_matches(7, &sent, RecordType::AAAA, true, &ok),
        Err(Mismatch::Question)
    );
}
