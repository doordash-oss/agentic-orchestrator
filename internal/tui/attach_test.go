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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// testAttachModel creates an AttachModel with properly initialized viewport and
// textarea fields, suitable for tests that call switchToTab or updateViewport.
func testAttachModel(sess session.SessionView, width, height int, tabs []repoTab, activeIdx int) AttachModel {
	vp := viewport.New(viewport.WithWidth(width), viewport.WithHeight(height-6))
	vp.Style = lipgloss.NewStyle()

	ti := textarea.New()
	ti.SetWidth(width)
	ti.SetHeight(minInputLines)
	ti.ShowLineNumbers = false
	ti.Focus()

	return AttachModel{
		viewport:     vp,
		input:        ti,
		sess:         sess,
		width:        width,
		height:       height,
		inputHeight:  minInputLines,
		repoTabs:     tabs,
		activeTabIdx: activeIdx,
	}
}

func TestNewAttachModel(t *testing.T) {
	sess := session.NewSession("test-attach", "feat-1", 0)
	m := attachModelFromSession(sess, 80, 24)

	if m.sess != sess {
		t.Error("session not stored correctly")
	}
	if m.done {
		t.Error("should not be done initially")
	}
	if m.detached {
		t.Error("should not be detached initially")
	}
	if m.width != 80 || m.height != 24 {
		t.Errorf("dimensions = %dx%d, want 80x24", m.width, m.height)
	}
}

func TestAttachModelInputFocused(t *testing.T) {
	sess := session.NewSession("test-focus", "feat-1", 0)
	m := attachModelFromSession(sess, 80, 24)

	if !m.input.Focused() {
		t.Error("text input should be focused on creation")
	}
}

func TestAttachModelKeyInputAndCtrlSSend(t *testing.T) {
	sess := session.NewSession("test-key", "feat-1", 0)
	m := attachModelFromSession(sess, 80, 24)

	// Type some characters
	for _, ch := range "hello" {
		km := tea.KeyPressMsg{Code: ch, Text: string(ch)}
		m, _ = m.Update(km)
	}
	if m.input.Value() != "hello" {
		t.Errorf("input value = %q, want %q", m.input.Value(), "hello")
	}

	// Press Ctrl+S — should reset input and produce a cmd
	ctrlSKey := tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl}
	var cmd tea.Cmd
	m, cmd = m.Update(ctrlSKey)
	if m.input.Value() != "" {
		t.Errorf("input should be cleared after Ctrl+S, got %q", m.input.Value())
	}
	if cmd == nil {
		t.Error("Ctrl+S with text should produce a command (to send message)")
	}
}

func TestAttachModelView(t *testing.T) {
	sess := session.NewSession("test-view", "feat-1", 0)
	m := attachModelFromSession(sess, 80, 24)

	view := m.View()
	if view == "" {
		t.Error("view should not be empty")
	}
}

func TestAttachModelViewUsesWatchVocabulary(t *testing.T) {
	sess := session.NewSession("test-view-watch", "feat-1", feature.PhaseImplement)
	m := attachModelFromSession(sess, 80, 24)

	view := stripANSI(m.View())
	for _, want := range []string{"Watch", "Stop watching"} {
		if !strings.Contains(view, want) {
			t.Errorf("AttachModel.View() missing %q in:\n%s", want, view)
		}
	}
	for _, retired := range []string{"Detach", "Attach View"} {
		if strings.Contains(view, retired) {
			t.Errorf("AttachModel.View() contains retired copy %q in:\n%s", retired, view)
		}
	}
}

func TestAttachModelRenderViewportContent_ExcludesSpinnerLine(t *testing.T) {
	sess := session.NewSession("test-spinner", "feat-1", 0)
	m := attachModelFromSession(sess, 80, 24)
	m.thinkingLine = "Using Bash..."
	m.spinnerView = "⠋"

	content := m.renderViewportContent(nil)
	if strings.Contains(content, "Using Bash...") {
		t.Fatalf("viewport content should not include spinner line, got %q", content)
	}

	view := m.View()
	if !strings.Contains(view, "Using Bash...") {
		t.Fatalf("full view should render spinner line outside the viewport content, got %q", view)
	}
}

func TestAttachModelRenderSpinnerLine_ContainedToSingleLine(t *testing.T) {
	sess := session.NewSession("test-spinner-single-line", "feat-1", 0)
	m := attachModelFromSession(sess, 40, 24)
	m.spinnerView = "⠋"
	m.thinkingLine = "Bash:\ncontinue\n}\nfor _, block := range msg.Assistant.Message.Content {"

	line := m.renderSpinnerLine()
	if strings.Contains(line, "\n") {
		t.Fatalf("spinner line should be single-line, got %q", line)
	}
	if w := lipgloss.Width(line); w > m.viewport.Width() {
		t.Fatalf("spinner width = %d, want <= %d", w, m.viewport.Width())
	}
}

// While a turn is active, the spinner must stay animated even when the
// last-activity timestamp is arbitrarily old — silence on stdout during a
// long Bash tool or long API roundtrip is not evidence of a stall.
func TestRenderSpinnerLine_TurnActiveBypassesIdle(t *testing.T) {
	sess := session.NewSession("test-spinner-turn-active", "feat-1", 0)
	m := attachModelFromSession(sess, 80, 24)
	m.spinnerView = "⠋"
	m.thinkingLine = "Using Bash..."
	m.turnActive = true
	m.lastActivityAt = time.Now().Add(-10 * time.Minute)

	line := m.renderSpinnerLine()
	if !strings.Contains(line, "⠋") {
		t.Fatalf("expected animated spinner frame while turn active, got %q", line)
	}
	if strings.Contains(line, "no activity") || strings.Contains(line, "idle ") {
		t.Fatalf("turn-active spinner must not show idle/dead badge, got %q", line)
	}
}

// Ctrl+S invokes sendCurrentMessage; that should immediately flag the turn
// active and bootstrap a visible "Thinking..." so the user sees progress
// without waiting for the first stream event.
func TestSendCurrentMessageSetsTurnActive(t *testing.T) {
	sess := mocks.NewMockSessionView("s-send", "feat-1")
	m := testAttachModel(sess, 80, 24, nil, 0)

	for _, ch := range "hi" {
		km := tea.KeyPressMsg{Code: ch, Text: string(ch)}
		m, _ = m.Update(km)
	}
	ctrlS := tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl}
	m, _ = m.Update(ctrlS)

	if !m.turnActive {
		t.Fatal("turnActive should be true immediately after sending a message")
	}
	if m.thinkingLine == "" {
		t.Fatal("thinkingLine should be seeded after sending a message")
	}
	if m.lastActivityAt.IsZero() {
		t.Fatal("lastActivityAt should be refreshed after sending a message")
	}
}

// A Result on the attach channel ends the turn: turnActive and thinkingLine
// both clear, unblocking the "awaiting user input" UI state.
func TestResultClearsTurnActive(t *testing.T) {
	sess := session.NewSession("s-result", "feat-1", 0)
	m := attachModelFromSession(sess, 80, 24)
	m.turnActive = true
	m.thinkingLine = "Using Bash..."

	resultMsg := attachMsgsMsg{generation: m.tabGeneration, messages: []llm.SDKMessage{
		{Type: "result", Result: &llm.ResultMessage{Subtype: "success"}},
	}}
	am, _ := m.Update(resultMsg)

	if am.turnActive {
		t.Error("turnActive should be false after Result")
	}
	if am.thinkingLine != "" {
		t.Errorf("thinkingLine should be cleared after Result, got %q", am.thinkingLine)
	}
}

// When the session closes (process exit → attachDoneMsg), we must not leave
// turnActive stuck true.
func TestAttachDoneClearsTurnActive(t *testing.T) {
	sess := session.NewSession("s-done", "feat-1", 0)
	m := attachModelFromSession(sess, 80, 24)
	m.turnActive = true
	m.thinkingLine = "Thinking..."

	am, _ := m.Update(attachDoneMsg{generation: m.tabGeneration})

	if am.turnActive {
		t.Error("turnActive should be false after attachDoneMsg")
	}
	if am.thinkingLine != "" {
		t.Errorf("thinkingLine should be cleared after attachDoneMsg, got %q", am.thinkingLine)
	}
	if !am.done {
		t.Error("done should be true after attachDoneMsg")
	}
}

func TestRenderAttachTranscript_InterleavesFileEvents(t *testing.T) {
	msgs := []llm.SDKMessage{
		{
			Assistant: &llm.AssistantMessage{
				Message: llm.ConversationMsg{Content: []llm.ContentBlock{{Type: "text", Text: "Now let me update the tests."}}},
			},
		},
		{
			Assistant: &llm.AssistantMessage{
				Message: llm.ConversationMsg{Content: []llm.ContentBlock{{Type: "text", Text: "Next I'll update the integration test."}}},
			},
		},
	}
	events := []attachFileEvent{
		{
			afterMessageCount: 1,
			change: attachFileChange{
				Path:      "internal/foo_test.go",
				Operation: "update",
				Detail:    "Captured from tool usage.",
			},
		},
	}

	rendered := renderAttachTranscript(msgs, events, 0, filterAll, 120, nil)
	// Glamour splits assistant text across ANSI color runs, so the raw
	// rendered string doesn't contain the source text as contiguous bytes.
	// Strip ANSI before ordering assertions on assistant text.
	cleanRendered := ansiRegex.ReplaceAllString(rendered, "")
	firstMsg := strings.Index(cleanRendered, "Now let me update the tests.")
	diffMsg := strings.Index(cleanRendered, "Update(internal/foo_test.go)")
	secondMsg := strings.Index(cleanRendered, "Next I'll update the integration test.")
	if firstMsg < 0 || diffMsg < 0 || secondMsg < 0 {
		t.Fatalf("rendered transcript missing expected sections: %q", rendered)
	}
	if !(firstMsg < diffMsg && diffMsg < secondMsg) {
		t.Fatalf("expected diff event to be interleaved between assistant messages, got %q", rendered)
	}
	lines := strings.Split(cleanRendered, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " ")
	}
	normalized := strings.Join(lines, "\n")
	if !strings.Contains(normalized, "\n╭") {
		t.Fatalf("expected diff event to be rendered in a bordered box, got %q", rendered)
	}
	if !strings.Contains(normalized, "tests.\n\n╭") {
		t.Fatalf("expected a blank line before the diff box, got %q", rendered)
	}
	if !strings.Contains(normalized, "╯\n\n  Next I'll update the integration test.") {
		t.Fatalf("expected a blank line after the diff box, got %q", rendered)
	}
}

func TestRenderAttachTranscript_UsesBaseMessageOffset(t *testing.T) {
	msgs := []llm.SDKMessage{
		{
			Assistant: &llm.AssistantMessage{
				Message: llm.ConversationMsg{Content: []llm.ContentBlock{{Type: "text", Text: "visible one"}}},
			},
		},
		{
			Assistant: &llm.AssistantMessage{
				Message: llm.ConversationMsg{Content: []llm.ContentBlock{{Type: "text", Text: "visible two"}}},
			},
		},
	}
	events := []attachFileEvent{
		{
			afterMessageCount: 1001,
			change: attachFileChange{
				Path:      "internal/foo.go",
				Operation: "update",
			},
		},
	}

	rendered := renderAttachTranscript(msgs, events, 1000, filterAll, 120, nil)
	cleanRendered := ansiRegex.ReplaceAllString(rendered, "")
	firstMsg := strings.Index(cleanRendered, "visible one")
	diffMsg := strings.Index(cleanRendered, "Update(internal/foo.go)")
	secondMsg := strings.Index(cleanRendered, "visible two")
	if firstMsg < 0 || diffMsg < 0 || secondMsg < 0 {
		t.Fatalf("rendered transcript missing expected sections: %q", rendered)
	}
	if !(firstMsg < diffMsg && diffMsg < secondMsg) {
		t.Fatalf("expected diff event to be interleaved between visible messages, got %q", rendered)
	}
}

func TestAttachModelUpdateViewport_FileEventsRespectScrollPosition(t *testing.T) {
	mv := mocks.NewMockSessionView("sess-1", "feat-1")
	for i := 0; i < 40; i++ {
		mv.MessageLogVal.Append(llm.SDKMessage{
			Assistant: &llm.AssistantMessage{
				Message: llm.ConversationMsg{Content: []llm.ContentBlock{{Type: "text", Text: fmt.Sprintf("line %02d", i)}}},
			},
		})
	}

	m := attachModelFromSession(mv, 80, 12)
	m.updateViewport()
	m.viewport.SetYOffset(0)
	before := m.viewport.YOffset()
	mv.MessageLogVal.Append(llm.SDKMessage{
		Assistant: &llm.AssistantMessage{
			Message: llm.ConversationMsg{Content: []llm.ContentBlock{{
				Type:  "tool_use",
				Name:  "Edit",
				Input: json.RawMessage(`{"file_path":"file.go"}`),
			}}},
		},
	})

	m.updateViewport()
	if m.viewport.YOffset() != before {
		t.Fatalf("YOffset = %d, want %d when user is scrolled up", m.viewport.YOffset(), before)
	}
}

func TestBuildAttachFileEvents_PrunesOldEvents(t *testing.T) {
	msgs := make([]llm.SDKMessage, 0, attachViewportFileLimit+50)
	for i := 0; i < attachViewportFileLimit+50; i++ {
		msgs = append(msgs, llm.SDKMessage{
			Assistant: &llm.AssistantMessage{
				Message: llm.ConversationMsg{Content: []llm.ContentBlock{{
					Type:  "tool_use",
					Name:  "Edit",
					Input: json.RawMessage(fmt.Sprintf(`{"file_path":"file-%03d.go"}`, i)),
				}}},
			},
		})
	}

	events := buildAttachFileEvents(msgs, 0)
	if got := len(events); got != attachViewportFileLimit {
		t.Fatalf("len(events) = %d, want %d", got, attachViewportFileLimit)
	}
	if events[0].change.Path != "file-050.go" {
		t.Fatalf("oldest retained event = %q, want file-050.go", events[0].change.Path)
	}
}

func TestBuildAttachFileEvents_ReconstructsFromToolHistory(t *testing.T) {
	msgs := []llm.SDKMessage{
		{
			Assistant: &llm.AssistantMessage{
				Message: llm.ConversationMsg{Content: []llm.ContentBlock{
					{Type: "text", Text: "Updating the file now."},
					{Type: "tool_use", Name: "Edit", Input: json.RawMessage(`{"file_path":"internal/foo.go"}`)},
				}},
			},
		},
		{
			ToolProgress: &llm.ToolProgressMessage{
				ToolName: "Write",
				Data:     "Updated internal/foo.go",
			},
		},
	}

	events := buildAttachFileEvents(msgs, 100)
	if len(events) != 2 {
		t.Fatalf("len(events) = %d, want 2", len(events))
	}
	if events[0].afterMessageCount != 101 {
		t.Fatalf("first event anchor = %d, want 101", events[0].afterMessageCount)
	}
	if events[0].change.Path != "internal/foo.go" {
		t.Fatalf("first event path = %q, want internal/foo.go", events[0].change.Path)
	}
	if events[1].afterMessageCount != 102 {
		t.Fatalf("second event anchor = %d, want 102", events[1].afterMessageCount)
	}
	if events[1].change.Detail == "" {
		t.Fatal("expected progress-derived event detail")
	}
}

func TestFileChangesFromToolProgress_PreservesMultilineDetail(t *testing.T) {
	changes := fileChangesFromToolProgress(llm.ToolProgressMessage{
		ToolName: "Write",
		Data:     "Success. Updated the following files:\nM internal/tui/dashboard.go\n-const Version = \"0.117.2\"\n+const Version = \"0.117.3\"",
	})

	if len(changes) != 1 {
		t.Fatalf("len(changes) = %d, want 1", len(changes))
	}
	if !strings.Contains(changes[0].Detail, "\n-const Version") {
		t.Fatalf("expected multiline diff detail, got %q", changes[0].Detail)
	}
	if !strings.Contains(changes[0].Detail, "\n+const Version") {
		t.Fatalf("expected added line in detail, got %q", changes[0].Detail)
	}
}

func TestFileChangesFromToolUse_EditIncludesReplacementDiff(t *testing.T) {
	block := llm.ContentBlock{
		Type: "tool_use",
		Name: "Edit",
		Input: json.RawMessage(`{
			"file_path":"internal/tui/dashboard.go",
			"old_string":"const Version = \"0.117.2\"",
			"new_string":"const Version = \"0.117.3\""
		}`),
	}

	changes := fileChangesFromToolUse(block)
	if len(changes) != 1 {
		t.Fatalf("len(changes) = %d, want 1", len(changes))
	}
	if !strings.Contains(changes[0].Detail, "- const Version = \"0.117.2\"") {
		t.Fatalf("expected removed line in detail, got %q", changes[0].Detail)
	}
	if !strings.Contains(changes[0].Detail, "+ const Version = \"0.117.3\"") {
		t.Fatalf("expected added line in detail, got %q", changes[0].Detail)
	}
}

func TestRenderFileEvent_RendersMultilineDetail(t *testing.T) {
	rendered := renderFileEvent(attachFileChange{
		Path:      "internal/tui/dashboard.go",
		Operation: "update",
		Detail:    "Success. Updated the following files:\n- const Version = \"0.117.2\"\n+ const Version = \"0.117.3\"",
	}, 120)

	if !strings.Contains(rendered, "Success. Updated the following files:") {
		t.Fatalf("expected summary line in rendered file event, got %q", rendered)
	}
	if !strings.Contains(rendered, "- const Version = \"0.117.2\"") {
		t.Fatalf("expected removed diff line in rendered file event, got %q", rendered)
	}
	if !strings.Contains(rendered, "+ const Version = \"0.117.3\"") {
		t.Fatalf("expected added diff line in rendered file event, got %q", rendered)
	}
}

func TestRenderFileEventDiff_ColorsAndLineNumbers(t *testing.T) {
	patch := `--- a/file.go
+++ b/file.go
@@ -10,6 +10,8 @@ func main() {
 	fmt.Println("hello")
 	fmt.Println("world")
+	fmt.Println("added1")
+	fmt.Println("added2")
 	return
-	// old comment
 }`

	rendered := renderFileEventDiff(patch, 120)
	clean := ansiRegex.ReplaceAllString(rendered, "")
	if !strings.Contains(clean, "12 +") {
		t.Errorf("expected new line number 12, got %q", clean)
	}
	if !strings.Contains(clean, "added1") {
		t.Error("expected added line content")
	}
	if !strings.Contains(clean, "old comment") {
		t.Error("expected removed line content")
	}
	// Verify colors are applied (ANSI escapes present in raw output).
	if !strings.Contains(rendered, "\x1b[") {
		t.Error("expected ANSI color codes in rendered output")
	}
}

