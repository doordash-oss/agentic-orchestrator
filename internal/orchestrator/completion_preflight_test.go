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
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
)

type completionFixture struct {
	orchestrator *Orchestrator
	worktrees    map[string]string
}

func newCompletionFixture(t *testing.T) completionFixture {
	t.Helper()
	store := feature.NewStore(t.TempDir())
	manager := feature.NewManager(store, config.NewDefault())
	repos := make([]feature.FeatureRepo, 0, 2)
	repoStates := make(map[string]*feature.RepoState, 2)
	worktrees := make(map[string]string, 2)
	for _, name := range []string{"repo-a", "repo-b"} {
		wt := initGitWorktree(t, name)
		worktrees[name] = wt
		repos = append(repos, feature.FeatureRepo{
			Name:         name,
			Path:         wt,
			WorktreePath: wt,
			Branch:       "feature/x",
			BaseBranch:   "main",
			Publishable:  boolPtr(true),
		})
		repoStates[name] = &feature.RepoState{Touched: true}
	}
	repoStates["repo-a"].PRURL = "https://github.example/repo-a/pull/1"
	f := &feature.Feature{
		ID:            "feat-completion",
		Name:          "Completion",
		Slug:          "completion",
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
	return completionFixture{
		orchestrator: New(Deps{Lifecycle: manager, Store: store}, Hooks{}),
		worktrees:    worktrees,
	}
}

func newCompletionOrchestrator(t *testing.T) *Orchestrator {
	t.Helper()
	return newCompletionFixture(t).orchestrator
}

func initGitWorktree(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "--initial-branch=main"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
		{"commit", "--allow-empty", "-m", "initial"},
	} {
		runCompletionGit(t, dir, args...)
	}
	return dir
}

func runCompletionGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s in %s: %s: %v", strings.Join(args, " "), dir, strings.TrimSpace(string(out)), err)
	}
}

func boolPtr(b bool) *bool { return &b }

func TestCompletionPreflightEnumeratesRepoStatus(t *testing.T) {
	t.Parallel()
	o := newCompletionOrchestrator(t)
	result, err := o.CompletionPreflight("feat-completion")
	if err != nil {
		t.Fatalf("CompletionPreflight: %v", err)
	}
	if result.FeatureID != "feat-completion" {
		t.Fatalf("feature_id = %q", result.FeatureID)
	}
	if len(result.Repos) != 2 {
		t.Fatalf("repos len = %d; want 2", len(result.Repos))
	}
	found := map[string]string{}
	for _, r := range result.Repos {
		found[r.Repo] = r.Status
	}
	if found["repo-a"] != completionStatusAlreadyPublished {
		t.Fatalf("repo-a status = %q; want %q", found["repo-a"], completionStatusAlreadyPublished)
	}
	if found["repo-b"] != completionStatusEligible {
		t.Fatalf("repo-b status = %q; want %q", found["repo-b"], completionStatusEligible)
	}
	if result.SourceRevision == "" {
		t.Fatal("source_revision is empty; want non-empty")
	}
}

func TestCompletionPreflightCanMarkDone(t *testing.T) {
	t.Parallel()
	o := newCompletionOrchestrator(t)
	result, err := o.CompletionPreflight("feat-completion")
	if err != nil {
		t.Fatalf("CompletionPreflight: %v", err)
	}
	if !result.CanMarkDone {
		t.Fatalf("can_mark_done = false; want true for StatusPublished")
	}
}

func TestCompletionPreflightSourceRevisionIsStable(t *testing.T) {
	t.Parallel()
	o := newCompletionOrchestrator(t)
	r1, err := o.CompletionPreflight("feat-completion")
	if err != nil {
		t.Fatalf("first preflight: %v", err)
	}
	r2, err := o.CompletionPreflight("feat-completion")
	if err != nil {
		t.Fatalf("second preflight: %v", err)
	}
	if r1.SourceRevision != r2.SourceRevision {
		t.Fatalf("source_revision changed: %q vs %q", r1.SourceRevision, r2.SourceRevision)
	}
}

