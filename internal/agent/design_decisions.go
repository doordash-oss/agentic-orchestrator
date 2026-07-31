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
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

const designDecisionLedgerFile = "decision-ledger.md"
const designHumanDirectionQAFile = "human-direction-qa.md"

type designDecision struct {
	Question string
	Answer   string
	Notes    string
	Source   string
}

// WriteDesignDecisionLedger renders the harness-owned, stable-ID authority
// record consumed by every Design author, critic, and reviser.
func WriteDesignDecisionLedger(artifactDir string, f *feature.Feature, qaFilePaths []string) (string, error) {
	if f == nil {
		return "", fmt.Errorf("writing Design decision ledger: feature is nil")
	}
	var decisions []designDecision
	for _, qaPath := range qaFilePaths {
		data, err := os.ReadFile(qaPath)
		if err != nil {
			return "", fmt.Errorf("reading Design decision source %s: %w", qaPath, err)
		}
		decisions = append(decisions, parseDesignDecisions(string(data), qaPath)...)
	}

	var b strings.Builder
	b.WriteString("# Design Decision Ledger\n\n")
	b.WriteString("This file is generated and owned by the Agentico harness. It is the authoritative record of feature requirements and binding human decisions for every Design author, critic, and reviser. IDs remain stable while the ordered source inputs remain unchanged.\n\n")
	b.WriteString("## Requirements\n\n")
	b.WriteString("### REQ-001 — Feature request\n\n")
	b.WriteString("**Source:** original feature description\n\n")
	fmt.Fprintf(&b, "%s\n\n", strings.TrimSpace(f.EffectiveDescription()))
	if exitCriteria := strings.TrimSpace(f.ExitCriteria); exitCriteria != "" {
		b.WriteString("### REQ-002 — Exit criteria\n\n")
		b.WriteString("**Source:** feature exit criteria\n\n")
		fmt.Fprintf(&b, "%s\n\n", exitCriteria)
	}
	b.WriteString("## Binding Decisions\n\n")
	if len(decisions) == 0 {
		b.WriteString("- None recorded yet.\n")
	} else {
		for i, decision := range decisions {
			fmt.Fprintf(&b, "### DEC-%03d — %s\n\n", i+1, decision.Question)
			fmt.Fprintf(&b, "**Source:** %s\n\n", decision.Source)
			fmt.Fprintf(&b, "**Decision:** %s\n\n", decision.Answer)
			if decision.Notes != "" {
				fmt.Fprintf(&b, "**Notes:** %s\n\n", decision.Notes)
			}
		}
	}

	path := filepath.Join(artifactDir, designDecisionLedgerFile)
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return "", fmt.Errorf("writing Design decision ledger: %w", err)
	}
	return path, nil
}

func parseDesignDecisions(contents, source string) []designDecision {
	lines := strings.Split(strings.ReplaceAll(contents, "\r\n", "\n"), "\n")
	var out []designDecision
	for i := 0; i < len(lines); {
		question, ok := strings.CutPrefix(strings.TrimSpace(lines[i]), "## Q:")
		if !ok {
			i++
			continue
		}
		question = strings.TrimSpace(question)
		i++
		var answerLines []string
		var notesLines []string
		capture := ""
		for i < len(lines) {
			trimmed := strings.TrimSpace(lines[i])
			if strings.HasPrefix(trimmed, "## Q:") {
				break
			}
			if answer, found := strings.CutPrefix(trimmed, "**A:**"); found {
				capture = "answer"
				answerLines = append(answerLines, strings.TrimSpace(answer))
				i++
				continue
			}
			if notes, found := strings.CutPrefix(trimmed, "**Notes:**"); found {
				capture = "notes"
				notesLines = append(notesLines, strings.TrimSpace(notes))
				i++
				continue
			}
			if strings.HasPrefix(trimmed, "_(auto-picked,") {
				capture = ""
			}
			switch capture {
			case "answer":
				answerLines = append(answerLines, strings.TrimSpace(lines[i]))
			case "notes":
				notesLines = append(notesLines, strings.TrimSpace(lines[i]))
			}
			i++
		}
		answer := strings.TrimSpace(strings.Join(answerLines, "\n"))
		if question != "" && answer != "" {
			out = append(out, designDecision{
				Question: question,
				Answer:   answer,
				Notes:    strings.TrimSpace(strings.Join(notesLines, "\n")),
				Source:   source,
			})
		}
	}
	return out
}

// designDecisionSourcePaths appends Q&A captured during the initial Design
// interview and later revision attempts after upstream decision sources.
// Duplicate paths are removed.
func designDecisionSourcePaths(upstream []string, artifactDir string) []string {
	out := make([]string, 0, len(upstream)+1)
	seen := make(map[string]struct{}, len(upstream)+1)
	appendPath := func(path string) {
		if path == "" {
			return
		}
		if _, ok := seen[path]; ok {
			return
		}
		seen[path] = struct{}{}
		out = append(out, path)
	}
	for _, path := range upstream {
		appendPath(path)
	}
	current := filepath.Join(artifactDir, "qa-answers.md")
	if info, err := os.Stat(current); err == nil && info.Mode().IsRegular() {
		appendPath(current)
	}
	attemptDirs, _ := filepath.Glob(filepath.Join(artifactDir, "attempt-*"))
	for _, attemptDir := range attemptDirs {
		for _, name := range []string{"qa-answers.md", designHumanDirectionQAFile} {
			path := filepath.Join(attemptDir, name)
			if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
				appendPath(path)
			}
		}
	}
	return out
}