func TestRenderFileEventDiff_SkipsHeaders(t *testing.T) {
	patch := `--- a/file.go
+++ b/file.go
@@ -1,3 +1,3 @@
 line1
-old
+new
 line3`

	rendered := renderFileEventDiff(patch, 80)
	clean := ansiRegex.ReplaceAllString(rendered, "")
	if strings.Contains(clean, "--- a/file.go") {
		t.Error("should skip --- header line")
	}
	if strings.Contains(clean, "+++ b/file.go") {
		t.Error("should skip +++ header line")
	}
	if !strings.Contains(clean, "@@") {
		t.Error("should include hunk header")
	}
}

func TestRenderFileEvent_WithDiffPatch(t *testing.T) {
	rendered := renderFileEvent(attachFileChange{
		Path:         "internal/tui/dashboard.go",
		Operation:    "update",
		HasDiffPatch: true,
		AddedLines:   2,
		RemovedLines: 1,
		Detail: `@@ -1,3 +1,4 @@
 line1
-old
+new1
+new2
 line3`,
	}, 120)

	clean := ansiRegex.ReplaceAllString(rendered, "")
	if !strings.Contains(clean, "Added 2") {
		t.Error("expected 'Added 2' in summary")
	}
	if !strings.Contains(clean, "removed 1") {
		t.Error("expected 'removed 1' in summary")
	}
	if !strings.Contains(clean, "new1") {
		t.Error("expected diff content")
	}
}

func TestRenderFileEventDetail_ColorsFallback(t *testing.T) {
	rendered := renderFileEventDetail("- old line\n+ new line\ncontext")
	clean := ansiRegex.ReplaceAllString(rendered, "")
	if !strings.Contains(clean, "- old line") {
		t.Error("expected removed line in output")
	}
	if !strings.Contains(clean, "+ new line") {
		t.Error("expected added line in output")
	}
	// Should have ANSI codes for coloring.
	if !strings.Contains(rendered, "\x1b[") {
		t.Error("expected ANSI color codes")
	}
}

func TestMakeRelativePath(t *testing.T) {
	tests := []struct {
		name     string
		workDir  string
		filePath string
		want     string
	}{
		{"absolute_match", "/home/user/project", "/home/user/project/src/file.go", "src/file.go"},
		{"already_relative", "/home/user/project", "src/file.go", "src/file.go"},
		{"no_match", "/home/user/project", "/other/path/file.go", "/other/path/file.go"},
		{"empty_workdir", "", "/home/user/project/file.go", "/home/user/project/file.go"},
		{"empty_path", "/home/user/project", "", ""},
		{"trailing_slash", "/home/user/project/", "/home/user/project/file.go", "file.go"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := makeRelativePath(tt.workDir, tt.filePath)
			if got != tt.want {
				t.Errorf("makeRelativePath(%q, %q) = %q, want %q", tt.workDir, tt.filePath, got, tt.want)
			}
		})
	}
}

func TestAttachDiffCacheInvalidation(t *testing.T) {
	mv := mocks.NewMockSessionView("sess-cache", "feat-1")
	mv.MessageLogVal.Append(llm.SDKMessage{
		Assistant: &llm.AssistantMessage{
			Message: llm.ConversationMsg{Content: []llm.ContentBlock{{Type: "text", Text: "hello"}}},
		},
	})

	m := attachModelFromSession(mv, 80, 24)
	// Seed cache with a fake entry.
	m.diffCache["fake.go"] = nil
	m.diffCacheGeneration = 1

	// Add another message to change the generation.
	mv.MessageLogVal.Append(llm.SDKMessage{
		Assistant: &llm.AssistantMessage{
			Message: llm.ConversationMsg{Content: []llm.ContentBlock{{Type: "text", Text: "world"}}},
		},
	})

	// Enrichment should detect the generation change and clear the cache.
	m.enrichFileEventsWithDiffs(nil)
	if len(m.diffCache) != 0 {
		t.Errorf("expected cache to be cleared after generation change, got %d entries", len(m.diffCache))
	}
	if m.diffCacheGeneration != 2 {
		t.Errorf("diffCacheGeneration = %d, want 2", m.diffCacheGeneration)
	}
}

func TestAttachModelDetached(t *testing.T) {
	sess := session.NewSession("test-detach", "feat-1", 0)
	m := attachModelFromSession(sess, 80, 24)

	if m.Detached() {
		t.Error("should not be detached initially")
	}
	m.detached = true
	if !m.Detached() {
		t.Error("should be detached after setting flag")
	}
	if m.View() != "" {
		t.Error("view should be empty when detached")
	}
}

func TestRenderAttachMessages(t *testing.T) {
	msgs := []llm.SDKMessage{
		{
			Type: "system",
			Init: &llm.SystemInitMessage{SessionID: "s1", Model: "opus"},
		},
		{
			Type: "assistant",
			Assistant: &llm.AssistantMessage{
				Message: llm.ConversationMsg{
					Role:    "assistant",
					Content: []llm.ContentBlock{{Type: "text", Text: "hello world"}},
				},
			},
		},
		{
			Type: "result",
			Result: &llm.ResultMessage{
				Subtype:      "success",
				TotalCostUSD: 1.5,
			},
		},
	}

	output := renderAttachMessages(msgs, filterAll, 120, nil)
	if output == "" {
		t.Error("rendered output should not be empty")
	}
	clean := ansiRegex.ReplaceAllString(output, "")
	if !strings.Contains(clean, "hello world") {
		t.Errorf("expected 'hello world' in output, got: %s", output)
	}
}

func TestRenderAttachMessages_ControlRequestCanUseToolRendersAsToolUse(t *testing.T) {
	msgs := []llm.SDKMessage{
		{
			Type: "control_request",
			ControlRequest: &llm.ControlRequestMessage{
				RequestID: "req-1",
				Request: llm.ControlRequest{
					Subtype:  "can_use_tool",
					ToolName: "AskUserQuestion",
				},
			},
		},
	}

	output := renderAttachMessages(msgs, filterAll, 120, nil)
	if !strings.Contains(output, "[tool_use] AskUserQuestion") {
		t.Fatalf("expected tool_use rendering, got: %s", output)
	}
	if strings.Contains(output, "[permission]") {
		t.Fatalf("expected can_use_tool to avoid permission rendering, got: %s", output)
	}

	output = renderAttachMessages(msgs, filterNoTools, 120, nil)
	if strings.Contains(output, "AskUserQuestion") {
		t.Fatalf("expected No Tools filter to hide control-request tool lines, got: %s", output)
	}
}

func TestRenderAttachMessages_DedupesConsecutiveAssistantQuestion(t *testing.T) {
	question := "What exact version should Agentic be bumped to?"
	msgs := []llm.SDKMessage{
		{
			Type: "assistant",
			Assistant: &llm.AssistantMessage{
				Message: llm.ConversationMsg{
					Role:    "assistant",
					Content: []llm.ContentBlock{{Type: "text", Text: question}},
				},
			},
		},
		{
			Type: "assistant",
			Assistant: &llm.AssistantMessage{
				Message: llm.ConversationMsg{
					Role:    "assistant",
					Content: []llm.ContentBlock{{Type: "text", Text: question}},
				},
			},
		},
	}

	output := renderAttachMessages(msgs, filterAll, 120, nil)
	clean := ansiRegex.ReplaceAllString(output, "")
	if strings.Count(clean, question) != 1 {
		t.Fatalf("expected duplicate assistant question to be rendered once, got: %s", output)
	}
}

func TestPlanReviewMenuDispatchesDecision(t *testing.T) {
	sess := session.NewSession("feat-1-plan-review", "feat-1", 0)
	m := attachModelFromSession(sess, 80, 24)
	m.planReviewMode = true
	m.planReviewFeatureID = "feat-1"

	// Press Ctrl+D to show the menu
	ctrlD := tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl}
	m, _ = m.Update(ctrlD)
	if !m.showPlanReviewMenu {
		t.Fatal("expected plan review menu to be shown after Ctrl+D")
	}

	// Default choice is 0 ("iterate"). Press Enter.
	enter := tea.KeyPressMsg{Code: tea.KeyEnter}
	var cmd tea.Cmd
	m, cmd = m.Update(enter)
	if !m.Detached() {
		t.Error("expected model to be detached after Enter in plan review menu")
	}
	if cmd == nil {
		t.Fatal("expected a command from plan review menu Enter")
	}

	// Execute the cmd to get the message
	msg := cmd()
	decision, ok := msg.(PlanReviewDecisionMsg)
	if !ok {
		t.Fatalf("expected PlanReviewDecisionMsg, got %T", msg)
	}
	if decision.FeatureID != "feat-1" {
		t.Errorf("expected FeatureID=feat-1, got %s", decision.FeatureID)
	}
	if decision.Decision != "iterate" {
		t.Errorf("expected Decision=iterate, got %s", decision.Decision)
	}
}

func TestPlanReviewMenuProceedChoice(t *testing.T) {
	sess := session.NewSession("feat-1-plan-review", "feat-1", 0)
	m := attachModelFromSession(sess, 80, 24)
	m.planReviewMode = true
	m.planReviewFeatureID = "feat-1"

	// Show menu
	m, _ = m.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})

	// Navigate down to "proceed"
	m, _ = m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	if m.planReviewChoice != 1 {
		t.Fatalf("expected planReviewChoice=1 after down, got %d", m.planReviewChoice)
	}

	// Press Enter
	var cmd tea.Cmd
	m, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected command from proceed selection")
	}
	msg := cmd()
	decision, ok := msg.(PlanReviewDecisionMsg)
	if !ok {
		t.Fatalf("expected PlanReviewDecisionMsg, got %T", msg)
	}
	if decision.Decision != "proceed" {
		t.Errorf("expected Decision=proceed, got %s", decision.Decision)
	}
}

func TestSummarizeToolInput(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "empty", input: "", want: ""},
		{name: "short", input: `{"key":"value"}`, want: `{"key":"value"}`},
		{name: "long", input: string(make([]byte, 100)), want: string(make([]byte, 80)) + "..."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := summarizeToolInput(tt.input)
			if tt.name == "empty" && got != "" {
				t.Errorf("got %q, want empty", got)
			}
			if tt.name == "long" && len(got) != 83 { // 80 + "..."
				t.Errorf("got len %d, want 83", len(got))
			}
		})
	}
}

func TestRenderTabBar(t *testing.T) {
	t.Run("two repos with first selected", func(t *testing.T) {
		sess1 := session.NewSession("f1-impl-taulu-01", "f1", feature.PhaseImplement)
		sess2 := session.NewSession("f1-impl-graph-runner-01", "f1", feature.PhaseImplement)
		m := AttachModel{
			sess:   sess1,
			width:  80,
			height: 24,
			repoTabs: []repoTab{
				{repoName: "taulu", sess: sess1, status: statusImplementing},
				{repoName: "graph-runner", sess: sess2, status: statusPending},
			},
			activeTabIdx: 0,
		}
		bar := m.renderTabBar(80)
		if bar == "" {
			t.Error("expected non-empty tab bar")
		}
		if !strings.Contains(bar, "taulu") {
			t.Error("expected taulu in tab bar")
		}
		if !strings.Contains(bar, "graph") {
			t.Error("expected graph (abbreviated) in tab bar")
		}
	})

	t.Run("repo without session shown muted", func(t *testing.T) {
		sess := session.NewSession("f1-impl-taulu-01", "f1", feature.PhaseImplement)
		m := AttachModel{
			sess:   sess,
			width:  80,
			height: 24,
			repoTabs: []repoTab{
				{repoName: "taulu", sess: sess, status: statusImplementing},
				{repoName: "graph-runner", sess: nil, status: statusPending},
			},
			activeTabIdx: 0,
		}
		bar := m.renderTabBar(80)
		if !strings.Contains(bar, "graph") {
			t.Error("expected graph in tab bar even without session")
		}
	})
}

func TestTabBarNotShownSingleSession(t *testing.T) {
	sess := session.NewSession("f1-impl-01", "f1", feature.PhaseImplement)
	m := attachModelFromSession(sess, 80, 24)
	// The tab bar uses | separator between tabs - single session should not have it
	// renderTabBar returns empty for no repoTabs.
	bar := m.renderTabBar(80)
	if bar != "" {
		t.Error("should not render tab bar for single-session attach")
	}
}

func TestFindNextActiveTab(t *testing.T) {
	active := session.NewSession("s1", "f1", feature.PhaseImplement)
	tests := []struct {
		name      string
		tabs      []repoTab
		current   int
		direction int
		want      int
	}{
		{
			name:      "right to next active",
			tabs:      []repoTab{{sess: active}, {sess: active}, {sess: active}},
			current:   0,
			direction: 1,
			want:      1,
		},
		{
			name:      "right skips nil session",
			tabs:      []repoTab{{sess: active}, {sess: nil}, {sess: active}},
			current:   0,
			direction: 1,
			want:      2,
		},
		{
			name:      "right wraps around",
			tabs:      []repoTab{{sess: active}, {sess: nil}, {sess: active}},
			current:   2,
			direction: 1,
			want:      0,
		},
		{
			name:      "left wraps around",
			tabs:      []repoTab{{sess: active}, {sess: nil}, {sess: active}},
			current:   0,
			direction: -1,
			want:      2,
		},
		{
			name:      "no other active tab",
			tabs:      []repoTab{{sess: active}, {sess: nil}, {sess: nil}},
			current:   0,
			direction: 1,
			want:      -1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := AttachModel{repoTabs: tt.tabs, activeTabIdx: tt.current}
			got := m.findNextActiveTab(tt.direction)
			if got != tt.want {
				t.Errorf("findNextActiveTab(%d) = %d, want %d", tt.direction, got, tt.want)
			}
		})
	}
}

func TestTabSwitchUpdatesSession(t *testing.T) {
	sess1 := session.NewSession("f1-impl-taulu-01", "f1", feature.PhaseImplement)
	sess2 := session.NewSession("f1-impl-graph-01", "f1", feature.PhaseImplement)
	tabs := []repoTab{
		{repoName: "taulu", sess: sess1, status: statusImplementing},
		{repoName: "graph", sess: sess2, status: statusImplementing},
	}
	m := testAttachModel(sess1, 80, 24, tabs, 0)
	m, _ = m.switchToTab(1)
	if m.sess != sess2 {
		t.Error("expected sess to switch to sess2")
	}
	if m.activeTabIdx != 1 {
		t.Errorf("expected activeTabIdx=1, got %d", m.activeTabIdx)
	}
}

func TestTabSwitchResetsPromptState(t *testing.T) {
	sess1 := session.NewSession("s1", "f1", feature.PhaseImplement)
	sess2 := session.NewSession("s2", "f1", feature.PhaseImplement)
	tabs := []repoTab{
		{repoName: "a", sess: sess1},
		{repoName: "b", sess: sess2},
	}
	m := testAttachModel(sess1, 80, 24, tabs, 0)
	m.pendingPermRequestID = "req-123"
	m.pendingPermToolName = "Bash"
	m.awaitingInput = true
	m, _ = m.switchToTab(1)
	if m.pendingPermRequestID != "" {
		t.Error("expected permission state to be cleared after tab switch")
	}
	if m.awaitingInput {
		t.Error("expected awaitingInput to be cleared after tab switch")
	}
}

func TestStaleMessagesIgnored(t *testing.T) {
	sess1 := session.NewSession("s1", "f1", feature.PhaseImplement)
	sess2 := session.NewSession("s2", "f1", feature.PhaseImplement)
	m := AttachModel{
		sess:   sess1,
		width:  80,
		height: 24,
		repoTabs: []repoTab{
			{repoName: "a", sess: sess1},
			{repoName: "b", sess: sess2},
		},
		activeTabIdx:  0,
		tabGeneration: 1,
	}
	staleMsg := attachMsgsMsg{generation: 0, messages: []llm.SDKMessage{
		{Type: "assistant", Assistant: &llm.AssistantMessage{
			Message: llm.ConversationMsg{
				Role:    "assistant",
				Content: []llm.ContentBlock{{Type: "text", Text: "stale"}},
			},
		}},
	}}
	var cmd tea.Cmd
	m, cmd = m.Update(staleMsg)
	if cmd != nil {
		t.Error("expected nil cmd for stale message")
	}
}

func TestUpdateTabStatus(t *testing.T) {
	sess := session.NewSession("s1", "f1", feature.PhaseImplement)
	m := AttachModel{
		sess:  sess,
		width: 80, height: 24,
		repoTabs: []repoTab{
			{repoName: "taulu", sess: sess, status: statusImplementing},
			{repoName: "graph", sess: nil, status: statusPending},
		},
		activeTabIdx: 0,
	}
	m.updateTabStatus("graph", statusImplementing)
	if m.repoTabs[1].status != statusImplementing {
		t.Errorf("expected status update, got %v", m.repoTabs[1].status)
	}
}

func TestResolveInitialTab(t *testing.T) {
	active := session.NewSession("s1", "f1", feature.PhaseImplement)
	tests := []struct {
		name             string
		tabs             []repoTab
		lastAttachedRepo string
		want             int
	}{
		{
			name:             "matches last attached",
			tabs:             []repoTab{{repoName: "a", sess: active}, {repoName: "b", sess: active}},
			lastAttachedRepo: "b",
			want:             1,
		},
		{
			name:             "last attached has no session falls back to first active",
			tabs:             []repoTab{{repoName: "a", sess: active}, {repoName: "b", sess: nil}},
			lastAttachedRepo: "b",
			want:             0,
		},
		{
			name:             "empty last attached uses first active",
			tabs:             []repoTab{{repoName: "a", sess: nil}, {repoName: "b", sess: active}},
			lastAttachedRepo: "",
			want:             1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveInitialTab(tt.tabs, tt.lastAttachedRepo)
			if got != tt.want {
				t.Errorf("resolveInitialTab(%q) = %d, want %d", tt.lastAttachedRepo, got, tt.want)
			}
		})
	}
}

func TestTabSwitchKeepsInteractiveControls(t *testing.T) {
	t.Run("implement to review", func(t *testing.T) {
		implSess := session.NewSession("f1-impl-taulu-01", "f1", feature.PhaseImplement)
		reviewSess := session.NewSession("f1-review-graph-01", "f1", feature.PhaseReview)
		tabs := []repoTab{
			{repoName: "taulu", sess: implSess, status: statusImplementing},
			{repoName: "graph", sess: reviewSess, status: statusReviewing},
		}
		m := testAttachModel(implSess, 80, 24, tabs, 0)
		if m.readOnly {
			t.Fatal("expected readOnly=false for impl session")
		}
		m, _ = m.switchToTab(1)
		if m.readOnly {
			t.Error("expected readOnly=false after switching to review session")
		}
		if m.sess != reviewSess {
			t.Error("expected sess to switch to reviewSess")
		}
	})

	t.Run("review to implement", func(t *testing.T) {
		reviewSess := session.NewSession("f1-review-taulu-01", "f1", feature.PhaseReview)
		implSess := session.NewSession("f1-impl-graph-01", "f1", feature.PhaseImplement)
		tabs := []repoTab{
			{repoName: "taulu", sess: reviewSess, status: statusReviewing},
			{repoName: "graph", sess: implSess, status: statusImplementing},
		}
		m := testAttachModel(reviewSess, 80, 24, tabs, 0)
		m.readOnly = true // start in read-only
		m, _ = m.switchToTab(1)
		if m.readOnly {
			t.Error("expected readOnly=false after switching to interactive impl session")
		}
	})
}

