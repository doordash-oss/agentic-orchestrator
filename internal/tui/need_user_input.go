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
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// reviewModeNeedUserInput is the artifact-review questionnaire-mode tag
// for the need-user-input gate. The gate questionnaire is hosted inside
// ArtifactReviewModel rather than as a separate top-level workflow so
// the existing attach / detach / reattach shell carries it.
const reviewModeNeedUserInput = "need_user_input"

// NeedUserInputDecisionMsg is emitted when the user picks Resume or Abort
// from the gate menu. Routed through the artifact-review menu so the
// shell stays in lockstep with review-gate menus. RepoName is empty for
// feature-scoped gates; non-empty selects the cycle-scoped gate on
// RepoCycles[RepoName] (post-publish). CycleType is surfaced for
// diagnostics; the orchestrator derives the actual restart type from
// the persisted RepoCycleState.
type NeedUserInputDecisionMsg struct {
	FeatureID string
	RepoName  string
	CycleType feature.RepoCycleType
	Decision  string // "resume" | "abort"
}

// openNeedUserInputMsg is the TUI-internal command result that triggers
// attaching the questionnaire to a paused gate. Used by the tweak session
// completion path to reattach immediately on a NEED_USER_INPUT exit.
type openNeedUserInputMsg struct {
	FeatureID string
	RepoName  string
	CycleType feature.RepoCycleType
	GatePath  string
}

// needUserInputForm is the questionnaire body rendered inside
// ArtifactReviewModel when reviewMode == reviewModeNeedUserInput. It is
// intentionally NOT a top-level tea.Model — folding it into the
// artifact-review shell keeps the attach pattern (Ctrl+D menu, detach,
// reattach) from review gates without inventing a parallel workflow.
type needUserInputForm struct {
	gatePath    string
	record      agent.NeedUserInputRecord
	inputs      []textinput.Model
	cursor      int
	width       int
	loadErr     error
	decisionErr error
}

// newNeedUserInputForm loads the gate artifact at gatePath and primes
// per-question text inputs. A read failure is captured in loadErr so
// the shell can surface it without crashing.
func newNeedUserInputForm(gatePath string, width int) *needUserInputForm {
	f := &needUserInputForm{gatePath: gatePath, width: width}
	rec, err := agent.ReadNeedUserInputRecord(gatePath)
	if err != nil {
		f.loadErr = err
		return f
	}
	f.record = rec
	f.inputs = make([]textinput.Model, len(rec.Questions))
	for i, q := range rec.Questions {
		ti := textinput.New()
		ti.Placeholder = fmt.Sprintf("Answer for Q%d...", i+1)
		ti.SetValue(q.Answer)
		ti.SetWidth(max(width-8, 20))
		f.inputs[i] = ti
	}
	if len(f.inputs) > 0 {
		_ = f.inputs[0].Focus()
	}
	return f
}

// SetWidth resizes every input. Called from ArtifactReviewModel.recalcLayout.
func (f *needUserInputForm) SetWidth(width int) {
	f.width = width
	for i := range f.inputs {
		f.inputs[i].SetWidth(max(width-8, 20))
	}
}

// HandleKey routes a key press into the questionnaire (typing /
// navigation). Returns a follow-up cmd; menu / detach keys are handled
// by the artifact-review shell BEFORE this is called. Every editing
// keystroke is flushed to disk via Persist so a hard restart while the
// questionnaire is open recovers the draft from the persisted gate
// artifact (the design's restart-recovery contract).
func (f *needUserInputForm) HandleKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "tab", "down":
		f.moveFocus(1)
		return nil
	case "shift+tab", "up":
		f.moveFocus(-1)
		return nil
	}
	if f.cursor >= 0 && f.cursor < len(f.inputs) {
		before := f.inputs[f.cursor].Value()
		var cmd tea.Cmd
		f.inputs[f.cursor], cmd = f.inputs[f.cursor].Update(msg)
		if f.inputs[f.cursor].Value() != before {
			_ = f.Persist()
		}
		return cmd
	}
	return nil
}

