//! Per-client-query work limits: outgoing queries, delegation depth and dependency cycles, so one
//! client query cannot make the resolver amplify work against authoritative servers.

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
    /// Lowercase (name, type) lookups currently being resolved on this query's behalf.
    in_progress: RefCell<Vec<(Name, RecordType)>>,
}

impl WorkBudget {
    pub fn new(max_queries: u32, max_depth: u32) -> Self {
        Self {
            queries: Cell::new(0),
            max_queries,
            max_depth,
            in_progress: RefCell::new(Vec::new()),
        }
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

    pub fn check_depth(&self, depth: u32) -> Result<(), Limit> {
        if depth > self.max_depth {
            return Err(Limit::DelegationDepth);
        }
        Ok(())
    }

    /// Marks `(name, rtype)` as being resolved; false when it already is (a dependency cycle).
    pub fn enter(&self, name: &Name, rtype: RecordType) -> bool {
        let key = (name.to_lowercase(), rtype);
        let mut stack = self.in_progress.borrow_mut();
        if stack.contains(&key) {
            return false;
        }
        stack.push(key);
        true
    }

    pub fn leave(&self, name: &Name, rtype: RecordType) {
        let key = (name.to_lowercase(), rtype);
        let mut stack = self.in_progress.borrow_mut();
        if let Some(i) = stack.iter().rposition(|k| *k == key) {
            stack.remove(i);
        }
    }

    pub fn queries_used(&self) -> u32 {
        self.queries.get()
    }
}
