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
	"os"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func newTestEditor(t *testing.T, content string) (*MarkdownEditor, string) {
	t.Helper()
	tmp := t.TempDir()
	f := filepath.Join(tmp, "test.md")
	if err := os.WriteFile(f, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	e := NewMarkdownEditor(80, 24)
	if err := e.Load(f); err != nil {
		t.Fatal(err)
	}
	_ = e.Focus()
	return &e, f
}

func editorSendKey(e *MarkdownEditor, key string) {
	for _, r := range key {
		msg := tea.KeyPressMsg{Code: r, Text: string(r)}
		updated, _ := e.Update(msg)
		*e = updated
	}
}

func editorSendSpecialKey(e *MarkdownEditor, code rune) {
	updated, _ := e.Update(tea.KeyPressMsg{Code: code})
	*e = updated
}

func TestMarkdownEditorLoadSave(t *testing.T) {
	e, f := newTestEditor(t, "# Hello\nWorld")

	if len(e.lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(e.lines))
	}
	if e.lines[0] != "# Hello" {
		t.Errorf("expected '# Hello', got %q", e.lines[0])
	}
	if e.Content() != "# Hello\nWorld" {
		t.Errorf("unexpected content: %q", e.Content())
	}

	// Modify and save
	e.lines[0] = "# Modified"
	e.dirty = true
	if err := e.Save(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(f)
	if string(data) != "# Modified\nWorld" {
		t.Errorf("saved content mismatch: %q", string(data))
	}
	if e.Dirty() {
		t.Error("should not be dirty after save")
	}
}

func TestMarkdownEditorNormalModeNavigation(t *testing.T) {
	e, _ := newTestEditor(t, "abc\ndef\nghi")

	// Start at 0,0
	if e.row != 0 || e.col != 0 {
		t.Fatalf("expected 0,0 got %d,%d", e.row, e.col)
	}

	// l moves right
	editorSendKey(e, "l")
	if e.col != 1 {
		t.Errorf("l: expected col 1, got %d", e.col)
	}

	// j moves down
	editorSendKey(e, "j")
	if e.row != 1 {
		t.Errorf("j: expected row 1, got %d", e.row)
	}

	// h moves left
	editorSendKey(e, "h")
	if e.col != 0 {
		t.Errorf("h: expected col 0, got %d", e.col)
	}

	// k moves up
	editorSendKey(e, "k")
	if e.row != 0 {
		t.Errorf("k: expected row 0, got %d", e.row)
	}

	// h at col 0 stays
	editorSendKey(e, "h")
	if e.col != 0 {
		t.Errorf("h at 0: expected col 0, got %d", e.col)
	}

	// k at row 0 stays
	editorSendKey(e, "k")
	if e.row != 0 {
		t.Errorf("k at 0: expected row 0, got %d", e.row)
	}
}

func TestMarkdownEditorInsertMode(t *testing.T) {
	e, _ := newTestEditor(t, "abc")

	editorSendKey(e, "i")
	if e.Mode() != InsertMode {
		t.Fatal("expected InsertMode after 'i'")
	}

	// Type 'X'
	editorSendKey(e, "X")
	if e.lines[0] != "Xabc" {
		t.Errorf("expected 'Xabc', got %q", e.lines[0])
	}

	// Esc returns to normal mode
	editorSendSpecialKey(e, tea.KeyEscape)
	if e.Mode() != NormalMode {
		t.Error("expected NormalMode after Esc")
	}
}

func TestMarkdownEditorNewline(t *testing.T) {
	e, _ := newTestEditor(t, "abcdef")

	// Move to col 3, enter insert mode, press Enter
	editorSendKey(e, "l")
	editorSendKey(e, "l")
	editorSendKey(e, "l")
	editorSendKey(e, "i")
	editorSendSpecialKey(e, tea.KeyEnter)

	if len(e.lines) != 2 {
		t.Fatalf("expected 2 lines after Enter, got %d", len(e.lines))
	}
	if e.lines[0] != "abc" {
		t.Errorf("first line should be 'abc', got %q", e.lines[0])
	}
	if e.lines[1] != "def" {
		t.Errorf("second line should be 'def', got %q", e.lines[1])
	}
}

func TestMarkdownEditorBackspace(t *testing.T) {
	e, _ := newTestEditor(t, "abc\ndef")

	// Go to start of second line in insert mode
	editorSendKey(e, "j")
	editorSendKey(e, "i")
	editorSendSpecialKey(e, tea.KeyBackspace)

	if len(e.lines) != 1 {
		t.Fatalf("expected 1 line after backspace at line start, got %d", len(e.lines))
	}
	if e.lines[0] != "abcdef" {
		t.Errorf("expected 'abcdef', got %q", e.lines[0])
	}
}

func TestMarkdownEditorDeleteLine(t *testing.T) {
	e, _ := newTestEditor(t, "line1\nline2\nline3")

	// Move to line2 and press dd
	editorSendKey(e, "j")
	editorSendKey(e, "d")
	editorSendKey(e, "d")

	if len(e.lines) != 2 {
		t.Fatalf("expected 2 lines after dd, got %d", len(e.lines))
	}
	if e.lines[0] != "line1" || e.lines[1] != "line3" {
		t.Errorf("expected line1,line3 got %q,%q", e.lines[0], e.lines[1])
	}
}

func TestMarkdownEditorUndo(t *testing.T) {
	e, _ := newTestEditor(t, "original")

	// Enter insert mode, type something
	editorSendKey(e, "i")
	editorSendKey(e, "X")

	if e.lines[0] != "Xoriginal" {
		t.Fatalf("expected 'Xoriginal', got %q", e.lines[0])
	}

	// Esc to normal, then undo
	editorSendSpecialKey(e, tea.KeyEscape)
	editorSendKey(e, "u")

	if e.lines[0] != "original" {
		t.Errorf("undo should restore 'original', got %q", e.lines[0])
	}
}

func TestMarkdownEditorWordMotion(t *testing.T) {
	e, _ := newTestEditor(t, "hello world foo")

	// w jumps to next word
	editorSendKey(e, "w")
	if e.col != 6 {
		t.Errorf("w: expected col 6, got %d", e.col)
	}

	// b jumps back
	editorSendKey(e, "b")
	if e.col != 0 {
		t.Errorf("b: expected col 0, got %d", e.col)
	}
}

func TestMarkdownEditorGG(t *testing.T) {
	e, _ := newTestEditor(t, "line1\nline2\nline3\nline4\nline5")

	// G goes to bottom
	editorSendKey(e, "G")
	if e.row != 4 {
		t.Errorf("G: expected row 4, got %d", e.row)
	}

	// gg goes to top
	editorSendKey(e, "g")
	editorSendKey(e, "g")
	if e.row != 0 {
		t.Errorf("gg: expected row 0, got %d", e.row)
	}
}

func TestMarkdownEditorLineMotions(t *testing.T) {
	e, _ := newTestEditor(t, "hello world")

	// $ goes to end of line
	editorSendKey(e, "$")
	if e.col != 10 { // "hello world" has 11 chars, last index is 10
		t.Errorf("$: expected col 10, got %d", e.col)
	}

	// 0 goes to start of line
	editorSendKey(e, "0")
	if e.col != 0 {
		t.Errorf("0: expected col 0, got %d", e.col)
	}
}

func TestMarkdownEditorOpenLine(t *testing.T) {
	e, _ := newTestEditor(t, "line1\nline2")

	// o opens line below
	editorSendKey(e, "o")
	if len(e.lines) != 3 {
		t.Fatalf("o: expected 3 lines, got %d", len(e.lines))
	}
	if e.row != 1 {
		t.Errorf("o: expected row 1, got %d", e.row)
	}
	if e.Mode() != InsertMode {
		t.Error("o: should be in insert mode")
	}
	if e.lines[1] != "" {
		t.Errorf("o: new line should be empty, got %q", e.lines[1])
	}
}

func TestMarkdownEditorOpenLineAbove(t *testing.T) {
	e, _ := newTestEditor(t, "line1\nline2")

	// Move to line2, then O opens line above
	editorSendKey(e, "j")
	editorSendKey(e, "O")
	if len(e.lines) != 3 {
		t.Fatalf("O: expected 3 lines, got %d", len(e.lines))
	}
	if e.row != 1 {
		t.Errorf("O: expected row 1, got %d", e.row)
	}
	if e.Mode() != InsertMode {
		t.Error("O: should be in insert mode")
	}
}

func TestMarkdownEditorSetContentWithDiff(t *testing.T) {
	e := NewMarkdownEditor(80, 24)
	e.lines = []string{"line1", "line2", "line3"}

	e.SetContent("line1\nMODIFIED\nline3\nline4", true)

	if len(e.lines) != 4 {
		t.Fatalf("expected 4 lines, got %d", len(e.lines))
	}
	if !e.highlightedLines[1] {
		t.Error("line 1 should be highlighted (changed)")
	}
	if !e.highlightedLines[3] {
		t.Error("line 3 should be highlighted (added)")
	}
	if e.highlightedLines[0] {
		t.Error("line 0 should not be highlighted (unchanged)")
	}
	if e.highlightedLines[2] {
		t.Error("line 2 should not be highlighted (unchanged)")
	}
}

func TestMarkdownEditorMarkClean(t *testing.T) {
	e := NewMarkdownEditor(80, 24)
	e.lines = []string{"line1", "line2"}

	// SetContent marks dirty
	e.SetContent("line1\nMODIFIED", true)
	if !e.Dirty() {
		t.Fatal("expected editor to be dirty after SetContent")
	}

	// MarkClean resets the dirty flag
	e.MarkClean()
	if e.Dirty() {
		t.Fatal("expected editor to be clean after MarkClean")
	}
}

func TestDiffLines(t *testing.T) {
	tests := []struct {
		name     string
		old      []string
		updated  []string
		expected map[int]bool
	}{
		{
			name:     "identical",
			old:      []string{"a", "b", "c"},
			updated:  []string{"a", "b", "c"},
			expected: map[int]bool{},
		},
		{
			name:     "modified line",
			old:      []string{"a", "b", "c"},
			updated:  []string{"a", "X", "c"},
			expected: map[int]bool{1: true},
		},
		{
			name:     "added lines",
			old:      []string{"a", "b"},
			updated:  []string{"a", "b", "c", "d"},
			expected: map[int]bool{2: true, 3: true},
		},
		{
			name:     "removed lines",
			old:      []string{"a", "b", "c"},
			updated:  []string{"a"},
			expected: map[int]bool{1: true, 2: true},
		},
		{
			name:     "all different",
			old:      []string{"a", "b"},
			updated:  []string{"x", "y"},
			expected: map[int]bool{0: true, 1: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := diffLines(tt.old, tt.updated)
			if len(result) != len(tt.expected) {
				t.Errorf("expected %d changed lines, got %d", len(tt.expected), len(result))
			}
			for idx := range tt.expected {
				if !result[idx] {
					t.Errorf("line %d should be marked as changed", idx)
				}
			}
		})
	}
}

func TestMarkdownEditorClearHighlights(t *testing.T) {
	e := NewMarkdownEditor(80, 24)
	e.lines = []string{"a", "b", "c"}
	e.highlightedLines = map[int]bool{0: true, 2: true}

	e.ClearHighlights()

	if len(e.highlightedLines) != 0 {
		t.Error("highlights should be empty after ClearHighlights")
	}
}

func TestMarkdownEditorPendingKeyCancel(t *testing.T) {
	e, _ := newTestEditor(t, "line1\nline2")

	// Press 'g' then 'j' — should not go to top, should move down
	editorSendKey(e, "g")
	if e.pendingKey != 'g' {
		t.Fatal("pending key should be 'g'")
	}
	editorSendKey(e, "j")
	if e.row != 1 {
		t.Errorf("expected row 1 after g+j, got %d", e.row)
	}
	if e.pendingKey != 0 {
		t.Error("pending key should be cleared")
	}
}

func TestMarkdownEditorUndoStackCap(t *testing.T) {
	e, _ := newTestEditor(t, "test")

	// Push more than maxUndoEntries
	for i := range maxUndoEntries + 10 {
		e.pushUndo()
		e.lines[0] = string(rune('A' + i%26))
	}

	if len(e.undoStack) != maxUndoEntries {
		t.Errorf("undo stack should be capped at %d, got %d", maxUndoEntries, len(e.undoStack))
	}
}
