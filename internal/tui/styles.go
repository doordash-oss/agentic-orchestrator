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
	"regexp"
	"strings"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/compat"
	"github.com/charmbracelet/x/ansi"
)

// ansiRegex matches ANSI escape sequences for stripping from rendered text.
var ansiRegex = regexp.MustCompile(`\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[\[\]()][^\x1b]*?[a-zA-Z~\\]|\x1b[=>]|\x1b\[\?[0-9;]*[a-zA-Z]`)

// Color palette — Catppuccin-inspired with adaptive light/dark support.
var (
	colorBase    = compat.AdaptiveColor{Light: lipgloss.Color("#eff1f5"), Dark: lipgloss.Color("#1e1e2e")}
	colorSurface = compat.AdaptiveColor{Light: lipgloss.Color("#ccd0da"), Dark: lipgloss.Color("#313244")}
	colorOverlay = compat.AdaptiveColor{Light: lipgloss.Color("#9ca0b0"), Dark: lipgloss.Color("#6c7086")}
	colorText    = compat.AdaptiveColor{Light: lipgloss.Color("#4c4f69"), Dark: lipgloss.Color("#cdd6f4")}
	colorSubtext = compat.AdaptiveColor{Light: lipgloss.Color("#6c6f85"), Dark: lipgloss.Color("#a6adc8")}

	// Semantic colors
	colorBrand   = compat.AdaptiveColor{Light: lipgloss.Color("#8839ef"), Dark: lipgloss.Color("#cba6f7")} // Mauve
	colorSuccess = compat.AdaptiveColor{Light: lipgloss.Color("#40a02b"), Dark: lipgloss.Color("#a6e3a1")} // Green
	colorWarning = compat.AdaptiveColor{Light: lipgloss.Color("#df8e1d"), Dark: lipgloss.Color("#f9e2af")} // Yellow
	colorError   = compat.AdaptiveColor{Light: lipgloss.Color("#d20f39"), Dark: lipgloss.Color("#f38ba8")} // Red
	colorInfo    = compat.AdaptiveColor{Light: lipgloss.Color("#1e66f5"), Dark: lipgloss.Color("#89b4fa")} // Blue
	colorActive  = compat.AdaptiveColor{Light: lipgloss.Color("#179299"), Dark: lipgloss.Color("#94e2d5")} // Teal
	colorPeach   = compat.AdaptiveColor{Light: lipgloss.Color("#fe640b"), Dark: lipgloss.Color("#fab387")} // Peach

	// DSP mode (--dangerously-skip-permissions) — menacing red-on-black theme
	colorDSPRed    = compat.AdaptiveColor{Light: lipgloss.Color("#cc0000"), Dark: lipgloss.Color("#ff2020")}
	colorDSPDimRed = compat.AdaptiveColor{Light: lipgloss.Color("#8b0000"), Dark: lipgloss.Color("#991111")}
	colorDSPBlack  = compat.AdaptiveColor{Light: lipgloss.Color("#2b0000"), Dark: lipgloss.Color("#0a0000")}
)

// Base text styles
var (
	TitleStyle    = lipgloss.NewStyle().Bold(true).Foreground(colorBrand)
	SubtitleStyle = lipgloss.NewStyle().Foreground(colorOverlay)
	ErrorStyle    = lipgloss.NewStyle().Bold(true).Foreground(colorError)
	SuccessStyle  = lipgloss.NewStyle().Foreground(colorSuccess)
	WarningStyle  = lipgloss.NewStyle().Foreground(colorWarning)
	ReviewStyle   = lipgloss.NewStyle().Foreground(colorBrand)
	BadgeStyle    = lipgloss.NewStyle().Bold(true).Foreground(colorPeach)
	MutedStyle    = lipgloss.NewStyle().Foreground(colorOverlay)

	// chatUserTagStyle marks a "[you]" turn tag in the AMA/attach transcripts.
	chatUserTagStyle = lipgloss.NewStyle().Bold(true).Foreground(colorBrand)

	// chatAgentTagStyle marks a "[agent]" turn tag — a distinct accent (Teal)
	// from colorBrand so the agent's voice reads as a different kind of
	// surface than chrome/user input, without clashing with colorInfo's
	// existing "in-progress status" semantics elsewhere in styles.go.
	chatAgentTagStyle = lipgloss.NewStyle().Bold(true).Foreground(colorActive)

	// chatAgentTagErrorStyle is the error-state variant of the agent tag.
	chatAgentTagErrorStyle = lipgloss.NewStyle().Bold(true).Foreground(colorError)
)

