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
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
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
	// A one-layer stack whose repo-a entry carries a recorded pull request:
	// repo-a reads already-published through the stack read model, while
	// repo-b — no entry — stays eligible. The entries carry no SHAs, so the
	// live layer measurement degrades to the no-commits shape.
	f := &feature.Feature{
		ID:            "feat-completion",
		Name:          "Completion",
		Slug:          "completion",
		Status:        feature.StatusPublished,
		SchemaVersion: feature.SchemaVersionCurrent,
		ActiveRun:     1,
		RunCount:      1,
		Stack: []feature.StackLayer{{
			Position: 1, Title: "Bootstrap", Branch: "feature/x",
			Repos: map[string]feature.StackRepoEntry{
				"repo-a": {PRURL: "https://github.example/repo-a/pull/1", PRState: feature.StackPRStateOpen},
			},
		}},
		Repos:      repos,
		RepoStates: repoStates,
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

func TestKnowledgeBaseCompletionContractUsesSessionRepoMetadata(t *testing.T) {
	t.Parallel()

	store := feature.NewStore(t.TempDir())
	sessions := mocks.NewMockSessionManager()
	const (
		sessionID = "abc123-kbv2-root-a-service-0123456789ab"
		repoName  = "root-a/service"
	)
	sess := mocks.NewMockSessionView(sessionID, "abc123")
	sess.RepoNameVal = repoName
	sessions.GetSessionFn = func(id string) ports.SessionView {
		if id != sessionID {
			t.Fatalf("GetSession(%q), want %q", id, sessionID)
		}
		return sess
	}
	o := New(Deps{Store: store, Sessions: sessions}, Hooks{})

	role, artifactDir, repoNames, err := o.singleShotCompletionContract(
		&feature.Feature{ID: "abc123"},
		sessionID,
		feature.PhaseKnowledgeBase,
	)
	if err != nil {
		t.Fatalf("singleShotCompletionContract: %v", err)
	}
	if role != agent.RoleKnowledgeBaseBuilder {
		t.Errorf("role = %q, want %q", role, agent.RoleKnowledgeBaseBuilder)
	}
	if want := agent.KBStateDir(store.BaseDir, repoName); artifactDir != want {
		t.Errorf("artifactDir = %q, want %q", artifactDir, want)
	}
	if len(repoNames) != 1 || repoNames[0] != repoName {
		t.Errorf("repoNames = %v, want [%q]", repoNames, repoName)
	}
}

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
	if result.PartialFailure != nil {
		t.Fatalf("partial failure = %#v; want none", result.PartialFailure)
	}
	if len(result.Files) != 1 {
		t.Fatalf("files len = %d; want 1", len(result.Files))
	}
	if result.Files[0].Path != "changed.txt" {
		t.Fatalf("file path = %q; want changed.txt", result.Files[0].Path)
	}
}

