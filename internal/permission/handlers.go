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

package permission

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// DefaultWriteGuardBytes is the byte threshold above which the SizeGuardHandler
// denies Claude `Write` tool calls. Large Writes (observed at ~50KB, sometimes
// smaller) have repeatedly hung the server-side tool call for minutes and then
// dropped the turn with nothing written to disk. 20KB is a conservative limit
// that matches the in-prompt directive in internal/agent/phase.go.
const DefaultWriteGuardBytes = 20_000

// SizeGuardHandler denies Claude `Write` tool calls whose `content` payload
// exceeds MaxBytes, returning an actionable message that nudges the model
// toward the skeleton-then-Edit pattern. This is a structural enforcement of
// the write-vs-edit directive in internal/agent/phase.go: prompt guidance
// alone has proven insufficient.
//
// Only fires for ProviderName == "claude". Codex routes through the same
// permission pipeline but has not exhibited the hang, so we leave it alone.
//
// Set MaxBytes to 0 to disable the guard entirely (useful in tests).
type SizeGuardHandler struct {
	Inner    ports.PermissionHandler
	MaxBytes int
}

// CanUseTool enforces the Write payload limit for Claude, then delegates.
func (h *SizeGuardHandler) CanUseTool(req ports.ToolPermissionRequest) (ports.PermissionDecision, error) {
	if h.MaxBytes > 0 && req.ProviderName == "claude" && req.ToolName == "Write" {
		if n, ok := writeContentSize(req.Input); ok && n > h.MaxBytes {
			return ports.PermissionDecision{
				Behavior: "deny",
				Reason: fmt.Sprintf(
					"Write blocked: content payload %d bytes exceeds %d-byte limit. "+
						"Large Writes have repeatedly hung the Claude tool-call stream and dropped the turn with nothing written. "+
						"Instead: use Write to create a small skeleton (<5KB) containing only section headings and short `<!-- SECTION -->` placeholders, "+
						"then fill in each section with a separate Edit call. If a single Edit's new_string would still exceed this limit, split it across multiple Edit calls against successive anchor points.",
					n, h.MaxBytes),
			}, nil
		}
	}
	return h.Inner.CanUseTool(req)
}

