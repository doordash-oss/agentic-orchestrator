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

// TouchWithinRootsHandler auto-approves a plain, unflagged `touch <path>...`
// Bash command when every target path falls within Roots, deferring to Inner
// for everything else. `touch` can only create an empty file or bump an
// existing one's mtime — the same effect Edit/Write already have when
// AcceptEditsHandler approves them unconditionally — so this closes a gap
// where the harness prompts for something like a `phase_complete` marker
// while the equivalent empty Write to that exact path would go through
// silently.
//
// Unlike Edit/Write, OpenCode has no per-path scoping for Bash to fall back
// on: its declarative permission map treats "bash" as a single ask/allow/deny
// decision, never a per-path glob (see internal/llm/opencode/config.go). So
// Roots here is the only boundary standing between an approval and letting a
// shell command anywhere on disk through — a touch outside it always defers
// to Inner rather than being approved by default.
type TouchWithinRootsHandler struct {
	Inner ports.PermissionHandler
	Roots []string
}

// CanUseTool approves an in-root touch; everything else defers to Inner.
func (h *TouchWithinRootsHandler) CanUseTool(req ports.ToolPermissionRequest) (ports.PermissionDecision, error) {
	if req.ToolName == "Bash" {
		if targets, ok := touchCommandTargets(req.Input); ok && allPathsWithinRoots(targets, h.Roots) {
			return ports.PermissionDecision{Behavior: "allow"}, nil
		}
	}
	return h.Inner.CanUseTool(req)
}

// touchCommandTargets parses a Bash command as a plain `touch <path>...`
// invocation with no flags, chaining, redirection, or substitution, returning
// its literal path arguments. Returns ok=false for anything else, including a
// touch with flags: options like -r/-t change what the command does to files
// outside its argument list, which is outside what this narrow exception is
// meant to cover.
func touchCommandTargets(input string) ([]string, bool) {
	command := strings.TrimSpace(extractBashCommand(input))
	if command == "" {
		return nil, false
	}
	if strings.ContainsAny(command, "\n\r;`<>|&") || strings.Contains(command, "$(") {
		return nil, false
	}
	fields := strings.Fields(command)
	if len(fields) < 2 || filepath.Base(fields[0]) != "touch" {
		return nil, false
	}
	targets := fields[1:]
	for _, t := range targets {
		if strings.HasPrefix(t, "-") {
			return nil, false
		}
	}
	return targets, true
}

// allPathsWithinRoots reports whether every path is exactly one of roots or a
// descendant of one. Returns false for an empty paths list so a touch with no
// targets never matches.
func allPathsWithinRoots(paths, roots []string) bool {
	if len(paths) == 0 {
		return false
	}
	for _, p := range paths {
		if !pathWithinRoots(p, roots) {
			return false
		}
	}
	return true
}