func TestRepositoryDiffUnknownRepoIsNotFoundError(t *testing.T) {
	t.Parallel()
	o := newCompletionOrchestrator(t)
	_, err := o.RepositoryDiff("feat-completion", "nonexistent", "")
	if !errors.Is(err, feature.ErrRepositoryNotFound) {
		t.Fatalf("RepositoryDiff error = %v; want feature.ErrRepositoryNotFound", err)
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
	if result.PartialFailure == nil || result.PartialFailure.Kind != RepositoryDiffFailed ||
		!strings.Contains(result.PartialFailure.Err.Error(), "main") {
		t.Fatalf("partial failure = %#v; want propagated diff error naming main", result.PartialFailure)
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

func newPendingDeliveryOrchestrator(t *testing.T, status feature.Status, repo feature.FeatureRepo, state *feature.RepoState, stack []feature.StackLayer) *Orchestrator {
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
		Stack:         stack,
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
func newPendingDeliveryOrchestratorWithWorktrees(t *testing.T, status feature.Status, repo feature.FeatureRepo, state *feature.RepoState, stack []feature.StackLayer) *Orchestrator {
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
		Stack:         stack,
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

// pendingDeliveryStack builds a one-layer stack over the pending-delivery
// repository's feature branch with the given per-repo entry, so the stacked
// preflight measurement runs against the same worktree.
func pendingDeliveryStack(entry feature.StackRepoEntry) []feature.StackLayer {
	return []feature.StackLayer{{
		Position: 1, Title: "Bootstrap", Branch: "feature/x",
		Repos: map[string]feature.StackRepoEntry{"repo-a": entry},
	}}
}

// pendingDeliveryEntry shapes a delivered-layer entry: a recorded pull
// request whose tip and last-pushed SHA the test controls.
func pendingDeliveryEntry(t *testing.T, tip, lastPushed string) feature.StackRepoEntry {
	t.Helper()
	return feature.StackRepoEntry{
		TipSHA:        tip,
		LastPushedSHA: lastPushed,
		PRURL:         "https://github.example/repo-a/pull/1",
		PRState:       feature.StackPRStateOpen,
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
	tip := completionGitOutput(t, repo.WorktreePath, "rev-parse", "HEAD")
	base := completionGitOutput(t, repo.WorktreePath, "rev-parse", "main")
	stack := pendingDeliveryStack(pendingDeliveryEntry(t, tip, base))
	o := newPendingDeliveryOrchestrator(t, feature.StatusCodeReady, repo,
		&feature.RepoState{Touched: true}, stack)

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
	runCompletionGit(t, repo.WorktreePath, "commit", "--allow-empty", "-m", "delivered layer")
	runCompletionGit(t, repo.WorktreePath, "push", "origin", "feature/x")
	tip := completionGitOutput(t, repo.WorktreePath, "rev-parse", "HEAD")
	stack := pendingDeliveryStack(pendingDeliveryEntry(t, tip, tip))
	o := newPendingDeliveryOrchestrator(t, feature.StatusCodeReady, repo,
		&feature.RepoState{Touched: true}, stack)

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
	pushed := completionGitOutput(t, repo.WorktreePath, "rev-parse", "HEAD")
	runCompletionGit(t, repo.WorktreePath, "reset", "--hard", "HEAD~1")
	runCompletionGit(t, repo.WorktreePath, "commit", "--allow-empty", "-m", "rewritten")
	rewritten := completionGitOutput(t, repo.WorktreePath, "rev-parse", "HEAD")
	stack := pendingDeliveryStack(pendingDeliveryEntry(t, rewritten, pushed))
	o := newPendingDeliveryOrchestrator(t, feature.StatusCodeReady, repo,
		&feature.RepoState{Touched: true}, stack)

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
	o := newPendingDeliveryOrchestrator(t, feature.StatusDone, repo, &feature.RepoState{Touched: true}, nil)

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
		&feature.RepoState{Touched: true},
		pendingDeliveryStack(pendingDeliveryEntry(t,
			completionGitOutput(t, repo.WorktreePath, "rev-parse", "HEAD"),
			completionGitOutput(t, repo.WorktreePath, "rev-parse", "HEAD"))))

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

func TestCompletionPreflightDedupesStagedAndFurtherModifiedFile(t *testing.T) {
	t.Parallel()
	repo := pendingDeliveryRepo(t, true)
	path := filepath.Join(repo.WorktreePath, "both.txt")
	if err := os.WriteFile(path, []byte("committed"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	runCompletionGit(t, repo.WorktreePath, "add", "both.txt")
	runCompletionGit(t, repo.WorktreePath, "commit", "-m", "add both.txt")
	// Stage a modification, then modify again without staging: git status
	// reports this single path as "MM" — staged AND unstaged.
	if err := os.WriteFile(path, []byte("staged change"), 0o644); err != nil {
		t.Fatalf("stage modification: %v", err)
	}
	runCompletionGit(t, repo.WorktreePath, "add", "both.txt")
	if err := os.WriteFile(path, []byte("further unstaged change"), 0o644); err != nil {
		t.Fatalf("unstaged modification: %v", err)
	}
	o := newPendingDeliveryOrchestratorWithWorktrees(t, feature.StatusCodeReady, repo,
		&feature.RepoState{Touched: true},
		pendingDeliveryStack(pendingDeliveryEntry(t,
			completionGitOutput(t, repo.WorktreePath, "rev-parse", "HEAD"),
			completionGitOutput(t, repo.WorktreePath, "rev-parse", "HEAD"))))

	got := onlyPendingRepo(t, o)
	if !got.PendingDirty {
		t.Fatal("pending dirty = false; want true")
	}
	if got.PendingDirtyFileTotal != 1 {
		t.Errorf("pending dirty file total = %d; want 1 (deduped)", got.PendingDirtyFileTotal)
	}
	if len(got.PendingDirtyFiles) != 1 || got.PendingDirtyFiles[0] != "both.txt" {
		t.Errorf("pending dirty files = %v; want [both.txt] exactly once", got.PendingDirtyFiles)
	}
}

func TestCompletionPreflightWithoutWorktreesLeavesDirtyFilesUnset(t *testing.T) {
	t.Parallel()
	repo := pendingDeliveryRepo(t, true)
	if err := os.WriteFile(filepath.Join(repo.WorktreePath, "README.md"), []byte("tracked change"), 0o644); err != nil {
		t.Fatalf("write tracked file: %v", err)
	}
	o := newPendingDeliveryOrchestrator(t, feature.StatusCodeReady, repo,
		&feature.RepoState{Touched: true},
		pendingDeliveryStack(pendingDeliveryEntry(t,
			completionGitOutput(t, repo.WorktreePath, "rev-parse", "HEAD"),
			completionGitOutput(t, repo.WorktreePath, "rev-parse", "HEAD"))))

	got := onlyPendingRepo(t, o)
	if !got.PendingDirty {
		t.Fatal("pending dirty = false; want true")
	}
	if got.PendingDirtyFiles != nil || got.PendingDirtyFileTotal != 0 {
		t.Errorf("dirty files leaked without Worktrees dep: files=%v total=%d", got.PendingDirtyFiles, got.PendingDirtyFileTotal)
	}
}

func TestCompletionPreflightUnresolvedBaseKeepsStatus(t *testing.T) {
	t.Parallel()
	wt := initGitWorktree(t, "no-remote")
	repo := feature.FeatureRepo{
		Name: "repo-a", Path: wt, WorktreePath: wt,
		Branch: "feature/x", BaseBranch: "missing-base", Publishable: boolPtr(true),
	}
	stack := pendingDeliveryStack(feature.StackRepoEntry{
		PRURL:   "https://github.example/repo-a/pull/1",
		PRState: feature.StackPRStateOpen,
	})
	o := newPendingDeliveryOrchestrator(t, feature.StatusCodeReady, repo,
		&feature.RepoState{Touched: true}, stack)

	got := onlyPendingRepo(t, o)
	if got.Status != completionStatusAlreadyPublished {
		t.Errorf("status = %q; want %q", got.Status, completionStatusAlreadyPublished)
	}
	if got.PendingCommits != 0 || got.PendingDirty || got.PushMode != "" {
		t.Errorf("pending fields set without a resolvable base: %+v", got)
	}
}

// completionGitOutput runs git in dir and returns the trimmed stdout.
func completionGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s in %s: %v", strings.Join(args, " "), dir, err)
	}
	return strings.TrimSpace(string(out))
}

// stackedPreflightRepo builds a real git repository wired to a bare origin
// with a two-layer stack on paper: one commit on feature/l1 off main and one
// commit on feature/l2 on top of it. Both layer branches stay local until a
// test pushes them; origin/main is the remote-tracking base the stack
// preflight resolves.
func stackedPreflightRepo(t *testing.T) (worktree, tip1, tip2 string) {
	t.Helper()
	dir := t.TempDir()
	bare := filepath.Join(t.TempDir(), "remote.git")
	runCompletionGit(t, "", "init", "--bare", "--initial-branch=main", bare)
	for _, args := range [][]string{
		{"init", "--initial-branch=main"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
		{"commit", "--allow-empty", "-m", "initial"},
		{"remote", "add", "origin", bare},
		{"push", "-u", "origin", "main"},
		{"checkout", "-b", "feature/l1"},
		{"commit", "--allow-empty", "-m", "layer one"},
		{"checkout", "-b", "feature/l2"},
		{"commit", "--allow-empty", "-m", "layer two"},
	} {
		runCompletionGit(t, dir, args...)
	}
	return dir,
		completionGitOutput(t, dir, "rev-parse", "feature/l1"),
		completionGitOutput(t, dir, "rev-parse", "feature/l2")
}

// newStackedPreflightOrchestrator wires a one-repository feature whose stack
// and repository state the test shapes — the same shape as
// newPendingDeliveryOrchestrator plus the delivery stack. The feature keeps
// the feat-pending ID so onlyPendingRepo addresses it.
func newStackedPreflightOrchestrator(t *testing.T, status feature.Status, repoPath string, stack []feature.StackLayer, state *feature.RepoState) *Orchestrator {
	t.Helper()
	store := feature.NewStore(t.TempDir())
	manager := feature.NewManager(store, config.NewDefault())
	f := &feature.Feature{
		ID:            "feat-pending",
		Name:          "Stack pending",
		Slug:          "stack-pending",
		Status:        status,
		SchemaVersion: feature.SchemaVersionCurrent,
		ActiveRun:     1,
		RunCount:      1,
		Stack:         stack,
		Repos: []feature.FeatureRepo{{
			Name:         "repo-a",
			Path:         repoPath,
			WorktreePath: repoPath,
			Branch:       "feature/l1",
			BaseBranch:   "main",
			Publishable:  boolPtr(true),
		}},
		RepoStates: map[string]*feature.RepoState{"repo-a": state},
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}
	return New(Deps{Lifecycle: manager, Store: store}, Hooks{})
}

// A feature whose layer 1 is published (branch pushed, PR recorded, tip equal
// to the last-pushed SHA) and whose layer 2 carries commits without a PR
// reports unpublished-changes: the stack read model projects layer 1's PR, the
// stack measurement sees layer 2's undelivered range, and the pending count
// equals layer 2's full commit range.
func TestCompletionPreflightStackUpperLayerUnpublishedReportsUnpublishedChanges(t *testing.T) {
	t.Parallel()
	repo, tip1, tip2 := stackedPreflightRepo(t)
	runCompletionGit(t, repo, "push", "origin", "feature/l1")
	const layer1URL = "https://github.example/repo-a/pull/1"
	stack := []feature.StackLayer{
		{
			Position: 1, Title: "Bootstrap", Branch: "feature/l1",
			Repos: map[string]feature.StackRepoEntry{
				"repo-a": {TipSHA: tip1, LastPushedSHA: tip1, PRURL: layer1URL, PRState: feature.StackPRStateOpen},
			},
		},
		{
			Position: 2, Title: "Build and polish", Branch: "feature/l2",
			Repos: map[string]feature.StackRepoEntry{
				"repo-a": {TipSHA: tip2},
			},
		},
	}
	o := newStackedPreflightOrchestrator(t, feature.StatusCodeReady, repo, stack,
		&feature.RepoState{Touched: true})

	got := onlyPendingRepo(t, o)
	if got.Status != completionStatusUnpublishedChanges {
		t.Errorf("status = %q; want %q (layer 2 has commits but no PR)", got.Status, completionStatusUnpublishedChanges)
	}
	if got.PendingCommits != 1 {
		t.Errorf("pending commits = %d; want 1 (layer 2's full range)", got.PendingCommits)
	}
	if got.PendingDirty {
		t.Error("pending dirty = true; want false")
	}
	if got.PushMode != completionPushModeFastForward {
		t.Errorf("push mode = %q; want %q", got.PushMode, completionPushModeFastForward)
	}
	if len(got.PullRequests) != 2 {
		t.Fatalf("pull requests len = %d; want one entry per layer", len(got.PullRequests))
	}
	if got.PullRequests[0].URL != layer1URL || !got.PullRequests[0].PushedUpToDate || got.PullRequests[0].PushMode != completionPushModeNone {
		t.Errorf("layer 1 entry = %+v, want the recorded PR delivered up to date", got.PullRequests[0])
	}
	if got.PullRequests[1].URL != "" || got.PullRequests[1].PushMode != completionPushModeCreate || got.PullRequests[1].NoCommits {
		t.Errorf("layer 2 entry = %+v, want no PR and the create push mode for its commits", got.PullRequests[1])
	}
}

// Once layer 2 is published too (branch pushed, PR recorded, tip equal to the
// last-pushed SHA), every layer with commits is delivered and the repository
// reports already-published with no pending commits.
func TestCompletionPreflightStackAllLayersPublishedReportsAlreadyPublished(t *testing.T) {
	t.Parallel()
	repo, tip1, tip2 := stackedPreflightRepo(t)
	runCompletionGit(t, repo, "push", "origin", "feature/l1")
	runCompletionGit(t, repo, "push", "origin", "feature/l2")
	const layer2URL = "https://github.example/repo-a/pull/2"
	stack := []feature.StackLayer{
		{
			Position: 1, Title: "Bootstrap", Branch: "feature/l1",
			Repos: map[string]feature.StackRepoEntry{
				"repo-a": {TipSHA: tip1, LastPushedSHA: tip1, PRURL: "https://github.example/repo-a/pull/1", PRState: feature.StackPRStateOpen},
			},
		},
		{
			Position: 2, Title: "Build and polish", Branch: "feature/l2",
			Repos: map[string]feature.StackRepoEntry{
				"repo-a": {TipSHA: tip2, LastPushedSHA: tip2, PRURL: layer2URL, PRState: feature.StackPRStateOpen},
			},
		},
	}
	o := newStackedPreflightOrchestrator(t, feature.StatusCodeReady, repo, stack,
		&feature.RepoState{Touched: true})

	got := onlyPendingRepo(t, o)
	if got.Status != completionStatusAlreadyPublished {
		t.Errorf("status = %q; want %q", got.Status, completionStatusAlreadyPublished)
	}
	if got.PendingCommits != 0 {
		t.Errorf("pending commits = %d; want 0", got.PendingCommits)
	}
	if got.PendingDirty {
		t.Error("pending dirty = true; want false")
	}
	if len(got.PullRequests) != 2 {
		t.Fatalf("pull requests len = %d; want one entry per layer", len(got.PullRequests))
	}
	if got.PullRequests[0].URL != "https://github.example/repo-a/pull/1" || got.PullRequests[1].URL != layer2URL {
		t.Errorf("pull request URLs = [%s %s], want both layers' PRs", got.PullRequests[0].URL, got.PullRequests[1].URL)
	}
	for _, pr := range got.PullRequests {
		if !pr.PushedUpToDate || pr.PushMode != completionPushModeNone {
			t.Errorf("layer %d entry = %+v, want pushed up to date with nothing to push", pr.Position, pr)
		}
	}
}

// A rewritten layer 1 whose remote still holds the pushed tip reports push
// mode rewrite: the remote contains a commit the local tip does not, so a
// republish cannot be a fast-forward.
func TestCompletionPreflightStackRewrittenLayerReportsRewritePushMode(t *testing.T) {
	t.Parallel()
	repo, tip1, _ := stackedPreflightRepo(t)
	runCompletionGit(t, repo, "checkout", "feature/l1")
	runCompletionGit(t, repo, "commit", "--allow-empty", "-m", "pushed later")
	runCompletionGit(t, repo, "push", "origin", "feature/l1")
	pushed := completionGitOutput(t, repo, "rev-parse", "feature/l1")
	runCompletionGit(t, repo, "reset", "--hard", tip1)
	runCompletionGit(t, repo, "commit", "--allow-empty", "-m", "rewritten")
	rewritten := completionGitOutput(t, repo, "rev-parse", "feature/l1")
	const layer1URL = "https://github.example/repo-a/pull/1"
	stack := []feature.StackLayer{
		{
			Position: 1, Title: "Bootstrap", Branch: "feature/l1",
			Repos: map[string]feature.StackRepoEntry{
				"repo-a": {TipSHA: rewritten, LastPushedSHA: pushed, PRURL: layer1URL, PRState: feature.StackPRStateOpen},
			},
		},
	}
	o := newStackedPreflightOrchestrator(t, feature.StatusCodeReady, repo, stack,
		&feature.RepoState{Touched: true})

	got := onlyPendingRepo(t, o)
	if got.Status != completionStatusUnpublishedChanges {
		t.Errorf("status = %q; want %q (the rewritten tip is unpushed)", got.Status, completionStatusUnpublishedChanges)
	}
	if got.PushMode != completionPushModeRewrite {
		t.Errorf("push mode = %q; want %q (the remote holds the old tip)", got.PushMode, completionPushModeRewrite)
	}
	if got.PendingCommits != 1 {
		t.Errorf("pending commits = %d; want 1 (the rewritten commit beyond the pushed tip)", got.PendingCommits)
	}
}

// The deferred Final Review staging predicate, driven per repository off the
// stack: a layer with commits lacking a PR or with a tip that differs from
// its last-pushed SHA stages review; NoCommits layers and fully delivered
// stacks do not; a touched repository on a stackless run always stages —
// publish fails closed for it, so there is no layer composition to deliver.
func TestReposNeedFinalReviewStackStaging(t *testing.T) {
	t.Parallel()
	repo, tip1, tip2 := stackedPreflightRepo(t)
	const layer1URL = "https://github.example/repo-a/pull/1"
	const layer2URL = "https://github.example/repo-a/pull/2"

	featureFor := func(stack []feature.StackLayer) *feature.Feature {
		return &feature.Feature{
			Stack: stack,
			Repos: []feature.FeatureRepo{{
				Name: "repo-a", Path: repo, WorktreePath: repo,
				Branch: "feature/l1", BaseBranch: "main", Publishable: boolPtr(true),
			}},
			RepoStates: map[string]*feature.RepoState{
				"repo-a": {Touched: true},
			},
		}
	}
	publishedLayer1 := feature.StackLayer{
		Position: 1, Title: "Bootstrap", Branch: "feature/l1",
		Repos: map[string]feature.StackRepoEntry{
			"repo-a": {TipSHA: tip1, LastPushedSHA: tip1, PRURL: layer1URL, PRState: feature.StackPRStateOpen},
		},
	}
	layer2WithCommits := feature.StackLayer{
		Position: 2, Title: "Build and polish", Branch: "feature/l2",
		Repos: map[string]feature.StackRepoEntry{
			"repo-a": {TipSHA: tip2},
		},
	}

	cases := []struct {
		name string
		f    *feature.Feature
		want bool
	}{
		{
			name: "upper layer with commits and no PR",
			f:    featureFor([]feature.StackLayer{publishedLayer1, layer2WithCommits}),
			want: true,
		},
		{
			name: "every layer with commits published",
			f: featureFor([]feature.StackLayer{publishedLayer1, {
				Position: 2, Title: "Build and polish", Branch: "feature/l2",
				Repos: map[string]feature.StackRepoEntry{
					"repo-a": {TipSHA: tip2, LastPushedSHA: tip2, PRURL: layer2URL, PRState: feature.StackPRStateOpen},
				},
			}}),
			want: false,
		},
		{
			name: "upper layer marked no-commits never stages",
			f: featureFor([]feature.StackLayer{publishedLayer1, {
				Position: 2, Title: "Build and polish", Branch: "feature/l2",
				Repos: map[string]feature.StackRepoEntry{
					"repo-a": {TipSHA: tip2, NoCommits: true},
				},
			}}),
			want: false,
		},
		{
			name: "rewritten tip after publish",
			f: featureFor([]feature.StackLayer{{
				Position: 1, Title: "Bootstrap", Branch: "feature/l1",
				Repos: map[string]feature.StackRepoEntry{
					"repo-a": {TipSHA: tip2, LastPushedSHA: tip1, PRURL: layer1URL, PRState: feature.StackPRStateOpen},
				},
			}}),
			want: true,
		},
		{
			name: "stackless touched repo always stages",
			f:    featureFor(nil),
			want: true,
		},
	}
	o := New(Deps{}, Hooks{})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := o.reposNeedFinalReview(tc.f); got != tc.want {
				t.Errorf("reposNeedFinalReview = %v; want %v", got, tc.want)
			}
		})
	}
}
