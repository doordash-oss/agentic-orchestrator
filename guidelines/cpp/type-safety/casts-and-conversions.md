# Casts and Conversions

## Never Use C-Style Casts

C-style casts `(T)expr` silently perform a union of `static_cast`, `const_cast`,
and `reinterpret_cast`. They are impossible to grep for and easy to misuse.

## The Four Named Casts

### `static_cast` — The Workhorse

Safe, compile-time, no runtime overhead:

```cpp
double d = 3.99;
int i = static_cast<int>(d);              // Truncates to 3

Derived* dp = new Derived{};
Base* bp = static_cast<Base*>(dp);         // Safe upcast

auto raw = static_cast<std::underlying_type_t<HttpStatus>>(HttpStatus::Ok);
```

### `dynamic_cast` — Runtime-Checked Downcast

Requires at least one virtual function (RTTI enabled):

```cpp
void describe(Shape* s) {
    if (auto* c = dynamic_cast<Circle*>(s)) {
        std::cout << "circle r=" << c->radius;
    }
}

// Reference form throws std::bad_cast on failure
auto& c = dynamic_cast<Circle&>(s);
```

### `const_cast` — Add/Remove Const

Only for calling const-incorrect legacy APIs. Removing `const` from an
originally-const object and writing is UB.

### `reinterpret_cast` — Almost Never

Only well-defined for pointer-to-integer round-trips and hardware registers:

```cpp
uintptr_t as_int = reinterpret_cast<uintptr_t>(ptr);
void* back = reinterpret_cast<void*>(as_int);

volatile uint32_t* gpio = reinterpret_cast<volatile uint32_t*>(0x40020000U);
```

Every `reinterpret_cast` in application code is a code-review red flag.

## `std::bit_cast` (C++20) — Type-Safe Bit Reinterpretation

Copies exact bit representation without violating strict aliasing. `constexpr`:

```cpp
int bits = std::bit_cast<int>(3.14f);  // Well-defined

constexpr uint32_t pi_bits = std::bit_cast<uint32_t>(3.14159265f);
static_assert(pi_bits == 0x40490FDB);

// Pre-C++20: use std::memcpy
float g = 2.71f;
int g_bits;
std::memcpy(&g_bits, &g, sizeof(float));
```

## Implicit Conversions: `explicit`

Mark single-argument constructors `explicit` unless implicit conversion is
a deliberate design decision:

```cpp
class Timeout {
public:
    explicit Timeout(int ms) : ms_(ms) {}
};

// set_timeout(500);           // COMPILE ERROR
set_timeout(Timeout{500});    // Explicit — clear intent
```

Same for conversion operators:
```cpp
class Handle {
public:
    explicit operator bool() const { return ptr_ != nullptr; }
};
if (h) { /* OK */ }
// int x = h;  // COMPILE ERROR
```

## Narrowing Conversions: Brace Initialization

Brace initialization `{}` rejects narrowing at compile time:

```cpp
int x = 300;
// char c{x};        // COMPILE ERROR: narrowing
char c = static_cast<char>(x);  // Intentional, documented

// constexpr exception: if the value fits, it's not narrowing
constexpr int val = 42;
char c{val};  // OK: 42 fits in char
```

## Decision Matrix

| Situation | Correct Tool |
|-----------|-------------|
| Numeric conversion (`int` -> `float`) | `static_cast` |
| Intentional narrowing | `static_cast` with comment |
| Downcast (type unknown) | `dynamic_cast` |
| Downcast (type known) | `static_cast` (carefully) |
| Const-incorrect legacy API | `const_cast` |
| Bit-level reinterpretation | `std::bit_cast` (C++20) / `memcpy` |
| Pointer to integer | `reinterpret_cast` |
| C-style cast | Never in new code |
