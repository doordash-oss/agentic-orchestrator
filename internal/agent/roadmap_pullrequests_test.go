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
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

const pullRequestsFixturePhases = 3

func pullRequestsRoadmapText(table string) string {
	var b strings.Builder
	b.WriteString("# Roadmap\n\n")
	for phase := 1; phase <= pullRequestsFixturePhases; phase++ {
		b.WriteString("## Phase ")
		b.WriteString(strings.TrimSpace(itoa(phase)))
		b.WriteString(": Slice ")
		b.WriteString(strings.TrimSpace(itoa(phase)))
		b.WriteString("\n\n### Goal\n\nShip it.\n\n")
	}
	b.WriteString("## Pull Requests\n\n")
	b.WriteString(table)
	b.WriteString("\n\n## Overall Exit Criteria\n\n- Done.\n")
	return b.String()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}

const pullRequestsValidTable = `| # | Title | Phases | Rationale |
|---|---|---|---|
| 1 | Skeleton | 1 | Phase 1 stands alone. |
| 2 | Core and polish | 2-3 | Two halves of one reviewable concern. |`

func TestParseRoadmapPullRequestsValidMultiRow(t *testing.T) {
	text := pullRequestsRoadmapText(pullRequestsValidTable)
	rows, problems := ParseRoadmapPullRequests(text)
	if len(problems) > 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if rows[0].Position != 1 || rows[0].Title != "Skeleton" || rows[1].Position != 2 || rows[1].Title != "Core and polish" {
		t.Errorf("positions/titles mismatch: %+v", rows)
	}
	if len(rows[0].Phases) != 1 || rows[0].Phases[0] != 1 {
		t.Errorf("row 1 phases = %v, want [1]", rows[0].Phases)
	}
	if len(rows[1].Phases) != 2 || rows[1].Phases[0] != 2 || rows[1].Phases[1] != 3 {
		t.Errorf("row 2 phases = %v, want [2 3]", rows[1].Phases)
	}
	if rows[0].Rationale != "Phase 1 stands alone." || rows[1].Rationale != "Two halves of one reviewable concern." {
		t.Errorf("rationales mismatch: %+v", rows)
	}
}

func TestParseRoadmapPullRequestsStillYieldsPhasesWithoutSection(t *testing.T) {
	text := "# Roadmap\n\n## Phase 1: Only\n\n### Goal\n\nShip it.\n"
	phases, err := ParseRoadmap(text)
	if err != nil {
		t.Fatalf("ParseRoadmap: %v", err)
	}
	if len(phases) != 1 || phases[0].Number != 1 || phases[0].Name != "Only" {
		t.Fatalf("phases = %+v, want one phase named Only", phases)
	}
	rows, problems := ParseRoadmapPullRequests(text)
	if rows != nil {
		t.Errorf("rows = %+v, want nil without the section", rows)
	}
	if len(problems) != 1 || !strings.Contains(problems[0], "## Pull Requests") {
		t.Errorf("problems = %v, want one naming the section", problems)
	}
}

