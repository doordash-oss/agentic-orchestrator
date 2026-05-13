# Sync Primitives

**Critical rule**: all `sync` types must **not be copied** after first use.
Pass by pointer.

## Mutex and RWMutex

```go
var mu sync.Mutex
mu.Lock()
defer mu.Unlock()
// critical section
```

- `sync.Mutex`: exclusive access. `TryLock()` (Go 1.18+) for non-blocking.
- `sync.RWMutex`: multiple concurrent readers OR single writer. Writers have priority.
- Not goroutine-tied: one goroutine can lock, another can unlock.

**Rules:**
- Always `defer mu.Unlock()` immediately after `Lock()` — prevents forgetting on
  early returns.
- Keep critical sections small — don't call external functions while holding a lock.
- Never call `RLock()` recursively — a pending `Lock()` blocks new `RLock()` callers,
  which deadlocks if the goroutine holding `RLock()` tries to `RLock()` again.
- Go mutexes are not reentrant — locking twice from the same goroutine deadlocks.

## WaitGroup

```go
var wg sync.WaitGroup
for _, item := range items {
    wg.Add(1)
    go func(it Item) {
        defer wg.Done()
        process(it)
    }(item)
}
wg.Wait()
```

Call `Add()` **before** launching the goroutine, not inside it — otherwise
`Wait()` might return before `Add()` runs.

Go 1.25 adds `wg.Go(f)` which combines `Add(1)` + `go f()` + auto-`Done()`.

## Once

Exactly-one-time initialization:

```go
var (
    instance *Config
    once     sync.Once
)

func GetConfig() *Config {
    once.Do(func() {
        instance = loadConfig()
    })
    return instance
}
```

- If `f` panics, `Do` considers it completed — future calls return without
  invoking `f`.
- `sync.OnceValue[T]` (Go 1.21+) caches a return value:
  `getValue := sync.OnceValue(func() *Config { return loadConfig() })`

## Pool

Reusable object cache to reduce GC pressure:

```go
var bufPool = sync.Pool{
    New: func() any { return new(bytes.Buffer) },
}

func process() {
    buf := bufPool.Get().(*bytes.Buffer)
    buf.Reset()
    defer bufPool.Put(buf)
    // use buf
}
```

- Items may be evicted at any GC cycle — don't rely on persistence.
- Not for connections or resources requiring cleanup.
- Best for frequently allocated temporary objects (buffers, encoders).

## Map

Concurrent-safe map, optimized for two patterns:

1. Write-once, read-many (caches, registries)
2. Goroutines with disjoint key sets

```go
var m sync.Map
m.Store("key", value)
v, ok := m.Load("key")
m.Delete("key")
m.Range(func(k, v any) bool { return true })
```

**Not a general replacement** for `map` + `Mutex`. Use `sync.Map` only when
its specific optimization patterns apply. For most concurrent maps, a regular
`map` protected by `sync.RWMutex` is simpler and often faster.

## Atomic Operations

For single values where a full mutex is overkill:

```go
var counter atomic.Int64
counter.Add(1)
counter.Load()
counter.CompareAndSwap(old, new)
```

`atomic.Value` for read-mostly config with copy-on-write:

```go
var cfg atomic.Value // stores Config
cfg.Store(loadConfig())

// Read path (lock-free):
c := cfg.Load().(Config)

// Write path (coordinate writers with mutex):
mu.Lock()
defer mu.Unlock()
old := cfg.Load().(Config)
old.Field = newVal
cfg.Store(old)
```

**When to use atomics vs mutexes:**
- Atomics: single counter, flag, or pointer. One load/store/add/CAS.
- Mutexes: multiple related variables that must change together, or complex
  invariants.
- If two variables must change atomically together, use a mutex or wrap them
  in a struct stored via `atomic.Value`.

## Deadlock Prevention

- Establish a consistent lock ordering — always acquire locks in the same sequence.
- Keep critical sections small.
- Use `context.WithTimeout` to prevent indefinite blocking.
- Go's runtime detects "all goroutines blocked" deadlocks and panics. Partial
  deadlocks (some goroutines still running) are not detected automatically.
