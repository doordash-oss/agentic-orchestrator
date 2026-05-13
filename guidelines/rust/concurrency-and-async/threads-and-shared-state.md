# Threads and Shared State

## std::thread Basics

Rust threads are OS threads — each gets its own stack and is scheduled by the OS:

```rust
use std::thread;

let handle = thread::spawn(|| {
    println!("hello from a thread");
    42
});

let result = handle.join().unwrap();  // blocks until thread finishes
assert_eq!(result, 42);
```

Data moved into threads must be `'static + Send`:

```rust
let data = vec![1, 2, 3];
thread::spawn(move || {
    // data is moved into the thread
    println!("{data:?}");
});
// data is no longer available here
```

## Send and Sync

| Trait | Meaning |
|-------|---------|
| `Send` | Value can be transferred to another thread |
| `Sync` | Value can be referenced from multiple threads (`&T` is `Send`) |

Most types are `Send + Sync` automatically. Notable exceptions:
- `Rc<T>` is neither `Send` nor `Sync` — use `Arc<T>` instead
- `Cell<T>`, `RefCell<T>` are `Send` but not `Sync`
- Raw pointers are neither — must manually impl if safe

## Arc\<Mutex\<T\>\> — Shared Mutable State

The canonical pattern for shared mutable state across threads:

```rust
use std::sync::{Arc, Mutex};

let counter = Arc::new(Mutex::new(0));
let mut handles = vec![];

for _ in 0..10 {
    let counter = Arc::clone(&counter);
    handles.push(thread::spawn(move || {
        let mut num = counter.lock().unwrap();
        *num += 1;
    }));
}

for handle in handles {
    handle.join().unwrap();
}

assert_eq!(*counter.lock().unwrap(), 10);
```

### Mutex Best Practices

- **Hold locks for the minimum time** — don't do I/O or computation while holding a lock
- **Use scoping to auto-drop locks**:
```rust
{
    let mut data = shared.lock().unwrap();
    data.push(item);
}  // lock released here
// do other work without the lock
```
- **Avoid nested locks** — if you must, always acquire in the same order to prevent deadlocks

### Mutex Poisoning

If a thread panics while holding a lock, the mutex becomes "poisoned":

```rust
match mutex.lock() {
    Ok(guard) => { /* use guard */ }
    Err(poisoned) => {
        // Decide: recover or propagate
        let guard = poisoned.into_inner();  // recover
    }
}
```

## RwLock — Read-Heavy Workloads

Multiple readers or one writer:

```rust
use std::sync::RwLock;

let config = Arc::new(RwLock::new(Config::default()));

// Multiple threads can read simultaneously
let cfg = config.read().unwrap();

// Only one thread can write (blocks readers)
let mut cfg = config.write().unwrap();
cfg.reload();
```

Use `RwLock` when reads vastly outnumber writes. Otherwise, `Mutex` is simpler
and has less overhead.

## Atomic Types

Lock-free operations for simple values:

```rust
use std::sync::atomic::{AtomicUsize, Ordering};

static COUNTER: AtomicUsize = AtomicUsize::new(0);

fn increment() {
    COUNTER.fetch_add(1, Ordering::Relaxed);
}

fn get() -> usize {
    COUNTER.load(Ordering::Relaxed)
}
```

**Ordering guidelines**:
- `Relaxed` — no ordering guarantees, just atomicity (counters, flags)
- `Acquire`/`Release` — synchronize memory between threads (common for flags)
- `SeqCst` — strongest guarantee, rarely needed, highest cost

## Rayon for CPU-Bound Parallelism

Data parallelism with zero configuration:

```rust
use rayon::prelude::*;

// Parallel iterator — splits work across threads
let sum: i64 = (0..1_000_000)
    .into_par_iter()
    .map(|x| x * x)
    .sum();

// Parallel sort
let mut data = vec![5, 3, 1, 4, 2];
data.par_sort();

// Parallel processing of a collection
let results: Vec<_> = inputs
    .par_iter()
    .map(|input| expensive_computation(input))
    .collect();
```

**When to use Rayon vs async**:
- **Rayon**: CPU-bound work (computation, data processing, sorting)
- **Async (Tokio)**: I/O-bound work (network, file I/O, database queries)

## Scoped Threads (Rust 1.63+)

Borrow data from the parent stack without `move` or `Arc`:

```rust
let mut data = vec![1, 2, 3];

thread::scope(|s| {
    s.spawn(|| {
        println!("{data:?}");  // borrows data — no move needed
    });
    s.spawn(|| {
        println!("length: {}", data.len());
    });
});
// All scoped threads joined here — safe to use data again
```

Scoped threads are guaranteed to finish before the scope exits —
the compiler can verify borrow lifetimes.