func TestValidateRoadmapPullRequestsTableEveryRule(t *testing.T) {
	cases := []struct {
		name     string
		roadmap  string
		wantText string
	}{
		{
			name:     "missing section",
			roadmap:  "# Roadmap\n\n## Phase 1: Only\n\n### Goal\n\nShip it.\n",
			wantText: "section is missing",
		},
		{
			name:     "missing table",
			roadmap:  "# Roadmap\n\n## Phase 1: Only\n\n### Goal\n\nShip it.\n\n## Pull Requests\n\nNo table here.\n",
			wantText: "no table found",
		},
		{
			name: "positions out of order",
			roadmap: pullRequestsRoadmapText(`| # | Title | Phases | Rationale |
|---|---|---|---|
| 2 | Skeleton | 1 | First. |
| 1 | Rest | 2-3 | Second. |`),
			wantText: "positions must be 1..2 in order",
		},
		{
			name: "positions not starting at one",
			roadmap: pullRequestsRoadmapText(`| # | Title | Phases | Rationale |
|---|---|---|---|
| 2 | Skeleton | 1 | First. |`),
			wantText: "positions must be 1..1 in order",
		},
		{
			name: "phase missing from every row",
			roadmap: pullRequestsRoadmapText(`| # | Title | Phases | Rationale |
|---|---|---|---|
| 1 | Skeleton | 1 | First. |
| 2 | Polish | 3 | Last. |`),
			wantText: "phase 2 is not covered by any row",
		},
		{
			name: "phase in two rows",
			roadmap: pullRequestsRoadmapText(`| # | Title | Phases | Rationale |
|---|---|---|---|
| 1 | First | 1-2 | First. |
| 2 | Second | 2-3 | Second. |`),
			wantText: "phase 2 appears in rows 1, 2",
		},
		{
			name: "unknown phase number",
			roadmap: pullRequestsRoadmapText(`| # | Title | Phases | Rationale |
|---|---|---|---|
| 1 | Everything | 1-4 | All of it. |`),
			wantText: "phase 4, which is not a roadmap phase",
		},
		{
			name: "non-consecutive phases in a row",
			roadmap: pullRequestsRoadmapText(`| # | Title | Phases | Rationale |
|---|---|---|---|
| 1 | First | 1 | First. |
| 2 | Rest | 2, 3 | Rest. |`),
			wantText: "Phases cell",
		},
		{
			name: "rows not ascending",
			roadmap: pullRequestsRoadmapText(`| # | Title | Phases | Rationale |
|---|---|---|---|
| 1 | Upper | 3 | Last. |
| 2 | Lower | 1-2 | First. |`),
			wantText: "rows must be contiguous and ascending",
		},
		{
			name: "empty title",
			roadmap: pullRequestsRoadmapText(`| # | Title | Phases | Rationale |
|---|---|---|---|
| 1 | | 1-3 | Everything. |`),
			wantText: "row 1 has an empty title",
		},
		{
			name: "single-phase roadmap with two rows",
			roadmap: "# Roadmap\n\n## Phase 1: Only\n\n### Goal\n\nShip it.\n\n## Pull Requests\n\n" + `| # | Title | Phases | Rationale |
|---|---|---|---|
| 1 | First | 1 | First. |
| 2 | Second | 1 | Second. |`,
			wantText: "single-phase roadmap must have exactly one row",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			phases, err := ParseRoadmap(tc.roadmap)
			if err != nil {
				t.Fatalf("ParseRoadmap: %v", err)
			}
			_, problems := ValidateRoadmapPullRequestsTable(tc.roadmap, phases)
			if len(problems) == 0 {
				t.Fatalf("expected at least one problem, got none")
			}
			found := false
			for _, problem := range problems {
				if strings.Contains(problem, "## Pull Requests") && strings.Contains(problem, tc.wantText) {
					found = true
				}
			}
			if !found {
				t.Errorf("no problem names the section and %q; problems: %v", tc.wantText, problems)
			}
		})
	}
}

func TestValidateRoadmapPullRequestsTableValidReportsNoProblems(t *testing.T) {
	text := pullRequestsRoadmapText(pullRequestsValidTable)
	phases, err := ParseRoadmap(text)
	if err != nil {
		t.Fatalf("ParseRoadmap: %v", err)
	}
	_, problems := ValidateRoadmapPullRequestsTable(text, phases)
	if len(problems) > 0 {
		t.Errorf("unexpected problems: %v", problems)
	}
}

func TestValidateRoadmapPullRequestsTableNonConsecutiveRange(t *testing.T) {
	// Comma lists are rejected at the cell level; a row covering 1 and 3
	// without a comma is impossible via ranges, so build rows directly to
	// exercise the consecutive-phase rule on parsed rows.
	rows := []RoadmapPullRequest{
		{Position: 1, Title: "First", Phases: []int{1, 3}},
	}
	phases := []RoadmapPhase{{Number: 1}, {Number: 2}, {Number: 3}}
	problems := ValidateRoadmapPullRequests(phases, rows)
	found := false
	for _, problem := range problems {
		if strings.Contains(problem, "## Pull Requests") && strings.Contains(problem, "phases 1 and 3 are not consecutive") {
			found = true
		}
	}
	if !found {
		t.Errorf("no consecutive-phase problem; problems: %v", problems)
	}
}

