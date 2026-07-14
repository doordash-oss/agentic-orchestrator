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
	"strings"
	"testing"
)

func TestStore_SaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	rules := []Rule{
		{ToolPattern: patternBashGoTest, Effect: DecisionAllow, RepoName: testMyRepo},
		{ToolPattern: patternBashRm, Effect: DecisionDeny, RepoName: testMyRepo},
	}
	if err := s.Save(testMyRepo, rules); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := s.Load(testMyRepo)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(loaded))
	}
	// Verify ToolPattern and Effect preserved
	if loaded[0].ToolPattern != patternBashGoTest || loaded[0].Effect != DecisionAllow {
		t.Errorf("rule 0: got %+v", loaded[0])
	}
	if loaded[1].ToolPattern != patternBashRm || loaded[1].Effect != DecisionDeny {
		t.Errorf("rule 1: got %+v", loaded[1])
	}
	// RepoName inferred from scope
	if loaded[0].RepoName != testMyRepo {
		t.Errorf("rule 0 RepoName = %q, want %q", loaded[0].RepoName, testMyRepo)
	}
}

func TestStore_SaveAndLoad_GlobalScope(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	rules := []Rule{
		{ToolPattern: patternBashEcho, Effect: DecisionAllow},
	}
	if err := s.Save(globalScope, rules); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := s.Load(globalScope)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(loaded))
	}
	// Global scope → RepoName: ""
	if loaded[0].RepoName != "" {
		t.Errorf("global rule RepoName = %q, want empty", loaded[0].RepoName)
	}
}

func TestStore_Load_FileNotExists(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	loaded, err := s.Load("nonexistent")
	if err != nil {
		t.Fatalf("Load: %v (expected nil, nil)", err)
	}
	if loaded != nil {
		t.Errorf("expected nil rules, got %v", loaded)
	}
}

func TestStore_Load_CorruptedJSON(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	// Write garbage to the file
	path := filepath.Join(dir, "corrupt.json")
	if err := os.WriteFile(path, []byte("{{{not json"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	loaded, err := s.Load("corrupt")
	if err != nil {
		t.Fatalf("Load: %v (expected nil, nil for corrupt)", err)
	}
	if loaded != nil {
		t.Errorf("expected nil rules for corrupt file, got %v", loaded)
	}
}

func TestStore_Load_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	// Write empty bytes
	path := filepath.Join(dir, "empty.json")
	if err := os.WriteFile(path, []byte{}, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	loaded, err := s.Load("empty")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded != nil {
		t.Errorf("expected nil rules for empty file, got %v", loaded)
	}
}

func TestStore_AppendRule(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	r1 := Rule{ToolPattern: patternBashGoTest, Effect: DecisionAllow}
	r2 := Rule{ToolPattern: "Bash(go build *)", Effect: DecisionAllow}

	inserted, err := s.AppendRule(testMyRepo, r1)
	if err != nil {
		t.Fatalf("AppendRule 1: %v", err)
	}
	if !inserted {
		t.Fatal("AppendRule 1 inserted = false, want true")
	}
	inserted, err = s.AppendRule(testMyRepo, r2)
	if err != nil {
		t.Fatalf("AppendRule 2: %v", err)
	}
	if !inserted {
		t.Fatal("AppendRule 2 inserted = false, want true")
	}

	loaded, err := s.Load(testMyRepo)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(loaded))
	}
}

func TestStore_AppendRule_Deduplicates(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	r := Rule{ToolPattern: patternBashGoTest, Effect: DecisionAllow}

	inserted, err := s.AppendRule(testMyRepo, r)
	if err != nil {
		t.Fatalf("AppendRule 1: %v", err)
	}
	if !inserted {
		t.Fatal("AppendRule 1 inserted = false, want true")
	}
	inserted, err = s.AppendRule(testMyRepo, r)
	if err != nil {
		t.Fatalf("AppendRule 2: %v", err)
	}
	if inserted {
		t.Fatal("AppendRule 2 inserted = true, want false for duplicate")
	}

	loaded, err := s.Load(testMyRepo)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("expected 1 rule (dedup), got %d", len(loaded))
	}
}

