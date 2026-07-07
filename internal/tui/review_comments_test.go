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

func TestReviewCommentsModelSortsByCreatedAt(t *testing.T) {
	t.Parallel()

	comments := []git.ReviewComment{
		{ID: 3, Type: git.CommentTypeReview, Path: "late.go", Line: 30, Body: "late", CreatedAt: "2026-07-07T12:00:00Z"},
		{ID: 1, Type: git.CommentTypeReview, Path: "early.go", Line: 10, Body: "early", CreatedAt: "2026-07-07T10:00:00Z"},
		{ID: 2, Type: git.CommentTypeReview, Path: "middle.go", Line: 20, Body: "middle", CreatedAt: "2026-07-07T11:00:00Z"},
	}

	m := NewReviewCommentsModel("feat-1", "chronological", comments, 120, 36)
	if m.browser.items[0].ID != 1 || m.browser.items[1].ID != 2 || m.browser.items[2].ID != 3 {
		t.Fatalf("browser item order = [%d %d %d], want [1 2 3]",
			m.browser.items[0].ID, m.browser.items[1].ID, m.browser.items[2].ID)
	}

	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter returned nil command, want chronological action")
	}
	msg, ok := cmd().(ReviewCommentsActionMsg)
	if !ok {
		t.Fatalf("enter command type = %T, want ReviewCommentsActionMsg", cmd())
	}
	for i, wantID := range []int{1, 2, 3} {
		if msg.Comments[i].ID != wantID {
			t.Fatalf("action comments[%d].ID = %d, want %d", i, msg.Comments[i].ID, wantID)
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

	m := NewReviewCommentsModel("feat-1", "bpf-cassandra-probe", testReviewComments(), 120, 24)
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

func TestReviewCommentsModelPanelFocusControlsArrowBehavior(t *testing.T) {
	t.Parallel()

	comments := testReviewComments()
	comments[0].Body = strings.Repeat("Long review comment line with enough words to wrap inside the detail panel. ", 40)
	m := NewReviewCommentsModel("feat-1", "bpf-cassandra-probe", comments, 120, 24)
	focused, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if focused.browser.focus != reviewCommentsFocusDetail {
		t.Fatalf("right arrow focus = %v, want detail", focused.browser.focus)
	}
	scrolled, _ := focused.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if scrolled.browser.selected != focused.browser.selected {
		t.Fatalf("down key in detail focus changed selection from %d to %d", focused.browser.selected, scrolled.browser.selected)
	}
	if scrolled.browser.detail.YOffset() <= focused.browser.detail.YOffset() {
		t.Fatalf("down key in detail focus did not scroll detail; offset %d <= %d", scrolled.browser.detail.YOffset(), focused.browser.detail.YOffset())
	}

	focused, _ = scrolled.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if focused.browser.focus != reviewCommentsFocusQueue {
		t.Fatalf("left arrow focus = %v, want queue", focused.browser.focus)
	}
	moved, _ := focused.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if moved.browser.selected != focused.browser.selected+1 {
		t.Fatalf("down key in queue focus selected %d, want %d", moved.browser.selected, focused.browser.selected+1)
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

func TestReviewCommentsModelFitsFooterWithinHeight(t *testing.T) {
	t.Parallel()

	m := NewReviewCommentsModel("feat-1", "bpf-cassandra-probe", testReviewComments(), 160, 24)
	view := stripANSI(m.View())
	lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
	if len(lines) > 24 {
		t.Fatalf("view rendered %d lines, want <= 24:\n%s", len(lines), view)
	}
	if !strings.Contains(lines[len(lines)-1], "[Shift+A] Address all 3") {
		t.Fatalf("footer not visible as final line:\n%s", view)
	}
}

func TestReviewCommentsModelPgDownKeepsFooterVisible(t *testing.T) {
	t.Parallel()

	long := testReviewComments()[0]
	long.Body = strings.Repeat("Long review comment line with enough words to wrap inside the detail panel. ", 80)
	m := NewReviewCommentsModel("feat-1", "bpf-cassandra-probe", []git.ReviewComment{long}, 180, 30)
	for i := 0; i < 12; i++ {
		m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	}

	view := stripANSI(m.View())
	lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
	if len(lines) > 30 {
		t.Fatalf("view rendered %d lines after PgDown, want <= 30:\n%s", len(lines), view)
	}
	if !strings.Contains(lines[len(lines)-1], "[Shift+A] Address all 1") {
		t.Fatalf("footer not visible as final line after PgDown:\n%s", view)
	}
}

func TestReviewCommentsModelUsesDashboardStyleBoxes(t *testing.T) {
	t.Parallel()

	m := NewReviewCommentsModel("feat-1", "bpf-cassandra-probe", testReviewComments(), 180, 30)
	view := stripANSI(m.View())

	lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
	topLine := -1
	bottomLine := -1
	for i, line := range lines {
		if strings.Count(line, "╭") >= 2 && strings.Count(line, "╮") >= 2 {
			if topLine == -1 {
				topLine = i
			}
		}
		if strings.Count(line, "╰") >= 2 && strings.Count(line, "╯") >= 2 {
			bottomLine = i
		}
	}
	if topLine == -1 || bottomLine == -1 || bottomLine <= topLine {
		t.Fatalf("review comments did not render closed dashboard-style box borders:\n%s", view)
	}
	for _, line := range []string{lines[topLine], lines[bottomLine]} {
		for _, asciiBorder := range []string{"+", "|"} {
			if strings.Contains(line, asciiBorder) {
				t.Fatalf("review comments box border should match dashboard style, found ASCII %q in %q", asciiBorder, line)
			}
		}
	}
	if strings.Count(lines[topLine], "╭") < 2 || strings.Count(lines[topLine], "╮") < 2 ||
		strings.Count(lines[bottomLine], "╰") < 2 || strings.Count(lines[bottomLine], "╯") < 2 {
		t.Fatalf("review comments borders do not include both pane corners:\n%s\n%s", lines[topLine], lines[bottomLine])
	}
	if !strings.Contains(lines[topLine], "Queue") || !strings.Contains(lines[topLine], "Detail") {
		t.Fatalf("review comments boxes should render titles in the top border like the dashboard:\n%s", lines[topLine])
	}
}
