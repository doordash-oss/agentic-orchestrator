# Coroutines (C++20)

## The Three Keywords

A function becomes a coroutine if it uses any of these:

| Keyword | Meaning |
|---------|---------|
| `co_await expr` | Suspend until `expr` completes; return its result |
| `co_yield value` | Produce `value` to the caller, then suspend |
| `co_return value` | Complete the coroutine with a final result |

C++20 defines the coroutine machinery (`promise_type`, `coroutine_handle`,
awaitables) but provides no ready-made types. Use a library (cppcoro) or write
your own `task<T>`, `generator<T>`, etc.

## Generator Pattern with `co_yield`

A generator produces a lazy sequence of values synchronously:

```cpp
template<typename T>
struct Generator {
    struct promise_type {
        T current_value;
        Generator get_return_object() {
            return Generator{std::coroutine_handle<promise_type>::from_promise(*this)};
        }
        std::suspend_always initial_suspend() { return {}; }
        std::suspend_always final_suspend() noexcept { return {}; }
        std::suspend_always yield_value(T value) {
            current_value = std::move(value);
            return {};
        }
        void return_void() {}
        void unhandled_exception() { std::terminate(); }
    };

    std::coroutine_handle<promise_type> handle;
    explicit Generator(std::coroutine_handle<promise_type> h) : handle(h) {}
    ~Generator() { if (handle) handle.destroy(); }

    Generator(const Generator&) = delete;
    Generator& operator=(const Generator&) = delete;

    bool next() { handle.resume(); return !handle.done(); }
    T value() { return handle.promise().current_value; }
};

Generator<uint64_t> fibonacci() {
    uint64_t a = 0, b = 1;
    while (true) {
        co_yield a;
        auto next = a + b;
        a = b;
        b = next;
    }
}
```

### Do Not Use Capturing Lambdas as Coroutines (CP.51)

```cpp
// WRONG: Captured reference dangles at suspension
auto bad = [&local_var]() -> Generator<int> {
    co_yield local_var;  // local_var may be gone
};

// CORRECT: Capture by value
auto good = [copy = local_var]() -> Generator<int> {
    co_yield copy;
};
```

## Async Task Pattern with `co_await`

### Do Not Hold Locks Across Suspension Points (CP.52)

```cpp
// WRONG: Lock held across suspension — may resume on different thread
Task<void> bad() {
    std::lock_guard lock(mtx);
    co_await some_async_io();  // UB: unlock on wrong thread
}

// CORRECT: Release lock before suspension
Task<void> good() {
    {
        std::lock_guard lock(mtx);
        prepare_data();
    }  // Lock released
    co_await some_async_io();  // Safe
}
```

### Pass Parameters by Value (CP.53)

```cpp
// WRONG: Reference dangles at suspension
Task<void> bad(const std::string& name) {
    co_await async_op();
    use(name);  // name may be dangling
}

// CORRECT: Take by value
Task<void> good(std::string name) {
    co_await async_op();
    use(name);  // Safe: owns the data
}
```

## Using cppcoro

[cppcoro](https://github.com/andreasbuhr/cppcoro) provides production-quality
coroutine types:

```cpp
#include <cppcoro/task.hpp>
#include <cppcoro/sync_wait.hpp>
#include <cppcoro/when_all.hpp>

cppcoro::task<int> compute(int x) { co_return x * x; }

cppcoro::task<int> composed() {
    auto [a, b] = co_await cppcoro::when_all(compute(3), compute(4));
    co_return a + b;  // 25
}

int main() {
    return cppcoro::sync_wait(composed());
}
```

## When to Use Coroutines vs Threads

| Scenario | Recommendation |
|----------|---------------|
| I/O-bound (network, file, database) | Coroutines — suspend while waiting |
| CPU-bound computation | Threads — parallel execution on multiple cores |
| Thousands of concurrent operations | Coroutines — KB-sized frames vs MB-per-thread |
| Complex async flows with multiple I/O steps | Coroutines — linear code that reads synchronously |
| Mixed I/O + CPU work | Hybrid: coroutines for I/O, thread pool for CPU |

## Coroutine Limitations

- Cannot use variadic arguments, plain `return`, or `auto` return types
- Constructors, destructors, `main()`, and `constexpr`/`consteval` functions cannot be coroutines
- C++20 provides only machinery — use a library for ready-made types
- Debugging is harder: call stacks are non-linear across suspension points
