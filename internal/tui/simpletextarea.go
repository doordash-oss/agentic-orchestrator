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
	"image/color"
	"strings"
	"unicode/utf8"

	"charm.land/bubbles/v2/cursor"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// SimpleTextarea is a minimal multiline text editor with soft word-wrapping.
// Long logical lines wrap visually at the widget width but remain single lines
// in the data model, avoiding phantom newlines from the bubbles textarea engine.
// Scroll offset and cursor navigation operate in visual-line space.
type SimpleTextarea struct {
	lines        []string
	row, col     int // cursor position (col in runes)
	width        int
	height       int
	scrollOffset int // first visible line
	focused      bool
	cursor       cursor.Model

	Placeholder     string
	MaxHeight       int         // max number of lines (0 = unlimited)
	CharLimit       int         // max total characters (0 = unlimited)
	BackgroundColor color.Color // optional background applied to text and padding (not cursor)
}

// NewSimpleTextarea creates a SimpleTextarea with sensible defaults.
func NewSimpleTextarea() SimpleTextarea {
	return SimpleTextarea{
		lines:  []string{""},
		width:  40,
		height: 4,
		cursor: cursor.New(),
	}
}

// SimpleTextareaBlink returns a blink command for the cursor.
func SimpleTextareaBlink() tea.Msg {
	return cursor.Blink()
}

// Focus focuses the textarea and starts cursor blink.
func (t *SimpleTextarea) Focus() tea.Cmd {
	t.focused = true
	return t.cursor.Focus()
}

// Blur removes focus from the textarea.
func (t *SimpleTextarea) Blur() {
	t.focused = false
	t.cursor.Blur()
}

// Focused returns whether the textarea is focused.
func (t *SimpleTextarea) Focused() bool {
	return t.focused
}

// Value returns the full text with lines joined by \n.
func (t *SimpleTextarea) Value() string {
	return strings.Join(t.lines, "\n")
}

// SetValue replaces the content, placing the cursor at the end.
func (t *SimpleTextarea) SetValue(s string) {
	if s == "" {
		t.lines = []string{""}
		t.row = 0
		t.col = 0
		t.scrollOffset = 0
		return
	}
	t.lines = strings.Split(s, "\n")
	t.row = len(t.lines) - 1
	t.col = utf8.RuneCountInString(t.lines[t.row])
	t.ensureVisible()
}

// Reset clears all content.
func (t *SimpleTextarea) Reset() {
	t.SetValue("")
}

// InsertString inserts a string at the current cursor position.
func (t *SimpleTextarea) InsertString(s string) {
	for _, r := range s {
		if r == '\n' {
			t.insertNewline()
		} else {
			t.insertRune(r)
		}
	}
}

// SetWidth sets the visible width.
func (t *SimpleTextarea) SetWidth(w int) {
	if w < 1 {
		w = 1
	}
	t.width = w
}

// SetHeight sets the visible height (number of lines).
func (t *SimpleTextarea) SetHeight(h int) {
	if h < 1 {
		h = 1
	}
	t.height = h
}

// Width returns the current width.
func (t *SimpleTextarea) Width() int {
	return t.width
}

// Line returns the current cursor row (0-based).
func (t *SimpleTextarea) Line() int {
	return t.row
}

// LineCount returns the number of lines.
func (t *SimpleTextarea) LineCount() int {
	return len(t.lines)
}

// totalChars returns the total character count (runes + newlines between lines).
func (t *SimpleTextarea) totalChars() int {
	total := 0
	for i, line := range t.lines {
		total += utf8.RuneCountInString(line)
		if i > 0 {
			total++ // count the \n
		}
	}
	return total
}

