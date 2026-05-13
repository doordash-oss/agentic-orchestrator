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

package agent

import (
	"sort"
	"testing"
)

func TestExtractAtMentionRepos_FullPaths(t *testing.T) {
	repoPaths := map[string]string{
		"agentic":      "/Users/ivar/Projects/agentic",
		"taulu":        "/Users/ivar/Projects/taulu",
		"graph-runner": "/Users/ivar/Projects/graph-runner",
	}

	desc := "Fix the bug in @/Users/ivar/Projects/agentic/internal/tui/wizard.go where step counting is wrong"
	got := ExtractAtMentionRepos(desc, repoPaths)

	if len(got) != 1 || got[0] != "agentic" {
		t.Errorf("expected [agentic], got %v", got)
	}
}

func TestExtractAtMentionRepos_RepoNamePrefix(t *testing.T) {
	repoPaths := map[string]string{
		"agentic": "/Users/ivar/Projects/agentic",
		"taulu":   "/Users/ivar/Projects/taulu",
	}

	desc := "Fix @agentic/internal/tui/wizard.go and @taulu/cmd/main.go"
	got := ExtractAtMentionRepos(desc, repoPaths)

	if len(got) != 2 {
		t.Errorf("expected 2 repos, got %v", got)
	}
}

func TestExtractAtMentionRepos_NoDuplicates(t *testing.T) {
	repoPaths := map[string]string{
		"agentic": "/Users/ivar/Projects/agentic",
	}

	desc := "Fix @/Users/ivar/Projects/agentic/a.go and @/Users/ivar/Projects/agentic/b.go"
	got := ExtractAtMentionRepos(desc, repoPaths)

	if len(got) != 1 {
		t.Errorf("expected 1 repo (deduplicated), got %v", got)
	}
}

func TestExtractAtMentionRepos_NoFalseMatchOverlappingPaths(t *testing.T) {
	repoPaths := map[string]string{
		"repo":  "/Users/ivar/Projects/repo",
		"repo2": "/Users/ivar/Projects/repo2",
	}

	desc := "Fix @/Users/ivar/Projects/repo2/main.go"
	got := ExtractAtMentionRepos(desc, repoPaths)

	if len(got) != 1 || got[0] != "repo2" {
		t.Errorf("expected [repo2], got %v (should not false-match 'repo')", got)
	}
}

func TestExtractAtMentionRepos_ExactPathMatch(t *testing.T) {
	repoPaths := map[string]string{
		"repo": "/Users/ivar/Projects/repo",
	}

	desc := "Fix @/Users/ivar/Projects/repo"
	got := ExtractAtMentionRepos(desc, repoPaths)

	if len(got) != 1 || got[0] != "repo" {
		t.Errorf("expected [repo], got %v", got)
	}
}

func TestExtractAtMentionRepos_Empty(t *testing.T) {
	got := ExtractAtMentionRepos("no mentions here", map[string]string{"repo": "/path"})
	if len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

func TestExtractAtMentionRepos_NilInputs(t *testing.T) {
	got := ExtractAtMentionRepos("", nil)
	if got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestExtractAtMentionRepos_QualifiedRepoName(t *testing.T) {
	repoPaths := map[string]string{
		"rootA/myrepo": "/some/path/myrepo",
		"other":        "/some/other",
	}

	desc := "Fix @rootA/myrepo/internal/file.go"
	got := ExtractAtMentionRepos(desc, repoPaths)

	if len(got) != 1 {
		t.Fatalf("expected 1 repo, got %v", got)
	}
	if got[0] != "rootA/myrepo" {
		t.Errorf("expected [rootA/myrepo], got %v", got)
	}
}

func TestExtractAtMentionRepos_QualifiedRepoNameExact(t *testing.T) {
	repoPaths := map[string]string{
		"rootA/myrepo": "/some/path/myrepo",
	}

	desc := "See @rootA/myrepo for details"
	got := ExtractAtMentionRepos(desc, repoPaths)

	if len(got) != 1 {
		t.Fatalf("expected 1 repo, got %v", got)
	}
	if got[0] != "rootA/myrepo" {
		t.Errorf("expected [rootA/myrepo], got %v", got)
	}
}

func TestExtractAtMentionRepos_QualifiedAndSimpleRepoNames(t *testing.T) {
	repoPaths := map[string]string{
		"rootA/myrepo": "/some/path/myrepo",
		"agentic":      "/some/agentic",
	}

	desc := "Fix @rootA/myrepo/file.go and @agentic/cmd/main.go"
	got := ExtractAtMentionRepos(desc, repoPaths)

	sort.Strings(got)
	if len(got) != 2 {
		t.Fatalf("expected 2 repos, got %v", got)
	}
	if got[0] != "agentic" || got[1] != "rootA/myrepo" {
		t.Errorf("expected [agentic, rootA/myrepo], got %v", got)
	}
}

func TestExtractAtMentionRepos_QualifiedNameNoFalsePrefix(t *testing.T) {
	repoPaths := map[string]string{
		"rootA":        "/path/rootA",
		"rootA/myrepo": "/path/myrepo",
	}

	desc := "Fix @rootA/myrepo/file.go"
	got := ExtractAtMentionRepos(desc, repoPaths)

	// Should resolve to the most specific (longest) match only
	if len(got) != 1 {
		t.Fatalf("expected 1 repo (most specific match), got %v", got)
	}
	if got[0] != "rootA/myrepo" {
		t.Errorf("expected [rootA/myrepo], got %v", got)
	}
}

func TestExtractAtMentionRepos_ShortRepoStillMatchesAlone(t *testing.T) {
	// When the mention is just @rootA/file.go (not @rootA/myrepo/...),
	// the short repo "rootA" should still match correctly.
	repoPaths := map[string]string{
		"rootA":        "/path/rootA",
		"rootA/myrepo": "/path/myrepo",
	}

	desc := "Fix @rootA/somefile.go"
	got := ExtractAtMentionRepos(desc, repoPaths)

	if len(got) != 1 {
		t.Fatalf("expected 1 repo, got %v", got)
	}
	if got[0] != "rootA" {
		t.Errorf("expected [rootA], got %v", got)
	}
}
