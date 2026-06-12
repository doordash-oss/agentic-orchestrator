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
	"charm.land/bubbles/v2/textarea"
	"charm.land/lipgloss/v2"
)

func newStyledTextarea() textarea.Model {
	ta := textarea.New()
	applyTextareaPalette(&ta)
	return ta
}

func applyTextareaPalette(ta *textarea.Model) {
	styles := ta.Styles()

	styles.Focused.Base = lipgloss.NewStyle()
	styles.Focused.Text = lipgloss.NewStyle()
	styles.Focused.CursorLine = lipgloss.NewStyle()
	styles.Focused.CursorLineNumber = lipgloss.NewStyle().Foreground(colorOverlay)
	styles.Focused.EndOfBuffer = lipgloss.NewStyle()
	styles.Focused.LineNumber = lipgloss.NewStyle().Foreground(colorOverlay)
	styles.Focused.Placeholder = lipgloss.NewStyle().Foreground(colorOverlay)
	styles.Focused.Prompt = lipgloss.NewStyle().Foreground(colorBrand)

	styles.Blurred.Base = lipgloss.NewStyle()
	styles.Blurred.Text = lipgloss.NewStyle()
	styles.Blurred.CursorLine = lipgloss.NewStyle()
	styles.Blurred.CursorLineNumber = lipgloss.NewStyle().Foreground(colorOverlay)
	styles.Blurred.EndOfBuffer = lipgloss.NewStyle()
	styles.Blurred.LineNumber = lipgloss.NewStyle().Foreground(colorOverlay)
	styles.Blurred.Placeholder = lipgloss.NewStyle().Foreground(colorOverlay)
	styles.Blurred.Prompt = lipgloss.NewStyle().Foreground(colorOverlay)

	styles.Cursor.Color = colorBrand
	ta.SetStyles(styles)
}
