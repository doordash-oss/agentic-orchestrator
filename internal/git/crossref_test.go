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

func TestBuildCrossReferenceSection(t *testing.T) {
	tests := []struct {
		name        string
		featureName string
		entries     []CrossRefEntry
		wantEmpty   bool
		contains    []string
		notContains []string
	}{
		{
			name:        "two repos both with PRs",
			featureName: "my feature",
			entries: []CrossRefEntry{
				{RepoName: "repo-a", Branch: "branch-a", PRURL: "https://github.com/org/repo-a/pull/42"},
				{RepoName: "repo-b", Branch: "branch-b", PRURL: "https://github.com/org/repo-b/pull/43"},
			},
			contains: []string{
				CrossRefSectionHeader,
				"repo-a",
				"repo-b",
				"[#42]",
				"[#43]",
				"multi-repo feature",
			},
		},
		{
			name:        "two repos one pending",
			featureName: "my feature",
			entries: []CrossRefEntry{
				{RepoName: "repo-a", Branch: "branch-a", PRURL: "https://github.com/org/repo-a/pull/42"},
				{RepoName: "repo-b", Branch: "branch-b", PRURL: ""},
			},
			contains: []string{
				"_(pending)_",
				"[#42]",
			},
		},
		{
			name:        "two repos one failed",
			featureName: "my feature",
			entries: []CrossRefEntry{
				{RepoName: "repo-a", Branch: "branch-a", PRURL: "https://github.com/org/repo-a/pull/42"},
				{RepoName: "repo-b", Branch: "branch-b", PRURL: "(failed)"},
			},
			contains: []string{
				"_(failed)_",
			},
		},
		{
			name:        "single repo",
			featureName: "my feature",
			entries: []CrossRefEntry{
				{RepoName: "repo-a", Branch: "branch-a", PRURL: "https://github.com/org/repo-a/pull/42"},
			},
			wantEmpty: true,
		},
		{
			name:        "empty entries",
			featureName: "my feature",
			entries:     []CrossRefEntry{},
			wantEmpty:   true,
		},
		{
			name:        "three repos mixed",
			featureName: "my feature",
			entries: []CrossRefEntry{
				{RepoName: "a", Branch: "b-a", PRURL: "https://github.com/org/a/pull/1"},
				{RepoName: "b", Branch: "b-b", PRURL: ""},
				{RepoName: "c", Branch: "b-c", PRURL: "https://github.com/org/c/pull/3"},
			},
			contains: []string{
				"a", "b", "c",
				"_(pending)_",
			},
		},
		{
			name:        "PR number extraction from URL",
			featureName: "my feature",
			entries: []CrossRefEntry{
				{RepoName: "repo-a", Branch: "branch", PRURL: "https://github.com/org/repo/pull/42"},
				{RepoName: "repo-b", Branch: "branch", PRURL: ""},
			},
			contains: []string{
				"[#42](https://github.com/org/repo/pull/42)",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildCrossReferenceSection(tt.featureName, tt.entries)

			if tt.wantEmpty {
				if got != "" {
					t.Errorf("expected empty string, got: %q", got)
				}
				return
			}

			for _, s := range tt.contains {
				if !strings.Contains(got, s) {
					t.Errorf("expected result to contain %q, got:\n%s", s, got)
				}
			}
			for _, s := range tt.notContains {
				if strings.Contains(got, s) {
					t.Errorf("expected result NOT to contain %q, got:\n%s", s, got)
				}
			}
		})
	}
}

