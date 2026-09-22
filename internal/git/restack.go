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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// RestackCutPoint labels one commit of the linear chain a restack rewrites.
// Cut points are listed bottom-up, starting at the chain's base commit; the
// segment a cut point owns is the range of commits strictly above the
// previous cut point up to and including its own. Labels are caller-chosen
// (for example "base", "phase:3", "layer:1") so anchors and layer tips can
// be mapped back by name; they must be unique and non-empty.
type RestackCutPoint struct {
	Label string
	SHA   string
}

// RestackOpKind selects which restack operation an entry describes.
type RestackOpKind string

const (
	// RestackReplaceBase replaces the chain's base commit (the first cut
	// point) with another commit; every segment replays onto the new base.
	RestackReplaceBase RestackOpKind = "replace_base"
	// RestackDropSegment drops the segment owned by the named cut point —
	// every commit strictly above the previous cut point up to and including
	// the named one. Segments above replay without them.
	RestackDropSegment RestackOpKind = "drop_segment"
	// RestackInsertAfter inserts the given commits immediately after the
	// named cut point, extending the segment that cut point owns: the cut
	// point's new SHA becomes the last surviving inserted commit.
	RestackInsertAfter RestackOpKind = "insert_after"
	// RestackAppendChain appends the given commits above the last cut point
	// (the chain's top); the existing cut points keep their positions.
	RestackAppendChain RestackOpKind = "append_chain"
)

// RestackOp is one operation in a restack request. CutPointLabel targets the
// base cut point for ReplaceBase, the cut point whose segment is dropped for
// DropSegment, and the cut point to insert after for InsertAfter; AppendChain
// ignores it. ReplaceBaseSHA carries the replacement base commit and
// CommitSHAs the commits to insert or append.
type RestackOp struct {
	Kind           RestackOpKind
	CutPointLabel  string
	ReplaceBaseSHA string
	CommitSHAs     []string
}

// RestackNewCutPoint is a cut point's position in the rewritten chain.
type RestackNewCutPoint struct {
	Label string
	SHA   string
}

// RestackResult describes a successful restack. CutPoints lists every cut
// point's new SHA in input order — a cut point whose commits all vanished
// maps to the nearest surviving commit below it. CommitMap carries the
// old-to-new SHA of every replayed commit, with a dropped commit pointing at
// its predecessor's new SHA. Dropped lists the old SHAs of commits whose
// replay left an empty tree change. HeadSHA is the rewritten chain's top.
type RestackResult struct {
	CutPoints []RestackNewCutPoint
	CommitMap map[string]string
	Dropped   []string
	HeadSHA   string
}

// RestackConflictError reports a cherry-pick conflict during a restack
// replay. SegmentFrom and SegmentTo name the bounding cut points of the
// segment being replayed, CommitSHA the commit whose application conflicted,
// and ConflictFiles the conflicted paths. When a resolver engaged, Attempts
// carries the number of resolution attempts used, LastFailure the reason the
// final attempt failed, and AttemptDir the directory the attempts were logged
// under; a zero Attempts means no resolver engaged and the conflict aborted
// immediately. The attempt was aborted and no ref of the repository changed.
type RestackConflictError struct {
	SegmentFrom   string
	SegmentTo     string
	CommitSHA     string
	ConflictFiles []string
	Attempts      int
	LastFailure   string
	AttemptDir    string
}

func (e *RestackConflictError) Error() string {
	base := fmt.Sprintf("restack conflict in segment %s..%s applying %s: %v",
		e.SegmentFrom, e.SegmentTo, e.CommitSHA, e.ConflictFiles)
	if e.Attempts > 0 {
		base += fmt.Sprintf(" after %d resolution attempts", e.Attempts)
	}
	return base
}

// extractConflictFiles reads the conflict file list from a worktree with an
// in-progress cherry-pick or merge.
func extractConflictFiles(worktreePath string) []string {
	cmd := readGitCmd(worktreePath, "diff", "--name-only", "--diff-filter=U")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var files []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			files = append(files, line)
		}
	}
	return files
}

// restackSegLabel returns the label bounding a segment's upper end, or a
// readable placeholder for the region above the top cut point.
func restackSegLabel(cutPoints []RestackCutPoint, idx int) string {
	if idx >= 0 && idx < len(cutPoints) {
		return cutPoints[idx].Label
	}
	return "(top)"
}

