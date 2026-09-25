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
	"net/http"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/slack/testsupport"
)

func TestServiceSendTestMessageIsSequentialAndContinuesDestinationFailures(t *testing.T) {
	server := testsupport.New(t)
	server.Script("conversations.open", testsupport.Response{Body: map[string]any{
		"ok": true, "channel": map[string]any{"id": "D12345678"},
	}})
	server.Script("chat.postMessage",
		testsupport.Response{Body: map[string]any{"ok": true}},
		testsupport.Response{Body: map[string]any{"ok": false, "error": "not_in_channel"}},
		testsupport.Response{Body: map[string]any{"ok": true}},
	)
	recipients := []ports.SlackRecipient{
		{TypedText: "@alex", Kind: ports.SlackRecipientUser, ID: "U12345678", DisplayName: "Alex"},
		{TypedText: "#private-ops", Kind: ports.SlackRecipientChannel, ID: "G12345678", DisplayName: "#private-ops"},
		{TypedText: "#eng", Kind: ports.SlackRecipientChannel, ID: "C12345678", DisplayName: "#eng"},
	}

	results, err := serviceForServer(server).SendTestMessage(
		t.Context(), "xoxb-secret-1234", "My Server", recipients,
	)
	if err != nil {
		t.Fatalf("SendTestMessage() error = %v", err)
	}
	if len(results) != 3 || !results[0].Delivered || results[0].Error != nil ||
		results[1].Delivered || results[1].Error == nil ||
		results[1].Error.Code != errcat.SlackNotInChannel ||
		!results[2].Delivered || results[2].Error != nil {
		t.Fatalf("SendTestMessage() = %#v; want delivered, not-in-channel, delivered", results)
	}
	if results[1].Error.Remediation == nil ||
		!strings.Contains(results[1].Error.Remediation.Hint, "Invite the Agentico app to #private-ops in Slack") {
		t.Fatalf("second result remediation = %#v; want invite guidance", results[1].Error.Remediation)
	}

	requests := server.AllRequests()
	if len(requests) != 4 ||
		requests[0].Path != "/api/conversations.open" ||
		requests[1].Path != "/api/chat.postMessage" ||
		requests[2].Path != "/api/chat.postMessage" ||
		requests[3].Path != "/api/chat.postMessage" {
		t.Fatalf("Slack request order = %#v; want open then three sequential posts", requests)
	}
	if requests[0].Fields["users"] != "U12345678" ||
		requests[1].Fields["channel"] != "D12345678" ||
		requests[2].Fields["channel"] != "G12345678" ||
		requests[3].Fields["channel"] != "C12345678" {
		t.Fatalf("Slack request fields = %#v; want list-order destinations", requests)
	}
	for _, request := range requests[1:] {
		text, _ := request.Fields["text"].(string)
		if !strings.Contains(text, "My Server") {
			t.Fatalf("chat.postMessage text = %q; want server display name", text)
		}
	}
}

func TestServiceSendTestMessageMapsPerRecipientFailures(t *testing.T) {
	tests := []struct {
		name      string
		recipient ports.SlackRecipient
		method    string
		response  testsupport.Response
		want      errcat.Code
	}{
		{
			name:      "archived channel",
			recipient: ports.SlackRecipient{Kind: ports.SlackRecipientChannel, ID: "C12345678", DisplayName: "#old"},
			method:    "chat.postMessage",
			response:  testsupport.Response{Body: map[string]any{"ok": false, "error": "is_archived"}},
			want:      errcat.SlackChannelArchived,
		},
		{
			name:      "channel not found",
			recipient: ports.SlackRecipient{Kind: ports.SlackRecipientChannel, ID: "C12345678", DisplayName: "#gone"},
			method:    "chat.postMessage",
			response:  testsupport.Response{Body: map[string]any{"ok": false, "error": "channel_not_found"}},
			want:      errcat.SlackChannelNotFound,
		},
		{
			name:      "user not found opening DM",
			recipient: ports.SlackRecipient{Kind: ports.SlackRecipientUser, ID: "U12345678", DisplayName: "Alex"},
			method:    "conversations.open",
			response:  testsupport.Response{Body: map[string]any{"ok": false, "error": "user_not_found"}},
			want:      errcat.SlackUserNotFound,
		},
		{
			name:      "cannot DM bot",
			recipient: ports.SlackRecipient{Kind: ports.SlackRecipientUser, ID: "U12345678", DisplayName: "Bot"},
			method:    "conversations.open",
			response:  testsupport.Response{Body: map[string]any{"ok": false, "error": "cannot_dm_bot"}},
			want:      errcat.SlackDeliveryFailed,
		},
		{
			name:      "transport failure",
			recipient: ports.SlackRecipient{Kind: ports.SlackRecipientChannel, ID: "C12345678", DisplayName: "#eng"},
			method:    "chat.postMessage",
			response:  testsupport.Response{Status: http.StatusServiceUnavailable},
			want:      errcat.SlackUnreachable,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := testsupport.New(t)
			server.Script(tc.method, tc.response)
			results, err := serviceForServer(server).SendTestMessage(
				t.Context(), "xoxb-secret", "Server", []ports.SlackRecipient{tc.recipient},
			)
			if err != nil {
				t.Fatalf("SendTestMessage() error = %v", err)
			}
			if len(results) != 1 || results[0].Delivered || results[0].Error == nil ||
				results[0].Error.Code != tc.want {
				t.Fatalf("SendTestMessage() = %#v; want recipient error %q", results, tc.want)
			}
			if tc.want == errcat.SlackDeliveryFailed &&
				!strings.Contains(results[0].Error.Diagnostics, "cannot_dm_bot") {
				t.Fatalf("delivery diagnostics = %q; want Slack error string", results[0].Error.Diagnostics)
			}
		})
	}
}

func TestServiceSendTestMessageStopsOnCredentialFailureAndScrubsToken(t *testing.T) {
	const token = "xoxb-distinctive-secret-1234"
	server := testsupport.New(t)
	server.Script("chat.postMessage",
		testsupport.Response{Body: map[string]any{"ok": false, "error": "invalid_auth: " + token}},
		testsupport.Response{Body: map[string]any{"ok": true}},
	)
	recipients := []ports.SlackRecipient{
		{Kind: ports.SlackRecipientChannel, ID: "C11111111", DisplayName: "#one"},
		{Kind: ports.SlackRecipientChannel, ID: "C22222222", DisplayName: "#two"},
	}

	results, err := serviceForServer(server).SendTestMessage(t.Context(), token, "Server", recipients)
	if results != nil {
		t.Fatalf("SendTestMessage() results = %#v; want nil on credential failure", results)
	}
	canonical := assertSlackCode(t, err, errcat.SlackInvalidToken)
	if got := server.CallCount("chat.postMessage"); got != 1 {
		t.Fatalf("chat.postMessage calls = %d; want 1 after credential failure", got)
	}
	if strings.Contains(err.Error(), token) || strings.Contains(canonical.Diagnostics, token) {
		t.Fatalf("SendTestMessage() leaked token: %v / %#v", err, canonical)
	}
}
