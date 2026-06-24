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

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// ConfigEditorModel is the shared three-axis editor sub-model. Owns the
// catalog, the flat row cursor, and per-axis value state. Phase 3 embeds
// this into the wizard's Review step; Phase 2's new overlay owns it outright.
type ConfigEditorModel struct {
	original feature.ConfigSnapshot

	models      config.ModelConfig
	inquireness feature.Inquireness
	checkpoints feature.Checkpoints
	pipeline    feature.PipelineProfile

	catalog                PhaseModelCatalog
	provisionalPublishable bool

	// rowCursor indexes into a flat logical row list:
	//   0 .. len(catalog.Fields)-1     : Models rows
	//   modelsCount                    : Inquireness
	//   modelsCount+1 .. lastRow()     : Checkpoints (4 base + 1 when publishable)
	rowCursor int

	activeModelCell   modelCell
	modelFilterActive bool
	modelFilter       string
	modelFilterCursor int
}

// rowCategory identifies which sub-editor the row cursor currently sits in.
type rowCategory int

const (
	rowCatModels rowCategory = iota
	rowCatInquireness
	rowCatCheckpoints
)

type modelCell int

const (
	modelCellAgent modelCell = iota
	modelCellModel
	modelCellPhase
)

// Row-layout constants. Callers use methods (modelsCount, inquirenessRow,
// checkpointsStart, lastRow) because the Checkpoints count depends on
// provisionalPublishable.
const (
	configEditorModelsCount = 5
)

type checkpointField struct {
	Gate  feature.GateIndex
	Label string
	Desc  string
}

var checkpointFields = []checkpointField{
	{Gate: feature.GateInquiryReview, Label: "Inquiry Review", Desc: "Pause after inquiry before research"},
	{Gate: feature.GateResearchReview, Label: "Research Review", Desc: "Pause after research before design"},
	{Gate: feature.GateDesignReview, Label: "Design Review", Desc: "Pause after design before planning"},
	{Gate: feature.GatePlanReview, Label: "Plan Review", Desc: "Pause after planning before implementation"},
	{Gate: feature.GateManualPublish, Label: "Manual Publish", Desc: "Review diff and PR before publishing"},
}

// NewConfigEditorModel constructs an editor seeded with the given feature's
// current config values and a pre-built catalog. provisionalPublishable
// controls whether the ManualPublish checkpoint row is shown (matches the
// wizard's provisionalPublishable flag semantics — derived from
// feature.Feature.IsPublishable() at modal-open time).
//
// Re-entrancy + crash recovery: constructor only, no persisted state. Safe
// to rebuild on every modal open; identical inputs yield an equal model.
func NewConfigEditorModel(f *feature.Feature, cat PhaseModelCatalog, provisionalPublishable bool) ConfigEditorModel {
	if f == nil {
		f = &feature.Feature{}
	}
	pipeline := f.Pipeline
	normalized := pipeline.NormalizeCheckpoints(f.Checkpoints, provisionalPublishable)
	snap := feature.ConfigSnapshot{
		Models:      f.Models,
		Inquireness: f.Inquireness,
		Checkpoints: normalized,
	}
	return ConfigEditorModel{
		original:               snap,
		models:                 snap.Models,
		inquireness:            snap.Inquireness,
		checkpoints:            snap.Checkpoints,
		pipeline:               pipeline,
		catalog:                cat,
		provisionalPublishable: provisionalPublishable,
		rowCursor:              0,
	}
}

// Update handles inner keys. The flat row cursor walks all three sub-editors
// with wrap at both ends; per-row value cycling dispatches by row category.
// tab/shift+tab is context-sensitive: Models rows switch between Agent and
// Model cells; non-Models rows jump to the next/previous sub-editor.
func (m ConfigEditorModel) Update(msg tea.Msg) (ConfigEditorModel, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	key := keyMsg.String()
	if m.modelFilterActive {
		switch key {
		case "esc":
			m.clearModelFilter()
			return m, nil
		case "enter":
			m.acceptFilteredModel()
			return m, nil
		case "up", "k":
			m.moveModelFilterCursor(-1)
			return m, nil
		case "down", "j":
			m.moveModelFilterCursor(+1)
			return m, nil
		case "backspace":
			m.modelFilter = trimLastRune(m.modelFilter)
			m.modelFilterCursor = 0
			return m, nil
		}
		if len([]rune(keyMsg.Text)) == 1 {
			m.modelFilter += keyMsg.Text
			m.modelFilterCursor = 0
			return m, nil
		}
		return m, nil
	}
	// Row-cursor walk wraps at both ends.
	switch key {
	case "up", "k":
		m.rowCursor = wrapRow(m.rowCursor-1, m.lastRow())
		return m, nil
	case "down", "j":
		m.rowCursor = wrapRow(m.rowCursor+1, m.lastRow())
		return m, nil
	}
	// Per-row value cycling + context-sensitive tab/shift+tab.
	switch m.rowCategory() {
	case rowCatModels:
		switch key {
		case "tab":
			m.activeModelCell = modelCellModel
		case "shift+tab":
			m.activeModelCell = modelCellAgent
		case "/":
			if m.activeModelCell == modelCellModel {
				m.startModelFilter()
			}
		case "right", "l":
			if m.activeModelCell == modelCellAgent {
				m.cycleAgent(+1)
			} else {
				m.cycleModelForward()
			}
		case "left", "h":
			if m.activeModelCell == modelCellAgent {
				m.cycleAgent(-1)
			} else {
				m.cycleModelBackward()
			}
		}
	case rowCatInquireness:
		switch key {
		case "right", "l":
			m.inquireness = cycleInquireness(m.inquireness, +1)
		case "left", "h":
			m.inquireness = cycleInquireness(m.inquireness, -1)
		case "tab":
			m.rowCursor = m.nextEditorStart()
		case "shift+tab":
			m.rowCursor = m.previousEditorStart()
		}
	case rowCatCheckpoints:
		switch key {
		case " ", "space":
			m.toggleCurrentCheckpoint()
		case "tab":
			m.rowCursor = m.nextEditorStart()
		case "shift+tab":
			m.rowCursor = m.previousEditorStart()
		}
	}
	return m, nil
}