// pathWithinRoots reports whether path is exactly one of roots or a
// descendant of one, comparing absolute forms so a root supplied as a
// relative path still matches consistently.
func pathWithinRoots(path string, roots []string) bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	for _, root := range roots {
		rootAbs, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		if abs == rootAbs || strings.HasPrefix(abs, rootAbs+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// WrapGeneralPhaseHandlerWithTouch wraps h in a TouchWithinRootsHandler when h
// is one of the general accept-edits handlers permHandlerFor builds
// (AutoApproveHandler, AcceptEditsHandler, or CachingHandler over one of
// those — optionally already wrapped in a SizeGuardHandler via Guarded).
// Any other handler shape — bounded helpers, plan/rewind/review-feedback
// sessions, chat's ReadOnlyHandler — is returned unchanged, since those exist
// specifically to restrict writes below the general writable-root policy and
// must not be loosened by this exception.
func WrapGeneralPhaseHandlerWithTouch(h ports.PermissionHandler, roots []string) ports.PermissionHandler {
	if len(roots) == 0 || !isGeneralPhaseHandler(h) {
		return h
	}
	return &TouchWithinRootsHandler{Inner: h, Roots: roots}
}

// isGeneralPhaseHandler reports whether h is, optionally through a
// SizeGuardHandler, one of the handler types permHandlerFor builds.
func isGeneralPhaseHandler(h ports.PermissionHandler) bool {
	if guard, ok := h.(*SizeGuardHandler); ok {
		h = guard.Inner
	}
	switch h.(type) {
	case *AutoApproveHandler, *AcceptEditsHandler, *CachingHandler:
		return true
	default:
		return false
	}
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
// conservative read-only shell probes, read-only sub-agent spawning, and file
// edits only for the exact artifact paths declared by the harness. Mutating
// shells and undeclared file writes are hard-denied so bounded helpers cannot
// mutate the worktree or escape their helper directory.
type BoundedHelperArtifactHandler struct {
	AllowedPaths []string
	// Sandboxed indicates the helper process itself runs under an OS filesystem
	// sandbox that makes the reviewed worktree read-only at the kernel layer. When
	// set, shell access is unrestricted (any worktree write fails as an ordinary
	// non-zero shell result the model absorbs) instead of gated by the read-only
	// command allowlist.
	Sandboxed bool
}

// CanUseTool approves reads and exact declared artifact writes.
func (h *BoundedHelperArtifactHandler) CanUseTool(req ports.ToolPermissionRequest) (ports.PermissionDecision, error) {
	switch req.ToolName {
	case "Read", "Glob", "Grep", "LS", "LSP", "ExternalDirectory", "WebSearch", "WebFetch":
		return ports.PermissionDecision{Behavior: "allow"}, nil

	// Sub-agent spawning — auto-approve. Spawned sub-agents inherit the
	// provider's depth-1 profile (no sub-sub-agents) and cannot mutate the
	// worktree, matching the research-phase treatment.
	case "Agent":
		return ports.PermissionDecision{Behavior: "allow"}, nil

	case "Bash":
		if h.Sandboxed || boundedHelperReadOnlyBashAllowed(req.Input) {
			return ports.PermissionDecision{Behavior: "allow"}, nil
		}
		return ports.PermissionDecision{
			Behavior: "deny",
			Reason:   "bounded helper shell access is limited to read-only inspection commands",
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
		Reason:   "bounded helper may not mutate undeclared files",
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

// boundedHelperReadOnlyBashAllowed reports whether a bounded helper's bash
// command is a conservative read-only inspection: no chaining/redirection/
// substitution metacharacters, and every &&/||/| segment is a known read-only
// program. Sandboxed helpers bypass this via Sandboxed.
func boundedHelperReadOnlyBashAllowed(input string) bool {
	command := strings.TrimSpace(extractBashCommand(input))
	if command == "" {
		return false
	}
	command = stripReadOnlyShellRedirections(command)
	if strings.ContainsAny(command, "\n\r;`<>") || strings.Contains(command, "$(") {
		return false
	}
	parts := splitReadOnlyShellSegments(command)
	if len(parts) == 0 {
		return false
	}
	for _, part := range parts {
		if !readOnlySimpleCommandAllowed(part) {
			return false
		}
	}
	return true
}

func splitReadOnlyShellSegments(command string) []string {
	normalized := strings.NewReplacer(
		"&&", "\x00",
		"||", "\x00",
		"|", "\x00",
	).Replace(command)
	rawParts := strings.Split(normalized, "\x00")
	parts := make([]string, 0, len(rawParts))
	for _, part := range rawParts {
		part = strings.TrimSpace(part)
		if part != "" {
			parts = append(parts, part)
		}
	}
	return parts
}

func readOnlySimpleCommandAllowed(command string) bool {
	if strings.Contains(command, "&") {
		return false
	}
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return false
	}
	name := filepath.Base(fields[0])
	switch name {
	case "cd", "pwd", "ls", "cat", "head", "tail", "wc", "grep", "rg", "echo", "true", "false", "sort", "uniq", "cut", "tr":
		return true
	case "test", "[":
		return true
	case "find":
		return !hasAnyShellToken(fields[1:], "-delete", "-exec", "-execdir", "-ok", "-okdir")
	case "sed":
		return !hasSedInPlaceFlag(fields[1:])
	case "git":
		return gitReadOnlySubcommandAllowed(fields[1:])
	default:
		return false
	}
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
