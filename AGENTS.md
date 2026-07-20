# AGENTS.md — Testing Agentic Orchestrator

## License header (required on every new source file)

Every new source file (`*.go`, `*.sh`, `*.py`) must begin with the Apache 2.0
notice below. For shell/Python files keep any shebang on line 1 and place the
block immediately after it. For Go files place the block above the `package`
clause; if the file has a package doc comment, separate the copyright block
from the doc comment with one blank line so the doc comment still attaches to
`package`.

```
Copyright <YEAR> DoorDash, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
```

Use `//` as the comment prefix for `.go` files and `#` for `.sh` / `.py`.
Set `<YEAR>` to the year the file is first authored.

## Verification tiers

Run the fast suite before every handoff. Add the extended gates that match the
area you touched, and always record the tier names in the PR description.

| Tier | Command | Current wall time | When to run |
|------|---------|-------------------|-------------|
| Fast suite | `make test-fast` | 23s, target <=30s | Run before every handoff; this is the everyday all-package short-mode check. |
| E2E smoke shell | `bash test/e2e/smoke.sh` | 48.53s | Run when touching launch behavior, embedded skills, or release packaging. |
| Isolated integration | `go test ./test/integration/... -count=1` | 323.06s | Run when touching lifecycle, state-machine, runs layout, or protocol-violation behavior. |
| E2E Go (process-launch / API-driven) | `go test ./test/e2e/... -count=1 -race` | 41.51s | Run when touching TUI, Bubble Tea model behavior, or session lifecycle. |
| TUI observability | `go test -tags tui_observe ./internal/tui -run 'Observed|Emits' -count=1` | 15.14s | Run when touching TUI observer wiring, emitted observability events, or feature-span propagation. |
| Race regression | `go test ./... -count=1 -race` | 158.82s | Run before merging high-risk changes or concurrency-sensitive work. |
| Eval | `AGENTIC_EVAL=1 go test ./test/eval/... -count=1` | gated; not measured | Run only when validating live skill/guideline discovery against real LLM CLIs. |

Static analysis and build checks still apply:

```bash
go vet ./...
go build ./...
```

The default tiers do not require build tags. The tagged **TUI observability**
gate is the explicit opt-in check for slower observer-backed TUI integration
coverage. The fast suite uses the existing `testing.Short` guards and
intentionally omits the race detector. The race-enabled all-package sweep is the
extended **Race regression** gate, not the everyday unit command. The baseline
timing report lives at `docs/testing-baseline.md`.

TUI package tests in `internal/tui` are part of the fast suite and must stay at
the model layer: drive `APIAppModel.Init`, `Update`, `View`, subcomponent
reducers, keyboard handlers, and event translators directly. Full Bubble Tea
program drivers, process-launch, and terminal-lifecycle smoke flows belong in
the extended `test/e2e` gate with `testing.Short` guards.

## Web UI compatibility with the TUI

Web UI and REST API changes must preserve existing TUI behavior. The browser UI
is an additional client for the same Agentico runtime; it must not introduce
read-path side effects, background polling, recovery scans, or mutations that
can change feature/session state behind the TUI's back. In particular, `GET`
endpoints consumed by the Web UI should be observational unless their contract
explicitly documents a reconciliation side effect that is safe while another
client is active.

When touching `web/`, `internal/server/`, API client code, SSE/refresh logic,
recovery, setup, or feature lifecycle actions, add or update coverage that
exercises the API-backed TUI path as well as the browser path. At minimum, run
the relevant targeted TUI/server tests plus `make test-fast`; add the E2E Go
gate when the change affects Bubble Tea behavior, launch flow, session
lifecycle, recovery, or setup.

## Test isolation and parallelism

Tests are disqualified from `t.Parallel()` when they touch package-level
mutable globals, including timeouts and golden update flags; mutate the process
environment or working directory; depend on global config paths or shared
on-disk fixtures; or own long-running subprocess or session state. In short:
package-level mutable globals, process environment or working directory,
global config paths or shared on-disk fixtures, and long-running subprocess or
session state.

Tests are good parallel candidates when they exercise pure functions,
read-only fixtures, independent t.TempDir() per test, or isolated table cases that copy
their case value before calling `t.Parallel()`. Prefer `t.Setenv` over
`os.Setenv`, `t.TempDir` over ad-hoc temp dirs, and `t.Cleanup` that waits on
observable conditions such as done channels, wait groups, manager shutdown, or
bounded process-exit signals instead of fixed `time.Sleep` drains. For session
timeouts and other mutable behavior, use per-test option overrides rather than
changing package-level defaults.

## PR verification note

PR descriptions should include a short `Verification` note naming each tier run
from the table above. Name any intentionally skipped relevant tier with a
one-sentence reason, for example: `Skipped Race regression: docs-only change`.

## Regenerating golden templates

Prompt templates have byte-exact `.golden` snapshots in
`internal/agent/prompts/testdata/`. After intentionally editing a `.tmpl`,
regenerate and review the diff:

```bash
go test ./internal/agent/prompts/... -update
```

Commit the updated `.golden` files alongside the template change.

## Isolated run (second instance)

`agentico` has no single-instance lock. A second instance launched against the
default state dir scans for `session*.pid` files, finds the first instance's
live sessions, and shows the **Session Recovery** screen — selecting Kill or
Resume sends SIGTERM to the first instance's process group.

To run a second instance against a real workspace without disturbing the first,
isolate **both** state and config:

```bash
agentico --config /tmp/agentico2/config.yaml --state-dir /tmp/agentico2/features
```

Sibling dirs (`permissions/`, `worktrees/`, `skills/`, `guidelines/`,
`agentico.log`) are derived from `filepath.Dir(--state-dir)`. Always pass a
`<parent>/features` path — not the parent itself — so they stay grouped under
`<parent>/`.
