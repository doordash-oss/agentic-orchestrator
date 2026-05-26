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

package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// newReviewCommentsTestFeature seeds a multi-repo feature whose RepoImpl
// entries are at "pr_ready" (post-publish) — the precondition for
// the unified review-comments cycle. The store is a real on-disk store
// so AtomicPhaseStamp's transactional writes round-trip through
// Modify/Load.
func newReviewCommentsTestFeature(t *testing.T, stateDir, featureID string, repoNames []string) (*feature.Store, *feature.Feature, []string) {
	t.Helper()
	store := feature.NewStore(stateDir)
	repos := make([]feature.FeatureRepo, 0, len(repoNames))
	repoPaths := make([]string, 0, len(repoNames))
	repoImpl := map[string]*feature.RepoState{}
	for _, name := range repoNames {
		repoDir := filepath.Join(t.TempDir(), name)
		if err := os.MkdirAll(repoDir, 0o755); err != nil {
			t.Fatalf("mkdir repo %q: %v", name, err)
		}
		repos = append(repos, feature.FeatureRepo{
			Name:       name,
			Path:       repoDir,
			BaseBranch: "main",
		})
		repoPaths = append(repoPaths, repoDir)
		repoImpl[name] = &feature.RepoState{
			Touched: true, PRURL: fmt.Sprintf("https://github.com/example/%s/pull/1", name),
		}
	}
	f := &feature.Feature{
		ID:            featureID,
		Name:          "Review Comments Loop Test",
		Slug:          "review-comments-loop-test",
		Description:   "Feature-level review-comments cycle test fixture",
		Status:        feature.StatusPublished,
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
		Repos:         repos,
		RepoStates:    repoImpl,
		ExitCriteria:  "Review comments addressed",
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}
	loaded, err := store.Load(featureID)
	if err != nil {
		t.Fatalf("reload feature: %v", err)
	}
	return store, loaded, repoPaths
}

// makeReviewCommentTarget is a builder for a per-repo target with N
// synthetic comments.
func makeReviewCommentTarget(repo string, prURL string, ids ...int) ReviewCommentsRepoTarget {
	cs := make([]ports.ReviewComment, 0, len(ids))
	for _, id := range ids {
		c := ports.ReviewComment{
			ID:   id,
			Path: fmt.Sprintf("%s/file.go", repo),
			Line: id,
			Body: fmt.Sprintf("comment %d body in %s", id, repo),
			Type: ports.CommentTypeReview,
		}
		c.User.Login = "reviewer"
		cs = append(cs, c)
	}
	return ReviewCommentsRepoTarget{
		RepoName: repo,
		PRURL:    prURL,
		Mode:     "auto",
		Comments: cs,
	}
}

// TestRunReviewCommentsLoop_SuccessAtomicallyStampsStagedRepos covers the
// SUCCESS path: the inner implement loop returns review_passed; the
// review-comments loop stamps every staged repo to
// "awaiting_final_review" and clears Feature.ActiveCycle.
func TestRunReviewCommentsLoop_SuccessAtomicallyStampsStagedRepos(t *testing.T) {
	stateDir := t.TempDir()
	store, f, _ := newReviewCommentsTestFeature(t, stateDir, "rc-success", []string{"api", "web", "infra"})

	cfg := ReviewCommentsLoopConfig{
		Feature:      f,
		FeatureStore: store,
		StateDir:     stateDir,
		RepoTargets: []ReviewCommentsRepoTarget{
			makeReviewCommentTarget("api", "https://github.com/example/api/pull/1", 101, 102),
			makeReviewCommentTarget("web", "https://github.com/example/web/pull/1", 201),
		},
		MaxIterations:  3,
		RunImplementFn: stubRunImplementFn(&LoopResult{FinalStatus: "review_passed", Iterations: 1}, nil),
	}

	result, err := RunReviewCommentsLoop(cfg, nil)
	if err != nil {
		t.Fatalf("RunReviewCommentsLoop: %v", err)
	}
	if result.FinalStatus != "review_passed" {
		t.Errorf("FinalStatus = %q, want review_passed", result.FinalStatus)
	}
	if got, want := result.Repos, []string{"api", "web"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Repos = %v, want %v", got, want)
	}

	loaded, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, name := range []string{"api", "web"} {
		st := loaded.RepoStates[name]
		if st == nil || st.Touched == false {
			t.Errorf("repo %s = %+v, want awaiting_final_review", name, st)
		}
	}
	if st := loaded.RepoStates["infra"]; st == nil || st.PRURL == "" {
		t.Errorf("infra = %+v, want pr_ready (preserved — no comments)", st)
	}
	if loaded.ActiveCycle != nil {
		t.Errorf("ActiveCycle = %+v, want nil after success", loaded.ActiveCycle)
	}
	if loaded.ReviewCommentsCount() != 1 {
		t.Errorf("ReviewCommentsCount = %d, want 1", loaded.ReviewCommentsCount())
	}
}