func TestActiveRepoName(t *testing.T) {
	t.Run("returns repo name for multi-repo", func(t *testing.T) {
		m := AttachModel{
			repoTabs: []repoTab{
				{repoName: "taulu"},
				{repoName: "graph"},
			},
			activeTabIdx: 1,
		}
		if got := m.ActiveRepoName(); got != "graph" {
			t.Errorf("ActiveRepoName() = %q, want %q", got, "graph")
		}
	})

	t.Run("returns empty for single-session", func(t *testing.T) {
		m := AttachModel{}
		if got := m.ActiveRepoName(); got != "" {
			t.Errorf("ActiveRepoName() = %q, want empty", got)
		}
	})
}

func TestAttachModel_PermMenuRendered(t *testing.T) {
	sess := session.NewSession("test-perm-render", "feat-1", feature.PhaseImplement)
	tabs := []repoTab{{repoName: "repo-a", sess: sess}}
	m := testAttachModel(sess, 80, 24, tabs, 0)
	m.showPermMenu = true
	m.permMenuChoice = 0
	m.permMenuPattern = `Bash(npm test *)`
	m.pendingPermToolName = "Bash"
	m.pendingPermToolInput = json.RawMessage(`{"command":"npm test --coverage"}`)

	output := m.renderPermMenu()

	for _, want := range []string{"Allow & Remember", "Deny", "Bash(npm test *)", "Allow Bash?"} {
		if !strings.Contains(output, want) {
			t.Errorf("renderPermMenu output missing %q", want)
		}
	}
	if strings.Contains(output, "[y/r/n]") {
		t.Error("renderPermMenu should not contain legacy [y/r/n] prompt")
	}
}

func TestAttachModel_PermMenuHighlightMoves(t *testing.T) {
	sess := session.NewSession("test-perm-nav", "feat-1", feature.PhaseImplement)
	tabs := []repoTab{{repoName: "repo-a", sess: sess}}
	m := testAttachModel(sess, 80, 24, tabs, 0)
	m.showPermMenu = true
	m.permMenuChoice = 0
	m.pendingPermRequestID = "req-1"

	jKey := tea.KeyPressMsg{Code: 'j', Text: "j"}
	kKey := tea.KeyPressMsg{Code: 'k', Text: "k"}

	// j → choice 1
	m, _ = m.Update(jKey)
	if m.permMenuChoice != 1 {
		t.Errorf("after first j: permMenuChoice = %d, want 1", m.permMenuChoice)
	}

	// j → choice 2
	m, _ = m.Update(jKey)
	if m.permMenuChoice != 2 {
		t.Errorf("after second j: permMenuChoice = %d, want 2", m.permMenuChoice)
	}

	// j → clamped at 2
	m, _ = m.Update(jKey)
	if m.permMenuChoice != 2 {
		t.Errorf("after third j: permMenuChoice = %d, want 2 (clamped)", m.permMenuChoice)
	}

	// k → choice 1
	m, _ = m.Update(kKey)
	if m.permMenuChoice != 1 {
		t.Errorf("after k: permMenuChoice = %d, want 1", m.permMenuChoice)
	}
}

func TestAttachModel_PermMenu_YSelectsAllow(t *testing.T) {
	sess := session.NewSession("test-perm-y", "feat-1", feature.PhaseImplement)
	tabs := []repoTab{{repoName: "repo-a", sess: sess}}
	m := testAttachModel(sess, 80, 24, tabs, 0)
	m.sess = sess
	m.showPermMenu = true
	m.pendingPermRequestID = "req-y"
	m.pendingPermToolName = "Bash"
	m.pendingPermToolInput = json.RawMessage(`{"command":"ls -la"}`)
	m.permMenuPattern = "Bash(ls *)"

	yKey := tea.KeyPressMsg{Code: 'y', Text: "y"}
	var cmd tea.Cmd
	m, cmd = m.Update(yKey)

	if m.showPermMenu {
		t.Error("expected showPermMenu=false after 'y'")
	}
	if m.pendingPermRequestID != "" {
		t.Error("expected pendingPermRequestID to be cleared after 'y'")
	}
	if cmd == nil {
		t.Error("expected non-nil cmd after 'y' (allow)")
	}
}

func TestAttachModel_PermMenu_RSelectsRemember(t *testing.T) {
	sess := session.NewSession("test-perm-r", "feat-1", feature.PhaseImplement)
	tabs := []repoTab{{repoName: "repo-a", sess: sess}}
	m := testAttachModel(sess, 80, 24, tabs, 0)
	m.sess = sess
	m.showPermMenu = true
	m.pendingPermRequestID = "req-r"
	m.pendingPermToolName = "Bash"
	m.pendingPermToolInput = json.RawMessage(`{"command":"npm install"}`)
	m.permMenuPattern = "Bash(npm install *)"

	rKey := tea.KeyPressMsg{Code: 'r', Text: "r"}
	var cmd tea.Cmd
	m, cmd = m.Update(rKey)

	if m.showPermMenu {
		t.Error("expected showPermMenu=false after 'r'")
	}
	if m.pendingPermRequestID != "" {
		t.Error("expected pendingPermRequestID to be cleared after 'r'")
	}
	if cmd == nil {
		t.Error("expected non-nil cmd after 'r' (remember)")
	}
}

func TestAttachModel_PermMenu_NSelectsDeny(t *testing.T) {
	sess := session.NewSession("test-perm-n", "feat-1", feature.PhaseImplement)
	tabs := []repoTab{{repoName: "repo-a", sess: sess}}
	m := testAttachModel(sess, 80, 24, tabs, 0)
	m.sess = sess
	m.showPermMenu = true
	m.pendingPermRequestID = "req-n"
	m.pendingPermToolName = "Bash"
	m.pendingPermToolInput = json.RawMessage(`{"command":"rm -rf /"}`)
	m.permMenuPattern = "Bash(rm *)"

	nKey := tea.KeyPressMsg{Code: 'n', Text: "n"}
	var cmd tea.Cmd
	m, cmd = m.Update(nKey)

	if m.showPermMenu {
		t.Error("expected showPermMenu=false after 'n'")
	}
	if m.pendingPermRequestID != "" {
		t.Error("expected pendingPermRequestID to be cleared after 'n'")
	}
	if cmd == nil {
		t.Error("expected non-nil cmd after 'n' (deny)")
	}
}

func TestAttachModel_PermMenu_EnterSubmitsSelection(t *testing.T) {
	sess := session.NewSession("test-perm-enter", "feat-1", feature.PhaseImplement)
	tabs := []repoTab{{repoName: "repo-a", sess: sess}}
	m := testAttachModel(sess, 80, 24, tabs, 0)
	m.sess = sess
	m.showPermMenu = true
	m.permMenuChoice = 2 // Deny
	m.pendingPermRequestID = "req-enter"
	m.pendingPermToolName = "Bash"
	m.pendingPermToolInput = json.RawMessage(`{"command":"echo hi"}`)
	m.permMenuPattern = "Bash(echo *)"

	enterKey := tea.KeyPressMsg{Code: tea.KeyEnter}
	var cmd tea.Cmd
	m, cmd = m.Update(enterKey)

	if m.showPermMenu {
		t.Error("expected showPermMenu=false after Enter")
	}
	if m.pendingPermRequestID != "" {
		t.Error("expected pendingPermRequestID to be cleared after Enter")
	}
	if cmd == nil {
		t.Error("expected non-nil cmd after Enter")
	}
}

func TestAttachModel_PermMenu_JKNavigation(t *testing.T) {
	sess := session.NewSession("test-perm-jk", "feat-1", feature.PhaseImplement)
	tabs := []repoTab{{repoName: "repo-a", sess: sess}}
	m := testAttachModel(sess, 80, 24, tabs, 0)
	m.showPermMenu = true
	m.permMenuChoice = 0
	m.pendingPermRequestID = "req-jk"

	// j moves down
	m, _ = m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	if m.permMenuChoice != 1 {
		t.Errorf("after j: permMenuChoice = %d, want 1", m.permMenuChoice)
	}

	// k moves up
	m, _ = m.Update(tea.KeyPressMsg{Code: 'k', Text: "k"})
	if m.permMenuChoice != 0 {
		t.Errorf("after k: permMenuChoice = %d, want 0", m.permMenuChoice)
	}

	// k clamped at 0
	m, _ = m.Update(tea.KeyPressMsg{Code: 'k', Text: "k"})
	if m.permMenuChoice != 0 {
		t.Errorf("after second k: permMenuChoice = %d, want 0 (clamped)", m.permMenuChoice)
	}
}

func TestAttachModel_PermMenu_SwallowsOtherKeys(t *testing.T) {
	sess := session.NewSession("test-perm-swallow", "feat-1", feature.PhaseImplement)
	tabs := []repoTab{{repoName: "repo-a", sess: sess}}
	m := testAttachModel(sess, 80, 24, tabs, 0)
	m.showPermMenu = true
	m.permMenuChoice = 1
	m.pendingPermRequestID = "req-swallow"
	m.pendingPermToolName = "Bash"
	m.pendingPermToolInput = json.RawMessage(`{"command":"ls"}`)
	m.permMenuPattern = "Bash(ls *)"

	arbitraryKeys := []rune{'q', 'a', 'x', 'z', 'p'}
	for _, ch := range arbitraryKeys {
		var cmd tea.Cmd
		m, cmd = m.Update(tea.KeyPressMsg{Code: ch, Text: string(ch)})
		if !m.showPermMenu {
			t.Errorf("key %q should not close the perm menu", ch)
		}
		if cmd != nil {
			t.Errorf("key %q should produce nil cmd, got non-nil", ch)
		}
	}
	// Verify state is unchanged
	if m.permMenuChoice != 1 {
		t.Errorf("permMenuChoice changed to %d, expected 1", m.permMenuChoice)
	}
}

func TestAttachModel_PermMenu_DetachStillWorks(t *testing.T) {
	sess := session.NewSession("test-perm-detach", "feat-1", feature.PhaseImplement)
	tabs := []repoTab{{repoName: "repo-a", sess: sess}}

	// Ctrl+] should detach while perm menu is open.
	m := testAttachModel(sess, 80, 24, tabs, 0)
	m.showPermMenu = true
	m.permMenuChoice = 0
	m.pendingPermRequestID = "req-detach-ctrl"
	m.permMenuPattern = "Bash(ls *)"
	m, _ = m.Update(tea.KeyPressMsg{Code: ']', Mod: tea.ModCtrl})
	if !m.detached {
		t.Fatal("Ctrl+] should detach while perm menu is active")
	}

	// Esc should also detach while perm menu is open (non-tweak session).
	m = testAttachModel(sess, 80, 24, tabs, 0)
	m.showPermMenu = true
	m.permMenuChoice = 0
	m.pendingPermRequestID = "req-detach-esc"
	m.permMenuPattern = "Bash(ls *)"
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !m.detached {
		t.Fatal("Esc should detach while perm menu is active")
	}
}

func TestAttachModel_PermMenuPatternPreview(t *testing.T) {
	toolInput := `{"command":"npm test --coverage"}`
	expectedPattern := permission.InferBashPattern("Bash", toolInput)

	sess := session.NewSession("test-perm-pattern", "feat-1", feature.PhaseImplement)
	tabs := []repoTab{{repoName: "repo-a", sess: sess}}
	m := testAttachModel(sess, 80, 24, tabs, 0)
	m.showPermMenu = true
	m.pendingPermToolName = "Bash"
	m.pendingPermToolInput = json.RawMessage(toolInput)
	m.permMenuPattern = expectedPattern

	output := m.renderPermMenu()

	if !strings.Contains(output, expectedPattern) {
		t.Errorf("renderPermMenu output missing expected pattern %q\noutput: %s", expectedPattern, output)
	}
}

func TestAttachModel_TabSwitchClearsPermMenu(t *testing.T) {
	sess1 := session.NewSession("s1", "f1", feature.PhaseImplement)
	sess2 := session.NewSession("s2", "f1", feature.PhaseImplement)
	tabs := []repoTab{
		{repoName: "a", sess: sess1},
		{repoName: "b", sess: sess2},
	}
	m := testAttachModel(sess1, 80, 24, tabs, 0)
	m.showPermMenu = true
	m.permMenuChoice = 1
	m.permMenuPattern = "test"

	m, _ = m.switchToTab(1)

	if m.showPermMenu {
		t.Error("expected showPermMenu=false after tab switch")
	}
	if m.permMenuChoice != 0 {
		t.Errorf("expected permMenuChoice=0 after tab switch, got %d", m.permMenuChoice)
	}
	if m.permMenuPattern != "" {
		t.Errorf("expected permMenuPattern='' after tab switch, got %q", m.permMenuPattern)
	}
}

// TestAttachModelWithMockSessionView demonstrates using MockSessionView
// instead of a real *Session for TUI testing. This avoids importing
// session internals and lets tests configure exact return values.
func TestAttachModelWithMockSessionView(t *testing.T) {
	mv := mocks.NewMockSessionView("sess-1", "feat-1")
	mv.StatusVal = session.SessionRunning
	mv.ModelVal = "opus"
	mv.IterationVal = 2
	mv.RepoNameVal = "test-repo"

	m := attachModelFromSession(mv, 80, 24)
	if m.sess != mv {
		t.Error("session not stored correctly")
	}

	view := m.View()
	if view == "" {
		t.Error("view should not be empty")
	}

	// Verify interactions are recorded on the mock
	if err := mv.SendUserMessage("test message"); err != nil {
		t.Fatalf("SendUserMessage: %v", err)
	}
	if len(mv.SentMessages) != 1 || mv.SentMessages[0] != "test message" {
		t.Errorf("SentMessages = %v, want [test message]", mv.SentMessages)
	}
}

// TestSwitchToTab_UsesPermCacheScope_NotRepoName is a regression test verifying
// that permission scoping uses PermCacheScope() (which returns the actual repo
// name even for single-repo features) instead of RepoName() (which returns ""
// for single-repo features, causing permissions to be stored globally).
func TestSwitchToTab_UsesPermCacheScope_NotRepoName(t *testing.T) {
	// Create two mock sessions where RepoName returns "" (single-repo feature)
	// but PermCacheScope returns the actual repo name.
	mv1 := mocks.NewMockSessionView("sess-1", "feat-1")
	mv1.RepoNameVal = ""                // single-repo: RepoName() returns ""
	mv1.PermCacheScopeVal = "my-repo-a" // but PermCacheScope() returns actual repo

	mv2 := mocks.NewMockSessionView("sess-2", "feat-1")
	mv2.RepoNameVal = ""                // single-repo: RepoName() returns ""
	mv2.PermCacheScopeVal = "my-repo-b" // but PermCacheScope() returns actual repo

	tabs := []repoTab{
		{repoName: "a", sess: mv1},
		{repoName: "b", sess: mv2},
	}
	m := testAttachModel(mv1, 80, 24, tabs, 0)

	// Switch to tab 1 — permRepoName should come from PermCacheScope, not RepoName.
	m, _ = m.switchToTab(1)

	if m.permRepoName != "my-repo-b" {
		t.Errorf("permRepoName = %q after switchToTab, want %q (from PermCacheScope); "+
			"if this is empty, permission scoping is incorrectly using RepoName()",
			m.permRepoName, "my-repo-b")
	}
}

// TestRememberPermission_UsesPermCacheScope verifies that when the user selects
// "Allow & Remember" in the permission menu, the permission is stored with
// the scope from PermCacheScope() rather than RepoName(). This prevents
// single-repo features from accidentally storing permissions at global scope.
func TestRememberPermission_UsesPermCacheScope(t *testing.T) {
	mv := mocks.NewMockSessionView("sess-1", "feat-1")
	mv.RepoNameVal = ""              // RepoName() returns "" (would cause global scope)
	mv.PermCacheScopeVal = "my-repo" // PermCacheScope() returns actual repo

	tabs := []repoTab{{repoName: "my-repo", sess: mv}}
	m := testAttachModel(mv, 80, 24, tabs, 0)

	// Set up permission cache (in-memory only, no persistence store)
	cache := permission.NewCache(nil)
	m.permCache = cache
	m.permRepoName = mv.PermCacheScope() // simulate what attachToSession does

	// Set up a pending permission request
	m.showPermMenu = true
	m.permMenuChoice = 1 // "Allow & Remember"
	m.pendingPermRequestID = "req-1"
	m.pendingPermToolName = "Bash"
	m.pendingPermToolInput = json.RawMessage(`{"command":"npm test --coverage"}`)
	m.permMenuPattern = "Bash(npm test *)"

	// Execute the permission choice
	cmd := m.executePermChoice()
	if cmd != nil {
		// Run the command to trigger the RememberAllow call
		cmd()
	}

	// Verify the rule was stored with the repo-scoped name, not ""
	rules := cache.Rules()
	if len(rules) != 1 {
		t.Fatalf("expected 1 permission rule, got %d", len(rules))
	}
	if rules[0].RepoName != "my-repo" {
		t.Errorf("permission rule RepoName = %q, want %q; "+
			"if empty, permissions are incorrectly stored at global scope",
			rules[0].RepoName, "my-repo")
	}
}

func TestAttachPermissionRequestedIncludesRepoScopeAndIteration(t *testing.T) {
	featureID := "feat-perm-req-1"

	obs := &recordingObserver{}
	sess := mocks.NewMockSessionView("sess-1", featureID)
	sess.PermCacheScopeVal = "my-service"
	sess.IterationVal = 3

	m := AttachModel{
		observer:             obs,
		traceID:              "trace-perm-1",
		sess:                 sess,
		pendingPermToolName:  "Bash",
		pendingPermToolInput: json.RawMessage(`echo hello`),
	}

	m.emitPermissionRequested()

	evt, found := obs.event("permission.requested")
	if !found {
		t.Fatal("no permission.requested event found")
	}
	if evt.sessionID != "sess-1" {
		t.Errorf("expected session_id=sess-1, got %q", evt.sessionID)
	}
	if evt.repoName != "my-service" {
		t.Errorf("expected repo_name=my-service, got %q", evt.repoName)
	}
	if evt.iteration != 3 {
		t.Errorf("expected iteration=3, got %d", evt.iteration)
	}
	if evt.data["tool_name"] != "Bash" {
		t.Errorf("expected tool_name=Bash, got %v", evt.data["tool_name"])
	}
}

