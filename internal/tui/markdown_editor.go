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
	"os"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"charm.land/bubbles/v2/cursor"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// EditorMode represents the vim-like mode of the editor.
type EditorMode int

const (
	NormalMode EditorMode = iota
	InsertMode
)

const maxUndoEntries = 50

type undoEntry struct {
	lines    []string
	row, col int
}

// MarkdownEditor is a vim-like markdown editor with line numbers,
// undo support, and agent edit highlighting.
type MarkdownEditor struct {
	lines        []string
	row, col     int // cursor position (col in runes)
	scrollOffset int
	width        int
	height       int
	mode         EditorMode
	dirty        bool
	filePath     string
	cursor       cursor.Model
	focused      bool

	// Two-key sequence state (gg, dd)
	pendingKey rune

	// Undo stack
	undoStack []undoEntry

	// Agent edit highlighting
	highlightedLines map[int]bool
}

// NewMarkdownEditor creates a MarkdownEditor with empty content.
func NewMarkdownEditor(width, height int) MarkdownEditor {
	return MarkdownEditor{
		lines:            []string{""},
		width:            width,
		height:           height,
		cursor:           cursor.New(),
		highlightedLines: make(map[int]bool),
	}
}

// Load reads a file from disk into the editor.
func (e *MarkdownEditor) Load(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("loading file: %w", err)
	}
	e.filePath = path
	content := string(data)
	if content == "" {
		e.lines = []string{""}
	} else {
		e.lines = strings.Split(content, "\n")
	}
	e.row = 0
	e.col = 0
	e.scrollOffset = 0
	e.dirty = false
	return nil
}

// Save writes the editor content back to disk.
func (e *MarkdownEditor) Save() error {
	if e.filePath == "" {
		return fmt.Errorf("no file path set")
	}
	content := strings.Join(e.lines, "\n")
	if err := os.WriteFile(e.filePath, []byte(content), 0o644); err != nil {
		return fmt.Errorf("saving file: %w", err)
	}
	e.dirty = false
	return nil
}

// Content returns the full editor content as a single string.
func (e *MarkdownEditor) Content() string {
	return strings.Join(e.lines, "\n")
}

// Dirty returns whether the editor has unsaved changes.
func (e *MarkdownEditor) Dirty() bool {
	return e.dirty
}

// MarkClean resets the dirty flag, indicating content matches the on-disk state.
func (e *MarkdownEditor) MarkClean() {
	e.dirty = false
}

// Mode returns the current editor mode.
func (e *MarkdownEditor) Mode() EditorMode {
	return e.mode
}

// FilePath returns the path of the loaded file.
func (e *MarkdownEditor) FilePath() string {
	return e.filePath
}

// SetSize updates the editor dimensions.
func (e *MarkdownEditor) SetSize(w, h int) {
	e.width = max(w, 1)
	e.height = max(h, 1)
	e.ensureVisible()
}

// Focus focuses the editor and starts cursor blink.
func (e *MarkdownEditor) Focus() tea.Cmd {
	e.focused = true
	return e.cursor.Focus()
}

// Blur removes focus from the editor.
func (e *MarkdownEditor) Blur() {
	e.focused = false
	e.cursor.Blur()
}

// Focused returns whether the editor is focused.
func (e *MarkdownEditor) Focused() bool {
	return e.focused
}

// lineNumberWidth returns the width reserved for line numbers.
func (e *MarkdownEditor) lineNumberWidth() int {
	digits := max(len(fmt.Sprintf("%d", len(e.lines))), 2)
	return digits + 1 // digits + 1 space separator
}

// contentWidth returns the available width for text content.
func (e *MarkdownEditor) contentWidth() int {
	return max(e.width-e.lineNumberWidth(), 1)
}

