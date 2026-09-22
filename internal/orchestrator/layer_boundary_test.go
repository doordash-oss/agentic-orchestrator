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
	"fmt"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// The layer-boundary fixtures: a two-layer, three-phase stack — layer 1
// "Bootstrap" covers phases 1 and 2, layer 2 "Build and polish" covers
// phase 3 — over three repositories, two with worktree paths and one
// without, so completing phase 2 crosses the boundary and completing
// phase 3 records only the top layer's tips.
const (
	boundaryLayer1Branch = "feature/pr-stacks-a1b2c3d4/1-bootstrap"
	boundaryLayer2Branch = "feature/pr-stacks-a1b2c3d4/2-build-and-polish"
	boundaryRepoATip     = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	boundaryRepoBTip     = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	boundaryRepoATopTip  = "cccccccccccccccccccccccccccccccccccccccc"
	boundaryRepoBTopTip  = "dddddddddddddddddddddddddddddddddddddddd"
)

// boundaryWorktrees is a fake worktree adapter that tracks the branch each
// worktree path is on, the refs that resolve per path with their SHAs, and
// the CreateBranchAtHead calls it served (and which paths must fail one).
type boundaryWorktrees struct {
	*mocks.MockWorktreeOps
	branches    map[string]string
	refs        map[string]map[string]string
	failCreate  map[string]error
	createCalls []string
}

func newBoundaryWorktrees() *boundaryWorktrees {
	return &boundaryWorktrees{
		MockWorktreeOps: mocks.NewMockWorktreeOps(),
		branches:        map[string]string{},
		refs:            map[string]map[string]string{},
		failCreate:      map[string]error{},
	}
}

func (w *boundaryWorktrees) on(path, branch string, refs map[string]string) *boundaryWorktrees {
	w.branches[path] = branch
	w.refs[path] = refs
	return w
}

func (w *boundaryWorktrees) CurrentBranch(path string) string { return w.branches[path] }

func (w *boundaryWorktrees) RefSHA(path, ref string) (string, error) {
	sha, ok := w.refs[path][ref]
	if !ok {
		return "", fmt.Errorf("ref %s does not resolve in %s", ref, path)
	}
	return sha, nil
}

func (w *boundaryWorktrees) CreateBranchAtHead(path, branch string) error {
	if err := w.failCreate[path]; err != nil {
		return err
	}
	w.createCalls = append(w.createCalls, path)
	w.refs[path][branch] = w.refs[path][w.branches[path]]
	w.branches[path] = branch
	return nil
}

func (w *boundaryWorktrees) createsFor(path string) int {
	count := 0
	for _, called := range w.createCalls {
		if called == path {
			count++
		}
	}
	return count
}

// onLayer1 returns a fake with both worktrees on layer 1's branch and only
// layer 1's ref resolving.
func onLayer1(repoA, repoB string) *boundaryWorktrees {
	return newBoundaryWorktrees().
		on(repoA, boundaryLayer1Branch, map[string]string{boundaryLayer1Branch: boundaryRepoATip}).
		on(repoB, boundaryLayer1Branch, map[string]string{boundaryLayer1Branch: boundaryRepoBTip})
}