func TestAttachPermissionResolvedIncludesRepoScopeAndIteration(t *testing.T) {
	featureID := "feat-perm-res-1"

	obs := &recordingObserver{}
	sess := mocks.NewMockSessionView("sess-2", featureID)
	sess.PermCacheScopeVal = "backend-svc"
	sess.IterationVal = 5

	m := AttachModel{
		observer:            obs,
		traceID:             "trace-perm-res-1",
		sess:                sess,
		pendingPermToolName: "Edit",
	}

	// Simulate permission resolution inline (mirrors executePermChoice behavior)
	sc, ok := m.sessionSpanContext()
	if !ok {
		t.Fatal("expected valid span context")
	}
	m.observer.PermissionResolved(sc, m.sess.ID(), m.sess.PermCacheScope(), m.sess.Iteration(), "Edit", "allow")

	evt, found := obs.event("permission.resolved")
	if !found {
		t.Fatal("no permission.resolved event found")
	}
	if evt.sessionID != "sess-2" {
		t.Errorf("expected session_id=sess-2, got %q", evt.sessionID)
	}
	if evt.repoName != "backend-svc" {
		t.Errorf("expected repo_name=backend-svc, got %q", evt.repoName)
	}
	if evt.iteration != 5 {
		t.Errorf("expected iteration=5, got %d", evt.iteration)
	}
	if evt.data["decision"] != "allow" {
		t.Errorf("expected decision=allow, got %v", evt.data["decision"])
	}
}

func TestAttachQuestionAskedIncludesRepoAndIteration(t *testing.T) {
	featureID := "feat-qa-1"

	obs := &recordingObserver{}
	sess := mocks.NewMockSessionView("sess-3", featureID)
	sess.RepoNameVal = "frontend-app"
	sess.IterationVal = 2

	m := AttachModel{
		observer: obs,
		traceID:  "trace-qa-1",
		sess:     sess,
	}

	q := askUserQuestion{
		Question: "Which framework do you prefer?",
		Header:   "Framework",
	}
	m.emitQuestionAsked(q)

	evt, found := obs.event("question.asked")
	if !found {
		t.Fatal("no question.asked event found")
	}
	if evt.sessionID != "sess-3" {
		t.Errorf("expected session_id=sess-3, got %q", evt.sessionID)
	}
	if evt.repoName != "frontend-app" {
		t.Errorf("expected repo_name=frontend-app, got %q", evt.repoName)
	}
	if evt.iteration != 2 {
		t.Errorf("expected iteration=2, got %d", evt.iteration)
	}
	if evt.data["question"] != "Which framework do you prefer?" {
		t.Errorf("expected question text, got %v", evt.data["question"])
	}
}

func TestAttachQuestionAskedFallsBackToPermCacheScope(t *testing.T) {
	featureID := "feat-qa-fb-1"

	obs := &recordingObserver{}
	sess := mocks.NewMockSessionView("sess-4", featureID)
	sess.RepoNameVal = "" // empty — should fall back to PermCacheScope
	sess.PermCacheScopeVal = "fallback-scope"
	sess.IterationVal = 1

	m := AttachModel{
		observer: obs,
		traceID:  "trace-qa-fb-1",
		sess:     sess,
	}

	q := askUserQuestion{Question: "Confirm?"}
	m.emitQuestionAsked(q)

	evt, found := obs.event("question.asked")
	if !found {
		t.Fatal("no question.asked event found")
	}
	if evt.repoName != "fallback-scope" {
		t.Errorf("expected repo_name=fallback-scope (from PermCacheScope), got %q", evt.repoName)
	}
}

func TestAttachQuestionAnsweredIncludesRepoAndIteration(t *testing.T) {
	featureID := "feat-ans-1"

	obs := &recordingObserver{}
	sess := mocks.NewMockSessionView("sess-5", featureID)
	sess.RepoNameVal = "data-pipeline"
	sess.IterationVal = 4

	m := AttachModel{
		observer: obs,
		traceID:  "trace-ans-1",
		sess:     sess,
	}

	m.emitQuestionAnswered("Which DB?", "PostgreSQL")

	evt, found := obs.event("question.answered")
	if !found {
		t.Fatal("no question.answered event found")
	}
	if evt.sessionID != "sess-5" {
		t.Errorf("expected session_id=sess-5, got %q", evt.sessionID)
	}
	if evt.repoName != "data-pipeline" {
		t.Errorf("expected repo_name=data-pipeline, got %q", evt.repoName)
	}
	if evt.iteration != 4 {
		t.Errorf("expected iteration=4, got %d", evt.iteration)
	}
	if evt.data["question"] != "Which DB?" {
		t.Errorf("expected question='Which DB?', got %v", evt.data["question"])
	}
	if evt.data["answer"] != "PostgreSQL" {
		t.Errorf("expected answer='PostgreSQL', got %v", evt.data["answer"])
	}
}

func TestAttachRestoredPermissionReEmitsObservability(t *testing.T) {
	featureID := "feat-restore-perm-1"

	obs := &recordingObserver{}
	sess := mocks.NewMockSessionView("sess-6", featureID)
	sess.PermCacheScopeVal = "restored-svc"
	sess.IterationVal = 7

	m := AttachModel{
		observer:             obs,
		traceID:              "trace-restore-1",
		sess:                 sess,
		showPermMenu:         true,
		pendingPermToolName:  "Write",
		pendingPermToolInput: json.RawMessage(`file.go`),
	}

	m.emitRestoredObservability()

	evt, found := obs.event("permission.requested")
	if !found {
		t.Fatal("no permission.requested event from restored observability")
	}
	if evt.repoName != "restored-svc" {
		t.Errorf("expected repo_name=restored-svc, got %q", evt.repoName)
	}
	if evt.iteration != 7 {
		t.Errorf("expected iteration=7, got %d", evt.iteration)
	}
}

func TestAttachRestoredQuestionReEmitsObservability(t *testing.T) {
	featureID := "feat-restore-q-1"

	obs := &recordingObserver{}
	sess := mocks.NewMockSessionView("sess-7", featureID)
	sess.RepoNameVal = "restored-repo"
	sess.IterationVal = 2

	m := AttachModel{
		observer:           obs,
		traceID:            "trace-restore-q-1",
		sess:               sess,
		showPermMenu:       false,
		pendingQuestions:   []askUserQuestion{{Question: "Restored question?"}},
		currentQuestionIdx: 0,
	}

	m.emitRestoredObservability()

	evt, found := obs.event("question.asked")
	if !found {
		t.Fatal("no question.asked event from restored observability")
	}
	if evt.repoName != "restored-repo" {
		t.Errorf("expected repo_name=restored-repo, got %q", evt.repoName)
	}
	if evt.data["question"] != "Restored question?" {
		t.Errorf("expected question='Restored question?', got %v", evt.data["question"])
	}
}

func TestAttachSessionSpanContextReturnsEmptyForNoFeature(t *testing.T) {
	obs := &recordingObserver{}
	sess := mocks.NewMockSessionView("sess-8", "") // empty featureID

	m := AttachModel{
		observer: obs,
		traceID:  "trace-empty-1",
		sess:     sess,
	}

	_, ok := m.sessionSpanContext()
	if ok {
		t.Error("expected sessionSpanContext to return false for empty featureID")
	}
}

func TestAttachSessionSpanContextReturnsEmptyForNilSession(t *testing.T) {
	obs := &recordingObserver{}

	m := AttachModel{
		observer: obs,
		traceID:  "trace-nil-1",
		sess:     nil,
	}

	_, ok := m.sessionSpanContext()
	if ok {
		t.Error("expected sessionSpanContext to return false for nil session")
	}
}

// TestAttachToSessionEmitsRestoredPermissionObservability verifies that when
// attachToSession wires observer and traceID BEFORE calling emitRestoredObservability,
// a pending permission prompt produces a permission.requested event with valid
// traceID and non-empty SpanID. This is a regression test for the timing bug where
// emitRestoredObservability was called inside the constructor (before observer was set).
func TestAttachToSessionEmitsRestoredPermissionObservability(t *testing.T) {
	featureID := "feat-attach-perm-wiring"

	obs := &recordingObserver{}
	traceID := "trace-attach-perm-wiring"

	// Create a mock session with a pending permission prompt already active in its message log.
	sess := mocks.NewMockSessionView("sess-perm-wiring", featureID)
	sess.PermCacheScopeVal = "wired-svc"
	sess.IterationVal = 1

	// Simulate the wiring sequence from attachToSession:
	// 1. Constructor creates the model (observer is nil at this point)
	m := attachModelFromSession(sess, 80, 24)

	// 2. Simulate a pending permission prompt that was detected during construction
	m.showPermMenu = true
	m.pendingPermToolName = "Bash"
	m.pendingPermToolInput = json.RawMessage(`{"command":"npm test"}`)
	m.pendingPermRequestID = "req-wiring"

	// 3. Wire observer and traceID (as attachToSession does AFTER constructor)
	m.observer = obs
	m.traceID = traceID

	// 4. Now emit restored observability (as the fixed attachToSession does)
	m.emitRestoredObservability()

	// Verify the event was emitted with valid observer state
	evt, found := obs.event("permission.requested")
	if !found {
		t.Fatal("no permission.requested event found; emitRestoredObservability may have fired before observer was wired")
	}
	if evt.traceID != traceID {
		t.Errorf("expected trace_id=%s, got %q", traceID, evt.traceID)
	}
	if evt.spanID == "" {
		t.Error("expected non-empty span_id (observer was wired before emit)")
	}
	if evt.sessionID != "sess-perm-wiring" {
		t.Errorf("expected session_id=sess-perm-wiring, got %q", evt.sessionID)
	}
	if evt.data["tool_name"] != "Bash" {
		t.Errorf("expected tool_name=Bash, got %v", evt.data["tool_name"])
	}
}

// TestAttachToMultiRepoEmitsRestoredQuestionObservability verifies that the
// multi-repo attach path emits a question.asked event when a question was already
// active at attach time, using the correct traceID and observer. This is a
// regression test for the timing bug where emitRestoredObservability was called
// inside NewAttachModel before observer/traceID were set.
func TestAttachToMultiRepoEmitsRestoredQuestionObservability(t *testing.T) {
	featureID := "feat-multi-q-wiring"

	obs := &recordingObserver{}
	traceID := "trace-multi-q-wiring"

	sess1 := mocks.NewMockSessionView("sess-multi-q-1", featureID)
	sess1.PermCacheScopeVal = "repo-alpha"
	sess1.RepoNameVal = "repo-alpha"
	sess1.IterationVal = 3

	sess2 := mocks.NewMockSessionView("sess-multi-q-2", featureID)
	sess2.PermCacheScopeVal = "repo-beta"
	sess2.RepoNameVal = "repo-beta"
	sess2.IterationVal = 1

	tabs := []repoTab{
		{repoName: "repo-alpha", sess: sess1, status: statusImplementing},
		{repoName: "repo-beta", sess: sess2, status: statusImplementing},
	}

	// 1. Constructor creates the model (observer is nil at this point)
	m := NewAttachModel(tabs, 0, featureID, 80, 24)

	// 2. Simulate a pending question that was detected during construction
	m.pendingQuestions = []askUserQuestion{{Question: "Which database?", Header: "DB Choice"}}
	m.currentQuestionIdx = 0

	// 3. Wire observer and traceID (as attachToMultiRepoFeature does AFTER constructor)
	m.observer = obs
	m.traceID = traceID

	// 4. Now emit restored observability (as the fixed attachToMultiRepoFeature does)
	m.emitRestoredObservability()

	// Verify the event was emitted with valid observer state
	evt, found := obs.event("question.asked")
	if !found {
		t.Fatal("no question.asked event found; emitRestoredObservability may have fired before observer was wired")
	}
	if evt.traceID != traceID {
		t.Errorf("expected trace_id=%s, got %q", traceID, evt.traceID)
	}
	if evt.spanID == "" {
		t.Error("expected non-empty span_id (observer was wired before emit)")
	}
	if evt.sessionID != "sess-multi-q-1" {
		t.Errorf("expected session_id=sess-multi-q-1, got %q", evt.sessionID)
	}
	if evt.repoName != "repo-alpha" {
		t.Errorf("expected repo_name=repo-alpha, got %q", evt.repoName)
	}
	if evt.data["question"] != "Which database?" {
		t.Errorf("expected question='Which database?', got %v", evt.data["question"])
	}
}

func TestAttachModel_Esc_TweakSession_ShowsFinishPrompt(t *testing.T) {
	mv := mocks.NewMockSessionView("sess-1", "feat-1")
	m := attachModelFromSession(mv, 80, 24)
	m.isTweakSession = true

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})

	if !m.showFinishPrompt {
		t.Error("expected showFinishPrompt to be true after Esc in tweak session")
	}
	if m.Detached() {
		t.Error("should not be detached yet — prompt should be shown first")
	}
	if m.TweakFinishing() {
		t.Error("should not be finishing yet — user hasn't chosen")
	}
}

func TestAttachModel_Esc_NonTweakSession_DetachesNormally(t *testing.T) {
	mv := mocks.NewMockSessionView("sess-2", "feat-2")
	m := attachModelFromSession(mv, 80, 24)
	m.isTweakSession = false

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})

	if !m.Detached() {
		t.Error("non-tweak session should detach on Esc")
	}
	if m.showFinishPrompt {
		t.Error("non-tweak session should not show finish prompt")
	}
}

func TestAttachModel_FinishPrompt_F_FinishesAndDetaches(t *testing.T) {
	mv := mocks.NewMockSessionView("sess-3", "feat-3")
	m := attachModelFromSession(mv, 80, 24)
	m.isTweakSession = true
	m.showFinishPrompt = true

	m, _ = m.Update(tea.KeyPressMsg{Code: 'f', Text: "f"})

	if !m.TweakFinishing() {
		t.Error("expected tweakFinishing after pressing 'f' in finish prompt")
	}
	if !m.Detached() {
		t.Error("expected detached after pressing 'f' in finish prompt")
	}
	if m.showFinishPrompt {
		t.Error("finish prompt should be cleared after selection")
	}
}

func TestAttachModel_FinishPrompt_Enter_FinishesAndDetaches(t *testing.T) {
	mv := mocks.NewMockSessionView("sess-4", "feat-4")
	m := attachModelFromSession(mv, 80, 24)
	m.isTweakSession = true
	m.showFinishPrompt = true

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	if !m.TweakFinishing() {
		t.Error("expected tweakFinishing after pressing Enter in finish prompt")
	}
	if !m.Detached() {
		t.Error("expected detached after pressing Enter in finish prompt")
	}
}

func TestAttachModel_FinishPrompt_S_TriggersInterrupt(t *testing.T) {
	mv := mocks.NewMockSessionView("sess-stop-1", "feat-stop-1")
	m := attachModelFromSession(mv, 80, 24)
	m.isTweakSession = true
	m.showFinishPrompt = true

	m, cmd := m.Update(tea.KeyPressMsg{Code: 's', Text: "s"})

	if m.showFinishPrompt {
		t.Error("'s' should close the finish prompt")
	}
	if m.Detached() {
		t.Error("'s' should NOT detach — session stays alive after Stop")
	}
	if m.TweakFinishing() {
		t.Error("'s' should NOT mark the session as finishing")
	}
	if cmd == nil {
		t.Fatal("expected an interrupt command to be returned")
	}

	// Executing the cmd should call Interrupt() on the session and return
	// an agentInterruptedMsg.
	msg := cmd()
	if _, ok := msg.(agentInterruptedMsg); !ok {
		t.Fatalf("expected agentInterruptedMsg, got %T", msg)
	}
	if mv.InterruptCalled != 1 {
		t.Errorf("expected Interrupt() to be called once, got %d", mv.InterruptCalled)
	}
}

func TestAttachModel_AgentInterruptedMsg_SetsSuccessToast(t *testing.T) {
	mv := mocks.NewMockSessionView("sess-stop-2", "feat-stop-2")
	m := attachModelFromSession(mv, 80, 24)
	m.isTweakSession = true

	m, cmd := m.Update(agentInterruptedMsg{err: nil})

	if !strings.HasPrefix(m.interruptToast, "✓") {
		t.Errorf("expected success toast starting with ✓, got %q", m.interruptToast)
	}
	if m.interruptToastAt.IsZero() {
		t.Error("interruptToastAt should be stamped after success")
	}
	if cmd == nil {
		t.Error("expected a clear tick cmd to be returned")
	}
}

func TestAttachModel_AgentInterruptedMsg_SetsFailureToast(t *testing.T) {
	mv := mocks.NewMockSessionView("sess-stop-3", "feat-stop-3")
	m := attachModelFromSession(mv, 80, 24)
	m.isTweakSession = true

	m, _ = m.Update(agentInterruptedMsg{err: fmt.Errorf("boom")})

	if !strings.HasPrefix(m.interruptToast, "✗") {
		t.Errorf("expected failure toast starting with ✗, got %q", m.interruptToast)
	}
	if !strings.Contains(m.interruptToast, "boom") {
		t.Errorf("expected failure toast to include error text, got %q", m.interruptToast)
	}
}

func TestAttachModel_AgentToastClearMsg_ClearsToast(t *testing.T) {
	mv := mocks.NewMockSessionView("sess-stop-4", "feat-stop-4")
	m := attachModelFromSession(mv, 80, 24)
	m.interruptToast = "✓ Agent interrupted: what should I do instead?"
	// Stamp the toast in the past so the clear message drops it.
	m.interruptToastAt = m.interruptToastAt.Add(-2 * interruptToastDuration)

	m, _ = m.Update(agentToastClearMsg{})

	if m.interruptToast != "" {
		t.Errorf("expected toast cleared, got %q", m.interruptToast)
	}
}

func TestAttachModel_FinishPromptRenderer_IncludesStopOption(t *testing.T) {
	mv := mocks.NewMockSessionView("sess-stop-render", "feat-stop-render")
	m := attachModelFromSession(mv, 80, 24)
	rendered := m.renderFinishPromptInline()

	if !strings.Contains(rendered, "[s]") {
		t.Errorf("finish prompt should list the [s] Stop option, got: %s", rendered)
	}
	if !strings.Contains(rendered, "Stop") {
		t.Errorf("finish prompt should mention Stop, got: %s", rendered)
	}
}

func TestAttachModel_FinishPrompt_D_DetachesWithoutFinishing(t *testing.T) {
	mv := mocks.NewMockSessionView("sess-5", "feat-5")
	m := attachModelFromSession(mv, 80, 24)
	m.isTweakSession = true
	m.showFinishPrompt = true

	m, _ = m.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})

	if !m.Detached() {
		t.Error("expected detached after pressing 'd' in finish prompt")
	}
	if m.TweakFinishing() {
		t.Error("should NOT be finishing after pressing 'd' — detach only")
	}
}

func TestAttachModel_FinishPrompt_SecondEsc_CancelsPrompt(t *testing.T) {
	mv := mocks.NewMockSessionView("sess-6", "feat-6")
	m := attachModelFromSession(mv, 80, 24)
	m.isTweakSession = true
	m.showFinishPrompt = true

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})

	if m.showFinishPrompt {
		t.Error("second Esc should cancel the finish prompt")
	}
	if m.Detached() {
		t.Error("second Esc should not detach — just cancel the prompt")
	}
}

