// Copyright 2026 DoorDash, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package tui

// tui_boundary_test.go enforces the architectural boundary between the TUI
// (internal/tui/app.go) and the orchestrator / domain layers.
//
// The rule: app.go is a thin Bubbletea shell. Every feature/phase/repo state
// mutation, every observer emission, every repo-mutating git call, and every
// recovery dispatch must route through m.orchestrator.*. The TUI retains only
// UI concerns (rendering, key handling, modal state) and thin command wrappers
// that forward to the orchestrator.
//
// These invariants cannot be expressed at compile time, so this file scans
// internal/tui/app.go (and neighbors) as source text and AST. A regression
// here typically means a new handler slipped a direct featureManager / git /
// observer call past review.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// repoRoot returns the repository root (the path containing go.mod).
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not locate repo root from " + file)
		}
		dir = parent
	}
}

func readFileOrFatal(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func appGoPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(repoRoot(t), "internal", "tui", "app.go")
}

func orchAPIPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(repoRoot(t), "internal", "tui", "orchestrator_api.go")
}

func parseAppGo(t *testing.T) (*ast.File, *token.FileSet) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, appGoPath(t), nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse app.go: %v", err)
	}
	return f, fset
}

func findFuncDecl(file *ast.File, name string) *ast.FuncDecl {
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if fd.Recv == nil || len(fd.Recv.List) == 0 {
			continue
		}
		if fd.Name.Name == name {
			return fd
		}
	}
	return nil
}

func funcBodyText(t *testing.T, fset *token.FileSet, fd *ast.FuncDecl, srcPath string) string {
	t.Helper()
	if fd == nil || fd.Body == nil {
		return ""
	}
	data, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatalf("read %s: %v", srcPath, err)
	}
	start := fset.Position(fd.Body.Pos()).Offset
	end := fset.Position(fd.Body.End()).Offset
	if start < 0 || end > len(data) || start >= end {
		t.Fatalf("invalid body offsets for %s: start=%d end=%d len=%d", fd.Name.Name, start, end, len(data))
	}
	return string(data[start:end])
}

// ---------------------------------------------------------------------------
// Orchestrator call-site floor. The TUI must delegate to m.orchestrator.* a
// minimum number of times. A sharp drop signals that a handler lost its
// delegation.
// ---------------------------------------------------------------------------

func TestTUIBoundary_OrchestratorDelegation_MinCallSites(t *testing.T) {
	t.Parallel()
	src := readFileOrFatal(t, appGoPath(t))
	n := strings.Count(src, "m.orchestrator.")
	const floor = 10
	if n < floor {
		t.Errorf("m.orchestrator.* call count = %d, want >= %d — a handler lost its orchestrator delegation", n, floor)
	}
}

// ---------------------------------------------------------------------------
// Delegations that must remain in place. Each handler must contain its
// orchestrator call.
// ---------------------------------------------------------------------------

func TestTUIBoundary_HandlePhaseCompleted_DelegatesToOrchestrator(t *testing.T) {
	t.Parallel()
	file, fset := parseAppGo(t)
	fd := findFuncDecl(file, "handlePhaseCompleted")
	if fd == nil {
		t.Fatal("handlePhaseCompleted not found in app.go")
	}
	body := funcBodyText(t, fset, fd, appGoPath(t))
	if !strings.Contains(body, "m.orchestrator.HandlePhaseCompletion(") {
		t.Errorf("handlePhaseCompleted body missing m.orchestrator.HandlePhaseCompletion call; body:\n%s", body)
	}
}

func TestTUIBoundary_HandleMultiRepoImplDone_FailureDelegates(t *testing.T) {
	t.Parallel()
	file, fset := parseAppGo(t)
	fd := findFuncDecl(file, "handleMultiRepoImplDone")
	if fd == nil {
		t.Fatal("handleMultiRepoImplDone not found in app.go")
	}
	body := funcBodyText(t, fset, fd, appGoPath(t))
	if !strings.Contains(body, "m.orchestrator.HandlePhaseCompletion(") {
		t.Errorf("handleMultiRepoImplDone body missing m.orchestrator.HandlePhaseCompletion call (expected on failure branch)")
	}
}

func TestTUIBoundary_HandleGateReviewDecision_DelegatesToOrchestrator(t *testing.T) {
	t.Parallel()
	file, fset := parseAppGo(t)
	fd := findFuncDecl(file, "handleGateReviewDecision")
	if fd == nil {
		t.Fatal("handleGateReviewDecision not found in app.go")
	}
	body := funcBodyText(t, fset, fd, appGoPath(t))
	if !strings.Contains(body, "m.orchestrator.HandleReviewDecision(") {
		t.Errorf("handleGateReviewDecision body missing m.orchestrator.HandleReviewDecision call")
	}
}

