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

package opencode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// fullManagedConfig parses the entire generated managed config for assertions.
type fullManagedConfig struct {
	Schema       string                     `json:"$schema"`
	Model        string                     `json:"model"`
	Instructions []string                   `json:"instructions"`
	Permission   map[string]json.RawMessage `json:"permission"`
	Agent        map[string]struct {
		Description string                     `json:"description"`
		Mode        string                     `json:"mode"`
		Prompt      string                     `json:"prompt"`
		Model       string                     `json:"model"`
		Permission  map[string]json.RawMessage `json:"permission"`
	} `json:"agent"`
	Provider   map[string]json.RawMessage `json:"provider"`
	Share      string                     `json:"share"`
	Autoupdate *bool                      `json:"autoupdate"`
}

func envValue(env []string, name string) (string, bool) {
	for _, e := range env {
		if after, ok := strings.CutPrefix(e, name+"="); ok {
			return after, true
		}
	}
	return "", false
}

func hasEnv(env []string, kv string) bool { return slices.Contains(env, kv) }

// readManagedConfigFile reads and parses the managed config the launch env
// points OPENCODE_CONFIG at.
func readManagedConfigFile(t *testing.T, env []string) fullManagedConfig {
	t.Helper()
	path, ok := envValue(env, configFileEnvVar)
	if !ok {
		t.Fatalf("env %v missing %s", env, configFileEnvVar)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading managed config %q: %v", path, err)
	}
	var cfg fullManagedConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("managed config %q is not valid JSON: %v", path, err)
	}
	return cfg
}

// parseFull parses the inline OPENCODE_CONFIG_CONTENT into the full config.
func parseFullInline(t *testing.T, env []string) fullManagedConfig {
	t.Helper()
	var cfg fullManagedConfig
	if err := json.Unmarshal([]byte(configContentValue(t, env)), &cfg); err != nil {
		t.Fatalf("OPENCODE_CONFIG_CONTENT not valid JSON: %v", err)
	}
	return cfg
}

// --- Task 1: deterministic managed config generation & isolation env ---

func TestManagedConfig_WrittenUnderStateDirAndPointedAt(t *testing.T) {
	state := t.TempDir()
	p := New()
	_, env, err := p.BuildCommand(llm.CommandBuildOpts{
		Model:    "anthropic/claude-sonnet-4-5",
		StateDir: state,
	})
	if err != nil {
		t.Fatalf("BuildCommand() error: %v", err)
	}
	path, ok := envValue(env, configFileEnvVar)
	if !ok {
		t.Fatalf("env missing %s; got %v", configFileEnvVar, env)
	}
	// The managed config must live under the Agentico-owned state dir, never the
	// user's global OpenCode config directory.
	if !strings.HasPrefix(path, state) {
		t.Fatalf("managed config %q not under state dir %q", path, state)
	}
	if filepath.Base(path) != configFileName {
		t.Fatalf("managed config base = %q, want %q", filepath.Base(path), configFileName)
	}
	cfg := readManagedConfigFile(t, env)
	if cfg.Model != "anthropic/claude-sonnet-4-5" {
		t.Fatalf("managed config model = %q, want backend model", cfg.Model)
	}
	// Inline content mirrors the file (highest-precedence channel) so the model
	// and permission win over merged global config.
	if inline := parseFullInline(t, env); inline.Model != cfg.Model {
		t.Fatalf("inline model %q != file model %q", inline.Model, cfg.Model)
	}
}

