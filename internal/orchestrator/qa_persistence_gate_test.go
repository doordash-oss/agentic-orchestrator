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
	"sync"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// gateTestFeatureStore is a featureStore stand-in scoped to this test file —
// the orchestrator_test package has its own helpers but they live in a
// different package and are not reachable from the internal package, so this
// file defines a minimal implementation.
type gateTestFeatureStore struct {
	*mocks.MockFeatureStore
	mu       sync.Mutex
	features map[string]*feature.Feature
}

func newGateFeatureStore(features ...*feature.Feature) *gateTestFeatureStore {
	fs := &gateTestFeatureStore{
		MockFeatureStore: mocks.NewMockFeatureStore(),
		features:         make(map[string]*feature.Feature),
	}
	for _, f := range features {
		fs.features[f.ID] = f
	}
	fs.LoadFn = func(id string) (*feature.Feature, error) {
		fs.mu.Lock()
		defer fs.mu.Unlock()
		f, ok := fs.features[id]
		if !ok {
			return nil, errors.New("feature not found")
		}
		return f, nil
	}
	fs.ModifyFn = func(id string, fn func(ff *feature.Feature) error) error {
		fs.mu.Lock()
		defer fs.mu.Unlock()
		f, ok := fs.features[id]
		if !ok {
			return errors.New("feature not found")
		}
		return fn(f)
	}
	return fs
}

func newGateLifecycle(f *feature.Feature) *mocks.MockFeatureLifecycle {
	lc := mocks.NewMockFeatureLifecycle()
	lc.GetFn = func(id string) (*feature.Feature, error) { return f, nil }
	lc.TransitionFn = func(id string, to feature.Status) error {
		f.Status = to
		return nil
	}
	lc.CompleteInquireFn = func(id string) error { return nil }
	lc.CompleteResearchFn = func(id string) error { return nil }
	lc.CompleteDesignFn = func(id string) error { return nil }
	lc.StartInquireFn = func(id string) error { return nil }
	lc.StartResearchFn = func(id string) error { return nil }
	lc.StartDesignFn = func(id string) error { return nil }
	lc.StartPlanningFn = func(id string) error { return nil }
	return lc
}

// sessionManagerWithQALog returns a MockSessionManager whose GetSession
// returns a MockSessionView with one Q&A pair so the writeQAFile path has
// realistic input.
func sessionManagerWithQALog(sessionID string) *mocks.MockSessionManager {
	sm := mocks.NewMockSessionManager()
	sm.GetSessionFn = func(id string) session.SessionView {
		if id != sessionID {
			return nil
		}
		v := mocks.NewMockSessionView(sessionID, "feat-gate")
		v.QALogVal = []ports.QAPair{
			{Question: "Q1", Answer: "A1", Notes: "note one"},
			{Question: "Q2", Answer: "Auto (Recommended)", AutoPicked: true, Confidence: 0.85},
		}
		return v
	}
	return sm
}

