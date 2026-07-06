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
	"fmt"
	"os/exec"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// FormatNoCLIMessage generates a user-friendly message when no provider CLIs
// are detected. Includes install hints for each registered provider.
func FormatNoCLIMessage(providers []llm.LLMProvider) string {
	var b strings.Builder
	b.WriteString("No AI coding assistant CLIs detected.\n\n")
	b.WriteString("Agentic Orchestrator requires at least one of these tools to be installed:\n\n")
	for _, p := range providers {
		fmt.Fprintf(&b, "  %-8s %s\n", p.Name(), p.InstallHint())
	}
	b.WriteString("\nInstall one and run 'agentico' again.")
	return b.String()
}

// CheckRequiredTools verifies that external CLI tools needed by agentic are
// available in PATH. Returns hard errors (missing required tools) and soft
// warnings (missing optional tools) as separate slices.
func CheckRequiredTools() (errors []string, warnings []string) {
	type tool struct {
		name     string
		required bool
		hint     string
	}
	tools := []tool{
		{"git", true, "Install from https://git-scm.com/downloads"},
		{"gh", false, "Install from https://cli.github.com/ (required for PR publishing)"},
	}
	for _, t := range tools {
		if _, err := exec.LookPath(t.name); err != nil {
			msg := fmt.Sprintf("%s not found in PATH. %s", t.name, t.hint)
			if t.required {
				errors = append(errors, fmt.Sprintf("Error: %s", msg))
			} else {
				warnings = append(warnings, fmt.Sprintf("Warning: %s", msg))
			}
		}
	}
	return errors, warnings
}

// ApplyStartupDefaults converts a PhaseRole→string default map to the format
// expected by config.ApplyProviderDefaults and applies it. Returns true if
// any config fields were changed (useful for deciding whether to save).
func ApplyStartupDefaults(cfg *config.Config, defaultModels map[string]string) bool {
	// Snapshot model fields before
	before := cfg.Defaults.Models

	config.ApplyProviderDefaults(cfg, defaultModels)

	// Compare after
	return cfg.Defaults.Models != before
}
