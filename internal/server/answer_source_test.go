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
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

type answerSourceMutationTarget struct {
	MutationTarget
	permission []PermissionAnswerRequest
	askUser    []AskUserAnswerRequest
	help       []HelpAnswerRequest
}

func (t *answerSourceMutationTarget) AnswerPermission(req PermissionAnswerRequest) (PermissionAnswerResponse, error) {
	t.permission = append(t.permission, req)
	return PermissionAnswerResponse{RequestID: req.RequestID, Result: resultAnswered}, nil
}

func (t *answerSourceMutationTarget) AnswerAskUser(req AskUserAnswerRequest) (AskUserAnswerResponse, error) {
	t.askUser = append(t.askUser, req)
	return AskUserAnswerResponse{RequestID: req.RequestID, Result: resultAnswered}, nil
}

func (t *answerSourceMutationTarget) SendHelp(req HelpAnswerRequest) (HelpSendResponse, error) {
	t.help = append(t.help, req)
	return HelpSendResponse{FeatureID: req.FeatureID, Result: "sent"}, nil
}

func TestAnswerMutationRoutesPreserveOptionalAnswerSource(t *testing.T) {
	tests := []struct {
		name string
		path string
		body string
		got  func(*answerSourceMutationTarget) *ports.AnswerSource
		want *ports.AnswerSource
	}{
		{
			name: "permission omitted",
			path: apiPathPermissionsAnswer,
			body: `{"request_id":"perm-1","decision":"allow_once"}`,
			got:  func(target *answerSourceMutationTarget) *ports.AnswerSource { return target.permission[0].Source },
		},
		{
			name: "permission slack",
			path: apiPathPermissionsAnswer,
			body: `{"request_id":"perm-1","decision":"allow_once","source":{"kind":"slack","responder":"Ada"}}`,
			got:  func(target *answerSourceMutationTarget) *ports.AnswerSource { return target.permission[0].Source },
			want: &ports.AnswerSource{Kind: ports.AnswerSourceSlack, Responder: "Ada"},
		},
		{
			name: "ask user desktop",
			path: "/api/v1/prompts/ask-user/answer",
			body: `{"request_id":"ask-1","answers":{"Scope?":"Repository"},"source":{"kind":"desktop"}}`,
			got:  func(target *answerSourceMutationTarget) *ports.AnswerSource { return target.askUser[0].Source },
			want: &ports.AnswerSource{Kind: ports.AnswerSourceDesktop},
		},
		{
			name: "help slack",
			path: "/api/v1/prompts/help/send",
			body: `{"feature_id":"feature-1","message":"Continue","source":{"kind":"slack","responder":"Grace"}}`,
			got:  func(target *answerSourceMutationTarget) *ports.AnswerSource { return target.help[0].Source },
			want: &ports.AnswerSource{Kind: ports.AnswerSourceSlack, Responder: "Grace"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target := &answerSourceMutationTarget{}
			handler := NewHandler(HandlerOptions{Mutations: target, DisableHostValidation: true})
			req := httptest.NewRequest(http.MethodPost, test.path, bytes.NewBufferString(test.body))
			req.Header.Set("Content-Type", contentTypeJSON)
			req.Header.Set("X-Agentico-Client", trustedClientHeaderValue)
			recorder := httptest.NewRecorder()

			handler.ServeHTTP(recorder, req)

			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
			}
			got := test.got(target)
			if fmt.Sprintf("%+v", got) != fmt.Sprintf("%+v", test.want) {
				t.Fatalf("source = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestAnswerMutationRoutesRejectInvalidAnswerSource(t *testing.T) {
	tests := []struct {
		name string
		path string
		body string
	}{
		{"permission unknown kind", apiPathPermissionsAnswer, `{"request_id":"perm-1","decision":"allow_once","source":{"kind":"email"}}`},
		{"permission non object", apiPathPermissionsAnswer, `{"request_id":"perm-1","decision":"allow_once","source":"slack"}`},
		{"ask user unknown kind", "/api/v1/prompts/ask-user/answer", `{"request_id":"ask-1","answers":{"Scope?":"Repository"},"source":{"kind":"email"}}`},
		{"help unknown kind", "/api/v1/prompts/help/send", `{"feature_id":"feature-1","message":"Continue","source":{"kind":"email"}}`},
		{"review unknown kind", "/api/v1/features/feature-1/reviews/review-1/decision", `{"decision":"proceed","base_revision":"rev-1","source":{"kind":"email"}}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := NewHandler(HandlerOptions{Mutations: &answerSourceMutationTarget{}, DisableHostValidation: true})
			req := httptest.NewRequest(http.MethodPost, test.path, bytes.NewBufferString(test.body))
			req.Header.Set("Content-Type", contentTypeJSON)
			req.Header.Set("X-Agentico-Client", trustedClientHeaderValue)
			recorder := httptest.NewRecorder()

			handler.ServeHTTP(recorder, req)

			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", recorder.Code, recorder.Body.String())
			}
			var response ErrorResponse
			if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if response.Error.Code != string(errcat.BadRequest) {
				t.Fatalf("code = %q, want %q", response.Error.Code, errcat.BadRequest)
			}
			if !strings.Contains(response.Error.Diagnostics, "source") {
				t.Fatalf("diagnostics = %q, want source field named", response.Error.Diagnostics)
			}
		})
	}
}

func TestNoLongerPendingMutationErrorIsConflict(t *testing.T) {
	recorder := httptest.NewRecorder()

	writeMutationError(recorder, fmt.Errorf("wrapped: %w", ErrNoLongerPending))

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", recorder.Code)
	}
	var response ErrorResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Error.Code != string(errcat.NoLongerPending) {
		t.Fatalf("code = %q, want %q", response.Error.Code, errcat.NoLongerPending)
	}
}
