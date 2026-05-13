# Move Semantics

## `std::move` Is Just a Cast

`std::move` does not move anything. It is a `static_cast` to rvalue reference,
enabling overload resolution to select move constructors/assignment operators:

```cpp
std::string s1 = "hello";
std::string s2 = std::move(s1);
// s2 stole s1's buffer. s1 is in a "valid but unspecified state".
```

## Move Constructors and Move Assignment

```cpp
class Buffer {
public:
    Buffer(Buffer&& other) noexcept
        : data_(std::exchange(other.data_, nullptr)),
          size_(std::exchange(other.size_, 0)) {}

    Buffer& operator=(Buffer&& other) noexcept {
        if (this != &other) {
            delete[] data_;
            data_ = std::exchange(other.data_, nullptr);
            size_ = std::exchange(other.size_, 0);
        }
        return *this;
    }
private:
    int* data_ = nullptr;
    std::size_t size_ = 0;
};
```

**Critical: move constructors must be `noexcept`.** `std::vector` checks
`is_nothrow_move_constructible` at compile time. Without `noexcept`, vector
*copies* instead of moves during reallocation.

## Rule of Zero: Implicit Move Generation

The compiler generates move operations when none of destructor, copy constructor,
copy assignment, move constructor, or move assignment are user-declared:

```cpp
struct DataChunk {
    std::vector<float> data;
    std::string label;
    int id = 0;
    // All five special members generated automatically
};

// GOTCHA: adding a destructor kills implicit move
struct Broken {
    std::vector<float> data;
    ~Broken() { /* logging */ }
    // Move is DELETED — must explicitly default:
    Broken(Broken&&) = default;
    Broken& operator=(Broken&&) = default;
};
```

## RVO/NRVO: Return Value Optimization

```cpp
// RVO (unnamed return) — GUARANTEED in C++17
std::vector<int> make_vec() {
    return std::vector<int>{1, 2, 3, 4, 5};
    // Constructed directly in caller — no copy, no move
}

// NRVO (named return) — not guaranteed but applied by all major compilers
std::vector<int> make_named() {
    std::vector<int> result;
    result.reserve(1000);
    for (int i = 0; i < 1000; ++i) result.push_back(i);
    return result;  // NRVO: constructed in caller
}

// ANTI-PATTERN: std::move on return DISABLES NRVO
std::vector<int> bad_return() {
    std::vector<int> result;
    return std::move(result);  // WRONG: forces move, disables NRVO
}
```

## Perfect Forwarding with `std::forward`

Preserves the value category of a forwarding reference argument:

```cpp
template<typename T>
void wrapper(T&& arg) {
    target(std::forward<T>(arg));
    // If caller passed rvalue: forwarded as rvalue
    // If caller passed lvalue: forwarded as lvalue
}

// Factory functions use perfect forwarding
template<typename T, typename... Args>
T* make_in_pool(Pool& pool, Args&&... args) {
    void* mem = pool.allocate(sizeof(T), alignof(T));
    return new (mem) T(std::forward<Args>(args)...);
}
```

**Decision rule:**
- `std::move` on *rvalue reference* parameters (`Buffer&&`)
- `std::forward` on *forwarding reference* parameters (template `T&&`)
- Never `std::move` on a forwarding reference

## Pass-by-Value + Move vs Pass-by-Reference

```cpp
class Widget {
    std::string name_;
public:
    // Option A: Two overloads — zero copies for both cases
    void set_name(const std::string& s) { name_ = s; }
    void set_name(std::string&& s)      { name_ = std::move(s); }

    // Option B: Pass-by-value + move — simpler, one extra move
    void set_data(std::vector<int> d) { data_ = std::move(d); }

    // Option C: Perfect forwarding — best perf, most complex
    template<typename S>
    void set_name_fwd(S&& s) { name_ = std::forward<S>(s); }
};
```

**Guidelines:**
- `const T&`: read-only access to any type
- `T` by value + `std::move`: sink parameters, cheap-to-move types
- `T&&`: sink parameters for move-only types (`unique_ptr`, `thread`)
- Template `T&&` + `std::forward`: factory functions and generic wrappers
