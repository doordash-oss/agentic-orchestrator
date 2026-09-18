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

package orchestrator

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// writeOriginRoadmapArtifact writes a two-row `## Pull Requests` roadmap
// artifact and returns its absolute path, the origin child's roadmap shape
// these tests re-read through buildLayerPRContext.
func writeOriginRoadmapArtifact(t *testing.T) string {
	t.Helper()
	content := `# Roadmap

## Phase 1: First slice

### Goal

Ship the first slice.

## Phase 2: Second slice

### Goal

Ship the second slice.

## Pull Requests

| # | Title | Phases | Rationale |
|---|---|---|---|
| 1 | Child first | 1 | Child layer one rationale. |
| 2 | Child second | 2 | Child layer two rationale. |

## Overall Exit Criteria

- Done.
`
	path := filepath.Join(t.TempDir(), "roadmap.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write origin roadmap: %v", err)
	}
	return path
}

// An appended layer's PR context is described from its origin: the child
// feature's name and description, the child roadmap's row at the origin
// position for phases and rationale, and the parent's persisted layer for
// position and title — while the stack view still lists every layer of the
// parent's stack. A roadmap-derived layer keeps the run's own feature
// context and persisted phases.
func TestBuildLayerPRContextAppendedLayerDescribedFromOrigin(t *testing.T) {
	t.Parallel()

	child := &feature.Feature{
		ID:          "feat-origin-child-ctx",
		Name:        "child refactor pass",
		Description: "Refactor the delivery path.",
		Artifacts:   map[string]string{"roadmap": writeOriginRoadmapArtifact(t)},
	}
	parent := &feature.Feature{
		ID:          "feat-origin-parent-ctx",
		Name:        "parent origin walk",
		Description: "Parent delivery.",
		Stack: []feature.StackLayer{
			{Position: 1, Title: "Foundation", Slug: "foundation", Phases: []int{1}, Branch: "feature/p/1-foundation"},
			{Position: 2, Title: "Fix auth", Slug: "fix-auth", Phases: []int{2}, Branch: "feature/p/2-fix-auth"},
			{
				Position: 3, Title: "Child first", Slug: "child-first", Branch: "feature/p/3-child-first",
				Origin: &feature.StackLayerOrigin{SourceFeatureID: child.ID, SourceLayerPosition: 1},
			},
		},
	}
	lc := mocks.NewMockFeatureLifecycle()
	lc.GetFn = func(id string) (*feature.Feature, error) {
		switch id {
		case parent.ID:
			return parent, nil
		case child.ID:
			return child, nil
		}
		return nil, errors.New("feature not found: " + id)
	}
	o := New(Deps{Lifecycle: lc, PhaseRunner: &agent.PhaseRunner{StateDir: t.TempDir()}}, Hooks{})

	prCtx := o.buildLayerPRContext(parent, feature.FeatureRepo{}, parent.Stack[2], "", "")
	if prCtx.FeatureName != child.Name || prCtx.FeatureDescription != child.Description {
		t.Fatalf("feature context = %q/%q, want the origin child's name and description", prCtx.FeatureName, prCtx.FeatureDescription)
	}
	if prCtx.LayerPosition != 3 || prCtx.LayerTitle != "Child first" {
		t.Fatalf("layer identity = %d/%q, want the parent layer's position and title", prCtx.LayerPosition, prCtx.LayerTitle)
	}
	if len(prCtx.LayerPhases) != 1 || prCtx.LayerPhases[0] != 1 {
		t.Fatalf("layer phases = %v, want the origin roadmap row's [1]", prCtx.LayerPhases)
	}
	if prCtx.LayerRationale != "Child layer one rationale." {
		t.Fatalf("layer rationale = %q, want the origin roadmap row's rationale", prCtx.LayerRationale)
	}
	if len(prCtx.Stack) != 3 {
		t.Fatalf("stack view len = %d, want every layer of the parent's stack", len(prCtx.Stack))
	}
	wantBranches := []string{"feature/p/1-foundation", "feature/p/2-fix-auth", "feature/p/3-child-first"}
	for i, want := range wantBranches {
		if prCtx.Stack[i].Position != i+1 || prCtx.Stack[i].Branch != want {
			t.Errorf("stack view[%d] = %+v, want position %d and branch %q", i, prCtx.Stack[i], i+1, want)
		}
	}

	roadCtx := o.buildLayerPRContext(parent, feature.FeatureRepo{}, parent.Stack[0], "", "")
	if roadCtx.FeatureName != parent.Name || roadCtx.FeatureDescription != parent.Description {
		t.Fatalf("roadmap layer feature context = %q/%q, want the parent's", roadCtx.FeatureName, roadCtx.FeatureDescription)
	}
	if len(roadCtx.LayerPhases) != 1 || roadCtx.LayerPhases[0] != 1 {
		t.Fatalf("roadmap layer phases = %v, want the persisted layer's [1]", roadCtx.LayerPhases)
	}
	if roadCtx.LayerRationale != "" {
		t.Fatalf("roadmap layer rationale = %q, want empty without a parent roadmap", roadCtx.LayerRationale)
	}
}

