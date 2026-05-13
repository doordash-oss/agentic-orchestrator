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
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// gitRepoScanMsg is sent when a directory's children have been scanned for .git dirs.
type gitRepoScanMsg struct {
	dir           string
	count         int
	repoDirs      map[string]bool // child dir names that contain .git
	dirRepoCounts map[string]int  // child dir name → number of git repos inside it (for non-repo dirs)
}

// dirScanTimeoutMsg fires after a threshold to show scanning indicator for slow scans.
type dirScanTimeoutMsg struct {
	dir string
}

// scanTimeout is the threshold after which a scanning indicator is shown.
const scanTimeout = 100 * time.Millisecond

// column represents one vertical panel in the Miller columns view.
type column struct {
	path          string          // absolute directory this column represents
	entries       []string        // sorted child directory names only
	cursor        int             // index of highlighted entry
	scroll        int             // first visible index for scrolling
	repoDirs      map[string]bool // child name → is git repo
	dirRepoCounts map[string]int  // child name → count of git repos inside
	scanDone      bool            // git scan completed for this column
}

// highlightedPath returns the full path of the highlighted entry, or "" if empty.
func (c *column) highlightedPath() string {
	if len(c.entries) == 0 {
		return ""
	}
	return filepath.Join(c.path, c.entries[c.cursor])
}

// highlightedName returns the name of the highlighted entry, or "" if empty.
func (c *column) highlightedName() string {
	if len(c.entries) == 0 {
		return ""
	}
	return c.entries[c.cursor]
}

// ensureVisible adjusts scroll so the cursor is within the visible window.
func (c *column) ensureVisible(visibleHeight int) {
	if visibleHeight <= 0 {
		return
	}
	if c.cursor < c.scroll {
		c.scroll = c.cursor
	}
	if c.cursor >= c.scroll+visibleHeight {
		c.scroll = c.cursor - visibleHeight + 1
	}
}

type dirPickerMode int

const (
	dirPickerModeBrowse     dirPickerMode = iota // existing behavior: select a workspace root
	dirPickerModeCreateRepo                      // select a parent directory for new repo
)

// DirPickerModel provides a Miller columns filesystem directory browser.
type DirPickerModel struct {
	columns    []column // path ancestry columns; last one is the "active" column
	previewCol *column  // optional rightmost column showing children of highlighted entry

	selected  string
	cancelled bool
	done      bool
	width     int
	height    int

	showHidden bool

	// Public-facing git repo info for the current directory (backward compat)
	gitRepoCount  int
	gitRepoDirs   map[string]bool
	dirRepoCounts map[string]int

	// Empty root confirmation
	confirmEmpty     bool
	confirmSelection string

	mode dirPickerMode
}

// isGitRepo checks whether a directory is a git repository.
// It handles both regular repos (.git is a directory) and worktrees (.git is a file).
func isGitRepo(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

// readDirEntries reads a directory and returns sorted child directory names only.
func readDirEntries(path string, showHidden bool) []string {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil
	}
	var dirs []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if !showHidden && strings.HasPrefix(name, ".") {
			continue
		}
		dirs = append(dirs, name)
	}
	sort.Strings(dirs)
	return dirs
}

// makeColumn creates a column for the given directory path.
func makeColumn(path string, showHidden bool) column {
	return column{
		path:          path,
		entries:       readDirEntries(path, showHidden),
		repoDirs:      make(map[string]bool),
		dirRepoCounts: make(map[string]int),
	}
}

// NewDirPickerModel creates a new Miller columns directory picker starting from $HOME.
func NewDirPickerModel() DirPickerModel {
	return NewDirPickerModelWithMode(dirPickerModeBrowse)
}

// NewDirPickerModelWithMode creates a new Miller columns directory picker with the given mode.
func NewDirPickerModelWithMode(mode dirPickerMode) DirPickerModel {
	home, _ := os.UserHomeDir()
	if home == "" {
		home = "/"
	}

	rootCol := makeColumn(home, false)

	m := DirPickerModel{
		columns:     []column{rootCol},
		gitRepoDirs: make(map[string]bool),
		mode:        mode,
	}

	// Build preview for highlighted entry
	m.rebuildPreview()
	return m
}

// rebuildPreview creates the preview column from the active column's highlighted entry.
func (m *DirPickerModel) rebuildPreview() {
	active := &m.columns[len(m.columns)-1]
	if hp := active.highlightedPath(); hp != "" {
		col := makeColumn(hp, m.showHidden)
		m.previewCol = &col
	} else {
		m.previewCol = nil
	}
}

