use std::alloc::{GlobalAlloc, Layout, System};
use std::sync::atomic::{AtomicUsize, Ordering};

static USED: AtomicUsize = AtomicUsize::new(0);
static TOTAL: AtomicUsize = AtomicUsize::new(384 << 20);
static SINGLE: AtomicUsize = AtomicUsize::new(64 << 20);

pub struct Bounded;

fn claim(bytes: usize) {
    if bytes > SINGLE.load(Ordering::Relaxed) {
        refuse();
    }
    let maximum = TOTAL.load(Ordering::Relaxed);
    let result = USED.fetch_update(Ordering::AcqRel, Ordering::Relaxed, |current| {
        current.checked_add(bytes).filter(|next| *next <= maximum)
    });
    if result.is_err() {
        refuse();
    }
}

pub fn configure(total: u64, single: u64) {
    TOTAL.store(total as usize, Ordering::Release);
    SINGLE.store(single as usize, Ordering::Release);
    if USED.load(Ordering::Acquire) > total as usize {
        refuse();
    }
}

fn refuse() -> ! {
    crate::response::terminate(b"{\"format\":\"brine-native-reader-v1\",\"outcome\":\"refused\",\"code\":\"allocation_limit\"}\n")
}

unsafe impl GlobalAlloc for Bounded {
    unsafe fn alloc(&self, layout: Layout) -> *mut u8 {
        claim(layout.size());
        unsafe { System.alloc(layout) }
    }

    unsafe fn alloc_zeroed(&self, layout: Layout) -> *mut u8 {
        claim(layout.size());
        unsafe { System.alloc_zeroed(layout) }
    }

    unsafe fn dealloc(&self, ptr: *mut u8, layout: Layout) {
        unsafe { System.dealloc(ptr, layout) };
        USED.fetch_sub(layout.size(), Ordering::AcqRel);
    }

    unsafe fn realloc(&self, ptr: *mut u8, old: Layout, size: usize) -> *mut u8 {
        claim(size);
        let result = unsafe { System.realloc(ptr, old, size) };
        USED.fetch_sub(old.size(), Ordering::AcqRel);
        result
    }
}
