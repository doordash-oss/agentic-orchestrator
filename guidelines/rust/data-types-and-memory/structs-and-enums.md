# Structs and Enums

## Struct Design

### Named Structs

```rust
#[derive(Debug, Clone)]
pub struct Config {
    pub host: String,
    pub port: u16,
    workers: usize,  // private — implementation detail
}
```

### Tuple Structs (Newtypes)

Wrap a single value to add type safety:

```rust
pub struct UserId(pub u64);
pub struct Email(String);  // private inner — validation in constructor

impl Email {
    pub fn new(value: impl Into<String>) -> Result<Self, ValidationError> {
        let s = value.into();
        if s.contains('@') {
            Ok(Email(s))
        } else {
            Err(ValidationError::InvalidEmail)
        }
    }

    pub fn as_str(&self) -> &str {
        &self.0
    }
}
```

### Unit Structs

Used as markers or zero-size tokens:

```rust
pub struct Production;
pub struct Development;

struct App<Env> {
    _env: std::marker::PhantomData<Env>,
}
```

### Struct Update Syntax

```rust
let defaults = Config::default();
let config = Config {
    port: 9090,
    ..defaults  // fill remaining fields from defaults
};
```

### Private Fields with Constructors (C-STRUCT-PRIVATE)

Keep fields private for future compatibility:

```rust
pub struct Connection {
    host: String,
    port: u16,
}

impl Connection {
    pub fn new(host: impl Into<String>, port: u16) -> Self {
        Connection { host: host.into(), port }
    }

    pub fn host(&self) -> &str { &self.host }
    pub fn port(&self) -> u16 { self.port }
}
```

## Enum Design

### Make Illegal States Unrepresentable

```rust
// Bad: multiple booleans with invalid combinations
struct Connection {
    is_connected: bool,
    is_authenticated: bool,  // meaningless if not connected
    is_idle: bool,           // meaningless if not authenticated
}

// Good: enum encodes valid states only
enum ConnectionState {
    Disconnected,
    Connected { addr: SocketAddr },
    Authenticated { addr: SocketAddr, user: User },
    Idle { addr: SocketAddr, user: User, since: Instant },
}
```

### Data-Carrying Variants

```rust
#[derive(Debug)]
pub enum Command {
    Get { key: String },
    Set { key: String, value: Vec<u8>, ttl: Option<Duration> },
    Delete { key: String },
    Ping,
}

fn handle(cmd: Command) {
    match cmd {
        Command::Get { key } => { ... }
        Command::Set { key, value, ttl } => { ... }
        Command::Delete { key } => { ... }
        Command::Ping => { ... }
    }
}
```

### #\[non_exhaustive\]

Allow adding variants without breaking downstream code:

```rust
#[derive(Debug)]
#[non_exhaustive]
pub enum Error {
    NotFound,
    PermissionDenied,
    Timeout,
    // Future versions can add variants
}

// Downstream code MUST have a wildcard arm:
match err {
    Error::NotFound => { ... }
    Error::PermissionDenied => { ... }
    Error::Timeout => { ... }
    _ => { ... }  // required due to #[non_exhaustive]
}
```

Also works on structs — prevents construction with struct literal syntax
outside the defining crate.

## Builder Pattern

For types with many optional fields:

```rust
pub struct Request {
    url: String,
    method: Method,
    headers: HeaderMap,
    timeout: Duration,
    body: Option<Vec<u8>>,
}

impl Request {
    pub fn builder(url: impl Into<String>) -> RequestBuilder {
        RequestBuilder {
            url: url.into(),
            method: Method::GET,
            headers: HeaderMap::new(),
            timeout: Duration::from_secs(30),
            body: None,
        }
    }
}

pub struct RequestBuilder { ... }

impl RequestBuilder {
    pub fn method(mut self, method: Method) -> Self {
        self.method = method;
        self
    }

    pub fn header(mut self, name: &str, value: &str) -> Self {
        self.headers.insert(name, value);
        self
    }

    pub fn body(mut self, body: Vec<u8>) -> Self {
        self.body = Some(body);
        self
    }

    pub fn build(self) -> Request {
        Request {
            url: self.url,
            method: self.method,
            headers: self.headers,
            timeout: self.timeout,
            body: self.body,
        }
    }
}
```

## Type Safety with Enums Over Booleans (C-CUSTOM-TYPE)

```rust
// Bad: what does `true` mean?
fn connect(host: &str, use_tls: bool, verify_cert: bool) { ... }
connect("example.com", true, false);  // what are these bools?

// Good: self-documenting
enum TlsMode { Disabled, Enabled, InsecureSkipVerify }
fn connect(host: &str, tls: TlsMode) { ... }
connect("example.com", TlsMode::InsecureSkipVerify);
```

## Bitflags

For sets of flags, use the `bitflags` crate instead of enums:

```rust
use bitflags::bitflags;

bitflags! {
    #[derive(Debug, Clone, Copy)]
    pub struct Permissions: u32 {
        const READ    = 0b001;
        const WRITE   = 0b010;
        const EXECUTE = 0b100;
    }
}

let perms = Permissions::READ | Permissions::WRITE;
assert!(perms.contains(Permissions::READ));
```