// currentDir returns the active column's path (the directory that space selects).
func (m DirPickerModel) currentDir() string {
	return m.columns[len(m.columns)-1].path
}

// updatePublicGitInfo syncs the public gitRepoCount/gitRepoDirs fields from the active column.
func (m *DirPickerModel) updatePublicGitInfo() {
	active := &m.columns[len(m.columns)-1]
	if active.scanDone {
		m.gitRepoCount = len(active.repoDirs)
		m.gitRepoDirs = active.repoDirs
		m.dirRepoCounts = active.dirRepoCounts
	} else {
		m.gitRepoCount = 0
		m.gitRepoDirs = make(map[string]bool)
		m.dirRepoCounts = make(map[string]int)
	}
}

// Init initializes the directory picker and fires git scans for visible columns.
func (m DirPickerModel) Init() tea.Cmd {
	var cmds []tea.Cmd
	for i := range m.columns {
		cmds = append(cmds, scanGitReposCmd(m.columns[i].path))
	}
	if m.previewCol != nil {
		cmds = append(cmds, scanGitReposCmd(m.previewCol.path))
	}
	return tea.Batch(cmds...)
}

// scanGitRepos synchronously scans a directory for immediate children containing .git.
// For non-repo child directories, it also counts git repos one level deeper.
func scanGitRepos(dir string) gitRepoScanMsg {
	result := gitRepoScanMsg{
		dir:           dir,
		repoDirs:      make(map[string]bool),
		dirRepoCounts: make(map[string]int),
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return result
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		childPath := filepath.Join(dir, e.Name())
		if isGitRepo(childPath) {
			result.repoDirs[e.Name()] = true
			result.count++
		} else {
			subEntries, err := os.ReadDir(childPath)
			if err != nil {
				continue
			}
			subCount := 0
			for _, se := range subEntries {
				if se.IsDir() && isGitRepo(filepath.Join(childPath, se.Name())) {
					subCount++
				}
			}
			if subCount > 0 {
				result.dirRepoCounts[e.Name()] = subCount
			}
		}
	}
	return result
}

// scanGitReposCmd returns a tea.Cmd that scans for git repos in dir.
func scanGitReposCmd(dir string) tea.Cmd {
	return func() tea.Msg {
		return scanGitRepos(dir)
	}
}

// scanTimeoutCmd returns a tea.Cmd that fires after the scan threshold.
func scanTimeoutCmd(dir string) tea.Cmd {
	return tea.Tick(scanTimeout, func(time.Time) tea.Msg {
		return dirScanTimeoutMsg{dir: dir}
	})
}

// Update handles messages for the directory picker.
func (m DirPickerModel) Update(msg tea.Msg) (DirPickerModel, tea.Cmd) {
	if m.done {
		return m, nil
	}

	switch msg := msg.(type) {
	case gitRepoScanMsg:
		// Match scan result to any visible column or preview
		for i := range m.columns {
			if m.columns[i].path == msg.dir {
				m.columns[i].repoDirs = msg.repoDirs
				m.columns[i].dirRepoCounts = msg.dirRepoCounts
				m.columns[i].scanDone = true
			}
		}
		if m.previewCol != nil && m.previewCol.path == msg.dir {
			m.previewCol.repoDirs = msg.repoDirs
			m.previewCol.dirRepoCounts = msg.dirRepoCounts
			m.previewCol.scanDone = true
		}
		m.updatePublicGitInfo()
		return m, nil

	case dirScanTimeoutMsg:
		// No-op for Miller columns (scanning indicator not used)
		return m, nil

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyPressMsg:
		// Handle confirmation state first
		if m.confirmEmpty {
			switch {
			case key.Matches(msg, key.NewBinding(key.WithKeys("y", "enter"))):
				m.selected = m.confirmSelection
				m.done = true
				m.confirmEmpty = false
				return m, nil
			case key.Matches(msg, key.NewBinding(key.WithKeys("n", "esc"))):
				m.confirmEmpty = false
				m.confirmSelection = ""
				return m, nil
			}
			return m, nil
		}

		switch {
		case key.Matches(msg, key.NewBinding(key.WithKeys("up", "k"))):
			return m.moveCursor(-1)

		case key.Matches(msg, key.NewBinding(key.WithKeys("down", "j"))):
			return m.moveCursor(1)

		case key.Matches(msg, key.NewBinding(key.WithKeys("right", "l"))):
			return m.enterHighlighted()

		case key.Matches(msg, key.NewBinding(key.WithKeys("left", "h", "backspace"))):
			return m.goBack()

		case key.Matches(msg, key.NewBinding(key.WithKeys("enter", "space"))):
			return m.selectHighlighted()

		case key.Matches(msg, key.NewBinding(key.WithKeys("esc"))):
			m.cancelled = true
			m.done = true
			return m, nil

		case key.Matches(msg, key.NewBinding(key.WithKeys("."))):
			return m.toggleHidden()
		}
	}

	return m, nil
}

