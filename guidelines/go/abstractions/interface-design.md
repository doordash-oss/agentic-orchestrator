# Interface Design

## The Core Principles

1. **Accept interfaces, return concrete types**
2. **The bigger the interface, the weaker the abstraction**
3. **Define interfaces in the consumer, not the producer**
4. **Don't define interfaces before they are used**

## Accept Interfaces, Return Structs

Functions should take interfaces for flexibility and return concrete types for
extensibility:

```go
// Good: accepts interface, returns concrete
func NewDecoder(r io.Reader) *Decoder { ... }

// Bad: returns interface — freezes the API
func NewDecoder(r io.Reader) Decoder { ... }
```

**Why return concrete?** You can add methods to a struct without breaking callers.
Adding methods to an interface breaks all implementors.

**Exception**: the `error` interface, and factory functions where multiple concrete
types may be returned (strategy/command patterns).

## Consumer-Side Interface Definition

The consumer defines the interface containing only the methods it needs:

```go
// producer package — returns concrete type
package db
type UserStore struct { ... }
func (s *UserStore) GetUser(id string) (*User, error) { ... }
func (s *UserStore) ListUsers() ([]*User, error) { ... }
func (s *UserStore) DeleteUser(id string) error { ... }
func NewUserStore(dsn string) *UserStore { ... }

// consumer package — defines only what it needs
package handler
type UserGetter interface {
    GetUser(id string) (*User, error)
}
func NewHandler(users UserGetter) *Handler { ... }
```

This means the handler only depends on the single method it uses, not the
entire `UserStore` API. Testing requires a mock with one method, not five.

**Anti-pattern**: defining the interface in the producer package:

```go
// Bad: producer defines and returns an interface
package db
type Store interface {
    GetUser(id string) (*User, error)
    ListUsers() ([]*User, error)
    DeleteUser(id string) error
}
func New(dsn string) Store { return &userStore{...} }
```

This forces all consumers to depend on the full interface, even if they only
need one method.

## Keep Interfaces Small

The standard library demonstrates the pattern:

| Interface | Methods | Power |
|-----------|---------|-------|
| `io.Reader` | 1 | Composes into everything |
| `io.Writer` | 1 | Composes into everything |
| `io.Closer` | 1 | Composes freely |
| `fmt.Stringer` | 1 | Universal string representation |
| `error` | 1 | Universal error handling |

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

## Don't Define Interfaces Prematurely

Wait until you have at least two concrete implementations, or a real testing
need. Without a realistic use case, it's impossible to know which methods the
interface should include.

```go
// Premature — only one implementation exists
type Logger interface {
    Debug(msg string, args ...any)
    Info(msg string, args ...any)
    Warn(msg string, args ...any)
    Error(msg string, args ...any)
}

// Better — just accept *slog.Logger until you have a real need for abstraction
func New(logger *slog.Logger) *Service { ... }
```

## Compile-Time Interface Verification

Assert that a type satisfies an interface at compile time:

```go
var _ json.Marshaler = (*RawMessage)(nil)
var _ io.ReadWriter = (*Buffer)(nil)
```

This fails at compile time if the type doesn't implement the interface. Zero
runtime cost. Place at the package level in the file defining the type.

## The `interface{}` / `any` Problem

**Go proverb**: "`interface{}` says nothing."

Avoid `any` as a substitute for proper types. It loses type safety and requires
type assertions or reflection. With generics (Go 1.18+), most uses of `any`
can be replaced with type parameters:

```go
// Before generics:
func Contains(slice []any, item any) bool { ... }

// With generics:
func Contains[T comparable](slice []T, item T) bool { ... }
```

## The HandlerFunc Adapter Pattern

A function type can implement an interface, allowing ordinary functions to
satisfy interfaces without wrapping in a struct:

```go
type HandlerFunc func(ResponseWriter, *Request)

func (f HandlerFunc) ServeHTTP(w ResponseWriter, req *Request) {
    f(w, req)
}

// Any function with the right signature satisfies http.Handler:
http.Handle("/path", http.HandlerFunc(myFunc))
```