// Update handles input messages.
func (e MarkdownEditor) Update(msg tea.Msg) (MarkdownEditor, tea.Cmd) {
	if !e.focused {
		return e, nil
	}

	switch msg := msg.(type) {
	case tea.PasteMsg:
		if e.mode == InsertMode {
			e.pushUndo()
			for _, r := range msg.Content {
				if r == '\n' || r == '\r' {
					e.insertNewline()
				} else {
					e.insertRune(r)
				}
			}
			e.ensureVisible()
		}
		return e, nil

	case tea.KeyPressMsg:
		switch e.mode {
		case NormalMode:
			e.updateNormalMode(msg)
		case InsertMode:
			e.updateInsertMode(msg)
		}
		e.ensureVisible()
		return e, nil

	default:
		var cmd tea.Cmd
		e.cursor, cmd = e.cursor.Update(msg)
		return e, cmd
	}
}

func (e *MarkdownEditor) updateNormalMode(msg tea.KeyPressMsg) {
	key := msg.String()

	// Handle two-key sequences
	if e.pendingKey != 0 {
		pending := e.pendingKey
		e.pendingKey = 0
		switch {
		case pending == 'g' && key == "g":
			e.row = 0
			e.col = 0
			return
		case pending == 'd' && key == "d":
			e.deleteLine()
			return
		}
		// Not a valid sequence — discard pending and process current key normally
	}

	switch key {
	case "h", "left":
		if e.col > 0 {
			e.col--
		}
	case "j", "down":
		if e.row < len(e.lines)-1 {
			e.row++
			e.clampCol()
		}
	case "k", "up":
		if e.row > 0 {
			e.row--
			e.clampCol()
		}
	case "l", "right":
		lineLen := utf8.RuneCountInString(e.lines[e.row])
		if e.col < lineLen-1 {
			e.col++
		}
	case "i":
		e.pushUndo()
		e.mode = InsertMode
	case "a":
		e.pushUndo()
		lineLen := utf8.RuneCountInString(e.lines[e.row])
		if e.col < lineLen {
			e.col++
		}
		e.mode = InsertMode
	case "o":
		e.pushUndo()
		newLines := make([]string, 0, len(e.lines)+1)
		newLines = append(newLines, e.lines[:e.row+1]...)
		newLines = append(newLines, "")
		newLines = append(newLines, e.lines[e.row+1:]...)
		e.lines = newLines
		e.row++
		e.col = 0
		e.dirty = true
		e.mode = InsertMode
	case "O":
		e.pushUndo()
		newLines := make([]string, 0, len(e.lines)+1)
		newLines = append(newLines, e.lines[:e.row]...)
		newLines = append(newLines, "")
		newLines = append(newLines, e.lines[e.row:]...)
		e.lines = newLines
		e.col = 0
		e.dirty = true
		e.mode = InsertMode
	case "G":
		e.row = len(e.lines) - 1
		e.clampCol()
	case "g":
		e.pendingKey = 'g'
	case "d":
		e.pendingKey = 'd'
	case "0":
		e.col = 0
	case "$":
		lineLen := utf8.RuneCountInString(e.lines[e.row])
		if lineLen > 0 {
			e.col = lineLen - 1
		}
	case "w":
		e.wordForward()
	case "b":
		e.wordBackward()
	case "u":
		e.popUndo()
	case "x":
		e.pushUndo()
		e.delete()
	case "ctrl+u":
		e.halfPageUp()
	case "ctrl+f":
		e.halfPageDown()
	}
}

func (e *MarkdownEditor) pushUndo() {
	snapshot := undoEntry{
		lines: make([]string, len(e.lines)),
		row:   e.row,
		col:   e.col,
	}
	copy(snapshot.lines, e.lines)
	e.undoStack = append(e.undoStack, snapshot)
	if len(e.undoStack) > maxUndoEntries {
		e.undoStack = e.undoStack[1:]
	}
}

func (e *MarkdownEditor) popUndo() {
	if len(e.undoStack) == 0 {
		return
	}
	entry := e.undoStack[len(e.undoStack)-1]
	e.undoStack = e.undoStack[:len(e.undoStack)-1]
	e.lines = entry.lines
	e.row = entry.row
	e.col = entry.col
	e.dirty = true
}