// boundaryFeature builds the two-layer feature at the given roadmap phase.
// Phases 1 and 2 sit inside layer 1 (records on the layer-1 branch); phase
// 3 is the top layer's phase, so every record already sits on layer 2's
// branch from the phase-2 boundary.
func boundaryFeature(t *testing.T, phase int, repoA, repoB string) *feature.Feature {
	t.Helper()
	recordedBranch := boundaryLayer1Branch
	if phase >= 3 {
		recordedBranch = boundaryLayer2Branch
	}
	f := &feature.Feature{
		ID:                  "feat-layer-boundary",
		Name:                "Layer Boundary",
		Slug:                "pr-stacks",
		Status:              feature.StatusImplementing,
		CurrentPhase:        feature.PhaseImplement,
		CurrentRoadmapPhase: phase,
		TotalRoadmapPhases:  3,
		Stack: []feature.StackLayer{
			{Position: 1, Title: "Bootstrap", Slug: "bootstrap", Phases: []int{1, 2}, Branch: boundaryLayer1Branch},
			{Position: 2, Title: "Build and polish", Slug: "build-and-polish", Phases: []int{3}, Branch: boundaryLayer2Branch},
		},
		Repos: []feature.FeatureRepo{
			{Name: "repo-a", Path: repoA, WorktreePath: repoA, Branch: recordedBranch, BaseBranch: "main"},
			{Name: "repo-b", Path: repoB, WorktreePath: repoB, Branch: recordedBranch, BaseBranch: "main"},
			{Name: "repo-c", Path: "/src/repo-c", Branch: boundaryLayer1Branch, BaseBranch: "main"},
		},
		RepoStates: map[string]*feature.RepoState{
			"repo-a": {Touched: true},
			"repo-b": {Touched: true},
		},
	}
	f.Run().Setup = &feature.SetupState{
		Status: feature.SetupStatusDone,
		Tasks: map[string]feature.SetupTask{
			"worktree:repo-a": {Key: "worktree:repo-a", Kind: feature.SetupTaskWorktree, Repo: "repo-a", Status: feature.SetupStatusDone, Branch: recordedBranch},
			"worktree:repo-b": {Key: "worktree:repo-b", Kind: feature.SetupTaskWorktree, Repo: "repo-b", Status: feature.SetupStatusDone, Branch: recordedBranch},
			"worktree:repo-c": {Key: "worktree:repo-c", Kind: feature.SetupTaskWorktree, Repo: "repo-c", Status: feature.SetupStatusDone, Branch: boundaryLayer1Branch},
		},
	}
	return f
}

// boundaryHarness wires the fake lifecycle, the ordered feature store, and
// the fake worktrees around a boundary feature over two real repositories,
// recording the order the completion path crosses its writes.
type boundaryHarness struct {
	f        *feature.Feature
	lc       *mocks.MockFeatureLifecycle
	fs       *featureStore
	wt       *boundaryWorktrees
	o        *orchestrator.Orchestrator
	repoA    string
	repoB    string
	order    []string
	boundary *observe.LayerBoundaryEvent
	hookCall int
}

func newBoundaryHarness(t *testing.T, phase int, wtFor func(repoA, repoB string) *boundaryWorktrees) *boundaryHarness {
	t.Helper()
	repoA := testutil.InitGitRepo(t)
	repoB := testutil.InitGitRepo(t)
	f := boundaryFeature(t, phase, repoA, repoB)
	f.Artifacts = map[string]string{
		"roadmap": writeTempFile(t, "roadmap.md",
			"# Roadmap\n\n## Phase 1: Bootstrap\n### Goal\nInit\n\n## Phase 2: Build\n### Goal\nBuild\n\n## Phase 3: Polish\n### Goal\nPolish\n"),
	}
	lc := lifecycleForFeature(f)
	lc.CompleteImplementationFn = func(id string) error {
		f.Status = feature.StatusReviewPassed
		return nil
	}
	lc.StartPlanningFn = func(id string) error { f.Status = feature.StatusPlanning; return nil }
	lc.RecordRoadmapPhaseCommitAnchorsFn = func(id string, phase int, anchors map[string]string) error {
		return nil
	}
	wt := wtFor(repoA, repoB)
	h := &boundaryHarness{f: f, lc: lc, wt: wt, repoA: repoA, repoB: repoB}
	lc.AdvanceRoadmapPhaseFn = func(id string) error {
		h.order = append(h.order, "advance")
		f.CurrentRoadmapPhase++
		f.Status = feature.StatusPlanning
		return nil
	}
	fs := newFeatureStore(f)
	stackTips := func(ff *feature.Feature) int {
		total := 0
		for _, layer := range ff.Stack {
			total += len(layer.Repos)
		}
		return total
	}
	inner := fs.ModifyFn
	fs.ModifyFn = func(id string, fn func(ff *feature.Feature) error) error {
		tipsBefore := stackTips(f)
		err := inner(id, fn)
		if stackTips(f) > tipsBefore {
			h.order = append(h.order, "boundary-write")
		}
		return err
	}
	h.fs = fs
	h.o = orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs, Worktrees: wt}, orchestrator.Hooks{
		OnLayerBoundaryCrossed: func(featureID string, boundary observe.LayerBoundaryEvent) {
			h.hookCall++
			h.boundary = &boundary
		},
	})
	return h
}

