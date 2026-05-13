# Module System

## File Layout

Rust's module system maps to the file system:

```
src/
├── lib.rs           # crate root, declares modules
├── config.rs        # mod config
├── error.rs         # mod error
├── server/          # mod server (directory form)
│   ├── mod.rs       # module root
│   ├── handler.rs   # server::handler
│   └── middleware.rs # server::middleware
```

Declare modules in the parent:

```rust
// src/lib.rs
pub mod config;      // loads from src/config.rs
pub mod error;       // loads from src/error.rs
pub mod server;      // loads from src/server/mod.rs
```

## Visibility

| Modifier | Scope |
|----------|-------|
| (none) | Private to current module and its children |
| `pub` | Public to everyone |
| `pub(crate)` | Public within the crate only |
| `pub(super)` | Public to the parent module |
| `pub(in path)` | Public to a specific ancestor module |

```rust
pub struct Server {
    pub(crate) config: Config,  // visible within crate, not to users
    address: String,            // private
}
```

**Best practice**: default to private. Use `pub(crate)` for internal sharing.
Use `pub` only for your actual public API.

## Re-exports for Clean APIs

Use `pub use` to create a flat, discoverable public surface:

```rust
// src/lib.rs
mod config;
mod error;
mod server;

// Re-export the public API
pub use config::Config;
pub use error::Error;
pub use server::Server;

// Users write:
use my_crate::{Config, Error, Server};
// Instead of:
use my_crate::config::Config;
use my_crate::error::Error;
```

## Prelude Pattern

For crates with many commonly-used items:

```rust
// src/prelude.rs
pub use crate::Config;
pub use crate::Error;
pub use crate::Result;
pub use crate::traits::{Serialize, Deserialize};

// Users can import everything at once:
use my_crate::prelude::*;
```

Use sparingly — preludes that export too many names cause confusion.

## Module-Level Documentation

```rust
//! # Server Module
//!
//! Handles HTTP request routing and middleware.
//! See [`Server::new`] for configuration options.

pub struct Server { ... }
```

The `//!` syntax documents the module itself (shown at the top of the
module's docs page).

## Binary + Library Pattern

A common pattern is to have both `lib.rs` and `main.rs`:

```rust
// src/lib.rs — all the logic
pub mod config;
pub mod server;
pub mod error;

pub fn run(config: Config) -> Result<()> { ... }

// src/main.rs — thin wrapper
use my_crate::{Config, run};

fn main() -> anyhow::Result<()> {
    let config = Config::from_env()?;
    run(config)
}
```

This lets integration tests use the library API directly.

## Conditional Compilation

```rust
// Platform-specific code
#[cfg(target_os = "linux")]
mod linux;

#[cfg(target_os = "macos")]
mod macos;

// Feature-gated modules
#[cfg(feature = "serde")]
mod serialization;

// Conditional attributes
#[cfg_attr(feature = "serde", derive(Serialize, Deserialize))]
pub struct Config { ... }
```

## Common Module Layout for Applications

```
src/
├── main.rs
├── lib.rs           # re-exports, app-level types
├── config.rs        # configuration loading
├── error.rs         # error types
├── cli.rs           # clap argument definitions
├── routes/          # HTTP handlers
│   ├── mod.rs
│   ├── health.rs
│   └── users.rs
├── models/          # domain types
│   ├── mod.rs
│   └── user.rs
├── services/        # business logic
│   ├── mod.rs
│   └── auth.rs
└── db/              # database access
    ├── mod.rs
    └── queries.rs
```

## Anti-Patterns

```rust
// Bad: wildcard re-exports leak everything
pub use internal_module::*;

// Good: explicit re-exports
pub use internal_module::{PublicType, PublicFn};

// Bad: deeply nested public paths
use my_crate::server::http::router::v2::handler::UserHandler;

// Good: re-export at a reasonable depth
pub use server::UserHandler;  // in lib.rs
```
