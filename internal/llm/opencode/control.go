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

package opencode

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// statFunc resolves the phase_complete marker on disk. It is a variable so
// tests can drive marker presence deterministically without touching the
// filesystem.
var statFunc = os.Stat

// syntheticAskUserPrefix marks AskUserQuestion request ids the tracer
// synthesizes from plain assistant text. Such questions have no native ACP
// request to answer, so the answer is delivered as a framed follow-up turn
// rather than a session/request_permission outcome.
const syntheticAskUserPrefix = "opencode-synthetic-"

// handleRequestPermission translates a session/request_permission request into a
// shared control request: a tool permission prompt (Bash/Write/WebFetch/...) or,
// when OpenCode is asking the user a question, an AskUserQuestion. It records the
// option ids the eventual answer selects, then returns the control SDKMessage.
//
// Once a terminal result has been emitted the turn is sealed: a late control
// request gets a "cancelled" outcome so OpenCode unblocks without running the
// action, and no new control request is surfaced to the session.
func (p *Protocol) handleRequestPermission(rawID json.RawMessage, params json.RawMessage) (llm.SDKMessage, bool) {
	id, err := parseID(rawID)
	if err != nil {
		p.logDebug("[opencode] session/request_permission with unparseable id %q", string(rawID))
		return llm.SDKMessage{}, false
	}

	if p.terminalEmitted() {
		// The session already has its outcome; release OpenCode without surfacing
		// a new prompt the completed session would never answer.
		_ = p.writePermissionOutcome(id, OutcomeCancelled, "")
		return llm.SDKMessage{}, false
	}

	var pp RequestPermissionParams
	if jerr := json.Unmarshal(params, &pp); jerr != nil {
		p.logDebug("[opencode] failed to parse session/request_permission params: %v", jerr)
		_ = p.writePermissionOutcome(id, OutcomeCancelled, "")
		return llm.SDKMessage{}, false
	}

	reqID := strconv.Itoa(id)
	if pp.ToolCall.Kind == ToolKindQuestion {
		return p.buildQuestionControl(reqID, pp), true
	}
	return p.buildPermissionControl(reqID, pp), true
}

// buildPermissionControl normalizes a tool-permission request into a
// can_use_tool control request and records the allow/reject option ids so an
// approve/deny decision resolves to the right ACP outcome.
func (p *Protocol) buildPermissionControl(reqID string, pp RequestPermissionParams) llm.SDKMessage {
	toolName, input := normalizePermissionInput(pp.ToolCall)

	allowID, rejectID := classifyOptionIDs(pp.Options)
	p.mu.Lock()
	if p.pendingPerms == nil {
		p.pendingPerms = make(map[string]permissionOptions)
	}
	p.pendingPerms[reqID] = permissionOptions{allowID: allowID, rejectID: rejectID}
	p.mu.Unlock()

	return llm.SDKMessage{
		Type:    "control_request",
		Subtype: "can_use_tool",
		ControlRequest: &llm.ControlRequestMessage{
			Type:      "control_request",
			RequestID: reqID,
			Request: llm.ControlRequest{
				Subtype:  "can_use_tool",
				ToolName: toolName,
				Input:    input,
			},
		},
	}
}

