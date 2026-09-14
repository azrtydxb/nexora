//! The shared filter index: every block and allow list of a snapshot in one pointer-free table of
//! 128-byte blocks, with per-policy views over its list sets.
//!
//! Names are grouped by their last two labels: a name is placed in one of the two candidate blocks
//! of the hash of its two-label suffix (its "key"), so one fetch of two blocks answers every level
//! of a query name at once. A two-label suffix with more than [`GROUP_MAX`] names is heavy: its
//! longer names are keyed by their own hash instead, and the suffix gets a marker entry that sends
//! lookups under it to a second, per-level probe. Single-label names are keyed by their own hash.

use super::names::{self, Levels, MAX_LEVELS};
use super::prefetch::prefetch_read;
use super::storage::{AlignedBytes, BLOCK};
use rustc_hash::FxHashMap;
use std::mem::MaybeUninit;
use std::sync::Arc;
use std::sync::atomic::{AtomicU64, AtomicUsize, Ordering};
use std::time::Instant;

/// Target fill of the primary blocks.
pub const BLOCK_FILL: f64 = 0.88;
/// List indexes are `u16`; `u16::MAX` marks "no list".
pub const MAX_LISTS: usize = 65_534;
/// Index builds so far in this process (generations start at 1).
pub static BUILDS: AtomicU64 = AtomicU64::new(0);
/// Names per two-label suffix above which the suffix is heavy.
pub const GROUP_MAX: usize = 4;

/// Block header: entry count, then the overflow tags (bit `fingerprint & 7` of every name that
/// has this block as a candidate but lives in the stash); then one fingerprint octet per entry and
/// the entries in the same order.
const HEADER: usize = 2;
const CAPACITY: usize = BLOCK - HEADER;
/// Entries per block, so three fingerprint words cover a block.
const MAX_ENTRIES: usize = 24;
const STASH_FILL: f64 = 0.5;
const COUNT_MASK: u8 = 0x3F;
/// Short entry header bit: this name is a heavy two-label suffix.
const MARKER: u8 = 0x80;
const LONG: u8 = 0xFF;
const LONG_MARKER: u8 = 0xFE;
const LONG_SYMBOLS: usize = 64;
const STASHED: u32 = u32::MAX;
const SCAN_CHUNK: usize = 16 << 20;
const ONES: u64 = 0x0101_0101_0101_0101;
/// Per entry count: 0x80 in the fingerprint octets of the first word that hold entries.
static FP_MASKS: [u64; 64] = {
    let mut t = [0u64; 64];
    let mut c = 1;
    while c < 64 {
        let n = if c >= 8 { 8 } else { c };
        t[c] = (u64::MAX >> (64 - 8 * n)) & (ONES << 7);
        c += 1;
    }
    t
};

/// `Unique::flags`: the name has three or more labels.
const DEEP: u8 = 0x01;
/// `Unique::flags`: the name has one label.
const SINGLE: u8 = 0x02;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ListKind {
    Block,
    Allow,
}

/// One decompressed list: one domain per line.
pub struct ListInput<'a> {
    pub id: &'a str,
    pub category: &'a str,
    pub category_slot: u8,
    pub kind: ListKind,
    pub text: &'a [u8],
}

#[derive(Clone, Debug)]
pub struct ListMeta {
    pub id: Box<str>,
    pub category: Box<str>,
    pub category_slot: u8,
    pub kind: ListKind,
    pub invalid_lines: u64,
}

#[derive(Clone, Copy, Debug)]
pub struct IndexOptions {
    pub max_bytes: u64,
    pub threads: usize,
    pub seed: u64,
    pub fill: f64,
}

impl IndexOptions {
    /// Up to four build threads, the default fill and a random seed.
    pub fn new(max_bytes: u64) -> IndexOptions {
        use rand::RngExt;
        let mut seed = [0u8; 8];
        rand::rng().fill(&mut seed[..]);
        IndexOptions {
            max_bytes,
            threads: std::thread::available_parallelism()
                .map_or(1, |n| n.get())
                .min(4),
            seed: u64::from_le_bytes(seed),
            fill: BLOCK_FILL,
        }
    }
}

#[derive(Debug, PartialEq, Eq, thiserror::Error)]
pub enum IndexError {
    #[error("filter index needs {needed} bytes, above the cap of {cap} bytes")]
    OverCap { needed: u64, cap: u64 },
    #[error("{0} filter lists exceed 65534")]
    TooManyLists(usize),
    #[error("filter index placement failed after 4 stash resizes")]
    Placement,
}

/// A matched name: the first list of the view that lists it, and its list set.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct ListHit {
    pub list: u16,
    pub set: u32,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum FilterDecision {
    None,
    Blocked(ListHit),
    Allowed,
}

/// One valid list line during the build (24 bytes).
#[derive(Clone, Copy, Default)]
struct Record {
    hash: u64,
    key: u64,
    offset: u32,
    list: u16,
    len: u8,
    labels: u8,
}

/// One deduplicated name (or heavy-suffix marker) with its list set and placement key.
#[derive(Clone, Copy, Default)]
struct Unique {
    key: u64,
    hash: u64,
    offset: u32,
    set: u32,
    list: u16,
    len: u8,
    flags: u8,
}

fn text_at<'a>(lists: &[ListInput<'a>], list: u16, offset: u32, len: u8) -> &'a [u8] {
    &lists[usize::from(list)].text[offset as usize..offset as usize + usize::from(len)]
}

struct SetTable {
    words: usize,
    bits: Vec<u64>,
    counts: Vec<u32>,
    heads: FxHashMap<u64, u32>,
    next: Vec<u32>,
}

impl SetTable {
    fn new(lists: usize) -> SetTable {
        let words = lists.div_ceil(64).max(1);
        // Set 0 is the empty set, never interned.
        SetTable {
            words,
            bits: vec![0; words],
            counts: vec![0],
            heads: Default::default(),
            next: vec![u32::MAX],
        }
    }
    fn len(&self) -> usize {
        self.counts.len()
    }
    fn get(&self, id: u32) -> &[u64] {
        &self.bits[id as usize * self.words..][..self.words]
    }
    /// The id of `set`, counting `uses` more uses of it.
    fn intern(&mut self, set: &[u64], uses: u32) -> u32 {
        let key = set.iter().fold(0x243F_6A88_85A3_08D3u64, |h, &w| {
            names::fold(h ^ w, 0x9E37_79B9_7F4A_7C15)
        });
        let mut id = self.heads.get(&key).copied().unwrap_or(u32::MAX);
        while id != u32::MAX {
            if self.get(id) == set {
                self.counts[id as usize] += uses;
                return id;
            }
            id = self.next[id as usize];
        }
        let new = self.counts.len() as u32;
        self.bits.extend_from_slice(set);
        self.counts.push(uses);
        self.next
            .push(self.heads.insert(key, new).unwrap_or(u32::MAX));
        new
    }
    /// Renumbers sets by descending use so frequent sets get one-byte varints; returns old -> new.
    fn renumber(&mut self) -> Vec<u32> {
        let mut order: Vec<u32> = (1..self.len() as u32).collect();
        order.sort_by_key(|&id| std::cmp::Reverse(self.counts[id as usize]));
        let mut remap = vec![0u32; self.len()];
        let mut bits = vec![0u64; self.words];
        let mut counts = vec![0u32];
        for (new, &old) in order.iter().enumerate() {
            remap[old as usize] = new as u32 + 1;
            bits.extend_from_slice(self.get(old));
            counts.push(self.counts[old as usize]);
        }
        self.bits = bits;
        self.counts = counts;
        self.heads = Default::default();
        self.next = Vec::new();
        remap
    }
}

/// One suffix of a query name: its wire octets from `start`, `symbols` of them before the root.
#[derive(Clone, Copy)]
struct Query<'a> {
    wire: &'a [u8],
    start: usize,
    symbols: u8,
}

/// Every block and allow list of a snapshot, deduplicated into 128-byte blocks. Immutable.
pub struct FilterIndex {
    seed: u64,
    blocks: AlignedBytes,
    block_count: u32,
    stash: AlignedBytes,
    stash_count: u32,
    long: Vec<u8>,
    /// Bit `hash & 255` of every single-label name, so most lookups skip that probe.
    single_mask: [u64; 4],
    /// Keys of heavy two-label suffixes (open addressing, 0 = empty), small enough to stay in
    /// cache, so lookups under them prefetch their per-level blocks in the first wave.
    heavy: Box<[u64]>,
    set_words: usize,
    set_bits: Vec<u64>,
    set_count: usize,
    lists: Vec<ListMeta>,
    by_id: FxHashMap<Box<str>, u16>,
    entries: u64,
    invalid_lines: u64,
    stash_entries: u64,
    memory_bytes: u64,
    build_seconds: f64,
    generation: u64,
}