func TestAttachModel_FinishPrompt_UnrecognizedKey_CancelsPrompt(t *testing.T) {
	mv := mocks.NewMockSessionView("sess-7", "feat-7")
	m := attachModelFromSession(mv, 80, 24)
	m.isTweakSession = true
	m.showFinishPrompt = true

	m, _ = m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})

	if m.showFinishPrompt {
		t.Error("unrecognized key should cancel the finish prompt")
	}
	if m.Detached() {
		t.Error("unrecognized key should not detach")
	}
	if m.TweakFinishing() {
		t.Error("unrecognized key should not finish")
	}
}

func TestAttachModel_CtrlBracket_TweakSession_DetachesWithoutPrompt(t *testing.T) {
	mv := mocks.NewMockSessionView("sess-9", "feat-9")
	m := attachModelFromSession(mv, 80, 24)
	m.isTweakSession = true

	m, _ = m.Update(tea.KeyPressMsg{Code: ']', Mod: tea.ModCtrl})

	if !m.Detached() {
		t.Error("Ctrl+] should detach directly in tweak session")
	}
	if m.showFinishPrompt {
		t.Error("Ctrl+] should not show finish prompt")
	}
	if m.TweakFinishing() {
		t.Error("Ctrl+] should not set finishing (just detach)")
	}
}

func TestAttachModel_PermPrompt_Priority_OverFinishPrompt(t *testing.T) {
	sess := session.NewSession("sess-10", "feat-10", feature.PhaseImplement)
	tabs := []repoTab{{repoName: "repo-a", sess: sess}}
	m := testAttachModel(sess, 80, 24, tabs, 0)
	m.isTweakSession = true
	m.showPermMenu = true
	m.permMenuChoice = 0
	m.pendingPermRequestID = "req-1"

	// Press a key that navigates the perm menu
	m, _ = m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})

	if m.showFinishPrompt {
		t.Error("finish prompt should not activate while perm menu is visible")
	}
	if m.permMenuChoice != 1 {
		t.Errorf("perm menu should navigate, got choice %d", m.permMenuChoice)
	}
}

func TestAttachModel_AskUserQuestion_Priority_OverFinishPrompt(t *testing.T) {
	mv := mocks.NewMockSessionView("sess-11", "feat-11")
	m := attachModelFromSession(mv, 80, 24)
	m.isTweakSession = true
	m.pendingQuestions = []askUserQuestion{{
		Question: "Pick one",
		Options:  []askUserOption{{Label: "A"}, {Label: "B"}},
	}}
	m.currentQuestionIdx = 0
	m.selectedOption = 0

	// Esc in question mode detaches (existing behavior)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})

	if m.showFinishPrompt {
		t.Error("finish prompt should not activate while question is active")
	}
	// Question mode Esc detaches — this is existing behavior
	if !m.Detached() {
		t.Error("Esc during question should detach (existing behavior)")
	}
}

func TestAttachModel_CtrlD_TweakSession_SetsFinishing(t *testing.T) {
	mv := mocks.NewMockSessionView("sess-1", "feat-1")
	m := attachModelFromSession(mv, 80, 24)
	m.isTweakSession = true

	// Send Ctrl+D (same representation used in TestPlanReviewMenuDispatchesDecision)
	ctrlD := tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl}
	m, _ = m.Update(ctrlD)

	if !m.tweakFinishing {
		t.Error("tweakFinishing should be true after Ctrl+D in tweak session")
	}
	if !m.Detached() {
		t.Error("should be detached after Ctrl+D in tweak session")
	}
}

func TestAttachModel_CtrlD_NonTweakSession_NoOp(t *testing.T) {
	mv := mocks.NewMockSessionView("sess-2", "feat-2")
	m := attachModelFromSession(mv, 80, 24)
	m.isTweakSession = false

	// Send Ctrl+D
	ctrlD := tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl}
	m, _ = m.Update(ctrlD)

	if m.tweakFinishing {
		t.Error("tweakFinishing should remain false after Ctrl+D in non-tweak session")
	}
	if m.Detached() {
		t.Error("should not be detached after Ctrl+D in non-tweak session")
	}
}

func TestAttachModel_EnterSendsMessage(t *testing.T) {
	mv := mocks.NewMockSessionView("sess-enter", "feat-1")
	m := attachModelFromSession(mv, 80, 24)

	// Type "hello"
	for _, ch := range "hello" {
		m, _ = m.Update(tea.KeyPressMsg{Code: ch, Text: string(ch)})
	}
	if m.input.Value() != "hello" {
		t.Fatalf("input value = %q, want %q", m.input.Value(), "hello")
	}

	// Press Enter
	var cmd tea.Cmd
	m, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.input.Value() != "" {
		t.Errorf("input should be cleared after Enter, got %q", m.input.Value())
	}
	if cmd == nil {
		t.Error("Enter with text should produce a command (to send message)")
	}

	// Verify message logged
	msgs := mv.MessageLog().LastN(10)
	found := false
	for _, msg := range msgs {
		if msg.User != nil {
			for _, block := range msg.User.Message.Content {
				if block.IsText() && block.Text == "hello" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Error("expected user message 'hello' in message log")
	}
}

func TestAttachModel_EnterEmptyIsNoop(t *testing.T) {
	mv := mocks.NewMockSessionView("sess-empty", "feat-1")
	m := attachModelFromSession(mv, 80, 24)

	// Press Enter on empty textarea
	var cmd tea.Cmd
	m, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Error("Enter on empty textarea should produce nil cmd")
	}
	if m.input.Value() != "" {
		t.Errorf("input should remain empty, got %q", m.input.Value())
	}
}

func TestAttachModel_ShiftEnterInsertsNewline(t *testing.T) {
	mv := mocks.NewMockSessionView("sess-shift", "feat-1")
	m := attachModelFromSession(mv, 80, 24)

	// Type "line1"
	for _, ch := range "line1" {
		m, _ = m.Update(tea.KeyPressMsg{Code: ch, Text: string(ch)})
	}

	// Press Shift+Enter for newline
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})

	// Type "line2"
	for _, ch := range "line2" {
		m, _ = m.Update(tea.KeyPressMsg{Code: ch, Text: string(ch)})
	}

	if !strings.Contains(m.input.Value(), "\n") {
		t.Errorf("expected newline in input, got %q", m.input.Value())
	}
}

func TestAttachModel_CtrlSSendsMessage(t *testing.T) {
	mv := mocks.NewMockSessionView("sess-ctrls", "feat-1")
	m := attachModelFromSession(mv, 80, 24)

	// Type "hello"
	for _, ch := range "hello" {
		m, _ = m.Update(tea.KeyPressMsg{Code: ch, Text: string(ch)})
	}

	// Press Ctrl+S
	var cmd tea.Cmd
	m, cmd = m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if m.input.Value() != "" {
		t.Errorf("input should be cleared after Ctrl+S, got %q", m.input.Value())
	}
	if cmd == nil {
		t.Error("Ctrl+S with text should produce a command")
	}
}

func TestAttachModel_TextareaGrows(t *testing.T) {
	mv := mocks.NewMockSessionView("sess-grow", "feat-1")
	m := attachModelFromSession(mv, 80, 24)

	if m.inputHeight != minInputLines {
		t.Fatalf("initial inputHeight = %d, want %d", m.inputHeight, minInputLines)
	}

	// Type text with Shift+Enter to create multiple lines
	for i := 0; i < 4; i++ {
		for _, ch := range "text" {
			m, _ = m.Update(tea.KeyPressMsg{Code: ch, Text: string(ch)})
		}
		m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
	}

	if m.inputHeight <= minInputLines {
		t.Errorf("inputHeight should have grown, got %d", m.inputHeight)
	}
	if m.chatPanelHeight() != m.inputHeight+2 {
		t.Errorf("chatPanelHeight() = %d, want inputHeight+2 = %d", m.chatPanelHeight(), m.inputHeight+2)
	}
}

func TestAttachModel_TextareaShrinks(t *testing.T) {
	mv := mocks.NewMockSessionView("sess-shrink", "feat-1")
	m := attachModelFromSession(mv, 80, 24)

	// Type multi-line content
	for i := 0; i < 3; i++ {
		for _, ch := range "text" {
			m, _ = m.Update(tea.KeyPressMsg{Code: ch, Text: string(ch)})
		}
		m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
	}
	if m.inputHeight <= minInputLines {
		t.Fatalf("inputHeight should have grown, got %d", m.inputHeight)
	}

	// Send the message (which resets)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.inputHeight != minInputLines {
		t.Errorf("after send, inputHeight = %d, want %d", m.inputHeight, minInputLines)
	}
}

func TestAttachModel_TextareaMaxHeight(t *testing.T) {
	mv := mocks.NewMockSessionView("sess-max", "feat-1")
	m := attachModelFromSession(mv, 80, 24)

	// Create 8 lines via Shift+Enter
	for i := 0; i < 8; i++ {
		for _, ch := range "line" {
			m, _ = m.Update(tea.KeyPressMsg{Code: ch, Text: string(ch)})
		}
		if i < 7 {
			m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
		}
	}

	if m.inputHeight != maxInputLines {
		t.Errorf("inputHeight = %d, want maxInputLines = %d", m.inputHeight, maxInputLines)
	}
}

func TestAttachModel_ViewportAdjustsWithTextarea(t *testing.T) {
	mv := mocks.NewMockSessionView("sess-vp", "feat-1")
	m := attachModelFromSession(mv, 80, 40)

	// Compute effective viewport height from layout (View is value-receiver, so
	// viewport.SetHeight inside View doesn't persist on the original model).
	computeVPH := func(model AttachModel) int {
		const headerH = 3
		const footerH = 1
		const spacing = 2
		chatH := model.chatPanelHeight()
		msgPanelH := max(model.height-headerH-chatH-footerH-spacing, 6)
		return max(msgPanelH-2, 4)
	}

	vpH1 := computeVPH(m)

	// Grow textarea to maxInputLines
	for i := range maxInputLines {
		for _, ch := range "text" {
			m, _ = m.Update(tea.KeyPressMsg{Code: ch, Text: string(ch)})
		}
		if i < maxInputLines-1 {
			m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
		}
	}

	vpH6 := computeVPH(m)

	if vpH1 <= vpH6 {
		t.Errorf("viewport should be taller at 1-line textarea (%d) than at %d-line (%d)", vpH1, maxInputLines, vpH6)
	}
	if vpH1-vpH6 != maxInputLines-minInputLines {
		t.Errorf("viewport height difference = %d, want %d", vpH1-vpH6, maxInputLines-minInputLines)
	}
}

func TestAttachModel_WindowResizeRecalculates(t *testing.T) {
	mv := mocks.NewMockSessionView("sess-resize", "feat-1")
	m := attachModelFromSession(mv, 80, 24)

	// Type some multi-line content
	for _, ch := range "line1" {
		m, _ = m.Update(tea.KeyPressMsg{Code: ch, Text: string(ch)})
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
	for _, ch := range "line2" {
		m, _ = m.Update(tea.KeyPressMsg{Code: ch, Text: string(ch)})
	}
	grownHeight := m.inputHeight

	// Send a resize
	m, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if m.width != 120 || m.height != 40 {
		t.Errorf("dimensions = %dx%d, want 120x40", m.width, m.height)
	}
	// inputHeight should be preserved (content-dependent, not reset)
	if m.inputHeight != grownHeight {
		t.Errorf("inputHeight = %d after resize, want %d (preserved)", m.inputHeight, grownHeight)
	}
}

func TestAttachModel_SlashCommandSentAsIs(t *testing.T) {
	mv := mocks.NewMockSessionView("sess-slash", "feat-1")
	m := attachModelFromSession(mv, 80, 24)

	// Type "/help" — autocomplete activates on "/" with no match for "help"
	for _, ch := range "/help" {
		m, _ = m.Update(tea.KeyPressMsg{Code: ch, Text: string(ch)})
	}
	if !m.autocomplete.active {
		t.Fatal("autocomplete should be active after typing '/help'")
	}

	// First Enter dismisses the autocomplete (no matching items)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.autocomplete.active {
		t.Fatal("autocomplete should be dismissed after first Enter")
	}
	if len(mv.SentMessages) != 0 {
		t.Fatalf("first Enter should not send, got %d messages", len(mv.SentMessages))
	}

	// Second Enter sends the message normally
	var cmd tea.Cmd
	m, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected non-nil cmd for /help on second Enter")
	}

	cmd()

	if len(mv.SentMessages) != 1 {
		t.Fatalf("expected 1 sent message, got %d", len(mv.SentMessages))
	}
	if mv.SentMessages[0] != "/help" {
		t.Errorf("sent message = %q, want %q", mv.SentMessages[0], "/help")
	}
}

func TestAttachModel_EnterSendsInFreeformMode(t *testing.T) {
	mv := mocks.NewMockSessionView("sess-freeform", "feat-1")
	m := attachModelFromSession(mv, 80, 24)

	// Activate freeform typing mode via activate so questionStates/collectedAnswers are primed.
	m.activateAskUserQuestions(
		[]askUserQuestion{{Question: "What?", Options: nil}},
		"req-freeform",
		json.RawMessage(`{"questions":[{"question":"What?"}]}`),
	)

	// Type "answer"
	for _, ch := range "answer" {
		m, _ = m.Update(tea.KeyPressMsg{Code: ch, Text: string(ch)})
	}

	// First Enter commits the answer and advances to the recap slot. The
	// textarea should be cleared; no RespondToAskUser cmd yet.
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.input.Value() != "" {
		t.Errorf("input should be cleared after Enter in freeform mode, got %q", m.input.Value())
	}
	if !m.onRecapSlot() {
		t.Fatalf("expected recap slot after freeform Enter, got idx=%d", m.currentQuestionIdx)
	}

	// Second Enter on recap dispatches and yields a non-nil command.
	var cmd tea.Cmd
	m, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Error("Enter on recap slot should produce a dispatch command")
	}
}

func TestAttachModel_InitialTextareaHeight(t *testing.T) {
	mv := mocks.NewMockSessionView("sess-initial", "feat-1")
	m := attachModelFromSession(mv, 80, 24)

	if m.inputHeight != 1 {
		t.Errorf("initial inputHeight = %d, want 1", m.inputHeight)
	}
	if m.chatPanelHeight() != 3 {
		t.Errorf("initial chatPanelHeight() = %d, want 3 (1+2)", m.chatPanelHeight())
	}
}

