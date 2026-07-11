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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// mockHandler is a simple PermissionHandler for testing the CachingHandler.
type mockHandler struct {
	behavior string
	err      error
}

func (h *mockHandler) CanUseTool(_ ports.ToolPermissionRequest) (ports.PermissionDecision, error) {
	return ports.PermissionDecision{Behavior: h.behavior}, h.err
}

func TestBoundedHelperArtifactHandler_AllowsOnlyDeclaredArtifacts(t *testing.T) {
	helperDir := t.TempDir()
	feedbackPath := filepath.Join(helperDir, "review-feedback.md")
	markerPath := filepath.Join(helperDir, "phase_complete")
	approvalPath := filepath.Join(helperDir, "axis-approved-scope.md")

	handler := &BoundedHelperArtifactHandler{
		AllowedPaths: []string{feedbackPath, markerPath, approvalPath},
	}

	requirePermissionAllowed(t, handler, "Read", `{"file_path":"`+feedbackPath+`"}`)
	requirePermissionAllowed(t, handler, toolNameWrite, `{"file_path":"`+feedbackPath+`"}`)
	requirePermissionAllowed(t, handler, toolNameEdit, `{"file_path":"`+markerPath+`"}`)
	requirePermissionAllowed(t, handler, toolNameWrite, `{"file_path":"`+approvalPath+`"}`)

	requirePermissionDenied(t, handler, toolNameWrite, `{"file_path":"`+filepath.Join(helperDir, "notes.md")+`"}`)
	requirePermissionDenied(t, handler, toolNameWrite, `{"file_path":"`+filepath.Join(filepath.Dir(helperDir), "review-feedback.md")+`"}`)
	requirePermissionDenied(t, handler, toolNameWrite, `{"file_path":"`+filepath.Join(helperDir, "..", "review-feedback.md")+`"}`)
	requirePermissionAllowed(t, handler, toolNameBash, `{"command":"ls `+helperDir+` 2>/dev/null || echo \"DIR_NOT_FOUND\""}`)
	requirePermissionAllowed(t, handler, toolNameBash, `{"command":"test -f `+feedbackPath+` && echo \"FEEDBACK_EXISTS\" || echo \"FEEDBACK_NOT_FOUND\""}`)
	requirePermissionDenied(t, handler, toolNameBash, `{"command":"touch phase_complete"}`)
	requirePermissionDenied(t, handler, toolNameBash, `{"command":"mkdir -p `+filepath.Join(helperDir, "subdir")+`"}`)
	requirePermissionDenied(t, handler, toolNameBash, `{"command":"echo ok > `+markerPath+`"}`)
	requirePermissionAllowed(t, handler, "Agent", `{"prompt":"explore the implementation"}`)
}

// TestBoundedHelperArtifactHandler_SandboxedAllowsAnyShell confirms that when the
// helper process runs under an OS read-only-worktree sandbox (Sandboxed=true),
// shell is unrestricted — the read-only-analysis constructs the allowlist rejects
// (command substitution, loops) are permitted, since the kernel, not the
// allowlist, prevents worktree mutation. Non-sandboxed helpers keep the
// restrictive allowlist, and edits stay gated to declared artifacts either way.
func TestBoundedHelperArtifactHandler_SandboxedAllowsAnyShell(t *testing.T) {
	helperDir := t.TempDir()
	allowed := []string{filepath.Join(helperDir, "review-feedback.md"), filepath.Join(helperDir, "phase_complete")}
	// The real read-only analysis (loop + command substitution) that aborts a
	// non-sandboxed glm helper.
	analysis := `{"command":"for s in a b; do c=$(grep -c \"$s\" README.md); echo \"$s: $c\"; done"}`

	sandboxed := &BoundedHelperArtifactHandler{AllowedPaths: allowed, Sandboxed: true}
	requirePermissionAllowed(t, sandboxed, toolNameBash, analysis)
	requirePermissionAllowed(t, sandboxed, toolNameBash, `{"command":"\"$AGENTICO_BIN\" validate-artifacts --dir x"}`)
	requirePermissionAllowed(t, sandboxed, toolNameWrite, `{"file_path":"`+allowed[0]+`"}`)
	requirePermissionDenied(t, sandboxed, toolNameWrite, `{"file_path":"`+filepath.Join(helperDir, "other.md")+`"}`)

	strict := &BoundedHelperArtifactHandler{AllowedPaths: allowed}
	requirePermissionDenied(t, strict, toolNameBash, analysis)
}

func requirePermissionDeferred(t *testing.T, handler ports.PermissionHandler, input string) {
	t.Helper()
	const toolName = toolNameBash
	decision, err := handler.CanUseTool(ports.ToolPermissionRequest{ToolName: toolName, Input: input})
	if err != nil {
		t.Fatalf("CanUseTool(%q, %q) error = %v", toolName, input, err)
	}
	if decision.Behavior != "" {
		t.Fatalf("CanUseTool(%q, %q).Behavior = %q, want empty (deferred)", toolName, input, decision.Behavior)
	}
}

func TestCreateWithinRootsHandler_AllowsPlainTouchInsideRoot(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "phase_complete")
	handler := &CreateWithinRootsHandler{Inner: &AcceptEditsHandler{}, Roots: []string{root}}

	requirePermissionAllowed(t, handler, toolNameBash, `{"command":"touch `+marker+`"}`)
	// Multiple targets, all inside a root, still qualify.
	requirePermissionAllowed(t, handler, toolNameBash, `{"command":"touch `+marker+` `+filepath.Join(root, "other")+`"}`)
	// Non-Bash tools and non-touch Bash still fall through to Inner unchanged.
	requirePermissionAllowed(t, handler, toolNameWrite, `{"file_path":"`+marker+`"}`)
	requirePermissionDeferred(t, handler, `{"command":"ls `+root+`"}`)
}

func TestCreateWithinRootsHandler_DefersOutsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	handler := &CreateWithinRootsHandler{Inner: &AcceptEditsHandler{}, Roots: []string{root}}

	requirePermissionDeferred(t, handler, `{"command":"touch `+filepath.Join(outside, "phase_complete")+`"}`)
	// One target outside the root is enough to defer, even if another is inside.
	requirePermissionDeferred(t, handler,
		`{"command":"touch `+filepath.Join(root, "ok")+` `+filepath.Join(outside, "bad")+`"}`)
}

