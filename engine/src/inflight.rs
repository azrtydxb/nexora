//! Cross-worker request coalescing: one upstream resolution per cache key.

use crate::cache::CacheKey;
use bytes::Bytes;
use parking_lot::Mutex;
use rustc_hash::{FxHashMap, FxHasher};
use std::hash::{Hash, Hasher};
use std::sync::Arc;
use tokio::sync::watch;

const SHARDS: usize = 64;

#[derive(Clone, Debug)]
pub enum Resolution {
    Answer(Bytes),
    ServFail,
}

pub struct Pending {
    tx: watch::Sender<Option<Resolution>>,
}

type Shard = Mutex<FxHashMap<CacheKey, Arc<Pending>>>;

struct Inner {
    shards: Box<[Shard; SHARDS]>,
}

impl Inner {
    fn shard(&self, key: &CacheKey) -> &Shard {
        let mut h = FxHasher::default();
        key.hash(&mut h);
        &self.shards[h.finish() as usize & (SHARDS - 1)]
    }

    /// Removes `key` only while it still maps to `pending`.
    fn remove(&self, key: &CacheKey, pending: &Arc<Pending>) {
        let mut shard = self.shard(key).lock();
        if shard.get(key).is_some_and(|p| Arc::ptr_eq(p, pending)) {
            shard.remove(key);
        }
    }
}

pub struct InFlight {
    inner: Arc<Inner>,
}

#[allow(clippy::large_enum_variant)] // the guard carries its key; moved once per miss
pub enum Join {
    Leader(LeaderGuard),
    Follower(watch::Receiver<Option<Resolution>>),
}

impl Default for InFlight {
    fn default() -> Self {
        InFlight::new()
    }
}

impl InFlight {
    pub fn new() -> InFlight {
        InFlight {
            inner: Arc::new(Inner {
                shards: Box::new(std::array::from_fn(|_| Mutex::default())),
            }),
        }
    }

    /// The first caller for a key leads the resolution; later callers follow it.
    pub fn join(&self, key: CacheKey) -> Join {
        let mut shard = self.inner.shard(&key).lock();
        if let Some(pending) = shard.get(&key) {
            return Join::Follower(pending.tx.subscribe());
        }
        let pending = Arc::new(Pending {
            tx: watch::channel(None).0,
        });
        shard.insert(key, pending.clone());
        Join::Leader(LeaderGuard {
            inflight: self.inner.clone(),
            key,
            pending,
            done: false,
        })
    }

    pub fn len(&self) -> usize {
        self.inner.shards.iter().map(|s| s.lock().len()).sum()
    }

    pub fn is_empty(&self) -> bool {
        self.len() == 0
    }
}

pub struct LeaderGuard {
    inflight: Arc<Inner>,
    key: CacheKey,
    pending: Arc<Pending>,
    done: bool,
}

impl LeaderGuard {
    /// Frees the key, then wakes every follower with `r`.
    pub fn complete(mut self, r: Resolution) {
        self.finish(r);
    }

    fn finish(&mut self, r: Resolution) {
        self.done = true;
        self.inflight.remove(&self.key, &self.pending);
        self.pending.tx.send_replace(Some(r));
    }
}

impl Drop for LeaderGuard {
    fn drop(&mut self) {
        if !self.done {
            self.finish(Resolution::ServFail);
        }
    }
}

/// Waits for the leader's resolution; `ServFail` if the leader vanished without one.
pub async fn wait(mut rx: watch::Receiver<Option<Resolution>>) -> Resolution {
    loop {
        if let Some(r) = rx.borrow_and_update().clone() {
            return r;
        }
        if rx.changed().await.is_err() {
            return rx.borrow().clone().unwrap_or(Resolution::ServFail);
        }
    }
}