func TestAttachModel_DirectFreeformAutoExpand(t *testing.T) {
	mv := mocks.NewMockSessionView("sess-df-expand", "feat-1")
	m := attachModelFromSession(mv, 80, 40)

	// Activate a direct-freeform question
	m.activateAskUserQuestions(
		[]askUserQuestion{{Question: "What version?", Options: nil}},
		"req-1",
		json.RawMessage(`{"questions":[{"question":"What version?"}]}`),
	)

	if m.inputHeight != minInputLines {
		t.Fatalf("after activateAskUserQuestions, inputHeight = %d, want %d", m.inputHeight, minInputLines)
	}

	initialChatH := m.chatPanelHeight()

	// Type multi-line content via Shift+Enter to grow to 3 lines
	for _, ch := range "line1" {
		m, _ = m.Update(tea.KeyPressMsg{Code: ch, Text: string(ch)})
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
	for _, ch := range "line2" {
		m, _ = m.Update(tea.KeyPressMsg{Code: ch, Text: string(ch)})
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
	for _, ch := range "line3" {
		m, _ = m.Update(tea.KeyPressMsg{Code: ch, Text: string(ch)})
	}

	if m.inputHeight != 3 {
		t.Errorf("inputHeight = %d after typing 3 lines, want 3", m.inputHeight)
	}

	grownChatH := m.chatPanelHeight()
	if grownChatH <= initialChatH {
		t.Errorf("chatPanelHeight should grow: initial=%d, grown=%d", initialChatH, grownChatH)
	}
	if grownChatH-initialChatH != 2 {
		t.Errorf("chatPanelHeight difference = %d, want 2 (3-1)", grownChatH-initialChatH)
	}
}

func TestAttachModel_ActivateQuestionResetsInputHeight(t *testing.T) {
	mv := mocks.NewMockSessionView("sess-reset-q", "feat-1")
	m := attachModelFromSession(mv, 80, 24)

	// Simulate having typed multi-line content
	m.inputHeight = 4
	m.input.SetHeight(4)

	// Activate a question
	m.activateAskUserQuestions(
		[]askUserQuestion{{Question: "Pick one", Options: []askUserOption{{Label: "A"}, {Label: "B"}}}},
		"req-2",
		json.RawMessage(`{"questions":[{"question":"Pick one","options":[{"label":"A"},{"label":"B"}]}]}`),
	)

	if m.inputHeight != minInputLines {
		t.Errorf("inputHeight = %d after activateAskUserQuestions, want %d", m.inputHeight, minInputLines)
	}
	if m.input.Value() != "" {
		t.Errorf("input value should be empty after activateAskUserQuestions, got %q", m.input.Value())
	}
}

func TestAttachAutocomplete_SlashActivates(t *testing.T) {
	mv := mocks.NewMockSessionView("s1", "f1")
	m := attachModelFromSession(mv, 80, 24)

	m, _ = m.Update(tea.KeyPressMsg{Code: '/', Text: "/"})

	if !m.autocomplete.active {
		t.Fatal("autocomplete should be active after typing '/'")
	}
	if m.autocomplete.mode != AutocompleteSkill {
		t.Errorf("autocomplete mode = %d, want AutocompleteSkill (%d)", m.autocomplete.mode, AutocompleteSkill)
	}
	// First trigger fires async load — autocomplete should be in loading state.
	if !m.autocomplete.loading {
		t.Error("autocomplete should be in loading state on first '/' trigger")
	}
	if !m.skillsLoading {
		t.Error("skillsLoading should be true while async load is in flight")
	}
}

func TestAttachAutocomplete_AtActivates(t *testing.T) {
	mv := mocks.NewMockSessionView("s1", "f1")
	m := attachModelFromSession(mv, 80, 24)

	m, _ = m.Update(tea.KeyPressMsg{Code: '@', Text: "@"})

	if !m.autocomplete.active {
		t.Fatal("autocomplete should be active after typing '@'")
	}
	if m.autocomplete.mode != AutocompleteFile {
		t.Errorf("autocomplete mode = %d, want AutocompleteFile (%d)", m.autocomplete.mode, AutocompleteFile)
	}
}

func TestAttachAutocomplete_AtStaysActiveAfterSlashInPath(t *testing.T) {
	mv := mocks.NewMockSessionView("s1", "f1")
	m := attachModelFromSession(mv, 80, 24)

	// Type "@src" — triggers file autocomplete on "@"
	for _, ch := range "@src" {
		m, _ = m.Update(tea.KeyPressMsg{Code: ch, Text: string(ch)})
	}
	if !m.autocomplete.active {
		t.Fatal("autocomplete should be active after typing '@src'")
	}
	if m.autocomplete.mode != AutocompleteFile {
		t.Fatalf("mode = %d, want AutocompleteFile", m.autocomplete.mode)
	}

	// Type "/" — autocomplete must stay active with query "src/"
	m, _ = m.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	if !m.autocomplete.active {
		t.Fatal("autocomplete should remain active after typing '/' inside an @ file query")
	}
	if m.autocomplete.mode != AutocompleteFile {
		t.Fatalf("mode = %d after '/', want AutocompleteFile", m.autocomplete.mode)
	}

	// Type "ma" — autocomplete must still be active with query "src/ma"
	for _, ch := range "ma" {
		m, _ = m.Update(tea.KeyPressMsg{Code: ch, Text: string(ch)})
	}
	if !m.autocomplete.active {
		t.Fatal("autocomplete should remain active after typing 'ma' in '@src/ma'")
	}
	if m.autocomplete.query != "src/ma" {
		t.Errorf("query = %q, want \"src/ma\"", m.autocomplete.query)
	}
}

func TestAttachAutocomplete_AtShowsLoadingState(t *testing.T) {
	mv := mocks.NewMockSessionView("s1", "f1")
	m := attachModelFromSession(mv, 80, 24)

	m, _ = m.Update(tea.KeyPressMsg{Code: '@', Text: "@"})

	view := m.View()
	if !strings.Contains(view, "Loading files...") {
		t.Fatalf("expected 'Loading files...' in view for file autocomplete loading, got view without it")
	}
}

func TestAttachAutocomplete_DownUpNavigates(t *testing.T) {
	mv := mocks.NewMockSessionView("s1", "f1")
	m := attachModelFromSession(mv, 80, 24)

	// Pre-populate skill cache so "/" uses cached items.
	m.skillItems = []AutocompleteItem{
		{Name: "commit", Description: "Create a git commit", Source: "agentic"},
		{Name: "review-pr", Description: "Review a pull request", Source: "agentic"},
		{Name: "debug", Description: "Debug issues", Source: "agentic"},
	}
	m.skillItemsLoaded = true

	// Activate with "/"
	m, _ = m.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	if m.autocomplete.cursor != 0 {
		t.Fatalf("cursor = %d after activation, want 0", m.autocomplete.cursor)
	}

	// Down -> cursor=1
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.autocomplete.cursor != 1 {
		t.Fatalf("cursor = %d after first Down, want 1", m.autocomplete.cursor)
	}

	// Down -> cursor=2
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.autocomplete.cursor != 2 {
		t.Fatalf("cursor = %d after second Down, want 2", m.autocomplete.cursor)
	}

	// Up -> cursor=1
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.autocomplete.cursor != 1 {
		t.Fatalf("cursor = %d after Up, want 1", m.autocomplete.cursor)
	}
}

func TestAttachAutocomplete_EnterSelects(t *testing.T) {
	mv := mocks.NewMockSessionView("s1", "f1")
	m := attachModelFromSession(mv, 80, 24)

	// Pre-populate skill cache so "/" uses cached items.
	m.skillItems = []AutocompleteItem{
		{Name: "commit", Description: "Create a git commit", Source: "agentic"},
		{Name: "review-pr", Description: "Review a pull request", Source: "agentic"},
		{Name: "debug", Description: "Debug issues", Source: "agentic"},
	}
	m.skillItemsLoaded = true

	// Activate with "/"
	m, _ = m.Update(tea.KeyPressMsg{Code: '/', Text: "/"})

	// Down to select second item (review-pr)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})

	// Enter to select
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	if m.autocomplete.active {
		t.Fatal("autocomplete should be dismissed after Enter selection")
	}
	val := m.input.Value()
	if !strings.Contains(val, "/review-pr ") {
		t.Errorf("input value = %q, want it to contain '/review-pr '", val)
	}
}

func TestAttachAutocomplete_EnterOnEmptyDismisses(t *testing.T) {
	mv := mocks.NewMockSessionView("s1", "f1")
	m := attachModelFromSession(mv, 80, 24)

	// Activate with "@" (file mode, stub returns no items)
	m, _ = m.Update(tea.KeyPressMsg{Code: '@', Text: "@"})
	if !m.autocomplete.active {
		t.Fatal("autocomplete should be active after '@'")
	}

	// Enter on empty list should dismiss with no text insertion and no send
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	if m.autocomplete.active {
		t.Fatal("autocomplete should be dismissed after Enter on empty list")
	}
	// No message should have been sent — Enter was consumed by the dismiss
	if len(mv.SentMessages) != 0 {
		t.Fatalf("expected 0 sent messages, got %d", len(mv.SentMessages))
	}
}

func TestAttachAutocomplete_EscDismisses(t *testing.T) {
	mv := mocks.NewMockSessionView("s1", "f1")
	m := attachModelFromSession(mv, 80, 24)

	// Activate with "/"
	m, _ = m.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	if !m.autocomplete.active {
		t.Fatal("autocomplete should be active after '/'")
	}

	// Esc to dismiss
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})

	if m.autocomplete.active {
		t.Fatal("autocomplete should be dismissed after Esc")
	}
	val := m.input.Value()
	if !strings.Contains(val, "/") {
		t.Errorf("input should still contain '/', got %q", val)
	}
}

func TestAttachAutocomplete_NormalInputWhenInactive(t *testing.T) {
	mv := mocks.NewMockSessionView("s1", "f1")
	m := attachModelFromSession(mv, 80, 24)

	for _, ch := range "hello" {
		m, _ = m.Update(tea.KeyPressMsg{Code: ch, Text: string(ch)})
	}

	if m.autocomplete.active {
		t.Fatal("autocomplete should not be active after typing 'hello'")
	}
	if m.input.Value() != "hello" {
		t.Errorf("input value = %q, want 'hello'", m.input.Value())
	}
}

func TestAttachAutocomplete_SlashMidWordNoTrigger(t *testing.T) {
	mv := mocks.NewMockSessionView("s1", "f1")
	m := attachModelFromSession(mv, 80, 24)

	// Type "path" one character at a time
	for _, ch := range "path" {
		m, _ = m.Update(tea.KeyPressMsg{Code: ch, Text: string(ch)})
	}

	// Type "/" — should NOT trigger autocomplete since not preceded by whitespace
	m, _ = m.Update(tea.KeyPressMsg{Code: '/', Text: "/"})

	if m.autocomplete.active {
		t.Fatal("autocomplete should not activate for '/' in the middle of a word")
	}

	// Type "foo"
	for _, ch := range "foo" {
		m, _ = m.Update(tea.KeyPressMsg{Code: ch, Text: string(ch)})
	}

	if m.autocomplete.active {
		t.Fatal("autocomplete should remain inactive")
	}
}

func TestAttachAutocomplete_ViewShowsOverlay(t *testing.T) {
	mv := mocks.NewMockSessionView("s1", "f1")
	m := attachModelFromSession(mv, 80, 24)

	// Pre-populate skill cache so items show in overlay.
	m.skillItems = []AutocompleteItem{
		{Name: "commit", Description: "Create a git commit", Source: "agentic"},
		{Name: "review-pr", Description: "Review a pull request", Source: "agentic"},
		{Name: "debug", Description: "Debug issues", Source: "agentic"},
	}
	m.skillItemsLoaded = true

	m, _ = m.Update(tea.KeyPressMsg{Code: '/', Text: "/"})

	view := m.View()
	if !strings.Contains(view, "commit") {
		t.Fatalf("expected 'commit' in view overlay, got %q", view)
	}
}

func TestAttachAutocomplete_DismissesOnTabSwitch(t *testing.T) {
	mv1 := mocks.NewMockSessionView("s1", "f1")
	mv2 := mocks.NewMockSessionView("s2", "f1")
	tabs := []repoTab{{sess: mv1, repoName: "repo1"}, {sess: mv2, repoName: "repo2"}}
	m := testAttachModel(mv1, 80, 24, tabs, 0)

	// Pre-populate skill cache.
	m.skillItems = []AutocompleteItem{{Name: "test", Source: "agentic"}}
	m.skillItemsLoaded = true

	// Activate autocomplete with "/"
	m, _ = m.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	if !m.autocomplete.active {
		t.Fatal("autocomplete should be active after '/'")
	}

	// Switch to second tab
	m, _ = m.switchToTab(1)

	if m.autocomplete.active {
		t.Fatal("autocomplete should be dismissed after tab switch")
	}
	// Verify cache is invalidated.
	if m.skillItemsLoaded {
		t.Error("skillItemsLoaded should be false after tab switch")
	}
	if m.skillItems != nil {
		t.Error("skillItems should be nil after tab switch")
	}
}

func TestAttachAutocomplete_DetachStillWorks(t *testing.T) {
	mv := mocks.NewMockSessionView("s1", "f1")
	m := attachModelFromSession(mv, 80, 24)

	// Activate autocomplete with "/"
	m, _ = m.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	if !m.autocomplete.active {
		t.Fatal("autocomplete should be active after '/'")
	}

	// Ctrl+] should fall through and detach (not consumed by autocomplete)
	m, _ = m.Update(tea.KeyPressMsg{Code: ']', Mod: tea.ModCtrl})

	if !m.detached {
		t.Fatal("Ctrl+] should detach even while autocomplete is active")
	}
}

func TestAttachAutocomplete_FilterToggleStillWorks(t *testing.T) {
	mv := mocks.NewMockSessionView("s1", "f1")
	m := attachModelFromSession(mv, 80, 24)

	initialFilter := m.filter

	// Activate autocomplete with "/"
	m, _ = m.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	if !m.autocomplete.active {
		t.Fatal("autocomplete should be active after '/'")
	}

	// Ctrl+F should fall through and toggle filter (not consumed by autocomplete)
	m, _ = m.Update(tea.KeyPressMsg{Code: 'f', Mod: tea.ModCtrl})

	if m.filter == initialFilter {
		t.Fatal("Ctrl+F should toggle filter even while autocomplete is active")
	}
}

func TestAttachAutocomplete_SlashActivatesWithRealSkills(t *testing.T) {
	mv := mocks.NewMockSessionView("s1", "f1")
	m := attachModelFromSession(mv, 80, 24)

	// Pre-populate with known items.
	m.skillItems = []AutocompleteItem{
		{Name: "commit", Description: "Create a git commit", Source: "agentic"},
		{Name: "review-pr", Description: "Review a PR", Source: "agentic"},
	}
	m.skillItemsLoaded = true

	m, _ = m.Update(tea.KeyPressMsg{Code: '/', Text: "/"})

	if !m.autocomplete.active {
		t.Fatal("autocomplete should be active")
	}
	if m.autocomplete.loading {
		t.Error("should not be loading when cache is populated")
	}
	if len(m.autocomplete.filtered) != 2 {
		t.Errorf("filtered length = %d, want 2", len(m.autocomplete.filtered))
	}
}

func TestAttachAutocomplete_SlashTriggersAsyncLoad(t *testing.T) {
	mv := mocks.NewMockSessionView("s1", "f1")
	m := attachModelFromSession(mv, 80, 24)

	// No pre-population — cache is empty.
	m, cmd := m.Update(tea.KeyPressMsg{Code: '/', Text: "/"})

	if !m.autocomplete.active {
		t.Fatal("autocomplete should be active")
	}
	if !m.autocomplete.loading {
		t.Error("autocomplete should be in loading state")
	}
	if !m.skillsLoading {
		t.Error("skillsLoading should be true")
	}
	if cmd == nil {
		t.Error("expected non-nil tea.Cmd for async skill load")
	}
}

func TestAttachAutocomplete_SkillsLoadedMsg_RefreshesDropdown(t *testing.T) {
	mv := mocks.NewMockSessionView("s1", "f1")
	m := attachModelFromSession(mv, 80, 24)

	// Activate in loading state.
	m, _ = m.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	if !m.autocomplete.loading {
		t.Fatal("precondition: autocomplete should be loading")
	}

	// Deliver skillsLoadedMsg.
	items := []AutocompleteItem{
		{Name: "commit", Description: "Create a git commit", Source: "agentic"},
		{Name: "debug", Description: "Debug issues", Source: "agentic"},
	}
	m, _ = m.Update(skillsLoadedMsg{items: items})

	if !m.skillItemsLoaded {
		t.Error("skillItemsLoaded should be true")
	}
	if m.skillsLoading {
		t.Error("skillsLoading should be false after load completes")
	}
	if m.autocomplete.loading {
		t.Error("autocomplete.loading should be false after items arrive")
	}
	if len(m.autocomplete.filtered) != 2 {
		t.Errorf("filtered length = %d, want 2", len(m.autocomplete.filtered))
	}
}

func TestAttachAutocomplete_SkillsLoadedMsg_WhileTyping(t *testing.T) {
	mv := mocks.NewMockSessionView("s1", "f1")
	m := attachModelFromSession(mv, 80, 24)

	// Activate with "/co" query in loading state.
	m, _ = m.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	// Type "co" to set query.
	for _, ch := range "co" {
		m, _ = m.Update(tea.KeyPressMsg{Code: ch, Text: string(ch)})
	}

	// Deliver skillsLoadedMsg with mixed items.
	items := []AutocompleteItem{
		{Name: "commit", Description: "Create a git commit", Source: "agentic"},
		{Name: "debug", Description: "Debug issues", Source: "agentic"},
	}
	m, _ = m.Update(skillsLoadedMsg{items: items})

	// The query "co" should filter to just "commit".
	if len(m.autocomplete.filtered) != 1 {
		t.Errorf("filtered length = %d, want 1 (only 'commit' matches 'co')", len(m.autocomplete.filtered))
	}
	if len(m.autocomplete.filtered) > 0 && m.autocomplete.filtered[0].Name != "commit" {
		t.Errorf("filtered[0].Name = %q, want \"commit\"", m.autocomplete.filtered[0].Name)
	}
}

func TestAttachAutocomplete_TabSwitch_InvalidatesSkillCache(t *testing.T) {
	mv1 := mocks.NewMockSessionView("s1", "f1")
	mv2 := mocks.NewMockSessionView("s2", "f1")
	tabs := []repoTab{{sess: mv1, repoName: "repo1"}, {sess: mv2, repoName: "repo2"}}
	m := testAttachModel(mv1, 80, 24, tabs, 0)

	// Pre-populate cache.
	m.skillItems = []AutocompleteItem{{Name: "commit", Source: "agentic"}}
	m.skillItemsLoaded = true
	m.skillsLoading = false

	// Switch tabs.
	m, _ = m.switchToTab(1)

	if m.skillItemsLoaded {
		t.Error("skillItemsLoaded should be false after tab switch")
	}
	if m.skillItems != nil {
		t.Error("skillItems should be nil after tab switch")
	}
	if m.skillsLoading {
		t.Error("skillsLoading should be false after tab switch")
	}
	if m.skillsWorkDir != "" {
		t.Errorf("skillsWorkDir = %q, want empty after tab switch", m.skillsWorkDir)
	}
}

func TestAttachAutocomplete_SkillsLoadedMsg_StaleWorkDir(t *testing.T) {
	mv1 := mocks.NewMockSessionView("s1", "f1")
	mv1.WorkDirVal = "/repo/a"
	mv2 := mocks.NewMockSessionView("s2", "f1")
	mv2.WorkDirVal = "/repo/b"
	tabs := []repoTab{{sess: mv1, repoName: "repo1"}, {sess: mv2, repoName: "repo2"}}
	m := testAttachModel(mv1, 80, 24, tabs, 0)

	// Trigger skill loading on tab A.
	m, _ = m.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	if !m.skillsLoading {
		t.Fatal("precondition: skillsLoading should be true")
	}
	if m.skillsWorkDir != "/repo/a" {
		t.Fatalf("skillsWorkDir = %q, want /repo/a", m.skillsWorkDir)
	}

	// Switch to tab B — clears skill state.
	m, _ = m.switchToTab(1)

	// Stale skillsLoadedMsg from repo A arrives.
	items := []AutocompleteItem{{Name: "commit", Source: "agentic"}}
	m, _ = m.Update(skillsLoadedMsg{items: items, workDir: "/repo/a"})

	// Should be discarded — skill state should remain empty for repo B.
	if m.skillItemsLoaded {
		t.Error("skillItemsLoaded should be false — stale result should be discarded")
	}
	if m.skillItems != nil {
		t.Error("skillItems should be nil — stale result should be discarded")
	}
}

func TestAttach_TabSwitch_PreservesPerTabMedia(t *testing.T) {
	mv1 := mocks.NewMockSessionView("s1", "f1")
	mv2 := mocks.NewMockSessionView("s2", "f1")
	tabs := []repoTab{{sess: mv1, repoName: "repo1"}, {sess: mv2, repoName: "repo2"}}
	m := testAttachModel(mv1, 80, 24, tabs, 0)

	// Simulate pasted media on tab A (idx 0).
	m.pastedImages = []string{"/tmp/img-1.png", "/tmp/img-2.png"}
	m.pastedFiles = []string{"/tmp/spec.pdf"}
	m.pastedFileNames = []string{"spec.pdf"}
	m.imageCounter = 2

	// Switch to tab B — tab B starts with no media.
	m, _ = m.switchToTab(1)

	if len(m.pastedImages) != 0 {
		t.Errorf("tab B pastedImages should be empty, got %d", len(m.pastedImages))
	}
	if len(m.pastedFiles) != 0 {
		t.Errorf("tab B pastedFiles should be empty, got %d", len(m.pastedFiles))
	}
	if len(m.pastedFileNames) != 0 {
		t.Errorf("tab B pastedFileNames should be empty, got %d", len(m.pastedFileNames))
	}
	// imageCounter stays global (monotonic) to avoid filename collisions in shared imageTempDir.
	if m.imageCounter != 2 {
		t.Errorf("imageCounter = %d, want 2 (global, not per-tab)", m.imageCounter)
	}

	// Switch back to tab A — media should be restored.
	m, _ = m.switchToTab(0)

	if len(m.pastedImages) != 2 {
		t.Errorf("tab A pastedImages after round-trip = %d, want 2", len(m.pastedImages))
	}
	if len(m.pastedFiles) != 1 {
		t.Errorf("tab A pastedFiles after round-trip = %d, want 1", len(m.pastedFiles))
	}
	if len(m.pastedFileNames) != 1 {
		t.Errorf("tab A pastedFileNames after round-trip = %d, want 1", len(m.pastedFileNames))
	}
}

