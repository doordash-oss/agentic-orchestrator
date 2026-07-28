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
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// ---------------------------------------------------------------------------
// stubSessionHandle — minimal session.SessionHandle used by PhaseRunner
// wiring tests. All mutation methods are no-ops; read methods return zero.
//
// The PhaseRunner code that runs after MockSessionManager.StartSession
// returns the handle uses only a narrow slice of SessionHandle — it
// registers cleanup callbacks, sets a log file, and registers a tool-read
// tracker. The stub satisfies those calls without spinning up a real PTY.
// ---------------------------------------------------------------------------

type stubSessionHandle struct {
	id        string
	featureID string
	phase     feature.Phase
	repoName  string
	done      chan struct{}
	statusCh  chan string
	attachCh  chan llm.SDKMessage
	msgLog    *session.MessageLog
}

func newStubSessionHandle(id, featureID string, phase feature.Phase, repoName string) *stubSessionHandle {
	return &stubSessionHandle{
		id:        id,
		featureID: featureID,
		phase:     phase,
		repoName:  repoName,
		done:      make(chan struct{}),
		statusCh:  make(chan string, 1),
		attachCh:  make(chan llm.SDKMessage, 1),
		msgLog:    session.NewMessageLog(),
	}
}

// SessionView identity/metadata.
func (s *stubSessionHandle) ID() string              { return s.id }
func (s *stubSessionHandle) FeatureID() string       { return s.featureID }
func (s *stubSessionHandle) Phase() feature.Phase    { return s.phase }
func (s *stubSessionHandle) RepoName() string        { return s.repoName }
func (s *stubSessionHandle) PermCacheScope() string  { return "" }
func (s *stubSessionHandle) Kind() ports.SessionKind { return ports.KindPhase }
func (s *stubSessionHandle) Label() string           { return "" }

// SessionView state.
func (s *stubSessionHandle) Status() session.SessionStatus    { return session.SessionRunning }
func (s *stubSessionHandle) IsActive() bool                   { return true }
func (s *stubSessionHandle) Iteration() int                   { return 0 }
func (s *stubSessionHandle) StartedAt() time.Time             { return time.Time{} }
func (s *stubSessionHandle) InitialPrompt() string            { return "" }
func (s *stubSessionHandle) ProviderName() string             { return "" }
func (s *stubSessionHandle) Model() string                    { return "" }
func (s *stubSessionHandle) WorkDir() string                  { return "" }
func (s *stubSessionHandle) EffectiveEffort() llm.EffortLevel { return "" }
func (s *stubSessionHandle) EffortSource() llm.EffortSource   { return "" }

// SessionView data access.
func (s *stubSessionHandle) MessageLog() ports.MessageLog                         { return s.msgLog }
func (s *stubSessionHandle) Cost() *llm.ResultMessage                             { return nil }
func (s *stubSessionHandle) LatestUsage() *llm.Usage                              { return nil }
func (s *stubSessionHandle) AccumulatedUsage() llm.Usage                          { return llm.Usage{} }
func (s *stubSessionHandle) LastControlRequest() *llm.ControlRequestMessage       { return nil }
func (s *stubSessionHandle) PendingControlRequests() []*llm.ControlRequestMessage { return nil }
func (s *stubSessionHandle) QALog() []session.QAPair                              { return nil }
func (s *stubSessionHandle) LogFilePath() string                                  { return "" }
func (s *stubSessionHandle) ContextPercentage() int                               { return 0 }
func (s *stubSessionHandle) ErrorDetail() string                                  { return "" }
func (s *stubSessionHandle) ExitCodeDetail() string                               { return "" }
func (s *stubSessionHandle) LastStdoutAt() time.Time                              { return time.Time{} }

// SessionView channels.
func (s *stubSessionHandle) StatusCh() <-chan string         { return s.statusCh }
func (s *stubSessionHandle) AttachCh() <-chan llm.SDKMessage { return s.attachCh }
func (s *stubSessionHandle) Done() <-chan struct{}           { return s.done }

// SessionView query.
func (s *stubSessionHandle) HasPendingAskUserQuestion() bool { return false }

