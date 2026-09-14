//! A per-worker cache of filter decisions for repeated names.
//!
//! A direct-mapped table of 64-octet slots (one cache line each). A slot holds the decision for one
//! wire name under one filter view of one index build. The slot is chosen by the name's seeded
//! 64-bit hash, and a one-octet tag per slot (a separate array small enough for L1/L2) rejects most
//! misses before the slot itself is read, so a miss does not wait on a cold slot. A hit needs the stored owner tag (index generation and view id), length and name
//! octets to equal the query's, so a hash or slot collision can never return another name's
//! decision (the hash itself is a function of the compared octets and need not be stored). Names
//! longer than [`CACHED_NAME_MAX`] octets are decided without the cache. Lookups and inserts never
//! allocate or lock; the table is owned by one worker (`Cell`, so it is `!Sync`).

use super::index::{FilterDecision, FilterView, ListHit};
use super::names::fold;
use std::cell::Cell;

/// Slots per worker: 32,768 × 65 octets (slot and tag) ≈ 2 MiB. On kw (Cortex-A76: 512 KiB L2,
/// 3 MiB shared L3) with the 5.1M-name corpus and `filter_bench`'s Zipf workload (s = 1.0 over 1M
/// names), 8,192 / 16,384 / 32,768 / 65,536 slots hit 53% / 58% / 64% / 70% of the queries at
/// 139 / 138 / 122 / 119 ns per decision (144 ns without the cache); repeated names take 31-33 ns
/// at every size. Doubling past 2 MiB buys 3 ns per worker core.
pub const DEFAULT_SLOTS: usize = 1 << 15;
/// Longest wire name (root octet included) the cache holds.
pub const CACHED_NAME_MAX: usize = 48;

const WORDS: usize = CACHED_NAME_MAX / 8;
const NONE: u8 = 0;
const BLOCKED: u8 = 1;
const ALLOWED: u8 = 2;

#[derive(Clone, Copy)]
#[repr(C, align(64))]
struct Slot {
    /// [`FilterView::cache_owner`] of the view that decided; 0 marks an empty slot.
    owner: u64,
    set: u32,
    list: u16,
    /// `NONE`, `BLOCKED` or `ALLOWED` in the low two bits, the hit's suffix offset above them
    /// (cached names are at most 48 octets, so the offset fits six bits).
    kind: u8,
    len: u8,
    /// The name, zero-padded, as little-endian words.
    words: [u64; WORDS],
}

const _: () = assert!(size_of::<Slot>() == 64);

impl Slot {
    const EMPTY: Slot = Slot {
        owner: 0,
        set: 0,
        list: 0,
        kind: NONE,
        len: 0,
        words: [0; WORDS],
    };
}

#[cfg(test)]
thread_local! {
    /// Test hook: every cache hash becomes this value (collision tests).
    static FORCED_HASH: Cell<Option<u64>> = const { Cell::new(None) };
}

pub struct DecisionCache {
    slots: Box<[Cell<Slot>]>,
    /// Per slot: 0 when empty, else the tag of the name in it (hash bits 57..64, low bit set).
    tags: Box<[Cell<u8>]>,
    mask: usize,
    keys: [u64; WORDS],
    hits: Cell<u64>,
}

impl Default for DecisionCache {
    fn default() -> DecisionCache {
        DecisionCache::new(DEFAULT_SLOTS)
    }
}

/// The octets of `name` (at most [`CACHED_NAME_MAX`]) as zero-padded little-endian words: whole
/// words in place, the last partial word from the final 8-octet window shifted down.
#[inline(always)]
fn name_words(name: &[u8]) -> [u64; WORDS] {
    let len = name.len();
    let mut words = [0u64; WORDS];
    if len < 8 {
        let mut short = [0u8; 8];
        short[..len].copy_from_slice(name);
        words[0] = u64::from_le_bytes(short);
        return words;
    }
    let chunks = name.chunks_exact(8);
    let rest = chunks.remainder().len();
    for (w, c) in words.iter_mut().zip(chunks) {
        *w = u64::from_le_bytes(c.try_into().expect("8 octets"));
    }
    if rest != 0 {
        let last = u64::from_le_bytes(name[len - 8..].try_into().expect("8 octets"));
        words[len / 8] = last >> (8 * (8 - rest));
    }
    words
}

/// `view.decide` out of line, so the cache's hit path does not carry the index walk's frame.
#[inline(never)]
fn uncached(view: &FilterView, name_wire: &[u8]) -> FilterDecision {
    view.decide(name_wire)
}