// RestackChain computes a rewritten chain from the ordered cut points of a
// linear chain and a list of operations, replaying every affected segment
// commit by commit with cherry-picks inside a detached temporary worktree
// created under the OS temporary directory. Segments below the first affected
// cut point are kept as they are. Author, message, and trailers of replayed
// commits are preserved. A commit whose replay leaves the index identical to
// HEAD is skipped and recorded as dropped.
//
// RestackChain is a pure single attempt: it never falls back, never resolves
// conflicts, and never modifies any ref or the repository's existing
// worktrees. On conflict it returns *RestackConflictError; on any other
// failure a plain error. The temporary worktree is removed from the worktree
// list and from disk on success, on conflict, and on any other error.
func RestackChain(mainRepo string, cutPoints []RestackCutPoint, ops []RestackOp) (*RestackResult, error) {
	return restackChain(mainRepo, cutPoints, ops, nil, "")
}

// restackChain is the shared core of RestackChain and
// RestackChainWithResolver; a non-nil resolver pauses conflicting picks for
// resolution instead of aborting them (see RestackChainWithResolver).
func restackChain(mainRepo string, cutPoints []RestackCutPoint, ops []RestackOp, resolver RestackConflictResolver, attemptsRoot string) (*RestackResult, error) {
	if len(cutPoints) == 0 {
		return nil, fmt.Errorf("restack requires at least the base cut point")
	}
	if err := validateRestackInputs(mainRepo, cutPoints, ops); err != nil {
		return nil, err
	}

	n := len(cutPoints) - 1
	plan, err := planRestack(cutPoints, ops)
	if err != nil {
		return nil, err
	}

	// Temporary detached worktree under the OS temporary directory, removed
	// on every exit path (disk and worktree registry), like the
	// merge-candidate helper.
	tmpDir, err := os.MkdirTemp("", "restack-*")
	if err != nil {
		return nil, fmt.Errorf("creating temp dir for restack: %w", err)
	}
	defer os.RemoveAll(tmpDir)
	tmpWorktree := filepath.Join(tmpDir, "wt")
	addCmd := exec.Command("git", "-C", mainRepo, "worktree", "add", "--detach", tmpWorktree, plan.start)
	if out, err := addCmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("creating restack temp worktree at %s: %s: %w", plan.start, strings.TrimSpace(string(out)), err)
	}
	defer func() {
		_ = exec.Command("git", "-C", mainRepo, "worktree", "remove", "--force", tmpWorktree).Run()
	}()

	result := &RestackResult{CommitMap: make(map[string]string)}
	cpNew := make([]string, len(cutPoints))
	head := plan.start

	// Kept region: base plus every segment below the first affected one.
	cpNew[0] = cutPoints[0].SHA
	if plan.replaceBase != "" {
		cpNew[0] = plan.replaceBase
	}
	for j := 1; j < plan.firstSeg && j <= n; j++ {
		cpNew[j] = cutPoints[j].SHA
	}

	apply := func(oldSHA, segFrom, segTo string) error {
		newSHA, dropped, err := restackCherryPick(mainRepo, tmpWorktree, oldSHA, segFrom, segTo, resolver, attemptsRoot)
		if err != nil {
			return err
		}
		if dropped {
			result.Dropped = append(result.Dropped, oldSHA)
			result.CommitMap[oldSHA] = head
			return nil
		}
		head = newSHA
		result.CommitMap[oldSHA] = newSHA
		return nil
	}
	applyAll := func(shas []string, segFrom, segTo string) error {
		for _, sha := range shas {
			if err := apply(sha, segFrom, segTo); err != nil {
				return err
			}
		}
		return nil
	}

	if plan.firstSeg <= n {
		// Inserts after the last kept cut point extend its segment before the
		// first replayed segment lands on top.
		boundary := plan.firstSeg - 1
		if err := applyAll(plan.inserts[boundary], cutPoints[boundary].Label, restackSegLabel(cutPoints, boundary+1)); err != nil {
			return nil, err
		}
		cpNew[boundary] = head

		for j := plan.firstSeg; j <= n; j++ {
			segFrom := cutPoints[j-1].Label
			segTo := cutPoints[j].Label
			if plan.droppedSegs[j] {
				commits, err := restackSegmentCommits(mainRepo, cutPoints[j-1].SHA, cutPoints[j].SHA)
				if err != nil {
					return nil, err
				}
				for _, c := range commits {
					result.Dropped = append(result.Dropped, c)
					// A commit inserted elsewhere in this same restack (a
					// move: insert after the new position, drop the old
					// segment) keeps the mapping its insert recorded — the
					// surviving copy is where the commit's content lives on
					// the rewritten chain. Only commits removed outright map
					// to their predecessor's new SHA.
					if _, moved := result.CommitMap[c]; moved {
						continue
					}
					result.CommitMap[c] = head
				}
			} else {
				commits, err := restackSegmentCommits(mainRepo, cutPoints[j-1].SHA, cutPoints[j].SHA)
				if err != nil {
					return nil, err
				}
				for _, c := range commits {
					if err := apply(c, segFrom, segTo); err != nil {
						return nil, err
					}
				}
			}
			// Inserts after this cut point extend its segment.
			if err := applyAll(plan.inserts[j], segTo, restackSegLabel(cutPoints, j+1)); err != nil {
				return nil, err
			}
			// A cut point whose commits all vanished shares the tip below.
			cpNew[j] = head
		}
	} else {
		// No existing segment replays; inserts after the top cut point extend
		// its segment, above the kept chain.
		if err := applyAll(plan.inserts[n], cutPoints[n].Label, restackSegLabel(cutPoints, n+1)); err != nil {
			return nil, err
		}
		cpNew[n] = head
	}

	// Appended commits sit above every cut point; cut points keep positions.
	if err := applyAll(plan.appends, cutPoints[n].Label, restackSegLabel(cutPoints, n+1)); err != nil {
		return nil, err
	}

	for i, cp := range cutPoints {
		result.CutPoints = append(result.CutPoints, RestackNewCutPoint{Label: cp.Label, SHA: cpNew[i]})
	}
	result.HeadSHA = head
	return result, nil
}

