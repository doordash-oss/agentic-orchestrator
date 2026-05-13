# Error Wrapping and Context

## The %w Verb

Go 1.13 introduced `%w` in `fmt.Errorf` to wrap errors while preserving the
original for programmatic inspection:

```go
// %w wraps — callers can use errors.Is/errors.As
return fmt.Errorf("loading config: %w", err)

// %v adds context as string only — underlying error is hidden
return fmt.Errorf("loading config: %v", err)
```

## When to Use %w vs %v

The choice is an API design decision:

- **Use `%w`** when the wrapped error is part of your package's contract and callers
  need to inspect it (e.g., `errors.Is(err, fs.ErrNotExist)`).
- **Use `%v`** when the underlying error is an implementation detail. Wrapping with
  `%w` exposes it as public API — callers can depend on it, making internal changes
  a breaking change.

```go
// sql.ErrNoRows is an implementation detail — don't expose it
return fmt.Errorf("user lookup failed: %v", err)

// fs.ErrNotExist is part of the contract — expose it
return fmt.Errorf("reading %s: %w", path, err)
```

## errors.Is and errors.As

Always use these instead of `==` or type assertions — they traverse the full
wrapping chain:

```go
// Pre-1.13 (misses wrapped errors):
if err == ErrNotFound { }

// Correct:
if errors.Is(err, ErrNotFound) { }

// Type extraction — pre-1.13:
if e, ok := err.(*os.PathError); ok { }

// Correct:
var pathErr *fs.PathError
if errors.As(err, &pathErr) {
    fmt.Println("failed at path:", pathErr.Path)
}
```

Exception: `io.EOF` is never wrapped by convention, so `err == io.EOF` is fine.

## errors.Join (Go 1.20+)

Combine multiple errors into one:

```go
err := errors.Join(err1, err2, err3)
// errors.Is checks all wrapped errors via tree traversal
```

Also works with multiple `%w` verbs:

```go
err := fmt.Errorf("problems: %w and %w", err1, err2)
```

## Wrapping Depth

Wrap at **abstraction boundaries**, not at every function call:

```go
// Too much wrapping — redundant context:
// "opening config: reading file: open /etc/app.yaml: no such file"
return fmt.Errorf("opening config: %w",
    fmt.Errorf("reading file: %w", err))

// Right level — one wrap at the boundary:
// "loading config: open /etc/app.yaml: no such file"
return fmt.Errorf("loading config: %w", err)
```

Each wrapper should add **unique, non-redundant context** — the operation name,
a key identifier (user ID, file path), or the abstraction being crossed.

## Error String Conventions

Error strings must be lowercase (unless starting with an exported name or proper
noun) and must not end with punctuation:

```go
// Correct:
fmt.Errorf("loading config: %w", err)
errors.New("connection refused")

// Wrong:
fmt.Errorf("Loading config: %w", err)   // capitalized
errors.New("connection refused.")        // punctuation
```

Reason: errors are often printed mid-sentence:
`log.Printf("startup: %v", err)` produces `startup: Loading config:...` with a
spurious capital.

## The nil-Interface Trap

A function returning a concrete error type can produce a non-nil `error` even
when the value is nil:

```go
// BUG: always returns non-nil error
func check() error {
    var err *MyError = nil  // typed nil
    return err              // interface{type: *MyError, value: nil} != nil
}

// Fix: return nil explicitly
func check() error {
    if somethingWrong() {
        return &MyError{...}
    }
    return nil
}
```

**Rule**: always use `error` in return signatures, never a concrete error type.
