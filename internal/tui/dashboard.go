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
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// version is set at build time via ldflags:
//
//	-X github.com/doordash-oss/agentic-orchestrator/internal/tui.version=1.2.3
var version string

// GetVersion returns the application version, resolving in order:
//  1. Build-time ldflags (goreleaser, make build)
//  2. Go module info (go install ...@v1.2.3)
//  3. "dev" fallback (local go run / untagged build)
func GetVersion() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		v := info.Main.Version
		if v != "" && v != "(devel)" {
			return strings.TrimPrefix(v, "v")
		}
	}
	return "dev"
}

// listItemKind distinguishes section headers from feature rows in the virtual list.
type listItemKind int

const (
	listItemSectionHeader listItemKind = iota
	listItemFeature
	listItemGhostCTA // empty-state call-to-action
)

// listItem represents a single navigable entry in the feature list (section header or feature).
type listItem struct {
	kind    listItemKind
	feature *feature.Feature // non-nil for feature items
	section string           // "inProgress", "published", "completed"
}

type DashboardModel struct {
	features             []*feature.Feature
	cursor               int
	focusPanel           int // 0=list, 1=detail
	preview              DetailModel
	livePreview          LivePreviewModel
	spinnerView          string // set by parent from app-level spinner
	width                int
	height               int
	stateDir             string
	creatingName         string          // non-empty while a feature is being created (worktree setup)
	dangerouslySkipPerms bool            // true when --dangerously-skip-permissions is active
	collapsedSections    map[string]bool // section-level collapse state ("published", "completed")
	visibleItems         []listItem      // computed list of navigable items
	scrollOffset         int             // first visible line in the feature list panel
	cursorLine           int             // line index of the cursor's item (computed during Update)
	panelHeight          int             // last known panel height for scroll computation
	statusMessage        string          // transient status message displayed in footer
	rewindingFeatureID   string          // feature ID currently being rewound (shows "Stopping..." label)
	indexingInProgress   bool            // true while classifier is initializing
	indexingDone         int             // repos indexed so far
	indexingTotal        int             // total repos to index
	wantNewFeature       bool            // set when ghost CTA Enter is pressed
	welcomeSkipped       bool            // true when user skipped the welcome flow
}

func NewDashboardModel(features []*feature.Feature, stateDir string) DashboardModel {
	sortFeatures(features)
	m := DashboardModel{
		features:          features,
		stateDir:          stateDir,
		collapsedSections: make(map[string]bool),
	}
	m.buildVisibleItems()
	// Create the preview once — its spinner ID must stay stable for tick chain
	var firstFeature *feature.Feature
	if len(m.visibleItems) > 0 && m.visibleItems[0].kind == listItemFeature {
		firstFeature = m.visibleItems[0].feature
	}
	m.preview = NewDetailModel(firstFeature, stateDir)
	m.livePreview = newLivePreviewModel(firstFeature)
	return m
}

// ConsumeWantNewFeature returns true once if Enter was pressed on the ghost CTA,
// then resets the flag. The caller (app.go) uses this to trigger the wizard.
func (m *DashboardModel) ConsumeWantNewFeature() bool {
	if m.wantNewFeature {
		m.wantNewFeature = false
		return true
	}
	return false
}

func (m DashboardModel) Init() tea.Cmd {
	return nil
}

func (m DashboardModel) Update(msg tea.Msg) (DashboardModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, keys.Tab):
			if len(m.features) > 0 {
				m.focusPanel = (m.focusPanel + 1) % 2
			}
		case key.Matches(msg, keys.Enter):
			if m.focusPanel == 0 && len(m.visibleItems) > 0 && m.cursor < len(m.visibleItems) {
				item := m.visibleItems[m.cursor]
				switch item.kind {
				case listItemSectionHeader:
					// IN PROGRESS section cannot be collapsed
					if item.section != "inProgress" {
						m.collapsedSections[item.section] = !m.collapsedSections[item.section]
						m.buildVisibleItems()
						if m.cursor >= len(m.visibleItems) {
							m.cursor = max(0, len(m.visibleItems)-1)
							m.computeCursorLine()
						}
						m.updateScrollState(0)
						return m, nil
					}
				case listItemFeature:
					// Focus right panel
					m.focusPanel = 1
				case listItemGhostCTA:
					m.wantNewFeature = true
				}
			}
		case key.Matches(msg, keys.Back), key.Matches(msg, keys.PanelLeft):
			// Esc or Left arrow returns focus to the left panel
			if m.focusPanel == 1 {
				m.focusPanel = 0
			}
		case key.Matches(msg, keys.PanelRight):
			// Right arrow focuses the right panel (same as Enter on a feature)
			if m.focusPanel == 0 && m.SelectedFeature() != nil {
				m.focusPanel = 1
			}
		case key.Matches(msg, keys.Up):
			if m.focusPanel == 0 && m.cursor > 0 {
				m.cursor--
				m.computeCursorLine()
				m.updateScrollState(0)
				m.syncPreview()
			}
		case key.Matches(msg, keys.Down):
			if m.focusPanel == 0 && m.cursor < len(m.visibleItems)-1 {
				m.cursor++
				m.computeCursorLine()
				m.updateScrollState(0)
				m.syncPreview()
			}
		}
	}
	return m, nil
}

// MoveToAdjacentFeature moves the cursor to the nearest feature in the given
// direction (delta = -1 for up, +1 for down), skipping section headers and the
// ghost CTA. Used when navigating from the right panel, where only feature
// selection is meaningful (landing on a header would unfocus the right panel).
func (m *DashboardModel) MoveToAdjacentFeature(delta int) {
	if delta == 0 || len(m.visibleItems) == 0 {
		return
	}
	for i := m.cursor + delta; i >= 0 && i < len(m.visibleItems); i += delta {
		if m.visibleItems[i].kind == listItemFeature {
			m.cursor = i
			m.computeCursorLine()
			m.updateScrollState(0)
			m.syncPreview()
			return
		}
	}
}

