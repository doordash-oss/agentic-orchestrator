# Serialization and I/O

## Serde — Serialization Framework

Serde is the standard serialization framework. Use derive macros for most types:

```rust
use serde::{Serialize, Deserialize};

#[derive(Debug, Serialize, Deserialize)]
pub struct Config {
    pub host: String,
    pub port: u16,
    #[serde(default = "default_retries")]
    pub retries: u32,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub api_key: Option<String>,
}

fn default_retries() -> u32 { 3 }
```

### Serde Attributes

```rust
#[derive(Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]        // JSON convention
#[serde(deny_unknown_fields)]             // strict parsing
pub struct ApiResponse {
    #[serde(rename = "type")]             // Rust keyword conflict
    pub kind: String,

    #[serde(default)]                     // use Default if missing
    pub tags: Vec<String>,

    #[serde(with = "humantime_serde")]    // custom serialization
    pub timeout: Duration,

    #[serde(flatten)]                     // inline nested struct
    pub metadata: Metadata,
}
```

### Enum Serialization

```rust
#[derive(Serialize, Deserialize)]
#[serde(tag = "type")]  // internally tagged: {"type": "circle", "radius": 5}
pub enum Shape {
    #[serde(rename = "circle")]
    Circle { radius: f64 },
    #[serde(rename = "rectangle")]
    Rectangle { width: f64, height: f64 },
}

// Other tagging strategies:
// #[serde(tag = "t", content = "c")]  — adjacently tagged
// #[serde(untagged)]                  — no tag (tries each variant)
```

### Gate Serde Behind a Feature Flag

For libraries — don't force serde on users:

```toml
[features]
serde = ["dep:serde"]

[dependencies]
serde = { version = "1", features = ["derive"], optional = true }
```

```rust
#[derive(Debug, Clone)]
#[cfg_attr(feature = "serde", derive(serde::Serialize, serde::Deserialize))]
pub struct MyType { ... }
```

## std::io — Buffered I/O

### Always Buffer File I/O

```rust
use std::io::{BufReader, BufWriter, Read, Write, BufRead};

// Reading — unbuffered is ~100x slower
let file = File::open("data.txt")?;
let reader = BufReader::new(file);

for line in reader.lines() {
    let line = line?;
    process(&line);
}

// Writing — flush happens on drop or explicit flush
let file = File::create("output.txt")?;
let mut writer = BufWriter::new(file);
writeln!(writer, "line {}", i)?;
writer.flush()?;  // ensure all data is written
```

### Read and Write Traits

Accept generic `Read`/`Write` for flexible APIs (C-RW-VALUE):

```rust
fn parse_data<R: Read>(reader: R) -> Result<Data> {
    let mut buf = String::new();
    let mut reader = BufReader::new(reader);
    reader.read_to_string(&mut buf)?;
    // parse buf
}

// Works with files, network streams, byte slices, etc.
parse_data(File::open("data.txt")?)?;
parse_data(&b"inline data"[..])?;
parse_data(TcpStream::connect("host:80")?)?;
```

## std::fs — File Operations

```rust
use std::fs;

// Simple read/write
let content = fs::read_to_string("config.toml")?;
fs::write("output.txt", "hello")?;

// Read bytes
let bytes = fs::read("image.png")?;

// Atomic write (write to temp, then rename)
use tempfile::NamedTempFile;
let mut tmp = NamedTempFile::new_in(".")?;
write!(tmp, "{}", content)?;
tmp.persist("config.toml")?;  // atomic rename
```

## tracing — Structured Logging

Use `tracing` over `log` — it's structured, span-aware, and async-compatible:

```rust
use tracing::{info, warn, error, debug, instrument, span, Level};

#[instrument(skip(db), fields(user_id = %user_id))]
async fn get_user(db: &Database, user_id: u64) -> Result<User> {
    info!("fetching user");

    let user = db.find_user(user_id).await
        .map_err(|e| {
            error!(error = %e, "database query failed");
            e
        })?;

    debug!(name = %user.name, "user found");
    Ok(user)
}
```

### Subscriber Configuration

```rust
use tracing_subscriber::{fmt, EnvFilter, layer::SubscriberExt, util::SubscriberInitExt};

fn init_tracing() {
    tracing_subscriber::registry()
        .with(EnvFilter::try_from_default_env()
            .unwrap_or_else(|_| EnvFilter::new("info")))
        .with(fmt::layer()
            .with_target(true)
            .with_thread_ids(true)
            .with_file(true)
            .with_line_number(true))
        .init();
}

// Control log level with RUST_LOG env var:
// RUST_LOG=debug cargo run
// RUST_LOG=my_crate=trace,tower_http=debug cargo run
```

### JSON Logging for Production

```rust
tracing_subscriber::registry()
    .with(EnvFilter::new("info"))
    .with(fmt::layer().json())  // structured JSON output
    .init();
```

## Compile Regex Once

```rust
use std::sync::LazyLock;
use regex::Regex;

static EMAIL_RE: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(r"^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$").unwrap()
});

fn is_valid_email(email: &str) -> bool {
    EMAIL_RE.is_match(email)
}
```
