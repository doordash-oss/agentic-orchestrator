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
	"regexp"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// askUserQuestion holds parsed AskUserQuestion data for picker rendering.
type askUserQuestion struct {
	RawQuestion string
	Question    string
	Header      string
	MultiSelect bool
	Options     []askUserOption
}

type askUserOption struct {
	Label       string
	Description string
	Confidence  *float64
	// Preview is free-text (markdown or HTML) Claude emits when the question
	// benefits from a visual mockup; rendered in a bordered box alongside the
	// option list. Empty when Claude did not attach a preview.
	Preview string
}

// pendingAskBundle captures everything needed to re-activate a deferred
// AskUserQuestion control_request after the active one is submitted.
type pendingAskBundle struct {
	questions []askUserQuestion
	requestID string
	raw       json.RawMessage
}

// questionUIState is a per-question snapshot of selection/cursor state so
// that back/forward navigation through an AskUserQuestion batch can restore
// what the user had chosen.
type questionUIState struct {
	selectedOption int          // cursor row (incl. "Type something" slot)
	selectedMulti  map[int]bool // ticked option indices for multiSelect
	scrollOffset   int          // windowed option-list offset
	typingCustom   bool         // answer was given via "Type something" freeform
	customText     string       // stored freeform text for re-editing
	askedEmitted   bool         // true once QuestionAsked observability fired
}

var numberedAskUserOptionRe = regexp.MustCompile(`^\d+\.\s+(.+)$`)
var askUserConfidenceSuffixRe = regexp.MustCompile(`(?i)\s+\[confidence:\s*(0(?:\.\d+)?|1(?:\.0+)?)\]\s*$`)

