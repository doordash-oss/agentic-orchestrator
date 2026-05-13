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

// TestVisualEvidenceImplementSection_NilFeatureNoBlock: safety — never
// panic, emit nothing when the feature pointer is nil.
func TestVisualEvidenceImplementSection_NilFeatureNoBlock(t *testing.T) {
	if got := visualEvidenceImplementSection(nil, "/tmp/iter"); got != "" {
		t.Errorf("visualEvidenceImplementSection(nil, _) = %q, want empty", got)
	}
}

// TestVisualEvidenceImplementSection_EmptyIterDirNoBlock: without a path
// to publish, the block is useless.
func TestVisualEvidenceImplementSection_EmptyIterDirNoBlock(t *testing.T) {
	f := &feature.Feature{Tags: []string{feature.TagFrontend}}
	if got := visualEvidenceImplementSection(f, ""); got != "" {
		t.Errorf("visualEvidenceImplementSection(frontend, \"\") = %q, want empty", got)
	}
}

// TestVisualEvidenceImplementSection_BackendTagNoBlock: the primary
// regression guard for the split — backend features see nothing about
// screenshots in their implement prompts.
func TestVisualEvidenceImplementSection_BackendTagNoBlock(t *testing.T) {
	f := &feature.Feature{Tags: []string{feature.TagBackend}}
	got := visualEvidenceImplementSection(f, "/tmp/iter")
	if got != "" {
		t.Errorf("backend-tagged feature unexpectedly carries screenshots block:\n%s", got)
	}
}

// TestVisualEvidenceImplementSection_UntaggedFeatureNoBlock: features
// predating the classifier (no tags) don't get the block either.
func TestVisualEvidenceImplementSection_UntaggedFeatureNoBlock(t *testing.T) {
	f := &feature.Feature{}
	if got := visualEvidenceImplementSection(f, "/tmp/iter"); got != "" {
		t.Errorf("untagged feature unexpectedly carries screenshots block:\n%s", got)
	}
}

// TestVisualEvidenceImplementSection_FrontendTagEmitsBlock is the other
// half of the regression guard: frontend features must carry the path.
func TestVisualEvidenceImplementSection_FrontendTagEmitsBlock(t *testing.T) {
	f := &feature.Feature{Tags: []string{feature.TagFrontend}}
	iterDir := "/tmp/feat/run-001/phase-01/implement/agentic/iteration-02"
	got := visualEvidenceImplementSection(f, iterDir)
	if got == "" {
		t.Fatalf("frontend-tagged feature got empty block")
	}
	if !strings.Contains(got, "Visual Evidence") {
		t.Errorf("block missing header; got:\n%s", got)
	}
	if !strings.Contains(got, iterDir+"/screenshots/") {
		t.Errorf("block missing screenshots path; got:\n%s", got)
	}
	// The methodology details must NOT be duplicated inline — they live in
	// the frontend-design skill. The block just points there.
	if !strings.Contains(got, "skills/frontend-design/SKILL.md") {
		t.Errorf("block should reference frontend-design skill for methodology; got:\n%s", got)
	}
	// Guard against inline re-publication of the methodology we just moved
	// to the skill. Keep the call-site block thin.
	if strings.Contains(got, "Playwright") || strings.Contains(got, "Puppeteer") {
		t.Errorf("block is duplicating methodology that belongs in the skill; got:\n%s", got)
	}
}

func TestVisualEvidenceReviewSection_NilFeatureNoBlock(t *testing.T) {
	if got := visualEvidenceReviewSection(nil, "/tmp/iter"); got != "" {
		t.Errorf("visualEvidenceReviewSection(nil, _) = %q, want empty", got)
	}
}

func TestVisualEvidenceReviewSection_BackendTagNoBlock(t *testing.T) {
	f := &feature.Feature{Tags: []string{feature.TagBackend}}
	if got := visualEvidenceReviewSection(f, "/tmp/iter"); got != "" {
		t.Errorf("backend-tagged feature unexpectedly carries review screenshots block:\n%s", got)
	}
}

// TestVisualEvidenceReviewSection_FrontendTagGatesOnScreenshots is the
// safety rail that closes the loop: the reviewer must know missing
// screenshots on a UI-touching diff is a CHANGES_REQUESTED finding.
func TestVisualEvidenceReviewSection_FrontendTagGatesOnScreenshots(t *testing.T) {
	f := &feature.Feature{Tags: []string{feature.TagFrontend}}
	iterDir := "/tmp/iter-02"
	got := visualEvidenceReviewSection(f, iterDir)
	if got == "" {
		t.Fatalf("frontend-tagged review got empty block")
	}
	if !strings.Contains(got, "Visual Evidence From This Iteration") {
		t.Errorf("review block missing header; got:\n%s", got)
	}
	if !strings.Contains(got, iterDir+"/screenshots/") {
		t.Errorf("review block missing screenshots path; got:\n%s", got)
	}
	if !strings.Contains(got, "CHANGES_REQUESTED") {
		t.Errorf("review block should encode the approval gate; got:\n%s", got)
	}
	if !strings.Contains(got, "skills/frontend-design/SKILL.md") {
		t.Errorf("review block should reference frontend-design skill; got:\n%s", got)
	}
}

