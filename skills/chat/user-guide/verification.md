# Verification

Agentic uses a fast everyday suite plus explicit extended gates. Contributors
should run the fast suite before every handoff, then add the extended tiers that
match the area they changed.

| Tier | Command | Current wall time | When to run |
|------|---------|-------------------|-------------|
| Fast suite | `make test-fast` | 23s, target <=30s | Before every handoff; all packages in short mode without the race detector. |
| E2E smoke shell | `bash test/e2e/smoke.sh` | 48.53s | When launch behavior, embedded skills, or release packaging may be affected. |
| Isolated integration | `go test ./test/integration/... -count=1` | 323.06s | When lifecycle, state-machine, runs layout, or protocol-violation behavior changes. |
| E2E Go (TUI / teatest) | `go test ./test/e2e/... -count=1 -race` | 41.51s | When TUI, Bubble Tea model, or session lifecycle behavior changes. |
| TUI observability | `go test -tags tui_observe ./internal/tui -run 'Observed|Emits' -count=1` | 15.14s | When TUI observer wiring, emitted events, or feature-span propagation changes. |
| Race regression | `go test ./... -count=1 -race` | 158.82s | Before merging high-risk or concurrency-sensitive changes. |
| Eval | `AGENTIC_EVAL=1 go test ./test/eval/... -count=1` | gated; not measured | Only when validating live skill/guideline discovery against real LLM CLIs. |

`go vet ./...` and `go build ./...` remain required static and build checks.
The tagged **TUI observability** gate is the explicit opt-in check for slower
observer-backed TUI integration coverage. The race-enabled all-package sweep is
the **Race regression** gate, not the ordinary unit command. Timing details are
recorded in
`docs/testing-baseline.md`.

PR descriptions should name the tier(s) run and include a one-sentence reason
for any intentionally skipped relevant tier.
