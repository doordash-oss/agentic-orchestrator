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

package orchestrator

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

func TestHandleFeatureRebaseDone_PrePRCodeReadyResumesPublish(t *testing.T) {
	store := feature.NewStore(t.TempDir())
	cfg := config.NewDefault()
	mgr := feature.NewManager(store, cfg)

	const featureID = "feat-pre-pr-rebase"
	f := &feature.Feature{
		ID:            featureID,
		Name:          "Pre PR Rebase",
		Slug:          "pre-pr-rebase",
		Status:        feature.StatusCodeReady,
		SchemaVersion: feature.SchemaVersionCurrent,
		Repos: []feature.FeatureRepo{
			{Name: "agentic", Path: "/tmp/agentic", Branch: "feature/pre-pr-rebase"},
		},
		RepoStates: map[string]*feature.RepoState{
			"agentic": {Touched: true},
		},
		RepoCycles: map[string]*feature.RepoCycleState{
			"agentic": {Type: feature.CycleRebase, Status: feature.RepoCycleRunning, Count: 1},
		},
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}

	o := New(Deps{Lifecycle: mgr, Store: store}, Hooks{})
	var publishCalls int
	o.SetPublishFn(func(id string) error {
		publishCalls++
		if id != featureID {
			t.Fatalf("publish id = %q, want %q", id, featureID)
		}
		return nil
	})

	o.handleFeatureRebaseDone(featureID,
		[]agent.RebaseRepoTarget{{RepoName: "agentic", RebaseTarget: "master"}},
		&agent.RebaseLoopResult{FinalStatus: "review_passed", Repos: []string{"agentic"}},
		nil,
	)

	if publishCalls != 1 {
		t.Fatalf("publish calls = %d, want 1", publishCalls)
	}
	got, err := mgr.Get(featureID)
	if err != nil {
		t.Fatalf("load feature: %v", err)
	}
	if got.RepoCycles != nil {
		t.Fatalf("RepoCycles = %+v, want cleared before publish resume", got.RepoCycles)
	}
}

func TestHandleFeatureRebaseDone_ManualPublishDoesNotResumePublish(t *testing.T) {
	store := feature.NewStore(t.TempDir())
	cfg := config.NewDefault()
	mgr := feature.NewManager(store, cfg)

	const featureID = "feat-manual-publish-rebase"
	f := &feature.Feature{
		ID:            featureID,
		Name:          "Manual Publish Rebase",
		Slug:          "manual-publish-rebase",
		Status:        feature.StatusCodeReady,
		SchemaVersion: feature.SchemaVersionCurrent,
		Checkpoints:   feature.Checkpoints{ManualPublish: true},
		Repos: []feature.FeatureRepo{
			{Name: "agentic", Path: "/tmp/agentic", Branch: "feature/manual-publish-rebase"},
		},
		RepoStates: map[string]*feature.RepoState{
			"agentic": {Touched: true},
		},
		RepoCycles: map[string]*feature.RepoCycleState{
			"agentic": {Type: feature.CycleRebase, Status: feature.RepoCycleRunning, Count: 1},
		},
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}

	o := New(Deps{Lifecycle: mgr, Store: store}, Hooks{})
	o.SetPublishFn(func(id string) error {
		t.Fatalf("publish should not resume for manual publish feature %q", id)
		return nil
	})

	o.handleFeatureRebaseDone(featureID,
		[]agent.RebaseRepoTarget{{RepoName: "agentic", RebaseTarget: "master"}},
		&agent.RebaseLoopResult{FinalStatus: "review_passed", Repos: []string{"agentic"}},
		nil,
	)
}

