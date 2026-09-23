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

// Linux exposes process CPU in USER_HZ ticks and RSS in the running system's pages.
// Cache these process-invariant values off the query path. Unknown values produce zero,
// rather than inventing a tick rate or page size.
fn system_units() -> (u64, u64) {
    static UNITS: OnceLock<(u64, u64)> = OnceLock::new();
    *UNITS.get_or_init(|| {
        // SAFETY: sysconf takes constant names and accesses no caller-owned memory.
        unsafe {
            let positive = |n: libc::c_long| u64::try_from(n).ok().filter(|v| *v > 0).unwrap_or(0);
            (
                positive(libc::sysconf(libc::_SC_CLK_TCK)),
                positive(libc::sysconf(libc::_SC_PAGESIZE)),
            )
        }
    })
}

fn cpu_from_stat(stat: &str, ticks_per_second: u64) -> f64 {
    if ticks_per_second == 0 {
        return 0.0;
    }
    let Some((_, rest)) = stat.rsplit_once(')') else {
        return 0.0;
    };
    let mut fields = rest.split_whitespace();
    let Some(user) = fields.nth(11).and_then(|v| v.parse::<u64>().ok()) else {
        return 0.0;
    };
    let Some(system) = fields.next().and_then(|v| v.parse::<u64>().ok()) else {
        return 0.0;
    };
    (user as f64 + system as f64) / ticks_per_second as f64
}

fn resident_from_statm(statm: &str, page_bytes: u64) -> u64 {
    statm
        .split_whitespace()
        .nth(1)
        .and_then(|v| v.parse::<u64>().ok())
        .map_or(0, |pages| pages.saturating_mul(page_bytes))
}

/// utime + stime of this process in seconds, from /proc/self/stat fields 14 and 15.
pub fn cpu_seconds() -> f64 {
    std::fs::read_to_string("/proc/self/stat")
        .map_or(0.0, |stat| cpu_from_stat(&stat, system_units().0))
}

/// Resident set size in bytes using the running Linux system's page size.
pub fn resident_bytes() -> u64 {
    std::fs::read_to_string("/proc/self/statm")
        .map_or(0, |stat| resident_from_statm(&stat, system_units().1))
}

/// The cgroup v2 memory limit, 0 when unlimited or unknown.
pub fn memory_limit_bytes() -> u64 {
    std::fs::read_to_string("/sys/fs/cgroup/memory.max")
        .ok()
        .and_then(|s| s.trim().parse::<u64>().ok())
        .unwrap_or(0)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn process_units_follow_system_values() {
        let (ticks, pages) = system_units();
        assert!(ticks > 0 && pages > 0);
        // SAFETY: constant sysconf queries have no pointer arguments.
        unsafe {
            assert_eq!(ticks, libc::sysconf(libc::_SC_CLK_TCK) as u64);
            assert_eq!(pages, libc::sysconf(libc::_SC_PAGESIZE) as u64);
        }
    }

    #[test]
    fn conversions_support_non_default_linux_units() {
        let stat = "42 (worker ) name) S 0 0 0 0 0 0 0 0 0 0 250 125 0";
        assert_eq!(cpu_from_stat(stat, 100), 3.75);
        assert_eq!(cpu_from_stat(stat, 250), 1.5);
        assert_eq!(cpu_from_stat(stat, 1000), 0.375);
        for page in [4096, 16384, 65536] {
            assert_eq!(resident_from_statm("999 17 0", page), 17 * page);
        }
        assert_eq!(cpu_from_stat(stat, 0), 0.0);
        assert_eq!(cpu_from_stat("malformed", 100), 0.0);
        assert_eq!(cpu_from_stat("1 (x) S", 100), 0.0);
        assert_eq!(resident_from_statm("bad", 65536), 0);
        assert_eq!(resident_from_statm("1 17", 0), 0);
        assert_eq!(
            resident_from_statm("1 18446744073709551615", 65536),
            u64::MAX
        );
    }
}
