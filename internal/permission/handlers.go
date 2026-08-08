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
	"slices"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// providerNameClaude identifies the Claude CLI provider in ports.ToolPermissionRequest.ProviderName.
const providerNameClaude = "claude"

// readOnlyToolNames lists the tools every general-purpose handler treats as
// always safe to auto-approve: read-only inspection plus todo/task bookkeeping.
var readOnlyToolNames = map[string]bool{
	"Read": true, "Glob": true, "Grep": true, "LS": true, "LSP": true, "ExternalDirectory": true,
	"WebSearch": true, "WebFetch": true,
	"TodoWrite": true, "TaskCreate": true, "TaskGet": true, "TaskList": true, "TaskUpdate": true,
}

// isReadOnlyTool reports whether name is one of readOnlyToolNames.
func isReadOnlyTool(name string) bool {
	return readOnlyToolNames[name]
}

// DefaultWriteGuardBytes is the byte threshold above which the SessionGuardHandler
// denies Claude `Write` tool calls. Large Writes (observed at ~50KB, sometimes
// smaller) have repeatedly hung the server-side tool call for minutes and then
// dropped the turn with nothing written to disk. 20KB is a conservative limit
// that matches the in-prompt directive in internal/agent/phase.go.
const DefaultWriteGuardBytes = 20_000

// SessionGuardHandler denies Claude `Write` tool calls whose `content` payload
// exceeds MaxWriteBytes, returning an actionable message that nudges the model
// toward the skeleton-then-Edit pattern. This is a structural enforcement of
// the write-vs-edit directive in internal/agent/phase.go: prompt guidance
// alone has proven insufficient.
//
// Only fires for ProviderName == "claude". Codex routes through the same
// permission pipeline but has not exhibited the hang, so we leave it alone.
//
// Set MaxWriteBytes to 0 to disable the guard entirely (useful in tests).
type SessionGuardHandler struct {
	Inner         ports.PermissionHandler
	MaxWriteBytes int
}

// CanUseTool protects harness-owned protocol state, enforces the Claude Write
// payload limit, then delegates.
func (h *SessionGuardHandler) CanUseTool(req ports.ToolPermissionRequest) (ports.PermissionDecision, error) {
	if requestsCompletionReceiptMutation(req) {
		return ports.PermissionDecision{
			Behavior: DecisionDeny,
			Reason:   "phase_complete is a harness-owned completion receipt; emit the structured root outcome instead",
		}, nil
	}
	if requestsHarnessFileMutation(req, testingContractFilename) {
		return ports.PermissionDecision{
			Behavior: DecisionDeny,
			Reason:   "testing-contract.yaml is harness-owned and regenerated every iteration; amend the phase plan's verification section instead",
		}, nil
	}
	if h.MaxWriteBytes > 0 && req.ProviderName == providerNameClaude && req.ToolName == toolNameWrite {
		if n, ok := writeContentSize(req.Input); ok && n > h.MaxWriteBytes {
			return ports.PermissionDecision{
				Behavior: DecisionDeny,
				Reason: fmt.Sprintf(
					"Write blocked: content payload %d bytes exceeds %d-byte limit. "+
						"Large Writes have repeatedly hung the Claude tool-call stream and dropped the turn with nothing written. "+
						"Instead: use Write to create a small skeleton (<5KB) containing only section headings and short `<!-- SECTION -->` placeholders, "+
						"then fill in each section with a separate Edit call. If a single Edit's new_string would still exceed this limit, split it across multiple Edit calls against successive anchor points.",
					n, h.MaxWriteBytes),
			}, nil
		}
	}
	return h.Inner.CanUseTool(req)
}

const (
	completionReceiptFilename = "phase_complete"
	testingContractFilename   = "testing-contract.yaml"
)

func requestsCompletionReceiptMutation(req ports.ToolPermissionRequest) bool {
	return requestsHarnessFileMutation(req, completionReceiptFilename)
}