// SessionView interaction (no-ops).
func (s *stubSessionHandle) SendUserMessage(text string) error { return nil }
func (s *stubSessionHandle) RespondToControl(requestID string, allow bool, reason string) error {
	return nil
}
func (s *stubSessionHandle) RespondToAskUser(requestID string, questions json.RawMessage, answers map[string]string, annotations map[string]llm.AskUserAnnotation) error {
	return nil
}
func (s *stubSessionHandle) ClearPendingQuestion(requestID string) {}
func (s *stubSessionHandle) ResetWaitingStatus()                   {}
func (s *stubSessionHandle) Stop() error                           { return nil }
func (s *stubSessionHandle) Interrupt() error                      { return nil }
func (s *stubSessionHandle) Wait()                                 {}

// SessionHandle mutation (no-ops).
func (s *stubSessionHandle) SetStatus(status session.SessionStatus) {}
func (s *stubSessionHandle) SetLogFile(f *os.File)                  {}
func (s *stubSessionHandle) AddCleanupFunc(fn func())               {}
func (s *stubSessionHandle) SetHasUnansweredQuestion(v bool)        {}
func (s *stubSessionHandle) CloseStdin()                            {}
func (s *stubSessionHandle) SetOnToolAllowed(fn func(toolName string, input json.RawMessage)) {
}
func (s *stubSessionHandle) SetOnFileRead(fn func(read llm.FileReadEvent))  {}
func (s *stubSessionHandle) SetOnSubagentEvent(fn func(msg llm.SDKMessage)) {}

// Compile-time check that stubSessionHandle satisfies SessionHandle.
var _ session.SessionHandle = (*stubSessionHandle)(nil)

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
	startCalls   []mocks.MockStartSessionCall
}

func newCapturingPhaseRunner(t *testing.T) *capturingPhaseRunner {
	t.Helper()
	stateDir := t.TempDir()
	sm := mocks.NewMockSessionManager()
	cmd := mocks.NewMockCommandRunner()
	// Default: git rev-parse HEAD returns a stable commit so KB freshness
	// can be observed as non-fresh (we don't seed state.json by default).
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
		FeatureStore:   mocks.NewMockFeatureStore(),
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
		cpr.mu.Lock()
		cpr.startCalls = append(cpr.startCalls, mocks.MockStartSessionCall{
			ID:        id,
			FeatureID: featureID,
			Phase:     phase,
			Command:   append([]string(nil), command...),
		})
		cpr.mu.Unlock()
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
// multiple BuildSession invocations (e.g. one per repo in KB).
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
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []mocks.MockStartSessionCall
	for _, call := range c.startCalls {
		if call.Phase == want {
			out = append(out, call)
		}
	}
	return out
}

// seedFreshKB writes a state.json + index.md under the KB dir for repoName
// so that agent.IsKBFresh returns true when paired with a CommandRunner
// that echoes the same head commit. Returns the kbDir path.
func seedFreshKB(t *testing.T, stateDir, repoName, headCommit string) string {
	t.Helper()
	kbDir := agent.KBStateDir(stateDir, repoName)
	if err := os.MkdirAll(kbDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", kbDir, err)
	}
	// Write state.json with matching head commit.
	stateData := map[string]any{
		"head_commit":  headCommit,
		"last_updated": time.Now().UTC().Format(time.RFC3339),
		"version":      1,
	}
	b, err := json.Marshal(stateData)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(kbDir, "state.json"), b, 0o644); err != nil {
		t.Fatalf("write state.json: %v", err)
	}
	// Write a non-empty index.md so the existence check passes.
	if err := os.WriteFile(agent.KBPath(kbDir), []byte("# KB\n"), 0o644); err != nil {
		t.Fatalf("write index.md: %v", err)
	}
	return kbDir
}

// countLifecycleCalls counts recorded calls whose method matches name.
func countLifecycleCalls(lc *mocks.MockFeatureLifecycle, method string) int {
	count := 0
	for _, c := range lc.Calls {
		if c.Method == method {
			count++
		}
	}
	return count
}

// lifecycleCallArgs returns the first recorded call for method, or nil.
func lifecycleCallArgs(lc *mocks.MockFeatureLifecycle, method string) []any {
	for _, c := range lc.Calls {
		if c.Method == method {
			return c.Args
		}
	}
	return nil
}

