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
	"sort"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// PhaseScopeResult is the pure output of PhaseScope: the canonical repo set the
// phase implementer should mount, plus a structured validation diagnostic. The
// validation slice is non-nil only on rejected scopes; ScopeOK reports the
// happy path. The result has no I/O side effects beyond reading the plan path
// passed in.
type PhaseScopeResult struct {
	// Repos is the deduplicated, sorted list of repo names declared by the
	// phase plan's `**Repo:** <name>` Task tags. Empty for single-repo
	// features that legitimately omit tags — callers fall back to
	// Feature.Repos[0] in that case.
	Repos []string
	// Tasks is the parsed Tasks section, returned for convenience so callers
	// (testing-contract compiler, implement loop) don't re-parse the plan.
	Tasks []PlanTask
	// Issues lists structural validation problems. Non-empty means the plan
	// is malformed; ScopeOK is false.
	Issues []PhaseScopeIssue
}

// PhaseScopeIssue is a single structural validation problem raised by
// PhaseScope. Callers either render to a human-friendly diagnostic or feed
// into a validator gate.
type PhaseScopeIssue struct {
	Code    string // stable identifier ("repo-not-in-feature", "untagged-task", ...)
	Message string // human-readable detail
	Task    string // task heading the issue belongs to (when applicable)
}

// ScopeOK reports the happy path: zero validation issues. False means the
// plan failed structural validation; the caller must render the issues and
// abort whatever workflow asked for the scope.
func (r PhaseScopeResult) ScopeOK() bool { return len(r.Issues) == 0 }

// PhaseScope computes the per-phase repo set and validates the plan's
// `**Repo:** <name>` tagging structure. This is the unified-flow replacement
// for LoadExecutionPlan + ParseExecutionOrder + ValidateExecutionOrder.
//
// Rules:
//   - Single-repo features (len(feat.Repos) == 1) may omit Task tags entirely;
//     the result's Repos slice contains that one repo regardless of tagging.
//   - Multi-repo features must tag every Task. Untagged tasks in a multi-repo
//     plan produce one "untagged-task" issue per offending task.
//   - Every tag value must reference a repo in feat.Repos; foreign tags
//     produce "repo-not-in-feature" issues.
//   - Empty plans (no Tasks section, no headings) produce a single
//     "no-tasks" issue.
//
// The function is pure: planPath is read once, parsed, validated, and the
// result returned. It does not touch the store or the filesystem beyond that
// read.
func PhaseScope(feat *feature.Feature, planPath string) (PhaseScopeResult, error) {
	if feat == nil {
		return PhaseScopeResult{}, fmt.Errorf("phase scope: feature is nil")
	}
	planText, err := os.ReadFile(planPath)
	if err != nil {
		return PhaseScopeResult{}, fmt.Errorf("phase scope: reading plan: %w", err)
	}
	return PhaseScopeFromText(feat, string(planText)), nil
}

// PhaseScopeFromText is the in-memory variant for tests and callers that
// already have the plan markdown in hand.
func PhaseScopeFromText(feat *feature.Feature, planText string) PhaseScopeResult {
	tasks := ParsePlanTasks(planText)

	known := make(map[string]bool, len(feat.Repos))
	for _, r := range feat.Repos {
		known[r.Name] = true
	}

	multiRepo := len(feat.Repos) > 1

	var issues []PhaseScopeIssue
	if len(tasks) == 0 {
		issues = append(issues, PhaseScopeIssue{
			Code:    "no-tasks",
			Message: "phase plan has no '## Tasks' section or no '### Task N:' headings",
		})
	}

	tagged := make(map[string]bool)
	for _, t := range tasks {
		if t.Repo == "" {
			if multiRepo {
				issues = append(issues, PhaseScopeIssue{
					Code:    "untagged-task",
					Message: "task missing required '**Repo:** <name>' tag (every task in a multi-repo phase must declare its repo)",
					Task:    strings.TrimSpace(t.Heading),
				})
			}
			continue
		}
		if !known[t.Repo] {
			issues = append(issues, PhaseScopeIssue{
				Code:    "repo-not-in-feature",
				Message: fmt.Sprintf("task tagged repo %q is not in Feature.Repos", t.Repo),
				Task:    strings.TrimSpace(t.Heading),
			})
			continue
		}
		tagged[t.Repo] = true
	}

	var repos []string
	if len(tagged) > 0 {
		for name := range tagged {
			repos = append(repos, name)
		}
		sort.Strings(repos)
	} else if !multiRepo && len(feat.Repos) == 1 {
		// Single-repo, untagged plan: scope is the lone repo.
		repos = []string{feat.Repos[0].Name}
	}

	return PhaseScopeResult{
		Repos:  repos,
		Tasks:  tasks,
		Issues: issues,
	}
}

// IssueSummary renders the Issues slice as a human-readable error string.
// Returns "" when the result is OK.
func (r PhaseScopeResult) IssueSummary() string {
	if len(r.Issues) == 0 {
		return ""
	}
	var b strings.Builder
	for i, issue := range r.Issues {
		if i > 0 {
			b.WriteString("; ")
		}
		if issue.Task != "" {
			b.WriteString(issue.Task)
			b.WriteString(": ")
		}
		b.WriteString(issue.Message)
	}
	return b.String()
}