// TestOrchestrator_OnArtifactPhaseCompleted_QAWritesForInteractivePlanningPhases is the
// table-driven persistence-whitelist regression. It calls onArtifactPhaseCompleted
// directly with each interactive planning phase plus roadmap/phase-plan
// sentinel keys. Inquire and Research persist the session Q&A log; Design,
// Roadmap, and Phase-Plan transcripts are owned by their iterative loops.
func TestOrchestrator_OnArtifactPhaseCompleted_QAWritesForInteractivePlanningPhases(t *testing.T) {
	cases := []struct {
		phaseKey       string
		phase          feature.Phase
		wantQAFile     bool
		completionFnFn func(lc *mocks.MockFeatureLifecycle) func(string) error
	}{
		{"inquire", feature.PhaseInquire, true, func(lc *mocks.MockFeatureLifecycle) func(string) error {
			return lc.CompleteInquire
		}},
		{"roadmap", feature.PhaseInquire /* unused */, false, func(lc *mocks.MockFeatureLifecycle) func(string) error {
			return func(string) error { return nil }
		}},
		{"phase-plan", feature.PhaseInquire /* unused */, false, func(lc *mocks.MockFeatureLifecycle) func(string) error {
			return func(string) error { return nil }
		}},
		{"research", feature.PhaseResearch, true, func(lc *mocks.MockFeatureLifecycle) func(string) error {
			return lc.CompleteResearch
		}},
	}

	for _, tc := range cases {
		t.Run(tc.phaseKey, func(t *testing.T) {
			tmpStateDir := t.TempDir()
			featureID := "feat-gate"
			f := &feature.Feature{
				ID:           featureID,
				ActiveRun:    1,
				RunCount:     1,
				CurrentPhase: tc.phase,
				Pipeline:     feature.PipelineLarge,
				// All review gates are enabled so advanceToNextPhase always hits
				// a checkpoint and short-circuits rather than dispatching a
				// real downstream phase that would need a model registry.
				Checkpoints: feature.Checkpoints{
					InquiryReview:   true,
					ResearchReview:  true,
					DesignReview:    true,
					RoadmapReview:   true,
					PhasePlanReview: true,
				},
			}

			phaseDir := filepath.Join(agent.ActiveRunDir(tmpStateDir, f), tc.phaseKey)
			if err := os.MkdirAll(phaseDir, 0o755); err != nil {
				t.Fatalf("mkdir phaseDir: %v", err)
			}
			if err := os.WriteFile(filepath.Join(phaseDir, agent.PhaseCompleteFile), nil, 0o644); err != nil {
				t.Fatalf("write phase_complete: %v", err)
			}
			// Drop a stand-in artifact so contract validation doesn't trip the
			// onArtifactPhaseCompleted control flow.
			artifactPath := filepath.Join(phaseDir, tc.phaseKey+".md")
			if err := os.WriteFile(artifactPath, []byte("# "+tc.phaseKey+"\n"), 0o644); err != nil {
				t.Fatalf("write artifact: %v", err)
			}

			lc := newGateLifecycle(f)
			fs := newGateFeatureStore(f)
			sm := sessionManagerWithQALog("sess-1")
			pr := &agent.PhaseRunner{StateDir: tmpStateDir}

			o := New(Deps{Lifecycle: lc, Store: fs, Sessions: sm, PhaseRunner: pr}, Hooks{})

			input := PhaseCompletionInput{
				Phase:     tc.phase,
				SessionID: "sess-1",
				Success:   true,
			}
			if err := o.onArtifactPhaseCompleted(featureID, input, tc.phaseKey, tc.completionFnFn(lc)); err != nil {
				t.Fatalf("onArtifactPhaseCompleted(%q): %v", tc.phaseKey, err)
			}

			qaPath := filepath.Join(phaseDir, "qa-answers.md")
			_, err := os.Stat(qaPath)
			switch {
			case tc.wantQAFile && err != nil:
				t.Errorf("expected qa-answers.md to be written for phaseKey=%q, but got: %v", tc.phaseKey, err)
			case !tc.wantQAFile && err == nil:
				t.Errorf("expected NO qa-answers.md to be written for phaseKey=%q, but the file exists", tc.phaseKey)
			}
		})
	}
}

const sampleAgentAuthoredQA = `# User Q&A — Phase Clarifications

## Q: What does the user want?
**A:** Option A

## Q: How confident are we in the recommended option?
**A:** Recommended option B
_(auto-picked, confidence: 0.85)_
`

const sampleHarnessOwnedQA = `# User Q&A — Phase Clarifications

## Q: Q1

**A:** A1

**Notes:** note one

## Q: Q2

**A:** Auto (Recommended)

_(auto-picked, confidence: 0.85)_

`

