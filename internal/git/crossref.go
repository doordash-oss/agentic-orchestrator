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
	"path"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/github"
)

// CrossRefEntry describes one repo's PR status for cross-reference rendering.
type CrossRefEntry struct {
	RepoName string
	Branch   string
	PRURL    string // empty = pending, "(failed)" = failed repo
}

// CrossRefSectionHeader is the markdown header for the cross-reference section.
const CrossRefSectionHeader = "## Related PRs"

// BuildCrossReferenceSection builds a markdown table of related PRs for a multi-repo feature.
// Returns empty string if there are fewer than 2 entries (no cross-refs for single-repo).
func BuildCrossReferenceSection(featureName string, entries []CrossRefEntry) string {
	if len(entries) <= 1 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString(CrossRefSectionHeader)
	sb.WriteString("\n\n")
	fmt.Fprintf(&sb, "This PR is part of the multi-repo feature **\"%s\"**.", featureName)
	sb.WriteString("\n\n")
	sb.WriteString("| Repository | Branch | PR |\n")
	sb.WriteString("|------------|--------|----|")

	for _, e := range entries {
		fmt.Fprintf(&sb, "\n| %s | %s | %s |", e.RepoName, e.Branch, crossRefPRCell(e.PRURL))
	}

	return sb.String()
}

// crossRefPRCell renders the PR column of a cross-reference row from an entry
// URL: pending, failed, or a link carrying the PR number when the URL shape
// allows extracting one.
func crossRefPRCell(prURL string) string {
	switch prURL {
	case "":
		return "_(pending)_"
	case "(failed)":
		return "_(failed)_"
	}
	num := path.Base(prURL)
	if num != "" && num != "." && num != "/" {
		return fmt.Sprintf("[#%s](%s)", num, prURL)
	}
	return fmt.Sprintf("[PR](%s)", prURL)
}