func TestValidateRoadmapPullRequestsDeliveryMode(t *testing.T) {
	twoRows := []RoadmapPullRequest{
		{Position: 1, Title: "First", Phases: []int{1}},
		{Position: 2, Title: "Second", Phases: []int{2}},
	}
	oneRowCoveringAll := []RoadmapPullRequest{
		{Position: 1, Title: "Whole feature", Phases: []int{1, 2, 3}},
	}

	problems := ValidateRoadmapPullRequestsDeliveryMode(feature.DeliveryModeSingle, twoRows)
	if len(problems) != 1 {
		t.Fatalf("single + two rows problems = %v, want exactly one", problems)
	}
	if !strings.Contains(problems[0], "## Pull Requests") || !strings.Contains(problems[0], "single pull request") {
		t.Errorf("single + two rows problem = %q, want it to name the section and the single-pull-request delivery", problems[0])
	}

	if got := ValidateRoadmapPullRequestsDeliveryMode(feature.DeliveryModeSingle, oneRowCoveringAll); len(got) != 0 {
		t.Errorf("single + one row covering all phases problems = %v, want none", got)
	}
	if got := ValidateRoadmapPullRequestsDeliveryMode(feature.DeliveryModeStack, twoRows); len(got) != 0 {
		t.Errorf("stack + two rows problems = %v, want none", got)
	}
	if got := ValidateRoadmapPullRequestsDeliveryMode("", twoRows); len(got) != 0 {
		t.Errorf("unset mode + two rows problems = %v, want none (unset defaults to stack)", got)
	}
}

func TestValidateRoadmapPullRequestsTableForMode(t *testing.T) {
	twoPhaseRoadmap := func(table string) string {
		return "# Roadmap\n\n## Phase 1: First\n\n### Goal\n\nShip it.\n\n## Phase 2: Second\n\n### Goal\n\nShip it.\n\n## Pull Requests\n\n" + table
	}
	validTwoRowTable := `| # | Title | Phases | Rationale |
|---|---|---|---|
| 1 | First | 1 | First slice. |
| 2 | Second | 2 | Second slice. |`
	oneRowTable := `| # | Title | Phases | Rationale |
|---|---|---|---|
| 1 | Whole feature | 1-2 | One pull request. |`
	structurallyBrokenTable := `| # | Title | Phases | Rationale |
|---|---|---|---|
| 1 | First | 1 | First slice. |
| 2 | Second | 3 | Not a roadmap phase. |`

	t.Run("single one row covering all phases is valid", func(t *testing.T) {
		text := twoPhaseRoadmap(oneRowTable)
		phases, err := ParseRoadmap(text)
		if err != nil {
			t.Fatalf("ParseRoadmap: %v", err)
		}
		rows, problems := ValidateRoadmapPullRequestsTableForMode(text, phases, feature.DeliveryModeSingle)
		if len(problems) != 0 {
			t.Fatalf("problems = %v, want none", problems)
		}
		if len(rows) != 1 || len(rows[0].Phases) != 2 {
			t.Errorf("rows = %+v, want one row covering both phases", rows)
		}
	})

	t.Run("single two rows reports only the delivery problem", func(t *testing.T) {
		text := twoPhaseRoadmap(validTwoRowTable)
		phases, err := ParseRoadmap(text)
		if err != nil {
			t.Fatalf("ParseRoadmap: %v", err)
		}
		_, problems := ValidateRoadmapPullRequestsTableForMode(text, phases, feature.DeliveryModeSingle)
		if len(problems) != 1 {
			t.Fatalf("problems = %v, want exactly the delivery problem", problems)
		}
		if !strings.Contains(problems[0], "## Pull Requests") || !strings.Contains(problems[0], "single pull request") {
			t.Errorf("problem = %q, want it to name the section and the single-pull-request delivery", problems[0])
		}
	})

	t.Run("stack two rows stays valid", func(t *testing.T) {
		text := twoPhaseRoadmap(validTwoRowTable)
		phases, err := ParseRoadmap(text)
		if err != nil {
			t.Fatalf("ParseRoadmap: %v", err)
		}
		_, problems := ValidateRoadmapPullRequestsTableForMode(text, phases, feature.DeliveryModeStack)
		if len(problems) != 0 {
			t.Fatalf("problems = %v, want none for stack delivery", problems)
		}
	})

	t.Run("single combines structural and delivery problems", func(t *testing.T) {
		text := twoPhaseRoadmap(structurallyBrokenTable)
		phases, err := ParseRoadmap(text)
		if err != nil {
			t.Fatalf("ParseRoadmap: %v", err)
		}
		_, problems := ValidateRoadmapPullRequestsTableForMode(text, phases, feature.DeliveryModeSingle)
		if len(problems) < 2 {
			t.Fatalf("problems = %v, want structural and delivery problems", problems)
		}
		sawStructural, sawDelivery := false, false
		for _, problem := range problems {
			if strings.Contains(problem, "not a roadmap phase") {
				sawStructural = true
			}
			if strings.Contains(problem, "single pull request") {
				sawDelivery = true
			}
		}
		if !sawStructural || !sawDelivery {
			t.Errorf("problems = %v, want both a structural problem and the single-delivery problem", problems)
		}
	})
}