// moveCursor moves the cursor in the active column by delta (-1 or +1).
func (m DirPickerModel) moveCursor(delta int) (DirPickerModel, tea.Cmd) {
	active := &m.columns[len(m.columns)-1]
	if len(active.entries) == 0 {
		return m, nil
	}
	active.cursor += delta
	if active.cursor < 0 {
		active.cursor = 0
	}
	if active.cursor >= len(active.entries) {
		active.cursor = len(active.entries) - 1
	}
	// Rebuild preview for the new highlighted entry
	m.rebuildPreview()
	var cmd tea.Cmd
	if m.previewCol != nil {
		cmd = scanGitReposCmd(m.previewCol.path)
	}
	return m, cmd
}

// enterHighlighted navigates into the highlighted directory.
func (m DirPickerModel) enterHighlighted() (DirPickerModel, tea.Cmd) {
	active := &m.columns[len(m.columns)-1]
	hp := active.highlightedPath()
	if hp == "" {
		return m, nil
	}

	// Create new column for the highlighted directory
	newCol := makeColumn(hp, m.showHidden)

	// If we have preview data for this path, reuse it
	if m.previewCol != nil && m.previewCol.path == hp {
		newCol = *m.previewCol
	}

	m.columns = append(m.columns, newCol)
	m.rebuildPreview()
	m.updatePublicGitInfo()

	// Fire scans for the new column and preview
	var cmds []tea.Cmd
	if !newCol.scanDone {
		cmds = append(cmds, scanGitReposCmd(newCol.path))
	}
	if m.previewCol != nil {
		cmds = append(cmds, scanGitReposCmd(m.previewCol.path))
	}
	return m, tea.Batch(cmds...)
}

// goBack navigates to the parent directory.
func (m DirPickerModel) goBack() (DirPickerModel, tea.Cmd) {
	if len(m.columns) > 1 {
		// Pop the last column
		m.columns = m.columns[:len(m.columns)-1]
		m.rebuildPreview()
		m.updatePublicGitInfo()
		var cmd tea.Cmd
		if m.previewCol != nil && !m.previewCol.scanDone {
			cmd = scanGitReposCmd(m.previewCol.path)
		}
		return m, cmd
	}

	// At the first column — try to go to parent
	currentPath := m.columns[0].path
	parent := filepath.Dir(currentPath)
	if parent == currentPath {
		// At filesystem root, can't go higher
		return m, nil
	}

	// Create parent column, set cursor to point at the old directory
	parentCol := makeColumn(parent, m.showHidden)
	oldName := filepath.Base(currentPath)
	for i, name := range parentCol.entries {
		if name == oldName {
			parentCol.cursor = i
			break
		}
	}

	// Old first column becomes an ancestry column; prepend parent
	m.columns = append([]column{parentCol}, m.columns...)
	// Pop the rightmost so active stays the same depth
	// Actually: we want to go back, so activeCol should be the parent
	// Keep only the parent column
	m.columns = m.columns[:1]
	m.rebuildPreview()
	m.updatePublicGitInfo()

	cmds := []tea.Cmd{scanGitReposCmd(parentCol.path)}
	if m.previewCol != nil {
		cmds = append(cmds, scanGitReposCmd(m.previewCol.path))
	}
	return m, tea.Batch(cmds...)
}

// selectHighlighted selects the highlighted directory in the active column.
func (m DirPickerModel) selectHighlighted() (DirPickerModel, tea.Cmd) {
	active := &m.columns[len(m.columns)-1]
	path := active.highlightedPath()
	if path == "" {
		return m, nil
	}

	// In create-repo mode, allow selecting any directory without git repo checks.
	if m.mode == dirPickerModeCreateRepo {
		m.selected = path
		m.done = true
		return m, nil
	}

	// Determine repo count for the selected directory.
	// A directory can be valid as: (a) itself a git repo, (b) containing git repos.
	repoCount := 0
	isRepo := active.scanDone && active.repoDirs[active.highlightedName()]

	if isRepo {
		// The highlighted entry is itself a git repo — always valid (count as 1)
		repoCount = 1
	} else if m.previewCol != nil && m.previewCol.path == path && m.previewCol.scanDone {
		// Use preview column's scan data for child repo count
		repoCount = len(m.previewCol.repoDirs)
	} else if active.scanDone {
		if count, ok := active.dirRepoCounts[active.highlightedName()]; ok {
			repoCount = count
		}
	} else {
		return m, nil // block selection while scanning
	}

	// Update the public-facing count to reflect the selected entry
	m.gitRepoCount = repoCount

	if repoCount == 0 {
		m.confirmEmpty = true
		m.confirmSelection = path
		return m, nil
	}
	m.selected = path
	m.done = true
	return m, nil
}

