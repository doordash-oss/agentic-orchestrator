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
