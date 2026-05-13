# Variadic Templates

## Parameter Packs

```cpp
template<typename... Args>      // Args: template parameter pack
void func(Args&&... args) {     // args: function parameter pack
    std::cout << sizeof...(Args) << '\n';
}
```

### Pack Expansion Patterns

```cpp
template<typename... Ts>
void example(Ts... vals) {
    other(vals...);                          // Expand as arguments
    other(std::forward<Ts>(vals)...);        // Forward each
    int arr[] = {vals...};                   // Expand into initializer
    auto t = std::tuple<Ts...>{vals...};     // Expand into template args
    std::size_t sizes[] = {sizeof(Ts)...};   // Apply sizeof to each
}
```

### Prefer `initializer_list` for Homogeneous Args (T.103)

```cpp
// WRONG: variadic for same-type args
template<typename... Ints>
void append(std::vector<int>& v, Ints... vals) {
    (v.push_back(vals), ...);
}

// CORRECT: initializer_list
void append(std::vector<int>& v, std::initializer_list<int> vals) {
    v.insert(v.end(), vals.begin(), vals.end());
}
```

## Perfect Forwarding

Preserves value category (lvalue/rvalue) through a template:

```cpp
template<typename T>
void wrapper(T&& arg) {                  // Forwarding reference
    inner(std::forward<T>(arg));          // Preserves lvalue/rvalue-ness
}

// Variadic perfect forwarding: the canonical factory pattern
template<typename T, typename... Args>
std::unique_ptr<T> make_unique(Args&&... args) {
    return std::unique_ptr<T>(new T(std::forward<Args>(args)...));
}
```

### Rules

- `std::move` on rvalue reference parameters (`Buffer&&`)
- `std::forward` on forwarding reference parameters (template `T&&`)
- Never `std::move` on a forwarding reference
- Never forward the same argument twice (moved-from after first)
- Forwarding references only work with deduced `T` — `S(T&& x)` in a
  class template where `T` is fixed is NOT a forwarding reference

## `std::apply` and `std::invoke`

```cpp
// std::invoke: uniformly calls any callable
std::invoke(add, 1, 2);                    // Free function
std::invoke(&MyClass::method, obj, arg);   // Member function

// std::apply: unpacks tuple as arguments
auto add = [](int a, int b) { return a + b; };
std::apply(add, std::tuple{3, 4});  // 7

// Heterogeneous tuple iteration
template<typename... Args>
void print_tuple(const std::tuple<Args...>& t) {
    std::apply([](const auto&... args) {
        ((std::cout << args << ' '), ...);
    }, t);
}
```
