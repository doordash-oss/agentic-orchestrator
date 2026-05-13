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

	tea "charm.land/bubbletea/v2"
)

// runeMsg is a helper to create a KeyPressMsg for rune input.
func runeMsg(r rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: r, Text: string(r)}
}

// sendRunes sends each rune in s to the textarea.
func sendRunes(t *SimpleTextarea, s string) {
	t.focused = true
	for _, r := range s {
		*t, _ = t.Update(runeMsg(r))
	}
}

// sendKey sends a special key to the textarea.
func sendKey(t *SimpleTextarea, kt rune) {
	t.focused = true
	*t, _ = t.Update(tea.KeyPressMsg{Code: kt})
}

func newFocused() SimpleTextarea {
	ta := NewSimpleTextarea()
	ta.SetHeight(4)
	ta.MaxHeight = 8
	ta.CharLimit = 200
	ta.SetWidth(40)
	ta.focused = true
	return ta
}

func TestSimpleTextarea_CharInsertion(t *testing.T) {
	t.Parallel()
	ta := newFocused()
	sendRunes(&ta, "hello")
	if got := ta.Value(); got != "hello" {
		t.Errorf("Value() = %q, want %q", got, "hello")
	}
	if ta.col != 5 {
		t.Errorf("col = %d, want 5", ta.col)
	}
}

func TestSimpleTextarea_EnterSplitsLine(t *testing.T) {
	t.Parallel()
	ta := newFocused()
	sendRunes(&ta, "ab")
	sendKey(&ta, tea.KeyEnter)
	sendRunes(&ta, "cd")
	if got := ta.Value(); got != "ab\ncd" {
		t.Errorf("Value() = %q, want %q", got, "ab\\ncd")
	}
	if ta.row != 1 {
		t.Errorf("row = %d, want 1", ta.row)
	}
}

func TestSimpleTextarea_BackspaceMergesLines(t *testing.T) {
	t.Parallel()
	ta := newFocused()
	ta.SetValue("abc\ndef")
	// cursor is at end of "def" (row=1, col=3)
	sendKey(&ta, tea.KeyHome)
	// cursor at start of row 1
	sendKey(&ta, tea.KeyBackspace)
	if got := ta.Value(); got != "abcdef" {
		t.Errorf("after merge Value() = %q, want %q", got, "abcdef")
	}
	if ta.row != 0 || ta.col != 3 {
		t.Errorf("cursor = (%d,%d), want (0,3)", ta.row, ta.col)
	}
}

func TestSimpleTextarea_LeftRightWrap(t *testing.T) {
	t.Parallel()
	ta := newFocused()
	ta.SetValue("ab\ncd")
	// cursor at end of row 1
	sendKey(&ta, tea.KeyLeft)
	sendKey(&ta, tea.KeyLeft)
	// now at col=0, row=1 — one more left should wrap to row 0 end
	sendKey(&ta, tea.KeyLeft)
	if ta.row != 0 || ta.col != 2 {
		t.Errorf("after wrap-left cursor = (%d,%d), want (0,2)", ta.row, ta.col)
	}
	// Now go right past end of row 0 to wrap to row 1
	sendKey(&ta, tea.KeyRight)
	if ta.row != 1 || ta.col != 0 {
		t.Errorf("after wrap-right cursor = (%d,%d), want (1,0)", ta.row, ta.col)
	}
}

func TestSimpleTextarea_UpDownClamp(t *testing.T) {
	t.Parallel()
	ta := newFocused()
	ta.SetValue("short\nverylongline")
	// cursor at end of row 1 (col=12)
	sendKey(&ta, tea.KeyUp)
	// row 0 is "short" (len=5), col should clamp to 5
	if ta.row != 0 {
		t.Errorf("row = %d, want 0", ta.row)
	}
	if ta.col != 5 {
		t.Errorf("col = %d, want 5 (clamped)", ta.col)
	}
}

func TestSimpleTextarea_MaxHeightEnforced(t *testing.T) {
	t.Parallel()
	ta := newFocused()
	ta.MaxHeight = 3
	ta.SetValue("a\nb")
	// Adding one more line is allowed (we now have 2 lines, max 3)
	sendKey(&ta, tea.KeyEnd)
	sendKey(&ta, tea.KeyEnter)
	if ta.LineCount() != 3 {
		t.Errorf("LineCount() = %d, want 3", ta.LineCount())
	}
	// Adding a 4th line should be blocked
	sendKey(&ta, tea.KeyEnter)
	if ta.LineCount() != 3 {
		t.Errorf("LineCount() should remain 3, got %d", ta.LineCount())
	}
}

