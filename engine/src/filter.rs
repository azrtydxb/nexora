//! Blocklist/allowlist matching on wire-format name suffixes, and block replies.

use crate::edns::ReplyOpt;
use crate::wire::{self, NameKey, QueryView, SynthAnswer};
use rustc_hash::FxHashSet;
use std::io::Read;

const TYPE_A: u16 = 1;
const TYPE_AAAA: u16 = 28;
const MAX_TEXT_LEN: usize = 253;
const MAX_LABEL_LEN: usize = 63;
/// Largest decompressed blob accepted; bounds memory against a zstd bomb.
const MAX_BLOB_BYTES: u64 = 512 << 20;

/// Discriminants equal the proto `BlockMode` values.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum BlockMode {
    NullIp = 1,
    NxDomain = 2,
    Refused = 3,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum FilterDecision {
    None,
    Blocked,
    Allowed,
}

#[derive(Debug, Default, PartialEq, Eq)]
pub struct ListStats {
    pub entries: usize,
    pub invalid_lines: usize,
}

pub struct FilterSet {
    pub mode: BlockMode,
    pub ttl: u32,
    blocked: FxHashSet<Box<[u8]>>,
    allowed: FxHashSet<Box<[u8]>>,
}

impl FilterSet {
    pub fn empty() -> FilterSet {
        FilterSet {
            mode: BlockMode::NullIp,
            ttl: 0,
            blocked: FxHashSet::default(),
            allowed: FxHashSet::default(),
        }
    }

    /// Builds from decompressed lists of one domain per line.
    pub fn build(
        blocklists: &[Vec<u8>],
        allowlists: &[Vec<u8>],
        mode: BlockMode,
        ttl: u32,
    ) -> (FilterSet, ListStats) {
        let mut stats = ListStats::default();
        let mut load = |lists: &[Vec<u8>], set: &mut FxHashSet<Box<[u8]>>| {
            for line in lists.iter().flat_map(|l| l.split(|&b| b == b'\n')) {
                if line.is_empty() {
                    continue;
                }
                match domain_to_wire(line) {
                    Some(name) => {
                        set.insert(name);
                    }
                    None => stats.invalid_lines += 1,
                }
            }
        };
        let mut set = FilterSet {
            mode,
            ttl,
            ..FilterSet::empty()
        };
        load(blocklists, &mut set.blocked);
        load(allowlists, &mut set.allowed);
        stats.entries = set.blocked.len() + set.allowed.len();
        (set, stats)
    }

    /// Walks the label suffixes of a lowercase wire name, longest first; the
    /// first allowlisted suffix wins, then the first blocklisted one.
    pub fn decide(&self, name_wire: &[u8]) -> FilterDecision {
        if self.blocked.is_empty() && self.allowed.is_empty() {
            return FilterDecision::None;
        }
        let mut pos = 0;
        let mut decision = FilterDecision::None;
        while let Some(&len) = name_wire.get(pos) {
            if len == 0 {
                break;
            }
            let suffix = &name_wire[pos..];
            if self.allowed.contains(suffix) {
                return FilterDecision::Allowed;
            }
            if decision == FilterDecision::None && self.blocked.contains(suffix) {
                if self.allowed.is_empty() {
                    return FilterDecision::Blocked;
                }
                // A shorter allowlisted suffix still beats this block.
                decision = FilterDecision::Blocked;
            }
            pos += 1 + usize::from(len);
        }
        decision
    }

    /// True when any CNAME target is blocked.
    pub fn cloaked(&self, cname_targets: &[NameKey]) -> bool {
        cname_targets
            .iter()
            .any(|t| self.decide(t.as_wire()) == FilterDecision::Blocked)
    }

    /// Writes the configured block reply; returns 0 when `out` is too small.
    pub fn write_block_reply(
        &self,
        q: &QueryView<'_>,
        out: &mut [u8],
        opt: Option<&ReplyOpt>,
    ) -> usize {
        match self.mode {
            BlockMode::NullIp => {
                let answer = match q.qtype {
                    TYPE_A => Some(SynthAnswer::A([0; 4])),
                    TYPE_AAAA => Some(SynthAnswer::Aaaa([0; 16])),
                    _ => None,
                };
                wire::write_synth_reply(q, wire::RCODE_NOERROR, answer, self.ttl, out, opt)
            }
            BlockMode::NxDomain => wire::write_rcode_reply(q, wire::RCODE_NXDOMAIN, out, opt),
            BlockMode::Refused => wire::write_rcode_reply(q, wire::RCODE_REFUSED, out, opt),
        }
    }
}

/// Decompresses a blob, refusing output larger than 512 MiB.
pub fn decode_blob(zstd_bytes: &[u8]) -> std::io::Result<Vec<u8>> {
    let mut out = Vec::new();
    zstd::stream::read::Decoder::new(zstd_bytes)?
        .take(MAX_BLOB_BYTES + 1)
        .read_to_end(&mut out)?;
    if out.len() as u64 > MAX_BLOB_BYTES {
        return Err(std::io::Error::new(
            std::io::ErrorKind::InvalidData,
            "blob decompresses past 512 MiB",
        ));
    }
    Ok(out)
}

/// The lowercase uncompressed wire name (with root byte) of a text domain made
/// of `[a-z0-9-_.]`; `None` for anything else.
pub fn domain_to_wire(domain: &[u8]) -> Option<Box<[u8]>> {
    if domain.is_empty() || domain.len() > MAX_TEXT_LEN {
        return None;
    }
    let mut out = Vec::with_capacity(domain.len() + 2);
    for label in domain.split(|&b| b == b'.') {
        let valid = !label.is_empty()
            && label.len() <= MAX_LABEL_LEN
            && label[0] != b'-'
            && label[label.len() - 1] != b'-'
            && label
                .iter()
                .all(|b| b.is_ascii_alphanumeric() || *b == b'-' || *b == b'_');
        if !valid {
            return None;
        }
        out.push(label.len() as u8);
        out.extend(label.iter().map(u8::to_ascii_lowercase));
    }
    out.push(0);
    Some(out.into_boxed_slice())
}