// toggleHidden toggles hidden directory visibility and rebuilds all columns.
func (m DirPickerModel) toggleHidden() (DirPickerModel, tea.Cmd) {
	m.showHidden = !m.showHidden

	var cmds []tea.Cmd
	// Rebuild each column's entries while preserving cursor position
	for i := range m.columns {
		col := &m.columns[i]
		oldHighlight := col.highlightedName()
		col.entries = readDirEntries(col.path, m.showHidden)
		col.cursor = 0
		col.scroll = 0
		// Try to restore cursor to the same entry
		for j, name := range col.entries {
			if name == oldHighlight {
				col.cursor = j
				break
			}
		}
		col.scanDone = false
		cmds = append(cmds, scanGitReposCmd(col.path))
	}
	m.rebuildPreview()
	if m.previewCol != nil {
		cmds = append(cmds, scanGitReposCmd(m.previewCol.path))
	}
	return m, tea.Batch(cmds...)
}

// viewContent renders the picker box + hints without centering.
// Used by ViewContent() for overlay stacking and by View() for full-screen display.
func (m DirPickerModel) viewContent() string {
	// Determine visible columns (up to 3: ancestor?, active, preview)
	var visCols []column
	activeIdx := len(m.columns) - 1
	startIdx := activeIdx - 1
	if startIdx < 0 {
		startIdx = 0
	}
	for i := startIdx; i <= activeIdx; i++ {
		visCols = append(visCols, m.columns[i])
	}
	// The active column's index within visCols
	activeVisIdx := len(visCols) - 1

	// Compute layout dimensions
	colCount := len(visCols)
	if m.previewCol != nil {
		colCount++
	}
	if colCount == 0 {
		colCount = 1
	}

	// Box uses 4/5 of terminal width
	outerWidth := m.width * 4 / 5
	if outerWidth < 50 {
		outerWidth = 50
	}
	if m.width > 0 && outerWidth > m.width-4 {
		outerWidth = m.width - 4
	}

	// Column inner width (account for outer box border+padding: ~6 chars, and column borders: ~4 per col)
	innerWidth := outerWidth - 6
	colWidth := innerWidth / colCount
	if colWidth < 15 {
		colWidth = 15
	}

	// Visible height for entries — use 2/3 of terminal height minus chrome
	// (outer box border+padding: 4, breadcrumb: 2, hint+help below: 4)
	colHeight := m.height*2/3 - 10
	if colHeight < 5 {
		colHeight = 5
	}

	// Render each column panel
	var panels []string
	for i, col := range visCols {
		isActive := i == activeVisIdx
		panels = append(panels, m.renderColumn(&col, colWidth, colHeight, isActive))
	}
	if m.previewCol != nil {
		panels = append(panels, m.renderColumn(m.previewCol, colWidth, colHeight, false))
	}

	columnsView := lipgloss.JoinHorizontal(lipgloss.Top, panels...)

	// Build full content
	var b strings.Builder

	// Current location breadcrumb
	labelStyle := lipgloss.NewStyle().Faint(true)
	pathStyle := lipgloss.NewStyle().Foreground(colorBrand)
	b.WriteString(labelStyle.Render("Current dir: "))
	b.WriteString(pathStyle.Render(compactHome(m.currentDir())))
	b.WriteString("\n\n")
	b.WriteString(columnsView)

	// Outer box
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorBrand).
		Padding(1, 1)
	box := boxStyle.Render(b.String())

	// Hint and help below the box
	hintStyle := lipgloss.NewStyle().Foreground(colorInfo).Italic(true)
	hint := hintStyle.Render("💡 Select the parent folder that contains your git repositories")

	helpStyle := lipgloss.NewStyle().Faint(true)
	help := helpStyle.Render("↑↓ navigate • →/l open dir • ←/h go back • enter/space select highlighted • . hidden • esc cancel")

	return box + "\n\n" + hint + "\n\n" + help
}

