# Advanced Trait Patterns

## Associated Types vs Generic Parameters

**Associated types**: one natural implementation per type:
```rust
trait Iterator {
    type Item;  // each iterator has ONE item type
    fn next(&mut self) -> Option<Self::Item>;
}
```

**Generic parameters**: multiple implementations possible:
```rust
trait From<T> {
    fn from(t: T) -> Self;
}
// String implements From<&str>, From<Vec<u8>>, From<char>, etc.
```

**Rule of thumb**: if a type can only meaningfully implement the trait one
way, use associated types. If multiple implementations make sense, use generics.

## Type-State Builder Pattern

Encode valid construction states in the type system:

```rust
struct NoUrl;
struct HasUrl(String);
struct NoTimeout;
struct HasTimeout(Duration);

struct RequestBuilder<U, T> {
    url: U,
    timeout: T,
}

impl RequestBuilder<NoUrl, NoTimeout> {
    fn new() -> Self {
        RequestBuilder { url: NoUrl, timeout: NoTimeout }
    }
}

impl<T> RequestBuilder<NoUrl, T> {
    fn url(self, url: impl Into<String>) -> RequestBuilder<HasUrl, T> {
        RequestBuilder { url: HasUrl(url.into()), timeout: self.timeout }
    }
}

impl<U> RequestBuilder<U, NoTimeout> {
    fn timeout(self, dur: Duration) -> RequestBuilder<U, HasTimeout> {
        RequestBuilder { url: self.url, timeout: HasTimeout(dur) }
    }
}

// build() only available when URL is set
impl<T> RequestBuilder<HasUrl, T> {
    fn build(self) -> Request {
        Request { url: self.url.0, timeout: ... }
    }
}

// Usage: compile error if URL is missing
let req = RequestBuilder::new()
    .url("https://example.com")
    .timeout(Duration::from_secs(30))
    .build();  // only works because HasUrl
```

## Simple Builder Pattern

For simpler cases, use a standard builder:

```rust
#[derive(Debug)]
pub struct Server {
    host: String,
    port: u16,
    workers: usize,
}

#[derive(Default)]
pub struct ServerBuilder {
    host: Option<String>,
    port: Option<u16>,
    workers: Option<usize>,
}

impl ServerBuilder {
    pub fn host(mut self, host: impl Into<String>) -> Self {
        self.host = Some(host.into());
        self
    }

    pub fn port(mut self, port: u16) -> Self {
        self.port = Some(port);
        self
    }

    pub fn workers(mut self, n: usize) -> Self {
        self.workers = Some(n);
        self
    }

    pub fn build(self) -> Result<Server, BuildError> {
        Ok(Server {
            host: self.host.unwrap_or_else(|| "localhost".into()),
            port: self.port.ok_or(BuildError::MissingPort)?,
            workers: self.workers.unwrap_or(num_cpus::get()),
        })
    }
}
```

## Newtype Pattern

Wrap a type to add meaning, implement foreign traits, or restrict APIs:

```rust
// Type safety: prevent mixing up IDs
struct UserId(u64);
struct OrderId(u64);

fn process_order(user: UserId, order: OrderId) { ... }
// process_order(order_id, user_id) → compile error!

// Implement foreign traits via newtype
struct Wrapper(Vec<String>);

impl Display for Wrapper {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "[{}]", self.0.join(", "))
    }
}
```

## Blanket Implementations

Implement a trait for all types that satisfy a bound:

```rust
trait Printable {
    fn print(&self);
}

// Blanket: all Display types are Printable
impl<T: Display> Printable for T {
    fn print(&self) {
        println!("{self}");
    }
}
```

**Be careful**: blanket impls can make it impossible for downstream crates
to implement the trait for their types (orphan rule conflicts).

## Marker Traits

Traits with no methods — used for type-level properties:

```rust
// std examples:
// Send — safe to send between threads
// Sync — safe to share references between threads
// Unpin — safe to move after pinning
// Sized — has a known size at compile time

// Custom marker trait
trait Validated {}

fn process<T: Validated>(data: T) { ... }
```

## Operator Overloading

```rust
use std::ops::Add;

#[derive(Debug, Clone, Copy)]
struct Vector2D { x: f64, y: f64 }

impl Add for Vector2D {
    type Output = Self;

    fn add(self, other: Self) -> Self {
        Vector2D {
            x: self.x + other.x,
            y: self.y + other.y,
        }
    }
}

let v = Vector2D { x: 1.0, y: 2.0 } + Vector2D { x: 3.0, y: 4.0 };
```

**Guideline (C-OVERLOAD)**: operator overloads should be unsurprising.
Don't overload `+` to mean something other than addition.

## Strategy Pattern via Traits

```rust
trait Compression {
    fn compress(&self, data: &[u8]) -> Vec<u8>;
    fn decompress(&self, data: &[u8]) -> Vec<u8>;
}

struct Gzip;
struct Zstd;

impl Compression for Gzip { ... }
impl Compression for Zstd { ... }

struct Storage<C: Compression> {
    compressor: C,
}

impl<C: Compression> Storage<C> {
    fn save(&self, data: &[u8]) {
        let compressed = self.compressor.compress(data);
        // write compressed
    }
}
```
