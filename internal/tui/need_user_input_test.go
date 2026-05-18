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
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// writeTestGate persists a NeedUserInputRecord under t.TempDir/need-user-input.yaml
// with the supplied prompts/answers and returns the absolute path.
func writeTestGate(t *testing.T, summary string, prompts []string, answers []string) string {
	t.Helper()
	dir := t.TempDir()
	rec := agent.NeedUserInputRecord{Summary: summary, Iteration: 1}
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
		t.Fatalf("seed gate: %v", err)
	}
	return path
}

// newTestNeedUserInputReview wraps NewArtifactReviewModel for the
// need-user-input questionnaire mode used by every test below.
func newTestNeedUserInputReview(t *testing.T, featureID, gatePath string) ArtifactReviewModel {
	t.Helper()
	return NewArtifactReviewModel(
		gatePath, featureID, reviewModeNeedUserInput,
		feature.PhaseImplement, 80, 24, nil, "", nil,
	)
}

// TestNeedUserInputOverlayBlocksEmptyResume locks in that selecting
// "Resume implementation" while at least one answer is empty does NOT
// emit a NeedUserInputDecisionMsg.
func TestNeedUserInputOverlayBlocksEmptyResume(t *testing.T) {
	gatePath := writeTestGate(t, "Decide.", []string{"Q1", "Q2"}, []string{"only A1"})
	m := newTestNeedUserInputReview(t, "feat-1", gatePath)
	if m.AllAnswered() {
		t.Fatalf("test setup: expected gate not all answered")
	}

	// Open the menu (Ctrl+D).
	m, _ = m.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	if !m.MenuOpen() {
		t.Fatalf("menu should be open after Ctrl+D")
	}
	// Press enter on the default selection (Resume).
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		// If a cmd fired, it must NOT be a decision message.
		out := cmd()
		if dm, ok := out.(NeedUserInputDecisionMsg); ok {
			t.Fatalf("blocked resume must not emit a decision; got %+v", dm)
		}
	}
}

// TestNeedUserInputOverlayResumeWhenAnswered locks in the happy path:
// once every answer is non-empty, the questionnaire emits a Decision="resume"
// message and transitions to a decided state.
func TestNeedUserInputOverlayResumeWhenAnswered(t *testing.T) {
	gatePath := writeTestGate(t, "Decide.", []string{"Q1"}, []string{"A1"})
	m := newTestNeedUserInputReview(t, "feat-2", gatePath)
	if !m.AllAnswered() {
		t.Fatalf("test setup: expected gate fully answered")
	}

	// Open the menu and select Resume.
	m, _ = m.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	model, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatalf("expected a Cmd carrying the resume decision")
	}
	out := cmd()
	dm, ok := out.(NeedUserInputDecisionMsg)
	if !ok {
		t.Fatalf("expected NeedUserInputDecisionMsg; got %T (%+v)", out, out)
	}
	if dm.FeatureID != "feat-2" || dm.Decision != "resume" {
		t.Errorf("decision = %+v, want feat-2/resume", dm)
	}
	if !model.Decided() {
		t.Error("model should report Decided() after resume decision")
	}
}

// TestNeedUserInputOverlayPersistsDraftOnDetach asserts that draft answers
// typed into the questionnaire survive a Ctrl+D detach. This is the
// round-trip the design requires for restart safety.
func TestNeedUserInputOverlayPersistsDraftOnDetach(t *testing.T) {
	gatePath := writeTestGate(t, "Decide.", []string{"Q1", "Q2"}, []string{"", ""})
	m := newTestNeedUserInputReview(t, "feat-3", gatePath)

	m = m.SetAnswer(0, "draft 1").SetAnswer(1, "draft 2")
	// Trigger Ctrl+D — the artifact-review shell persists draft answers
	// before opening the menu so detach is safe.
	m, _ = m.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})

	loaded, err := agent.ReadNeedUserInputRecord(gatePath)
	if err != nil {
		t.Fatalf("reload gate: %v", err)
	}
	if loaded.Questions[0].Answer != "draft 1" || loaded.Questions[1].Answer != "draft 2" {
		t.Errorf("answers not persisted: %+v", loaded.Questions)
	}
}

// TestNeedUserInputOverlayPersistsDraftPerKeystroke locks in the
// restart-recovery contract: every editing keystroke flushes the in-memory
// record to disk so a hard app restart while the questionnaire is open
// recovers the partial answer from the persisted gate artifact, without
// requiring the user to first hit Ctrl+D / detach / commit. This is the
// gap the iteration-02 reviewer flagged.
func TestNeedUserInputOverlayPersistsDraftPerKeystroke(t *testing.T) {
	gatePath := writeTestGate(t, "Decide.", []string{"Q1"}, []string{""})
	m := newTestNeedUserInputReview(t, "feat-keystroke", gatePath)

	// Type a single character. No Ctrl+D, no detach, no menu commit.
	m, _ = m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})

	loaded, err := agent.ReadNeedUserInputRecord(gatePath)
	if err != nil {
		t.Fatalf("reload gate: %v", err)
	}
	if len(loaded.Questions) != 1 || loaded.Questions[0].Answer != "a" {
		t.Errorf("keystroke not flushed to disk: %+v", loaded.Questions)
	}

	// A second keystroke must extend the persisted answer in place — proves
	// the flush happens on every edit, not just the first.
	m, _ = m.Update(tea.KeyPressMsg{Code: 'b', Text: "b"})
	loaded, err = agent.ReadNeedUserInputRecord(gatePath)
	if err != nil {
		t.Fatalf("reload gate after second keystroke: %v", err)
	}
	if loaded.Questions[0].Answer != "ab" {
		t.Errorf("second keystroke not flushed: %q", loaded.Questions[0].Answer)
	}
}

