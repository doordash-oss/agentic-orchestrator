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

	"charm.land/lipgloss/v2"
)

func TestRenderBorderTitleNoANSI(t *testing.T) {
	t.Parallel()
	// Plain box without ANSI — the original code path.
	box := "╭──────────────────╮\n│ content          │\n╰──────────────────╯"
	result := renderBorderTitle(box, "Info", TitleStyle)
	lines := strings.Split(result, "\n")
	clean := ansiRegex.ReplaceAllString(lines[0], "")
	if !strings.HasPrefix(clean, "╭─") {
		t.Errorf("expected top line to start with ╭─, got %q", clean)
	}
	if !strings.Contains(clean, "Info") {
		t.Errorf("expected top line to contain title, got %q", clean)
	}
	if !strings.HasSuffix(clean, "╮") {
		t.Errorf("expected top line to end with ╮, got %q", clean)
	}
}

func TestRenderBorderTitleWithANSI(t *testing.T) {
	t.Parallel()
	// Simulate a lipgloss-rendered box with 24-bit color ANSI sequences.
	// This is the pattern that broke in truecolor terminals (zsh).
	ansiColor := "\x1b[38;2;203;166;247m"
	reset := "\x1b[0m"
	topLine := ansiColor + "╭──────────────────╮" + reset
	box := topLine + "\n" + ansiColor + "│ content          │" + reset + "\n" + ansiColor + "╰──────────────────╯" + reset

	result := renderBorderTitle(box, "Info", TitleStyle)
	lines := strings.Split(result, "\n")

	// The top line must not contain raw SGR parameters as visible text.
	clean := ansiRegex.ReplaceAllString(lines[0], "")
	if strings.Contains(clean, "38;2;") {
		t.Errorf("raw ANSI parameters leaked into visible text: %q", clean)
	}
	if !strings.HasPrefix(clean, "╭─") {
		t.Errorf("expected top line to start with ╭─, got %q", clean)
	}
	if !strings.Contains(clean, "Info") {
		t.Errorf("expected title 'Info' in top line, got %q", clean)
	}
	if !strings.HasSuffix(clean, "╮") {
		t.Errorf("expected top line to end with ╮, got %q", clean)
	}
}

func TestRenderBorderTitlePreservesBorderColor(t *testing.T) {
	t.Parallel()
	// The border color from the original box should be re-applied.
	ansiColor := "\x1b[38;2;49;50;68m" // colorSurface dark
	reset := "\x1b[0m"
	topLine := ansiColor + "╭────────────╮" + reset
	box := topLine + "\n" + ansiColor + "│ hi         │" + reset + "\n" + ansiColor + "╰────────────╯" + reset

	result := renderBorderTitle(box, "X", TitleStyle)
	lines := strings.Split(result, "\n")

	// The border color sequence should appear in the output.
	if !strings.Contains(lines[0], ansiColor) {
		t.Errorf("border color not preserved in top line: %q", lines[0])
	}
}

func TestRenderBorderTitleWidth(t *testing.T) {
	t.Parallel()
	// The visual width of the top line must match the original.
	style := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorSurface).
		Width(30).
		Padding(0, 1)
	box := style.Render("test content")
	origLines := strings.Split(box, "\n")
	origWidth := lipgloss.Width(origLines[0])

	result := renderBorderTitle(box, "Title", TitleStyle)
	newLines := strings.Split(result, "\n")
	newWidth := lipgloss.Width(newLines[0])

	if newWidth != origWidth {
		t.Errorf("width changed: original=%d new=%d", origWidth, newWidth)
	}
}

