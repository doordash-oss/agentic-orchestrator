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
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// fanoutQAFile is the synthetic two-record qa-answers.md the plan-loop rows
// pre-write to mimic harness-owned roadmap and phase-plan transcripts already
// persisted by the planner loop. Includes the auto-pick annotation so the
// integration test pins both record shapes.
const fanoutQAFile = `# User Q&A — Phase Clarifications

## Q: What does the user want?
**A:** Option A

## Q: How confident are we in the recommended option?
**A:** Recommended option B
_(auto-picked, confidence: 0.85)_
`

// TestRunPhasePlanning_HighInquireness_GrillMePromptInvariant drives
// startRoadmapPhasePlan with a high-inquireness feature and captures the
// prompt the orchestrator hands to the PhaseRunner via BuildSessionFn.
// Asserts the captured prompt carries the shared policy-free `[grill-me]`
// directive and no longer depends on a prompt-build-time inquireness override.
func TestRunPhasePlanning_HighInquireness_GrillMePromptInvariant(t *testing.T) {
	tmpStateDir := t.TempDir()
	featureID := "feat-phaseplan-high"

	// Feature is in Planning with an active roadmap phase. Inquireness=High
	// would, without the override, route the legacy `[autonomous]` block in.
	f := &feature.Feature{
		ID:                  featureID,
		ActiveRun:           1,
		RunCount:            1,
		Status:              feature.StatusPlanning,
		CurrentPhase:        feature.PhasePlan,
		CurrentRoadmapPhase: 1,
		Pipeline:            feature.PipelineLarge,
		Inquireness:         feature.InquirenessHigh,
		Repos: []feature.FeatureRepo{
			{Name: "repo1", Path: tmpStateDir},
		},
	}

	roadmapDir := filepath.Join(agent.ActiveRunDir(tmpStateDir, f), "roadmap")
	if err := os.MkdirAll(roadmapDir, 0o755); err != nil {
		t.Fatalf("mkdir roadmap: %v", err)
	}
	roadmapPath := filepath.Join(roadmapDir, "roadmap.md")
	roadmapBody := strings.Join([]string{
		"# Roadmap",
		"",
		"## Phase 1: Tracer",
		"",
		"### Goal",
		"Prove the wiring.",
		"",
	}, "\n")
	if err := os.WriteFile(roadmapPath, []byte(roadmapBody), 0o644); err != nil {
		t.Fatalf("write roadmap: %v", err)
	}
	if f.Artifacts == nil {
		f.Artifacts = map[string]string{}
	}
	f.Artifacts["roadmap"] = roadmapPath

	var (
		mu             sync.Mutex
		capturedPrompt string
		captured       bool
	)
	capturedCh := make(chan struct{}, 1)

	sm := mocks.NewMockSessionManager()
	// StartSession should not be reached (BuildSessionFn returns an error
	// to short-circuit the planning loop), but provide a stub anyway.
	sm.StartSessionFn = func(id, fid string, phase feature.Phase,
		command []string, workdir string, env []string,
		opts ...*session.SessionOpts) (ports.SessionHandle, error) {
		return nil, session.ErrShuttingDown
	}

	lc := newGateLifecycle(f)
	fs := newGateFeatureStore(f)

	pr := &agent.PhaseRunner{
		SessionManager: sm,
		FeatureStore:   fs,
		StateDir:       tmpStateDir,
	}
	pr.BuildSessionFn = func(opts agent.BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
		mu.Lock()
		if !captured {
			capturedPrompt = opts.Prompt
			captured = true
			select {
			case capturedCh <- struct{}{}:
			default:
			}
		}
		mu.Unlock()
		return nil, nil, nil, session.ErrShuttingDown
	}

	o := New(Deps{
		Lifecycle:   lc,
		Store:       fs,
		Sessions:    sm,
		PhaseRunner: pr,
	}, Hooks{})

	if _, err := o.startRoadmapPhasePlan(featureID, f); err != nil {
		t.Fatalf("startRoadmapPhasePlan: %v", err)
	}

	select {
	case <-capturedCh:
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("BuildSession was never called within timeout")
	}

	mu.Lock()
	prompt := capturedPrompt
	gotCaptured := captured
	mu.Unlock()
	if !gotCaptured {
		t.Fatalf("BuildSession was never called within timeout")
	}

	if !strings.Contains(prompt, "## Ambiguity Resolution [grill-me]") {
		t.Errorf("phase-plan prompt missing [grill-me] header even with InquirenessHigh; prompt tail:\n%s", promptTail(prompt))
	}
	for _, unwanted := range []string{
		"strictly greater than",
		"auto-pick",
		"auto-resolve",
		"silent",
		"qa-answers.md",
		"threshold",
		"self-rated confidence",
		"strictly greater than 0.7",
		"Do not auto-pick any answer",
		"## Ambiguity Resolution: [autonomous]",
		"## Ambiguity Resolution [autonomous]",
	} {
		if strings.Contains(strings.ToLower(prompt), strings.ToLower(unwanted)) {
			t.Errorf("phase-plan prompt unexpectedly contains %q for InquirenessHigh; prompt tail:\n%s", unwanted, promptTail(prompt))
		}
	}
}

