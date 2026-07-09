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
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// ---------------------------------------------------------------------------
// Category A — KB completion
// ---------------------------------------------------------------------------

func writeKBCompletionArtifacts(t *testing.T, stateDir, repoName string, withMarker, withIndex bool) string {
	t.Helper()
	kbDir := agent.KBStateDir(stateDir, repoName)
	if err := os.MkdirAll(kbDir, 0o755); err != nil {
		t.Fatalf("mkdir kb dir: %v", err)
	}
	if withIndex {
		if err := os.WriteFile(agent.KBPath(kbDir), []byte("# repo kb\n"), 0o644); err != nil {
			t.Fatalf("write index.md: %v", err)
		}
	}
	if withMarker {
		if err := os.WriteFile(filepath.Join(kbDir, agent.PhaseCompleteFile), nil, 0o644); err != nil {
			t.Fatalf("write phase_complete: %v", err)
		}
	}
	return kbDir
}

type kbRetryFixture struct {
	orchestrator *orchestrator.Orchestrator
	feature      *feature.Feature
	store        *featureStore
	runner       *capturingPhaseRunner
	lifecycle    *mocks.MockFeatureLifecycle
	kbDir        string
}

func newKBRetryFixture(t *testing.T, featureID string, repoNames ...string) kbRetryFixture {
	t.Helper()
	cpr := newCapturingPhaseRunner(t)
	repos := make([]feature.FeatureRepo, 0, len(repoNames))
	kbStatus := make(map[string]string, len(repoNames))
	for _, name := range repoNames {
		repos = append(repos, feature.FeatureRepo{Name: name, Path: filepath.Join(t.TempDir(), name)})
		kbStatus[name] = "running"
	}
	f := &feature.Feature{
		ID:            featureID,
		Status:        feature.StatusBuildingKB,
		CurrentPhase:  feature.PhaseKnowledgeBase,
		Pipeline:      feature.PipelineLarge,
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
		Repos:         repos,
		KBStatus:      kbStatus,
	}
	fs := newFeatureStore(f)
	lc := lifecycleForFeature(f)
	lc.GetFn = fs.Load
	lc.ListFn = fs.List
	lc.MarkFailedFn = func(id, ft, msg string) error {
		return fs.Modify(id, func(ff *feature.Feature) error {
			ff.Status = feature.StatusFailed
			ff.FailureType = ft
			ff.LastError = msg
			return nil
		})
	}
	lc.MarkRepoKBCompletedFn = func(id, repoName string) error {
		return fs.Modify(id, func(ff *feature.Feature) error {
			ff.KBStatus[repoName] = kbStatusCompleted
			return nil
		})
	}
	lc.MarkRepoKBFailedFn = func(id, repoName, msg string) error {
		return fs.Modify(id, func(ff *feature.Feature) error {
			ff.KBStatus[repoName] = "failed: " + msg
			return nil
		})
	}
	lc.AllKBsCompletedFn = func(id string) (bool, error) { return false, nil }

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
		Store:       fs,
		Sessions:    cpr.sm,
		PhaseRunner: cpr.pr,
		CmdRunner:   cpr.cmd,
	}, orchestrator.Hooks{})

	return kbRetryFixture{
		orchestrator: o,
		feature:      f,
		store:        fs,
		runner:       cpr,
		lifecycle:    lc,
		kbDir:        agent.KBStateDir(cpr.stateDir, repoNames[0]),
	}
}

func readKBRetrySidecar(t *testing.T, kbDir, featureID string) *agent.ProtocolRetrySidecar {
	t.Helper()
	sidecar, err := agent.ReadProtocolRetrySidecarAt(kbDir, agent.KBProtocolRetrySidecarFilename(featureID))
	if err != nil {
		t.Fatalf("ReadProtocolRetrySidecarAt() error = %v", err)
	}
	if sidecar == nil {
		t.Fatal("KB retry sidecar = nil, want retry state")
	}
	return sidecar
}

func releaseKBLockForRetry(t *testing.T, kbDir, featureID string) {
	t.Helper()
	if err := agent.ReleaseKBLock(kbDir, featureID); err != nil {
		t.Fatalf("ReleaseKBLock() error = %v", err)
	}
}

func loadKBRetryFeature(t *testing.T, fix kbRetryFixture) *feature.Feature {
	t.Helper()
	f, err := fix.store.Load(fix.feature.ID)
	if err != nil {
		t.Fatalf("load feature: %v", err)
	}
	return f
}

// onKBCompleted success + not-all-done: PhaseCompleted is NOT emitted yet;
// advance is NOT called. Just MarkRepoKBCompleted is recorded.
func TestOrchestrator_HandlePhaseCompletion_KB_Success_NotAllDone(t *testing.T) {
	stateDir := t.TempDir()
	writeKBCompletionArtifacts(t, stateDir, repoName, true, true)

	f := &feature.Feature{
		ID:           "feat-kb1",
		Status:       feature.StatusBuildingKB,
		CurrentPhase: feature.PhaseKnowledgeBase,
		Pipeline:     feature.PipelineLarge,
		Repos: []feature.FeatureRepo{
			{Name: repoName, Path: repoAPath},
			{Name: repoNameB, Path: repoBPath},
		},
	}
	lc := lifecycleForFeature(f)
	lc.AllKBsCompletedFn = func(id string) (bool, error) { return false, nil }
	lc.MarkRepoKBCompletedFn = func(id, repoName string) error { return nil }
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
		Store:       fs,
		PhaseRunner: &agent.PhaseRunner{StateDir: stateDir},
		CmdRunner:   mocks.NewMockCommandRunner(),
	}, orchestrator.Hooks{})

	err := o.HandlePhaseCompletion("feat-kb1", orchestrator.PhaseCompletionInput{
		Phase:     feature.PhaseKnowledgeBase,
		SessionID: "feat-kb1-kb-repo-a",
		Success:   true,
	})
	if err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	// MarkRepoKBCompleted recorded for repo-a.
	if n := countLifecycleCalls(lc, "MarkRepoKBCompleted"); n != 1 {
		t.Errorf("MarkRepoKBCompleted calls = %d, want 1", n)
	}
	refuteLifecycleCall(t, lc, "CompleteKnowledgeBase")

	events := drainEvents(o)
	if hasEventType(events, ports.PhaseCompleted) {
		t.Error("PhaseCompleted should NOT fire while other KBs are still pending")
	}

	// Regression guard: without a resolved stateDir, onKBCompleted must NOT
	// scribble a knowledge-base/ tree into the test's working directory.
	// See completion.go — MarkKBFresh is skipped when baseDir == "".
	if _, err := os.Stat("knowledge-base"); !os.IsNotExist(err) {
		t.Errorf("onKBCompleted leaked ./knowledge-base into CWD (stat err=%v)", err)
	}
}