// Layout styles
var (
	// SelectedRowStyle highlights the currently selected row in lists
	SelectedRowStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(colorActive)

	// SectionHeaderStyle for section dividers like "IN PROGRESS", "COMPLETED"
	SectionHeaderStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(colorSubtext)

	// SectionHeaderSelectedStyle highlights a section header when selected by cursor
	SectionHeaderSelectedStyle = lipgloss.NewStyle().
					Bold(true).
					Foreground(colorActive)

	// BoxStyle wraps content sections with a rounded border
	BoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorSurface).
			Padding(0, 1)

	// KeyHelpStyle for the bottom action bar
	KeyHelpStyle = lipgloss.NewStyle().
			Foreground(colorOverlay).
			MarginTop(1)

	// LabelStyle for metadata labels in the detail view
	LabelStyle = lipgloss.NewStyle().
			Foreground(colorSubtext).
			Width(12)

	// VersionStyle for the version badge
	VersionStyle = lipgloss.NewStyle().
			Foreground(colorOverlay)

	// StepStyle for wizard step indicator
	StepStyle = lipgloss.NewStyle().
			Foreground(colorOverlay)

	// WizardLabelStyle is a wider label for the wizard review screen
	// to accommodate "Exit Criteria" (14 chars).
	WizardLabelStyle = lipgloss.NewStyle().
				Foreground(colorSubtext).
				Width(14)

	// SummarySelectedValueStyle highlights the value on the currently selected row.
	SummarySelectedValueStyle = lipgloss.NewStyle().
					Foreground(colorActive)
)

// statusIcon returns a styled icon character for each feature status.
func statusIcon(status string) string {
	switch status {
	case "Created":
		return MutedStyle.Render(icons.Created)
	case "SettingUpWorktrees":
		return lipgloss.NewStyle().Foreground(colorInfo).Render(icons.Created)
	case "Researching":
		return lipgloss.NewStyle().Foreground(colorInfo).Render(icons.Researching)
	case "Planning":
		return lipgloss.NewStyle().Foreground(colorInfo).Render(icons.Planning)
	case "Implementing":
		return lipgloss.NewStyle().Foreground(colorInfo).Render(icons.Implementing)
	case "PlanReady", "ImplementReady", "PlanNeedsReview":
		return lipgloss.NewStyle().Foreground(colorWarning).Render(icons.Ready)
	case "PromptNeedsReview", "InquiryNeedsReview", "ResearchNeedsReview", "DesignNeedsReview":
		return lipgloss.NewStyle().Foreground(colorBrand).Render(icons.Ready)
	case "ReviewPassed", "CodeReady":
		return SuccessStyle.Render(icons.Done)
	case "Published", "Done":
		return SuccessStyle.Render(icons.Done)
	case "Failed":
		return ErrorStyle.Render(icons.Failed)
	case "Reviewing", "FinalReviewing":
		return ReviewStyle.Render(icons.Implementing)
	case "Interrupted":
		return WarningStyle.Render(icons.Interrupted)
	default:
		return "?"
	}
}

// phaseIcon returns a styled icon for phase progress.
// When spinnerView is non-empty, it's used for the current phase (animated spinner).
func phaseIcon(done, current bool) string {
	if done {
		return SuccessStyle.Render(icons.Done)
	}
	if current {
		return lipgloss.NewStyle().Foreground(colorInfo).Bold(true).Render(icons.Implementing)
	}
	return MutedStyle.Render(icons.Created)
}

// panelStyle returns a bordered style based on whether the panel is active.
func panelStyle(active bool) lipgloss.Style {
	borderColor := colorSurface
	if active {
		borderColor = colorBrand
	}
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Padding(0, 1)
}