// Update handles input messages.
func (t SimpleTextarea) Update(msg tea.Msg) (SimpleTextarea, tea.Cmd) {
	if !t.focused {
		return t, nil
	}

	switch msg := msg.(type) {
	case tea.PasteMsg:
		t.InsertString(msg.Content)
		t.ensureVisible()
		return t, nil
	case tea.KeyPressMsg:
		hasAlt := msg.Mod.Contains(tea.ModAlt)

		// Handle Alt combos first. On macOS, Option+Left sends ESC+b
		// (Code='b', Mod=ModAlt, Text=""), while other terminals send
		// CSI modified arrows (Code=KeyLeft, Mod=ModAlt). Check Code
		// for both letter runes and arrow keys.
		if hasAlt {
			switch msg.Code {
			case 'b', tea.KeyLeft:
				t.wordLeft()
			case 'f', tea.KeyRight:
				t.wordRight()
			case 'd':
				t.deleteWordForward()
			case tea.KeyBackspace:
				t.deleteWordBackward()
			case tea.KeyDelete:
				t.deleteWordForward()
			}
			t.ensureVisible()
			return t, nil
		}

		switch {
		case len(msg.Text) > 0:
			for _, r := range msg.Text {
				t.insertRune(r)
			}
		case msg.Code == tea.KeySpace:
			t.insertRune(' ')
		case msg.Code == tea.KeyTab:
			for i := 0; i < 4; i++ {
				t.insertRune(' ')
			}
		case msg.Code == tea.KeyEnter:
			t.insertNewline()
		case msg.Code == tea.KeyBackspace:
			t.backspace()
		case msg.Code == tea.KeyDelete:
			t.delete()
		case msg.Code == tea.KeyUp:
			t.moveUp()
		case msg.Code == tea.KeyDown:
			t.moveDown()
		case msg.Code == tea.KeyLeft:
			t.moveLeft()
		case msg.Code == tea.KeyRight:
			t.moveRight()
		case msg.Code == tea.KeyHome || msg.String() == "ctrl+a":
			t.col = 0
		case msg.Code == tea.KeyEnd || msg.String() == "ctrl+e":
			t.col = utf8.RuneCountInString(t.lines[t.row])
		case msg.String() == "ctrl+k":
			runes := []rune(t.lines[t.row])
			t.lines[t.row] = string(runes[:t.col])
		case msg.String() == "ctrl+u":
			runes := []rune(t.lines[t.row])
			t.lines[t.row] = string(runes[t.col:])
			t.col = 0
		case msg.String() == "ctrl+w":
			t.deleteWordBackward()
		}
		t.ensureVisible()
		return t, nil

	default:
		var cmd tea.Cmd
		t.cursor, cmd = t.cursor.Update(msg)
		return t, cmd
	}
}

func (t *SimpleTextarea) insertRune(r rune) {
	if r == '\n' || r == '\r' {
		t.insertNewline()
		return
	}
	if r < 32 {
		return
	}
	if t.CharLimit > 0 && t.totalChars() >= t.CharLimit {
		return
	}
	runes := []rune(t.lines[t.row])
	if t.col > len(runes) {
		t.col = len(runes)
	}
	newRunes := make([]rune, 0, len(runes)+1)
	newRunes = append(newRunes, runes[:t.col]...)
	newRunes = append(newRunes, r)
	newRunes = append(newRunes, runes[t.col:]...)
	t.lines[t.row] = string(newRunes)
	t.col++
}

func (t *SimpleTextarea) insertNewline() {
	if t.MaxHeight > 0 && len(t.lines) >= t.MaxHeight {
		return
	}
	if t.CharLimit > 0 && t.totalChars() >= t.CharLimit {
		return
	}
	runes := []rune(t.lines[t.row])
	if t.col > len(runes) {
		t.col = len(runes)
	}
	before := string(runes[:t.col])
	after := string(runes[t.col:])

	newLines := make([]string, 0, len(t.lines)+1)
	newLines = append(newLines, t.lines[:t.row]...)
	newLines = append(newLines, before)
	newLines = append(newLines, after)
	newLines = append(newLines, t.lines[t.row+1:]...)
	t.lines = newLines
	t.row++
	t.col = 0
}

func (t *SimpleTextarea) backspace() {
	if t.col > 0 {
		runes := []rune(t.lines[t.row])
		t.lines[t.row] = string(append(runes[:t.col-1], runes[t.col:]...))
		t.col--
	} else if t.row > 0 {
		// Merge with previous line
		prevLen := utf8.RuneCountInString(t.lines[t.row-1])
		t.lines[t.row-1] += t.lines[t.row]
		t.lines = append(t.lines[:t.row], t.lines[t.row+1:]...)
		t.row--
		t.col = prevLen
	}
}