func TestAttach_TabSwitch_MediaRoundTrip_PlaceholderRendering(t *testing.T) {
	mv1 := mocks.NewMockSessionView("s1", "f1")
	mv2 := mocks.NewMockSessionView("s2", "f1")
	tabs := []repoTab{{sess: mv1, repoName: "repo1"}, {sess: mv2, repoName: "repo2"}}
	m := testAttachModel(mv1, 80, 24, tabs, 0)

	imgPath := "/tmp/agentic-test/image-1.png"
	filePath := "/tmp/agentic-test/report.pdf"
	m.pastedImages = []string{imgPath}
	m.pastedFiles = []string{filePath}
	m.pastedFileNames = []string{"report.pdf"}

	// Build a pastedMediaMap as renderViewportContent would — should find the paths.
	checkMedia := func(label string) {
		t.Helper()
		var media *pastedMediaMap
		if len(m.pastedImages) > 0 || len(m.pastedFiles) > 0 {
			media = &pastedMediaMap{
				images:    m.pastedImages,
				files:     m.pastedFiles,
				fileNames: m.pastedFileNames,
			}
		}
		if media == nil {
			t.Fatalf("%s: pastedMediaMap is nil — placeholders will regress to raw paths", label)
		}
		text := "look at " + imgPath + " and " + filePath
		result := replacePastedPaths(text, media)
		if strings.Contains(result, imgPath) {
			t.Errorf("%s: image path not replaced: %s", label, result)
		}
		if strings.Contains(result, filePath) {
			t.Errorf("%s: file path not replaced: %s", label, result)
		}
		if !strings.Contains(result, "Image #1") {
			t.Errorf("%s: missing [Image #1] placeholder: %s", label, result)
		}
		if !strings.Contains(result, "report.pdf") {
			t.Errorf("%s: missing [report.pdf] placeholder: %s", label, result)
		}
	}

	// Before any switch — should work.
	checkMedia("before switch")

	// Switch to tab B then back to tab A — media must survive the round-trip.
	m, _ = m.switchToTab(1)
	m, _ = m.switchToTab(0)

	checkMedia("after round-trip A→B→A")
}

func TestAttachAutocomplete_StubRemoved(t *testing.T) {
	// Source-scan regression guard: ensure stubSkillItems is not referenced
	// in non-test production code.
	data, err := os.ReadFile("autocomplete.go")
	if err != nil {
		t.Fatalf("reading autocomplete.go: %v", err)
	}
	if strings.Contains(string(data), "stubSkillItems") {
		t.Error("stubSkillItems still referenced in autocomplete.go — should be removed")
	}
	if strings.Contains(string(data), "stubFileItems") {
		t.Error("stubFileItems still referenced in autocomplete.go — should be removed")
	}

	// Also check attach.go
	data, err = os.ReadFile("attach.go")
	if err != nil {
		t.Fatalf("reading attach.go: %v", err)
	}
	if strings.Contains(string(data), "stubSkillItems") {
		t.Error("stubSkillItems still referenced in attach.go — should be removed")
	}
	if strings.Contains(string(data), "stubFileItems") {
		t.Error("stubFileItems still referenced in attach.go — should be removed")
	}
}

func TestAttachModel_AtTriggerStartsFileIndexBuild(t *testing.T) {
	mv := mocks.NewMockSessionView("s1", "f1")
	mv.WorkDirVal = t.TempDir()
	m := attachModelFromSession(mv, 80, 24)

	m, cmd := m.Update(tea.KeyPressMsg{Code: '@', Text: "@"})

	if !m.fileIndexLoading {
		t.Error("fileIndexLoading should be true after first @ trigger")
	}
	if m.fileIndexWorkDir != mv.WorkDirVal {
		t.Errorf("fileIndexWorkDir = %q, want %q", m.fileIndexWorkDir, mv.WorkDirVal)
	}
	if cmd == nil {
		t.Fatal("expected non-nil cmd (buildFileIndexCmd) after first @ trigger")
	}
	if !m.autocomplete.Active() {
		t.Error("autocomplete should be active")
	}
	if !m.autocomplete.Loading() {
		t.Error("autocomplete should be in loading state")
	}
}

func TestAttachModel_AtTriggerWithReadyIndex(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "src"), 0o755)
	os.WriteFile(filepath.Join(dir, "src", "main.go"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "src", "util.go"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("x"), 0o644)

	fi := &FileIndex{}
	if err := fi.Build(dir); err != nil {
		t.Fatalf("Build: %v", err)
	}

	mv := mocks.NewMockSessionView("s1", "f1")
	mv.WorkDirVal = dir
	m := attachModelFromSession(mv, 80, 24)
	m.fileIndex = fi
	m.fileIndexWorkDir = dir

	// Type "@main" — should search index and show matching results.
	for _, ch := range "@main" {
		m, _ = m.Update(tea.KeyPressMsg{Code: rune(ch), Text: string(ch)})
	}

	if !m.autocomplete.Active() {
		t.Fatal("autocomplete should be active")
	}
	if m.autocomplete.Loading() {
		t.Error("autocomplete should not be loading when index is ready")
	}
	// "main" should match "src/main.go"
	sel := m.autocomplete.Selected()
	if sel == nil {
		t.Fatal("expected at least one result for query 'main'")
	}
	if sel.Name != "src/main.go" {
		t.Errorf("Selected().Name = %q, want \"src/main.go\"", sel.Name)
	}
}

func TestAttachModel_FileIndexReadyMsg_RefreshesResults(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("x"), 0o644)

	fi := &FileIndex{}
	fi.Build(dir)

	mv := mocks.NewMockSessionView("s1", "f1")
	mv.WorkDirVal = dir
	m := attachModelFromSession(mv, 80, 24)

	// Simulate: user typed "@", index build in flight.
	m, _ = m.Update(tea.KeyPressMsg{Code: '@', Text: "@"})
	if !m.fileIndexLoading {
		t.Fatal("precondition: fileIndexLoading should be true")
	}

	// Deliver fileIndexReadyMsg.
	m, _ = m.Update(fileIndexReadyMsg{index: fi, workDir: dir})

	if m.fileIndexLoading {
		t.Error("fileIndexLoading should be false after receiving fileIndexReadyMsg")
	}
	if m.fileIndex == nil {
		t.Fatal("fileIndex should be set after fileIndexReadyMsg")
	}
	if !m.fileIndex.Ready() {
		t.Fatal("fileIndex should be ready")
	}
}

func TestAttachModel_FileIndexReadyMsg_StaleWorkDir(t *testing.T) {
	mv := mocks.NewMockSessionView("s1", "f1")
	mv.WorkDirVal = "/original/dir"
	m := attachModelFromSession(mv, 80, 24)

	// Simulate: index build started for "/original/dir".
	m.fileIndexLoading = true
	m.fileIndexWorkDir = "/original/dir"

	// Tab switch happened, workDir changed.
	m.fileIndexWorkDir = "/new/dir"

	// Stale msg arrives.
	fi := &FileIndex{}
	fi.ready = true
	m, _ = m.Update(fileIndexReadyMsg{index: fi, workDir: "/original/dir"})

	// Should be discarded since workDir doesn't match.
	if m.fileIndex != nil {
		t.Error("stale fileIndexReadyMsg should be discarded")
	}
}

func TestAttachModel_TabSwitchResetsFileIndex(t *testing.T) {
	mv1 := mocks.NewMockSessionView("s1", "f1")
	mv1.WorkDirVal = "/repo1"
	mv2 := mocks.NewMockSessionView("s2", "f1")
	mv2.WorkDirVal = "/repo2"

	tabs := []repoTab{
		{repoName: "repo1", sess: mv1},
		{repoName: "repo2", sess: mv2},
	}
	m := testAttachModel(mv1, 80, 24, tabs, 0)

	// Set up file index state on first tab.
	m.fileIndex = &FileIndex{ready: true}
	m.fileIndexLoading = false
	m.fileIndexWorkDir = "/repo1"

	// Switch to second tab.
	m, _ = m.switchToTab(1)

	if m.fileIndex != nil {
		t.Error("fileIndex should be nil after tab switch")
	}
	if m.fileIndexLoading {
		t.Error("fileIndexLoading should be false after tab switch")
	}
	if m.fileIndexWorkDir != "" {
		t.Errorf("fileIndexWorkDir = %q, want empty", m.fileIndexWorkDir)
	}
}

func TestAttachModel_FileSearchLoadingIndicator(t *testing.T) {
	mv := mocks.NewMockSessionView("s1", "f1")
	m := attachModelFromSession(mv, 80, 24)

	m, _ = m.Update(tea.KeyPressMsg{Code: '@', Text: "@"})

	if !m.autocomplete.Active() {
		t.Fatal("autocomplete should be active")
	}
	view := m.View()
	if !strings.Contains(view, "Loading files...") {
		t.Error("View should contain 'Loading files...' while file index is building")
	}
}

func TestAttachModel_FileSearchSelection(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "test.go"), []byte("x"), 0o644)

	fi := &FileIndex{}
	fi.Build(dir)

	mv := mocks.NewMockSessionView("s1", "f1")
	mv.WorkDirVal = dir
	m := attachModelFromSession(mv, 80, 24)
	m.fileIndex = fi
	m.fileIndexWorkDir = dir

	// Type "@main"
	for _, ch := range "@main" {
		m, _ = m.Update(tea.KeyPressMsg{Code: rune(ch), Text: string(ch)})
	}

	if !m.autocomplete.Active() {
		t.Fatal("autocomplete should be active")
	}

	// Enter to select first result.
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	if m.autocomplete.Active() {
		t.Error("autocomplete should be dismissed after selection")
	}
	val := m.input.Value()
	if !strings.Contains(val, "@main.go ") {
		t.Errorf("input value = %q, expected it to contain '@main.go '", val)
	}
}

func TestReplacePastedPaths(t *testing.T) {
	tests := []struct {
		name       string
		text       string
		media      *pastedMediaMap
		wantPlain  string // expected substring after ANSI stripping (empty = skip check)
		wantAbsent string // must NOT be present after ANSI stripping (empty = skip check)
		wantExact  string // exact return value (empty = skip check)
	}{
		{
			name:      "nil media",
			text:      "some text",
			media:     nil,
			wantExact: "some text",
		},
		{
			name:       "single image",
			text:       "/tmp/img-1.png check this",
			media:      &pastedMediaMap{images: []string{"/tmp/img-1.png"}},
			wantPlain:  "[Image #1]",
			wantAbsent: "/tmp/img-1.png",
		},
		{
			name:      "multiple images",
			text:      "/tmp/img-1.png and /tmp/img-2.png",
			media:     &pastedMediaMap{images: []string{"/tmp/img-1.png", "/tmp/img-2.png"}},
			wantPlain: "[Image #1]",
		},
		{
			name:       "file placeholder",
			text:       "/tmp/spec.pdf review",
			media:      &pastedMediaMap{files: []string{"/tmp/spec.pdf"}, fileNames: []string{"spec.pdf"}},
			wantPlain:  "[spec.pdf]",
			wantAbsent: "/tmp/spec.pdf",
		},
		{
			name:       "mixed content",
			text:       "please review /tmp/img-1.png and give feedback",
			media:      &pastedMediaMap{images: []string{"/tmp/img-1.png"}},
			wantPlain:  "[Image #1]",
			wantAbsent: "/tmp/img-1.png",
		},
		{
			name:      "no matching paths",
			text:      "hello world",
			media:     &pastedMediaMap{images: []string{"/tmp/not-in-text.png"}},
			wantExact: "hello world",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := replacePastedPaths(tt.text, tt.media)
			stripped := ansiRegex.ReplaceAllString(got, "")

			if tt.wantExact != "" && got != tt.wantExact {
				t.Errorf("got %q, want exact %q", got, tt.wantExact)
			}
			if tt.wantPlain != "" && !strings.Contains(stripped, tt.wantPlain) {
				t.Errorf("stripped output %q does not contain %q", stripped, tt.wantPlain)
			}
			if tt.wantAbsent != "" && strings.Contains(stripped, tt.wantAbsent) {
				t.Errorf("stripped output %q should not contain %q", stripped, tt.wantAbsent)
			}
		})
	}

	// Additional check for "multiple images" — verify both placeholders present.
	t.Run("multiple images has both placeholders", func(t *testing.T) {
		got := replacePastedPaths("/tmp/img-1.png and /tmp/img-2.png", &pastedMediaMap{
			images: []string{"/tmp/img-1.png", "/tmp/img-2.png"},
		})
		stripped := ansiRegex.ReplaceAllString(got, "")
		if !strings.Contains(stripped, "[Image #1]") {
			t.Errorf("stripped output %q missing [Image #1]", stripped)
		}
		if !strings.Contains(stripped, "[Image #2]") {
			t.Errorf("stripped output %q missing [Image #2]", stripped)
		}
	})

	// Additional check for "mixed content" — surrounding text preserved.
	t.Run("mixed content preserves surrounding text", func(t *testing.T) {
		got := replacePastedPaths("please review /tmp/img-1.png and give feedback", &pastedMediaMap{
			images: []string{"/tmp/img-1.png"},
		})
		stripped := ansiRegex.ReplaceAllString(got, "")
		if !strings.Contains(stripped, "please review") {
			t.Errorf("stripped output %q missing surrounding text 'please review'", stripped)
		}
		if !strings.Contains(stripped, "and give feedback") {
			t.Errorf("stripped output %q missing surrounding text 'and give feedback'", stripped)
		}
	})
}

func TestImagePaste_TracksPastedImage(t *testing.T) {
	m := AttachModel{}
	m.input = textarea.New()

	m, _ = m.Update(ImagePastedMsg{Path: "/tmp/image-1.png"})

	if len(m.pastedImages) != 1 {
		t.Fatalf("len(pastedImages) = %d, want 1", len(m.pastedImages))
	}
	if m.pastedImages[0] != "/tmp/image-1.png" {
		t.Errorf("pastedImages[0] = %q, want %q", m.pastedImages[0], "/tmp/image-1.png")
	}
	if !strings.Contains(m.input.Value(), "[Image #1]") {
		t.Errorf("input value %q does not contain placeholder [Image #1]", m.input.Value())
	}
}

func TestImagePaste_SequentialNumbering(t *testing.T) {
	m := AttachModel{}
	m.input = textarea.New()

	m, _ = m.Update(ImagePastedMsg{Path: "/tmp/image-1.png"})
	m, _ = m.Update(ImagePastedMsg{Path: "/tmp/image-2.png"})

	if len(m.pastedImages) != 2 {
		t.Fatalf("len(pastedImages) = %d, want 2", len(m.pastedImages))
	}
	if m.pastedImages[0] != "/tmp/image-1.png" {
		t.Errorf("pastedImages[0] = %q, want %q", m.pastedImages[0], "/tmp/image-1.png")
	}
	if m.pastedImages[1] != "/tmp/image-2.png" {
		t.Errorf("pastedImages[1] = %q, want %q", m.pastedImages[1], "/tmp/image-2.png")
	}
}

func TestFilePaste_TracksFilesAndNames(t *testing.T) {
	m := AttachModel{}
	m.input = textarea.New()

	m, _ = m.Update(FilesPastedMsg{Paths: []string{"/tmp/spec.pdf"}, Names: []string{"spec.pdf"}})

	if len(m.pastedFiles) != 1 {
		t.Fatalf("len(pastedFiles) = %d, want 1", len(m.pastedFiles))
	}
	if m.pastedFiles[0] != "/tmp/spec.pdf" {
		t.Errorf("pastedFiles[0] = %q, want %q", m.pastedFiles[0], "/tmp/spec.pdf")
	}
	if len(m.pastedFileNames) != 1 {
		t.Fatalf("len(pastedFileNames) = %d, want 1", len(m.pastedFileNames))
	}
	if m.pastedFileNames[0] != "spec.pdf" {
		t.Errorf("pastedFileNames[0] = %q, want %q", m.pastedFileNames[0], "spec.pdf")
	}
	if !strings.Contains(m.input.Value(), "[spec.pdf]") {
		t.Errorf("input value %q does not contain placeholder [spec.pdf]", m.input.Value())
	}
}

func TestImagePaste_PlaceholderInInput(t *testing.T) {
	m := AttachModel{}
	m.input = textarea.New()

	m, _ = m.Update(ImagePastedMsg{Path: "/tmp/a.png"})
	m, _ = m.Update(ImagePastedMsg{Path: "/tmp/b.png"})

	val := m.input.Value()
	if !strings.Contains(val, "[Image #1]") {
		t.Errorf("input %q missing [Image #1]", val)
	}
	if !strings.Contains(val, "[Image #2]") {
		t.Errorf("input %q missing [Image #2]", val)
	}
	if strings.Contains(val, "/tmp/") {
		t.Errorf("input %q should not contain raw paths", val)
	}
}

func TestExpandMediaPlaceholders(t *testing.T) {
	tests := []struct {
		name  string
		text  string
		media *pastedMediaMap
		want  string
	}{
		{
			name:  "nil media",
			text:  "hello [Image #1]",
			media: nil,
			want:  "hello [Image #1]",
		},
		{
			name: "single image",
			text: "check [Image #1]",
			media: &pastedMediaMap{
				images: []string{"/tmp/img-1.png"},
			},
			want: "check /tmp/img-1.png",
		},
		{
			name: "multiple images",
			text: "[Image #1] and [Image #2]",
			media: &pastedMediaMap{
				images: []string{"/tmp/a.png", "/tmp/b.png"},
			},
			want: "/tmp/a.png and /tmp/b.png",
		},
		{
			name: "single file",
			text: "see [spec.pdf]",
			media: &pastedMediaMap{
				files:     []string{"/tmp/spec.pdf"},
				fileNames: []string{"spec.pdf"},
			},
			want: "see /tmp/spec.pdf",
		},
		{
			name: "mixed images and files",
			text: "[Image #1] with [report.pdf]",
			media: &pastedMediaMap{
				images:    []string{"/tmp/img.png"},
				files:     []string{"/tmp/report.pdf"},
				fileNames: []string{"report.pdf"},
			},
			want: "/tmp/img.png with /tmp/report.pdf",
		},
		{
			name: "no placeholders in text",
			text: "just regular text",
			media: &pastedMediaMap{
				images: []string{"/tmp/img.png"},
			},
			want: "just regular text",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := expandMediaPlaceholders(tt.text, tt.media)
			if got != tt.want {
				t.Errorf("expandMediaPlaceholders(%q) = %q, want %q", tt.text, got, tt.want)
			}
		})
	}
}

func TestRenderAttachMessages_ImagePlaceholderInViewport(t *testing.T) {
	msgs := []llm.SDKMessage{
		{
			Type: "user",
			User: &llm.UserMessage{
				Message: llm.ConversationMsg{
					Role:    "user",
					Content: []llm.ContentBlock{{Type: "text", Text: "/tmp/image-1.png"}},
				},
			},
			LocallyAppended: true,
		},
	}

	media := &pastedMediaMap{images: []string{"/tmp/image-1.png"}}
	output := renderAttachMessages(msgs, filterAll, 120, media)
	stripped := ansiRegex.ReplaceAllString(output, "")

	if !strings.Contains(stripped, "[Image #1]") {
		t.Errorf("stripped output %q does not contain [Image #1]", stripped)
	}
	if strings.Contains(stripped, "/tmp/image-1.png") {
		t.Errorf("stripped output %q should not contain raw path /tmp/image-1.png", stripped)
	}
	if !strings.Contains(stripped, "[you]") {
		t.Errorf("stripped output %q does not contain [you] label", stripped)
	}
}

