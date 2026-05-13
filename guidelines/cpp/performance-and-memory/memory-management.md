# Memory Management

## Stack vs Heap Allocation

Prefer the stack for small, short-lived objects with bounded size. Use the heap
for objects whose size is unknown at compile time or whose lifetime must outlive
their scope.

**Stack**: O(1) allocation (just decrement stack pointer), same cache region as
calling code, no fragmentation, deterministic cleanup.

**Heap**: 50-200+ cycles per `malloc`/`new`, scattered addresses, potential
cache misses.

```cpp
// Compile-time dispatch: stack for small, heap for large
constexpr std::size_t on_stack_max = 64;

template<typename T>
struct Scoped { T value; };

template<typename T>
struct On_heap { std::unique_ptr<T> ptr; };

template<typename T>
using Handle = std::conditional_t<(sizeof(T) <= on_stack_max),
                                   Scoped<T>, On_heap<T>>;
```

## `std::pmr` — Polymorphic Memory Resources (C++17)

`std::pmr` decouples allocation strategy from container type via runtime
polymorphism. All `std::pmr::` containers are type-compatible with their
`std::` counterparts.

### Arena Allocator for Batch Workloads

```cpp
void process_request() {
    std::byte stack_buf[8192];
    std::pmr::monotonic_buffer_resource arena{stack_buf, sizeof(stack_buf)};

    std::pmr::vector<std::pmr::string> items{&arena};
    items.reserve(64);
    for (int i = 0; i < 64; ++i)
        items.emplace_back("item " + std::to_string(i));
    // Entire slab freed in one shot on arena destruction
}
```

### Built-in Resources

| Resource | Thread-Safe | Best For |
|----------|-------------|----------|
| `monotonic_buffer_resource` | No | Short-lived sequential allocations |
| `unsynchronized_pool_resource` | No | Single-thread, varied sizes |
| `synchronized_pool_resource` | Yes | Multi-threaded pool |
| `new_delete_resource()` | Yes | Default/fallback heap |

### PMR Pitfalls

```cpp
// PITFALL: PMR allocators do NOT propagate on copy
std::pmr::vector<int> copy = source;  // copy uses default resource, not arena!

// PITFALL: Swapping containers with different arenas is UB
std::swap(a, b);  // UB if a and b use different memory_resources

// PITFALL: Forgetting pmr:: prefix on element types
std::pmr::vector<std::string> bad{&arena};      // strings use heap!
std::pmr::vector<std::pmr::string> good{&arena}; // strings use arena
```

## Placement New

Constructs an object at a pre-allocated address. Building block of custom
allocators.

```cpp
alignas(MyType) std::byte buf[sizeof(MyType)];
MyType* obj = new (buf) MyType{args...};
obj->~MyType();  // Explicit destructor — do NOT call delete
```

Justified uses: custom allocators, implementing `optional`/`variant`, shared
memory, real-time systems.

## Memory Alignment

```cpp
// Cache-line aligned to prevent false sharing
struct alignas(64) ThreadCounter {
    std::atomic<int64_t> value{0};
};

// C++17 portable cache line size
struct alignas(std::hardware_destructive_interference_size) Counter {
    std::atomic<int64_t> value{0};
};

// Struct padding — order members largest-first
struct Optimized {
    double b;  // 8 bytes
    int    c;  // 4 bytes
    char   a;  // 1 byte + 3 pad = 16 total
};
// vs naive ordering: 24 bytes with padding
```