func TestTUIBoundary_HandleRewindReviewDecision_DelegatesToOrchestrator(t *testing.T) {
	t.Parallel()
	file, fset := parseAppGo(t)
	fd := findFuncDecl(file, "handleRewindReviewDecision")
	if fd == nil {
		t.Fatal("handleRewindReviewDecision not found in app.go")
	}
	body := funcBodyText(t, fset, fd, appGoPath(t))
	// The rewind-confirm path delegates to ProceedFromRewindReview (NOT
	// HandleReviewDecision({Decision:"rewind"}) — that earlier wiring fired
	// a second RewindToPhase and produced a phantom run on every confirm).
	if !strings.Contains(body, "m.orchestrator.ProceedFromRewindReview(") {
		t.Errorf("handleRewindReviewDecision body missing m.orchestrator.ProceedFromRewindReview call")
	}
	if strings.Contains(body, "m.orchestrator.HandleReviewDecision(") {
		t.Errorf("handleRewindReviewDecision must NOT call HandleReviewDecision (re-runs the rewind on confirm)")
	}
}

func TestTUIBoundary_HandlePlanReviewDecision_DelegatesToOrchestrator(t *testing.T) {
	t.Parallel()
	file, fset := parseAppGo(t)
	fd := findFuncDecl(file, "handlePlanReviewDecision")
	if fd == nil {
		t.Fatal("handlePlanReviewDecision not found in app.go")
	}
	body := funcBodyText(t, fset, fd, appGoPath(t))
	if !strings.Contains(body, "m.orchestrator.HandleReviewDecision(") {
		t.Errorf("handlePlanReviewDecision body missing m.orchestrator.HandleReviewDecision call")
	}
}

func TestTUIBoundary_HandleRoadmapReviewDecision_DelegatesToOrchestrator(t *testing.T) {
	t.Parallel()
	file, fset := parseAppGo(t)
	fd := findFuncDecl(file, "handleRoadmapReviewDecision")
	if fd == nil {
		t.Fatal("handleRoadmapReviewDecision not found in app.go")
	}
	body := funcBodyText(t, fset, fd, appGoPath(t))
	if !strings.Contains(body, "m.orchestrator.HandleReviewDecision(") {
		t.Errorf("handleRoadmapReviewDecision body missing m.orchestrator.HandleReviewDecision call")
	}
}

func TestTUIBoundary_DeleteFeatureCmd_DelegatesToOrchestrator(t *testing.T) {
	t.Parallel()
	file, fset := parseAppGo(t)
	fd := findFuncDecl(file, "deleteFeatureCmd")
	if fd == nil {
		t.Fatal("deleteFeatureCmd not found in app.go")
	}
	body := funcBodyText(t, fset, fd, appGoPath(t))
	if !strings.Contains(body, "m.orchestrator.Delete(") {
		t.Errorf("deleteFeatureCmd body missing m.orchestrator.Delete call")
	}
}

func TestTUIBoundary_StopFeatureCmd_DelegatesToOrchestrator(t *testing.T) {
	t.Parallel()
	file, fset := parseAppGo(t)
	fd := findFuncDecl(file, "stopFeatureCmd")
	if fd == nil {
		t.Fatal("stopFeatureCmd not found in app.go")
	}
	body := funcBodyText(t, fset, fd, appGoPath(t))
	if !strings.Contains(body, "m.orchestrator.InterruptFeature(") {
		t.Errorf("stopFeatureCmd body missing m.orchestrator.InterruptFeature call")
	}
}

func TestTUIBoundary_CreateFeatureCmd_DelegatesToOrchestrator(t *testing.T) {
	t.Parallel()
	file, fset := parseAppGo(t)
	fd := findFuncDecl(file, "createFeatureCmd")
	if fd == nil {
		t.Fatal("createFeatureCmd not found in app.go")
	}
	body := funcBodyText(t, fset, fd, appGoPath(t))
	if !strings.Contains(body, "m.orchestrator.CreateFeature(") {
		t.Errorf("createFeatureCmd body missing m.orchestrator.CreateFeature call")
	}
}

// ---------------------------------------------------------------------------
// publishCmd: the Update dispatch for the Publish phase is a one-liner calling
// publishCmd; per-repo fan-out, cross-ref injection, and conflict routing all
// live inside the orchestrator.
// ---------------------------------------------------------------------------

func TestTUIBoundary_StartPhaseMsg_PublishBranchIsThin(t *testing.T) {
	t.Parallel()
	src := readFileOrFatal(t, appGoPath(t))
	if !strings.Contains(src, "return m, m.publishCmd(msg.FeatureID)") {
		t.Errorf("StartPhaseMsg publish branch must return m, m.publishCmd(msg.FeatureID) — fan-out belongs in the helper")
	}
}

func TestTUIBoundary_PublishCmd_RoutesThroughOrchestrator(t *testing.T) {
	t.Parallel()
	file, fset := parseAppGo(t)
	fd := findFuncDecl(file, "publishCmd")
	if fd == nil {
		t.Fatal("publishCmd not found in app.go — StartPhaseMsg publish branch relies on it")
	}
	body := funcBodyText(t, fset, fd, appGoPath(t))
	if !strings.Contains(body, "m.orchestrator.Publish(") {
		t.Errorf("publishCmd must call m.orchestrator.Publish to route the publish pipeline through the orchestrator")
	}
	if !strings.Contains(body, "m.orchestrator.TryCompletePublish(") {
		t.Errorf("publishCmd must call m.orchestrator.TryCompletePublish to finalise when every repo is already code-ready")
	}
}