func TestManagedConfig_DeterministicAndSafeOverwrite(t *testing.T) {
	state := t.TempDir()
	p := New()
	opts := llm.CommandBuildOpts{Model: "openai/gpt-5", StateDir: state, SystemPrompt: "role instructions body"}

	_, env1, err := p.BuildCommand(opts)
	if err != nil {
		t.Fatalf("first BuildCommand: %v", err)
	}
	path1, _ := envValue(env1, configFileEnvVar)
	data1, err := os.ReadFile(path1)
	if err != nil {
		t.Fatalf("read first config: %v", err)
	}
	info1, _ := os.Stat(path1)

	_, env2, err := p.BuildCommand(opts)
	if err != nil {
		t.Fatalf("second BuildCommand: %v", err)
	}
	path2, _ := envValue(env2, configFileEnvVar)
	if path1 != path2 {
		t.Fatalf("identical inputs produced different managed paths: %q vs %q", path1, path2)
	}
	data2, _ := os.ReadFile(path2)
	if string(data1) != string(data2) {
		t.Fatalf("identical inputs produced different config bytes")
	}
	// Re-running with identical content must not rewrite the file (idempotent).
	info2, _ := os.Stat(path2)
	if !info1.ModTime().Equal(info2.ModTime()) {
		t.Errorf("unchanged managed config was rewritten (modtime changed)")
	}
}

func TestManagedConfig_DistinctInputsDistinctPaths(t *testing.T) {
	state := t.TempDir()
	p := New()
	_, envA, err := p.BuildCommand(llm.CommandBuildOpts{Model: "openai/gpt-5", StateDir: state})
	if err != nil {
		t.Fatalf("BuildCommand A: %v", err)
	}
	_, envB, err := p.BuildCommand(llm.CommandBuildOpts{Model: "anthropic/claude-sonnet-4-5", StateDir: state})
	if err != nil {
		t.Fatalf("BuildCommand B: %v", err)
	}
	pathA, _ := envValue(envA, configFileEnvVar)
	pathB, _ := envValue(envB, configFileEnvVar)
	if pathA == pathB {
		t.Fatalf("different models shared a managed config path %q (collision risk)", pathA)
	}
}

func TestManagedConfig_NoGlobalDirsWritten(t *testing.T) {
	// Point HOME/XDG at empty temp dirs; generating the managed config must not
	// create or touch any global OpenCode config directory.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	state := t.TempDir()

	p := New()
	if _, _, err := p.BuildCommand(llm.CommandBuildOpts{
		Model:        "openai/gpt-5",
		StateDir:     state,
		SystemPrompt: "role",
		AgentsJSON:   `{"a":{"description":"d","prompt":"p"}}`,
	}); err != nil {
		t.Fatalf("BuildCommand: %v", err)
	}
	for _, dir := range []string{
		filepath.Join(home, ".config", "opencode"),
		filepath.Join(home, ".config", "opencode", "opencode.json"),
		filepath.Join(home, ".local", "share", "opencode"),
	} {
		if _, err := os.Stat(dir); err == nil {
			t.Errorf("managed config generation created global OpenCode path %q", dir)
		}
	}
}

func TestManagedConfig_IsolationEnvScrubsInheritedSurfaces(t *testing.T) {
	state := t.TempDir()
	p := New()
	_, env, err := p.BuildCommand(llm.CommandBuildOpts{Model: "openai/gpt-5", StateDir: state})
	if err != nil {
		t.Fatalf("BuildCommand: %v", err)
	}
	// Every inherited-surface isolation flag must be present so user-global
	// plugins, project config, Claude-compat rules, and external skills cannot
	// bypass the managed contract.
	want := []string{
		"OPENCODE_PURE=1",
		"OPENCODE_DISABLE_DEFAULT_PLUGINS=1",
		"OPENCODE_DISABLE_PROJECT_CONFIG=1",
		"OPENCODE_DISABLE_CLAUDE_CODE=1",
		"OPENCODE_DISABLE_CLAUDE_CODE_PROMPT=1",
		"OPENCODE_DISABLE_CLAUDE_CODE_SKILLS=1",
		"OPENCODE_DISABLE_EXTERNAL_SKILLS=1",
		"OPENCODE_DISABLE_AUTOUPDATE=1",
		"OPENCODE_DISABLE_SHARE=1",
	}
	for _, kv := range want {
		if !hasEnv(env, kv) {
			t.Errorf("isolation env missing %q; got %v", kv, env)
		}
	}
	// Noninteractive runtime settings are also pinned in config.
	cfg := readManagedConfigFile(t, env)
	if cfg.Share != "disabled" {
		t.Errorf("config share = %q, want disabled", cfg.Share)
	}
	if cfg.Autoupdate == nil || *cfg.Autoupdate {
		t.Errorf("config autoupdate = %v, want false", cfg.Autoupdate)
	}
}