// TestRunReviewCommentsLoop_RetryLandsAfterFollowupIteration exercises a
// RETRY path: the inner loop returns review_passed after Iterations: 2 —
// implementer needed a second pass to address a tricky comment, reviewer
// approved on iter-2.
func TestRunReviewCommentsLoop_RetryLandsAfterFollowupIteration(t *testing.T) {
	stateDir := t.TempDir()
	store, f, _ := newReviewCommentsTestFeature(t, stateDir, "rc-retry", []string{"api", "web"})

	cfg := ReviewCommentsLoopConfig{
		Feature:      f,
		FeatureStore: store,
		StateDir:     stateDir,
		RepoTargets: []ReviewCommentsRepoTarget{
			makeReviewCommentTarget("api", "https://github.com/example/api/pull/1", 1, 2, 3),
			makeReviewCommentTarget("web", "https://github.com/example/web/pull/1", 50),
		},
		MaxIterations:  5,
		RunImplementFn: stubRunImplementFn(&LoopResult{FinalStatus: "review_passed", Iterations: 2}, nil),
	}

	result, err := RunReviewCommentsLoop(cfg, nil)
	if err != nil {
		t.Fatalf("RunReviewCommentsLoop: %v", err)
	}
	if result.FinalStatus != "review_passed" {
		t.Errorf("FinalStatus = %q, want review_passed", result.FinalStatus)
	}
	if result.Iterations != 2 {
		t.Errorf("Iterations = %d, want 2 (followup pass)", result.Iterations)
	}

	loaded, _ := store.Load(f.ID)
	planPath := filepath.Join(ActiveRunDir(stateDir, loaded), "review-comments-1", "review-plan.md")
	planBytes, readErr := os.ReadFile(planPath)
	if readErr != nil {
		t.Fatalf("read review-plan.md: %v", readErr)
	}
	plan := string(planBytes)
	for _, want := range []string{"## Repo: `api`", "## Repo: `web`", "ID: 1)", "ID: 50)"} {
		if !strings.Contains(plan, want) {
			t.Errorf("plan missing %q\n---\n%s", want, plan)
		}
	}
}

// TestRunReviewCommentsLoop_MaxIterationsTrip exercises the safety-rail
// trip: inner loop returns max_iterations. The cycle stamps every staged
// repo "failed"; ActiveCycle.Status flips to "failed".
func TestRunReviewCommentsLoop_MaxIterationsTrip(t *testing.T) {
	stateDir := t.TempDir()
	store, f, _ := newReviewCommentsTestFeature(t, stateDir, "rc-maxiter", []string{"api", "web"})

	cfg := ReviewCommentsLoopConfig{
		Feature:      f,
		FeatureStore: store,
		StateDir:     stateDir,
		RepoTargets: []ReviewCommentsRepoTarget{
			makeReviewCommentTarget("api", "https://github.com/example/api/pull/1", 1),
		},
		MaxIterations: 2,
		RunImplementFn: stubRunImplementFn(&LoopResult{
			FinalStatus: "max_iterations",
			Iterations:  2,
			LastError:   "Reviewer kept rejecting: comment 1 fix introduced regression",
		}, nil),
	}

	result, err := RunReviewCommentsLoop(cfg, nil)
	if err != nil {
		t.Fatalf("RunReviewCommentsLoop: %v", err)
	}
	if result.FinalStatus != "max_iterations" {
		t.Errorf("FinalStatus = %q, want max_iterations", result.FinalStatus)
	}
	if !strings.Contains(result.LastError, "regression") {
		t.Errorf("LastError = %q, want it to surface inner error", result.LastError)
	}

	loaded, _ := store.Load(f.ID)
	if st := loaded.RepoStates["api"]; st == nil || st.LastError == "" {
		t.Errorf("api = %+v, want failed", st)
	}
	// Repo without comments preserved.
	if st := loaded.RepoStates["web"]; st == nil || st.PRURL == "" {
		t.Errorf("web = %+v, want pr_ready (preserved — no comments staged)", st)
	}
	if loaded.ActiveCycle == nil {
		t.Fatal("ActiveCycle = nil, want non-nil with Status=failed")
	}
	if loaded.ActiveCycle.Status != feature.RepoCycleFailed {
		t.Errorf("ActiveCycle.Status = %q, want failed", loaded.ActiveCycle.Status)
	}
	if loaded.ActiveCycle.Type != feature.CycleReviewComments {
		t.Errorf("ActiveCycle.Type = %q, want review-comments", loaded.ActiveCycle.Type)
	}
}