// View renders three stacked boxes (Models, Inquireness, Checkpoints).
// Width propagates to each renderer.
func (m ConfigEditorModel) View() string {
	return m.ViewWidth(0)
}

// ViewWidth renders the three boxes with an explicit content width.
func (m ConfigEditorModel) ViewWidth(width int) string {
	var b strings.Builder
	b.WriteString(m.renderModelsBox(width))
	b.WriteString("\n\n")
	b.WriteString(m.renderInquirenessBox())
	b.WriteString("\n\n")
	b.WriteString(m.renderCheckpointsBox(width))
	return b.String()
}

// Snapshot returns the current edited values as a ConfigSnapshot. When the
// ManualPublish row is hidden (!provisionalPublishable), the saved snapshot
// forces ManualPublish=true to match the wizard's invariant.
func (m ConfigEditorModel) Snapshot() feature.ConfigSnapshot {
	cp := m.pipeline.NormalizeCheckpoints(m.checkpoints, m.provisionalPublishable)
	return feature.ConfigSnapshot{
		Models:      m.models,
		Inquireness: m.inquireness,
		Checkpoints: cp,
	}
}

// Original returns the seed snapshot captured at construction time.
func (m ConfigEditorModel) Original() feature.ConfigSnapshot { return m.original }

// HasChanges reports whether the current snapshot differs from the original
// along any of the three axes.
func (m ConfigEditorModel) HasChanges() bool {
	s := m.Snapshot()
	return s.Models != m.original.Models ||
		s.Inquireness != m.original.Inquireness ||
		s.Checkpoints != m.original.Checkpoints
}

// ModelsChangeCount returns the number of phase-role fields in Models that
// differ from the original snapshot.
func (m ConfigEditorModel) ModelsChangeCount() int {
	orig := m.original.Models
	cur := m.models
	count := 0
	if cur.Research != orig.Research {
		count++
	}
	if cur.Planning != orig.Planning {
		count++
	}
	if cur.Implementation != orig.Implementation {
		count++
	}
	if cur.Review != orig.Review {
		count++
	}
	if cur.Utilities != orig.Utilities {
		count++
	}
	if cur.KBBuild != orig.KBBuild {
		count++
	}
	return count
}

// CheckpointsChangeCount returns the number of Checkpoints struct fields
// that differ from the original snapshot (applying ManualPublish
// substitution to keep parity with Snapshot()).
func (m ConfigEditorModel) CheckpointsChangeCount() int {
	cp := m.Snapshot().Checkpoints
	orig := m.original.Checkpoints
	count := 0
	if cp.InquiryReview != orig.InquiryReview {
		count++
	}
	if cp.ResearchReview != orig.ResearchReview {
		count++
	}
	if cp.DesignReview != orig.DesignReview {
		count++
	}
	if cp.PlanReview != orig.PlanReview {
		count++
	}
	if cp.ManualPublish != orig.ManualPublish {
		count++
	}
	return count
}

// InquirenessChanged reports whether the inquireness axis differs from the
// original.
func (m ConfigEditorModel) InquirenessChanged() bool {
	return m.inquireness != m.original.Inquireness
}

// --- Row-layout helpers ---

func (m ConfigEditorModel) modelsCount() int {
	if n := len(m.catalog.Fields); n > 0 {
		return n
	}
	return configEditorModelsCount
}

func (m ConfigEditorModel) inquirenessRow() int   { return m.modelsCount() }
func (m ConfigEditorModel) checkpointsStart() int { return m.modelsCount() + 1 }

func (m ConfigEditorModel) visibleCheckpointCount() int {
	return len(m.visibleCheckpointFields())
}

