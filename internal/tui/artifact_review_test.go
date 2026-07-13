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
	"github.com/charmbracelet/x/ansi"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/server"
)

func testReviewSession(text string) server.ReviewSessionResponse {
	return server.ReviewSessionResponse{
		APIVersion:     server.APIVersion,
		FeatureID:      "feat-1",
		ReviewID:       "review-1",
		ReviewMode:     "plan",
		TargetPhase:    feature.PhaseImplement.DirName(),
		RunNumber:      1,
		ArtifactID:     "plan",
		Text:           text,
		DraftRevision:  "rev-1",
		SourceRevision: "source-rev-1",
		CanIterate:     true,
	}
}

func TestArtifactReviewLoadsDraftWithoutArtifactPath(t *testing.T) {
	m := NewArtifactReviewModel(testReviewSession("# Plan\nbody"), feature.PhaseImplement, 80, 24)

	if got := m.editor.Content(); got != "# Plan\nbody" {
		t.Fatalf("editor content = %q, want draft text", got)
	}
	if m.editor.FilePath() != "" {
		t.Fatalf("editor file path = %q, want pathless draft editor", m.editor.FilePath())
	}
	if got := m.ReviewID(); got != "review-1" {
		t.Fatalf("ReviewID() = %q, want review-1", got)
	}
	if got := m.ArtifactID(); got != "plan" {
		t.Fatalf("ArtifactID() = %q, want plan", got)
	}
}

func TestArtifactReviewCtrlSRequestsDraftSave(t *testing.T) {
	m := NewArtifactReviewModel(testReviewSession("original"), feature.PhaseImplement, 80, 24)
	m.editor.Focus()
	m.editor.SetContent("edited", false)

	_, cmd := m.Update(keyPress("ctrl+s"))
	if cmd == nil {
		t.Fatal("ctrl+s returned nil command, want draft save request")
	}
	msg := cmd()
	save, ok := msg.(ArtifactReviewDraftSaveMsg)
	if !ok {
		t.Fatalf("ctrl+s emitted %T, want ArtifactReviewDraftSaveMsg", msg)
	}
	if save.FeatureID != "feat-1" || save.ReviewID != "review-1" || save.BaseRevision != "rev-1" || save.Text != "edited" {
		t.Fatalf("save message = %+v, want feature/review/revision/text", save)
	}
}

func TestArtifactReviewMenuDecisionUsesCurrentDraftRevisionAndText(t *testing.T) {
	m := NewArtifactReviewModel(testReviewSession("original"), feature.PhaseImplement, 80, 24)
	m.editor.Focus()
	m.editor.SetContent("edited before decision", false)
	m, _ = m.Update(keyPress("ctrl+d"))
	m, _ = m.Update(keyPress("down"))

	_, cmd := m.Update(keyPress("enter"))
	if cmd == nil {
		t.Fatal("menu decision returned nil command")
	}
	msg := cmd()
	decision, ok := msg.(ArtifactReviewSessionDecisionMsg)
	if !ok {
		t.Fatalf("menu decision emitted %T, want ArtifactReviewSessionDecisionMsg", msg)
	}
	if decision.FeatureID != "feat-1" || decision.ReviewID != "review-1" {
		t.Fatalf("decision identity = %+v, want feature/review", decision)
	}
	if decision.Decision != "proceed" {
		t.Fatalf("decision = %q, want proceed", decision.Decision)
	}
	if decision.BaseRevision != "rev-1" || decision.Text != "edited before decision" {
		t.Fatalf("decision draft = %+v, want current revision/text", decision)
	}
}

func TestArtifactReviewHasNoChatSessionSurface(t *testing.T) {
	m := NewArtifactReviewModel(testReviewSession("draft"), feature.PhaseImplement, 80, 24)

	if got := m.View(); containsAny(got, "Chat", "Tab to chat", "AI") {
		t.Fatalf("view still exposes removed chat UI:\n%s", got)
	}
	if _, cmd := m.Update(keyPress("tab")); cmd != nil {
		t.Fatal("tab returned a command; chat focus cycling should be removed")
	}
}

func TestArtifactReviewMenuOverlayCentersAgainstViewportWidth(t *testing.T) {
	const width = 180
	m := NewArtifactReviewModel(testReviewSession(strings.Repeat("long review content ", 20)), feature.PhaseImplement, width, 36)
	m.showMenu = true

	menuStart, menuWidth, ok := reviewDecisionMenuPosition(m.View())
	if !ok {
		t.Fatalf("review decision menu not found in view:\n%s", ansi.Strip(m.View()))
	}
	wantStart := (width - menuWidth) / 2
	if delta := absInt(menuStart - wantStart); delta > 1 {
		t.Fatalf("menu start column = %d, want centered at %d (menu width %d, viewport %d)", menuStart, wantStart, menuWidth, width)
	}
}

func keyPress(keys string) tea.KeyPressMsg {
	switch keys {
	case "ctrl+s":
		return tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl}
	case "ctrl+d":
		return tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl}
	case "ctrl+]":
		return tea.KeyPressMsg{Code: ']', Mod: tea.ModCtrl}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	default:
		return tea.KeyPressMsg{Code: []rune(keys)[0], Text: keys}
	}
}

func reviewDecisionMenuPosition(view string) (start, width int, ok bool) {
	for _, line := range strings.Split(view, "\n") {
		plain := ansi.Strip(line)
		titleIdx := strings.Index(plain, "Review Decision")
		if titleIdx < 0 {
			continue
		}
		leftBorderIdx := strings.LastIndex(plain[:titleIdx], "│")
		rightBorderIdx := strings.LastIndex(plain, "│")
		if leftBorderIdx < 0 || rightBorderIdx <= leftBorderIdx {
			return 0, 0, false
		}
		return ansi.StringWidth(plain[:leftBorderIdx]), ansi.StringWidth(plain[leftBorderIdx : rightBorderIdx+len("│")]), true
	}
	return 0, 0, false
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func containsAny(s string, parts ...string) bool {
	for _, part := range parts {
		if strings.Contains(s, part) {
			return true
		}
	}
	return false
}
