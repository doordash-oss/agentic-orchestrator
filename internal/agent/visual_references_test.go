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
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent/roles"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

func TestResolveVisualReferences_MergesFeatureImagesAndApprovedMockups(t *testing.T) {
	designRoot := t.TempDir()
	designPath := filepath.Join(designRoot, "design.md")
	writeVisualReferenceTestFile(t, designPath, "# Design")
	writeVisualReferenceTestFile(t, filepath.Join(designRoot, "mockups", "index.html"), "<html>checkout</html>")
	writeVisualReferenceTestPNG(t, filepath.Join(designRoot, "mockups", "states", "checkout.png"), color.RGBA{R: 0xff, A: 0xff})
	writeVisualReferenceTestFile(t, filepath.Join(designRoot, "mockups", "manifest.yaml"), `schema_version: 1
design_artifact: ../design.md
html: index.html
responsive_expectations: [Responsive layout remains usable.]
binding_decisions: [Primary action remains visible.]
illustrative_details: []
states:
  - id: checkout
    title: Checkout
    source: index.html#state=checkout
    png: states/checkout.png
    viewport:
      width: 2
      height: 2
      device_scale_factor: 1
    design_sections: [User Experience, Contracts]
    description: Approved checkout state.
`)

	got, err := ResolveVisualReferences(
		&feature.Feature{Images: []string{"/tmp/user-reference.png"}},
		designPath,
	)
	if err != nil {
		t.Fatalf("ResolveVisualReferences() error = %v", err)
	}
	if !slices.Equal(got.Images, []string{"/tmp/user-reference.png"}) {
		t.Fatalf("ResolveVisualReferences().Images = %v", got.Images)
	}
	if len(got.Mockups) != 1 {
		t.Fatalf("ResolveVisualReferences().Mockups = %v, want one mockup", got.Mockups)
	}
	mockup := got.Mockups[0]
	if mockup.ID != "checkout" ||
		mockup.HTMLPath != filepath.Join(designRoot, "mockups", "index.html")+"#state=checkout" ||
		mockup.PNGPath != filepath.Join(designRoot, "mockups", "states", "checkout.png") {
		t.Errorf("ResolveVisualReferences().Mockups[0] = %+v", mockup)
	}
}

func TestResolveVisualReferences_MissingManifestPreservesFeatureImages(t *testing.T) {
	images := []string{"/tmp/one.png", "/tmp/two.png"}
	got, err := ResolveVisualReferences(
		&feature.Feature{Images: images},
		filepath.Join(t.TempDir(), "design.md"),
	)
	if err != nil {
		t.Fatalf("ResolveVisualReferences() error = %v", err)
	}
	if !slices.Equal(got.Images, images) || len(got.Mockups) != 0 {
		t.Errorf("ResolveVisualReferences() = %+v, want only original images", got)
	}
}