func (t *SimpleTextarea) delete() {
	runes := []rune(t.lines[t.row])
	if t.col < len(runes) {
		t.lines[t.row] = string(append(runes[:t.col], runes[t.col+1:]...))
	} else if t.row < len(t.lines)-1 {
		// Merge next line into current
		t.lines[t.row] += t.lines[t.row+1]
		t.lines = append(t.lines[:t.row+1], t.lines[t.row+2:]...)
	}
}

func (t *SimpleTextarea) moveUp() {
	visibleWidth := max(t.width, 1)
	visualCol := t.col % visibleWidth

	if t.col >= visibleWidth {
		// Move to previous visual line within the same logical line.
		t.col -= visibleWidth
		return
	}

	if t.row > 0 {
		t.row--
		prevLen := utf8.RuneCountInString(t.lines[t.row])
		if prevLen == 0 {
			t.col = 0
			return
		}
		lastVRow := (prevLen - 1) / visibleWidth
		t.col = lastVRow*visibleWidth + visualCol
		if t.col > prevLen {
			t.col = prevLen
		}
	}
}

func (t *SimpleTextarea) moveDown() {
	visibleWidth := max(t.width, 1)
	lineLen := utf8.RuneCountInString(t.lines[t.row])
	visualCol := t.col % visibleWidth
	nextVLineStart := ((t.col / visibleWidth) + 1) * visibleWidth

	if nextVLineStart < lineLen {
		// Move to next visual line within the same logical line.
		t.col = nextVLineStart + visualCol
		if t.col > lineLen {
			t.col = lineLen
		}
		return
	}

	if t.row < len(t.lines)-1 {
		t.row++
		nextLen := utf8.RuneCountInString(t.lines[t.row])
		if visualCol > nextLen {
			t.col = nextLen
		} else {
			t.col = visualCol
		}
	}
}

func (t *SimpleTextarea) moveLeft() {
	if t.col > 0 {
		t.col--
	} else if t.row > 0 {
		t.row--
		t.col = utf8.RuneCountInString(t.lines[t.row])
	}
}

func (t *SimpleTextarea) moveRight() {
	lineLen := utf8.RuneCountInString(t.lines[t.row])
	if t.col < lineLen {
		t.col++
	} else if t.row < len(t.lines)-1 {
		t.row++
		t.col = 0
	}
}

func (t *SimpleTextarea) wordLeft() {
	if t.col == 0 && t.row > 0 {
		t.row--
		t.col = utf8.RuneCountInString(t.lines[t.row])
		return
	}
	runes := []rune(t.lines[t.row])
	i := t.col - 1
	// Skip spaces
	for i > 0 && runes[i] == ' ' {
		i--
	}
	// Skip word characters
	for i > 0 && runes[i-1] != ' ' {
		i--
	}
	t.col = i
}

func (t *SimpleTextarea) wordRight() {
	runes := []rune(t.lines[t.row])
	lineLen := len(runes)
	if t.col >= lineLen && t.row < len(t.lines)-1 {
		t.row++
		t.col = 0
		return
	}
	i := t.col
	// Skip word characters
	for i < lineLen && runes[i] != ' ' {
		i++
	}
	// Skip spaces
	for i < lineLen && runes[i] == ' ' {
		i++
	}
	t.col = i
}

func (t *SimpleTextarea) deleteWordBackward() {
	if t.col == 0 {
		t.backspace()
		return
	}
	runes := []rune(t.lines[t.row])
	end := t.col
	i := end - 1
	for i > 0 && runes[i] == ' ' {
		i--
	}
	for i > 0 && runes[i-1] != ' ' {
		i--
	}
	t.lines[t.row] = string(append(runes[:i], runes[end:]...))
	t.col = i
}

func (t *SimpleTextarea) deleteWordForward() {
	runes := []rune(t.lines[t.row])
	if t.col >= len(runes) {
		t.delete()
		return
	}
	i := t.col
	for i < len(runes) && runes[i] != ' ' {
		i++
	}
	for i < len(runes) && runes[i] == ' ' {
		i++
	}
	t.lines[t.row] = string(append(runes[:t.col], runes[i:]...))
}

func (t *SimpleTextarea) ensureVisible() {
	visibleWidth := max(t.width, 1)

	// Compute the cursor's visual row across all logical lines.
	vRow := 0
	for i := 0; i < t.row; i++ {
		n := utf8.RuneCountInString(t.lines[i])
		if n == 0 {
			vRow++
		} else {
			vRow += (n + visibleWidth - 1) / visibleWidth
		}
	}
	vRow += t.col / visibleWidth

	if vRow < t.scrollOffset {
		t.scrollOffset = vRow
	}
	if vRow >= t.scrollOffset+t.height {
		t.scrollOffset = vRow - t.height + 1
	}
}

