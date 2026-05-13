# Embedding and Composition

Go uses composition instead of inheritance. Embedding promotes methods from an
inner type to an outer type without writing forwarding methods.

## Struct Embedding

```go
type Job struct {
    Command string
    *log.Logger
}

job := &Job{
    Command: "process",
    Logger:  log.New(os.Stderr, "Job: ", log.Ldate),
}
job.Println("starting") // calls job.Logger.Println
```

The embedded type's name becomes the field name for direct access: `job.Logger`.

## Interface Embedding

Compose larger interfaces from smaller ones:

```go
type ReadWriter interface {
    io.Reader
    io.Writer
}

type ReadWriteCloser interface {
    io.Reader
    io.Writer
    io.Closer
}
```

## Embedding Is Not Inheritance

**Critical distinction**: when an embedded method is called, the receiver is the
**inner type**, not the outer type. The embedded type doesn't know it's embedded.

```go
type Base struct{}
func (b Base) Name() string { return "base" }

type Extended struct{ Base }

e := Extended{}
e.Name() // returns "base", not "extended"
// There is no virtual dispatch — Base.Name knows nothing about Extended
```

## When to Embed vs Forward

### Embed When

- You want to promote the full method set of the inner type
- The outer type genuinely "is-a" or "has-a" relationship with the inner type
- The inner type's methods make sense on the outer type
- You want the outer type to satisfy interfaces the inner type satisfies

### Forward When

- You only need a subset of the inner type's methods
- The inner type has methods that don't make sense on the outer type
- You need to intercept or modify behavior

```go
// Forwarding — explicit control over the API:
type MyWriter struct {
    w io.Writer
}

func (mw *MyWriter) Write(p []byte) (int, error) {
    // intercept, log, transform, etc.
    return mw.w.Write(p)
}
```

## Override an Embedded Method

Outer methods shadow embedded methods with the same name:

```go
func (job *Job) Printf(format string, args ...any) {
    job.Logger.Printf("%q: %s", job.Command, fmt.Sprintf(format, args...))
}
```

## Name Conflicts

- An outer field/method hides any inner field/method with the same name (depth wins).
- Two embedded types at the same nesting level with the same name cause a compile
  error if that name is used.
- If the name is never referenced, there's no error — it just can't be accessed
  without qualification.

## Pointer vs Value Receiver Rules

| Condition | Receiver |
|-----------|----------|
| Method mutates the receiver | Pointer |
| Contains `sync.Mutex` or similar | Pointer |
| Large struct or array | Pointer (efficiency) |
| Map, func, or chan | Value (never pointer to these) |
| Slice without reslice/realloc | Value |
| Small, naturally immutable (`time.Time`) | Value |
| Any element is a pointer to mutating data | Pointer |

**Critical**: don't mix pointer and value receivers on the same type. If any
method uses a pointer receiver, all should.

Value methods can be called on both values and pointers. Pointer methods can
only be called on pointers (the compiler auto-takes the address for addressable
values).

## Embedding in Public APIs

Be cautious about embedding types in public structs — it exposes the embedded
type's full method set as part of your API:

```go
// Dangerous: exposes all sync.Mutex methods publicly
type Cache struct {
    sync.Mutex
    data map[string]string
}
// cache.Lock() and cache.Unlock() are now public API

// Better: keep mutex private
type Cache struct {
    mu   sync.Mutex
    data map[string]string
}
```

Only embed types in public structs when you explicitly want their methods to be
part of your API.

## Composition Patterns

### Middleware via Wrapping

```go
type loggingHandler struct {
    next   http.Handler
    logger *slog.Logger
}

func (h *loggingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    h.logger.Info("request", "method", r.Method, "path", r.URL.Path)
    h.next.ServeHTTP(w, r)
}

func WithLogging(next http.Handler, logger *slog.Logger) http.Handler {
    return &loggingHandler{next: next, logger: logger}
}
```

### Decorator Pattern

```go
type RetryClient struct {
    client     *http.Client
    maxRetries int
}

func (rc *RetryClient) Do(req *http.Request) (*http.Response, error) {
    var resp *http.Response
    var err error
    for i := 0; i <= rc.maxRetries; i++ {
        resp, err = rc.client.Do(req)
        if err == nil && resp.StatusCode < 500 {
            return resp, nil
        }
    }
    return resp, err
}
```
