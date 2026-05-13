# noexcept and Contracts

## When to Use `noexcept`

### Mandatory Sites

```cpp
// 1. Move constructors and move assignment — CRITICAL
Buffer(Buffer&& other) noexcept
    : data_(std::exchange(other.data_, nullptr)) {}

// 2. swap functions
void swap(Buffer& a, Buffer& b) noexcept;

// 3. Destructors (implicit since C++11, but be explicit)
~Buffer() noexcept { delete[] data_; }

// 4. Leaf functions that genuinely cannot fail
int size() const noexcept { return size_; }
```

### The vector Reallocation Problem

`std::vector` uses `move_if_noexcept` during reallocation. Without `noexcept`
on the move constructor, vector **copies** instead of moves — up to 84x slower:

```cpp
// WRONG: vector will COPY during reallocation
struct Bad {
    std::vector<HeavyData> data_;
    Bad(Bad&& other) { data_ = std::move(other.data_); }  // No noexcept!
};

// CORRECT: vector will MOVE during reallocation
struct Good {
    std::vector<HeavyData> data_;
    Good(Good&& other) noexcept : data_(std::move(other.data_)) {}
};
```

### Conditional `noexcept` for Generic Code

```cpp
template <typename T>
void swap_elements(T& a, T& b) noexcept(
    std::is_nothrow_move_constructible_v<T> &&
    std::is_nothrow_move_assignable_v<T>
) {
    T temp = std::move(a);
    a = std::move(b);
    b = std::move(temp);
}
```

### Pitfalls

```cpp
// TRAP: noexcept calls std::terminate if anything throws
void dangerous() noexcept {
    vec.push_back(value);  // Can throw bad_alloc -> terminate!
}

// TRAP: Virtual noexcept constrains ALL overrides
class Base {
    virtual void process() noexcept;  // All overrides must be noexcept
};
```

## assert vs Exceptions vs Contracts

| Mechanism | For | When Violated |
|-----------|-----|---------------|
| `assert` | Programmer bugs (invariants, preconditions) | Crash in debug; gone in release |
| Exceptions | Runtime errors (resources, input, network) | Throw, propagate, catch, recover |
| Contracts (C++26) | API contracts between caller/callee | Configurable: enforce/observe/ignore |

```cpp
// assert: programmer errors
int get(const std::vector<int>& v, size_t i) {
    assert(i < v.size() && "Index out of range — programmer bug");
    return v[i];
}

// Exceptions: runtime errors caller handles
int parsePort(std::string_view s) {
    int port;
    auto [ptr, ec] = std::from_chars(s.begin(), s.end(), port);
    if (ec != std::errc{}) throw std::invalid_argument("Invalid port");
    return port;
}

// static_assert: compile-time invariants (zero runtime cost)
static_assert(sizeof(int) == 4);
```

Never put side effects inside `assert()` — they disappear in release builds.
