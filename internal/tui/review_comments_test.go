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

func TestReviewCommentsModelSelectionAndDetailScroll(t *testing.T) {
	t.Parallel()

	m := NewReviewCommentsModel("feat-1", "bpf-cassandra-probe", testReviewComments(), 120, 18)
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyDown, Text: "j"})
	view := stripANSI(updated.View())
	if !strings.Contains(view, "cmd/bpfagent/api.go:116") || !strings.Contains(view, "redactFlags(flags)") {
		t.Fatalf("down key did not move selected detail to second comment:\n%s", view)
	}

	scrolled, _ := updated.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if scrolled.browser.selected != updated.browser.selected {
		t.Fatalf("PgDown changed selected index from %d to %d", updated.browser.selected, scrolled.browser.selected)
	}
}

func TestReviewCommentsModelIncludeExcludeAndEnterAction(t *testing.T) {
	t.Parallel()

	m := NewReviewCommentsModel("feat-1", "bpf-cassandra-probe", testReviewComments(), 120, 36)
	excluded, _ := m.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	view := stripANSI(excluded.View())
	if !strings.Contains(view, "2 included") || !strings.Contains(view, "[enter] Address included 2") {
		t.Fatalf("space did not update included count:\n%s", view)
	}

	_, cmd := excluded.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter returned nil command, want included review-comments action")
	}
	msg, ok := cmd().(ReviewCommentsActionMsg)
	if !ok {
		t.Fatalf("enter command type = %T, want ReviewCommentsActionMsg", cmd())
	}
	if msg.Mode != ReviewCommentsActionIncluded || len(msg.Comments) != 2 || msg.Comments[0].ID != 102 {
		t.Fatalf("enter action = %+v, want included comments 102 and 201", msg)
	}
}

func TestReviewCommentsModelShiftAAlwaysAddressesAll(t *testing.T) {
	t.Parallel()

	m := NewReviewCommentsModel("feat-1", "bpf-cassandra-probe", testReviewComments(), 120, 36)
	excluded, _ := m.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})

	_, cmd := excluded.Update(tea.KeyPressMsg{Code: 'A', Text: "A"})
	if cmd == nil {
		t.Fatal("Shift+A returned nil command, want address-all action")
	}
	msg, ok := cmd().(ReviewCommentsActionMsg)
	if !ok {
		t.Fatalf("Shift+A command type = %T, want ReviewCommentsActionMsg", cmd())
	}
	if msg.Mode != ReviewCommentsActionAll || len(msg.Comments) != 3 {
		t.Fatalf("Shift+A action = %+v, want all 3 comments", msg)
	}
}

func TestReviewCommentsModelFilterAndEscape(t *testing.T) {
	t.Parallel()

	m := NewReviewCommentsModel("feat-1", "bpf-cassandra-probe", testReviewComments(), 120, 36)
	filtering, _ := m.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	filtering, _ = filtering.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	filtering, _ = filtering.Update(tea.KeyPressMsg{Code: 'e', Text: "e"})
	filtering, _ = filtering.Update(tea.KeyPressMsg{Code: 'v', Text: "v"})
	filtering, _ = filtering.Update(tea.KeyPressMsg{Code: 'i', Text: "i"})
	filtering, _ = filtering.Update(tea.KeyPressMsg{Code: 'e', Text: "e"})
	filtering, _ = filtering.Update(tea.KeyPressMsg{Code: 'w', Text: "w"})
	filtering, _ = filtering.Update(tea.KeyPressMsg{Code: 'e', Text: "e"})
	filtering, _ = filtering.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})

	view := stripANSI(filtering.View())
	if strings.Contains(view, "cmd/bpfagent/api.go:79") || !strings.Contains(view, "PR conversation") {
		t.Fatalf("filter did not narrow to reviewer issue comment:\n%s", view)
	}

	cleared, _ := filtering.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	view = stripANSI(cleared.View())
	if !strings.Contains(view, "cmd/bpfagent/api.go:79") || !strings.Contains(view, "3 pending") {
		t.Fatalf("escape did not clear filter:\n%s", view)
	}
}

func TestReviewCommentsModelAllExcludedDisablesEnter(t *testing.T) {
	t.Parallel()

	m := NewReviewCommentsModel("feat-1", "bpf-cassandra-probe", testReviewComments(), 120, 36)
	for range testReviewComments() {
		m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
		m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown, Text: "j"})
	}

	view := stripANSI(m.View())
	if !strings.Contains(view, "0 included") {
		t.Fatalf("expected all comments excluded:\n%s", view)
	}
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("enter with all comments excluded returned command, want nil")
	}
	view = stripANSI(updated.View())
	if !strings.Contains(view, "No comments included") {
		t.Fatalf("missing all-excluded warning:\n%s", view)
	}
}

func TestReviewCommentsModelMissingDiffAndNarrowWidth(t *testing.T) {
	t.Parallel()

	comment := git.ReviewComment{ID: 301, Type: git.CommentTypeReviewBody, Body: "Top-level review body without a diff."}
	comment.User.Login = "reviewer"
	m := NewReviewCommentsModel("feat-1", "narrow-feature", []git.ReviewComment{comment}, 72, 20)
	view := stripANSI(m.View())

	for _, want := range []string{
		"Top-level review body without a diff.",
		"No diff context available",
		"[Shift+A] Address all 1",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("narrow/missing-diff view missing %q:\n%s", want, view)
		}
	}
}
