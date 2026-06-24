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
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// configTab identifies one of the three segmented tabs in the overlay.
type configTab int

const (
	tabModels configTab = iota
	tabBehavior
	tabGates
)

// EditConfigModel is the modal overlay opened by the `e` keybinding. Owns
// the shared ConfigEditorModel plus modal-specific state: the active
// segmented tab, which feature is being edited, save-in-flight flag, last
// save error for the banner, and the one-shot discard-confirm prompt
// state.
type EditConfigModel struct {
	featureID   string
	featureName string
	repos       []string
	pipeline    feature.PipelineProfile
	publishable bool
	editor      ConfigEditorModel
	activeTab   configTab
	saving      bool
	saveErr     string
	// discardConfirm is true after esc-with-changes and before y/n resolution.
	discardConfirm bool
}

// NewEditConfigModel constructs the overlay for the given feature. The
// caller builds the catalog via BuildPhaseModelCatalog at modal-open time;
// provisionalPublishable is derived from f.IsPublishable().
//
// Re-entrancy + crash recovery: constructor only, no persisted state. Safe
// to call repeatedly; state lives in memory until save dispatches through
// AppModel's saveConfigCmd.
func NewEditConfigModel(f *feature.Feature, cat PhaseModelCatalog, provisionalPublishable bool) EditConfigModel {
	repos := make([]string, 0, len(f.Repos))
	for _, repo := range f.Repos {
		repos = append(repos, repo.Name)
	}
	return EditConfigModel{
		featureID:   f.ID,
		featureName: f.Name,
		repos:       repos,
		pipeline:    f.Pipeline,
		publishable: provisionalPublishable,
		editor:      NewConfigEditorModel(f, cat, provisionalPublishable),
		activeTab:   tabModels,
	}
}

// Update owns the modal-level keyboard grammar for the segmented-tab
// layout:
//
//   - tab / shift+tab cycle the active tab (Models → Behavior → Gates).
//   - ↑/↓/j/k walk rows and wrap within the active tab only.
//   - All other keys (←/→/space) delegate to the embedded editor, which
//     still owns per-row value cycling and toggles.
//
// AppModel.Update owns enter/esc/save dispatch on top of this — those keys
// are short-circuited before reaching here.
func (m EditConfigModel) Update(msg tea.Msg) (EditConfigModel, tea.Cmd) {
	if m.activeTab == tabModels && m.editor.ModelFilteringActive() {
		var cmd tea.Cmd
		m.editor, cmd = m.editor.Update(msg)
		return m, cmd
	}

	if keyMsg, ok := msg.(tea.KeyPressMsg); ok {
		switch keyMsg.String() {
		case "tab":
			m.cycleActiveTab(+1)
			return m, nil
		case "shift+tab":
			m.cycleActiveTab(-1)
			return m, nil
		case "up", "k":
			lo, hi := m.tabRowRange()
			m.editor.rowCursor = wrapInRange(m.editor.rowCursor-1, lo, hi)
			return m, nil
		case "down", "j":
			lo, hi := m.tabRowRange()
			m.editor.rowCursor = wrapInRange(m.editor.rowCursor+1, lo, hi)
			return m, nil
		}
	}

	// Keep the editor's cursor inside the active tab's row range before
	// delegating. This prevents a stale cursor from a prior tab from
	// letting keys like space/←/→ operate on a hidden row.
	m.clampToActiveTab()

	var cmd tea.Cmd
	m.editor, cmd = m.editor.Update(msg)
	return m, cmd
}

