# Ownership Patterns

## Smart Pointer Passing Rules

Only pass a smart pointer when the function's purpose is to manipulate
ownership. Otherwise, pass the underlying object by reference or raw pointer.

```
Parameter intent                       Type to use
─────────────────────────────────────  ──────────────────────────────
Read-only access, non-null             const T&
Read-write access, non-null            T&
Optional access (may be null)          const T*  or  T*
Transfer ownership (sink)              std::unique_ptr<T>   (by value)
Reseat a unique_ptr                    std::unique_ptr<T>&
Add a shared owner                     std::shared_ptr<T>   (by value)
Conditionally reseat shared_ptr        std::shared_ptr<T>&
```

```cpp
// Non-owning access — works with any ownership scheme
void render(const Widget& w);     // preferred: non-null, non-owning

Widget stack_widget;
auto unique_w = std::make_unique<Widget>();
auto shared_w = std::make_shared<Widget>();

render(stack_widget);    // OK
render(*unique_w);       // OK
render(*shared_w);       // OK

// INCORRECT: forces callers to use shared_ptr
void render(const std::shared_ptr<Widget>& w);  // overly restrictive
```

## Sink Parameters: Taking Ownership

A "sink" takes ownership. Use `unique_ptr` by value — the caller must
`std::move`, making the transfer explicit:

```cpp
class WorkQueue {
    std::vector<std::unique_ptr<Task>> pending_;
public:
    void enqueue(std::unique_ptr<Task> task) {
        pending_.push_back(std::move(task));
    }
};

auto task = std::make_unique<ComputeTask>(data);
queue.enqueue(std::move(task));  // Explicit — clear at call site
// task is now null
```

Pass by value (not `&&`) because passing by value *requires* the move,
making semantics unambiguous.

## Factory Functions

Return `unique_ptr<Base>` from polymorphic factories:

```cpp
std::unique_ptr<Logger> make_logger(LogLevel level) {
    if (level == LogLevel::Debug)
        return std::make_unique<DebugLogger>();
    return std::make_unique<ProductionLogger>(level);
}

// Caller can hold as unique or convert to shared
auto logger = make_logger(LogLevel::Info);
std::shared_ptr<Logger> shared_logger = make_logger(LogLevel::Info);
```

### Wrapping Legacy C APIs

Immediately wrap owning raw pointers from C APIs:

```cpp
Widget* create_widget_legacy();
void    destroy_widget(Widget* w);

auto w = std::unique_ptr<Widget, decltype(&destroy_widget)>(
    create_widget_legacy(), &destroy_widget);
```

## Return Values Over Out Parameters

```cpp
// INCORRECT: out parameter anti-pattern
bool create_widget(Widget** out);
void create_widget(std::unique_ptr<Widget>& out);

// CORRECT: return the value
std::unique_ptr<Widget> create_widget();
std::optional<Widget>   try_create();       // nullable without pointer
std::expected<Widget, Error> create();      // C++23: value or error
```

## Non-Owning Access

Raw pointers and references are the correct way to express non-owning access:

```cpp
class Renderer {
    const Scene* scene_;  // Non-owning — someone else manages the Scene
public:
    explicit Renderer(const Scene* scene) : scene_(scene) {}
};

class Application {
    Scene    scene_;       // Application owns the Scene
    Renderer renderer_;    // Renderer borrows it
public:
    Application() : renderer_(&scene_) {}
};
```

## Ownership Transfer Idioms

```cpp
// Transfer into a container
std::vector<std::unique_ptr<Task>> queue;
queue.push_back(std::move(task));

// Transfer into a class member (constructor sink)
class Pipeline {
    std::unique_ptr<Source> source_;
    std::unique_ptr<Sink>  sink_;
public:
    Pipeline(std::unique_ptr<Source> source, std::unique_ptr<Sink> sink)
        : source_(std::move(source)), sink_(std::move(sink)) {}
};
```
