# Template Patterns

## Function Templates and Deduction

```cpp
template<typename T>
T max_val(T a, T b) { return a > b ? a : b; }

auto r = max_val(3, 5);          // T = int, deduced
auto r2 = max_val<float>(3, 5);  // T = float, explicit
```

## Class Templates and CTAD (C++17)

CTAD deduces class template arguments from constructor arguments:

```cpp
std::pair p{1, 2.5};            // deduces pair<int, double>
std::vector v{1, 2, 3};         // deduces vector<int>
std::lock_guard lk{my_mutex};   // deduces lock_guard<std::mutex>
```

### Deduction Guides

Required for aggregates in C++17 (C++20 deduces aggregates automatically):

```cpp
template<typename T, typename U>
struct Pair { T first; U second; };

template<typename T, typename U>
Pair(T, U) -> Pair<T, U>;

Pair p{42, 3.14};  // Pair<int, double>
```

**Caveats**: `unique_ptr` and `shared_ptr` intentionally have no CTAD guides.

## Template Specialization

```cpp
// Primary template
template<typename T>
struct Serializer {
    static std::string serialize(const T& v) { return std::to_string(v); }
};

// Full specialization
template<>
struct Serializer<std::string> {
    static std::string serialize(const std::string& v) { return '"' + v + '"'; }
};

// Partial specialization
template<typename T>
struct Serializer<T*> {
    static std::string serialize(const T* v) {
        return v ? Serializer<T>::serialize(*v) : "null";
    }
};
```

**Function templates cannot be partially specialized** — use overloading:

```cpp
template<typename T> void process(T v)  { /* general */ }
template<typename T> void process(T* p) { /* pointer overload */ }
```

## Alias Templates

```cpp
template<typename T>
using Vec = std::vector<T, MyAllocator<T>>;

Vec<int> vi;  // std::vector<int, MyAllocator<int>>
```

## Variable Templates (C++14)

```cpp
template<typename T>
constexpr T pi = T(3.14159265358979323846);

float area = pi<float> * r * r;
```

## Non-Type Template Parameters (C++20)

```cpp
// Floating-point NTTPs (C++20)
template<double Scale>
double scaled(double v) { return v * Scale; }

// String literal NTTPs via structural wrapper
template<std::size_t N>
struct StringLiteral {
    constexpr StringLiteral(const char (&str)[N]) { std::copy_n(str, N, value); }
    char value[N];
};

template<StringLiteral Name>
struct Tagged { static constexpr auto name = Name.value; };

Tagged<"velocity"> v;
```