func TestTUIBoundary_PublishCmd_GuardsInsideClosure(t *testing.T) {
	t.Parallel()
	file, fset := parseAppGo(t)
	fd := findFuncDecl(file, "publishCmd")
	if fd == nil {
		t.Fatal("publishCmd not found in app.go")
	}
	body := funcBodyText(t, fset, fd, appGoPath(t))
	if !strings.Contains(body, "IsPublishable()") {
		t.Errorf("publishCmd must guard via f.IsPublishable() inside the closure")
	}
	if !strings.Contains(body, "AutoPublish()") {
		t.Errorf("publishCmd must guard via Checkpoints.AutoPublish() inside the closure")
	}
}

// ---------------------------------------------------------------------------
// rewindCmd / deleteFeatureCmd route session-stop through the orchestrator so
// child-session walks, signal flushing, and observer emission live in one
// chokepoint rather than scattered TUI helpers.
// ---------------------------------------------------------------------------

func TestTUIBoundary_RewindCmd_UsesOrchestratorStopFeatureSessions(t *testing.T) {
	t.Parallel()
	file, fset := parseAppGo(t)
	fd := findFuncDecl(file, "rewindCmd")
	if fd == nil {
		t.Fatal("rewindCmd not found in app.go")
	}
	body := funcBodyText(t, fset, fd, appGoPath(t))
	if !strings.Contains(body, "m.orchestrator.StopFeatureSessions(") {
		t.Errorf("rewindCmd must call m.orchestrator.StopFeatureSessions so session walk stays in the orchestrator")
	}
}

func TestTUIBoundary_DeleteFeatureCmd_NoRedundantSessionWalk(t *testing.T) {
	t.Parallel()
	file, fset := parseAppGo(t)
	fd := findFuncDecl(file, "deleteFeatureCmd")
	if fd == nil {
		t.Fatal("deleteFeatureCmd not found in app.go")
	}
	body := funcBodyText(t, fset, fd, appGoPath(t))
	if strings.Contains(body, "m.sessionManager.ActiveSessions()") {
		t.Errorf("deleteFeatureCmd must not iterate m.sessionManager.ActiveSessions() directly — orchestrator.Delete already walks sessions")
	}
	if strings.Contains(body, "m.orchestrator.StopFeatureSessions(") {
		t.Errorf("deleteFeatureCmd must not call StopFeatureSessions separately — orchestrator.Delete handles the walk")
	}
}

func TestTUIBoundary_OrchestratorDelete_HasChildSessionWalk(t *testing.T) {
	t.Parallel()
	path := filepath.Join(repoRoot(t), "internal", "orchestrator", "orchestrator.go")
	src := readFileOrFatal(t, path)
	hasStopWalker := strings.Contains(src, "StopFeatureSessions") ||
		strings.Contains(src, "FeatureSessions(")
	hasLifecycleDelete := strings.Contains(src, "Lifecycle.Delete(") ||
		strings.Contains(src, "o.deps.Lifecycle.Delete(")
	if !hasStopWalker {
		t.Errorf("orchestrator.go Delete path missing child-session walk (expected StopFeatureSessions or FeatureSessions usage)")
	}
	if !hasLifecycleDelete {
		t.Errorf("orchestrator.go Delete path missing Lifecycle.Delete call")
	}
}

// ---------------------------------------------------------------------------
// Review handlers must not perform direct featureManager mutation. Every
// lifecycle transition belongs inside the orchestrator.
// ---------------------------------------------------------------------------