// View renders the modal: title + diff summary + tab strip + active-tab
// body + save status banner + key hints / discard-confirm prompt.
func (m EditConfigModel) View() string {
	var b strings.Builder

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")).Background(lipgloss.Color("62")).Padding(0, 1)
	diffStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Italic(true)

	b.WriteString(titleStyle.Render(fmt.Sprintf(" Edit Config · %s ", m.featureName)))
	b.WriteString("\n")
	b.WriteString(diffStyle.Render(m.diffSummary()))
	b.WriteString("\n\n")

	b.WriteString(m.renderTabStrip())
	b.WriteString("\n\n")

	switch m.activeTab {
	case tabModels:
		b.WriteString(m.editor.renderModelsBox(0))
	case tabBehavior:
		b.WriteString(m.renderBehaviorPane())
	case tabGates:
		b.WriteString(m.renderGatesPane())
	}
	b.WriteString("\n")

	if m.saveErr != "" {
		errStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("9"))
		b.WriteString("\n")
		b.WriteString(errStyle.Render("Save failed: " + m.saveErr))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(m.renderHintBar())

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		Padding(1, 2).
		BorderForeground(lipgloss.Color("62"))
	return boxStyle.Render(b.String())
}

// renderTabStrip renders the three segmented tab chips. The active tab is
// a solid pill; inactive tabs are muted. A small ● dot flags tabs with
// unsaved changes so the user can see at a glance where edits live.
func (m EditConfigModel) renderTabStrip() string {
	activePill := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")).Background(lipgloss.Color("62")).Padding(0, 2)
	idlePill := lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Padding(0, 2)
	dirtyDot := lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Render("●")

	tabs := []struct {
		id    configTab
		label string
		dirty bool
	}{
		{tabModels, "Models", m.editor.ModelsChangeCount() > 0},
		{tabBehavior, "Behavior", m.editor.InquirenessChanged()},
		{tabGates, "Gates", m.editor.CheckpointsChangeCount() > 0},
	}

	parts := make([]string, 0, len(tabs))
	for _, t := range tabs {
		label := t.label
		if t.dirty {
			label = t.label + " " + dirtyDot
		}
		if t.id == m.activeTab {
			parts = append(parts, activePill.Render(label))
		} else {
			parts = append(parts, idlePill.Render(label))
		}
	}
	return strings.Join(parts, "  ")
}

// renderBehaviorPane renders the single-setting Inquireness pane. Unlike
// the flat-walk layout, we drop the redundant "Current:" echo and the
// "Options" label — the highlighted pill already answers both. The
// description below updates live as the pill changes.
func (m EditConfigModel) renderBehaviorPane() string {
	header := lipgloss.NewStyle().Bold(true).Render("Inquireness")

	pills := []feature.Inquireness{
		feature.InquirenessNone,
		feature.InquirenessMedium,
		feature.InquirenessHigh,
	}
	activeStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")).Background(lipgloss.Color("62")).Padding(0, 1)
	idleStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Padding(0, 1)

	rendered := make([]string, 0, len(pills))
	for _, p := range pills {
		label := string(p)
		if p == m.editor.inquireness {
			rendered = append(rendered, activeStyle.Render(label))
		} else {
			rendered = append(rendered, idleStyle.Render(label))
		}
	}

	lines := []string{
		header,
		"",
		"  " + strings.Join(rendered, "  "),
	}
	if desc := inquirenessDescription(m.editor.inquireness); desc != "" {
		lines = append(lines, "", MutedStyle.Render(desc))
	}
	return strings.Join(lines, "\n")
}

// renderGatesPane renders the Gates checkbox list. The per-row description
// column is dropped — gate names self-explain — and the focused row's
// description surfaces as a single contextual help line below the list.
func (m EditConfigModel) renderGatesPane() string {
	title := lipgloss.NewStyle().Bold(true).Render("Gates")
	lines := []string{title, ""}

	total := m.editor.visibleCheckpointCount()
	fields := m.editor.visibleCheckpointFields()
	onGates := m.editor.rowCategory() == rowCatCheckpoints
	focusedIdx := -1
	if onGates {
		focusedIdx = m.editor.checkpointIndexForRow(m.editor.rowCursor)
	}

	for i := range total {
		cp := fields[i]
		var box string
		if m.editor.checkpointValue(cp.Gate) {
			box = SuccessStyle.Render("[x]")
		} else {
			box = MutedStyle.Render("[ ]")
		}
		prefix := "  "
		label := cp.Label
		rendered := fmt.Sprintf("%s  %s", box, label)
		if i == focusedIdx {
			prefix = SelectedRowStyle.Render("▸ ")
			rendered = fmt.Sprintf("%s  %s", box, SelectedRowStyle.Render(label))
		}
		lines = append(lines, prefix+rendered)
	}

	if focusedIdx >= 0 && focusedIdx < total {
		lines = append(lines, "", MutedStyle.Render(fields[focusedIdx].Desc))
	}
	return strings.Join(lines, "\n")
}

