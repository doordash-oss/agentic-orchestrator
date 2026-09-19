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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

type fakeSlackService struct {
	validation ports.SlackValidation
	err        error
	calls      atomic.Int64
	lastToken  string
	successAt  time.Time
	failureAt  time.Time
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
		MissingScopes json.RawMessage `json:"missing_scopes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if string(body.MissingScopes) != "[]" {
		t.Fatalf("missing_scopes JSON = %s; want []", body.MissingScopes)
	}
}

func TestSlackValidateRouteStoredTokenRefreshesCache(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Slack = &config.SlackConfig{Token: "xoxp-stored-1234", Enabled: false}
	service := &fakeSlackService{validation: ports.SlackValidation{
		TokenType: ports.SlackTokenUser,
		Identity: ports.SlackIdentity{
			TeamID: "T123", TeamName: "Acme", UserID: "U234", DisplayName: "Ada",
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
