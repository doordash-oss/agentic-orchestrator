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

package agent

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/claude"
	opencode "github.com/doordash-oss/agentic-orchestrator/internal/llm/opencode"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
)

// newRegistryWithOpenCode builds a registry containing the real OpenCode
// provider alongside a normal catalog provider, so explicit routing and
// build-session wiring exercise the production path.
func newRegistryWithOpenCode() *llm.Registry {
	reg := llm.NewRegistry()
	claudeProvider := &claude.Provider{}
	claudeProvider.SetModelCatalog([]llm.ModelInfo{
		{ID: "sonnet[200K]", Category: "balanced", Aliases: []string{"sonnet"}},
	})
	reg.Register(claudeProvider)
	reg.Register(opencode.New())
	return reg
}

// TestOpenCodeBuildSessionPromptAndMarkerContract proves OpenCode receives the
// same rendered phase prompt and marker-path contract as existing providers,
// and that the managed launch selects the backend model.
func TestOpenCodeBuildSessionReceivesSamePromptAndMarkerContract(t *testing.T) {
	dir := t.TempDir()
	eventCh := make(chan interface{}, 8)
	sm := session.NewManager(eventCh)
	store := feature.NewStore(dir)
	pr := NewPhaseRunner(sm, store, dir)
	pr.Registry = newRegistryWithOpenCode()

	markerPath := filepath.Join(dir, "phase_complete")
	const renderedPrompt = "RENDERED PHASE PROMPT with artifact preflight + completion marker"

	cmd, env, sessOpts, err := pr.BuildSession(BuildSessionOpts{
		Model:        "opencode:anthropic/claude-sonnet-4-5",
		Prompt:       renderedPrompt,
		SystemPrompt: "system prompt",
		MarkerPath:   markerPath,
		PIDDir:       filepath.Join(dir, "pid"),
		PermHandler:  permHandlerFor(false, nil, ""),
		WorkDir:      dir,
	})
	if err != nil {
		t.Fatalf("BuildSession() error: %v", err)
	}

	if !slices.Equal(cmd, []string{"opencode", "acp"}) {
		t.Fatalf("BuildSession() cmd = %v, want [opencode acp]", cmd)
	}

	// Env carries the backend-model selection plus the standard AGENTICO_BIN.
	var hasConfig, hasBin bool
	for _, e := range env {
		if strings.HasPrefix(e, "OPENCODE_CONFIG_CONTENT=") {
			hasConfig = true
			if !strings.Contains(e, "anthropic/claude-sonnet-4-5") {
				t.Fatalf("OPENCODE_CONFIG_CONTENT missing backend model: %q", e)
			}
		}
		if strings.HasPrefix(e, "AGENTICO_BIN=") {
			hasBin = true
		}
	}
	if !hasConfig {
		t.Fatalf("BuildSession() env = %v, want OPENCODE_CONFIG_CONTENT", env)
	}
	if !hasBin {
		t.Fatalf("BuildSession() env = %v, want AGENTICO_BIN", env)
	}

	if sessOpts == nil || sessOpts.ProviderName != "opencode" {
		t.Fatalf("BuildSession() sessOpts = %#v, want ProviderName opencode", sessOpts)
	}
	if sessOpts.Watchdog == nil {
		t.Fatal("BuildSession() Watchdog = nil, want OpenCode watchdog config")
	}
	if sessOpts.Watchdog.PendingToolIdleTimeout <= 0 {
		t.Fatalf("BuildSession() Watchdog.PendingToolIdleTimeout = %s, want enabled", sessOpts.Watchdog.PendingToolIdleTimeout)
	}
	proto, ok := sessOpts.Protocol.(*opencode.Protocol)
	if !ok {
		t.Fatalf("BuildSession() Protocol type = %T, want *opencode.Protocol", sessOpts.Protocol)
	}
	if got := proto.InitialPromptForTest(); got != renderedPrompt {
		t.Fatalf("OpenCode protocol initial prompt = %q, want the rendered phase prompt", got)
	}
	if got := proto.MarkerPathForTest(); got != markerPath {
		t.Fatalf("OpenCode protocol marker path = %q, want %q", got, markerPath)
	}
	if got := proto.WorkDirForTest(); got != dir {
		t.Fatalf("OpenCode protocol work dir = %q, want %q", got, dir)
	}
	if got := proto.BackendModelForTest(); got != "anthropic/claude-sonnet-4-5" {
		t.Fatalf("OpenCode protocol backend model = %q, want anthropic/claude-sonnet-4-5", got)
	}
}

