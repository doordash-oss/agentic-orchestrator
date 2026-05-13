# Cache and Data Layout

## AoS vs SoA: Data-Oriented Design

**Array of Structures (AoS)** — the natural OOP layout:

```cpp
struct Particle {
    float x, y, z;       // 12 bytes
    float vx, vy, vz;    // 12 bytes
    float mass;           //  4 bytes
    int   alive;          //  4 bytes
};                        // 32 bytes total
Particle particles[10000];

// To update positions: loads all 32 bytes per particle including unused fields
// 10000 * 32 = 320 KB — spans L2/L3
```

**Structure of Arrays (SoA)** — data-oriented layout:

```cpp
struct Particles {
    float x[10000], y[10000], z[10000];
    float vx[10000], vy[10000], vz[10000];
    float mass[10000];
    int   alive[10000];
};

// Position update: touches only x[], y[], z[], vx[], vy[], vz[]
// 10000 * 4 * 6 = 240 KB — fits in L2
// Sequential access enables hardware prefetcher and SIMD auto-vectorization
```

**When AoS wins**: accessing all fields together (serialization, single-object lookup).
**When SoA wins**: loops touch only a subset of fields (common in simulations, ECS, rendering).

## False Sharing

False sharing occurs when two threads write to different variables on the same
64-byte cache line. The MESI protocol forces invalidation on every write —
performance can degrade 10-16x.

```cpp
// BROKEN: counters share a cache line
struct Counters {
    std::atomic<int64_t> a{0};  // bytes 0-7
    std::atomic<int64_t> b{0};  // bytes 8-15
};

// FIXED: each counter on its own cache line
struct Counters {
    alignas(64) std::atomic<int64_t> a{0};
    alignas(64) std::atomic<int64_t> b{0};
};
```

**Trade-off**: `alignas(64)` on a 4-byte int wastes 60 bytes. Use padding
only where false sharing is confirmed by profiling (`perf stat -e cache-misses`).

## Hot/Cold Data Splitting

Group frequently accessed fields together:

```cpp
struct EntityHot {
    float x, y, z;
    float vx, vy, vz;
    bool  active;
};

struct EntityCold {
    std::string name, description;
    int spawn_frame;
};

struct Entity {
    EntityHot   hot;              // Accessed every frame
    EntityCold* cold = nullptr;   // Accessed only on spawn/destroy
};
```

## Contiguous vs Node-Based Containers

- Iterating 1M elements: `std::vector` is **5-15x faster** than `std::list`
- Destroying 1M elements: `std::list` is **10x+ slower**

```cpp
// PREFER: contiguous, cache-friendly
std::vector<int> v(1000000);
for (int x : v) sum += x;  // L1/L2 hit rate near 100%

// AVOID for iteration: pointer-chasing kills cache
std::list<int> l(1000000);
for (int x : l) sum += x;  // Every node at arbitrary heap address
```

**When `std::list` IS appropriate:**
- O(1) splice of sublists (`list::splice`)
- Stable iterators across insertions/deletions
- LRU caches (move-to-front is O(1))

For read-heavy lookup, consider sorted `std::vector` + `std::lower_bound`
instead of `std::map`.

## Cache-Friendly Iteration

```cpp
int matrix[1000][1000];

// CACHE-HOSTILE: column-major — each row increment jumps a cache line
for (int c = 0; c < 1000; ++c)
    for (int r = 0; r < 1000; ++r)
        sum += matrix[r][c];

// CACHE-FRIENDLY: row-major — sequential access
for (int r = 0; r < 1000; ++r)
    for (int c = 0; c < 1000; ++c)
        sum += matrix[r][c];
```