// syncPreview updates the right panel's preview model to the currently selected feature.
func (m *DashboardModel) syncPreview() {
	f := m.SelectedFeature()
	if f == nil {
		m.preview = NewDetailModel(nil, m.stateDir)
		m.preview.width = m.width
		m.preview.height = m.height
		m.livePreview = newLivePreviewModel(nil)
		m.livePreview.width = m.width
		m.livePreview.height = m.height
		m.focusPanel = 0
		return
	}
	m.preview = NewDetailModel(f, m.stateDir)
	m.preview.width = m.width
	m.preview.height = m.height
	m.livePreview = newLivePreviewModel(f)
	m.livePreview.width = m.width
	m.livePreview.height = m.height
}

type layoutMode int

const (
	layoutNarrow   layoutMode = iota // < 80 cols: single panel
	layoutStandard                   // 80-120 cols: split panels 35/65
	layoutWide                       // > 120 cols: split panels 30/70
)

// effectivePanelHeight computes the content area height for the feature list panel
// using the same formula as View(). This allows scroll state to be pre-computed during Update.
func (m DashboardModel) effectivePanelHeight() int {
	w := m.width
	h := m.height
	if w < 40 {
		w = 80
	}
	if h < 10 {
		h = 24
	}

	header := m.renderHeader(w)
	footer := m.renderFooter()
	headerH := lipgloss.Height(header)
	footerH := lipgloss.Height(footer) + 2
	panelHeight := h - headerH - footerH
	if panelHeight < 6 {
		panelHeight = 6
	}
	return panelHeight
}

func (m DashboardModel) getLayoutMode() layoutMode {
	switch {
	case m.width < 80:
		return layoutNarrow
	case m.width > 120:
		return layoutWide
	default:
		return layoutStandard
	}
}

func (m DashboardModel) View() string {
	w := m.width
	h := m.height
	if w < 40 {
		w = 80
	}
	if h < 10 {
		h = 24
	}

	// Header — styled brand bar
	header := m.renderHeader(w)

	// Footer — context-sensitive
	footer := m.renderFooter()

	// Measure actual rendered heights to avoid hard-coded line counts
	headerH := lipgloss.Height(header)
	footerH := lipgloss.Height(footer) + 2
	panelHeight := h - headerH - footerH
	if panelHeight < 6 {
		panelHeight = 6
	}

	mode := m.getLayoutMode()

	// Narrow mode: single panel only
	if mode == layoutNarrow {
		leftContent := m.scrollFeatureList(panelHeight)
		leftBox := panelStyle(true).
			Width(w).
			Height(panelHeight + 2).
			Render(leftContent)
		leftBox = renderBorderTitle(leftBox, "Features", lipgloss.NewStyle().Foreground(colorBrand))
		return header + leftBox + "\n" + footer + "\n"
	}

	// Split mode: calculate widths based on layout
	var leftPct int
	if mode == layoutWide {
		leftPct = 30
	} else {
		leftPct = 35
	}

	leftWidth := w * leftPct / 100
	rightWidth := w - leftWidth
	if leftWidth < 20 {
		leftWidth = 20
	}
	if rightWidth < 20 {
		rightWidth = 20
	}

	// Build left panel
	leftContent := m.scrollFeatureList(panelHeight)
	if m.focusPanel == 1 {
		leftContent = dimContent(leftContent)
	}
	leftBox := panelStyle(m.focusPanel == 0).
		Width(leftWidth).
		Height(panelHeight + 2).
		Render(leftContent)
	leftTitleStyle := lipgloss.NewStyle().Foreground(colorBrand)
	if m.focusPanel == 1 {
		leftTitleStyle = lipgloss.NewStyle().Foreground(colorOverlay)
	}
	leftBox = renderBorderTitle(leftBox, "Features", leftTitleStyle)

	// Build right panel
	var rightContent string
	var rightTitle string
	if len(m.features) == 0 && m.creatingName == "" {
		rightContent = m.renderWelcomePanel(rightWidth)
		rightTitle = "Welcome"
	} else {
		if m.preview.refactorActive || m.preview.refactorPipelineActive || !isLivePreviewEligible(m.SelectedFeature()) {
			rightContent = m.preview.ViewCompact(rightWidth)
		} else {
			livePreview := m.livePreview
			livePreview.spinnerView = m.spinnerView
			livePreview.height = panelHeight
			rightContent = livePreview.ViewCompact(rightWidth)
		}
		rightTitle = "Detail"
		if f := m.SelectedFeature(); f != nil {
			rightTitle = f.Slug
		}
	}
	if m.focusPanel == 0 {
		rightContent = dimContent(rightContent)
	}
	// Add back-arrow breadcrumb when right panel is focused
	if m.focusPanel == 1 {
		rightTitle = "\u25c2 " + rightTitle
	}
	// Truncate content to fit within the panel's inner height so that the
	// bordered box (top + bottom border) never exceeds panelHeight+2 lines.
	rightContent = truncateLines(rightContent, panelHeight)
	rightBox := panelStyle(m.focusPanel == 1).
		Width(rightWidth).
		Height(panelHeight + 2).
		Render(rightContent)
	rightTitleStyle := lipgloss.NewStyle().Foreground(colorBrand)
	if m.focusPanel == 0 {
		rightTitleStyle = lipgloss.NewStyle().Foreground(colorOverlay)
	}
	rightBox = renderBorderTitle(rightBox, rightTitle, rightTitleStyle)

	panels := lipgloss.JoinHorizontal(lipgloss.Top, leftBox, rightBox)

	return header + panels + "\n" + footer + "\n"
}