// buildAskUserAnnotations converts the collected per-question notes map into
// the llm wire-format map; returns nil when no notes were captured so the
// response payload omits the `annotations` key entirely.
func buildAskUserAnnotations(notes map[string]string) map[string]llm.AskUserAnnotation {
	if len(notes) == 0 {
		return nil
	}
	out := make(map[string]llm.AskUserAnnotation, len(notes))
	for q, n := range notes {
		if n == "" {
			continue
		}
		out[q] = llm.AskUserAnnotation{Notes: n}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func questionUsesDirectFreeform(q askUserQuestion) bool {
	return len(q.Options) == 0
}

// parseAskUserQuestions extracts structured question data from AskUserQuestion tool input.
func parseAskUserQuestions(input json.RawMessage) []askUserQuestion {
	if len(input) == 0 {
		return nil
	}
	var parsed struct {
		Questions []struct {
			Question    string `json:"question"`
			Header      string `json:"header"`
			MultiSelect bool   `json:"multiSelect"`
			Options     []struct {
				Label       string   `json:"label"`
				Description string   `json:"description"`
				Confidence  *float64 `json:"confidence"`
				Preview     string   `json:"preview"`
			} `json:"options"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(input, &parsed); err != nil || len(parsed.Questions) == 0 {
		return nil
	}
	var result []askUserQuestion
	for _, q := range parsed.Questions {
		aq := askUserQuestion{
			RawQuestion: q.Question,
			Question:    q.Question,
			Header:      q.Header,
			MultiSelect: q.MultiSelect,
		}
		for _, o := range q.Options {
			aq.Options = append(aq.Options, askUserOption{
				Label:       o.Label,
				Description: o.Description,
				Confidence:  o.Confidence,
				Preview:     o.Preview,
			})
		}
		if len(aq.Options) == 0 {
			if cleaned, inferred, ok := inferAskUserOptionsFromQuestionText(q.Question); ok {
				aq.Question = cleaned
				aq.Options = inferred
			}
		}
		result = append(result, aq)
	}
	return result
}

func inferAskUserOptionsFromQuestionText(question string) (string, []askUserOption, bool) {
	lines := strings.Split(strings.ReplaceAll(question, "\r\n", "\n"), "\n")
	stem := make([]string, 0, len(lines))
	rawOptions := make([]string, 0, 4)
	inOptions := false

	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			if !inOptions && len(stem) > 0 && stem[len(stem)-1] != "" {
				stem = append(stem, "")
			}
			continue
		}

		if matches := numberedAskUserOptionRe.FindStringSubmatch(line); matches != nil {
			inOptions = true
			rawOptions = append(rawOptions, strings.TrimSpace(matches[1]))
			continue
		}

		if inOptions {
			if isAskUserReplyInstruction(line) {
				continue
			}
			if len(rawOptions) > 0 {
				rawOptions[len(rawOptions)-1] += " " + line
				continue
			}
		}

		if !inOptions {
			stem = append(stem, line)
		}
	}

	if len(rawOptions) < 2 {
		return "", nil, false
	}
	if looksLikeAskUserQuestionBundle(rawOptions) {
		return "", nil, false
	}

	options := make([]askUserOption, 0, len(rawOptions))
	for _, raw := range rawOptions {
		label, desc, confidence := splitAskUserOption(raw)
		if label == "" {
			return "", nil, false
		}
		options = append(options, askUserOption{Label: label, Description: desc, Confidence: confidence})
	}

	cleaned := strings.TrimSpace(strings.Join(stem, "\n"))
	if cleaned == "" {
		cleaned = question
	}
	return cleaned, options, true
}

func looksLikeAskUserQuestionBundle(rawOptions []string) bool {
	questionCount := 0
	for _, raw := range rawOptions {
		text := strings.TrimSpace(raw)
		if text == "" {
			continue
		}
		if strings.Contains(text, "?") {
			questionCount++
		}
	}
	return questionCount == len(rawOptions)
}

func splitAskUserOption(raw string) (string, string, *float64) {
	raw, confidence := splitAskUserOptionConfidence(raw)
	if raw == "" {
		return "", "", confidence
	}
	label := raw
	desc := ""
	if idx := strings.Index(raw, ":"); idx >= 0 {
		label = strings.TrimSpace(raw[:idx])
		desc = strings.TrimSpace(raw[idx+1:])
	}
	label = strings.Trim(label, "`")
	label = strings.TrimSpace(label)
	return label, desc, confidence
}

func splitAskUserOptionConfidence(raw string) (string, *float64) {
	raw = strings.TrimSpace(raw)
	matches := askUserConfidenceSuffixRe.FindStringSubmatch(raw)
	if matches == nil {
		return raw, nil
	}
	confidence, err := strconv.ParseFloat(matches[1], 64)
	if err != nil {
		return raw, nil
	}
	trimmed := strings.TrimSpace(raw[:len(raw)-len(matches[0])])
	return trimmed, &confidence
}

func isAskUserReplyInstruction(line string) bool {
	lower := strings.ToLower(strings.TrimSpace(line))
	return strings.HasPrefix(lower, "reply with ") ||
		strings.HasPrefix(lower, "respond with ") ||
		strings.HasPrefix(lower, "answer with ")
}

func cloneIntBoolMap(src map[int]bool) map[int]bool {
	if len(src) == 0 {
		return nil
	}
	out := make(map[int]bool, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

// wrappedLineCount returns how many terminal lines text occupies when
// rendered at the given width.
func wrappedLineCount(text string, width int) int {
	if width <= 0 || text == "" {
		return 1
	}
	rendered := lipgloss.NewStyle().Width(width).Render(text)
	return lipgloss.Height(rendered)
}

const questionPromptMaxLines = 6 // cap long AskUser context so choices stay visible

func questionPromptVisualLines(question string, width int) []string {
	if question == "" {
		return []string{""}
	}
	if width <= 0 {
		return strings.Split(question, "\n")
	}
	rendered := lipgloss.NewStyle().Width(width).Render(question)
	return strings.Split(rendered, "\n")
}

func questionPromptIsTruncated(question string, width int) bool {
	return len(questionPromptVisualLines(question, width)) > questionPromptMaxLines
}

func questionPromptText(question string, width int) string {
	if question == "" {
		return question
	}
	lines := questionPromptVisualLines(question, width)
	if len(lines) <= questionPromptMaxLines {
		return strings.Join(lines, "\n")
	}
	return strings.Join(lines[:questionPromptMaxLines-1], "\n") + "\n..."
}

func questionPromptLineCount(question string, width int) int {
	return wrappedLineCount(questionPromptText(question, width), width)
}

// questionOptionLineCount returns the number of wrapped terminal lines a
// single option occupies at the given content width, including its label
// and optional description/confidence lines.
func questionOptionLineCount(opt askUserOption, idx int, width int) int {
	label := fmt.Sprintf("  %d. %s", idx+1, opt.Label)
	lines := wrappedLineCount(label, width)
	if opt.Description != "" {
		desc := fmt.Sprintf("     %s", opt.Description)
		lines += wrappedLineCount(desc, width)
	}
	if opt.Confidence != nil {
		lines += wrappedLineCount(fmt.Sprintf("     Confidence: %.2f", *opt.Confidence), width)
	}
	return lines
}

// questionVisibleWindowPure computes the visible option range for a
// scrollable option list, given explicit selection/scroll state instead of
// reading a host model's fields. Returns start (inclusive) and end
// (exclusive) option indices, plus whether above/below scroll indicators
// are needed.
func questionVisibleWindowPure(options []askUserOption, selectedOption, scrollOffset, optionArea, contentWidth int) (start, end int, needAbove, needBelow bool) {
	totalOptions := len(options)

	totalLines := 0
	for i, o := range options {
		totalLines += questionOptionLineCount(o, i, contentWidth)
	}

	if totalLines <= optionArea {
		return 0, totalOptions, false, false
	}

	start = scrollOffset
	if start >= totalOptions {
		start = totalOptions - 1
	}
	if start < 0 {
		start = 0
	}

	needAbove = start > 0
	budget := optionArea
	if needAbove {
		budget--
	}

	usedLines := 0
	end = start
	for i := start; i < totalOptions; i++ {
		ol := questionOptionLineCount(options[i], i, contentWidth)
		if usedLines+ol > budget {
			break
		}
		usedLines += ol
		end = i + 1
	}

	needBelow = end < totalOptions
	if needBelow {
		for end > start && usedLines > budget-1 {
			end--
			ol := questionOptionLineCount(options[end], end, contentWidth)
			usedLines -= ol
		}
	}

	_ = selectedOption // selection doesn't affect the window itself, only which row the caller scrolls to keep visible (see updateQuestionScrollOffset in each host)
	return start, end, needAbove, needBelow
}

// renderQuestionOptionsBlock renders the numbered option list for a single
// AskUserQuestion, given explicit selection state. Mirrors the option-list
// portion of attach.go's renderQuestion — cursor + bold-brand highlight on
// the focused row, "[ ]"/"[x]" checkboxes when MultiSelect, muted
// description/confidence lines, and "N more above/below" scroll indicators.
func renderQuestionOptionsBlock(q askUserQuestion, selectedOption int, selectedMulti map[int]bool, start, end int, needAbove, needBelow bool, contentWidth int) string {
	selectedLabel := lipgloss.NewStyle().Foreground(colorBrand).Bold(true)
	normalLabel := lipgloss.NewStyle()
	descStyle := MutedStyle

	var b strings.Builder
	if needAbove {
		b.WriteString(MutedStyle.Render(fmt.Sprintf("  ↑ %d more above", start)))
		b.WriteByte('\n')
	}
	for i := start; i < end; i++ {
		opt := q.Options[i]
		cursor := "  "
		labelStyle := normalLabel
		if i == selectedOption {
			cursor = "> "
			labelStyle = selectedLabel
		}
		if q.MultiSelect {
			checkbox := "[ ] "
			if selectedMulti[i] {
				checkbox = "[x] "
			}
			writeWrappedQuestionLine(&b, labelStyle.Render(fmt.Sprintf("%s%d. %s%s", cursor, i+1, checkbox, opt.Label)), contentWidth)
		} else {
			writeWrappedQuestionLine(&b, labelStyle.Render(fmt.Sprintf("%s%d. %s", cursor, i+1, opt.Label)), contentWidth)
		}
		if opt.Description != "" {
			writeWrappedQuestionLine(&b, descStyle.Render(fmt.Sprintf("     %s", opt.Description)), contentWidth)
		}
		if opt.Confidence != nil {
			writeWrappedQuestionLine(&b, descStyle.Render(fmt.Sprintf("     Confidence: %.2f", *opt.Confidence)), contentWidth)
		}
	}
	if needBelow {
		b.WriteString(MutedStyle.Render(fmt.Sprintf("  ↓ %d more below", len(q.Options)-end)))
		b.WriteByte('\n')
	}
	return b.String()
}

func writeWrappedQuestionLine(b *strings.Builder, rendered string, width int) {
	b.WriteString(wrapForViewport(rendered, width))
	b.WriteByte('\n')
}

// renderQuestionFooterHint renders the contextual footer hint below a
// question's option list — mirrors attach.go's hint-assembly in
// renderQuestion's default branch.
func renderQuestionFooterHint(q askUserQuestion, questionIdx, totalQuestions int, canBack, canForward, promptTruncated bool, extraHint string) string {
	hintStyle := MutedStyle
	var hint string
	if q.MultiSelect {
		hint = "Space to toggle · Enter to submit · ↑/↓ to navigate"
	} else {
		hint = "Enter to select · ↑/↓ to navigate"
	}
	if promptTruncated {
		hint += " · ? full question"
	}
	if extraHint != "" {
		hint += " · " + extraHint
	}
	switch {
	case canBack && canForward:
		hint += " · ←/→ prev/next question"
	case canBack:
		hint += " · ← prev question"
	case canForward:
		hint += " · → next question"
	}
	if totalQuestions > 1 {
		hint += fmt.Sprintf(" · Question %d of %d", questionIdx+1, totalQuestions)
	}
	return hintStyle.Render(hint)
}

// renderExpandedQuestionBody returns the scrolled, wrapped lines of a
// question's full text for the "?" expanded-prompt view, windowed to
// lineBudget lines starting at scroll.
func renderExpandedQuestionBody(q askUserQuestion, contentWidth, scroll, lineBudget int) []string {
	lines := questionPromptVisualLines(q.Question, contentWidth)
	if len(lines) == 0 {
		lines = []string{""}
	}
	if scroll < 0 {
		scroll = 0
	}
	if scroll >= len(lines) {
		scroll = max(len(lines)-1, 0)
	}
	end := min(scroll+lineBudget, len(lines))
	return lines[scroll:end]
}
