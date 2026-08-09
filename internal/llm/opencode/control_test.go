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
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// TestNormalizePermissionInput_EditFilepathLowercase guards the bug where the
// OpenCode edit tool reports its target under the lowercase "filepath" key (with
// a "diff"), unlike the write tool's camelCase "filePath". When unrecognized, the
// path falls back to the tool title ("edit") and a bounded-helper handler denies
// the write to its own feedback artifact, aborting the helper.
func TestNormalizePermissionInput_EditFilepathLowercase(t *testing.T) {
	want := "/x/validate-architecture/validation-architecture-feedback.md"
	tc := PermissionToolCall{
		Kind:     ToolKindEdit,
		Title:    "edit",
		RawInput: json.RawMessage(`{"filepath":"` + want + `","diff":"--- a\n+++ b\n"}`),
	}
	name, input := normalizePermissionInput(tc)
	if name != "Write" {
		t.Fatalf("toolName = %q, want Write", name)
	}
	var got struct {
		FilePath string `json:"file_path"`
	}
	if err := json.Unmarshal(input, &got); err != nil {
		t.Fatalf("unmarshal normalized input: %v", err)
	}
	if got.FilePath != want {
		t.Fatalf("file_path = %q, want %q (edit tool uses lowercase \"filepath\")", got.FilePath, want)
	}
}

// TestNormalizePermissionInput_OtherExternalDirectory guards the observed
// OpenCode shape for external-directory access: recent runs emitted
// kind="other" with filepath/parentDir instead of kind="read" or title
// "external_directory". That must still map to ExternalDirectory so safe reads
// from mounted context roots are auto-approved and not displayed as opaque
// "other" prompts.
func TestNormalizePermissionInput_OtherExternalDirectory(t *testing.T) {
	parent := "/Users/x/.agentic-workflow/knowledge-base/repo"
	tc := PermissionToolCall{
		Kind:     ToolKindOther,
		Title:    parent,
		RawInput: json.RawMessage(`{"filepath":"` + parent + `/index.md","parentDir":"` + parent + `"}`),
	}

	name, input := normalizePermissionInput(tc)
	if name != "ExternalDirectory" {
		t.Fatalf("toolName = %q, want ExternalDirectory", name)
	}
	var got struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(input, &got); err != nil {
		t.Fatalf("unmarshal normalized input: %v", err)
	}
	if got.Path != parent {
		t.Fatalf("path = %q, want %q", got.Path, parent)
	}
}

// TestNormalizePermissionInput_OtherCommand guards the observed OpenCode shape
// for command-level path permission checks: a shell command can arrive as
// kind="other" with command/directories/patterns. It should normalize to Bash
// so prompts are recognizable and remembered Bash rules can match them.
func TestNormalizePermissionInput_OtherCommand(t *testing.T) {
	command := "cd /repo && cat > /tmp/debug-test.ts << 'EOF'\nEOF\nnpx vitest run"
	tc := PermissionToolCall{
		Kind:     ToolKindOther,
		Title:    command,
		RawInput: json.RawMessage(`{"command":"` + strings.ReplaceAll(command, "\n", `\n`) + `","directories":["/repo","/tmp"],"patterns":["/repo/*","/tmp/*"]}`),
	}

	name, input := normalizePermissionInput(tc)
	if name != "Bash" {
		t.Fatalf("toolName = %q, want Bash", name)
	}
	var got struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(input, &got); err != nil {
		t.Fatalf("unmarshal normalized input: %v", err)
	}
	if got.Command != command {
		t.Fatalf("command = %q, want %q", got.Command, command)
	}
}

// permissionRequestLine builds a session/request_permission server request with
// the given tool-call kind, raw input, and a standard allow/reject option set.
func permissionRequestLine(t *testing.T, id int, kind, title string, rawInput map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(rawInput)
	if err != nil {
		t.Fatalf("marshal rawInput: %v", err)
	}
	return serverRequestLine(t, id, requestPermissionMethod, map[string]any{
		"sessionId": "ses_x",
		"toolCall": map[string]any{
			"toolCallId": "tc_1",
			"title":      title,
			"kind":       kind,
			"rawInput":   json.RawMessage(raw),
		},
		"options": []map[string]any{
			{"optionId": "opt-allow-once", "name": "Allow", "kind": OptionKindAllowOnce},
			{"optionId": "opt-allow-always", "name": "Always allow", "kind": OptionKindAllowAlways},
			{"optionId": "opt-reject", "name": "Reject", "kind": OptionKindRejectOnce},
		},
	})
}

