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

	"github.com/cli/go-gh/v2/pkg/auth"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
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

// ToolIssue is one structured tool-check finding: the catalog code that
// owns its text, optional summary params, and raw diagnostics. Helpers
// return these instead of pre-formatted `Error:`/`Warning:` strings; the
// CLI renders them through the shared renderer.
type ToolIssue struct {
	Code        errcat.Code
	Params      errcat.Params
	Diagnostics string
}

// CheckRequiredTools verifies that external CLI tools needed by agentic are
// available in PATH. Hard issues block startup; soft issues are degradations
// the caller renders as warnings.
func CheckRequiredTools() (hard, soft []ToolIssue) {
	return checkRequiredTools(exec.LookPath)
}

func checkRequiredTools(lookPath func(string) (string, error)) (hard, soft []ToolIssue) {
	if _, err := lookPath("git"); err != nil {
		hard = append(hard, ToolIssue{
			Code:        errcat.MissingExecutable,
			Diagnostics: "git not found in PATH. Install from https://git-scm.com/downloads",
		})
	}
	if token, _ := auth.TokenForHost("github.com"); token == "" {
		soft = append(soft, ToolIssue{
			Code:        errcat.GithubCredentialsMissing,
			Diagnostics: "no GitHub credentials found",
		})
	}
	return hard, soft
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