// normalizePermissionInput maps an ACP tool call to the normalized tool name and
// input shape the existing permission UI and cache expect. Known kinds carry the
// detail field the TUI renders (command for Bash, file_path for Write); unknown
// kinds still surface as a permission prompt with a best-effort detail so the
// user can decide rather than the tracer failing closed.
func normalizePermissionInput(tc PermissionToolCall) (string, json.RawMessage) {
	switch tc.Kind {
	case ToolKindExecute:
		return "Bash", inputObject("command", firstStringField(tc.RawInput, tc.Title, "command", "cmd", "script"))
	case ToolKindEdit:
		// OpenCode's write tool reports "filePath" (camelCase) while its edit tool
		// reports "filepath" (lowercase); accept both so an edit to a declared
		// artifact is not mistaken for the tool title and denied.
		return "Write", inputObject("file_path", firstStringField(tc.RawInput, tc.Title, "filePath", "filepath", "file_path", "path"))
	case ToolKindFetch:
		return "WebFetch", inputObject("url", firstStringField(tc.RawInput, tc.Title, "url", "uri"))
	case ToolKindSearch:
		return "WebSearch", inputObject("query", firstStringField(tc.RawInput, tc.Title, "query", "q", "search"))
	case ToolKindRead:
		return "ExternalDirectory", inputObject("path", firstStringField(tc.RawInput, tc.Title, "path", "directory", "dir"))
	case ToolKindThink:
		// OpenCode's task (subagent-spawn) permission. Surface it under the
		// canonical "Agent" tool name so the shared permission handlers
		// auto-approve subagent spawning (subagents are sandboxed) instead of
		// prompting on an opaque "think" name. The native input already carries
		// description/subagent_type, so it is passed through for display when
		// present.
		if len(tc.RawInput) > 0 {
			return "Agent", tc.RawInput
		}
		return "Agent", inputObject("subagent_type", tc.Title)
	case ToolKindOther:
		if tc.Title == "external_directory" {
			return "ExternalDirectory", inputObject("path", firstStringField(tc.RawInput, tc.Title, "path", "filepath", "filePath", "directory", "dir", "parentDir"))
		}
		return unknownPermissionInput(tc)
	default:
		// An unrecognized permission kind is still a permission the user should
		// decide on. Preserve the raw input when present so the prompt carries
		// whatever detail OpenCode supplied.
		return unknownPermissionInput(tc)
	}
}

func unknownPermissionInput(tc PermissionToolCall) (string, json.RawMessage) {
	name := tc.Kind
	if name == "" {
		name = "Tool"
	}
	if len(tc.RawInput) > 0 {
		return name, tc.RawInput
	}
	return name, inputObject("detail", tc.Title)
}

// inputObject marshals a single-field detail object for a normalized permission
// input. An empty value still yields a valid (empty-valued) object.
func inputObject(key, value string) json.RawMessage {
	b, _ := json.Marshal(map[string]string{key: value})
	return b
}

// firstStringField returns the first non-empty string value among the named keys
// of a JSON object, falling back to fallback when none are present. It tolerates
// absent or non-object raw input.
func firstStringField(raw json.RawMessage, fallback string, keys ...string) string {
	if len(raw) > 0 {
		var obj map[string]any
		if err := json.Unmarshal(raw, &obj); err == nil {
			for _, k := range keys {
				if v, ok := obj[k].(string); ok && strings.TrimSpace(v) != "" {
					return v
				}
			}
		}
	}
	return fallback
}

// classifyOptionIDs picks the option ids an approve and a deny decision select.
// It prefers the once-scoped variants and matches on the ACP option kind prefix
// (allow_*/reject_*); a missing kind leaves that direction empty so the response
// path falls back to a cancelled outcome.
func classifyOptionIDs(options []PermissionOption) (allowID, rejectID string) {
	for _, o := range options {
		switch {
		case strings.HasPrefix(o.Kind, "allow"):
			if allowID == "" || o.Kind == OptionKindAllowOnce {
				allowID = o.OptionID
			}
		case strings.HasPrefix(o.Kind, "reject"):
			if rejectID == "" || o.Kind == OptionKindRejectOnce {
				rejectID = o.OptionID
			}
		}
	}
	return allowID, rejectID
}

