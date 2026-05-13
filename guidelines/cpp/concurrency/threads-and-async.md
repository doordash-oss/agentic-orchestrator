# Threads and Async

## `std::thread` — The Low-Level Primitive

`std::thread` is the foundational threading primitive, but it has an inherent
footgun: if you destroy a joinable thread — by forgetting `join()` or because an
exception unwinds the stack — the runtime calls `std::terminate()`.

```cpp
// DANGEROUS: Exception causes terminate()
void dangerous() {
    std::thread t(do_work);
    might_throw();  // Exception unwinds, t destroyed while joinable
    t.join();       // Never reached — std::terminate() called
}
```

Always prefer `std::jthread` (C++20) or wrap `std::thread` in RAII.

## `std::jthread` (C++20) — Prefer Over `std::thread`

`std::jthread` adds automatic joining in the destructor and cooperative
cancellation via `std::stop_token`. Always prefer it in C++20+ code
(Core Guidelines CP.25).

```cpp
void worker(std::stop_token stoken) {
    while (!stoken.stop_requested()) {
        // Do incremental work
        std::this_thread::sleep_for(std::chrono::milliseconds(100));
    }
    // Cleanup here
}

int main() {
    std::jthread t(worker);  // stop_token injected automatically
    std::this_thread::sleep_for(std::chrono::seconds(1));
    // Destructor calls request_stop() then join()
}
```

### Integration with `std::condition_variable_any`

Waiting threads can be woken by either a notification or a stop request:

```cpp
void worker(std::stop_token stoken, std::queue<int>& queue, std::mutex& mtx) {
    std::unique_lock lock(mtx);
    std::condition_variable_any cv;
    while (!stoken.stop_requested()) {
        cv.wait(lock, stoken, [&]{ return !queue.empty(); });
        if (!queue.empty()) {
            process(queue.front());
            queue.pop();
        }
    }
}
```

### `std::stop_callback` for Cleanup Registration

```cpp
std::jthread t([](std::stop_token tok) {
    std::stop_callback cb(tok, []{
        // Close file handles, cancel I/O, etc.
    });
    while (!tok.stop_requested()) { /* work */ }
});
```

## `std::async` and `std::future`

### Always Specify `std::launch::async`

The default launch policy is implementation-defined (`async | deferred`). A
deferred future never runs until `.get()` is called, which can cause hangs:

```cpp
// WRONG: May be deferred — loops forever waiting
auto f = std::async(compute_something);
while (f.wait_for(10ms) != std::future_status::ready) {
    do_other_work();  // Never exits if deferred
}

// CORRECT: Forces a new thread
auto f = std::async(std::launch::async, compute_something);
```

### The Blocking Destructor Pitfall

A `std::future` from `std::async` blocks in its destructor:

```cpp
// WRONG: Temporary future — destructor blocks immediately
std::async(std::launch::async, long_task);  // Blocks here!

// CORRECT: Store the future
auto f = std::async(std::launch::async, long_task);
do_other_work();
f.get();  // Block intentionally
```

### When to Use `std::async` vs a Thread Pool

- **`std::async`**: Small-scale parallelism, one-off tasks, exception propagation needed
- **Thread pool**: High-frequency task submission, production systems, tight latency requirements

## Thread Pools

The standard library provides no thread pool. For production code, use a
library or implement one. Size CPU-bound pools to `std::thread::hardware_concurrency()`.

```cpp
class ThreadPool {
public:
    explicit ThreadPool(std::size_t n_threads) {
        for (std::size_t i = 0; i < n_threads; ++i) {
            workers.emplace_back([this] {
                for (;;) {
                    std::function<void()> task;
                    {
                        std::unique_lock lock(mutex);
                        cv.wait(lock, [this] { return stop || !tasks.empty(); });
                        if (stop && tasks.empty()) return;
                        task = std::move(tasks.front());
                        tasks.pop();
                    }
                    task();
                }
            });
        }
    }

    template<typename F>
    auto submit(F&& f) -> std::future<std::invoke_result_t<F>> {
        using R = std::invoke_result_t<F>;
        auto task = std::make_shared<std::packaged_task<R()>>(std::forward<F>(f));
        auto result = task->get_future();
        {
            std::lock_guard lock(mutex);
            tasks.emplace([task] { (*task)(); });
        }
        cv.notify_one();
        return result;
    }

    ~ThreadPool() {
        { std::lock_guard lock(mutex); stop = true; }
        cv.notify_all();
        for (auto& w : workers) w.join();
    }

private:
    std::vector<std::thread>          workers;
    std::queue<std::function<void()>> tasks;
    std::mutex                        mutex;
    std::condition_variable           cv;
    bool                              stop{false};
};
```

## Detached Threads — Almost Always Wrong

Core Guidelines CP.26: Do not `detach()` a thread. Once detached, you lose
lifetime control. Detached threads accessing objects with static storage
duration create data races during program shutdown.

```cpp
// WRONG: Detached thread can outlive its data
void bad() {
    std::string data = "important";
    std::thread([&data] { use(data); }).detach();  // data may be destroyed
}

// CORRECT: Use jthread with proper lifetime, or a thread pool
```

## Thread-Local Storage

`thread_local` gives each thread its own instance, eliminating synchronization:

```cpp
thread_local char log_buffer[4096];
thread_local int call_depth = 0;
```

**Good for**: per-thread caches, buffers, RNG state.

**Pitfalls**:
- Non-trivial destructors can cause subtle lifecycle bugs (destroyed when thread exits)
- Avoid with `std::async` on MSVC (thread pool reuses threads, TLS state bleeds)
- Never share the address of a `thread_local` variable with another thread
