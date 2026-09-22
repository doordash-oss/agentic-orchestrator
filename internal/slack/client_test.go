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
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
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

func TestClientUserMethodsSendFormFieldsAndDecodeMembers(t *testing.T) {
	server := testsupport.New(t)
	server.Script("users.lookupByEmail", testsupport.Response{Body: map[string]any{
		"ok": true,
		"user": map[string]any{
			"id": "U12345678", "name": "alex", "real_name": "Alex Example",
			"profile": map[string]any{"display_name": "Alex E."},
		},
	}})
	server.Script("users.info", testsupport.Response{Body: map[string]any{
		"ok": true,
		"user": map[string]any{
			"id": "U87654321", "name": "sam", "deleted": true, "is_bot": true,
			"profile": map[string]any{"display_name": "Sam"},
		},
	}})
	server.Script("users.list",
		testsupport.Response{Body: map[string]any{
			"ok": true,
			"members": []any{
				map[string]any{"id": "U11111111", "name": "first"},
			},
			"response_metadata": map[string]any{"next_cursor": "next-users"},
		}},
		testsupport.Response{Body: map[string]any{
			"ok": true,
			"members": []any{
				map[string]any{"id": "U22222222", "name": "second"},
			},
			"response_metadata": map[string]any{"next_cursor": ""},
		}},
	)
	client, err := NewClient("xoxb-secret-1234", WithBaseURL(server.URL()))
	if err != nil {
		t.Fatal(err)
	}

	emailUser, err := client.LookupUserByEmail(t.Context(), "alex@example.com")
	if err != nil {
		t.Fatalf("LookupUserByEmail() error = %v", err)
	}
	if emailUser.ID != "U12345678" || emailUser.Name != "alex" ||
		emailUser.RealName != "Alex Example" || emailUser.DisplayName != "Alex E." {
		t.Fatalf("LookupUserByEmail() = %#v; want decoded member", emailUser)
	}
	infoUser, err := client.UserInfo(t.Context(), "U87654321")
	if err != nil {
		t.Fatalf("UserInfo() error = %v", err)
	}
	if !infoUser.Deleted || !infoUser.IsBot {
		t.Fatalf("UserInfo() = %#v; want deleted bot flags", infoUser)
	}
	firstPage, err := client.UsersList(t.Context(), "", 200)
	if err != nil {
		t.Fatalf("UsersList(first) error = %v", err)
	}
	secondPage, err := client.UsersList(t.Context(), firstPage.NextCursor, 200)
	if err != nil {
		t.Fatalf("UsersList(second) error = %v", err)
	}
	if firstPage.NextCursor != "next-users" || len(secondPage.Users) != 1 ||
		secondPage.Users[0].ID != "U22222222" {
		t.Fatalf("UsersList pages = %#v / %#v; want cursor walk", firstPage, secondPage)
	}

	if got := server.Requests("users.lookupByEmail")[0].Fields["email"]; got != "alex@example.com" {
		t.Fatalf("users.lookupByEmail email = %#v; want alex@example.com", got)
	}
	if got := server.Requests("users.info")[0].Fields["user"]; got != "U87654321" {
		t.Fatalf("users.info user = %#v; want U87654321", got)
	}
	listRequests := server.Requests("users.list")
	if len(listRequests) != 2 || listRequests[0].Fields["limit"] != "200" ||
		listRequests[1].Fields["cursor"] != "next-users" {
		t.Fatalf("users.list requests = %#v; want limit and second-page cursor", listRequests)
	}
}

