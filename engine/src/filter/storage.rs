//! Zeroed byte storage for filter index blocks: anonymous `mmap` advised for transparent huge pages
//! before first touch on Linux (kw runs THP in `madvise` mode), a 2 MiB-aligned allocation elsewhere.

use std::ptr::NonNull;

pub const BLOCK: usize = 128;

pub struct AlignedBytes {
    ptr: NonNull<u8>,
    len: usize,
}

// SAFETY: the bytes are owned plain data with no interior pointers.
unsafe impl Send for AlignedBytes {}
// SAFETY: shared access is read-only.
unsafe impl Sync for AlignedBytes {}

impl AlignedBytes {
    pub fn zeroed(len: usize) -> AlignedBytes {
        let len = len.max(BLOCK).next_multiple_of(BLOCK);
        #[cfg(target_os = "linux")]
        {
            // SAFETY: a fresh private anonymous mapping; the kernel zero-fills it on first touch.
            let ptr = unsafe {
                libc::mmap(
                    std::ptr::null_mut(),
                    len,
                    libc::PROT_READ | libc::PROT_WRITE,
                    libc::MAP_PRIVATE | libc::MAP_ANONYMOUS,
                    -1,
                    0,
                )
            };
            assert!(
                ptr != libc::MAP_FAILED,
                "filter index mmap of {len} bytes failed"
            );
            // SAFETY: advice on the mapping created above; failure only loses huge pages.
            unsafe { libc::madvise(ptr, len, libc::MADV_HUGEPAGE) };
            AlignedBytes {
                ptr: NonNull::new(ptr.cast()).expect("mmap returned null"),
                len,
            }
        }
        #[cfg(not(target_os = "linux"))]
        {
            let layout = std::alloc::Layout::from_size_align(len, 2 << 20).expect("layout");
            // SAFETY: the layout has a non-zero size.
            let ptr = unsafe { std::alloc::alloc_zeroed(layout) };
            let Some(ptr) = NonNull::new(ptr) else {
                std::alloc::handle_alloc_error(layout)
            };
            AlignedBytes { ptr, len }
        }
    }
    pub fn len(&self) -> usize {
        self.len
    }
    pub fn is_empty(&self) -> bool {
        false
    }
    pub fn as_ptr(&self) -> *const u8 {
        self.ptr.as_ptr()
    }
    pub fn as_slice(&self) -> &[u8] {
        // SAFETY: ptr..ptr+len is one live zero-initialised allocation owned by self.
        unsafe { std::slice::from_raw_parts(self.ptr.as_ptr(), self.len) }
    }
    pub fn as_mut_slice(&mut self) -> &mut [u8] {
        // SAFETY: as above, and &mut self guarantees exclusive access.
        unsafe { std::slice::from_raw_parts_mut(self.ptr.as_ptr(), self.len) }
    }
    #[inline(always)]
    pub fn block(&self, i: u32) -> &[u8; BLOCK] {
        self.as_slice()[i as usize * BLOCK..][..BLOCK]
            .try_into()
            .expect("block size")
    }
}

impl Drop for AlignedBytes {
    fn drop(&mut self) {
        #[cfg(target_os = "linux")]
        // SAFETY: unmaps exactly the mapping created in `zeroed`.
        unsafe {
            libc::munmap(self.ptr.as_ptr().cast(), self.len);
        }
        #[cfg(not(target_os = "linux"))]
        // SAFETY: deallocates with the layout used in `zeroed`.
        unsafe {
            std::alloc::dealloc(
                self.ptr.as_ptr(),
                std::alloc::Layout::from_size_align(self.len, 2 << 20).expect("layout"),
            );
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn aligned_bytes_are_zeroed_writable_and_block_addressable() {
        let mut b = AlignedBytes::zeroed(3 * BLOCK + 5);
        assert_eq!(b.len(), 4 * BLOCK, "rounded up to whole blocks");
        assert!(b.as_slice().iter().all(|&x| x == 0));
        b.as_mut_slice()[2 * BLOCK + 7] = 9;
        assert_eq!(b.block(2)[7], 9);
        crate::filter::prefetch::prefetch_read(b.as_ptr());
        crate::filter::prefetch::prefetch_read(std::ptr::null());
        assert_eq!(AlignedBytes::zeroed(0).len(), BLOCK);
    }
}
