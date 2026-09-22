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

package orchestrator_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// approvalRoadmapText is a two-row roadmap: layer 1 "Bootstrap" and layer 2
// "Build and polish".
func approvalRoadmapText(t *testing.T, layerOneTitle string) string {
	t.Helper()
	return "# Roadmap\n\n## Phase 1: Bootstrap\n### Goal\nInit\n\n## Phase 2: Build\n### Goal\nBuild\n\n## Phase 3: Polish\n### Goal\nPolish\n\n" +
		"## Pull Requests\n\n| # | Title | Phases | Rationale |\n|---|---|---|---|\n" +
		"| 1 | " + layerOneTitle + " | 1 | Stands alone. |\n| 2 | Build and polish | 2-3 | Two halves of one concern. |\n"
}

// approvalWorktrees is a fake worktree adapter that tracks the branch each
// worktree path is on and can fail renames for selected paths.
type approvalWorktrees struct {
	branches    map[string]string
	renameCalls []string
	failRename  map[string]error
}

func newApprovalWorktrees(branches map[string]string) *approvalWorktrees {
	return &approvalWorktrees{branches: branches, failRename: map[string]error{}}
}

func (w *approvalWorktrees) mock() *mocks.MockWorktreeOps {
	m := mocks.NewMockWorktreeOps()
	m.CurrentBranchFn = func(path string) string { return w.branches[path] }
	m.RenameBranchFn = func(path, oldName, newName string) error {
		if err := w.failRename[path]; err != nil {
			return err
		}
		w.renameCalls = append(w.renameCalls, path)
		w.branches[path] = newName
		return nil
	}
	return m
}

func (w *approvalWorktrees) renamesFor(path string) int {
	count := 0
	for _, called := range w.renameCalls {
		if called == path {
			count++
		}
	}
	return count
}

// approvalFeature builds a feature with two worktree-backed repositories on
// the provisional layer-1 branch and durable setup tasks recording it.
func approvalFeature(t *testing.T, id, slug, layerOneTitle string) (*feature.Feature, string) {
	t.Helper()
	workspaceSlug := feature.WorkspaceSlug(slug, id)
	provisional := git.LayerBranchName(workspaceSlug, 1, slug)
	f := &feature.Feature{
		ID:     id,
		Name:   "Approval Feature",
		Slug:   slug,
		Status: feature.StatusPlanNeedsReview,
		// Set by the caller when a gate is in play; see seedPlanGate.
		Pipeline:            feature.PipelineLarge,
		TotalRoadmapPhases:  3,
		CurrentRoadmapPhase: 0,
		Repos: []feature.FeatureRepo{
			{Name: "repo-a", Path: "/src/repo-a", WorktreePath: "/wt/repo-a", Branch: provisional, BaseBranch: "main"},
			{Name: "repo-b", Path: "/src/repo-b", WorktreePath: "/wt/repo-b", Branch: provisional, BaseBranch: "main"},
		},
	}
	f.Run().Setup = &feature.SetupState{
		Status: feature.SetupStatusDone,
		Tasks: map[string]feature.SetupTask{
			"worktree:repo-a": {Key: "worktree:repo-a", Kind: feature.SetupTaskWorktree, Repo: "repo-a", Status: feature.SetupStatusDone, Branch: provisional},
			"worktree:repo-b": {Key: "worktree:repo-b", Kind: feature.SetupTaskWorktree, Repo: "repo-b", Status: feature.SetupStatusDone, Branch: provisional},
		},
	}
	roadmapPath := filepath.Join(t.TempDir(), "roadmap.md")
	if err := os.WriteFile(roadmapPath, []byte(approvalRoadmapText(t, layerOneTitle)), 0o644); err != nil {
		t.Fatalf("write roadmap: %v", err)
	}
	f.Artifacts = map[string]string{"roadmap": roadmapPath}
	return f, provisional
}

func seedPlanGate(f *feature.Feature) {
	planGate := feature.PhasePlan
	f.PendingReviewPhase = &planGate
}