// TestParseLine_PermissionRequestsSurfaceAsControl proves each permission
// surface (shell, edit, web fetch/search, external directory) becomes a shared
// can_use_tool control request carrying a stable request id, a normalized tool
// name, and raw input detail the existing permission UI/cache can use — instead
// of failing closed (Task 1).
func TestParseLine_PermissionRequestsSurfaceAsControl(t *testing.T) {
	cases := []struct {
		name      string
		kind      string
		title     string
		rawInput  map[string]any
		wantTool  string
		detailKey string // key the normalized Input must carry
		detailVal string
	}{
		{"shell", ToolKindExecute, "Run tests", map[string]any{"command": "go test ./..."}, "Bash", "command", "go test ./..."},
		{"edit", ToolKindEdit, "Edit main.go", map[string]any{"filePath": "/repo/main.go"}, "Write", "file_path", "/repo/main.go"},
		{"web fetch", ToolKindFetch, "Fetch docs", map[string]any{"url": "https://example.com"}, "WebFetch", "url", "https://example.com"},
		{"web search", ToolKindSearch, "Search", map[string]any{"query": "golang acp"}, "WebSearch", "query", "golang acp"},
		{"external dir", ToolKindRead, "Read /etc", map[string]any{"path": "/etc/hosts"}, "ExternalDirectory", "path", "/etc/hosts"},
		{"subagent spawn", ToolKindThink, "Research api-surface", map[string]any{"subagent_type": "api-surface-researcher", "description": "Research api-surface"}, "Agent", "subagent_type", "api-surface-researcher"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, _, _ := newPostHandshakeProtocol(t)
			const reqID = 4242
			msgs := mustParse(t, p, permissionRequestLine(t, reqID, tc.kind, tc.title, tc.rawInput))

			if len(msgs) != 1 || msgs[0].ControlRequest == nil {
				t.Fatalf("%s permission produced %+v, want one control_request", tc.name, msgs)
			}
			cr := msgs[0].ControlRequest
			if cr.Request.Subtype != "can_use_tool" {
				t.Fatalf("control subtype = %q, want can_use_tool", cr.Request.Subtype)
			}
			if cr.RequestID != "4242" {
				t.Fatalf("control request id = %q, want stable \"4242\"", cr.RequestID)
			}
			if cr.Request.ToolName != tc.wantTool {
				t.Fatalf("normalized tool = %q, want %q", cr.Request.ToolName, tc.wantTool)
			}
			var input map[string]any
			if err := json.Unmarshal(cr.Request.Input, &input); err != nil {
				t.Fatalf("control input not an object: %v (%s)", err, cr.Request.Input)
			}
			if got, _ := input[tc.detailKey].(string); got != tc.detailVal {
				t.Fatalf("input[%q] = %v, want %q", tc.detailKey, input[tc.detailKey], tc.detailVal)
			}
			// A permission control request is NOT a terminal result and must not
			// seal the turn — the prompt can still complete afterward.
			if msgs[0].Result != nil {
				t.Fatalf("permission control request carried a terminal result: %+v", msgs[0].Result)
			}
		})
	}
}

// TestRespondToControl_ApprovalAndDenialSelectACPOutcomes proves user approval
// selects an allow-kind option and denial selects a reject-kind option, both
// answered back as ACP permission outcomes keyed by the request id — rather than
// bypassing the provider adapter (Task 1).
func TestRespondToControl_ApprovalAndDenialSelectACPOutcomes(t *testing.T) {
	t.Run("approve", func(t *testing.T) {
		p, buf, _ := newPostHandshakeProtocol(t)
		const reqID = 51
		mustParse(t, p, permissionRequestLine(t, reqID, ToolKindExecute, "Run", map[string]any{"command": "ls"}))

		if err := p.RespondToControl("51", true, nil, ""); err != nil {
			t.Fatalf("RespondToControl(allow) error: %v", err)
		}
		out := decodePermissionResponse(t, buf.lastLine(t))
		if out.ID != 51 {
			t.Fatalf("response id = %d, want 51", out.ID)
		}
		if out.Result.Outcome.Outcome != OutcomeSelected || out.Result.Outcome.OptionID != "opt-allow-once" {
			t.Fatalf("approve outcome = %+v, want selected opt-allow-once", out.Result.Outcome)
		}
	})

	t.Run("deny", func(t *testing.T) {
		p, buf, _ := newPostHandshakeProtocol(t)
		const reqID = 52
		mustParse(t, p, permissionRequestLine(t, reqID, ToolKindEdit, "Edit", map[string]any{"filePath": "/x"}))

		if err := p.RespondToControl("52", false, nil, "user denied"); err != nil {
			t.Fatalf("RespondToControl(deny) error: %v", err)
		}
		out := decodePermissionResponse(t, buf.lastLine(t))
		if out.Result.Outcome.Outcome != OutcomeSelected || out.Result.Outcome.OptionID != "opt-reject" {
			t.Fatalf("deny outcome = %+v, want selected opt-reject", out.Result.Outcome)
		}
	})
}

