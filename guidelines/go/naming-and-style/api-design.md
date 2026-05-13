# API Design

## Constructor Patterns

### Simple Constructor

```go
func New(addr string) *Server {
    return &Server{addr: addr}
}
```

When the package exports only one main type, the constructor is `New()`.
Clients call `server.New(addr)`.

### Constructor with Error

```go
func New(addr string) (*Server, error) {
    if addr == "" {
        return nil, errors.New("empty address")
    }
    return &Server{addr: addr}, nil
}
```

## Functional Options Pattern

For APIs where most callers need zero or few options:

```go
type Option func(*Server)

func WithTimeout(d time.Duration) Option {
    return func(s *Server) { s.timeout = d }
}

func WithLogger(l *slog.Logger) Option {
    return func(s *Server) { s.logger = l }
}

func New(addr string, opts ...Option) *Server {
    s := &Server{
        addr:    addr,
        timeout: 30 * time.Second, // default
        logger:  slog.Default(),   // default
    }
    for _, opt := range opts {
        opt(s)
    }
    return s
}

// Usage:
srv := server.New(":8080", server.WithTimeout(10*time.Second))
```

**Advantages:**
- Default case requires zero arguments: `New(addr)`
- Self-documenting at call sites
- No `nil` parameters needed
- Adding options is backward-compatible

## Config Struct Pattern

For APIs where callers typically specify multiple settings:

```go
type Config struct {
    Addr    string
    Timeout time.Duration // zero value = use default
    Logger  *slog.Logger  // nil = use default
}

func New(cfg Config) *Server {
    if cfg.Timeout == 0 {
        cfg.Timeout = 30 * time.Second
    }
    if cfg.Logger == nil {
        cfg.Logger = slog.Default()
    }
    return &Server{cfg: cfg}
}

// Usage:
srv := server.New(server.Config{
    Addr:    ":8080",
    Timeout: 10 * time.Second,
})
```

**When to use**: when most callers specify at least one option, or when options
have complex relationships.

**Critical**: zero values must be meaningful defaults. If `Timeout: 0` means
"no timeout" and you also want a default, use a pointer or a separate sentinel.

## Choosing Between Patterns

| Situation | Pattern |
|-----------|---------|
| Most callers need zero options | Functional options |
| Most callers specify 2+ settings | Config struct |
| Settings are tightly coupled | Config struct |
| API must stay backward-compatible | Both work (prefer functional options) |
| Need validation on option combinations | Config struct (validate in constructor) |

## Method Design

### Pointer vs Value Receiver

See [abstractions/embedding-and-composition.md](../abstractions/embedding-and-composition.md)
for the full rules. Quick summary:

- **Pointer**: mutates receiver, contains sync fields, or is a large struct
- **Value**: small immutable types like `time.Time`
- **Never mix** pointer and value receivers on the same type

### Method Naming

- Don't stutter: `queue.Enqueue()` is fine, `queue.QueueItem()` is not
- Common verbs: `New`, `Get` (only for map-like access), `Set`, `Delete`,
  `Close`, `Reset`, `String`, `Error`
- Prefer single-word methods when possible: `Start`, `Stop`, `Run`

## Backward Compatibility

From the Go module compatibility guide:

- **Adding methods to a struct**: always safe
- **Adding methods to an interface**: **breaking** — all implementors must update
- **Adding fields to a config struct**: safe if zero value is the default
- **Adding non-comparable fields** (slices, maps, functions) to a struct
  previously used in `==` comparisons: **breaking**
- **Removing or renaming exported names**: **breaking**

This is why functions should **return concrete types** (can add methods freely)
and **accept interfaces** (caller defines what they need).
