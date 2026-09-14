//! The engine log ring buffer: every stderr line, redacted, rate capped and bounded, read back by the
//! management plane over the control stream (`LogRequest` -> `LogBatch`). Never on the query path.

use crate::proto::{LogBatch, LogLevel, LogLine, LogRequest};
use parking_lot::Mutex;
use std::borrow::Cow;
use std::collections::VecDeque;
use std::io::Write;
use std::sync::LazyLock;
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::{SystemTime, UNIX_EPOCH};

/// Lines the process-wide ring holds; older lines are overwritten.
pub const CAPACITY: usize = 2000;
/// A line is truncated to this many octets (at a char boundary).
pub const LINE_MAX: usize = 512;
/// Sustained lines per second admitted into the ring.
pub const RATE_PER_SEC: u32 = 100;
/// Lines admitted in a burst above the sustained rate.
pub const BURST: u32 = 200;
/// Lines returned when a request names no limit.
const DEFAULT_LIMIT: usize = 1000;

/// The process-wide buffer every `eprintln!`/`log_*!` line reaches.
pub static GLOBAL: LazyLock<LogBuffer> =
    LazyLock::new(|| LogBuffer::new(CAPACITY, RATE_PER_SEC, BURST));

struct Ring {
    lines: VecDeque<LogLine>,
    next_seq: u64,
    /// Token bucket in thousandths of a line.
    tokens_milli: u64,
    refilled_ms: Option<i64>,
}

/// A bounded, rate-capped ring of log lines with monotonically increasing sequence numbers.
pub struct LogBuffer {
    ring: Mutex<Ring>,
    capacity: usize,
    rate_per_sec: u64,
    burst_milli: u64,
    dropped: AtomicU64,
}

impl LogBuffer {
    pub fn new(capacity: usize, rate_per_sec: u32, burst: u32) -> LogBuffer {
        let burst_milli = u64::from(burst) * 1000;
        LogBuffer {
            ring: Mutex::new(Ring {
                lines: VecDeque::with_capacity(capacity),
                next_seq: 1,
                tokens_milli: burst_milli,
                refilled_ms: None,
            }),
            capacity: capacity.max(1),
            rate_per_sec: u64::from(rate_per_sec),
            burst_milli,
            dropped: AtomicU64::new(0),
        }
    }

    /// Appends a line at `now_ms`, or counts it as dropped when the rate cap has no token left.
    pub fn push(&self, level: LogLevel, message: &str, now_ms: i64) {
        let mut ring = self.ring.lock();
        let elapsed = ring.refilled_ms.map_or(0, |last| {
            u64::try_from(now_ms.saturating_sub(last)).unwrap_or(0)
        });
        if ring.refilled_ms.is_none_or(|last| now_ms > last) {
            ring.refilled_ms = Some(now_ms);
        }
        ring.tokens_milli = ring
            .tokens_milli
            .saturating_add(elapsed.saturating_mul(self.rate_per_sec))
            .min(self.burst_milli);
        if ring.tokens_milli < 1000 {
            drop(ring);
            self.dropped.fetch_add(1, Ordering::Relaxed);
            return;
        }
        ring.tokens_milli -= 1000;
        let mut end = message.len().min(LINE_MAX);
        while !message.is_char_boundary(end) {
            end -= 1;
        }
        if ring.lines.len() == self.capacity {
            ring.lines.pop_front();
        }
        let seq = ring.next_seq;
        ring.next_seq += 1;
        ring.lines.push_back(LogLine {
            seq,
            unix_ms: now_ms,
            level: level as i32,
            message: message[..end].to_owned(),
        });
    }

    /// Lines with `seq > after_seq` and `level <= min_level` (ERROR = 1 is most severe; an
    /// unspecified level means every level) containing `contains` (ASCII case-insensitive), oldest
    /// first, at most `limit` (0 -> 1000). Echoes `request_id`.
    pub fn read(&self, req: &LogRequest) -> LogBatch {
        let min_level = if req.min_level == LogLevel::Unspecified as i32 {
            LogLevel::Debug as i32
        } else {
            req.min_level
        };
        let limit = match req.limit {
            0 => DEFAULT_LIMIT,
            n => n as usize,
        };
        let ring = self.ring.lock();
        let lines = ring
            .lines
            .iter()
            .filter(|l| {
                l.seq > req.after_seq
                    && l.level <= min_level
                    && contains_ignore_ascii_case(&l.message, &req.contains)
            })
            .take(limit)
            .cloned()
            .collect();
        LogBatch {
            request_id: req.request_id.clone(),
            lines,
            last_seq: ring.next_seq - 1,
            oldest_seq: ring.lines.front().map_or(0, |l| l.seq),
        }
    }

    /// Lines refused by the rate cap since start.
    pub fn dropped(&self) -> u64 {
        self.dropped.load(Ordering::Relaxed)
    }
}

