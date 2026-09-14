//! Per-client-query work limits: outgoing queries, delegation depth and dependency cycles, so one
//! client query cannot make the resolver amplify work against authoritative servers.

use super::trace::Phase;
use hickory_proto::rr::{Name, RecordType};
use std::cell::{Cell, RefCell};

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Limit {
    UpstreamQueries,
    DelegationDepth,
    CnameDepth,
}

pub struct WorkBudget {
    queries: Cell<u32>,
    max_queries: u32,
    max_depth: u32,
    phase: Phase,
    /// Zone cuts the client's question resolved through, not yet taken by `take_cuts`.
    cuts: RefCell<Vec<Name>>,
}

impl WorkBudget {
    pub fn new(max_queries: u32, max_depth: u32) -> Self {
        Self {
            queries: Cell::new(0),
            max_queries,
            max_depth,
            phase: Phase::Resolve,
            cuts: RefCell::new(Vec::new()),
        }
    }

    /// Notes a zone cut the client's question resolves through (ignored on a chain-of-trust
    /// budget), so its chain of trust can be fetched meanwhile.
    pub fn note_cut(&self, zone: &Name) {
        if self.phase == Phase::Resolve {
            let mut cuts = self.cuts.borrow_mut();
            if !cuts.contains(zone) {
                cuts.push(zone.clone());
            }
        }
    }

    pub fn take_cuts(&self) -> Vec<Name> {
        std::mem::take(&mut self.cuts.borrow_mut())
    }

    /// A budget for chain-of-trust lookups (only the trace tells the phases apart).
    pub fn for_validation(mut self) -> Self {
        self.phase = Phase::Validate;
        self
    }

    pub fn phase(&self) -> Phase {
        self.phase
    }

    /// Counts one outgoing query; errors once more than `max_queries` were spent.
    pub fn spend_query(&self) -> Result<(), Limit> {
        let used = self.queries.get().saturating_add(1);
        self.queries.set(used);
        if used > self.max_queries {
            return Err(Limit::UpstreamQueries);
        }
        Ok(())
    }

    /// Counts one optional outgoing query (a race) only while the limit allows it.
    pub fn try_spend_query(&self) -> bool {
        let used = self.queries.get();
        if used >= self.max_queries {
            return false;
        }
        self.queries.set(used + 1);
        true
    }

    pub fn check_depth(&self, depth: u32) -> Result<(), Limit> {
        if depth > self.max_depth {
            return Err(Limit::DelegationDepth);
        }
        Ok(())
    }

    pub fn queries_used(&self) -> u32 {
        self.queries.get()
    }
}

/// The nameserver-address lookups one resolution is nested in, innermost first. Kept per call
/// chain rather than per budget: concurrent lookups of the same name for one client query are
/// not a cycle, a lookup that depends on itself is.
#[derive(Clone, Copy, Default)]
pub struct Dependencies<'a> {
    innermost: Option<&'a Dependency<'a>>,
}

pub struct Dependency<'a> {
    /// Lowercase.
    name: Name,
    rtype: RecordType,
    outer: Dependencies<'a>,
}

impl<'a> Dependencies<'a> {
    pub fn is_empty(&self) -> bool {
        self.innermost.is_none()
    }

    pub fn contains(&self, name: &Name, rtype: RecordType) -> bool {
        let mut next = self.innermost;
        while let Some(d) = next {
            if d.rtype == rtype && d.name == *name {
                return true;
            }
            next = d.outer.innermost;
        }
        false
    }

    /// The lookup of `name`/`rtype` nested in this chain; `None` when the chain already holds it
    /// (a dependency cycle).
    pub fn enter(self, name: &Name, rtype: RecordType) -> Option<Dependency<'a>> {
        let name = name.to_lowercase();
        (!self.contains(&name, rtype)).then_some(Dependency {
            name,
            rtype,
            outer: self,
        })
    }
}

impl Dependency<'_> {
    pub fn rtype(&self) -> RecordType {
        self.rtype
    }

    /// The chain with this lookup innermost.
    pub fn chain(&self) -> Dependencies<'_> {
        Dependencies {
            innermost: Some(self),
        }
    }
}