// RespondToControl answers a permission request as an ACP outcome: approval
// selects an allow-kind option and denial selects a reject-kind option. A denial
// with no reject option (or an unknown request) is answered "cancelled" so the
// action does not run and OpenCode still unblocks. originalInput and reason are
// accepted for interface parity; OpenCode's outcome carries neither.
func (p *Protocol) RespondToControl(requestID string, allow bool, _ json.RawMessage, _ string) error {
	id, err := strconv.Atoi(requestID)
	if err != nil {
		return fmt.Errorf("invalid opencode request id %q: %w", requestID, err)
	}

	p.mu.Lock()
	perm, ok := p.pendingPerms[requestID]
	delete(p.pendingPerms, requestID)
	p.mu.Unlock()

	if !ok {
		// No recorded options (late or out-of-band response): release OpenCode
		// without running the action.
		return p.writePermissionOutcome(id, OutcomeCancelled, "")
	}

	if allow {
		if perm.allowID != "" {
			return p.writePermissionOutcome(id, OutcomeSelected, perm.allowID)
		}
		p.logDebug("[opencode] approve for request %s had no allow option; cancelling", requestID)
		return p.writePermissionOutcome(id, OutcomeCancelled, "")
	}
	if perm.rejectID != "" {
		return p.writePermissionOutcome(id, OutcomeSelected, perm.rejectID)
	}
	return p.writePermissionOutcome(id, OutcomeCancelled, "")
}

// writePermissionOutcome sends the JSON-RPC result for a
// session/request_permission request.
func (p *Protocol) writePermissionOutcome(id int, outcome, optionID string) error {
	return p.writeJSON(PermissionResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  PermissionOutcomeResult{Outcome: PermissionOutcome{Outcome: outcome, OptionID: optionID}},
	})
}

// --- structured questions ---

// buildQuestionControl converts a question-kind permission request into an
// AskUserQuestion control request, preserving option labels, descriptions,
// recommended markers, and confidence scores. It records the answer-label ->
// optionId map so the user's selection resolves to the native ACP outcome.
func (p *Protocol) buildQuestionControl(reqID string, pp RequestPermissionParams) llm.SDKMessage {
	type claudeOption struct {
		Label       string   `json:"label"`
		Description string   `json:"description"`
		Confidence  *float64 `json:"confidence,omitempty"`
	}

	labelToOption := make(map[string]string, len(pp.Options))
	seen := make(map[string]int)
	opts := make([]claudeOption, 0, len(pp.Options))
	for _, o := range pp.Options {
		label := o.Name
		if o.Recommended && !strings.Contains(label, "(Recommended)") {
			label = label + " (Recommended)"
		}
		seen[label]++
		if seen[label] > 1 {
			label = fmt.Sprintf("%s (#%d)", label, seen[label])
		}
		labelToOption[label] = o.OptionID
		opts = append(opts, claudeOption{Label: label, Description: o.Description, Confidence: o.Confidence})
	}

	p.mu.Lock()
	if p.pendingQuestionOpts == nil {
		p.pendingQuestionOpts = make(map[string]map[string]string)
	}
	p.pendingQuestionOpts[reqID] = labelToOption
	p.mu.Unlock()

	question := strings.TrimSpace(pp.ToolCall.Title)
	if question == "" {
		question = "OpenCode is asking for your input."
	}
	inputJSON, _ := json.Marshal(map[string]any{
		"questions": []map[string]any{
			{
				"question":    question,
				"header":      "Agent Question",
				"multiSelect": false,
				"options":     opts,
			},
		},
	})

	return llm.SDKMessage{
		Type:    "control_request",
		Subtype: "can_use_tool",
		ControlRequest: &llm.ControlRequestMessage{
			Type:      "control_request",
			RequestID: reqID,
			Request: llm.ControlRequest{
				Subtype:  "can_use_tool",
				ToolName: "AskUserQuestion",
				Input:    inputJSON,
			},
		},
	}
}

