# Interior Mutability

## What It Is

Interior mutability lets you mutate data even when there are immutable references
to it, by moving borrow checking from compile time to runtime. Use it as a
last resort — prefer compile-time borrow checking when possible.

## Cell\<T\> — Copy Types Only

`Cell` provides zero-cost interior mutability for `Copy` types:

```rust
use std::cell::Cell;

struct Counter {
    count: Cell<u32>,
}

impl Counter {
    fn increment(&self) {  // note: &self, not &mut self
        self.count.set(self.count.get() + 1);
    }
}
```

**Constraints**: `Cell` is `!Sync` (single-threaded only), and `T` must be
`Copy` for `get()`. You never get a reference to the inner value — only
get/set/replace.

## RefCell\<T\> — Runtime Borrow Checking

`RefCell` provides runtime-checked borrowing for non-`Copy` types:

```rust
use std::cell::RefCell;

let data = RefCell::new(vec![1, 2, 3]);

// Runtime borrow check — panics if rules violated
{
    let mut borrow = data.borrow_mut();
    borrow.push(4);
}  // mutable borrow dropped

let borrow = data.borrow();  // ok: no active mutable borrow
println!("{:?}", *borrow);
```

**Use `try_borrow()` / `try_borrow_mut()`** if a panic is unacceptable —
they return `Result` instead:

```rust
match data.try_borrow_mut() {
    Ok(mut borrow) => borrow.push(5),
    Err(_) => eprintln!("already borrowed"),
}
```

**Rules** (enforced at runtime, panic on violation):
- Multiple `borrow()` calls — ok
- Single `borrow_mut()` — ok
- `borrow()` + `borrow_mut()` simultaneously — **panics**

## OnceCell and LazyLock

**`std::cell::OnceCell`** — single-threaded, write-once:
```rust
use std::cell::OnceCell;

let cell = OnceCell::new();
cell.set("hello".to_string()).unwrap();
assert_eq!(cell.get(), Some(&"hello".to_string()));
cell.set("world".to_string()).unwrap_err();  // already set
```

**`std::sync::OnceLock`** — thread-safe, write-once:
```rust
use std::sync::OnceLock;

static CONFIG: OnceLock<Config> = OnceLock::new();

fn get_config() -> &'static Config {
    CONFIG.get_or_init(|| Config::load())
}
```

**`std::sync::LazyLock`** (Rust 1.80+) — thread-safe lazy initialization:
```rust
use std::sync::LazyLock;

static REGEX: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(r"\d+").unwrap()
});
```

## When to Use Each

| Type | Thread-safe | Mutable | Use Case |
|------|-------------|---------|----------|
| `Cell<T>` | No | set/get/replace | Simple counters, flags |
| `RefCell<T>` | No | Full borrow | Mock internals, graph nodes |
| `OnceCell<T>` | No | Write-once | Lazy init (single thread) |
| `OnceLock<T>` | Yes | Write-once | Lazy init (global/static) |
| `LazyLock<T>` | Yes | Write-once | Static values with init logic |
| `Mutex<T>` | Yes | Full lock | Shared mutable state (multi-thread) |
| `RwLock<T>` | Yes | Read/write lock | Read-heavy shared state |

## Common Pattern: Rc\<RefCell\<T\>\>

Single-threaded shared mutable state:

```rust
use std::cell::RefCell;
use std::rc::Rc;

struct Node {
    value: i32,
    children: Vec<Rc<RefCell<Node>>>,
}

let node = Rc::new(RefCell::new(Node {
    value: 1,
    children: vec![],
}));

let child = Rc::new(RefCell::new(Node {
    value: 2,
    children: vec![],
}));

node.borrow_mut().children.push(Rc::clone(&child));
```

**Multi-threaded equivalent**: `Arc<Mutex<T>>` or `Arc<RwLock<T>>`.

## Anti-Patterns

**Don't use `RefCell` to work around ownership design issues**:
```rust
// Bad: using RefCell because the design doesn't fit Rust's model
struct God {
    everything: RefCell<HashMap<String, RefCell<Vec<RefCell<Thing>>>>>,
}

// Good: restructure to use clear ownership
struct Registry {
    items: HashMap<String, Vec<Thing>>,
}
```

**Don't use `Cell` for complex types** — it forces `Copy`, which means
no heap data. Use `RefCell` instead.