impl DecisionCache {
    /// A cache of `slots` (rounded up to a power of two) with random hash keys.
    pub fn new(slots: usize) -> DecisionCache {
        use rand::RngExt;
        let slots = slots.max(1).next_power_of_two();
        let mut keys = [0u64; WORDS];
        for k in &mut keys {
            // Odd keys: a key never zeroes a multiplier on its own.
            *k = rand::rng().random::<u64>() | 1;
        }
        DecisionCache {
            slots: vec![Cell::new(Slot::EMPTY); slots].into_boxed_slice(),
            tags: vec![Cell::new(0); slots].into_boxed_slice(),
            mask: slots - 1,
            keys,
            hits: Cell::new(0),
        }
    }

    /// Decisions answered from the cache so far.
    pub fn hits(&self) -> u64 {
        self.hits.get()
    }

    pub fn memory_bytes(&self) -> usize {
        self.slots.len() * (size_of::<Slot>() + 1)
    }

    /// Three independent multiplies over the six keyed words.
    #[inline(always)]
    fn hash(&self, w: &[u64; WORDS], len: usize) -> u64 {
        #[cfg(test)]
        if let Some(forced) = FORCED_HASH.with(Cell::get) {
            return forced;
        }
        let k = &self.keys;
        fold(w[0] ^ k[0], w[1] ^ k[1])
            ^ fold(w[2] ^ k[2], w[3] ^ k[3])
            ^ fold(w[4] ^ k[4], w[5] ^ k[5] ^ len as u64)
    }

    /// `view.decide(name_wire)`, answered from the cache when this worker decided the same name
    /// under the same view owner before. Allocation- and lock-free.
    #[inline]
    pub fn decide(&self, view: &FilterView, name_wire: &[u8]) -> FilterDecision {
        let owner = view.cache_owner();
        let len = name_wire.len();
        if owner == 0 || len > CACHED_NAME_MAX {
            return uncached(view, name_wire);
        }
        let words = name_words(name_wire);
        let hash = self.hash(&words, len);
        let at = (hash ^ hash >> 32) as usize & self.mask;
        let tag = (hash >> 56) as u8 | 1;
        if self.tags[at].get() != tag {
            return self.miss(at, tag, owner, words, view, name_wire);
        }
        let slot = self.slots[at].get();
        let diff = words.iter().zip(&slot.words).fold(
            (slot.owner ^ owner) | u64::from(slot.len ^ len as u8),
            |d, (a, b)| d | (a ^ b),
        );
        if diff == 0 {
            self.hits.set(self.hits.get() + 1);
            let hit = ListHit {
                list: slot.list,
                set: slot.set,
                offset: slot.kind >> 2,
            };
            return match slot.kind & 3 {
                BLOCKED => FilterDecision::Blocked(hit),
                ALLOWED => FilterDecision::Allowed(hit),
                _ => FilterDecision::None,
            };
        }
        self.miss(at, tag, owner, words, view, name_wire)
    }

