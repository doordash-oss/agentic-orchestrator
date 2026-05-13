# Benchmarks and Fuzzing

## Criterion — Statistical Benchmarking

Criterion.rs provides statistically rigorous benchmarks with automatic
regression detection:

```rust
// benches/my_benchmark.rs
use criterion::{criterion_group, criterion_main, Criterion, black_box};

fn bench_sort(c: &mut Criterion) {
    let data: Vec<i32> = (0..1000).rev().collect();

    c.bench_function("sort 1000 elements", |b| {
        b.iter(|| {
            let mut v = data.clone();
            v.sort();
            black_box(v)  // prevent optimizer from eliminating the work
        })
    });
}

fn bench_parse(c: &mut Criterion) {
    c.bench_function("parse config", |b| {
        b.iter(|| {
            black_box(Config::parse(black_box(SAMPLE_CONFIG)))
        })
    });
}

criterion_group!(benches, bench_sort, bench_parse);
criterion_main!(benches);
```

### Cargo.toml Configuration

```toml
[dev-dependencies]
criterion = { version = "0.5", features = ["html_reports"] }

[[bench]]
name = "my_benchmark"
harness = false
```

### Benchmark Groups for Comparison

```rust
fn bench_serialization(c: &mut Criterion) {
    let mut group = c.benchmark_group("serialization");
    let data = make_test_data();

    group.bench_function("json", |b| {
        b.iter(|| serde_json::to_string(&data))
    });
    group.bench_function("bincode", |b| {
        b.iter(|| bincode::serialize(&data))
    });
    group.bench_function("msgpack", |b| {
        b.iter(|| rmp_serde::to_vec(&data))
    });

    group.finish();
}
```

### Parameterized Benchmarks

```rust
fn bench_vec_push(c: &mut Criterion) {
    let mut group = c.benchmark_group("vec_push");

    for size in [100, 1000, 10000] {
        group.bench_with_input(
            BenchmarkId::from_parameter(size),
            &size,
            |b, &size| {
                b.iter(|| {
                    let mut v = Vec::new();
                    for i in 0..size {
                        v.push(i);
                    }
                    black_box(v)
                })
            },
        );
    }
    group.finish();
}
```

Run benchmarks: `cargo bench`

## Fuzzing with cargo-fuzz

Find crashes, panics, and undefined behavior through automated random input
generation:

```bash
cargo install cargo-fuzz
cargo fuzz init
cargo fuzz add my_target
```

```rust
// fuzz/fuzz_targets/my_target.rs
#![no_main]
use libfuzzer_sys::fuzz_target;

fuzz_target!(|data: &[u8]| {
    if let Ok(s) = std::str::from_utf8(data) {
        let _ = my_crate::parse(s);  // should never panic
    }
});
```

### Structured Fuzzing with Arbitrary

Generate structured inputs instead of raw bytes:

```rust
use libfuzzer_sys::fuzz_target;
use arbitrary::Arbitrary;

#[derive(Arbitrary, Debug)]
struct FuzzInput {
    name: String,
    port: u16,
    retries: u8,
}

fuzz_target!(|input: FuzzInput| {
    let _ = Config::validate(&input.name, input.port, input.retries);
});
```

Run: `cargo fuzz run my_target -- -max_len=4096`

## Code Coverage

### cargo-llvm-cov

```bash
cargo install cargo-llvm-cov
cargo llvm-cov               # run tests and show coverage
cargo llvm-cov --html        # generate HTML report
cargo llvm-cov --lcov --output-path lcov.info  # for CI integration
```

### cargo-tarpaulin (Linux)

```bash
cargo install cargo-tarpaulin
cargo tarpaulin --out html
```

## CI Pipeline Configuration

```yaml
# .github/workflows/ci.yml
- name: Test
  run: cargo test --all-features

- name: Benchmark (check for regressions)
  run: cargo bench -- --output-format bencher | tee bench.txt

- name: Coverage
  run: |
    cargo llvm-cov --lcov --output-path lcov.info
    # upload to Codecov or similar
```

## Benchmarking Best Practices

- **Use `black_box()`** to prevent the optimizer from removing benchmarked code
- **Benchmark realistic workloads** — micro-benchmarks can be misleading
- **Run benchmarks in `--release` mode** (Criterion does this by default)
- **Compare against baselines** — Criterion auto-detects regressions
- **Don't benchmark in CI** unless you have dedicated, consistent hardware
- **Profile before optimizing** — use `cargo flamegraph` to find hotspots