func (m ConfigEditorModel) lastRow() int {
	return m.checkpointsStart() + m.visibleCheckpointCount() - 1
}

func (m ConfigEditorModel) rowCategory() rowCategory {
	switch {
	case m.rowCursor < m.modelsCount():
		return rowCatModels
	case m.rowCursor == m.inquirenessRow():
		return rowCatInquireness
	default:
		return rowCatCheckpoints
	}
}

func (m ConfigEditorModel) nextEditorStart() int {
	switch m.rowCategory() {
	case rowCatModels:
		return m.inquirenessRow()
	case rowCatInquireness:
		return m.checkpointsStart()
	case rowCatCheckpoints:
		return 0
	}
	return 0
}

func (m ConfigEditorModel) previousEditorStart() int {
	switch m.rowCategory() {
	case rowCatModels:
		return m.checkpointsStart()
	case rowCatInquireness:
		return m.modelsCount() - 1
	case rowCatCheckpoints:
		return m.inquirenessRow()
	}
	return 0
}

// wrapRow clamps-then-wraps a flat row index in [0, last] so up-from-0
// lands on last and down-from-last lands on 0.
func wrapRow(idx, last int) int {
	if last < 0 {
		return 0
	}
	size := last + 1
	if size <= 0 {
		return 0
	}
	mod := idx % size
	if mod < 0 {
		mod += size
	}
	return mod
}

// --- Models helpers ---

func (m ConfigEditorModel) currentModelField() string {
	if len(m.catalog.Fields) == 0 {
		return ""
	}
	idx := m.rowCursor
	if idx < 0 || idx >= len(m.catalog.Fields) {
		return ""
	}
	return m.catalog.Fields[idx]
}

func (m ConfigEditorModel) modelValueForField(field string) string {
	switch field {
	case "Research":
		return m.models.Research
	case "Planning":
		return m.models.Planning
	case "Implementation":
		return m.models.Implementation
	case "Review":
		return m.models.Review
	case "KB Build":
		return m.models.KBBuild
	}
	return ""
}

func (m *ConfigEditorModel) setModelValueForField(field, value string) {
	switch field {
	case "Research":
		m.models.Research = value
	case "Planning":
		m.models.Planning = value
	case "Implementation":
		m.models.Implementation = value
	case "Review":
		m.models.Review = value
	case "KB Build":
		m.models.KBBuild = value
	}
}

func (m ConfigEditorModel) currentModelEntries() []PhaseModelEntry {
	field := m.currentModelField()
	if field == "" {
		return nil
	}
	return m.catalog.ModelEntriesForField(field)
}

func (m ConfigEditorModel) agentOptionsForField(field string) []string {
	var agents []string
	for _, group := range m.catalog.ProviderEntryGroupsForField(field) {
		if len(group.Models) > 0 {
			agents = append(agents, group.Name)
		}
	}
	return agents
}

func (m ConfigEditorModel) agentValueForField(field string) string {
	agents := m.agentOptionsForField(field)
	if len(agents) == 0 {
		return ""
	}
	for _, value := range []string{m.modelValueForField(field), m.catalog.PhaseDefaults[field]} {
		if entry, ok := m.entryForFieldValue(field, value); ok {
			return entry.Agent
		}
		if provider, _ := splitProviderModel(value); provider != "" {
			for _, agent := range agents {
				if agent == provider {
					return agent
				}
			}
		}
	}
	return agents[0]
}

func (m *ConfigEditorModel) cycleAgent(delta int) {
	field := m.currentModelField()
	if field == "" {
		return
	}
	agents := m.agentOptionsForField(field)
	if len(agents) == 0 {
		return
	}
	current := m.agentValueForField(field)
	idx := 0
	for i, agent := range agents {
		if agent == current {
			idx = i
			break
		}
	}
	next := (idx + delta%len(agents) + len(agents)) % len(agents)
	targetAgent := agents[next]
	if targetAgent == current {
		value := m.modelValueForField(field)
		if value == "" {
			return
		}
		if entry, ok := m.entryForFieldValue(field, value); ok && entry.Agent == targetAgent {
			return
		}
	}
	entry, ok := m.catalog.RecommendedEntryForAgent(field, targetAgent)
	if !ok {
		return
	}
	m.setModelValueForField(field, m.catalog.SelectionValue(entry))
}

func (m *ConfigEditorModel) cycleModelForward() {
	field := m.currentModelField()
	if field == "" {
		return
	}
	agent := m.agentValueForField(field)
	entries := m.catalog.EntriesForFieldAndAgent(field, agent)
	if len(entries) == 0 {
		entries = m.currentModelEntries()
	}
	if len(entries) == 0 {
		return
	}
	current := m.modelValueForField(field)
	nextIdx := 0 // stale-model preservation: unknown value advances to first eligible
	for i, entry := range entries {
		if m.catalog.MatchesModelValue(entry.Agent, entry.ModelID, current) {
			nextIdx = (i + 1) % len(entries)
			break
		}
	}
	m.setModelValueForField(field, m.catalog.SelectionValue(entries[nextIdx]))
}

