use super::notify_out::{NotifyJob, NotifyResult, send_notify};
use std::time::Duration;
use tokio::net::UdpSocket;

fn soa_rr() -> Vec<u8> {
    let mut v = b"\x07example\x04test\x00".to_vec();
    v.extend_from_slice(&[0, 6, 0, 1, 0, 0, 0x0e, 0x10]);
    let rdata = b"\x03ns1\x07example\x04test\x00\x01h\x07example\x04test\x00\x00\x00\x00\x07\x00\x00\x1c\x20\x00\x00\x0e\x10\x00\x12\x75\x00\x00\x00\x01\x2c";
    v.extend_from_slice(&(rdata.len() as u16).to_be_bytes());
    v.extend_from_slice(rdata);
    v
}

#[tokio::test]
async fn retries_until_the_secondary_answers() {
    let secondary = UdpSocket::bind("127.0.0.1:0").await.unwrap();
    let target = secondary.local_addr().unwrap();
    let server = tokio::spawn(async move {
        let mut buf = [0u8; 1500];
        let (_, _) = secondary.recv_from(&mut buf).await.unwrap(); // drop the first attempt
        let (n, from) = secondary.recv_from(&mut buf).await.unwrap();
        assert_eq!((buf[2] >> 3) & 0x0f, 4, "opcode NOTIFY");
        assert_ne!(buf[2] & 0x04, 0, "AA set");
        let mut resp = buf[..n].to_vec();
        resp[2] |= 0x80; // QR
        resp[6..8].copy_from_slice(&[0, 0]); // no answer section
        let qend = 12 + 14 + 4;
        resp.truncate(qend);
        secondary.send_to(&resp, from).await.unwrap();
    });
    let job = NotifyJob {
        zone: b"\x07example\x04test\x00".to_vec().into(),
        soa_rr: soa_rr(),
        target,
        key: None,
    };
    match send_notify(job, Duration::from_millis(50), 5).await {
        NotifyResult::Acked { attempts } => assert_eq!(attempts, 2),
        _ => panic!("expected ack on the second attempt"),
    }
    server.await.unwrap();
}

#[tokio::test]
async fn gives_up_after_the_attempt_budget() {
    let silent = UdpSocket::bind("127.0.0.1:0").await.unwrap();
    let job = NotifyJob {
        zone: b"\x07example\x04test\x00".to_vec().into(),
        soa_rr: soa_rr(),
        target: silent.local_addr().unwrap(),
        key: None,
    };
    assert!(matches!(
        send_notify(job, Duration::from_millis(20), 3).await,
        NotifyResult::Timeout
    ));
}
