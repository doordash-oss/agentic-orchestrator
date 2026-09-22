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
	"os/exec"
	"path/filepath"
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

// The round-commit relocation fixtures: a real repository holding a
// two-layer, three-phase stack — layer 1 "Bootstrap" covers phases 1 and 2,
// layer 2 "Build and polish" covers phase 3 — with the worktree checked out
// on layer 2's branch, the run recording every phase anchor and both layer
// tips, exactly as the boundary machinery leaves it.
const (
	roundLayer1Branch = "feature/round-relocation/1-bootstrap"
	roundLayer2Branch = "feature/round-relocation/2-build-and-polish"
)

// roundRelocationHarness wires a real feature store and manager (so the
// restack journal lifecycle runs for real), a real git worktree manager
// (optionally wrapped for failure injection), and the round-commit hook.
type roundRelocationHarness struct {
	store   *feature.Store
	mgr     *feature.Manager
	f       *feature.Feature
	hook    agent.RoundCommitHook
	o       *orchestrator.Orchestrator
	repo    string
	iterDir string

	base string
	a1   string
	a2   string
	a3   string
}

func runRoundGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, ok := tryRoundGit(t, dir, args...)
	if !ok {
		t.Fatalf("git %s: unexpected failure\n%s", strings.Join(args, " "), out)
	}
	return strings.TrimSpace(out)
}

// tryRoundGit runs git without failing the test; the caller decides whether
// the outcome is expected.
func tryRoundGit(t *testing.T, dir string, args ...string) (string, bool) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(testutil.GitTestEnv(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@test.com",
	)
	out, err := cmd.CombinedOutput()
	return string(out), err == nil
}

