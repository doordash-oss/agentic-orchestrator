# Module Management

## go.mod Essentials

```
module github.com/org/myproject

go 1.22

require (
    github.com/foo/bar v1.2.3
    golang.org/x/sync v0.7.0
)
```

Key directives:
- `module` — module path; for v2+, must include `/v2` suffix
- `go` — minimum Go version (mandatory as of Go 1.21; enforced by toolchain)
- `require` — minimum required version of each dependency
- `replace` — redirect to fork or local path (only applies in main module)
- `retract` — signal that a published version should not be used

## go.sum

Records SHA-256 hashes for every module version used. Never edit by hand.

- **Always commit to version control** — both `go.mod` and `go.sum`
- Verified against `sum.golang.org` by default
- Run `go mod tidy` to keep consistent with actual imports

## Minimal Version Selection (MVS)

Go selects the **minimum version** satisfying all requirements. This is
deterministic: the same `go.mod` always produces the same build list.

- New releases of dependencies don't affect your build unless you `go get`
- When two deps require different versions of a third module, MVS selects
  the higher minimum
- `go mod tidy` maintains consistency

## Common Commands

```bash
go mod tidy          # sync go.mod/go.sum with actual imports
go mod download      # pre-fetch dependencies
go get pkg@latest    # update a dependency
go get pkg@v1.2.3   # pin a specific version
go mod vendor        # copy deps into vendor/
go mod graph         # print dependency graph
go mod why pkg       # explain why a dependency is needed
```

## Versioning Strategy

| Version | Meaning |
|---------|---------|
| `v0.x.x` | Unstable, no compat guarantee |
| `v1.x.x` | Stable, backward-compatible within major |
| `v2+` | New module path (`/v2`), separate module |

## Private Modules

```bash
export GOPRIVATE=github.com/myorg/*
# Bypasses proxy and sum database for matching modules
```

For CI, set `GONOSUMCHECK` and `GONOPROXY` for private module patterns.

## Go Workspaces (go.work)

For developing across multiple local modules simultaneously:

```bash
go work init ./moduleA ./moduleB
go work use ./moduleC
```

- `go.work` takes precedence over `replace` directives
- **Don't commit `go.work` to version control** in most cases — it can break
  CI and other developers' setups
- Exception: repos where modules are exclusively co-developed

## Vendoring

- **Libraries should never vendor** — creates impossible dependency trees
- **Binaries can vendor** for reproducibility: `go mod vendor`
- CI/CD and air-gapped environments benefit from vendoring

## Multi-Module Repositories

**Single module per repo is the recommended default** — simpler versioning.

When multiple modules are needed:
- Each module root gets its own `go.mod`
- Version tags include subdirectory prefix: `module1/v1.2.3`
- Use case: components needing truly independent versioning
