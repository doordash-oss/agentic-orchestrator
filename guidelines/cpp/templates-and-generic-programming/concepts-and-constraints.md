# Concepts and Constraints (C++20)

## Defining Concepts

A concept is a named compile-time predicate:

```cpp
template<typename T>
concept Hashable = requires(T a) {
    { std::hash<T>{}(a) } noexcept -> std::convertible_to<std::size_t>;
};
```

### Four Kinds of Requirements

```cpp
template<typename T>
concept MyType = requires(T a, T b) {
    a + b;                                    // Simple: expression must be valid
    typename T::value_type;                   // Type: nested type must exist
    { *a } -> std::convertible_to<int>;       // Compound: valid + return constraint
    requires std::is_signed_v<T>;             // Nested: further constraint
};
```

### Five Syntactic Positions

```cpp
template <Concept1 T>                        // Constrained type parameter
requires Concept2<T>                         // Requires clause
Concept3 auto                                // Constrained return type
myFunction(Concept4 auto param)              // Constrained abbreviated param
requires Concept5<T>;                        // Trailing requires clause
```

## Standard Library Concepts

Check `<concepts>`, `<ranges>`, and `<iterator>` before writing your own:

| Concept | Meaning |
|---------|---------|
| `std::integral<T>` | T is an integral type |
| `std::floating_point<T>` | T is floating-point |
| `std::same_as<T, U>` | T and U are the same type |
| `std::convertible_to<From, To>` | Implicit conversion exists |
| `std::invocable<F, Args...>` | F callable with Args |
| `std::ranges::range<T>` | T has begin/end |
| `std::ranges::random_access_range<T>` | Random-access range |

Build on library concepts:
```cpp
template<typename T>
concept Numeric = std::integral<T> || std::floating_point<T>;
```

## Concept-Constrained `auto`

```cpp
void print(std::integral auto x) { std::cout << x; }
std::integral auto get_count();
auto square = [](std::integral auto n) { return n * n; };
```

## Concepts vs SFINAE — Migration

```cpp
// Old: enable_if SFINAE
template <typename T, typename = std::enable_if_t<std::is_arithmetic_v<T>>>
T product(T a, T b) { return a * b; }

// C++17: if constexpr (single body, doesn't remove from overload set)
template <typename T>
bool close_enough(T a, T b) {
    if constexpr (std::is_floating_point_v<T>) return std::abs(a - b) < T(1e-6);
    else return a == b;
}

// C++20: named concept (preferred)
template<typename T>
concept Arithmetic = std::integral<T> || std::floating_point<T>;

template <Arithmetic T>
T product(T a, T b) { return a * b; }
```

## Subsumption (Overload Ordering)

A more-constrained overload is preferred:

```cpp
template<typename T>
concept Incrementable = requires(T t) { ++t; };

template<typename T>
concept BidirectionalIterator = Incrementable<T> && requires(T t) { --t; };

template<Incrementable T>           void advance(T& it);  // #1
template<BidirectionalIterator T>   void advance(T& it);  // #2: more constrained

int* p; advance(p);  // Selects #2
```