// TestRunReviewCommentsLoop_SafetyRailTrip exercises the consec-fails
// safety rail: inner loop returns safety_rail.
func TestRunReviewCommentsLoop_SafetyRailTrip(t *testing.T) {
	stateDir := t.TempDir()
	store, f, _ := newReviewCommentsTestFeature(t, stateDir, "rc-safety", []string{"api", "web"})

	cfg := ReviewCommentsLoopConfig{
		Feature:      f,
		FeatureStore: store,
		StateDir:     stateDir,
		RepoTargets: []ReviewCommentsRepoTarget{
			makeReviewCommentTarget("api", "https://github.com/example/api/pull/1", 1, 2),
			makeReviewCommentTarget("web", "https://github.com/example/web/pull/1", 5),
		},
		MaxIterations:  10,
		MaxConsecFails: 2,
		RunImplementFn: stubRunImplementFn(&LoopResult{
			FinalStatus: "safety_rail",
			Iterations:  3,
			LastError:   "no progress for 3 consecutive iterations",
		}, nil),
	}

	result, err := RunReviewCommentsLoop(cfg, nil)
	if err != nil {
		t.Fatalf("RunReviewCommentsLoop: %v", err)
	}
	if result.FinalStatus != "safety_rail" {
		t.Errorf("FinalStatus = %q, want safety_rail", result.FinalStatus)
	}

	loaded, _ := store.Load(f.ID)
	for _, name := range []string{"api", "web"} {
		st := loaded.RepoStates[name]
		if st == nil || st.LastError == "" {
			t.Errorf("repo %s = %+v, want failed", name, st)
		}
	}
}

// TestRunReviewCommentsLoop_DispatchErrorStampsFailure verifies that when
// the inner loop dispatcher returns an error, the cycle still stamps
// "failed" and surfaces the error.
func TestRunReviewCommentsLoop_DispatchErrorStampsFailure(t *testing.T) {
	stateDir := t.TempDir()
	store, f, _ := newReviewCommentsTestFeature(t, stateDir, "rc-dispatch-err", []string{"api"})

	dispatchErr := errors.New("session manager: ports.ErrSessionShuttingDown")
	cfg := ReviewCommentsLoopConfig{
		Feature:      f,
		FeatureStore: store,
		StateDir:     stateDir,
		RepoTargets: []ReviewCommentsRepoTarget{
			makeReviewCommentTarget("api", "https://github.com/example/api/pull/1", 7),
		},
		MaxIterations:  3,
		RunImplementFn: stubRunImplementFn(nil, dispatchErr),
	}

	result, err := RunReviewCommentsLoop(cfg, nil)
	if err == nil {
		t.Fatalf("RunReviewCommentsLoop returned nil error, want %v", dispatchErr)
	}
	if !errors.Is(err, dispatchErr) {
		t.Errorf("err = %v, want errors.Is %v", err, dispatchErr)
	}
	if result == nil {
		t.Fatal("result = nil, want non-nil")
	}
	if result.FinalStatus != "failed" {
		t.Errorf("FinalStatus = %q, want failed", result.FinalStatus)
	}

	loaded, _ := store.Load(f.ID)
	if st := loaded.RepoStates["api"]; st == nil || st.LastError == "" {
		t.Errorf("api = %+v, want failed", st)
	}
}

// TestRunReviewCommentsLoop_InterruptedPreservesState verifies "interrupted"
// preserves state for resume.
func TestRunReviewCommentsLoop_InterruptedPreservesState(t *testing.T) {
	stateDir := t.TempDir()
	store, f, _ := newReviewCommentsTestFeature(t, stateDir, "rc-interrupt", []string{"api"})

	cfg := ReviewCommentsLoopConfig{
		Feature:      f,
		FeatureStore: store,
		StateDir:     stateDir,
		RepoTargets: []ReviewCommentsRepoTarget{
			makeReviewCommentTarget("api", "https://github.com/example/api/pull/1", 1),
		},
		MaxIterations:  3,
		RunImplementFn: stubRunImplementFn(&LoopResult{FinalStatus: "interrupted", Iterations: 1}, nil),
	}

	result, err := RunReviewCommentsLoop(cfg, nil)
	if err != nil {
		t.Fatalf("RunReviewCommentsLoop: %v", err)
	}
	if result.FinalStatus != "interrupted" {
		t.Errorf("FinalStatus = %q, want interrupted", result.FinalStatus)
	}

	loaded, _ := store.Load(f.ID)
	if st := loaded.RepoStates["api"]; st == nil || st.PRURL == "" {
		t.Errorf("api = %+v, want pr_ready (preserved on interrupt)", st)
	}
	if loaded.ActiveCycle == nil {
		t.Fatal("ActiveCycle = nil, want preserved at Status=running for resume")
	}
	if loaded.ActiveCycle.Status != feature.RepoCycleRunning {
		t.Errorf("ActiveCycle.Status = %q, want running (preserved)", loaded.ActiveCycle.Status)
	}
}

