# Type Erasure and Variants

## `std::variant` — Type-Safe Union

`std::variant` (C++17) is a discriminated union with value semantics. Use it
when the set of types is closed and known at compile time:

```cpp
using ParseResult = std::variant<int, double, std::string, std::monostate>;
```

### The Overload Pattern for Visitors

```cpp
template<typename... Ts>
struct overload : Ts... { using Ts::operator()...; };

ParseResult r = parse_input("3.14");

std::visit(overload{
    [](int v)          { std::cout << "int: " << v; },
    [](double v)       { std::cout << "double: " << v; },
    [](std::string& v) { std::cout << "string: " << v; },
    [](std::monostate) { std::cout << "empty"; },
}, r);
```

`std::visit` guarantees exhaustiveness — adding a type to the variant without
handling it causes a compile error.

### Safe Access with `std::get_if`

```cpp
// Throws std::bad_variant_access on wrong type
auto val = std::get<int>(r);

// Returns nullptr on mismatch — no exception
if (const int* p = std::get_if<int>(&r)) {
    use(*p);
}
```

### `std::monostate` for Default Construction

When no alternative is default-constructible:
```cpp
std::variant<std::monostate, NonDefaultConstructible, int> v;
// v is in monostate
```

## `std::variant` vs Virtual Dispatch

| Concern | `std::variant` | Virtual dispatch |
|---------|---------------|-----------------|
| Closed type set | Ideal | Overkill |
| Open/extensible types | Must recompile | Add new subclass |
| Memory locality | Value semantics, stack | Heap typical |
| Inlining | Full | Limited |
| Exhaustiveness | Compile-time | None |

## `std::any` — Dynamic Type Erasure

Holds any copy-constructible type. Use only when types are genuinely unknown
at compile time (plugin systems, event buses):

```cpp
std::map<std::string, std::any> config;
config["threshold"] = 0.85;

if (auto* p = std::any_cast<double>(&config["threshold"])) {
    use(*p);
}
```

Prefer `std::variant` when the type set is known.

## `std::function` and `std::move_only_function`

```cpp
// Type-erased callable — accepts lambdas, functors, function pointers
std::function<double(double, double)> op = std::plus<double>{};

// C++23: move-only callable (for lambdas capturing unique_ptr)
std::move_only_function<void()> task = [p = std::make_unique<Widget>()]() {
    p->process();
};
```

For hot paths, prefer direct templates over `std::function` (avoids indirection).

## CRTP: Compile-Time Polymorphism

Static polymorphism without vtables:

```cpp
template <typename Derived>
class Serializable {
public:
    std::string serialize() const {
        return static_cast<const Derived*>(this)->to_string();
    }
};

class Config : public Serializable<Config> {
public:
    std::string to_string() const { return "{...}"; }
};
```

Use CRTP when: all derived types known at compile time, performance-critical
paths, policy-based design. Use virtual when: open hierarchies, plugin systems.

## Custom Type Erasure: Concept/Model Pattern

For duck-typing without inheritance:

```cpp
class Drawable {
    struct Concept {
        virtual ~Concept() = default;
        virtual void draw() const = 0;
    };
    template <typename T>
    struct Model final : Concept {
        T object_;
        explicit Model(T obj) : object_(std::move(obj)) {}
        void draw() const override { object_.draw(); }
    };
    std::unique_ptr<Concept> pimpl_;
public:
    template <typename T>
    Drawable(T obj) : pimpl_(std::make_unique<Model<T>>(std::move(obj))) {}
    void draw() const { pimpl_->draw(); }
};

// Any type with .draw() works — no inheritance required
std::vector<Drawable> shapes;
shapes.emplace_back(Circle{});
shapes.emplace_back(Square{});
```
