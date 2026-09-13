//! The RFC 1034 §4.3.2 name walk with wildcards (RFC 4592) and DNAME (RFC 6672), allocation-free.

use super::name::{KEY_BUF, canon_key_buf, label_offsets};
use super::zone::{NODE_WILDCARD_CHILD, Node, Zone};
use super::{T_DNAME, T_DS};

pub enum Lookup<'z> {
    /// The query name's node (possibly an empty non-terminal).
    Exact(&'z Node),
    /// No node for the name; `wildcard` is `*.<closest_encloser>`.
    Wildcard {
        closest_encloser: &'z Node,
        wildcard: &'z Node,
    },
    NxDomain {
        closest_encloser: &'z Node,
    },
    /// A delegation point at or above the query name.
    Delegation(&'z Node),
    /// A DNAME strictly above the query name.
    Dname(&'z Node),
}

impl Zone {
    /// Walks from the apex towards `qname` (a wire name inside this zone). DS at a delegation
    /// point is answered from this (parent) side.
    pub fn lookup(&self, qname: &[u8], qtype: u16) -> Lookup<'_> {
        let mut offs = [0u16; 128];
        let total = label_offsets(qname, &mut offs);
        let mut key = [0u8; KEY_BUF];
        let mut encloser = self.apex();
        for depth in (self.origin_labels() + 1)..=total {
            let name = &qname[offs[total - depth] as usize..];
            let n = canon_key_buf(name, &mut key);
            let Some(node) = self.node_by_key(&key[..n]) else {
                if encloser.flags & NODE_WILDCARD_CHILD != 0 {
                    let n = canon_key_buf(&encloser.owner, &mut key);
                    if n + 2 <= KEY_BUF {
                        key[n..n + 2].copy_from_slice(b"*\0");
                        if let Some(w) = self.node_by_key(&key[..n + 2]) {
                            return Lookup::Wildcard {
                                closest_encloser: encloser,
                                wildcard: w,
                            };
                        }
                    }
                }
                return Lookup::NxDomain {
                    closest_encloser: encloser,
                };
            };
            let is_qname = depth == total;
            if node.is_cut() && !(is_qname && qtype == T_DS) {
                return Lookup::Delegation(node);
            }
            if !is_qname && node.get(T_DNAME).is_some() {
                return Lookup::Dname(node);
            }
            encloser = node;
        }
        Lookup::Exact(encloser)
    }
}
