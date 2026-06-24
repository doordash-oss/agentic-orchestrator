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
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"

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

type configFocusZone int

const (
	configFocusTabs configFocusZone = iota
	configFocusBody
	configFocusPhaseList
	configFocusAgentList
	configFocusModelList
	configFocusGateList
	configFocusGateState
)

const (
	modelPhasePanelWidth = 56
	modelAgentPanelWidth = 22
	modelListPanelWidth  = 74
	modelPanelHeight     = 11
	modelPhaseLabelWidth = 48
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
	focus       configFocusZone
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
		focus:       configFocusTabs,
	}
}

// Update owns the modal-level keyboard grammar for the segmented-tab
// layout:
//
//   - tab / shift+tab and left/right cycle tabs only while the tab strip is
//     focused.
//   - down enters the active tab's body.
//   - the Models body owns an explicit phase → agent → model focus chain.
//   - Behavior delegates value changes to the embedded editor.
//   - Gates owns an explicit gate → on/off focus chain.
//
// AppModel.Update owns enter/esc/save dispatch on top of this — those keys
// are short-circuited before reaching here.
func (m EditConfigModel) Update(msg tea.Msg) (EditConfigModel, tea.Cmd) {
	if m.activeTab == tabModels && m.editor.ModelFilteringActive() {
		key := ""
		if keyMsg, ok := msg.(tea.KeyPressMsg); ok {
			key = keyMsg.String()
		}
		var cmd tea.Cmd
		m.editor, cmd = m.editor.Update(msg)
		if key == "enter" {
			m.focus = configFocusPhaseList
		}
		return m, cmd
	}

	if keyMsg, ok := msg.(tea.KeyPressMsg); ok {
		key := keyMsg.String()
		if m.focus == configFocusTabs {
			switch key {
			case "tab", "right", "l":
				m.cycleActiveTab(+1)
				return m, nil
			case "shift+tab", "left", "h":
				m.cycleActiveTab(-1)
				return m, nil
			case "down", "j":
				m.enterActiveTabBody()
				return m, nil
			}
			return m, nil
		}

		if m.activeTab == tabModels {
			if m.updateModelsWorkspaceKey(keyMsg) {
				return m, nil
			}
			return m, nil
		}
		if m.activeTab == tabGates {
			if m.updateGatesWorkspaceKey(keyMsg) {
				return m, nil
			}
			return m, nil
		}
		if m.activeTab == tabBehavior {
			switch key {
			case "up", "k":
				m.editor.inquireness = cycleInquireness(m.editor.inquireness, -1)
				return m, nil
			case "down", "j":
				m.editor.inquireness = cycleInquireness(m.editor.inquireness, +1)
				return m, nil
			case "enter":
				m.focus = configFocusTabs
				return m, nil
			case "tab":
				m.focus = configFocusTabs
				m.cycleActiveTab(+1)
				return m, nil
			case "shift+tab":
				m.focus = configFocusTabs
				m.cycleActiveTab(-1)
				return m, nil
			}
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
		b.WriteString(m.renderModelsWorkspace())
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
	focusedTabStyle := lipgloss.NewStyle().Bold(true).Foreground(colorBrand)
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
		if t.id == m.activeTab && m.focus == configFocusTabs {
			parts = append(parts, focusedTabStyle.Render("▸ "+label))
			continue
		}
		if t.id == m.activeTab {
			parts = append(parts, activePill.Render(label))
		} else {
			parts = append(parts, idlePill.Render(label))
		}
	}
	return strings.Join(parts, "  ")
}

func (m EditConfigModel) renderModelsWorkspace() string {
	return m.editor.renderModelsWorkspaceWithFocus(m.focus)
}

func truncatePhaseLabel(label string) string {
	return truncatePhaseLabelForWidth(label, modelPhasePanelWidth)
}

func truncatePhaseLabelForWidth(label string, panelWidth int) string {
	labelWidth := modelPhaseLabelWidth
	if panelWidth > 0 {
		labelWidth = maxInt(panelWidth-8, 16)
	}
	const unavailableSuffix = " (unavailable))"
	if strings.HasSuffix(label, unavailableSuffix) && len(label) > labelWidth {
		head := strings.TrimSpace(strings.TrimSuffix(label, unavailableSuffix))
		headWidth := labelWidth - len(unavailableSuffix)
		if headWidth > 3 {
			return truncateString(head, headWidth) + unavailableSuffix
		}
	}
	return truncateString(label, labelWidth)
}

func titledConfigBox(title, body string, width, height int, focused bool) string {
	borderColor := lipgloss.Color("238")
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("244"))
	if focused {
		borderColor = lipgloss.Color("62")
		titleStyle = lipgloss.NewStyle().Bold(true).Foreground(colorBrand)
	}
	return lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(borderColor).
		BorderTop(true).
		Padding(0, 1).
		Width(width).
		Height(height).
		Render(titleStyle.Render(title) + "\n" + body)
}

