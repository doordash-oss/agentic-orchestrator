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
	"os/exec"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

const (
	preflightRepoA = "repo-a"
	preflightRepoB = "repo-b"
	preflightMain  = "main"
)

func newPreflightOrchestrator(t *testing.T, behind map[string]bool) *Orchestrator {
	t.Helper()
	store := feature.NewStore(t.TempDir())
	manager := feature.NewManager(store, config.NewDefault())
	remote := mocks.NewMockRemoteOps()
	remote.PRBaseBranchFn = func(_, _ string) string { return preflightMain }

	repos := make([]feature.FeatureRepo, 0, 2)
	repoStates := make(map[string]*feature.RepoState, 2)
	for _, name := range []string{preflightRepoA, preflightRepoB} {
		wt, bare := testutil.InitPublishReadyGitRepo(t)
		if behind[name] {
			testutil.CommitFile(t, wt, "remote.txt", name, "advance remote")
			testutil.SimulatePush(t, wt, bare, preflightMain, preflightMain)
			cmd := exec.Command("git", "reset", "--hard", "HEAD~1")
			cmd.Dir = wt
			cmd.Env = testutil.GitTestEnv()
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("reset local repo: %v: %s", err, out)
			}
		}
		repos = append(repos, feature.FeatureRepo{
			Name:         name,
			Path:         wt,
			WorktreePath: wt,
			Branch:       "feature/x",
			BaseBranch:   preflightMain,
		})
		repoStates[name] = &feature.RepoState{Touched: true, PRURL: "https://github.example/" + name + "/pull/1"}
	}
	f := &feature.Feature{
		ID:            "feat-preflight",
		Name:          "Preflight",
		Slug:          "preflight",
		Status:        feature.StatusPublished,
		SchemaVersion: feature.SchemaVersionCurrent,
		ActiveRun:     1,
		RunCount:      1,
		Repos:         repos,
		RepoStates:    repoStates,
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}
	return New(Deps{Lifecycle: manager, Store: store, Remote: remote}, Hooks{})
}

func TestRebasePreflightEnumeratesReposAndRevision(t *testing.T) {
	t.Parallel()
	o := newPreflightOrchestrator(t, map[string]bool{preflightRepoA: true, preflightRepoB: false})
	result, err := o.RebasePreflight("feat-preflight")
	if err != nil {
		t.Fatalf("RebasePreflight: %v", err)
	}
	if result.FeatureID != "feat-preflight" {
		t.Fatalf("feature id = %q", result.FeatureID)
	}
	if len(result.Repos) != 2 {
		t.Fatalf("repos = %d; want 2", len(result.Repos))
	}
	if result.SourceRevision == "" {
		t.Fatal("source revision is empty; want a stable fingerprint")
	}
	for _, r := range result.Repos {
		if r.Target == "" {
			t.Fatalf("repo %s has empty target", r.Repo)
		}
		if r.Freshness == "" {
			t.Fatalf("repo %s has empty freshness", r.Repo)
		}
		wantBehind := r.Repo == preflightRepoA
		if r.Behind != wantBehind {
			t.Fatalf("repo %s Behind = %v; want %v", r.Repo, r.Behind, wantBehind)
		}
		wantFreshness := preflightFreshnessUpToDate
		if wantBehind {
			wantFreshness = preflightFreshnessBehind
		}
		if r.Freshness != wantFreshness {
			t.Fatalf("repo %s Freshness = %q; want %q", r.Repo, r.Freshness, wantFreshness)
		}
	}
}

func TestRebasePreflightSourceRevisionIsStable(t *testing.T) {
	t.Parallel()
	o := newPreflightOrchestrator(t, nil)
	first, err := o.RebasePreflight("feat-preflight")
	if err != nil {
		t.Fatalf("first preflight: %v", err)
	}
	second, err := o.RebasePreflight("feat-preflight")
	if err != nil {
		t.Fatalf("second preflight: %v", err)
	}
	if first.SourceRevision != second.SourceRevision {
		t.Fatalf("source revision drifted without a state change: %q != %q", first.SourceRevision, second.SourceRevision)
	}
	// The recomputed guard revision must agree with the preview.
	guard, err := o.RebasePreflightSourceRevision("feat-preflight")
	if err != nil {
		t.Fatalf("guard revision: %v", err)
	}
	if guard != first.SourceRevision {
		t.Fatalf("guard revision %q != preview %q", guard, first.SourceRevision)
	}
}
