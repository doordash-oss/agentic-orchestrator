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
	"sort"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/doordash-oss/agentic-orchestrator/internal/skilldef"
)

// AutocompleteMode identifies which trigger character activated autocomplete.
type AutocompleteMode int

const (
	AutocompleteSkill AutocompleteMode = iota
	AutocompleteFile
)

// AutocompleteItem is a single entry in the autocomplete dropdown.
type AutocompleteItem struct {
	Name        string
	Description string
	Source      string // "agentic", "repo", "global" for skills; "file" for files
	Path        string // on-disk path to the skill/command file (empty for file-mode items)
}

// AutocompleteModel manages the dropdown state for the attach view.
type AutocompleteModel struct {
	active        bool
	mode          AutocompleteMode
	cursor        int
	scrollOffset  int                // first visible item index in the dropdown
	items         []AutocompleteItem // all items for current mode
	filtered      []AutocompleteItem // items matching query
	query         string
	triggerOffset int  // byte offset in textarea Value() where trigger char sits
	loading       bool // true while async skill discovery is in flight
}

const autocompleteMaxVisible = 5

// Activate sets the autocomplete to active with the given mode, trigger offset,
// and items. Filtered list is computed from the initial query.
// The model remains active even when filtered is empty (renders "No results").
func (a AutocompleteModel) Activate(mode AutocompleteMode, triggerOffset int, query string, items []AutocompleteItem) AutocompleteModel {
	a.active = true
	a.mode = mode
	a.triggerOffset = triggerOffset
	a.query = query
	a.items = items
	a.filtered = filterItems(items, query, mode)
	a.cursor = 0
	a.loading = false
	return a
}

// ActivateLoading sets the autocomplete to active in a loading state.
// Shows "Loading skills..." until items arrive via a refresh call.
func (a AutocompleteModel) ActivateLoading(mode AutocompleteMode, triggerOffset int, query string) AutocompleteModel {
	a.active = true
	a.mode = mode
	a.triggerOffset = triggerOffset
	a.query = query
	a.items = nil
	a.filtered = nil
	a.cursor = 0
	a.loading = true
	return a
}

// Dismiss deactivates the autocomplete, clearing all state.
func (a AutocompleteModel) Dismiss() AutocompleteModel {
	return AutocompleteModel{}
}

// UpdateQuery updates the query and refilters the items.
// The model stays active even when no items match — View() renders a
// "No results" placeholder. Dismissal only happens via explicit user action
// (Escape, Enter, or backspacing past the trigger character).
func (a AutocompleteModel) UpdateQuery(query string) AutocompleteModel {
	a.query = query
	a.filtered = filterItems(a.items, query, a.mode)
	if a.cursor >= len(a.filtered) {
		a.cursor = max(len(a.filtered)-1, 0)
	}
	// Reset scroll when filtered list changes so cursor stays visible.
	a.scrollOffset = 0
	if a.cursor >= autocompleteMaxVisible {
		a.scrollOffset = a.cursor - autocompleteMaxVisible + 1
	}
	return a
}

// MoveUp moves the cursor up by one, wrapping to the bottom.
// The scroll offset adjusts only when the cursor leaves the visible window.
func (a AutocompleteModel) MoveUp() AutocompleteModel {
	if len(a.filtered) == 0 {
		return a
	}
	a.cursor--
	if a.cursor < 0 {
		a.cursor = len(a.filtered) - 1
		// Wrapped to bottom — snap window to show the last items.
		a.scrollOffset = max(len(a.filtered)-autocompleteMaxVisible, 0)
	} else if a.cursor < a.scrollOffset {
		a.scrollOffset = a.cursor
	}
	return a
}

// MoveDown moves the cursor down by one, wrapping to the top.
// The scroll offset adjusts only when the cursor leaves the visible window.
func (a AutocompleteModel) MoveDown() AutocompleteModel {
	if len(a.filtered) == 0 {
		return a
	}
	a.cursor++
	if a.cursor >= len(a.filtered) {
		a.cursor = 0
		a.scrollOffset = 0 // Wrapped to top — snap window to the start.
	} else if a.cursor >= a.scrollOffset+autocompleteMaxVisible {
		a.scrollOffset = a.cursor - autocompleteMaxVisible + 1
	}
	return a
}

// Selected returns the currently highlighted item, or nil if filtered is empty.
func (a AutocompleteModel) Selected() *AutocompleteItem {
	if len(a.filtered) == 0 {
		return nil
	}
	item := a.filtered[a.cursor]
	return &item
}

