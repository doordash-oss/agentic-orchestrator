# Panic and Recovery

## When to Panic

`panic!` is for **unrecoverable programmer errors** — situations where
continuing would be unsafe or meaningless:

```rust
// Invariant violation — should never happen if code is correct
fn get_index(data: &[u8], idx: usize) -> u8 {
    assert!(idx < data.len(), "index {idx} out of bounds for len {}", data.len());
    data[idx]
}

// Unreachable code paths
match direction {
    Direction::North => move_north(),
    Direction::South => move_south(),
    // All variants handled — if a new variant is added, this catches it
}
```

**Acceptable panics**:
- Contract violations that indicate bugs (bad indices, invalid state)
- `todo!()` / `unimplemented!()` during development
- Tests (via `assert!`, `assert_eq!`, `#[should_panic]`)
- Initialization failures where recovery is impossible

**Never panic for**:
- Missing files, network errors, invalid user input
- Anything the caller might reasonably want to handle

## unwrap() and expect()

| Method | Use When |
|--------|----------|
| `.unwrap()` | Tests only, or trivially provable (e.g., `"123".parse::<i32>().unwrap()`) |
| `.expect("reason")` | When an invariant is documented but can't be encoded in types |
| `?` | Almost everywhere else |

```rust
// Good: expect with invariant reason
let home = std::env::var("HOME")
    .expect("HOME environment variable must be set");

// Good: unwrap on a compile-time-provable value
let re = Regex::new(r"^\d{4}-\d{2}-\d{2}$").unwrap();  // static pattern

// Bad: unwrap on user input
let port: u16 = args[1].parse().unwrap();  // could be "abc"

// Good: propagate with context
let port: u16 = args[1].parse()
    .context("invalid port number")?;
```

## Error Handling in main()

### With anyhow

```rust
use anyhow::Result;

fn main() -> Result<()> {
    let config = load_config()?;
    run_server(config)?;
    Ok(())
}
// Prints: "Error: failed to read config from ./config.toml"
//         "Caused by: No such file or directory (os error 2)"
```

### With process::ExitCode (Rust 1.61+)

```rust
use std::process::ExitCode;

fn main() -> ExitCode {
    match run() {
        Ok(()) => ExitCode::SUCCESS,
        Err(err) => {
            eprintln!("error: {err:#}");
            ExitCode::FAILURE
        }
    }
}

fn run() -> anyhow::Result<()> {
    // ...
    Ok(())
}
```

### Custom Exit Codes

```rust
fn main() -> ExitCode {
    match run() {
        Ok(()) => ExitCode::SUCCESS,
        Err(err) => {
            eprintln!("error: {err:#}");
            ExitCode::from(2)  // custom exit code
        }
    }
}
```

## catch_unwind

Catches panics at FFI boundaries or in spawned threads:

```rust
use std::panic;

let result = panic::catch_unwind(|| {
    // code that might panic
    risky_operation()
});

match result {
    Ok(value) => println!("success: {value}"),
    Err(_) => eprintln!("operation panicked"),
}
```

**When to use**:
- FFI boundaries (panics across FFI are undefined behavior)
- Thread pools where one task panicking shouldn't kill the pool
- Plugin systems where untrusted code runs

**When NOT to use**:
- As a general error-handling mechanism (use `Result` instead)
- `catch_unwind` does NOT catch `abort` panics (`panic = "abort"` in Cargo.toml)

## The Never Type (!)

Indicates a function never returns — useful for error handling:

```rust
fn exit_with_error(msg: &str) -> ! {
    eprintln!("fatal: {msg}");
    std::process::exit(1);
}

// The never type coerces to any type, enabling patterns like:
let value: i32 = match result {
    Ok(v) => v,
    Err(e) => exit_with_error(&e.to_string()),  // ! coerces to i32
};
```

## Panic Hooks

Customize panic output for production:

```rust
std::panic::set_hook(Box::new(|info| {
    // Log to your observability system
    tracing::error!("panic: {info}");

    // Optionally include backtrace
    let bt = std::backtrace::Backtrace::capture();
    tracing::error!("backtrace: {bt}");
}));
```