func TestPanelStyleV2BoxModel(t *testing.T) {
	t.Parallel()
	// Horizontal frame: 2 (border left+right) + 2 (padding left+right) = 4
	// Vertical frame:   2 (border top+bottom)
	const hFrame = 4
	const vFrame = 2

	t.Run("total width matches Width(w)", func(t *testing.T) {
		tests := []struct {
			name   string
			width  int
			active bool
		}{
			{"inactive w=40", 40, false},
			{"active w=40", 40, true},
			{"inactive w=80", 80, false},
			{"inactive w=120", 120, false},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				style := panelStyle(tt.active).Width(tt.width)
				rendered := style.Render("hello")
				for i, line := range strings.Split(rendered, "\n") {
					got := lipgloss.Width(line)
					if got != tt.width {
						t.Errorf("line %d: rendered width = %d, want %d", i, got, tt.width)
					}
				}
			})
		}
	})

	t.Run("total height matches Height(h)", func(t *testing.T) {
		tests := []struct {
			name   string
			height int
		}{
			{"h=5", 5},
			{"h=10", 10},
			{"h=20", 20},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				style := panelStyle(false).Width(40).Height(tt.height)
				rendered := style.Render("hello")
				lines := strings.Split(rendered, "\n")
				if len(lines) != tt.height {
					t.Errorf("line count = %d, want %d", len(lines), tt.height)
				}
			})
		}
	})

	t.Run("content area dimensions", func(t *testing.T) {
		tests := []struct {
			name       string
			width      int
			height     int
			wantCW     int
			wantCLines int
		}{
			{"40x10", 40, 10, 40 - hFrame, 10 - vFrame},
			{"80x20", 80, 20, 80 - hFrame, 20 - vFrame},
			{"120x30", 120, 30, 120 - hFrame, 30 - vFrame},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				style := panelStyle(false).Width(tt.width).Height(tt.height)
				// Fill content so we can measure actual content area.
				filler := strings.Repeat("X", tt.wantCW)
				contentLines := make([]string, tt.wantCLines)
				for i := range contentLines {
					contentLines[i] = filler
				}
				rendered := style.Render(strings.Join(contentLines, "\n"))
				lines := strings.Split(rendered, "\n")

				// Total dimensions must still hold.
				if len(lines) != tt.height {
					t.Errorf("total height = %d, want %d", len(lines), tt.height)
				}
				for i, line := range lines {
					if w := lipgloss.Width(line); w != tt.width {
						t.Errorf("line %d: total width = %d, want %d", i, w, tt.width)
					}
				}

				// Inner content lines (skip top/bottom border) should contain the filler.
				innerLines := lines[1 : len(lines)-1]
				if len(innerLines) != tt.wantCLines {
					t.Errorf("content lines = %d, want %d", len(innerLines), tt.wantCLines)
				}
				for i, line := range innerLines {
					clean := ansiRegex.ReplaceAllString(line, "")
					// Content line format: "│ " + content + " │"
					// Strip border chars and padding, check content width.
					inner := strings.TrimPrefix(clean, "│ ")
					inner = strings.TrimSuffix(inner, " │")
					// The inner content may be space-padded; measure visible width.
					if w := lipgloss.Width(inner); w != tt.wantCW {
						t.Errorf("content line %d: inner width = %d, want %d", i, w, tt.wantCW)
					}
				}
			})
		}
	})

	t.Run("dashboard panel widths", func(t *testing.T) {
		// Simulate dashboard layout: terminal width 120, 30% left split.
		const termWidth = 120
		const leftPct = 30
		leftWidth := termWidth * leftPct / 100 // 36
		rightWidth := termWidth - leftWidth    // 84

		if leftWidth != 36 {
			t.Fatalf("leftWidth = %d, want 36", leftWidth)
		}
		if rightWidth != 84 {
			t.Fatalf("rightWidth = %d, want 84", rightWidth)
		}

		leftPanel := panelStyle(true).Width(leftWidth).Render("left content")
		rightPanel := panelStyle(false).Width(rightWidth).Render("right content")

		leftLines := strings.Split(leftPanel, "\n")
		rightLines := strings.Split(rightPanel, "\n")

		for i, line := range leftLines {
			if w := lipgloss.Width(line); w != leftWidth {
				t.Errorf("left panel line %d: width = %d, want %d", i, w, leftWidth)
			}
		}
		for i, line := range rightLines {
			if w := lipgloss.Width(line); w != rightWidth {
				t.Errorf("right panel line %d: width = %d, want %d", i, w, rightWidth)
			}
		}

		// Combined width must exactly fill the terminal.
		combined := lipgloss.Width(leftLines[0]) + lipgloss.Width(rightLines[0])
		if combined != termWidth {
			t.Errorf("combined width = %d, want %d", combined, termWidth)
		}
	})

	t.Run("nested panel (detail ViewCompact pattern)", func(t *testing.T) {
		// The dashboard right panel is rendered at Width(rightWidth).
		// Inside ViewCompact, inner boxes use Width(rightWidth - 4) which
		// should exactly fill the outer panel's content area.
		const rightWidth = 84
		const innerWidth = rightWidth - hFrame // 80

		innerBox := panelStyle(false).Width(innerWidth).Render("inner content")
		innerLines := strings.Split(innerBox, "\n")

		for i, line := range innerLines {
			if w := lipgloss.Width(line); w != innerWidth {
				t.Errorf("inner box line %d: width = %d, want %d", i, w, innerWidth)
			}
		}

		// The inner box should exactly fit inside the outer content area.
		outerContentWidth := rightWidth - hFrame
		if innerWidth != outerContentWidth {
			t.Errorf("inner width %d != outer content width %d", innerWidth, outerContentWidth)
		}
	})
}

func TestLeadingANSI(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"no ansi", "╭──╮", ""},
		{"single sequence", "\x1b[38;2;1;2;3m╭──╮", "\x1b[38;2;1;2;3m"},
		{"multiple sequences", "\x1b[1m\x1b[38;2;1;2;3m╭──╮", "\x1b[1m\x1b[38;2;1;2;3m"},
		{"empty string", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := leadingANSI(tt.input)
			if got != tt.want {
				t.Errorf("leadingANSI(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestChatTagStylesAreDistinctColors(t *testing.T) {
	t.Parallel()
	userRendered := chatUserTagStyle.Render("[you]")
	agentRendered := chatAgentTagStyle.Render("[agent]")
	if userRendered == agentRendered {
		t.Error("chatUserTagStyle and chatAgentTagStyle must render differently")
	}
	if chatAgentTagStyle.GetForeground() == chatUserTagStyle.GetForeground() {
		t.Error("agent tag must use a different accent color than the user tag")
	}
}
