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
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

// SkillItem represents a discoverable repo-level skill, command, or agent.
type SkillItem struct {
	Name        string // e.g., "commit", "codebase-analyzer"
	Type        string // "skill", "command", "agent"
	Description string // first line of description from frontmatter
	RepoName    string // which repo this belongs to
	Path        string // on-disk path to the skill/command/agent file
}

// SkillPickerModel provides inline autocomplete for repo-level skills,
// commands, and agents. Triggered by "/" in text inputs. Unlike FilePickerModel
// which scans the filesystem on each prefix change, SkillPickerModel loads
// all items once and filters in-memory.
type SkillPickerModel struct {
	active     bool
	prefix     string      // text typed after trigger "/"
	allItems   []SkillItem // all items from all selected repos
	matches    []SkillItem // filtered by prefix
	cursor     int
	maxDisplay int
	multiRepo  bool // true when items come from multiple repos
}

// NewSkillPickerModel creates a new skill picker.
func NewSkillPickerModel() SkillPickerModel {
	return SkillPickerModel{maxDisplay: 8}
}

// LoadItems scans the .claude/ directories of the given repos to discover
// available skills, commands, and agents.
func (m *SkillPickerModel) LoadItems(repoPaths map[string]string, selectedRepos map[string]bool) {
	m.allItems = nil
	repoCount := 0
	for name, path := range repoPaths {
		if !selectedRepos[name] {
			continue
		}
		repoCount++
		m.allItems = append(m.allItems, discoverRepoItems(name, path)...)
	}
	m.multiRepo = repoCount > 1
	sort.Slice(m.allItems, func(i, j int) bool {
		if m.allItems[i].Type != m.allItems[j].Type {
			return m.allItems[i].Type < m.allItems[j].Type
		}
		return m.allItems[i].Name < m.allItems[j].Name
	})
}

// HasItems returns true if any items were discovered.
func (m SkillPickerModel) HasItems() bool {
	return len(m.allItems) > 0
}

// Activate starts the picker with an initial prefix (typically "").
func (m *SkillPickerModel) Activate(prefix string) {
	m.active = true
	m.prefix = prefix
	m.cursor = 0
	m.updateMatches()
}

// Deactivate hides the picker.
func (m *SkillPickerModel) Deactivate() {
	m.active = false
	m.prefix = ""
	m.matches = nil
	m.cursor = 0
}

// IsActive returns whether the picker is visible.
func (m SkillPickerModel) IsActive() bool {
	return m.active
}

// SelectedName returns the name of the currently highlighted item.
func (m SkillPickerModel) SelectedName() string {
	if m.cursor >= 0 && m.cursor < len(m.matches) {
		return m.matches[m.cursor].Name
	}
	return m.prefix
}

// SetPrefix updates the search prefix and refreshes matches.
func (m *SkillPickerModel) SetPrefix(prefix string) {
	if prefix == m.prefix {
		return
	}
	m.prefix = prefix
	m.cursor = 0
	m.updateMatches()
}

// Update handles navigation keys while the picker is active.
// Returns (updated model, selected name or "", whether event was consumed).
func (m SkillPickerModel) Update(msg tea.KeyPressMsg) (SkillPickerModel, string, bool) {
	switch {
	case key.Matches(msg, key.NewBinding(key.WithKeys("tab"))),
		key.Matches(msg, key.NewBinding(key.WithKeys("enter"))):
		selected := m.SelectedName()
		m.Deactivate()
		return m, selected, true

	case key.Matches(msg, key.NewBinding(key.WithKeys("esc"))):
		m.Deactivate()
		return m, "", true

	case key.Matches(msg, key.NewBinding(key.WithKeys("up"))),
		key.Matches(msg, key.NewBinding(key.WithKeys("ctrl+p"))):
		if m.cursor > 0 {
			m.cursor--
		}
		return m, "", true

	case key.Matches(msg, key.NewBinding(key.WithKeys("down"))),
		key.Matches(msg, key.NewBinding(key.WithKeys("ctrl+n"))):
		if m.cursor < len(m.matches)-1 {
			m.cursor++
		}
		return m, "", true
	}

	return m, "", false
}

// View renders the autocomplete suggestions dropdown.
func (m SkillPickerModel) View() string {
	if !m.active || len(m.matches) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString(MutedStyle.Render("  Skill completions:") + "\n")

	start := 0
	end := len(m.matches)
	if end > m.maxDisplay {
		start = m.cursor - m.maxDisplay/2
		if start < 0 {
			start = 0
		}
		end = start + m.maxDisplay
		if end > len(m.matches) {
			end = len(m.matches)
			start = end - m.maxDisplay
		}
	}

	for i := start; i < end; i++ {
		cursor := "  "
		if i == m.cursor {
			cursor = "> "
		}
		item := m.matches[i]
		display := "/" + item.Name
		meta := item.Type
		if m.multiRepo {
			meta = item.RepoName + " · " + item.Type
		}
		if item.Description != "" {
			display += "  " + MutedStyle.Render(item.Description)
		}
		display += "  " + MutedStyle.Render("("+meta+")")
		if len(display) > 80 {
			display = display[:77] + "..."
		}
		b.WriteString("  " + cursor + display + "\n")
	}

	if len(m.matches) > m.maxDisplay {
		b.WriteString(MutedStyle.Render(fmt.Sprintf("  (%d of %d)", end-start, len(m.matches))) + "\n")
	}

	return b.String()
}

// updateMatches filters allItems by the current prefix.
func (m *SkillPickerModel) updateMatches() {
	m.matches = nil
	lower := strings.ToLower(m.prefix)
	for _, item := range m.allItems {
		if lower == "" || strings.HasPrefix(strings.ToLower(item.Name), lower) {
			m.matches = append(m.matches, item)
		}
	}
	if len(m.matches) > 50 {
		m.matches = m.matches[:50]
	}
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

// findSkillTriggerSlash returns the index of the last "/" in s that is preceded
// by start-of-string or a space character — i.e., a valid skill trigger position.
// Returns -1 if no valid trigger slash is found.
func findSkillTriggerSlash(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '/' {
			if i == 0 || s[i-1] == ' ' {
				return i
			}
		}
	}
	return -1
}
