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

package slack

import (
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/slack/testsupport"
)

func TestServiceValidateBotAndUserTokens(t *testing.T) {
	for _, tc := range []struct {
		name     string
		token    string
		wantType ports.SlackTokenType
		botID    string
	}{
		{"bot", "xoxb-secret-1234", ports.SlackTokenBot, "B123"},
		{"user", "xoxp-secret-1234", ports.SlackTokenUser, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := testsupport.New(t)
			server.Script("auth.test", testsupport.Response{
				Body: map[string]any{
					"ok": true, "team": "Example", "team_id": "T123",
					"user": "agentico", "user_id": "U123", "bot_id": tc.botID,
				},
				Headers: http.Header{"X-Oauth-Scopes": []string{strings.Join(RequiredScopes(), ",")}},
			})
			service := NewService(WithClientFactory(func(token string) (authTester, error) {
				return NewClient(token, WithBaseURL(server.URL()))
			}))
			got, err := service.Validate(t.Context(), tc.token)
			if err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			if got.TokenType != tc.wantType || got.Identity.BotID != tc.botID ||
				got.Identity.TeamID != "T123" || len(got.MissingScopes) != 0 {
				t.Fatalf("Validate() = %#v; want %s identity with no missing scopes", got, tc.wantType)
			}
		})
	}
}

func TestServiceValidationCanonicalErrors(t *testing.T) {
	t.Run("unsupported makes no request", func(t *testing.T) {
		server := testsupport.New(t)
		service := NewService(WithClientFactory(func(token string) (authTester, error) {
			return NewClient(token, WithBaseURL(server.URL()))
		}))
		_, err := service.Validate(t.Context(), "xoxa-secret-1234")
		assertSlackCode(t, err, errcat.SlackUnsupportedToken)
		if got := len(server.AllRequests()); got != 0 {
			t.Fatalf("unsupported token requests = %d; want 0", got)
		}
	})

	t.Run("invalid auth", func(t *testing.T) {
		server := testsupport.New(t)
		server.Script("auth.test", testsupport.Response{
			Body: map[string]any{"ok": false, "error": "invalid_auth"},
		})
		service := serviceForServer(server)
		_, err := service.Validate(t.Context(), "xoxb-secret-1234")
		canonical := assertSlackCode(t, err, errcat.SlackInvalidToken)
		if canonical.Diagnostics != "Slack auth.test returned invalid_auth" {
			t.Fatalf("invalid auth diagnostics = %q", canonical.Diagnostics)
		}
	})

	t.Run("missing scopes stable", func(t *testing.T) {
		server := testsupport.New(t)
		granted := RequiredScopes()[2:]
		server.Script("auth.test", testsupport.Response{
			Body:    map[string]any{"ok": true},
			Headers: http.Header{"X-Oauth-Scopes": []string{strings.Join(granted, ",")}},
		})
		service := serviceForServer(server)
		_, err := service.Validate(t.Context(), "xoxb-secret-1234")
		canonical := assertSlackCode(t, err, errcat.SlackMissingScopes)
		if !strings.Contains(canonical.Summary, "chat:write, im:write") {
			t.Fatalf("missing-scopes summary = %q; want sorted missing names", canonical.Summary)
		}
	})

	t.Run("unreachable", func(t *testing.T) {
		service := NewService(WithClientFactory(func(token string) (authTester, error) {
			return NewClient(token, WithBaseURL("http://127.0.0.1:1/api/"), WithTimeout(100*time.Millisecond))
		}))
		_, err := service.Validate(t.Context(), "xoxb-secret-1234")
		assertSlackCode(t, err, errcat.SlackUnreachable)
	})
}

func TestServiceValidationErrorsNeverContainToken(t *testing.T) {
	const token = "xoxb-distinctive-secret-1234"
	server := testsupport.New(t)
	server.Script("auth.test", testsupport.Response{
		Status: http.StatusInternalServerError,
		Body:   map[string]any{"ok": false, "echo": token},
	})
	service := serviceForServer(server)
	_, err := service.Validate(t.Context(), token)
	canonical := assertSlackCode(t, err, errcat.SlackUnreachable)
	if strings.Contains(err.Error(), token) || strings.Contains(canonical.Diagnostics, token) {
		t.Fatalf("Validate() leaked token in error: %v / %#v", err, canonical)
	}
}

func TestServiceStatusDerivationAndPublication(t *testing.T) {
	var publishes atomic.Int32
	service := NewService(WithPublishHook(func() { publishes.Add(1) }))

	if got := service.Status(ports.SlackStatusInput{}); got.State != ports.SlackNotConfigured ||
		got.LastError != nil || got.LastChecked != nil {
		t.Fatalf("Status(no token) = %#v; want clean not_configured", got)
	}
	if got := service.Status(ports.SlackStatusInput{Token: "xoxb-secret"}); got.State != ports.SlackWarning {
		t.Fatalf("Status(token only) = %#v; want warning", got)
	}
	if got := service.Status(ports.SlackStatusInput{Token: "xoxb-secret", HasIdentity: true}); got.State != ports.SlackConnected {
		t.Fatalf("Status(identity) = %#v; want connected", got)
	}

	checkedAt := time.Date(2026, 9, 19, 12, 0, 0, 0, time.FixedZone("offset", 3600))
	unreachable := errcat.New(errcat.SlackUnreachable, errcat.WithDiagnostics("timeout"))
	service.RecordValidationFailure(checkedAt, unreachable)
	got := service.Status(ports.SlackStatusInput{Token: "xoxb-secret", HasIdentity: true})
	if got.State != ports.SlackWarning || got.LastError == nil ||
		got.LastError.Code != errcat.SlackUnreachable || got.LastChecked == nil ||
		got.LastChecked.Location() != time.UTC {
		t.Fatalf("Status(failure) = %#v; want UTC warning snapshot", got)
	}

	service.RecordValidationSuccess(checkedAt.Add(time.Minute))
	got = service.Status(ports.SlackStatusInput{Token: "xoxb-secret", HasIdentity: true})
	if got.State != ports.SlackConnected || got.LastError != nil {
		t.Fatalf("Status(success) = %#v; want connected", got)
	}
	service.ClearStatus()
	if got := publishes.Load(); got != 3 {
		t.Fatalf("publish hook calls = %d; want 3 snapshot changes", got)
	}
}

func serviceForServer(server *testsupport.Server) *Service {
	return NewService(WithClientFactory(func(token string) (authTester, error) {
		return NewClient(token, WithBaseURL(server.URL()))
	}))
}

func assertSlackCode(t testing.TB, err error, want errcat.Code) errcat.Error {
	t.Helper()
	var validationErr *ports.SlackValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("error = %#v; want SlackValidationError", err)
	}
	if validationErr.Canonical.Code != want {
		t.Fatalf("canonical code = %q; want %q", validationErr.Canonical.Code, want)
	}
	return validationErr.Canonical
}