// renderHintBar renders the bottom key-hint line. Hints are tab-scoped:
// each tab shows only the keys that do something useful on it.
func (m EditConfigModel) renderHintBar() string {
	hintStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	warnStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("11"))

	switch {
	case m.discardConfirm:
		return warnStyle.Render("Discard unsaved changes? [y/n]")
	case m.saving:
		return hintStyle.Render("saving…")
	}

	var keys string
	switch m.activeTab {
	case tabModels:
		keys = "↑↓ phase   tab agent/model   ←→ choose   / filter   enter save   esc cancel"
	case tabBehavior:
		keys = "←→ choose   tab next section   enter save   esc cancel"
	case tabGates:
		keys = "↑↓ row   space toggle   tab next section   enter save   esc cancel"
	}
	return hintStyle.Render(keys)
}

// diffSummary builds the per-axis header summary string. When there are
// no changes it reads "No changes"; otherwise all three fragments always
// render (zero-count fragments still print). The string is intentionally
// stable — AppModel's save/test paths treat it as part of the header.
func (m EditConfigModel) diffSummary() string {
	if !m.editor.HasChanges() {
		return "No changes"
	}
	models := m.editor.ModelsChangeCount()
	gates := m.editor.CheckpointsChangeCount()
	inq := "unchanged"
	if m.editor.InquirenessChanged() {
		inq = "changed"
	}
	return fmt.Sprintf("Models: %s · Gates: %s · Inquiry: %s",
		pluralChanges(models), pluralChanges(gates), inq)
}

func pluralChanges(n int) string {
	if n == 1 {
		return "1 change"
	}
	return fmt.Sprintf("%d changes", n)
}

// cycleActiveTab advances the active tab by delta and snaps the editor's
// row cursor to the first row of the new tab, so per-row operations
// always target a visible row.
func (m *EditConfigModel) cycleActiveTab(delta int) {
	order := []configTab{tabModels, tabBehavior, tabGates}
	idx := int(m.activeTab)
	n := len(order)
	next := (idx + delta%n + n) % n
	m.activeTab = order[next]
	lo, _ := m.tabRowRange()
	m.editor.rowCursor = lo
}

// tabRowRange returns the inclusive [lo, hi] row-cursor range that maps
// to the active tab's rows inside the shared flat layout.
func (m EditConfigModel) tabRowRange() (int, int) {
	switch m.activeTab {
	case tabModels:
		return 0, m.editor.modelsCount() - 1
	case tabBehavior:
		row := m.editor.inquirenessRow()
		return row, row
	case tabGates:
		return m.editor.checkpointsStart(), m.editor.lastRow()
	}
	return 0, m.editor.lastRow()
}

// clampToActiveTab pins the editor's rowCursor into the active tab's
// range. Called before delegating keys so operations like ←/→/space
// always hit a row that matches the visible tab.
func (m *EditConfigModel) clampToActiveTab() {
	lo, hi := m.tabRowRange()
	if m.editor.rowCursor < lo {
		m.editor.rowCursor = lo
	}
	if m.editor.rowCursor > hi {
		m.editor.rowCursor = hi
	}
}

// wrapInRange wraps idx into the inclusive [lo, hi] range so that
// moving one past hi lands on lo and one before lo lands on hi.
func wrapInRange(idx, lo, hi int) int {
	if hi <= lo {
		return lo
	}
	size := hi - lo + 1
	mod := (idx - lo) % size
	if mod < 0 {
		mod += size
	}
	return lo + mod
}
