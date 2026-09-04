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

package codex

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// Dynamic tools are the experimental app-server contract. Keep their wire
// shapes here so neither phase validation nor the question UI depends on Codex.
type dynamicToolCall struct {
	ThreadID  string          `json:"threadId"`
	TurnID    string          `json:"turnId"`
	CallID    string          `json:"callId"`
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
}

type questionOption struct {
	Label       string   `json:"label"`
	Description string   `json:"description"`
	Confidence  *float64 `json:"confidence,omitempty"`
	Recommended *bool    `json:"recommended,omitempty"`
}

type structuredQuestion struct {
	ID       string           `json:"id"`
	Kind     string           `json:"kind"`
	Question string           `json:"question"`
	Header   string           `json:"header"`
	Options  []questionOption `json:"options"`
}

type pendingQuestionRequest struct {
	Dynamic     bool
	CallID      string
	QuestionIDs map[string]string
}

func (p *Protocol) developerInstructions() *string {
	if p.opts.SystemPrompt == "" {
		return nil
	}
	instructions := p.opts.SystemPrompt
	return &instructions
}

func (p *Protocol) threadConfig() map[string]interface{} {
	if p.opts.Interactive {
		return nil
	}
	return map[string]interface{}{"include_collaboration_mode_instructions": false}
}

func (p *Protocol) dynamicTools() *[]map[string]interface{} {
	if p.opts.Interactive || p.opts.NativeToollessReview {
		return nil
	}
	var questionSchema map[string]interface{}
	_ = json.Unmarshal([]byte(`{
  "type":"object","additionalProperties":false,
  "required":["id","kind","question","header","options"],
  "properties":{
   "id":{"type":"string","minLength":1},
   "kind":{"type":"string","enum":["choice","free_form"]},
   "question":{"type":"string","minLength":1},
   "header":{"type":"string","minLength":1},
   "options":{"type":"array","maxItems":3,"items":{
    "type":"object","additionalProperties":false,
    "required":["label","description","confidence","recommended"],
    "properties":{
     "label":{"type":"string","minLength":1},
     "description":{"type":"string","minLength":1},
     "confidence":{"type":"number","minimum":0,"maximum":1},
     "recommended":{"type":"boolean"}
    }
   }}
  }
 }`), &questionSchema)
	tools := []map[string]interface{}{{
		"name":        "ask_user",
		"description": "Ask one blocking question. Choice questions require exactly three options with confidence scores and one uniquely highest-confidence recommendation. Use free_form with empty options only for an inherently unconstrained exact value. Wait for the tool result before continuing.",
		"inputSchema": questionSchema,
	}}
	if p.UsesStructuredCompletion() {
		tools = append(tools, map[string]interface{}{
			"name":        "complete_phase",
			"description": "Request phase completion after all blocking questions are answered and required artifacts are ready. The harness validates completion; a rejected request returns corrections to address before retrying.",
			"inputSchema": map[string]interface{}{
				"type": "object", "additionalProperties": false,
				"required": []string{"status"},
				"properties": map[string]interface{}{
					"status":  map[string]interface{}{"type": "string", "enum": []string{"success", "retry"}},
					"summary": map[string]interface{}{"type": "string"},
				},
			},
		})
	}
	return &tools
}

func decodeToolArguments(raw json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("expected one JSON object")
	}
	return nil
}

