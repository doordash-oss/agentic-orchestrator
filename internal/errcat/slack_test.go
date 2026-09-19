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

package errcat

import (
	"strings"
	"testing"
)

func TestSlackCodesContract(t *testing.T) {
	cases := []struct {
		code  Code
		class Class
	}{
		{SlackInvalidToken, ClassBlocking},
		{SlackUnsupportedToken, ClassBlocking},
		{SlackMissingScopes, ClassBlocking},
		{SlackUnreachable, ClassWarning},
		{SlackUserNotFound, ClassWarning},
		{SlackChannelNotFound, ClassWarning},
		{SlackNotInChannel, ClassWarning},
		{SlackChannelArchived, ClassWarning},
		{SlackAmbiguousHandle, ClassWarning},
		{SlackScanCapReached, ClassWarning},
		{SlackUnrecognizedRecipient, ClassWarning},
		{SlackDeliveryFailed, ClassWarning},
	}
	for _, tc := range cases {
		entry, ok := Lookup(tc.code)
		if !ok {
			t.Fatalf("Lookup(%q) missing Slack catalog entry", tc.code)
		}
		if entry.Class != tc.class {
			t.Errorf("Lookup(%q).Class = %q; want %q", tc.code, entry.Class, tc.class)
		}
		if entry.Title == "" || entry.Summary == "" || entry.Remediation == "" {
			t.Errorf("Lookup(%q) = %#v; want complete authored text", tc.code, entry)
		}
		if len(entry.Actions) != 0 || len(entry.Blocks) != 0 {
			t.Errorf("Lookup(%q) actions/blocks = %#v/%#v; want none", tc.code, entry.Actions, entry.Blocks)
		}
	}
}

func TestSlackRecipientErrorsRenderSpecificGuidance(t *testing.T) {
	ambiguous := New(SlackAmbiguousHandle, WithParams(SlackAmbiguousHandleParams{
		Handle:     "@alex",
		MatchCount: 2,
	}))
	if !strings.Contains(ambiguous.Summary, "2") ||
		!strings.Contains(ambiguous.Summary, "@alex") ||
		!strings.Contains(ambiguous.Remediation.Hint, "email") ||
		!strings.Contains(ambiguous.Remediation.Hint, "member ID") {
		t.Fatalf("ambiguous handle error = %#v; want count, handle, and disambiguation guidance", ambiguous)
	}

	scanCap := New(SlackScanCapReached, WithParams(SlackScanCapReachedParams{
		Name:    "@alex",
		Kind:    "members",
		Scanned: 4000,
	}))
	if !strings.Contains(scanCap.Summary, "@alex") ||
		!strings.Contains(scanCap.Summary, "4,000") ||
		!strings.Contains(scanCap.Summary, "members") ||
		!strings.Contains(scanCap.Remediation.Hint, "email") ||
		!strings.Contains(scanCap.Remediation.Hint, "Slack ID") {
		t.Fatalf("scan cap error = %#v; want scanned count and alternate lookup guidance", scanCap)
	}

	notInChannel := New(
		SlackNotInChannel,
		WithParams(SlackRecipientParams{Recipient: "#eng"}),
		WithRemediationHint("Invite the Agentico app to #eng in Slack, then try again."),
	)
	if !strings.Contains(notInChannel.Summary, "#eng") ||
		!strings.Contains(notInChannel.Remediation.Hint, "Invite the Agentico app to #eng in Slack") {
		t.Fatalf("not-in-channel error = %#v; want channel-specific invite guidance", notInChannel)
	}
}

func TestSlackMissingScopesSummaryIsStable(t *testing.T) {
	rendered := New(SlackMissingScopes, WithParams(SlackMissingScopesParams{
		Scopes: []string{"users:read", "chat:write", "users:read", " channels:read "},
	}))
	want := "The Slack token is missing required scopes: channels:read, chat:write, users:read."
	if rendered.Summary != want {
		t.Fatalf("SlackMissingScopes summary = %q; want %q", rendered.Summary, want)
	}
	if !strings.Contains(rendered.Remediation.Hint, "Reinstall") {
		t.Fatalf("SlackMissingScopes remediation = %q; want reinstall guidance", rendered.Remediation.Hint)
	}
}
