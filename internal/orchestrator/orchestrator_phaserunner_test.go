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
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// ---------------------------------------------------------------------------
// stubSessionHandle — minimal ports.SessionHandle used by PhaseRunner
// wiring tests. All mutation methods are no-ops; read methods return zero.
//
// The PhaseRunner code that runs after MockSessionManager.StartSession
// returns the handle uses only a narrow slice of SessionHandle — it
// registers cleanup callbacks, sets a log file, and registers a tool-read
// tracker. The stub satisfies those calls without spinning up a real PTY.
// ---------------------------------------------------------------------------

// stubSessionHandle embeds the shared mock session view; the PhaseRunner
// code that runs after MockSessionManager.StartSession returns the handle
// uses only a narrow slice of SessionHandle — it registers cleanup
// callbacks, sets a log file, and registers a tool-read tracker. The mock
// satisfies those calls without spinning up a real PTY.
type stubSessionHandle struct {
	*mocks.MockSessionView
}

func newStubSessionHandle(id, featureID string, phase feature.Phase, repoName string) *stubSessionHandle {
	m := mocks.NewMockSessionView(id, featureID)
	m.PhaseVal = phase
	m.RepoNameVal = repoName
	return &stubSessionHandle{MockSessionView: m}
}

// ---------------------------------------------------------------------------
// capturingPhaseRunner — helper that constructs a real *agent.PhaseRunner
// with BuildSessionFn capturing per-invocation opts, and a MockSessionManager
// that returns stubSessionHandles. Both the orchestrator and the PhaseRunner
// observe the same SessionManager, letting tests verify the full
// orchestrator→PhaseRunner→SessionManager dispatch chain without spinning
// up real PTYs.
// ---------------------------------------------------------------------------

type capturingPhaseRunner struct {
	pr           *agent.PhaseRunner
	sm           *mocks.MockSessionManager
	cmd          *mocks.MockCommandRunner
	stateDir     string
	mu           sync.Mutex
	capturedOpts []agent.BuildSessionOpts
}

func newCapturingPhaseRunner(t *testing.T) *capturingPhaseRunner {
	t.Helper()
	stateDir := t.TempDir()
	sm := mocks.NewMockSessionManager()
	cmd := mocks.NewMockCommandRunner()
	// Default: git commands return a stable commit.
	cmd.RunFn = func(ctx context.Context, name string, args []string, opts ports.CommandOpts) ([]byte, error) {
		return []byte("deadbeef\n"), nil
	}

	cpr := &capturingPhaseRunner{
		sm:       sm,
		cmd:      cmd,
		stateDir: stateDir,
	}

	pr := &agent.PhaseRunner{
		SessionManager: sm,
		FeatureStore:   feature.NewStore(stateDir),
		CommandRunner:  cmd,
		StateDir:       stateDir,
	}
	pr.BuildSessionFn = func(opts agent.BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
		cpr.mu.Lock()
		cpr.capturedOpts = append(cpr.capturedOpts, opts)
		cpr.mu.Unlock()
		return []string{"echo", "test"}, nil, &session.SessionOpts{
			PIDDir:        opts.PIDDir,
			PermHandler:   opts.PermHandler,
			InitialPrompt: opts.Prompt,
			RepoName:      opts.RepoName,
		}, nil
	}
	cpr.pr = pr

	// MockSessionManager.StartSession returns a stubSessionHandle so the
	// PhaseRunner's post-StartSession bookkeeping (AddCleanupFunc, SetLogFile,
	// observer hooks) has something to operate on.
	sm.StartSessionFn = func(id, featureID string, phase feature.Phase,
		command []string, workdir string, env []string,
		opts ...*session.SessionOpts) (ports.SessionHandle, error) {
		repoName := ""
		if len(opts) > 0 && opts[0] != nil {
			repoName = opts[0].RepoName
		}
		return newStubSessionHandle(id, featureID, phase, repoName), nil
	}

	return cpr
}

// capturedByPhase returns captured BuildSessionOpts entries whose Phase
// matches want. A helper because a single StartFeature call may trigger
// multiple BuildSession invocations.
func (c *capturingPhaseRunner) capturedByPhase(want feature.Phase) []agent.BuildSessionOpts {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []agent.BuildSessionOpts
	for _, o := range c.capturedOpts {
		if o.Phase == want {
			out = append(out, o)
		}
	}
	return out
}

