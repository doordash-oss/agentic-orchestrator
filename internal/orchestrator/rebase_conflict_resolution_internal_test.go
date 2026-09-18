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

// Real-git coverage of the agent conflict-resolution budget on the Phase 12
// conflicting fixture: a scripted resolution session that writes
// marker-free content lands the pass in one attempt; a session that edits
// outside the conflicted set is reverted and retried; a session that takes
// the target's side drops the commit; the in-flight guard refuses a second
// start; and a stop during an attempt aborts the loop without parking.

package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// resolutionScript is one scripted resolution attempt: it runs against the
// temporary worktree the request carries and reports the session's outcome.
type resolutionScript struct {
	// run edits the temporary worktree and returns the session result.
	run func(req agent.ConflictResolutionRequest) (*agent.ConflictResolutionResult, error)
}

// scriptResolution installs scripted resolution attempts, in order; a call
// beyond the script's length fails the test.
func scriptResolution(t *testing.T, o *Orchestrator, scripts ...resolutionScript) {
	t.Helper()
	calls := 0
	o.SetRunConflictResolutionFn(func(ctx context.Context, req agent.ConflictResolutionRequest) (*agent.ConflictResolutionResult, error) {
		if calls >= len(scripts) {
			t.Fatalf("resolution attempt %d has no script", calls+1)
			return nil, errors.New("no script")
		}
		script := scripts[calls]
		calls++
		return script.run(req)
	})
}

// writeResolutionFile writes marker-free resolved content into the paused
// pick's temporary worktree.
func writeResolutionFile(t *testing.T, req agent.ConflictResolutionRequest, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(req.WorkDir, "l2.txt"), []byte(content), 0o644); err != nil {
		t.Fatalf("writing the resolution into %s: %v", req.WorkDir, err)
	}
}

// completedResult is a session that ended with the success outcome.
func completedResult() *agent.ConflictResolutionResult {
	return &agent.ConflictResolutionResult{Status: agent.ConflictResolutionCompleted}
}