// TestRespondToControl_DenyWithoutRejectOptionCancels proves a denial of a
// permission request that offers no reject-kind option is answered as a
// "cancelled" outcome so OpenCode still unblocks and the action is not run.
func TestRespondToControl_DenyWithoutRejectOptionCancels(t *testing.T) {
	p, buf, _ := newPostHandshakeProtocol(t)
	const reqID = 60
	mustParse(t, p, serverRequestLine(t, reqID, requestPermissionMethod, map[string]any{
		"sessionId": "ses_x",
		"toolCall":  map[string]any{"kind": ToolKindExecute, "title": "Run", "rawInput": map[string]any{"command": "ls"}},
		"options": []map[string]any{
			{"optionId": "only-allow", "name": "Allow", "kind": OptionKindAllowOnce},
		},
	}))
	if err := p.RespondToControl("60", false, nil, "no"); err != nil {
		t.Fatalf("RespondToControl(deny) error: %v", err)
	}
	out := decodePermissionResponse(t, buf.lastLine(t))
	if out.Result.Outcome.Outcome != OutcomeCancelled {
		t.Fatalf("deny-without-reject outcome = %+v, want cancelled", out.Result.Outcome)
	}
}

func decodePermissionResponse(t *testing.T, line []byte) PermissionResponse {
	t.Helper()
	var out PermissionResponse
	if err := json.Unmarshal(line, &out); err != nil {
		t.Fatalf("response is not a PermissionResponse: %v (raw %q)", err, line)
	}
	return out
}

// --- question helpers + structured-question tests (Task 3) ---

type renderedOption struct {
	Label       string   `json:"label"`
	Description string   `json:"description"`
	Confidence  *float64 `json:"confidence"`
}

type renderedQuestion struct {
	Question    string           `json:"question"`
	Header      string           `json:"header"`
	MultiSelect bool             `json:"multiSelect"`
	Options     []renderedOption `json:"options"`
}

// askUserQuestionsFrom decodes the {"questions":[...]} envelope an AskUserQuestion
// control request carries in its Input.
func askUserQuestionsFrom(t *testing.T, cr *llm.ControlRequestMessage) []renderedQuestion {
	t.Helper()
	if cr == nil || cr.Request.ToolName != "AskUserQuestion" {
		t.Fatalf("control request is not an AskUserQuestion: %+v", cr)
	}
	var env struct {
		Questions []renderedQuestion `json:"questions"`
	}
	if err := json.Unmarshal(cr.Request.Input, &env); err != nil {
		t.Fatalf("AskUserQuestion input not decodable: %v (%s)", err, cr.Request.Input)
	}
	return env.Questions
}

// structuredQuestionLine builds a session/request_permission request whose tool
// call is a question, with the given answer options.
func structuredQuestionLine(t *testing.T, id int, question string, options []map[string]any) []byte {
	t.Helper()
	return serverRequestLine(t, id, requestPermissionMethod, map[string]any{
		"sessionId": "ses_x",
		"toolCall":  map[string]any{"kind": ToolKindQuestion, "title": question},
		"options":   options,
	})
}