// BuildLayerCrossReferenceSection builds the related-PRs section for one stack
// layer: entries are that layer's per-repository entries across the feature,
// and currentRepoName is the repository whose PR body the section is rendered
// into. Only sibling repositories with a published PR for the layer are linked;
// the current repository and repositories whose layer has no PR (pending or
// failed) are omitted. Returns empty when no sibling PR exists for the layer.
func BuildLayerCrossReferenceSection(featureName string, entries []CrossRefEntry, currentRepoName string) string {
	var siblings []CrossRefEntry
	for _, e := range entries {
		if e.RepoName == currentRepoName || e.PRURL == "" || e.PRURL == "(failed)" {
			continue
		}
		siblings = append(siblings, e)
	}
	if len(siblings) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString(CrossRefSectionHeader)
	sb.WriteString("\n\n")
	fmt.Fprintf(&sb, "This PR is part of the multi-repo feature **\"%s\"**.", featureName)
	sb.WriteString("\n\n")
	sb.WriteString("| Repository | Branch | PR |\n")
	sb.WriteString("|------------|--------|----|")
	for _, e := range siblings {
		fmt.Fprintf(&sb, "\n| %s | %s | %s |", e.RepoName, e.Branch, crossRefPRCell(e.PRURL))
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
	if strings.Contains(body, CrossRefSectionHeader) {
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
	return extractSectionByHeader(body, CrossRefSectionHeader)
}

// extractSectionByHeader extracts the section starting at the first occurrence
// of header in body, including the header and trimmed of trailing whitespace.
// The section ends at the next markdown H2 header, the PR signature, or the end
// of the body, whichever comes first. Returns empty string when the header is
// absent.
func extractSectionByHeader(body, header string) string {
	idx := strings.Index(body, header)
	if idx < 0 {
		return ""
	}

	rest := body[idx:]

	// Find the end boundary: next "## " header, PRSignature, or end of body.
	endIdx := len(rest)

	// Look for next markdown H2 header after the current one.
	afterHeader := rest[len(header):]
	nextH2 := strings.Index(afterHeader, "\n## ")
	if nextH2 >= 0 {
		candidate := len(header) + nextH2
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
	return removeSectionByHeader(body, CrossRefSectionHeader)
}

// removeSectionByHeader removes the section starting at the first occurrence of
// header in body, using the same end boundaries as extractSectionByHeader, and
// collapses the leftover whitespace so at most two consecutive newlines remain.
func removeSectionByHeader(body, header string) string {
	idx := strings.Index(body, header)
	if idx < 0 {
		return body
	}

	rest := body[idx:]

	// Find the end boundary (same logic as extractSectionByHeader).
	endIdx := len(rest)

	afterHeader := rest[len(header):]
	nextH2 := strings.Index(afterHeader, "\n## ")
	if nextH2 >= 0 {
		candidate := len(header) + nextH2
		if candidate < endIdx {
			endIdx = candidate
		}
	}

	sigIdx := strings.Index(rest, PRSignature)
	if sigIdx >= 0 && sigIdx < endIdx {
		endIdx = sigIdx
	}

	result := body[:idx] + body[idx+endIdx:]

	// Collapse multiple consecutive newlines to at most 2.
	for strings.Contains(result, "\n\n\n") {
		result = strings.ReplaceAll(result, "\n\n\n", "\n\n")
	}

	return result
}

// UpdatePRBody updates the body of a GitHub PR by URL.
func UpdatePRBody(prURL, newBody string) error {
	owner, repo, number, err := ParsePRURL(prURL)
	if err != nil {
		return err
	}
	client, err := github.ForHost(prURLHost(prURL))
	if err != nil {
		return err
	}
	if err := client.UpdatePRBody(owner, repo, number, newBody); err != nil {
		return fmt.Errorf("editing PR: %w", err)
	}
	return nil
}

// GetPRBody fetches the body of a GitHub PR by URL.
func GetPRBody(prURL string) (string, error) {
	owner, repo, number, err := ParsePRURL(prURL)
	if err != nil {
		return "", err
	}
	client, err := github.ForHost(prURLHost(prURL))
	if err != nil {
		return "", err
	}
	info, err := client.GetPR(owner, repo, number)
	if err != nil {
		return "", fmt.Errorf("fetching PR body: %w", err)
	}
	return strings.TrimSpace(info.Body), nil
}

// RetroactivelyUpdateCrossRefs updates the cross-reference sections in all
// related PRs (except the current repo's PR). Errors are collected and returned
// rather than aborting on the first failure.
func RetroactivelyUpdateCrossRefs(featureName string, entries []CrossRefEntry, currentRepoName string) []error {
	section := BuildCrossReferenceSection(featureName, entries)
	if section == "" {
		return nil
	}

	var prURLs []string
	for _, entry := range entries {
		if entry.PRURL == "" || entry.PRURL == "(failed)" || entry.RepoName == currentRepoName {
			continue
		}
		prURLs = append(prURLs, entry.PRURL)
	}
	return UpdatePRBodiesWithSection(prURLs, section)
}

// UpdatePRBodiesWithSection injects a rendered related-PRs section into every
// given PR body through the remote PR-body read/update operations. Bodies the
// injection leaves unchanged are not written back. Errors are collected per PR
// rather than aborting the set.
func UpdatePRBodiesWithSection(prURLs []string, section string) []error {
	if section == "" {
		return nil
	}
	return updatePRBodies(prURLs, func(body string) string {
		return InjectCrossReferenceSection(body, section)
	})
}

// updatePRBodies reads each PR body, applies transform, and writes the result
// back with UpdatePRBody. A transform that leaves the body unchanged produces
// no write. Placeholder URLs ("(failed)") are skipped. Errors are collected per
// PR rather than aborting the set.
func updatePRBodies(prURLs []string, transform func(body string) string) []error {
	var errs []error
	for _, prURL := range prURLs {
		if prURL == "" || prURL == "(failed)" {
			continue
		}

		body, err := GetPRBody(prURL)
		if err != nil {
			errs = append(errs, fmt.Errorf("PR %s: %w", prURL, err))
			continue
		}

		updated := transform(body)
		if updated == body {
			continue
		}

		if err := UpdatePRBody(prURL, updated); err != nil {
			errs = append(errs, fmt.Errorf("PR %s: %w", prURL, err))
		}
	}
	return errs
}
