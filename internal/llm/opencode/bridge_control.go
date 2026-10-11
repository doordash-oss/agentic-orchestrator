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
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/askuser"
)

// Request-id prefixes for bridged control requests. ACP request ids are
// JSON-RPC integers, so a prefixed id can never collide with one.
const (
	bridgedPermissionPrefix = "opencode-http-permission-"
	bridgedQuestionPrefix   = "opencode-http-question-"
)

// OpenCode permission replies.
const (
	replyOnce   = "once"
	replyAlways = "always"
	replyReject = "reject"
)

// permissionAsked is the properties of a permission.asked event and an entry
// of GET /permission.
type permissionAsked struct {
	ID         string          `json:"id"`
	SessionID  string          `json:"sessionID"`
	Permission string          `json:"permission"`
	Patterns   []string        `json:"patterns"`
	Metadata   json.RawMessage `json:"metadata"`
	Always     []string        `json:"always"`
}

// questionAsked is the properties of a question.asked event and an entry of
// GET /question.
type questionAsked struct {
	ID        string             `json:"id"`
	SessionID string             `json:"sessionID"`
	Questions []openCodeQuestion `json:"questions"`
}

type openCodeQuestion struct {
	Question string `json:"question"`
	Header   string `json:"header"`
	Options  []struct {
		Label       string `json:"label"`
		Description string `json:"description"`
	} `json:"options"`
	Multiple bool `json:"multiple"`
}

// requestResolved is the properties of permission.replied,
// question.replied and question.rejected.
type requestResolved struct {
	SessionID string `json:"sessionID"`
	RequestID string `json:"requestID"`
}

// bridgedRequest is a bridged request the session has not answered yet.
type bridgedRequest struct {
	openCodeID string
	question   bool
	questions  []openCodeQuestion
	// askedSeq is the reconcile pass during which the request was first
	// seen; only an earlier pass's lists can prove it was answered.
	askedSeq int
}

// handleBridgeLine translates one bridged line. ok is false when the line is
// not a genuine bridge line, so it is parsed as ordinary stdout.
func (p *Protocol) handleBridgeLine(raw []byte) ([]llm.SDKMessage, bool) {
	var line bridgeLine
	if err := json.Unmarshal(raw, &line); err != nil || line.Nonce != p.bridge.nonce {
		return nil, false
	}
	switch line.Kind {
	case bridgeLineReconcileBegin:
		p.mu.Lock()
		p.bridgeSeq = line.Seq
		p.mu.Unlock()
	case bridgeLineReconcileEnd:
		p.resolveVanished(line)
	case bridgeLineEvent:
		switch line.Type {
		case "permission.asked":
			var ev permissionAsked
			if json.Unmarshal(line.Properties, &ev) == nil {
				return p.bridgePermission(ev), true
			}
		case "question.asked":
			var ev questionAsked
			if json.Unmarshal(line.Properties, &ev) == nil {
				return p.bridgeQuestion(ev), true
			}
		case "permission.replied":
			p.resolveElsewhere(bridgedPermissionPrefix, line.Properties)
		case "question.replied", "question.rejected":
			p.resolveElsewhere(bridgedQuestionPrefix, line.Properties)
		}
	}
	return nil, true
}

// claimNewBridged records a newly seen request and reports whether it is new.
// Root permission requests are left to ACP, which carries them too.
func (p *Protocol) claimNewBridged(reqID string, req *bridgedRequest) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.bridgedDone[req.openCodeID] {
		return false
	}
	if _, held := p.bridged[reqID]; held {
		return false
	}
	if p.bridged == nil {
		p.bridged = make(map[string]*bridgedRequest)
	}
	req.askedSeq = p.bridgeSeq
	p.bridged[reqID] = req
	return true
}

func (p *Protocol) bridgePermission(ev permissionAsked) []llm.SDKMessage {
	if ev.ID == "" || ev.SessionID == "" || ev.SessionID == p.SessionID() {
		return nil
	}
	reqID := bridgedPermissionPrefix + ev.ID
	if !p.claimNewBridged(reqID, &bridgedRequest{openCodeID: ev.ID}) {
		return nil
	}
	if p.terminalEmitted() {
		// The turn already has its outcome; release the child without a
		// prompt nobody would answer, as the ACP path does.
		go func() { _ = p.respondBridged(reqID, replyReject, nil) }()
		return nil
	}
	toolName, input := bridgedPermissionInput(ev)
	return []llm.SDKMessage{p.bridgedControl(reqID, ev.SessionID, toolName, input)}
}

