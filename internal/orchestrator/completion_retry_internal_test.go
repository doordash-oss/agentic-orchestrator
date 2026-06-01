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
	"time"

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

func TestKBLoopMissingIndexFailsWithoutSidecar(t *testing.T) {
	stateDir := t.TempDir()
	kbDir := agent.KBStateDir(stateDir, "repo-a")
	if err := os.MkdirAll(kbDir, 0o755); err != nil {
		t.Fatalf("mkdir kb dir: %v", err)
	}

	f := &feature.Feature{
		ID:            "feat-infra",
		Status:        feature.StatusBuildingKB,
		CurrentPhase:  feature.PhaseKnowledgeBase,
		Pipeline:      feature.PipelineLarge,
		ActiveRun:     1,
		SchemaVersion: feature.SchemaVersionCurrent,
		Repos:         []feature.FeatureRepo{{Name: "repo-a", Path: "/tmp/repo-a"}},
	}
	store := feature.NewStore(stateDir)
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}
	lc := mocks.NewMockFeatureLifecycle()
	lc.GetFn = store.Load
	lc.MarkRepoKBFailedFn = func(id, repo, msg string) error {
		return store.Modify(id, func(ff *feature.Feature) error {
			if ff.KBStatus == nil {
				ff.KBStatus = map[string]string{}
			}
			ff.KBStatus[repo] = "failed: " + msg
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
	sm := mocks.NewMockSessionManager()
	o := New(Deps{
		Lifecycle: lc,
		Store:     store,
		Sessions:  sm,
		PhaseRunner: &agent.PhaseRunner{
			StateDir: stateDir,
		},
	}, Hooks{})

	if err := o.onKnowledgeBaseLoopDone(f.ID, "repo-a", &agent.BlockingLoopResult{
		FinalStatus: agent.BlockingLoopStatusSuccess,
	}); err != nil {
		t.Fatalf("onKnowledgeBaseLoopDone() error = %v", err)
	}
	reloaded, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("load feature: %v", err)
	}
	if reloaded.FailureType != feature.FailureProtocolViolation {
		t.Fatalf("FailureType = %q, want %q", reloaded.FailureType, feature.FailureProtocolViolation)
	}
	if !strings.Contains(reloaded.LastError, "index.md is missing") {
		t.Fatalf("LastError = %q, want missing index.md", reloaded.LastError)
	}
	if matches, err := filepath.Glob(filepath.Join(kbDir, ".protocol-retry-*.yaml")); err != nil || len(matches) != 0 {
		t.Fatalf("KB protocol retry sidecars = %v, %v; want none", matches, err)
	}
	if got := len(sm.StopCalls); got != 0 {
		t.Fatalf("StopSession calls = %d, want 0", got)
	}
}

func TestKBLoopProtocolViolationPreservesIndexAndState(t *testing.T) {
	stateDir := t.TempDir()
	kbDir := agent.KBStateDir(stateDir, "repo-a")
	if err := os.MkdirAll(kbDir, 0o755); err != nil {
		t.Fatalf("mkdir kb dir: %v", err)
	}
	indexPath := agent.KBPath(kbDir)
	indexBytes := []byte("# existing KB\n\ncontent\n")
	if err := os.WriteFile(indexPath, indexBytes, 0o644); err != nil {
		t.Fatalf("write index.md: %v", err)
	}
	if err := agent.SaveKBState(kbDir, &agent.KBState{
		HeadCommit:  "old-head",
		LastUpdated: time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC),
		Version:     1,
	}); err != nil {
		t.Fatalf("SaveKBState() error = %v", err)
	}
	statePath := filepath.Join(kbDir, "state.json")
	stateBytes, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(kbDir, agent.PhaseCompleteFile), nil, 0o644); err != nil {
		t.Fatalf("write phase_complete: %v", err)
	}

	f := &feature.Feature{
		ID:            "feat-preserve",
		Status:        feature.StatusBuildingKB,
		CurrentPhase:  feature.PhaseKnowledgeBase,
		Pipeline:      feature.PipelineLarge,
		ActiveRun:     1,
		SchemaVersion: feature.SchemaVersionCurrent,
		Repos:         []feature.FeatureRepo{{Name: "repo-a", Path: "/tmp/repo-a"}},
	}
	store := feature.NewStore(stateDir)
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}
	lc := mocks.NewMockFeatureLifecycle()
	lc.GetFn = store.Load
	lc.ListFn = func() ([]*feature.Feature, error) { return []*feature.Feature{f}, nil }
	lc.MarkRepoKBFailedFn = func(id, repo, msg string) error { return nil }
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
		CmdRunner:   mocks.NewMockCommandRunner(),
		PhaseRunner: &agent.PhaseRunner{StateDir: stateDir},
	}, Hooks{})

	if err := o.onKnowledgeBaseLoopDone(f.ID, "repo-a", &agent.BlockingLoopResult{
		FinalStatus: agent.BlockingLoopStatusProtocolViolation,
		LastError:   "synthetic validator violation",
	}); err != nil {
		t.Fatalf("onKnowledgeBaseLoopDone() error = %v", err)
	}

	gotIndex, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read index.md after retry: %v", err)
	}
	if string(gotIndex) != string(indexBytes) {
		t.Fatalf("index.md changed: got %q, want %q", gotIndex, indexBytes)
	}
	gotState, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state.json after retry: %v", err)
	}
	if string(gotState) != string(stateBytes) {
		t.Fatalf("state.json changed: got %q, want %q", gotState, stateBytes)
	}
	if matches, err := filepath.Glob(filepath.Join(kbDir, ".protocol-retry-*.yaml")); err != nil || len(matches) != 0 {
		t.Fatalf("KB protocol retry sidecars = %v, %v; want none", matches, err)
	}
}
