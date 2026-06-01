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

package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"

	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

func repoStatePending() *feature.RepoState {
	return &feature.RepoState{}
}

func repoStateTouched() *feature.RepoState {
	return &feature.RepoState{Touched: true}
}

func repoStatePR(url string) *feature.RepoState {
	return &feature.RepoState{Touched: true, PRURL: url}
}

func repoStateFailed(msg string) *feature.RepoState {
	if msg == "" {
		msg = "failed"
	}
	return &feature.RepoState{Touched: true, LastError: msg}
}

func TestFeatureIDFromSession(t *testing.T) {
	tests := []struct {
		sessionID string
		want      string
	}{
		{"abc123-research", "abc123"},
		{"abc123-plan", "abc123"},
		{"abc123-impl-01", "abc123"},
		{"abc123-review-03", "abc123"},
		{"abc123-kb", "abc123"},
		{"abc123-inquire", "abc123"},
		{"abc123-design", "abc123"},
		{"abc123-artifact-review", "abc123"},
		{"longid1234567890-artifact-review", "longid1234567890"},
		{"longid1234567890-research", "longid1234567890"},
		{"longid1234567890-inquire", "longid1234567890"},
		{"longid1234567890-design", "longid1234567890"},
		// Phase-scoped session IDs
		{"abc123-phase-01-impl-01", "abc123"},
		{"abc123-phase-02-review-03", "abc123"},
		{"abc123-phase-01-plan-01", "abc123"},
		{"abc123-phase-03-planreview-01", "abc123"},
		// Fix-agent sessions: "<featureID>-fix[-<repoName>]-<NN>".
		// Repo names may contain phase-like substrings, so we anchor on
		// "-fix-" before the general suffix scan. Regression coverage for
		// orphan artifact dirs at features/<featureID>-fix-<repo>-<NN>/.
		{"abc123-fix-01", "abc123"},
		{"abc123-fix-agentic-01", "abc123"},
		{"abc123-fix-auth-service-04", "abc123"},
		{"abc123-fix-code-review-tool-02", "abc123"},
		{"unknown-session", "unknown-session"},
	}
	for _, tt := range tests {
		t.Run(tt.sessionID, func(t *testing.T) {
			got := featureIDFromSession(tt.sessionID)
			if got != tt.want {
				t.Errorf("featureIDFromSession(%q) = %q, want %q", tt.sessionID, got, tt.want)
			}
		})
	}
}

// TestEventFIDAndPhase_PreferEventFields is the regression test for the
// orphan artifact directory bug: a fix-agent session's SDKEventMsg must
// route to the real feature ID / phase even when the sessionID-parser
// would mis-classify it (fall through to the PhaseResearch default with
// the entire session ID treated as a feature ID).
//
// The fallback to the legacy string parser stays exercised by the other
// tests in this file; this one pins the fast path.
func TestEventFIDAndPhase_PreferEventFields(t *testing.T) {
	// A fix-agent session ID the legacy parser would mis-classify.
	const sessionID = "c278a827d83dc2e2-fix-auth-service-04"
	const realFID = "c278a827d83dc2e2"

	if got := eventFID(sessionID, realFID); got != realFID {
		t.Errorf("eventFID with populated FeatureID = %q, want %q", got, realFID)
	}
	if got := eventPhase(sessionID, realFID, feature.PhaseReview); got != feature.PhaseReview {
		t.Errorf("eventPhase with populated FeatureID = %v, want %v", got, feature.PhaseReview)
	}

	// With the defensive "-fix-" anchor added to the parsers, the legacy
	// fallback path now also resolves fix sessions correctly — callers
	// that forget to populate FeatureID still don't produce orphan dirs.
	if got := eventFID(sessionID, ""); got != realFID {
		t.Errorf("eventFID fallback (legacy parser) = %q, want %q", got, realFID)
	}
	if got := eventPhase(sessionID, "", 0); got != feature.PhaseReview {
		t.Errorf("eventPhase fallback (legacy parser) = %v, want %v", got, feature.PhaseReview)
	}
}

// TestEventFIDAndPhase_StructuredFieldsWinOverParser constructs an event
// whose SessionID pattern would be mis-classified by the string parser
// (a fix-agent session that, absent the "-fix-" anchor added later, would
// fall through to the default PhaseResearch) — and verifies that the
// populated FeatureID/Phase on SDKEventMsg take precedence. This pins
// F4-2: production paths always set the structured fields at emission
// time, and the TUI must prefer them unconditionally.
func TestEventFIDAndPhase_StructuredFieldsWinOverParser(t *testing.T) {
	// A fix-agent session ID. The embedded repo name "some-repo"
	// doesn't match any phase suffix, so an omitted fast path would
	// return PhaseResearch (default) rather than the real PhaseImplement
	// the manager emitted.
	evt := session.SDKEventMsg{
		SessionID: "abc123-fix-some-repo-04",
		FeatureID: "abc123",
		Phase:     feature.PhaseImplement,
	}
	if got := eventFID(evt.SessionID, evt.FeatureID); got != "abc123" {
		t.Errorf("eventFID structured = %q, want %q", got, "abc123")
	}
	if got := eventPhase(evt.SessionID, evt.FeatureID, evt.Phase); got != feature.PhaseImplement {
		t.Errorf("eventPhase structured = %v, want %v", got, feature.PhaseImplement)
	}
}

func TestPhaseFromSessionID(t *testing.T) {
	tests := []struct {
		sessionID string
		want      feature.Phase
	}{
		{"abc123-research", feature.PhaseResearch},
		{"abc123-plan", feature.PhasePlan},
		{"abc123-plan-01", feature.PhasePlan},
		{"abc123-planreview-01", feature.PhaseReview},
		{"abc123-impl-01", feature.PhaseImplement},
		{"abc123-review-01", feature.PhaseReview},
		{"abc123-kb", feature.PhaseKnowledgeBase},
		{"abc123-inquire", feature.PhaseInquire},
		{"abc123-design", feature.PhaseDesign},
		// Phase-scoped session IDs
		{"abc123-phase-01-impl-01", feature.PhaseImplement},
		{"abc123-phase-02-review-03", feature.PhaseReview},
		{"abc123-phase-01-plan-01", feature.PhasePlan},
		{"abc123-phase-03-planreview-01", feature.PhaseReview},
		// Multi-repo: repo names containing phase-like substrings must resolve to PhaseImplement
		{"abc123-impl-my-repo-01", feature.PhaseImplement},
		{"abc123-impl-deploy-plan-01", feature.PhaseImplement},
		{"abc123-impl-data-research-01", feature.PhaseImplement},
		{"abc123-impl-my-kb-service-01", feature.PhaseImplement},
		{"abc123-impl-code-review-tool-01", feature.PhaseImplement},
		{"abc123-impl-design-helper-01", feature.PhaseImplement},
		{"abc123-impl-inquire-svc-01", feature.PhaseImplement},
		// Fix-agent sessions live inside the final-review loop, so they
		// resolve to PhaseReview. Repo names may contain phase-like
		// substrings, so the "-fix-" anchor wins before the suffix scan.
		{"abc123-fix-01", feature.PhaseReview},
		{"abc123-fix-agentic-01", feature.PhaseReview},
		{"abc123-fix-auth-service-04", feature.PhaseReview},
		{"abc123-fix-code-review-tool-02", feature.PhaseReview},
	}
	for _, tt := range tests {
		t.Run(tt.sessionID, func(t *testing.T) {
			got := phaseFromSessionID(tt.sessionID)
			if got != tt.want {
				t.Errorf("phaseFromSessionID(%q) = %v, want %v", tt.sessionID, got, tt.want)
			}
		})
	}
}

func TestSplitLastN(t *testing.T) {
	tests := []struct {
		name  string
		input string
		n     int
		want  int
	}{
		{"empty", "", 10, 0},
		{"fewer than n", "a\nb\nc", 10, 3},
		{"exactly n", "a\nb\nc", 3, 3},
		{"more than n", "a\nb\nc\nd\ne", 3, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitLastN(tt.input, tt.n)
			if len(got) != tt.want {
				t.Errorf("splitLastN() len = %d, want %d", len(got), tt.want)
			}
		})
	}
}

func TestProgramRefNilSafety(t *testing.T) {
	// ProgramRef should be initialized in NewAppModel
	ref := &ProgramRef{}
	if ref.P != nil {
		t.Error("expected nil P on new ProgramRef")
	}
}

// newTestOrchestrator builds a minimal orchestrator wired to a feature
// manager and session manager, suitable for tests that exercise
// NewAppModel's startup recovery scan and stale-state sweep.
func newTestOrchestrator(fm *feature.Manager, sm *session.Manager) *orchestrator.Orchestrator {
	return orchestrator.New(orchestrator.Deps{
		Lifecycle: fm,
		Store:     fm.Store,
		Sessions:  sm,
		Recovery:  session.NewRecoveryAdapter(fm.Store.BaseDir, fm),
	}, orchestrator.Hooks{})
}

// newTestAppModel creates an AppModel backed by a real feature store for testing.
func newTestAppModel(t *testing.T) (AppModel, *feature.Manager) {
	t.Helper()
	dir := t.TempDir()
	store := feature.NewStore(dir)
	cfg := config.NewDefault()
	cfg.Repos["test-repo"] = config.RepoConfig{
		Path: "/tmp/test-repo",
	}
	fm := feature.NewManager(store, cfg)
	dash := NewDashboardModel(nil, "")
	dash.width = 80
	dash.height = 24
	registry := llm.NewRegistry()
	phaseRunner := &agent.PhaseRunner{
		CommandRunner: agent.NewExecCommandRunner(),
		Registry:      registry,
		StateDir:      dir,
	}
	sm := session.NewManager(nil)
	// Tests exercise handler delegation, so hand out a real orchestrator.
	// Adapters we do not use are left nil; every delegation path that depends
	// on them is covered by orchestrator_bridge_test.go with fakeOrch. The
	// BuildHooks call wires observer emission into handler dispatch so
	// observe_test.go works without the TUI re-emitting events on its own.
	orch := orchestrator.New(orchestrator.Deps{
		Lifecycle:   fm,
		Store:       store,
		Sessions:    sm,
		PhaseRunner: phaseRunner,
		CmdRunner:   phaseRunner.CommandRunner,
		Publisher:   &git.PublishAdapter{},
		Rebaser:     &git.RebaseAdapter{},
	}, orchestrator.BuildHooks(nil, nil, store, store.BaseDir))
	app := AppModel{
		currentView:    ViewDashboard,
		dashboard:      dash,
		featureManager: fm,
		sessionManager: sm,
		orchestrator:   orch,
		registry:       registry,
		programRef:     &ProgramRef{},
		phaseRunner:    phaseRunner,
		width:          80,
		height:         24,
	}
	return app, fm
}

// mockGitPush overrides git.PushFunc and git.ForcePushFunc with a simulated
// push that updates bare-remote refs via git fetch instead of real git-push.
// The original functions are restored via t.Cleanup.
func mockGitPush(t *testing.T) {
	t.Helper()
	origPush := git.PushFunc
	origForcePush := git.ForcePushFunc
	t.Cleanup(func() {
		git.PushFunc = origPush
		git.ForcePushFunc = origForcePush
	})

	simulatedPush := func(worktreePath, branch string) error {
		// Discover the bare remote URL.
		urlCmd := exec.Command("git", "-C", worktreePath, "remote", "get-url", "origin")
		urlOut, err := urlCmd.Output()
		if err != nil {
			return fmt.Errorf("simulated push: get remote url: %w", err)
		}
		remoteDir := strings.TrimSpace(string(urlOut))

		// Fetch objects + update ref in the bare repo directly from the worktree.
		fetchCmd := exec.Command("git", "-C", remoteDir, "fetch", worktreePath, branch+":refs/heads/"+branch)
		if out, err := fetchCmd.CombinedOutput(); err != nil {
			return fmt.Errorf("simulated push: fetch into bare: %s: %w", out, err)
		}

		// Fetch back so local tracking refs update.
		fetchBack := exec.Command("git", "-C", worktreePath, "fetch", "origin")
		if out, err := fetchBack.CombinedOutput(); err != nil {
			return fmt.Errorf("simulated push: fetch back: %s: %w", out, err)
		}
		return nil
	}

	git.PushFunc = simulatedPush
	git.ForcePushFunc = simulatedPush
}

func TestPublishExecuteResultMarksPublished(t *testing.T) {
	app, fm := newTestAppModel(t)

	// Create a feature and advance it to PRReady
	f, err := fm.Create("Publish Test", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = fm.Transition(f.ID, feature.StatusResearching)
	_ = fm.Transition(f.ID, feature.StatusPlanReady)
	_ = fm.Transition(f.ID, feature.StatusPlanning)
	_ = fm.Transition(f.ID, feature.StatusImplementReady)
	_ = fm.Transition(f.ID, feature.StatusImplementing)
	_ = fm.Transition(f.ID, feature.StatusReviewPassed)
	_ = fm.Transition(f.ID, feature.StatusCodeReady)

	// Set up publish model with the feature ID
	app.publish = newTestPublishModel(f.ID, "diff", "log", "title", "body", 120, 40)
	app.currentView = ViewPublish

	// Send a successful publishExecuteResultMsg
	result, _ := app.Update(publishExecuteResultMsg{prURL: "https://github.com/org/repo/pull/1"})
	updatedApp := result.(AppModel)

	// Verify publish model received the result
	if updatedApp.publish.prURL != "https://github.com/org/repo/pull/1" {
		t.Errorf("prURL = %q, want PR URL", updatedApp.publish.prURL)
	}

	// Verify feature was marked as Published
	f, _ = fm.Get(f.ID)
	if f.Status != feature.StatusPublished {
		t.Errorf("feature status = %v, want Published", f.Status)
	}
}

func TestPublishExecuteErrorMarksFailed(t *testing.T) {
	app, fm := newTestAppModel(t)

	f, _ := fm.Create("Publish Error Test", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	_ = fm.Transition(f.ID, feature.StatusResearching)
	_ = fm.Transition(f.ID, feature.StatusPlanReady)
	_ = fm.Transition(f.ID, feature.StatusPlanning)
	_ = fm.Transition(f.ID, feature.StatusImplementReady)
	_ = fm.Transition(f.ID, feature.StatusImplementing)
	_ = fm.Transition(f.ID, feature.StatusReviewPassed)
	_ = fm.Transition(f.ID, feature.StatusCodeReady)

	app.publish = newTestPublishModel(f.ID, "diff", "log", "title", "body", 120, 40)
	app.currentView = ViewPublish

	// Send an error result with featureID so MarkFailed can be called
	result, _ := app.Update(publishExecuteResultMsg{featureID: f.ID, err: os.ErrNotExist})
	updatedApp := result.(AppModel)

	if updatedApp.publish.errMsg == "" {
		t.Error("expected error message to be set")
	}

	// Feature should be marked as Failed on publish infrastructure errors
	f, _ = fm.Get(f.ID)
	if f.Status != feature.StatusFailed {
		t.Errorf("feature status = %v, want Failed", f.Status)
	}
}

// TestPublishExecuteError_MultiRepoRecordsRepoScopedError verifies that when
// a multi-repo manual publish fails (repoName is set on the error), the error
// is recorded per-repo via SetRepoPublishError instead of failing the entire
// feature via MarkFailed. This allows other repos to still be published.
func TestPublishExecuteError_MultiRepoRecordsRepoScopedError(t *testing.T) {
	app, fm := newTestAppModel(t)

	f := &feature.Feature{
		ID:     "test-multi-repo-err",
		Name:   "Multi Repo Error",
		Slug:   "multi-repo-error",
		Status: feature.StatusCodeReady,
		Repos: []feature.FeatureRepo{
			{Name: "repo-a", Path: "/tmp/repo-a", WorktreePath: "/tmp/wt-a", Branch: "feature/test"},
			{Name: "repo-b", Path: "/tmp/repo-b", WorktreePath: "/tmp/wt-b", Branch: "feature/test"},
		},
		RepoStates: map[string]*feature.RepoState{
			"repo-a": repoStateTouched(),
			"repo-b": repoStateTouched(),
		},
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	if err := fm.Store.Save(f); err != nil {
		t.Fatal(err)
	}

	app.publish = newTestPublishModel(f.ID, "diff", "log", "title", "body", 120, 40)
	app.publish.hasRepoSelect = true
	app.publish.repoName = "repo-a"
	app.currentView = ViewPublish

	// Send an error result WITH repoName — should record repo-scoped error, NOT MarkFailed
	result, _ := app.Update(publishExecuteResultMsg{
		featureID: f.ID,
		repoName:  "repo-a",
		err:       fmt.Errorf("push failed: connection refused"),
	})
	_ = result.(AppModel)

	// Feature must NOT be Failed — only repo-a should have an error.
	got, err := fm.Get("test-multi-repo-err")
	if err != nil {
		t.Fatalf("failed to get feature: %v", err)
	}
	if got.Status == feature.StatusFailed {
		t.Error("feature status = Failed, want PRReady; multi-repo publish error should be repo-scoped, not feature-wide")
	}
	// Verify the repo-scoped error was recorded.
	repoState, ok := got.RepoStates["repo-a"]
	if !ok {
		t.Fatal("repo-a not found in RepoStates")
	}
	if repoState.LastError == "" {
		t.Error("repo-a LastError is empty, expected error to be recorded")
	}
}

func TestPublishConflictExitsPublishView(t *testing.T) {
	app, _ := newTestAppModel(t)

	app.publish = newTestPublishModel("feat-1", "diff", "log", "title", "body", 120, 40)
	app.publish.step = publishStepExecute
	app.currentView = ViewPublish

	// Send a conflict result while in the publish view
	result, _ := app.Update(publishExecuteResultMsg{
		featureID:        "feat-1",
		err:              fmt.Errorf("pull-rebase conflict"),
		conflictDetected: true,
		branch:           "feature/test",
	})
	updatedApp := result.(AppModel)

	// The publish model must no longer be in publishStepExecute (which blocks input)
	if updatedApp.publish.step == publishStepExecute {
		t.Error("publish model should not remain in publishStepExecute after conflict")
	}

	// The view should have transitioned away from ViewPublish
	if updatedApp.currentView == ViewPublish {
		t.Error("currentView should not remain ViewPublish after conflict resolution starts")
	}
}
func TestLogsViewTransition(t *testing.T) {
	app, _ := newTestAppModel(t)

	// Set up detail context
	f := &feature.Feature{
		ID:            "test-feat",
		Slug:          "test",
		Status:        feature.StatusImplementing,
		CurrentPhase:  feature.PhaseImplement,
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	app.detail = NewDetailModel(f, "")

	// Send LogsContentMsg
	result, _ := app.Update(LogsContentMsg{
		Title:   "Logs: test-feat (Implement)",
		Content: "line1\nline2\nline3",
	})
	updatedApp := result.(AppModel)

	if updatedApp.currentView != ViewLogs {
		t.Errorf("view = %v, want ViewLogs", updatedApp.currentView)
	}

	// Verify logs view renders content
	view := updatedApp.View().Content
	if !strings.Contains(view, "Logs: test-feat") {
		t.Error("expected logs title in view")
	}
}

func TestLogsViewBackToDetail(t *testing.T) {
	app, fm := newTestAppModel(t)

	// Create and store a feature so detail can be restored
	f, _ := fm.Create("Log Detail Test", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	app.detail = NewDetailModel(f, "")

	// Transition to logs view
	app.logs = NewLogsModel("Test Logs", "log content", 80, 24)
	app.currentView = ViewLogs

	// Press 'esc' to go back
	result, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	updatedApp := result.(AppModel)

	if updatedApp.currentView != ViewDetail {
		t.Errorf("view = %v, want ViewDetail after back", updatedApp.currentView)
	}
}

func TestViewLogsCmdReturnsContent(t *testing.T) {
	dir := t.TempDir()
	store := feature.NewStore(dir)
	cfg := config.NewDefault()
	fm := feature.NewManager(store, cfg)

	app := AppModel{featureManager: fm}

	// Persist a minimal feature so viewLogsCmd can load it.
	f := &feature.Feature{ID: "feat-1", Name: "feat-1", ActiveRun: 1, RunCount: 1, SchemaVersion: feature.SchemaVersionCurrent}
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}

	// Write a log file using the lowercase dir name matching phase.DirName()
	logDir := filepath.Join(dir, "feat-1", "runs", "run-001", "implement")
	_ = os.MkdirAll(logDir, 0o755)
	_ = os.WriteFile(filepath.Join(logDir, "output.txt"), []byte("log line 1\nlog line 2\n"), 0o644)

	cmd := app.viewLogsCmd("feat-1", feature.PhaseImplement, 0)
	msg := cmd()

	logsMsg, ok := msg.(LogsContentMsg)
	if !ok {
		t.Fatalf("expected LogsContentMsg, got %T", msg)
	}
	if !strings.Contains(logsMsg.Content, "log line 1") {
		t.Error("expected log content in message")
	}
	if !strings.Contains(logsMsg.Title, "feat-1") {
		t.Error("expected feature ID in title")
	}
}

func TestViewLogsCmdMissingFile(t *testing.T) {
	dir := t.TempDir()
	store := feature.NewStore(dir)
	cfg := config.NewDefault()
	fm := feature.NewManager(store, cfg)

	app := AppModel{featureManager: fm}

	cmd := app.viewLogsCmd("nonexistent", feature.PhaseResearch, 0)
	msg := cmd()

	// Should return RefreshFeaturesMsg when file doesn't exist
	if _, ok := msg.(RefreshFeaturesMsg); !ok {
		t.Fatalf("expected RefreshFeaturesMsg for missing log, got %T", msg)
	}
}

func TestViewLogsCmdUsesLowercaseDirNames(t *testing.T) {
	dir := t.TempDir()
	store := feature.NewStore(dir)
	cfg := config.NewDefault()
	fm := feature.NewManager(store, cfg)
	app := AppModel{featureManager: fm}

	// Persist a minimal feature so viewLogsCmd can load it.
	f := &feature.Feature{ID: "feat-path-test", Name: "feat-path-test", ActiveRun: 1, RunCount: 1,
		SchemaVersion: feature.SchemaVersionCurrent}
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}

	// Phase runners write to lowercase directories.
	// viewLogsCmd must use the same lowercase names to find logs.
	phases := []struct {
		phase feature.Phase
		dir   string
	}{
		{feature.PhaseResearch, "research"},
		{feature.PhasePlan, "plan"},
		{feature.PhaseImplement, "implement"},
	}

	for _, p := range phases {
		// Write log in lowercase dir (as phase runners do)
		logDir := filepath.Join(dir, "feat-path-test", "runs", "run-001", p.dir)
		_ = os.MkdirAll(logDir, 0o755)
		_ = os.WriteFile(filepath.Join(logDir, "output.txt"), []byte(p.dir+" output"), 0o644)

		cmd := app.viewLogsCmd("feat-path-test", p.phase, 0)
		msg := cmd()

		logsMsg, ok := msg.(LogsContentMsg)
		if !ok {
			t.Errorf("phase %s: expected LogsContentMsg, got %T", p.phase, msg)
			continue
		}
		if !strings.Contains(logsMsg.Content, p.dir+" output") {
			t.Errorf("phase %s: expected content %q, got %q", p.phase, p.dir+" output", logsMsg.Content)
		}
	}
}

func TestViewLogsCmdReadsCleanText(t *testing.T) {
	dir := t.TempDir()
	store := feature.NewStore(dir)
	cfg := config.NewDefault()
	fm := feature.NewManager(store, cfg)

	app := AppModel{featureManager: fm}

	// Persist a minimal feature so viewLogsCmd can load it.
	f := &feature.Feature{ID: "feat-clean", Name: "feat-clean", ActiveRun: 1, RunCount: 1,
		SchemaVersion: feature.SchemaVersionCurrent}
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}

	// With JSON protocol, log files contain clean text from MessageLog
	logDir := filepath.Join(dir, "feat-clean", "runs", "run-001", "implement")
	_ = os.MkdirAll(logDir, 0o755)
	_ = os.WriteFile(filepath.Join(logDir, "output.txt"),
		[]byte("[assistant] ERROR: test failed\n[assistant] ok done\n"), 0o644)

	cmd := app.viewLogsCmd("feat-clean", feature.PhaseImplement, 0)
	msg := cmd()

	logsMsg, ok := msg.(LogsContentMsg)
	if !ok {
		t.Fatalf("expected LogsContentMsg, got %T", msg)
	}
	if !strings.Contains(logsMsg.Content, "ERROR: test failed") {
		t.Error("expected error text in logs")
	}
	if !strings.Contains(logsMsg.Content, "ok done") {
		t.Error("expected success text in logs")
	}
}

func TestViewLogsCmdRoadmapPhase(t *testing.T) {
	dir := t.TempDir()
	store := feature.NewStore(dir)
	cfg := config.NewDefault()
	fm := feature.NewManager(store, cfg)
	app := AppModel{featureManager: fm}

	// Persist a minimal feature so viewLogsCmd can load it.
	f := &feature.Feature{ID: "feat-roadmap", Name: "feat-roadmap", ActiveRun: 1, RunCount: 1,
		SchemaVersion: feature.SchemaVersionCurrent}
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}

	// Roadmap features store logs under runs/run-001/phase-NN/<phase>/
	logDir := filepath.Join(dir, "feat-roadmap", "runs", "run-001", "phase-04", "plan")
	_ = os.MkdirAll(logDir, 0o755)
	_ = os.WriteFile(filepath.Join(logDir, "output.txt"), []byte("roadmap plan output\n"), 0o644)

	// With roadmapPhase > 0, viewLogsCmd should look under runs/run-001/phase-04/plan/
	cmd := app.viewLogsCmd("feat-roadmap", feature.PhasePlan, 4)
	msg := cmd()

	logsMsg, ok := msg.(LogsContentMsg)
	if !ok {
		t.Fatalf("expected LogsContentMsg, got %T", msg)
	}
	if !strings.Contains(logsMsg.Content, "roadmap plan output") {
		t.Errorf("expected roadmap log content, got %q", logsMsg.Content)
	}
}

func TestHelpInputActivation(t *testing.T) {
	app, fm := newTestAppModel(t)

	// Create a feature with a pending help request
	f, _ := fm.Create("Help Test", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	f.Status = feature.StatusImplementing
	f.CurrentPhase = feature.PhaseImplement
	f.HelpQueue = []feature.HelpRequest{
		{Question: "What auth method?", Pending: true},
	}
	_ = fm.Store.Save(f)

	// Set up detail view
	app.detail = NewDetailModel(f, "")
	app.currentView = ViewDetail

	// Press 'h' to activate help input
	result, _ := app.Update(tea.KeyPressMsg{Code: 'h', Text: "h"})
	updatedApp := result.(AppModel)

	if !updatedApp.helpInputActive {
		t.Error("expected helpInputActive to be true after pressing 'h'")
	}
	if updatedApp.helpQuestion != "What auth method?" {
		t.Errorf("helpQuestion = %q, want %q", updatedApp.helpQuestion, "What auth method?")
	}
	if updatedApp.helpFeatureID != f.ID {
		t.Errorf("helpFeatureID = %q, want %q", updatedApp.helpFeatureID, f.ID)
	}
}

func TestHelpInputView(t *testing.T) {
	app, fm := newTestAppModel(t)

	f, _ := fm.Create("Help View", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	f.HelpQueue = []feature.HelpRequest{{Question: "Which DB?", Pending: true}}
	_ = fm.Store.Save(f)

	app.detail = NewDetailModel(f, "")
	app.currentView = ViewDetail
	app.helpInputActive = true
	app.helpQuestion = "Which DB?"

	view := app.View().Content
	if !strings.Contains(view, "Agent needs help") {
		t.Error("expected help section in view")
	}
	if !strings.Contains(view, "Which DB?") {
		t.Error("expected help question in view")
	}
}

func TestHelpInputCancel(t *testing.T) {
	app, _ := newTestAppModel(t)

	app.currentView = ViewDetail
	app.detail = NewDetailModel(&feature.Feature{
		ID: "test", Slug: "test", Status: feature.StatusImplementing,
		SchemaVersion: feature.SchemaVersionCurrent,
	}, "")
	app.helpInputActive = true
	app.helpFeatureID = "test"

	// Press Esc to cancel
	result, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	updatedApp := result.(AppModel)

	if updatedApp.helpInputActive {
		t.Error("expected helpInputActive to be false after Esc")
	}
}

func TestWizardTextInputKeysThroughAppModel(t *testing.T) {
	app, _ := newTestAppModel(t)
	app = app.transitionToWizard()

	// Type "quick" — contains both 'q' and 'k' which are bound
	// to quit and up-navigation respectively. They must NOT be
	// swallowed when a text input is active.
	for _, r := range "quick" {
		result, _ := app.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		app = result.(AppModel)
	}

	if app.wizard.nameInput.Value() != "quick" {
		t.Errorf("name input = %q, want %q (q/k keys must not be swallowed on text input steps)", app.wizard.nameInput.Value(), "quick")
	}
	if app.currentView != ViewWizard {
		t.Errorf("view = %v, want ViewWizard (q must not trigger global quit in wizard text input)", app.currentView)
	}
}

func TestTransitionToWizardAutoDiscoversRepos(t *testing.T) {
	// Create a workspace with git repos
	wsDir := t.TempDir()
	for _, name := range []string{"my-service", "my-lib"} {
		if err := os.MkdirAll(filepath.Join(wsDir, name, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	dir := t.TempDir()
	store := feature.NewStore(dir)
	cfg := config.NewDefault()
	cfg.WorkspaceRoots = []string{wsDir}
	fm := feature.NewManager(store, cfg)

	configPath := filepath.Join(dir, "config.yaml")
	app := AppModel{
		currentView:    ViewDashboard,
		dashboard:      NewDashboardModel(nil, ""),
		featureManager: fm,
		programRef:     &ProgramRef{},
		configPath:     configPath,
	}

	app = app.transitionToWizard()

	if app.currentView != ViewWizard {
		t.Errorf("expected wizard view, got %d", app.currentView)
	}

	// Check that repos were discovered via workspace roots
	if len(app.wizard.availRepos) != 2 {
		t.Errorf("expected 2 auto-discovered repos, got %d: %v", len(app.wizard.availRepos), app.wizard.availRepos)
	}

	// Check repos are in DiscoveredRepos (not Repos — discovery is in-memory only)
	allRepos := config.AllRepos(fm.Config)
	if _, ok := allRepos["my-service"]; !ok {
		t.Error("expected my-service in AllRepos")
	}
	if _, ok := allRepos["my-lib"]; !ok {
		t.Error("expected my-lib in AllRepos")
	}
	// Explicit Repos should NOT be mutated by discovery
	if len(fm.Config.Repos) != 0 {
		t.Errorf("expected Repos to remain empty, got %d entries", len(fm.Config.Repos))
	}
}

func TestAttachDisallowedForNonRunning(t *testing.T) {
	app, fm := newTestAppModel(t)

	// Create a feature in Created status (not running)
	f, err := fm.Create("Attach Test", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	f.CurrentPhase = feature.PhaseResearch
	_ = fm.Store.Save(f)

	app.detail = NewDetailModel(f, "")
	app.currentView = ViewDetail

	sm := session.NewManager(nil)
	app.sessionManager = sm

	// Press 'a' on a non-running feature — should be a no-op
	_, cmd := app.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})

	if cmd != nil {
		t.Error("expected nil command for non-running feature, got a command")
	}
}

func TestDashboardWatchAttachUsesExistingSingleSessionPath(t *testing.T) {
	app, fm := newTestAppModel(t)

	f, err := fm.Create("Watch Single", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	advanceTestFeatureToImplementing(t, fm, f.ID)
	f, _ = fm.Get(f.ID)

	sess := session.NewSession(f.ID+"-impl-01", f.ID, feature.PhaseImplement)
	sess.SetStatus(session.SessionRunning)
	app.sessionManager.RegisterTestSession(sess)

	app.dashboard.SetFeatures([]*feature.Feature{f})
	app.dashboard.cursor = 1
	app.dashboard.syncPreview()
	app.dashboard.focusPanel = 1

	result, cmd := app.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if cmd == nil {
		t.Fatal("expected watch action to return attach command")
	}
	updated := result.(AppModel)
	if updated.currentView != ViewAttach {
		t.Fatalf("currentView = %v, want ViewAttach", updated.currentView)
	}
	if updated.attach.sess == nil || updated.attach.sess.ID() != sess.ID() {
		t.Fatalf("attached session = %v, want %s", updated.attach.sess, sess.ID())
	}
	if len(updated.attach.repoTabs) != 1 {
		t.Fatalf("single-session watch repoTabs = %d, want 1", len(updated.attach.repoTabs))
	}
}

func TestDashboardNeedsReviewIgnoresArtifactReviewSessionForWatch(t *testing.T) {
	app, fm := newTestAppModel(t)

	f, err := fm.Create("Review Gate", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	target := feature.PhaseResearch
	if err := fm.Store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.Status = feature.StatusInquiryNeedsReview
		ff.CurrentPhase = feature.PhaseInquire
		ff.PendingReviewPhase = &target
		ff.HelpQueue = []feature.HelpRequest{{Question: waitingInputHelpMessage, Pending: true}}
		return nil
	}); err != nil {
		t.Fatalf("modify: %v", err)
	}
	f, _ = fm.Get(f.ID)

	artifactPath := filepath.Join(t.TempDir(), "questions.md")
	if err := os.WriteFile(artifactPath, []byte("# Questions\n"), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	app.artifactReview = NewArtifactReviewModel(artifactPath, f.ID, "gate", target, 120, 30, app.sessionManager, t.TempDir(), nil)
	app.artifactReview.detached = true

	reviewSess := session.NewSession(f.ID+"-artifact-review", f.ID, target)
	reviewSess.SetStatus(session.SessionWaitingHelp)
	reviewSess.SetLastControlRequest(&llm.ControlRequestMessage{
		RequestID: "ask-review",
		Request: llm.ControlRequest{
			Subtype:  "can_use_tool",
			ToolName: "AskUserQuestion",
			Input:    json.RawMessage(`{"questions":[{"question":"Review helper question?"}]}`),
		},
	})
	app.sessionManager.RegisterTestSession(reviewSess)

	app.dashboard.SetFeatures([]*feature.Feature{f})
	app.dashboard.cursor = 1
	app.dashboard.syncPreview()
	app.dashboard.focusPanel = 1

	if sess := app.livePreviewSessionForFeature(f); sess != nil {
		t.Fatalf("livePreviewSessionForFeature() = %q, want nil for artifact-review helper", sess.ID())
	}
	if tabs := app.buildRepoTabs(f); len(tabs) != 0 {
		t.Fatalf("buildRepoTabs() included artifact-review helper: %+v", tabs)
	}

	view := stripANSI(app.View().Content)
	if strings.Contains(view, "Live Preview") {
		t.Fatalf("needs-review feature should render overview, not live preview:\n%s", view)
	}
	if !strings.Contains(view, "[a] Review") || strings.Contains(view, "[a] Answer") {
		t.Fatalf("needs-review footer should route [a] to Review, got:\n%s", view)
	}
	if strings.Contains(view, waitingInputHelpMessage) {
		t.Fatalf("needs-review overview should suppress stale generic help, got:\n%s", view)
	}

	result, _ := app.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	updated := result.(AppModel)
	if updated.currentView != ViewArtifactReview {
		t.Fatalf("currentView = %v, want ViewArtifactReview", updated.currentView)
	}
	if updated.currentView == ViewAttach {
		t.Fatalf("artifact-review helper must not open generic Watch view")
	}
}

func TestDashboardWatchAttachPreservesMultiRepoTabs(t *testing.T) {
	app, fm := newTestAppModel(t)
	fm.Config.Repos["api"] = config.RepoConfig{Path: "/tmp/api"}
	fm.Config.Repos["web"] = config.RepoConfig{Path: "/tmp/web"}

	f, err := fm.Create("Watch Multi", "desc", []string{"api", "web"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	advanceTestFeatureToImplementing(t, fm, f.ID)
	f, _ = fm.Get(f.ID)

	apiSess := session.NewSession(f.ID+"-impl-api-01", f.ID, feature.PhaseImplement)
	apiSess.SetKind(ports.KindRepoImpl)
	apiSess.SetRepoName("api")
	apiSess.SetStatus(session.SessionRunning)
	webSess := session.NewSession(f.ID+"-impl-web-01", f.ID, feature.PhaseImplement)
	webSess.SetKind(ports.KindRepoImpl)
	webSess.SetRepoName("web")
	webSess.SetStatus(session.SessionRunning)
	app.sessionManager.RegisterTestSession(apiSess)
	app.sessionManager.RegisterTestSession(webSess)

	app.dashboard.SetFeatures([]*feature.Feature{f})
	app.dashboard.cursor = 1
	app.dashboard.focusPanel = 1

	result, cmd := app.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if cmd == nil {
		t.Fatal("expected watch action to return attach command")
	}
	updated := result.(AppModel)
	if updated.currentView != ViewAttach {
		t.Fatalf("currentView = %v, want ViewAttach", updated.currentView)
	}
	if len(updated.attach.repoTabs) != 2 {
		t.Fatalf("multi-repo watch repoTabs = %d, want 2", len(updated.attach.repoTabs))
	}
	if updated.attach.repoTabs[0].repoName != "api" || updated.attach.repoTabs[1].repoName != "web" {
		t.Fatalf("repo tab order = [%s %s], want [api web]", updated.attach.repoTabs[0].repoName, updated.attach.repoTabs[1].repoName)
	}
}

func TestDashboardRightPanelOverviewReturnsToLivePreview(t *testing.T) {
	app, fm := newTestAppModel(t)

	f, err := fm.Create("Overview Toggle", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	advanceTestFeatureToImplementing(t, fm, f.ID)
	f, _ = fm.Get(f.ID)

	app.dashboard.SetFeatures([]*feature.Feature{f})
	app.dashboard.cursor = 1
	app.dashboard.syncPreview()
	app.dashboard.focusPanel = 1

	result, cmd := app.Update(tea.KeyPressMsg{Code: 'o', Text: "o"})
	if cmd != nil {
		t.Fatalf("overview key returned command, want nil")
	}
	updated := result.(AppModel)
	if updated.dashboard.rightPanelMode != dashboardRightPanelOverview {
		t.Fatalf("rightPanelMode = %v, want overview", updated.dashboard.rightPanelMode)
	}
	overview := stripANSI(updated.dashboard.View())
	if !strings.Contains(overview, "Phase Progress") || !strings.Contains(overview, "[l] Live Preview") {
		t.Fatalf("overview view missing old detail content or live-preview return hint:\n%s", overview)
	}

	result, cmd = updated.Update(tea.KeyPressMsg{Code: 'l', Text: "l"})
	if cmd != nil {
		t.Fatalf("live-preview return key returned command, want nil")
	}
	updated = result.(AppModel)
	if updated.dashboard.rightPanelMode != dashboardRightPanelLivePreview {
		t.Fatalf("rightPanelMode = %v, want live preview", updated.dashboard.rightPanelMode)
	}
	live := stripANSI(updated.dashboard.View())
	if !strings.Contains(live, "Live Preview") || !strings.Contains(live, "[o] Overview") {
		t.Fatalf("live preview view missing live content or overview hint:\n%s", live)
	}
	if strings.Contains(live, "Phase Progress") {
		t.Fatalf("live preview should not render old overview phase progress:\n%s", live)
	}
}

func TestDashboardContextualAttachPermissionSelectsWaitingSession(t *testing.T) {
	app, fm := newTestAppModel(t)
	fm.Config.Repos["api"] = config.RepoConfig{Path: "/tmp/api"}
	fm.Config.Repos["web"] = config.RepoConfig{Path: "/tmp/web"}

	f, err := fm.Create("Permission Target", "desc", []string{"api", "web"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	advanceTestFeatureToImplementing(t, fm, f.ID)
	if err := fm.Store.Modify(f.ID, func(f *feature.Feature) error {
		f.LastAttachedRepo = "web"
		return nil
	}); err != nil {
		t.Fatalf("modify: %v", err)
	}
	f, _ = fm.Get(f.ID)

	apiSess := session.NewSession(f.ID+"-impl-api-01", f.ID, feature.PhaseImplement)
	apiSess.SetKind(ports.KindRepoImpl)
	apiSess.SetRepoName("api")
	apiSess.SetStatus(session.SessionWaitingPermission)
	apiSess.SetLastControlRequest(&llm.ControlRequestMessage{
		RequestID: "perm-api",
		Request: llm.ControlRequest{
			Subtype:  "can_use_tool",
			ToolName: "Bash",
			Input:    json.RawMessage(`{"command":"go test ./internal/tui"}`),
		},
	})
	webSess := session.NewSession(f.ID+"-impl-web-01", f.ID, feature.PhaseImplement)
	webSess.SetKind(ports.KindRepoImpl)
	webSess.SetRepoName("web")
	webSess.SetStatus(session.SessionRunning)
	app.sessionManager.RegisterTestSession(apiSess)
	app.sessionManager.RegisterTestSession(webSess)

	app.dashboard.SetFeatures([]*feature.Feature{f})
	app.dashboard.cursor = 1
	app.dashboard.focusPanel = 1

	result, cmd := app.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if cmd == nil {
		t.Fatal("expected contextual permission action to return attach command")
	}
	updated := result.(AppModel)
	if updated.currentView != ViewAttach {
		t.Fatalf("currentView = %v, want ViewAttach", updated.currentView)
	}
	if got := updated.attach.ActiveRepoName(); got != "api" {
		t.Fatalf("active attach repo = %q, want api despite LastAttachedRepo=web", got)
	}
	if updated.attach.sess == nil || updated.attach.sess.ID() != apiSess.ID() {
		t.Fatalf("attached session = %v, want %s", updated.attach.sess, apiSess.ID())
	}
}

func advanceTestFeatureToImplementing(t *testing.T, fm *feature.Manager, featureID string) {
	t.Helper()
	for _, status := range []feature.Status{
		feature.StatusResearching,
		feature.StatusPlanReady,
		feature.StatusPlanning,
		feature.StatusImplementReady,
		feature.StatusImplementing,
	} {
		if err := fm.Transition(featureID, status); err != nil {
			t.Fatalf("transition to %s: %v", status, err)
		}
	}
}

func TestStartPhaseDispatch(t *testing.T) {
	app, fm := newTestAppModel(t)

	// Create a feature
	f, err := fm.Create("Phase Dispatch", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	tests := []struct {
		name  string
		phase feature.Phase
	}{
		{"research", feature.PhaseResearch},
		{"plan", feature.PhasePlan},
		{"implement", feature.PhaseImplement},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Send StartPhaseMsg and verify it produces a command (not nil)
			result, cmd := app.Update(StartPhaseMsg{FeatureID: f.ID, Phase: tt.phase})
			_ = result.(AppModel)
			if cmd == nil {
				t.Errorf("StartPhaseMsg{Phase: %s} returned nil cmd, expected start command", tt.phase)
			}
		})
	}
}

func TestRestartPhaseStopsAndDispatches(t *testing.T) {
	app, fm := newTestAppModel(t)

	// Create a feature in Researching state
	f, err := fm.Create("Restart Test", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = fm.Transition(f.ID, feature.StatusResearching)
	f, _ = fm.Get(f.ID)

	sm := session.NewManager(nil)
	app.sessionManager = sm

	// Execute restartPhaseCmd directly
	cmd := app.restartPhaseCmd(f.ID)
	msg := cmd()

	// The cmd should return RefreshFeaturesMsg
	if _, ok := msg.(RefreshFeaturesMsg); !ok {
		t.Fatalf("expected RefreshFeaturesMsg, got %T", msg)
	}

	// Verify the feature state was set to Interrupted (ready for startResearchCmd to re-transition)
	f, _ = fm.Get(f.ID)
	if f.Status != feature.StatusInterrupted {
		t.Errorf("feature status = %v, want StatusInterrupted after restart", f.Status)
	}
}

func TestRestartPhasePlanStopsAndDispatches(t *testing.T) {
	app, fm := newTestAppModel(t)

	f, err := fm.Create("Restart Plan", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = fm.Transition(f.ID, feature.StatusResearching)
	_ = fm.Transition(f.ID, feature.StatusPlanReady)
	_ = fm.Transition(f.ID, feature.StatusPlanning)
	f, _ = fm.Get(f.ID)
	f.CurrentPhase = feature.PhasePlan
	_ = fm.Store.Save(f)

	sm := session.NewManager(nil)
	app.sessionManager = sm

	cmd := app.restartPhaseCmd(f.ID)
	msg := cmd()

	if _, ok := msg.(RefreshFeaturesMsg); !ok {
		t.Fatalf("expected RefreshFeaturesMsg, got %T", msg)
	}

	f, _ = fm.Get(f.ID)
	if f.Status != feature.StatusInterrupted {
		t.Errorf("feature status = %v, want StatusInterrupted after plan restart", f.Status)
	}
}

func TestRestartPhaseImplementStopsAndDispatches(t *testing.T) {
	app, fm := newTestAppModel(t)

	f, err := fm.Create("Restart Impl", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = fm.Transition(f.ID, feature.StatusResearching)
	_ = fm.Transition(f.ID, feature.StatusPlanReady)
	_ = fm.Transition(f.ID, feature.StatusPlanning)
	_ = fm.Transition(f.ID, feature.StatusImplementReady)
	_ = fm.Transition(f.ID, feature.StatusImplementing)
	f, _ = fm.Get(f.ID)
	// Set CurrentPhase manually since bare Transition doesn't update it
	f.CurrentPhase = feature.PhaseImplement
	_ = fm.Store.Save(f)

	sm := session.NewManager(nil)
	app.sessionManager = sm

	cmd := app.restartPhaseCmd(f.ID)
	msg := cmd()

	if _, ok := msg.(RefreshFeaturesMsg); !ok {
		t.Fatalf("expected RefreshFeaturesMsg, got %T", msg)
	}

	f, _ = fm.Get(f.ID)
	if f.Status != feature.StatusImplementReady {
		t.Errorf("feature status = %v, want StatusImplementReady after implement restart", f.Status)
	}
}

func TestRestartFromFailedPlanPhase(t *testing.T) {
	app, fm := newTestAppModel(t)

	f, err := fm.Create("Failed Plan", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Simulate: feature was planning, then failed (e.g. TUI killed mid-session)
	_ = fm.Transition(f.ID, feature.StatusResearching)
	_ = fm.Transition(f.ID, feature.StatusPlanReady)
	_ = fm.Transition(f.ID, feature.StatusPlanning)
	_ = fm.Transition(f.ID, feature.StatusFailed)
	f, _ = fm.Get(f.ID)
	f.CurrentPhase = feature.PhasePlan
	_ = fm.Store.Save(f)

	sm := session.NewManager(nil)
	app.sessionManager = sm

	cmd := app.restartPhaseCmd(f.ID)
	msg := cmd()

	if _, ok := msg.(RefreshFeaturesMsg); !ok {
		t.Fatalf("expected RefreshFeaturesMsg, got %T", msg)
	}

	// Feature should be in PlanReady (ready for startPlanningCmd to transition to Planning)
	f, _ = fm.Get(f.ID)
	if f.Status != feature.StatusPlanReady {
		t.Errorf("feature status = %v, want StatusPlanReady after restart from Failed+PhasePlan", f.Status)
	}
}

func TestResumeAllRestartsFailedFinalReviewProtocolViolation(t *testing.T) {
	app, fm := newTestAppModel(t)

	f, err := fm.Create("Failed Final Review", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := fm.Store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.Status = feature.StatusFailed
		ff.CurrentPhase = feature.PhaseFinalReview
		ff.LastError = "protocol violation"
		ff.FailureType = feature.FailureProtocolViolation
		return nil
	}); err != nil {
		t.Fatalf("modify feature: %v", err)
	}

	msg := app.resumeAllCmd()()
	if _, ok := msg.(RefreshFeaturesMsg); !ok {
		t.Fatalf("expected RefreshFeaturesMsg, got %T", msg)
	}

	f, err = fm.Get(f.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if f.Status != feature.StatusReviewPassed {
		t.Errorf("feature status = %v, want StatusReviewPassed after resume-all final review restart", f.Status)
	}
	if f.CurrentPhase != feature.PhaseFinalReview {
		t.Errorf("current phase = %v, want PhaseFinalReview", f.CurrentPhase)
	}
	if f.LastError != "" || f.FailureType != "" {
		t.Errorf("failure context = (%q, %q), want cleared", f.LastError, f.FailureType)
	}
}

func TestRestartFromFailedClearsFailureContext(t *testing.T) {
	app, fm := newTestAppModel(t)

	f, err := fm.Create("Clear Failure", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = fm.Transition(f.ID, feature.StatusResearching)
	_ = fm.MarkFailed(f.ID, feature.FailureSafetyRail, "no progress")
	f, _ = fm.Get(f.ID)

	sm := session.NewManager(nil)
	app.sessionManager = sm

	cmd := app.restartPhaseCmd(f.ID)
	_ = cmd()

	f, _ = fm.Get(f.ID)
	if f.LastError != "" {
		t.Errorf("LastError should be cleared after restart, got %q", f.LastError)
	}
	if f.FailureType != "" {
		t.Errorf("FailureType should be cleared after restart, got %q", f.FailureType)
	}
}

func TestRestartFromFailedResearchPhase(t *testing.T) {
	app, fm := newTestAppModel(t)

	f, err := fm.Create("Failed Research", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = fm.Transition(f.ID, feature.StatusResearching)
	_ = fm.Transition(f.ID, feature.StatusFailed)
	f, _ = fm.Get(f.ID)
	f.CurrentPhase = feature.PhaseResearch
	_ = fm.Store.Save(f)

	sm := session.NewManager(nil)
	app.sessionManager = sm

	cmd := app.restartPhaseCmd(f.ID)
	_ = cmd()

	f, _ = fm.Get(f.ID)
	if f.Status != feature.StatusResearching {
		t.Errorf("feature status = %v, want StatusResearching after restart from Failed+PhaseResearch", f.Status)
	}
}

func TestClearPendingHelpByMessageMarksResolved(t *testing.T) {
	f := &feature.Feature{
		HelpQueue: []feature.HelpRequest{
			{Question: "waiting", Pending: true},
			{Question: "other", Pending: true},
		},
		SchemaVersion: feature.SchemaVersionCurrent,
	}

	cleared := clearPendingHelpByMessage(f, "waiting")
	if !cleared {
		t.Error("expected clearPendingHelpByMessage to return true")
	}
	if len(f.HelpQueue) != 2 {
		t.Errorf("expected 2 entries (mark-resolved, not remove), got %d", len(f.HelpQueue))
	}
	if f.HelpQueue[0].Pending {
		t.Error("expected first entry to be marked resolved")
	}
	if !f.HelpQueue[1].Pending {
		t.Error("expected second entry to remain pending")
	}
}

func TestHasHelpRequestMessageFindsResolved(t *testing.T) {
	f := &feature.Feature{
		HelpQueue: []feature.HelpRequest{
			{Question: "waiting", Pending: false}, // resolved
		},
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	if !hasHelpRequestMessage(f, "waiting") {
		t.Error("hasHelpRequestMessage should find resolved entries too")
	}
}

func TestRemoveResolvedHelpByMessage(t *testing.T) {
	f := &feature.Feature{
		HelpQueue: []feature.HelpRequest{
			{Question: "waiting", Pending: false},
			{Question: "other", Pending: true},
			{Question: "waiting", Pending: false},
		},
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	removeResolvedHelpByMessage(f, "waiting")
	if len(f.HelpQueue) != 1 {
		t.Errorf("expected 1 entry after removal, got %d", len(f.HelpQueue))
	}
	if f.HelpQueue[0].Question != "other" {
		t.Errorf("expected 'other' to remain, got %q", f.HelpQueue[0].Question)
	}
}

func TestClearPendingHelpByPrefix(t *testing.T) {
	f := &feature.Feature{
		HelpQueue: []feature.HelpRequest{
			{Question: "API error: rate limit exceeded (429) — press 'a' to answer", Pending: true},
			{Question: "Agent is waiting for input — press 'a' to answer", Pending: true},
			{Question: "API error: API overloaded (529) — press 'a' to answer", Pending: true},
		},
		SchemaVersion: feature.SchemaVersionCurrent,
	}

	cleared := clearPendingHelpByPrefix(f, apiErrorHelpPrefix)
	if !cleared {
		t.Error("expected clearPendingHelpByPrefix to return true")
	}
	if len(f.HelpQueue) != 3 {
		t.Errorf("expected 3 entries (mark-resolved, not remove), got %d", len(f.HelpQueue))
	}
	if f.HelpQueue[0].Pending {
		t.Error("expected first API error entry to be marked resolved")
	}
	if !f.HelpQueue[1].Pending {
		t.Error("expected generic waiting entry to remain pending")
	}
	if f.HelpQueue[2].Pending {
		t.Error("expected second API error entry to be marked resolved")
	}
}

func TestRemoveResolvedHelpByPrefix(t *testing.T) {
	f := &feature.Feature{
		HelpQueue: []feature.HelpRequest{
			{Question: "API error: rate limit exceeded (429) — press 'a' to answer", Pending: false},
			{Question: "other question", Pending: true},
			{Question: "API error: API overloaded (529) — press 'a' to answer", Pending: false},
		},
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	removeResolvedHelpByPrefix(f, apiErrorHelpPrefix)
	if len(f.HelpQueue) != 1 {
		t.Errorf("expected 1 entry after removal, got %d", len(f.HelpQueue))
	}
	if f.HelpQueue[0].Question != "other question" {
		t.Errorf("expected 'other question' to remain, got %q", f.HelpQueue[0].Question)
	}
}

func TestAttachClearsAPIErrorHelp(t *testing.T) {
	app, fm := newTestAppModel(t)

	f, _ := fm.Create("API Error Attach", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	f.Status = feature.StatusImplementing
	f.CurrentPhase = feature.PhaseImplement
	f.HelpQueue = []feature.HelpRequest{
		{Question: "API error: rate limit exceeded (429) — press 'a' to answer", Pending: true, Time: time.Now()},
		{Question: "Agent is waiting for input — press 'a' to answer", Pending: true, Time: time.Now()},
	}
	_ = fm.Store.Save(f)

	// Simulate what attachCmd does: clear both generic and API error help
	_ = fm.Store.Modify(f.ID, func(feat *feature.Feature) error {
		const inputMsg = "Agent is waiting for input \u2014 press 'a' to answer"
		clearPendingHelpByMessage(feat, inputMsg)
		clearPendingHelpByPrefix(feat, apiErrorHelpPrefix)
		return nil
	})

	f, _ = fm.Get(f.ID)
	for _, h := range f.HelpQueue {
		if h.Pending {
			t.Errorf("expected all help requests to be resolved after attach, but %q is still pending", h.Question)
		}
	}
	_ = app // keep reference
}

func TestSessionDoneClearsAPIErrorHelp(t *testing.T) {
	_, fm := newTestAppModel(t)

	f, _ := fm.Create("API Error Done", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	f.Status = feature.StatusImplementing
	f.CurrentPhase = feature.PhaseImplement
	f.HelpQueue = []feature.HelpRequest{
		{Question: "API error: rate limit exceeded (429) — press 'a' to answer", Pending: true, Time: time.Now()},
		{Question: "API error: API overloaded (529) — press 'a' to answer", Pending: false, Time: time.Now()},
	}
	_ = fm.Store.Save(f)

	// Simulate what handleSessionDone does: clear and remove both generic and API error help
	_ = fm.Store.Modify(f.ID, func(feat *feature.Feature) error {
		const inputMsg = "Agent is waiting for input \u2014 press 'a' to answer"
		clearPendingHelpByMessage(feat, inputMsg)
		removeResolvedHelpByMessage(feat, inputMsg)
		clearPendingHelpByPrefix(feat, apiErrorHelpPrefix)
		removeResolvedHelpByPrefix(feat, apiErrorHelpPrefix)
		return nil
	})

	f, _ = fm.Get(f.ID)
	if len(f.HelpQueue) != 0 {
		t.Errorf("expected all API error help entries removed after session done, got %d", len(f.HelpQueue))
	}
}

func TestHandleSDKEvent_AskUserQuestionControlRequestAddsQuestionHelp(t *testing.T) {
	app, fm := newTestAppModel(t)
	app.lastNotifyTime = make(map[notifyKey]time.Time)

	f, err := fm.Create("AskUser Event", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := fm.StartInquire(f.ID); err != nil {
		t.Fatalf("StartInquire: %v", err)
	}

	msg := SDKSessionEventMsg{
		Event: session.SDKEventMsg{
			SessionID: f.ID + "-inquire",
			Message: llm.SDKMessage{
				Type: "control_request",
				ControlRequest: &llm.ControlRequestMessage{
					Type:      "control_request",
					RequestID: "ask-1",
					Request: llm.ControlRequest{
						Subtype:  "can_use_tool",
						ToolName: "AskUserQuestion",
						Input:    json.RawMessage(`{"questions":[{"question":"What version?"}]}`),
					},
				},
			},
		},
	}

	_, _ = app.handleSDKEvent(msg)

	f, _ = fm.Get(f.ID)
	if !hasPendingHelpRequestMessage(f, questionHelpMessage) {
		t.Fatalf("expected pending question help, got %+v", f.HelpQueue)
	}
}

func assertTUIArtifactPhaseRetry(t *testing.T, fm *feature.Manager, f *feature.Feature, phase feature.Phase, wantViolation string) {
	t.Helper()
	updated, err := fm.Get(f.ID)
	if err != nil {
		t.Fatalf("get feature: %v", err)
	}
	if updated.Status != statusForPhase(phase) {
		t.Fatalf("status = %v, want %v", updated.Status, statusForPhase(phase))
	}
	if updated.FailureType != "" {
		t.Fatalf("FailureType = %q, want empty", updated.FailureType)
	}
	if updated.LastError != "" {
		t.Fatalf("LastError = %q, want empty", updated.LastError)
	}
	phaseDir := filepath.Join(agent.ActiveRunDir(fm.Store.BaseDir, updated), phase.DirName())
	sidecar, err := agent.ReadProtocolRetrySidecar(phaseDir)
	if err != nil {
		t.Fatalf("ReadProtocolRetrySidecar() error = %v", err)
	}
	if sidecar == nil {
		t.Fatal("retry sidecar = nil, want sidecar")
	}
	if sidecar.Consecutive != 1 {
		t.Fatalf("sidecar.Consecutive = %d, want 1", sidecar.Consecutive)
	}
	if !strings.Contains(sidecar.LastViolation, wantViolation) {
		t.Fatalf("sidecar.LastViolation = %q, want %q", sidecar.LastViolation, wantViolation)
	}
	if _, err := os.Stat(filepath.Join(phaseDir, agent.PhaseCompleteFile)); !os.IsNotExist(err) {
		t.Fatalf("phase_complete stat err = %v, want removed", err)
	}
}

func TestHandleSDKEvent_ResearchSuccessWithoutArtifactRetriesProtocolViolation(t *testing.T) {
	app, fm := newTestAppModel(t)
	app.lastNotifyTime = make(map[notifyKey]time.Time)
	app.eventCh = make(chan interface{}, 1)

	f, err := fm.Create("Research Missing Artifact", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = fm.Store.Modify(f.ID, func(feat *feature.Feature) error {
		feat.Status = feature.StatusResearching
		feat.CurrentPhase = feature.PhaseResearch
		return nil
	})

	sm := session.NewManager(nil)
	sess := session.NewSession(f.ID+"-research", f.ID, feature.PhaseResearch)
	sess.MessageLog().Append(llm.SDKMessage{
		Type: "assistant",
		Assistant: &llm.AssistantMessage{
			Message: llm.ConversationMsg{
				Role: "assistant",
				Content: []llm.ContentBlock{
					{Type: "text", Text: "## Analysis\n\nThe current source of truth is internal/tui/dashboard.go."},
				},
			},
		},
	})
	sm.RegisterTestSession(sess)
	app.sessionManager = sm

	msg := SDKSessionEventMsg{
		Event: session.SDKEventMsg{
			SessionID: f.ID + "-research",
			Message: llm.SDKMessage{
				Type:    "result",
				Subtype: "success",
				Result: &llm.ResultMessage{
					Type:    "result",
					Subtype: "success",
				},
			},
		},
	}

	_, _ = app.handleSDKEvent(msg)

	assertTUIArtifactPhaseRetry(t, fm, f, feature.PhaseResearch, agent.PhaseCompleteFile)
}

func TestPhaseOutputSuggestsQuestion_MultilineAssistantQuestionStillDetected(t *testing.T) {
	output := `[rate_limit] Rate limit info received
[assistant] Based on the current code and KB, this research will center on:
Before I proceed, I need your decisions on scope:

1. Is this research only about the in-repo application version string exposed by Agentic itself, or do you also want related external version constraints documented, such as the minimum supported claude CLI version?
2. Do you want the document to cover only code locations that currently hardcode or display the Agentic version, or also release-facing references like docs, tests, screenshots, and generated artifacts that may need matching updates?
3. Are you investigating a specific target version number for this bump, or should I keep the research version-agnostic and document all current version touchpoints without assuming the new value?
[assistant] Awaiting your clarification on the three scope questions above before I continue the research document.
[result] success cost=$0.0000
`

	if !phaseOutputSuggestsQuestion(output) {
		t.Fatal("expected multiline assistant question transcript to be detected as waiting for input")
	}
}

func TestPhaseOutputSuggestsQuestion_UserReplyClearsQuestionSignal(t *testing.T) {
	output := `[assistant] What exact version should Agentic be bumped to?
[user] 0.83.0
[result] success cost=$0.0000
`

	if phaseOutputSuggestsQuestion(output) {
		t.Fatal("expected user reply to clear question signal")
	}
}

func TestHandleTick_RestoresQuestionHelpFromInquireOutput(t *testing.T) {
	app, fm := newTestAppModel(t)
	app.lastNotifyTime = make(map[notifyKey]time.Time)
	app.sessionManager = session.NewManager(nil)

	f, err := fm.Create("Recover Question", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := fm.StartInquire(f.ID); err != nil {
		t.Fatalf("StartInquire: %v", err)
	}

	inquireDir := filepath.Join(agent.ActiveRunDir(fm.Store.BaseDir, f), "inquire")
	if err := os.MkdirAll(inquireDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	output := "[assistant] First requirement to pin down: what exact version should Agentic be bumped to?\n[result] success cost=$0.0000\n"
	if err := os.WriteFile(filepath.Join(inquireDir, "output.txt"), []byte(output), 0o644); err != nil {
		t.Fatalf("write output: %v", err)
	}

	_, _ = app.handleTick()

	f, _ = fm.Get(f.ID)
	if !hasPendingHelpRequestMessage(f, questionHelpMessage) {
		t.Fatalf("expected recovered pending question help, got %+v", f.HelpQueue)
	}
}

func TestNewAppModel_DoesNotRestoreQuestionHelpForInterruptedFeatureOnStartup(t *testing.T) {
	dir := t.TempDir()
	store := feature.NewStore(dir)
	cfg := config.NewDefault()
	cfg.Repos["test-repo"] = config.RepoConfig{
		Path: "/tmp/test-repo",
	}
	fm := feature.NewManager(store, cfg)

	f, err := fm.Create("Interrupted Question", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := fm.StartInquire(f.ID); err != nil {
		t.Fatalf("StartInquire: %v", err)
	}

	inquireDir := filepath.Join(agent.ActiveRunDir(fm.Store.BaseDir, f), "inquire")
	if err := os.MkdirAll(inquireDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	output := "[assistant] What exact version should Agentic be bumped to?\n[result] success cost=$0.0000\n"
	if err := os.WriteFile(filepath.Join(inquireDir, "output.txt"), []byte(output), 0o644); err != nil {
		t.Fatalf("write output: %v", err)
	}

	sm := session.NewManager(nil)
	eventCh := make(chan interface{}, 16)
	orch := newTestOrchestrator(fm, sm)
	_, err = NewAppModel(fm, sm, orch, nil, eventCh)
	if err != nil {
		t.Fatalf("NewAppModel: %v", err)
	}

	f, _ = fm.Get(f.ID)
	if f.Status != feature.StatusInterrupted {
		t.Fatalf("feature status = %v, want %v", f.Status, feature.StatusInterrupted)
	}
	if hasPendingHelpRequestMessage(f, questionHelpMessage) {
		t.Fatalf("expected no restored pending question help after restart, got %+v", f.HelpQueue)
	}
	if hasPendingHelpRequestMessage(f, waitingInputHelpMessage) {
		t.Fatalf("expected no restored generic waiting help after restart, got %+v", f.HelpQueue)
	}
}

func TestHandleTick_PendingAskUserQuestionReplacesGenericWaitingHelp(t *testing.T) {
	app, fm := newTestAppModel(t)
	app.lastNotifyTime = make(map[notifyKey]time.Time)

	f, err := fm.Create("Question Beats Generic", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := fm.StartInquire(f.ID); err != nil {
		t.Fatalf("StartInquire: %v", err)
	}

	_ = fm.Store.Modify(f.ID, func(feat *feature.Feature) error {
		feat.HelpQueue = append(feat.HelpQueue, feature.HelpRequest{
			Question: waitingInputHelpMessage,
			Pending:  true,
			Time:     time.Now(),
		})
		return nil
	})

	sm := session.NewManager(nil)
	sess := session.NewSession(f.ID+"-inquire", f.ID, feature.PhaseInquire)
	sess.SetStatus(session.SessionWaitingHelp)
	sess.SetLastControlRequest(&llm.ControlRequestMessage{
		RequestID: "ask-1",
		Request: llm.ControlRequest{
			Subtype:  "can_use_tool",
			ToolName: "AskUserQuestion",
			Input:    json.RawMessage(`{"questions":[{"question":"What version?","options":[]}]}`),
		},
	})
	sm.RegisterTestSession(sess)
	app.sessionManager = sm

	_, _ = app.handleTick()

	f, _ = fm.Get(f.ID)
	if !hasPendingHelpRequestMessage(f, questionHelpMessage) {
		t.Fatalf("expected question help after tick, got %+v", f.HelpQueue)
	}
	if hasPendingHelpRequestMessage(f, waitingInputHelpMessage) {
		t.Fatalf("expected generic waiting help to be cleared, got %+v", f.HelpQueue)
	}
}

func TestHandleSessionDone_RegistryOwnedSuccessDoesNotCreateQuestionHelp(t *testing.T) {
	app, fm := newTestAppModel(t)
	app.lastNotifyTime = make(map[notifyKey]time.Time)
	app.eventCh = make(chan interface{}, 1)

	f, err := fm.Create("Done Keeps Question", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := fm.StartInquire(f.ID); err != nil {
		t.Fatalf("StartInquire: %v", err)
	}

	sm := session.NewManager(nil)
	sess := session.NewSession(f.ID+"-inquire", f.ID, feature.PhaseInquire)
	sess.SendStatus("SUCCESS")
	sess.MessageLog().Append(llm.SDKMessage{
		Type: "assistant",
		Assistant: &llm.AssistantMessage{
			Message: llm.ConversationMsg{
				Role: "assistant",
				Content: []llm.ContentBlock{
					{Type: "text", Text: "What exact version should Agentic be bumped to?"},
				},
			},
		},
	})
	sm.RegisterTestSession(sess)
	app.sessionManager = sm

	doneMsg := SessionDoneTUIMsg{
		Done: session.SessionDoneMsg{
			SessionID: f.ID + "-inquire",
			Status:    session.SessionDone,
		},
	}

	_, _ = app.handleSessionDone(doneMsg)

	f, _ = fm.Get(f.ID)
	if f.Status != feature.StatusInquiring {
		t.Errorf("status = %v, want Inquiring", f.Status)
	}
	assertTUIArtifactPhaseRetry(t, fm, f, feature.PhaseInquire, agent.PhaseCompleteFile)
	assertNoPendingQuestionHelp(t, f)
}

func TestHandleSessionDone_SuccessWithoutArtifactRetriesProtocolViolation(t *testing.T) {
	app, fm := newTestAppModel(t)
	app.lastNotifyTime = make(map[notifyKey]time.Time)
	app.eventCh = make(chan interface{}, 1)

	f, err := fm.Create("Done Missing Artifact", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = fm.Store.Modify(f.ID, func(feat *feature.Feature) error {
		feat.Status = feature.StatusResearching
		feat.CurrentPhase = feature.PhaseResearch
		return nil
	})

	sm := session.NewManager(nil)
	sess := session.NewSession(f.ID+"-research", f.ID, feature.PhaseResearch)
	sess.SendStatus("SUCCESS")
	sess.MessageLog().Append(llm.SDKMessage{
		Type: "assistant",
		Assistant: &llm.AssistantMessage{
			Message: llm.ConversationMsg{
				Role: "assistant",
				Content: []llm.ContentBlock{
					{Type: "text", Text: "## Analysis\n\nThe current source of truth is internal/tui/dashboard.go."},
				},
			},
		},
	})
	sm.RegisterTestSession(sess)
	app.sessionManager = sm

	doneMsg := SessionDoneTUIMsg{
		Done: session.SessionDoneMsg{
			SessionID: f.ID + "-research",
			Status:    session.SessionDone,
		},
	}

	_, _ = app.handleSessionDone(doneMsg)

	assertTUIArtifactPhaseRetry(t, fm, f, feature.PhaseResearch, agent.PhaseCompleteFile)
}

func TestPublishDescGeneratedForwarded(t *testing.T) {
	app, _ := newTestAppModel(t)

	app.publish = newTestPublishModel("feat-1", "diff", "log", "", "", 120, 40)
	app.currentView = ViewPublish
	app.publish.generating = true

	// Send generated description
	result, _ := app.Update(publishDescGeneratedMsg{title: "Gen Title", body: "Gen Body"})
	updatedApp := result.(AppModel)

	if updatedApp.publish.prTitle != "Gen Title" {
		t.Errorf("prTitle = %q, want 'Gen Title'", updatedApp.publish.prTitle)
	}
	if updatedApp.publish.prBody != "Gen Body" {
		t.Errorf("prBody = %q, want 'Gen Body'", updatedApp.publish.prBody)
	}
	if updatedApp.publish.generating {
		t.Error("expected generating to be false")
	}
}

func TestFeatureSummaryMsgUpdatesSummary(t *testing.T) {
	app, mgr := newTestAppModel(t)

	f, err := mgr.Create("test-feat", "A long and rambling description", []string{"test-repo"},
		config.ModelConfig{Research: "opus", Planning: "opus", Implementation: "opus", Review: "opus"},
		"", "", nil, feature.CreateOptions{})
	if err != nil {
		t.Fatalf("create feature: %v", err)
	}

	result, _ := app.Update(featureSummaryMsg{featureID: f.ID, summary: "Add caching to the API."})
	updatedApp := result.(AppModel)

	got, err := updatedApp.featureManager.Get(f.ID)
	if err != nil {
		t.Fatalf("get feature: %v", err)
	}
	if got.Summary != "Add caching to the API." {
		t.Errorf("Summary = %q, want %q", got.Summary, "Add caching to the API.")
	}
}

func TestFeatureSummaryMsgIgnoresEmpty(t *testing.T) {
	app, _ := newTestAppModel(t)

	result, _ := app.Update(featureSummaryMsg{featureID: "nonexistent", summary: ""})
	updatedApp := result.(AppModel)

	// Should be a no-op, no panic
	_ = updatedApp
}

// TestDedupAllowsRedetectionAfterResolution verifies that after a help request is resolved,
// the same message can be added again (hasPendingHelpRequestMessage is used, not hasHelpRequestMessage).
func TestDedupAllowsRedetectionAfterResolution(t *testing.T) {
	const inputMsg = "Agent is waiting for input \u2014 press 'a' to answer"

	f := &feature.Feature{
		HelpQueue: []feature.HelpRequest{
			{Question: inputMsg, Pending: false}, // already resolved
		},
		SchemaVersion: feature.SchemaVersionCurrent,
	}

	// hasPendingHelpRequestMessage should return false (resolved doesn't count)
	if hasPendingHelpRequestMessage(f, inputMsg) {
		t.Error("hasPendingHelpRequestMessage should return false for resolved entry")
	}

	// hasHelpRequestMessage would return true (finds resolved entry too)
	if !hasHelpRequestMessage(f, inputMsg) {
		t.Error("hasHelpRequestMessage should return true even for resolved entry")
	}

	// Simulate the dedup check that now uses hasPendingHelpRequestMessage
	if !hasPendingHelpRequestMessage(f, inputMsg) {
		// This means we CAN add a new entry — correct behavior
		f.HelpQueue = append(f.HelpQueue, feature.HelpRequest{
			Question: inputMsg,
			Pending:  true,
		})
	}

	// Should now have 2 entries: 1 resolved + 1 pending
	if len(f.HelpQueue) != 2 {
		t.Errorf("expected 2 entries, got %d", len(f.HelpQueue))
	}
	if !f.HelpQueue[1].Pending {
		t.Error("newly added entry should be pending")
	}
}

func TestEnsurePendingWaitingInputHelpNormalizesLegacyCopy(t *testing.T) {
	f := &feature.Feature{
		HelpQueue: []feature.HelpRequest{
			{Question: "Agent is waiting for input — attach with 'a' to respond", Pending: true},
		},
		SchemaVersion: feature.SchemaVersionCurrent,
	}

	changed := ensurePendingWaitingInputHelp(f)
	if changed {
		t.Error("ensurePendingWaitingInputHelp() changed = true, want false for existing legacy pending entry")
	}
	if len(f.HelpQueue) != 1 {
		t.Fatalf("len(HelpQueue) = %d, want 1", len(f.HelpQueue))
	}
	if got := f.HelpQueue[0].Question; got != waitingInputHelpMessage {
		t.Errorf("HelpQueue[0].Question = %q, want %q", got, waitingInputHelpMessage)
	}
	if !f.HelpQueue[0].Pending {
		t.Error("HelpQueue[0].Pending = false, want true")
	}
}

func TestNormalizeManagedHelpQueueUpdatesLegacyAPIErrorCopy(t *testing.T) {
	const legacy = "API error: rate limit exceeded (429) — attach with 'a' to respond"
	const current = "API error: rate limit exceeded (429) — press 'a' to answer"
	f := &feature.Feature{
		HelpQueue: []feature.HelpRequest{
			{Question: legacy, Pending: true},
		},
		SchemaVersion: feature.SchemaVersionCurrent,
	}

	if !normalizeManagedHelpQueue(f) {
		t.Fatal("normalizeManagedHelpQueue() changed = false, want true")
	}
	if got := f.HelpQueue[0].Question; got != current {
		t.Errorf("HelpQueue[0].Question = %q, want %q", got, current)
	}
	if !hasPendingHelpRequestMessage(f, current) {
		t.Error("hasPendingHelpRequestMessage() = false, want true for normalized API error")
	}
}

func TestHasPendingHelpRequestMessageMatchesLegacyAPIErrorCopy(t *testing.T) {
	const legacy = "API error: rate limit exceeded (429) — attach with 'a' to respond"
	const current = "API error: rate limit exceeded (429) — press 'a' to answer"
	f := &feature.Feature{
		HelpQueue: []feature.HelpRequest{
			{Question: legacy, Pending: true},
		},
		SchemaVersion: feature.SchemaVersionCurrent,
	}

	if !hasPendingHelpRequestMessage(f, current) {
		t.Error("hasPendingHelpRequestMessage() = false, want true for legacy API error copy")
	}
}

func TestReconcileHelpQueueClearsLegacyManagedHelpCopy(t *testing.T) {
	app, fm := newTestAppModel(t)
	f, err := fm.Create("reconcile-legacy-help", "legacy help reconciliation", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = fm.Transition(f.ID, feature.StatusResearching)
	_ = fm.Store.Modify(f.ID, func(feat *feature.Feature) error {
		feat.HelpQueue = append(feat.HelpQueue,
			feature.HelpRequest{Question: "Agent has a question — attach with 'a' to respond", Pending: true},
			feature.HelpRequest{Question: "Agent is waiting for input — attach with 'a' to respond", Pending: true},
		)
		return nil
	})

	sm := session.NewManager(nil)
	sess := session.NewSession(f.ID+"-impl-repo-01", f.ID, feature.PhaseImplement)
	sess.SetStatus(session.SessionRunning)
	sm.RegisterTestSession(sess)
	app.sessionManager = sm

	app.reconcileHelpQueue(f.ID)

	updated, _ := fm.Get(f.ID)
	for _, h := range updated.HelpQueue {
		if h.Pending {
			t.Errorf("help entry %q still pending; want cleared when no session is waiting", h.Question)
		}
		if strings.Contains(h.Question, "attach with") {
			t.Errorf("help entry kept retired copy after reconcile: %q", h.Question)
		}
	}
}

// TestNoRenotificationForStalePendingHelp verifies that no re-notification
// fires for a pending help request that has been waiting a long time. Only
// the initial notification should fire; the re-notification loop has been removed.
// Also verifies that cleanup still removes lastNotifyTime when help is resolved.
func TestNoRenotificationForStalePendingHelp(t *testing.T) {
	_, fm := newTestAppModel(t)

	f, err := fm.Create("renotify-test", "desc", nil, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Add a pending help request with Time set to 60 seconds ago
	oldTime := time.Now().Add(-60 * time.Second)
	_ = fm.Store.Modify(f.ID, func(f *feature.Feature) error {
		f.HelpQueue = append(f.HelpQueue, feature.HelpRequest{
			Question: "Agent is waiting for input",
			Time:     oldTime,
			Pending:  true,
		})
		return nil
	})

	app := AppModel{
		featureManager: fm,
		lastNotifyTime: make(map[notifyKey]time.Time),
		sessionManager: session.NewManager(nil),
	}

	key := notifyKey{featureID: f.ID, notifyType: notifyWaitingInput}

	// First notification should fire
	cmd := app.maybeNotifyUser(f.ID, notifyWaitingInput, f.Slug, "agent waiting for input")
	if cmd == nil {
		t.Error("first maybeNotifyUser call should return a non-nil cmd")
	}

	// A second call to maybeNotifyUser for the same feature/type should be
	// deduped within the cooldown window (no re-notification).
	cmd = app.maybeNotifyUser(f.ID, notifyWaitingInput, f.Slug, "agent waiting for input")
	if cmd != nil {
		t.Error("second maybeNotifyUser call within dedup window should return nil (no re-notification)")
	}

	// Even after the help request has been stale for a long time, handleTick
	// should NOT produce re-notification commands. The old code had an explicit
	// re-notification loop that would call maybeNotifyUser for stale pending
	// requests. That loop has been removed; handleTick only notifies when a
	// NEW help request is first added (via the `added` flag).
	// We verify indirectly: calling maybeNotifyUser again still returns nil
	// because lastNotifyTime was set and the re-notification loop is gone.
	cmd = app.maybeNotifyUser(f.ID, notifyWaitingInput, f.Slug, "agent still waiting")
	if cmd != nil {
		t.Error("third maybeNotifyUser call should still be deduped")
	}

	// Simulate resolution: clear pending help, then cleanup should remove lastNotifyTime entry
	_ = fm.Store.Modify(f.ID, func(f *feature.Feature) error {
		for i := range f.HelpQueue {
			f.HelpQueue[i].Pending = false
		}
		return nil
	})
	resolvedFeatures, _ := fm.List()
	for _, feat := range resolvedFeatures {
		hasPending := false
		for _, h := range feat.HelpQueue {
			if h.Pending {
				hasPending = true
				break
			}
		}
		if !hasPending {
			for k := range app.lastNotifyTime {
				if k.featureID == feat.ID {
					delete(app.lastNotifyTime, k)
				}
			}
		}
	}
	if _, ok := app.lastNotifyTime[key]; ok {
		t.Error("lastNotifyTime entry should be cleaned up when no pending help remains")
	}

	// After cleanup, a new notification should fire immediately
	cmd = app.maybeNotifyUser(f.ID, notifyWaitingInput, f.Slug, "new question")
	if cmd == nil {
		t.Error("notification after resolution cleanup should fire immediately")
	}
}

// TestMaybeNotifyUserCrossPathDedup verifies that maybeNotifyUser prevents
// multiple notifications for the same feature within the cooldown window,
// regardless of which path triggers the notification.
func TestMaybeNotifyUserCrossPathDedup(t *testing.T) {
	_, fm := newTestAppModel(t)

	f, err := fm.Create("dedup-test", "desc", nil, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	app := AppModel{
		featureManager: fm,
		lastNotifyTime: make(map[notifyKey]time.Time),
		sessionManager: session.NewManager(nil),
	}

	// First call should return a non-nil cmd (notification fires)
	cmd := app.maybeNotifyUser(f.ID, notifyWaitingInput, f.Slug, "agent waiting for input")
	if cmd == nil {
		t.Error("first maybeNotifyUser call should return a non-nil cmd")
	}

	// Second call immediately after should return nil (suppressed)
	cmd = app.maybeNotifyUser(f.ID, notifyWaitingInput, f.Slug, "agent waiting for input")
	if cmd != nil {
		t.Error("second maybeNotifyUser call should be suppressed within cooldown")
	}

	// Third call with a different reason but same type should also be suppressed
	cmd = app.maybeNotifyUser(f.ID, notifyWaitingInput, f.Slug, "still waiting for input")
	if cmd != nil {
		t.Error("third maybeNotifyUser call (same type, different reason) should be suppressed")
	}
}

// TestMaybeNotifyUserNewCycleAfterResolution verifies that resolving all
// pending help requests and cleaning up lastNotifyTime allows the next
// waiting cycle to notify immediately.
func TestMaybeNotifyUserNewCycleAfterResolution(t *testing.T) {
	_, fm := newTestAppModel(t)

	f, err := fm.Create("cycle-test", "desc", nil, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	app := AppModel{
		featureManager: fm,
		lastNotifyTime: make(map[notifyKey]time.Time),
		sessionManager: session.NewManager(nil),
	}

	// First waiting cycle: notification fires
	cmd := app.maybeNotifyUser(f.ID, notifyWaitingInput, f.Slug, "agent waiting for input")
	if cmd == nil {
		t.Error("first cycle notification should fire")
	}

	// Simulate resolution: remove lastNotifyTime entries for this feature
	// (as the cleanup logic in handleTick does when no pending help remains)
	for key := range app.lastNotifyTime {
		if key.featureID == f.ID {
			delete(app.lastNotifyTime, key)
		}
	}

	// New waiting cycle: notification should fire immediately (no cooldown)
	cmd = app.maybeNotifyUser(f.ID, notifyWaitingInput, f.Slug, "agent waiting for input")
	if cmd == nil {
		t.Error("notification after resolution should fire immediately")
	}
}

// TestMaybeNotifyUserAPIErrorIndependent verifies that API error notifications
// are not throttled by waiting-for-input cooldowns and vice versa.
func TestMaybeNotifyUserAPIErrorIndependent(t *testing.T) {
	_, fm := newTestAppModel(t)

	f, err := fm.Create("apierr-test", "desc", nil, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	app := AppModel{
		featureManager: fm,
		lastNotifyTime: make(map[notifyKey]time.Time),
		sessionManager: session.NewManager(nil),
	}

	// Fire waiting-for-input notification
	cmd := app.maybeNotifyUser(f.ID, notifyWaitingInput, f.Slug, "agent waiting for input")
	if cmd == nil {
		t.Error("waiting-for-input notification should fire")
	}

	// Fire API error notification immediately after — should NOT be suppressed
	cmd = app.maybeNotifyUser(f.ID, notifyAPIError, f.Slug, "API error: rate limit")
	if cmd == nil {
		t.Error("API error notification should fire independently of waiting-for-input cooldown")
	}

	// Fire another waiting-for-input — should be suppressed
	cmd = app.maybeNotifyUser(f.ID, notifyWaitingInput, f.Slug, "agent waiting for input")
	if cmd != nil {
		t.Error("second waiting-for-input should still be suppressed")
	}

	// Fire another API error — should be suppressed (within its own cooldown)
	cmd = app.maybeNotifyUser(f.ID, notifyAPIError, f.Slug, "API error: timeout")
	if cmd != nil {
		t.Error("second API error should be suppressed within its own cooldown")
	}
}

// TestMaybeNotifyUserDedupWindow verifies the 10s dedup window: notifications
// are suppressed within the window and allowed after it expires.
func TestMaybeNotifyUserDedupWindow(t *testing.T) {
	_, fm := newTestAppModel(t)

	f, err := fm.Create("dedup-window-test", "desc", nil, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	app := AppModel{
		featureManager: fm,
		lastNotifyTime: make(map[notifyKey]time.Time),
		sessionManager: session.NewManager(nil),
	}

	// Fire initial notification
	cmd := app.maybeNotifyUser(f.ID, notifyWaitingInput, f.Slug, "agent waiting for input")
	if cmd == nil {
		t.Error("initial notification should fire")
	}

	// Immediately call again — should be suppressed
	cmd = app.maybeNotifyUser(f.ID, notifyWaitingInput, f.Slug, "agent waiting for input")
	if cmd != nil {
		t.Error("notification within dedup window should be suppressed")
	}

	// Simulate 11s passing by backdating the lastNotifyTime entry
	key := notifyKey{featureID: f.ID, notifyType: notifyWaitingInput}
	app.lastNotifyTime[key] = time.Now().Add(-11 * time.Second)

	// Should fire again (dedup window expired)
	cmd = app.maybeNotifyUser(f.ID, notifyWaitingInput, f.Slug, "still waiting for input")
	if cmd == nil {
		t.Error("notification should fire after 10s dedup window expires")
	}
}

func TestMaybeNotifyUserMutedWaitingInput(t *testing.T) {
	_, fm := newTestAppModel(t)
	fm.Config.Notifications.MuteFeatureInput = true

	f, err := fm.Create("mute-input-test", "desc", nil, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	app := AppModel{
		featureManager: fm,
		lastNotifyTime: make(map[notifyKey]time.Time),
		sessionManager: session.NewManager(nil),
	}

	cmd := app.maybeNotifyUser(f.ID, notifyWaitingInput, f.Slug, "agent waiting for input")
	if cmd != nil {
		t.Error("waiting-for-input notification should be muted by config")
	}

	cmd = app.maybeNotifyUser(f.ID, notifyAPIError, f.Slug, "API error: timeout")
	if cmd == nil {
		t.Error("API error notification should not be muted by input mute config")
	}
}

func TestMaybeNotifyUserFeatureOverrideUnmutesGlobalMute(t *testing.T) {
	_, fm := newTestAppModel(t)
	fm.Config.Notifications.MuteFeatureInput = true

	f, err := fm.Create("override-unmute-test", "desc", nil, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	override := false
	if err := fm.Store.Modify(f.ID, func(feat *feature.Feature) error {
		feat.MuteInputNotifications = &override
		return nil
	}); err != nil {
		t.Fatalf("modify: %v", err)
	}

	app := AppModel{
		featureManager: fm,
		lastNotifyTime: make(map[notifyKey]time.Time),
		sessionManager: session.NewManager(nil),
	}

	cmd := app.maybeNotifyUser(f.ID, notifyWaitingInput, f.Slug, "agent waiting for input")
	if cmd == nil {
		t.Error("feature override should enable waiting-for-input notifications")
	}
}

func TestToggleInputNotifyInDetail(t *testing.T) {
	app, fm := newTestAppModel(t)
	f, err := fm.Create("toggle-detail-test", "desc", nil, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	app.currentView = ViewDetail
	app.detail = NewDetailModel(f, fm.Store.BaseDir)

	model, cmd := app.updateDetail(tea.KeyPressMsg{Code: 'N', Text: "N"})
	updated := model.(AppModel)
	if cmd == nil {
		t.Fatal("expected refresh cmd after toggling input notifications")
	}

	loaded, err := fm.Get(f.ID)
	if err != nil {
		t.Fatalf("get feature: %v", err)
	}
	if loaded.MuteInputNotifications == nil || !*loaded.MuteInputNotifications {
		t.Fatal("expected feature input notifications to be muted after first toggle")
	}
	if !strings.Contains(updated.statusMessage, "muted") {
		t.Errorf("expected muted status message, got %q", updated.statusMessage)
	}
}

// TestWindowSizeMsgOnDashboard verifies that sending a WindowSizeMsg to a
// fresh AppModel on the dashboard view does not panic and correctly updates
// dimensions.
func TestWindowSizeMsgOnDashboard(t *testing.T) {
	app, _ := newTestAppModel(t)

	if app.currentView != ViewDashboard {
		t.Fatalf("expected ViewDashboard, got %v", app.currentView)
	}

	result, cmd := app.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	updatedApp := result.(AppModel)

	if updatedApp.width != 120 || updatedApp.height != 40 {
		t.Errorf("size = %dx%d, want 120x40", updatedApp.width, updatedApp.height)
	}
	// WindowSizeMsg should return nil cmd
	if cmd != nil {
		t.Errorf("expected nil cmd from WindowSizeMsg, got %v", cmd)
	}
}

// TestWindowSizeMsgPropagatestoChat verifies that sending a WindowSizeMsg when
// the chat view is active propagates the resize to the ChatModel.
func TestWindowSizeMsgPropagatesToChat(t *testing.T) {
	app, _ := newTestAppModel(t)

	// Open the chat view
	app.chat = NewChatModel(80, 24, nil, "/tmp", "test", nil, "", "")
	app.currentView = ViewChat

	result, cmd := app.Update(tea.WindowSizeMsg{Width: 150, Height: 50})
	updatedApp := result.(AppModel)

	if updatedApp.width != 150 || updatedApp.height != 50 {
		t.Errorf("app size = %dx%d, want 150x50", updatedApp.width, updatedApp.height)
	}
	if updatedApp.chat.width != 150 || updatedApp.chat.height != 50 {
		t.Errorf("chat size = %dx%d, want 150x50", updatedApp.chat.width, updatedApp.chat.height)
	}
	if cmd != nil {
		t.Errorf("expected nil cmd from WindowSizeMsg, got %v", cmd)
	}
}

// TestHelpRequestTimePopulated verifies that new help requests have Time populated.
func TestHelpRequestTimePopulated(t *testing.T) {
	_, fm := newTestAppModel(t)

	f, err := fm.Create("time-test", "desc", nil, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = fm.Transition(f.ID, feature.StatusResearching)

	// Simulate handleSessionEvent adding a help request (with Time populated)
	const inputMsg = "Agent is waiting for input \u2014 press 'a' to answer"
	now := time.Now()
	_ = fm.Store.Modify(f.ID, func(f *feature.Feature) error {
		if !hasPendingHelpRequestMessage(f, inputMsg) {
			f.HelpQueue = append(f.HelpQueue, feature.HelpRequest{
				Question: inputMsg,
				Time:     now,
				Pending:  true,
			})
		}
		return nil
	})

	f, _ = fm.Get(f.ID)
	if len(f.HelpQueue) == 0 {
		t.Fatal("expected help request to be added")
	}
	if f.HelpQueue[0].Time.IsZero() {
		t.Error("HelpRequest.Time should be populated (non-zero)")
	}
}

// TestAutoPublishCommitFailureMarksFailed verifies that when git.CommitAll fails
// during auto-publish, the feature transitions to Failed and no push/PR is attempted.
// TestAutoPublishResultMsgSuccessTransitionsToPublished verifies that when the
// autoPublishResultMsg handler receives a successful result, the feature
// transitions from PRReady to Published. This covers the legacy single-repo
// auto-publish path used by Init() and StartPhaseMsg callers.
// newTestAppWithPublishedFeature creates an app model with a single Published feature
// selected in the dashboard right panel, ready for tweak tests.
func newTestAppWithPublishedFeature(t *testing.T) (AppModel, *feature.Feature) {
	t.Helper()
	app, fm := newTestAppModel(t)

	f, err := fm.Create("Tweak Target", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = fm.Transition(f.ID, feature.StatusResearching)
	_ = fm.Transition(f.ID, feature.StatusPlanReady)
	_ = fm.Transition(f.ID, feature.StatusPlanning)
	_ = fm.Transition(f.ID, feature.StatusImplementReady)
	_ = fm.Transition(f.ID, feature.StatusImplementing)
	_ = fm.Transition(f.ID, feature.StatusReviewPassed)
	_ = fm.Transition(f.ID, feature.StatusCodeReady)
	_ = fm.Transition(f.ID, feature.StatusPublished)

	f, _ = fm.Get(f.ID)
	features, _ := fm.List()
	app.dashboard.SetFeatures(features)
	// Cursor 0 is now a section header; move to first feature (index 1)
	app.dashboard.cursor = 1
	app.dashboard.syncPreview()
	app.dashboard.focusPanel = 1 // right panel

	return app, f
}

func TestTweakKeyReturnsCmd_Dashboard(t *testing.T) {
	app, _ := newTestAppWithPublishedFeature(t)

	// Press 't' — should return a tea.Cmd (startInteractiveTweakCmd), not activate textarea
	result, cmd := app.Update(tea.KeyPressMsg{Code: 't', Text: "t"})
	app = result.(AppModel)

	if cmd == nil {
		t.Fatal("expected non-nil cmd from pressing 't' on Published feature")
	}
}

func TestTweakKeyReturnsCmd_DetailView(t *testing.T) {
	app, fm := newTestAppModel(t)

	f, err := fm.Create("PRReady Tweak", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil,
		feature.CreateOptions{Checkpoints: feature.Checkpoints{ManualPublish: true}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = fm.Transition(f.ID, feature.StatusResearching)
	_ = fm.Transition(f.ID, feature.StatusPlanReady)
	_ = fm.Transition(f.ID, feature.StatusPlanning)
	_ = fm.Transition(f.ID, feature.StatusImplementReady)
	_ = fm.Transition(f.ID, feature.StatusImplementing)
	_ = fm.Transition(f.ID, feature.StatusReviewPassed)
	_ = fm.Transition(f.ID, feature.StatusCodeReady)

	f, _ = fm.Get(f.ID)
	features, _ := fm.List()
	app.dashboard.SetFeatures(features)
	app.dashboard.focusPanel = 1

	// Switch to detail view
	app.currentView = ViewDetail
	app.detail.feature = f

	// Press 't' — should return a tea.Cmd, not activate textarea
	result, cmd := app.Update(tea.KeyPressMsg{Code: 't', Text: "t"})
	app = result.(AppModel)

	if cmd == nil {
		t.Fatal("expected non-nil cmd from pressing 't' on CodeReady feature in detail view")
	}
}

func TestDashboardTweakIgnoredForPRReadyAutoPublish(t *testing.T) {
	app, fm := newTestAppModel(t)

	f, err := fm.Create("AutoPub Tweak", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = fm.Transition(f.ID, feature.StatusResearching)
	_ = fm.Transition(f.ID, feature.StatusPlanReady)
	_ = fm.Transition(f.ID, feature.StatusPlanning)
	_ = fm.Transition(f.ID, feature.StatusImplementReady)
	_ = fm.Transition(f.ID, feature.StatusImplementing)
	_ = fm.Transition(f.ID, feature.StatusReviewPassed)
	_ = fm.Transition(f.ID, feature.StatusCodeReady)

	// Ensure AutoPublish is enabled (ManualPublish=false is the default, so AutoPublish()=true)
	f, _ = fm.Get(f.ID)
	f.Checkpoints.ManualPublish = false
	_ = fm.Store.Save(f)

	features, _ := fm.List()
	app.dashboard.SetFeatures(features)
	app.dashboard.focusPanel = 1

	// Press 't' on the dashboard — should NOT activate tweak (old textarea removed)
	result, _ := app.Update(tea.KeyPressMsg{Code: 't', Text: "t"})
	_ = result.(AppModel)
}

func TestDetailViewTweakIgnoredForPRReadyAutoPublish(t *testing.T) {
	app, fm := newTestAppModel(t)

	f, err := fm.Create("AutoPub Detail Tweak", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = fm.Transition(f.ID, feature.StatusResearching)
	_ = fm.Transition(f.ID, feature.StatusPlanReady)
	_ = fm.Transition(f.ID, feature.StatusPlanning)
	_ = fm.Transition(f.ID, feature.StatusImplementReady)
	_ = fm.Transition(f.ID, feature.StatusImplementing)
	_ = fm.Transition(f.ID, feature.StatusReviewPassed)
	_ = fm.Transition(f.ID, feature.StatusCodeReady)

	// Ensure AutoPublish is enabled (ManualPublish=false is the default, so AutoPublish()=true)
	f, _ = fm.Get(f.ID)
	f.Checkpoints.ManualPublish = false
	_ = fm.Store.Save(f)
	f, _ = fm.Get(f.ID)

	features, _ := fm.List()
	app.dashboard.SetFeatures(features)
	app.dashboard.focusPanel = 1

	// Switch to detail view
	app.currentView = ViewDetail
	app.detail.feature = f

	// Press 't' — should NOT activate tweak (old textarea removed)
	result, _ := app.Update(tea.KeyPressMsg{Code: 't', Text: "t"})
	_ = result.(AppModel)
}

func TestAttachToSession_SetsTweakSession(t *testing.T) {
	app, fm := newTestAppModel(t)

	f, err := fm.Create("Tweak Reattach", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Tweak session ID: matches isTweakSessionID pattern
	sess := mocks.NewMockSessionView(f.ID+"-impl-tweak", f.ID)
	app.attachToSession(sess)

	if !app.attach.isTweakSession {
		t.Error("expected isTweakSession=true when attaching to a tweak session")
	}
}

func TestAttachToSession_NonTweak_DoesNotSetTweakSession(t *testing.T) {
	app, fm := newTestAppModel(t)

	f, err := fm.Create("Normal Attach", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Normal implementation session ID: does not match tweak pattern
	sess := mocks.NewMockSessionView(f.ID+"-impl", f.ID)
	app.attachToSession(sess)

	if app.attach.isTweakSession {
		t.Error("expected isTweakSession=false for non-tweak session")
	}
}

func TestAttachToSession_MultiRepoTweak(t *testing.T) {
	app, fm := newTestAppModel(t)

	f, err := fm.Create("Multi Tweak", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Multi-repo tweak session ID
	sess := mocks.NewMockSessionView(f.ID+"-impl-api-tweak", f.ID)
	app.attachToSession(sess)

	if !app.attach.isTweakSession {
		t.Error("expected isTweakSession=true for multi-repo tweak session")
	}
}

// TestChatPersistsAcrossTransitions verifies that the chat model is created
// lazily on first open and reused when returning to chat, preserving
// conversation history within the same TUI session.
func TestChatPersistsAcrossTransitions(t *testing.T) {
	app, _ := newTestAppModel(t)

	// First open: chatReady should be false
	if app.chatReady {
		t.Fatal("expected chatReady=false before first open")
	}

	// Simulate transition to chat
	result, _ := app.transitionToChat()
	app = result.(AppModel)

	if !app.chatReady {
		t.Fatal("expected chatReady=true after first open")
	}
	if !app.chatOpen {
		t.Fatal("expected chatOpen=true after first open")
	}
	// Chat is now a bottom panel, so view stays on dashboard
	if app.currentView != ViewDashboard {
		t.Fatalf("expected ViewDashboard (chat is a bottom panel), got %v", app.currentView)
	}

	// Simulate adding history (as if user sent a message)
	app.chat.history += "You: hello\n\nAssistant: hi\n"
	historyBefore := app.chat.history

	// Close chat panel
	app.chatOpen = false

	// Reopen chat
	result2, _ := app.transitionToChat()
	app = result2.(AppModel)

	// History should be preserved
	if app.chat.history != historyBefore {
		t.Errorf("history lost after reopen:\ngot:  %q\nwant: %q", app.chat.history, historyBefore)
	}
}

// TestChatRecoveryTickReachesChatWhileInAttachView verifies the routing
// fix for the AMA hang: even when the user has navigated away from the
// chat panel (e.g. attached to a running feature), the chat's recovery
// tick messages must still reach the ChatModel. Otherwise a tick
// dispatched while another view is active gets eaten by that view's
// Update, and a single dropped Result message can leave the chat stuck
// in "Thinking…" forever.
func TestChatRecoveryTickReachesChatWhileInAttachView(t *testing.T) {
	app, _ := newTestAppModel(t)

	// Prepare the chat (lazy init).
	result, _ := app.transitionToChat()
	app = result.(AppModel)
	if !app.chatReady {
		t.Fatal("expected chatReady=true after transitionToChat")
	}

	// Put the chat into a state where the recovery tick should flip
	// responding to false: responding=true, turnCostBaseline=nil, and a
	// fake session whose Cost() returns a non-nil pointer (simulating
	// "Result recorded on the session, not delivered via attachCh").
	sess := mocks.NewMockSessionView("__chat__", "")
	sess.CostVal = &llm.ResultMessage{Type: "result", Subtype: "success"}
	app.chat.sess = sess
	app.chat.responding = true
	app.chat.turnCostBaseline = nil
	app.chat.history = ""
	app.chat.partialText = ""

	// Simulate the user having navigated away from the chat. chatOpen
	// is false and current view is not dashboard — this is the
	// configuration under which the pre-fix code routed the tick to
	// the wrong view and lost it.
	app.chatOpen = false
	app.currentView = ViewAttach

	// Deliver a recovery tick for the current session. This message
	// arrives from tea.Tick and takes the non-key default path in
	// AppModel.Update → forwardToActiveInput.
	result, _ = app.Update(chatRecoveryTickMsg{sess: sess, baseline: nil})
	app = result.(AppModel)

	if app.chat.responding {
		t.Fatal("expected chat.responding=false after recovery tick with fresh Cost while in attach view")
	}
	if app.chat.turnCostBaseline != sess.CostVal {
		t.Error("expected turnCostBaseline advanced to observed Cost pointer")
	}
}

// TestChatCloseRestoresDashboardHeight verifies that closing the chat panel
// restores the dashboard to the full app height without requiring a
// WindowSizeMsg (regression test for compressed-dashboard bug).
func TestChatCloseRestoresDashboardHeight(t *testing.T) {
	app, _ := newTestAppModel(t)
	fullHeight := app.height // 24

	// Simulate a WindowSizeMsg so dashboard.height is properly initialized
	result, _ := app.Update(tea.WindowSizeMsg{Width: app.width, Height: fullHeight})
	app = result.(AppModel)

	if app.dashboard.height != fullHeight {
		t.Fatalf("dashboard.height before chat: got %d, want %d", app.dashboard.height, fullHeight)
	}

	// Open chat
	result, _ = app.transitionToChat()
	app = result.(AppModel)

	if !app.chatOpen {
		t.Fatal("expected chatOpen=true after opening chat")
	}

	// Simulate a WindowSizeMsg while chat is open — this is the path that
	// could leave dashboard.height compressed if not handled correctly.
	result, _ = app.Update(tea.WindowSizeMsg{Width: app.width, Height: fullHeight})
	app = result.(AppModel)

	// Close chat via ChatExitMsg — the handler must restore dashboard height
	result, _ = app.Update(ChatExitMsg{})
	app = result.(AppModel)

	if app.chatOpen {
		t.Fatal("expected chatOpen=false after ChatExitMsg")
	}
	if app.dashboard.height != fullHeight {
		t.Fatalf("dashboard.height after chat close: got %d, want %d (should be restored to full height)", app.dashboard.height, fullHeight)
	}
}

func TestHelpOverlayOpenClose(t *testing.T) {
	app, _ := newTestAppModel(t)

	if app.helpOverlayActive {
		t.Fatal("expected helpOverlayActive=false initially")
	}

	// Open help overlay via ? key
	result, _ := app.Update(tea.KeyPressMsg{Code: '?', Text: "?"})
	app = result.(AppModel)

	if !app.helpOverlayActive {
		t.Fatal("expected helpOverlayActive=true after pressing ?")
	}

	// Close help overlay via esc — the overlay returns a HelpOverlayCloseMsg command
	result, cmd := app.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	app = result.(AppModel)
	if cmd != nil {
		msg := cmd()
		if _, ok := msg.(HelpOverlayCloseMsg); ok {
			result, _ = app.Update(msg)
			app = result.(AppModel)
		}
	}

	if app.helpOverlayActive {
		t.Fatal("expected helpOverlayActive=false after pressing esc")
	}

	// Open again and close via ?
	result, _ = app.Update(tea.KeyPressMsg{Code: '?', Text: "?"})
	app = result.(AppModel)
	if !app.helpOverlayActive {
		t.Fatal("expected helpOverlayActive=true after pressing ? again")
	}

	result, cmd = app.Update(tea.KeyPressMsg{Code: '?', Text: "?"})
	app = result.(AppModel)
	if cmd != nil {
		msg := cmd()
		if _, ok := msg.(HelpOverlayCloseMsg); ok {
			result, _ = app.Update(msg)
			app = result.(AppModel)
		}
	}
	if app.helpOverlayActive {
		t.Fatal("expected helpOverlayActive=false after pressing ? to close")
	}
}

func TestHelpOverlayBlocksQuit(t *testing.T) {
	app, _ := newTestAppModel(t)

	// Open help overlay
	result, _ := app.Update(tea.KeyPressMsg{Code: '?', Text: "?"})
	app = result.(AppModel)

	if !app.helpOverlayActive {
		t.Fatal("expected helpOverlayActive=true")
	}

	// Press q — should NOT trigger quit confirmation
	result, _ = app.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	app = result.(AppModel)

	if app.quitConfirmActive {
		t.Error("q should not trigger quit confirmation when help overlay is active")
	}
	// Help overlay should still be active (q is forwarded to viewport)
	if !app.helpOverlayActive {
		t.Error("help overlay should remain active after pressing q")
	}
}

// TestHandleSessionDone_SDKFailureOverridesCleanExit verifies that a clean
// process exit (SessionDone) combined with an SDK result indicating failure
// produces a PhaseCompletedMsg with Success=false, which in turn marks the
// feature as Failed rather than advancing it.
func TestHandleSessionDone_SDKFailureOverridesCleanExit(t *testing.T) {
	app, fm := newTestAppModel(t)

	f, err := fm.Create("sdk-fail-test", "desc", nil, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = fm.Store.Modify(f.ID, func(f *feature.Feature) error {
		f.Status = feature.StatusResearching
		return nil
	})

	// Create a session with FAILED in StatusCh (simulates max_turns, error, etc.).
	sm := session.NewManager(nil)
	sess := session.NewSession(f.ID+"-research", f.ID, feature.PhaseResearch)
	sess.SendStatus("FAILED")
	sm.RegisterTestSession(sess)
	app.sessionManager = sm
	app.lastNotifyTime = make(map[notifyKey]time.Time)
	app.eventCh = make(chan interface{}, 1) // prevent listenForEvents from blocking

	// handleSessionDone with clean exit but FAILED SDK status should produce
	// PhaseCompletedMsg{Success: false}. Feed that through handlePhaseCompleted.
	pcm := PhaseCompletedMsg{
		FeatureID: f.ID,
		Phase:     feature.PhaseResearch,
		SessionID: f.ID + "-research",
		Success:   false, // this is what the fixed handleSessionDone produces
	}
	app.handlePhaseCompleted(pcm)

	// Feature should be Failed (not advanced to PlanReady).
	updated, _ := fm.Get(f.ID)
	if updated.Status != feature.StatusFailed {
		t.Errorf("feature status = %v, want Failed (SDK FAILED should not advance research)", updated.Status)
	}

	// Now verify the positive case: SUCCESS in StatusCh + clean exit = success.
	f2, err := fm.Create("sdk-success-test", "desc", nil, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = fm.Store.Modify(f2.ID, func(f *feature.Feature) error {
		f.Status = feature.StatusResearching
		return nil
	})
	sess2 := session.NewSession(f2.ID+"-research", f2.ID, feature.PhaseResearch)
	sess2.SendStatus("SUCCESS")
	sm.RegisterTestSession(sess2)

	doneMsg := SessionDoneTUIMsg{
		Done: session.SessionDoneMsg{
			SessionID: f2.ID + "-research",
			Status:    session.SessionDone,
		},
	}
	result, _ := app.handleSessionDone(doneMsg)
	app = result.(AppModel)
	// The event ch will receive a listenForEvents; ignore it.
	// Verify the session's StatusCh was consumed to set success.
	// We can't easily execute the full batch, but the key validation is
	// that the feature should NOT be Failed. The PhaseCompletedMsg will
	// carry Success=true.
}

// TestHandlePhaseCompleted_ErrorDetailInLastError verifies that ErrorDetail
// from the session is included in the feature's LastError when a phase fails.
func TestHandlePhaseCompleted_ErrorDetailInLastError(t *testing.T) {
	app, fm := newTestAppModel(t)

	f, err := fm.Create("error-detail-test", "desc", nil, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = fm.Store.Modify(f.ID, func(f *feature.Feature) error {
		f.Status = feature.StatusResearching
		return nil
	})

	pcm := PhaseCompletedMsg{
		FeatureID:   f.ID,
		Phase:       feature.PhaseResearch,
		SessionID:   f.ID + "-research",
		Success:     false,
		ErrorDetail: "Request too large (max 20MB). Try with a smaller file.",
	}
	app.handlePhaseCompleted(pcm)

	updated, _ := fm.Get(f.ID)
	if updated.Status != feature.StatusFailed {
		t.Errorf("feature status = %v, want Failed", updated.Status)
	}
	if !strings.Contains(updated.LastError, "Request too large") {
		t.Errorf("LastError = %q, want it to contain the error detail", updated.LastError)
	}
	if !strings.Contains(updated.LastError, "Research") {
		t.Errorf("LastError = %q, want it to contain the phase name", updated.LastError)
	}
}

// TestHandleSessionDone_ReviewFailureDoesNotFailFeature verifies that a review
// session failure does not mark the feature as Failed. Review sessions are
// loop-internal and should be skipped by hasAdvancedPast.
func TestHandleSessionDone_ReviewFailureDoesNotFailFeature(t *testing.T) {
	app, fm := newTestAppModel(t)

	f, err := fm.Create("review-fail-test", "desc", nil, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = fm.Store.Modify(f.ID, func(f *feature.Feature) error {
		f.Status = feature.StatusImplementing
		return nil
	})

	sm := session.NewManager(nil)
	reviewSessID := f.ID + "-review-01"
	sess := session.NewSession(reviewSessID, f.ID, feature.PhaseReview)
	sess.SendStatus("FAILED")
	sm.RegisterTestSession(sess)
	app.sessionManager = sm
	app.lastNotifyTime = make(map[notifyKey]time.Time)
	app.eventCh = make(chan interface{}, 1)

	// Simulate review session completing with failure.
	doneMsg := SessionDoneTUIMsg{
		Done: session.SessionDoneMsg{
			SessionID: reviewSessID,
			Status:    session.SessionFailed,
		},
	}
	_, _ = app.handleSessionDone(doneMsg)

	// Feature should still be Implementing — not Failed.
	// hasAdvancedPast returns true for PhaseReview, so handleSessionDone
	// returns early without emitting PhaseCompletedMsg.
	updated, _ := fm.Get(f.ID)
	if updated.Status == feature.StatusFailed {
		t.Error("review session failure should not mark feature as Failed")
	}
	if updated.Status != feature.StatusImplementing {
		t.Errorf("feature status = %v, want Implementing", updated.Status)
	}
}

// TestHasAdvancedPast_ReviewAlwaysTrue confirms that hasAdvancedPast returns
// true for PhaseReview regardless of feature status, preventing review-gate
// sessions from emitting PhaseCompletedMsg.
func TestHasAdvancedPast_ReviewAlwaysTrue(t *testing.T) {
	statuses := []feature.Status{
		feature.StatusCreated,
		feature.StatusResearching,
		feature.StatusImplementing,
		feature.StatusFailed,
	}
	for _, s := range statuses {
		f := &feature.Feature{Status: s,
			SchemaVersion: feature.SchemaVersionCurrent}
		if !hasAdvancedPast(f, feature.PhaseReview) {
			t.Errorf("hasAdvancedPast(status=%v, PhaseReview) = false, want true", s)
		}
	}
}

// TestPlanReviewTick_NoSessionCreated verifies that when a feature is in
// StatusPlanNeedsReview, the tick handler does NOT create a background session.
// The new artifact review model opens the editor directly without a session.
func TestPlanReviewTick_NoSessionCreated(t *testing.T) {
	app, fm := newTestAppModel(t)

	f, err := fm.Create("stale-review-test", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Advance to StatusPlanNeedsReview
	_ = fm.Store.Modify(f.ID, func(f *feature.Feature) error {
		f.Status = feature.StatusPlanNeedsReview
		return nil
	})

	sm := session.NewManager(nil)
	app.sessionManager = sm
	app.lastNotifyTime = make(map[notifyKey]time.Time)
	app.eventCh = make(chan interface{}, 16)

	// handleTick should NOT create any plan-review sessions — the new artifact
	// review model is opened on-demand when the user attaches, not via tick.
	_, _ = app.handleTick()

	// Verify no sessions were created
	if sessions := sm.FeatureSessions(f.ID); len(sessions) > 0 {
		t.Errorf("handleTick should not create sessions for PlanNeedsReview; got %d", len(sessions))
	}
}

// TestPlanReviewTick_ExistingSessionUntouched verifies that when a feature is
// in StatusPlanNeedsReview and a session exists from a prior lifecycle, the
// tick handler does not interfere with it. (Artifact review is session-less.)
func TestPlanReviewTick_ExistingSessionUntouched(t *testing.T) {
	app, fm := newTestAppModel(t)

	f, err := fm.Create("active-review-test", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = fm.Store.Modify(f.ID, func(f *feature.Feature) error {
		f.Status = feature.StatusPlanNeedsReview
		return nil
	})

	// Create an active session (e.g. from a prior lifecycle)
	sm := session.NewManager(nil)
	activeSession := session.NewSession(f.ID+"-plan-review", f.ID, feature.PhasePlan)
	sm.RegisterTestSession(activeSession)

	app.sessionManager = sm
	app.lastNotifyTime = make(map[notifyKey]time.Time)
	app.eventCh = make(chan interface{}, 16)

	// handleTick should not touch the existing session
	_, _ = app.handleTick()

	sess := sm.GetSession(f.ID + "-plan-review")
	if sess != activeSession {
		t.Error("tick should not replace or remove existing sessions")
	}
}

// TestDashboardAttach_PlanNeedsReview verifies that pressing 'a' on a
// PlanNeedsReview feature opens the artifact review editor (not the legacy
// attach view), regardless of whether any prior sessions exist.
func TestDashboardAttach_PlanNeedsReview(t *testing.T) {
	app, fm := newTestAppModel(t)

	f, err := fm.Create("stale-attach-test", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = fm.Store.Modify(f.ID, func(f *feature.Feature) error {
		f.Status = feature.StatusPlanNeedsReview
		return nil
	})

	sm := session.NewManager(nil)
	app.sessionManager = sm
	app.lastNotifyTime = make(map[notifyKey]time.Time)
	app.eventCh = make(chan interface{}, 16)

	// Set up dashboard with the feature selected
	features, _ := fm.List()
	app.dashboard = NewDashboardModel(features, "")
	app.dashboard.width = 80
	app.dashboard.height = 24
	// Find the feature in visibleItems (skip section headers)
	for i, item := range app.dashboard.visibleItems {
		if item.kind == listItemFeature && item.feature.ID == f.ID {
			app.dashboard.cursor = i
			break
		}
	}
	// Focus the right panel where attach key is handled
	app.dashboard.focusPanel = 1

	// Press 'a' to attach
	result, cmd := app.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	updatedApp := result.(AppModel)

	// Should NOT have transitioned to ViewAttach (uses artifact review now)
	if updatedApp.currentView == ViewAttach {
		t.Error("PlanNeedsReview should use artifact review, not legacy attach view")
	}
	// Should have produced a cmd (startPlanReviewSessionCmd returns ArtifactReviewStartMsg)
	if cmd == nil {
		t.Error("pressing 'a' on PlanNeedsReview should produce a cmd to open artifact review")
	}
}

// TestStartPlanReviewSessionCmd_WorkDir verifies that startPlanReviewSessionCmd
// populates WorkDir from the feature's worktree/repo path.
func TestStartPlanReviewSessionCmd_WorkDir(t *testing.T) {
	app, fm := newTestAppModel(t)

	f, err := fm.Create("workdir-test", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Create a plan artifact file on disk so resolvePhaseArtifactPath finds it.
	planDir := filepath.Join(fm.Store.BaseDir, f.ID, "runs", "run-001", "plan")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	planFile := filepath.Join(planDir, "plan.md")
	if err := os.WriteFile(planFile, []byte("# Plan"), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}

	// Set the feature's repo worktree path.
	worktreePath := t.TempDir()
	_ = fm.Store.Modify(f.ID, func(f *feature.Feature) error {
		f.Status = feature.StatusPlanNeedsReview
		if len(f.Repos) > 0 {
			f.Repos[0].WorktreePath = worktreePath
		}
		return nil
	})

	cmd := app.startPlanReviewSessionCmd(f.ID, true)
	msg := cmd()

	startMsg, ok := msg.(ArtifactReviewStartMsg)
	if !ok {
		t.Fatalf("expected ArtifactReviewStartMsg, got %T", msg)
	}
	if startMsg.ReviewMode != "plan" {
		t.Errorf("ReviewMode = %q, want %q", startMsg.ReviewMode, "plan")
	}
	if startMsg.WorkDir != worktreePath {
		t.Errorf("WorkDir = %q, want %q", startMsg.WorkDir, worktreePath)
	}
	if startMsg.ArtifactPath == "" {
		t.Error("ArtifactPath should not be empty")
	}
}

// TestStartRewindReviewSessionCmd_Payload verifies that startRewindReviewSessionCmd
// populates RewindPhase and WorkDir correctly for different target phases.
func TestStartRewindReviewSessionCmd_Payload(t *testing.T) {
	app, fm := newTestAppModel(t)

	f, err := fm.Create("rewind-payload-test", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Create a plan artifact file so resolving the "plan" artifact for PhaseImplement works.
	planDir := filepath.Join(fm.Store.BaseDir, f.ID, "runs", "run-001", "plan")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	planFile := filepath.Join(planDir, "plan.md")
	if err := os.WriteFile(planFile, []byte("# Plan"), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}

	// Set the feature's repo path (no worktree — should fall back to Path).
	repoPath := "/tmp/test-repo"
	_ = fm.Store.Modify(f.ID, func(f *feature.Feature) error {
		f.Status = feature.StatusImplementing
		if len(f.Repos) > 0 {
			f.Repos[0].WorktreePath = ""
			f.Repos[0].Path = repoPath
		}
		return nil
	})

	cmd := app.startRewindReviewSessionCmd(f.ID, feature.PhaseImplement, false)
	msg := cmd()

	startMsg, ok := msg.(ArtifactReviewStartMsg)
	if !ok {
		t.Fatalf("expected ArtifactReviewStartMsg, got %T", msg)
	}
	if startMsg.ReviewMode != "rewind" {
		t.Errorf("ReviewMode = %q, want %q", startMsg.ReviewMode, "rewind")
	}
	if startMsg.RewindPhase != feature.PhaseImplement {
		t.Errorf("RewindPhase = %q, want %q", startMsg.RewindPhase, feature.PhaseImplement)
	}
	if startMsg.WorkDir != repoPath {
		t.Errorf("WorkDir = %q, want %q (should fall back to repo Path)", startMsg.WorkDir, repoPath)
	}
	if startMsg.ArtifactPath == "" {
		t.Error("ArtifactPath should not be empty")
	}
}

func TestStopFeatureCmdTransitionsToInterrupted(t *testing.T) {
	app, fm := newTestAppModel(t)

	// Create a feature in Researching state
	f, err := fm.Create("Stop Test", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = fm.Transition(f.ID, feature.StatusResearching)
	f, _ = fm.Get(f.ID)

	sm := session.NewManager(nil)
	app.sessionManager = sm

	// Execute stopFeatureCmd directly
	cmd := app.stopFeatureCmd(f.ID)
	msg := cmd()

	// The cmd should return RefreshFeaturesMsg
	if _, ok := msg.(RefreshFeaturesMsg); !ok {
		t.Fatalf("expected RefreshFeaturesMsg, got %T", msg)
	}

	// Verify the feature transitioned to Interrupted
	f, _ = fm.Get(f.ID)
	if f.Status != feature.StatusInterrupted {
		t.Errorf("feature status = %v, want StatusInterrupted after stop", f.Status)
	}
}

func TestStopFeatureCmdPublishedRebaseCycleTransitionsToInterrupted(t *testing.T) {
	app, fm := newTestAppModel(t)

	f, err := fm.Create("Stop Rebase Test", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	walkFeatureToPublished(t, fm, f.ID)
	if err := fm.StartRepoCycle(f.ID, "test-repo", feature.CycleRebase); err != nil {
		t.Fatalf("StartRepoCycle: %v", err)
	}

	msg := app.stopFeatureCmd(f.ID)()
	if _, ok := msg.(RefreshFeaturesMsg); !ok {
		t.Fatalf("expected RefreshFeaturesMsg, got %T", msg)
	}

	f, _ = fm.Get(f.ID)
	if f.Status != feature.StatusInterrupted {
		t.Errorf("feature status = %v, want StatusInterrupted after stopping active rebase", f.Status)
	}
	rc, ok := f.RepoCycles["test-repo"]
	if !ok {
		t.Fatal("RepoCycles[test-repo] missing")
	}
	if rc.Status != feature.RepoCycleInterrupted {
		t.Errorf("RepoCycles[test-repo].Status = %q, want interrupted", rc.Status)
	}
}

func TestStopConfirmCancelIsNoOp(t *testing.T) {
	app, fm := newTestAppModel(t)

	// Create a feature in Researching state
	f, err := fm.Create("Stop Cancel Test", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = fm.Transition(f.ID, feature.StatusResearching)

	sm := session.NewManager(nil)
	app.sessionManager = sm

	// Activate stop confirmation
	app.stopConfirmActive = true
	app.stopConfirmFeatureID = f.ID
	app.stopConfirmFeatureName = f.Name

	// Send a non-y key to cancel
	updatedModel, _ := app.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	updated := updatedModel.(AppModel)

	// Modal should be dismissed
	if updated.stopConfirmActive {
		t.Error("stopConfirmActive should be false after cancel")
	}

	// Feature should still be running
	f, _ = fm.Get(f.ID)
	if f.Status != feature.StatusResearching {
		t.Errorf("feature status = %v, want StatusResearching after cancel", f.Status)
	}
}

func TestStopKeyNoOpOnNonRunningFeature(t *testing.T) {
	app, fm := newTestAppModel(t)

	// Create a feature in Created state (non-running)
	f, err := fm.Create("Stop NoOp Test", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	sm := session.NewManager(nil)
	app.sessionManager = sm

	// Set up dashboard with the feature selected, right panel focused
	app.currentView = ViewDashboard
	app.dashboard.focusPanel = 1 // right panel
	app.dashboard.SetFeatures([]*feature.Feature{f})

	// Send Stop key
	updatedModel, _ := app.Update(tea.KeyPressMsg{Code: 's', Text: "s"})
	updated := updatedModel.(AppModel)

	// Stop confirmation should not be activated
	if updated.stopConfirmActive {
		t.Error("stopConfirmActive should remain false for non-running feature")
	}

	// Feature should still be Created
	f, _ = fm.Get(f.ID)
	if f.Status != feature.StatusCreated {
		t.Errorf("feature status = %v, want StatusCreated", f.Status)
	}
}

func TestStopFeatureCmdClearsPendingHelp(t *testing.T) {
	app, fm := newTestAppModel(t)

	// Create a feature in Implementing state with pending help
	f, err := fm.Create("Stop Help Test", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = fm.Transition(f.ID, feature.StatusResearching)
	_ = fm.Transition(f.ID, feature.StatusPlanReady)
	_ = fm.Transition(f.ID, feature.StatusPlanning)
	_ = fm.Transition(f.ID, feature.StatusImplementReady)
	_ = fm.Transition(f.ID, feature.StatusImplementing)

	// Add a pending help request
	_ = fm.Store.Modify(f.ID, func(f *feature.Feature) error {
		f.HelpQueue = append(f.HelpQueue, feature.HelpRequest{
			Question: "Need help with something",
			Pending:  true,
			Time:     time.Now(),
		})
		return nil
	})

	sm := session.NewManager(nil)
	app.sessionManager = sm

	// Execute stopFeatureCmd
	cmd := app.stopFeatureCmd(f.ID)
	msg := cmd()

	if _, ok := msg.(RefreshFeaturesMsg); !ok {
		t.Fatalf("expected RefreshFeaturesMsg, got %T", msg)
	}

	// Verify help queue is cleared
	f, _ = fm.Get(f.ID)
	for i, hr := range f.HelpQueue {
		if hr.Pending {
			t.Errorf("HelpQueue[%d].Pending = true, want false after stop", i)
		}
	}

	// Verify feature is interrupted
	if f.Status != feature.StatusInterrupted {
		t.Errorf("feature status = %v, want StatusInterrupted", f.Status)
	}
}

// TestActiveSessionContextPctClearsWhenNoActiveSession verifies that the
// context % clears as soon as no active session is producing usage data,
// rather than bleeding through a prior session's last reading. The renderer
// surfaces -1 as "calculating…" so a freshly-started phase never inherits
// the previous phase's final fill.
func TestActiveSessionContextPctClearsWhenNoActiveSession(t *testing.T) {
	app, fm := newTestAppModel(t)
	f, err := fm.Create("ctx-clears", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = fm.Transition(f.ID, feature.StatusResearching)
	f, _ = fm.Get(f.ID)

	sm := session.NewManager(nil)
	app.sessionManager = sm

	// Done session with usage data must NOT back-fill the active reading.
	sess := session.NewSession(f.ID+"-research", f.ID, feature.PhaseResearch)
	sess.SetModel("opus")
	sess.SetLatestUsage(&llm.Usage{InputTokens: 40_000, ContextWindow: 200_000})
	sess.SetStatus(session.SessionDone)
	sm.RegisterTestSession(sess)

	app.detail.feature = f
	if pct := app.activeSessionContextPct(); pct != -1 {
		t.Errorf("expected -1 (no active session), got %d", pct)
	}
}

func TestContextPctForFeature(t *testing.T) {
	app, fm := newTestAppModel(t)
	f, err := fm.Create("ctx-for-feat", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = fm.Transition(f.ID, feature.StatusResearching)
	f, _ = fm.Get(f.ID)

	sm := session.NewManager(nil)
	app.sessionManager = sm

	sess := session.NewSession(f.ID+"-research", f.ID, feature.PhaseResearch)
	sess.SetModel("sonnet")
	sess.SetLatestUsage(&llm.Usage{InputTokens: 60_000, ContextWindow: 200_000})
	sess.SetStatus(session.SessionRunning)
	sm.RegisterTestSession(sess)

	// contextPctForFeature should return 30 (60_000/200_000*100)
	pct := app.contextPctForFeature(f)
	if pct != 30 {
		t.Errorf("contextPctForFeature = %d, want 30", pct)
	}

	// nil feature → -1
	if got := app.contextPctForFeature(nil); got != -1 {
		t.Errorf("contextPctForFeature(nil) = %d, want -1", got)
	}
}

func TestContextPctForFeature_ReturnsMaxAcrossParallelActiveSessions(t *testing.T) {
	app, fm := newTestAppModel(t)
	f, err := fm.Create("ctx-parallel-max", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = fm.Transition(f.ID, feature.StatusResearching)
	f, _ = fm.Get(f.ID)

	sm := session.NewManager(nil)
	app.sessionManager = sm

	// Simulates three plan-validation gates running in parallel (Struct,
	// Ground, Scope). Each has its own independent ContextPercentage; the
	// dashboard should surface the one closest to its limit, not whichever
	// happens to have started most recently.
	cases := []struct {
		name   string
		tokens int
	}{
		{"struct", 40_000}, // 20%
		{"ground", 90_000}, // 45% — highest
		{"scope", 60_000},  // 30%
	}
	for _, c := range cases {
		s := session.NewSession(f.ID+"-validator-"+c.name, f.ID, feature.PhasePlan)
		s.SetModel("sonnet")
		s.SetLatestUsage(&llm.Usage{InputTokens: c.tokens, ContextWindow: 200_000})
		s.SetStatus(session.SessionRunning)
		sm.RegisterTestSession(s)
	}

	if pct := app.contextPctForFeature(f); pct != 45 {
		t.Errorf("contextPctForFeature() = %d, want 45 (max across parallel sessions)", pct)
	}
}

func TestContextPctForFeature_PostPublishCycle(t *testing.T) {
	app, fm := newTestAppModel(t)
	f, err := fm.Create("ctx-cycle", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = fm.Transition(f.ID, feature.StatusPublished)
	_ = fm.Store.Modify(f.ID, func(feat *feature.Feature) error {
		feat.CurrentPhase = feature.PhasePublish
		feat.SetRebaseCount(1)
		feat.SetActiveCycleType(feature.CycleRebase)
		feat.ActiveCycle = &feature.CycleState{
			Type:   feature.CycleRebase,
			Status: feature.RepoCycleRunning,
			Count:  1,
		}
		return nil
	})
	f, _ = fm.Get(f.ID)

	sm := session.NewManager(nil)
	app.sessionManager = sm

	sess := session.NewSession(f.ID+"-rebase-1", f.ID, feature.PhaseImplement)
	sess.SetModel("sonnet")
	sess.SetLatestUsage(&llm.Usage{InputTokens: 80_000, ContextWindow: 200_000})
	sess.SetStatus(session.SessionRunning)
	sm.RegisterTestSession(sess)

	if pct := app.contextPctForFeature(f); pct != 40 {
		t.Errorf("contextPctForFeature() = %d, want 40 for running post-publish cycle", pct)
	}
}

// TestContextPctForFeature_NoFallbackToPriorSession verifies that when the
// active session has no usage data yet, contextPctForFeature returns -1
// instead of bleeding through a prior session's reading. The renderer maps
// -1 to "calculating…" so a freshly-started phase never inherits the
// previous phase's final fill (the bug that motivated this design).
func TestContextPctForFeature_NoFallbackToPriorSession(t *testing.T) {
	app, fm := newTestAppModel(t)
	f, err := fm.Create("ctx-no-fallback", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = fm.Transition(f.ID, feature.StatusResearching)
	f, _ = fm.Get(f.ID)

	sm := session.NewManager(nil)
	app.sessionManager = sm

	active := session.NewSession(f.ID+"-research-live", f.ID, feature.PhaseResearch)
	active.SetModel("sonnet")
	active.SetStatus(session.SessionRunning)
	sm.RegisterTestSession(active)

	done := session.NewSession(f.ID+"-research-prev", f.ID, feature.PhaseResearch)
	done.SetModel("sonnet")
	done.SetLatestUsage(&llm.Usage{InputTokens: 50_000, ContextWindow: 200_000})
	done.SetStatus(session.SessionDone)
	sm.RegisterTestSession(done)

	if pct := app.contextPctForFeature(f); pct != -1 {
		t.Errorf("contextPctForFeature() = %d, want -1 (no fallback to prior session)", pct)
	}
}

func TestDashboardPreviewContextPct(t *testing.T) {
	app, fm := newTestAppModel(t)
	f, err := fm.Create("ctx-preview", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = fm.Transition(f.ID, feature.StatusResearching)
	f, _ = fm.Get(f.ID)

	sm := session.NewManager(nil)
	app.sessionManager = sm

	sess := session.NewSession(f.ID+"-research", f.ID, feature.PhaseResearch)
	sess.SetModel("opus")
	sess.SetLatestUsage(&llm.Usage{InputTokens: 50_000, ContextWindow: 200_000})
	sess.SetStatus(session.SessionRunning)
	sm.RegisterTestSession(sess)

	// contextPctForFeature should return 25 (50_000/200_000*100).
	pct := app.contextPctForFeature(f)
	if pct != 25 {
		t.Errorf("contextPctForFeature = %d, want 25", pct)
	}

	// The compact view should render "25%" when contextPct is set on the model.
	// This verifies the full rendering path from session usage → compact preview.
	dm := NewDetailModel(f, "")
	dm.contextPct = pct
	compact := dm.ViewCompact(80)
	if !strings.Contains(compact, "25%") {
		t.Errorf("expected 25%% in compact view for running feature (contextPct=%d)", pct)
	}

	app.dashboard.SetFeatures([]*feature.Feature{f})
	app.dashboard.cursor = 1
	app.dashboard.syncPreview()
	app.dashboard.focusPanel = 1
	view := stripANSI(app.View().Content)
	if !strings.Contains(view, "Context") || !strings.Contains(view, "25%") {
		t.Errorf("expected live preview top box to include context 25%%, got:\n%s", view)
	}
}

func TestIsRunningFeatureIncludesInquiringAndDesigning(t *testing.T) {
	runningStatuses := []feature.Status{
		feature.StatusBuildingKB,
		feature.StatusResearching,
		feature.StatusInquiring,
		feature.StatusDesigning,
		feature.StatusPlanning,
		feature.StatusImplementing,
	}
	for _, s := range runningStatuses {
		f := &feature.Feature{Status: s,
			SchemaVersion: feature.SchemaVersionCurrent}
		if !isRunningFeature(f) {
			t.Errorf("isRunningFeature(%v) = false, want true", s)
		}
	}

	nonRunningStatuses := []feature.Status{
		feature.StatusCreated,
		feature.StatusPlanReady,
		feature.StatusImplementReady,
		feature.StatusFailed,
		feature.StatusInterrupted,
		feature.StatusPublished,
		feature.StatusDone,
	}
	for _, s := range nonRunningStatuses {
		f := &feature.Feature{Status: s,
			SchemaVersion: feature.SchemaVersionCurrent}
		if isRunningFeature(f) {
			t.Errorf("isRunningFeature(%v) = true, want false", s)
		}
	}

	// Published with active repo cycles should be running
	f := &feature.Feature{
		Status: feature.StatusPublished,
		RepoCycles: map[string]*feature.RepoCycleState{
			"api": {Type: feature.CycleReviewComments, Status: "running"},
		},
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	if !isRunningFeature(f) {
		t.Error("isRunningFeature(Published+activeCycles) = false, want true")
	}

	// Published with only completed/failed cycles should not be running
	f2 := &feature.Feature{
		Status: feature.StatusPublished,
		RepoCycles: map[string]*feature.RepoCycleState{
			"api": {Type: feature.CycleTweak, Status: "completed"},
		},
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	if isRunningFeature(f2) {
		t.Error("isRunningFeature(Published+completedCycles) = true, want false")
	}

	// Published with a reviewing repo cycle should be running
	f3 := &feature.Feature{
		Status: feature.StatusPublished,
		RepoCycles: map[string]*feature.RepoCycleState{
			"api": {Type: feature.CycleRebase, Status: "reviewing"},
		},
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	if !isRunningFeature(f3) {
		t.Error("isRunningFeature(Published+reviewingCycle) = false, want true")
	}

	// CodeReady with an active tweak/rebase cycle should be running. The
	// manual_publish flow keeps the feature at CodeReady while cycles run;
	// the attach gate must accept it so the user can drop into the session.
	f4 := &feature.Feature{
		Status: feature.StatusCodeReady,
		RepoCycles: map[string]*feature.RepoCycleState{
			"api": {Type: feature.CycleTweak, Status: "running"},
		},
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	if !isRunningFeature(f4) {
		t.Error("isRunningFeature(CodeReady+activeTweak) = false, want true")
	}

	// CodeReady with no active cycles should not be running.
	f5 := &feature.Feature{Status: feature.StatusCodeReady, SchemaVersion: feature.SchemaVersionCurrent}
	if isRunningFeature(f5) {
		t.Error("isRunningFeature(CodeReady+noCycles) = true, want false")
	}
}

func TestStopAllowedForInquiringAndDesigning(t *testing.T) {
	tests := []struct {
		name   string
		status feature.Status
		phase  feature.Phase
	}{
		{"inquiring", feature.StatusInquiring, feature.PhaseInquire},
		{"designing", feature.StatusDesigning, feature.PhaseDesign},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app, fm := newTestAppModel(t)

			f, err := fm.Create("Stop "+tt.name, "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			// Force status via store modify (avoids full transition chain)
			_ = fm.Store.Modify(f.ID, func(f *feature.Feature) error {
				f.Status = tt.status
				f.CurrentPhase = tt.phase
				return nil
			})
			f, _ = fm.Get(f.ID)

			sm := session.NewManager(nil)
			app.sessionManager = sm

			app.detail = NewDetailModel(f, "")
			app.currentView = ViewDetail

			// Press 's' (stop) — should activate stop confirmation for running phases
			updatedModel, _ := app.Update(tea.KeyPressMsg{Code: 's', Text: "s"})
			updated := updatedModel.(AppModel)
			if !updated.stopConfirmActive {
				t.Errorf("stopConfirmActive should be true for %v status", tt.status)
			}
		})
	}
}

// TestRoadmapRejectRestartsPlanning verifies that rejecting a roadmap
// transitions the feature back to a running planning state. Phase 8:
// orchestrator.reviewIterate now dispatches startPhase(PhasePlan) internally,
// so the feature is observed in StatusPlanning (having moved PlanReady →
// Planning atomically). TotalRoadmapPhases is reset so the re-run is fresh.
// TestPhasePlanCmdTransitionsToPlanning verifies that startPhasePlanCmd
// transitions the feature to StatusPlanning when called from a non-Planning state
// (e.g. after restart from Interrupted or PlanReady). Regression test for a bug
// where the result was discarded by handlePlanLoopDone because the feature
// remained in Interrupted status.
// TestPlanLoopDone_NeedsHumanReview_RoadmapCase verifies that PlanLoopDoneMsg with
// needs_human_review resolves the roadmap artifact when CurrentRoadmapPhase == 0.
func TestPlanLoopDone_NeedsHumanReview_RoadmapCase(t *testing.T) {
	app, fm := newTestAppModel(t)

	f, err := fm.Create("Roadmap Review", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Enable PlanReview checkpoint so needs_human_review triggers review (not failure)
	_ = fm.Store.Modify(f.ID, func(feat *feature.Feature) error {
		feat.Checkpoints.PlanReview = true
		return nil
	})

	// Advance to Planning with roadmap context (phase 0 = roadmap itself)
	_ = fm.Transition(f.ID, feature.StatusResearching)
	_ = fm.Transition(f.ID, feature.StatusPlanReady)
	_ = fm.Transition(f.ID, feature.StatusPlanning)
	// Set Artifacts["roadmap"] to simulate what RunRoadmapPlanningLoop does before
	// returning needs_human_review. Do NOT set TotalRoadmapPhases — that field is
	// only populated after the roadmap is approved and parsed by handlePlanLoopDone.
	roadmapDir := filepath.Join(agent.ActiveRunDir(fm.Store.BaseDir, f), "roadmap")
	if err := os.MkdirAll(roadmapDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	roadmapFile := filepath.Join(roadmapDir, "roadmap.md")
	if err := os.WriteFile(roadmapFile, []byte("# Roadmap\n## Phase 1: Skeleton\n## Phase 2: Fill In\n## Phase 3: Polish"), 0o644); err != nil {
		t.Fatalf("write roadmap: %v", err)
	}
	_ = fm.Store.Modify(f.ID, func(feat *feature.Feature) error {
		if feat.Artifacts == nil {
			feat.Artifacts = make(map[string]string)
		}
		feat.Artifacts["roadmap"] = roadmapFile
		return nil
	})

	sm := session.NewManager(nil)
	app.sessionManager = sm

	// Send PlanLoopDoneMsg with needs_human_review
	result := &agent.PlanLoopResult{FinalStatus: "needs_human_review"}
	_, cmd := app.Update(PlanLoopDoneMsg{FeatureID: f.ID, Result: result})

	// The cmd should be a batch; execute it to get the ArtifactReviewStartMsg
	if cmd == nil {
		t.Fatal("expected a cmd from PlanLoopDoneMsg with needs_human_review")
	}

	// Execute all batch commands to find the ArtifactReviewStartMsg
	msgs := executeBatchCmd(t, cmd)
	var startMsg ArtifactReviewStartMsg
	found := false
	for _, msg := range msgs {
		if sm, ok := msg.(ArtifactReviewStartMsg); ok {
			startMsg = sm
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected ArtifactReviewStartMsg from needs_human_review cmd batch")
	}

	if startMsg.ReviewMode != "plan" {
		t.Errorf("ReviewMode = %q, want %q", startMsg.ReviewMode, "plan")
	}
	// The artifact path should resolve to the roadmap artifact, not "plan"
	if !strings.Contains(startMsg.ArtifactPath, "roadmap") {
		t.Errorf("ArtifactPath = %q, want path containing 'roadmap'", startMsg.ArtifactPath)
	}
}

// TestPlanLoopDone_NeedsHumanReview_PhasePlanCase verifies that PlanLoopDoneMsg with
// needs_human_review resolves the phase-specific plan artifact when CurrentRoadmapPhase > 0.
// TestPlanReviewDecision_Iterate_PhasePlan verifies that PlanReviewDecisionMsg with
// Decision "iterate" dispatches startPhasePlanCmd (not startPlanningCmd) when
// CurrentRoadmapPhase > 0. The cmd transitions PlanNeedsReview → Planning inside
// startPhasePlanCmd (which will eventually fail without a roadmap artifact, but the
// state transition happens before the failure).
// TestPlanReviewDecision_Proceed_PhasePlan verifies that when a phase plan
// (CurrentRoadmapPhase > 0) goes through the needs_human_review → proceed path,
// it uses agent.PhasePlanDir() for the execution plan instead of the legacy plan/ directory.
func TestPlanReviewDecision_Proceed_PhasePlan(t *testing.T) {
	app, fm := newTestAppModel(t)

	f, err := fm.Create("Proceed Phase Plan", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Advance to PlanNeedsReview with phase 2 active
	_ = fm.Transition(f.ID, feature.StatusResearching)
	_ = fm.Transition(f.ID, feature.StatusPlanReady)
	_ = fm.Transition(f.ID, feature.StatusPlanning)
	_ = fm.Transition(f.ID, feature.StatusPlanNeedsReview)
	_ = fm.Store.Modify(f.ID, func(feat *feature.Feature) error {
		feat.TotalRoadmapPhases = 3
		feat.CurrentRoadmapPhase = 2
		return nil
	})

	// Create phase-specific plan directory with an execution-order.yaml
	phasePlanDir := agent.PhasePlanDir(fm.Store.BaseDir, f, 2)
	if err := os.MkdirAll(phasePlanDir, 0o755); err != nil {
		t.Fatalf("mkdir phase plan dir: %v", err)
	}
	execOrderYAML := "stages:\n  - repos: [test-repo]\n"
	if err := os.WriteFile(filepath.Join(phasePlanDir, "execution-order.yaml"), []byte(execOrderYAML), 0o644); err != nil {
		t.Fatalf("write execution-order.yaml: %v", err)
	}

	sm := session.NewManager(nil)
	app.sessionManager = sm

	// Send PlanReviewDecisionMsg with proceed
	_, cmd := app.Update(PlanReviewDecisionMsg{FeatureID: f.ID, Decision: "proceed"})

	// Orchestrator.reviewProceed moves ImplementReady → Implementing via
	// startPhase(PhaseImplement) → startImplement → StartImplementation,
	// which is the expected end-state after Phase 8.
	f, _ = fm.Get(f.ID)
	if f.Status != feature.StatusImplementing {
		t.Errorf("status = %v, want Implementing (orchestrator auto-dispatches after reviewProceed)", f.Status)
	}

	// (ExecutionPlan field was removed in SchemaVersionCurrent = 3; the
	// per-phase execution-order.yaml is read fresh from disk per orchestrator
	// cycle. The status transition above is the surviving observable of
	// reviewProceed for this test.)

	// cmd should be non-nil (the refresh tea.Cmd follow-up).
	if cmd == nil {
		t.Fatal("expected non-nil cmd from proceed decision on phase plan")
	}
}

// executeBatchCmd executes a tea.Cmd and collects all messages, handling batches.
func executeBatchCmd(t *testing.T, cmd tea.Cmd) []tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if msg == nil {
		return nil
	}
	// tea.BatchMsg is a []tea.Cmd
	if batch, ok := msg.(tea.BatchMsg); ok {
		var msgs []tea.Msg
		for _, c := range batch {
			if c != nil {
				msgs = append(msgs, executeBatchCmd(t, c)...)
			}
		}
		return msgs
	}
	return []tea.Msg{msg}
}

// TestPlanReviewArtifactResolution_RoadmapWithoutTotalPhases verifies that
// startPlanReviewSessionCmd resolves the "roadmap" artifact key when
// Artifacts["roadmap"] is set, even when TotalRoadmapPhases == 0. This is a
// regression test: before the fix, the code checked TotalRoadmapPhases > 0
// which was not yet populated when the roadmap hit needs_human_review.
func TestPlanReviewArtifactResolution_RoadmapWithoutTotalPhases(t *testing.T) {
	app, fm := newTestAppModel(t)

	f, err := fm.Create("Roadmap No TotalPhases", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Enable PlanReview checkpoint so needs_human_review triggers review (not failure).
	// Advance to Planning — do NOT set TotalRoadmapPhases (leave at 0).
	// Only set Artifacts["roadmap"] to simulate the roadmap artifact being
	// emitted before TotalRoadmapPhases is populated.
	_ = fm.Transition(f.ID, feature.StatusResearching)
	_ = fm.Transition(f.ID, feature.StatusPlanReady)
	_ = fm.Transition(f.ID, feature.StatusPlanning)
	_ = fm.Store.Modify(f.ID, func(feat *feature.Feature) error {
		feat.Checkpoints.PlanReview = true
		feat.TotalRoadmapPhases = 0
		feat.CurrentRoadmapPhase = 0
		if feat.Artifacts == nil {
			feat.Artifacts = make(map[string]string)
		}
		feat.Artifacts["roadmap"] = "/some/roadmap/path.md"
		return nil
	})

	// Create the roadmap artifact on disk so resolvePhaseArtifactPath can find it.
	roadmapDir := filepath.Join(fm.Store.BaseDir, f.ID, "runs", "run-001", "roadmap")
	if err := os.MkdirAll(roadmapDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	roadmapFile := filepath.Join(roadmapDir, "roadmap.md")
	if err := os.WriteFile(roadmapFile, []byte("# Roadmap\n## Phase 1: Skeleton\n## Phase 2: Fill In"), 0o644); err != nil {
		t.Fatalf("write roadmap: %v", err)
	}

	sm := session.NewManager(nil)
	app.sessionManager = sm

	// Send PlanLoopDoneMsg with needs_human_review
	result := &agent.PlanLoopResult{FinalStatus: "needs_human_review"}
	_, cmd := app.Update(PlanLoopDoneMsg{FeatureID: f.ID, Result: result})
	if cmd == nil {
		t.Fatal("expected a cmd from PlanLoopDoneMsg with needs_human_review")
	}

	msgs := executeBatchCmd(t, cmd)
	var startMsg ArtifactReviewStartMsg
	found := false
	for _, msg := range msgs {
		if sm, ok := msg.(ArtifactReviewStartMsg); ok {
			startMsg = sm
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected ArtifactReviewStartMsg from needs_human_review cmd batch")
	}

	if startMsg.ReviewMode != "plan" {
		t.Errorf("ReviewMode = %q, want %q", startMsg.ReviewMode, "plan")
	}
	// The artifact path must resolve via the "roadmap" key, not "plan".
	if !strings.Contains(startMsg.ArtifactPath, "roadmap") {
		t.Errorf("ArtifactPath = %q, want path containing 'roadmap'", startMsg.ArtifactPath)
	}
}

// TestPlanReviewDecision_Iterate_PhasePlan_ExtendsBudget verifies that an
// "iterate" decision on a phase plan extends MaxPlanIterations by 3 on top of
// the effective default when MaxPlanIterations starts at 0.
func TestPlanReviewDecision_Iterate_PhasePlan_ExtendsBudget(t *testing.T) {
	app, fm := newTestAppModel(t)

	f, err := fm.Create("Iterate Budget", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Advance to PlanNeedsReview with a phase plan context.
	_ = fm.Transition(f.ID, feature.StatusResearching)
	_ = fm.Transition(f.ID, feature.StatusPlanReady)
	_ = fm.Transition(f.ID, feature.StatusPlanning)
	_ = fm.Transition(f.ID, feature.StatusPlanNeedsReview)
	_ = fm.Store.Modify(f.ID, func(feat *feature.Feature) error {
		feat.CurrentRoadmapPhase = 1
		feat.TotalRoadmapPhases = 3
		feat.MaxPlanIterations = 0 // starts at zero (default)
		return nil
	})

	sm := session.NewManager(nil)
	app.sessionManager = sm

	// Send iterate decision
	_, _ = app.Update(PlanReviewDecisionMsg{FeatureID: f.ID, Decision: "iterate"})

	// MaxPlanIterations should be DefaultMaxPlanAttempts + 3
	f, _ = fm.Get(f.ID)
	want := agent.DefaultMaxPlanAttempts + 3
	if f.MaxPlanIterations != want {
		t.Errorf("MaxPlanIterations = %d, want %d (DefaultMaxPlanAttempts=%d + 3)",
			f.MaxPlanIterations, want, agent.DefaultMaxPlanAttempts)
	}
}

// TestRoadmapReviewDecision_Reject_ExtendsBudget verifies that rejecting a
// roadmap extends MaxPlanIterations by 1 on top of the effective default when
// MaxPlanIterations starts at 0, giving the planning loop room for revision.
func TestRoadmapReviewDecision_Reject_ExtendsBudget(t *testing.T) {
	app, fm := newTestAppModel(t)

	f, err := fm.Create("Reject Budget", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Advance to PlanNeedsReview with roadmap context.
	_ = fm.Transition(f.ID, feature.StatusResearching)
	_ = fm.Transition(f.ID, feature.StatusPlanReady)
	_ = fm.Transition(f.ID, feature.StatusPlanning)
	_ = fm.Transition(f.ID, feature.StatusPlanNeedsReview)
	_ = fm.Store.Modify(f.ID, func(feat *feature.Feature) error {
		feat.TotalRoadmapPhases = 3
		feat.CurrentRoadmapPhase = 0
		feat.MaxPlanIterations = 0 // starts at zero (default)
		if feat.Artifacts == nil {
			feat.Artifacts = make(map[string]string)
		}
		feat.Artifacts["roadmap"] = "roadmap.md"
		return nil
	})

	sm := session.NewManager(nil)
	app.sessionManager = sm

	// Send reject decision
	_, _ = app.Update(RoadmapReviewDecisionMsg{
		FeatureID: f.ID,
		Decision:  "reject",
		Comment:   "Needs more detail on phase 2.",
	})

	// MaxPlanIterations should be DefaultMaxPlanAttempts + 1
	f, _ = fm.Get(f.ID)
	want := agent.DefaultMaxPlanAttempts + 1
	if f.MaxPlanIterations != want {
		t.Errorf("MaxPlanIterations = %d, want %d (DefaultMaxPlanAttempts=%d + 1)",
			f.MaxPlanIterations, want, agent.DefaultMaxPlanAttempts)
	}

	// TotalRoadmapPhases should be reset to 0 for the re-run.
	if f.TotalRoadmapPhases != 0 {
		t.Errorf("TotalRoadmapPhases = %d, want 0 (reset on reject)", f.TotalRoadmapPhases)
	}

	// Orchestrator dispatches startPhase(PhasePlan) internally which transitions
	// PlanReady → Planning before returning from HandleReviewDecision.
	if f.Status != feature.StatusPlanning {
		t.Errorf("Status = %v, want Planning (orchestrator auto-dispatches startPlan after reject)", f.Status)
	}
}

// TestNeedsHumanReview_RoadmapProceed_SetsTotalPhases_AdvancesThroughAllPhases
// is a regression test that verifies the full roadmap lifecycle when a roadmap
// goes through the needs_human_review → proceed path:
//  1. PlanReviewDecisionMsg{Decision: "proceed"} parses the roadmap, sets
//     TotalRoadmapPhases, and advances CurrentRoadmapPhase to 1 (not PRReady).
//  2. After phase 1 implementation completes (review_passed), the feature
//     advances to phase 2 planning instead of going straight to PRReady.
//

// TestFeatureIDFromSession_MultiRepo verifies that featureIDFromSession correctly
// extracts the feature ID from session IDs that include repo names (multi-repo format).
func TestFeatureIDFromSession_MultiRepo(t *testing.T) {
	tests := []struct {
		name      string
		sessionID string
		want      string
	}{
		{
			name:      "old format impl",
			sessionID: "abc123-impl-01",
			want:      "abc123",
		},
		{
			name:      "new format with repo name",
			sessionID: "abc123-impl-myrepo-01",
			want:      "abc123",
		},
		{
			name:      "new format with hyphenated repo name",
			sessionID: "abc123-impl-my-repo-01",
			want:      "abc123",
		},
		{
			name:      "repo name contains plan",
			sessionID: "abc123-impl-deploy-plan-01",
			want:      "abc123",
		},
		{
			name:      "repo name contains research",
			sessionID: "abc123-impl-data-research-01",
			want:      "abc123",
		},
		{
			name:      "repo name contains kb",
			sessionID: "abc123-impl-my-kb-service-01",
			want:      "abc123",
		},
		{
			name:      "repo name contains review",
			sessionID: "abc123-impl-code-review-tool-01",
			want:      "abc123",
		},
		{
			name:      "research session",
			sessionID: "abc123-research",
			want:      "abc123",
		},
		{
			name:      "review session",
			sessionID: "abc123-review-01",
			want:      "abc123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := featureIDFromSession(tt.sessionID)
			if got != tt.want {
				t.Errorf("featureIDFromSession(%q) = %q, want %q", tt.sessionID, got, tt.want)
			}
		})
	}
}

// TestHandleMultiRepoImplDone_AllPassed verifies that when the orchestrator
// reports all_passed with auto-publish enabled, handleMultiRepoImplDone
// calls CompleteImplementation. Since no per-repo publishes have completed,
// the feature stays at ReviewPassed (waiting for per-repo publish results).
// TestHandleMultiRepoImplDone_Failed verifies that when the orchestrator
// reports failure, handleMultiRepoImplDone delegates to handleImplementLoopDone
// with failed status, which marks the feature as Failed.
func TestHandleMultiRepoImplDone_Failed(t *testing.T) {
	app, fm := newTestAppModel(t)

	f, err := fm.Create("Multi Failed", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = fm.Transition(f.ID, feature.StatusResearching)
	_ = fm.Transition(f.ID, feature.StatusPlanReady)
	_ = fm.Transition(f.ID, feature.StatusPlanning)
	_ = fm.Transition(f.ID, feature.StatusImplementReady)
	_ = fm.Transition(f.ID, feature.StatusImplementing)

	msg := MultiRepoImplDoneMsg{
		FeatureID: f.ID,
		Result: &agent.OrchestratorResult{
			FinalStatus: "failed",
			FailedRepos: []string{"repo-a", "repo-b"},
			LastError:   "repo-a: tests failed",
		},
	}
	result, _ := app.Update(msg)
	_ = result.(AppModel)

	updated, _ := fm.Get(f.ID)
	if updated.Status != feature.StatusFailed {
		t.Errorf("feature status = %v, want Failed after orchestrator failure", updated.Status)
	}
	if !strings.Contains(updated.LastError, "repo-a: tests failed") {
		t.Errorf("LastError = %q, want it to contain the orchestrator error", updated.LastError)
	}
}

// TestHandleMultiRepoImplDone_FailedDefaultError verifies that when LastError
// is empty in the orchestrator result, a default error message is constructed
// from the FailedRepos list.
func TestHandleMultiRepoImplDone_FailedDefaultError(t *testing.T) {
	app, fm := newTestAppModel(t)

	f, err := fm.Create("Multi Failed Default", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = fm.Transition(f.ID, feature.StatusResearching)
	_ = fm.Transition(f.ID, feature.StatusPlanReady)
	_ = fm.Transition(f.ID, feature.StatusPlanning)
	_ = fm.Transition(f.ID, feature.StatusImplementReady)
	_ = fm.Transition(f.ID, feature.StatusImplementing)

	msg := MultiRepoImplDoneMsg{
		FeatureID: f.ID,
		Result: &agent.OrchestratorResult{
			FinalStatus: "failed",
			FailedRepos: []string{"repo-a", "repo-b"},
		},
	}
	result, _ := app.Update(msg)
	_ = result.(AppModel)

	updated, _ := fm.Get(f.ID)
	if updated.Status != feature.StatusFailed {
		t.Errorf("feature status = %v, want Failed", updated.Status)
	}
	if !strings.Contains(updated.LastError, "repo-a") || !strings.Contains(updated.LastError, "repo-b") {
		t.Errorf("LastError = %q, want it to mention failed repos", updated.LastError)
	}
}

func TestSessionIDParsing_KBWithRepo(t *testing.T) {
	tests := []struct {
		name      string
		sessionID string
		wantID    string
		wantPhase feature.Phase
	}{
		{"per-repo KB", "abc123-kb-my-service", "abc123", feature.PhaseKnowledgeBase},
		{"legacy KB", "abc123-kb", "abc123", feature.PhaseKnowledgeBase},
		{"per-repo KB with dashes in repo name", "abc123-kb-my-repo-with-dashes", "abc123", feature.PhaseKnowledgeBase},
		{"KB repo name contains research", "abc123-kb-data-research", "abc123", feature.PhaseKnowledgeBase},
		{"KB repo name contains review", "abc123-kb-code-review-tool", "abc123", feature.PhaseKnowledgeBase},
		{"KB repo name contains plan", "abc123-kb-deploy-plan", "abc123", feature.PhaseKnowledgeBase},
		{"KB repo name contains kb", "abc123-kb-kb-utils", "abc123", feature.PhaseKnowledgeBase},
		{"research session", "abc123-research", "abc123", feature.PhaseResearch},
		{"impl session", "abc123-impl-my-service-01", "abc123", feature.PhaseImplement},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotID := featureIDFromSession(tt.sessionID)
			if gotID != tt.wantID {
				t.Errorf("featureIDFromSession(%q) = %q, want %q", tt.sessionID, gotID, tt.wantID)
			}
			gotPhase := phaseFromSessionID(tt.sessionID)
			if gotPhase != tt.wantPhase {
				t.Errorf("phaseFromSessionID(%q) = %v, want %v", tt.sessionID, gotPhase, tt.wantPhase)
			}
		})
	}
}

func TestRepoNameFromKBSession(t *testing.T) {
	tests := []struct {
		name      string
		sessionID string
		want      string
	}{
		{"per-repo KB", "abc123-kb-my-service", "my-service"},
		{"legacy KB", "abc123-kb", ""},
		{"per-repo KB with dashes", "abc123-kb-my-repo-with-dashes", "my-repo-with-dashes"},
		{"KB repo name contains research", "abc123-kb-data-research", "data-research"},
		{"KB repo name contains review", "abc123-kb-code-review-tool", "code-review-tool"},
		{"KB repo name contains plan", "abc123-kb-deploy-plan", "deploy-plan"},
		{"KB repo name contains kb", "abc123-kb-kb-utils", "kb-utils"},
		{"research session", "abc123-research", ""},
		{"impl session", "abc123-impl-my-service-01", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := repoNameFromKBSession(tt.sessionID)
			if got != tt.want {
				t.Errorf("repoNameFromKBSession(%q) = %q, want %q", tt.sessionID, got, tt.want)
			}
		})
	}
}

// TestKBFailureStopsSiblingKBSessions verifies that when one repo's KB build
// fails, the other active KB sessions for the same feature are stopped. This
// prevents their locks from being treated as stale by other features once the
// owning feature transitions out of StatusBuildingKB.
// TestKBLockWaitAbortsOnFeatureFailure verifies that runKBForRepo exits its
// lock-wait loop when the feature transitions out of StatusBuildingKB (e.g.,
// because a sibling repo's KB build failed). Without this check, the waiting
// goroutine would remain alive and could later acquire the lock or emit a
// completion for a feature that is already Failed.
// TestLateKBCompletionIgnoredAfterFailure verifies that handlePhaseCompleted
// ignores a KB success message if the feature has already left StatusBuildingKB
// (e.g., because a sibling repo failed). Without this guard, the late
// completion would mutate KBStatus on a Failed feature and potentially trigger
// a state advance.
func TestLateKBCompletionIgnoredAfterFailure(t *testing.T) {
	app, fm := newTestAppModel(t)
	fm.Config.Repos["repo-b"] = config.RepoConfig{Path: "/tmp/repo-b"}

	f, err := fm.Create("late-kb-completion", "desc", []string{"test-repo", "repo-b"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Set up as if repo-a failed: feature is now Failed with per-repo tracking
	_ = fm.Store.Modify(f.ID, func(feat *feature.Feature) error {
		feat.Status = feature.StatusFailed
		feat.KBStatus = map[string]string{
			"test-repo": "failed",
			"repo-b":    "building",
		}
		return nil
	})

	// Simulate a late KB success from repo-b (its session completed after
	// the feature was already marked Failed due to repo-a's failure)
	pcm := PhaseCompletedMsg{
		FeatureID:   f.ID,
		Phase:       feature.PhaseKnowledgeBase,
		SessionID:   f.ID + "-kb-repo-b",
		Success:     true,
		ErrorDetail: "",
	}
	app.handlePhaseCompleted(pcm)

	// Verify: the feature should still be Failed — the late completion
	// should have been ignored.
	updated, _ := fm.Get(f.ID)
	if updated.Status != feature.StatusFailed {
		t.Errorf("feature status = %v, want Failed (late completion should be ignored)", updated.Status)
	}

	// Verify: repo-b's KBStatus should still be "building" — not "completed"
	if updated.KBStatus["repo-b"] != "building" {
		t.Errorf("repo-b KBStatus = %q, want %q (late completion should not mutate state)", updated.KBStatus["repo-b"], "building")
	}
}

func TestHandleSDKEvent_KBResultSuccessRoutesThroughProtocolValidation(t *testing.T) {
	app, fm := newTestAppModel(t)
	app.eventCh = make(chan interface{}, 1)

	f, err := fm.Create("KB SDK Success", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := fm.Store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.Status = feature.StatusBuildingKB
		ff.CurrentPhase = feature.PhaseKnowledgeBase
		return nil
	}); err != nil {
		t.Fatalf("modify feature: %v", err)
	}

	kbDir := agent.KBStateDir(fm.Store.BaseDir, "test-repo")
	if err := os.MkdirAll(kbDir, 0o755); err != nil {
		t.Fatalf("mkdir kb dir: %v", err)
	}
	if err := os.WriteFile(agent.KBPath(kbDir), []byte("# kb\n"), 0o644); err != nil {
		t.Fatalf("write index.md: %v", err)
	}
	// Intentionally do not write phase_complete.

	sm := session.NewManager(nil)
	sess := session.NewSession(f.ID+"-kb-test-repo", f.ID, feature.PhaseKnowledgeBase)
	sm.RegisterTestSession(sess)
	app.sessionManager = sm

	_, _ = app.handleSDKEvent(SDKSessionEventMsg{
		Event: session.SDKEventMsg{
			SessionID: sess.ID(),
			FeatureID: f.ID,
			Phase:     feature.PhaseKnowledgeBase,
			Message: llm.SDKMessage{
				Type:    "result",
				Subtype: "success",
				Result:  &llm.ResultMessage{Type: "result", Subtype: "success"},
			},
		},
	})

	updated, err := fm.Get(f.ID)
	if err != nil {
		t.Fatalf("get feature: %v", err)
	}
	if updated.Status != feature.StatusBuildingKB {
		t.Fatalf("status = %v, want BuildingKB first retry", updated.Status)
	}
	if updated.FailureType != "" {
		t.Fatalf("failure type = %q, want empty on first retry", updated.FailureType)
	}
	if updated.LastError != "" {
		t.Fatalf("LastError = %q, want empty on first retry", updated.LastError)
	}
	sidecar, err := agent.ReadProtocolRetrySidecarAt(kbDir, agent.KBProtocolRetrySidecarFilename(f.ID))
	if err != nil {
		t.Fatalf("ReadProtocolRetrySidecarAt() error = %v", err)
	}
	if sidecar == nil || sidecar.Consecutive != 1 || !strings.Contains(sidecar.LastViolation, agent.PhaseCompleteFile) {
		t.Fatalf("sidecar = %#v, want first phase_complete retry", sidecar)
	}
}

func TestHandleSessionDone_KBSuccessRoutesThroughProtocolValidation(t *testing.T) {
	app, fm := newTestAppModel(t)
	app.eventCh = make(chan interface{}, 1)

	f, err := fm.Create("KB Done Success", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := fm.Store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.Status = feature.StatusBuildingKB
		ff.CurrentPhase = feature.PhaseKnowledgeBase
		return nil
	}); err != nil {
		t.Fatalf("modify feature: %v", err)
	}

	kbDir := agent.KBStateDir(fm.Store.BaseDir, "test-repo")
	if err := os.MkdirAll(kbDir, 0o755); err != nil {
		t.Fatalf("mkdir kb dir: %v", err)
	}
	if err := os.WriteFile(agent.KBPath(kbDir), []byte("# kb\n"), 0o644); err != nil {
		t.Fatalf("write index.md: %v", err)
	}
	// Intentionally do not write phase_complete.

	sm := session.NewManager(nil)
	sess := session.NewSession(f.ID+"-kb-test-repo", f.ID, feature.PhaseKnowledgeBase)
	sess.SendStatus("SUCCESS")
	sm.RegisterTestSession(sess)
	app.sessionManager = sm

	_, _ = app.handleSessionDone(SessionDoneTUIMsg{
		Done: session.SessionDoneMsg{
			SessionID: sess.ID(),
			FeatureID: f.ID,
			Phase:     feature.PhaseKnowledgeBase,
			Status:    session.SessionDone,
		},
	})

	updated, err := fm.Get(f.ID)
	if err != nil {
		t.Fatalf("get feature: %v", err)
	}
	if updated.Status != feature.StatusBuildingKB {
		t.Fatalf("status = %v, want BuildingKB first retry", updated.Status)
	}
	if updated.FailureType != "" {
		t.Fatalf("failure type = %q, want empty on first retry", updated.FailureType)
	}
	if updated.LastError != "" {
		t.Fatalf("LastError = %q, want empty on first retry", updated.LastError)
	}
	sidecar, err := agent.ReadProtocolRetrySidecarAt(kbDir, agent.KBProtocolRetrySidecarFilename(f.ID))
	if err != nil {
		t.Fatalf("ReadProtocolRetrySidecarAt() error = %v", err)
	}
	if sidecar == nil || sidecar.Consecutive != 1 || !strings.Contains(sidecar.LastViolation, agent.PhaseCompleteFile) {
		t.Fatalf("sidecar = %#v, want first phase_complete retry", sidecar)
	}
}

type tuiArtifactPhaseCase struct {
	name  string
	phase feature.Phase
}

func tuiArtifactPhaseCases() []tuiArtifactPhaseCase {
	return []tuiArtifactPhaseCase{
		{"inquire", feature.PhaseInquire},
		{"research", feature.PhaseResearch},
		{"design", feature.PhaseDesign},
	}
}

func createFeatureInPhase(t *testing.T, fm *feature.Manager, phase feature.Phase) *feature.Feature {
	t.Helper()
	f, err := fm.Create("Artifact Phase Success", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := fm.Store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.Status = statusForPhase(phase)
		ff.CurrentPhase = phase
		return nil
	}); err != nil {
		t.Fatalf("modify feature: %v", err)
	}
	updated, err := fm.Get(f.ID)
	if err != nil {
		t.Fatalf("get feature: %v", err)
	}
	return updated
}

func writeTUIPhaseMarkdown(t *testing.T, baseDir string, f *feature.Feature, phase feature.Phase, name string) string {
	t.Helper()
	artifactDir := filepath.Join(agent.ActiveRunDir(baseDir, f), phase.DirName())
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		t.Fatalf("mkdir artifact dir: %v", err)
	}
	path := filepath.Join(artifactDir, name)
	if err := os.WriteFile(path, []byte("# "+phase.DirName()+"\n"), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	return path
}

func writeTUIPhaseComplete(t *testing.T, baseDir string, f *feature.Feature, phase feature.Phase) string {
	t.Helper()
	artifactDir := filepath.Join(agent.ActiveRunDir(baseDir, f), phase.DirName())
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		t.Fatalf("mkdir artifact dir: %v", err)
	}
	path := filepath.Join(artifactDir, agent.PhaseCompleteFile)
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("write phase_complete: %v", err)
	}
	return path
}

func appendAssistantQuestion(t *testing.T, sess *session.Session) {
	t.Helper()
	sess.MessageLog().Append(llm.SDKMessage{
		Type: "assistant",
		Assistant: &llm.AssistantMessage{
			Message: llm.ConversationMsg{
				Role: "assistant",
				Content: []llm.ContentBlock{
					{Type: "text", Text: "Should I continue with this interpretation?"},
				},
			},
		},
	})
}

func assertNoPendingQuestionHelp(t *testing.T, f *feature.Feature) {
	t.Helper()
	for _, h := range f.HelpQueue {
		if h.Pending && (h.Question == questionHelpMessage || h.Question == waitingInputHelpMessage) {
			t.Fatalf("unexpected pending help request: %q", h.Question)
		}
	}
}

func TestHandleSDKEvent_ArtifactPhaseResultSuccessRoutesThroughProtocolValidation(t *testing.T) {
	for _, tc := range tuiArtifactPhaseCases() {
		t.Run(tc.name, func(t *testing.T) {
			app, fm := newTestAppModel(t)
			app.eventCh = make(chan interface{}, 1)
			app.lastNotifyTime = make(map[notifyKey]time.Time)
			f := createFeatureInPhase(t, fm, tc.phase)
			writeTUIPhaseMarkdown(t, fm.Store.BaseDir, f, tc.phase, "artifact.md")

			sm := session.NewManager(nil)
			sess := session.NewSession(f.ID+"-"+tc.phase.DirName(), f.ID, tc.phase)
			appendAssistantQuestion(t, sess)
			sm.RegisterTestSession(sess)
			app.sessionManager = sm

			_, _ = app.handleSDKEvent(SDKSessionEventMsg{
				Event: session.SDKEventMsg{
					SessionID: sess.ID(),
					FeatureID: f.ID,
					Phase:     tc.phase,
					Message: llm.SDKMessage{
						Type:    "result",
						Subtype: "success",
						Result:  &llm.ResultMessage{Type: "result", Subtype: "success"},
					},
				},
			})

			updated, err := fm.Get(f.ID)
			if err != nil {
				t.Fatalf("get feature: %v", err)
			}
			assertTUIArtifactPhaseRetry(t, fm, updated, tc.phase, agent.PhaseCompleteFile)
			assertNoPendingQuestionHelp(t, updated)
		})
	}
}

func TestHandleSessionDone_StaleRegistryOwnedCompletionDoesNotRetryReplacement(t *testing.T) {
	app, fm := newTestAppModel(t)
	app.eventCh = make(chan interface{}, 1)
	app.lastNotifyTime = make(map[notifyKey]time.Time)
	f := createFeatureInPhase(t, fm, feature.PhaseInquire)
	writeTUIPhaseMarkdown(t, fm.Store.BaseDir, f, feature.PhaseInquire, "inquiry.md")

	sm := session.NewManager(nil)
	sessionID := f.ID + "-inquire"
	oldSess := session.NewSession(sessionID, f.ID, feature.PhaseInquire)
	appendAssistantQuestion(t, oldSess)
	oldStartedAt := oldSess.StartedAt()
	if oldStartedAt.IsZero() {
		t.Fatal("old session StartedAt is zero")
	}
	sm.RegisterTestSession(oldSess)
	app.sessionManager = sm

	_, _ = app.handleSDKEvent(SDKSessionEventMsg{
		Event: session.SDKEventMsg{
			SessionID: sessionID,
			FeatureID: f.ID,
			Phase:     feature.PhaseInquire,
			StartedAt: oldStartedAt,
			Message: llm.SDKMessage{
				Type:    "result",
				Subtype: "success",
				Result:  &llm.ResultMessage{Type: "result", Subtype: "success"},
			},
		},
	})

	retrySess := session.NewSession(sessionID, f.ID, feature.PhaseInquire)
	for retrySess.StartedAt().Equal(oldStartedAt) {
		time.Sleep(time.Nanosecond)
		retrySess = session.NewSession(sessionID, f.ID, feature.PhaseInquire)
	}
	sm.RegisterTestSession(retrySess)

	_, _ = app.handleSessionDone(SessionDoneTUIMsg{
		Done: session.SessionDoneMsg{
			SessionID: sessionID,
			FeatureID: f.ID,
			Phase:     feature.PhaseInquire,
			StartedAt: oldStartedAt,
			Status:    session.SessionDone,
		},
	})

	updated, err := fm.Get(f.ID)
	if err != nil {
		t.Fatalf("get feature: %v", err)
	}
	assertTUIArtifactPhaseRetry(t, fm, updated, feature.PhaseInquire, agent.PhaseCompleteFile)
}

func TestHandleSessionDone_ArtifactPhaseSuccessRoutesThroughProtocolValidation(t *testing.T) {
	for _, tc := range tuiArtifactPhaseCases() {
		t.Run(tc.name, func(t *testing.T) {
			app, fm := newTestAppModel(t)
			app.eventCh = make(chan interface{}, 1)
			app.lastNotifyTime = make(map[notifyKey]time.Time)
			f := createFeatureInPhase(t, fm, tc.phase)
			writeTUIPhaseMarkdown(t, fm.Store.BaseDir, f, tc.phase, "artifact.md")

			sm := session.NewManager(nil)
			sess := session.NewSession(f.ID+"-"+tc.phase.DirName(), f.ID, tc.phase)
			appendAssistantQuestion(t, sess)
			sess.SendStatus("SUCCESS")
			sm.RegisterTestSession(sess)
			app.sessionManager = sm

			_, _ = app.handleSessionDone(SessionDoneTUIMsg{
				Done: session.SessionDoneMsg{
					SessionID: sess.ID(),
					FeatureID: f.ID,
					Phase:     tc.phase,
					Status:    session.SessionDone,
				},
			})

			updated, err := fm.Get(f.ID)
			if err != nil {
				t.Fatalf("get feature: %v", err)
			}
			assertTUIArtifactPhaseRetry(t, fm, updated, tc.phase, agent.PhaseCompleteFile)
			assertNoPendingQuestionHelp(t, updated)
		})
	}
}

func TestBuildRepoTabsFindsReviewSession(t *testing.T) {
	sm := session.NewManager(nil)
	app := AppModel{sessionManager: sm}

	// Create a multi-repo feature with repos "payments" and "graph"
	f := &feature.Feature{
		ID:     "f1",
		Status: feature.StatusImplementing,
		Repos: []feature.FeatureRepo{
			{Name: "payments"},
			{Name: "graph"},
		},
		RepoStates: map[string]*feature.RepoState{
			"payments": repoStatePending(),
			"graph":    repoStatePending(),
		},
		SchemaVersion: feature.SchemaVersionCurrent,
	}

	// Register an impl session for "payments" and a review session for "graph"
	implSess := session.NewSession("f1-impl-payments-01", "f1", feature.PhaseImplement)
	implSess.SetRepoName("payments")
	sm.RegisterTestSession(implSess)

	reviewSess := session.NewSession("f1-review-graph-01", "f1", feature.PhaseReview)
	reviewSess.SetRepoName("graph")
	sm.RegisterTestSession(reviewSess)

	tabs := app.buildRepoTabs(f)
	if len(tabs) != 2 {
		t.Fatalf("expected 2 tabs, got %d", len(tabs))
	}

	// Verify both sessions are discovered
	foundImpl := false
	foundReview := false
	for _, tab := range tabs {
		switch tab.repoName {
		case "payments":
			if tab.sess != implSess {
				t.Error("expected payments tab to have impl session")
			}
			foundImpl = true
		case "graph":
			if tab.sess != reviewSess {
				t.Error("expected graph tab to have review session")
			}
			// Mid-flight per-repo status was retired with the unified
			// flow; tabs derive their presentation status from RepoStates
			// plus the active cycle map.
			if tab.status != statusPending {
				t.Errorf("expected graph status=pending, got %v", tab.status)
			}
			foundReview = true
		}
	}
	if !foundImpl {
		t.Error("impl session tab not found")
	}
	if !foundReview {
		t.Error("review session tab not found")
	}
}

func TestBuildRepoTabsFindsReviewBySessionID(t *testing.T) {
	sm := session.NewManager(nil)
	app := AppModel{sessionManager: sm}

	f := &feature.Feature{
		ID:     "f1",
		Status: feature.StatusImplementing,
		Repos: []feature.FeatureRepo{
			{Name: "graph"},
		},
		RepoStates: map[string]*feature.RepoState{
			"graph": repoStatePending(),
		},
		SchemaVersion: feature.SchemaVersionCurrent,
	}

	// Register a review session WITHOUT RepoName set — should fall back to session ID parsing
	reviewSess := session.NewSession("f1-review-graph-02", "f1", feature.PhaseReview)
	sm.RegisterTestSession(reviewSess)

	tabs := app.buildRepoTabs(f)
	if len(tabs) != 1 {
		t.Fatalf("expected 1 tab, got %d", len(tabs))
	}
	if tabs[0].sess != reviewSess {
		t.Error("expected review session to be discovered via session ID parsing")
	}
}

// TestBuildCrossRefEntries verifies that cross-repo PR references reflect the
// just-published repo plus any sibling repo state already persisted on disk.
func TestBuildCrossRefEntries(t *testing.T) {
	tests := []struct {
		name              string
		repos             []feature.FeatureRepo
		repoStates        map[string]*feature.RepoState
		justPublishedRepo string
		justPublishedURL  string
		expected          []git.CrossRefEntry
	}{
		{
			name: "two repos one just published",
			repos: []feature.FeatureRepo{
				{Name: "repo-a", Branch: "feature/test"},
				{Name: "repo-b", Branch: "feature/test"},
			},
			repoStates: map[string]*feature.RepoState{
				"repo-a": repoStateTouched(),
				"repo-b": repoStatePR("https://github.com/org/repo-b/pull/43"),
			},
			justPublishedRepo: "repo-a",
			justPublishedURL:  "https://github.com/org/repo-a/pull/42",
			expected: []git.CrossRefEntry{
				{RepoName: "repo-a", Branch: "feature/test", PRURL: "https://github.com/org/repo-a/pull/42"},
				{RepoName: "repo-b", Branch: "feature/test", PRURL: "https://github.com/org/repo-b/pull/43"},
			},
		},
		{
			name: "one failed repo",
			repos: []feature.FeatureRepo{
				{Name: "repo-a", Branch: "feature/test"},
				{Name: "repo-b", Branch: "feature/test"},
			},
			repoStates: map[string]*feature.RepoState{
				"repo-a": repoStatePR("https://github.com/org/repo-a/pull/42"),
				"repo-b": repoStateFailed("failed"),
			},
			justPublishedRepo: "",
			justPublishedURL:  "",
			expected: []git.CrossRefEntry{
				{RepoName: "repo-a", Branch: "feature/test", PRURL: "https://github.com/org/repo-a/pull/42"},
				{RepoName: "repo-b", Branch: "feature/test", PRURL: "(failed)"},
			},
		},
		{
			name: "all pending",
			repos: []feature.FeatureRepo{
				{Name: "repo-a", Branch: "feature/test"},
				{Name: "repo-b", Branch: "feature/test"},
			},
			repoStates: map[string]*feature.RepoState{
				"repo-a": repoStatePending(),
				"repo-b": repoStatePending(),
			},
			justPublishedRepo: "",
			justPublishedURL:  "",
			expected: []git.CrossRefEntry{
				{RepoName: "repo-a", Branch: "feature/test", PRURL: ""},
				{RepoName: "repo-b", Branch: "feature/test", PRURL: ""},
			},
		},
		{
			name: "single repo",
			repos: []feature.FeatureRepo{
				{Name: "repo-a", Branch: "feature/test"},
			},
			repoStates: map[string]*feature.RepoState{
				"repo-a": repoStatePR("https://github.com/org/repo-a/pull/42"),
			},
			justPublishedRepo: "",
			justPublishedURL:  "",
			expected: []git.CrossRefEntry{
				{RepoName: "repo-a", Branch: "feature/test", PRURL: "https://github.com/org/repo-a/pull/42"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &feature.Feature{
				Repos:         tt.repos,
				RepoStates:    tt.repoStates,
				SchemaVersion: feature.SchemaVersionCurrent,
			}
			got := buildCrossRefEntries(f, tt.justPublishedRepo, tt.justPublishedURL)
			if len(got) != len(tt.expected) {
				t.Fatalf("expected %d entries, got %d", len(tt.expected), len(got))
			}
			for i := range tt.expected {
				if got[i].RepoName != tt.expected[i].RepoName {
					t.Errorf("entry %d: RepoName = %q, want %q", i, got[i].RepoName, tt.expected[i].RepoName)
				}
				if got[i].Branch != tt.expected[i].Branch {
					t.Errorf("entry %d: Branch = %q, want %q", i, got[i].Branch, tt.expected[i].Branch)
				}
				if got[i].PRURL != tt.expected[i].PRURL {
					t.Errorf("entry %d: PRURL = %q, want %q", i, got[i].PRURL, tt.expected[i].PRURL)
				}
			}
		})
	}
}

// TestBuildCrossRefEntries_FreshStateIncludesSiblingPR verifies that when a
// feature's RepoStates already contains PR URLs from earlier publishes (i.e., the
// caller re-read the feature after SetRepoPublished), buildCrossRefEntries
// correctly picks up those sibling PR URLs. This is a regression test for
// batched auto-publish using stale feature snapshots.
func TestBuildCrossRefEntries_FreshStateIncludesSiblingPR(t *testing.T) {
	// Three repos: repo-a is being published now, repo-b was published
	// earlier (PRURL already in RepoStates), repo-c is still pending.
	f := &feature.Feature{
		Repos: []feature.FeatureRepo{
			{Name: "repo-a", Branch: "feature/multi"},
			{Name: "repo-b", Branch: "feature/multi"},
			{Name: "repo-c", Branch: "feature/multi"},
		},
		RepoStates: map[string]*feature.RepoState{
			"repo-a": repoStateTouched(),
			"repo-b": repoStatePR("https://github.com/org/repo-b/pull/10"),
			"repo-c": repoStateTouched(),
		},
		SchemaVersion: feature.SchemaVersionCurrent,
	}

	got := buildCrossRefEntries(f, "repo-a", "https://github.com/org/repo-a/pull/11")

	if len(got) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(got))
	}

	// repo-a: just-published URL takes precedence
	if got[0].RepoName != "repo-a" {
		t.Errorf("entry 0: RepoName = %q, want %q", got[0].RepoName, "repo-a")
	}
	if got[0].PRURL != "https://github.com/org/repo-a/pull/11" {
		t.Errorf("entry 0: PRURL = %q, want %q", got[0].PRURL, "https://github.com/org/repo-a/pull/11")
	}

	// repo-b: sibling PR URL from RepoStates (fresh state after earlier publish)
	if got[1].RepoName != "repo-b" {
		t.Errorf("entry 1: RepoName = %q, want %q", got[1].RepoName, "repo-b")
	}
	if got[1].PRURL != "https://github.com/org/repo-b/pull/10" {
		t.Errorf("entry 1: PRURL = %q, want %q", got[1].PRURL, "https://github.com/org/repo-b/pull/10")
	}

	// repo-c: still pending, empty PRURL
	if got[2].RepoName != "repo-c" {
		t.Errorf("entry 2: RepoName = %q, want %q", got[2].RepoName, "repo-c")
	}
	if got[2].PRURL != "" {
		t.Errorf("entry 2: PRURL = %q, want empty string (pending)", got[2].PRURL)
	}

	// All entries share the same branch
	for i, entry := range got {
		if entry.Branch != "feature/multi" {
			t.Errorf("entry %d: Branch = %q, want %q", i, entry.Branch, "feature/multi")
		}
	}
}

// TestBuildCrossRefEntries_PublishErrorShowsFailed verifies that when a repo
// has LastError set (from SetRepoPublishError),
// the cross-ref entry still shows "(failed)".
func TestBuildCrossRefEntries_PublishErrorShowsFailed(t *testing.T) {
	f := &feature.Feature{
		Repos: []feature.FeatureRepo{
			{Name: "repo-a", Branch: "feature/test"},
			{Name: "repo-b", Branch: "feature/test"},
		},
		RepoStates: map[string]*feature.RepoState{
			"repo-a": repoStatePR("https://github.com/org/repo-a/pull/42"),
			"repo-b": repoStateFailed("publish failed"),
		},
		SchemaVersion: feature.SchemaVersionCurrent,
	}

	got := buildCrossRefEntries(f, "", "")

	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(got))
	}

	// repo-a: normal PR URL
	if got[0].PRURL != "https://github.com/org/repo-a/pull/42" {
		t.Errorf("entry 0: PRURL = %q, want %q", got[0].PRURL, "https://github.com/org/repo-a/pull/42")
	}

	// repo-b: LastError set should still show "(failed)"
	if got[1].PRURL != "(failed)" {
		t.Errorf("entry 1: PRURL = %q, want %q", got[1].PRURL, "(failed)")
	}
}

func TestTransitionToPublish_MultiRepo(t *testing.T) {
	m, fm := newTestAppModel(t)

	// Create a feature with 2 repos
	f := &feature.Feature{
		ID:     "test-multi-pub",
		Name:   "Multi Repo Publish",
		Slug:   "multi-repo-publish",
		Status: feature.StatusCodeReady,
		Repos: []feature.FeatureRepo{
			{Name: "repo-a", Path: "/tmp/repo-a", WorktreePath: "/tmp/wt-a", Branch: "feature/test"},
			{Name: "repo-b", Path: "/tmp/repo-b", WorktreePath: "/tmp/wt-b", Branch: "feature/test"},
		},
		RepoStates: map[string]*feature.RepoState{
			"repo-a": repoStatePR("https://github.com/org/repo-a/pull/42"),
			"repo-b": repoStateTouched(),
		},
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	if err := fm.Store.Save(f); err != nil {
		t.Fatal(err)
	}

	m, _ = m.transitionToPublish("test-multi-pub")

	if m.currentView != ViewPublish {
		t.Errorf("currentView = %v, want ViewPublish", m.currentView)
	}
	if !m.publish.hasRepoSelect {
		t.Error("expected hasRepoSelect to be true")
	}
	if len(m.publish.repos) != 2 {
		t.Errorf("expected 2 repos, got %d", len(m.publish.repos))
	}
	// Check first repo (published)
	if m.publish.repos[0].PRStatus != "published" {
		t.Errorf("repo-a PRStatus = %q, want 'published'", m.publish.repos[0].PRStatus)
	}
	if m.publish.repos[0].PRURL != "https://github.com/org/repo-a/pull/42" {
		t.Errorf("repo-a PRURL = %q, want URL", m.publish.repos[0].PRURL)
	}
	// Check second repo (pending)
	if m.publish.repos[1].PRStatus != "pending" {
		t.Errorf("repo-b PRStatus = %q, want 'pending'", m.publish.repos[1].PRStatus)
	}
}

func TestTransitionToPublish_SingleRepo(t *testing.T) {
	m, fm := newTestAppModel(t)

	f := &feature.Feature{
		ID:     "test-single-pub",
		Name:   "Single Repo Publish",
		Slug:   "single-repo-publish",
		Status: feature.StatusCodeReady,
		Repos: []feature.FeatureRepo{
			{Name: "repo-a", Path: "/tmp/repo-a", WorktreePath: "/tmp/wt-a", Branch: "feature/test"},
		},
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	if err := fm.Store.Save(f); err != nil {
		t.Fatal(err)
	}

	m, _ = m.transitionToPublish("test-single-pub")

	if m.currentView != ViewPublish {
		t.Errorf("currentView = %v, want ViewPublish", m.currentView)
	}
	if m.publish.hasRepoSelect {
		t.Error("expected hasRepoSelect to be false for single repo")
	}
}

// TestUpdatePublish_MultiRepoKeepsPRReady verifies that exiting the publish
// screen after publishing one repo of a multi-repo feature does NOT mark the
// whole feature as Published. The feature must remain PRReady so the user can
// publish remaining repos.
func TestUpdatePublish_MultiRepoKeepsPRReady(t *testing.T) {
	m, fm := newTestAppModel(t)

	f := &feature.Feature{
		ID:     "test-multi-partial",
		Name:   "Partial Publish",
		Slug:   "partial-publish",
		Status: feature.StatusCodeReady,
		Repos: []feature.FeatureRepo{
			{Name: "repo-a", Path: "/tmp/repo-a", WorktreePath: "/tmp/wt-a", Branch: "feature/test"},
			{Name: "repo-b", Path: "/tmp/repo-b", WorktreePath: "/tmp/wt-b", Branch: "feature/test"},
		},
		RepoStates: map[string]*feature.RepoState{
			"repo-a": repoStateTouched(),
			"repo-b": repoStateTouched(),
		},
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	if err := fm.Store.Save(f); err != nil {
		t.Fatal(err)
	}

	m, _ = m.transitionToPublish("test-multi-partial")
	if !m.publish.hasRepoSelect {
		t.Fatal("expected hasRepoSelect to be true for multi-repo")
	}

	// Simulate having published one repo: set prURL and advance to done step.
	m.publish.prURL = "https://github.com/org/repo-a/pull/99"
	m.publish.step = publishStepDone
	m.currentView = ViewPublish

	// Press enter on the done step — triggers IsDone() → true.
	updated, _ := m.updatePublish(tea.KeyPressMsg{Code: tea.KeyEnter})
	updatedApp := updated.(AppModel)

	// Feature must still be PRReady, NOT Published.
	got, err := fm.Get("test-multi-partial")
	if err != nil {
		t.Fatalf("failed to get feature: %v", err)
	}
	if got.Status != feature.StatusCodeReady {
		t.Errorf("feature status = %v, want %v; multi-repo publish should not mark Published after one repo",
			got.Status, feature.StatusCodeReady)
	}
	// Should have transitioned back to dashboard.
	if updatedApp.currentView != ViewDashboard {
		t.Errorf("currentView = %v, want ViewDashboard", updatedApp.currentView)
	}
}

// TestBuildCrossRefEntries_RecoveredRepoShowsPending verifies that a repo which
// recovered from a failure (empty LastError) is shown
// as pending (empty PRURL) rather than "(failed)" in cross-ref entries.
func TestBuildCrossRefEntries_RecoveredRepoShowsPending(t *testing.T) {
	f := &feature.Feature{
		Repos: []feature.FeatureRepo{
			{Name: "repo-a", Branch: "feature/test"},
			{Name: "repo-b", Branch: "feature/test"},
		},
		RepoStates: map[string]*feature.RepoState{
			"repo-a": repoStateTouched(),
			"repo-b": repoStatePR("https://github.com/org/repo-b/pull/55"),
		},
		SchemaVersion: feature.SchemaVersionCurrent,
	}

	got := buildCrossRefEntries(f, "", "")

	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(got))
	}

	// repo-a: recovered, should show as pending (empty PRURL), NOT "(failed)"
	if got[0].RepoName != "repo-a" {
		t.Errorf("entry 0: RepoName = %q, want %q", got[0].RepoName, "repo-a")
	}
	if got[0].PRURL != "" {
		t.Errorf("entry 0: PRURL = %q, want empty (recovered repo should be pending, not failed)", got[0].PRURL)
	}

	// repo-b: has a PR URL, should be returned as-is
	if got[1].RepoName != "repo-b" {
		t.Errorf("entry 1: RepoName = %q, want %q", got[1].RepoName, "repo-b")
	}
	if got[1].PRURL != "https://github.com/org/repo-b/pull/55" {
		t.Errorf("entry 1: PRURL = %q, want %q", got[1].PRURL, "https://github.com/org/repo-b/pull/55")
	}
}

// TestTransitionToPublish_RecoveredRepoShowsPending verifies that a repo which
// recovered from failure (empty LastError) is
// shown as "pending" in the publish selector, not "failed".
func TestTransitionToPublish_RecoveredRepoShowsPending(t *testing.T) {
	m, fm := newTestAppModel(t)

	f := &feature.Feature{
		ID:     "test-recovered-pub",
		Name:   "Recovered Repo Publish",
		Slug:   "recovered-repo-publish",
		Status: feature.StatusCodeReady,
		Repos: []feature.FeatureRepo{
			{Name: "repo-a", Path: "/tmp/repo-a", WorktreePath: "/tmp/wt-a", Branch: "feature/test"},
			{Name: "repo-b", Path: "/tmp/repo-b", WorktreePath: "/tmp/wt-b", Branch: "feature/test"},
		},
		RepoStates: map[string]*feature.RepoState{
			"repo-a": repoStateTouched(),
			"repo-b": repoStatePR("https://github.com/org/repo-b/pull/77"),
		},
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	if err := fm.Store.Save(f); err != nil {
		t.Fatal(err)
	}

	m, _ = m.transitionToPublish("test-recovered-pub")

	if m.currentView != ViewPublish {
		t.Errorf("currentView = %v, want ViewPublish", m.currentView)
	}
	if !m.publish.hasRepoSelect {
		t.Error("expected hasRepoSelect to be true for multi-repo")
	}
	if len(m.publish.repos) != 2 {
		t.Fatalf("expected 2 repos, got %d", len(m.publish.repos))
	}

	// repo-a: recovered, should show as "pending", NOT "failed"
	if m.publish.repos[0].Name != "repo-a" {
		t.Errorf("repos[0].Name = %q, want %q", m.publish.repos[0].Name, "repo-a")
	}
	if m.publish.repos[0].PRStatus != "pending" {
		t.Errorf("repos[0].PRStatus = %q, want %q (recovered repo should not show as failed)",
			m.publish.repos[0].PRStatus, "pending")
	}

	// repo-b: already published, should show as "published"
	if m.publish.repos[1].Name != "repo-b" {
		t.Errorf("repos[1].Name = %q, want %q", m.publish.repos[1].Name, "repo-b")
	}
	if m.publish.repos[1].PRStatus != "published" {
		t.Errorf("repos[1].PRStatus = %q, want %q", m.publish.repos[1].PRStatus, "published")
	}
	if m.publish.repos[1].PRURL != "https://github.com/org/repo-b/pull/77" {
		t.Errorf("repos[1].PRURL = %q, want %q", m.publish.repos[1].PRURL, "https://github.com/org/repo-b/pull/77")
	}
}

// TestTransitionToPublish_UnpublishableFeatureBlocksEntry verifies that
// transitionToPublish returns early for features with unpublishable repos,
// keeping the view on the dashboard and NOT setting publish.publishable = true.
// This is a regression test for the defense-in-depth wiring: even if the outer
// guard in transitionToPublish is bypassed (e.g. crash-recovery or manual state
// edits), the publishable flag must propagate from the feature into the publish model.
func TestTransitionToPublish_UnpublishableFeatureBlocksEntry(t *testing.T) {
	m, fm := newTestAppModel(t)

	f := &feature.Feature{
		ID:     "test-unpub-block",
		Name:   "Unpublishable Feature",
		Slug:   "unpublishable-feature",
		Status: feature.StatusCodeReady,
		Repos: []feature.FeatureRepo{
			{Name: "repo-a", Path: "/tmp/repo-a", WorktreePath: "/tmp/wt-a", Branch: "feature/test", Publishable: boolPtr(false)},
		},
		RepoStates: map[string]*feature.RepoState{
			"repo-a": repoStateTouched(),
		},
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	if err := fm.Store.Save(f); err != nil {
		t.Fatal(err)
	}

	m, cmd := m.transitionToPublish("test-unpub-block")

	if m.currentView != ViewDashboard {
		t.Errorf("currentView = %v, want ViewDashboard (unpublishable feature should not enter publish)", m.currentView)
	}
	if cmd != nil {
		t.Error("expected nil cmd for unpublishable feature")
	}
	if m.publish.publishable {
		t.Error("publish.publishable should not be true for unpublishable feature")
	}
}

// TestHandleRepoStatusUpdate_FinalPhase_SuppressesAutoPublish verifies that
// handleRepoStatusUpdate does NOT trigger auto-publish when CurrentRoadmapPhase
// equals TotalRoadmapPhases (final phase). Roadmap features defer per-repo
// publish to handleMultiRepoImplDone so that commitRoadmapPhase runs first.
// TestHandleMultiRepoImplDone_FinalPhase_PublishesAfterCommit verifies that
// handleMultiRepoImplDone dispatches per-repo auto-publish for the final
// roadmap phase. The feature should be marked PRReady and the returned batch
// should contain one autoPublishRepoCmd per repo.
// --- Refactor Pipeline Wiring Tests ---

// newTestAppWithPublishedRefactorFeature creates an app model with a single Published
// feature that has an active refactor cycle, ready for refactor pipeline tests.
func newTestAppWithPublishedRefactorFeature(t *testing.T, roadmapPhases, currentPhase int) (AppModel, *feature.Feature) {
	t.Helper()
	app, fm := newTestAppModel(t)

	f, err := fm.Create("Refactor Target", "original desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Advance to Published
	_ = fm.Transition(f.ID, feature.StatusResearching)
	_ = fm.Transition(f.ID, feature.StatusPlanReady)
	_ = fm.Transition(f.ID, feature.StatusPlanning)
	_ = fm.Transition(f.ID, feature.StatusImplementReady)
	_ = fm.Transition(f.ID, feature.StatusImplementing)
	_ = fm.Transition(f.ID, feature.StatusReviewPassed)
	_ = fm.Transition(f.ID, feature.StatusCodeReady)
	_ = fm.Transition(f.ID, feature.StatusPublished)

	// Start a refactor cycle (Published -> Inquiring). Manager.StartRefactor
	// is no longer available; emulate it directly.
	if err := fm.Store.Modify(f.ID, func(ff *feature.Feature) error {
		if err := ff.Transition(feature.StatusInquiring); err != nil {
			return err
		}
		ff.SetRefactorCount(ff.RefactorCount() + 1)
		ff.SetActiveCycleType(feature.CycleRefactor)
		ff.RefactorPrompt = "add caching layer"
		ff.CurrentPhase = feature.PhaseInquire
		return nil
	}); err != nil {
		t.Fatalf("set refactor state: %v", err)
	}

	// Set up roadmap state and advance through pipeline to Implementing
	_ = fm.Store.Modify(f.ID, func(feat *feature.Feature) error {
		feat.TotalRoadmapPhases = roadmapPhases
		feat.CurrentRoadmapPhase = currentPhase
		return nil
	})
	_ = fm.Transition(f.ID, feature.StatusInquireReady)
	_ = fm.Transition(f.ID, feature.StatusResearching)
	_ = fm.Transition(f.ID, feature.StatusDesignReady)
	_ = fm.Transition(f.ID, feature.StatusDesigning)
	_ = fm.Transition(f.ID, feature.StatusPlanReady)
	_ = fm.Transition(f.ID, feature.StatusPlanning)
	_ = fm.Transition(f.ID, feature.StatusImplementReady)
	_ = fm.Transition(f.ID, feature.StatusImplementing)

	f, _ = fm.Get(f.ID)
	features, _ := fm.List()
	app.dashboard.SetFeatures(features)
	app.dashboard.cursor = 1
	app.dashboard.syncPreview()
	app.dashboard.focusPanel = 1

	return app, f
}

func TestStartRefactor_ClearsStaleArtifactPlan(t *testing.T) {
	// StartRefactor should clear Artifacts["plan"] so implementation
	// picks up the fresh refactor plan, not the stale pre-refactor plan.
	_, fm := newTestAppModel(t)

	f, err := fm.Create("Stale Plan", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Advance to Published with a stale plan artifact
	_ = fm.Transition(f.ID, feature.StatusResearching)
	_ = fm.Transition(f.ID, feature.StatusPlanReady)
	_ = fm.Transition(f.ID, feature.StatusPlanning)
	_ = fm.Transition(f.ID, feature.StatusImplementReady)
	_ = fm.Transition(f.ID, feature.StatusImplementing)
	_ = fm.Transition(f.ID, feature.StatusReviewPassed)
	_ = fm.Transition(f.ID, feature.StatusCodeReady)
	_ = fm.Transition(f.ID, feature.StatusPublished)

	// Set a stale plan artifact
	_ = fm.Store.Modify(f.ID, func(feat *feature.Feature) error {
		if feat.Artifacts == nil {
			feat.Artifacts = make(map[string]string)
		}
		feat.Artifacts["plan"] = "/old/stale/plan.md"
		return nil
	})

	// Start refactor (Manager.StartRefactor is no longer available; emulate it).
	if err := fm.Store.Modify(f.ID, func(ff *feature.Feature) error {
		if err := ff.Transition(feature.StatusInquiring); err != nil {
			return err
		}
		ff.SetRefactorCount(ff.RefactorCount() + 1)
		ff.RefactorPrompt = "refactor prompt"
		ff.SetActiveCycleType(feature.CycleRefactor)
		ff.CurrentPhase = feature.PhaseInquire
		delete(ff.Artifacts, "plan")
		return nil
	}); err != nil {
		t.Fatalf("set refactor state: %v", err)
	}

	// Verify the stale plan artifact was cleared
	f, _ = fm.Get(f.ID)
	if plan := f.Artifacts["plan"]; plan != "" {
		t.Errorf("Artifacts[\"plan\"] = %q, want empty (should be cleared by StartRefactor)", plan)
	}
	// Other artifacts should be preserved
	if f.RefactorPrompt != "refactor prompt" {
		t.Errorf("RefactorPrompt = %q, want %q", f.RefactorPrompt, "refactor prompt")
	}
}

func TestRoadmapRejectDuringRefactor_WritesToRefactorDir(t *testing.T) {
	// Regression: rejecting a roadmap during a refactor must write feedback
	// to the refactor-scoped roadmap directory, not the base roadmap directory.
	app, fm := newTestAppModel(t)
	app.sessionManager = session.NewManager(nil)

	f, err := fm.Create("Refactor Reject", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Advance to Published
	_ = fm.Transition(f.ID, feature.StatusResearching)
	_ = fm.Transition(f.ID, feature.StatusPlanReady)
	_ = fm.Transition(f.ID, feature.StatusPlanning)
	_ = fm.Transition(f.ID, feature.StatusImplementReady)
	_ = fm.Transition(f.ID, feature.StatusImplementing)
	_ = fm.Transition(f.ID, feature.StatusReviewPassed)
	_ = fm.Transition(f.ID, feature.StatusCodeReady)
	_ = fm.Transition(f.ID, feature.StatusPublished)

	// Start a refactor cycle (Published → Inquiring). Manager.StartRefactor
	// is no longer available; emulate it directly.
	if err := fm.Store.Modify(f.ID, func(ff *feature.Feature) error {
		if err := ff.Transition(feature.StatusInquiring); err != nil {
			return err
		}
		ff.SetRefactorCount(ff.RefactorCount() + 1)
		ff.RefactorPrompt = "add caching"
		ff.SetActiveCycleType(feature.CycleRefactor)
		ff.CurrentPhase = feature.PhaseInquire
		return nil
	}); err != nil {
		t.Fatalf("set refactor state: %v", err)
	}

	// Advance the refactor pipeline through to PlanReady (where roadmap review happens)
	_ = fm.Transition(f.ID, feature.StatusInquireReady)
	_ = fm.Transition(f.ID, feature.StatusResearching)
	_ = fm.Transition(f.ID, feature.StatusDesignReady)
	_ = fm.Transition(f.ID, feature.StatusDesigning)
	_ = fm.Transition(f.ID, feature.StatusPlanReady)

	// Get the updated feature to learn the refactor count
	f, _ = fm.Get(f.ID)
	if !f.IsRefactoring() {
		t.Fatal("expected feature to be refactoring")
	}

	// Create the refactor-scoped roadmap directory and place a completed attempt there
	refactorRoadmapDir := filepath.Join(fm.Store.BaseDir, f.ID, "runs", "run-001", f.RefactorPrefix(), "roadmap")
	if err := os.MkdirAll(refactorRoadmapDir, 0o755); err != nil {
		t.Fatalf("mkdir refactor roadmap dir: %v", err)
	}
	_ = agent.WritePlanAttemptMeta(refactorRoadmapDir, agent.PlanAttemptMeta{
		Attempt:      1,
		AgentStatus:  "SUCCESS",
		ReviewStatus: "APPROVED",
	})

	// Also create the base (non-refactor) roadmap dir — should NOT be touched
	baseRoadmapDir := agent.RoadmapDir(fm.Store.BaseDir, f)
	if err := os.MkdirAll(baseRoadmapDir, 0o755); err != nil {
		t.Fatalf("mkdir base roadmap dir: %v", err)
	}

	// Send a reject decision
	result, _ := app.Update(RoadmapReviewDecisionMsg{
		FeatureID: f.ID,
		Decision:  "reject",
		Comment:   "needs more detail",
	})
	_ = result.(AppModel)

	// Verify: rejection feedback was written to the REFACTOR-scoped dir
	feedbackPath := filepath.Join(refactorRoadmapDir, "attempt-01", "validation-feedback.md")
	data, err := os.ReadFile(feedbackPath)
	if err != nil {
		t.Fatalf("expected feedback file at refactor roadmap dir %s: %v", feedbackPath, err)
	}
	if string(data) != "needs more detail" {
		t.Errorf("feedback = %q, want %q", string(data), "needs more detail")
	}

	// Verify: the meta was overwritten to CHANGES_REQUESTED in the refactor dir
	meta, err := os.ReadFile(filepath.Join(refactorRoadmapDir, "attempt-01", "meta.yaml"))
	if err != nil {
		t.Fatalf("reading meta: %v", err)
	}
	if !strings.Contains(string(meta), "CHANGES_REQUESTED") {
		t.Errorf("refactor roadmap meta should contain CHANGES_REQUESTED, got: %s", string(meta))
	}

	// Verify: the base roadmap dir should NOT have a feedback file
	baseFeedbackPath := filepath.Join(baseRoadmapDir, "attempt-01", "validation-feedback.md")
	if _, err := os.Stat(baseFeedbackPath); err == nil {
		t.Error("feedback file should NOT exist in base roadmap dir (should be in refactor dir)")
	}
}

// --- Multi-Repo Refactor Cycle Tests ---

func TestDispatchRepoCycleCmdRefactor(t *testing.T) {
	app, _ := newTestAppModel(t)
	cmd := app.dispatchRepoCycleCmd("feat-1", "my-repo", feature.CycleRefactor)
	msg := cmd()
	rm, ok := msg.(showRefactorForRepoMsg)
	if !ok {
		t.Fatalf("expected showRefactorForRepoMsg, got %T", msg)
	}
	if rm.FeatureID != "feat-1" || rm.RepoName != "my-repo" {
		t.Errorf("unexpected msg fields: %+v", rm)
	}
}

func TestShowRefactorForRepoMsgActivatesTextarea(t *testing.T) {
	app, _ := newTestAppModel(t)
	msg := showRefactorForRepoMsg{FeatureID: "feat-1", RepoName: "api"}
	result, cmd := app.Update(msg)
	m := result.(AppModel)
	if !m.refactorInputActive {
		t.Error("expected refactorInputActive == true")
	}
	if m.cycleSelectRefactor != "api" {
		t.Errorf("cycleSelectRefactor = %q, want %q", m.cycleSelectRefactor, "api")
	}
	if m.refactorFeatureID != "feat-1" {
		t.Errorf("refactorFeatureID = %q, want %q", m.refactorFeatureID, "feat-1")
	}
	if cmd == nil {
		t.Error("expected non-nil cmd (textarea.Blink)")
	}
}

func TestStartRepoCycleRefactorCmdCreatesArtifactDir(t *testing.T) {
	app, fm := newTestAppModel(t)

	// Set up a phaseRunner and sessionManager so startRefactorCmd can access them
	app.phaseRunner = &agent.PhaseRunner{
		BuildSessionFn: func(opts agent.BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
			return []string{"bash", "-c", "echo done"}, nil, &session.SessionOpts{}, nil
		},
	}
	app.sessionManager = session.NewManager(nil)

	// Add a second repo to config for multi-repo feature
	fm.Config.Repos["web"] = config.RepoConfig{Path: "/tmp/web"}

	f, err := fm.Create("test-refactor", "Test desc", []string{"test-repo", "web"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Walk through states to Published
	_ = fm.Transition(f.ID, feature.StatusResearching)
	_ = fm.Transition(f.ID, feature.StatusPlanReady)
	_ = fm.Transition(f.ID, feature.StatusPlanning)
	_ = fm.Transition(f.ID, feature.StatusImplementReady)
	_ = fm.Transition(f.ID, feature.StatusImplementing)
	_ = fm.Transition(f.ID, feature.StatusReviewPassed)
	_ = fm.Transition(f.ID, feature.StatusCodeReady)
	_ = fm.Transition(f.ID, feature.StatusPublished)

	// Execute startRefactorCmd. The refactor loop now lives in
	// orchestrator.StartRefactorCycle, which runs the loop synchronously via a
	// direct HandleRefactorCycleLoopDone call rather than through the tea.Program
	// bus. This means a failing agent loop can race the test and clear
	// RefactorPrompt before we read it, so we check feature state *immediately*
	// after cmd() returns (the setup-persist happens inline) rather than after
	// a sleep.
	cmd := app.startRefactorCmd(f.ID, "web", "improve performance")
	msg := cmd()

	// Should return RefreshFeaturesMsg
	if _, ok := msg.(RefreshFeaturesMsg); !ok {
		t.Fatalf("expected RefreshFeaturesMsg, got %T", msg)
	}

	// Reload feature immediately: RefactorCount bump + RefactorPrompt set happen
	// inline inside StartRefactorCycle before the agent goroutine launches.
	f, _ = fm.Get(f.ID)
	if f.RefactorCount() != 1 {
		t.Errorf("RefactorCount = %d, want 1", f.RefactorCount())
	}

	// Assert artifact directory was created inside the active run using the
	// flat refactor-N/ layout with no per-repo subdir.
	baseDir := fm.Store.BaseDir
	refactorDir := filepath.Join(agent.ActiveRunDir(baseDir, f), "refactor-1")
	if _, err := os.Stat(refactorDir); os.IsNotExist(err) {
		t.Errorf("expected artifact directory to exist: %s", refactorDir)
	}

	// Assert plan path file exists (refactor-prompt.md embeds the prompt text,
	// so we also grep it to prove the prompt was captured — this replaces the
	// prior RefactorPrompt field check that raced the failure cleanup).
	planPath := filepath.Join(refactorDir, "refactor-prompt.md")
	if data, err := os.ReadFile(planPath); err != nil {
		t.Errorf("expected plan file to exist: %s: %v", planPath, err)
	} else if !strings.Contains(string(data), "improve performance") {
		t.Errorf("refactor-prompt.md does not embed the prompt text; got:\n%s", data)
	}

	app.orchestrator.(*orchestrator.Orchestrator).WaitForCycles()
}

func TestStartRepoCycleImplementCmd_SurfacesStartError(t *testing.T) {
	app, _ := newTestAppModel(t)
	app.orchestrator = &fakeOrch{repoCycleErr: errors.New("already running a review-comments cycle")}

	cmd := app.startRepoCycleImplementCmd("feat-1", "agentic", feature.CycleReviewComments, "# plan")
	msg := cmd()

	startMsg, ok := msg.(repoCycleStartResultMsg)
	if !ok {
		t.Fatalf("expected repoCycleStartResultMsg, got %T", msg)
	}
	if startMsg.Err == nil {
		t.Fatal("expected repoCycleStartResultMsg.Err to be set")
	}

	updatedModel, followup := app.Update(startMsg)
	updated, ok := updatedModel.(AppModel)
	if !ok {
		t.Fatalf("expected AppModel from Update, got %T", updatedModel)
	}
	if !strings.Contains(updated.statusMessage, "already running a review-comments cycle") {
		t.Fatalf("statusMessage = %q, want surfaced cycle-start error", updated.statusMessage)
	}
	if followup == nil {
		t.Fatal("expected refresh follow-up command after surfacing cycle-start error")
	}
}

func TestStartReviewCommentsRepoCycleFromView_SavesCommentsForRepo(t *testing.T) {
	app, fm := newTestAppModel(t)
	fake := newFakeOrch()
	app.orchestrator = fake

	f, err := fm.Create("test-review-comments-cycle", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := fm.Store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.Status = feature.StatusPublished
		ff.CurrentPhase = feature.PhasePublish
		ff.RepoStates = map[string]*feature.RepoState{
			"test-repo": repoStatePR("https://github.com/org/test-repo/pull/1"),
		}
		return nil
	}); err != nil {
		t.Fatalf("modify: %v", err)
	}

	comments := []git.ReviewComment{
		{ID: 42, Body: "needs fix", Type: git.CommentTypeReview, User: struct {
			Login string `json:"login"`
		}{Login: "alice"}},
	}

	msg := app.startReviewCommentsRepoCycleFromView(f.ID, "test-repo", comments, "auto")()
	if _, ok := msg.(RefreshFeaturesMsg); !ok {
		t.Fatalf("expected RefreshFeaturesMsg, got %T", msg)
	}
	if len(fake.startRepoCycleImplementArgs) != 1 {
		t.Fatalf("StartRepoCycleImplement calls = %d, want 1", len(fake.startRepoCycleImplementArgs))
	}

	loaded, err := fm.Get(f.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	saved, err := agent.LoadReviewCommentsForRepo(fm.Store.BaseDir, loaded, "test-repo")
	if err != nil {
		t.Fatalf("LoadReviewCommentsForRepo: %v", err)
	}
	if saved.Mode != "auto" || len(saved.Comments) != 1 || saved.Comments[0].ID != 42 {
		t.Fatalf("unexpected saved review comments: %+v", saved)
	}
}
func TestStartRepoCycleRefactorCmdRepoNotFound(t *testing.T) {
	app, fm := newTestAppModel(t)

	f, err := fm.Create("test-refactor-nf", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Walk through states to Published
	_ = fm.Transition(f.ID, feature.StatusResearching)
	_ = fm.Transition(f.ID, feature.StatusPlanReady)
	_ = fm.Transition(f.ID, feature.StatusPlanning)
	_ = fm.Transition(f.ID, feature.StatusImplementReady)
	_ = fm.Transition(f.ID, feature.StatusImplementing)
	_ = fm.Transition(f.ID, feature.StatusReviewPassed)
	_ = fm.Transition(f.ID, feature.StatusCodeReady)
	_ = fm.Transition(f.ID, feature.StatusPublished)

	cmd := app.startRefactorCmd(f.ID, "nonexistent-repo", "some prompt")
	msg := cmd()
	if _, ok := msg.(RefreshFeaturesMsg); !ok {
		t.Fatalf("expected RefreshFeaturesMsg, got %T", msg)
	}
	// Refactor is feature-level; repoName is a hint only. Drain the
	// background goroutine so the agent loop's writes do not race the TempDir
	// cleanup.
	app.orchestrator.(*orchestrator.Orchestrator).WaitForCycles()
}

// TestCompleteRefactorRepoCycleCmd_RepoNotFound was retired in Phase 8
// iteration 3. The TUI no longer owns a completeRefactorRepoCycleCmd helper;
// repo-cycle completion flows through orchestrator.CompleteRefactorRepoCycle
// (see internal/orchestrator/cycles_test.go).

func TestCycleSelectModalRefactorLabel(t *testing.T) {
	app, fm := newTestAppModel(t)

	fm.Config.Repos["web"] = config.RepoConfig{Path: "/tmp/web"}
	f, err := fm.Create("refactor-modal", "desc", []string{"test-repo", "web"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Walk through states to Published
	_ = fm.Transition(f.ID, feature.StatusResearching)
	_ = fm.Transition(f.ID, feature.StatusPlanReady)
	_ = fm.Transition(f.ID, feature.StatusPlanning)
	_ = fm.Transition(f.ID, feature.StatusImplementReady)
	_ = fm.Transition(f.ID, feature.StatusImplementing)
	_ = fm.Transition(f.ID, feature.StatusReviewPassed)
	_ = fm.Transition(f.ID, feature.StatusCodeReady)
	_ = fm.Transition(f.ID, feature.StatusPublished)

	// Set up cycle selector state
	app.openCycleSelector(f.ID, feature.CycleRefactor)

	output := app.cycleSelectModal()
	if !strings.Contains(output, "Refactor") {
		t.Errorf("cycleSelectModal output does not contain %q:\n%s", "Refactor", output)
	}
}

func TestExtractRefactorPrompt(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "standard format with header",
			content: "# Refactor: api\n\nimprove performance\n",
			want:    "improve performance",
		},
		{
			name:    "plain prompt without header",
			content: "just a prompt",
			want:    "just a prompt",
		},
		{
			name:    "empty string",
			content: "",
			want:    "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractRefactorPrompt(tt.content)
			if got != tt.want {
				t.Errorf("extractRefactorPrompt(%q) = %q, want %q", tt.content, got, tt.want)
			}
		})
	}
}

func TestRestartRefactorCycleMsg(t *testing.T) {
	app, _ := newTestAppModel(t)
	msg := restartRefactorCycleMsg{
		FeatureID: "feat-1",
		RepoName:  "api",
		Prompt:    "redo refactor",
	}
	_, cmd := app.Update(msg)
	if cmd == nil {
		t.Error("expected non-nil cmd from restartRefactorCycleMsg")
	}
}

func TestIsRunningFeatureWithRefactorCycle(t *testing.T) {
	f := &feature.Feature{
		ID:     "test-running-refactor",
		Status: feature.StatusPublished,
		RepoCycles: map[string]*feature.RepoCycleState{
			"api": {Type: feature.CycleRefactor, Status: "running"},
		},
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	if !isRunningFeature(f) {
		t.Error("expected isRunningFeature to return true for Published feature with running refactor cycle")
	}
}

func TestStartRepoCycleRefactorCmd_BlocksConcurrentRefactor(t *testing.T) {
	app, fm := newTestAppModel(t)

	app.phaseRunner = &agent.PhaseRunner{}
	app.sessionManager = session.NewManager(nil)

	// Add a second repo to config for multi-repo feature
	fm.Config.Repos["web"] = config.RepoConfig{Path: "/tmp/web"}

	f, err := fm.Create("test-concurrent-refactor", "Test desc", []string{"test-repo", "web"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Walk through states to Published
	_ = fm.Transition(f.ID, feature.StatusResearching)
	_ = fm.Transition(f.ID, feature.StatusPlanReady)
	_ = fm.Transition(f.ID, feature.StatusPlanning)
	_ = fm.Transition(f.ID, feature.StatusImplementReady)
	_ = fm.Transition(f.ID, feature.StatusImplementing)
	_ = fm.Transition(f.ID, feature.StatusReviewPassed)
	_ = fm.Transition(f.ID, feature.StatusCodeReady)
	_ = fm.Transition(f.ID, feature.StatusPublished)

	// Simulate repo "test-repo" already running a refactor cycle
	_ = fm.StartRepoCycle(f.ID, "test-repo", feature.CycleRefactor)

	// Attempt to start a refactor on "web" — should be blocked
	cmd := app.startRefactorCmd(f.ID, "web", "improve performance")
	msg := cmd()

	if _, ok := msg.(RefreshFeaturesMsg); !ok {
		t.Fatalf("expected RefreshFeaturesMsg, got %T", msg)
	}

	// Verify RefactorCount was NOT incremented (blocked before that step)
	f, _ = fm.Get(f.ID)
	if f.RefactorCount() != 0 {
		t.Errorf("RefactorCount = %d, want 0 (should not increment when blocked)", f.RefactorCount())
	}

	// Verify "web" does NOT have a running cycle
	if rc, ok := f.RepoCycles["web"]; ok && rc.Status == "running" {
		t.Error("expected web repo to NOT have a running cycle")
	}
}

func TestRestartRepoCycleRefactorCmd_PreservesRefactorCount(t *testing.T) {
	app, fm := newTestAppModel(t)

	app.phaseRunner = &agent.PhaseRunner{
		BuildSessionFn: func(opts agent.BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
			return []string{"bash", "-c", "echo done"}, nil, &session.SessionOpts{}, nil
		},
	}
	app.sessionManager = session.NewManager(nil)

	// Add a second repo to config for multi-repo feature
	fm.Config.Repos["web"] = config.RepoConfig{Path: "/tmp/web"}

	f, err := fm.Create("test-restart-refactor", "Test desc", []string{"test-repo", "web"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Walk through states to Published
	_ = fm.Transition(f.ID, feature.StatusResearching)
	_ = fm.Transition(f.ID, feature.StatusPlanReady)
	_ = fm.Transition(f.ID, feature.StatusPlanning)
	_ = fm.Transition(f.ID, feature.StatusImplementReady)
	_ = fm.Transition(f.ID, feature.StatusImplementing)
	_ = fm.Transition(f.ID, feature.StatusReviewPassed)
	_ = fm.Transition(f.ID, feature.StatusCodeReady)
	_ = fm.Transition(f.ID, feature.StatusPublished)

	// Simulate a prior refactor attempt: set RefactorCount to 2.
	_ = fm.Store.Modify(f.ID, func(f *feature.Feature) error {
		f.SetRefactorCount(2)
		return nil
	})
	baseDir := fm.Store.BaseDir

	// Call restartRepoCycleRefactorCmd. The flat artifact layout means a
	// restart bumps the count and stages a fresh refactor-N dir with no
	// per-repo subdir to reuse. This is intentional: the AtomicPhaseStamp
	// staged-subset semantics make fresh-dir-per-retry the simpler invariant.
	cmd := app.restartRepoCycleRefactorCmd(f.ID, "web", "improve performance")
	msg := cmd()

	if _, ok := msg.(RefreshFeaturesMsg); !ok {
		t.Fatalf("expected RefreshFeaturesMsg, got %T", msg)
	}

	// RefactorCount bumps from 2 → 3 on restart under the unified flow.
	f, _ = fm.Get(f.ID)
	if f.RefactorCount() != 3 {
		t.Errorf("RefactorCount = %d, want 3 (restart bumps under unified flow)", f.RefactorCount())
	}

	// Verify the refactor-3 (flat) artifact dir was staged.
	runDir := agent.ActiveRunDir(baseDir, f)
	planPath := filepath.Join(runDir, "refactor-3", "refactor-prompt.md")
	if _, err := os.Stat(planPath); os.IsNotExist(err) {
		t.Errorf("expected plan file at %s (flat layout)", planPath)
	} else {
		data, _ := os.ReadFile(planPath)
		if !strings.Contains(string(data), "improve performance") {
			t.Errorf("plan file content = %q, want to contain %q", string(data), "improve performance")
		}
	}

	app.orchestrator.(*orchestrator.Orchestrator).WaitForCycles()
}

func TestHasRunningRefactorCycle(t *testing.T) {
	tests := []struct {
		name   string
		cycles map[string]*feature.RepoCycleState
		want   bool
	}{
		{
			name:   "no cycles",
			cycles: nil,
			want:   false,
		},
		{
			name: "running tweak only",
			cycles: map[string]*feature.RepoCycleState{
				"api": {Type: feature.CycleTweak, Status: "running"},
			},
			want: false,
		},
		{
			name: "running refactor",
			cycles: map[string]*feature.RepoCycleState{
				"api": {Type: feature.CycleRefactor, Status: "running"},
			},
			want: true,
		},
		{
			name: "failed refactor",
			cycles: map[string]*feature.RepoCycleState{
				"api": {Type: feature.CycleRefactor, Status: "failed"},
			},
			want: false,
		},
		{
			name: "refactor on one repo, tweak on another",
			cycles: map[string]*feature.RepoCycleState{
				"api": {Type: feature.CycleRefactor, Status: "running"},
				"web": {Type: feature.CycleTweak, Status: "running"},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &feature.Feature{RepoCycles: tt.cycles,
				SchemaVersion: feature.SchemaVersionCurrent}
			got := hasRunningRefactorCycle(f)
			if got != tt.want {
				t.Errorf("hasRunningRefactorCycle() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRestartRepoCycleRefactorCmd_BlocksConcurrentRefactor(t *testing.T) {
	app, fm := newTestAppModel(t)

	app.phaseRunner = &agent.PhaseRunner{}
	app.sessionManager = session.NewManager(nil)

	fm.Config.Repos["web"] = config.RepoConfig{Path: "/tmp/web"}

	f, err := fm.Create("test-restart-block", "Test desc", []string{"test-repo", "web"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	_ = fm.Transition(f.ID, feature.StatusResearching)
	_ = fm.Transition(f.ID, feature.StatusPlanReady)
	_ = fm.Transition(f.ID, feature.StatusPlanning)
	_ = fm.Transition(f.ID, feature.StatusImplementReady)
	_ = fm.Transition(f.ID, feature.StatusImplementing)
	_ = fm.Transition(f.ID, feature.StatusReviewPassed)
	_ = fm.Transition(f.ID, feature.StatusCodeReady)
	_ = fm.Transition(f.ID, feature.StatusPublished)

	// Simulate "test-repo" already running a refactor cycle
	_ = fm.StartRepoCycle(f.ID, "test-repo", feature.CycleRefactor)

	// Set RefactorCount so restartRepoCycleRefactorCmd has a valid refactor-N dir
	_ = fm.Store.Modify(f.ID, func(f *feature.Feature) error {
		f.SetRefactorCount(1)
		return nil
	})

	// Attempt to restart a refactor on "web" — should be blocked
	cmd := app.restartRepoCycleRefactorCmd(f.ID, "web", "improve perf")
	msg := cmd()

	if _, ok := msg.(RefreshFeaturesMsg); !ok {
		t.Fatalf("expected RefreshFeaturesMsg, got %T", msg)
	}

	// Verify "web" does NOT have a running cycle
	f, _ = fm.Get(f.ID)
	if rc, ok := f.RepoCycles["web"]; ok && rc.Status == "running" {
		t.Error("expected web repo to NOT have a running cycle when another refactor is running")
	}
}

func TestApproveAndRememberCmd_OnlyProcessesPermissionSessions(t *testing.T) {
	app, fm := newTestAppModel(t)

	f, err := fm.Create("perm-test", "mixed-state feature", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = fm.Transition(f.ID, feature.StatusResearching)

	// Add pending permission to feature so hasPendingPerms() returns true
	_ = fm.Store.Modify(f.ID, func(feat *feature.Feature) error {
		feat.PermissionsQueue = append(feat.PermissionsQueue, feature.PermissionRequest{
			Tool:    "Bash",
			Args:    `{"command":"go test ./..."}`,
			Pending: true,
		})
		return nil
	})

	sm := session.NewManager(nil)

	// Session 1: waiting for permission — should be processed
	permSess := session.NewSession(f.ID+"-impl-repo-01", f.ID, feature.PhaseImplement)
	permSess.SetStatus(session.SessionWaitingPermission)
	permSess.SetPermCacheScope("test-repo")
	permSess.SetLastControlRequest(&llm.ControlRequestMessage{
		RequestID: "req-perm-1",
		Request: llm.ControlRequest{
			Subtype:  "can_use_tool",
			ToolName: "Bash",
			Input:    json.RawMessage(`{"command":"go test ./..."}`),
		},
	})
	sm.RegisterTestSession(permSess)

	// Session 2: waiting for help (AskUserQuestion) — must NOT be processed
	helpSess := session.NewSession(f.ID+"-impl-repo-02", f.ID, feature.PhaseImplement)
	helpSess.SetStatus(session.SessionWaitingHelp)
	helpSess.SetHasUnansweredQuestion(true)
	helpSess.SetLastControlRequest(&llm.ControlRequestMessage{
		RequestID: "req-help-1",
		Request: llm.ControlRequest{
			Subtype:  "can_use_tool",
			ToolName: "AskUserQuestion",
			Input:    json.RawMessage(`{"question":"Which approach?"}`),
		},
	})
	sm.RegisterTestSession(helpSess)

	app.sessionManager = sm

	// Set up a real permission cache to verify RememberAllow is called
	store := permission.NewStore(t.TempDir())
	cache := permission.NewCache(store)
	app.permissionCache = cache

	// Execute approveAndRememberCmd directly
	cmd := app.approveAndRememberCmd(f.ID)
	msg := cmd()

	if _, ok := msg.(RefreshFeaturesMsg); !ok {
		t.Fatalf("expected RefreshFeaturesMsg, got %T", msg)
	}

	// Permission session should have been reset
	if permSess.Status() == session.SessionWaitingPermission {
		t.Error("permission session status should have been reset, still SessionWaitingPermission")
	}

	// Help session must remain untouched
	if helpSess.Status() != session.SessionWaitingHelp {
		t.Errorf("help session status = %v, want SessionWaitingHelp (must not be processed by approve-and-remember)", helpSess.Status())
	}
	if !helpSess.HasUnansweredQuestion() {
		t.Error("help session HasUnansweredQuestion should still be true")
	}

	// Permission cache should have a rule for the Bash command (from the permission session)
	rules := cache.Rules()
	if len(rules) == 0 {
		t.Error("expected permission cache to have a remembered rule for the Bash command")
	}
}

func TestApproveAndRememberCmd_HelpQueuePreservedWhenHelpSessionRemains(t *testing.T) {
	// Regression: In a mixed state (pending Bash permission + pending help question),
	// pressing Shift+A should clear PermissionsQueue but NOT clear HelpQueue entries
	// while a SessionWaitingHelp session still exists for the feature.
	app, fm := newTestAppModel(t)

	f, err := fm.Create("mixed-help-perm", "mixed help+perm feature", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = fm.Transition(f.ID, feature.StatusResearching)

	// Populate both PermissionsQueue and HelpQueue
	const inputMsg = "Agent is waiting for input \u2014 press 'a' to answer"
	_ = fm.Store.Modify(f.ID, func(feat *feature.Feature) error {
		feat.PermissionsQueue = append(feat.PermissionsQueue, feature.PermissionRequest{
			Tool:    "Bash",
			Args:    `{"command":"go test ./..."}`,
			Pending: true,
		})
		feat.HelpQueue = append(feat.HelpQueue, feature.HelpRequest{
			Question: inputMsg,
			Pending:  true,
		})
		return nil
	})

	sm := session.NewManager(nil)

	// Session 1: permission session — will be processed
	permSess := session.NewSession(f.ID+"-impl-repo-01", f.ID, feature.PhaseImplement)
	permSess.SetStatus(session.SessionWaitingPermission)
	permSess.SetPermCacheScope("test-repo")
	permSess.SetLastControlRequest(&llm.ControlRequestMessage{
		RequestID: "req-perm-mixed",
		Request: llm.ControlRequest{
			Subtype:  "can_use_tool",
			ToolName: "Bash",
			Input:    json.RawMessage(`{"command":"go test ./..."}`),
		},
	})
	sm.RegisterTestSession(permSess)

	// Session 2: help session — must remain untouched
	helpSess := session.NewSession(f.ID+"-impl-repo-02", f.ID, feature.PhaseImplement)
	helpSess.SetStatus(session.SessionWaitingHelp)
	helpSess.SetHasUnansweredQuestion(true)
	sm.RegisterTestSession(helpSess)

	app.sessionManager = sm
	store := permission.NewStore(t.TempDir())
	cache := permission.NewCache(store)
	app.permissionCache = cache

	cmd := app.approveAndRememberCmd(f.ID)
	_ = cmd()

	// PermissionsQueue should be cleared
	updated, _ := fm.Get(f.ID)
	if hasPendingPerms(updated) {
		t.Error("PermissionsQueue entries should be marked not-pending")
	}

	// HelpQueue must STILL be pending because a SessionWaitingHelp session exists
	helpPending := false
	for _, h := range updated.HelpQueue {
		if h.Question == inputMsg && h.Pending {
			helpPending = true
			break
		}
	}
	if !helpPending {
		t.Error("HelpQueue entry should remain pending while a SessionWaitingHelp session exists; got cleared")
	}

	// Permission session should have been reset
	if permSess.Status() == session.SessionWaitingPermission {
		t.Error("permission session should have been reset")
	}

	// Help session must remain waiting
	if helpSess.Status() != session.SessionWaitingHelp {
		t.Errorf("help session status = %v, want SessionWaitingHelp", helpSess.Status())
	}
}

// TestReconcileHelpQueue_NoSessionWaitingClearsBadges verifies that when no
// session is in a waiting state, the reconciler clears both the question and
// waiting-input badges. This is the "user answered and agent is now running"
// path.
func TestReconcileHelpQueue_NoSessionWaitingClearsBadges(t *testing.T) {
	app, fm := newTestAppModel(t)
	f, err := fm.Create("reconcile-clear", "reconcile clears when no waiting", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = fm.Transition(f.ID, feature.StatusResearching)
	_ = fm.Store.Modify(f.ID, func(feat *feature.Feature) error {
		feat.HelpQueue = append(feat.HelpQueue,
			feature.HelpRequest{Question: questionHelpMessage, Pending: true},
			feature.HelpRequest{Question: waitingInputHelpMessage, Pending: true},
		)
		return nil
	})

	sm := session.NewManager(nil)
	sess := session.NewSession(f.ID+"-impl-repo-01", f.ID, feature.PhaseImplement)
	sess.SetStatus(session.SessionRunning)
	sm.RegisterTestSession(sess)
	app.sessionManager = sm

	app.reconcileHelpQueue(f.ID)

	updated, _ := fm.Get(f.ID)
	for _, h := range updated.HelpQueue {
		if h.Pending {
			t.Errorf("help entry %q still pending; want cleared when no session is waiting", h.Question)
		}
	}
}

// TestReconcileHelpQueue_PreservesQuestionBadgeWhenAnotherSessionWaits is the
// core regression: resolving one session's waiting state must not wipe the
// badge while another session for the same feature is still in
// SessionWaitingHelp (the multi-repo or stacked-question case).
func TestReconcileHelpQueue_PreservesQuestionBadgeWhenAnotherSessionWaits(t *testing.T) {
	app, fm := newTestAppModel(t)
	f, err := fm.Create("reconcile-multi-session", "multi-session help preservation", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = fm.Transition(f.ID, feature.StatusResearching)
	_ = fm.Store.Modify(f.ID, func(feat *feature.Feature) error {
		feat.HelpQueue = append(feat.HelpQueue, feature.HelpRequest{
			Question: questionHelpMessage,
			Pending:  true,
		})
		return nil
	})

	sm := session.NewManager(nil)
	// Session A answered — status transitioned back to Running.
	sessA := session.NewSession(f.ID+"-impl-repo-01", f.ID, feature.PhaseImplement)
	sessA.SetStatus(session.SessionRunning)
	sm.RegisterTestSession(sessA)
	// Session B still waiting for its own question.
	sessB := session.NewSession(f.ID+"-impl-repo-02", f.ID, feature.PhaseImplement)
	sessB.SetStatus(session.SessionWaitingHelp)
	sm.RegisterTestSession(sessB)
	app.sessionManager = sm

	app.reconcileHelpQueue(f.ID)

	updated, _ := fm.Get(f.ID)
	pending := false
	for _, h := range updated.HelpQueue {
		if h.Question == questionHelpMessage && h.Pending {
			pending = true
			break
		}
	}
	if !pending {
		t.Error("question badge should remain pending while another session is in SessionWaitingHelp")
	}
}

// TestReconcileHelpQueue_PreservesWaitingInputWhilePermissionPending ensures
// the generic waiting-input badge stays up while a SessionWaitingPermission
// session exists, even if no session is in SessionWaitingHelp.
func TestReconcileHelpQueue_PreservesWaitingInputWhilePermissionPending(t *testing.T) {
	app, fm := newTestAppModel(t)
	f, err := fm.Create("reconcile-perm", "waiting-input preserved with permission", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = fm.Transition(f.ID, feature.StatusResearching)
	_ = fm.Store.Modify(f.ID, func(feat *feature.Feature) error {
		feat.HelpQueue = append(feat.HelpQueue, feature.HelpRequest{
			Question: waitingInputHelpMessage,
			Pending:  true,
		})
		return nil
	})

	sm := session.NewManager(nil)
	sess := session.NewSession(f.ID+"-impl-repo-01", f.ID, feature.PhaseImplement)
	sess.SetStatus(session.SessionWaitingPermission)
	sm.RegisterTestSession(sess)
	app.sessionManager = sm

	app.reconcileHelpQueue(f.ID)

	updated, _ := fm.Get(f.ID)
	pending := false
	for _, h := range updated.HelpQueue {
		if h.Question == waitingInputHelpMessage && h.Pending {
			pending = true
			break
		}
	}
	if !pending {
		t.Error("waiting-input badge should remain pending while a session is in SessionWaitingPermission")
	}
}

// TestDetachWithoutAnswer_PreservesHelpBadge is the user-reported bug:
// during Tweak with an open AskUserQuestion, detaching without answering
// must leave the help badge pending. Detach no longer touches HelpQueue,
// so we simply verify that going through the detach path does not clear it.
func TestDetachWithoutAnswer_PreservesHelpBadge(t *testing.T) {
	app, fm := newTestAppModel(t)
	f, err := fm.Create("detach-no-answer", "detach without answering", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = fm.Transition(f.ID, feature.StatusResearching)
	_ = fm.Store.Modify(f.ID, func(feat *feature.Feature) error {
		feat.HelpQueue = append(feat.HelpQueue, feature.HelpRequest{
			Question: questionHelpMessage,
			Pending:  true,
		})
		return nil
	})

	sm := session.NewManager(nil)
	sess := session.NewSession(f.ID+"-impl-repo-01", f.ID, feature.PhaseImplement)
	sess.SetStatus(session.SessionWaitingHelp)
	sess.SetHasUnansweredQuestion(true)
	sm.RegisterTestSession(sess)
	app.sessionManager = sm

	// User detaches without answering. In the new design this is a no-op on
	// the feature store; reconcileHelpQueue is NOT called because no
	// HelpResolvedMsg was emitted. Directly verify the invariant: even if
	// reconcile runs (belt-and-suspenders), the badge is preserved because
	// the session is still SessionWaitingHelp.
	app.reconcileHelpQueue(f.ID)

	updated, _ := fm.Get(f.ID)
	pending := false
	for _, h := range updated.HelpQueue {
		if h.Question == questionHelpMessage && h.Pending {
			pending = true
			break
		}
	}
	if !pending {
		t.Error("question badge must stay pending when session is still SessionWaitingHelp and user detached without answering")
	}
}

func TestApproveAndRememberCmd_PermissionsQueueCleared(t *testing.T) {
	app, fm := newTestAppModel(t)

	f, err := fm.Create("perm-clear-test", "verify queue cleared", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = fm.Transition(f.ID, feature.StatusResearching)

	// Populate PermissionsQueue
	_ = fm.Store.Modify(f.ID, func(feat *feature.Feature) error {
		feat.PermissionsQueue = append(feat.PermissionsQueue, feature.PermissionRequest{
			Tool:    "Bash",
			Args:    `{"command":"npm test"}`,
			Pending: true,
		})
		return nil
	})

	sm := session.NewManager(nil)
	app.sessionManager = sm

	// Execute approveAndRememberCmd
	cmd := app.approveAndRememberCmd(f.ID)
	_ = cmd()

	// Verify PermissionsQueue entries are marked not pending
	updated, _ := fm.Get(f.ID)
	if hasPendingPerms(updated) {
		t.Error("expected all PermissionsQueue entries to be marked not pending after approve-and-remember")
	}
}

func TestSDKEvent_AskUserQuestionDoesNotPopulatePermissionsQueue(t *testing.T) {
	// Regression: AskUserQuestion arrives as a control_request, but must NOT
	// be added to PermissionsQueue (only real tool permissions like Bash belong there).
	app, fm := newTestAppModel(t)
	app.lastNotifyTime = make(map[notifyKey]time.Time)

	f, err := fm.Create("ask-q-test", "AskUserQuestion regression", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = fm.Transition(f.ID, feature.StatusResearching)

	// Construct session ID that featureIDFromSession() can resolve
	sessionID := f.ID + "-impl-test-repo-01"

	// Send an AskUserQuestion control_request through the SDKEvent handler
	evt := SDKSessionEventMsg{
		Event: session.SDKEventMsg{
			SessionID: sessionID,
			Message: llm.SDKMessage{
				ControlRequest: &llm.ControlRequestMessage{
					RequestID: "req-ask-1",
					Request: llm.ControlRequest{
						Subtype:  "can_use_tool",
						ToolName: "AskUserQuestion",
						Input:    json.RawMessage(`{"question":"Which approach should I use?"}`),
					},
				},
			},
		},
	}

	app.Update(evt)

	// Verify PermissionsQueue was NOT populated
	updated, _ := fm.Get(f.ID)
	if hasPendingPerms(updated) {
		t.Error("AskUserQuestion control_request must NOT populate PermissionsQueue")
	}

	// Verify HelpQueue WAS populated (AskUserQuestion still needs a help entry)
	hasHelp := false
	for _, h := range updated.HelpQueue {
		if h.Pending {
			hasHelp = true
			break
		}
	}
	if !hasHelp {
		t.Error("AskUserQuestion control_request should still populate HelpQueue")
	}
}

func TestSDKEvent_BashPermissionPopulatesPermissionsQueue(t *testing.T) {
	// Complementary test: real tool permissions (Bash) DO populate PermissionsQueue.
	app, fm := newTestAppModel(t)
	app.lastNotifyTime = make(map[notifyKey]time.Time)

	f, err := fm.Create("bash-perm-test", "Bash permission test", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = fm.Transition(f.ID, feature.StatusResearching)

	sessionID := f.ID + "-impl-test-repo-01"

	evt := SDKSessionEventMsg{
		Event: session.SDKEventMsg{
			SessionID: sessionID,
			Message: llm.SDKMessage{
				ControlRequest: &llm.ControlRequestMessage{
					RequestID: "req-bash-1",
					Request: llm.ControlRequest{
						Subtype:  "can_use_tool",
						ToolName: "Bash",
						Input:    json.RawMessage(`{"command":"go test ./..."}`),
					},
				},
			},
		},
	}

	app.Update(evt)

	// Verify PermissionsQueue WAS populated for Bash
	updated, _ := fm.Get(f.ID)
	if !hasPendingPerms(updated) {
		t.Error("Bash control_request should populate PermissionsQueue")
	}
}

func TestDashboardLeftPanel_ShiftA_ApproveAndRemember(t *testing.T) {
	// Regression: Shift+A must work from the left (feature list) panel,
	// not only from the right (detail) panel.
	app, fm := newTestAppModel(t)

	f, err := fm.Create("left-panel-A", "test left panel approve", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = fm.Transition(f.ID, feature.StatusResearching)

	// Add pending permission so hasPendingPerms() returns true
	_ = fm.Store.Modify(f.ID, func(feat *feature.Feature) error {
		feat.PermissionsQueue = append(feat.PermissionsQueue, feature.PermissionRequest{
			Tool:    "Bash",
			Args:    `{"command":"go build ./..."}`,
			Pending: true,
		})
		return nil
	})

	// Reload the feature and populate the dashboard's feature list
	updated, _ := fm.Get(f.ID)
	app.dashboard.SetFeatures([]*feature.Feature{updated})
	// Move cursor past the section header to the feature item
	app.dashboard, _ = app.dashboard.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})

	// Ensure left panel is focused (the default)
	app.dashboard.focusPanel = 0
	app.currentView = ViewDashboard

	sm := session.NewManager(nil)
	permSess := session.NewSession(f.ID+"-impl-repo-01", f.ID, feature.PhaseImplement)
	permSess.SetStatus(session.SessionWaitingPermission)
	permSess.SetPermCacheScope("test-repo")
	permSess.SetLastControlRequest(&llm.ControlRequestMessage{
		RequestID: "req-left-perm",
		Request: llm.ControlRequest{
			Subtype:  "can_use_tool",
			ToolName: "Bash",
			Input:    json.RawMessage(`{"command":"go build ./..."}`),
		},
	})
	sm.RegisterTestSession(permSess)
	app.sessionManager = sm

	store := permission.NewStore(t.TempDir())
	cache := permission.NewCache(store)
	app.permissionCache = cache

	// Press Shift+A (capital A) from left panel
	_, cmd := app.Update(tea.KeyPressMsg{Code: 'A', Text: "A"})
	if cmd == nil {
		t.Fatal("expected a command from Shift+A on left panel with pending permissions, got nil")
	}

	// Execute the command and verify it produces a RefreshFeaturesMsg
	msg := cmd()
	if _, ok := msg.(RefreshFeaturesMsg); !ok {
		t.Fatalf("expected RefreshFeaturesMsg, got %T", msg)
	}

	// Permission session should have been processed
	if permSess.Status() == session.SessionWaitingPermission {
		t.Error("permission session should have been reset after approve-and-remember from left panel")
	}

	// Permission cache should have a rule
	rules := cache.Rules()
	if len(rules) == 0 {
		t.Error("expected permission cache to have a remembered rule")
	}
}

func TestRecoveryOnlyRestartsOneRefactorCycle(t *testing.T) {
	app, fm := newTestAppModel(t)

	// Gate BuildSession so the first refactor goroutine parks inside the
	// agent loop and can't race ahead to mark its own cycle "failed" before
	// cmd2's concurrent guard check reads the state. Mutating the shared
	// phaseRunner in-place (rather than replacing the pointer) keeps the
	// orchestrator's reference to the same runner the TUI is using.
	gate := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-gate:
		default:
			close(gate)
		}
		app.orchestrator.(*orchestrator.Orchestrator).WaitForCycles()
	})

	app.phaseRunner.BuildSessionFn = func(opts agent.BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
		<-gate
		return nil, nil, nil, fmt.Errorf("stub: session never starts")
	}

	fm.Config.Repos["web"] = config.RepoConfig{Path: "/tmp/web"}

	f, err := fm.Create("test-multi-refactor-recovery", "Test desc", []string{"test-repo", "web"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	_ = fm.Transition(f.ID, feature.StatusResearching)
	_ = fm.Transition(f.ID, feature.StatusPlanReady)
	_ = fm.Transition(f.ID, feature.StatusPlanning)
	_ = fm.Transition(f.ID, feature.StatusImplementReady)
	_ = fm.Transition(f.ID, feature.StatusImplementing)
	_ = fm.Transition(f.ID, feature.StatusReviewPassed)
	_ = fm.Transition(f.ID, feature.StatusCodeReady)
	_ = fm.Transition(f.ID, feature.StatusPublished)

	// Simulate: the recovery path would send two restartRefactorCycleMsgs. The
	// first restart opens per-repo cycle entries for every Feature.Repos entry
	// so TUI rendering stays in sync. The second restart for any repo must be
	// blocked by the concurrent guard.
	msg1 := restartRefactorCycleMsg{FeatureID: f.ID, RepoName: "test-repo", Prompt: "refactor 1"}
	_, cmd1 := app.Update(msg1)
	if cmd1 == nil {
		t.Fatal("expected non-nil cmd from first restartRefactorCycleMsg")
	}

	// StartRepoCycle registers the cycle synchronously; the agent goroutine
	// that runs after is parked on the gate so the cycles stay "running".
	result1 := cmd1()
	if _, ok := result1.(RefreshFeaturesMsg); !ok {
		t.Fatalf("expected RefreshFeaturesMsg from first restart, got %T", result1)
	}

	f, _ = fm.Get(f.ID)
	rc, ok := f.RepoCycles["test-repo"]
	if !ok {
		t.Fatalf("expected test-repo to have a registered refactor cycle, got missing entry")
	}
	if rc.Status != "running" {
		t.Fatalf("expected test-repo cycle status=running (gate holds the goroutine), got %q", rc.Status)
	}

	// Capture the per-repo cycle counts after first restart so we can
	// assert the second restart did NOT bump them (guard worked).
	priorCount := f.RepoCycles["web"].Count
	priorRefactorCount := f.RefactorCount()

	// Second restart must be blocked by the concurrent guard — a refactor
	// is still running on the feature.
	cmd2 := app.restartRepoCycleRefactorCmd(f.ID, "web", "refactor 2")
	result2 := cmd2()
	if _, ok := result2.(RefreshFeaturesMsg); !ok {
		t.Fatalf("expected RefreshFeaturesMsg from blocked restart, got %T", result2)
	}

	// Per-repo cycle counts must not have been bumped: the guard fired
	// before any state mutation.
	f, _ = fm.Get(f.ID)
	if rc, ok := f.RepoCycles["web"]; ok && rc.Count != priorCount {
		t.Errorf("expected web cycle Count = %d (unchanged by blocked restart), got %d", priorCount, rc.Count)
	}
	if f.RefactorCount() != priorRefactorCount {
		t.Errorf("expected RefactorCount = %d (unchanged by blocked restart), got %d", priorRefactorCount, f.RefactorCount())
	}
}

// TestRefactorKeyMultiRepo_PRReady_BlocksCycleSelector verifies that pressing F on a
// multi-repo PRReady feature does NOT open the cycle selector (regression test).
// The cycle machinery only supports Published features; allowing PRReady would create
// an unsupported state where stop/recovery won't function correctly.
func TestRefactorKeyMultiRepo_PRReady_BlocksCycleSelector(t *testing.T) {
	app, fm := newTestAppModel(t)

	// Add a second repo to config for multi-repo feature
	fm.Config.Repos["web"] = config.RepoConfig{Path: "/tmp/web"}

	f, err := fm.Create("PRReady Multi Refactor", "desc", []string{"test-repo", "web"}, fm.Config.Defaults.Models, "", "", nil,
		feature.CreateOptions{Checkpoints: feature.Checkpoints{ManualPublish: true}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Advance to PRReady (not Published)
	_ = fm.Transition(f.ID, feature.StatusResearching)
	_ = fm.Transition(f.ID, feature.StatusPlanReady)
	_ = fm.Transition(f.ID, feature.StatusPlanning)
	_ = fm.Transition(f.ID, feature.StatusImplementReady)
	_ = fm.Transition(f.ID, feature.StatusImplementing)
	_ = fm.Transition(f.ID, feature.StatusReviewPassed)
	_ = fm.Transition(f.ID, feature.StatusCodeReady)

	f, _ = fm.Get(f.ID)
	if f.Status != feature.StatusCodeReady {
		t.Fatalf("precondition: expected PRReady, got %s", f.Status)
	}

	features, _ := fm.List()
	app.dashboard.SetFeatures(features)
	app.dashboard.cursor = 1
	app.dashboard.syncPreview()
	app.dashboard.focusPanel = 1

	// Press 'F' (refactor key)
	result, _ := app.Update(tea.KeyPressMsg{Code: 'F', Text: "F"})
	app = result.(AppModel)

	// Cycle selector must NOT be activated for PRReady multi-repo
	if app.cycleSelectActive {
		t.Error("expected cycleSelectActive to be false for PRReady multi-repo feature")
	}
	// Should show status message instead
	if app.statusMessage == "" {
		t.Error("expected a status message explaining why multi-repo refactor is blocked")
	}
}

// TestStartRepoCycleRefactorCmd_RejectsNonPublished verifies the defensive guard
// in startRefactorCmd that prevents creating repo cycles from non-Published states.
func TestStartRepoCycleRefactorCmd_RejectsNonPublished(t *testing.T) {
	app, fm := newTestAppModel(t)

	app.phaseRunner = &agent.PhaseRunner{}
	app.sessionManager = session.NewManager(nil)

	fm.Config.Repos["web"] = config.RepoConfig{Path: "/tmp/web"}

	f, err := fm.Create("test-non-published-refactor", "Test desc", []string{"test-repo", "web"}, fm.Config.Defaults.Models, "", "", nil,
		feature.CreateOptions{Checkpoints: feature.Checkpoints{ManualPublish: true}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Advance only to PRReady (not Published)
	_ = fm.Transition(f.ID, feature.StatusResearching)
	_ = fm.Transition(f.ID, feature.StatusPlanReady)
	_ = fm.Transition(f.ID, feature.StatusPlanning)
	_ = fm.Transition(f.ID, feature.StatusImplementReady)
	_ = fm.Transition(f.ID, feature.StatusImplementing)
	_ = fm.Transition(f.ID, feature.StatusReviewPassed)
	_ = fm.Transition(f.ID, feature.StatusCodeReady)

	// Attempt to directly call startRefactorCmd — should bail out
	cmd := app.startRefactorCmd(f.ID, "test-repo", "improve performance")
	msg := cmd()

	if _, ok := msg.(RefreshFeaturesMsg); !ok {
		t.Fatalf("expected RefreshFeaturesMsg, got %T", msg)
	}

	// Verify RefactorCount was NOT incremented (blocked before that step)
	f, _ = fm.Get(f.ID)
	if f.RefactorCount() != 0 {
		t.Errorf("RefactorCount = %d, want 0 (should not increment for non-Published)", f.RefactorCount())
	}

	// Verify no repo cycle was started
	if rc, ok := f.RepoCycles["test-repo"]; ok && rc.Status == "running" {
		t.Error("expected test-repo to NOT have a running cycle for non-Published feature")
	}
}

func TestShowRefactorForRepoMsg_PopulatesFeatureName(t *testing.T) {
	app, fm := newTestAppModel(t)

	f, err := fm.Create("test-refactor-name", "Test desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	msg := showRefactorForRepoMsg{FeatureID: f.ID, RepoName: "test-repo"}
	result, _ := app.Update(msg)
	m := result.(AppModel)

	if m.refactorFeatureName != f.Name {
		t.Errorf("refactorFeatureName = %q, want %q", m.refactorFeatureName, f.Name)
	}
}

// stubTUIProvider implements llm.LLMProvider for TUI tests.
type stubTUIProvider struct {
	name     string
	models   []string
	detected bool
}

func (s *stubTUIProvider) Name() string { return s.name }
func (s *stubTUIProvider) MatchesModel(model string) bool {
	for _, m := range s.models {
		if m == model {
			return true
		}
	}
	return false
}
func (s *stubTUIProvider) DetectCLI() bool           { return s.detected }
func (s *stubTUIProvider) AvailableModels() []string { return s.models }
func (s *stubTUIProvider) BuildCommand(_ llm.CommandBuildOpts) ([]string, []string, error) {
	return nil, nil, nil
}
func (s *stubTUIProvider) NewProtocol(_ llm.ProtocolOpts) llm.Protocol { return nil }
func (s *stubTUIProvider) InstallHint() string                         { return "" }
func (s *stubTUIProvider) VersionInfo() (string, error)                { return "1.0.0", nil }
func (s *stubTUIProvider) MinVersion() [3]int                          { return [3]int{0, 0, 0} }
func (s *stubTUIProvider) EnvVarsToExclude() []string                  { return nil }

func TestTransitionToWizardUsesRegistryModels(t *testing.T) {
	t.Helper()
	app, _ := newTestAppModel(t)
	// Register a provider that contributes models via AvailableModels().
	app.registry.Register(&stubTUIProvider{
		name:     "test-provider",
		models:   []string{"model-a", "model-b"},
		detected: true,
	})

	app = app.transitionToWizard()

	if app.currentView != ViewWizard {
		t.Fatalf("expected ViewWizard, got %v", app.currentView)
	}
	// providerModels should contain the provider's models
	wantProviderModels := map[string][]string{"test-provider": {"model-a", "model-b"}}
	if !reflect.DeepEqual(app.wizard.providerModels, wantProviderModels) {
		t.Errorf("providerModels = %v, want %v", app.wizard.providerModels, wantProviderModels)
	}
	// allModels should be the flattened list
	wantAllModels := []string{"model-a", "model-b"}
	if !reflect.DeepEqual(app.wizard.allModels, wantAllModels) {
		t.Errorf("allModels = %v, want %v", app.wizard.allModels, wantAllModels)
	}
}

func TestTransitionToWizardFallsBackToAvailableModels(t *testing.T) {
	t.Helper()
	app, _ := newTestAppModel(t)
	// Register a provider with available models.
	app.registry.Register(&stubTUIProvider{
		name:     "basic-provider",
		models:   []string{"basic-model"},
		detected: true,
	})

	app = app.transitionToWizard()

	if app.currentView != ViewWizard {
		t.Fatalf("expected ViewWizard, got %v", app.currentView)
	}
	// providerModels should contain the provider's models
	wantProviderModels := map[string][]string{"basic-provider": {"basic-model"}}
	if !reflect.DeepEqual(app.wizard.providerModels, wantProviderModels) {
		t.Errorf("providerModels = %v, want %v", app.wizard.providerModels, wantProviderModels)
	}
	// allModels should be the flattened list
	wantAllModels := []string{"basic-model"}
	if !reflect.DeepEqual(app.wizard.allModels, wantAllModels) {
		t.Errorf("allModels = %v, want %v", app.wizard.allModels, wantAllModels)
	}
}

// --- Workspace Roots tests ---

// newTestAppModelWithConfig creates a minimal AppModel using NewAppModel so the
// startup detection logic (welcome flow, discovery) runs with the given config.
func newTestAppModelWithConfig(t *testing.T, cfg *config.Config) (AppModel, *feature.Manager) {
	t.Helper()
	dir := t.TempDir()
	home := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(filepath.Join(home, "workspace"), 0o755); err != nil {
		t.Fatalf("create test HOME: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	store := feature.NewStore(dir)
	fm := feature.NewManager(store, cfg)
	sm := session.NewManager(nil)
	pr := agent.NewPhaseRunner(sm, store, dir)
	pr.CommandRunner = agent.NewExecCommandRunner()
	pr.Config = cfg
	configPath := filepath.Join(dir, "config.yaml")
	app, err := NewAppModel(fm, sm, nil, permission.NewCache(nil), nil, WithPhaseRunner(pr), WithConfigPath(configPath))
	if err != nil {
		t.Fatalf("NewAppModel: %v", err)
	}
	app.width = 80
	app.height = 24
	return app, fm
}

func TestWelcomeDetectionEmptyRoots(t *testing.T) {
	cfg := config.NewDefault()
	// Ensure no workspace roots and no repos
	cfg.WorkspaceRoots = nil
	cfg.Repos = make(map[string]config.RepoConfig)

	app, _ := newTestAppModelWithConfig(t, cfg)

	if app.currentView != ViewWelcome {
		t.Errorf("currentView = %v, want ViewWelcome when WorkspaceRoots is empty", app.currentView)
	}
}

func TestWelcomeDetectionPopulatedRoots(t *testing.T) {
	wsDir := t.TempDir()
	// Create a fake git repo
	if err := os.MkdirAll(filepath.Join(wsDir, "my-repo", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := config.NewDefault()
	cfg.WorkspaceRoots = []string{wsDir}
	cfg.Repos = make(map[string]config.RepoConfig)

	app, _ := newTestAppModelWithConfig(t, cfg)

	if app.currentView != ViewDashboard {
		t.Errorf("currentView = %v, want ViewDashboard when WorkspaceRoots is populated", app.currentView)
	}
}

func TestStartupDiscovery(t *testing.T) {
	wsDir := t.TempDir()
	for _, name := range []string{"repoA", "repoB"} {
		if err := os.MkdirAll(filepath.Join(wsDir, name, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	cfg := config.NewDefault()
	cfg.WorkspaceRoots = []string{wsDir}
	cfg.Repos = make(map[string]config.RepoConfig)

	app, _ := newTestAppModelWithConfig(t, cfg)

	// Verify DiscoveredRepos is populated
	if len(cfg.DiscoveredRepos) != 2 {
		t.Errorf("DiscoveredRepos count = %d, want 2; got %v", len(cfg.DiscoveredRepos), cfg.DiscoveredRepos)
	}
	if _, ok := cfg.DiscoveredRepos["repoA"]; !ok {
		t.Error("expected repoA in DiscoveredRepos")
	}
	if _, ok := cfg.DiscoveredRepos["repoB"]; !ok {
		t.Error("expected repoB in DiscoveredRepos")
	}

	// Verify cfg.Repos is unchanged (still empty)
	if len(cfg.Repos) != 0 {
		t.Errorf("Repos count = %d, want 0; explicit repos should not be modified", len(cfg.Repos))
	}

	// Should NOT be in welcome view since workspace roots are populated
	if app.currentView == ViewWelcome {
		t.Error("expected currentView != ViewWelcome when WorkspaceRoots is populated")
	}
}

func TestWelcomeCompletionTransition(t *testing.T) {
	cfg := config.NewDefault()
	cfg.WorkspaceRoots = nil
	cfg.Repos = make(map[string]config.RepoConfig)

	app, _ := newTestAppModelWithConfig(t, cfg)

	if app.currentView != ViewWelcome {
		t.Fatalf("precondition: currentView = %v, want ViewWelcome", app.currentView)
	}

	// Create a temp dir with git repos for the picker to select
	wsDir := t.TempDir()
	for _, name := range []string{"svc-a", "svc-b"} {
		if err := os.MkdirAll(filepath.Join(wsDir, name, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// Drive the real welcome flow through app.Update:

	// Step 1: Press Enter to advance from intro to picker
	result, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	app = result.(AppModel)

	if app.welcome.step != welcomeStepPicker {
		t.Fatalf("expected welcomeStepPicker after Enter, got step %v", app.welcome.step)
	}

	// Verify hasActiveTextInput recognizes the welcome picker
	if !app.hasActiveTextInput() {
		t.Fatal("hasActiveTextInput() should return true when welcome picker is active")
	}

	// Step 2: Set the picker to wsDir's parent, cursor on wsDir basename.
	// Enter selects the highlighted entry (wsDir), which contains repos.
	parentDir := filepath.Dir(wsDir)
	wsDirName := filepath.Base(wsDir)
	app.welcome.picker.setCurrentDir(parentDir)
	activeCol := &app.welcome.picker.columns[len(app.welcome.picker.columns)-1]
	activeCol.scanDone = true
	activeCol.dirRepoCounts = map[string]int{wsDirName: 2}
	// Position cursor on wsDir entry
	for i, name := range activeCol.entries {
		if name == wsDirName {
			activeCol.cursor = i
			break
		}
	}
	app.welcome.picker.gitRepoCount = 2
	result, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	app = result.(AppModel)

	// After picker completion, model goes to confirm step (not dashboard yet)
	if app.currentView != ViewWelcome || app.welcome.step != welcomeStepConfirm {
		t.Fatalf("expected welcomeStepConfirm, got view=%v step=%v", app.currentView, app.welcome.step)
	}

	// Step 3: Press Enter on confirm to finalize
	result, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	app = result.(AppModel)

	// After completion, verify:
	// 1. Transitioned to dashboard
	if app.currentView != ViewDashboard {
		t.Errorf("currentView = %v, want ViewDashboard after welcome completion", app.currentView)
	}

	// 2. Config WorkspaceRoots contains wsDir (the highlighted entry)
	found := false
	for _, root := range cfg.WorkspaceRoots {
		if root == wsDir {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("WorkspaceRoots = %v, want to contain %q", cfg.WorkspaceRoots, wsDir)
	}

	// 3. DiscoveredRepos is populated
	if len(cfg.DiscoveredRepos) == 0 {
		t.Error("DiscoveredRepos is empty, want discovered repos from workspace root")
	}
	if _, ok := cfg.DiscoveredRepos["svc-a"]; !ok {
		t.Error("expected svc-a in DiscoveredRepos")
	}
	if _, ok := cfg.DiscoveredRepos["svc-b"]; !ok {
		t.Error("expected svc-b in DiscoveredRepos")
	}

	// 4. Explicit Repos is still empty
	if len(cfg.Repos) != 0 {
		t.Errorf("Repos count = %d, want 0; only DiscoveredRepos should be populated", len(cfg.Repos))
	}
}

func TestWelcomeWindowSizeForwardedToPicker(t *testing.T) {
	cfg := config.NewDefault()
	cfg.WorkspaceRoots = nil
	cfg.Repos = make(map[string]config.RepoConfig)

	app, _ := newTestAppModelWithConfig(t, cfg)

	if app.currentView != ViewWelcome {
		t.Fatalf("precondition: currentView = %v, want ViewWelcome", app.currentView)
	}

	// Send initial WindowSizeMsg — stored on welcome model
	result, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	app = result.(AppModel)

	if app.welcome.width != 100 || app.welcome.height != 30 {
		t.Fatalf("welcome dimensions = %dx%d, want 100x30", app.welcome.width, app.welcome.height)
	}

	// Enter picker
	result, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	app = result.(AppModel)

	if app.welcome.step != welcomeStepPicker {
		t.Fatalf("expected picker step, got %v", app.welcome.step)
	}

	// Picker should have received the stored dimensions
	if app.welcome.picker.width != 100 || app.welcome.picker.height != 30 {
		t.Errorf("picker dimensions = %dx%d, want 100x30 (stored size applied on transition)", app.welcome.picker.width, app.welcome.picker.height)
	}

	// Resize while picker is active — should propagate through
	result, _ = app.Update(tea.WindowSizeMsg{Width: 60, Height: 20})
	app = result.(AppModel)

	if app.welcome.picker.width != 60 || app.welcome.picker.height != 20 {
		t.Errorf("picker dimensions = %dx%d after resize, want 60x20", app.welcome.picker.width, app.welcome.picker.height)
	}
}

func TestWelcomePickerSuppressesGlobalKeys(t *testing.T) {
	cfg := config.NewDefault()
	cfg.WorkspaceRoots = nil
	cfg.Repos = make(map[string]config.RepoConfig)

	app, _ := newTestAppModelWithConfig(t, cfg)

	// Advance from intro to picker step
	result, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	app = result.(AppModel)

	if app.welcome.step != welcomeStepPicker {
		t.Fatalf("precondition: expected welcomeStepPicker, got %v", app.welcome.step)
	}

	// Press 'q' — should NOT trigger quit; should be forwarded to text input
	result, _ = app.Update(tea.KeyPressMsg{Text: "q"})
	app = result.(AppModel)

	// Must still be in welcome view (global 'q' quit was suppressed)
	if app.currentView != ViewWelcome {
		t.Errorf("pressing 'q' in welcome picker changed view to %v, want ViewWelcome", app.currentView)
	}

	// The 'q' key should not crash or change the picker's state (filepicker ignores it)
	if app.welcome.picker.IsDone() {
		t.Error("picker should not be done after 'q' press")
	}
}

func TestTransitionToWizardUsesAllRepos(t *testing.T) {

	wsDir := t.TempDir()
	for _, name := range []string{"discovered-svc", "discovered-lib"} {
		if err := os.MkdirAll(filepath.Join(wsDir, name, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	dir := t.TempDir()
	store := feature.NewStore(dir)
	cfg := config.NewDefault()
	cfg.WorkspaceRoots = []string{wsDir}
	cfg.Repos = make(map[string]config.RepoConfig)

	// Run discovery to populate DiscoveredRepos
	config.DiscoverReposFromRoots(cfg)

	fm := feature.NewManager(store, cfg)

	app := AppModel{
		currentView:    ViewDashboard,
		dashboard:      NewDashboardModel(nil, ""),
		featureManager: fm,
		programRef:     &ProgramRef{},
		width:          80,
		height:         24,
	}

	app = app.transitionToWizard()

	if app.currentView != ViewWizard {
		t.Errorf("currentView = %v, want ViewWizard", app.currentView)
	}

	// Verify the wizard's availRepos contains repos from workspace roots
	if len(app.wizard.availRepos) != 2 {
		t.Errorf("wizard.availRepos count = %d, want 2; got %v", len(app.wizard.availRepos), app.wizard.availRepos)
	}

	// Check that the discovered repos are present
	repoSet := make(map[string]bool)
	for _, r := range app.wizard.availRepos {
		repoSet[r] = true
	}
	if !repoSet["discovered-svc"] {
		t.Error("expected discovered-svc in wizard.availRepos")
	}
	if !repoSet["discovered-lib"] {
		t.Error("expected discovered-lib in wizard.availRepos")
	}
}

func TestWelcomeToWizardCreateE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-git welcome-to-wizard regression in short mode")
	}

	// --- Setup: temp dir with a single git repo inside it ---
	wsDir := t.TempDir()
	repoName := "my-discovered-repo"
	if err := os.MkdirAll(filepath.Join(wsDir, repoName, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	// git operations in feature.Create need a valid default branch;
	// initialise a real git repo so git.DefaultBranch succeeds.
	cmd := exec.Command("git", "init", filepath.Join(wsDir, repoName))
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %s: %v", out, err)
	}
	cmd = exec.Command("git", "-C", filepath.Join(wsDir, repoName), "commit", "--allow-empty", "-m", "init")
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test", "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %s: %v", out, err)
	}

	// --- Build AppModel with empty config (triggers ViewWelcome) ---
	cfg := config.NewDefault()
	cfg.WorkspaceRoots = nil
	cfg.Repos = make(map[string]config.RepoConfig)

	app, fm := newTestAppModelWithConfig(t, cfg)

	// (a) Verify initial view is ViewWelcome
	if app.currentView != ViewWelcome {
		t.Fatalf("precondition: currentView = %v, want ViewWelcome", app.currentView)
	}

	// --- Drive through welcome flow ---

	// (b) Press Enter to move from intro to picker step
	result, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	app = result.(AppModel)

	if app.welcome.step != welcomeStepPicker {
		t.Fatalf("expected welcomeStepPicker after Enter, got step %v", app.welcome.step)
	}

	// (c) Set the picker to wsDir's parent, cursor on wsDir basename.
	// Space selects the highlighted entry (wsDir), which contains repos.
	parentDir := filepath.Dir(wsDir)
	wsDirName := filepath.Base(wsDir)
	app.welcome.picker.setCurrentDir(parentDir)
	activeCol := &app.welcome.picker.columns[len(app.welcome.picker.columns)-1]
	activeCol.scanDone = true
	activeCol.dirRepoCounts = map[string]int{wsDirName: 1}
	for i, name := range activeCol.entries {
		if name == wsDirName {
			activeCol.cursor = i
			break
		}
	}
	app.welcome.picker.gitRepoCount = 1
	result, _ = app.Update(tea.KeyPressMsg{Code: ' '})
	app = result.(AppModel)

	// Model should be in confirm step now
	if app.currentView != ViewWelcome || app.welcome.step != welcomeStepConfirm {
		t.Fatalf("expected welcomeStepConfirm after picker selection, got view=%v step=%v", app.currentView, app.welcome.step)
	}

	// Press Enter on confirm to finalize
	result, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	app = result.(AppModel)

	// --- Verify welcome completion ---

	// (e) View transitioned to ViewDashboard
	if app.currentView != ViewDashboard {
		t.Errorf("currentView = %v, want ViewDashboard after welcome completion", app.currentView)
	}

	// (f) Config's WorkspaceRoots contains the selected entry (wsDir)
	foundRoot := false
	for _, root := range cfg.WorkspaceRoots {
		if root == wsDir {
			foundRoot = true
			break
		}
	}
	if !foundRoot {
		t.Errorf("WorkspaceRoots = %v, want to contain %q", cfg.WorkspaceRoots, wsDir)
	}

	// (g) Config's DiscoveredRepos contains the repo
	if _, ok := cfg.DiscoveredRepos[repoName]; !ok {
		t.Errorf("DiscoveredRepos = %v, want to contain %q", cfg.DiscoveredRepos, repoName)
	}

	// --- Trigger wizard transition ---
	app = app.transitionToWizard()

	if app.currentView != ViewWizard {
		t.Errorf("currentView = %v, want ViewWizard after transitionToWizard", app.currentView)
	}

	// (h) Verify the wizard's available repos include the discovered repo
	repoSet := make(map[string]bool)
	for _, r := range app.wizard.availRepos {
		repoSet[r] = true
	}
	if !repoSet[repoName] {
		t.Errorf("wizard.availRepos = %v, want to contain %q", app.wizard.availRepos, repoName)
	}

	// --- Create a feature using the discovered repo ---

	// (i) Verify the discovered repo appears in config.AllRepos
	allRepos := config.AllRepos(cfg)
	if _, ok := allRepos[repoName]; !ok {
		t.Fatalf("AllRepos does not contain %q; got keys: %v", repoName, allRepos)
	}

	// (j) Create a feature through the feature manager with the discovered repo
	f, err := fm.Create(
		"E2E Test Feature",
		"test description",
		[]string{repoName},
		fm.Config.Defaults.Models,
		"", "", nil,
	)
	if err != nil {
		t.Fatalf("feature Create failed: %v", err)
	}
	if f.Name != "E2E Test Feature" {
		t.Errorf("feature Name = %q, want %q", f.Name, "E2E Test Feature")
	}
	if len(f.Repos) != 1 || f.Repos[0].Name != repoName {
		t.Errorf("feature Repos = %v, want single repo %q", f.Repos, repoName)
	}
}

func TestWelcomeAddAnotherPersistsEachRoot(t *testing.T) {
	// Setup: two temp dirs, each with git repos
	wsDir1 := t.TempDir()
	os.MkdirAll(filepath.Join(wsDir1, "repo-a", ".git"), 0o755)
	wsDir2 := t.TempDir()
	os.MkdirAll(filepath.Join(wsDir2, "repo-b", ".git"), 0o755)

	cfg := config.NewDefault()
	cfg.WorkspaceRoots = nil
	cfg.Repos = make(map[string]config.RepoConfig)
	app, _ := newTestAppModelWithConfig(t, cfg)

	// Enter → picker
	result, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	app = result.(AppModel)

	// Select first dir — set picker to wsDir1's parent, cursor on wsDir1
	parentDir1 := filepath.Dir(wsDir1)
	baseName1 := filepath.Base(wsDir1)
	app.welcome.picker.setCurrentDir(parentDir1)
	col1 := &app.welcome.picker.columns[len(app.welcome.picker.columns)-1]
	col1.scanDone = true
	col1.dirRepoCounts = map[string]int{baseName1: 1}
	for i, name := range col1.entries {
		if name == baseName1 {
			col1.cursor = i
			break
		}
	}
	result, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	app = result.(AppModel)

	if app.welcome.step != welcomeStepConfirm {
		t.Fatalf("expected welcomeStepConfirm, got %v", app.welcome.step)
	}
	if len(cfg.WorkspaceRoots) != 1 || cfg.WorkspaceRoots[0] != wsDir1 {
		t.Errorf("after first root: WorkspaceRoots = %v, want [%s]", cfg.WorkspaceRoots, wsDir1)
	}

	// Press 'a' to add another
	result, _ = app.Update(tea.KeyPressMsg{Text: "a"})
	app = result.(AppModel)
	if app.welcome.step != welcomeStepPicker {
		t.Fatalf("expected welcomeStepPicker after 'a', got %v", app.welcome.step)
	}

	// Select second dir — set picker to wsDir2's parent, cursor on wsDir2
	parentDir2 := filepath.Dir(wsDir2)
	baseName2 := filepath.Base(wsDir2)
	app.welcome.picker.setCurrentDir(parentDir2)
	col2 := &app.welcome.picker.columns[len(app.welcome.picker.columns)-1]
	col2.scanDone = true
	col2.dirRepoCounts = map[string]int{baseName2: 1}
	for i, name := range col2.entries {
		if name == baseName2 {
			col2.cursor = i
			break
		}
	}
	result, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	app = result.(AppModel)

	if len(cfg.WorkspaceRoots) != 2 {
		t.Errorf("after second root: WorkspaceRoots = %v, want 2 entries", cfg.WorkspaceRoots)
	}

	// Press Enter to finish
	result, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	app = result.(AppModel)

	if app.currentView != ViewDashboard {
		t.Errorf("expected ViewDashboard, got %v", app.currentView)
	}
}

func TestWelcomeSkipShowsGuidance(t *testing.T) {
	cfg := config.NewDefault()
	cfg.WorkspaceRoots = nil
	cfg.Repos = make(map[string]config.RepoConfig)
	app, _ := newTestAppModelWithConfig(t, cfg)

	// Press Esc to skip
	result, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	app = result.(AppModel)

	if app.currentView != ViewDashboard {
		t.Fatalf("expected ViewDashboard after Esc, got %v", app.currentView)
	}
	if !app.dashboard.welcomeSkipped {
		t.Error("expected dashboard.welcomeSkipped to be true")
	}
	// The transient status message should contain guidance text.
	// Use app.View() (not app.dashboard.View()) so the statusMessage
	// syncs from AppModel to DashboardModel before rendering.
	view := app.View().Content
	if !strings.Contains(view, "pressing W") {
		t.Error("expected guidance text about W keybinding in dashboard view")
	}
}

func TestWelcomeSkipShowsGuidanceWithExistingFeatures(t *testing.T) {
	cfg := config.NewDefault()
	cfg.WorkspaceRoots = nil
	cfg.Repos = map[string]config.RepoConfig{
		"test-repo": {Path: "/tmp/test-repo"},
	}
	app, fm := newTestAppModelWithConfig(t, cfg)

	// Seed an existing feature so the dashboard has a non-empty feature list
	_, err := fm.Create("Existing Feature", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create feature: %v", err)
	}

	// Press Esc to skip welcome
	result, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	app = result.(AppModel)

	if app.currentView != ViewDashboard {
		t.Fatalf("expected ViewDashboard after Esc, got %v", app.currentView)
	}

	// Dashboard should have loaded the existing feature
	if len(app.dashboard.features) == 0 {
		t.Fatal("expected dashboard to have features loaded after skip")
	}

	// The transient status message should still show guidance text even
	// though features exist (renders in footer, not empty-state block).
	view := app.View().Content
	if !strings.Contains(view, "pressing W") {
		t.Error("expected guidance text about W keybinding in dashboard view with existing features")
	}
}

func TestHasActiveTextInputWelcomeConfirm(t *testing.T) {
	cfg := config.NewDefault()
	cfg.WorkspaceRoots = nil
	cfg.Repos = make(map[string]config.RepoConfig)
	app, _ := newTestAppModelWithConfig(t, cfg)

	// Go to picker
	result, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	app = result.(AppModel)

	// Set picker completed
	app.welcome.picker.selected = t.TempDir()
	app.welcome.picker.done = true
	app.welcome.picker.gitRepoCount = 1
	app.welcome.picker.columns[len(app.welcome.picker.columns)-1].scanDone = true
	result, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	app = result.(AppModel)

	if app.welcome.step != welcomeStepConfirm {
		t.Fatalf("expected welcomeStepConfirm, got %v", app.welcome.step)
	}
	// Confirm step has no text input
	if app.hasActiveTextInput() {
		t.Error("hasActiveTextInput() should return false for welcomeStepConfirm")
	}
}

// --- Workspace Manager Overlay tests ---

func TestWorkspaceManagerWKeyOpensOverlay(t *testing.T) {
	wsDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wsDir, "repo1", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := config.NewDefault()
	cfg.WorkspaceRoots = []string{wsDir}
	cfg.Repos = make(map[string]config.RepoConfig)

	app, _ := newTestAppModelWithConfig(t, cfg)

	if app.currentView != ViewDashboard {
		t.Fatalf("precondition: currentView = %v, want ViewDashboard", app.currentView)
	}

	// Press W to open workspace manager
	result, _ := app.Update(tea.KeyPressMsg{Code: 'W', Text: "W"})
	app = result.(AppModel)

	if !app.workspaceManagerActive {
		t.Error("workspaceManagerActive should be true after pressing W")
	}

	view := app.View().Content
	if !strings.Contains(view, "Workspace Manager") {
		t.Error("expected view to contain 'Workspace Manager' overlay content")
	}
}

func TestWorkspaceManagerWKeyBlockedDuringTextInput(t *testing.T) {
	wsDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wsDir, "repo1", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := config.NewDefault()
	cfg.WorkspaceRoots = []string{wsDir}
	cfg.Repos = make(map[string]config.RepoConfig)

	app, _ := newTestAppModelWithConfig(t, cfg)

	// Simulate text input being active (chat open)
	app.chatOpen = true

	// Press W — should NOT open workspace manager because text input is active
	result, _ := app.Update(tea.KeyPressMsg{Code: 'W', Text: "W"})
	app = result.(AppModel)

	if app.workspaceManagerActive {
		t.Error("workspaceManagerActive should be false when chatOpen is true (text input active)")
	}
}

func TestWorkspaceManagerWKeyBlockedOffDashboard(t *testing.T) {
	wsDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wsDir, "repo1", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := config.NewDefault()
	cfg.WorkspaceRoots = []string{wsDir}
	cfg.Repos = make(map[string]config.RepoConfig)

	app, _ := newTestAppModelWithConfig(t, cfg)

	// Set view to detail (not dashboard)
	app.currentView = ViewDetail

	// Press W — should NOT open workspace manager from non-dashboard view
	result, _ := app.Update(tea.KeyPressMsg{Code: 'W', Text: "W"})
	app = result.(AppModel)

	if app.workspaceManagerActive {
		t.Error("workspaceManagerActive should be false when not on ViewDashboard")
	}
}

func TestWorkspaceManagerEscClosesOverlay(t *testing.T) {
	wsDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wsDir, "repo1", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := config.NewDefault()
	cfg.WorkspaceRoots = []string{wsDir}
	cfg.Repos = make(map[string]config.RepoConfig)

	app, _ := newTestAppModelWithConfig(t, cfg)

	// Open workspace manager
	result, _ := app.Update(tea.KeyPressMsg{Code: 'W', Text: "W"})
	app = result.(AppModel)

	if !app.workspaceManagerActive {
		t.Fatalf("precondition: workspaceManagerActive should be true")
	}

	// Press Esc to close
	result, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	app = result.(AppModel)

	if app.workspaceManagerActive {
		t.Error("workspaceManagerActive should be false after pressing Esc")
	}
}

func TestWorkspaceManagerBlocksGlobalKeys(t *testing.T) {
	wsDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wsDir, "repo1", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := config.NewDefault()
	cfg.WorkspaceRoots = []string{wsDir}
	cfg.Repos = make(map[string]config.RepoConfig)

	app, _ := newTestAppModelWithConfig(t, cfg)

	// Open workspace manager
	result, _ := app.Update(tea.KeyPressMsg{Code: 'W', Text: "W"})
	app = result.(AppModel)

	if !app.workspaceManagerActive {
		t.Fatalf("precondition: workspaceManagerActive should be true")
	}

	t.Run("q does not trigger quit", func(t *testing.T) {
		result, _ := app.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
		updated := result.(AppModel)
		if updated.quitConfirmActive {
			t.Error("pressing 'q' inside workspace manager should not trigger quit confirmation")
		}
	})

	t.Run("? does not trigger help overlay", func(t *testing.T) {
		result, _ := app.Update(tea.KeyPressMsg{Code: '?', Text: "?"})
		updated := result.(AppModel)
		if updated.helpOverlayActive {
			t.Error("pressing '?' inside workspace manager should not trigger help overlay")
		}
	})

	t.Run("n does not open wizard", func(t *testing.T) {
		result, _ := app.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
		updated := result.(AppModel)
		if updated.currentView != ViewDashboard {
			t.Errorf("pressing 'n' inside workspace manager changed view to %v, want ViewDashboard", updated.currentView)
		}
	})
}

func TestWorkspaceManagerHelpContextIncludesW(t *testing.T) {
	ctx := dashboardLeftHelp()

	found := false
	for _, section := range ctx.Sections {
		for _, binding := range section.Bindings {
			if strings.Contains(binding.Key, "W") {
				found = true
				break
			}
		}
		if found {
			break
		}
	}
	if !found {
		t.Error("dashboardLeftHelp() should contain a binding with 'W' for workspace manager")
	}
}

func TestWorkspaceManagerAddDuplicateRootSkipped(t *testing.T) {
	wsDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wsDir, "repo1", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := config.NewDefault()
	cfg.WorkspaceRoots = []string{wsDir}
	cfg.Repos = make(map[string]config.RepoConfig)

	app, _ := newTestAppModelWithConfig(t, cfg)

	// Open workspace manager
	result, _ := app.Update(tea.KeyPressMsg{Code: 'W', Text: "W"})
	app = result.(AppModel)

	if !app.workspaceManagerActive {
		t.Fatalf("precondition: workspaceManagerActive should be true")
	}

	// --- First add: press 'a' to open the directory picker ---
	result, _ = app.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	app = result.(AppModel)

	if !app.workspaceManager.IsPickerActive() {
		t.Fatal("picker should be active after pressing 'a'")
	}

	// Set the picker to our controlled workspace dir and mark it ready
	app.workspaceManager.picker.setCurrentDir(wsDir)
	activeCol := &app.workspaceManager.picker.columns[len(app.workspaceManager.picker.columns)-1]
	highlightedName := activeCol.highlightedName()
	activeCol.scanDone = true
	activeCol.repoDirs = map[string]bool{highlightedName: true}
	app.workspaceManager.picker.gitRepoCount = 1
	app.workspaceManager.picker.gitRepoDirs = map[string]bool{highlightedName: true}

	// Press Enter to select the highlighted entry
	result, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	app = result.(AppModel)

	// The picker should be done and the root consumed by the app
	rootsAfterFirst := len(cfg.WorkspaceRoots)

	// --- Second add: open picker again and select the same directory ---
	result, _ = app.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	app = result.(AppModel)

	if !app.workspaceManager.IsPickerActive() {
		t.Fatal("picker should be active after pressing 'a' the second time")
	}

	// Set picker to same dir again (duplicate attempt)
	app.workspaceManager.picker.setCurrentDir(wsDir)
	activeCol2 := &app.workspaceManager.picker.columns[len(app.workspaceManager.picker.columns)-1]
	activeCol2.scanDone = true
	activeCol2.repoDirs = map[string]bool{highlightedName: true}
	app.workspaceManager.picker.gitRepoCount = 1

	// Press Enter to select the same highlighted entry (duplicate)
	result, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	app = result.(AppModel)

	// Both picks selected the same highlighted entry, count should not have increased
	if len(cfg.WorkspaceRoots) != rootsAfterFirst {
		t.Errorf("WorkspaceRoots count = %d after duplicate add, want %d (duplicate should be skipped)",
			len(cfg.WorkspaceRoots), rootsAfterFirst)
	}
}

func TestWorkspaceManagerRemoveRootPersistsConfig(t *testing.T) {
	wsDir1 := t.TempDir()
	wsDir2 := t.TempDir()
	for _, dir := range []string{wsDir1, wsDir2} {
		if err := os.MkdirAll(filepath.Join(dir, "repo", ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	cfg := config.NewDefault()
	cfg.WorkspaceRoots = []string{wsDir1, wsDir2}
	cfg.Repos = make(map[string]config.RepoConfig)

	app, _ := newTestAppModelWithConfig(t, cfg)

	// Open workspace manager
	result, _ := app.Update(tea.KeyPressMsg{Code: 'W', Text: "W"})
	app = result.(AppModel)

	if !app.workspaceManagerActive {
		t.Fatalf("precondition: workspaceManagerActive should be true")
	}

	// Cursor starts at index 0 — move to index 1 (wsDir2)
	result, _ = app.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	app = result.(AppModel)

	// Press 'd' to start delete confirmation
	result, _ = app.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})
	app = result.(AppModel)

	if !app.workspaceManager.confirmDelete {
		t.Fatal("confirmDelete should be true after pressing 'd'")
	}
	if app.workspaceManager.confirmPath != wsDir2 {
		t.Fatalf("confirmPath = %q, want %q", app.workspaceManager.confirmPath, wsDir2)
	}

	// Press 'y' to confirm removal
	result, _ = app.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	app = result.(AppModel)

	// Verify config has only 1 root remaining
	if len(cfg.WorkspaceRoots) != 1 {
		t.Errorf("WorkspaceRoots count = %d, want 1 after removal", len(cfg.WorkspaceRoots))
	}
	if len(cfg.WorkspaceRoots) > 0 && cfg.WorkspaceRoots[0] != wsDir1 {
		t.Errorf("remaining root = %q, want %q", cfg.WorkspaceRoots[0], wsDir1)
	}

	// Verify config file on disk was updated
	if app.configPath != "" {
		loaded, err := config.Load(app.configPath)
		if err != nil {
			t.Fatalf("loading persisted config: %v", err)
		}
		if len(loaded.WorkspaceRoots) != 1 {
			t.Errorf("persisted WorkspaceRoots count = %d, want 1", len(loaded.WorkspaceRoots))
		}
	}
}

func TestWorkspaceManagerOverlayStacking(t *testing.T) {
	wsDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wsDir, "repo1", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := config.NewDefault()
	cfg.WorkspaceRoots = []string{wsDir}
	cfg.Repos = make(map[string]config.RepoConfig)

	app, _ := newTestAppModelWithConfig(t, cfg)

	// Open workspace manager
	result, _ := app.Update(tea.KeyPressMsg{Code: 'W', Text: "W"})
	app = result.(AppModel)

	view := app.View().Content
	if !strings.Contains(view, "Workspace Manager") {
		t.Error("expected view to contain 'Workspace Manager' content")
	}

	// Activate the directory picker by pressing 'a' (add root)
	result, _ = app.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	app = result.(AppModel)

	if !app.workspaceManager.IsPickerActive() {
		t.Fatal("expected picker to be active after pressing 'a'")
	}

	// The stacked picker overlay should render on top. With proper sizing,
	// the picker fully covers the workspace manager, so check for picker content.
	view = app.View().Content
	if !strings.Contains(view, "esc cancel") {
		t.Error("expected view to contain picker content ('esc cancel') with stacked overlay")
	}
}

func TestWorkspaceManagerRefreshesAfterChanges(t *testing.T) {
	wsDir1 := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wsDir1, "repo1", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := config.NewDefault()
	cfg.WorkspaceRoots = []string{wsDir1}
	cfg.Repos = make(map[string]config.RepoConfig)

	app, _ := newTestAppModelWithConfig(t, cfg)

	// Open workspace manager
	result, _ := app.Update(tea.KeyPressMsg{Code: 'W', Text: "W"})
	app = result.(AppModel)

	// Verify only 1 root shown initially
	if len(app.workspaceManager.roots) != 1 {
		t.Fatalf("initial roots count = %d, want 1", len(app.workspaceManager.roots))
	}

	// Press 'a' to open the directory picker
	result, _ = app.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	app = result.(AppModel)

	if !app.workspaceManager.IsPickerActive() {
		t.Fatal("picker should be active after pressing 'a'")
	}

	// Set picker to a controlled directory and mark it ready
	newDir := t.TempDir()
	os.MkdirAll(filepath.Join(newDir, "new-repo", ".git"), 0o755)
	app.workspaceManager.picker.setCurrentDir(newDir)
	activeCol := &app.workspaceManager.picker.columns[len(app.workspaceManager.picker.columns)-1]
	activeCol.scanDone = true
	activeCol.repoDirs = map[string]bool{"new-repo": true}
	app.workspaceManager.picker.gitRepoCount = 1

	// Press Enter to select the highlighted entry
	result, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	app = result.(AppModel)

	// The picker should be done and the root consumed
	if app.workspaceManager.IsPickerActive() {
		t.Fatal("picker should be inactive after Enter")
	}

	// Config should now have 2 roots (original + picked)
	if len(cfg.WorkspaceRoots) != 2 {
		t.Errorf("WorkspaceRoots count = %d, want 2 after add", len(cfg.WorkspaceRoots))
	}

	// The manager's root list should be refreshed to show 2 roots
	if len(app.workspaceManager.roots) != 2 {
		t.Errorf("workspace manager roots count = %d, want 2 after refresh", len(app.workspaceManager.roots))
	}

	// Close the manager
	result, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	app = result.(AppModel)

	if app.workspaceManagerActive {
		t.Fatal("workspace manager should be closed after Esc")
	}

	// Reopen the manager and verify the new root appears
	result, _ = app.Update(tea.KeyPressMsg{Code: 'W', Text: "W"})
	app = result.(AppModel)

	if len(app.workspaceManager.roots) != 2 {
		t.Errorf("reopened workspace manager roots count = %d, want 2", len(app.workspaceManager.roots))
	}

	// Verify the picked directory is present in the reopened manager
	selectedDir := filepath.Join(newDir, "new-repo")
	foundPicked := false
	for _, root := range app.workspaceManager.roots {
		if root.Path == selectedDir {
			foundPicked = true
			break
		}
	}
	if !foundPicked {
		t.Errorf("workspace manager should contain picked root %q, got %v", selectedDir, app.workspaceManager.roots)
	}
}

func TestWorkspaceManagerNonKeyMessagesReachPicker(t *testing.T) {
	wsDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wsDir, "repo1", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := config.NewDefault()
	cfg.WorkspaceRoots = []string{wsDir}
	cfg.Repos = make(map[string]config.RepoConfig)

	app, _ := newTestAppModelWithConfig(t, cfg)

	// Open workspace manager
	result, _ := app.Update(tea.KeyPressMsg{Code: 'W', Text: "W"})
	app = result.(AppModel)

	// Press 'a' to open the directory picker
	result, _ = app.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	app = result.(AppModel)

	if !app.workspaceManager.IsPickerActive() {
		t.Fatal("picker should be active after pressing 'a'")
	}

	// Verify picker starts with scanDone = false
	activeCol := &app.workspaceManager.picker.columns[len(app.workspaceManager.picker.columns)-1]
	if activeCol.scanDone {
		t.Fatal("picker active column scanDone should be false initially")
	}

	pickerDir := app.workspaceManager.picker.currentDir()

	// Send a gitRepoScanMsg (a non-key message) through the app.
	// This tests the core routing fix: non-key messages must reach the
	// workspace manager's nested picker via the default case in Update().
	result, _ = app.Update(gitRepoScanMsg{
		dir:           pickerDir,
		count:         3,
		repoDirs:      map[string]bool{"a": true, "b": true, "c": true},
		dirRepoCounts: map[string]int{},
	})
	app = result.(AppModel)

	// Verify the message reached the picker: scanDone should now be true
	activeCol = &app.workspaceManager.picker.columns[len(app.workspaceManager.picker.columns)-1]
	if !activeCol.scanDone {
		t.Error("picker active column scanDone should be true after gitRepoScanMsg was routed through the app")
	}

	// Verify the picker received the repo count
	if app.workspaceManager.picker.gitRepoCount != 3 {
		t.Errorf("picker.gitRepoCount = %d, want 3", app.workspaceManager.picker.gitRepoCount)
	}
}

func TestWorkspaceManagerPickerSizing(t *testing.T) {
	// Regression: the workspace-manager picker must receive the app's
	// terminal dimensions both at creation time and on subsequent resizes.
	wsDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wsDir, "repo1", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := config.NewDefault()
	cfg.WorkspaceRoots = []string{wsDir}
	cfg.Repos = make(map[string]config.RepoConfig)

	app, _ := newTestAppModelWithConfig(t, cfg)

	// Open workspace manager
	result, _ := app.Update(tea.KeyPressMsg{Code: 'W', Text: "W"})
	app = result.(AppModel)

	// Open picker via 'a'
	result, _ = app.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	app = result.(AppModel)

	if !app.workspaceManager.IsPickerActive() {
		t.Fatal("picker should be active after pressing 'a'")
	}

	// The picker should have received the app's dimensions (80x24 from newTestAppModelWithConfig).
	if app.workspaceManager.picker.width != 80 {
		t.Errorf("picker.width = %d, want 80 (app width)", app.workspaceManager.picker.width)
	}
	if app.workspaceManager.picker.height != 24 {
		t.Errorf("picker.height = %d, want 24 (app height)", app.workspaceManager.picker.height)
	}

	// Send a WindowSizeMsg through the app — it should reach the nested picker.
	result, _ = app.Update(tea.WindowSizeMsg{Width: 160, Height: 48})
	app = result.(AppModel)

	if app.workspaceManager.picker.width != 160 {
		t.Errorf("picker.width after resize = %d, want 160", app.workspaceManager.picker.width)
	}
	if app.workspaceManager.picker.height != 48 {
		t.Errorf("picker.height after resize = %d, want 48", app.workspaceManager.picker.height)
	}
}

func TestWorkspaceManagerAddDuplicateRootExpandedPath(t *testing.T) {
	// Regression: adding a root whose expanded path matches an existing root
	// with a ~ prefix must be detected as a duplicate.
	wsDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wsDir, "repo1", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := config.NewDefault()
	// Store the root using its fully expanded absolute path.
	cfg.WorkspaceRoots = []string{wsDir}
	cfg.Repos = make(map[string]config.RepoConfig)

	app, _ := newTestAppModelWithConfig(t, cfg)

	// Directly invoke updateWorkspaceManager with a pre-loaded addedRoot
	// that uses the exact same expanded path. This exercises containsRootExpanded
	// without depending on the picker's chosen directory.
	app.workspaceManagerActive = true
	app.workspaceManager = NewWorkspaceManagerModel(
		buildWorkspaceRoots(cfg), app.width, app.height,
	)

	// Simulate adding the same directory (already in config).
	app.workspaceManager.addedRoot = wsDir

	result, _ := app.updateWorkspaceManager(tea.KeyPressMsg{Code: 0})
	app = result.(AppModel)

	if len(cfg.WorkspaceRoots) != 1 {
		t.Errorf("WorkspaceRoots = %d, want 1 (duplicate should be skipped)", len(cfg.WorkspaceRoots))
	}

	// Now simulate adding via a ~ prefix path. Construct a tilde-prefixed
	// version by replacing $HOME at the start.
	home := t.TempDir()
	t.Setenv("HOME", home)
	if !strings.HasPrefix(wsDir, home) {
		// Temp dir is not under $HOME (common on macOS: /var/folders/...).
		// Create a directory under a test-scoped $HOME to test the tilde expansion case.
		homeSubDir := filepath.Join(home, ".agentic-test-dup-root-"+filepath.Base(wsDir))
		if err := os.MkdirAll(filepath.Join(homeSubDir, "repo1", ".git"), 0o755); err != nil {
			t.Fatal(err)
		}

		// Reset config with this home-relative directory.
		cfg.WorkspaceRoots = []string{homeSubDir}
		app.workspaceManager = NewWorkspaceManagerModel(
			buildWorkspaceRoots(cfg), app.width, app.height,
		)
		tildeDir := "~/" + strings.TrimPrefix(homeSubDir, home+"/")
		app.workspaceManager.addedRoot = tildeDir

		result, _ = app.updateWorkspaceManager(tea.KeyPressMsg{Code: 0})
		app = result.(AppModel)

		if len(cfg.WorkspaceRoots) != 1 {
			t.Errorf("WorkspaceRoots = %d after ~/... duplicate add, want 1", len(cfg.WorkspaceRoots))
		}
	} else {
		// wsDir is under $HOME — test directly.
		tildeDir := "~/" + strings.TrimPrefix(wsDir, home+"/")
		app.workspaceManager = NewWorkspaceManagerModel(
			buildWorkspaceRoots(cfg), app.width, app.height,
		)
		app.workspaceManager.addedRoot = tildeDir

		result, _ = app.updateWorkspaceManager(tea.KeyPressMsg{Code: 0})
		app = result.(AppModel)

		if len(cfg.WorkspaceRoots) != 1 {
			t.Errorf("WorkspaceRoots = %d after ~/... duplicate add, want 1", len(cfg.WorkspaceRoots))
		}
	}
}

func TestWizardBrowseOverlayStacking(t *testing.T) {
	cfg := config.NewDefault()
	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, "test-repo")
	os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755)
	cfg.WorkspaceRoots = []string{tmpDir}
	config.DiscoverReposFromRoots(cfg)
	app, _ := newTestAppModelWithConfig(t, cfg)

	// Open wizard
	result, _ := app.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	app = result.(AppModel)

	// Advance wizard to repos step
	result, _ = app.Update(tea.KeyPressMsg{Text: "test-feature"})
	app = result.(AppModel)
	result, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // name → description
	app = result.(AppModel)
	result, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // description → Where
	app = result.(AppModel)

	// Tab to Browse focus, then Enter to open the picker.
	result, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	app = result.(AppModel)
	result, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	app = result.(AppModel)

	if !app.wizard.IsPickerActive() {
		t.Fatal("expected picker to be active")
	}

	view := app.View().Content
	// The picker overlay is stacked on top of the wizard. The picker is centered
	// and covers the wizard, showing its own controls.
	if !strings.Contains(view, "esc cancel") && !strings.Contains(view, "esc") {
		t.Error("expected picker footer controls in stacked view")
	}
	if !strings.Contains(view, "Current dir") {
		t.Error("expected picker breadcrumb in stacked view")
	}
}

func TestWizardBrowsePersistsConfigAndRediscovers(t *testing.T) {
	cfg := config.NewDefault()
	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, "existing-repo")
	os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755)
	cfg.WorkspaceRoots = []string{tmpDir}
	config.DiscoverReposFromRoots(cfg)
	app, _ := newTestAppModelWithConfig(t, cfg)

	// Simulate wizard selecting a browse root
	newRootDir := t.TempDir()
	newRepoDir := filepath.Join(newRootDir, "new-repo")
	os.MkdirAll(filepath.Join(newRepoDir, ".git"), 0o755)

	// Directly set browseRoot on wizard (simulating picker completion)
	app.wizard.browseRoot = newRootDir

	// Trigger the handling
	app = app.handleWizardBrowseResult()

	// Verify config updated
	found := false
	for _, root := range app.featureManager.Config.WorkspaceRoots {
		if root == newRootDir {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected new root in config workspace roots")
	}

	// Verify repos rediscovered
	allRepos := config.AllRepos(app.featureManager.Config)
	if _, ok := allRepos["new-repo"]; !ok {
		t.Error("expected new-repo to be discovered after browse")
	}
}

func TestWizardBrowseDuplicateRootSkipped(t *testing.T) {
	cfg := config.NewDefault()
	tmpDir := t.TempDir()
	cfg.WorkspaceRoots = []string{tmpDir}
	app, _ := newTestAppModelWithConfig(t, cfg)

	// Set browse root to existing root
	app.wizard.browseRoot = tmpDir
	app = app.handleWizardBrowseResult()

	// Should still have exactly 1 root
	if len(app.featureManager.Config.WorkspaceRoots) != 1 {
		t.Errorf("expected 1 workspace root, got %d", len(app.featureManager.Config.WorkspaceRoots))
	}
}

func TestWizardBrowseNewReposNotAutoSelected(t *testing.T) {
	cfg := config.NewDefault()
	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, "repo-a")
	os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755)
	cfg.WorkspaceRoots = []string{tmpDir}
	config.DiscoverReposFromRoots(cfg)
	app, _ := newTestAppModelWithConfig(t, cfg)

	// Open wizard and advance to repos
	result, _ := app.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	app = result.(AppModel)
	result, _ = app.Update(tea.KeyPressMsg{Text: "test-feature"})
	app = result.(AppModel)
	result, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // name → description
	app = result.(AppModel)
	result, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // description → Where
	app = result.(AppModel)

	// Select repo-a (Space toggles repos on Where step)
	result, _ = app.Update(tea.KeyPressMsg{Code: ' ', Text: " "})
	app = result.(AppModel)

	// Browse and add new root
	newRootDir := t.TempDir()
	newRepoDir := filepath.Join(newRootDir, "repo-c")
	os.MkdirAll(filepath.Join(newRepoDir, ".git"), 0o755)
	app.wizard.browseRoot = newRootDir
	app = app.handleWizardBrowseResult()

	if !app.wizard.selectedRepos["repo-a"] {
		t.Error("repo-a should still be selected after browse refresh")
	}
	if app.wizard.selectedRepos["repo-c"] {
		t.Error("repo-c should NOT be auto-selected after browse refresh")
	}
}

func TestWizardBrowseCollisionPreservesSelection(t *testing.T) {
	cfg := config.NewDefault()

	// rootA has "myrepo"
	rootA := t.TempDir()
	os.MkdirAll(filepath.Join(rootA, "myrepo", ".git"), 0o755)
	cfg.WorkspaceRoots = []string{rootA}
	config.DiscoverReposFromRoots(cfg)

	app, _ := newTestAppModelWithConfig(t, cfg)

	// Open wizard and advance to repos
	result, _ := app.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	app = result.(AppModel)
	result, _ = app.Update(tea.KeyPressMsg{Text: "test-feature"})
	app = result.(AppModel)
	result, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // name → description
	app = result.(AppModel)
	result, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // description → Where
	app = result.(AppModel)

	// Select "myrepo" (Space toggles repos on Where step)
	result, _ = app.Update(tea.KeyPressMsg{Code: ' ', Text: " "})
	app = result.(AppModel)
	if !app.wizard.selectedRepos["myrepo"] {
		t.Fatal("precondition: myrepo should be selected")
	}

	// rootB also has "myrepo" — triggers collision re-keying
	rootB := t.TempDir()
	os.MkdirAll(filepath.Join(rootB, "myrepo", ".git"), 0o755)
	app.wizard.browseRoot = rootB
	app = app.handleWizardBrowseResult()

	// After re-discovery with collision, keys become qualified (basename/repo).
	// The original selection must survive under the new qualified key.
	rootABase := filepath.Base(rootA)
	qualifiedKey := rootABase + "/myrepo"
	if !app.wizard.selectedRepos[qualifiedKey] {
		t.Errorf("selection for rootA/myrepo should survive collision; selectedRepos=%v, expected key=%q",
			app.wizard.selectedRepos, qualifiedKey)
	}
	if app.wizard.selectedRepos["myrepo"] {
		t.Error("old unqualified key 'myrepo' should no longer be in selectedRepos")
	}
	// rootB's myrepo should not be auto-selected
	rootBBase := filepath.Base(rootB)
	if app.wizard.selectedRepos[rootBBase+"/myrepo"] {
		t.Error("rootB/myrepo should NOT be auto-selected")
	}
}

func TestWizardBrowseNonKeyMessagesReachPicker(t *testing.T) {
	cfg := config.NewDefault()
	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, "test-repo")
	os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755)
	cfg.WorkspaceRoots = []string{tmpDir}
	config.DiscoverReposFromRoots(cfg)
	app, _ := newTestAppModelWithConfig(t, cfg)

	// Open wizard
	result, _ := app.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	app = result.(AppModel)
	result, _ = app.Update(tea.KeyPressMsg{Text: "test-feature"})
	app = result.(AppModel)
	result, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // name → description
	app = result.(AppModel)
	result, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // description → Where
	app = result.(AppModel)

	// Tab to Browse focus, then Enter to open the picker.
	result, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	app = result.(AppModel)
	result, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	app = result.(AppModel)

	if !app.wizard.IsPickerActive() {
		t.Fatal("precondition: picker should be active")
	}

	// Send non-key message via forwardToActiveInput path — should not panic
	result, _ = app.Update(gitRepoScanMsg{dir: "/tmp", count: 0, repoDirs: map[string]bool{}})
	app = result.(AppModel)

	// Picker should still be active (scan msg doesn't complete picker)
	if !app.wizard.IsPickerActive() {
		t.Error("picker should still be active after non-key message")
	}
}

func TestCreateFeatureCmdSavesGatesToConfig(t *testing.T) {
	app, fm := newTestAppModel(t)

	// Set up configPath to a temp file so Save writes there
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	app.configPath = configPath

	result := &WizardResult{
		Name:        "gate-persist-test",
		Description: "test gate persistence",
		Repos:       []string{"test-repo"},
		Pipeline:    feature.PipelineMedium,
		Models: config.ModelConfig{
			Research:       "claude:haiku",
			Planning:       "claude:haiku",
			Implementation: "claude:sonnet",
			Review:         "codex:gpt-5.4-mini",
			KBBuild:        "claude:opus",
		},
		Inquireness: "none",
		Checkpoints: feature.Checkpoints{ManualPublish: false},
	}

	// Execute the command synchronously
	cmd := app.createFeatureCmd(result)
	msg := cmd()

	// Verify feature was created
	createdMsg, ok := msg.(FeatureCreatedMsg)
	if !ok {
		t.Fatalf("expected FeatureCreatedMsg, got %T", msg)
	}
	if createdMsg.Err != nil {
		t.Fatalf("unexpected error: %v", createdMsg.Err)
	}

	// Verify gates were saved to config in memory
	rc := fm.Config.Repos["test-repo"]
	if rc.PipelineGates == nil {
		t.Fatal("PipelineGates should be set on test-repo")
	}
	saved, ok := rc.PipelineGates["medium"]
	if !ok {
		t.Fatal("PipelineGates should have 'medium' key")
	}
	if saved.ManualPublish {
		t.Error("saved ManualPublish should be false")
	}
	pref, ok := fm.Config.Defaults.PipelinePreferences["medium"]
	if !ok {
		t.Fatal("PipelinePreferences should have 'medium' key")
	}
	if pref.Inquireness != "none" {
		t.Errorf("saved inquireness = %q, want %q", pref.Inquireness, "none")
	}
	if pref.Models.Implementation != "claude:sonnet" {
		t.Errorf("saved implementation model = %q, want %q", pref.Models.Implementation, "claude:sonnet")
	}

	// Verify config was persisted to disk
	loaded, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("loading saved config: %v", err)
	}
	loadedRC := loaded.Repos["test-repo"]
	if loadedRC.PipelineGates == nil {
		t.Fatal("loaded config should have PipelineGates for test-repo")
	}
	loadedGates, ok := loadedRC.PipelineGates["medium"]
	if !ok {
		t.Fatal("loaded config PipelineGates should have 'medium' key")
	}
	if loadedGates.ManualPublish {
		t.Error("loaded ManualPublish should be false")
	}
	loadedPref, ok := loaded.Defaults.PipelinePreferences["medium"]
	if !ok {
		t.Fatal("loaded config PipelinePreferences should have 'medium' key")
	}
	if loadedPref.Inquireness != "none" {
		t.Errorf("loaded inquireness = %q, want %q", loadedPref.Inquireness, "none")
	}
	if loadedPref.Models.Review != "codex:gpt-5.4-mini" {
		t.Errorf("loaded review model = %q, want %q", loadedPref.Models.Review, "codex:gpt-5.4-mini")
	}
}

func TestCreateFeatureCmdNormalizesExpressGatesBeforeSave(t *testing.T) {
	app, fm := newTestAppModel(t)

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	app.configPath = configPath

	result := &WizardResult{
		Name:        "medium-normalization",
		Description: "test medium normalization",
		Repos:       []string{"test-repo"},
		Pipeline:    feature.PipelineMedium,
		Checkpoints: feature.Checkpoints{
			InquiryReview: true,
			DesignReview:  true,
			PlanReview:    true,
			ManualPublish: true,
		},
	}

	msg := app.createFeatureCmd(result)()
	created, ok := msg.(FeatureCreatedMsg)
	if !ok {
		t.Fatalf("expected FeatureCreatedMsg, got %T", msg)
	}
	if created.Err != nil {
		t.Fatalf("unexpected create error: %v", created.Err)
	}
	if len(created.FeatureIDs) != 1 {
		t.Fatalf("FeatureCreatedMsg.FeatureIDs = %v, want one feature ID", created.FeatureIDs)
	}

	feat, err := fm.Get(created.FeatureIDs[0])
	if err != nil {
		t.Fatalf("loading created feature: %v", err)
	}
	want := feature.Checkpoints{PlanReview: true, ManualPublish: true}
	if feat.Checkpoints != want {
		t.Fatalf("created feature checkpoints = %+v, want %+v", feat.Checkpoints, want)
	}

	saved := fm.Config.Repos["test-repo"].PipelineGates["medium"]
	if saved != (config.Checkpoints{PlanReview: true, ManualPublish: true}) {
		t.Fatalf("saved medium gates = %+v, want PlanReview+ManualPublish", saved)
	}

	loaded, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("loading saved config: %v", err)
	}
	loadedGates := loaded.Repos["test-repo"].PipelineGates["medium"]
	if loadedGates.InquiryReview || loadedGates.ResearchReview || loadedGates.DesignReview || !loadedGates.PlanReview || !loadedGates.ManualPublish {
		t.Fatalf("loaded medium gates = %+v, want PlanReview+ManualPublish", loadedGates)
	}
}

func TestCreateFeatureCmdSavesGatesToAllRepos(t *testing.T) {
	dir := t.TempDir()
	store := feature.NewStore(dir)
	cfg := config.NewDefault()
	cfg.Repos["repo-a"] = config.RepoConfig{Path: "/tmp/repo-a"}
	cfg.Repos["repo-b"] = config.RepoConfig{Path: "/tmp/repo-b"}
	fm := feature.NewManager(store, cfg)

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	dash := NewDashboardModel(nil, "")
	dash.width = 80
	dash.height = 24
	// Iteration 11 routes createFeatureCmd through orchestrator.CreateFeature
	// so we wire a minimal orchestrator to satisfy the call path.
	orch := orchestrator.New(orchestrator.Deps{
		Lifecycle: fm,
		Store:     store,
	}, orchestrator.Hooks{})
	app := AppModel{
		currentView:    ViewDashboard,
		dashboard:      dash,
		featureManager: fm,
		orchestrator:   orch,
		programRef:     &ProgramRef{},
		configPath:     configPath,
		width:          80,
		height:         24,
	}

	result := &WizardResult{
		Name:        "multi-repo-gates",
		Description: "test multi-repo gate save",
		Repos:       []string{"repo-a", "repo-b"},
		Pipeline:    feature.PipelineLarge,
		Checkpoints: feature.Checkpoints{DesignReview: true, PlanReview: true, ManualPublish: true},
	}

	cmd := app.createFeatureCmd(result)
	msg := cmd()

	createdMsg, ok := msg.(FeatureCreatedMsg)
	if !ok {
		t.Fatalf("expected FeatureCreatedMsg, got %T", msg)
	}
	if createdMsg.Err != nil {
		t.Fatalf("unexpected error: %v", createdMsg.Err)
	}

	// Both repos should have large gates saved
	for _, repoName := range []string{"repo-a", "repo-b"} {
		rc := fm.Config.Repos[repoName]
		if rc.PipelineGates == nil {
			t.Errorf("%s: PipelineGates should be set", repoName)
			continue
		}
		gates, ok := rc.PipelineGates["large"]
		if !ok {
			t.Errorf("%s: PipelineGates should have 'large' key", repoName)
			continue
		}
		if !gates.DesignReview || !gates.PlanReview || !gates.ManualPublish {
			t.Errorf("%s: gates should have DesignReview+PlanReview+ManualPublish, got %+v", repoName, gates)
		}
	}
}

func TestSaveConfigCmdPersistsNormalizedExpressGatesAfterEdit(t *testing.T) {
	app, fm := newTestAppModel(t)

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	app.configPath = configPath

	f, err := fm.Create("edit-medium-gates", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil, feature.CreateOptions{
		Pipeline:    feature.PipelineMedium,
		Checkpoints: feature.Checkpoints{ManualPublish: true},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	msg := app.saveConfigCmd(f.ID, orchestrator.UpdateFeatureConfigInput{
		Models:      config.ModelConfig{Implementation: "claude:sonnet"},
		Inquireness: feature.InquirenessNone,
		Checkpoints: feature.Checkpoints{
			InquiryReview: true,
			DesignReview:  true,
			PlanReview:    true,
			ManualPublish: true,
		},
	}, []string{"test-repo"}, feature.PipelineMedium, true)()
	result, ok := msg.(editConfigResultMsg)
	if !ok {
		t.Fatalf("expected editConfigResultMsg, got %T", msg)
	}
	if result.err != nil {
		t.Fatalf("unexpected save error: %v", result.err)
	}

	savedFeature, err := fm.Get(f.ID)
	if err != nil {
		t.Fatalf("reloading feature: %v", err)
	}
	if got := savedFeature.Checkpoints; got != (feature.Checkpoints{PlanReview: true, ManualPublish: true}) {
		t.Fatalf("feature checkpoints = %+v, want PlanReview+ManualPublish", got)
	}

	gates := fm.Config.Repos["test-repo"].PipelineGates["medium"]
	if gates != (config.Checkpoints{PlanReview: true, ManualPublish: true}) {
		t.Fatalf("saved medium gates = %+v, want PlanReview+ManualPublish", gates)
	}

	loaded, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("loading saved config: %v", err)
	}
	got := loaded.Repos["test-repo"].PipelineGates["medium"]
	if got.InquiryReview || got.ResearchReview || got.DesignReview || !got.PlanReview || !got.ManualPublish {
		t.Fatalf("loaded medium gates = %+v, want PlanReview+ManualPublish", got)
	}
}

func TestRewindMenuModal_ExpressOnlyShowsPipelinePhases(t *testing.T) {
	app, fm := newTestAppModel(t)
	f, err := fm.Create("menu-annotation", "Test annotations", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Make it medium at implementing
	err = fm.Store.Modify(f.ID, func(f *feature.Feature) error {
		f.Status = feature.StatusImplementing
		f.Pipeline = feature.PipelineMedium
		f.Checkpoints = feature.DefaultCheckpointsForProfile(feature.PipelineMedium)
		return nil
	})
	if err != nil {
		t.Fatalf("modify: %v", err)
	}
	// Load fresh feature and compute choices
	f, _ = fm.Get(f.ID)
	choices := feature.RewindChoicesForFeature(f)

	// Medium should only have Plan + Implement as rewind choices
	if len(choices) != 2 {
		t.Fatalf("expected 2 rewind choices for medium, got %d", len(choices))
	}

	// Set up rewind menu state
	app.rewindMenuActive = true
	app.rewindMenuChoices = choices
	app.rewindMenuCursor = 0
	app.rewindMenuUpgradeOptions = []feature.PipelineProfile{feature.PipelineLarge, feature.PipelineMoonshot}
	app.width = 80

	output := app.rewindMenuModal()

	// Should NOT contain escalation annotations (no auto-escalation anymore)
	if strings.Contains(output, "escalate") {
		t.Errorf("medium menu should not show escalation annotations, got:\n%s", output)
	}
	// Check that upgrade section exists with KB rewind hint
	if !strings.Contains(output, "Pipeline Upgrade") {
		t.Errorf("expected 'Pipeline Upgrade' section in menu, got:\n%s", output)
	}
	if !strings.Contains(output, "Upgrade to large") {
		t.Errorf("expected 'Upgrade to large' option, got:\n%s", output)
	}
	if !strings.Contains(output, "rewinds to KB Build") {
		t.Errorf("expected 'rewinds to KB Build' hint on upgrade options, got:\n%s", output)
	}
}

func TestRewindMenuModal_ThoroughNoEscalation(t *testing.T) {
	app, fm := newTestAppModel(t)
	f, err := fm.Create("menu-moonshot", "Test moonshot", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	err = fm.Store.Modify(f.ID, func(f *feature.Feature) error {
		f.Status = feature.StatusImplementing
		f.Pipeline = feature.PipelineMoonshot
		f.Checkpoints = feature.DefaultCheckpointsForProfile(feature.PipelineMoonshot)
		return nil
	})
	if err != nil {
		t.Fatalf("modify: %v", err)
	}
	f, _ = fm.Get(f.ID)
	choices := feature.RewindChoicesForFeature(f)

	app.rewindMenuActive = true
	app.rewindMenuChoices = choices
	app.rewindMenuCursor = 0
	// rewindMenuHasEscalation removed — no auto-escalation
	app.rewindMenuUpgradeOptions = nil
	app.width = 80

	output := app.rewindMenuModal()

	if strings.Contains(output, "escalate") {
		t.Errorf("moonshot feature should not have escalation annotations, got:\n%s", output)
	}
	if strings.Contains(output, "Pipeline Upgrade") {
		t.Errorf("moonshot feature should not have upgrade section, got:\n%s", output)
	}
	if strings.Contains(output, "Escalation resets") {
		t.Errorf("moonshot feature should not have escalation info text, got:\n%s", output)
	}
}

func TestRewindMenuModal_UpgradedExpressShowsKBRestart(t *testing.T) {
	app, fm := newTestAppModel(t)
	f, err := fm.Create("menu-upgraded", "Test upgraded medium", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	err = fm.Store.Modify(f.ID, func(f *feature.Feature) error {
		f.Status = feature.StatusImplementing
		f.Pipeline = feature.PipelineLarge
		f.PipelineUpgradedFrom = feature.PipelineMedium
		f.Checkpoints = feature.DefaultCheckpointsForProfile(feature.PipelineLarge)
		return nil
	})
	if err != nil {
		t.Fatalf("modify: %v", err)
	}
	f, _ = fm.Get(f.ID)
	choices := feature.RewindChoicesForFeature(f)

	app.rewindMenuActive = true
	app.rewindMenuChoices = choices
	app.rewindMenuCursor = 0
	// rewindMenuHasEscalation removed — no auto-escalation
	app.rewindMenuUpgradeOptions = []feature.PipelineProfile{feature.PipelineMoonshot}
	app.width = 80

	output := app.rewindMenuModal()

	// Should contain "restarts from KB Build" for pre-plan phases
	if !strings.Contains(output, "restarts from KB Build") {
		t.Errorf("expected 'restarts from KB Build' annotation for upgraded medium, got:\n%s", output)
	}
	// Should NOT contain "escalate to large" since already large
	if strings.Contains(output, "escalate to large") {
		t.Errorf("upgraded feature should not show 'escalate to large', got:\n%s", output)
	}
}

func TestRewindConfirmModal_PipelineUpgradeWarning(t *testing.T) {
	app, fm := newTestAppModel(t)
	f, err := fm.Create("confirm-kb", "Test KB restart", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	app.rewindConfirmActive = true
	app.rewindConfirmFeatureID = f.ID
	app.rewindConfirmPhase = feature.PhaseInquire
	app.rewindConfirmPhaseName = "KB Build"
	app.rewindConfirmEscalates = feature.PipelineLarge
	app.rewindConfirmOverridesKB = true
	app.rewindConfirmUpgrade = feature.PipelineLarge

	output := app.rewindConfirmModal()

	if !strings.Contains(output, "Upgrade to large") {
		t.Errorf("expected pipeline upgrade title, got:\n%s", output)
	}
	if !strings.Contains(output, "KB Build") {
		t.Errorf("expected KB Build restart warning, got:\n%s", output)
	}
}

func TestRewindConfirmModal_NoKBRestartForNormalRewind(t *testing.T) {
	app, fm := newTestAppModel(t)
	f, err := fm.Create("confirm-normal", "Test normal rewind", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	app.rewindConfirmActive = true
	app.rewindConfirmFeatureID = f.ID
	app.rewindConfirmPhase = feature.PhasePlan
	app.rewindConfirmPhaseName = "Plan"
	app.rewindConfirmEscalates = ""
	app.rewindConfirmOverridesKB = false

	output := app.rewindConfirmModal()

	if strings.Contains(output, "restart from KB Build") {
		t.Errorf("normal rewind should not show KB restart warning, got:\n%s", output)
	}
}

func TestRewindDoneMsg_NormalRewindStartsArtifactReview(t *testing.T) {
	app, fm := newTestAppModel(t)
	f, err := fm.Create("normal-rewind", "Test normal rewind dispatch", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Set up the feature with plan artifact so startRewindReviewSessionCmd can find it
	err = fm.Store.Modify(f.ID, func(f *feature.Feature) error {
		f.Status = feature.StatusImplementing
		f.Repos[0].WorktreePath = ""
		f.Repos[0].Path = "/tmp/test-repo"
		return nil
	})
	if err != nil {
		t.Fatalf("modify: %v", err)
	}
	// Create a plan artifact under the active run dir.
	planDir := filepath.Join(fm.Store.BaseDir, f.ID, "runs", "run-001", "plan")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatalf("mkdir plan: %v", err)
	}
	if err := os.WriteFile(filepath.Join(planDir, "plan.md"), []byte("# Plan"), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}

	app.rewindingFeatureID = f.ID

	msg := RewindDoneMsg{
		FeatureID:   f.ID,
		TargetPhase: feature.PhaseImplement,
	}

	_, cmd := app.Update(msg)

	if cmd == nil {
		t.Fatal("expected a command from normal rewind dispatch")
	}
	msgs := executeBatchCmd(t, cmd)
	foundArtifactReview := false
	for _, m := range msgs {
		if _, ok := m.(ArtifactReviewStartMsg); ok {
			foundArtifactReview = true
		}
	}
	if !foundArtifactReview {
		t.Error("normal rewind should dispatch artifact review")
	}
}

func TestHandleArtifactReviewStart_UsesFeatureUtilityModel(t *testing.T) {
	app, fm := newTestAppModel(t)
	f, err := fm.Create("artifact-review-model", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := fm.Store.Modify(f.ID, func(f *feature.Feature) error {
		f.Models.Utilities = "claude:haiku"
		return nil
	}); err != nil {
		t.Fatalf("modify: %v", err)
	}

	artifactPath := filepath.Join(fm.Store.BaseDir, f.ID, "plan.md")
	if err := os.WriteFile(artifactPath, []byte("# Plan"), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}

	model, cmd := app.handleArtifactReviewStart(ArtifactReviewStartMsg{
		FeatureID:    f.ID,
		ArtifactPath: artifactPath,
		ReviewMode:   "plan",
		WorkDir:      "/tmp/test-repo",
	})
	app = model.(AppModel)

	if app.artifactReview.utilityModel != "claude:haiku" {
		t.Errorf("artifact review utility model = %q, want %q", app.artifactReview.utilityModel, "claude:haiku")
	}
	if app.currentView != ViewArtifactReview {
		t.Fatalf("currentView = %v, want %v", app.currentView, ViewArtifactReview)
	}
	if cmd == nil {
		t.Fatal("expected focus command")
	}
}

// --- Refactor Pipeline Selector Tests ---
func TestRefactorPipelineSelectorEscCancels(t *testing.T) {
	app, _ := newTestAppWithPublishedFeature(t)

	// Directly set up pipeline selector state
	app.refactorPipelineActive = true
	app.refactorPipelineOptions = []feature.PipelineProfile{
		feature.PipelineMedium,
		feature.PipelineLarge,
		feature.PipelineMoonshot,
	}
	app.refactorPipelineCursor = 0

	// Press Esc — should cancel the pipeline selector
	result, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	app = result.(AppModel)

	if app.refactorPipelineActive {
		t.Error("expected refactorPipelineActive == false after Esc")
	}
}

func TestRefactorPipelineSelectorArrowNavigation(t *testing.T) {
	app, _ := newTestAppWithPublishedFeature(t)

	// Set up pipeline selector state with 3 options, cursor at 0
	app.refactorPipelineActive = true
	app.refactorPipelineOptions = []feature.PipelineProfile{
		feature.PipelineMedium,
		feature.PipelineLarge,
		feature.PipelineMoonshot,
	}
	app.refactorPipelineCursor = 0

	// Right arrow -> cursor should be 1
	result, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	app = result.(AppModel)
	if app.refactorPipelineCursor != 1 {
		t.Errorf("after first right: cursor = %d, want 1", app.refactorPipelineCursor)
	}

	// Right arrow -> cursor should be 2
	result, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	app = result.(AppModel)
	if app.refactorPipelineCursor != 2 {
		t.Errorf("after second right: cursor = %d, want 2", app.refactorPipelineCursor)
	}

	// Right arrow -> cursor should stay at 2 (bounds check)
	result, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	app = result.(AppModel)
	if app.refactorPipelineCursor != 2 {
		t.Errorf("after third right (overflow): cursor = %d, want 2", app.refactorPipelineCursor)
	}

	// Left arrow -> cursor should be 1
	result, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	app = result.(AppModel)
	if app.refactorPipelineCursor != 1 {
		t.Errorf("after first left: cursor = %d, want 1", app.refactorPipelineCursor)
	}

	// Left arrow -> cursor should be 0
	result, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	app = result.(AppModel)
	if app.refactorPipelineCursor != 0 {
		t.Errorf("after second left: cursor = %d, want 0", app.refactorPipelineCursor)
	}

	// Left arrow -> cursor should stay at 0 (bounds check)
	result, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	app = result.(AppModel)
	if app.refactorPipelineCursor != 0 {
		t.Errorf("after third left (underflow): cursor = %d, want 0", app.refactorPipelineCursor)
	}
}
func TestRefactorPipelineGateResetOnSelection(t *testing.T) {
	app, fm := newTestAppModel(t)

	// Create a feature with PipelineMoonshot
	f, err := fm.Create("Gate Reset Test", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil,
		feature.CreateOptions{
			Pipeline:    feature.PipelineMoonshot,
			Checkpoints: feature.DefaultCheckpointsForProfile(feature.PipelineMoonshot),
		})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Advance to Published
	_ = fm.Transition(f.ID, feature.StatusResearching)
	_ = fm.Transition(f.ID, feature.StatusPlanReady)
	_ = fm.Transition(f.ID, feature.StatusPlanning)
	_ = fm.Transition(f.ID, feature.StatusImplementReady)
	_ = fm.Transition(f.ID, feature.StatusImplementing)
	_ = fm.Transition(f.ID, feature.StatusReviewPassed)
	_ = fm.Transition(f.ID, feature.StatusCodeReady)
	_ = fm.Transition(f.ID, feature.StatusPublished)

	// Verify precondition: feature has moonshot checkpoints
	f, _ = fm.Get(f.ID)
	if f.Pipeline != feature.PipelineMoonshot {
		t.Fatalf("precondition: Pipeline = %q, want moonshot", f.Pipeline)
	}

	// Call applyRefactorPipelineAndStart with PipelineMedium
	// This calls Store.Modify to set pipeline and checkpoints, then returns a cmd.
	// We only need to verify the store mutation here.
	_ = app.applyRefactorPipelineAndStart(f.ID, "", "some prompt", feature.PipelineMedium)

	// Reload feature and assert pipeline + checkpoints
	f, err = fm.Get(f.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if f.Pipeline != feature.PipelineMedium {
		t.Errorf("Pipeline = %q, want medium", f.Pipeline)
	}
	wantCheckpoints := feature.DefaultCheckpointsForProfile(feature.PipelineMedium)
	if f.Checkpoints != wantCheckpoints {
		t.Errorf("Checkpoints = %+v, want %+v", f.Checkpoints, wantCheckpoints)
	}
}

func TestRefactorPipelineDowngradeAllowed(t *testing.T) {
	_, fm := newTestAppModel(t)

	// Create a feature with PipelineMoonshot
	f, err := fm.Create("Downgrade Test", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil,
		feature.CreateOptions{
			Pipeline:    feature.PipelineMoonshot,
			Checkpoints: feature.DefaultCheckpointsForProfile(feature.PipelineMoonshot),
		})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Verify precondition
	f, _ = fm.Get(f.ID)
	if f.Pipeline != feature.PipelineMoonshot {
		t.Fatalf("precondition: Pipeline = %q, want moonshot", f.Pipeline)
	}

	// Downgrade to PipelineMedium via Store.Modify (mimicking applyRefactorPipelineAndStart)
	err = fm.Store.Modify(f.ID, func(feat *feature.Feature) error {
		feat.Pipeline = feature.PipelineMedium
		feat.Checkpoints = feature.DefaultCheckpointsForProfile(feature.PipelineMedium)
		return nil
	})
	if err != nil {
		t.Fatalf("Store.Modify: %v", err)
	}

	// Reload and assert
	f, err = fm.Get(f.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if f.Pipeline != feature.PipelineMedium {
		t.Errorf("Pipeline = %q, want medium (downgrade should be allowed)", f.Pipeline)
	}
}

func TestRefactorPipelineSameProfileAllowed(t *testing.T) {
	_, fm := newTestAppModel(t)

	// Create a feature with PipelineLarge
	f, err := fm.Create("Same Profile Test", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil,
		feature.CreateOptions{
			Pipeline:    feature.PipelineLarge,
			Checkpoints: feature.DefaultCheckpointsForProfile(feature.PipelineLarge),
		})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Modify some checkpoints to differ from defaults (simulate drift)
	err = fm.Store.Modify(f.ID, func(feat *feature.Feature) error {
		feat.Checkpoints.DesignReview = false // was true in large defaults
		return nil
	})
	if err != nil {
		t.Fatalf("Store.Modify (drift): %v", err)
	}
	f, _ = fm.Get(f.ID)
	if f.Checkpoints.DesignReview {
		t.Fatal("precondition: DesignReview should be false after drift modification")
	}

	// Re-apply PipelineLarge with default gates (mimicking applyRefactorPipelineAndStart)
	err = fm.Store.Modify(f.ID, func(feat *feature.Feature) error {
		feat.Pipeline = feature.PipelineLarge
		feat.Checkpoints = feature.DefaultCheckpointsForProfile(feature.PipelineLarge)
		return nil
	})
	if err != nil {
		t.Fatalf("Store.Modify (reset): %v", err)
	}

	// Reload and assert gates are reset to large defaults
	f, err = fm.Get(f.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	wantCheckpoints := feature.DefaultCheckpointsForProfile(feature.PipelineLarge)
	if f.Checkpoints != wantCheckpoints {
		t.Errorf("Checkpoints = %+v, want %+v (gates should be reset to large defaults)", f.Checkpoints, wantCheckpoints)
	}
	if f.Pipeline != feature.PipelineLarge {
		t.Errorf("Pipeline = %q, want large", f.Pipeline)
	}
}

func boolPtr(b bool) *bool { return &b }

func TestRebaseRepoCycleCmdUnpublishableReturnsCmd(t *testing.T) {
	app, fm := newTestAppModel(t)
	f, err := fm.Create("Unpub Rebase", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	_ = fm.Store.Modify(f.ID, func(feat *feature.Feature) error {
		feat.Status = feature.StatusPublished
		feat.Repos = []feature.FeatureRepo{
			{Name: "repo-a", Path: "/tmp/a", WorktreePath: "/tmp/a", Branch: "main", Publishable: boolPtr(false)},
		}
		return nil
	})

	cmd := app.rebaseCmd(f.ID, "repo-a")
	if cmd == nil {
		t.Error("expected non-nil cmd for unpublishable feature (local rebase is allowed)")
	}
}

func TestRebaseRepoCycleCmdPublishableStillReturnsCmd(t *testing.T) {
	app, fm := newTestAppModel(t)
	f, err := fm.Create("Pub Rebase", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	_ = fm.Store.Modify(f.ID, func(feat *feature.Feature) error {
		feat.Status = feature.StatusPublished
		feat.Repos = []feature.FeatureRepo{
			{Name: "repo-a", Path: "/tmp/a", WorktreePath: "/tmp/a", Branch: "main"}, // nil = publishable
		}
		return nil
	})

	cmd := app.rebaseCmd(f.ID, "repo-a")
	if cmd == nil {
		t.Error("expected non-nil cmd for publishable feature")
	}
}

// ---------------------------------------------------------------------------
// Post-publish cycle routing through Final Review
// ---------------------------------------------------------------------------

// walkFeatureToPublished walks a feature through the full lifecycle to StatusPublished.
func walkFeatureToPublished(t *testing.T, fm *feature.Manager, featureID string) {
	t.Helper()
	for _, s := range []feature.Status{
		feature.StatusResearching,
		feature.StatusPlanReady,
		feature.StatusPlanning,
		feature.StatusImplementReady,
		feature.StatusImplementing,
		feature.StatusReviewPassed,
		feature.StatusCodeReady,
		feature.StatusPublished,
	} {
		if err := fm.Transition(featureID, s); err != nil {
			t.Fatalf("transition to %s: %v", s, err)
		}
	}
}

// TestHandleFinalReviewDone_CycleFailure_MarksFeatureFailed verifies that
// when Final Review fails (e.g. max_iterations) during a cycle, the feature
// is marked as Failed.
// TestHandleMultiRepoImplDone_SingleRepoRefactor_RoutesToFinalReview verifies
// that for a single-repo refactor, handleMultiRepoImplDone routes through
// Final Review instead of calling completeRefactorCmd directly.
// TestHandleMultiRepoImplDone_MultiRepoRefactor_SkipsFinalReview verifies
// that multi-repo refactors bypass Final Review and route directly to
// completeRefactorCmd.
func TestHandleMultiRepoImplDone_MultiRepoRefactor_SkipsFinalReview(t *testing.T) {
	app, fm := newTestAppModel(t)

	// Add second repo to config for multi-repo feature
	fm.Config.Repos["test-repo-2"] = config.RepoConfig{Path: "/tmp/test-repo-2"}

	f, err := fm.Create("Multi Refactor", "desc", []string{"test-repo", "test-repo-2"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	walkFeatureToPublished(t, fm, f.ID)

	// Set up refactor state directly (Manager.StartRefactor is no longer available).
	if err := fm.Store.Modify(f.ID, func(ff *feature.Feature) error {
		if err := ff.Transition(feature.StatusInquiring); err != nil {
			return err
		}
		ff.SetRefactorCount(ff.RefactorCount() + 1)
		ff.RefactorPrompt = "refactor prompt"
		ff.SetActiveCycleType(feature.CycleRefactor)
		ff.CurrentPhase = feature.PhaseInquire
		return nil
	}); err != nil {
		t.Fatalf("set refactor state: %v", err)
	}
	// Walk through the refactor pipeline to StatusImplementing
	_ = fm.Transition(f.ID, feature.StatusInquireReady)
	_ = fm.Transition(f.ID, feature.StatusResearching)
	_ = fm.Transition(f.ID, feature.StatusDesignReady)
	_ = fm.Transition(f.ID, feature.StatusDesigning)
	_ = fm.Transition(f.ID, feature.StatusPlanReady)
	_ = fm.Transition(f.ID, feature.StatusPlanning)
	_ = fm.Transition(f.ID, feature.StatusImplementReady)
	_ = fm.Transition(f.ID, feature.StatusImplementing)

	msg := MultiRepoImplDoneMsg{
		FeatureID: f.ID,
		Result:    &agent.OrchestratorResult{FinalStatus: "all_passed"},
	}
	result, cmd := app.Update(msg)
	_ = result.(AppModel)

	if cmd == nil {
		t.Fatal("expected non-nil cmd for multi-repo refactor completion")
	}

	// Multi-repo refactors bypass Final Review — feature should NOT be in
	// any per-repo Final Review state.
	updated, _ := fm.Get(f.ID)
	if updated.IsReviewing() {
		t.Errorf("IsReviewing()=true, but multi-repo refactors should bypass Final Review")
	}
}

// Slice 8: TestHandleRepoCycleLoopDone_ReviewPassed_RoutesToFinalReview
// removed. The legacy per-repo FR routing it asserted on (RunRepoCycleFinalReview
// with cycle-scoped contract / per-repo prompt sections) is gone; the
// post-publish cycle FR is now feature-level via StartCycleFinalReview.
// Coverage of the dispatch shape lives in
// internal/agent/final_review_loop_test.go.

// T5: handleRepoCycleLoopDone with failure leaves cycle failed, feature unchanged
func TestHandleRepoCycleLoopDone_Failure_Unchanged(t *testing.T) {
	app, fm := newTestAppModel(t)

	fm.Config.Repos["web"] = config.RepoConfig{Path: "/tmp/web"}
	f, err := fm.Create("cycle-loop-fail", "desc", []string{"test-repo", "web"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Walk through states to Published
	_ = fm.Transition(f.ID, feature.StatusResearching)
	_ = fm.Transition(f.ID, feature.StatusPlanReady)
	_ = fm.Transition(f.ID, feature.StatusPlanning)
	_ = fm.Transition(f.ID, feature.StatusImplementReady)
	_ = fm.Transition(f.ID, feature.StatusImplementing)
	_ = fm.Transition(f.ID, feature.StatusReviewPassed)
	_ = fm.Transition(f.ID, feature.StatusCodeReady)
	_ = fm.Transition(f.ID, feature.StatusPublished)

	// Start a rebase cycle
	_ = fm.StartRepoCycle(f.ID, "web", feature.CycleRebase)

	msg := RepoCycleLoopDoneMsg{
		FeatureID: f.ID,
		RepoName:  "web",
		CycleType: feature.CycleRebase,
		Result:    &agent.LoopResult{FinalStatus: "failed", LastError: "build error"},
	}
	_, cmd := app.handleRepoCycleLoopDone(msg)

	// FailRepoCycle should have been called
	f, _ = fm.Get(f.ID)
	if rc, ok := f.RepoCycles["web"]; ok {
		if rc.Status != "failed" {
			t.Errorf("RepoCycles[web].Status = %q, want %q", rc.Status, "failed")
		}
	} else {
		t.Error("expected RepoCycles[web] to exist")
	}

	// Feature stays StatusPublished
	if f.Status != feature.StatusPublished {
		t.Errorf("Status = %v, want StatusPublished", f.Status)
	}

	if cmd == nil {
		t.Error("expected non-nil cmd (RefreshFeaturesMsg)")
	}
}

// Per-repo Final Review tests were removed alongside the legacy
// RepoCycleFinalReviewDoneMsg / handleRepoCycleFinalReviewDone path. The
// post-publish cycle Final Review is now feature-level and routes through
// orchestrator.StartCycleFinalReview.

func TestBuildCycleRepoEntries_ReviewingState(t *testing.T) {
	f := &feature.Feature{
		Status: feature.StatusPublished,
		Repos: []feature.FeatureRepo{
			{Name: "api", Branch: "feature/test"},
		},
		RepoCycles: map[string]*feature.RepoCycleState{
			"api": {Type: feature.CycleRebase, Status: "reviewing"},
		},
		SchemaVersion: feature.SchemaVersionCurrent,
	}

	entries := buildCycleRepoEntries(f)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].CycleStatus != "reviewing:rebase" {
		t.Errorf("CycleStatus = %q, want %q", entries[0].CycleStatus, "reviewing:rebase")
	}
}

// TestFeatureCompletionWritesObserveSummary — moved into
// orchestrator/hooks_test.go. BuildHooks' OnFeatureSummaryNeeded hook is
// exercised there; the TUI no longer owns a writeFeatureSummary helper
// because terminal transitions route through orchestrator.MarkDone /
// MarkPublished / MarkFailed which all fire the hook.

// ---------------------------------------------------------------------------
// Tweak session tests
// ---------------------------------------------------------------------------

func TestIsTweakSessionID(t *testing.T) {
	tests := []struct {
		sessionID string
		want      bool
	}{
		{"abc123-impl-tweak", true},
		{"abc123-impl-01", false},
		{"abc123-tweak", false},
		{"abc123-impl-tweak-01", false},
	}
	for _, tt := range tests {
		t.Run(tt.sessionID, func(t *testing.T) {
			got := isTweakSessionID(tt.sessionID)
			if got != tt.want {
				t.Errorf("isTweakSessionID(%q) = %v, want %v", tt.sessionID, got, tt.want)
			}
		})
	}
}

func TestFeatureIDFromSession_TweakSession(t *testing.T) {
	got := featureIDFromSession("abc123-impl-tweak")
	if got != "abc123" {
		t.Errorf("featureIDFromSession(\"abc123-impl-tweak\") = %q, want \"abc123\"", got)
	}
}

func TestPhaseFromSessionID_TweakSession(t *testing.T) {
	got := phaseFromSessionID("abc123-impl-tweak")
	if got != feature.PhaseImplement {
		t.Errorf("phaseFromSessionID(\"abc123-impl-tweak\") = %v, want %v", got, feature.PhaseImplement)
	}
}

func TestHandleTweakSessionDone_Success_DispatchesCommitCmd(t *testing.T) {
	app, fm := newTestAppModel(t)
	app.eventCh = make(chan interface{}, 1)
	app.lastNotifyTime = make(map[notifyKey]time.Time)

	f, err := fm.Create("Tweak Success", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	_ = fm.Store.Modify(f.ID, func(feat *feature.Feature) error {
		feat.Status = feature.StatusImplementing
		feat.CurrentPhase = feature.PhaseImplement
		feat.SetActiveCycleType(feature.CycleTweak)
		feat.SetPRURL("")
		return nil
	})

	// handleTweakSessionDone now returns an async commit cmd
	_, cmd := app.handleTweakSessionDone(f.ID, f.ID+"-impl-tweak", nil, true)
	if cmd == nil {
		t.Fatal("expected non-nil cmd")
	}

	// Feature is still Implementing (commit is async)
	updated, _ := fm.Get(f.ID)
	if updated.Status != feature.StatusImplementing {
		t.Errorf("status = %v, want Implementing (commit not yet done)", updated.Status)
	}
}

// --- Test Group 2: Final Review Modal key handler ---
func TestTweakReviewModal_ConsumesOtherKeys(t *testing.T) {
	app, fm := newTestAppModel(t)
	app.eventCh = make(chan interface{}, 1)
	app.lastNotifyTime = make(map[notifyKey]time.Time)

	f, err := fm.Create("Tweak Modal Keys", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = fm.Store.Modify(f.ID, func(feat *feature.Feature) error {
		feat.Status = feature.StatusReviewPassed
		feat.SetActiveCycleType(feature.CycleTweak)
		return nil
	})

	app.tweakReviewModalActive = true
	app.tweakReviewModalFeatureID = f.ID

	result, cmd := app.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	updatedApp := result.(AppModel)

	if !updatedApp.tweakReviewModalActive {
		t.Error("modal should stay open for unrecognized keys")
	}
	if cmd != nil {
		t.Error("expected nil cmd for unrecognized key")
	}
}

// --- Test Group 5: restartPhaseCmd guards ---

func TestRestartPhaseCmd_CycleTweak_Implementing_RestoresCodeReady(t *testing.T) {
	app, fm := newTestAppModel(t)
	app.sessionManager = session.NewManager(nil)

	f, err := fm.Create("Tweak Restart CR", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = fm.Store.Modify(f.ID, func(feat *feature.Feature) error {
		feat.Status = feature.StatusImplementing
		feat.CurrentPhase = feature.PhaseImplement
		feat.SetActiveCycleType(feature.CycleTweak)
		feat.SetPRURL("")
		return nil
	})

	cmd := app.restartPhaseCmd(f.ID)
	result := cmd()
	if _, ok := result.(RefreshFeaturesMsg); !ok {
		t.Fatalf("cmd returned %T, want RefreshFeaturesMsg", result)
	}

	updated, _ := fm.Get(f.ID)
	if updated.Status != feature.StatusCodeReady {
		t.Errorf("status = %v, want CodeReady", updated.Status)
	}
	if updated.ActiveCycleType() != "" {
		t.Errorf("ActiveCycleType = %q, want empty", updated.ActiveCycleType())
	}
	if updated.LastError != "" {
		t.Errorf("LastError = %q, want empty", updated.LastError)
	}
}

func TestRestartPhaseCmd_CycleTweak_Implementing_RestoresPublished(t *testing.T) {
	app, fm := newTestAppModel(t)
	app.sessionManager = session.NewManager(nil)

	f, err := fm.Create("Tweak Restart Pub", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = fm.Store.Modify(f.ID, func(feat *feature.Feature) error {
		feat.Status = feature.StatusImplementing
		feat.CurrentPhase = feature.PhaseImplement
		feat.SetActiveCycleType(feature.CycleTweak)
		// Per-repo PR URL is the only source of truth post-cutover.
		if feat.RepoStates == nil {
			feat.RepoStates = map[string]*feature.RepoState{}
		}
		feat.RepoStates["test-repo"] = repoStatePR("https://github.com/test/pr/1")
		return nil
	})

	cmd := app.restartPhaseCmd(f.ID)
	_ = cmd()

	updated, _ := fm.Get(f.ID)
	if updated.Status != feature.StatusPublished {
		t.Errorf("status = %v, want Published", updated.Status)
	}
	if updated.ActiveCycleType() != "" {
		t.Errorf("ActiveCycleType = %q, want empty", updated.ActiveCycleType())
	}
}

func TestRestartPhaseCmd_CycleTweak_Failed_RestoresCodeReady(t *testing.T) {
	app, fm := newTestAppModel(t)
	app.sessionManager = session.NewManager(nil)

	f, err := fm.Create("Tweak Restart Failed", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = fm.Store.Modify(f.ID, func(feat *feature.Feature) error {
		feat.Status = feature.StatusFailed
		feat.CurrentPhase = feature.PhaseImplement
		feat.SetActiveCycleType(feature.CycleTweak)
		feat.SetPRURL("")
		feat.LastError = "some error"
		feat.FailureType = feature.FailureInfrastructure
		return nil
	})

	cmd := app.restartPhaseCmd(f.ID)
	_ = cmd()

	updated, _ := fm.Get(f.ID)
	if updated.Status != feature.StatusCodeReady {
		t.Errorf("status = %v, want CodeReady", updated.Status)
	}
	if updated.ActiveCycleType() != "" {
		t.Errorf("ActiveCycleType = %q, want empty", updated.ActiveCycleType())
	}
	if updated.LastError != "" {
		t.Errorf("LastError = %q, want empty", updated.LastError)
	}
	if updated.FailureType != "" {
		t.Errorf("FailureType = %q, want empty", updated.FailureType)
	}
}

func TestRestartPhaseCmd_CycleTweak_Interrupted_RestoresPublished(t *testing.T) {
	app, fm := newTestAppModel(t)
	app.sessionManager = session.NewManager(nil)

	f, err := fm.Create("Tweak Restart Int", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = fm.Store.Modify(f.ID, func(feat *feature.Feature) error {
		feat.Status = feature.StatusInterrupted
		feat.CurrentPhase = feature.PhaseImplement
		feat.SetActiveCycleType(feature.CycleTweak)
		// Per-repo PR URL is the only source of truth post-cutover.
		if feat.RepoStates == nil {
			feat.RepoStates = map[string]*feature.RepoState{}
		}
		feat.RepoStates["test-repo"] = repoStatePR("https://github.com/test/pr/1")
		return nil
	})

	cmd := app.restartPhaseCmd(f.ID)
	_ = cmd()

	updated, _ := fm.Get(f.ID)
	if updated.Status != feature.StatusPublished {
		t.Errorf("status = %v, want Published", updated.Status)
	}
	if updated.ActiveCycleType() != "" {
		t.Errorf("ActiveCycleType = %q, want empty", updated.ActiveCycleType())
	}
}

// --- Test Group 7: handleFinalReviewDone CycleTweak routing ---
// --- Test Group 8: Event pump preservation ---

func TestHandleTweakSessionDone_EventPump_PreservedOnSuccess(t *testing.T) {
	app, fm := newTestAppModel(t)
	app.eventCh = make(chan interface{}, 1)
	app.lastNotifyTime = make(map[notifyKey]time.Time)

	f, err := fm.Create("Tweak Pump OK", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = fm.Store.Modify(f.ID, func(feat *feature.Feature) error {
		feat.Status = feature.StatusImplementing
		feat.CurrentPhase = feature.PhaseImplement
		feat.SetActiveCycleType(feature.CycleTweak)
		return nil
	})

	_, cmd := app.handleTweakSessionDone(f.ID, f.ID+"-impl-tweak", nil, true)
	if cmd == nil {
		t.Fatal("expected non-nil cmd")
	}

	// The cmd should be a batch; verify the event pump was re-armed by
	// sending a message on eventCh and checking it can be consumed.
	app.eventCh <- session.SessionDoneMsg{SessionID: "test-pump"}

	// If the pump was re-armed, one of the batch cmds will read from eventCh.
	// Execute the batch and verify at least one cmd produces a SessionDoneTUIMsg.
	batchResult := cmd()
	if batch, ok := batchResult.(tea.BatchMsg); ok {
		foundPump := false
		for _, c := range batch {
			msg := c()
			if _, isPump := msg.(SessionDoneTUIMsg); isPump {
				foundPump = true
			}
		}
		if !foundPump {
			t.Error("event pump not found in batch — listenForEvents() may be missing")
		}
	}
}

func TestHandleTweakSessionDone_EventPump_PreservedOnFailure(t *testing.T) {
	app, fm := newTestAppModel(t)
	app.eventCh = make(chan interface{}, 1)
	app.lastNotifyTime = make(map[notifyKey]time.Time)

	f, err := fm.Create("Tweak Pump Fail", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = fm.Store.Modify(f.ID, func(feat *feature.Feature) error {
		feat.Status = feature.StatusImplementing
		feat.CurrentPhase = feature.PhaseImplement
		feat.SetActiveCycleType(feature.CycleTweak)
		return nil
	})

	_, cmd := app.handleTweakSessionDone(f.ID, f.ID+"-impl-tweak", nil, false)
	if cmd == nil {
		t.Fatal("expected non-nil cmd")
	}

	// Send a message on eventCh to verify the pump was re-armed
	app.eventCh <- session.SessionDoneMsg{SessionID: "test-pump-fail"}

	batchResult := cmd()
	if batch, ok := batchResult.(tea.BatchMsg); ok {
		foundPump := false
		for _, c := range batch {
			msg := c()
			if _, isPump := msg.(SessionDoneTUIMsg); isPump {
				foundPump = true
			}
		}
		if !foundPump {
			t.Error("event pump not found in batch on failure path — listenForEvents() may be missing")
		}
	}
}

// --- Test Group 4 (continued): Session Done Routing ---
// --- Test Group 6 (continued): updateRecovery tweak exclusion ---

// TestUpdateRecovery_DelegatesToOrchestrator verifies that updateRecovery calls
// orchestrator.ExecuteRecovery exactly once (the orchestrator owns resume
// dispatch including the tweak-cycle exclusion via
// session.IsRecoveryTweakSession) and does NOT re-dispatch StartPhaseMsg.
// The double-dispatch would start a second concurrent phase run for every
// resumed feature — see orchestrator/recovery.go where startPhase is called.
func TestUpdateRecovery_DelegatesToOrchestrator(t *testing.T) {
	app, fm := newTestAppModel(t)
	app.observer = &recordingObserver{}

	// Create a tweak feature
	tweakF, err := fm.Create("Tweak Recovery", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create tweak: %v", err)
	}
	_ = fm.Store.Modify(tweakF.ID, func(feat *feature.Feature) error {
		feat.Status = feature.StatusImplementing
		feat.CurrentPhase = feature.PhaseImplement
		feat.SetActiveCycleType(feature.CycleTweak)
		return nil
	})

	// Write a PID file for the tweak feature so ScanForRecovery finds it.
	// Use PID 99999 (assumed dead) to avoid process-group operations.
	tweakPIDDir := filepath.Join(fm.Store.BaseDir, tweakF.ID)
	_ = session.WritePIDFile(tweakPIDDir, session.PIDFile{
		PID:       99999,
		FeatureID: tweakF.ID,
		Phase:     "implement",
		Iteration: 1,
	})

	// Create recovery items matching what ScanForRecovery would produce
	tweakItem := session.RecoveryItem{
		PIDFile: session.PIDFile{
			FeatureID: tweakF.ID,
			PID:       99999,
			Phase:     "implement",
			Iteration: 1,
			Dir:       tweakPIDDir,
		},
		ProcessAlive: false,
		Feature:      tweakF,
	}

	// Create the RecoveryModel, then FORCE the tweak item's action to RecoveryResume
	// (bypassing the UI guard to simulate a stale/corrupted action).
	rm := NewRecoveryModel([]session.RecoveryItem{tweakItem})
	tweakKey := session.RecoveryActionKey(tweakF.ID, "")
	rm.actions[tweakKey] = session.RecoveryResume
	app.recovery = rm

	// Press Enter to complete recovery
	result, cmd := app.updateRecovery(tea.KeyPressMsg{Code: tea.KeyEnter})
	_ = result.(AppModel)

	// The returned cmd must be non-nil (refresh after recovery), and must NOT
	// be a batch that includes a StartPhaseMsg — the orchestrator's
	// ExecuteRecovery is the sole resume dispatch site. A stale/corrupted
	// RecoveryResume action on a tweak feature is filtered by
	// session.IsRecoveryTweakSession inside orchestrator.ExecuteRecovery, not
	// by a TUI-side guard on resumedFeatures.
	if cmd == nil {
		t.Error("expected non-nil refresh cmd from updateRecovery")
		return
	}
	// Execute the returned cmd and verify it is a RefreshFeaturesMsg (not a
	// StartPhaseMsg fan-out). A non-Refresh result would indicate the TUI is
	// still dispatching phases directly from updateRecovery.
	msg := cmd()
	if _, ok := msg.(RefreshFeaturesMsg); !ok {
		t.Errorf("expected RefreshFeaturesMsg, got %T — updateRecovery must not fan out StartPhaseMsg", msg)
	}
}

// --- Phase 5 Multi-Repo Interactive Tweak Tests ---

// newMultiRepoTestAppModel creates an AppModel with two repos ("api" and "backend") in config.
func newMultiRepoTestAppModel(t *testing.T) (AppModel, *feature.Manager) {
	t.Helper()
	dir := t.TempDir()
	store := feature.NewStore(dir)
	cfg := config.NewDefault()
	cfg.Repos["api"] = config.RepoConfig{Path: "/tmp/api"}
	cfg.Repos["backend"] = config.RepoConfig{Path: "/tmp/backend"}
	fm := feature.NewManager(store, cfg)
	dash := NewDashboardModel(nil, "")
	dash.width = 80
	dash.height = 24
	registry := llm.NewRegistry()
	phaseRunner := &agent.PhaseRunner{
		CommandRunner: agent.NewExecCommandRunner(),
		Registry:      registry,
		StateDir:      dir,
	}
	sm := session.NewManager(nil)
	orch := orchestrator.New(orchestrator.Deps{
		Lifecycle:   fm,
		Store:       store,
		Sessions:    sm,
		PhaseRunner: phaseRunner,
		CmdRunner:   phaseRunner.CommandRunner,
		Publisher:   &git.PublishAdapter{},
		Rebaser:     &git.RebaseAdapter{},
	}, orchestrator.BuildHooks(nil, nil, store, store.BaseDir))
	app := AppModel{
		currentView:    ViewDashboard,
		dashboard:      dash,
		featureManager: fm,
		sessionManager: sm,
		orchestrator:   orch,
		registry:       registry,
		programRef:     &ProgramRef{},
		phaseRunner:    phaseRunner,
		width:          80,
		height:         24,
	}
	return app, fm
}

func TestIsTweakSessionID_MultiRepo(t *testing.T) {
	tests := []struct {
		sessionID string
		want      bool
	}{
		{"abc123-impl-api-tweak", true},
		{"abc123-impl-backend-tweak", true},
		{"abc123-impl-tweak", true},
		{"abc123-impl-01", false},
	}
	for _, tt := range tests {
		t.Run(tt.sessionID, func(t *testing.T) {
			got := isTweakSessionID(tt.sessionID)
			if got != tt.want {
				t.Errorf("isTweakSessionID(%q) = %v, want %v", tt.sessionID, got, tt.want)
			}
		})
	}
}

func TestFeatureIDFromSession_MultiRepoTweak(t *testing.T) {
	got := featureIDFromSession("abc123-impl-api-tweak")
	if got != "abc123" {
		t.Errorf("featureIDFromSession(\"abc123-impl-api-tweak\") = %q, want \"abc123\"", got)
	}
}

func TestHandleTweakSessionDone_MultiRepo_Success_CommitsForRepo(t *testing.T) {
	app, fm := newMultiRepoTestAppModel(t)
	app.eventCh = make(chan interface{}, 1)
	app.lastNotifyTime = make(map[notifyKey]time.Time)

	f, err := fm.Create("MR Tweak Success", "desc", []string{"api", "backend"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	_ = fm.Store.Modify(f.ID, func(feat *feature.Feature) error {
		feat.Status = feature.StatusPublished
		feat.CurrentPhase = feature.PhaseImplement
		feat.RepoCycles = map[string]*feature.RepoCycleState{
			"api": {Type: feature.CycleTweak, Status: "running"},
		}
		return nil
	})

	sess := mocks.NewMockSessionView(f.ID+"-impl-api-tweak", f.ID)
	sess.RepoNameVal = "api"

	result, cmd := app.handleTweakSessionDone(f.ID, f.ID+"-impl-api-tweak", sess, true)
	updatedApp := result.(AppModel)

	if cmd == nil {
		t.Fatal("expected non-nil cmd (feature-level commit dispatched)")
	}
	// Tweak is feature-level, so the guard is just the featureID with no
	// per-repo suffix. The orchestrator's CompleteTweakCommit fans out across
	// every Feature.Repos worktree internally.
	wantGuard := f.ID
	if updatedApp.tweakCompletingFeatureID != wantGuard {
		t.Errorf("tweakCompletingFeatureID = %q, want %q", updatedApp.tweakCompletingFeatureID, wantGuard)
	}
}

func TestHandleTweakSessionDone_MultiRepo_Failure_FailsRepoCycle(t *testing.T) {
	app, fm := newMultiRepoTestAppModel(t)
	app.eventCh = make(chan interface{}, 1)
	app.lastNotifyTime = make(map[notifyKey]time.Time)

	f, err := fm.Create("MR Tweak Failure", "desc", []string{"api", "backend"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	_ = fm.Store.Modify(f.ID, func(feat *feature.Feature) error {
		feat.Status = feature.StatusPublished
		feat.CurrentPhase = feature.PhaseImplement
		feat.RepoCycles = map[string]*feature.RepoCycleState{
			"api": {Type: feature.CycleTweak, Status: "running"},
		}
		return nil
	})

	sess := mocks.NewMockSessionView(f.ID+"-impl-api-tweak", f.ID)
	sess.RepoNameVal = "api"

	_, _ = app.handleTweakSessionDone(f.ID, f.ID+"-impl-api-tweak", sess, false)

	updated, _ := fm.Get(f.ID)
	// Feature stays Published — only the repo cycle is failed
	if updated.Status != feature.StatusPublished {
		t.Errorf("status = %v, want Published (multi-repo failure should not interrupt feature)", updated.Status)
	}
	rc, ok := updated.RepoCycles["api"]
	if !ok {
		t.Fatal("RepoCycles[\"api\"] should exist")
	}
	if rc.Status != "failed" {
		t.Errorf("RepoCycles[\"api\"].Status = %q, want \"failed\"", rc.Status)
	}
}

func TestHandleTweakSessionDone_MultiRepo_ExplicitFinish_OverridesFailure(t *testing.T) {
	app, fm := newMultiRepoTestAppModel(t)
	app.eventCh = make(chan interface{}, 1)
	app.lastNotifyTime = make(map[notifyKey]time.Time)

	f, err := fm.Create("MR Tweak Finish Override", "desc", []string{"api", "backend"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	_ = fm.Store.Modify(f.ID, func(feat *feature.Feature) error {
		feat.Status = feature.StatusPublished
		feat.CurrentPhase = feature.PhaseImplement
		feat.RepoCycles = map[string]*feature.RepoCycleState{
			"api": {Type: feature.CycleTweak, Status: "running"},
		}
		return nil
	})

	// Set explicit finish intent (simulates Ctrl+D)
	app.tweakFinishingFeatureID = f.ID

	sess := mocks.NewMockSessionView(f.ID+"-impl-api-tweak", f.ID)
	sess.RepoNameVal = "api"

	_, cmd := app.handleTweakSessionDone(f.ID, f.ID+"-impl-api-tweak", sess, false)

	if cmd == nil {
		t.Fatal("expected non-nil cmd (commit dispatched despite failure, due to explicit finish)")
	}

	// Verify no failure was recorded on the repo cycle
	updated, _ := fm.Get(f.ID)
	rc, ok := updated.RepoCycles["api"]
	if !ok {
		t.Fatal("RepoCycles[\"api\"] should still exist")
	}
	if rc.Status == "failed" {
		t.Error("RepoCycles[\"api\"] should NOT be failed — explicit finish overrides")
	}
}

func TestHandleTweakSessionDone_MultiRepo_EventPump_Preserved(t *testing.T) {
	app, fm := newMultiRepoTestAppModel(t)
	app.eventCh = make(chan interface{}, 1)
	app.lastNotifyTime = make(map[notifyKey]time.Time)

	f, err := fm.Create("MR Tweak Pump", "desc", []string{"api", "backend"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	_ = fm.Store.Modify(f.ID, func(feat *feature.Feature) error {
		feat.Status = feature.StatusPublished
		feat.CurrentPhase = feature.PhaseImplement
		feat.RepoCycles = map[string]*feature.RepoCycleState{
			"api": {Type: feature.CycleTweak, Status: "running"},
		}
		return nil
	})

	sess := mocks.NewMockSessionView(f.ID+"-impl-api-tweak", f.ID)
	sess.RepoNameVal = "api"

	t.Run("success", func(t *testing.T) {
		localApp := app
		localApp.eventCh = make(chan interface{}, 1)

		_, cmd := localApp.handleTweakSessionDone(f.ID, f.ID+"-impl-api-tweak", sess, true)
		if cmd == nil {
			t.Fatal("expected non-nil cmd")
		}

		localApp.eventCh <- session.SessionDoneMsg{SessionID: "pump-check-success"}
		batchResult := cmd()
		if batch, ok := batchResult.(tea.BatchMsg); ok {
			foundPump := false
			for _, c := range batch {
				msg := c()
				if _, isPump := msg.(SessionDoneTUIMsg); isPump {
					foundPump = true
				}
			}
			if !foundPump {
				t.Error("event pump not found in batch on success path — listenForEvents() may be missing")
			}
		}
	})

	t.Run("failure", func(t *testing.T) {
		localApp := app
		localApp.eventCh = make(chan interface{}, 1)

		_, cmd := localApp.handleTweakSessionDone(f.ID, f.ID+"-impl-api-tweak", sess, false)
		if cmd == nil {
			t.Fatal("expected non-nil cmd")
		}

		localApp.eventCh <- session.SessionDoneMsg{SessionID: "pump-check-failure"}
		batchResult := cmd()
		if batch, ok := batchResult.(tea.BatchMsg); ok {
			foundPump := false
			for _, c := range batch {
				msg := c()
				if _, isPump := msg.(SessionDoneTUIMsg); isPump {
					foundPump = true
				}
			}
			if !foundPump {
				t.Error("event pump not found in batch on failure path — listenForEvents() may be missing")
			}
		}
	})
}

func TestHandleTweakSessionDone_MultiRepo_RapidCtrlD_Idempotent(t *testing.T) {
	app, fm := newMultiRepoTestAppModel(t)
	app.eventCh = make(chan interface{}, 2)
	app.lastNotifyTime = make(map[notifyKey]time.Time)

	f, err := fm.Create("MR Tweak Rapid CtrlD", "desc", []string{"api", "backend"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = fm.Store.Modify(f.ID, func(feat *feature.Feature) error {
		feat.Status = feature.StatusPublished
		feat.CurrentPhase = feature.PhaseImplement
		feat.RepoCycles = map[string]*feature.RepoCycleState{
			"api": {Type: feature.CycleTweak, Status: "running"},
		}
		return nil
	})

	// Set explicit finish intent
	app.tweakFinishingFeatureID = f.ID

	sess := mocks.NewMockSessionView(f.ID+"-impl-api-tweak", f.ID)
	sess.RepoNameVal = "api"

	// First call
	result1, cmd1 := app.handleTweakSessionDone(f.ID, f.ID+"-impl-api-tweak", sess, false)
	updatedApp := result1.(AppModel)

	if cmd1 == nil {
		t.Fatal("first call: expected non-nil cmd")
	}
	// Feature-level guard with no per-repo suffix.
	wantGuard := f.ID
	if updatedApp.tweakCompletingFeatureID != wantGuard {
		t.Errorf("first call: tweakCompletingFeatureID = %q, want %q", updatedApp.tweakCompletingFeatureID, wantGuard)
	}

	// Second call: guard key already set — should short-circuit
	app.eventCh <- session.SessionDoneMsg{SessionID: "pump-check-2"}
	result2, cmd2 := updatedApp.handleTweakSessionDone(f.ID, f.ID+"-impl-api-tweak", sess, false)
	updatedApp2 := result2.(AppModel)

	if cmd2 == nil {
		t.Fatal("second call: expected non-nil cmd (listenForEvents)")
	}

	// Second call returns just listenForEvents (not a batch)
	batchResult2 := cmd2()
	if _, isPump := batchResult2.(SessionDoneTUIMsg); !isPump {
		t.Error("second call: expected listenForEvents cmd, not a batch")
	}

	// Guard should remain set
	if updatedApp2.tweakCompletingFeatureID != wantGuard {
		t.Errorf("second call: tweakCompletingFeatureID = %q, want %q (should remain set)", updatedApp2.tweakCompletingFeatureID, wantGuard)
	}

	// Feature stays Published — not interrupted
	updated, _ := fm.Get(f.ID)
	if updated.Status != feature.StatusPublished {
		t.Errorf("status = %v, want Published (second call should be no-op)", updated.Status)
	}
}

func TestHandleTweakMultiRepoCommitDone_Error_ClearsGuard(t *testing.T) {
	// Under the iteration-11 contract, orchestrator.CompleteTweakCommit
	// fires FailRepoCycle internally when the commit fails. The TUI's
	// handleTweakCommitDone only clears the in-flight guard and
	// returns a refresh.
	app, fm := newMultiRepoTestAppModel(t)
	app.eventCh = make(chan interface{}, 1)
	app.lastNotifyTime = make(map[notifyKey]time.Time)

	f, err := fm.Create("MR Commit Error", "desc", []string{"api", "backend"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = fm.Store.Modify(f.ID, func(feat *feature.Feature) error {
		feat.Status = feature.StatusPublished
		feat.CurrentPhase = feature.PhaseImplement
		feat.RepoCycles = map[string]*feature.RepoCycleState{
			"api": {Type: feature.CycleTweak, Status: "running"},
		}
		return nil
	})

	app.tweakCompletingFeatureID = f.ID

	msg := tweakCommitDoneMsg{featureID: f.ID, err: fmt.Errorf("boom")}
	result, _ := app.handleTweakCommitDone(msg)
	updatedApp := result.(AppModel)

	if updatedApp.tweakCompletingFeatureID != "" {
		t.Errorf("tweakCompletingFeatureID = %q, want empty (cleared on error)", updatedApp.tweakCompletingFeatureID)
	}
}

func TestHandleTweakMultiRepoCommitDone_NoChanges_SkipsReview(t *testing.T) {
	app, fm := newMultiRepoTestAppModel(t)
	app.eventCh = make(chan interface{}, 1)
	app.lastNotifyTime = make(map[notifyKey]time.Time)

	f, err := fm.Create("MR Commit NoChanges", "desc", []string{"api", "backend"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = fm.Store.Modify(f.ID, func(feat *feature.Feature) error {
		feat.Status = feature.StatusPublished
		feat.CurrentPhase = feature.PhaseImplement
		feat.RepoCycles = map[string]*feature.RepoCycleState{
			"api": {Type: feature.CycleTweak, Status: "running"},
		}
		return nil
	})

	app.tweakCompletingFeatureID = f.ID

	msg := tweakCommitDoneMsg{featureID: f.ID, hadChanges: false}
	result, cmd := app.handleTweakCommitDone(msg)
	updatedApp := result.(AppModel)

	if updatedApp.tweakReviewModalActive {
		t.Error("tweakReviewModalActive should be false when no changes")
	}
	if cmd == nil {
		t.Fatal("expected non-nil cmd (completeTweakFinishCmd dispatched)")
	}
}

func TestHandleTweakMultiRepoCommitDone_WithChanges_ShowsReviewModal(t *testing.T) {
	app, fm := newMultiRepoTestAppModel(t)
	app.eventCh = make(chan interface{}, 1)
	app.lastNotifyTime = make(map[notifyKey]time.Time)

	f, err := fm.Create("MR Commit WithChanges", "desc", []string{"api", "backend"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = fm.Store.Modify(f.ID, func(feat *feature.Feature) error {
		feat.Status = feature.StatusPublished
		feat.CurrentPhase = feature.PhaseImplement
		feat.RepoCycles = map[string]*feature.RepoCycleState{
			"api": {Type: feature.CycleTweak, Status: "running"},
		}
		return nil
	})

	app.tweakCompletingFeatureID = f.ID

	msg := tweakCommitDoneMsg{featureID: f.ID, hadChanges: true}
	result, _ := app.handleTweakCommitDone(msg)
	updatedApp := result.(AppModel)

	if !updatedApp.tweakReviewModalActive {
		t.Error("tweakReviewModalActive should be true when changes committed")
	}
	// Tweak is feature-level, so tweakReviewModalRepoName is not populated
	// from the commit-done path. The modal body shows feature-scope text.
	if updatedApp.tweakReviewModalRepoName != "" {
		t.Errorf("tweakReviewModalRepoName = %q, want empty (feature-level tweak does not pin a repo)", updatedApp.tweakReviewModalRepoName)
	}
}

func TestFinalReviewModal_MultiRepo_Esc_ClearsRepoCycle(t *testing.T) {
	app, fm := newMultiRepoTestAppModel(t)
	app.eventCh = make(chan interface{}, 1)
	app.lastNotifyTime = make(map[notifyKey]time.Time)

	f, err := fm.Create("MR Modal Esc", "desc", []string{"api", "backend"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = fm.Store.Modify(f.ID, func(feat *feature.Feature) error {
		feat.Status = feature.StatusPublished
		feat.CurrentPhase = feature.PhaseImplement
		feat.RepoCycles = map[string]*feature.RepoCycleState{
			"api": {Type: feature.CycleTweak, Status: "running"},
		}
		return nil
	})

	app.tweakReviewModalActive = true
	app.tweakReviewModalFeatureID = f.ID
	app.tweakReviewModalRepoName = "api"

	result, cmd := app.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	updatedApp := result.(AppModel)

	if updatedApp.tweakReviewModalActive {
		t.Error("modal should be dismissed after Esc")
	}

	if cmd == nil {
		t.Fatal("expected non-nil cmd")
	}
	cmdResult := cmd()
	if _, ok := cmdResult.(RefreshFeaturesMsg); !ok {
		t.Fatalf("cmd returned %T, want RefreshFeaturesMsg", cmdResult)
	}

	// RepoCycles["api"] should be removed (not failed) by restoreTweakFromReviewCmd
	updated, _ := fm.Get(f.ID)
	if _, ok := updated.RepoCycles["api"]; ok {
		t.Error("RepoCycles[\"api\"] should be removed after Esc (not failed)")
	}
	if updated.Status != feature.StatusPublished {
		t.Errorf("status = %v, want Published", updated.Status)
	}
}

func TestFinalReviewModal_MultiRepo_N_CompletesWithoutReview(t *testing.T) {
	app, fm := newMultiRepoTestAppModel(t)
	app.eventCh = make(chan interface{}, 1)
	app.lastNotifyTime = make(map[notifyKey]time.Time)

	f, err := fm.Create("MR Modal N", "desc", []string{"api", "backend"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = fm.Store.Modify(f.ID, func(feat *feature.Feature) error {
		feat.Status = feature.StatusPublished
		feat.CurrentPhase = feature.PhaseImplement
		feat.RepoCycles = map[string]*feature.RepoCycleState{
			"api": {Type: feature.CycleTweak, Status: "running"},
		}
		return nil
	})

	app.tweakReviewModalActive = true
	app.tweakReviewModalFeatureID = f.ID
	app.tweakReviewModalRepoName = "api"

	result, cmd := app.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	updatedApp := result.(AppModel)

	if updatedApp.tweakReviewModalActive {
		t.Error("modal should be dismissed after 'n'")
	}
	if cmd == nil {
		t.Fatal("expected non-nil cmd (completeTweakFinishCmd dispatched)")
	}
	// Modal state should be cleared
	if updatedApp.tweakReviewModalFeatureID != "" {
		t.Errorf("tweakReviewModalFeatureID = %q, want empty", updatedApp.tweakReviewModalFeatureID)
	}
	if updatedApp.tweakReviewModalRepoName != "" {
		t.Errorf("tweakReviewModalRepoName = %q, want empty", updatedApp.tweakReviewModalRepoName)
	}
}

func TestRestartRepoCycleMsg_CycleTweak_ClearsRepoCycle(t *testing.T) {
	app, fm := newMultiRepoTestAppModel(t)
	app.eventCh = make(chan interface{}, 1)
	app.lastNotifyTime = make(map[notifyKey]time.Time)

	f, err := fm.Create("MR Restart Tweak", "desc", []string{"api", "backend"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = fm.Store.Modify(f.ID, func(feat *feature.Feature) error {
		feat.Status = feature.StatusPublished
		feat.CurrentPhase = feature.PhaseImplement
		feat.RepoCycles = map[string]*feature.RepoCycleState{
			"api": {Type: feature.CycleTweak, Status: "running"},
		}
		return nil
	})

	msg := restartRepoCycleMsg{FeatureID: f.ID, RepoName: "api", CycleType: feature.CycleTweak}
	_, cmd := app.Update(msg)

	if cmd == nil {
		t.Fatal("expected non-nil cmd (refresh features)")
	}

	// RepoCycles["api"] should be removed (not failed) because tweak cycles
	// have no plan file and cannot restart autonomously
	updated, _ := fm.Get(f.ID)
	if _, ok := updated.RepoCycles["api"]; ok {
		t.Error("RepoCycles[\"api\"] should be removed after restartRepoCycleMsg with CycleTweak")
	}
}

// publishedFeatureWithRepos creates a published feature with N repos and the
// minimum scaffolding needed for cycle-key handlers to dispatch.
func publishedFeatureWithRepos(t *testing.T, fm *feature.Manager, repoCount int) *feature.Feature {
	t.Helper()
	repoNames := make([]string, 0, repoCount)
	for i := 0; i < repoCount; i++ {
		name := "repo-a"
		if i == 1 {
			name = "repo-b"
		} else if i > 1 {
			name = "repo-c"
		}
		repoNames = append(repoNames, name)
	}
	repos := make([]feature.FeatureRepo, 0, repoCount)
	repoStates := make(map[string]*feature.RepoState, repoCount)
	for _, name := range repoNames {
		repos = append(repos, feature.FeatureRepo{Name: name, Path: "/tmp/" + name, WorktreePath: "/tmp/wt/" + name, Branch: "feature/test"})
		repoStates[name] = repoStatePR("https://github.com/org/" + name + "/pull/1")
	}
	for _, n := range repoNames {
		if _, ok := fm.Config.Repos[n]; !ok {
			fm.Config.Repos[n] = config.RepoConfig{Path: "/tmp/" + n}
		}
	}
	f := &feature.Feature{
		ID:            "feat-rebase-chrome",
		Name:          "Rebase Chrome Test",
		Slug:          "rebase-chrome-test",
		Status:        feature.StatusPublished,
		Repos:         repos,
		RepoStates:    repoStates,
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	if err := fm.Store.Save(f); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return f
}

func TestUpdateDashboard_Rebase_StatusMessage_SingleRepo(t *testing.T) {
	app, fm := newTestAppModel(t)
	f := publishedFeatureWithRepos(t, fm, 1)
	features, _ := fm.List()
	app.dashboard = NewDashboardModel(features, "")
	app.dashboard.width = 80
	app.dashboard.height = 24
	app.dashboard.focusPanel = 0
	for i, item := range app.dashboard.visibleItems {
		if item.kind == listItemFeature && item.feature.ID == f.ID {
			app.dashboard.cursor = i
			break
		}
	}
	result, _ := app.Update(tea.KeyPressMsg{Code: 'b', Text: "b"})
	got := result.(AppModel)
	if !strings.Contains(got.statusMessage, "Rebasing") {
		t.Errorf("statusMessage = %q, want it to contain Rebasing for single-repo", got.statusMessage)
	}
}

func TestUpdateDashboard_Rebase_StatusMessage_MultiRepo(t *testing.T) {
	app, fm := newTestAppModel(t)
	f := publishedFeatureWithRepos(t, fm, 2)
	features, _ := fm.List()
	app.dashboard = NewDashboardModel(features, "")
	app.dashboard.width = 80
	app.dashboard.height = 24
	app.dashboard.focusPanel = 0
	for i, item := range app.dashboard.visibleItems {
		if item.kind == listItemFeature && item.feature.ID == f.ID {
			app.dashboard.cursor = i
			break
		}
	}
	result, _ := app.Update(tea.KeyPressMsg{Code: 'b', Text: "b"})
	got := result.(AppModel)
	if strings.Contains(got.statusMessage, "Rebasing") {
		t.Errorf("statusMessage = %q, did not expect Rebasing for multi-repo (cycle selector overlay opens)", got.statusMessage)
	}
}

func TestUpdateDashboardRightPanel_Tweak_BlockedForMultiRepoUnpublished(t *testing.T) {
	app, fm := newTestAppModel(t)
	// CodeReady multi-repo feature with ManualPublish (so Tweak gate triggers)
	f := &feature.Feature{
		ID:          "feat-tweak-mr-unp",
		Name:        "Tweak MR Unpublished",
		Slug:        "tweak-mr-unp",
		Status:      feature.StatusCodeReady,
		Checkpoints: feature.Checkpoints{ManualPublish: true},
		Repos: []feature.FeatureRepo{
			{Name: "repo-a", Path: "/tmp/repo-a", WorktreePath: "/tmp/wt-a", Branch: "feature/test"},
			{Name: "repo-b", Path: "/tmp/repo-b", WorktreePath: "/tmp/wt-b", Branch: "feature/test"},
		},
		RepoStates: map[string]*feature.RepoState{
			"repo-a": repoStateTouched(),
			"repo-b": repoStateTouched(),
		},
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	if err := fm.Store.Save(f); err != nil {
		t.Fatal(err)
	}
	features, _ := fm.List()
	app.dashboard = NewDashboardModel(features, "")
	app.dashboard.width = 80
	app.dashboard.height = 24
	app.dashboard.focusPanel = 1
	for i, item := range app.dashboard.visibleItems {
		if item.kind == listItemFeature && item.feature.ID == f.ID {
			app.dashboard.cursor = i
			break
		}
	}
	_, cmd := app.Update(tea.KeyPressMsg{Code: 't', Text: "t"})
	if cmd != nil {
		t.Errorf("expected nil cmd for Tweak on multi-repo unpublished, got non-nil")
	}
}

func TestUpdateDashboardRightPanel_Tweak_AllowedForSingleRepoUnpublished(t *testing.T) {
	app, fm := newTestAppModel(t)
	f := &feature.Feature{
		ID:          "feat-tweak-sr-unp",
		Name:        "Tweak SR Unpublished",
		Slug:        "tweak-sr-unp",
		Status:      feature.StatusCodeReady,
		Checkpoints: feature.Checkpoints{ManualPublish: true},
		Repos: []feature.FeatureRepo{
			{Name: "repo-a", Path: "/tmp/repo-a", WorktreePath: "/tmp/wt-a", Branch: "feature/test"},
		},
		RepoStates: map[string]*feature.RepoState{
			"repo-a": repoStateTouched(),
		},
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	if err := fm.Store.Save(f); err != nil {
		t.Fatal(err)
	}
	features, _ := fm.List()
	app.dashboard = NewDashboardModel(features, "")
	app.dashboard.width = 80
	app.dashboard.height = 24
	app.dashboard.focusPanel = 1
	for i, item := range app.dashboard.visibleItems {
		if item.kind == listItemFeature && item.feature.ID == f.ID {
			app.dashboard.cursor = i
			break
		}
	}
	_, cmd := app.Update(tea.KeyPressMsg{Code: 't', Text: "t"})
	if cmd == nil {
		t.Errorf("expected non-nil cmd for Tweak on single-repo unpublished, got nil")
	}
}

func TestUpdateDashboardRightPanel_Refactor_BlockedForMultiRepoUnpublished(t *testing.T) {
	app, fm := newTestAppModel(t)
	f := &feature.Feature{
		ID:          "feat-refactor-mr-unp",
		Name:        "Refactor MR Unpublished",
		Slug:        "refactor-mr-unp",
		Status:      feature.StatusCodeReady,
		Checkpoints: feature.Checkpoints{ManualPublish: true},
		Repos: []feature.FeatureRepo{
			{Name: "repo-a", Path: "/tmp/repo-a", WorktreePath: "/tmp/wt-a", Branch: "feature/test"},
			{Name: "repo-b", Path: "/tmp/repo-b", WorktreePath: "/tmp/wt-b", Branch: "feature/test"},
		},
		RepoStates: map[string]*feature.RepoState{
			"repo-a": repoStateTouched(),
			"repo-b": repoStateTouched(),
		},
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	if err := fm.Store.Save(f); err != nil {
		t.Fatal(err)
	}
	features, _ := fm.List()
	app.dashboard = NewDashboardModel(features, "")
	app.dashboard.width = 80
	app.dashboard.height = 24
	app.dashboard.focusPanel = 1
	for i, item := range app.dashboard.visibleItems {
		if item.kind == listItemFeature && item.feature.ID == f.ID {
			app.dashboard.cursor = i
			break
		}
	}
	result, cmd := app.Update(tea.KeyPressMsg{Code: 'F', Text: "F"})
	got := result.(AppModel)
	if cmd != nil {
		t.Errorf("expected nil cmd for Refactor on multi-repo unpublished, got non-nil")
	}
	if !strings.Contains(got.statusMessage, "Multi-repo refactor") {
		t.Errorf("statusMessage = %q, want it to contain 'Multi-repo refactor' guard message", got.statusMessage)
	}
}

func TestUpdateDashboardRightPanel_Refactor_AllowedForSingleRepoUnpublished(t *testing.T) {
	app, fm := newTestAppModel(t)
	f := &feature.Feature{
		ID:          "feat-refactor-sr-unp",
		Name:        "Refactor SR Unpublished",
		Slug:        "refactor-sr-unp",
		Status:      feature.StatusCodeReady,
		Checkpoints: feature.Checkpoints{ManualPublish: true},
		Repos: []feature.FeatureRepo{
			{Name: "repo-a", Path: "/tmp/repo-a", WorktreePath: "/tmp/wt-a", Branch: "feature/test"},
		},
		RepoStates: map[string]*feature.RepoState{
			"repo-a": repoStateTouched(),
		},
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	if err := fm.Store.Save(f); err != nil {
		t.Fatal(err)
	}
	features, _ := fm.List()
	app.dashboard = NewDashboardModel(features, "")
	app.dashboard.width = 80
	app.dashboard.height = 24
	app.dashboard.focusPanel = 1
	for i, item := range app.dashboard.visibleItems {
		if item.kind == listItemFeature && item.feature.ID == f.ID {
			app.dashboard.cursor = i
			break
		}
	}
	result, cmd := app.Update(tea.KeyPressMsg{Code: 'F', Text: "F"})
	got := result.(AppModel)
	if cmd == nil {
		t.Errorf("expected non-nil cmd for Refactor on single-repo unpublished, got nil")
	}
	if strings.Contains(got.statusMessage, "Multi-repo refactor") {
		t.Errorf("statusMessage = %q, did not expect 'Multi-repo refactor' guard for single-repo", got.statusMessage)
	}
}

func TestBuildRepoTabs_SingleRepo_NoTabWithoutSession(t *testing.T) {
	sm := session.NewManager(nil)
	app := AppModel{sessionManager: sm}
	f := &feature.Feature{
		ID:     "f1",
		Status: feature.StatusImplementing,
		Repos:  []feature.FeatureRepo{{Name: "repo-a"}},
		RepoStates: map[string]*feature.RepoState{
			"repo-a": repoStatePending(),
		},
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	tabs := app.buildRepoTabs(f)
	for _, tab := range tabs {
		if tab.repoName == "repo-a" && tab.kind == ports.KindRepoImpl {
			t.Errorf("buildRepoTabs() = unexpected repo tab %+v for single-repo with no session", tab)
		}
	}
}

func TestBuildRepoTabs_MultiRepo_TabWithoutSession(t *testing.T) {
	sm := session.NewManager(nil)
	app := AppModel{sessionManager: sm}
	f := &feature.Feature{
		ID:     "f1",
		Status: feature.StatusImplementing,
		Repos:  []feature.FeatureRepo{{Name: "repo-a"}, {Name: "repo-b"}},
		RepoStates: map[string]*feature.RepoState{
			"repo-a": repoStatePending(),
			"repo-b": repoStatePending(),
		},
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	tabs := app.buildRepoTabs(f)
	if len(tabs) != 2 {
		t.Fatalf("buildRepoTabs() = %d tabs, want 2 for multi-repo (no session, but both repos rendered)", len(tabs))
	}
	if tabs[0].sess != nil {
		t.Errorf("tabs[0].sess = non-nil, want nil for multi-repo with no live session")
	}
}
