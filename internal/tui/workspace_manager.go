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
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// workspaceRoot holds display info for a single workspace root.
type workspaceRoot struct {
	Path      string
	RepoCount int
	IsRepo    bool // true if the root itself is a git repo
}

// WorkspaceManagerModel manages the workspace roots overlay.
type WorkspaceManagerModel struct {
	roots  []workspaceRoot
	cursor int
	width  int
	height int

	// Add-root sub-flow
	picker       DirPickerModel
	pickerActive bool

	// Delete confirmation sub-state
	confirmDelete bool
	confirmPath   string

	// State signals consumed by app
	closed      bool
	addedRoot   string
	removedRoot string
}

// NewWorkspaceManagerModel creates a workspace manager overlay with the given roots.
func NewWorkspaceManagerModel(roots []workspaceRoot, width, height int) WorkspaceManagerModel {
	return WorkspaceManagerModel{
		roots:  roots,
		width:  width,
		height: height,
	}
}

// Init returns the initial command for the workspace manager (none needed).
func (m WorkspaceManagerModel) Init() tea.Cmd {
	return nil
}

// Update handles messages for the workspace manager overlay.
func (m WorkspaceManagerModel) Update(msg tea.Msg) (WorkspaceManagerModel, tea.Cmd) {
	// Handle window resize: store dimensions and forward to picker if active.
	if wsm, ok := msg.(tea.WindowSizeMsg); ok {
		m.width = wsm.Width
		m.height = wsm.Height
		if m.pickerActive {
			m.picker, _ = m.picker.Update(wsm)
		}
		return m, nil
	}

	keyMsg, ok := msg.(tea.KeyPressMsg)
	if !ok {
		// Forward non-key messages to picker if active
		if m.pickerActive {
			var cmd tea.Cmd
			m.picker, cmd = m.picker.Update(msg)
			if m.picker.IsDone() && !m.picker.IsCancelled() {
				m.addedRoot = m.picker.SelectedPath()
				m.pickerActive = false
			} else if m.picker.IsCancelled() {
				m.pickerActive = false
			}
			return m, cmd
		}
		return m, nil
	}

	// Picker active: delegate all input
	if m.pickerActive {
		var cmd tea.Cmd
		m.picker, cmd = m.picker.Update(keyMsg)
		if m.picker.IsDone() && !m.picker.IsCancelled() {
			m.addedRoot = m.picker.SelectedPath()
			m.pickerActive = false
		} else if m.picker.IsCancelled() {
			m.pickerActive = false
		}
		return m, cmd
	}

	// Confirm delete active
	if m.confirmDelete {
		switch keyMsg.String() {
		case "y", "Y":
			m.removedRoot = m.confirmPath
			// Remove from local roots list
			newRoots := make([]workspaceRoot, 0, len(m.roots))
			for _, r := range m.roots {
				if r.Path != m.confirmPath {
					newRoots = append(newRoots, r)
				}
			}
			m.roots = newRoots
			// Adjust cursor
			if m.cursor >= len(m.roots) && m.cursor > 0 {
				m.cursor = len(m.roots) - 1
			}
			m.confirmDelete = false
			m.confirmPath = ""
		default:
			m.confirmDelete = false
			m.confirmPath = ""
		}
		return m, nil
	}

	// Default state
	switch {
	case key.Matches(keyMsg, keys.Back):
		m.closed = true
	case keyMsg.String() == "a":
		m.picker = NewDirPickerModel()
		m.pickerActive = true
		cmds := []tea.Cmd{m.picker.Init()}
		// Apply already-known terminal size to the new picker (welcome flow pattern).
		if m.width > 0 && m.height > 0 {
			m.picker, _ = m.picker.Update(tea.WindowSizeMsg{
				Width:  m.width,
				Height: m.height,
			})
		}
		return m, tea.Batch(cmds...)
	case keyMsg.String() == "d":
		if len(m.roots) > 0 {
			m.confirmDelete = true
			m.confirmPath = m.roots[m.cursor].Path
		}
	case key.Matches(keyMsg, keys.Down):
		if m.cursor < len(m.roots)-1 {
			m.cursor++
		}
	case key.Matches(keyMsg, keys.Up):
		if m.cursor > 0 {
			m.cursor--
		}
	}

	return m, nil
}

// View renders the workspace manager overlay content.
func (m WorkspaceManagerModel) View() string {
	modalWidth := max(min(m.width-4, 54), 28)
	contentWidth := modalWidth - 4 // padding

	var content strings.Builder

	if len(m.roots) == 0 {
		content.WriteString("\n")
		content.WriteString("  No workspace roots configured.\n")
		content.WriteString("  Press 'a' to add a workspace root.\n")
		content.WriteString("\n")
	} else {
		content.WriteString("\n")
		for i, root := range m.roots {
			cursor := "  "
			if i == m.cursor {
				cursor = "> "
			}
			displayPath := compactHome(root.Path)
			var countStr string
			if root.IsRepo {
				countStr = "★ repo"
			} else {
				countStr = fmt.Sprintf("%d repos", root.RepoCount)
			}
			// Right-align the count
			padding := max(contentWidth-len(cursor)-len(displayPath)-len(countStr), 1)
			content.WriteString(cursor + displayPath + strings.Repeat(" ", padding) + countStr + "\n")
		}
		content.WriteString("\n")
	}

	// Delete confirmation
	if m.confirmDelete {
		confirmPath := compactHome(m.confirmPath)
		fmt.Fprintf(&content, "  Remove %s? [y]es  [n]o\n", confirmPath)
		content.WriteString("\n")
	}

	// Key hints footer
	var hints string
	if len(m.roots) > 0 {
		hints = "a add  •  d remove  •  esc close"
	} else {
		hints = "a add  •  esc close"
	}
	content.WriteString(KeyHelpStyle.Render(hints))

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorBrand).
		Padding(0, 1).
		Width(modalWidth).
		Render(content.String())

	box = renderBorderTitle(box, "Workspace Manager", TitleStyle)

	return box
}

// PickerView returns the picker overlay content when the picker is active.
func (m WorkspaceManagerModel) PickerView() string {
	if !m.pickerActive {
		return ""
	}
	return m.picker.ViewContent()
}

// IsPickerActive returns true when the directory picker sub-flow is active.
func (m WorkspaceManagerModel) IsPickerActive() bool {
	return m.pickerActive
}

// IsClosed returns true when the user dismissed the overlay.
func (m WorkspaceManagerModel) IsClosed() bool {
	return m.closed
}

// ConsumeAddedRoot returns and clears the pending added root path.
func (m *WorkspaceManagerModel) ConsumeAddedRoot() string {
	root := m.addedRoot
	m.addedRoot = ""
	return root
}

// ConsumeRemovedRoot returns and clears the pending removed root path.
func (m *WorkspaceManagerModel) ConsumeRemovedRoot() string {
	root := m.removedRoot
	m.removedRoot = ""
	return root
}

// SetRoots replaces the current root list and clamps the cursor.
func (m *WorkspaceManagerModel) SetRoots(roots []workspaceRoot) {
	m.roots = roots
	if m.cursor >= len(m.roots) {
		if len(m.roots) > 0 {
			m.cursor = len(m.roots) - 1
		} else {
			m.cursor = 0
		}
	}
}

// countGitReposInDir counts immediate child directories containing .git.
// Follows the same scanning pattern as scanGitRepos in dirpicker.go.
func countGitReposInDir(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	count := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if isGitRepo(filepath.Join(dir, e.Name())) {
			count++
		}
	}
	return count
}