// --- Task 2: role instructions delivered as managed instructions ---

func TestManagedConfig_RoleInstructionsDeliveredAsFile(t *testing.T) {
	state := t.TempDir()
	role := "# Agentico Role\nWrite only inside output roots.\nCompletion marker contract.\n"
	p := New()
	_, env, err := p.BuildCommand(llm.CommandBuildOpts{
		Model:        "anthropic/claude-sonnet-4-5",
		StateDir:     state,
		SystemPrompt: role,
	})
	if err != nil {
		t.Fatalf("BuildCommand: %v", err)
	}
	cfg := readManagedConfigFile(t, env)
	if len(cfg.Instructions) != 1 {
		t.Fatalf("config instructions = %v, want exactly one managed file", cfg.Instructions)
	}
	instrPath := cfg.Instructions[0]
	if !filepath.IsAbs(instrPath) {
		t.Errorf("instructions path %q is not absolute", instrPath)
	}
	body, err := os.ReadFile(instrPath)
	if err != nil {
		t.Fatalf("reading managed instructions %q: %v", instrPath, err)
	}
	// The role prompt must reach OpenCode verbatim, not be lost or truncated.
	if string(body) != role {
		t.Fatalf("managed instructions body = %q, want exact role prompt %q", body, role)
	}
}

func TestManagedConfig_NoSystemPromptOmitsInstructions(t *testing.T) {
	state := t.TempDir()
	p := New()
	_, env, err := p.BuildCommand(llm.CommandBuildOpts{Model: "openai/gpt-5", StateDir: state})
	if err != nil {
		t.Fatalf("BuildCommand: %v", err)
	}
	if cfg := readManagedConfigFile(t, env); len(cfg.Instructions) != 0 {
		t.Fatalf("config instructions = %v, want none when no system prompt", cfg.Instructions)
	}
}

func TestManagedConfig_RoleInstructionsRequireStateDir(t *testing.T) {
	// A role prompt with no provider-managed state dir cannot be delivered; the
	// provider must fail closed rather than silently drop the role instructions.
	p := New()
	cmd, env, err := p.BuildCommand(llm.CommandBuildOpts{
		Model:        "openai/gpt-5",
		SystemPrompt: "role that cannot be delivered",
	})
	if err == nil {
		t.Fatalf("BuildCommand() = (%v,%v,nil), want fail-closed error", cmd, env)
	}
	if cmd != nil || env != nil {
		t.Fatalf("failed BuildCommand returned cmd=%v env=%v, want nil/nil", cmd, env)
	}
}

// --- Task 3: read roots, writable roots, permissions ---

func TestManagedConfig_WritableRootsBoundEdits(t *testing.T) {
	state := t.TempDir()
	worktree := t.TempDir()
	p := New()
	_, env, err := p.BuildCommand(llm.CommandBuildOpts{
		Model:         "openai/gpt-5",
		StateDir:      state,
		WritableRoots: []string{state, worktree},
	})
	if err != nil {
		t.Fatalf("BuildCommand: %v", err)
	}
	perm := readManagedConfigFile(t, env).Permission
	for _, key := range []string{"edit", "write", "apply_patch"} {
		raw, ok := perm[key]
		if !ok {
			t.Fatalf("permission missing %q", key)
		}
		var patterns map[string]string
		if err := json.Unmarshal(raw, &patterns); err != nil {
			t.Fatalf("permission[%q] = %s, want path-pattern object: %v", key, raw, err)
		}
		if patterns["*"] != "deny" {
			t.Errorf("permission[%q] default = %q, want deny outside writable roots", key, patterns["*"])
		}
		for _, root := range []string{state, worktree} {
			glob := root + "/**"
			if patterns[glob] != "ask" {
				t.Errorf("permission[%q][%q] = %q, want ask inside writable root", key, glob, patterns[glob])
			}
		}
	}
}