func TestResolveBehindSubset_SkipsUpToDateHintWithoutConflictFiles(t *testing.T) {
	rebaser := mocks.NewMockRebaseOperator()
	rebaser.IsBehindRemoteFn = func(string, string) (bool, error) {
		return false, nil
	}
	o := New(Deps{Rebaser: rebaser}, Hooks{})

	f := &feature.Feature{
		SchemaVersion: feature.SchemaVersionCurrent,
		Repos: []feature.FeatureRepo{
			{
				Name:         "agentic",
				Path:         "/tmp/agentic",
				WorktreePath: "/tmp/agentic",
				Branch:       "feature/up-to-date",
				BaseBranch:   "main",
			},
		},
	}

	got := o.resolveBehindSubset(f, "agentic", "", nil)

	if len(got) != 0 {
		t.Fatalf("resolveBehindSubset returned %v, want no targets for an up-to-date non-conflict hint", got)
	}
}

func TestHandleFeatureRebaseDone_NeedUserInputPausesCycle(t *testing.T) {
	store := feature.NewStore(t.TempDir())
	cfg := config.NewDefault()
	mgr := feature.NewManager(store, cfg)
	gatePath := filepath.Join(t.TempDir(), "need-user-input.yaml")

	const featureID = "feat-rebase-nui"
	f := &feature.Feature{
		ID:            featureID,
		Name:          "Rebase Need User Input",
		Slug:          "rebase-need-user-input",
		Status:        feature.StatusPublished,
		SchemaVersion: feature.SchemaVersionCurrent,
		Repos: []feature.FeatureRepo{
			{Name: "agentic", Path: "/tmp/agentic", Branch: "feature/rebase-need-user-input"},
		},
		RepoStates: map[string]*feature.RepoState{
			"agentic": {Touched: true, PRURL: "https://github.com/example/agentic/pull/1"},
		},
		RepoCycles: map[string]*feature.RepoCycleState{
			"agentic": {Type: feature.CycleRebase, Status: feature.RepoCycleRunning, Count: 1},
		},
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}

	o := New(Deps{Lifecycle: mgr, Store: store}, Hooks{})
	o.handleFeatureRebaseDone(featureID,
		[]agent.RebaseRepoTarget{{RepoName: "agentic", RebaseTarget: "master"}},
		&agent.RebaseLoopResult{
			FinalStatus:       "need_user_input",
			LastError:         "Build gate needs a human decision.",
			NeedUserInputPath: gatePath,
			Repos:             []string{"agentic"},
		},
		nil,
	)

	got, err := mgr.Get(featureID)
	if err != nil {
		t.Fatalf("load feature: %v", err)
	}
	rc := got.RepoCycles["agentic"]
	if rc == nil {
		t.Fatal("RepoCycles[agentic] missing")
	}
	if rc.Status != feature.RepoCycleNeedUserInput {
		t.Fatalf("RepoCycles[agentic].Status = %q, want %q", rc.Status, feature.RepoCycleNeedUserInput)
	}
	if rc.PendingNeedUserInputPath != gatePath {
		t.Errorf("PendingNeedUserInputPath = %q, want %q", rc.PendingNeedUserInputPath, gatePath)
	}
	if rc.LastError != "Build gate needs a human decision." {
		t.Errorf("LastError = %q", rc.LastError)
	}
	if st := got.RepoStates["agentic"]; st == nil || st.LastError != "" {
		t.Errorf("RepoStates[agentic] = %+v, want no failure", st)
	}
}

