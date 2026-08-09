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

See [`docs/testing-baseline.md`](docs/testing-baseline.md) for the canonical
verification tier list, commands, timings, and when-to-run guidance.

Static analysis and build checks still apply:

```bash
go vet ./...
go build ./...
```

The default tiers do not require build tags. The fast suite uses the existing `testing.Short` guards and
intentionally omits the race detector. The race-enabled all-package sweep is the
extended **Race regression** gate, not the everyday unit command. The baseline
timing report lives at `docs/testing-baseline.md`.

Electron renderer, main-process, security, and packaged tests live under
`desktop/` and run through the npm scripts in the root package. Go process-launch
and server-lifecycle coverage belongs in `test/e2e` with `testing.Short` guards.

## Desktop UI changes must keep the packaged journeys green

The Playwright journeys in `desktop/test/e2e/journeys/` assert on user-visible
text, ARIA roles, and accessible names. They are the UX contract: any
intentional change to renderer copy, roles, `aria-label`s, dialog/section
names, or button labels **will** break them, and unit tests will not catch it.

Before handing off any change under `desktop/src/renderer/` (and any preload
or IPC surface change):

1. Grep the journeys for every string, label, or role name you changed or
   removed: `grep -rn '<old string>' desktop/test/e2e/journeys/`.
2. Update the affected specs alongside the UI change — they are part of the
   change, not a follow-up.
3. Run at least the affected specs against the packaged app. You do not need
   the full suite; a targeted run is cheap after the one-time package build:

   ```bash
   cd desktop && npm run test:e2e:packaged -- test/e2e/journeys/<spec>.spec.ts -g "<test title>"
   ```

Skipping this because "the packaged tier runs on CI" is how CI ends up red:
CI is where these failures are most expensive to diagnose, not a substitute
for running them.

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
from the canonical verification baseline. Name any intentionally skipped
relevant tier with a one-sentence reason, for example: `Skipped Race regression:
docs-only change`.

## Regenerating golden templates

Prompt templates have byte-exact `.golden` snapshots in
`internal/agent/prompts/testdata/`. After intentionally editing a `.tmpl`,
regenerate and review the diff:

```bash
go test ./internal/agent/prompts/... -update
```

Commit the updated `.golden` files alongside the template change.

## Isolated server run

`agentico` starts the headless server in the foreground. It has no desktop
dashboard or session-recovery screen. The desktop app normally supervises its
own bundled server and can instead attach to a compatible externally owned
server without taking ownership of that process.

To run an isolated server against a real workspace without disturbing another
runtime, isolate **both** state and config:

```bash
agentico server --config /tmp/agentico2/config.yaml --state-dir /tmp/agentico2/features
```

Sibling dirs (`permissions/`, `provider-state/`, `worktrees/`, `skills/`,
`guidelines/`, `agentico.log`) are derived from `filepath.Dir(--state-dir)`. Always pass a
`<parent>/features` path — not the parent itself — so they stay grouped under
`<parent>/`.