// TestRebaseResolution_ResolvesInOneAttemptAndLands proves the resolving
// journey: the start returns at once, the loop resolves the conflict in one
// scripted attempt, records one resolution (commit, files, attempts 1, not
// dropped) on the restack result, lands with the child's Final Review
// dispatched, the parent refs and the replayed chain behave exactly as the
// Phase 12 happy path, and the generated description and exit criteria name
// the resolved commit and its files.
func TestRebaseResolution_ResolvesInOneAttemptAndLands(t *testing.T) {
	fx := newRebaseRestackFixture(t, rebaseRestackFixtureOpts{Conflicting: true})
	o := fx.orchestrator()
	scriptResolution(t, o, resolutionScript{
		run: func(req agent.ConflictResolutionRequest) (*agent.ConflictResolutionResult, error) {
			writeResolutionFile(t, req, "layer2 v1\n")
			return completedResult(), nil
		},
	})

	if err := o.StartFeature(fx.childID); err != nil {
		t.Fatalf("StartFeature() error = %v", err)
	}
	child := fx.waitLanded(t)

	// One resolution recorded on the restack result: the conflicting commit,
	// its segment, its files, one attempt, not dropped.
	if len(child.Parent.RebaseRestacks) != 1 {
		t.Fatalf("restacks = %+v, want one entry for repoA", child.Parent.RebaseRestacks)
	}
	rs := child.Parent.RebaseRestacks[0]
	if len(rs.ResolvedConflicts) != 1 {
		t.Fatalf("resolved conflicts = %+v, want one", rs.ResolvedConflicts)
	}
	rc := rs.ResolvedConflicts[0]
	if rc.Commit != fx.anchors[3] || rc.Segment != "phase:2..phase:3" {
		t.Fatalf("resolution record = %+v, want the phase-3 commit in segment phase:2..phase:3", rc)
	}
	if len(rc.Files) != 1 || rc.Files[0] != "l2.txt" || rc.Attempts != 1 || rc.Dropped {
		t.Fatalf("resolution record = %+v, want l2.txt, one attempt, not dropped", rc)
	}

	// The replayed chain keeps every commit with its original subject and
	// author, exactly as the conflict-free happy path, and carries the
	// resolved content.
	subjects := restackGitLines(t, fx.childWT, "log", "--format=%s", fx.targetSHA+"..HEAD")
	reverseStrings(subjects)
	if strings.Join(subjects, "|") != strings.Join(fx.Subjects, "|") {
		t.Fatalf("replayed subjects = %v, want %v", subjects, fx.Subjects)
	}
	authors := restackGitLines(t, fx.childWT, "log", "--format=%an <%ae>", fx.targetSHA+"..HEAD")
	reverseStrings(authors)
	if strings.Join(authors, "|") != strings.Join(fx.Authors, "|") {
		t.Fatalf("replayed authors = %v, want %v", authors, fx.Authors)
	}
	resolvedSHA := rs.AnchorRemap[3]
	if content := restackGit(t, fx.childWT, "show", resolvedSHA+":l2.txt"); content != "layer2 v1" {
		t.Fatalf("resolved commit's l2.txt = %q, want the resolved content", content)
	}

	// The generated description and exit criteria name the resolved commit
	// and its files so the verification round scrutinizes them.
	if !strings.Contains(child.Description, fx.anchors[3]) || !strings.Contains(child.Description, "l2.txt") {
		t.Fatalf("description does not name the resolved commit and files:\n%s", child.Description)
	}
	if !strings.Contains(child.ExitCriteria, fx.anchors[3]) {
		t.Fatalf("exit criteria do not name the resolved commit:\n%s", child.ExitCriteria)
	}

	// Only the first attempt directory exists.
	commitRoot := filepath.Join(fx.store.BaseDir, fx.childID, "runs", "run-001", "rebase-resolution", "repoA", fx.anchors[3][:7])
	if _, err := os.Stat(filepath.Join(commitRoot, "attempt-01", "user-prompt.md")); err != nil {
		t.Fatalf("attempt-01 prompt missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(commitRoot, "attempt-02")); !os.IsNotExist(err) {
		t.Fatalf("attempt-02 exists after a one-attempt resolution: %v", err)
	}

	// The parent refs are byte-identical and no temporary worktree remains.
	for i, want := range fx.layerTips {
		ref := fmt.Sprintf("stack/%d", i+1)
		if got := fx.refSHA(ref); got != want {
			t.Fatalf("parent ref %s moved: %s, want %s", ref, got, want)
		}
	}
	if got := fx.refSHA("main"); got != fx.targetSHA {
		t.Fatalf("parent ref main moved: %s, want %s", got, fx.targetSHA)
	}
	assertNoRestackTempWorktrees(t, fx.repoDir)
}

// TestRebaseResolution_OutOfScopeEditRevertedThenResolved proves the
// guardrail harness revert: the first scripted session also writes an extra
// file outside the conflicted set, which is reverted and named in the
// attempt's feedback; the second session resolves cleanly and lands a commit
// that does not contain the extra file.
func TestRebaseResolution_OutOfScopeEditRevertedThenResolved(t *testing.T) {
	fx := newRebaseRestackFixture(t, rebaseRestackFixtureOpts{Conflicting: true})
	o := fx.orchestrator()
	scriptResolution(t, o,
		resolutionScript{
			run: func(req agent.ConflictResolutionRequest) (*agent.ConflictResolutionResult, error) {
				if err := os.WriteFile(filepath.Join(req.WorkDir, "extra.txt"), []byte("out of scope\n"), 0o644); err != nil {
					return nil, err
				}
				return completedResult(), nil
			},
		},
		resolutionScript{
			run: func(req agent.ConflictResolutionRequest) (*agent.ConflictResolutionResult, error) {
				writeResolutionFile(t, req, "layer2 v1\n")
				return completedResult(), nil
			},
		},
	)

	if err := o.StartFeature(fx.childID); err != nil {
		t.Fatalf("StartFeature() error = %v", err)
	}
	child := fx.waitLanded(t)

	rs := child.Parent.RebaseRestacks[0]
	if len(rs.ResolvedConflicts) != 1 || rs.ResolvedConflicts[0].Attempts != 2 {
		t.Fatalf("resolved conflicts = %+v, want one resolution after two attempts", rs.ResolvedConflicts)
	}

	// The first attempt's feedback names the reverted out-of-scope file, and
	// the second attempt's prompt carries it.
	commitRoot := filepath.Join(fx.store.BaseDir, fx.childID, "runs", "run-001", "rebase-resolution", "repoA", fx.anchors[3][:7])
	feedback, err := os.ReadFile(filepath.Join(commitRoot, "attempt-01", "feedback.md"))
	if err != nil {
		t.Fatalf("reading attempt-01 feedback: %v", err)
	}
	if !strings.Contains(string(feedback), "extra.txt") {
		t.Fatalf("attempt-01 feedback does not name the out-of-scope file:\n%s", feedback)
	}
	prompt, err := os.ReadFile(filepath.Join(commitRoot, "attempt-02", "user-prompt.md"))
	if err != nil {
		t.Fatalf("reading attempt-02 prompt: %v", err)
	}
	if !strings.Contains(string(prompt), "extra.txt") {
		t.Fatalf("attempt-02 prompt does not carry the out-of-scope feedback:\n%s", prompt)
	}

	// The landed commit does not contain the extra file and the child
	// worktree does not either.
	childHead := restackGit(t, fx.childWT, "rev-parse", "HEAD")
	if out := restackGitOutputErr(t, fx.childWT, "show", childHead+":extra.txt"); out == nil {
		t.Fatal("the landed chain contains the out-of-scope extra.txt")
	}
	if _, err := os.Stat(filepath.Join(fx.childWT, "extra.txt")); !os.IsNotExist(err) {
		t.Fatal("the child worktree contains the out-of-scope extra.txt")
	}
	assertNoRestackTempWorktrees(t, fx.repoDir)
}

// TestRebaseResolution_TargetSideResolutionDropsCommit proves a session that
// takes the target's side entirely lands with the commit dropped and the
// resolution recorded as dropped. (The conflicting fixture's layer-2
// segment carries two commits on the same file, so the phase-4 replay also
// conflicts and resolves to its own side.)
func TestRebaseResolution_TargetSideResolutionDropsCommit(t *testing.T) {
	fx := newRebaseRestackFixture(t, rebaseRestackFixtureOpts{Conflicting: true})
	o := fx.orchestrator()
	scriptResolution(t, o,
		resolutionScript{
			run: func(req agent.ConflictResolutionRequest) (*agent.ConflictResolutionResult, error) {
				// Exactly the target's content: the continued pick is empty.
				writeResolutionFile(t, req, "upstream conflicting content\n")
				return completedResult(), nil
			},
		},
		resolutionScript{
			run: func(req agent.ConflictResolutionRequest) (*agent.ConflictResolutionResult, error) {
				// The phase-4 commit's own side, so its replay is clean.
				writeResolutionFile(t, req, "layer2 v2\n")
				return completedResult(), nil
			},
		},
	)

	if err := o.StartFeature(fx.childID); err != nil {
		t.Fatalf("StartFeature() error = %v", err)
	}
	child := fx.waitLanded(t)

	rs := child.Parent.RebaseRestacks[0]
	if len(rs.ResolvedConflicts) != 2 {
		t.Fatalf("resolved conflicts = %+v, want two (phase 3 dropped, phase 4 kept)", rs.ResolvedConflicts)
	}
	if !rs.ResolvedConflicts[0].Dropped || rs.ResolvedConflicts[0].Commit != fx.anchors[3] {
		t.Fatalf("first resolution = %+v, want the phase-3 commit dropped", rs.ResolvedConflicts[0])
	}
	if rs.ResolvedConflicts[1].Dropped || rs.ResolvedConflicts[1].Commit != fx.anchors[4] {
		t.Fatalf("second resolution = %+v, want the phase-4 commit kept", rs.ResolvedConflicts[1])
	}
	// The dropped commit's replay vanished: the chain above the target is
	// phases 4 and 5 only.
	subjects := restackGitLines(t, fx.childWT, "log", "--format=%s", fx.targetSHA+"..HEAD")
	reverseStrings(subjects)
	want := []string{"layer2 phase4", "layer3 phase5"}
	if strings.Join(subjects, "|") != strings.Join(want, "|") {
		t.Fatalf("replayed subjects = %v, want %v (phase 3 dropped as empty)", subjects, want)
	}
}

// TestRebaseResolution_BusyGuardAndRestartOutcome proves a start issued
// while the loop runs returns the feature-busy error, the restart path
// reports the restack-running outcome, and the loop never holds the
// relationship lock while a session runs.
func TestRebaseResolution_BusyGuardAndRestartOutcome(t *testing.T) {
	fx := newRebaseRestackFixture(t, rebaseRestackFixtureOpts{Conflicting: true})
	o := fx.orchestrator()

	release := make(chan struct{})
	started := make(chan agent.ConflictResolutionRequest, 1)
	o.SetRunConflictResolutionFn(func(ctx context.Context, req agent.ConflictResolutionRequest) (*agent.ConflictResolutionResult, error) {
		started <- req
		<-release
		writeResolutionFile(t, req, "layer2 v1\n")
		return completedResult(), nil
	})

	outcome, err := o.RestartPhase(fx.childID, 0, 0)
	if err != nil {
		t.Fatalf("RestartPhase() error = %v", err)
	}
	if outcome.Action != RestartRestackRunning {
		t.Fatalf("restart outcome = %d, want RestartRestackRunning", outcome.Action)
	}

	// The loop is registered synchronously by the start, so the busy guard
	// is deterministic even before the first session begins.
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("the resolution session never started")
	}

	// While a session runs, the loop holds no relationship lock: a write-lock
	// operation completes instead of deadlocking.
	lockDone := make(chan error, 1)
	go func() {
		lockDone <- o.WithRelationshipWriteLock(func() error { return nil })
	}()
	select {
	case err := <-lockDone:
		if err != nil {
			t.Fatalf("write lock operation failed while a session ran: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the relationship write lock was held while a resolution session ran")
	}

	if err := o.StartFeature(fx.childID); !errors.Is(err, ErrFeatureBusy) {
		t.Fatalf("StartFeature() while the loop runs = %v, want ErrFeatureBusy", err)
	}
	if _, err := o.RestartPhase(fx.childID, 0, 0); !errors.Is(err, ErrFeatureBusy) {
		t.Fatalf("RestartPhase() while the loop runs = %v, want ErrFeatureBusy", err)
	}

	close(release)
	fx.waitLanded(t)
}

// TestRebaseResolution_StopDuringAttemptAbortsLoop proves stopping the child
// during the first attempt ends the loop with the child at Created, no
// attention journal, no ref change, and no temporary worktree — and that a
// later start re-runs the loop.
func TestRebaseResolution_StopDuringAttemptAbortsLoop(t *testing.T) {
	fx := newRebaseRestackFixture(t, rebaseRestackFixtureOpts{Conflicting: true})
	o := fx.orchestrator()

	release := make(chan struct{})
	started := make(chan agent.ConflictResolutionRequest, 1)
	stoppedSession := &agent.ConflictResolutionResult{
		Status: agent.ConflictResolutionFailed,
		Reason: "session was stopped",
	}
	o.SetRunConflictResolutionFn(func(ctx context.Context, req agent.ConflictResolutionRequest) (*agent.ConflictResolutionResult, error) {
		started <- req
		<-release
		return stoppedSession, nil
	})

	if err := o.StartFeature(fx.childID); err != nil {
		t.Fatalf("StartFeature() error = %v", err)
	}
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("the resolution session never started")
	}

	if err := o.InterruptFeature(fx.childID); err != nil {
		t.Fatalf("InterruptFeature() error = %v", err)
	}
	// The rebase child stays at Created: the pass's home state.
	if child := fx.reloadChild(); child.Status != feature.StatusCreated {
		t.Fatalf("child status after stop = %s, want Created", child.Status)
	}

	close(release)
	// The loop aborts without parking: the child stays at Created with no
	// journal, and the paused pick's temporary worktree is removed.
	deadline := time.Now().Add(15 * time.Second)
	for {
		if !restackTempWorktreeListed(t, fx.repoDir) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the temporary restack worktree was never removed after the stop")
		}
		time.Sleep(20 * time.Millisecond)
	}
	child := fx.reloadChild()
	if child.Status != feature.StatusCreated {
		t.Fatalf("child status after the aborted loop = %s, want Created", child.Status)
	}
	if child.Parent.Transaction != nil {
		t.Fatalf("transaction = %+v, want no attention journal after a stop", child.Parent.Transaction)
	}
	select {
	case <-fx.dispatched:
		t.Fatal("final review dispatched for a stopped pass")
	default:
	}
	for i, want := range fx.layerTips {
		ref := fmt.Sprintf("stack/%d", i+1)
		if got := fx.refSHA(ref); got != want {
			t.Fatalf("parent ref %s moved: %s, want %s", ref, got, want)
		}
	}
	if got := restackGit(t, fx.childWT, "rev-parse", "HEAD"); got != fx.parentTip {
		t.Fatalf("child worktree HEAD = %s, want the parent tip %s", got, fx.parentTip)
	}

	// A later start re-runs the loop from scratch.
	scriptResolution(t, o, resolutionScript{
		run: func(req agent.ConflictResolutionRequest) (*agent.ConflictResolutionResult, error) {
			writeResolutionFile(t, req, "layer2 v1\n")
			return completedResult(), nil
		},
	})
	if err := o.StartFeature(fx.childID); err != nil {
		t.Fatalf("StartFeature() after the stop = %v", err)
	}
	fx.waitLanded(t)
}

// restackGitOutputErr runs one git command and reports whether it succeeded,
// for asserting a path's absence from a commit.
func restackGitOutputErr(t *testing.T, dir string, args ...string) error {
	t.Helper()
	_, err := restackGitOutput(t, dir, args...)
	return err
}
