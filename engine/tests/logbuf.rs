use nexora_engine::proto::{LogLevel, LogRequest};
use nexora_engine::telemetry::logbuf::{LogBuffer, classify, redact};

#[test]
fn log_buffer_is_bounded_and_rate_capped() {
    let b = LogBuffer::new(10, 5, 8);
    for i in 0..20 {
        b.push(LogLevel::Info, &format!("line {i}"), 1_000);
    }
    assert_eq!(
        b.dropped(),
        12,
        "burst of 8 admitted in the first second, 12 dropped"
    );
    for i in 0..20 {
        b.push(
            LogLevel::Info,
            &format!("later {i}"),
            5_000 + i64::from(i as u8),
        );
    }
    let all = b.read(&LogRequest {
        limit: 1000,
        min_level: LogLevel::Debug as i32,
        ..Default::default()
    });
    assert_eq!(all.lines.len(), 10, "ring holds at most its capacity");
    assert!(all.oldest_seq > 1, "old lines were overwritten");
    assert_eq!(all.last_seq, all.lines.last().unwrap().seq);
    let long = "x".repeat(2000);
    b.push(LogLevel::Warn, &long, 60_000);
    let tail = b.read(&LogRequest {
        after_seq: all.last_seq,
        limit: 5,
        min_level: LogLevel::Debug as i32,
        ..Default::default()
    });
    assert_eq!(
        tail.lines[0].message.len(),
        512,
        "a line is truncated to 512 octets"
    );
}

#[test]
fn log_lines_are_redacted() {
    for (input, secret) in [
        (
            "join with nxj1.ABCDEFGHIJKLMNOP.0123abcd",
            "ABCDEFGHIJKLMNOP",
        ),
        (
            "token nxt_ABCDEFGHIJKLMNOPQRSTUV used",
            "ABCDEFGHIJKLMNOPQRSTUV",
        ),
        (
            "-----BEGIN PRIVATE KEY-----\nMIIEvQ\n-----END PRIVATE KEY-----",
            "MIIEvQ",
        ),
        (
            "tsig secret=c2VjcmV0 password=hunter2hunter2",
            "hunter2hunter2",
        ),
    ] {
        let out = redact(input);
        assert!(!out.contains(secret), "{input:?} -> {out:?}");
        assert!(out.contains("[redacted"), "{out:?}");
    }
    assert_eq!(redact("serving version 7"), "serving version 7");
}

#[test]
fn log_request_returns_filtered_lines_after_cursor() {
    let b = LogBuffer::new(100, 1000, 1000);
    b.push(
        classify("nexora-engine: serving version 3"),
        "nexora-engine: serving version 3",
        1,
    );
    b.push(
        classify("nexora-engine: snapshot rejected: bad cidr"),
        "nexora-engine: snapshot rejected: bad cidr",
        2,
    );
    b.push(LogLevel::Debug, "nexora-engine: debug detail", 3);
    b.push(LogLevel::Error, "nexora-engine: control stream error", 4);
    let warn = b.read(&LogRequest {
        min_level: LogLevel::Warn as i32,
        limit: 10,
        ..Default::default()
    });
    assert_eq!(
        warn.lines.iter().map(|l| l.seq).collect::<Vec<_>>(),
        vec![2, 4],
        "warn and error only"
    );
    let search = b.read(&LogRequest {
        min_level: LogLevel::Debug as i32,
        contains: "SERVING".into(),
        limit: 10,
        ..Default::default()
    });
    assert_eq!(search.lines.len(), 1);
    let after = b.read(&LogRequest {
        after_seq: 2,
        min_level: LogLevel::Debug as i32,
        limit: 10,
        ..Default::default()
    });
    assert_eq!(
        after.lines.iter().map(|l| l.seq).collect::<Vec<_>>(),
        vec![3, 4]
    );
    assert_eq!(after.oldest_seq, 1);
}

#[test]
fn crate_eprintln_is_captured() {
    nexora_engine::eprintln!("nexora-engine: probe {} password=hunter2hunter2", 1);
    let batch = nexora_engine::telemetry::logbuf::GLOBAL.read(&LogRequest {
        request_id: "r1".into(),
        min_level: LogLevel::Debug as i32,
        contains: "probe 1".into(),
        limit: 10,
        ..Default::default()
    });
    assert_eq!(batch.request_id, "r1", "read echoes the request id");
    assert_eq!(batch.lines.len(), 1);
    assert_eq!(batch.lines[0].level, LogLevel::Info as i32);
    assert_eq!(
        batch.lines[0].message,
        "nexora-engine: probe 1 password=[redacted]"
    );
}
