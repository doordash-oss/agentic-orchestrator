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

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
)

type reviewViewportModel struct {
	viewport viewport.Model
}

func newReviewViewportModel(width, height int, content string) reviewViewportModel {
	vp := viewport.New(
		viewport.WithWidth(reviewViewportWidth(width)),
		viewport.WithHeight(reviewViewportHeight(height)),
	)
	vp.SoftWrap = true
	vp.SetContent(content)
	return reviewViewportModel{viewport: vp}
}

func reviewViewportWidth(width int) int {
	return max(width-6, 40)
}

func reviewViewportHeight(height int) int {
	return max(height-8, 10)
}

func (m reviewViewportModel) Update(msg tea.Msg) (reviewViewportModel, tea.Cmd) {
	vp, cmd := m.viewport.Update(msg)
	m.viewport = vp
	return m, cmd
}

func (m *reviewViewportModel) Resize(width, height int) {
	m.viewport.SetWidth(reviewViewportWidth(width))
	m.viewport.SetHeight(reviewViewportHeight(height))
}

func (m *reviewViewportModel) SetContent(content string) {
	m.viewport.SetContent(content)
}

func (m *reviewViewportModel) GotoTop() {
	m.viewport.GotoTop()
}

func (m reviewViewportModel) Width() int {
	return m.viewport.Width()
}

func (m reviewViewportModel) Height() int {
	return m.viewport.Height()
}

func (m reviewViewportModel) View() string {
	return m.viewport.View()
}

func (m reviewViewportModel) ScrollPercent() float64 {
	return m.viewport.ScrollPercent()
}

func renderReviewViewportBox(boxWidth int, title string, vp reviewViewportModel) string {
	box := panelStyle(true).Width(boxWidth).Render(vp.View())
	return renderBorderTitle(box, title, TitleStyle)
}

func renderReviewViewportScrollPercent(vp reviewViewportModel) string {
	return MutedStyle.Render(fmt.Sprintf("  %3.0f%%", vp.ScrollPercent()*100))
}

func renderReviewViewportScreen(width int, title, step, panelTitle string, vp reviewViewportModel, footer string) string {
	w := width
	if w < 40 {
		w = 80
	}

	var b strings.Builder
	b.WriteString(TitleStyle.Render(" " + title))
	if step != "" {
		b.WriteString("  ")
		b.WriteString(step)
	}
	b.WriteString("\n\n")

	box := renderReviewViewportBox(w-2, panelTitle, vp)
	b.WriteString(" " + strings.ReplaceAll(box, "\n", "\n "))
	b.WriteString("\n")
	b.WriteString(renderReviewViewportScrollPercent(vp))
	b.WriteString("\n")
	if footer != "" {
		b.WriteString(KeyHelpStyle.Render(footer))
		b.WriteString("\n")
	}
	return b.String()
}
