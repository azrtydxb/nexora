//! Coarse process clock: whole seconds from a ticker thread, read lock-free on the hot path.

use std::sync::atomic::{AtomicU32, Ordering};
use std::sync::{Once, OnceLock};
use std::time::{Duration, Instant};

static START: OnceLock<Instant> = OnceLock::new();
static NOW: AtomicU32 = AtomicU32::new(0);
static TICKER: Once = Once::new();

fn start() -> Instant {
    *START.get_or_init(Instant::now)
}

fn elapsed_secs() -> u32 {
    start().elapsed().as_secs() as u32 + 1
}

/// Seconds since process start, plus one (never 0).
pub fn now_secs() -> u32 {
    match NOW.load(Ordering::Relaxed) {
        0 => elapsed_secs(), // ticker not started (tests only)
        now => now,
    }
}

/// Monotonic microseconds since process start, for stage timestamps.
pub fn now_micros() -> u64 {
    start().elapsed().as_micros() as u64
}

/// Starts the `nexora-clock` thread updating `now_secs` every 100 ms; later calls do nothing.
pub fn start_ticker() {
    TICKER.call_once(|| {
        NOW.store(elapsed_secs(), Ordering::Relaxed);
        std::thread::Builder::new()
            .name("nexora-clock".into())
            .spawn(tick)
            .expect("spawn nexora-clock thread");
    });
}

fn tick() {
    loop {
        std::thread::sleep(Duration::from_millis(100));
        NOW.store(elapsed_secs(), Ordering::Relaxed);
    }
}
