//! Suffix hashing and 6-bit symbol packing of DNS names for the filter index.
//!
//! Hashes and symbols are computed eight octets at a time (SWAR): a label of up to 16 octets costs
//! two independent multiplies, validation and lowercasing are a few word operations, and packing is
//! done only when a candidate entry has to be confirmed.

use std::mem::MaybeUninit;

pub const MAX_LEVELS: usize = 127;
pub const MAX_TEXT_LEN: usize = 253;
pub const PACKED_LEN: usize = 232;
pub const SEP: u8 = 0;
pub const INVALID: u8 = 0xFF;
/// The one multiplier of the suffix hash (a single constant keeps the aarch64 hot loop from
/// rematerialising several 64-bit immediates per label).
const K: u64 = 0xA076_1D64_78BD_642F;
const ONES: u64 = 0x0101_0101_0101_0101;
const HIGH: u64 = ONES << 7;

/// The 7-bit symbol of a list octet: the lowercased octet itself for `[A-Za-z0-9_-]`, INVALID for
/// anything else. Entries are the symbols of their wire form (length octets included, no root),
/// so a query suffix confirms by comparing its own octets, 7 bits each, against the entry: every
/// octet below 0x80 maps to itself, and an octet with the high bit set never matches.
pub static SYMBOL: [u8; 256] = {
    let mut t = [INVALID; 256];
    let mut c = 0;
    while c < 256 {
        let b = c as u8;
        if b.is_ascii_alphanumeric() || b == b'-' || b == b'_' {
            t[c] = b.to_ascii_lowercase();
        }
        c += 1;
    }
    t
};

/// 1 for octets a list line may hold besides dots: `[A-Za-z0-9_-]`.
static TEXT_OCTET: [u8; 256] = {
    let mut t = [0u8; 256];
    let mut c = 0;
    while c < 256 {
        let b = c as u8;
        t[c] = (b.is_ascii_alphanumeric() || b == b'-' || b == b'_') as u8;
        c += 1;
    }
    t
};

/// Per label length: the masks of its first and second 8-octet chunks.
static LABEL_MASKS: [[u64; 2]; 256] = {
    let mut t = [[0u64; 2]; 256];
    let mut n: usize = 1;
    while n < 256 {
        let first = if n >= 8 { 8 } else { n };
        let second = if n >= 16 { 8 } else { n.saturating_sub(8) };
        t[n][0] = u64::MAX >> (64 - 8 * first);
        t[n][1] = if second == 0 {
            0
        } else {
            u64::MAX >> (64 - 8 * second)
        };
        n += 1;
    }
    t
};

#[cfg(test)]
thread_local! {
    /// Test hook: every suffix hash becomes this value (collision tests).
    pub(crate) static FORCED_HASH: std::cell::Cell<Option<u64>> = const { std::cell::Cell::new(None) };
}

#[inline(always)]
fn finish(h: u64) -> u64 {
    #[cfg(test)]
    if let Some(forced) = FORCED_HASH.with(|c| c.get()) {
        return forced;
    }
    h
}

/// The xor of the low and high halves of the 128-bit product.
#[inline(always)]
pub fn fold(a: u64, b: u64) -> u64 {
    let r = u128::from(a) * u128::from(b);
    (r as u64) ^ ((r >> 64) as u64)
}

/// Absorbs a label of `len` octets whose first two zero-padded 8-octet chunks are `w0` and `w1`:
/// two multiplies that both depend only on the parent hash, so they run in parallel. The length
/// selects the first multiplier, which keeps label boundaries unambiguous; the parent hash (seeded)
/// keys both, so list content cannot be tuned to collide.
#[inline(always)]
fn absorb(parent: u64, len: usize, w0: u64, w1: u64) -> u64 {
    fold(parent ^ w0, K ^ ((len as u64) << 8)) ^ fold(parent.rotate_left(32) ^ w1, K)
}

