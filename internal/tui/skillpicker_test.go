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

package tui

import (
	"os"
	"path/filepath"
	"testing"
)

func mkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSkillPickerDiscoverSkills(t *testing.T) {
	dir := t.TempDir()
	mkdirAll(t, filepath.Join(dir, ".claude", "skills", "my-skill"))
	writeFile(t, filepath.Join(dir, ".claude", "skills", "my-skill", "SKILL.md"),
		"---\nname: my-skill\ndescription: A test skill\n---\nBody")

	items := discoverRepoItems("test-repo", dir)
	var skills []SkillItem
	for _, item := range items {
		if item.Type == "skill" {
			skills = append(skills, item)
		}
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	if skills[0].Name != "my-skill" {
		t.Errorf("skill name = %q, want %q", skills[0].Name, "my-skill")
	}
	if skills[0].Description != "A test skill" {
		t.Errorf("skill description = %q, want %q", skills[0].Description, "A test skill")
	}
	if skills[0].RepoName != "test-repo" {
		t.Errorf("skill repo = %q, want %q", skills[0].RepoName, "test-repo")
	}
}

func TestSkillPickerDiscoverCommands(t *testing.T) {
	dir := t.TempDir()
	mkdirAll(t, filepath.Join(dir, ".claude", "commands"))
	writeFile(t, filepath.Join(dir, ".claude", "commands", "commit.md"),
		"---\ndescription: Create a git commit\n---\nBody")

	items := discoverRepoItems("test-repo", dir)
	var commands []SkillItem
	for _, item := range items {
		if item.Type == "command" {
			commands = append(commands, item)
		}
	}
	if len(commands) != 1 {
		t.Fatalf("expected 1 command, got %d", len(commands))
	}
	if commands[0].Name != "commit" {
		t.Errorf("command name = %q, want %q", commands[0].Name, "commit")
	}
	if commands[0].Description != "Create a git commit" {
		t.Errorf("command description = %q, want %q", commands[0].Description, "Create a git commit")
	}
}

func TestSkillPickerDiscoverCommandsNested(t *testing.T) {
	dir := t.TempDir()
	mkdirAll(t, filepath.Join(dir, ".claude", "commands", "ci"))
	writeFile(t, filepath.Join(dir, ".claude", "commands", "ci", "deploy.md"),
		"---\ndescription: Deploy to CI\n---\nBody")

	items := discoverRepoItems("test-repo", dir)
	var commands []SkillItem
	for _, item := range items {
		if item.Type == "command" {
			commands = append(commands, item)
		}
	}
	if len(commands) != 1 {
		t.Fatalf("expected 1 command, got %d", len(commands))
	}
	if commands[0].Name != "ci:deploy" {
		t.Errorf("nested command name = %q, want %q", commands[0].Name, "ci:deploy")
	}
}

func TestSkillPickerDiscoverAgents(t *testing.T) {
	dir := t.TempDir()
	mkdirAll(t, filepath.Join(dir, ".claude", "agents"))
	writeFile(t, filepath.Join(dir, ".claude", "agents", "analyzer.md"),
		"---\nname: analyzer\ndescription: Code analysis\n---\nBody")

	items := discoverRepoItems("test-repo", dir)
	var agents []SkillItem
	for _, item := range items {
		if item.Type == "agent" {
			agents = append(agents, item)
		}
	}
	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}
	if agents[0].Name != "analyzer" {
		t.Errorf("agent name = %q, want %q", agents[0].Name, "analyzer")
	}
	if agents[0].Description != "Code analysis" {
		t.Errorf("agent description = %q, want %q", agents[0].Description, "Code analysis")
	}
}

func TestSkillPickerSkillWithoutFrontmatter(t *testing.T) {
	dir := t.TempDir()
	mkdirAll(t, filepath.Join(dir, ".claude", "skills", "no-frontmatter"))
	writeFile(t, filepath.Join(dir, ".claude", "skills", "no-frontmatter", "SKILL.md"),
		"Just a skill body with no frontmatter")

	items := discoverRepoItems("test-repo", dir)
	var skills []SkillItem
	for _, item := range items {
		if item.Type == "skill" {
			skills = append(skills, item)
		}
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	// Should fallback to directory name
	if skills[0].Name != "no-frontmatter" {
		t.Errorf("skill name = %q, want %q (fallback to dir name)", skills[0].Name, "no-frontmatter")
	}
}

func TestSkillPickerAgentWithoutName(t *testing.T) {
	dir := t.TempDir()
	mkdirAll(t, filepath.Join(dir, ".claude", "agents"))
	writeFile(t, filepath.Join(dir, ".claude", "agents", "codebase-analyzer.md"),
		"---\ndescription: Analyze code\n---\nBody")

	items := discoverRepoItems("test-repo", dir)
	var agents []SkillItem
	for _, item := range items {
		if item.Type == "agent" {
			agents = append(agents, item)
		}
	}
	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}
	// Should fallback to filename without .md
	if agents[0].Name != "codebase-analyzer" {
		t.Errorf("agent name = %q, want %q (fallback to filename)", agents[0].Name, "codebase-analyzer")
	}
}

func TestParseSkillFrontmatter(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantKey string
		wantVal string
		wantNil bool
	}{
		{
			name:    "valid frontmatter",
			content: "---\nname: my-skill\ndescription: A test skill\n---\nBody",
			wantKey: "name",
			wantVal: "my-skill",
		},
		{
			name:    "no frontmatter",
			content: "Just a body",
			wantNil: true,
		},
		{
			name:    "unclosed frontmatter",
			content: "---\nname: my-skill\nno closing",
			wantNil: true,
		},
		{
			name:    "empty frontmatter",
			content: "---\n---\nBody",
			wantKey: "name",
			wantVal: "",
		},
		{
			name:    "multiline description takes first line",
			content: "---\ndescription: First line\n---\nBody",
			wantKey: "description",
			wantVal: "First line",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fm := parseSkillFrontmatter(tt.content)
			if tt.wantNil {
				if fm != nil {
					t.Errorf("expected nil, got %v", fm)
				}
				return
			}
			if fm == nil {
				t.Fatal("expected non-nil frontmatter")
			}
			if tt.wantVal != "" && fm[tt.wantKey] != tt.wantVal {
				t.Errorf("fm[%q] = %q, want %q", tt.wantKey, fm[tt.wantKey], tt.wantVal)
			}
		})
	}
}
