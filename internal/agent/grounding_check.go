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
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// GroundingCheckResult is the outcome of the deterministic pre-check on a
// plan's `## Grounding` table. The gate flags two failure modes:
// classification cells outside {EXISTS, WILL-BE-CREATED}, and
// WILL-BE-CREATED rows whose bare-path Reference already exists on disk.
// EXISTS file-existence is left to the LLM judge — the hand-coded path
// parser was structurally brittle to repo-prefixed References and produced
// false positives that corrupted planner revisions.
type GroundingCheckResult struct {
	HasSection bool
	Findings   []GroundingFinding
}

// GroundingFinding describes one mechanical defect in a Grounding row.
type GroundingFinding struct {
	RowNumber      int    // 1-indexed row position in the data rows of the table
	Reference      string // verbatim Reference cell content
	Classification string // verbatim Classification cell content
	Path           string // resolved file path the check ran against (may be empty)
	Reason         string // human-readable defect description
}

// OK reports whether the pre-check found zero defects. A missing `## Grounding`
// section is itself a defect.
func (r GroundingCheckResult) OK() bool {
	return r.HasSection && len(r.Findings) == 0
}

// validClassifications mirrors the contract enforced by
// `skills/validate-phase-plan-grounding/SKILL.md`: rows must be exactly one of
// these two values. Anything else (e.g., `EXISTS-POST-PHASE-N`, `MAYBE`) is
// rejected by the LLM judge today; we reject it mechanically so the loop can
// converge in one revision.
var validClassifications = map[string]struct{}{
	"EXISTS":          {},
	"WILL-BE-CREATED": {},
}

// pathLikeRE matches backticked tokens that look like real on-disk file paths
// in the source tree: contain a slash, end in a known source-code extension,
// and have no template variables. Tokens with `<`/`{`
// (e.g. `<runDir>/implement/<repoName>/iter-NN/`) are excluded — those need
// fuzzy reasoning the LLM judge handles. Capture group 1 is the bare path,
// capture group 2 is the optional `:Symbol` or `:LineNumber` suffix (empty if
// none) — callers use it to distinguish "new symbol in existing file" from
// "new file" for WILL-BE-CREATED rows.
var pathLikeRE = regexp.MustCompile("`([^`<>{}]+/[^`<>{}]+\\.(?:go|md|yaml|yml|sh|bash|json|toml|txt|tmpl|html|css|js|ts|tsx|jsx|sql|proto|py|rs))(:[^`]+)?`")

// repoPrefixedPathRE matches `<repo>:<path>` even when <path> has no
// internal slash. Validated against known root Names by the caller.
var repoPrefixedPathRE = regexp.MustCompile("`([a-zA-Z][a-zA-Z0-9_-]*):([^`<>{}:]+\\.(?:go|md|yaml|yml|sh|bash|json|toml|txt|tmpl|html|css|js|ts|tsx|jsx|sql|proto|py|rs))(:[^`]+)?`")

// runtimeAnnotationRE detects rows whose Reference is annotated as a runtime
// path convention (e.g., `runs/run-001/run.yaml` (path convention)) rather
// than a source-tree file. The annotation is prose; we use its presence as a
// signal to skip the deterministic check and defer to the LLM judge.
var runtimeAnnotationRE = regexp.MustCompile("(?i)\\(path convention\\)|\\(runtime\\)|\\(at runtime\\)")

// GroundingRoot pairs an optional repo Name with its Worktree. When Name is
// non-empty, `<Name>:<path>` references resolve against this Worktree only.
type GroundingRoot struct {
	Name     string
	Worktree string
}

// CheckGroundingTable is the single-root convenience wrapper. HasSection=false
// signals a missing `## Grounding` section (itself a defect).
func CheckGroundingTable(planPath, worktree string) GroundingCheckResult {
	return CheckGroundingTableRoots(planPath, []string{worktree})
}

// CheckGroundingTableRoots resolves references against any of worktreeRoots.
// Anonymous (no Name) — `<repo>:<path>` prefixes don't route. Callers with
// repo names should use CheckGroundingTableRepos.
func CheckGroundingTableRoots(planPath string, worktreeRoots []string) GroundingCheckResult {
	roots := make([]GroundingRoot, 0, len(worktreeRoots))
	for _, w := range worktreeRoots {
		roots = append(roots, GroundingRoot{Worktree: w})
	}
	return CheckGroundingTableRepos(planPath, roots)
}

// CheckGroundingTableRepos resolves named worktree roots, routing
// `<repo>:<path>` and `<repo>/<path>` references to the matching root.
func CheckGroundingTableRepos(planPath string, roots []GroundingRoot) GroundingCheckResult {
	body := extractPlanSection(planPath, "## Grounding")
	if len(body) == 0 {
		return GroundingCheckResult{HasSection: false}
	}
	res := GroundingCheckResult{HasSection: true}
	rows := parseGroundingTable(string(body))
	for i, row := range rows {
		findings := checkGroundingRow(i+1, row, roots)
		res.Findings = append(res.Findings, findings...)
	}
	return res
}