func TestManagedConfig_DangerousSkipBoundsWritesToRoots(t *testing.T) {
	state := t.TempDir()
	p := New()
	_, env, err := p.BuildCommand(llm.CommandBuildOpts{
		Model:                "openai/gpt-5",
		StateDir:             state,
		WritableRoots:        []string{state},
		DangerouslySkipPerms: true,
	})
	if err != nil {
		t.Fatalf("BuildCommand: %v", err)
	}
	perm := readManagedConfigFile(t, env).Permission
	var patterns map[string]string
	if err := json.Unmarshal(perm["edit"], &patterns); err != nil {
		t.Fatalf("edit permission not an object: %v", err)
	}
	// Even in dangerous-skip mode, edits stay bounded to writable roots.
	if patterns[state+"/**"] != "allow" {
		t.Errorf("dangerous-skip edit inside root = %q, want allow", patterns[state+"/**"])
	}
	if patterns["*"] != "deny" {
		t.Errorf("dangerous-skip edit outside roots = %q, want deny", patterns["*"])
	}
}

// TestManagedConfig_ReadsBoundedToMountedRootsInNormalMode proves normal-mode
// reads are allowed inside every mounted read root (state, skills, guidelines,
// other repos) but ask for anything outside them, so an external-directory read
// pauses through Agentico instead of being silently allowed (Task 3).
func TestManagedConfig_ReadsBoundedToMountedRootsInNormalMode(t *testing.T) {
	state := t.TempDir()
	readRoots := []string{state, "/skills", "/guidelines", "/repo-b"}
	p := New()
	_, env, err := p.BuildCommand(llm.CommandBuildOpts{
		Model:         "openai/gpt-5",
		StateDir:      state,
		ReadRoots:     readRoots,
		WritableRoots: []string{state},
	})
	if err != nil {
		t.Fatalf("BuildCommand: %v", err)
	}
	raw, ok := readManagedConfigFile(t, env).Permission["read"]
	if !ok {
		t.Fatalf("permission missing read")
	}
	var patterns map[string]string
	if err := json.Unmarshal(raw, &patterns); err != nil {
		t.Fatalf("permission[read] = %s, want path-pattern object: %v", raw, err)
	}
	if patterns["*"] != "ask" {
		t.Errorf("permission[read] default = %q, want ask outside mounted read roots", patterns["*"])
	}
	for _, root := range readRoots {
		glob := root + "/**"
		if patterns[glob] != "allow" {
			t.Errorf("permission[read][%q] = %q, want allow inside mounted read root", glob, patterns[glob])
		}
	}
}

// TestManagedConfig_DangerousSkipAllowsReads proves reads stay noninteractive in
// dangerous-skip mode (the same noninteractive surface other providers allow),
// rather than carrying a path-pattern gate (Task 3).
func TestManagedConfig_DangerousSkipAllowsReads(t *testing.T) {
	state := t.TempDir()
	p := New()
	_, env, err := p.BuildCommand(llm.CommandBuildOpts{
		Model:                "openai/gpt-5",
		StateDir:             state,
		ReadRoots:            []string{state, "/repo-b"},
		WritableRoots:        []string{state},
		DangerouslySkipPerms: true,
	})
	if err != nil {
		t.Fatalf("BuildCommand: %v", err)
	}
	if got := permString(t, readManagedConfigFile(t, env).Permission, "read"); got != "allow" {
		t.Errorf("dangerous-skip permission[read] = %q, want allow (reads are noninteractive)", got)
	}
}

