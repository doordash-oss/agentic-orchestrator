# Table-Driven Tests

## The Core Pattern

```go
func TestAdd(t *testing.T) {
    tests := []struct {
        name string
        a, b int
        want int
    }{
        {"positive", 2, 3, 5},
        {"negative", -1, -2, -3},
        {"zero", 0, 0, 0},
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got := Add(tt.a, tt.b)
            if got != tt.want {
                t.Errorf("Add(%d, %d) = %d, want %d", tt.a, tt.b, got, tt.want)
            }
        })
    }
}
```

## Error Message Format

Always: **function name, inputs, got, want** — in that order:

```go
t.Errorf("Foo(%q) = %d; want %d", tt.in, got, tt.want)
```

- Got before want (not the backwards style from some frameworks)
- Name the function so the failure message is self-contained
- Use `t.Error` (not `t.Fatal`) for individual checks so all cases run
- Use `t.Fatal` only for setup failures that prevent the test from continuing

## Subtests with t.Run

`t.Run` creates named subtests, enabling:
- **Fine-grained execution**: `go test -run=TestAdd/negative`
- **Isolated failures**: `t.Fatal` in a subtest stops only that subtest
- **Parallel execution**: each subtest can call `t.Parallel()`

Subtest names are sanitized: spaces become underscores. Avoid slashes in names
(they are treated as separators).

## Complex Struct Comparison

Use `cmp.Diff` from `github.com/google/go-cmp` instead of `reflect.DeepEqual`:

```go
if diff := cmp.Diff(want, got); diff != "" {
    t.Errorf("Fetch() mismatch (-want +got):\n%s", diff)
}
```

Useful options:
- `cmpopts.IgnoreFields(T{}, "CreatedAt")` — ignore volatile fields
- `cmpopts.SortSlices(less)` — order-independent comparison
- `cmpopts.EquateEmpty()` — treat nil and empty slices as equal

Never use `reflect.DeepEqual` in tests — it doesn't produce helpful diffs.

## Test Helpers and t.Helper

Always call `t.Helper()` as the first line of any helper function:

```go
func requireNoError(t testing.TB, err error) {
    t.Helper() // failure points to the caller, not this line
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
}
```

Accept `testing.TB` (not `*testing.T`) so helpers work in benchmarks and fuzz
tests too.

## t.Cleanup vs defer

Prefer `t.Cleanup` in test helpers — it's scoped to the test, not the function:

```go
func setupDB(t *testing.T) *sql.DB {
    t.Helper()
    db := openTestDB(t)
    t.Cleanup(func() { db.Close() })
    return db
}
```

`t.Cleanup` runs even if the test panics, and in LIFO order (like defer).

## Other Useful testing.T Methods

- `t.TempDir()` — creates a temp directory, auto-cleaned after the test
- `t.Setenv("KEY", "val")` — sets env var, restores after the test (Go 1.17+)
- `t.Context()` — returns a context canceled before Cleanup runs (Go 1.24+)
- `t.Parallel()` — runs this test concurrently with other parallel tests

## Parallel Table Tests

```go
for _, tt := range tests {
    t.Run(tt.name, func(t *testing.T) {
        t.Parallel() // runs concurrently
        got := Foo(tt.input)
        if got != tt.want {
            t.Errorf("Foo(%v) = %v, want %v", tt.input, got, tt.want)
        }
    })
}
```

Go 1.22+ creates a new loop variable per iteration, so `tt` capture is safe.
For Go <1.22, add `tt := tt` before the `t.Run` call.

## Test File Organization

- White-box tests (`package foo`): access unexported identifiers. Use for
  internal logic.
- Black-box tests (`package foo_test`): test the public API only. Preferred
  for contract testing.
- Use `testdata/` for fixtures, golden files, and fuzz corpus.

## Example Functions

```go
func ExampleFoo() {
    fmt.Println(Foo("world"))
    // Output: Hello, world
}
```

Examples appear in godoc and are compiled and executed as tests. Use
`// Unordered output:` for non-deterministic output.
