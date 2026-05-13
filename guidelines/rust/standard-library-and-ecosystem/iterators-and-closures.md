# Iterators and Closures

## Iterator Chains

Prefer iterator chains over manual loops — they're zero-cost abstractions
that the compiler optimizes to the same code as hand-written loops:

```rust
// Manual loop
let mut results = Vec::new();
for item in &items {
    if item.is_active() {
        results.push(item.name().to_uppercase());
    }
}

// Iterator chain — same performance, more expressive
let results: Vec<String> = items.iter()
    .filter(|item| item.is_active())
    .map(|item| item.name().to_uppercase())
    .collect();
```

## Essential Iterator Methods

```rust
// Transforming
iter.map(|x| x * 2)          // transform each element
iter.flat_map(|x| x.children()) // transform and flatten
iter.filter(|x| x > &0)     // keep matching elements
iter.filter_map(|x| x.parse().ok()) // filter + map in one step

// Consuming
iter.collect::<Vec<_>>()     // gather into collection
iter.count()                 // count elements
iter.sum::<i32>()            // sum elements
iter.any(|x| x > 10)        // short-circuit check
iter.all(|x| x > 0)         // all match
iter.find(|x| x > &10)      // first match
iter.position(|x| x > 10)   // index of first match
iter.min() / iter.max()      // extremes

// Combining
iter.zip(other)              // pair up two iterators
iter.chain(other)            // concatenate two iterators
iter.enumerate()             // add indices: (0, elem), (1, elem)...
iter.take(5)                 // first 5 elements
iter.skip(3)                 // skip first 3
iter.peekable()              // look ahead without consuming

// Folding
iter.fold(0, |acc, x| acc + x)     // reduce with initial value
iter.reduce(|a, b| a.max(b))       // reduce without initial value
iter.scan(0, |state, x| { ... })   // stateful transformation
```

## Three Iterator Types

```rust
let v = vec![1, 2, 3];

v.iter()       // &T — borrows elements
v.iter_mut()   // &mut T — mutable borrows
v.into_iter()  // T — consumes the collection

// for loops use into_iter by default
for item in &v { }      // equivalent to v.iter()
for item in &mut v { }  // equivalent to v.iter_mut()
for item in v { }        // equivalent to v.into_iter()
```

## Custom Iterators

Implement `Iterator` for lazy, composable sequences:

```rust
struct Fibonacci {
    a: u64,
    b: u64,
}

impl Fibonacci {
    fn new() -> Self {
        Fibonacci { a: 0, b: 1 }
    }
}

impl Iterator for Fibonacci {
    type Item = u64;

    fn next(&mut self) -> Option<u64> {
        let next = self.a;
        self.a = self.b;
        self.b = next + self.b;
        Some(next)
    }
}

// Usage
let first_10: Vec<u64> = Fibonacci::new().take(10).collect();
```

## Closure Traits

| Trait | Captures | Can Call Multiple Times | Use Case |
|-------|----------|------------------------|----------|
| `Fn` | `&self` (immutable borrow) | Yes | Most callbacks, map/filter |
| `FnMut` | `&mut self` (mutable borrow) | Yes | Accumulating state |
| `FnOnce` | `self` (takes ownership) | Once | Consuming closures, thread spawning |

```rust
// Fn: borrows immutably
let name = String::from("Alice");
let greet = || println!("Hello, {name}!");
greet();
greet();  // can call multiple times

// FnMut: borrows mutably
let mut count = 0;
let mut increment = || { count += 1; count };
assert_eq!(increment(), 1);
assert_eq!(increment(), 2);

// FnOnce: takes ownership
let name = String::from("Alice");
let consume = move || {
    drop(name);  // consumes name
};
consume();
// consume(); — error: moved
```

### move Closures

Force a closure to take ownership of captured variables:

```rust
let name = String::from("Alice");
let closure = move || println!("{name}");
// name is moved into the closure — no longer available here
```

Required for closures passed to `thread::spawn` or `tokio::spawn`.

## Accepting Closures as Parameters

```rust
// Accept the most general form (FnOnce unless you need to call multiple times)
fn apply<F: FnOnce() -> R, R>(f: F) -> R {
    f()
}

// Or with impl Trait for simpler signatures
fn for_each(items: &[i32], f: impl Fn(i32)) {
    for &item in items {
        f(item);
    }
}
```

### Returning Closures

```rust
fn make_adder(n: i32) -> impl Fn(i32) -> i32 {
    move |x| x + n
}

let add_5 = make_adder(5);
assert_eq!(add_5(3), 8);
```

## Common Patterns

### Collecting into HashMap

```rust
let map: HashMap<String, i32> = items.iter()
    .map(|item| (item.name.clone(), item.value))
    .collect();
```

### Partitioning

```rust
let (evens, odds): (Vec<_>, Vec<_>) = numbers.iter()
    .partition(|&&x| x % 2 == 0);
```

### Flattening Nested Results

```rust
// Collect Vec<Result<T, E>> into Result<Vec<T>, E>
let results: Result<Vec<_>, _> = items.iter()
    .map(|item| item.parse())
    .collect();
```
