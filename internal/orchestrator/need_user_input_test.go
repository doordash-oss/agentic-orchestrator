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
)

// writeGateArtifact persists a NeedUserInputRecord under iter/need-user-input.yaml
// and returns the absolute path. Helper for the gate-resume tests.
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

func writeHarnessVerificationGate(t *testing.T, stateRoot, featureID, summary, answer string) string {
	t.Helper()
	contractPath := filepath.Join(stateRoot, featureID, "testing-contract.yaml")
	contract := agent.TestingContract{
		Version:  1,
		Revision: 1,
		Items: []agent.TestingContractItem{{
			ID: "capability-blocked",
			Policy: agent.TestingContractItemPolicy{
				Required:    true,
				AllowWaiver: true,
			},
		}},
	}
	if err := agent.WriteTestingContract(contractPath, contract); err != nil {
		t.Fatalf("write testing contract: %v", err)
	}
	rec := agent.SynthesizeVerificationNeedUserInputGate(contractPath, 1, []string{"capability-blocked"}, 2)
	rec.Summary = summary
	rec.Questions[0].Answer = answer
	path := filepath.Join(stateRoot, featureID, "iteration-02", agent.NeedUserInputArtifactName)
	if err := agent.WriteNeedUserInputRecord(path, rec); err != nil {
		t.Fatalf("write harness gate: %v", err)
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

func TestResumeNeedUserInput_StaleRepoNameRoutesToFeatureScope(t *testing.T) {
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

	err := o.ResumeNeedUserInput("feat-mr-nui-no-repo", orchestrator.NeedUserInputResume{})
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

func TestResumeNeedUserInput_GenericGateResumesWithoutVerificationDecision(t *testing.T) {
	stateRoot := t.TempDir()
	gatePath := writeGateArtifact(
		t,
		"Implementation needs a deployment window.",
		[]string{"Deployment window?"},
		[]string{"Tomorrow morning."},
	)
	f := &feature.Feature{
		ID: "feat-generic-gate", Name: "Generic gate", Slug: "generic-gate",
		SchemaVersion: feature.SchemaVersionCurrent,
		Status:        feature.StatusNeedUserInput, CurrentPhase: feature.PhaseImplement,
		PendingNeedUserInputPath: gatePath,
		Repos:                    []feature.FeatureRepo{{Name: repoName, Path: repoAPath}},
	}
	store := feature.NewStore(stateRoot)
	if err := store.Save(f); err != nil {
		t.Fatal(err)
	}
	lc := lifecycleForFeature(f)
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: store}, orchestrator.Hooks{})

	err := o.ResumeNeedUserInput(f.ID, orchestrator.NeedUserInputResume{})
	if err == nil || !strings.Contains(err.Error(), "plan phase did not produce an artifact") {
		t.Fatalf("ResumeNeedUserInput() error = %v, want expected post-resume dispatch failure", err)
	}
	if strings.Contains(err.Error(), "not a harness verification decision") {
		t.Fatalf("ResumeNeedUserInput() error = %v, generic gate must not require verification decision", err)
	}
}

func TestResumeNeedUserInputAppliesHarnessWaiverBeforeResume(t *testing.T) {
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
		SchemaVersion: feature.SchemaVersionCurrent, CurrentPhase: feature.PhaseImplement,
		PendingNeedUserInputPath: gatePath,
		Repos:                    []feature.FeatureRepo{{Name: repoName, Path: repoAPath}},
	}
	store := feature.NewStore(stateRoot)
	if err := store.Save(f); err != nil {
		t.Fatal(err)
	}
	lc := lifecycleForFeature(f)
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: store}, orchestrator.Hooks{})

	if err := o.ResumeNeedUserInput(f.ID, orchestrator.NeedUserInputResume{}); err == nil || !strings.Contains(err.Error(), "plan phase did not produce an artifact") {
		t.Fatalf("ResumeNeedUserInput() error = %v, want expected post-resume dispatch failure", err)
	}
	got, err := agent.ReadTestingContract(contractPath)
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != 3 || !agent.IsTestingContractItemWaived(got.Items[0]) {
		t.Fatalf("contract after resume = %+v, want durable revision-3 user waiver", got)
	}
}

func TestResumeNeedUserInputRejectsVerificationGateOutsideFeatureScope(t *testing.T) {
	stateRoot := t.TempDir()
	const featureID = "feat-gate-scope"
	contractPath := filepath.Join(stateRoot, featureID, "testing-contract.yaml")
	if err := agent.WriteTestingContract(contractPath, agent.TestingContract{
		Version: 1, Revision: 1,
		Items: []agent.TestingContractItem{{ID: "blocked"}},
	}); err != nil {
		t.Fatal(err)
	}
	rec := agent.SynthesizeVerificationNeedUserInputGate(contractPath, 1, []string{"blocked"}, 1)
	rec.Questions[0].Answer = agent.NeedUserVerificationRetryAfterAuth
	gatePath := filepath.Join(stateRoot, "another-feature", "iteration-01", agent.NeedUserInputArtifactName)
	if err := agent.WriteNeedUserInputRecord(gatePath, rec); err != nil {
		t.Fatal(err)
	}
	f := &feature.Feature{
		ID: featureID, Name: "Gate scope", Slug: "gate-scope", Status: feature.StatusNeedUserInput,
		CurrentPhase: feature.PhaseImplement, PendingNeedUserInputPath: gatePath,
		Repos: []feature.FeatureRepo{{Name: repoName, Path: repoAPath}},
	}
	store := feature.NewStore(stateRoot)
	if err := store.Save(f); err != nil {
		t.Fatal(err)
	}
	o := orchestrator.New(
		orchestrator.Deps{Lifecycle: lifecycleForFeature(f), Store: store},
		orchestrator.Hooks{},
	)

	err := o.ResumeNeedUserInput(featureID, orchestrator.NeedUserInputResume{})
	if err == nil || !strings.Contains(err.Error(), "gate") || !strings.Contains(err.Error(), "not scoped") {
		t.Fatalf("ResumeNeedUserInput() error = %v, want off-feature gate rejection", err)
	}
}
