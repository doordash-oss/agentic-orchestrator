# Sanitizers and Static Analysis

## AddressSanitizer (ASan)

Detects: heap/stack/global buffer overflows, use-after-free, double-free, leaks.
Overhead: ~2x slowdown, ~2x memory.

```bash
clang++ -O1 -g -fsanitize=address -fno-omit-frame-pointer -o prog prog.cc
```

Cannot be combined with TSan or MSan (UBSan can be combined with ASan).

## UndefinedBehaviorSanitizer (UBSan)

Detects: signed overflow, null dereference, shift errors, alignment issues,
reaching end of non-void function.

```bash
# Combined with ASan (recommended default)
clang++ -fsanitize=address,undefined -g -fno-omit-frame-pointer prog.cc

# Exit on first error (CI)
clang++ -fsanitize=undefined -fno-sanitize-recover=undefined prog.cc
```

Key checks: `signed-integer-overflow`, `null`, `shift`, `alignment`, `return`,
`vptr` (strict aliasing violations).

Per-function suppression for intentional behavior:
```cpp
[[clang::no_sanitize("signed-integer-overflow")]]
uint64_t hash_mix(uint64_t a, uint64_t b) { return a * 2654435761ULL + b; }
```

## ThreadSanitizer (TSan)

Detects data races. Overhead: 5-15x slowdown, 5-10x memory.

```bash
clang++ -fsanitize=thread -O2 -g -fno-omit-frame-pointer prog.cc
```

Use `-O2` (not `-O1`) with TSan to keep overhead manageable. Rebuild all
dependencies with `-fsanitize=thread` for accurate results.

## MemorySanitizer (MSan)

Detects uninitialized memory reads. Clang-only, Linux only.

**Requires all code** (including libc++) to be compiled with `-fsanitize=memory`.
Mixing instrumented and uninstrumented code produces false positives.

## clang-tidy

Recommended `.clang-tidy`:
```yaml
Checks: >
  -*,
  bugprone-*,
  modernize-use-nullptr,
  modernize-use-override,
  modernize-use-using,
  modernize-make-unique,
  modernize-make-shared,
  modernize-loop-convert,
  performance-*,
  readability-braces-around-statements,
  cppcoreguidelines-avoid-goto,
  cppcoreguidelines-no-malloc,
  clang-analyzer-*
WarningsAsErrors: "bugprone-*,performance-*"
```

CMake integration:
```cmake
set(CMAKE_CXX_CLANG_TIDY clang-tidy;-p=${CMAKE_BINARY_DIR})
```

## cppcheck

Complementary static analyzer with very low false-positive rate:
```bash
cppcheck --enable=all --error-exitcode=1 src/
```

Use alongside clang-tidy — they catch different bugs.

## Compiler Warnings

### GCC / Clang

```cmake
target_compile_options(my_target PRIVATE
    -Wall -Wextra -Wpedantic -Werror
    -Wshadow -Wnull-dereference -Wformat=2
    -Wconversion -Wsign-conversion -Wdouble-promotion
    -Wmisleading-indentation -Wunused
)
```

### MSVC

```cmake
target_compile_options(my_target PRIVATE
    /W4 /WX /permissive-
)
```