func TestResolveVisualReferences_RejectsInvalidManifestArtifacts(t *testing.T) {
	tests := []struct {
		name       string
		manifest   string
		beforeLoad func(t *testing.T, designRoot string)
		wantError  string
	}{
		{
			name: "unsupported version",
			manifest: `schema_version: 2
design_artifact: ../design.md
html: index.html
states:
  - id: checkout
    source: index.html#state=checkout
    png: states/checkout.png
`,
			wantError: "schema_version 2",
		},
		{
			name: "missing required id",
			manifest: `schema_version: 1
design_artifact: ../design.md
html: index.html
states:
  - source: index.html#state=checkout
    png: states/checkout.png
`,
			wantError: "id is required",
		},
		{
			name: "missing required html",
			manifest: `schema_version: 1
design_artifact: ../design.md
states:
  - id: checkout
    source: index.html#state=checkout
    png: states/checkout.png
`,
			wantError: "has no html",
		},
		{
			name: "missing required design artifact",
			manifest: `schema_version: 1
html: index.html
states:
  - id: checkout
    source: index.html#state=checkout
    png: states/checkout.png
`,
			wantError: "has no design_artifact",
		},
		{
			name: "missing required states",
			manifest: `schema_version: 1
design_artifact: ../design.md
html: index.html
`,
			wantError: "has no states",
		},
		{
			name: "missing required png",
			manifest: `schema_version: 1
design_artifact: ../design.md
html: index.html
states:
  - id: checkout
    source: index.html#state=checkout
`,
			wantError: "png is required",
		},
		{
			name: "missing required source",
			manifest: `schema_version: 1
design_artifact: ../design.md
html: index.html
states:
  - id: checkout
    png: states/checkout.png
`,
			wantError: "source is required",
		},
		{
			name: "duplicate ids",
			manifest: `schema_version: 1
design_artifact: ../design.md
html: index.html
states:
  - id: checkout
    source: index.html#state=checkout
    png: states/checkout.png
  - id: checkout
    source: index.html#state=confirmation
    png: states/confirmation.png
`,
			wantError: `duplicate mockup id "checkout"`,
		},
		{
			name: "source requires fragment",
			manifest: `schema_version: 1
design_artifact: ../design.md
html: index.html
states:
  - id: checkout
    source: index.html
    png: states/checkout.png
`,
			wantError: "requires a fragment",
		},
		{
			name: "duplicate source paths",
			manifest: `schema_version: 1
design_artifact: ../design.md
html: index.html
states:
  - id: checkout
    source: index.html#state=checkout
    png: states/checkout.png
  - id: confirmation
    source: index.html#state=checkout
    png: states/confirmation.png
`,
			wantError: "duplicate source path",
		},
		{
			name: "duplicate png paths",
			manifest: `schema_version: 1
design_artifact: ../design.md
html: index.html
states:
  - id: checkout
    source: index.html#state=checkout
    png: states/checkout.png
  - id: confirmation
    source: index.html#state=confirmation
    png: states/checkout.png
`,
			wantError: "duplicate png path",
		},
		{
			name: "path escapes design root",
			manifest: `schema_version: 1
design_artifact: ../design.md
html: index.html
states:
  - id: checkout
    source: ../../outside.html#state=checkout
    png: states/checkout.png
`,
			wantError: "escapes the Design artifact root",
		},
		{
			name: "absolute path",
			manifest: `schema_version: 1
design_artifact: ../design.md
html: index.html
states:
  - id: checkout
    source: index.html#state=checkout
    png: /tmp/outside.png
`,
			wantError: "must be relative",
		},
		{
			name: "missing file",
			manifest: `schema_version: 1
design_artifact: ../design.md
html: missing.html
states:
  - id: checkout
    source: missing.html#state=checkout
    png: states/checkout.png
`,
			wantError: "no such file",
		},
		{
			name: "empty file",
			manifest: `schema_version: 1
design_artifact: ../design.md
html: index.html
states:
  - id: checkout
    source: index.html#state=checkout
    png: states/empty.png
`,
			beforeLoad: func(t *testing.T, designRoot string) {
				writeVisualReferenceTestFile(t, filepath.Join(designRoot, "mockups", "states", "empty.png"), "")
			},
			wantError: "is empty",
		},
		{
			name: "wrong extension",
			manifest: `schema_version: 1
design_artifact: ../design.md
html: index.txt
states:
  - id: checkout
    source: index.txt#state=checkout
    png: states/checkout.png
`,
			wantError: "must use the .html extension",
		},
		{
			name: "missing visual authority metadata",
			manifest: `schema_version: 1
design_artifact: ../design.md
html: index.html
states:
  - id: checkout
    title: Checkout
    source: index.html#checkout
    png: states/checkout.png
    viewport:
      width: 2
      height: 2
      device_scale_factor: 1
    design_sections: [User Experience]
    description: Checkout state.
`,
			wantError: "responsive_expectations",
		},
		{
			name: "render dimensions disagree with viewport",
			manifest: `schema_version: 1
design_artifact: ../design.md
html: index.html
responsive_expectations: [Responsive layout remains usable.]
binding_decisions: [Primary action remains visible.]
illustrative_details: []
states:
  - id: checkout
    title: Checkout
    source: index.html#checkout
    png: states/checkout.png
    viewport:
      width: 4
      height: 4
      device_scale_factor: 1
    design_sections: [User Experience]
    description: Checkout state.
`,
			wantError: "PNG dimensions are 2x2; want 4x4",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			designRoot := t.TempDir()
			writeVisualReferenceTestFile(t, filepath.Join(designRoot, "design.md"), "# Design")
			writeVisualReferenceTestFile(t, filepath.Join(designRoot, "mockups", "index.html"), "<html>checkout</html>")
			writeVisualReferenceTestPNG(t, filepath.Join(designRoot, "mockups", "states", "checkout.png"), color.RGBA{R: 0xff, A: 0xff})
			writeVisualReferenceTestPNG(t, filepath.Join(designRoot, "mockups", "states", "confirmation.png"), color.RGBA{G: 0xff, A: 0xff})
			writeVisualReferenceTestFile(t, filepath.Join(designRoot, "mockups", "manifest.yaml"), tt.manifest)
			if tt.beforeLoad != nil {
				tt.beforeLoad(t, designRoot)
			}

			_, err := ResolveVisualReferences(nil, filepath.Join(designRoot, "design.md"))
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("ResolveVisualReferences() error = %v, want containing %q", err, tt.wantError)
			}
		})
	}
}