fn contains_ignore_ascii_case(haystack: &str, needle: &str) -> bool {
    let (h, n) = (haystack.as_bytes(), needle.as_bytes());
    n.is_empty() || h.windows(n.len()).any(|w| w.eq_ignore_ascii_case(n))
}

/// `warn` when the line mentions `error`, `failed`, `rejected`, `cannot` or `invalid` (ASCII
/// case-insensitive), otherwise `info`.
pub fn classify(message: &str) -> LogLevel {
    const WARN_WORDS: [&str; 5] = ["error", "failed", "rejected", "cannot", "invalid"];
    if WARN_WORDS
        .iter()
        .any(|w| contains_ignore_ascii_case(message, w))
    {
        LogLevel::Warn
    } else {
        LogLevel::Info
    }
}

const PEM_BEGIN: &[u8] = b"-----BEGIN ";
const PEM_END: &[u8] = b"-----END ";
const DASHES: &[u8] = b"-----";

/// Replaces join-token secrets (`nxj1.<secret>`), API tokens (`nxt_<secret>`), PEM blocks and
/// `secret=`/`password=`/`key=` values with redaction markers. A PEM block without its END line is
/// redacted to the end of the message.
pub fn redact(message: &str) -> Cow<'_, str> {
    let b = message.as_bytes();
    let mut out = String::new();
    let (mut copied, mut i) = (0, 0);
    while i < b.len() {
        let rest = &b[i..];
        // (text kept before the marker, marker, octets replaced by the marker)
        let hit = if rest.starts_with(b"nxj1.") {
            Some(("nxj1.", "[redacted]", alnum_run(&rest[5..])))
        } else if rest.starts_with(b"nxt_") {
            Some(("nxt_", "[redacted]", alnum_run(&rest[4..])))
        } else if rest.starts_with(PEM_BEGIN) {
            Some(("", "[redacted PEM]", pem_block_len(rest)))
        } else {
            ["secret=", "password=", "key="]
                .into_iter()
                .find(|f| rest.starts_with(f.as_bytes()))
                .map(|f| {
                    let value = &message[i + f.len()..];
                    let run = value.find(char::is_whitespace).unwrap_or(value.len());
                    (f, "[redacted]", run)
                })
        };
        match hit {
            Some((kept, marker, len)) if len > 0 => {
                out.push_str(&message[copied..i + kept.len()]);
                out.push_str(marker);
                i += kept.len() + len;
                copied = i;
            }
            _ => i += 1,
        }
    }
    if copied == 0 {
        Cow::Borrowed(message)
    } else {
        out.push_str(&message[copied..]);
        Cow::Owned(out)
    }
}

fn alnum_run(b: &[u8]) -> usize {
    b.iter().take_while(|c| c.is_ascii_alphanumeric()).count()
}

/// The length of the PEM block at the start of `b` (which starts with `-----BEGIN `), through the
/// closing `-----END <label>-----`, or the rest of the message when it is not closed.
fn pem_block_len(b: &[u8]) -> usize {
    let body = PEM_BEGIN.len();
    let Some(label) = b[body..].iter().position(|&c| c == b'-') else {
        return b.len();
    };
    let mut j = body + label;
    while j < b.len() {
        if b[j..].starts_with(PEM_END) {
            let after = j + PEM_END.len();
            let label = b[after..]
                .iter()
                .position(|&c| c == b'-')
                .unwrap_or(b.len() - after);
            if b[after + label..].starts_with(DASHES) {
                return after + label + DASHES.len();
            }
        }
        j += 1;
    }
    b.len()
}

/// Writes the formatted line to stderr, then pushes its redacted form to [`GLOBAL`], classified
/// with [`classify`] when no level is given. Used by the crate's `eprintln!` and `log_*!` macros;
/// it allocates, so it never runs on the query path.
pub fn emit(level: Option<LogLevel>, args: std::fmt::Arguments<'_>) {
    let line = args.to_string();
    {
        let mut err = std::io::stderr().lock();
        let _ = writeln!(err, "{line}");
    }
    let level = level.unwrap_or_else(|| classify(&line));
    let now_ms = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map_or(0, |d| i64::try_from(d.as_millis()).unwrap_or(i64::MAX));
    GLOBAL.push(level, &redact(&line), now_ms);
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Break caught: a module inside the crate calling plain `eprintln!` (like `server/tcp.rs`)
    /// still resolving to std's macro, so its line never reaches the ring.
    #[test]
    fn plain_eprintln_inside_the_crate_reaches_the_ring() {
        eprintln!("nexora-engine: tcp listener: in-crate probe {}", 7);
        let batch = GLOBAL.read(&LogRequest {
            contains: "in-crate probe 7".into(),
            ..Default::default()
        });
        assert_eq!(batch.lines.len(), 1);
        assert_eq!(batch.lines[0].level, LogLevel::Info as i32);
    }
}