func (e *MarkdownEditor) deleteLine() {
	e.pushUndo()
	if len(e.lines) == 1 {
		e.lines[0] = ""
		e.col = 0
	} else {
		e.lines = append(e.lines[:e.row], e.lines[e.row+1:]...)
		if e.row >= len(e.lines) {
			e.row = len(e.lines) - 1
		}
	}
	e.clampCol()
	e.dirty = true
}

func (e *MarkdownEditor) wordForward() {
	runes := []rune(e.lines[e.row])
	lineLen := len(runes)
	if e.col >= lineLen-1 {
		// Move to next line start
		if e.row < len(e.lines)-1 {
			e.row++
			e.col = 0
		}
		return
	}
	col := e.col
	// Skip current word characters
	for col < lineLen && !unicode.IsSpace(runes[col]) {
		col++
	}
	// Skip whitespace
	for col < lineLen && unicode.IsSpace(runes[col]) {
		col++
	}
	if col >= lineLen && e.row < len(e.lines)-1 {
		e.row++
		e.col = 0
	} else {
		e.col = min(col, max(lineLen-1, 0))
	}
}

func (e *MarkdownEditor) wordBackward() {
	if e.col == 0 {
		if e.row > 0 {
			e.row--
			lineLen := utf8.RuneCountInString(e.lines[e.row])
			if lineLen > 0 {
				e.col = lineLen - 1
			} else {
				e.col = 0
			}
		}
		return
	}
	runes := []rune(e.lines[e.row])
	col := e.col
	// Skip whitespace going backward
	for col > 0 && unicode.IsSpace(runes[col-1]) {
		col--
	}
	// Skip word characters going backward
	for col > 0 && !unicode.IsSpace(runes[col-1]) {
		col--
	}
	e.col = col
}

func (e *MarkdownEditor) halfPageUp() {
	half := max(e.height/2, 1)
	// Move up by visual lines
	remaining := half
	for remaining > 0 && e.row > 0 {
		e.row--
		remaining -= e.visualRowCount(e.row)
	}
	if e.row < 0 {
		e.row = 0
	}
	e.clampCol()
}

func (e *MarkdownEditor) halfPageDown() {
	half := max(e.height/2, 1)
	// Move down by visual lines
	remaining := half
	for remaining > 0 && e.row < len(e.lines)-1 {
		remaining -= e.visualRowCount(e.row)
		e.row++
	}
	if e.row >= len(e.lines) {
		e.row = len(e.lines) - 1
	}
	e.clampCol()
}