/// Absorbs one lowercase label into the hash of its parent suffix: [`absorb`] over the first 16
/// octets, then one multiply per further zero-padded 8-octet chunk.
#[inline(always)]
pub fn mix_label(parent: u64, label: &[u8]) -> u64 {
    let chunk = |at: usize| load8(label, at) & mask(label.len().saturating_sub(at));
    let mut h = absorb(parent, label.len(), chunk(0), chunk(8));
    let mut at = 16;
    while at < label.len() {
        h = fold(h ^ chunk(at), K);
        at += 8;
    }
    h
}

/// The block directory fingerprint: the top byte of a multiply that mixes every hash bit.
#[inline(always)]
pub fn fingerprint(hash: u64) -> u8 {
    // The low octets of both halves: block candidates depend on the high bits of each half.
    (hash ^ (hash >> 32)) as u8
}

/// The two candidate blocks of a hash among `blocks` (distinct unless there is one block).
#[inline(always)]
pub fn candidates(hash: u64, blocks: u32) -> (u32, u32) {
    let n = u64::from(blocks);
    let b1 = (((hash >> 32) * n) >> 32) as u32;
    let b2 = (((hash & 0xFFFF_FFFF) * n) >> 32) as u32;
    if b2 != b1 {
        (b1, b2)
    } else if b1 + 1 < blocks {
        (b1, b1 + 1)
    } else {
        (b1, 0)
    }
}

/// v1's label rule: 1..=63 octets of [A-Za-z0-9-_], not starting or ending with '-'.
pub fn valid_label(label: &[u8]) -> bool {
    !label.is_empty()
        && label.len() <= 63
        && label[0] != b'-'
        && label[label.len() - 1] != b'-'
        && label
            .iter()
            .all(|b| b.is_ascii_alphanumeric() || *b == b'-' || *b == b'_')
}

/// Little-endian octets `at..at + 8` of `b`, zero-filled past its end.
#[inline(always)]
fn load8(b: &[u8], at: usize) -> u64 {
    let word = |i: usize| u64::from_le_bytes(b[i..i + 8].try_into().expect("8 octets"));
    if at + 8 <= b.len() {
        word(at)
    } else if at >= b.len() {
        0
    } else if b.len() >= 8 {
        // The last eight octets, shifted so octet `at` comes first.
        word(b.len() - 8) >> (8 * (at + 8 - b.len()))
    } else {
        b[at..]
            .iter()
            .rev()
            .fold(0, |w, &c| (w << 8) | u64::from(c))
    }
}

/// The position of the first `c` in `text[from..]`, or `text.len()`.
#[inline]
pub fn find_byte(text: &[u8], from: usize, c: u8) -> usize {
    let mut at = from;
    while at + 8 <= text.len() {
        let hits = eq(load8(text, at), c);
        if hits != 0 {
            return at + hits.trailing_zeros() as usize / 8;
        }
        at += 8;
    }
    text[at.min(text.len())..]
        .iter()
        .position(|&b| b == c)
        .map_or(text.len(), |p| at + p)
}

/// The mask of the first `octets` octets of a word (all of them from 8 on).
#[inline(always)]
fn mask(octets: usize) -> u64 {
    u64::MAX
        .checked_shr(64 - 8 * octets.min(8) as u32)
        .unwrap_or(0)
}

/// 0x80 in every octet of `w` that is equal to `c`.
#[inline(always)]
fn eq(w: u64, c: u8) -> u64 {
    let x = w ^ (ONES * u64::from(c));
    !(((x & !HIGH) + !HIGH) | x) & HIGH
}

/// 0x80 in every octet of `w` (all below 0x80) that is at least `c` (below 0x80).
#[inline(always)]
fn at_least(w: u64, c: u8) -> u64 {
    (w + ONES * u64::from(0x80 - c)) & HIGH
}