// TestManagedConfig_ExternalDirectoryAllowsMountedRoots proves the
// external_directory surface — OpenCode's separate gate for any path outside the
// session cwd — is configured to allow every root Agentico mounts (state,
// skills, guidelines, other repos) and ask for anything else. Without this,
// OpenCode's built-in external_directory default asks for every mounted root,
// and a subagent (whose ask is not forwarded over ACP) stalls reading a mounted
// skill directory.
func TestManagedConfig_ExternalDirectoryAllowsMountedRoots(t *testing.T) {
	state := t.TempDir()
	readRoots := []string{state, "/skills", "/guidelines", "/repo-b"}
	p := New()
	_, env, err := p.BuildCommand(llm.CommandBuildOpts{
		Model:         "openai/gpt-5",
		StateDir:      state,
		ReadRoots:     readRoots,
		WritableRoots: []string{state},
	})
	if err != nil {
		t.Fatalf("BuildCommand: %v", err)
	}
	raw, ok := readManagedConfigFile(t, env).Permission["external_directory"]
	if !ok {
		t.Fatalf("permission missing external_directory")
	}
	var patterns map[string]string
	if err := json.Unmarshal(raw, &patterns); err != nil {
		t.Fatalf("permission[external_directory] = %s, want path-pattern object: %v", raw, err)
	}
	if patterns["*"] != "ask" {
		t.Errorf("permission[external_directory] default = %q, want ask outside mounted roots", patterns["*"])
	}
	for _, root := range readRoots {
		glob := root + "/**"
		if patterns[glob] != "allow" {
			t.Errorf("permission[external_directory][%q] = %q, want allow inside mounted root", glob, patterns[glob])
		}
	}
}

// TestManagedConfig_DangerousSkipAllowsExternalDirectory proves external_directory
// access is noninteractive in dangerous-skip mode, mirroring reads.
func TestManagedConfig_DangerousSkipAllowsExternalDirectory(t *testing.T) {
	state := t.TempDir()
	p := New()
	_, env, err := p.BuildCommand(llm.CommandBuildOpts{
		Model:                "openai/gpt-5",
		StateDir:             state,
		ReadRoots:            []string{state, "/repo-b"},
		WritableRoots:        []string{state},
		DangerouslySkipPerms: true,
	})
	if err != nil {
		t.Fatalf("BuildCommand: %v", err)
	}
	if got := permString(t, readManagedConfigFile(t, env).Permission, "external_directory"); got != "allow" {
		t.Errorf("dangerous-skip permission[external_directory] = %q, want allow", got)
	}
}

// TestManagedConfig_SubagentsRunNonInteractive proves every converted subagent
// carries a permission override that resolves deterministically. OpenCode's ACP
// bridge silently drops permission requests from internally-spawned child
// sessions, so a subagent that reaches an "ask" decision deadlocks. Surfaces
// Agentico auto-approves resolve to allow; human-gated surfaces (bash, skill)
// deny in normal mode (fail fast, not hang); questions deny (no human answer).
func TestManagedConfig_SubagentsRunNonInteractive(t *testing.T) {
	state := t.TempDir()
	agentsJSON := `{"api-surface-researcher":{"description":"Researches API surface","prompt":"You research."}}`
	p := New()
	_, env, err := p.BuildCommand(llm.CommandBuildOpts{
		Model:         "openai/gpt-5",
		StateDir:      state,
		AgentsJSON:    agentsJSON,
		WritableRoots: []string{state},
	})
	if err != nil {
		t.Fatalf("BuildCommand: %v", err)
	}
	ag, ok := readManagedConfigFile(t, env).Agent["api-surface-researcher"]
	if !ok {
		t.Fatalf("converted agents missing api-surface-researcher")
	}
	if ag.Permission == nil {
		t.Fatalf("subagent carries no permission override; an un-forwardable ask would deadlock it")
	}
	for _, key := range []string{"read", "external_directory", "webfetch", "websearch"} {
		if got := permString(t, ag.Permission, key); got != "allow" {
			t.Errorf("subagent permission[%q] = %q, want allow", key, got)
		}
	}
	// bash/skill deny in normal mode (fail fast, not hang). task denies
	// unconditionally: subagents must not spawn subagents (depth-1 delegation
	// prevents multiplicative fan-out).
	for _, key := range []string{"bash", "skill", "task"} {
		if got := permString(t, ag.Permission, key); got != "deny" {
			t.Errorf("subagent permission[%q] = %q, want deny", key, got)
		}
	}
	if got := permString(t, ag.Permission, "question"); got != "deny" {
		t.Errorf("subagent permission[question] = %q, want deny", got)
	}
}