// writeContentSize extracts the byte length of the "content" field from a
// JSON-encoded Write tool input. Returns (0, false) if the input is not valid
// JSON or lacks a string "content" field — in that case the guard defers to
// the inner handler rather than blocking on a parse failure.
func writeContentSize(raw string) (int, bool) {
	var payload struct {
		Content *string `json:"content"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil || payload.Content == nil {
		return 0, false
	}
	return len(*payload.Content), true
}

// Guarded wraps inner in a SizeGuardHandler using DefaultWriteGuardBytes.
// Use this at every site that constructs a permission handler for a session
// so oversized Claude Writes get rejected uniformly.
func Guarded(inner ports.PermissionHandler) ports.PermissionHandler {
	return &SizeGuardHandler{Inner: inner, MaxBytes: DefaultWriteGuardBytes}
}

// AutoApproveHandler approves all tool use requests immediately.
type AutoApproveHandler struct{}

// CanUseTool always returns allow.
func (h *AutoApproveHandler) CanUseTool(_ ports.ToolPermissionRequest) (ports.PermissionDecision, error) {
	return ports.PermissionDecision{Behavior: "allow"}, nil
}

// AcceptEditsHandler auto-approves read-only tools and file edit/write tools,
// but leaves everything else (Bash, Agent, etc.) for the TUI to prompt.
// Returning an empty Behavior ("") signals that the handler declines to decide
// and the request should be surfaced to the user.
type AcceptEditsHandler struct{}

// CanUseTool approves reads and file edits; defers everything else.
func (h *AcceptEditsHandler) CanUseTool(req ports.ToolPermissionRequest) (ports.PermissionDecision, error) {
	switch req.ToolName {
	// Read-only tools — always safe
	case "Read", "Glob", "Grep", "LS", "LSP", "ExternalDirectory",
		"WebSearch", "WebFetch",
		"TodoWrite", "TaskCreate", "TaskGet", "TaskList", "TaskUpdate":
		return ports.PermissionDecision{Behavior: "allow"}, nil

	// File modification tools — auto-approve (matches Claude Code acceptEdits)
	case "Edit", "Write", "NotebookEdit":
		return ports.PermissionDecision{Behavior: "allow"}, nil

	// Agent spawning — auto-approve (subagents are sandboxed)
	case "Agent":
		return ports.PermissionDecision{Behavior: "allow"}, nil
	}

	// Everything else (Bash, etc.) — defer to TUI
	return ports.PermissionDecision{}, nil
}

// PlanReviewHandler auto-approves read-only tools and file edits ONLY to the
// specified plan file path. All other write operations are hard-denied.
type PlanReviewHandler struct {
	AllowedPath string // absolute path to the plan file that may be edited
}

// CanUseTool approves reads and edits to the plan file; defers everything else.
func (h *PlanReviewHandler) CanUseTool(req ports.ToolPermissionRequest) (ports.PermissionDecision, error) {
	switch req.ToolName {
	// Read-only tools — always safe
	case "Read", "Glob", "Grep", "LS", "LSP", "ExternalDirectory",
		"WebSearch", "WebFetch",
		"TodoWrite", "TaskCreate", "TaskGet", "TaskList", "TaskUpdate":
		return ports.PermissionDecision{Behavior: "allow"}, nil

	// Agent spawning — auto-approve (subagents are sandboxed)
	case "Agent":
		return ports.PermissionDecision{Behavior: "allow"}, nil

	// File modification tools — only auto-approve for the plan file
	case "Edit", "Write", "NotebookEdit":
		if h.AllowedPath != "" && toolInputContainsPath(req.Input, h.AllowedPath) {
			return ports.PermissionDecision{Behavior: "allow"}, nil
		}
		return ports.PermissionDecision{Behavior: "deny", Reason: "only the plan file may be modified during plan review"}, nil
	}

	// Everything else (Bash, etc.) — defer to TUI
	return ports.PermissionDecision{}, nil
}

// toolInputContainsPath checks if the JSON-encoded tool input references the given file path.
func toolInputContainsPath(input, allowedPath string) bool {
	// The input is JSON with a "file_path" field for Edit/Write/NotebookEdit.
	// Simple substring check is sufficient since paths are absolute.
	return strings.Contains(input, allowedPath)
}

// ReadOnlyHandler auto-approves read-only tools and Agent spawning,
// but hard-denies all file modification tools. Used by the "Ask me Anything" chat.
type ReadOnlyHandler struct{}

// CanUseTool approves reads; denies all writes.
func (h *ReadOnlyHandler) CanUseTool(req ports.ToolPermissionRequest) (ports.PermissionDecision, error) {
	switch req.ToolName {
	// Read-only tools — always safe
	case "Read", "Glob", "Grep", "LS", "LSP", "ExternalDirectory",
		"WebSearch", "WebFetch",
		"TodoWrite", "TaskCreate", "TaskGet", "TaskList", "TaskUpdate":
		return ports.PermissionDecision{Behavior: "allow"}, nil

	// Agent spawning — auto-approve (subagents are sandboxed)
	case "Agent":
		return ports.PermissionDecision{Behavior: "allow"}, nil

	// File modification tools — hard deny
	case "Edit", "Write", "NotebookEdit":
		return ports.PermissionDecision{Behavior: "deny", Reason: "chat is read-only"}, nil
	}

	// Everything else (Bash, etc.) — hard deny for safety
	return ports.PermissionDecision{Behavior: "deny", Reason: "chat is read-only"}, nil
}

// ReviewFeedbackHandler auto-approves read-only tools and file edits ONLY to
// the specified review-feedback.md path. All other write operations are
// hard-denied. Used by the bounded review/validation helper so the reviewer
// can produce its structured handoff file (the harness then parses the file
// deterministically) while still being prevented from touching the rest of
// the worktree.
//
// Structurally identical to PlanReviewHandler / RewindReviewHandler; the type
// is kept distinct so the deny-reason text and the wiring site stay
// self-documenting (a stack trace through ReviewFeedbackHandler.CanUseTool
// makes the call site obvious).
type ReviewFeedbackHandler struct {
	AllowedPath string // absolute path to the review-feedback.md file the reviewer must produce
}

// CanUseTool approves reads and edits to the review-feedback file; denies
// every other write or shell tool so the reviewer cannot mutate the worktree.
func (h *ReviewFeedbackHandler) CanUseTool(req ports.ToolPermissionRequest) (ports.PermissionDecision, error) {
	switch req.ToolName {
	case "Read", "Glob", "Grep", "LS", "LSP", "ExternalDirectory",
		"WebSearch", "WebFetch",
		"TodoWrite", "TaskCreate", "TaskGet", "TaskList", "TaskUpdate":
		return ports.PermissionDecision{Behavior: "allow"}, nil

	case "Agent":
		return ports.PermissionDecision{Behavior: "allow"}, nil

	case "Edit", "Write", "NotebookEdit":
		if h.AllowedPath != "" && toolInputContainsPath(req.Input, h.AllowedPath) {
			return ports.PermissionDecision{Behavior: "allow"}, nil
		}
		return ports.PermissionDecision{Behavior: "deny", Reason: "review helper may only write the review-feedback.md handoff file"}, nil
	}

	return ports.PermissionDecision{Behavior: "deny", Reason: "review helper is read-only except for review-feedback.md"}, nil
}

// BoundedHelperArtifactHandler auto-approves read-only inspection tools,
// conservative read-only shell probes, and file edits only for the exact
// artifact paths declared by the harness. Mutating shells, subagents, and
// undeclared file writes are hard-denied so bounded helpers cannot mutate the
// worktree or escape their helper directory.
type BoundedHelperArtifactHandler struct {
	AllowedPaths []string
}

// CanUseTool approves reads and exact declared artifact writes.
func (h *BoundedHelperArtifactHandler) CanUseTool(req ports.ToolPermissionRequest) (ports.PermissionDecision, error) {
	switch req.ToolName {
	case "Read", "Glob", "Grep", "LS", "LSP", "ExternalDirectory", "WebSearch", "WebFetch":
		return ports.PermissionDecision{Behavior: "allow"}, nil

	case "Bash":
		if h.bashAllowed(req.Input) {
			return ports.PermissionDecision{Behavior: "allow"}, nil
		}
		return ports.PermissionDecision{
			Behavior: "deny",
			Reason:   "bounded helper shell is limited to read-only inspection and writes to its declared artifacts",
		}, nil

	case "Edit", "Write", "NotebookEdit":
		path, ok := toolInputFilePath(req.Input)
		if ok && h.pathAllowed(path) {
			return ports.PermissionDecision{Behavior: "allow"}, nil
		}
		return ports.PermissionDecision{
			Behavior: "deny",
			Reason:   "bounded helper may write only declared artifacts in its helper directory",
		}, nil
	}

	return ports.PermissionDecision{
		Behavior: "deny",
		Reason:   "bounded helper may not spawn agents or mutate undeclared files",
	}, nil
}

func (h *BoundedHelperArtifactHandler) pathAllowed(path string) bool {
	cleaned, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return false
	}
	for _, allowed := range h.AllowedPaths {
		allowedAbs, err := filepath.Abs(filepath.Clean(allowed))
		if err == nil && cleaned == allowedAbs {
			return true
		}
	}
	return false
}

func toolInputFilePath(input string) (string, bool) {
	var payload struct {
		FilePath string `json:"file_path"`
	}
	if err := json.Unmarshal([]byte(input), &payload); err != nil || payload.FilePath == "" {
		return "", false
	}
	return payload.FilePath, true
}

// boundedHelperPreflightAllowed reports whether the command is the read-only
// artifact preflight the bounded-helper prompt instructs: a single invocation of
// $AGENTICO_BIN (or its absolute path) with the validate-artifacts subcommand,
// which only inspects the helper's own output. Any other invocation stays denied.
func boundedHelperPreflightAllowed(input string) bool {
	command := strings.TrimSpace(extractBashCommand(input))
	if command == "" {
		return false
	}
	if strings.ContainsAny(command, "\n\r;|&<>`") || strings.Contains(command, "$(") {
		return false
	}
	fields := strings.Fields(command)
	if len(fields) < 2 {
		return false
	}
	prog := strings.Trim(fields[0], `"'`)
	if prog != "$AGENTICO_BIN" && !strings.HasPrefix(prog, "/") {
		return false
	}
	return fields[1] == "validate-artifacts"
}

func stripReadOnlyShellRedirections(command string) string {
	replacer := strings.NewReplacer(
		"2>/dev/null", "",
		"2> /dev/null", "",
		"2>&1", "",
	)
	return replacer.Replace(command)
}

func hasAnyShellToken(fields []string, denied ...string) bool {
	for _, field := range fields {
		for _, token := range denied {
			if field == token {
				return true
			}
		}
	}
	return false
}

func hasSedInPlaceFlag(fields []string) bool {
	for _, field := range fields {
		if field == "-i" || strings.HasPrefix(field, "-i.") || strings.HasPrefix(field, "-i'") || strings.HasPrefix(field, `-i"`) {
			return true
		}
	}
	return false
}

func gitReadOnlySubcommandAllowed(fields []string) bool {
	if len(fields) == 0 {
		return false
	}
	switch fields[0] {
	case "status", "diff", "log", "show", "ls-files", "rev-parse", "branch":
		return true
	default:
		return false
	}
}

// RewindReviewHandler auto-approves read-only tools and file edits ONLY to the
// specified artifact file path. All other write operations are hard-denied.
// Used during rewind review sessions to let the user modify the previous phase's output.
type RewindReviewHandler struct {
	AllowedPath string // absolute path to the artifact file that may be edited
}

// CanUseTool approves reads and edits to the artifact file; defers everything else.
func (h *RewindReviewHandler) CanUseTool(req ports.ToolPermissionRequest) (ports.PermissionDecision, error) {
	switch req.ToolName {
	// Read-only tools — always safe
	case "Read", "Glob", "Grep", "LS", "LSP", "ExternalDirectory",
		"WebSearch", "WebFetch",
		"TodoWrite", "TaskCreate", "TaskGet", "TaskList", "TaskUpdate":
		return ports.PermissionDecision{Behavior: "allow"}, nil

	// Agent spawning — auto-approve (subagents are sandboxed)
	case "Agent":
		return ports.PermissionDecision{Behavior: "allow"}, nil

	// File modification tools — only auto-approve for the allowed artifact file
	case "Edit", "Write", "NotebookEdit":
		if h.AllowedPath != "" && toolInputContainsPath(req.Input, h.AllowedPath) {
			return ports.PermissionDecision{Behavior: "allow"}, nil
		}
		return ports.PermissionDecision{Behavior: "deny", Reason: "only the artifact file may be modified during rewind review"}, nil
	}

	// Everything else (Bash, etc.) — defer to TUI
	return ports.PermissionDecision{}, nil
}

// DenyAllHandler denies all tool use requests.
type DenyAllHandler struct{}

// CanUseTool always returns deny.
func (h *DenyAllHandler) CanUseTool(_ ports.ToolPermissionRequest) (ports.PermissionDecision, error) {
	return ports.PermissionDecision{Behavior: "deny", Reason: "all tools denied"}, nil
}
