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

package orchestrator_test

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// The review-feedback child fixtures: a real repository holding the parent's
// layer branches (one commit per layer, tips recorded on the parent's
// stack), the child checked out on its own branch off the parent's top tip,
// and a real feature store so the hook loads both records through the
// lifecycle exactly as production does.
const roundChildBranch = "feature/rf-child/fixes"

// roundChildHarness wires the stacked parent, the review-feedback child, and
// the round-commit hook over one real repository.
type roundChildHarness struct {
	store   *feature.Store
	mgr     *feature.Manager
	parent  *feature.Feature
	child   *feature.Feature
	hook    agent.RoundCommitHook
	o       *orchestrator.Orchestrator
	repo    string
	iterDir string
	tips    map[int]string
	layers  int
}

func roundChildLayerBranch(pos int) string {
	return fmt.Sprintf("feature/rf-parent-stack/%d-layer-%d", pos, pos)
}

func newRoundChildHarness(t *testing.T, layers int, comments []feature.ReviewFeedbackComment) *roundChildHarness {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	repo := testutil.InitGitRepo(t)
	h := &roundChildHarness{repo: repo, tips: map[int]string{}, layers: layers}
	topBranch := ""
	for pos := 1; pos <= layers; pos++ {
		runRoundGit(t, repo, "checkout", "-b", roundChildLayerBranch(pos))
		h.tips[pos] = testutil.CommitFile(t, repo, fmt.Sprintf("p%d.txt", pos),
			fmt.Sprintf("layer %d work\n", pos), fmt.Sprintf("Layer %d work", pos))
		topBranch = roundChildLayerBranch(pos)
	}
	runRoundGit(t, repo, "checkout", "-b", roundChildBranch)

	store := feature.NewStore(t.TempDir())
	mgr := feature.NewManager(store, &config.Config{})
	h.store, h.mgr = store, mgr

	parent := &feature.Feature{
		ID:            "feat-rf-parent",
		Name:          "RF Parent",
		Slug:          "rf-parent",
		Status:        feature.StatusImplementing,
		SchemaVersion: feature.SchemaVersionCurrent,
		Repos:         []feature.FeatureRepo{{Name: "repo-a", Path: repo, WorktreePath: repo, Branch: topBranch, BaseBranch: "main"}},
	}
	parent.Stack = make([]feature.StackLayer, 0, layers)
	for pos := 1; pos <= layers; pos++ {
		parent.Stack = append(parent.Stack, feature.StackLayer{
			Position: pos, Title: fmt.Sprintf("Layer %d", pos), Slug: fmt.Sprintf("layer-%d", pos),
			Phases: []int{pos}, Branch: roundChildLayerBranch(pos),
			Repos: map[string]feature.StackRepoEntry{"repo-a": {TipSHA: h.tips[pos]}},
		})
	}
	child := &feature.Feature{
		ID:             "feat-rf-child",
		Name:           "RF Child",
		Slug:           "rf-child",
		Status:         feature.StatusImplementing,
		SchemaVersion:  feature.SchemaVersionCurrent,
		Parent:         &feature.ChildRelationship{ParentID: parent.ID, Kind: feature.ChildKindReviewFeedback},
		ReviewFeedback: comments,
		Repos:          []feature.FeatureRepo{{Name: "repo-a", Path: repo, WorktreePath: repo, Branch: roundChildBranch, BaseBranch: topBranch}},
	}
	if err := store.Save(parent); err != nil {
		t.Fatalf("save parent: %v", err)
	}
	if err := store.Save(child); err != nil {
		t.Fatalf("save child: %v", err)
	}
	h.parent, h.child = parent, child

	pr := &agent.PhaseRunner{CommandRunner: agent.NewExecCommandRunner()}
	h.o = orchestrator.New(orchestrator.Deps{
		Lifecycle:   mgr,
		Store:       store,
		PhaseRunner: pr,
		CmdRunner:   pr.CommandRunner,
	}, orchestrator.Hooks{})
	if pr.RoundCommitHook == nil {
		t.Fatal("orchestrator.New must install the round commit hook")
	}
	h.hook = pr.RoundCommitHook
	h.iterDir = t.TempDir()
	return h
}