// TestManagedConfig_SubagentBashAllowedUnderDangerousSkip proves the human-gated
// surfaces become allow for subagents under dangerous-skip mode, where the whole
// run is already noninteractive.
func TestManagedConfig_SubagentBashAllowedUnderDangerousSkip(t *testing.T) {
	state := t.TempDir()
	agentsJSON := `{"api-surface-researcher":{"description":"d","prompt":"p"}}`
	p := New()
	_, env, err := p.BuildCommand(llm.CommandBuildOpts{
		Model:                "openai/gpt-5",
		StateDir:             state,
		AgentsJSON:           agentsJSON,
		WritableRoots:        []string{state},
		DangerouslySkipPerms: true,
	})
	if err != nil {
		t.Fatalf("BuildCommand: %v", err)
	}
	ag := readManagedConfigFile(t, env).Agent["api-surface-researcher"]
	for _, key := range []string{"bash", "skill"} {
		if got := permString(t, ag.Permission, key); got != "allow" {
			t.Errorf("dangerous-skip subagent permission[%q] = %q, want allow", key, got)
		}
	}
}

// --- Task 4: agents converted to OpenCode managed agents ---

func TestManagedConfig_AgentsConverted(t *testing.T) {
	state := t.TempDir()
	agentsJSON := `{"reviewer":{"description":"Reviews code","prompt":"You review.","model":"openai/gpt-5"},` +
		`"explorer":{"description":"Explores","prompt":"You explore.","model":"sonnet"}}`
	p := New()
	_, env, err := p.BuildCommand(llm.CommandBuildOpts{
		Model:      "anthropic/claude-sonnet-4-5",
		StateDir:   state,
		AgentsJSON: agentsJSON,
	})
	if err != nil {
		t.Fatalf("BuildCommand: %v", err)
	}
	cfg := readManagedConfigFile(t, env)
	rev, ok := cfg.Agent["reviewer"]
	if !ok {
		t.Fatalf("converted agents missing reviewer; got %v", cfg.Agent)
	}
	if rev.Description != "Reviews code" || rev.Prompt != "You review." {
		t.Errorf("reviewer description/prompt not preserved: %+v", rev)
	}
	if rev.Mode != "subagent" {
		t.Errorf("reviewer mode = %q, want subagent", rev.Mode)
	}
	// A valid backend model passes through; an Agentico-internal alias is omitted.
	if rev.Model != "openai/gpt-5" {
		t.Errorf("reviewer model = %q, want openai/gpt-5 (valid backend)", rev.Model)
	}
	if exp := cfg.Agent["explorer"]; exp.Model != "" {
		t.Errorf("explorer model = %q, want omitted (alias is not a valid backend)", exp.Model)
	}
}

