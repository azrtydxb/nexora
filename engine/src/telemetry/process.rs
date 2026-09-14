//! Process CPU, resident memory, cgroup memory limit and start time for `Stats` (off the query path).

use std::sync::OnceLock;
use std::time::{SystemTime, UNIX_EPOCH};

static STARTED: OnceLock<i64> = OnceLock::new();

/// Milliseconds since the epoch at the first call (main calls it at start).
pub fn started_unix_ms() -> i64 {
    *STARTED.get_or_init(|| {
        SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .map_or(0, |d| d.as_millis() as i64)
    })
}

// debt: `cpu_seconds` assumes CLK_TCK = 100 and `resident_bytes` a 4096-octet page, true on the
// kw arm64 and x86_64 Linux kernels; revisit for an engine on a kernel with another tick rate or
// page size.

/// utime + stime of this process in seconds, from /proc/self/stat (fields 14 and 15, 100 Hz ticks).
pub fn cpu_seconds() -> f64 {
    let Ok(stat) = std::fs::read_to_string("/proc/self/stat") else {
        return 0.0;
    };
    let Some((_, rest)) = stat.rsplit_once(')') else {
        return 0.0;
    };
    let f: Vec<&str> = rest.split_whitespace().collect();
    let ticks = |i: usize| f.get(i).and_then(|v| v.parse::<u64>().ok()).unwrap_or(0);
    (ticks(11) + ticks(12)) as f64 / 100.0
}

/// Resident set size in bytes from /proc/self/statm (pages x 4096).
pub fn resident_bytes() -> u64 {
    std::fs::read_to_string("/proc/self/statm")
        .ok()
        .and_then(|s| {
            s.split_whitespace()
                .nth(1)
                .and_then(|v| v.parse::<u64>().ok())
        })
        .map_or(0, |pages| pages * 4096)
}

/// The cgroup v2 memory limit, 0 when unlimited or unknown.
pub fn memory_limit_bytes() -> u64 {
    std::fs::read_to_string("/sys/fs/cgroup/memory.max")
        .ok()
        .and_then(|s| s.trim().parse::<u64>().ok())
        .unwrap_or(0)
}