// renderHeader renders a branded block-element header inspired by K9s.
// In DSP mode the header switches to a red-on-black theme with a skull motif.
func (m DashboardModel) renderHeader(w int) string {
	artLines := []string{
		" \u2584\u2580\u2588 \u2588\u2580\u2580 \u2588\u2580\u2580 \u2588\u2584\u2591\u2588 \u2580\u2588\u2580 \u2588 \u2588\u2580\u2580 \u2588\u2580\u2588",
		" \u2588\u2580\u2588 \u2588\u2584\u2588 \u2588\u2588\u2584 \u2588\u2591\u2580\u2588 \u2591\u2588\u2591 \u2588 \u2588\u2584\u2584 \u2588\u2584\u2588",
	}

	brandStyle := lipgloss.NewStyle().Foreground(colorBrand).Bold(true)
	dimStyle := lipgloss.NewStyle().Foreground(colorSurface)
	infoColor := colorOverlay

	// DSP skull art (2 lines, aligned with logo height)
	skullLines := [2]string{"", ""}

	if m.dangerouslySkipPerms {
		brandStyle = lipgloss.NewStyle().Foreground(colorDSPRed).Background(colorDSPBlack).Bold(true)
		dimStyle = lipgloss.NewStyle().Foreground(colorDSPDimRed)
		infoColor = colorDSPDimRed
		skullStyle := lipgloss.NewStyle().Foreground(colorDSPRed)
		skullLines = [2]string{
			skullStyle.Render(" \u2620"),
			skullStyle.Render("  "),
		}
	}

	info := make([]string, len(artLines))
	info[0] = lipgloss.NewStyle().Foreground(infoColor).Render("Orchestrator v" + GetVersion())
	if m.indexingInProgress {
		indexStyle := lipgloss.NewStyle().Foreground(colorPeach)
		info[0] += "  " + indexStyle.Render(fmt.Sprintf("Indexing repos (%d/%d)...", m.indexingDone, m.indexingTotal))
	}

	var contextParts []string
	if len(m.features) > 0 {
		activeCount, publishedCount, completedCount := 0, 0, 0
		for _, f := range m.features {
			switch f.Status {
			case feature.StatusPublished:
				publishedCount++
			case feature.StatusDone:
				completedCount++
			default:
				activeCount++
			}
		}
		featureText := fmt.Sprintf("%d active", activeCount)
		if publishedCount > 0 {
			featureText += fmt.Sprintf(", %d published", publishedCount)
		}
		if completedCount > 0 {
			featureText += fmt.Sprintf(", %d completed", completedCount)
		}
		contextParts = append(contextParts,
			lipgloss.NewStyle().Foreground(infoColor).Render(featureText))
	}
	needAttention := m.countNeedAttention()
	if needAttention > 0 {
		contextParts = append(contextParts,
			WarningStyle.Render(fmt.Sprintf("\u26a0 %d need attention", needAttention)))
	}
	if m.dangerouslySkipPerms {
		dspBadge := lipgloss.NewStyle().Foreground(colorDSPRed).Bold(true).Render("\u26a0 DSP")
		contextParts = append(contextParts, dspBadge)
	}
	if len(contextParts) > 0 {
		info[1] = strings.Join(contextParts, "  ")
	}

	var header strings.Builder
	for i, line := range artLines {
		artRendered := brandStyle.Render(line) + skullLines[i]
		artW := lipgloss.Width(artRendered)

		infoStr := info[i]
		infoW := lipgloss.Width(infoStr)

		gap := w - artW - infoW - 1
		if gap < 1 {
			gap = 1
		}

		header.WriteString(artRendered + strings.Repeat(" ", gap) + infoStr + "\n")
	}

	header.WriteString(dimStyle.Render(strings.Repeat("\u2500", w)) + "\n")

	return header.String()
}

// renderFooter returns context-sensitive key hints based on focus and selection.
// The [/] Ask and [?] Help hints are always right-aligned in brand color for discoverability.
func (m DashboardModel) renderFooter() string {
	var hints []string

	var leadHint string
	if m.focusPanel == 1 {
		// Right panel focused — show detail actions
		f := m.SelectedFeature()
		hints = append(hints, "[←/esc] Back")

		if f != nil {
			activePublishedCycle := isActivePublishedCycle(f)
			if actionHint, lead := contextualAActionHint(f); actionHint != "" {
				if lead {
					leadHint = WarningStyle.Bold(true).Render(actionHint)
				} else {
					hints = append(hints, actionHint)
				}
			}
			if hasPendingPerms(f) {
				hints = append(hints, "[y] Approve", "[Shift+A] Approve & Remember")
			}
			if isRunningFeature(f) {
				hints = append(hints, "[s] Stop")
			} else {
				hints = append(hints, "[r] Restart")
			}
			hints = append(hints, "[ctrl+r] Rewind")
			if f.Status == feature.StatusFailed || f.Status == feature.StatusInterrupted {
				hints = append(hints, "[l] Logs")
			}

			if f.Status == feature.StatusCodeReady && len(f.Repos) > 0 && f.Repos[0].WorktreePath != "" {
				hints = append(hints, "[v] Diff")
			}
			if f.Status == feature.StatusCodeReady && !f.Checkpoints.AutoPublish() && f.IsPublishable() {
				hints = append(hints, "[p] Publish")
			}
			if (!activePublishedCycle && f.Status == feature.StatusPublished) || (f.Status == feature.StatusCodeReady && !f.Checkpoints.AutoPublish()) {
				hints = append(hints, "[t] Tweak")
				hints = append(hints, "[Shift+F] Refactor")
				hints = append(hints, "[b] Rebase")
			}
			if f.Status == feature.StatusCodeReady && !f.IsPublishable() {
				hints = append(hints, "[Shift+M] Merge to base")
				hints = append(hints, "[Shift+D] Mark done")
			}
			if f.Status == feature.StatusPublished && !activePublishedCycle {
				hints = append(hints, "[Shift+D] Mark done")
				if len(f.PRURLs()) > 0 && f.IsPublishable() {
					hints = append(hints, "[g] Reviews")
				}
			}
			if ((f.Status == feature.StatusPublished && !activePublishedCycle) || f.Status == feature.StatusDone) &&
				len(f.Repos) > 0 && f.Repos[0].WorktreePath != "" {
				hints = append(hints, "[c] Clean worktree")
			}
			if isFeatureQuiescent(f) {
				hints = append(hints, "[e] Edit config")
			}
			hints = append(hints, "[Shift+N] Input alerts")
			hints = append(hints, "[d] Delete")
		}
	} else {
		// Left panel focused — show list actions
		hints = append(hints, "[n] New")
		if m.SelectedFeature() != nil {
			hints = append(hints, "[→/enter] Focus")
		}
		hints = append(hints, "[Shift+W] Workspaces", "[Shift+R] Resume All", "[tab] Panel", "[q] Quit")
	}

	leftPart := KeyHelpStyle.Render(" " + strings.Join(hints, "   "))
	if leadHint != "" {
		// Render actions without MarginTop so leadHint and actions share one line;
		// add the margin newline manually so the footer still sits one row down.
		plainHelpStyle := lipgloss.NewStyle().Foreground(colorOverlay)
		leftPart = "\n " + leadHint + plainHelpStyle.Render("   "+strings.Join(hints, "   "))
	}
	brandHint := lipgloss.NewStyle().Foreground(colorBrand).Bold(true)
	rightHints := MutedStyle.Render("Layout: "+KeyboardLayoutHint()) + "  " + brandHint.Render("["+ChatKeyHint()+"] Ask") + "  " + brandHint.Render("["+HelpKeyHint()+"] Help")

	gap := m.width - lipgloss.Width(leftPart) - lipgloss.Width(rightHints) - 1
	if gap < 2 {
		gap = 2
	}

	footer := leftPart + strings.Repeat(" ", gap) + rightHints
	if m.statusMessage != "" {
		statusStyle := lipgloss.NewStyle().Foreground(colorInfo).Bold(true)
		if strings.HasPrefix(m.statusMessage, "\u2717") {
			statusStyle = lipgloss.NewStyle().Foreground(colorError).Bold(true)
		} else if strings.HasPrefix(m.statusMessage, "\u2713") {
			statusStyle = lipgloss.NewStyle().Foreground(colorSuccess).Bold(true)
		}
		msg := m.statusMessage
		maxW := m.width - 3
		if maxW > 0 && lipgloss.Width(msg) > maxW {
			msg = msg[:maxW-1] + "…"
		}
		footer = " " + statusStyle.Render(msg) + "\n" + footer
	}
	return footer
}