func TestHandleFeatureRebaseDone_NeedUserInputClearFeatureGateErrorFailsCycle(t *testing.T) {
	const featureID = "feat-rebase-nui-clear-fails"
	gatePath := filepath.Join(t.TempDir(), "need-user-input.yaml")

	store := mocks.NewMockFeatureStore()
	modifyCalls := 0
	store.ModifyFn = func(_ string, fn func(*feature.Feature) error) error {
		modifyCalls++
		if modifyCalls == 1 {
			return fn(&feature.Feature{
				RepoCycles: map[string]*feature.RepoCycleState{
					"agentic": {Type: feature.CycleRebase, Status: feature.RepoCycleRunning, Count: 1},
				},
				PendingNeedUserInputPath: gatePath,
			})
		}
		return errors.New("store write failed")
	}
	lifecycle := mocks.NewMockFeatureLifecycle()

	o := New(Deps{Lifecycle: lifecycle, Store: store}, Hooks{})
	o.handleFeatureRebaseDone(featureID,
		[]agent.RebaseRepoTarget{{RepoName: "agentic", RebaseTarget: "main"}},
		&agent.RebaseLoopResult{
			FinalStatus:       "need_user_input",
			Iterations:        1,
			LastError:         "Build gate needs a human decision.",
			NeedUserInputPath: gatePath,
			Repos:             []string{"agentic"},
		},
		nil,
	)

	var failCall *mocks.MockCall
	for i := range lifecycle.Calls {
		if lifecycle.Calls[i].Method == "FailRepoCycle" {
			failCall = &lifecycle.Calls[i]
			break
		}
	}
	if failCall == nil {
		t.Fatal("FailRepoCycle was not called")
	}
	if got := failCall.Args[1]; got != "agentic" {
		t.Fatalf("FailRepoCycle repo = %v, want agentic", got)
	}
	msg, _ := failCall.Args[2].(string)
	if !strings.Contains(msg, "rebase: clear stale feature-level need-user-input gate") ||
		!strings.Contains(msg, "store write failed") {
		t.Fatalf("FailRepoCycle message = %q, want stale-gate persistence error context", msg)
	}
}

