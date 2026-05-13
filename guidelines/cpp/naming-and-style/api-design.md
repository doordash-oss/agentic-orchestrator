# API Design

## Parameter Passing

| Intent | Type | Example |
|--------|------|---------|
| Read-only, small | By value | `void f(int x)` |
| Read-only, large | `const T&` | `void f(const std::string& s)` |
| Read-only string | `std::string_view` | `void f(std::string_view s)` |
| Read-only array | `std::span<const T>` | `void f(std::span<const int> data)` |
| Modify in-place | `T&` | `void normalize(std::vector<double>& v)` |
| Take ownership | By value + move | `void store(std::string s)` |
| Optional | `const T*` or `std::optional<T>` | `void f(const Foo* opt)` |

## Return Values

Prefer returning values over out parameters. Rely on RVO/NRVO:

```cpp
std::vector<int> buildIndex(const Document& doc) {
    std::vector<int> result;
    // ... populate ...
    return result;  // NRVO: no copy
}

// Multiple returns: use structured bindings
struct DivResult { int quotient; int remainder; };
DivResult divide(int a, int b) { return {a / b, a % b}; }
auto [q, r] = divide(14, 3);
```

### `[[nodiscard]]`

Mark returns that callers must check:

```cpp
[[nodiscard]] std::error_code writeFile(const Path& p, const Data& d);
[[nodiscard]] bool initialize();
```

### Never Return Dangling References

```cpp
// WRONG
const std::string& getName() {
    std::string name = computeName();
    return name;  // Dangling!
}
// CORRECT: return by value
std::string getName() { return computeName(); }
```

## Namespace Design

```cpp
namespace myproject {
namespace internal { /* implementation details */ }
class PublicClass {};
}

// NEVER in headers:
using namespace std;

// OK in .cpp files:
using std::string;
using std::vector;
```

### Inline Namespaces for Versioning

```cpp
namespace mylib {
    inline namespace v2 { class Widget { /* current */ }; }
    namespace v1 { class Widget { /* deprecated */ }; }
}
// Users write mylib::Widget — gets v2 transparently
```

## Operator Overloading

Only when behavior is obvious and matches built-in semantics:

```cpp
class Money {
public:
    Money& operator+=(const Money& rhs) {
        cents_ += rhs.cents_;
        return *this;
    }
    bool operator==(const Money& rhs) const { return cents_ == rhs.cents_; }

    // C++20: spaceship generates all comparison operators
    auto operator<=>(const Money&) const = default;
};

// Binary: non-member, in terms of +=
inline Money operator+(Money lhs, const Money& rhs) { return lhs += rhs; }

// Stream output: non-member friend
friend std::ostream& operator<<(std::ostream& os, const Money& m);
```

**Never overload** `&&`, `||`, `,` — short-circuit/sequencing semantics are lost.

## Overload Sets

Every overload should do the same thing — differ only in argument types:

```cpp
// GOOD: same operation, different types
void print(int x);
void print(double x);
void print(std::string_view x);

// BAD: different semantics under same name
void open(Gate& g);        // Opens a gate
void open(const char* f);  // Opens a file
```

Prefer default arguments over overloads when behavior is identical:

```cpp
void connect(const std::string& host, int port = 443, bool tls = true);
```