func (e *MarkdownEditor) updateInsertMode(msg tea.KeyPressMsg) {
	hasAlt := msg.Mod.Contains(tea.ModAlt)

	// Alt+letter or Alt+Arrow for word navigation. On macOS, Option+Left
	// sends ESC+b (Code='b', Mod=ModAlt, Text=""), while other terminals
	// send CSI modified arrows (Code=KeyLeft, Mod=ModAlt). Handle both by
	// checking Code for letter runes and arrow keys.
	if hasAlt {
		switch msg.Code {
		case 'b', tea.KeyLeft:
			e.wordBackward()
			return
		case 'f', tea.KeyRight:
			e.wordForward()
			return
		case 'd':
			e.pushUndo()
			e.deleteWordForward()
			return
		case tea.KeyBackspace:
			e.pushUndo()
			e.deleteWordBackward()
			return
		case tea.KeyDelete:
			e.pushUndo()
			e.deleteWordForward()
			return
		}
		// Unrecognised alt combo — ignore (don't insert stray chars)
		return
	}

	switch {
	case msg.Code == tea.KeyEscape:
		e.mode = NormalMode
		// In vim, Esc moves cursor back one if past position 0
		if e.col > 0 {
			e.col--
		}
	case len(msg.Text) > 0:
		for _, r := range msg.Text {
			e.insertRune(r)
		}
	case msg.Code == tea.KeySpace:
		e.insertRune(' ')
	case msg.Code == tea.KeyEnter:
		e.insertNewline()
	case msg.Code == tea.KeyBackspace:
		e.backspace()
	case msg.Code == tea.KeyDelete:
		e.delete()
	case msg.String() == "ctrl+w":
		e.pushUndo()
		e.deleteWordBackward()
	case msg.Code == tea.KeyUp:
		cw := max(e.contentWidth(), 1)
		visualCol := e.col % cw
		if e.col >= cw {
			// Move to previous visual line within the same logical line
			e.col -= cw
		} else if e.row > 0 {
			e.row--
			prevLen := utf8.RuneCountInString(e.lines[e.row])
			if prevLen == 0 {
				e.col = 0
			} else {
				lastVRow := (prevLen - 1) / cw
				e.col = lastVRow*cw + visualCol
				if e.col > prevLen {
					e.col = prevLen
				}
			}
		}
	case msg.Code == tea.KeyDown:
		cw := max(e.contentWidth(), 1)
		lineLen := utf8.RuneCountInString(e.lines[e.row])
		visualCol := e.col % cw
		nextVLineStart := ((e.col / cw) + 1) * cw
		if nextVLineStart < lineLen {
			// Move to next visual line within the same logical line
			e.col = nextVLineStart + visualCol
			if e.col > lineLen {
				e.col = lineLen
			}
		} else if e.row < len(e.lines)-1 {
			e.row++
			nextLen := utf8.RuneCountInString(e.lines[e.row])
			if visualCol > nextLen {
				e.col = nextLen
			} else {
				e.col = visualCol
			}
		}
	case msg.Code == tea.KeyLeft:
		if hasAlt {
			e.wordBackward()
		} else if e.col > 0 {
			e.col--
		} else if e.row > 0 {
			e.row--
			e.col = utf8.RuneCountInString(e.lines[e.row])
		}
	case msg.Code == tea.KeyRight:
		if hasAlt {
			e.wordForward()
		} else {
			lineLen := utf8.RuneCountInString(e.lines[e.row])
			if e.col < lineLen {
				e.col++
			} else if e.row < len(e.lines)-1 {
				e.row++
				e.col = 0
			}
		}
	case msg.Code == tea.KeyTab:
		for range 4 {
			e.insertRune(' ')
		}
	}
}

func (e *MarkdownEditor) insertRune(r rune) {
	if r == '\n' || r == '\r' {
		e.insertNewline()
		return
	}
	if r < 32 {
		return
	}
	runes := []rune(e.lines[e.row])
	if e.col > len(runes) {
		e.col = len(runes)
	}
	newRunes := make([]rune, 0, len(runes)+1)
	newRunes = append(newRunes, runes[:e.col]...)
	newRunes = append(newRunes, r)
	newRunes = append(newRunes, runes[e.col:]...)
	e.lines[e.row] = string(newRunes)
	e.col++
	e.dirty = true
}

func (e *MarkdownEditor) insertNewline() {
	runes := []rune(e.lines[e.row])
	if e.col > len(runes) {
		e.col = len(runes)
	}
	before := string(runes[:e.col])
	after := string(runes[e.col:])

	newLines := make([]string, 0, len(e.lines)+1)
	newLines = append(newLines, e.lines[:e.row]...)
	newLines = append(newLines, before)
	newLines = append(newLines, after)
	newLines = append(newLines, e.lines[e.row+1:]...)
	e.lines = newLines
	e.row++
	e.col = 0
	e.dirty = true
}

func (e *MarkdownEditor) backspace() {
	if e.col > 0 {
		runes := []rune(e.lines[e.row])
		e.lines[e.row] = string(append(runes[:e.col-1], runes[e.col:]...))
		e.col--
		e.dirty = true
	} else if e.row > 0 {
		prevLen := utf8.RuneCountInString(e.lines[e.row-1])
		e.lines[e.row-1] += e.lines[e.row]
		e.lines = append(e.lines[:e.row], e.lines[e.row+1:]...)
		e.row--
		e.col = prevLen
		e.dirty = true
	}
}

func (e *MarkdownEditor) delete() {
	runes := []rune(e.lines[e.row])
	if e.col < len(runes) {
		e.lines[e.row] = string(append(runes[:e.col], runes[e.col+1:]...))
		e.dirty = true
	} else if e.row < len(e.lines)-1 {
		// Merge next line into current
		e.lines[e.row] += e.lines[e.row+1]
		e.lines = append(e.lines[:e.row+1], e.lines[e.row+2:]...)
		e.dirty = true
	}
}

