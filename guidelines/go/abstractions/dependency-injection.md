# Dependency Injection

Go's approach to dependency injection is simple: **pass dependencies as
constructor parameters**. No frameworks, no annotations, no magic.

## Constructor Injection

```go
type Service struct {
    store  Store
    logger *slog.Logger
    client *http.Client
}

func NewService(store Store, logger *slog.Logger, client *http.Client) *Service {
    return &Service{
        store:  store,
        logger: logger,
        client: client,
    }
}
```

Every dependency is explicit. There's no hidden global state, no service locator,
no container to configure.

## Wiring in main()

`main()` is the only place that knows about all concrete implementations:

```go
func main() {
    logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
    db, err := sql.Open("postgres", dsn)
    if err != nil {
        log.Fatal(err)
    }
    defer db.Close()

    store := postgres.NewUserStore(db)
    client := &http.Client{Timeout: 10 * time.Second}
    svc := service.NewService(store, logger, client)
    handler := handler.New(svc)

    srv := &http.Server{Addr: ":8080", Handler: handler}
    log.Fatal(srv.ListenAndServe())
}
```

This pattern scales well. If wiring becomes a maintenance burden (dozens of
services), consider Google's Wire code generator.

## Avoid Package-Level State

Package-level variables and `init()` side effects are test-isolation killers:

```go
// Anti-pattern: global database connection
package db
var conn *sql.DB
func init() {
    var err error
    conn, err = sql.Open("postgres", os.Getenv("DB_URL"))
    if err != nil {
        log.Fatal(err)
    }
}

// Correct: explicit dependency
package db
type Store struct{ db *sql.DB }
func NewStore(db *sql.DB) *Store { return &Store{db: db} }
```

**Rules from Peter Bourgon:**
- Loggers are dependencies — pass them as parameters
- Only `func main()` should read flags and environment variables
- Library code receives configuration via parameters
- Define flags in `main`

## Interface Placement for DI

Define the interface where you use it, not where you implement it. This keeps
each package's dependencies explicit and minimal:

```go
// In the handler package:
type UserFinder interface {
    FindUser(ctx context.Context, id string) (*User, error)
}

type Handler struct {
    users UserFinder
}

// In the store package — no interface needed:
type PostgresStore struct { db *sql.DB }
func (s *PostgresStore) FindUser(ctx context.Context, id string) (*User, error) {
    // ...
}
```

The handler depends on `UserFinder` (which it defines), not on `*PostgresStore`.
In tests, pass a mock that satisfies `UserFinder`.

## Testing with DI

Constructor injection makes testing straightforward:

```go
func TestHandler(t *testing.T) {
    mock := &mockUserFinder{
        findUserFn: func(ctx context.Context, id string) (*User, error) {
            return &User{ID: id, Name: "Test"}, nil
        },
    }
    h := handler.New(mock)
    // test h
}
```

No framework needed. The mock implements the consumer-defined interface.

## When to Use Wire

Google's Wire generates wiring code from provider functions:

```go
// Providers:
func NewStore(db *sql.DB) *Store { ... }
func NewService(store *Store) *Service { ... }

// wire.go (injector declaration):
func InitializeService(db *sql.DB) *Service {
    wire.Build(NewStore, NewService)
    return nil // replaced by generated code
}
```

**Use Wire when**: wiring code in main() exceeds ~50 lines and is hard to
maintain. For most projects, manual wiring is simpler and more readable.

## Anti-Patterns

### Service Locator

```go
// Anti-pattern: hidden dependency resolution
func NewHandler() *Handler {
    store := container.Get("store").(Store)
    return &Handler{store: store}
}
```

Dependencies are hidden. Tests must set up the container. Compile-time safety
is lost.

### Interface Pollution

```go
// Anti-pattern: defining interfaces for everything "just for testing"
type ConfigLoader interface { Load() (*Config, error) }

// If there's only one implementation and no polymorphism need,
// just pass the concrete type:
func New(cfg *Config) *Service { ... }
```

Don't create interfaces solely for mocking. If the concrete type is simple and
has no external dependencies, test with the real thing.