// TestParseLine_StructuredQuestionBecomesAskUserQuestion proves a question-kind
// permission request becomes a shared AskUserQuestion control request whose
// ToolName routes the session to the help-waiting state, preserving option
// labels, descriptions, recommended markers, and confidence scores (Task 3).
func TestParseLine_StructuredQuestionBecomesAskUserQuestion(t *testing.T) {
	p, _, _ := newPostHandshakeProtocol(t)
	const reqID = 90
	msgs := mustParse(t, p, structuredQuestionLine(t, reqID, "Which migration strategy?", []map[string]any{
		{"optionId": "o1", "name": "Online", "description": "No downtime", "recommended": true, "confidence": 0.81},
		{"optionId": "o2", "name": "Offline", "description": "Simpler, needs a window", "confidence": 0.4},
	}))
	if len(msgs) != 1 || msgs[0].ControlRequest == nil {
		t.Fatalf("structured question produced %+v, want one control_request", msgs)
	}
	cr := msgs[0].ControlRequest
	// ToolName "AskUserQuestion" is what drives the session to help-waiting.
	if cr.Request.ToolName != "AskUserQuestion" {
		t.Fatalf("tool name = %q, want AskUserQuestion (drives help-waiting)", cr.Request.ToolName)
	}
	if cr.RequestID != "90" {
		t.Fatalf("request id = %q, want \"90\"", cr.RequestID)
	}
	qs := askUserQuestionsFrom(t, cr)
	if len(qs) != 1 || qs[0].Question != "Which migration strategy?" {
		t.Fatalf("question = %+v, want the structured stem", qs)
	}
	if len(qs[0].Options) != 2 {
		t.Fatalf("options = %+v, want 2", qs[0].Options)
	}
	o1 := qs[0].Options[0]
	if o1.Label != "Online (Recommended)" {
		t.Fatalf("option 1 label = %q, want recommended marker appended", o1.Label)
	}
	if o1.Description != "No downtime" || o1.Confidence == nil || *o1.Confidence != 0.81 {
		t.Fatalf("option 1 = %+v, want description+confidence preserved", o1)
	}
	if msgs[0].Result != nil {
		t.Fatalf("structured question carried a terminal result: %+v", msgs[0].Result)
	}
}

// TestRespondToAskUser_StructuredAnswerSelectsNativeOption proves answering a
// structured question selects the matching option's id through the native ACP
// outcome — resuming via the provider protocol's pending request (Task 3).
func TestRespondToAskUser_StructuredAnswerSelectsNativeOption(t *testing.T) {
	p, buf, _ := newPostHandshakeProtocol(t)
	const reqID = 91
	msgs := mustParse(t, p, structuredQuestionLine(t, reqID, "Pick one", []map[string]any{
		{"optionId": "opt-online", "name": "Online", "recommended": true},
		{"optionId": "opt-offline", "name": "Offline"},
	}))
	raw := msgs[0].ControlRequest.Request.Input

	answers := map[string]string{"Pick one": "Online (Recommended)"}
	if err := p.RespondToAskUser("91", raw, answers, nil); err != nil {
		t.Fatalf("RespondToAskUser error: %v", err)
	}
	out := decodePermissionResponse(t, buf.lastLine(t))
	if out.ID != 91 || out.Result.Outcome.Outcome != OutcomeSelected || out.Result.Outcome.OptionID != "opt-online" {
		t.Fatalf("structured answer outcome = %+v, want selected opt-online", out.Result.Outcome)
	}
}

// TestRespondToAskUser_StructuredFreeFormFallsBackToFollowUpTurn proves a
// structured answer that matches no listed option still releases the native
// request (cancelled) and delivers the user's intent as a framed follow-up turn
// so the agent is not left blocked (Task 3).
func TestRespondToAskUser_StructuredFreeFormFallsBackToFollowUpTurn(t *testing.T) {
	p, buf, _ := newPostHandshakeProtocol(t)
	const reqID = 92
	msgs := mustParse(t, p, structuredQuestionLine(t, reqID, "Pick one", []map[string]any{
		{"optionId": "opt-a", "name": "A"},
		{"optionId": "opt-b", "name": "B"},
	}))
	raw := msgs[0].ControlRequest.Request.Input

	if err := p.RespondToAskUser("92", raw, map[string]string{"Pick one": "Actually do C instead"}, nil); err != nil {
		t.Fatalf("RespondToAskUser error: %v", err)
	}
	// The last outbound line is the follow-up turn carrying the answer envelope.
	var followUp Request
	if err := json.Unmarshal(buf.lastLine(t), &followUp); err != nil {
		t.Fatalf("expected a follow-up session/prompt: %v (%q)", err, buf.String())
	}
	if followUp.Method != "session/prompt" {
		t.Fatalf("follow-up method = %q, want session/prompt", followUp.Method)
	}
	// encoding/json HTML-escapes the quoted prompt marker as \\u003e on the
	// wire; normalize it so this contract checks the framed prompt text.
	wire := strings.ReplaceAll(buf.String(), `\u003e`, ">")
	for _, want := range []string{
		`"outcome":"cancelled"`,
		"[AskUserQuestion answer]",
		"> Pick one",
		"User's selected answer: Actually do C instead",
	} {
		if !strings.Contains(wire, want) {
			t.Errorf("custom-answer wire output missing %q: %s", want, wire)
		}
	}
}

// --- plain-text question synthesis tests (Task 3) ---

