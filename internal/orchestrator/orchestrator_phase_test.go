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

package orchestrator_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// writeTempFile writes contents to a file in t.TempDir and returns the path.
func writeTempFile(t *testing.T, name, contents string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(contents), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	return p
}

// writeExecOrderNextToPlan writes a trivial single-stage execution-order.yaml
// in the same directory as planPath. Per SchemaVersionCurrent = 3, the
// orchestrator hard-fails if the file is missing, so most tests that exercise
// StartMultiRepoImplementation must call this after writeTempFile("plan.md").
//
// The default authored plan lists every repo in a single parallel stage. Tests
// that need a more specific shape can overwrite the file afterwards.
func writeExecOrderNextToPlan(t *testing.T, planPath string, repos []feature.FeatureRepo) {
	t.Helper()
	var sb strings.Builder
	sb.WriteString("stages:\n  - repos: [")
	for i, r := range repos {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(r.Name)
	}
	sb.WriteString("]\n")
	yamlPath := filepath.Join(filepath.Dir(planPath), "execution-order.yaml")
	if err := os.WriteFile(yamlPath, []byte(sb.String()), 0o644); err != nil {
		t.Fatalf("write execution-order.yaml: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// featureStore wraps a map[string]*feature.Feature so that ModifyFn callbacks
// observe the same object that GetFn returns. Tests mutate features via
// store.set, and Modify applies the callback to the stored feature.
type featureStore struct {
	*mocks.MockFeatureStore
	mu       sync.Mutex
	features map[string]*feature.Feature
}

func newFeatureStore(features ...*feature.Feature) *featureStore {
	fs := &featureStore{
		MockFeatureStore: mocks.NewMockFeatureStore(),
		features:         make(map[string]*feature.Feature),
	}
	for _, f := range features {
		fs.features[f.ID] = f
	}
	fs.LoadFn = func(id string) (*feature.Feature, error) {
		fs.mu.Lock()
		defer fs.mu.Unlock()
		f, ok := fs.features[id]
		if !ok {
			return nil, errors.New("feature not found")
		}
		return f, nil
	}
	fs.ModifyFn = func(id string, fn func(ff *feature.Feature) error) error {
		fs.mu.Lock()
		defer fs.mu.Unlock()
		f, ok := fs.features[id]
		if !ok {
			return errors.New("feature not found")
		}
		return fn(f)
	}
	fs.ListFn = func() ([]*feature.Feature, error) {
		fs.mu.Lock()
		defer fs.mu.Unlock()
		out := make([]*feature.Feature, 0, len(fs.features))
		for _, f := range fs.features {
			out = append(out, f)
		}
		return out, nil
	}
	return fs
}

// lifecycleForFeature returns a MockFeatureLifecycle whose Get returns the
// supplied feature and Transition mutates the feature's Status in-place.
func lifecycleForFeature(f *feature.Feature) *mocks.MockFeatureLifecycle {
	lc := mocks.NewMockFeatureLifecycle()
	lc.GetFn = func(id string) (*feature.Feature, error) { return f, nil }
	lc.TransitionFn = func(id string, to feature.Status) error {
		f.Status = to
		return nil
	}
	return lc
}

// withStatusTransitions wires StartXxx hooks on a MockFeatureLifecycle so
// that each transition also mutates the feature's Status field in-place.
// Async phase loops (plan_validation.go, implement.go, final_review.go) gate
// on isFeatureInterrupted, which reads Status from the store — without these
// transitions the feature stays at StatusInterrupted and loops exit before
// BuildSession fires. Tests that exercise full PhaseRunner dispatch chains
// should apply this helper after lifecycleForFeature.
func withStatusTransitions(lc *mocks.MockFeatureLifecycle, f *feature.Feature) *mocks.MockFeatureLifecycle {
	lc.StartInquireFn = func(id string) error { f.Status = feature.StatusInquiring; return nil }
	lc.StartBrainstormFn = func(id string) error { f.Status = feature.StatusBrainstorming; return nil }
	lc.StartResearchFn = func(id string) error { f.Status = feature.StatusResearching; return nil }
	lc.StartPlanningFn = func(id string) error { f.Status = feature.StatusPlanning; return nil }
	lc.StartImplementationFn = func(id string) error { f.Status = feature.StatusImplementing; return nil }
	lc.StartKnowledgeBaseFn = func(id string) error { f.Status = feature.StatusBuildingKB; return nil }
	return lc
}

// drainEvents pulls all events currently buffered on the channel.
func drainEvents(o *orchestrator.Orchestrator) []ports.Event {
	var out []ports.Event
	for {
		select {
		case ev := <-o.Events():
			out = append(out, ev)
		default:
			return out
		}
	}
}

// hasPhaseStarted returns the first PhaseStarted event, or nil if none.
func hasPhaseStarted(events []ports.Event, want feature.Phase) *ports.Event {
	for i, ev := range events {
		if ev.Type == ports.PhaseStarted && ev.Phase == want {
			return &events[i]
		}
	}
	return nil
}

// hasEventType returns true if any event of the given type was observed.
func hasEventType(events []ports.Event, want ports.EventType) bool {
	for _, ev := range events {
		if ev.Type == want {
			return true
		}
	}
	return false
}

func hasPhaseCompleted(events []ports.Event, want feature.Phase) bool {
	for _, ev := range events {
		if ev.Type == ports.PhaseCompleted && ev.Phase == want {
			return true
		}
	}
	return false
}

// assertLifecycleCall asserts that the mock lifecycle recorded a call to the
// named method. Returns the first matching call, or nil.
func assertLifecycleCall(t *testing.T, lc *mocks.MockFeatureLifecycle, method string) *mocks.MockCall {
	t.Helper()
	for i, c := range lc.Calls {
		if c.Method == method {
			return &lc.Calls[i]
		}
	}
	t.Errorf("expected lifecycle call %q; got calls: %v", method, lifecycleCallNames(lc))
	return nil
}

func lifecycleCallNames(lc *mocks.MockFeatureLifecycle) []string {
	names := make([]string, len(lc.Calls))
	for i, c := range lc.Calls {
		names[i] = c.Method
	}
	return names
}

func indexOf(names []string, want string) int {
	for i, name := range names {
		if name == want {
			return i
		}
	}
	return -1
}

// refuteLifecycleCall asserts that the lifecycle did NOT receive a named call.
func refuteLifecycleCall(t *testing.T, lc *mocks.MockFeatureLifecycle, method string) {
	t.Helper()
	for _, c := range lc.Calls {
		if c.Method == method {
			t.Errorf("did not expect lifecycle call %q, but it was called; all calls: %v", method, lifecycleCallNames(lc))
			return
		}
	}
}
