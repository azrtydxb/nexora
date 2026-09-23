//! The shared filter index: every block and allow list of a snapshot in one pointer-free table of
//! 128-byte blocks, with per-policy views over its list sets.
//!
//! Names are grouped by their last two labels: a name is placed in one of the two candidate blocks
//! of the hash of its two-label suffix (its "key"), so one fetch of two blocks answers every level
//! of a query name at once. A two-label suffix with more than [`GROUP_MAX`] names is heavy: its
//! longer names are keyed by their own hash instead, and the suffix gets a marker entry that sends
//! lookups under it to a second, per-level probe. Single-label names are keyed by their own hash.

use super::memory::BuildMemory;
use super::names::{self, Levels, MAX_LEVELS};
use super::prefetch::prefetch_read;
use super::storage::{AlignedBytes, BLOCK, RELEASE_BYTES, huge_vec, release, small_page_vec};
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

/// Decompressed list texts of a build in list order, each in its own zeroed mapping advised for
/// transparent huge pages (the build reads the texts at random, and on huge pages those reads and
/// their prefetches do not miss the TLB) and followed by 8 zero bytes. Decoding blobs straight into
/// it keeps a single copy of every text; the build frees it as soon as every name is encoded.
#[derive(Default)]
pub struct TextArena {
    texts: Vec<(AlignedBytes, usize)>,
}

impl TextArena {
    pub fn new() -> TextArena {
        TextArena::default()
    }

    /// Appends a text of `len` bytes that `fill` writes.
    pub fn push_with<E>(
        &mut self,
        len: usize,
        fill: impl FnOnce(&mut [u8]) -> Result<(), E>,
    ) -> Result<(), E> {
        let mut bytes = AlignedBytes::zeroed(len + 8);
        fill(&mut bytes.as_mut_slice()[..len])?;
        self.texts.push((bytes, len));
        Ok(())
    }

    pub fn push(&mut self, text: &[u8]) {
        let copied: Result<(), std::convert::Infallible> = self.push_with(text.len(), |b| {
            b.copy_from_slice(text);
            Ok(())
        });
        let Ok(()) = copied;
    }

    pub fn len(&self) -> usize {
        self.texts.len()
    }

    pub fn is_empty(&self) -> bool {
        self.texts.is_empty()
    }

    pub fn text(&self, i: usize) -> &[u8] {
        let (bytes, len) = &self.texts[i];
        &bytes.as_slice()[..*len]
    }
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
    #[error(
        "filter index build stopped before {phase}: {in_use} bytes in use plus {needed} bytes \
         needed exceed the engine memory limit of {limit} bytes less a {margin} byte margin"
    )]
    MemoryLimit {
        phase: &'static str,
        in_use: u64,
        needed: u64,
        limit: u64,
        margin: u64,
    },
}

/// A matched name: the first list of the view that lists it, its list set, and the octet offset
/// of the matched (listed) suffix in the queried wire name.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct ListHit {
    pub list: u16,
    pub set: u32,
    pub offset: u8,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum FilterDecision {
    None,
    Blocked(ListHit),
    /// Allowed by the view's first allow list (position order) listing the matched suffix.
    Allowed(ListHit),
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

/// One deduplicated name (or heavy-suffix marker) with its list set and placement key (24 bytes:
/// the name's own hash is only needed while its group is deduplicated).
#[derive(Clone, Copy, Default)]
struct Unique {
    key: u64,
    offset: u32,
    set: u32,
    list: u16,
    len: u8,
    flags: u8,
}

fn text_at<'a>(texts: &[&'a [u8]], list: u16, offset: u32, len: u8) -> &'a [u8] {
    &texts[usize::from(list)][offset as usize..offset as usize + usize::from(len)]
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
    lists: Vec<Arc<ListMeta>>,
    by_id: FxHashMap<Box<str>, u16>,
    entries: u64,
    invalid_lines: u64,
    stash_entries: u64,
    memory_bytes: u64,
    build_seconds: f64,
    generation: u64,
    /// View ids for the decision cache: one per distinct (block, allow) list mask pair, so equal
    /// views share cached decisions and different views never do.
    view_ids: parking_lot::Mutex<FxHashMap<Box<[u64]>, u16>>,
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

/// A line-aligned chunk of one list text: list, start, end, and its line count (a bound on its
/// records).
type Chunk = (u16, usize, usize, usize);