// TestManagedConfig_BuiltinOverridesWhenEmpty proves that with no embedded
// agents, the config still pins OpenCode's built-in spawnable subagents to the
// deterministic deny profile — they cannot inherit the top-level "ask" and hang,
// and they cannot spawn their own subagents (depth-1 delegation). No other
// Agentico-defined agents are added.
func TestManagedConfig_BuiltinOverridesWhenEmpty(t *testing.T) {
	state := t.TempDir()
	p := New()
	_, env, err := p.BuildCommand(llm.CommandBuildOpts{Model: "openai/gpt-5", StateDir: state})
	if err != nil {
		t.Fatalf("BuildCommand: %v", err)
	}
	cfg := readManagedConfigFile(t, env)
	if len(cfg.Agent) != len(openCodeBuiltinSubagents) {
		t.Fatalf("agents = %v, want only the %d built-in subagent overrides", cfg.Agent, len(openCodeBuiltinSubagents))
	}
	for _, name := range openCodeBuiltinSubagents {
		ag, ok := cfg.Agent[name]
		if !ok {
			t.Fatalf("built-in subagent %q missing from config; got %v", name, cfg.Agent)
		}
		if string(ag.Permission["bash"]) != `"deny"` {
			t.Errorf("built-in %q bash = %s, want \"deny\"", name, ag.Permission["bash"])
		}
		// Subagents must not spawn subagents — depth-1 delegation prevents the
		// multiplicative fan-out that exhausts memory.
		if string(ag.Permission["task"]) != `"deny"` {
			t.Errorf("built-in subagent %q task = %s, want \"deny\" (no sub-subagents)", name, ag.Permission["task"])
		}
	}
}

// --- Task 6: conservative effort mapping ---

func TestEffortMapping_StableForSupportedBackend(t *testing.T) {
	state := t.TempDir()
	p := New()
	_, env, err := p.BuildCommand(llm.CommandBuildOpts{
		Model:       "openai/gpt-5",
		StateDir:    state,
		EffortLevel: llm.EffortHigh,
	})
	if err != nil {
		t.Fatalf("BuildCommand: %v", err)
	}
	cfg := readManagedConfigFile(t, env)
	raw, ok := cfg.Provider["openai"]
	if !ok {
		t.Fatalf("expected provider.openai effort mapping; got %v", cfg.Provider)
	}
	if !strings.Contains(string(raw), `"reasoningEffort"`) || !strings.Contains(string(raw), `"high"`) {
		t.Fatalf("provider.openai = %s, want reasoningEffort=high", raw)
	}
}

func TestEffortMapping_OmittedForUnsupportedBackend(t *testing.T) {
	state := t.TempDir()
	p := New()
	for _, model := range []string{"anthropic/claude-sonnet-4-5", "ollama/llama3.1:8b"} {
		_, env, err := p.BuildCommand(llm.CommandBuildOpts{
			Model:       model,
			StateDir:    state,
			EffortLevel: llm.EffortMax,
		})
		if err != nil {
			t.Fatalf("BuildCommand(%q): %v", model, err)
		}
		cfg := readManagedConfigFile(t, env)
		if len(cfg.Provider) != 0 {
			t.Errorf("model %q left an effort/provider key behind: %v", model, cfg.Provider)
		}
	}
}

func TestEffortMapping_PureFunctionParity(t *testing.T) {
	cases := []struct {
		backend string
		level   llm.EffortLevel
		want    string
		ok      bool
	}{
		{"openai/gpt-5", llm.EffortLow, "low", true},
		{"openai/gpt-5", llm.EffortMedium, "medium", true},
		{"openai/gpt-5", llm.EffortHigh, "high", true},
		{"openai/gpt-5", llm.EffortXHigh, "high", true},
		{"openai/gpt-5", llm.EffortMax, "high", true},
		{"openai/gpt-5", "", "", false},
		{"anthropic/claude-sonnet-4-5", llm.EffortHigh, "", false},
		{"ollama/llama3.1:8b", llm.EffortHigh, "", false},
		{"bareword", llm.EffortHigh, "", false},
	}
	for _, tc := range cases {
		got, ok := reasoningEffortFor(tc.backend, tc.level)
		if got != tc.want || ok != tc.ok {
			t.Errorf("reasoningEffortFor(%q,%q) = (%q,%v), want (%q,%v)", tc.backend, tc.level, got, ok, tc.want, tc.ok)
		}
	}
}

func TestBuildCommand_InvalidModelRejectedBeforeManagedConfig(t *testing.T) {
	state := t.TempDir()
	p := New()
	cmd, env, err := p.BuildCommand(llm.CommandBuildOpts{Model: "anthropic/$(whoami)", StateDir: state, EffortLevel: llm.EffortHigh})
	if err == nil {
		t.Fatalf("BuildCommand(invalid) = (%v,%v,nil), want rejection", cmd, env)
	}
	// No managed config file may be written for a rejected model.
	entries, _ := os.ReadDir(filepath.Join(state, managedRootDirName))
	if len(entries) != 0 {
		t.Errorf("rejected model wrote managed artifacts: %v", entries)
	}
}