func TestCreateWithinRootsHandler_DefersOnFlagsChainingAndSubstitution(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "phase_complete")
	handler := &CreateWithinRootsHandler{Inner: &AcceptEditsHandler{}, Roots: []string{root}}

	requirePermissionDeferred(t, handler, `{"command":"touch -r `+marker+` `+marker+`"}`)
	requirePermissionDeferred(t, handler, `{"command":"touch `+marker+` && rm -rf /"}`)
	requirePermissionDeferred(t, handler, `{"command":"touch `+marker+`; echo done"}`)
	requirePermissionDeferred(t, handler, `{"command":"touch $(echo `+marker+`)"}`)
	requirePermissionDeferred(t, handler, `{"command":"touch `+marker+` > /dev/null"}`)
	requirePermissionDeferred(t, handler, `{"command":"nottouch `+marker+`"}`)
	requirePermissionDeferred(t, handler, `{"command":"touch"}`)
}

func TestCreateWithinRootsHandler_AllowsPlainMkdirInsideRoot(t *testing.T) {
	root := t.TempDir()
	runDir := filepath.Join(root, "runs", "run-001", "research")
	handler := &CreateWithinRootsHandler{Inner: &AcceptEditsHandler{}, Roots: []string{root}}

	requirePermissionAllowed(t, handler, toolNameBash, `{"command":"mkdir -p `+runDir+`"}`)
	// Bare mkdir (no -p) on an in-root target still qualifies.
	requirePermissionAllowed(t, handler, toolNameBash, `{"command":"mkdir `+runDir+`"}`)
	// Multiple targets, all inside a root, still qualify.
	requirePermissionAllowed(t, handler, toolNameBash, `{"command":"mkdir -p `+runDir+` `+filepath.Join(root, "other")+`"}`)
}

func TestCreateWithinRootsHandler_MkdirDefersOutsideRootOrOnOtherFlags(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	inRoot := filepath.Join(root, "runs", "run-001", "research")
	handler := &CreateWithinRootsHandler{Inner: &AcceptEditsHandler{}, Roots: []string{root}}

	requirePermissionDeferred(t, handler, `{"command":"mkdir -p `+filepath.Join(outside, "research")+`"}`)
	requirePermissionDeferred(t, handler,
		`{"command":"mkdir -p `+inRoot+` `+filepath.Join(outside, "bad")+`"}`)
	requirePermissionDeferred(t, handler, `{"command":"mkdir -m 0700 `+inRoot+`"}`)
	requirePermissionDeferred(t, handler, `{"command":"mkdir -p `+inRoot+` && rm -rf /"}`)
	requirePermissionDeferred(t, handler, `{"command":"mkdir -p"}`)
	requirePermissionDeferred(t, handler, `{"command":"mkdir -p -p"}`)
}

func TestWrapGeneralPhaseHandlerWithSafeCreate_WrapsAcceptEditsAndCaching(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "phase_complete")

	for _, inner := range []ports.PermissionHandler{
		&AutoApproveHandler{},
		&AcceptEditsHandler{},
		&CachingHandler{Inner: &AcceptEditsHandler{}, Cache: NewCache(nil)},
		Guarded(&AcceptEditsHandler{}),
	} {
		wrapped := WrapGeneralPhaseHandlerWithSafeCreate(inner, []string{root})
		requirePermissionAllowed(t, wrapped, toolNameBash, `{"command":"touch `+marker+`"}`)
	}
}

func TestWrapGeneralPhaseHandlerWithSafeCreate_LeavesNarrowerHandlersUnwrapped(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "phase_complete")

	for name, inner := range map[string]ports.PermissionHandler{
		"BoundedHelperArtifactHandler":        &BoundedHelperArtifactHandler{AllowedPaths: []string{marker}},
		"BoundedHelperArtifactHandlerGuarded": Guarded(&BoundedHelperArtifactHandler{AllowedPaths: []string{marker}}),
		"ReadOnlyHandler":                     &ReadOnlyHandler{},
		"PlanReviewHandler":                   &PlanReviewHandler{AllowedPath: marker},
		"RewindReviewHandler":                 &RewindReviewHandler{AllowedPath: marker},
		"ReviewFeedbackHandler":               &ReviewFeedbackHandler{AllowedPath: marker},
	} {
		t.Run(name, func(t *testing.T) {
			wrapped := WrapGeneralPhaseHandlerWithSafeCreate(inner, []string{root})
			if wrapped != inner {
				t.Fatalf("WrapGeneralPhaseHandlerWithSafeCreate(%s, roots) returned a new handler, want the original passed through unchanged", name)
			}
		})
	}
}

func TestWrapGeneralPhaseHandlerWithSafeCreate_NoRootsIsNoop(t *testing.T) {
	inner := &AcceptEditsHandler{}
	if got := WrapGeneralPhaseHandlerWithSafeCreate(inner, nil); got != inner {
		t.Fatal("WrapGeneralPhaseHandlerWithSafeCreate(inner, nil) should return inner unchanged")
	}
}

func requirePermissionAllowed(t *testing.T, handler ports.PermissionHandler, toolName, input string) {
	t.Helper()
	decision, err := handler.CanUseTool(ports.ToolPermissionRequest{ToolName: toolName, Input: input})
	if err != nil {
		t.Fatalf("CanUseTool(%q, %q) error = %v", toolName, input, err)
	}
	if decision.Behavior != DecisionAllow {
		t.Fatalf("CanUseTool(%q, %q).Behavior = %q, want allow; reason=%q", toolName, input, decision.Behavior, decision.Reason)
	}
}

func requirePermissionDenied(t *testing.T, handler ports.PermissionHandler, toolName, input string) {
	t.Helper()
	decision, err := handler.CanUseTool(ports.ToolPermissionRequest{ToolName: toolName, Input: input})
	if err != nil {
		t.Fatalf("CanUseTool(%q, %q) error = %v", toolName, input, err)
	}
	if decision.Behavior != DecisionDeny {
		t.Fatalf("CanUseTool(%q, %q).Behavior = %q, want deny", toolName, input, decision.Behavior)
	}
	if strings.TrimSpace(decision.Reason) == "" {
		t.Fatalf("CanUseTool(%q, %q).Reason is empty, want actionable deny reason", toolName, input)
	}
}

// ---------------------------------------------------------------------------
// Rule tests
// ---------------------------------------------------------------------------

func TestRule_Match_Exact(t *testing.T) {
	r := Rule{ToolPattern: patternBashLSExact, Effect: DecisionAllow}
	if !r.Match(toolNameBash, testLsLa) {
		t.Errorf("expected rule to match tool=Bash input='ls -la'")
	}
}

func TestRule_Match_NoMatch(t *testing.T) {
	r := Rule{ToolPattern: patternBashLSExact, Effect: DecisionAllow}
	if r.Match(toolNameBash, testRmRfRoot) {
		t.Errorf("expected rule NOT to match tool=Bash input='rm -rf /'")
	}
}

func TestRule_Match_DifferentTool(t *testing.T) {
	r := Rule{ToolPattern: patternBashLSExact, Effect: DecisionAllow}
	if r.Match(toolNameEdit, testLsLa) {
		t.Errorf("expected rule NOT to match tool=Edit input='ls -la'")
	}
}

