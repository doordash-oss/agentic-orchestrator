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
		RepoName: testMyRepo,
	}

	// Simulate: user runs testNpmTestCoverage, presses "r"
	cache.RememberAllow(toolNameBash, testNpmTestCoverage, testMyRepo)

	// Verify the inferred pattern
	rules := cache.Rules()
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	if rules[0].ToolPattern != patternBashNpmTest {
		t.Errorf("inferred pattern = %q, want %q", rules[0].ToolPattern, patternBashNpmTest)
	}

	// The SAME command should be auto-approved
	decision, err := handler.CanUseTool(ports.ToolPermissionRequest{
		RequestID: "req_1",
		ToolName:  toolNameBash,
		Input:     testNpmTestCoverage,
	})
	if err != nil {
		t.Fatalf("CanUseTool: %v", err)
	}
	if decision.Behavior != DecisionAllow {
		t.Errorf("same command: behavior = %q, want allow", decision.Behavior)
	}

	// A DIFFERENT npm test variant should also be auto-approved
	decision, err = handler.CanUseTool(ports.ToolPermissionRequest{
		RequestID: "req_2",
		ToolName:  toolNameBash,
		Input:     "npm test src/",
	})
	if err != nil {
		t.Fatalf("CanUseTool: %v", err)
	}
	if decision.Behavior != DecisionAllow {
		t.Errorf("different suffix: behavior = %q, want allow", decision.Behavior)
	}

	// A completely different command should still defer
	decision, err = handler.CanUseTool(ports.ToolPermissionRequest{
		RequestID: "req_3",
		ToolName:  toolNameBash,
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
		RepoName: testMyRepo,
	}

	// Simulate: TUI "r" keybinding passes raw JSON from pendingPermToolInput
	jsonInput := testJSONNpmTestCoverage
	cache.RememberAllow(toolNameBash, jsonInput, testMyRepo)

	// Verify the inferred pattern extracts the command and builds a wildcard
	rules := cache.Rules()
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	if rules[0].ToolPattern != patternBashNpmTest {
		t.Errorf("inferred pattern = %q, want %q", rules[0].ToolPattern, patternBashNpmTest)
	}

	// The SAME command (JSON payload) should be auto-approved
	decision, err := handler.CanUseTool(ports.ToolPermissionRequest{
		RequestID: "req_1",
		ToolName:  toolNameBash,
		Input:     testJSONNpmTestCoverage,
	})
	if err != nil {
		t.Fatalf("CanUseTool same command: %v", err)
	}
	if decision.Behavior != DecisionAllow {
		t.Errorf("same command: behavior = %q, want allow", decision.Behavior)
	}

	// A DIFFERENT npm test variant (JSON) should also be auto-approved
	decision, err = handler.CanUseTool(ports.ToolPermissionRequest{
		RequestID: "req_2",
		ToolName:  toolNameBash,
		Input:     `{"command":"npm test src/"}`,
	})
	if err != nil {
		t.Fatalf("CanUseTool different suffix: %v", err)
	}
	if decision.Behavior != DecisionAllow {
		t.Errorf("different suffix: behavior = %q, want allow", decision.Behavior)
	}

	// A completely different command (JSON) should still defer
	decision, err = handler.CanUseTool(ports.ToolPermissionRequest{
		RequestID: "req_3",
		ToolName:  toolNameBash,
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

	// Handler 1: wraps the mock with repo scope testMyRepo.
	handler1 := &CachingHandler{
		Inner:    inner,
		Cache:    cache,
		RepoName: testMyRepo,
	}

	// Pre-load an allow rule for testNpmTest.
	cache.RememberAllow(toolNameBash, testNpmTest, testMyRepo)

	// Step 1: cached rule should produce an allow decision.
	req1 := ports.ToolPermissionRequest{
		RequestID: "req_1",
		ToolName:  toolNameBash,
		Input:     testNpmTest,
	}
	decision, err := handler1.CanUseTool(req1)
	if err != nil {
		t.Fatalf("step 1: CanUseTool: %v", err)
	}
	if decision.Behavior != DecisionAllow {
		t.Errorf("step 1: behavior = %q, want allow", decision.Behavior)
	}

	// Step 2: no cached rule for testRmRfRoot — should defer.
	req2 := ports.ToolPermissionRequest{
		RequestID: "req_2",
		ToolName:  toolNameBash,
		Input:     testRmRfRoot,
	}
	decision, err = handler1.CanUseTool(req2)
	if err != nil {
		t.Fatalf("step 2: CanUseTool: %v", err)
	}
	if decision.Behavior != "" {
		t.Errorf("step 2: behavior = %q, want empty (defer)", decision.Behavior)
	}

	// Step 3: remember testRmRfRoot as allowed.
	cache.RememberAllow(toolNameBash, testRmRfRoot, testMyRepo)

	// Step 4: now the same request should be allowed.
	decision, err = handler1.CanUseTool(req2)
	if err != nil {
		t.Fatalf("step 4: CanUseTool: %v", err)
	}
	if decision.Behavior != DecisionAllow {
		t.Errorf("step 4: behavior = %q, want allow", decision.Behavior)
	}

	// Step 5: create a second handler with a fresh mock, same cache, same repo.
	inner2 := &mockHandler{behavior: ""}
	handler2 := &CachingHandler{
		Inner:    inner2,
		Cache:    cache,
		RepoName: testMyRepo,
	}

	// The rule remembered via handler1's cache should be visible to handler2.
	decision, err = handler2.CanUseTool(req2)
	if err != nil {
		t.Fatalf("step 5: CanUseTool: %v", err)
	}
	if decision.Behavior != DecisionAllow {
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
	if err := ImportRepoSettings(repoDir, testMyRepo, s); err != nil {
		t.Fatalf("ImportRepoSettings: %v", err)
	}

	// 3. Create cache and load
	cache := NewCache(s)
	cache.LoadAndMerge(testMyRepo)

	// 4. Verify imported rule auto-approves
	h := &CachingHandler{
		Inner:    &mockHandler{behavior: ""},
		Cache:    cache,
		RepoName: testMyRepo,
	}
	decision, err := h.CanUseTool(ports.ToolPermissionRequest{
		RequestID: "req_1",
		ToolName:  toolNameBash,
		Input:     testGoTestDotDotDot,
	})
	if err != nil {
		t.Fatalf("CanUseTool: %v", err)
	}
	if decision.Behavior != DecisionAllow {
		t.Errorf("imported rule: behavior = %q, want allow", decision.Behavior)
	}

	// 5. Remember a new rule
	cache.RememberAllow(toolNameBash, testNpmTest, testMyRepo)

	// 6. Simulate restart: new cache, same store
	cache2 := NewCache(s)
	cache2.LoadAndMerge(testMyRepo)

	// 7. Verify testNpmTest survived the "restart"
	h2 := &CachingHandler{
		Inner:    &mockHandler{behavior: ""},
		Cache:    cache2,
		RepoName: testMyRepo,
	}
	decision, err = h2.CanUseTool(ports.ToolPermissionRequest{
		RequestID: "req_2",
		ToolName:  toolNameBash,
		Input:     testNpmTest,
	})
	if err != nil {
		t.Fatalf("CanUseTool (restart): %v", err)
	}
	if decision.Behavior != DecisionAllow {
		t.Errorf("remembered rule after restart: behavior = %q, want allow", decision.Behavior)
	}

	// 8. Verify imported rule also survived
	decision, err = h2.CanUseTool(ports.ToolPermissionRequest{
		RequestID: "req_3",
		ToolName:  toolNameBash,
		Input:     "go build ./...",
	})
	if err != nil {
		t.Fatalf("CanUseTool (import after restart): %v", err)
	}
	if decision.Behavior != DecisionAllow {
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
	cache.LoadAndMerge(testMyRepo)

	handler := &CachingHandler{
		Inner:    &mockHandler{behavior: ""},
		Cache:    cache,
		RepoName: testMyRepo,
	}

	decision, err := handler.CanUseTool(ports.ToolPermissionRequest{
		RequestID: "req_ls",
		ToolName:  toolNameBash,
		Input:     `{"command":"git diff --stat"}`,
	})
	if err != nil {
		t.Fatalf("CanUseTool(git diff): %v", err)
	}
	if decision.Behavior != DecisionAllow {
		t.Errorf("git diff behavior = %q, want allow", decision.Behavior)
	}

	decision, err = handler.CanUseTool(ports.ToolPermissionRequest{
		RequestID: "req_rm",
		ToolName:  toolNameBash,
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
	if err := ImportRepoSettings(repoDir, testMyRepo, s); err != nil {
		t.Fatalf("ImportRepoSettings: %v", err)
	}
	cache := NewCache(s)
	cache.LoadAndMerge(testMyRepo)

	// Immediately create a handler (mirrors permHandlerFor)
	h := &CachingHandler{
		Inner:    &mockHandler{behavior: ""},
		Cache:    cache,
		RepoName: testMyRepo,
	}

	// Should auto-approve matching command
	decision, err := h.CanUseTool(ports.ToolPermissionRequest{
		RequestID: "req_1",
		ToolName:  toolNameBash,
		Input:     testGoTestDotDotDot,
	})
	if err != nil {
		t.Fatalf("CanUseTool: %v", err)
	}
	if decision.Behavior != DecisionAllow {
		t.Errorf("first session: behavior = %q, want allow", decision.Behavior)
	}

	// Should NOT auto-approve unmatched command
	decision, err = h.CanUseTool(ports.ToolPermissionRequest{
		RequestID: "req_2",
		ToolName:  toolNameBash,
		Input:     testRmRfRoot,
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
	if err := ImportRepoSettings(repoDir, testMyRepo, store); err != nil {
		t.Fatalf("ImportRepoSettings: %v", err)
	}

	cache := NewCache(store)
	cache.LoadAndMerge(testMyRepo)
	cache.RememberAllow(toolNameBash, testJSONNpmTestCoverage, testMyRepo)

	repoHandler := &CachingHandler{
		Inner:    &mockHandler{behavior: ""},
		Cache:    cache,
		RepoName: testMyRepo,
	}
	otherRepoHandler := &CachingHandler{
		Inner:    &mockHandler{behavior: ""},
		Cache:    cache,
		RepoName: "other-repo",
	}

	assertDecisionBehavior(t, repoHandler, testJSONLsLa, DecisionAllow)
	assertDecisionBehavior(t, repoHandler, testGoTestDotDotDot, DecisionAllow)
	assertDecisionBehavior(t, repoHandler, `{"command":"npm test src/"}`, DecisionAllow)
	assertDecisionBehavior(t, otherRepoHandler, testJSONLsLa, DecisionAllow)
	assertDecisionBehavior(t, otherRepoHandler, testGoTestDotDotDot, "")
	assertDecisionBehavior(t, otherRepoHandler, `{"command":"npm test src/"}`, "")

	cacheAfterRestart := NewCache(store)
	cacheAfterRestart.LoadAndMerge(testMyRepo)

	repoHandlerAfterRestart := &CachingHandler{
		Inner:    &mockHandler{behavior: ""},
		Cache:    cacheAfterRestart,
		RepoName: testMyRepo,
	}
	otherRepoHandlerAfterRestart := &CachingHandler{
		Inner:    &mockHandler{behavior: ""},
		Cache:    cacheAfterRestart,
		RepoName: "other-repo",
	}

	assertDecisionBehavior(t, repoHandlerAfterRestart, testJSONLsLa, DecisionAllow)
	assertDecisionBehavior(t, repoHandlerAfterRestart, testGoTestDotDotDot, DecisionAllow)
	assertDecisionBehavior(t, repoHandlerAfterRestart, `{"command":"npm test src/"}`, DecisionAllow)
	assertDecisionBehavior(t, otherRepoHandlerAfterRestart, testJSONLsLa, DecisionAllow)
	assertDecisionBehavior(t, otherRepoHandlerAfterRestart, testGoTestDotDotDot, "")
	assertDecisionBehavior(t, otherRepoHandlerAfterRestart, `{"command":"npm test src/"}`, "")
}

func assertDecisionBehavior(t *testing.T, handler *CachingHandler, input, want string) {
	t.Helper()

	decision, err := handler.CanUseTool(ports.ToolPermissionRequest{
		RequestID: "req",
		ToolName:  toolNameBash,
		Input:     input,
	})
	if err != nil {
		t.Fatalf("CanUseTool(%q): %v", input, err)
	}
	if decision.Behavior != want {
		t.Fatalf("CanUseTool(%q) behavior = %q, want %q", input, decision.Behavior, want)
	}
}