// sectionMeta holds metadata for a feature section during buildVisibleItems.
type sectionMeta struct {
	key      string
	label    string
	features []*feature.Feature
}

// buildVisibleItems computes the flat list of navigable items (section headers + features).
// It must be called whenever features or collapsed sections change.
func (m *DashboardModel) buildVisibleItems() {
	// Partition features into sections
	var inProgress, published, completed []*feature.Feature
	for _, f := range m.features {
		switch {
		case f.Status == feature.StatusDone:
			completed = append(completed, f)
		case isActivePublishedCycle(f):
			inProgress = append(inProgress, f)
		case f.Status == feature.StatusPublished:
			published = append(published, f)
		default:
			inProgress = append(inProgress, f)
		}
	}

	sections := []sectionMeta{
		{"inProgress", "IN PROGRESS", inProgress},
		{"published", "PUBLISHED", published},
		{"completed", "COMPLETED", completed},
	}

	var items []listItem
	for _, sec := range sections {
		if len(sec.features) == 0 {
			continue
		}
		items = append(items, listItem{kind: listItemSectionHeader, section: sec.key})

		if sec.key != "inProgress" && m.collapsedSections[sec.key] {
			continue // section collapsed, skip features
		}

		for _, f := range sec.features {
			items = append(items, listItem{kind: listItemFeature, feature: f, section: sec.key})
		}
	}

	// When the list is empty and no feature is being created, show a ghost CTA
	if len(items) == 0 && m.creatingName == "" {
		items = append(items, listItem{kind: listItemGhostCTA})
	}

	m.visibleItems = items
	m.computeCursorLine()
}

// computeCursorLine calculates the rendered line index for the current cursor position.
// This must be called whenever cursor or visibleItems change.
func (m *DashboardModel) computeCursorLine() {
	lineIndex := 0
	prevSection := ""
	for i, item := range m.visibleItems {
		switch item.kind {
		case listItemSectionHeader:
			if prevSection != "" {
				lineIndex++ // blank line between sections
			}
			prevSection = item.section
			if i == m.cursor {
				m.cursorLine = lineIndex
				return
			}
			lineIndex++ // header line
			lineIndex++ // blank after header
		case listItemFeature:
			if i == m.cursor {
				m.cursorLine = lineIndex
				return
			}
			lineIndex++ // feature row
		}
	}
}

// updateScrollState ensures the scroll offset keeps the cursor visible within panelHeight.
// Must be called after cursor movement, item changes, or panel resize.
func (m *DashboardModel) updateScrollState(panelHeight int) {
	if panelHeight > 0 {
		m.panelHeight = panelHeight
	}
	if m.panelHeight <= 0 {
		return
	}

	// Count total rendered lines (same logic as renderFeatureList)
	totalLines := 0
	prevSection := ""
	for _, item := range m.visibleItems {
		if item.kind == listItemSectionHeader {
			if prevSection != "" {
				totalLines++
			}
			prevSection = item.section
			totalLines += 2 // header + blank after
		} else {
			totalLines++ // feature row
		}
	}

	if totalLines <= m.panelHeight {
		m.scrollOffset = 0
		return
	}

	// Ensure cursor line is visible, accounting for indicator lines.
	// When scrollOffset > 0, the first visible line is a ▲ indicator,
	// so the actual content starts at scrollOffset+1 visually.
	// Similarly, when there's content below, the last visible line is ▼.
	topMargin := 0
	if m.scrollOffset > 0 {
		topMargin = 1
	}
	// Adjust scroll so cursor is within the safe (non-indicator) zone.
	// Top edge: when scrollOffset > 0, the ▲ indicator occupies the first visible line,
	// so content starts at scrollOffset+1. Setting scrollOffset = cursorLine would hide
	// the cursor behind the indicator. Instead, back off by 1 so the cursor is the first
	// content line after the indicator.
	if m.cursorLine < m.scrollOffset+topMargin {
		if m.cursorLine > 0 {
			m.scrollOffset = m.cursorLine - 1 // leave room for ▲ indicator
		} else {
			m.scrollOffset = 0 // at the very top, no indicator needed
		}
	}
	// Recalculate margins after potential adjustment
	end := m.scrollOffset + m.panelHeight
	if end > totalLines {
		end = totalLines
	}
	bottomMargin := 0
	if end < totalLines {
		bottomMargin = 1
	}

	if m.cursorLine >= m.scrollOffset+m.panelHeight-bottomMargin {
		m.scrollOffset = m.cursorLine - m.panelHeight + 1 + bottomMargin
	}

	// Clamp
	maxOffset := totalLines - m.panelHeight
	if maxOffset < 0 {
		maxOffset = 0
	}
	if m.scrollOffset > maxOffset {
		m.scrollOffset = maxOffset
	}
	if m.scrollOffset < 0 {
		m.scrollOffset = 0
	}

	// Final pass: verify cursor isn't on an indicator line after clamping
}

