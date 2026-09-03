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
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// TestOrchestrator_HandlePhaseCompletion_MultiRepo_NeedUserInput_PausesFeature
// is the regression test for the slice-2 NUI persistence gap. Under the
// unified flow the gate is feature-scoped (Feature.PendingNeedUserInputPath
// + Feature.Status == StatusNeedUserInput); the orchestrator's NUI branch
// in onMultiRepoImplementDone has to write both. This test drives the full
// regression path:
//
//  1. The unified loop returned FinalStatus="need_user_input" with a gate
//     path. HandlePhaseCompletion routes to onMultiRepoImplementDone's
//     "need_user_input" branch.
//  2. Verify the orchestrator persisted f.PendingNeedUserInputPath and
//     transitioned f.Status to StatusNeedUserInput. Without this, the
//     resume dispatcher refuses with "feature is not paused on a
//     need-user-input gate (status=Implementing)".
//  3. Verify NeedUserInputRequired fires; PhaseCompleted does NOT (the
//     phase is paused, not done).
//  4. Drive ResumeNeedUserInput and verify it does NOT return the
//     "is not paused" sentinel — i.e. the dispatcher recognises the
//     feature as paused. (We don't drive the dispatcher all the way
//     through startPhase here; the bug under test was upstream of that.)
func TestOrchestrator_HandlePhaseCompletion_MultiRepo_NeedUserInput_PausesFeature(t *testing.T) {
	gatePath := writeGateArtifact(t,
		"clarify the API contract",
		[]string{"Should the new endpoint be idempotent?"},
		[]string{""}, // unanswered while the user thinks
	)

	f := &feature.Feature{
		ID:           "feat-mr-nui",
		Status:       feature.StatusImplementing,
		CurrentPhase: feature.PhaseImplement,
		Pipeline:     feature.PipelineMedium,
		Repos: []feature.FeatureRepo{
			{Name: repoName}, {Name: repoNameB}, {Name: repoNameC},
		},
		RepoStates: map[string]*feature.RepoState{
			repoName:  {},
			repoNameB: {},
			repoNameC: {},
		},
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{})

	// --- 1. Loop returns need_user_input → orchestrator must pause feature. ---
	if err := o.HandlePhaseCompletion("feat-mr-nui", orchestrator.PhaseCompletionInput{
		Phase: feature.PhaseImplement,
		MultiRepoResult: &agent.OrchestratorResult{
			FinalStatus:       "need_user_input",
			NeedUserInputPath: gatePath,
			PausedRepos:       []string{repoName, repoNameB, repoNameC},
			LastError:         "implementation needs user input",
		},
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	// 2. Feature must be persisted as paused: gate path written + status
	// transitioned. This is the slice-2 fix — without it the resume
	// dispatcher rejects with "is not paused".
	if f.PendingNeedUserInputPath != gatePath {
		t.Errorf("Feature.PendingNeedUserInputPath = %q, want %q", f.PendingNeedUserInputPath, gatePath)
	}
	if f.Status != feature.StatusNeedUserInput {
		t.Errorf("Feature.Status = %q, want StatusNeedUserInput (resume dispatcher gates on this)", f.Status)
	}
	// Per-repo state untouched: gate is feature-scoped.
	for _, name := range []string{repoName, repoNameB, repoNameC} {
		if st := f.RepoStates[name]; st != nil && (st.Touched || st.Error != nil) {
			t.Errorf("RepoStates[%q] = %+v, want untouched (feature-scoped gate must not mutate per-repo state)", name, st)
		}
	}

	// 3. Event invariants: NeedUserInputRequired fires, PhaseCompleted does NOT.
	events := drainEvents(o)
	if !hasEventType(events, ports.NeedUserInputRequired) {
		t.Error("expected NeedUserInputRequired event")
	}
	if hasEventType(events, ports.PhaseCompleted) {
		t.Error("PhaseCompleted MUST NOT fire on a paused phase; the phase is paused, not done")
	}
	if hasEventType(events, ports.FeatureFailed) {
		t.Error("FeatureFailed MUST NOT fire on a paused phase")
	}

	// --- 4. Resume dispatcher recognises the feature as paused. ---
	// The user attempts to resume without answering — the gate's own
	// AllAnswered guard rejects, but crucially NOT with the
	// "is not paused" sentinel that fired before the fix.
	err := o.ResumeNeedUserInput("feat-mr-nui", orchestrator.NeedUserInputResume{})
	if err == nil {
		t.Fatal("expected resume to error on unanswered question")
	}
	if strings.Contains(err.Error(), "is not paused on a need-user-input gate") {
		t.Errorf("dispatcher rejected with stale 'is not paused' sentinel — orchestrator failed to persist gate state: %v", err)
	}
	if !strings.Contains(err.Error(), "every question must have a non-empty answer") {
		t.Errorf("expected unanswered-question diagnostic; got: %v", err)
	}
}
