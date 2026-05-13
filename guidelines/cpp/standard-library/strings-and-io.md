# Strings and I/O

## `std::string` vs `std::string_view`

`string_view` (C++17) is a non-owning, read-only view — pointer and length:

```cpp
// Forces allocation for string literals
void log(const std::string& msg);

// Zero allocation for any string-like argument
void log(std::string_view msg) noexcept;
```

**Danger**: never return a `string_view` to a local `std::string`:
```cpp
std::string_view danger() {
    std::string tmp = "data";
    return tmp;  // DANGLING — tmp destroyed
}
```

C++20 adds `starts_with`, `ends_with`, `contains` on both types.

## Small String Optimization (SSO)

Short strings are stored inline (no heap allocation):
- libstdc++ (GCC): 15 chars
- libc++ (Clang): 22 chars
- MSVC: 15 chars

Moving a short string is the same cost as copying (no heap pointer to steal).

## String Formatting

### `std::format` (C++20)

```cpp
auto s = std::format("Hello, {}! You are {} years old.", name, age);
auto hex = std::format("{:#010x}", 255);  // "0x000000ff"
```

### `std::print` / `std::println` (C++23)

Combines `std::format` with direct output, matching `printf` performance:

```cpp
std::println("Value: {}", 42);
std::println(stderr, "Error: {}", msg);
```

### Custom Formatter

```cpp
struct Point { int x, y; };
template<> struct std::formatter<Point> {
    constexpr auto parse(std::format_parse_context& ctx) { return ctx.begin(); }
    auto format(const Point& p, std::format_context& ctx) const {
        return std::format_to(ctx.out(), "({}, {})", p.x, p.y);
    }
};
```

## File I/O

```cpp
namespace fs = std::filesystem;
fs::path p = "/data/input.bin";
if (!fs::exists(p)) throw std::runtime_error("Not found");

std::ifstream in(p, std::ios::binary);
// Set large buffer BEFORE reading
std::vector<char> buf(1 << 20);
in.rdbuf()->pubsetbuf(buf.data(), buf.size());

// Use '\n' not std::endl — endl flushes on every call
out << "line\n";   // GOOD
out << "line" << std::endl;  // BAD: flushes

// Disable stdio sync for pure C++ I/O
std::ios::sync_with_stdio(false);
std::cin.tie(nullptr);
```

## `std::regex` — Prefer Alternatives

`std::regex` is 10-100x slower than RE2 or CTRE:

```cpp
// For simple checks: string methods
if (line.starts_with("ERROR")) handle(line.substr(6));

// For performance-critical regex: RE2
static const RE2 re("\\d+");
RE2::PartialMatch(line, re, &match);

// For compile-time patterns: CTRE
if (auto m = ctre::match<"(\\d+)">(line)) use(m.get<1>());
```