// startSessionsByPhase returns MockStartSessionCall entries whose Phase
// matches want.
func (c *capturingPhaseRunner) startSessionsByPhase(want feature.Phase) []mocks.MockStartSessionCall {
	var out []mocks.MockStartSessionCall
	for _, call := range c.sm.StartSessionCalls {
		if call.Phase == want {
			out = append(out, call)
		}
	}
	return out
}

func writeRoadmap(t *testing.T, path string) {
	t.Helper()
	body := strings.Join([]string{
		"# Roadmap",
		"",
		"## Phase 1: Tracer",
		"",
		"### Goal",
		"Prove the wiring.",
		"",
		"## Phase 2: Follow-up",
		"",
		"### Goal",
		"Extend the tracer with real logic.",
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write roadmap: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Item 6: per-phase PhaseRunner dispatch — synchronous phases
// ---------------------------------------------------------------------------
//
// Verifies that startInquire/startResearch/startDesign actually wire
// into PhaseRunner and produce BuildSession invocations carrying the
// correct BuildSessionOpts.Phase. This is what iteration-01 was missing —
// the old table-driven test constructed the orchestrator with only
// Lifecycle+Store, so no PhaseRunner context could be asserted.
//
// All three phases delegate to runInteractivePhase which calls BuildSession
// synchronously; when StartFeature returns, capturedOpts is already populated.

func TestOrchestrator_StartPhase_PhaseRunnerDispatch_Sync(t *testing.T) {
	tests := []struct {
		name           string
		phase          feature.Phase
		wantTransition string
		setup          func(t *testing.T, f *feature.Feature, stateDir string)
	}{
		{
			name:           "Inquire",
			phase:          feature.PhaseInquire,
			wantTransition: "StartInquire",
			setup:          func(t *testing.T, f *feature.Feature, stateDir string) {},
		},
		{
			name:           "Research",
			phase:          feature.PhaseResearch,
			wantTransition: "StartResearch",
			// PhaseResearch is the iota-0 zero value. For a legitimate
			// interrupted-in-Research resume, StartedAt must be set
			// (signals a phase already started). Without it, StartFeature's
			// fallback reroutes to the pipeline's first phase.
			//
			// startResearch also requires an inquire artifact (the questions
			// file Research consumes), so we seed it on disk and in the
			// feature's Artifacts map.
			setup: func(t *testing.T, f *feature.Feature, stateDir string) {
				started := time.Now().Add(-time.Hour)
				f.StartedAt = &started

				inquireDir := filepath.Join(stateDir, f.ID, "inquire")
				if err := os.MkdirAll(inquireDir, 0o755); err != nil {
					t.Fatalf("mkdir inquire dir: %v", err)
				}
				questionsPath := filepath.Join(inquireDir, "questions.md")
				if err := os.WriteFile(questionsPath, []byte("# Questions\n"), 0o644); err != nil {
					t.Fatalf("write questions: %v", err)
				}
				if f.Artifacts == nil {
					f.Artifacts = map[string]string{}
				}
				f.Artifacts["inquire"] = questionsPath
			},
		},
		{
			name:           "Design",
			phase:          feature.PhaseDesign,
			wantTransition: "StartDesign",
			setup: func(t *testing.T, f *feature.Feature, stateDir string) {
				researchDir := filepath.Join(stateDir, f.ID, "research")
				if err := os.MkdirAll(researchDir, 0o755); err != nil {
					t.Fatalf("mkdir research dir: %v", err)
				}
				researchPath := filepath.Join(researchDir, "research.md")
				if err := os.WriteFile(researchPath, []byte("# research\n"), 0o644); err != nil {
					t.Fatalf("write research: %v", err)
				}
				if f.Artifacts == nil {
					f.Artifacts = map[string]string{}
				}
				f.Artifacts["research"] = researchPath
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cpr := newCapturingPhaseRunner(t)
			f := &feature.Feature{
				ID:           "feat-dispatch",
				Status:       feature.StatusInterrupted,
				CurrentPhase: tt.phase,
				Pipeline:     feature.PipelineLarge,
			}
			tt.setup(t, f, cpr.stateDir)

			fs, lc := newStoredFeatureFixture(t, f)
			cpr.pr.FeatureStore = fs
			o := orchestrator.New(orchestrator.Deps{
				Manager:     lc,
				Store:       fs,
				Sessions:    cpr.sm,
				PhaseRunner: cpr.pr,
				CmdRunner:   cpr.cmd,
			}, orchestrator.Hooks{})
			t.Cleanup(o.WaitForCycles)

			if err := o.StartFeature(f.ID); err != nil {
				t.Fatalf("StartFeature: %v", err)
			}

			if got := loadStoredFeature(t, fs, f.ID).CurrentPhase; got != tt.phase {
				t.Errorf("persisted phase = %s, want %s", got, tt.phase)
			}

			captured := cpr.capturedByPhase(tt.phase)
			if len(captured) == 0 {
				t.Fatalf("BuildSession was not called with Phase=%v; all captures: %v",
					tt.phase, cpr.capturedOpts)
			}
			// Confirm StartSession dispatch carried the same phase.
			starts := cpr.startSessionsByPhase(tt.phase)
			if len(starts) == 0 {
				t.Errorf("StartSession not called with Phase=%v; calls: %v",
					tt.phase, cpr.sm.StartSessionCalls)
			}
			// FeatureID is plumbed end-to-end.
			for _, s := range starts {
				if s.FeatureID != f.ID {
					t.Errorf("StartSession FeatureID = %q, want %q", s.FeatureID, f.ID)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Item 6: per-phase PhaseRunner dispatch — asynchronous phases
// ---------------------------------------------------------------------------
//
// startPlan/startReview hand off to PhaseRunner methods that launch a
// goroutine (RunPlanningWithValidation / RunFinalReview). We configure
// MockSessionManager.StartSession to return session.ErrShuttingDown so the
// loops exit cleanly after one BuildSession→StartSession attempt. The test
// then polls capturedOpts for a matching Phase entry.

func waitForCapturedPhase(t *testing.T, cpr *capturingPhaseRunner, want feature.Phase, timeout time.Duration) []agent.BuildSessionOpts {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if got := cpr.capturedByPhase(want); len(got) > 0 {
			return got
		}
		// Retained: bounded poll interval for asynchronous phase dispatch capture.
		time.Sleep(10 * time.Millisecond)
	}
	return cpr.capturedByPhase(want)
}

func TestOrchestrator_StartPhase_PhaseRunnerDispatch_Async(t *testing.T) {
	tests := []struct {
		name           string
		phase          feature.Phase
		wantTransition string
		setup          func(t *testing.T, f *feature.Feature, stateDir string)
	}{
		{
			name:           "Plan_Medium",
			phase:          feature.PhasePlan,
			wantTransition: "StartPlanning",
			setup: func(t *testing.T, f *feature.Feature, stateDir string) {
				f.Pipeline = feature.PipelineMedium
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cpr := newCapturingPhaseRunner(t)
			// Return ErrShuttingDown for the phase we're testing so the
			// async loop exits cleanly after capturing BuildSession opts.
			cpr.sm.StartSessionFn = func(id, featureID string, phase feature.Phase,
				command []string, workdir string, env []string,
				opts ...*session.SessionOpts) (ports.SessionHandle, error) {
				return nil, session.ErrShuttingDown
			}

			f := &feature.Feature{
				ID:           "feat-async",
				Status:       feature.StatusInterrupted,
				CurrentPhase: tt.phase,
				Pipeline:     feature.PipelineLarge,
				Repos: []feature.FeatureRepo{
					{Name: "repo1", Path: cpr.stateDir},
				},
			}
			tt.setup(t, f, cpr.stateDir)

			fs, lc := newStoredFeatureFixture(t, f)
			cpr.pr.FeatureStore = fs
			o := orchestrator.New(orchestrator.Deps{
				Manager:     lc,
				Store:       fs,
				Sessions:    cpr.sm,
				PhaseRunner: cpr.pr,
				CmdRunner:   cpr.cmd,
			}, orchestrator.Hooks{})
			t.Cleanup(o.WaitForCycles)

			if err := o.StartFeature(f.ID); err != nil {
				t.Fatalf("StartFeature: %v", err)
			}

			if got := loadStoredFeature(t, fs, f.ID).Status; got != feature.StatusPlanning {
				t.Errorf("persisted status = %s, want Planning", got)
			}

			captured := waitForCapturedPhase(t, cpr, tt.phase, 3*time.Second)
			if len(captured) == 0 {
				t.Fatalf("BuildSession was not called with Phase=%v within timeout; all captures: %v",
					tt.phase, cpr.capturedOpts)
			}
		})
	}
}

func TestOrchestrator_StartPlanIncludesResearchDocumentInRoadmapPrompt(t *testing.T) {
	cpr := newCapturingPhaseRunner(t)
	cpr.sm.StartSessionFn = func(id, featureID string, phase feature.Phase,
		command []string, workdir string, env []string,
		opts ...*session.SessionOpts) (ports.SessionHandle, error) {
		return nil, session.ErrShuttingDown
	}

	featureID := "feat-plan-research"
	designDir := filepath.Join(cpr.stateDir, featureID, "design")
	researchDir := filepath.Join(cpr.stateDir, featureID, "research")
	if err := os.MkdirAll(designDir, 0o755); err != nil {
		t.Fatalf("mkdir design dir: %v", err)
	}
	if err := os.MkdirAll(researchDir, 0o755); err != nil {
		t.Fatalf("mkdir research dir: %v", err)
	}
	designPath := filepath.Join(designDir, "design.md")
	researchPath := filepath.Join(researchDir, "research.md")
	if err := os.WriteFile(designPath, []byte("# Design\n"), 0o644); err != nil {
		t.Fatalf("write design: %v", err)
	}
	if err := os.WriteFile(researchPath, []byte("# Research\n"), 0o644); err != nil {
		t.Fatalf("write research: %v", err)
	}

	f := &feature.Feature{
		ID:           featureID,
		Status:       feature.StatusInterrupted,
		CurrentPhase: feature.PhasePlan,
		Pipeline:     feature.PipelineLarge,
		Artifacts:    map[string]string{"design": designPath, "research": researchPath},
		Repos:        []feature.FeatureRepo{{Name: "repo1", Path: cpr.stateDir}},
	}

	fs, lc := newStoredFeatureFixture(t, f)
	cpr.pr.FeatureStore = fs
	o := orchestrator.New(orchestrator.Deps{
		Manager:     lc,
		Store:       fs,
		Sessions:    cpr.sm,
		PhaseRunner: cpr.pr,
		CmdRunner:   cpr.cmd,
	}, orchestrator.Hooks{})
	t.Cleanup(o.WaitForCycles)

	if err := o.StartFeature(featureID); err != nil {
		t.Fatalf("StartFeature: %v", err)
	}

	captured := waitForCapturedPhase(t, cpr, feature.PhasePlan, 3*time.Second)
	if len(captured) == 0 {
		t.Fatalf("no BuildSession captured for PhasePlan; captures: %v", cpr.capturedOpts)
	}
	prompt := captured[0].Prompt
	if !strings.Contains(prompt, "Design Document: "+designPath) {
		t.Fatalf("roadmap prompt missing design document path:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Research Document: "+researchPath) {
		t.Fatalf("roadmap prompt missing research document path:\n%s", prompt)
	}
}

func TestOrchestrator_StartRoadmapPhasePlanIncludesResearchDocument(t *testing.T) {
	cpr := newCapturingPhaseRunner(t)
	cpr.sm.StartSessionFn = func(id, featureID string, phase feature.Phase,
		command []string, workdir string, env []string,
		opts ...*session.SessionOpts) (ports.SessionHandle, error) {
		return nil, session.ErrShuttingDown
	}

	featureID := "feat-phase-plan-research"
	roadmapDir := filepath.Join(cpr.stateDir, featureID, "roadmap")
	researchDir := filepath.Join(cpr.stateDir, featureID, "research")
	if err := os.MkdirAll(roadmapDir, 0o755); err != nil {
		t.Fatalf("mkdir roadmap dir: %v", err)
	}
	if err := os.MkdirAll(researchDir, 0o755); err != nil {
		t.Fatalf("mkdir research dir: %v", err)
	}
	roadmapPath := filepath.Join(roadmapDir, "roadmap.md")
	researchPath := filepath.Join(researchDir, "research.md")
	writeRoadmap(t, roadmapPath)
	if err := os.WriteFile(researchPath, []byte("# Research\n"), 0o644); err != nil {
		t.Fatalf("write research: %v", err)
	}

	f := &feature.Feature{
		ID:                  featureID,
		Status:              feature.StatusPlanning,
		CurrentPhase:        feature.PhasePlan,
		CurrentRoadmapPhase: 2,
		Pipeline:            feature.PipelineMoonshot,
		Artifacts:           map[string]string{"roadmap": roadmapPath, "research": researchPath},
		Repos:               []feature.FeatureRepo{{Name: "repo1", Path: cpr.stateDir}},
	}

	fs, lc := newStoredFeatureFixture(t, f)
	cpr.pr.FeatureStore = fs
	o := orchestrator.New(orchestrator.Deps{
		Manager:     lc,
		Store:       fs,
		Sessions:    cpr.sm,
		PhaseRunner: cpr.pr,
		CmdRunner:   cpr.cmd,
	}, orchestrator.Hooks{})
	t.Cleanup(o.WaitForCycles)

	if err := o.StartFeature(featureID); err != nil {
		t.Fatalf("StartFeature: %v", err)
	}

	captured := waitForCapturedPhase(t, cpr, feature.PhasePlan, 3*time.Second)
	if len(captured) == 0 {
		t.Fatalf("no BuildSession captured for PhasePlan; captures: %v", cpr.capturedOpts)
	}
	prompt := captured[0].Prompt
	if !strings.Contains(prompt, "Roadmap: "+roadmapPath) {
		t.Fatalf("phase-plan prompt missing roadmap path:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Research Document: "+researchPath) {
		t.Fatalf("phase-plan prompt missing research document path:\n%s", prompt)
	}
}

// ---------------------------------------------------------------------------
// Item 6: Implement dispatch — verify StartImplementation lifecycle and that
// PhaseRunner was engaged (via captured BuildSession). RunMultiRepoOrchestrator
// exits cleanly when StartSession returns ErrShuttingDown.
// ---------------------------------------------------------------------------

func TestOrchestrator_StartPhase_PhaseRunnerDispatch_Implement(t *testing.T) {
	cpr := newCapturingPhaseRunner(t)
	cpr.sm.StartSessionFn = func(id, featureID string, phase feature.Phase,
		command []string, workdir string, env []string,
		opts ...*session.SessionOpts) (ports.SessionHandle, error) {
		return nil, session.ErrShuttingDown
	}

	planPath := writeTempFile(t, "plan.md", "plan body")
	f := &feature.Feature{
		ID:           "feat-impl",
		Status:       feature.StatusInterrupted,
		CurrentPhase: feature.PhaseImplement,
		Pipeline:     feature.PipelineLarge,
		Artifacts:    map[string]string{"plan": planPath},
		Repos: []feature.FeatureRepo{
			{Name: "repo1", Path: cpr.stateDir},
		},
	}
	writeExecOrderNextToPlan(t, planPath, f.Repos)

	fs, lc := newStoredFeatureFixture(t, f)
	cpr.pr.FeatureStore = fs
	o := orchestrator.New(orchestrator.Deps{
		Manager:     lc,
		Store:       fs,
		Sessions:    cpr.sm,
		PhaseRunner: cpr.pr,
		CmdRunner:   cpr.cmd,
	}, orchestrator.Hooks{})

	// BuildSession is observed before completion handling finishes writing state.
	t.Cleanup(o.WaitForCycles)

	if err := o.StartFeature(f.ID); err != nil {
		t.Fatalf("StartFeature: %v", err)
	}

	stored := loadStoredFeature(t, fs, f.ID)
	if stored.Status != feature.StatusImplementing || stored.RepoStates["repo1"] == nil {
		t.Errorf("persisted implementation state = status %s, repos %v", stored.Status, stored.RepoStates)
	}

	// Wait for the multi-repo orchestrator goroutine to reach BuildSession.
	captured := waitForCapturedPhase(t, cpr, feature.PhaseImplement, 3*time.Second)
	if len(captured) == 0 {
		t.Fatalf("BuildSession was not called with Phase=PhaseImplement; all captures: %v", cpr.capturedOpts)
	}
}

// ---------------------------------------------------------------------------
// StartFeature fallback — interrupted feature with corrupted/missing
// current_phase (zero value == PhaseResearch) on a Large/Moonshot
// pipeline should route to the pipeline's FirstPhase (Inquire), not Research.
//
// Distinguishes the "legitimately interrupted mid-Research" case (StartedAt
// set → honor CurrentPhase) from "missing current_phase field" (StartedAt
// nil → fall back to FirstPhase). StartedAt survives the startup interrupt
// sweep.
// ---------------------------------------------------------------------------

func TestOrchestrator_StartFeature_InterruptedWithMissingCurrentPhase_FallsBackToFirstPhase(t *testing.T) {
	cpr := newCapturingPhaseRunner(t)

	f := &feature.Feature{
		ID:     "feat-corrupted",
		Status: feature.StatusInterrupted,
		// CurrentPhase deliberately left zero (== PhaseResearch via iota).
		Pipeline: feature.PipelineLarge,
		// StartedAt nil — no phase ever started.
		Repos: nil,
	}
	fs, lc := newStoredFeatureFixture(t, f)
	cpr.pr.FeatureStore = fs

	var observed feature.Phase
	o := orchestrator.New(orchestrator.Deps{
		Manager:     lc,
		Store:       fs,
		Sessions:    cpr.sm,
		PhaseRunner: cpr.pr,
		CmdRunner:   cpr.cmd,
	}, orchestrator.Hooks{
		OnPhaseStarted: func(id string, p feature.Phase) { observed = p },
	})
	t.Cleanup(o.WaitForCycles)

	if err := o.StartFeature(f.ID); err != nil {
		t.Fatalf("StartFeature: %v", err)
	}

	// Nil StartedAt + zero CurrentPhase + Large pipeline → fallback to Inquire.
	if got := loadStoredFeature(t, fs, f.ID).Status; got != feature.StatusInquiring {
		t.Errorf("persisted status = %s, want Inquiring", got)
	}
	if observed != feature.PhaseInquire {
		t.Errorf("OnPhaseStarted phase = %v, want PhaseInquire", observed)
	}
}

// Companion: an interrupted feature with StartedAt set and CurrentPhase==0
// (PhaseResearch) should honor the CurrentPhase — it was legitimately
// interrupted mid-Research. Regression guard: a quit-during-Research feature
// must not rewind back to Inquire on restart.
func TestOrchestrator_StartFeature_InterruptedMidResearch_HonorsCurrentPhase(t *testing.T) {
	cpr := newCapturingPhaseRunner(t)

	started := time.Now().Add(-time.Hour)
	f := &feature.Feature{
		ID:           "feat-midresearch",
		Status:       feature.StatusInterrupted,
		CurrentPhase: feature.PhaseResearch, // iota==0, but the intent is real
		Pipeline:     feature.PipelineLarge,
		// StartedAt disambiguates legitimate-mid-Research from unset CurrentPhase.
		StartedAt: &started,
	}

	// Seed an inquire artifact: startResearch demands the questions file
	// Research is driven from. Without it, the legit-mid-Research resume
	// would error out before we get a chance to assert StartResearch fired.
	inquireDir := filepath.Join(cpr.stateDir, f.ID, "inquire")
	if err := os.MkdirAll(inquireDir, 0o755); err != nil {
		t.Fatalf("mkdir inquire dir: %v", err)
	}
	questionsPath := filepath.Join(inquireDir, "questions.md")
	if err := os.WriteFile(questionsPath, []byte("# Questions\n"), 0o644); err != nil {
		t.Fatalf("write questions: %v", err)
	}
	f.Artifacts = map[string]string{"inquire": questionsPath}

	fs, lc := newStoredFeatureFixture(t, f)
	cpr.pr.FeatureStore = fs

	o := orchestrator.New(orchestrator.Deps{
		Manager:     lc,
		Store:       fs,
		Sessions:    cpr.sm,
		PhaseRunner: cpr.pr,
		CmdRunner:   cpr.cmd,
	}, orchestrator.Hooks{})
	t.Cleanup(o.WaitForCycles)

	if err := o.StartFeature(f.ID); err != nil {
		t.Fatalf("StartFeature: %v", err)
	}

	if got := loadStoredFeature(t, fs, f.ID).Status; got != feature.StatusResearching {
		t.Errorf("persisted status = %s, want Researching", got)
	}
}

// ---------------------------------------------------------------------------
// Item 7: Roadmap-phase plan — StartPlanning is conditional on status
// ---------------------------------------------------------------------------
//
// startRoadmapPhasePlan (orchestrator.go:415-426) only calls
// StartPlanning when the feature is not already StatusPlanning. Both cases
// must be exercised with a real roadmap fixture so CurrentRoadmapPhase and
// ParseRoadmap are actually consumed.

func TestOrchestrator_StartPlan_RoadmapPhase_WhenPlanning_SkipsTransition(t *testing.T) {
	cpr := newCapturingPhaseRunner(t)
	cpr.sm.StartSessionFn = func(id, featureID string, phase feature.Phase,
		command []string, workdir string, env []string,
		opts ...*session.SessionOpts) (ports.SessionHandle, error) {
		return nil, session.ErrShuttingDown
	}

	// Roadmap artifact at stateDir/featureID/roadmap/roadmap.md so
	// resolveArtifactPath discovers it.
	featureID := "feat-roadmap-planning"
	roadmapDir := filepath.Join(cpr.stateDir, featureID, "roadmap")
	if err := os.MkdirAll(roadmapDir, 0o755); err != nil {
		t.Fatalf("mkdir roadmap dir: %v", err)
	}
	roadmapPath := filepath.Join(roadmapDir, "roadmap.md")
	writeRoadmap(t, roadmapPath)

	f := &feature.Feature{
		ID:                  featureID,
		Status:              feature.StatusPlanning, // ← already planning
		CurrentPhase:        feature.PhasePlan,
		CurrentRoadmapPhase: 2,
		Pipeline:            feature.PipelineMoonshot,
		Artifacts:           map[string]string{"roadmap": roadmapPath},
		Repos:               []feature.FeatureRepo{{Name: "repo1", Path: cpr.stateDir}},
	}

	fs, lc := newStoredFeatureFixture(t, f)
	cpr.pr.FeatureStore = fs
	o := orchestrator.New(orchestrator.Deps{
		Manager:     lc,
		Store:       fs,
		Sessions:    cpr.sm,
		PhaseRunner: cpr.pr,
		CmdRunner:   cpr.cmd,
	}, orchestrator.Hooks{})
	t.Cleanup(o.WaitForCycles)

	if err := o.StartFeature(featureID); err != nil {
		t.Fatalf("StartFeature: %v", err)
	}

	if got := loadStoredFeature(t, fs, f.ID).Status; got != feature.StatusPlanning {
		t.Errorf("persisted status = %s, want Planning", got)
	}

	// RunPhasePlanning should still fire — verify a BuildSession capture
	// arrived with Phase=PhasePlan.
	captured := waitForCapturedPhase(t, cpr, feature.PhasePlan, 3*time.Second)
	if len(captured) == 0 {
		t.Fatalf("no BuildSession captured for Phase=PhasePlan; captures: %v", cpr.capturedOpts)
	}
}

func TestOrchestrator_StartPlan_RoadmapPhase_WhenNotPlanning_Transitions(t *testing.T) {
	cpr := newCapturingPhaseRunner(t)
	cpr.sm.StartSessionFn = func(id, featureID string, phase feature.Phase,
		command []string, workdir string, env []string,
		opts ...*session.SessionOpts) (ports.SessionHandle, error) {
		return nil, session.ErrShuttingDown
	}

	featureID := "feat-roadmap-notplanning"
	roadmapDir := filepath.Join(cpr.stateDir, featureID, "roadmap")
	if err := os.MkdirAll(roadmapDir, 0o755); err != nil {
		t.Fatalf("mkdir roadmap dir: %v", err)
	}
	roadmapPath := filepath.Join(roadmapDir, "roadmap.md")
	writeRoadmap(t, roadmapPath)

	f := &feature.Feature{
		ID:                  featureID,
		Status:              feature.StatusPlanReady, // ← not planning yet
		CurrentPhase:        feature.PhasePlan,
		CurrentRoadmapPhase: 2,
		Pipeline:            feature.PipelineMoonshot,
		Artifacts:           map[string]string{"roadmap": roadmapPath},
		Repos:               []feature.FeatureRepo{{Name: "repo1", Path: cpr.stateDir}},
	}

	fs, lc := newStoredFeatureFixture(t, f)
	cpr.pr.FeatureStore = fs
	o := orchestrator.New(orchestrator.Deps{
		Manager:     lc,
		Store:       fs,
		Sessions:    cpr.sm,
		PhaseRunner: cpr.pr,
		CmdRunner:   cpr.cmd,
	}, orchestrator.Hooks{})
	t.Cleanup(o.WaitForCycles)

	if err := o.StartFeature(featureID); err != nil {
		t.Fatalf("StartFeature: %v", err)
	}

	if got := loadStoredFeature(t, fs, f.ID).Status; got != feature.StatusPlanning {
		t.Errorf("persisted status = %s, want Planning", got)
	}

	captured := waitForCapturedPhase(t, cpr, feature.PhasePlan, 3*time.Second)
	if len(captured) == 0 {
		t.Fatalf("no BuildSession captured for Phase=PhasePlan; captures: %v", cpr.capturedOpts)
	}

}