func (q structuredQuestion) validate() error {
	if q.Options == nil {
		return fmt.Errorf("options must be an array; use an empty array for free_form")
	}
	if strings.TrimSpace(q.ID) == "" || strings.TrimSpace(q.Question) == "" || strings.TrimSpace(q.Header) == "" {
		return fmt.Errorf("id, question, and header must be nonempty")
	}
	if q.Kind == "free_form" {
		if len(q.Options) != 0 {
			return fmt.Errorf("free_form requires empty options")
		}
		return nil
	}
	if q.Kind != "choice" || len(q.Options) != 3 {
		return fmt.Errorf("choice requires exactly three options; use free_form only for inherently unconstrained values")
	}
	recommendation := -1
	labels := map[string]bool{}
	for i, o := range q.Options {
		label := strings.TrimSpace(o.Label)
		if label == "" || strings.TrimSpace(o.Description) == "" || labels[strings.ToLower(label)] {
			return fmt.Errorf("options require distinct nonempty labels and descriptions")
		}
		labels[strings.ToLower(label)] = true
		if strings.Contains(strings.ToLower(label), "(recommended)") {
			return fmt.Errorf("set recommended as a boolean; the UI adds the recommendation label")
		}
		if o.Confidence == nil || *o.Confidence < 0 || *o.Confidence > 1 {
			return fmt.Errorf("each option requires confidence between 0 and 1")
		}
		if o.Recommended == nil {
			return fmt.Errorf("each option requires a recommended boolean")
		}
		if *o.Recommended {
			if recommendation >= 0 {
				return fmt.Errorf("exactly one option must be recommended")
			}
			recommendation = i
		}
	}
	if recommendation < 0 {
		return fmt.Errorf("exactly one option must be recommended")
	}
	for i, o := range q.Options {
		if i != recommendation && *o.Confidence >= *q.Options[recommendation].Confidence {
			return fmt.Errorf("recommended option must have uniquely highest confidence")
		}
	}
	return nil
}

func (p *Protocol) handleDynamicToolCall(id int, raw json.RawMessage) (llm.SDKMessage, bool) {
	var call dynamicToolCall
	if err := json.Unmarshal(raw, &call); err != nil {
		return p.rejectDynamicTool(id, "Malformed tool call: "+err.Error())
	}
	if call.CallID == "" || call.ThreadID == "" || call.TurnID == "" {
		return p.rejectDynamicTool(id, "Tool call requires callId, threadId, and turnId")
	}
	if p.opts.Interactive || p.opts.NativeToollessReview {
		return p.rejectDynamicTool(id, "Agentico phase tools are unavailable in this session")
	}
	p.mu.Lock()
	isRoot := p.isMainThread(call.ThreadID)
	stale := isRoot && p.turnID != "" && call.TurnID != p.turnID
	pendingQuestion := len(p.pendingQuestions) > 0
	pendingCompletion := p.pendingCompletion != ""
	p.mu.Unlock()
	if stale {
		return p.rejectDynamicTool(id, "Tool call belongs to a stale turn")
	}
	switch call.Tool {
	case "ask_user":
		if !isRoot {
			return p.rejectDynamicTool(id, "Only the root agent may ask the user. Report missing inputs to your parent agent.")
		}
		if pendingQuestion || pendingCompletion {
			return p.rejectDynamicTool(id, "Wait for the pending question or completion request before asking another question")
		}
		var question structuredQuestion
		if err := decodeToolArguments(call.Arguments, &question); err != nil {
			return p.rejectDynamicTool(id, "Invalid ask_user arguments: "+err.Error())
		}
		if err := question.validate(); err != nil {
			return p.rejectDynamicTool(id, "Invalid ask_user arguments: "+err.Error())
		}
		options := make([]questionOption, len(question.Options))
		copy(options, question.Options)
		for i := range options {
			if *options[i].Recommended {
				options[i].Label += " (Recommended)"
			}
		}
		input, _ := json.Marshal(map[string]any{"questions": []map[string]any{{"question": question.Question, "header": question.Header, "multiSelect": false, "options": options}}})
		p.rememberQuestions(id, pendingQuestionRequest{Dynamic: true, CallID: call.CallID, QuestionIDs: map[string]string{question.Question: question.ID}})
		return p.askUserControl(id, call.ThreadID, input), true
	case "complete_phase":
		if !p.UsesStructuredCompletion() || !isRoot {
			return p.rejectDynamicTool(id, "Only the root of an orchestrated phase can request completion")
		}
		var intent llm.CompletionIntent
		if err := decodeToolArguments(call.Arguments, &intent); err != nil {
			return p.rejectDynamicTool(id, "Invalid complete_phase arguments: "+err.Error())
		}
		if intent.Status != llm.CompletionIntentSuccess && intent.Status != llm.CompletionIntentRetry {
			return p.rejectDynamicTool(id, "status must be success or retry")
		}
		if pendingQuestion || pendingCompletion {
			return p.rejectDynamicTool(id, "Answer every pending question before requesting completion")
		}
		p.mu.Lock()
		p.pendingCompletion = strconv.Itoa(id)
		p.mu.Unlock()
		intent.Found = true
		return llm.SDKMessage{Type: "completion_request", Origin: llm.EventOrigin{Kind: llm.EventOriginRoot}, CompletionRequest: &llm.PhaseCompletionRequest{RequestID: strconv.Itoa(id), Intent: intent}}, true
	default:
		return p.rejectDynamicTool(id, "Unknown Agentico tool: "+call.Tool)
	}
}

