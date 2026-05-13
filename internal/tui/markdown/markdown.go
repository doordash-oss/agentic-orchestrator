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

// Package markdown renders markdown to ANSI-styled text for display in the
// TUI. It wraps charmbracelet/glamour with a catppuccin-aligned style and
// caches rendered output per (content, width) so re-renders on scroll or
// resize don't re-parse markdown.
//
// Callers should only invoke Render on complete assistant messages. Partial
// streaming content (Codex deltas) routinely contains half-written fenced
// code blocks and should continue to use plain wrapping until the message
// finalizes; otherwise the markdown renderer can produce incomplete output and
// repeat parser work on every delta.
package markdown

import (
	"strings"
	"sync"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/compat"
	"github.com/charmbracelet/glamour"
)

// minWidth guards against pathologically narrow viewports that glamour
// refuses to render into.
const minWidth = 20

var (
	cache = newRenderCache()

	renderersMu sync.Mutex
	renderers   = map[int]*glamour.TermRenderer{}
)

// Render converts markdown to ANSI-styled text wrapped at width. On any
// error (renderer init, parse, write) it falls back to plain lipgloss
// wrapping so output is never lost.
func Render(text string, width int) string {
	if width < minWidth {
		width = minWidth
	}

	if cached, ok := cache.get(text, width); ok {
		return cached
	}

	out, err := renderMarkdown(text, width)
	if err != nil || out == "" {
		return lipgloss.NewStyle().Width(width).Render(text)
	}

	// glamour always sandwiches output in newlines; trim them so the
	// caller's line-by-line indenter doesn't produce blank leading/trailing
	// lines.
	out = strings.Trim(out, "\n")

	cache.put(text, width, out)
	return out
}

func renderMarkdown(text string, width int) (string, error) {
	r, err := getRenderer(width)
	if err != nil {
		return "", err
	}
	return r.Render(text)
}

func getRenderer(width int) (*glamour.TermRenderer, error) {
	renderersMu.Lock()
	defer renderersMu.Unlock()

	if r, ok := renderers[width]; ok {
		return r, nil
	}

	style := buildStyle(compat.HasDarkBackground)
	r, err := glamour.NewTermRenderer(
		glamour.WithStyles(style),
		glamour.WithWordWrap(width),
		glamour.WithEmoji(),
	)
	if err != nil {
		return nil, err
	}
	renderers[width] = r
	return r, nil
}
