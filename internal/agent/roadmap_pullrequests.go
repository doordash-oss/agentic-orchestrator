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
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// PullRequestsHeading is the exact heading text of the roadmap section that
// groups phases into pull requests. Like every roadmap heading it
// participates in byte-equal section matching (sticky-approval frozen
// sections, forced-edit unlocking), so the text must stay stable.
const PullRequestsHeading = "## Pull Requests"

// RoadmapPullRequest is one parsed row of the roadmap's `## Pull Requests`
// table: a proposed pull request covering one or more consecutive phases.
type RoadmapPullRequest struct {
	Position  int
	Title     string
	Phases    []int
	Rationale string
}

var tableSeparatorCellRe = regexp.MustCompile(`^:?\-{1,}:?$`)

// ValidateRoadmapPullRequestsTable parses the roadmap's `## Pull Requests`
// table and checks it against the roadmap's phases, returning the parsed
// rows and every problem found. Each problem names the section so planning
// feedback can unlock revising it and humans can locate it. rows is nil
// when the section or its table could not be located at all.
func ValidateRoadmapPullRequestsTable(roadmapText string, phases []RoadmapPhase) (rows []RoadmapPullRequest, problems []string) {
	rows, problems = ParseRoadmapPullRequests(roadmapText)
	if rows == nil {
		return nil, problems
	}
	return rows, append(problems, ValidateRoadmapPullRequests(phases, rows)...)
}

// ParseRoadmapPullRequests locates the `## Pull Requests` section, reads the
// first markdown table under it, and decodes each row's position, title,
// phase set, and rationale. Problems report a missing section, a missing or
// malformed table, and unparseable cells; rows is nil only when no table
// could be read (missing section, missing table, or wrong columns).
func ParseRoadmapPullRequests(roadmapText string) (rows []RoadmapPullRequest, problems []string) {
	section, ok := extractTopLevelSection(roadmapText, PullRequestsHeading)
	if !ok {
		return nil, []string{PullRequestsHeading + ": section is missing — expected after the last phase section and before ## Overall Exit Criteria"}
	}
	tableLines := firstMarkdownTableLines(section)
	if len(tableLines) == 0 {
		return nil, []string{PullRequestsHeading + ": no table found under the heading — expected one table with the columns #, Title, Phases, Rationale"}
	}
	header := splitTableRow(tableLines[0])
	wantColumns := []string{"#", "Title", "Phases", "Rationale"}
	if len(header) != len(wantColumns) {
		return nil, []string{fmt.Sprintf("%s: table columns must be %s — found %s", PullRequestsHeading, strings.Join(wantColumns, ", "), quoteCells(header))}
	}
	for i, column := range wantColumns {
		if header[i] != column {
			return nil, []string{fmt.Sprintf("%s: table columns must be %s — found %s", PullRequestsHeading, strings.Join(wantColumns, ", "), quoteCells(header))}
		}
	}
	body := tableLines[1:]
	if len(body) > 0 && isTableSeparatorRow(body[0]) {
		body = body[1:]
	}
	rows = make([]RoadmapPullRequest, 0, len(body))
	for i, line := range body {
		rowNumber := i + 1
		cells := splitTableRow(line)
		if len(cells) < len(wantColumns) {
			problems = append(problems, fmt.Sprintf("%s: row %d is malformed — expected cells for #, Title, Phases, Rationale", PullRequestsHeading, rowNumber))
			continue
		}
		row := RoadmapPullRequest{
			Title:     strings.TrimSpace(cells[1]),
			Rationale: strings.TrimSpace(cells[3]),
		}
		position, posErr := strconv.Atoi(strings.TrimSpace(cells[0]))
		if posErr != nil {
			problems = append(problems, fmt.Sprintf("%s: row %d position cell %q is not a number — positions are 1..M in order", PullRequestsHeading, rowNumber, strings.TrimSpace(cells[0])))
		} else {
			row.Position = position
		}
		rowPhases, cellProblems := parsePhasesCell(rowNumber, cells[2])
		problems = append(problems, cellProblems...)
		row.Phases = rowPhases
		rows = append(rows, row)
	}
	return rows, problems
}