func (h *boundaryHarness) complete(t *testing.T) error {
	t.Helper()
	return h.o.HandlePhaseCompletion(h.f.ID, orchestrator.PhaseCompletionInput{
		Phase:           feature.PhaseImplement,
		MultiRepoResult: &agent.OrchestratorResult{FinalStatus: "all_passed"},
	})
}

func (h *boundaryHarness) repo(name string) *feature.FeatureRepo {
	for i := range h.f.Repos {
		if h.f.Repos[i].Name == name {
			return &h.f.Repos[i]
		}
	}
	panic(fmt.Sprintf("repo %q not found", name))
}

func (h *boundaryHarness) setupTask(name string) feature.SetupTask {
	return h.f.Run().Setup.Tasks["worktree:"+name]
}

func (h *boundaryHarness) orderIndex(step string) int {
	return indexOf(h.order, step)
}

func (h *boundaryHarness) layerTips(layerPosition int) map[string]feature.StackRepoEntry {
	for _, layer := range h.f.Stack {
		if layer.Position == layerPosition {
			return layer.Repos
		}
	}
	return nil
}

// Completing a mid-layer phase (phase 1 of layer 1) records anchors only:
// no tips, no branch changes, no boundary observability.
func TestOrchestrator_LayerBoundary_MidLayerPhaseRecordsAnchorsOnly(t *testing.T) {
	h := newBoundaryHarness(t, 1, onLayer1)

	if err := h.complete(t); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	assertLifecycleCall(t, h.lc, "RecordRoadmapPhaseCommitAnchors")
	if got := len(h.layerTips(1)); got != 0 {
		t.Fatalf("layer 1 repo entries = %d after a mid-layer phase, want none", got)
	}
	if got := len(h.layerTips(2)); got != 0 {
		t.Fatalf("layer 2 repo entries = %d after a mid-layer phase, want none", got)
	}
	if h.repo("repo-a").Branch != boundaryLayer1Branch || h.repo("repo-b").Branch != boundaryLayer1Branch {
		t.Fatal("repository branches moved at a mid-layer phase")
	}
	if h.wt.createsFor(h.repoA) != 0 || h.wt.createsFor(h.repoB) != 0 {
		t.Fatal("worktrees were split at a mid-layer phase")
	}
	if h.hookCall != 0 {
		t.Fatal("boundary observer event fired at a mid-layer phase")
	}
	if h.orderIndex("boundary-write") != -1 {
		t.Fatalf("boundary persistence ran at a mid-layer phase; order = %v", h.order)
	}
	assertLifecycleCall(t, h.lc, "AdvanceRoadmapPhase")
}

