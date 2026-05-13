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

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
)

type LogsModel struct {
	viewport      viewport.Model
	title         string
	featureID     string // optional: set when viewing diff to enable publish shortcut
	autoPublish   bool   // when true, hide the [p] Publish hint
	isPublishable bool   // when false, hide the [p] Publish hint (no remote)
}

func NewLogsModel(title, content string, width, height int) LogsModel {
	vp := viewport.New(viewport.WithWidth(max(width-4, 40)), viewport.WithHeight(max(height-6, 10)))
	vp.SetContent(content)
	return LogsModel{
		viewport: vp,
		title:    title,
	}
}

func (m LogsModel) Init() tea.Cmd {
	return nil
}

func (m LogsModel) Update(msg tea.Msg) (LogsModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		if key.Matches(msg, keys.Back) || key.Matches(msg, keys.Quit) {
			return m, nil
		}
	case tea.WindowSizeMsg:
		m.viewport.SetWidth(msg.Width - 4)
		m.viewport.SetHeight(msg.Height - 6)
	}
	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}

// WithSize returns a copy with updated dimensions.
func (m LogsModel) WithSize(width, height int) LogsModel {
	m.viewport.SetWidth(max(width-4, 40))
	m.viewport.SetHeight(max(height-6, 10))
	return m
}

func (m LogsModel) View() string {
	w := m.viewport.Width() + 4
	if w < 40 {
		w = 80
	}

	var b strings.Builder

	// Wrap viewport in a bordered box
	boxWidth := w
	logBox := panelStyle(true).
		Width(boxWidth).
		Render(m.viewport.View())
	logBox = renderBorderTitle(logBox, m.title, TitleStyle)
	b.WriteString(" " + logBox)

	// Scroll indicator
	pct := m.viewport.ScrollPercent()
	b.WriteString("\n")
	b.WriteString(MutedStyle.Render(fmt.Sprintf("  %3.0f%%", pct*100)))
	b.WriteString("\n")

	hints := " [b] Back   [q] Quit   [\u2191/\u2193] Scroll"
	if m.featureID != "" && !m.autoPublish && m.isPublishable {
		hints += "   [p] Publish"
	}
	b.WriteString(KeyHelpStyle.Render(hints))
	b.WriteString("\n")

	return b.String()
}