/// Runs `work(i)` for `i` in `0..items` on `threads` scoped threads (inline for one thread).
fn run_parallel<T: Send>(items: usize, threads: usize, work: impl Fn(usize) -> T + Sync) -> Vec<T> {
    if threads <= 1 || items <= 1 {
        return (0..items).map(work).collect();
    }
    let slots: Vec<parking_lot::Mutex<Option<T>>> =
        (0..items).map(|_| parking_lot::Mutex::new(None)).collect();
    let cursor = AtomicUsize::new(0);
    std::thread::scope(|s| {
        for _ in 0..threads.min(items) {
            s.spawn(|| {
                loop {
                    let i = cursor.fetch_add(1, Ordering::Relaxed);
                    if i >= items {
                        break;
                    }
                    *slots[i].lock() = Some(work(i));
                }
            });
        }
    });
    slots
        .into_iter()
        .map(|m| m.into_inner().expect("every item ran"))
        .collect()
}

/// The records and invalid line count of one scanned chunk.
type Scanned = (Vec<Record>, u64);

/// Splits list texts into line-aligned chunks and hashes every line into records.
fn scan_lists(lists: &[ListInput<'_>], opts: &IndexOptions) -> (Vec<Vec<Record>>, Vec<u64>) {
    let mut items: Vec<(u16, usize, usize)> = Vec::new();
    for (list, input) in lists.iter().enumerate() {
        let text = input.text;
        // Blobs decompress to at most 512 MiB, so offsets fit u32.
        assert!(
            text.len() <= u32::MAX as usize,
            "list {} is above 4 GiB",
            input.id
        );
        let mut start = 0;
        while start < text.len() {
            let mut end = (start + SCAN_CHUNK).min(text.len());
            if end < text.len() {
                end = (names::find_byte(text, end, b'\n') + 1).min(text.len());
            }
            items.push((list as u16, start, end));
            start = end;
        }
    }
    let results: Vec<Scanned> = run_parallel(items.len(), opts.threads, |i| {
        let (list, start, end) = items[i];
        let text = lists[usize::from(list)].text;
        let mut out = Vec::with_capacity((end - start) / 12 + 16);
        let mut invalid = 0;
        let mut at = start;
        while at < end {
            let line_end = names::find_byte(text, at, b'\n').min(end);
            if line_end > at {
                match names::text_hashes_in(opts.seed, text, at, line_end) {
                    Some(t) => out.push(Record {
                        hash: t.hash,
                        key: t.key,
                        offset: at as u32,
                        list,
                        len: (line_end - at) as u8,
                        labels: t.labels,
                    }),
                    None => invalid += 1,
                }
            }
            at = line_end + 1;
        }
        (out, invalid)
    });
    let mut invalid = vec![0u64; lists.len()];
    let mut parts = Vec::with_capacity(results.len());
    for (&(list, _, _), (records, bad)) in items.iter().zip(results) {
        invalid[usize::from(list)] += bad;
        parts.push(records);
    }
    (parts, invalid)
}

/// Scatters records by the top key byte into one allocation, part by part in parallel (each part
/// writes into offsets reserved for it); returns it with the bucket sizes.
fn scatter(parts: Vec<Vec<Record>>, threads: usize) -> (Vec<Record>, [usize; 256]) {
    let part_counts: Vec<[usize; 256]> = run_parallel(parts.len(), threads, |p| {
        let mut c = [0usize; 256];
        for r in &parts[p] {
            c[(r.key >> 56) as usize] += 1;
        }
        c
    });
    let mut counts = [0usize; 256];
    for c in &part_counts {
        for (t, n) in counts.iter_mut().zip(c) {
            *t += n;
        }
    }
    // Bucket starts, then each part's first offset per bucket.
    let mut cursor = [0usize; 256];
    let mut at = 0;
    for (c, n) in cursor.iter_mut().zip(counts) {
        *c = at;
        at += n;
    }
    let mut offsets = Vec::with_capacity(parts.len());
    for c in &part_counts {
        offsets.push(cursor);
        for (cur, n) in cursor.iter_mut().zip(c) {
            *cur += n;
        }
    }
    let mut out = vec![Record::default(); at];
    let dst = SharedRecords(out.as_mut_ptr());
    run_parallel(parts.len(), threads, |p| {
        let mut cur = offsets[p];
        for r in &parts[p] {
            let b = (r.key >> 56) as usize;
            // SAFETY: part p's records go to [offsets[p][b], offsets[p][b] + part_counts[p][b]),
            // ranges that are disjoint across parts and buckets and inside `out`.
            unsafe { dst.at(cur[b]).write(*r) };
            cur[b] += 1;
        }
    });
    (out, counts)
}

/// A pointer to the scatter output shared by the threads writing disjoint ranges of it.
#[derive(Clone, Copy)]
struct SharedRecords(*mut Record);

impl SharedRecords {
    fn at(self, i: usize) -> *mut Record {
        self.0.wrapping_add(i)
    }
}
// SAFETY: every thread writes only its own disjoint index range (see `scatter`).
unsafe impl Send for SharedRecords {}
// SAFETY: as above.
unsafe impl Sync for SharedRecords {}

/// The last two labels of a validated text name.
fn two_label_suffix(text: &[u8]) -> &[u8] {
    let mut dots = text.iter().enumerate().rev().filter(|(_, c)| **c == b'.');
    match (dots.next(), dots.next()) {
        (Some(_), Some((i, _))) => &text[i + 1..],
        _ => text,
    }
}

/// Sorts a bucket (records sharing the top key octet) by `(key, hash)` into `sorted`: a scatter
/// by the next key octet into cache-sized runs, each sorted in place.
fn sort_bucket(bucket: &[Record], sorted: &mut Vec<Record>) {
    let mut starts = [0usize; 257];
    for r in bucket {
        starts[((r.key >> 48) & 0xFF) as usize + 1] += 1;
    }
    for i in 0..256 {
        starts[i + 1] += starts[i];
    }
    sorted.clear();
    sorted.resize(bucket.len(), Record::default());
    let mut fill = starts;
    for r in bucket {
        let b = ((r.key >> 48) & 0xFF) as usize;
        sorted[fill[b]] = *r;
        fill[b] += 1;
    }
    for b in 0..256 {
        sorted[starts[b]..starts[b + 1]]
            .sort_unstable_by_key(|r| (u128::from(r.key) << 64) | u128::from(r.hash));
    }
}

/// Sorts one bucket by `(key, hash)`, merges equal names (ASCII case-insensitively) into
/// uniques with local set ids, and splits heavy two-label groups (flat keys and markers).
fn dedupe_bucket(
    lists: &[ListInput<'_>],
    bucket: &mut [Record],
    sorted: &mut Vec<Record>,
    sets: &mut SetTable,
    out: &mut Vec<Unique>,
) {
    sort_bucket(bucket, sorted);
    let bucket = &sorted[..bucket.len()];
    let mut scratch = vec![0u64; sets.words];
    // Set ids of the one-list sets, which most names have.
    let mut single = vec![u32::MAX; lists.len()];
    let text = |r: &Record| text_at(lists, r.list, r.offset, r.len);
    let mut i = 0;
    while i < bucket.len() {
        // Warm the texts of an upcoming run of equal names, which are compared.
        if let Some(ahead) = bucket.get(i + 16..i + 18)
            && ahead[0].hash == ahead[1].hash
        {
            for r in ahead {
                prefetch_read(
                    lists[usize::from(r.list)]
                        .text
                        .as_ptr()
                        .wrapping_add(r.offset as usize),
                );
            }
        }
        let key = bucket[i].key;
        let group_start = out.len();
        while i < bucket.len() && bucket[i].key == key {
            let hash = bucket[i].hash;
            let mut j = i + 1;
            while j < bucket.len() && bucket[j].key == key && bucket[j].hash == hash {
                j += 1;
            }
            let run = &bucket[i..j];
            if run.len() == 1 {
                let r = &run[0];
                let slot = &mut single[usize::from(r.list)];
                let set = if *slot == u32::MAX {
                    scratch.fill(0);
                    scratch[usize::from(r.list) / 64] |= 1 << (r.list % 64);
                    *slot = sets.intern(&scratch, 1);
                    *slot
                } else {
                    sets.counts[*slot as usize] += 1;
                    *slot
                };
                out.push(Unique {
                    key,
                    hash,
                    offset: r.offset,
                    set,
                    list: r.list,
                    len: r.len,
                    flags: match r.labels {
                        1 => SINGLE,
                        2 => 0,
                        _ => DEEP,
                    },
                });
                i = j;
                continue;
            }
            for (k, r) in run.iter().enumerate() {
                scratch.fill(0);
                if run.len() > 1 {
                    let t = text(r);
                    if run[..k].iter().any(|e| text(e).eq_ignore_ascii_case(t)) {
                        continue;
                    }
                    for e in &run[k..] {
                        if text(e).eq_ignore_ascii_case(t) {
                            scratch[usize::from(e.list) / 64] |= 1 << (e.list % 64);
                        }
                    }
                } else {
                    scratch[usize::from(r.list) / 64] |= 1 << (r.list % 64);
                }
                out.push(Unique {
                    key,
                    hash,
                    offset: r.offset,
                    set: sets.intern(&scratch, 1),
                    list: r.list,
                    len: r.len,
                    flags: match r.labels {
                        1 => SINGLE,
                        2 => 0,
                        _ => DEEP,
                    },
                });
            }
            i = j;
        }
        if out.len() - group_start > GROUP_MAX {
            split_heavy_group(lists, out, group_start);
        }
    }
}

/// Gives the deep names of a heavy group their own hash as key and marks every distinct
/// two-label suffix of the group (adding a marker entry when the suffix is not listed itself).
fn split_heavy_group(lists: &[ListInput<'_>], out: &mut Vec<Unique>, start: usize) {
    let end = out.len();
    let group_key = out[start].key;
    // Distinct suffixes as (list, offset, len); a group almost always has exactly one.
    let mut suffixes: Vec<(u16, u32, u8)> = Vec::new();
    for u in &mut out[start..end] {
        let t = text_at(lists, u.list, u.offset, u.len);
        let s = two_label_suffix(t);
        let known = suffixes
            .iter()
            .any(|&(l, o, n)| text_at(lists, l, o, n).eq_ignore_ascii_case(s));
        if !known {
            let offset = u.offset + (t.len() - s.len()) as u32;
            suffixes.push((u.list, offset, s.len() as u8));
        }
        if u.flags & DEEP != 0 {
            u.key = u.hash;
        }
    }
    for (list, offset, len) in suffixes {
        let s = text_at(lists, list, offset, len);
        let listed = out[start..end].iter_mut().find(|u| {
            u.flags & DEEP == 0 && text_at(lists, u.list, u.offset, u.len).eq_ignore_ascii_case(s)
        });
        match listed {
            Some(u) => u.flags |= MARKER,
            None => {
                out.push(Unique {
                    key: group_key,
                    hash: group_key,
                    offset,
                    set: 0,
                    list,
                    len,
                    flags: MARKER,
                });
            }
        }
    }
}

/// The multi-octet set ids of rare list combinations.
#[cold]
#[inline(never)]
fn read_varint_cold(bytes: &[u8]) -> (u32, usize) {
    names::read_varint(bytes)
}

/// The directory fingerprint of an entry: its placement key's fingerprint and its symbol count,
/// so a probe of a group derives every level's fingerprint from the one key hash it has.
#[inline(always)]
fn entry_fingerprint(key: u64, symbols: u8) -> u8 {
    names::fingerprint(key) ^ symbols
}

fn entry_len(symbols: usize, set: u32) -> usize {
    if symbols > LONG_SYMBOLS {
        1 + names::varint_len(set) + 1 + 4
    } else {
        1 + names::varint_len(set) + (7 * symbols).div_ceil(8)
    }
}

/// Placement state of one block: bytes used, entries, and the first of its members (a list
/// threaded through `next`), in one cache-friendly 8-byte slot.
#[derive(Clone, Copy)]
struct Slot {
    used: u8,
    count: u8,
    head: u32,
}

impl Slot {
    const EMPTY: Slot = Slot {
        used: 0,
        count: 0,
        head: u32::MAX,
    };

    fn fits(self, size: u8) -> bool {
        usize::from(self.used) + usize::from(size) <= CAPACITY
            && usize::from(self.count) < MAX_ENTRIES
    }
}

/// The less-used candidate block that still has room for an entry of `size` bytes, else the other.
fn pick(slots: &[Slot], b1: u32, b2: u32, size: u8) -> Option<u32> {
    let (first, second) = if slots[b2 as usize].used < slots[b1 as usize].used {
        (b2, b1)
    } else {
        (b1, b2)
    };
    [first, second]
        .into_iter()
        .find(|&b| slots[b as usize].fits(size))
}

/// Places items `(key, unique, size)` (in the given order) into `slots`, threading each block's
/// members through `next`; returns the chosen block per unique (`STASHED` for none) and the
/// uniques that did not fit.
fn place(order: &[(u64, u32, u8)], slots: &mut [Slot], next: &mut [u32]) -> (Vec<u32>, Vec<u32>) {
    let blocks = slots.len() as u32;
    let mut chosen = vec![STASHED; next.len()];
    let mut overflow = Vec::new();
    for (p, &(key, i, size)) in order.iter().enumerate() {
        if let Some(&(ahead, _, _)) = order.get(p + 16) {
            let (a1, a2) = names::candidates(ahead, blocks);
            prefetch_read((&slots[a1 as usize] as *const Slot).cast());
            prefetch_read((&slots[a2 as usize] as *const Slot).cast());
        }
        let (b1, b2) = names::candidates(key, blocks);
        match pick(slots, b1, b2, size) {
            Some(b) => {
                let slot = &mut slots[b as usize];
                slot.used += size;
                slot.count += 1;
                next[i as usize] = slot.head;
                slot.head = i;
                chosen[i as usize] = b;
            }
            None => overflow.push(i),
        }
    }
    (chosen, overflow)
}

/// Places overflow names by moving one entry of a candidate block to that entry's other candidate
/// (one cuckoo step); returns the names that still do not fit.
fn relocate(
    uniques: &[Unique],
    sizes: &[u8],
    chosen: &mut [u32],
    slots: &mut [Slot],
    next: &mut [u32],
    overflow: Vec<u32>,
) -> Vec<u32> {
    let blocks = slots.len() as u32;
    let mut still = Vec::new();
    'names: for i in overflow {
        let size = sizes[i as usize];
        let (b1, b2) = names::candidates(uniques[i as usize].key, blocks);
        for b in [b1, b2] {
            let (mut prev, mut v) = (u32::MAX, slots[b as usize].head);
            while v != u32::MAX {
                let v_size = sizes[v as usize];
                let (a1, a2) = names::candidates(uniques[v as usize].key, blocks);
                let alt = if a1 == b { a2 } else { a1 };
                let here = slots[b as usize];
                let fits_here = usize::from(here.used - v_size) + usize::from(size) <= CAPACITY;
                if alt != b && fits_here && slots[alt as usize].fits(v_size) {
                    // Unlink v from b, link it into alt, and put i in its place.
                    let after = next[v as usize];
                    if prev == u32::MAX {
                        slots[b as usize].head = after;
                    } else {
                        next[prev as usize] = after;
                    }
                    let there = &mut slots[alt as usize];
                    next[v as usize] = there.head;
                    there.head = v;
                    there.used += v_size;
                    there.count += 1;
                    chosen[v as usize] = alt;
                    let here = &mut slots[b as usize];
                    next[i as usize] = here.head;
                    here.head = i;
                    here.used = here.used - v_size + size;
                    chosen[i as usize] = b;
                    continue 'names;
                }
                (prev, v) = (v, next[v as usize]);
            }
        }
        still.push(i);
    }
    still
}

/// An open-addressing set of `keys` with at least twice as many slots, 0 meaning empty (a zero
/// key is left out: lookups under it take the second wave, which stays correct).
fn heavy_table(keys: &[u64]) -> Box<[u64]> {
    if keys.is_empty() {
        return Box::new([]);
    }
    let slots = (keys.len() * 2).next_power_of_two();
    let mut table = vec![0u64; slots];
    for &k in keys.iter().filter(|&&k| k != 0) {
        let mut i = (k >> 40) as usize & (slots - 1);
        while table[i] != 0 && table[i] != k {
            i = (i + 1) & (slots - 1);
        }
        table[i] = k;
    }
    table.into_boxed_slice()
}

/// Places the overflow names into a stash of blocks, doubling it on failure.
fn place_stash(
    uniques: &[Unique],
    sizes: &[u8],
    overflow: &[u32],
) -> Result<(u32, Vec<u32>), IndexError> {
    let total: usize = overflow
        .iter()
        .map(|&i| usize::from(sizes[i as usize]))
        .sum();
    let mut count = ((total as f64 / (CAPACITY as f64 * STASH_FILL)).ceil() as usize).max(1);
    for _ in 0..5 {
        let Ok(n) = u32::try_from(count) else { break };
        let mut slots = vec![Slot::EMPTY; count];
        let mut chosen = Vec::with_capacity(overflow.len());
        for &i in overflow {
            let (b1, b2) = names::candidates(uniques[i as usize].key, n);
            let size = sizes[i as usize];
            let Some(b) = pick(&slots, b1, b2, size) else {
                break;
            };
            slots[b as usize].used += size;
            slots[b as usize].count += 1;
            chosen.push(b);
        }
        if chosen.len() == overflow.len() {
            return Ok((n, chosen));
        }
        count *= 2;
    }
    Err(IndexError::Placement)
}

/// Packs a validated text name (octets outside the alphabet were rejected by the scan).
fn pack_text_into(text: &[u8], dst: &mut [u8]) -> usize {
    let mut packed = [0u8; names::PACKED_LEN];
    let n = names::pack_wire_text(text, &mut packed);
    dst[..n].copy_from_slice(&packed[..n]);
    n
}

struct BlockWriter<'a, 'b> {
    uniques: &'a [Unique],
    lists: &'a [ListInput<'b>],
    long_at: &'a [(u32, u32)],
}

impl BlockWriter<'_, '_> {
    /// Encodes the entry of unique `m` into `out`; returns its length.
    fn encode(&self, out: &mut [u8; 72], m: u32) -> usize {
        let u = &self.uniques[m as usize];
        let marker = u.flags & MARKER != 0;
        let symbols = u.len + 1;
        if usize::from(symbols) > LONG_SYMBOLS {
            out[0] = if marker { LONG_MARKER } else { LONG };
            let mut pos = 1 + names::write_varint(&mut out[1..], u.set);
            out[pos] = symbols;
            pos += 1;
            let at = self
                .long_at
                .binary_search_by_key(&m, |&(i, _)| i)
                .expect("long names are in the arena");
            out[pos..pos + 4].copy_from_slice(&self.long_at[at].1.to_le_bytes());
            pos + 4
        } else {
            out[0] = symbols | if marker { MARKER } else { 0 };
            let pos = 1 + names::write_varint(&mut out[1..], u.set);
            let text = text_at(self.lists, u.list, u.offset, u.len);
            pos + pack_text_into(text, &mut out[pos..])
        }
    }

    /// Writes `count` blocks. Item `p` (unique `item(p)`) goes to block `chosen[p]`. A sequential
    /// pass fixes every entry's slot and octet position, so `threads` threads then write disjoint
    /// octet ranges over contiguous item ranges, prefetching the texts and blocks ahead.
    fn write_all(
        &self,
        count: usize,
        chosen: &[u32],
        item: impl Fn(usize) -> u32 + Sync,
        tags: &[u8],
        threads: usize,
    ) -> AlignedBytes {
        // One spare block keeps word comparisons of the last block's entries inside the table.
        let mut bytes = AlignedBytes::zeroed((count + 1) * BLOCK);
        let mut cursor: Vec<(u8, u8)> = vec![(0, 0); count];
        for &b in chosen.iter().filter(|&&b| b != STASHED) {
            cursor[b as usize].1 += 1;
        }
        {
            let table = bytes.as_mut_slice();
            for (b, c) in cursor.iter_mut().enumerate() {
                table[b * BLOCK] = c.1;
                table[b * BLOCK + 1] = tags.get(b).copied().unwrap_or(0);
                *c = ((HEADER + usize::from(c.1)) as u8, 0);
            }
        }
        // Per item: octet offset of its entry and of its fingerprint in the table.
        let places: Vec<(u32, u32)> = chosen
            .iter()
            .enumerate()
            .map(|(p, &b)| {
                if b == STASHED {
                    return (u32::MAX, u32::MAX);
                }
                let u = &self.uniques[item(p) as usize];
                let (pos, k) = cursor[b as usize];
                cursor[b as usize] = (pos + entry_len(usize::from(u.len) + 1, u.set) as u8, k + 1);
                let base = b * BLOCK as u32;
                (base + u32::from(pos), base + (HEADER as u32) + u32::from(k))
            })
            .collect();
        let dst = SharedBytes(bytes.as_mut_slice().as_mut_ptr());
        let per = chosen.len().div_ceil(threads.max(1)).max(1);
        run_parallel(chosen.len().div_ceil(per), threads, |t| {
            let mut entry = [0u8; 72];
            for p in t * per..((t + 1) * per).min(chosen.len()) {
                if let Some(&(ahead, _)) = places.get(p + 8)
                    && ahead != u32::MAX
                {
                    let u = &self.uniques[item(p + 8) as usize];
                    let text = self.lists[usize::from(u.list)].text;
                    prefetch_read(text.as_ptr().wrapping_add(u.offset as usize));
                    prefetch_read(dst.at(ahead as usize));
                }
                let (at, fp_at) = places[p];
                if at == u32::MAX {
                    continue;
                }
                let m = item(p);
                let n = self.encode(&mut entry, m);
                let u = &self.uniques[m as usize];
                // SAFETY: `places` gives every item its own octets (entries do not overlap and fit
                // their block, fingerprints have one slot each), inside the table.
                unsafe {
                    std::ptr::copy_nonoverlapping(entry.as_ptr(), dst.at(at as usize), n);
                    dst.at(fp_at as usize)
                        .write(entry_fingerprint(u.key, u.len + 1));
                }
            }
        });
        bytes
    }
}

/// A pointer to table octets shared by threads writing disjoint ranges of them.
#[derive(Clone, Copy)]
struct SharedBytes(*mut u8);

impl SharedBytes {
    fn at(self, i: usize) -> *mut u8 {
        self.0.wrapping_add(i)
    }
}

// SAFETY: every thread writes only octets `write_all` assigned to its items.
unsafe impl Send for SharedBytes {}
// SAFETY: as above.
unsafe impl Sync for SharedBytes {}

impl FilterIndex {
    /// Builds the index; never allocates blocks for an index above `opts.max_bytes`.
    pub fn build(lists: &[ListInput<'_>], opts: &IndexOptions) -> Result<FilterIndex, IndexError> {
        let started = Instant::now();
        if lists.len() > MAX_LISTS {
            return Err(IndexError::TooManyLists(lists.len()));
        }
        // The build reads list texts at random; on huge pages those reads (and their prefetches)
        // do not miss the TLB.
        let total: usize = lists.iter().map(|l| l.text.len() + 8).sum();
        let mut arena = AlignedBytes::zeroed(total.max(1));
        let mut at = 0;
        for l in lists {
            arena.as_mut_slice()[at..at + l.text.len()].copy_from_slice(l.text);
            at += l.text.len() + 8;
        }
        let mut at = 0;
        let local: Vec<ListInput<'_>> = lists
            .iter()
            .map(|l| {
                let text = &arena.as_slice()[at..at + l.text.len()];
                at += l.text.len() + 8;
                ListInput { text, ..*l }
            })
            .collect();
        let lists = &local[..];
        let (parts, invalid) = scan_lists(lists, opts);
        let (mut records, bucket_sizes) = scatter(parts, opts.threads);

        // Sort and dedupe the key buckets in parallel, each thread with its own set table.
        let threads = opts.threads.max(1);
        let mut buckets: Vec<&mut [Record]> = Vec::with_capacity(256);
        let mut rest = records.as_mut_slice();
        for n in bucket_sizes {
            let (head, tail) = rest.split_at_mut(n);
            buckets.push(head);
            rest = tail;
        }
        let per = 256usize.div_ceil(threads);
        let groups: Vec<parking_lot::Mutex<&mut [&mut [Record]]>> = buckets
            .chunks_mut(per)
            .map(parking_lot::Mutex::new)
            .collect();
        let deduped = run_parallel(groups.len(), threads, |g| {
            let mut group = groups[g].lock();
            let mut sets = SetTable::new(lists.len());
            let mut out = Vec::with_capacity(group.iter().map(|b| b.len()).sum());
            let mut sorted = Vec::new();
            for bucket in group.iter_mut() {
                dedupe_bucket(lists, bucket, &mut sorted, &mut sets, &mut out);
            }
            (sets, out)
        });
        drop(groups);
        drop(records);
        let mut sets = SetTable::new(lists.len());
        let mut uniques = Vec::with_capacity(deduped.iter().map(|(_, u)| u.len()).sum());
        for (local, part) in deduped {
            let remap: Vec<u32> = (0..local.len() as u32)
                .map(|id| {
                    if id == 0 {
                        0
                    } else {
                        sets.intern(local.get(id), local.counts[id as usize])
                    }
                })
                .collect();
            uniques.extend(part.into_iter().map(|u| Unique {
                set: remap[u.set as usize],
                ..u
            }));
        }
        let remap = sets.renumber();
        let mut single_mask = [0u64; 4];
        let mut heavy_keys = Vec::new();
        for u in &mut uniques {
            u.set = remap[u.set as usize];
            if u.flags & SINGLE != 0 {
                single_mask[(u.hash >> 6) as usize & 3] |= 1 << (u.hash & 63);
            }
            if u.flags & MARKER != 0 {
                heavy_keys.push(u.key);
            }
        }
        let heavy = heavy_table(&heavy_keys);

        let mut sizes = Vec::with_capacity(uniques.len());
        let (mut sum, mut long_bytes) = (0usize, 0usize);
        for u in &uniques {
            // The entry and its fingerprint octet.
            let size = entry_len(usize::from(u.len) + 1, u.set) + 1;
            sum += size;
            if usize::from(u.len) + 1 > LONG_SYMBOLS {
                long_bytes += (7 * (usize::from(u.len) + 1)).div_ceil(8);
            }
            sizes.push(size as u8);
        }
        let block_count = ((sum as f64 / (CAPACITY as f64 * opts.fill)).ceil() as usize).max(1);
        let needed =
            ((block_count + 1) * BLOCK + long_bytes + 8 + 8 * sets.bits.len() + 2 * BLOCK) as u64;
        if needed > opts.max_bytes || block_count > u32::MAX as usize {
            return Err(IndexError::OverCap {
                needed,
                cap: opts.max_bytes,
            });
        }

        // Largest entries first (key order within a size) leaves the least unusable slack.
        let mut size_starts = [0usize; 256];
        for &size in &sizes {
            size_starts[usize::from(size)] += 1;
        }
        let mut at = 0;
        for start in size_starts.iter_mut().rev() {
            (*start, at) = (at, at + *start);
        }
        let mut order = vec![(0u64, 0u32, 0u8); uniques.len()];
        for (i, (u, &size)) in uniques.iter().zip(&sizes).enumerate() {
            order[size_starts[usize::from(size)]] = (u.key, i as u32, size);
            size_starts[usize::from(size)] += 1;
        }
        let mut slots = vec![Slot::EMPTY; block_count];
        let mut next = vec![u32::MAX; uniques.len()];
        let (mut chosen, overflow) = place(&order, &mut slots, &mut next);
        drop(order);
        let overflow = relocate(
            &uniques,
            &sizes,
            &mut chosen,
            &mut slots,
            &mut next,
            overflow,
        );
        drop((slots, next));
        let mut tags = vec![0u8; block_count];
        for &i in &overflow {
            let u = &uniques[i as usize];
            let (b1, b2) = names::candidates(u.key, block_count as u32);
            let tag = 1 << (entry_fingerprint(u.key, u.len + 1) & 7);
            tags[b1 as usize] |= tag;
            tags[b2 as usize] |= tag;
        }
        let (stash_count, stash_chosen) = place_stash(&uniques, &sizes, &overflow)?;
        drop(sizes);
        let needed = needed - 2 * BLOCK as u64 + u64::from(stash_count + 1) * BLOCK as u64;
        if needed > opts.max_bytes {
            return Err(IndexError::OverCap {
                needed,
                cap: opts.max_bytes,
            });
        }

        let mut long = Vec::with_capacity(long_bytes + 8);
        let mut long_at = Vec::new();
        let mut packer = names::Packer::new();
        for (i, u) in uniques.iter().enumerate() {
            if usize::from(u.len) + 1 > LONG_SYMBOLS {
                long_at.push((i as u32, long.len() as u32));
                names::pack_text(text_at(lists, u.list, u.offset, u.len), &mut packer);
                long.extend_from_slice(packer.bytes());
            }
        }
        long.extend_from_slice(&[0; 8]);

        let writer = BlockWriter {
            uniques: &uniques,
            lists,
            long_at: &long_at,
        };
        let blocks = writer.write_all(block_count, &chosen, |p| p as u32, &tags, threads);
        let stash = writer.write_all(
            stash_count as usize,
            &stash_chosen,
            |p| overflow[p],
            &[],
            threads,
        );

        let metas: Vec<ListMeta> = lists
            .iter()
            .zip(&invalid)
            .map(|(l, &bad)| ListMeta {
                id: l.id.into(),
                category: l.category.into(),
                category_slot: l.category_slot,
                kind: l.kind,
                invalid_lines: bad,
            })
            .collect();
        let by_id = metas
            .iter()
            .enumerate()
            .map(|(i, m)| (m.id.clone(), i as u16))
            .collect();
        let memory_bytes = (blocks.len()
            + stash.len()
            + long.len()
            + 8 * sets.bits.len()
            + 4 * sets.counts.len()
            + 64 * metas.len()) as u64;
        Ok(FilterIndex {
            seed: opts.seed,
            blocks,
            block_count: block_count as u32,
            stash,
            stash_count,
            long,
            single_mask,
            heavy,
            set_words: sets.words,
            set_count: sets.len(),
            set_bits: sets.bits,
            lists: metas,
            by_id,
            entries: uniques.iter().filter(|u| u.set != 0).count() as u64,
            invalid_lines: invalid.iter().sum(),
            stash_entries: overflow.len() as u64,
            memory_bytes,
            build_seconds: started.elapsed().as_secs_f64(),
            generation: BUILDS.fetch_add(1, Ordering::Relaxed) + 1,
        })
    }

    /// An index without lists (generation 0).
    pub fn empty() -> FilterIndex {
        FilterIndex {
            seed: 0,
            blocks: AlignedBytes::zeroed(2 * BLOCK),
            block_count: 1,
            stash: AlignedBytes::zeroed(2 * BLOCK),
            stash_count: 1,
            long: Vec::new(),
            single_mask: [0; 4],
            heavy: Box::new([]),
            set_words: 1,
            set_bits: vec![0],
            set_count: 1,
            lists: Vec::new(),
            by_id: FxHashMap::default(),
            entries: 0,
            invalid_lines: 0,
            stash_entries: 0,
            memory_bytes: 4 * BLOCK as u64,
            build_seconds: 0.0,
            generation: 0,
        }
    }

    pub fn entries(&self) -> u64 {
        self.entries
    }

    pub fn invalid_lines(&self) -> u64 {
        self.invalid_lines
    }

    pub fn stash_entries(&self) -> u64 {
        self.stash_entries
    }

    pub fn memory_bytes(&self) -> u64 {
        self.memory_bytes
    }

    pub fn build_seconds(&self) -> f64 {
        self.build_seconds
    }

    pub fn generation(&self) -> u64 {
        self.generation
    }

    pub fn lists(&self) -> &[ListMeta] {
        &self.lists
    }

    pub fn list_index(&self, id: &str) -> Option<u16> {
        self.by_id.get(id).copied()
    }

    /// A view that blocks with the `block` lists and allows with the `allow` lists (list indexes).
    pub fn view(self: &Arc<Self>, block: &[u16], allow: &[u16]) -> FilterView {
        let words = self.set_words;
        let mask = |ls: &[u16]| {
            let mut m = vec![0u64; words];
            for &l in ls.iter().filter(|&&l| usize::from(l) < self.lists.len()) {
                m[usize::from(l) / 64] |= 1 << (l % 64);
            }
            m
        };
        let (block_mask, allow_mask) = (mask(block), mask(allow));
        let mut class = vec![0u8; self.set_count];
        let mut first_list = vec![u16::MAX; self.set_count];
        let mut categories = vec![0u64; self.set_count];
        for id in 1..self.set_count {
            let set = &self.set_bits[id * words..][..words];
            for (w, (&s, (&b, &a))) in set
                .iter()
                .zip(block_mask.iter().zip(&allow_mask))
                .enumerate()
            {
                if s & a != 0 {
                    class[id] |= 2;
                }
                let mut hits = s & b;
                if hits != 0 {
                    class[id] |= 1;
                    if first_list[id] == u16::MAX {
                        first_list[id] = (w * 64) as u16 + hits.trailing_zeros() as u16;
                    }
                }
                while hits != 0 {
                    let l = w * 64 + hits.trailing_zeros() as usize;
                    categories[id] |= 1u64
                        .checked_shl(u32::from(self.lists[l].category_slot))
                        .unwrap_or(0);
                    hits &= hits - 1;
                }
            }
        }
        FilterView {
            index: self.clone(),
            memory_bytes: 11 * self.set_count as u64,
            class: class.into_boxed_slice(),
            first_list: first_list.into_boxed_slice(),
            categories: categories.into_boxed_slice(),
            has_allow: !allow.is_empty(),
            empty: block.is_empty() && allow.is_empty(),
        }
    }

    #[inline(always)]
    fn prefetch_blocks(&self, key: u64) {
        let (b1, b2) = names::candidates(key, self.block_count);
        let base = self.blocks.as_ptr();
        for b in [b1, b2] {
            let p = base.wrapping_add(b as usize * BLOCK);
            prefetch_read(p);
            prefetch_read(p.wrapping_add(64));
        }
    }

    /// The set id and marker flag of entry `k` of block `b` of `table` when it holds `symbols`
    /// packed symbols equal to `query`. Tables have one spare block, so word comparisons of an
    /// entry at the end of a block stay inside the table.
    #[inline(always)]
    fn entry(&self, table: &AlignedBytes, b: u32, k: usize, q: Query<'_>) -> Option<(u32, bool)> {
        let block = table.block(b);
        let count = usize::from(block[0] & COUNT_MASK);
        // Entries follow the fingerprints in slot order; skip the k before this one.
        let mut pos = HEADER + count;
        for _ in 0..k {
            let head = block[pos];
            let set_len = if block[pos + 1] < 0x80 {
                1
            } else {
                read_varint_cold(&block[pos + 1..]).1
            };
            pos += 1
                + set_len
                + if head >= LONG_MARKER {
                    5
                } else {
                    (7 * usize::from(head & !MARKER)).div_ceil(8)
                };
        }
        let e = &table.as_slice()[b as usize * BLOCK + pos..];
        let head = e[0];
        if head == LONG || head == LONG_MARKER {
            let (set, n) = read_varint_cold(&e[1..]);
            if e[1 + n] == q.symbols {
                let off = u32::from_le_bytes([e[2 + n], e[3 + n], e[4 + n], e[5 + n]]) as usize;
                if names::suffix_matches(&self.long[off..], q.wire, q.start, q.symbols) {
                    return Some((set, head == LONG_MARKER));
                }
            }
        } else if head & !MARKER == q.symbols {
            let (set, n) = if e[1] < 0x80 {
                (u32::from(e[1]), 1)
            } else {
                read_varint_cold(&e[1..])
            };
            if names::suffix_matches(&e[1 + n..], q.wire, q.start, q.symbols) {
                return Some((set, head & MARKER != 0));
            }
        }
        None
    }

    /// The entry with fingerprint `fp` among slots `first..` of block `b` of `table`.
    #[inline(never)]
    fn scan_block(
        &self,
        table: &AlignedBytes,
        b: u32,
        first: usize,
        fp: u8,
        q: Query<'_>,
    ) -> Option<(u32, bool)> {
        let block = table.block(b);
        let count = usize::from(block[0] & COUNT_MASK);
        (first..count).find_map(|k| {
            if block[HEADER + k] == fp {
                self.entry(table, b, k, q)
            } else {
                None
            }
        })
    }

    /// The entry among the slots set in `slots` (0x80 in octet k for slot k) of block `b`.
    #[inline(always)]
    fn confirm(&self, b: u32, mut slots: u64, q: Query<'_>) -> Option<(u32, bool)> {
        while slots != 0 {
            let k = slots.trailing_zeros() as usize / 8;
            slots &= slots - 1;
            if let Some(hit) = self.entry(&self.blocks, b, k, q) {
                return Some(hit);
            }
        }
        None
    }

    #[inline(always)]
    fn single_bit(&self, hash: u64) -> bool {
        self.single_mask[(hash >> 6) as usize & 3] & (1 << (hash & 63)) != 0
    }

    #[inline(always)]
    fn is_heavy(&self, key: u64) -> bool {
        let slots = self.heavy.len();
        if slots == 0 {
            return false;
        }
        let mut i = (key >> 40) as usize & (slots - 1);
        loop {
            match self.heavy[i] {
                0 => return false,
                k if k == key => return true,
                _ => i = (i + 1) & (slots - 1),
            }
        }
    }

    /// Probes the two candidate blocks of `key` (and the stash when a tag says so) for the levels
    /// `from..to` of `levels`, recording matches in `found`/`sets`. Returns whether level `from`
    /// is a heavy-suffix marker.
    #[inline(always)]
    #[allow(clippy::needless_range_loop)] // `level` also selects the level's suffix and bit.
    fn probe(
        &self,
        key: u64,
        name: &[u8],
        levels: &Levels,
        (from, to): (usize, usize),
        found: &mut u128,
        sets: &mut [MaybeUninit<u32>; MAX_LEVELS],
    ) -> bool {
        const LOW7: u64 = !(ONES << 7);
        let (b1, b2) = names::candidates(key, self.block_count);
        let (x1, x2) = (self.blocks.block(b1), self.blocks.block(b2));
        let first_word = |x: &[u8; BLOCK]| {
            let w = u64::from_le_bytes(x[HEADER..HEADER + 8].try_into().expect("8 bytes"));
            (w, FP_MASKS[usize::from(x[0] & COUNT_MASK)])
        };
        let ((w1, m1), (w2, m2)) = (first_word(x1), first_word(x2));
        let tags = x1[1] | x2[1];
        let more = (x1[0] & COUNT_MASK) > 8 || (x2[0] & COUNT_MASK) > 8;
        let key_fp = names::fingerprint(key);
        let mut marker = false;
        for level in from..to {
            let fp = key_fp ^ levels.symbols(level);
            let rep = ONES * u64::from(fp);
            let (y1, y2) = (w1 ^ rep, w2 ^ rep);
            let hit1 = !(((y1 & LOW7) + LOW7) | y1) & m1;
            let hit2 = !(((y2 & LOW7) + LOW7) | y2) & m2;
            let stashed = tags & (1 << (fp & 7)) != 0;
            if hit1 | hit2 == 0 && !more && !stashed {
                continue;
            }
            let q = Query {
                wire: name,
                start: levels.start(level),
                symbols: levels.symbols(level),
            };
            let mut hit = self.confirm(b1, hit1, q);
            if hit.is_none() {
                hit = self.confirm(b2, hit2, q);
            }
            if hit.is_none() && more {
                hit = self.scan_block(&self.blocks, b1, 8, fp, q);
                if hit.is_none() {
                    hit = self.scan_block(&self.blocks, b2, 8, fp, q);
                }
            }
            if hit.is_none() && stashed {
                let (s1, s2) = names::candidates(key, self.stash_count);
                hit = self.scan_block(&self.stash, s1, 0, fp, q);
                if hit.is_none() {
                    hit = self.scan_block(&self.stash, s2, 0, fp, q);
                }
            }
            if let Some((set, is_marker)) = hit {
                if set != 0 {
                    *found |= 1 << level;
                    sets[level] = MaybeUninit::new(set);
                }
                marker |= is_marker && level == from;
            }
        }
        marker
    }

    /// Calls `visit` with the set id of every listed suffix of a lowercase wire name, longest
    /// first, until it returns true. Allocation-free. Only the one- and two-label suffixes are
    /// hashed (and every level under a heavy suffix); the blocks they select are prefetched as soon
    /// as their hash is known.
    #[inline]
    fn for_each_match(&self, name_wire: &[u8], mut visit: impl FnMut(u32) -> bool) {
        if self.entries == 0 {
            return;
        }
        let mut pad = [0u8; 16];
        let name: &[u8] = if name_wire.len() >= 8 {
            name_wire
        } else {
            pad[..name_wire.len()].copy_from_slice(name_wire);
            &pad
        };
        let mut levels = Levels::new();
        let (mut level, mut heavy) = (0, false);
        names::walk_hashes(self.seed, name, name_wire.len(), &mut levels, |h| {
            match level {
                0 if self.single_bit(h) => self.prefetch_blocks(h),
                1 => {
                    self.prefetch_blocks(h);
                    heavy = self.is_heavy(h);
                }
                2.. => self.prefetch_blocks(h),
                _ => {}
            }
            level += 1;
            level < 2 || heavy
        });
        let labels = levels.labels();
        let mut found = 0u128;
        let mut sets = [MaybeUninit::<u32>::uninit(); MAX_LEVELS];
        if levels.count() > 0 && self.single_bit(levels.hash(0)) {
            let key = levels.hash(0);
            self.probe(key, name, &levels, (0, 1), &mut found, &mut sets);
        }
        if levels.count() > 1 {
            let key = levels.hash(1);
            if self.probe(key, name, &levels, (1, labels), &mut found, &mut sets) && labels > 2 {
                if levels.count() < labels {
                    // Only a heavy key the in-cache table cannot hold (zero) gets here.
                    levels = Levels::new();
                    names::walk_hashes(self.seed, name, name_wire.len(), &mut levels, |h| {
                        self.prefetch_blocks(h);
                        true
                    });
                }
                for l in 2..labels {
                    let key = levels.hash(l);
                    self.probe(key, name, &levels, (l, l + 1), &mut found, &mut sets);
                }
            }
        }
        while found != 0 {
            let level = 127 - found.leading_zeros() as usize;
            found &= !(1 << level);
            // SAFETY: `probe` initialises sets[level] whenever it sets bit `level` of `found`.
            if visit(unsafe { sets[level].assume_init() }) {
                return;
            }
        }
    }
}

/// One policy's verdicts over a shared [`FilterIndex`]: per list set, allow/block class, the first
/// matching block list and category slot bits.
pub struct FilterView {
    index: Arc<FilterIndex>,
    class: Box<[u8]>,
    first_list: Box<[u16]>,
    categories: Box<[u64]>,
    has_allow: bool,
    empty: bool,
    memory_bytes: u64,
}

impl FilterView {
    #[inline]
    pub fn decide(&self, name_wire: &[u8]) -> FilterDecision {
        if self.empty {
            return FilterDecision::None;
        }
        let mut decision = FilterDecision::None;
        self.index.for_each_match(name_wire, |set| {
            let class = self.class[set as usize];
            if class & 2 != 0 {
                decision = FilterDecision::Allowed;
                return true;
            }
            if class & 1 != 0 && decision == FilterDecision::None {
                decision = FilterDecision::Blocked(ListHit {
                    list: self.first_list[set as usize],
                    set,
                });
                return !self.has_allow;
            }
            false
        });
        decision
    }

    pub fn cloaked(&self, cname_targets: &[crate::wire::NameKey]) -> Option<ListHit> {
        cname_targets
            .iter()
            .find_map(|t| match self.decide(t.as_wire()) {
                FilterDecision::Blocked(hit) => Some(hit),
                _ => None,
            })
    }

    #[inline]
    pub fn categories(&self, hit: ListHit) -> u64 {
        self.categories[hit.set as usize]
    }

    pub fn memory_bytes(&self) -> u64 {
        self.memory_bytes
    }

    pub fn index(&self) -> &Arc<FilterIndex> {
        &self.index
    }

    pub fn is_empty(&self) -> bool {
        self.empty
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::filter::BlockMode;
    use crate::filter::domain_to_wire;
    use crate::filter::oracle::{Decision, FilterSet};

    struct XorShift(u64);
    impl XorShift {
        fn next(&mut self) -> u64 {
            self.0 ^= self.0 << 13;
            self.0 ^= self.0 >> 7;
            self.0 ^= self.0 << 17;
            self.0
        }
        fn below(&mut self, n: usize) -> usize {
            (self.next() % n as u64) as usize
        }
    }

    fn opts() -> IndexOptions {
        IndexOptions {
            max_bytes: 64 << 20,
            threads: 2,
            seed: 7,
            fill: BLOCK_FILL,
        }
    }

    /// Names over a tiny label alphabet, so exact names and suffixes repeat across lists.
    fn name(r: &mut XorShift) -> String {
        const LABELS: [&str; 10] = [
            "a", "b", "ab", "a-b", "x_1", "0", "test", "example", "cdn", "www",
        ];
        let labels = 1 + r.below(4);
        (0..labels)
            .map(|_| LABELS[r.below(LABELS.len())])
            .collect::<Vec<_>>()
            .join(".")
    }

    fn list_text(r: &mut XorShift, lines: usize) -> Vec<u8> {
        let mut t = String::new();
        for _ in 0..lines {
            match r.below(20) {
                0 => t.push_str("-bad-.example"),
                1 => t.push_str("UPPER.Example"),
                2 => {}
                _ => t.push_str(&name(r)),
            }
            t.push('\n');
        }
        t.into_bytes()
    }

    fn subset(r: &mut XorShift, of: &[u16]) -> Vec<u16> {
        of.iter().copied().filter(|_| r.below(3) == 0).collect()
    }

    fn inputs<'a>(
        ids: &'a [String],
        texts: &'a [Vec<u8>],
        kinds: &[ListKind],
    ) -> Vec<ListInput<'a>> {
        (0..texts.len())
            .map(|i| ListInput {
                id: &ids[i],
                category: if i % 2 == 0 { "ads" } else { "" },
                category_slot: (i % 2) as u8,
                kind: kinds[i],
                text: &texts[i],
            })
            .collect()
    }

    /// The first list (by index) of the longest suffix any of `per_list` lists exactly.
    fn expected_list(per_list: &[(u16, FilterSet)], wire: &[u8]) -> u16 {
        let mut pos = 0;
        while wire[pos] != 0 {
            let suffix = &wire[pos..];
            if let Some((i, _)) = per_list
                .iter()
                .filter(|(_, s)| s.blocks_exactly(suffix))
                .min_by_key(|(i, _)| *i)
            {
                return *i;
            }
            pos += 1 + usize::from(wire[pos]);
        }
        panic!("blocked without a listed suffix");
    }

    #[test]
    fn filter_index_matches_filterset_semantics() {
        let mut r = XorShift(0x9E37_79B9_7F4A_7C15);
        for round in 0..40 {
            let list_count = if round % 4 == 3 { 70 } else { 1 + r.below(12) };
            let texts: Vec<Vec<u8>> = (0..list_count)
                .map(|_| {
                    let n = r.below(40);
                    list_text(&mut r, n)
                })
                .collect();
            let kinds: Vec<ListKind> = (0..list_count)
                .map(|_| {
                    if r.below(4) == 0 {
                        ListKind::Allow
                    } else {
                        ListKind::Block
                    }
                })
                .collect();
            let ids: Vec<String> = (0..list_count).map(|i| format!("l{i}")).collect();
            let index =
                Arc::new(FilterIndex::build(&inputs(&ids, &texts, &kinds), &opts()).unwrap());
            let (_, all) = FilterSet::build(&texts, &[], BlockMode::NullIp, 60);
            assert_eq!(
                index.invalid_lines(),
                all.invalid_lines as u64,
                "invalid lines (round {round})"
            );
            let block: Vec<u16> = (0..list_count as u16)
                .filter(|&i| kinds[usize::from(i)] == ListKind::Block)
                .collect();
            let allow: Vec<u16> = (0..list_count as u16)
                .filter(|&i| kinds[usize::from(i)] == ListKind::Allow)
                .collect();
            let mut views = vec![(block.clone(), allow.clone())];
            for _ in 0..3 {
                views.push((subset(&mut r, &block), subset(&mut r, &allow)));
            }
            for (vb, va) in &views {
                let view = index.view(vb, va);
                let pick = |ls: &[u16]| {
                    ls.iter()
                        .map(|&i| texts[usize::from(i)].clone())
                        .collect::<Vec<_>>()
                };
                let (oracle, _) = FilterSet::build(&pick(vb), &pick(va), BlockMode::NullIp, 60);
                let per_list: Vec<(u16, FilterSet)> = vb
                    .iter()
                    .map(|&i| {
                        (
                            i,
                            FilterSet::build(
                                &[texts[usize::from(i)].clone()],
                                &[],
                                BlockMode::NullIp,
                                60,
                            )
                            .0,
                        )
                    })
                    .collect();
                for _ in 0..400 {
                    let q = format!(
                        "{}{}",
                        if r.below(2) == 0 { "www." } else { "" },
                        name(&mut r)
                    );
                    let wire = domain_to_wire(q.as_bytes()).unwrap();
                    match (oracle.decide(&wire), view.decide(&wire)) {
                        (Decision::None, FilterDecision::None)
                        | (Decision::Allowed, FilterDecision::Allowed) => {}
                        (Decision::Blocked, FilterDecision::Blocked(hit)) => {
                            assert_eq!(
                                hit.list,
                                expected_list(&per_list, &wire),
                                "attribution of {q} (round {round})"
                            );
                        }
                        (want, got) => panic!(
                            "{q} (round {round}, view {vb:?}/{va:?}): oracle {want:?}, index {got:?}"
                        ),
                    }
                }
            }
        }
    }

    fn one_block_list(text: &[u8], o: IndexOptions) -> (Arc<FilterIndex>, FilterView) {
        let input = [ListInput {
            id: "l",
            category: "",
            category_slot: 0,
            kind: ListKind::Block,
            text,
        }];
        let index = Arc::new(FilterIndex::build(&input, &o).unwrap());
        let view = index.view(&[0], &[]);
        (index, view)
    }

    fn w(s: &str) -> Box<[u8]> {
        domain_to_wire(s.as_bytes()).unwrap()
    }

    #[test]
    fn hash_collisions_are_confirmed_against_stored_names() {
        crate::filter::names::FORCED_HASH.with(|c| c.set(Some(0x0123_4567_89AB_CDEF)));
        let (index, view) = one_block_list(
            b"one.test\ntwo.test\nthree.test\n",
            IndexOptions {
                threads: 1,
                ..opts()
            },
        );
        let blocked = [
            view.decide(&w("one.test")),
            view.decide(&w("x.two.test")),
            view.decide(&w("three.test")),
        ];
        let unlisted = [
            view.decide(&w("four.test")),
            view.decide(&w("test")),
            view.decide(&w("onetest")),
        ];
        crate::filter::names::FORCED_HASH.with(|c| c.set(None));
        assert_eq!(index.entries(), 3, "colliding names are all stored");
        assert!(
            blocked
                .iter()
                .all(|d| matches!(d, FilterDecision::Blocked(_))),
            "{blocked:?}"
        );
        assert!(
            unlisted.iter().all(|d| *d == FilterDecision::None),
            "{unlisted:?}"
        );
    }

    #[test]
    fn more_than_64_lists_attribute_and_split_views() {
        let texts: Vec<Vec<u8>> = (0..100)
            .map(|i| {
                let mut t = format!("n{i}.test\n");
                if i == 70 || i == 99 {
                    t.push_str("shared.test\n");
                }
                t.into_bytes()
            })
            .collect();
        let ids: Vec<String> = (0..100).map(|i| format!("l{i}")).collect();
        let kinds = vec![ListKind::Block; 100];
        let index = Arc::new(FilterIndex::build(&inputs(&ids, &texts, &kinds), &opts()).unwrap());
        let high: Vec<u16> = (65..100).collect();
        let v = index.view(&high, &[]);
        assert!(matches!(
            v.decide(&w("a.shared.test")),
            FilterDecision::Blocked(ListHit { list: 70, .. })
        ));
        assert!(matches!(
            index.view(&[99], &[]).decide(&w("shared.test")),
            FilterDecision::Blocked(ListHit { list: 99, .. })
        ));
        assert!(matches!(
            v.decide(&w("n80.test")),
            FilterDecision::Blocked(ListHit { list: 80, .. })
        ));
        assert_eq!(
            index.view(&[0, 1, 2], &[]).decide(&w("n80.test")),
            FilterDecision::None
        );
        assert_eq!(index.list_index("l70"), Some(70));
    }

    #[test]
    fn long_names_empty_lists_and_empty_views() {
        let long = format!("{a}.{a}.{a}.{b}", a = "a".repeat(63), b = "b".repeat(61));
        let (index, view) = one_block_list(format!("{long}\nshort.test\n").as_bytes(), opts());
        assert!(matches!(view.decide(&w(&long)), FilterDecision::Blocked(_)));
        let mut near = long.clone().into_bytes();
        near[0] = b'c';
        assert_eq!(
            view.decide(&domain_to_wire(&near).unwrap()),
            FilterDecision::None
        );
        assert!(matches!(
            view.decide(&w("short.test")),
            FilterDecision::Blocked(_)
        ));
        assert_eq!(
            index.view(&[], &[]).decide(&w("short.test")),
            FilterDecision::None
        );
        let (empty, ev) = one_block_list(b"", opts());
        assert_eq!(
            (empty.entries(), ev.decide(&w("short.test"))),
            (0, FilterDecision::None)
        );
        assert_eq!(
            Arc::new(FilterIndex::empty())
                .view(&[], &[])
                .decide(&w("short.test")),
            FilterDecision::None
        );
    }

    #[test]
    fn the_stash_holds_overflow_and_lookups_find_it() {
        let mut r = XorShift(3);
        let mut text = String::new();
        let mut listed = Vec::new();
        for i in 0..20_000 {
            let n = format!("s{}-{i}.example{}.test", r.next() % 1_000_000, r.below(50));
            text.push_str(&n);
            text.push('\n');
            listed.push(n);
        }
        let (index, view) = one_block_list(
            text.as_bytes(),
            IndexOptions {
                fill: 0.99,
                ..opts()
            },
        );
        assert!(index.stash_entries() > 0, "fill 0.99 overflows some blocks");
        for n in &listed {
            assert!(
                matches!(view.decide(&w(n)), FilterDecision::Blocked(_)),
                "{n}"
            );
        }
        for i in 0..20_000 {
            assert_eq!(
                view.decide(&w(&format!("u{i}.example1.test"))),
                FilterDecision::None
            );
        }
    }

    #[test]
    fn heavy_suffixes_are_found_through_their_markers() {
        let mut text = String::from("big2.test\n");
        for i in 0..50 {
            text.push_str(&format!("n{i}.big.test\nx.n{i}.big2.test\n"));
        }
        text.push_str("deep.n1.big.test\nsingle\n");
        let (index, view) = one_block_list(text.as_bytes(), opts());
        assert_eq!(
            index.entries(),
            103,
            "the marker of big.test is not an entry"
        );
        let blocked = |q: &str| matches!(view.decide(&w(q)), FilterDecision::Blocked(_));
        assert!(
            blocked("n7.big.test") && blocked("www.n7.big.test") && blocked("deep.n1.big.test")
        );
        assert!(blocked("big2.test") && blocked("a.big2.test") && blocked("x.n9.big2.test"));
        assert!(blocked("single") && blocked("a.single"));
        for clean in [
            "big.test",
            "other.big.test",
            "n7.big.test.other",
            "n50.big.test",
            "test",
            "x.n9.big3.test",
        ] {
            assert_eq!(view.decide(&w(clean)), FilterDecision::None, "{clean}");
        }
    }

    #[test]
    fn over_cap_is_rejected_before_blocks_are_allocated() {
        let text: String = (0..10_000)
            .map(|i| format!("name{i}.example.test\n"))
            .collect();
        let input = [ListInput {
            id: "l",
            category: "",
            category_slot: 0,
            kind: ListKind::Block,
            text: text.as_bytes(),
        }];
        match FilterIndex::build(
            &input,
            &IndexOptions {
                max_bytes: 4096,
                ..opts()
            },
        ) {
            Err(IndexError::OverCap { needed, cap }) => {
                assert!(needed > 4096 && cap == 4096, "{needed} {cap}")
            }
            other => panic!("expected OverCap, got {:?}", other.map(|i| i.entries())),
        }
        let before = BUILDS.load(Ordering::Relaxed);
        let ok = FilterIndex::build(&input, &opts()).unwrap();
        assert!(ok.generation() > before && ok.memory_bytes() < 64 << 20);
    }
}
