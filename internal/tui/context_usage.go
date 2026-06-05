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

	"charm.land/lipgloss/v2"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
)

type smartZoneContextUsage struct {
	fillTokens      int
	thresholdTokens int
	pct             int
}

func emptySmartZoneContextUsage() smartZoneContextUsage {
	return smartZoneContextUsage{fillTokens: -1, pct: -1}
}

func newSmartZoneContextUsage(fillTokens, thresholdTokens int) smartZoneContextUsage {
	if fillTokens < 0 || thresholdTokens <= 0 {
		return emptySmartZoneContextUsage()
	}
	return smartZoneContextUsage{
		fillTokens:      fillTokens,
		thresholdTokens: thresholdTokens,
		pct:             fillTokens * 100 / thresholdTokens,
	}
}

func smartZoneContextUsageForSession(sess session.SessionView) smartZoneContextUsage {
	if sess == nil {
		return emptySmartZoneContextUsage()
	}
	return newSmartZoneContextUsage(sess.ContextFillTokens(), sess.ContextHandoffThresholdTokens())
}

func (u smartZoneContextUsage) available() bool {
	return u.fillTokens >= 0 && u.thresholdTokens > 0 && u.pct >= 0
}

func (u smartZoneContextUsage) closerThan(other smartZoneContextUsage) bool {
	if !u.available() {
		return false
	}
	if !other.available() {
		return true
	}
	return int64(u.fillTokens)*int64(other.thresholdTokens) > int64(other.fillTokens)*int64(u.thresholdTokens)
}

func formatSmartZoneContextUsage(u smartZoneContextUsage) string {
	return fmt.Sprintf("%s / %s (%d%%)", formatTokenK(u.fillTokens), formatTokenK(u.thresholdTokens), u.pct)
}

func formatTokenK(tokens int) string {
	if tokens < 0 {
		tokens = 0
	}
	return fmt.Sprintf("%dK", tokens/1000)
}

// contextBox holds the data for the multi-line Context box shown in the live
// preview for any running session. mainFill / window / threshold are main-only
// (the Smart Zone is never moved by sub-agents); subCount / subMaxFill describe
// currently-active fan-out sub-agents and are display-only.
type contextBox struct {
	mainFill   int
	window     int
	threshold  int
	subCount   int
	subMaxFill int
}

// contextBoxRedWindowPct is the percentage of the window at which the
// main-usage number turns red, independent of the Smart Zone threshold.
const contextBoxRedWindowPct = 80

func contextBoxForSession(sess session.SessionView) contextBox {
	if sess == nil {
		return contextBox{mainFill: -1}
	}
	return contextBox{
		mainFill:   sess.ContextFillTokens(),
		window:     sess.ContextWindowTokens(),
		threshold:  sess.ContextHandoffThresholdTokens(),
		subCount:   sess.ActiveSubAgentCount(),
		subMaxFill: sess.MaxActiveSubAgentFillTokens(),
	}
}

// mainAvailable reports whether a real main-fill snapshot has arrived. The
// window/threshold may still be unknown (window 0), which the renderer handles.
func (b contextBox) mainAvailable() bool {
	return b.mainFill >= 0
}

// mainStyle colors the main-usage number: red at/above contextBoxRedWindowPct%
// of the window, else yellow at/above the Smart Zone threshold, else neutral.
func (b contextBox) mainStyle() lipgloss.Style {
	if b.window > 0 && b.mainFill*100 >= b.window*contextBoxRedWindowPct {
		return ErrorStyle
	}
	if b.threshold > 0 && b.mainFill >= b.threshold {
		return WarningStyle
	}
	return contextBoxNeutralStyle
}

// thresholdStyle colors the Smart Zone threshold number: yellow once the main
// fill reaches it, else neutral. It never goes red and never references the
// 80%-window alarm.
func (b contextBox) thresholdStyle() lipgloss.Style {
	if b.threshold > 0 && b.mainFill >= b.threshold {
		return WarningStyle
	}
	return contextBoxNeutralStyle
}

// contextBoxNeutralStyle is the un-alarmed default for context numbers (the
// window size, percentage, and the entire Sub-agents line).
var contextBoxNeutralStyle = lipgloss.NewStyle().Foreground(colorText)

// mainPct is the main fill as a percentage of the window (0 when the window is
// unknown).
func (b contextBox) mainPct() int {
	if b.window <= 0 {
		return 0
	}
	pct := b.mainFill * 100 / b.window
	if pct > 100 {
		pct = 100
	}
	return pct
}

// contextBoxLines renders the Context box body lines. When no main-fill
// snapshot has arrived it returns a single muted Calculating placeholder,
// matching the legacy single-line indicator.
func contextBoxLines(b contextBox) []string {
	if !b.mainAvailable() {
		return []string{MutedStyle.Render("Calculating...")}
	}

	labelStyle := lipgloss.NewStyle().Foreground(colorSubtext)
	label := func(s string) string {
		return labelStyle.Width(12).Render(s)
	}

	mainNum := b.mainStyle().Render(formatTokenK(b.mainFill))
	windowNum := contextBoxNeutralStyle.Render(formatTokenK(b.window))
	pct := contextBoxNeutralStyle.Render(fmt.Sprintf("(%d%%)", b.mainPct()))
	mainLine := label("Main:") + mainNum + contextBoxNeutralStyle.Render(" / ") + windowNum + " " + pct

	thresholdLine := label("Smart Zone:") + b.thresholdStyle().Render(formatTokenK(b.threshold))

	subValue := fmt.Sprintf("%d · max %s", b.subCount, formatTokenK(b.subMaxFill))
	subLine := label("Sub-agents:") + contextBoxNeutralStyle.Render(subValue)

	return []string{mainLine, thresholdLine, subLine}
}

func smartZoneContextUsageStyle(u smartZoneContextUsage) lipgloss.Style {
	switch {
	case !u.available():
		return MutedStyle
	case u.pct >= 90:
		return ErrorStyle
	case u.pct >= 75:
		return WarningStyle
	default:
		return SuccessStyle
	}
}