func TestStore_AtomicWrite(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	rules := []Rule{
		{ToolPattern: patternBashEcho, Effect: DecisionAllow},
	}
	if err := s.Save("test-scope", rules); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Verify no temp files remain
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}

	// Verify the actual file exists
	path := filepath.Join(dir, "test-scope.json")
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected file at %s: %v", path, err)
	}
}

func TestScopeFor(t *testing.T) {
	tests := []struct {
		repoName string
		want     string
	}{
		{"", "global"},               // real global scope → global.json
		{testMyRepo, testMyRepo},     // normal repo
		{"other-repo", "other-repo"}, // normal repo
		{"global", "_repo_global"},   // repo named "global" escaped to avoid collision
	}
	for _, tt := range tests {
		got := scopeFor(tt.repoName)
		if got != tt.want {
			t.Errorf("scopeFor(%q) = %q, want %q", tt.repoName, got, tt.want)
		}
	}
}

func TestScopeForEncodesUnsafeRepoNames(t *testing.T) {
	for _, repoName := range []string{"../outside", "org/repo", `org\repo`, "repo with spaces"} {
		t.Run(repoName, func(t *testing.T) {
			scope := scopeFor(repoName)
			if scope == repoName {
				t.Fatalf("scopeFor(%q) = raw scope %q, want encoded", repoName, scope)
			}
			if strings.Contains(scope, "/") || strings.Contains(scope, `\`) || strings.Contains(scope, "..") {
				t.Fatalf("scopeFor(%q) = %q, want safe filename scope", repoName, scope)
			}
			if got := repoNameForScope(scope); got != repoName {
				t.Fatalf("repoNameForScope(scopeFor(%q)) = %q, want original", repoName, got)
			}
		})
	}
}

func TestStoreRejectsUnsafeRawScopeFilenames(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	s := NewStore(dir)
	rule := Rule{ToolPattern: patternBashGoTest, Effect: DecisionAllow}
	if err := s.Save("../outside", []Rule{rule}); err == nil {
		t.Fatal("Save(unsafe scope) error = nil, want error")
	}
	if _, err := s.Load("../outside"); err == nil {
		t.Fatal("Load(unsafe scope) error = nil, want error")
	}
	if _, err := s.AppendRule("../outside", rule); err == nil {
		t.Fatal("AppendRule(unsafe scope) error = nil, want error")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "outside.json")); !os.IsNotExist(err) {
		t.Fatalf("outside stat error = %v, want no escaped write", err)
	}
}

func TestCacheRememberAllowPatternEncodesUnsafeScopeInsidePermissionsDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store := NewStore(dir)
	cache := NewCache(store)
	repoName := "../outside"
	result, err := cache.RememberAllowPattern(patternBashGoTest, repoName)
	if err != nil {
		t.Fatalf("RememberAllowPattern() error = %v", err)
	}
	if !result.Persisted || result.Scope != repoName {
		t.Fatalf("RememberAllowPattern() result = %+v, want original logical scope persisted", result)
	}
	scope := scopeFor(repoName)
	if _, err := os.Stat(filepath.Join(dir, scope+".json")); err != nil {
		t.Fatalf("encoded scope file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "outside.json")); !os.IsNotExist(err) {
		t.Fatalf("outside stat error = %v, want no escaped write", err)
	}
	loaded, err := store.Load(scope)
	if err != nil {
		t.Fatalf("Load(encoded scope) error = %v", err)
	}
	if len(loaded) != 1 || loaded[0].RepoName != repoName {
		t.Fatalf("loaded rules = %+v, want original repo name", loaded)
	}
}

// ---------------------------------------------------------------------------
// normalizePattern tests
// ---------------------------------------------------------------------------

func TestNormalizePattern(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Bash(go:*)", "Bash(go *)"},
		{"Bash(go vet:*)", "Bash(go vet *)"},
		{"Bash(done)", "Bash(done)"},
		{"WebSearch", "WebSearch"},
		{"Bash(npm test:*)", patternBashNpmTest},
		{"WebFetch(domain:x)", "WebFetch(domain:x)"},
		{"Read(//path/**)", "Read(//path/**)"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := normalizePattern(tt.input)
			if got != tt.want {
				t.Errorf("normalizePattern(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ImportRepoSettings tests
// ---------------------------------------------------------------------------

func TestImportRepoSettings_AllowAndDeny(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	repoDir := t.TempDir()
	claudeDir := filepath.Join(repoDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	settingsJSON := `{"permissions": {"allow": ["Bash(go:*)"], "deny": ["Bash(rm:*)"]}}`
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(settingsJSON), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := ImportRepoSettings(repoDir, testMyRepo, s); err != nil {
		t.Fatalf("ImportRepoSettings: %v", err)
	}

	loaded, err := s.Load(testMyRepo)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(loaded))
	}

	// Check allow rule with pattern conversion
	foundAllow := false
	foundDeny := false
	for _, r := range loaded {
		if r.ToolPattern == "Bash(go *)" && r.Effect == DecisionAllow {
			foundAllow = true
		}
		if r.ToolPattern == patternBashRm && r.Effect == DecisionDeny {
			foundDeny = true
		}
	}
	if !foundAllow {
		t.Errorf("expected allow rule 'Bash(go *)', rules: %v", loaded)
	}
	if !foundDeny {
		t.Errorf("expected deny rule 'Bash(rm *)', rules: %v", loaded)
	}
}

func TestImportRepoSettings_LocalSettings(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	repoDir := t.TempDir()
	claudeDir := filepath.Join(repoDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// settings.json has one rule
	settingsJSON := `{"permissions": {"allow": ["Bash(go test:*)"]}}`
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(settingsJSON), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// settings.local.json has another rule
	localJSON := `{"permissions": {"allow": ["Bash(npm test:*)"]}}`
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.local.json"), []byte(localJSON), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := ImportRepoSettings(repoDir, testMyRepo, s); err != nil {
		t.Fatalf("ImportRepoSettings: %v", err)
	}

	loaded, err := s.Load(testMyRepo)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("expected 2 rules (from both files), got %d", len(loaded))
	}
}

func TestImportRepoSettings_NoSettingsFile(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	repoDir := t.TempDir() // no .claude/ directory

	if err := ImportRepoSettings(repoDir, testMyRepo, s); err != nil {
		t.Fatalf("ImportRepoSettings: %v (expected no error for missing files)", err)
	}

	loaded, err := s.Load(testMyRepo)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded != nil {
		t.Errorf("expected nil rules for missing settings, got %v", loaded)
	}
}

func TestImportRepoSettings_CorruptedSettingsJSON(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	repoDir := t.TempDir()
	claudeDir := filepath.Join(repoDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte("{{{bad"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := ImportRepoSettings(repoDir, testMyRepo, s); err != nil {
		t.Fatalf("ImportRepoSettings: %v (expected no error for corrupt file)", err)
	}

	loaded, err := s.Load(testMyRepo)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded != nil {
		t.Errorf("expected nil rules for corrupt settings, got %v", loaded)
	}
}

func TestImportRepoSettings_MergesWithExisting(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	// Pre-populate with an existing rule
	existing := Rule{ToolPattern: "Bash(make *)", Effect: DecisionAllow}
	if inserted, err := s.AppendRule(testMyRepo, existing); err != nil {
		t.Fatalf("AppendRule: %v", err)
	} else if !inserted {
		t.Fatal("AppendRule inserted = false, want true")
	}

	// Import adds a new rule
	repoDir := t.TempDir()
	claudeDir := filepath.Join(repoDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	settingsJSON := `{"permissions": {"allow": ["Bash(go test:*)"]}}`
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(settingsJSON), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := ImportRepoSettings(repoDir, testMyRepo, s); err != nil {
		t.Fatalf("ImportRepoSettings: %v", err)
	}

	loaded, err := s.Load(testMyRepo)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("expected 2 rules (existing + imported), got %d", len(loaded))
	}
}

func TestImportRepoSettings_Idempotent(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	repoDir := t.TempDir()
	claudeDir := filepath.Join(repoDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	settingsJSON := `{"permissions": {"allow": ["Bash(go test:*)"]}}`
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(settingsJSON), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Import twice
	if err := ImportRepoSettings(repoDir, testMyRepo, s); err != nil {
		t.Fatalf("ImportRepoSettings 1: %v", err)
	}
	if err := ImportRepoSettings(repoDir, testMyRepo, s); err != nil {
		t.Fatalf("ImportRepoSettings 2: %v", err)
	}

	loaded, err := s.Load(testMyRepo)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("expected 1 rule (idempotent), got %d", len(loaded))
	}
}

// ---------------------------------------------------------------------------
// Regression test: repo named "global" must not collide with the real global scope
// ---------------------------------------------------------------------------

func TestRepoNamedGlobal_NoCollision(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	// 1. ImportRepoSettings for a repo literally named "global"
	repoDir := t.TempDir()
	claudeDir := filepath.Join(repoDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	settingsJSON := `{"permissions": {"allow": ["Bash(make:*)"]}}`
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(settingsJSON), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := ImportRepoSettings(repoDir, "global", s); err != nil {
		t.Fatalf("ImportRepoSettings for repo 'global': %v", err)
	}

	// The imported rule must land in "_repo_global.json", NOT "global.json" (the real global scope)
	repoRules, err := s.Load(scopeFor("global"))
	if err != nil {
		t.Fatalf("Load repo 'global': %v", err)
	}
	if len(repoRules) != 1 {
		t.Fatalf("expected 1 rule in repo 'global', got %d", len(repoRules))
	}
	if repoRules[0].RepoName != "global" {
		t.Errorf("repo rule RepoName = %q, want %q", repoRules[0].RepoName, "global")
	}

	// The real global scope file (global.json) must be empty/nonexistent
	globalRules, err := s.Load(globalScope)
	if err != nil {
		t.Fatalf("Load global scope: %v", err)
	}
	if len(globalRules) != 0 {
		t.Errorf("expected 0 rules in global scope, got %d (collision!)", len(globalRules))
	}

	// 2. RememberAllow for repo "global" must persist to "_repo_global.json", not "global.json"
	c := NewCache(s)
	c.RememberAllow(toolNameBash, testNpmTestCoverage, "global")

	repoRules2, err := s.Load(scopeFor("global"))
	if err != nil {
		t.Fatalf("Load repo 'global' after RememberAllow: %v", err)
	}
	if len(repoRules2) != 2 {
		t.Fatalf("expected 2 rules in repo 'global', got %d", len(repoRules2))
	}
	globalRules2, err := s.Load(globalScope)
	if err != nil {
		t.Fatalf("Load global scope after RememberAllow: %v", err)
	}
	if len(globalRules2) != 0 {
		t.Errorf("real global scope should still be empty, got %d rules (collision!)", len(globalRules2))
	}

	// 3. RememberAllow for the real global scope ("") must NOT appear in repo "global"
	c.RememberAllow(toolNameBash, "echo hello", "") // global scope
	globalRules3, err := s.Load(globalScope)
	if err != nil {
		t.Fatalf("Load global scope after global RememberAllow: %v", err)
	}
	if len(globalRules3) != 1 {
		t.Fatalf("expected 1 rule in real global scope, got %d", len(globalRules3))
	}
	repoRules3, err := s.Load(scopeFor("global"))
	if err != nil {
		t.Fatalf("Load repo 'global' after global RememberAllow: %v", err)
	}
	if len(repoRules3) != 2 {
		t.Errorf("repo 'global' should still have 2 rules, got %d (leak from real global!)", len(repoRules3))
	}

	// 4. LoadAndMerge for repo "global" must load repo rules + real global rules
	c2 := NewCache(s)
	c2.LoadAndMerge("global")
	cached := c2.Rules()
	// Should have: 2 from repo "global" + 1 from real global scope = 3
	if len(cached) != 3 {
		t.Fatalf("expected 3 rules after LoadAndMerge('global'), got %d", len(cached))
	}

	// 5. Check: repo "global" rule must NOT auto-apply to unrelated repos
	_, found := c2.Check(toolNameBash, "make build", "other-repo")
	if found {
		t.Error("repo 'global' rule should NOT match 'other-repo' — scope collision detected")
	}

	// But it should match repo "global" itself
	rule, found := c2.Check(toolNameBash, "make build", "global")
	if !found || rule.Effect != DecisionAllow {
		t.Errorf("repo 'global' rule should match repo 'global', found=%v rule=%+v", found, rule)
	}

	// Real global rule (echo hello) should match any repo
	rule, found = c2.Check(toolNameBash, "echo hello", "other-repo")
	if !found || rule.Effect != DecisionAllow {
		t.Errorf("real global rule should match any repo, found=%v rule=%+v", found, rule)
	}
}

// TestPreexistingGlobalJSON_HonoredAfterRestart verifies that a manually
// created or preexisting global.json file is still honored after restart.
// This is the backward-compatibility contract: the plan specifies
// ~/.agentic-workflow/permissions/global.json as the canonical global file.
func TestPreexistingGlobalJSON_HonoredAfterRestart(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	// 1. Simulate a preexisting global.json (created by hand or a prior version)
	globalJSON := `{"rules": [{"tool_pattern": "Bash(echo *)", "effect": "allow"}]}`
	if err := os.WriteFile(filepath.Join(dir, "global.json"), []byte(globalJSON), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// 2. Create a cache, call LoadAndMerge("") to load global rules
	c := NewCache(s)
	c.LoadAndMerge("")

	// 3. The preexisting global rule must be loaded into the cache
	cached := c.Rules()
	if len(cached) != 1 {
		t.Fatalf("expected 1 rule from preexisting global.json, got %d", len(cached))
	}
	if cached[0].ToolPattern != patternBashEcho {
		t.Errorf("rule pattern = %q, want %q", cached[0].ToolPattern, patternBashEcho)
	}
	if cached[0].RepoName != "" {
		t.Errorf("rule RepoName = %q, want empty (global)", cached[0].RepoName)
	}
	if cached[0].Effect != DecisionAllow {
		t.Errorf("rule Effect = %q, want allow", cached[0].Effect)
	}

	// 4. Simulate "restart": new cache, same store — global rules must survive
	c2 := NewCache(s)
	c2.LoadAndMerge("")
	cached2 := c2.Rules()
	if len(cached2) != 1 {
		t.Fatalf("after restart: expected 1 rule from global.json, got %d", len(cached2))
	}
	if cached2[0].ToolPattern != patternBashEcho {
		t.Errorf("after restart: rule pattern = %q, want %q", cached2[0].ToolPattern, patternBashEcho)
	}

	// 5. Adding a new global rule via RememberAllow must also go to global.json
	c2.RememberAllow(toolNameBash, testLsLa, "")
	globalRules, err := s.Load(globalScope)
	if err != nil {
		t.Fatalf("Load global scope: %v", err)
	}
	if len(globalRules) != 2 {
		t.Fatalf("expected 2 rules in global.json after RememberAllow, got %d", len(globalRules))
	}
}

func TestStore_EnsureGlobalDefaults_CreatesCuratedGlobalJSON(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	if err := s.EnsureGlobalDefaults(); err != nil {
		t.Fatalf("EnsureGlobalDefaults: %v", err)
	}

	globalPath := filepath.Join(dir, "global.json")
	if _, err := os.Stat(globalPath); err != nil {
		t.Fatalf("Stat(global.json): %v", err)
	}

	globalRules, err := s.Load(globalScope)
	if err != nil {
		t.Fatalf("Load(global): %v", err)
	}
	want := defaultGlobalRules()
	if len(globalRules) != len(want) {
		t.Fatalf("len(globalRules) = %d, want %d", len(globalRules), len(want))
	}
	for i := range want {
		if globalRules[i] != want[i] {
			t.Errorf("globalRules[%d] = %+v, want %+v", i, globalRules[i], want[i])
		}
	}

	cache := NewCache(s)
	cache.LoadAndMerge("")

	cachedRules := cache.Rules()
	if len(cachedRules) != len(want) {
		t.Fatalf("len(cachedRules) = %d, want %d", len(cachedRules), len(want))
	}

	rule, found := cache.Check(toolNameBash, testJSONLsLa, "any-repo")
	if !found {
		t.Fatal("cache.Check(ls -la) found = false, want true")
	}
	if rule.Effect != DecisionAllow {
		t.Errorf("rule.Effect = %q, want %q", rule.Effect, DecisionAllow)
	}
}

func TestStore_EnsureGlobalDefaults_MergesIntoExistingGlobalJSON(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	// Pre-populate with a single user-added rule.
	userRule := Rule{ToolPattern: patternBashEcho, Effect: DecisionAllow}
	if err := s.Save(globalScope, []Rule{userRule}); err != nil {
		t.Fatalf("Save(global): %v", err)
	}

	if err := s.EnsureGlobalDefaults(); err != nil {
		t.Fatalf("EnsureGlobalDefaults: %v", err)
	}

	rules, err := s.Load(globalScope)
	if err != nil {
		t.Fatalf("Load(global): %v", err)
	}

	defaults := defaultGlobalRules()
	// Should have the original user rule + all defaults.
	wantLen := 1 + len(defaults)
	if len(rules) != wantLen {
		t.Fatalf("len(rules) = %d, want %d", len(rules), wantLen)
	}
	// First rule should be the user-added one (preserved in order).
	if rules[0].ToolPattern != patternBashEcho {
		t.Errorf("rules[0].ToolPattern = %q, want %q", rules[0].ToolPattern, patternBashEcho)
	}

	cache := NewCache(s)
	cache.LoadAndMerge("")
	// Verify a default rule is now accessible via the cache.
	rule, found := cache.Check(toolNameBash, `{"command":"find /tmp -name foo"}`, "any-repo")
	if !found {
		t.Fatal("cache.Check(find) found = false, want true")
	}
	if rule.Effect != DecisionAllow {
		t.Errorf("rule.Effect = %q, want %q", rule.Effect, DecisionAllow)
	}
}

func TestStore_EnsureGlobalDefaults_IsIdempotent(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	if err := s.EnsureGlobalDefaults(); err != nil {
		t.Fatalf("EnsureGlobalDefaults(first): %v", err)
	}

	if err := s.EnsureGlobalDefaults(); err != nil {
		t.Fatalf("EnsureGlobalDefaults(second): %v", err)
	}

	rules, err := s.Load(globalScope)
	if err != nil {
		t.Fatalf("Load(global): %v", err)
	}
	want := defaultGlobalRules()
	if len(rules) != len(want) {
		t.Fatalf("len(rules) = %d, want %d", len(rules), len(want))
	}
	for i := range want {
		if rules[i] != want[i] {
			t.Errorf("rules[%d] = %+v, want %+v", i, rules[i], want[i])
		}
	}
}
