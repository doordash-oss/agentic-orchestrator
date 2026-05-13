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

package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func GitTestEnv() []string {
	return append(os.Environ(), gitTestEnvOverrides()...)
}

func IsolateGitEnv() func() {
	overrides := gitTestEnvOverrides()
	previous := make(map[string]struct {
		value string
		ok    bool
	}, len(overrides))
	for _, assignment := range overrides {
		key, value, _ := strings.Cut(assignment, "=")
		old, ok := os.LookupEnv(key)
		previous[key] = struct {
			value string
			ok    bool
		}{value: old, ok: ok}
		_ = os.Setenv(key, value)
	}
	return func() {
		for key, old := range previous {
			if old.ok {
				_ = os.Setenv(key, old.value)
			} else {
				_ = os.Unsetenv(key)
			}
		}
	}
}

func gitTestEnvOverrides() []string {
	return []string{
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_EDITOR=:",
		"GIT_SEQUENCE_EDITOR=:",
		"GIT_MERGE_AUTOEDIT=no",
		"GIT_TRACE2_EVENT=",
	}
}

func gitCommandEnv() []string {
	return append(GitTestEnv(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@test.com",
	)
}

func runGit(t testing.TB, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = gitCommandEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// InitMinimalGitRepo creates a temp git repo with no initial commit.
func InitMinimalGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@test.com")
	runGit(t, dir, "config", "user.name", "Test")
	runGit(t, dir, "config", "commit.gpgsign", "false")
	runGit(t, dir, "config", "tag.gpgsign", "false")

	return dir
}

// InitGitRepo creates a temp git repo with an initial commit. Returns the repo path.
func InitGitRepo(t *testing.T) string {
	t.Helper()
	dir := InitMinimalGitRepo(t)

	// Create initial commit
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Test\n"), 0o644); err != nil {
		t.Fatalf("writing README: %v", err)
	}
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-m", "Initial commit")

	return dir
}

// InitBareRemote creates a bare repo and sets it as "origin" for the given repo.
func InitBareRemote(t *testing.T, repoPath string) string {
	t.Helper()
	return PairWithBareRemote(t, repoPath, "main", "main")
}

// PairWithBareRemote creates a bare origin for repoPath and pushes srcBranch to dstBranch.
func PairWithBareRemote(t *testing.T, repoPath, srcBranch, dstBranch string) string {
	t.Helper()
	bareDir := t.TempDir()

	runGit(t, bareDir, "init", "--bare")
	runGit(t, bareDir, "symbolic-ref", "HEAD", "refs/heads/"+dstBranch)

	cmd := exec.Command("git", "remote", "get-url", "origin")
	cmd.Dir = repoPath
	cmd.Env = gitCommandEnv()
	if out, err := cmd.CombinedOutput(); err == nil && strings.TrimSpace(string(out)) != "" {
		runGit(t, repoPath, "remote", "set-url", "origin", bareDir)
	} else {
		runGit(t, repoPath, "remote", "add", "origin", bareDir)
	}
	SimulatePush(t, repoPath, bareDir, srcBranch, dstBranch)

	return bareDir
}

// InitPublishReadyGitRepo creates an initial-commit repo wired to a bare origin.
func InitPublishReadyGitRepo(t *testing.T) (string, string) {
	t.Helper()
	repoPath := InitGitRepo(t)
	bareRemote := PairWithBareRemote(t, repoPath, "main", "main")
	return repoPath, bareRemote
}

// SimulatePush replicates the effect of "git push -u origin <dstBranch>" without
// invoking git-push. It fetches objects from srcRepo into bareRepo (using a
// direct path fetch), then fetches back so the local repo has remote-tracking
// refs and sets the upstream.
func SimulatePush(t *testing.T, srcRepo, bareRepo, srcBranch, dstBranch string) {
	t.Helper()

	// 1. Fetch objects + ref from srcRepo directly into the bare repo.
	//    "git fetch <path> <src>:<dst>" transfers objects and creates the ref.
	runGit(t, bareRepo, "fetch", srcRepo, srcBranch+":refs/heads/"+dstBranch)

	// 2. Fetch from origin so the local repo sees origin/<dstBranch>.
	runGit(t, srcRepo, "fetch", "origin")

	// 3. Set upstream tracking.
	runGit(t, srcRepo, "branch", "--set-upstream-to=origin/"+dstBranch, srcBranch)
}

// CommitFile creates a file with content and commits it. Returns the commit hash.
func CommitFile(t *testing.T, repoPath, filename, content, message string) string {
	t.Helper()
	fpath := filepath.Join(repoPath, filename)
	if err := os.WriteFile(fpath, []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", filename, err)
	}

	runGit(t, repoPath, "add", filename)
	runGit(t, repoPath, "commit", "-m", message)
	return runGit(t, repoPath, "rev-parse", "HEAD")
}

// CreateBranch creates and checks out a new branch.
func CreateBranch(t *testing.T, repoPath, branchName string) {
	t.Helper()
	runGit(t, repoPath, "checkout", "-b", branchName)
}