// parsePhasesCell decodes a `Phases` cell: a single phase number (`3`) or a
// hyphen range (`2-4`), with an en dash and surrounding whitespace
// tolerated. Comma lists and `Phase` prefixes are rejected with a problem
// naming the offending cell.
func parsePhasesCell(rowNumber int, cell string) ([]int, []string) {
	raw := strings.TrimSpace(cell)
	normalized := strings.ReplaceAll(raw, "–", "-")
	normalized = strings.TrimSpace(normalized)
	if normalized == "" {
		return nil, []string{fmt.Sprintf("%s: row %d Phases cell is empty — use a single phase number or a hyphen range like 2-4", PullRequestsHeading, rowNumber)}
	}
	if strings.Contains(normalized, ",") {
		return nil, []string{invalidPhasesCellProblem(rowNumber, raw)}
	}
	if strings.HasPrefix(strings.ToLower(normalized), "phase") {
		return nil, []string{invalidPhasesCellProblem(rowNumber, raw)}
	}
	lo, hi, isRange := strings.Cut(normalized, "-")
	lo = strings.TrimSpace(lo)
	if isRange {
		hi = strings.TrimSpace(hi)
		start, startErr := strconv.Atoi(lo)
		end, endErr := strconv.Atoi(hi)
		if startErr != nil || endErr != nil || start <= 0 || end < start {
			return nil, []string{invalidPhasesCellProblem(rowNumber, raw)}
		}
		phases := make([]int, 0, end-start+1)
		for p := start; p <= end; p++ {
			phases = append(phases, p)
		}
		return phases, nil
	}
	number, err := strconv.Atoi(lo)
	if err != nil || number <= 0 {
		return nil, []string{invalidPhasesCellProblem(rowNumber, raw)}
	}
	return []int{number}, nil
}

func invalidPhasesCellProblem(rowNumber int, raw string) string {
	return fmt.Sprintf("%s: row %d Phases cell %q is invalid — use a single phase number or a hyphen range like 2-4", PullRequestsHeading, rowNumber, raw)
}

// ValidateRoadmapPullRequests checks the structural rules of the parsed
// table against the roadmap's phases: positions are 1..M in order; every
// roadmap phase appears in exactly one row and no row names an unknown
// phase; each row's phases are consecutive; rows are contiguous and
// ascending in phase order; titles are non-empty; a single-phase roadmap
// has exactly one row.
func ValidateRoadmapPullRequests(phases []RoadmapPhase, rows []RoadmapPullRequest) []string {
	var problems []string
	if len(rows) == 0 {
		return []string{PullRequestsHeading + ": table has no rows — one row per pull request is required"}
	}
	for i, row := range rows {
		if row.Position != i+1 {
			problems = append(problems, fmt.Sprintf("%s: row %d has position %d — positions must be 1..%d in order starting at 1", PullRequestsHeading, i+1, row.Position, len(rows)))
		}
	}
	known := make(map[int]bool, len(phases))
	for _, phase := range phases {
		known[phase.Number] = true
	}
	coveredBy := make(map[int][]int)
	for i, row := range rows {
		for _, phaseNumber := range row.Phases {
			if !known[phaseNumber] {
				problems = append(problems, fmt.Sprintf("%s: row %d covers phase %d, which is not a roadmap phase", PullRequestsHeading, i+1, phaseNumber))
				continue
			}
			coveredBy[phaseNumber] = append(coveredBy[phaseNumber], i+1)
		}
	}
	for _, phase := range phases {
		rowsFor := coveredBy[phase.Number]
		switch len(rowsFor) {
		case 0:
			problems = append(problems, fmt.Sprintf("%s: phase %d is not covered by any row", PullRequestsHeading, phase.Number))
		case 1:
		default:
			problems = append(problems, fmt.Sprintf("%s: phase %d appears in rows %s — every phase belongs to exactly one row", PullRequestsHeading, phase.Number, joinInts(rowsFor)))
		}
	}
	for i, row := range rows {
		for j := 1; j < len(row.Phases); j++ {
			if row.Phases[j] != row.Phases[j-1]+1 {
				problems = append(problems, fmt.Sprintf("%s: row %d phases %d and %d are not consecutive", PullRequestsHeading, i+1, row.Phases[j-1], row.Phases[j]))
				break
			}
		}
	}
	for i := 1; i < len(rows); i++ {
		previous, current := rows[i-1], rows[i]
		if len(previous.Phases) == 0 || len(current.Phases) == 0 {
			continue
		}
		previousEnd := previous.Phases[len(previous.Phases)-1]
		if current.Phases[0] != previousEnd+1 {
			problems = append(problems, fmt.Sprintf("%s: row %d starts at phase %d but row %d ends at phase %d — rows must be contiguous and ascending", PullRequestsHeading, i+1, current.Phases[0], i, previousEnd))
		}
	}
	for i, row := range rows {
		if strings.TrimSpace(row.Title) == "" {
			problems = append(problems, fmt.Sprintf("%s: row %d has an empty title", PullRequestsHeading, i+1))
		}
	}
	if len(phases) == 1 && len(rows) != 1 {
		problems = append(problems, fmt.Sprintf("%s: a single-phase roadmap must have exactly one row covering phase 1 — found %d rows", PullRequestsHeading, len(rows)))
	}
	return problems
}

