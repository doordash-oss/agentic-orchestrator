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
	"strings"
	"testing"
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
				crossRefSectionHeader,
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
	section := crossRefSectionHeader + "\n\nsection content"

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
				sectionIdx := strings.Index(result, crossRefSectionHeader)
				if sigIdx < 0 {
					t.Fatal("PRSignature not found in result")
				}
				if sectionIdx < 0 {
					t.Fatal("cross-reference header not found in result")
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
			body:    "content\n\n" + crossRefSectionHeader + "\n\nold table\n",
			section: crossRefSectionHeader + "\n\nnew table",
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
			body:     "content\n\n" + crossRefSectionHeader + "\n\n|table|",
			expected: "content\n\n",
		},
		{
			name:     "section before signature",
			body:     "content\n\n" + crossRefSectionHeader + "\n\n|table|" + PRSignature,
			expected: "content" + PRSignature,
		},
		{
			name:     "section between sections",
			body:     "## Summary\n\ncontent\n\n" + crossRefSectionHeader + "\n\n|table|\n\n## Test Plan",
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
			body:     "content\n\n" + crossRefSectionHeader + "\n\nsome table",
			expected: crossRefSectionHeader + "\n\nsome table",
		},
		{
			name:     "section before other heading",
			body:     crossRefSectionHeader + "\n\ntable\n\n## Other",
			expected: crossRefSectionHeader + "\n\ntable",
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

func TestUpdatePRBody_Error(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	err := UpdatePRBody("https://github.com/invalid/nonexistent/pull/99999", "body")
	if err == nil {
		t.Fatal("expected error from UpdatePRBody with invalid repo")
	}
	if !strings.Contains(err.Error(), "editing PR") {
		t.Errorf("expected error containing 'editing PR', got: %v", err)
	}
}

func TestGetPRBody_Error(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

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
