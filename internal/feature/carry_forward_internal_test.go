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

package feature

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCarryForwardCostKey(t *testing.T) {
	t.Parallel()

	cases := []struct {
		key          string
		target       Phase
		roadmapPhase int
		want         bool
	}{
		{"knowledgebase", PhaseInquire, 0, true},
		{"inquire", PhaseInquire, 0, false},
		{"inquire", PhaseResearch, 0, true},
		{"research", PhaseResearch, 0, false},
		{"research", PhaseDesign, 0, true},
		{"design", PhasePlan, 0, true},
		{"plan", PhasePlan, 0, false},
		// Full implement rewind: planning history survives, implement spend does not.
		{"plan", PhaseImplement, 0, true},
		{"phase-3-plan", PhaseImplement, 0, true},
		{"phase-3-impl", PhaseImplement, 0, false},
		{"implement", PhaseImplement, 0, false},
		{"review", PhaseImplement, 0, false},
		{"rebase-1", PhaseImplement, 0, false},
		// Partial implement rewind to roadmap phase 9: phases 1-8 are kept.
		{"phase-8-impl", PhaseImplement, 9, true},
		{"phase-9-impl", PhaseImplement, 9, false},
		{"phase-9-plan", PhaseImplement, 9, true},
		{"phase-10-plan", PhaseImplement, 9, false},
		{"knowledgebase", PhaseImplement, 9, true},
	}
	for _, tc := range cases {
		if got := carryForwardCostKey(tc.key, tc.target, tc.roadmapPhase); got != tc.want {
			t.Errorf("carryForwardCostKey(%q, %v, %d) = %v, want %v", tc.key, tc.target, tc.roadmapPhase, got, tc.want)
		}
	}
}

func TestCopyRunArtifactsForward_SkipsSymlinks(t *testing.T) {
	t.Parallel()

	sealed := t.TempDir()
	fresh := t.TempDir()

	// Review agents leave scratch dirs with node_modules symlinks pointing at
	// the (mutable) worktree. Following one lands io.Copy on a directory.
	linkTargetDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(linkTargetDir, "dep.js"), []byte("dep"), 0o644); err != nil {
		t.Fatalf("write link target file: %v", err)
	}
	linkTargetFile := filepath.Join(linkTargetDir, "dep.js")

	implDir := filepath.Join(sealed, "phase-01", "implement", "tmp", "driver")
	if err := os.MkdirAll(implDir, 0o755); err != nil {
		t.Fatalf("mkdir impl dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(implDir, "driver.mjs"), []byte("script"), 0o644); err != nil {
		t.Fatalf("write driver: %v", err)
	}
	if err := os.Symlink(linkTargetDir, filepath.Join(implDir, "node_modules")); err != nil {
		t.Fatalf("symlink dir: %v", err)
	}
	if err := os.Symlink(linkTargetFile, filepath.Join(implDir, "dep-link.js")); err != nil {
		t.Fatalf("symlink file: %v", err)
	}

	// A carried path that is itself a symlink must be skipped, not followed.
	if err := os.Symlink(linkTargetDir, filepath.Join(sealed, "linked-root")); err != nil {
		t.Fatalf("symlink root: %v", err)
	}

	dirs := []string{filepath.Join("phase-01", "implement"), "linked-root"}
	if err := copyRunArtifactsForward(sealed, fresh, dirs); err != nil {
		t.Fatalf("copyRunArtifactsForward: %v", err)
	}

	copiedDriver := filepath.Join(fresh, "phase-01", "implement", "tmp", "driver", "driver.mjs")
	if data, err := os.ReadFile(copiedDriver); err != nil || string(data) != "script" {
		t.Errorf("driver.mjs = %q, %v; want %q copied", data, err, "script")
	}
	for _, rel := range []string{
		filepath.Join("phase-01", "implement", "tmp", "driver", "node_modules"),
		filepath.Join("phase-01", "implement", "tmp", "driver", "dep-link.js"),
		"linked-root",
	} {
		if _, err := os.Lstat(filepath.Join(fresh, rel)); !os.IsNotExist(err) {
			t.Errorf("%s exists in new run dir (err=%v), want skipped", rel, err)
		}
	}
}