// TestGrillMeFanout_PrimaryBuilders_EndToEnd is the table-driven fan-out
// integration test for the three primary grill-me builders. For each row:
//
//	(i) the test pre-writes a stale qa-answers.md at the canonical phase
//	    directory;
//	(ii) drives HandlePhaseCompletion with the input shape the production
//	    code uses for that phase (BlockingLoopResult-based for Design,
//	    PlanResult-based for Roadmap and Phase-Plan);
//	(iii) asserts all three preserve transcripts already written by their
//	    producer loops;
//	(iv) for Design and Roadmap (which have downstream consumers of
//	    collectQAFilePaths), asserts the path is surfaced to the next phase.
//
// Phase-Plan deliberately does not propagate qa-answers.md downstream
// (see plan §`What We're NOT Doing`), so the phase-plan row asserts
// persistence only.
func TestGrillMeFanout_PrimaryBuilders_EndToEnd(t *testing.T) {
	cases := []struct {
		name             string
		phaseDirRel      func(stateDir string, f *feature.Feature) string
		setupFeature     func() *feature.Feature
		invoke           func(t *testing.T, o *Orchestrator, f *feature.Feature, phaseDir, artifactPath string)
		wantQAFile       string
		assertCollectQA  bool
		expectedQAPathFn func(stateDir string, f *feature.Feature) string
	}{
		{
			name: "design",
			phaseDirRel: func(stateDir string, f *feature.Feature) string {
				return filepath.Join(agent.ActiveRunDir(stateDir, f), "design")
			},
			setupFeature: func() *feature.Feature {
				return &feature.Feature{
					ID:           "feat-fanout-design",
					ActiveRun:    1,
					RunCount:     1,
					Status:       feature.StatusDesigning,
					CurrentPhase: feature.PhaseDesign,
					Pipeline:     feature.PipelineLarge,
					Checkpoints: feature.Checkpoints{
						InquiryReview:  true,
						ResearchReview: true,
						DesignReview:   true,
						PlanReview:     true,
					},
				}
			},
			invoke: func(t *testing.T, o *Orchestrator, f *feature.Feature, phaseDir, artifactPath string) {
				terminalDir := filepath.Dir(artifactPath)
				if err := o.HandlePhaseCompletion(f.ID, PhaseCompletionInput{
					Phase: feature.PhaseDesign,
					DesignResult: &agent.BlockingLoopResult{
						FinalStatus:          agent.BlockingLoopStatusSuccess,
						Iterations:           1,
						TerminalIterationDir: terminalDir,
						CanonicalPath:        artifactPath,
					},
				}); err != nil {
					t.Fatalf("HandlePhaseCompletion design: %v", err)
				}
			},
			wantQAFile:      fanoutQAFile,
			assertCollectQA: true,
			expectedQAPathFn: func(stateDir string, f *feature.Feature) string {
				return filepath.Join(agent.ActiveRunDir(stateDir, f), "design", "qa-answers.md")
			},
		},
		{
			name: "roadmap",
			phaseDirRel: func(stateDir string, f *feature.Feature) string {
				return filepath.Join(agent.ActiveRunDir(stateDir, f), "roadmap")
			},
			setupFeature: func() *feature.Feature {
				return &feature.Feature{
					ID:                  "feat-fanout-roadmap",
					ActiveRun:           1,
					RunCount:            1,
					Status:              feature.StatusPlanning,
					CurrentPhase:        feature.PhasePlan,
					CurrentRoadmapPhase: 0,
					Pipeline:            feature.PipelineLarge,
					Checkpoints: feature.Checkpoints{
						InquiryReview:  true,
						ResearchReview: true,
						DesignReview:   true,
						PlanReview:     true,
					},
				}
			},
			invoke: func(t *testing.T, o *Orchestrator, f *feature.Feature, phaseDir, artifactPath string) {
				if err := o.HandlePhaseCompletion(f.ID, PhaseCompletionInput{
					Phase:      feature.PhasePlan,
					PlanResult: &agent.PlanLoopResult{FinalStatus: "approved"},
				}); err != nil {
					t.Fatalf("HandlePhaseCompletion roadmap: %v", err)
				}
			},
			wantQAFile:      fanoutQAFile,
			assertCollectQA: true,
			expectedQAPathFn: func(stateDir string, f *feature.Feature) string {
				return filepath.Join(agent.ActiveRunDir(stateDir, f), "roadmap", "qa-answers.md")
			},
		},
		{
			name: "phase-plan",
			phaseDirRel: func(stateDir string, f *feature.Feature) string {
				return agent.PhasePlanDir(stateDir, f, 1)
			},
			setupFeature: func() *feature.Feature {
				return &feature.Feature{
					ID:                  "feat-fanout-phaseplan",
					ActiveRun:           1,
					RunCount:            1,
					Status:              feature.StatusPlanning,
					CurrentPhase:        feature.PhasePlan,
					CurrentRoadmapPhase: 1,
					Pipeline:            feature.PipelineLarge,
					Checkpoints: feature.Checkpoints{
						InquiryReview:  true,
						ResearchReview: true,
						DesignReview:   true,
						PlanReview:     true,
					},
				}
			},
			invoke: func(t *testing.T, o *Orchestrator, f *feature.Feature, phaseDir, artifactPath string) {
				if err := o.HandlePhaseCompletion(f.ID, PhaseCompletionInput{
					Phase:      feature.PhasePlan,
					PlanResult: &agent.PlanLoopResult{FinalStatus: "approved"},
				}); err != nil {
					t.Fatalf("HandlePhaseCompletion phase-plan: %v", err)
				}
			},
			wantQAFile:      fanoutQAFile,
			assertCollectQA: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmpStateDir := t.TempDir()
			f := tc.setupFeature()

			phaseDir := tc.phaseDirRel(tmpStateDir, f)
			if err := os.MkdirAll(phaseDir, 0o755); err != nil {
				t.Fatalf("mkdir phaseDir: %v", err)
			}
			// Drop a stand-in artifact so phase-completion routing finds one.
			artifactDir := phaseDir
			if tc.name == "design" {
				artifactDir = filepath.Join(phaseDir, "iteration-01")
				if err := os.MkdirAll(artifactDir, 0o755); err != nil {
					t.Fatalf("mkdir terminal artifact dir: %v", err)
				}
			}
			artifactPath := filepath.Join(artifactDir, tc.name+".md")
			if err := os.WriteFile(artifactPath, []byte("# "+tc.name+"\n"), 0o644); err != nil {
				t.Fatalf("write artifact: %v", err)
			}
			if err := os.WriteFile(filepath.Join(phaseDir, agent.PhaseCompleteFile), nil, 0o644); err != nil {
				t.Fatalf("write phase_complete: %v", err)
			}
			qaPath := filepath.Join(phaseDir, "qa-answers.md")
			if err := os.WriteFile(qaPath, []byte(fanoutQAFile), 0o644); err != nil {
				t.Fatalf("pre-write qa-answers.md: %v", err)
			}

			lc := newGateLifecycle(f)
			fs := newGateFeatureStore(f)
			sm := sessionManagerWithQALog("sess-1")
			pr := &agent.PhaseRunner{StateDir: tmpStateDir}

			o := New(Deps{Lifecycle: lc, Store: fs, Sessions: sm, PhaseRunner: pr}, Hooks{})

			tc.invoke(t, o, f, phaseDir, artifactPath)

			got, err := os.ReadFile(qaPath)
			if err != nil {
				t.Fatalf("read qa-answers.md after gate fired: %v", err)
			}
			if string(got) != tc.wantQAFile {
				t.Errorf("[%s] qa-answers.md content mismatch after orchestrator gate.\n--- got ---\n%s\n--- want ---\n%s", tc.name, got, tc.wantQAFile)
			}
			if strings.Contains(tc.wantQAFile, "_(auto-picked, confidence: 0.85)_") && !strings.Contains(string(got), "_(auto-picked, confidence: 0.85)_") {
				t.Errorf("[%s] auto-pick annotation lost:\n%s", tc.name, got)
			}

			if tc.assertCollectQA {
				expected := tc.expectedQAPathFn(tmpStateDir, f)
				paths := o.collectQAFilePaths(f, f.RefactorPrefix())
				found := false
				for _, p := range paths {
					if p == expected {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("[%s] collectQAFilePaths did not include %q; got %v", tc.name, expected, paths)
				}
			}
		})
	}
}

