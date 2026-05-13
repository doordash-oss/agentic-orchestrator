# Const Correctness

## Fundamental Rules

Mark everything `const` that should not be modified. It serves as
compiler-enforced documentation, enables optimization, and makes code
easier to reason about.

```cpp
const int max_retries = 3;
const std::string url = get_config("url");

class Buffer {
public:
    size_t size() const { return data_.size(); }          // const: no mutation
    const std::byte* data() const { return data_.data(); }
    void append(std::byte b) { data_.push_back(b); }     // non-const: mutates
private:
    std::vector<std::byte> data_;
};
```

### Pointer Distinctions

```cpp
const int* p1 = &x;        // Pointer to const — can change p1, not *p1
int* const p2 = &x;        // Const pointer — can change *p2, not p2
const int* const p3 = &x;  // Both const
```

## `const&` vs Value Parameters

```cpp
// By value: scalars, small trivially-copyable types, sink parameters
void set_name(std::string name) { name_ = std::move(name); }

// By const&: large non-trivial objects, read-only access
void render(const Scene& scene);

// By string_view/span: read-only string/array access
void parse(std::string_view input);
void process(std::span<const int> data);
```

Never pass `const int&` or `const bool&` — for small types, the reference
overhead exceeds the copy cost.

## `constexpr` — Compile-Time Constants and Functions

```cpp
constexpr int max_buffer = 4096;

constexpr int factorial(int n) {
    return (n <= 1) ? 1 : n * factorial(n - 1);
}
static_assert(factorial(5) == 120);

// C++17: inline constexpr in headers
inline constexpr int kMaxConnections = 128;
```

## `consteval` (C++20) — Guaranteed Compile-Time

Every call must produce a constant expression:

```cpp
consteval uint32_t fnv1a_hash(std::string_view s) {
    uint32_t h = 2166136261u;
    for (char c : s) h = (h ^ c) * 16777619u;
    return h;
}
static_assert(fnv1a_hash("hello") == 0x4f9f2cab);

// int n = read_input();
// fnv1a_hash(n);  // COMPILE ERROR: n is not constant
```

## `constinit` (C++20) — Safe Static Initialization

Asserts a static/thread-local variable is initialized by a constant expression,
preventing the static initialization order fiasco:

```cpp
constinit static int g_count = 0;     // Initialized at compile time
constinit thread_local int t_id = 0;  // Same for thread-local

g_count++;  // Still mutable at runtime (constinit != const)
```

## `mutable` — Logical Constness

Allows specific members to be modified in `const` functions. Limited to caches,
lazy initialization, and synchronization:

```cpp
class Query {
public:
    const std::vector<Row>& results() const {
        if (!valid_) {
            cache_ = execute(sql_);   // mutable: allowed in const fn
            valid_ = true;
        }
        return cache_;
    }
private:
    std::string sql_;
    mutable std::vector<Row> cache_;
    mutable bool valid_ = false;
    mutable std::mutex mu_;
};
```

## `const_cast` — Use Sparingly

One legitimate use: calling const-incorrect legacy APIs:

```cpp
void legacy_c_api(char* str);  // Doesn't modify str

void call_legacy(const std::string& s) {
    legacy_c_api(const_cast<char*>(s.c_str()));
}
```

Removing `const` from an originally-`const` object and writing through it
is **undefined behavior**.