func TestClientConversationAndMessageMethodsSendFormFields(t *testing.T) {
	server := testsupport.New(t)
	server.Script("conversations.list", testsupport.Response{Body: map[string]any{
		"ok": true,
		"channels": []any{
			map[string]any{
				"id": "C12345678", "name": "eng", "is_private": true,
				"is_member": true, "is_archived": false,
			},
		},
		"response_metadata": map[string]any{"next_cursor": "next-channels"},
	}})
	server.Script("conversations.info", testsupport.Response{Body: map[string]any{
		"ok": true,
		"channel": map[string]any{
			"id": "G87654321", "name": "private-ops", "is_private": true,
			"is_member": false, "is_archived": true,
		},
	}})
	server.Script("conversations.open", testsupport.Response{Body: map[string]any{
		"ok": true, "channel": map[string]any{"id": "D12345678"},
	}})
	server.Script("chat.postMessage", testsupport.Response{Body: map[string]any{
		"ok": true, "channel": "D12345678", "ts": "1.0",
	}})
	client, err := NewClient("xoxb-secret-1234", WithBaseURL(server.URL()))
	if err != nil {
		t.Fatal(err)
	}

	page, err := client.ConversationsList(t.Context(), "", 200)
	if err != nil {
		t.Fatalf("ConversationsList() error = %v", err)
	}
	if len(page.Conversations) != 1 || !page.Conversations[0].IsPrivate ||
		!page.Conversations[0].IsMember || page.NextCursor != "next-channels" {
		t.Fatalf("ConversationsList() = %#v; want decoded conversation page", page)
	}
	channel, err := client.ConversationInfo(t.Context(), "G87654321")
	if err != nil {
		t.Fatalf("ConversationInfo() error = %v", err)
	}
	if channel.ID != "G87654321" || channel.IsMember || !channel.IsArchived {
		t.Fatalf("ConversationInfo() = %#v; want archived non-member channel", channel)
	}
	dmChannel, err := client.OpenConversation(t.Context(), "U12345678")
	if err != nil {
		t.Fatalf("OpenConversation() error = %v", err)
	}
	if dmChannel != "D12345678" {
		t.Fatalf("OpenConversation() = %q; want D12345678", dmChannel)
	}
	if err := client.PostMessage(t.Context(), dmChannel, "Agentico test"); err != nil {
		t.Fatalf("PostMessage() error = %v", err)
	}

	listFields := server.Requests("conversations.list")[0].Fields
	if listFields["types"] != "public_channel,private_channel" ||
		listFields["exclude_archived"] != "false" ||
		listFields["limit"] != "200" {
		t.Fatalf("conversations.list fields = %#v; want types, archived, and limit", listFields)
	}
	if got := server.Requests("conversations.info")[0].Fields["channel"]; got != "G87654321" {
		t.Fatalf("conversations.info channel = %#v; want G87654321", got)
	}
	if got := server.Requests("conversations.open")[0].Fields["users"]; got != "U12345678" {
		t.Fatalf("conversations.open users = %#v; want U12345678", got)
	}
	postFields := server.Requests("chat.postMessage")[0].Fields
	if postFields["channel"] != "D12345678" || postFields["text"] != "Agentico test" {
		t.Fatalf("chat.postMessage fields = %#v; want channel and text", postFields)
	}
}

