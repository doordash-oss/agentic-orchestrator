# Generics and Bounds

## Static Dispatch vs Dynamic Dispatch

```rust
// Static dispatch (monomorphized) — zero-cost, one copy per type
fn process<T: Display>(item: T) {
    println!("{item}");
}

// Dynamic dispatch (vtable lookup) — one copy, runtime cost
fn process(item: &dyn Display) {
    println!("{item}");
}

// impl Trait in argument position — syntactic sugar for generics
fn process(item: impl Display) {
    println!("{item}");  // equivalent to the generic version
}
```

**Choose generics (static dispatch) when**: performance matters, types are
known at compile time, you want to avoid heap allocation.

**Choose trait objects (dynamic dispatch) when**: you need heterogeneous
collections, the set of types isn't known at compile time, or you want to
reduce code bloat from monomorphization.

## impl Trait

### In Argument Position (Universal)

Syntactic sugar for a generic parameter — caller chooses the type:

```rust
fn log(msg: impl Display) { ... }
// equivalent to:
fn log<T: Display>(msg: T) { ... }
```

### In Return Position (Existential)

The function chooses a single concrete type — callers can't name it:

```rust
fn make_iter() -> impl Iterator<Item = i32> {
    (0..10).filter(|x| x % 2 == 0)
}
```

**Limitation**: all return paths must return the same concrete type:

```rust
// Error: returns two different types
fn make_iter(ascending: bool) -> impl Iterator<Item = i32> {
    if ascending {
        (0..10).into_iter()         // Range<i32>
    } else {
        (0..10).rev().into_iter()   // Rev<Range<i32>>
    }
}

// Fix: use Box<dyn Iterator>
fn make_iter(ascending: bool) -> Box<dyn Iterator<Item = i32>> {
    if ascending {
        Box::new(0..10)
    } else {
        Box::new((0..10).rev())
    }
}
```

## where Clauses

Use `where` for complex bounds to keep signatures readable:

```rust
// Inline: ok for simple bounds
fn process<T: Display + Clone>(item: T) { ... }

// where clause: better for complex bounds
fn merge<K, V, I>(iter: I) -> HashMap<K, V>
where
    K: Eq + Hash + Clone,
    V: Default + Merge,
    I: IntoIterator<Item = (K, V)>,
{
    // ...
}
```

## Multiple Trait Bounds

```rust
// Plus syntax
fn print_and_log<T: Display + Debug>(item: T) { ... }

// where clause for readability
fn process<T>(item: T)
where
    T: Display + Debug + Clone + Send + 'static,
{ ... }
```

## Turbofish Syntax

Explicitly specify generic types when inference fails:

```rust
let numbers: Vec<i32> = "1,2,3".split(',')
    .map(|s| s.parse::<i32>().unwrap())
    .collect();

// Or with turbofish on collect
let numbers = "1,2,3".split(',')
    .map(|s| s.parse::<i32>().unwrap())
    .collect::<Vec<_>>();
```

## Default Type Parameters

```rust
// Default Hasher = RandomState
struct HashMap<K, V, S = RandomState> { ... }

// Callers usually don't specify S
let map: HashMap<String, i32> = HashMap::new();
```

## Phantom Types

Generic parameters used for type-level programming, not at runtime:

```rust
use std::marker::PhantomData;

struct Meters;
struct Seconds;

struct Quantity<Unit> {
    value: f64,
    _unit: PhantomData<Unit>,
}

impl<Unit> Quantity<Unit> {
    fn new(value: f64) -> Self {
        Quantity { value, _unit: PhantomData }
    }
}

// Type system prevents mixing units
let distance = Quantity::<Meters>::new(100.0);
let time = Quantity::<Seconds>::new(9.58);
// distance + time → compile error
```

## Const Generics

Type parameters that are values, not types:

```rust
// Fixed-size array wrapper
struct Matrix<const ROWS: usize, const COLS: usize> {
    data: [[f64; COLS]; ROWS],
}

impl<const ROWS: usize, const COLS: usize> Matrix<ROWS, COLS> {
    fn new() -> Self {
        Matrix { data: [[0.0; COLS]; ROWS] }
    }
}

let m: Matrix<3, 3> = Matrix::new();
```

## Anti-Patterns

```rust
// Bad: over-constrained — does this really need Clone + Debug?
fn process<T: Display + Debug + Clone + Send + Sync>(item: T) {
    println!("{item}");  // only uses Display
}

// Good: minimal bounds
fn process<T: Display>(item: T) {
    println!("{item}");
}

// Bad: trait object when generics would work
fn process(items: &[Box<dyn Display>]) {
    for item in items { println!("{item}"); }
}

// Good (if items are homogeneous): generics
fn process<T: Display>(items: &[T]) {
    for item in items { println!("{item}"); }
}
```
