# Conventions

## Go Style

- **Error wrapping**: Always wrap errors with context: `fmt.Errorf("loading config: %w", err)`
- **Sentinel errors**: Export as `var ErrXYZ = fmt.Errorf("...")` (e.g., `ErrShuttingDown`)
- **No panics**: Use error returns, not panics
- **Channel-based concurrency**: Events flow through typed channels; no shared mutable state patterns
- **Filesystem persistence**: All state is YAML files on disk; no database

## Testing

- **Table-driven tests**: Use `t.Run()` subtests with descriptive names
- **Test helpers**: Use `t.Helper()` in helper functions
- **Public API only**: Never test private functions directly
- **Integration tests**: Skipped with `testing.Short()` / `-short` flag
- **CLI tests**: Black-box via `testscript` (txtar format) in `cmd/agentic/cli_test.go`

## Naming Conventions

| Entity | Pattern | Example |
|--------|---------|---------|
| Feature IDs | Random 16-byte hex | `a1b2c3d4e5f6...` |
| Slugs | Lowercase alphanumeric + hyphens | `add-dark-mode` |
| Branch names | `feature/<slug>` | `feature/add-dark-mode` |
| Session IDs | `<featureID>-<phase>` | `abc123-research` |
| Phase directories | Lowercase phase name | `research/`, `plan/`, `implement/` |
| Artifact files | Fixed names per phase | `phase_complete`, `output.txt`, `plan.md` |
| Iteration dirs | `iteration-NN` | `iteration-01`, `iteration-02` |

## Error Handling

- Errors are wrapped with context at each call site using `%w` verb
- `FailureType` constants categorize failure reasons for features
- Session crashes are detected via PID files and the recovery system
- Transactional operations (e.g., feature split) include rollback on failure

## TUI Versioning

The application version is sourced from **git tags** (e.g. `v1.2.3`) and injected at build time via ldflags into the `version` variable in `internal/tui/dashboard.go`. **Do not** edit that variable manually — `GetVersion()` resolves it in order: ldflags → Go module build info (`go install …@vTAG`) → `"dev"` fallback.

To cut a release with a new version, use the `/release` skill (runs GoReleaser locally). Use semantic versioning: patch for fixes, minor for new features or behavior changes, major for breaking changes. Routine TUI changes do not require any per-change version edit.

## State Transitions

- All feature status transitions are validated against the `validTransitions` map (`internal/feature/feature.go:292-308`)
- Phase timing is automatically accumulated on transitions
- Cost tracking per phase via `PhaseCosts` map on the Feature struct

## Status Serialization

- Feature status is serialized as string names (not integers) in YAML for stability
- Legacy integer fallback support exists for backward compatibility

## Filesystem Layout

```
~/.agentic-workflow/
├── config.yaml                        Global configuration
├── features/                          Feature state directory
│   └── <featureID>/
│       ├── feature.yaml               Feature state (YAML)
│       ├── research/                  Research phase output
│       ├── plan/                      Plan phase output
│       ├── implement/                 Implementation output
│       │   └── iteration-NN/          Per-iteration output
│       └── publish/                   Publish phase output
└── worktrees/                         Git worktrees
    └── <featureSlug>/
        └── <repoName>/               Worktree for feature
```

## Agent Protocol Signals

Agents communicate completion and status through specific mechanisms:

| Signal | Description |
|--------|-------------|
| `phase_complete` file | Written to output directory to signal phase completion |
| `AskUserQuestion` tool use | Agent needs human input (surfaced to TUI) |
| `review-feedback.md` with `## Verdict\nAPPROVED` | Review gate passed (file-based handoff) |
| `review-feedback.md` with `## Verdict\nCHANGES_REQUESTED` | Review gate wants changes (file-based handoff) |
| SDK `result` message | Session completed (check `IsSuccess()`) |
