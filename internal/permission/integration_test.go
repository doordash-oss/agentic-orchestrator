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
	"os"
	"path/filepath"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

func TestInferThenMatch_Integration(t *testing.T) {
	cache := NewCache(nil)
	inner := &mockHandler{behavior: ""}

	handler := &CachingHandler{
		Inner:    inner,
		Cache:    cache,
		RepoName: "my-repo",
	}

	// Simulate: user runs "npm test --coverage", presses "r"
	cache.RememberAllow("Bash", "npm test --coverage", "my-repo")

	// Verify the inferred pattern
	rules := cache.Rules()
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	if rules[0].ToolPattern != "Bash(npm test *)" {
		t.Errorf("inferred pattern = %q, want %q", rules[0].ToolPattern, "Bash(npm test *)")
	}

	// The SAME command should be auto-approved
	decision, err := handler.CanUseTool(ports.ToolPermissionRequest{
		RequestID: "req_1",
		ToolName:  "Bash",
		Input:     "npm test --coverage",
	})
	if err != nil {
		t.Fatalf("CanUseTool: %v", err)
	}
	if decision.Behavior != "allow" {
		t.Errorf("same command: behavior = %q, want allow", decision.Behavior)
	}

	// A DIFFERENT npm test variant should also be auto-approved
	decision, err = handler.CanUseTool(ports.ToolPermissionRequest{
		RequestID: "req_2",
		ToolName:  "Bash",
		Input:     "npm test src/",
	})
	if err != nil {
		t.Fatalf("CanUseTool: %v", err)
	}
	if decision.Behavior != "allow" {
		t.Errorf("different suffix: behavior = %q, want allow", decision.Behavior)
	}

	// A completely different command should still defer
	decision, err = handler.CanUseTool(ports.ToolPermissionRequest{
		RequestID: "req_3",
		ToolName:  "Bash",
		Input:     "npm install",
	})
	if err != nil {
		t.Fatalf("CanUseTool: %v", err)
	}
	if decision.Behavior != "" {
		t.Errorf("different command: behavior = %q, want empty (defer)", decision.Behavior)
	}
}

// TestInferThenMatch_JSONPayload_Integration tests the full end-to-end flow
// using the real runtime JSON payload shape ({"command":"..."}) that the
// Claude CLI wire protocol sends through session.go and attach.go.
func TestInferThenMatch_JSONPayload_Integration(t *testing.T) {
	cache := NewCache(nil)
	handler := &CachingHandler{
		Inner:    &mockHandler{behavior: ""},
		Cache:    cache,
		RepoName: "my-repo",
	}

	// Simulate: TUI "r" keybinding passes raw JSON from pendingPermToolInput
	jsonInput := `{"command":"npm test --coverage"}`
	cache.RememberAllow("Bash", jsonInput, "my-repo")

	// Verify the inferred pattern extracts the command and builds a wildcard
	rules := cache.Rules()
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	if rules[0].ToolPattern != "Bash(npm test *)" {
		t.Errorf("inferred pattern = %q, want %q", rules[0].ToolPattern, "Bash(npm test *)")
	}

	// The SAME command (JSON payload) should be auto-approved
	decision, err := handler.CanUseTool(ports.ToolPermissionRequest{
		RequestID: "req_1",
		ToolName:  "Bash",
		Input:     `{"command":"npm test --coverage"}`,
	})
	if err != nil {
		t.Fatalf("CanUseTool same command: %v", err)
	}
	if decision.Behavior != "allow" {
		t.Errorf("same command: behavior = %q, want allow", decision.Behavior)
	}

	// A DIFFERENT npm test variant (JSON) should also be auto-approved
	decision, err = handler.CanUseTool(ports.ToolPermissionRequest{
		RequestID: "req_2",
		ToolName:  "Bash",
		Input:     `{"command":"npm test src/"}`,
	})
	if err != nil {
		t.Fatalf("CanUseTool different suffix: %v", err)
	}
	if decision.Behavior != "allow" {
		t.Errorf("different suffix: behavior = %q, want allow", decision.Behavior)
	}

	// A completely different command (JSON) should still defer
	decision, err = handler.CanUseTool(ports.ToolPermissionRequest{
		RequestID: "req_3",
		ToolName:  "Bash",
		Input:     `{"command":"npm install"}`,
	})
	if err != nil {
		t.Fatalf("CanUseTool different command: %v", err)
	}
	if decision.Behavior != "" {
		t.Errorf("different command: behavior = %q, want empty (defer)", decision.Behavior)
	}
}

