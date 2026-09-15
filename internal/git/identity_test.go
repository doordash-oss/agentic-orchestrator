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
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func runTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git -C %s %v: %v\n%s", dir, args, err, out)
	}
	return string(out)
}

func initIdentityRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runTestGit(t, dir, "init", "-b", "main")
	runTestGit(t, dir, "config", "user.email", "test@example.com")
	runTestGit(t, dir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, dir, "add", ".")
	runTestGit(t, dir, "commit", "-m", "initial")
	return dir
}

func TestResolveRepoIdentityBindsCanonicalCheckoutAndCommonDir(t *testing.T) {
	dir := initIdentityRepo(t)
	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	identity, ok := ResolveRepoIdentity(dir)
	if !ok {
		t.Fatal("expected resolvable identity")
	}
	if identity.Path != canonical {
		t.Errorf("path = %q, want %q", identity.Path, canonical)
	}
	if identity.CommonDir != filepath.Join(canonical, ".git") {
		t.Errorf("common dir = %q, want %q", identity.CommonDir, filepath.Join(canonical, ".git"))
	}
	if identity.Device == 0 || identity.Inode == 0 {
		t.Errorf("expected nonzero filesystem identity, got %+v", identity)
	}
	if FormatIdentityDevice(identity.Device) == "" || FormatIdentityInode(identity.Inode) == "" {
		t.Error("expected decimal wire text")
	}
}

func TestRepoIdentityStableAcrossCommitsAndCheckouts(t *testing.T) {
	dir := initIdentityRepo(t)
	before, ok := ResolveRepoIdentity(dir)
	if !ok {
		t.Fatal("expected resolvable identity")
	}
	if err := os.WriteFile(filepath.Join(dir, "two.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, dir, "add", ".")
	runTestGit(t, dir, "commit", "-m", "second")
	runTestGit(t, dir, "checkout", "-b", "topic")
	after, ok := ResolveRepoIdentity(dir)
	if !ok {
		t.Fatal("expected resolvable identity after commits and checkout")
	}
	if !before.Equal(after) {
		t.Errorf("identity changed across commits/checkout: %+v -> %+v", before, after)
	}
}

func TestRepoIdentityResolvesUnbornRepository(t *testing.T) {
	dir := t.TempDir()
	runTestGit(t, dir, "init", "-b", "main")
	identity, ok := ResolveRepoIdentity(dir)
	if !ok {
		t.Fatal("expected resolvable identity for an unborn repository")
	}
	if identity.Path == "" || identity.CommonDir == "" {
		t.Errorf("unexpected empty identity: %+v", identity)
	}
}

func TestRepoIdentityDistinguishesLinkedWorktrees(t *testing.T) {
	main := initIdentityRepo(t)
	canonicalMain, err := filepath.EvalSymlinks(main)
	if err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(t.TempDir(), "linked")
	runTestGit(t, main, "worktree", "add", worktree, "-b", "topic")

	mainIdentity, ok := ResolveRepoIdentity(main)
	if !ok {
		t.Fatal("expected resolvable main identity")
	}
	linkedIdentity, ok := ResolveRepoIdentity(worktree)
	if !ok {
		t.Fatal("expected resolvable linked worktree identity")
	}
	if mainIdentity.Equal(linkedIdentity) {
		t.Fatalf("linked worktree identity must stay distinguishable: %+v", linkedIdentity)
	}
	if linkedIdentity.Path == canonicalMain {
		t.Errorf("linked worktree path = main checkout path %q", linkedIdentity.Path)
	}
	// Linked worktrees share the Git common directory with the main checkout,
	// so the distinction must come from the canonical checkout path.
	if linkedIdentity.CommonDir != mainIdentity.CommonDir {
		t.Errorf("expected shared common dir, got %q and %q", linkedIdentity.CommonDir, mainIdentity.CommonDir)
	}
}

func TestRepoIdentityInvalidatedByCheckoutReplacement(t *testing.T) {
	dir := initIdentityRepo(t)
	before, ok := ResolveRepoIdentity(dir)
	if !ok {
		t.Fatal("expected resolvable identity")
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, dir, "init", "-b", "main")
	after, ok := ResolveRepoIdentity(dir)
	if !ok {
		t.Fatal("expected resolvable identity after replacement")
	}
	if before.Equal(after) {
		t.Errorf("identity survived checkout replacement at the same path: %+v", after)
	}
}

func TestRepoIdentityInvalidatedByGitDirectoryReplacement(t *testing.T) {
	dir := initIdentityRepo(t)
	before, ok := ResolveRepoIdentity(dir)
	if !ok {
		t.Fatal("expected resolvable identity")
	}
	if err := os.RemoveAll(filepath.Join(dir, ".git")); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, dir, "init", "-b", "main")
	after, ok := ResolveRepoIdentity(dir)
	if !ok {
		t.Fatal("expected resolvable identity after git directory replacement")
	}
	if before.Equal(after) {
		t.Errorf("identity survived git directory replacement: %+v", after)
	}
}

func TestResolveRepoIdentityRejectsNonRepositories(t *testing.T) {
	plain := t.TempDir()
	if _, ok := ResolveRepoIdentity(plain); ok {
		t.Error("plain directory must not resolve")
	}
	broken := t.TempDir()
	if err := os.WriteFile(filepath.Join(broken, ".git"), []byte("gitdir: /nonexistent/repo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := ResolveRepoIdentity(broken); ok {
		t.Error("worktree pointer to a missing repository must not resolve")
	}
}