func (p *Protocol) bridgeQuestion(ev questionAsked) []llm.SDKMessage {
	if ev.ID == "" || len(ev.Questions) == 0 {
		return nil
	}
	reqID := bridgedQuestionPrefix + ev.ID
	if !p.claimNewBridged(reqID, &bridgedRequest{openCodeID: ev.ID, question: true, questions: ev.Questions}) {
		return nil
	}
	if p.terminalEmitted() {
		go func() { _ = p.respondBridged(reqID, replyReject, nil) }()
		return nil
	}
	bundle := askuser.Bundle{Questions: make([]askuser.Question, 0, len(ev.Questions))}
	for _, q := range ev.Questions {
		options := make([]askuser.Option, 0, len(q.Options))
		for _, o := range q.Options {
			options = append(options, askuser.Option{Label: o.Label, Description: o.Description})
		}
		header := strings.TrimSpace(q.Header)
		if header == "" {
			header = "Agent Question"
		}
		bundle.Questions = append(bundle.Questions, askuser.Question{Question: q.Question, Header: header, MultiSelect: q.Multiple, Options: options})
	}
	input := bundle.Encode()
	return []llm.SDKMessage{p.bridgedControl(reqID, ev.SessionID, "AskUserQuestion", input)}
}

func (p *Protocol) bridgedControl(reqID, sessionID, toolName string, input json.RawMessage) llm.SDKMessage {
	origin := p.originForSession(sessionID)
	if origin.Kind == llm.EventOriginTask {
		if taskID := p.parentTaskForChildSession(sessionID); taskID != "" {
			origin.TaskID = taskID
		}
	}
	return llm.SDKMessage{
		Type:       "control_request",
		Subtype:    "can_use_tool",
		Origin:     origin,
		OccurredAt: time.Now().UTC(),
		ControlRequest: &llm.ControlRequestMessage{
			Type:      "control_request",
			RequestID: reqID,
			Origin:    origin,
			Request: llm.ControlRequest{
				Subtype:  "can_use_tool",
				ToolName: toolName,
				Input:    input,
			},
		},
	}
}

// bridgedPermissionInput maps an OpenCode permission to the normalized tool
// name and input the permission handlers expect. The input carries OpenCode's
// metadata and patterns, plus the canonical detail key for known tools.
func bridgedPermissionInput(ev permissionAsked) (string, json.RawMessage) {
	fields := map[string]any{}
	_ = json.Unmarshal(ev.Metadata, &fields)
	if len(ev.Patterns) > 0 {
		if _, taken := fields["patterns"]; !taken {
			fields["patterns"] = ev.Patterns
		}
	}
	firstPattern := ""
	if len(ev.Patterns) > 0 {
		firstPattern = ev.Patterns[0]
	}
	fallback := func(key, value string) {
		if s, _ := fields[key].(string); strings.TrimSpace(s) == "" {
			fields[key] = value
		}
	}
	meta := json.RawMessage(ev.Metadata)
	var name string
	switch ev.Permission {
	case "bash":
		name = "Bash"
		fallback("command", firstStringField(meta, strings.Join(ev.Patterns, "\n"), "command"))
	case "edit":
		name = "Write"
		fallback("file_path", firstStringField(meta, firstPattern, "filePath", "filepath", "file_path", "path"))
	case "external_directory":
		name = "ExternalDirectory"
		fallback("path", firstStringField(meta, firstPattern, "path", "filepath", "filePath", "directory", "dir", "parentDir"))
	case "skill":
		name = "Skill"
		fallback("skill", firstStringField(meta, firstPattern, "skill", "name"))
	case "task":
		name = "Agent"
		fallback("subagent_type", firstStringField(meta, firstPattern, "subagent_type"))
	case "webfetch":
		name = "WebFetch"
		fallback("url", firstStringField(meta, firstPattern, "url"))
	case "websearch":
		name = "WebSearch"
		fallback("query", firstStringField(meta, firstPattern, "query"))
	default:
		name = ev.Permission
		if name == "" {
			name = "Tool"
		}
	}
	input, _ := json.Marshal(fields)
	return name, input
}