func approvalLifecycle(f *feature.Feature, t *testing.T) (*mocks.MockFeatureLifecycle, *int, *[]feature.StackLayer) {
	t.Helper()
	lc := lifecycleForFeature(f)
	totalAtAdvance := 0
	var stackAtAdvance []feature.StackLayer
	lc.AdvanceRoadmapPhaseFn = func(id string) error {
		totalAtAdvance = f.TotalRoadmapPhases
		stackAtAdvance = append([]feature.StackLayer(nil), f.Stack...)
		f.CurrentRoadmapPhase = 1
		f.Status = feature.StatusPlanning
		return nil
	}
	lc.StartPlanningFn = func(id string) error { f.Status = feature.StatusPlanning; return nil }
	return lc, &totalAtAdvance, &stackAtAdvance
}

// Human proceed on a two-row roadmap renames every repository worktree from
// the provisional branch to the approved layer-1 name, persists that name on
// each repository record and worktree setup task, persists every layer's
// branch name on the stack, and does so before the roadmap phase advances.
func TestOrchestrator_HandleReviewDecision_Proceed_Roadmap_RenamesBranchesToLayerOne(t *testing.T) {
	f, provisional := approvalFeature(t, "feat-rm-rename", "pr-stacks-demo", "Bootstrap")
	seedPlanGate(f)
	lc, totalAtAdvance, stackAtAdvance := approvalLifecycle(f, t)
	worktrees := newApprovalWorktrees(map[string]string{"/wt/repo-a": provisional, "/wt/repo-b": provisional})
	fs := newFeatureStore(f)
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs, Worktrees: worktrees.mock()}, orchestrator.Hooks{})

	if err := o.HandleReviewDecision(f.ID, orchestrator.ReviewDecision{Decision: "proceed", Roadmap: true}); err != nil {
		t.Fatalf("HandleReviewDecision: %v", err)
	}

	workspaceSlug := feature.WorkspaceSlug(f.Slug, f.ID)
	approvedOne := git.LayerBranchName(workspaceSlug, 1, "bootstrap")
	approvedTwo := git.LayerBranchName(workspaceSlug, 2, "build-and-polish")
	if approvedOne == provisional {
		t.Fatalf("test setup: approved layer 1 %q must differ from the provisional %q", approvedOne, provisional)
	}
	for _, repo := range f.Repos {
		if repo.Branch != approvedOne {
			t.Errorf("repo %s branch = %q, want %q", repo.Name, repo.Branch, approvedOne)
		}
		if worktrees.branches[repo.WorktreePath] != approvedOne {
			t.Errorf("repo %s worktree still on %q, want %q", repo.Name, worktrees.branches[repo.WorktreePath], approvedOne)
		}
		if task := f.Run().Setup.Tasks["worktree:"+repo.Name]; task.Branch != approvedOne {
			t.Errorf("repo %s setup task branch = %q, want %q", repo.Name, task.Branch, approvedOne)
		}
		if got := worktrees.renamesFor(repo.WorktreePath); got != 1 {
			t.Errorf("repo %s rename calls = %d, want exactly one", repo.Name, got)
		}
	}
	if len(f.Stack) != 2 || f.Stack[0].Branch != approvedOne || f.Stack[1].Branch != approvedTwo {
		t.Fatalf("stack = %+v, want layer branch names %q and %q", f.Stack, approvedOne, approvedTwo)
	}
	if *totalAtAdvance != 3 || len(*stackAtAdvance) != 2 || (*stackAtAdvance)[0].Branch != approvedOne {
		t.Errorf("state at advance = %d/%+v, want the approval persisted before advancing", *totalAtAdvance, *stackAtAdvance)
	}
	assertLifecycleCall(t, lc, "AdvanceRoadmapPhase")
}

