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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

func TestArtifactPhaseCompletionValidationInfraErrorBubbles(t *testing.T) {
	oldValidate := validateAgentContract
	validateAgentContract = func(feature.Phase, agent.Role, string) (agent.Outcome, []agent.ProtocolViolation, error) {
		return agent.Outcome{}, nil, errors.New("parser exploded")
	}
	t.Cleanup(func() {
		validateAgentContract = oldValidate
	})

	stateDir := t.TempDir()
	f := &feature.Feature{
		ID:            "feat-infra-error",
		Status:        feature.StatusInquiring,
		CurrentPhase:  feature.PhaseInquire,
		Pipeline:      feature.PipelineLarge,
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	phaseDir := filepath.Join(agent.ActiveRunDir(stateDir, f), "inquire")
	if err := os.MkdirAll(phaseDir, 0o755); err != nil {
		t.Fatalf("mkdir phase dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(phaseDir, agent.PhaseCompleteFile), nil, 0o644); err != nil {
		t.Fatalf("write phase_complete: %v", err)
	}

	store := feature.NewStore(stateDir)
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}
	lc := mocks.NewMockFeatureLifecycle()
	lc.GetFn = func(id string) (*feature.Feature, error) {
		return store.Load(id)
	}
	lc.CompleteInquireFn = func(id string) error {
		return store.Modify(id, func(ff *feature.Feature) error {
			ff.Status = feature.StatusInquireReady
			return nil
		})
	}
	lc.MarkFailedFn = func(id, ft, msg string) error {
		return store.Modify(id, func(ff *feature.Feature) error {
			ff.Status = feature.StatusFailed
			ff.FailureType = ft
			ff.LastError = msg
			return nil
		})
	}

	o := New(Deps{
		Lifecycle:   lc,
		Store:       store,
		PhaseRunner: &agent.PhaseRunner{StateDir: stateDir},
	}, Hooks{})

	err := o.onArtifactPhaseCompleted(f.ID, PhaseCompletionInput{
		Phase:   feature.PhaseInquire,
		Success: true,
	}, "inquire", lc.CompleteInquire)
	if err == nil {
		t.Fatal("onArtifactPhaseCompleted() error = nil, want validation error")
	}
	if !strings.Contains(err.Error(), "validating inquire contract") || !strings.Contains(err.Error(), "parser exploded") {
		t.Fatalf("onArtifactPhaseCompleted() error = %v, want wrapped validation error", err)
	}

	reloaded, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("load feature: %v", err)
	}
	if reloaded.FailureType == feature.FailureProtocolViolation {
		t.Fatalf("FailureType = %q, want no protocol violation marking", reloaded.FailureType)
	}
	if _, err := os.Stat(filepath.Join(phaseDir, agent.ProtocolRetrySidecarFile)); !os.IsNotExist(err) {
		t.Fatalf("sidecar stat err = %v, want not exist", err)
	}
	for _, call := range lc.Calls {
		if call.Method == "MarkFailed" {
			t.Fatalf("MarkFailed called on infra validation error; calls=%#v", lc.Calls)
		}
	}
	select {
	case ev := <-o.Events():
		if ev.Type == ports.PhaseCompleted {
			t.Fatalf("PhaseCompleted emitted on infra validation error: %#v", ev)
		}
	default:
	}
}
