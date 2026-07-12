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
	"path/filepath"
	"sort"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

type DetailModel struct {
	feature        *feature.Feature
	stateDir       string // base state dir for computing artifact paths
	spinnerView    string // set by parent from app-level spinner
	contextPct     int    // context window usage percentage for active session; -1 = no data
	kbStaleWarning string // yellow warning text when KB is outdated or missing
	width          int
	height         int

	// Refactor overlay — set by parent when refactor input is active.
	// When refactorActive is true, View/ViewCompact render a refactor input panel
	// instead of the normal detail content.
	refactorActive      bool
	refactorInputView   string // pre-rendered textarea.View() output
	refactorFeatureName string

	// Refactor pipeline selector — set by parent after refactor
	// prompt is submitted. Shows pipeline selection overlay.
	refactorPipelineActive bool
	refactorPipelineView   string // pre-rendered pipeline selector output
}

func NewDetailModel(f *feature.Feature, stateDir string) DetailModel {
	return DetailModel{feature: f, stateDir: stateDir, contextPct: -1}
}

// renderRefactorOverlay renders the refactor input panel that replaces normal
// detail content when a refactor is being composed. width is the available
// content width (outer panel width for compact, boxWidth for full-screen).
func (m DetailModel) renderRefactorOverlay(width int) string {
	var b strings.Builder

	// Brief header: feature name
	b.WriteString(TitleStyle.Render("Refactor"))
	b.WriteString("  " + MutedStyle.Render(m.refactorFeatureName))
	b.WriteString("\n\n")

	// Textarea in a styled sub-panel (active border = brand color)
	taBox := panelStyle(true).Width(width - 4).Render(m.refactorInputView)
	taBox = renderBorderTitle(taBox, "What changes do you want to make?", lipgloss.NewStyle().Foreground(colorBrand))
	b.WriteString(taBox)
	b.WriteString("\n")

	// Key hints
	b.WriteString(KeyHelpStyle.Render(" [ctrl+s] Submit   [esc] Cancel"))
	b.WriteString("\n")

	return b.String()
}

// kbStatusIcon returns a styled icon for a per-repo KB build status string.
func kbStatusIcon(status string) string {
	switch {
	case status == "completed":
		return SuccessStyle.Render("\u2713")
	case strings.HasPrefix(status, "failed"):
		return ErrorStyle.Render("\u2717")
	case status == "pending":
		return MutedStyle.Render("\u25cb")
	default:
		return MutedStyle.Render("\u27F3")
	}
}

// repoDisplayOrder returns repo names in feature-declared order.
//
// Prior to SchemaVersionCurrent = 3, the TUI ordered repos by the persisted
// feature.ExecutionPlan. With per-phase execution-order.yaml read fresh from
// disk by the orchestrator, the feature no longer carries a plan; the TUI
// falls back to the feature's declared Repos order, which is stable and
// deterministic across phases.
func repoDisplayOrder(f *feature.Feature) []string {
	names := make([]string, len(f.Repos))
	for i, r := range f.Repos {
		names[i] = r.Name
	}
	return names
}

func featureHasDisplayFailure(f *feature.Feature) bool {
	return f != nil && f.HasTerminalFailure()
}

func effectiveCurrentPhaseForDisplay(f *feature.Feature) feature.Phase {
	if f == nil {
		return 0
	}
	if featureHasDisplayFailure(f) && f.CurrentPhase == feature.PhasePublish && looksLikeFinalReviewFailureForDisplay(f.LastError) {
		return feature.PhaseFinalReview
	}
	return f.CurrentPhase
}

func looksLikeFinalReviewFailureForDisplay(msg string) bool {
	msg = strings.ToLower(msg)
	return strings.Contains(msg, "final_review") || strings.Contains(msg, "final review")
}

func phaseMatchesCurrentForDisplay(row, current feature.Phase) bool {
	return row == current || (row == feature.PhaseReview && current == feature.PhaseFinalReview)
}

// cycleGroup is a display-aggregated view of one cycle type (rebase, tweak,
// refactor, review-comments) under the Implement phase. It collapses all
// individual cycles of the same type into a single line showing the latest
// index, total count, cumulative duration, and cumulative cost.
type cycleGroup struct {
	label       string        // e.g. "Rebase #3 (3 total)" or "Review Comments"
	totalDur    time.Duration // sum across all cycles in the group
	totalCost   float64       // sum across all cycles in the group
	active      bool          // true if f.ActiveTimingKey matches any cycle in this group
	interrupted bool          // true if the latest cycle is in RepoCycleInterrupted (resumable)
}

