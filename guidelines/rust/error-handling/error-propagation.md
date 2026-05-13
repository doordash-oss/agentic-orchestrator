# Error Propagation

## anyhow for Application Code

Applications don't need callers to match on specific error variants — they need
rich context for debugging. Use `anyhow`:

```rust
use anyhow::{Context, Result, bail, ensure};

fn load_config(path: &Path) -> Result<Config> {
    let content = std::fs::read_to_string(path)
        .with_context(|| format!("failed to read config from {}", path.display()))?;

    let config: Config = toml::from_str(&content)
        .context("failed to parse config")?;

    ensure!(config.port > 0, "port must be positive, got {}", config.port);

    Ok(config)
}
```

### anyhow Macros

| Macro | Purpose |
|-------|---------|
| `anyhow!("msg")` | Create an ad-hoc error |
| `bail!("msg")` | Return an error immediately (`return Err(anyhow!(...))`) |
| `ensure!(cond, "msg")` | Return error if condition is false |
| `.context("msg")` | Add context to any `Result` or `Option` |
| `.with_context(\|\| ...)` | Add lazy context (avoids allocation on success) |

### When to Use context() vs with_context()

```rust
// context(): when the message is a static string or cheap
result.context("failed to connect")?;

// with_context(): when the message involves formatting
result.with_context(|| format!("failed to connect to {host}:{port}"))?;
```

## thiserror vs anyhow Decision

| | `thiserror` | `anyhow` |
|--|-------------|----------|
| **Use in** | Libraries | Applications |
| **Error type** | Custom enum/struct | `anyhow::Error` (type-erased) |
| **Callers can** | Match on variants | Only display or downcast |
| **Context** | Structured fields | String messages |
| **Dependencies** | Zero runtime deps | Minimal |

**The boundary**: if your code is `pub` and consumed by other crates, use
`thiserror`. If it's your application's internal code, use `anyhow`.

You can use both in the same project — `thiserror` for your library layer,
`anyhow` in your binary/main.

## Adding Context at Boundaries

Add context when crossing abstraction boundaries, not at every call:

```rust
// Too much wrapping — redundant
fn read_config(path: &Path) -> Result<String> {
    std::fs::read_to_string(path)
        .context("reading file")              // layer 1
        .context("in read_config")            // layer 2 — redundant
        .context("loading configuration")     // layer 3 — redundant
}

// Right level — one context at the boundary
fn read_config(path: &Path) -> Result<String> {
    std::fs::read_to_string(path)
        .with_context(|| format!("reading config from {}", path.display()))
}
```

## Downcasting anyhow Errors

When you need to check the underlying error type:

```rust
fn handle_error(err: &anyhow::Error) {
    if let Some(io_err) = err.downcast_ref::<io::Error>() {
        match io_err.kind() {
            io::ErrorKind::NotFound => { /* handle missing file */ }
            io::ErrorKind::PermissionDenied => { /* handle permissions */ }
            _ => { /* other I/O error */ }
        }
    }
}
```

## Error Handling in Async Code

`?` works seamlessly in async functions:

```rust
async fn fetch_data(url: &str) -> Result<Data> {
    let response = reqwest::get(url)
        .await
        .with_context(|| format!("failed to fetch {url}"))?;

    let data = response
        .json::<Data>()
        .await
        .context("failed to parse response")?;

    Ok(data)
}
```

## Error Handling Across Thread Boundaries

Errors must be `Send + Sync` to cross thread boundaries:

```rust
// Works: thiserror errors are Send + Sync by default
tokio::spawn(async {
    process().await?;
    Ok::<_, anyhow::Error>(())
});

// Fails: Rc is not Send
// Ensure all error types in the chain are Send + Sync
```

## Pattern: Error Mapping at Module Boundaries

```rust
// Internal module uses its own error type
mod storage {
    pub fn get(key: &str) -> Result<Vec<u8>, StorageError> { ... }
}

// Public API maps to application error
pub fn get_user(id: &str) -> Result<User> {
    let data = storage::get(id)
        .map_err(|e| match e {
            StorageError::NotFound => AppError::UserNotFound(id.into()),
            other => AppError::Storage(other),
        })?;
    deserialize(&data).context("deserializing user")
}
```
