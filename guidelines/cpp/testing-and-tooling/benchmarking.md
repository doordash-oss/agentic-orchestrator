# Benchmarking

## Google Benchmark

```cpp
#include <benchmark/benchmark.h>

static void BM_StringCreation(benchmark::State& state) {
    for (auto _ : state) {
        std::string s("hello");
        benchmark::DoNotOptimize(s);
    }
}
BENCHMARK(BM_StringCreation);
BENCHMARK_MAIN();
```

### Parameterized Benchmarks

```cpp
static void BM_VectorPush(benchmark::State& state) {
    for (auto _ : state) {
        std::vector<int> v;
        v.reserve(state.range(0));
        for (int i = 0; i < state.range(0); ++i) v.push_back(i);
        benchmark::ClobberMemory();
    }
    state.SetItemsProcessed(state.iterations() * state.range(0));
}
BENCHMARK(BM_VectorPush)->Range(8, 8<<10);
```

### Preventing Optimizer Defeat

- **`DoNotOptimize(x)`**: forces `x` to be stored in a register/memory
- **`ClobberMemory()`**: forces all pending writes to be committed
- Use `state.range(0)` as input — not compile-time constants

```cpp
// BAD: compiler may compute at compile time
benchmark::DoNotOptimize(foo(0));

// GOOD: runtime value
benchmark::DoNotOptimize(foo(state.range(0)));
```

### Complexity Analysis

```cpp
BENCHMARK(BM_Sort)
    ->RangeMultiplier(2)->Range(1<<10, 1<<20)
    ->Complexity(benchmark::oNLogN);
```

## Microbenchmark Pitfalls

1. **Cache effects**: small datasets fit in L1, producing unrealistic results
2. **Branch predictor training**: tight loops train predictors unlike production
3. **CPU frequency scaling**: Turbo Boost varies across iterations
4. **`PauseTiming` overhead**: ~213ns per call; restructure instead of pausing in hot loops
5. **Compiler flag sensitivity**: results can differ 60x between `-O0` and `-O3`
6. **Always build benchmarks in release mode** (`-O2` or `-O3`)

## When to Benchmark vs Profile

| Benchmark when... | Profile when... |
|-------------------|-----------------|
| Preventing regressions in CI | Investigating an observed problem |
| Comparing two implementations | Finding where time is spent |
| Validating complexity claims | Understanding cache/branch behavior |

**Workflow**: profile first to identify hot paths, then write benchmarks to
guide optimization and detect regressions.

### Profiling Tools

| Tool | Platform | Strengths |
|------|----------|-----------|
| `perf` | Linux | Lightweight, hardware counters, flame graphs |
| Intel VTune | Linux/Windows | Microarchitectural analysis, ~5% overhead |
| Instruments | macOS | CPU Time Profiler, Allocations, integrated with Xcode |
| Callgrind | Linux | Instruction-level, very slow (~20x) but accurate |

Build for profiling: `-O2 -g` (optimizations + debug symbols).
