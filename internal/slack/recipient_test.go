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
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/slack/testsupport"
)

func TestServiceResolveRecipientUserForms(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		method     string
		response   map[string]any
		wantID     string
		wantName   string
		wantFields map[string]any
	}{
		{
			name:   "email",
			input:  " alex@example.com ",
			method: "users.lookupByEmail",
			response: map[string]any{"ok": true, "user": slackUser(
				"U12345678", "alex", "Alex Example", "Alex E.", false, false,
			)},
			wantID:     "U12345678",
			wantName:   "Alex Example",
			wantFields: map[string]any{"email": "alex@example.com"},
		},
		{
			name:   "member ID",
			input:  "U87654321",
			method: "users.info",
			response: map[string]any{"ok": true, "user": slackUser(
				"U87654321", "sam", "", "Sam Display", false, false,
			)},
			wantID:     "U87654321",
			wantName:   "Sam Display",
			wantFields: map[string]any{"user": "U87654321"},
		},
		{
			name:   "username handle",
			input:  "@alex",
			method: "users.list",
			response: map[string]any{"ok": true, "members": []any{
				slackUser("U11111111", "ignored", "", "ignored", true, false),
				slackUser("U22222222", "alex", "", "", false, false),
			}},
			wantID:   "U22222222",
			wantName: "alex",
			wantFields: map[string]any{
				"limit": "200",
			},
		},
		{
			name:   "display name case insensitive",
			input:  "AL",
			method: "users.list",
			response: map[string]any{"ok": true, "members": []any{
				slackUser("U33333333", "alexandra", "Alexandra Example", "al", false, false),
				slackUser("U44444444", "al", "", "bot match", false, true),
			}},
			wantID:   "U33333333",
			wantName: "Alexandra Example",
			wantFields: map[string]any{
				"limit": "200",
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := testsupport.New(t)
			server.Script(tc.method, testsupport.Response{Body: tc.response})
			service := serviceForServer(server)

			got, err := service.ResolveRecipient(t.Context(), "xoxb-secret-1234", tc.input)
			if err != nil {
				t.Fatalf("ResolveRecipient(%q) error = %v", tc.input, err)
			}
			want := ports.SlackRecipient{
				TypedText:   strings.TrimSpace(tc.input),
				Kind:        ports.SlackRecipientUser,
				ID:          tc.wantID,
				DisplayName: tc.wantName,
			}
			if got != want {
				t.Fatalf("ResolveRecipient(%q) = %#v; want %#v", tc.input, got, want)
			}
			requests := server.Requests(tc.method)
			if len(requests) != 1 {
				t.Fatalf("%s requests = %d; want 1", tc.method, len(requests))
			}
			for key, wantValue := range tc.wantFields {
				if gotValue := requests[0].Fields[key]; gotValue != wantValue {
					t.Fatalf("%s field %q = %#v; want %#v", tc.method, key, gotValue, wantValue)
				}
			}
		})
	}
}

func TestServiceResolveRecipientHandlePaginationAmbiguityAndCap(t *testing.T) {
	t.Run("walks every page then accepts one match", func(t *testing.T) {
		server := testsupport.New(t)
		server.Script("users.list",
			testsupport.Response{Body: map[string]any{
				"ok": true,
				"members": []any{
					slackUser("U11111111", "other", "", "", false, false),
				},
				"response_metadata": map[string]any{"next_cursor": "page-two"},
			}},
			testsupport.Response{Body: map[string]any{
				"ok": true,
				"members": []any{
					slackUser("U22222222", "alex", "Alex Example", "", false, false),
				},
				"response_metadata": map[string]any{"next_cursor": ""},
			}},
		)
		got, err := serviceForServer(server).ResolveRecipient(t.Context(), "xoxb-secret", "@alex")
		if err != nil {
			t.Fatalf("ResolveRecipient() error = %v", err)
		}
		if got.ID != "U22222222" || server.CallCount("users.list") != 2 {
			t.Fatalf("ResolveRecipient() = %#v, calls = %d; want page-two match and two calls", got, server.CallCount("users.list"))
		}
		requests := server.Requests("users.list")
		if requests[1].Fields["cursor"] != "page-two" {
			t.Fatalf("second users.list fields = %#v; want page-two cursor", requests[1].Fields)
		}
	})

	t.Run("two matches on one page are ambiguous", func(t *testing.T) {
		server := testsupport.New(t)
		server.Script("users.list", testsupport.Response{Body: map[string]any{
			"ok": true,
			"members": []any{
				slackUser("U11111111", "alex", "", "", false, false),
				slackUser("U22222222", "other", "", "Alex", false, false),
			},
		}})
		_, err := serviceForServer(server).ResolveRecipient(t.Context(), "xoxb-secret", "@alex")
		canonical := assertSlackCode(t, err, errcat.SlackAmbiguousHandle)
		if !strings.Contains(canonical.Summary, "2") {
			t.Fatalf("ambiguous summary = %q; want match count", canonical.Summary)
		}
	})

	t.Run("matches on different pages are ambiguous", func(t *testing.T) {
		server := testsupport.New(t)
		server.Script("users.list",
			testsupport.Response{Body: map[string]any{
				"ok": true,
				"members": []any{
					slackUser("U11111111", "alex", "", "", false, false),
				},
				"response_metadata": map[string]any{"next_cursor": "page-two"},
			}},
			testsupport.Response{Body: map[string]any{
				"ok": true,
				"members": []any{
					slackUser("U22222222", "other", "", "Alex", false, false),
				},
			}},
		)
		_, err := serviceForServer(server).ResolveRecipient(t.Context(), "xoxb-secret", "@alex")
		canonical := assertSlackCode(t, err, errcat.SlackAmbiguousHandle)
		if !strings.Contains(canonical.Summary, "2") || server.CallCount("users.list") != 2 {
			t.Fatalf("ambiguous summary = %q, calls = %d; want match count after both pages",
				canonical.Summary, server.CallCount("users.list"))
		}
	})

	t.Run("twenty pages reach scan cap", func(t *testing.T) {
		server := testsupport.New(t)
		for page := 1; page <= 20; page++ {
			nextCursor := fmt.Sprintf("page-%d", page+1)
			server.Script("users.list", testsupport.Response{Body: map[string]any{
				"ok":                true,
				"members":           []any{},
				"response_metadata": map[string]any{"next_cursor": nextCursor},
			}})
		}
		_, err := serviceForServer(server).ResolveRecipient(t.Context(), "xoxb-secret", "@missing")
		assertSlackCode(t, err, errcat.SlackScanCapReached)
		if got := server.CallCount("users.list"); got != 20 {
			t.Fatalf("users.list calls = %d; want 20", got)
		}
	})
}

