# Custom Error Types

## Library Errors with thiserror

Libraries should expose typed, specific errors so callers can handle them
programmatically. Use `thiserror` for ergonomic derive macros:

```rust
use thiserror::Error;

#[derive(Debug, Error)]
pub enum ConfigError {
    #[error("failed to read config file: {path}")]
    ReadFile {
        path: PathBuf,
        #[source]
        source: io::Error,
    },

    #[error("invalid config format")]
    Parse(#[from] toml::de::Error),

    #[error("missing required field: {0}")]
    MissingField(String),

    #[error("value out of range: {field} must be {min}..={max}, got {value}")]
    OutOfRange {
        field: String,
        min: i64,
        max: i64,
        value: i64,
    },
}
```

### thiserror Attributes

| Attribute | Purpose |
|-----------|---------|
| `#[error("...")]` | Generates `Display` impl — use `{0}`, `{field}` for interpolation |
| `#[from]` | Generates `From<T>` impl for automatic `?` conversion |
| `#[source]` | Marks the field as the error source (for `Error::source()`) |

**`#[from]` implies `#[source]`** — don't use both on the same field.

## Enum vs Struct Errors

**Enum errors** (most common): when a function can fail in multiple distinct ways:
```rust
#[derive(Debug, Error)]
pub enum StorageError {
    #[error("connection failed")]
    Connection(#[source] io::Error),
    #[error("query failed: {0}")]
    Query(String),
    #[error("not found: {0}")]
    NotFound(String),
}
```

**Struct errors**: when there's only one kind of failure, or for wrapping:
```rust
#[derive(Debug, Error)]
#[error("database operation failed: {operation}")]
pub struct DbError {
    pub operation: String,
    #[source]
    pub source: sqlx::Error,
}
```

## Error Type Design Principles

### Error Messages Are Lowercase

Error strings should be lowercase, no trailing punctuation — they compose
into larger messages:

```rust
// Good
#[error("connection refused")]
#[error("invalid port number: {0}")]

// Bad
#[error("Connection refused.")]
#[error("Invalid port number: {0}!")]
```

### Don't Expose Implementation Details

Only include error variants that are part of your public API contract:

```rust
// Bad: leaks that you use reqwest internally
pub enum ApiError {
    #[error("http error")]
    Http(#[from] reqwest::Error),  // callers now depend on reqwest
}

// Good: wrap the internal error
pub enum ApiError {
    #[error("http request failed")]
    Http(#[source] Box<dyn std::error::Error + Send + Sync>),
}
```

### Errors Must Be Send + Sync

Required for use across threads and with most error-handling libraries:

```rust
// thiserror derives this automatically if all fields are Send + Sync
#[derive(Debug, Error)]
pub enum MyError {
    // All variants should contain Send + Sync types
}
```

### Use #\[non_exhaustive\] for Public Error Enums

Allows adding variants without breaking downstream code:

```rust
#[derive(Debug, Error)]
#[non_exhaustive]
pub enum ParseError {
    #[error("unexpected token: {0}")]
    UnexpectedToken(String),
    #[error("unexpected end of input")]
    UnexpectedEof,
    // Future: can add variants without semver break
}
```

## Nested Error Types

For large libraries, organize errors by module:

```rust
// lib/src/error.rs — top-level error
#[derive(Debug, Error)]
pub enum Error {
    #[error("config error")]
    Config(#[from] config::Error),
    #[error("storage error")]
    Storage(#[from] storage::Error),
    #[error("auth error")]
    Auth(#[from] auth::Error),
}

// lib/src/config/error.rs — module-level error
#[derive(Debug, Error)]
pub enum Error {
    #[error("failed to read: {0}")]
    Read(#[from] io::Error),
    #[error("invalid format")]
    Parse(#[from] toml::de::Error),
}
```

## Converting with From

Implement `From` (or use `#[from]`) for automatic `?` conversion:

```rust
// Manual From impl when you need custom logic
impl From<io::Error> for AppError {
    fn from(err: io::Error) -> Self {
        match err.kind() {
            io::ErrorKind::NotFound => AppError::NotFound,
            io::ErrorKind::PermissionDenied => AppError::Unauthorized,
            _ => AppError::Internal(err.into()),
        }
    }
}
```

## Display vs Debug

- **`Display`**: user-facing, concise — "connection refused"
- **`Debug`**: developer-facing, detailed — `ConnectionError { addr: "127.0.0.1:5432", source: Os { code: 111 } }`

`thiserror`'s `#[error("...")]` generates `Display`. Always derive `Debug`.