// restackPlan is the computed shape of one restack attempt. firstSeg is the
// 1-based index of the first segment to replay (n+1 when no existing segment
// replays); segments below it are kept as they are. droppedSegs marks
// segments removed by drop operations. inserts maps a cut point index to the
// commits inserted after it. appends are the commits appended above the top
// cut point. start is the commit the temp worktree is created at; replaceBase
// is the replacement base commit, empty when the base is kept.
type restackPlan struct {
	firstSeg    int
	droppedSegs map[int]bool
	inserts     map[int][]string
	appends     []string
	start       string
	replaceBase string
}

func planRestack(cutPoints []RestackCutPoint, ops []RestackOp) (*restackPlan, error) {
	n := len(cutPoints) - 1
	indexOf := func(label string) (int, error) {
		for i, cp := range cutPoints {
			if cp.Label == label {
				return i, nil
			}
		}
		return -1, fmt.Errorf("restack operation names unknown cut point %q", label)
	}

	plan := &restackPlan{
		firstSeg:    n + 1,
		droppedSegs: make(map[int]bool),
		inserts:     make(map[int][]string),
		start:       cutPoints[0].SHA,
	}
	for _, op := range ops {
		switch op.Kind {
		case RestackReplaceBase:
			if plan.replaceBase != "" {
				return nil, fmt.Errorf("restack allows at most one base replacement")
			}
			if op.ReplaceBaseSHA == "" {
				return nil, fmt.Errorf("replace_base operation requires a replacement commit")
			}
			plan.replaceBase = op.ReplaceBaseSHA
			plan.start = op.ReplaceBaseSHA
			if plan.firstSeg > 1 {
				plan.firstSeg = 1
			}
		case RestackDropSegment:
			idx, err := indexOf(op.CutPointLabel)
			if err != nil {
				return nil, err
			}
			if idx < 1 {
				return nil, fmt.Errorf("drop_segment cannot target the base cut point %q", op.CutPointLabel)
			}
			plan.droppedSegs[idx] = true
			if plan.firstSeg > idx {
				plan.firstSeg = idx
			}
		case RestackInsertAfter:
			idx, err := indexOf(op.CutPointLabel)
			if err != nil {
				return nil, err
			}
			if len(op.CommitSHAs) == 0 {
				return nil, fmt.Errorf("insert_after operation for %q requires commits", op.CutPointLabel)
			}
			plan.inserts[idx] = append(plan.inserts[idx], op.CommitSHAs...)
			if plan.firstSeg > idx+1 {
				plan.firstSeg = idx + 1
			}
		case RestackAppendChain:
			if len(op.CommitSHAs) == 0 {
				return nil, fmt.Errorf("append_chain operation requires commits")
			}
			plan.appends = append(plan.appends, op.CommitSHAs...)
		default:
			return nil, fmt.Errorf("unknown restack operation %q", op.Kind)
		}
	}
	if plan.firstSeg <= n {
		plan.start = cutPoints[plan.firstSeg-1].SHA
		if plan.replaceBase != "" {
			plan.start = plan.replaceBase
		}
	} else {
		// No existing segment replays; appended or inserted commits land
		// above the top cut point (or the replaced base when it is the only
		// cut point).
		plan.start = cutPoints[n].SHA
		if plan.replaceBase != "" {
			plan.start = plan.replaceBase
		}
	}
	return plan, nil
}