// RespondToAskUser delivers the user's answer to an OpenCode question. A
// structured question with a native pending request is answered through that
// request's ACP outcome (selecting the option whose label the user chose); a
// synthetic question parsed from plain text — and a structured answer that
// matched no listed option — is delivered as a framed follow-up turn so the
// agent still receives the user's intent. annotations are accepted for interface
// parity; OpenCode carries no per-question note side-channel.
func (p *Protocol) RespondToAskUser(requestID string, questions json.RawMessage, answers map[string]string, _ map[string]llm.AskUserAnnotation) error {
	if strings.HasPrefix(requestID, syntheticAskUserPrefix) {
		return p.sendPrompt(buildAskUserAnswerEnvelope(questions, answers))
	}

	p.mu.Lock()
	labelToOption := p.pendingQuestionOpts[requestID]
	delete(p.pendingQuestionOpts, requestID)
	p.mu.Unlock()

	id, err := strconv.Atoi(requestID)
	if err != nil {
		return fmt.Errorf("invalid opencode request id %q: %w", requestID, err)
	}

	optionID := matchAnswerOption(labelToOption, answers)
	if optionID != "" {
		return p.writePermissionOutcome(id, OutcomeSelected, optionID)
	}

	// No listed option matched (e.g. a free-form answer). Release the native
	// request and deliver the answer as a follow-up turn so the agent still
	// learns what the user chose.
	if err := p.writePermissionOutcome(id, OutcomeCancelled, ""); err != nil {
		return err
	}
	return p.sendPrompt(buildAskUserAnswerEnvelope(questions, answers))
}

// matchAnswerOption returns the optionId whose label the user's answer selected,
// or "" when the answer matched no listed option. Exact label match is tried
// first, then a recommended-suffix-insensitive match.
func matchAnswerOption(labelToOption map[string]string, answers map[string]string) string {
	if len(labelToOption) == 0 {
		return ""
	}
	for _, ans := range answers {
		if id, ok := labelToOption[ans]; ok {
			return id
		}
		stripped := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(ans), "(Recommended)"))
		for label, id := range labelToOption {
			base := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(label), "(Recommended)"))
			if base != "" && base == stripped {
				return id
			}
		}
	}
	return ""
}

// terminalEmitted reports whether a terminal result has already sealed the turn.
func (p *Protocol) terminalEmitted() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.resultEmitted
}

// --- plain-text question synthesis (mirrors the Codex provider strategy) ---

// maxQuestionFormatRetries bounds the reformat-reminder turns sent for a
// plain-text question that lacked the required numbered options.
const maxQuestionFormatRetries = 2

// maybeSynthesizeQuestion inspects the final assistant text of a completed turn
// and, when it is really a user-facing question rather than a completion,
// returns a synthetic AskUserQuestion (and emit=true) instead of a success
// result. It mirrors the Codex strategy: an explicit FREE_FORM opt-out becomes
// an options-free question, numbered alternatives become structured options, and
// an unformatted question first earns one reformat-reminder turn (returning
// emit=false with no message) before falling back to a free-form question.
//
// done=false means the text is a clean completion and the caller should emit the
// normal success result.
func (p *Protocol) maybeSynthesizeQuestion(lastText string) (msg llm.SDKMessage, emit bool, done bool) {
	if strings.TrimSpace(lastText) == "" {
		return llm.SDKMessage{}, false, false
	}

	// A present phase_complete marker means the agent finished its work this
	// turn; treat the turn as a completion even if its text reads like a
	// question, so a stray '?' can never block a finished phase.
	if p.markerPresent() {
		p.resetFormatRetry()
		return llm.SDKMessage{}, false, false
	}

	if stripped, ok := trimFreeFormSentinel(lastText); ok {
		p.resetFormatRetry()
		return p.synthesizeAskUser(stripped, nil), true, true
	}

	if stem, options, ok := parseNumberedOptions(lastText); ok {
		if !parsedOptionsHaveConfidence(options) {
			if p.sendQuestionFormatReminder(lastText) {
				return llm.SDKMessage{}, false, true
			}
		}
		p.resetFormatRetry()
		return p.synthesizeAskUser(stem, options), true, true
	}

	if !textLooksLikeQuestion(lastText) {
		p.resetFormatRetry()
		return llm.SDKMessage{}, false, false
	}

	// A question without the required options, no marker: nudge it back into the
	// one-question / three-option format once (up to maxQuestionFormatRetries)
	// before falling back to a free-form synthetic question.
	if p.sendQuestionFormatReminder(lastText) {
		return llm.SDKMessage{}, false, true
	}
	p.resetFormatRetry()
	return p.synthesizeAskUser(lastText, nil), true, true
}

