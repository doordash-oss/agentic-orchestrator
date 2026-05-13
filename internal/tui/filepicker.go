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

// FilePickerModel provides inline file path autocomplete with virtual repo
// roots. When multiple repos are available, it shows repo names as top-level
// entries. Selecting a repo drills into its actual filesystem. When only one
// repo exists, the repo-name level is skipped automatically.
//
// Characters typed by the user flow to the parent text input; the picker only
// intercepts navigation keys (Tab, Enter, Esc, Up, Down).
type FilePickerModel struct {
	active      bool
	prefix      string   // relative path typed so far after "@"
	matches     []string // relative paths matching prefix
	cursor      int
	maxDisplay  int
	repoRoots   map[string]string // repo name → absolute path
	repoNames   []string          // sorted repo names (derived from repoRoots keys)
	currentRepo string            // "" = at repo-name level; set = browsing inside this repo
}

func NewFilePickerModel(repoRoots map[string]string) FilePickerModel {
	names := make([]string, 0, len(repoRoots))
	for name := range repoRoots {
		names = append(names, name)
	}
	sort.Strings(names)
	return FilePickerModel{
		maxDisplay: 8,
		repoRoots:  repoRoots,
		repoNames:  names,
	}
}

// Activate starts the file picker with an initial prefix (typically "").
// If only one repo exists, it skips the repo-name level and descends directly.
func (m *FilePickerModel) Activate(prefix string) {
	m.active = true
	m.prefix = prefix
	m.cursor = 0
	m.currentRepo = ""

	// Single-repo optimization: skip repo-name level
	if len(m.repoNames) == 1 {
		m.currentRepo = m.repoNames[0]
		if prefix == "" {
			m.prefix = m.currentRepo + "/"
		}
	}

	m.updateMatches()
}

// Deactivate hides the file picker.
func (m *FilePickerModel) Deactivate() {
	m.active = false
	m.prefix = ""
	m.matches = nil
	m.cursor = 0
	m.currentRepo = ""
}

// IsActive returns whether the picker is visible.
func (m FilePickerModel) IsActive() bool {
	return m.active
}

// SelectedPath returns the currently highlighted path.
func (m FilePickerModel) SelectedPath() string {
	if m.cursor >= 0 && m.cursor < len(m.matches) {
		return m.matches[m.cursor]
	}
	return m.prefix
}

// SetPrefix updates the search prefix and refreshes matches.
// Called by the parent when the text input value changes.
func (m *FilePickerModel) SetPrefix(prefix string) {
	if prefix == m.prefix {
		return
	}
	m.prefix = prefix

	// Always resolve the longest matching repo for multi-repo setups.
	// This handles overlapping names like "rootA" vs "rootA/myrepo" — as the
	// user types incrementally from "rootA/" to "rootA/myrepo/", the current
	// repo upgrades from "rootA" to "rootA/myrepo". Backspacing re-resolves
	// back to the shorter repo.
	if len(m.repoNames) > 1 {
		best := ""
		for _, name := range m.repoNames {
			if strings.HasPrefix(prefix, name+"/") && len(name) > len(best) {
				best = name
			}
		}
		if best != "" {
			m.currentRepo = best
		} else {
			// No repo prefix matches — back at repo-name level
			m.currentRepo = ""
		}
	}

	m.cursor = 0
	m.updateMatches()
}

// UpdateRepoRoots replaces the repo root map and re-derives sorted names.
// If the picker is currently active, it resets to the repo-name level.
func (m *FilePickerModel) UpdateRepoRoots(repoRoots map[string]string) {
	m.repoRoots = repoRoots
	names := make([]string, 0, len(repoRoots))
	for name := range repoRoots {
		names = append(names, name)
	}
	sort.Strings(names)
	m.repoNames = names
	if m.active {
		m.currentRepo = ""
		m.prefix = ""
		m.cursor = 0
		m.updateMatches()
	}
}

