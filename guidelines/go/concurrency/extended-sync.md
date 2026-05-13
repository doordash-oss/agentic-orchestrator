# Extended Sync (golang.org/x/sync)

The `golang.org/x/sync` module provides higher-level concurrency primitives
that complement the standard `sync` package.

## errgroup

Extends `sync.WaitGroup` with error propagation and optional context cancellation.

```go
import "golang.org/x/sync/errgroup"

g, ctx := errgroup.WithContext(ctx)
g.Go(func() error { return fetchA(ctx) })
g.Go(func() error { return fetchB(ctx) })
if err := g.Wait(); err != nil {
    // first non-nil error; ctx was canceled on that error
}
```

### Bounded Parallelism

```go
g := new(errgroup.Group)
g.SetLimit(20) // max 20 concurrent goroutines

for _, url := range urls {
    g.Go(func() error {
        return fetch(url) // Go 1.22+: safe to capture loop var
    })
}
if err := g.Wait(); err != nil { ... }
```

`SetLimit` must be called before any `Go` calls. `TryGo` returns `false` if
at the limit (non-blocking alternative).

### Pipeline Pattern with errgroup

```go
g, ctx := errgroup.WithContext(ctx)
paths := make(chan string)

// Producer
g.Go(func() error {
    defer close(paths)
    return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
        if err != nil { return err }
        select {
        case paths <- p:
        case <-ctx.Done():
            return ctx.Err()
        }
        return nil
    })
})

// Workers
results := make(chan Result)
for range 20 {
    g.Go(func() error {
        for p := range paths {
            select {
            case results <- process(p):
            case <-ctx.Done():
                return ctx.Err()
            }
        }
        return nil
    })
}

// Close results when all workers done
go func() { g.Wait(); close(results) }()
```

## semaphore

Weighted semaphore for bounding concurrent resource access:

```go
import "golang.org/x/sync/semaphore"

sem := semaphore.NewWeighted(int64(maxWorkers))

for _, item := range items {
    if err := sem.Acquire(ctx, 1); err != nil {
        break // context canceled
    }
    go func(it Item) {
        defer sem.Release(1)
        process(it)
    }(item)
}
// Wait for all by acquiring full weight:
sem.Acquire(ctx, int64(maxWorkers))
```

**Weighted use case**: different task types with different costs:

```go
sem := semaphore.NewWeighted(100) // 100 units of capacity
// Small tasks: sem.Acquire(ctx, 1)
// Large tasks: sem.Acquire(ctx, 25)
```

## singleflight

Deduplicates concurrent calls with the same key. Exactly one function executes;
all other callers block and receive the same result:

```go
import "golang.org/x/sync/singleflight"

var g singleflight.Group

func getUser(id string) (*User, error) {
    v, err, shared := g.Do(id, func() (any, error) {
        return db.QueryUser(id)
    })
    if shared {
        // result was shared with other callers
    }
    return v.(*User), err
}
```

### Thundering Herd Prevention

When a cache expires and many goroutines simultaneously repopulate it,
`singleflight` ensures only one backend call:

```go
func getCached(key string) ([]byte, error) {
    if v, ok := cache.Get(key); ok {
        return v, nil
    }
    v, err, _ := g.Do(key, func() (any, error) {
        data, err := expensiveLookup(key)
        if err == nil {
            cache.Set(key, data, ttl)
        }
        return data, err
    })
    return v.([]byte), err
}
```

### Caution

- If the executing call is slow, all callers wait.
- If it returns an error, all callers get that error.
- Use `g.Forget(key)` to allow retry after failure.
- `DoChan(key, fn)` returns a `<-chan Result` for non-blocking use.

## Choosing the Right Tool

| Need | Tool |
|------|------|
| Fan-out with error collection | `errgroup` |
| Bounded parallelism | `errgroup.SetLimit` or `semaphore` |
| Deduplicate identical concurrent work | `singleflight` |
| Weighted resource limiting | `semaphore` |
| Simple goroutine coordination | `sync.WaitGroup` |
| Exactly-once initialization | `sync.Once` |