func TestClientPostMessageRichSendsBlocksThreadAndBroadcast(t *testing.T) {
	server := testsupport.New(t)
	server.Script("chat.postMessage", testsupport.Response{Body: map[string]any{
		"ok": true, "channel": "C12345678", "ts": "1758499200.000001",
	}})
	client, err := NewClient("xoxb-secret-1234", WithBaseURL(server.URL()))
	if err != nil {
		t.Fatal(err)
	}

	blocks := []Block{
		headerBlockFor("Feature"),
		sectionBlockFor([]textObject{{Type: textTypeMrkdwn, Text: "*Status:* Running"}}),
	}
	result, err := client.PostMessageRich(t.Context(), PostMessageInput{
		Channel:        "C12345678",
		FallbackText:   "Feature — Running",
		Blocks:         blocks,
		ThreadTS:       "1758000000.000001",
		ReplyBroadcast: true,
	})
	if err != nil {
		t.Fatalf("PostMessageRich() error = %v", err)
	}
	if result.TS != "1758499200.000001" || result.Channel != "C12345678" {
		t.Fatalf("PostMessageRich() = %#v; want echoed timestamp and channel", result)
	}
	fields := server.Requests("chat.postMessage")[0].Fields
	if fields["channel"] != "C12345678" || fields["text"] != "Feature — Running" {
		t.Fatalf("post fields = %#v; want channel and fallback text", fields)
	}
	if fields["thread_ts"] != "1758000000.000001" || fields["reply_broadcast"] != "true" {
		t.Fatalf("post fields = %#v; want thread and broadcast flags", fields)
	}
	var sentBlocks []map[string]any
	if err := json.Unmarshal([]byte(fields["blocks"].(string)), &sentBlocks); err != nil {
		t.Fatalf("blocks are not JSON: %v", err)
	}
	if sentBlocks[0]["type"] != "header" || sentBlocks[1]["type"] != "section" {
		t.Fatalf("blocks = %#v; want header then section", sentBlocks)
	}
}