// TestRunReviewCommentsLoop_NeedUserInputSurfacesGate verifies that an
// ambiguous review-comment decision pauses the cycle instead of stamping
// staged repos failed.
func TestRunReviewCommentsLoop_NeedUserInputSurfacesGate(t *testing.T) {
	stateDir := t.TempDir()
	store, f, _ := newReviewCommentsTestFeature(t, stateDir, "rc-nui", []string{"api", "web"})
	gatePath := filepath.Join(stateDir, "review-comments-1", "iteration-01", "need-user-input.yaml")

	cfg := ReviewCommentsLoopConfig{
		Feature:      f,
		FeatureStore: store,
		StateDir:     stateDir,
		RepoTargets: []ReviewCommentsRepoTarget{
			makeReviewCommentTarget("api", "https://github.com/example/api/pull/1", 1),
			makeReviewCommentTarget("web", "https://github.com/example/web/pull/1", 2),
		},
		MaxIterations: 3,
		RunImplementFn: stubRunImplementFn(&LoopResult{
			FinalStatus:       "need_user_input",
			Iterations:        1,
			LastError:         "Reviewer request conflicts with product decision.",
			NeedUserInputPath: gatePath,
		}, nil),
	}

	result, err := RunReviewCommentsLoop(cfg, nil)
	if err != nil {
		t.Fatalf("RunReviewCommentsLoop: %v", err)
	}
	if result.FinalStatus != "need_user_input" {
		t.Fatalf("FinalStatus = %q, want need_user_input", result.FinalStatus)
	}
	if result.NeedUserInputPath != gatePath {
		t.Errorf("NeedUserInputPath = %q, want %q", result.NeedUserInputPath, gatePath)
	}

	loaded, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.PendingNeedUserInputPath != gatePath {
		t.Errorf("PendingNeedUserInputPath = %q, want %q", loaded.PendingNeedUserInputPath, gatePath)
	}
	for _, name := range []string{"api", "web"} {
		st := loaded.RepoStates[name]
		if st == nil || st.LastError != "" {
			t.Errorf("repo %s = %+v, want prior state without failure", name, st)
		}
	}
	if loaded.ActiveCycle == nil {
		t.Fatal("ActiveCycle = nil, want preserved at Status=running for gate resume")
	}
	if loaded.ActiveCycle.Status != feature.RepoCycleRunning {
		t.Errorf("ActiveCycle.Status = %q, want running", loaded.ActiveCycle.Status)
	}
}

// TestRunReviewCommentsLoop_NoTargetsShortCircuits verifies the no-op
// degenerate case: zero repos with comments returns FinalStatus=no_op
// without touching state.
func TestRunReviewCommentsLoop_NoTargetsShortCircuits(t *testing.T) {
	stateDir := t.TempDir()
	store, f, _ := newReviewCommentsTestFeature(t, stateDir, "rc-empty", []string{"api"})

	cfg := ReviewCommentsLoopConfig{
		Feature:      f,
		FeatureStore: store,
		StateDir:     stateDir,
		RepoTargets:  nil,
	}

	result, err := RunReviewCommentsLoop(cfg, nil)
	if err != nil {
		t.Fatalf("RunReviewCommentsLoop: %v", err)
	}
	if result.FinalStatus != "no_op" {
		t.Errorf("FinalStatus = %q, want no_op", result.FinalStatus)
	}

	loaded, _ := store.Load(f.ID)
	if loaded.ReviewCommentsCount() != 0 {
		t.Errorf("ReviewCommentsCount = %d, want 0 (no-op)", loaded.ReviewCommentsCount())
	}
	if loaded.ActiveCycle != nil {
		t.Errorf("ActiveCycle = %+v, want nil", loaded.ActiveCycle)
	}
}

// TestRunReviewCommentsLoop_FullWorkspaceMounted verifies the cycle
// mounts EVERY Feature.Repos worktree (not just the staged subset) —
// review threads frequently reference cross-repo behavior.
func TestRunReviewCommentsLoop_FullWorkspaceMounted(t *testing.T) {
	stateDir := t.TempDir()
	store, f, repoPaths := newReviewCommentsTestFeature(t, stateDir, "rc-workspace", []string{"api", "web", "infra"})

	captureFn, captured := capturingRunImplementFn(&LoopResult{FinalStatus: "review_passed", Iterations: 1})

	cfg := ReviewCommentsLoopConfig{
		Feature:      f,
		FeatureStore: store,
		StateDir:     stateDir,
		RepoTargets: []ReviewCommentsRepoTarget{
			makeReviewCommentTarget("api", "https://github.com/example/api/pull/1", 1),
			// web has comments; infra does NOT — but infra MUST be mounted.
			makeReviewCommentTarget("web", "https://github.com/example/web/pull/1", 2),
		},
		MaxIterations:  3,
		RunImplementFn: captureFn,
	}

	if _, err := RunReviewCommentsLoop(cfg, nil); err != nil {
		t.Fatalf("RunReviewCommentsLoop: %v", err)
	}
	if len(*captured) != 1 {
		t.Fatalf("captured = %d, want 1", len(*captured))
	}
	got := (*captured)[0]
	dirs := got.AdditionalDirs

	apiAbs, _ := filepath.Abs(repoPaths[0])
	webAbs, _ := filepath.Abs(repoPaths[1])
	infraAbs, _ := filepath.Abs(repoPaths[2])

	if !sliceContainsAny(dirs, apiAbs) {
		t.Errorf("AdditionalDirs missing api worktree %q (got %v)", apiAbs, dirs)
	}
	if !sliceContainsAny(dirs, webAbs) {
		t.Errorf("AdditionalDirs missing web worktree %q (got %v)", webAbs, dirs)
	}
	if !sliceContainsAny(dirs, infraAbs) {
		t.Errorf("AdditionalDirs missing infra worktree %q — review-comments should mount full workspace, not behind subset", infraAbs)
	}
}

