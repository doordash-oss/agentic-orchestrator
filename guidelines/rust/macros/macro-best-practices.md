# Macro Best Practices

## When to Use Macros vs Functions vs Generics

| Need | Solution |
|------|----------|
| Same logic for different types | Generics |
| Implement a trait for a type | Derive macro |
| Variable number of arguments | `macro_rules!` |
| Custom syntax or DSL | Proc macro (function-like) |
| Reduce boilerplate across types | `macro_rules!` or derive macro |
| Code that must run at compile time | Proc macro |

**Default preference order**: functions → generics → `macro_rules!` → proc macros.

```rust
// Don't use a macro when a function works
macro_rules! add {
    ($a:expr, $b:expr) => { $a + $b };  // unnecessary
}

// Just use a function
fn add(a: i32, b: i32) -> i32 { a + b }

// Don't use a macro when generics work
macro_rules! print_debug {
    ($val:expr) => { println!("{:?}", $val); };  // unnecessary
}

// Just use a generic function
fn print_debug(val: &impl std::fmt::Debug) { println!("{val:?}"); }
```

## macro_rules! vs Proc Macros

| | `macro_rules!` | Proc macros |
|--|----------------|-------------|
| Compilation | Fast | Adds compile time |
| Dependencies | None | `syn`, `quote`, `proc-macro2` |
| Crate | Same crate | Separate crate required |
| Debugging | `cargo expand` | `cargo expand` |
| Capabilities | Pattern matching on syntax | Full Rust code generation |
| Hygiene | Partially hygienic | Fully controllable |

**The Rust project's direction** (2025): improving `macro_rules!` to reduce
the need for proc macros. Prefer `macro_rules!` when it can express your needs.

## Keep Macros Simple

```rust
// Bad: complex logic in macro
macro_rules! complex_macro {
    ($($item:expr),*) => {{
        let mut result = Vec::new();
        $(
            let processed = if $item > 0 {
                $item * 2
            } else {
                -$item
            };
            if processed > 10 {
                result.push(processed);
            }
        )*
        result
    }};
}

// Good: macro delegates to a function
macro_rules! process_items {
    ($($item:expr),* $(,)?) => {
        $crate::_process_items(&[$($item),*])
    };
}

// Logic lives in testable function
pub fn _process_items(items: &[i32]) -> Vec<i32> {
    items.iter()
        .map(|&x| if x > 0 { x * 2 } else { -x })
        .filter(|&x| x > 10)
        .collect()
}
```

## Testing Macros

### Testing macro_rules!

Test the expanded output directly:

```rust
#[test]
fn test_hashmap_macro() {
    let map = hashmap! {
        "a" => 1,
        "b" => 2,
    };
    assert_eq!(map["a"], 1);
    assert_eq!(map["b"], 2);
    assert_eq!(map.len(), 2);
}
```

### Testing Proc Macros with trybuild

Verify compile-time behavior:

```rust
#[test]
fn compile_tests() {
    let t = trybuild::TestCases::new();
    t.pass("tests/pass/*.rs");       // should compile
    t.compile_fail("tests/fail/*.rs"); // should fail with expected error
}
```

```rust
// tests/fail/missing_field.rs
use my_crate::Builder;

#[derive(Builder)]
struct Config {
    #[builder(required)]
    host: String,
}

fn main() {
    Config::builder().build();  // error: missing required field 'host'
}
```

```
// tests/fail/missing_field.stderr
error: missing required field 'host'
 --> tests/fail/missing_field.rs:9:26
```

## Macro Documentation

Document macros with examples that compile and run:

```rust
/// Creates a `HashMap` from key-value pairs.
///
/// # Examples
///
/// ```
/// use my_crate::hashmap;
///
/// let map = hashmap! {
///     "name" => "Alice",
///     "role" => "admin",
/// };
/// assert_eq!(map["name"], "Alice");
/// ```
#[macro_export]
macro_rules! hashmap { ... }
```

## Compile-Time Impact

Proc macros increase compile time because they:
1. Must be compiled first (separate crate)
2. Pull in `syn` (~50k lines of code to parse)
3. Run during compilation of every file that uses them

**Mitigations**:
- Use `syn` with minimal features: `syn = { version = "2", features = ["derive"] }`
  instead of `features = ["full"]`
- Cache proc macro results where possible
- Consider `macro_rules!` alternatives for simple cases

## Debugging Workflow

```bash
# See what the macro generates
cargo expand path::to::module

# See macro expansion for a specific item
cargo expand path::to::MyStruct

# Verbose macro expansion trace (nightly)
RUSTFLAGS="-Z macro-backtrace" cargo build
```

## Common Pitfalls

- **Accidental identifier capture**: use `$crate::` for paths in exported macros
- **Multiple evaluation**: `$expr` used multiple times expands the expression
  multiple times — assign to a local variable first:
```rust
macro_rules! double {
    ($x:expr) => {{
        let val = $x;  // evaluate once
        val + val
    }};
}
```
- **Missing semicolons in repetition**: `$(stmt;)*` needs the semicolons inside
- **Type inference failures**: macros can produce code where types aren't clear —
  add type annotations in the generated code
