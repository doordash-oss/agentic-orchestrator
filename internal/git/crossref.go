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
	"fmt"
	"os/exec"
	"path"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// CrossRefEntry aliases the canonical port type. Kept for source
// compatibility with existing git-package callers.
type CrossRefEntry = ports.CrossRefEntry

const crossRefSectionHeader = "## Related PRs"

// BuildCrossReferenceSection builds a markdown table of related PRs for a multi-repo feature.
// Returns empty string if there are fewer than 2 entries (no cross-refs for single-repo).
func BuildCrossReferenceSection(featureName string, entries []CrossRefEntry) string {
	if len(entries) <= 1 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString(crossRefSectionHeader)
	sb.WriteString("\n\n")
	fmt.Fprintf(&sb, "This PR is part of the multi-repo feature **\"%s\"**.", featureName)
	sb.WriteString("\n\n")
	sb.WriteString("| Repository | Branch | PR |\n")
	sb.WriteString("|------------|--------|----|")

	for _, e := range entries {
		prCol := "_(pending)_"
		if e.PRURL == "(failed)" {
			prCol = "_(failed)_"
		} else if e.PRURL != "" {
			// Extract PR number from URL (last segment after "/pull/")
			num := path.Base(e.PRURL)
			if num != "" && num != "." && num != "/" {
				prCol = fmt.Sprintf("[#%s](%s)", num, e.PRURL)
			} else {
				prCol = fmt.Sprintf("[PR](%s)", e.PRURL)
			}
		}
		fmt.Fprintf(&sb, "\n| %s | %s | %s |", e.RepoName, e.Branch, prCol)
	}

	return sb.String()
}

// InjectCrossReferenceSection inserts the cross-reference section into a PR body.
// If the body already contains a cross-reference section, it is replaced.
// The section is placed before the PRSignature if present, otherwise appended.
func InjectCrossReferenceSection(body, section string) string {
	if section == "" {
		return body
	}

	// Remove existing cross-reference section if present.
	if strings.Contains(body, crossRefSectionHeader) {
		body = RemoveCrossReferenceSection(body)
	}

	// Insert before PRSignature if present.
	if strings.Contains(body, PRSignature) {
		idx := strings.Index(body, PRSignature)
		before := body[:idx]
		after := body[idx:]
		return before + "\n\n" + section + after
	}

	// No signature found — append to body.
	if body != "" {
		return body + "\n\n" + section
	}
	return section
}

// ExtractCrossReferenceSection extracts the cross-reference section from a PR body.
// Returns the section including the header, trimmed of trailing whitespace.
// Returns empty string if no cross-reference section is found.
func ExtractCrossReferenceSection(body string) string {
	idx := strings.Index(body, crossRefSectionHeader)
	if idx < 0 {
		return ""
	}

	sectionStart := idx
	rest := body[sectionStart:]

	// Find the end boundary: next "## " header, PRSignature, or end of body.
	endIdx := len(rest)

	// Look for next markdown H2 header after the current one.
	afterHeader := rest[len(crossRefSectionHeader):]
	nextH2 := strings.Index(afterHeader, "\n## ")
	if nextH2 >= 0 {
		candidate := len(crossRefSectionHeader) + nextH2
		if candidate < endIdx {
			endIdx = candidate
		}
	}

	// Look for PRSignature boundary.
	sigIdx := strings.Index(rest, PRSignature)
	if sigIdx >= 0 && sigIdx < endIdx {
		endIdx = sigIdx
	}

	return strings.TrimRight(rest[:endIdx], " \t\n\r")
}

// RemoveCrossReferenceSection removes the cross-reference section from a PR body.
// Cleans up extra whitespace left behind.
func RemoveCrossReferenceSection(body string) string {
	idx := strings.Index(body, crossRefSectionHeader)
	if idx < 0 {
		return body
	}

	sectionStart := idx
	rest := body[sectionStart:]

	// Find the end boundary (same logic as ExtractCrossReferenceSection).
	endIdx := len(rest)

	afterHeader := rest[len(crossRefSectionHeader):]
	nextH2 := strings.Index(afterHeader, "\n## ")
	if nextH2 >= 0 {
		candidate := len(crossRefSectionHeader) + nextH2
		if candidate < endIdx {
			endIdx = candidate
		}
	}

	sigIdx := strings.Index(rest, PRSignature)
	if sigIdx >= 0 && sigIdx < endIdx {
		endIdx = sigIdx
	}

	before := body[:sectionStart]
	after := body[sectionStart+endIdx:]

	result := before + after

	// Collapse multiple consecutive newlines to at most 2.
	for strings.Contains(result, "\n\n\n") {
		result = strings.ReplaceAll(result, "\n\n\n", "\n\n")
	}

	return result
}

// UpdatePRBody updates the body of a GitHub PR by URL using the gh CLI.
func UpdatePRBody(prURL, newBody string) error {
	cmd := exec.Command("gh", "pr", "edit", prURL, "--body", newBody)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("editing PR: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// GetPRBody fetches the body of a GitHub PR by URL using the gh CLI.
func GetPRBody(prURL string) (string, error) {
	cmd := exec.Command("gh", "pr", "view", prURL, "--json", "body", "--jq", ".body")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("fetching PR body: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return strings.TrimSpace(string(out)), nil
}

// RetroactivelyUpdateCrossRefs updates the cross-reference sections in all
// related PRs (except the current repo's PR). Errors are collected and returned
// rather than aborting on the first failure.
func RetroactivelyUpdateCrossRefs(featureName string, entries []CrossRefEntry, currentRepoName string) []error {
	var errs []error

	for _, entry := range entries {
		if entry.PRURL == "" || entry.PRURL == "(failed)" || entry.RepoName == currentRepoName {
			continue
		}

		body, err := GetPRBody(entry.PRURL)
		if err != nil {
			errs = append(errs, fmt.Errorf("repo %s: %w", entry.RepoName, err))
			continue
		}

		section := BuildCrossReferenceSection(featureName, entries)
		updated := InjectCrossReferenceSection(body, section)

		if err := UpdatePRBody(entry.PRURL, updated); err != nil {
			errs = append(errs, fmt.Errorf("repo %s: %w", entry.RepoName, err))
		}
	}

	return errs
}
