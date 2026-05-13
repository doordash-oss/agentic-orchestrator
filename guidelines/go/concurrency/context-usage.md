# Context Usage

`context.Context` carries cancellation signals, deadlines, and request-scoped
values across API boundaries. It is the standard mechanism for goroutine
lifecycle management.

## Creation

```go
ctx := context.Background()      // root: main, init, top-level handlers
ctx := context.TODO()            // placeholder: signals "context needed but unclear which"

ctx, cancel := context.WithCancel(parent)          // manual cancellation
ctx, cancel := context.WithTimeout(parent, 5*time.Second) // auto-cancel after duration
ctx, cancel := context.WithDeadline(parent, deadline)      // auto-cancel at absolute time
```

**Always `defer cancel()`** immediately after creation — even if the context
will expire naturally. This prevents resource leaks.

## Go 1.20+ Additions

```go
ctx, cancel := context.WithCancelCause(parent)
cancel(fmt.Errorf("shutting down"))  // record a cause
err := context.Cause(ctx)           // retrieve it

context.AfterFunc(ctx, func() {
    // runs in its own goroutine when ctx is canceled
})

ctx = context.WithoutCancel(parent) // inherits values but NOT cancellation
// useful for cleanup work that must continue after request cancellation
```

## Propagation Rules

1. **First parameter, named `ctx`**:
   ```go
   func DoWork(ctx context.Context, id string) error
   ```

2. **Never store in a struct**. Pass explicitly through function parameters.
   Exception: backward-compat APIs like `http.Request` that already embed it.

3. **Never pass `nil`**. Use `context.Background()` or `context.TODO()`.

4. **Values are for request-scoped data only**: trace IDs, auth tokens, request
   IDs. Not for optional function parameters or configuration.

5. **Key types must be unexported** to prevent collisions:
   ```go
   type contextKey string
   const userIDKey contextKey = "userID"

   func WithUserID(ctx context.Context, id string) context.Context {
       return context.WithValue(ctx, userIDKey, id)
   }
   ```

6. **Contexts are immutable** — safe to pass the same ctx to multiple concurrent
   calls sharing the same deadline and cancellation.

## Listening to Cancellation

```go
select {
case result := <-work:
    return result, nil
case <-ctx.Done():
    return nil, ctx.Err() // context.Canceled or context.DeadlineExceeded
}
```

For long-running operations, check periodically:

```go
for i, item := range items {
    if ctx.Err() != nil {
        return ctx.Err()
    }
    process(item)
}
```

## HTTP Context

In HTTP handlers, use the request's context:

```go
func handler(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context() // canceled when client disconnects
    results, err := db.QueryContext(ctx, query)
    // ...
}
```

## Common Mistakes

**Storing context in a struct:**
```go
// Wrong:
type Server struct { ctx context.Context }

// Correct:
func (s *Server) Process(ctx context.Context) error { ... }
```

**Using context values as function parameters:**
```go
// Wrong:
ctx = context.WithValue(ctx, "verbose", true)

// Correct:
func Process(ctx context.Context, verbose bool) error { ... }
```

**Forgetting to cancel:**
```go
// Wrong — context and its resources leak:
ctx, _ := context.WithTimeout(parent, 5*time.Second)

// Correct:
ctx, cancel := context.WithTimeout(parent, 5*time.Second)
defer cancel()
```