func TestSimpleTextarea_CharLimitEnforced(t *testing.T) {
	t.Parallel()
	ta := newFocused()
	ta.CharLimit = 5
	sendRunes(&ta, "hello")
	// 6th char should be rejected
	sendRunes(&ta, "X")
	if got := ta.Value(); got != "hello" {
		t.Errorf("Value() = %q, want %q", got, "hello")
	}
}

func TestSimpleTextarea_SetValueRoundtrip(t *testing.T) {
	t.Parallel()
	ta := newFocused()
	input := "line one\nline two\nline three"
	ta.SetValue(input)
	if got := ta.Value(); got != input {
		t.Errorf("SetValue/Value roundtrip failed: got %q, want %q", got, input)
	}
	// Cursor should be at end
	if ta.row != 2 || ta.col != len("line three") {
		t.Errorf("cursor = (%d,%d), want (2,10)", ta.row, ta.col)
	}
}

func TestSimpleTextarea_Reset(t *testing.T) {
	t.Parallel()
	ta := newFocused()
	ta.SetValue("hello\nworld")
	ta.Reset()
	if got := ta.Value(); got != "" {
		t.Errorf("after Reset, Value() = %q, want empty", got)
	}
	if ta.row != 0 || ta.col != 0 {
		t.Errorf("after Reset, cursor = (%d,%d), want (0,0)", ta.row, ta.col)
	}
}

func TestSimpleTextarea_ViewportScrolling(t *testing.T) {
	t.Parallel()
	ta := newFocused()
	ta.SetHeight(2)
	ta.MaxHeight = 10
	ta.SetValue("a\nb\nc\nd")
	// cursor is at row 3 (last line), scrollOffset should have scrolled
	if ta.scrollOffset > ta.row-ta.height+1 {
		t.Errorf("scrollOffset = %d, expected <= %d", ta.scrollOffset, ta.row-ta.height+1)
	}
	// View should contain exactly 2 lines
	view := ta.View()
	lines := strings.Split(view, "\n")
	if len(lines) != 2 {
		t.Errorf("View() has %d lines, want 2; view=%q", len(lines), view)
	}
}

func TestSimpleTextarea_FocusBlurPlaceholder(t *testing.T) {
	t.Parallel()
	ta := NewSimpleTextarea()
	ta.Placeholder = "Type here..."
	ta.SetWidth(20)
	ta.SetHeight(2)

	// Blurred + empty → show placeholder
	view := ta.View()
	if !strings.Contains(view, "Type here...") {
		t.Errorf("expected placeholder in blurred view, got %q", view)
	}

	// Focused → no placeholder
	ta.focused = true
	view = ta.View()
	if strings.Contains(view, "Type here...") {
		t.Errorf("did not expect placeholder in focused view, got %q", view)
	}
}

func TestSimpleTextarea_HomeEnd(t *testing.T) {
	t.Parallel()
	ta := newFocused()
	sendRunes(&ta, "hello")
	sendKey(&ta, tea.KeyHome)
	if ta.col != 0 {
		t.Errorf("after Home, col = %d, want 0", ta.col)
	}
	sendKey(&ta, tea.KeyEnd)
	if ta.col != 5 {
		t.Errorf("after End, col = %d, want 5", ta.col)
	}
}

func TestSimpleTextarea_InsertString(t *testing.T) {
	t.Parallel()
	ta := newFocused()
	ta.InsertString("foo\nbar")
	if got := ta.Value(); got != "foo\nbar" {
		t.Errorf("InsertString: Value() = %q, want %q", got, "foo\\nbar")
	}
}

func TestSimpleTextarea_PasteNewlines(t *testing.T) {
	t.Parallel()
	ta := newFocused()
	// Simulate bracketed paste: newlines arrive as KeyPressMsg, not KeyEnter
	for _, r := range "hello" {
		ta, _ = ta.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	ta, _ = ta.Update(tea.KeyPressMsg{Code: '\n', Text: "\n"})
	for _, r := range "world" {
		ta, _ = ta.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	if got := ta.Value(); got != "hello\nworld" {
		t.Errorf("paste with newline: Value() = %q, want %q", got, "hello\\nworld")
	}
	if ta.row != 1 {
		t.Errorf("row = %d, want 1", ta.row)
	}
}

func TestSimpleTextarea_ControlCharsDropped(t *testing.T) {
	t.Parallel()
	ta := newFocused()
	for _, r := range "ab" {
		ta, _ = ta.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	ta, _ = ta.Update(tea.KeyPressMsg{Code: '\t', Text: "\t"})
	ta, _ = ta.Update(tea.KeyPressMsg{Code: 0x01, Text: string(rune(0x01))})
	for _, r := range "cd" {
		ta, _ = ta.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	if got := ta.Value(); got != "abcd" {
		t.Errorf("control chars: Value() = %q, want %q", got, "abcd")
	}
}