// validateRestackInputs checks the cut points label a linear chain: labels
// are unique and non-empty, SHAs resolve, and each cut point is an ancestor
// of the one above it.
func validateRestackInputs(mainRepo string, cutPoints []RestackCutPoint, ops []RestackOp) error {
	seen := make(map[string]bool, len(cutPoints))
	for i, cp := range cutPoints {
		if cp.Label == "" {
			return fmt.Errorf("restack cut point %d has an empty label", i)
		}
		if seen[cp.Label] {
			return fmt.Errorf("restack cut point label %q is duplicated", cp.Label)
		}
		seen[cp.Label] = true
		if cp.SHA == "" {
			return fmt.Errorf("restack cut point %q has an empty SHA", cp.Label)
		}
	}
	for i := 1; i < len(cutPoints); i++ {
		if cutPoints[i].SHA == cutPoints[i-1].SHA {
			return fmt.Errorf("restack cut points %q and %q sit on the same commit", cutPoints[i-1].Label, cutPoints[i].Label)
		}
		cmd := exec.Command("git", "-C", mainRepo, "merge-base", "--is-ancestor", cutPoints[i-1].SHA, cutPoints[i].SHA)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("restack cut point %q is not an ancestor of %q", cutPoints[i-1].Label, cutPoints[i].Label)
		}
	}
	return nil
}

// CommitsBetween lists the commits of one linear range — strictly above
// lower and up to and including upper — oldest first.
func CommitsBetween(repoPath, lower, upper string) ([]string, error) {
	cmd := readGitCmd(repoPath, "rev-list", "--reverse", lower+".."+upper)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("listing commits %s..%s: %w", lower, upper, err)
	}
	var commits []string
	for _, line := range strings.Fields(string(out)) {
		commits = append(commits, line)
	}
	return commits, nil
}

// restackSegmentCommits lists the commits of one segment — strictly above
// lower and up to and including upper — oldest first.
func restackSegmentCommits(mainRepo, lower, upper string) ([]string, error) {
	return CommitsBetween(mainRepo, lower, upper)
}

