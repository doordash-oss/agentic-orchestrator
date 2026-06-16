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
	"charm.land/lipgloss/v2"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
)

// ReviewCommentsModel displays fetched PR review comments and lets the user
// choose how to handle them.
type ReviewCommentsModel struct {
	featureID   string
	featureSlug string
	comments    []git.ReviewComment
	viewport    viewport.Model
	width       int
	height      int
}

func NewReviewCommentsModel(featureID, slug string, comments []git.ReviewComment, width, height int) ReviewCommentsModel {
	vp := viewport.New(viewport.WithWidth(max(width-6, 40)), viewport.WithHeight(max(height-8, 10)))
	vp.SetContent(renderReviewComments(comments))
	return ReviewCommentsModel{
		featureID:   featureID,
		featureSlug: slug,
		comments:    comments,
		viewport:    vp,
		width:       width,
		height:      height,
	}
}

func (m ReviewCommentsModel) Init() tea.Cmd {
	return nil
}

func (m ReviewCommentsModel) Update(msg tea.Msg) (ReviewCommentsModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.viewport.SetWidth(max(msg.Width-6, 40))
		m.viewport.SetHeight(max(msg.Height-8, 10))
	case tea.KeyPressMsg:
		// Scrolling is handled by viewport; action keys are handled by the parent model.
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd
	}
	return m, nil
}

// WithSize returns a copy with updated dimensions.
func (m ReviewCommentsModel) WithSize(width, height int) ReviewCommentsModel {
	m.width = width
	m.height = height
	m.viewport.SetWidth(max(width-6, 40))
	m.viewport.SetHeight(max(height-8, 10))
	return m
}

func (m ReviewCommentsModel) View() string {
	w := m.width
	if w < 40 {
		w = 80
	}

	var b strings.Builder

	// Wrap viewport in a bordered box. Box outer width is w-2 so that with
	// a 1-char left margin on every line the rendered output fits in w cols.
	boxWidth := w - 2
	contentBox := panelStyle(true).
		Width(boxWidth).
		Render(m.viewport.View())
	title := fmt.Sprintf("Review Comments: %s (%d)", m.featureSlug, len(m.comments))
	contentBox = renderBorderTitle(contentBox, title, TitleStyle)
	// Indent every line so the box's left edge aligns top-to-bottom.
	b.WriteString(" " + strings.ReplaceAll(contentBox, "\n", "\n "))

	// Scroll indicator
	pct := m.viewport.ScrollPercent()
	b.WriteString("\n")
	b.WriteString(MutedStyle.Render(fmt.Sprintf("  %3.0f%%", pct*100)))
	b.WriteString("\n")

	// Footer with key hints
	hints := " [Shift+A] Auto-address   [esc] Back   [q] Quit   [↑/↓] Scroll"
	b.WriteString(KeyHelpStyle.Render(hints))
	b.WriteString("\n")

	return b.String()
}

func renderReviewComments(comments []git.ReviewComment) string {
	if len(comments) == 0 {
		return MutedStyle.Render("\n  No pending review comments on this PR.\n")
	}

	var b strings.Builder
	for i, c := range comments {
		// Comment header
		b.WriteString(lipgloss.NewStyle().Bold(true).Render(
			fmt.Sprintf("  Comment %d/%d", i+1, len(comments))))
		b.WriteString("\n")

		switch c.Type {
		case git.CommentTypeIssue:
			b.WriteString(LabelStyle.Render("  Location"))
			b.WriteString("  PR conversation\n")
		case git.CommentTypeReviewBody:
			b.WriteString(LabelStyle.Render("  Location"))
			b.WriteString("  PR review\n")
		default:
			b.WriteString(LabelStyle.Render("  File"))
			path := c.Path
			if c.Line > 0 {
				path = fmt.Sprintf("%s:%d", c.Path, c.Line)
			}
			b.WriteString("  " + path + "\n")
		}

		b.WriteString(LabelStyle.Render("  Author"))
		b.WriteString("  @" + c.User.Login + "\n\n")

		// Diff context
		if c.DiffHunk != "" {
			lines := strings.Split(c.DiffHunk, "\n")
			// Show last few lines of the diff hunk for context
			start := 0
			if len(lines) > 6 {
				start = len(lines) - 6
			}
			for _, line := range lines[start:] {
				b.WriteString("  " + MutedStyle.Render(line) + "\n")
			}
			b.WriteString("\n")
		}

		// Comment body
		for _, line := range strings.Split(c.Body, "\n") {
			b.WriteString("  " + line + "\n")
		}
		b.WriteString("\n")

		// Separator
		if i < len(comments)-1 {
			b.WriteString(MutedStyle.Render("  "+strings.Repeat("\u2500", 40)) + "\n\n")
		}
	}
	return b.String()
}
