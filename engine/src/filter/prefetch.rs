//! A cache prefetch hint that compiles on stable Rust: `_mm_prefetch` on x86_64 (stable; its
//! `#[target_feature(enable = "sse")]` still needs an `unsafe` call although SSE is baseline),
//! `PRFM` through `asm!` on aarch64 (`_prefetch` and `core::hint::prefetch_read` are unstable in
//! 1.97), nothing elsewhere.

#[inline(always)]
pub fn prefetch_read(p: *const u8) {
    #[cfg(target_arch = "x86_64")]
    // SAFETY: SSE is a baseline x86_64 feature, and a prefetch hint never faults, even for an
    // unmapped address.
    unsafe {
        core::arch::x86_64::_mm_prefetch::<{ core::arch::x86_64::_MM_HINT_T0 }>(p.cast());
    }
    #[cfg(target_arch = "aarch64")]
    // SAFETY: PRFM only hints the data cache: it never faults, even for an unmapped address, and
    // changes no register, flag or memory.
    unsafe {
        core::arch::asm!("prfm pldl1keep, [{p}]", p = in(reg) p, options(nostack, readonly, preserves_flags));
    }
    #[cfg(not(any(target_arch = "x86_64", target_arch = "aarch64")))]
    let _ = p;
}