// --- Task 5: version-gated isolation + fallback diagnostics ---

func TestIsolationEnv_AllAvailableAtMinVersion(t *testing.T) {
	env, unavailable := buildIsolationEnv(minSupportedVersion())
	if len(unavailable) != 0 {
		t.Fatalf("isolation features unavailable at MinVersion: %v", unavailable)
	}
	if len(env) != len(isolationFeatures) {
		t.Fatalf("emitted %d isolation flags, want %d", len(env), len(isolationFeatures))
	}
}

func TestIsolationEnv_VersionGatesFutureFeatureAndReportsFallback(t *testing.T) {
	features := []isolationFeature{
		{"OPENCODE_PURE=1", "external-plugin isolation", [3]int{1, 0, 0}},
		{"OPENCODE_FUTURE_FLAG=1", "future isolation", [3]int{9, 0, 0}},
	}
	env, unavailable := isolationEnvFrom(features, [3]int{1, 17, 9})
	if !slices.Contains(env, "OPENCODE_PURE=1") {
		t.Errorf("supported flag not emitted: %v", env)
	}
	if slices.Contains(env, "OPENCODE_FUTURE_FLAG=1") {
		t.Errorf("unsupported future flag was emitted: %v", env)
	}
	if !slices.Contains(unavailable, "future isolation") {
		t.Fatalf("unavailable did not report the future feature: %v", unavailable)
	}
	diag := isolationFallbackDiagnostic(unavailable)
	if !strings.Contains(diag, "permission mediation") || !strings.Contains(diag, "future isolation") {
		t.Errorf("fallback diagnostic = %q, want it to name the feature and the mediation fallback", diag)
	}
}

// --- Task 7: managed-config diagnostic redaction ---

func TestManagedConfig_BuildErrorOmitsConfigAndSecrets(t *testing.T) {
	// A malformed agents JSON carrying a secret must abort the build with an error
	// that names the operation but echoes neither the raw input nor the secret.
	withEnv(t, []string{"PATH=/usr/bin"})
	state := t.TempDir()
	p := New()
	secret := "sk-ant-MANAGEDLEAK1234567890"
	cmd, env, err := p.BuildCommand(llm.CommandBuildOpts{
		Model:      "openai/gpt-5",
		StateDir:   state,
		AgentsJSON: `{"bad": not-json ` + secret + `}`,
	})
	if err == nil {
		t.Fatalf("BuildCommand(malformed agents) = (%v,%v,nil), want error", cmd, env)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("build error leaked secret: %q", err.Error())
	}
	if strings.Contains(err.Error(), "not-json") {
		t.Fatalf("build error echoed raw agent JSON input: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "agent") {
		t.Errorf("build error lost actionable framing: %q", err.Error())
	}
}

func TestManagedConfig_InlineAndFileNeverInDebugAsRawSecrets(t *testing.T) {
	// The managed config carries no provider secret of its own: the inline
	// content and file are pure config (model/permission/instructions paths), and
	// effort options carry only a documented reasoning level, never credentials.
	state := t.TempDir()
	p := New()
	_, env, err := p.BuildCommand(llm.CommandBuildOpts{
		Model:        "openai/gpt-5",
		StateDir:     state,
		SystemPrompt: "role",
		EffortLevel:  llm.EffortHigh,
	})
	if err != nil {
		t.Fatalf("BuildCommand: %v", err)
	}
	inline := configContentValue(t, env)
	for _, bad := range []string{"apiKey", "Bearer ", "sk-ant-", "password"} {
		if strings.Contains(inline, bad) {
			t.Errorf("inline managed config unexpectedly contains %q: %s", bad, inline)
		}
	}
}