// streamAssistantText feeds an agent_message_chunk so the protocol accumulates
// the assistant's final text for end-of-turn question detection.
func streamAssistantText(t *testing.T, p *Protocol, text string) {
	t.Helper()
	mustParse(t, p, notificationLine(t, "session/update", map[string]any{
		"sessionId": "ses_x",
		"update":    map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": text}},
	}))
}

func endTurn(t *testing.T, p *Protocol, promptID int) []llm.SDKMessage {
	t.Helper()
	return mustParse(t, p, responseLine(t, promptID, map[string]any{"stopReason": "end_turn"}))
}

// TestPromptEndTurn_FreeFormQuestionSynthesizesAskUser proves the explicit
// FREE_FORM opt-out becomes a synthetic AskUserQuestion with no options — the
// free-form exception for inherently unconstrained answers — instead of a
// success result (Task 3).
func TestPromptEndTurn_FreeFormQuestionSynthesizesAskUser(t *testing.T) {
	p, _, promptID := newPostHandshakeProtocol(t)
	streamAssistantText(t, p, "FREE_FORM: What exact version string should I tag the release with?")
	msgs := endTurn(t, p, promptID)
	if len(msgs) != 1 || msgs[0].ControlRequest == nil {
		t.Fatalf("free-form question produced %+v, want one AskUserQuestion control request", msgs)
	}
	if msgs[0].Result != nil {
		t.Fatalf("free-form question emitted a terminal result: %+v", msgs[0].Result)
	}
	qs := askUserQuestionsFrom(t, msgs[0].ControlRequest)
	if len(qs) != 1 || len(qs[0].Options) != 0 {
		t.Fatalf("free-form question = %+v, want one question with no options", qs)
	}
	if !strings.Contains(qs[0].Question, "exact version string") {
		t.Fatalf("free-form stem = %q, want the FREE_FORM-stripped text", qs[0].Question)
	}
	if !strings.HasPrefix(msgs[0].ControlRequest.RequestID, syntheticAskUserPrefix) {
		t.Fatalf("synthetic request id = %q, want %q prefix", msgs[0].ControlRequest.RequestID, syntheticAskUserPrefix)
	}
}

// TestPromptEndTurn_NumberedOptionsSynthesizeAskUserWithConfidence proves a
// well-formatted numbered question becomes a structured AskUserQuestion that
// preserves labels, descriptions, recommended markers, and confidence scores
// inferred from the text (Task 3).
func TestPromptEndTurn_NumberedOptionsSynthesizeAskUserWithConfidence(t *testing.T) {
	p, _, promptID := newPostHandshakeProtocol(t)
	streamAssistantText(t, p, strings.Join([]string{
		"Which database should I target?",
		"1. Postgres (Recommended): Mature and well-supported. [confidence: 0.88]",
		"2. MySQL: Familiar but fewer features. [confidence: 0.40]",
		"3. SQLite: Simplest but single-writer. [confidence: 0.20]",
	}, "\n"))
	msgs := endTurn(t, p, promptID)
	if len(msgs) != 1 || msgs[0].ControlRequest == nil || msgs[0].Result != nil {
		t.Fatalf("numbered question produced %+v, want one AskUserQuestion control request and no result", msgs)
	}
	qs := askUserQuestionsFrom(t, msgs[0].ControlRequest)
	if len(qs) != 1 || len(qs[0].Options) != 3 {
		t.Fatalf("numbered question = %+v, want one question with 3 options", qs)
	}
	first := qs[0].Options[0]
	if first.Label != "Postgres (Recommended)" || first.Confidence == nil || *first.Confidence != 0.88 {
		t.Fatalf("option 1 = %+v, want recommended label + confidence 0.88", first)
	}
	if first.Description != "Mature and well-supported." {
		t.Fatalf("option 1 description = %q, want the parsed tradeoff", first.Description)
	}
}

