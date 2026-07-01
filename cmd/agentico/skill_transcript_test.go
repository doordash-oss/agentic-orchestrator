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

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgenticoIntegrationBundleDocumentsJSONOnlySkills(t *testing.T) {
	root := filepath.Join("..", "..", "agentico-agent-integration")
	required := map[string][]string{
		filepath.Join("skills", "agentico-create-feature", "SKILL.md"): {
			"agentico server ensure --json",
			"agentico feature select --json",
			"agentico feature create --json",
			"agentico feature action <feature-id> start --json",
			"agentico feature manage <feature-id> --json --watch",
			"harness owns reasoning",
			"Agentico owns runtime discovery, server startup, REST details, retries, event watching, and state classification",
		},
		filepath.Join("skills", "agentico-manage-feature", "SKILL.md"): {
			"agentico feature detail <feature-id> --json",
			"agentico feature manage <feature-id> --json --watch",
			"agentico feature answer <feature-id> --json",
			"agentico feature review <feature-id> --json",
			"agentico feature action <feature-id> publish --json",
			"feature-scoped context",
			"turn-by-turn snapshots",
		},
		filepath.Join("skills", "references", "creation-options.md"): {
			"CreateFeatureRequest",
			"repos",
			"checkpoints",
			"pipeline",
		},
		filepath.Join("skills", "references", "management-actions.md"): {
			"feature action",
			"feature answer",
			"feature review",
			"publish",
		},
		filepath.Join("skills", "references", "review-gates.md"): {
			"review_gate",
			"proceed",
			"iterate",
		},
		filepath.Join("skills", "references", "watch-events.md"): {
			"snapshot",
			"attention_required",
			"terminal",
		},
		filepath.Join("codex-plugin", "README.md"): {
			"thin wrapper",
			"../skills",
			"agentico-create-feature",
			"agentico-manage-feature",
		},
		filepath.Join("codex-plugin", ".codex-plugin", "plugin.json"): {
			`"name": "agentico-agent-integration"`,
			`"skills": "../skills/"`,
			"agentico-create-feature",
			"agentico-manage-feature",
		},
		filepath.Join("claude-code-plugin", "README.md"): {
			"thin wrapper",
			"../skills",
			"agentico-create-feature",
			"agentico-manage-feature",
		},
		filepath.Join("claude-code-plugin", "manifest.json"): {
			`"name": "agentico-agent-integration"`,
			`"skills": "../skills"`,
			"agentico-create-feature",
			"agentico-manage-feature",
		},
		filepath.Join("opencode-plugin", "README.md"): {
			"thin wrapper",
			"../skills",
			"agentico-create-feature",
			"agentico-manage-feature",
		},
		filepath.Join("opencode-plugin", "manifest.json"): {
			`"name": "agentico-agent-integration"`,
			`"skills": "../skills"`,
			"agentico-create-feature",
			"agentico-manage-feature",
		},
	}

	for rel, wants := range required {
		t.Run(rel, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(root, rel))
			if err != nil {
				t.Fatalf("read required bundle file: %v", err)
			}
			content := string(data)
			for _, want := range wants {
				if !strings.Contains(content, want) {
					t.Errorf("%s missing %q", rel, want)
				}
			}
			for _, forbidden := range []string{"curl ", "/api/v1/", "http://127.0.0.1"} {
				if strings.Contains(content, forbidden) {
					t.Errorf("%s contains forbidden non-CLI detail %q", rel, forbidden)
				}
			}
		})
	}
}

func TestAgenticoSkillTranscriptsUseJSONCLICommands(t *testing.T) {
	transcriptDir := filepath.Join("testdata", "skill-transcripts")
	requiredFlows := map[string][]string{
		"create.md": {
			"agentico server ensure --json",
			"agentico feature select --json",
			"agentico feature create --json",
			"agentico feature action feat-cli-1 start --json",
			"agentico feature manage feat-cli-1 --json --watch",
		},
		"active-manage.md": {
			"agentico feature detail feat-cli-1 --json",
			"agentico feature manage feat-cli-1 --json --watch",
		},
		"review-gate.md": {
			"agentico feature manage feat-cli-1 --json --watch",
			"agentico feature review feat-cli-1 --json",
		},
		"ask-user.md": {
			"agentico feature manage feat-cli-1 --json --watch",
			"agentico feature answer feat-cli-1 --json",
		},
		"permission.md": {
			"agentico feature manage feat-cli-1 --json --watch",
			"agentico feature answer feat-cli-1 --json",
		},
		"publish.md": {
			"agentico feature detail feat-cli-1 --json",
			"agentico feature action feat-cli-1 publish --json",
		},
		"parked-management.md": {
			"agentico feature detail feat-cli-parked --json",
			"agentico feature action feat-cli-parked resume --json",
		},
	}

	for name, wants := range requiredFlows {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(transcriptDir, name))
			if err != nil {
				t.Fatalf("read transcript fixture: %v", err)
			}
			content := string(data)
			for _, want := range wants {
				if !strings.Contains(content, "$ "+want) {
					t.Errorf("%s missing command %q", name, want)
				}
			}
			for _, cmd := range transcriptCommands(content) {
				if !strings.HasPrefix(cmd, "agentico ") {
					t.Errorf("%s command %q does not use agentico CLI", name, cmd)
				}
				if !strings.Contains(cmd, " --json") && !strings.HasSuffix(cmd, " --json") {
					t.Errorf("%s command %q does not request JSON output", name, cmd)
				}
				if strings.Contains(cmd, "curl ") || strings.Contains(cmd, "/api/v1/") {
					t.Errorf("%s command %q bypasses CLI JSON", name, cmd)
				}
			}
		})
	}
}

func transcriptCommands(content string) []string {
	var commands []string
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "$ ") {
			commands = append(commands, strings.TrimSpace(strings.TrimPrefix(line, "$ ")))
		}
	}
	return commands
}