func (e *MarkdownEditor) deleteWordBackward() {
	if e.col == 0 {
		e.backspace()
		return
	}
	runes := []rune(e.lines[e.row])
	end := e.col
	i := end - 1
	for i > 0 && unicode.IsSpace(runes[i]) {
		i--
	}
	for i > 0 && !unicode.IsSpace(runes[i-1]) {
		i--
	}
	e.lines[e.row] = string(append(runes[:i], runes[end:]...))
	e.col = i
	e.dirty = true
}

func (e *MarkdownEditor) deleteWordForward() {
	runes := []rune(e.lines[e.row])
	if e.col >= len(runes) {
		e.delete()
		return
	}
	i := e.col
	for i < len(runes) && !unicode.IsSpace(runes[i]) {
		i++
	}
	for i < len(runes) && unicode.IsSpace(runes[i]) {
		i++
	}
	e.lines[e.row] = string(append(runes[:e.col], runes[i:]...))
	e.dirty = true
}

// clampCol ensures the cursor column doesn't exceed the current line length.
func (e *MarkdownEditor) clampCol() {
	lineLen := utf8.RuneCountInString(e.lines[e.row])
	if e.mode == NormalMode && lineLen > 0 {
		// In normal mode, cursor sits on characters (0 to len-1)
		if e.col >= lineLen {
			e.col = lineLen - 1
		}
	} else {
		if e.col > lineLen {
			e.col = lineLen
		}
	}
}

// visualRowCount returns the number of visual rows a logical line occupies
// given the available content width (soft-wrapping).
func (e *MarkdownEditor) visualRowCount(lineIdx int) int {
	cWidth := e.contentWidth()
	n := utf8.RuneCountInString(e.lines[lineIdx])
	if n == 0 {
		return 1
	}
	return (n + cWidth - 1) / cWidth
}

// ensureVisible adjusts scrollOffset (in visual-line space) so the cursor is visible.
func (e *MarkdownEditor) ensureVisible() {
	cWidth := e.contentWidth()

	// Compute visual row of the cursor across all logical lines.
	vRow := 0
	for i := 0; i < e.row; i++ {
		vRow += e.visualRowCount(i)
	}
	vRow += e.col / max(cWidth, 1)

	if vRow < e.scrollOffset {
		e.scrollOffset = vRow
	}
	if vRow >= e.scrollOffset+e.height {
		e.scrollOffset = vRow - e.height + 1
	}
}

// SetContent replaces all lines. If highlightDiff is true, computes changed
// lines relative to current content and populates highlightedLines.
func (e *MarkdownEditor) SetContent(content string, highlightDiff bool) {
	var newLines []string
	if content == "" {
		newLines = []string{""}
	} else {
		newLines = strings.Split(content, "\n")
	}
	if highlightDiff {
		e.highlightedLines = diffLines(e.lines, newLines)
	}
	e.lines = newLines
	if e.row >= len(e.lines) {
		e.row = len(e.lines) - 1
	}
	e.clampCol()
	e.ensureVisible()
	e.dirty = true
}

// ClearHighlights removes all agent-edit highlights.
func (e *MarkdownEditor) ClearHighlights() {
	e.highlightedLines = make(map[int]bool)
}

// diffLines returns a set of line indices in 'updated' that differ from 'old'.
func diffLines(old, updated []string) map[int]bool {
	changed := make(map[int]bool)
	maxLen := len(old)
	if len(updated) > maxLen {
		maxLen = len(updated)
	}
	for i := range maxLen {
		if i >= len(old) || i >= len(updated) || old[i] != updated[i] {
			changed[i] = true
		}
	}
	return changed
}

