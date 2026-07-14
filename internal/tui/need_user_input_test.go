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
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/doordash-oss/agentic-orchestrator/internal/server"
)

func TestNeedUserInputReviewBlocksEmptyResume(t *testing.T) {
	t.Parallel()

	m := NewNeedUserInputReviewModel("feat-1", server.NeedInputGateDTO{
		FeatureID: "feat-1",
		Summary:   "Decide.",
		Questions: []server.NeedUserInputQuestionDTO{
			{Index: 1, Prompt: "Q1", Answer: "A1"},
			{Index: 2, Prompt: "Q2"},
		},
	}, 80, 24)
	if m.AllAnswered() {
		t.Fatal("test setup: expected gate not all answered")
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	if !m.MenuOpen() {
		t.Fatal("menu should be open after Ctrl+D")
	}
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		var decisions []NeedUserInputDecisionMsg
		apiTestCollectNeedUserInputDecisionMsgs(cmd(), &decisions)
		if len(decisions) > 0 {
			t.Fatalf("blocked resume must not emit a decision; got %+v", decisions)
		}
	}
}

func TestNeedUserInputReviewResumeWhenAnswered(t *testing.T) {
	t.Parallel()

	m := NewNeedUserInputReviewModel("feat-2", server.NeedInputGateDTO{
		FeatureID: "feat-2",
		RepoName:  "api",
		CycleType: "rebase",
		Summary:   "Decide.",
		Questions: []server.NeedUserInputQuestionDTO{
			{Index: 1, Prompt: "Q1", Answer: "A1"},
		},
	}, 80, 24)
	if !m.AllAnswered() {
		t.Fatal("test setup: expected gate fully answered")
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	model, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	decision := apiTestNeedUserInputDecisionMsg(t, cmd)
	if decision.FeatureID != "feat-2" || decision.RepoName != "api" || string(decision.CycleType) != "rebase" || decision.Decision != "resume" {
		t.Fatalf("decision = %+v, want feat-2/api/rebase/resume", decision)
	}
	if !model.Decided() {
		t.Fatal("model should report Decided() after resume decision")
	}
}

func TestNeedUserInputReviewDraftsPerKeystroke(t *testing.T) {
	t.Parallel()

	m := NewNeedUserInputReviewModel("feat-keystroke", server.NeedInputGateDTO{
		RepoName:  "api",
		CycleType: "tweak",
		Summary:   "Decide.",
		Questions: []server.NeedUserInputQuestionDTO{
			{Index: 1, Prompt: "Q1"},
		},
	}, 80, 24)

	m, cmd := m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	draft := needUserInputDraftFromCmd(t, cmd)
	if draft.FeatureID != "feat-keystroke" || draft.RepoName != "api" || string(draft.CycleType) != "tweak" || draft.Gate.Questions[0].Answer != "a" {
		t.Fatalf("first draft = %+v, want feature/repo/cycle answer a", draft)
	}

	_, cmd = m.Update(tea.KeyPressMsg{Code: 'b', Text: "b"})
	draft = needUserInputDraftFromCmd(t, cmd)
	if draft.Gate.Questions[0].Answer != "ab" {
		t.Fatalf("second draft answer = %q, want ab", draft.Gate.Questions[0].Answer)
	}
}

func TestNeedUserInputReviewMenuLabels(t *testing.T) {
	t.Parallel()

	m := NewNeedUserInputReviewModel("feat-4", server.NeedInputGateDTO{
		Summary: "Decide.",
		Questions: []server.NeedUserInputQuestionDTO{
			{Index: 1, Prompt: "Q1"},
		},
	}, 80, 24)
	labels := m.MenuItemLabels()
	if len(labels) < 1 || !strings.Contains(labels[0], "Resume") {
		t.Fatalf("expected Resume as first menu item; got %v", labels)
	}
	if !strings.Contains(labels[0], "answer all questions to enable") {
		t.Fatalf("Resume label should advertise gating when answers are missing; got %q", labels[0])
	}
}

func TestNeedUserInputReviewUsesArtifactShellCopy(t *testing.T) {
	t.Parallel()

	m := NewNeedUserInputReviewModel("feat-shell", server.NeedInputGateDTO{
		Scope:     "feature",
		RepoName:  "api",
		CycleType: "rebase",
		Iteration: 2,
		Summary:   "Resolve the conflict before continuing.",
		Questions: []server.NeedUserInputQuestionDTO{
			{Index: 1, Prompt: "Keep branch behavior or target behavior?"},
		},
	}, 80, 24)
	view := stripANSI(m.View())
	for _, want := range []string{
		"Need User Input",
		"Implementation needs user input",
		"Resolve the conflict before continuing.",
		"Keep branch behavior or target behavior?",
		"Tab/Shift+Tab: navigate │ Ctrl+D: actions menu",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("need-user-input view missing %q in:\n%s", want, view)
		}
	}
	for _, removed := range []string{"Scope:", "Repo:", "Cycle:", "Iteration:", "[tab] Next question", "Ctrl+D menu", "Answer..."} {
		if strings.Contains(view, removed) {
			t.Fatalf("need-user-input view still contains removed modal copy %q in:\n%s", removed, view)
		}
	}
}

func TestNeedUserInputReviewMenuUsesFullHeightOverlay(t *testing.T) {
	t.Parallel()

	const height = 30
	m := NewNeedUserInputReviewModel("feat-centered", server.NeedInputGateDTO{
		Summary: "Decide.",
		Questions: []server.NeedUserInputQuestionDTO{
			{Index: 1, Prompt: "Q1", Answer: "A1"},
		},
	}, 100, height)
	m, _ = m.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})

	view := stripANSI(m.View())
	lines := strings.Split(view, "\n")
	if len(lines) != height {
		t.Fatalf("menu overlay rendered %d lines, want full terminal height %d:\n%s", len(lines), height, view)
	}
	menuLine := -1
	for i, line := range lines {
		if strings.Contains(line, "Choose an action:") {
			menuLine = i
			break
		}
	}
	if menuLine < height/3 {
		t.Fatalf("menu starts at line %d in height %d, looks top-compressed:\n%s", menuLine, height, view)
	}
}

func needUserInputDraftFromCmd(t *testing.T, cmd tea.Cmd) NeedUserInputDraftMsg {
	t.Helper()
	if cmd == nil {
		t.Fatal("command is nil")
	}
	var drafts []NeedUserInputDraftMsg
	apiTestCollectNeedUserInputDraftMsgs(cmd(), &drafts)
	if len(drafts) != 1 {
		t.Fatalf("command produced %d NeedUserInputDraftMsg messages, want 1: %+v", len(drafts), drafts)
	}
	return drafts[0]
}