func (p *Protocol) UsesStructuredCompletion() bool {
	return p.opts.StructuredCompletion && !p.opts.Interactive && !p.opts.NativeToollessReview
}

func (p *Protocol) RespondToCompletion(requestID string, accepted bool, message string) error {
	id, err := strconv.Atoi(requestID)
	if err != nil {
		return fmt.Errorf("invalid Codex request ID %q: %w", requestID, err)
	}
	p.mu.Lock()
	if p.pendingCompletion != requestID {
		p.mu.Unlock()
		return fmt.Errorf("no pending Codex completion for request %s", requestID)
	}
	// The server can emit its next request as soon as it reads this response.
	// Release the old request before writing, never after that next request.
	p.pendingCompletion = ""
	p.mu.Unlock()
	if err := p.respondDynamicTool(id, accepted, message); err != nil {
		p.mu.Lock()
		if p.pendingCompletion == "" && len(p.pendingQuestions) == 0 {
			p.pendingCompletion = requestID
		}
		p.mu.Unlock()
		return err
	}
	return nil
}

func (p *Protocol) respondDynamicTool(id int, success bool, text string) error {
	return p.writeJSON(Response{JSONRPC: "2.0", ID: id, Result: map[string]any{
		"success": success, "contentItems": []map[string]string{{"type": "inputText", "text": text}},
	}})
}

func (p *Protocol) rejectDynamicTool(id int, reason string) (llm.SDKMessage, bool) {
	if err := p.respondDynamicTool(id, false, reason); err != nil {
		return llm.SDKMessage{Type: "result", Subtype: "error", Result: &llm.ResultMessage{Type: "result", Subtype: "error", IsError: true, Result: err.Error()}}, true
	}
	return llm.SDKMessage{}, false
}

func (p *Protocol) rememberQuestions(id int, request pendingQuestionRequest) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pendingQuestions == nil {
		p.pendingQuestions = make(map[string]pendingQuestionRequest)
	}
	p.pendingQuestions[strconv.Itoa(id)] = request
}

func (p *Protocol) askUserControl(id int, threadID string, input json.RawMessage) llm.SDKMessage {
	return p.controlMessageOrigin(llm.SDKMessage{Type: "control_request", Subtype: "can_use_tool", ControlRequest: &llm.ControlRequestMessage{
		Type: "control_request", RequestID: strconv.Itoa(id), Request: llm.ControlRequest{Subtype: "can_use_tool", ToolName: "AskUserQuestion", Input: input},
	}}, threadID)
}

func (p *Protocol) respondToAskUser(requestID string, answers map[string]string, annotations map[string]llm.AskUserAnnotation) error {
	id, err := strconv.Atoi(requestID)
	if err != nil {
		return fmt.Errorf("invalid Codex request ID %q: %w", requestID, err)
	}
	p.mu.Lock()
	pending, ok := p.pendingQuestions[requestID]
	p.mu.Unlock()
	if !ok {
		return fmt.Errorf("no pending Codex question for request %s", requestID)
	}
	mapped := map[string]map[string][]string{}
	mappedAnnotations := map[string]llm.AskUserAnnotation{}
	for question, qID := range pending.QuestionIDs {
		answer, exists := answers[question]
		if !exists || strings.TrimSpace(answer) == "" {
			return fmt.Errorf("missing answer for question %q", question)
		}
		mapped[qID] = map[string][]string{"answers": {answer}}
		if note, ok := annotations[question]; ok {
			mappedAnnotations[qID] = note
		}
	}
	p.mu.Lock()
	if _, exists := p.pendingQuestions[requestID]; !exists {
		p.mu.Unlock()
		return fmt.Errorf("no pending Codex question for request %s", requestID)
	}
	// Claim the response before writing: an immediately following question
	// must observe that this answer has already released the previous one.
	delete(p.pendingQuestions, requestID)
	p.mu.Unlock()
	if pending.Dynamic {
		data, _ := json.Marshal(map[string]any{"callId": pending.CallID, "answers": mapped, "annotations": mappedAnnotations})
		err = p.respondDynamicTool(id, true, string(data))
	} else {
		err = p.writeJSON(Response{JSONRPC: "2.0", ID: id, Result: map[string]any{"answers": mapped}})
	}
	if err != nil {
		p.mu.Lock()
		if len(p.pendingQuestions) == 0 && p.pendingCompletion == "" {
			p.pendingQuestions[requestID] = pending
		}
		p.mu.Unlock()
	}
	return err
}