func requestsHarnessFileMutation(req ports.ToolPermissionRequest, filename string) bool {
	switch req.ToolName {
	case toolNameEdit, toolNameWrite, toolNameNotebookEdit:
		if path, ok := toolInputFilePath(req.Input); ok {
			return filepath.Base(filepath.Clean(path)) == filename
		}
		return strings.Contains(req.Input, filename)
	case toolNameBash:
		// Bash is write-capable and shell syntax is intentionally not parsed
		// here. Deny any reference through Bash; read-only inspection
		// remains available through the Read tool.
		return strings.Contains(extractBashCommand(req.Input), filename)
	default:
		return false
	}
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

// Guarded wraps inner in a SessionGuardHandler. Every guarded session rejects
// agent writes to the harness completion receipt and the harness-owned
// testing contract; oversized Claude Writes are rejected uniformly as well.
func Guarded(inner ports.PermissionHandler) ports.PermissionHandler {
	return &SessionGuardHandler{Inner: inner, MaxWriteBytes: DefaultWriteGuardBytes}
}

// AutoApproveHandler approves all tool use requests immediately.
type AutoApproveHandler struct{}

// CanUseTool always returns allow.
func (h *AutoApproveHandler) CanUseTool(_ ports.ToolPermissionRequest) (ports.PermissionDecision, error) {
	return ports.PermissionDecision{Behavior: DecisionAllow}, nil
}

// AcceptEditsHandler auto-approves read-only tools and file edit/write tools,
// but leaves everything else (Bash, Agent, etc.) for the desktop app to prompt.
// Returning an empty Behavior ("") signals that the handler declines to decide
// and the request should be surfaced to the user.
type AcceptEditsHandler struct{}

// CanUseTool approves reads and file edits; defers everything else.
func (h *AcceptEditsHandler) CanUseTool(req ports.ToolPermissionRequest) (ports.PermissionDecision, error) {
	if isReadOnlyTool(req.ToolName) {
		return ports.PermissionDecision{Behavior: DecisionAllow}, nil
	}
	switch req.ToolName {
	// File modification tools — auto-approve (matches Claude Code acceptEdits)
	case toolNameEdit, toolNameWrite, toolNameNotebookEdit:
		return ports.PermissionDecision{Behavior: DecisionAllow}, nil

	// Agent spawning — auto-approve (subagents are sandboxed)
	case "Agent":
		return ports.PermissionDecision{Behavior: DecisionAllow}, nil
	}

	// Everything else (Bash, etc.) — defer to desktop app
	return ports.PermissionDecision{}, nil
}

// CreateWithinRootsHandler auto-approves a plain, conservatively-flagged
// `touch <path>...` or `mkdir [-p] <path>...` Bash command when every target
// path falls within Roots, deferring to Inner for everything else. Both
// commands can only bring an empty file or directory into existence (or, for
// touch, bump an existing file's mtime) — the same effect Edit/Write already
// have when AcceptEditsHandler approves them unconditionally. This closes a
// gap where the harness prompts for an output directory it already created
// before the session started, while the equivalent Write/directory-already-
// exists case would go through silently.
//
// Unlike Edit/Write, OpenCode has no per-path scoping for Bash to fall back
// on: its declarative permission map treats "bash" as a single ask/allow/deny
// decision, never a per-path glob (see internal/llm/opencode/config.go). So
// Roots here is the only boundary standing between an approval and letting a
// shell command anywhere on disk through — a touch/mkdir outside it always
// defers to Inner rather than being approved by default.
type CreateWithinRootsHandler struct {
	Inner ports.PermissionHandler
	Roots []string
}

// CanUseTool approves an in-root touch/mkdir; everything else defers to Inner.
func (h *CreateWithinRootsHandler) CanUseTool(req ports.ToolPermissionRequest) (ports.PermissionDecision, error) {
	if req.ToolName == toolNameBash {
		if targets, ok := simpleCommandTargets(req.Input, "touch"); ok && allPathsWithinRoots(targets, h.Roots) {
			return ports.PermissionDecision{Behavior: DecisionAllow}, nil
		}
		if targets, ok := simpleCommandTargets(req.Input, "mkdir", "-p"); ok && allPathsWithinRoots(targets, h.Roots) {
			return ports.PermissionDecision{Behavior: DecisionAllow}, nil
		}
	}
	return h.Inner.CanUseTool(req)
}

// simpleCommandTargets parses a Bash command as a plain `<name> [flag]... <path>...`
// invocation with no chaining, redirection, or substitution, returning its
// literal path arguments. Only flags listed in allowedFlags are recognized and
// skipped; any other `-`-prefixed argument (e.g. touch's -r/-t or mkdir's -m)
// rejects the whole command, since such flags change what the command does to
// files outside its argument list — outside what this narrow exception is
// meant to cover.
func simpleCommandTargets(input, name string, allowedFlags ...string) ([]string, bool) {
	command := strings.TrimSpace(extractBashCommand(input))
	if command == "" {
		return nil, false
	}
	// This parser deliberately accepts literal paths only. Expansion happens
	// after lexical root validation, so allowing any of these shell constructs
	// would let one apparently in-root token expand to an out-of-root target.
	if strings.ContainsAny(command, "\n\r;`<>|&$*?[]{}~") {
		return nil, false
	}
	fields := strings.Fields(command)
	if len(fields) < 2 || filepath.Base(fields[0]) != name {
		return nil, false
	}
	var targets []string
	for _, f := range fields[1:] {
		if strings.HasPrefix(f, "-") {
			if !slices.Contains(allowedFlags, f) {
				return nil, false
			}
			continue
		}
		targets = append(targets, f)
	}
	return targets, len(targets) > 0
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

// WrapGeneralPhaseHandlerWithSafeCreate wraps h in a CreateWithinRootsHandler
// when h is one of the general accept-edits handlers permHandlerFor builds
// (AutoApproveHandler, AcceptEditsHandler, or CachingHandler over one of
// those — optionally already wrapped in a SessionGuardHandler via Guarded).
// Any other handler shape — bounded helpers, review-feedback sessions, and
// restricted read-only sessions — is returned unchanged, since those exist
// specifically to restrict writes below the general writable-root policy and
// must not be loosened by this exception.
func WrapGeneralPhaseHandlerWithSafeCreate(h ports.PermissionHandler, roots []string) ports.PermissionHandler {
	if len(roots) == 0 || !IsGeneralPhaseHandler(h) {
		return h
	}
	return &CreateWithinRootsHandler{Inner: h, Roots: roots}
}

// IsGeneralPhaseHandler reports whether h is, optionally through a
// SessionGuardHandler, one of the handler types permHandlerFor builds. Callers
// outside the permission package use it to scope behavior that must only
// attach to the general-phase permission policy (e.g. the automatic Bash-
// review decorator), never to review-helper or restricted handlers.
func IsGeneralPhaseHandler(h ports.PermissionHandler) bool {
	if guard, ok := h.(*SessionGuardHandler); ok {
		h = guard.Inner
	}
	switch h.(type) {
	case *AutoApproveHandler, *AcceptEditsHandler, *CachingHandler:
		return true
	default:
		return false
	}
}

// IsAutomaticReviewHandler reports whether h uses a permission policy whose
// undecided Bash requests may be sent through automatic review. In addition to
// the general phase handlers, Ask Me Anything chat deliberately defers Bash to
// the same user-facing permission UI and therefore participates in review.
//
// Keep this separate from IsGeneralPhaseHandler: AMA chat must not inherit
// unrelated general-phase exceptions such as safe-create approval.
func IsAutomaticReviewHandler(h ports.PermissionHandler) bool {
	if guard, ok := h.(*SessionGuardHandler); ok {
		h = guard.Inner
	}
	if _, ok := h.(*AMAHandler); ok {
		return true
	}
	return IsGeneralPhaseHandler(h)
}

// ReadOnlyHandler auto-approves read-only tools and Agent spawning, but
// hard-denies all file modification tools.
type ReadOnlyHandler struct{}

// CanUseTool approves reads; denies all writes.
func (h *ReadOnlyHandler) CanUseTool(req ports.ToolPermissionRequest) (ports.PermissionDecision, error) {
	if isReadOnlyTool(req.ToolName) {
		return ports.PermissionDecision{Behavior: DecisionAllow}, nil
	}
	switch req.ToolName {
	// Agent spawning — auto-approve (subagents are sandboxed)
	case "Agent":
		return ports.PermissionDecision{Behavior: DecisionAllow}, nil

	// File modification tools — hard deny
	case toolNameEdit, toolNameWrite, toolNameNotebookEdit:
		return ports.PermissionDecision{Behavior: DecisionDeny, Reason: "chat is read-only"}, nil
	}

	// Everything else (Bash, etc.) — hard deny for safety
	return ports.PermissionDecision{Behavior: DecisionDeny, Reason: "chat is read-only"}, nil
}

// AMAHandler is used by the Ask Me Anything chat. It auto-approves read-only
// inspection, disables delegation, and leaves other top-level tools for the
// user-facing permission UI.
type AMAHandler struct{}

// CanUseTool approves safe reads, denies subagent delegation, and defers
// diagnostics or mutations such as Bash/Edit/Write to the caller.
func (h *AMAHandler) CanUseTool(req ports.ToolPermissionRequest) (ports.PermissionDecision, error) {
	if isReadOnlyTool(req.ToolName) {
		return ports.PermissionDecision{Behavior: DecisionAllow}, nil
	}
	switch req.ToolName {
	case "Agent", "Task":
		return ports.PermissionDecision{Behavior: DecisionDeny, Reason: "AMA chat does not support sub-agents"}, nil
	default:
		return ports.PermissionDecision{}, nil
	}
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
		return ports.PermissionDecision{Behavior: DecisionAllow}, nil

	// Sub-agent spawning — auto-approve. Spawned sub-agents inherit the
	// provider's depth-1 profile (no sub-sub-agents) and cannot mutate the
	// worktree, matching the research-phase treatment.
	case "Agent":
		return ports.PermissionDecision{Behavior: DecisionAllow}, nil

	case toolNameBash:
		if h.Sandboxed || boundedHelperReadOnlyBashAllowed(req.Input) {
			return ports.PermissionDecision{Behavior: DecisionAllow}, nil
		}
		return ports.PermissionDecision{
			Behavior: DecisionDeny,
			Reason:   "bounded helper shell access is limited to read-only inspection commands",
		}, nil

	case toolNameEdit, toolNameWrite, toolNameNotebookEdit:
		path, ok := toolInputFilePath(req.Input)
		if ok && h.pathAllowed(path) {
			return ports.PermissionDecision{Behavior: DecisionAllow}, nil
		}
		return ports.PermissionDecision{
			Behavior: DecisionDeny,
			Reason:   "bounded helper may write only declared artifacts in its helper directory",
		}, nil
	}

	return ports.PermissionDecision{
		Behavior: DecisionDeny,
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

// LiveRunReviewHandler grants a review axis hands-on command access while
// keeping tool-driven writes scoped to harness-owned helper artifacts and
// scratch roots outside the reviewed source tree.
type LiveRunReviewHandler struct {
	AllowedPaths  []string
	ScratchRoots  []string
	DenyWriteHint string
}

// CanUseTool allows broad shell use for live-run review and restricts file
// mutation tools to helper artifacts or scratch roots.
func (h *LiveRunReviewHandler) CanUseTool(req ports.ToolPermissionRequest) (ports.PermissionDecision, error) {
	switch req.ToolName {
	case "Read", "Glob", "Grep", "LS", "LSP", "ExternalDirectory", "WebSearch", "WebFetch":
		return ports.PermissionDecision{Behavior: "allow"}, nil

	case "Agent":
		return ports.PermissionDecision{Behavior: "allow"}, nil

	case "Bash":
		return ports.PermissionDecision{Behavior: "allow"}, nil

	case "Edit", "Write", "NotebookEdit":
		path, ok := toolInputFilePath(req.Input)
		if ok && (pathExactlyAllowed(path, h.AllowedPaths) || pathUnderAnyRoot(path, h.ScratchRoots)) {
			return ports.PermissionDecision{Behavior: "allow"}, nil
		}
		reason := h.DenyWriteHint
		if strings.TrimSpace(reason) == "" {
			reason = "live-run review may write only helper artifacts and scratch roots"
		}
		return ports.PermissionDecision{Behavior: "deny", Reason: reason}, nil
	}

	return ports.PermissionDecision{
		Behavior: "deny",
		Reason:   "live-run review may use shell and read tools, but may not use undeclared tools",
	}, nil
}

func pathExactlyAllowed(path string, allowedPaths []string) bool {
	cleaned, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return false
	}
	for _, allowed := range allowedPaths {
		allowedAbs, err := filepath.Abs(filepath.Clean(allowed))
		if err == nil && cleaned == allowedAbs {
			return true
		}
	}
	return false
}

func pathUnderAnyRoot(path string, roots []string) bool {
	cleaned, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return false
	}
	for _, root := range roots {
		rootAbs, err := filepath.Abs(filepath.Clean(root))
		if err != nil {
			continue
		}
		if cleaned == rootAbs {
			return true
		}
		rel, err := filepath.Rel(rootAbs, cleaned)
		if err == nil && rel != "." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".." {
			return true
		}
	}
	return false
}

func toolInputFilePath(input string) (string, bool) {
	var payload struct {
		FilePath     string `json:"file_path"`
		NotebookPath string `json:"notebook_path"`
	}
	if err := json.Unmarshal([]byte(input), &payload); err != nil {
		return "", false
	}
	if payload.FilePath != "" {
		return payload.FilePath, true
	}
	if payload.NotebookPath != "" {
		return payload.NotebookPath, true
	}
	return "", false
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
	var ok bool
	command, ok = stripReadOnlyShellRedirections(command)
	if !ok {
		return false
	}
	// Only literal arguments reach the command-specific flag checks below.
	// Otherwise a variable, glob, or brace expansion could turn an apparently
	// harmless argument into a denied output/exec flag after approval.
	if strings.ContainsAny(command, "\n\r;`<>$*?[]{}~") {
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
	case "cd", "pwd", "ls", "cat", "head", "tail", "wc", "grep", "echo", "true", "false", "cut", "tr":
		return true
	case "rg":
		return !hasShellOption(fields[1:], "--pre")
	case "sort":
		return !hasShellOption(fields[1:], "-o", "--output", "--compress-program")
	case "uniq":
		return readOnlyUniqAllowed(fields[1:])
	case "test", "[":
		return true
	case "find":
		return !hasAnyShellToken(fields[1:],
			"-delete", "-exec", "-execdir", "-ok", "-okdir",
			"-fprint", "-fprint0", "-fprintf", "-fls",
		)
	case "git":
		return gitReadOnlySubcommandAllowed(fields[1:])
	default:
		return false
	}
}

func stripReadOnlyShellRedirections(command string) (string, bool) {
	fields := strings.Fields(command)
	kept := make([]string, 0, len(fields))
	for i := 0; i < len(fields); i++ {
		switch fields[i] {
		case "2>/dev/null", "2>&1":
			continue
		case "2>":
			if i+1 >= len(fields) || fields[i+1] != "/dev/null" {
				return "", false
			}
			i++
			continue
		default:
			kept = append(kept, fields[i])
		}
	}
	return strings.Join(kept, " "), true
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

// hasShellOption rejects both a standalone option (whose value may be the
// following field) and attached forms such as -oFILE or --output=FILE.
func hasShellOption(fields []string, denied ...string) bool {
	for _, field := range fields {
		for _, option := range denied {
			if field == option || strings.HasPrefix(field, option+"=") ||
				(strings.HasPrefix(option, "-") && !strings.HasPrefix(option, "--") && strings.HasPrefix(field, option) && len(field) > len(option)) {
				return true
			}
		}
	}
	return false
}

// readOnlyUniqAllowed permits the common `uniq [INPUT]` form but not uniq's
// optional OUTPUT operand. Keeping the accepted flag grammar deliberately
// narrow avoids mistaking an option value for the input file.
func readOnlyUniqAllowed(fields []string) bool {
	positional := 0
	for _, field := range fields {
		if strings.HasPrefix(field, "-") {
			if field == "-" || field == "-c" || field == "-d" || field == "-D" || field == "-i" || field == "-u" || field == "-z" ||
				field == "--count" || field == "--repeated" || field == "--all-repeated" || field == "--ignore-case" || field == "--unique" || field == "--zero-terminated" {
				continue
			}
			return false
		}
		positional++
	}
	return positional <= 1
}

func gitReadOnlySubcommandAllowed(fields []string) bool {
	if len(fields) == 0 {
		return false
	}
	switch fields[0] {
	case "status", "ls-files", "rev-parse":
		return true
	case "diff", "log", "show":
		return !hasShellOption(fields[1:], "-o", "--output", "--ext-diff", "--textconv")
	case "branch":
		return gitReadOnlyBranchAllowed(fields[1:])
	default:
		return false
	}
}

func gitReadOnlyBranchAllowed(fields []string) bool {
	for _, field := range fields {
		switch field {
		case "-a", "--all", "-r", "--remotes", "-v", "-vv", "--verbose", "--list", "--show-current", "--no-color":
			continue
		default:
			// Patterns and branch names are ambiguous here: `git branch foo`
			// creates a branch, so only known listing flags are accepted.
			return false
		}
	}
	return true
}

// DenyAllHandler denies all tool use requests.
type DenyAllHandler struct{}

// CanUseTool always returns deny.
func (h *DenyAllHandler) CanUseTool(_ ports.ToolPermissionRequest) (ports.PermissionDecision, error) {
	return ports.PermissionDecision{Behavior: DecisionDeny, Reason: "all tools denied"}, nil
}