// TestPromptEndTurn_NumberedOptionsToleratesTrailingStemAndRecommendedAfterConfidence
// pins the real OpenCode drift where the model listed options before the final
// question and put "(Recommended)" after the confidence suffix.
func TestPromptEndTurn_NumberedOptionsToleratesTrailingStemAndRecommendedAfterConfidence(t *testing.T) {
	p, _, promptID := newPostHandshakeProtocol(t)
	streamAssistantText(t, p, strings.Join([]string{
		"I read the research and need one final decision.",
		"",
		"1. Italian tech terms where established: Follow normal Italian developer-doc practice. [confidence: 0.85] (Recommended)",
		"2. Keep all English tech terms untranslated: Simpler for developers but reads as code-switching. [confidence: 0.45]",
		"3. Fully Italianize everything possible: Linguistically pure but risks awkward calques. [confidence: 0.25]",
		"",
		"Which technical-term strategy should the translation use?",
	}, "\n"))
	msgs := endTurn(t, p, promptID)
	if len(msgs) != 1 || msgs[0].ControlRequest == nil || msgs[0].Result != nil {
		t.Fatalf("numbered question produced %+v, want one AskUserQuestion control request and no result", msgs)
	}
	qs := askUserQuestionsFrom(t, msgs[0].ControlRequest)
	if len(qs) != 1 {
		t.Fatalf("questions = %+v, want one question", qs)
	}
	if qs[0].Question != "Which technical-term strategy should the translation use?" {
		t.Fatalf("question = %q, want trailing question stem", qs[0].Question)
	}
	if len(qs[0].Options) != 3 {
		t.Fatalf("options = %+v, want 3", qs[0].Options)
	}
	first := qs[0].Options[0]
	if first.Label != "Italian tech terms where established (Recommended)" {
		t.Fatalf("option 1 label = %q, want recommended marker on label", first.Label)
	}
	if first.Confidence == nil || *first.Confidence != 0.85 {
		t.Fatalf("option 1 confidence = %v, want 0.85", first.Confidence)
	}
	if strings.Contains(first.Description, "confidence") || strings.Contains(first.Description, "Recommended") {
		t.Fatalf("option 1 description still contains parser metadata: %q", first.Description)
	}
}

func TestPromptEndTurn_NumberedOptionsMissingConfidenceRemindsBeforeSurfacing(t *testing.T) {
	p, buf, promptID := newPostHandshakeProtocol(t)
	streamAssistantText(t, p, strings.Join([]string{
		"Which database should I target?",
		"1. Postgres (Recommended): Mature and well-supported.",
		"2. MySQL: Familiar but fewer features.",
		"3. SQLite: Simplest but single-writer.",
	}, "\n"))
	if msgs := endTurn(t, p, promptID); len(msgs) != 0 {
		t.Fatalf("missing-confidence question produced %+v, want no message while reminder is sent", msgs)
	}
	var reminder Request
	if err := json.Unmarshal(buf.lastLine(t), &reminder); err != nil || reminder.Method != "session/prompt" {
		t.Fatalf("expected a confidence-format reminder session/prompt; got %q (err %v)", buf.String(), err)
	}
	var pp PromptParams
	if err := json.Unmarshal(mustMarshal(t, reminder.Params), &pp); err != nil {
		t.Fatalf("decode reminder params: %v", err)
	}
	if !strings.Contains(pp.Prompt[0].Text, "[confidence: 0.00]") {
		t.Fatalf("reminder = %q, want confidence suffix instruction", pp.Prompt[0].Text)
	}
}

// TestPromptEndTurn_MultiSentenceStemQuestionSurfacesOptions proves a stem
// whose '?' ends the first sentence — with a declarative clarifier after it —
// still surfaces as an AskUserQuestion instead of being misread as a plain
// completion.
func TestPromptEndTurn_MultiSentenceStemQuestionSurfacesOptions(t *testing.T) {
	p, _, promptID := newPostHandshakeProtocol(t)
	streamAssistantText(t, p, strings.Join([]string{
		"How should the opt-in notification preview setting be scoped? Existing server settings already control whether a feature may notify; this decision only governs how much content a notification reveals.",
		"",
		"1. Global preview toggle (Recommended): One app-local privacy setting. [confidence: 0.88]",
		"2. Per-feature preview toggle: More configuration and state. [confidence: 0.62]",
		"3. Per-attention-type toggle: Most control, highest complexity. [confidence: 0.38]",
	}, "\n"))
	msgs := endTurn(t, p, promptID)
	if len(msgs) != 1 || msgs[0].ControlRequest == nil || msgs[0].Result != nil {
		t.Fatalf("multi-sentence stem produced %+v, want one AskUserQuestion control request and no result", msgs)
	}
	qs := askUserQuestionsFrom(t, msgs[0].ControlRequest)
	if len(qs) != 1 || len(qs[0].Options) != 3 {
		t.Fatalf("questions = %+v, want one question with 3 options", qs)
	}
	if !strings.HasPrefix(qs[0].Question, "How should the opt-in notification preview setting be scoped?") {
		t.Fatalf("question = %q, want the multi-sentence stem", qs[0].Question)
	}
}