/// 0x80 in every octet of `w` that is in `[a-z0-9_-]` (lowercase letters only).
#[inline(always)]
fn listable(w: u64) -> u64 {
    let seven = w & !HIGH;
    let lower = at_least(seven, b'a') & !at_least(seven, b'z' + 1);
    let digit = at_least(seven, b'0') & !at_least(seven, b'9' + 1);
    (lower | digit | eq(w, b'-') | eq(w, b'_')) & !w & HIGH
}

/// 0x80 in every octet of `w` that is in `[A-Z]`.
#[inline(always)]
fn upper(w: u64) -> u64 {
    let seven = w & !HIGH;
    at_least(seven, b'A') & !at_least(seven, b'Z' + 1) & !w & HIGH
}

/// `w` with `[A-Z]` lowercased.
#[inline(always)]
fn lowercase(w: u64) -> u64 {
    w | (upper(w) >> 2)
}

pub struct Packer {
    buf: [u8; PACKED_LEN],
    bits: usize,
}

impl Default for Packer {
    fn default() -> Packer {
        Packer::new()
    }
}

impl Packer {
    pub fn new() -> Packer {
        Packer {
            buf: [0; PACKED_LEN],
            bits: 0,
        }
    }
    pub fn clear(&mut self) {
        self.buf[..self.bits.div_ceil(8)].fill(0);
        self.bits = 0;
    }
    #[inline(always)]
    pub fn push(&mut self, symbol: u8) {
        let byte = self.bits >> 3;
        let v = u16::from(symbol) << (self.bits & 7);
        self.buf[byte] |= v as u8;
        self.buf[byte + 1] |= (v >> 8) as u8;
        self.bits += 7;
    }
    pub fn symbols(&self) -> usize {
        self.bits / 7
    }
    pub fn bytes(&self) -> &[u8] {
        &self.buf[..self.bits.div_ceil(8)]
    }
    pub fn buf(&self) -> &[u8; PACKED_LEN] {
        &self.buf
    }
}

/// The hashes of one list line.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct TextHashes {
    /// The name's suffix hash.
    pub hash: u64,
    /// The suffix hash of its last two labels (of the name itself when it has one label).
    pub key: u64,
    pub labels: u8,
}

/// Hashes the text domain `text[start..end]` with v1's validation, lowercasing ASCII letters.
/// Reads up to 7 octets past a label inside `text` (never past its end).
#[inline]
pub fn text_hashes_in(seed: u64, text: &[u8], start: usize, end: usize) -> Option<TextHashes> {
    let len = end - start;
    if len == 0 || len > MAX_TEXT_LEN {
        return None;
    }
    // Label ends (exclusive) left to right, found and validated eight octets at a time.
    let mut ends = [0u8; MAX_LEVELS];
    let mut labels = 0;
    let mut at = 0;
    while at < len {
        let m = mask(len - at);
        let w = load8(text, start + at) & m;
        let dots = eq(w, b'.') & m;
        // Table lookups per octet: cheaper here than range tests with wide constants.
        let octets = w.to_le_bytes();
        let valid = (0..8).fold(0u64, |v, i| {
            v | (u64::from(TEXT_OCTET[usize::from(octets[i])]) << (8 * i + 7))
        });
        if (dots | valid) & m != HIGH & m {
            return None;
        }
        let mut d = dots;
        while d != 0 {
            if labels == MAX_LEVELS - 1 {
                return None;
            }
            ends[labels] = (at + d.trailing_zeros() as usize / 8) as u8;
            labels += 1;
            d &= d - 1;
        }
        at += 8;
    }
    ends[labels] = len as u8;
    labels += 1;
    let mut h = seed;
    let mut key = 0;
    let mut label_end = len;
    for i in (0..labels).rev() {
        let label_start = if i == 0 {
            0
        } else {
            usize::from(ends[i - 1]) + 1
        };
        let n = label_end.wrapping_sub(label_start);
        if n == 0 || n > 63 {
            return None;
        }
        let first = text[start + label_start];
        let last = text[start + label_end - 1];
        if first == b'-' || last == b'-' {
            return None;
        }
        let chunk =
            |c: usize| lowercase(load8(text, start + label_start + c) & mask(n.saturating_sub(c)));
        let mut lh = absorb(h, n, chunk(0), chunk(8));
        let mut c = 16;
        while c < n {
            lh = fold(lh ^ chunk(c), K);
            c += 8;
        }
        h = finish(lh);
        if i + 2 == labels || labels == 1 {
            key = h;
        }
        label_end = label_start.wrapping_sub(1);
    }
    Some(TextHashes {
        hash: h,
        key,
        labels: labels as u8,
    })
}