// A layer-1 slug equal to the feature slug means the approved name equals
// the provisional branch: no rename runs, and the stack still persists.
func TestOrchestrator_HandleReviewDecision_Proceed_Roadmap_LayerOneSlugEqualsFeatureSlugSkipsRename(t *testing.T) {
	f, provisional := approvalFeature(t, "feat-rm-noop", "pr-stacks-demo", "Pr stacks demo")
	seedPlanGate(f)
	lc, _, _ := approvalLifecycle(f, t)
	worktrees := newApprovalWorktrees(map[string]string{"/wt/repo-a": provisional, "/wt/repo-b": provisional})
	fs := newFeatureStore(f)
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs, Worktrees: worktrees.mock()}, orchestrator.Hooks{})

	if err := o.HandleReviewDecision(f.ID, orchestrator.ReviewDecision{Decision: "proceed", Roadmap: true}); err != nil {
		t.Fatalf("HandleReviewDecision: %v", err)
	}

	workspaceSlug := feature.WorkspaceSlug(f.Slug, f.ID)
	approvedOne := git.LayerBranchName(workspaceSlug, 1, "pr-stacks-demo")
	if approvedOne != provisional {
		t.Fatalf("test setup: approved layer 1 %q must equal the provisional %q", approvedOne, provisional)
	}
	if len(worktrees.renameCalls) != 0 {
		t.Fatalf("rename calls = %v, want none when the names are identical", worktrees.renameCalls)
	}
	if len(f.Stack) != 2 || f.Stack[0].Branch != approvedOne || f.Stack[1].Branch == "" {
		t.Fatalf("stack = %+v, want branch names persisted without a rename", f.Stack)
	}
	for _, repo := range f.Repos {
		if repo.Branch != approvedOne {
			t.Errorf("repo %s branch = %q, want %q", repo.Name, repo.Branch, approvedOne)
		}
	}
}

// Proceed with one repository on an unexpected branch: the decision errors
// naming that repository, every record stays unchanged, the roadmap phase
// never advances, and retries succeed — both after the worktree is put back
// and after a partial rename left some repositories on the target.
func TestOrchestrator_HandleReviewDecision_Proceed_Roadmap_RenameFailureKeepsGateAndRetriesHeal(t *testing.T) {
	f, provisional := approvalFeature(t, "feat-rm-fail", "pr-stacks-demo", "Bootstrap")
	seedPlanGate(f)
	lc, _, _ := approvalLifecycle(f, t)
	advanced := 0
	lc.AdvanceRoadmapPhaseFn = func(id string) error {
		advanced++
		f.CurrentRoadmapPhase = 1
		f.Status = feature.StatusPlanning
		return nil
	}
	worktrees := newApprovalWorktrees(map[string]string{"/wt/repo-a": provisional, "/wt/repo-b": "feature/someone-else"})
	fs := newFeatureStore(f)
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs, Worktrees: worktrees.mock()}, orchestrator.Hooks{})

	err := o.HandleReviewDecision(f.ID, orchestrator.ReviewDecision{Decision: "proceed", Roadmap: true})
	if err == nil {
		t.Fatal("HandleReviewDecision must fail when a repository is on an unexpected branch")
	}
	for _, want := range []string{"repo-b", "feature/someone-else", provisional} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to name %q", err, want)
		}
	}
	if advanced != 0 {
		t.Errorf("AdvanceRoadmapPhase calls = %d, want 0", advanced)
	}
	if f.PendingReviewPhase == nil {
		t.Error("PendingReviewPhase must stay set so the gate remains open")
	}
	for _, repo := range f.Repos {
		if repo.Branch != provisional {
			t.Errorf("repo %s branch = %q, want unchanged %q", repo.Name, repo.Branch, provisional)
		}
		if task := f.Run().Setup.Tasks["worktree:"+repo.Name]; task.Branch != provisional {
			t.Errorf("repo %s setup task branch = %q, want unchanged %q", repo.Name, task.Branch, provisional)
		}
	}
	if len(f.Stack) != 0 {
		t.Errorf("stack = %+v, want nothing persisted", f.Stack)
	}
	if f.TotalRoadmapPhases != 3 {
		t.Errorf("TotalRoadmapPhases = %d, want the pre-existing 3", f.TotalRoadmapPhases)
	}

	// Retry after the worktree is put back on the recorded branch succeeds.
	worktrees.branches["/wt/repo-b"] = provisional
	if err := o.HandleReviewDecision(f.ID, orchestrator.ReviewDecision{Decision: "proceed", Roadmap: true}); err != nil {
		t.Fatalf("retry after restoring the worktree: %v", err)
	}
	if advanced != 1 {
		t.Errorf("AdvanceRoadmapPhase calls after retry = %d, want 1", advanced)
	}
	approvedOne := git.LayerBranchName(feature.WorkspaceSlug(f.Slug, f.ID), 1, "bootstrap")
	for _, repo := range f.Repos {
		if repo.Branch != approvedOne {
			t.Errorf("repo %s branch after retry = %q, want %q", repo.Name, repo.Branch, approvedOne)
		}
	}

	// A partial rename (one repository renamed, one failing) also heals: the
	// already-renamed repository is skipped, not renamed twice.
	f2, provisional2 := approvalFeature(t, "feat-rm-partial", "pr-stacks-demo", "Bootstrap")
	seedPlanGate(f2)
	lc2, _, _ := approvalLifecycle(f2, t)
	advanced2 := 0
	lc2.AdvanceRoadmapPhaseFn = func(id string) error {
		advanced2++
		f2.CurrentRoadmapPhase = 1
		f2.Status = feature.StatusPlanning
		return nil
	}
	partial := newApprovalWorktrees(map[string]string{"/wt/repo-a": provisional2, "/wt/repo-b": provisional2})
	partial.failRename["/wt/repo-b"] = errors.New("target ref already exists")
	fs2 := newFeatureStore(f2)
	o2 := orchestrator.New(orchestrator.Deps{Lifecycle: lc2, Store: fs2, Worktrees: partial.mock()}, orchestrator.Hooks{})
	if err := o2.HandleReviewDecision(f2.ID, orchestrator.ReviewDecision{Decision: "proceed", Roadmap: true}); err == nil {
		t.Fatal("HandleReviewDecision must fail when a rename fails")
	}
	if advanced2 != 0 {
		t.Fatalf("AdvanceRoadmapPhase calls = %d, want 0 after a failed rename", advanced2)
	}
	if partial.branches["/wt/repo-a"] != git.LayerBranchName(feature.WorkspaceSlug(f2.Slug, f2.ID), 1, "bootstrap") {
		t.Fatalf("partial rename did not reach repo-a: %+v", partial.branches)
	}
	// The first attempt left nothing persisted; the worktree state is ahead
	// of the record, which is exactly the shape the retry must heal.
	delete(partial.failRename, "/wt/repo-b")
	if err := o2.HandleReviewDecision(f2.ID, orchestrator.ReviewDecision{Decision: "proceed", Roadmap: true}); err != nil {
		t.Fatalf("retry after partial rename: %v", err)
	}
	if got := partial.renamesFor("/wt/repo-a"); got != 1 {
		t.Errorf("repo-a rename calls across attempts = %d, want 1 (skip when already on target)", got)
	}
	if got := partial.renamesFor("/wt/repo-b"); got != 1 {
		t.Errorf("repo-b rename calls across attempts = %d, want 1", got)
	}
	for _, repo := range f2.Repos {
		if repo.Branch != git.LayerBranchName(feature.WorkspaceSlug(f2.Slug, f2.ID), 1, "bootstrap") {
			t.Errorf("repo %s branch after partial-rename retry = %q, want the approved name", repo.Name, repo.Branch)
		}
	}
	if advanced2 != 1 {
		t.Errorf("AdvanceRoadmapPhase calls after retry = %d, want 1", advanced2)
	}
}

