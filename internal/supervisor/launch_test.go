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

package supervisor

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/agent/prompts"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// capturingSessions records the launch a SessionLauncher hands to the
// session manager and refuses to start it.
type capturingSessions struct {
	ports.SessionManager
	cmd  []string
	env  []string
	opts *ports.SessionOpts
}

var errCaptured = errors.New("captured")

func (c *capturingSessions) StartSession(_, _ string, _ feature.Phase, cmd []string, _ string, env []string, opts ...*ports.SessionOpts) (ports.SessionHandle, error) {
	c.cmd, c.env = cmd, env
	if len(opts) > 0 {
		c.opts = opts[0]
	}
	return nil, errCaptured
}

func newLaunchTestRunner(t *testing.T) (*agent.PhaseRunner, *capturingSessions, string) {
	t.Helper()
	script := testutil.WriteFakeClaudeScript(t, "exit 0\n")
	reg := testutil.NewFakeClaudeRegistry(t, script)
	sessions := &capturingSessions{}
	runtimeDir := t.TempDir()
	stateDir := filepath.Join(runtimeDir, "state")
	runner := agent.NewPhaseRunner(sessions, feature.NewStore(stateDir), stateDir)
	runner.Registry = reg
	runner.Config = config.NewDefault()
	runner.SkillsDir = filepath.Join(runtimeDir, "skills")
	return runner, sessions, runtimeDir
}

func TestSessionLauncher_CarriesSupervisorPromptAndRuntimeDirEnv(t *testing.T) {
	runner, sessions, runtimeDir := newLaunchTestRunner(t)
	launcher := &SessionLauncher{
		Runner:        runner,
		Sessions:      sessions,
		RuntimeDir:    runtimeDir,
		ConfigPath:    filepath.Join(runtimeDir, "config.yaml"),
		DiscoveryPath: filepath.Join(runtimeDir, ".agentico-server.json"),
		AgenticoBin:   "/opt/agentico/bin/agentico",
	}
	workDir := t.TempDir()
	_, err := launcher.Launch(context.Background(), LaunchRequest{
		SessionID: "supervisor-1",
		Settings:  Settings{Harness: "claude", Model: "haiku[200K]"},
		WorkDir:   workDir,
		PIDDir:    t.TempDir(),
	})
	if !errors.Is(err, errCaptured) {
		t.Fatalf("Launch err = %v, want the captured start", err)
	}
	want := prompts.SupervisorSystemPrompt(prompts.SupervisorSystemInput{
		RuntimeDir:    runtimeDir,
		StateDir:      runner.StateDir,
		WorkDir:       workDir,
		ConfigPath:    filepath.Join(runtimeDir, "config.yaml"),
		DiscoveryPath: filepath.Join(runtimeDir, ".agentico-server.json"),
		SkillPath:     filepath.Join(runtimeDir, "skills", "supervisor", "SKILL.md"),
		HelperCommand: "/opt/agentico/bin/agentico api",
	})
	if got := sessions.opts.DebugSystemPrompt; got != want {
		t.Fatalf("system prompt =\n%s\nwant\n%s", got, want)
	}
	if i := slices.Index(sessions.cmd, "--append-system-prompt"); i < 0 || i+1 >= len(sessions.cmd) || sessions.cmd[i+1] != want {
		t.Fatalf("launch command does not carry the prompt on its system-prompt flag: %q", sessions.cmd)
	}
	if sessions.opts.InitialPrompt != "" {
		t.Fatalf("initial prompt = %q, want none (the first message is the user's own text)", sessions.opts.InitialPrompt)
	}
	if !slices.Contains(sessions.env, agent.RuntimeDirEnv+"="+runtimeDir) {
		t.Fatalf("env %q lacks %s", sessions.env, agent.RuntimeDirEnv)
	}
	if !slices.ContainsFunc(sessions.env, func(e string) bool { return strings.HasPrefix(e, "AGENTICO_BIN=") }) {
		t.Fatalf("env %q lacks AGENTICO_BIN", sessions.env)
	}
}

func TestPhaseWorkerSessionCarriesNoSupervisorPromptOrRuntimeDir(t *testing.T) {
	runner, _, _ := newLaunchTestRunner(t)
	cmd, env, sessOpts, err := runner.BuildSession(agent.BuildSessionOpts{
		Model:        "claude:haiku[200K]",
		SystemPrompt: "phase role prompt",
		WorkDir:      t.TempDir(),
		PermHandler:  &permission.AcceptEditsHandler{},
		Phase:        feature.PhaseImplement,
	})
	if err != nil {
		t.Fatalf("BuildSession: %v", err)
	}
	if slices.ContainsFunc(env, func(e string) bool { return strings.HasPrefix(e, agent.RuntimeDirEnv+"=") }) {
		t.Fatalf("phase-worker env carries %s: %q", agent.RuntimeDirEnv, env)
	}
	if strings.Contains(strings.Join(cmd, "\n"), "Agentico supervisor") || strings.Contains(sessOpts.DebugSystemPrompt, "Agentico supervisor") {
		t.Fatalf("phase-worker session carries the supervisor prompt")
	}
}

func TestRegistryCatalog_ValidatesHarnessModelAndEffort(t *testing.T) {
	reg := llm.NewRegistry()
	reg.Register(testutil.FakeClaudeProvider{Script: "unused"})
	eligible := reg.EligibleModelsForPhase(llm.PhaseChat)["claude"]
	if len(eligible) == 0 {
		t.Fatal("fake Claude has no chat-eligible model")
	}
	catalog := RegistryCatalog{Registry: reg}
	if err := catalog.ValidateSettings(Settings{Harness: "claude", Model: eligible[0]}); err != nil {
		t.Fatalf("eligible model with default effort: %v", err)
	}
	var invalid *SettingsInvalidError
	for name, s := range map[string]Settings{
		"unknown harness":       {Harness: "nope", Model: eligible[0]},
		"model outside catalog": {Harness: "claude", Model: "opus-imaginary"},
		"unsupported effort":    {Harness: "claude", Model: eligible[0], Effort: "max"},
	} {
		if err := catalog.ValidateSettings(s); !errors.As(err, &invalid) {
			t.Fatalf("%s: err = %v, want SettingsInvalidError", name, err)
		}
	}
	if err := (RegistryCatalog{}).ValidateSettings(Settings{Harness: "claude", Model: eligible[0]}); !errors.As(err, &invalid) {
		t.Fatalf("nil registry err = %v", err)
	}
}