pub fn text_hash(seed: u64, domain: &[u8]) -> Option<u64> {
    text_hashes_in(seed, domain, 0, domain.len()).map(|t| t.hash)
}

pub fn text_name(seed: u64, domain: &[u8], packer: &mut Packer) -> Option<u64> {
    let hash = text_hash(seed, domain)?;
    pack_text(domain, packer);
    Some(hash)
}

/// Packs the wire form of an already validated text domain (build side): each label's length,
/// then its lowercased octets, left to right; `packer.symbols()` is the wire length without the
/// root octet.
pub fn pack_text(domain: &[u8], packer: &mut Packer) {
    packer.clear();
    for label in domain.split(|&b| b == b'.') {
        packer.push(label.len() as u8);
        for &c in label {
            packer.push(SYMBOL[usize::from(c)]);
        }
    }
}

/// Little-endian octets `at..at + 8` of `name`, which has at least 8 octets: octets past its end
/// read as zero when `at` is inside `name`, and as unspecified values otherwise (callers mask
/// such words away entirely).
#[inline(always)]
fn word(name: &[u8], at: usize) -> u64 {
    let o = at.min(name.len() - 8);
    u64::from_le_bytes(name[o..o + 8].try_into().expect("8 octets"))
        .wrapping_shr((8 * (at - o)) as u32)
}

/// Packs the wire form of a validated text domain (as [`pack_text`]) eight octets at a time into
/// `out`; returns the packed length in octets.
pub fn pack_wire_text(text: &[u8], out: &mut [u8; PACKED_LEN]) -> usize {
    let n = text.len();
    let mut wire = [0u8; MAX_TEXT_LEN + 1 + 16];
    wire[1..1 + n].copy_from_slice(text);
    let mut length_at = 0;
    let mut dot = find_byte(text, 0, b'.');
    while dot < n {
        wire[length_at] = (dot - length_at) as u8;
        length_at = dot + 1;
        dot = find_byte(text, dot + 1, b'.');
    }
    wire[length_at] = (n - length_at) as u8;
    let symbols = n + 1;
    let mut at = 0;
    while at < symbols {
        let w = u64::from_le_bytes(wire[at..at + 8].try_into().expect("8 octets"));
        let packed = pack7(lowercase(w) & mask(symbols - at)).to_le_bytes();
        out[at / 8 * 7..at / 8 * 7 + 8].copy_from_slice(&packed);
        at += 8;
    }
    (7 * symbols).div_ceil(8)
}

/// True when the `symbols` octets of `name` from `start` are the packed `entry`, which must have
/// at least 8 octets after its packed symbols. Exact: symbols are the octets themselves, and an
/// octet with the high bit set (which 7 bits cannot hold) never matches.
#[inline(always)]
pub fn suffix_matches(entry: &[u8], name: &[u8], start: usize, symbols: u8) -> bool {
    let n = usize::from(symbols);
    let mut at = 0;
    while at < n {
        let rest = n - at;
        let w = word(name, start + at) & mask(rest);
        // Eight symbols are 56 bits, so chunk k of the entry starts at octet 7k.
        let stored = u64::from_le_bytes(
            entry[at / 8 * 7..at / 8 * 7 + 8]
                .try_into()
                .expect("8 octets"),
        );
        if w & HIGH != 0 || (pack7(w) ^ stored) & (u64::MAX >> (64 - 7 * rest.min(8))) != 0 {
            return false;
        }
        at += 8;
    }
    true
}

