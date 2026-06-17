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

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

func TestLivePreviewAssistantRowsUseMarkdownRenderer(t *testing.T) {
	previous := renderMarkdown
	var renderedText string
	var renderedWidth int
	SetMarkdownRenderer(func(text string, width int) string {
		renderedText = text
		renderedWidth = width
		return "markdown-rendered assistant body"
	})
	t.Cleanup(func() { renderMarkdown = previous })

	f := &feature.Feature{Status: feature.StatusImplementing, CurrentPhase: feature.PhaseImplement}
	sess := newLivePreviewSession("markdown-preview", feature.PhaseImplement,
		assistantMessage(llm.ContentBlock{Type: "text", Text: "**Rendered** update\n\n> quoted context"}),
	)
	view := stripANSI(newLivePreviewModel(f).withSession(sess).withHeight(24).ViewCompact(100))

	if !strings.Contains(view, "markdown-rendered assistant body") {
		t.Fatalf("live preview should render assistant markdown through renderer, got:\n%s", view)
	}
	if strings.Contains(view, "**Rendered**") {
		t.Fatalf("live preview leaked raw markdown, got:\n%s", view)
	}
	if renderedText != "**Rendered** update\n\n> quoted context" {
		t.Fatalf("markdown renderer text = %q, want original assistant markdown", renderedText)
	}
	if renderedWidth <= 0 || renderedWidth >= 100 {
		t.Fatalf("markdown renderer width = %d, want constrained content width", renderedWidth)
	}
}
