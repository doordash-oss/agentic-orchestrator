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
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
)

const (
	livePreviewTranscriptMessageLimit = 80
	livePreviewTailMinContentWidth    = 56
	livePreviewBaseRows               = 7
	livePreviewTailOverheadRows       = 2
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
	return computeFeatureAttention(f, nil).HasCTA()
}

func (m LivePreviewModel) ViewCompact(width int) string {
	if m.feature == nil {
		return MutedStyle.Render("No feature selected")
	}
	if width < 20 {
		width = 40
	}

	f := m.feature
	att := computeFeatureAttention(f, m.session)
	contentWidth := max(width-4, 20)
	var b strings.Builder
	b.WriteString(TitleStyle.Render("Live Preview") + "\n")
	b.WriteString(livePreviewHeaderLine("Phase", livePreviewPhaseLabel(f, m.session), contentWidth) + "\n")
	b.WriteString(livePreviewHeaderLine("Status", livePreviewStatusText(f), contentWidth) + "\n")
	b.WriteString(livePreviewHeaderLine("Elapsed", livePreviewElapsedText(f), contentWidth) + "\n")
	b.WriteString(livePreviewHeaderLine("Cost", livePreviewCostText(f), contentWidth) + "\n")
	if m.height == 0 || m.height > livePreviewBaseRows {
		b.WriteString("\n")
	}

	activity := livePreviewActivityLine(f, m.session)
	if att.RequiresUser() {
		for _, line := range renderLivePreviewAttentionBlock(att, contentWidth) {
			b.WriteString(line)
			b.WriteString("\n")
		}
		if m.height == 0 || m.height > livePreviewBaseRows+4 {
			b.WriteString("\n")
		}
		activity = att.ActivityLine()
	}
	prefix := m.spinnerView
	if prefix == "" {
		prefix = MutedStyle.Render("⟳")
	}
	if att.RequiresUser() {
		prefix = awaitingUserGlyph()
	}
	line := "  " + prefix + " " + chatThinkingStyle.Render(activity)
	b.WriteString(ansi.Truncate(line, contentWidth, "…"))
	b.WriteString("\n")

	tail := livePreviewTranscriptTail(f, m.session, contentWidth, livePreviewTailHeight(m.height, att))
	if len(tail) > 0 {
		b.WriteString("\n")
		b.WriteString(livePreviewTailBannerLine(f, m.session, contentWidth))
		b.WriteString("\n")
		for _, line := range tail {
			b.WriteString(line)
			b.WriteString("\n")
		}
	}
	return b.String()
}

func renderLivePreviewAttentionBlock(att featureAttention, width int) []string {
	if !att.RequiresUser() {
		return nil
	}
	summary := att.Summary
	if summary == "" {
		summary = att.TypeLabel
	}
	lines := []string{
		"  " + awaitingUserGlyph() + " " + WarningStyle.Bold(true).Render(att.TypeLabel),
		"  " + MutedStyle.Render(summary),
	}
	if hint := att.FooterHint(); hint != "" {
		lines = append(lines, "  "+WarningStyle.Bold(true).Render(hint))
	}
	for i, line := range lines {
		lines[i] = ansi.Truncate(line, width, "…")
	}
	return lines
}

