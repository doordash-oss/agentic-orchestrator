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
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

type DetailModel struct {
	feature        *feature.Feature
	stateDir       string // base state dir for computing artifact paths
	spinnerView    string // set by parent from app-level spinner
	contextBox     contextBox
	kbStaleWarning string // yellow warning text when KB is outdated or missing
	width          int
	height         int

	// Refactor overlay — set by parent (AppModel) when refactor input is active.
	// When refactorActive is true, View/ViewCompact render a refactor input panel
	// instead of the normal detail content.
	refactorActive      bool
	refactorInputView   string // pre-rendered textarea.View() output
	refactorFeatureName string

	// Refactor pipeline selector — set by parent (AppModel) after refactor
	// prompt is submitted. Shows pipeline selection overlay.
	refactorPipelineActive bool
	refactorPipelineView   string // pre-rendered pipeline selector output
}

func NewDetailModel(f *feature.Feature, stateDir string) DetailModel {
	return DetailModel{feature: f, stateDir: stateDir, contextBox: contextBox{mainFill: -1}}
}

func (m DetailModel) Init() tea.Cmd {
	return nil
}

func (m DetailModel) Update(msg tea.Msg) (DetailModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		_ = msg // handled by parent
	}
	return m, nil
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

// featureHasFailedRepos returns true if the feature has any repos in failed
// state. Used by the detail view's footer and the retry-phase handler.
func featureHasFailedRepos(f *feature.Feature) bool {
	if f == nil {
		return false
	}
	for _, state := range f.RepoStates {
		if state != nil && state.LastError != "" {
			return true
		}
	}
	return false
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

func (m DetailModel) View() string {
	if m.feature == nil {
		return "No feature selected\n"
	}

	f := m.feature
	w := m.width
	if w < 40 {
		w = 80
	}

	// When refactor input is active, show the refactor overlay with fill-lines
	// below content to occupy remaining height.
	if m.refactorActive {
		boxWidth := min(w-2, 78)
		content := m.renderRefactorOverlay(boxWidth)

		// Pin content to top using fill-lines
		contentLines := strings.Count(content, "\n")
		fillLines := m.height - contentLines - 2
		var sb strings.Builder
		sb.WriteString(content)
		for i := 0; i < max(fillLines, 0); i++ {
			sb.WriteString("\n")
		}
		return sb.String()
	}

	// When refactor pipeline selector is active, show the selector overlay.
	if m.refactorPipelineActive {
		content := m.refactorPipelineView
		contentLines := strings.Count(content, "\n")
		fillLines := m.height - contentLines - 2
		var sb strings.Builder
		sb.WriteString(content)
		for i := 0; i < max(fillLines, 0); i++ {
			sb.WriteString("\n")
		}
		return sb.String()
	}

	var b strings.Builder

	// Header
	b.WriteString(TitleStyle.Render(fmt.Sprintf(" %s", f.Slug)))

	pendingHelp := countPendingHelp(f)
	if pendingHelp > 0 {
		b.WriteString("  " + WarningStyle.Render("\u26a0 waiting for input"))
	}
	b.WriteString("\n\n")

	boxWidth := min(w-2, 78)

	// Metadata box
	metaContent := m.renderMetadataFull(f)
	metaBox := panelStyle(false).Width(boxWidth).Render(metaContent)
	metaBox = renderBorderTitle(metaBox, "Info", MutedStyle)
	b.WriteString(" " + metaBox + "\n")

	// Phase progress box
	phaseContent := m.renderPhaseProgressFull(f)
	phaseBox := panelStyle(false).Width(boxWidth).Render(phaseContent)
	phaseBox = renderBorderTitle(phaseBox, "Phase Progress", MutedStyle)
	b.WriteString(" " + phaseBox + "\n")

	// Failure info box.
	if featureHasDisplayFailure(f) {
		failureContent := m.renderFailureInfo(f)
		failureBox := panelStyle(false).
			BorderForeground(colorError).
			Width(boxWidth).
			Render(failureContent)
		failureBox = renderBorderTitle(failureBox, "Failure Info", ErrorStyle)
		b.WriteString(" " + failureBox + "\n")
	}

	if hint := renderLightbulbHint(f, boxWidth); hint != "" {
		b.WriteString(" " + hint + "\n")
	}

	// Needs-review banner — prominent call-to-action when an artifact awaits review.
	if banner := needsReviewBanner(f); banner != "" {
		reviewBox := panelStyle(false).
			BorderForeground(colorWarning).
			Width(boxWidth).
			Render(banner)
		reviewBox = renderBorderTitle(reviewBox, "Needs Review", WarningStyle)
		b.WriteString(" " + reviewBox + "\n")
	}

	// Help queue
	if pendingHelp > 0 {
		attentionContent := m.renderAttention(f)
		attentionBox := panelStyle(false).
			BorderForeground(colorWarning).
			Width(boxWidth).
			Render(attentionContent)
		attentionBox = renderBorderTitle(attentionBox, "Attention", WarningStyle)
		b.WriteString(" " + attentionBox + "\n")
	}

	// Footer (pinned to bottom)
	var actionParts []string
	var leadHint string
	if actionHint, lead := contextualAActionHint(f); actionHint != "" {
		if lead {
			leadHint = WarningStyle.Bold(true).Render(actionHint)
		} else {
			actionParts = append(actionParts, actionHint)
		}
	}
	// Phase retry action — only when the feature is quiescent (Failed/Interrupted)
	// so we don't kill unrelated in-flight repo sessions.
	if (f.Status == feature.StatusFailed || f.Status == feature.StatusInterrupted) &&
		featureHasFailedRepos(f) {
		actionParts = append(actionParts, "[Shift+R] Retry phase")
	}
	if isRunningFeature(f) {
		actionParts = append(actionParts, "[s] Stop")
	} else {
		actionParts = append(actionParts, "[r] Restart")
	}
	actionParts = append(actionParts, "[ctrl+r] Rewind")
	if f.Status == feature.StatusFailed || f.Status == feature.StatusInterrupted || featureHasDisplayFailure(f) {
		actionParts = append(actionParts, "[l] Logs")
	}
	if f.Status == feature.StatusCodeReady && len(f.Repos) > 0 && f.Repos[0].WorktreePath != "" {
		actionParts = append(actionParts, "[v] Diff")
	}
	if f.Status == feature.StatusCodeReady && !f.Checkpoints.AutoPublish() && f.IsPublishable() {
		actionParts = append(actionParts, "[p] Publish", "[m] Manual publish")
	}
	if f.Status == feature.StatusPublished || (f.Status == feature.StatusCodeReady && !f.Checkpoints.AutoPublish()) {
		actionParts = append(actionParts, "[t] Tweak", "[Shift+F] Refactor", "[b] Rebase")
	}
	if f.Status == feature.StatusCodeReady && !f.IsPublishable() {
		actionParts = append(actionParts, "[Shift+M] Merge to base", "[Shift+D] Mark done")
	}
	if f.Status == feature.StatusPublished {
		actionParts = append(actionParts, "[Shift+D] Mark done")
		if len(f.PRURLs()) > 0 && f.IsPublishable() {
			actionParts = append(actionParts, "[g] Reviews")
		}
	}
	if (f.Status == feature.StatusPublished || f.Status == feature.StatusDone) && len(f.Repos) > 0 && f.Repos[0].WorktreePath != "" {
		actionParts = append(actionParts, "[c] Clean worktree")
	}
	if isFeatureQuiescent(f) {
		actionParts = append(actionParts, "[e] Edit config")
	}
	actionParts = append(actionParts, "[Shift+N] Input alerts", "[d] Delete", "[esc] Back")
	actions := " " + strings.Join(actionParts, "  ")
	brandHintStyle := lipgloss.NewStyle().Foreground(colorBrand).Bold(true)
	helpHint := brandHintStyle.Render("[" + HelpKeyHint() + "] Help")
	footer := KeyHelpStyle.Render(actions) + "  " + helpHint
	if leadHint != "" {
		// Render actions without MarginTop so leadHint and actions share one line;
		// add the margin newline manually so the footer still sits one row down.
		plainHelpStyle := lipgloss.NewStyle().Foreground(colorOverlay)
		footer = "\n " + leadHint + plainHelpStyle.Render(actions) + "  " + helpHint
	}

	// Count content lines and fill to push footer to bottom
	contentStr := b.String()
	contentLines := strings.Count(contentStr, "\n")
	fillLines := m.height - contentLines - 2
	for i := 0; i < max(fillLines, 1); i++ {
		b.WriteString("\n")
	}
	b.WriteString(footer + "\n")

	return b.String()
}

func (m DetailModel) renderMetadataFull(f *feature.Feature) string {
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
	b.WriteString("  " + MutedStyle.Render(fmt.Sprintf("R:%s  P:%s  I:%s  Rev:%s  KB:%s",
		f.Models.Research, f.Models.Planning, f.Models.Implementation, f.Models.Review, f.Models.KBBuild)) + "\n")
	b.WriteString(LabelStyle.Render("Input Alerts"))
	b.WriteString("  " + MutedStyle.Render(inputAlertModeLabel(f)) + "\n")
	if f.RiskLevel != "" {
		b.WriteString(LabelStyle.Render("Risk Level"))
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
		b.WriteString(LabelStyle.Render("Cost"))
		b.WriteString("    " + MutedStyle.Render(formatCost(totalCost)) + "\n")
	}
	if line := formatDeferralLine(f); line != "" {
		b.WriteString(LabelStyle.Render("Deferrals"))
		b.WriteString("  " + line + "\n")
	}
	return b.String()
}

// formatDeferralLine renders a one-line summary of the run's cross-phase
// deferral ledger: total open entries, next-due phase, and a chronic-slip
// flag when any entry has been re-deferred ≥ 2 times. Returns "" when the
// ledger is empty.
func formatDeferralLine(f *feature.Feature) string {
	run := f.Run()
	if run == nil || len(run.Deferrals) == 0 {
		return ""
	}
	open := feature.OpenDeferrals(run.Deferrals)
	if len(open) == 0 {
		return MutedStyle.Render(fmt.Sprintf("0 open (%d closed)", len(run.Deferrals)))
	}
	var parts []string
	parts = append(parts, fmt.Sprintf("%d open", len(open)))
	// Next due phase — the smallest DueByPhase across open entries.
	nextDue := open[0].DueByPhase
	parts = append(parts, fmt.Sprintf("next due: phase %d", nextDue))
	// Chronic slippage flag.
	chronic := 0
	for _, d := range open {
		if d.RedeferralCount() >= 2 {
			chronic++
		}
	}
	line := MutedStyle.Render(strings.Join(parts, " • "))
	if chronic > 0 {
		line += "  " + WarningStyle.Render(fmt.Sprintf("⚠ %d chronic", chronic))
	}
	return line
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

// formatContextUsage renders the inline one-line context summary appended to
// the detail/overview phase rows: main-agent fill over the model WINDOW with a
// percent, colored like the live-preview Context box's main number (red at/above
// 80% of the window, yellow at/above the Smart Zone threshold, else neutral).
// Window-relative \u2014 not Smart-Zone-relative \u2014 so it matches the Context box and
// never reads as "100%" merely because the Smart Zone threshold was reached.
func formatContextUsage(box contextBox) string {
	if !box.mainAvailable() {
		return MutedStyle.Render(" context: calculating\u2026")
	}
	text := fmt.Sprintf(" %s / %s (%d%%)", formatTokenK(box.mainFill), formatTokenK(box.window), box.mainPct())
	return box.mainStyle().Render(text)
}

func (m DetailModel) renderPhaseProgressFull(f *feature.Feature) string {
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

		// Phase duration and cost
		timing := ""
		if p.timerKey != "" {
			d := f.PhaseRuntime(p.timerKey)
			timing = formatPhaseDuration(d)
			if c := f.PhaseCost(p.timerKey); c > 0 {
				timing += MutedStyle.Render(" " + formatCost(c))
			}
		}

		// Smart Zone context usage (active phase only, full-screen view)
		if current && isRunningStatus(f.Status) {
			timing += formatContextUsage(m.contextBox)
		}

		nameStr := p.name
		dots := MutedStyle.Render(" " + strings.Repeat("\u00b7", 8) + " ")

		connector := MutedStyle.Render("  \u2502")
		if i == len(phases)-1 {
			connector = ""
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

		b.WriteString(fmt.Sprintf("  %s %s%s%s%s%s\n", icon, nameStr, dots, status, timing, kbWarning))

		// Render per-repo KB sub-items for multi-repo features
		if p.phase == feature.PhaseKnowledgeBase && (current || done) && len(f.Repos) > 1 && len(f.KBStatus) > 0 {
			for _, repo := range f.Repos {
				kbStatus, ok := f.KBStatus[repo.Name]
				if !ok {
					kbStatus = "pending"
				}
				b.WriteString(fmt.Sprintf("  %s  \u21b3 %s %s\n",
					MutedStyle.Render(" "),
					MutedStyle.Render(repo.Name),
					kbStatusIcon(kbStatus)))
			}
		}

		// Render roadmap phase sub-items under Planning
		if p.phase == feature.PhasePlan && (current || (done && f.TotalRoadmapPhases > 0)) {
			renderRoadmapPlanSubItems(&b, f)
		}

		// Render roadmap phase sub-items under Implement
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
					cycleTiming += formatContextUsage(m.contextBox)
				}
				// Post-publish cycles (rebase/tweak/refactor/review-comments) keep
				// the feature at StatusPublished/StatusCodeReady while the cycle
				// loop is mid-flight, so isRunningStatus(f.Status) is false even
				// when the cycle is actively running. Trust the cycle's own
				// running flag (set inside cycleGroup via ActiveTimingKey or
				// ActiveCycle) so the in-flight row still renders "in progress".
				switch {
				case g.active:
					b.WriteString(fmt.Sprintf("  %s  \u21b3 %s %s%s\n",
						MutedStyle.Render(" "),
						m.activeProgressIcon()+" "+MutedStyle.Render(g.label),
						lipgloss.NewStyle().Foreground(colorInfo).Render("in progress"),
						cycleTiming))
				case g.interrupted:
					b.WriteString(fmt.Sprintf("  %s  \u21b3 %s %s%s\n",
						MutedStyle.Render(" "),
						MutedStyle.Render("\u23f8")+" "+MutedStyle.Render(g.label),
						WarningStyle.Render("interrupted"),
						cycleTiming))
				default:
					b.WriteString(fmt.Sprintf("  %s  \u21b3 %s%s\n",
						MutedStyle.Render(" "),
						MutedStyle.Render(g.label),
						cycleTiming))
				}
			}
		}

		if connector != "" {
			b.WriteString(connector + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
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

	pendingHelp := countPendingHelp(f)
	if pendingHelp > 0 {
		b.WriteString(WarningStyle.Render("\u26a0 waiting for input"))
		b.WriteString("\n")
	}

	// Metadata
	metaContent := m.renderMetadataCompact(f)
	metaBox := panelStyle(false).Width(width - 4).Render(metaContent)
	metaBox = renderBorderTitle(metaBox, "Info", MutedStyle)
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

	// Help queue
	if pendingHelp > 0 {
		attentionContent := m.renderAttention(f)
		attentionBox := panelStyle(false).
			BorderForeground(colorWarning).
			Width(width - 4).
			Render(attentionContent)
		attentionBox = renderBorderTitle(attentionBox, "Attention", WarningStyle)
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
	b.WriteString("  " + MutedStyle.Render(fmt.Sprintf("R:%s P:%s I:%s Rev:%s KB:%s",
		f.Models.Research, f.Models.Planning, f.Models.Implementation, f.Models.Review, f.Models.KBBuild)) + "\n")
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
		b.WriteString(LabelStyle.Render("Cost"))
		b.WriteString("    " + MutedStyle.Render(formatCost(totalCost)) + "\n")
	}
	return b.String()
}

func (m DetailModel) renderPhaseProgress(f *feature.Feature) string {
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

		// Phase duration and cost
		timing := ""
		if p.timerKey != "" {
			d := f.PhaseRuntime(p.timerKey)
			timing = formatPhaseDuration(d)
			if c := f.PhaseCost(p.timerKey); c > 0 {
				timing += MutedStyle.Render(" " + formatCost(c))
			}
		}

		// Smart Zone context usage (active phase only)
		if current && isRunningStatus(f.Status) {
			timing += formatContextUsage(m.contextBox)
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
					cycleTiming += formatContextUsage(m.contextBox)
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

// renderRoadmapPlanSubItems renders "Roadmap" and "Phase N Plan" sub-items
// under the Planning phase row in the full-screen detail view.
func renderRoadmapPlanSubItems(b *strings.Builder, f *feature.Feature) {
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
	// Roadmap timing from "plan" key (roadmap creation phase)
	roadmapTiming := roadmapPhaseTiming(f, "plan")
	b.WriteString(fmt.Sprintf("  %s  \u21b3 %s %s%s\n",
		MutedStyle.Render(" "), roadmapIcon+" "+MutedStyle.Render("Roadmap"), roadmapStatus, roadmapTiming))

	// Per-phase plan sub-items
	for i := 1; i <= f.TotalRoadmapPhases; i++ {
		label := fmt.Sprintf("Phase %d Plan", i)
		timingKey := fmt.Sprintf("phase-%d-plan", i)
		timing := roadmapPhaseTiming(f, timingKey)
		if i < f.CurrentRoadmapPhase {
			b.WriteString(fmt.Sprintf("  %s  \u21b3 %s %s%s\n",
				MutedStyle.Render(" "), SuccessStyle.Render("\u2713")+" "+MutedStyle.Render(label), SuccessStyle.Render("complete"), timing))
		} else if i == f.CurrentRoadmapPhase && f.Status == feature.StatusPlanNeedsReview {
			b.WriteString(fmt.Sprintf("  %s  \u21b3 %s %s%s\n",
				MutedStyle.Render(" "), WarningStyle.Render("\u27F3")+" "+MutedStyle.Render(label), WarningStyle.Render("needs review"), timing))
		} else if i == f.CurrentRoadmapPhase && f.CurrentPhase == feature.PhasePlan {
			b.WriteString(fmt.Sprintf("  %s  \u21b3 %s %s%s\n",
				MutedStyle.Render(" "), MutedStyle.Render("\u27F3")+" "+MutedStyle.Render(label), lipgloss.NewStyle().Foreground(colorInfo).Render("in progress"), timing))
		} else if i == f.CurrentRoadmapPhase {
			b.WriteString(fmt.Sprintf("  %s  \u21b3 %s %s%s\n",
				MutedStyle.Render(" "), SuccessStyle.Render("\u2713")+" "+MutedStyle.Render(label), SuccessStyle.Render("complete"), timing))
		}
	}
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

func (m DetailModel) renderAttention(f *feature.Feature) string {
	if f.Status.IsNeedsReview() {
		return ""
	}
	var b strings.Builder
	for _, h := range f.HelpQueue {
		if h.Pending {
			b.WriteString(WarningStyle.Render("  \u25b8 "))
			b.WriteString(normalizeManagedHelpQuestion(h.Question))
			b.WriteString("\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
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
	switch f.Status {
	case feature.StatusPlanNeedsReview:
		if f.CurrentRoadmapPhase == 0 {
			return WarningStyle.Bold(true).Render("Roadmap")
		}
		if f.TotalRoadmapPhases > 1 {
			return WarningStyle.Bold(true).Render(fmt.Sprintf("Phase %d plan", f.CurrentRoadmapPhase))
		}
		return WarningStyle.Bold(true).Render("Plan")
	case feature.StatusPromptNeedsReview:
		return WarningStyle.Bold(true).Render("Prompt")
	case feature.StatusInquiryNeedsReview:
		return WarningStyle.Bold(true).Render("Inquiry")
	case feature.StatusResearchNeedsReview:
		return WarningStyle.Bold(true).Render("Research")
	case feature.StatusDesignNeedsReview:
		return WarningStyle.Bold(true).Render("Design")
	default:
		return WarningStyle.Bold(true).Render("Artifact")
	}
}

func (m DetailModel) FeatureID() string {
	if m.feature != nil {
		return m.feature.ID
	}
	return ""
}

func (m DetailModel) HasAction(msg tea.KeyPressMsg) bool {
	return key.Matches(msg, keys.Attach) ||
		key.Matches(msg, keys.Approve) ||
		key.Matches(msg, keys.Help) ||
		key.Matches(msg, keys.ToggleInputNotify) ||
		key.Matches(msg, keys.Publish) ||
		key.Matches(msg, keys.ManualPublish) ||
		key.Matches(msg, keys.Rewind) ||
		key.Matches(msg, keys.Delete) ||
		key.Matches(msg, keys.ViewDiff) ||
		key.Matches(msg, keys.CleanWorktree) ||
		key.Matches(msg, keys.Rebase) ||
		key.Matches(msg, keys.Tweak) ||
		key.Matches(msg, keys.Refactor) ||
		key.Matches(msg, keys.MarkDone) ||
		key.Matches(msg, keys.ReviewComments) ||
		key.Matches(msg, keys.ViewLogs) ||
		key.Matches(msg, keys.RetryPhase) ||
		key.Matches(msg, keys.Back) ||
		key.Matches(msg, keys.PanelLeft)
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
	if featureHasDisplayFailure(f) && f.Status != feature.StatusFailed && !isRunningStatus(f.Status) {
		msg := "Failed"
		if f.FailureType != "" {
			msg = "Failed (" + formatFailureType(f.FailureType) + ")"
		}
		return ErrorStyle.Render(msg + " — press [r] to restart, [l] logs")
	}
	switch f.Status {
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
		case "APPROVED":
			parts = append(parts, SuccessStyle.Render(v.short+" \u2713"))
		case "CHANGES_REQUESTED":
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