// View renders the picker centered in the full terminal (for standalone use).
func (m DirPickerModel) View() string {
	if m.confirmEmpty {
		return m.viewConfirmation()
	}
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, m.viewContent())
}

// ViewContent renders the picker without centering (for overlay stacking).
func (m DirPickerModel) ViewContent() string {
	if m.confirmEmpty {
		return m.viewConfirmation()
	}
	return m.viewContent()
}

// renderColumn renders a single column panel.
func (m DirPickerModel) renderColumn(col *column, width, height int, active bool) string {
	// Column title — basename of the directory (or ~ for home)
	title := filepath.Base(col.path)
	home, _ := os.UserHomeDir()
	if col.path == home {
		title = "~"
	}

	// Ensure scroll/cursor bounds
	col.ensureVisible(height)

	// Render entries
	var lines []string
	end := col.scroll + height
	if end > len(col.entries) {
		end = len(col.entries)
	}

	cursorStyle := lipgloss.NewStyle().Bold(true).Foreground(colorBrand)
	normalStyle := lipgloss.NewStyle()
	repoStyle := lipgloss.NewStyle().Foreground(colorSuccess).Faint(true)
	countStyle := lipgloss.NewStyle().Foreground(colorInfo).Faint(true)

	for i := col.scroll; i < end; i++ {
		name := col.entries[i]

		// Cursor indicator
		prefix := "  "
		style := normalStyle
		if i == col.cursor && active {
			prefix = "> "
			style = cursorStyle
		}

		// Git annotation
		annotation := ""
		if col.scanDone {
			if col.repoDirs[name] {
				annotation = " " + repoStyle.Render("★")
			} else if count, ok := col.dirRepoCounts[name]; ok {
				label := fmt.Sprintf("%d", count)
				annotation = " " + countStyle.Render(label)
			}
		}

		// Truncate name if needed
		maxNameWidth := width - 6 - lipgloss.Width(annotation) // 6 = prefix(2) + padding(4)
		if maxNameWidth < 4 {
			maxNameWidth = 4
		}
		displayName := name
		if len(displayName) > maxNameWidth {
			displayName = displayName[:maxNameWidth-1] + "…"
		}

		line := prefix + style.Render(displayName) + annotation
		lines = append(lines, line)
	}

	// Fill remaining height with empty lines
	for len(lines) < height {
		lines = append(lines, "")
	}

	content := strings.Join(lines, "\n")

	// Apply panel style
	style := panelStyle(active).Width(width).Height(height)
	rendered := style.Render(content)

	// Add title to border
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(colorBrand)
	if !active {
		titleStyle = lipgloss.NewStyle().Faint(true)
	}
	return renderBorderTitle(rendered, title, titleStyle)
}

// viewConfirmation renders the "no git repos found" confirmation prompt.
func (m DirPickerModel) viewConfirmation() string {
	var b strings.Builder
	warnStyle := lipgloss.NewStyle().Bold(true).Foreground(colorWarning)
	helpStyle := lipgloss.NewStyle().Faint(true)

	b.WriteString(warnStyle.Render("No git repos found in this directory."))
	b.WriteString("\n\n")
	b.WriteString(fmt.Sprintf("Select %q anyway?", compactHome(m.confirmSelection)))

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorWarning).
		Padding(1, 3)
	box := boxStyle.Render(b.String())

	help := helpStyle.Render("y/enter confirm • n/esc cancel")
	content := box + "\n" + help

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, content)
}

// compactHome replaces $HOME prefix with ~.
func compactHome(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	if strings.HasPrefix(path, home+string(os.PathSeparator)) {
		return "~" + path[len(home):]
	}
	return path
}

// setCurrentDir replaces the active column with one rooted at dir (for tests).
func (m *DirPickerModel) setCurrentDir(dir string) {
	col := makeColumn(dir, m.showHidden)
	m.columns = []column{col}
	m.rebuildPreview()
}

// Mode returns the picker's current mode.
func (m DirPickerModel) Mode() dirPickerMode { return m.mode }

// SelectedPath returns the absolute path of the selected directory.
func (m DirPickerModel) SelectedPath() string { return m.selected }

// IsDone returns true when the picker has completed (selection or cancellation).
func (m DirPickerModel) IsDone() bool { return m.done }

// IsCancelled returns true when the user cancelled the picker.
func (m DirPickerModel) IsCancelled() bool { return m.cancelled }
