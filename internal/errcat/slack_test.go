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
