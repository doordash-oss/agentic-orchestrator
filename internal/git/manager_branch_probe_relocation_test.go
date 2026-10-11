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

// This file relocates the feature-manager branch-collision tests that inject
// scripted branch-probe runners. They live in the git package's own tests
// because the runner boundary and its result type are internal to this
// package; the scripted-runner adapters in export_test.go stay visible only
// to this package's tests.

package git_test

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// relocatedFeatureTestGitOutput runs git in dir and fails the test on error.
func relocatedFeatureTestGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s: %v", args, strings.TrimSpace(string(out)), err)
	}
	return strings.TrimSpace(string(out))
}

func TestManagerCreateKeepsLocallyUniqueBranchWhenOriginProbeIsUnavailable(t *testing.T) {
	t.Parallel()
	// The unreachable origin must stay nonblocking in both source modes while
	// acceptance still pins the exact local commit.
	for _, tc := range []struct {
		name       string
		useCurrent bool
		wantBranch string
	}{
		{name: "default mode pins the resolved default branch", useCurrent: false, wantBranch: "main"},
		{name: "current mode pins the checked-out slash branch", useCurrent: true, wantBranch: "release/2026/q4"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			repoDir := testutil.InitGitRepo(t)
			if tc.useCurrent {
				testutil.CreateBranch(t, repoDir, tc.wantBranch)
				testutil.CommitFile(t, repoDir, "current.txt", "current source\n", "current source")
			}
			acceptedCommit := relocatedFeatureTestGitOutput(t, repoDir, "rev-parse", "HEAD")
			relocatedFeatureTestGitOutput(t, repoDir, "remote", "add", "origin", "https://user:secret@example.test/repo.git")
			store := feature.NewStore(t.TempDir())
			cfg := config.NewDefault()
			cfg.Repos["repo-a"] = config.RepoConfig{Path: repoDir}
			mgr := feature.NewManager(store, cfg)
			mgr.Worktrees = mocks.NewMockWorktreeOps()
			mgr.BranchProbeOptions = git.BranchProbeOptions{Runner: git.ScriptedBranchProbeRunnerForTest(func(_ context.Context, _ string, args []string, _ int) (string, string, int, error) {
				if len(args) > 0 && args[0] == "show-ref" {
					return "", "", 1, nil
				}
				return "", "fatal: https://user:secret@example.test/repo.git offline\x1b[31m", 128, errors.New("exit status 128")
			})}

			f, err := mgr.Create("Offline Branch", "test", []string{"repo-a"}, cfg.Defaults.Models, "", "", nil, feature.CreateOptions{
				QueueSetup: true, UseCurrentBranch: tc.useCurrent, PinLocalSources: true,
			})
			if err != nil {
				t.Fatalf("Create() error = %v", err)
			}
			if len(f.CreationWarnings) != 1 {
				t.Fatalf("creation warnings = %+v, want one", f.CreationWarnings)
			}
			warning := f.CreationWarnings[0]
			if warning.Repository != "repo-a" || warning.Branch != f.Repos[0].Branch {
				t.Fatalf("warning = %+v, want repo-a on %q", warning, f.Repos[0].Branch)
			}
			if strings.Contains(warning.Diagnostics, "secret") || strings.ContainsRune(warning.Diagnostics, '\x1b') || !strings.Contains(warning.Diagnostics, "[redacted]") {
				t.Fatalf("diagnostics = %q, want bounded redaction", warning.Diagnostics)
			}
			source := f.Repos[0].Source
			if source == nil || source.Branch != tc.wantBranch || source.Commit != acceptedCommit {
				t.Fatalf("accepted source = %+v, want branch %q at %q", source, tc.wantBranch, acceptedCommit)
			}
		})
	}
}

