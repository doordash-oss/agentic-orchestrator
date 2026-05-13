# Time and Context Patterns

## time.Duration

Always use `time.Duration` for time intervals, never raw integers:

```go
// Good:
timeout := 30 * time.Second
delay := 500 * time.Millisecond

// Bad:
timeout := 30 // seconds? milliseconds? nanoseconds?
```

## Monotonic Clocks

`time.Now()` includes both wall clock and monotonic clock readings. Duration
calculations automatically use the monotonic component, making them immune to
system clock adjustments:

```go
start := time.Now()
doWork()
elapsed := time.Since(start) // uses monotonic clock — reliable
```

## time.After Leaks in Loops

`time.After` creates a timer that isn't garbage collected until it fires:

```go
// BUG: leaks a timer every iteration
for {
    select {
    case msg := <-ch:
        process(msg)
    case <-time.After(5 * time.Second):
        handleTimeout()
    }
}

// Fix: reuse a timer
timer := time.NewTimer(5 * time.Second)
defer timer.Stop()
for {
    if !timer.Stop() {
        select {
        case <-timer.C:
        default:
        }
    }
    timer.Reset(5 * time.Second)
    select {
    case msg := <-ch:
        process(msg)
    case <-timer.C:
        handleTimeout()
    }
}
```

## time.Ticker

For periodic operations:

```go
ticker := time.NewTicker(1 * time.Minute)
defer ticker.Stop()

for {
    select {
    case <-ticker.C:
        doPeriodicWork()
    case <-ctx.Done():
        return
    }
}
```

Always call `ticker.Stop()` to release resources.

## Time Comparison

```go
// Correct:
if deadline.Before(time.Now()) { ... }
if time.Since(start) > maxDuration { ... }

// Avoid:
if time.Now().Sub(deadline) > 0 { ... } // harder to read
```

## Parsing and Formatting

Go uses a reference time: `Mon Jan 2 15:04:05 MST 2006` (1/2 3:4:5 6 7):

```go
t, err := time.Parse("2006-01-02", "2024-03-15")
s := t.Format("2006-01-02T15:04:05Z07:00") // RFC 3339
s := t.Format(time.RFC3339)                  // same, using constant
```

## log/slog (Structured Logging, Go 1.21+)

### Basic Usage

```go
slog.Info("request handled",
    "method", r.Method,
    "path", r.URL.Path,
    "status", status,
    "duration", time.Since(start),
)
```

### Handler Configuration

```go
// JSON output:
logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
    Level: slog.LevelInfo,
}))

// Text output:
logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

// Set as default:
slog.SetDefault(logger)
```

### Logger with Context

```go
logger := slog.With("service", "auth", "version", "1.0")
logger.Info("starting")

// With context (for trace correlation):
slog.InfoContext(ctx, "processing request", "user_id", userID)
```

### Passing Loggers

Loggers are dependencies — pass them explicitly:

```go
type Service struct {
    logger *slog.Logger
}

func NewService(logger *slog.Logger) *Service {
    return &Service{logger: logger}
}
```

## crypto Best Practices

- **Always use `crypto/rand`** for keys, tokens, nonces — never `math/rand`
- **Use `crypto/rand.Text()`** (Go 1.24+) for random text
- Use `crypto/subtle.ConstantTimeCompare` for comparing secrets (prevents
  timing attacks)
- Use `crypto/tls` with modern TLS settings — never disable verification
  in production

```go
import "crypto/rand"

func GenerateToken() string {
    return rand.Text() // cryptographically secure random text
}
```