func (p *Protocol) sendQuestionFormatReminder(lastText string) bool {
	p.mu.Lock()
	retry := p.formatRetryCount
	p.mu.Unlock()
	if retry >= maxQuestionFormatRetries {
		return false
	}
	if err := p.sendPrompt(questionFormatReminder(lastText)); err != nil {
		p.logDebug("[opencode] failed to send reformat reminder: %v", err)
		return false
	}
	p.mu.Lock()
	p.formatRetryCount++
	p.mu.Unlock()
	return true
}

func (p *Protocol) resetFormatRetry() {
	p.mu.Lock()
	p.formatRetryCount = 0
	p.mu.Unlock()
}

// markerPresent reports whether the phase_complete marker exists on disk. A
// missing marker path is treated as absent.
func (p *Protocol) markerPresent() bool {
	if p.opts.MarkerPath == "" {
		return false
	}
	_, err := statFunc(p.opts.MarkerPath)
	return err == nil
}

// synthesizeAskUser builds an AskUserQuestion control request from a plain-text
// question and any parsed options, tagged with a synthetic request id so the
// answer is delivered as a follow-up turn.
func (p *Protocol) synthesizeAskUser(text string, options []parsedOption) llm.SDKMessage {
	p.mu.Lock()
	p.synthSeq++
	seq := p.synthSeq
	p.mu.Unlock()

	opts := make([]map[string]any, 0, len(options))
	for _, o := range options {
		opt := map[string]any{"label": o.Label, "description": o.Description}
		if o.Confidence != nil {
			opt["confidence"] = *o.Confidence
		}
		opts = append(opts, opt)
	}

	inputJSON, _ := json.Marshal(map[string]any{
		"questions": []map[string]any{
			{
				"question":    strings.TrimSpace(text),
				"header":      "Agent Question",
				"multiSelect": false,
				"options":     opts,
			},
		},
	})

	return llm.SDKMessage{
		Type:    "control_request",
		Subtype: "can_use_tool",
		ControlRequest: &llm.ControlRequestMessage{
			Type:      "control_request",
			RequestID: fmt.Sprintf("%s%d", syntheticAskUserPrefix, seq),
			Request: llm.ControlRequest{
				Subtype:  "can_use_tool",
				ToolName: "AskUserQuestion",
				Input:    inputJSON,
			},
		},
	}
}

func textLooksLikeQuestion(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	return strings.Contains(text, "?")
}

// parsedOption holds one numbered alternative extracted from a plain-text
// question.
type parsedOption struct {
	Label       string
	Description string
	Confidence  *float64
}

var numberedOptionRe = regexp.MustCompile(`^\d+\.\s+(.+)$`)
var confidenceSuffixRe = regexp.MustCompile(`(?i)\s+\[confidence:\s*(0(?:\.\d+)?|1(?:\.0+)?)\]\s*$`)
var trailingRecommendedRe = regexp.MustCompile(`(?i)\s+\(recommended\)\s*$`)

