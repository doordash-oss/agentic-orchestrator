# Smart Pointers

## The Ownership Vocabulary

| Declaration | Ownership Meaning |
|-------------|-------------------|
| `T&` or `const T&` | Non-owning borrow; cannot be null |
| `T*` or `const T*` | Non-owning borrow; may be null |
| `std::unique_ptr<T>` | Exclusive ownership |
| `std::shared_ptr<T>` | Shared (reference-counted) ownership |
| `std::weak_ptr<T>` | Non-owning observer of a shared object |

## `std::unique_ptr` — Exclusive Ownership

Use `unique_ptr` for any heap-allocated object with a single owner. It is the
default choice (Core Guidelines R.21).

### Always Use `make_unique`

```cpp
// CORRECT: exception-safe, no redundant type name
auto widget = std::make_unique<Widget>(args...);

// INCORRECT: redundant type, potential exception-safety hazard
std::unique_ptr<Widget> widget(new Widget(args...));
```

`make_unique` eliminates the class of bugs where `new` and the `unique_ptr`
constructor are separated by a potentially-throwing expression.

### Factory Functions

Return `unique_ptr<Base>` from polymorphic factories. The caller can keep it
as `unique_ptr` or convert to `shared_ptr`:

```cpp
std::unique_ptr<Shape> make_shape(ShapeType type) {
    switch (type) {
        case ShapeType::Circle: return std::make_unique<Circle>();
        case ShapeType::Square: return std::make_unique<Square>();
    }
}

auto shape = make_shape(ShapeType::Circle);           // unique ownership
std::shared_ptr<Shape> shared = make_shape(ShapeType::Square); // converts
```

Base class **must** have a `virtual` destructor for safe polymorphic deletion.

### Custom Deleters

Manage non-memory resources within `unique_ptr`:

```cpp
// FILE* with fclose
auto fp = std::unique_ptr<FILE, decltype(&fclose)>(
    fopen("data.txt", "r"), &fclose);

// Lambda deleter (zero-size via EBO)
auto sdl_deleter = [](SDL_Surface* s) { SDL_FreeSurface(s); };
std::unique_ptr<SDL_Surface, decltype(sdl_deleter)> surface(
    SDL_LoadBMP("img.bmp"), sdl_deleter);

// Functor (reusable, zero-size)
struct PGresultDeleter {
    void operator()(PGresult* r) const noexcept { PQclear(r); }
};
using PGresultPtr = std::unique_ptr<PGresult, PGresultDeleter>;
```

Prefer stateless lambdas or empty functors — they add no size overhead.

### pImpl Idiom

`unique_ptr` supports incomplete types for the pImpl pattern:

```cpp
// widget.h
class Widget {
public:
    Widget();
    ~Widget();                    // must be declared, defined in .cpp
    Widget(Widget&&) noexcept;    // same
    Widget& operator=(Widget&&) noexcept;
private:
    struct Impl;
    std::unique_ptr<Impl> pImpl_;
};

// widget.cpp — Impl is complete here
struct Widget::Impl { int value; std::string name; };
Widget::Widget() : pImpl_(std::make_unique<Impl>()) {}
Widget::~Widget() = default;
Widget::Widget(Widget&&) noexcept = default;
Widget& Widget::operator=(Widget&&) noexcept = default;
```

## `std::shared_ptr` — Shared Ownership

Use only when multiple independent owners with non-deterministic lifetimes
must share an object. Not the default.

### Always Use `make_shared`

```cpp
// Single allocation for object + control block
auto ptr = std::make_shared<Widget>(args...);
```

### Legitimate Uses

- Shared immutable data: `shared_ptr<const Config>`
- Observer/event systems where observers may outlive the subject
- Caches and graphs with multiple parent nodes

### Performance Cost

- Two pointers (object + control block) vs one for `unique_ptr`
- Atomic reference count on every copy/destruction
- Avoid passing by value when ownership isn't being transferred

## `std::weak_ptr` — Non-Owning Observer

Observes a `shared_ptr`-managed object without extending its lifetime.
Always check `lock()` before use:

```cpp
if (auto sp = weak.lock()) {
    sp->doSomething();  // Object still alive
} else {
    // Object has been destroyed
}
```

### Breaking Reference Cycles

```cpp
struct A { std::shared_ptr<B> b_ptr; };
struct B { std::weak_ptr<A> a_ptr; };  // Breaks the cycle
```

### Cache Pattern

```cpp
class Cache {
    std::unordered_map<int, std::weak_ptr<Widget>> entries_;
public:
    std::shared_ptr<Widget> get(int id) {
        auto& weak = entries_[id];
        if (auto sp = weak.lock()) return sp;
        auto sp = load_widget(id);
        weak = sp;
        return sp;
    }
};
```