func TestInjectCrossReferenceSection(t *testing.T) {
	section := CrossRefSectionHeader + "\n\nsection content"

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
			name:    "body with signature",
			body:    "content" + PRSignature,
			section: section,
			check: func(t *testing.T, result string) {
				t.Helper()
				sigIdx := strings.Index(result, PRSignature)
				sectionIdx := strings.Index(result, CrossRefSectionHeader)
				if sigIdx < 0 {
					t.Fatal("PRSignature not found in result")
				}
				if sectionIdx < 0 {
					t.Fatal("CrossRefSectionHeader not found in result")
				}
				if sectionIdx >= sigIdx {
					t.Errorf("section should appear before PRSignature: section at %d, signature at %d", sectionIdx, sigIdx)
				}
			},
		},
		{
			name:    "body without signature",
			body:    "just content",
			section: section,
			check: func(t *testing.T, result string) {
				t.Helper()
				expected := "just content\n\n" + section
				if result != expected {
					t.Errorf("expected %q, got: %q", expected, result)
				}
			},
		},
		{
			name:    "body already has cross-ref",
			body:    "content\n\n" + CrossRefSectionHeader + "\n\nold table\n",
			section: CrossRefSectionHeader + "\n\nnew table",
			check: func(t *testing.T, result string) {
				t.Helper()
				if strings.Contains(result, "old table") {
					t.Error("old section content should be replaced")
				}
				if !strings.Contains(result, "new table") {
					t.Error("new section content should be present")
				}
			},
		},
		{
			name:    "idempotent",
			body:    "content" + PRSignature,
			section: section,
			check: func(t *testing.T, result string) {
				t.Helper()
				second := InjectCrossReferenceSection(result, section)
				if result != second {
					t.Errorf("InjectCrossReferenceSection is not idempotent:\nfirst:  %q\nsecond: %q", result, second)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := InjectCrossReferenceSection(tt.body, tt.section)
			tt.check(t, got)
		})
	}
}

func TestRemoveCrossReferenceSection(t *testing.T) {
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
			body:     "content\n\n" + CrossRefSectionHeader + "\n\n|table|",
			expected: "content\n\n",
		},
		{
			name:     "section before signature",
			body:     "content\n\n" + CrossRefSectionHeader + "\n\n|table|" + PRSignature,
			expected: "content" + PRSignature,
		},
		{
			name:     "section between sections",
			body:     "## Summary\n\ncontent\n\n" + CrossRefSectionHeader + "\n\n|table|\n\n## Test Plan",
			expected: "## Summary\n\ncontent\n\n## Test Plan",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RemoveCrossReferenceSection(tt.body)
			if got != tt.expected {
				t.Errorf("RemoveCrossReferenceSection():\n  got:  %q\n  want: %q", got, tt.expected)
			}
		})
	}
}

func TestExtractCrossReferenceSection(t *testing.T) {
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
			body:     "content\n\n" + CrossRefSectionHeader + "\n\nsome table",
			expected: CrossRefSectionHeader + "\n\nsome table",
		},
		{
			name:     "section before other heading",
			body:     CrossRefSectionHeader + "\n\ntable\n\n## Other",
			expected: CrossRefSectionHeader + "\n\ntable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractCrossReferenceSection(tt.body)
			if got != tt.expected {
				t.Errorf("ExtractCrossReferenceSection():\n  got:  %q\n  want: %q", got, tt.expected)
			}
		})
	}
}

func TestGetPRBodyAndUpdatePRBodyUseAPI(t *testing.T) {
	fake := testutil.InstallFakeGitHubAPI(t)
	var patchedBody string
	fake.Mux.HandleFunc("/repos/acme/widgets/pulls/7", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			fmt.Fprint(w, `{"body":"original body"}`)
		case http.MethodPatch:
			body, _ := io.ReadAll(r.Body)
			var payload struct {
				Body string `json:"body"`
			}
			_ = json.Unmarshal(body, &payload)
			patchedBody = payload.Body
			fmt.Fprint(w, `{}`)
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	})

	got, err := GetPRBody("https://github.com/acme/widgets/pull/7")
	if err != nil {
		t.Fatalf("GetPRBody() error = %v", err)
	}
	if got != "original body" {
		t.Errorf("GetPRBody() = %q, want %q", got, "original body")
	}

	if err := UpdatePRBody("https://github.com/acme/widgets/pull/7", "new body"); err != nil {
		t.Fatalf("UpdatePRBody() error = %v", err)
	}
	if patchedBody != "new body" {
		t.Errorf("PATCH body field = %q, want %q", patchedBody, "new body")
	}
}

