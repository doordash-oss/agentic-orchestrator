# Performance

## Profile Before Optimizing

Never optimize based on intuition. Use Go's built-in profiling tools:

```bash
# CPU profiling
go test -bench=. -cpuprofile=cpu.out
go tool pprof cpu.out

# Memory profiling
go test -bench=. -memprofile=mem.out
go tool pprof -alloc_objects mem.out

# HTTP server profiling
import _ "net/http/pprof"
# Then visit http://localhost:6060/debug/pprof/
```

Common pprof commands:
- `top` — hottest functions
- `list FuncName` — annotated source
- `web` — call graph in browser

## String Concatenation

| Method | Use When |
|--------|----------|
| `+` operator | 2-3 small strings |
| `fmt.Sprintf` | Formatting with verbs |
| `strings.Builder` | Building strings in loops |
| `strings.Join` | Joining a slice |

```go
// Loop concatenation — use strings.Builder
var b strings.Builder
for _, s := range items {
    b.WriteString(s)
}
result := b.String()

// Pre-allocate if size is known:
b.Grow(totalLen)
```

**Never** use `+` in a loop — it allocates a new string each iteration.

## strconv vs fmt

`strconv` is faster and allocates less for primitive conversions:

```go
// Prefer:
s := strconv.Itoa(42)
n, err := strconv.Atoi("42")
f, err := strconv.ParseFloat("3.14", 64)

// Avoid for simple conversions:
s := fmt.Sprintf("%d", 42)
```

## Slice and Map Pre-Allocation

When the size is known, pre-allocate to avoid repeated growth:

```go
// Slices:
result := make([]Item, 0, len(input))

// Maps:
m := make(map[string]int, expectedSize)
```

## Escape Analysis

Go's compiler decides whether variables are allocated on the stack (cheap) or
heap (requires GC). Variables "escape" to the heap when:

- They are returned from a function (pointer to local variable)
- They are stored in an interface
- They are captured by a goroutine closure
- Their size is too large or unknown at compile time

```bash
go build -gcflags="-m" ./...  # show escape analysis decisions
```

## sync.Pool

Reuse temporary objects to reduce GC pressure:

```go
var bufPool = sync.Pool{
    New: func() any { return new(bytes.Buffer) },
}

func process() {
    buf := bufPool.Get().(*bytes.Buffer)
    buf.Reset()
    defer bufPool.Put(buf)
    // use buf
}
```

- Objects may be evicted at any GC cycle
- Not for connections or resources requiring cleanup
- Best for frequently allocated temporary objects

## Pointer vs Value Performance

Passing large structs by pointer avoids copying. But for small structs,
value receivers can be faster because:
- Values stay on the stack (no heap allocation)
- Better cache locality
- No pointer indirection

**Rule**: benchmark before choosing. Default to pointer for structs >64 bytes
or with pointer fields.

## Interface Boxing Cost

Assigning a value to an interface variable may allocate:

```go
var r io.Reader = &buf  // pointer — no allocation
var r io.Reader = buf   // value — may allocate to box the value
```

For hot paths, consider accepting concrete types instead of interfaces.

## Common Performance Pitfalls

### time.After Leaks in Loops

```go
// BUG: each iteration creates a timer that isn't GC'd until it fires
for {
    select {
    case <-ch:
    case <-time.After(5 * time.Second):
    }
}

// Fix: reuse a timer
timer := time.NewTimer(5 * time.Second)
defer timer.Stop()
for {
    timer.Reset(5 * time.Second)
    select {
    case <-ch:
    case <-timer.C:
    }
}
```

### Unnecessary Copies of Large Structs

```go
// Copies the entire struct on each iteration:
for _, item := range largeStructSlice {
    process(item)
}

// Better: use index or pointer:
for i := range largeStructSlice {
    process(&largeStructSlice[i])
}
```

### Pre-Compile Regular Expressions

```go
// Package-level — compiled once:
var emailRe = regexp.MustCompile(`^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$`)

// Never inside a function — recompiles each call:
func validate(email string) bool {
    re := regexp.MustCompile(`...`) // anti-pattern
    return re.MatchString(email)
}
```