func TestHandleNeedUserInputDecision_RebaseResumeRestartsUnifiedLoop(t *testing.T) {
	stateDir := t.TempDir()
	store := feature.NewStore(stateDir)
	mgr := feature.NewManager(store, config.NewDefault())
	gatePath := writeAnsweredRebaseGate(t, "Resolve generated-file policy.", "Regenerate or preserve generated files?")

	const featureID = "feat-rebase-nui-resume"
	apiDir := t.TempDir()
	webDir := t.TempDir()
	f := &feature.Feature{
		ID:            featureID,
		Name:          "Rebase Need User Input Resume",
		Slug:          "rebase-need-user-input-resume",
		Status:        feature.StatusPublished,
		SchemaVersion: feature.SchemaVersionCurrent,
		MaxIterations: 4,
		Repos: []feature.FeatureRepo{
			{Name: "api", Path: apiDir, WorktreePath: apiDir, Branch: "feature/rebase-nui", BaseBranch: "main"},
			{Name: "web", Path: webDir, WorktreePath: webDir, Branch: "feature/rebase-nui", BaseBranch: "main"},
		},
		RepoStates: map[string]*feature.RepoState{
			"api": {Touched: true, PRURL: "https://github.com/example/api/pull/1"},
			"web": {Touched: true, PRURL: "https://github.com/example/web/pull/2"},
		},
		RepoCycles: map[string]*feature.RepoCycleState{
			"api": {Type: feature.CycleRebase, Status: feature.RepoCycleRunning, Count: 1},
			"web": {Type: feature.CycleRebase, Status: feature.RepoCycleRunning, Count: 1},
		},
		ActiveCycle:              &feature.CycleState{Type: feature.CycleRebase, Status: feature.RepoCycleRunning, Count: 1},
		PendingNeedUserInputPath: gatePath,
	}
	f.SetRebaseCount(1)
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}

	o := New(Deps{
		Lifecycle:   mgr,
		Store:       store,
		PhaseRunner: agent.NewPhaseRunner(nil, store, stateDir),
	}, Hooks{})

	behind := []agent.RebaseRepoTarget{
		{RepoName: "api", RebaseTarget: "main"},
		{RepoName: "web", RebaseTarget: "main"},
	}
	o.handleFeatureRebaseDone(featureID, behind, &agent.RebaseLoopResult{
		FinalStatus:       "need_user_input",
		Iterations:        1,
		LastError:         "Resolve generated-file policy.",
		NeedUserInputPath: gatePath,
		Repos:             []string{"api", "web"},
	}, nil)

	paused, err := mgr.Get(featureID)
	if err != nil {
		t.Fatalf("load paused feature: %v", err)
	}
	if paused.Status != feature.StatusPublished {
		t.Fatalf("Status = %q, want published", paused.Status)
	}
	if paused.PendingNeedUserInputPath != "" {
		t.Fatalf("feature-level PendingNeedUserInputPath = %q, want cleared for cycle-scoped gate", paused.PendingNeedUserInputPath)
	}
	for _, name := range []string{"api", "web"} {
		rc := paused.RepoCycles[name]
		if rc == nil {
			t.Fatalf("RepoCycles[%s] missing", name)
		}
		if rc.Status != feature.RepoCycleNeedUserInput {
			t.Fatalf("RepoCycles[%s].Status = %q, want need_user_input", name, rc.Status)
		}
		if rc.PendingNeedUserInputPath != gatePath {
			t.Fatalf("RepoCycles[%s].PendingNeedUserInputPath = %q, want %q", name, rc.PendingNeedUserInputPath, gatePath)
		}
	}

	captured := make(chan agent.RebaseLoopConfig, 1)
	o.runRebaseLoopFn = func(cfg agent.RebaseLoopConfig, _ ports.SessionManager) (*agent.RebaseLoopResult, error) {
		cfgCopy := cfg
		cfgCopy.BehindRepos = append([]agent.RebaseRepoTarget(nil), cfg.BehindRepos...)
		if cfg.Feature != nil {
			featureCopy := *cfg.Feature
			featureCopy.RepoCycles = make(map[string]*feature.RepoCycleState, len(cfg.Feature.RepoCycles))
			for name, rc := range cfg.Feature.RepoCycles {
				if rc == nil {
					continue
				}
				rcCopy := *rc
				featureCopy.RepoCycles[name] = &rcCopy
			}
			cfgCopy.Feature = &featureCopy
		}
		captured <- cfgCopy
		return &agent.RebaseLoopResult{
			FinalStatus: "review_passed",
			Repos:       []string{"api", "web"},
		}, nil
	}

	if err := o.HandleNeedUserInputDecision(featureID, NeedUserInputDecision{
		Decision:  "resume",
		RepoName:  "api",
		CycleType: feature.CycleRebase,
	}); err != nil {
		t.Fatalf("HandleNeedUserInputDecision resume: %v", err)
	}
	o.WaitForCycles()

	var cfg agent.RebaseLoopConfig
	select {
	case cfg = <-captured:
	default:
		t.Fatal("rebase loop was not relaunched")
	}
	if !cfg.ResumeExistingCycle {
		t.Fatal("ResumeExistingCycle = false, want true")
	}
	if cfg.Feature == nil || cfg.Feature.RebaseCount() != 1 {
		t.Fatalf("captured feature RebaseCount = %v, want 1", cfg.Feature)
	}
	if got := rebaseTargetNames(cfg.BehindRepos); !reflect.DeepEqual(got, []string{"api", "web"}) {
		t.Fatalf("BehindRepos = %v, want [api web]", got)
	}
	for _, name := range []string{"api", "web"} {
		rc := cfg.Feature.RepoCycles[name]
		if rc == nil {
			t.Fatalf("captured RepoCycles[%s] missing", name)
		}
		if rc.Status != feature.RepoCycleRunning {
			t.Fatalf("captured RepoCycles[%s].Status = %q, want running", name, rc.Status)
		}
		if rc.PendingNeedUserInputPath != "" {
			t.Fatalf("captured RepoCycles[%s].PendingNeedUserInputPath = %q, want cleared", name, rc.PendingNeedUserInputPath)
		}
	}

	done, err := mgr.Get(featureID)
	if err != nil {
		t.Fatalf("load resumed feature: %v", err)
	}
	if done.PendingNeedUserInputPath != "" {
		t.Fatalf("feature-level PendingNeedUserInputPath after resume = %q, want cleared", done.PendingNeedUserInputPath)
	}
	if len(done.RepoCycles) != 0 {
		t.Fatalf("RepoCycles after successful resumed rebase = %+v, want cleared", done.RepoCycles)
	}
}

