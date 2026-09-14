//! Per-worker pool of stream query, answer and frame buffers. Every worker thread runs its own
//! single-threaded runtime, so a thread-local pool is a per-worker pool without locks.

use std::cell::RefCell;

pub const POOL_MAX: usize = 256;
/// A 65,535-octet message plus its 2-octet length prefix.
pub const BUFFER_CAPACITY: usize = 65_537;

thread_local! {
    static POOL: RefCell<Vec<Vec<u8>>> = const { RefCell::new(Vec::new()) };
}

/// An empty buffer with `BUFFER_CAPACITY` octets of capacity.
pub fn take() -> Vec<u8> {
    POOL.with(|p| p.borrow_mut().pop())
        .unwrap_or_else(|| Vec::with_capacity(BUFFER_CAPACITY))
}

/// Returns a buffer; kept only when it still has the pool's capacity and the pool has room.
pub fn give(mut b: Vec<u8>) {
    if !(BUFFER_CAPACITY - 2..=BUFFER_CAPACITY).contains(&b.capacity()) {
        return;
    }
    b.clear();
    POOL.with(|p| {
        let mut p = p.borrow_mut();
        if p.len() < POOL_MAX {
            p.push(b);
        }
    });
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn pool_reuses_bounded_and_rejects_grown_buffers() {
        let b = take();
        let ptr = b.as_ptr();
        assert_eq!(b.capacity(), BUFFER_CAPACITY);
        give(b);
        let again = take();
        assert_eq!(again.as_ptr(), ptr, "a returned buffer is reused");
        drop(again);

        let mut grown = take();
        grown.reserve(BUFFER_CAPACITY + 1);
        give(grown);
        give(Vec::with_capacity(512));
        POOL.with(|p| assert!(p.borrow().is_empty(), "foreign capacities are not kept"));

        for _ in 0..POOL_MAX + 10 {
            give(Vec::with_capacity(BUFFER_CAPACITY));
        }
        POOL.with(|p| assert_eq!(p.borrow().len(), POOL_MAX));
    }
}
