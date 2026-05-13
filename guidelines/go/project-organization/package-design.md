# Package Design

## Package Naming Rules

- **Lowercase, single-word, no underscores**: `http`, `json`, `sync`
- **Short, concise, evocative**: suggests what the package does
- **Never repeat in exported names**: `http.Server` not `http.HTTPServer`
- **Constructor for the primary type is `New()`**: `list.New()` returns `*list.List`

### Banned Package Names

| Name | Problem | Fix |
|------|---------|-----|
| `util` | Meaningless dumping ground | Split into focused packages |
| `common` | Same | Split by concept |
| `misc` | Same | Split by concept |
| `base` | Same | Split by concept |
| `helper` | Same | Move functions to where they're used |
| `types` | Too generic | Name for the domain |
| `interfaces` | Too generic | Define interfaces in consumers |

> "If you cannot come up with a package name that's a meaningful prefix for the
> package's contents, the package abstraction boundary may be wrong."

## Package Cohesion

Everything in a package should relate to a single concept. The standard library
is the model: `bytes`, `strings`, `http`, `time` — tightly focused packages.

Signs of poor cohesion:
- Package has many unrelated types
- You can't describe the package in one sentence
- Types in the package don't interact with each other
- The package name is a category rather than a concept

## internal/ for Encapsulation

The `internal/` directory is **compiler-enforced**. Code under `internal/` can
only be imported by code in the parent directory tree.

```
mymodule/
├── cmd/myapp/        # can import internal/
├── internal/
│   ├── config/       # only importable within mymodule/
│   └── worker/
├── pkg/              # (if used) importable by anyone
└── go.mod
```

Use `internal/` for:
- Implementation details of a larger module
- Packages shared across `cmd/` binaries but not for external use
- Preventing accidental API surface growth in libraries

## Project Layout

The official guidance (go.dev/doc/modules/layout) defines patterns, not rules:

| Project Type | Layout |
|-------------|--------|
| Single library | All code at root beside `go.mod` |
| Single binary | `main.go` at root |
| Library + binary | `cmd/myapp/` for binary, root for library |
| Multiple binaries | `cmd/app1/`, `cmd/app2/` |
| Server project | `cmd/server/`, `internal/` for all logic |

### The /pkg Debate

The `golang-standards/project-layout` repo popularized `/pkg`, but the Go team
does not endorse it. Most projects don't need it — importable packages can live
directly at the root or in named subdirectories.

**Consensus**: use `cmd/` for executables and `internal/` for private code.
Don't reflexively add `/pkg` — it adds noise without value for most projects.

## Ben Johnson's Standard Package Layout

For larger applications:

1. **Root package**: domain types and interfaces only (no imports beyond stdlib)
2. **Subpackages by dependency**: `postgres/`, `http/`, `mock/`
3. **`main` package wires everything together**

This isolates external dependencies and makes the business domain the center
of the program.

## Export Only What Consumers Need

- Start with everything unexported
- Export only when an external package needs access
- Review exports when adding new types or methods
- Fewer exports = smaller API surface = easier maintenance

## Documentation Conventions

- Every exported name gets a doc comment
- Package comments: `// Package foo provides...`
- For extensive docs, use `doc.go` with only the package comment
- First sentence appears in package listings — make it count
- Deprecation: `// Deprecated: Use NewFoo instead.`