// View renders the dropdown box. Width controls the maximum dropdown width.
// When filtered is empty, renders a single-line "No results" placeholder.
func (a AutocompleteModel) View(width int) string {
	if width < 10 {
		width = 10
	}

	borderStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorSurface).
		Padding(0, 1)

	if len(a.filtered) == 0 {
		var msg string
		if a.loading {
			if a.mode == AutocompleteFile {
				msg = "Loading files..."
			} else {
				msg = "Loading skills..."
			}
		} else {
			msg = "No results"
		}
		content := MutedStyle.Render(msg)
		return borderStyle.Width(width).Render(content)
	}

	// Use the tracked scroll offset for the visible window.
	start := a.scrollOffset
	end := start + autocompleteMaxVisible
	if end > len(a.filtered) {
		end = len(a.filtered)
	}
	visible := a.filtered[start:end]

	highlightStyle := lipgloss.NewStyle().Foreground(colorBrand).Bold(true)
	normalStyle := lipgloss.NewStyle()
	sourceStyle := lipgloss.NewStyle().Foreground(colorInfo)
	descStyle := MutedStyle

	// Reserve space for name + source + gap + description within the content area.
	// Content area is width minus border (2) and padding (2).
	contentW := max(width-4, 6)

	var lines []string
	for i, item := range visible {
		name := item.Name
		desc := item.Description

		// Build source suffix for skill mode (not file mode).
		var sourceSuffix string
		var sourceSuffixW int
		if a.mode == AutocompleteSkill && item.Source != "" && item.Source != "file" {
			sourceSuffix = " (" + sourceDisplayLabel(item.Source) + ")"
			sourceSuffixW = len(sourceSuffix)
		}

		nameW := lipgloss.Width(name)
		gap := 2
		usedW := nameW + sourceSuffixW + gap
		descMaxW := max(contentW-usedW, 0)
		if lipgloss.Width(desc) > descMaxW {
			if descMaxW > 3 {
				desc = desc[:descMaxW-3] + "..."
			} else {
				desc = ""
			}
		}

		var line string
		if i+start == a.cursor {
			line = highlightStyle.Render(name)
		} else {
			line = normalStyle.Render(name)
		}
		if sourceSuffix != "" {
			line += sourceStyle.Render(sourceSuffix)
		}
		if desc != "" {
			line += strings.Repeat(" ", gap) + descStyle.Render(desc)
		}
		lines = append(lines, line)
	}

	content := strings.Join(lines, "\n")
	return borderStyle.Width(width).Render(content)
}

// SetLoading sets the loading flag without changing other state.
func (a AutocompleteModel) SetLoading(loading bool) AutocompleteModel {
	a.loading = loading
	return a
}

// Active reports whether the autocomplete dropdown is visible.
func (a AutocompleteModel) Active() bool { return a.active }

// Mode returns the current autocomplete trigger mode.
func (a AutocompleteModel) Mode() AutocompleteMode { return a.mode }

// Query returns the current query string typed after the trigger character.
func (a AutocompleteModel) Query() string { return a.query }

// TriggerOffset returns the byte offset of the trigger character in the textarea value.
func (a AutocompleteModel) TriggerOffset() int { return a.triggerOffset }

// Loading reports whether the autocomplete is in a loading state.
func (a AutocompleteModel) Loading() bool { return a.loading }

// filterItems applies prefix matching (skill mode) or returns items as-is (file mode).
// File mode items are pre-filtered by FileIndex.Search(), so filterItems is an identity.
func filterItems(items []AutocompleteItem, query string, mode AutocompleteMode) []AutocompleteItem {
	if mode == AutocompleteFile {
		return items
	}

	if query == "" {
		result := make([]AutocompleteItem, len(items))
		copy(result, items)
		return result
	}

	lowerQuery := strings.ToLower(query)
	var result []AutocompleteItem
	for _, item := range items {
		if strings.HasPrefix(strings.ToLower(item.Name), lowerQuery) {
			result = append(result, item)
		}
	}
	return result
}

// detectTrigger scans backward from the cursor position in the textarea value
// to find a '/' or '@' preceded by whitespace or at position 0. Returns the
// trigger mode, the byte offset of the trigger char, and the query string after
// the trigger. ok is false if no valid trigger is found.
//
// When a '/' or '@' is found at an invalid position (not preceded by whitespace
// and not at position 0), the scan continues backward. This allows file paths
// inside an '@' query to contain '/' characters (e.g., "@src/main.go").
func detectTrigger(value string, cursorOffset int) (mode AutocompleteMode, triggerOffset int, query string, ok bool) {
	if cursorOffset <= 0 || cursorOffset > len(value) {
		return 0, 0, "", false
	}

	// Walk backward from cursorOffset-1 to find a trigger character.
	for i := cursorOffset - 1; i >= 0; i-- {
		ch := value[i]
		switch ch {
		case '/', '@':
			// Check if preceded by whitespace, newline, or at position 0.
			if i == 0 || value[i-1] == ' ' || value[i-1] == '\t' || value[i-1] == '\n' {
				if ch == '/' {
					mode = AutocompleteSkill
				} else {
					mode = AutocompleteFile
				}
				return mode, i, value[i+1 : cursorOffset], true
			}
			// Trigger char not at valid position — continue scanning backward
			// to find an earlier valid trigger (e.g., "@src/main" has "/" mid-path
			// but "@" at a valid position).
		case ' ', '\t', '\n':
			// Hit whitespace before finding a trigger — no trigger.
			return 0, 0, "", false
		}
	}

	return 0, 0, "", false
}

