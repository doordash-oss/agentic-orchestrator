// Copyright 2026 DoorDash, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
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

// TestChatContextCodesPinShape pins the authored contract of the two
// chat-context codes: their classes, and that neither references an action
// or declares a context block.
func TestChatContextCodesPinShape(t *testing.T) {
	cases := []struct {
		code  Code
		class Class
	}{
		{ChatContextInvalid, ClassBlocking},
		{ChatContextNotFound, ClassWarning},
	}
	for _, tc := range cases {
		entry, ok := Lookup(tc.code)
		if !ok {
			t.Fatalf("%s: missing from catalog", tc.code)
		}
		if entry.Class != tc.class {
			t.Errorf("%s: class is %q; want %q", tc.code, entry.Class, tc.class)
		}
		if len(entry.Actions) != 0 {
			t.Errorf("%s: references actions %#v; want none", tc.code, entry.Actions)
		}
		if len(entry.Blocks) != 0 {
			t.Errorf("%s: declares blocks %#v; want none", tc.code, entry.Blocks)
		}
		if entry.Remediation == "" {
			t.Errorf("%s: needs a remediation hint", tc.code)
		}
	}
}

// TestChatContextSummariesNameScopeAndCode pins that both summaries name
// the referenced scope and code, and degrade to nonempty static text with
// zero-value params.
func TestChatContextSummariesNameScopeAndCode(t *testing.T) {
	params := ChatContextParams{Scope: "setup", Code: "worktree_setup_failed"}

	rendered := New(ChatContextInvalid, WithParams(params))
	want := `The chat context reference (scope "setup", code "worktree_setup_failed") could not be understood.`
	if rendered.Summary != want {
		t.Fatalf("chat_context_invalid summary = %q; want %q", rendered.Summary, want)
	}
	if !strings.Contains(rendered.Remediation.Hint, "Reopen the card") {
		t.Fatalf("chat_context_invalid hint = %q; want the reopen-the-card hint", rendered.Remediation.Hint)
	}

	rendered = New(ChatContextNotFound, WithParams(params))
	want = `The referenced error (scope "setup", code "worktree_setup_failed") is no longer present.`
	if rendered.Summary != want {
		t.Fatalf("chat_context_not_found summary = %q; want %q", rendered.Summary, want)
	}
	if !strings.Contains(rendered.Remediation.Hint, "Refresh the view") {
		t.Fatalf("chat_context_not_found hint = %q; want the refresh hint", rendered.Remediation.Hint)
	}

	for _, code := range []Code{ChatContextInvalid, ChatContextNotFound} {
		if rendered := New(code, WithParams(ChatContextParams{})); rendered.Title == "" || rendered.Summary == "" {
			t.Fatalf("%s: zero-value params render has empty title or summary", code)
		}
	}
}
