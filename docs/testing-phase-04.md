# Testing Phase 4 Timing Report

This report records the Phase 4 `internal/feature` reduction pass.
Phase 1's baseline remains in `docs/testing-baseline.md`.

## Scope

This pass made the raw feature state-machine tables the canonical owner for
transition allow-list coverage, replaced manager-test git/gh subprocess usage
with existing hand-written mocks where the manager behavior is the subject
under test, consolidated deferral/pipeline helper tests, removed unused
carry-forward `*ForTest` hooks, and moved overlapping rewind regression variants
out of short mode.
The extended variants still run under `go test ./internal/feature/... -count=1`.

## Internal Feature Package

| Measurement | Before this pass | After this pass | Budget |
|-------------|------------------|-----------------|--------|
| Targeted short command wall time, `go test ./internal/feature/... -count=1 -short` | 2m37.60s | 1.884s JSON profile / 1.848s verification | 6s per package |
| Targeted short package elapsed | 157.299s | 1.884s | 6s per package |
| Targeted full feature command, `go test ./internal/feature/... -count=1` | not re-baselined | 31.089s | extended regression |

Post-pass slowest short-mode tests:

| Test | Elapsed |
|------|---------|
| `TestRewindToPhase_CarriesForwardCorrectPhases/implement_carries_prior+plan+roadmap+phase-NN/plan` | 0.52s |
| `TestRewindToPhase_StatusMapping/rewind_to_research` | 0.51s |
| `TestStoreConcurrentModify` | 0.04s |

Post-phase slowest-package ranking delta from `docs/testing-baseline.md`:

| Package | Phase 1 elapsed | Phase 4 elapsed | Budget state |
|---------|-----------------|-----------------|--------------|
| `github.com/doordash-oss/agentic-orchestrator/internal/git` | 99.230s | not remeasured here | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/agent` | 80.750s | not remeasured here | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/session` | 54.244s | not remeasured here | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/tui` | 32.942s | not remeasured here | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/ports` | 24.060s | not remeasured here | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/feature` | 69.800s | 1.848s verification / 1.884s JSON profile | within 6s |

## Extended Ownership

The following `internal/feature` scenarios are intentionally outside short mode
after this pass:

- overlapping rewind artifact-preservation, artifact-map, timing/cost, field
  reset, PR-close, backup-branch, review-state, crash-recovery, and idempotent
  reseal regressions
- pipeline-upgrade rewind escalation variants
- orphan-run cleanup edge-case matrix

The fast suite keeps representative rewind status and carry-forward cases.
The full feature command above confirms the extended scenarios still execute.

## Parallel Audit

Every retained `internal/feature` test now carries an in-source `parallel-candidate` marker. No `parallel-exempt` cases remain and this phase still adds no `t.Parallel()` calls.

Flat list for Phase 10:

| Test | File | Classification | Reason |
|------|------|----------------|--------|
| `TestRewindToPhase_CarryForwardArtifactPathEdges` | `internal/feature/carry_forward_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestClassify_FrontendKeywords` | `internal/feature/classify_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestClassify_ImagesImplyFrontend` | `internal/feature/classify_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestClassify_DedupAndSort` | `internal/feature/classify_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestClassify_WordBoundary` | `internal/feature/classify_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestClassify_RepoWithFrontendFiles` | `internal/feature/classify_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestClassify_PureBackendDescription` | `internal/feature/classify_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestHasTag` | `internal/feature/classify_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestStore_CleanupOrphanRuns` | `internal/feature/cleanup_orphan_runs_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestStore_CleanupOrphanRuns_EdgeCases` | `internal/feature/cleanup_orphan_runs_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestDeferralID_Stable` | `internal/feature/deferral_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestMergeDeferrals` | `internal/feature/deferral_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestDueForPhase_FiltersCorrectly` | `internal/feature/deferral_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestCloseDeferrals_TransitionsMatchingIDs` | `internal/feature/deferral_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestDeferral_YAMLRoundTrip` | `internal/feature/deferral_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestDeferral_YAMLRoundTripPreservesRepoScope` | `internal/feature/deferral_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestDeferral_YAMLOmitsEmptyRepoScope` | `internal/feature/deferral_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestDeferral_LegacyYAMLDecodesAsFeatureWide` | `internal/feature/deferral_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestDueForPhaseScopedTo` | `internal/feature/deferral_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestOpenDeferrals_SortedByDueByPhaseThenID` | `internal/feature/deferral_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestTransitionValid` | `internal/feature/feature_test.go` | parallel-candidate | pure state-machine table with no shared state. |
| `TestTransitionInvalid` | `internal/feature/feature_test.go` | parallel-candidate | pure state-machine table with no shared state. |
| `TestPhaseString` | `internal/feature/feature_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestPhaseDirName` | `internal/feature/feature_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestStatusString` | `internal/feature/feature_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestStatusYAMLRoundTrip` | `internal/feature/feature_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestStatusYAMLLegacyInt` | `internal/feature/feature_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestSlugify` | `internal/feature/feature_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestIsRunning` | `internal/feature/feature_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestTransitionAccumulatesTime` | `internal/feature/feature_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestTransitionDoesNotAccumulateWithinRunning` | `internal/feature/feature_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestTotalRuntimeWithPhaseTimings` | `internal/feature/feature_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestTotalRuntimeWithActivePhase` | `internal/feature/feature_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestTotalRuntimeLegacyFallback` | `internal/feature/feature_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestTotalRuntimeNoData` | `internal/feature/feature_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestPhaseRuntime` | `internal/feature/feature_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestTransitionToFailedAccumulatesTime` | `internal/feature/feature_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestTransitionToInterruptedAccumulatesTime` | `internal/feature/feature_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestResumeRebaseCyclePreservesTimingKey` | `internal/feature/feature_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestResumeTweakCyclePreservesTimingKey` | `internal/feature/feature_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestResumeReviewCommentsCyclePreservesTimingKey` | `internal/feature/feature_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestIsImplementTimingKey` | `internal/feature/feature_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestAddPhaseCost` | `internal/feature/feature_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestAddPhaseCostAccumulates` | `internal/feature/feature_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestAddPhaseCostZeroNegativeNoop` | `internal/feature/feature_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestPhaseCostNilMap` | `internal/feature/feature_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestPhaseCostsYAMLRoundTrip` | `internal/feature/feature_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestPhaseCostsOmittedWhenNil` | `internal/feature/feature_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestNormalFlowResetsTimingKeyForImplement` | `internal/feature/feature_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestPhaseKnowledgeBase` | `internal/feature/feature_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestLogicalOrder` | `internal/feature/feature_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestStatusBuildingKB` | `internal/feature/feature_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestKBStatusPersistence` | `internal/feature/feature_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestInquireResearchBrainstormPlanProgression` | `internal/feature/feature_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestKBTimingAccumulation` | `internal/feature/feature_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestCheckpointsHasGateForPhase` | `internal/feature/feature_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestCheckpointsAutoPublish` | `internal/feature/feature_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestCheckpointsYAMLRoundTrip` | `internal/feature/feature_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestRoadmapFieldsYAMLRoundTrip` | `internal/feature/feature_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestRoadmapFieldsOmittedWhenZero` | `internal/feature/feature_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestCheckpointsZeroValueFromYAML` | `internal/feature/feature_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestRepoStateYAMLRoundTrip` | `internal/feature/feature_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestValidTransitionsNoRetiredStates` | `internal/feature/feature_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestFirstRepoPRURL` | `internal/feature/feature_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestAllReposPublished` | `internal/feature/feature_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestTouchedRepos` | `internal/feature/feature_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestIsRefactoring` | `internal/feature/feature_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestRefactorPrefix` | `internal/feature/feature_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestCyclePrefix` | `internal/feature/feature_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestRepoCycleDirName` | `internal/feature/feature_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestEffectiveDescription` | `internal/feature/feature_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestEffectivePipeline` | `internal/feature/feature_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestPipelineYAMLRoundTrip` | `internal/feature/feature_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestPipelineYAMLBackwardCompat` | `internal/feature/feature_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestIsPublishable` | `internal/feature/feature_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestEffectivePhases` | `internal/feature/feature_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestFeature_HasActiveRepoCycles` | `internal/feature/feature_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestPendingUserInputCycles_ReturnsPausedCycles` | `internal/feature/feature_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestPendingUserInputCycles_NilOrEmpty` | `internal/feature/feature_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestIsReviewing` | `internal/feature/feature_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestPRURLs` | `internal/feature/feature_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestPRURLs_NoLegacyFallback` | `internal/feature/feature_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestManagerCreate` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestManagerCreateStampsSchemaVersionAndPrePopulatesRun` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestManagerLifecycleTransitions` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestManagerUpdateIteration` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestManagerCreateWithWorktree` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestManagerCreateWithoutWorktree` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestManagerPhaseProgression` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestManagerMarkPublished` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestManagerMarkFailed` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestManagerCreateWithImages` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestManagerReturnToPublished` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestMarkPublished_NonPublishableEmptyURLAccepted` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestReturnToPublished_PublishableNoURLRejected` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestReturnToPublished_RepoLevelURLAccepted` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestReturnToPublished_NonPublishableNoURL` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestManagerRecreateWorktree` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestManagerFullRebaseCycle` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestManagerCreateWithAutoPublish` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestManagerCreateWithoutAutoPublish` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestManagerCreateNoImages` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestManagerCreateWithAttachments` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestManagerCreateNoAttachments` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestManagerCreateDeduplicatesUpstreamBranch` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestManagerCreateNoSuffixWhenNoUpstreamConflict` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestSlugExists` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestCreateDuplicateSlug` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestRewindToPhase_StatusMapping` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestRewindToPhase_ArtifactsPreservedInSealedRun` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestRewindToPhase_ArtifactMap_ForwardCarriesCorrectSubset_SealedPreserved` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestRewindToPhase_TimingsCostsForkedEmpty_Run001Preserved` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestRewindToPhase_PRURLCleared` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestRewindToPhase_BackupBranchWarning` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestRewindablePhases` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestRewindToPhase_FieldsZeroed` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestRewindToPhase_InvalidTargetFromCurrentState` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestAdvanceRoadmapPhase` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestAdvanceRoadmapPhaseTDDFillIn` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestAdvanceRoadmapPhaseCollapsed` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestStartRoadmapPhaseImplementation` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestCompleteRoadmap` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestRestartFromBeginningClearsRoadmapFields` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestRoadmapPhaseTimingKeyTransitions` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestRoadmapPhasePlanTimingSurvivesInterruptRestart` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestInitRepoImpl` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestInitKBStatus` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestMarkRepoKBCompleted` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestMarkRepoKBFailed` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestAllKBsCompleted` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestSetRepoPublished` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestSetRepoPublishError` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestSetRepoPublished_ClearsLastError` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestTryCompletePublish_AllReady` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestTryCompletePublish_NotAllReady` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestTryCompletePublish_FeatureAtPRReady` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestTryCompletePublish_WrongStatus` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestRewindToPhase_ClearsRepoImpl` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestRewindToPhase_MediumPlanWritesDescriptionReview` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestRewindToPhase_LargePlanDoesNotWriteDescriptionReview` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestRewindToPhase_ClosesPerRepoPRs` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestRewindToPhase_ImplementResetsMultiRepoState` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestRepoCycleMethods` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestManagerCompleteRefactor` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestCreateMediumFeature` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestCreateMoonshotFeature` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestCreateDefaultPipeline` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestCreateInvalidDefaultsPipeline` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestCreateEmptyDefaultsPipelineFallsBackToMoonshot` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestCreateExplicitPipelineOverridesDefault` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestCreateFeatureWithDiscoveredRepo` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestCreateFeatureExplicitRepoStillWorks` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestRewindChoicesForFeature_OnlyCurrentPipelinePhases` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestRewindChoicesForFeature_MoonshotNoEscalation` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestRewindChoicesForFeature_StandardNoEscalation` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestRewindChoicesForFeature_MediumAtPlanning` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestRewindToPhase_MediumRejectsNonMediumPhase` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestUpgradeThenRewind_MediumToStandard` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestRewindToPhase_NoEscalationWithinProfile` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestRewindToPhase_StandardToMoonshotNotNeeded` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestUpgradePipeline_MediumToStandard` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestUpgradePipeline_StandardToMoonshot` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestUpgradePipeline_MoonshotRejectsUpgrade` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestUpgradePipeline_CannotDowngrade` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestUpgradeThenRewind_MediumToStandard_ResearchGoesToKB` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestUpgradeThenRewind_MediumToMoonshot_BrainstormGoesToKB` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestUpgradeThenRewind_StandardToMoonshot_NoKBEscalation` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestManagerMarkDone` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestManagerCreateSetsPublishable` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestRewindToPhase_SkipsClosePRForUnpublished` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestRewindToPhase_ClosePRStillCalledForPublished` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestRestartFromBeginning_UnpublishedUsesLocalReset` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestRewindToPhase_UnpublishedUsesLocalReset` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestManagerCreateSkipsBranchCheckForUnpublishedRepos` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestRewindToPhase_FromReviewing_StatusMapping` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestRewindToPhase_FromReviewing_ReviewArtifactsPreservedInSealedRun` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestRewindToPhase_FromReviewing_ClearsReviewIteration` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestRewindToPhase_NotFromReviewing_ReviewDirPreservedInSealedRun` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestRewindToPhase_FromReviewing_ForksEmptyTimings` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestRewindToPhase_FromReviewing_InvalidTarget` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestMarkRepoCycleReviewing` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestRemoveRepoCycle` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestHasActiveRepoCycles_TreatsNeedUserInputAsActive` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestFailRepoCycle_ClearsPausedGateAndRefactorPrompt` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestHasActiveRepoCycles_ReviewingIsActive` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestInitRepoImpl_PreservesExistingState` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestInitRepoImpl_PrunesRemovedRepos` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestInitRepoImpl_AddsMissingEntriesAsPending` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestRewindToPhase_CarriesForwardCorrectPhases` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestRewindToPhase_ToPhaseImplement_CarriesPlanDirs` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestRewindToPhase_RecordsBackupBranches` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestRewindToPhase_PartialBranchFailureStillSeals` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestRewindToPhase_ArtifactMapCarriedForward` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestRewindToPhase_CrashBeforeCommitFlagCleared_CleanedUpOnStartup` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestRewindToPhase_ReSealAfterCleanup_IdempotentSealFields` | `internal/feature/manager_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestPipelineProfileIsValid` | `internal/feature/pipeline_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestPipelineProfilePhaseProgression` | `internal/feature/pipeline_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestPipelineProfileNextProfile` | `internal/feature/pipeline_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestShouldSkipPlanValidation` | `internal/feature/pipeline_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestShouldSkipIterationReview` | `internal/feature/pipeline_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestDefaultCheckpointsForProfile` | `internal/feature/pipeline_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestPipelineProfileApplicableGates` | `internal/feature/pipeline_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestProjectGatesNormalizeCheckpointsForPersistence` | `internal/feature/pipeline_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestPipelineProfileFilterCheckpoints` | `internal/feature/pipeline_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestProjectMergedGates` | `internal/feature/pipeline_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestParsePipelineProfileValid` | `internal/feature/pipeline_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestPipelineEffortLevel` | `internal/feature/pipeline_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestParsePipelineProfileInvalid` | `internal/feature/pipeline_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestConfigCheckpointsToFeatureCheckpoints` | `internal/feature/pipeline_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestFeatureCheckpointsToConfigCheckpoints` | `internal/feature/pipeline_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestMinimumProfileForPhase` | `internal/feature/pipeline_test.go` | parallel-candidate | pure value, table-driven, or per-test temp-dir assertions with no shared state. |
| `TestStoreSaveAndLoad` | `internal/feature/store_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestStoreList` | `internal/feature/store_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestStoreListPartialLoad` | `internal/feature/store_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestStoreDelete` | `internal/feature/store_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestStoreNeedUserInputPathPersistence` | `internal/feature/store_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestStoreFailureFieldsPersistence` | `internal/feature/store_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestStoreMuteInputNotificationsPersistence` | `internal/feature/store_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestStoreConcurrentModify` | `internal/feature/store_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestStoreConcurrentRepoImplModify` | `internal/feature/store_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestPublishableFieldYAMLRoundTrip` | `internal/feature/store_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestStore_SealAndForkRun_CommittingLifecycle_SkeletonPersistedBeforePopulate` | `internal/feature/store_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestStore_SealAndForkRun_CommittingLifecycle_PopulateCanMutateNewRun` | `internal/feature/store_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestStore_SealAndForkRun_PopulateErrorLeavesSkeleton` | `internal/feature/store_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestStore_SealAndForkRun_IdempotentReseal` | `internal/feature/store_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestStore_SealAndForkRun_ForkMustSetCommitting` | `internal/feature/store_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestStoreRoundTripsSchemaVersion` | `internal/feature/store_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestStoreLoadAcceptsCurrentSchemaVersion` | `internal/feature/store_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |
| `TestStoreLoadCurrentSchemaIgnoresLegacyJiraKeys` | `internal/feature/store_test.go` | parallel-candidate | per-test temp dirs and mocks isolate filesystem and collaborator state. |

## Regeneration Sequence

Run from the repository root on a warm build cache:

```bash
go test ./internal/feature/... -count=1 -short
go test ./internal/feature/... -count=1
go test -json ./internal/feature/... -short -count=1 > /private/tmp/agentic-feature-phase4.json
jq -r 'select(.Action=="pass" and .Test != null and .Elapsed != null) | [.Elapsed, .Test] | @tsv' /private/tmp/agentic-feature-phase4.json | sort -nr | head
```