// ---------------------------------------------------------------------------
// Rule.Match() comprehensive tests
// ---------------------------------------------------------------------------

func TestRule_Match(t *testing.T) {
	tests := []struct {
		name      string
		pattern   string // Rule.ToolPattern
		toolName  string
		toolInput string
		want      bool
	}{
		// Exact match (no wildcard)
		{"exact match", patternBashLSExact, toolNameBash, testLsLa, true},
		{"exact no match", patternBashLSExact, toolNameBash, testRmRfRoot, false},
		{"exact wrong tool", patternBashLSExact, toolNameEdit, testLsLa, false},

		// Prefix wildcard
		{"prefix match with args", patternBashNpmTest, toolNameBash, testNpmTestCoverage, true},
		{"prefix match exact prefix", patternBashNpmTest, toolNameBash, testNpmTest, true},
		{"prefix match different suffix", patternBashNpmTest, toolNameBash, "npm test src/", true},
		{"prefix no match different cmd", patternBashNpmTest, toolNameBash, "npm install", false},
		{"prefix no match partial word", patternBashNpmTest, toolNameBash, "npm testing", false},
		{"prefix wrong tool", patternBashNpmTest, toolNameEdit, testNpmTestCoverage, false},
		{"prefix single-word pattern", patternBashLS, toolNameBash, "ls -la /tmp", true},
		{"prefix single-word exact", patternBashLS, toolNameBash, "ls", true},

		// Tool-name-only (no parentheses)
		{"tool-only matches any input", toolNameBash, toolNameBash, "anything here", true},
		{"tool-only matches empty input", toolNameBash, toolNameBash, "", true},
		{"tool-only wrong tool", toolNameBash, toolNameEdit, "something", false},

		// Wildcard-only: Bash(*)
		{"wildcard-only matches any", patternBashAny, toolNameBash, testRmRfRoot, true},
		{"wildcard-only matches empty", patternBashAny, toolNameBash, "", true},
		{"wildcard-only wrong tool", patternBashAny, toolNameEdit, "anything", false},

		// Empty inner pattern: Bash()
		{"empty pattern matches empty input", "Bash()", toolNameBash, "", true},
		{"empty pattern no match non-empty", "Bash()", toolNameBash, "ls", false},

		// Non-Bash tool exact match
		{"edit exact match", patternEditFilePath, toolNameEdit, testFilePath, true},
		{"edit no match", patternEditFilePath, toolNameEdit, "/other/file", false},

		// JSON input (real runtime shape from Claude CLI wire protocol)
		{"json prefix match", patternBashNpmTest, toolNameBash, testJSONNpmTestCoverage, true},
		{"json prefix exact prefix", patternBashNpmTest, toolNameBash, `{"command":"npm test"}`, true},
		{"json prefix no match", patternBashNpmTest, toolNameBash, `{"command":"npm install"}`, false},
		{"json exact match", patternBashLSExact, toolNameBash, testJSONLsLa, true},
		{"json exact no match", patternBashLSExact, toolNameBash, `{"command":"rm -rf /"}`, false},
		{"json wildcard-only", patternBashAny, toolNameBash, `{"command":"anything"}`, true},
		{"json tool-only", toolNameBash, toolNameBash, `{"command":"anything"}`, true},

		// Chained commands (cd && ...) — normalized before matching
		{"chain cd then ls", patternBashLS, toolNameBash, "cd /path && ls -la", true},
		{"chain cd then npm test", patternBashNpmTest, toolNameBash, "cd /path && npm test --coverage", true},
		{"chain cd then git diff", patternBashGitDiff, toolNameBash, "cd /repo && git diff --stat", true},
		{"chain no match after normalize", patternBashLS, toolNameBash, "cd /path && rm -rf /", false},

		// Piped commands — normalized before matching
		{"pipe ls head", patternBashLS, toolNameBash, "ls -la | head -20", true},
		{"pipe git diff head", patternBashGitDiff, toolNameBash, "git diff --stat | head", true},
		{"pipe no match", patternBashLS, toolNameBash, "cat foo | grep bar", false},

		// Chain + pipe combined
		{"chain and pipe", patternBashLS, toolNameBash, "cd /path && ls -la | head -20", true},
		{"chain and pipe git", "Bash(git log *)", toolNameBash, "cd /repo && git log --oneline | head -30", true},

		// JSON variants of chained commands
		{"json chain cd then ls", patternBashLS, toolNameBash, `{"command":"cd /path && ls -la"}`, true},
		{"json chain cd then find", "Bash(find *)", toolNameBash, `{"command":"cd /path && find src -type f | sort"}`, true},
		{"json pipe ls", patternBashLS, toolNameBash, `{"command":"ls -la src/__tests__/ | grep test"}`, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Rule{ToolPattern: tt.pattern, Effect: DecisionAllow}
			got := r.Match(tt.toolName, tt.toolInput)
			if got != tt.want {
				t.Errorf("Rule{%q}.Match(%q, %q) = %v, want %v",
					tt.pattern, tt.toolName, tt.toolInput, got, tt.want)
			}
		})
	}
}

