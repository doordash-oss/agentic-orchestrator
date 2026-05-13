# Goroutine Lifecycle

## The Fundamental Rule

Before writing `go`, answer: **how will this goroutine terminate?** Under what
conditions? Who signals shutdown?

Goroutines leak by blocking on channels — the garbage collector will not terminate
a goroutine even if the channels it's blocked on are unreachable.

## Starting Goroutines

Goroutines are cheap (~2KB initial stack, grows dynamically) and multiplexed onto
OS threads. Starting one is fine — losing track of it is the problem.

```go
// Arguments are evaluated in the calling goroutine
go process(ctx, item)

// Closures share variables with the enclosing scope
go func() {
    // captured variables are shared, not copied
}()
```

**Go 1.22+ loop variable fix**: each iteration creates a new variable, so this
is now safe:

```go
for _, item := range items {
    go func() {
        process(item) // safe in Go 1.22+
    }()
}
```

## Shutdown Patterns

### Context-Based (Preferred)

```go
func worker(ctx context.Context, jobs <-chan Job) {
    for {
        select {
        case j := <-jobs:
            process(j)
        case <-ctx.Done():
            return
        }
    }
}
```

### Done Channel (Legacy)

```go
done := make(chan struct{})
go func() {
    for {
        select {
        case work := <-jobs:
            process(work)
        case <-done:
            return
        }
    }
}()
// Later: close(done) broadcasts to all goroutines
```

### Reply Channel for Clean Shutdown

When you need confirmation that shutdown completed:

```go
type sub struct {
    closing chan chan error
}

func (s *sub) Close() error {
    errc := make(chan error)
    s.closing <- errc
    return <-errc
}

// Inside the goroutine:
// case errc := <-s.closing:
//     errc <- cleanup()
//     return
```

## Graceful Server Shutdown

```go
ctx, cancel := signal.NotifyContext(context.Background(),
    os.Interrupt, syscall.SIGTERM)
defer cancel()

srv := &http.Server{Addr: ":8080", Handler: mux}
go func() { srv.ListenAndServe() }()

<-ctx.Done()
shutdownCtx, shutdownCancel := context.WithTimeout(
    context.Background(), 30*time.Second)
defer shutdownCancel()
srv.Shutdown(shutdownCtx)
```

## Leak Prevention Checklist

- Every channel send has a corresponding receive (or the channel is buffered)
- Every goroutine has a path to `return` — either via channel close, context
  cancellation, or a `done` signal
- Library functions should leave concurrency to the caller — avoid launching
  goroutines that callers cannot cancel
- Goroutines that persist until program exit (server accept loops, config
  watchers) are the rare acceptable exception — document them

## Worker Pools

Fixed goroutine pool — the canonical pattern for bounded concurrency:

```go
func worker(jobs <-chan Job, results chan<- Result) {
    for j := range jobs {
        results <- process(j)
    }
}

jobs := make(chan Job, 100)
results := make(chan Result, 100)
for w := 0; w < numWorkers; w++ {
    go worker(jobs, results)
}
for _, j := range allJobs {
    jobs <- j
}
close(jobs)
for range allJobs {
    <-results
}
```

See also [extended-sync.md](extended-sync.md) for `errgroup.SetLimit` and
semaphore-based alternatives.
