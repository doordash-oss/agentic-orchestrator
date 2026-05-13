# Testing Phase 10 Timing Report

This report records the Phase 10 isolation hardening and safe-parallelism pass.
Phase 1's baseline and the 6s per-package fast-suite budget remain in
`docs/testing-baseline.md`.

## Fast Suite

| Measurement | Before Phase 10 | Final Phase 10 |
|-------------|-----------------|----------------|
| Fast-suite wall time, `make test-fast` | 30s Phase 9 final | 23s final-review fix run, passed |
| Target | <=30s warm cache | met on this machine |

The final-review fix keeps the documented fast suite tag-free and restores the
roadmap's all-package short-mode scope. `make test-fast` now uses `go list
./...` to partition every package into two short-mode shards and runs each
shard with `go test <packages> -short -count=1 -parallel 32`.

The 23s final-review fix run reported `internal/git` at 9.673s under the
all-package fast target, staying within the <=30s warm-cache wall-clock target.
The follow-up package-budget fix reduced `internal/tui` below the 6s package
budget; a warm targeted rerun of `go test ./internal/tui -short -count=1`
reported 5.023s, and a subsequent rerun reported 4.669s.
`keys_test.go` and `markdown/markdown_test.go` were reclassified as
TUI-ineligible after the race detector exposed shared state, so Phase 10 did
not parallelize those cost centers.

## Per-Package Elapsed Deltas

| Package | Prior phase final | Phase 10 targeted elapsed | 6s budget | Result |
|---------|-------------------|---------------------------|-----------|--------|
| `internal/feature` | 1.884s Phase 4 JSON profile | 1.409s, `go test ./internal/feature -short -count=1` | 6s | within 6s |
| `internal/tui` | 8.830s Phase 7 package elapsed | 5.023s, `go test ./internal/tui -short -count=1` | 6s | within 6s |
| `internal/session` | 1.386s Phase 5 warm JSON timing | 1.976s, `go test ./internal/session -short -count=1` | 6s | within 6s |

The final TUI pass avoids repeated `git` calls for missing publish worktree
fixtures, skips host clipboard probes and real-git remote detection in short
mode, wires the heavyweight markdown renderer from the binary instead of the
fast TUI package, and keeps TUI observability integration behind the
`tui_observe` build tag while model-level observer routing remains covered by
fast fakes. The tagged coverage is documented as the **TUI observability**
extended gate: `go test -tags tui_observe ./internal/tui -run 'Observed|Emits' -count=1`.

## Race-Detector Run

| Command | Result | Evidence |
|---------|--------|----------|
| `go test ./... -count=1 -race` | passed | Full race sweep passed; slowest package was `internal/agent` at 275.094s. |

Targeted package race checks passed during implementation:

- `go test ./internal/feature/... -count=1 -race`
- `go test ./internal/tui/... -count=1 -race`
- `go test ./internal/session/... -count=1 -race`

## Flake Guard

| Command | Result | Evidence |
|---------|--------|----------|
| `go test ./... -short -count=10` | passed | Full repeat guard passed; `internal/tui` took 125.933s across 10 repeats. |

Repeat-100 spot checks:

| Package | Command | Result |
|---------|---------|--------|
| `internal/feature` | `go test ./internal/feature -run '^TestDeferralID_Stable$' -count=100` | passed 100/100 |
| `internal/tui` | `go test ./internal/tui -run '^TestExtractActivityLinesCleanInput$' -count=100` | passed 100/100 |
| `internal/session` | `go test ./internal/session -run '^TestSessionInterrupt_NoProcess_NoError$' -count=100` | passed 100/100 |

## Sandboxed-Env Run

| Command | Result | Evidence |
|---------|--------|----------|
| `sandbox_home=/private/tmp/agentic-finalfix-home-$$; cache=/private/tmp/agentic-finalfix-gocache-$$; mkdir -p "$sandbox_home" "$cache"; chmod 500 "$sandbox_home"; GOCACHE="$cache" GOMODCACHE="$(go env GOMODCACHE)" GOPATH="$(go env GOPATH)" HOME="$sandbox_home" USERPROFILE="$sandbox_home" make test-fast` | passed | Final-review rerun passed with fast-suite wall time 41s on a cold temporary build cache. This is an isolation check, not the warm-cache budget measurement. |

The first sandboxed run exposed `TestWorkspaceManagerAddRootPickerComplete`
reading an empty read-only HOME. The test now sets `HOME` and `USERPROFILE` to
a `t.TempDir()` fixture with a selectable child directory.

## Isolation Audit Tables

### Home-Directory-Isolated Tests

| File | Production caller covered | Isolation strategy |
|------|---------------------------|--------------------|
| `cmd/agentico/cli_test.go` | CLI config discovery | Testscript sets `HOME=$WORK`. |
| `internal/config/config_test.go` | default config path resolution | `HOME` and `USERPROFILE` set to `t.TempDir()`. |
| `internal/llm/codex/provider_test.go` | Codex home and tilde expansion | `HOME` and `CODEX_HOME` set with `t.Setenv`. |
| `internal/agent/codex_env_wiring_test.go` | Codex command environment wiring | `HOME` and `CODEX_HOME` set with `t.Setenv`. |
| `internal/agent/phase_test.go` | phase runner Codex command construction | `HOME` and `CODEX_HOME` set with `t.Setenv`. |
| `internal/agent/phase_integration_test.go` | integration path Codex command construction | `HOME` and `CODEX_HOME` set with `t.Setenv`. |
| `internal/tui/dirpicker_test.go` | directory picker starting at user home | helper sets `HOME` and `USERPROFILE` to `t.TempDir()`. |
| `internal/tui/app_test.go` | workspace path compaction to `~` | test-scoped `HOME` with child workspace path. |
| `internal/tui/workspace_manager_test.go` | workspace add-root picker completion | helper sets `HOME` and `USERPROFILE` to `t.TempDir()`. |

Environment mutation audit:

```text
rg -n "^\s*os\.Setenv" --glob '*_test.go' .
# only test/e2e/tui_test.go package init NO_COLOR=1 remains

