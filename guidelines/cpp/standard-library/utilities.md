# Utilities

## `std::optional` / `std::variant` / `std::any`

| Type | Use when... |
|------|-------------|
| `optional<T>` | Value may or may not be present; single type |
| `variant<T...>` | Value is one of a fixed set of types (compile-time) |
| `any` | Types unknown at compile time; heterogeneous containers |

See [error-handling/expected-and-optional.md](../error-handling/expected-and-optional.md)
and [type-safety/type-erasure-and-variants.md](../type-safety/type-erasure-and-variants.md)
for detailed patterns.

## `std::tuple` and Structured Bindings

```cpp
auto [count, avg, desc] = getStats(data);

for (const auto& [name, score] : scores)
    std::cout << name << ": " << score << "\n";

// Modify through binding
for (auto& [key, value] : scores) value *= 2;
```

## `std::function`

Type-erasing callable wrapper. Has overhead (virtual call, possible heap alloc):

```cpp
// OK: storing callbacks (not a hot path)
std::unordered_map<std::string, std::function<void(Event)>> handlers;

// For hot paths: use templates instead
template<std::invocable<int> F>
void process(std::vector<int>& v, F fn) {
    for (auto& x : v) x = fn(x);  // Fully inlined
}
```

C++23: `std::move_only_function` for move-only callables (lambdas capturing `unique_ptr`).

## `std::chrono`

```cpp
using namespace std::chrono_literals;

auto d = 5s;
auto d2 = 100ms;
auto ms = std::chrono::duration_cast<std::chrono::milliseconds>(d);

// Measure elapsed time — always use steady_clock
auto start = std::chrono::steady_clock::now();
do_work();
auto elapsed = std::chrono::steady_clock::now() - start;
```

**Rules:**
- `steady_clock` for elapsed time (monotonic, never goes backward)
- `system_clock` for wall clock / calendar display
- Never `high_resolution_clock` for portability

## Random Number Generation

Never use `std::rand()`. Use `<random>`:

```cpp
std::random_device rd;
std::mt19937 gen(rd());
std::uniform_int_distribution<int> dist(1, 100);
int roll = dist(gen);

// Shuffle (replaces deprecated random_shuffle)
std::shuffle(deck.begin(), deck.end(), gen);
```

`mt19937` is NOT cryptographically secure. For security, use OS-level APIs.

## `std::source_location` (C++20)

Replaces `__FILE__`/`__LINE__` macros:

```cpp
void log(std::string_view msg,
         std::source_location loc = std::source_location::current()) {
    std::println("{}:{} {}", loc.file_name(), loc.line(), msg);
}

log("Connected");  // Captures THIS call site
```