// Completing a layer's last mid-flight phase (phase 2) records layer 1's
// tip per repository from layer 1's ref, creates and checks out layer 2's
// branch in every repository with a worktree path, persists the tips and
// the new branch on the repository records and the worktree setup tasks in
// one write before the roadmap phase advances, and emits the boundary
// observer event plus one repository status event per repository.
func TestOrchestrator_LayerBoundary_Phase2SplitsToLayer2(t *testing.T) {
	h := newBoundaryHarness(t, 2, onLayer1)

	if err := h.complete(t); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	// Layer 1's tips recorded from its ref, for every repository with a
	// worktree path — the repository without one is skipped and its record
	// left untouched.
	if got := h.layerTips(1)["repo-a"].TipSHA; got != boundaryRepoATip {
		t.Errorf("layer 1 repo-a tip = %q, want %q", got, boundaryRepoATip)
	}
	if got := h.layerTips(1)["repo-b"].TipSHA; got != boundaryRepoBTip {
		t.Errorf("layer 1 repo-b tip = %q, want %q", got, boundaryRepoBTip)
	}
	if _, ok := h.layerTips(1)["repo-c"]; ok {
		t.Error("repo-c (no worktree path) received a layer tip")
	}
	if got := len(h.layerTips(2)); got != 0 {
		t.Errorf("layer 2 entries = %d at the phase-2 boundary, want none", got)
	}

	// Branches moved to layer 2 on the repository records and the setup
	// tasks; the worktree-less repository keeps its recorded branch.
	for _, name := range []string{"repo-a", "repo-b"} {
		if got := h.repo(name).Branch; got != boundaryLayer2Branch {
			t.Errorf("%s recorded branch = %q, want %q", name, got, boundaryLayer2Branch)
		}
		if got := h.setupTask(name).Branch; got != boundaryLayer2Branch {
			t.Errorf("%s setup task branch = %q, want %q", name, got, boundaryLayer2Branch)
		}
	}
	if got := h.repo("repo-c").Branch; got != boundaryLayer1Branch {
		t.Errorf("repo-c recorded branch = %q, want it untouched on %q", got, boundaryLayer1Branch)
	}
	if got := h.setupTask("repo-c").Branch; got != boundaryLayer1Branch {
		t.Errorf("repo-c setup task branch = %q, want it untouched on %q", got, boundaryLayer1Branch)
	}

	// The worktrees are on layer 2's branch.
	if got := h.wt.branches[h.repoA]; got != boundaryLayer2Branch {
		t.Errorf("repo-a worktree on %q, want %q", got, boundaryLayer2Branch)
	}
	if got := h.wt.branches[h.repoB]; got != boundaryLayer2Branch {
		t.Errorf("repo-b worktree on %q, want %q", got, boundaryLayer2Branch)
	}

	// One write persisted the boundary before the roadmap phase advanced.
	if got := h.orderIndex("boundary-write"); got == -1 {
		t.Fatalf("boundary persistence never ran; order = %v", h.order)
	}
	if !(h.orderIndex("boundary-write") < h.orderIndex("advance")) {
		t.Fatalf("boundary write must precede the roadmap advance; order = %v", h.order)
	}

	// Boundary observability: one feature-scoped event with the layer
	// snapshot and the next layer, and one repository status event per
	// repository whose recorded branch changed.
	if h.hookCall != 1 {
		t.Fatalf("boundary observer events = %d, want 1", h.hookCall)
	}
	if h.boundary.LayerPosition != 1 || h.boundary.LayerTitle != "Bootstrap" || h.boundary.LayerBranch != boundaryLayer1Branch {
		t.Errorf("boundary event layer = %+v, want layer 1", *h.boundary)
	}
	if h.boundary.RepoTips["repo-a"] != boundaryRepoATip || h.boundary.RepoTips["repo-b"] != boundaryRepoBTip {
		t.Errorf("boundary event tips = %v, want both repository tips", h.boundary.RepoTips)
	}
	if h.boundary.NextLayerPosition != 2 || h.boundary.NextLayerBranch != boundaryLayer2Branch {
		t.Errorf("boundary event next layer = %d/%q, want 2/%q", h.boundary.NextLayerPosition, h.boundary.NextLayerBranch, boundaryLayer2Branch)
	}
	events := drainEvents(h.o)
	statusEvents := 0
	for _, ev := range events {
		if ev.Type != ports.RepoStatusChanged {
			continue
		}
		if ev.Branch != boundaryLayer2Branch {
			t.Errorf("RepoStatusChanged branch = %q, want %q", ev.Branch, boundaryLayer2Branch)
		}
		if ev.RepoName != "repo-a" && ev.RepoName != "repo-b" {
			t.Errorf("RepoStatusChanged repo = %q, want a worktree repository", ev.RepoName)
		}
		statusEvents++
	}
	if statusEvents != 2 {
		t.Fatalf("RepoStatusChanged events = %d, want one per worktree repository", statusEvents)
	}

	// The roadmap phase advanced and the next plan dispatched.
	assertLifecycleCall(t, h.lc, "AdvanceRoadmapPhase")
	sawAdvance := false
	for _, ev := range events {
		if ev.Type == ports.FeatureAdvanced && ev.Phase == feature.PhasePlan {
			sawAdvance = true
		}
	}
	if !sawAdvance {
		t.Error("expected FeatureAdvanced(PhasePlan) after the boundary")
	}
	if h.f.CurrentRoadmapPhase != 3 {
		t.Fatalf("CurrentRoadmapPhase = %d, want 3", h.f.CurrentRoadmapPhase)
	}
}

