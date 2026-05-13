# Unit and Integration Tests

## Unit Test Organization

Unit tests live in the same file as the code they test, inside a
`#[cfg(test)]` module:

```rust
pub fn add(a: i32, b: i32) -> i32 {
    a + b
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_add() {
        assert_eq!(add(2, 3), 5);
    }

    #[test]
    fn test_add_negative() {
        assert_eq!(add(-1, 1), 0);
    }
}
```

**Why `#[cfg(test)]`**: the module and all its imports are stripped from
release builds — zero cost.

**Test the public API** — prefer `use super::*` to test through the public
interface rather than reaching into private internals.

## Assertions

```rust
assert_eq!(got, expected);                      // equality
assert_ne!(got, other);                         // inequality
assert!(condition);                             // boolean
assert!(result.is_err());                       // error checking
assert_eq!(got, expected, "context: {input}");  // with message

// Pattern matching in assertions
assert!(matches!(result, Ok(42)));
assert!(matches!(err, Err(MyError::NotFound { .. })));
```

**Assertion message convention**: include the input that caused the failure:

```rust
assert_eq!(
    parse_port("abc"),
    Err(ParseError::InvalidPort),
    "expected InvalidPort for input 'abc'"
);
```

## Integration Tests

Live in `tests/` at the crate root — they test your public API as an
external consumer:

```
my_crate/
├── src/
│   └── lib.rs
├── tests/
│   ├── api_tests.rs        # each file is a separate test binary
│   └── common/
│       └── mod.rs           # shared helpers (not a test file)
```

```rust
// tests/api_tests.rs
use my_crate::Client;

#[test]
fn client_connects() {
    let client = Client::new("localhost:8080");
    assert!(client.ping().is_ok());
}
```

**Shared helpers**: put them in `tests/common/mod.rs` (not `tests/common.rs`,
which Cargo would try to compile as a test binary).

## Doc Tests

Code examples in doc comments run as tests with `cargo test --doc`:

```rust
/// Adds two numbers.
///
/// # Examples
///
/// ```
/// use my_crate::add;
/// assert_eq!(add(2, 3), 5);
/// ```
pub fn add(a: i32, b: i32) -> i32 {
    a + b
}
```

### Hiding Setup Code

Use `#` to hide lines in rendered docs but still compile them:

```rust
/// ```
/// # use my_crate::Config;
/// # fn main() -> Result<(), Box<dyn std::error::Error>> {
/// let config = Config::from_file("config.toml")?;
/// assert_eq!(config.port, 8080);
/// # Ok(())
/// # }
/// ```
```

### Marking Examples That Should Not Compile

```rust
/// ```compile_fail
/// let x: i32 = "not a number";  // intentionally fails
/// ```

/// ```no_run
/// // Compiles but doesn't execute (e.g., requires network)
/// let response = reqwest::blocking::get("https://example.com")?;
/// ```
```

## #\[should_panic\]

Test that code panics:

```rust
#[test]
#[should_panic(expected = "index out of bounds")]
fn test_index_panic() {
    let v = vec![1, 2, 3];
    let _ = v[99];
}
```

Prefer testing `Result::Err` over panics when possible.

## Async Tests

```rust
#[tokio::test]
async fn test_async_fetch() {
    let result = fetch_data("key").await;
    assert!(result.is_ok());
}

// Single-threaded variant
#[tokio::test(flavor = "current_thread")]
async fn test_single_threaded() {
    // ...
}
```

## Test Naming Conventions

Use descriptive names that describe the scenario:

```rust
#[test]
fn parse_valid_port_returns_number() { ... }

#[test]
fn parse_empty_string_returns_error() { ... }

#[test]
fn connect_with_invalid_host_times_out() { ... }
```

## Conditional Tests

```rust
#[test]
#[ignore]  // skip by default, run with cargo test -- --ignored
fn expensive_integration_test() { ... }

#[test]
#[cfg(target_os = "linux")]
fn linux_specific_test() { ... }
```

## Test Organization Best Practices

- Keep unit tests close to the code they test
- Use `#[test]` for synchronous, `#[tokio::test]` for async
- One assertion per test when possible — clearer failure messages
- Test error paths as thoroughly as success paths
- Use `assert_eq!(got, want)` — got first, want second