// resolveElsewhere drops a held request another client answered, so a later
// answer from the session posts nothing.
func (p *Protocol) resolveElsewhere(prefix string, props json.RawMessage) {
	var ev requestResolved
	if json.Unmarshal(props, &ev) != nil || ev.RequestID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, held := p.bridged[prefix+ev.RequestID]; held {
		delete(p.bridged, prefix+ev.RequestID)
		p.logDebug("[opencode] bridged request %s was answered elsewhere", ev.RequestID)
	}
	p.markBridgedDoneLocked(ev.RequestID)
}

// resolveVanished drops held requests a reconcile pass no longer lists. Only
// requests first seen before that pass began are judged: anything newer may
// have been asked after the lists were read.
func (p *Protocol) resolveVanished(line bridgeLine) {
	pending := make(map[string]bool, len(line.Permissions)+len(line.Questions))
	for _, id := range line.Permissions {
		pending[bridgedPermissionPrefix+id] = true
	}
	for _, id := range line.Questions {
		pending[bridgedQuestionPrefix+id] = true
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for reqID, req := range p.bridged {
		if req.askedSeq < line.Seq && !pending[reqID] {
			delete(p.bridged, reqID)
			p.markBridgedDoneLocked(req.openCodeID)
			p.logDebug("[opencode] bridged request %s was answered while the event stream was down", req.openCodeID)
		}
	}
}

func (p *Protocol) markBridgedDoneLocked(openCodeID string) {
	if p.bridgedDone == nil {
		p.bridgedDone = make(map[string]bool)
	}
	p.bridgedDone[openCodeID] = true
}

func isBridgedRequestID(requestID string) bool {
	return strings.HasPrefix(requestID, bridgedPermissionPrefix) || strings.HasPrefix(requestID, bridgedQuestionPrefix)
}

// respondBridged answers a bridged request over HTTP. reply is a permission
// reply; for a question, replyReject rejects it and anything else posts
// answers. The request is claimed before the post so a concurrent event
// cannot answer it twice, and restored if the post fails so the answer can
// be retried. An unknown request was already answered or resolved
// elsewhere and posts nothing.
func (p *Protocol) respondBridged(requestID, reply string, answers [][]string) error {
	p.mu.Lock()
	req, held := p.bridged[requestID]
	if held {
		delete(p.bridged, requestID)
		p.markBridgedDoneLocked(req.openCodeID)
	}
	p.mu.Unlock()
	if !held {
		p.logDebug("[opencode] no held bridged request %s; nothing to answer", requestID)
		return nil
	}
	id := url.PathEscape(req.openCodeID)
	var err error
	switch {
	case !req.question:
		err = p.bridge.client.postJSON(context.Background(), "/permission/"+id+"/reply", map[string]string{"reply": reply})
	case reply == replyReject:
		err = p.bridge.client.postJSON(context.Background(), "/question/"+id+"/reject", nil)
	default:
		err = p.bridge.client.postJSON(context.Background(), "/question/"+id+"/reply", map[string]any{"answers": answers})
	}
	if err != nil {
		p.mu.Lock()
		p.bridged[requestID] = req
		delete(p.bridgedDone, req.openCodeID)
		p.mu.Unlock()
		return fmt.Errorf("answering OpenCode request %s: %s", req.openCodeID, sanitizeDiagnostic(err.Error()))
	}
	return nil
}

// bridgedAnswers maps the resolved answers to OpenCode's per-question label
// lists: a selected answer sends its labels and a free-text answer is a
// custom answer.
func bridgedAnswers(questions []openCodeQuestion, resolved askuser.Resolved) ([][]string, error) {
	if len(resolved.Answers) != len(questions) {
		return nil, fmt.Errorf("resolved %d answers for %d questions", len(resolved.Answers), len(questions))
	}
	out := make([][]string, 0, len(resolved.Answers))
	for _, answer := range resolved.Answers {
		if answer.Selected {
			out = append(out, append([]string(nil), answer.Labels...))
			continue
		}
		out = append(out, []string{answer.Raw})
	}
	return out, nil
}

// respondBridgedAskUser answers a bridged question.
func (p *Protocol) respondBridgedAskUser(requestID string, resolved askuser.Resolved) error {
	p.mu.Lock()
	req, held := p.bridged[requestID]
	p.mu.Unlock()
	if !held {
		return p.respondBridged(requestID, "", nil)
	}
	mapped, err := bridgedAnswers(req.questions, resolved)
	if err != nil {
		return err
	}
	return p.respondBridged(requestID, "", mapped)
}
