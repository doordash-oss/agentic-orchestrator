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

package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
)

// promptRouteTarget records the prompt replies that still exist; every
// other mutation keeps the nop behavior.
type promptRouteTarget struct {
	nopMutationTarget
	askUser []AskUserAnswerRequest
	help    []HelpAnswerRequest
}

func (t *promptRouteTarget) AnswerAskUser(req AskUserAnswerRequest) (AskUserAnswerResponse, error) {
	t.askUser = append(t.askUser, req)
	return AskUserAnswerResponse{RequestID: req.RequestID, SessionID: "sess-1"}, nil
}

func (t *promptRouteTarget) SendHelp(req HelpAnswerRequest) (HelpSendResponse, error) {
	t.help = append(t.help, req)
	return HelpSendResponse{FeatureID: "feat-1", SessionID: req.SessionID}, nil
}

// TestRetiredChatPromptRoutesAreNotFound pins the chat surface removal: the
// two chat prompt routes answer the canonical 404 through the live handler
// while the surviving prompt replies still reach the mutation target.
func TestRetiredChatPromptRoutesAreNotFound(t *testing.T) {
	t.Parallel()
	target := &promptRouteTarget{}
	handler := NewHandler(HandlerOptions{Mutations: target, DisableHostValidation: true})

	for _, path := range []string{"/api/v1/prompts/chat/start", "/api/v1/prompts/chat/end"} {
		w := postTrustedJSON(handler, path, map[string]any{"message": "What is running?"})
		if w.Code != http.StatusNotFound {
			t.Fatalf("POST %s status = %d body=%s; want 404", path, w.Code, w.Body.String())
		}
		var body ErrorResponse
		if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
			t.Fatalf("decode %s response: %v", path, err)
		}
		if body.Error.Code != string(errcat.NotFound) {
			t.Fatalf("POST %s code = %q; want %q", path, body.Error.Code, errcat.NotFound)
		}
	}

	w := postTrustedJSON(handler, "/api/v1/prompts/ask-user/answer", map[string]any{
		"request_id": "ask-1",
		"answers":    map[string]string{"Which cache?": "Redis"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("ask-user/answer status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	if len(target.askUser) != 1 || target.askUser[0].RequestID != "ask-1" {
		t.Fatalf("ask-user replies = %+v; want the one answer", target.askUser)
	}

	w = postTrustedJSON(handler, "/api/v1/prompts/help/send", map[string]any{
		"session_id": "sess-1",
		"message":    "Use the staging bucket.",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("help/send status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	if len(target.help) != 1 || target.help[0].Message != "Use the staging bucket." {
		t.Fatalf("help replies = %+v; want the one message", target.help)
	}
}