func TestDefaultGlobalRules_MatchReadOnlyCommands(t *testing.T) {
	cache := NewCache(nil)
	cache.mu.Lock()
	cache.rules = append(cache.rules, defaultGlobalRules()...)
	cache.mu.Unlock()

	tests := []struct {
		name    string
		command string
	}{
		{name: "ls", command: testJSONLsLa},
		{name: "pwd", command: `{"command":"pwd"}`},
		{name: "rg", command: `{"command":"rg EnsureGlobalDefaults internal/permission"}`},
		{name: "git diff", command: `{"command":"git diff --stat"}`},
		{name: "git show", command: `{"command":"git show HEAD~1"}`},
		// Chained commands that Claude actually sends
		{name: "cd then ls", command: `{"command":"cd /path && ls -la"}`},
		{name: "cd then git diff", command: `{"command":"cd /repo && git diff --stat"}`},
		{name: "cd then git log piped", command: `{"command":"cd /repo && git log --oneline | head -30"}`},
		{name: "ls piped", command: `{"command":"ls -la | head -20"}`},
		{name: "find", command: `{"command":"find src -type f"}`},
		// agentico's own artifact-validation preflight, run by every agent
		// session before phase_complete (see rolespec_prompt.go).
		{name: "validate-artifacts", command: `{"command":"\"$AGENTICO_BIN\" validate-artifacts --phase review --role final_reviewer --dir \"/state/feat-x/runs/run-001/review/iteration-03\""}`},
		{name: "cd then validate-artifacts", command: `{"command":"cd /repo && \"$AGENTICO_BIN\" validate-artifacts --phase plan --role designer --dir \"/state/feat-x/plan\""}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rule, found := cache.Check(toolNameBash, tt.command, "any-repo")
			if !found {
				t.Errorf("cache.Check(%q) found = false, want true", tt.command)
				return
			}
			if rule.Effect != DecisionAllow {
				t.Errorf("cache.Check(%q) effect = %q, want allow", tt.command, rule.Effect)
			}
		})
	}
}

func TestDefaultGlobalRules_MatchAgenticoValidateArtifacts(t *testing.T) {
	cache := NewCache(nil)
	cache.mu.Lock()
	cache.rules = append(cache.rules, defaultGlobalRules()...)
	cache.mu.Unlock()

	command := `{"command":"\"$AGENTICO_BIN\" validate-artifacts --phase review --role final_reviewer --dir /tmp/iteration-01"}`
	rule, found := cache.Check(toolNameBash, command, "any-repo")
	if !found {
		t.Fatalf("cache.Check(%q) found = false, want true", command)
	}
	if rule.Effect != DecisionAllow {
		t.Fatalf("cache.Check(%q) effect = %q, want allow", command, rule.Effect)
	}
}

func TestDefaultGlobalRules_DoNotMatchMutatingCommands(t *testing.T) {
	cache := NewCache(nil)
	cache.mu.Lock()
	cache.rules = append(cache.rules, defaultGlobalRules()...)
	cache.mu.Unlock()

	tests := []struct {
		name    string
		command string
	}{
		{name: "rm", command: `{"command":"rm -rf /tmp/x"}`},
		{name: "go build", command: `{"command":"go build ./..."}`},
		{name: "go test", command: `{"command":"go test ./..."}`},
		{name: "git add", command: `{"command":"git add ."}`},
		{name: "git commit", command: `{"command":"git commit -m test"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rule, found := cache.Check(toolNameBash, tt.command, "any-repo")
			if found {
				t.Errorf("cache.Check(%q) = %+v, want no match", tt.command, rule)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// extractBashCommand tests
// ---------------------------------------------------------------------------

func TestExtractBashCommand(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"json command", testJSONLsLa, testLsLa},
		{"json with spaces", testJSONNpmTestCoverage, testNpmTestCoverage},
		{"plain string", testLsLa, testLsLa},
		{"empty string", "", ""},
		{"json empty command", `{"command":""}`, ""},
		{"invalid json", `{bad json`, `{bad json`},
		{"json no command field", `{"other":"value"}`, ""},
		{"json with extra fields", `{"command":"ls","timeout":30}`, "ls"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractBashCommand(tt.input)
			if got != tt.want {
				t.Errorf("extractBashCommand(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// InferBashPattern tests
// ---------------------------------------------------------------------------

func TestInferBashPattern(t *testing.T) {
	tests := []struct {
		name      string
		toolName  string
		toolInput string
		want      string
	}{
		// Standard commands (binary + subcommand)
		{"npm test with args", toolNameBash, testNpmTestCoverage, patternBashNpmTest},
		{"go test with args", toolNameBash, testGoTestDotDotDot, patternBashGoTest},
		{"git commit with flags", toolNameBash, "git commit -m 'foo'", "Bash(git commit *)"},
		{"docker compose up", toolNameBash, "docker compose up -d", "Bash(docker compose *)"},
		{"kubectl get pods", toolNameBash, "kubectl get pods -n default", "Bash(kubectl get *)"},

		// Single-token commands
		{"ls alone", toolNameBash, "ls", patternBashLS},
		{"pwd alone", toolNameBash, "pwd", "Bash(pwd *)"},

		// Binary with flags only (second token is flag → binary *)
		{"ls with flags", toolNameBash, "ls -la /tmp", patternBashLS},
		{"rm with flags", toolNameBash, "rm -rf /tmp/foo", patternBashRm},
		{"grep with flags", toolNameBash, "grep -rn 'pattern' .", "Bash(grep *)"},

		// Chained commands: cd && ...
		{"cd then npm test", toolNameBash, "cd /path && npm test --coverage", patternBashNpmTest},
		{"cd then go build", toolNameBash, "cd /repo && go build ./...", "Bash(go build *)"},
		{"cd then ls", toolNameBash, "cd /tmp && ls -la", patternBashLS},
		{"multiple chains", toolNameBash, "cd /a && cd /b && npm test", patternBashNpmTest},

		// Pipes (take content before first |)
		{"npm test piped", toolNameBash, "npm test 2>&1 | tee log", patternBashNpmTest},
		{"cat piped to grep", toolNameBash, "cat file | grep pattern", "Bash(cat file *)"},

		// Empty input
		{"empty input", toolNameBash, "", patternBashAny},
		{"whitespace only", toolNameBash, "   ", patternBashAny},

		// Non-Bash tools: return exact pattern (no inference)
		{"edit tool", toolNameEdit, testFilePath, patternEditFilePath},
		{"write tool", toolNameWrite, testFilePath, "Write(/path/to/file)"},

		// JSON input (real runtime shape from Claude CLI wire protocol)
		{"json npm test", toolNameBash, testJSONNpmTestCoverage, patternBashNpmTest},
		{"json ls", toolNameBash, testJSONLsLa, patternBashLS},
		{"json go test", toolNameBash, `{"command":"go test ./..."}`, patternBashGoTest},
		{"json cd chain", toolNameBash, `{"command":"cd /repo && npm test"}`, patternBashNpmTest},
		{"json empty command", toolNameBash, `{"command":""}`, patternBashAny},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := InferBashPattern(tt.toolName, tt.toolInput)
			if got != tt.want {
				t.Errorf("InferBashPattern(%q, %q) = %q, want %q",
					tt.toolName, tt.toolInput, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Cache tests
// ---------------------------------------------------------------------------

func TestCacheRememberAllowPatternPersistsBeforeMemory(t *testing.T) {
	t.Parallel()

	store := NewStore(t.TempDir())
	cache := NewCache(store)
	result, err := cache.RememberAllowPattern(patternBashGoTest, testRepoA)
	if err != nil {
		t.Fatalf("RememberAllowPattern() error = %v", err)
	}
	if !result.Persisted || result.AlreadyExisted {
		t.Fatalf("result = %+v, want persisted new rule", result)
	}
	if _, ok := cache.Check(toolNameBash, `{"command":"go test ./internal/tui"}`, testRepoA); !ok {
		t.Fatal("cache does not match remembered rule after persistence")
	}
	rules, err := store.Load(scopeFor(testRepoA))
	if err != nil {
		t.Fatalf("Load(repo-a): %v", err)
	}
	if len(rules) != 1 || rules[0].ToolPattern != patternBashGoTest || rules[0].RepoName != testRepoA {
		t.Fatalf("stored rules = %+v, want repo-scoped remembered rule", rules)
	}
}

func TestCacheRememberAllowPatternExactScopeDuplicateSkipsWrite(t *testing.T) {
	t.Parallel()

	store := NewStore(t.TempDir())
	cache := NewCache(store)
	if _, err := cache.RememberAllowPattern(patternBashGoTest, testRepoA); err != nil {
		t.Fatalf("initial remember: %v", err)
	}
	result, err := cache.RememberAllowPattern(patternBashGoTest, testRepoA)
	if err != nil {
		t.Fatalf("duplicate remember: %v", err)
	}
	if !result.AlreadyExisted || result.Persisted {
		t.Fatalf("duplicate result = %+v, want already_existed without persisted", result)
	}
}

func TestCacheRememberAllowPatternPersistenceErrorDoesNotMutateMemory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	storePath := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(storePath, []byte("file blocks permissions dir"), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", storePath, err)
	}

	cache := NewCache(NewStore(storePath))
	result, err := cache.RememberAllowPattern(patternBashGoTest, testRepoA)
	if err == nil {
		t.Fatal("RememberAllowPattern() error = nil, want persistence error")
	}
	if result.Persisted || result.AlreadyExisted {
		t.Fatalf("result = %+v, want not persisted and not already existed", result)
	}
	if rules := cache.Rules(); len(rules) != 0 {
		t.Fatalf("cache.Rules() = %+v, want empty after persistence error", rules)
	}
	if _, ok := cache.Check(toolNameBash, `{"command":"go test ./internal/tui"}`, testRepoA); ok {
		t.Fatal("cache matches remembered rule after persistence error")
	}
}

func TestCache_Check_Empty(t *testing.T) {
	c := NewCache(nil)
	_, found := c.Check(toolNameBash, "ls", "repo")
	if found {
		t.Errorf("expected no match in empty cache")
	}
}

func TestCache_Check_AllowMatch(t *testing.T) {
	c := NewCache(nil)
	c.RememberAllow(toolNameBash, "ls", "repo")

	rule, found := c.Check(toolNameBash, "ls", "repo")
	if !found {
		t.Fatal("expected match for allow rule")
	}
	if rule.Effect != DecisionAllow {
		t.Errorf("effect = %q, want allow", rule.Effect)
	}
}

func TestCache_Check_DenyMatch(t *testing.T) {
	c := NewCache(nil)
	// Manually add a deny rule since RememberAllow only adds allow rules.
	c.mu.Lock()
	c.rules = append(c.rules, Rule{
		ToolPattern: "Bash(rm -rf /)",
		Effect:      DecisionDeny,
		RepoName:    "repo",
	})
	c.mu.Unlock()

	rule, found := c.Check(toolNameBash, testRmRfRoot, "repo")
	if !found {
		t.Fatal("expected match for deny rule")
	}
	if rule.Effect != DecisionDeny {
		t.Errorf("effect = %q, want deny", rule.Effect)
	}
}

func TestCache_Check_DenyWinsOverAllow(t *testing.T) {
	c := NewCache(nil)
	// Add an allow rule.
	c.RememberAllow(toolNameBash, testNpmTest, "repo")
	// Add a deny rule for the same pattern.
	c.mu.Lock()
	c.rules = append(c.rules, Rule{
		ToolPattern: "Bash(npm test)",
		Effect:      DecisionDeny,
		RepoName:    "repo",
	})
	c.mu.Unlock()

	rule, found := c.Check(toolNameBash, testNpmTest, "repo")
	if !found {
		t.Fatal("expected match")
	}
	if rule.Effect != DecisionDeny {
		t.Errorf("effect = %q, want deny (deny should win over allow)", rule.Effect)
	}
}

func TestCache_Check_RepoScope(t *testing.T) {
	c := NewCache(nil)
	c.RememberAllow(toolNameBash, "make build", testRepoA)

	// Should match the correct repo.
	_, found := c.Check(toolNameBash, "make build", testRepoA)
	if !found {
		t.Errorf("expected match for repo-a")
	}

	// Should NOT match a different repo.
	_, found = c.Check(toolNameBash, "make build", "repo-b")
	if found {
		t.Errorf("expected no match for repo-b (rule is scoped to repo-a)")
	}
}

func TestCache_Check_GlobalScope(t *testing.T) {
	c := NewCache(nil)
	// Add a global rule (empty repo name).
	c.RememberAllow(toolNameBash, "echo hello", "")

	// Should match any repo.
	_, found := c.Check(toolNameBash, "echo hello", "repo-x")
	if !found {
		t.Errorf("expected global rule to match repo-x")
	}
	_, found = c.Check(toolNameBash, "echo hello", "repo-y")
	if !found {
		t.Errorf("expected global rule to match repo-y")
	}
}

func TestCache_RememberAllow(t *testing.T) {
	c := NewCache(nil)
	c.RememberAllow(toolNameBash, testGoTestDotDotDot, testMyRepo)

	rule, found := c.Check(toolNameBash, testGoTestDotDotDot, testMyRepo)
	if !found {
		t.Fatal("expected match after RememberAllow")
	}
	if rule.Effect != DecisionAllow {
		t.Errorf("effect = %q, want allow", rule.Effect)
	}
	if rule.ToolPattern != patternBashGoTest {
		t.Errorf("pattern = %q, want %q", rule.ToolPattern, patternBashGoTest)
	}
}

func TestCache_RememberAllow_CrossSession(t *testing.T) {
	c := NewCache(nil)

	// Add a rule via the shared cache.
	c.RememberAllow(toolNameBash, "pytest", "shared-repo")

	// Handler 2 shares the same cache and should find the rule.
	h2 := &CachingHandler{
		Inner:    &mockHandler{behavior: ""},
		Cache:    c,
		RepoName: "shared-repo",
	}
	req := ports.ToolPermissionRequest{
		RequestID: "req_1",
		ToolName:  toolNameBash,
		Input:     "pytest",
	}
	decision, err := h2.CanUseTool(req)
	if err != nil {
		t.Fatalf("CanUseTool: %v", err)
	}
	if decision.Behavior != DecisionAllow {
		t.Errorf("behavior = %q, want allow (cross-session cache hit)", decision.Behavior)
	}
}

// ---------------------------------------------------------------------------
// CachingHandler tests
// ---------------------------------------------------------------------------

func TestCachingHandler_Passthrough(t *testing.T) {
	h := &CachingHandler{
		Inner:    &mockHandler{behavior: DecisionAllow},
		Cache:    NewCache(nil),
		RepoName: "repo",
	}
	req := ports.ToolPermissionRequest{
		RequestID: "req_1",
		ToolName:  toolNameBash,
		Input:     "ls",
	}
	decision, err := h.CanUseTool(req)
	if err != nil {
		t.Fatalf("CanUseTool: %v", err)
	}
	if decision.Behavior != DecisionAllow {
		t.Errorf("behavior = %q, want allow (inner handler passthrough)", decision.Behavior)
	}
}

func TestCachingHandler_InnerDeny(t *testing.T) {
	h := &CachingHandler{
		Inner:    &mockHandler{behavior: DecisionDeny},
		Cache:    NewCache(nil),
		RepoName: "repo",
	}
	req := ports.ToolPermissionRequest{
		RequestID: "req_1",
		ToolName:  toolNameBash,
		Input:     testRmRfRoot,
	}
	decision, err := h.CanUseTool(req)
	if err != nil {
		t.Fatalf("CanUseTool: %v", err)
	}
	if decision.Behavior != DecisionDeny {
		t.Errorf("behavior = %q, want deny (inner handler deny passthrough)", decision.Behavior)
	}
}

func TestCachingHandler_InnerError(t *testing.T) {
	innerErr := errors.New("something went wrong")
	h := &CachingHandler{
		Inner:    &mockHandler{behavior: "", err: innerErr},
		Cache:    NewCache(nil),
		RepoName: "repo",
	}
	req := ports.ToolPermissionRequest{
		RequestID: "req_1",
		ToolName:  toolNameBash,
		Input:     "ls",
	}
	_, err := h.CanUseTool(req)
	if err == nil {
		t.Fatal("expected error from inner handler")
	}
	if !errors.Is(err, innerErr) {
		t.Errorf("err = %v, want %v", err, innerErr)
	}
}

func TestCachingHandler_CacheHit(t *testing.T) {
	cache := NewCache(nil)
	cache.RememberAllow(toolNameBash, testNpmTest, "repo")

	h := &CachingHandler{
		Inner:    &mockHandler{behavior: ""}, // inner defers
		Cache:    cache,
		RepoName: "repo",
	}
	req := ports.ToolPermissionRequest{
		RequestID: "req_1",
		ToolName:  toolNameBash,
		Input:     testNpmTest,
	}
	decision, err := h.CanUseTool(req)
	if err != nil {
		t.Fatalf("CanUseTool: %v", err)
	}
	if decision.Behavior != DecisionAllow {
		t.Errorf("behavior = %q, want allow (cache hit)", decision.Behavior)
	}
}

func TestCachingHandler_CacheMiss(t *testing.T) {
	h := &CachingHandler{
		Inner:    &mockHandler{behavior: ""}, // inner defers
		Cache:    NewCache(nil),              // empty cache
		RepoName: "repo",
	}
	req := ports.ToolPermissionRequest{
		RequestID: "req_1",
		ToolName:  toolNameBash,
		Input:     "unknown command",
	}
	decision, err := h.CanUseTool(req)
	if err != nil {
		t.Fatalf("CanUseTool: %v", err)
	}
	if decision.Behavior != "" {
		t.Errorf("behavior = %q, want empty (defer to TUI)", decision.Behavior)
	}
}

func TestCachingHandler_DenyWins(t *testing.T) {
	cache := NewCache(nil)
	// Add an allow rule.
	cache.RememberAllow(toolNameBash, testNpmTest, "repo")
	// Add a deny rule for the same pattern.
	cache.mu.Lock()
	cache.rules = append(cache.rules, Rule{
		ToolPattern: "Bash(npm test)",
		Effect:      DecisionDeny,
		RepoName:    "repo",
	})
	cache.mu.Unlock()

	h := &CachingHandler{
		Inner:    &mockHandler{behavior: ""}, // inner defers
		Cache:    cache,
		RepoName: "repo",
	}
	req := ports.ToolPermissionRequest{
		RequestID: "req_1",
		ToolName:  toolNameBash,
		Input:     testNpmTest,
	}
	decision, err := h.CanUseTool(req)
	if err != nil {
		t.Fatalf("CanUseTool: %v", err)
	}
	if decision.Behavior != DecisionDeny {
		t.Errorf("behavior = %q, want deny (deny wins over allow)", decision.Behavior)
	}
}

// TestSingleRepoPermissionScoping is a regression test ensuring that
// single-repo features scope permissions to the repo name, not globally.
// This mirrors the derive-permRepoName logic in implement.go where
// cfg.RepoName is empty (PID naming compat) but the feature's first repo
// name is used for permission scoping.
func TestSingleRepoPermissionScoping(t *testing.T) {
	cache := NewCache(nil)

	// Simulate single-repo feature: cfg.RepoName is "" but the feature has a repo.
	cfgRepoName := "" // empty for single-repo features (PID naming compat)
	featureRepoName := "my-service"

	// Derive the permission repo name — same logic as implement.go
	permRepoName := cfgRepoName
	if permRepoName == "" {
		permRepoName = featureRepoName
	}

	// Create handler with the derived repo name.
	h := &CachingHandler{
		Inner:    &mockHandler{behavior: ""}, // inner defers (AcceptEditsHandler for Bash)
		Cache:    cache,
		RepoName: permRepoName,
	}

	// Simulate "Allow & Remember" in the TUI (r keybinding).
	cache.RememberAllow(toolNameBash, testNpmTest, permRepoName)

	// The rule should be scoped to "my-service", NOT to "".
	rules := cache.Rules()
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	if rules[0].RepoName != "my-service" {
		t.Errorf("rule.RepoName = %q, want %q (must be per-repo, not global)", rules[0].RepoName, "my-service")
	}

	// Handler should auto-approve the cached command.
	req := ports.ToolPermissionRequest{
		RequestID: "req_1",
		ToolName:  toolNameBash,
		Input:     testNpmTest,
	}
	decision, err := h.CanUseTool(req)
	if err != nil {
		t.Fatalf("CanUseTool: %v", err)
	}
	if decision.Behavior != DecisionAllow {
		t.Errorf("behavior = %q, want allow (cached rule)", decision.Behavior)
	}

	// A handler for a DIFFERENT repo must NOT auto-approve.
	h2 := &CachingHandler{
		Inner:    &mockHandler{behavior: ""},
		Cache:    cache,
		RepoName: "other-service",
	}
	decision2, err := h2.CanUseTool(req)
	if err != nil {
		t.Fatalf("CanUseTool (other repo): %v", err)
	}
	if decision2.Behavior != "" {
		t.Errorf("behavior = %q, want empty (rule scoped to my-service, not other-service)", decision2.Behavior)
	}
}

// TestGlobalScopeSessionRememberAllow is a regression test ensuring that
// pressing "r" on a global-scope session (inquire, research, plan) stores
// the rule under "" and that the session's CachingHandler — also scoped to
// "" — immediately sees it. Previously, the TUI attach flow would
// reconstruct the scope from the feature's first repo name, causing rules
// to be written under a repo name the global-scope handler would never
// match.
func TestGlobalScopeSessionRememberAllow(t *testing.T) {
	cache := NewCache(nil)

	// Handler created for a non-implementation session uses global scope ("").
	handler := &CachingHandler{
		Inner:    &mockHandler{behavior: ""}, // inner defers Bash
		Cache:    cache,
		RepoName: "", // global scope — same as permHandlerFor(..., "")
	}

	req := ports.ToolPermissionRequest{
		RequestID: "req_1",
		ToolName:  toolNameBash,
		Input:     "go vet ./...",
	}

	// Before remembering: handler should defer (no cached rule).
	decision, err := handler.CanUseTool(req)
	if err != nil {
		t.Fatalf("CanUseTool before remember: %v", err)
	}
	if decision.Behavior != "" {
		t.Errorf("before remember: behavior = %q, want empty (defer)", decision.Behavior)
	}

	// Simulate pressing "r" in the TUI attach view. The TUI now uses
	// sess.PermCacheScope (which is "" for global-scope sessions) as the
	// repoName argument to RememberAllow, matching the handler's scope.
	cache.RememberAllow(toolNameBash, "go vet ./...", "" /* PermCacheScope = "" */)

	// After remembering: handler must auto-approve.
	decision, err = handler.CanUseTool(req)
	if err != nil {
		t.Fatalf("CanUseTool after remember: %v", err)
	}
	if decision.Behavior != DecisionAllow {
		t.Errorf("after remember: behavior = %q, want allow (global-scope cache hit)", decision.Behavior)
	}

	// A repo-scoped handler should also see global rules (global matches any repo).
	repoHandler := &CachingHandler{
		Inner:    &mockHandler{behavior: ""},
		Cache:    cache,
		RepoName: "some-repo",
	}
	decision, err = repoHandler.CanUseTool(req)
	if err != nil {
		t.Fatalf("CanUseTool repo handler: %v", err)
	}
	if decision.Behavior != DecisionAllow {
		t.Errorf("repo handler: behavior = %q, want allow (global rule matches any repo)", decision.Behavior)
	}
}

// ---------------------------------------------------------------------------
// LoadAndMerge tests
// ---------------------------------------------------------------------------

func TestCache_LoadAndMerge_GlobalOnly(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	rules := []Rule{{ToolPattern: patternBashEcho, Effect: DecisionAllow}}
	if err := s.Save(globalScope, rules); err != nil {
		t.Fatalf("Save: %v", err)
	}

	c := NewCache(s)
	c.LoadAndMerge("")

	cached := c.Rules()
	if len(cached) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(cached))
	}
	if cached[0].ToolPattern != patternBashEcho {
		t.Errorf("rule pattern = %q, want %q", cached[0].ToolPattern, patternBashEcho)
	}
	if cached[0].RepoName != "" {
		t.Errorf("rule RepoName = %q, want empty (global)", cached[0].RepoName)
	}
}

func TestCache_LoadAndMerge_GlobalAndRepo(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	if err := s.Save(globalScope, []Rule{{ToolPattern: patternBashEcho, Effect: DecisionAllow}}); err != nil {
		t.Fatalf("Save global: %v", err)
	}
	if err := s.Save(testMyRepo, []Rule{{ToolPattern: patternBashGoTest, Effect: DecisionAllow, RepoName: testMyRepo}}); err != nil {
		t.Fatalf("Save repo: %v", err)
	}

	c := NewCache(s)
	c.LoadAndMerge(testMyRepo)

	cached := c.Rules()
	if len(cached) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(cached))
	}
}

func TestCache_LoadAndMerge_Additive(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	if err := s.Save(testRepoA, []Rule{{ToolPattern: "Bash(make *)", Effect: DecisionAllow, RepoName: testRepoA}}); err != nil {
		t.Fatalf("Save repo-a: %v", err)
	}
	if err := s.Save("repo-b", []Rule{{ToolPattern: "Bash(cargo *)", Effect: DecisionAllow, RepoName: "repo-b"}}); err != nil {
		t.Fatalf("Save repo-b: %v", err)
	}

	c := NewCache(s)
	c.LoadAndMerge(testRepoA)
	c.LoadAndMerge("repo-b")

	cached := c.Rules()
	if len(cached) != 2 {
		t.Fatalf("expected 2 rules (additive), got %d", len(cached))
	}
}

func TestCache_LoadAndMerge_Deduplicates(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	if err := s.Save(testMyRepo, []Rule{{ToolPattern: patternBashGoTest, Effect: DecisionAllow, RepoName: testMyRepo}}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	c := NewCache(s)
	// Pre-load the same rule into the cache
	c.RememberAllow(toolNameBash, testGoTestDotDotDot, testMyRepo)
	// Now load from disk — should deduplicate
	c.LoadAndMerge(testMyRepo)

	cached := c.Rules()
	if len(cached) != 1 {
		t.Fatalf("expected 1 rule (dedup), got %d", len(cached))
	}
}

func TestCache_LoadAndMerge_NoStore(t *testing.T) {
	c := NewCache(nil)
	// Should not panic
	c.LoadAndMerge(testMyRepo)
	if len(c.Rules()) != 0 {
		t.Errorf("expected 0 rules with nil store")
	}
}

// ---------------------------------------------------------------------------
// RememberAllow persistence tests
// ---------------------------------------------------------------------------

func TestCache_RememberAllow_Persists(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	c := NewCache(s)

	c.RememberAllow(toolNameBash, testGoTestDotDotDot, testMyRepo)

	// Load directly from store to verify persistence
	loaded, err := s.Load(testMyRepo)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("expected 1 rule on disk, got %d", len(loaded))
	}
	if loaded[0].ToolPattern != patternBashGoTest {
		t.Errorf("persisted pattern = %q, want %q", loaded[0].ToolPattern, patternBashGoTest)
	}
}

func TestCache_RememberAllow_ConcurrentSafety(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	c := NewCache(s)

	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func(n int) {
			defer func() { done <- struct{}{} }()
			c.RememberAllow(toolNameBash, fmt.Sprintf("cmd-%d", n), "repo")
		}(i)
	}
	for i := 0; i < 10; i++ {
		<-done
	}

	rules := c.Rules()
	if len(rules) != 10 {
		t.Errorf("expected 10 rules, got %d", len(rules))
	}

	// Verify all rules reached disk
	loaded, err := s.Load("repo")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded) != 10 {
		t.Errorf("expected 10 rules on disk, got %d", len(loaded))
	}
}

// TestCacheCheckConcurrentSafety locks in the RWMutex contract that Cache's
// concurrent readers (Check) and writers (RememberAllow) rely on. The
// orchestrator calls both concurrently from per-repo goroutines once stages
// fan out; this test asserts that mixing reads and writes does not race and
// produces a deterministic post-condition (every write op persists a rule).
func TestCacheCheckConcurrentSafety(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	c := NewCache(s)

	// Pre-seed some rules so Check has matchable patterns to look up.
	for i := 0; i < 5; i++ {
		c.RememberAllow(toolNameBash, fmt.Sprintf("seed-%d", i), "repo")
	}

	const goroutines = 20
	const opsPerGoroutine = 50
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for op := 0; op < opsPerGoroutine; op++ {
				// Mix of operations: every 4th op is a write, the rest are reads.
				if op%4 == 0 {
					c.RememberAllow(toolNameBash, fmt.Sprintf("g%d-op%d", g, op), "repo")
				} else {
					_, _ = c.Check(toolNameBash, fmt.Sprintf("seed-%d", op%5), "repo")
				}
			}
		}(g)
	}
	wg.Wait()

	// Sanity: 5 seeds plus one rule per write op (every 4th op of every
	// goroutine). RememberAllow is idempotent on duplicates, but each
	// goroutine writes a distinct (g, op) tuple so duplicates cannot occur.
	expectedNewRules := goroutines * (opsPerGoroutine / 4)
	rules := c.Rules()
	if len(rules) < 5+expectedNewRules {
		t.Errorf("expected at least %d rules (5 seeds + %d writes), got %d",
			5+expectedNewRules, expectedNewRules, len(rules))
	}
}

// ---------------------------------------------------------------------------
// SizeGuardHandler tests
// ---------------------------------------------------------------------------

// writeInput builds a JSON-encoded Write tool input with the given content.
func writeInput(content string) string {
	return fmt.Sprintf(`{"file_path":"/tmp/x","content":%q}`, content)
}

func TestSizeGuardHandler_BlocksOversizedClaudeWrite(t *testing.T) {
	inner := &mockHandler{behavior: DecisionAllow}
	h := &SizeGuardHandler{Inner: inner, MaxBytes: 100}

	req := ports.ToolPermissionRequest{
		ToolName:     toolNameWrite,
		Input:        writeInput(strings.Repeat("a", 200)),
		ProviderName: providerNameClaude,
	}
	decision, err := h.CanUseTool(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision.Behavior != DecisionDeny {
		t.Fatalf("Behavior = %q, want deny", decision.Behavior)
	}
	if decision.Reason == "" {
		t.Error("Reason should be non-empty with actionable guidance")
	}
}

func TestSizeGuardHandler_AllowsUnderThreshold(t *testing.T) {
	inner := &mockHandler{behavior: DecisionAllow}
	h := &SizeGuardHandler{Inner: inner, MaxBytes: 1000}

	req := ports.ToolPermissionRequest{
		ToolName:     toolNameWrite,
		Input:        writeInput("small content"),
		ProviderName: providerNameClaude,
	}
	decision, err := h.CanUseTool(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision.Behavior != DecisionAllow {
		t.Errorf("Behavior = %q, want allow (delegated to inner)", decision.Behavior)
	}
}

func TestSizeGuardHandler_IgnoresCodex(t *testing.T) {
	inner := &mockHandler{behavior: DecisionAllow}
	h := &SizeGuardHandler{Inner: inner, MaxBytes: 100}

	req := ports.ToolPermissionRequest{
		ToolName:     toolNameWrite,
		Input:        writeInput(strings.Repeat("a", 500)),
		ProviderName: "codex",
	}
	decision, err := h.CanUseTool(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision.Behavior != DecisionAllow {
		t.Errorf("Behavior = %q, want allow (guard is Claude-only)", decision.Behavior)
	}
}

func TestSizeGuardHandler_IgnoresOtherTools(t *testing.T) {
	inner := &mockHandler{behavior: DecisionAllow}
	h := &SizeGuardHandler{Inner: inner, MaxBytes: 100}

	// Edit is not size-guarded (it streams only a diff, not a full payload).
	req := ports.ToolPermissionRequest{
		ToolName:     toolNameEdit,
		Input:        fmt.Sprintf(`{"file_path":"/tmp/x","new_string":%q}`, strings.Repeat("a", 500)),
		ProviderName: providerNameClaude,
	}
	decision, err := h.CanUseTool(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision.Behavior != DecisionAllow {
		t.Errorf("Behavior = %q, want allow (Edit is not guarded)", decision.Behavior)
	}
}

func TestSizeGuardHandler_DisabledWhenMaxBytesZero(t *testing.T) {
	inner := &mockHandler{behavior: DecisionAllow}
	h := &SizeGuardHandler{Inner: inner, MaxBytes: 0}

	req := ports.ToolPermissionRequest{
		ToolName:     toolNameWrite,
		Input:        writeInput(strings.Repeat("a", 100_000)),
		ProviderName: providerNameClaude,
	}
	decision, err := h.CanUseTool(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision.Behavior != DecisionAllow {
		t.Errorf("Behavior = %q, want allow (MaxBytes=0 disables guard)", decision.Behavior)
	}
}

func TestSizeGuardHandler_DelegatesOnMalformedInput(t *testing.T) {
	// If the input is not valid JSON or lacks a content field, the guard should
	// defer to the inner handler rather than block (fail-open on parse errors).
	inner := &mockHandler{behavior: DecisionDeny}
	h := &SizeGuardHandler{Inner: inner, MaxBytes: 10}

	req := ports.ToolPermissionRequest{
		ToolName:     toolNameWrite,
		Input:        "not json at all",
		ProviderName: providerNameClaude,
	}
	decision, err := h.CanUseTool(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision.Behavior != DecisionDeny {
		t.Errorf("Behavior = %q, want deny from inner handler (guard should delegate on parse failure)", decision.Behavior)
	}
}

func TestSizeGuardHandler_PropagatesInnerError(t *testing.T) {
	boom := errors.New("inner boom")
	inner := &mockHandler{err: boom}
	h := &SizeGuardHandler{Inner: inner, MaxBytes: 1000}

	req := ports.ToolPermissionRequest{
		ToolName:     toolNameWrite,
		Input:        writeInput("small"),
		ProviderName: providerNameClaude,
	}
	_, err := h.CanUseTool(req)
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want %v", err, boom)
	}
}

func TestGuarded_UsesDefaultThreshold(t *testing.T) {
	inner := &mockHandler{behavior: DecisionAllow}
	guard, ok := Guarded(inner).(*SizeGuardHandler)
	if !ok {
		t.Fatalf("Guarded returned %T, want *SizeGuardHandler", guard)
	}
	if guard.MaxBytes != DefaultWriteGuardBytes {
		t.Errorf("MaxBytes = %d, want %d", guard.MaxBytes, DefaultWriteGuardBytes)
	}
	if guard.Inner != inner {
		t.Error("Guarded did not preserve inner handler")
	}
}