/// Splits list texts into line-aligned chunks and counts their lines in parallel.
fn chunk_lists(texts: &[&[u8]], threads: usize) -> Vec<Chunk> {
    let mut chunks: Vec<Chunk> = Vec::new();
    for (list, text) in texts.iter().enumerate() {
        // Blobs decompress to at most 512 MiB, so offsets fit u32.
        assert!(
            text.len() <= u32::MAX as usize,
            "list {list} is above 4 GiB"
        );
        let mut start = 0;
        while start < text.len() {
            let mut end = (start + SCAN_CHUNK).min(text.len());
            if end < text.len() {
                end = (names::find_byte(text, end, b'\n') + 1).min(text.len());
            }
            chunks.push((list as u16, start, end, 0));
            start = end;
        }
    }
    let lines = run_parallel(chunks.len(), threads, |i| {
        let (list, start, end, _) = chunks[i];
        let t = &texts[usize::from(list)][start..end];
        t.iter().filter(|&&b| b == b'\n').count() + usize::from(t.last() != Some(&b'\n'))
    });
    for (c, n) in chunks.iter_mut().zip(lines) {
        c.3 = n;
    }
    chunks
}

/// Hashes every line of the chunks into records (one exactly sized buffer per chunk).
fn scan_lists(
    texts: &[&[u8]],
    chunks: &[Chunk],
    opts: &IndexOptions,
) -> (Vec<Vec<Record>>, Vec<u64>) {
    let results: Vec<Scanned> = run_parallel(chunks.len(), opts.threads, |i| {
        let (list, start, end, lines) = chunks[i];
        let text = texts[usize::from(list)];
        let mut out = huge_vec(lines);
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
    let mut invalid = vec![0u64; texts.len()];
    let mut parts = Vec::with_capacity(results.len());
    for (&(list, _, _, _), (records, bad)) in chunks.iter().zip(results) {
        invalid[usize::from(list)] += bad;
        parts.push(records);
    }
    (parts, invalid)
}

/// Scatters records by the top key byte into one allocation on base pages, part by part in
/// parallel (each part writes into offsets reserved for it), returning each part's pages to the
/// kernel as they are consumed, so the scatter holds about one copy of the records; returns the
/// records with the bucket sizes.
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
    let mut out: Vec<Record> = small_page_vec(at);
    let dst = SharedMut(out.as_mut_ptr());
    let parts: Vec<parking_lot::Mutex<Vec<Record>>> =
        parts.into_iter().map(parking_lot::Mutex::new).collect();
    let step = RELEASE_BYTES / 2 / size_of::<Record>();
    run_parallel(parts.len(), threads, |p| {
        let mut part = std::mem::take(&mut *parts[p].lock());
        let mut cur = offsets[p];
        for k in 0..part.len() {
            let r = part[k];
            let b = (r.key >> 56) as usize;
            // SAFETY: part p's records go to [offsets[p][b], offsets[p][b] + part_counts[p][b]),
            // ranges that are disjoint across parts and buckets and inside `out`.
            unsafe { dst.at(cur[b]).write(r) };
            cur[b] += 1;
            if (k + 1) % step == 0 {
                // SAFETY: records 0..=k of the part this thread owns were scattered and are never
                // read again; they are integers.
                unsafe { release(part.as_mut_ptr().cast(), (k + 1) * size_of::<Record>()) };
            }
        }
    });
    // SAFETY: the parts wrote every index below `at` exactly once.
    unsafe { out.set_len(at) };
    (out, counts)
}

/// A pointer into one allocation shared by threads that each write their own disjoint index
/// range of it.
struct SharedMut<T>(*mut T);

impl<T> Clone for SharedMut<T> {
    fn clone(&self) -> Self {
        *self
    }
}
impl<T> Copy for SharedMut<T> {}

impl<T> SharedMut<T> {
    fn at(self, i: usize) -> *mut T {
        self.0.wrapping_add(i)
    }
}
// SAFETY: every user writes only the index range it owns (see the call sites).
unsafe impl<T: Send> Send for SharedMut<T> {}
// SAFETY: as above.
unsafe impl<T: Send> Sync for SharedMut<T> {}

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

/// Merges equal names (ASCII case-insensitively) of a bucket sorted by `(key, hash)` into uniques
/// with local set ids, and splits heavy two-label groups (flat keys and markers). `hashes` is
/// scratch for the name hashes of the current group.
fn dedupe_bucket(
    texts: &[&[u8]],
    bucket: &[Record],
    sets: &mut SetTable,
    out: &mut Vec<Unique>,
    hashes: &mut Vec<u64>,
) {
    let mut scratch = vec![0u64; sets.words];
    // Set ids of the one-list sets, which most names have.
    let mut single = vec![u32::MAX; texts.len()];
    let text = |r: &Record| text_at(texts, r.list, r.offset, r.len);
    let flags = |r: &Record| match r.labels {
        1 => SINGLE,
        2 => 0,
        _ => DEEP,
    };
    let mut i = 0;
    while i < bucket.len() {
        // Warm the texts of an upcoming run of equal names, which are compared.
        if let Some(ahead) = bucket.get(i + 16..i + 18)
            && ahead[0].hash == ahead[1].hash
        {
            for r in ahead {
                prefetch_read(
                    texts[usize::from(r.list)]
                        .as_ptr()
                        .wrapping_add(r.offset as usize),
                );
            }
        }
        let key = bucket[i].key;
        let group_start = out.len();
        hashes.clear();
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
                    offset: r.offset,
                    set,
                    list: r.list,
                    len: r.len,
                    flags: flags(r),
                });
                hashes.push(hash);
                i = j;
                continue;
            }
            for (k, r) in run.iter().enumerate() {
                scratch.fill(0);
                let t = text(r);
                if run[..k].iter().any(|e| text(e).eq_ignore_ascii_case(t)) {
                    continue;
                }
                for e in &run[k..] {
                    if text(e).eq_ignore_ascii_case(t) {
                        scratch[usize::from(e.list) / 64] |= 1 << (e.list % 64);
                    }
                }
                out.push(Unique {
                    key,
                    offset: r.offset,
                    set: sets.intern(&scratch, 1),
                    list: r.list,
                    len: r.len,
                    flags: flags(r),
                });
                hashes.push(hash);
            }
            i = j;
        }
        if out.len() - group_start > GROUP_MAX {
            split_heavy_group(texts, out, group_start, hashes);
        }
    }
}