// RecordDesignHumanDirection persists review-gate feedback as a binding Q&A
// source so the next Design attempt receives it through the decision ledger.
func RecordDesignHumanDirection(attemptDir, feedback string) (string, error) {
	feedback = strings.TrimSpace(feedback)
	if feedback == "" {
		return "", fmt.Errorf("recording Design human direction: feedback is empty")
	}
	var b strings.Builder
	b.WriteString("# User Q&A — Human Design Review\n\n")
	b.WriteString("## Q: Which direction did the human give after reviewing this Design attempt?\n\n")
	fmt.Fprintf(&b, "**A:** %s\n", feedback)

	path := filepath.Join(attemptDir, designHumanDirectionQAFile)
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return "", fmt.Errorf("recording Design human direction: %w", err)
	}
	return path, nil
}

func archiveDesignAttempt(designPath, attemptDir string) (string, error) {
	data, err := os.ReadFile(designPath)
	if err != nil {
		return "", err
	}
	path := filepath.Join(attemptDir, "design.md")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

type designReviewDispositionResult struct {
	RequiresHuman bool
	Reason        string
}

// designReviewDisposition prevents critic preferences or unresolved material
// choices from entering the autonomous reviser. Only decision-referenced,
// observable contract defects and grounding errors may revise automatically.
func designReviewDisposition(results []ValidatorResult, decisionIDs map[string]struct{}) designReviewDispositionResult {
	for _, result := range results {
		if result.Status != ReviewChangesRequested {
			continue
		}
		findings := strings.TrimSpace(extractMarkdownSection(result.Feedback, "## Findings"))
		if findings == "" || findings == "- (none)" {
			return designReviewDispositionResult{
				RequiresHuman: true,
				Reason:        fmt.Sprintf("%s critic requested changes without a classified blocking finding", result.Domain),
			}
		}
		foundBlocking := false
		for _, line := range strings.Split(findings, "\n") {
			trimmed := strings.TrimSpace(line)
			var body string
			switch {
			case strings.HasPrefix(trimmed, "- **Critical**:"):
				body = strings.TrimSpace(strings.TrimPrefix(trimmed, "- **Critical**:"))
			case strings.HasPrefix(trimmed, "- **High**:"):
				body = strings.TrimSpace(strings.TrimPrefix(trimmed, "- **High**:"))
			default:
				continue
			}
			foundBlocking = true
			class, rest, ok := cutBracketToken(body)
			if !ok {
				return designReviewDispositionResult{
					RequiresHuman: true,
					Reason:        fmt.Sprintf("%s critic emitted an unclassified blocking finding", result.Domain),
				}
			}
			reference, detail, ok := cutBracketToken(rest)
			if !ok || !validDesignDecisionReference(reference) {
				return designReviewDispositionResult{
					RequiresHuman: true,
					Reason:        fmt.Sprintf("%s critic finding must cite a REQ-### or DEC-### decision-ledger ID", result.Domain),
				}
			}
			if _, exists := decisionIDs[reference]; !exists {
				return designReviewDispositionResult{
					RequiresHuman: true,
					Reason:        fmt.Sprintf("%s critic cited %s, which is not present in the decision ledger", result.Domain, reference),
				}
			}
			if !strings.Contains(detail, "Observable failure:") {
				return designReviewDispositionResult{
					RequiresHuman: true,
					Reason:        fmt.Sprintf("%s critic finding for %s must state an Observable failure", result.Domain, reference),
				}
			}
			switch class {
			case "CONTRACT_DEFECT", "GROUNDING_ERROR":
				// Safe for autonomous revision when the remaining contract is
				// satisfied.
			case "DECISION_CONFLICT", "MISSING_DECISION":
				return designReviewDispositionResult{
					RequiresHuman: true,
					Reason:        fmt.Sprintf("%s critic reported %s for %s", result.Domain, class, reference),
				}
			default:
				return designReviewDispositionResult{
					RequiresHuman: true,
					Reason:        fmt.Sprintf("%s critic emitted unknown finding classification %q", result.Domain, class),
				}
			}
		}
		if !foundBlocking {
			return designReviewDispositionResult{
				RequiresHuman: true,
				Reason:        fmt.Sprintf("%s critic requested changes without a Critical or High finding", result.Domain),
			}
		}
	}
	return designReviewDispositionResult{}
}

func readDesignDecisionIDs(path string) (map[string]struct{}, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading Design decision ledger IDs: %w", err)
	}
	ids := make(map[string]struct{})
	for _, line := range strings.Split(string(data), "\n") {
		heading, ok := strings.CutPrefix(strings.TrimSpace(line), "### ")
		if !ok {
			continue
		}
		id, _, _ := strings.Cut(heading, " ")
		if validDesignDecisionReference(id) {
			ids[id] = struct{}{}
		}
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("Design decision ledger %s contains no requirement or decision IDs", path)
	}
	return ids, nil
}

func cutBracketToken(s string) (token, rest string, ok bool) {
	if !strings.HasPrefix(s, "[") {
		return "", s, false
	}
	end := strings.IndexByte(s, ']')
	if end <= 1 {
		return "", s, false
	}
	return s[1:end], strings.TrimSpace(s[end+1:]), true
}

func validDesignDecisionReference(reference string) bool {
	if len(reference) != 7 || reference[3] != '-' {
		return false
	}
	if reference[:3] != "REQ" && reference[:3] != "DEC" {
		return false
	}
	for _, ch := range reference[4:] {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}
