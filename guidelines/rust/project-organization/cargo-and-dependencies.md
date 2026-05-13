# Cargo and Dependencies

## Cargo.toml Best Practices

```toml
[package]
name = "my-crate"
version = "0.1.0"
edition = "2021"
rust-version = "1.75"  # MSRV — minimum supported Rust version
description = "A brief description"
license = "MIT OR Apache-2.0"
repository = "https://github.com/org/repo"
keywords = ["async", "http"]
categories = ["web-programming"]

[dependencies]
tokio = { version = "1", features = ["full"] }
serde = { version = "1", features = ["derive"] }

[dev-dependencies]
criterion = { version = "0.5", features = ["html_reports"] }
tempfile = "3"
```

## Workspaces

For multi-crate projects, use a workspace:

```toml
# Cargo.toml (workspace root)
[workspace]
members = [
    "crates/core",
    "crates/cli",
    "crates/server",
]
resolver = "2"

[workspace.package]
version = "0.1.0"
edition = "2021"
license = "MIT"

[workspace.dependencies]
tokio = { version = "1", features = ["full"] }
serde = { version = "1", features = ["derive"] }
anyhow = "1"
```

Member crates inherit workspace dependencies:

```toml
# crates/core/Cargo.toml
[package]
name = "my-core"
version.workspace = true
edition.workspace = true

[dependencies]
serde.workspace = true
anyhow.workspace = true
```

**Benefits**: unified `Cargo.lock`, shared dependency versions, single
`cargo test --workspace` command.

## Feature Flags

Features must be **additive** — enabling a feature should never remove
functionality:

```toml
[features]
default = ["json"]
json = ["dep:serde_json"]
yaml = ["dep:serde_yaml"]
full = ["json", "yaml"]

[dependencies]
serde = "1"
serde_json = { version = "1", optional = true }
serde_yaml = { version = "0.9", optional = true }
```

In code:

```rust
#[cfg(feature = "json")]
pub fn to_json<T: Serialize>(value: &T) -> Result<String> {
    serde_json::to_string(value).map_err(Into::into)
}
```

### Feature Naming Conventions (C-FEATURE)

- Name features directly: `std`, `json`, `async`
- **Don't** use prefixes like `use-` or `with-`: not `use-serde`, just `serde`
- Keep default features minimal — users can always opt in

## Dependency Management

### Version Requirements

```toml
# For applications: exact or tight ranges
tokio = "=1.35.0"           # exact
serde = ">=1.0, <2.0"       # range

# For libraries: use semver ranges (default caret)
tokio = "1"                  # ^1.0.0 — compatible updates
```

### Cargo.lock

- **Commit for binaries** — reproducible builds
- **Don't commit for libraries** — let downstream resolve versions

### Security Auditing

```bash
cargo install cargo-audit
cargo audit                  # check for known vulnerabilities

cargo install cargo-deny
cargo deny check             # licenses + vulnerabilities + duplicates
```

### Minimizing Dependencies

- Check what a dependency pulls in: `cargo tree -d` (duplicates)
- Prefer crates with few transitive dependencies
- Use feature flags to avoid pulling in unused functionality
- Consider `cargo-udeps` to find unused dependencies

## Build Scripts (build.rs)

Use sparingly — for code generation, linking C libraries, or embedding data:

```rust
// build.rs
fn main() {
    println!("cargo:rerun-if-changed=proto/service.proto");
    tonic_build::compile_protos("proto/service.proto").unwrap();
}
```

**Rules**:
- Always set `cargo:rerun-if-changed` to avoid rebuilding unnecessarily
- Don't do heavy computation — it runs on every build
- Prefer proc macros over build scripts when possible

## Release Process

```toml
# Cargo.toml
[package]
version = "0.2.0"  # bump according to semver

# Profile for production
[profile.release]
lto = true          # link-time optimization
codegen-units = 1   # max optimization (slower compile)
strip = true        # strip debug symbols
```

Use `cargo-release` for automated versioning and publishing:

```bash
cargo install cargo-release
cargo release patch --execute  # 0.1.0 → 0.1.1
cargo release minor --execute  # 0.1.0 → 0.2.0
```
