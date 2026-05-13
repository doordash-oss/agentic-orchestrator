# Panic and Recover

## When to Panic

Panic is for **truly unrecoverable** situations. The Go FAQ explicitly states:

> "Library functions should avoid panic when possible. If the problem can be
> masked or worked around, it's better to let execution continue."

**Acceptable uses of panic:**
- Programmer errors that violate invariants (index out of bounds, nil dereference
  where it should be impossible)
- Initialization failures in `init()` when a library cannot set itself up
- `MustCompile`-style constructors that are called with compile-time-known inputs:
  `regexp.MustCompile("^[a-z]+$")`

**Never panic for:**
- Recoverable conditions (file not found, network timeout, invalid user input)
- Any error a caller might reasonably trigger
- Resource exhaustion that could be handled gracefully

## Never Panic Across Package Boundaries

The core rule:

> "No explicit panic() should be allowed to cross a package boundary.
> Indicating error conditions to callers should be done by returning error values."

If a package uses panics internally (e.g., for recursive descent parsing), it
must catch them at the API boundary and convert to errors:

```go
func Parse(input string) (result *AST, err error) {
    defer func() {
        if r := recover(); r != nil {
            if e, ok := r.(*syntaxError); ok {
                err = fmt.Errorf("parse: %v", e.msg)
            } else {
                panic(r) // re-panic unknown values
            }
        }
    }()
    return doParse(input), nil
}
```

## Recover Patterns

`recover()` only works inside a deferred function. It returns the value passed
to `panic()`, or `nil` if no panic is in progress.

### Goroutine Crash Isolation

Prevent one failing goroutine from killing the entire server:

```go
func safelyDo(work *Work) {
    defer func() {
        if err := recover(); err != nil {
            log.Printf("work failed: %v", err)
        }
    }()
    do(work)
}
```

### Re-Panic on Unknown Values

Use a named type for intentional panics so the recover function can distinguish
them from unexpected panics:

```go
type internalError string

func (e internalError) Error() string { return string(e) }

defer func() {
    if r := recover(); r != nil {
        if _, ok := r.(internalError); ok {
            err = fmt.Errorf("internal: %v", r)
        } else {
            panic(r) // unexpected — propagate
        }
    }
}()
```

## Goroutine Panic Rules

- A panic in one goroutine **cannot** be recovered by another goroutine.
- Each goroutine that might panic must have its own deferred recover.
- The `net/http` server automatically recovers panics in handlers and closes
  the connection. Use `panic(http.ErrAbortHandler)` to abort without logging.

## Error Handling in main()

Use a `run()` function that returns an error, and handle it in `main()`:

```go
func main() {
    if err := run(); err != nil {
        log.Fatal(err)
    }
}

func run() error {
    cfg, err := config.Load()
    if err != nil {
        return fmt.Errorf("loading config: %w", err)
    }
    // ...
    return nil
}
```

`log.Fatal` calls `os.Exit(1)` after logging — clean error output without a
stack trace. Use it for initialization failures, not `panic`.