/// True when every octet of the label at wire offset `start` of `name` (at least 8 octets) is
/// `[a-z0-9_-]`.
#[inline(always)]
pub fn label_listable(name: &[u8], start: usize) -> bool {
    let n = usize::from(name[start]);
    let mut at = 0;
    while at < n {
        let m = mask(n - at);
        if (listable(word(name, start + 1 + at) & m) | !m) & HIGH != HIGH {
            return false;
        }
        at += 8;
    }
    true
}

pub fn write_varint(out: &mut [u8], mut v: u32) -> usize {
    let mut i = 0;
    loop {
        let b = (v & 0x7F) as u8;
        v >>= 7;
        if v == 0 {
            out[i] = b;
            return i + 1;
        }
        out[i] = b | 0x80;
        i += 1;
    }
}

pub fn read_varint(bytes: &[u8]) -> (u32, usize) {
    let mut v = 0u32;
    for (i, &b) in bytes.iter().take(5).enumerate() {
        v |= u32::from(b & 0x7F) << (7 * i);
        if b & 0x80 == 0 {
            return (v, i + 1);
        }
    }
    (v, 5)
}

pub fn varint_len(v: u32) -> usize {
    match v {
        0..=0x7F => 1,
        0x80..=0x3FFF => 2,
        0x4000..=0x1F_FFFF => 3,
        0x20_0000..=0xFFF_FFFF => 4,
        _ => 5,
    }
}

/// Per-suffix results of one walk; level 0 is the shortest suffix (the last label).
pub struct Levels {
    hashes: [MaybeUninit<u64>; MAX_LEVELS],
    /// Wire offsets of the labels, left to right.
    starts: [MaybeUninit<u8>; MAX_LEVELS],
    labels: usize,
    root: usize,
    count: usize,
}

impl Default for Levels {
    fn default() -> Levels {
        Levels::new()
    }
}

impl Levels {
    #[inline(always)]
    pub fn new() -> Levels {
        Levels {
            hashes: [MaybeUninit::uninit(); MAX_LEVELS],
            starts: [MaybeUninit::uninit(); MAX_LEVELS],
            labels: 0,
            root: 0,
            count: 0,
        }
    }
    pub fn count(&self) -> usize {
        self.count
    }
    #[inline(always)]
    pub fn hash(&self, level: usize) -> u64 {
        assert!(level < self.count, "level {level} of {}", self.count);
        // SAFETY: `walk_hashes` initialises hashes[0..count] before raising `count`.
        unsafe { self.hashes[level].assume_init() }
    }
    /// The number of labels, which is the number of levels whether hashed or not.
    pub fn labels(&self) -> usize {
        self.labels
    }
    /// The wire offset of the suffix at `level` (any level below `labels()`).
    #[inline(always)]
    pub fn start(&self, level: usize) -> usize {
        assert!(level < self.labels, "level {level} of {}", self.labels);
        // SAFETY: the forward pass of `walk_hashes` initialises starts[0..labels].
        usize::from(unsafe { self.starts[self.labels - 1 - level].assume_init() })
    }
    /// The wire offset of the root octet.
    #[inline(always)]
    pub fn root(&self) -> usize {
        self.root
    }
    /// The symbol count of the suffix at `level`: its wire length without the root octet.
    #[inline(always)]
    pub fn symbols(&self, level: usize) -> u8 {
        (self.root - self.start(level)) as u8
    }
}

/// Packs 8 octets below 0x80 into their 56-bit little-endian stream of 7-bit symbols.
#[inline(always)]
fn pack7(w: u64) -> u64 {
    let w = (w & 0x007F_007F_007F_007F) | ((w >> 8) & 0x007F_007F_007F_007F) << 7;
    let w = (w & 0x0000_3FFF_0000_3FFF) | ((w >> 16) & 0x0000_3FFF_0000_3FFF) << 14;
    (w & 0x0FFF_FFFF) | (w >> 32) << 28
}

