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
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/server"
)

const reviewModeNeedUserInput = "need_user_input"

type NeedUserInputDecisionMsg struct {
	FeatureID string
	RepoName  string
	CycleType feature.RepoCycleType
	Decision  string
}

type NeedUserInputDraftMsg struct {
	FeatureID      string
	RepoName       string
	CycleType      feature.RepoCycleType
	Gate           server.NeedInputGateDTO
	QuestionOffset int
}

type needUserInputForm struct {
	gate        server.NeedInputGateDTO
	inputs      []textinput.Model
	cursor      int
	width       int
	decisionErr error
}

func newNeedUserInputForm(gate server.NeedInputGateDTO, width int) *needUserInputForm {
	f := &needUserInputForm{gate: gate, width: width}
	f.inputs = make([]textinput.Model, len(gate.Questions))
	for i, q := range gate.Questions {
		ti := textinput.New()
		ti.Placeholder = fmt.Sprintf("Answer for Q%d...", i+1)
		ti.SetValue(q.Answer)
		ti.SetWidth(max(width-8, 20))
		f.inputs[i] = ti
	}
	if len(f.inputs) > 0 {
		_ = f.inputs[0].Focus()
	}
	f.syncDraftAnswers()
	return f
}

func (f *needUserInputForm) SetWidth(width int) {
	f.width = width
	for i := range f.inputs {
		f.inputs[i].SetWidth(max(width-8, 20))
	}
}

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
			f.syncDraftAnswers()
			return tea.Batch(cmd, f.draftCmd(f.cursor))
		}
		return cmd
	}
	return nil
}

func (f *needUserInputForm) Forward(msg tea.Msg) tea.Cmd {
	if f.cursor < 0 || f.cursor >= len(f.inputs) {
		return nil
	}
	before := f.inputs[f.cursor].Value()
	var cmd tea.Cmd
	f.inputs[f.cursor], cmd = f.inputs[f.cursor].Update(msg)
	if f.inputs[f.cursor].Value() != before {
		f.syncDraftAnswers()
		return tea.Batch(cmd, f.draftCmd(f.cursor))
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

func (f *needUserInputForm) syncDraftAnswers() {
	for i := range f.gate.Questions {
		if i < len(f.inputs) {
			f.gate.Questions[i].Answer = strings.TrimSpace(f.inputs[i].Value())
		}
	}
}

func (f *needUserInputForm) draftCmd(questionOffset int) tea.Cmd {
	gate := f.gate
	msg := NeedUserInputDraftMsg{
		FeatureID:      gate.FeatureID,
		RepoName:       gate.RepoName,
		CycleType:      feature.RepoCycleType(gate.CycleType),
		Gate:           gate,
		QuestionOffset: questionOffset,
	}
	return func() tea.Msg { return msg }
}

func (f *needUserInputForm) AllAnswered() bool {
	if len(f.gate.Questions) == 0 {
		return false
	}
	f.syncDraftAnswers()
	for _, q := range f.gate.Questions {
		if strings.TrimSpace(q.Answer) == "" {
			return false
		}
	}
	return true
}

func (f *needUserInputForm) SetAnswer(i int, answer string) {
	if i < 0 || i >= len(f.inputs) {
		return
	}
	f.inputs[i].SetValue(answer)
	f.syncDraftAnswers()
}

func (f *needUserInputForm) Focus() tea.Cmd {
	if f.cursor < 0 || f.cursor >= len(f.inputs) {
		return nil
	}
	return f.inputs[f.cursor].Focus()
}

func (f *needUserInputForm) View() string {
	var sb strings.Builder
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(colorBrand)
	sb.WriteString(titleStyle.Render("Implementation needs user input"))
	sb.WriteString("\n\n")
	if strings.TrimSpace(f.gate.Summary) != "" {
		sb.WriteString(lipgloss.NewStyle().Foreground(colorSubtext).Render(f.gate.Summary))
		sb.WriteString("\n\n")
	}
	if f.decisionErr != nil {
		errStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)
		sb.WriteString(errStyle.Render(fmt.Sprintf("Decision failed: %v", f.decisionErr)))
		sb.WriteString("\n")
		sb.WriteString(lipgloss.NewStyle().Foreground(colorSubtext).Render(
			"The feature is back on the gate - re-open the menu to retry resume or abort."))
		sb.WriteString("\n\n")
	}
	for i, q := range f.gate.Questions {
		index := q.Index
		if index <= 0 {
			index = i + 1
		}
		num := lipgloss.NewStyle().Bold(true).Render(fmt.Sprintf("%d. ", index))
		sb.WriteString(num + strings.TrimSpace(q.Prompt) + "\n")
		if i < len(f.inputs) {
			sb.WriteString("   " + f.inputs[i].View())
		}
		sb.WriteString("\n\n")
	}
	hintStyle := lipgloss.NewStyle().Foreground(colorSubtext)
	sb.WriteString(hintStyle.Render("Tab/Shift+Tab: navigate │ Ctrl+D: actions menu"))
	return sb.String()
}
