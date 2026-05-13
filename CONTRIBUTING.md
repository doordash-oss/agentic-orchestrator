# Contributing to Agentic Orchestrator

Thanks for your interest in contributing! This document explains how to get a change accepted into Agentic Orchestrator.

## Contributor License Agreement (CLA)

Before we can merge any contribution, you must accept the [Agentic Orchestrator Contributor License Agreement](CLA.md). The CLA grants DoorDash the rights it needs to redistribute your contribution under the project's [Apache 2.0 license](LICENSE.txt) and protects both you and downstream users.

You only need to sign the CLA once. The same signature covers all of your future contributions to this repository.

### How to sign

We use [cla-bot](https://colineberhardt.github.io/cla-bot/) to check signatures on every pull request. When you open your first PR, the bot will comment if your GitHub username is not yet on file.

To sign, open a separate pull request that adds your GitHub username to the `contributors` array in [`.clabot`](.clabot), with this exact line in the PR description:

> I have read and agree to the Agentic Orchestrator Contributor License Agreement (CLA.md, Version 1.0).

If you are contributing on behalf of an employer, confirm that you have authority to do so under section 4 of the CLA. Once that PR is merged, cla-bot will recheck your contribution PRs automatically (you can also comment `@cla-bot check` to trigger a recheck).

If your contribution includes material that is **not** your original creation, follow section 7 of the CLA: submit it separately, identify the source and license, and mark it conspicuously as "Not a Contribution."

## Reporting Issues

Use [GitHub Issues](https://github.com/doordash-oss/agentic-orchestrator/issues) for bug reports and feature requests. Include:

- The version of `agentico` you are running (`agentico --version`)
- Your OS and architecture
- A minimal reproduction (commands run, expected vs. actual behavior)

## Development Setup

See [README.md](README.md#prerequisites) for prerequisites and [AGENTS.md](AGENTS.md) for the full verification playbook.

```bash
git clone https://github.com/doordash-oss/agentic-orchestrator.git
cd agentic-orchestrator
make install        # builds and installs the agentico binary
make test-fast      # runs the everyday fast suite
```

## Verification

Run the **Fast suite** before every handoff. The extended gates remain available
by named tier, with current timings captured in [docs/testing-baseline.md](docs/testing-baseline.md).

| Tier | Command | Current wall time | Purpose |
|------|---------|-------------------|---------|
| Fast suite | `make test-fast` | 23s, target <=30s | Everyday local confidence check over all packages in short mode. |
| E2E smoke shell | `bash test/e2e/smoke.sh` | 48.53s | Binary, CLI flags, and embedded skill layout smoke coverage. |
| Isolated integration | `go test ./test/integration/... -count=1` | 323.06s | Lifecycle, state-machine, and protocol-violation coverage. |
| E2E Go (TUI / teatest) | `go test ./test/e2e/... -count=1 -race` | 41.51s | Full TUI and teatest behavior with the race detector. |
| TUI observability | `go test -tags tui_observe ./internal/tui -run 'Observed|Emits' -count=1` | 15.14s | Observer-backed TUI event and feature-span integration coverage. |
| Race regression | `go test ./... -count=1 -race` | 158.82s | Extended all-package race/regression sweep. |
| Eval | `AGENTIC_EVAL=1 go test ./test/eval/... -count=1` | gated; not measured | Live skill/guideline discovery against real LLM CLIs. |

`go vet ./...` and `go build ./...` are still required pre-push gates. The
race-enabled all-package sweep is the **Race regression** tier, not ordinary
unit testing. The default tiers do not require build tags; the tagged
**TUI observability** tier is the explicit opt-in gate for slower observer-backed
TUI integration coverage. The fast target runs all packages in short mode with
the existing `testing.Short` guards.

For new tests, follow the [Test isolation and parallelism](AGENTS.md#test-isolation-and-parallelism)
rules in `AGENTS.md`. The `parallel-candidate` / `parallel-exempt` comments in
`internal/feature` and `internal/session`, plus the `internal/tui`
`parallel_safety_test.go` inventory, are the authoritative classification for
those packages and should be updated as part of authoring the test.

## Submitting a Pull Request

1. **Branch naming** — use `feature/<short-name>`, `fix/<short-name>`, or `technical/<short-name>`.
2. **Commits** — one logical change per commit; subjects in sentence case, imperative mood, under 60 characters.
3. **Tests** — add or update unit, integration, or e2e tests as appropriate. New behavior without test coverage will generally be asked to add some.
4. **Lint and build** — `go vet ./...`, `go build ./...`, and the **Fast suite** must pass before you push.
5. **Verification note** — in the PR description, list the verification tier(s) run by name and name any intentionally skipped relevant tier with a one-sentence reason.
6. **Open the PR** — fill in the description, link any related issue, and (until CLA automation is in place) include the CLA acceptance line described above.
7. **Review** — a maintainer will review and may request changes. Once approved and verification is accepted, a maintainer will merge.

## Third-Party Dependencies

If your change adds, removes, or upgrades a Go module dependency, update [`NOTICE.txt`](NOTICE.txt) accordingly. New dependency licenses must be reviewed and approved by DoorDash Legal before the PR can be merged.

## Code of Conduct

Be respectful, assume good faith, and keep discussions focused on the work. Maintainers may close or moderate threads that drift from these principles.
