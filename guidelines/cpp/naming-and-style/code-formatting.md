# Code Formatting

## clang-format

Commit a `.clang-format` file to the repository. Generate a starting config:

```bash
clang-format -style=google -dump-config > .clang-format
```

### Key Options

```yaml
BasedOnStyle: Google
IndentWidth: 2
ColumnLimit: 100
BreakBeforeBraces: Attach
PointerAlignment: Left        # int* p
SortIncludes: true
IncludeBlocks: Regroup
```

### Available Base Styles

| Style | Indent | Braces | Line Limit |
|-------|--------|--------|------------|
| Google | 2 spaces | K&R attach | 80 |
| LLVM | 2 spaces | K&R attach | 80 |
| Mozilla | 2 spaces | Break before functions | 80 |
| WebKit | 4 spaces | Attach | - |
| Microsoft | 4 spaces | Allman | - |

## Brace Placement

Pick one and enforce with clang-format:

```cpp
// K&R / Attach (Google, LLVM — most common)
if (condition) {
    doSomething();
} else {
    doOther();
}

// Allman (Microsoft)
if (condition)
{
    doSomething();
}
```

**Always brace** controlled statements, even single-line bodies.

## Line Length

80 is most portable; 100 is a reasonable modern compromise. Set in `.clang-format`.

## Include Ordering

Related header first, then C system, C++ standard, third-party, project:

```cpp
#include "mylib/http_connection.h"   // 1. Related header FIRST

#include <sys/types.h>               // 2. C system headers
#include <unistd.h>

#include <algorithm>                 // 3. C++ standard library
#include <memory>
#include <string>

#include "absl/strings/str_cat.h"    // 4. Third-party
#include "openssl/sha.h"

#include "mylib/config.h"            // 5. Project headers
```

Including the related header first catches missing dependencies immediately.

## `auto` Usage

**Use `auto` when** the type is obvious from the initializer, verbose, or an iterator:

```cpp
auto it = container.begin();
auto p = std::make_unique<Widget>();
for (const auto& [key, value] : map) {}
auto callback = [](int x) { return x * 2; };
```

**Avoid `auto` when** the type adds semantic clarity:

```cpp
int count = 0;         // Not: auto count = 0;
bool valid = true;     // Not: auto valid = true;
Widget w = create();   // What is w? Widget makes it clear
```

## Trailing Return Types

Use when needed for correctness; avoid for simple cases:

```cpp
// Necessary: return type depends on template parameters
template <typename L, typename R>
auto add(const L& lhs, const R& rhs) -> decltype(lhs + rhs);

// Clearer: out-of-class definition
auto SurfaceMesh::vertices_begin() -> VertexIterator;

// Unnecessary: simple return type
int add(int a, int b);  // Not: auto add(int a, int b) -> int;
```