// ansi24bitFg matches 24-bit ANSI foreground color sequences, including when
// combined with other SGR attributes (e.g. bold): ESC[1;38;2;R;G;Bm
var ansi24bitFg = regexp.MustCompile(`\x1b\[([\d;]*?)38;2;(\d+);(\d+);(\d+)(;[\d;]*)?m`)

// ansiReset matches ANSI SGR reset sequences (both \x1b[m and \x1b[0m).
var ansiReset = regexp.MustCompile(`\x1b\[0?m`)

// dimContent reduces the brightness of all 24-bit ANSI foreground colors by
// halving their RGB values, and sets a dim foreground for unstyled text.
// This preserves color identity (green stays green, red stays red) while
// visually de-emphasizing the inactive panel.
func dimContent(s string) string {
	// Reduce RGB brightness to 65% in all 24-bit foreground color sequences
	dimmed := ansi24bitFg.ReplaceAllFunc([]byte(s), func(match []byte) []byte {
		parts := ansi24bitFg.FindSubmatch(match)
		if len(parts) != 6 {
			return match
		}
		prefix := string(parts[1]) // e.g. "1;" for bold
		r := atoiSafe(parts[2]) * 65 / 100
		g := atoiSafe(parts[3]) * 65 / 100
		b := atoiSafe(parts[4]) * 65 / 100
		suffix := string(parts[5]) // e.g. ";4" for underline
		return []byte(fmt.Sprintf("\x1b[%s38;2;%d;%d;%d%sm", prefix, r, g, b, suffix))
	})

	// Get the dim color's ANSI escape sequence by rendering through lipgloss
	// so it resolves the adaptive color for the current terminal.
	dimEsc := extractFgEscape(colorSubtext)

	// For each line: prepend the dim color (sets default for unstyled text),
	// and re-apply it after every ANSI reset so inner resets don't revert
	// to full-brightness terminal default.
	lines := strings.Split(string(dimmed), "\n")
	for i, line := range lines {
		line = ansiReset.ReplaceAllString(line, "${0}"+dimEsc)
		lines[i] = dimEsc + line + "\x1b[m"
	}
	return strings.Join(lines, "\n")
}

// extractFgEscape renders a marker character with the given color and returns
// the ANSI escape prefix (everything lipgloss emits before the content).
func extractFgEscape(c compat.AdaptiveColor) string {
	const marker = "\x01"
	rendered := lipgloss.NewStyle().Foreground(c).Render(marker)
	idx := strings.Index(rendered, marker)
	if idx > 0 {
		return rendered[:idx]
	}
	return ""
}

// atoiSafe parses a byte slice as an integer, returning 0 on failure.
func atoiSafe(b []byte) int {
	n := 0
	for _, c := range b {
		if c >= '0' && c <= '9' {
			n = n*10 + int(c-'0')
		}
	}
	return n
}

// renderBorderTitle injects a title into the top border of a rendered box.
// The box must already be rendered (string). This replaces the first line's
// border characters after the corner to insert the styled title.
func renderBorderTitle(box, title string, titleStyle lipgloss.Style) string {
	lines := strings.Split(box, "\n")
	if len(lines) == 0 {
		return box
	}

	line := lines[0]
	visWidth := lipgloss.Width(line)
	if visWidth < 4 {
		return box
	}

	// The rendered line contains ANSI escape sequences for the border
	// color, e.g. "\x1b[38;2;R;G;Bm╭──────╮\x1b[0m". Rune-level slicing
	// would split those sequences in half, leaking raw SGR parameters as
	// visible text. Instead, strip ANSI to get the clean border characters
	// for slicing, then extract the border color from the leading ANSI
	// sequences so it can be re-applied.
	clean := ansiRegex.ReplaceAllString(line, "")
	cleanRunes := []rune(clean)
	if len(cleanRunes) < 4 {
		return box
	}

	// Collect consecutive ANSI sequences at the start of the line — this
	// is the border color lipgloss applied.
	borderColor := leadingANSI(line)

	rendered := titleStyle.Render(title)
	// Build: "╭─" + " Title " + "──...──" + "╮", with border color
	// wrapping the non-title parts.
	insertPoint := 2 // after "╭─"
	opener := string(cleanRunes[:insertPoint])
	closer := string(cleanRunes[len(cleanRunes)-1:])

	newTop := borderColor + opener + "\x1b[0m " + rendered + " " + borderColor
	remainingWidth := visWidth - lipgloss.Width(newTop) - 1
	if remainingWidth > 0 {
		newTop += strings.Repeat("─", remainingWidth)
	}
	newTop += closer + "\x1b[0m"
	lines[0] = newTop
	return strings.Join(lines, "\n")
}