// restackCherryPick applies one commit in the restack worktree with a plain
// cherry-pick. A conflict returns *RestackConflictError naming the segment
// and the conflicted files — unless a resolver is supplied, in which case the
// pick is left in progress inside the worktree and handed to the resolver:
// resolved re-scans, stages, and continues; exhausted aborts and returns the
// conflict error with the attempt count; a resolver error aborts and
// propagates. A pick whose result is empty — detected by comparing the index
// with HEAD while a cherry-pick is in progress — is skipped as dropped. The
// caller receives the new HEAD SHA for applied commits.
func restackCherryPick(mainRepo, tmpWorktree, commitSHA, segFrom, segTo string, resolver RestackConflictResolver, attemptsRoot string) (newSHA string, dropped bool, err error) {
	pickArgs := append([]string{"-C", tmpWorktree}, identityFallbackArgs(tmpWorktree)...)
	pickArgs = append(pickArgs, "cherry-pick", commitSHA)
	out, pickErr := exec.Command("git", pickArgs...).CombinedOutput()
	if pickErr == nil {
		headCmd := readGitCmd(tmpWorktree, "rev-parse", "HEAD")
		headOut, err := headCmd.Output()
		if err != nil {
			return "", false, fmt.Errorf("capturing restack HEAD after %s: %w", commitSHA, err)
		}
		sha := strings.TrimSpace(string(headOut))
		if sha == "" {
			return "", false, fmt.Errorf("restack HEAD is empty after %s", commitSHA)
		}
		return sha, false, nil
	}

	conflictFiles := extractConflictFiles(tmpWorktree)
	if len(conflictFiles) > 0 {
		if resolver != nil {
			return restackResolveConflict(mainRepo, tmpWorktree, commitSHA, segFrom, segTo, conflictFiles, resolver, attemptsRoot)
		}
		abortRestackPick(tmpWorktree)
		return "", false, &RestackConflictError{
			SegmentFrom:   segFrom,
			SegmentTo:     segTo,
			CommitSHA:     commitSHA,
			ConflictFiles: conflictFiles,
		}
	}
	if restackPickEmpty(tmpWorktree) {
		abortRestackPick(tmpWorktree)
		return "", true, nil
	}
	return "", false, fmt.Errorf("cherry-pick %s in restack: %s: %w", commitSHA, strings.TrimSpace(string(out)), pickErr)
}

// abortRestackPick cancels an in-progress cherry-pick in the restack
// worktree. Failures are ignored: the worktree is removed on every exit path
// regardless.
func abortRestackPick(tmpWorktree string) {
	_ = exec.Command("git", "-C", tmpWorktree, "cherry-pick", "--abort").Run()
}

// restackResolveConflict engages the resolver for one conflicting pick. The
// pick stays in progress inside the temporary worktree for the duration, so
// the resolver's edits land on the paused pick and the continuation commits
// them with the original author, message, and trailers.
func restackResolveConflict(mainRepo, tmpWorktree, commitSHA, segFrom, segTo string, conflictFiles []string, resolver RestackConflictResolver, attemptsRoot string) (newSHA string, dropped bool, err error) {
	res, resolveErr := resolver(RestackResolverInput{
		WorktreePath:  tmpWorktree,
		MainRepo:      mainRepo,
		SegmentFrom:   segFrom,
		SegmentTo:     segTo,
		CommitSHA:     commitSHA,
		ConflictFiles: conflictFiles,
		AttemptRoot:   attemptsRoot,
	})
	if resolveErr != nil {
		abortRestackPick(tmpWorktree)
		return "", false, fmt.Errorf("resolving the conflict applying %s: %w", commitSHA, resolveErr)
	}
	switch res.Resolution {
	case RestackResolutionExhausted:
		abortRestackPick(tmpWorktree)
		return "", false, &RestackConflictError{
			SegmentFrom:   segFrom,
			SegmentTo:     segTo,
			CommitSHA:     commitSHA,
			ConflictFiles: conflictFiles,
			Attempts:      res.Attempts,
			LastFailure:   res.LastFailure,
			AttemptDir:    res.AttemptDir,
		}
	case RestackResolutionResolved:
		return restackContinuePick(tmpWorktree, commitSHA, conflictFiles)
	default:
		abortRestackPick(tmpWorktree)
		return "", false, fmt.Errorf("resolver returned an unknown resolution %d for commit %s", res.Resolution, commitSHA)
	}
}

