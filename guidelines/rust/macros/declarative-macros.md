# Declarative Macros

## macro_rules! Basics

Declarative macros use pattern matching on Rust syntax:

```rust
macro_rules! vec_of_strings {
    ($($element:expr),* $(,)?) => {
        vec![$($element.to_string()),*]
    };
}

let names = vec_of_strings!["Alice", "Bob", "Charlie"];
```

## Fragment Specifiers

| Specifier | Matches | Example |
|-----------|---------|---------|
| `$x:expr` | Expression | `42`, `a + b`, `foo()` |
| `$x:ident` | Identifier | `my_var`, `String` |
| `$x:ty` | Type | `i32`, `Vec<String>` |
| `$x:pat` | Pattern | `Some(x)`, `_`, `1..=5` |
| `$x:stmt` | Statement | `let x = 1;` |
| `$x:block` | Block | `{ ... }` |
| `$x:item` | Item (fn, struct, etc.) | `fn foo() {}` |
| `$x:path` | Path | `std::io::Error` |
| `$x:tt` | Single token tree | Any token or `(...)` group |
| `$x:literal` | Literal | `"hello"`, `42`, `true` |

## Repetition

```rust
// Zero or more: $(...),*
macro_rules! print_all {
    ($($val:expr),*) => {
        $(println!("{}", $val);)*
    };
}

// One or more: $(...),+
macro_rules! min {
    ($x:expr $(, $y:expr)+) => {
        {
            let mut min_val = $x;
            $(if $y < min_val { min_val = $y; })+
            min_val
        }
    };
}

// Optional trailing comma: $(,)?
macro_rules! my_vec {
    ($($elem:expr),* $(,)?) => { ... };
}
```

## Common Patterns

### Hashmap Literal

```rust
macro_rules! hashmap {
    ($($key:expr => $value:expr),* $(,)?) => {{
        let mut map = ::std::collections::HashMap::new();
        $(map.insert($key, $value);)*
        map
    }};
}

let scores = hashmap! {
    "Alice" => 100,
    "Bob" => 85,
};
```

### Newtype with Trait Delegation

```rust
macro_rules! newtype {
    ($name:ident, $inner:ty) => {
        #[derive(Debug, Clone, PartialEq, Eq, Hash)]
        pub struct $name($inner);

        impl $name {
            pub fn new(value: $inner) -> Self {
                $name(value)
            }

            pub fn into_inner(self) -> $inner {
                self.0
            }
        }

        impl std::fmt::Display for $name {
            fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
                write!(f, "{}", self.0)
            }
        }
    };
}

newtype!(UserId, u64);
newtype!(Email, String);
```

### Enum Dispatch

```rust
macro_rules! dispatch {
    ($self:expr, $method:ident $(, $arg:expr)*) => {
        match $self {
            Shape::Circle(inner) => inner.$method($($arg),*),
            Shape::Rectangle(inner) => inner.$method($($arg),*),
            Shape::Triangle(inner) => inner.$method($($arg),*),
        }
    };
}

impl Shape {
    fn area(&self) -> f64 {
        dispatch!(self, area)
    }
}
```

## Exporting Macros

```rust
// From a library crate
#[macro_export]
macro_rules! my_macro {
    () => { ... };
}

// Users import with: use my_crate::my_macro;
```

Within a crate, macros are available in the order they're defined (top-to-bottom
within a file, declaration order across modules).

## Hygiene

Rust macros are partially hygienic — identifiers created inside macros
don't conflict with identifiers in the calling scope:

```rust
macro_rules! make_var {
    () => {
        let x = 42;  // this 'x' is in the macro's scope
    };
}

let x = 10;
make_var!();
assert_eq!(x, 10);  // the outer 'x' is unchanged
```

Use `$crate` to refer to the defining crate within exported macros:

```rust
#[macro_export]
macro_rules! my_macro {
    () => {
        $crate::some_function()  // always resolves to the macro's crate
    };
}
```

## Debugging Macros

```bash
# Expand all macros in a file
cargo expand src/main.rs

# Expand a specific item
cargo expand my_module::MyStruct
```

Enable trace in nightly:
```rust
#![feature(trace_macros)]
trace_macros!(true);
my_macro!(1, 2, 3);
trace_macros!(false);
```