// groundingRow holds one parsed table row.
type groundingRow struct {
	Reference      string
	Classification string
	Evidence       string
}

// parseGroundingTable extracts data rows from any Markdown table(s) inside the
// section body. Plans sometimes split the Grounding section into multiple
// tables separated by sub-headings or prose; this parser handles all of them
// by skipping any row whose Classification cell is the literal header word
// `Classification` and any separator row (cells made of dashes/colons).
// Lines that do not start with `|` (prose paragraphs) are ignored.
func parseGroundingTable(body string) []groundingRow {
	var rows []groundingRow
	for line := range strings.SplitSeq(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "|") {
			continue
		}
		cells := splitMarkdownRow(trimmed)
		if len(cells) < 3 {
			continue
		}
		// Skip Markdown separator rows (e.g., |---|---|---|).
		if isSeparatorRow(cells) {
			continue
		}
		ref := strings.TrimSpace(cells[0])
		class := strings.TrimSpace(cells[1])
		evidence := strings.TrimSpace(cells[2])
		// Skip header rows. Plans may include several tables in one section,
		// each repeating the header — recognising the literal header text is
		// cheaper and less error-prone than tracking table-boundary state.
		if strings.EqualFold(ref, "Reference") && strings.EqualFold(class, "Classification") {
			continue
		}
		rows = append(rows, groundingRow{
			Reference:      ref,
			Classification: class,
			Evidence:       evidence,
		})
	}
	return rows
}

func splitMarkdownRow(line string) []string {
	// Strip leading/trailing pipe before splitting so the inner cells line up.
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	return strings.Split(line, "|")
}

func isSeparatorRow(cells []string) bool {
	for _, c := range cells {
		c = strings.TrimSpace(c)
		c = strings.TrimPrefix(c, ":")
		c = strings.TrimSuffix(c, ":")
		if c == "" || strings.Trim(c, "-") != "" {
			return false
		}
	}
	return true
}

// checkGroundingRow flags illegal classifications and WILL-BE-CREATED rows
// whose bare-path Reference already exists. EXISTS file-existence is
// intentionally not checked — see GroundingCheckResult. The contradiction
// check fails safe: any path-resolution miss leaves the row to the LLM judge.
func checkGroundingRow(num int, row groundingRow, roots []GroundingRoot) []GroundingFinding {
	var findings []GroundingFinding
	classification := strings.ToUpper(strings.Trim(row.Classification, "`*_ "))
	// Reject classifications outside the two-value contract.
	if _, ok := validClassifications[classification]; !ok {
		return []GroundingFinding{{
			RowNumber:      num,
			Reference:      row.Reference,
			Classification: row.Classification,
			Reason:         fmt.Sprintf("classification %q is not one of {EXISTS, WILL-BE-CREATED} — see validate-phase-plan-grounding", row.Classification),
		}}
	}

	if classification != "WILL-BE-CREATED" {
		return findings
	}
	if runtimeAnnotationRE.MatchString(row.Reference) {
		return findings
	}

	candidateRoots := roots
	refPath, refSuffix, refLooksLikePath := extractFirstPath(row.Reference)
	if refPath == "" {
		if p, sfx, root := extractRepoPrefixedReference(row.Reference, roots); root != nil {
			refPath = p
			refSuffix = sfx
			refLooksLikePath = true
			candidateRoots = []GroundingRoot{*root}
		}
	}
	// `path:Symbol` means "new symbol in existing file" — legitimate WBC.
	if refPath == "" || !refLooksLikePath || refSuffix != "" {
		return findings
	}
	if stripped, matched := stripRepoPrefix(refPath, roots); matched != nil {
		refPath = stripped
		candidateRoots = []GroundingRoot{*matched}
	}

	if filepath.IsAbs(refPath) {
		if _, err := os.Stat(refPath); err == nil {
			findings = append(findings, wbcContradictionFinding(num, row, refPath))
		}
		return findings
	}
	for _, root := range candidateRoots {
		if root.Worktree == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(root.Worktree, refPath)); err == nil {
			findings = append(findings, wbcContradictionFinding(num, row, refPath))
			return findings
		}
	}
	return findings
}

func wbcContradictionFinding(num int, row groundingRow, resolvedPath string) GroundingFinding {
	return GroundingFinding{
		RowNumber:      num,
		Reference:      row.Reference,
		Classification: row.Classification,
		Path:           resolvedPath,
		Reason:         "WILL-BE-CREATED row references a bare path that already exists in the worktree (contradicts itself; if a new symbol is being added to an existing file, write the row as `path:NewSymbol` instead of just `path`)",
	}
}