// newRoundRelocationHarness builds the stacked repository and feature. When
// wrapWorktrees is set it replaces the real worktree manager for failure
// injection.
func newRoundRelocationHarness(t *testing.T, wrapWorktrees func(*git.WorktreeManager) feature.WorktreeOps) *roundRelocationHarness {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	repo := testutil.InitGitRepo(t)
	h := &roundRelocationHarness{repo: repo}
	h.base = runRoundGit(t, repo, "rev-parse", "HEAD")

	// Layer 1: phases 1 and 2.
	runRoundGit(t, repo, "checkout", "-b", roundLayer1Branch)
	h.a1 = testutil.CommitFile(t, repo, "p1.txt", "phase 1\n", "Phase 1 work")
	h.a2 = testutil.CommitFile(t, repo, "p2.txt", "phase 2\n", "Phase 2 work")
	// Layer 2: phase 3, checked out in the main worktree.
	runRoundGit(t, repo, "checkout", "-b", roundLayer2Branch)
	h.a3 = testutil.CommitFile(t, repo, "p3.txt", "phase 3\n", "Phase 3 work")

	store := feature.NewStore(t.TempDir())
	mgr := feature.NewManager(store, &config.Config{})
	h.store, h.mgr = store, mgr
	f := &feature.Feature{
		ID:                  "feat-round-relocation",
		Name:                "Round Relocation",
		Slug:                "round-relocation",
		Status:              feature.StatusFinalReviewing,
		CurrentPhase:        feature.PhaseFinalReview,
		CurrentRoadmapPhase: 3,
		TotalRoadmapPhases:  3,
		SchemaVersion:       feature.SchemaVersionCurrent,
		Repos: []feature.FeatureRepo{
			{Name: "repo-a", Path: repo, WorktreePath: repo, Branch: roundLayer2Branch, BaseBranch: "main"},
		},
	}
	f.Stack = []feature.StackLayer{
		{
			Position: 1, Title: "Bootstrap", Slug: "bootstrap", Phases: []int{1, 2}, Branch: roundLayer1Branch,
			Repos: map[string]feature.StackRepoEntry{"repo-a": {TipSHA: h.a2}},
		},
		{
			Position: 2, Title: "Build and polish", Slug: "build-and-polish", Phases: []int{3}, Branch: roundLayer2Branch,
			Repos: map[string]feature.StackRepoEntry{"repo-a": {TipSHA: h.a3}},
		},
	}
	f.Run().RoadmapPhaseCommitAnchors = map[int]map[string]string{
		1: {"repo-a": h.a1},
		2: {"repo-a": h.a2},
		3: {"repo-a": h.a3},
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}
	h.f = f

	worktrees := feature.WorktreeOps(git.NewWorktreeManager(t.TempDir()))
	if wrapWorktrees != nil {
		worktrees = wrapWorktrees(git.NewWorktreeManager(t.TempDir()))
	}
	pr := &agent.PhaseRunner{CommandRunner: agent.NewExecCommandRunner()}
	h.o = orchestrator.New(orchestrator.Deps{
		Lifecycle:   mgr,
		Store:       store,
		Worktrees:   worktrees,
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

func (h *roundRelocationHarness) fixInput() agent.RoundCommitInput {
	return agent.RoundCommitInput{
		FeatureID:       h.f.ID,
		Iteration:       1,
		Kind:            agent.RoundCommitFinalReviewFix,
		FixNumber:       1,
		FixIterationDir: h.iterDir,
		Repos:           map[string]string{"repo-a": h.repo},
	}
}

func (h *roundRelocationHarness) writeManifest(t *testing.T, yaml string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(h.iterDir, "fix-manifest.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatalf("write fix manifest: %v", err)
	}
}

func (h *roundRelocationHarness) dirty(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(h.repo, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func (h *roundRelocationHarness) ref(t *testing.T, branch string) string {
	t.Helper()
	return runRoundGit(t, h.repo, "rev-parse", "refs/heads/"+branch)
}

func (h *roundRelocationHarness) head(t *testing.T) string {
	t.Helper()
	return runRoundGit(t, h.repo, "rev-parse", "HEAD")
}

func (h *roundRelocationHarness) loadFeature(t *testing.T) *feature.Feature {
	t.Helper()
	loaded, err := h.store.Load(h.f.ID)
	if err != nil {
		t.Fatalf("load feature: %v", err)
	}
	return loaded
}

func (h *roundRelocationHarness) subjects(t *testing.T, from, to string) []string {
	t.Helper()
	out := runRoundGit(t, h.repo, "log", "--format=%s", from+".."+to)
	var subjects []string
	for _, s := range strings.Split(out, "\n") {
		if s = strings.TrimSpace(s); s != "" {
			subjects = append(subjects, s)
		}
	}
	return subjects
}

func (h *roundRelocationHarness) treeOf(t *testing.T, sha string) string {
	t.Helper()
	return runRoundGit(t, h.repo, "rev-parse", sha+"^{tree}")
}

func warningEvents(events []ports.Event, code errcat.Code) []ports.Event {
	var out []ports.Event
	for _, ev := range events {
		if ev.Type == ports.RepoStatusChanged && ev.CanonicalError != nil && ev.CanonicalError.Code == code {
			out = append(out, ev)
		}
	}
	return out
}

// A Final Review fix round on a two-layer stack with a manifest assigning
// one changed file to layer 1 and leaving another unlisted: layer 1's ref
// gains a new commit containing only the first file's change, layer 2's
// commits replay above it with unchanged messages, the unlisted file lands
// in a top-layer commit, the top layer's ref equals the worktree HEAD, the
// new top tree is byte-identical to the tree before relocation, the phase
// anchors and both layer tips are remapped to live commits, and no journal
// entry, temporary worktree, or cherry-pick remains.
func TestRoundCommitHook_RelocatesManifestFixIntoLayerOne(t *testing.T) {
	h := newRoundRelocationHarness(t, nil)
	h.dirty(t, "fix-l1.txt", "layer 1 fix\n")
	h.dirty(t, "fix-top.txt", "top fix\n")
	h.writeManifest(t, "entries:\n  - layer: 1\n    repository: repo-a\n    paths:\n      - fix-l1.txt\n")

	if err := h.hook(h.fixInput()); err != nil {
		t.Fatalf("final review fix round commit: %v", err)
	}

	fixMsg := "Final review fix 1 (address review feedback)"

	// Layer 1's ref moved and contains only the first file's change.
	layer1 := h.ref(t, roundLayer1Branch)
	if layer1 == h.a2 {
		t.Fatal("layer 1 ref did not move")
	}
	if got := runRoundGit(t, h.repo, "show", layer1+":fix-l1.txt"); got != "layer 1 fix" {
		t.Fatalf("layer 1 tip fix-l1.txt = %q, want the relocated change", got)
	}
	if _, ok := tryRoundGit(t, h.repo, "cat-file", "-e", layer1+":fix-top.txt"); ok {
		t.Fatal("layer 1 tip contains the unlisted file; want it only in the top layer")
	}
	// The relocated fix is the only commit above the phase-2 anchor.
	if got := h.subjects(t, h.a2, layer1); len(got) != 1 || got[0] != fixMsg {
		t.Fatalf("layer 1 subjects above the phase-2 anchor = %v, want one %q", got, fixMsg)
	}

	// Layer 2's commits replay above with unchanged messages: the phase-3
	// commit plus the top-layer fix commit.
	head := h.head(t)
	if got := h.subjects(t, layer1, head); len(got) != 2 || got[1] != "Phase 3 work" || got[0] != fixMsg {
		t.Fatalf("subjects above layer 1's new tip = %v, want [top fix, Phase 3 work]", got)
	}
	// The unlisted file is in the top commit only.
	if got := runRoundGit(t, h.repo, "show", head+":fix-top.txt"); got != "top fix" {
		t.Fatalf("top fix-l1 content = %q, want the top fix change", got)
	}
	if names := runRoundGit(t, h.repo, "show", "--name-only", "--format=", head); names != "fix-top.txt" {
		t.Fatalf("top commit touched %q, want only fix-top.txt", names)
	}

	// The top layer's ref equals the worktree HEAD.
	if h.ref(t, roundLayer2Branch) != head {
		t.Fatalf("layer 2 ref = %s, want worktree HEAD %s", h.ref(t, roundLayer2Branch), head)
	}

	// The new top tree is byte-identical to the tree before relocation: the
	// same two changes applied on the old top produce the same tree.
	scratch := filepath.Join(t.TempDir(), "wt")
	runRoundGit(t, h.repo, "worktree", "add", "--detach", scratch, h.a3)
	for name, content := range map[string]string{"fix-l1.txt": "layer 1 fix\n", "fix-top.txt": "top fix\n"} {
		if err := os.WriteFile(filepath.Join(scratch, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	runRoundGit(t, scratch, "add", "-A")
	runRoundGit(t, scratch, "commit", "-m", "expected")
	expectedTree := runRoundGit(t, scratch, "rev-parse", "HEAD^{tree}")
	runRoundGit(t, h.repo, "worktree", "remove", "--force", scratch)
	if got := h.treeOf(t, head); got != expectedTree {
		t.Fatalf("new top tree = %s, want the pre-relocation tree %s", got, expectedTree)
	}

	// The run records the remapped anchors and tips and no journal entry.
	loaded := h.loadFeature(t)
	anchors := loaded.Run().RoadmapPhaseCommitAnchors
	if anchors[1]["repo-a"] != h.a1 || anchors[2]["repo-a"] != h.a2 {
		t.Fatalf("phase 1/2 anchors = %v, want unchanged below the relocation", anchors)
	}
	if anchors[3]["repo-a"] == h.a3 || anchors[3]["repo-a"] == "" {
		t.Fatalf("phase 3 anchor = %q, want a remapped live commit", anchors[3]["repo-a"])
	}
	if got := loaded.Stack[0].Repos["repo-a"].TipSHA; got != layer1 {
		t.Fatalf("layer 1 tip = %s, want its ref %s", got, layer1)
	}
	if got := loaded.Stack[1].Repos["repo-a"].TipSHA; got != head {
		t.Fatalf("layer 2 tip = %s, want the new top %s", got, head)
	}
	if journal := loaded.Run().RestackJournal; len(journal) != 0 {
		t.Fatalf("restack journal = %+v, want no entries", journal)
	}

	// No leftover restack state.
	if status := runRoundGit(t, h.repo, "status", "--porcelain"); status != "" {
		t.Fatalf("worktree dirty after relocation: %q", status)
	}
	if list := runRoundGit(t, h.repo, "worktree", "list"); len(strings.Split(strings.TrimSpace(list), "\n")) != 1 {
		t.Fatalf("leftover worktrees: %s", list)
	}
	gitDir := runRoundGit(t, h.repo, "rev-parse", "--git-dir")
	if _, err := os.Stat(filepath.Join(gitDir, "CHERRY_PICK_HEAD")); err == nil {
		t.Fatal("a cherry-pick is still in progress")
	}

	// One informational relocation event, no warnings.
	events := drainEvents(h.o)
	if warningEvents(events, errcat.FixRelocatedAboveLayer) != nil {
		t.Fatalf("unexpected relocation warnings: %+v", warningEvents(events, errcat.FixRelocatedAboveLayer))
	}
	found := false
	for _, ev := range events {
		if ev.Type == ports.RepoStatusChanged && ev.RepoName == "repo-a" &&
			ev.Message == "relocated a final review fix into layer 1" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a relocation event for repo-a; got %+v", events)
	}
}

// A manifest fix whose relocation conflicts with layer 2's commits lands in
// layer 2's commit range with one relocated-above warning naming both
// layers, and layer 1's ref stays unchanged.
func TestRoundCommitHook_ConflictingFixFallsBackToLayerTwo(t *testing.T) {
	h := newRoundRelocationHarness(t, nil)
	// The fix rewrites a file layer 2's phase-3 commit introduced, so
	// replaying that commit on top of the relocated fix conflicts.
	h.dirty(t, "p3.txt", "layer 1 version\n")
	h.writeManifest(t, "entries:\n  - layer: 1\n    repository: repo-a\n    paths:\n      - p3.txt\n")

	if err := h.hook(h.fixInput()); err != nil {
		t.Fatalf("final review fix round commit: %v", err)
	}

	if got := h.ref(t, roundLayer1Branch); got != h.a2 {
		t.Fatalf("layer 1 ref = %s, want unchanged %s", got, h.a2)
	}
	head := h.head(t)
	if got := runRoundGit(t, h.repo, "show", head+":p3.txt"); got != "layer 1 version" {
		t.Fatalf("p3.txt at HEAD = %q, want the fix applied in layer 2's range", got)
	}
	// The fix sits above the phase-3 commit: layer 2's commit range.
	if got := h.subjects(t, h.a3, head); len(got) != 1 {
		t.Fatalf("subjects above the phase-3 commit = %v, want only the fix", got)
	}

	events := drainEvents(h.o)
	warnings := warningEvents(events, errcat.FixRelocatedAboveLayer)
	if len(warnings) != 1 {
		t.Fatalf("relocation warnings = %d, want exactly 1: %+v", len(warnings), events)
	}
	summary := warnings[0].CanonicalError.Summary
	if !strings.Contains(summary, "layer 2") || !strings.Contains(summary, "layer 1") {
		t.Fatalf("warning summary = %q, want both layers named", summary)
	}

	if status := runRoundGit(t, h.repo, "status", "--porcelain"); status != "" {
		t.Fatalf("worktree dirty after fallback: %q", status)
	}
	if list := runRoundGit(t, h.repo, "worktree", "list"); len(strings.Split(strings.TrimSpace(list), "\n")) != 1 {
		t.Fatalf("leftover worktrees: %s", list)
	}
}

// A fix whose replay would silently change the top tree without conflicting
// — removing code layer 2 reintroduces when replayed — falls back upward the
// same way as a conflict.
func TestRoundCommitHook_TreeMismatchFallsBackToLayerTwo(t *testing.T) {
	h := newRoundRelocationHarness(t, nil)
	// The fix deletes a file layer 2's phase-3 commit introduced: replayed
	// onto layer 1 the deletion is empty and dropped, then the replayed
	// phase-3 commit reintroduces the file, changing the top tree.
	if err := os.Remove(filepath.Join(h.repo, "p3.txt")); err != nil {
		t.Fatalf("remove p3.txt: %v", err)
	}
	h.writeManifest(t, "entries:\n  - layer: 1\n    repository: repo-a\n    paths:\n      - p3.txt\n")

	if err := h.hook(h.fixInput()); err != nil {
		t.Fatalf("final review fix round commit: %v", err)
	}

	if got := h.ref(t, roundLayer1Branch); got != h.a2 {
		t.Fatalf("layer 1 ref = %s, want unchanged %s", got, h.a2)
	}
	head := h.head(t)
	if _, err := os.Stat(filepath.Join(h.repo, "p3.txt")); !os.IsNotExist(err) {
		t.Fatalf("p3.txt still present at HEAD (stat err = %v); the fix must survive at layer 2", err)
	}
	if _, ok := tryRoundGit(t, h.repo, "cat-file", "-e", head+":p3.txt"); ok {
		t.Fatal("p3.txt is in the top tree; the fix vanished instead of falling back")
	}

	events := drainEvents(h.o)
	if warnings := warningEvents(events, errcat.FixRelocatedAboveLayer); len(warnings) != 1 {
		t.Fatalf("relocation warnings = %d, want exactly 1", len(warnings))
	}
}

// Every invalid manifest shape degrades to the top layer with exactly one
// ignored-entry warning each, and the round still succeeds.
func TestRoundCommitHook_IgnoredManifestEntries(t *testing.T) {
	h := newRoundRelocationHarness(t, nil)
	h.dirty(t, "fix-dup.txt", "duplicated\n")
	h.dirty(t, "fix-top.txt", "unlisted\n")
	h.writeManifest(t, `entries:
  - layer: 5
    repository: repo-a
    paths:
      - fix-top.txt
  - layer: 1
    repository: no-such-repo
    paths:
      - fix-top.txt
  - layer: 1
    repository: repo-a
    paths:
      - not-dirty.txt
  - layer: 1
    repository: repo-a
    paths:
      - fix-dup.txt
  - layer: 2
    repository: repo-a
    paths:
      - fix-dup.txt
`)

	if err := h.hook(h.fixInput()); err != nil {
		t.Fatalf("final review fix round commit: %v", err)
	}

	events := drainEvents(h.o)
	ignored := warningEvents(events, errcat.FixManifestEntryIgnored)
	if len(ignored) != 4 {
		t.Fatalf("ignored-entry warnings = %d, want 4 (invalid position, unknown repository, path not changed, duplicate path): %+v", len(ignored), events)
	}
	wantReasons := []string{"invalid position", "unknown repository", "path not changed", "duplicate path"}
	for _, reason := range wantReasons {
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

	// The duplicated path went to the highest valid listed layer (the top),
	// everything stayed out of layer 1, and the worktree is clean.
	if got := h.ref(t, roundLayer1Branch); got != h.a2 {
		t.Fatalf("layer 1 ref = %s, want unchanged %s", got, h.a2)
	}
	head := h.head(t)
	if got := runRoundGit(t, h.repo, "show", head+":fix-dup.txt"); got != "duplicated" {
		t.Fatalf("fix-dup.txt = %q, want it committed in the top layer", got)
	}
	if names := runRoundGit(t, h.repo, "show", "--name-only", "--format=", head); !strings.Contains(names, "fix-dup.txt") || !strings.Contains(names, "fix-top.txt") {
		t.Fatalf("top commit touched %q, want both files", names)
	}
	if status := runRoundGit(t, h.repo, "status", "--porcelain"); status != "" {
		t.Fatalf("worktree dirty: %q", status)
	}
}

// A malformed manifest commits everything to the top layer with one
// unparsable-manifest warning; a missing manifest commits everything to the
// top layer with no warning at all. Both rounds succeed.
func TestRoundCommitHook_MalformedAndMissingManifestsCommitToTop(t *testing.T) {
	h := newRoundRelocationHarness(t, nil)
	h.dirty(t, "fix-top.txt", "top fix\n")
	h.writeManifest(t, "entries: [not: a: valid: manifest\n")

	if err := h.hook(h.fixInput()); err != nil {
		t.Fatalf("round commit over a malformed manifest: %v", err)
	}
	events := drainEvents(h.o)
	ignored := warningEvents(events, errcat.FixManifestEntryIgnored)
	if len(ignored) != 1 || !strings.Contains(ignored[0].CanonicalError.Summary, "unparsable manifest") {
		t.Fatalf("ignored-entry warnings = %+v, want one unparsable manifest", ignored)
	}
	if got := h.ref(t, roundLayer1Branch); got != h.a2 {
		t.Fatalf("layer 1 ref moved over a malformed manifest: %s", got)
	}
	if got := runRoundGit(t, h.repo, "show", h.head(t)+":fix-top.txt"); got != "top fix" {
		t.Fatalf("fix-top.txt = %q, want it committed to the top layer", got)
	}

	// A missing manifest is silent.
	h.dirty(t, "fix-top-2.txt", "top fix 2\n")
	if err := os.Remove(filepath.Join(h.iterDir, "fix-manifest.yaml")); err != nil {
		t.Fatalf("remove manifest: %v", err)
	}
	if err := h.hook(h.fixInput()); err != nil {
		t.Fatalf("round commit without a manifest: %v", err)
	}
	events = drainEvents(h.o)
	if ignored := warningEvents(events, errcat.FixManifestEntryIgnored); len(ignored) != 0 {
		t.Fatalf("warnings for a missing manifest = %+v, want none", ignored)
	}
	if got := runRoundGit(t, h.repo, "show", h.head(t)+":fix-top-2.txt"); got != "top fix 2" {
		t.Fatalf("fix-top-2.txt = %q, want it committed to the top layer", got)
	}
}

// failingTransactionWorktrees delegates everything to the real worktree
// manager but fails the multi-ref transaction.
type failingTransactionWorktrees struct {
	*git.WorktreeManager
	err error
}

func (w *failingTransactionWorktrees) UpdateRefsTransaction(string, []git.RefUpdate) error {
	return w.err
}

// failingResetWorktrees delegates everything to the real worktree manager
// but fails the hard reset after a successful transaction.
type failingResetWorktrees struct {
	*git.WorktreeManager
	err error
}

func (w *failingResetWorktrees) ResetToCommit(string, string) error { return w.err }

// A transaction mismatch leaves the commits on the top branch, emits the
// relocated-above warning with the mismatch as diagnostics, drops the
// journal entry, and the hook still returns nil.
func TestRoundCommitHook_TransactionMismatchKeepsCommitsOnTop(t *testing.T) {
	h := newRoundRelocationHarness(t, func(real *git.WorktreeManager) feature.WorktreeOps {
		return &failingTransactionWorktrees{WorktreeManager: real,
			err: &git.RefCASMismatchError{Ref: "refs/heads/" + roundLayer1Branch, Expected: "old", Observed: "observed"}}
	})
	h.dirty(t, "fix-l1.txt", "layer 1 fix\n")
	h.writeManifest(t, "entries:\n  - layer: 1\n    repository: repo-a\n    paths:\n      - fix-l1.txt\n")

	if err := h.hook(h.fixInput()); err != nil {
		t.Fatalf("round commit must survive a transaction mismatch: %v", err)
	}

	if got := h.ref(t, roundLayer1Branch); got != h.a2 {
		t.Fatalf("layer 1 ref = %s, want unchanged %s", got, h.a2)
	}
	head := h.head(t)
	if got := runRoundGit(t, h.repo, "show", head+":fix-l1.txt"); got != "layer 1 fix" {
		t.Fatalf("fix-l1.txt = %q, want the fix committed on the top branch", got)
	}
	if h.ref(t, roundLayer2Branch) != head {
		t.Fatalf("layer 2 ref = %s, want HEAD %s", h.ref(t, roundLayer2Branch), head)
	}

	loaded := h.loadFeature(t)
	if journal := loaded.Run().RestackJournal; len(journal) != 0 {
		t.Fatalf("restack journal = %+v, want the entry dropped", journal)
	}
	events := drainEvents(h.o)
	if warnings := warningEvents(events, errcat.FixRelocatedAboveLayer); len(warnings) != 1 {
		t.Fatalf("relocation warnings = %d, want 1", len(warnings))
	}
	if status := runRoundGit(t, h.repo, "status", "--porcelain"); status != "" {
		t.Fatalf("worktree dirty: %q", status)
	}
}

// A worktree reset that fails after a successful transaction leaves the run
// holding an applied journal entry with pending sync while the refs already
// sit at their new SHAs.
func TestRoundCommitHook_FailedResetLeavesPendingSync(t *testing.T) {
	h := newRoundRelocationHarness(t, func(real *git.WorktreeManager) feature.WorktreeOps {
		return &failingResetWorktrees{WorktreeManager: real, err: fmt.Errorf("reset refused")}
	})
	h.dirty(t, "fix-l1.txt", "layer 1 fix\n")
	h.writeManifest(t, "entries:\n  - layer: 1\n    repository: repo-a\n    paths:\n      - fix-l1.txt\n")

	if err := h.hook(h.fixInput()); err != nil {
		t.Fatalf("round commit must survive a failed worktree reset: %v", err)
	}

	layer1 := h.ref(t, roundLayer1Branch)
	if layer1 == h.a2 {
		t.Fatal("layer 1 ref did not move; the transaction landed")
	}
	if got := runRoundGit(t, h.repo, "show", layer1+":fix-l1.txt"); got != "layer 1 fix" {
		t.Fatalf("layer 1 tip fix-l1.txt = %q, want the relocated change", got)
	}

	loaded := h.loadFeature(t)
	journal := loaded.Run().RestackJournal
	if len(journal) != 1 {
		t.Fatalf("restack journal = %+v, want one applied pending-sync entry", journal)
	}
	entry := journal[0]
	if entry.State != feature.RestackJournalApplied || !entry.PendingSync {
		t.Fatalf("journal entry = %+v, want applied with pending sync", entry)
	}
	if entry.NewTopSHA == "" {
		t.Fatal("journal entry lacks the new top SHA")
	}
	// The anchors and tips were already remapped by the applied entry.
	if got := loaded.Stack[0].Repos["repo-a"].TipSHA; got != layer1 {
		t.Fatalf("layer 1 tip = %s, want its ref %s", got, layer1)
	}
}

// Rounds that never relocate — implementation rounds, in-phase fix rounds,
// and Final Review fix rounds without a stack or with a single layer —
// commit exactly as before, one commit per dirty repository, and a stacked
// feature still gets its top layer's tip recorded live.
func TestRoundCommitHook_NonRelocatingRoundsCommitAsBefore(t *testing.T) {
	h := newRoundRelocationHarness(t, nil)

	// Implementation round.
	h.dirty(t, "impl.txt", "implementation\n")
	if err := h.hook(agent.RoundCommitInput{
		FeatureID: h.f.ID, PhaseNumber: 3, TotalPhases: 3, PhaseType: "fill-in",
		Iteration: 2, Kind: agent.RoundCommitImplement,
		Repos: map[string]string{"repo-a": h.repo},
	}); err != nil {
		t.Fatalf("implementation round commit: %v", err)
	}
	implHead := h.head(t)
	if got := h.subjects(t, h.a3, implHead); len(got) != 1 {
		t.Fatalf("implementation round subjects = %v, want one commit", got)
	}
	loaded := h.loadFeature(t)
	if got := loaded.Stack[1].Repos["repo-a"].TipSHA; got != implHead {
		t.Fatalf("live top tip = %s, want the round's HEAD %s", got, implHead)
	}

	// In-phase fix round.
	h.dirty(t, "fix.txt", "fix\n")
	if err := h.hook(agent.RoundCommitInput{
		FeatureID: h.f.ID, PhaseNumber: 3, TotalPhases: 3, PhaseType: "fill-in",
		Iteration: 3, Kind: agent.RoundCommitFix, FixNumber: 1,
		Repos: map[string]string{"repo-a": h.repo},
	}); err != nil {
		t.Fatalf("fix round commit: %v", err)
	}
	fixHead := h.head(t)
	if got := h.subjects(t, implHead, fixHead); len(got) != 1 {
		t.Fatalf("fix round subjects = %v, want one commit", got)
	}
	loaded = h.loadFeature(t)
	if got := loaded.Stack[1].Repos["repo-a"].TipSHA; got != fixHead {
		t.Fatalf("live top tip = %s, want the round's HEAD %s", got, fixHead)
	}

	// Final Review fix round without the iteration directory: one commit,
	// no manifest read, no warnings.
	h.dirty(t, "fr-fix.txt", "final review fix\n")
	input := h.fixInput()
	input.FixIterationDir = ""
	if err := h.hook(input); err != nil {
		t.Fatalf("final review fix round commit without iteration dir: %v", err)
	}
	frHead := h.head(t)
	if got := h.subjects(t, fixHead, frHead); len(got) != 1 {
		t.Fatalf("final review fix subjects = %v, want one commit", got)
	}
	events := drainEvents(h.o)
	if warnings := warningEvents(events, errcat.FixRelocatedAboveLayer); len(warnings) != 0 {
		t.Fatalf("unexpected relocation warnings: %+v", warnings)
	}

	// A single-layer stack commits as today and ignores the manifest.
	single := &feature.Feature{
		ID:                  "feat-single-layer",
		Name:                "Single Layer",
		Slug:                "single-layer",
		Status:              feature.StatusFinalReviewing,
		CurrentPhase:        feature.PhaseFinalReview,
		CurrentRoadmapPhase: 1,
		TotalRoadmapPhases:  1,
		SchemaVersion:       feature.SchemaVersionCurrent,
		Repos:               []feature.FeatureRepo{{Name: "repo-a", Path: h.repo, WorktreePath: h.repo, Branch: roundLayer2Branch, BaseBranch: "main"}},
	}
	single.Stack = []feature.StackLayer{{
		Position: 1, Title: "Only", Slug: "only", Phases: []int{1}, Branch: roundLayer2Branch,
		Repos: map[string]feature.StackRepoEntry{"repo-a": {TipSHA: frHead}},
	}}
	if err := h.store.Save(single); err != nil {
		t.Fatalf("save single-layer feature: %v", err)
	}
	singleHeadBefore := frHead
	h.dirty(t, "single.txt", "single layer fix\n")
	iterDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(iterDir, "fix-manifest.yaml"), []byte("entries:\n  - layer: 1\n    repository: repo-a\n    paths:\n      - single.txt\n"), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	singleInput := agent.RoundCommitInput{
		FeatureID: single.ID, Iteration: 1, Kind: agent.RoundCommitFinalReviewFix, FixNumber: 1,
		FixIterationDir: iterDir, Repos: map[string]string{"repo-a": h.repo},
	}
	if err := h.hook(singleInput); err != nil {
		t.Fatalf("single-layer final review fix commit: %v", err)
	}
	singleHead := h.head(t)
	if got := h.subjects(t, singleHeadBefore, singleHead); len(got) != 1 {
		t.Fatalf("single-layer subjects = %v, want one commit exactly as today", got)
	}
	if got := h.ref(t, roundLayer1Branch); got != h.a2 {
		t.Fatalf("layer 1 ref = %s, want untouched by a single-layer feature", got)
	}
}