// TestRunReviewCommentsLoop_PlanLessTestingContractEmitted verifies the
// loop persists a plan-less testing contract: per-repo baseline rows
// only, no plan-source items.
func TestRunReviewCommentsLoop_PlanLessTestingContractEmitted(t *testing.T) {
	stateDir := t.TempDir()
	store, f, _ := newReviewCommentsTestFeature(t, stateDir, "rc-contract", []string{"api", "web"})

	cfg := ReviewCommentsLoopConfig{
		Feature:      f,
		FeatureStore: store,
		StateDir:     stateDir,
		RepoTargets: []ReviewCommentsRepoTarget{
			makeReviewCommentTarget("api", "https://github.com/example/api/pull/1", 1),
			makeReviewCommentTarget("web", "https://github.com/example/web/pull/1", 2),
		},
		MaxIterations:  3,
		RunImplementFn: stubRunImplementFn(&LoopResult{FinalStatus: "review_passed", Iterations: 1}, nil),
	}

	if _, err := RunReviewCommentsLoop(cfg, nil); err != nil {
		t.Fatalf("RunReviewCommentsLoop: %v", err)
	}

	loaded, _ := store.Load(f.ID)
	contractPath := filepath.Join(ActiveRunDir(stateDir, loaded), "review-comments-1", "testing-contract.yaml")
	contract, readErr := ReadTestingContract(contractPath)
	if readErr != nil {
		t.Fatalf("read contract: %v", readErr)
	}
	gotPerRepo := map[string]int{}
	for _, item := range contract.Items {
		if item.Source == testingContractPlanSource {
			t.Errorf("plan-source item leaked into plan-less review-comments contract: %+v", item)
		}
		gotPerRepo[item.Repo]++
	}
	if gotPerRepo["api"] == 0 || gotPerRepo["web"] == 0 {
		t.Errorf("expected per-repo baseline rows for api+web; got %v", gotPerRepo)
	}
}

// TestRunReviewCommentsLoop_FlatArtifactDirLayout verifies the cycle
// artifact dir flattens — `runs/run-N/review-comments-N/iteration-NN/`
// (no per-repo subdir).
func TestRunReviewCommentsLoop_FlatArtifactDirLayout(t *testing.T) {
	stateDir := t.TempDir()
	store, f, _ := newReviewCommentsTestFeature(t, stateDir, "rc-flat", []string{"api", "web"})

	captureFn, captured := capturingRunImplementFn(&LoopResult{FinalStatus: "review_passed"})

	cfg := ReviewCommentsLoopConfig{
		Feature:      f,
		FeatureStore: store,
		StateDir:     stateDir,
		RepoTargets: []ReviewCommentsRepoTarget{
			makeReviewCommentTarget("api", "https://github.com/example/api/pull/1", 1),
			makeReviewCommentTarget("web", "https://github.com/example/web/pull/1", 2),
		},
		MaxIterations:  3,
		RunImplementFn: captureFn,
	}

	if _, err := RunReviewCommentsLoop(cfg, nil); err != nil {
		t.Fatalf("RunReviewCommentsLoop: %v", err)
	}
	if len(*captured) != 1 {
		t.Fatalf("captured = %d, want 1", len(*captured))
	}
	gotDir := (*captured)[0].ArtifactDir
	loaded, _ := store.Load(f.ID)
	wantDir := filepath.Join(ActiveRunDir(stateDir, loaded), "review-comments-1")
	if gotDir != wantDir {
		t.Errorf("ArtifactDir = %q, want %q (flat layout)", gotDir, wantDir)
	}
	for _, repo := range []string{"api", "web"} {
		legacyPath := filepath.Join(wantDir, repo)
		if _, statErr := os.Stat(legacyPath); statErr == nil {
			t.Errorf("legacy per-repo subdir %q exists; flat layout violated", legacyPath)
		}
	}
}

