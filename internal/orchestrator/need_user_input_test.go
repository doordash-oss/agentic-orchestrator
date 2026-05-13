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
		Repos:        []feature.FeatureRepo{{Name: "repo-a", Path: "/tmp/repo-a"}},
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
	// is treated as a stale hint (the TUI's repo-tab focus context) and
	// routed to the feature-level handler, which validates against
	// Feature.Status / Feature.PendingNeedUserInputPath. Here the feature is
	// StatusImplementing (not paused), so the feature-level handler rejects
	// with its own diagnostic — never the legacy repo-scoped sentinel.
	f := &feature.Feature{
		ID:         "feat-mr-nui-no-repo",
		Status:     feature.StatusImplementing,
		Repos:      []feature.FeatureRepo{{Name: "repo-a"}},
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