func TestClientPostMessageRichOmitsOptionalFields(t *testing.T) {
	server := testsupport.New(t)
	server.Script("chat.postMessage", testsupport.Response{Body: map[string]any{
		"ok": true, "channel": "C1", "ts": "2.0",
	}})
	client, err := NewClient("xoxb-secret-1234", WithBaseURL(server.URL()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.PostMessageRich(t.Context(), PostMessageInput{
		Channel:      "C1",
		FallbackText: "plain",
	}); err != nil {
		t.Fatalf("PostMessageRich() error = %v", err)
	}
	fields := server.Requests("chat.postMessage")[0].Fields
	for _, absent := range []string{"blocks", "thread_ts", "reply_broadcast"} {
		if _, present := fields[absent]; present {
			t.Fatalf("post fields contain %q: %#v", absent, fields)
		}
	}
}

func TestClientPostMessagePreservesPlainMessageBehavior(t *testing.T) {
	server := testsupport.New(t)
	server.Script("chat.postMessage", testsupport.Response{Body: map[string]any{
		"ok": true, "channel": "C1", "ts": "2.0",
	}})
	client, err := NewClient("xoxb-secret-1234", WithBaseURL(server.URL()))
	if err != nil {
		t.Fatal(err)
	}

	if err := client.PostMessage(t.Context(), "C1", "plain"); err != nil {
		t.Fatalf("PostMessage() error = %v", err)
	}
	fields := server.Requests("chat.postMessage")[0].Fields
	if fields["channel"] != "C1" || fields["text"] != "plain" {
		t.Errorf("PostMessage() fields = %#v; want channel and text", fields)
	}
	for _, absent := range []string{"blocks", "thread_ts", "reply_broadcast"} {
		if _, present := fields[absent]; present {
			t.Errorf("PostMessage() fields contain %q: %#v", absent, fields)
		}
	}
}

func TestClientUpdateMessageSendsChannelTimestampTextAndBlocks(t *testing.T) {
	server := testsupport.New(t)
	server.Script("chat.update", testsupport.Response{Body: map[string]any{"ok": true}})
	client, err := NewClient("xoxb-secret-1234", WithBaseURL(server.URL()))
	if err != nil {
		t.Fatal(err)
	}
	blocks := []Block{headerBlockFor("Feature")}
	if err := client.UpdateMessage(t.Context(), "C12345678", "1758499200.000001", "updated", blocks); err != nil {
		t.Fatalf("UpdateMessage() error = %v", err)
	}
	fields := server.Requests("chat.update")[0].Fields
	if fields["channel"] != "C12345678" || fields["ts"] != "1758499200.000001" ||
		fields["text"] != "updated" {
		t.Fatalf("update fields = %#v; want channel, timestamp, and fallback", fields)
	}
	if !strings.Contains(fields["blocks"].(string), "Feature") {
		t.Fatalf("update blocks = %q; want encoded header block", fields["blocks"])
	}
}

func TestClientRichMethodsMapErrorsAndScrubToken(t *testing.T) {
	server := testsupport.New(t)
	server.Script("chat.postMessage",
		testsupport.Response{Body: map[string]any{"ok": false, "error": "xoxb-secret-1234"}},
		testsupport.Response{Status: http.StatusBadGateway, Body: map[string]any{"ok": false}},
	)
	server.Script("chat.update",
		testsupport.Response{Body: map[string]any{"ok": false, "error": "xoxb-secret-1234"}},
		testsupport.Response{Status: http.StatusBadGateway, Body: map[string]any{"ok": false}},
	)
	client, err := NewClient("xoxb-secret-1234", WithBaseURL(server.URL()))
	if err != nil {
		t.Fatal(err)
	}

	_, err = client.PostMessageRich(t.Context(), PostMessageInput{Channel: "C1", FallbackText: "x"})
	if !errors.As(err, new(*APIError)) || strings.Contains(err.Error(), "xoxb-secret-1234") {
		t.Fatalf("post envelope error = %#v; want scrubbed APIError", err)
	}
	_, err = client.PostMessageRich(t.Context(), PostMessageInput{Channel: "C1", FallbackText: "x"})
	if !errors.As(err, new(*TransportError)) || err.(*TransportError).StatusCode != http.StatusBadGateway {
		t.Fatalf("post transport error = %#v; want 502 TransportError", err)
	}
	err = client.UpdateMessage(t.Context(), "C1", "1.0", "x", nil)
	if !errors.As(err, new(*APIError)) || strings.Contains(err.Error(), "xoxb-secret-1234") {
		t.Fatalf("update envelope error = %#v; want scrubbed APIError", err)
	}
	err = client.UpdateMessage(t.Context(), "C1", "1.0", "x", nil)
	if !errors.As(err, new(*TransportError)) || err.(*TransportError).StatusCode != http.StatusBadGateway {
		t.Fatalf("update transport error = %#v; want 502 TransportError", err)
	}
}

func TestClientUploadFileToThreadUsesExternalUploadPair(t *testing.T) {
	const (
		channelID = "C12345678"
		threadTS  = "1758000000.000001"
		shareTS   = "1758499200.000001"
	)
	payload := []byte("# Phase 3 plan\n\nImplementation details.\n")
	server := testsupport.New(t)
	server.Script("files.completeUploadExternal", testsupport.Response{Body: map[string]any{
		"ok": true,
		"files": []any{map[string]any{
			"id": "F00000001",
			"shares": map[string]any{
				"public": map[string]any{
					channelID: []any{map[string]any{"ts": shareTS, "thread_ts": threadTS}},
				},
			},
		}},
	}})
	client, err := NewClient("xoxb-secret-1234", WithBaseURL(server.URL()))
	if err != nil {
		t.Fatal(err)
	}

	result, err := client.UploadFileToThread(t.Context(), UploadFileInput{
		Filename:  "phase-plan.md",
		Title:     "Review: Phase 3 plan",
		Data:      payload,
		ChannelID: channelID,
		ThreadTS:  threadTS,
	})
	if err != nil {
		t.Fatalf("UploadFileToThread() error = %v", err)
	}
	if result.FileID != "F00000001" || result.ShareTS != shareTS {
		t.Fatalf("UploadFileToThread() = %#v; want file and share timestamps", result)
	}

	requests := server.AllRequests()
	if len(requests) != 3 {
		t.Fatalf("AllRequests() length = %d; want 3: %#v", len(requests), requests)
	}
	if requests[0].Path != "/api/files.getUploadURLExternal" ||
		requests[0].Fields["filename"] != "phase-plan.md" ||
		requests[0].Fields["length"] != fmt.Sprint(len(payload)) {
		t.Fatalf("upload URL request = %#v; want filename and exact byte length", requests[0])
	}
	digest := sha256.Sum256(payload)
	if requests[1].Method != http.MethodPost || requests[1].Path != "/upload/F00000001" ||
		requests[1].BearerPresent || requests[1].ContentType != "application/octet-stream" ||
		requests[1].ContentLength != int64(len(payload)) {
		t.Fatalf("raw upload request = %#v; want bearer-less octet stream", requests[1])
	}
	if requests[1].Digest != fmt.Sprintf("%x", digest) {
		t.Fatalf("raw upload request = %#v; want bearer-less payload digest %x", requests[1], digest)
	}
	if requests[2].Path != "/api/files.completeUploadExternal" ||
		requests[2].Fields["channel_id"] != channelID ||
		requests[2].Fields["thread_ts"] != threadTS {
		t.Fatalf("completion request = %#v; want channel and thread", requests[2])
	}
	var files []map[string]string
	if err := json.Unmarshal([]byte(requests[2].Fields["files"].(string)), &files); err != nil {
		t.Fatalf("completion files are not JSON: %v", err)
	}
	if len(files) != 1 || files[0]["id"] != "F00000001" ||
		files[0]["title"] != "Review: Phase 3 plan" {
		t.Fatalf("completion files = %#v; want issued file ID and title", files)
	}
}

func TestClientUploadFileToThreadReturnsEmptyShareTimestampWhenAbsent(t *testing.T) {
	server := testsupport.New(t)
	client, err := NewClient("xoxb-secret-1234", WithBaseURL(server.URL()))
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.UploadFileToThread(t.Context(), UploadFileInput{
		Filename: "roadmap.md", Title: "Roadmap", Data: []byte("roadmap"),
		ChannelID: "D12345678", ThreadTS: "1.0",
	})
	if err != nil {
		t.Fatalf("UploadFileToThread() error = %v", err)
	}
	if result.FileID != "F00000001" || result.ShareTS != "" {
		t.Fatalf("UploadFileToThread() = %#v; want issued ID and no share timestamp", result)
	}
}

func TestClientUploadFileToThreadScrubsFilenameAndTitle(t *testing.T) {
	const (
		token       = "xoxb-distinctive-secret-1234"
		secondToken = "xoxp-another-secret-5678"
	)
	server := testsupport.New(t)
	client, err := NewClient(token, WithBaseURL(server.URL()))
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.UploadFileToThread(t.Context(), UploadFileInput{
		Filename:  "plan-" + token + "-" + secondToken + ".md",
		Title:     "Review " + token + " " + secondToken,
		Data:      []byte("safe"),
		ChannelID: "C12345678",
		ThreadTS:  "1.0",
	})
	if err != nil {
		t.Fatalf("UploadFileToThread() error = %v", err)
	}
	requestText := ""
	for _, request := range server.AllRequests() {
		encoded, marshalErr := json.Marshal(request.Fields)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		requestText += string(encoded)
	}
	if strings.Contains(requestText, token) || strings.Contains(requestText, secondToken) {
		t.Fatalf("upload request fields leaked credentials: %s", requestText)
	}
	if strings.Count(requestText, "[REDACTED]") < 2 {
		t.Fatalf("upload request fields = %s; want redaction markers", requestText)
	}
}

func TestClientUploadFileToThreadEnvelopeErrorsStopTheSequence(t *testing.T) {
	const token = "xoxb-distinctive-secret-1234"
	for _, tc := range []struct {
		name         string
		method       string
		wantRequests int
	}{
		{name: "upload URL", method: "files.getUploadURLExternal", wantRequests: 1},
		{name: "completion", method: "files.completeUploadExternal", wantRequests: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := testsupport.New(t)
			server.Script(tc.method, testsupport.Response{Body: map[string]any{
				"ok": false, "error": "invalid_auth: " + token,
			}})
			client, err := NewClient(token, WithBaseURL(server.URL()))
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.UploadFileToThread(t.Context(), UploadFileInput{
				Filename: "plan.md", Title: "Plan", Data: []byte("plan"),
				ChannelID: "C12345678", ThreadTS: "1.0",
			})
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("UploadFileToThread() error = %#v; want APIError", err)
			}
			if apiErr.SlackError != "invalid_auth: [REDACTED]" || strings.Contains(err.Error(), token) {
				t.Fatalf("UploadFileToThread() error = %#v; want scrubbed Slack error", err)
			}
			if got := len(server.AllRequests()); got != tc.wantRequests {
				t.Fatalf("AllRequests() length = %d; want %d", got, tc.wantRequests)
			}
		})
	}
}