// TestInquirePhase_WritesHarnessOwnedQAFile pre-writes stale qa-answers.md,
// drives HandlePhaseCompletion through Inquire, and asserts the file is
// replaced from the session Q&A log. Then asserts
// collectQAFilePaths returns the inquire path so Design consumes it.
func TestInquirePhase_WritesHarnessOwnedQAFile(t *testing.T) {
	tmpStateDir := t.TempDir()
	featureID := "feat-preserve-inq"
	f := &feature.Feature{
		ID:           featureID,
		ActiveRun:    1,
		RunCount:     1,
		Status:       feature.StatusInquiring,
		CurrentPhase: feature.PhaseInquire,
		Pipeline:     feature.PipelineLarge,
		Checkpoints: feature.Checkpoints{
			InquiryReview:   true,
			ResearchReview:  true,
			DesignReview:    true,
			RoadmapReview:   true,
			PhasePlanReview: true,
		},
	}

	phaseDir := filepath.Join(agent.ActiveRunDir(tmpStateDir, f), "inquire")
	if err := os.MkdirAll(phaseDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	artifactPath := filepath.Join(phaseDir, "inquire.md")
	if err := os.WriteFile(artifactPath, []byte("# inquire\n"), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	if err := os.WriteFile(filepath.Join(phaseDir, agent.PhaseCompleteFile), nil, 0o644); err != nil {
		t.Fatalf("write phase_complete: %v", err)
	}
	qaPath := filepath.Join(phaseDir, "qa-answers.md")
	if err := os.WriteFile(qaPath, []byte(sampleAgentAuthoredQA), 0o644); err != nil {
		t.Fatalf("pre-write qa-answers.md: %v", err)
	}

	lc := newGateLifecycle(f)
	fs := newGateFeatureStore(f)
	sm := sessionManagerWithQALog("sess-1")
	pr := &agent.PhaseRunner{StateDir: tmpStateDir}

	o := New(Deps{Lifecycle: lc, Store: fs, Sessions: sm, PhaseRunner: pr}, Hooks{})

	if err := o.HandlePhaseCompletion(featureID, PhaseCompletionInput{
		Phase:     feature.PhaseInquire,
		SessionID: "sess-1",
		Success:   true,
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	got, err := os.ReadFile(qaPath)
	if err != nil {
		t.Fatalf("read qa-answers.md: %v", err)
	}
	if string(got) != sampleHarnessOwnedQA {
		t.Errorf("qa-answers.md was not written from session QALog.\n--- got ---\n%s\n--- want ---\n%s", got, sampleHarnessOwnedQA)
	}

	paths := o.collectQAFilePaths(f, f.RefactorPrefix())
	found := false
	for _, p := range paths {
		if p == qaPath {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("collectQAFilePaths did not include inquire qa path %q; got %v", qaPath, paths)
	}
}

// TestInquirePhase_AutoPickAnnotationRendered asserts auto-picked Q&A
// metadata renders end-to-end with two decimal places.
func TestInquirePhase_AutoPickAnnotationPreserved(t *testing.T) {
	tmpStateDir := t.TempDir()
	featureID := "feat-autopick"
	f := &feature.Feature{
		ID:           featureID,
		ActiveRun:    1,
		RunCount:     1,
		Status:       feature.StatusInquiring,
		CurrentPhase: feature.PhaseInquire,
		Pipeline:     feature.PipelineLarge,
		Checkpoints: feature.Checkpoints{
			InquiryReview:   true,
			ResearchReview:  true,
			DesignReview:    true,
			RoadmapReview:   true,
			PhasePlanReview: true,
		},
	}
	phaseDir := filepath.Join(agent.ActiveRunDir(tmpStateDir, f), "inquire")
	if err := os.MkdirAll(phaseDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	artifactPath := filepath.Join(phaseDir, "inquire.md")
	if err := os.WriteFile(artifactPath, []byte("# inquire\n"), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	if err := os.WriteFile(filepath.Join(phaseDir, agent.PhaseCompleteFile), nil, 0o644); err != nil {
		t.Fatalf("write phase_complete: %v", err)
	}
	qaPath := filepath.Join(phaseDir, "qa-answers.md")
	if err := os.WriteFile(qaPath, []byte(sampleAgentAuthoredQA), 0o644); err != nil {
		t.Fatalf("pre-write qa: %v", err)
	}

	lc := newGateLifecycle(f)
	fs := newGateFeatureStore(f)
	sm := sessionManagerWithQALog("sess-1")
	pr := &agent.PhaseRunner{StateDir: tmpStateDir}

	o := New(Deps{Lifecycle: lc, Store: fs, Sessions: sm, PhaseRunner: pr}, Hooks{})

	if err := o.HandlePhaseCompletion(featureID, PhaseCompletionInput{
		Phase:     feature.PhaseInquire,
		SessionID: "sess-1",
		Success:   true,
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	got, err := os.ReadFile(qaPath)
	if err != nil {
		t.Fatalf("read qa-answers.md: %v", err)
	}
	const wantAnnotation = "_(auto-picked, confidence: 0.85)_"
	gotStr := string(got)
	if gotStr == "" || !contains(gotStr, wantAnnotation) {
		t.Errorf("expected qa-answers.md to contain %q after HandlePhaseCompletion; got:\n%s", wantAnnotation, gotStr)
	}
	// Annotation must survive exactly once — no duplication or transformation.
	if countSubstr(gotStr, wantAnnotation) != 1 {
		t.Errorf("auto-pick annotation count = %d, want 1; content:\n%s", countSubstr(gotStr, wantAnnotation), gotStr)
	}
}

// TestInquirePhase_NoExistingQAFile_WritesHarnessOwnedFile asserts that when
// no qa-answers.md exists yet, the orchestrator writes one from the session log.
func TestInquirePhase_NoExistingQAFile_WritesHarnessOwnedFile(t *testing.T) {
	tmpStateDir := t.TempDir()
	featureID := "feat-no-qa"
	f := &feature.Feature{
		ID:           featureID,
		ActiveRun:    1,
		RunCount:     1,
		Status:       feature.StatusInquiring,
		CurrentPhase: feature.PhaseInquire,
		Pipeline:     feature.PipelineLarge,
		Checkpoints: feature.Checkpoints{
			InquiryReview:   true,
			ResearchReview:  true,
			DesignReview:    true,
			RoadmapReview:   true,
			PhasePlanReview: true,
		},
	}
	phaseDir := filepath.Join(agent.ActiveRunDir(tmpStateDir, f), "inquire")
	if err := os.MkdirAll(phaseDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	artifactPath := filepath.Join(phaseDir, "inquire.md")
	if err := os.WriteFile(artifactPath, []byte("# inquire\n"), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	if err := os.WriteFile(filepath.Join(phaseDir, agent.PhaseCompleteFile), nil, 0o644); err != nil {
		t.Fatalf("write phase_complete: %v", err)
	}

	lc := newGateLifecycle(f)
	fs := newGateFeatureStore(f)
	sm := sessionManagerWithQALog("sess-1")
	pr := &agent.PhaseRunner{StateDir: tmpStateDir}

	o := New(Deps{Lifecycle: lc, Store: fs, Sessions: sm, PhaseRunner: pr}, Hooks{})

	if err := o.HandlePhaseCompletion(featureID, PhaseCompletionInput{
		Phase:     feature.PhaseInquire,
		SessionID: "sess-1",
		Success:   true,
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	qaPath := filepath.Join(phaseDir, "qa-answers.md")
	got, err := os.ReadFile(qaPath)
	if err != nil {
		t.Fatalf("read qa-answers.md: %v", err)
	}
	if string(got) != sampleHarnessOwnedQA {
		t.Errorf("qa-answers.md was not written from session QALog.\n--- got ---\n%s\n--- want ---\n%s", got, sampleHarnessOwnedQA)
	}
}

// TestInquirePhase_RefPrefix_WritesQAFile asserts the gate honors the
// refactor-prefix path layout when writing the harness-owned file.
func TestInquirePhase_RefPrefix_WritesQAFile(t *testing.T) {
	tmpStateDir := t.TempDir()
	featureID := "feat-refprefix"
	f := &feature.Feature{
		ID:           featureID,
		ActiveRun:    1,
		RunCount:     1,
		Status:       feature.StatusInquiring,
		CurrentPhase: feature.PhaseInquire,
		Pipeline:     feature.PipelineLarge,
		// RefactorCount + RefactorPrompt together drive RefactorPrefix();
		// any non-empty prefix exercises the prefixed-path branch.
		RefactorPrompt: "polish",
		Checkpoints: feature.Checkpoints{
			InquiryReview:   true,
			ResearchReview:  true,
			DesignReview:    true,
			RoadmapReview:   true,
			PhasePlanReview: true,
		},
	}
	f.SetRefactorCount(1)
	prefix := f.RefactorPrefix()
	if prefix == "" {
		t.Fatalf("expected non-empty RefactorPrefix() for refactor count = 1")
	}
	phaseDir := filepath.Join(agent.ActiveRunDir(tmpStateDir, f), prefix, "inquire")
	if err := os.MkdirAll(phaseDir, 0o755); err != nil {
		t.Fatalf("mkdir refactor phase dir: %v", err)
	}
	artifactPath := filepath.Join(phaseDir, "inquire.md")
	if err := os.WriteFile(artifactPath, []byte("# inquire\n"), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	if err := os.WriteFile(filepath.Join(phaseDir, agent.PhaseCompleteFile), nil, 0o644); err != nil {
		t.Fatalf("write phase_complete: %v", err)
	}
	qaPath := filepath.Join(phaseDir, "qa-answers.md")
	if err := os.WriteFile(qaPath, []byte(sampleAgentAuthoredQA), 0o644); err != nil {
		t.Fatalf("pre-write qa: %v", err)
	}

	lc := newGateLifecycle(f)
	fs := newGateFeatureStore(f)
	sm := sessionManagerWithQALog("sess-1")
	pr := &agent.PhaseRunner{StateDir: tmpStateDir}

	o := New(Deps{Lifecycle: lc, Store: fs, Sessions: sm, PhaseRunner: pr}, Hooks{})

	if err := o.HandlePhaseCompletion(featureID, PhaseCompletionInput{
		Phase:     feature.PhaseInquire,
		SessionID: "sess-1",
		Success:   true,
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	got, err := os.ReadFile(qaPath)
	if err != nil {
		t.Fatalf("read qa file under refactor prefix: %v", err)
	}
	if string(got) != sampleHarnessOwnedQA {
		t.Errorf("refactor-prefixed qa-answers.md was not written from session QALog:\n got:\n%s\n want:\n%s", got, sampleHarnessOwnedQA)
	}

	paths := o.collectQAFilePaths(f, prefix)
	found := false
	for _, p := range paths {
		if p == qaPath {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("collectQAFilePaths(prefix=%q) did not include refactor-prefixed inquire path %q; got %v", prefix, qaPath, paths)
	}
}

// contains and countSubstr — tiny strings.Contains / strings.Count wrappers
// kept local so this test file does not pull strings only for two callers.
func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

func countSubstr(haystack, needle string) int {
	if needle == "" {
		return 0
	}
	n := 0
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			n++
			i += len(needle) - 1
		}
	}
	return n
}