func compactModelEntryLabel(entry PhaseModelEntry, fallback string) string {
	label := displayModelLabel(entry, fallback)
	if context := compactContextWindow(entry.ContextWindow); context != "" && !strings.Contains(label, "["+context+"]") && !strings.Contains(label, "("+context+")") {
		label += "[" + context + "]"
	}
	return label
}

func compactModelEntryMeta(entry PhaseModelEntry) string {
	var parts []string
	if entry.Category != "" {
		parts = append(parts, entry.Category)
	}
	if context := compactContextWindow(entry.ContextWindow); context != "" {
		parts = append(parts, context)
	}
	if entry.Recommended {
		parts = append(parts, "recommended")
	}
	return strings.Join(parts, " ")
}

func compactContextWindow(tokens int) string {
	return llm.ContextWindowLabel(tokens)
}

func visibleWindow(total, focusIdx, size int) (int, int) {
	if total <= size {
		return 0, total
	}
	if focusIdx < 0 {
		focusIdx = 0
	}
	if focusIdx >= total {
		focusIdx = total - 1
	}
	start := focusIdx - size/2
	if start < 0 {
		start = 0
	}
	if start+size > total {
		start = total - size
	}
	return start, start + size
}

// renderBehaviorPane renders Behavior in the same three-panel workspace
// shape as Models and Gates so tab changes do not resize the overlay.
func (m EditConfigModel) renderBehaviorPane() string {
	return renderConfigWorkspace(
		"Behavior",
		m.renderBehaviorSettingPanel(),
		m.renderBehaviorValuesPanel(),
		m.renderBehaviorDetailsPanel(),
	)
}

func (m EditConfigModel) renderBehaviorSettingPanel() string {
	label := "Inquireness"
	if m.focus == configFocusBody {
		label = SummarySelectedValueStyle.Render(label)
	}
	return titledConfigBox("Settings", "  "+label, modelPhasePanelWidth, modelPanelHeight, false)
}

func (m EditConfigModel) renderBehaviorValuesPanel() string {
	pills := []feature.Inquireness{
		feature.InquirenessNone,
		feature.InquirenessMedium,
		feature.InquirenessHigh,
	}
	lines := make([]string, 0, len(pills))
	for _, p := range pills {
		label := string(p)
		prefix := "  "
		selected := p == m.editor.inquireness
		focused := selected && m.focus == configFocusBody
		switch {
		case focused:
			prefix = SelectedRowStyle.Render("▸ ")
			label = SelectedRowStyle.Render(label)
		case selected && m.focus != configFocusTabs:
			prefix = MutedStyle.Render("✓ ")
			label = SummarySelectedValueStyle.Render(label)
		}
		lines = append(lines, prefix+label)
	}
	return titledConfigBox("Values", strings.Join(lines, "\n"), modelAgentPanelWidth, modelPanelHeight, m.focus == configFocusBody)
}

func (m EditConfigModel) renderBehaviorDetailsPanel() string {
	lines := []string{
		MutedStyle.Render("Selected"),
		"  " + SummarySelectedValueStyle.Render(string(m.editor.inquireness)),
	}
	if desc := inquirenessDescription(m.editor.inquireness); desc != "" {
		lines = append(lines,
			"",
			MutedStyle.Render("Effect"),
			"  "+MutedStyle.Render(desc),
			"",
			MutedStyle.Render("Scope"),
			"  "+MutedStyle.Render("Applies to future planning questions"),
		)
	}
	return titledConfigBox("Details", strings.Join(lines, "\n"), modelListPanelWidth, modelPanelHeight, false)
}

// renderGatesPane renders Gates in the same three-panel workspace shape as
// Models and Behavior so tab changes do not resize the overlay.
func (m EditConfigModel) renderGatesPane() string {
	return renderConfigWorkspace(
		"Gates",
		m.renderGateListPanel(),
		m.renderGateStatePanel(),
		m.renderGateDetailsPanel(),
	)
}