// TestRunReviewCommentsLoop_CountIncrementsPerInvocation verifies the
// per-invocation counter bump and dir naming.
func TestRunReviewCommentsLoop_CountIncrementsPerInvocation(t *testing.T) {
	stateDir := t.TempDir()
	store, f, _ := newReviewCommentsTestFeature(t, stateDir, "rc-count", []string{"api"})

	cfg := ReviewCommentsLoopConfig{
		Feature:      f,
		FeatureStore: store,
		StateDir:     stateDir,
		RepoTargets: []ReviewCommentsRepoTarget{
			makeReviewCommentTarget("api", "https://github.com/example/api/pull/1", 1),
		},
		MaxIterations:  3,
		RunImplementFn: stubRunImplementFn(&LoopResult{FinalStatus: "review_passed"}, nil),
	}
	if _, err := RunReviewCommentsLoop(cfg, nil); err != nil {
		t.Fatalf("first RunReviewCommentsLoop: %v", err)
	}
	loaded, _ := store.Load(f.ID)
	if loaded.ReviewCommentsCount() != 1 {
		t.Errorf("after first invocation, ReviewCommentsCount = %d, want 1", loaded.ReviewCommentsCount())
	}
	if _, statErr := os.Stat(filepath.Join(ActiveRunDir(stateDir, loaded), "review-comments-1")); statErr != nil {
		t.Errorf("review-comments-1 dir missing: %v", statErr)
	}

	// Reset staged repo to CodeReady so a second invocation makes
	// sense.
	if err := store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.RepoStates["api"].LastError = ""
		return nil
	}); err != nil {
		t.Fatalf("modify: %v", err)
	}
	loaded, _ = store.Load(f.ID)
	cfg.Feature = loaded
	if _, err := RunReviewCommentsLoop(cfg, nil); err != nil {
		t.Fatalf("second RunReviewCommentsLoop: %v", err)
	}
	loaded, _ = store.Load(f.ID)
	if loaded.ReviewCommentsCount() != 2 {
		t.Errorf("after second invocation, ReviewCommentsCount = %d, want 2", loaded.ReviewCommentsCount())
	}
	if _, statErr := os.Stat(filepath.Join(ActiveRunDir(stateDir, loaded), "review-comments-2")); statErr != nil {
		t.Errorf("review-comments-2 dir missing: %v", statErr)
	}
}

// TestRunReviewCommentsLoop_CrashRecoveryReusesArtifactDir verifies
// mid-iteration crash recovery: when ActiveCycle is already running with
// Count=1 from a prior crashed run, the next invocation increments the
// counter and the inner ImplementConfig.ArtifactDir points at the new
// review-comments-N dir, with iteration-01 from the prior crash still
// readable so ArtifactManager.LatestIteration sees it.
func TestRunReviewCommentsLoop_CrashRecoveryReusesArtifactDir(t *testing.T) {
	stateDir := t.TempDir()
	store, f, _ := newReviewCommentsTestFeature(t, stateDir, "rc-recover", []string{"api"})

	if err := store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.SetReviewCommentsCount(1)
		ff.SetActiveCycleType(feature.CycleReviewComments)
		ff.ActiveCycle = &feature.CycleState{
			Type:   feature.CycleReviewComments,
			Status: feature.RepoCycleRunning,
			Count:  1,
		}
		return nil
	}); err != nil {
		t.Fatalf("seed prior crash state: %v", err)
	}
	loaded, _ := store.Load(f.ID)

	priorDir := filepath.Join(ActiveRunDir(stateDir, loaded), "review-comments-2", "iteration-01")
	if err := os.MkdirAll(priorDir, 0o755); err != nil {
		t.Fatalf("seed iteration-01: %v", err)
	}

	captureFn, captured := capturingRunImplementFn(&LoopResult{FinalStatus: "review_passed", Iterations: 2})

	cfg := ReviewCommentsLoopConfig{
		Feature:      loaded,
		FeatureStore: store,
		StateDir:     stateDir,
		RepoTargets: []ReviewCommentsRepoTarget{
			makeReviewCommentTarget("api", "https://github.com/example/api/pull/1", 1),
		},
		MaxIterations:  3,
		RunImplementFn: captureFn,
	}

	if _, err := RunReviewCommentsLoop(cfg, nil); err != nil {
		t.Fatalf("RunReviewCommentsLoop: %v", err)
	}
	if len(*captured) != 1 {
		t.Fatalf("captured = %d, want 1", len(*captured))
	}
	gotDir := (*captured)[0].ArtifactDir
	loaded, _ = store.Load(f.ID)
	wantDir := filepath.Join(ActiveRunDir(stateDir, loaded), "review-comments-2")
	if gotDir != wantDir {
		t.Errorf("recovered ArtifactDir = %q, want %q (re-uses review-comments-2 from prior crash)", gotDir, wantDir)
	}
	if _, err := os.Stat(filepath.Join(wantDir, "iteration-01")); err != nil {
		t.Errorf("iteration-01 missing: %v (recovery would not see prior iteration)", err)
	}
}