func TestResolvedVisualReferences_PropagateAcrossDownstreamPromptSurfaces(t *testing.T) {
	references := ResolvedVisualReferences{
		Images: []string{"/tmp/user.png"},
		Mockups: []ApprovedMockup{{
			ID:       "checkout",
			HTMLPath: "/design/mockups/checkout.html",
			PNGPath:  "/design/mockups/checkout.png",
		}},
	}

	promptsByCaller := map[string]string{
		"roadmap": roles.BuildRoadmapPrompt(roles.RoadmapUserInput{
			DesignArtifactPath: "/design/design.md",
			VisualReferences:   references.PromptInput("producing the roadmap"),
		}),
		"phase plan": roles.BuildPhasePlanPrompt(roles.PhasePlanUserInput{
			VisualReferences: references.PromptInput("planning this phase"),
		}),
		"implementation": roles.BuildImplementPrompt(roles.ImplementUserInput{
			VisualReferences: references.PromptInput("implementing this iteration"),
		}),
		"implementation review": roles.BuildImplementationReviewAxisPrompt(roles.ImplementationReviewAxisUserInput{
			ReviewUserInput: roles.ReviewUserInput{
				VisualReferences: references.PromptInput("reviewing this iteration"),
			},
			AxisLabel: "Visual fidelity",
		}),
		"final review": roles.BuildImplementationReviewAxisPrompt(roles.ImplementationReviewAxisUserInput{
			ReviewUserInput: roles.ReviewUserInput{
				VisualReferences: references.PromptInput("conducting this final review"),
				FinalGate:        true,
			},
			AxisLabel: "Final visual fidelity",
		}),
	}

	for caller, prompt := range promptsByCaller {
		t.Run(caller, func(t *testing.T) {
			for _, want := range []string{
				"/tmp/user.png",
				"checkout",
				"/design/mockups/checkout.html",
				"/design/mockups/checkout.png",
			} {
				if !strings.Contains(prompt, want) {
					t.Errorf("%s prompt missing %q:\n%s", caller, want, prompt)
				}
			}
		})
	}
}

func writeVisualReferenceTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
}

func writeVisualReferenceTestPNG(t *testing.T, path string, fill color.Color) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", filepath.Dir(path), err)
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("Create(%q): %v", path, err)
	}
	canvas := image.NewRGBA(image.Rect(0, 0, 2, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 2; x++ {
			canvas.Set(x, y, fill)
		}
	}
	if err := png.Encode(file, canvas); err != nil {
		_ = file.Close()
		t.Fatalf("png.Encode(%q): %v", path, err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close(%q): %v", path, err)
	}
}

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

func TestBuildRoadmapPrompt_CarriesVisualReferencesWhenDesignDocPresent(t *testing.T) {
	f := &feature.Feature{
		Name:        "X",
		Description: "do a thing",
		Images:      []string{"/tmp/mockup.png"},
	}
	prompt := BuildRoadmapPrompt(f, "", "", "/tmp/design.md", nil)
	if !strings.Contains(prompt, "Visual References") {
		t.Errorf("roadmap prompt missing Visual References when a design doc is present:\n%s", prompt)
	}
	if !strings.Contains(prompt, "/tmp/mockup.png") {
		t.Errorf("roadmap prompt missing mockup path when a design doc is present:\n%s", prompt)
	}
}

func TestBuildRoadmapPrompt_NoImagesNoBlock(t *testing.T) {
	f := &feature.Feature{Name: "X", Description: "do a thing"}
	prompt := BuildRoadmapPrompt(f, "", "", "", nil)
	if strings.Contains(prompt, "Visual References") {
		t.Errorf("roadmap prompt unexpectedly carries Visual References for image-less feature")
	}
}

func TestBuildPhasePlanPrompt_CarriesVisualReferences(t *testing.T) {
	f := &feature.Feature{
		Name:        "X",
		Description: "build a screen",
		Images:      []string{"/tmp/screen.png"},
	}
	prompt := BuildPhasePlanPrompt(f, "", "", "/tmp/roadmap.md", RoadmapPhase{Number: 1, Type: "tracer-bullet", Name: "Setup"}, nil)
	if !strings.Contains(prompt, "Visual References") {
		t.Errorf("phase plan prompt missing Visual References:\n%s", prompt)
	}
	if !strings.Contains(prompt, "/tmp/screen.png") {
		t.Errorf("phase plan prompt missing visual reference path:\n%s", prompt)
	}
}

func TestBuildPhasePlanRevisionPrompt_CarriesVisualReferences(t *testing.T) {
	f := &feature.Feature{
		Name:   "X",
		Images: []string{"/tmp/screen.png"},
	}
	prompt := BuildPhasePlanRevisionPrompt(f, "", "/tmp/prev.md", "feedback", "", RoadmapPhase{Number: 1, Type: "tdd-fill-in", Name: "Fill"}, 2, nil)
	if !strings.Contains(prompt, "Visual References") {
		t.Errorf("phase plan revision prompt missing Visual References:\n%s", prompt)
	}
	if !strings.Contains(prompt, "/tmp/screen.png") {
		t.Errorf("phase plan revision prompt missing visual reference path:\n%s", prompt)
	}
}

func TestBuildRoadmapRevisionPrompt_CarriesVisualReferences(t *testing.T) {
	f := &feature.Feature{
		Name:   "X",
		Images: []string{"/tmp/screen.png"},
	}
	prompt := BuildRoadmapRevisionPrompt(f, "", "/tmp/r.md", "/tmp/p.md", "feedback", "", 2, nil)
	if !strings.Contains(prompt, "Visual References") {
		t.Errorf("roadmap revision prompt missing Visual References:\n%s", prompt)
	}
	if !strings.Contains(prompt, "/tmp/screen.png") {
		t.Errorf("roadmap revision prompt missing image path:\n%s", prompt)
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
