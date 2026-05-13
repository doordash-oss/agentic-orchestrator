# Sentinel and Custom Errors

## Sentinel Errors

Package-level error variables compared by identity:

```go
var (
    ErrNotFound   = errors.New("not found")
    ErrPermission = errors.New("permission denied")
)
```

Each call to `errors.New` creates a distinct value, even with identical text.
Sentinels are identified by pointer identity, not string comparison.

**Naming convention**: `ErrXxx` for exported, `errXxx` for package-internal.

**When to use**: when callers need to take different code paths based on the error.
When NOT to use: for internal errors that callers don't need to distinguish.

**API contract**: declaring a sentinel makes it part of your public API. Removing
or changing it is a breaking change. Be deliberate about which errors you export.

### Standard Library Examples

```go
// From io:
var EOF = errors.New("EOF")

// From os (aliasing fs sentinels):
var (
    ErrNotExist   = fs.ErrNotExist
    ErrExist      = fs.ErrExist
    ErrPermission = fs.ErrPermission
)
```

## Custom Error Types

When errors need to carry structured data beyond a message:

```go
type QueryError struct {
    Query string
    Err   error
}

func (e *QueryError) Error() string {
    return e.Query + ": " + e.Err.Error()
}

// Implement Unwrap to enable errors.Is/errors.As traversal
func (e *QueryError) Unwrap() error { return e.Err }
```

### Standard Library Example: os.PathError

```go
type PathError struct {
    Op   string  // "open", "read", etc.
    Path string
    Err  error
}

func (e *PathError) Error() string {
    return e.Op + " " + e.Path + ": " + e.Err.Error()
}
```

Produces: `open /etc/passwx: no such file or directory` — clear origin and context.

### When to Implement Unwrap

Only implement `Unwrap()` if you **intend to expose** the wrapped error. If the
inner error is an implementation detail, omit `Unwrap` — this prevents callers
from depending on internal error types.

## Custom Is() Method

For non-equality matching (e.g., partial field comparison):

```go
type Error struct {
    Path string
    User string
}

func (e *Error) Is(target error) bool {
    t, ok := target.(*Error)
    if !ok {
        return false
    }
    return (e.Path == t.Path || t.Path == "") &&
           (e.User == t.User || t.User == "")
}

// Allows partial matching:
if errors.Is(err, &Error{User: "admin"}) { ... }
```

## Behavior-Based Error Interfaces

Define error interfaces by behavior rather than identity:

```go
type TemporaryError interface {
    error
    Temporary() bool
}

type TimeoutError interface {
    error
    Timeout() bool
}
```

Callers query behavior without knowing concrete types:

```go
var netErr net.Error
if errors.As(err, &netErr) && netErr.Timeout() {
    // retry
}
```

This decouples error handling from implementation — any error type can signal
timeout behavior by implementing `Timeout() bool`.

## Error Hierarchies for Applications

For larger applications, define a domain error type:

```go
type AppError struct {
    Code    string // machine-readable code
    Message string // human-readable message
    Err     error  // underlying cause
}

func (e *AppError) Error() string { return e.Message }
func (e *AppError) Unwrap() error { return e.Err }
```

This separates internal errors (for logging) from user-facing messages
(for API responses). See [error-flow-patterns.md](error-flow-patterns.md) for
the HTTP handler pattern that uses this.