// leadingANSI returns the concatenation of all ANSI escape sequences at the
// very start of s (i.e., before the first visible character).
func leadingANSI(s string) string {
	result := ""
	for {
		loc := ansiRegex.FindStringIndex(s[len(result):])
		if loc == nil || loc[0] != 0 {
			break
		}
		result += s[len(result) : len(result)+loc[1]]
	}
	return result
}

// DiffAddStyle colors added lines in diffs.
var DiffAddStyle = lipgloss.NewStyle().Foreground(colorSuccess)

// DiffRemoveStyle colors removed lines in diffs.
var DiffRemoveStyle = lipgloss.NewStyle().Foreground(colorError)

// DiffHunkStyle colors hunk headers in diffs.
var DiffHunkStyle = lipgloss.NewStyle().Foreground(colorInfo)

// DiffHeaderStyle colors diff file headers.
var DiffHeaderStyle = lipgloss.NewStyle().Bold(true).Foreground(colorBrand)

// colorizeDiff applies syntax coloring to unified diff output.
func colorizeDiff(raw string) string {
	lines := strings.Split(raw, "\n")
	for i, line := range lines {
		if len(line) == 0 {
			continue
		}
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
			lines[i] = DiffHeaderStyle.Render(line)
		case strings.HasPrefix(line, "@@"):
			lines[i] = DiffHunkStyle.Render(line)
		case strings.HasPrefix(line, "+"):
			lines[i] = DiffAddStyle.Render(line)
		case strings.HasPrefix(line, "-"):
			lines[i] = DiffRemoveStyle.Render(line)
		case strings.HasPrefix(line, "diff "):
			lines[i] = DiffHeaderStyle.Render(line)
		}
	}
	return strings.Join(lines, "\n")
}

// truncateLines limits s to at most maxLines lines.
func truncateLines(s string, maxLines int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= maxLines {
		return s
	}
	return strings.Join(lines[:maxLines], "\n")
}

// truncateRenderedLines limits every rendered line to width display cells while
// preserving ANSI escape sequences.
func truncateRenderedLines(s string, width int) string {
	if width <= 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if ansi.StringWidth(line) > width {
			lines[i] = ansi.Truncate(line, width, "")
		}
	}
	return strings.Join(lines, "\n")
}

// overlayModal renders modal centered on top of a dimmed background.
func overlayModal(background, modal string, width, height int) string {
	dimmed := dimText(background)
	return placeCentered(dimmed, modal, width, height)
}

// dimText strips ANSI styles and re-applies a dark foreground color.
func dimText(s string) string {
	dim := lipgloss.NewStyle().Foreground(colorSurface)
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		clean := ansiRegex.ReplaceAllString(line, "")
		lines[i] = dim.Render(clean)
	}
	return strings.Join(lines, "\n")
}

// placeCentered overlays modal lines centered on the background.
func placeCentered(bg, modal string, width, height int) string {
	bgLines := strings.Split(bg, "\n")
	for len(bgLines) < height {
		bgLines = append(bgLines, "")
	}
	if len(bgLines) > height {
		bgLines = bgLines[:height]
	}

	modalLines := strings.Split(modal, "\n")
	modalW := lipgloss.Width(modal)

	topOffset := (height - len(modalLines)) / 2
	leftOffset := (width - modalW) / 2
	if topOffset < 0 {
		topOffset = 0
	}
	if leftOffset < 0 {
		leftOffset = 0
	}

	leftPad := strings.Repeat(" ", leftOffset)
	for i, ml := range modalLines {
		row := topOffset + i
		if row >= 0 && row < len(bgLines) {
			bgLines[row] = leftPad + ml
		}
	}

	return strings.Join(bgLines, "\n")
}
