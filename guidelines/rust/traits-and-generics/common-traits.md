# Common Traits

## Eagerly Implement Common Traits

The Rust API Guidelines (C-COMMON-TRAITS) recommend implementing these traits
on all public types where they make sense:

| Trait | Purpose | When to Derive |
|-------|---------|----------------|
| `Debug` | Developer-facing string representation | **Always** on public types |
| `Clone` | Explicit deep copy | When copying makes sense |
| `PartialEq`, `Eq` | Equality comparison | When equality is meaningful |
| `PartialOrd`, `Ord` | Ordering | When ordering is meaningful |
| `Hash` | Hash value for use in `HashMap`/`HashSet` | When `Eq` is implemented |
| `Default` | Sensible zero/empty value | When a natural default exists |
| `Display` | User-facing string representation | For types shown to users |

```rust
#[derive(Debug, Clone, PartialEq, Eq, Hash, Default)]
pub struct UserId(String);
```

## Display and Debug

```rust
use std::fmt;

#[derive(Debug)]  // Debug: auto-derived for development
struct Point {
    x: f64,
    y: f64,
}

// Display: manually implemented for user-facing output
impl fmt::Display for Point {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "({}, {})", self.x, self.y)
    }
}

// Debug: Point { x: 1.0, y: 2.0 }
// Display: (1.0, 2.0)
```

**Rule**: `Debug` should be derived on all public types. `Display` should
be implemented only when there's a clear user-facing representation.

## From and Into

Implement `From<T>` — never implement `Into<T>` directly (blanket impl):

```rust
struct Celsius(f64);
struct Fahrenheit(f64);

impl From<Celsius> for Fahrenheit {
    fn from(c: Celsius) -> Self {
        Fahrenheit(c.0 * 9.0 / 5.0 + 32.0)
    }
}

// Into is automatically available
let f: Fahrenheit = Celsius(100.0).into();

// From is more explicit
let f = Fahrenheit::from(Celsius(100.0));
```

### TryFrom for Fallible Conversions

```rust
impl TryFrom<i64> for Port {
    type Error = PortError;

    fn try_from(value: i64) -> Result<Self, Self::Error> {
        let port = u16::try_from(value)
            .map_err(|_| PortError::OutOfRange(value))?;
        if port == 0 {
            return Err(PortError::Zero);
        }
        Ok(Port(port))
    }
}
```

## AsRef and AsMut

For functions that accept multiple reference types cheaply:

```rust
// Accepts &str, &String, &Path, etc.
fn read_file(path: impl AsRef<Path>) -> io::Result<String> {
    std::fs::read_to_string(path.as_ref())
}

read_file("config.toml");              // &str → AsRef<Path>
read_file(String::from("config.toml")); // String → AsRef<Path>
read_file(Path::new("config.toml"));    // &Path → AsRef<Path>
```

## Deref and DerefMut

**Only implement on smart pointer types** (C-DEREF). Deref enables
transparent access to inner data:

```rust
use std::ops::Deref;

struct MyVec<T>(Vec<T>);

impl<T> Deref for MyVec<T> {
    type Target = [T];
    fn deref(&self) -> &[T] {
        &self.0
    }
}

// Now MyVec can use all &[T] methods
let v = MyVec(vec![1, 2, 3]);
println!("{}", v.len());  // deref coercion to &[T]
```

**Anti-pattern**: using `Deref` for inheritance-like behavior between
unrelated types. `Deref` is for wrappers/pointers, not for composition.

## Default

```rust
#[derive(Default)]
struct Config {
    host: String,        // Default: ""
    port: u16,           // Default: 0
    retries: u32,        // Default: 0
    verbose: bool,       // Default: false
}

// Custom default when derive doesn't suffice
impl Default for Config {
    fn default() -> Self {
        Config {
            host: "localhost".to_string(),
            port: 8080,
            retries: 3,
            verbose: false,
        }
    }
}

// Struct update syntax with defaults
let config = Config {
    port: 9090,
    ..Config::default()
};
```

## Iterator

Custom iterators implement `Iterator`:

```rust
struct Counter {
    count: usize,
    max: usize,
}

impl Iterator for Counter {
    type Item = usize;

    fn next(&mut self) -> Option<Self::Item> {
        if self.count < self.max {
            self.count += 1;
            Some(self.count)
        } else {
            None
        }
    }
}
```

Collections should implement `IntoIterator` for `for` loop support:

```rust
impl IntoIterator for MyCollection {
    type Item = Element;
    type IntoIter = std::vec::IntoIter<Element>;

    fn into_iter(self) -> Self::IntoIter {
        self.elements.into_iter()
    }
}
```

## Naming Convention (C-CONV)

| Prefix | Cost | Example |
|--------|------|---------|
| `as_` | Free (borrow → borrow) | `str::as_bytes()` → `&[u8]` |
| `to_` | Expensive (may allocate) | `str::to_lowercase()` → `String` |
| `into_` | Consumes self | `String::into_bytes()` → `Vec<u8>` |

## #\[must_use\]

Mark functions whose return values should not be ignored:

```rust
#[must_use = "this returns a new string and does not modify the original"]
pub fn to_uppercase(&self) -> String { ... }

#[must_use]
pub fn is_valid(&self) -> bool { ... }
```

The compiler warns if the return value is unused.