// TestPromptEndTurn_InformationalListStaysCompletion proves a numbered list
// with no question signal (no '?' stem, no confidence/(Recommended) markers)
// is still a normal completion.
func TestPromptEndTurn_InformationalListStaysCompletion(t *testing.T) {
	p, _, promptID := newPostHandshakeProtocol(t)
	streamAssistantText(t, p, strings.Join([]string{
		"Here's what I changed.",
		"1. Updated the handler to reject oversized bodies.",
		"2. Added a regression test for the new limit.",
	}, "\n"))
	msgs := endTurn(t, p, promptID)
	if !terminalResult(t, msgs).IsSuccess() {
		t.Fatalf("informational list produced %+v, want a success result", msgs)
	}
}

// TestPromptEndTurn_MalformedQuestionRemindsThenSynthesizes proves an unformatted
// question first earns a reformat-reminder turn (no message, no result), and when
// the agent still fails to format on the reminded turn the provider falls back to
// a free-form synthetic AskUserQuestion (Task 3).
func TestPromptEndTurn_MalformedQuestionRemindsThenSynthesizes(t *testing.T) {
	p, buf, promptID := newPostHandshakeProtocol(t)

	// Turn 1: a bare question with no numbered options.
	streamAssistantText(t, p, "Should I proceed with the destructive migration?")
	if msgs := endTurn(t, p, promptID); len(msgs) != 0 {
		t.Fatalf("malformed question turn 1 produced %+v, want no message (reformat reminder sent)", msgs)
	}
	// A reformat-reminder follow-up turn was sent.
	var reminder Request
	if err := json.Unmarshal(buf.lastLine(t), &reminder); err != nil || reminder.Method != "session/prompt" {
		t.Fatalf("expected a reformat-reminder session/prompt; got %q (err %v)", buf.String(), err)
	}

	// Turn 2: the agent ignores the reminder and ends with another bare question.
	// After the retry budget is spent the provider synthesizes a free-form
	// AskUserQuestion rather than blocking forever.
	p2ID := p.promptIDForTest()
	streamAssistantText(t, p, "Are you sure?")
	msgs := endTurn(t, p, p2ID)
	// Either another reminder (retry remaining) or a synthesized question; drive
	// until a question is synthesized, bounded by the retry limit.
	for i := 0; i < maxQuestionFormatRetries+1 && len(msgs) == 0; i++ {
		pID := p.promptIDForTest()
		streamAssistantText(t, p, "Still just asking?")
		msgs = endTurn(t, p, pID)
	}
	if len(msgs) != 1 || msgs[0].ControlRequest == nil || msgs[0].Result != nil {
		t.Fatalf("after retries, produced %+v, want a synthesized AskUserQuestion and no result", msgs)
	}
	if !strings.HasPrefix(msgs[0].ControlRequest.RequestID, syntheticAskUserPrefix) {
		t.Fatalf("fallback request id = %q, want synthetic", msgs[0].ControlRequest.RequestID)
	}
}

// TestPromptEndTurn_InteractiveNeverSynthesizesQuestion proves an Interactive
// session (AMA chat, where a human answers every turn directly) never
// synthesizes an AskUserQuestion picker, no matter how question-shaped the
// final text is: the text-parsing pipeline exists only to imitate Claude's
// native tool-call UX for a provider that can otherwise only express a
// question as plain text, and a human reading the chat gets no benefit from
// that imitation — they can just read whatever the model asked and reply with
// an ordinary follow-up message.
func TestPromptEndTurn_InteractiveNeverSynthesizesQuestion(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{"numbered options", strings.Join([]string{
			"Which database should I target?",
			"1. Postgres (Recommended): Mature and well-supported.",
			"2. MySQL: Familiar but fewer features.",
			"3. SQLite: Simplest but single-writer.",
		}, "\n")},
		{"bare question", "Should I proceed with the destructive migration?"},
		{"FREE_FORM sentinel", "FREE_FORM: What exact version string should I tag the release with?"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, buf, promptID := newPostHandshakeProtocol(t, llm.ProtocolOpts{
				Model:       "opencode:anthropic/claude-sonnet-4-5",
				Interactive: true,
			})
			streamAssistantText(t, p, c.text)
			msgs := endTurn(t, p, promptID)
			if !terminalResult(t, msgs).IsSuccess() {
				t.Fatalf("produced %+v, want a success result", msgs)
			}
			if buf.String() != "" {
				t.Fatalf("sent a reformat reminder %q, want none", buf.String())
			}
		})
	}
}

