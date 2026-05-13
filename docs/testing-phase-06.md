# Testing Phase 6 Timing Report

This report records the Phase 6 `internal/orchestrator` contract-pruning pass.
Phase 1's baseline remains in `docs/testing-baseline.md`.

## Scope

This pass moved the orchestrator fast suite closer to the contract-and-decision
boundary from the phase plan. Fast tests still cover completion routing,
artifact-contract failures, failure-type preservation, event emission,
NeedUserInput dispatch, final-review routing, and mocked publish decisions.
Real-git scrub semantics, real Store/Manager recovery transitions, and
StartFeature-to-multi-repo lifecycle fan-out now run only in the extended
non-short orchestrator command.

No `t.Parallel()` calls were added in this phase.

## Internal Orchestrator Package

| Measurement | Before Phase 6 | Final Phase 6 | Budget |
|-------------|----------------|---------------|--------|
| Targeted short command wall time, `go test ./internal/orchestrator/ -short -count=1` | 13.712s Phase 1 package baseline | 1.64s timed wall / 1.115s package elapsed | 6s per package |
| Targeted short JSON profile, `go test -json ./internal/orchestrator/ -short -count=1` | not remeasured before edits | 1.102s package elapsed | 6s per package |
| Targeted full orchestrator command, `go test ./internal/orchestrator/ -count=1` | not re-baselined here | 5.176s | extended regression |

The package is now within the Phase 1 per-package fast-suite budget.

## Slowest Short-Mode Tests

| Test | Elapsed | Classification |
|------|---------|----------------|
| `TestDispatchMultiRepoResults_NeedUserInputRoutesPausedTerminalState` | 0.10s | kept by design: mocked NeedUserInput terminal dispatch and event-emission contract. |
| `TestOrchestrator_Shutdown_SignalsDoneAndStopsSessions` | 0.08s | kept by design: shutdown event-loop contract with bounded synchronization. |
| `TestStartMultiRepoImplementation_ReturnsSynchronously` | 0.05s | kept by design: StartMultiRepoImplementation async-return contract on mocked engine. |
| `TestStartMultiRepoImplementation_EngineError_Returns` | 0.05s | kept by design: mocked engine-error routing and no-dispatch contract. |
| `TestBuildHooks_PopulatedHooksEmit` | 0.03s | kept by design: hook-to-observer event contract. |

## Disposition Log

### Task 1: Real-git and real-manager tests

| Test | Disposition | Owner |
|------|-------------|-------|
| `TestFinalReviewRootArtifactScrub_RemovesOnlyUntrackedRootOrchestrationFiles` | short-gated | Extended orchestrator run owns real `git ls-files --others` scrub semantics. |
| `TestAdvanceAfterFinalReviewScrubsRootArtifactsBeforeCommitAll` | kept fast on mocks | Mocked command runner proves root artifact scrub happens before publish `CommitAll`; real git semantics are owned by `TestFinalReviewRootArtifactScrub_RemovesOnlyUntrackedRootOrchestrationFiles`. |
| `TestAdvanceAfterFinalReviewRoadmapFinalScrubsRootArtifactsBeforeCommitAll` | kept fast on mocks | Mocked command runner proves every touched repo is scrubbed before final publish; real git semantics are owned by `TestFinalReviewRootArtifactScrub_RemovesOnlyUntrackedRootOrchestrationFiles`. |
| `TestExecuteRecovery_Resume_InquiringFeature_RealManager_NoInvalidTransition` | short-gated | Extended orchestrator run owns real `feature.Store`/`feature.Manager` transition-rule regression coverage. |
| `TestExecuteRecovery_Resume_BuildingKBFeature_AllFresh_RealManager_NoInvalidTransition` | short-gated | Extended orchestrator run owns real Store/Manager plus real-git KB freshness transition coverage. |

### Task 2: Multi-repo lifecycle realism

| Test | Disposition | Owner |
|------|-------------|-------|
| `TestStartMultiRepoImplementation_HandlePhaseCompletionError_MarksFeatureFailed` | short-gated | Extended orchestrator run owns async dispatch error plumbing; fast completion-handler tests keep failure classification coverage. |
| `TestStartMultiRepoImplementation_PublishConflictDoesNotMarkFeatureFailed` | short-gated | Extended orchestrator run owns async publish-conflict dispatch; fast publish tests keep conflict sentinel behavior. |
| `TestStartFeature_ImplementPhase_AllPassed_CompletesFeature` | short-gated | Extended orchestrator run owns StartFeature-to-multi-repo lifecycle dispatch; fast StartMultiRepoImplementation tests keep routing contracts. |
| `TestStartFeature_ImplementPhase_Failed_MarksFeatureFailed` | short-gated | Extended orchestrator run owns StartFeature-to-multi-repo failure dispatch; fast HandlePhaseCompletion tests keep failure-event contracts. |
| `TestGrillMeFanout_StartPaths_DriveEntryPath` | short-gated | Extended orchestrator run owns broad grill-me start-path fan-out; fast prompt-capture and persistence-gate tests keep builder contract coverage. |

### Task 3: Duplicated coverage pruning

No tests were deleted in this pass. The phase kept existing fast contract tests
where they already asserted orchestrator-specific routing, failure
classification, status transition, or event emission. The real-git final-review
publish cases were narrowed to mocked command-runner assertions instead of
relocating them.

### Relocated Tests

No tests were relocated to `test/integration/` in this pass. The plan allowed
mocking or short-mode gating first, and every candidate fit one of those
dispositions.

## Verification Notes

- `go test ./internal/orchestrator/ -short -count=1` passed with package
  elapsed 1.115s and wall time 1.64s.
- `go test ./internal/orchestrator/ -count=1` passed, proving the short-gated
  extended owners still execute.
- The fast short run reports the eight gated tests as skipped.
- The non-short spot check for
  `TestFinalReviewRootArtifactScrub_RemovesOnlyUntrackedRootOrchestrationFiles`
  passed in 0.20s.
