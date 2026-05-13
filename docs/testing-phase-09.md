# Testing Phase 9 Timing Report

This report records the Phase 9 docs and launch-surface contract trim. Phase 1's
baseline and the 6s per-package fast-suite budget remain in
`docs/testing-baseline.md`.

## Scope

`cmd/agentico/testdata/scripts/launch_contract.txtar` now exercises only the
current binary launch surface: help text, version output, and startup provider
validation. Removed command-era negative paths stay pinned by
`TestParseLaunchArgsRejectsRemovedSurface`.

The migration-era file-scanning meta-tests were trimmed from `cmd/agentico/`.
Current docs contracts remain in place for the TUI-only launch story, renamed
product story, and smoke-shell launch path.

## Fast Suite

| Measurement | Before Phase 9 | Final Phase 9 |
|-------------|----------------|---------------|
| Fast-suite wall time, `make test-fast` | 31s | 30s |
| `cmd/agentico` package elapsed in `make test-fast` | 12.803s | 3.071s |
| Per-package budget | 6s | within budget |

The fast-suite total did not regress. The `cmd/agentico` short-mode package
profile is now below the Phase 1 per-package budget. Local Go build-cache state
still dominates some package-level wall-clock samples, so the direct contract
trim evidence is also recorded below.

## `cmd/agentico` Timing Split

| Measurement | Before Phase 9 | Final Phase 9 |
|-------------|----------------|---------------|
| Full package command, `go test -json ./cmd/agentico -count=1` | 3.223s package elapsed | 3.142s package elapsed |
| `TestCLIScripts/launch_contract` in full package command | 1.912s | 1.239s |
| Targeted `TestCLIScripts`, `go test -json ./cmd/agentico -run TestCLIScripts -count=1` | 2.696s package elapsed / 1.490s script elapsed | 3.562s package elapsed / 0.956s script elapsed |
| Targeted in-process contract tests | 1.926s package elapsed | 1.311s package elapsed |

The package-level targeted `TestCLIScripts` command varied upward after the
trim because its wall time includes compile and test binary startup cost. The
script body itself is smaller and the isolated post-trim script elapsed is under
one second.

## Trim Counts

| Count | Before Phase 9 | Final Phase 9 |
|-------|----------------|---------------|
| `exec` / `! exec` blocks in `launch_contract.txtar` | 8 | 3 |
| Top-level `Test*` funcs in `cmd/agentico/*_test.go`, including `TestMain` | 20 | 18 |
| Runnable test funcs excluding `TestMain` | 19 | 17 |

Post-trim `launch_contract.txtar` has exactly one `exec agentico --help`, one
`exec agentico --version`, and one provider-validation `! exec agentico ...`
block.

## Retired Contracts

| Retired check | Retained owner |
|---------------|----------------|
| `! exec agentico run` | `TestParseLaunchArgsRejectsRemovedSurface/run_alias` |
| `! exec agentico feature list` | `TestParseLaunchArgsRejectsRemovedSurface/feature_list` |
| `! exec agentico feature create --name ...` | `TestParseLaunchArgsRejectsRemovedSurface/feature_create`; top-level `--name` remains covered by `feature_create_flag_at_top_level` |
| `! exec agentico --config ... --state-dir ... feature create --jira ...` | `TestParseLaunchArgsRejectsRemovedSurface/feature_create` |
| `! exec agentico --refresh-models` | `TestParseLaunchArgsRejectsRemovedSurface/refresh_models` |
| `TestDirectFeatureCommandImplementationRetired` | `TestParseLaunchArgsRejectsRemovedSurface` and `TestRunArgsLaunchesTUIByDefault` own the user-facing parser/default-launch contract |
| `TestLegacyCommandFixtureQuarantineRetired` | Trimmed `launch_contract.txtar` owns the active script set; unused quarantine files would not add current-surface coverage |
| `TestUserFacingDocsAdvertiseRenamedProduct` stale-pattern scan | `TestUserFacingDocsDescribeTUIOnlyLaunchSurface` owns removed command examples in user-facing launch docs |

## Manual Spot Checks

- Temporarily added `agentico feature create --name demo` to `README.md`;
  `TestUserFacingDocsDescribeTUIOnlyLaunchSurface` failed on the banned-token
  list for `agentico feature create`. The README mutation was reverted.
- Temporarily changed `parseLaunchArgs` to accept `feature` as a no-op
  subcommand; `TestParseLaunchArgsRejectsRemovedSurface/feature_list` and
  `/feature_create` failed with nil errors. The parser mutation was reverted.
- Temporarily added a `Commands:` section to the live help output;
  `TestCLIScripts/launch_contract` failed on the retained
  `! stdout 'Commands:'` assertion. The help mutation was reverted.

## Verification Notes

- `go test ./cmd/agentico/... -count=1 -run TestCLIScripts` passed after the
  fixture trim.
- `go test ./cmd/agentico/... -count=1 -run TestParseLaunchArgsRejectsRemovedSurface`
  passed after confirming every retired script block has parse-layer coverage.
- `go test ./cmd/agentico/... -count=1 -run 'Test(UserFacingDocsDescribeTUIOnlyLaunchSurface|UserFacingDocsAdvertiseRenamedProduct|SmokeScriptDocsRetainRenamedSurface)'`
  passed after the meta-test trim.
- `make test-fast` passed with final wall time 30s.
