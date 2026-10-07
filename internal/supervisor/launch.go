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
	"fmt"
	"path/filepath"
	"slices"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/agent/prompts"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// sessionLabel is the session-manager label for supervisor sessions.
const sessionLabel = "supervisor"

// SessionLauncher launches the supervisor provider through the phase
// runner's session builder and registers it with the session manager.
type SessionLauncher struct {
	Runner   *agent.PhaseRunner
	Sessions ports.SessionManager
	// RuntimeDir is the serving runtime directory, which holds this server's
	// discovery file. It is exported to the child as agent.RuntimeDirEnv so
	// the `agentico api` helper binds to this server.
	RuntimeDir string
	// ConfigPath is the serving config file, named in the system prompt.
	ConfigPath string
	// DiscoveryPath is this server's discovery file, named in the system
	// prompt so the model knows where the helper looks.
	DiscoveryPath string
	// AgenticoBin overrides the helper binary path; empty uses the running
	// executable, the same path exported as AGENTICO_BIN.
	AgenticoBin string
}

// skillRelPath locates the supervisor skill core inside the reconciled
// skills directory.
const skillRelPath = "supervisor/SKILL.md"

// Launch builds a harness-normal interactive session: the chosen model and
// effort, the supervisor permission policy, the supervisor system prompt on
// the launch channel, no disallowed tools, no completion protocol, no
// ask-user auto-pick and no tool watchdog. The first user message is sent by
// the coordinator after the handshake and stays the user's own text.
func (l *SessionLauncher) Launch(_ context.Context, req LaunchRequest) (ports.SessionView, error) {
	if l.Runner == nil || l.Sessions == nil {
		return nil, errors.New("supervisor launcher is not configured")
	}
	cmd, env, sessOpts, err := l.Runner.BuildSession(agent.BuildSessionOpts{
		Model:        req.Settings.Harness + ":" + req.Settings.Model,
		SystemPrompt: l.systemPrompt(req.WorkDir),
		WorkDir:      req.WorkDir,
		PIDDir:       req.PIDDir,
		LogPath:      req.LogPath,
		PermHandler:  &permission.SupervisorHandler{},
		TurnMode:     ports.TurnModeInteractive,
		EffortLevel:  llm.EffortLevel(req.Settings.Effort),
		Interactive:  true,
		// The coordinator rebuilt the native session from the transcript;
		// the harness resumes it so the model sees the prior conversation.
		ResumeSessionID: req.ResumeSessionID,
		SeedHistoryPath: req.SeedHistoryPath,
	})
	if err != nil {
		return nil, fmt.Errorf("build supervisor session: %w", err)
	}
	if l.RuntimeDir != "" {
		env = agent.SetEnv(env, agent.RuntimeDirEnv, l.RuntimeDir)
	}
	if sessOpts == nil {
		sessOpts = &ports.SessionOpts{}
	}
	// A supervisor turn may legitimately run for hours (monitoring, long
	// builds); only the handshake bound and process-exit detection remain.
	sessOpts.Watchdog = nil
	sessOpts.AskUserAutoPick = nil
	sessOpts.PIDDir = req.PIDDir
	sessOpts.Kind = ports.KindSupervisor
	sessOpts.TurnMode = ports.TurnModeInteractive
	sessOpts.Label = sessionLabel
	sessOpts.LogPath = req.LogPath
	sessOpts.StderrPath = req.StderrPath
	sessOpts.InitialPrompt = ""
	sessOpts.Observer = req.Observer
	if req.OnSpawned != nil {
		onSpawned := req.OnSpawned
		sessOpts.SessionBuildNotices = append(sessOpts.SessionBuildNotices, ports.SessionBuildNotice{
			Emit: func(ports.SessionBuildNoticeContext) { onSpawned() },
		})
	}
	sess, err := l.Sessions.StartSession(req.SessionID, FeatureID, feature.PhaseResearch, cmd, req.WorkDir, env, sessOpts)
	if err != nil {
		return nil, fmt.Errorf("start supervisor session: %w", err)
	}
	return sess, nil
}

// systemPrompt renders the launch-channel prompt from the runner's paths and
// this server's runtime identity.
func (l *SessionLauncher) systemPrompt(workDir string) string {
	bin := l.AgenticoBin
	if bin == "" {
		bin = agent.AgenticoBinPath()
	}
	var skillPath string
	if l.Runner.SkillsDir != "" {
		skillPath = filepath.Join(l.Runner.SkillsDir, filepath.FromSlash(skillRelPath))
	}
	return prompts.SupervisorSystemPrompt(prompts.SupervisorSystemInput{
		RuntimeDir:    l.RuntimeDir,
		StateDir:      l.Runner.StateDir,
		WorkDir:       workDir,
		ConfigPath:    l.ConfigPath,
		DiscoveryPath: l.DiscoveryPath,
		SkillPath:     skillPath,
		HelperCommand: bin + " api",
	})
}

// RegistryCatalog validates settings against the provider registry's
// chat-eligible models and per-model effort capabilities.
type RegistryCatalog struct {
	Registry *llm.Registry
}

func (c RegistryCatalog) DefaultModel(harness string) (string, error) {
	if c.Registry != nil {
		models := c.Registry.EligibleModelsForPhase(llm.PhaseChat)[harness]
		if len(models) > 0 {
			return models[0], nil
		}
	}
	return "", &SettingsInvalidError{Reason: fmt.Sprintf("no chat-eligible models for harness %q", harness)}
}

// ValidateSettings accepts a detected harness, one of its chat-eligible
// models, and an effort the model supports (empty means the default).
func (c RegistryCatalog) ValidateSettings(s Settings) error {
	if c.Registry == nil {
		return &SettingsInvalidError{Reason: "no providers are available"}
	}
	var provider llm.LLMProvider
	for _, p := range c.Registry.DetectedProviders() {
		if p.Name() == s.Harness {
			provider = p
			break
		}
	}
	if provider == nil {
		return &SettingsInvalidError{Reason: fmt.Sprintf("harness %q is not available", s.Harness)}
	}
	if !slices.Contains(c.Registry.EligibleModelsForPhase(llm.PhaseChat)[s.Harness], s.Model) {
		return &SettingsInvalidError{Reason: fmt.Sprintf("model %q is not available for harness %q", s.Model, s.Harness)}
	}
	if s.Effort != "" && !llm.EffortCapabilitySupported(llm.EffortCapabilitiesForModel(provider, s.Model), llm.EffortLevel(s.Effort)) {
		return &SettingsInvalidError{Reason: fmt.Sprintf("effort %q is not supported by model %q", s.Effort, s.Model)}
	}
	return nil
}