func TestTUIBoundary_ReviewHandlers_NoDirectFeatureManagerMutation(t *testing.T) {
	t.Parallel()
	file, fset := parseAppGo(t)
	handlers := []string{
		"handlePlanReviewDecision",
		"handleRoadmapReviewDecision",
		"handleGateReviewDecision",
		"handleRewindReviewDecision",
	}
	banned := []string{
		"m.featureManager.Transition(",
		"m.featureManager.MarkFailed(",
		"m.featureManager.MarkPublished(",
		"m.featureManager.MarkCodeReady(",
		"m.featureManager.SetStatus(",
		"m.featureManager.AdvanceRoadmapPhase(",
		"m.featureManager.ResetPlanStatusForRoadmap(",
		"m.featureManager.RecordRoadmapRejection(",
		"m.featureManager.SetTotalRoadmapPhases(",
		"m.featureManager.CommitRoadmapPhase(",
		"m.featureManager.PopulateExecutionPlanForPhase(",
		"m.featureManager.PopulateLegacyExecutionPlan(",
	}
	for _, name := range handlers {
		fd := findFuncDecl(file, name)
		if fd == nil {
			t.Errorf("%s not found in app.go", name)
			continue
		}
		body := funcBodyText(t, fset, fd, appGoPath(t))
		for _, b := range banned {
			if strings.Contains(body, b) {
				t.Errorf("%s: banned direct mutation %q — route through orchestrator", name, b)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// File-wide ban on direct featureManager mutations anywhere in app.go.
// Read-only Get/List/Config/Store.BaseDir lookups are still fine; state
// transitions are not.
// ---------------------------------------------------------------------------

func TestTUIBoundary_AppGo_NoBannedFeatureManagerMutations(t *testing.T) {
	t.Parallel()
	src := readFileOrFatal(t, appGoPath(t))
	banned := []string{
		"m.featureManager.Transition(",
		"m.featureManager.MarkFailed(",
		"m.featureManager.MarkPublished(",
		"m.featureManager.MarkDone(",
		"m.featureManager.CompleteImplementation(",
		"m.featureManager.StartTweak(",
		"m.featureManager.StartImplementation(",
		"m.featureManager.StartFinalReview(",
		"m.featureManager.ReturnToPublished(",
		"m.featureManager.StartRepoCycle(",
		"m.featureManager.CompleteRepoCycle(",
		"m.featureManager.FailRepoCycle(",
		"m.featureManager.RemoveRepoCycle(",
		"m.featureManager.MarkRepoCycleReviewing(",
		"m.featureManager.SetRepoCyclePlanPath(",
		"m.featureManager.StartRebase(",
		"m.featureManager.MarkCodeReady(",
		"m.featureManager.CompleteRefactor(",
		"m.featureManager.RecreateWorktree(",
		"m.featureManager.SetRepoPublished(",
		"m.featureManager.SetRepoPublishError(",
		"m.featureManager.StartRefactorAtPhase(",
		"m.featureManager.RetryRepo(",
		"m.featureManager.TryCompletePublish(",
		"m.featureManager.ClearRepoCycles(",
		"m.featureManager.NeedsPlanReview(",
		"m.featureManager.UpgradePipeline(",
		"m.featureManager.RewindToPhase(",
		"m.featureManager.CleanWorktree(",
		"m.featureManager.Delete(",
		"m.featureManager.Create(",
		"m.featureManager.Store.Save(",
	}
	for _, b := range banned {
		if strings.Contains(src, b) {
			t.Errorf("banned direct mutation %q present in app.go — route through orchestrator delegate instead", b)
		}
	}
}

// ---------------------------------------------------------------------------
// File-wide ban on repo-mutating git calls in app.go. Read-only helpers
// (DiffSummary, HasUncommittedChanges, CommitLog, DefaultBranch,
// PRBaseBranch, IsBehindRemote, IsBehindLocal, FetchPRComments) remain
// permitted for status rendering.
// ---------------------------------------------------------------------------

func TestTUIBoundary_AppGo_NoBannedGitMutators(t *testing.T) {
	t.Parallel()
	src := readFileOrFatal(t, appGoPath(t))
	banned := []string{
		"git.CommitAll(",
		"git.PullRebase(",
		"git.Push(",
		"git.ForcePush(",
		"git.Fetch(",
		"git.RebaseOnto(",
		"git.CreatePR(",
		"git.GetPRBody(",
		"git.UpdatePRBody(",
		"git.RetroactivelyUpdateCrossRefs(",
		"git.InjectCrossReferenceSection(",
		"git.BuildCrossReferenceSection(",
		"git.MergeFeatureBranch(",
	}
	for _, b := range banned {
		if strings.Contains(src, b) {
			t.Errorf("banned git mutator %q present in app.go — move the call into an orchestrator method", b)
		}
	}
}

// ---------------------------------------------------------------------------
// Uniform thin-delegate AST guard over every retained handler. Each body must
// avoid direct featureManager mutations, direct git mutators, direct observer
// emission, and direct phase-runner / agent invocations. Any of those belong
// inside the orchestrator.
// ---------------------------------------------------------------------------

func TestTUIBoundary_RetainedHandlers_AreThinDelegates(t *testing.T) {
	t.Parallel()
	file, fset := parseAppGo(t)
	handlers := []string{
		// Phase-completion handlers.
		"handlePhaseCompleted",
		"handlePlanLoopDone",
		"handleImplementLoopDone",
		"handleMultiRepoImplDone",
		"handleFinalReviewDone",
		// Review-decision handlers.
		"handlePlanReviewDecision",
		"handleRoadmapReviewDecision",
		"handleRewindReviewDecision",
		"handleGateReviewDecision",
		// Repo status + recovery.
		"handleRepoStatusUpdate",
		"updateRecovery",
		// First-phase start.
		"startFirstPhaseCmd",
		// Tweak delegates.
		"startInteractiveTweakCmd",
		"startInteractiveTweakCmd",
		"completeTweakCommitCmd",
		"completeTweakCommitCmd",
		"completeTweakFinishCmd",
		"completeTweakFinishCmd",
		"restoreTweakFromReviewCmd",
		"restoreTweakFromReviewCmd",
		"restoreTweakFeatureCmd",
		// Rebase delegates.
		"rebaseCmd",
		"rebaseCmd",
		"forcePushCmd",
		"resumeRebaseAfterConflictCmd",
		"startRebaseImplementationCmd",
		"completeRebaseCmd",
		// Refactor delegates.
		"startRefactorCmd",
		// Feature lifecycle adapters.
		"deleteFeatureCmd",
		"stopFeatureCmd",
		"mergeLocalCmd",
		"cleanWorktreeCmd",
		"retryPhaseCmd",
		"rewindCmd",
		// Restart / review-gate mutators.
		"restartPhaseCmd",
		"applyRefactorPipelineAndStart",
		"triggerReviewGateCmd",
	}

	bannedMutations := []string{
		"m.featureManager.Transition(",
		"m.featureManager.MarkFailed(",
		"m.featureManager.MarkPublished(",
		"m.featureManager.MarkDone(",
		"m.featureManager.CompleteImplementation(",
		"m.featureManager.StartTweak(",
		"m.featureManager.StartImplementation(",
		"m.featureManager.StartFinalReview(",
		"m.featureManager.ReturnToPublished(",
		"m.featureManager.StartRepoCycle(",
		"m.featureManager.CompleteRepoCycle(",
		"m.featureManager.FailRepoCycle(",
		"m.featureManager.RemoveRepoCycle(",
		"m.featureManager.MarkRepoCycleReviewing(",
		"m.featureManager.SetRepoCyclePlanPath(",
		"m.featureManager.StartRebase(",
		"m.featureManager.MarkCodeReady(",
		"m.featureManager.CompleteRefactor(",
		"m.featureManager.RecreateWorktree(",
		"m.featureManager.SetRepoPublished(",
		"m.featureManager.SetRepoPublishError(",
		"m.featureManager.StartRefactorAtPhase(",
		"m.featureManager.RetryRepo(",
		"m.featureManager.TryCompletePublish(",
		"m.featureManager.ClearRepoCycles(",
		"m.featureManager.NeedsPlanReview(",
		"m.featureManager.UpgradePipeline(",
		"m.featureManager.RewindToPhase(",
		"m.featureManager.CleanWorktree(",
		"m.featureManager.Delete(",
		"m.featureManager.Store.Save(",
		// Store.Modify lets a handler rewrite feature state without going
		// through a typed lifecycle method — route through a typed
		// orchestrator delegate instead.
		"m.featureManager.Store.Modify(",
	}
	bannedGit := []string{
		"git.CommitAll(",
		"git.PullRebase(",
		"git.Push(",
		"git.ForcePush(",
		"git.Fetch(",
		"git.RebaseOnto(",
		"git.CreatePR(",
		"git.GetPRBody(",
		"git.UpdatePRBody(",
		"git.RetroactivelyUpdateCrossRefs(",
		"git.InjectCrossReferenceSection(",
		"git.BuildCrossReferenceSection(",
		"git.MergeFeatureBranch(",
	}
	bannedObserver := []string{
		"m.observer.",
	}
	bannedRunner := []string{
		"m.phaseRunner.RunImplementation(",
		"m.phaseRunner.BuildSession(",
		"agent.BuildRebasePlan(",
		"agent.RunImplementationLoop(",
		"agent.RunRefactorLoop(",
	}

	for _, name := range handlers {
		fd := findFuncDecl(file, name)
		if fd == nil {
			// Missing handler is OK — a previous change may have renamed or
			// removed it. The test is about what remains.
			continue
		}
		body := funcBodyText(t, fset, fd, appGoPath(t))
		for _, b := range bannedMutations {
			if strings.Contains(body, b) {
				t.Errorf("%s: banned featureManager mutation %q — route through orchestrator", name, b)
			}
		}
		for _, b := range bannedGit {
			if strings.Contains(body, b) {
				t.Errorf("%s: banned git mutator %q — move into orchestrator", name, b)
			}
		}
		for _, b := range bannedObserver {
			if strings.Contains(body, b) {
				t.Errorf("%s: banned observer call %q — BuildHooks owns emission", name, b)
			}
		}
		for _, b := range bannedRunner {
			if strings.Contains(body, b) {
				t.Errorf("%s: banned phase-runner/agent call %q — orchestrator starters own phase dispatch", name, b)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// orchestratorAPI interface surface. The TUI binds to the orchestrator
// through a local interface so hand-written fakes (orchestrator_bridge_test.go)
// stay in sync with the concrete orchestrator.
// ---------------------------------------------------------------------------

func TestTUIBoundary_OrchestratorAPI_ExposesRequiredMethods(t *testing.T) {
	t.Parallel()
	src := readFileOrFatal(t, orchAPIPath(t))
	required := []string{
		// Lifecycle.
		"CreateFeature(",
		"InterruptFeature(",
		"Delete(",
		"StartFeature(",
		// Phase lifecycle.
		"HandlePhaseCompletion(",
		"HandleReviewDecision(",
		"Publish(",
		"StartMultiRepoImplementation(",
		// Recovery.
		"ScanRecovery(",
		"ExecuteRecovery(",
		// Failure escape hatch.
		"MarkFailed(",
		// Session walk (so rewindCmd can delegate).
		"StopFeatureSessions(",
		// Tweak, rebase, cycle, refactor — spot-check one of each.
		"StartTweak(",
		"StartRebase(",
		"DispatchRepoCycle(",
		"StartRefactorCycle(",
		// Publish delegation so publishCmd can finalise without mutating
		// featureManager directly.
		"TryCompletePublish(",
		// Restart + gate-review resolver.
		"RestartPhase(",
		"ResolveGateReviewContext(",
	}
	for _, sig := range required {
		if !strings.Contains(src, sig) {
			t.Errorf("orchestrator_api.go missing required interface method %q", sig)
		}
	}
}

func TestTUIBoundary_OrchestratorAPI_CompileTimeBinding(t *testing.T) {
	t.Parallel()
	src := readFileOrFatal(t, orchAPIPath(t))
	if !strings.Contains(src, "var _ orchestratorAPI = (*orchestrator.Orchestrator)(nil)") {
		t.Errorf("orchestrator_api.go missing compile-time binding `var _ orchestratorAPI = (*orchestrator.Orchestrator)(nil)`")
	}
}

func TestTUIBoundary_AppModel_OrchestratorIsInterface(t *testing.T) {
	t.Parallel()
	src := readFileOrFatal(t, appGoPath(t))
	re := regexp.MustCompile(`orchestrator\s+orchestratorAPI\b`)
	if !re.MatchString(src) {
		t.Errorf("AppModel.orchestrator must be the local orchestratorAPI interface; saw no `orchestrator orchestratorAPI` declaration")
	}
	if strings.Contains(src, "orchestrator *orchestrator.Orchestrator") {
		t.Errorf("AppModel.orchestrator must not reference *orchestrator.Orchestrator directly (breaks fakeOrch)")
	}
}

func TestTUIBoundary_FakeOrch_ImplementsRecentMethods(t *testing.T) {
	t.Parallel()
	path := filepath.Join(repoRoot(t), "internal", "tui", "orchestrator_bridge_test.go")
	src := readFileOrFatal(t, path)
	required := []string{
		"func (f *fakeOrch) StopFeatureSessions(",
		"func (f *fakeOrch) TryCompletePublish(",
		"func (f *fakeOrch) HandleReviewDecision(",
		"func (f *fakeOrch) HandlePhaseCompletion(",
		"func (f *fakeOrch) Delete(",
	}
	for _, sig := range required {
		if !strings.Contains(src, sig) {
			t.Errorf("orchestrator_bridge_test.go missing %q — fakeOrch lagging behind orchestratorAPI", sig)
		}
	}
}

// ---------------------------------------------------------------------------
// Retention: Tweak UI methods must remain on AppModel. Tweak session/commit
// flows carry UI-specific state (modals, review prompts) that cannot be
// cleanly moved to a pure orchestrator surface.
// ---------------------------------------------------------------------------

func TestTUIBoundary_TweakUIMethods_Retained(t *testing.T) {
	t.Parallel()
	file, _ := parseAppGo(t)
	required := []string{
		"handleTweakSessionDone",
		"handleTweakCommitDone",
		"renderTweakReviewModal",
	}
	for _, name := range required {
		if findFuncDecl(file, name) == nil {
			t.Errorf("required tweak UI method %q missing from app.go", name)
		}
	}
}

// ---------------------------------------------------------------------------
// MarkFailed is a narrow escape hatch; bulk usage from the TUI indicates a
// missing typed orchestrator method. Cap at ≤ 2 occurrences in app.go.
// ---------------------------------------------------------------------------

func TestTUIBoundary_OrchestratorMarkFailed_CallCountBounded(t *testing.T) {
	t.Parallel()
	src := readFileOrFatal(t, appGoPath(t))
	n := strings.Count(src, "m.orchestrator.MarkFailed(")
	const cap = 2
	if n > cap {
		t.Errorf("m.orchestrator.MarkFailed(...) occurs %d times in app.go, cap is %d. New call sites must either (a) delegate to a typed orchestrator method that handles its own failures internally, or (b) bump this cap with a justification.", n, cap)
	}
}

// ---------------------------------------------------------------------------
// Observer direct usage is banned entirely. Observer emission fires
// exclusively through orchestrator.BuildHooks (OnFeatureCompleted +
// OnFeatureSummaryNeeded + OnFeatureFailed + OnRecoveryScanned +
// OnRecoveryAction). A direct m.observer.* call from app.go would re-
// introduce the scattered side-channel BuildHooks replaces.
// ---------------------------------------------------------------------------

func TestTUIBoundary_ObserverDirectUsage_Banned(t *testing.T) {
	t.Parallel()
	src := readFileOrFatal(t, appGoPath(t))
	n := strings.Count(src, "m.observer.")
	if n != 0 {
		t.Errorf("m.observer.* occurs %d times in app.go, expected 0 — observer emission must go through orchestrator.BuildHooks", n)
	}
}

// ---------------------------------------------------------------------------
// Recovery: submission uses the items RecoveryModel captured at view entry,
// not a fresh scan (rescanning on submit can apply actions to a different
// orphan set). updateRecovery must dispatch exactly once via
// orchestrator.ExecuteRecovery — a second StartPhaseMsg fan-out would start a
// concurrent phase run for every resumed feature.
// ---------------------------------------------------------------------------

func TestTUIBoundary_UpdateRecovery_NoStartPhaseFanout(t *testing.T) {
	t.Parallel()
	file, fset := parseAppGo(t)
	fd := findFuncDecl(file, "updateRecovery")
	if fd == nil {
		t.Fatal("updateRecovery not found in app.go")
	}
	body := funcBodyText(t, fset, fd, appGoPath(t))
	if !strings.Contains(body, "m.orchestrator.ExecuteRecovery(") {
		t.Errorf("updateRecovery must call m.orchestrator.ExecuteRecovery — the sole entry point for recovery dispatch")
	}
	if strings.Contains(body, "StartPhaseMsg{") {
		t.Errorf("updateRecovery must NOT construct StartPhaseMsg — orchestrator.ExecuteRecovery already relaunches resumed features via startPhase; a TUI-side fan-out starts a second concurrent phase run")
	}
	if strings.Contains(body, "programRef.P.Send(") {
		t.Errorf("updateRecovery must NOT call programRef.P.Send — the orchestrator's ExecuteRecovery owns resume dispatch")
	}
}

func TestTUIBoundary_UpdateRecovery_UsesStoredItems(t *testing.T) {
	t.Parallel()
	file, fset := parseAppGo(t)
	fd := findFuncDecl(file, "updateRecovery")
	if fd == nil {
		t.Fatal("updateRecovery not found in app.go")
	}
	body := funcBodyText(t, fset, fd, appGoPath(t))
	if !strings.Contains(body, "m.recovery.Items()") {
		t.Errorf("updateRecovery must consume the items snapshot via m.recovery.Items() — rescanning on submit can apply actions to a different orphan set")
	}
	if strings.Contains(body, "m.orchestrator.ScanRecovery(") {
		t.Errorf("updateRecovery must NOT call m.orchestrator.ScanRecovery on submit — the scan already ran at view entry (NewAppModel) and RecoveryModel captured the slice")
	}
}

func TestTUIBoundary_AppGo_NoSessionRecoveryDirectCalls(t *testing.T) {
	t.Parallel()
	src := readFileOrFatal(t, appGoPath(t))
	if strings.Contains(src, "session.ScanForRecovery(") {
		t.Errorf("app.go must not call session.ScanForRecovery — route recovery through orchestrator.ScanRecovery")
	}
	if strings.Contains(src, "session.ExecuteRecovery(") {
		t.Errorf("app.go must not call session.ExecuteRecovery — route recovery through orchestrator.ExecuteRecovery")
	}
}

// ---------------------------------------------------------------------------
// OrchPublishCompletedMsg has its own Update case so PublishConflictError
// routes into the rebase-resolution UX. Lumping it into the generic
// orchestrator-event catch-all would swallow the conflict path silently.
// ---------------------------------------------------------------------------

func TestTUIBoundary_OrchPublishCompletedMsg_HasDedicatedCase(t *testing.T) {
	t.Parallel()
	src := readFileOrFatal(t, appGoPath(t))
	if !strings.Contains(src, "case OrchPublishCompletedMsg:") {
		t.Errorf("Update() must have a dedicated `case OrchPublishCompletedMsg:` branch — the catch-all swallows PublishConflictError routing")
	}
	if !strings.Contains(src, "orchestrator.PublishConflictError") {
		t.Errorf("Update() must reference orchestrator.PublishConflictError to route pull-rebase conflicts to the rebase-resolution UX")
	}
}

// ---------------------------------------------------------------------------
// restartPhaseCmd / applyRefactorPipelineAndStart / startFirstPhaseCmd /
// triggerReviewGateCmd: Update-dispatched helpers must delegate to typed
// orchestrator methods rather than running Store.Modify or session walks
// in-line.
// ---------------------------------------------------------------------------

func TestTUIBoundary_RestartPhaseCmd_NoStoreModifyOrSessionWalk(t *testing.T) {
	t.Parallel()
	file, fset := parseAppGo(t)
	fd := findFuncDecl(file, "restartPhaseCmd")
	if fd == nil {
		t.Fatal("restartPhaseCmd not found in app.go")
	}
	body := funcBodyText(t, fset, fd, appGoPath(t))
	if strings.Contains(body, "m.featureManager.Store.Modify(") {
		t.Errorf("restartPhaseCmd must not call Store.Modify — route through orchestrator.RestartPhase")
	}
	if strings.Contains(body, "m.sessionManager.StopSession(") {
		t.Errorf("restartPhaseCmd must not call sessionManager.StopSession directly — use orchestrator.RestartPhase")
	}
	if strings.Contains(body, "m.sessionManager.ActiveSessions()") {
		t.Errorf("restartPhaseCmd must not iterate sessionManager.ActiveSessions() — use orchestrator.RestartPhase")
	}
	if strings.Contains(body, "m.orchestrator.StopFeatureSessions(") {
		t.Errorf("restartPhaseCmd must not call StopFeatureSessions directly — orchestrator.RestartPhase handles session-stop internally")
	}
	if strings.Contains(body, "m.orchestrator.ResetToPublishedFromTweak(") {
		t.Errorf("restartPhaseCmd must not call ResetToPublishedFromTweak directly — orchestrator.RestartPhase dispatches tweak reset")
	}
	if strings.Contains(body, "m.orchestrator.ExtendFailedPhaseBudget(") {
		t.Errorf("restartPhaseCmd must not call ExtendFailedPhaseBudget directly — orchestrator.RestartPhase handles budget extension")
	}
	if strings.Contains(body, "m.orchestrator.CollectAndClearRepoCycleRestarts(") {
		t.Errorf("restartPhaseCmd must not call CollectAndClearRepoCycleRestarts directly — orchestrator.RestartPhase handles repo-cycle fan-out")
	}
	if strings.Contains(body, "m.orchestrator.TransitionTo(") {
		t.Errorf("restartPhaseCmd must not call TransitionTo directly — orchestrator.RestartPhase owns the phase/status transitions")
	}
	if strings.Contains(body, "m.orchestrator.SetBrainstormReady(") {
		t.Errorf("restartPhaseCmd must not call SetBrainstormReady directly — orchestrator.RestartPhase handles brainstorm restart")
	}
	if !strings.Contains(body, "m.orchestrator.RestartPhase(") {
		t.Errorf("restartPhaseCmd must delegate to orchestrator.RestartPhase — that is the single restart entrypoint")
	}
}

func TestTUIBoundary_ApplyRefactorPipelineAndStart_UsesOrchestratorDelegate(t *testing.T) {
	t.Parallel()
	file, fset := parseAppGo(t)
	fd := findFuncDecl(file, "applyRefactorPipelineAndStart")
	if fd == nil {
		t.Fatal("applyRefactorPipelineAndStart not found in app.go")
	}
	body := funcBodyText(t, fset, fd, appGoPath(t))
	if strings.Contains(body, "m.featureManager.Store.Modify(") {
		t.Errorf("applyRefactorPipelineAndStart must not call Store.Modify — route through orchestrator.ApplyRefactorPipeline")
	}
	if !strings.Contains(body, "m.orchestrator.ApplyRefactorPipeline(") {
		t.Errorf("applyRefactorPipelineAndStart must delegate to orchestrator.ApplyRefactorPipeline")
	}
}

func TestTUIBoundary_StartFirstPhaseCmd_DelegatesToStartFeature(t *testing.T) {
	t.Parallel()
	file, fset := parseAppGo(t)
	fd := findFuncDecl(file, "startFirstPhaseCmd")
	if fd == nil {
		t.Fatal("startFirstPhaseCmd not found in app.go")
	}
	body := funcBodyText(t, fset, fd, appGoPath(t))
	if strings.Contains(body, "m.featureManager.Store.Modify(") {
		t.Errorf("startFirstPhaseCmd must not call Store.Modify — orchestrator.StartFeature owns the Medium pre-transition")
	}
	if !strings.Contains(body, "m.orchestrator.StartFeature(") {
		t.Errorf("startFirstPhaseCmd must delegate to orchestrator.StartFeature")
	}
}

func TestTUIBoundary_TriggerReviewGateCmd_UsesOrchestratorDelegate(t *testing.T) {
	t.Parallel()
	file, fset := parseAppGo(t)
	fd := findFuncDecl(file, "triggerReviewGateCmd")
	if fd == nil {
		return
	}
	body := funcBodyText(t, fset, fd, appGoPath(t))
	if strings.Contains(body, "m.featureManager.Store.Modify(") {
		t.Errorf("triggerReviewGateCmd must not call Store.Modify — route through orchestrator.EnterReviewGate")
	}
	if !strings.Contains(body, "m.orchestrator.EnterReviewGate(") {
		t.Errorf("triggerReviewGateCmd must delegate to orchestrator.EnterReviewGate")
	}
}

// ---------------------------------------------------------------------------
// Gate-review start command must not reimplement the target-phase → artifact-key
// mapping; orchestrator.ResolveGateReviewContext owns that logic.
// ---------------------------------------------------------------------------

func TestTUIBoundary_GateReviewStartCmd_DelegatesToOrchestrator(t *testing.T) {
	t.Parallel()
	file, fset := parseAppGo(t)
	for _, name := range []string{"gateReviewStartCmd", "gateReviewStartMsg"} {
		fd := findFuncDecl(file, name)
		if fd == nil {
			continue
		}
		body := funcBodyText(t, fset, fd, appGoPath(t))
		bannedArtifactKeys := []string{
			`resolvePhaseArtifactPath(f, "inquire")`,
			`resolvePhaseArtifactPath(f, "research")`,
			`resolvePhaseArtifactPath(f, "brainstorm")`,
			`resolvePhaseArtifactPath(f, "roadmap")`,
			`resolvePhaseArtifactPath(f, "plan")`,
		}
		for _, b := range bannedArtifactKeys {
			if strings.Contains(body, b) {
				t.Errorf("%s must not reimplement %q — orchestrator.ResolveGateReviewContext owns artifact resolution", name, b)
			}
		}
	}
}

func TestTUIBoundary_RewindReviewStartCmd_DelegatesToOrchestrator(t *testing.T) {
	t.Parallel()

	file, fset := parseAppGo(t)
	fd := findFuncDecl(file, "startRewindReviewSessionCmd")
	if fd == nil {
		t.Fatal("startRewindReviewSessionCmd not found in app.go")
	}
	body := funcBodyText(t, fset, fd, appGoPath(t))
	if !strings.Contains(body, "m.orchestrator.ResolveRewindReviewContext(") {
		t.Errorf("startRewindReviewSessionCmd must delegate artifact resolution to orchestrator.ResolveRewindReviewContext")
	}
	for _, b := range []string{
		`resolvePhaseArtifactPath(f, "inquire")`,
		`resolvePhaseArtifactPath(f, "research")`,
		`resolvePhaseArtifactPath(f, "brainstorm")`,
		`resolvePhaseArtifactPath(f, "plan")`,
	} {
		if strings.Contains(body, b) {
			t.Errorf("startRewindReviewSessionCmd must not reimplement %q — orchestrator.ResolveRewindReviewContext owns artifact resolution", b)
		}
	}
}