func TestRenderAttachMessages_AutoPickedUserLabel(t *testing.T) {
	msgs := []llm.SDKMessage{
		{
			Type:               "user",
			LocallyAppended:    true,
			AutoPicked:         true,
			AutoPickConfidence: 0.85,
			User: &llm.UserMessage{
				Message: llm.ConversationMsg{
					Role:    "user",
					Content: []llm.ContentBlock{{Type: "text", Text: "Focused (Recommended)"}},
				},
			},
		},
	}

	output := renderAttachMessages(msgs, filterAll, 120, nil)
	stripped := ansiRegex.ReplaceAllString(output, "")
	if !strings.Contains(stripped, "[auto-picked, confidence: 0.85] Focused (Recommended)") {
		t.Fatalf("stripped output %q does not contain auto-picked answer", stripped)
	}
	if strings.Contains(stripped, "[you]") {
		t.Fatalf("stripped output %q should not label auto-picked answer as [you]", stripped)
	}
}

func TestRenderAttachMessages_MultipleImagePlaceholders(t *testing.T) {
	msgs := []llm.SDKMessage{
		{
			Type: "user",
			User: &llm.UserMessage{
				Message: llm.ConversationMsg{
					Role:    "user",
					Content: []llm.ContentBlock{{Type: "text", Text: "/tmp/img-1.png and /tmp/img-2.png"}},
				},
			},
			LocallyAppended: true,
		},
	}

	media := &pastedMediaMap{images: []string{"/tmp/img-1.png", "/tmp/img-2.png"}}
	output := renderAttachMessages(msgs, filterAll, 120, media)
	stripped := ansiRegex.ReplaceAllString(output, "")

	if !strings.Contains(stripped, "[Image #1]") {
		t.Errorf("stripped output %q does not contain [Image #1]", stripped)
	}
	if !strings.Contains(stripped, "[Image #2]") {
		t.Errorf("stripped output %q does not contain [Image #2]", stripped)
	}
}

func TestRenderAttachMessages_FilePlaceholder(t *testing.T) {
	msgs := []llm.SDKMessage{
		{
			Type: "user",
			User: &llm.UserMessage{
				Message: llm.ConversationMsg{
					Role:    "user",
					Content: []llm.ContentBlock{{Type: "text", Text: "/tmp/spec.pdf"}},
				},
			},
			LocallyAppended: true,
		},
	}

	media := &pastedMediaMap{
		files:     []string{"/tmp/spec.pdf"},
		fileNames: []string{"spec.pdf"},
	}
	output := renderAttachMessages(msgs, filterAll, 120, media)
	stripped := ansiRegex.ReplaceAllString(output, "")

	if !strings.Contains(stripped, "[spec.pdf]") {
		t.Errorf("stripped output %q does not contain [spec.pdf]", stripped)
	}
	if strings.Contains(stripped, "/tmp/spec.pdf") {
		t.Errorf("stripped output %q should not contain raw path /tmp/spec.pdf", stripped)
	}
}

func TestRenderAttachMessages_AssistantMessageUnaffected(t *testing.T) {
	msgs := []llm.SDKMessage{
		{
			Type: "assistant",
			Assistant: &llm.AssistantMessage{
				Message: llm.ConversationMsg{
					Role:    "assistant",
					Content: []llm.ContentBlock{{Type: "text", Text: "/tmp/image-1.png"}},
				},
			},
		},
	}

	media := &pastedMediaMap{images: []string{"/tmp/image-1.png"}}
	output := renderAttachMessages(msgs, filterAll, 120, media)
	stripped := ansiRegex.ReplaceAllString(output, "")

	if !strings.Contains(stripped, "/tmp/image-1.png") {
		t.Errorf("stripped output %q should still contain raw path for assistant message", stripped)
	}
}

func TestRenderAttachMessages_NonLocalUserUnaffected(t *testing.T) {
	msgs := []llm.SDKMessage{
		{
			Type: "user",
			User: &llm.UserMessage{
				Message: llm.ConversationMsg{
					Role:    "user",
					Content: []llm.ContentBlock{{Type: "text", Text: "/tmp/image-1.png"}},
				},
			},
			LocallyAppended: false,
		},
	}

	media := &pastedMediaMap{images: []string{"/tmp/image-1.png"}}
	output := renderAttachMessages(msgs, filterAll, 120, media)
	stripped := ansiRegex.ReplaceAllString(output, "")

	if !strings.Contains(stripped, "/tmp/image-1.png") {
		t.Errorf("stripped output %q should still contain raw path for non-local user message", stripped)
	}
}

func TestRenderAttachMessages_NilMediaPassthrough(t *testing.T) {
	msgs := []llm.SDKMessage{
		{
			Type: "user",
			User: &llm.UserMessage{
				Message: llm.ConversationMsg{
					Role:    "user",
					Content: []llm.ContentBlock{{Type: "text", Text: "/tmp/image-1.png"}},
				},
			},
			LocallyAppended: true,
		},
	}

	output := renderAttachMessages(msgs, filterAll, 120, nil)
	stripped := ansiRegex.ReplaceAllString(output, "")

	if !strings.Contains(stripped, "/tmp/image-1.png") {
		t.Errorf("stripped output %q should still contain raw path when media is nil", stripped)
	}
}

// TestRenderAttachMessages_PartialAssistantSkipsMarkdown verifies that a
// streaming (partial) assistant message is rendered as plain wrapped text —
// raw backticks and hashes leak through. This is the streaming-safe path
// for Codex deltas, whose accumulated text routinely contains half-written
// fenced code blocks that would parse as broken markdown.
func TestRenderAttachMessages_PartialAssistantSkipsMarkdown(t *testing.T) {
	// A half-written code fence as it might appear mid-stream.
	partialText := "Here is some code:\n\n```go\nfunc foo("
	msgs := []llm.SDKMessage{
		{
			Type:    "assistant",
			Subtype: "partial",
			Assistant: &llm.AssistantMessage{
				Message: llm.ConversationMsg{
					Role:    "assistant",
					Content: []llm.ContentBlock{{Type: "text", Text: partialText}},
				},
			},
		},
	}

	output := renderAttachMessages(msgs, filterAll, 120, nil)
	stripped := ansiRegex.ReplaceAllString(output, "")

	// Raw markdown syntax should still be visible (not parsed away).
	if !strings.Contains(stripped, "```go") {
		t.Errorf("partial output should preserve raw backtick fence, got %q", stripped)
	}
	if !strings.Contains(stripped, "func foo(") {
		t.Errorf("partial output should preserve raw code text, got %q", stripped)
	}
}

// TestRenderAttachMessages_CompleteAssistantRendersMarkdown verifies that a
// non-partial assistant message runs through the configured markdown renderer.
func TestRenderAttachMessages_CompleteAssistantRendersMarkdown(t *testing.T) {
	previous := renderMarkdown
	SetMarkdownRenderer(func(text string, width int) string {
		text = strings.ReplaceAll(text, "`inline code`", "inline code")
		text = strings.ReplaceAll(text, "- item one", "• item one")
		return "\x1b[1m" + text + "\x1b[0m"
	})
	t.Cleanup(func() { renderMarkdown = previous })

	text := "# Heading\n\nHere is some text with `inline code` and a list:\n\n- item one\n- item two\n"
	msgs := []llm.SDKMessage{
		{
			Type: "assistant",
			// No Subtype: this is a finalized message.
			Assistant: &llm.AssistantMessage{
				Message: llm.ConversationMsg{
					Role:    "assistant",
					Content: []llm.ContentBlock{{Type: "text", Text: text}},
				},
			},
		},
	}

	output := renderAttachMessages(msgs, filterAll, 120, nil)

	if !strings.Contains(output, "\x1b[") {
		t.Errorf("expected ANSI escapes in rendered complete assistant message, got %q", output)
	}
	stripped := ansiRegex.ReplaceAllString(output, "")
	if !strings.Contains(stripped, "item one") {
		t.Errorf("expected list content to be preserved in output, got %q", stripped)
	}
	// Inline code backticks should be consumed by the markdown parser
	// (the literal backticks are not part of rendered output).
	if strings.Contains(stripped, "`inline code`") {
		t.Errorf("expected inline code backticks to be consumed by renderer, got %q", stripped)
	}
	// List items should render with bullet glyphs, not the raw "-" marker.
	if strings.Contains(stripped, "- item one") {
		t.Errorf("expected list dash to be replaced with bullet glyph, got %q", stripped)
	}
}

func TestAttachModel_QuestionPanelWrappedHeight(t *testing.T) {
	// Regression: at narrow widths, long question text and long option
	// labels/descriptions wrap to multiple terminal lines. The panel
	// measurement must account for this wrapping so choices and hints
	// are not clipped off-screen.
	mv := mocks.NewMockSessionView("sess-wrap", "feat-1")

	// Use a narrow terminal width (30 cols). Content width after borders
	// and padding: max(30-2,22) - 4 = 28 - 4 = 24 characters.
	narrowWidth := 30
	m := attachModelFromSession(mv, narrowWidth, 40)

	longQuestion := "Which authentication provider should we use for the new user management system?"
	longLabel := "OAuth2 with PKCE flow (Recommended)"
	longDesc := "Uses industry-standard OAuth2 with Proof Key for Code Exchange for maximum security"

	m.activateAskUserQuestions(
		[]askUserQuestion{{
			Question: longQuestion,
			Options: []askUserOption{
				{Label: longLabel, Description: longDesc, Confidence: floatPtr(0.82)},
				{Label: "Basic JWT tokens", Description: "Simple stateless tokens"},
				{Label: "Session cookies", Description: "Traditional server-side sessions"},
			},
		}},
		"req-wrap",
		json.RawMessage(`{"questions":[{"question":"q"}]}`),
	)

	contentW := m.questionContentWidth()

	// The question text should wrap to more than 1 line at 26-char width.
	qLines := wrappedLineCount(longQuestion, contentW)
	if qLines <= 1 {
		t.Fatalf("question text %q should wrap at width %d, got %d lines", longQuestion, contentW, qLines)
	}

	// The first option label should also wrap.
	labelLines := wrappedLineCount(fmt.Sprintf("  1. %s", longLabel), contentW)
	if labelLines <= 1 {
		t.Fatalf("option label %q should wrap at width %d, got %d lines", longLabel, contentW, labelLines)
	}

	// chatPanelHeight must be taller than the fixed-count estimate.
	// Fixed estimate = overhead(8 = q(1) + blank(1) + separator(1) +
	// "Type something"(1) + blank(1) + notes(1) + blank(1) + hint(1)) +
	// option lines (first option label+desc+confidence = 3, remaining two
	// options label+desc = 2 each) = 15.
	fixedEstimate := 15
	actualH := m.chatPanelHeight()
	if actualH <= fixedEstimate {
		t.Errorf("chatPanelHeight() = %d, should be > %d (fixed estimate) because text wraps at width %d",
			actualH, fixedEstimate, contentW)
	}

	// Scrolling should let all options become reachable. Scroll down through
	// each option and verify questionVisibleWindow keeps the selected option
	// in view.
	for i := range 3 {
		m.selectedOption = i
		m.updateQuestionScrollOffset()
		start, end, _, _ := m.questionVisibleWindow()
		if i < start || i >= end {
			t.Errorf("option %d not visible after scroll: start=%d, end=%d", i, start, end)
		}
	}

	// Verify that a wide terminal (120 cols) produces the old fixed-count height
	// for the same question, confirming wrapping only kicks in when needed.
	mWide := attachModelFromSession(mv, 120, 40)
	mWide.activateAskUserQuestions(
		[]askUserQuestion{{
			Question: longQuestion,
			Options: []askUserOption{
				{Label: longLabel, Description: longDesc, Confidence: floatPtr(0.82)},
				{Label: "Basic JWT tokens", Description: "Simple stateless tokens"},
				{Label: "Session cookies", Description: "Traditional server-side sessions"},
			},
		}},
		"req-wide",
		json.RawMessage(`{"questions":[{"question":"q"}]}`),
	)
	wideH := mWide.chatPanelHeight()
	if wideH != fixedEstimate {
		t.Errorf("wide chatPanelHeight() = %d, want %d (no wrapping at 120 cols)", wideH, fixedEstimate)
	}
}

func TestAttachModel_QuestionPanelWrappedOptionScrolling(t *testing.T) {
	// Verify that wrapped options correctly trigger scrolling when they
	// exceed the available panel space at a narrow width.
	mv := mocks.NewMockSessionView("sess-wrap-scroll", "feat-1")

	// Narrow width: contentW = max(30-2,22) - 4 = 24
	m := attachModelFromSession(mv, 30, 40)

	// Create many options with long labels to exceed 20-line panel cap.
	var opts []askUserOption
	for i := 0; i < 6; i++ {
		opts = append(opts, askUserOption{
			Label:       fmt.Sprintf("Option %d with a very long label that will definitely wrap at narrow width", i+1),
			Description: "This description is also quite long and should wrap to multiple lines at narrow terminal widths",
		})
	}

	m.activateAskUserQuestions(
		[]askUserQuestion{{
			Question: "Pick one:",
			Options:  opts,
		}},
		"req-scroll",
		json.RawMessage(`{"questions":[{"question":"q"}]}`),
	)

	contentW := m.questionContentWidth()

	// Each option should take multiple lines when wrapped.
	opt0Lines := questionOptionLineCount(opts[0], 0, contentW)
	if opt0Lines <= 2 {
		t.Fatalf("option 0 should take >2 lines at width %d, got %d", contentW, opt0Lines)
	}

	// chatPanelHeight should be capped at 20.
	chatH := m.chatPanelHeight()
	if chatH > 20 {
		t.Errorf("chatPanelHeight() = %d, should be capped at 20", chatH)
	}

	// Not all options should be visible (scrolling should be needed).
	start, end, _, needBelow := m.questionVisibleWindow()
	if !needBelow {
		t.Error("questionVisibleWindow: expected needBelow=true with many wrapped options")
	}
	if end-start >= 6 {
		t.Errorf("questionVisibleWindow: all 6 options visible (start=%d, end=%d), expected fewer due to wrapping", start, end)
	}
}

func TestAttachModel_QuestionContentWidthAccountsForBorders(t *testing.T) {
	// Regression: questionContentWidth must subtract both box borders (2) and
	// padding (2) from panelW. If it only subtracts padding, text that wraps
	// in the real rendered box is measured as fitting on one line, clipping
	// the lower part of the question panel.
	//
	// At terminal width 30: panelW = max(30-2,22) = 28.
	// Correct contentW = 28 - 4 = 24. Wrong (border-unaware) = 28 - 2 = 26.
	// A 25-char string wraps at 24 but NOT at 26, so this test fails if the
	// width drifts back to the old over-estimate.
	mv := mocks.NewMockSessionView("sess-border", "feat-1")
	m := attachModelFromSession(mv, 30, 40)

	contentW := m.questionContentWidth()
	if contentW != 24 {
		t.Fatalf("questionContentWidth() = %d, want 24 (panelW 28 - border 2 - padding 2)", contentW)
	}

	// 25-char string: wraps at width 24 (correct) but fits at width 26 (wrong).
	text25 := "abcdefghij klmnopqr uvwxy" // exactly 25 chars
	linesCorrect := wrappedLineCount(text25, 24)
	linesWrong := wrappedLineCount(text25, 26)
	if linesCorrect <= 1 {
		t.Fatalf("text %q should wrap at width 24, got %d lines", text25, linesCorrect)
	}
	if linesWrong != 1 {
		t.Fatalf("text %q should NOT wrap at width 26, got %d lines", text25, linesWrong)
	}

	// Wire the text as a question and verify panel height reflects the wrap.
	m.activateAskUserQuestions(
		[]askUserQuestion{{
			Question: text25,
			Options: []askUserOption{
				{Label: "A", Description: "first"},
				{Label: "B", Description: "second"},
			},
		}},
		"req-border",
		json.RawMessage(`{"questions":[{"question":"q"}]}`),
	)

	chatH := m.chatPanelHeight()
	// With 2-line question text: overhead = 2 + 5 = 7, options = 2*2 = 4 → 11.
	// With 1-line (wrong): overhead = 1 + 5 = 6, options = 4 → 10.
	if chatH < 11 {
		t.Errorf("chatPanelHeight() = %d, want >= 11 (question should wrap at true content width)", chatH)
	}
}

func TestAttachModelFromSession_PopulatesSingleTab(t *testing.T) {
	sess := mocks.NewMockSessionView("sess-1", "feat-1")
	sess.PermCacheScopeVal = "repo-a"
	sess.RepoNameVal = "repo-a"
	m := attachModelFromSession(sess, 80, 24)

	if len(m.repoTabs) != 1 {
		t.Fatalf("repoTabs length = %d, want 1", len(m.repoTabs))
	}
	if m.repoTabs[0].sess != sess {
		t.Errorf("repoTabs[0].sess != input session")
	}
	if m.activeTabIdx != 0 {
		t.Errorf("activeTabIdx = %d, want 0", m.activeTabIdx)
	}
	if m.featureID != "feat-1" {
		t.Errorf("featureID = %q, want feat-1", m.featureID)
	}
	if m.sess != sess {
		t.Errorf("sess != input session")
	}
}

func TestNewAttachModel_WithSingleTab_RendersWithoutTabBar(t *testing.T) {
	sess := mocks.NewMockSessionView("sess-1", "feat-1")
	sess.RepoNameVal = "repo-a"
	tabs := []repoTab{{repoName: "repo-a", sess: sess, status: statusPending}}
	m := NewAttachModel(tabs, 0, "feat-1", 80, 24)
	if got := m.renderTabBar(80); got != "" {
		t.Errorf("renderTabBar() = %q, want \"\" for single-tab attach (preserves classic look)", got)
	}
}

func TestNewAttachModel_WithMultipleTabs_RendersTabBar(t *testing.T) {
	sess1 := mocks.NewMockSessionView("sess-1", "feat-1")
	sess1.RepoNameVal = "alpha-repo"
	sess2 := mocks.NewMockSessionView("sess-2", "feat-1")
	sess2.RepoNameVal = "beta-repo"
	tabs := []repoTab{
		{repoName: "alpha-repo", sess: sess1, status: statusImplementing},
		{repoName: "beta-repo", sess: sess2, status: statusImplementing},
	}
	m := NewAttachModel(tabs, 0, "feat-1", 80, 24)
	got := m.renderTabBar(80)
	if got == "" {
		t.Fatal("renderTabBar() = \"\", want non-empty for multi-tab attach")
	}
	// Tab bar renders abbreviated repo names with a separator between tabs;
	// look for both abbreviation prefixes (`alpha`, `beta`) instead of full names.
	if !strings.Contains(got, "alpha") {
		t.Errorf("renderTabBar() = %q, want it to contain alpha (first tab)", got)
	}
	if !strings.Contains(got, "beta") {
		t.Errorf("renderTabBar() = %q, want it to contain beta (second tab)", got)
	}
}

func TestAbbreviateRepoName(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"services-protobuf", "proto"},
		{"graph-runner", "graph"},
		{"taulu", "taulu"},
		{"my-long-repo-name", "my-lo"},
		{"a", "a"},
		{"identity-service", "ident"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := abbreviateRepoName(tt.name)
			if got != tt.want {
				t.Errorf("abbreviateRepoName(%q) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}
