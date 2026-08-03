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

package git

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

func TestIsAncestor_AncestorHolds(t *testing.T) {
	t.Parallel()
	repo := testutil.InitGitRepo(t)
	root := runGitAncestorTest(t, repo, "rev-parse", "HEAD")
	testutil.CommitFile(t, repo, "a.txt", "a\n", "child")
	child := runGitAncestorTest(t, repo, "rev-parse", "HEAD")

	if !IsAncestor(repo, root, child) {
		t.Errorf("IsAncestor(root, child) = false; want true (root is ancestor of child)")
	}
}

func TestIsAncestor_NonAncestor(t *testing.T) {
	t.Parallel()
	repo := testutil.InitGitRepo(t)
	root := runGitAncestorTest(t, repo, "rev-parse", "HEAD")
	testutil.CommitFile(t, repo, "a.txt", "a\n", "child")
	child := runGitAncestorTest(t, repo, "rev-parse", "HEAD")

	// child is NOT an ancestor of root (the relationship is the other way).
	if IsAncestor(repo, child, root) {
		t.Errorf("IsAncestor(child, root) = true; want false (child is not an ancestor of root)")
	}
}

func TestIsAncestor_EmptyArguments(t *testing.T) {
	t.Parallel()
	repo := testutil.InitGitRepo(t)
	head := runGitAncestorTest(t, repo, "rev-parse", "HEAD")

	if IsAncestor(repo, "", head) {
		t.Errorf("IsAncestor(empty, head) = true; want false on empty ancestor")
	}
	if IsAncestor(repo, head, "") {
		t.Errorf("IsAncestor(head, empty) = true; want false on empty descendant")
	}
	if IsAncestor(repo, "", "") {
		t.Errorf("IsAncestor(empty, empty) = true; want false")
	}
}

func TestIsAncestor_UnknownCommit(t *testing.T) {
	t.Parallel()
	repo := testutil.InitGitRepo(t)
	head := runGitAncestorTest(t, repo, "rev-parse", "HEAD")
	bogus := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"

	if IsAncestor(repo, bogus, head) {
		t.Errorf("IsAncestor(unknown, head) = true; want false (unknown ancestor)")
	}
	if IsAncestor(repo, head, bogus) {
		t.Errorf("IsAncestor(head, unknown) = true; want false (unknown descendant)")
	}
}

func TestIsAncestor_SelfIsAncestor(t *testing.T) {
	t.Parallel()
	repo := testutil.InitGitRepo(t)
	head := runGitAncestorTest(t, repo, "rev-parse", "HEAD")
	if !IsAncestor(repo, head, head) {
		t.Errorf("IsAncestor(head, head) = false; want true (a commit is its own ancestor)")
	}
}

// runGitAncestorTest runs a git command in dir and returns the trimmed stdout.
func runGitAncestorTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(testutil.GitTestEnv(),
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@test.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}