// parseNumberedOptions extracts a question stem and its numbered alternatives
// from plain text. It returns ok=false when fewer than two options are present
// or when every option itself contains "?" (a bundle of questions, not a
// stem-plus-choices).
func parseNumberedOptions(text string) (string, []parsedOption, bool) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	stem := make([]string, 0, len(lines))
	raw := make([]string, 0, 4)
	trailingStem := make([]string, 0, 2)
	inOptions := false
	inTrailingStem := false
	sawBlankAfterOptions := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if inTrailingStem {
				if len(trailingStem) > 0 && trailingStem[len(trailingStem)-1] != "" {
					trailingStem = append(trailingStem, "")
				}
				continue
			}
			if inOptions {
				if len(raw) > 0 {
					sawBlankAfterOptions = true
				}
				continue
			}
			if len(stem) > 0 && stem[len(stem)-1] != "" {
				stem = append(stem, "")
			}
			continue
		}
		if !inTrailingStem {
			if m := numberedOptionRe.FindStringSubmatch(trimmed); m != nil {
				inOptions = true
				sawBlankAfterOptions = false
				raw = append(raw, strings.TrimSpace(m[1]))
				continue
			}
		}
		if inTrailingStem {
			trailingStem = append(trailingStem, trimmed)
			continue
		}
		if inOptions {
			if sawBlankAfterOptions && looksLikeTrailingQuestion(trimmed) {
				inTrailingStem = true
				trailingStem = append(trailingStem, trimmed)
				continue
			}
			if len(raw) > 0 {
				raw[len(raw)-1] += " " + trimmed
			}
			sawBlankAfterOptions = false
			continue
		}
		stem = append(stem, trimmed)
	}

	if len(raw) < 2 {
		return "", nil, false
	}
	questionLines := 0
	for _, r := range raw {
		if strings.Contains(r, "?") {
			questionLines++
		}
	}
	if questionLines == len(raw) {
		return "", nil, false
	}

	options := make([]parsedOption, 0, len(raw))
	for _, r := range raw {
		trimmed, confidence, trailingRecommended := splitOptionConfidence(r)
		label, desc := splitOptionLabelDesc(trimmed)
		if trailingRecommended && !labelHasRecommended(label) {
			label += " (Recommended)"
		}
		if label == "" {
			return "", nil, false
		}
		options = append(options, parsedOption{Label: label, Description: desc, Confidence: confidence})
	}

	cleaned := strings.TrimSpace(strings.Join(stem, "\n"))
	if len(trailingStem) > 0 {
		cleaned = strings.TrimSpace(strings.Join(trailingStem, "\n"))
	}
	if cleaned == "" {
		cleaned = text
	}
	return cleaned, options, true
}

func looksLikeTrailingQuestion(line string) bool {
	return strings.HasSuffix(strings.TrimSpace(line), "?")
}

func parsedOptionsHaveConfidence(options []parsedOption) bool {
	for _, opt := range options {
		if opt.Confidence == nil {
			return false
		}
	}
	return len(options) > 0
}

func labelHasRecommended(label string) bool {
	return strings.Contains(strings.ToLower(label), "(recommended)")
}

func trimTrailingRecommended(raw string) (string, bool) {
	matches := trailingRecommendedRe.FindStringSubmatch(raw)
	if matches == nil {
		return raw, false
	}
	return strings.TrimSpace(raw[:len(raw)-len(matches[0])]), true
}

// trimFreeFormSentinel recognizes the explicit "FREE_FORM:" opt-out the reformat
// reminder teaches the agent to emit when a question genuinely needs a free-text
// answer. It returns the stripped text and ok=true when the sentinel is present.
func trimFreeFormSentinel(text string) (string, bool) {
	trimmed := strings.TrimLeft(text, " \t\n\r")
	const prefix = "FREE_FORM:"
	if !strings.HasPrefix(trimmed, prefix) {
		return "", false
	}
	return strings.TrimSpace(trimmed[len(prefix):]), true
}

func splitOptionLabelDesc(raw string) (string, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}
	label := raw
	desc := ""
	if idx := strings.Index(raw, ":"); idx >= 0 {
		label = strings.TrimSpace(raw[:idx])
		desc = strings.TrimSpace(raw[idx+1:])
	}
	label = strings.Trim(label, "`")
	label = strings.TrimSpace(label)
	return label, desc
}

func splitOptionConfidence(raw string) (string, *float64, bool) {
	raw = strings.TrimSpace(raw)
	raw, trailingRecommended := trimTrailingRecommended(raw)
	matches := confidenceSuffixRe.FindStringSubmatch(raw)
	if matches == nil {
		return raw, nil, trailingRecommended
	}
	confidence, err := strconv.ParseFloat(matches[1], 64)
	if err != nil {
		return raw, nil, trailingRecommended
	}
	trimmed := strings.TrimSpace(raw[:len(raw)-len(matches[0])])
	return trimmed, &confidence, trailingRecommended
}

