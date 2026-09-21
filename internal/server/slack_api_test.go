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
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

type fakeSlackService struct {
	validation     ports.SlackValidation
	err            error
	resolved       ports.SlackRecipient
	resolveErr     error
	results        []ports.SlackDeliveryResult
	sendErr        error
	calls          atomic.Int64
	lastToken      string
	lastInput      string
	lastName       string
	lastRecipients []ports.SlackRecipient
	successAt      time.Time
	failureAt      time.Time
}

func (s *fakeSlackService) Manifest() string         { return "{}" }
func (s *fakeSlackService) RequiredScopes() []string { return []string{"chat:write"} }
func (s *fakeSlackService) Status(ports.SlackStatusInput) ports.SlackStatusSnapshot {
	return ports.SlackStatusSnapshot{State: ports.SlackConnected}
}
func (s *fakeSlackService) ClearStatus() {}
func (s *fakeSlackService) Validate(_ context.Context, token string) (ports.SlackValidation, error) {
	s.calls.Add(1)
	s.lastToken = token
	return s.validation, s.err
}
func (s *fakeSlackService) ResolveRecipient(
	_ context.Context,
	token string,
	input string,
) (ports.SlackRecipient, error) {
	s.lastToken = token
	s.lastInput = input
	return s.resolved, s.resolveErr
}
func (s *fakeSlackService) SendTestMessage(
	_ context.Context,
	token string,
	serverName string,
	recipients []ports.SlackRecipient,
) ([]ports.SlackDeliveryResult, error) {
	s.lastToken = token
	s.lastName = serverName
	s.lastRecipients = append([]ports.SlackRecipient(nil), recipients...)
	return append([]ports.SlackDeliveryResult(nil), s.results...), s.sendErr
}
func (s *fakeSlackService) RecordValidationSuccess(at time.Time) { s.successAt = at }
func (s *fakeSlackService) RecordValidationFailure(at time.Time, _ errcat.Error) {
	s.failureAt = at
}

type slackMutationRecorder struct {
	MutationTarget
	runtimeCalls atomic.Int64
	mu           sync.Mutex
	token        string
	generation   uint64
	stored       *ports.SlackValidation
	storedAt     time.Time
	recipients   []ports.SlackRecipient
}

func (r *slackMutationRecorder) RuntimeConfig(RuntimeConfigMutationRequest) (RuntimeConfigUpdateResponse, error) {
	r.runtimeCalls.Add(1)
	return RuntimeConfigUpdateResponse{Result: resultUpdated}, nil
}

func (r *slackMutationRecorder) LoadSlackCredential() (string, uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.token, r.generation
}

func (r *slackMutationRecorder) StoreSlackValidation(
	token string,
	generation uint64,
	validation *ports.SlackValidation,
	at time.Time,
) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.token != token || r.generation != generation {
		return false, nil
	}
	r.stored = validation
	r.storedAt = at
	return true, nil
}

func (r *slackMutationRecorder) SlackCredentialCurrent(token string, generation uint64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.token == token && r.generation == generation
}

func (r *slackMutationRecorder) LoadSlackDeliveryConfig() (string, []ports.SlackRecipient) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.token, append([]ports.SlackRecipient(nil), r.recipients...)
}