// Completing the final roadmap phase (phase 3) records only the top layer's
// tips and leaves every branch unchanged: no split, no repository status
// events, and a boundary event without the next layer.
func TestOrchestrator_LayerBoundary_Phase3RecordsTopLayerTipsOnly(t *testing.T) {
	onLayer2 := func(repoA, repoB string) *boundaryWorktrees {
		return newBoundaryWorktrees().
			on(repoA, boundaryLayer2Branch, map[string]string{boundaryLayer1Branch: boundaryRepoATip, boundaryLayer2Branch: boundaryRepoATopTip}).
			on(repoB, boundaryLayer2Branch, map[string]string{boundaryLayer1Branch: boundaryRepoBTip, boundaryLayer2Branch: boundaryRepoBTopTip})
	}
	h := newBoundaryHarness(t, 3, onLayer2)
	// Both repositories are untouched and non-publishable, so the final
	// phase needs no final-review pass and falls straight through to
	// MarkCodeReady instead of publish.
	unpub := false
	for i := range h.f.Repos {
		h.f.Repos[i].Publishable = &unpub
	}
	h.f.RepoStates["repo-a"].Touched = false
	h.f.RepoStates["repo-b"].Touched = false
	h.lc.MarkCodeReadyFn = func(id string) error {
		h.f.Status = feature.StatusCodeReady
		return nil
	}

	if err := h.complete(t); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	if got := h.layerTips(2)["repo-a"].TipSHA; got != boundaryRepoATopTip {
		t.Errorf("layer 2 repo-a tip = %q, want %q", got, boundaryRepoATopTip)
	}
	if got := h.layerTips(2)["repo-b"].TipSHA; got != boundaryRepoBTopTip {
		t.Errorf("layer 2 repo-b tip = %q, want %q", got, boundaryRepoBTopTip)
	}
	if _, ok := h.layerTips(2)["repo-c"]; ok {
		t.Error("repo-c (no worktree path) received a top layer tip")
	}
	if got := len(h.layerTips(1)); got != 0 {
		t.Errorf("layer 1 entries = %d at the final phase, want none recorded here", got)
	}
	for _, name := range []string{"repo-a", "repo-b"} {
		if got := h.repo(name).Branch; got != boundaryLayer2Branch {
			t.Errorf("%s recorded branch = %q, want unchanged %q", name, got, boundaryLayer2Branch)
		}
		if got := h.setupTask(name).Branch; got != boundaryLayer2Branch {
			t.Errorf("%s setup task branch = %q, want unchanged %q", name, got, boundaryLayer2Branch)
		}
	}
	if h.wt.createsFor(h.repoA) != 0 || h.wt.createsFor(h.repoB) != 0 {
		t.Fatal("worktrees were split while recording the top layer's tips")
	}
	if h.hookCall != 1 {
		t.Fatalf("boundary observer events = %d, want 1", h.hookCall)
	}
	if h.boundary.LayerPosition != 2 || h.boundary.LayerBranch != boundaryLayer2Branch {
		t.Errorf("boundary event layer = %+v, want the top layer", *h.boundary)
	}
	if h.boundary.NextLayerBranch != "" {
		t.Errorf("boundary event next layer = %q, want none for the final phase", h.boundary.NextLayerBranch)
	}
	for _, ev := range drainEvents(h.o) {
		if ev.Type == ports.RepoStatusChanged {
			t.Fatalf("unexpected RepoStatusChanged event while recording tips: %+v", ev)
		}
	}
	refuteLifecycleCall(t, h.lc, "AdvanceRoadmapPhase")
}

// A repository already on the next layer's branch is skipped without any
// git call while the others are split: the healed retry.
func TestOrchestrator_LayerBoundary_AlreadyOnNextBranchSkipsGitCall(t *testing.T) {
	h := newBoundaryHarness(t, 2, func(repoA, repoB string) *boundaryWorktrees {
		return newBoundaryWorktrees().
			// repo-a was already split by an earlier attempt.
			on(repoA, boundaryLayer2Branch, map[string]string{boundaryLayer1Branch: boundaryRepoATip, boundaryLayer2Branch: boundaryRepoATopTip}).
			on(repoB, boundaryLayer1Branch, map[string]string{boundaryLayer1Branch: boundaryRepoBTip})
	})

	if err := h.complete(t); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	if h.wt.createsFor(h.repoA) != 0 {
		t.Fatal("repo-a was already on layer 2's branch; it must be skipped without a git call")
	}
	if h.wt.createsFor(h.repoB) != 1 {
		t.Fatal("repo-b is on the recorded branch; it must be split exactly once")
	}
	if got := h.layerTips(1)["repo-a"].TipSHA; got != boundaryRepoATip {
		t.Errorf("layer 1 repo-a tip = %q, want it read from layer 1's ref %q", got, boundaryRepoATip)
	}
	if got := h.repo("repo-a").Branch; got != boundaryLayer2Branch {
		t.Errorf("repo-a recorded branch = %q, want %q", got, boundaryLayer2Branch)
	}
}