// phaseTimingKeys returns grouped cycle entries for the implement phase. Each
// group collapses multiple cycles of the same type (rebase/tweak/refactor)
// into a single row so the panel doesn't grow unboundedly.
func phaseTimingKeys(f *feature.Feature) []cycleGroup {
	if f.PhaseTimings == nil {
		return nil
	}
	var groups []cycleGroup
	// activeCycle surfaces an in-flight cycle even before its timing/cost have
	// been persisted. The rebase/tweak loops do not overwrite ActiveTimingKey
	// when they bump the counter, so a running rebase-N can leave PhaseTimings
	// without an entry while ActiveTimingKey is still pointing at the previous
	// cycle key — without consulting ActiveCycle, the right panel would silently
	// drop the row that the left panel labels "Rebasing".
	activeCycle := f.ActiveCycle
	collect := func(prefix, singular string, count int) {
		var g cycleGroup
		lastIdx := 0
		present := 0
		for i := 1; i <= count; i++ {
			k := fmt.Sprintf("%s-%d", prefix, i)
			cycleInFlight := activeCycle != nil &&
				string(activeCycle.Type) == prefix &&
				activeCycle.Status == feature.RepoCycleRunning &&
				activeCycle.Count == i
			cycleEnded := activeCycle != nil &&
				string(activeCycle.Type) == prefix &&
				activeCycle.Count == i &&
				(activeCycle.Status == feature.RepoCycleFailed ||
					activeCycle.Status == feature.RepoCycleInterrupted)
			hasData := f.PhaseTimings[k] > 0 || f.ActiveTimingKey == k || cycleInFlight
			if !hasData {
				continue
			}
			present++
			lastIdx = i
			g.totalDur += f.PhaseTimings[k]
			g.totalCost += f.PhaseCost(k)
			// ActiveCycle is the source of truth for what's running now.
			// ActiveTimingKey is preserved across cycle transitions, so a
			// finished tweak-N can still equal it after a fresh rebase
			// starts — only cycleInFlight is safe to drive "active".
			if cycleInFlight {
				g.active = true
			}
			if cycleEnded && activeCycle.Status == feature.RepoCycleInterrupted {
				g.interrupted = true
			}
		}
		if present == 0 {
			return
		}
		if present == 1 {
			g.label = fmt.Sprintf("%s #%d", singular, lastIdx)
		} else {
			g.label = fmt.Sprintf("%s #%d (%d total)", singular, lastIdx, present)
		}
		groups = append(groups, g)
	}
	collect("rebase", "Rebase", f.RebaseCount())
	collect("tweak", "Tweak", f.TweakCount())
	collect("refactor", "Refactor", f.RefactorCount())
	k := "review-comments"
	rcInFlight := activeCycle != nil &&
		activeCycle.Type == feature.CycleReviewComments &&
		activeCycle.Status == feature.RepoCycleRunning
	rcEnded := activeCycle != nil &&
		activeCycle.Type == feature.CycleReviewComments &&
		(activeCycle.Status == feature.RepoCycleFailed ||
			activeCycle.Status == feature.RepoCycleInterrupted)
	if f.PhaseTimings[k] > 0 || f.ActiveTimingKey == k || rcInFlight {
		groups = append(groups, cycleGroup{
			label:       "Review Comments",
			totalDur:    f.PhaseTimings[k],
			totalCost:   f.PhaseCost(k),
			active:      rcInFlight,
			interrupted: rcEnded && activeCycle.Status == feature.RepoCycleInterrupted,
		})
	}
	return groups
}

// formatPhaseDuration returns a styled duration string for display, or empty
// string if the duration is zero.
func formatPhaseDuration(d time.Duration) string {
	if d == 0 {
		return ""
	}
	return MutedStyle.Render(" " + formatDuration(d))
}

func (m DetailModel) activeProgressIcon() string {
	if m.spinnerView != "" {
		return m.spinnerView
	}
	return MutedStyle.Render("\u27F3")
}

func formatContextUsage(contextPct int) string {
	if contextPct < 0 {
		return MutedStyle.Render(" context window: calculating\u2026")
	}
	pctStr := fmt.Sprintf(" context window: %d%%", contextPct)
	switch {
	case contextPct >= 80:
		return ErrorStyle.Render(pctStr)
	case contextPct >= 60:
		return WarningStyle.Render(pctStr)
	default:
		return SuccessStyle.Render(pctStr)
	}
}

func setupState(f *feature.Feature) *feature.SetupState {
	if f == nil {
		return nil
	}
	return f.Run().Setup
}

func isSetupLifecycle(f *feature.Feature) bool {
	if f == nil {
		return false
	}
	if f.Status == feature.StatusSettingUpWorktrees || f.FailureType == feature.FailureWorktreeSetup {
		return true
	}
	if setup := setupState(f); setup != nil {
		return setup.Status == feature.SetupStatusRunning || setup.Status == feature.SetupStatusFailed
	}
	return false
}

func canRetrySetup(f *feature.Feature) bool {
	setup := setupState(f)
	return f != nil &&
		f.Status == feature.StatusFailed &&
		f.FailureType == feature.FailureWorktreeSetup &&
		setup != nil &&
		setup.Status == feature.SetupStatusFailed
}

func (m DetailModel) renderSetupProgress(_ *feature.Feature, setup *feature.SetupState) string {
	var b strings.Builder
	header := "Setup"
	if setup.Attempt > 0 {
		header += fmt.Sprintf(" attempt %d", setup.Attempt)
	}
	b.WriteString(fmt.Sprintf("  %s %s %s\n", setupStatusIcon(setup.Status, m), header, setupStatusLabel(setup.Status)))
	if setup.LatestLogPath != "" {
		b.WriteString("    " + LabelStyle.Render("Log") + "  " + MutedStyle.Render(setup.LatestLogPath) + "\n")
	}
	if setup.LastError != "" {
		b.WriteString("    " + LabelStyle.Render("Error") + "  " + ErrorStyle.Render(setup.LastError) + "\n")
	}
	for _, key := range setupTaskOrder(setup) {
		task := setup.Tasks[key]
		b.WriteString(renderSetupTask(task, m))
	}
	return strings.TrimRight(b.String(), "\n")
}

func setupTaskOrder(setup *feature.SetupState) []string {
	if setup == nil {
		return nil
	}
	seen := make(map[string]bool, len(setup.Tasks))
	var order []string
	for _, key := range setup.TaskOrder {
		if _, ok := setup.Tasks[key]; ok && !seen[key] {
			order = append(order, key)
			seen[key] = true
		}
	}
	var rest []string
	for key := range setup.Tasks {
		if !seen[key] {
			rest = append(rest, key)
		}
	}
	sort.Strings(rest)
	return append(order, rest...)
}