func TestClientUploadFileToThreadRawTransportErrors(t *testing.T) {
	for _, tc := range []struct {
		name       string
		response   testsupport.Response
		wantStatus int
		wantRetry  time.Duration
	}{
		{
			name:       "server error",
			response:   testsupport.Response{Status: http.StatusInternalServerError},
			wantStatus: http.StatusInternalServerError,
		},
		{
			name: "rate limit",
			response: testsupport.Response{
				Status: http.StatusTooManyRequests,
				Headers: http.Header{
					"Retry-After": []string{"9"},
				},
			},
			wantStatus: http.StatusTooManyRequests,
			wantRetry:  9 * time.Second,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := testsupport.New(t)
			server.Script("upload", tc.response)
			client, err := NewClient("xoxb-secret-1234", WithBaseURL(server.URL()))
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.UploadFileToThread(t.Context(), UploadFileInput{
				Filename: "plan.md", Title: "Plan", Data: []byte("plan"),
				ChannelID: "C12345678", ThreadTS: "1.0",
			})
			var transportErr *TransportError
			if !errors.As(err, &transportErr) {
				t.Fatalf("UploadFileToThread() error = %#v; want TransportError", err)
			}
			if transportErr.StatusCode != tc.wantStatus || transportErr.RetryAfter != tc.wantRetry {
				t.Fatalf("TransportError = %#v; want status %d retry %s",
					transportErr, tc.wantStatus, tc.wantRetry)
			}
			if got := len(server.AllRequests()); got != 2 {
				t.Fatalf("AllRequests() length = %d; want upload URL and byte upload", got)
			}
		})
	}
}