rg -n "^\s*os\.Chdir" --glob '*_test.go' .
# no matches
```

### Time.Sleep Audit

| Site | Strategy |
|------|----------|
| `internal/agent/implement_test.go` ticker pacing sleeps | converted to observable poll hooks. |
| `internal/tui/app_test.go` refactor/orchestrator drain sleeps | converted to `WaitForCycles()` completion waits. |
| `internal/orchestrator/shutdown_test.go` shutdown coordination sleeps | converted to start/exited channels. |
| `internal/session/session_test.go:938` | retained in short-skipped SIGTERM escalation test; child trap has no readiness signal. |
| `internal/session/session_test.go:1898` | retained as deliberate slow-consumer simulation for backpressure. |
| `internal/session/control_request_routing_test.go:136` | retained as deliberate slow-consumer delay before freeing one attach slot. |
| `internal/session/codex_integration_test.go:234` | retained in short-skipped live Codex integration as bounded poll interval. |
| `internal/session/manager_test.go:224` | retained in extended stress test as deliberate slow-consumer simulation. |
| `internal/session/manager_test.go:453` | retained in extended manager test as bounded status poll interval. |
| `internal/session/manager_test.go:458` | retained in extended manager test as negative assertion window. |
| `internal/feature/cleanup_orphan_runs_test.go:600` | retained to create a visible filesystem mtime boundary. |
| `internal/orchestrator/multirepo_test.go:207` | retained negative assertion window; absence of dispatch is the behavior under test. |
| `internal/orchestrator/orchestrator_phaserunner_test.go:571` | retained bounded poll interval for asynchronous phase dispatch capture. |
| `internal/orchestrator/grill_me_fanout_integration_test.go:633` | retained bounded poll interval for asynchronous prompt capture. |
| `internal/llm/codex/provider_test.go:225` | retained to create a visible filesystem mtime boundary. |
| `internal/agent/integration_test.go:579` | retained bounded poll interval while draining asynchronous events. |
| `internal/agent/integration_test.go:1167` | retained bounded poll interval for subprocess output file creation. |
| `internal/agent/phase_integration_test.go:508` | retained bounded poll interval while draining asynchronous events. |
| `internal/guidelinedef/guidelinedef_test.go:241` | retained to create a visible filesystem mtime boundary. |
| `internal/skilldef/skilldef_test.go:308`, `:471`, `:589` | retained to create visible filesystem mtime boundaries. |
| `test/e2e/tui_test.go:92`, `:607` | retained in extended gate for PTY cleanup and asynchronous orchestration observation. |
| `test/integration/lifecycle_test.go:294`, `:568`, `:590` | retained in integration gate for bounded event polling and child-process PID file observation. |

## Parallel-Coverage Tables

| Package | Candidate scope | Converted to `t.Parallel()` | Remaining exempt or ineligible |
|---------|-----------------|-----------------------------|--------------------------------|
| `internal/feature` | 229 `// parallel-candidate` test comments | 229 | 0 `// parallel-exempt` comments |
| `internal/tui` | 14 final candidate files, 183 top-level tests | 183 | 29 ineligible files; `keys_test.go` and `markdown/markdown_test.go` reclassified after race failures |
| `internal/session` | 30 `// parallel-candidate` test comments | 30 | 2 `// parallel-exempt` comments: subprocess shutdown and process-group cleanup representatives |