// TestRunReviewCommentsLoop_ActiveCycleSetAtEntry verifies the cycle
// entry stamp lands before the inner loop runs.
func TestRunReviewCommentsLoop_ActiveCycleSetAtEntry(t *testing.T) {
	stateDir := t.TempDir()
	store, f, _ := newReviewCommentsTestFeature(t, stateDir, "rc-cycle-entry", []string{"api"})

	var midRunCycle *feature.CycleState
	cfg := ReviewCommentsLoopConfig{
		Feature:      f,
		FeatureStore: store,
		StateDir:     stateDir,
		RepoTargets: []ReviewCommentsRepoTarget{
			makeReviewCommentTarget("api", "https://github.com/example/api/pull/1", 1),
		},
		MaxIterations: 3,
		RunImplementFn: func(_ ImplementConfig, _ ports.SessionManager) (*LoopResult, error) {
			loaded, _ := store.Load(f.ID)
			if loaded != nil && loaded.ActiveCycle != nil {
				cp := *loaded.ActiveCycle
				midRunCycle = &cp
			}
			return &LoopResult{FinalStatus: "review_passed"}, nil
		},
	}

	if _, err := RunReviewCommentsLoop(cfg, nil); err != nil {
		t.Fatalf("RunReviewCommentsLoop: %v", err)
	}
	if midRunCycle == nil {
		t.Fatal("ActiveCycle was nil during inner loop run; stamp never landed")
	}
	if midRunCycle.Type != feature.CycleReviewComments {
		t.Errorf("ActiveCycle.Type = %q, want review-comments", midRunCycle.Type)
	}
	if midRunCycle.Status != feature.RepoCycleRunning {
		t.Errorf("ActiveCycle.Status = %q, want running", midRunCycle.Status)
	}
}

// TestRunReviewCommentsLoop_NilFeatureReturnsError covers defensive
// validation.
func TestRunReviewCommentsLoop_NilFeatureReturnsError(t *testing.T) {
	_, err := RunReviewCommentsLoop(ReviewCommentsLoopConfig{}, nil)
	if err == nil {
		t.Fatal("RunReviewCommentsLoop with nil feature returned nil error")
	}
}

// TestRunReviewCommentsLoop_CrossPRAggregationSurfacesAllComments verifies
// that comments from multiple PRs all reach the implement prompt — the
// core "aggregate across all PRs" guarantee. Drives via the synchronously
// staged plan markdown, since the loop persists the aggregated plan
// before invoking the inner implement loop.
func TestRunReviewCommentsLoop_CrossPRAggregationSurfacesAllComments(t *testing.T) {
	stateDir := t.TempDir()
	store, f, _ := newReviewCommentsTestFeature(t, stateDir, "rc-aggregate", []string{"api", "web", "infra"})

	cfg := ReviewCommentsLoopConfig{
		Feature:      f,
		FeatureStore: store,
		StateDir:     stateDir,
		RepoTargets: []ReviewCommentsRepoTarget{
			makeReviewCommentTarget("api", "https://github.com/example/api/pull/1", 100, 101, 102),
			makeReviewCommentTarget("web", "https://github.com/example/web/pull/1", 200, 201),
			// infra: no comments — should be omitted from the plan.
		},
		MaxIterations:  3,
		RunImplementFn: stubRunImplementFn(&LoopResult{FinalStatus: "review_passed"}, nil),
	}

	if _, err := RunReviewCommentsLoop(cfg, nil); err != nil {
		t.Fatalf("RunReviewCommentsLoop: %v", err)
	}

	loaded, _ := store.Load(f.ID)
	planPath := filepath.Join(ActiveRunDir(stateDir, loaded), "review-comments-1", "review-plan.md")
	planBytes, readErr := os.ReadFile(planPath)
	if readErr != nil {
		t.Fatalf("read review-plan.md: %v", readErr)
	}
	plan := string(planBytes)

	// Every staged repo's section must appear.
	for _, want := range []string{"## Repo: `api`", "## Repo: `web`"} {
		if !strings.Contains(plan, want) {
			t.Errorf("plan missing repo section %q", want)
		}
	}
	// Every comment ID must appear.
	for _, id := range []int{100, 101, 102, 200, 201} {
		needle := fmt.Sprintf("ID: %d)", id)
		if !strings.Contains(plan, needle) {
			t.Errorf("plan missing comment %q", needle)
		}
	}
	// Each comment must be tagged with its repo.
	for _, want := range []string{"`repo: api`", "`repo: web`"} {
		if !strings.Contains(plan, want) {
			t.Errorf("plan missing repo tag %q", want)
		}
	}
	// infra must NOT appear (no comments).
	if strings.Contains(plan, "## Repo: `infra`") {
		t.Errorf("plan unexpectedly includes infra (no comments staged)")
	}
	// The combined resolutions JSON path must appear once.
	wantResolutions := filepath.Join(ActiveRunDir(stateDir, loaded), "review-comments-1", "review-resolutions.json")
	if !strings.Contains(plan, wantResolutions) {
		t.Errorf("plan missing combined resolutions path %q", wantResolutions)
	}
}