func livePreviewTailHeight(height int, att featureAttention) int {
	if height <= 0 || !att.RequiresUser() {
		return height
	}
	return max(height-4, 0)
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

type livePreviewTranscriptKind int

const (
	livePreviewTranscriptAssistant livePreviewTranscriptKind = iota
	livePreviewTranscriptTool
	livePreviewTranscriptResult
	livePreviewTranscriptWarning
	livePreviewTranscriptQuestion
	livePreviewTranscriptTask
	livePreviewTranscriptMuted
)

type livePreviewTranscriptRow struct {
	kind livePreviewTranscriptKind
	text string
}

func livePreviewTranscriptTail(f *feature.Feature, sess session.SessionView, width, height int) []string {
	if width < livePreviewTailMinContentWidth {
		return nil
	}
	if sess == nil || sess.MessageLog() == nil {
		return nil
	}

	rows := livePreviewTranscriptRows(sess.MessageLog().LastN(livePreviewTranscriptMessageLimit))
	if len(rows) == 0 {
		return nil
	}

	if height > 0 {
		maxRows := height - livePreviewBaseRows - livePreviewTailOverheadRows
		if maxRows <= 0 {
			return nil
		}
		if len(rows) > maxRows {
			rows = rows[len(rows)-maxRows:]
		}
	}

	lines := make([]string, 0, len(rows))
	for _, row := range rows {
		lines = append(lines, renderLivePreviewTranscriptRow(row, width))
	}
	return lines
}

func livePreviewTranscriptRows(msgs []llm.SDKMessage) []livePreviewTranscriptRow {
	var rows []livePreviewTranscriptRow
	toolNames := make(map[string]string)
	lastKey := ""

	appendRow := func(row livePreviewTranscriptRow) {
		row.text = singleLine(row.text)
		if row.text == "" {
			return
		}
		key := fmt.Sprintf("%d:%s", row.kind, row.text)
		if key == lastKey {
			return
		}
		rows = append(rows, row)
		lastKey = key
	}

	for _, msg := range msgs {
		switch {
		case msg.StreamDeltaType != "" || msg.Type == "stream_event":
			continue
		case msg.Init != nil:
			continue
		case msg.Assistant != nil:
			if msg.Subtype == "partial" {
				continue
			}
			for _, block := range msg.Assistant.Message.Content {
				switch {
				case block.IsText():
					appendRow(livePreviewTranscriptRow{kind: livePreviewTranscriptAssistant, text: block.Text})
				case block.IsToolUse():
					if block.ID != "" && block.Name != "" {
						toolNames[block.ID] = block.Name
					}
					appendRow(livePreviewToolUseRow(block))
				}
			}
		case msg.User != nil:
			for _, block := range msg.User.Message.Content {
				if !block.IsToolResult() {
					continue
				}
				appendRow(livePreviewToolResultRow(block, toolNames[block.ToolUseID]))
			}
		case msg.Result != nil:
			appendRow(livePreviewResultRow(msg.Result))
		case msg.ControlRequest != nil:
			appendRow(livePreviewControlRequestRow(msg.ControlRequest))
		case msg.TaskStarted != nil:
			appendRow(livePreviewTaskStartedRow(msg.TaskStarted))
		case msg.TaskProgress != nil:
			appendRow(livePreviewTaskProgressRow(msg.TaskProgress))
		case msg.TaskNotification != nil:
			appendRow(livePreviewTaskNotificationRow(msg.TaskNotification))
		case msg.Compact != nil:
			appendRow(livePreviewTranscriptRow{kind: livePreviewTranscriptMuted, text: "Context compacted"})
		}
	}
	return rows
}

func livePreviewToolUseRow(block llm.ContentBlock) livePreviewTranscriptRow {
	if block.Name == "AskUserQuestion" {
		return livePreviewTranscriptRow{kind: livePreviewTranscriptQuestion, text: "AskUser: " + livePreviewAskUserSummary(block.Input)}
	}
	text := block.Name
	if detail := livePreviewToolInputSummary(block.Name, block.Input); detail != "" {
		text += ": " + detail
	}
	return livePreviewTranscriptRow{kind: livePreviewTranscriptTool, text: text}
}

func livePreviewToolResultRow(block llm.ContentBlock, toolName string) livePreviewTranscriptRow {
	name := toolName
	if name == "" {
		name = "Tool"
	}
	detail := livePreviewJSONSummary(block.Content)
	if block.IsError {
		return livePreviewTranscriptRow{kind: livePreviewTranscriptWarning, text: name + " failed: " + detail}
	}
	return livePreviewTranscriptRow{kind: livePreviewTranscriptResult, text: name + " result: " + detail}
}

func livePreviewResultRow(result *llm.ResultMessage) livePreviewTranscriptRow {
	if result == nil {
		return livePreviewTranscriptRow{}
	}
	if result.IsSuccess() {
		text := "Turn complete"
		if detail := singleLine(result.Result); detail != "" {
			text += ": " + detail
		} else if result.TotalCostUSD > 0 {
			text += ": " + formatCost(result.TotalCostUSD)
		}
		return livePreviewTranscriptRow{kind: livePreviewTranscriptResult, text: text}
	}

	label := "Turn stopped"
	kind := livePreviewTranscriptWarning
	if result.Subtype == "error" || result.IsError {
		label = "Turn failed"
		kind = livePreviewTranscriptWarning
	}
	detail := singleLine(result.Result)
	if detail == "" {
		detail = result.Subtype
	}
	return livePreviewTranscriptRow{kind: kind, text: label + ": " + detail}
}

func livePreviewControlRequestRow(req *llm.ControlRequestMessage) livePreviewTranscriptRow {
	if req == nil {
		return livePreviewTranscriptRow{}
	}
	if req.Request.Subtype == "hook_callback" {
		return livePreviewTranscriptRow{}
	}
	if req.Request.ToolName == "AskUserQuestion" {
		return livePreviewTranscriptRow{kind: livePreviewTranscriptQuestion, text: "AskUser: " + livePreviewAskUserSummary(req.Request.Input)}
	}
	if req.Request.Subtype == "can_use_tool" && req.Request.ToolName != "" {
		text := "Permission: " + req.Request.ToolName
		if detail := livePreviewToolInputSummary(req.Request.ToolName, req.Request.Input); detail != "" {
			text += ": " + detail
		}
		return livePreviewTranscriptRow{kind: livePreviewTranscriptWarning, text: text}
	}
	return livePreviewTranscriptRow{}
}

func livePreviewTaskStartedRow(msg *llm.TaskStartedMessage) livePreviewTranscriptRow {
	text := "Task started"
	if detail := firstNonEmpty(msg.Description, msg.TaskType, msg.TaskID); detail != "" {
		text += ": " + detail
	}
	return livePreviewTranscriptRow{kind: livePreviewTranscriptTask, text: text}
}

func livePreviewTaskProgressRow(msg *llm.TaskProgressMessage) livePreviewTranscriptRow {
	text := "Task progress"
	if detail := firstNonEmpty(msg.Description, msg.TaskID); detail != "" {
		text += ": " + detail
	}
	if msg.LastToolName != "" {
		text += " via " + msg.LastToolName
	}
	return livePreviewTranscriptRow{kind: livePreviewTranscriptTask, text: text}
}

func livePreviewTaskNotificationRow(msg *llm.TaskNotificationMessage) livePreviewTranscriptRow {
	status := firstNonEmpty(msg.Status, "notification")
	text := "Task " + status
	if detail := firstNonEmpty(msg.Summary, msg.OutputFile, msg.TaskID); detail != "" {
		text += ": " + detail
	}
	kind := livePreviewTranscriptTask
	if status == "failed" || status == "error" {
		kind = livePreviewTranscriptWarning
	}
	return livePreviewTranscriptRow{kind: kind, text: text}
}

func renderLivePreviewTranscriptRow(row livePreviewTranscriptRow, width int) string {
	glyph := livePreviewTranscriptGlyph(row.kind)
	style := MutedStyle
	switch row.kind {
	case livePreviewTranscriptAssistant:
		style = SubtitleStyle
	case livePreviewTranscriptTool:
		style = WarningStyle
	case livePreviewTranscriptResult:
		style = SuccessStyle
	case livePreviewTranscriptWarning:
		style = ErrorStyle
	case livePreviewTranscriptQuestion:
		style = ReviewStyle
	case livePreviewTranscriptTask:
		style = chatThinkingStyle
	case livePreviewTranscriptMuted:
		style = MutedStyle
	}
	return ansi.Truncate("  "+style.Render(glyph+" "+row.text), width, "…")
}

func livePreviewTranscriptGlyph(kind livePreviewTranscriptKind) string {
	switch kind {
	case livePreviewTranscriptAssistant:
		return ">"
	case livePreviewTranscriptTool:
		return "$"
	case livePreviewTranscriptResult:
		return "="
	case livePreviewTranscriptWarning:
		return "!"
	case livePreviewTranscriptQuestion:
		return "?"
	case livePreviewTranscriptTask:
		return "*"
	default:
		return "-"
	}
}

func livePreviewTailBannerLine(f *feature.Feature, sess session.SessionView, width int) string {
	return ansi.Truncate("  "+ReviewStyle.Render("- "+livePreviewTailBannerLabel(f, sess)), width, "…")
}

func livePreviewTailBannerLabel(f *feature.Feature, sess session.SessionView) string {
	if label, _, ok := activePublishedCycleStatus(f); ok {
		return "Current: " + label
	}

	phase := feature.Phase(-1)
	if sess != nil {
		phase = sess.Phase()
	} else if f != nil {
		phase = f.CurrentPhase
	}
	if phase == feature.Phase(-1) {
		return "Current: Unknown"
	}

	parts := []string{phase.String()}
	if f != nil && phase == feature.PhaseImplement {
		if f.CurrentRoadmapPhase > 0 && f.TotalRoadmapPhases > 0 {
			parts = append(parts, fmt.Sprintf("Phase %d/%d", f.CurrentRoadmapPhase, f.TotalRoadmapPhases))
		}
		iteration := f.CurrentIteration
		if sess != nil && sess.Iteration() > 0 {
			iteration = sess.Iteration()
		}
		if iteration > 0 {
			parts = append(parts, fmt.Sprintf("Iteration %d", iteration))
		}
	}
	return "Current: " + strings.Join(parts, " · ")
}

func livePreviewToolInputSummary(toolName string, input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	if toolName == "AskUserQuestion" {
		return livePreviewAskUserSummary(input)
	}

	var parsed map[string]any
	if err := json.Unmarshal(input, &parsed); err != nil {
		return singleLine(string(input))
	}

	switch toolName {
	case "Bash":
		return stringField(parsed, "command")
	case "Read", "Write", "Edit", "MultiEdit":
		return stringField(parsed, "file_path")
	case "Glob":
		return joinNonEmpty(stringField(parsed, "pattern"), stringField(parsed, "path"))
	case "Grep":
		return joinNonEmpty(stringField(parsed, "pattern"), stringField(parsed, "path"))
	case "LS":
		return stringField(parsed, "path")
	case "WebFetch":
		return stringField(parsed, "url")
	case "Task":
		return firstNonEmpty(stringField(parsed, "description"), stringField(parsed, "prompt"))
	}
	return livePreviewJSONSummary(input)
}

func livePreviewAskUserSummary(input json.RawMessage) string {
	var parsed map[string]any
	if err := json.Unmarshal(input, &parsed); err != nil {
		return livePreviewJSONSummary(input)
	}
	if question := stringField(parsed, "question"); question != "" {
		return question
	}
	questions, ok := parsed["questions"].([]any)
	if !ok || len(questions) == 0 {
		return livePreviewJSONSummary(input)
	}
	first, ok := questions[0].(map[string]any)
	if !ok {
		return livePreviewJSONSummary(input)
	}
	question := stringField(first, "question")
	if question == "" {
		return livePreviewJSONSummary(input)
	}
	if len(questions) > 1 {
		return fmt.Sprintf("%s (+%d more)", question, len(questions)-1)
	}
	return question
}

func livePreviewJSONSummary(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return singleLine(text)
	}

	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var parts []string
		for _, block := range blocks {
			if block.Text != "" {
				parts = append(parts, block.Text)
			}
		}
		if len(parts) > 0 {
			return singleLine(strings.Join(parts, " "))
		}
	}

	var parsed any
	if err := json.Unmarshal(raw, &parsed); err == nil {
		if compact, err := json.Marshal(parsed); err == nil {
			return singleLine(string(compact))
		}
	}
	return singleLine(string(raw))
}

func stringField(values map[string]any, key string) string {
	value, ok := values[key]
	if !ok {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return singleLine(text)
}

func singleLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if out := singleLine(value); out != "" {
			return out
		}
	}
	return ""
}

func joinNonEmpty(values ...string) string {
	var parts []string
	for _, value := range values {
		if value != "" {
			parts = append(parts, value)
		}
	}
	return strings.Join(parts, " ")
}