// onKBCompleted success + all done: emits PhaseCompleted, marks feature KB done,
// clears ForceKBRebuild, and drives advanceToNextPhase.
func TestOrchestrator_HandlePhaseCompletion_KB_Success_AllDone(t *testing.T) {
	stateDir := t.TempDir()
	writeKBCompletionArtifacts(t, stateDir, repoName, true, true)

	f := &feature.Feature{
		ID:             "feat-kb2",
		Status:         feature.StatusBuildingKB,
		CurrentPhase:   feature.PhaseKnowledgeBase,
		Pipeline:       feature.PipelineLarge,
		ForceKBRebuild: true,
		Repos: []feature.FeatureRepo{
			{Name: repoName, Path: repoAPath},
		},
	}
	lc := lifecycleForFeature(f)
	lc.AllKBsCompletedFn = func(id string) (bool, error) { return true, nil }
	lc.MarkRepoKBCompletedFn = func(id, repoName string) error { return nil }
	lc.CompleteKnowledgeBaseFn = func(id string) error {
		f.Status = feature.StatusInquireReady
		return nil
	}
	lc.StartInquireFn = func(id string) error { f.Status = feature.StatusInquiring; return nil }
	fs := newFeatureStore(f)
	sm := mocks.NewMockSessionManager()
	sm.StartSessionFn = func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*session.SessionOpts) (ports.SessionHandle, error) {
		return newStubSessionHandle(id, featureID, phase, ""), nil
	}
	pr := &agent.PhaseRunner{
		StateDir:       stateDir,
		SessionManager: sm,
		BuildSessionFn: func(opts agent.BuildSessionOpts) ([]string, []string, *ports.SessionOpts, error) {
			return nil, nil, &ports.SessionOpts{}, nil
		},
	}

	var phaseCompletedFeatureID string
	var phaseCompletedPhase feature.Phase
	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
		Store:       fs,
		Sessions:    sm,
		PhaseRunner: pr,
		CmdRunner:   mocks.NewMockCommandRunner(),
	}, orchestrator.Hooks{
		OnPhaseCompleted: func(id string, p feature.Phase, _ error) {
			phaseCompletedFeatureID = id
			phaseCompletedPhase = p
		},
	})

	if err := o.HandlePhaseCompletion("feat-kb2", orchestrator.PhaseCompletionInput{
		Phase:     feature.PhaseKnowledgeBase,
		SessionID: "feat-kb2-kb-repo-a",
		Success:   true,
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	// PhaseCompleted hook fired.
	if phaseCompletedFeatureID != "feat-kb2" || phaseCompletedPhase != feature.PhaseKnowledgeBase {
		t.Errorf("OnPhaseCompleted got (%q, %v), want (feat-kb2, PhaseKnowledgeBase)",
			phaseCompletedFeatureID, phaseCompletedPhase)
	}
	// CompleteKnowledgeBase recorded.
	assertLifecycleCall(t, lc, "CompleteKnowledgeBase")

	events := drainEvents(o)
	if !hasEventType(events, ports.PhaseCompleted) {
		t.Error("expected PhaseCompleted event")
	}

	// Feature.ForceKBRebuild was cleared via Store.Modify.
	if f.ForceKBRebuild {
		t.Error("ForceKBRebuild should be cleared after KB completion")
	}
}

func TestOrchestrator_HandlePhaseCompletion_KB_MissingPhaseCompleteProtocolViolation(t *testing.T) {
	fix := newKBRetryFixture(t, "feat-no-marker", repoName, repoNameB, "repo-c")
	writeKBCompletionArtifacts(t, fix.runner.stateDir, repoName, false, true)
	fix.runner.sm.FeatureSessionsFn = func(id string) []session.SessionView {
		return []session.SessionView{
			newStubSessionHandle(id+"-kb-repo-b", id, feature.PhaseKnowledgeBase, repoNameB),
			newStubSessionHandle(id+"-kb-repo-c", id, feature.PhaseKnowledgeBase, "repo-c"),
		}
	}

	if err := fix.orchestrator.HandlePhaseCompletion(fix.feature.ID, orchestrator.PhaseCompletionInput{
		Phase:     feature.PhaseKnowledgeBase,
		SessionID: fix.feature.ID + "-kb-repo-a",
		Success:   true,
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion() error = %v", err)
	}

	reloaded := loadKBRetryFeature(t, fix)
	if reloaded.Status != feature.StatusBuildingKB {
		t.Fatalf("Status = %s, want %s", reloaded.Status, feature.StatusBuildingKB)
	}
	if reloaded.FailureType != "" || reloaded.LastError != "" {
		t.Fatalf("FailureType/LastError = %q/%q, want empty", reloaded.FailureType, reloaded.LastError)
	}
	if got := reloaded.KBStatus[repoName]; got != "running" {
		t.Fatalf("KBStatus[repo-a] = %q, want unchanged running", got)
	}
	sidecar := readKBRetrySidecar(t, fix.kbDir, fix.feature.ID)
	if sidecar.Role != agent.RoleKnowledgeBaseBuilder || sidecar.ActiveRun != reloaded.ActiveRun || sidecar.Consecutive != 1 {
		t.Fatalf("sidecar = %#v, want role=%s active_run=%d consecutive=1", sidecar, agent.RoleKnowledgeBaseBuilder, reloaded.ActiveRun)
	}
	if !strings.Contains(sidecar.LastViolation, agent.PhaseCompleteFile) {
		t.Fatalf("sidecar.LastViolation = %q, want phase_complete", sidecar.LastViolation)
	}
	if _, err := os.Stat(filepath.Join(fix.kbDir, agent.PhaseCompleteFile)); !os.IsNotExist(err) {
		t.Fatalf("phase_complete stat err = %v, want removed/missing", err)
	}
	if got := len(fix.runner.capturedByPhase(feature.PhaseKnowledgeBase)); got != 1 {
		t.Fatalf("KB retry starts = %d, want 1", got)
	}
	if got := fix.runner.capturedByPhase(feature.PhaseKnowledgeBase)[0].RepoName; got != repoName {
		t.Fatalf("retried repo = %q, want repo-a", got)
	}
	if got := len(fix.runner.sm.StopCalls); got != 0 {
		t.Fatalf("StopSession calls = %d, want 0", got)
	}
	refuteLifecycleCall(t, fix.lifecycle, "MarkRepoKBFailed")
	refuteLifecycleCall(t, fix.lifecycle, "MarkFailed")
	refuteLifecycleCall(t, fix.lifecycle, "MarkRepoKBCompleted")
	refuteLifecycleCall(t, fix.lifecycle, "CompleteKnowledgeBase")

	events := drainEvents(fix.orchestrator)
	if hasEventType(events, ports.PhaseCompleted) {
		t.Fatalf("PhaseCompleted emitted on retry: %#v", events)
	}
	if !hasEventType(events, ports.FeatureFailed) {
		return
	}
	t.Fatalf("FeatureFailed emitted on retry: %#v", events)
}

func TestOrchestrator_HandlePhaseCompletion_KB_MissingIndexProtocolViolation(t *testing.T) {
	fix := newKBRetryFixture(t, "feat-no-index", repoName)
	writeKBCompletionArtifacts(t, fix.runner.stateDir, repoName, true, false)

	if err := fix.orchestrator.HandlePhaseCompletion(fix.feature.ID, orchestrator.PhaseCompletionInput{
		Phase:     feature.PhaseKnowledgeBase,
		SessionID: fix.feature.ID + "-kb-repo-a",
		Success:   true,
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion() error = %v", err)
	}

	reloaded := loadKBRetryFeature(t, fix)
	if reloaded.FailureType != "" || reloaded.LastError != "" {
		t.Fatalf("FailureType/LastError = %q/%q, want empty", reloaded.FailureType, reloaded.LastError)
	}
	if got := reloaded.KBStatus[repoName]; got != "running" {
		t.Fatalf("KBStatus[repo-a] = %q, want unchanged running", got)
	}
	sidecar := readKBRetrySidecar(t, fix.kbDir, fix.feature.ID)
	if sidecar.Consecutive != 1 || !strings.Contains(sidecar.LastViolation, "index.md") {
		t.Fatalf("sidecar = %#v, want first index.md retry", sidecar)
	}
	if _, err := os.Stat(filepath.Join(fix.kbDir, agent.PhaseCompleteFile)); !os.IsNotExist(err) {
		t.Fatalf("phase_complete stat err = %v, want removed", err)
	}
	if got := len(fix.runner.capturedByPhase(feature.PhaseKnowledgeBase)); got != 1 {
		t.Fatalf("KB retry starts = %d, want 1", got)
	}
	refuteLifecycleCall(t, fix.lifecycle, "MarkRepoKBFailed")
	refuteLifecycleCall(t, fix.lifecycle, "MarkFailed")
	refuteLifecycleCall(t, fix.lifecycle, "MarkRepoKBCompleted")
	refuteLifecycleCall(t, fix.lifecycle, "CompleteKnowledgeBase")
	if events := drainEvents(fix.orchestrator); hasEventType(events, ports.PhaseCompleted) {
		t.Fatalf("PhaseCompleted emitted on retry: %#v", events)
	}
}

func TestOrchestrator_HandlePhaseCompletion_KB_RetryThenSuccessCompletesRepo(t *testing.T) {
	fix := newKBRetryFixture(t, "feat-retry-success", repoName)
	writeKBCompletionArtifacts(t, fix.runner.stateDir, repoName, false, true)

	if err := fix.orchestrator.HandlePhaseCompletion(fix.feature.ID, orchestrator.PhaseCompletionInput{
		Phase:     feature.PhaseKnowledgeBase,
		SessionID: fix.feature.ID + "-kb-repo-a",
		Success:   true,
	}); err != nil {
		t.Fatalf("first HandlePhaseCompletion() error = %v", err)
	}
	releaseKBLockForRetry(t, fix.kbDir, fix.feature.ID)
	drainEvents(fix.orchestrator)

	writeKBCompletionArtifacts(t, fix.runner.stateDir, repoName, true, true)
	if err := fix.orchestrator.HandlePhaseCompletion(fix.feature.ID, orchestrator.PhaseCompletionInput{
		Phase:     feature.PhaseKnowledgeBase,
		SessionID: fix.feature.ID + "-kb-repo-a",
		Success:   true,
	}); err != nil {
		t.Fatalf("second HandlePhaseCompletion() error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(fix.kbDir, agent.KBProtocolRetrySidecarFilename(fix.feature.ID))); !os.IsNotExist(err) {
		t.Fatalf("sidecar stat err = %v, want removed", err)
	}
	reloaded := loadKBRetryFeature(t, fix)
	if got := reloaded.KBStatus[repoName]; got != kbStatusCompleted {
		t.Fatalf("KBStatus[repo-a] = %q, want completed", got)
	}
	if got := countLifecycleCalls(fix.lifecycle, "MarkRepoKBCompleted"); got != 1 {
		t.Fatalf("MarkRepoKBCompleted calls = %d, want 1", got)
	}
	refuteLifecycleCall(t, fix.lifecycle, "MarkRepoKBFailed")
	refuteLifecycleCall(t, fix.lifecycle, "MarkFailed")
	if got := len(fix.runner.capturedByPhase(feature.PhaseKnowledgeBase)); got != 1 {
		t.Fatalf("KB retry starts = %d, want 1", got)
	}
	if events := drainEvents(fix.orchestrator); hasEventType(events, ports.PhaseCompleted) {
		t.Fatalf("PhaseCompleted emitted before all repos completed: %#v", events)
	}
}

func TestOrchestrator_HandlePhaseCompletion_KB_ThirdViolationTerminates(t *testing.T) {
	fix := newKBRetryFixture(t, "feat-third-violation", repoName, repoNameB)
	writeKBCompletionArtifacts(t, fix.runner.stateDir, repoName, false, true)
	fix.runner.sm.FeatureSessionsFn = func(id string) []session.SessionView {
		return []session.SessionView{
			newStubSessionHandle(id+"-kb-repo-b", id, feature.PhaseKnowledgeBase, repoNameB),
		}
	}

	for attempt := 1; attempt <= agent.DefaultMaxConsecutiveProtocolViolations; attempt++ {
		if attempt > 1 {
			releaseKBLockForRetry(t, fix.kbDir, fix.feature.ID)
		}
		if err := fix.orchestrator.HandlePhaseCompletion(fix.feature.ID, orchestrator.PhaseCompletionInput{
			Phase:     feature.PhaseKnowledgeBase,
			SessionID: fix.feature.ID + "-kb-repo-a",
			Success:   true,
		}); err != nil {
			t.Fatalf("HandlePhaseCompletion(attempt %d) error = %v", attempt, err)
		}
	}

	violations := []agent.ProtocolViolation{{
		Artifact: agent.PhaseCompleteFile,
		Reason:   "SDK reported success but phase_complete was not present",
	}}
	wantErr := agent.FormatSingleShotProtocolViolationError(agent.RoleKnowledgeBaseBuilder, fix.kbDir, violations)
	reloaded := loadKBRetryFeature(t, fix)
	if reloaded.FailureType != feature.FailureProtocolViolation {
		t.Fatalf("FailureType = %q, want %q", reloaded.FailureType, feature.FailureProtocolViolation)
	}
	if reloaded.LastError != wantErr {
		t.Fatalf("LastError = %q, want %q", reloaded.LastError, wantErr)
	}
	if got := reloaded.KBStatus[repoName]; got != "failed: "+wantErr {
		t.Fatalf("KBStatus[repo-a] = %q, want failed with terminal error", got)
	}
	sidecar := readKBRetrySidecar(t, fix.kbDir, fix.feature.ID)
	if sidecar.Consecutive != agent.DefaultMaxConsecutiveProtocolViolations {
		t.Fatalf("sidecar.Consecutive = %d, want %d", sidecar.Consecutive, agent.DefaultMaxConsecutiveProtocolViolations)
	}
	if got := countLifecycleCalls(fix.lifecycle, "MarkRepoKBFailed"); got != 1 {
		t.Fatalf("MarkRepoKBFailed calls = %d, want 1", got)
	}
	if got := countLifecycleCalls(fix.lifecycle, "MarkFailed"); got != 1 {
		t.Fatalf("MarkFailed calls = %d, want 1", got)
	}
	if got := len(fix.runner.sm.StopCalls); got != 1 {
		t.Fatalf("StopSession calls = %d, want 1", got)
	}
	if got := len(fix.runner.capturedByPhase(feature.PhaseKnowledgeBase)); got != agent.DefaultMaxConsecutiveProtocolViolations-1 {
		t.Fatalf("KB retry starts = %d, want %d", got, agent.DefaultMaxConsecutiveProtocolViolations-1)
	}
	events := drainEvents(fix.orchestrator)
	if got := countPhaseCompletedEvents(events, feature.PhaseKnowledgeBase); got != 1 {
		t.Fatalf("PhaseCompleted events = %d, want 1; events=%#v", got, events)
	}
}

func TestOrchestrator_HandlePhaseCompletion_KB_SidecarScoping(t *testing.T) {
	t.Run("stale_active_run_resets", func(t *testing.T) {
		fix := newKBRetryFixture(t, "feat-stale-run", repoName)
		writeKBCompletionArtifacts(t, fix.runner.stateDir, repoName, false, true)
		if err := agent.WriteProtocolRetrySidecarAt(fix.kbDir, agent.KBProtocolRetrySidecarFilename(fix.feature.ID), agent.ProtocolRetrySidecar{
			Role:          agent.RoleKnowledgeBaseBuilder,
			ActiveRun:     fix.feature.ActiveRun + 1,
			Consecutive:   2,
			LastViolation: "stale run",
			UpdatedAt:     time.Now().UTC(),
		}); err != nil {
			t.Fatalf("WriteProtocolRetrySidecarAt() error = %v", err)
		}

		if err := fix.orchestrator.HandlePhaseCompletion(fix.feature.ID, orchestrator.PhaseCompletionInput{
			Phase:     feature.PhaseKnowledgeBase,
			SessionID: fix.feature.ID + "-kb-repo-a",
			Success:   true,
		}); err != nil {
			t.Fatalf("HandlePhaseCompletion() error = %v", err)
		}

		reloaded := loadKBRetryFeature(t, fix)
		if reloaded.FailureType != "" {
			t.Fatalf("FailureType = %q, want empty", reloaded.FailureType)
		}
		sidecar := readKBRetrySidecar(t, fix.kbDir, fix.feature.ID)
		if sidecar.ActiveRun != fix.feature.ActiveRun || sidecar.Consecutive != 1 {
			t.Fatalf("sidecar = %#v, want active_run=%d consecutive=1", sidecar, fix.feature.ActiveRun)
		}
	})

	t.Run("matching_active_run_terminates", func(t *testing.T) {
		fix := newKBRetryFixture(t, "feat-matching-run", repoName)
		writeKBCompletionArtifacts(t, fix.runner.stateDir, repoName, false, true)
		if err := agent.WriteProtocolRetrySidecarAt(fix.kbDir, agent.KBProtocolRetrySidecarFilename(fix.feature.ID), agent.ProtocolRetrySidecar{
			Role:          agent.RoleKnowledgeBaseBuilder,
			ActiveRun:     fix.feature.ActiveRun,
			Consecutive:   2,
			LastViolation: "prior run",
			UpdatedAt:     time.Now().UTC(),
		}); err != nil {
			t.Fatalf("WriteProtocolRetrySidecarAt() error = %v", err)
		}

		if err := fix.orchestrator.HandlePhaseCompletion(fix.feature.ID, orchestrator.PhaseCompletionInput{
			Phase:     feature.PhaseKnowledgeBase,
			SessionID: fix.feature.ID + "-kb-repo-a",
			Success:   true,
		}); err != nil {
			t.Fatalf("HandlePhaseCompletion() error = %v", err)
		}

		reloaded := loadKBRetryFeature(t, fix)
		if reloaded.FailureType != feature.FailureProtocolViolation {
			t.Fatalf("FailureType = %q, want %q", reloaded.FailureType, feature.FailureProtocolViolation)
		}
		sidecar := readKBRetrySidecar(t, fix.kbDir, fix.feature.ID)
		if sidecar.Consecutive != agent.DefaultMaxConsecutiveProtocolViolations {
			t.Fatalf("sidecar.Consecutive = %d, want %d", sidecar.Consecutive, agent.DefaultMaxConsecutiveProtocolViolations)
		}
	})

	t.Run("other_feature_sidecar_ignored", func(t *testing.T) {
		fix := newKBRetryFixture(t, "feat-current", repoName)
		writeKBCompletionArtifacts(t, fix.runner.stateDir, repoName, false, true)
		otherFilename := agent.KBProtocolRetrySidecarFilename("feat-other")
		otherSidecar := agent.ProtocolRetrySidecar{
			Role:          agent.RoleKnowledgeBaseBuilder,
			ActiveRun:     fix.feature.ActiveRun,
			Consecutive:   3,
			LastViolation: "other feature terminal",
			UpdatedAt:     time.Date(2026, 5, 18, 1, 2, 3, 0, time.UTC),
		}
		if err := agent.WriteProtocolRetrySidecarAt(fix.kbDir, otherFilename, otherSidecar); err != nil {
			t.Fatalf("WriteProtocolRetrySidecarAt(other) error = %v", err)
		}

		if err := fix.orchestrator.HandlePhaseCompletion(fix.feature.ID, orchestrator.PhaseCompletionInput{
			Phase:     feature.PhaseKnowledgeBase,
			SessionID: fix.feature.ID + "-kb-repo-a",
			Success:   true,
		}); err != nil {
			t.Fatalf("HandlePhaseCompletion() error = %v", err)
		}

		reloaded := loadKBRetryFeature(t, fix)
		if reloaded.FailureType != "" {
			t.Fatalf("FailureType = %q, want empty", reloaded.FailureType)
		}
		current := readKBRetrySidecar(t, fix.kbDir, fix.feature.ID)
		if current.Consecutive != 1 {
			t.Fatalf("current sidecar.Consecutive = %d, want 1", current.Consecutive)
		}
		other, err := agent.ReadProtocolRetrySidecarAt(fix.kbDir, otherFilename)
		if err != nil {
			t.Fatalf("ReadProtocolRetrySidecarAt(other) error = %v", err)
		}
		if other == nil || other.Consecutive != otherSidecar.Consecutive || other.LastViolation != otherSidecar.LastViolation {
			t.Fatalf("other sidecar = %#v, want preserved %#v", other, otherSidecar)
		}
	})
}

func TestOrchestrator_HandlePhaseCompletion_KB_SessionCrashDoesNotReadSidecar(t *testing.T) {
	fix := newKBRetryFixture(t, "feat-crash", repoName)
	if err := os.MkdirAll(fix.kbDir, 0o755); err != nil {
		t.Fatalf("mkdir kb dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fix.kbDir, agent.KBProtocolRetrySidecarFilename(fix.feature.ID)), []byte("role: ["), 0o644); err != nil {
		t.Fatalf("write malformed sidecar: %v", err)
	}

	if err := fix.orchestrator.HandlePhaseCompletion(fix.feature.ID, orchestrator.PhaseCompletionInput{
		Phase:       feature.PhaseKnowledgeBase,
		SessionID:   fix.feature.ID + "-kb-repo-a",
		Success:     false,
		ErrorDetail: "kb crashed",
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion() error = %v", err)
	}

	reloaded := loadKBRetryFeature(t, fix)
	if reloaded.FailureType != feature.FailureSessionCrash {
		t.Fatalf("FailureType = %q, want %q", reloaded.FailureType, feature.FailureSessionCrash)
	}
	if got := len(fix.runner.capturedByPhase(feature.PhaseKnowledgeBase)); got != 0 {
		t.Fatalf("KB retry starts = %d, want 0", got)
	}
	if got := countLifecycleCalls(fix.lifecycle, "MarkRepoKBFailed"); got != 1 {
		t.Fatalf("MarkRepoKBFailed calls = %d, want 1", got)
	}
}

// onKBCompleted terminal-state guard: when feature.Status != BuildingKB, the
// handler no-ops even if all KBs are marked done.
func TestOrchestrator_HandlePhaseCompletion_KB_Terminal_Interrupted(t *testing.T) {
	f := &feature.Feature{
		ID:           "feat-kb-int",
		Status:       feature.StatusInterrupted,
		CurrentPhase: feature.PhaseKnowledgeBase,
		Pipeline:     feature.PipelineLarge,
		Repos:        []feature.FeatureRepo{{Name: repoName, Path: repoAPath}},
	}
	lc := lifecycleForFeature(f)
	lc.AllKBsCompletedFn = func(id string) (bool, error) { return true, nil }
	lc.MarkRepoKBCompletedFn = func(id, repoName string) error { return nil }
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{})

	if err := o.HandlePhaseCompletion("feat-kb-int", orchestrator.PhaseCompletionInput{
		Phase:     feature.PhaseKnowledgeBase,
		SessionID: "feat-kb-int-kb-repo-a",
		Success:   true,
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	// MarkRepoKBCompleted still recorded before the terminal-state check, but
	// CompleteKnowledgeBase must not run when status is not BuildingKB.
	refuteLifecycleCall(t, lc, "CompleteKnowledgeBase")
	events := drainEvents(o)
	if hasEventType(events, ports.PhaseCompleted) {
		t.Error("PhaseCompleted should NOT fire for terminal-state KB completion")
	}
}

// onKBCompleted failure path: MarkRepoKBFailed + markFailedWithEvent + emits
// PhaseCompleted(err) and FeatureFailed.
func TestOrchestrator_HandlePhaseCompletion_KB_Failure(t *testing.T) {
	f := &feature.Feature{
		ID:           "feat-kb-fail",
		Status:       feature.StatusBuildingKB,
		CurrentPhase: feature.PhaseKnowledgeBase,
		Pipeline:     feature.PipelineLarge,
		Repos:        []feature.FeatureRepo{{Name: repoName, Path: repoAPath}},
	}
	lc := lifecycleForFeature(f)
	lc.MarkRepoKBFailedFn = func(id, repo, msg string) error { return nil }
	lc.MarkFailedFn = func(id, ft, msg string) error { return nil }
	fs := newFeatureStore(f)

	var failureType, errMsg string
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{
		OnFeatureFailed: func(id string, ft, em string) {
			failureType = ft
			errMsg = em
		},
	})

	if err := o.HandlePhaseCompletion("feat-kb-fail", orchestrator.PhaseCompletionInput{
		Phase:       feature.PhaseKnowledgeBase,
		SessionID:   "feat-kb-fail-kb-repo-a",
		Success:     false,
		ErrorDetail: "kb tanked",
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	assertLifecycleCall(t, lc, "MarkRepoKBFailed")
	assertLifecycleCall(t, lc, "MarkFailed")
	if failureType != feature.FailureSessionCrash {
		t.Errorf("failure type = %q, want %q", failureType, feature.FailureSessionCrash)
	}
	if errMsg != "kb tanked" {
		t.Errorf("error message = %q, want %q", errMsg, "kb tanked")
	}

	events := drainEvents(o)
	if !hasEventType(events, ports.PhaseCompleted) {
		t.Error("expected PhaseCompleted event on KB failure")
	}
	if !hasEventType(events, ports.FeatureFailed) {
		t.Error("expected FeatureFailed event")
	}
}

// ---------------------------------------------------------------------------
// Category B — Artifact phase completion (Inquire / Research / Design)
// ---------------------------------------------------------------------------

type artifactPhaseCase struct {
	name        string
	phase       feature.Phase
	phaseKey    string
	artifactKey string
	status      feature.Status
}

func artifactPhaseCases() []artifactPhaseCase {
	return []artifactPhaseCase{
		{"inquire", feature.PhaseInquire, "inquire", "inquire", feature.StatusInquiring},
		{"research", feature.PhaseResearch, "research", "research", feature.StatusResearching},
		// The Design phase keeps the legacy "design" on-disk subdir for
		// compat but persists under the canonical Design artifact key.
		{"design", feature.PhaseDesign, "design", feature.DesignArtifactKey, feature.StatusDesigning},
	}
}

func newArtifactPhaseOrchestratorFixture(t *testing.T, tc artifactPhaseCase) (*orchestrator.Orchestrator, *feature.Feature, *feature.Store) {
	t.Helper()

	stateDir := t.TempDir()
	f := &feature.Feature{
		ID:            "feat-" + tc.phaseKey,
		Status:        tc.status,
		CurrentPhase:  tc.phase,
		Pipeline:      feature.PipelineMedium,
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	store := feature.NewStore(stateDir)
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}
	lc := lifecycleForFeature(f)
	lc.GetFn = func(id string) (*feature.Feature, error) {
		return store.Load(id)
	}
	lc.MarkFailedFn = func(id, ft, msg string) error {
		return store.Modify(id, func(ff *feature.Feature) error {
			ff.Status = feature.StatusFailed
			ff.FailureType = ft
			ff.LastError = msg
			return nil
		})
	}
	lc.CompleteInquireFn = func(id string) error {
		return store.Modify(id, func(ff *feature.Feature) error {
			ff.Status = feature.StatusInquireReady
			return nil
		})
	}
	lc.CompleteResearchFn = func(id string) error {
		return store.Modify(id, func(ff *feature.Feature) error {
			ff.Status = feature.StatusPlanReady
			return nil
		})
	}
	lc.CompleteDesignFn = func(id string) error {
		return store.Modify(id, func(ff *feature.Feature) error {
			ff.Status = feature.StatusDesignReady
			return nil
		})
	}
	lc.StartResearchFn = func(id string) error {
		return store.Modify(id, func(ff *feature.Feature) error {
			ff.Status = feature.StatusResearching
			return nil
		})
	}
	lc.StartPlanningFn = func(id string) error {
		return store.Modify(id, func(ff *feature.Feature) error {
			ff.Status = feature.StatusPlanning
			return nil
		})
	}

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     store,
	}, orchestrator.Hooks{})
	return o, f, store
}

func writePhaseComplete(t *testing.T, stateDir string, f *feature.Feature, phaseKey string) string {
	t.Helper()
	phaseDir := filepath.Join(agent.ActiveRunDir(stateDir, f), phaseKey)
	if err := os.MkdirAll(phaseDir, 0o755); err != nil {
		t.Fatalf("mkdir phase dir: %v", err)
	}
	markerPath := filepath.Join(phaseDir, agent.PhaseCompleteFile)
	if err := os.WriteFile(markerPath, nil, 0o644); err != nil {
		t.Fatalf("write phase_complete: %v", err)
	}
	return markerPath
}

func writePhaseMarkdown(t *testing.T, stateDir string, f *feature.Feature, phaseKey, name string) string {
	t.Helper()
	phaseDir := filepath.Join(agent.ActiveRunDir(stateDir, f), phaseKey)
	if err := os.MkdirAll(phaseDir, 0o755); err != nil {
		t.Fatalf("mkdir phase dir: %v", err)
	}
	path := filepath.Join(phaseDir, name)
	if err := os.WriteFile(path, []byte("# "+phaseKey+"\n"), 0o644); err != nil {
		t.Fatalf("write phase markdown: %v", err)
	}
	return path
}

func setOlderModTime(t *testing.T, path string) {
	t.Helper()
	older := time.Now().Add(-1 * time.Hour)
	if err := os.Chtimes(path, older, older); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}

func loadStoredFeature(t *testing.T, store *feature.Store, featureID string) *feature.Feature {
	t.Helper()
	f, err := store.Load(featureID)
	if err != nil {
		t.Fatalf("load feature: %v", err)
	}
	return f
}

type artifactPhaseRetryFixture struct {
	orchestrator *orchestrator.Orchestrator
	feature      *feature.Feature
	store        *feature.Store
	runner       *capturingPhaseRunner
	phaseDir     string
	lifecycle    *mocks.MockFeatureLifecycle
}

func newArtifactPhaseRetryFixture(t *testing.T, tc artifactPhaseCase, checkpoints feature.Checkpoints) artifactPhaseRetryFixture {
	t.Helper()

	cpr := newCapturingPhaseRunner(t)
	stateDir := cpr.stateDir
	f := &feature.Feature{
		ID:            "feat-retry-" + tc.phaseKey,
		Status:        tc.status,
		CurrentPhase:  tc.phase,
		Pipeline:      feature.PipelineLarge,
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
		Checkpoints:   checkpoints,
		Artifacts:     make(map[string]string),
	}
	seedPriorInteractiveArtifacts(t, stateDir, f, tc)

	store := feature.NewStore(stateDir)
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}

	lc := lifecycleForFeature(f)
	lc.GetFn = func(id string) (*feature.Feature, error) {
		return store.Load(id)
	}
	lc.MarkFailedFn = func(id, ft, msg string) error {
		return store.Modify(id, func(ff *feature.Feature) error {
			ff.Status = feature.StatusFailed
			ff.FailureType = ft
			ff.LastError = msg
			return nil
		})
	}
	lc.CompleteInquireFn = func(id string) error {
		return store.Modify(id, func(ff *feature.Feature) error {
			ff.Status = feature.StatusInquireReady
			return nil
		})
	}
	lc.CompleteResearchFn = func(id string) error {
		return store.Modify(id, func(ff *feature.Feature) error {
			ff.Status = feature.StatusDesignReady
			return nil
		})
	}
	lc.CompleteDesignFn = func(id string) error {
		return store.Modify(id, func(ff *feature.Feature) error {
			ff.Status = feature.StatusPlanReady
			return nil
		})
	}
	lc.StartInquireFn = func(id string) error {
		return store.Modify(id, func(ff *feature.Feature) error {
			ff.Status = feature.StatusInquiring
			return nil
		})
	}
	lc.StartResearchFn = func(id string) error {
		return store.Modify(id, func(ff *feature.Feature) error {
			ff.Status = feature.StatusResearching
			return nil
		})
	}
	lc.StartDesignFn = func(id string) error {
		return store.Modify(id, func(ff *feature.Feature) error {
			ff.Status = feature.StatusDesigning
			return nil
		})
	}

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
		Store:       store,
		Sessions:    cpr.sm,
		PhaseRunner: cpr.pr,
		CmdRunner:   cpr.cmd,
	}, orchestrator.Hooks{})

	return artifactPhaseRetryFixture{
		orchestrator: o,
		feature:      f,
		store:        store,
		runner:       cpr,
		phaseDir:     filepath.Join(agent.ActiveRunDir(stateDir, f), tc.phaseKey),
		lifecycle:    lc,
	}
}

func seedPriorInteractiveArtifacts(t *testing.T, stateDir string, f *feature.Feature, tc artifactPhaseCase) {
	t.Helper()
	switch tc.phase {
	case feature.PhaseResearch:
		f.Artifacts["inquire"] = writePhaseMarkdown(t, stateDir, f, "inquire", "inquire.md")
	case feature.PhaseDesign:
		f.Artifacts["inquire"] = writePhaseMarkdown(t, stateDir, f, "inquire", "inquire.md")
		f.Artifacts["research"] = writePhaseMarkdown(t, stateDir, f, "research", "research.md")
	}
}

func artifactPhaseRoleForTest(t *testing.T, phase feature.Phase) agent.Role {
	t.Helper()
	switch phase {
	case feature.PhaseInquire:
		return agent.RoleInquirer
	case feature.PhaseResearch:
		return agent.RoleResearcher
	case feature.PhaseDesign:
		return agent.RoleDesigner
	default:
		t.Fatalf("unknown artifact phase %s", phase)
		return ""
	}
}

func retrySuccessCheckpointForTest(phase feature.Phase) feature.Checkpoints {
	switch phase {
	case feature.PhaseInquire:
		return feature.Checkpoints{InquiryReview: true}
	case feature.PhaseResearch:
		return feature.Checkpoints{ResearchReview: true}
	case feature.PhaseDesign:
		return feature.Checkpoints{DesignReview: true}
	default:
		return feature.Checkpoints{}
	}
}

func countPhaseCompletedEvents(events []ports.Event, phase feature.Phase) int {
	count := 0
	for _, ev := range events {
		if ev.Type == ports.PhaseCompleted && ev.Phase == phase {
			count++
		}
	}
	return count
}

func assertFirstArtifactRetry(t *testing.T, fix artifactPhaseRetryFixture, tc artifactPhaseCase, wantViolation string) {
	t.Helper()
	reloaded := loadStoredFeature(t, fix.store, fix.feature.ID)
	if reloaded.Status != tc.status {
		t.Fatalf("Status = %s, want %s", reloaded.Status, tc.status)
	}
	if reloaded.FailureType != "" {
		t.Fatalf("FailureType = %q, want empty", reloaded.FailureType)
	}
	if reloaded.LastError != "" {
		t.Fatalf("LastError = %q, want empty", reloaded.LastError)
	}

	sidecar, err := agent.ReadProtocolRetrySidecar(fix.phaseDir)
	if err != nil {
		t.Fatalf("ReadProtocolRetrySidecar() error = %v", err)
	}
	if sidecar == nil {
		t.Fatal("sidecar = nil, want retry state")
	}
	if sidecar.Consecutive != 1 {
		t.Fatalf("sidecar.Consecutive = %d, want 1", sidecar.Consecutive)
	}
	if sidecar.ActiveRun != reloaded.ActiveRun {
		t.Fatalf("sidecar.ActiveRun = %d, want %d", sidecar.ActiveRun, reloaded.ActiveRun)
	}
	if sidecar.Role != artifactPhaseRoleForTest(t, tc.phase) {
		t.Fatalf("sidecar.Role = %q, want %q", sidecar.Role, artifactPhaseRoleForTest(t, tc.phase))
	}
	if !strings.Contains(sidecar.LastViolation, wantViolation) {
		t.Fatalf("sidecar.LastViolation = %q, want %q", sidecar.LastViolation, wantViolation)
	}
	if _, err := os.Stat(filepath.Join(fix.phaseDir, agent.PhaseCompleteFile)); !os.IsNotExist(err) {
		t.Fatalf("phase_complete stat err = %v, want removed", err)
	}
	if got := len(fix.runner.capturedByPhase(tc.phase)); got != 1 {
		t.Fatalf("starter captures for %s = %d, want 1", tc.phase, got)
	}
	if events := drainEvents(fix.orchestrator); hasEventType(events, ports.PhaseCompleted) {
		t.Fatalf("PhaseCompleted emitted on retry: %#v", events)
	}
}

func gitStatusForCompletionTest(t *testing.T, repoPath string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", repoPath, "status", "--porcelain", "--untracked-files=all")
	cmd.Env = testutil.GitTestEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git status: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestOrchestrator_HandlePhaseCompletion_ArtifactPhaseMissingPhaseCompleteProtocolViolation(t *testing.T) {
	for _, tc := range artifactPhaseCases() {
		t.Run(tc.name, func(t *testing.T) {
			fix := newArtifactPhaseRetryFixture(t, tc, feature.Checkpoints{})
			writePhaseMarkdown(t, fix.store.BaseDir, fix.feature, tc.phaseKey, "artifact.md")

			if err := fix.orchestrator.HandlePhaseCompletion(fix.feature.ID, orchestrator.PhaseCompletionInput{
				Phase:   tc.phase,
				Success: true,
			}); err != nil {
				t.Fatalf("HandlePhaseCompletion() error = %v", err)
			}

			assertFirstArtifactRetry(t, fix, tc, agent.PhaseCompleteFile)
		})
	}
}

func TestOrchestrator_HandlePhaseCompletion_ArtifactPhaseMissingMarkdownProtocolViolation(t *testing.T) {
	for _, tc := range artifactPhaseCases() {
		t.Run(tc.name, func(t *testing.T) {
			fix := newArtifactPhaseRetryFixture(t, tc, feature.Checkpoints{})
			writePhaseComplete(t, fix.store.BaseDir, fix.feature, tc.phaseKey)

			if err := fix.orchestrator.HandlePhaseCompletion(fix.feature.ID, orchestrator.PhaseCompletionInput{
				Phase:   tc.phase,
				Success: true,
			}); err != nil {
				t.Fatalf("HandlePhaseCompletion() error = %v", err)
			}

			assertFirstArtifactRetry(t, fix, tc, "markdown")
		})
	}
}

func TestOrchestrator_HandlePhaseCompletion_ArtifactPhaseRepoMutationProtocolViolationRestoresWorktree(t *testing.T) {
	for _, tc := range artifactPhaseCases() {
		t.Run(tc.name, func(t *testing.T) {
			fix := newArtifactPhaseRetryFixture(t, tc, retrySuccessCheckpointForTest(tc.phase))
			fix.runner.cmd.RunFn = agent.NewExecCommandRunner().Run
			repoPath := testutil.InitGitRepo(t)
			if err := fix.store.Modify(fix.feature.ID, func(ff *feature.Feature) error {
				ff.Repos = []feature.FeatureRepo{{
					Name:         repoName,
					Path:         repoPath,
					WorktreePath: repoPath,
					Branch:       mainBranch,
					BaseBranch:   mainBranch,
				}}
				return nil
			}); err != nil {
				t.Fatalf("record feature repo: %v", err)
			}

			readmePath := filepath.Join(repoPath, "README.md")
			if err := os.WriteFile(readmePath, []byte("# Testu\n"), 0o644); err != nil {
				t.Fatalf("dirty README: %v", err)
			}
			strayPath := filepath.Join(repoPath, "stray.md")
			if err := os.WriteFile(strayPath, []byte("stray artifact\n"), 0o644); err != nil {
				t.Fatalf("write stray file: %v", err)
			}
			if got := gitStatusForCompletionTest(t, repoPath); got == "" {
				t.Fatal("repo status is clean before completion; test setup failed")
			}

			writePhaseComplete(t, fix.store.BaseDir, fix.feature, tc.phaseKey)
			writePhaseMarkdown(t, fix.store.BaseDir, fix.feature, tc.phaseKey, tc.phaseKey+".md")

			if err := fix.orchestrator.HandlePhaseCompletion(fix.feature.ID, orchestrator.PhaseCompletionInput{
				Phase:   tc.phase,
				Success: true,
			}); err != nil {
				t.Fatalf("HandlePhaseCompletion() error = %v", err)
			}

			data, err := os.ReadFile(readmePath)
			if err != nil {
				t.Fatalf("read README after restore: %v", err)
			}
			if got, want := string(data), "# Test\n"; got != want {
				t.Fatalf("README after restore = %q, want %q", got, want)
			}
			if _, err := os.Stat(strayPath); !os.IsNotExist(err) {
				t.Fatalf("stray file stat err = %v, want removed", err)
			}
			if got := gitStatusForCompletionTest(t, repoPath); got != "" {
				t.Fatalf("repo status after restore = %q, want clean", got)
			}

			matches, err := filepath.Glob(filepath.Join(fix.phaseDir, "violations", "repo-mutation-*.patch"))
			if err != nil {
				t.Fatalf("glob violation patches: %v", err)
			}
			if len(matches) != 1 {
				t.Fatalf("violation patch count = %d, want 1 (%v)", len(matches), matches)
			}
			patch, err := os.ReadFile(matches[0])
			if err != nil {
				t.Fatalf("read violation patch: %v", err)
			}
			patchText := string(patch)
			for _, want := range []string{repoName, "README.md", "# Testu", "stray.md"} {
				if !strings.Contains(patchText, want) {
					t.Fatalf("violation patch missing %q:\n%s", want, patchText)
				}
			}

			assertFirstArtifactRetry(t, fix, tc, "modified target repo")
		})
	}
}

func TestOrchestrator_HandlePhaseCompletion_ArtifactPhaseRepoMutationRestoresPrePhaseBaseline(t *testing.T) {
	tc := artifactPhaseCases()[0]
	fix := newArtifactPhaseRetryFixture(t, tc, retrySuccessCheckpointForTest(tc.phase))
	fix.runner.cmd.RunFn = agent.NewExecCommandRunner().Run
	repoPath := testutil.InitGitRepo(t)
	if err := fix.store.Modify(fix.feature.ID, func(ff *feature.Feature) error {
		ff.Repos = []feature.FeatureRepo{{
			Name:         repoName,
			Path:         repoPath,
			WorktreePath: repoPath,
			Branch:       mainBranch,
			BaseBranch:   mainBranch,
		}}
		return nil
	}); err != nil {
		t.Fatalf("record feature repo: %v", err)
	}

	readmePath := filepath.Join(repoPath, "README.md")
	if err := os.WriteFile(readmePath, []byte("# Baseline\n"), 0o644); err != nil {
		t.Fatalf("write baseline README: %v", err)
	}
	baselineNotePath := filepath.Join(repoPath, "baseline-note.md")
	if err := os.WriteFile(baselineNotePath, []byte("keep me\n"), 0o644); err != nil {
		t.Fatalf("write baseline untracked file: %v", err)
	}
	baselineStatus := gitStatusForCompletionTest(t, repoPath)
	if !strings.Contains(baselineStatus, "README.md") || !strings.Contains(baselineStatus, "baseline-note.md") {
		t.Fatalf("baseline status = %q, want README.md and baseline-note.md", baselineStatus)
	}

	if err := fix.orchestrator.StartFeature(fix.feature.ID); err != nil {
		t.Fatalf("StartFeature() error = %v", err)
	}
	drainEvents(fix.orchestrator)

	if err := os.WriteFile(readmePath, []byte("# Mutated by read-only phase\n"), 0o644); err != nil {
		t.Fatalf("write read-only mutation: %v", err)
	}
	if err := os.WriteFile(baselineNotePath, []byte("mutated note\n"), 0o644); err != nil {
		t.Fatalf("mutate baseline untracked file: %v", err)
	}
	strayPath := filepath.Join(repoPath, "stray.md")
	if err := os.WriteFile(strayPath, []byte("stray artifact\n"), 0o644); err != nil {
		t.Fatalf("write stray file: %v", err)
	}

	writePhaseComplete(t, fix.store.BaseDir, fix.feature, tc.phaseKey)
	writePhaseMarkdown(t, fix.store.BaseDir, fix.feature, tc.phaseKey, tc.phaseKey+".md")

	if err := fix.orchestrator.HandlePhaseCompletion(fix.feature.ID, orchestrator.PhaseCompletionInput{
		Phase:   tc.phase,
		Success: true,
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion() error = %v", err)
	}

	data, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("read README after restore: %v", err)
	}
	if got, want := string(data), "# Baseline\n"; got != want {
		t.Fatalf("README after restore = %q, want %q", got, want)
	}
	noteData, err := os.ReadFile(baselineNotePath)
	if err != nil {
		t.Fatalf("read baseline untracked file after restore: %v", err)
	}
	if got, want := string(noteData), "keep me\n"; got != want {
		t.Fatalf("baseline untracked after restore = %q, want %q", got, want)
	}
	if _, err := os.Stat(strayPath); !os.IsNotExist(err) {
		t.Fatalf("stray file stat err = %v, want removed", err)
	}
	if got := gitStatusForCompletionTest(t, repoPath); got != baselineStatus {
		t.Fatalf("repo status after restore = %q, want baseline %q", got, baselineStatus)
	}

	sidecar, err := agent.ReadProtocolRetrySidecar(fix.phaseDir)
	if err != nil {
		t.Fatalf("ReadProtocolRetrySidecar() error = %v", err)
	}
	if sidecar == nil || sidecar.Consecutive != 1 || !strings.Contains(sidecar.LastViolation, "modified target repo") {
		t.Fatalf("sidecar = %#v, want first repo mutation violation", sidecar)
	}
	if got := len(fix.runner.capturedByPhase(tc.phase)); got != 2 {
		t.Fatalf("starter captures for %s = %d, want initial start plus retry", tc.phase, got)
	}
	if events := drainEvents(fix.orchestrator); hasEventType(events, ports.PhaseCompleted) {
		t.Fatalf("PhaseCompleted emitted on retry: %#v", events)
	}
}

func TestOrchestrator_HandlePhaseCompletion_ArtifactPhaseRetryThenSuccessAdvances(t *testing.T) {
	for _, tc := range artifactPhaseCases() {
		t.Run(tc.name, func(t *testing.T) {
			fix := newArtifactPhaseRetryFixture(t, tc, retrySuccessCheckpointForTest(tc.phase))
			writePhaseComplete(t, fix.store.BaseDir, fix.feature, tc.phaseKey)

			if err := fix.orchestrator.HandlePhaseCompletion(fix.feature.ID, orchestrator.PhaseCompletionInput{
				Phase:   tc.phase,
				Success: true,
			}); err != nil {
				t.Fatalf("first HandlePhaseCompletion() error = %v", err)
			}
			drainEvents(fix.orchestrator)

			writePhaseComplete(t, fix.store.BaseDir, fix.feature, tc.phaseKey)
			artifactPath := writePhaseMarkdown(t, fix.store.BaseDir, fix.feature, tc.phaseKey, tc.phaseKey+".md")
			if err := fix.orchestrator.HandlePhaseCompletion(fix.feature.ID, orchestrator.PhaseCompletionInput{
				Phase:   tc.phase,
				Success: true,
			}); err != nil {
				t.Fatalf("second HandlePhaseCompletion() error = %v", err)
			}

			if _, err := agent.ReadProtocolRetrySidecar(fix.phaseDir); err != nil {
				t.Fatalf("ReadProtocolRetrySidecar() error = %v", err)
			}
			if _, err := os.Stat(filepath.Join(fix.phaseDir, agent.ProtocolRetrySidecarFile)); !os.IsNotExist(err) {
				t.Fatalf("sidecar stat err = %v, want removed", err)
			}

			reloaded := loadStoredFeature(t, fix.store, fix.feature.ID)
			if got := reloaded.Artifacts[tc.phaseKey]; got != artifactPath {
				t.Fatalf("Artifacts[%q] = %q, want %q", tc.phaseKey, got, artifactPath)
			}
			assertLifecycleCall(t, fix.lifecycle, "Complete"+strings.Title(tc.phaseKey))

			events := drainEvents(fix.orchestrator)
			if got := countPhaseCompletedEvents(events, tc.phase); got != 1 {
				t.Fatalf("PhaseCompleted events = %d, want 1; events=%#v", got, events)
			}
			if !hasEventType(events, ports.ReviewRequired) {
				t.Fatalf("ReviewRequired event missing after successful retry; events=%#v", events)
			}
		})
	}
}

func TestOrchestrator_HandlePhaseCompletion_ArtifactPhaseThirdViolationTerminates(t *testing.T) {
	for _, tc := range artifactPhaseCases() {
		t.Run(tc.name, func(t *testing.T) {
			fix := newArtifactPhaseRetryFixture(t, tc, feature.Checkpoints{})
			for attempt := 1; attempt <= agent.DefaultMaxConsecutiveProtocolViolations; attempt++ {
				if err := fix.orchestrator.HandlePhaseCompletion(fix.feature.ID, orchestrator.PhaseCompletionInput{
					Phase:   tc.phase,
					Success: true,
				}); err != nil {
					t.Fatalf("HandlePhaseCompletion(attempt %d) error = %v", attempt, err)
				}
			}

			reloaded := loadStoredFeature(t, fix.store, fix.feature.ID)
			if reloaded.FailureType != feature.FailureProtocolViolation {
				t.Fatalf("FailureType = %q, want %q", reloaded.FailureType, feature.FailureProtocolViolation)
			}
			role := artifactPhaseRoleForTest(t, tc.phase)
			_, contractViolations, err := agent.Validate(tc.phase, role, fix.phaseDir)
			if err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			violations := []agent.ProtocolViolation{{
				Artifact: agent.PhaseCompleteFile,
				Reason:   "SDK reported success but phase_complete was not present",
			}}
			violations = append(violations, contractViolations...)
			wantErr := agent.FormatSingleShotProtocolViolationError(role, fix.phaseDir, violations)
			if reloaded.LastError != wantErr {
				t.Fatalf("LastError = %q, want %q", reloaded.LastError, wantErr)
			}

			sidecar, err := agent.ReadProtocolRetrySidecar(fix.phaseDir)
			if err != nil {
				t.Fatalf("ReadProtocolRetrySidecar() error = %v", err)
			}
			if sidecar == nil || sidecar.Consecutive != agent.DefaultMaxConsecutiveProtocolViolations {
				t.Fatalf("sidecar = %#v, want consecutive %d", sidecar, agent.DefaultMaxConsecutiveProtocolViolations)
			}
			if got := len(fix.runner.capturedByPhase(tc.phase)); got != agent.DefaultMaxConsecutiveProtocolViolations-1 {
				t.Fatalf("starter captures for %s = %d, want %d", tc.phase, got, agent.DefaultMaxConsecutiveProtocolViolations-1)
			}
			events := drainEvents(fix.orchestrator)
			if got := countPhaseCompletedEvents(events, tc.phase); got != 1 {
				t.Fatalf("PhaseCompleted events = %d, want 1; events=%#v", got, events)
			}
		})
	}
}

func TestOrchestrator_HandlePhaseCompletion_ArtifactPhaseSidecarRestartRegression(t *testing.T) {
	for _, tc := range artifactPhaseCases() {
		t.Run(tc.name+"_survives_restart", func(t *testing.T) {
			fix := newArtifactPhaseRetryFixture(t, tc, feature.Checkpoints{})
			if err := agent.WriteProtocolRetrySidecar(fix.phaseDir, agent.ProtocolRetrySidecar{
				Role:          artifactPhaseRoleForTest(t, tc.phase),
				ActiveRun:     fix.feature.ActiveRun,
				Consecutive:   2,
				LastViolation: "prior violation",
				UpdatedAt:     time.Now().UTC(),
			}); err != nil {
				t.Fatalf("WriteProtocolRetrySidecar() error = %v", err)
			}

			if err := fix.orchestrator.HandlePhaseCompletion(fix.feature.ID, orchestrator.PhaseCompletionInput{
				Phase:   tc.phase,
				Success: true,
			}); err != nil {
				t.Fatalf("HandlePhaseCompletion() error = %v", err)
			}

			reloaded := loadStoredFeature(t, fix.store, fix.feature.ID)
			if reloaded.FailureType != feature.FailureProtocolViolation {
				t.Fatalf("FailureType = %q, want %q", reloaded.FailureType, feature.FailureProtocolViolation)
			}
		})

		t.Run(tc.name+"_stale_active_run_resets", func(t *testing.T) {
			fix := newArtifactPhaseRetryFixture(t, tc, feature.Checkpoints{})
			if err := agent.WriteProtocolRetrySidecar(fix.phaseDir, agent.ProtocolRetrySidecar{
				Role:          artifactPhaseRoleForTest(t, tc.phase),
				ActiveRun:     fix.feature.ActiveRun + 1,
				Consecutive:   2,
				LastViolation: "prior violation",
				UpdatedAt:     time.Now().UTC(),
			}); err != nil {
				t.Fatalf("WriteProtocolRetrySidecar() error = %v", err)
			}

			if err := fix.orchestrator.HandlePhaseCompletion(fix.feature.ID, orchestrator.PhaseCompletionInput{
				Phase:   tc.phase,
				Success: true,
			}); err != nil {
				t.Fatalf("HandlePhaseCompletion() error = %v", err)
			}

			reloaded := loadStoredFeature(t, fix.store, fix.feature.ID)
			if reloaded.Status != tc.status {
				t.Fatalf("Status = %s, want %s", reloaded.Status, tc.status)
			}
			sidecar, err := agent.ReadProtocolRetrySidecar(fix.phaseDir)
			if err != nil {
				t.Fatalf("ReadProtocolRetrySidecar() error = %v", err)
			}
			if sidecar == nil || sidecar.Consecutive != 1 || sidecar.ActiveRun != fix.feature.ActiveRun {
				t.Fatalf("sidecar = %#v, want fresh active_run=%d consecutive=1", sidecar, fix.feature.ActiveRun)
			}
		})
	}
}

func TestOrchestrator_HandlePhaseCompletion_ArtifactPhaseSessionCrashDoesNotReadSidecar(t *testing.T) {
	for _, tc := range artifactPhaseCases() {
		t.Run(tc.name, func(t *testing.T) {
			fix := newArtifactPhaseRetryFixture(t, tc, feature.Checkpoints{})
			if err := os.MkdirAll(fix.phaseDir, 0o755); err != nil {
				t.Fatalf("mkdir phase dir: %v", err)
			}
			if err := os.WriteFile(filepath.Join(fix.phaseDir, agent.ProtocolRetrySidecarFile), []byte("role: ["), 0o644); err != nil {
				t.Fatalf("write malformed sidecar: %v", err)
			}

			if err := fix.orchestrator.HandlePhaseCompletion(fix.feature.ID, orchestrator.PhaseCompletionInput{
				Phase:       tc.phase,
				Success:     false,
				ErrorDetail: "session crashed",
			}); err != nil {
				t.Fatalf("HandlePhaseCompletion() error = %v", err)
			}

			reloaded := loadStoredFeature(t, fix.store, fix.feature.ID)
			if reloaded.FailureType != feature.FailureSessionCrash {
				t.Fatalf("FailureType = %q, want %q", reloaded.FailureType, feature.FailureSessionCrash)
			}
			if got := len(fix.runner.capturedByPhase(tc.phase)); got != 0 {
				t.Fatalf("starter captures for %s = %d, want 0", tc.phase, got)
			}
		})
	}
}

func TestOrchestrator_HandlePhaseCompletion_ArtifactPhaseSuccessRecordsRegistryArtifact(t *testing.T) {
	for _, tc := range artifactPhaseCases() {
		t.Run(tc.name, func(t *testing.T) {
			o, f, store := newArtifactPhaseOrchestratorFixture(t, tc)
			stateDir := store.BaseDir
			writePhaseComplete(t, stateDir, f, tc.phaseKey)
			oldPath := writePhaseMarkdown(t, stateDir, f, tc.phaseKey, "old.md")
			newPath := writePhaseMarkdown(t, stateDir, f, tc.phaseKey, "new.md")
			setOlderModTime(t, oldPath)

			if err := o.HandlePhaseCompletion(f.ID, orchestrator.PhaseCompletionInput{
				Phase:   tc.phase,
				Success: true,
			}); err != nil {
				t.Fatalf("HandlePhaseCompletion() error = %v", err)
			}

			reloaded := loadStoredFeature(t, store, f.ID)
			if got := reloaded.Artifacts[tc.artifactKey]; got != newPath {
				t.Fatalf("Artifacts[%q] = %q, want %q", tc.artifactKey, got, newPath)
			}
		})
	}
}

// Inquire success: advances to next phase. Design would be the next
// phase for PipelineMoonshot (KB→Inquire→Research→Design), but on
// PipelineLarge (no design) it is Research.
func TestOrchestrator_HandlePhaseCompletion_Inquire_Success_Advances(t *testing.T) {
	stateDir := t.TempDir()
	f := &feature.Feature{
		ID:            "feat-inq",
		Status:        feature.StatusInquiring,
		CurrentPhase:  feature.PhaseInquire,
		Pipeline:      feature.PipelineLarge,
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	lc := lifecycleForFeature(f)
	// CompleteInquire moves to InquireReady (mirrors real manager.go:262).
	// StartResearch then transitions to Researching. Having CompleteInquire
	// skip straight to Researching would cause startResearch's idempotent
	// guard to bypass the StartResearch transition.
	lc.CompleteInquireFn = func(id string) error {
		f.Status = feature.StatusInquireReady
		return nil
	}
	lc.StartResearchFn = func(id string) error { f.Status = feature.StatusResearching; return nil }
	fs := feature.NewStore(stateDir)
	if err := fs.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}
	writePhaseComplete(t, stateDir, f, "inquire")
	writePhaseMarkdown(t, stateDir, f, "inquire", "inquire.md")

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
	}, orchestrator.Hooks{})

	if err := o.HandlePhaseCompletion("feat-inq", orchestrator.PhaseCompletionInput{
		Phase:   feature.PhaseInquire,
		Success: true,
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	assertLifecycleCall(t, lc, "CompleteInquire")
	assertLifecycleCall(t, lc, "StartResearch")

	events := drainEvents(o)
	if !hasEventType(events, ports.PhaseCompleted) {
		t.Error("expected PhaseCompleted event for Inquire")
	}
	if hasPhaseStarted(events, feature.PhaseResearch) == nil {
		t.Error("expected PhaseStarted event for Research after inquire completion")
	}
}

// Research success — for PipelineLarge, Research → Plan. We set
// Pipeline=Medium so the next phase is also Plan but is dispatchable (no
// plan artifacts required for medium).
func TestOrchestrator_HandlePhaseCompletion_Research_Success(t *testing.T) {
	stateDir := t.TempDir()
	f := &feature.Feature{
		ID:            "feat-res",
		Status:        feature.StatusResearching,
		CurrentPhase:  feature.PhaseResearch,
		Pipeline:      feature.PipelineMedium,
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	lc := lifecycleForFeature(f)
	lc.CompleteResearchFn = func(id string) error { return nil }
	lc.StartPlanningFn = func(id string) error { f.Status = feature.StatusPlanning; return nil }
	fs := feature.NewStore(stateDir)
	if err := fs.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}
	writePhaseComplete(t, stateDir, f, "research")
	writePhaseMarkdown(t, stateDir, f, "research", "research.md")

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
	}, orchestrator.Hooks{})

	if err := o.HandlePhaseCompletion("feat-res", orchestrator.PhaseCompletionInput{
		Phase:   feature.PhaseResearch,
		Success: true,
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	assertLifecycleCall(t, lc, "CompleteResearch")
}

// Artifact phase failure: emits PhaseCompleted(err) and FeatureFailed.
func TestOrchestrator_HandlePhaseCompletion_Inquire_Failure(t *testing.T) {
	f := &feature.Feature{
		ID:           "feat-inq-fail",
		Status:       feature.StatusInquiring,
		CurrentPhase: feature.PhaseInquire,
		Pipeline:     feature.PipelineLarge,
	}
	lc := lifecycleForFeature(f)
	lc.MarkFailedFn = func(id, ft, msg string) error { return nil }
	fs := newFeatureStore(f)

	var failureType string
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{
		OnFeatureFailed: func(id string, ft, em string) { failureType = ft },
	})

	if err := o.HandlePhaseCompletion("feat-inq-fail", orchestrator.PhaseCompletionInput{
		Phase:       feature.PhaseInquire,
		Success:     false,
		ErrorDetail: "inquire broke",
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	if failureType != feature.FailureSessionCrash {
		t.Errorf("failure type = %q, want %q", failureType, feature.FailureSessionCrash)
	}
	events := drainEvents(o)
	if !hasEventType(events, ports.FeatureFailed) {
		t.Error("expected FeatureFailed event on artifact phase failure")
	}
}

func TestOrchestrator_HandlePhaseCompletion_ArtifactPhaseFailureOnInterruptedFeature_DoesNotMarkFailed(t *testing.T) {
	for _, tc := range artifactPhaseCases() {
		t.Run(tc.name, func(t *testing.T) {
			f := &feature.Feature{
				ID:           "feat-int-" + tc.phaseKey,
				Status:       feature.StatusInterrupted,
				CurrentPhase: tc.phase,
				Pipeline:     feature.PipelineLarge,
			}
			lc := lifecycleForFeature(f)
			fs := newFeatureStore(f)
			o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{})

			if err := o.HandlePhaseCompletion(f.ID, orchestrator.PhaseCompletionInput{
				Phase:       tc.phase,
				Success:     false,
				ErrorDetail: "session stopped while agent was mid-turn",
			}); err != nil {
				t.Fatalf("HandlePhaseCompletion: %v", err)
			}

			refuteLifecycleCall(t, lc, "MarkFailed")
			events := drainEvents(o)
			if hasEventType(events, ports.FeatureFailed) {
				t.Fatalf("unexpected FeatureFailed event for interrupted %s completion: %#v", tc.phase, events)
			}
			if hasEventType(events, ports.PhaseCompleted) {
				t.Fatalf("unexpected PhaseCompleted event for interrupted %s completion: %#v", tc.phase, events)
			}
			if f.Status != feature.StatusInterrupted {
				t.Fatalf("Status = %s, want Interrupted", f.Status)
			}
		})
	}
}

// Artifact phase terminal-state guard.
func TestOrchestrator_HandlePhaseCompletion_Inquire_Terminal(t *testing.T) {
	f := &feature.Feature{
		ID:           "feat-inq-zmb",
		Status:       feature.StatusInterrupted,
		CurrentPhase: feature.PhaseInquire,
		Pipeline:     feature.PipelineLarge,
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{})

	if err := o.HandlePhaseCompletion("feat-inq-zmb", orchestrator.PhaseCompletionInput{
		Phase:   feature.PhaseInquire,
		Success: true,
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	refuteLifecycleCall(t, lc, "CompleteInquire")
}

// ---------------------------------------------------------------------------
// Category C — Plan loop completion
// ---------------------------------------------------------------------------

func TestOrchestrator_HandlePhaseCompletion_Plan_RoadmapApproved_RoadmapGate(t *testing.T) {
	f := &feature.Feature{
		ID:                  "feat-roadmap-gate",
		Status:              feature.StatusPlanning,
		CurrentPhase:        feature.PhasePlan,
		Pipeline:            feature.PipelineMedium,
		CurrentRoadmapPhase: 0,
		Checkpoints:         feature.Checkpoints{RoadmapReview: true},
	}
	lc := lifecycleForFeature(f)
	lc.NeedsPlanReviewFn = func(id string) error {
		f.Status = feature.StatusPlanNeedsReview
		return nil
	}
	fs := newFeatureStore(f)

	var reviewPhase feature.Phase
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{
		OnReviewRequired: func(id string, p feature.Phase) { reviewPhase = p },
	})

	if err := o.HandlePhaseCompletion(f.ID, orchestrator.PhaseCompletionInput{
		Phase:      feature.PhasePlan,
		PlanResult: &agent.PlanLoopResult{FinalStatus: "approved"},
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	assertLifecycleCall(t, lc, "NeedsPlanReview")
	refuteLifecycleCall(t, lc, "AdvanceRoadmapPhase")
	if reviewPhase != feature.PhasePlan {
		t.Errorf("OnReviewRequired phase = %v, want PhasePlan", reviewPhase)
	}
	events := drainEvents(o)
	if !hasEventType(events, ports.ReviewRequired) {
		t.Error("expected ReviewRequired event")
	}
}

func TestOrchestrator_HandlePhaseCompletion_Plan_PhasePlanApproved_PhasePlanGate(t *testing.T) {
	f := &feature.Feature{
		ID:                  "feat-phase-plan-gate",
		Status:              feature.StatusPlanning,
		CurrentPhase:        feature.PhasePlan,
		Pipeline:            feature.PipelineLarge,
		CurrentRoadmapPhase: 2,
		TotalRoadmapPhases:  3,
		Checkpoints:         feature.Checkpoints{PhasePlanReview: true},
	}
	lc := lifecycleForFeature(f)
	lc.NeedsPlanReviewFn = func(id string) error {
		f.Status = feature.StatusPlanNeedsReview
		return nil
	}
	fs := newFeatureStore(f)

	var reviewPhase feature.Phase
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{
		OnReviewRequired: func(id string, p feature.Phase) { reviewPhase = p },
	})

	if err := o.HandlePhaseCompletion(f.ID, orchestrator.PhaseCompletionInput{
		Phase:      feature.PhasePlan,
		PlanResult: &agent.PlanLoopResult{FinalStatus: "approved"},
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	assertLifecycleCall(t, lc, "NeedsPlanReview")
	refuteLifecycleCall(t, lc, "StartRoadmapPhaseImplementation")
	if reviewPhase != feature.PhasePlan {
		t.Errorf("OnReviewRequired phase = %v, want PhasePlan", reviewPhase)
	}
	events := drainEvents(o)
	if !hasEventType(events, ports.ReviewRequired) {
		t.Error("expected ReviewRequired event")
	}
}

func TestOrchestrator_HandlePhaseCompletion_Plan_RevivesFailedPhasePlanRetry(t *testing.T) {
	f := &feature.Feature{
		ID:                  "feat-phase-plan-retry",
		Status:              feature.StatusFailed,
		CurrentPhase:        feature.PhasePlan,
		Pipeline:            feature.PipelineLarge,
		CurrentRoadmapPhase: 1,
		TotalRoadmapPhases:  2,
		FailureType:         feature.FailureInfrastructure,
		LastError:           "phase plan session did not complete",
		Checkpoints:         feature.Checkpoints{PhasePlanReview: true},
	}
	lc := lifecycleForFeature(f)
	lc.NeedsPlanReviewFn = func(id string) error {
		if f.Status != feature.StatusPlanning {
			t.Fatalf("NeedsPlanReview called from status %s, want Planning", f.Status)
		}
		f.Status = feature.StatusPlanNeedsReview
		return nil
	}
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{})
	if err := o.HandlePhaseCompletion(f.ID, orchestrator.PhaseCompletionInput{
		Phase:      feature.PhasePlan,
		PlanResult: &agent.PlanLoopResult{FinalStatus: "approved"},
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	assertLifecycleCall(t, lc, "NeedsPlanReview")
	if f.Status != feature.StatusPlanNeedsReview {
		t.Fatalf("Status = %s, want PlanNeedsReview", f.Status)
	}
	if f.FailureType != "" || f.LastError != "" {
		t.Fatalf("failure fields = %q/%q, want cleared", f.FailureType, f.LastError)
	}
}

func TestOrchestrator_HandlePhaseCompletion_Plan_DuplicateReviewGateIsIdempotent(t *testing.T) {
	f := &feature.Feature{
		ID:                  "feat-phase-plan-duplicate-review",
		Status:              feature.StatusPlanNeedsReview,
		CurrentPhase:        feature.PhasePlan,
		Pipeline:            feature.PipelineLarge,
		CurrentRoadmapPhase: 1,
		TotalRoadmapPhases:  2,
		Checkpoints:         feature.Checkpoints{PhasePlanReview: true},
	}
	lc := lifecycleForFeature(f)
	lc.NeedsPlanReviewFn = func(id string) error {
		t.Fatal("NeedsPlanReview should not be called for an existing PlanNeedsReview gate")
		return nil
	}
	fs := newFeatureStore(f)

	var reviewPhase feature.Phase
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{
		OnReviewRequired: func(id string, p feature.Phase) { reviewPhase = p },
	})
	if err := o.HandlePhaseCompletion(f.ID, orchestrator.PhaseCompletionInput{
		Phase:      feature.PhasePlan,
		PlanResult: &agent.PlanLoopResult{FinalStatus: "approved"},
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	if f.Status != feature.StatusPlanNeedsReview {
		t.Fatalf("Status = %s, want PlanNeedsReview", f.Status)
	}
	if reviewPhase != feature.PhasePlan {
		t.Fatalf("OnReviewRequired phase = %v, want PhasePlan", reviewPhase)
	}
	events := drainEvents(o)
	if !hasEventType(events, ports.ReviewRequired) {
		t.Error("expected ReviewRequired event")
	}
}

func TestOrchestrator_HandlePhaseCompletion_Plan_PromotesRevisedPhasePlanArtifact(t *testing.T) {
	tmpStateDir := t.TempDir()
	f := &feature.Feature{
		ID:                  "feat-phase-plan-promote",
		Status:              feature.StatusPlanning,
		CurrentPhase:        feature.PhasePlan,
		Pipeline:            feature.PipelineLarge,
		CurrentRoadmapPhase: 2,
		TotalRoadmapPhases:  2,
		ActiveRun:           1,
		RunCount:            1,
		Checkpoints:         feature.Checkpoints{PhasePlanReview: true},
	}
	phasePlanDir := agent.PhasePlanDir(tmpStateDir, f, 2)
	if err := os.MkdirAll(filepath.Join(phasePlanDir, "attempt-02"), 0o755); err != nil {
		t.Fatalf("mkdir attempt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(phasePlanDir, "phase-plan.md"), []byte("old pedregal plan"), 0o644); err != nil {
		t.Fatalf("write old plan: %v", err)
	}
	revised := []byte("revised nucleus-only plan")
	if err := os.WriteFile(filepath.Join(phasePlanDir, "attempt-02", "phase-plan.md"), revised, 0o644); err != nil {
		t.Fatalf("write revised plan: %v", err)
	}

	lc := lifecycleForFeature(f)
	lc.NeedsPlanReviewFn = func(id string) error {
		f.Status = feature.StatusPlanNeedsReview
		return nil
	}
	fs := newFeatureStore(f)
	pr := &agent.PhaseRunner{StateDir: tmpStateDir}

	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs, PhaseRunner: pr}, orchestrator.Hooks{})
	if err := o.HandlePhaseCompletion(f.ID, orchestrator.PhaseCompletionInput{
		Phase:      feature.PhasePlan,
		PlanResult: &agent.PlanLoopResult{FinalStatus: "approved", Iterations: 2},
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(phasePlanDir, "phase-plan.md"))
	if err != nil {
		t.Fatalf("read canonical phase plan: %v", err)
	}
	if string(data) != string(revised) {
		t.Fatalf("canonical phase plan = %q, want revised %q", data, revised)
	}
}

// Plan needs_human_review always raises a review gate, even when
// planning review gates are disabled. The validator's escalation is an
// exception path — failing the feature would discard a working plan
// over a question the user can resolve in seconds.
func TestOrchestrator_HandlePhaseCompletion_Plan_NeedsReview_NoGate_RaisesReview(t *testing.T) {
	f := &feature.Feature{
		ID:           "feat-plan-nr",
		Status:       feature.StatusPlanning,
		CurrentPhase: feature.PhasePlan,
		Pipeline:     feature.PipelineMedium,
		// RoadmapReview and PhasePlanReview deliberately left false.
	}
	lc := lifecycleForFeature(f)
	lc.NeedsPlanReviewFn = func(id string) error {
		f.Status = feature.StatusPlanNeedsReview
		return nil
	}
	lc.MarkFailedFn = func(id, ft, msg string) error {
		t.Fatalf("MarkFailed must not be called; needs_human_review must always open a gate, got ft=%q msg=%q", ft, msg)
		return nil
	}
	fs := newFeatureStore(f)

	var reviewPhase feature.Phase
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{
		OnReviewRequired: func(id string, p feature.Phase) { reviewPhase = p },
		OnFeatureFailed: func(id string, ft, em string) {
			t.Fatalf("OnFeatureFailed must not fire; needs_human_review must always open a gate, got ft=%q em=%q", ft, em)
		},
	})

	if err := o.HandlePhaseCompletion("feat-plan-nr", orchestrator.PhaseCompletionInput{
		Phase:      feature.PhasePlan,
		PlanResult: &agent.PlanLoopResult{FinalStatus: "needs_human_review"},
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	assertLifecycleCall(t, lc, "NeedsPlanReview")
	if reviewPhase != feature.PhasePlan {
		t.Errorf("OnReviewRequired phase = %v, want PhasePlan", reviewPhase)
	}
	events := drainEvents(o)
	if !hasEventType(events, ports.ReviewRequired) {
		t.Error("expected ReviewRequired event")
	}
}

// Plan failed → markFailedWithEvent(FailureInfrastructure).
func TestOrchestrator_HandlePhaseCompletion_Plan_Failed(t *testing.T) {
	f := &feature.Feature{
		ID:           "feat-plan-err",
		Status:       feature.StatusPlanning,
		CurrentPhase: feature.PhasePlan,
		Pipeline:     feature.PipelineMedium,
	}
	lc := lifecycleForFeature(f)
	lc.MarkFailedFn = func(id, ft, msg string) error { return nil }
	fs := newFeatureStore(f)

	var failureType, errMsg string
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{
		OnFeatureFailed: func(id string, ft, em string) {
			failureType = ft
			errMsg = em
		},
	})

	if err := o.HandlePhaseCompletion("feat-plan-err", orchestrator.PhaseCompletionInput{
		Phase: feature.PhasePlan,
		PlanResult: &agent.PlanLoopResult{
			FinalStatus: finalStatusFailed,
			LastError:   "validator exploded",
		},
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	if failureType != feature.FailureInfrastructure {
		t.Errorf("failure type = %q, want %q", failureType, feature.FailureInfrastructure)
	}
	if errMsg != "validator exploded" {
		t.Errorf("error message = %q, want %q", errMsg, "validator exploded")
	}
}

func TestOrchestrator_HandlePhaseCompletion_Plan_ProtocolViolation(t *testing.T) {
	f := &feature.Feature{
		ID:           "feat-plan-protocol",
		Status:       feature.StatusPlanning,
		CurrentPhase: feature.PhasePlan,
		Pipeline:     feature.PipelineMedium,
	}
	lc := lifecycleForFeature(f)
	lc.MarkFailedFn = func(id, ft, msg string) error { return nil }
	fs := newFeatureStore(f)

	var failureType, errMsg string
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{
		OnFeatureFailed: func(id string, ft, em string) {
			failureType = ft
			errMsg = em
		},
	})

	if err := o.HandlePhaseCompletion("feat-plan-protocol", orchestrator.PhaseCompletionInput{
		Phase: feature.PhasePlan,
		PlanResult: &agent.PlanLoopResult{
			FinalStatus: "protocol_violation",
			LastError:   "protocol violation: plan_phase_planner @ /tmp/attempt-01: phase plan markdown is missing",
		},
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion() error = %v", err)
	}

	if failureType != feature.FailureProtocolViolation {
		t.Errorf("failure type = %q, want %q", failureType, feature.FailureProtocolViolation)
	}
	if !strings.Contains(errMsg, "plan_phase_planner") {
		t.Errorf("error message = %q, want planner role", errMsg)
	}
}

// Plan interrupted is a no-op.
func TestOrchestrator_HandlePhaseCompletion_Plan_Interrupted_NoOp(t *testing.T) {
	f := &feature.Feature{
		ID:           "feat-plan-int",
		Status:       feature.StatusPlanning,
		CurrentPhase: feature.PhasePlan,
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{})

	if err := o.HandlePhaseCompletion("feat-plan-int", orchestrator.PhaseCompletionInput{
		Phase:      feature.PhasePlan,
		PlanResult: &agent.PlanLoopResult{FinalStatus: finalStatusInterrupted},
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}
	refuteLifecycleCall(t, lc, "CompletePlanning")
	refuteLifecycleCall(t, lc, "MarkFailed")
}

// Plan result is nil → markFailedWithEvent(FailureInfrastructure).
func TestOrchestrator_HandlePhaseCompletion_Plan_NilResult_Fails(t *testing.T) {
	f := &feature.Feature{
		ID:           "feat-plan-nil",
		Status:       feature.StatusPlanning,
		CurrentPhase: feature.PhasePlan,
	}
	lc := lifecycleForFeature(f)
	lc.MarkFailedFn = func(id, ft, msg string) error { return nil }
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{})

	if err := o.HandlePhaseCompletion("feat-plan-nil", orchestrator.PhaseCompletionInput{
		Phase:      feature.PhasePlan,
		PlanResult: nil,
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}
	assertLifecycleCall(t, lc, "MarkFailed")
}

// Multi-repo all_passed non-roadmap, non-publishable → CompleteImplementation
// + MarkCodeReady + no TryCompletePublish (we're not publishable).
func TestOrchestrator_HandlePhaseCompletion_Implement_Multi_AllPassed_NotPublishable(t *testing.T) {
	unpub := false
	f := &feature.Feature{
		ID:           "feat-multi",
		Status:       feature.StatusImplementing,
		CurrentPhase: feature.PhaseImplement,
		Repos: []feature.FeatureRepo{
			{Name: repoName, Path: repoAPath, Publishable: &unpub},
			{Name: repoNameB, Path: repoBPath, Publishable: &unpub},
		},
	}
	lc := lifecycleForFeature(f)
	lc.CompleteImplementationFn = func(id string) error { return nil }
	lc.MarkCodeReadyFn = func(id string) error { return nil }
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{})

	if err := o.HandlePhaseCompletion("feat-multi", orchestrator.PhaseCompletionInput{
		Phase:           feature.PhaseImplement,
		MultiRepoResult: &agent.OrchestratorResult{FinalStatus: "all_passed"},
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	assertLifecycleCall(t, lc, "CompleteImplementation")
	assertLifecycleCall(t, lc, "MarkCodeReady")
	refuteLifecycleCall(t, lc, "TryCompletePublish")
}

// Multi-repo all_passed, publishable, auto-publish on (>1 repos, non-roadmap)
// → tryCompleteAndEmit fires (which calls TryCompletePublish). When
// TryCompletePublish returns (false, nil) — the common "not all repos published
// yet" resume case — the handler MUST fall back to MarkCodeReady so Init() and
// StartPhaseMsg can continue the remaining repo publishes on restart. Mirrors
// app.go:3761-3774.
func TestOrchestrator_HandlePhaseCompletion_Implement_Multi_AllPassed_AutoPublish_PartialPublishFallsBackToCodeReady(t *testing.T) {
	f := &feature.Feature{
		ID:           "feat-multi-ap",
		Status:       feature.StatusImplementing,
		CurrentPhase: feature.PhaseImplement,
		Repos: []feature.FeatureRepo{
			{Name: repoName, Path: repoAPath},
			{Name: repoNameB, Path: repoBPath},
		},
	}
	lc := lifecycleForFeature(f)
	lc.CompleteImplementationFn = func(id string) error { return nil }
	lc.TryCompletePublishFn = func(id string) (bool, error) { return false, nil }
	lc.MarkCodeReadyFn = func(id string) error { return nil }
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{})

	if err := o.HandlePhaseCompletion("feat-multi-ap", orchestrator.PhaseCompletionInput{
		Phase:           feature.PhaseImplement,
		MultiRepoResult: &agent.OrchestratorResult{FinalStatus: "all_passed"},
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	assertLifecycleCall(t, lc, "CompleteImplementation")
	assertLifecycleCall(t, lc, "TryCompletePublish")
	// The partial-publish fallback — without MarkCodeReady a crashed feature
	// would stay at StatusReviewPassed and resume paths that dispatch on
	// StatusCodeReady would never fire again.
	assertLifecycleCall(t, lc, "MarkCodeReady")
	// Multi-repo (>1) non-roadmap path does NOT route through Final Review.
	refuteLifecycleCall(t, lc, "StartFinalReview")
}

// Multi-repo all_passed, publishable, auto-publish on (>1 repos, non-roadmap)
// with TryCompletePublish → (true, nil) — the fully-published case. Asserts
// the handler does NOT call MarkCodeReady, because the feature just
// transitioned to StatusPublished and MarkCodeReady would regress it.
func TestOrchestrator_HandlePhaseCompletion_Implement_Multi_AllPassed_AutoPublish_FullyPublishedSkipsCodeReady(t *testing.T) {
	f := &feature.Feature{
		ID:           "feat-multi-ap-full",
		Status:       feature.StatusImplementing,
		CurrentPhase: feature.PhaseImplement,
		Repos: []feature.FeatureRepo{
			{Name: repoName, Path: repoAPath},
			{Name: repoNameB, Path: repoBPath},
		},
	}
	lc := lifecycleForFeature(f)
	lc.CompleteImplementationFn = func(id string) error { return nil }
	lc.TryCompletePublishFn = func(id string) (bool, error) { return true, nil }
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{})

	if err := o.HandlePhaseCompletion("feat-multi-ap-full", orchestrator.PhaseCompletionInput{
		Phase:           feature.PhaseImplement,
		MultiRepoResult: &agent.OrchestratorResult{FinalStatus: "all_passed"},
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	assertLifecycleCall(t, lc, "TryCompletePublish")
	// When TryCompletePublish → true the feature is at StatusPublished —
	// MarkCodeReady would regress it.
	refuteLifecycleCall(t, lc, "MarkCodeReady")
}

// Multi-repo requires StatusImplementing — otherwise no-op.
func TestOrchestrator_HandlePhaseCompletion_Implement_Multi_WrongStatus_NoOp(t *testing.T) {
	f := &feature.Feature{
		ID:           "feat-multi-bad",
		Status:       feature.StatusInterrupted,
		CurrentPhase: feature.PhaseImplement,
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{})

	if err := o.HandlePhaseCompletion("feat-multi-bad", orchestrator.PhaseCompletionInput{
		Phase:           feature.PhaseImplement,
		MultiRepoResult: &agent.OrchestratorResult{FinalStatus: "all_passed"},
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	refuteLifecycleCall(t, lc, "CompleteImplementation")
}

// Multi-repo failed → MarkFailed(infrastructure).
func TestOrchestrator_HandlePhaseCompletion_Implement_Multi_Failed(t *testing.T) {
	f := &feature.Feature{
		ID:           "feat-multi-err",
		Status:       feature.StatusImplementing,
		CurrentPhase: feature.PhaseImplement,
	}
	lc := lifecycleForFeature(f)
	lc.MarkFailedFn = func(id, ft, msg string) error { return nil }
	lc.ClearAddressingReviewsFn = func(id string) error { return nil }
	fs := newFeatureStore(f)

	var failureType string
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{
		OnFeatureFailed: func(id string, ft, em string) { failureType = ft },
	})

	if err := o.HandlePhaseCompletion("feat-multi-err", orchestrator.PhaseCompletionInput{
		Phase: feature.PhaseImplement,
		MultiRepoResult: &agent.OrchestratorResult{
			FinalStatus: finalStatusFailed,
			LastError:   "multi-repo blew up",
		},
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	if failureType != feature.FailureInfrastructure {
		t.Errorf("failure type = %q, want %q", failureType, feature.FailureInfrastructure)
	}
}

func TestOrchestrator_HandlePhaseCompletion_Implement_MissingEvidenceRoutesPhasePlanRevision(t *testing.T) {
	cpr := newCapturingPhaseRunner(t)
	cpr.sm.StartSessionFn = func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*session.SessionOpts) (ports.SessionHandle, error) {
		return nil, session.ErrShuttingDown
	}
	tmpStateDir := cpr.stateDir
	featureID := "feat-impl-missing-evidence"
	roadmapPath := filepath.Join(tmpStateDir, "roadmap.md")
	roadmap := "# Roadmap\n\n## Phase 1: Bootstrap\n### Goal\nInit\n\n## Phase 2: Build\n### Goal\nBuild\n"
	if err := os.WriteFile(roadmapPath, []byte(roadmap), 0o644); err != nil {
		t.Fatalf("write roadmap: %v", err)
	}
	f := &feature.Feature{
		ID:                  featureID,
		Status:              feature.StatusImplementing,
		Pipeline:            feature.PipelineLarge,
		ActiveRun:           1,
		RunCount:            1,
		CurrentRoadmapPhase: 1,
		TotalRoadmapPhases:  2,
		Artifacts:           map[string]string{"roadmap": roadmapPath},
		Repos: []feature.FeatureRepo{
			{Name: "app", Path: "/tmp/app"},
		},
	}
	phasePlanDir := agent.PhasePlanDir(tmpStateDir, f, 1)
	if err := os.MkdirAll(phasePlanDir, 0o755); err != nil {
		t.Fatalf("mkdir phase plan dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(phasePlanDir, "phase-plan.md"), []byte("# Phase 1 plan"), 0o644); err != nil {
		t.Fatalf("write phase plan: %v", err)
	}
	if err := agent.WritePlanAttemptMeta(phasePlanDir, agent.PlanAttemptMeta{
		Attempt:      1,
		AgentStatus:  "SUCCESS",
		ReviewStatus: "APPROVED",
	}); err != nil {
		t.Fatalf("seed approved phase plan attempt: %v", err)
	}

	lc := lifecycleForFeature(f)
	lc.NeedsPlanReviewFn = func(id string) error {
		f.Status = feature.StatusPlanNeedsReview
		return nil
	}
	lc.StartPlanningFn = func(id string) error {
		f.Status = feature.StatusPlanning
		return nil
	}
	fs := newFeatureStore(f)
	cpr.pr.FeatureStore = fs
	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
		Store:       fs,
		Sessions:    cpr.sm,
		PhaseRunner: cpr.pr,
		CmdRunner:   cpr.cmd,
	}, orchestrator.Hooks{})

	feedback := "- **Critical**: MISSING_EVIDENCE_REQUIREMENT visual: Capture the setup wizard empty state."
	if err := o.HandlePhaseCompletion(featureID, orchestrator.PhaseCompletionInput{
		Phase: feature.PhaseImplement,
		MultiRepoResult: &agent.OrchestratorResult{
			FinalStatus:          "plan_revision_required",
			PlanRevisionFeedback: feedback,
		},
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion() error = %v", err)
	}

	if f.Status != feature.StatusPlanning {
		t.Fatalf("feature status = %v, want Planning after missing-evidence plan repair dispatch", f.Status)
	}
	if captured := waitForCapturedPhase(t, cpr, feature.PhasePlan, 3*time.Second); len(captured) == 0 {
		t.Fatalf("no phase-plan revision session captured; captures: %+v", cpr.capturedOpts)
	}
	events := drainEvents(o)
	sawAdvance := false
	for _, ev := range events {
		if ev.Type == ports.FeatureAdvanced && ev.Phase == feature.PhasePlan {
			sawAdvance = true
		}
	}
	if !sawAdvance {
		t.Fatalf("expected FeatureAdvanced(PhasePlan) after missing-evidence plan repair dispatch; events: %+v", events)
	}
	data, err := os.ReadFile(filepath.Join(phasePlanDir, "attempt-01", "validation-feedback.md"))
	if err != nil {
		t.Fatalf("read validation feedback: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "MISSING_EVIDENCE_REQUIREMENT visual: Capture the setup wizard empty state.") {
		t.Errorf("validation feedback missing reviewer-authored requirement:\n%s", got)
	}
	if latest := agent.LatestCompletedPlanAttempt(phasePlanDir); latest != 0 {
		t.Errorf("LatestCompletedPlanAttempt() = %d, want 0 after invalidating approved phase plan", latest)
	}
}

func TestOrchestrator_HandlePhaseCompletion_Implement_MissingEvidenceUsesLatestInvalidatedAttemptFeedback(t *testing.T) {
	cpr := newCapturingPhaseRunner(t)
	cpr.sm.StartSessionFn = func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*session.SessionOpts) (ports.SessionHandle, error) {
		return nil, session.ErrShuttingDown
	}
	featureID := "feat-impl-missing-evidence-attempt-02"
	roadmapPath := filepath.Join(cpr.stateDir, "roadmap.md")
	writeRoadmap(t, roadmapPath)
	f := &feature.Feature{
		ID:                  featureID,
		Status:              feature.StatusImplementing,
		Pipeline:            feature.PipelineLarge,
		ActiveRun:           1,
		RunCount:            1,
		CurrentRoadmapPhase: 1,
		TotalRoadmapPhases:  2,
		Artifacts:           map[string]string{"roadmap": roadmapPath},
		Repos:               []feature.FeatureRepo{{Name: "app", Path: cpr.stateDir}},
	}
	phasePlanDir := agent.PhasePlanDir(cpr.stateDir, f, 1)
	if err := os.MkdirAll(filepath.Join(phasePlanDir, "attempt-01"), 0o755); err != nil {
		t.Fatalf("mkdir attempt-01: %v", err)
	}
	if err := os.WriteFile(filepath.Join(phasePlanDir, "plan.md"), []byte("# Phase 1 plan"), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	if err := os.WriteFile(filepath.Join(phasePlanDir, "attempt-01", "validation-feedback.md"), []byte("stale validator feedback from attempt 01"), 0o644); err != nil {
		t.Fatalf("write stale feedback: %v", err)
	}
	if err := agent.WritePlanAttemptMeta(phasePlanDir, agent.PlanAttemptMeta{
		Attempt:      1,
		AgentStatus:  "SUCCESS",
		ReviewStatus: "CHANGES_REQUESTED",
	}); err != nil {
		t.Fatalf("seed rejected attempt 01: %v", err)
	}
	if err := agent.WritePlanAttemptMeta(phasePlanDir, agent.PlanAttemptMeta{
		Attempt:      2,
		AgentStatus:  "SUCCESS",
		ReviewStatus: "APPROVED",
	}); err != nil {
		t.Fatalf("seed approved attempt 02: %v", err)
	}

	lc := lifecycleForFeature(f)
	lc.NeedsPlanReviewFn = func(id string) error {
		f.Status = feature.StatusPlanNeedsReview
		return nil
	}
	lc.StartPlanningFn = func(id string) error {
		f.Status = feature.StatusPlanning
		return nil
	}
	fs := newFeatureStore(f)
	cpr.pr.FeatureStore = fs
	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
		Store:       fs,
		Sessions:    cpr.sm,
		PhaseRunner: cpr.pr,
		CmdRunner:   cpr.cmd,
	}, orchestrator.Hooks{})

	feedback := "- **Critical**: MISSING_EVIDENCE_REQUIREMENT visual: Capture the approved attempt 02 setup wizard."
	if err := o.HandlePhaseCompletion(featureID, orchestrator.PhaseCompletionInput{
		Phase: feature.PhaseImplement,
		MultiRepoResult: &agent.OrchestratorResult{
			FinalStatus:          "plan_revision_required",
			PlanRevisionFeedback: feedback,
		},
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion() error = %v", err)
	}

	captured := waitForCapturedPhase(t, cpr, feature.PhasePlan, 3*time.Second)
	if len(captured) == 0 {
		t.Fatalf("no phase-plan revision session captured; captures: %+v", cpr.capturedOpts)
	}
	prompt := captured[0].Prompt
	if !strings.Contains(prompt, "MISSING_EVIDENCE_REQUIREMENT visual: Capture the approved attempt 02 setup wizard.") {
		t.Fatalf("phase-plan revision prompt missing latest missing-evidence feedback:\n%s", prompt)
	}
	if strings.Contains(prompt, "stale validator feedback from attempt 01") {
		t.Fatalf("phase-plan revision prompt used stale attempt-01 feedback:\n%s", prompt)
	}
}

// Multi-repo failed with at least one repo reporting FinalStatus="max_iterations"
// must surface FailureMaxIterations (not FailureInfrastructure). The restart
// handler in the TUI (restartPhaseCmd at app.go:8941) only grows the iteration
// budget when FailureType == FailureMaxIterations — losing that signal would
// silently revert to the default cap on resume. Regression guard for the
// fix in completion.go:onMultiRepoImplementDone's "failed" branch.
func TestOrchestrator_HandlePhaseCompletion_Implement_Multi_Failed_MaxIterations_Preserved(t *testing.T) {
	f := &feature.Feature{
		ID:           "feat-multi-maxit",
		Status:       feature.StatusImplementing,
		CurrentPhase: feature.PhaseImplement,
		Repos: []feature.FeatureRepo{
			{Name: repoName, Path: repoAPath},
			{Name: repoNameB, Path: repoBPath},
		},
	}
	lc := lifecycleForFeature(f)
	lc.MarkFailedFn = func(id, ft, msg string) error { return nil }
	lc.ClearAddressingReviewsFn = func(id string) error { return nil }
	fs := newFeatureStore(f)

	var failureType string
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{
		OnFeatureFailed: func(id string, ft, em string) { failureType = ft },
	})

	if err := o.HandlePhaseCompletion("feat-multi-maxit", orchestrator.PhaseCompletionInput{
		Phase: feature.PhaseImplement,
		MultiRepoResult: &agent.OrchestratorResult{
			FinalStatus: finalStatusFailed,
			LastError:   "one repo hit iteration cap",
			RepoStatuses: map[string]string{
				repoName: "max_iterations",
				repoNameB: finalStatusFailed,
			},
		},
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	if failureType != feature.FailureMaxIterations {
		t.Errorf("failure type = %q, want %q (single repo max_iterations must propagate)", failureType, feature.FailureMaxIterations)
	}
}

func TestOrchestrator_HandlePhaseCompletion_Implement_Multi_Failed_ProtocolViolation_Preserved(t *testing.T) {
	f := &feature.Feature{
		ID:           "feat-multi-protocol",
		Status:       feature.StatusImplementing,
		CurrentPhase: feature.PhaseImplement,
		Repos: []feature.FeatureRepo{
			{Name: repoName, Path: repoAPath},
		},
	}
	lc := lifecycleForFeature(f)
	lc.MarkFailedFn = func(id, ft, msg string) error { return nil }
	lc.ClearAddressingReviewsFn = func(id string) error { return nil }
	fs := newFeatureStore(f)

	var failureType string
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{
		OnFeatureFailed: func(id string, ft string, em string) { failureType = ft },
	})

	if err := o.HandlePhaseCompletion("feat-multi-protocol", orchestrator.PhaseCompletionInput{
		Phase: feature.PhaseImplement,
		MultiRepoResult: &agent.OrchestratorResult{
			FinalStatus: finalStatusFailed,
			LastError:   "protocol violation: implementer @ /tmp/iter: progress.md is missing",
			RepoStatuses: map[string]string{
				repoName: "protocol_violation",
			},
		},
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion() error = %v", err)
	}
	if failureType != feature.FailureProtocolViolation {
		t.Fatalf("failure type = %q, want %q", failureType, feature.FailureProtocolViolation)
	}
}

func TestOrchestrator_HandlePhaseCompletion_Implement_Multi_Failed_SafetyRail_Preserved(t *testing.T) {
	f := &feature.Feature{
		ID:           "feat-multi-safety-rail",
		Status:       feature.StatusImplementing,
		CurrentPhase: feature.PhaseImplement,
		Repos: []feature.FeatureRepo{
			{Name: repoName, Path: repoAPath},
		},
	}
	lc := lifecycleForFeature(f)
	lc.MarkFailedFn = func(id, ft, msg string) error { return nil }
	lc.ClearAddressingReviewsFn = func(id string) error { return nil }
	fs := newFeatureStore(f)

	var failureType string
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{
		OnFeatureFailed: func(id string, ft string, em string) { failureType = ft },
	})

	if err := o.HandlePhaseCompletion("feat-multi-safety-rail", orchestrator.PhaseCompletionInput{
		Phase: feature.PhaseImplement,
		MultiRepoResult: &agent.OrchestratorResult{
			FinalStatus: finalStatusFailed,
			LastError:   "3 consecutive agent failures",
			RepoStatuses: map[string]string{
				repoName: "safety_rail",
			},
		},
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion() error = %v", err)
	}
	if failureType != feature.FailureSafetyRail {
		t.Fatalf("failure type = %q, want %q", failureType, feature.FailureSafetyRail)
	}
}

// Multi-repo failed without any max_iterations entries keeps FailureInfrastructure
// as the default. Companion to the max_iterations test — proves the status
// scan does not over-trigger.
func TestOrchestrator_HandlePhaseCompletion_Implement_Multi_Failed_NoMaxIterations_DefaultsToInfra(t *testing.T) {
	f := &feature.Feature{
		ID:           "feat-multi-infra",
		Status:       feature.StatusImplementing,
		CurrentPhase: feature.PhaseImplement,
		Repos: []feature.FeatureRepo{
			{Name: repoName, Path: repoAPath},
		},
	}
	lc := lifecycleForFeature(f)
	lc.MarkFailedFn = func(id, ft, msg string) error { return nil }
	lc.ClearAddressingReviewsFn = func(id string) error { return nil }
	fs := newFeatureStore(f)

	var failureType string
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{
		OnFeatureFailed: func(id string, ft, em string) { failureType = ft },
	})

	if err := o.HandlePhaseCompletion("feat-multi-infra", orchestrator.PhaseCompletionInput{
		Phase: feature.PhaseImplement,
		MultiRepoResult: &agent.OrchestratorResult{
			FinalStatus:  "failed",
			LastError:    "build error",
			RepoStatuses: map[string]string{repoName: "failed"},
		},
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	if failureType != feature.FailureInfrastructure {
		t.Errorf("failure type = %q, want %q", failureType, feature.FailureInfrastructure)
	}
}

// Roadmap-final multi-repo auto-publish routes through Publish(featureID) so
// PublishStarted/PublishCompleted events + hooks fire, and per-repo errors
// propagate to callers instead of being silently swallowed. Mirrors
// app.go:3749-3759 + the OnPublishStarted/OnPublishCompleted contract.
func TestOrchestrator_HandlePhaseCompletion_Implement_Multi_RoadmapFinal_RoutesThroughPublish(t *testing.T) {
	f := &feature.Feature{
		ID:                  "feat-multi-rf",
		Status:              feature.StatusImplementing,
		CurrentPhase:        feature.PhaseImplement,
		CurrentRoadmapPhase: 3,
		TotalRoadmapPhases:  3,
		Repos: []feature.FeatureRepo{
			{Name: repoName, Path: repoAPath},
			{Name: repoNameB, Path: repoBPath},
		},
	}
	lc := lifecycleForFeature(f)
	lc.CompleteImplementationFn = func(id string) error { return nil }
	lc.MarkCodeReadyFn = func(id string) error { return nil }
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{})

	// Capture publish dispatch so we know we routed through Publish rather
	// than open-coding the repo loop.
	var publishFeatureID string
	o.SetPublishFn(func(id string) error {
		publishFeatureID = id
		return nil
	})

	if err := o.HandlePhaseCompletion("feat-multi-rf", orchestrator.PhaseCompletionInput{
		Phase:           feature.PhaseImplement,
		MultiRepoResult: &agent.OrchestratorResult{FinalStatus: "all_passed"},
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	assertLifecycleCall(t, lc, "CompleteImplementation")
	assertLifecycleCall(t, lc, "MarkCodeReady")
	if publishFeatureID != "feat-multi-rf" {
		t.Errorf("publishFn not invoked via roadmap-final multi-repo path; got publishFeatureID=%q", publishFeatureID)
	}
	// The open-coded per-repo loop (which ignored errors) must be gone; the
	// orchestrator relies on Publish() for repo fan-out.
	refuteLifecycleCall(t, lc, "SetRepoPublishError")
}

// Roadmap-final multi-repo auto-publish propagates publishFn errors back to
// the caller — they must not be silently dropped like the open-coded loop did.
func TestOrchestrator_HandlePhaseCompletion_Implement_Multi_RoadmapFinal_PublishErrorsPropagate(t *testing.T) {
	f := &feature.Feature{
		ID:                  "feat-multi-rf-err",
		Status:              feature.StatusImplementing,
		CurrentPhase:        feature.PhaseImplement,
		CurrentRoadmapPhase: 2,
		TotalRoadmapPhases:  2,
		Repos: []feature.FeatureRepo{
			{Name: repoName, Path: repoAPath},
			{Name: repoNameB, Path: repoBPath},
		},
	}
	lc := lifecycleForFeature(f)
	lc.CompleteImplementationFn = func(id string) error { return nil }
	lc.MarkCodeReadyFn = func(id string) error { return nil }
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{})

	sentinel := errors.New("publish exploded")
	o.SetPublishFn(func(id string) error { return sentinel })

	err := o.HandlePhaseCompletion("feat-multi-rf-err", orchestrator.PhaseCompletionInput{
		Phase:           feature.PhaseImplement,
		MultiRepoResult: &agent.OrchestratorResult{FinalStatus: "all_passed"},
	})
	if !errors.Is(err, sentinel) {
		t.Errorf("HandlePhaseCompletion error = %v, want sentinel %v", err, sentinel)
	}
}

// onImplementCompleted with neither result pointer → MarkFailed(infrastructure).
func TestOrchestrator_HandlePhaseCompletion_Implement_NoPayload(t *testing.T) {
	f := &feature.Feature{
		ID:           "feat-impl-np",
		Status:       feature.StatusImplementing,
		CurrentPhase: feature.PhaseImplement,
	}
	lc := lifecycleForFeature(f)
	lc.MarkFailedFn = func(id, ft, msg string) error { return nil }
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{})

	if err := o.HandlePhaseCompletion("feat-impl-np", orchestrator.PhaseCompletionInput{
		Phase: feature.PhaseImplement,
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	assertLifecycleCall(t, lc, "MarkFailed")
}

// ---------------------------------------------------------------------------
// Regression: unknown phase returns an error.
// ---------------------------------------------------------------------------

func TestOrchestrator_HandlePhaseCompletion_UnknownPhase_Errors(t *testing.T) {
	lc := mocks.NewMockFeatureLifecycle()
	fs := mocks.NewMockFeatureStore()
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{})

	err := o.HandlePhaseCompletion("whatever", orchestrator.PhaseCompletionInput{
		Phase: feature.Phase(99),
	})
	if err == nil {
		t.Fatal("expected error for unknown phase, got nil")
	}
}

// ---------------------------------------------------------------------------
// Regression: Unknown FinalStatus paths all funnel through MarkFailed.
// ---------------------------------------------------------------------------

func TestOrchestrator_HandlePhaseCompletion_Plan_UnknownStatus_Fails(t *testing.T) {
	f := &feature.Feature{
		ID:           "feat-unk",
		Status:       feature.StatusPlanning,
		CurrentPhase: feature.PhasePlan,
	}
	lc := lifecycleForFeature(f)
	lc.MarkFailedFn = func(id, ft, msg string) error { return nil }
	fs := newFeatureStore(f)
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{})

	if err := o.HandlePhaseCompletion("feat-unk", orchestrator.PhaseCompletionInput{
		Phase:      feature.PhasePlan,
		PlanResult: &agent.PlanLoopResult{FinalStatus: "martian"},
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}
	assertLifecycleCall(t, lc, "MarkFailed")
}

// ---------------------------------------------------------------------------
// Smoke: QA file is written by artifact phase completion for inquire/research.
// ---------------------------------------------------------------------------

// The test creates the phase state dir under a temp state dir so
// onArtifactPhaseCompleted's writeQAFile call does not error; we only verify
// the handler completes without panicking. A full QA write check would need
// a real session manager with QALog data.
func TestOrchestrator_HandlePhaseCompletion_Inquire_PhaseDirCreated(t *testing.T) {
	tmpStateDir := t.TempDir()
	featureID := "feat-inq-dir"
	f := &feature.Feature{
		ID:           featureID,
		ActiveRun:    1,
		RunCount:     1,
		Status:       feature.StatusInquiring,
		CurrentPhase: feature.PhaseInquire,
		Pipeline:     feature.PipelineLarge,
		// InquiryReview gate forces advanceToNextPhase to emit ReviewRequired
		// rather than dispatching to startResearch (which would need a real
		// PhaseRunner/model registry).
		Checkpoints: feature.Checkpoints{InquiryReview: true},
	}
	// Route the phase dir through the run-aware path the completion handler
	// now uses — runs/run-001/inquire/.
	phaseDir := filepath.Join(agent.ActiveRunDir(tmpStateDir, f), "inquire")
	if err := os.MkdirAll(phaseDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(phaseDir, agent.PhaseCompleteFile), nil, 0o644); err != nil {
		t.Fatalf("write phase_complete: %v", err)
	}
	// Write an artifact so registry validation discovers it.
	artifactPath := filepath.Join(phaseDir, "inquire.md")
	if err := os.WriteFile(artifactPath, []byte("# inquire\n"), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	lc := lifecycleForFeature(f)
	lc.CompleteInquireFn = func(id string) error { return nil }
	fs := newFeatureStore(f)

	// PhaseRunner holds the StateDir used by stateDir().
	pr := &agent.PhaseRunner{StateDir: tmpStateDir}

	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs, PhaseRunner: pr}, orchestrator.Hooks{})

	if err := o.HandlePhaseCompletion(featureID, orchestrator.PhaseCompletionInput{
		Phase:   feature.PhaseInquire,
		Success: true,
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	// Artifact recorded on feature.
	if path := f.Artifacts["inquire"]; path == "" {
		t.Errorf("f.Artifacts[inquire] not recorded after inquire completion")
	}
}

// ---------------------------------------------------------------------------
// FeatureAdvanced emission contract for direct startPhase sites (completion.go)
// ---------------------------------------------------------------------------
//
// The phase-sequencing event contract requires FeatureAdvanced after every
// successful phase dispatch. Several branches in completion.go call
// o.startPhase(...) directly (not via advanceToNextPhase) — before the fix
// those branches silently omitted the event, so subscribers tracking phase
// progression missed roadmap approval, per-phase advance, final-review
// routing, and multi-repo mid-flight advance. These tests pin each emission
// site.

// Roadmap-level plan approved (auto-advance, no plan-review gate) → dispatches
// PhasePlan for phase 1. FeatureAdvanced(PhasePlan) must fire after the
// AdvanceRoadmapPhase + startPhase pair so subscribers see the transition.
// Covers completion.go:328 (onPlanApproved roadmap auto-advance branch).
func TestOrchestrator_HandlePhaseCompletion_Plan_RoadmapApproved_NoGate_EmitsFeatureAdvanced(t *testing.T) {
	tmpDir := t.TempDir()
	roadmapPath := filepath.Join(tmpDir, "roadmap.md")
	roadmap := "# Roadmap\n\n## Phase 1: Bootstrap\n### Goal\nInit\n\n## Phase 2: Build\n### Goal\nBuild\n"
	if err := os.WriteFile(roadmapPath, []byte(roadmap), 0o644); err != nil {
		t.Fatalf("write roadmap: %v", err)
	}

	f := &feature.Feature{
		ID:                  "feat-ra-advance",
		Status:              feature.StatusPlanning,
		CurrentPhase:        feature.PhasePlan,
		Pipeline:            feature.PipelineLarge,
		CurrentRoadmapPhase: 0, // top-level roadmap approval
		Artifacts:           map[string]string{"roadmap": roadmapPath},
		Repos:               []feature.FeatureRepo{{Name: "r1", Path: "/tmp/r1"}},
	}
	lc := lifecycleForFeature(f)
	lc.AdvanceRoadmapPhaseFn = func(id string) error {
		f.CurrentRoadmapPhase = 1
		f.Status = feature.StatusPlanning
		return nil
	}
	lc.StartPlanningFn = func(id string) error { f.Status = feature.StatusPlanning; return nil }
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{})

	if err := o.HandlePhaseCompletion("feat-ra-advance", orchestrator.PhaseCompletionInput{
		Phase:      feature.PhasePlan,
		PlanResult: &agent.PlanLoopResult{FinalStatus: "approved"},
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	assertLifecycleCall(t, lc, "AdvanceRoadmapPhase")

	events := drainEvents(o)
	sawAdvance := false
	for _, ev := range events {
		if ev.Type == ports.FeatureAdvanced && ev.Phase == feature.PhasePlan {
			sawAdvance = true
		}
	}
	if !sawAdvance {
		t.Errorf("expected FeatureAdvanced(PhasePlan) after roadmap auto-advance; got events: %+v", events)
	}
}

// Per-phase plan approved (roadmap mid-flight) → dispatches PhaseImplement.
// FeatureAdvanced(PhaseImplement) must fire after
// StartRoadmapPhaseImplementation + populate + startPhase so subscribers see
// the phase entry. Covers completion.go:373 (onPlanApproved per-phase branch).
func TestOrchestrator_HandlePhaseCompletion_Plan_PerPhaseApproved_EmitsFeatureAdvanced(t *testing.T) {
	planPath := writeTempFile(t, "plan.md", "plan body")
	f := &feature.Feature{
		ID:                  "feat-pp-advance",
		Status:              feature.StatusPlanning,
		CurrentPhase:        feature.PhasePlan,
		Pipeline:            feature.PipelineLarge,
		CurrentRoadmapPhase: 2,
		TotalRoadmapPhases:  3,
		Artifacts:           map[string]string{"plan": planPath},
		Repos:               []feature.FeatureRepo{{Name: "r1", Path: "/tmp/r1"}},
	}
	writeExecOrderNextToPlan(t, planPath, f.Repos)
	lc := lifecycleForFeature(f)
	lc.StartRoadmapPhaseImplementationFn = func(id string) error {
		f.Status = feature.StatusImplementing
		return nil
	}
	lc.InitRepoImplFn = func(id string) error { return nil }
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{})
	o.SetRunMultiRepoImplFn(noopRunMultiRepoImplFn())

	if err := o.HandlePhaseCompletion("feat-pp-advance", orchestrator.PhaseCompletionInput{
		Phase:      feature.PhasePlan,
		PlanResult: &agent.PlanLoopResult{FinalStatus: "approved"},
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	assertLifecycleCall(t, lc, "StartRoadmapPhaseImplementation")

	events := drainEvents(o)
	sawAdvance := false
	for _, ev := range events {
		if ev.Type == ports.FeatureAdvanced && ev.Phase == feature.PhaseImplement {
			sawAdvance = true
		}
	}
	if !sawAdvance {
		t.Errorf("expected FeatureAdvanced(PhaseImplement) after per-phase plan approved; got events: %+v", events)
	}
}

// Medium pipeline roadmap approved + no gate → AdvanceRoadmapPhase +
// startPhase(PhasePlan) for phase 1. Regression guard: an earlier refactor
// short-circuited Medium straight to PhaseImplement via CompletePlanning,
// which skipped per-phase plan generation and left startImplement with no
// plan artifact (feature stuck in StatusImplementing with a
// "plan phase did not produce an artifact" failure). All pipelines — Medium
// included — must advance to phase 1 planning from the top-level roadmap
// approval, matching master's TUI handlePlanLoopDone behavior.
func TestOrchestrator_HandlePhaseCompletion_Plan_MediumRoadmapApproved_AdvancesToPhase1(t *testing.T) {
	roadmapPath := writeTempFile(t, "roadmap.md",
		"# Roadmap\n\n## Phase 1: Do the thing\n\n### Goal\nShip.\n")
	f := &feature.Feature{
		ID:           "feat-medium-advance",
		Status:       feature.StatusPlanning,
		CurrentPhase: feature.PhasePlan,
		Pipeline:     feature.PipelineMedium,
		Artifacts:    map[string]string{"roadmap": roadmapPath},
		Repos:        []feature.FeatureRepo{{Name: "r1", Path: "/tmp/r1"}},
	}
	lc := lifecycleForFeature(f)
	advanceCalled := false
	lc.AdvanceRoadmapPhaseFn = func(id string) error {
		advanceCalled = true
		f.CurrentRoadmapPhase = 1
		f.Status = feature.StatusPlanning
		return nil
	}
	completePlanningCalled := false
	lc.CompletePlanningFn = func(id string) error {
		completePlanningCalled = true
		return nil
	}
	lc.StartPlanningFn = func(id string) error { f.Status = feature.StatusPlanning; return nil }
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{})

	if err := o.HandlePhaseCompletion("feat-medium-advance", orchestrator.PhaseCompletionInput{
		Phase:      feature.PhasePlan,
		PlanResult: &agent.PlanLoopResult{FinalStatus: "approved"},
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	if !advanceCalled {
		t.Fatal("AdvanceRoadmapPhase not called — Medium roadmap approval skipped phase-1 advancement (regression)")
	}
	if completePlanningCalled {
		t.Error("CompletePlanning called — Medium roadmap approval must advance to phase 1, not short-circuit to implement")
	}
	// Asserting on lifecycle calls also catches the regression: StartImplementation
	// would be recorded if the buggy flat-plan path ran.
	for _, c := range lc.Calls {
		if c.Method == "StartImplementation" {
			t.Error("StartImplementation called — Medium roadmap approval must not bypass phase-1 plan")
		}
	}

	events := drainEvents(o)
	sawAdvancePlan := false
	for _, ev := range events {
		if ev.Type == ports.FeatureAdvanced && ev.Phase == feature.PhasePlan {
			sawAdvancePlan = true
		}
	}
	if !sawAdvancePlan {
		t.Errorf("expected FeatureAdvanced(PhasePlan) after Medium roadmap approved; got events: %+v", events)
	}
}

// Multi-repo roadmap mid-flight all_passed → AdvanceRoadmapPhase +
// startPhase(PhasePlan). FeatureAdvanced(PhasePlan) must fire so subscribers
// see the mid-flight phase advance. Covers completion.go:609
// (onMultiReposPassed mid-flight branch).
func TestOrchestrator_HandlePhaseCompletion_Implement_Multi_RoadmapMidflight_EmitsFeatureAdvanced(t *testing.T) {
	roadmapPath := writeTempFile(t, "roadmap.md",
		"# Roadmap\n\n## Phase 1: Tracer\n### Goal\nFirst phase.\n\n## Phase 2: Fill\n### Goal\nSecond phase.\n\n## Phase 3: Finale\n### Goal\nThird phase.\n")
	f := &feature.Feature{
		ID:                  "feat-multi-midflight",
		Status:              feature.StatusImplementing,
		CurrentPhase:        feature.PhaseImplement,
		CurrentRoadmapPhase: 1,
		TotalRoadmapPhases:  3,
		Artifacts:           map[string]string{"roadmap": roadmapPath},
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: "/tmp/r1"},
			{Name: "r2", Path: "/tmp/r2"},
		},
	}
	lc := lifecycleForFeature(f)
	lc.CompleteImplementationFn = func(id string) error { return nil }
	lc.AdvanceRoadmapPhaseFn = func(id string) error {
		f.CurrentRoadmapPhase = 2
		f.Status = feature.StatusPlanning
		return nil
	}
	lc.StartPlanningFn = func(id string) error { f.Status = feature.StatusPlanning; return nil }
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{})

	if err := o.HandlePhaseCompletion("feat-multi-midflight", orchestrator.PhaseCompletionInput{
		Phase:           feature.PhaseImplement,
		MultiRepoResult: &agent.OrchestratorResult{FinalStatus: "all_passed"},
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	assertLifecycleCall(t, lc, "AdvanceRoadmapPhase")

	events := drainEvents(o)
	sawAdvance := false
	for _, ev := range events {
		if ev.Type == ports.FeatureAdvanced && ev.Phase == feature.PhasePlan {
			sawAdvance = true
		}
	}
	if !sawAdvance {
		t.Errorf("expected FeatureAdvanced(PhasePlan) after multi-repo roadmap mid-flight advance; got events: %+v", events)
	}
}

func TestOrchestrator_HandlePhaseCompletion_Implement_RoadmapRecordsCommitAnchors(t *testing.T) {
	roadmapPath := writeTempFile(t, "roadmap.md",
		"# Roadmap\n\n## Phase 1: Tracer\n### Goal\nFirst phase.\n\n## Phase 2: Fill\n### Goal\nSecond phase.\n")
	f := &feature.Feature{
		ID:                  "feat-roadmap-anchors",
		Status:              feature.StatusImplementing,
		CurrentPhase:        feature.PhaseImplement,
		CurrentRoadmapPhase: 1,
		TotalRoadmapPhases:  2,
		RoadmapPhaseType:    "tracer-bullet",
		Artifacts:           map[string]string{"roadmap": roadmapPath},
		Repos: []feature.FeatureRepo{
			{Name: "changed", Path: "/tmp/changed", WorktreePath: "/tmp/wt-changed"},
			{Name: "unchanged", Path: "/tmp/unchanged", WorktreePath: "/tmp/wt-unchanged"},
		},
	}
	lc := lifecycleForFeature(f)
	lc.CompleteImplementationFn = func(id string) error { return nil }
	lc.AdvanceRoadmapPhaseFn = func(id string) error {
		f.CurrentRoadmapPhase = 2
		f.Status = feature.StatusPlanning
		return nil
	}
	lc.StartPlanningFn = func(id string) error { f.Status = feature.StatusPlanning; return nil }
	var recordedPhase int
	var recordedAnchors map[string]string
	lc.RecordRoadmapPhaseCommitAnchorsFn = func(id string, phase int, anchors map[string]string) error {
		recordedPhase = phase
		recordedAnchors = anchors
		return nil
	}
	pub := mocks.NewMockPublisher()
	pub.CommitAllAndGetHeadFn = func(worktreePath, message string) (string, error) {
		switch worktreePath {
		case "/tmp/wt-changed":
			return "1111111111111111111111111111111111111111", nil
		case "/tmp/wt-unchanged":
			return "2222222222222222222222222222222222222222", nil
		default:
			t.Fatalf("unexpected worktree path %q", worktreePath)
		}
		return "", nil
	}
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs, Publisher: pub}, orchestrator.Hooks{})

	if err := o.HandlePhaseCompletion("feat-roadmap-anchors", orchestrator.PhaseCompletionInput{
		Phase:           feature.PhaseImplement,
		MultiRepoResult: &agent.OrchestratorResult{FinalStatus: "all_passed"},
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	assertLifecycleCall(t, lc, "RecordRoadmapPhaseCommitAnchors")
	if recordedPhase != 1 {
		t.Fatalf("recorded phase = %d, want 1", recordedPhase)
	}
	want := map[string]string{
		"changed":   "1111111111111111111111111111111111111111",
		"unchanged": "2222222222222222222222222222222222222222",
	}
	for repo, wantSHA := range want {
		if got := recordedAnchors[repo]; got != wantSHA {
			t.Errorf("anchor[%s] = %q, want %q", repo, got, wantSHA)
		}
	}
}

func TestOrchestrator_HandlePhaseCompletion_Implement_SkipsAnchorOnCommitFailure(t *testing.T) {
	roadmapPath := writeTempFile(t, "roadmap.md",
		"# Roadmap\n\n## Phase 1: Tracer\n### Goal\nFirst phase.\n\n## Phase 2: Fill\n### Goal\nSecond phase.\n")
	f := &feature.Feature{
		ID:                  "feat-roadmap-anchor-failure",
		Status:              feature.StatusImplementing,
		CurrentPhase:        feature.PhaseImplement,
		CurrentRoadmapPhase: 1,
		TotalRoadmapPhases:  2,
		RoadmapPhaseType:    "tracer-bullet",
		Artifacts:           map[string]string{"roadmap": roadmapPath},
		Repos: []feature.FeatureRepo{
			{Name: "ok", Path: "/tmp/ok", WorktreePath: "/tmp/wt-ok"},
			{Name: "failed", Path: "/tmp/failed", WorktreePath: "/tmp/wt-failed"},
		},
	}
	lc := lifecycleForFeature(f)
	lc.CompleteImplementationFn = func(id string) error { return nil }
	lc.AdvanceRoadmapPhaseFn = func(id string) error {
		f.CurrentRoadmapPhase = 2
		f.Status = feature.StatusPlanning
		return nil
	}
	lc.StartPlanningFn = func(id string) error { f.Status = feature.StatusPlanning; return nil }
	var recordedAnchors map[string]string
	lc.RecordRoadmapPhaseCommitAnchorsFn = func(id string, phase int, anchors map[string]string) error {
		recordedAnchors = anchors
		return nil
	}
	pub := mocks.NewMockPublisher()
	pub.CommitAllAndGetHeadFn = func(worktreePath, message string) (string, error) {
		if worktreePath == "/tmp/wt-failed" {
			return "", errors.New("commit failed")
		}
		return "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", nil
	}
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs, Publisher: pub}, orchestrator.Hooks{})

	if err := o.HandlePhaseCompletion("feat-roadmap-anchor-failure", orchestrator.PhaseCompletionInput{
		Phase:           feature.PhaseImplement,
		MultiRepoResult: &agent.OrchestratorResult{FinalStatus: "all_passed"},
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	if got := recordedAnchors["ok"]; got != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Errorf("ok anchor = %q", got)
	}
	if _, ok := recordedAnchors["failed"]; ok {
		t.Fatalf("failed repo received misleading anchor: %#v", recordedAnchors)
	}
}

// ---------------------------------------------------------------------------
// Non-roadmap multi-repo auto-publish: PublishCompleted emission
// ---------------------------------------------------------------------------
//
// When the non-roadmap multi-repo auto-publish path completes — per-repo
// publishes fired via OnRepoStatusChanged as each repo hit review_passed, and
// the cross-repo join inside onMultiReposPassed reaches tryCompleteAndEmit
// with published==true — the orchestrator must emit PublishCompleted and
// fire OnPublishCompleted. Without this, subscribers tracking publish
// completion (dashboards, observability, tests) miss the non-roadmap
// multi-repo happy path entirely because the per-repo publishes never emit
// the feature-level event and the Publish() pipeline is not invoked here.

// Fully-published non-roadmap multi-repo path: tryCompleteAndEmit succeeds
// (TryCompletePublish → (true, nil)) → must emit PublishCompleted and fire
// OnPublishCompleted with the per-repo PR URL map. The nil error signals the
// happy path.
func TestOrchestrator_HandlePhaseCompletion_Implement_Multi_NonRoadmap_FullyPublished_EmitsPublishCompleted(t *testing.T) {
	f := &feature.Feature{
		ID:           "feat-multi-pub-full",
		Status:       feature.StatusImplementing,
		CurrentPhase: feature.PhaseImplement,
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: "/tmp/r1"},
			{Name: "r2", Path: "/tmp/r2"},
		},
		RepoStates: map[string]*feature.RepoState{
			"r1": {PRURL: "https://github.com/org/r1/pull/1"},
			"r2": {PRURL: "https://github.com/org/r2/pull/2"},
		},
	}
	lc := lifecycleForFeature(f)
	lc.CompleteImplementationFn = func(id string) error { return nil }
	lc.TryCompletePublishFn = func(id string) (bool, error) {
		f.Status = feature.StatusPublished
		return true, nil
	}
	fs := newFeatureStore(f)

	var pubCompletedID string
	var pubCompletedURLs map[string]string
	var pubCompletedErr error
	var pubHookCalls int
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{
		OnPublishCompleted: func(id string, urls map[string]string, err error) {
			pubCompletedID = id
			pubCompletedURLs = urls
			pubCompletedErr = err
			pubHookCalls++
		},
	})

	if err := o.HandlePhaseCompletion("feat-multi-pub-full", orchestrator.PhaseCompletionInput{
		Phase:           feature.PhaseImplement,
		MultiRepoResult: &agent.OrchestratorResult{FinalStatus: "all_passed"},
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	// tryCompleteAndEmit ran and returned published=true — the orchestrator
	// must NOT regress the feature to StatusCodeReady.
	refuteLifecycleCall(t, lc, "MarkCodeReady")

	// PublishCompleted event must be on the bus.
	events := drainEvents(o)
	var pubEvent *ports.Event
	for i, ev := range events {
		if ev.Type == ports.PublishCompleted {
			pubEvent = &events[i]
			break
		}
	}
	if pubEvent == nil {
		t.Fatalf("expected PublishCompleted event after non-roadmap multi-repo auto-publish; got events: %+v", events)
	}
	if pubEvent.Error != nil {
		t.Errorf("PublishCompleted.Error = %v, want nil on fully-published happy path", pubEvent.Error)
	}

	// OnPublishCompleted hook must have fired exactly once with no error and
	// the per-repo PR URLs gathered from RepoImpl.
	if pubHookCalls != 1 {
		t.Errorf("OnPublishCompleted fired %d times, want 1", pubHookCalls)
	}
	if pubCompletedID != "feat-multi-pub-full" {
		t.Errorf("OnPublishCompleted featureID = %q, want feat-multi-pub-full", pubCompletedID)
	}
	if pubCompletedErr != nil {
		t.Errorf("OnPublishCompleted err = %v, want nil", pubCompletedErr)
	}
	if got := pubCompletedURLs["r1"]; got != "https://github.com/org/r1/pull/1" {
		t.Errorf("OnPublishCompleted prURLs[r1] = %q, want r1 URL", got)
	}
	if got := pubCompletedURLs["r2"]; got != "https://github.com/org/r2/pull/2" {
		t.Errorf("OnPublishCompleted prURLs[r2] = %q, want r2 URL", got)
	}
}

// Partially-published non-roadmap multi-repo path: tryCompleteAndEmit returns
// (false, nil) (not yet fully published — e.g. a repo publish is still
// pending). The handler must fall back to MarkCodeReady so resume paths can
// recover the partial state, and PublishCompleted must NOT fire (the publish
// has not actually completed yet).
func TestOrchestrator_HandlePhaseCompletion_Implement_Multi_NonRoadmap_NotFullyPublished_NoPublishCompleted(t *testing.T) {
	f := &feature.Feature{
		ID:           "feat-multi-pub-partial",
		Status:       feature.StatusImplementing,
		CurrentPhase: feature.PhaseImplement,
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: "/tmp/r1"},
			{Name: "r2", Path: "/tmp/r2"},
		},
	}
	lc := lifecycleForFeature(f)
	lc.CompleteImplementationFn = func(id string) error { return nil }
	lc.TryCompletePublishFn = func(id string) (bool, error) { return false, nil }
	lc.MarkCodeReadyFn = func(id string) error { return nil }
	fs := newFeatureStore(f)

	var pubHookCalls int
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{
		OnPublishCompleted: func(id string, urls map[string]string, err error) { pubHookCalls++ },
	})

	if err := o.HandlePhaseCompletion("feat-multi-pub-partial", orchestrator.PhaseCompletionInput{
		Phase:           feature.PhaseImplement,
		MultiRepoResult: &agent.OrchestratorResult{FinalStatus: "all_passed"},
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	// Not fully published → MarkCodeReady must fire so resume paths recover.
	assertLifecycleCall(t, lc, "MarkCodeReady")

	// PublishCompleted must NOT have fired — the publish has not completed.
	events := drainEvents(o)
	if hasEventType(events, ports.PublishCompleted) {
		t.Error("PublishCompleted must NOT fire when the feature is not yet fully published")
	}
	if pubHookCalls != 0 {
		t.Errorf("OnPublishCompleted fired %d times on partial-publish path; want 0", pubHookCalls)
	}
}

// Idempotency guard on onMultiReposPassed. A second "all_passed" delivery
// (e.g. after the synchronous OnRepoStatusChanged trigger already advanced
// the feature) must be a no-op — no duplicate CompleteImplementation, no
// duplicate PhaseCompleted emission, and crucially no lifecycle error that
// would cause surfaceDispatchCompletionError to wrongly mark the feature
// Failed.
func TestOrchestrator_HandlePhaseCompletion_Implement_Multi_AllPassed_Idempotent(t *testing.T) {
	f := &feature.Feature{
		ID:           "feat-multi-idem",
		Status:       feature.StatusReviewPassed, // already transitioned by a prior caller
		CurrentPhase: feature.PhaseImplement,
		Repos: []feature.FeatureRepo{
			{Name: repoName, Path: repoAPath},
			{Name: repoNameB, Path: repoBPath},
		},
	}
	lc := lifecycleForFeature(f)
	lc.CompleteImplementationFn = func(id string) error { return nil }
	lc.MarkCodeReadyFn = func(id string) error { return nil }
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{})

	if err := o.HandlePhaseCompletion("feat-multi-idem", orchestrator.PhaseCompletionInput{
		Phase:           feature.PhaseImplement,
		MultiRepoResult: &agent.OrchestratorResult{FinalStatus: "all_passed"},
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion (second call): %v", err)
	}

	refuteLifecycleCall(t, lc, "CompleteImplementation")
	refuteLifecycleCall(t, lc, "MarkCodeReady")

	events := drainEvents(o)
	if hasEventType(events, ports.PhaseCompleted) {
		t.Error("PhaseCompleted must NOT re-fire when the feature is already past StatusImplementing")
	}
}