func (h *roundChildHarness) implementInput() agent.RoundCommitInput {
	return agent.RoundCommitInput{
		FeatureID:            h.child.ID,
		Iteration:            1,
		Kind:                 agent.RoundCommitImplement,
		FirstImplementCommit: true,
		IterationDir:         h.iterDir,
		Repos:                map[string]string{"repo-a": h.repo},
	}
}

// frFixInput carries only IterationDir: the child mode must read the
// manifest from the round's iteration directory, not the Final Review
// fixer's FixIterationDir.
func (h *roundChildHarness) frFixInput() agent.RoundCommitInput {
	return agent.RoundCommitInput{
		FeatureID:    h.child.ID,
		Iteration:    2,
		Kind:         agent.RoundCommitFinalReviewFix,
		FixNumber:    1,
		IterationDir: h.iterDir,
		Repos:        map[string]string{"repo-a": h.repo},
	}
}

func (h *roundChildHarness) writeManifest(t *testing.T, yaml string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(h.iterDir, "fix-manifest.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatalf("write fix manifest: %v", err)
	}
}

func (h *roundChildHarness) dirty(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(h.repo, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func (h *roundChildHarness) ref(t *testing.T, branch string) string {
	t.Helper()
	return runRoundGit(t, h.repo, "rev-parse", "refs/heads/"+branch)
}

func (h *roundChildHarness) head(t *testing.T) string {
	t.Helper()
	return runRoundGit(t, h.repo, "rev-parse", "HEAD")
}

func (h *roundChildHarness) childCommits(t *testing.T) []git.StackLayerCommit {
	t.Helper()
	got, err := git.CommitsBetweenWithStackLayer(h.repo, h.tips[h.layers], h.head(t))
	if err != nil {
		t.Fatalf("listing child commits: %v", err)
	}
	return got
}

func (h *roundChildHarness) commitBody(t *testing.T, sha string) string {
	t.Helper()
	return runRoundGit(t, h.repo, "log", "-1", "--format=%B", sha)
}

func (h *roundChildHarness) commitPaths(t *testing.T, sha string) []string {
	t.Helper()
	out := runRoundGit(t, h.repo, "show", "--name-only", "--format=", sha)
	var paths []string
	for _, p := range strings.Split(out, "\n") {
		if p = strings.TrimSpace(p); p != "" {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	return paths
}

// assertParentStackUntouched pins the no-relocation contract: the child mode
// never moves a parent layer ref.
func (h *roundChildHarness) assertParentStackUntouched(t *testing.T) {
	t.Helper()
	for pos := 1; pos <= h.layers; pos++ {
		if got := h.ref(t, roundChildLayerBranch(pos)); got != h.tips[pos] {
			t.Fatalf("parent layer %d ref = %s, want untouched %s", pos, got, h.tips[pos])
		}
	}
}

func (h *roundChildHarness) assertCleanWorktree(t *testing.T) {
	t.Helper()
	if status := runRoundGit(t, h.repo, "status", "--porcelain"); status != "" {
		t.Fatalf("worktree dirty after the round: %q", status)
	}
}

func roundChildComments() []feature.ReviewFeedbackComment {
	return []feature.ReviewFeedbackComment{
		{Repo: "repo-a", ID: 101, Type: feature.ReviewFeedbackCommentTypeReview, Path: "p2.txt", Line: 1, Author: "reviewer", Body: "please fix", LayerPosition: 2, LayerTitle: "Layer two"},
	}
}

// A review-feedback child round dirtying three files with a manifest sending
// one to parent layer 1 and one to layer 2 produces two commits: the first
// contains only the layer-1 file and carries trailer 1, the second contains
// the layer-2 file and the unlisted file and carries trailer 2 (the
// commented layer — every selected comment sits on layer 2, the parent's
// top). No parent ref moves and the messages keep the round-commit shape
// plus the trailer.
func TestRoundCommitHook_ReviewFeedbackChildPartitionsManifestAcrossLayers(t *testing.T) {
	h := newRoundChildHarness(t, 2, roundChildComments())
	h.dirty(t, "fix-l1.txt", "layer 1 fix\n")
	h.dirty(t, "fix-l2.txt", "layer 2 fix\n")
	h.dirty(t, "fix-unlisted.txt", "unlisted fix\n")
	h.writeManifest(t, `entries:
  - layer: 1
    repository: repo-a
    paths:
      - fix-l1.txt
  - layer: 2
    repository: repo-a
    paths:
      - fix-l2.txt
`)

	if err := h.hook(h.implementInput()); err != nil {
		t.Fatalf("review-feedback child round commit: %v", err)
	}

	commits := h.childCommits(t)
	if len(commits) != 2 {
		t.Fatalf("child commits = %d, want 2: %+v", len(commits), commits)
	}
	if commits[0].Layer != "1" || commits[1].Layer != "2" {
		t.Fatalf("commit trailers = [%s, %s], want [1, 2]", commits[0].Layer, commits[1].Layer)
	}
	if got, want := h.commitPaths(t, commits[0].SHA), []string{"fix-l1.txt"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("layer-1 commit touched %v, want only the layer-1 file", got)
	}
	if got, want := h.commitPaths(t, commits[1].SHA), []string{"fix-l2.txt", "fix-unlisted.txt"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("layer-2 commit touched %v, want the layer-2 file and the unlisted file", got)
	}

	wantFirst := "Implementation round 1\n\nFeature: rf-child\n\nStack-Layer: 1\n\nCo-authored-by: Agentico <noreply@doordash-oss.github.com>"
	if got := h.commitBody(t, commits[0].SHA); got != wantFirst {
		t.Fatalf("layer-1 commit message:\n got: %q\nwant: %q", got, wantFirst)
	}
	wantSecond := "Implementation round 1\n\nFeature: rf-child\n\nStack-Layer: 2\n\nCo-authored-by: Agentico <noreply@doordash-oss.github.com>"
	if got := h.commitBody(t, commits[1].SHA); got != wantSecond {
		t.Fatalf("layer-2 commit message:\n got: %q\nwant: %q", got, wantSecond)
	}

	h.assertParentStackUntouched(t)
	h.assertCleanWorktree(t)
	if h.ref(t, roundChildBranch) != h.head(t) {
		t.Fatal("child commits must land on the child's own branch")
	}

	events := drainEvents(h.o)
	if ignored := warningEvents(events, errcat.FixManifestEntryIgnored); len(ignored) != 0 {
		t.Fatalf("unexpected ignored-entry warnings: %+v", ignored)
	}
	if warnings := warningEvents(events, errcat.FixRelocatedAboveLayer); len(warnings) != 0 {
		t.Fatalf("child mode must never relocate: %+v", warnings)
	}
	found := false
	for _, ev := range events {
		if ev.Type == ports.RepoStatusChanged && ev.RepoName == "repo-a" && ev.Message == "committed implementation round 1" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected the committed event for repo-a; got %+v", events)
	}
}

// With the selected comments spanning layers 1 and 3 of a three-layer
// parent, the unlisted file lands in a commit carrying the top layer's
// trailer, and the commits are created in ascending layer order.
func TestRoundCommitHook_ReviewFeedbackChildUnlistedPathsDefaultToTopLayer(t *testing.T) {
	comments := append(roundChildComments(),
		feature.ReviewFeedbackComment{Repo: "repo-a", ID: 102, Type: feature.ReviewFeedbackCommentTypeIssue, Path: "p1.txt", Author: "reviewer", Body: "also fix layer 1", LayerPosition: 1, LayerTitle: "Layer one"})
	h := newRoundChildHarness(t, 3, comments)
	h.dirty(t, "fix-l1.txt", "layer 1 fix\n")
	h.dirty(t, "fix-l2.txt", "layer 2 fix\n")
	h.dirty(t, "fix-unlisted.txt", "unlisted fix\n")
	h.writeManifest(t, `entries:
  - layer: 1
    repository: repo-a
    paths:
      - fix-l1.txt
  - layer: 2
    repository: repo-a
    paths:
      - fix-l2.txt
`)

	if err := h.hook(h.implementInput()); err != nil {
		t.Fatalf("review-feedback child round commit: %v", err)
	}

	commits := h.childCommits(t)
	if len(commits) != 3 {
		t.Fatalf("child commits = %d, want 3: %+v", len(commits), commits)
	}
	wantLayers := []string{"1", "2", "3"}
	for i, want := range wantLayers {
		if commits[i].Layer != want {
			t.Fatalf("commit %d trailer = %q, want %q (ascending layer order)", i, commits[i].Layer, want)
		}
	}
	if got, want := h.commitPaths(t, commits[2].SHA), []string{"fix-unlisted.txt"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("top-layer commit touched %v, want only the unlisted file", got)
	}

	h.assertParentStackUntouched(t)
	h.assertCleanWorktree(t)
	events := drainEvents(h.o)
	if ignored := warningEvents(events, errcat.FixManifestEntryIgnored); len(ignored) != 0 {
		t.Fatalf("unexpected ignored-entry warnings: %+v", ignored)
	}
}

// An invalid position and a path listed twice each emit exactly one
// ignored-entry warning; the invalid entry's path lands per the default rule
// and the duplicated path goes to its highest valid listed layer.
func TestRoundCommitHook_ReviewFeedbackChildIgnoredEntriesWarnAndDefault(t *testing.T) {
	h := newRoundChildHarness(t, 2, roundChildComments())
	h.dirty(t, "fix-invalid.txt", "invalid target\n")
	h.dirty(t, "fix-dup.txt", "duplicated\n")
	h.writeManifest(t, `entries:
  - layer: 9
    repository: repo-a
    paths:
      - fix-invalid.txt
  - layer: 1
    repository: repo-a
    paths:
      - fix-dup.txt
  - layer: 2
    repository: repo-a
    paths:
      - fix-dup.txt
`)

	if err := h.hook(h.implementInput()); err != nil {
		t.Fatalf("review-feedback child round commit: %v", err)
	}

	events := drainEvents(h.o)
	ignored := warningEvents(events, errcat.FixManifestEntryIgnored)
	if len(ignored) != 2 {
		t.Fatalf("ignored-entry warnings = %d, want 2 (invalid position, duplicate path): %+v", len(ignored), events)
	}
	for _, reason := range []string{"invalid position", "duplicate path"} {
		found := false
		for _, ev := range ignored {
			if strings.Contains(ev.CanonicalError.Summary, reason) {
				found = true
			}
		}
		if !found {
			t.Errorf("no ignored-entry warning carries reason %q; summaries: %v", reason, ignored)
		}
	}

	// Both paths land on layer 2 — the default for the invalid entry's path,
	// the highest valid listing for the duplicated one — so the round yields
	// a single commit tagged with layer 2.
	commits := h.childCommits(t)
	if len(commits) != 1 {
		t.Fatalf("child commits = %d, want 1: %+v", len(commits), commits)
	}
	if commits[0].Layer != "2" {
		t.Fatalf("commit trailer = %q, want 2", commits[0].Layer)
	}
	if got, want := h.commitPaths(t, commits[0].SHA), []string{"fix-dup.txt", "fix-invalid.txt"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("commit touched %v, want both files", got)
	}

	h.assertParentStackUntouched(t)
	h.assertCleanWorktree(t)
}

// A missing manifest commits everything into one commit tagged with the
// commented layer — even when that layer is not the parent's top — and a
// round whose every path lands on a single target layer yields exactly one
// commit tagged with that layer.
func TestRoundCommitHook_ReviewFeedbackChildMissingManifestCommitsOneTaggedLayer(t *testing.T) {
	// The single selected comment sits on layer 2 of a three-layer parent,
	// so the commented-layer default must tag the commit with layer 2, not
	// the top layer.
	h := newRoundChildHarness(t, 3, roundChildComments())
	h.dirty(t, "fix-a.txt", "fix a\n")
	h.dirty(t, "fix-b.txt", "fix b\n")

	if err := h.hook(h.implementInput()); err != nil {
		t.Fatalf("review-feedback child round commit without a manifest: %v", err)
	}

	commits := h.childCommits(t)
	if len(commits) != 1 {
		t.Fatalf("child commits = %d, want 1: %+v", len(commits), commits)
	}
	if commits[0].Layer != "2" {
		t.Fatalf("commit trailer = %q, want the commented layer's position 2", commits[0].Layer)
	}
	if got, want := h.commitPaths(t, commits[0].SHA), []string{"fix-a.txt", "fix-b.txt"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("commit touched %v, want both files", got)
	}
	events := drainEvents(h.o)
	if ignored := warningEvents(events, errcat.FixManifestEntryIgnored); len(ignored) != 0 {
		t.Fatalf("a missing manifest must be silent: %+v", ignored)
	}

	// A manifest sending every dirty path to one lower layer yields a single
	// commit tagged with that layer.
	h2 := newRoundChildHarness(t, 3, roundChildComments())
	h2.dirty(t, "fix-a.txt", "fix a\n")
	h2.dirty(t, "fix-b.txt", "fix b\n")
	h2.writeManifest(t, `entries:
  - layer: 1
    repository: repo-a
    paths:
      - fix-a.txt
      - fix-b.txt
`)
	if err := h2.hook(h2.implementInput()); err != nil {
		t.Fatalf("review-feedback child round commit: %v", err)
	}
	commits = h2.childCommits(t)
	if len(commits) != 1 {
		t.Fatalf("single-layer child commits = %d, want 1: %+v", len(commits), commits)
	}
	if commits[0].Layer != "1" {
		t.Fatalf("single-layer commit trailer = %q, want 1", commits[0].Layer)
	}
}

// A Final Review fix round of a review-feedback child partitions through the
// round's iteration directory — FixIterationDir stays empty — and tags each
// commit with its target layer.
func TestRoundCommitHook_ReviewFeedbackChildFinalReviewFixUsesIterationDir(t *testing.T) {
	h := newRoundChildHarness(t, 2, roundChildComments())
	h.dirty(t, "fr-l1.txt", "final review layer 1 fix\n")
	h.dirty(t, "fr-top.txt", "final review top fix\n")
	h.writeManifest(t, `entries:
  - layer: 1
    repository: repo-a
    paths:
      - fr-l1.txt
`)

	if err := h.hook(h.frFixInput()); err != nil {
		t.Fatalf("review-feedback child final review fix round: %v", err)
	}

	commits := h.childCommits(t)
	if len(commits) != 2 {
		t.Fatalf("child commits = %d, want 2: %+v", len(commits), commits)
	}
	if commits[0].Layer != "1" || commits[1].Layer != "2" {
		t.Fatalf("commit trailers = [%s, %s], want [1, 2]", commits[0].Layer, commits[1].Layer)
	}
	wantFirst := "Final review fix 1 (address review feedback)\n\nFeature: rf-child\n\nStack-Layer: 1\n\nCo-authored-by: Agentico <noreply@doordash-oss.github.com>"
	if got := h.commitBody(t, commits[0].SHA); got != wantFirst {
		t.Fatalf("layer-1 fix message:\n got: %q\nwant: %q", got, wantFirst)
	}
	if got, want := h.commitPaths(t, commits[1].SHA), []string{"fr-top.txt"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("layer-2 fix commit touched %v, want only the unlisted file", got)
	}

	h.assertParentStackUntouched(t)
	h.assertCleanWorktree(t)
	events := drainEvents(h.o)
	if ignored := warningEvents(events, errcat.FixManifestEntryIgnored); len(ignored) != 0 {
		t.Fatalf("unexpected ignored-entry warnings: %+v", ignored)
	}
}

// Rounds of every other feature kind behave exactly as today: a refactor
// child of a stacked parent commits one untagged commit and never reads the
// manifest, and a review-feedback child whose parent has no stack does the
// same.
func TestRoundCommitHook_RefactorChildAndStacklessParentCommitAsToday(t *testing.T) {
	h := newRoundChildHarness(t, 2, roundChildComments())
	headBefore := h.head(t)

	refactor := &feature.Feature{
		ID:            "feat-refactor-child",
		Name:          "Refactor Child",
		Slug:          "refactor-child",
		Status:        feature.StatusImplementing,
		SchemaVersion: feature.SchemaVersionCurrent,
		Parent:        &feature.ChildRelationship{ParentID: h.parent.ID, Kind: feature.ChildKindRefactor},
		Repos:         []feature.FeatureRepo{{Name: "repo-a", Path: h.repo, WorktreePath: h.repo, Branch: roundChildBranch, BaseBranch: roundChildLayerBranch(2)}},
	}
	if err := h.store.Save(refactor); err != nil {
		t.Fatalf("save refactor child: %v", err)
	}
	h.dirty(t, "refactor.txt", "refactor change\n")
	// A manifest naming layer 1 must be ignored entirely by a refactor child.
	h.writeManifest(t, `entries:
  - layer: 1
    repository: repo-a
    paths:
      - refactor.txt
`)
	if err := h.hook(agent.RoundCommitInput{
		FeatureID:            refactor.ID,
		Iteration:            1,
		Kind:                 agent.RoundCommitImplement,
		FirstImplementCommit: true,
		IterationDir:         h.iterDir,
		Repos:                map[string]string{"repo-a": h.repo},
	}); err != nil {
		t.Fatalf("refactor child round commit: %v", err)
	}
	commits, err := git.CommitsBetweenWithStackLayer(h.repo, headBefore, h.head(t))
	if err != nil {
		t.Fatalf("listing refactor child commits: %v", err)
	}
	if len(commits) != 1 {
		t.Fatalf("refactor child commits = %d, want 1: %+v", len(commits), commits)
	}
	if commits[0].Layer != "" {
		t.Fatalf("refactor child commit carries trailer %q, want none", commits[0].Layer)
	}
	wantBody := "Implementation round 1\n\nFeature: refactor-child\n\nCo-authored-by: Agentico <noreply@doordash-oss.github.com>"
	if got := h.commitBody(t, commits[0].SHA); got != wantBody {
		t.Fatalf("refactor child message:\n got: %q\nwant: %q", got, wantBody)
	}
	h.assertParentStackUntouched(t)
	h.assertCleanWorktree(t)
	events := drainEvents(h.o)
	if ignored := warningEvents(events, errcat.FixManifestEntryIgnored); len(ignored) != 0 {
		t.Fatalf("refactor child must not read the manifest: %+v", ignored)
	}

	// A review-feedback child whose parent records no stack commits as today.
	h.parent.Stack = nil
	if err := h.store.Save(h.parent); err != nil {
		t.Fatalf("re-save parent without a stack: %v", err)
	}
	loaded, err := h.store.Load(h.parent.ID)
	if err != nil {
		t.Fatalf("reload parent: %v", err)
	}
	if len(loaded.Stack) != 0 {
		t.Fatalf("parent stack = %+v, want empty", loaded.Stack)
	}
	headBefore = h.head(t)
	h.dirty(t, "stackless.txt", "stackless parent fix\n")
	if err := h.hook(h.implementInput()); err != nil {
		t.Fatalf("stackless-parent child round commit: %v", err)
	}
	commits, err = git.CommitsBetweenWithStackLayer(h.repo, headBefore, h.head(t))
	if err != nil {
		t.Fatalf("listing stackless-parent child commits: %v", err)
	}
	if len(commits) != 1 {
		t.Fatalf("stackless-parent child commits = %d, want 1: %+v", len(commits), commits)
	}
	if commits[0].Layer != "" {
		t.Fatalf("stackless-parent child commit carries trailer %q, want none", commits[0].Layer)
	}
	h.assertCleanWorktree(t)
}
