# Metaprogramming

## `if constexpr` (C++17) — Replacing SFINAE

Evaluates at compile time; the non-taken branch is discarded entirely:

```cpp
template<class T>
constexpr bool close_enough(T a, T b) {
    if constexpr (std::is_floating_point_v<T>)
        return std::abs(a - b) < T(1e-6);
    else
        return a == b;
}
```

**Key limitation**: `if constexpr` does NOT remove the function from the overload
set. Use concepts/SFINAE when you need true overload exclusion.

## Type Traits

Always use the `_v` and `_t` shortcuts in C++17+:

```cpp
// Classification
std::is_integral_v<T>
std::is_floating_point_v<T>
std::is_pointer_v<T>
std::is_same_v<T, U>

// Transformation
std::remove_reference_t<T>     // T& → T
std::remove_cvref_t<T>         // const T& → T (C++20)
std::decay_t<T>                // array→pointer, remove cv-ref
std::conditional_t<B, T, F>    // B ? T : F

// Utility
std::invoke_result_t<F, Args...>   // return type of F(Args...)
```

## Fold Expressions (C++17)

Collapse parameter packs with a binary operator:

```cpp
// Sum with initial value (handles empty packs)
template<typename... Args>
auto sum(Args... args) { return (0 + ... + args); }

// All-true check
template<typename... Args>
bool all(Args... args) { return (... && args); }

// Print all arguments
template<typename... Args>
void print_all(Args&&... args) {
    ((std::cout << std::forward<Args>(args) << ' '), ...);
}

// Push multiple elements
template<typename T, typename... Args>
void push_all(std::vector<T>& v, Args&&... args) {
    (v.push_back(std::forward<Args>(args)), ...);
}
```

## `consteval` (C++20)

Every call must produce a constant expression:

```cpp
consteval int validated_port(int port) {
    if (port < 1 || port > 65535)
        throw std::invalid_argument("invalid port");
    return port;
}
constexpr int PORT = validated_port(8080);  // Validated at compile time

consteval uint32_t hash(const char* s, uint32_t h = 2166136261u) {
    return *s ? hash(s + 1, (h ^ (uint8_t)*s) * 16777619u) : h;
}
constexpr auto ID = hash("my_resource");  // Zero runtime cost
```

## When Metaprogramming Is Justified

Core Guideline T.120: Use TMP only when you really need to.

**Signs it's overengineering:**
- Same logic achievable with `if constexpr` or `constexpr` functions
- Writing recursive template structs to compute values (use `constexpr`)
- Code requires macros to be readable
- Compile times becoming a bottleneck

```cpp
// WRONG: old-style TMP value computation
template<int N>
struct Factorial { static constexpr int value = N * Factorial<N-1>::value; };

// CORRECT: constexpr function
constexpr int factorial(int n) { return n <= 1 ? 1 : n * factorial(n - 1); }
```