Mechanical guards added in this phase:

- `internal/feature/parallel_safety_test.go`
- `internal/tui/parallel_safety_test.go`
- `internal/session/parallel_safety_test.go`

Manual guard spot checks:

- Temporarily added a shared package variable mutation to two feature
  parallel-candidate tests. `go test -race ./internal/feature -run
  '^TestClassify_(FrontendKeywords|ImagesImplyFrontend)$' -count=1` failed
  with a data race naming `phase10RaceProbe`.
- Temporarily mutated `criticalAttachSendTimeout` in
  `TestSessionStartAndCapture`. `TestSessionPackageVarsNotMutatedByParallelTests`
  failed and named `TestSessionStartAndCapture` plus
  `criticalAttachSendTimeout`.
- Temporarily added `t.Parallel()` to the `parallel-exempt`
  `TestSessionStop`. `TestSessionParallelCandidatesCallTParallel -count=5`
  failed on each repetition and named the offending test.

## Regeneration Sequence

Run from the repository root on a warm local build cache.

```bash
go test ./internal/feature -short -count=1
go test -json ./internal/tui -short -count=1 > /private/tmp/agentic-tui-phase10.json
jq -r 'select(.Action=="pass" and .Test != null and .Elapsed != null) | [.Elapsed, .Test] | @tsv' /private/tmp/agentic-tui-phase10.json | sort -nr | head -12
go test ./internal/session -short -count=1
go test -tags tui_observe ./internal/tui -run 'Observed|Emits' -count=1
make test-fast
sandbox_home=/private/tmp/agentic-finalfix-home-$$; cache=/private/tmp/agentic-finalfix-gocache-$$; mkdir -p "$sandbox_home" "$cache"; chmod 500 "$sandbox_home"; GOCACHE="$cache" GOMODCACHE="$(go env GOMODCACHE)" GOPATH="$(go env GOPATH)" HOME="$sandbox_home" USERPROFILE="$sandbox_home" make test-fast
go test ./internal/feature -run '^TestDeferralID_Stable$' -count=100
go test ./internal/tui -run '^TestExtractActivityLinesCleanInput$' -count=100
go test ./internal/session -run '^TestSessionInterrupt_NoProcess_NoError$' -count=100
go build ./...
go vet ./...
go test ./cmd/agentico/... -count=1
go test ./... -short -count=10
go test ./... -count=1 -race
bash test/e2e/smoke.sh
```