// DeriveStackLayers converts validated table rows into the run-persisted
// layer composition. Each layer's slug reuses the feature slug rule
// (lowercase alphanumerics and hyphens, 40-character cap); duplicate slugs
// within one stack receive a numeric suffix so every layer slug is unique.
func DeriveStackLayers(rows []RoadmapPullRequest) []feature.StackLayer {
	if len(rows) == 0 {
		return nil
	}
	const slugBound = 40
	used := make(map[string]bool, len(rows))
	layers := make([]feature.StackLayer, 0, len(rows))
	for _, row := range rows {
		slug := feature.Slugify(row.Title)
		if used[slug] {
			for suffix := 2; ; suffix++ {
				suffixText := "-" + strconv.Itoa(suffix)
				base := slug
				if len(base)+len(suffixText) > slugBound {
					base = strings.TrimRight(base[:slugBound-len(suffixText)], "-")
				}
				candidate := base + suffixText
				if !used[candidate] {
					slug = candidate
					break
				}
			}
		}
		used[slug] = true
		layers = append(layers, feature.StackLayer{
			Position: row.Position,
			Title:    row.Title,
			Slug:     slug,
			Phases:   append([]int(nil), row.Phases...),
		})
	}
	return layers
}

// extractTopLevelSection returns the content between the line exactly equal
// to heading and the next top-level (`#`/`##`) heading. Heading matching is
// byte-equal after right-trimming, mirroring the section matching used for
// sticky approvals.
func extractTopLevelSection(text, heading string) (string, bool) {
	lines := strings.Split(text, "\n")
	start := -1
	for i, line := range lines {
		if strings.TrimRight(line, " \t") == heading {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return "", false
	}
	var section []string
	for i := start; i < len(lines); i++ {
		line := lines[i]
		if strings.HasPrefix(line, "# ") || strings.HasPrefix(line, "## ") {
			break
		}
		section = append(section, line)
	}
	return strings.Join(section, "\n"), true
}

// firstMarkdownTableLines returns the first contiguous run of pipe-table
// lines in section.
func firstMarkdownTableLines(section string) []string {
	var table []string
	inTable := false
	for _, line := range strings.Split(section, "\n") {
		trimmed := strings.TrimSpace(line)
		isTableLine := strings.HasPrefix(trimmed, "|")
		if isTableLine {
			inTable = true
			table = append(table, trimmed)
			continue
		}
		if inTable {
			break
		}
	}
	return table
}

// splitTableRow splits a pipe-table line into trimmed cells, dropping the
// leading and trailing delimiters.
func splitTableRow(line string) []string {
	trimmed := strings.TrimSpace(line)
	trimmed = strings.TrimPrefix(trimmed, "|")
	trimmed = strings.TrimSuffix(trimmed, "|")
	cells := strings.Split(trimmed, "|")
	for i := range cells {
		cells[i] = strings.TrimSpace(cells[i])
	}
	return cells
}

func isTableSeparatorRow(line string) bool {
	cells := splitTableRow(line)
	if len(cells) == 0 {
		return false
	}
	for _, cell := range cells {
		if !tableSeparatorCellRe.MatchString(cell) {
			return false
		}
	}
	return true
}

func quoteCells(cells []string) string {
	quoted := make([]string, len(cells))
	for i, cell := range cells {
		quoted[i] = strconv.Quote(cell)
	}
	return strings.Join(quoted, ", ")
}

func joinInts(values []int) string {
	texts := make([]string, len(values))
	for i, value := range values {
		texts[i] = strconv.Itoa(value)
	}
	return strings.Join(texts, ", ")
}
