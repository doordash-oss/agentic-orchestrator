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

	"charm.land/lipgloss/v2"
)

type markdownRenderer func(text string, width int) string

var renderMarkdown markdownRenderer = func(text string, width int) string {
	return lipgloss.NewStyle().Width(width).Render(text)
}

// SetMarkdownRenderer wires the richer markdown renderer used by the app
// binary without forcing the fast TUI package tests to import it.
func SetMarkdownRenderer(renderer markdownRenderer) {
	if renderer == nil {
		return
	}
	renderMarkdown = renderer
}

func renderMarkdownPreview(text string, width int) (out string) {
	width = max(width, 1)
	defer func() {
		if recover() != nil {
			out = renderPlainMarkdownPreview(text, width)
		}
	}()

	out = renderMarkdown(text, width)
	if strings.TrimSpace(text) != "" && strings.TrimSpace(ansiRegex.ReplaceAllString(out, "")) == "" {
		return renderPlainMarkdownPreview(text, width)
	}
	return out
}

func renderPlainMarkdownPreview(text string, width int) string {
	return lipgloss.NewStyle().Width(width).Render(text)
}
