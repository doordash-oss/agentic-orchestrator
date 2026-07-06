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

package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
)

func testReviewComments() []git.ReviewComment {
	c1 := git.ReviewComment{
		ID:       101,
		Type:     git.CommentTypeReview,
		Path:     "cmd/bpfagent/api.go",
		Line:     79,
		Body:     "unhandled error",
		DiffHunk: "@@ -76,7 +76,7 @@ func handleFeatures\n- json.NewEncoder(w).Encode(features)\n+ _ = json.NewEncoder(w).Encode(features)",
	}
	c1.User.Login = "ltagliamonte-dd"
	c2 := git.ReviewComment{
		ID:       102,
		Type:     git.CommentTypeReview,
		Path:     "cmd/bpfagent/api.go",
		Line:     116,
		Body:     "ditto",
		DiffHunk: "@@ -111,9 +111,9 @@ func handleFeatureAction\n- logger.Info(\"feature enabled\", \"flags\", flags)\n+ logger.Info(\"feature enabled\", \"flags\", redactFlags(flags))",
	}
	c2.User.Login = "ltagliamonte-dd"
	c3 := git.ReviewComment{
		ID:   201,
		Type: git.CommentTypeIssue,
		Body: "Please add a short note to the PR conversation.",
	}
	c3.User.Login = "reviewer"
	return []git.ReviewComment{c1, c2, c3}
}

func TestReviewCommentsModelInitialSplitView(t *testing.T) {
	t.Parallel()

	m := NewReviewCommentsModel("feat-1", "bpf-cassandra-probe", testReviewComments(), 120, 36)
	view := stripANSI(m.View())

	for _, want := range []string{
		"Review Comments",
		"bpf-cassandra-probe",
		"3 pending",
		"3 included",
		"Queue",
		"cmd/bpfagent/api.go:79",
		"@ltagliamonte-dd",
		"unhandled error",
		"Detail",
		"Inline review comment",
		"@@ -76,7 +76,7 @@ func handleFeatures",
		"[Shift+A] Address all 3",
		"[enter] Address included 3",
		"[/] Filter",
		"[esc] Back",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q:\n%s", want, view)
		}
	}
}

func TestReviewCommentsModelEmptyState(t *testing.T) {
	t.Parallel()

	m := NewReviewCommentsModel("feat-1", "empty-feature", nil, 100, 24)
	view := stripANSI(m.View())

	for _, want := range []string{
		"No pending review comments for this PR.",
		"[esc] Back",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("empty view missing %q:\n%s", want, view)
		}
	}
}

func TestReviewCommentsModelWindowResizePreservesSplitView(t *testing.T) {
	t.Parallel()

	m := NewReviewCommentsModel("feat-1", "bpf-cassandra-probe", testReviewComments(), 100, 24)
	updated, cmd := m.Update(tea.WindowSizeMsg{Width: 132, Height: 40})
	if cmd != nil {
		t.Fatal("resize returned command, want nil")
	}
	view := stripANSI(updated.View())
	if !strings.Contains(view, "cmd/bpfagent/api.go:79") || !strings.Contains(view, "Detail") {
		t.Fatalf("resized view lost split-view content:\n%s", view)
	}
}
