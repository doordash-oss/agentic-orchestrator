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
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/slack/testsupport"
)

func TestClientAuthTestReturnsIdentityScopesAndBearer(t *testing.T) {
	server := testsupport.New(t)
	server.Script("auth.test", testsupport.Response{
		Body: map[string]any{
			"ok":      true,
			"team":    "Example",
			"team_id": "T123",
			"user":    "agentico",
			"user_id": "U123",
			"url":     "https://example.slack.com/",
			"bot_id":  "B123",
		},
		Headers: http.Header{"X-Oauth-Scopes": []string{" users:read,chat:write, users:read "}},
	})
	client, err := NewClient("xoxb-secret-1234", WithBaseURL(server.URL()))
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.AuthTest(t.Context())
	if err != nil {
		t.Fatalf("AuthTest() error = %v", err)
	}
	if got.TeamID != "T123" || got.TeamName != "Example" || got.UserID != "U123" ||
		got.UserName != "agentico" || got.BotID != "B123" ||
		got.WorkspaceURL != "https://example.slack.com/" {
		t.Fatalf("AuthTest() = %#v; want scripted identity", got)
	}
	if strings.Join(got.GrantedScopes, ",") != "chat:write,users:read" {
		t.Fatalf("AuthTest().GrantedScopes = %#v; want sorted deduplicated scopes", got.GrantedScopes)
	}
	requests := server.Requests("auth.test")
	if len(requests) != 1 || requests[0].Method != http.MethodPost ||
		requests[0].Path != "/api/auth.test" || !requests[0].BearerPresent {
		t.Fatalf("auth.test requests = %#v; want one bearer POST", requests)
	}
}

func TestClientAuthTestAPIErrorsPreserveSlackCode(t *testing.T) {
	for _, slackCode := range []string{"invalid_auth", "token_revoked", "account_inactive", "missing_scope"} {
		t.Run(slackCode, func(t *testing.T) {
			server := testsupport.New(t)
			server.Script("auth.test", testsupport.Response{
				Body: map[string]any{"ok": false, "error": slackCode},
			})
			client, err := NewClient("xoxb-secret-1234", WithBaseURL(server.URL()))
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.AuthTest(t.Context())
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.SlackError != slackCode {
				t.Fatalf("AuthTest() error = %#v; want APIError(%q)", err, slackCode)
			}
		})
	}
}

func TestClientAuthTestAPIErrorsScrubEchoedToken(t *testing.T) {
	const token = "xoxb-distinctive-secret-1234"
	server := testsupport.New(t)
	server.Script("auth.test", testsupport.Response{
		Body: map[string]any{"ok": false, "error": "invalid_auth: " + token},
	})
	client, err := NewClient(token, WithBaseURL(server.URL()))
	if err != nil {
		t.Fatal(err)
	}

	_, err = client.AuthTest(t.Context())
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("AuthTest() error = %#v; want APIError", err)
	}
	if strings.Contains(apiErr.SlackError, token) || strings.Contains(err.Error(), token) {
		t.Fatalf("API error leaked token: %#v", apiErr)
	}
	if apiErr.SlackError != "invalid_auth: [REDACTED]" {
		t.Fatalf("APIError.SlackError = %q; want scrubbed Slack error", apiErr.SlackError)
	}
}

func TestClientTransportErrorsAndTokenScrubbing(t *testing.T) {
	const token = "xoxb-distinctive-secret-1234"
	cases := []struct {
		name       string
		response   testsupport.Response
		wantStatus int
		wantRetry  time.Duration
	}{
		{
			name: "server error",
			response: testsupport.Response{
				Status: http.StatusInternalServerError,
				Body:   "echo " + token,
			},
			wantStatus: http.StatusInternalServerError,
		},
		{
			name: "rate limit",
			response: testsupport.Response{
				Status:  http.StatusTooManyRequests,
				Body:    map[string]any{"ok": false, "token": token},
				Headers: http.Header{"Retry-After": []string{"7"}},
			},
			wantStatus: http.StatusTooManyRequests,
			wantRetry:  7 * time.Second,
		},
		{
			name: "malformed JSON",
			response: testsupport.Response{
				Body: `{"ok":true,"echo":"` + token,
			},
			wantStatus: http.StatusOK,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := testsupport.New(t)
			server.Script("auth.test", tc.response)
			client, err := NewClient(token, WithBaseURL(server.URL()))
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.AuthTest(t.Context())
			var transportErr *TransportError
			if !errors.As(err, &transportErr) {
				t.Fatalf("AuthTest() error = %#v; want TransportError", err)
			}
			if transportErr.StatusCode != tc.wantStatus || transportErr.RetryAfter != tc.wantRetry {
				t.Fatalf("TransportError = %#v; want status %d retry %s", transportErr, tc.wantStatus, tc.wantRetry)
			}
			if strings.Contains(err.Error(), token) || strings.Contains(transportErr.Detail, token) {
				t.Fatalf("transport error leaked token: %#v", transportErr)
			}
			if tc.name == "malformed JSON" && !strings.Contains(err.Error(), "decoding JSON response") {
				t.Fatalf("AuthTest() error = %q; want decoding context", err)
			}
		})
	}
}

func TestClientTimeoutAndUnreachableAreBounded(t *testing.T) {
	const timeout = 40 * time.Millisecond
	server := testsupport.New(t)
	server.Script("auth.test", testsupport.Response{
		Delay: 500 * time.Millisecond,
		Body:  map[string]any{"ok": true},
	})
	client, err := NewClient("xoxp-secret-1234", WithBaseURL(server.URL()), WithTimeout(timeout))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = client.AuthTest(context.Background())
	var transportErr *TransportError
	if !errors.As(err, &transportErr) {
		t.Fatalf("AuthTest() timeout error = %#v; want TransportError", err)
	}
	if elapsed := time.Since(start); elapsed > 5*timeout {
		t.Fatalf("AuthTest() elapsed = %s; want bounded near %s", elapsed, timeout)
	}

	client, err = NewClient("xoxp-secret-1234",
		WithBaseURL("http://127.0.0.1:1/api/"),
		WithTimeout(200*time.Millisecond),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.AuthTest(t.Context()); !errors.As(err, &transportErr) {
		t.Fatalf("AuthTest() unreachable error = %#v; want TransportError", err)
	}
}

func TestClientEnvironmentBaseURL(t *testing.T) {
	server := testsupport.New(t)
	server.Script("auth.test", testsupport.Response{Body: map[string]any{"ok": true}})
	t.Setenv(EnvSlackAPIBase, server.URL())
	client, err := NewClient("xoxb-secret-1234")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.AuthTest(t.Context()); err != nil {
		t.Fatalf("AuthTest() with %s error = %v", EnvSlackAPIBase, err)
	}
}