// questionFormatReminder is the follow-up user turn sent when the agent emits a
// question that lacks the required numbered options.
func questionFormatReminder(violating string) string {
	return strings.Join([]string{
		"Your previous message was not in the required question format:",
		"",
		"> " + strings.ReplaceAll(strings.TrimSpace(violating), "\n", "\n> "),
		"",
		"Reformat and resend the question using exactly 3 numbered options, one marked (Recommended).",
		"Use this structure and output nothing else:",
		"",
		"<question stem ending with '?'>",
		"1. <Label> (Recommended): <one-line tradeoff> [confidence: 0.00]",
		"2. <Label>: <one-line tradeoff> [confidence: 0.00]",
		"3. <Label>: <one-line tradeoff> [confidence: 0.00]",
		"",
		"Only skip numbered options if the answer is inherently unconstrained (an exact version string, a free-form name, or an arbitrary identifier). In that case, prefix the question with the literal string 'FREE_FORM:' so the orchestrator knows it is intentional.",
	}, "\n")
}

// --- follow-up answer envelope ---

const askingFormatReminder = `[Reminder] When you ask your next question, follow the asking-questions format from your system prompt.`

// askUserOptionView is the subset of an AskUserQuestion option the answer
// envelope restates to the agent.
type askUserOptionView struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// askUserQuestionView is the subset of an AskUserQuestion entry the answer
// envelope restates to the agent.
type askUserQuestionView struct {
	Question string              `json:"question"`
	Options  []askUserOptionView `json:"options,omitempty"`
}

// buildAskUserAnswerEnvelope frames the user's answer as a follow-up turn. A
// bare answer is indistinguishable from a fresh directive in the prompt channel,
// so it is wrapped with the original question and options for context.
func buildAskUserAnswerEnvelope(questions json.RawMessage, answers map[string]string) string {
	parsed := parseAskUserQuestions(questions)
	byText := make(map[string]askUserQuestionView, len(parsed))
	for _, q := range parsed {
		byText[q.Question] = q
	}

	keys := make([]string, 0, len(answers))
	for q := range answers {
		keys = append(keys, q)
	}
	sort.Strings(keys)

	var sb strings.Builder
	sb.WriteString("[AskUserQuestion answer]\n")
	sb.WriteString("The user has answered your question.\n")

	for _, q := range keys {
		sb.WriteString("\nQuestion you asked:\n> ")
		sb.WriteString(strings.ReplaceAll(strings.TrimSpace(q), "\n", "\n> "))
		sb.WriteString("\n")
		if qv, ok := byText[q]; ok && len(qv.Options) > 0 {
			sb.WriteString("\nOptions you presented:\n")
			for i, opt := range qv.Options {
				fmt.Fprintf(&sb, "  %d. %s", i+1, opt.Label)
				if opt.Description != "" {
					sb.WriteString(" — ")
					sb.WriteString(opt.Description)
				}
				sb.WriteString("\n")
			}
		}
		sb.WriteString("\nUser's selected answer: ")
		sb.WriteString(answers[q])
		sb.WriteString("\n")
	}
	sb.WriteString("\n")
	sb.WriteString(askingFormatReminder)
	return strings.TrimSpace(sb.String())
}

func parseAskUserQuestions(raw json.RawMessage) []askUserQuestionView {
	if len(raw) == 0 {
		return nil
	}
	var envelope struct {
		Questions []askUserQuestionView `json:"questions"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil && len(envelope.Questions) > 0 {
		return envelope.Questions
	}
	var bare []askUserQuestionView
	if err := json.Unmarshal(raw, &bare); err == nil && len(bare) > 0 {
		return bare
	}
	return nil
}
