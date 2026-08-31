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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

func TestResolvePromptIntent(t *testing.T) {
	f := &feature.Feature{
		Description:  "raw description",
		ExitCriteria: "raw criteria",
		Artifacts:    map[string]string{},
	}

	got := resolvePromptIntent(f)
	if got.Description != "raw description" || got.ExitCriteria != "raw criteria" || got.AcceptanceClause != "" {
		t.Fatalf("no artifacts: want raw intent, got %+v", got)
	}

	roadmapPath := writeArtifact(t, "roadmap.md", "# Roadmap\n\n## Overall Exit Criteria\n\ndone.\n")
	f.Artifacts["roadmap"] = roadmapPath
	got = resolvePromptIntent(f)
	if got.Description != "" || got.ExitCriteria != "" {
		t.Fatalf("roadmap present: raw intent must not flow, got %+v", got)
	}
	if !strings.Contains(got.AcceptanceClause, "## Overall Exit Criteria") ||
		!strings.Contains(got.AcceptanceClause, roadmapPath) {
		t.Fatalf("roadmap present: clause must cite the roadmap section, got %q", got.AcceptanceClause)
	}

	designPath := writeArtifact(t, "design.md", "# Design\n\n## Acceptance Criteria\n\ndone.\n")
	f.Artifacts[feature.DesignArtifactKey] = designPath
	got = resolvePromptIntent(f)
	if got.Description != "" || got.ExitCriteria != "" {
		t.Fatalf("design present: raw intent must not flow, got %+v", got)
	}
	if !strings.Contains(got.AcceptanceClause, "## Acceptance Criteria") ||
		!strings.Contains(got.AcceptanceClause, designPath) {
		t.Fatalf("design present: clause must cite the design section, got %q", got.AcceptanceClause)
	}

	if got := resolvePromptIntent(nil); got != (promptIntent{}) {
		t.Fatalf("nil feature: want zero value, got %+v", got)
	}
}

// writeArtifact writes content to a file named name under a fresh temp dir
// and returns its path.
func writeArtifact(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write artifact %s: %v", name, err)
	}
	return path
}

func TestResolvePromptIntentMissingHeadingFallsBackToRaw(t *testing.T) {
	base := &feature.Feature{
		Description:  "raw description",
		ExitCriteria: "raw criteria",
	}

	// (a) design file with the heading -> design clause.
	f := *base
	f.Artifacts = map[string]string{
		feature.DesignArtifactKey: writeArtifact(t, "design.md", "## Acceptance Criteria\ntext\n"),
	}
	got := resolvePromptIntent(&f)
	if got.ExitCriteria != "" || !strings.Contains(got.AcceptanceClause, "## Acceptance Criteria") {
		t.Fatalf("design with heading: want design clause, got %+v", got)
	}

	// (b) design without heading but roadmap with heading -> roadmap clause.
	f = *base
	f.Artifacts = map[string]string{
		feature.DesignArtifactKey: writeArtifact(t, "design.md", "# no heading here\n"),
		"roadmap":                 writeArtifact(t, "roadmap.md", "## Overall Exit Criteria\ntext\n"),
	}
	got = resolvePromptIntent(&f)
	if got.ExitCriteria != "" || !strings.Contains(got.AcceptanceClause, "## Overall Exit Criteria") {
		t.Fatalf("design missing heading, roadmap has it: want roadmap clause, got %+v", got)
	}

	// (c) neither heading present -> raw fallback.
	f = *base
	f.Artifacts = map[string]string{
		feature.DesignArtifactKey: writeArtifact(t, "design.md", "# no heading\n"),
		"roadmap":                 writeArtifact(t, "roadmap.md", "# no heading either\n"),
	}
	got = resolvePromptIntent(&f)
	if got.AcceptanceClause != "" || got.Description != "raw description" || got.ExitCriteria != "raw criteria" {
		t.Fatalf("neither heading present: want raw fallback, got %+v", got)
	}

	// (d) nonexistent paths -> raw fallback.
	f = *base
	f.Artifacts = map[string]string{
		feature.DesignArtifactKey: filepath.Join(t.TempDir(), "missing-design.md"),
		"roadmap":                 filepath.Join(t.TempDir(), "missing-roadmap.md"),
	}
	got = resolvePromptIntent(&f)
	if got.AcceptanceClause != "" || got.Description != "raw description" || got.ExitCriteria != "raw criteria" {
		t.Fatalf("nonexistent paths: want raw fallback, got %+v", got)
	}
}