func TestClientUploadFileToThreadRawUploadHasIndependentLongerTimeout(t *testing.T) {
	server := testsupport.New(t)
	server.Script("upload", testsupport.Response{
		Delay: 60 * time.Millisecond,
		Body:  map[string]any{"ok": true},
	})
	client, err := NewClient(
		"xoxb-secret-1234",
		WithBaseURL(server.URL()),
		WithTimeout(20*time.Millisecond),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.UploadFileToThread(t.Context(), UploadFileInput{
		Filename: "plan.md", Title: "Plan", Data: []byte("plan"),
		ChannelID: "C12345678", ThreadTS: "1.0",
	}); err != nil {
		t.Fatalf("UploadFileToThread() with delayed raw upload error = %v", err)
	}

	server = testsupport.New(t)
	server.Script("upload", testsupport.Response{
		Delay: 500 * time.Millisecond,
		Body:  map[string]any{"ok": true},
	})
	client, err = NewClient(
		"xoxb-secret-1234",
		WithBaseURL(server.URL()),
		WithUploadTimeout(40*time.Millisecond),
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.UploadFileToThread(t.Context(), UploadFileInput{
		Filename: "plan.md", Title: "Plan", Data: []byte("plan"),
		ChannelID: "C12345678", ThreadTS: "1.0",
	})
	var transportErr *TransportError
	if !errors.As(err, &transportErr) || !strings.Contains(transportErr.Detail, "deadline exceeded") {
		t.Fatalf("UploadFileToThread() timeout error = %#v; want deadline TransportError", err)
	}
}