// The origin re-read degrades best-effort: a missing origin feature or an
// unreadable origin roadmap leaves the feature context, phases, and
// rationale empty, a readable roadmap without a row at the origin position
// keeps the loaded origin feature's name with empty phases and rationale,
// and the parent layer's identity and stack view survive every case.
func TestBuildLayerPRContextOriginDegradesBestEffort(t *testing.T) {
	t.Parallel()

	const parentName = "parent degrade"
	newParent := func(origin feature.StackLayerOrigin) *feature.Feature {
		return &feature.Feature{
			ID:   "feat-origin-parent-degrade",
			Name: parentName,
			Stack: []feature.StackLayer{
				{Position: 1, Title: "Foundation", Phases: []int{1}, Branch: "feature/d/1-foundation"},
				{Position: 2, Title: "Child first", Branch: "feature/d/2-child-first", Origin: &origin},
			},
		}
	}

	cases := []struct {
		name          string
		child         *feature.Feature
		origin        feature.StackLayerOrigin
		wantName      string
		wantPhases    []int
		wantRationale string
	}{
		{
			name:          "missing origin feature",
			child:         nil,
			origin:        feature.StackLayerOrigin{SourceFeatureID: "feat-ghost-child", SourceLayerPosition: 1},
			wantName:      "",
			wantPhases:    nil,
			wantRationale: "",
		},
		{
			name:          "origin feature without a readable roadmap",
			child:         &feature.Feature{ID: "feat-child-no-roadmap", Name: "child no roadmap", Description: "No roadmap here."},
			origin:        feature.StackLayerOrigin{SourceFeatureID: "feat-child-no-roadmap", SourceLayerPosition: 1},
			wantName:      "",
			wantPhases:    nil,
			wantRationale: "",
		},
		{
			name:          "readable roadmap without a row at the origin position",
			child:         &feature.Feature{ID: "feat-child-other-row", Name: "child other row", Description: "Row is missing.", Artifacts: map[string]string{"roadmap": writeOriginRoadmapArtifact(t)}},
			origin:        feature.StackLayerOrigin{SourceFeatureID: "feat-child-other-row", SourceLayerPosition: 9},
			wantName:      "child other row",
			wantPhases:    nil,
			wantRationale: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			parent := newParent(tc.origin)
			lc := mocks.NewMockFeatureLifecycle()
			lc.GetFn = func(id string) (*feature.Feature, error) {
				if id == parent.ID {
					return parent, nil
				}
				if tc.child != nil && id == tc.child.ID {
					return tc.child, nil
				}
				return nil, errors.New("feature not found: " + id)
			}
			o := New(Deps{Lifecycle: lc, PhaseRunner: &agent.PhaseRunner{StateDir: t.TempDir()}}, Hooks{})

			prCtx := o.buildLayerPRContext(parent, feature.FeatureRepo{}, parent.Stack[1], "", "")
			if prCtx.FeatureName != tc.wantName {
				t.Errorf("feature name = %q, want %q", prCtx.FeatureName, tc.wantName)
			}
			if prCtx.FeatureDescription != "" && tc.wantName == "" {
				t.Errorf("feature description = %q, want empty alongside the empty name", prCtx.FeatureDescription)
			}
			if len(prCtx.LayerPhases) != len(tc.wantPhases) {
				t.Errorf("layer phases = %v, want %v", prCtx.LayerPhases, tc.wantPhases)
			}
			if prCtx.LayerRationale != tc.wantRationale {
				t.Errorf("layer rationale = %q, want %q", prCtx.LayerRationale, tc.wantRationale)
			}
			if prCtx.FeatureName == parentName {
				t.Errorf("feature name fell back to the parent's %q", parentName)
			}
			if prCtx.LayerPosition != 2 || prCtx.LayerTitle != "Child first" {
				t.Errorf("layer identity = %d/%q, want the parent layer's position and title", prCtx.LayerPosition, prCtx.LayerTitle)
			}
			if len(prCtx.Stack) != 2 {
				t.Errorf("stack view len = %d, want both parent layers", len(prCtx.Stack))
			}
		})
	}
}

