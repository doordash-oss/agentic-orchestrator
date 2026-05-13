# Testing Baseline

This report captures the Phase 1 verification baseline for Agentic's fast-suite
work. The fast suite is the everyday local confidence check; the other commands
remain explicit extended gates.

## Reference Machine

Measured on an Apple M3 Max running macOS 26.4.1 (Darwin 25.4.0 arm64) with
Go 1.25.0 (`go version go1.25.0 darwin/arm64`). Timings are local wall-clock
measurements and are not hardware-independent guarantees.

## Fast Suite

- **Command**: `make test-fast`
- **Effective Go command**: `go list ./...` partitioned into two package
  shards, each run with `go test <packages> -short -count=1 -parallel 32`
- **Target**: <=30s on a warm build cache
- **Phase 1 measured wall time**: 115s
- **Current measured wall time**: 23s, target <=30s, recorded in
  [docs/testing-phase-10.md](testing-phase-10.md)

Phase 1 established the fast-suite entry point and baseline only. Later phases
reduced that 115s measurement to the <=30s target.

## Per-Package Fast-Suite Budget

The per-package budget is **6s**. It is derived from the 30s fast-suite target
divided by five top-package contributors, because the current timing profile is
dominated by five packages whose package-level scheduling is enough to decide
whether the total can fit inside the target. Packages above 6s are treated as
budget violations for later pruning, fixture reduction, or lower-level tests.

## Fast-Suite Package Timings

Package timings come from `go test -json ./... -short -count=1` package pass
events, sorted by elapsed time descending. Packages with no tests are omitted.

| Package | Elapsed | Budget |
|---------|---------|--------|
| `github.com/doordash-oss/agentic-orchestrator/internal/git` | 99.230s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/agent` | 80.750s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/feature` | 69.800s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/session` | 54.244s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/tui` | 32.942s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/ports` | 24.060s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/config` | 21.047s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/guidelinedef` | 20.248s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/skilldef` | 18.453s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/llm/clirun` | 17.017s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/test/e2e` | 16.063s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/llm/codex` | 15.780s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/observe` | 15.182s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/test/eval` | 14.846s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/tui/markdown` | 13.980s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks` | 13.927s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/orchestrator` | 13.712s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/test/integration` | 13.465s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/utilskill` | 13.159s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/llm/claude` | 12.745s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/cmd/agentico` | 11.911s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/permission` | 10.901s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/llm` | 8.880s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/agentdef` | 6.096s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/agent/prompts` | 5.187s | within 6s |

Packages currently exceeding the 6s budget are every package listed above
except `github.com/doordash-oss/agentic-orchestrator/internal/agent/prompts`.

## Extended Gate Timings

| Tier | Command | Current wall time | Purpose |
|------|---------|-------------------|---------|
| CLI black-box | `go test ./cmd/agentico/... -count=1` | 3.23s | Launch-surface and docs-contract tests. |
| E2E Go (TUI / teatest) | `go test ./test/e2e/... -count=1 -race` | 41.51s | Full TUI and teatest behavior with the race detector. |
| TUI observability | `go test -tags tui_observe ./internal/tui -run 'Observed|Emits' -count=1` | 15.14s | Observer-backed TUI event and feature-span integration coverage. |
| E2E smoke shell | `bash test/e2e/smoke.sh` | 48.53s | Builds the binary and checks CLI flags plus embedded skill layout. |
| Race regression | `go test ./... -count=1 -race` | 158.82s | Full all-package race-enabled regression sweep. |
| Isolated integration | `go test ./test/integration/... -count=1` | 323.06s | Lifecycle, state-machine, and protocol-violation coverage. |
| Eval | `AGENTIC_EVAL=1 go test ./test/eval/... -count=1` | gated; not measured this phase | Requires `AGENTIC_EVAL=1` and live LLM CLIs, so it remains an opt-in extended gate. |

## Regeneration Sequence

Run from the repository root on a warm local build cache. The first command is a
warm-up; the second command is the fast-suite wall-clock measurement recorded in
this report.

```bash
make test-fast
make test-fast
go test -json ./... -short -count=1 > /private/tmp/agentic-fast-suite.json
jq -r 'select(.Action=="pass" and .Package != null and .Elapsed != null and (.Test|not)) | [.Elapsed, .Package] | @tsv' /private/tmp/agentic-fast-suite.json | sort -nr
/usr/bin/time -p go test ./cmd/agentico/... -count=1
/usr/bin/time -p go test ./test/integration/... -count=1
/usr/bin/time -p go test -tags tui_observe ./internal/tui -run 'Observed|Emits' -count=1
/usr/bin/time -p bash test/e2e/smoke.sh
/usr/bin/time -p go test ./test/e2e/... -count=1 -race
/usr/bin/time -p go test ./... -count=1 -race
```

Warm-cache totals on the same machine should reproduce within **120 seconds**.
That tolerance reflects the existing standalone integration gate variance
observed during this baseline capture (`224.84s` then `323.06s`); later speedup
phases should tighten the tolerance as slow subprocess paths are reduced.
