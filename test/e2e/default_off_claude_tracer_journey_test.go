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

package e2e

import (
	"path/filepath"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// TestDefaultOffClaudeTracerJourney is the deterministic fake-Claude journey
// covering the default-off-to-enabled automatic Bash-review path. It exercises
// the real PhaseRunner.BuildSession chokepoint to verify the global setting
// affects subsequently created sessions while preserving the ordinary
// callback/prompt flow. Edge-case decorator coverage (variants, defers,
// timeout, malformed output, provider failure, existing allow/deny, control
// requests, error results) lives in internal/agent/autoreview_internal_test.go
// alongside the code it exercises. The test name is retained for
// testing-contract compatibility.
func TestDefaultOffClaudeTracerJourney(t *testing.T) {
	if testing.Short() {
		t.Skip("journey test launches fake Claude subprocesses")
	}
	workDir := t.TempDir()

	bashReq := func(command string) ports.ToolPermissionRequest {
		return ports.ToolPermissionRequest{ToolName: "Bash", Input: `{"command":"` + command + `"}`}
	}

	// buildSessionViaPhaseRunner creates a PhaseRunner with the given config
	// and registry, calls BuildSession with a general-phase AcceptEdits
	// handler, and returns the session's permission handler.
	buildSessionViaPhaseRunner := func(t *testing.T, cfg *config.Config, reg *llm.Registry, workDir string) ports.PermissionHandler {
		t.Helper()
		stateDir := t.TempDir()
		store := feature.NewStore(stateDir)
		pr := agent.NewPhaseRunner(nil, store, stateDir)
		pr.Registry = reg
		pr.Config = cfg
		_, _, sessOpts, err := pr.BuildSession(agent.BuildSessionOpts{
			Model:       "haiku",
			Prompt:      "test",
			PermHandler: permission.Guarded(&permission.AcceptEditsHandler{}),
			WorkDir:     workDir,
			Phase:       feature.PhaseImplement,
		})
		if err != nil {
			t.Fatalf("BuildSession failed: %v", err)
		}
		if sessOpts == nil || sessOpts.PermHandler == nil {
			t.Fatalf("BuildSession returned nil sessOpts or PermHandler")
		}
		return sessOpts.PermHandler
	}

	t.Run("vertical_journey_disabled_config_does_not_decorate", func(t *testing.T) {
		// Persist a config with AutomaticReviewEnabled disabled, load it,
		// and build a session. The session's permission handler must defer
		// exact-command Bash to the human prompt (no reviewer invoked).
		stateDir := t.TempDir()
		configPath := filepath.Join(stateDir, "config.yaml")
		cfg := config.NewDefault()
		cfg.Defaults.AutomaticReviewEnabled = false
		if err := config.Save(configPath, cfg); err != nil {
			t.Fatalf("Save config: %v", err)
		}
		loaded, err := config.Load(configPath)
		if err != nil {
			t.Fatalf("Load config: %v", err)
		}
		reg := fakeRegistry(t, testutil.FakeClaudeAllowScriptBody())
		handler := buildSessionViaPhaseRunner(t, loaded, reg, workDir)
		got, err := handler.CanUseTool(bashReq("go test ./..."))
		if err != nil || got.Behavior != "" {
			t.Fatalf("disabled config should defer to human: got %+v err %v", got, err)
		}
	})

	t.Run("vertical_journey_enabled_config_decorates_and_approves", func(t *testing.T) {
		// Persist a config with AutomaticReviewEnabled enabled, reload it,
		// and build a new session. The session's permission handler must
		// auto-approve the exact command via the fake Claude reviewer.
		stateDir := t.TempDir()
		configPath := filepath.Join(stateDir, "config.yaml")
		cfg := config.NewDefault()
		cfg.Defaults.AutomaticReviewEnabled = true
		if err := config.Save(configPath, cfg); err != nil {
			t.Fatalf("Save config: %v", err)
		}
		loaded, err := config.Load(configPath)
		if err != nil {
			t.Fatalf("Load config: %v", err)
		}
		reg := fakeRegistry(t, testutil.FakeClaudeAllowScriptBody())
		handler := buildSessionViaPhaseRunner(t, loaded, reg, workDir)
		got, err := handler.CanUseTool(bashReq("go test ./..."))
		if err != nil || got.Behavior != "allow" {
			t.Fatalf("enabled config should auto-approve: got %+v err %v", got, err)
		}
	})

	t.Run("vertical_journey_snapshot_isolates_from_config_edit", func(t *testing.T) {
		// Build a session with auto-review enabled, then disable it in the
		// config. The first session's handler must still auto-approve
		// because the snapshot was taken at build time. A second session
		// built after the edit must not auto-approve.
		cfg := config.NewDefault()
		cfg.Defaults.AutomaticReviewEnabled = true
		reg := fakeRegistry(t, testutil.FakeClaudeAllowScriptBody())
		handler1 := buildSessionViaPhaseRunner(t, cfg, reg, workDir)

		// Edit config: disable auto-review.
		cfg.Defaults.AutomaticReviewEnabled = false

		// First session's handler is unaffected by the edit.
		got1, err := handler1.CanUseTool(bashReq("go test ./..."))
		if err != nil || got1.Behavior != "allow" {
			t.Fatalf("first session should still auto-approve (snapshot): got %+v err %v", got1, err)
		}

		// Second session built after the edit must not auto-approve.
		handler2 := buildSessionViaPhaseRunner(t, cfg, reg, workDir)
		got2, err := handler2.CanUseTool(bashReq("go test ./..."))
		if err != nil || got2.Behavior != "" {
			t.Fatalf("second session should defer after config disable: got %+v err %v", got2, err)
		}
	})

	t.Run("vertical_journey_preserves_original_callback_input", func(t *testing.T) {
		// The auto-approval decision must come through the existing permission
		// handler interface with the original request input unchanged. The
		// decorator returns a plain allow decision; the request's Input field
		// is not modified.
		cfg := config.NewDefault()
		cfg.Defaults.AutomaticReviewEnabled = true
		reg := fakeRegistry(t, testutil.FakeClaudeAllowScriptBody())
		handler := buildSessionViaPhaseRunner(t, cfg, reg, workDir)
		originalInput := `{"command":"go test ./..."}`
		req := ports.ToolPermissionRequest{ToolName: "Bash", Input: originalInput}
		got, err := handler.CanUseTool(req)
		if err != nil || got.Behavior != "allow" {
			t.Fatalf("expected auto-approve: got %+v err %v", got, err)
		}
		if req.Input != originalInput {
			t.Errorf("request Input was modified: got %q, want %q", req.Input, originalInput)
		}
	})

	t.Run("vertical_journey_crash_resume_retains_reviewer", func(t *testing.T) {
		// Build a session with auto-review enabled and a fake Claude that
		// allows. Snapshot the resolved reviewer identity. Simulate
		// crash-resume with a provider whose bare-auth is no longer
		// usable (e.g. switched from API key to OAuth). ResolveReviewer
		// would reject this provider, but crash-resume uses RestoreReviewer
		// which ignores bare-auth and restores the reviewer from the
		// snapshot. The session model still resolves (ResolveModel does
		// not check bare-auth), and the decorator is created. The restored
		// reviewer's script exits immediately, so classification fails and
		// defers to the human prompt — but the reviewer identity is
		// retained from the original session, not re-resolved.
		cfg := config.NewDefault()
		cfg.Defaults.AutomaticReviewEnabled = true
		reg := fakeRegistry(t, testutil.FakeClaudeAllowScriptBody())
		stateDir := t.TempDir()
		store := feature.NewStore(stateDir)
		pr := agent.NewPhaseRunner(nil, store, stateDir)
		pr.Registry = reg
		pr.Config = cfg

		_, _, sessOpts, err := pr.BuildSession(agent.BuildSessionOpts{
			Model:       "haiku",
			Prompt:      "test",
			PermHandler: permission.Guarded(&permission.AcceptEditsHandler{}),
			WorkDir:     workDir,
			Phase:       feature.PhaseImplement,
		})
		if err != nil {
			t.Fatalf("BuildSession failed: %v", err)
		}
		if sessOpts.AutoReview.ReviewerProvider != "claude" {
			t.Fatalf("expected ReviewerProvider=claude in snapshot, got %q", sessOpts.AutoReview.ReviewerProvider)
		}
		if sessOpts.AutoReview.ReviewerModel == "" {
			t.Fatalf("expected non-empty ReviewerModel in snapshot")
		}

		// Simulate crash-resume: the provider's bare-auth changed (e.g.
		// from API key to OAuth). The session model still resolves, but
		// ResolveReviewer would reject this provider. The snapshot's
		// reviewer identity is restored instead.
		oauthReg := llm.NewRegistry()
		oauthReg.Register(oauthFakeClaude{
			FakeClaudeProvider: testutil.FakeClaudeProvider{
				Script: testutil.WriteFakeClaudeScript(t, testutil.FakeClaudeExitScriptBody()),
			},
		})
		pr.Registry = oauthReg
		_, _, sessOpts2, err := pr.BuildSession(agent.BuildSessionOpts{
			Model:       "haiku",
			Prompt:      "test",
			PermHandler: permission.Guarded(&permission.AcceptEditsHandler{}),
			WorkDir:     workDir,
			Phase:       feature.PhaseImplement,
			AutoReview:  sessOpts.AutoReview,
		})
		if err != nil {
			t.Fatalf("crash-resume BuildSession failed: %v", err)
		}
		if sessOpts2 == nil || sessOpts2.PermHandler == nil {
			t.Fatalf("crash-resume returned nil sessOpts or PermHandler")
		}
		// The snapshot identity is preserved through the resume.
		if sessOpts2.AutoReview.ReviewerProvider != sessOpts.AutoReview.ReviewerProvider {
			t.Fatalf("resume ReviewerProvider = %q, want %q (snapshot)",
				sessOpts2.AutoReview.ReviewerProvider, sessOpts.AutoReview.ReviewerProvider)
		}
		if sessOpts2.AutoReview.ReviewerModel != sessOpts.AutoReview.ReviewerModel {
			t.Fatalf("resume ReviewerModel = %q, want %q (snapshot)",
				sessOpts2.AutoReview.ReviewerModel, sessOpts.AutoReview.ReviewerModel)
		}
		// The decorator was created (enabled=true from snapshot) with the
		// restored reviewer. The restored reviewer's script exits, so
		// classification fails and defers to the human prompt.
		got, err := sessOpts2.PermHandler.CanUseTool(bashReq("go test ./..."))
		if err != nil || got.Behavior != "" {
			t.Fatalf("crash-resume with changed auth should defer: got %+v err %v", got, err)
		}
	})
}

// oauthFakeClaude wraps FakeClaudeProvider but reports bare auth as
// unusable, simulating a Claude installation that switched from API key
// to OAuth between the original session and crash-resume.
type oauthFakeClaude struct {
	testutil.FakeClaudeProvider
}

func (oauthFakeClaude) CheckBareAuth() bool { return false }

// fakeRegistry creates a Registry with a single FakeClaudeProvider running a
// script built from the given body.
func fakeRegistry(t *testing.T, scriptBody string) *llm.Registry {
	t.Helper()
	return testutil.NewFakeClaudeRegistry(t, testutil.WriteFakeClaudeScript(t, scriptBody))
}