func renderSetupTask(task feature.SetupTask, m DetailModel) string {
	var b strings.Builder
	icon := setupStatusIcon(task.Status, m)
	label := task.Label
	if label == "" {
		label = task.Key
	}
	b.WriteString(fmt.Sprintf("  %s  \u21b3 %s [%s] %s", MutedStyle.Render(" "), icon+" "+label, task.Kind, setupStatusLabel(task.Status)))
	if task.Attempt > 0 {
		b.WriteString(fmt.Sprintf(" attempt %d", task.Attempt))
	}
	b.WriteString("\n")
	if task.Repo != "" {
		b.WriteString("      " + LabelStyle.Render("Repo") + "  " + MutedStyle.Render(task.Repo) + "\n")
	}
	if task.Branch != "" {
		b.WriteString("      " + LabelStyle.Render("Branch") + "  " + MutedStyle.Render(task.Branch) + "\n")
	}
	if task.Path != "" {
		b.WriteString("      " + LabelStyle.Render("Path") + "  " + MutedStyle.Render(task.Path) + "\n")
	}
	if task.StartedAt != nil {
		b.WriteString("      " + LabelStyle.Render("Started") + "  " + MutedStyle.Render(formatSetupTime(*task.StartedAt)) + "\n")
	}
	if task.EndedAt != nil {
		b.WriteString("      " + LabelStyle.Render("Ended") + "  " + MutedStyle.Render(formatSetupTime(*task.EndedAt)) + "\n")
	}
	if task.LastError != "" {
		b.WriteString("      " + LabelStyle.Render("Error") + "  " + ErrorStyle.Render(task.LastError) + "\n")
	}
	return b.String()
}

func setupStatusIcon(status feature.SetupStatus, m DetailModel) string {
	switch status {
	case feature.SetupStatusRunning:
		return m.activeProgressIcon()
	case feature.SetupStatusDone:
		return SuccessStyle.Render("\u2713")
	case feature.SetupStatusFailed:
		return ErrorStyle.Render("\u2717")
	default:
		return MutedStyle.Render("\u25cb")
	}
}

func setupStatusLabel(status feature.SetupStatus) string {
	switch status {
	case feature.SetupStatusRunning:
		return lipgloss.NewStyle().Foreground(colorInfo).Render("running")
	case feature.SetupStatusDone:
		return SuccessStyle.Render("done")
	case feature.SetupStatusFailed:
		return ErrorStyle.Render("failed")
	default:
		return MutedStyle.Render("queued")
	}
}

func formatSetupTime(t time.Time) string {
	return t.Format("2006-01-02 15:04:05")
}

// ViewCompact renders a condensed version for the dashboard right panel.
func (m DetailModel) ViewCompact(width int) string {
	if m.feature == nil {
		return MutedStyle.Render("No feature selected")
	}

	f := m.feature
	if width < 20 {
		width = 40
	}

	// When refactor input is active, show the refactor overlay instead of
	// normal detail content.
	if m.refactorActive {
		return m.renderRefactorOverlay(width)
	}

	// When refactor pipeline selector is active, show the selector overlay.
	if m.refactorPipelineActive {
		return m.refactorPipelineView
	}

	var b strings.Builder

	attention := computeFeatureAttention(f, nil)
	if detailShowsInputAttention(attention) {
		b.WriteString(WarningStyle.Render("\u26a0 waiting for input"))
		b.WriteString("\n")
	}

	// Metadata
	metaContent := m.renderMetadataCompact(f)
	metaBox := panelStyle(false).Width(width - 4).Render(metaContent)
	metaBox = renderBorderTitle(metaBox, labelInfo, MutedStyle)
	b.WriteString(metaBox)
	b.WriteString("\n")

	// Phase progress
	phaseContent := m.renderPhaseProgress(f)
	phaseBox := panelStyle(false).Width(width - 4).Render(phaseContent)
	phaseBox = renderBorderTitle(phaseBox, "Phase Progress", MutedStyle)
	b.WriteString(phaseBox)
	b.WriteString("\n")

	// Failure info box.
	if featureHasDisplayFailure(f) {
		failureContent := m.renderFailureInfo(f)
		failureBox := panelStyle(false).
			BorderForeground(colorError).
			Width(width - 4).
			Render(failureContent)
		failureBox = renderBorderTitle(failureBox, "Failure Info", ErrorStyle)
		b.WriteString(failureBox)
		b.WriteString("\n")
	}

	if hint := renderLightbulbHint(f, width-4); hint != "" {
		b.WriteString(hint + "\n")
	}

	// Needs-review banner — prominent call-to-action when an artifact awaits review.
	if banner := needsReviewBanner(f); banner != "" {
		reviewBox := panelStyle(false).
			BorderForeground(colorWarning).
			Width(width - 4).
			Render(banner)
		reviewBox = renderBorderTitle(reviewBox, "Needs Review", WarningStyle)
		b.WriteString(reviewBox)
		b.WriteString("\n")
	}

	if attentionBox := renderDetailInputAttentionBox(attention, width-4); attentionBox != "" {
		b.WriteString(attentionBox)
		b.WriteString("\n")
	}

	return b.String()
}

func (m DetailModel) renderMetadataCompact(f *feature.Feature) string {
	var b strings.Builder
	b.WriteString(LabelStyle.Render("Status"))
	b.WriteString("  " + formatDetailStatus(f) + "\n")
	if desc := featureDescLine(f); desc != "" {
		b.WriteString(LabelStyle.Render("Desc"))
		b.WriteString("  " + MutedStyle.Render(desc) + "\n")
	}
	if len(f.Repos) > 0 {
		b.WriteString(LabelStyle.Render("Repos"))
		b.WriteString("  " + formatDetailRepos(f.Repos) + "\n")
	}
	if rows := renderReposBlock(f); len(rows) > 0 {
		b.WriteString(LabelStyle.Render("Repo Status") + "\n")
		for _, row := range rows {
			b.WriteString("  " + row + "\n")
		}
	}
	b.WriteString(LabelStyle.Render("Models"))
	b.WriteString("  " + MutedStyle.Render(compactModelSummary(f.Models, " ")) + "\n")
	b.WriteString(LabelStyle.Render("Input Alerts"))
	b.WriteString("  " + MutedStyle.Render(inputAlertModeLabel(f)) + "\n")
	if f.RiskLevel != "" {
		b.WriteString(LabelStyle.Render("Risk"))
		b.WriteString("  " + formatRiskBadge(f.RiskLevel) + " " + string(f.RiskLevel) + "\n")
	}

	if len(f.Repos) > 0 && f.Repos[0].WorktreePath != "" {
		workDir := f.Repos[0].WorktreePath
		if len(f.Repos) > 1 {
			workDir = filepath.Dir(workDir)
		}
		b.WriteString(LabelStyle.Render("WorkDir"))
		b.WriteString("  " + MutedStyle.Render(workDir) + "\n")
	}
	if m.stateDir != "" {
		b.WriteString(LabelStyle.Render("Artifacts"))
		b.WriteString("  " + MutedStyle.Render(filepath.Join(m.stateDir, f.ID)) + "\n")
	}
	if totalCost := f.TotalCost(); totalCost > 0 {
		b.WriteString(LabelStyle.Render(labelCost))
		b.WriteString("    " + MutedStyle.Render(formatCost(totalCost)) + "\n")
	}
	return b.String()
}

