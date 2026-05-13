# Error Flow Patterns

## The "Errors Are Values" Philosophy

Rob Pike's key insight: errors are not a special category — they are ordinary
values that can be programmed. The excessive `if err != nil` pattern is a habit,
not a language limitation.

### The errWriter Pattern

Accumulate errors and check once at the end:

```go
type errWriter struct {
    w   io.Writer
    err error
}

func (ew *errWriter) write(buf []byte) {
    if ew.err != nil {
        return // short-circuit after first error
    }
    _, ew.err = ew.w.Write(buf)
}

// Usage: replaces repeated if-err-nil checks
ew := &errWriter{w: fd}
ew.write(header)
ew.write(body)
ew.write(footer)
if ew.err != nil {
    return ew.err
}
```

Real-world examples: `bufio.Writer` (errors deferred to `Flush()`),
`bufio.Scanner` (errors checked once after the scan loop).

## Handle or Return — Never Both

Three valid strategies when encountering an error:

1. **Handle it** — take corrective action and continue
2. **Return it** (with context) — let the caller decide
3. **Fatal/panic** — only in truly terminal situations

**Never log and return** — this creates duplicate log entries at every level:

```go
// Anti-pattern:
if err != nil {
    log.Printf("query failed: %v", err)
    return fmt.Errorf("query failed: %w", err)
}

// Correct — just return:
if err != nil {
    return fmt.Errorf("query failed: %w", err)
}
```

## Indent Error Flow

Keep the happy path at the left margin. Handle errors first and return early:

```go
// Anti-pattern: normal code nested in else
if err != nil {
    // error handling
} else {
    // normal code
}

// Correct: early return flattens the code
if err != nil {
    return err
}
// normal code continues at the left margin
```

When an `if` has an init statement, move the declaration to its own line for
clarity:

```go
x, err := f()
if err != nil {
    return err
}
// use x
```

## Error Handling in HTTP Handlers

Centralize error handling with an adapter type:

```go
type appError struct {
    Error   error  // internal (for logging)
    Message string // user-facing (for response)
    Code    int    // HTTP status
}

type appHandler func(http.ResponseWriter, *http.Request) *appError

func (fn appHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    if e := fn(w, r); e != nil {
        log.Printf("%v", e.Error)
        http.Error(w, e.Message, e.Code)
    }
}
```

Handlers return structured errors instead of writing responses inline:

```go
func viewRecord(w http.ResponseWriter, r *http.Request) *appError {
    record, err := db.Get(r.Context(), key)
    if err != nil {
        return &appError{err, "Record not found", 404}
    }
    return nil
}

http.Handle("/view", appHandler(viewRecord))
```

## Error Handling in Goroutines

Goroutines cannot return errors to their caller. Options:

### Channel-based

```go
errc := make(chan error, 1)
go func() {
    errc <- doWork()
}()
if err := <-errc; err != nil {
    // handle
}
```

### errgroup (preferred for fan-out)

```go
g, ctx := errgroup.WithContext(ctx)
g.Go(func() error { return fetchA(ctx) })
g.Go(func() error { return fetchB(ctx) })
if err := g.Wait(); err != nil {
    // first non-nil error; ctx is cancelled
}
```

## Error Handling in Deferred Functions

Deferred functions can read and modify named return values:

```go
func writeFile(path string, data []byte) (err error) {
    f, err := os.Create(path)
    if err != nil {
        return err
    }
    defer func() {
        cerr := f.Close()
        if err == nil {
            err = cerr // only capture close error if no prior error
        }
    }()
    _, err = f.Write(data)
    return err
}
```

## In-Band Errors — The Anti-Pattern

Never use sentinel return values when they could also be valid results:

```go
// Anti-pattern: "" could be a valid result
func Lookup(key string) string { return "" }

// Correct: explicit ok signal
func Lookup(key string) (value string, ok bool)

// Also correct: return error
func Lookup(key string) (string, error)
```
