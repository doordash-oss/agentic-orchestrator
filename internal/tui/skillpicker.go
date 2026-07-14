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
	"strings"
)

// SkillItem represents a discoverable repo-level skill, command, or agent.
type SkillItem struct {
	Name        string // e.g., "commit", "codebase-analyzer"
	Type        string // "skill", "command", "agent"
	Description string // first line of description from frontmatter
	RepoName    string // which repo this belongs to
	Path        string // on-disk path to the skill/command/agent file
}

// discoverRepoItems scans a repo's .claude/ directory for skills, commands, and agents.
func discoverRepoItems(repoName, repoPath string) []SkillItem {
	var items []SkillItem
	claudeDir := filepath.Join(repoPath, ".claude")

	// Discover skills: .claude/skills/<name>/SKILL.md
	items = append(items, discoverSkills(repoName, claudeDir)...)

	// Discover commands: .claude/commands/**/*.md
	items = append(items, discoverCommands(repoName, claudeDir)...)

	// Discover agents: .claude/agents/*.md
	items = append(items, discoverAgents(repoName, claudeDir)...)

	return items
}

// discoverSkills scans .claude/skills/*/SKILL.md
func discoverSkills(repoName, claudeDir string) []SkillItem {
	var items []SkillItem
	skillsDir := filepath.Join(claudeDir, "skills")
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		return nil
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		skillFile := filepath.Join(skillsDir, entry.Name(), "SKILL.md")
		data, err := os.ReadFile(skillFile)
		if err != nil {
			continue
		}
		fm := parseSkillFrontmatter(string(data))
		name := fm["name"]
		if name == "" {
			name = entry.Name() // fallback to directory name
		}
		items = append(items, SkillItem{
			Name:        name,
			Type:        "skill",
			Description: firstLine(fm["description"]),
			RepoName:    repoName,
			Path:        skillFile,
		})
	}
	return items
}

// discoverCommands scans .claude/commands/**/*.md recursively.
func discoverCommands(repoName, claudeDir string) []SkillItem {
	var items []SkillItem
	commandsDir := filepath.Join(claudeDir, "commands")
	err := filepath.WalkDir(commandsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".md") {
			return nil
		}
		rel, _ := filepath.Rel(commandsDir, path)
		// Command name: strip .md, replace "/" with ":" for nested commands
		name := strings.TrimSuffix(rel, ".md")
		name = strings.ReplaceAll(name, string(filepath.Separator), ":")

		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		fm := parseSkillFrontmatter(string(data))
		items = append(items, SkillItem{
			Name:        name,
			Type:        "command",
			Description: firstLine(fm["description"]),
			RepoName:    repoName,
			Path:        path,
		})
		return nil
	})
	if err != nil {
		return nil
	}
	return items
}

// discoverAgents scans .claude/agents/*.md
func discoverAgents(repoName, claudeDir string) []SkillItem {
	var items []SkillItem
	agentsDir := filepath.Join(claudeDir, "agents")
	entries, err := os.ReadDir(agentsDir)
	if err != nil {
		return nil
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(agentsDir, entry.Name()))
		if err != nil {
			continue
		}
		fm := parseSkillFrontmatter(string(data))
		name := fm["name"]
		if name == "" {
			name = strings.TrimSuffix(entry.Name(), ".md")
		}
		items = append(items, SkillItem{
			Name:        name,
			Type:        "agent",
			Description: firstLine(fm["description"]),
			RepoName:    repoName,
			Path:        filepath.Join(agentsDir, entry.Name()),
		})
	}
	return items
}

// parseSkillFrontmatter does a simple key: value parse of YAML-like frontmatter.
// Handles only single-line scalar values (matches parseFrontmatterFields in agent package).
func parseSkillFrontmatter(content string) map[string]string {
	if !strings.HasPrefix(content, "---") {
		return nil
	}
	rest := content[3:]
	idx := strings.Index(rest, "---")
	if idx < 0 {
		return nil
	}
	fields := make(map[string]string)
	for _, line := range strings.Split(rest[:idx], "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		colonIdx := strings.Index(line, ":")
		if colonIdx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:colonIdx])
		val := strings.TrimSpace(line[colonIdx+1:])
		if key != "" {
			fields[key] = val
		}
	}
	return fields
}

// firstLine returns the first non-empty line of s, or s if single-line.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		return strings.TrimSpace(s[:idx])
	}
	return s
}
