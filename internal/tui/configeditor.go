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
}

// rowCategory identifies which sub-editor the row cursor currently sits in.
type rowCategory int

const (
	rowCatModels rowCategory = iota
	rowCatInquireness
	rowCatCheckpoints
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
// tab/shift+tab is context-sensitive: Models rows cycle the model value,
// non-Models rows jump to the next/previous sub-editor (also wrapping).
func (m ConfigEditorModel) Update(msg tea.Msg) (ConfigEditorModel, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	key := keyMsg.String()
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
		case "right", "l", "tab":
			m.cycleModelForward()
		case "left", "h", "shift+tab":
			m.cycleModelBackward()
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

func (m *ConfigEditorModel) cycleModelForward() {
	field := m.currentModelField()
	if field == "" {
		return
	}
	opts, providers := m.catalog.FlatOptionsForField(field)
	if len(opts) == 0 {
		return
	}
	current := m.modelValueForField(field)
	nextIdx := 0 // stale-model preservation: unknown value advances to first eligible
	for i := range opts {
		if m.catalog.MatchesModelValue(providers[i], opts[i], current) {
			nextIdx = (i + 1) % len(opts)
			break
		}
	}
	m.setModelValueForField(field, opts[nextIdx])
}

func (m *ConfigEditorModel) cycleModelBackward() {
	field := m.currentModelField()
	if field == "" {
		return
	}
	opts, providers := m.catalog.FlatOptionsForField(field)
	if len(opts) == 0 {
		return
	}
	current := m.modelValueForField(field)
	nextIdx := len(opts) - 1 // stale-model preservation: unknown value goes to last eligible
	for i := range opts {
		if m.catalog.MatchesModelValue(providers[i], opts[i], current) {
			nextIdx = (i - 1 + len(opts)) % len(opts)
			break
		}
	}
	m.setModelValueForField(field, opts[nextIdx])
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

// renderModelsBox renders the Models sub-editor: a header row, 5 phase-role
// rows with the focused row highlighted, and — when the cursor is on a
// Models row — a provider-grouped picker beneath the row list. Mirrors
// WizardModel.renderModelEditor's visual grammar.
func (m ConfigEditorModel) renderModelsBox(width int) string {
	title := lipgloss.NewStyle().Bold(true).Render("Models")
	lines := []string{title, MutedStyle.Render("Assignments")}
	onModelsRow := m.rowCategory() == rowCatModels

	fields := m.catalog.Fields
	if len(fields) == 0 {
		fields = phaseCatalogFields
	}
	for i, field := range fields {
		label := field
		value := m.modelValueForField(field)
		summary := m.modelAssignmentSummary(field, value)
		prefix := "  "
		rendered := fmt.Sprintf("%-14s %s", label, summary)
		if onModelsRow && i == m.rowCursor {
			prefix = SelectedRowStyle.Render("▸ ")
			rendered = SelectedRowStyle.Render(rendered)
		}
		lines = append(lines, prefix+rendered)
	}

	if onModelsRow {
		field := m.currentModelField()
		lines = append(lines, "", lipgloss.NewStyle().Bold(true).Foreground(colorBrand).Render("Choices for "+field))
		lines = append(lines, m.renderModelChoiceLines(field, width)...)
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
	provider, model := splitProviderModel(value)
	label := value
	if provider != "" {
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
	for _, group := range m.catalog.ProviderGroupsForField(field) {
		for _, opt := range group.Models {
			if m.catalog.MatchesModelValue(group.Name, opt, value) {
				return true
			}
		}
	}
	return false
}

func (m ConfigEditorModel) renderModelChoiceLines(field string, width int) []string {
	current := m.modelValueForField(field)
	groups := m.catalog.ProviderGroupsForField(field)
	if len(groups) == 0 {
		return []string{MutedStyle.Render("No eligible models available.")}
	}

	selectedPillStyle := lipgloss.NewStyle().Bold(true).Foreground(colorBase).Background(colorActive).Padding(0, 1)
	optionStyle := lipgloss.NewStyle().Foreground(colorText)
	recommendedStyle := lipgloss.NewStyle().Bold(true).Foreground(colorInfo)

	var lines []string
	for _, group := range groups {
		providerLabel := group.Name
		if providerLabel == "Available" {
			providerLabel = "models"
		}
		prefix := MutedStyle.Render(fmt.Sprintf("%-8s ", providerLabel))
		var tokens []string
		for _, opt := range group.Models {
			_, label := splitProviderModel(opt)
			recommended := m.catalog.MatchesModelValue(group.Name, opt, m.catalog.PhaseDefaults[field])
			selected := m.catalog.MatchesModelValue(group.Name, opt, current)
			if recommended {
				label += "★"
			}
			switch {
			case selected:
				tokens = append(tokens, selectedPillStyle.Render(label))
			case recommended:
				tokens = append(tokens, recommendedStyle.Render(label))
			default:
				tokens = append(tokens, optionStyle.Render(label))
			}
		}
		if width > 0 {
			lines = append(lines, wrapRenderedTokensWithPrefix(prefix, tokens, width)...)
		} else {
			lines = append(lines, prefix+strings.Join(tokens, " "))
		}
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