func (m EditConfigModel) renderGateListPanel() string {
	total := m.editor.visibleCheckpointCount()
	fields := m.editor.visibleCheckpointFields()
	onGates := m.editor.rowCategory() == rowCatCheckpoints
	focusedIdx := -1
	if onGates && m.gateListFocused() {
		focusedIdx = m.editor.checkpointIndexForRow(m.editor.rowCursor)
	}

	lines := make([]string, 0, total)
	for i := range total {
		cp := fields[i]
		prefix := "  "
		label := cp.Label
		selected := i == m.editor.checkpointIndexForRow(m.editor.rowCursor)
		focused := i == focusedIdx
		switch {
		case focused:
			prefix = SelectedRowStyle.Render("▸ ")
			label = SelectedRowStyle.Render(label)
		case selected && m.focus == configFocusGateState:
			prefix = MutedStyle.Render("✓ ")
			label = SummarySelectedValueStyle.Render(label)
		}
		lines = append(lines, prefix+label)
	}
	return titledConfigBox("Gates", strings.Join(lines, "\n"), modelPhasePanelWidth, modelPanelHeight, m.gateListFocused())
}

func (m EditConfigModel) renderGateStatePanel() string {
	field, ok := m.selectedCheckpointField()
	if !ok {
		return titledConfigBox("State", MutedStyle.Render("No gate"), modelAgentPanelWidth, modelPanelHeight, m.focus == configFocusGateState)
	}
	current := m.editor.checkpointValue(field.Gate)
	choices := []struct {
		label string
		value bool
	}{
		{"on", true},
		{"off", false},
	}
	lines := make([]string, 0, len(choices))
	for _, choice := range choices {
		prefix := "  "
		label := choice.label
		selected := choice.value == current
		focused := selected && m.focus == configFocusGateState
		switch {
		case focused:
			prefix = SelectedRowStyle.Render("▸ ")
			label = SelectedRowStyle.Render(label)
		case selected:
			prefix = MutedStyle.Render("✓ ")
			label = SummarySelectedValueStyle.Render(label)
		}
		lines = append(lines, prefix+label)
	}
	return titledConfigBox("State", strings.Join(lines, "\n"), modelAgentPanelWidth, modelPanelHeight, m.focus == configFocusGateState)
}

func (m EditConfigModel) renderGateDetailsPanel() string {
	field, ok := m.selectedCheckpointField()
	if !ok {
		return titledConfigBox("Details", MutedStyle.Render("No gates"), modelListPanelWidth, modelPanelHeight, false)
	}
	return titledConfigBox("Details", MutedStyle.Render(field.Desc), modelListPanelWidth, modelPanelHeight, false)
}

func (m EditConfigModel) selectedCheckpointField() (checkpointField, bool) {
	fields := m.editor.visibleCheckpointFields()
	if len(fields) == 0 {
		return checkpointField{}, false
	}
	idx := 0
	if m.editor.rowCategory() == rowCatCheckpoints {
		idx = m.editor.checkpointIndexForRow(m.editor.rowCursor)
	}
	if idx < 0 || idx >= len(fields) {
		idx = 0
	}
	return fields[idx], true
}

func (m EditConfigModel) gateListFocused() bool {
	return m.focus == configFocusGateList || m.focus == configFocusBody
}

