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
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

func TestBuildWorkspace_GrantsOnlyActiveRunState(t *testing.T) {
	featureStateDir := t.TempDir()
	sealedRunDir := filepath.Join(featureStateDir, "runs", "run-004")
	activeRunDir := filepath.Join(featureStateDir, "runs", "run-005")
	for _, dir := range []string{sealedRunDir, activeRunDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", dir, err)
		}
	}

	ws, err := BuildWorkspace(&feature.Feature{ActiveRun: 5}, featureStateDir)
	if err != nil {
		t.Fatalf("BuildWorkspace() error = %v", err)
	}
	if ws.Cwd != activeRunDir {
		t.Fatalf("Cwd = %q, want active run %q", ws.Cwd, activeRunDir)
	}
	if !slices.Contains(ws.AdditionalDirs, activeRunDir) {
		t.Fatalf("AdditionalDirs = %v, want active run %q", ws.AdditionalDirs, activeRunDir)
	}
	for _, forbidden := range []string{featureStateDir, sealedRunDir} {
		if slices.Contains(ws.AdditionalDirs, forbidden) {
			t.Fatalf("AdditionalDirs = %v, must not grant predecessor scope %q", ws.AdditionalDirs, forbidden)
		}
	}
}

func TestBuildWorkspace_SingleRepoWorktree(t *testing.T) {
	stateDir := t.TempDir()
	wt := t.TempDir()
	feat := &feature.Feature{
		Repos: []feature.FeatureRepo{{Name: "solo", Path: "/repos/solo", WorktreePath: wt}},
	}
	ws, err := BuildWorkspace(feat, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	activeRunDir := filepath.Join(stateDir, "runs", "run-001")
	if ws.Cwd != activeRunDir {
		t.Errorf("Cwd = %q, want %q", ws.Cwd, activeRunDir)
	}
	want := []string{activeRunDir, wt}
	if !reflect.DeepEqual(ws.AdditionalDirs, want) {
		t.Errorf("AdditionalDirs = %v, want %v", ws.AdditionalDirs, want)
	}
	if ws.RepoPaths["solo"] != wt {
		t.Errorf("RepoPaths[solo] = %q, want %q", ws.RepoPaths["solo"], wt)
	}
}

func TestBuildWorkspace_MultiRepoSorted(t *testing.T) {
	stateDir := t.TempDir()
	feat := &feature.Feature{
		Repos: []feature.FeatureRepo{
			{Name: "zeta", Path: "/repos/zeta"},
			{Name: "alpha", Path: "/repos/alpha"},
			{Name: "mid", Path: "/repos/mid"},
		},
	}
	ws, err := BuildWorkspace(feat, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	abs := func(p string) string { a, _ := filepath.Abs(p); return a }
	want := []string{filepath.Join(stateDir, "runs", "run-001"), abs("/repos/alpha"), abs("/repos/mid"), abs("/repos/zeta")}
	if !reflect.DeepEqual(ws.AdditionalDirs, want) {
		t.Errorf("AdditionalDirs = %v, want %v", ws.AdditionalDirs, want)
	}
}

func TestBuildWorkspace_NoRepos(t *testing.T) {
	stateDir := t.TempDir()
	feat := &feature.Feature{}
	ws, err := BuildWorkspace(feat, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	activeRunDir := filepath.Join(stateDir, "runs", "run-001")
	if len(ws.AdditionalDirs) != 1 || ws.AdditionalDirs[0] != activeRunDir {
		t.Errorf("AdditionalDirs = %v, want only active run dir", ws.AdditionalDirs)
	}
}

func TestBuildWorkspace_NilFeature(t *testing.T) {
	if _, err := BuildWorkspace(nil, "/tmp"); err == nil {
		t.Fatal("expected error for nil feature")
	}
}

func TestBuildWorkspace_EmptyStateDir(t *testing.T) {
	if _, err := BuildWorkspace(&feature.Feature{}, ""); err == nil {
		t.Fatal("expected error for empty state dir")
	}
}

func TestWorkspaceForRepos_FiltersSubset(t *testing.T) {
	stateDir := t.TempDir()
	feat := &feature.Feature{
		Repos: []feature.FeatureRepo{
			{Name: testRepoNameAPI, Path: "/r/api"},
			{Name: testRepoNameWeb, Path: "/r/web"},
			{Name: testRepoNameInfra, Path: "/r/infra"},
		},
	}
	ws, err := WorkspaceForRepos(feat, stateDir, []string{testRepoNameWeb, testRepoNameAPI})
	if err != nil {
		t.Fatal(err)
	}
	abs := func(p string) string { a, _ := filepath.Abs(p); return a }
	want := []string{filepath.Join(stateDir, "runs", "run-001"), abs("/r/api"), abs("/r/web")}
	if !reflect.DeepEqual(ws.AdditionalDirs, want) {
		t.Errorf("AdditionalDirs = %v, want %v", ws.AdditionalDirs, want)
	}
	if _, ok := ws.RepoPaths[testRepoNameInfra]; ok {
		t.Errorf("expected infra to be filtered out")
	}
}

func TestWorkspaceForRepos_EmptyMeansAll(t *testing.T) {
	stateDir := t.TempDir()
	feat := &feature.Feature{
		Repos: []feature.FeatureRepo{{Name: testRepoNameAPI, Path: "/r/api"}},
	}
	ws, err := WorkspaceForRepos(feat, stateDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(ws.RepoPaths) != 1 {
		t.Errorf("expected all repos when filter is empty")
	}
}

func TestBuildWorkspace_RepoMissingBothPathFields(t *testing.T) {
	stateDir := t.TempDir()
	feat := &feature.Feature{
		Repos: []feature.FeatureRepo{
			{Name: "ghost"}, // missing both Path and WorktreePath
			{Name: "real", Path: "/r/real"},
		},
	}
	ws, err := BuildWorkspace(feat, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ws.RepoPaths["ghost"]; ok {
		t.Errorf("expected ghost (missing path) to be skipped")
	}
	if ws.RepoPaths["real"] == "" {
		t.Errorf("expected real to be present")
	}
}