func (m DetailModel) renderPhaseProgress(f *feature.Feature) string {
	if setup := setupState(f); setup != nil && isSetupLifecycle(f) {
		return m.renderSetupProgress(f, setup)
	}

	var b strings.Builder
	allPhases := []struct {
		name     string
		phase    feature.Phase
		timerKey string
	}{
		{"Building Knowledge Base", feature.PhaseKnowledgeBase, "knowledgebase"},
		{"Inquire", feature.PhaseInquire, "inquire"},
		{"Research", feature.PhaseResearch, "research"},
		{"Design", feature.PhaseDesign, "design"},
		{"Planning", feature.PhasePlan, "plan"},
		{"Implement", feature.PhaseImplement, "implement"},
		{"Final Review", feature.PhaseReview, "review"},
		{"Publish", feature.PhasePublish, ""},
	}
	effectivePhases := f.EffectivePhases()
	effectiveSet := make(map[feature.Phase]bool, len(effectivePhases))
	for _, ep := range effectivePhases {
		effectiveSet[ep] = true
	}
	var phases []struct {
		name     string
		phase    feature.Phase
		timerKey string
	}
	for _, p := range allPhases {
		if effectiveSet[p.phase] {
			phases = append(phases, p)
		}
	}

	currentPhase := effectiveCurrentPhaseForDisplay(f)
	for i, p := range phases {
		done := p.phase.LogicalOrder() < currentPhase.LogicalOrder()
		// PhaseReview and PhaseFinalReview share logical order 6 — the row is
		// labelled "Final Review" with phase enum PhaseReview, but f.CurrentPhase
		// becomes PhaseFinalReview when the deferred end-of-feature FR pass is
		// active. Treat them as equivalent here so the row highlights correctly.
		current := phaseMatchesCurrentForDisplay(p.phase, currentPhase)
		failed := current && featureHasDisplayFailure(f)
		if failed {
			done = false
		}

		icon := phaseIcon(done, current)
		if current && isRunningStatus(f.Status) {
			icon = m.activeProgressIcon()
		} else if failed {
			icon = ErrorStyle.Render("✗")
		}

		status := MutedStyle.Render("pending")
		if failed {
			status = ErrorStyle.Render("failed")
		} else if done {
			status = SuccessStyle.Render("complete")
		} else if current {
			status = formatPhaseStatus(f)
		}

		// Mirror in-flight post-publish cycle state on the Implement parent row.
		if p.phase == feature.PhaseImplement {
			for _, g := range phaseTimingKeys(f) {
				if g.active {
					icon = m.activeProgressIcon()
					status = lipgloss.NewStyle().Foreground(colorInfo).Render("in progress")
					break
				}
			}
		}

		timing := phaseProgressTiming(f, p.phase, p.timerKey)

		// Context usage percentage (active phase only)
		if current && isRunningStatus(f.Status) {
			timing += formatContextUsage(m.contextPct)
		}

		// KB stale warning (shown when phase is pending or running). When the
		// feature is parked behind another feature's kb.lock, the wait note
		// takes precedence so the user can see why no session is running.
		kbWarning := ""
		if p.phase == feature.PhaseKnowledgeBase && !done {
			if f.Status == feature.StatusBuildingKB && f.KBWaitMessage != "" {
				kbWarning = "  " + WarningStyle.Render("\u26a0 "+f.KBWaitMessage)
			} else if m.kbStaleWarning != "" {
				kbWarning = "  " + WarningStyle.Render("\u26a0 "+m.kbStaleWarning)
			}
		}

		dots := MutedStyle.Render(" " + strings.Repeat("\u00b7", 8) + " ")
		b.WriteString(fmt.Sprintf("  %s %s%s%s%s%s", icon, p.name, dots, status, timing, kbWarning))

		// Render per-repo KB sub-items for multi-repo features
		if p.phase == feature.PhaseKnowledgeBase && (current || done) && len(f.Repos) > 1 && len(f.KBStatus) > 0 {
			for _, repo := range f.Repos {
				kbStatus, ok := f.KBStatus[repo.Name]
				if !ok {
					kbStatus = "pending"
				}
				b.WriteString(fmt.Sprintf("\n  %s  \u21b3 %s %s",
					MutedStyle.Render(" "),
					MutedStyle.Render(repo.Name),
					kbStatusIcon(kbStatus)))
			}
		}

		// Render roadmap phase sub-items under Planning (compact)
		if p.phase == feature.PhasePlan && (current || (done && f.TotalRoadmapPhases > 0)) {
			renderRoadmapPlanSubItemsCompact(&b, f)
		}

		// Render roadmap phase sub-items under Implement (compact)
		if p.phase == feature.PhaseImplement && (done || current) && f.TotalRoadmapPhases > 0 {
			renderRoadmapImplSubItems(&b, f, "\n")
		}

		// Render cycle sub-items after Implement phase
		if p.phase == feature.PhaseImplement && (done || current) {
			for _, g := range phaseTimingKeys(f) {
				cycleTiming := formatPhaseDuration(g.totalDur)
				if g.totalCost > 0 {
					cycleTiming += MutedStyle.Render(" " + formatCost(g.totalCost))
				}
				if g.active {
					cycleTiming += formatContextUsage(m.contextPct)
				}
				// Post-publish cycles (rebase/tweak/refactor/review-comments) keep
				// the feature at StatusPublished/StatusCodeReady while the cycle
				// loop is mid-flight, so isRunningStatus(f.Status) is false even
				// when the cycle is actively running. Trust the cycle's own
				// running flag (set inside cycleGroup via ActiveTimingKey or
				// ActiveCycle) so the in-flight row still renders "in progress".
				switch {
				case g.active:
					b.WriteString(fmt.Sprintf("\n  %s  \u21b3 %s %s%s",
						MutedStyle.Render(" "),
						m.activeProgressIcon()+" "+MutedStyle.Render(g.label),
						lipgloss.NewStyle().Foreground(colorInfo).Render("in progress"),
						cycleTiming))
				case g.interrupted:
					b.WriteString(fmt.Sprintf("\n  %s  \u21b3 %s %s%s",
						MutedStyle.Render(" "),
						MutedStyle.Render("\u23f8")+" "+MutedStyle.Render(g.label),
						WarningStyle.Render("interrupted"),
						cycleTiming))
				default:
					b.WriteString(fmt.Sprintf("\n  %s  \u21b3 %s%s",
						MutedStyle.Render(" "),
						MutedStyle.Render(g.label),
						cycleTiming))
				}
			}
		}

		if i < len(phases)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// renderRoadmapPlanSubItemsCompact renders plan sub-items in compact (inline newline) format.
func renderRoadmapPlanSubItemsCompact(b *strings.Builder, f *feature.Feature) {
	// Roadmap sub-item
	roadmapDone := f.CurrentRoadmapPhase > 0
	roadmapIcon := MutedStyle.Render("\u25cb")
	roadmapStatus := MutedStyle.Render("pending")
	if roadmapDone {
		roadmapIcon = SuccessStyle.Render("\u2713")
		roadmapStatus = SuccessStyle.Render("approved")
	} else if f.Status == feature.StatusPlanNeedsReview && f.CurrentRoadmapPhase == 0 {
		roadmapIcon = WarningStyle.Render("\u27F3")
		roadmapStatus = WarningStyle.Render("needs review")
	} else if f.CurrentPhase == feature.PhasePlan && f.CurrentRoadmapPhase == 0 {
		roadmapIcon = MutedStyle.Render("\u27F3")
		roadmapStatus = lipgloss.NewStyle().Foreground(colorInfo).Render("in progress")
	}
	timing := roadmapPhaseTiming(f, "plan")
	b.WriteString(fmt.Sprintf("\n  %s  \u21b3 %s %s%s",
		MutedStyle.Render(" "), roadmapIcon+" "+MutedStyle.Render("Roadmap"), roadmapStatus, timing))

	// Per-phase plan sub-items
	for i := 1; i <= f.TotalRoadmapPhases; i++ {
		label := fmt.Sprintf("Phase %d Plan", i)
		timingKey := fmt.Sprintf("phase-%d-plan", i)
		pTiming := roadmapPhaseTiming(f, timingKey)
		if i < f.CurrentRoadmapPhase {
			b.WriteString(fmt.Sprintf("\n  %s  \u21b3 %s %s%s",
				MutedStyle.Render(" "), SuccessStyle.Render("\u2713")+" "+MutedStyle.Render(label), SuccessStyle.Render("complete"), pTiming))
		} else if i == f.CurrentRoadmapPhase && f.Status == feature.StatusPlanNeedsReview {
			b.WriteString(fmt.Sprintf("\n  %s  \u21b3 %s %s%s",
				MutedStyle.Render(" "), WarningStyle.Render("\u27F3")+" "+MutedStyle.Render(label), WarningStyle.Render("needs review"), pTiming))
		} else if i == f.CurrentRoadmapPhase && f.CurrentPhase == feature.PhasePlan {
			b.WriteString(fmt.Sprintf("\n  %s  \u21b3 %s %s%s",
				MutedStyle.Render(" "), MutedStyle.Render("\u27F3")+" "+MutedStyle.Render(label), lipgloss.NewStyle().Foreground(colorInfo).Render("in progress"), pTiming))
		} else if i == f.CurrentRoadmapPhase {
			b.WriteString(fmt.Sprintf("\n  %s  \u21b3 %s %s%s",
				MutedStyle.Render(" "), SuccessStyle.Render("\u2713")+" "+MutedStyle.Render(label), SuccessStyle.Render("complete"), pTiming))
		}
	}
}

// isCycleKey returns true if the timing key represents a post-implementation cycle
// (rebase, tweak, or review-comments) rather than a roadmap phase implementation.
func isCycleKey(key string) bool {
	return strings.HasPrefix(key, "rebase-") ||
		strings.HasPrefix(key, "tweak-") ||
		strings.HasPrefix(key, "refactor-") ||
		key == "review-comments"
}

// renderRoadmapImplSubItems renders "Phase N" sub-items under the Implement phase row.
// sep is "\n" for full-screen (each item on its own line) or "" for compact (inline).
func renderRoadmapImplSubItems(b *strings.Builder, f *feature.Feature, sep string) {
	// When a cycle (rebase/tweak/review-comments) is active, all roadmap phases are complete.
	cycleActive := f.CurrentPhase == feature.PhaseImplement && isCycleKey(f.ActiveTimingKey)

	for i := 1; i <= f.TotalRoadmapPhases; i++ {
		label := fmt.Sprintf("Phase %d", i)
		timingKey := fmt.Sprintf("phase-%d-impl", i)
		timing := roadmapPhaseTiming(f, timingKey)
		if i < f.CurrentRoadmapPhase {
			b.WriteString(fmt.Sprintf("%s  %s  \u21b3 %s %s%s",
				sep, MutedStyle.Render(" "), SuccessStyle.Render("\u2713")+" "+MutedStyle.Render(label), SuccessStyle.Render("complete"), timing))
		} else if i == f.CurrentRoadmapPhase && f.CurrentPhase == feature.PhaseImplement && !cycleActive {
			b.WriteString(fmt.Sprintf("%s  %s  \u21b3 %s %s%s",
				sep, MutedStyle.Render(" "), MutedStyle.Render("\u27F3")+" "+MutedStyle.Render(label), lipgloss.NewStyle().Foreground(colorInfo).Render("in progress"), timing))
		} else if i == f.CurrentRoadmapPhase && (f.CurrentPhase.LogicalOrder() > feature.PhaseImplement.LogicalOrder() || cycleActive) {
			b.WriteString(fmt.Sprintf("%s  %s  \u21b3 %s %s%s",
				sep, MutedStyle.Render(" "), SuccessStyle.Render("\u2713")+" "+MutedStyle.Render(label), SuccessStyle.Render("complete"), timing))
		}
	}
}

// roadmapPhaseTiming returns a formatted duration + cost string for a timing key,
// or empty string if no timing data exists.
func roadmapPhaseTiming(f *feature.Feature, key string) string {
	d := f.PhaseRuntime(key)
	timing := formatPhaseDuration(d)
	if c := f.PhaseCost(key); c > 0 {
		timing += MutedStyle.Render(" " + formatCost(c))
	}
	return timing
}

func phaseProgressTiming(f *feature.Feature, phase feature.Phase, baseKey string) string {
	d, c := phaseProgressTotals(f, phase, baseKey)
	timing := formatPhaseDuration(d)
	if c > 0 {
		timing += MutedStyle.Render(" " + formatCost(c))
	}
	return timing
}

func phaseProgressTotals(f *feature.Feature, phase feature.Phase, baseKey string) (time.Duration, float64) {
	var totalDur time.Duration
	var totalCost float64
	add := func(key string) {
		if key == "" {
			return
		}
		totalDur += f.PhaseRuntime(key)
		totalCost += f.PhaseCost(key)
	}

	add(baseKey)
	if f.TotalRoadmapPhases <= 0 {
		return totalDur, totalCost
	}

	var suffix string
	switch phase {
	case feature.PhasePlan:
		suffix = "plan"
	case feature.PhaseImplement:
		suffix = "impl"
	default:
		return totalDur, totalCost
	}
	for i := 1; i <= f.TotalRoadmapPhases; i++ {
		add(fmt.Sprintf("phase-%d-%s", i, suffix))
	}
	return totalDur, totalCost
}

func detailShowsInputAttention(att featureAttention) bool {
	return att.RequiresUser() && att.Kind != attentionReview
}

func renderDetailInputAttentionBox(att featureAttention, boxWidth int) string {
	if !detailShowsInputAttention(att) {
		return ""
	}
	if att.Kind == attentionAskUser {
		att.TypeLabel = "Attention"
	}
	contentWidth := max(boxWidth-4, 1)
	return livePreviewAttentionSectionBox(att, att.ActivityLine(), boxWidth, contentWidth)
}

// needsReviewBanner returns a prominent banner describing the pending review
// artifact and the key to open it. Returns empty string when no review is pending.
func needsReviewBanner(f *feature.Feature) string {
	if !f.Status.IsNeedsReview() {
		return ""
	}
	label := needsReviewLabel(f)
	keyStyle := WarningStyle.Bold(true)
	return WarningStyle.Render("  \u25b8 ") + label + WarningStyle.Render(" needs review — press ") + keyStyle.Render("[a]") + WarningStyle.Render(" to review")
}

// needsReviewLabel returns a human-readable label for the artifact awaiting review.
func needsReviewLabel(f *feature.Feature) string {
	return WarningStyle.Bold(true).Render(reviewArtifactLabel(f))
}

func inputAlertModeLabel(f *feature.Feature) string {
	if f.MuteInputNotifications == nil {
		return "default"
	}
	if *f.MuteInputNotifications {
		return "muted"
	}
	return "enabled"
}

func countPendingHelp(f *feature.Feature) int {
	count := 0
	for _, h := range f.HelpQueue {
		if h.Pending {
			count++
		}
	}
	return count
}

func finalReviewSubphaseText(f *feature.Feature) string {
	subphase := "reviewing"
	if f != nil && f.ReviewFixing {
		subphase = "fixing"
	}
	if f != nil && f.ReviewIteration > 0 {
		return fmt.Sprintf("%s iteration %d", subphase, f.ReviewIteration)
	}
	return subphase
}

func finalReviewStatusText(f *feature.Feature) string {
	return "Final Review: " + finalReviewSubphaseText(f)
}

func formatDetailStatus(f *feature.Feature) string {
	if setup := setupState(f); setup != nil && setup.Status == feature.SetupStatusFailed {
		msg := "Failed"
		if f.FailureType != "" {
			msg = "Failed (" + formatFailureType(f.FailureType) + ")"
		}
		var hints []string
		if canRetrySetup(f) {
			hints = append(hints, "press [r] to retry setup")
		}
		if setup.LatestLogPath != "" {
			hints = append(hints, "[l] logs")
		}
		if len(hints) > 0 {
			msg += " \u2014 " + strings.Join(hints, ", ")
		}
		return ErrorStyle.Render(msg)
	}
	if featureHasDisplayFailure(f) && f.Status != feature.StatusFailed && !isRunningStatus(f.Status) {
		msg := "Failed"
		if f.FailureType != "" {
			msg = "Failed (" + formatFailureType(f.FailureType) + ")"
		}
		return ErrorStyle.Render(msg + " — press [r] to restart, [l] logs")
	}
	if f.Status.IsNeedsReview() {
		return WarningStyle.Render(fmt.Sprintf("%s needs review — [a] Review", reviewArtifactLabel(f)))
	}
	switch f.Status {
	case feature.StatusSettingUpWorktrees:
		return lipgloss.NewStyle().Foreground(colorInfo).Render("Setting up worktrees")
	case feature.StatusImplementing:
		var label string
		switch f.ActiveCycleType() {
		case feature.CycleRebase:
			label = fmt.Sprintf("Rebasing (Iteration %d)", f.CurrentIteration)
		case feature.CycleTweak:
			label = fmt.Sprintf("Tweaking (Iteration %d)", f.CurrentIteration)
		case feature.CycleRefactor:
			label = fmt.Sprintf("Refactoring (Iteration %d)", f.CurrentIteration)
		case feature.CycleReviewComments:
			label = fmt.Sprintf("Addressing Reviews (Iteration %d)", f.CurrentIteration)
		default:
			label = fmt.Sprintf("Implementing (Iteration %d)", f.CurrentIteration)
			if f.CurrentRoadmapPhase > 0 && f.TotalRoadmapPhases > 0 {
				label = fmt.Sprintf("Implementing Phase %d/%d (Iteration %d)", f.CurrentRoadmapPhase, f.TotalRoadmapPhases, f.CurrentIteration)
			}
		}
		return lipgloss.NewStyle().Foreground(colorInfo).Render(label)
	case feature.StatusResearching:
		return lipgloss.NewStyle().Foreground(colorInfo).Render("Researching")
	case feature.StatusPlanning:
		var label string
		if f.CurrentRoadmapPhase > 0 {
			label = fmt.Sprintf("Planning Phase %d/%d", f.CurrentRoadmapPhase, f.TotalRoadmapPhases)
		} else {
			label = "Creating Roadmap"
		}
		return lipgloss.NewStyle().Foreground(colorInfo).Render(label)
	case feature.StatusPublished:
		if label, reviewing, ok := activePublishedCycleStatus(f); ok {
			label += " — [a] Watch"
			if hasPendingPerms(f) || hasPendingHelp(f) {
				label += " | waiting input"
			}
			if reviewing {
				return ReviewStyle.Render(label)
			}
			return lipgloss.NewStyle().Foreground(colorInfo).Render(label)
		}
		hint := "\u2713 Published \u2014 [t] tweak  [Shift+F] refactor"
		if f.IsPublishable() {
			hint += "  [b] rebase"
		}
		if len(f.PRURLs()) > 0 && f.IsPublishable() {
			hint += "  [g] reviews"
		}
		hint += "  [Shift+D] mark done"
		return SuccessStyle.Render(hint)
	case feature.StatusDone:
		return SuccessStyle.Render("\u2713 Done")
	case feature.StatusCodeReady:
		if label, reviewing, ok := activePublishedCycleStatus(f); ok {
			label += " \u2014 [a] Watch"
			if hasPendingPerms(f) || hasPendingHelp(f) {
				label += " | waiting input"
			}
			if reviewing {
				return ReviewStyle.Render(label)
			}
			return lipgloss.NewStyle().Foreground(colorInfo).Render(label)
		}
		if f.IsPublishable() && f.Checkpoints.AutoPublish() {
			return lipgloss.NewStyle().Foreground(colorInfo).Render("Publishing...")
		}
		hints := "Code Ready \u2014"
		if f.IsPublishable() {
			hints += " [p] publish  [m] manual publish "
		}
		hints += " [t] tweak  [Shift+F] refactor  [b] rebase"
		if !f.IsPublishable() {
			hints += "  [Shift+M] merge  [Shift+D] done"
		}
		return SuccessStyle.Render(hints)
	case feature.StatusPlanNeedsReview:
		return WarningStyle.Render("Plan needs review \u2014 [a] Review")
	case feature.StatusPromptNeedsReview:
		return WarningStyle.Render("Prompt needs review \u2014 [a] Review")
	case feature.StatusInquiryNeedsReview:
		return WarningStyle.Render("Inquiry needs review \u2014 [a] Review")
	case feature.StatusResearchNeedsReview:
		return WarningStyle.Render("Research needs review \u2014 [a] Review")
	case feature.StatusDesignNeedsReview:
		return WarningStyle.Render("Design needs review \u2014 [a] Review")
	case feature.StatusNeedUserInput:
		return WarningStyle.Render("Implementation needs user input \u2014 [a] Answer")
	case feature.StatusFinalReviewing:
		return ReviewStyle.Render(finalReviewStatusText(f))
	case feature.StatusInterrupted:
		return MutedStyle.Render("Stopped \u2014 press [r] to restart " + f.CurrentPhase.String())
	case feature.StatusFailed:
		msg := "Failed"
		if f.FailureType != "" {
			msg = "Failed (" + formatFailureType(f.FailureType) + ")"
		}
		return ErrorStyle.Render(msg + " \u2014 press [r] to restart, [l] logs")
	default:
		return f.Status.String()
	}
}

func (m DetailModel) renderFailureInfo(f *feature.Feature) string {
	var b strings.Builder

	if f.FailureType != "" {
		b.WriteString(LabelStyle.Render("Type"))
		b.WriteString("  " + ErrorStyle.Render(formatFailureType(f.FailureType)) + "\n")
	}

	if f.LastError != "" {
		b.WriteString(LabelStyle.Render("Error"))
		b.WriteString("  " + f.LastError + "\n")
	}

	// Recovery suggestion based on failure type
	suggestion := recoverySuggestion(f.FailureType)
	if suggestion != "" {
		b.WriteString(LabelStyle.Render("Suggestion"))
		b.WriteString("  " + MutedStyle.Render(suggestion))
	}

	return strings.TrimRight(b.String(), "\n")
}

// renderLightbulbHint returns a bordered lightbulb box for non-moonshot
// features (medium or large), suggesting pipeline upgrade.
func renderLightbulbHint(f *feature.Feature, boxWidth int) string {
	profile := f.EffectivePipeline()
	if profile == feature.PipelineMoonshot {
		return ""
	}
	var upgradePath string
	if profile == feature.PipelineMedium {
		upgradePath = "large or moonshot"
	} else {
		upgradePath = "moonshot"
	}
	text := fmt.Sprintf("This feature uses the %s pipeline.\nUpgrade to %s for more rigorous analysis.", profile, upgradePath)
	text += "\nPress ctrl+r to open Rewind & Upgrade."
	infoStyle := lipgloss.NewStyle().Foreground(colorInfo)
	box := panelStyle(false).
		BorderForeground(colorInfo).
		Width(boxWidth).
		Render(text)
	return renderBorderTitle(box, "💡", infoStyle)
}

// recoverySuggestion returns a recovery hint based on the failure type.
func recoverySuggestion(ft string) string {
	switch ft {
	case feature.FailureSafetyRail:
		return "Agent made no progress. Consider simplifying the task or adding more context."
	case feature.FailureMaxIterations:
		return "Reached max iterations. The task may need to be broken into smaller steps."
	case feature.FailureSessionCrash:
		return "Session crashed. Press [r] to retry or [l] to view logs."
	case feature.FailureMissingArtifact:
		return "A required artifact is missing. Press [ctrl+r] to rewind."
	case feature.FailureProtocolViolation:
		return "The agent ended its turn but did not produce the required artifacts. Press [r] to retry; press [l] to view the session transcript and the contract violation message."
	case feature.FailureInfrastructure:
		return "A system error occurred. Check logs with [l] for details."
	default:
		return "Press [r] to restart the failed phase or [ctrl+r] to rewind."
	}
}

// featureDescLine returns the Summary if available, otherwise falls back to
// a truncated Description. Returns "" if both are empty.
func featureDescLine(f *feature.Feature) string {
	if f.Summary != "" {
		return f.Summary
	}
	if f.Description != "" {
		return truncateText(f.Description, 120)
	}
	return ""
}

// truncateText collapses newlines into spaces and truncates to maxLen with "…".
func truncateText(s string, maxLen int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-1] + "…"
}

func formatDetailRepos(repos []feature.FeatureRepo) string {
	parts := make([]string, len(repos))
	for i, r := range repos {
		if r.WorktreePath != "" {
			parts[i] = fmt.Sprintf("%s \u2192 %s", r.Name, r.Branch)
		} else {
			parts[i] = r.Name
		}
	}
	return strings.Join(parts, ", ")
}

// isRunningStatus mirrors feature.Status.IsRunning() so phase-progress rows
// pick up every actively-executing status. Kept as a thin wrapper so callers
// read uniformly across this file.
func isRunningStatus(s feature.Status) bool {
	return s.IsRunning()
}

// formatValidatorStatuses renders per-validator progress inline.
// Example: "Arch ✓  Sec ✓  Perf ⟳  Test ⟳"
func formatValidatorStatuses(statuses map[string]string) string {
	order := []struct {
		key   string
		short string
	}{
		{"Architecture", "Arch"},
		{"Structural", "Struct"},
		{"Grounding", "Ground"},
		{"Security", "Sec"},
		{"Performance", "Perf"},
		{"Testing", "Test"},
		{"Scope", "Scope"},
	}
	var parts []string
	for _, v := range order {
		s, ok := statuses[v.key]
		if !ok {
			continue
		}
		switch s {
		case validatorStatusApproved:
			parts = append(parts, SuccessStyle.Render(v.short+" \u2713"))
		case validatorStatusChangesRequested:
			parts = append(parts, WarningStyle.Render(v.short+" \u2717"))
		case "error":
			parts = append(parts, ErrorStyle.Render(v.short+" !"))
		default:
			parts = append(parts, MutedStyle.Render(v.short+" \u27F3"))
		}
	}
	return strings.Join(parts, "  ")
}

func formatPhaseStatus(f *feature.Feature) string {
	needsInput := countPendingHelp(f) > 0
	if f.Status == feature.StatusFinalReviewing {
		label := finalReviewSubphaseText(f)
		if needsInput {
			return WarningStyle.Render(label + " | waiting input")
		}
		return ReviewStyle.Render(label)
	}
	if needsInput {
		return WarningStyle.Render("waiting for input")
	}

	switch f.Status {
	case feature.StatusImplementing:
		if f.ReviewingGate {
			return ReviewStyle.Render(fmt.Sprintf("reviewing [%d]", f.CurrentIteration))
		}
		return lipgloss.NewStyle().Foreground(colorInfo).Render(
			fmt.Sprintf("iteration %d", f.CurrentIteration))
	case feature.StatusInquiring:
		return lipgloss.NewStyle().Foreground(colorInfo).Render("running")
	case feature.StatusResearching:
		return lipgloss.NewStyle().Foreground(colorInfo).Render("running")
	case feature.StatusDesigning:
		return lipgloss.NewStyle().Foreground(colorInfo).Render("running")
	case feature.StatusPlanning:
		if f.ValidatingPlan && len(f.ValidatorStatuses) > 0 {
			return ReviewStyle.Render("validating: ") + formatValidatorStatuses(f.ValidatorStatuses)
		}
		if f.ValidatingPlan {
			return ReviewStyle.Render("validating plan")
		}
		if f.PlanIteration > 1 {
			return lipgloss.NewStyle().Foreground(colorInfo).Render(
				fmt.Sprintf("iteration %d", f.PlanIteration))
		}
		return lipgloss.NewStyle().Foreground(colorInfo).Render("running")
	case feature.StatusPlanNeedsReview, feature.StatusPromptNeedsReview,
		feature.StatusInquiryNeedsReview, feature.StatusResearchNeedsReview,
		feature.StatusDesignNeedsReview:
		return WarningStyle.Render("needs review")
	case feature.StatusInterrupted:
		return WarningStyle.Render("interrupted — press [r] to restart")
	case feature.StatusFailed:
		return ErrorStyle.Render("failed")
	default:
		return f.Status.String()
	}
}
