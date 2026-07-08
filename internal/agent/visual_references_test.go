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

func TestVisualReferencesSection_EmptyImagesNoBlock(t *testing.T) {
	if got := visualReferencesSection(nil, "implementing"); got != "" {
		t.Errorf("visualReferencesSection(nil) = %q, want empty", got)
	}
	if got := visualReferencesSection([]string{}, "implementing"); got != "" {
		t.Errorf("visualReferencesSection([]) = %q, want empty", got)
	}
}

func TestVisualReferencesSection_ImagesEmitsBlockWithPaths(t *testing.T) {
	images := []string{"/tmp/mockup-1.png", "/tmp/mockup-2.png"}
	got := visualReferencesSection(images, "implementing")
	if got == "" {
		t.Fatalf("visualReferencesSection returned empty for non-empty images")
	}
	if !strings.Contains(got, "Visual References") {
		t.Errorf("missing header; got:\n%s", got)
	}
	if !strings.Contains(got, "IMPORTANT") {
		t.Errorf("missing IMPORTANT verb; got:\n%s", got)
	}
	for _, p := range images {
		if !strings.Contains(got, p) {
			t.Errorf("missing path %q in block:\n%s", p, got)
		}
	}
	if !strings.Contains(got, "implementing") {
		t.Errorf("label 'implementing' missing from block:\n%s", got)
	}
}

// TestBuildRoadmapPrompt_EmitsVisualReferences asserts that in Medium
// mode (no design artifact) the roadmap planner still sees
// user-attached mockups — the design doc isn't there to carry them
// forward, so the prompt itself must surface the images.
func TestBuildRoadmapPrompt_EmitsVisualReferences(t *testing.T) {
	f := &feature.Feature{
		Name:        "X",
		Description: "do a thing",
		Images:      []string{"/tmp/mockup.png"},
	}
	prompt := BuildRoadmapPrompt(f, "", "", "", nil)
	if !strings.Contains(prompt, "Visual References") {
		t.Errorf("roadmap prompt missing Visual References block")
	}
	if !strings.Contains(prompt, "/tmp/mockup.png") {
		t.Errorf("roadmap prompt missing mockup path")
	}
}

// TestBuildRoadmapPrompt_SuppressesVisualReferencesWhenDesignDocPresent
// asserts the inverse path: when a design/design artifact exists,
// the design doc is the authoritative spec and already incorporates the
// user's visual references. Re-injecting them in the roadmap prompt
// would create two competing sources of truth, so the prompt suppresses
// the block and points the planner at the design doc instead.
func TestBuildRoadmapPrompt_SuppressesVisualReferencesWhenDesignDocPresent(t *testing.T) {
	f := &feature.Feature{
		Name:        "X",
		Description: "do a thing",
		Images:      []string{"/tmp/mockup.png"},
	}
	prompt := BuildRoadmapPrompt(f, "", "", "/tmp/design.md", nil)
	if strings.Contains(prompt, "Visual References") {
		t.Errorf("roadmap prompt should suppress Visual References when a design doc is present:\n%s", prompt)
	}
	if strings.Contains(prompt, "/tmp/mockup.png") {
		t.Errorf("roadmap prompt should not list mockup paths when a design doc is present:\n%s", prompt)
	}
}

func TestBuildRoadmapPrompt_NoImagesNoBlock(t *testing.T) {
	f := &feature.Feature{Name: "X", Description: "do a thing"}
	prompt := BuildRoadmapPrompt(f, "", "", "", nil)
	if strings.Contains(prompt, "Visual References") {
		t.Errorf("roadmap prompt unexpectedly carries Visual References for image-less feature")
	}
}

// TestBuildPhasePlanPrompt_OmitsVisualReferences pins the deliberate
// decision that the per-phase plan prompt does NOT carry visual
// references. The approved roadmap is the authoritative input at this
// stage and already incorporates the user's mockups; re-injecting them
// here would create two competing sources of truth.
func TestBuildPhasePlanPrompt_OmitsVisualReferences(t *testing.T) {
	f := &feature.Feature{
		Name:        "X",
		Description: "build a screen",
		Images:      []string{"/tmp/screen.png"},
	}
	prompt := BuildPhasePlanPrompt(f, "", "", "/tmp/roadmap.md", RoadmapPhase{Number: 1, Type: "tracer-bullet", Name: "Setup"}, nil)
	if strings.Contains(prompt, "Visual References") {
		t.Errorf("phase plan prompt should NOT carry Visual References, got:\n%s", prompt)
	}
	if strings.Contains(prompt, "/tmp/screen.png") {
		t.Errorf("phase plan prompt should NOT carry mockup paths, got:\n%s", prompt)
	}
}

// TestBuildPhasePlanRevisionPrompt_OmitsVisualReferences pins the
// deliberate decision that the per-phase plan revision prompt does NOT
// carry visual references. A revision is a text-level edit driven by
// critic feedback against the prior plan, not a fresh design pass —
// mockups belong on the create-roadmap surface.
func TestBuildPhasePlanRevisionPrompt_OmitsVisualReferences(t *testing.T) {
	f := &feature.Feature{
		Name:   "X",
		Images: []string{"/tmp/screen.png"},
	}
	prompt := BuildPhasePlanRevisionPrompt(f, "", "/tmp/prev.md", "feedback", "", RoadmapPhase{Number: 1, Type: "tdd-fill-in", Name: "Fill"}, 2, nil)
	if strings.Contains(prompt, "Visual References") {
		t.Errorf("phase plan revision prompt should NOT carry Visual References, got:\n%s", prompt)
	}
	if strings.Contains(prompt, "/tmp/screen.png") {
		t.Errorf("phase plan revision prompt should NOT carry mockup paths, got:\n%s", prompt)
	}
}

// TestBuildRoadmapRevisionPrompt_OmitsVisualReferences pins the deliberate
// decision that the roadmap revision prompt does NOT carry visual references.
// A revision is a text-level edit driven by critic feedback, not a fresh
// design pass; mockups belong on the create-roadmap and per-phase plan
// prompts, not here.
func TestBuildRoadmapRevisionPrompt_OmitsVisualReferences(t *testing.T) {
	f := &feature.Feature{
		Name:   "X",
		Images: []string{"/tmp/screen.png"},
	}
	prompt := BuildRoadmapRevisionPrompt(f, "", "/tmp/r.md", "/tmp/p.md", "feedback", "", 2, nil)
	if strings.Contains(prompt, "Visual References") {
		t.Errorf("roadmap revision prompt should NOT carry Visual References, got:\n%s", prompt)
	}
	if strings.Contains(prompt, "/tmp/screen.png") {
		t.Errorf("roadmap revision prompt should NOT carry image paths, got:\n%s", prompt)
	}
}

func TestBuildFinalFixPrompt_EmitsVisualReferences(t *testing.T) {
	prompt := BuildFinalFixPrompt(FinalFixPromptOpts{
		Feedback:     "fix this",
		ExitCriteria: "tests pass",
		Iteration:    1,
		Images:       []string{"/tmp/mock.png"},
	})
	if !strings.Contains(prompt, "Visual References") {
		t.Errorf("final fix prompt missing Visual References block")
	}
	if !strings.Contains(prompt, "/tmp/mock.png") {
		t.Errorf("final fix prompt missing mockup path")
	}
}