func TestServiceResolveRecipientChannels(t *testing.T) {
	t.Run("name resolves when member", func(t *testing.T) {
		server := testsupport.New(t)
		server.Script("conversations.list", testsupport.Response{Body: map[string]any{
			"ok": true,
			"channels": []any{
				slackChannel("C12345678", "eng", true, false),
			},
		}})
		got, err := serviceForServer(server).ResolveRecipient(t.Context(), "xoxb-secret", "#Eng")
		if err != nil {
			t.Fatalf("ResolveRecipient() error = %v", err)
		}
		want := ports.SlackRecipient{
			TypedText: "#Eng", Kind: ports.SlackRecipientChannel,
			ID: "C12345678", DisplayName: "#eng",
		}
		if got != want {
			t.Fatalf("ResolveRecipient() = %#v; want %#v", got, want)
		}
	})

	t.Run("membership and archive failures are specific", func(t *testing.T) {
		tests := []struct {
			name    string
			channel map[string]any
			want    errcat.Code
		}{
			{"not member", slackChannel("C12345678", "eng", false, false), errcat.SlackNotInChannel},
			{"archived", slackChannel("C12345678", "eng", true, true), errcat.SlackChannelArchived},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				server := testsupport.New(t)
				server.Script("conversations.list", testsupport.Response{Body: map[string]any{
					"ok": true, "channels": []any{tc.channel},
				}})
				_, err := serviceForServer(server).ResolveRecipient(t.Context(), "xoxb-secret", "#eng")
				canonical := assertSlackCode(t, err, tc.want)
				if tc.want == errcat.SlackNotInChannel &&
					(canonical.Remediation == nil ||
						!strings.Contains(canonical.Remediation.Hint, "Invite the Agentico app to #eng in Slack")) {
					t.Fatalf("not-in-channel remediation = %#v; want channel invite guidance", canonical.Remediation)
				}
			})
		}
	})

	t.Run("channel ID uses conversations info", func(t *testing.T) {
		server := testsupport.New(t)
		server.Script("conversations.info", testsupport.Response{Body: map[string]any{
			"ok": true, "channel": slackChannel("G87654321", "private-ops", true, false),
		}})
		got, err := serviceForServer(server).ResolveRecipient(t.Context(), "xoxb-secret", "G87654321")
		if err != nil {
			t.Fatalf("ResolveRecipient() error = %v", err)
		}
		if got.ID != "G87654321" || got.DisplayName != "#private-ops" {
			t.Fatalf("ResolveRecipient() = %#v; want channel ID result", got)
		}
		if fields := server.Requests("conversations.info")[0].Fields; fields["channel"] != "G87654321" {
			t.Fatalf("conversations.info fields = %#v; want channel ID", fields)
		}
	})

	t.Run("walks cursor then stops on name match", func(t *testing.T) {
		server := testsupport.New(t)
		server.Script("conversations.list",
			testsupport.Response{Body: map[string]any{
				"ok": true,
				"channels": []any{
					slackChannel("C11111111", "other", true, false),
				},
				"response_metadata": map[string]any{"next_cursor": "page-two"},
			}},
			testsupport.Response{Body: map[string]any{
				"ok": true,
				"channels": []any{
					slackChannel("C22222222", "eng", true, false),
				},
				"response_metadata": map[string]any{"next_cursor": "unused"},
			}},
		)
		got, err := serviceForServer(server).ResolveRecipient(t.Context(), "xoxb-secret", "#eng")
		if err != nil {
			t.Fatalf("ResolveRecipient() error = %v", err)
		}
		if got.ID != "C22222222" || server.CallCount("conversations.list") != 2 {
			t.Fatalf("ResolveRecipient() = %#v, calls = %d; want page-two match and two calls", got, server.CallCount("conversations.list"))
		}
		requests := server.Requests("conversations.list")
		if requests[1].Fields["cursor"] != "page-two" {
			t.Fatalf("second conversations.list fields = %#v; want page-two cursor", requests[1].Fields)
		}
	})

	t.Run("twenty pages reach scan cap", func(t *testing.T) {
		server := testsupport.New(t)
		for page := 1; page <= 20; page++ {
			nextCursor := fmt.Sprintf("page-%d", page+1)
			server.Script("conversations.list", testsupport.Response{Body: map[string]any{
				"ok":                true,
				"channels":          []any{},
				"response_metadata": map[string]any{"next_cursor": nextCursor},
			}})
		}
		_, err := serviceForServer(server).ResolveRecipient(t.Context(), "xoxb-secret", "#missing")
		canonical := assertSlackCode(t, err, errcat.SlackScanCapReached)
		if got := server.CallCount("conversations.list"); got != 20 {
			t.Fatalf("conversations.list calls = %d; want 20", got)
		}
		if canonical.Remediation == nil ||
			!strings.Contains(canonical.Remediation.Hint, "email") ||
			!strings.Contains(canonical.Remediation.Hint, "Slack ID") {
			t.Fatalf("scan-cap remediation = %#v; want email and Slack ID guidance", canonical.Remediation)
		}
	})
}

