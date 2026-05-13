# Borrowing and References

## The Borrowing Rules

At any given time, you can have **either**:
- One mutable reference (`&mut T`), **or**
- Any number of immutable references (`&T`)

References must always be valid — the compiler prevents dangling references.

## Accept the Most General Form

Prefer slice/reference types over owned containers in function parameters:

```rust
// Bad: forces callers to have a String/Vec
fn process(data: &String) { ... }
fn sum(values: &Vec<i32>) -> i32 { ... }

// Good: accepts String, &str, string literals, slices, arrays, Vec...
fn process(data: &str) { ... }
fn sum(values: &[i32]) -> i32 { ... }
```

This rule extends to other types:
- `&Path` instead of `&PathBuf`
- `&OsStr` instead of `&OsString`
- `&CStr` instead of `&CString`

**Why**: `&String` auto-derefs to `&str`, but requiring `&String` forces
callers to allocate when they might already have a `&str`.

## Borrow vs Own in Parameters

**Borrow** (`&T`) when:
- The function only reads the data
- The caller needs the value after the call
- The function doesn't need to store the value long-term

**Own** (`T`) when:
- The function needs to store the value (e.g., in a struct field)
- The function consumes or transforms the value
- The value is `Copy` (no cost to pass by value for small types)

```rust
// Borrows: just needs to read
fn contains_keyword(text: &str) -> bool {
    text.contains("rust")
}

// Owns: stores in struct
struct Config {
    name: String,
}

impl Config {
    fn new(name: String) -> Self {
        Config { name }  // takes ownership, stores it
    }
}
```

## Mutable References

Only one `&mut` at a time prevents data races at compile time:

```rust
let mut s = String::from("hello");

let r1 = &s;     // ok: immutable borrow
let r2 = &s;     // ok: multiple immutable borrows
println!("{r1} {r2}");
// r1, r2 no longer used after this point (NLL)

let r3 = &mut s;  // ok: no active immutable borrows
r3.push_str(" world");
```

Non-Lexical Lifetimes (NLL) means borrows end at their last use, not at
scope end — this is why the above compiles.

## Reborrowing

Mutable references can be temporarily "reborrowed" as immutable:

```rust
fn print_and_modify(data: &mut Vec<i32>) {
    println!("{:?}", &*data);  // reborrow as &Vec<i32>
    data.push(42);             // still usable as &mut
}
```

## Copy vs Clone

**`Copy`**: bitwise copy, zero-cost, implicit. For small, stack-only types:
- All integer types, `bool`, `char`, `f32`, `f64`
- Tuples of `Copy` types: `(i32, bool)` is `Copy`
- `&T` is `Copy` (references are just pointers)
- `&mut T` is NOT `Copy` (uniqueness guarantee)

**`Clone`**: explicit deep copy, potentially expensive:
- `String`, `Vec<T>`, `HashMap<K, V>`, etc.
- Derive when needed: `#[derive(Clone)]`

```rust
// Copy — implicit, no-cost
let x: i32 = 42;
let y = x;  // x is still valid

// Clone — explicit, allocates
let s1 = String::from("hello");
let s2 = s1.clone();  // s1 is still valid, new heap allocation
```

**Rule**: if you're adding `.clone()` to satisfy the borrow checker, step
back and restructure. Clone as a borrow-checker workaround is a code smell.

## Cow: Clone on Write

`Cow<'a, B>` defers cloning until mutation is needed:

```rust
use std::borrow::Cow;

fn normalize(input: &str) -> Cow<'_, str> {
    if input.contains(' ') {
        // Allocates only when needed
        Cow::Owned(input.replace(' ', "_"))
    } else {
        // Zero-cost borrow
        Cow::Borrowed(input)
    }
}
```

Use `Cow` when a function *sometimes* needs to allocate and sometimes doesn't.
Common in parsers, string processing, and configuration handling.

## Common Clippy Lints

- **`needless_pass_by_value`**: parameter should be borrowed, not owned
- **`ptr_arg`**: use `&str` instead of `&String`, `&[T]` instead of `&Vec<T>`
- **`clone_on_copy`**: calling `.clone()` on a `Copy` type is unnecessary
- **`redundant_clone`**: value is cloned but the original is never used again