// No-gate auto-approval renames and persists like a human proceed.
func TestOrchestrator_HandlePhaseCompletion_Plan_RoadmapApproved_NoGate_RenamesBranchesToLayerOne(t *testing.T) {
	f, provisional := approvalFeature(t, "feat-ra-rename", "pr-stacks-demo", "Bootstrap")
	f.Status = feature.StatusPlanning
	f.CurrentPhase = feature.PhasePlan
	f.PendingReviewPhase = nil
	lc, _, _ := approvalLifecycle(f, t)
	worktrees := newApprovalWorktrees(map[string]string{"/wt/repo-a": provisional, "/wt/repo-b": provisional})
	fs := newFeatureStore(f)
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs, Worktrees: worktrees.mock()}, orchestrator.Hooks{})

	if err := o.HandlePhaseCompletion(f.ID, orchestrator.PhaseCompletionInput{
		Phase:      feature.PhasePlan,
		PlanResult: &agent.PlanLoopResult{FinalStatus: "approved"},
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	approvedOne := git.LayerBranchName(feature.WorkspaceSlug(f.Slug, f.ID), 1, "bootstrap")
	for _, repo := range f.Repos {
		if repo.Branch != approvedOne {
			t.Errorf("repo %s branch = %q, want %q", repo.Name, repo.Branch, approvedOne)
		}
		if worktrees.branches[repo.WorktreePath] != approvedOne {
			t.Errorf("repo %s worktree still on %q", repo.Name, worktrees.branches[repo.WorktreePath])
		}
		if task := f.Run().Setup.Tasks["worktree:"+repo.Name]; task.Branch != approvedOne {
			t.Errorf("repo %s setup task branch = %q, want %q", repo.Name, task.Branch, approvedOne)
		}
	}
	if len(f.Stack) != 2 || f.Stack[0].Branch != approvedOne {
		t.Fatalf("stack = %+v, want approved branch names", f.Stack)
	}
	if f.CurrentRoadmapPhase != 1 {
		t.Errorf("CurrentRoadmapPhase = %d, want 1 (advanced)", f.CurrentRoadmapPhase)
	}
}

// No-gate auto-approval with one failing repository: the warning event fires
// for that repository, its record stays on the branch still checked out, the
// other repositories record the approved name, and the run advances.
func TestOrchestrator_HandlePhaseCompletion_Plan_RoadmapApproved_NoGate_RenameFailureWarnsAndAdvances(t *testing.T) {
	f, provisional := approvalFeature(t, "feat-ra-warn", "pr-stacks-demo", "Bootstrap")
	f.Status = feature.StatusPlanning
	f.CurrentPhase = feature.PhasePlan
	f.PendingReviewPhase = nil
	lc, _, _ := approvalLifecycle(f, t)
	worktrees := newApprovalWorktrees(map[string]string{"/wt/repo-a": provisional, "/wt/repo-b": provisional})
	worktrees.failRename["/wt/repo-b"] = errors.New("target ref already exists")
	fs := newFeatureStore(f)
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs, Worktrees: worktrees.mock()}, orchestrator.Hooks{})

	if err := o.HandlePhaseCompletion(f.ID, orchestrator.PhaseCompletionInput{
		Phase:      feature.PhasePlan,
		PlanResult: &agent.PlanLoopResult{FinalStatus: "approved"},
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	approvedOne := git.LayerBranchName(feature.WorkspaceSlug(f.Slug, f.ID), 1, "bootstrap")
	if f.Repos[0].Branch != approvedOne || worktrees.branches["/wt/repo-a"] != approvedOne {
		t.Errorf("repo-a branch = %q (worktree %q), want the approved %q", f.Repos[0].Branch, worktrees.branches["/wt/repo-a"], approvedOne)
	}
	if f.Repos[1].Branch != provisional {
		t.Errorf("repo-b branch = %q, want the branch still checked out %q", f.Repos[1].Branch, provisional)
	}
	if task := f.Run().Setup.Tasks["worktree:repo-b"]; task.Branch != provisional {
		t.Errorf("repo-b setup task branch = %q, want %q", task.Branch, provisional)
	}
	if f.CurrentRoadmapPhase != 1 {
		t.Errorf("CurrentRoadmapPhase = %d, want 1 (the run advances past a rename warning)", f.CurrentRoadmapPhase)
	}

	sawWarning := false
	for _, ev := range drainEvents(o) {
		if ev.Type != ports.RepoStatusChanged || ev.RepoName != "repo-b" {
			continue
		}
		if ev.CanonicalError == nil || ev.CanonicalError.Code != errcat.RoadmapBranchRenameFailed {
			t.Fatalf("warning event canonical error = %+v, want roadmap_branch_rename_failed", ev.CanonicalError)
		}
		sawWarning = true
	}
	if !sawWarning {
		t.Error("expected a RepoStatusChanged warning event for repo-b")
	}
}

// Auto-approval that routes to the human review gate performs no rename; the
// gate's proceed does.
func TestOrchestrator_HandlePhaseCompletion_Plan_RoadmapApproved_GateRouteDoesNotRename(t *testing.T) {
	f, provisional := approvalFeature(t, "feat-ra-gate", "pr-stacks-demo", "Bootstrap")
	f.Status = feature.StatusPlanning
	f.CurrentPhase = feature.PhasePlan
	f.PendingReviewPhase = nil
	f.Checkpoints.RoadmapReview = true
	lc, _, _ := approvalLifecycle(f, t)
	lc.NeedsPlanReviewFn = func(id string) error { f.Status = feature.StatusPlanNeedsReview; return nil }
	worktrees := newApprovalWorktrees(map[string]string{"/wt/repo-a": provisional, "/wt/repo-b": provisional})
	fs := newFeatureStore(f)
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs, Worktrees: worktrees.mock()}, orchestrator.Hooks{})

	if err := o.HandlePhaseCompletion(f.ID, orchestrator.PhaseCompletionInput{
		Phase:      feature.PhasePlan,
		PlanResult: &agent.PlanLoopResult{FinalStatus: "approved"},
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	if len(worktrees.renameCalls) != 0 {
		t.Errorf("rename calls = %v, want none when approval routes to the review gate", worktrees.renameCalls)
	}
	for _, repo := range f.Repos {
		if repo.Branch != provisional {
			t.Errorf("repo %s branch = %q, want the untouched provisional %q", repo.Name, repo.Branch, provisional)
		}
	}
	if f.CurrentRoadmapPhase != 0 {
		t.Errorf("CurrentRoadmapPhase = %d, want 0 (gate pending)", f.CurrentRoadmapPhase)
	}
	// The stack still persists with branch names, as today.
	if len(f.Stack) != 2 || f.Stack[0].Branch != git.LayerBranchName(feature.WorkspaceSlug(f.Slug, f.ID), 1, "bootstrap") {
		t.Errorf("stack = %+v, want branch names persisted without a rename", f.Stack)
	}
}

// Publish on a repository record with an empty recorded branch errors naming
// the repository instead of fabricating a name.
func TestOrchestrator_PublishRepoWithoutRecordedBranchErrorsNamingRepository(t *testing.T) {
	f := &feature.Feature{
		ID:     "feat-pub-nobranch",
		Name:   "no-branch",
		Slug:   "no-branch",
		Status: feature.StatusReviewPassed,
		Models: config.ModelConfig{Planning: "sonnet"},
		// The approved stack exists; the repository record is what is broken.
		Stack: singleLayerStack(1, "feature/no-branch"),
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: "/tmp/r1", WorktreePath: "/tmp/wt-r1", BaseBranch: "main"},
		},
	}
	lc := lifecycleForFeature(f)
	lc.SetRepoPublishErrorFn = func(id, repo string, record errcat.FailureRecord) error { return nil }
	fs := newFeatureStore(f)
	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
		Remote:    mocks.NewMockRemoteOps(),
	}, orchestrator.Hooks{})

	err := o.Publish(f.ID)
	if err == nil || !strings.Contains(err.Error(), "r1") || !strings.Contains(err.Error(), "no feature branch recorded") {
		t.Fatalf("Publish() error = %v, want an error naming repo r1's missing branch", err)
	}
}