// sectionFeatureCount returns the total number of features in a section (regardless of collapse).
func (m DashboardModel) sectionFeatureCount(sectionKey string) int {
	count := 0
	for _, f := range m.features {
		switch sectionKey {
		case "inProgress":
			if isActivePublishedCycle(f) || (f.Status != feature.StatusPublished && f.Status != feature.StatusDone) {
				count++
			}
		case "published":
			if f.Status == feature.StatusPublished && !isActivePublishedCycle(f) {
				count++
			}
		case "completed":
			if f.Status == feature.StatusDone {
				count++
			}
		}
	}
	return count
}

// sectionLabel returns the display label for a section key.
func sectionLabel(key string) string {
	switch key {
	case "inProgress":
		return "IN PROGRESS"
	case "published":
		return "PUBLISHED"
	case "completed":
		return "COMPLETED"
	default:
		return strings.ToUpper(key)
	}
}

// renderFeatureList builds the left panel content listing features.
func (m DashboardModel) renderFeatureList() string {
	var content strings.Builder
	lineIndex := 0

	prevSection := ""
	for i, item := range m.visibleItems {
		switch item.kind {
		case listItemSectionHeader:
			// Add blank line between sections (not before the first)
			if prevSection != "" {
				content.WriteString("\n")
				lineIndex++
			}
			prevSection = item.section

			count := m.sectionFeatureCount(item.section)
			label := sectionLabel(item.section)
			arrow := "▾"
			if m.collapsedSections[item.section] {
				arrow = "▸"
			}
			headerText := fmt.Sprintf("%s %s (%d)", arrow, label, count)

			if i == m.cursor {
				content.WriteString(SectionHeaderSelectedStyle.Render(headerText) + "\n")
			} else {
				content.WriteString(SectionHeaderStyle.Render(headerText) + "\n")
			}
			lineIndex++

			// Blank line after header
			content.WriteString("\n")
			lineIndex++

		case listItemFeature:
			f := item.feature

			row := m.renderFeatureRowCompact(f, i == m.cursor)
			content.WriteString(row + "\n")
			lineIndex++

		case listItemGhostCTA:
			label := "+ Create your first feature (n)"
			if i == m.cursor {
				style := lipgloss.NewStyle().Bold(true).Foreground(colorBrand)
				content.WriteString(style.Render("  "+label) + "\n")
			} else {
				content.WriteString(MutedStyle.Render("  "+label) + "\n")
			}
			lineIndex++
		}
	}

	// Show placeholder row for a feature being created (worktree setup in progress)
	if m.creatingName != "" {
		creatingStyle := lipgloss.NewStyle().Foreground(colorPeach)
		icon := creatingStyle.Render(icons.Created)
		slug := m.creatingName
		if len(slug) > 20 {
			slug = slug[:19] + "\u2026"
		}
		status := creatingStyle.Render("setting up worktrees")
		row := fmt.Sprintf("%s %-20s %s", icon, slug, status)
		content.WriteString("  " + row + "\n")
	}

	return content.String()
}

// renderWelcomePanel builds the right-panel welcome/explainer shown when no features exist.
func (m DashboardModel) renderWelcomePanel(_ int) string {
	var b strings.Builder

	b.WriteString(TitleStyle.Render("What does Agentic Orchestrator do?") + "\n\n")
	b.WriteString("You describe a feature.\n")
	b.WriteString("AI researches, plans, and implements it —\n")
	b.WriteString("then hands you a pull request.\n\n")

	b.WriteString(SubtitleStyle.Render("Start with something ambitious.") + "\n")
	b.WriteString("Agentic Orchestrator works best on features that would\n")
	b.WriteString("take you an hour or more to code by hand.\n\n")

	b.WriteString(SubtitleStyle.Render("Examples:") + "\n\n")

	exStyle := lipgloss.NewStyle().Foreground(colorBrand).Italic(true)
	b.WriteString(exStyle.Render("  \"Add rate limiting to the payments API\n   with per-customer sliding window and\n   Redis backing\"") + "\n\n")
	b.WriteString(exStyle.Render("  \"Refactor the notification service to use\n   an event-driven architecture with dead\n   letter queues\"") + "\n\n")

	b.WriteString(MutedStyle.Render("Press  n  to start.") + "\n")
	return b.String()
}

// renderFeatureRowCompact renders a compact feature row for the left panel.
// scrollFeatureList renders the feature list with scroll offset and ▲/▼ indicators.
// Scroll offset and cursor line are pre-computed during Update; this method only renders.
func (m DashboardModel) scrollFeatureList(panelHeight int) string {
	raw := m.renderFeatureList()
	lines := strings.Split(strings.TrimRight(raw, "\n"), "\n")
	totalLines := len(lines)

	if totalLines <= panelHeight {
		return raw
	}

	// Use pre-computed scrollOffset (set during Update via updateScrollState).
	// Clamp defensively in case panelHeight changed since last Update.
	offset := m.scrollOffset
	maxOffset := totalLines - panelHeight
	if maxOffset < 0 {
		maxOffset = 0
	}
	if offset > maxOffset {
		offset = maxOffset
	}
	if offset < 0 {
		offset = 0
	}

	end := offset + panelHeight
	if end > totalLines {
		end = totalLines
	}

	hasAbove := offset > 0
	hasBelow := end < totalLines

	// Reserve dedicated lines for indicators so they never overwrite content.
	// Shrink the content window by 1 at each end that needs an indicator.
	contentStart := offset
	contentEnd := end
	if hasAbove {
		contentStart++
	}
	if hasBelow {
		contentEnd--
	}
	// Safety: ensure contentStart <= contentEnd
	if contentStart > contentEnd {
		contentStart = contentEnd
	}

	var visible []string
	if hasAbove {
		visible = append(visible, MutedStyle.Render("  ▲ more"))
	}
	visible = append(visible, lines[contentStart:contentEnd]...)
	if hasBelow {
		visible = append(visible, MutedStyle.Render("  ▼ more"))
	}

	return strings.Join(visible, "\n") + "\n"
}