func (m *ConfigEditorModel) cycleModelBackward() {
	field := m.currentModelField()
	if field == "" {
		return
	}
	agent := m.agentValueForField(field)
	entries := m.catalog.EntriesForFieldAndAgent(field, agent)
	if len(entries) == 0 {
		entries = m.currentModelEntries()
	}
	if len(entries) == 0 {
		return
	}
	current := m.modelValueForField(field)
	nextIdx := len(entries) - 1 // stale-model preservation: unknown value goes to last eligible
	for i, entry := range entries {
		if m.catalog.MatchesModelValue(entry.Agent, entry.ModelID, current) {
			nextIdx = (i - 1 + len(entries)) % len(entries)
			break
		}
	}
	m.setModelValueForField(field, m.catalog.SelectionValue(entries[nextIdx]))
}

func (m ConfigEditorModel) filteredModelEntriesForCurrentRow() []PhaseModelEntry {
	field := m.currentModelField()
	if field == "" {
		return nil
	}
	agent := m.agentValueForField(field)
	entries := m.catalog.EntriesForFieldAndAgent(field, agent)
	if len(entries) == 0 {
		entries = m.currentModelEntries()
	}
	filter := strings.ToLower(strings.TrimSpace(m.modelFilter))
	if filter == "" {
		return entries
	}
	var filtered []PhaseModelEntry
	for _, entry := range entries {
		if modelEntryMatchesFilter(entry, filter) {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

func (m *ConfigEditorModel) startModelFilter() {
	m.modelFilterActive = true
	m.modelFilter = ""
	m.modelFilterCursor = 0
	field := m.currentModelField()
	value := m.modelValueForField(field)
	for i, entry := range m.filteredModelEntriesForCurrentRow() {
		if m.catalog.MatchesModelValue(entry.Agent, entry.ModelID, value) {
			m.modelFilterCursor = i
			return
		}
	}
}

func (m *ConfigEditorModel) clearModelFilter() {
	m.modelFilterActive = false
	m.modelFilter = ""
	m.modelFilterCursor = 0
}

func (m *ConfigEditorModel) acceptFilteredModel() {
	if strings.TrimSpace(m.modelFilter) == "" {
		m.clearModelFilter()
		return
	}
	entries := m.filteredModelEntriesForCurrentRow()
	if len(entries) > 0 {
		idx := m.modelFilterCursor
		if idx < 0 {
			idx = 0
		}
		if idx >= len(entries) {
			idx = len(entries) - 1
		}
		m.setModelValueForField(m.currentModelField(), m.catalog.SelectionValue(entries[idx]))
	}
	m.clearModelFilter()
}

func (m ConfigEditorModel) ModelFilteringActive() bool { return m.modelFilterActive }

func (m ConfigEditorModel) entryForFieldValue(field, value string) (PhaseModelEntry, bool) {
	for _, entry := range m.catalog.ModelEntriesForField(field) {
		if m.catalog.MatchesModelValue(entry.Agent, entry.ModelID, value) {
			return entry, true
		}
	}
	return PhaseModelEntry{}, false
}

func (m *ConfigEditorModel) moveModelFilterCursor(delta int) {
	entries := m.filteredModelEntriesForCurrentRow()
	if len(entries) == 0 {
		m.modelFilterCursor = 0
		return
	}
	m.modelFilterCursor = (m.modelFilterCursor + delta%len(entries) + len(entries)) % len(entries)
}

func modelEntryMatchesFilter(entry PhaseModelEntry, filter string) bool {
	values := []string{
		entry.Agent,
		entry.ModelID,
		entry.DisplayName,
		entry.FullID,
		entry.Category,
	}
	values = append(values, entry.Aliases...)
	for _, value := range values {
		if strings.Contains(strings.ToLower(value), filter) {
			return true
		}
	}
	return false
}

func trimLastRune(s string) string {
	runes := []rune(s)
	if len(runes) == 0 {
		return s
	}
	return string(runes[:len(runes)-1])
}

func displayModelLabel(entry PhaseModelEntry, fallback string) string {
	if entry.DisplayName != "" {
		return entry.DisplayName
	}
	if fallback != "" {
		if _, model := splitProviderModel(fallback); model != "" {
			return model
		}
		return fallback
	}
	return entry.ModelID
}

// --- Checkpoint helpers ---

func (m ConfigEditorModel) checkpointIndexForRow(row int) int {
	return row - m.checkpointsStart()
}

func (m *ConfigEditorModel) toggleCurrentCheckpoint() {
	idx := m.checkpointIndexForRow(m.rowCursor)
	fields := m.visibleCheckpointFields()
	if idx < 0 || idx >= len(fields) {
		return
	}
	m.setCheckpointValue(fields[idx].Gate, !m.checkpointValue(fields[idx].Gate))
}

func (m ConfigEditorModel) checkpointValue(gate feature.GateIndex) bool {
	switch gate {
	case feature.GateInquiryReview:
		return m.checkpoints.InquiryReview
	case feature.GateResearchReview:
		return m.checkpoints.ResearchReview
	case feature.GateDesignReview:
		return m.checkpoints.DesignReview
	case feature.GatePlanReview:
		return m.checkpoints.PlanReview
	case feature.GateManualPublish:
		return m.checkpoints.ManualPublish
	}
	return false
}

func (m *ConfigEditorModel) setCheckpointValue(gate feature.GateIndex, value bool) {
	switch gate {
	case feature.GateInquiryReview:
		m.checkpoints.InquiryReview = value
	case feature.GateResearchReview:
		m.checkpoints.ResearchReview = value
	case feature.GateDesignReview:
		m.checkpoints.DesignReview = value
	case feature.GatePlanReview:
		m.checkpoints.PlanReview = value
	case feature.GateManualPublish:
		m.checkpoints.ManualPublish = value
	}
}

func (m ConfigEditorModel) visibleCheckpointFields() []checkpointField {
	projection := m.pipeline.ProjectGates(m.checkpoints, m.provisionalPublishable)
	fields := make([]checkpointField, 0, len(projection.Visible))
	for _, gate := range projection.Visible {
		for _, field := range checkpointFields {
			if field.Gate == gate {
				fields = append(fields, field)
				break
			}
		}
	}
	return fields
}

// --- Renderers ---

func cycleInquireness(v feature.Inquireness, delta int) feature.Inquireness {
	order := []feature.Inquireness{
		feature.InquirenessNone,
		feature.InquirenessMedium,
		feature.InquirenessHigh,
	}
	idx := 0
	for i, o := range order {
		if o == v {
			idx = i
			break
		}
	}
	n := len(order)
	next := (idx + delta%n + n) % n
	return order[next]
}

func inquirenessDescription(v feature.Inquireness) string {
	switch v {
	case feature.InquirenessNone:
		return "Harness keeps planning questions hidden unless manual input is required"
	case feature.InquirenessMedium:
		return "Harness surfaces key planning questions"
	case feature.InquirenessHigh:
		return "Harness surfaces more planning questions"
	default:
		return ""
	}
}

func (m ConfigEditorModel) renderModelsWorkspaceWithFocus(focus configFocusZone) string {
	return m.renderModelsWorkspaceWithFocusWidth(focus, 0)
}

func (m ConfigEditorModel) renderModelsWorkspaceWithFocusWidth(focus configFocusZone, width int) string {
	title := lipgloss.NewStyle().Bold(true).Render("Model Selection")
	phaseWidth, agentWidth, modelWidth := modelWorkspacePanelWidths(width)
	phasePane := m.renderModelPhaseList(focus, phaseWidth)
	inspector := m.renderModelInspector(focus, agentWidth, modelWidth)
	return strings.Join([]string{
		title,
		"",
		lipgloss.JoinHorizontal(lipgloss.Top, phasePane, "  ", inspector),
	}, "\n")
}

func modelWorkspacePanelWidths(width int) (int, int, int) {
	phaseWidth, agentWidth, modelWidth := modelPhasePanelWidth, modelAgentPanelWidth, modelListPanelWidth
	if width <= 0 {
		return phaseWidth, agentWidth, modelWidth
	}
	available := width - 4 // two 2-space gutters between the three panels
	if available <= 0 {
		return phaseWidth, agentWidth, modelWidth
	}
	overflow := phaseWidth + agentWidth + modelWidth - available
	if overflow <= 0 {
		return phaseWidth, agentWidth, modelWidth
	}
	shrink := func(current, minWidth int) int {
		if overflow <= 0 {
			return current
		}
		delta := current - minWidth
		if delta > overflow {
			delta = overflow
		}
		if delta < 0 {
			delta = 0
		}
		overflow -= delta
		return current - delta
	}
	modelWidth = shrink(modelWidth, 44)
	phaseWidth = shrink(phaseWidth, 34)
	agentWidth = shrink(agentWidth, 16)
	return phaseWidth, agentWidth, modelWidth
}

func (m ConfigEditorModel) renderModelPhaseList(focus configFocusZone, width int) string {
	fields := m.catalog.Fields
	if len(fields) == 0 {
		fields = phaseCatalogFields
	}
	lines := []string{}
	for i, field := range fields {
		label := truncatePhaseLabelForWidth(m.phaseAssignmentLabel(field), width)
		prefix := "  "
		selected := i == m.rowCursor
		focused := selected && focus == configFocusPhaseList
		switch {
		case focused:
			prefix = SelectedRowStyle.Render("▸ ")
			label = SelectedRowStyle.Render(label)
		case selected && focus != configFocusTabs:
			prefix = MutedStyle.Render("✓ ")
			label = SummarySelectedValueStyle.Render(label)
		}
		lines = append(lines, prefix+label)
	}
	return titledConfigBox("Phases", strings.Join(lines, "\n"), width, modelPanelHeight, focus == configFocusPhaseList)
}

func (m ConfigEditorModel) renderModelInspector(focus configFocusZone, agentWidth, modelWidth int) string {
	field := m.currentModelField()
	if field == "" {
		emptyAgents := titledConfigBox("Agents", MutedStyle.Render("No phase"), agentWidth, modelPanelHeight, focus == configFocusAgentList)
		emptyModels := titledConfigBox("Models", MutedStyle.Render("No phase"), modelWidth, modelPanelHeight, focus == configFocusModelList)
		return lipgloss.JoinHorizontal(lipgloss.Top, emptyAgents, "  ", emptyModels)
	}
	agent := m.agentValueForField(field)
	agents := m.renderAgentPicker(field, agent, focus, agentWidth)
	models := m.renderModelPicker(field, agent, focus, modelWidth)
	return lipgloss.JoinHorizontal(lipgloss.Top, agents, "  ", models)
}

func (m ConfigEditorModel) renderAgentPicker(field string, currentAgent string, focus configFocusZone, width int) string {
	agents := m.agentOptionsForField(field)
	if len(agents) == 0 {
		return titledConfigBox("Agents", MutedStyle.Render("No agents"), width, modelPanelHeight, focus == configFocusAgentList)
	}
	var lines []string
	for _, agent := range agents {
		prefix := "  "
		label := truncateString(agent, maxInt(width-6, 8))
		selected := agent == currentAgent
		focused := selected && focus == configFocusAgentList
		switch {
		case focused:
			prefix = SelectedRowStyle.Render("▸ ")
			label = SelectedRowStyle.Render(label)
		case selected && focus != configFocusTabs:
			prefix = MutedStyle.Render("✓ ")
			label = SummarySelectedValueStyle.Render(label)
		}
		lines = append(lines, prefix+label)
	}
	return titledConfigBox("Agents", strings.Join(lines, "\n"), width, modelPanelHeight, focus == configFocusAgentList)
}

func (m ConfigEditorModel) renderModelPicker(field, agent string, focus configFocusZone, width int) string {
	entries := m.filteredModelEntriesForCurrentRow()
	panelFocused := focus == configFocusModelList || m.ModelFilteringActive()
	title := truncateString("Models for "+agent, maxInt(width-4, 8))
	if len(entries) == 0 {
		return titledConfigBox(title, MutedStyle.Render("No models"), width, modelPanelHeight, panelFocused)
	}
	current := m.modelValueForField(field)
	focusIdx := m.modelPickerFocusIndex(entries, current)
	start, end := visibleWindow(len(entries), focusIdx, 9)
	labelWidth := modelEntryLabelWidth(width)

	var lines []string
	if m.ModelFilteringActive() {
		filter := m.modelFilter
		if filter == "" {
			filter = " "
		}
		lines = append(lines, MutedStyle.Render("Search  ")+SummarySelectedValueStyle.Render(filter), "")
	} else {
		lines = append(lines, MutedStyle.Render("/ search"), "")
	}
	if start > 0 {
		lines = append(lines, MutedStyle.Render("  ..."))
	}
	for i := start; i < end; i++ {
		entry := entries[i]
		selected := m.catalogEntrySelected(entry, current)
		highlighted := selected
		if m.ModelFilteringActive() {
			highlighted = i == m.modelFilterCursor
		}
		prefix := "  "
		label := compactModelEntryLabel(entry, entry.ModelID)
		meta := compactModelEntryMeta(entry)
		line := fmt.Sprintf("%-*s %s", labelWidth, truncateString(label, labelWidth), meta)
		focused := highlighted && panelFocused
		switch {
		case focused:
			line = SelectedRowStyle.Render(line)
			prefix = SelectedRowStyle.Render("▸ ")
		case selected && focus != configFocusTabs:
			prefix = MutedStyle.Render("✓ ")
			line = SummarySelectedValueStyle.Render(line)
		}
		lines = append(lines, prefix+line)
	}
	if end < len(entries) {
		lines = append(lines, MutedStyle.Render("  ..."))
	}
	return titledConfigBox(title, strings.Join(lines, "\n"), width, modelPanelHeight, panelFocused)
}

func modelEntryLabelWidth(panelWidth int) int {
	width := panelWidth - 30
	if width < 18 {
		return 18
	}
	if width > 44 {
		return 44
	}
	return width
}

func (m ConfigEditorModel) modelPickerFocusIndex(entries []PhaseModelEntry, current string) int {
	if len(entries) == 0 {
		return 0
	}
	if m.ModelFilteringActive() {
		if m.modelFilterCursor < 0 {
			return 0
		}
		if m.modelFilterCursor >= len(entries) {
			return len(entries) - 1
		}
		return m.modelFilterCursor
	}
	for i, entry := range entries {
		if m.catalogEntrySelected(entry, current) {
			return i
		}
	}
	return 0
}

func (m ConfigEditorModel) catalogEntrySelected(entry PhaseModelEntry, current string) bool {
	return m.catalog.MatchesModelValue(entry.Agent, entry.ModelID, current)
}

func (m ConfigEditorModel) phaseAssignmentLabel(field string) string {
	agent := m.agentValueForField(field)
	value := m.modelValueForField(field)
	if value == "" {
		if agent == "" {
			return field + " (default)"
		}
		return fmt.Sprintf("%s (%s/default)", field, agent)
	}
	model := value
	unavailable := ""
	if entry, ok := m.entryForFieldValue(field, value); ok {
		model = compactModelEntryLabel(entry, value)
	} else if value != "" {
		if provider, modelID := splitProviderModel(value); provider != "" {
			agent = provider
			model = modelID
		} else {
			model = value
		}
		unavailable = " (unavailable)"
	}
	if agent == "" {
		return field + " (" + model + unavailable + ")"
	}
	return fmt.Sprintf("%s (%s/%s%s)", field, agent, model, unavailable)
}

// renderModelsBox renders the Models sub-editor as a Phase | Agent | Model
// cascade. The focused row expands into agent choices and model choices scoped
// to the selected agent.
func (m ConfigEditorModel) renderModelsBox(width int) string {
	title := lipgloss.NewStyle().Bold(true).Render("Models")
	lines := []string{
		title,
		MutedStyle.Render(fmt.Sprintf("%-14s %-12s %s", "Phase", "Agent", "Model")),
	}
	onModelsRow := m.rowCategory() == rowCatModels

	fields := m.catalog.Fields
	if len(fields) == 0 {
		fields = phaseCatalogFields
	}
	for i, field := range fields {
		value := m.modelValueForField(field)
		agent := m.agentValueForField(field)
		model := m.modelAssignmentSummary(field, value)
		prefix := "  "
		renderedAgent := fmt.Sprintf("%-12s", agent)
		renderedModel := model
		if onModelsRow && i == m.rowCursor {
			prefix = SelectedRowStyle.Render("▸ ")
			if m.activeModelCell == modelCellAgent {
				renderedAgent = SummarySelectedValueStyle.Render(fmt.Sprintf("%-12s", agent))
			} else {
				renderedModel = SummarySelectedValueStyle.Render(model)
			}
		}
		rendered := fmt.Sprintf("%-14s %s %s", field, renderedAgent, renderedModel)
		if onModelsRow && i == m.rowCursor {
			rendered = SelectedRowStyle.Render(rendered)
		}
		lines = append(lines, prefix+rendered)
	}

	if onModelsRow {
		field := m.currentModelField()
		lines = append(lines, "", lipgloss.NewStyle().Bold(true).Foreground(colorBrand).Render("Selection for "+field))
		lines = append(lines, m.renderModelCascadeDetails(field, width)...)
	}
	return strings.Join(lines, "\n")
}

// modelAssignmentSummary mirrors the wizard helper, with an additional
// "(unavailable)" suffix when value is not present in the catalog's
// eligible list for field.
func (m ConfigEditorModel) modelAssignmentSummary(field, value string) string {
	if value == "" {
		return "(default)"
	}
	label := value
	if entry, ok := m.entryForFieldValue(field, value); ok {
		label = displayModelLabel(entry, value)
	} else if provider, model := splitProviderModel(value); provider != "" {
		label = provider + " / " + model
	}
	if !m.modelValueEligible(field, value) {
		label += " (unavailable)"
	}
	return label
}

func (m ConfigEditorModel) modelValueEligible(field, value string) bool {
	if value == "" {
		return true
	}
	_, ok := m.entryForFieldValue(field, value)
	return ok
}

func (m ConfigEditorModel) renderModelCascadeDetails(field string, width int) []string {
	current := m.modelValueForField(field)
	agents := m.agentOptionsForField(field)
	if len(agents) == 0 {
		return []string{MutedStyle.Render("No eligible models available.")}
	}

	selectedPillStyle := lipgloss.NewStyle().Bold(true).Foreground(colorBase).Background(colorActive).Padding(0, 1)
	optionStyle := lipgloss.NewStyle().Foreground(colorText)
	recommendedStyle := lipgloss.NewStyle().Bold(true).Foreground(colorInfo)

	var lines []string
	currentAgent := m.agentValueForField(field)
	var agentTokens []string
	for _, agent := range agents {
		label := agent
		if entry, ok := m.catalog.RecommendedEntryForAgent(field, agent); ok && entry.Recommended {
			label += "★"
		}
		if agent == currentAgent {
			agentTokens = append(agentTokens, selectedPillStyle.Render(label))
		} else {
			agentTokens = append(agentTokens, optionStyle.Render(label))
		}
	}
	agentPrefix := MutedStyle.Render("Agents  ")
	if width > 0 {
		lines = append(lines, wrapRenderedTokensWithPrefix(agentPrefix, agentTokens, width)...)
	} else {
		lines = append(lines, agentPrefix+strings.Join(agentTokens, " "))
	}

	if m.modelFilterActive {
		lines = append(lines, MutedStyle.Render("Filter  ")+SummarySelectedValueStyle.Render(m.modelFilter))
	}

	entries := m.filteredModelEntriesForCurrentRow()
	if len(entries) == 0 {
		lines = append(lines, MutedStyle.Render("Models  No matches."))
		return lines
	}

	var modelTokens []string
	highlightIdx := -1
	for i, entry := range entries {
		label := displayModelLabel(entry, entry.ModelID)
		if entry.Recommended {
			label += "★"
		}
		selected := m.catalog.MatchesModelValue(entry.Agent, entry.ModelID, current)
		highlighted := selected
		if m.modelFilterActive {
			highlighted = i == m.modelFilterCursor
		}
		switch {
		case highlighted:
			modelTokens = append(modelTokens, selectedPillStyle.Render(label))
			highlightIdx = i
		case entry.Recommended:
			modelTokens = append(modelTokens, recommendedStyle.Render(label))
		default:
			modelTokens = append(modelTokens, optionStyle.Render(label))
		}
	}
	modelPrefix := MutedStyle.Render("Models  ")
	if width > 0 {
		lines = append(lines, wrapRenderedTokensWithPrefix(modelPrefix, modelTokens, width)...)
	} else {
		lines = append(lines, modelPrefix+strings.Join(modelTokens, " "))
	}
	if highlightIdx < 0 {
		highlightIdx = 0
	}
	entry := entries[highlightIdx]
	var parts []string
	parts = append(parts, "agent "+entry.Agent)
	if entry.Category != "" {
		parts = append(parts, "category "+entry.Category)
	}
	if entry.ContextWindow > 0 {
		parts = append(parts, "context "+compactContextWindow(entry.ContextWindow))
	}
	if entry.Recommended {
		parts = append(parts, "recommended")
	}
	if len(parts) > 0 {
		lines = append(lines, MutedStyle.Render("Details ")+strings.Join(parts, ", "))
	}
	return lines
}

// renderInquirenessBox renders the Inquireness pill selector; the focused
// row highlight is applied when the cursor is on the Inquireness row.
func (m ConfigEditorModel) renderInquirenessBox() string {
	focused := m.rowCategory() == rowCatInquireness
	headerText := "Inquireness"
	header := lipgloss.NewStyle().Bold(true).Render(headerText)
	if focused {
		header = SelectedRowStyle.Bold(true).Render("▸ " + headerText)
	}
	pills := []feature.Inquireness{
		feature.InquirenessNone,
		feature.InquirenessMedium,
		feature.InquirenessHigh,
	}
	activeStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")).Background(lipgloss.Color("62")).Padding(0, 1)
	idleStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Padding(0, 1)
	var rendered []string
	for _, p := range pills {
		label := string(p)
		if p == m.inquireness {
			rendered = append(rendered, activeStyle.Render(label))
		} else {
			rendered = append(rendered, idleStyle.Render(label))
		}
	}
	currentLabel := MutedStyle.Render("Current:") + " " + SummarySelectedValueStyle.Render(string(m.inquireness))
	optionsLabel := MutedStyle.Render("Options")
	desc := inquirenessDescription(m.inquireness)
	lines := []string{
		header,
		currentLabel,
		"",
		optionsLabel,
		"  " + strings.Join(rendered, "  "),
	}
	if desc != "" {
		lines = append(lines, "", MutedStyle.Render(desc))
	}
	return strings.Join(lines, "\n")
}

// renderCheckpointsBox renders the Checkpoints sub-editor as a toggle grid.
// The ManualPublish row is hidden when !provisionalPublishable.
func (m ConfigEditorModel) renderCheckpointsBox(_ int) string {
	title := lipgloss.NewStyle().Bold(true).Render("Gates")
	lines := []string{title}
	fields := m.visibleCheckpointFields()
	for i, cp := range fields {
		box := "[ ]"
		if m.checkpointValue(cp.Gate) {
			box = SuccessStyle.Render("[x]")
		} else {
			box = MutedStyle.Render("[ ]")
		}
		label := cp.Label
		desc := MutedStyle.Render(cp.Desc)
		prefix := "  "
		rendered := fmt.Sprintf("%s %-18s %s", box, label, desc)
		if m.rowCategory() == rowCatCheckpoints && m.checkpointIndexForRow(m.rowCursor) == i {
			prefix = SelectedRowStyle.Render("▸ ")
			rendered = fmt.Sprintf("%s %s %s", box, SelectedRowStyle.Render(fmt.Sprintf("%-18s", label)), desc)
		}
		lines = append(lines, prefix+rendered)
	}
	return strings.Join(lines, "\n")
}