// promptTail returns the last 1500 bytes of a prompt for diagnostic display
// when an assertion fails. Mirrors the helper in
// internal/tui/grill_me_smoke_test.go.
func promptTail(prompt string) string {
	const tailBytes = 1500
	if len(prompt) <= tailBytes {
		return prompt
	}
	return "…" + prompt[len(prompt)-tailBytes:]
}

// TestGrillMeFanout_StartPaths_DriveEntryPath drives all three primary
// grill-me orchestrator entry points (startDesign, startPlan,
// startRoadmapPhasePlan) via the BuildSessionFn capture pattern and asserts
// each entry path produces the shared policy-free grill-me directive. This
// complements TestGrillMeFanout_PrimaryBuilders_EndToEnd's persistence-gate
// coverage by proving the orchestrator entry path actually wires the invariant
// builder into the captured prompt for Design, Roadmap, and Phase-Plan.
//
// The matrix uses Inquireness == medium to prove none of the entry paths leak
// medium-level auto-pick policy into the agent-facing prompt.
func TestGrillMeFanout_StartPaths_DriveEntryPath(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping grill-me start-path fan-out drive in short mode; fast suite keeps prompt-capture and persistence-gate contract tests")
	}

	cases := []struct {
		name                 string
		setupFeature         func(stateDir string) *feature.Feature
		seedUpstreamArtifact func(t *testing.T, stateDir string, f *feature.Feature)
		invoke               func(t *testing.T, o *Orchestrator, f *feature.Feature)
		expectDispatchFail   bool
		mustContain          []string
		mustNotContain       []string
	}{
		{
			name: "design",
			setupFeature: func(stateDir string) *feature.Feature {
				return &feature.Feature{
					ID:           "feat-entry-design",
					ActiveRun:    1,
					RunCount:     1,
					Status:       feature.StatusDesignReady,
					CurrentPhase: feature.PhaseDesign,
					Pipeline:     feature.PipelineLarge,
					Inquireness:  feature.InquirenessMedium,
					Repos: []feature.FeatureRepo{
						{Name: "repo1", Path: stateDir},
					},
				}
			},
			seedUpstreamArtifact: func(t *testing.T, stateDir string, f *feature.Feature) {
				researchDir := filepath.Join(agent.ActiveRunDir(stateDir, f), "research")
				if err := os.MkdirAll(researchDir, 0o755); err != nil {
					t.Fatalf("mkdir research: %v", err)
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
			invoke: func(t *testing.T, o *Orchestrator, f *feature.Feature) {
				if _, err := o.startDesign(f.ID); err != nil {
					t.Fatalf("startDesign synchronous failure: %v", err)
				}
			},
			expectDispatchFail: true,
			mustContain: []string{
				"## Ambiguity Resolution [grill-me]",
			},
			mustNotContain: []string{
				"strictly greater than",
				"auto-pick",
				"auto-resolve",
				"silent",
				"qa-answers.md",
				"threshold",
				"strictly greater than 0.5",
				"Do not auto-pick any answer",
				"## Ambiguity Resolution: [autonomous]",
				"## Ambiguity Resolution [autonomous]",
			},
		},
		{
			name: "roadmap",
			setupFeature: func(stateDir string) *feature.Feature {
				return &feature.Feature{
					ID:                  "feat-entry-roadmap",
					ActiveRun:           1,
					RunCount:            1,
					Status:              feature.StatusPlanReady,
					CurrentPhase:        feature.PhasePlan,
					CurrentRoadmapPhase: 0,
					Pipeline:            feature.PipelineLarge,
					Inquireness:         feature.InquirenessMedium,
					Repos: []feature.FeatureRepo{
						{Name: "repo1", Path: stateDir},
					},
				}
			},
			seedUpstreamArtifact: func(t *testing.T, stateDir string, f *feature.Feature) {
				designDir := filepath.Join(agent.ActiveRunDir(stateDir, f), "design")
				if err := os.MkdirAll(designDir, 0o755); err != nil {
					t.Fatalf("mkdir design: %v", err)
				}
				designPath := filepath.Join(designDir, "design.md")
				if err := os.WriteFile(designPath, []byte("# design\n"), 0o644); err != nil {
					t.Fatalf("write design: %v", err)
				}
				if f.Artifacts == nil {
					f.Artifacts = map[string]string{}
				}
				f.Artifacts["design"] = designPath
			},
			invoke: func(t *testing.T, o *Orchestrator, f *feature.Feature) {
				if _, err := o.startPlan(f.ID); err != nil {
					// Roadmap planning runs the loop in a goroutine, so the
					// synchronous return is nil — the BuildSessionFn error
					// surfaces as a "failed" PlanLoopResult later, observed
					// via OnFeatureFailed.
					t.Fatalf("startPlan synchronous failure: %v", err)
				}
			},
			expectDispatchFail: true,
			mustContain: []string{
				"## Ambiguity Resolution [grill-me]",
			},
			mustNotContain: []string{
				"strictly greater than",
				"auto-pick",
				"auto-resolve",
				"silent",
				"qa-answers.md",
				"threshold",
				"strictly greater than 0.5",
				"Do not auto-pick any answer",
				"## Ambiguity Resolution: [autonomous]",
				"## Ambiguity Resolution [autonomous]",
			},
		},
		{
			name: "phase-plan",
			setupFeature: func(stateDir string) *feature.Feature {
				return &feature.Feature{
					ID:                  "feat-entry-phaseplan",
					ActiveRun:           1,
					RunCount:            1,
					Status:              feature.StatusPlanning,
					CurrentPhase:        feature.PhasePlan,
					CurrentRoadmapPhase: 1,
					Pipeline:            feature.PipelineLarge,
					Inquireness:         feature.InquirenessMedium,
					Repos: []feature.FeatureRepo{
						{Name: "repo1", Path: stateDir},
					},
				}
			},
			seedUpstreamArtifact: func(t *testing.T, stateDir string, f *feature.Feature) {
				roadmapDir := filepath.Join(agent.ActiveRunDir(stateDir, f), "roadmap")
				if err := os.MkdirAll(roadmapDir, 0o755); err != nil {
					t.Fatalf("mkdir roadmap: %v", err)
				}
				roadmapPath := filepath.Join(roadmapDir, "roadmap.md")
				roadmapBody := strings.Join([]string{
					"# Roadmap", "",
					"## Phase 1: Tracer", "",
					"### Goal", "Prove the wiring.", "",
				}, "\n")
				if err := os.WriteFile(roadmapPath, []byte(roadmapBody), 0o644); err != nil {
					t.Fatalf("write roadmap: %v", err)
				}
				if f.Artifacts == nil {
					f.Artifacts = map[string]string{}
				}
				f.Artifacts["roadmap"] = roadmapPath
			},
			invoke: func(t *testing.T, o *Orchestrator, f *feature.Feature) {
				if _, err := o.startRoadmapPhasePlan(f.ID, f); err != nil {
					t.Fatalf("startRoadmapPhasePlan synchronous failure: %v", err)
				}
			},
			expectDispatchFail: true,
			mustContain: []string{
				"## Ambiguity Resolution [grill-me]",
			},
			mustNotContain: []string{
				"strictly greater than",
				"auto-pick",
				"auto-resolve",
				"silent",
				"qa-answers.md",
				"threshold",
				"strictly greater than 0.7",
				"Do not auto-pick any answer",
				"## Ambiguity Resolution: [autonomous]",
				"## Ambiguity Resolution [autonomous]",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmpStateDir := t.TempDir()
			f := tc.setupFeature(tmpStateDir)
			tc.seedUpstreamArtifact(t, tmpStateDir, f)

			var (
				mu             sync.Mutex
				capturedPrompt string
				captured       bool
			)

			sm := mocks.NewMockSessionManager()
			sm.StartSessionFn = func(id, fid string, phase feature.Phase,
				command []string, workdir string, env []string,
				opts ...*session.SessionOpts) (ports.SessionHandle, error) {
				return nil, session.ErrShuttingDown
			}

			lc := newGateLifecycle(f)
			// Plan / phase-plan dispatch a "failed" PlanLoopResult once
			// BuildSession returns ErrShuttingDown; the orchestrator routes
			// that through markFailedWithEvent. Provide MarkFailed so the
			// dispatch goroutine completes cleanly.
			lc.MarkFailedFn = func(id, ft, msg string) error {
				f.Status = feature.StatusFailed
				return nil
			}

			fs := newGateFeatureStore(f)

			pr := &agent.PhaseRunner{
				SessionManager: sm,
				FeatureStore:   fs,
				StateDir:       tmpStateDir,
			}
			pr.BuildSessionFn = func(opts agent.BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
				mu.Lock()
				if !captured {
					capturedPrompt = opts.Prompt
					captured = true
				}
				mu.Unlock()
				return nil, nil, nil, session.ErrShuttingDown
			}

			failedSignal := make(chan struct{}, 1)
			hooks := Hooks{
				OnFeatureFailed: func(featureID, failureType, errorMsg string) {
					select {
					case failedSignal <- struct{}{}:
					default:
					}
				},
			}

			o := New(Deps{
				Lifecycle:   lc,
				Store:       fs,
				Sessions:    sm,
				PhaseRunner: pr,
			}, hooks)

			tc.invoke(t, o, f)

			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				mu.Lock()
				got := captured
				mu.Unlock()
				if got {
					break
				}
				// Retained: bounded poll interval for asynchronous prompt capture.
				time.Sleep(10 * time.Millisecond)
			}

			mu.Lock()
			prompt := capturedPrompt
			gotCaptured := captured
			mu.Unlock()
			if !gotCaptured {
				t.Fatalf("[%s] BuildSession was never called within timeout — entry path did not reach the builder", tc.name)
			}

			for _, want := range tc.mustContain {
				if !strings.Contains(prompt, want) {
					t.Errorf("[%s] prompt missing %q; tail:\n%s", tc.name, want, promptTail(prompt))
				}
			}
			for _, unwanted := range tc.mustNotContain {
				if strings.Contains(strings.ToLower(prompt), strings.ToLower(unwanted)) {
					t.Errorf("[%s] prompt unexpectedly contains %q for InquirenessMedium; tail:\n%s", tc.name, unwanted, promptTail(prompt))
				}
			}

			if tc.expectDispatchFail {
				select {
				case <-failedSignal:
				case <-time.After(3 * time.Second):
					t.Errorf("[%s] dispatch goroutine never reached OnFeatureFailed; planning loop may have leaked", tc.name)
				}
			}
		})
	}
}