func (m DashboardModel) renderFeatureRowCompact(f *feature.Feature, selected bool) string {
	icon := statusIcon(f.Status.String())
	if isLivePreviewEligible(f) && m.spinnerView != "" {
		icon = m.spinnerView
	}
	var status string
	if m.rewindingFeatureID == f.ID {
		status = WarningStyle.Render("Stopping…")
	} else {
		status = formatStatus(f)
	}

	slug := f.Slug
	if len(slug) > 20 {
		slug = slug[:19] + "\u2026"
	}

	pipelineBadge := formatPipelineBadge(f.EffectivePipeline())
	row := fmt.Sprintf("%s %-20s %s %s", icon, slug, pipelineBadge, status)

	if selected {
		// Strip ANSI from sub-components so SelectedRowStyle colors the
		// whole row — otherwise embedded colors (e.g. Implementing,
		// Reviewing) win and the selection highlight is invisible.
		return SelectedRowStyle.Render("\u25b8 " + ansi.Strip(row))
	}
	// Only dim Done features, not Published
	if f.Status == feature.StatusDone {
		return MutedStyle.Render("  " + row)
	}
	return "  " + row
}

func (m DashboardModel) SelectedFeature() *feature.Feature {
	if m.cursor >= 0 && m.cursor < len(m.visibleItems) {
		item := m.visibleItems[m.cursor]
		if item.kind == listItemFeature {
			return item.feature
		}
	}
	return nil
}

// SelectedSection returns the section key if the cursor is on a section header, empty string otherwise.
func (m DashboardModel) SelectedSection() string {
	if m.cursor >= 0 && m.cursor < len(m.visibleItems) {
		item := m.visibleItems[m.cursor]
		if item.kind == listItemSectionHeader {
			return item.section
		}
	}
	return ""
}

func (m DashboardModel) SelectedFeatureID() string {
	f := m.SelectedFeature()
	if f != nil {
		return f.ID
	}
	return ""
}

func (m *DashboardModel) SetFeatures(features []*feature.Feature) {
	sortFeatures(features)
	m.features = features
	m.buildVisibleItems()
	if m.cursor >= len(m.visibleItems) {
		m.cursor = max(0, len(m.visibleItems)-1)
	}
	m.computeCursorLine()
	m.scrollOffset = 0
	m.updateScrollState(0)
	m.syncPreview()
}

// SetWelcomeSkipped marks that the user skipped the welcome flow, enabling
// guidance text in the empty-state panel.
func (m *DashboardModel) SetWelcomeSkipped() {
	m.welcomeSkipped = true
}

// SetCollapsedSections populates section-level collapse state from a config slice.
// The "inProgress" section is always forced expanded and ignored if present.
func (m *DashboardModel) SetCollapsedSections(sections []string) {
	m.collapsedSections = make(map[string]bool)
	validSections := map[string]bool{"published": true, "completed": true}
	for _, s := range sections {
		if validSections[s] {
			m.collapsedSections[s] = true
		}
	}
	m.buildVisibleItems()
	if m.cursor >= len(m.visibleItems) {
		m.cursor = max(0, len(m.visibleItems)-1)
	}
	m.computeCursorLine()
	m.updateScrollState(0)
}

// CollapsedSectionsList returns the currently collapsed sections as a slice for config persistence.
// The "inProgress" section is excluded so it self-heals if somehow present.
func (m DashboardModel) CollapsedSectionsList() []string {
	var result []string
	for s, collapsed := range m.collapsedSections {
		if collapsed && s != "inProgress" {
			result = append(result, s)
		}
	}
	sort.Strings(result)
	return result
}

func (m DashboardModel) countNeedAttention() int {
	count := 0
	for _, f := range m.features {
		if hasPendingPerms(f) || hasPendingHelp(f) {
			count++
		}
	}
	return count
}

func hasPendingPerms(f *feature.Feature) bool {
	for _, p := range f.PermissionsQueue {
		if p.Pending {
			return true
		}
	}
	return false
}

func hasPendingHelp(f *feature.Feature) bool {
	for _, h := range f.HelpQueue {
		if h.Pending {
			return true
		}
	}
	return false
}

// hasRepoPausedOnInput reports whether any repo (mainline implement or
func hasRepoPausedOnInput(f *feature.Feature) bool {
	if f == nil {
		return false
	}
	if f.PendingNeedUserInputPath != "" {
		return true
	}
	for _, rc := range f.RepoCycles {
		if rc != nil && rc.Status == feature.RepoCycleNeedUserInput {
			return true
		}
	}
	return false
}

func sortFeatures(features []*feature.Feature) {
	// First: standard sort
	sort.Slice(features, func(i, j int) bool {
		fi, fj := features[i], features[j]
		// Active > Published > Done
		iOrder := featureSortOrder(fi)
		jOrder := featureSortOrder(fj)
		if iOrder != jOrder {
			return iOrder < jOrder
		}
		// Within same group: needs attention first
		iNeeds := hasPendingPerms(fi) || hasPendingHelp(fi)
		jNeeds := hasPendingPerms(fj) || hasPendingHelp(fj)
		if iNeeds != jNeeds {
			return iNeeds
		}
		return fi.Created.After(fj.Created)
	})

}

func featureSortOrder(f *feature.Feature) int {
	if isActivePublishedCycle(f) {
		return 0
	}
	switch f.Status {
	case feature.StatusDone:
		return 2
	case feature.StatusPublished:
		return 1
	default:
		return 0
	}
}

func isActivePublishedCycle(f *feature.Feature) bool {
	_, _, ok := activePublishedCycleStatus(f)
	return ok
}

// formatPipelineBadge returns the wizard pipeline icon in a fixed-width cell
// for the compact dashboard list.
func formatPipelineBadge(profile feature.PipelineProfile) string {
	icon := pipelineProfileIcon(profile)
	if icon == "" {
		return MutedStyle.Render("   ")
	}

	style := lipgloss.NewStyle().Width(3)
	switch profile {
	case feature.PipelineMedium:
		style = style.Foreground(colorWarning)
	case feature.PipelineLarge:
		style = style.Foreground(colorInfo)
	case feature.PipelineMoonshot:
		style = style.Foreground(colorBrand)
	default:
		style = style.Foreground(colorOverlay)
	}
	return style.Render(icon)
}

// formatRiskBadge returns a short colored badge for the feature's risk level.
func formatRiskBadge(risk feature.RiskLevel) string {
	switch risk {
	case feature.RiskLow:
		return SuccessStyle.Render("[L]")
	case feature.RiskMedium:
		return WarningStyle.Render("[M]")
	case feature.RiskHigh:
		return ErrorStyle.Render("[H]")
	default:
		return MutedStyle.Render("   ")
	}
}