func TestServiceResolveRecipientRejectsInvalidGrammarWithoutSlackCall(t *testing.T) {
	for _, tc := range []struct {
		input string
		code  errcat.Code
	}{
		{"", errcat.BadRequest},
		{"   ", errcat.BadRequest},
		{"@", errcat.SlackUnrecognizedRecipient},
		{"#", errcat.SlackUnrecognizedRecipient},
		{"two words", errcat.SlackUnrecognizedRecipient},
	} {
		t.Run(fmt.Sprintf("%q", tc.input), func(t *testing.T) {
			server := testsupport.New(t)
			_, err := serviceForServer(server).ResolveRecipient(t.Context(), "xoxb-secret", tc.input)
			assertSlackCode(t, err, tc.code)
			if got := len(server.AllRequests()); got != 0 {
				t.Fatalf("ResolveRecipient(%q) Slack calls = %d; want 0", tc.input, got)
			}
		})
	}
}

func TestServiceResolveRecipientMapsSlackErrorsAndScrubsToken(t *testing.T) {
	const token = "xoxb-distinctive-secret-1234"
	tests := []struct {
		name     string
		method   string
		input    string
		response testsupport.Response
		want     errcat.Code
	}{
		{"unknown email", "users.lookupByEmail", "nobody@example.com", testsupport.Response{
			Body: map[string]any{"ok": false, "error": "users_not_found"},
		}, errcat.SlackUserNotFound},
		{"invalid token", "users.lookupByEmail", "nobody@example.com", testsupport.Response{
			Body: map[string]any{"ok": false, "error": "invalid_auth: " + token},
		}, errcat.SlackInvalidToken},
		{"revoked token", "users.lookupByEmail", "nobody@example.com", testsupport.Response{
			Body: map[string]any{"ok": false, "error": "token_revoked"},
		}, errcat.SlackInvalidToken},
		{"inactive account", "users.lookupByEmail", "nobody@example.com", testsupport.Response{
			Body: map[string]any{"ok": false, "error": "account_inactive"},
		}, errcat.SlackInvalidToken},
		{"missing scope", "users.lookupByEmail", "nobody@example.com", testsupport.Response{
			Body: map[string]any{"ok": false, "error": "missing_scope", "needed": "users:read.email"},
		}, errcat.SlackMissingScopes},
		{"channel not found", "conversations.info", "C12345678", testsupport.Response{
			Body: map[string]any{"ok": false, "error": "channel_not_found"},
		}, errcat.SlackChannelNotFound},
		{"unreachable", "users.lookupByEmail", "nobody@example.com", testsupport.Response{
			Status: http.StatusServiceUnavailable,
			Body:   map[string]any{"ok": false, "echo": token},
		}, errcat.SlackUnreachable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := testsupport.New(t)
			server.Script(tc.method, tc.response)
			_, err := serviceForServer(server).ResolveRecipient(t.Context(), token, tc.input)
			canonical := assertSlackCode(t, err, tc.want)
			if strings.Contains(err.Error(), token) || strings.Contains(canonical.Diagnostics, token) {
				t.Fatalf("ResolveRecipient() leaked token: %v / %#v", err, canonical)
			}
		})
	}
}

func slackUser(id, name, realName, displayName string, deleted, bot bool) map[string]any {
	return map[string]any{
		"id": id, "name": name, "real_name": realName,
		"profile": map[string]any{"display_name": displayName},
		"deleted": deleted, "is_bot": bot,
	}
}

func slackChannel(id, name string, member, archived bool) map[string]any {
	return map[string]any{
		"id": id, "name": name, "is_member": member, "is_archived": archived,
	}
}