func TestParsePhasesCellAcceptsAndRejects(t *testing.T) {
	accepts := []struct {
		cell string
		want []int
	}{
		{cell: "3", want: []int{3}},
		{cell: "2-4", want: []int{2, 3, 4}},
		{cell: "2–4", want: []int{2, 3, 4}},
		{cell: " 2-4 ", want: []int{2, 3, 4}},
		{cell: "2 - 4", want: []int{2, 3, 4}},
	}
	for _, tc := range accepts {
		got, problems := parsePhasesCell(1, tc.cell)
		if len(problems) > 0 {
			t.Errorf("cell %q: unexpected problems %v", tc.cell, problems)
			continue
		}
		if len(got) != len(tc.want) {
			t.Errorf("cell %q: phases = %v, want %v", tc.cell, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("cell %q: phases = %v, want %v", tc.cell, got, tc.want)
				break
			}
		}
	}
	rejects := []string{"1, 2", "1,2", "Phase 3", "phase 3", "two", "4-2", "-3", ""}
	for _, cell := range rejects {
		got, problems := parsePhasesCell(2, cell)
		if len(problems) == 0 {
			t.Errorf("cell %q: expected a problem, got phases %v", cell, got)
			continue
		}
		if got != nil {
			t.Errorf("cell %q: expected no phases, got %v", cell, got)
		}
		if !strings.Contains(problems[0], "## Pull Requests") {
			t.Errorf("cell %q: problem does not name the section: %q", cell, problems[0])
		}
		if cell != "" && !strings.Contains(problems[0], cell) {
			t.Errorf("cell %q: problem does not name the offending cell: %q", cell, problems[0])
		}
	}
}

func TestDeriveStackLayers(t *testing.T) {
	rows := []RoadmapPullRequest{
		{Position: 1, Title: "Skeleton", Phases: []int{1}},
		{Position: 2, Title: "A Very Long Layer Title That Certainly Exceeds The Forty Character Slug Bound", Phases: []int{2}},
		{Position: 3, Title: "Skeleton", Phases: []int{3}},
	}
	layers := DeriveStackLayers(rows)
	if len(layers) != 3 {
		t.Fatalf("layers = %d, want 3", len(layers))
	}
	if layers[0].Slug != "skeleton" {
		t.Errorf("layer 1 slug = %q, want %q", layers[0].Slug, "skeleton")
	}
	long := layers[1].Slug
	if len(long) > 40 {
		t.Errorf("layer 2 slug %q exceeds the 40-character slug bound", long)
	}
	if !strings.HasPrefix(long, "a-very-long-layer-title-that-certainly") {
		t.Errorf("layer 2 slug %q is not the truncated title slug", long)
	}
	if layers[2].Slug != "skeleton-2" {
		t.Errorf("layer 3 slug = %q, want deduplicated %q", layers[2].Slug, "skeleton-2")
	}
	for i, layer := range layers {
		if layer.Position != rows[i].Position || layer.Title != rows[i].Title {
			t.Errorf("layer %d identity mismatch: %+v", i+1, layer)
		}
	}
	seen := map[string]bool{}
	for _, layer := range layers {
		if seen[layer.Slug] {
			t.Errorf("duplicate slug %q", layer.Slug)
		}
		seen[layer.Slug] = true
	}
}