/// Walks the uncompressed wire name held in the first `len` octets of `name` (at least 8 octets,
/// zero after the name) right to left, hashing every suffix, and calls `on_level` with each suffix
/// hash as soon as it is known; hashing stops after a level for which `on_level` returns false
/// (every label start is still recorded). Octets are not validated here: a label outside `[a-z0-9_-]` cannot
/// be listed, which a match can contain.
#[inline(always)]
pub fn walk_hashes(
    seed: u64,
    name: &[u8],
    len: usize,
    out: &mut Levels,
    mut on_level: impl FnMut(u64) -> bool,
) {
    let mut labels = 0;
    let mut p = 0usize;
    loop {
        let n = usize::from(name[p]);
        if n == 0 {
            break;
        }
        let end = p + 1 + n;
        if end >= len || end > MAX_TEXT_LEN + 1 || labels == MAX_LEVELS {
            return;
        }
        out.starts[labels] = MaybeUninit::new(p as u8);
        labels += 1;
        p = end;
    }
    out.labels = labels;
    out.root = p;
    let mut h = seed;
    for level in 0..labels {
        // SAFETY: initialised by the forward pass above.
        let s = usize::from(unsafe { out.starts[labels - 1 - level].assume_init() });
        let n = usize::from(name[s]);
        let [m0, m1] = LABEL_MASKS[n];
        let mut lh = absorb(h, n, word(name, s + 1) & m0, word(name, s + 9) & m1);
        let mut at = 16;
        while at < n {
            lh = fold(lh ^ (word(name, s + 1 + at) & mask(n - at)), K);
            at += 8;
        }
        h = finish(lh);
        out.hashes[level] = MaybeUninit::new(h);
        out.count = level + 1;
        if !on_level(h) {
            return;
        }
    }
}

/// `name_wire` zero-padded to at least 8 octets.
fn padded(name_wire: &[u8]) -> Vec<u8> {
    let mut v = name_wire.to_vec();
    v.resize(name_wire.len().max(8), 0);
    v
}