func TestBuildSessionOnlyEnablesWatchdogForOpenCode(t *testing.T) {
	dir := t.TempDir()
	eventCh := make(chan interface{}, 8)
	sm := session.NewManager(eventCh)
	store := feature.NewStore(dir)
	pr := NewPhaseRunner(sm, store, dir)
	pr.Registry = newRegistryWithOpenCode()

	_, _, openCodeOpts, err := pr.BuildSession(BuildSessionOpts{
		Model:        "opencode:anthropic/claude-sonnet-4-5",
		Prompt:       "prompt",
		SystemPrompt: "system",
		WorkDir:      dir,
	})
	if err != nil {
		t.Fatalf("BuildSession(OpenCode) error: %v", err)
	}
	if openCodeOpts == nil || openCodeOpts.Watchdog == nil {
		t.Fatalf("BuildSession(OpenCode) Watchdog = %#v, want config", openCodeOpts)
	}

	_, _, claudeOpts, err := pr.BuildSession(BuildSessionOpts{
		Model:        "sonnet",
		Prompt:       "prompt",
		SystemPrompt: "system",
		WorkDir:      dir,
	})
	if err != nil {
		t.Fatalf("BuildSession(Claude) error: %v", err)
	}
	if claudeOpts == nil {
		t.Fatal("BuildSession(Claude) sessOpts = nil")
	}
	if claudeOpts.Watchdog != nil {
		t.Fatalf("BuildSession(Claude) Watchdog = %#v, want nil", claudeOpts.Watchdog)
	}
}

func TestOpenCodeBuildSessionCarriesSuffixedContextWindowToSession(t *testing.T) {
	dir := t.TempDir()
	eventCh := make(chan interface{}, 8)
	sm := session.NewManager(eventCh)
	store := feature.NewStore(dir)
	pr := NewPhaseRunner(sm, store, dir)
	pr.Registry = newRegistryWithOpenCode()

	_, _, sessOpts, err := pr.BuildSession(BuildSessionOpts{
		Model:       "opencode:portkey/@fireworks/accounts/fireworks/models/glm-5p2[1.04M]",
		Prompt:      "rendered prompt",
		PIDDir:      filepath.Join(dir, "pid"),
		PermHandler: permHandlerFor(false, nil, ""),
		WorkDir:     dir,
	})
	if err != nil {
		t.Fatalf("BuildSession() error: %v", err)
	}
	if sessOpts == nil {
		t.Fatal("BuildSession() session opts = nil")
	}
	if got := sessOpts.ContextWindow; got != 1_040_000 {
		t.Fatalf("SessionOpts.ContextWindow = %d, want 1040000", got)
	}
}

// TestOpenCodeBuildSessionRejectsInvalidModelBeforeLaunch proves the real
// launch path (PhaseRunner.BuildSession -> provider.BuildCommand) fails closed
// for empty, flag-shaped, and shell/interpolation-shaped OpenCode selections, so
// an invalid backend never reaches a constructed `opencode acp` command.
func TestOpenCodeBuildSessionRejectsInvalidModelBeforeLaunch(t *testing.T) {
	dir := t.TempDir()
	eventCh := make(chan interface{}, 8)
	sm := session.NewManager(eventCh)
	store := feature.NewStore(dir)
	pr := NewPhaseRunner(sm, store, dir)
	pr.Registry = newRegistryWithOpenCode()

	invalid := []string{
		"opencode:",                        // routing prefix with no backend model
		"opencode:--dangerously-skip",      // flag-shaped backend
		"opencode:anthropic/$(whoami)",     // command substitution
		"opencode:anthropic/claude;rm -rf", // shell metacharacters
	}
	for _, model := range invalid {
		t.Run(model, func(t *testing.T) {
			cmd, env, sessOpts, err := pr.BuildSession(BuildSessionOpts{
				Model:      model,
				Prompt:     "RENDERED PHASE PROMPT",
				MarkerPath: filepath.Join(dir, "phase_complete"),
				WorkDir:    dir,
			})
			if err == nil {
				t.Fatalf("BuildSession(%q) succeeded, want rejection before command construction (cmd=%v)", model, cmd)
			}
			if cmd != nil || env != nil || sessOpts != nil {
				t.Fatalf("BuildSession(%q) returned non-nil outputs on rejection: cmd=%v env=%v sessOpts=%v", model, cmd, env, sessOpts)
			}
		})
	}
}

// openCodePermission extracts the permission map from the OPENCODE_CONFIG_CONTENT
// env var of a built OpenCode session.
func openCodePermission(t *testing.T, env []string) map[string]json.RawMessage {
	t.Helper()
	content, ok := "", false
	for _, e := range env {
		if after, found := strings.CutPrefix(e, "OPENCODE_CONFIG_CONTENT="); found {
			content, ok = after, true
		}
	}
	if !ok {
		t.Fatalf("env %v missing OPENCODE_CONFIG_CONTENT", env)
	}
	var cfg struct {
		Permission map[string]json.RawMessage `json:"permission"`
	}
	if err := json.Unmarshal([]byte(content), &cfg); err != nil {
		t.Fatalf("OPENCODE_CONFIG_CONTENT not valid JSON: %v", err)
	}
	return cfg.Permission
}

