package opencode

import (
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// toolProgressRecords feeds ACP session/update payloads through the protocol
// and returns every tool_progress record emitted, in order.
func toolProgressRecords(t *testing.T, updates ...map[string]any) []llm.ToolProgressMessage {
	t.Helper()
	p, _, _ := newPostHandshakeProtocol(t)
	var out []llm.ToolProgressMessage
	for _, update := range updates {
		for _, m := range mustParse(t, p, notificationLine(t, "session/update", map[string]any{
			"sessionId": "ses_x",
			"update":    update,
		})) {
			if m.ToolProgress != nil {
				out = append(out, *m.ToolProgress)
			}
		}
	}
	return out
}

// opencodeToolSequence mirrors OpenCode's ACP agent: a pending tool_call
// titled with the tool id, an in_progress update with the real input, and a
// completed update whose title is the tool's display string.
func opencodeToolSequence(id, tool, kind string, rawInput map[string]any, locations []map[string]any, completedTitle string) []map[string]any {
	return []map[string]any{
		{"sessionUpdate": "tool_call", "toolCallId": id, "title": tool, "kind": kind, "status": "pending", "locations": []any{}, "rawInput": map[string]any{}},
		{"sessionUpdate": "tool_call_update", "toolCallId": id, "title": tool, "kind": kind, "status": "in_progress", "locations": locations, "rawInput": rawInput},
		// A repeated in_progress update carrying nothing new.
		{"sessionUpdate": "tool_call_update", "toolCallId": id, "title": tool, "kind": kind, "status": "in_progress", "locations": locations, "rawInput": rawInput},
		{"sessionUpdate": "tool_call_update", "toolCallId": id, "title": completedTitle, "kind": kind, "status": "completed", "rawInput": rawInput},
	}
}

func TestToolProgress_OpenCodeSequencesKeepToolNameAndCollapseRepeats(t *testing.T) {
	const skill = "/Users/dev/.agentic-orchestrator/skills/review/SKILL.md"
	cases := []struct {
		name        string
		updates     []map[string]any
		wantTool    string
		wantSummary string
	}{
		{
			name: "read",
			updates: opencodeToolSequence("call_read", "read", ToolKindRead,
				map[string]any{"filePath": skill},
				[]map[string]any{{"path": skill}},
				strings.TrimPrefix(skill, "/")),
			wantTool:    "Read",
			wantSummary: "File: " + skill,
		},
		{
			name: "grep",
			updates: opencodeToolSequence("call_grep", "grep", ToolKindSearch,
				map[string]any{"pattern": "TODO|FIXME|XXX|HACK", "path": "/repo"},
				nil,
				"TODO|FIXME|XXX|HACK"),
			wantTool:    "Grep",
			wantSummary: "TODO|FIXME|XXX|HACK",
		},
		{
			name: "bash",
			updates: opencodeToolSequence("call_bash", "bash", ToolKindExecute,
				map[string]any{"command": "cat RULES.md", "description": "Review Rules"},
				nil,
				"Review Rules"),
			wantTool:    "Bash",
			wantSummary: "Review Rules",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			records := toolProgressRecords(t, tc.updates...)
			// pending, in_progress with input, completed; the repeat collapses.
			if len(records) != 3 {
				t.Fatalf("records = %+v, want 3 (pending, in_progress, completed)", records)
			}
			for i, r := range records {
				if r.ToolName != tc.wantTool {
					t.Fatalf("record %d tool = %q, want %q", i, r.ToolName, tc.wantTool)
				}
			}
			final := records[2].Data
			if !strings.Contains(final, tc.wantSummary) || !strings.Contains(final, "Status: completed") {
				t.Fatalf("completed data = %q, want summary %q and completed status", final, tc.wantSummary)
			}
			if strings.Count(final, strings.TrimPrefix(tc.wantSummary, "File: /")) != 1 {
				t.Fatalf("completed data = %q repeats the summary", final)
			}
		})
	}
}

func TestToolProgress_PendingToInProgressWithoutNewInfoCollapses(t *testing.T) {
	records := toolProgressRecords(t,
		map[string]any{"sessionUpdate": "tool_call", "toolCallId": "call_1", "title": "read", "kind": "read", "status": "pending"},
		map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "call_1", "title": "read", "kind": "read", "status": "in_progress"},
	)
	if len(records) != 1 || records[0].ToolName != "Read" {
		t.Fatalf("records = %+v, want one Read record", records)
	}
}

func TestToolProgress_UnknownToolKeepsIdentifier(t *testing.T) {
	records := toolProgressRecords(t,
		map[string]any{"sessionUpdate": "tool_call", "toolCallId": "call_mcp", "title": "context7_resolve-library-id", "kind": "other", "status": "pending"},
		map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "call_mcp", "title": "Resolve react", "kind": "other", "status": "completed"},
	)
	if len(records) != 2 {
		t.Fatalf("records = %+v, want pending and completed", records)
	}
	for _, r := range records {
		if r.ToolName != "context7_resolve-library-id" {
			t.Fatalf("tool = %q, want the MCP tool identifier", r.ToolName)
		}
	}
}