// TestNeedUserInputOverlayMenuLabels verifies the menu text gates the
// resume label on AllAnswered so users see why resume is disabled.
func TestNeedUserInputOverlayMenuLabels(t *testing.T) {
	gatePath := writeTestGate(t, "Decide.", []string{"Q1"}, []string{""})
	m := newTestNeedUserInputReview(t, "feat-4", gatePath)
	labels := m.MenuItemLabels()
	if len(labels) < 1 || !strings.Contains(labels[0], "Resume") {
		t.Fatalf("expected Resume as first menu item; got %v", labels)
	}
	if !strings.Contains(labels[0], "answer all questions to enable") {
		t.Errorf("Resume label should advertise gating when answers are missing; got %q", labels[0])
	}
}

// TestNeedUserInputOverlayReattachAfterDetach asserts that the artifact-
// review shell can reopen the same gate questionnaire after a detach
// without losing draft answers — this is the contract behind the
// "press 'a' to reopen the gate" UX after detach or restart.
func TestNeedUserInputOverlayReattachAfterDetach(t *testing.T) {
	gatePath := writeTestGate(t, "Decide.", []string{"Q1"}, []string{""})
	m := newTestNeedUserInputReview(t, "feat-reattach", gatePath)
	m = m.SetAnswer(0, "first attempt")

	// Open menu and pick "Return to dashboard" (last item).
	m, _ = m.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	// Move down twice (Resume → Abort → Detach).
	m, _ = m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	m, _ = m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.Detached() {
		t.Fatalf("Return to dashboard must mark the questionnaire detached")
	}
	if m.Decided() {
		t.Fatalf("Return to dashboard must not flag the questionnaire as Decided()")
	}

	// Reattach should clear detached and refocus the form.
	_ = m.Reattach()
	if m.Detached() {
		t.Errorf("Reattach should clear detached")
	}
}

// TestNeedUserInputOverlayRepoScopeEmitsRepoName verifies that a
// repo-scoped questionnaire emits a NeedUserInputDecisionMsg whose
// RepoName carries the repo identifier so the orchestrator routes the
// decision to the right repo gate in a multi-repo run.
func TestNeedUserInputOverlayRepoScopeEmitsRepoName(t *testing.T) {
	gatePath := writeTestGate(t, "Decide for repo-a.", []string{"Q1"}, []string{"A1"})
	m := newTestNeedUserInputReview(t, "feat-mr", gatePath)
	m = m.SetRepoName("repo-a")

	m, _ = m.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	model, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatalf("expected a decision Cmd")
	}
	out := cmd()
	dm, ok := out.(NeedUserInputDecisionMsg)
	if !ok {
		t.Fatalf("expected NeedUserInputDecisionMsg; got %T (%+v)", out, out)
	}
	if dm.FeatureID != "feat-mr" || dm.RepoName != "repo-a" || dm.Decision != "resume" {
		t.Errorf("decision = %+v, want feat-mr/repo-a/resume", dm)
	}
	if !model.Decided() {
		t.Error("model should report Decided() after repo-scoped resume")
	}
}

// TestNeedUserInputOverlayAbortRepoScopeEmitsRepoName mirrors the resume
// path: an abort decision on a repo-scoped gate carries the repo name so
// the orchestrator can target the right repo's failure helper.
func TestNeedUserInputOverlayAbortRepoScopeEmitsRepoName(t *testing.T) {
	gatePath := writeTestGate(t, "Decide.", []string{"Q1"}, []string{"" /* empty so resume blocked */})
	m := newTestNeedUserInputReview(t, "feat-mr-abort", gatePath)
	m = m.SetRepoName("repo-b")

	// Open menu, move down once to Abort, hit Enter.
	m, _ = m.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	m, _ = m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatalf("expected an abort decision Cmd")
	}
	out := cmd()
	dm, ok := out.(NeedUserInputDecisionMsg)
	if !ok {
		t.Fatalf("expected NeedUserInputDecisionMsg; got %T (%+v)", out, out)
	}
	if dm.FeatureID != "feat-mr-abort" || dm.RepoName != "repo-b" || dm.Decision != "abort" {
		t.Errorf("decision = %+v, want feat-mr-abort/repo-b/abort", dm)
	}
}

