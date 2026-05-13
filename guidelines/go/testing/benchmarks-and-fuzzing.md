# Benchmarks and Fuzzing

## Benchmarks (testing.B)

### Go 1.24+ Style (Preferred)

```go
func BenchmarkSort(b *testing.B) {
    data := generateData()
    for b.Loop() {
        sort.Ints(data)
    }
}
```

`b.Loop()` runs exactly once per `-count`, prevents compiler optimization of
the loop body, and automatically manages the timer.

### Traditional Style

```go
func BenchmarkSort(b *testing.B) {
    for i := 0; i < b.N; i++ {
        sort.Ints(generateData())
    }
}
```

### Running Benchmarks

```bash
go test -bench=. -benchmem ./...          # all benchmarks with memory
go test -bench=BenchmarkSort -count=5     # 5 runs for stability
go test -bench=. -benchtime=3s            # longer sampling
go test -bench=. -cpuprofile=cpu.out      # with CPU profiling
```

### Excluding Setup from Timing

```go
func BenchmarkExpensive(b *testing.B) {
    data := expensiveSetup()
    b.ResetTimer() // exclude setup
    for b.Loop() {
        process(data)
    }
}
```

### Sub-Benchmarks

```go
func BenchmarkEncode(b *testing.B) {
    sizes := []int{1, 100, 10000}
    for _, size := range sizes {
        b.Run(fmt.Sprintf("size=%d", size), func(b *testing.B) {
            data := make([]byte, size)
            for b.Loop() {
                encode(data)
            }
        })
    }
}
```

### Parallel Benchmarks

```go
func BenchmarkParallel(b *testing.B) {
    b.RunParallel(func(pb *testing.PB) {
        for pb.Next() {
            doWork()
        }
    })
}
```

### Custom Metrics

```go
b.ReportMetric(float64(ops)/elapsed.Seconds(), "ops/sec")
b.ReportMetric(float64(allocBytes)/float64(b.N), "alloc-bytes/op")
```

## Fuzz Testing (testing.F, Go 1.18+)

Coverage-guided fuzzing finds edge cases automatically:

```go
func FuzzParseURL(f *testing.F) {
    // Seed corpus — known good inputs
    f.Add("https://example.com/path?q=1")
    f.Add("")
    f.Add("://invalid")

    f.Fuzz(func(t *testing.T, input string) {
        u, err := url.Parse(input)
        if err != nil {
            return // invalid input is expected
        }
        // Property: re-parsing should not fail
        _, err = url.Parse(u.String())
        if err != nil {
            t.Errorf("round-trip failed: Parse(%q).String() = %q, re-parse error: %v",
                input, u.String(), err)
        }
    })
}
```

### Running Fuzz Tests

```bash
go test                              # runs seed corpus only (CI-safe)
go test -fuzz=FuzzParseURL           # starts fuzzing
go test -fuzz=. -fuzztime=60s        # fuzz for 60 seconds
```

Failing inputs are saved to `testdata/fuzz/FuzzXxx/` and become permanent
regression tests.

### Fuzz Target Rules

- Must be fast, deterministic, and stateless
- Allowed parameter types: `[]byte`, `string`, `bool`, `byte`, `rune`,
  all integer sizes, `float32`, `float64`
- Exactly one `f.Fuzz` call per `FuzzXxx` function

## Race Detection

```bash
go test -race ./...    # run tests with race detector
go build -race ./...   # build binary with race detector
```

- **Zero false positives** — every report is a real data race
- 5-10x CPU and memory overhead — not for production
- Only detects races in code paths that actually execute
- **Always run in CI**: `go test -race ./...`

## Test Coverage

```bash
go test -cover ./...                    # print coverage %
go test -coverprofile=cover.out ./...   # write profile
go tool cover -html=cover.out           # interactive HTML report
go tool cover -func=cover.out           # per-function breakdown
```

Coverage modes:
- `set` — did each statement execute? (default, ~3% overhead)
- `count` — how many times? (heat maps)
- `atomic` — precise count for parallel code (highest cost)

**100% coverage is not the goal** — meaningful coverage of critical paths is.
Coverage is a tool for finding untested branches, not a proxy for test quality.

### Integration Test Coverage (Go 1.20+)

```bash
go build -cover -o myapp .
GOCOVERDIR=covdata ./myapp
go tool covdata percent -i=covdata
```
