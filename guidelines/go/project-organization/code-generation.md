# Code Generation

## go generate

`//go:generate` directives in source files are processed by `go generate`,
which must be run explicitly before `go build`:

```go
//go:generate stringer -type=Status
//go:generate mockgen -source=store.go -destination=mock_store_test.go
```

```bash
go generate ./...    # process all generate directives
```

## Rules for Generated Code

- Generated files must start with: `// Code generated ... DO NOT EDIT.`
- Place `//go:generate` directives in regular (non-generated) source files
- **Commit generated files** to version control — users of the package should
  not need the generation tools installed
- Run `go generate ./...` before releases to keep generated code current

## Common Generators

| Tool | Purpose |
|------|---------|
| `stringer` | `String()` method for integer constants |
| `mockgen` | Interface mocks for testing (go.uber.org/mock) |
| `protoc-gen-go` | Go code from Protocol Buffer definitions |
| `enumer` | Extended enum methods (names, values, parsing) |
| `go-bindata` | Embed binary assets (largely replaced by `//go:embed`) |

### stringer Example

```go
type Status int

const (
    StatusPending Status = iota
    StatusActive
    StatusDone
)

//go:generate stringer -type=Status
```

Generates `status_string.go` with `func (s Status) String() string`.

## Build Tags (Conditional Compilation)

Modern syntax (Go 1.17+):

```go
//go:build linux && amd64

package mypackage
```

Must appear before the `package` clause, preceded only by blank lines and
other comments.

### Pre-Defined Tags

| Category | Tags |
|----------|------|
| OS | `linux`, `windows`, `darwin`, `android`, `ios` |
| Architecture | `amd64`, `arm64`, `386`, `arm` |
| Compiler | `gc`, `gccgo` |
| CGO | `cgo` |
| Go version | `go1.18`, `go1.22`, etc. |
| General | `unix` (all Unix-like) |

### Filename-Based Constraints

Implicit build constraints from file names:

- `file_linux.go` — builds only on Linux
- `file_linux_amd64.go` — builds only on Linux/amd64
- `file_test.go` — included only in tests

### Custom Tags

```bash
go build -tags "debug,experimental" ./...
```

```go
//go:build debug

package mypackage

func init() {
    log.SetFlags(log.Lshortfile | log.Lmicroseconds)
}
```

## Environment Variables in Generators

Available to `//go:generate` commands:
`$GOFILE`, `$GOLINE`, `$GOPACKAGE`, `$GOARCH`, `$GOOS`, `$GOROOT`

## go:embed (Go 1.16+)

Embed files directly into Go binaries without code generation:

```go
import "embed"

//go:embed templates/*.html
var templates embed.FS

//go:embed version.txt
var version string

//go:embed logo.png
var logo []byte
```

This has largely replaced `go-bindata` and similar tools for static assets.