// TestBuildImplementPrompt_NoLongerEmitsScreenshotsBlock confirms the
// builder itself no longer carries the block (it lives at the call site
// now, tag-gated). Builders are called from many test contexts without a
// feature in scope; the block would be noise for those.
func TestBuildImplementPrompt_NoLongerEmitsScreenshotsBlock(t *testing.T) {
	prompt := BuildImplementPrompt(
		"/tmp/plan.md",
		"tests pass",
		"/tmp/progress.md",
		"/tmp/iter/verification-report.yaml",
		"",
		"",
		"",
		"",
		"",
		"",
		nil,
		1,
	)
	if strings.Contains(prompt, "Visual Evidence") {
		t.Errorf("BuildImplementPrompt should no longer carry the Visual Evidence block (moved to call site)")
	}
}

// TestBuildReviewPrompt_SuppressesEvidenceWithoutEvidenceIterDir confirms
// the evidence partials stay dark when the caller passes "" for
// evidenceIterDir — the same gate that keeps backend / untagged features
// from seeing screenshot / behaviors prose. The previous gate was at the
// call site (block prepend); it now lives inline in review.user.tmpl,
// driven by ReviewUserInput.{Behavioral,Visual}Evidence.IterDir.
func TestBuildReviewPrompt_SuppressesEvidenceWithoutEvidenceIterDir(t *testing.T) {
	prompt := BuildReviewPrompt(
		"/tmp/plan.md",
		"tests pass",
		"/tmp/progress.md",
		"/tmp/iter",
		"",
		"/tmp/verification.yaml",
		2,
		nil,
		"",
		"",
		"",
		"",
	)
	if strings.Contains(prompt, "Visual Evidence From This Iteration") {
		t.Errorf("BuildReviewPrompt with evidenceIterDir=\"\" must not render visual evidence block; got:\n%s", prompt)
	}
	if strings.Contains(prompt, "Behavioral Evidence From This Iteration") {
		t.Errorf("BuildReviewPrompt with evidenceIterDir=\"\" must not render behavioral evidence block; got:\n%s", prompt)
	}
}

// TestBuildReviewPrompt_EmitsEvidenceAfterProgress is the positive
// counterpart: when evidenceIterDir is set, both evidence partials must
// render, and they must appear immediately after the Progress section
// (and before Phase Type). This pins the placement the template enforces
// — drift would either move the evidence sections elsewhere or
// reorder them relative to Progress / Phase Type.
func TestBuildReviewPrompt_EmitsEvidenceAfterProgress(t *testing.T) {
	prompt := BuildReviewPrompt(
		"/tmp/plan.md",
		"tests pass",
		"/tmp/progress.md",
		"/tmp/iter-02",
		"",
		"/tmp/verification.yaml",
		2,
		nil,
		"",
		"tdd-fill-in",
		"",
		"/tmp/iter-02",
	)
	progressIdx := strings.Index(prompt, "## Progress")
	behavioralIdx := strings.Index(prompt, "## Behavioral Evidence From This Iteration")
	visualIdx := strings.Index(prompt, "## Visual Evidence From This Iteration")
	phaseIdx := strings.Index(prompt, "## Phase Type")
	if progressIdx < 0 {
		t.Fatalf("expected `## Progress` section in prompt; got:\n%s", prompt)
	}
	if behavioralIdx < 0 || visualIdx < 0 {
		t.Fatalf("expected both evidence sections in prompt; got:\n%s", prompt)
	}
	if phaseIdx < 0 {
		t.Fatalf("expected `## Phase Type` section in prompt; got:\n%s", prompt)
	}
	// Strict ordering: Progress < Behavioral < Visual < PhaseType.
	if !(progressIdx < behavioralIdx && behavioralIdx < visualIdx && visualIdx < phaseIdx) {
		t.Errorf("evidence sections must render between Progress and Phase Type;"+
			" indices: progress=%d behavioral=%d visual=%d phase=%d",
			progressIdx, behavioralIdx, visualIdx, phaseIdx)
	}
	// And the iteration's directories must be published verbatim so the
	// reviewer / agent can navigate to them via tool-use.
	if !strings.Contains(prompt, "/tmp/iter-02/behaviors/") {
		t.Errorf("expected behaviors path published in prompt; got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "/tmp/iter-02/screenshots/") {
		t.Errorf("expected screenshots path published in prompt; got:\n%s", prompt)
	}
}
