# Lock-Free Programming and Atomics

## Prefer Mutexes First (CP.100)

Lock-free programming is not faster by default. Mutexes can outperform
lock-free code under high contention (spinning wastes CPU cycles). Profile
first, prove the mutex is the bottleneck, then consider lock-free.

**Decision framework:**
1. Multiple variables or complex invariants? Use `std::mutex`
2. Single variable, simple operation (increment, flag, swap)? Use `std::atomic`
3. Proven hot path where mutex contention is profiled bottleneck? Consider CAS-based lock-free

## `std::atomic` — Correct Usage

`std::atomic<T>` guarantees atomic operations (no torn reads/writes) and
provides memory ordering guarantees.

```cpp
// Thread-UNSAFE: data race on non-atomic variable
int counter = 0;
void unsafe() { ++counter; }  // Undefined behavior from multiple threads

// Thread-SAFE: atomic increment
std::atomic<int> counter{0};
void safe() { ++counter; }    // Or: counter.fetch_add(1)

// CAS loop for compound updates
void safe_multiply(std::atomic<int>& val, int factor) {
    int expected = val.load();
    while (!val.compare_exchange_weak(expected, expected * factor)) {
        // expected is updated on failure
    }
}
```

## Memory Orderings

Memory orderings control how atomic operations synchronize with surrounding
non-atomic operations. Choosing the wrong ordering causes subtle,
platform-dependent bugs.

### `memory_order_seq_cst` — Default, Always Correct

Establishes a single total order across all `seq_cst` operations. **Use this
unless profiling proves a cost.** On x86, it compiles to the same code as
acquire-release.

```cpp
std::atomic<bool> flag{false};
int data = 0;

// Thread A
data = 42;
flag.store(true);  // seq_cst by default

// Thread B
if (flag.load()) {  // seq_cst by default
    assert(data == 42);  // Guaranteed
}
```

### `memory_order_release` / `memory_order_acquire`

The publish-subscribe pair. Release on a store ensures prior writes are visible
to any thread that performs an acquire load of the same variable.

```cpp
std::atomic<Result*> published{nullptr};

// Producer
void publish(Result* r) {
    published.store(r, std::memory_order_release);
}

// Consumer
void consume() {
    Result* r;
    while (!(r = published.load(std::memory_order_acquire)))
        std::this_thread::yield();
    use(*r);  // Safe: sees all writes before the release store
}
```

### `memory_order_relaxed` — Atomicity Only

Use only when you need atomicity but not ordering. Classic example: counters
where only the final value matters.

```cpp
std::atomic<int> hit_count{0};
void record_hit() {
    hit_count.fetch_add(1, std::memory_order_relaxed);
}
```

### Summary Table

| Ordering | Used On | Guarantee | Typical Use |
|----------|---------|-----------|-------------|
| `relaxed` | Any | Atomicity only | Counters, stats, reference counts |
| `release` | Store | Prior writes visible to acquirer | Publishing data |
| `acquire` | Load | Sees writes before release store | Consuming published data |
| `acq_rel` | RMW | Both acquire + release | Lock release, CAS in lock-free structures |
| `seq_cst` | Any | Total global order | Default; use when unsure |

## `std::atomic_ref` (C++20)

Applies atomic operations to an existing non-atomic object. Useful for legacy
code and selective atomic access.

```cpp
struct SensorReading {
    int value;  // Plain int in a legacy struct
};

void update(SensorReading& r, int v) {
    std::atomic_ref<int> ref(r.value);
    ref.store(v, std::memory_order_release);
}

int read(const SensorReading& r) {
    std::atomic_ref<const int> ref(r.value);
    return ref.load(std::memory_order_acquire);
}
```

**Rules**: the referenced object must outlive all `atomic_ref` instances; all
concurrent accesses must go through `atomic_ref` — mixing direct and atomic
access is still a data race.

## What Lock-Free is NOT

- Lock-free does not mean "no blocking" — spinning in a CAS loop blocks the core
- Lock-free does not guarantee better throughput under contention
- Lock-free code is not easier to reason about — it requires understanding happens-before graphs
- What works on x86 (strongly ordered) may fail on ARM (weakly ordered) — always use TSan (CP.101)
