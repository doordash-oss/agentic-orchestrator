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

func TestResolveArtifactPathDoesNotAliasOldArtifactToDesign(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	oldKey := "brain" + "storm"
	f := &feature.Feature{
		ID:        "feature-001",
		ActiveRun: 1,
		Artifacts: map[string]string{oldKey: oldKey + ".md"},
	}
	oldDir := filepath.Join(agent.ActiveRunDir(stateDir, f), oldKey)
	if err := os.MkdirAll(oldDir, 0o755); err != nil {
		t.Fatalf("mkdir old artifact dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(oldDir, oldKey+".md"), []byte("legacy"), 0o644); err != nil {
		t.Fatalf("write old artifact: %v", err)
	}

	o := New(Deps{PhaseRunner: &agent.PhaseRunner{StateDir: stateDir}}, Hooks{})
	if got := o.resolveArtifactPath(f, "design"); got != "" {
		t.Errorf("resolveArtifactPath(design) = %q, want empty", got)
	}
}

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

	got := o.collectQAFilePaths(f)

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

// TestCollectQAFilePaths_SkipsLegacyCycleDirs asserts the probe no longer
// discovers QA files under legacy "refactor-N" prefixed directories: the
// refactor cycle was removed, so artifacts staged there must not leak into
// planning prompts.
func TestCollectQAFilePaths_SkipsLegacyCycleDirs(t *testing.T) {
	tmpStateDir := t.TempDir()
	featureID := "feat-collect-refprefix"
	f := &feature.Feature{
		ID:        featureID,
		ActiveRun: 1,
		RunCount:  1,
	}

	runDir := agent.ActiveRunDir(tmpStateDir, f)
	for _, phase := range []string{"inquire", "research", "design", "roadmap"} {
		dir := filepath.Join(runDir, "refactor-1", phase)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "qa-answers.md"), []byte("# qa\n"), 0o644); err != nil {
			t.Fatalf("write qa-answers.md for %s: %v", phase, err)
		}
	}

	pr := &agent.PhaseRunner{StateDir: tmpStateDir}
	o := New(Deps{PhaseRunner: pr}, Hooks{})

	if got := o.collectQAFilePaths(f); len(got) != 0 {
		t.Errorf("collectQAFilePaths returned %d paths from a legacy refactor-N tree, want 0; got=%v", len(got), got)
	}
}