// The completion preflight lists an appended layer like any other persisted
// stack layer — no preflight change is needed for origins: the layer reads
// push mode create before its pull request exists and none once it is
// delivered up to date.
func TestCompletionPreflightListsAppendedLayerPushModes(t *testing.T) {
	t.Parallel()

	repo, tip1, tip2 := stackedPreflightRepo(t)
	runCompletionGit(t, repo, "push", "origin", "feature/l1")
	const layer1URL = "https://github.example/repo-a/pull/1"
	base := feature.StackLayer{
		Position: 1, Title: "Bootstrap", Branch: "feature/l1",
		Repos: map[string]feature.StackRepoEntry{
			"repo-a": {TipSHA: tip1, LastPushedSHA: tip1, PRURL: layer1URL, PRState: feature.StackPRStateOpen},
		},
	}
	appended := func(entry feature.StackRepoEntry) feature.StackLayer {
		return feature.StackLayer{
			Position: 2, Title: "Child first", Branch: "feature/l2",
			Origin: &feature.StackLayerOrigin{SourceFeatureID: "feat-child-preflight", SourceLayerPosition: 1},
			Repos:  map[string]feature.StackRepoEntry{"repo-a": entry},
		}
	}

	before := onlyPendingRepo(t, newStackedPreflightOrchestrator(t, feature.StatusCodeReady, repo,
		[]feature.StackLayer{base, appended(feature.StackRepoEntry{TipSHA: tip2})},
		&feature.RepoState{Touched: true}))
	if before.Status != completionStatusUnpublishedChanges {
		t.Fatalf("status before = %q, want %q (the appended layer is undelivered)", before.Status, completionStatusUnpublishedChanges)
	}
	if len(before.PullRequests) != 2 || before.PullRequests[1].PushMode != completionPushModeCreate {
		t.Fatalf("pull requests before = %+v, want the appended layer listed with the create push mode", before.PullRequests)
	}

	// Once the appended layer is delivered — branch pushed, PR recorded,
	// tip equal to the last-pushed SHA — every layer reads none.
	runCompletionGit(t, repo, "push", "origin", "feature/l2")
	after := onlyPendingRepo(t, newStackedPreflightOrchestrator(t, feature.StatusCodeReady, repo,
		[]feature.StackLayer{base, appended(feature.StackRepoEntry{
			TipSHA: tip2, LastPushedSHA: tip2,
			PRURL: "https://github.example/repo-a/pull/2", PRState: feature.StackPRStateOpen,
		})},
		&feature.RepoState{Touched: true}))
	if after.Status != completionStatusAlreadyPublished {
		t.Fatalf("status after = %q, want %q once the appended layer is delivered", after.Status, completionStatusAlreadyPublished)
	}
	for _, pr := range after.PullRequests {
		if pr.PushMode != completionPushModeNone {
			t.Fatalf("pull requests after = %+v, want push mode none on every layer", after.PullRequests)
		}
	}
}