// A repository on an unexpected branch fails the boundary closed: no tips,
// repository branches, or setup task branches are persisted, the roadmap
// phase is not advanced, the error names the repository, its current
// branch, and the expected branch — and re-running the completion after
// the worktree is put back succeeds, skipping the repository the failed
// attempt already split.
func TestOrchestrator_LayerBoundary_UnexpectedBranchFailsClosedAndRetryHeals(t *testing.T) {
	h := newBoundaryHarness(t, 2, func(repoA, repoB string) *boundaryWorktrees {
		return newBoundaryWorktrees().
			on(repoA, "feature/wrong-branch", map[string]string{boundaryLayer1Branch: boundaryRepoATip}).
			on(repoB, boundaryLayer1Branch, map[string]string{boundaryLayer1Branch: boundaryRepoBTip})
	})

	err := h.complete(t)
	if err == nil {
		t.Fatal("HandlePhaseCompletion must fail when a repository is on an unexpected branch")
	}
	msg := err.Error()
	for _, want := range []string{"repo-a", "feature/wrong-branch", boundaryLayer1Branch, boundaryLayer2Branch} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q must name %q", msg, want)
		}
	}

	// Nothing persisted: no tips, no branch changes, no phase advance.
	if got := len(h.layerTips(1)); got != 0 {
		t.Errorf("layer 1 tips persisted despite failure: %v", h.layerTips(1))
	}
	for _, name := range []string{"repo-a", "repo-b", "repo-c"} {
		if got := h.repo(name).Branch; got != boundaryLayer1Branch {
			t.Errorf("%s recorded branch = %q, want untouched %q", name, got, boundaryLayer1Branch)
		}
		if got := h.setupTask(name).Branch; got != boundaryLayer1Branch {
			t.Errorf("%s setup task branch = %q, want untouched %q", name, got, boundaryLayer1Branch)
		}
	}
	refuteLifecycleCall(t, h.lc, "AdvanceRoadmapPhase")
	if h.f.CurrentRoadmapPhase != 2 {
		t.Fatalf("CurrentRoadmapPhase = %d, want 2 (not advanced)", h.f.CurrentRoadmapPhase)
	}
	if h.hookCall != 0 {
		t.Fatal("boundary observer event fired for a failed boundary")
	}
	if h.orderIndex("boundary-write") != -1 {
		t.Fatalf("boundary persistence ran despite failure; order = %v", h.order)
	}

	// The failed attempt still split repo-b (git steps are idempotent and
	// run for every repository); putting repo-a back and re-running the
	// completion succeeds, skipping repo-b without another git call.
	h.wt.branches[h.repoA] = boundaryLayer1Branch
	h.f.Status = feature.StatusImplementing
	if err := h.complete(t); err != nil {
		t.Fatalf("HandlePhaseCompletion after healing: %v", err)
	}
	if got := h.wt.createsFor(h.repoB); got != 1 {
		t.Fatalf("repo-b create calls = %d, want exactly one across both attempts", got)
	}
	if got := h.layerTips(1)["repo-a"].TipSHA; got != boundaryRepoATip {
		t.Errorf("layer 1 repo-a tip after retry = %q, want %q", got, boundaryRepoATip)
	}
	if got := h.repo("repo-a").Branch; got != boundaryLayer2Branch {
		t.Errorf("repo-a recorded branch after retry = %q, want %q", got, boundaryLayer2Branch)
	}
	assertLifecycleCall(t, h.lc, "AdvanceRoadmapPhase")
	if h.f.CurrentRoadmapPhase != 3 {
		t.Fatalf("CurrentRoadmapPhase after retry = %d, want 3", h.f.CurrentRoadmapPhase)
	}
}