// TestAttachOpensSingleRepoPausedCycleFromDetail locks in that pressing 'a'
// on the detail view of a Published, single-repo feature whose only repo's
// post-publish cycle is paused on a NEED_USER_INPUT gate routes through
// attachNeedUserInput rather than falling back to attachToFeature. This
// regression guards the unified N=1-as-multi-repo invariant: a one-repo
// rebase/tweak/refactor/review-comments gate must be reopenable after pause.
func TestAttachOpensSingleRepoPausedCycleFromDetail(t *testing.T) {
	app, fm := newTestAppModel(t)
	f, err := fm.Create("Single Paused", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	for _, st := range []feature.Status{
		feature.StatusResearching,
		feature.StatusPlanReady,
		feature.StatusPlanning,
		feature.StatusImplementReady,
		feature.StatusImplementing,
		feature.StatusReviewPassed,
		feature.StatusCodeReady,
		feature.StatusPublished,
	} {
		_ = fm.Transition(f.ID, st)
	}
	gatePath := writeTestGate(t, "Decide.", []string{"Q1"}, []string{""})
	if err := fm.Store.Modify(f.ID, func(f *feature.Feature) error {
		if f.RepoCycles == nil {
			f.RepoCycles = make(map[string]*feature.RepoCycleState)
		}
		f.RepoCycles["test-repo"] = &feature.RepoCycleState{
			Type:                     feature.CycleTweak,
			Status:                   feature.RepoCycleNeedUserInput,
			PendingNeedUserInputPath: gatePath,
		}
		return nil
	}); err != nil {
		t.Fatalf("modify: %v", err)
	}
	f, _ = fm.Get(f.ID)
	if got := len(f.Repos); got != 1 {
		t.Fatalf("expected single-repo feature, got %d repos", got)
	}

	app.currentView = ViewDetail
	app.detail = NewDetailModel(f, "")

	model, _ := app.updateDetail(tea.KeyPressMsg{Code: 'a', Text: "a"})
	updated := model.(AppModel)

	if updated.currentView != ViewArtifactReview {
		t.Fatalf("currentView = %v, want ViewArtifactReview", updated.currentView)
	}
	if got := updated.artifactReview.ReviewMode(); got != reviewModeNeedUserInput {
		t.Errorf("artifactReview.ReviewMode() = %q, want %q", got, reviewModeNeedUserInput)
	}
	if got := updated.artifactReview.RepoName(); got != "test-repo" {
		t.Errorf("artifactReview.RepoName() = %q, want %q", got, "test-repo")
	}
	if got := updated.artifactReview.FeatureID(); got != f.ID {
		t.Errorf("artifactReview.FeatureID() = %q, want %q", got, f.ID)
	}
}

// TestAttachOpensSingleRepoPausedCycleFromDashboardRightPanel covers the
// split-view attach path: the dashboard right-panel handler must also route
// a single-repo paused cycle through attachNeedUserInput.
func TestAttachOpensSingleRepoPausedCycleFromDashboardRightPanel(t *testing.T) {
	app, fm := newTestAppModel(t)
	f, err := fm.Create("Single Paused Dash", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	for _, st := range []feature.Status{
		feature.StatusResearching,
		feature.StatusPlanReady,
		feature.StatusPlanning,
		feature.StatusImplementReady,
		feature.StatusImplementing,
		feature.StatusReviewPassed,
		feature.StatusCodeReady,
		feature.StatusPublished,
	} {
		_ = fm.Transition(f.ID, st)
	}
	gatePath := writeTestGate(t, "Decide.", []string{"Q1"}, []string{""})
	if err := fm.Store.Modify(f.ID, func(f *feature.Feature) error {
		if f.RepoCycles == nil {
			f.RepoCycles = make(map[string]*feature.RepoCycleState)
		}
		f.RepoCycles["test-repo"] = &feature.RepoCycleState{
			Type:                     feature.CycleRefactor,
			Status:                   feature.RepoCycleNeedUserInput,
			PendingNeedUserInputPath: gatePath,
		}
		return nil
	}); err != nil {
		t.Fatalf("modify: %v", err)
	}
	f, _ = fm.Get(f.ID)

	features, _ := fm.List()
	app.dashboard.SetFeatures(features)
	app.dashboard.cursor = 1
	app.dashboard.syncPreview()
	app.dashboard.focusPanel = 1

	model, _ := app.updateDashboardRightPanel(tea.KeyPressMsg{Code: 'a', Text: "a"})
	updated := model.(AppModel)

	if updated.currentView != ViewArtifactReview {
		t.Fatalf("currentView = %v, want ViewArtifactReview", updated.currentView)
	}
	if got := updated.artifactReview.ReviewMode(); got != reviewModeNeedUserInput {
		t.Errorf("artifactReview.ReviewMode() = %q, want %q", got, reviewModeNeedUserInput)
	}
	if got := updated.artifactReview.RepoName(); got != "test-repo" {
		t.Errorf("artifactReview.RepoName() = %q, want %q", got, "test-repo")
	}
}