// restackContinuePick finishes a resolver-resolved cherry-pick: it re-scans
// the conflicted files for markers (a resolution that still holds markers is
// a failed primitive run, never a silent commit), stages exactly those files,
// and continues the pick non-interactively. A continuation whose result is
// empty is skipped and recorded as dropped, exactly like a replay that became
// empty on its own.
func restackContinuePick(tmpWorktree, commitSHA string, conflictFiles []string) (newSHA string, dropped bool, err error) {
	marked, err := ConflictMarkerFilesInPaths(tmpWorktree, conflictFiles)
	if err != nil {
		abortRestackPick(tmpWorktree)
		return "", false, fmt.Errorf("scanning the resolved files of %s: %w", commitSHA, err)
	}
	if len(marked) > 0 {
		abortRestackPick(tmpWorktree)
		return "", false, fmt.Errorf("conflict resolution left conflict markers in %s", strings.Join(marked, ", "))
	}

	addArgs := append([]string{"-C", tmpWorktree, "add", "--"}, conflictFiles...)
	if out, err := exec.Command("git", addArgs...).CombinedOutput(); err != nil {
		abortRestackPick(tmpWorktree)
		return "", false, fmt.Errorf("staging the resolved files of %s: %s: %w", commitSHA, strings.TrimSpace(string(out)), err)
	}

	// A non-interactive editor keeps `cherry-pick --continue` from waiting
	// on a terminal; the pick's recorded author and message are used as-is.
	continueArgs := append([]string{"-C", tmpWorktree}, identityFallbackArgs(tmpWorktree)...)
	continueArgs = append(continueArgs, "-c", "core.editor=true", "cherry-pick", "--continue")
	out, contErr := exec.Command("git", continueArgs...).CombinedOutput()
	if contErr == nil {
		headCmd := readGitCmd(tmpWorktree, "rev-parse", "HEAD")
		headOut, err := headCmd.Output()
		if err != nil {
			abortRestackPick(tmpWorktree)
			return "", false, fmt.Errorf("capturing restack HEAD after continuing %s: %w", commitSHA, err)
		}
		sha := strings.TrimSpace(string(headOut))
		if sha == "" {
			abortRestackPick(tmpWorktree)
			return "", false, fmt.Errorf("restack HEAD is empty after continuing %s", commitSHA)
		}
		return sha, false, nil
	}
	// The resolution may have taken the target's side entirely: the pick
	// still in progress with an index identical to HEAD is an empty
	// continuation, dropped like any empty replay.
	if restackPickEmpty(tmpWorktree) {
		abortRestackPick(tmpWorktree)
		return "", true, nil
	}
	abortRestackPick(tmpWorktree)
	return "", false, fmt.Errorf("continuing the resolved cherry-pick of %s: %s: %w", commitSHA, strings.TrimSpace(string(out)), contErr)
}

// restackPickEmpty reports whether a failed cherry-pick with no conflicted
// files left the index identical to HEAD while the pick is still in
// progress — git's "the previous cherry-pick is now empty" state.
func restackPickEmpty(tmpWorktree string) bool {
	if !restackCherryPickInProgress(tmpWorktree) {
		return false
	}
	cmd := readGitCmd(tmpWorktree, "diff", "--cached", "--quiet", "HEAD")
	return cmd.Run() == nil
}

// restackCherryPickInProgress reports whether a cherry-pick operation state
// exists in the temp worktree's git directory.
func restackCherryPickInProgress(tmpWorktree string) bool {
	gitDirOut, err := readGitCmd(tmpWorktree, "rev-parse", "--git-dir").Output()
	if err != nil {
		return false
	}
	gitDir := strings.TrimSpace(string(gitDirOut))
	if gitDir == "" {
		return false
	}
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(tmpWorktree, gitDir)
	}
	_, err = os.Stat(filepath.Join(gitDir, "CHERRY_PICK_HEAD"))
	return err == nil
}

// CommitTreeSHA returns the tree identifier of the given commit, so callers
// can compare the trees of two commits byte-for-byte.
func CommitTreeSHA(repoPath, commitSHA string) (string, error) {
	cmd := readGitCmd(repoPath, "rev-parse", commitSHA+"^{tree}")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("resolving tree of %s in %s: %w", commitSHA, repoPath, err)
	}
	tree := strings.TrimSpace(string(out))
	if tree == "" {
		return "", fmt.Errorf("tree of %s in %s is empty", commitSHA, repoPath)
	}
	return tree, nil
}

// RestackChain exposes the package-level restack primitive on the manager.
func (m *WorktreeManager) RestackChain(mainRepo string, cutPoints []RestackCutPoint, ops []RestackOp) (*RestackResult, error) {
	return RestackChain(mainRepo, cutPoints, ops)
}

// CommitTreeSHA exposes the package-level tree helper on the manager.
func (m *WorktreeManager) CommitTreeSHA(repoPath, commitSHA string) (string, error) {
	return CommitTreeSHA(repoPath, commitSHA)
}
