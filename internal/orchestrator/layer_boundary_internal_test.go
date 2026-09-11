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
	"strings"
	"sync"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// TestSurfaceDispatchCompletionError_LayerBoundaryMarksFailedWithCanonicalCode
// pins the dispatcher mapping: the typed layer-boundary failure becomes the
// feature's terminal failure record carrying layer_boundary_failed, a phase
// block for the implementation phase, and a repositories block naming each
// failing repository with the branch its worktree is on. The diagnostics
// name the repository, its current branch, and the expected branch.
func TestSurfaceDispatchCompletionError_LayerBoundaryMarksFailedWithCanonicalCode(t *testing.T) {
	f := &feature.Feature{
		ID:                  "feat-boundary-dispatch",
		Status:              feature.StatusImplementing,
		CurrentPhase:        feature.PhaseImplement,
		CurrentRoadmapPhase: 2,
		TotalRoadmapPhases:  3,
	}
	lc := mocks.NewMockFeatureLifecycle()
	lc.GetFn = func(id string) (*feature.Feature, error) { return f, nil }
	var record errcat.FailureRecord
	var markErr error
	lc.MarkFailedFn = func(id string, failure errcat.FailureRecord) error {
		record = failure
		f.Status = feature.StatusFailed
		stored := failure
		f.Run().Failure = &stored
		return nil
	}
	fs := mocks.NewMockFeatureStore()
	var mu sync.Mutex
	fs.LoadFn = func(id string) (*feature.Feature, error) {
		mu.Lock()
		defer mu.Unlock()
		return f, nil
	}
	fs.ModifyFn = func(id string, fn func(ff *feature.Feature) error) error {
		mu.Lock()
		defer mu.Unlock()
		return fn(f)
	}
	o := New(Deps{Lifecycle: lc, Store: fs}, Hooks{})

	cause := &layerBoundaryError{
		diagnostics: strings.Join([]string{
			`repo "repo-a" is on branch "feature/wrong-branch", want it on the recorded "feature/pr-stacks-a1b2c3d4/1-bootstrap" so it can be split onto layer 2's branch "feature/pr-stacks-a1b2c3d4/2-build-and-polish"`,
			`repo "repo-b": layer 2's branch "feature/pr-stacks-a1b2c3d4/2-build-and-polish" already exists as a ref while the worktree is on "feature/pr-stacks-a1b2c3d4/1-bootstrap"`,
		}, "\n"),
		repos: []errcat.CodeRepository{
			{Name: "repo-a", Branch: "feature/wrong-branch"},
			{Name: "repo-b", Branch: "feature/pr-stacks-a1b2c3d4/1-bootstrap"},
		},
	}
	o.surfaceDispatchCompletionError(f.ID, cause)

	if markErr != nil {
		t.Fatalf("MarkFailed error: %v", markErr)
	}
	if record.Code != errcat.LayerBoundaryFailed {
		t.Fatalf("failure code = %q, want %q", record.Code, errcat.LayerBoundaryFailed)
	}
	if record.Context == nil || record.Context.Phase == nil || record.Context.Phase.Name != feature.PhaseImplement.FailureName() {
		t.Fatalf("record phase block = %+v, want the implement phase", record.Context)
	}
	if len(record.Context.Repositories) != 2 {
		t.Fatalf("repositories block = %+v, want both failing repositories", record.Context.Repositories)
	}
	if got := record.Context.Repositories[0]; got.Name != "repo-a" || got.Branch != "feature/wrong-branch" {
		t.Errorf("repositories[0] = %+v, want repo-a on its current branch", got)
	}
	for _, want := range []string{"repo-a", "feature/wrong-branch", "feature/pr-stacks-a1b2c3d4/1-bootstrap", "feature/pr-stacks-a1b2c3d4/2-build-and-polish"} {
		if !strings.Contains(record.Diagnostics, want) {
			t.Errorf("diagnostics %q must name %q", record.Diagnostics, want)
		}
	}

	// The rendered canonical error keeps the catalog contract: blocking
	// class, the layer-boundary title, and the restart action.
	rendered := errcat.RenderRecord(record)
	if rendered.Class != errcat.ClassBlocking {
		t.Errorf("rendered class = %q, want blocking", rendered.Class)
	}
	if rendered.Title != "Layer boundary failed" {
		t.Errorf("rendered title = %q, want %q", rendered.Title, "Layer boundary failed")
	}
	if len(rendered.Remediation.Actions) != 1 || rendered.Remediation.Actions[0] != "restart" {
		t.Errorf("rendered actions = %v, want [restart]", rendered.Remediation.Actions)
	}
	if !strings.Contains(rendered.Summary, "layer boundary") {
		t.Errorf("rendered summary = %q, want the layer-boundary summary", rendered.Summary)
	}
	if f.Status != feature.StatusFailed {
		t.Fatalf("feature status = %v, want Failed", f.Status)
	}
}

// The boundary failure's canonical error renders for the no-stack shape
// too, where no repository is named.
func TestLayerBoundaryErrorNoStackRenders(t *testing.T) {
	cause := &layerBoundaryError{
		diagnostics: `feature "feat-x" has no approved pull-request stack at the layer boundary of roadmap phase 2`,
	}
	record := cause.failureRecord()
	if record.Code != errcat.LayerBoundaryFailed {
		t.Fatalf("failure code = %q, want %q", record.Code, errcat.LayerBoundaryFailed)
	}
	if record.Context.Repositories != nil {
		t.Errorf("repositories block = %+v, want none", record.Context.Repositories)
	}
	rendered := errcat.RenderRecord(record)
	if rendered.Class != errcat.ClassBlocking || rendered.Title != "Layer boundary failed" {
		t.Fatalf("rendered = %+v, want the blocking layer-boundary contract", rendered)
	}
}