func activePublishedCycleStatus(f *feature.Feature) (label string, reviewing bool, ok bool) {
	if f == nil || len(f.RepoCycles) == 0 {
		return "", false, false
	}
	if f.Status != feature.StatusPublished && f.Status != feature.StatusCodeReady {
		return "", false, false
	}

	type activeCycle struct {
		repoName string
		rc       *feature.RepoCycleState
	}

	var active []activeCycle
	seen := make(map[string]bool, len(f.Repos))
	for _, repoName := range repoDisplayOrder(f) {
		seen[repoName] = true
		rc := f.RepoCycles[repoName]
		if rc == nil {
			continue
		}
		switch rc.Status {
		case feature.RepoCycleRunning, feature.RepoCycleReviewing, feature.RepoCycleNeedUserInput:
			active = append(active, activeCycle{repoName: repoName, rc: rc})
		}
	}

	if len(active) == 0 {
		var leftovers []string
		for repoName, rc := range f.RepoCycles {
			if seen[repoName] || rc == nil {
				continue
			}
			switch rc.Status {
			case feature.RepoCycleRunning, feature.RepoCycleReviewing, feature.RepoCycleNeedUserInput:
				leftovers = append(leftovers, repoName)
			}
		}
		sort.Strings(leftovers)
		for _, repoName := range leftovers {
			active = append(active, activeCycle{repoName: repoName, rc: f.RepoCycles[repoName]})
		}
	}

	if len(active) == 0 {
		return "", false, false
	}

	if len(active) > 1 {
		// Surface paused-on-user-input cycles ahead of in-flight Final
		// Review counts so the dashboard signals the human-attention
		// requirement first.
		for _, cycle := range active {
			if cycle.rc.Status == feature.RepoCycleNeedUserInput {
				return fmt.Sprintf("Repo Cycles Need Input (%d)", len(active)), false, true
			}
		}
		for _, cycle := range active {
			if cycle.rc.Status == feature.RepoCycleReviewing {
				return fmt.Sprintf("Repo Cycles In Final Review (%d)", len(active)), true, true
			}
		}
		return fmt.Sprintf("Repo Cycles Running (%d)", len(active)), false, true
	}

	cycle := active[0]
	switch cycle.rc.Status {
	case feature.RepoCycleNeedUserInput:
		label = string(cycle.rc.Type) + " needs input"
	case feature.RepoCycleReviewing:
		switch cycle.rc.Type {
		case feature.CycleReviewComments:
			label = "Final Review (Review Comments)"
		case feature.CycleRebase:
			label = "Final Review (Rebase)"
		case feature.CycleTweak:
			label = "Final Review (Tweak)"
		case feature.CycleRefactor:
			label = "Final Review (Refactor)"
		default:
			label = "Final Review (Repo Cycle)"
		}
		reviewing = true
	default:
		switch cycle.rc.Type {
		case feature.CycleReviewComments:
			label = "Addressing Review Comments"
		case feature.CycleRebase:
			label = "Rebasing"
		case feature.CycleTweak:
			label = "Tweaking"
		case feature.CycleRefactor:
			label = "Refactoring"
		default:
			label = "Repo Cycle Running"
		}
		if f.CurrentIteration > 0 {
			label = fmt.Sprintf("%s [%d]", label, f.CurrentIteration)
		}
	}

	if len(f.Repos) > 1 && cycle.repoName != "" {
		label += " · " + cycle.repoName
	}
	return label, reviewing, true
}

