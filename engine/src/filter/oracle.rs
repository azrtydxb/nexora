//! The v1 matcher, kept only as the oracle for filter_index_matches_filterset_semantics.

use super::{BlockMode, domain_to_wire};
use rustc_hash::FxHashSet;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Decision {
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
    blocked: FxHashSet<Box<[u8]>>,
    allowed: FxHashSet<Box<[u8]>>,
}

impl FilterSet {
    pub fn empty() -> FilterSet {
        FilterSet {
            blocked: FxHashSet::default(),
            allowed: FxHashSet::default(),
        }
    }

    /// Builds from decompressed lists of one domain per line.
    pub fn build(
        blocklists: &[Vec<u8>],
        allowlists: &[Vec<u8>],
        _mode: BlockMode,
        _ttl: u32,
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
        let mut set = FilterSet::empty();
        load(blocklists, &mut set.blocked);
        load(allowlists, &mut set.allowed);
        stats.entries = set.blocked.len() + set.allowed.len();
        (set, stats)
    }

    /// Walks the label suffixes of a lowercase wire name, longest first; the
    /// first allowlisted suffix wins, then the first blocklisted one.
    pub fn decide(&self, name_wire: &[u8]) -> Decision {
        if self.blocked.is_empty() && self.allowed.is_empty() {
            return Decision::None;
        }
        let mut pos = 0;
        let mut decision = Decision::None;
        while let Some(&len) = name_wire.get(pos) {
            if len == 0 {
                break;
            }
            let suffix = &name_wire[pos..];
            if self.allowed.contains(suffix) {
                return Decision::Allowed;
            }
            if decision == Decision::None && self.blocked.contains(suffix) {
                if self.allowed.is_empty() {
                    return Decision::Blocked;
                }
                // A shorter allowlisted suffix still beats this block.
                decision = Decision::Blocked;
            }
            pos += 1 + usize::from(len);
        }
        decision
    }

    /// True when exactly this wire name is in a block list.
    pub fn blocks_exactly(&self, name_wire: &[u8]) -> bool {
        self.blocked.contains(name_wire)
    }
}
