# Optimization Patterns

## Small Buffer Optimization (SBO/SSO)

SBO stores small objects inline, avoiding heap allocation. Used by:
- `std::string` (SSO): 15-23 chars inline depending on implementation
- `std::function`: typically 16-24 bytes inline
- `std::any`: implementation-defined

Strings just over the SSO threshold are the most expensive — one allocation
plus a cache miss. Prefer short string keys where possible.

## `std::string_view` for Zero-Copy Access

`std::string_view` (C++17) is a non-owning reference to a character sequence:

```cpp
// Forces allocation for string literals
void log_bad(const std::string& msg);

// Zero allocation for any string-like argument
void log_good(std::string_view msg) noexcept;

// Zero-copy substring
std::string data = "key=value;next=123";
std::string_view view{data};
auto key = view.substr(0, view.find('='));  // No allocation
```

**Danger**: `string_view` does NOT own memory. Never return a `string_view`
to a local `std::string`.

## `reserve` to Eliminate Reallocation

```cpp
// NAIVE: log2(n) reallocations
std::vector<int> v;
for (int i = 0; i < n; ++i) v.push_back(i);

// OPTIMIZED: single allocation
std::vector<int> v;
v.reserve(n);
for (int i = 0; i < n; ++i) v.push_back(i);

// For incrementally built strings
std::size_t total = 0;
for (const auto& p : parts) total += p.size();
std::string result;
result.reserve(total);
for (const auto& p : parts) result += p;
```

## Avoiding Unnecessary Copies

```cpp
// PITFALL: auto deduces value type — copies the vector
for (auto entry : registry) process(entry.second);  // Copies!

// CORRECT: const reference
for (const auto& [key, values] : registry) process(values);

// Lambda captures
auto bad  = [expensive_obj]() { use(expensive_obj); };          // Copies
auto good = [&expensive_obj]() { use(expensive_obj); };         // Reference
auto move = [obj = std::move(expensive_obj)]() { use(obj); };   // Moves
```

## `[[likely]]` / `[[unlikely]]` (C++20)

Static branch prediction hints. Affect code layout, not runtime prediction.
Use only when PGO is unavailable and profiling confirms a bias:

```cpp
if (ptr == nullptr) [[unlikely]] {
    throw std::invalid_argument("null ptr");
}
// Hot path continues directly below
```

## Link-Time Optimization and Profile-Guided Optimization

| Technique | Typical Gain | Complexity |
|-----------|-------------|------------|
| `[[likely]]`/`[[unlikely]]` | 1-5% on annotated branches | Low |
| LTO (`-flto`) | 5-15% whole-program | Low (compiler flag) |
| PGO | 10-20% whole-program | Medium (extra build step) |

```bash
# Enable LTO for all release builds
g++ -O2 -flto app.cpp -o app

# PGO workflow
g++ -O2 -fprofile-generate -o app_instr app.cpp
./app_instr < representative_input.txt
g++ -O2 -fprofile-use -o app_opt app.cpp
```
