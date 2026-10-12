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

package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// baseURLEnvVar overrides the Anthropic API endpoint Claude Code calls.
const baseURLEnvVar = "ANTHROPIC_BASE_URL"

// managedSettingsPath is Claude Code's system-wide managed settings file for
// this platform; a variable so tests can point it at a fixture.
var managedSettingsPath = map[string]string{
	"darwin": "/Library/Application Support/ClaudeCode/managed-settings.json",
	"linux":  "/etc/claude-code/managed-settings.json",
}[runtime.GOOS]

// ConfiguredEndpoints returns the custom ANTHROPIC_BASE_URL Claude Code is
// configured with, resolved with Claude's precedence: managed settings env,
// then user settings env (CLAUDE_CONFIG_DIR or ~/.claude), then the process
// environment. The default Anthropic endpoint is not reported.
func (p *Provider) ConfiguredEndpoints() []string {
	for _, path := range claudeSettingsPaths() {
		if raw, ok := settingsEnv(path, baseURLEnvVar); ok {
			return endpointList(raw)
		}
	}
	return endpointList(os.Getenv(baseURLEnvVar))
}

func endpointList(raw string) []string {
	if endpoint, ok := llm.EndpointURL(raw); ok {
		return []string{endpoint}
	}
	return nil
}

// claudeSettingsPaths lists settings files whose env block applies to every
// Claude Code launch, highest precedence first.
func claudeSettingsPaths() []string {
	var paths []string
	if managedSettingsPath != "" {
		paths = append(paths, managedSettingsPath)
	}
	dir := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR"))
	if dir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(home, ".claude")
		}
	}
	if dir != "" {
		paths = append(paths, filepath.Join(dir, "settings.json"))
	}
	return paths
}

// settingsEnv reads one env var from a settings file's "env" block.
func settingsEnv(path, key string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	var settings struct {
		Env map[string]string `json:"env"`
	}
	if json.Unmarshal(data, &settings) != nil {
		return "", false
	}
	value, ok := settings.Env[key]
	return value, ok && strings.TrimSpace(value) != ""
}

var _ llm.EndpointReporter = (*Provider)(nil)
