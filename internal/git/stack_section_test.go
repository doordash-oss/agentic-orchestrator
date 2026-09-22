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

package git

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

func TestBuildStackSection(t *testing.T) {
	threeLayers := []StackSectionLayer{
		{Position: 1, Title: "Foundation", PRURL: "https://github.com/org/repo/pull/11", PRState: PRStateMerged},
		{Position: 2, Title: "Middleware"},
		{Position: 3, Title: "API", PRURL: "https://github.com/org/repo/pull/33", PRState: PRStateOpen, Current: true},
	}
	threeLayersWant := StackSectionHeader + "\n\n" +
		"1. Foundation - [#11](https://github.com/org/repo/pull/11) (" + PRStateMerged + ")\n" +
		"2. Middleware - " + stackNoChangesText + "\n" +
		"3. API - [#33](https://github.com/org/repo/pull/33) (" + PRStateOpen + ") " + stackCurrentMarker

	tests := []struct {
		name      string
		layers    []StackSectionLayer
		want      string
		wantEmpty bool
	}{
		{
			name:   "three layers with PRs for layers 1 and 3, layer 3 current",
			layers: threeLayers,
			want:   threeLayersWant,
		},
		{
			name: "layers render in position order regardless of input order",
			layers: []StackSectionLayer{
				threeLayers[2],
				threeLayers[0],
				threeLayers[1],
			},
			want: threeLayersWant,
		},
		{
			name: "repository with a single PR renders nothing",
			layers: []StackSectionLayer{
				{Position: 1, Title: "Foundation", PRURL: "https://github.com/org/repo/pull/11", PRState: PRStateOpen},
				{Position: 2, Title: "Middleware"},
				{Position: 3, Title: "API"},
			},
			wantEmpty: true,
		},
		{
			name: "single-layer stack renders nothing",
			layers: []StackSectionLayer{
				{Position: 1, Title: "Only layer", PRURL: "https://github.com/org/repo/pull/5", PRState: PRStateOpen, Current: true},
			},
			wantEmpty: true,
		},
		{
			name: "repository with no PRs renders nothing",
			layers: []StackSectionLayer{
				{Position: 1, Title: "Foundation"},
				{Position: 2, Title: "Middleware"},
			},
			wantEmpty: true,
		},
		{
			name: "layer without a state renders the link without a state",
			layers: []StackSectionLayer{
				{Position: 1, Title: "Foundation", PRURL: "https://github.com/org/repo/pull/11", PRState: PRStateOpen},
				{Position: 2, Title: "API", PRURL: "https://github.com/org/repo/pull/22", Current: true},
			},
			want: StackSectionHeader + "\n\n" +
				"1. Foundation - [#11](https://github.com/org/repo/pull/11) (" + PRStateOpen + ")\n" +
				"2. API - [#22](https://github.com/org/repo/pull/22) " + stackCurrentMarker,
		},
		{
			name:      "empty layer list renders nothing",
			layers:    nil,
			wantEmpty: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildStackSection(tt.layers)
			if tt.wantEmpty {
				if got != "" {
					t.Errorf("expected empty string, got:\n%s", got)
				}
				return
			}
			if got != tt.want {
				t.Errorf("BuildStackSection():\n  got:  %q\n  want: %q", got, tt.want)
			}
		})
	}
}

