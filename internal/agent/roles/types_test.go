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

package roles

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewestPlanMarkdownArtifactExcludesPlanningHandoff(t *testing.T) {
	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.md")
	if err := os.WriteFile(planPath, []byte("# Plan\n"), 0o644); err != nil {
		t.Fatalf("write plan.md: %v", err)
	}
	handoffPath := filepath.Join(dir, "planning-handoff.md")
	if err := os.WriteFile(handoffPath, []byte("# Planning Handoff\n"), 0o644); err != nil {
		t.Fatalf("write planning-handoff.md: %v", err)
	}

	if got := newestPlanMarkdownArtifact(dir); got != planPath {
		t.Fatalf("newestPlanMarkdownArtifact() = %q, want %q", got, planPath)
	}
}