// Forward dispatches non-key bubbletea messages to the focused input
// (cursor blink, paste, etc.). Tolerant: when no input is focused, the
// message is dropped. When the dispatched message mutates the input
// value (e.g. a paste), the change is flushed to disk so a hard restart
// recovers the draft.
func (f *needUserInputForm) Forward(msg tea.Msg) tea.Cmd {
	if f.cursor < 0 || f.cursor >= len(f.inputs) {
		return nil
	}
	before := f.inputs[f.cursor].Value()
	var cmd tea.Cmd
	f.inputs[f.cursor], cmd = f.inputs[f.cursor].Update(msg)
	if f.inputs[f.cursor].Value() != before {
		_ = f.Persist()
	}
	return cmd
}

func (f *needUserInputForm) moveFocus(delta int) {
	if len(f.inputs) == 0 {
		return
	}
	if f.cursor >= 0 && f.cursor < len(f.inputs) {
		f.inputs[f.cursor].Blur()
	}
	f.cursor = (f.cursor + delta + len(f.inputs)) % len(f.inputs)
	_ = f.inputs[f.cursor].Focus()
}

// syncDraftAnswers copies input buffer values back into the in-memory
// record so Persist + AllAnswered reflect the user's typing.
func (f *needUserInputForm) syncDraftAnswers() {
	for i := range f.record.Questions {
		if i < len(f.inputs) {
			f.record.Questions[i].Answer = strings.TrimSpace(f.inputs[i].Value())
		}
	}
}

// Persist writes the in-memory record back to disk so detach + restart
// preserve draft answers.
func (f *needUserInputForm) Persist() error {
	if f.gatePath == "" {
		return nil
	}
	f.syncDraftAnswers()
	return agent.WriteNeedUserInputRecord(f.gatePath, f.record)
}

// AllAnswered reports whether every question has a non-empty answer.
func (f *needUserInputForm) AllAnswered() bool {
	if len(f.record.Questions) == 0 {
		return false
	}
	for _, q := range f.record.Questions {
		if strings.TrimSpace(q.Answer) == "" {
			return false
		}
	}
	return true
}

// SetAnswer is a test seam that sets the i-th input's value and syncs
// it into the record so AllAnswered / Persist see the change.
func (f *needUserInputForm) SetAnswer(i int, answer string) {
	if i < 0 || i >= len(f.inputs) {
		return
	}
	f.inputs[i].SetValue(answer)
	f.syncDraftAnswers()
}

// Focus refocuses the cursor input. Called on Reattach so the user can
// keep typing where they left off.
func (f *needUserInputForm) Focus() tea.Cmd {
	if f.cursor < 0 || f.cursor >= len(f.inputs) {
		return nil
	}
	return f.inputs[f.cursor].Focus()
}

// View renders the questionnaire body. The artifact-review shell wraps
// this output with header/footer/menu chrome.
func (f *needUserInputForm) View() string {
	var sb strings.Builder
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(colorBrand)
	sb.WriteString(titleStyle.Render("Implementation needs user input"))
	sb.WriteString("\n\n")
	if f.loadErr != nil {
		errStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
		sb.WriteString(errStyle.Render(fmt.Sprintf("Failed to load gate artifact: %v", f.loadErr)))
		return sb.String()
	}
	if strings.TrimSpace(f.record.Summary) != "" {
		sb.WriteString(lipgloss.NewStyle().Foreground(colorSubtext).Render(f.record.Summary))
		sb.WriteString("\n\n")
	}
	if f.decisionErr != nil {
		errStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)
		sb.WriteString(errStyle.Render(fmt.Sprintf("Decision failed: %v", f.decisionErr)))
		sb.WriteString("\n")
		sb.WriteString(lipgloss.NewStyle().Foreground(colorSubtext).Render(
			"The feature is back on the gate — re-open the menu to retry resume or abort."))
		sb.WriteString("\n\n")
	}
	for i, q := range f.record.Questions {
		num := lipgloss.NewStyle().Bold(true).Render(fmt.Sprintf("%d. ", q.Index))
		sb.WriteString(num + q.Prompt + "\n")
		if i < len(f.inputs) {
			sb.WriteString("   " + f.inputs[i].View())
		}
		sb.WriteString("\n\n")
	}
	hintStyle := lipgloss.NewStyle().Foreground(colorSubtext)
	sb.WriteString(hintStyle.Render("Tab/Shift+Tab: navigate │ Ctrl+D: actions menu"))
	return sb.String()
}
