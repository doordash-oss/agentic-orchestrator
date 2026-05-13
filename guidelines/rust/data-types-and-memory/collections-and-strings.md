# Collections and Strings

## Choosing the Right Collection

| Collection | Lookup | Insert | Ordered | Use Case |
|-----------|--------|--------|---------|----------|
| `Vec<T>` | O(n) / O(1) index | O(1) amortized push | Yes (insertion order) | Default sequence |
| `HashMap<K, V>` | O(1) average | O(1) average | No | Key-value lookup |
| `BTreeMap<K, V>` | O(log n) | O(log n) | Yes (sorted by key) | Sorted key-value |
| `HashSet<T>` | O(1) average | O(1) average | No | Unique elements |
| `BTreeSet<T>` | O(log n) | O(log n) | Yes (sorted) | Sorted unique elements |
| `VecDeque<T>` | O(1) front/back | O(1) front/back | Yes | Double-ended queue |
| `BinaryHeap<T>` | O(1) max | O(log n) | No | Priority queue |

## Vec Best Practices

### Pre-allocate When Size Is Known

```rust
// Bad: reallocates as it grows
let mut v = Vec::new();
for i in 0..1000 {
    v.push(i);
}

// Good: single allocation
let mut v = Vec::with_capacity(1000);
for i in 0..1000 {
    v.push(i);
}

// Best: use collect (handles capacity automatically)
let v: Vec<i32> = (0..1000).collect();
```

### Common Patterns

```rust
// Remove by swap (O(1) but changes order)
v.swap_remove(index);

// Retain only matching elements
v.retain(|x| x > &0);

// Deduplicate (must be sorted first)
v.sort();
v.dedup();

// Split and drain
let tail = v.split_off(5);  // v has first 5, tail has rest
let drained: Vec<_> = v.drain(2..5).collect();
```

## HashMap Best Practices

### The Entry API

Efficient insert-or-update without double lookup:

```rust
use std::collections::HashMap;

let mut counts: HashMap<String, usize> = HashMap::new();

// Bad: two lookups
if counts.contains_key(word) {
    *counts.get_mut(word).unwrap() += 1;
} else {
    counts.insert(word.to_string(), 1);
}

// Good: one lookup with entry API
*counts.entry(word.to_string()).or_insert(0) += 1;

// With default computation
counts.entry(word.to_string()).or_insert_with(|| expensive_default());
```

### Capacity Hints

```rust
let mut map = HashMap::with_capacity(expected_entries);
```

### Custom Hashing

For performance-sensitive code, consider `FxHashMap` (from `rustc-hash`)
or `ahash` — faster but not HashDoS resistant:

```rust
use rustc_hash::FxHashMap;
let mut map: FxHashMap<u64, String> = FxHashMap::default();
```

## String Types

| Type | Owned/Borrowed | Encoding | Use Case |
|------|----------------|----------|----------|
| `String` | Owned | UTF-8 | Mutable string data |
| `&str` | Borrowed | UTF-8 | String references, literals |
| `OsString` / `&OsStr` | Owned/Borrowed | Platform | File paths, env vars |
| `CString` / `&CStr` | Owned/Borrowed | Null-terminated | FFI with C |
| `PathBuf` / `&Path` | Owned/Borrowed | Platform | File system paths |

### String Best Practices

```rust
// Accept &str in function parameters
fn greet(name: &str) -> String {
    format!("Hello, {name}!")
}

// Use String::with_capacity for building strings
let mut s = String::with_capacity(100);
s.push_str("hello");
s.push(' ');
s.push_str("world");

// Prefer format! over manual concatenation
let msg = format!("{name} has {count} items");

// String slicing is by byte offset — can panic on non-ASCII
let s = "héllo";
// &s[0..2] — panics! 'é' is 2 bytes
// Use .chars() for character iteration
```

### Cow\<str\> for Flexible String Handling

```rust
use std::borrow::Cow;

fn normalize(input: &str) -> Cow<'_, str> {
    if input.contains('\t') {
        Cow::Owned(input.replace('\t', "    "))
    } else {
        Cow::Borrowed(input)  // zero-cost when no modification needed
    }
}
```

## Slices

Slices are views into contiguous data:

```rust
fn sum(values: &[i32]) -> i32 {
    values.iter().sum()
}

// Works with arrays, vecs, and other slices
sum(&[1, 2, 3]);
sum(&vec![1, 2, 3]);
sum(&array[1..3]);
```

### Useful Slice Methods

```rust
slice.chunks(3)           // iterate in groups of 3
slice.windows(2)          // sliding window of size 2
slice.split(|x| *x == 0) // split by predicate
slice.binary_search(&val) // O(log n) search (must be sorted)
slice.contains(&val)      // O(n) search
```

## Small Collection Optimizations

For performance-critical paths, consider stack-allocated alternatives:

```rust
// smallvec: stack-allocated for small sizes, heap for large
use smallvec::SmallVec;
let v: SmallVec<[u8; 16]> = SmallVec::new();  // on stack up to 16 elements

// arrayvec: fixed capacity, never allocates
use arrayvec::ArrayVec;
let mut v = ArrayVec::<u8, 32>::new();

// compact_str: inline small strings (up to 24 bytes on 64-bit)
use compact_str::CompactString;
```

Use these only when profiling shows collection allocation is a bottleneck.