func TestCachingHandler_Integration(t *testing.T) {
	cache := NewCache(nil)

	// Inner handler always defers (empty behavior).
	inner := &mockHandler{behavior: ""}

	// Handler 1: wraps the mock with repo scope "my-repo".
	handler1 := &CachingHandler{
		Inner:    inner,
		Cache:    cache,
		RepoName: "my-repo",
	}

	// Pre-load an allow rule for "npm test".
	cache.RememberAllow("Bash", "npm test", "my-repo")

	// Step 1: cached rule should produce an allow decision.
	req1 := ports.ToolPermissionRequest{
		RequestID: "req_1",
		ToolName:  "Bash",
		Input:     "npm test",
	}
	decision, err := handler1.CanUseTool(req1)
	if err != nil {
		t.Fatalf("step 1: CanUseTool: %v", err)
	}
	if decision.Behavior != "allow" {
		t.Errorf("step 1: behavior = %q, want allow", decision.Behavior)
	}

	// Step 2: no cached rule for "rm -rf /" — should defer.
	req2 := ports.ToolPermissionRequest{
		RequestID: "req_2",
		ToolName:  "Bash",
		Input:     "rm -rf /",
	}
	decision, err = handler1.CanUseTool(req2)
	if err != nil {
		t.Fatalf("step 2: CanUseTool: %v", err)
	}
	if decision.Behavior != "" {
		t.Errorf("step 2: behavior = %q, want empty (defer)", decision.Behavior)
	}

	// Step 3: remember "rm -rf /" as allowed.
	cache.RememberAllow("Bash", "rm -rf /", "my-repo")

	// Step 4: now the same request should be allowed.
	decision, err = handler1.CanUseTool(req2)
	if err != nil {
		t.Fatalf("step 4: CanUseTool: %v", err)
	}
	if decision.Behavior != "allow" {
		t.Errorf("step 4: behavior = %q, want allow", decision.Behavior)
	}

	// Step 5: create a second handler with a fresh mock, same cache, same repo.
	inner2 := &mockHandler{behavior: ""}
	handler2 := &CachingHandler{
		Inner:    inner2,
		Cache:    cache,
		RepoName: "my-repo",
	}

	// The rule remembered via handler1's cache should be visible to handler2.
	decision, err = handler2.CanUseTool(req2)
	if err != nil {
		t.Fatalf("step 5: CanUseTool: %v", err)
	}
	if decision.Behavior != "allow" {
		t.Errorf("step 5: behavior = %q, want allow (cross-session sharing)", decision.Behavior)
	}
}

