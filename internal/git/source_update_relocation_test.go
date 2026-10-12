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

// This file relocates the source-update response-loss-after-CAS case from the
// server source-reconcile suite. It lives in the git package's own tests
// because the runner boundaries and their result type are internal to this
// package; the scripted-runner adapters in export_test.go stay visible only
// to this package's tests.

package git_test

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// sourceUpdateRelocationGit runs git in dir (or the temp root when empty) and
// fails the test on error.
func sourceUpdateRelocationGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(testutil.GitTestEnv(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@test.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestUpdateSourceFromOriginResponseLossAfterCASProvesTarget proves that a
// landed compare-and-swap followed by a lost post-mutation verification
// reports unavailability while the ref actually advanced: the attempt proves
// nothing either way, and a later settlement read observes the advanced tip.
func TestUpdateSourceFromOriginResponseLossAfterCASProvesTarget(t *testing.T) {
	tmp := t.TempDir()
	bare := filepath.Join(tmp, "origin.git")
	repo := filepath.Join(tmp, "repo")

	sourceUpdateRelocationGit(t, "", "init", "--bare", bare)
	sourceUpdateRelocationGit(t, bare, "symbolic-ref", "HEAD", "refs/heads/main")
	sourceUpdateRelocationGit(t, "", "init", "--initial-branch=main", repo)
	sourceUpdateRelocationGit(t, repo, "commit", "--allow-empty", "-m", "initial")
	sourceUpdateRelocationGit(t, repo, "remote", "add", "origin", bare)
	sourceUpdateRelocationGit(t, bare, "fetch", repo, "main:refs/heads/main")
	sourceUpdateRelocationGit(t, repo, "fetch", "origin")
	sourceUpdateRelocationGit(t, repo, "checkout", "-b", "work")
	sourceUpdateRelocationGit(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")

	writer := filepath.Join(tmp, "writer")
	sourceUpdateRelocationGit(t, "", "clone", bare, writer)
	sourceUpdateRelocationGit(t, writer, "commit", "--allow-empty", "-m", "remote one")
	sourceUpdateRelocationGit(t, writer, "commit", "--allow-empty", "-m", "remote two")
	sourceUpdateRelocationGit(t, writer, "push", "origin", "main")

	localTip := sourceUpdateRelocationGit(t, repo, "rev-parse", "refs/heads/main")
	originTip := sourceUpdateRelocationGit(t, writer, "rev-parse", "refs/heads/main")
	headRef := sourceUpdateRelocationGit(t, repo, "symbolic-ref", "HEAD")
	headSHA := sourceUpdateRelocationGit(t, repo, "rev-parse", "HEAD")

	// The compare-and-swap lands, then the post-mutation verification is
	// lost: the attempt reports unavailable and proves nothing, while the
	// settlement read observes the advanced tip.
	var casDone atomic.Bool
	options := git.SourceUpdateOptions{
		UpdateRefRunner: git.ScriptedSourceUpdateRefRunnerForTest(func(ctx context.Context, repoPath, stdin string, args []string, diagnosticLimit int) (string, string, int, error) {
			stdout, diagnostics, exitCode, err := git.RunSourceUpdateRefForTest(ctx, repoPath, stdin, args, diagnosticLimit)
			if exitCode == 0 {
				casDone.Store(true)
			}
			return stdout, diagnostics, exitCode, err
		}),
		OriginCheckOptions: git.OriginCheckOptions{Runner: git.ScriptedBranchProbeRunnerForTest(func(ctx context.Context, repoPath string, args []string, diagnosticLimit int) (string, string, int, error) {
			if casDone.Load() && len(args) > 0 && args[0] == "rev-parse" {
				return "", "connection lost while verifying the ref update", -1, errors.New("connection lost")
			}
			return git.RunBranchProbeForTest(ctx, repoPath, args, diagnosticLimit)
		})},
	}

	expected := git.SourceUpdateExpectation{
		Mode:              git.LocalSourceModeDefault,
		Branch:            "main",
		OriginBranch:      "main",
		ExpectedLocalSHA:  localTip,
		ExpectedOriginSHA: originTip,
		CheckoutHeadRef:   headRef,
		CheckoutHeadSHA:   headSHA,
	}
	_, err := git.UpdateSourceFromOrigin(context.Background(), repo, expected, options)
	var unavailable *git.SourceUpdateUnavailableError
	if err == nil || !errors.As(err, &unavailable) {
		t.Fatalf("UpdateSourceFromOrigin() error = %v; want source-update unavailability", err)
	}
	if got := sourceUpdateRelocationGit(t, repo, "rev-parse", "refs/heads/main"); got != originTip {
		t.Fatalf("refs/heads/main = %s; want the advanced tip the CAS landed (%s)", got, originTip)
	}
}
