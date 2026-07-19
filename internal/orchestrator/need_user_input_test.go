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
	"path/filepath"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// writeGateArtifact persists a NeedUserInputRecord under iter/need-user-input.yaml
// and returns the absolute path. Helper for the gate-decision tests.
func writeGateArtifact(t *testing.T, summary string, prompts []string, answers []string) string {
	t.Helper()
	dir := t.TempDir()
	rec := agent.NeedUserInputRecord{Summary: summary, Iteration: 2}
	for i, p := range prompts {
		ans := ""
		if i < len(answers) {
			ans = answers[i]
		}
		rec.Questions = append(rec.Questions, agent.NeedUserInputQuestion{
			Index:  i + 1,
			Prompt: p,
			Answer: ans,
		})
	}
	path := filepath.Join(dir, "need-user-input.yaml")
	if err := agent.WriteNeedUserInputRecord(path, rec); err != nil {
		t.Fatalf("write gate: %v", err)
	}
	return path
}

// TestOrchestrator_HandlePhaseCompletion_NeedUserInput_PausesFeature locks
// in that an implement loop result with FinalStatus == "need_user_input"
// transitions the feature into StatusNeedUserInput, persists the gate
// path, and emits NeedUserInputRequired. PhaseCompleted MUST NOT fire —
// the implement phase is paused, not done.
func TestOrchestrator_HandlePhaseCompletion_NeedUserInput_PausesFeature(t *testing.T) {
	gatePath := writeGateArtifact(t, "Plan contradicts worktree.",
		[]string{"Use legacy or new auth?", "Skip migration?"}, nil)

	f := &feature.Feature{
		ID:           "feat-nui",
		Status:       feature.StatusImplementing,
		CurrentPhase: feature.PhaseImplement,
		Pipeline:     feature.PipelineMoonshot,
		Repos:        []feature.FeatureRepo{{Name: repoName, Path: repoAPath}},
	}
	lc := lifecycleForFeature(f)
	lc.ClearAddressingReviewsFn = func(id string) error { return nil }
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{})

	if err := o.HandlePhaseCompletion("feat-nui", orchestrator.PhaseCompletionInput{
		Phase: feature.PhaseImplement,
		ImplementResult: &agent.LoopResult{
			FinalStatus:       "need_user_input",
			Iterations:        2,
			LastError:         "Plan contradicts worktree.",
			NeedUserInputPath: gatePath,
		},
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	if f.Status != feature.StatusNeedUserInput {
		t.Errorf("Status = %v, want StatusNeedUserInput", f.Status)
	}
	if f.PendingNeedUserInputPath != gatePath {
		t.Errorf("PendingNeedUserInputPath = %q, want %q", f.PendingNeedUserInputPath, gatePath)
	}

	events := drainEvents(o)
	if !hasEventType(events, ports.NeedUserInputRequired) {
		t.Errorf("expected NeedUserInputRequired event; got %v", events)
	}
	if hasEventType(events, ports.PhaseCompleted) {
		t.Errorf("PhaseCompleted MUST NOT fire on a paused phase; got %v", events)
	}
	if hasEventType(events, ports.FeatureFailed) {
		t.Errorf("FeatureFailed MUST NOT fire on a paused phase; got %v", events)
	}
}

func TestHandleNeedUserInputDecision_StaleRepoNameRoutesToFeatureScope(t *testing.T) {
	// Under SchemaVersionCurrent = 4 phase-implement NEED_USER_INPUT is
	// feature-scoped. A non-empty RepoName that doesn't match a paused cycle
	// is treated as a stale presentation hint and
	// routed to the feature-level handler, which validates against
	// Feature.Status / Feature.PendingNeedUserInputPath. Here the feature is
	// StatusImplementing (not paused), so the feature-level handler rejects
	// with its own diagnostic — never the legacy repo-scoped sentinel.
	f := &feature.Feature{
		ID:         "feat-mr-nui-no-repo",
		Status:     feature.StatusImplementing,
		Repos:      []feature.FeatureRepo{{Name: repoName}},
		RepoStates: map[string]*feature.RepoState{},
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{})

	err := o.HandleNeedUserInputDecision("feat-mr-nui-no-repo",
		orchestrator.NeedUserInputDecision{Decision: "resume", RepoName: "ghost"})
	if err == nil {
		t.Fatal("expected error: feature is StatusImplementing, not paused on a gate")
	}
	if strings.Contains(err.Error(), "removed in SchemaVersionCurrent = 4") {
		t.Errorf("err = %q, must not return the legacy repo-scoped sentinel", err.Error())
	}
	if !strings.Contains(err.Error(), "is not paused on a need-user-input gate") {
		t.Errorf("err = %q, want feature-level handler diagnostic 'is not paused on a need-user-input gate'", err.Error())
	}
}

func TestHandleNeedUserInputDecisionAppliesHarnessWaiverBeforeResume(t *testing.T) {
	stateRoot := t.TempDir()
	contractPath := filepath.Join(stateRoot, "feat-waiver", "testing-contract.yaml")
	contract := agent.TestingContract{Version: 1, Revision: 2, Items: []agent.TestingContractItem{{
		ID: "protected", Policy: agent.TestingContractItemPolicy{Required: true, AllowWaiver: true},
	}}}
	if err := agent.WriteTestingContract(contractPath, contract); err != nil {
		t.Fatal(err)
	}
	rec := agent.SynthesizeVerificationNeedUserInputGate(contractPath, 2, []string{"protected"}, 1)
	rec.Questions[0].Answer = "WAIVE"
	gatePath := filepath.Join(stateRoot, "feat-waiver", "iteration-01", agent.NeedUserInputArtifactName)
	if err := agent.WriteNeedUserInputRecord(gatePath, rec); err != nil {
		t.Fatal(err)
	}
	f := &feature.Feature{
		ID: "feat-waiver", Name: "Waiver", Slug: "waiver", Status: feature.StatusNeedUserInput,
		CurrentPhase: feature.PhaseImplement, PendingNeedUserInputPath: gatePath,
		Repos: []feature.FeatureRepo{{Name: repoName, Path: repoAPath}},
	}
	store := feature.NewStore(stateRoot)
	if err := store.Save(f); err != nil {
		t.Fatal(err)
	}
	lc := lifecycleForFeature(f)
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: store}, orchestrator.Hooks{})

	if err := o.HandleNeedUserInputDecision(f.ID, orchestrator.NeedUserInputDecision{Decision: "resume"}); err == nil || !strings.Contains(err.Error(), "plan phase did not produce an artifact") {
		t.Fatalf("HandleNeedUserInputDecision() error = %v, want expected post-decision dispatch failure", err)
	}
	got, err := agent.ReadTestingContract(contractPath)
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != 3 || !agent.IsTestingContractItemWaived(got.Items[0]) {
		t.Fatalf("contract after resume = %+v, want durable revision-3 user waiver", got)
	}
}