// writeRoadmap writes a minimal roadmap document with two phases to path.
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
// Item 5: KB per-repo fan-out (mixed-fresh case)
// ---------------------------------------------------------------------------
//
// With 3 repos where 1 is fresh (state.json + index.md match the runner's
// HEAD commit) and 2 are not, startKB must:
//   - transition via StartKnowledgeBase + InitKBStatus exactly once
//   - NOT call RunKnowledgeBaseForRepo for the fresh repo — instead
//     MarkRepoKBCompleted is recorded for that repo
//   - call RunKnowledgeBaseForRepo for each non-fresh repo, which propagates
//     to MockSessionManager.StartSession with phase=PhaseKnowledgeBase and
//     session IDs of the form "<featureID>-kb-<repoName>"
//
// This exercises the per-repo fan-out through a real PhaseRunner instead of
// the "not observable" path called out in iteration-01 review feedback.

func TestOrchestrator_StartKB_MixedFresh_FansOutPerRepo(t *testing.T) {
	cpr := newCapturingPhaseRunner(t)

	// Seed repo-fresh as fresh against headCommit "deadbeef" — the default
	// MockCommandRunner response.
	seedFreshKB(t, cpr.stateDir, "repo-fresh", "deadbeef")
	// repo-stale and repo-dirty are left without state.json so they are
	// treated as not fresh by IsKBFresh.

	f := &feature.Feature{
		ID:           "feat-mixed",
		Status:       feature.StatusInterrupted,
		CurrentPhase: feature.PhaseKnowledgeBase,
		Pipeline:     feature.PipelineLarge,
		Repos: []feature.FeatureRepo{
			{Name: "repo-fresh", Path: "/tmp/repo-fresh"},
			{Name: "repo-stale", Path: "/tmp/repo-stale"},
			{Name: "repo-dirty", Path: "/tmp/repo-dirty"},
		},
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)
	cpr.pr.FeatureStore = fs

	var startedPhases []feature.Phase
	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
		Store:       fs,
		Sessions:    cpr.sm,
		PhaseRunner: cpr.pr,
		CmdRunner:   cpr.cmd,
	}, orchestrator.Hooks{
		OnPhaseStarted: func(id string, p feature.Phase) {
			startedPhases = append(startedPhases, p)
		},
	})

	if err := o.StartFeature("feat-mixed"); err != nil {
		t.Fatalf("StartFeature: %v", err)
	}

	// Transition/init fired exactly once.
	if n := countLifecycleCalls(lc, "StartKnowledgeBase"); n != 1 {
		t.Errorf("StartKnowledgeBase calls = %d, want 1", n)
	}
	if n := countLifecycleCalls(lc, "InitKBStatus"); n != 1 {
		t.Errorf("InitKBStatus calls = %d, want 1", n)
	}

	// Exactly one MarkRepoKBCompleted — for the fresh repo.
	if n := countLifecycleCalls(lc, "MarkRepoKBCompleted"); n != 1 {
		t.Errorf("MarkRepoKBCompleted calls = %d, want 1", n)
	}
	if args := lifecycleCallArgs(lc, "MarkRepoKBCompleted"); len(args) >= 2 {
		if repoName, ok := args[1].(string); !ok || repoName != "repo-fresh" {
			t.Errorf("MarkRepoKBCompleted repoName = %v, want repo-fresh", args[1])
		}
	}

	// Two KB sessions started — one per non-fresh repo.
	kbSessions := cpr.startSessionsByPhase(feature.PhaseKnowledgeBase)
	if len(kbSessions) != 2 {
		t.Fatalf("KB StartSession calls = %d, want 2", len(kbSessions))
	}
	gotRepoIDs := map[string]bool{}
	for _, call := range kbSessions {
		if call.FeatureID != "feat-mixed" {
			t.Errorf("KB session featureID = %q, want feat-mixed", call.FeatureID)
		}
		gotRepoIDs[call.ID] = true
	}
	for _, want := range []string{"feat-mixed-kb-repo-stale", "feat-mixed-kb-repo-dirty"} {
		if !gotRepoIDs[want] {
			t.Errorf("missing KB session with ID %q; got %v", want, gotRepoIDs)
		}
	}
	if gotRepoIDs["feat-mixed-kb-repo-fresh"] {
		t.Error("fresh repo should NOT get a KB StartSession call")
	}

	// BuildSession should have been invoked twice for phase=KB, once per
	// non-fresh repo, with distinct RepoName values.
	kbBuilds := cpr.capturedByPhase(feature.PhaseKnowledgeBase)
	if len(kbBuilds) != 2 {
		t.Fatalf("BuildSession calls for KB = %d, want 2", len(kbBuilds))
	}
	buildRepos := map[string]bool{}
	for _, b := range kbBuilds {
		buildRepos[b.RepoName] = true
	}
	for _, want := range []string{"repo-stale", "repo-dirty"} {
		if !buildRepos[want] {
			t.Errorf("BuildSession(KB) missing RepoName %q; got %v", want, buildRepos)
		}
	}

	// PhaseStarted hook fired exactly once for KB.
	if len(startedPhases) != 1 || startedPhases[0] != feature.PhaseKnowledgeBase {
		t.Errorf("startedPhases = %v, want [PhaseKnowledgeBase]", startedPhases)
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

			lc := lifecycleForFeature(f)
			if tt.phase == feature.PhaseDesign {
				lc.StartDesignFn = func(id string) error {
					f.Status = feature.StatusDesigning
					f.CurrentPhase = feature.PhaseDesign
					return nil
				}
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

			if err := o.StartFeature(f.ID); err != nil {
				t.Fatalf("StartFeature: %v", err)
			}

			if countLifecycleCalls(lc, tt.wantTransition) != 1 {
				t.Errorf("expected exactly one %s call; got %v", tt.wantTransition, lifecycleCallNames(lc))
			}

			captured := cpr.capturedByPhase(tt.phase)
			if tt.phase == feature.PhaseDesign {
				captured = waitForCapturedPhase(t, cpr, tt.phase, 3*time.Second)
			}
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

			lc := withStatusTransitions(lifecycleForFeature(f), f)
			fs := newFeatureStore(f)
			cpr.pr.FeatureStore = fs
			o := orchestrator.New(orchestrator.Deps{
				Lifecycle:   lc,
				Store:       fs,
				Sessions:    cpr.sm,
				PhaseRunner: cpr.pr,
				CmdRunner:   cpr.cmd,
			}, orchestrator.Hooks{})

			if err := o.StartFeature(f.ID); err != nil {
				t.Fatalf("StartFeature: %v", err)
			}

			if countLifecycleCalls(lc, tt.wantTransition) != 1 {
				t.Errorf("expected exactly one %s call; got %v", tt.wantTransition, lifecycleCallNames(lc))
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

	lc := withStatusTransitions(lifecycleForFeature(f), f)
	fs := newFeatureStore(f)
	cpr.pr.FeatureStore = fs
	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
		Store:       fs,
		Sessions:    cpr.sm,
		PhaseRunner: cpr.pr,
		CmdRunner:   cpr.cmd,
	}, orchestrator.Hooks{})

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

	lc := withStatusTransitions(lifecycleForFeature(f), f)
	fs := newFeatureStore(f)
	cpr.pr.FeatureStore = fs
	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
		Store:       fs,
		Sessions:    cpr.sm,
		PhaseRunner: cpr.pr,
		CmdRunner:   cpr.cmd,
	}, orchestrator.Hooks{})

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

	lc := withStatusTransitions(lifecycleForFeature(f), f)
	fs := newFeatureStore(f)
	cpr.pr.FeatureStore = fs
	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
		Store:       fs,
		Sessions:    cpr.sm,
		PhaseRunner: cpr.pr,
		CmdRunner:   cpr.cmd,
	}, orchestrator.Hooks{})

	if err := o.StartFeature(f.ID); err != nil {
		t.Fatalf("StartFeature: %v", err)
	}

	// Synchronous lifecycle transitions fire before the goroutine launches.
	if countLifecycleCalls(lc, "StartImplementation") != 1 {
		t.Errorf("expected exactly one StartImplementation call; got %v", lifecycleCallNames(lc))
	}
	if countLifecycleCalls(lc, "InitRepoImpl") != 1 {
		t.Errorf("expected exactly one InitRepoImpl call; got %v", lifecycleCallNames(lc))
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
// pipeline should route to the pipeline's FirstPhase (KB), not Research.
//
// Distinguishes the "legitimately interrupted mid-Research" case (StartedAt
// set → honor CurrentPhase) from "missing current_phase field" (StartedAt
// nil → fall back to FirstPhase). StartedAt survives the startup interrupt
// sweep, unlike KBStatus.
// ---------------------------------------------------------------------------

func TestOrchestrator_StartFeature_InterruptedWithMissingCurrentPhase_FallsBackToFirstPhase(t *testing.T) {
	cpr := newCapturingPhaseRunner(t)

	f := &feature.Feature{
		ID:     "feat-corrupted",
		Status: feature.StatusInterrupted,
		// CurrentPhase deliberately left zero (== PhaseResearch via iota).
		Pipeline: feature.PipelineLarge,
		// StartedAt nil — no phase ever started.
		Repos: nil, // empty repos so startKB skips straight to Inquire
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)
	cpr.pr.FeatureStore = fs

	var observed feature.Phase
	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
		Store:       fs,
		Sessions:    cpr.sm,
		PhaseRunner: cpr.pr,
		CmdRunner:   cpr.cmd,
	}, orchestrator.Hooks{
		OnPhaseStarted: func(id string, p feature.Phase) { observed = p },
	})

	if err := o.StartFeature(f.ID); err != nil {
		t.Fatalf("StartFeature: %v", err)
	}

	// Nil StartedAt + zero CurrentPhase + Large pipeline → fallback to KB.
	// With no repos, startKB cleanly skips to Inquire → StartInquire fires.
	if countLifecycleCalls(lc, "StartInquire") != 1 {
		t.Errorf("expected StartInquire via fallback; got calls: %v", lifecycleCallNames(lc))
	}
	if countLifecycleCalls(lc, "StartResearch") != 0 {
		t.Errorf("StartResearch should NOT fire when CurrentPhase==0 is treated as unset")
	}
	if observed != feature.PhaseInquire {
		t.Errorf("OnPhaseStarted phase = %v, want PhaseInquire (KB skipped to Inquire)", observed)
	}
}

