# Smart Pointers

## Choosing the Right Pointer

| Type | Ownership | Thread-safe | Use Case |
|------|-----------|-------------|----------|
| `Box<T>` | Single owner | Yes (`Send`/`Sync` if `T` is) | Heap allocation, recursive types, trait objects |
| `Rc<T>` | Shared (reference counted) | **No** | Single-threaded shared ownership |
| `Arc<T>` | Shared (atomic ref counted) | Yes | Multi-threaded shared ownership |
| `Cow<'a, B>` | Borrowed or owned | Depends on `B` | Deferred cloning, optimization |

## Box\<T\>

Heap-allocates a value with single ownership. Use for:

- **Recursive types** (the compiler needs a known size):
```rust
enum List {
    Cons(i32, Box<List>),
    Nil,
}
```

- **Trait objects** (dynamic dispatch):
```rust
fn make_formatter(json: bool) -> Box<dyn Formatter> {
    if json {
        Box::new(JsonFormatter)
    } else {
        Box::new(TextFormatter)
    }
}
```

- **Large values** you want to move without copying:
```rust
let large = Box::new([0u8; 1_000_000]);  // stack-safe
```

## Rc\<T\> — Single-Threaded Shared Ownership

Multiple owners, single thread only. Compiles out to a simple reference count
(no atomic operations — faster but not `Send`):

```rust
use std::rc::Rc;

let shared = Rc::new(vec![1, 2, 3]);
let clone1 = Rc::clone(&shared);  // bumps ref count, no deep copy
let clone2 = Rc::clone(&shared);

assert_eq!(Rc::strong_count(&shared), 3);
```

**Use `Rc::clone(&x)`** not `x.clone()` — the former makes it clear you're
bumping the reference count, not deep-copying.

## Arc\<T\> — Thread-Safe Shared Ownership

Like `Rc` but uses atomic operations for the reference count:

```rust
use std::sync::Arc;

let data = Arc::new(vec![1, 2, 3]);
let data_clone = Arc::clone(&data);

std::thread::spawn(move || {
    println!("{:?}", data_clone);
});
```

**Arc alone gives read-only access.** For mutation, combine with a lock:
- `Arc<Mutex<T>>` — exclusive access
- `Arc<RwLock<T>>` — multiple readers or one writer

## Pin\<P\>

Guarantees a value won't be moved in memory. Required for self-referential
types and async futures:

```rust
use std::pin::Pin;

// Most common use: async trait returns
fn process(&self) -> Pin<Box<dyn Future<Output = Result<()>> + Send + '_>> {
    Box::pin(async move {
        // ...
        Ok(())
    })
}
```

In practice, you rarely construct `Pin` manually — `Box::pin()` and
`tokio::pin!()` handle most cases. The main rule: **don't move pinned data**.

## Weak References

Both `Rc` and `Arc` support weak references that don't prevent deallocation:

```rust
use std::sync::{Arc, Weak};

struct Node {
    parent: Weak<Node>,    // won't create cycles
    children: Vec<Arc<Node>>,
}
```

Use `Weak` to break reference cycles that would otherwise leak memory.
Call `weak.upgrade()` to get an `Option<Arc<T>>`.

## Drop and RAII

The `Drop` trait runs cleanup code when a value goes out of scope — Rust's
version of RAII (Resource Acquisition Is Initialization):

```rust
struct TempFile {
    path: PathBuf,
}

impl Drop for TempFile {
    fn drop(&mut self) {
        let _ = std::fs::remove_file(&self.path);
    }
}
```

**Rules**:
- You cannot call `drop()` explicitly on a value — use `std::mem::drop(value)`
  to drop early
- `Drop` and `Copy` are mutually exclusive — a type cannot implement both
- Destructors must never panic — this causes a double-panic abort

## Decision Tree

```
Do you need heap allocation?
├─ No → use stack values
├─ Yes, single owner → Box<T>
├─ Yes, shared ownership, single thread → Rc<T>
├─ Yes, shared ownership, multi thread → Arc<T>
└─ Yes, shared + mutable →
    ├─ Single thread → Rc<RefCell<T>>
    └─ Multi thread → Arc<Mutex<T>> or Arc<RwLock<T>>
```
