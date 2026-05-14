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

	"github.com/charmbracelet/x/ansi"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
)

// LivePreviewModel renders the normal live dashboard right panel. It is a
// compact status shell, not the full attach transcript.
type LivePreviewModel struct {
	feature     *feature.Feature
	session     session.SessionView
	spinnerView string
	width       int
	height      int
}

func newLivePreviewModel(f *feature.Feature) LivePreviewModel {
	return LivePreviewModel{feature: f}
}

// isLivePreviewEligible reports whether the dashboard should render the live
// preview shell instead of the static detail panel for the selected feature.
func isLivePreviewEligible(f *feature.Feature) bool {
	if f == nil {
		return false
	}
	if f.Status == feature.StatusCreated {
		return true
	}
	if f.Status.IsRunning() {
		return true
	}
	if f.Status == feature.StatusPublished || f.Status == feature.StatusCodeReady {
		return f.HasActiveRepoCycles()
	}
	return false
}

func contextualAActionHint(f *feature.Feature) (hint string, lead bool) {
	if f == nil {
		return "", false
	}
	switch {
	case f.Status.IsNeedsReview():
		return "[a] Review", true
	case f.Status == feature.StatusNeedUserInput || firstRepoNeedingInput(f) != "":
		return "[a] Answer", true
	case hasPendingPerms(f) || hasPendingHelp(f):
		return "[a] Attach (⚠)", false
	case isLivePreviewEligible(f):
		return "[a] Watch", false
	case isRunningFeature(f):
		return "[a] Attach", false
	default:
		return "", false
	}
}

func (m LivePreviewModel) ViewCompact(width int) string {
	if m.feature == nil {
		return MutedStyle.Render("No feature selected")
	}
	if width < 20 {
		width = 40
	}

	f := m.feature
	contentWidth := max(width-4, 20)
	var b strings.Builder
	b.WriteString(TitleStyle.Render("Live Preview") + "\n")
	b.WriteString(livePreviewHeaderLine("Phase", livePreviewPhaseLabel(f, m.session), contentWidth) + "\n")
	b.WriteString(livePreviewHeaderLine("Status", livePreviewStatusText(f), contentWidth) + "\n")
	b.WriteString(livePreviewHeaderLine("Elapsed", livePreviewElapsedText(f), contentWidth) + "\n")
	b.WriteString(livePreviewHeaderLine("Cost", livePreviewCostText(f), contentWidth) + "\n\n")

	activity := livePreviewActivityLine(f, m.session)
	prefix := m.spinnerView
	if prefix == "" {
		prefix = MutedStyle.Render("⟳")
	}
	line := "  " + prefix + " " + chatThinkingStyle.Render(activity)
	b.WriteString(ansi.Truncate(line, contentWidth, "…"))
	b.WriteString("\n")
	return b.String()
}

func livePreviewHeaderLine(label, value string, width int) string {
	line := fmt.Sprintf("%s  %s", LabelStyle.Render(label), value)
	if width > 0 {
		return ansi.Truncate(line, width, "…")
	}
	return line
}

func livePreviewPhaseLabel(f *feature.Feature, sess session.SessionView) string {
	if sess != nil {
		return sess.Phase().String()
	}
	if f == nil {
		return "Unknown"
	}
	return f.CurrentPhase.String()
}

func livePreviewStatusText(f *feature.Feature) string {
	if f == nil {
		return "Unknown"
	}
	if label, _, ok := activePublishedCycleStatus(f); ok {
		return label
	}
	if f.Status == feature.StatusCreated {
		return "Starting"
	}
	return f.Status.String()
}

func livePreviewElapsedText(f *feature.Feature) string {
	if f == nil {
		return "—"
	}
	if d := f.TotalRuntime(); d > 0 {
		return formatDuration(d)
	}
	return "—"
}

func livePreviewCostText(f *feature.Feature) string {
	if f == nil {
		return "—"
	}
	if cost := f.TotalCost(); cost > 0 {
		return formatCost(cost)
	}
	return "—"
}

func livePreviewActivityLine(f *feature.Feature, sess session.SessionView) string {
	if f != nil && f.Status == feature.StatusCreated {
		return "Starting " + f.CurrentPhase.String() + "..."
	}
	if sess == nil || sess.MessageLog() == nil {
		return workingOnPhaseLine(f, sess)
	}

	msgs := sess.MessageLog().LastN(50)
	for i := len(msgs) - 1; i >= 0; i-- {
		if line, ok := activityLineFromMessage(f, sess, msgs[i]); ok {
			return line
		}
	}
	return workingOnPhaseLine(f, sess)
}

func activityLineFromMessage(f *feature.Feature, sess session.SessionView, msg llm.SDKMessage) (string, bool) {
	switch {
	case msg.Result != nil:
		if isTweakLivePreview(f, sess) {
			return "Waiting for tweak input...", true
		}
		return workingOnPhaseLine(f, sess), true
	case msg.ToolProgress != nil && msg.ToolProgress.ToolName != "":
		return "Using " + msg.ToolProgress.ToolName + "...", true
	case msg.Status != nil && strings.TrimSpace(msg.Status.Message) != "":
		return strings.TrimSpace(msg.Status.Message), true
	case msg.Assistant != nil:
		if toolName := lastToolUseName(msg.Assistant.Message.Content); toolName != "" {
			return "Using " + toolName + "...", true
		}
		if hasThinkingBlock(msg.Assistant.Message.Content) {
			return "Thinking...", true
		}
	}
	return "", false
}

func lastToolUseName(blocks []llm.ContentBlock) string {
	for i := len(blocks) - 1; i >= 0; i-- {
		if blocks[i].IsToolUse() && blocks[i].Name != "" {
			return blocks[i].Name
		}
	}
	return ""
}

func hasThinkingBlock(blocks []llm.ContentBlock) bool {
	for i := len(blocks) - 1; i >= 0; i-- {
		if blocks[i].IsThinking() {
			return true
		}
	}
	return false
}

func workingOnPhaseLine(f *feature.Feature, sess session.SessionView) string {
	return "Working on " + livePreviewPhaseName(f, sess) + "..."
}

func livePreviewPhaseName(f *feature.Feature, sess session.SessionView) string {
	if sess != nil {
		return sess.Phase().String()
	}
	if f != nil {
		return f.CurrentPhase.String()
	}
	return "feature"
}

func isTweakLivePreview(f *feature.Feature, sess session.SessionView) bool {
	if sess != nil {
		if sess.Kind() == ports.KindTweak || isTweakSessionID(sess.ID()) {
			return true
		}
	}
	if f == nil {
		return false
	}
	if hasPendingHelpRequestMessage(f, waitingInputHelpMessage) {
		return true
	}
	return hasTweakCycle(f)
}