// Markdown syntax coloring regex patterns
var (
	mdHeading1Re   = regexp.MustCompile(`^#\s`)
	mdHeading2Re   = regexp.MustCompile(`^#{2,6}\s`)
	mdCodeFenceRe  = regexp.MustCompile("^```")
	mdBulletRe     = regexp.MustCompile(`^(\s*[-*+]\s)`)
	mdOrderedRe    = regexp.MustCompile(`^(\s*\d+\.\s)`)
	mdBlockquoteRe = regexp.MustCompile(`^>\s?`)
	mdHRuleRe      = regexp.MustCompile(`^---+\s*$`)
	mdBoldRe       = regexp.MustCompile(`\*\*(.+?)\*\*`)
	mdItalicRe     = regexp.MustCompile(`\*(.+?)\*`)
	mdInlineCodeRe = regexp.MustCompile("`([^`]+)`")
)

// renderLine applies markdown syntax coloring to a single line.
func (e *MarkdownEditor) renderLine(line string, inCodeBlock bool) string {
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	if inCodeBlock {
		if mdCodeFenceRe.MatchString(line) {
			return dimStyle.Render(line)
		}
		return dimStyle.Render(line)
	}

	if mdCodeFenceRe.MatchString(line) {
		return dimStyle.Render(line)
	}

	if mdHRuleRe.MatchString(line) {
		return dimStyle.Render(line)
	}

	if mdBlockquoteRe.MatchString(line) {
		return dimStyle.Render("│ " + mdBlockquoteRe.ReplaceAllString(line, ""))
	}

	if mdHeading1Re.MatchString(line) {
		return lipgloss.NewStyle().Bold(true).Foreground(colorBrand).Render(line)
	}
	if mdHeading2Re.MatchString(line) {
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12")).Render(line)
	}

	if loc := mdBulletRe.FindStringIndex(line); loc != nil {
		accentStyle := lipgloss.NewStyle().Foreground(colorBrand)
		return accentStyle.Render(line[:loc[1]]) + e.renderInlineStyles(line[loc[1]:])
	}
	if loc := mdOrderedRe.FindStringIndex(line); loc != nil {
		accentStyle := lipgloss.NewStyle().Foreground(colorBrand)
		return accentStyle.Render(line[:loc[1]]) + e.renderInlineStyles(line[loc[1]:])
	}

	return e.renderInlineStyles(line)
}

func (e *MarkdownEditor) renderInlineStyles(text string) string {
	codeStyle := lipgloss.NewStyle().Background(lipgloss.Color("236"))
	boldStyle := lipgloss.NewStyle().Bold(true)

	// Apply inline code first (highest priority)
	text = mdInlineCodeRe.ReplaceAllStringFunc(text, func(m string) string {
		inner := m[1 : len(m)-1]
		return codeStyle.Render(inner)
	})

	// Apply bold
	text = mdBoldRe.ReplaceAllStringFunc(text, func(m string) string {
		inner := m[2 : len(m)-2]
		return boldStyle.Render(inner)
	})

	// Apply italic (only simple cases to avoid conflicts with bold)
	text = mdItalicRe.ReplaceAllStringFunc(text, func(m string) string {
		inner := m[1 : len(m)-1]
		return lipgloss.NewStyle().Italic(true).Render(inner)
	})

	return text
}

// editorVLine represents one visual line segment of a logical line.
type editorVLine struct {
	runes     []rune
	colOffset int  // rune offset within the logical line
	logRow    int  // which logical line this belongs to
	isFirst   bool // true for the first visual line of a logical line (gets line number)
}