// cursorByteOffset computes the byte offset in the textarea Value() string
// for the given row and column (both 0-based).
func cursorByteOffset(value string, row, col int) int {
	offset := 0
	currentRow := 0

	for currentRow < row {
		idx := strings.IndexByte(value[offset:], '\n')
		if idx < 0 {
			// Row exceeds actual line count — return end of value.
			return len(value)
		}
		offset += idx + 1
		currentRow++
	}

	// Add column offset, clamped to end of current line or value.
	lineEnd := strings.IndexByte(value[offset:], '\n')
	if lineEnd < 0 {
		lineEnd = len(value) - offset
	}
	if col > lineEnd {
		col = lineEnd
	}
	return offset + col
}

// discoverAllSkills loads skills from all three sources: embedded, repo, and global.
// Returns all items (no deduplication) sorted by name, then source priority.
func discoverAllSkills(repoPath, skillsDir string) []AutocompleteItem {
	return discoverAllSkillsWith(repoPath, "", skillsDir)
}

// discoverAllSkillsWith is the testable core of discoverAllSkills.
// globalDir overrides the global commands directory; empty string uses the default.
// skillsDir is the reconciled agentic skills directory on disk.
func discoverAllSkillsWith(repoPath, globalDir, skillsDir string) []AutocompleteItem {
	var items []AutocompleteItem

	// 1. Embedded skills
	if defs, err := skilldef.ParseEmbedded(); err == nil {
		for _, def := range defs {
			var path string
			if skillsDir != "" {
				path = filepath.Join(skillsDir, def.Name, "SKILL.md")
			}
			items = append(items, AutocompleteItem{
				Name:        def.Name,
				Description: def.Description,
				Source:      "agentic",
				Path:        path,
			})
		}
	}

	// 2. Global commands
	if globalDir != "" {
		items = append(items, discoverGlobalCommandsFrom(globalDir)...)
	} else {
		items = append(items, discoverGlobalCommands()...)
	}

	// 3. Repo items
	if repoPath != "" {
		for _, si := range discoverRepoItems("", repoPath) {
			items = append(items, AutocompleteItem{
				Name:        si.Name,
				Description: si.Description,
				Source:      "repo",
				Path:        si.Path,
			})
		}
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].Name != items[j].Name {
			return items[i].Name < items[j].Name
		}
		return sourcePriority(items[i].Source) < sourcePriority(items[j].Source)
	})
	return items
}

// sourceDisplayLabel maps internal source values to user-facing labels.
func sourceDisplayLabel(source string) string {
	switch source {
	case "agentic":
		return "built-in"
	case "repo":
		return "repo"
	case "global":
		return "global"
	default:
		return source
	}
}

// sourcePriority returns a sort key for source values. Lower values sort first.
// Repo items (highest priority) appear first within same-name groups.
func sourcePriority(source string) int {
	switch source {
	case "repo":
		return 0
	case "global":
		return 1
	case "agentic":
		return 2
	default:
		return 3
	}
}

// discoverGlobalCommands scans ~/.claude/commands/ recursively for .md files.
func discoverGlobalCommands() []AutocompleteItem {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return discoverGlobalCommandsFrom(filepath.Join(home, ".claude", "commands"))
}

// discoverGlobalCommandsFrom scans the given directory recursively for .md files
// and returns them as AutocompleteItem values with Source "global". Exported for testing.
func discoverGlobalCommandsFrom(dir string) []AutocompleteItem {
	var items []AutocompleteItem
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".md") {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		name := strings.TrimSuffix(rel, ".md")
		name = strings.ReplaceAll(name, string(filepath.Separator), ":")

		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		fm := parseSkillFrontmatter(string(data))
		var desc string
		if fm != nil {
			desc = firstLine(fm["description"])
		}
		items = append(items, AutocompleteItem{
			Name:        name,
			Description: desc,
			Source:      "global",
			Path:        path,
		})
		return nil
	})
	return items
}
