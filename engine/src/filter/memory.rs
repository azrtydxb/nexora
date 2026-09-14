//! The memory guard of filter index builds. A rebuild runs while the previous index keeps serving,
//! so before each large build allocation the engine cgroup's working set plus that allocation plus
//! a safety margin must fit the cgroup memory limit; otherwise the build stops with
//! [`IndexError::MemoryLimit`] (the snapshot is rejected and the previous index stays active)
//! instead of the kernel OOM-killing the engine.

use super::index::IndexError;
use std::path::PathBuf;

/// The engine's own cgroup v2 directory (cgroup namespace of its container).
pub const CGROUP_DIR: &str = "/sys/fs/cgroup";
/// Fixed part of the safety margin: allocator and huge-page rounding of the build buffers, build
/// thread stacks, and what queries allocate meanwhile.
const MARGIN_BYTES: u64 = 24 << 20;

/// Memory state of the engine cgroup.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Usage {
    /// `memory.current` less `inactive_file` of `memory.stat` (page cache the kernel reclaims
    /// before it OOM-kills), as the kubelet counts it.
    pub working_set: u64,
    /// `memory.max`.
    pub limit: u64,
}

type Observer = Box<dyn Fn(&'static str, u64) + Send + Sync>;

pub struct BuildMemory {
    cgroup: Option<PathBuf>,
    observe: Option<Observer>,
}

impl BuildMemory {
    /// No limit checks (tools and tests that build indexes directly).
    pub fn unlimited() -> BuildMemory {
        BuildMemory {
            cgroup: None,
            observe: None,
        }
    }

    /// Guards against the limit of the cgroup v2 directory `dir` (`memory.max`, `memory.current`,
    /// `memory.stat`); no limit when `memory.max` is `max` or unreadable.
    pub fn cgroup(dir: impl Into<PathBuf>) -> BuildMemory {
        BuildMemory {
            cgroup: Some(dir.into()),
            observe: None,
        }
    }

    /// Calls `f(phase, bytes)` at every reservation, before the limit check (measurement).
    pub fn observe(mut self, f: impl Fn(&'static str, u64) + Send + Sync + 'static) -> Self {
        self.observe = Some(Box::new(f));
        self
    }

    /// The cgroup memory limit, when there is one.
    pub fn limit(&self) -> Option<u64> {
        let dir = self.cgroup.as_ref()?;
        std::fs::read_to_string(dir.join("memory.max"))
            .ok()?
            .trim()
            .parse()
            .ok()
    }

    /// The cgroup's working set and limit, when it has a limit.
    pub fn usage(&self) -> Option<Usage> {
        let limit = self.limit()?;
        let dir = self.cgroup.as_ref()?;
        let current: u64 = std::fs::read_to_string(dir.join("memory.current"))
            .ok()?
            .trim()
            .parse()
            .ok()?;
        let inactive_file = std::fs::read_to_string(dir.join("memory.stat"))
            .ok()
            .and_then(|s| {
                s.lines()
                    .find_map(|l| l.strip_prefix("inactive_file "))
                    .and_then(|v| v.trim().parse::<u64>().ok())
            })
            .unwrap_or(0);
        Some(Usage {
            working_set: current.saturating_sub(inactive_file),
            limit,
        })
    }

    /// The safety margin below `limit`: 24 MiB plus 1/32 of the limit.
    pub fn margin(limit: u64) -> u64 {
        MARGIN_BYTES + limit / 32
    }

    /// Before a build step that adds `bytes` of memory: fails when the working set plus `bytes`
    /// would pass the limit less the margin.
    pub fn reserve(&self, phase: &'static str, bytes: u64) -> Result<(), IndexError> {
        if let Some(f) = &self.observe {
            f(phase, bytes);
        }
        let Some(u) = self.usage() else {
            return Ok(());
        };
        let margin = Self::margin(u.limit);
        if u.working_set.saturating_add(bytes) > u.limit.saturating_sub(margin) {
            return Err(IndexError::MemoryLimit {
                phase,
                in_use: u.working_set,
                needed: bytes,
                limit: u.limit,
                margin,
            });
        }
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn cgroup(max: &str, current: u64, inactive_file: u64) -> (tempfile::TempDir, BuildMemory) {
        let dir = tempfile::tempdir().unwrap();
        std::fs::write(dir.path().join("memory.max"), max).unwrap();
        std::fs::write(dir.path().join("memory.current"), format!("{current}\n")).unwrap();
        std::fs::write(
            dir.path().join("memory.stat"),
            format!("anon 1\nfile 9\nactive_file 5\ninactive_file {inactive_file}\n"),
        )
        .unwrap();
        let m = BuildMemory::cgroup(dir.path());
        (dir, m)
    }

    /// Catches: page cache counted as unavailable (false rejections), the margin left out, a
    /// missing or unlimited cgroup blocking builds.
    #[test]
    fn reserve_checks_working_set_plus_bytes_against_limit_less_margin() {
        let limit = 1u64 << 30;
        let margin = BuildMemory::margin(limit);
        assert_eq!(margin, (24 << 20) + (limit >> 5));
        let (_d, m) = cgroup("1073741824\n", 700 << 20, 200 << 20);
        assert_eq!(
            m.usage(),
            Some(Usage {
                working_set: 500 << 20,
                limit
            })
        );
        let room = limit - margin - (500 << 20);
        assert!(m.reserve("records", room).is_ok());
        match m.reserve("records", room + 1) {
            Err(IndexError::MemoryLimit {
                phase: "records",
                in_use,
                needed,
                limit: l,
                margin: g,
            }) => assert_eq!((in_use, needed, l, g), (500 << 20, room + 1, limit, margin)),
            other => panic!("expected MemoryLimit, got {other:?}"),
        }
        let (_d, m) = cgroup("max\n", 700 << 20, 0);
        assert_eq!(m.usage(), None);
        assert!(m.reserve("records", u64::MAX).is_ok());
        assert!(
            BuildMemory::cgroup("/nonexistent/cgroup")
                .reserve("records", u64::MAX)
                .is_ok()
        );
        assert!(BuildMemory::unlimited().reserve("x", u64::MAX).is_ok());
    }
}