// sharedGateFeature builds a two-repo feature paused on one shared
// review-comments NEED_USER_INPUT gate — both repos point at the same
// gate path — and wires a lifecycle/store pair that mirrors real
// FailRepoCycle side-effects.
func sharedGateFeature(t *testing.T) (*feature.Feature, *mocks.MockFeatureLifecycle, *featureStore, string) {
	t.Helper()
	gatePath := writeGateArtifact(t, "Shared gate needs answers.",
		[]string{"Apply suggestion?", "Keep tests?"}, []string{"yes", "yes"})

	f := &feature.Feature{
		ID:     "feat-shared-gate",
		Status: feature.StatusPublished,
		Repos: []feature.FeatureRepo{
			{Name: "repo-a", Path: "/tmp/repo-a"},
			{Name: "repo-b", Path: "/tmp/repo-b"},
		},
		RepoCycles: map[string]*feature.RepoCycleState{
			"repo-a": {
				Type:                     feature.CycleReviewComments,
				Status:                   feature.RepoCycleNeedUserInput,
				Count:                    1,
				Iteration:                2,
				PendingNeedUserInputPath: gatePath,
			},
			"repo-b": {
				Type:                     feature.CycleReviewComments,
				Status:                   feature.RepoCycleNeedUserInput,
				Count:                    1,
				Iteration:                2,
				PendingNeedUserInputPath: gatePath,
			},
		},
	}
	lc := lifecycleForFeature(f)
	lc.FailRepoCycleFn = func(_ string, repoName, errMsg string) error {
		_ = errMsg
		if rc, ok := f.RepoCycles[repoName]; ok && rc != nil {
			rc.Status = feature.RepoCycleFailed
			rc.PendingNeedUserInputPath = ""
		}
		return nil
	}
	fs := newFeatureStore(f)
	return f, lc, fs, gatePath
}

// TestHandleNeedUserInputDecision_SharedGateAbortFailsAllRepos verifies
// that aborting a shared multi-repo gate fails every repo that shares the
// gate, not just the repo named in the decision.
func TestHandleNeedUserInputDecision_SharedGateAbortFailsAllRepos(t *testing.T) {
	f, lc, fs, _ := sharedGateFeature(t)
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{})

	err := o.HandleNeedUserInputDecision("feat-shared-gate",
		orchestrator.NeedUserInputDecision{Decision: "abort", RepoName: "repo-a"})
	if err != nil {
		t.Fatalf("abort: %v", err)
	}

	// Both repos must be failed, not just repo-a.
	var failCalls []string
	for _, c := range lc.Calls {
		if c.Method == "FailRepoCycle" {
			if name, ok := c.Args[1].(string); ok {
				failCalls = append(failCalls, name)
			}
		}
	}
	if len(failCalls) != 2 {
		t.Fatalf("FailRepoCycle calls = %v, want 2 (repo-a and repo-b)", failCalls)
	}
	for _, name := range failCalls {
		if name != "repo-a" && name != "repo-b" {
			t.Errorf("FailRepoCycle called for unexpected repo %q", name)
		}
	}
	for _, repo := range []string{"repo-a", "repo-b"} {
		if rc := f.RepoCycles[repo]; rc != nil && rc.Status != feature.RepoCycleFailed {
			t.Errorf("RepoCycles[%s].Status = %q, want %q", repo, rc.Status, feature.RepoCycleFailed)
		}
	}
}

