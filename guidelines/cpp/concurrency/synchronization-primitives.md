# Synchronization Primitives

## Choosing the Right Mutex

| Type | Use Case |
|------|----------|
| `std::mutex` | General-purpose exclusive access |
| `std::shared_mutex` | Reader-writer pattern (many readers, few writers) |
| `std::recursive_mutex` | Same thread may re-enter (usually a design smell) |
| `std::timed_mutex` | Need to avoid blocking indefinitely |

### `std::shared_mutex` — Reader-Writer Pattern

```cpp
class ThreadSafeCache {
public:
    std::optional<std::string> get(int key) const {
        std::shared_lock lock(mtx);  // Shared (reader) lock
        auto it = cache.find(key);
        return it != cache.end() ? std::optional{it->second} : std::nullopt;
    }

    void put(int key, std::string value) {
        std::unique_lock lock(mtx);  // Exclusive (writer) lock
        cache[key] = std::move(value);
    }

private:
    mutable std::shared_mutex mtx;
    std::unordered_map<int, std::string> cache;
};
```

### Pair Mutex with Its Data (CP.50)

Declare the mutex adjacent to the data it protects:

```cpp
class BankAccount {
    mutable std::mutex mtx;   // Guards balance
    double balance = 0.0;
public:
    void deposit(double amount) {
        std::lock_guard lock(mtx);
        balance += amount;
    }
};
```

## Lock Guards — Always Use RAII (CP.20)

Never call `lock()`/`unlock()` directly. An exception or early return will
bypass the unlock, causing a deadlock.

| Guard Type | When to Use |
|------------|-------------|
| `std::lock_guard` | Simple exclusive lock, no early unlock needed |
| `std::scoped_lock` | One or multiple mutexes; **prefer in C++17+** |
| `std::unique_lock` | Need manual unlock, deferred lock, or condition variable |
| `std::shared_lock` | Shared (reader) lock for `std::shared_mutex` |

```cpp
// WRONG: Exception-unsafe
void unsafe_transfer(Account& from, Account& to, double amount) {
    from.mtx.lock();
    to.mtx.lock();     // If this throws, from.mtx stays locked
    from.balance -= amount;
    to.balance += amount;
    to.mtx.unlock();
    from.mtx.unlock();
}

// CORRECT: RAII, exception-safe, deadlock-free
void safe_transfer(Account& from, Account& to, double amount) {
    std::scoped_lock lock(from.mtx, to.mtx);  // Locks both atomically
    from.balance -= amount;
    to.balance += amount;
}
```

### Always Name Lock Guards (CP.44)

```cpp
// WRONG: Temporary created and immediately destroyed — no lock held!
std::lock_guard<std::mutex>(mtx);
critical_section();  // NOT protected!

// CORRECT:
std::lock_guard lock(mtx);
critical_section();
```

## Deadlock Avoidance

### Use `std::scoped_lock` for Multiple Mutexes (CP.21)

`std::scoped_lock` uses an internal deadlock-avoidance algorithm. The order
you list mutexes does not matter:

```cpp
// Both threads safe — scoped_lock handles ordering
// Thread A: scoped_lock(mtx_a, mtx_b)
// Thread B: scoped_lock(mtx_b, mtx_a)
std::scoped_lock lock(a.mtx, b.mtx);
```

### Additional Practices (CP.22)

- Never call unknown code (callbacks, virtual functions) while holding a lock
- Keep critical sections as short as possible (CP.43)
- Prefer lock-free for simple atomic operations

## Condition Variables — Always Use a Predicate (CP.42)

Condition variables have two failure modes: **spurious wakeups** (thread wakes
without notification) and **lost wakeups** (notification sent before wait
began). Both are handled by always using a predicate.

```cpp
// WRONG: No predicate — spurious/lost wakeup possible
std::unique_lock lock(mtx);
cv.wait(lock);
process_data();  // May run on invalid state

// CORRECT: Canonical producer-consumer
std::mutex mtx;
std::condition_variable cv;
std::queue<int> work_queue;

// Producer
void produce(int item) {
    {
        std::lock_guard lock(mtx);
        work_queue.push(item);
    }  // Release lock BEFORE notifying
    cv.notify_one();
}

// Consumer
void consume() {
    std::unique_lock lock(mtx);
    cv.wait(lock, [] { return !work_queue.empty(); });
    int item = work_queue.front();
    work_queue.pop();
    lock.unlock();
    process(item);
}
```

### `notify_one` vs `notify_all`

- `notify_one()`: Any single waiter can handle the event
- `notify_all()`: All waiters need to re-check (e.g., shutdown broadcast)

### Timed Wait

```cpp
std::unique_lock lock(mtx);
bool signaled = cv.wait_for(lock, std::chrono::seconds(5),
    [] { return !work_queue.empty(); });
if (!signaled) { /* timeout — no item arrived */ }
```

## `std::call_once` — Thread-Safe Initialization

For lazy initialization that must happen exactly once (CP.110):

```cpp
std::once_flag init_flag;
std::unique_ptr<Database> db;

Database& get_db() {
    std::call_once(init_flag, [] {
        db = std::make_unique<Database>("connection_string");
    });
    return *db;
}

// Even simpler: C++11 guarantees thread-safe static initialization
Database& better_get_db() {
    static Database db("connection_string");
    return db;
}
```

The function-static approach is almost always preferable unless you need
external control of the `once_flag`.