func TestHandleNeedUserInputDecision_RebaseAbortFailsSharedGateCycles(t *testing.T) {
	stateDir := t.TempDir()
	store := feature.NewStore(stateDir)
	mgr := feature.NewManager(store, config.NewDefault())
	gatePath := writeAnsweredRebaseGate(t, "User rejected generated-file policy.", "Regenerate or preserve generated files?")

	const featureID = "feat-rebase-nui-abort"
	f := &feature.Feature{
		ID:                       featureID,
		Name:                     "Rebase Need User Input Abort",
		Slug:                     "rebase-need-user-input-abort",
		Status:                   feature.StatusPublished,
		SchemaVersion:            feature.SchemaVersionCurrent,
		PendingNeedUserInputPath: gatePath,
		Repos: []feature.FeatureRepo{
			{Name: "api", Path: t.TempDir(), BaseBranch: "main"},
			{Name: "web", Path: t.TempDir(), BaseBranch: "main"},
		},
		RepoCycles: map[string]*feature.RepoCycleState{
			"api": {
				Type:                     feature.CycleRebase,
				Status:                   feature.RepoCycleNeedUserInput,
				Count:                    1,
				PendingNeedUserInputPath: gatePath,
			},
			"web": {
				Type:                     feature.CycleRebase,
				Status:                   feature.RepoCycleNeedUserInput,
				Count:                    1,
				PendingNeedUserInputPath: gatePath,
			},
		},
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}

	o := New(Deps{Lifecycle: mgr, Store: store}, Hooks{})
	if err := o.HandleNeedUserInputDecision(featureID, NeedUserInputDecision{
		Decision:  "abort",
		RepoName:  "api",
		CycleType: feature.CycleRebase,
	}); err != nil {
		t.Fatalf("HandleNeedUserInputDecision abort: %v", err)
	}

	got, err := mgr.Get(featureID)
	if err != nil {
		t.Fatalf("load feature: %v", err)
	}
	if got.Status != feature.StatusPublished {
		t.Fatalf("Status = %q, want published", got.Status)
	}
	if got.PendingNeedUserInputPath != "" {
		t.Fatalf("feature-level PendingNeedUserInputPath = %q, want cleared", got.PendingNeedUserInputPath)
	}
	for _, name := range []string{"api", "web"} {
		rc := got.RepoCycles[name]
		if rc == nil {
			t.Fatalf("RepoCycles[%s] missing", name)
		}
		if rc.Status != feature.RepoCycleFailed {
			t.Fatalf("RepoCycles[%s].Status = %q, want failed", name, rc.Status)
		}
		if rc.PendingNeedUserInputPath != "" {
			t.Fatalf("RepoCycles[%s].PendingNeedUserInputPath = %q, want cleared", name, rc.PendingNeedUserInputPath)
		}
		if rc.LastError != "User rejected generated-file policy." {
			t.Fatalf("RepoCycles[%s].LastError = %q", name, rc.LastError)
		}
	}
}

func writeAnsweredRebaseGate(t *testing.T, summary string, prompts ...string) string {
	t.Helper()
	rec := agent.NeedUserInputRecord{Summary: summary, Iteration: 1}
	for i, prompt := range prompts {
		rec.Questions = append(rec.Questions, agent.NeedUserInputQuestion{
			Index:  i + 1,
			Prompt: prompt,
			Answer: "use the generated-file policy from the feature plan",
		})
	}
	path := filepath.Join(t.TempDir(), agent.NeedUserInputArtifactName)
	if err := agent.WriteNeedUserInputRecord(path, rec); err != nil {
		t.Fatalf("write gate artifact: %v", err)
	}
	return path
}

func rebaseTargetNames(targets []agent.RebaseRepoTarget) []string {
	names := make([]string, 0, len(targets))
	for _, target := range targets {
		names = append(names, target.RepoName)
	}
	return names
}