// TestHandleNeedUserInputDecision_SharedGateResumeClearsAllRepos verifies
// that resuming a shared multi-repo gate clears the gate on every repo
// that shares it. The test instruments Store.Modify to capture whether
// both repos were transitioned to Running before the restart failure
// rolls them back.
func TestHandleNeedUserInputDecision_SharedGateResumeClearsAllRepos(t *testing.T) {
	f, lc, fs, gatePath := sharedGateFeature(t)

	// Track whether each repo was ever set to Running during the resume
	// attempt (before rollback).
	sawRunning := map[string]bool{}
	origModify := fs.ModifyFn
	fs.ModifyFn = func(id string, fn func(ff *feature.Feature) error) error {
		err := origModify(id, fn)
		for name, rc := range f.RepoCycles {
			if rc != nil && rc.Status == feature.RepoCycleRunning {
				sawRunning[name] = true
			}
		}
		return err
	}

	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{})

	err := o.HandleNeedUserInputDecision("feat-shared-gate",
		orchestrator.NeedUserInputDecision{Decision: "resume", RepoName: "repo-a"})
	if err == nil {
		t.Fatal("expected restart error (no PhaseRunner), got nil")
	}

	// Both repos must have been cleared to Running during the resume
	// attempt — proving the shared gate was resolved for all participants.
	for _, repo := range []string{"repo-a", "repo-b"} {
		if !sawRunning[repo] {
			t.Errorf("RepoCycles[%s] was never set to Running during resume; shared gate must clear all participating repos", repo)
		}
	}

	// After failed restart + rollback, both repos must be back on the
	// paused gate.
	for _, repo := range []string{"repo-a", "repo-b"} {
		rc := f.RepoCycles[repo]
		if rc == nil {
			t.Fatalf("RepoCycles[%s] is nil after rollback", repo)
		}
		if rc.Status != feature.RepoCycleNeedUserInput {
			t.Errorf("RepoCycles[%s].Status = %q, want %q (rollback must restore gate on all sharing repos)",
				repo, rc.Status, feature.RepoCycleNeedUserInput)
		}
		if rc.PendingNeedUserInputPath != gatePath {
			t.Errorf("RepoCycles[%s].PendingNeedUserInputPath = %q, want %q", repo, rc.PendingNeedUserInputPath, gatePath)
		}
	}
}

// TestHandleNeedUserInputDecision_SingleRepoGateAbortLeavesSiblingUnchanged
// verifies that aborting a single-repo gate (only one repo shares the
// gate path) does not affect a sibling repo paused on a DIFFERENT gate.
func TestHandleNeedUserInputDecision_SingleRepoGateAbortLeavesSiblingUnchanged(t *testing.T) {
	gateA := writeGateArtifact(t, "Gate A", []string{"Q1?"}, []string{"yes"})
	gateB := writeGateArtifact(t, "Gate B", []string{"Q2?"}, []string{"no"})

	f := &feature.Feature{
		ID:     "feat-separate-gates",
		Status: feature.StatusPublished,
		Repos: []feature.FeatureRepo{
			{Name: "repo-a", Path: "/tmp/repo-a"},
			{Name: "repo-b", Path: "/tmp/repo-b"},
		},
		RepoCycles: map[string]*feature.RepoCycleState{
			"repo-a": {
				Type:                     feature.CycleReviewComments,
				Status:                   feature.RepoCycleNeedUserInput,
				Count:                    1,
				PendingNeedUserInputPath: gateA,
			},
			"repo-b": {
				Type:                     feature.CycleRefactor,
				Status:                   feature.RepoCycleNeedUserInput,
				Count:                    2,
				PendingNeedUserInputPath: gateB,
			},
		},
	}
	lc := lifecycleForFeature(f)
	lc.FailRepoCycleFn = func(_ string, repoName, _ string) error {
		if rc, ok := f.RepoCycles[repoName]; ok && rc != nil {
			rc.Status = feature.RepoCycleFailed
			rc.PendingNeedUserInputPath = ""
			if rc.Type == feature.CycleRefactor {
				f.RefactorPrompt = ""
			}
		}
		return nil
	}
	fs := newFeatureStore(f)
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{})

	err := o.HandleNeedUserInputDecision("feat-separate-gates",
		orchestrator.NeedUserInputDecision{Decision: "abort", RepoName: "repo-a"})
	if err != nil {
		t.Fatalf("abort: %v", err)
	}

	if rc := f.RepoCycles["repo-a"]; rc == nil || rc.Status != feature.RepoCycleFailed {
		t.Errorf("repo-a should be failed, got %+v", rc)
	}
	if rc := f.RepoCycles["repo-b"]; rc == nil || rc.Status != feature.RepoCycleNeedUserInput {
		t.Errorf("repo-b should remain paused on its separate gate, got %+v", rc)
	}
	if rc := f.RepoCycles["repo-b"]; rc != nil && rc.PendingNeedUserInputPath != gateB {
		t.Errorf("repo-b gate path changed: got %q, want %q", rc.PendingNeedUserInputPath, gateB)
	}
}
