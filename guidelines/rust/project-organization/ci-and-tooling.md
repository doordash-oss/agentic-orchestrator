# CI and Tooling

## Essential CI Steps

Every Rust project CI should run these:

```bash
# Format check — zero tolerance
cargo fmt --all -- --check

# Lint — treat warnings as errors
cargo clippy --all-targets --all-features -- -D warnings

# Test — with race detector
cargo test --all-features

# Security audit
cargo audit
cargo deny check
```

## rustfmt

Format all Rust code consistently. Configure in `rustfmt.toml`:

```toml
# rustfmt.toml
edition = "2021"
max_width = 100
use_field_init_shorthand = true
```

**Rule**: run `cargo fmt` before every commit. Never argue about formatting.

## Clippy

Clippy catches common mistakes, performance issues, and style violations:

```bash
# Default lints
cargo clippy

# Strict: deny all warnings
cargo clippy -- -D warnings

# Pedantic: extra strict (good for libraries)
cargo clippy -- -W clippy::pedantic
```

### Configuring Clippy

In `Cargo.toml` or at the crate root:

```rust
// src/lib.rs
#![warn(clippy::pedantic)]
#![allow(clippy::module_name_repetitions)]  // with justification
```

### Suppressing Lints

Always include a justification:

```rust
#[allow(clippy::cast_possible_truncation)]
// Safe: value is guaranteed to be in u32 range by prior validation
fn as_u32(value: u64) -> u32 {
    value as u32
}
```

## cargo-deny

Check licenses, vulnerabilities, and duplicate dependencies:

```toml
# deny.toml
[advisories]
vulnerability = "deny"
unmaintained = "warn"

[licenses]
allow = ["MIT", "Apache-2.0", "BSD-2-Clause", "BSD-3-Clause", "ISC"]

[bans]
multiple-versions = "warn"  # flag duplicate dependency versions
```

## cargo-audit

```bash
cargo audit                   # check against RustSec advisory database
cargo audit fix               # auto-update vulnerable dependencies
```

## cargo-udeps

Find unused dependencies:

```bash
cargo install cargo-udeps
cargo +nightly udeps          # requires nightly
```

## Miri — Undefined Behavior Detection

```bash
rustup +nightly component add miri
cargo +nightly miri test      # detect UB in unsafe code
```

Use for code with `unsafe` blocks.

## cargo-expand

Inspect macro expansion:

```bash
cargo install cargo-expand
cargo expand my_module        # show expanded code
```

## Complete CI Configuration (GitHub Actions)

```yaml
name: CI
on: [push, pull_request]

jobs:
  check:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: dtolnay/rust-toolchain@stable
        with:
          components: rustfmt, clippy

      - name: Format
        run: cargo fmt --all -- --check

      - name: Clippy
        run: cargo clippy --all-targets --all-features -- -D warnings

      - name: Test
        run: cargo test --all-features

      - name: Doc tests
        run: cargo test --doc

  security:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: dtolnay/rust-toolchain@stable
      - run: cargo install cargo-audit cargo-deny
      - run: cargo audit
      - run: cargo deny check
```

## Pre-commit Hook

```bash
#!/bin/sh
# .git/hooks/pre-commit
cargo fmt --all -- --check || exit 1
cargo clippy -- -D warnings || exit 1
```

## Static Analysis Summary

| Tool | Purpose | When to Run |
|------|---------|-------------|
| `cargo fmt` | Code formatting | Every commit |
| `cargo clippy` | Lint and style | Every commit |
| `cargo test` | Unit + integration tests | Every commit |
| `cargo audit` | Vulnerability check | Every CI run |
| `cargo deny` | License + vulnerability + duplicates | Every CI run |
| `cargo udeps` | Unused dependencies | Periodically |
| `cargo miri` | Undefined behavior | On unsafe code changes |
| `cargo doc` | Documentation generation | Before release |