func TestRepositoryDiffListsChangedFiles(t *testing.T) {
	t.Parallel()
	fix := newCompletionFixture(t)
	worktree := fix.worktrees["repo-a"]
	if err := os.WriteFile(filepath.Join(worktree, "changed.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatalf("write changed file: %v", err)
	}

	result, err := fix.orchestrator.RepositoryDiff("feat-completion", "repo-a", "")
	if err != nil {
		t.Fatalf("RepositoryDiff: %v", err)
	}
	if result.Repo != "repo-a" {
		t.Fatalf("repo = %q; want repo-a", result.Repo)
	}
	if result.PartialFailure != "" {
		t.Fatalf("partial_failure = %q; want empty", result.PartialFailure)
	}
	if len(result.Files) != 1 {
		t.Fatalf("files len = %d; want 1", len(result.Files))
	}
	if result.Files[0].Path != "changed.txt" {
		t.Fatalf("file path = %q; want changed.txt", result.Files[0].Path)
	}
}

func TestRepositoryDiffReportsNotFoundRepo(t *testing.T) {
	t.Parallel()
	o := newCompletionOrchestrator(t)
	result, err := o.RepositoryDiff("feat-completion", "nonexistent", "")
	if err != nil {
		t.Fatalf("RepositoryDiff: %v", err)
	}
	if result.PartialFailure != "repository not found" {
		t.Fatalf("partial_failure = %q; want 'repository not found'", result.PartialFailure)
	}
}

func TestRepositoryDiffWithFilePathReturnsContent(t *testing.T) {
	t.Parallel()
	fix := newCompletionFixture(t)
	worktree := fix.worktrees["repo-a"]
	readme := filepath.Join(worktree, "README.md")
	if err := os.WriteFile(readme, []byte("initial\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	runCompletionGit(t, worktree, "add", "README.md")
	runCompletionGit(t, worktree, "commit", "-m", "add readme")
	if err := os.WriteFile(readme, []byte("updated\n"), 0o644); err != nil {
		t.Fatalf("update README: %v", err)
	}

	result, err := fix.orchestrator.RepositoryDiff("feat-completion", "repo-a", "README.md")
	if err != nil {
		t.Fatalf("RepositoryDiff: %v", err)
	}
	if result.Repo != "repo-a" {
		t.Fatalf("repo = %q; want repo-a", result.Repo)
	}
	if !strings.Contains(result.FileDiff, "+updated") {
		t.Fatalf("file_diff = %q; want updated line", result.FileDiff)
	}
}

func TestRepositoryDiffWithFilePathReportsDiffErrors(t *testing.T) {
	t.Parallel()
	fix := newCompletionFixture(t)
	worktree := fix.worktrees["repo-a"]
	runCompletionGit(t, worktree, "branch", "-m", "feature/x")

	result, err := fix.orchestrator.RepositoryDiff("feat-completion", "repo-a", "README.md")
	if err != nil {
		t.Fatalf("RepositoryDiff: %v", err)
	}
	if !strings.Contains(result.PartialFailure, "main") {
		t.Fatalf("partial_failure = %q; want propagated diff error", result.PartialFailure)
	}
	if result.FileUnavailable {
		t.Fatal("FileUnavailable = true; want git error reported as partial_failure")
	}
}

func TestRepositoryDiffTextNoNewlineMarkerIsNotBinary(t *testing.T) {
	t.Parallel()
	fix := newCompletionFixture(t)
	worktree := fix.worktrees["repo-a"]
	readme := filepath.Join(worktree, "README.md")
	if err := os.WriteFile(readme, []byte("with newline\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	runCompletionGit(t, worktree, "add", "README.md")
	runCompletionGit(t, worktree, "commit", "-m", "add readme")
	if err := os.WriteFile(readme, []byte("without newline"), 0o644); err != nil {
		t.Fatalf("rewrite README: %v", err)
	}

	result, err := fix.orchestrator.RepositoryDiff("feat-completion", "repo-a", "README.md")
	if err != nil {
		t.Fatalf("RepositoryDiff: %v", err)
	}
	if result.FileBinary {
		t.Fatalf("FileBinary = true; want false for text diff with no-newline marker")
	}
	if result.FileUnavailable {
		t.Fatal("FileUnavailable = true; want diff content")
	}
	if !strings.Contains(result.FileDiff, "\\ No newline at end of file") {
		t.Fatalf("file_diff = %q; want no-newline marker", result.FileDiff)
	}
}

func TestRepositoryDiffGitBinaryPatchIsBinary(t *testing.T) {
	t.Parallel()
	if !isBinaryPatch("diff --git a/logo.png b/logo.png\nGIT binary patch\nliteral 3\nabc") {
		t.Fatal("isBinaryPatch = false; want true for GIT binary patch")
	}
}

// pendingDeliveryWorktree returns a worktree checked out on feature/x whose
// origin/feature/x tracking ref exists, so a destination ref resolves.
func pendingDeliveryWorktree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bare := filepath.Join(t.TempDir(), "remote.git")
	runCompletionGit(t, "", "init", "--bare", "--initial-branch=main", bare)
	for _, args := range [][]string{
		{"init", "--initial-branch=main"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
		{"commit", "--allow-empty", "-m", "initial"},
		{"checkout", "-b", "feature/x"},
		{"remote", "add", "origin", bare},
		{"push", "-u", "origin", "feature/x"},
	} {
		runCompletionGit(t, dir, args...)
	}
	return dir
}

func newPendingDeliveryOrchestrator(t *testing.T, status feature.Status, repo feature.FeatureRepo, state *feature.RepoState) *Orchestrator {
	t.Helper()
	store := feature.NewStore(t.TempDir())
	manager := feature.NewManager(store, config.NewDefault())
	f := &feature.Feature{
		ID:            "feat-pending",
		Name:          "Pending",
		Slug:          "pending",
		Status:        status,
		SchemaVersion: feature.SchemaVersionCurrent,
		ActiveRun:     1,
		RunCount:      1,
		Repos:         []feature.FeatureRepo{repo},
		RepoStates:    map[string]*feature.RepoState{repo.Name: state},
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}
	return New(Deps{Lifecycle: manager, Store: store}, Hooks{})
}

// newPendingDeliveryOrchestratorWithWorktrees is newPendingDeliveryOrchestrator
// plus a real WorktreeOps dep, so applyPendingDelivery can enumerate dirty
// files via InspectCleanliness.
func newPendingDeliveryOrchestratorWithWorktrees(t *testing.T, status feature.Status, repo feature.FeatureRepo, state *feature.RepoState) *Orchestrator {
	t.Helper()
	store := feature.NewStore(t.TempDir())
	manager := feature.NewManager(store, config.NewDefault())
	f := &feature.Feature{
		ID:            "feat-pending",
		Name:          "Pending",
		Slug:          "pending",
		Status:        status,
		SchemaVersion: feature.SchemaVersionCurrent,
		ActiveRun:     1,
		RunCount:      1,
		Repos:         []feature.FeatureRepo{repo},
		RepoStates:    map[string]*feature.RepoState{repo.Name: state},
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}
	return New(Deps{Lifecycle: manager, Store: store, Worktrees: git.NewWorktreeManager(t.TempDir())}, Hooks{})
}

func pendingDeliveryRepo(t *testing.T, publishable bool) feature.FeatureRepo {
	t.Helper()
	wt := pendingDeliveryWorktree(t)
	return feature.FeatureRepo{
		Name:         "repo-a",
		Path:         wt,
		WorktreePath: wt,
		Branch:       "feature/x",
		BaseBranch:   "main",
		Publishable:  boolPtr(publishable),
	}
}

func onlyPendingRepo(t *testing.T, o *Orchestrator) CompletionRepoResult {
	t.Helper()
	result, err := o.CompletionPreflight("feat-pending")
	if err != nil {
		t.Fatalf("CompletionPreflight: %v", err)
	}
	if len(result.Repos) != 1 {
		t.Fatalf("repos len = %d; want 1", len(result.Repos))
	}
	return result.Repos[0]
}

func TestCompletionPreflightReportsUnpublishedChanges(t *testing.T) {
	t.Parallel()
	repo := pendingDeliveryRepo(t, true)
	runCompletionGit(t, repo.WorktreePath, "commit", "--allow-empty", "-m", "later pass")
	o := newPendingDeliveryOrchestrator(t, feature.StatusCodeReady, repo,
		&feature.RepoState{Touched: true, PRURL: "https://github.example/repo-a/pull/1"})

	got := onlyPendingRepo(t, o)
	if got.Status != completionStatusUnpublishedChanges {
		t.Errorf("status = %q; want %q", got.Status, completionStatusUnpublishedChanges)
	}
	if got.PendingCommits != 1 {
		t.Errorf("pending commits = %d; want 1", got.PendingCommits)
	}
	if got.PendingDirty {
		t.Error("pending dirty = true; want false")
	}
	if got.PushMode != completionPushModeFastForward {
		t.Errorf("push mode = %q; want %q", got.PushMode, completionPushModeFastForward)
	}
}

func TestCompletionPreflightKeepsAlreadyPublishedWhenDelivered(t *testing.T) {
	t.Parallel()
	repo := pendingDeliveryRepo(t, true)
	o := newPendingDeliveryOrchestrator(t, feature.StatusCodeReady, repo,
		&feature.RepoState{Touched: true, PRURL: "https://github.example/repo-a/pull/1"})

	got := onlyPendingRepo(t, o)
	if got.Status != completionStatusAlreadyPublished {
		t.Errorf("status = %q; want %q", got.Status, completionStatusAlreadyPublished)
	}
	if got.PendingCommits != 0 {
		t.Errorf("pending commits = %d; want 0", got.PendingCommits)
	}
}

func TestCompletionPreflightReportsRewritePushMode(t *testing.T) {
	t.Parallel()
	repo := pendingDeliveryRepo(t, true)
	runCompletionGit(t, repo.WorktreePath, "commit", "--allow-empty", "-m", "pushed later")
	runCompletionGit(t, repo.WorktreePath, "push", "origin", "feature/x")
	runCompletionGit(t, repo.WorktreePath, "reset", "--hard", "HEAD~1")
	runCompletionGit(t, repo.WorktreePath, "commit", "--allow-empty", "-m", "rewritten")
	o := newPendingDeliveryOrchestrator(t, feature.StatusCodeReady, repo,
		&feature.RepoState{Touched: true, PRURL: "https://github.example/repo-a/pull/1"})

	got := onlyPendingRepo(t, o)
	if got.Status != completionStatusUnpublishedChanges {
		t.Errorf("status = %q; want %q", got.Status, completionStatusUnpublishedChanges)
	}
	if got.PushMode != completionPushModeRewrite {
		t.Errorf("push mode = %q; want %q", got.PushMode, completionPushModeRewrite)
	}
}

func TestCompletionPreflightReportsUnmergedChanges(t *testing.T) {
	t.Parallel()
	repo := pendingDeliveryRepo(t, false)
	runCompletionGit(t, repo.WorktreePath, "commit", "--allow-empty", "-m", "later pass")
	o := newPendingDeliveryOrchestrator(t, feature.StatusDone, repo, &feature.RepoState{Touched: true})

	got := onlyPendingRepo(t, o)
	if got.Status != completionStatusUnmergedChanges {
		t.Errorf("status = %q; want %q", got.Status, completionStatusUnmergedChanges)
	}
	if got.PendingCommits != 1 {
		t.Errorf("pending commits = %d; want 1", got.PendingCommits)
	}
	if got.PushMode != "" {
		t.Errorf("push mode = %q; want empty for a local-only repository", got.PushMode)
	}
}

func TestCompletionPreflightListsDirtyFiles(t *testing.T) {
	t.Parallel()
	repo := pendingDeliveryRepo(t, true)
	if err := os.WriteFile(filepath.Join(repo.WorktreePath, "tracked.txt"), []byte("committed"), 0o644); err != nil {
		t.Fatalf("write tracked file: %v", err)
	}
	runCompletionGit(t, repo.WorktreePath, "add", "tracked.txt")
	runCompletionGit(t, repo.WorktreePath, "commit", "-m", "add tracked file")
	if err := os.WriteFile(filepath.Join(repo.WorktreePath, "tracked.txt"), []byte("modified"), 0o644); err != nil {
		t.Fatalf("modify tracked file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo.WorktreePath, "untracked.txt"), []byte("new file"), 0o644); err != nil {
		t.Fatalf("write untracked file: %v", err)
	}
	o := newPendingDeliveryOrchestratorWithWorktrees(t, feature.StatusCodeReady, repo,
		&feature.RepoState{Touched: true, PRURL: "https://github.example/repo-a/pull/1"})

	got := onlyPendingRepo(t, o)
	if !got.PendingDirty {
		t.Fatal("pending dirty = false; want true")
	}
	if got.PendingDirtyFileTotal != 2 {
		t.Errorf("pending dirty file total = %d; want 2", got.PendingDirtyFileTotal)
	}
	want := map[string]bool{"tracked.txt": true, "untracked.txt": true}
	if len(got.PendingDirtyFiles) != 2 {
		t.Fatalf("pending dirty files = %v; want 2 entries", got.PendingDirtyFiles)
	}
	for _, name := range got.PendingDirtyFiles {
		if !want[name] {
			t.Errorf("unexpected dirty file %q", name)
		}
	}
}

func TestCompletionPreflightWithoutWorktreesLeavesDirtyFilesUnset(t *testing.T) {
	t.Parallel()
	repo := pendingDeliveryRepo(t, true)
	if err := os.WriteFile(filepath.Join(repo.WorktreePath, "README.md"), []byte("tracked change"), 0o644); err != nil {
		t.Fatalf("write tracked file: %v", err)
	}
	o := newPendingDeliveryOrchestrator(t, feature.StatusCodeReady, repo,
		&feature.RepoState{Touched: true, PRURL: "https://github.example/repo-a/pull/1"})

	got := onlyPendingRepo(t, o)
	if !got.PendingDirty {
		t.Fatal("pending dirty = false; want true")
	}
	if got.PendingDirtyFiles != nil || got.PendingDirtyFileTotal != 0 {
		t.Errorf("dirty files leaked without Worktrees dep: files=%v total=%d", got.PendingDirtyFiles, got.PendingDirtyFileTotal)
	}
}

func TestCompletionPreflightUnresolvedDestinationKeepsStatus(t *testing.T) {
	t.Parallel()
	wt := initGitWorktree(t, "no-remote")
	repo := feature.FeatureRepo{
		Name: "repo-a", Path: wt, WorktreePath: wt,
		Branch: "feature/x", BaseBranch: "main", Publishable: boolPtr(true),
	}
	o := newPendingDeliveryOrchestrator(t, feature.StatusCodeReady, repo,
		&feature.RepoState{Touched: true, PRURL: "https://github.example/repo-a/pull/1"})

	got := onlyPendingRepo(t, o)
	if got.Status != completionStatusAlreadyPublished {
		t.Errorf("status = %q; want %q", got.Status, completionStatusAlreadyPublished)
	}
	if got.PendingCommits != 0 || got.PendingDirty || got.PushMode != "" {
		t.Errorf("pending fields set without a resolvable destination: %+v", got)
	}
}
