# Property-Based and Snapshot Testing

## Property-Based Testing with proptest

Instead of testing specific examples, define **properties** that must hold
for all inputs. `proptest` generates random inputs and shrinks failures:

```rust
use proptest::prelude::*;

proptest! {
    #[test]
    fn sort_preserves_length(ref v in prop::collection::vec(any::<i32>(), 0..100)) {
        let mut sorted = v.clone();
        sorted.sort();
        assert_eq!(sorted.len(), v.len());
    }

    #[test]
    fn sort_is_idempotent(ref v in prop::collection::vec(any::<i32>(), 0..100)) {
        let mut sorted = v.clone();
        sorted.sort();
        let mut double_sorted = sorted.clone();
        double_sorted.sort();
        assert_eq!(sorted, double_sorted);
    }
}
```

### Custom Strategies

Generate constrained random data:

```rust
use proptest::prelude::*;

fn valid_port() -> impl Strategy<Value = u16> {
    1..=65535u16
}

fn valid_email() -> impl Strategy<Value = String> {
    "[a-z]{1,10}@[a-z]{1,10}\\.[a-z]{2,4}"
}

proptest! {
    #[test]
    fn parse_valid_port(port in valid_port()) {
        let result = Port::try_from(port);
        assert!(result.is_ok());
    }
}
```

### Shrinking

When a test fails, `proptest` automatically finds the **smallest** input
that still fails — making debugging easier:

```
test sort_is_sorted ... FAILED
  minimal failing input: v = [1, 0]
```

### When to Use Property Tests

- Parsing/serialization round-trips: `parse(format(x)) == x`
- Invariants: sorting produces sorted output, compression preserves data
- Mathematical properties: associativity, commutativity, idempotency
- Fuzz-like exploration of edge cases

## Snapshot Testing with insta

Captures complex output and compares against stored snapshots:

```rust
use insta::assert_snapshot;
use insta::assert_debug_snapshot;

#[test]
fn test_error_message() {
    let err = validate_config(&bad_config);
    assert_snapshot!(err.to_string());
}

#[test]
fn test_parsed_ast() {
    let ast = parse("let x = 42;");
    assert_debug_snapshot!(ast);
}
```

### JSON/YAML Snapshots

```rust
use insta::assert_json_snapshot;

#[test]
fn test_api_response() {
    let response = build_response();
    assert_json_snapshot!(response);
}
```

### Workflow

1. Run tests — new snapshots are created as `.snap.new` files
2. Review with `cargo insta review` — interactive approval
3. Approved snapshots become the `.snap` reference files
4. Future runs compare against the reference

### Snapshot Settings

```rust
use insta::Settings;

#[test]
fn test_with_settings() {
    let mut settings = Settings::clone_current();
    settings.set_snapshot_suffix("linux");
    settings.set_sort_maps(true);
    settings.bind(|| {
        assert_snapshot!(...);
    });
}
```

### When to Use Snapshot Tests

- Complex structured output (ASTs, API responses, rendered templates)
- Error messages that should remain stable
- Serialized data structures
- CLI output formatting

### When NOT to Use Snapshot Tests

- Simple value comparisons (`assert_eq!` is clearer)
- Frequently changing output (snapshot churn)
- Tests where the expected value is easily expressed inline

## Combining Strategies

Use property tests and snapshots together:

```rust
proptest! {
    #[test]
    fn roundtrip_json(config in arb_config()) {
        let json = serde_json::to_string(&config).unwrap();
        let parsed: Config = serde_json::from_str(&json).unwrap();
        assert_eq!(config, parsed);
    }
}

#[test]
fn config_snapshot() {
    let config = Config::default();
    assert_json_snapshot!(config);
}
```