func TestUpdatePRBody_Error(t *testing.T) {
	fake := testutil.InstallFakeGitHubAPI(t)
	fake.HandleJSON("/repos/invalid/nonexistent/pulls/99999", 404, `{"message":"Not Found"}`)

	err := UpdatePRBody("https://github.com/invalid/nonexistent/pull/99999", "body")
	if err == nil {
		t.Fatal("expected error from UpdatePRBody with invalid repo")
	}
	if !strings.Contains(err.Error(), "editing PR") {
		t.Errorf("expected error containing 'editing PR', got: %v", err)
	}
}

func TestGetPRBody_Error(t *testing.T) {
	fake := testutil.InstallFakeGitHubAPI(t)
	fake.HandleJSON("/repos/invalid/nonexistent/pulls/99999", 404, `{"message":"Not Found"}`)

	_, err := GetPRBody("https://github.com/invalid/nonexistent/pull/99999")
	if err == nil {
		t.Fatal("expected error from GetPRBody with invalid repo")
	}
	if !strings.Contains(err.Error(), "fetching PR body") {
		t.Errorf("expected error containing 'fetching PR body', got: %v", err)
	}
}

func TestRetroactivelyUpdateCrossRefs_AllPending(t *testing.T) {
	entries := []CrossRefEntry{
		{RepoName: "repo-a", Branch: "branch-a", PRURL: ""},
		{RepoName: "repo-b", Branch: "branch-b", PRURL: ""},
	}

	errs := RetroactivelyUpdateCrossRefs("my feature", entries, "repo-a")
	if len(errs) != 0 {
		t.Errorf("expected no errors for all-pending entries, got: %v", errs)
	}
}

func TestRetroactivelyUpdateCrossRefs_SkipCurrentRepo(t *testing.T) {
	entries := []CrossRefEntry{
		{RepoName: "repo-a", Branch: "branch-a", PRURL: "https://github.com/org/repo-a/pull/42"},
		{RepoName: "repo-b", Branch: "branch-b", PRURL: ""},
	}

	errs := RetroactivelyUpdateCrossRefs("my feature", entries, "repo-a")
	if len(errs) != 0 {
		t.Errorf("expected no errors when only current repo has URL, got: %v", errs)
	}
}

func TestBuildLayerCrossReferenceSection(t *testing.T) {
	tests := []struct {
		name        string
		featureName string
		entries     []CrossRefEntry
		currentRepo string
		wantEmpty   bool
		contains    []string
		notContains []string
	}{
		{
			name:        "layer 2 of a two-repo feature links only the other repository's PR",
			featureName: "my feature",
			entries: []CrossRefEntry{
				{RepoName: "repo-a", Branch: "agentico/my-feature", PRURL: "https://github.com/org/repo-a/pull/21"},
				{RepoName: "repo-b", Branch: "agentico/my-feature", PRURL: "https://github.com/org/repo-b/pull/22"},
			},
			currentRepo: "repo-a",
			contains: []string{
				CrossRefSectionHeader,
				"repo-b",
				"[#22](https://github.com/org/repo-b/pull/22)",
				"multi-repo feature",
			},
			notContains: []string{
				"repo-a",
				"[#21]",
			},
		},
		{
			name:        "a repository whose layer has no PR is omitted",
			featureName: "my feature",
			entries: []CrossRefEntry{
				{RepoName: "repo-a", Branch: "agentico/my-feature", PRURL: "https://github.com/org/repo-a/pull/21"},
				{RepoName: "repo-b", Branch: "agentico/my-feature", PRURL: ""},
			},
			currentRepo: "repo-a",
			wantEmpty:   true,
		},
		{
			name:        "pending and failed siblings are omitted, current repo excluded",
			featureName: "my feature",
			entries: []CrossRefEntry{
				{RepoName: "repo-a", Branch: "agentico/my-feature", PRURL: "https://github.com/org/repo-a/pull/21"},
				{RepoName: "repo-b", Branch: "agentico/my-feature", PRURL: ""},
				{RepoName: "repo-c", Branch: "agentico/my-feature", PRURL: "(failed)"},
				{RepoName: "repo-d", Branch: "agentico/my-feature", PRURL: "https://github.com/org/repo-d/pull/24"},
			},
			currentRepo: "repo-a",
			contains: []string{
				"repo-d",
				"[#24](https://github.com/org/repo-d/pull/24)",
			},
			notContains: []string{
				"repo-a",
				"repo-b",
				"repo-c",
				"_(pending)_",
				"_(failed)_",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildLayerCrossReferenceSection(tt.featureName, tt.entries, tt.currentRepo)

			if tt.wantEmpty {
				if got != "" {
					t.Errorf("expected empty string, got: %q", got)
				}
				return
			}

			for _, s := range tt.contains {
				if !strings.Contains(got, s) {
					t.Errorf("expected result to contain %q, got:\n%s", s, got)
				}
			}
			for _, s := range tt.notContains {
				if strings.Contains(got, s) {
					t.Errorf("expected result NOT to contain %q, got:\n%s", s, got)
				}
			}
		})
	}
}

