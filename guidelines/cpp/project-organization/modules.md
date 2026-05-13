# C++20 Modules

## Basics

Modules replace `#include` with a compiler-level dependency system:

```cpp
// math.cppm — module interface
export module math;

export namespace math {
    constexpr double pi = 3.14159265358979;
    double sqrt(double x);
}

// math_impl.cpp — implementation unit
module math;
double math::sqrt(double x) { return std::sqrt(x); }

// main.cpp — consumer
import math;
int main() { return math::sqrt(4.0); }
```

Module imports are **not transitive** — if `math` imports `<cmath>`, consumers
must import it themselves. This enforces IWYU at the module level.

## Module Partitions

Split large modules across files, invisible to consumers:

```cpp
// math:algebra.cppm
export module math:algebra;
export float miles_to_km(float m) { return m * 1.609f; }

// math.cppm — primary interface re-exports partitions
export module math;
export import :algebra;
export import :geometry;
```

## Migration Strategy

Use the **global module fragment** for gradual migration:

```cpp
module;                          // Global module fragment
#include <vector>                // Legacy headers
#include <third_party_legacy.h>

export module my_module;         // Named module starts here
import <memory>;                 // Modern imports

export class MyClass { /* ... */ };
```

**Phases:**
1. New code in modules; old code uses headers
2. Isolate heavy headers — convert highest-impact ones first
3. Convert internal headers to module implementation units
4. Convert public API headers last (most disruptive for consumers)

## CMake Integration (3.28+)

```cmake
cmake_minimum_required(VERSION 3.28)
add_library(math)
target_sources(math
    PUBLIC FILE_SET CXX_MODULES FILES
        src/math.cppm
        src/math_algebra.cppm
    PRIVATE
        src/math_impl.cpp
)
```

## Compiler Support

| Compiler | Minimum | Notes |
|----------|---------|-------|
| MSVC | 14.34 (VS 17.4) | Best support |
| Clang | 16.0 | `.cppm` extension; Ninja required |
| GCC | 14 | Ninja required |
| Apple Clang | Not yet | Xcode generator unsupported |

`import std;` (C++23) requires Clang 18+, MSVC 14.36+, GCC 15+.

Only Ninja and Visual Studio generators support module scanning.