func renderConfigWorkspace(title string, left, middle, right string) string {
	renderedTitle := lipgloss.NewStyle().Bold(true).Render(title)
	return strings.Join([]string{
		renderedTitle,
		"",
		lipgloss.JoinHorizontal(lipgloss.Top, left, "  ", middle, "  ", right),
	}, "\n")
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

	if m.focus == configFocusTabs {
		return hintStyle.Render("←→/tab tabs   ↓ edit   enter save   esc cancel")
	}

	var keys string
	switch m.activeTab {
	case tabModels:
		switch m.focus {
		case configFocusPhaseList:
			keys = "↑↓ phase   → agents   enter save   esc cancel"
		case configFocusAgentList:
			keys = "↑↓ agent   → models   enter phase   esc cancel"
		case configFocusModelList:
			keys = "↑↓ model   / search   enter phase   esc cancel"
		default:
			keys = "↑↓ phase   → agents   / search   enter save   esc cancel"
		}
	case tabBehavior:
		keys = "↑↓ choose   enter tabs   tab tabs   esc cancel"
	case tabGates:
		switch m.focus {
		case configFocusGateList, configFocusBody:
			keys = "↑↓ gate   → state   space toggle   enter save   esc cancel"
		case configFocusGateState:
			keys = "↑ on   ↓ off   ←/enter gates   esc cancel"
		default:
			keys = "↑↓ gate   → state   space toggle   enter save   esc cancel"
		}
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

func (m *EditConfigModel) enterActiveTabBody() {
	switch m.activeTab {
	case tabModels:
		m.focus = configFocusPhaseList
		lo, hi := m.tabRowRange()
		m.editor.rowCursor = clampInRange(m.editor.rowCursor, lo, hi)
		m.editor.activeModelCell = modelCellAgent
	case tabBehavior:
		m.focus = configFocusBody
		lo, _ := m.tabRowRange()
		m.editor.rowCursor = lo
	case tabGates:
		m.focus = configFocusGateList
		lo, _ := m.tabRowRange()
		m.editor.rowCursor = lo
	}
}

func (m *EditConfigModel) updateModelsWorkspaceKey(keyMsg tea.KeyPressMsg) bool {
	key := keyMsg.String()
	switch m.focus {
	case configFocusPhaseList:
		switch key {
		case "up", "k":
			if m.editor.rowCursor <= 0 {
				m.focus = configFocusTabs
				m.editor.rowCursor = 0
				return true
			}
			m.editor.rowCursor--
			return true
		case "down", "j":
			last := m.editor.modelsCount() - 1
			if m.editor.rowCursor < last {
				m.editor.rowCursor++
			}
			return true
		case "right", "l":
			m.focus = configFocusAgentList
			m.editor.activeModelCell = modelCellAgent
			return true
		}
	case configFocusAgentList:
		switch key {
		case "up", "k":
			m.editor.cycleAgent(-1)
			return true
		case "down", "j":
			m.editor.cycleAgent(+1)
			return true
		case "right", "l":
			m.focus = configFocusModelList
			m.editor.activeModelCell = modelCellModel
			return true
		case "left", "h", "enter":
			m.focus = configFocusPhaseList
			return true
		}
	case configFocusModelList:
		switch key {
		case "up", "k":
			m.editor.cycleModelBackward()
			return true
		case "down", "j":
			m.editor.cycleModelForward()
			return true
		case "left", "h":
			m.focus = configFocusAgentList
			m.editor.activeModelCell = modelCellAgent
			return true
		case "enter":
			m.focus = configFocusPhaseList
			return true
		case "/":
			m.editor.startModelFilter()
			return true
		}
	}
	return false
}

func (m *EditConfigModel) updateGatesWorkspaceKey(keyMsg tea.KeyPressMsg) bool {
	key := keyMsg.String()
	lo, hi := m.tabRowRange()
	switch m.focus {
	case configFocusBody:
		m.focus = configFocusGateList
		fallthrough
	case configFocusGateList:
		switch key {
		case "up", "k":
			if m.editor.rowCursor <= lo {
				m.focus = configFocusTabs
				m.editor.rowCursor = lo
				return true
			}
			m.editor.rowCursor = clampInRange(m.editor.rowCursor-1, lo, hi)
			return true
		case "down", "j":
			m.editor.rowCursor = clampInRange(m.editor.rowCursor+1, lo, hi)
			return true
		case "right", "l":
			m.focus = configFocusGateState
			return true
		case " ", "space":
			m.editor.toggleCurrentCheckpoint()
			return true
		case "tab":
			m.focus = configFocusTabs
			m.cycleActiveTab(+1)
			return true
		case "shift+tab":
			m.focus = configFocusTabs
			m.cycleActiveTab(-1)
			return true
		}
	case configFocusGateState:
		field, ok := m.selectedCheckpointField()
		if !ok {
			m.focus = configFocusGateList
			return true
		}
		switch key {
		case "up", "k":
			m.editor.setCheckpointValue(field.Gate, true)
			return true
		case "down", "j":
			m.editor.setCheckpointValue(field.Gate, false)
			return true
		case " ", "space":
			m.editor.setCheckpointValue(field.Gate, !m.editor.checkpointValue(field.Gate))
			return true
		case "left", "h", "enter":
			m.focus = configFocusGateList
			return true
		case "tab":
			m.focus = configFocusTabs
			m.cycleActiveTab(+1)
			return true
		case "shift+tab":
			m.focus = configFocusTabs
			m.cycleActiveTab(-1)
			return true
		}
	}
	return false
}

func (m EditConfigModel) EnterIsLocal() bool {
	return (m.activeTab == tabModels && (m.editor.ModelFilteringActive() || m.focus == configFocusAgentList || m.focus == configFocusModelList)) ||
		(m.activeTab == tabBehavior && m.focus == configFocusBody) ||
		(m.activeTab == tabGates && m.focus == configFocusGateState)
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

func clampInRange(idx, lo, hi int) int {
	if idx < lo {
		return lo
	}
	if idx > hi {
		return hi
	}
	return idx
}
