# Header and Source Organization

## Pitchfork Layout (PFL)

The most widely adopted community specification for C++ project structure:

```
<project-root>/
  src/           Main source and private headers
  include/       Public API headers (separate layout)
  tests/         Integration tests
  examples/      Sample code
  external/      Vendored third-party (read-only)
  tools/         Development utilities
  docs/          Documentation
  build/         Ephemeral build artifacts (gitignored)
```

### Two Header Placement Strategies

**Separate** — public headers in `include/`, private in `src/`:
```
include/mylib/connection.hpp        # Public API
src/mylib/connection.cpp
src/mylib/connection_impl.hpp       # Private — not installed
```

**Merged** — everything in `src/` (internal tools, header-only libs):
```
src/mylib/connection.hpp
src/mylib/connection.cpp
```

### Namespace-to-Path Mapping

`geo::shapes::Polygon` → `include/geo/shapes/polygon.hpp`

## Include-What-You-Use (IWYU)

Every file must `#include` exactly the headers providing its symbols:

```cpp
// BAD: relying on <iostream> to transitively include <string>
#include <iostream>
std::string name;  // May break when <iostream> changes

// GOOD: explicit
#include <iostream>
#include <string>
std::string name;
```

## Forward Declarations

Use when only pointer/reference is needed (reduce header dependencies):

```cpp
class Bar;
class Foo {
    Bar* bar_;                    // Forward declaration sufficient
    void process(const Bar& b);  // Same
};
```

Never forward-declare `std::` types — use `<iosfwd>` for streams.

## Pimpl Idiom

Moves private members to `.cpp` for ABI stability and compile-time reduction:

```cpp
// widget.hpp — stable ABI surface
#pragma once
#include <memory>

class Widget {
public:
    Widget();
    ~Widget();                    // Defined in .cpp
    Widget(Widget&&) noexcept;    // Same
    Widget& operator=(Widget&&) noexcept;
    void render();
private:
    struct Impl;
    std::unique_ptr<Impl> impl_;
};

// widget.cpp — all implementation hidden
struct Widget::Impl {
    HeavyDep1 obj1;
    HeavyDep2 obj2;
};
Widget::Widget() : impl_(std::make_unique<Impl>()) {}
Widget::~Widget() = default;
Widget::Widget(Widget&&) noexcept = default;
Widget& Widget::operator=(Widget&&) noexcept = default;
```

## Core Guidelines: Source Files

| Rule | Guideline |
|------|-----------|
| SF.5 | `.cpp` must include its own `.h` first |
| SF.7 | Never `using namespace` at global scope in headers |
| SF.8 | All headers must have include guards |
| SF.10 | Do not rely on transitively included names (IWYU) |
| SF.11 | Headers must be self-contained |
| SF.22 | Use anonymous namespaces in `.cpp` for internal-only entities |