func TestInjectStackSection(t *testing.T) {
	section := StackSectionHeader + "\n\n" +
		"1. Foundation - [#11](https://github.com/org/repo/pull/11) (merged)\n" +
		"2. API - [#33](https://github.com/org/repo/pull/33) (open) (this pull request)"
	related := CrossRefSectionHeader + "\n\n| Repository | Branch | PR |"

	tests := []struct {
		name    string
		body    string
		section string
		check   func(t *testing.T, result string)
	}{
		{
			name:    "empty body",
			body:    "",
			section: section,
			check: func(t *testing.T, result string) {
				t.Helper()
				if result != section {
					t.Errorf("expected section only, got: %q", result)
				}
			},
		},
		{
			name:    "replaces only the stack section and keeps stack, related PRs, signature ordering",
			body:    "intro\n\n" + StackSectionHeader + "\n\nold stack lines\n\n" + related + PRSignature,
			section: section,
			check: func(t *testing.T, result string) {
				t.Helper()
				expected := "intro\n\n" + section + "\n\n" + related + PRSignature
				if result != expected {
					t.Errorf("InjectStackSection():\n  got:  %q\n  want: %q", result, expected)
				}
			},
		},
		{
			name:    "idempotent",
			body:    "intro\n\n" + StackSectionHeader + "\n\nold stack lines\n\n" + related + PRSignature,
			section: section,
			check: func(t *testing.T, result string) {
				t.Helper()
				second := InjectStackSection(result, section)
				if result != second {
					t.Errorf("InjectStackSection is not idempotent:\nfirst:  %q\nsecond: %q", result, second)
				}
			},
		},
		{
			name:    "no-op when the body already carries the exact section",
			body:    "intro\n\n" + section + "\n\n" + related + PRSignature,
			section: section,
			check: func(t *testing.T, result string) {
				t.Helper()
				expected := "intro\n\n" + section + "\n\n" + related + PRSignature
				if result != expected {
					t.Errorf("expected body unchanged:\n  got:  %q\n  want: %q", result, expected)
				}
			},
		},
		{
			name:    "fresh insert lands above the related-PRs section",
			body:    "intro\n\n" + related + PRSignature,
			section: section,
			check: func(t *testing.T, result string) {
				t.Helper()
				expected := "intro\n\n" + section + "\n\n" + related + PRSignature
				if result != expected {
					t.Errorf("InjectStackSection():\n  got:  %q\n  want: %q", result, expected)
				}
			},
		},
		{
			name:    "insert above the signature when no related-PRs section exists",
			body:    "content" + PRSignature,
			section: section,
			check: func(t *testing.T, result string) {
				t.Helper()
				expected := "content\n\n" + section + PRSignature
				if result != expected {
					t.Errorf("InjectStackSection():\n  got:  %q\n  want: %q", result, expected)
				}
			},
		},
		{
			name:    "append when no related-PRs section or signature exists",
			body:    "just content",
			section: section,
			check: func(t *testing.T, result string) {
				t.Helper()
				expected := "just content\n\n" + section
				if result != expected {
					t.Errorf("InjectStackSection():\n  got:  %q\n  want: %q", result, expected)
				}
			},
		},
		{
			name:    "empty section leaves the body unchanged",
			body:    "content\n\n" + StackSectionHeader + "\n\nold lines" + PRSignature,
			section: "",
			check: func(t *testing.T, result string) {
				t.Helper()
				expected := "content\n\n" + StackSectionHeader + "\n\nold lines" + PRSignature
				if result != expected {
					t.Errorf("expected body unchanged:\n  got:  %q\n  want: %q", result, expected)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := InjectStackSection(tt.body, tt.section)
			tt.check(t, got)
		})
	}
}

func TestExtractStackSection(t *testing.T) {
	section := StackSectionHeader + "\n\n1. Foundation - [#11](https://github.com/org/repo/pull/11) (merged)"

	tests := []struct {
		name     string
		body     string
		expected string
	}{
		{
			name:     "no section",
			body:     "just content",
			expected: "",
		},
		{
			name:     "section at end",
			body:     "content\n\n" + section,
			expected: section,
		},
		{
			name:     "section before related-PRs heading",
			body:     section + "\n\n" + CrossRefSectionHeader + "\n\ntable",
			expected: section,
		},
		{
			name:     "section before signature",
			body:     section + PRSignature,
			expected: section,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractStackSection(tt.body)
			if got != tt.expected {
				t.Errorf("ExtractStackSection():\n  got:  %q\n  want: %q", got, tt.expected)
			}
		})
	}
}

func TestRemoveStackSection(t *testing.T) {
	section := StackSectionHeader + "\n\n1. Foundation - [#11](https://github.com/org/repo/pull/11) (merged)"

	tests := []struct {
		name     string
		body     string
		expected string
	}{
		{
			name:     "no section",
			body:     "just content",
			expected: "just content",
		},
		{
			name:     "section at end",
			body:     "content\n\n" + section,
			expected: "content\n\n",
		},
		{
			name:     "section before related-PRs heading",
			body:     "content\n\n" + section + "\n\n" + CrossRefSectionHeader + "\n\ntable",
			expected: "content\n\n" + CrossRefSectionHeader + "\n\ntable",
		},
		{
			name:     "section before signature",
			body:     "content\n\n" + section + PRSignature,
			expected: "content" + PRSignature,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RemoveStackSection(tt.body)
			if got != tt.expected {
				t.Errorf("RemoveStackSection():\n  got:  %q\n  want: %q", got, tt.expected)
			}
		})
	}
}

func TestUpdatePRBodiesWithStackSection(t *testing.T) {
	fake := testutil.InstallFakeGitHubAPI(t)
	body := "original body" + PRSignature
	var patches int
	fake.Mux.HandleFunc("/repos/acme/widgets/pulls/7", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]string{"body": body})
		case http.MethodPatch:
			b, _ := io.ReadAll(r.Body)
			var payload struct {
				Body string `json:"body"`
			}
			_ = json.Unmarshal(b, &payload)
			body = payload.Body
			patches++
			fmt.Fprint(w, `{}`)
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	})

	section := BuildStackSection([]StackSectionLayer{
		{Position: 1, Title: "Foundation", PRURL: "https://github.com/acme/widgets/pull/7", PRState: PRStateOpen},
		{Position: 2, Title: "API", PRURL: "https://github.com/acme/widgets/pull/8", PRState: PRStateOpen, Current: true},
	})

	errs := UpdatePRBodiesWithStackSection([]string{"https://github.com/acme/widgets/pull/7"}, section)
	if len(errs) != 0 {
		t.Fatalf("UpdatePRBodiesWithStackSection() errors = %v", errs)
	}
	if patches != 1 {
		t.Errorf("expected exactly one PATCH, got %d", patches)
	}
	if !strings.Contains(body, StackSectionHeader) {
		t.Errorf("expected patched body to contain the stack section, got:\n%s", body)
	}
	if !strings.Contains(body, "original body") {
		t.Errorf("expected patched body to keep the original content, got:\n%s", body)
	}
	stackIdx := strings.Index(body, StackSectionHeader)
	sigIdx := strings.Index(body, PRSignature)
	if stackIdx < 0 || sigIdx < 0 || stackIdx >= sigIdx {
		t.Errorf("expected stack section above the signature: stack at %d, signature at %d", stackIdx, sigIdx)
	}

	// Re-running over the already-injected body writes nothing back.
	errs = UpdatePRBodiesWithStackSection([]string{"https://github.com/acme/widgets/pull/7"}, section)
	if len(errs) != 0 {
		t.Fatalf("second UpdatePRBodiesWithStackSection() errors = %v", errs)
	}
	if patches != 1 {
		t.Errorf("expected no additional PATCH on re-run, got %d", patches)
	}
}