// stripRepoPrefix strips a leading `<repo>:` or `<repo>/` segment when it
// matches a known root Name and returns the stripped path bound to that
// root. Anonymous roots never match. Only the first separator is considered.
func stripRepoPrefix(refPath string, roots []GroundingRoot) (string, *GroundingRoot) {
	sepIdx := -1
	for i, c := range refPath {
		if c == ':' || c == '/' {
			sepIdx = i
			break
		}
	}
	if sepIdx <= 0 {
		return "", nil
	}
	prefix := refPath[:sepIdx]
	for i := range roots {
		if roots[i].Name != "" && roots[i].Name == prefix {
			return refPath[sepIdx+1:], &roots[i]
		}
	}
	return "", nil
}

// extractRepoPrefixedReference picks a `<repo>:<path>` token whose path has
// no internal slash (e.g. `agentic:README.md`) — pathLikeRE requires `/`
// so root-level repo-prefixed paths need this fallback.
func extractRepoPrefixedReference(s string, roots []GroundingRoot) (string, string, *GroundingRoot) {
	matches := repoPrefixedPathRE.FindAllStringSubmatch(s, -1)
	for _, m := range matches {
		name := m[1]
		for i := range roots {
			if roots[i].Name != "" && roots[i].Name == name {
				suffix := ""
				if len(m) > 3 {
					suffix = m[3]
				}
				return m[2], suffix, &roots[i]
			}
		}
	}
	return "", "", nil
}

// extractFirstPath returns the first path-shaped backticked token in s, the
// suffix after the path (`:Symbol` or `:LineNumber`, or empty), and a flag
// indicating it was found in s itself (not inferred elsewhere).
func extractFirstPath(s string) (string, string, bool) {
	m := pathLikeRE.FindStringSubmatch(s)
	if m == nil {
		return "", "", false
	}
	suffix := ""
	if len(m) > 2 {
		suffix = m[2]
	}
	return m[1], suffix, true
}

// FormatGroundingPreCheckFeedback renders a deterministic, LLM-shaped feedback
// document the validator can persist as `validation-grounding-feedback.md` and
// the reviser can consume directly. Output conforms to the file-based review
// handoff schema (## Findings / ## Suggestions / ## Verdict) so
// ParseReviewFeedback re-parses it cleanly downstream.
func FormatGroundingPreCheckFeedback(result GroundingCheckResult, headRev, branch string) string {
	var b strings.Builder
	b.WriteString("## Pre-flight\n")
	b.WriteString("```text\n")
	if headRev != "" {
		fmt.Fprintf(&b, "$ git rev-parse HEAD\n%s\n\n", headRev)
	}
	if branch != "" {
		fmt.Fprintf(&b, "$ git branch --show-current\n%s\n", branch)
	}
	b.WriteString("```\n\n")
	b.WriteString("This verdict was produced by the deterministic grounding gate that runs ahead of the LLM judge. Every defect listed below was decided by os.Stat / regex against the current worktree — no model judgment was involved, so the list is exhaustive and fixing every row will let this axis pass on the next attempt.\n\n")
	if !result.HasSection {
		b.WriteString("- **Critical**: Grounding section presence — the plan has no `## Grounding` section. Add one as required by the SKILL contract.\n")
		return FormatStructuredReviewFeedback(
			"Multi-Validator Plan Review — Grounding (deterministic pre-check)",
			strings.TrimRight(b.String(), "\n"),
			"",
			ReviewChangesRequested,
		)
	}
	if len(result.Findings) == 0 {
		// Pre-check passed — defer to the LLM judge. Render an APPROVED
		// stub the caller can ignore (this branch is only reached on a
		// clean pre-check, which we already short-circuit elsewhere).
		b.WriteString("- Grounding section presence: PASS.\n")
		b.WriteString("- Mechanical row checks: PASS — handing off to the LLM judge.\n")
		return FormatStructuredReviewFeedback(
			"Multi-Validator Plan Review — Grounding (deterministic pre-check)",
			strings.TrimRight(b.String(), "\n"),
			"",
			ReviewApproved,
		)
	}
	fmt.Fprintf(&b, "- **High**: Mechanical row checks failed (%d defect(s) below).\n\n", len(result.Findings))
	b.WriteString("### Defective rows\n")
	for _, f := range result.Findings {
		fmt.Fprintf(&b, "- Row %d: %s\n", f.RowNumber, f.Reason)
		fmt.Fprintf(&b, "  - Reference: %s\n", f.Reference)
		fmt.Fprintf(&b, "  - Classification: %s\n", f.Classification)
		if f.Path != "" {
			fmt.Fprintf(&b, "  - Resolved path: %s\n", f.Path)
		}
	}
	b.WriteString("\nFix every row above before the next emit. The LLM grounding judge has not run for this attempt — the deterministic gate short-circuits the axis when any row fails so a single revision can clear all of them.\n")
	return FormatStructuredReviewFeedback(
		"Multi-Validator Plan Review — Grounding (deterministic pre-check)",
		strings.TrimRight(b.String(), "\n"),
		"",
		ReviewChangesRequested,
	)
}