func postSlackValidate(handler http.Handler, body any, trusted bool) *httptest.ResponseRecorder {
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, apiPathSlackValidate, bytes.NewReader(payload))
	req.Header.Set("Content-Type", contentTypeJSON)
	if trusted {
		req.Header.Set("X-Agentico-Client", trustedClientHeaderValue)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

func postSlackOperation(handler http.Handler, path string, body any, trusted bool) *httptest.ResponseRecorder {
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload))
	req.Header.Set("Content-Type", contentTypeJSON)
	if trusted {
		req.Header.Set("X-Agentico-Client", trustedClientHeaderValue)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

func TestSlackValidateRouteProvidedTokenDoesNotPersist(t *testing.T) {
	service := &fakeSlackService{validation: ports.SlackValidation{
		TokenType: ports.SlackTokenBot,
		Identity: ports.SlackIdentity{
			TeamID: "T123", TeamName: "Acme", UserID: "U123", DisplayName: "Agentico",
		},
		GrantedScopes: []string{"chat:write"},
		MissingScopes: []string{},
	}}
	recorder := &slackMutationRecorder{}
	handler := newAPIHandler(HandlerOptions{
		Config: config.NewDefault(), Slack: service, Mutations: recorder, DisableHostValidation: true,
	}).routes()

	w := postSlackValidate(handler, map[string]any{"token": "xoxb-secret-1234"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	if service.calls.Load() != 1 || service.lastToken != "xoxb-secret-1234" {
		t.Fatalf("Validate calls/token = %d/%q", service.calls.Load(), service.lastToken)
	}
	if recorder.stored != nil || recorder.runtimeCalls.Load() != 0 {
		t.Fatal("provided-token validation persisted configuration")
	}
	if bytes.Contains(w.Body.Bytes(), []byte("xoxb-secret-1234")) {
		t.Fatal("response leaked the Slack token")
	}
	var body struct {
		MissingScopes      json.RawMessage `json:"missing_scopes"`
		SuggestedRecipient json.RawMessage `json:"suggested_recipient"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if string(body.MissingScopes) != "[]" {
		t.Fatalf("missing_scopes JSON = %s; want []", body.MissingScopes)
	}
	if string(body.SuggestedRecipient) != "null" {
		t.Fatalf("suggested_recipient JSON = %s; want null for bot token", body.SuggestedRecipient)
	}
}

func TestSlackValidateRouteStoredTokenRefreshesCache(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Slack = &config.SlackConfig{Token: "xoxp-stored-1234", Enabled: false}
	service := &fakeSlackService{validation: ports.SlackValidation{
		TokenType: ports.SlackTokenUser,
		Identity: ports.SlackIdentity{
			TeamID: "T123", TeamName: "Acme", UserID: "U234", UserName: "ada", DisplayName: "Ada",
		},
		GrantedScopes: []string{"chat:write"},
		MissingScopes: []string{},
	}}
	recorder := &slackMutationRecorder{token: cfg.Slack.Token}
	handler := newAPIHandler(HandlerOptions{
		Config: cfg, Slack: service, Mutations: recorder, DisableHostValidation: true,
	}).routes()

	w := postSlackValidate(handler, map[string]any{}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	if recorder.stored == nil || recorder.stored.Identity.DisplayName != "Ada" {
		t.Fatalf("stored validation = %#v; want refreshed identity", recorder.stored)
	}
	if service.successAt.IsZero() || recorder.storedAt.IsZero() {
		t.Fatal("stored-token validation did not record its check time")
	}
	var body SlackValidateResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.SuggestedRecipient == nil ||
		body.SuggestedRecipient.TypedText != "@ada" ||
		body.SuggestedRecipient.ID != "U234" {
		t.Fatalf("suggested recipient = %#v; want owner", body.SuggestedRecipient)
	}
}

type delayedSlackService struct {
	fakeSlackService
	started chan string
	release chan struct{}
}

func (s *delayedSlackService) Validate(ctx context.Context, token string) (ports.SlackValidation, error) {
	s.calls.Add(1)
	select {
	case s.started <- token:
	case <-ctx.Done():
		return ports.SlackValidation{}, ctx.Err()
	}
	select {
	case <-s.release:
		return s.validation, s.err
	case <-ctx.Done():
		return ports.SlackValidation{}, ctx.Err()
	}
}

type delayedResolveSlackService struct {
	fakeSlackService
	started chan struct{}
	release chan struct{}
}

func (s *delayedResolveSlackService) ResolveRecipient(
	ctx context.Context,
	token string,
	input string,
) (ports.SlackRecipient, error) {
	s.lastToken = token
	s.lastInput = input
	close(s.started)
	select {
	case <-s.release:
		return s.resolved, s.resolveErr
	case <-ctx.Done():
		return ports.SlackRecipient{}, ctx.Err()
	}
}

type delayedSendSlackService struct {
	fakeSlackService
	started chan struct{}
	release chan struct{}
}

func (s *delayedSendSlackService) SendTestMessage(
	ctx context.Context,
	token string,
	serverName string,
	recipients []ports.SlackRecipient,
) ([]ports.SlackDeliveryResult, error) {
	s.lastToken = token
	s.lastName = serverName
	s.lastRecipients = append([]ports.SlackRecipient(nil), recipients...)
	close(s.started)
	select {
	case <-s.release:
		return append([]ports.SlackDeliveryResult(nil), s.results...), s.sendErr
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestSlackValidateRouteDiscardsDelayedStoredCredentialResults(t *testing.T) {
	tests := []struct {
		name       string
		validation ports.SlackValidation
		err        error
	}{
		{
			name: "success",
			validation: ports.SlackValidation{
				TokenType: ports.SlackTokenBot,
				Identity: ports.SlackIdentity{
					TeamID: "T-old", TeamName: "Old", UserID: "U-old", DisplayName: "Old Agent",
				},
				GrantedScopes: []string{"chat:write"},
				MissingScopes: []string{},
			},
		},
		{
			name: "failure",
			err: &ports.SlackValidationError{
				Canonical: errcat.New(errcat.SlackUnreachable),
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.NewDefault()
			cfg.Slack = &config.SlackConfig{Token: "xoxb-old-1234"}
			service := &delayedSlackService{
				fakeSlackService: fakeSlackService{validation: tc.validation, err: tc.err},
				started:          make(chan string, 1),
				release:          make(chan struct{}),
			}
			recorder := &slackMutationRecorder{token: cfg.Slack.Token, generation: 7}
			handler := newAPIHandler(HandlerOptions{
				Config: cfg, Slack: service, Mutations: recorder, DisableHostValidation: true,
			}).routes()

			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				done <- postSlackValidate(handler, map[string]any{}, true)
			}()
			if token := <-service.started; token != "xoxb-old-1234" {
				t.Fatalf("validated token = %q; want old token", token)
			}
			recorder.mu.Lock()
			recorder.token = "xoxb-new-5678"
			recorder.generation++
			recorder.mu.Unlock()
			close(service.release)

			w := <-done
			if tc.err == nil && w.Code != http.StatusOK {
				t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
			}
			if tc.err != nil && w.Code != http.StatusBadGateway {
				t.Fatalf("status = %d body=%s; want 502", w.Code, w.Body.String())
			}
			if recorder.stored != nil {
				t.Fatalf("stale validation persisted: %#v", recorder.stored)
			}
			if !service.successAt.IsZero() || !service.failureAt.IsZero() {
				t.Fatalf("stale validation changed status: success=%s failure=%s", service.successAt, service.failureAt)
			}
		})
	}
}

func TestSlackValidateRouteDoesNotBlockUnrelatedRuntimeAccess(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Slack = &config.SlackConfig{Token: "xoxb-stored-1234"}
	service := &delayedSlackService{
		fakeSlackService: fakeSlackService{validation: ports.SlackValidation{
			TokenType: ports.SlackTokenBot,
			Identity: ports.SlackIdentity{
				TeamID: "T123", TeamName: "Acme", UserID: "U123", DisplayName: "Agentico",
			},
			GrantedScopes: []string{"chat:write"},
			MissingScopes: []string{},
		}},
		started: make(chan string, 1),
		release: make(chan struct{}),
	}
	recorder := &slackMutationRecorder{token: cfg.Slack.Token}
	handler := newAPIHandler(HandlerOptions{
		Config: cfg, Slack: service, Mutations: recorder, DisableHostValidation: true,
	}).routes()

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- postSlackValidate(handler, map[string]any{}, true)
	}()
	<-service.started

	start := time.Now()
	write := patchTrustedJSON(handler, apiPathConfigRuntime, map[string]any{
		"notifications": map[string]any{"mute_feature_input": true},
	})
	if write.Code != http.StatusOK {
		t.Fatalf("unrelated write status = %d body=%s; want 200", write.Code, write.Body.String())
	}
	readReq := httptest.NewRequest(http.MethodGet, apiPathConfigRuntime, nil)
	read := httptest.NewRecorder()
	handler.ServeHTTP(read, readReq)
	if read.Code != http.StatusOK {
		t.Fatalf("unrelated read status = %d body=%s; want 200", read.Code, read.Body.String())
	}
	if elapsed := time.Since(start); elapsed > 250*time.Millisecond {
		t.Fatalf("unrelated read/write elapsed = %s; validation held a mutation lock", elapsed)
	}

	close(service.release)
	if w := <-done; w.Code != http.StatusOK {
		t.Fatalf("validation status = %d body=%s; want 200", w.Code, w.Body.String())
	}
}

func TestSlackValidateRouteRejectsUntrustedAndMissingStoredToken(t *testing.T) {
	service := &fakeSlackService{}
	handler := newAPIHandler(HandlerOptions{
		Config: config.NewDefault(), Slack: service, Mutations: &slackMutationRecorder{},
		DisableHostValidation: true,
	}).routes()

	if w := postSlackValidate(handler, map[string]any{}, false); w.Code != http.StatusForbidden {
		t.Fatalf("untrusted status = %d; want 403", w.Code)
	}
	w := postSlackValidate(handler, map[string]any{}, true)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing-token status = %d body=%s; want 400", w.Code, w.Body.String())
	}
	if service.calls.Load() != 0 {
		t.Fatalf("Validate calls = %d; want none", service.calls.Load())
	}
}

func TestSlackRuntimeMutationValidationFailuresDoNotReachTarget(t *testing.T) {
	service := &fakeSlackService{err: &ports.SlackValidationError{
		Canonical: errcat.New(errcat.SlackInvalidToken),
	}}
	recorder := &slackMutationRecorder{}
	handler := newAPIHandler(HandlerOptions{
		Config: config.NewDefault(), Slack: service, Mutations: recorder, DisableHostValidation: true,
	}).routes()

	w := patchTrustedJSON(handler, apiPathConfigRuntime, map[string]any{
		"slack": map[string]any{"enabled": true, "token": "xoxb-invalid-1234"},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s; want 400", w.Code, w.Body.String())
	}
	if recorder.runtimeCalls.Load() != 0 {
		t.Fatal("invalid token reached the mutation target")
	}
	if bytes.Contains(w.Body.Bytes(), []byte("xoxb-invalid-1234")) {
		t.Fatal("error response leaked the Slack token")
	}
}

func TestSlackRuntimeMutationRejectsInvalidRecipientsAtomically(t *testing.T) {
	tests := []struct {
		name       string
		recipients []map[string]any
		wantField  string
	}{
		{
			name: "missing display name",
			recipients: []map[string]any{{
				"typed_text": "@ada", "kind": "user", "id": "U12345678",
			}},
			wantField: "default_recipients[0].display_name",
		},
		{
			name: "unknown kind",
			recipients: []map[string]any{{
				"typed_text": "@ada", "kind": "group", "id": "U12345678", "display_name": "Ada",
			}},
			wantField: "default_recipients[0].kind",
		},
		{
			name: "duplicate destination",
			recipients: []map[string]any{
				{"typed_text": "@ada", "kind": "user", "id": "U12345678", "display_name": "Ada"},
				{"typed_text": "ada@example.com", "kind": "user", "id": "U12345678", "display_name": "Ada"},
			},
			wantField: "default_recipients[1] duplicates entry 0",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			recorder := &slackMutationRecorder{}
			handler := newAPIHandler(HandlerOptions{
				Config: config.NewDefault(), Slack: &fakeSlackService{}, Mutations: recorder,
				DisableHostValidation: true,
			}).routes()

			w := patchTrustedJSON(handler, apiPathConfigRuntime, map[string]any{
				"notifications": map[string]any{"mute_feature_input": true},
				"slack":         map[string]any{"default_recipients": tc.recipients},
			})
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d body=%s; want 400", w.Code, w.Body.String())
			}
			if recorder.runtimeCalls.Load() != 0 {
				t.Fatal("invalid recipient mutation reached the target")
			}
			if !strings.Contains(w.Body.String(), tc.wantField) {
				t.Fatalf("body = %s; want diagnostics containing %q", w.Body.String(), tc.wantField)
			}
		})
	}
}

func TestSlackRecipientResolveRouteUsesDraftAndStoredTokens(t *testing.T) {
	service := &fakeSlackService{resolved: ports.SlackRecipient{
		TypedText: "ada@example.com", Kind: ports.SlackRecipientUser,
		ID: "U12345678", DisplayName: "Ada Lovelace",
	}}
	recorder := &slackMutationRecorder{token: "xoxp-stored-1234"}
	handler := newAPIHandler(HandlerOptions{
		Config: config.NewDefault(), Slack: service, Mutations: recorder,
		DisableHostValidation: true,
	}).routes()

	w := postSlackOperation(handler, apiPathSlackRecipientResolve, map[string]any{
		"input": "ada@example.com", "token": "xoxp-draft-5678",
	}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("draft resolve status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	if service.lastToken != "xoxp-draft-5678" || service.lastInput != "ada@example.com" {
		t.Fatalf("draft resolve token/input = %q/%q", service.lastToken, service.lastInput)
	}
	if bytes.Contains(w.Body.Bytes(), []byte("xoxp-draft-5678")) {
		t.Fatal("draft resolve response leaked token")
	}

	w = postSlackOperation(handler, apiPathSlackRecipientResolve, map[string]any{
		"input": "@ada",
	}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("stored resolve status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	if service.lastToken != "xoxp-stored-1234" || service.lastInput != "@ada" {
		t.Fatalf("stored resolve token/input = %q/%q", service.lastToken, service.lastInput)
	}
}

func TestSlackTestMessageRouteUsesCurrentRowsAndReturnsPartialFailures(t *testing.T) {
	notInChannel := errcat.New(
		errcat.SlackNotInChannel,
		errcat.WithParams(errcat.SlackRecipientParams{Recipient: "#private-ops"}),
		errcat.WithRemediationHint("Invite the Agentico app to #private-ops in Slack, then try again."),
	)
	user := ports.SlackRecipient{
		TypedText: "@ada", Kind: ports.SlackRecipientUser, ID: "U12345678", DisplayName: "Ada",
	}
	channel := ports.SlackRecipient{
		TypedText: "#private-ops", Kind: ports.SlackRecipientChannel,
		ID: "C12345678", DisplayName: "#private-ops",
	}
	service := &fakeSlackService{results: []ports.SlackDeliveryResult{
		{Recipient: user, Delivered: true},
		{Recipient: channel, Error: &notInChannel},
	}}
	recorder := &slackMutationRecorder{
		token: "xoxb-stored-1234",
		recipients: []ports.SlackRecipient{{
			TypedText: "#stored", Kind: ports.SlackRecipientChannel,
			ID: "C87654321", DisplayName: "#stored",
		}},
	}
	handler := newAPIHandler(HandlerOptions{
		Name: "Local agent", Config: config.NewDefault(), Slack: service, Mutations: recorder,
		DisableHostValidation: true,
	}).routes()

	w := postSlackOperation(handler, apiPathSlackTestMessage, map[string]any{
		"recipients": []map[string]any{
			{"typed_text": user.TypedText, "kind": user.Kind, "id": user.ID, "display_name": user.DisplayName},
			{"typed_text": channel.TypedText, "kind": channel.Kind, "id": channel.ID, "display_name": channel.DisplayName},
		},
	}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	if service.lastToken != "xoxb-stored-1234" || service.lastName != "Local agent" ||
		len(service.lastRecipients) != 2 || service.lastRecipients[0].ID != user.ID {
		t.Fatalf("send arguments = token %q name %q recipients %#v", service.lastToken, service.lastName, service.lastRecipients)
	}
	var response SlackTestMessageResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Results) != 2 || !response.Results[0].Delivered ||
		response.Results[1].Error == nil ||
		response.Results[1].Error.Code != string(errcat.SlackNotInChannel) {
		t.Fatalf("results = %#v; want delivered then not-in-channel", response.Results)
	}
}

func TestSlackTestMessageRouteFallsBackToStoredDefaults(t *testing.T) {
	stored := ports.SlackRecipient{
		TypedText: "#eng", Kind: ports.SlackRecipientChannel, ID: "C12345678", DisplayName: "#eng",
	}
	service := &fakeSlackService{results: []ports.SlackDeliveryResult{{Recipient: stored, Delivered: true}}}
	recorder := &slackMutationRecorder{
		token: "xoxb-stored-1234", recipients: []ports.SlackRecipient{stored},
	}
	handler := newAPIHandler(HandlerOptions{
		Name: "Local agent", Config: config.NewDefault(), Slack: service, Mutations: recorder,
		DisableHostValidation: true,
	}).routes()

	w := postSlackOperation(handler, apiPathSlackTestMessage, map[string]any{}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	if len(service.lastRecipients) != 1 || service.lastRecipients[0] != stored {
		t.Fatalf("recipients = %#v; want stored defaults", service.lastRecipients)
	}
}

func TestSlackExplicitOperationsDoNotBlockRuntimeWrites(t *testing.T) {
	t.Run("resolve", func(t *testing.T) {
		service := &delayedResolveSlackService{
			fakeSlackService: fakeSlackService{resolved: ports.SlackRecipient{
				TypedText: "@ada", Kind: ports.SlackRecipientUser,
				ID: "U12345678", DisplayName: "Ada",
			}},
			started: make(chan struct{}),
			release: make(chan struct{}),
		}
		recorder := &slackMutationRecorder{token: "xoxp-stored-1234"}
		handler := newAPIHandler(HandlerOptions{
			Config: config.NewDefault(), Slack: service, Mutations: recorder,
			DisableHostValidation: true,
		}).routes()

		done := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			done <- postSlackOperation(handler, apiPathSlackRecipientResolve, map[string]any{
				"input": "@ada",
			}, true)
		}()
		<-service.started
		assertUnrelatedSlackWriteCompletes(t, handler)
		close(service.release)
		if w := <-done; w.Code != http.StatusOK {
			t.Fatalf("resolve status = %d body=%s; want 200", w.Code, w.Body.String())
		}
	})

	t.Run("test message", func(t *testing.T) {
		recipient := ports.SlackRecipient{
			TypedText: "#eng", Kind: ports.SlackRecipientChannel,
			ID: "C12345678", DisplayName: "#eng",
		}
		service := &delayedSendSlackService{
			fakeSlackService: fakeSlackService{
				results: []ports.SlackDeliveryResult{{Recipient: recipient, Delivered: true}},
			},
			started: make(chan struct{}),
			release: make(chan struct{}),
		}
		recorder := &slackMutationRecorder{
			token: "xoxb-stored-1234", recipients: []ports.SlackRecipient{recipient},
		}
		handler := newAPIHandler(HandlerOptions{
			Name: "Local agent", Config: config.NewDefault(), Slack: service, Mutations: recorder,
			DisableHostValidation: true,
		}).routes()

		done := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			done <- postSlackOperation(handler, apiPathSlackTestMessage, map[string]any{}, true)
		}()
		<-service.started
		assertUnrelatedSlackWriteCompletes(t, handler)
		close(service.release)
		if w := <-done; w.Code != http.StatusOK {
			t.Fatalf("test message status = %d body=%s; want 200", w.Code, w.Body.String())
		}
	})
}

func assertUnrelatedSlackWriteCompletes(t testing.TB, handler http.Handler) {
	t.Helper()
	start := time.Now()
	write := patchTrustedJSON(handler, apiPathConfigRuntime, map[string]any{
		"notifications": map[string]any{"mute_feature_input": true},
	})
	if write.Code != http.StatusOK {
		t.Fatalf("unrelated write status = %d body=%s; want 200", write.Code, write.Body.String())
	}
	if elapsed := time.Since(start); elapsed > 250*time.Millisecond {
		t.Fatalf("unrelated write elapsed = %s; Slack operation held a mutation lock", elapsed)
	}
}

func TestSlackRuntimeMutationCategoriesPatchIsLocalOnly(t *testing.T) {
	service := &fakeSlackService{}
	recorder := &slackMutationRecorder{}
	api := newAPIHandler(HandlerOptions{
		Config: config.NewDefault(), Slack: service, Mutations: recorder, DisableHostValidation: true,
	})
	events, _, _ := api.broker.subscribeAfter(0, "")
	defer api.broker.unsubscribe(events)
	handler := api.routes()

	w := patchTrustedJSON(handler, apiPathConfigRuntime, map[string]any{
		"slack": map[string]any{"categories": map[string]any{"progress": false}},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	if service.calls.Load() != 0 {
		t.Fatalf("Slack calls = %d; want zero for a categories-only patch", service.calls.Load())
	}
	select {
	case evt := <-events:
		if evt.Kind != sseEventConfigUpdated {
			t.Fatalf("event kind = %q; want %q", evt.Kind, sseEventConfigUpdated)
		}
		if !evt.SnapshotRequired || evt.Resource.Type != resourceTypeRuntime {
			t.Fatalf("config.updated event = %#v; want snapshot-required runtime event", evt)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("categories patch published no configuration-updated event")
	}
	select {
	case evt := <-events:
		t.Fatalf("categories patch published a second event: %#v", evt)
	default:
	}
}

func TestSlackRuntimeMutationRejectsUnknownCategoryField(t *testing.T) {
	recorder := &slackMutationRecorder{}
	handler := newAPIHandler(HandlerOptions{
		Config: config.NewDefault(), Slack: &fakeSlackService{}, Mutations: recorder,
		DisableHostValidation: true,
	}).routes()

	w := patchTrustedJSON(handler, apiPathConfigRuntime, map[string]any{
		"slack": map[string]any{"categories": map[string]any{"progres": false}},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s; want 400", w.Code, w.Body.String())
	}
	if recorder.runtimeCalls.Load() != 0 {
		t.Fatal("unknown category field reached the mutation target")
	}
	body := decodeErrorBody(t, w)
	if body.Error.Code != string(errcat.BadRequest) {
		t.Fatalf("error code = %q; want canonical bad request", body.Error.Code)
	}
}

func TestSlackRuntimeConfigReadProjectsCategories(t *testing.T) {
	cases := []struct {
		name        string
		categories  *config.SlackCategories
		wantOn      []bool
		wantHasFile bool
	}{
		{name: "no stored mapping reads as all on", wantOn: []bool{true, true, true}},
		{
			name: "stored mapping projects stored values",
			categories: config.NormalizeCategories(config.SlackCategories{
				Progress: false, NeedsInput: true, Problems: true,
			}),
			wantOn:      []bool{false, true, true},
			wantHasFile: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.NewDefault()
			cfg.Slack = &config.SlackConfig{Enabled: true, Token: "xoxb-sentinel-1234", Categories: tc.categories}
			if tc.wantHasFile {
				cfg.Slack.Categories = config.NormalizeCategories(
					cfg.Slack.Categories.Effective(),
				)
			}
			handler := newAPIHandler(HandlerOptions{
				Name: "Local agent", Config: cfg, Slack: &fakeSlackService{},
				Mutations: &slackMutationRecorder{}, DisableHostValidation: true,
			}).routes()

			w := httptest.NewRequest(http.MethodGet, apiPathConfigRuntime, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, w)
			if rec.Code != http.StatusOK {
				t.Fatalf("read status = %d body=%s", rec.Code, rec.Body.String())
			}
			var body struct {
				Slack struct {
					Categories struct {
						Progress   bool `json:"progress"`
						NeedsInput bool `json:"needs_input"`
						Problems   bool `json:"problems"`
					} `json:"categories"`
				} `json:"slack"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			got := []bool{
				body.Slack.Categories.Progress,
				body.Slack.Categories.NeedsInput,
				body.Slack.Categories.Problems,
			}
			want := []bool{tc.wantOn[0], tc.wantOn[1], tc.wantOn[2]}
			if !slices.Equal(got, want) {
				t.Fatalf("categories = %v; want %v", got, want)
			}
		})
	}
}