// Companion: an interrupted feature with StartedAt set and CurrentPhase==0
// (PhaseResearch) should honor the CurrentPhase — it was legitimately
// interrupted mid-Research. Regression guard: the startup interrupt sweep
// clears KBStatus, so the heuristic must NOT key off KBStatus, otherwise a
// quit-during-Research feature rewinds all the way back to Inquire/KB on
// restart.
func TestOrchestrator_StartFeature_InterruptedMidResearch_HonorsCurrentPhase(t *testing.T) {
	cpr := newCapturingPhaseRunner(t)

	started := time.Now().Add(-time.Hour)
	f := &feature.Feature{
		ID:           "feat-midresearch",
		Status:       feature.StatusInterrupted,
		CurrentPhase: feature.PhaseResearch, // iota==0, but the intent is real
		Pipeline:     feature.PipelineLarge,
		StartedAt:    &started,
		// KBStatus deliberately empty — simulates the post-sweep state where
		// InterruptAllRunning cleared it. StartedAt is the signal that
		// disambiguates legitimate-mid-Research from unset CurrentPhase.
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

	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)
	cpr.pr.FeatureStore = fs

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
		Store:       fs,
		Sessions:    cpr.sm,
		PhaseRunner: cpr.pr,
		CmdRunner:   cpr.cmd,
	}, orchestrator.Hooks{})

	if err := o.StartFeature(f.ID); err != nil {
		t.Fatalf("StartFeature: %v", err)
	}

	if countLifecycleCalls(lc, "StartResearch") != 1 {
		t.Errorf("expected StartResearch (legit mid-Research resume); got calls: %v", lifecycleCallNames(lc))
	}
	if countLifecycleCalls(lc, "StartKnowledgeBase") != 0 {
		t.Errorf("KB should NOT restart when StartedAt signals the feature already started")
	}
	if countLifecycleCalls(lc, "StartInquire") != 0 {
		t.Errorf("Inquire should NOT restart when StartedAt signals the feature already started")
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

	lc := withStatusTransitions(lifecycleForFeature(f), f)
	fs := newFeatureStore(f)
	cpr.pr.FeatureStore = fs
	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
		Store:       fs,
		Sessions:    cpr.sm,
		PhaseRunner: cpr.pr,
		CmdRunner:   cpr.cmd,
	}, orchestrator.Hooks{})

	if err := o.StartFeature(featureID); err != nil {
		t.Fatalf("StartFeature: %v", err)
	}

	// StartPlanning must NOT be called — feature is already StatusPlanning.
	if n := countLifecycleCalls(lc, "StartPlanning"); n != 0 {
		t.Errorf("StartPlanning calls = %d, want 0 (feature already in StatusPlanning)", n)
	}

	// RunPhasePlanning should still fire — verify a BuildSession capture
	// arrived with Phase=PhasePlan.
	captured := waitForCapturedPhase(t, cpr, feature.PhasePlan, 3*time.Second)
	if len(captured) == 0 {
		t.Fatalf("no BuildSession captured for Phase=PhasePlan; captures: %v", cpr.capturedOpts)
	}
}