func TestUpdatePRBodiesWithSection(t *testing.T) {
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

	section := CrossRefSectionHeader + "\n\n| Repository | Branch | PR |\n|------------|--------|----|\n| repo-b | branch-b | [#1](https://github.com/org/repo-b/pull/1) |"

	errs := UpdatePRBodiesWithSection([]string{
		"https://github.com/acme/widgets/pull/7",
		"",
		"(failed)",
	}, section)
	if len(errs) != 0 {
		t.Fatalf("UpdatePRBodiesWithSection() errors = %v", errs)
	}
	if patches != 1 {
		t.Errorf("expected exactly one PATCH, got %d", patches)
	}
	if !strings.Contains(body, CrossRefSectionHeader) {
		t.Errorf("expected patched body to contain the cross-reference section, got:\n%s", body)
	}

	// Re-running over the already-injected body writes nothing back.
	errs = UpdatePRBodiesWithSection([]string{"https://github.com/acme/widgets/pull/7"}, section)
	if len(errs) != 0 {
		t.Fatalf("second UpdatePRBodiesWithSection() errors = %v", errs)
	}
	if patches != 1 {
		t.Errorf("expected no additional PATCH on re-run, got %d", patches)
	}
}

func TestUpdatePRBodiesWithSection_Error(t *testing.T) {
	fake := testutil.InstallFakeGitHubAPI(t)
	fake.HandleJSON("/repos/invalid/nonexistent/pulls/99999", 404, `{"message":"Not Found"}`)

	errs := UpdatePRBodiesWithSection([]string{"https://github.com/invalid/nonexistent/pull/99999"}, CrossRefSectionHeader+"\n\ntable")
	if len(errs) != 1 {
		t.Fatalf("expected one error, got: %v", errs)
	}
	if !strings.Contains(errs[0].Error(), "https://github.com/invalid/nonexistent/pull/99999") {
		t.Errorf("expected error to name the PR URL, got: %v", errs[0])
	}
}

func TestRetroactivelyUpdateCrossRefs_UpdatesSiblingPRs(t *testing.T) {
	fake := testutil.InstallFakeGitHubAPI(t)
	var patchedBody string
	var patches int
	fake.Mux.HandleFunc("/repos/org/other/pulls/9", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			fmt.Fprint(w, `{"body":"sibling body"}`)
		case http.MethodPatch:
			b, _ := io.ReadAll(r.Body)
			var payload struct {
				Body string `json:"body"`
			}
			_ = json.Unmarshal(b, &payload)
			patchedBody = payload.Body
			patches++
			fmt.Fprint(w, `{}`)
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	})

	entries := []CrossRefEntry{
		{RepoName: "mine", Branch: "agentico/my-feature", PRURL: "https://github.com/org/mine/pull/42"},
		{RepoName: "other", Branch: "agentico/my-feature", PRURL: "https://github.com/org/other/pull/9"},
	}

	errs := RetroactivelyUpdateCrossRefs("my feature", entries, "mine")
	if len(errs) != 0 {
		t.Fatalf("RetroactivelyUpdateCrossRefs() errors = %v", errs)
	}
	if patches != 1 {
		t.Errorf("expected exactly one PATCH on the sibling PR, got %d", patches)
	}
	for _, want := range []string{
		"mine",
		"other",
		"[#42](https://github.com/org/mine/pull/42)",
		"[#9](https://github.com/org/other/pull/9)",
	} {
		if !strings.Contains(patchedBody, want) {
			t.Errorf("expected patched body to contain %q, got:\n%s", want, patchedBody)
		}
	}
}