func TestPersistence_EndToEnd(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	// 1. Create a temp repo with settings
	repoDir := t.TempDir()
	claudeDir := filepath.Join(repoDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	settingsJSON := `{"permissions": {"allow": ["Bash(go:*)"]}}`
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(settingsJSON), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// 2. Import repo settings
	if err := ImportRepoSettings(repoDir, "my-repo", s); err != nil {
		t.Fatalf("ImportRepoSettings: %v", err)
	}

	// 3. Create cache and load
	cache := NewCache(s)
	cache.LoadAndMerge("my-repo")

	// 4. Verify imported rule auto-approves
	h := &CachingHandler{
		Inner:    &mockHandler{behavior: ""},
		Cache:    cache,
		RepoName: "my-repo",
	}
	decision, err := h.CanUseTool(ports.ToolPermissionRequest{
		RequestID: "req_1",
		ToolName:  "Bash",
		Input:     "go test ./...",
	})
	if err != nil {
		t.Fatalf("CanUseTool: %v", err)
	}
	if decision.Behavior != "allow" {
		t.Errorf("imported rule: behavior = %q, want allow", decision.Behavior)
	}

	// 5. Remember a new rule
	cache.RememberAllow("Bash", "npm test", "my-repo")

	// 6. Simulate restart: new cache, same store
	cache2 := NewCache(s)
	cache2.LoadAndMerge("my-repo")

	// 7. Verify "npm test" survived the "restart"
	h2 := &CachingHandler{
		Inner:    &mockHandler{behavior: ""},
		Cache:    cache2,
		RepoName: "my-repo",
	}
	decision, err = h2.CanUseTool(ports.ToolPermissionRequest{
		RequestID: "req_2",
		ToolName:  "Bash",
		Input:     "npm test",
	})
	if err != nil {
		t.Fatalf("CanUseTool (restart): %v", err)
	}
	if decision.Behavior != "allow" {
		t.Errorf("remembered rule after restart: behavior = %q, want allow", decision.Behavior)
	}

	// 8. Verify imported rule also survived
	decision, err = h2.CanUseTool(ports.ToolPermissionRequest{
		RequestID: "req_3",
		ToolName:  "Bash",
		Input:     "go build ./...",
	})
	if err != nil {
		t.Fatalf("CanUseTool (import after restart): %v", err)
	}
	if decision.Behavior != "allow" {
		t.Errorf("imported rule after restart: behavior = %q, want allow", decision.Behavior)
	}
}

func TestBootstrapDefaults_AvailableToCachingHandlerOnFirstLoad(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	if err := s.EnsureGlobalDefaults(); err != nil {
		t.Fatalf("EnsureGlobalDefaults: %v", err)
	}

	cache := NewCache(s)
	cache.LoadAndMerge("my-repo")

	handler := &CachingHandler{
		Inner:    &mockHandler{behavior: ""},
		Cache:    cache,
		RepoName: "my-repo",
	}

	decision, err := handler.CanUseTool(ports.ToolPermissionRequest{
		RequestID: "req_ls",
		ToolName:  "Bash",
		Input:     `{"command":"git diff --stat"}`,
	})
	if err != nil {
		t.Fatalf("CanUseTool(git diff): %v", err)
	}
	if decision.Behavior != "allow" {
		t.Errorf("git diff behavior = %q, want allow", decision.Behavior)
	}

	decision, err = handler.CanUseTool(ports.ToolPermissionRequest{
		RequestID: "req_rm",
		ToolName:  "Bash",
		Input:     `{"command":"rm -rf /tmp/bootstrap-defaults"}`,
	})
	if err != nil {
		t.Fatalf("CanUseTool(rm): %v", err)
	}
	if decision.Behavior != "" {
		t.Errorf("rm behavior = %q, want empty (defer)", decision.Behavior)
	}
}

func TestImport_AvailableToFirstSession(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	// Set up a temp repo with settings
	repoDir := t.TempDir()
	claudeDir := filepath.Join(repoDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	settingsJSON := `{"permissions": {"allow": ["Bash(go test:*)"]}}`
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(settingsJSON), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Synchronous import + load (mirrors creation + TUI handler flow)
	if err := ImportRepoSettings(repoDir, "my-repo", s); err != nil {
		t.Fatalf("ImportRepoSettings: %v", err)
	}
	cache := NewCache(s)
	cache.LoadAndMerge("my-repo")

	// Immediately create a handler (mirrors permHandlerFor)
	h := &CachingHandler{
		Inner:    &mockHandler{behavior: ""},
		Cache:    cache,
		RepoName: "my-repo",
	}

	// Should auto-approve matching command
	decision, err := h.CanUseTool(ports.ToolPermissionRequest{
		RequestID: "req_1",
		ToolName:  "Bash",
		Input:     "go test ./...",
	})
	if err != nil {
		t.Fatalf("CanUseTool: %v", err)
	}
	if decision.Behavior != "allow" {
		t.Errorf("first session: behavior = %q, want allow", decision.Behavior)
	}

	// Should NOT auto-approve unmatched command
	decision, err = h.CanUseTool(ports.ToolPermissionRequest{
		RequestID: "req_2",
		ToolName:  "Bash",
		Input:     "rm -rf /",
	})
	if err != nil {
		t.Fatalf("CanUseTool: %v", err)
	}
	if decision.Behavior != "" {
		t.Errorf("unmatched command: behavior = %q, want empty (defer)", decision.Behavior)
	}
}

func TestGlobalSeededRules_CoexistWithRepoImportsAndRememberedApprovals_AfterRestart(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	if err := store.EnsureGlobalDefaults(); err != nil {
		t.Fatalf("EnsureGlobalDefaults: %v", err)
	}

	repoDir := t.TempDir()
	claudeDir := filepath.Join(repoDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	settingsJSON := `{"permissions": {"allow": ["Bash(go test:*)"]}}`
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(settingsJSON), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := ImportRepoSettings(repoDir, "my-repo", store); err != nil {
		t.Fatalf("ImportRepoSettings: %v", err)
	}

	cache := NewCache(store)
	cache.LoadAndMerge("my-repo")
	cache.RememberAllow("Bash", `{"command":"npm test --coverage"}`, "my-repo")

	repoHandler := &CachingHandler{
		Inner:    &mockHandler{behavior: ""},
		Cache:    cache,
		RepoName: "my-repo",
	}
	otherRepoHandler := &CachingHandler{
		Inner:    &mockHandler{behavior: ""},
		Cache:    cache,
		RepoName: "other-repo",
	}

	assertDecisionBehavior(t, repoHandler, `{"command":"ls -la"}`, "allow")
	assertDecisionBehavior(t, repoHandler, "go test ./...", "allow")
	assertDecisionBehavior(t, repoHandler, `{"command":"npm test src/"}`, "allow")
	assertDecisionBehavior(t, otherRepoHandler, `{"command":"ls -la"}`, "allow")
	assertDecisionBehavior(t, otherRepoHandler, "go test ./...", "")
	assertDecisionBehavior(t, otherRepoHandler, `{"command":"npm test src/"}`, "")

	cacheAfterRestart := NewCache(store)
	cacheAfterRestart.LoadAndMerge("my-repo")

	repoHandlerAfterRestart := &CachingHandler{
		Inner:    &mockHandler{behavior: ""},
		Cache:    cacheAfterRestart,
		RepoName: "my-repo",
	}
	otherRepoHandlerAfterRestart := &CachingHandler{
		Inner:    &mockHandler{behavior: ""},
		Cache:    cacheAfterRestart,
		RepoName: "other-repo",
	}

	assertDecisionBehavior(t, repoHandlerAfterRestart, `{"command":"ls -la"}`, "allow")
	assertDecisionBehavior(t, repoHandlerAfterRestart, "go test ./...", "allow")
	assertDecisionBehavior(t, repoHandlerAfterRestart, `{"command":"npm test src/"}`, "allow")
	assertDecisionBehavior(t, otherRepoHandlerAfterRestart, `{"command":"ls -la"}`, "allow")
	assertDecisionBehavior(t, otherRepoHandlerAfterRestart, "go test ./...", "")
	assertDecisionBehavior(t, otherRepoHandlerAfterRestart, `{"command":"npm test src/"}`, "")
}

func assertDecisionBehavior(t *testing.T, handler *CachingHandler, input, want string) {
	t.Helper()

	decision, err := handler.CanUseTool(ports.ToolPermissionRequest{
		RequestID: "req",
		ToolName:  "Bash",
		Input:     input,
	})
	if err != nil {
		t.Fatalf("CanUseTool(%q): %v", input, err)
	}
	if decision.Behavior != want {
		t.Fatalf("CanUseTool(%q) behavior = %q, want %q", input, decision.Behavior, want)
	}
}

// TestAutoReviewHandler_Integration tests the full end-to-end auto-review
// path: AcceptEditsHandler defers Bash → CachingHandler miss →
// AutoReviewHandler with a mock-allow classifier → session cache populated →
// a second identical call returns allow without re-invoking the classifier.
func TestAutoReviewHandler_Integration(t *testing.T) {
	sharedCache := NewCache(nil)
	inner := &AcceptEditsHandler{}
	caching := &CachingHandler{
		Inner:    inner,
		Cache:    sharedCache,
		RepoName: "my-repo",
	}

	classifyCalls := 0
	classify := func(_, _ string) (bool, error) {
		classifyCalls++
		return true, nil
	}

	autoReview := &AutoReviewHandler{
		Inner:    caching,
		Cache:    NewCache(nil), // session-only cache with nil Store
		Classify: classify,
	}

	req := ports.ToolPermissionRequest{
		RequestID: "req-1",
		ToolName:  "Bash",
		Input:     `{"command":"go test ./..."}`,
	}

	// First call: inner defers, caching misses, classify allows.
	decision, err := autoReview.CanUseTool(req)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if decision.Behavior != "allow" {
		t.Errorf("first call: behavior = %q, want allow", decision.Behavior)
	}
	if classifyCalls != 1 {
		t.Errorf("classify called %d times on first call, want 1", classifyCalls)
	}

	// Second identical call: should be served from session cache.
	classifyCalls = 0
	decision, err = autoReview.CanUseTool(req)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if decision.Behavior != "allow" {
		t.Errorf("second call: behavior = %q, want allow (session cache hit)", decision.Behavior)
	}
	if classifyCalls != 0 {
		t.Errorf("classify called %d times on second call, want 0 (cache hit)", classifyCalls)
	}

	// Verify the session cache's Store is nil (no persistence).
	if autoReview.Cache.StoreRef() != nil {
		t.Error("session cache Store should be nil")
	}
}
