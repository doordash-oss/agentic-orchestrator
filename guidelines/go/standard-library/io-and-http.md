# IO and HTTP

## io.Reader and io.Writer

The universal composition interfaces. Any code accepting `io.Reader` works with
files, network connections, buffers, compressed streams, and more:

```go
func process(r io.Reader) error {
    data, err := io.ReadAll(r)
    // works with os.File, http.Response.Body, bytes.Buffer, etc.
}
```

### Composition

```go
// Chain readers:
r := io.LimitReader(resp.Body, 1<<20) // limit to 1MB
r = io.TeeReader(r, &buf)             // copy to buffer while reading

// Multi-writer:
w := io.MultiWriter(file, os.Stdout)  // write to both simultaneously
```

### Writing to Any io.Writer

`fmt.Fprintf`, `fmt.Fprintln` take `io.Writer` — works for files, HTTP
responses, buffers:

```go
fmt.Fprintf(w, "Hello, %s!\n", name) // w can be anything
```

## HTTP Server

### Always Set Timeouts

```go
srv := &http.Server{
    Addr:         ":8080",
    Handler:      mux,
    ReadTimeout:  5 * time.Second,
    WriteTimeout: 10 * time.Second,
    IdleTimeout:  120 * time.Second,
}
```

**Never** use zero-value timeouts in production — they mean "wait forever",
enabling slowloris attacks and resource exhaustion.

### Never Use DefaultServeMux in Production

`http.DefaultServeMux` is a global mutable variable. Any imported package can
register handlers on it:

```go
// Anti-pattern:
http.HandleFunc("/path", handler)
http.ListenAndServe(":8080", nil) // uses DefaultServeMux

// Correct: create your own mux
mux := http.NewServeMux()
mux.HandleFunc("GET /path", handler)
srv := &http.Server{Addr: ":8080", Handler: mux}
srv.ListenAndServe()
```

### Go 1.22+ Enhanced Routing

```go
mux := http.NewServeMux()
mux.HandleFunc("GET /users/{id}", getUser)
mux.HandleFunc("POST /users", createUser)
mux.HandleFunc("DELETE /users/{id}", deleteUser)

func getUser(w http.ResponseWriter, r *http.Request) {
    id := r.PathValue("id")
    // ...
}
```

### Middleware Pattern

```go
func logging(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        start := time.Now()
        next.ServeHTTP(w, r)
        slog.Info("request", "method", r.Method, "path", r.URL.Path,
            "duration", time.Since(start))
    })
}

// Chain: logging(auth(mux))
```

### Graceful Shutdown

```go
ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
defer stop()

go func() { srv.ListenAndServe() }()

<-ctx.Done()
shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()
srv.Shutdown(shutdownCtx)
```

## HTTP Client

### Always Set Timeouts

```go
client := &http.Client{
    Timeout: 10 * time.Second,
}
```

`http.DefaultClient` has no timeout — never use it for external calls.

### Closing Response Bodies

```go
resp, err := client.Get(url)
if err != nil {
    return err
}
defer resp.Body.Close()
```

Always close `resp.Body`, even if you don't read it — otherwise the underlying
TCP connection cannot be reused.

### Checking Status Codes

```go
if resp.StatusCode != http.StatusOK {
    body, _ := io.ReadAll(resp.Body)
    return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, body)
}
```

`http.Client` does not treat 4xx/5xx as errors — only connection-level failures
return a non-nil error.

## database/sql Patterns

### Always Use Context

```go
rows, err := db.QueryContext(ctx, "SELECT id, name FROM users WHERE active = $1", true)
```

### Close Rows

```go
rows, err := db.QueryContext(ctx, query)
if err != nil {
    return err
}
defer rows.Close()

for rows.Next() {
    var u User
    if err := rows.Scan(&u.ID, &u.Name); err != nil {
        return err
    }
}
return rows.Err() // check for iteration errors
```

### Connection Pool Settings

```go
db.SetMaxOpenConns(25)
db.SetMaxIdleConns(10)
db.SetConnMaxLifetime(5 * time.Minute)
```