// Update handles navigation keys while the picker is active.
// Regular character input is NOT consumed — it flows to the text input.
// Returns (updated model, selected path or "", whether event was consumed).
func (m FilePickerModel) Update(msg tea.KeyPressMsg) (FilePickerModel, string, bool) {
	switch {
	case key.Matches(msg, key.NewBinding(key.WithKeys("tab"))):
		selected := m.SelectedPath()
		if strings.HasSuffix(selected, "/") {
			// At repo-name level: drill into the repo
			if m.currentRepo == "" {
				repoName := strings.TrimSuffix(selected, "/")
				m.currentRepo = repoName
			}
			// Directory or repo: drill in — update prefix, keep picker active
			m.prefix = selected
			m.cursor = 0
			m.updateMatches()
			return m, selected, true
		}
		// File: complete selection
		m.Deactivate()
		return m, selected, true

	case key.Matches(msg, key.NewBinding(key.WithKeys("enter"))):
		selected := m.SelectedPath()
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
func (m FilePickerModel) View() string {
	if !m.active || len(m.matches) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString(MutedStyle.Render("  File completions:") + "\n")

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
		display := m.matches[i]
		if len(display) > 60 {
			display = "..." + display[len(display)-57:]
		}
		b.WriteString("  " + cursor + display + "\n")
	}

	if len(m.matches) > m.maxDisplay {
		b.WriteString(MutedStyle.Render(fmt.Sprintf("  (%d of %d)", end-start, len(m.matches))) + "\n")
	}

	return b.String()
}

// updateMatches populates m.matches based on current state.
// At the repo-name level (currentRepo == ""), it lists matching repo names.
// Inside a repo, it scans the filesystem using the repo's root path.
func (m *FilePickerModel) updateMatches() {
	m.matches = nil

	if m.currentRepo == "" {
		// Repo-name level: list repo names matching prefix filter
		m.updateRepoNameMatches()
		return
	}

	// Within-repo level: filesystem scanning
	m.updateFileSystemMatches()
}

// updateRepoNameMatches lists repo names (suffixed with "/") that match the prefix.
func (m *FilePickerModel) updateRepoNameMatches() {
	filter := strings.ToLower(m.prefix)
	for _, name := range m.repoNames {
		if filter == "" || strings.HasPrefix(strings.ToLower(name), filter) {
			m.matches = append(m.matches, name+"/")
		}
	}
	// Already sorted since repoNames is sorted
	if len(m.matches) > 50 {
		m.matches = m.matches[:50]
	}
}

// updateFileSystemMatches scans the filesystem within the current repo.
func (m *FilePickerModel) updateFileSystemMatches() {
	baseDir := m.repoRoots[m.currentRepo]
	repoPrefix := m.currentRepo + "/"

	// Strip the repo prefix from the user's prefix to get the sub-path
	subPrefix := strings.TrimPrefix(m.prefix, repoPrefix)

	dir := subPrefix
	filter := ""

	if dir == "" {
		dir = "."
	} else {
		absPath := filepath.Join(baseDir, dir)
		info, err := os.Stat(absPath)
		if err != nil || !info.IsDir() {
			filter = strings.ToLower(filepath.Base(dir))
			dir = filepath.Dir(dir)
			if dir == "." && !strings.Contains(subPrefix, "/") {
				dir = "."
			}
		}
	}

	absDir := filepath.Join(baseDir, dir)
	entries, err := os.ReadDir(absDir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if filter != "" && !strings.HasPrefix(strings.ToLower(name), filter) {
			continue
		}
		relPath := filepath.Join(dir, name)
		relPath = strings.TrimPrefix(relPath, "./")
		if entry.IsDir() {
			relPath += "/"
		}
		// Prepend repo name for fully qualified paths
		m.matches = append(m.matches, repoPrefix+relPath)
	}

	sort.Strings(m.matches)

	if len(m.matches) > 50 {
		m.matches = m.matches[:50]
	}
}
