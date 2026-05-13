# Lifetimes

## What Lifetimes Are

Lifetimes are the compiler's way of tracking how long references are valid.
Every reference has a lifetime, but most are inferred automatically through
**lifetime elision**.

## Lifetime Elision Rules

The compiler applies three rules to infer lifetimes. If all output lifetimes
are determined after applying these rules, no annotations are needed:

1. **Each reference parameter gets its own lifetime**: `fn f(a: &str, b: &str)` becomes `fn f<'a, 'b>(a: &'a str, b: &'b str)`
2. **If there's exactly one input lifetime, it's assigned to all outputs**: `fn f(a: &str) -> &str` becomes `fn f<'a>(a: &'a str) -> &'a str`
3. **If one parameter is `&self` or `&mut self`, its lifetime is assigned to all outputs**

```rust
// No annotations needed — rule 2 applies
fn first_word(s: &str) -> &str {
    s.split_whitespace().next().unwrap_or("")
}

// No annotations needed — rule 3 applies
impl Config {
    fn name(&self) -> &str {
        &self.name
    }
}

// Annotations needed — two input lifetimes, compiler can't choose
fn longest<'a>(x: &'a str, y: &'a str) -> &'a str {
    if x.len() > y.len() { x } else { y }
}
```

## When Explicit Annotations Are Needed

You need explicit lifetimes when:

1. **Multiple input references, output borrows from one of them**:
```rust
fn first<'a>(x: &'a str, _y: &str) -> &'a str {
    x  // output borrows from x, not y
}
```

2. **Structs that hold references**:
```rust
struct Excerpt<'a> {
    text: &'a str,
}

impl<'a> Excerpt<'a> {
    fn content(&self) -> &str {
        self.text  // rule 3: returns with lifetime of &self
    }
}
```

3. **Trait objects with references**:
```rust
// Must specify lifetime bound
fn make_processor<'a>(data: &'a str) -> Box<dyn Processor + 'a> {
    Box::new(SimpleProcessor { data })
}
```

## The `'static` Lifetime

`'static` means the reference is valid for the entire program duration:

```rust
// String literals are 'static
let s: &'static str = "hello world";

// Owned types satisfy 'static (no borrows to expire)
fn spawn_task(name: String) {
    tokio::spawn(async move {
        println!("{name}");  // String is 'static — no references
    });
}
```

**Common misconception**: `T: 'static` does NOT mean "lives forever" — it
means "contains no non-static references." Owned types like `String`, `Vec<T>`,
`i32` all satisfy `'static`.

```rust
// This is fine — String owns its data, no references
fn needs_static<T: 'static>(val: T) { ... }
needs_static(String::from("hello"));  // compiles
```

## Lifetime Bounds on Generics

```rust
// T must outlive 'a
fn print_ref<'a, T: Display + 'a>(val: &'a T) {
    println!("{val}");
}

// Structs with generic lifetime-bounded fields
struct Wrapper<'a, T: 'a> {
    value: &'a T,
}
```

## Multiple Lifetimes

Use multiple lifetime parameters when inputs have different lifetimes:

```rust
// x and y can have different lifetimes
// Return value tied to x's lifetime
fn select<'a, 'b>(x: &'a str, _y: &'b str) -> &'a str {
    x
}
```

In practice, multiple distinct lifetimes are rare. Most functions use a single
lifetime parameter.

## Lifetime Anti-Patterns

**Don't over-annotate** — let elision work:
```rust
// Bad: unnecessary annotations
fn len<'a>(s: &'a str) -> usize { s.len() }

// Good: elision handles it
fn len(s: &str) -> usize { s.len() }
```

**Don't use `'static` as a workaround** — if the borrow checker complains,
restructure rather than slapping `'static` on everything:
```rust
// Bad: forcing 'static to avoid lifetime thinking
struct Parser {
    input: &'static str,  // probably wrong
}

// Good: parameterize the lifetime
struct Parser<'a> {
    input: &'a str,
}
```
