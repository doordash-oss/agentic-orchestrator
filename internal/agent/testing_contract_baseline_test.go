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
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

func TestEnsureTestingContractBaseCommitsCapturesEveryRepo(t *testing.T) {
	t.Parallel()

	contract := &TestingContract{}
	repos := []feature.FeatureRepo{
		{Name: "api", WorktreePath: "/worktrees/api"},
		{Name: "web", WorktreePath: "/worktrees/web"},
	}
	resolved := map[string]string{
		"/worktrees/api": "api-commit",
		"/worktrees/web": "web-commit",
	}

	err := EnsureTestingContractBaseCommits(contract, repos, func(path string) (string, error) {
		return resolved[path], nil
	})
	if err != nil {
		t.Fatalf("EnsureTestingContractBaseCommits: %v", err)
	}
	want := map[string]string{"api": "api-commit", "web": "web-commit"}
	if !reflect.DeepEqual(contract.BaseCommits, want) {
		t.Fatalf("BaseCommits = %#v, want %#v", contract.BaseCommits, want)
	}
}

func TestEnsureTestingContractBaseCommitsPreservesExistingAnchors(t *testing.T) {
	t.Parallel()

	contract := &TestingContract{BaseCommits: map[string]string{
		"api":     "original-api-commit",
		"retired": "preserved-extra-anchor",
	}}
	repos := []feature.FeatureRepo{
		{Name: "api", WorktreePath: "/worktrees/api"},
		{Name: "web", WorktreePath: "/worktrees/web"},
	}
	var resolved []string
	err := EnsureTestingContractBaseCommits(contract, repos, func(path string) (string, error) {
		resolved = append(resolved, path)
		return "new-web-commit", nil
	})
	if err != nil {
		t.Fatalf("EnsureTestingContractBaseCommits: %v", err)
	}
	if !reflect.DeepEqual(resolved, []string{"/worktrees/web"}) {
		t.Fatalf("resolved worktrees = %#v, want only missing web anchor", resolved)
	}
	want := map[string]string{
		"api":     "original-api-commit",
		"web":     "new-web-commit",
		"retired": "preserved-extra-anchor",
	}
	if !reflect.DeepEqual(contract.BaseCommits, want) {
		t.Fatalf("BaseCommits = %#v, want %#v", contract.BaseCommits, want)
	}
}

func TestEnsureTestingContractBaseCommitsIsAtomicOnResolutionFailure(t *testing.T) {
	t.Parallel()

	original := map[string]string{"existing": "existing-commit"}
	contract := &TestingContract{BaseCommits: original}
	repos := []feature.FeatureRepo{
		{Name: "api", WorktreePath: "/worktrees/api"},
		{Name: "web", WorktreePath: "/worktrees/web"},
	}
	errBoom := errors.New("cannot read HEAD")
	err := EnsureTestingContractBaseCommits(contract, repos, func(path string) (string, error) {
		if strings.HasSuffix(path, "/web") {
			return "", errBoom
		}
		return "api-commit", nil
	})
	if !errors.Is(err, errBoom) {
		t.Fatalf("error = %v, want wrapped %v", err, errBoom)
	}
	if !reflect.DeepEqual(contract.BaseCommits, original) {
		t.Fatalf("BaseCommits changed after failed resolution: %#v", contract.BaseCommits)
	}
}

func TestEnsureTestingContractBaseCommitsPathlessRepos(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		repos []feature.FeatureRepo
		want  map[string]string
	}{
		{
			name:  "falls back to repo path when worktree was cleaned",
			repos: []feature.FeatureRepo{{Name: "api", Path: "/repos/api"}},
			want:  map[string]string{"api": "head-of-/repos/api"},
		},
		{
			name:  "skips repo with neither path instead of failing",
			repos: []feature.FeatureRepo{{Name: "api"}, {Name: "web", WorktreePath: "/worktrees/web"}},
			want:  map[string]string{"web": "head-of-/worktrees/web"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			contract := &TestingContract{}
			err := EnsureTestingContractBaseCommits(contract, tt.repos, func(path string) (string, error) {
				return "head-of-" + path, nil
			})
			if err != nil {
				t.Fatalf("EnsureTestingContractBaseCommits: %v", err)
			}
			if !reflect.DeepEqual(contract.BaseCommits, tt.want) {
				t.Fatalf("BaseCommits = %#v, want %#v", contract.BaseCommits, tt.want)
			}
		})
	}
}

func TestTestingContractBaseCommitsYAMLRoundTrip(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "testing-contract.yaml")
	want := TestingContract{
		Version:     testingContractVersion,
		Revision:    testingContractInitialRev,
		Scope:       "phase-02",
		BaseCommits: map[string]string{"api": "api-commit", "web": "web-commit"},
	}
	if err := WriteTestingContract(path, want); err != nil {
		t.Fatalf("WriteTestingContract: %v", err)
	}
	got, err := ReadTestingContract(path)
	if err != nil {
		t.Fatalf("ReadTestingContract: %v", err)
	}
	if !reflect.DeepEqual(got.BaseCommits, want.BaseCommits) {
		t.Fatalf("BaseCommits = %#v, want %#v", got.BaseCommits, want.BaseCommits)
	}
}

func TestResolveTestingContractWorktreeHEAD(t *testing.T) {
	// Git config and subprocess state are isolated to this temporary repository.
	dir := t.TempDir()
	runGitForTestingContractBaseline(t, dir, "init")
	runGitForTestingContractBaseline(t, dir, "config", "user.email", "test@example.com")
	runGitForTestingContractBaseline(t, dir, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("write tracked file: %v", err)
	}
	runGitForTestingContractBaseline(t, dir, "add", "tracked.txt")
	runGitForTestingContractBaseline(t, dir, "commit", "-m", "base")
	want := runGitForTestingContractBaseline(t, dir, "rev-parse", "HEAD")

	got, err := resolveTestingContractWorktreeHEAD(dir)
	if err != nil {
		t.Fatalf("resolveTestingContractWorktreeHEAD: %v", err)
	}
	if got != want {
		t.Fatalf("HEAD = %q, want %q", got, want)
	}
}

func runGitForTestingContractBaseline(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}