/// Gives the deep names of a heavy group (from `start`, with name hashes `hashes`) their own hash
/// as key and marks every distinct two-label suffix of the group (adding a marker entry when the
/// suffix is not listed itself).
fn split_heavy_group(texts: &[&[u8]], out: &mut Vec<Unique>, start: usize, hashes: &[u64]) {
    let end = out.len();
    let group_key = out[start].key;
    // Distinct suffixes as (list, offset, len); a group almost always has exactly one.
    let mut suffixes: Vec<(u16, u32, u8)> = Vec::new();
    for (u, &hash) in out[start..end].iter_mut().zip(hashes) {
        let t = text_at(texts, u.list, u.offset, u.len);
        let s = two_label_suffix(t);
        let known = suffixes
            .iter()
            .any(|&(l, o, n)| text_at(texts, l, o, n).eq_ignore_ascii_case(s));
        if !known {
            let offset = u.offset + (t.len() - s.len()) as u32;
            suffixes.push((u.list, offset, s.len() as u8));
        }
        if u.flags & DEEP != 0 {
            u.key = hash;
        }
    }
    for (list, offset, len) in suffixes {
        let s = text_at(texts, list, offset, len);
        let listed = out[start..end].iter_mut().find(|u| {
            u.flags & DEEP == 0 && text_at(texts, u.list, u.offset, u.len).eq_ignore_ascii_case(s)
        });
        match listed {
            Some(u) => u.flags |= MARKER,
            None => {
                out.push(Unique {
                    key: group_key,
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

/// Places uniques `order` (in that order) into `slots`, threading each block's members through
/// `next`; returns the chosen block per unique (`STASHED` for none) and the uniques that did not
/// fit.
fn place(
    order: &[u32],
    keys: &[u64],
    sizes: &[u8],
    slots: &mut [Slot],
    next: &mut [u32],
) -> (Vec<u32>, Vec<u32>) {
    let blocks = slots.len() as u32;
    let mut chosen = huge_vec(next.len());
    chosen.resize(next.len(), STASHED);
    let mut overflow = Vec::new();
    for (p, &i) in order.iter().enumerate() {
        if let Some(&ahead) = order.get(p + 16) {
            let (a1, a2) = names::candidates(keys[ahead as usize], blocks);
            prefetch_read((&slots[a1 as usize] as *const Slot).cast());
            prefetch_read((&slots[a2 as usize] as *const Slot).cast());
        }
        let (b1, b2) = names::candidates(keys[i as usize], blocks);
        let size = sizes[i as usize];
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
    keys: &[u64],
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
        let (b1, b2) = names::candidates(keys[i as usize], blocks);
        for b in [b1, b2] {
            let (mut prev, mut v) = (u32::MAX, slots[b as usize].head);
            while v != u32::MAX {
                let v_size = sizes[v as usize];
                let (a1, a2) = names::candidates(keys[v as usize], blocks);
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
    keys: &[u64],
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
            let (b1, b2) = names::candidates(keys[i as usize], n);
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

/// Where the entries of the items to write are in [`Encoded::entries`].
#[derive(Clone, Copy)]
enum Items<'a> {
    /// Every unique in order, with its size.
    All(&'a [u8]),
    /// `(offset, size)` of each listed unique.
    Listed(&'a [(usize, u8)]),
}

/// Writes `count` blocks from `entries` (per unique: its fingerprint octet, then its entry). Item
/// `p` goes to block `chosen[p]`. Each of `threads` threads owns a contiguous block range: it
/// counts its blocks' entries, then copies its items in item order in batches (prefetching blocks
/// ahead), so entry order inside a block is item order whatever the thread count.
fn write_blocks(
    entries: &[u8],
    count: usize,
    chosen: &[u32],
    items: Items<'_>,
    tags: &[u8],
    threads: usize,
) -> AlignedBytes {
    const BATCH: usize = 1024;
    // One spare block keeps word comparisons of the last block's entries inside the table.
    let mut bytes = AlignedBytes::zeroed((count + 1) * BLOCK);
    let dst = SharedMut(bytes.as_mut_slice().as_mut_ptr());
    let per = count.div_ceil(threads.max(1)).max(1);
    run_parallel(count.div_ceil(per), threads, |t| {
        let (lo, hi) = ((t * per) as u32, ((t + 1) * per).min(count) as u32);
        // Per block of the range: next entry octet and next fingerprint slot.
        let mut cursor = vec![(0u8, 0u8); (hi - lo) as usize];
        for &b in chosen {
            if (lo..hi).contains(&b) {
                cursor[(b - lo) as usize].1 += 1;
            }
        }
        for (i, c) in cursor.iter_mut().enumerate() {
            let b = lo as usize + i;
            // SAFETY: the header octets of blocks lo..hi belong to this thread.
            unsafe {
                dst.at(b * BLOCK).write(c.1);
                dst.at(b * BLOCK + 1)
                    .write(tags.get(b).copied().unwrap_or(0));
            }
            *c = ((HEADER + usize::from(c.1)) as u8, 0);
        }
        let mut flush = |batch: &mut Vec<(u32, usize, u8)>| {
            for (j, &(b, at, size)) in batch.iter().enumerate() {
                if let Some(&(ahead, _, _)) = batch.get(j + 8) {
                    prefetch_read(dst.at(ahead as usize * BLOCK));
                }
                let c = &mut cursor[(b - lo) as usize];
                let n = usize::from(size) - 1;
                let entry = &entries[at..at + usize::from(size)];
                let base = b as usize * BLOCK;
                // SAFETY: the cursor gives every entry of block b (owned by this thread) its own
                // octets, which fit the block because placement counted their sizes, and its own
                // fingerprint slot; all inside the table.
                unsafe {
                    std::ptr::copy_nonoverlapping(
                        entry[1..].as_ptr(),
                        dst.at(base + usize::from(c.0)),
                        n,
                    );
                    dst.at(base + HEADER + usize::from(c.1)).write(entry[0]);
                }
                *c = (c.0 + n as u8, c.1 + 1);
            }
            batch.clear();
        };
        let mut batch = Vec::with_capacity(BATCH);
        let mut at = 0;
        for (p, &b) in chosen.iter().enumerate() {
            let (offset, size) = match items {
                Items::All(sizes) => {
                    at += usize::from(sizes[p]);
                    (at - usize::from(sizes[p]), sizes[p])
                }
                Items::Listed(listed) => listed[p],
            };
            if (lo..hi).contains(&b) {
                batch.push((b, offset, size));
                if batch.len() == BATCH {
                    flush(&mut batch);
                }
            }
        }
        flush(&mut batch);
    });
    bytes
}

/// Every unique name of a build encoded for placement; the list texts are no longer needed.
struct Encoded {
    /// Per unique in order: its fingerprint octet, then its entry.
    entries: Vec<u8>,
    keys: Vec<u64>,
    /// Octets per unique in `entries`.
    sizes: Vec<u8>,
    long: Vec<u8>,
    sets: SetTable,
    single_mask: [u64; 4],
    heavy: Box<[u64]>,
    invalid: Vec<u64>,
    /// Uniques that are names (not markers).
    names: u64,
    block_count: usize,
    /// Index bytes without the stash.
    needed: u64,
}

/// Build memory beyond the list texts for `lines` list lines: one record per line (scan, scatter
/// and deduplication each release the buffer they consume, as encoding releases the deduplicated
/// names), plus per-thread release lag and huge-page rounding. Encoding needs more only when the
/// entries with their keys outgrow the deduplicated names; its own reservation checks that.
fn estimate_build_bytes(lines: usize, threads: usize) -> u64 {
    let per_line = size_of::<Record>().max(size_of::<Unique>());
    (lines * per_line + (4 * threads.max(1) + 4) * RELEASE_BYTES) as u64
}

/// Scans, deduplicates and encodes every name of `texts`, checking the index cap and reserving
/// the memory of each step.
fn encode_entries(
    texts: &[&[u8]],
    opts: &IndexOptions,
    memory: &BuildMemory,
) -> Result<Encoded, IndexError> {
    let threads = opts.threads.max(1);
    // Per thread: release granularity plus huge-page rounding of the buffers it fills.
    let lag = (4 * threads * RELEASE_BYTES) as u64;
    let chunks = chunk_lists(texts, threads);
    let lines: usize = chunks.iter().map(|c| c.3).sum();
    memory.reserve("records", estimate_build_bytes(lines, threads))?;
    let (parts, invalid) = scan_lists(texts, &chunks, opts);
    drop(chunks);
    memory.reserve("scatter", lag)?;
    let (mut records, bucket_sizes) = scatter(parts, threads);

    // Sort and dedupe the key buckets in parallel, each thread with its own set table, releasing
    // the records of every bucket once it is sorted.
    let largest = bucket_sizes.iter().max().copied().unwrap_or(0);
    memory.reserve(
        "dedupe",
        lag + (threads * largest * size_of::<Record>()) as u64,
    )?;
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
        let start = group.first().map_or(0, |b| b.as_ptr() as usize);
        let mut sets = SetTable::new(texts.len());
        let mut out = huge_vec(group.iter().map(|b| b.len()).sum());
        let (mut sorted, mut hashes) = (Vec::new(), Vec::new());
        for bucket in group.iter_mut() {
            sort_bucket(bucket, &mut sorted);
            let end = bucket.as_mut_ptr() as usize + size_of_val(&**bucket);
            // SAFETY: the records of this group up to the end of this bucket are copied into
            // `sorted` or deduplicated; this thread owns them, never reads them again, and they
            // are integers.
            unsafe { release(start as *const u8, end - start) };
            dedupe_bucket(texts, &sorted, &mut sets, &mut out, &mut hashes);
        }
        (sets, out)
    });
    drop(groups);
    drop(records);
    // Intern every thread's sets (counts complete them) and renumber them.
    let mut sets = SetTable::new(texts.len());
    let (locals, parts): (Vec<SetTable>, Vec<Vec<Unique>>) = deduped.into_iter().unzip();
    let to_global: Vec<Vec<u32>> = locals
        .iter()
        .map(|local| {
            (0..local.len() as u32)
                .map(|id| match id {
                    0 => 0,
                    _ => sets.intern(local.get(id), local.counts[id as usize]),
                })
                .collect()
        })
        .collect();
    drop(locals);
    let renumbered = sets.renumber();

    // Final set ids and the entry octets of every part, in parallel.
    let uniques: usize = parts.iter().map(Vec::len).sum();
    let mut starts = Vec::with_capacity(parts.len());
    parts.iter().fold(0, |at, part| {
        starts.push(at);
        at + part.len()
    });
    let parts: Vec<parking_lot::Mutex<Vec<Unique>>> =
        parts.into_iter().map(parking_lot::Mutex::new).collect();
    let totals = run_parallel(parts.len(), threads, |i| {
        let mut part = parts[i].lock();
        let (mut single_mask, mut heavy_keys) = ([0u64; 4], Vec::new());
        let (mut bytes, mut long_bytes, mut names) = (0usize, 0usize, 0u64);
        for u in part.iter_mut() {
            u.set = renumbered[to_global[i][u.set as usize] as usize];
            let symbols = usize::from(u.len) + 1;
            // The entry and its fingerprint octet.
            let size = entry_len(symbols, u.set) + 1;
            bytes += size;
            if symbols > LONG_SYMBOLS {
                long_bytes += (7 * symbols).div_ceil(8);
            }
            names += u64::from(u.set != 0);
            // A single-label name's placement key is its own hash.
            if u.flags & SINGLE != 0 {
                single_mask[(u.key >> 6) as usize & 3] |= 1 << (u.key & 63);
            }
            if u.flags & MARKER != 0 {
                heavy_keys.push(u.key);
            }
        }
        (bytes, long_bytes, names, single_mask, heavy_keys)
    });
    drop(to_global);
    let (mut entry_starts, mut long_starts) = (Vec::new(), Vec::new());
    let (mut sum, mut long_bytes, mut names) = (0usize, 0usize, 0u64);
    let mut single_mask = [0u64; 4];
    let mut heavy_keys = Vec::new();
    for (bytes, long, n, mask, heavy) in totals {
        entry_starts.push(sum);
        long_starts.push(long_bytes);
        (sum, long_bytes, names) = (sum + bytes, long_bytes + long, names + n);
        for (m, w) in single_mask.iter_mut().zip(mask) {
            *m |= w;
        }
        heavy_keys.extend(heavy);
    }
    let heavy = heavy_table(&heavy_keys);
    drop(heavy_keys);
    let block_count = ((sum as f64 / (CAPACITY as f64 * opts.fill)).ceil() as usize).max(1);
    let needed =
        ((block_count + 1) * BLOCK + long_bytes + 8 + 8 * sets.bits.len() + 2 * BLOCK) as u64;
    if needed > opts.max_bytes || block_count > u32::MAX as usize {
        return Err(IndexError::OverCap {
            needed,
            cap: opts.max_bytes,
        });
    }

    // Encode every unique with its placement key and size, part by part in parallel into disjoint
    // ranges, releasing each part's pages as it is consumed.
    let parts_bytes = uniques * size_of::<Unique>();
    memory.reserve(
        "encode",
        lag + (sum + long_bytes + 9 * uniques).saturating_sub(parts_bytes) as u64,
    )?;
    let mut entries: Vec<u8> = huge_vec(sum);
    let mut long = vec![0u8; long_bytes + 8];
    let mut keys: Vec<u64> = huge_vec(uniques);
    let mut sizes: Vec<u8> = huge_vec(uniques);
    let (ed, ld) = (
        SharedMut(entries.as_mut_ptr()),
        SharedMut(long.as_mut_ptr()),
    );
    let (kd, sd) = (SharedMut(keys.as_mut_ptr()), SharedMut(sizes.as_mut_ptr()));
    let step = RELEASE_BYTES / 2 / size_of::<Unique>();
    run_parallel(parts.len(), threads, |i| {
        let mut part = std::mem::take(&mut *parts[i].lock());
        let (mut at, mut long_at) = (entry_starts[i], long_starts[i]);
        let mut entry = [0u8; 72];
        let mut packer = names::Packer::new();
        for k in 0..part.len() {
            if let Some(ahead) = part.get(k + 8) {
                prefetch_read(
                    texts[usize::from(ahead.list)]
                        .as_ptr()
                        .wrapping_add(ahead.offset as usize),
                );
            }
            let u = part[k];
            let text = text_at(texts, u.list, u.offset, u.len);
            let symbols = u.len + 1;
            let marker = u.flags & MARKER != 0;
            let n = if usize::from(symbols) > LONG_SYMBOLS {
                entry[0] = if marker { LONG_MARKER } else { LONG };
                let mut pos = 1 + names::write_varint(&mut entry[1..], u.set);
                entry[pos] = symbols;
                pos += 1;
                entry[pos..pos + 4].copy_from_slice(&(long_at as u32).to_le_bytes());
                names::pack_text(text, &mut packer);
                let packed = packer.bytes();
                assert_eq!(packed.len(), (7 * usize::from(symbols)).div_ceil(8));
                // SAFETY: part i owns long octets long_starts[i]..long_starts[i + 1], counted
                // with the same length; inside `long`.
                unsafe {
                    std::ptr::copy_nonoverlapping(packed.as_ptr(), ld.at(long_at), packed.len())
                };
                long_at += packed.len();
                pos + 4
            } else {
                entry[0] = symbols | if marker { MARKER } else { 0 };
                let pos = 1 + names::write_varint(&mut entry[1..], u.set);
                pos + pack_text_into(text, &mut entry[pos..])
            };
            assert_eq!(n, entry_len(usize::from(symbols), u.set), "entry size");
            // SAFETY: part i owns entry octets entry_starts[i]..entry_starts[i + 1], whose sizes
            // were counted per unique exactly as asserted, and indexes starts[i]..starts[i] + len
            // of the keys and sizes; all inside their capacity.
            unsafe {
                ed.at(at).write(entry_fingerprint(u.key, symbols));
                std::ptr::copy_nonoverlapping(entry.as_ptr(), ed.at(at + 1), n);
                kd.at(starts[i] + k).write(u.key);
                sd.at(starts[i] + k).write((n + 1) as u8);
            }
            at += n + 1;
            if (k + 1) % step == 0 {
                // SAFETY: uniques 0..=k of the part this thread owns are encoded and never read
                // again; they are integers.
                unsafe { release(part.as_mut_ptr().cast(), (k + 1) * size_of::<Unique>()) };
            }
        }
    });
    // SAFETY: the parts wrote every octet below `sum` and every index below `uniques` exactly once.
    unsafe {
        entries.set_len(sum);
        keys.set_len(uniques);
        sizes.set_len(uniques);
    }
    drop(parts);
    Ok(Encoded {
        entries,
        keys,
        sizes,
        long,
        sets,
        single_mask,
        heavy,
        invalid,
        names,
        block_count,
        needed,
    })
}

impl FilterIndex {
    /// Builds the index; never allocates blocks for an index above `opts.max_bytes`.
    pub fn build(lists: &[ListInput<'_>], opts: &IndexOptions) -> Result<FilterIndex, IndexError> {
        let mut arena = TextArena::new();
        for l in lists {
            arena.push(l.text);
        }
        let metas = lists
            .iter()
            .map(|l| ListMeta {
                id: l.id.into(),
                category: l.category.into(),
                category_slot: l.category_slot,
                kind: l.kind,
                invalid_lines: 0,
            })
            .collect();
        Self::build_in(arena, metas, opts, &BuildMemory::unlimited())
    }

    /// [`FilterIndex::build`] over the texts of `arena` (text `i` is list `i` of `lists`), freed as
    /// soon as every name is encoded; `memory` is checked before every build step.
    pub fn build_in(
        arena: TextArena,
        mut lists: Vec<ListMeta>,
        opts: &IndexOptions,
        memory: &BuildMemory,
    ) -> Result<FilterIndex, IndexError> {
        let started = Instant::now();
        if lists.len() > MAX_LISTS {
            return Err(IndexError::TooManyLists(lists.len()));
        }
        assert_eq!(arena.len(), lists.len(), "one text per list");
        let encoded = {
            let texts: Vec<&[u8]> = (0..arena.len()).map(|i| arena.text(i)).collect();
            encode_entries(&texts, opts, memory)?
        };
        drop(arena);
        let Encoded {
            entries,
            keys,
            sizes,
            long,
            sets,
            single_mask,
            heavy,
            invalid,
            names,
            block_count,
            needed,
        } = encoded;
        let threads = opts.threads.max(1);
        let uniques = keys.len();
        memory.reserve("placement", (12 * uniques + 8 * block_count) as u64)?;

        // Largest entries first (key order within a size) leaves the least unusable slack.
        let mut size_starts = [0usize; 256];
        for &size in &sizes {
            size_starts[usize::from(size)] += 1;
        }
        let mut at = 0;
        for start in size_starts.iter_mut().rev() {
            (*start, at) = (at, at + *start);
        }
        let mut order: Vec<u32> = huge_vec(uniques);
        order.resize(uniques, 0);
        for (i, &size) in sizes.iter().enumerate() {
            order[size_starts[usize::from(size)]] = i as u32;
            size_starts[usize::from(size)] += 1;
        }
        let mut slots = vec![Slot::EMPTY; block_count];
        let mut next = huge_vec(uniques);
        next.resize(uniques, u32::MAX);
        let (mut chosen, overflow) = place(&order, &keys, &sizes, &mut slots, &mut next);
        drop(order);
        let mut overflow = relocate(&keys, &sizes, &mut chosen, &mut slots, &mut next, overflow);
        drop((slots, next));
        overflow.sort_unstable();
        // Offsets of the overflow names' entries, and their tag bits in both candidate blocks.
        let mut stashed = Vec::with_capacity(overflow.len());
        let mut tags = vec![0u8; block_count];
        let (mut at, mut from) = (0usize, 0usize);
        for &i in &overflow {
            let i = i as usize;
            at += sizes[from..i]
                .iter()
                .map(|&s| usize::from(s))
                .sum::<usize>();
            from = i;
            stashed.push((at, sizes[i]));
            let (b1, b2) = names::candidates(keys[i], block_count as u32);
            let tag = 1 << (entries[at] & 7);
            tags[b1 as usize] |= tag;
            tags[b2 as usize] |= tag;
        }
        let (stash_count, stash_chosen) = place_stash(&keys, &sizes, &overflow)?;
        drop(keys);
        // Independent metadata Arcs add two refcounts and a vector pointer per list.
        let identity_overhead = (3 * size_of::<usize>() * lists.len()) as u64;
        let needed = needed - 2 * BLOCK as u64
            + u64::from(stash_count + 1) * BLOCK as u64
            + identity_overhead;
        if needed > opts.max_bytes {
            return Err(IndexError::OverCap {
                needed,
                cap: opts.max_bytes,
            });
        }

        memory.reserve(
            "blocks",
            ((block_count + stash_count as usize + 2) * BLOCK) as u64,
        )?;
        let blocks = write_blocks(
            &entries,
            block_count,
            &chosen,
            Items::All(&sizes),
            &tags,
            threads,
        );
        drop((chosen, sizes, tags));
        let stash = write_blocks(
            &entries,
            stash_count as usize,
            &stash_chosen,
            Items::Listed(&stashed),
            &[],
            threads,
        );
        drop(entries);

        for (meta, bad) in lists.iter_mut().zip(&invalid) {
            meta.invalid_lines = *bad;
        }
        let by_id = lists
            .iter()
            .enumerate()
            .map(|(i, m)| (m.id.clone(), i as u16))
            .collect();
        let memory_bytes = (blocks.len()
            + stash.len()
            + long.len()
            + 8 * sets.bits.len()
            + 4 * sets.counts.len()
            + 64 * lists.len()) as u64
            + identity_overhead;
        memory.reserve("list identities", identity_overhead)?;
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
            lists: lists.into_iter().map(Arc::new).collect(),
            by_id,
            entries: names,
            invalid_lines: invalid.iter().sum(),
            stash_entries: overflow.len() as u64,
            memory_bytes,
            build_seconds: started.elapsed().as_secs_f64(),
            generation: BUILDS.fetch_add(1, Ordering::Relaxed) + 1,
            view_ids: Default::default(),
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
            view_ids: Default::default(),
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

    pub fn lists(&self) -> &[Arc<ListMeta>] {
        &self.lists
    }

    pub fn list_index(&self, id: &str) -> Option<u16> {
        self.by_id.get(id).copied()
    }

    /// Up to `n` listed names as lowercase wire names, one per sampled primary block (blocks
    /// stepped evenly), skipping long names and heavy-suffix markers. Off the query path.
    pub fn sample_names(&self, n: usize) -> Vec<Box<[u8]>> {
        let mut out = Vec::with_capacity(n);
        if self.entries == 0 || n == 0 {
            return out;
        }
        let step = (self.block_count as usize / n).max(1);
        for b in (0..self.block_count).step_by(step) {
            if out.len() == n {
                break;
            }
            let block = self.blocks.as_slice();
            let base = b as usize * BLOCK;
            let count = usize::from(block[base] & COUNT_MASK);
            let mut pos = base + HEADER + count;
            for _ in 0..count {
                let head = block[pos];
                let (set, set_len) = names::read_varint(&block[pos + 1..]);
                if head >= LONG_MARKER {
                    pos += 1 + set_len + 5;
                    continue;
                }
                let symbols = head & !MARKER;
                let packed = pos + 1 + set_len;
                if set != 0 {
                    out.push(names::unpack_wire(&block[packed..], symbols));
                    break;
                }
                pos = packed + (7 * usize::from(symbols)).div_ceil(8);
            }
        }
        out
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
        let cache_owner = {
            let mut ids = self.view_ids.lock();
            let next = ids.len();
            let key: Box<[u64]> = block_mask.iter().chain(&allow_mask).copied().collect();
            let id = *ids
                .entry(key)
                .or_insert(next.min(usize::from(u16::MAX)) as u16);
            // Generation 0 (the empty index), generations past 48 bits and views past 65,535
            // distinct masks are decided without the cache.
            if self.generation == 0 || self.generation >> 48 != 0 || id == u16::MAX {
                0
            } else {
                self.generation << 16 | u64::from(id)
            }
        };
        let mut class = vec![0u8; self.set_count];
        let mut first_list = vec![u16::MAX; self.set_count];
        let mut first_allow = vec![u16::MAX; self.set_count];
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
                    if first_allow[id] == u16::MAX {
                        first_allow[id] = (w * 64) as u16 + (s & a).trailing_zeros() as u16;
                    }
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
            cache_owner,
            memory_bytes: 13 * self.set_count as u64,
            class: class.into_boxed_slice(),
            first_list: first_list.into_boxed_slice(),
            first_allow: first_allow.into_boxed_slice(),
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

    /// Calls `visit` with the set id and wire offset of every listed suffix of a lowercase wire
    /// name, longest first, until it returns true. Allocation-free. Only the one- and two-label suffixes are
    /// hashed (and every level under a heavy suffix); the blocks they select are prefetched as soon
    /// as their hash is known.
    #[inline]
    fn for_each_match(&self, name_wire: &[u8], mut visit: impl FnMut(u32, u8) -> bool) {
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
            // Wire names are at most 255 octets, so every offset fits a u8.
            if visit(
                unsafe { sets[level].assume_init() },
                levels.start(level) as u8,
            ) {
                return;
            }
        }
    }
}

/// One policy's verdicts over a shared [`FilterIndex`]: per list set, allow/block class, the first
/// matching block and allow lists and category slot bits.
pub struct FilterView {
    index: Arc<FilterIndex>,
    /// Decision cache key part: `generation << 16 | view id`, 0 = not cached.
    cache_owner: u64,
    class: Box<[u8]>,
    first_list: Box<[u16]>,
    first_allow: Box<[u16]>,
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
        self.index.for_each_match(name_wire, |set, offset| {
            let class = self.class[set as usize];
            if class & 2 != 0 {
                decision = FilterDecision::Allowed(ListHit {
                    list: self.first_allow[set as usize],
                    set,
                    offset,
                });
                return true;
            }
            if class & 1 != 0 && decision == FilterDecision::None {
                decision = FilterDecision::Blocked(ListHit {
                    list: self.first_list[set as usize],
                    set,
                    offset,
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

    /// The decision cache key of this view: index generation and view id (0: never cached).
    #[inline]
    pub fn cache_owner(&self) -> u64 {
        self.cache_owner
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
            let cache = crate::filter::decisions::DecisionCache::new(16);
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
                    let decided = view.decide(&wire);
                    assert_eq!(
                        cache.decide(&view, &wire),
                        decided,
                        "cached {q} (round {round})"
                    );
                    match (oracle.decide(&wire), decided) {
                        (Decision::None, FilterDecision::None)
                        | (Decision::Allowed, FilterDecision::Allowed(_)) => {}
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
