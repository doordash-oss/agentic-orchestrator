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
	"os"
	"path/filepath"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// TestCollectQAFilePaths_IncludesRoadmap asserts that when fixture
// qa-answers.md files exist under inquire/, research/, design/, and
// roadmap/ subdirs, all four paths are returned in the documented order.
func TestCollectQAFilePaths_IncludesRoadmap(t *testing.T) {
	tmpStateDir := t.TempDir()
	featureID := "feat-collect-roadmap"
	f := &feature.Feature{
		ID:        featureID,
		ActiveRun: 1,
		RunCount:  1,
	}

	runDir := agent.ActiveRunDir(tmpStateDir, f)
	for _, phase := range []string{"inquire", "research", "design", "roadmap"} {
		dir := filepath.Join(runDir, phase)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "qa-answers.md"), []byte("# qa\n"), 0o644); err != nil {
			t.Fatalf("write qa-answers.md for %s: %v", phase, err)
		}
	}

	pr := &agent.PhaseRunner{StateDir: tmpStateDir}
	o := New(Deps{PhaseRunner: pr}, Hooks{})

	got := o.collectQAFilePaths(f, "")

	want := []string{
		filepath.Join(runDir, "inquire", "qa-answers.md"),
		filepath.Join(runDir, "research", "qa-answers.md"),
		filepath.Join(runDir, "design", "qa-answers.md"),
		filepath.Join(runDir, "roadmap", "qa-answers.md"),
	}
	if len(got) != len(want) {
		t.Fatalf("collectQAFilePaths returned %d paths, want %d; got=%v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("collectQAFilePaths[%d] = %q, want %q", i, got[i], w)
		}
	}
}

// TestCollectQAFilePaths_RefactorPrefix asserts the new roadmap probe honors
// a non-empty refactor prefix, mirroring the existing behavior for the
// inquire/research/design probes.
func TestCollectQAFilePaths_RefactorPrefix(t *testing.T) {
	tmpStateDir := t.TempDir()
	featureID := "feat-collect-refprefix"
	f := &feature.Feature{
		ID:             featureID,
		ActiveRun:      1,
		RunCount:       1,
		RefactorPrompt: "polish",
	}
	f.SetRefactorCount(1)
	prefix := f.RefactorPrefix()
	if prefix == "" {
		t.Fatalf("expected non-empty RefactorPrefix() for refactor count = 1")
	}

	runDir := agent.ActiveRunDir(tmpStateDir, f)
	for _, phase := range []string{"inquire", "research", "design", "roadmap"} {
		dir := filepath.Join(runDir, prefix, phase)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "qa-answers.md"), []byte("# qa\n"), 0o644); err != nil {
			t.Fatalf("write qa-answers.md for %s: %v", phase, err)
		}
	}

	pr := &agent.PhaseRunner{StateDir: tmpStateDir}
	o := New(Deps{PhaseRunner: pr}, Hooks{})

	got := o.collectQAFilePaths(f, prefix)

	want := []string{
		filepath.Join(runDir, prefix, "inquire", "qa-answers.md"),
		filepath.Join(runDir, prefix, "research", "qa-answers.md"),
		filepath.Join(runDir, prefix, "design", "qa-answers.md"),
		filepath.Join(runDir, prefix, "roadmap", "qa-answers.md"),
	}
	if len(got) != len(want) {
		t.Fatalf("collectQAFilePaths returned %d paths, want %d; got=%v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("collectQAFilePaths[%d] = %q, want %q", i, got[i], w)
		}
	}
}