// Native Codex input remains available to interactive sessions. Orchestrated
// sessions reject it explicitly: native input has no confidence contract.
func (p *Protocol) handleNativeUserInput(id int, raw json.RawMessage) (llm.SDKMessage, bool) {
	if !p.opts.Interactive {
		err := p.writeJSON(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32602, "message": "Use Agentico ask_user with three confidence-scored options instead of request_user_input"}})
		if err != nil {
			return llm.SDKMessage{Type: "result", Subtype: "error", Result: &llm.ResultMessage{Type: "result", Subtype: "error", IsError: true, Result: err.Error()}}, true
		}
		return llm.SDKMessage{}, false
	}
	var params struct {
		ThreadID  string `json:"threadId"`
		Questions []struct {
			ID       string           `json:"id"`
			Header   string           `json:"header"`
			Question string           `json:"question"`
			Options  []questionOption `json:"options"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(raw, &params); err != nil || len(params.Questions) == 0 {
		return llm.SDKMessage{}, false
	}
	seen := map[string]int{}
	ids := map[string]string{}
	questions := []map[string]any{}
	for _, question := range params.Questions {
		display := question.Question
		seen[display]++
		if seen[display] > 1 {
			display = fmt.Sprintf("%s (#%d)", display, seen[display])
		}
		ids[display] = question.ID
		options := question.Options
		if options == nil {
			options = []questionOption{}
		}
		questions = append(questions, map[string]any{"question": display, "header": question.Header, "options": options, "multiSelect": false})
	}
	p.rememberQuestions(id, pendingQuestionRequest{QuestionIDs: ids})
	input, _ := json.Marshal(map[string]any{"questions": questions})
	return p.askUserControl(id, params.ThreadID, input), true
}

// Dynamic tools are persisted by Codex at thread creation, and cannot be added
// by thread/resume. Our own marker prevents resuming a legacy phase without its
// required tools; it never depends on Codex's private rollout format.
const structuredContractVersion = "1\n"

func (p *Protocol) contractPath(threadID string) string {
	digest := sha256.Sum256([]byte(threadID))
	return filepath.Join(p.opts.StateDir, "codex-contracts", fmt.Sprintf("%x", digest[:]))
}

func (p *Protocol) recordThreadContract(threadID string) error {
	if !p.UsesStructuredCompletion() || p.opts.ResumeSessionID != "" || p.opts.StateDir == "" {
		return nil
	}
	path := p.contractPath(threadID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("record Codex phase contract: %w", err)
	}
	if err := os.WriteFile(path, []byte(structuredContractVersion), 0o600); err != nil {
		return fmt.Errorf("record Codex phase contract: %w", err)
	}
	return nil
}

func (p *Protocol) checkResumableContract() error {
	if !p.UsesStructuredCompletion() {
		return nil
	}
	if p.opts.StateDir != "" {
		data, err := os.ReadFile(p.contractPath(p.opts.ResumeSessionID))
		if err == nil && string(data) == structuredContractVersion {
			return nil
		}
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("read Codex phase contract: %w", err)
		}
	}
	return fmt.Errorf("cannot resume Codex phase: this thread was not created with the current Agentico structured tools; restart the phase to create a compatible thread")
}
