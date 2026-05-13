# Integration Testing

## Short Flag Pattern

Skip slow tests in fast-feedback mode:

```go
func TestDatabaseIntegration(t *testing.T) {
    if testing.Short() {
        t.Skip("skipping integration test in short mode")
    }
    // ... slow test with real database
}
```

```bash
go test -short ./...    # fast: unit tests only
go test ./...           # full: includes integration tests
```

## Build Tag Separation

Completely separate integration test files:

```go
//go:build integration

package mypackage_test

func TestWithRealService(t *testing.T) { ... }
```

```bash
go test -tags=integration ./...
```

## TestMain for Shared Setup

One-time setup per package (databases, containers, services):

```go
func TestMain(m *testing.M) {
    db := setupTestDB()
    code := m.Run()
    db.Close()
    os.Exit(code)
}
```

## httptest — Testing HTTP Handlers

### ResponseRecorder (Handler Isolation)

```go
func TestHandler(t *testing.T) {
    req := httptest.NewRequest(http.MethodGet, "/users/123", nil)
    w := httptest.NewRecorder()

    handler(w, req)

    resp := w.Result()
    if resp.StatusCode != http.StatusOK {
        t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusOK)
    }
    body, _ := io.ReadAll(resp.Body)
    // assert on body
}
```

Use `httptest.NewRequest` (not `http.NewRequest`) for handler tests — it sets
`RequestURI` and creates a proper context.

### Test Server (Full HTTP Client Testing)

```go
func TestClient(t *testing.T) {
    ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        fmt.Fprintln(w, `{"id": "123"}`)
    }))
    defer ts.Close()

    client := NewClient(ts.URL)
    user, err := client.GetUser("123")
    if err != nil {
        t.Fatal(err)
    }
    // assert on user
}
```

For TLS testing, use `httptest.NewTLSServer` and `ts.Client()` (pre-configured
to trust the test server's certificate).

## Testcontainers

Real services in Docker for integration tests:

```go
func TestWithPostgres(t *testing.T) {
    if testing.Short() {
        t.Skip("requires Docker")
    }
    ctx := context.Background()

    container, err := testcontainers.Run(ctx, "postgres:16",
        testcontainers.WithEnv(map[string]string{
            "POSTGRES_PASSWORD": "test",
            "POSTGRES_DB":       "testdb",
        }),
        testcontainers.WithExposedPorts("5432/tcp"),
        testcontainers.WithWaitStrategy(
            wait.ForListeningPort("5432/tcp"),
        ),
    )
    require.NoError(t, err)
    defer testcontainers.CleanupContainer(t, container)

    host, _ := container.Host(ctx)
    port, _ := container.MappedPort(ctx, "5432/tcp")
    dsn := fmt.Sprintf("postgres://postgres:test@%s:%s/testdb",
        host, port.Port())
    // run tests against dsn
}
```

Use `TestMain` for expensive containers to share them across tests in a package.

## Golden File Testing

Compare output against known-good snapshots:

```go
var update = flag.Bool("update", false, "update golden files")

func TestRender(t *testing.T) {
    got := render(input)
    golden := filepath.Join("testdata", t.Name()+".golden")

    if *update {
        os.WriteFile(golden, got, 0o644)
        return
    }

    want, err := os.ReadFile(golden)
    if err != nil {
        t.Fatalf("reading golden: %v", err)
    }
    if !bytes.Equal(got, want) {
        t.Errorf("output mismatch; run with -update to regenerate")
    }
}
```

```bash
go test -update ./...   # regenerate golden files
go test ./...           # compare against golden files
```

Commit golden files to version control.

## testing/fstest — In-Memory Filesystem

Test code that works with `fs.FS`:

```go
fsys := fstest.MapFS{
    "config.yaml":     {Data: []byte("key: value")},
    "data/items.json": {Data: []byte(`["a","b"]`)},
}

// Validate the FS implementation:
if err := fstest.TestFS(fsys, "config.yaml", "data/items.json"); err != nil {
    t.Fatal(err)
}
```

## testing/iotest — Adversarial Readers

Test that code handles partial reads and errors:

```go
r := iotest.OneByteReader(strings.NewReader("hello"))
// Returns one byte at a time — catches code that assumes full reads

r := iotest.ErrReader(errors.New("disk failure"))
// Always returns an error — tests error handling paths

r := iotest.HalfReader(strings.NewReader("hello"))
// Returns half the requested bytes — tests buffer handling
```