    /// Decides without the cache and fills the slot (kept out of line so the hit path stays small).
    #[inline(never)]
    #[allow(clippy::too_many_arguments)] // the hit path's locals, passed on unchanged
    fn miss(
        &self,
        at: usize,
        tag: u8,
        owner: u64,
        words: [u64; WORDS],
        view: &FilterView,
        name_wire: &[u8],
    ) -> FilterDecision {
        let decision = uncached(view, name_wire);
        let (kind, hit) = match decision {
            FilterDecision::Blocked(hit) => (BLOCKED, hit),
            FilterDecision::Allowed(hit) => (ALLOWED, hit),
            FilterDecision::None => (
                NONE,
                ListHit {
                    list: 0,
                    set: 0,
                    offset: 0,
                },
            ),
        };
        self.tags[at].set(tag);
        self.slots[at].set(Slot {
            owner,
            set: hit.set,
            list: hit.list,
            // `name_wire.len() <= CACHED_NAME_MAX` (48), so `hit.offset < 64`.
            kind: kind | hit.offset << 2,
            len: name_wire.len() as u8,
            words,
        });
        decision
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::filter::domain_to_wire;
    use crate::filter::index::{FilterIndex, IndexOptions, ListInput, ListKind};
    use std::sync::Arc;

    fn w(s: &str) -> Box<[u8]> {
        domain_to_wire(s.as_bytes()).unwrap()
    }

    fn index(block: &[u8], allow: &[u8]) -> Arc<FilterIndex> {
        let inputs = [
            ListInput {
                id: "b",
                category: "",
                category_slot: 0,
                kind: ListKind::Block,
                text: block,
            },
            ListInput {
                id: "a",
                category: "",
                category_slot: 0,
                kind: ListKind::Allow,
                text: allow,
            },
        ];
        Arc::new(FilterIndex::build(&inputs, &IndexOptions::new(16 << 20)).unwrap())
    }

    #[test]
    fn repeated_names_are_answered_from_the_cache() {
        let idx = index(b"ads.test\n", b"ok.ads.test\n");
        let view = idx.view(&[0], &[1]);
        let cache = DecisionCache::new(64);
        let names = ["x.ads.test", "ok.ads.test", "clean.test"];
        let first: Vec<_> = names.iter().map(|n| cache.decide(&view, &w(n))).collect();
        let before = cache.hits();
        let again: Vec<_> = names.iter().map(|n| cache.decide(&view, &w(n))).collect();
        assert_eq!(cache.hits() - before, 3, "every repeat is a hit");
        assert_eq!(first, again);
        assert!(matches!(
            first[0],
            FilterDecision::Blocked(ListHit { list: 0, .. })
        ));
        assert!(matches!(
            first[1],
            FilterDecision::Allowed(ListHit {
                list: 1,
                offset: 0,
                ..
            })
        ));
        assert_eq!(first[2], FilterDecision::None);
        let long = format!("{}.ads.test", "l".repeat(CACHED_NAME_MAX));
        let before = cache.hits();
        for _ in 0..2 {
            assert!(matches!(
                cache.decide(&view, &w(&long)),
                FilterDecision::Blocked(_)
            ));
        }
        assert_eq!(
            cache.hits(),
            before,
            "names above {CACHED_NAME_MAX} octets bypass the cache"
        );
    }

    #[test]
    fn a_new_index_generation_invalidates_decisions() {
        let old = index(b"ads.test\n", b"");
        let new = index(b"other.test\n", b"");
        let (v_old, v_new) = (old.view(&[0], &[]), new.view(&[0], &[]));
        assert!(new.generation() > old.generation());
        assert_eq!(
            v_old.cache_owner() & 0xFFFF,
            v_new.cache_owner() & 0xFFFF,
            "the same view id on both builds"
        );
        let cache = DecisionCache::new(64);
        let name = w("x.ads.test");
        assert!(matches!(
            cache.decide(&v_old, &name),
            FilterDecision::Blocked(_)
        ));
        let before = cache.hits();
        assert!(matches!(
            cache.decide(&v_old, &name),
            FilterDecision::Blocked(_)
        ));
        assert_eq!(cache.hits(), before + 1, "positive path: the old view hits");
        assert_eq!(
            cache.decide(&v_new, &name),
            FilterDecision::None,
            "not listed in the new build"
        );
        assert_eq!(
            cache.hits(),
            before + 1,
            "the new generation never reads the old decision"
        );
    }

    #[test]
    fn colliding_names_never_share_a_decision() {
        let idx = index(b"ads.test\nads2.test\n", b"");
        let view = idx.view(&[0], &[]);
        // One slot and one forced hash: every name lands on the same slot with the same hash.
        let cache = DecisionCache::new(1);
        FORCED_HASH.with(|c| c.set(Some(0xDEAD_BEEF)));
        let before = cache.hits();
        let seq = [
            ("ads.test", true),
            ("ads.test", true),
            ("adt.test", false),
            ("ads.tesu", false),
            ("ads2.test", true),
            ("bds2.test", false),
            ("ads.test", true),
        ];
        let got: Vec<bool> = seq
            .iter()
            .map(|(n, _)| matches!(cache.decide(&view, &w(n)), FilterDecision::Blocked(_)))
            .collect();
        let hit_count = cache.hits() - before;
        FORCED_HASH.with(|c| c.set(None));
        assert_eq!(got, seq.iter().map(|(_, b)| *b).collect::<Vec<_>>());
        assert_eq!(hit_count, 1, "only the immediate repeat of ads.test hits");
    }

    #[test]
    fn views_on_one_index_keep_separate_decisions() {
        let idx = index(b"ads.test\n", b"ok.ads.test\n");
        let (blocking, allowing, nothing) = (
            idx.view(&[0], &[1]),
            idx.view(&[], &[1]),
            idx.view(&[0], &[]),
        );
        assert_ne!(blocking.cache_owner(), allowing.cache_owner());
        assert_ne!(blocking.cache_owner(), nothing.cache_owner());
        assert_eq!(
            idx.view(&[0], &[1]).cache_owner(),
            blocking.cache_owner(),
            "equal views share an id"
        );
        let cache = DecisionCache::new(1);
        let (ads, ok) = (w("x.ads.test"), w("ok.ads.test"));
        assert!(matches!(
            cache.decide(&blocking, &ads),
            FilterDecision::Blocked(_)
        ));
        assert_eq!(cache.decide(&allowing, &ads), FilterDecision::None);
        assert!(matches!(
            cache.decide(&blocking, &ads),
            FilterDecision::Blocked(_)
        ));
        assert!(matches!(
            cache.decide(&blocking, &ok),
            FilterDecision::Allowed(_)
        ));
        assert!(matches!(
            cache.decide(&nothing, &ok),
            FilterDecision::Blocked(_)
        ));
        assert!(matches!(
            cache.decide(&blocking, &ok),
            FilterDecision::Allowed(_)
        ));
    }
}