func TestBuildImplementPromptOmitsEmptyExitCriteria(t *testing.T) {
	without := BuildImplementPrompt("/plan.md", "", "", "", 1)
	if strings.Contains(without, "**Exit criteria**") {
		t.Fatalf("empty exit criteria must not render a header:\n%s", without)
	}
	with := BuildImplementPrompt("/plan.md", "cycle-scoped criteria", "", "", 1)
	if !strings.Contains(with, "**Exit criteria**: cycle-scoped criteria") {
		t.Fatalf("non-empty exit criteria must render:\n%s", with)
	}
}

func TestFinalFixPromptCitesAcceptanceAuthority(t *testing.T) {
	prompt := BuildFinalFixPrompt(FinalFixPromptOpts{
		Feedback:         "feedback body",
		FeedbackPath:     "/iter/review-feedback.md",
		AcceptanceClause: "Feature acceptance is defined by the `## Acceptance Criteria` section of the approved design at /state/design.md. Judge acceptance against that section, not against any restatement of it.",
		Iteration:        1,
		Publishable:      true,
	})
	if !strings.Contains(prompt, "/state/design.md") {
		t.Fatalf("final fix prompt must cite the acceptance authority:\n%s", prompt)
	}
	if strings.Contains(prompt, "**Exit criteria**") {
		t.Fatalf("clause and raw criteria must not render together:\n%s", prompt)
	}
}

func TestValidatorPromptUsesDistilledIntent(t *testing.T) {
	designPath := writeArtifact(t, "design.md", "## Acceptance Criteria\ntext\n")
	f := &feature.Feature{
		Name:         "sample-feature",
		Description:  "raw description",
		ExitCriteria: "raw criteria",
		Artifacts:    map[string]string{feature.DesignArtifactKey: designPath},
	}
	scopeDomain := validatorDomain{Name: "Scope", Template: "validate-phase-plan-scope"}
	prompt := buildSpecializedValidationPromptForArtifact(f, "/plan.md", "", "", "/feedback.md", scopeDomain, validationArtifactPhasePlan, planValidationExtras{})
	if strings.Contains(prompt, "raw criteria") || strings.Contains(prompt, "raw description") {
		t.Fatalf("design-backed validator prompt must not inline raw intent:\n%s", prompt)
	}
	if !strings.Contains(prompt, designPath) {
		t.Fatalf("design-backed validator prompt must cite the acceptance authority:\n%s", prompt)
	}

	f.Artifacts = map[string]string{}
	prompt = buildSpecializedValidationPromptForArtifact(f, "/plan.md", "", "", "/feedback.md", scopeDomain, validationArtifactPhasePlan, planValidationExtras{})
	if !strings.Contains(prompt, "raw criteria") {
		t.Fatalf("artifact-less validator prompt keeps raw intent:\n%s", prompt)
	}
}

func TestFinalReviewAxisPromptCitesAcceptanceAuthority(t *testing.T) {
	prompt := BuildImplementationReviewAxisPromptWithOpts(ImplementationReviewAxisPromptOpts{
		Gate:             implementationReviewGateFinal,
		AxisLabel:        "qa",
		AcceptanceClause: "Feature acceptance is defined by the `## Acceptance Criteria` section of the approved design at /state/design.md. Judge acceptance against that section, not against any restatement of it.",
		Iteration:        1,
		IterDir:          "/iter",
		FeedbackPath:     "/iter/review-feedback.md",
	})
	if !strings.Contains(prompt, "## Acceptance") ||
		!strings.Contains(prompt, "/state/design.md") {
		t.Fatalf("final axis prompt must cite the acceptance authority:\n%s", prompt)
	}
	if strings.Contains(prompt, "## Exit Criteria") {
		t.Fatalf("clause and raw criteria must not render together:\n%s", prompt)
	}
}
