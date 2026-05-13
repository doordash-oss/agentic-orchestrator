# Trait Design

## Core Design Principles

### Keep Traits Focused

A trait should represent a single capability:

```rust
// Bad: kitchen-sink trait
trait DataStore {
    fn get(&self, key: &str) -> Option<Vec<u8>>;
    fn set(&mut self, key: &str, value: &[u8]);
    fn serialize(&self) -> String;     // separate concern
    fn log_access(&self);              // separate concern
}

// Good: single responsibility
trait DataStore {
    fn get(&self, key: &str) -> Option<Vec<u8>>;
    fn set(&mut self, key: &str, value: &[u8]);
}
```

### Object Safety

A trait is object-safe (usable as `dyn Trait`) if:
- No methods return `Self`
- No methods have generic type parameters
- No `where Self: Sized` bounds on the trait itself

```rust
// Object-safe — can use as dyn Formatter
trait Formatter {
    fn format(&self, input: &str) -> String;
}

// NOT object-safe — returns Self
trait Cloneable {
    fn clone_self(&self) -> Self;
}

// Fix: add Sized bound to exclude from dyn dispatch
trait Cloneable {
    fn clone_self(&self) -> Self where Self: Sized;
    fn clone_boxed(&self) -> Box<dyn Cloneable>;  // object-safe alternative
}
```

## Sealed Traits

Prevent downstream crates from implementing your trait:

```rust
mod private {
    pub trait Sealed {}
}

// External crates can use this trait but not implement it
pub trait MyTrait: private::Sealed {
    fn method(&self);
}

// Only your crate can implement Sealed (and thus MyTrait)
impl private::Sealed for MyType {}
impl MyTrait for MyType {
    fn method(&self) { ... }
}
```

**When to seal**: when adding methods to the trait should not be a breaking
change. Sealed traits can grow without semver concerns.

## Extension Traits

Add methods to types you don't own:

```rust
pub trait IteratorExt: Iterator {
    fn chunks(self, size: usize) -> Chunks<Self>
    where
        Self: Sized,
    {
        Chunks { iter: self, size }
    }
}

// Blanket impl for all iterators
impl<I: Iterator> IteratorExt for I {}
```

Naming convention: `{TraitName}Ext`.

## Supertraits

Require another trait as a prerequisite:

```rust
trait Animal: Display + Debug {
    fn name(&self) -> &str;
}

// Implementors must also implement Display and Debug
#[derive(Debug)]
struct Dog { name: String }

impl Display for Dog {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "{}", self.name)
    }
}

impl Animal for Dog {
    fn name(&self) -> &str { &self.name }
}
```

## Constructors Are Static Methods

Use `new()` or named constructors, not trait-based construction:

```rust
impl Server {
    // Primary constructor
    pub fn new(config: Config) -> Self { ... }

    // Named constructors for common configurations
    pub fn with_defaults() -> Self { ... }
    pub fn from_env() -> Result<Self> { ... }
}
```

**Convention**: `new` takes the minimum required arguments. Use builder
pattern for complex construction (see [advanced-patterns.md](advanced-patterns.md)).

## Trait vs Enum

| Use Trait When | Use Enum When |
|----------------|---------------|
| Open set of types (extensible) | Closed set of variants (known at compile time) |
| Different crates implement it | All variants live in one crate |
| Behavior varies by impl | Data varies by variant |
| Need dynamic dispatch | Need exhaustive matching |

```rust
// Trait: open — anyone can add a new formatter
trait OutputFormat {
    fn render(&self, data: &Data) -> String;
}

// Enum: closed — known set of log levels
enum Level {
    Debug,
    Info,
    Warn,
    Error,
}
```

## The Orphan Rule

You can only implement a trait for a type if you own either the trait or the type.
Use the **newtype pattern** to work around it:

```rust
// Can't impl Display for Vec<T> — neither is ours
// But we can wrap Vec in a newtype:
struct Wrapper(Vec<String>);

impl Display for Wrapper {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "[{}]", self.0.join(", "))
    }
}
```
