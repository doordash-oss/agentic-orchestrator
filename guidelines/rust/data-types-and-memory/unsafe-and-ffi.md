# Unsafe Code and FFI

## When unsafe Is Justified

`unsafe` is needed for operations the compiler cannot verify:

1. **Dereferencing raw pointers**
2. **Calling unsafe functions** (including FFI)
3. **Implementing unsafe traits** (`Send`, `Sync`)
4. **Accessing mutable statics**
5. **Accessing fields of unions**

## Minimizing unsafe Surface Area

Encapsulate `unsafe` in safe abstractions:

```rust
// Bad: unsafe scattered throughout the codebase
pub fn process(ptr: *const u8, len: usize) -> &[u8] {
    unsafe { std::slice::from_raw_parts(ptr, len) }
}

// Good: unsafe encapsulated with documented invariants
pub struct Buffer {
    ptr: *mut u8,
    len: usize,
    cap: usize,
}

impl Buffer {
    /// Returns a slice of the buffer's contents.
    ///
    /// # Safety invariants maintained by this type:
    /// - `ptr` is valid for `len` bytes
    /// - `ptr` is properly aligned
    /// - The memory is initialized
    pub fn as_slice(&self) -> &[u8] {
        // SAFETY: Buffer maintains the invariant that ptr is valid
        // for len bytes and properly aligned.
        unsafe { std::slice::from_raw_parts(self.ptr, self.len) }
    }
}
```

### The SAFETY Comment Convention

Every `unsafe` block must have a `// SAFETY:` comment explaining why it's safe:

```rust
// SAFETY: We just checked that index < self.len, so this is in bounds.
unsafe { *self.ptr.add(index) }

// SAFETY: The C library guarantees the returned pointer is valid until
// the next call to lib_free(), and we call lib_free() in our Drop impl.
unsafe { CStr::from_ptr(raw_ptr) }
```

## FFI Basics

### Calling C from Rust

```rust
// Declare external functions
extern "C" {
    fn strlen(s: *const std::ffi::c_char) -> usize;
    fn malloc(size: usize) -> *mut std::ffi::c_void;
    fn free(ptr: *mut std::ffi::c_void);
}

// Wrap in safe Rust API
pub fn c_string_length(s: &CStr) -> usize {
    // SAFETY: CStr guarantees null-termination, which strlen requires.
    unsafe { strlen(s.as_ptr()) }
}
```

### Memory Layout for FFI

Use `#[repr(C)]` to match C struct layout:

```rust
#[repr(C)]
pub struct Point {
    pub x: f64,
    pub y: f64,
}

// repr(transparent) for single-field newtypes
#[repr(transparent)]
pub struct Handle(u64);
```

### Exposing Rust to C

```rust
#[no_mangle]
pub extern "C" fn rust_add(a: i32, b: i32) -> i32 {
    a + b
}

// Opaque types for C consumers
#[no_mangle]
pub extern "C" fn create_config() -> *mut Config {
    Box::into_raw(Box::new(Config::default()))
}

#[no_mangle]
pub unsafe extern "C" fn destroy_config(ptr: *mut Config) {
    if !ptr.is_null() {
        // SAFETY: ptr was created by create_config via Box::into_raw
        drop(Box::from_raw(ptr));
    }
}
```

### String Conversion at FFI Boundaries

```rust
use std::ffi::{CStr, CString};

// Rust → C
let c_string = CString::new("hello").unwrap();
unsafe { c_function(c_string.as_ptr()) };

// C → Rust
unsafe {
    let c_str = CStr::from_ptr(raw_ptr);
    let rust_str = c_str.to_str().unwrap();  // fails if not valid UTF-8
    // or: c_str.to_string_lossy() for lossy conversion
}
```

## Common Unsafe Patterns

### NonNull for Non-Null Pointers

```rust
use std::ptr::NonNull;

struct MyVec<T> {
    ptr: NonNull<T>,  // guarantees non-null, enables niche optimization
    len: usize,
    cap: usize,
}
```

### MaybeUninit for Deferred Initialization

```rust
use std::mem::MaybeUninit;

let mut array: [MaybeUninit<u32>; 100] = unsafe {
    MaybeUninit::uninit().assume_init()
};

for (i, elem) in array.iter_mut().enumerate() {
    elem.write(i as u32);
}

// SAFETY: All elements have been initialized above.
let array = unsafe {
    std::mem::transmute::<_, [u32; 100]>(array)
};
```

## Anti-Patterns

```rust
// Bad: transmute for type conversion
let x: u32 = unsafe { std::mem::transmute(1.0f32) };
// Good: use dedicated methods
let x: u32 = 1.0f32.to_bits();

// Bad: unnecessary unsafe
unsafe { vec.set_len(0) }  // just use vec.clear()

// Bad: unsafe to bypass borrow checker
// If you need unsafe to make it compile, your design is wrong

// Bad: implementing Send/Sync without justification
unsafe impl Send for MyType {}  // MUST prove thread-safety
```

## Testing Unsafe Code

```bash
# Use Miri to detect undefined behavior
cargo +nightly miri test

# Address Sanitizer
RUSTFLAGS="-Z sanitizer=address" cargo +nightly test
```

Write tests that exercise edge cases: empty inputs, maximum sizes,
alignment boundaries, null pointers.