// A target ref that already exists while not checked out fails the boundary
// closed: a stale ref is never clobbered.
func TestOrchestrator_LayerBoundary_ExistingTargetRefFailsClosed(t *testing.T) {
	h := newBoundaryHarness(t, 2, func(repoA, repoB string) *boundaryWorktrees {
		return newBoundaryWorktrees().
			on(repoA, boundaryLayer1Branch, map[string]string{
				boundaryLayer1Branch: boundaryRepoATip,
				// A stale layer-2 ref left behind by an earlier attempt.
				boundaryLayer2Branch: "e000000000000000000000000000000000000000",
			}).
			on(repoB, boundaryLayer1Branch, map[string]string{boundaryLayer1Branch: boundaryRepoBTip})
	})

	err := h.complete(t)
	if err == nil {
		t.Fatal("HandlePhaseCompletion must fail when the target ref already exists")
	}
	msg := err.Error()
	for _, want := range []string{"repo-a", boundaryLayer2Branch, "already exists as a ref"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q must name %q", msg, want)
		}
	}
	if got := len(h.layerTips(1)); got != 0 {
		t.Errorf("layer 1 tips persisted despite the stale ref: %v", h.layerTips(1))
	}
	refuteLifecycleCall(t, h.lc, "AdvanceRoadmapPhase")
	if h.wt.createsFor(h.repoA) != 0 {
		t.Fatal("the stale ref must not be clobbered by a create")
	}
}

// A roadmap feature with no stack, or whose current phase belongs to no
// layer, fails the boundary with the same canonical error and persists
// nothing.
func TestOrchestrator_LayerBoundary_NoStackOrPhaseOutsideLayersFailsClosed(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(f *feature.Feature)
		want   string
	}{
		{
			name:   "no stack",
			mutate: func(f *feature.Feature) { f.Stack = nil },
			want:   "no approved pull-request stack",
		},
		{
			name:   "phase belongs to no layer",
			mutate: func(f *feature.Feature) { f.Stack[0].Phases = []int{1} },
			want:   "belongs to no layer",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newBoundaryHarness(t, 2, onLayer1)
			tc.mutate(h.f)

			err := h.complete(t)
			if err == nil {
				t.Fatal("HandlePhaseCompletion must fail closed")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q must describe %q", err, tc.want)
			}
			if got := len(h.layerTips(1)); got != 0 {
				t.Errorf("tips persisted despite the closed failure: %v", h.layerTips(1))
			}
			refuteLifecycleCall(t, h.lc, "AdvanceRoadmapPhase")
			if h.wt.createsFor(h.repoA) != 0 || h.wt.createsFor(h.repoB) != 0 {
				t.Fatal("no git call may run before the stack resolves")
			}
		})
	}
}

// A non-roadmap feature (current roadmap phase zero) never records tips or
// touches branches, stack or no stack.
func TestOrchestrator_LayerBoundary_NonRoadmapNeverRecordsTips(t *testing.T) {
	h := newBoundaryHarness(t, 0, onLayer1)
	// Both repositories are untouched and non-publishable, so the
	// completion falls through to MarkCodeReady without a final-review
	// pass.
	unpub := false
	for i := range h.f.Repos {
		h.f.Repos[i].Publishable = &unpub
	}
	h.f.RepoStates["repo-a"].Touched = false
	h.f.RepoStates["repo-b"].Touched = false
	h.lc.MarkCodeReadyFn = func(id string) error {
		h.f.Status = feature.StatusCodeReady
		return nil
	}

	if err := h.complete(t); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	if got := len(h.layerTips(1)) + len(h.layerTips(2)); got != 0 {
		t.Fatalf("non-roadmap completion recorded layer tips: %+v", h.f.Stack)
	}
	for _, name := range []string{"repo-a", "repo-b"} {
		if got := h.repo(name).Branch; got != boundaryLayer1Branch {
			t.Errorf("%s recorded branch = %q, want untouched %q", name, got, boundaryLayer1Branch)
		}
	}
	if h.wt.createsFor(h.repoA) != 0 || h.wt.createsFor(h.repoB) != 0 {
		t.Fatal("non-roadmap completion touched the worktrees")
	}
	if h.hookCall != 0 {
		t.Fatal("non-roadmap completion emitted a boundary event")
	}
}