// TestRunReviewCommentsLoop_RepoNamesDeduplicateAndSort verifies the
// loop deduplicates and sorts repo names (used as the AtomicPhaseStamp
// staged subset).
func TestRunReviewCommentsLoop_RepoNamesDeduplicateAndSort(t *testing.T) {
	stateDir := t.TempDir()
	store, f, _ := newReviewCommentsTestFeature(t, stateDir, "rc-dedup", []string{"api", "web"})

	cfg := ReviewCommentsLoopConfig{
		Feature:      f,
		FeatureStore: store,
		StateDir:     stateDir,
		RepoTargets: []ReviewCommentsRepoTarget{
			makeReviewCommentTarget("web", "https://github.com/example/web/pull/1", 1),
			makeReviewCommentTarget("api", "https://github.com/example/api/pull/1", 2),
			makeReviewCommentTarget("web", "https://github.com/example/web/pull/1", 3), // duplicate repo
		},
		MaxIterations:  3,
		RunImplementFn: stubRunImplementFn(&LoopResult{FinalStatus: "review_passed"}, nil),
	}

	result, err := RunReviewCommentsLoop(cfg, nil)
	if err != nil {
		t.Fatalf("RunReviewCommentsLoop: %v", err)
	}
	want := []string{"api", "web"}
	got := append([]string(nil), result.Repos...)
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Repos = %v, want %v (deduplicated and sorted)", got, want)
	}
}

// TestBuildAggregatedReviewCommentsPlan_Formatting covers the plan
// composer for the multi-repo case: every staged repo gets a per-repo
// section, each comment is tagged with its repo, comment IDs are listed,
// the combined resolutions path appears.
func TestBuildAggregatedReviewCommentsPlan_Formatting(t *testing.T) {
	c1 := ports.ReviewComment{ID: 11, Path: "src/a.go", Line: 5, Body: "needs cleanup", Type: ports.CommentTypeReview}
	c1.User.Login = "alice"
	c2 := ports.ReviewComment{ID: 22, Body: "general note", Type: ports.CommentTypeIssue}
	c2.User.Login = "bob"
	c3 := ports.ReviewComment{ID: 33, Path: "lib/b.go", Line: 9, Body: "rename", Type: ports.CommentTypeReview, DiffHunk: "@@ -1 +1 @@\n-old\n+new"}
	c3.User.Login = "carol"

	plan := BuildAggregatedReviewCommentsPlan([]ReviewCommentsRepoTarget{
		{RepoName: "web", PRURL: "https://github.com/o/web/pull/2", Mode: "auto", Comments: []ports.ReviewComment{c1, c2}},
		{RepoName: "api", PRURL: "https://github.com/o/api/pull/1", Mode: "auto", Comments: []ports.ReviewComment{c3}},
	}, "/state/runs/run-001/review-comments-1/review-resolutions.json")

	for _, want := range []string{
		"## Repo: `api`",
		"## Repo: `web`",
		"`api` — PR https://github.com/o/api/pull/1 — 1 comment",
		"`web` — PR https://github.com/o/web/pull/2 — 2 comment",
		"ID: 11)",
		"ID: 22)",
		"ID: 33)",
		"`repo: api`",
		"`repo: web`",
		"/state/runs/run-001/review-comments-1/review-resolutions.json",
		"3 unaddressed PR review comment",
		"2 repo",
	} {
		if !strings.Contains(plan, want) {
			t.Errorf("plan missing %q", want)
		}
	}
	// Sorted output: api before web.
	if idxAPI, idxWeb := strings.Index(plan, "## Repo: `api`"), strings.Index(plan, "## Repo: `web`"); idxAPI >= 0 && idxWeb >= 0 && idxAPI > idxWeb {
		t.Errorf("api section should appear before web; api=%d web=%d", idxAPI, idxWeb)
	}
}

// TestBuildAggregatedReviewCommentsPlan_Empty covers the no-comments
// composer call.
func TestBuildAggregatedReviewCommentsPlan_Empty(t *testing.T) {
	plan := BuildAggregatedReviewCommentsPlan(nil, "/tmp/r.json")
	if !strings.Contains(plan, "0 unaddressed PR review comment") {
		t.Errorf("plan missing zero-comment marker: %s", plan)
	}
}