// View renders the editor with line numbers and soft-wrapping.
func (e MarkdownEditor) View() string {
	lnWidth := e.lineNumberWidth()
	cWidth := e.contentWidth()

	lnStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	highlightStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#00FF00")).Underline(true)

	// Build visual lines with wrapping
	var vlines []editorVLine
	cursorVI := 0

	// Track code block state for syntax coloring
	inCodeBlock := false

	for li := range e.lines {
		runes := []rune(e.lines[li])

		if len(runes) == 0 {
			if e.focused && li == e.row {
				cursorVI = len(vlines)
			}
			vlines = append(vlines, editorVLine{logRow: li, isFirst: true})
			if mdCodeFenceRe.MatchString(e.lines[li]) {
				inCodeBlock = !inCodeBlock
			}
			continue
		}

		first := true
		for start := 0; start < len(runes); start += cWidth {
			end := start + cWidth
			if end > len(runes) {
				end = len(runes)
			}
			if e.focused && li == e.row && e.col >= start && e.col < start+cWidth {
				cursorVI = len(vlines)
			}
			vlines = append(vlines, editorVLine{
				runes:     runes[start:end],
				colOffset: start,
				logRow:    li,
				isFirst:   first,
			})
			first = false
		}

		// Cursor at end of line that is exactly a multiple of cWidth
		if e.focused && li == e.row && e.col == len(runes) {
			if len(runes)%cWidth == 0 {
				cursorVI = len(vlines)
				vlines = append(vlines, editorVLine{colOffset: len(runes), logRow: li})
			} else {
				cursorVI = len(vlines) - 1
			}
		}

		if mdCodeFenceRe.MatchString(e.lines[li]) {
			inCodeBlock = !inCodeBlock
		}
	}

	// Re-track code block state for rendering visible lines
	// We need per-logical-line state, computed during the render pass.
	codeBlockState := make(map[int]bool) // logRow → inCodeBlock at that line
	inCodeBlock = false
	for i := range e.lines {
		codeBlockState[i] = inCodeBlock
		if mdCodeFenceRe.MatchString(e.lines[i]) {
			inCodeBlock = !inCodeBlock
		}
	}

	var sb strings.Builder
	endVI := min(e.scrollOffset+e.height, len(vlines))
	linesRendered := 0

	for vi := e.scrollOffset; vi < endVI; vi++ {
		if linesRendered > 0 {
			sb.WriteString("\n")
		}
		linesRendered++

		vl := vlines[vi]

		// Line number: show for first visual line of a logical line, blank for continuations
		if vl.isFirst {
			lnStr := fmt.Sprintf("%*d ", lnWidth-1, vl.logRow+1)
			sb.WriteString(lnStyle.Render(lnStr))
		} else {
			sb.WriteString(lnStyle.Render(strings.Repeat(" ", lnWidth)))
		}

		isHighlighted := e.highlightedLines[vl.logRow]

		if vi == cursorVI && e.focused {
			// Cursor visual line
			localCol := e.col - vl.colOffset
			if localCol < 0 {
				localCol = 0
			}
			if localCol > len(vl.runes) {
				localCol = len(vl.runes)
			}

			before := string(vl.runes[:localCol])
			if isHighlighted {
				before = highlightStyle.Render(before)
			}
			sb.WriteString(before)

			cursorChar := " "
			if localCol < len(vl.runes) {
				cursorChar = string(vl.runes[localCol])
			}
			e.cursor.SetChar(cursorChar)
			sb.WriteString(e.cursor.View())

			if localCol+1 < len(vl.runes) {
				after := string(vl.runes[localCol+1:])
				if isHighlighted {
					after = highlightStyle.Render(after)
				}
				sb.WriteString(after)
			}

			rendered := len(vl.runes)
			if localCol >= len(vl.runes) {
				rendered = localCol + 1
			}
			if rendered < cWidth {
				sb.WriteString(strings.Repeat(" ", cWidth-rendered))
			}
		} else {
			text := string(vl.runes)
			if isHighlighted {
				text = highlightStyle.Render(text)
			} else if vl.isFirst {
				// Only apply markdown syntax coloring on first visual line segment
				text = e.renderLine(text, codeBlockState[vl.logRow])
			}
			sb.WriteString(text)
			if pad := cWidth - len(vl.runes); pad > 0 {
				sb.WriteString(strings.Repeat(" ", pad))
			}
		}
	}

	// Pad remaining empty visible lines
	for vi := linesRendered; vi < e.height; vi++ {
		sb.WriteString("\n")
		lnStr := fmt.Sprintf("%*s ", lnWidth-1, "~")
		sb.WriteString(lnStyle.Render(lnStr))
		sb.WriteString(strings.Repeat(" ", cWidth))
	}

	return sb.String()
}
