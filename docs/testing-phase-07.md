# Testing Phase 7 Timing Report

This report records the Phase 7 TUI two-layer split. Phase 1's baseline remains
in `docs/testing-baseline.md`.

## Scope

Fast TUI coverage is locked to direct model-layer tests in `internal/tui`:
`AppModel.Init`, `Update`, `View`, subcomponent reducers, keyboard handlers, and
event translators. Full Bubble Tea program drivers remain in the extended
`test/e2e` gate and are skipped in short mode.

No `t.Parallel()` calls were added in this phase.

## Fast Suite

| Measurement | Before Phase 7 | Final Phase 7 |
|-------------|----------------|---------------|
| Fast-suite wall time, `go test ./... -short -count=1` | 115s Phase 1 baseline | 18.19s |
| TUI fast command wall time, `go test ./internal/tui/... -short -count=1` | 32.942s Phase 1 `internal/tui` package baseline | 9.63s timed wall |
| `internal/tui` package elapsed | 32.942s Phase 1 package baseline | 8.830s |
| `internal/tui/markdown` package elapsed | 13.980s Phase 1 package baseline | 1.175s |
| Per-package budget | 6s | above budget |

The TUI package is substantially faster than the Phase 1 baseline, but it still
exceeds the 6s per-package budget. The budget was not expanded. Phase 10 should
use the parallel-safety inventory in `internal/tui/parallel_safety_test.go` to
parallelize isolated model tests, and should either stub or extended-gate the
remaining host/real-git probes that still sit in the package.

## Slowest Short-Mode TUI Tests

| Test | Elapsed | Classification |
|------|---------|----------------|
| `TestPublishModel_StepCounter_TruthTable` | 0.45s | kept fast: publish model step-count contract; Phase 10 candidate for parallel table subtests. |
| `TestNewPublishModel_ChromeGateConsultsHelper` | 0.36s | kept fast: publish model chrome-gate selection contract; Phase 10 candidate for parallel table subtests. |
| `TestWizardProvisionalPublishabilityFromRemote` | 0.35s | kept fast: wizard publishability model state; can be audited for smaller fixture setup. |
| `TestTransitionToPublish_SingleRepo` | 0.21s | kept fast: TUI transition into publish model for one repo. |
| `TestStartRepoCycleRefactorCmdCreatesArtifactDir` | 0.21s | kept fast for artifact staging, but Phase 10 should review whether the filesystem portion belongs in an extended owner. |

## Extended TUI Smoke

| Measurement | Before Phase 7 | Final Phase 7 |
|-------------|----------------|---------------|
| E2E Go TUI gate | 41.51s Phase 1 race-enabled baseline | 25.47s non-race plan command |

The retained full-program smoke inventory is:

- `TestFreshFeatureSkeletonInvariants`
- `TestMediumWizardGateProjectionSmoke`
- `TestNeedUserInputPauseResumeSmoke`
- `TestDashboardRendersEmptyState`
- `TestDashboardShowsFeature`
- `TestTUI_FeatureFailsMidChain`
- `TestTUI_SessionCrashInterrupted`
- `TestTUI_ConcurrentFeatures`
- `TestTUI_PermissionPromptSurfaced`
- `TestTUI_HelpInputBlocking`

`test/e2e/smoke_contract_test.go` now checks this inventory so renamed or added
smoke flows drift loudly.

## Rendering Spot Check

Temporarily changing the empty-dashboard hint from `Press  n  to start.` to
`Press n to start.` and rerunning `go test ./internal/tui/... -short -count=1`
failed only:

- `TestGhostCTAWelcomePanel`
- `TestRenderWelcomePanel`

The hint was restored and the TUI fast command passed afterward.

## Verification Notes

- `go test ./internal/tui/... -short -count=1` passed with 9.63s wall time.
- `go test ./... -short -count=1` passed with 18.19s wall time.
- `go test ./test/e2e/... -count=1` passed with 25.47s wall time.
- `go build ./...` and `go vet ./...` passed.
- `! grep -RE "tea\.NewProgram|teatest" internal/tui --include='*_test.go'` passed.