// permPatternMap decodes a path-pattern permission object for key.
func permPatternMap(t *testing.T, perm map[string]json.RawMessage, key string) map[string]string {
	t.Helper()
	raw, ok := perm[key]
	if !ok {
		t.Fatalf("permission missing key %q", key)
	}
	var patterns map[string]string
	if err := json.Unmarshal(raw, &patterns); err != nil {
		t.Fatalf("permission[%q] = %s, want path-pattern object: %v", key, raw, err)
	}
	return patterns
}

// permStringVal decodes a plain string permission decision for key.
func permStringVal(t *testing.T, perm map[string]json.RawMessage, key string) string {
	t.Helper()
	raw, ok := perm[key]
	if !ok {
		t.Fatalf("permission missing key %q", key)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("permission[%q] = %s, want a string decision: %v", key, raw, err)
	}
	return s
}

// TestOpenCodeBuildSessionSkillsGuidelinesReadableNotWritable proves the
// production BuildSession path — which appends the reconciled skills and
// guidelines dirs as read mounts — lets OpenCode read those mounts but never
// edit/write/patch them, in both normal and dangerous-skip mode. This is the
// regression guard for the default writable-roots computation conflating
// read-only context mounts with writable roots (Task 4).
func TestOpenCodeBuildSessionSkillsGuidelinesReadableNotWritable(t *testing.T) {
	for _, dsp := range []bool{false, true} {
		name := "normal"
		if dsp {
			name = "dangerous-skip"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			skillsDir := filepath.Join(dir, "skills")
			guidelinesDir := filepath.Join(dir, "guidelines")
			eventCh := make(chan interface{}, 8)
			sm := session.NewManager(eventCh)
			store := feature.NewStore(dir)
			pr := NewPhaseRunner(sm, store, dir)
			pr.Registry = newRegistryWithOpenCode()
			pr.SkillsDir = skillsDir
			pr.GuidelinesDir = guidelinesDir
			pr.DangerouslySkipPermissions = dsp

			_, env, _, err := pr.BuildSession(BuildSessionOpts{
				Model:                          "opencode:anthropic/claude-sonnet-4-5",
				Prompt:                         "implement this",
				SystemPrompt:                   "you are an implementer",
				SystemPromptHasUsefulResources: true,
				MarkerPath:                     filepath.Join(dir, "phase_complete"),
				WorkDir:                        dir,
				Phase:                          feature.PhaseImplement,
			})
			if err != nil {
				t.Fatalf("BuildSession() error: %v", err)
			}
			perm := openCodePermission(t, env)

			// Skills and guidelines stay readable. In normal mode reads are bounded
			// to mounted roots (each appears as an allow glob); in dangerous-skip
			// mode reads are noninteractive everywhere.
			if dsp {
				if got := permStringVal(t, perm, "read"); got != "allow" {
					t.Errorf("dangerous-skip read = %q, want allow", got)
				}
			} else {
				readPatterns := permPatternMap(t, perm, "read")
				for _, root := range []string{skillsDir, guidelinesDir} {
					glob := root + "/**"
					if readPatterns[glob] != "allow" {
						t.Errorf("read[%q] = %q, want allow (read-only mount must stay readable)", glob, readPatterns[glob])
					}
				}
			}

			// Skills and guidelines are never writable: they get no writable glob in
			// any file-mutating surface and the catch-all denies them — even in
			// dangerous-skip mode, where edits would otherwise be noninteractive.
			for _, key := range []string{"edit", "write", "apply_patch"} {
				patterns := permPatternMap(t, perm, key)
				if patterns["*"] != "deny" {
					t.Errorf("%s default = %q, want deny so read-only mounts are not writable", key, patterns["*"])
				}
				for _, root := range []string{skillsDir, guidelinesDir} {
					glob := root + "/**"
					if _, ok := patterns[glob]; ok {
						t.Errorf("%s includes writable glob %q for a read-only mount; want absent", key, glob)
					}
				}
			}
		})
	}
}

// TestOpenCodeNotSelectableByBareModel confirms that without the explicit
// "opencode:" prefix, a bare model never routes to OpenCode even when it is
// registered — so OpenCode cannot be silently selected through defaults.
func TestOpenCodeNotSelectableByBareModel(t *testing.T) {
	reg := newRegistryWithOpenCode()
	prov, _, err := reg.ResolveModel("sonnet")
	if err != nil {
		t.Fatalf("ResolveModel(sonnet) error: %v", err)
	}
	if prov.Name() == "opencode" {
		t.Fatal("bare model 'sonnet' routed to opencode; explicit-only gating violated")
	}
}
