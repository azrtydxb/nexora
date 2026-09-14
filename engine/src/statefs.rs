//! Writes into the state directory that stay correct when two engine processes share it: during a
//! rolling update (`maxSurge: 1`) the new pod starts on the node's hostPath state directory while
//! the old pod still serves from it.
//!
//! Every file is replaced through a temporary name unique to the process and the call, so two
//! writers never interleave bytes in one temporary file; multi-file changes (the identity
//! directories) are serialized with an advisory lock on `state_dir/.lock`.

use std::fs::{File, OpenOptions};
use std::io::{self, Write};
use std::os::unix::fs::OpenOptionsExt;
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::Duration;

const LOCK_FILE: &str = ".lock";
const LOCK_POLL: Duration = Duration::from_millis(20);

/// `<path>.<pid>.<n>.tmp`: unique per process and call.
pub fn unique_tmp(path: &Path) -> PathBuf {
    static NEXT: AtomicU64 = AtomicU64::new(0);
    let mut name = path.file_name().unwrap_or_default().to_os_string();
    name.push(format!(
        ".{}.{}.tmp",
        std::process::id(),
        NEXT.fetch_add(1, Ordering::Relaxed)
    ));
    path.with_file_name(name)
}

/// Replaces `path` with `bytes` atomically: a unique temporary file (mode 0600), fsync, rename,
/// fsync of the directory. The temporary file is removed when a step fails.
pub fn write_atomic(path: &Path, bytes: &[u8]) -> io::Result<()> {
    let tmp = unique_tmp(path);
    let written = (|| {
        let mut f = OpenOptions::new()
            .write(true)
            .create_new(true)
            .mode(0o600)
            .open(&tmp)?;
        f.write_all(bytes)?;
        f.sync_all()?;
        drop(f);
        std::fs::rename(&tmp, path)
    })();
    if let Err(e) = written {
        let _ = std::fs::remove_file(&tmp);
        return Err(e);
    }
    match path.parent() {
        Some(dir) if !dir.as_os_str().is_empty() => File::open(dir)?.sync_all(),
        _ => Ok(()),
    }
}

/// An exclusive advisory lock (`flock`) on `state_dir/.lock`, held until dropped. It excludes other
/// processes and other holders in this process alike (each holder opens its own descriptor).
pub struct StateLock(#[allow(dead_code)] File);

impl StateLock {
    fn open(state_dir: &Path) -> io::Result<File> {
        std::fs::create_dir_all(state_dir)?;
        OpenOptions::new()
            .read(true)
            .write(true)
            .create(true)
            .truncate(false)
            .mode(0o600)
            .open(state_dir.join(LOCK_FILE))
    }

    /// Blocks until the lock is held.
    pub fn acquire(state_dir: &Path) -> io::Result<StateLock> {
        let f = Self::open(state_dir)?;
        f.lock()?;
        Ok(StateLock(f))
    }

    /// The lock if no one else holds it.
    pub fn try_acquire(state_dir: &Path) -> io::Result<Option<StateLock>> {
        let f = Self::open(state_dir)?;
        match f.try_lock() {
            Ok(()) => Ok(Some(StateLock(f))),
            Err(std::fs::TryLockError::WouldBlock) => Ok(None),
            Err(std::fs::TryLockError::Error(e)) => Err(e),
        }
    }

    /// Waits for the lock without blocking the async runtime thread.
    pub async fn acquire_async(state_dir: &Path) -> io::Result<StateLock> {
        loop {
            if let Some(lock) = Self::try_acquire(state_dir)? {
                return Ok(lock);
            }
            tokio::time::sleep(LOCK_POLL).await;
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::{Arc, Barrier};

    #[test]
    fn concurrent_atomic_writes_never_leave_a_mixed_or_partial_file() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("file");
        let writers = 8;
        let barrier = Arc::new(Barrier::new(writers));
        let handles: Vec<_> = (0..writers)
            .map(|i| {
                let (path, barrier) = (path.clone(), barrier.clone());
                std::thread::spawn(move || {
                    // Distinct content per writer, large enough to need several write calls.
                    let body = vec![b'a' + i as u8; 1 << 20];
                    barrier.wait();
                    for _ in 0..5 {
                        write_atomic(&path, &body).unwrap();
                        let seen = std::fs::read(&path).unwrap();
                        assert_eq!(seen.len(), body.len(), "partial file");
                        assert!(
                            seen.iter().all(|&b| b == seen[0]),
                            "bytes of two writers mixed"
                        );
                    }
                })
            })
            .collect();
        for h in handles {
            h.join().unwrap();
        }
        let left: Vec<_> = std::fs::read_dir(dir.path())
            .unwrap()
            .map(|e| e.unwrap().file_name())
            .filter(|n| n != "file")
            .collect();
        assert!(left.is_empty(), "temporary files left: {left:?}");
    }

    #[test]
    fn state_lock_is_exclusive_until_dropped() {
        let dir = tempfile::tempdir().unwrap();
        let held = StateLock::acquire(dir.path()).unwrap();
        assert!(StateLock::try_acquire(dir.path()).unwrap().is_none());
        let waiter = {
            let dir = dir.path().to_owned();
            std::thread::spawn(move || StateLock::acquire(&dir).map(|_| ()))
        };
        std::thread::sleep(Duration::from_millis(50));
        assert!(!waiter.is_finished(), "second holder got the lock");
        drop(held);
        waiter.join().unwrap().unwrap();
        assert!(StateLock::try_acquire(dir.path()).unwrap().is_some());
    }
}