func TestManagerCreateSelectsSuffixedCandidateWhenOriginConfirmsCollision(t *testing.T) {
	t.Parallel()
	// Controlled Git transport: the probe runs the real git ls-remote against a
	// bare origin; the fixture plants the first generated candidate on that
	// origin from a separate clone so the local show-ref check stays clean and
	// only the responding origin proves the collision.
	localDir, bareDir := testutil.InitPublishReadyGitRepo(t)
	fixtureDir := filepath.Join(t.TempDir(), "fixture")
	if out, err := exec.Command("git", "clone", bareDir, fixtureDir).CombinedOutput(); err != nil {
		t.Fatalf("clone fixture: %s: %v", strings.TrimSpace(string(out)), err)
	}

	var planted bool
	cfg := config.NewDefault()
	cfg.Repos["repo-a"] = config.RepoConfig{Path: localDir}
	mgr := feature.NewManager(feature.NewStore(t.TempDir()), cfg)
	mgr.Worktrees = mocks.NewMockWorktreeOps()
	mgr.PRs = mocks.NewMockPRCloser()
	mgr.BranchProbeOptions = git.BranchProbeOptions{
		OperationTimeout: 10 * time.Second,
		Runner: git.ScriptedBranchProbeRunnerForTest(func(ctx context.Context, repoPath string, args []string, limit int) (string, string, int, error) {
			if len(args) > 0 && args[0] == "ls-remote" && !planted {
				planted = true
				branch := strings.TrimPrefix(args[len(args)-1], "refs/heads/")
				testutil.CreateBranch(t, fixtureDir, branch)
				testutil.SimulatePush(t, fixtureDir, bareDir, branch, branch)
			}
			return git.RunBranchProbeForTest(ctx, repoPath, args, limit)
		}),
	}

	f, err := mgr.Create("Origin Collision", "test", []string{"repo-a"}, cfg.Defaults.Models, "", "", nil, feature.CreateOptions{QueueSetup: true})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if f.Slug == "origin-collision" || !strings.HasPrefix(f.Slug, "origin-collision-") {
		t.Fatalf("slug = %q, want a suffixed candidate after the confirmed origin collision", f.Slug)
	}
	if got := f.Repos[0].Branch; got != git.BranchName(feature.WorkspaceSlug(f.Slug, f.ID)) {
		t.Fatalf("selected branch = %q, want the regenerated candidate", got)
	}
	if len(f.CreationWarnings) != 0 {
		t.Fatalf("creation warnings = %+v, want none from a responding origin", f.CreationWarnings)
	}
	selected := exec.Command("git", "-C", localDir, "show-ref", "--verify", "--quiet", "refs/heads/"+f.Repos[0].Branch)
	if err := selected.Run(); err == nil {
		t.Fatalf("selected candidate %q already exists locally", f.Repos[0].Branch)
	}
}

func TestManagerCreateBoundsBranchProbesAcrossRepositories(t *testing.T) {
	cfg := config.NewDefault()
	for _, name := range []string{"repo-a", "repo-b"} {
		repoDir := testutil.InitGitRepo(t)
		relocatedFeatureTestGitOutput(t, repoDir, "remote", "add", "origin", "https://example.test/"+name+".git")
		cfg.Repos[name] = config.RepoConfig{Path: repoDir}
	}
	mgr := feature.NewManager(feature.NewStore(t.TempDir()), cfg)
	mgr.Worktrees = mocks.NewMockWorktreeOps()
	mgr.BranchProbeBudget = 40 * time.Millisecond
	mgr.BranchProbeOptions = git.BranchProbeOptions{OperationTimeout: time.Second, Runner: git.ScriptedBranchProbeRunnerForTest(func(ctx context.Context, _ string, args []string, _ int) (string, string, int, error) {
		if len(args) > 0 && args[0] == "show-ref" {
			return "", "", 1, nil
		}
		<-ctx.Done()
		return "", ctx.Err().Error(), -1, ctx.Err()
	})}

	started := time.Now()
	f, err := mgr.Create("Bounded Offline", "test", []string{"repo-a", "repo-b"}, cfg.Defaults.Models, "", "", nil, feature.CreateOptions{QueueSetup: true})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("Create() took %s, want bounded overall probe budget", elapsed)
	}
	if len(f.CreationWarnings) != 2 {
		t.Fatalf("creation warnings = %+v, want both unavailable origins", f.CreationWarnings)
	}
}

func TestManagerCreateFailsAfterFiniteBranchCandidateExhaustion(t *testing.T) {
	t.Parallel()
	repoDir := testutil.InitGitRepo(t)
	cfg := config.NewDefault()
	cfg.Repos["repo-a"] = config.RepoConfig{Path: repoDir}
	mgr := feature.NewManager(feature.NewStore(t.TempDir()), cfg)
	mgr.Worktrees = mocks.NewMockWorktreeOps()
	var calls int
	mgr.BranchProbeOptions = git.BranchProbeOptions{Runner: git.ScriptedBranchProbeRunnerForTest(func(context.Context, string, []string, int) (string, string, int, error) {
		calls++
		return "", "", 0, nil
	})}

	_, err := mgr.Create("Exhaust Branches", "test", []string{"repo-a"}, cfg.Defaults.Models, "", "", nil, feature.CreateOptions{QueueSetup: true})
	if err == nil || !strings.Contains(err.Error(), "after 5 attempts") {
		t.Fatalf("Create() error = %v, want finite exhaustion", err)
	}
	if calls != 5 {
		t.Fatalf("probe calls = %d, want 5 local collision attempts", calls)
	}
}
