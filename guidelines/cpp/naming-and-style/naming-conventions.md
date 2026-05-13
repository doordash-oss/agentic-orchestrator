# Naming Conventions

## Quick Reference

| Element | Google | LLVM | Qt/Mozilla |
|---------|--------|------|------------|
| Classes | `PascalCase` | `PascalCase` | `PascalCase` |
| Functions | `PascalCase` | `camelCase` | `camelCase` |
| Variables | `snake_case` | `CamelCase` | `camelCase` |
| Members | `name_` | `Name` | `m_name` |
| Constants | `kConstName` | `ConstName` | `kConstName` |
| Namespaces | `snake_case` | `llvm` | `lowercase` |
| Macros | `ALL_CAPS` | `ALL_CAPS` | `ALL_CAPS` |
| Files | `snake_case.h` | `PascalCase.h` | varies |

## Classes and Structs

All major guides agree: **PascalCase**.

```cpp
class HttpRequestHandler {};
struct ParseResult {};
// NOT: httpRequestHandler, HTTP_Request_Handler
```

## Functions

Names should be verbs or verb phrases. Choose camelCase or PascalCase
consistently; Qt-style omits `Get` prefix on getters:

```cpp
// Qt/LLVM style
bool isConnected() const;
std::string hostName() const;    // No "get" prefix
void setHostName(std::string h);

// Google style
bool IsConnected() const;
std::string GetHostName() const;
```

## Variables

Short names for tight scopes, longer for wider scopes:

```cpp
for (int i = 0; i < n; ++i) {}           // Short scope: OK
double area = kPi * radius * radius;      // Function scope: descriptive
```

## Member Variables

```cpp
// Google: trailing underscore
class Timer { int64_t start_time_; bool running_; };

// Qt/Mozilla: m_ prefix
class Timer { int64_t m_startTime; bool m_running; };
```

## Constants

`ALL_CAPS` is reserved exclusively for macros (Core Guidelines NL.9):

```cpp
constexpr int kMaxConnections = 100;       // Google style
constexpr double kPi = 3.14159265358979;
enum class Color { kRed, kGreen, kBlue };

// WRONG: looks like a macro
const int MAX_CONNECTIONS = 100;
```

## Template Parameters

Single letter for simple, descriptive PascalCase for constrained:

```cpp
template <typename T> T max_val(T a, T b);
template <typename Container> void print_all(const Container& c);
template <typename InputIterator, typename OutputIterator>
void transform(InputIterator first, InputIterator last, OutputIterator out);
```

## Namespaces

```cpp
namespace image_processing {
namespace detail { /* internal */ }
}
// NOT: ImageProcessing, ip
```

## Macros — Minimize Usage

```cpp
// If unavoidable: ALL_CAPS with project prefix
#define MYLIB_CHECK_BOUNDS(index, size) ...

// Better: replace with constexpr/inline/templates
constexpr int kMaxSize = 1000;
template <typename T> T Max(T a, T b) { return a > b ? a : b; }
```

## Header Guards

```cpp
// Traditional (portable)
#ifndef MYPROJECT_HTTP_CONNECTION_H_
#define MYPROJECT_HTTP_CONNECTION_H_
// ...
#endif

// Modern (simpler, all major compilers)
#pragma once
```