// vLine represents one visual line segment of a logical line.
type vLine struct {
	runes     []rune
	colOffset int // rune offset within the logical line
	logRow    int // which logical line this belongs to
}

// View renders the textarea with soft-wrapping.
func (t SimpleTextarea) View() string {
	if !t.focused && t.Value() == "" && t.Placeholder != "" {
		return t.renderPlaceholder()
	}

	visibleWidth := max(t.width, 1)

	var vlines []vLine
	cursorVI := 0

	for li := range t.lines {
		runes := []rune(t.lines[li])

		if len(runes) == 0 {
			if t.focused && li == t.row {
				cursorVI = len(vlines)
			}
			vlines = append(vlines, vLine{logRow: li})
			continue
		}

		for start := 0; start < len(runes); start += visibleWidth {
			end := start + visibleWidth
			if end > len(runes) {
				end = len(runes)
			}
			if t.focused && li == t.row && t.col >= start && t.col < start+visibleWidth {
				cursorVI = len(vlines)
			}
			vlines = append(vlines, vLine{runes: runes[start:end], colOffset: start, logRow: li})
		}

		if t.focused && li == t.row && t.col == len(runes) {
			if len(runes)%visibleWidth == 0 {
				cursorVI = len(vlines)
				vlines = append(vlines, vLine{colOffset: len(runes), logRow: li})
			} else {
				cursorVI = len(vlines) - 1
			}
		}
	}

	// Build a background style for text/padding (not cursor) if configured.
	bgStyle := lipgloss.NewStyle()
	if t.BackgroundColor != nil {
		bgStyle = bgStyle.Background(t.BackgroundColor)
	}

	var sb strings.Builder
	endVI := min(t.scrollOffset+t.height, len(vlines))

	for vi := t.scrollOffset; vi < endVI; vi++ {
		if vi > t.scrollOffset {
			sb.WriteString("\n")
		}
		vl := vlines[vi]

		if vi == cursorVI && t.focused {
			localCol := t.col - vl.colOffset
			if localCol < 0 {
				localCol = 0
			}
			if localCol > len(vl.runes) {
				localCol = len(vl.runes)
			}

			sb.WriteString(bgStyle.Render(string(vl.runes[:localCol])))

			cursorChar := " "
			if localCol < len(vl.runes) {
				cursorChar = string(vl.runes[localCol])
			}
			t.cursor.SetChar(cursorChar)
			sb.WriteString(t.cursor.View())

			if localCol+1 < len(vl.runes) {
				sb.WriteString(bgStyle.Render(string(vl.runes[localCol+1:])))
			}

			rendered := len(vl.runes)
			if localCol >= len(vl.runes) {
				rendered = localCol + 1
			}
			if rendered < visibleWidth {
				sb.WriteString(bgStyle.Render(strings.Repeat(" ", visibleWidth-rendered)))
			}
		} else {
			line := string(vl.runes)
			if pad := visibleWidth - len(vl.runes); pad > 0 {
				line += strings.Repeat(" ", pad)
			}
			sb.WriteString(bgStyle.Render(line))
		}
	}

	// Pad remaining empty visible lines
	for vi := endVI; vi < t.scrollOffset+t.height; vi++ {
		sb.WriteString("\n")
		sb.WriteString(bgStyle.Render(strings.Repeat(" ", visibleWidth)))
	}

	return sb.String()
}

func (t SimpleTextarea) renderPlaceholder() string {
	style := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	visibleWidth := max(t.width, 1)

	pRunes := []rune(t.Placeholder)
	if len(pRunes) > visibleWidth {
		pRunes = pRunes[:visibleWidth]
	}

	var sb strings.Builder
	sb.WriteString(style.Render(string(pRunes)))
	if pad := visibleWidth - len(pRunes); pad > 0 {
		sb.WriteString(strings.Repeat(" ", pad))
	}
	for i := 1; i < t.height; i++ {
		sb.WriteString("\n")
		sb.WriteString(strings.Repeat(" ", visibleWidth))
	}
	return sb.String()
}