// Completion preflight on a publishable repository with an empty recorded
// branch errors naming the repository.
func TestOrchestrator_CompletionPreflightWithoutRecordedBranchErrorsNamingRepository(t *testing.T) {
	publishable := true
	f := &feature.Feature{
		ID:     "feat-pf-nobranch",
		Name:   "no-branch",
		Slug:   "no-branch",
		Status: feature.StatusReviewPassed,
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: "/tmp/r1", WorktreePath: "/tmp/wt-r1", BaseBranch: "main", Publishable: &publishable},
		},
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{})

	_, err := o.CompletionPreflight(f.ID)
	if err == nil || !strings.Contains(err.Error(), "r1") || !strings.Contains(err.Error(), "no feature branch recorded") {
		t.Fatalf("CompletionPreflight() error = %v, want an error naming repo r1's missing branch", err)
	}
}

// Local merge on a repository record with an empty recorded branch errors
// naming the repository.
func TestOrchestrator_MergeFeatureLocalWithoutRecordedBranchErrorsNamingRepository(t *testing.T) {
	notPublishable := false
	f := &feature.Feature{
		ID:     "feat-merge-nobranch",
		Name:   "no-branch",
		Slug:   "no-branch",
		Status: feature.StatusReviewPassed,
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: t.TempDir(), BaseBranch: "main", Publishable: &notPublishable},
		},
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{})

	err := o.MergeFeatureLocal(f.ID)
	if err == nil || !strings.Contains(err.Error(), "r1") || !strings.Contains(err.Error(), "no feature branch recorded") {
		t.Fatalf("MergeFeatureLocal() error = %v, want an error naming repo r1's missing branch", err)
	}
}