func TestOrchestrator_StartPlan_UsesLegacyBrainstormArtifactDir(t *testing.T) {
	stateDir := t.TempDir()
	store := feature.NewStore(stateDir)
	featureID := "feat-legacy-brainstorm-plan"
	f := &feature.Feature{
		ID:            featureID,
		Name:          "Legacy Brainstorm Plan",
		Slug:          "legacy-brainstorm-plan",
		Description:   "resume planning from a pre-rename brainstorm artifact",
		Created:       time.Now().Truncate(time.Second),
		Status:        feature.StatusPlanReady,
		CurrentPhase:  feature.PhasePlan,
		Pipeline:      feature.PipelineLarge,
		Inquireness:   feature.InquirenessMedium,
		Repos:         []feature.FeatureRepo{{Name: "repo1", Path: stateDir}},
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}

	brainstormDir := filepath.Join(agent.ActiveRunDir(stateDir, f), "brainstorm")
	if err := os.MkdirAll(brainstormDir, 0o755); err != nil {
		t.Fatalf("mkdir brainstorm dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(brainstormDir, "brainstorm.md"), []byte("# Legacy Brainstorm\n"), 0o644); err != nil {
		t.Fatalf("write brainstorm artifact: %v", err)
	}

	mgr := feature.NewManager(store, nil)
	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: mgr,
		Store:     store,
	}, orchestrator.Hooks{})

	if err := o.StartFeature(featureID); err != nil {
		t.Fatalf("StartFeature() error = %v; want nil", err)
	}

	loaded, err := store.Load(featureID)
	if err != nil {
		t.Fatalf("load feature: %v", err)
	}
	if loaded.Status != feature.StatusPlanning {
		t.Errorf("loaded.Status = %v, want %v", loaded.Status, feature.StatusPlanning)
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

	lc := withStatusTransitions(lifecycleForFeature(f), f)
	fs := newFeatureStore(f)
	cpr.pr.FeatureStore = fs
	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
		Store:       fs,
		Sessions:    cpr.sm,
		PhaseRunner: cpr.pr,
		CmdRunner:   cpr.cmd,
	}, orchestrator.Hooks{})

	if err := o.StartFeature(featureID); err != nil {
		t.Fatalf("StartFeature: %v", err)
	}

	// StartPlanning MUST be called — feature was not yet StatusPlanning.
	if n := countLifecycleCalls(lc, "StartPlanning"); n != 1 {
		t.Errorf("StartPlanning calls = %d, want 1 (feature should transition from StatusPlanReady)", n)
	}

	captured := waitForCapturedPhase(t, cpr, feature.PhasePlan, 3*time.Second)
	if len(captured) == 0 {
		t.Fatalf("no BuildSession captured for Phase=PhasePlan; captures: %v", cpr.capturedOpts)
	}

	// After StartPlanning, the orchestrator re-loads the feature, so the
	// in-place Status mutation (from lifecycleForFeature's TransitionFn) is
	// visible to the rest of the dispatch. Assert the feature is now planning.
	if f.Status != feature.StatusPlanning {
		t.Errorf("feature.Status = %v, want StatusPlanning after transition", f.Status)
	}
}