/// [`walk_hashes`] of a wire name, keeping only the levels whose labels are all `[a-z0-9_-]`.
pub fn walk(seed: u64, name_wire: &[u8], out: &mut Levels, mut on_level: impl FnMut(u64)) {
    let name = padded(name_wire);
    walk_hashes(seed, &name, name_wire.len(), out, |h| {
        on_level(h);
        true
    });
    let mut valid = 0;
    while valid < out.count && label_listable(&name, out.start(valid)) {
        valid += 1;
    }
    out.count = valid;
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::filter::domain_to_wire;

    const SEED: u64 = 0x5EED_0000_0000_0001;

    fn levels_of(wire: &[u8]) -> Levels {
        let mut l = Levels::new();
        walk(SEED, wire, &mut l, |_| {});
        l
    }

    #[test]
    fn suffix_hash_is_independent_of_the_prefix() {
        let full = domain_to_wire(b"www.ads.example.test").unwrap();
        let l = levels_of(&full);
        assert_eq!(l.count(), 4);
        for (level, suffix) in [
            "test",
            "example.test",
            "ads.example.test",
            "www.ads.example.test",
        ]
        .iter()
        .enumerate()
        {
            let mut p = Packer::new();
            let h = text_name(SEED, suffix.as_bytes(), &mut p).unwrap();
            assert_eq!(l.hash(level), h, "hash of {suffix}");
            assert_eq!(text_hash(SEED, suffix.as_bytes()), Some(h));
            assert_eq!(usize::from(l.symbols(level)), suffix.len() + 1);
            assert_eq!(p.symbols(), suffix.len() + 1);
            assert!(
                suffix_matches(p.buf(), &full, l.start(level), l.symbols(level)),
                "packing of {suffix}"
            );
        }
        let other = levels_of(&domain_to_wire(b"cdn.tracker.ads.example.test").unwrap());
        assert_eq!(
            other.hash(2),
            l.hash(2),
            "ads.example.test under another prefix"
        );
        assert_ne!(
            other.hash(3),
            l.hash(3),
            "tracker.ads.example.test differs from www.ads.example.test"
        );
    }

    #[test]
    fn text_names_follow_v1_validation_and_lowercase() {
        let lower = text_name(SEED, b"ads.example.test", &mut Packer::new());
        assert!(lower.is_some());
        assert_eq!(
            text_name(SEED, b"Ads.Example.TEST", &mut Packer::new()),
            lower
        );
        for bad in [
            &b""[..],
            b"-bad-.example",
            b"a..b",
            b"a b.test",
            b"a.test.",
            b"\xc3\xa9.test",
            b".test",
        ] {
            assert_eq!(text_name(SEED, bad, &mut Packer::new()), None, "{bad:?}");
            assert!(domain_to_wire(bad).is_none(), "v1 agrees on {bad:?}");
        }
        let long_label = "a".repeat(64);
        assert_eq!(text_hash(SEED, long_label.as_bytes()), None);
        assert!(domain_to_wire(long_label.as_bytes()).is_none());
        assert!(text_hash(SEED, b"x_1.a-b.0").is_some() && domain_to_wire(b"x_1.a-b.0").is_some());
    }

    #[test]
    fn labels_with_octets_outside_the_list_alphabet_end_the_walk() {
        // A label "a.b" (a dot inside a label) and a label with a non-ASCII octet, left of a listable suffix.
        let mut wire = vec![3, b'a', b'.', b'b', 2, 0xC3, 0xA9];
        wire.extend_from_slice(&domain_to_wire(b"ads.example.test").unwrap());
        let l = levels_of(&wire);
        assert_eq!(
            l.count(),
            3,
            "only the listable suffixes right of the odd labels"
        );
        assert_eq!(Some(l.hash(2)), text_hash(SEED, b"ads.example.test"));
        let truncated = [3u8, b'w', b'w', b'w', 4, b't', b'e'];
        assert_eq!(
            levels_of(&truncated).count(),
            0,
            "a name without its root octet yields nothing"
        );
    }

    #[test]
    fn names_at_the_255_octet_limit_hash_and_pack_exactly() {
        let text = format!("{a}.{a}.{a}.{b}", a = "a".repeat(63), b = "b".repeat(61));
        assert_eq!(text.len(), 253);
        let wire = domain_to_wire(text.as_bytes()).unwrap();
        assert_eq!(wire.len(), 255);
        let l = levels_of(&wire);
        assert_eq!((l.count(), l.symbols(3)), (4, 254));
        let mut p = Packer::new();
        assert_eq!(Some(l.hash(3)), text_name(SEED, text.as_bytes(), &mut p));
        assert!(suffix_matches(p.buf(), &wire, 0, 254));
        let mut near = text.clone().into_bytes();
        near[252] = b'c';
        let mut q = Packer::new();
        text_name(SEED, &near, &mut q).unwrap();
        assert!(
            !suffix_matches(q.buf(), &wire, 0, 254),
            "the last packed symbol differs"
        );
    }

    #[test]
    fn candidates_varints_and_forced_hashes() {
        for blocks in [1u32, 2, 3, 1000, 845_000] {
            for i in 0..10_000u64 {
                let h = fold(i ^ 0xDEAD_BEEF, 0x9E37_79B9_7F4A_7C15);
                let (b1, b2) = candidates(h, blocks);
                assert!(b1 < blocks && b2 < blocks);
                assert!(blocks == 1 || b1 != b2);
            }
        }
        let mut v = [0u8; 8];
        for x in [0u32, 1, 127, 128, 300, 16_383, 16_384, u32::MAX] {
            let n = write_varint(&mut v, x);
            assert_eq!((read_varint(&v), varint_len(x)), ((x, n), n));
        }
        FORCED_HASH.with(|c| c.set(Some(42)));
        let forced = text_hash(SEED, b"one.test");
        FORCED_HASH.with(|c| c.set(None));
        assert_eq!(forced, Some(42));
    }
}
