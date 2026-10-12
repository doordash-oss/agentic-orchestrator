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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
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