// TestPromptEndTurn_NonQuestionIsCleanSuccess proves a final text that is not a
// question still completes as a success result — synthesis must not hijack a
// normal completion (Task 3 / Task 4).
func TestPromptEndTurn_NonQuestionIsCleanSuccess(t *testing.T) {
	p, _, promptID := newPostHandshakeProtocol(t)
	streamAssistantText(t, p, "All done. I wrote the marker and finished the work.")
	msgs := endTurn(t, p, promptID)
	if !terminalResult(t, msgs).IsSuccess() {
		t.Fatalf("non-question end_turn produced %+v, want a success result", msgs)
	}
}

// TestPromptEndTurn_SecondTurnAlsoEmitsSuccess proves a session's SECOND clean
// completion also reaches the caller as a success result — not just the
// first. markTerminal's one-shot latch is meant to seal a session's FINAL
// outcome (so a late duplicate can't overturn it), but a multi-turn session
// (e.g. AMA chat, where one Protocol instance serves many user messages) must
// still get a fresh terminal result for every turn, or the caller's UI is left
// waiting forever after the first reply.
func TestPromptEndTurn_SecondTurnAlsoEmitsSuccess(t *testing.T) {
	p, _, promptID1 := newPostHandshakeProtocol(t)
	streamAssistantText(t, p, "First answer.")
	msgs1 := endTurn(t, p, promptID1)
	if !terminalResult(t, msgs1).IsSuccess() {
		t.Fatalf("first turn produced %+v, want a success result", msgs1)
	}

	if err := p.SendUserMessage("second question"); err != nil {
		t.Fatalf("SendUserMessage: %v", err)
	}
	promptID2 := p.promptIDForTest()
	streamAssistantText(t, p, "Second answer.")
	msgs2 := endTurn(t, p, promptID2)
	if !terminalResult(t, msgs2).IsSuccess() {
		t.Fatalf("second turn produced %+v, want a success result", msgs2)
	}
}

// TestPromptEndTurn_InformationalNumberedListIsCleanSuccess proves a final text
// that merely summarizes findings as a numbered list — with no question mark
// anywhere — completes as a success result rather than being hijacked into a
// reformat-into-a-question loop. AMA answers routinely enumerate findings or
// steps this way, and parseNumberedOptions must not treat every such list as an
// unformatted AskUserQuestion (Task 3 regression: "every turn ends with a
// picker").
func TestPromptEndTurn_InformationalNumberedListIsCleanSuccess(t *testing.T) {
	p, _, promptID := newPostHandshakeProtocol(t)
	streamAssistantText(t, p, strings.Join([]string{
		"Here is what I found:",
		"1. The config loader ignores env overrides.",
		"2. The default timeout is 30s.",
		"3. Logs are written to /tmp/agentico.log.",
	}, "\n"))
	msgs := endTurn(t, p, promptID)
	if !terminalResult(t, msgs).IsSuccess() {
		t.Fatalf("informational numbered list produced %+v, want a success result", msgs)
	}
}

// TestRespondToAskUser_SyntheticAnswerDeliversFollowUpTurn proves a synthetic
// question's answer is delivered as a framed follow-up turn (since it has no
// native pending request), restating the question for context (Task 3).
func TestRespondToAskUser_SyntheticAnswerDeliversFollowUpTurn(t *testing.T) {
	p, buf, promptID := newPostHandshakeProtocol(t)
	streamAssistantText(t, p, "FREE_FORM: What release name should I use?")
	msgs := endTurn(t, p, promptID)
	cr := msgs[0].ControlRequest
	if cr == nil || !strings.HasPrefix(cr.RequestID, syntheticAskUserPrefix) {
		t.Fatalf("expected a synthetic AskUserQuestion, got %+v", msgs)
	}

	answers := map[string]string{"What release name should I use?": "phoenix"}
	if err := p.RespondToAskUser(cr.RequestID, cr.Request.Input, answers, nil); err != nil {
		t.Fatalf("RespondToAskUser(synthetic) error: %v", err)
	}
	var followUp Request
	if err := json.Unmarshal(buf.lastLine(t), &followUp); err != nil || followUp.Method != "session/prompt" {
		t.Fatalf("expected a follow-up session/prompt; got %q (err %v)", buf.String(), err)
	}
	var pp PromptParams
	if err := json.Unmarshal(mustMarshal(t, followUp.Params), &pp); err != nil {
		t.Fatalf("decode follow-up params: %v", err)
	}
	body := pp.Prompt[0].Text
	if !strings.Contains(body, "phoenix") || !strings.Contains(body, "release name") {
		t.Fatalf("follow-up turn = %q, want it to restate the question and carry the answer", body)
	}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}