func formatStatus(f *feature.Feature) string {
	needsInput := hasPendingPerms(f) || hasPendingHelp(f) || hasRepoPausedOnInput(f)
	elapsed := formatElapsed(f)

	switch f.Status {
	case feature.StatusImplementing:
		planPath := ""
		if f.Artifacts != nil {
			planPath = f.Artifacts["plan"]
		}
		normalizedPath := filepath.ToSlash(planPath)
		if f.AddressingReviews() && f.CurrentPhase == feature.PhaseImplement {
			base := fmt.Sprintf("Addressing reviews [%d]", f.CurrentIteration)
			if needsInput {
				return WarningStyle.Render(base+" | waiting input") + elapsed
			}
			return base + elapsed
		}
		if f.ActiveCycleType() == feature.CycleTweak && f.CurrentPhase == feature.PhaseImplement {
			base := fmt.Sprintf("Tweaking [%d]", f.CurrentIteration)
			if needsInput {
				return WarningStyle.Render(base+" | waiting input") + elapsed
			}
			return base + elapsed
		}
		if strings.Contains(normalizedPath, "/rebase/") && f.CurrentPhase == feature.PhaseImplement {
			base := fmt.Sprintf("Resolving conflicts [%d]", f.CurrentIteration)
			if needsInput {
				return WarningStyle.Render(base+" | waiting input") + elapsed
			}
			return base + elapsed
		}
		if f.IsRefactoring() && strings.Contains(normalizedPath, "/refactor-") && f.CurrentPhase == feature.PhaseImplement {
			base := fmt.Sprintf("Refactoring: Implementing [%d]", f.CurrentIteration)
			if needsInput {
				return WarningStyle.Render(base+" | waiting input") + elapsed
			}
			return base + elapsed
		}
		if f.ReviewingGate {
			return ReviewStyle.Render(fmt.Sprintf("Reviewing [%d]", f.CurrentIteration)) + elapsed
		}
		var base string
		if f.CurrentRoadmapPhase > 0 && f.TotalRoadmapPhases > 0 {
			base = fmt.Sprintf("Implementing (Phase %d/%d) [%d]", f.CurrentRoadmapPhase, f.TotalRoadmapPhases, f.CurrentIteration)
		} else {
			base = fmt.Sprintf("Implementing [%d]", f.CurrentIteration)
		}
		if needsInput {
			return WarningStyle.Render(base+" | waiting input") + elapsed
		}
		return base + elapsed
	case feature.StatusBuildingKB:
		if f.KBWaitMessage != "" {
			return MutedStyle.Render("Waiting for KB") + elapsed
		}
		if needsInput {
			return WarningStyle.Render("Building KB | waiting input") + elapsed
		}
		return "Building KB" + elapsed
	case feature.StatusResearching:
		label := "Researching"
		if f.IsRefactoring() {
			label = "Refactoring: Researching"
		}
		if needsInput {
			return WarningStyle.Render(label+" | waiting input") + elapsed
		}
		return label + elapsed
	case feature.StatusInquiring:
		label := "Inquiring"
		if f.IsRefactoring() {
			label = "Refactoring: Inquiring"
		}
		if needsInput {
			return WarningStyle.Render(label+" | waiting input") + elapsed
		}
		return label + elapsed
	case feature.StatusBrainstorming:
		label := "Brainstorming"
		if f.IsRefactoring() {
			label = "Refactoring: Brainstorming"
		}
		if needsInput {
			return WarningStyle.Render(label+" | waiting input") + elapsed
		}
		return label + elapsed
	case feature.StatusPlanning:
		refPrefix := ""
		if f.IsRefactoring() {
			refPrefix = "Refactoring: "
		}
		if f.CurrentRoadmapPhase > 0 {
			// Per-phase planning
			if f.ValidatingPlan {
				return ReviewStyle.Render(fmt.Sprintf("%sValidating Phase %d plan", refPrefix, f.CurrentRoadmapPhase)) + elapsed
			}
			base := fmt.Sprintf("%sPlanning Phase %d", refPrefix, f.CurrentRoadmapPhase)
			if f.PlanIteration > 1 {
				base = fmt.Sprintf("%sPlanning Phase %d [%d]", refPrefix, f.CurrentRoadmapPhase, f.PlanIteration)
			}
			if needsInput {
				return WarningStyle.Render(base+" | waiting input") + elapsed
			}
			return base + elapsed
		}
		// Roadmap creation (phase 0)
		if f.ValidatingPlan {
			return ReviewStyle.Render(refPrefix+"Validating roadmap") + elapsed
		}
		if f.PlanIteration > 1 {
			base := fmt.Sprintf("%sCreating Roadmap [%d]", refPrefix, f.PlanIteration)
			if needsInput {
				return WarningStyle.Render(base+" | waiting input") + elapsed
			}
			return base + elapsed
		}
		if needsInput {
			return WarningStyle.Render(refPrefix+"Creating Roadmap | waiting input") + elapsed
		}
		return refPrefix + "Creating Roadmap" + elapsed
	case feature.StatusPlanNeedsReview:
		return WarningStyle.Render("Plan needs review") + elapsed
	case feature.StatusPromptNeedsReview:
		return WarningStyle.Render("Prompt needs review") + elapsed
	case feature.StatusInquiryNeedsReview:
		return WarningStyle.Render("Inquiry needs review") + elapsed
	case feature.StatusResearchNeedsReview:
		return WarningStyle.Render("Research needs review") + elapsed
	case feature.StatusDesignNeedsReview:
		return WarningStyle.Render("Design needs review") + elapsed
	case feature.StatusNeedUserInput:
		return WarningStyle.Render("Needs user input") + elapsed
	case feature.StatusFinalReviewing:
		base := finalReviewStatusText(f)
		if needsInput {
			return WarningStyle.Render(base+" | waiting input") + elapsed
		}
		return ReviewStyle.Render(base) + elapsed
	case feature.StatusCodeReady:
		if label, reviewing, ok := activePublishedCycleStatus(f); ok {
			if needsInput {
				return WarningStyle.Render(label+" | waiting input") + elapsed
			}
			if reviewing {
				return ReviewStyle.Render(label) + elapsed
			}
			return lipgloss.NewStyle().Foreground(colorInfo).Render(label) + elapsed
		}
		if f.IsPublishable() && f.Checkpoints.AutoPublish() {
			return "Publishing..." + elapsed
		}
		return "Code Ready" + elapsed
	case feature.StatusPublished:
		if label, reviewing, ok := activePublishedCycleStatus(f); ok {
			if needsInput {
				return WarningStyle.Render(label+" | waiting input") + elapsed
			}
			if reviewing {
				return ReviewStyle.Render(label) + elapsed
			}
			return lipgloss.NewStyle().Foreground(colorInfo).Render(label) + elapsed
		}
		return SuccessStyle.Render("Published")
	case feature.StatusInterrupted:
		return MutedStyle.Render("Stopped (r to restart)") + elapsed
	case feature.StatusFailed:
		reason := "Failed"
		if f.FailureType != "" {
			reason = "Failed (" + formatFailureType(f.FailureType) + ")"
		}
		return ErrorStyle.Render(reason) + elapsed
	default:
		return f.Status.String() + elapsed
	}
}

// formatFailureType returns a human-readable short label for a failure type.
func formatFailureType(ft string) string {
	switch ft {
	case feature.FailureSafetyRail:
		return "safety rail"
	case feature.FailureMaxIterations:
		return "max iterations"
	case feature.FailureSessionCrash:
		return "session crash"
	case feature.FailureMissingArtifact:
		return "missing artifact"
	case feature.FailureProtocolViolation:
		return "protocol violation"
	case feature.FailureInfrastructure:
		return "error"
	default:
		return ft
	}
}

// formatElapsed returns a styled elapsed-time suffix showing the total active
// runtime and cost for the feature, or empty string if no data exists.
func formatElapsed(f *feature.Feature) string {
	d := f.TotalRuntime()
	cost := f.TotalCost()
	if d == 0 && cost == 0 {
		return ""
	}
	var parts []string
	if d > 0 {
		parts = append(parts, formatDuration(d))
	}
	if cost > 0 {
		parts = append(parts, formatCost(cost))
	}
	return MutedStyle.Render(" " + strings.Join(parts, " "))
}

// formatCost formats a USD cost value for display.
// Returns empty string for zero cost.
func formatCost(cost float64) string {
	if cost <= 0 {
		return ""
	}
	if cost < 0.01 {
		return fmt.Sprintf("$%.4f", cost)
	}
	return fmt.Sprintf("$%.2f", cost)
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return "<1m"
	}
	d = d.Truncate(time.Minute)

	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60

	switch {
	case days > 0:
		if hours > 0 {
			return fmt.Sprintf("%dd%dh", days, hours)
		}
		return fmt.Sprintf("%dd", days)
	case hours > 0:
		if minutes > 0 {
			return fmt.Sprintf("%dh%dm", hours, minutes)
		}
		return fmt.Sprintf("%dh", hours)
	default:
		return fmt.Sprintf("%dm", minutes)
	}
}
