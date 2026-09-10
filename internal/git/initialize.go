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
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// initializeOperationTimeout bounds the whole initialize sequence (probes,
// commit construction and the compare-and-swap ref update). Overridable in
// tests so deadline behavior can be exercised without sleeping.
var initializeOperationTimeout = 60 * time.Second

// initializeRefLockRetryWindow bounds how long a contended compare-and-swap
// ref update is retried when a competing Git process holds the ref lock.
// Overridable in tests.
var initializeRefLockRetryWindow = 3 * time.Second

// initializeFallbackBranch is used only when the repository has no valid
// symbolic branch: the plan allows main exactly then, never as a
// replacement for a valid existing branch name.
const initializeFallbackBranch = "main"

// Eligibility conditions that the caller maps onto canonical refusal codes.
// They are distinguished errors — never classified from output text — so a
// failed probe can never be mistaken for a refusal or for an unborn repo.
var (
	// ErrInitializeNotARepository reports that the path is not a git
	// repository (replaced or removed since discovery).
	ErrInitializeNotARepository = errors.New("path is not a git repository")
	// ErrInitializeOperationActive reports an in-progress merge, rebase,
	// cherry-pick or revert in the repository.
	ErrInitializeOperationActive = errors.New("a merge, rebase, cherry-pick or revert is in progress")
	// ErrInitializeContentPresent reports staged, unstaged or untracked
	// user content in the repository.
	ErrInitializeContentPresent = errors.New("repository has staged, unstaged or untracked content")
	// ErrInitializeIdentityChanged reports that the repository no longer
	// matches the server-resolved identity that authorized the operation.
	ErrInitializeIdentityChanged = errors.New("repository identity changed before initialization")
	// ErrInitializeProbeFailed reports that repository state could not be
	// proved (probe failure, timeout or unexpected git output). Callers
	// must treat it as indeterminate, never as unborn or clean.
	ErrInitializeProbeFailed = errors.New("repository state could not be proved")
)

// InitializeOutcome reports what the initialize operation did.
type InitializeOutcome struct {
	// Branch is the branch that received (or already carried) the initial
	// commit, verbatim, including slash-containing names.
	Branch string
	// Head is the resolved HEAD commit after the operation.
	Head string
	// AlreadyInitialized reports that HEAD already resolved (a competing
	// initializer or an external commit won): no commit was created and
	// nothing was overwritten. The caller treats it as refresh-only
	// success.
	AlreadyInitialized bool
}

// InitializeRepository creates exactly one empty root commit in the existing
// unborn repository at dir, preserving its origin remotes, its valid
// symbolic branch (verbatim, including slash-containing names) and every
// existing file. It never reinitializes, republishes or pushes.
//
// Eligibility is proved before any mutation: the repository must have no
// commits, no staged, unstaged or untracked content (ignored files are
// untouched and allowed), and no in-progress merge, rebase, cherry-pick or
// revert. A resolved existing commit short-circuits as a refresh-only
// success without any content check, so an already-initialized repository
// refreshes even when it has local changes.
//
// Exactly-once is structural rather than advisory: the commit object is
// built without touching the index or running hooks (git commit-tree), and
// the branch ref is created with a compare-and-swap update-ref whose old
// value is empty — the ref must not exist. A competing initializer that
// created the ref first therefore makes this fail and re-read HEAD instead
// of producing a second commit or replacing the competing commit. The
// whole sequence holds the shared per-path mutation lock, so Agentico's
// own affected-checkout mutations on the same repository serialize with it.
func InitializeRepository(ctx context.Context, dir string) (InitializeOutcome, error) {
	return initializeRepository(ctx, dir, nil)
}

// InitializeRepositoryAtIdentity is InitializeRepository with an additional
// identity fence. The expected identity is re-resolved while holding the
// shared repository mutation lock, so a checkout replaced after catalog
// resolution cannot inherit the prior request's authority.
func InitializeRepositoryAtIdentity(ctx context.Context, dir string, expected RepoIdentity) (InitializeOutcome, error) {
	return initializeRepository(ctx, dir, &expected)
}

func initializeRepository(ctx context.Context, dir string, expected *RepoIdentity) (InitializeOutcome, error) {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return InitializeOutcome{}, fmt.Errorf("%w: %s", ErrInitializeNotARepository, dir)
	}

	mu := worktreeMutationLock(dir)
	mu.Lock()
	defer mu.Unlock()
	if expected != nil {
		resolved, ok := ResolveRepoIdentity(dir)
		if !ok || !resolved.Equal(*expected) {
			return InitializeOutcome{}, ErrInitializeIdentityChanged
		}
	}

	ctx, cancel := context.WithTimeout(ctx, initializeOperationTimeout)
	defer cancel()

	head, err := initializeHeadState(ctx, dir)
	if err != nil {
		return InitializeOutcome{}, err
	}
	if head != "" {
		branch, branchErr := initializeBranch(ctx, dir)
		if branchErr != nil {
			return InitializeOutcome{}, branchErr
		}
		return InitializeOutcome{Branch: branch, Head: head, AlreadyInitialized: true}, nil
	}

	if err := initializeOperationState(dir); err != nil {
		return InitializeOutcome{}, err
	}

	branch, hasSymbolic, err := initializeBranchName(ctx, dir)
	if err != nil {
		return InitializeOutcome{}, err
	}

	remotesBefore, err := initializeRemotes(ctx, dir)
	if err != nil {
		return InitializeOutcome{}, err
	}

	if err := initializeProveClean(ctx, dir); err != nil {
		return InitializeOutcome{}, err
	}

	installed, commitSHA, err := initializeCreateCommit(ctx, dir, branch, hasSymbolic)
	if err != nil {
		return InitializeOutcome{}, err
	}

	// Post-verification: the promised shape must hold before success is
	// reported, so hostile configuration or a racing actor fails loudly
	// instead of reporting a different result.
	afterBranch, err := initializeBranch(ctx, dir)
	if err != nil {
		return InitializeOutcome{}, err
	}
	if afterBranch != branch {
		return InitializeOutcome{}, fmt.Errorf("%w: branch changed from %q to %q during initialization",
			ErrInitializeProbeFailed, branch, afterBranch)
	}
	afterHead, err := initializeHeadState(ctx, dir)
	if err != nil {
		return InitializeOutcome{}, err
	}
	if afterHead == "" {
		return InitializeOutcome{}, fmt.Errorf("%w: HEAD is still unborn after the initial commit", ErrInitializeProbeFailed)
	}
	remotesAfter, err := initializeRemotes(ctx, dir)
	if err != nil {
		return InitializeOutcome{}, err
	}
	if strings.Join(remotesAfter, "\x00") != strings.Join(remotesBefore, "\x00") {
		return InitializeOutcome{}, fmt.Errorf("%w: remote configuration changed during initialization", ErrInitializeProbeFailed)
	}
	outcome := InitializeOutcome{Branch: branch, Head: afterHead, AlreadyInitialized: !installed}
	if installed && afterHead != commitSHA {
		// The ref was created but HEAD resolves elsewhere: another actor
		// moved HEAD between the compare-and-swap and the verification.
		// Our commit stands on its branch, but it is not HEAD, so the
		// outcome is reported truthfully instead of claiming it.
		return InitializeOutcome{}, fmt.Errorf("%w: HEAD resolves to %q, not the created commit", ErrInitializeProbeFailed, afterHead)
	}
	return outcome, nil
}

// initializeOperationState refuses mutations while a merge, rebase,
// cherry-pick or revert is underway: those states own HEAD and the index,
// and an initial commit would interleave with them.
func initializeOperationState(dir string) error {
	gitDir := resolveGitDir(dir)
	markers := []string{
		"MERGE_HEAD",
		"rebase-merge",
		"rebase-apply",
		"CHERRY_PICK_HEAD",
		"REVERT_HEAD",
	}
	for _, marker := range markers {
		if _, err := os.Stat(filepath.Join(gitDir, marker)); err == nil {
			return fmt.Errorf("%w: %s is present", ErrInitializeOperationActive, marker)
		}
	}
	return nil
}

// initializeHeadState resolves HEAD. An empty return with nil error is an
// unborn branch (rev-parse --verify --quiet exits 1 with no output exactly
// then); any other failure is a probe failure, so a hung or broken git can
// never be mistaken for an empty repository.
func initializeHeadState(ctx context.Context, dir string) (string, error) {
	out, err := runInitializeGit(ctx, dir, "rev-parse", "--verify", "--quiet", "HEAD")
	if err == nil {
		return strings.TrimSpace(out), nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 && strings.TrimSpace(out) == "" {
		return "", nil
	}
	return "", fmt.Errorf("%w: resolve HEAD: %s", ErrInitializeProbeFailed, boundInitializeProbeDetail(out, err))
}

// initializeBranch reads the symbolic branch verbatim, including
// slash-containing names.
func initializeBranch(ctx context.Context, dir string) (string, error) {
	out, err := runInitializeGit(ctx, dir, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return "", fmt.Errorf("%w: read branch: %s", ErrInitializeProbeFailed, boundInitializeProbeDetail(out, err))
	}
	return strings.TrimSpace(out), nil
}

// initializeBranchName returns the branch that will receive the initial
// commit. The existing symbolic branch is preserved verbatim when valid;
// the main fallback applies only when no valid symbolic branch exists.
func initializeBranchName(ctx context.Context, dir string) (branch string, hasSymbolic bool, err error) {
	full, fullErr := runInitializeGit(ctx, dir, "symbolic-ref", "HEAD")
	if fullErr != nil {
		var exitErr *exec.ExitError
		if errors.As(fullErr, &exitErr) {
			// Git ran and proved that HEAD is detached or malformed: no
			// valid symbolic branch exists, so the permitted fallback
			// applies. Transport failures and expired contexts are
			// indeterminate and must never be mistaken for this case.
			return initializeFallbackBranch, false, nil
		}
		return "", false, fmt.Errorf("%w: read symbolic branch: %s", ErrInitializeProbeFailed, boundInitializeProbeDetail(full, fullErr))
	}
	ref := strings.TrimSpace(full)
	short := strings.TrimPrefix(ref, "refs/heads/")
	if short == ref || !validInitializeBranchName(short) {
		// HEAD points outside refs/heads/ or at an unusable name.
		return initializeFallbackBranch, false, nil
	}
	return short, true, nil
}

// validInitializeBranchName applies the git check-ref-format rules that
// matter here. Slash-containing names are valid and preserved verbatim.
func validInitializeBranchName(name string) bool {
	if name == "" || len(name) > 512 {
		return false
	}
	if strings.Contains(name, "..") || strings.Contains(name, "//") ||
		strings.Contains(name, "@{") || strings.HasSuffix(name, ".") ||
		strings.HasSuffix(name, ".lock") || strings.HasPrefix(name, "/") ||
		strings.HasSuffix(name, "/") || strings.HasPrefix(name, ".") {
		return false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	for _, segment := range strings.Split(name, "/") {
		if segment == "" || strings.HasPrefix(segment, ".") || strings.HasSuffix(segment, ".lock") {
			return false
		}
	}
	return true
}

// initializeRemotes records the remote configuration so it can be proven
// unchanged after the commit: the origin of the cloned repository is
// preserved, never removed or rewritten.
func initializeRemotes(ctx context.Context, dir string) ([]string, error) {
	out, err := runInitializeGit(ctx, dir, "remote")
	if err != nil {
		return nil, fmt.Errorf("%w: read remotes: %s", ErrInitializeProbeFailed, boundInitializeProbeDetail(out, err))
	}
	trimmed := strings.TrimSpace(out)
	if trimmed == "" {
		return nil, nil
	}
	return strings.Split(trimmed, "\n"), nil
}

// initializeProveClean proves the repository is free of staged, unstaged and
// untracked user content. Ignored files are excluded by porcelain status
// and therefore survive untouched. Any probe failure is indeterminate and
// refuses the mutation.
func initializeProveClean(ctx context.Context, dir string) error {
	out, err := runInitializeGit(ctx, dir, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return fmt.Errorf("%w: inspect repository content: %s", ErrInitializeProbeFailed, boundInitializeProbeDetail(out, err))
	}
	if strings.TrimSpace(out) != "" {
		return ErrInitializeContentPresent
	}
	return nil
}

// initializeCreateCommit builds the empty root commit without touching the
// index and installs it on the branch with a compare-and-swap ref update
// whose old value is empty (the ref must not exist). When no valid symbolic
// branch exists, HEAD is first pointed at the main fallback branch. It
// reports whether this call's commit was installed; false means a
// competing actor's commit already stands on the branch.
func initializeCreateCommit(ctx context.Context, dir, branch string, hasSymbolic bool) (installed bool, commitSHA string, err error) {
	if !hasSymbolic {
		if out, err := runInitializeGit(ctx, dir, "symbolic-ref", "HEAD", "refs/heads/"+initializeFallbackBranch); err != nil {
			return false, "", fmt.Errorf("%w: point HEAD at main: %s", ErrInitializeProbeFailed, boundInitializeProbeDetail(out, err))
		}
	}
	ref := "refs/heads/" + branch

	emptyTree, err := runInitializeGit(ctx, dir, "mktree")
	if err != nil {
		return false, "", fmt.Errorf("%w: read empty tree: %s", ErrInitializeProbeFailed, boundInitializeProbeDetail(emptyTree, err))
	}
	tree, err := runInitializeGit(ctx, dir, "write-tree")
	if err != nil {
		return false, "", fmt.Errorf("%w: read index tree: %s", ErrInitializeProbeFailed, boundInitializeProbeDetail(tree, err))
	}
	if strings.TrimSpace(tree) != strings.TrimSpace(emptyTree) {
		// The cleanliness proof said the index was empty; a non-empty
		// tree means content raced in and must not be committed.
		return false, "", ErrInitializeContentPresent
	}

	// commit-tree builds the commit object without staging anything,
	// without running hooks and without signing; the identity is forced
	// on the command line so hostile global configuration cannot alter
	// the author or committer.
	commit, err := runInitializeGit(ctx, dir,
		"-c", "user.name="+AgenticoName,
		"-c", "user.email="+agenticoCommitIdentity(),
		"commit-tree", strings.TrimSpace(tree), "-m", "Initial commit")
	if err != nil {
		return false, "", fmt.Errorf("%w: build initial commit: %s", ErrInitializeProbeFailed, boundInitializeProbeDetail(commit, err))
	}
	commitSHA = strings.TrimSpace(commit)

	installed, err = initializeCASRef(ctx, dir, ref, commitSHA)
	if err != nil {
		return false, "", err
	}
	return installed, commitSHA, nil
}

// initializeCASRef atomically creates ref at commitSHA only when the ref
// does not exist. A ref that already exists means a competing actor
// committed first: this call reports installed=false (never a second
// commit, never a replacement). Ref-lock contention is tolerated within
// the bounded window and one provably stale lock is removed, mirroring the
// worktree mutation discipline; a fresh lock is never clobbered. A born
// HEAD discovered mid-retry is likewise a competing success.
func initializeCASRef(ctx context.Context, dir, ref, commitSHA string) (bool, error) {
	deadline := time.Now().Add(initializeRefLockRetryWindow)
	removedStaleLock := false
	for {
		out, err := runInitializeGit(ctx, dir, "update-ref", "-m", "agentico: initialize repository", ref, commitSHA, "")
		if err == nil {
			return true, nil
		}
		trimmed := strings.TrimSpace(out)
		if strings.Contains(trimmed, "reference already exists") {
			return false, nil
		}
		head, headErr := initializeHeadState(ctx, dir)
		if headErr == nil && head != "" {
			// The competing initializer's commit stands; this attempt
			// is a refresh, not a failure.
			return false, nil
		}
		lockPath, contention := lockContention([]byte(out))
		if !contention {
			return false, fmt.Errorf("%w: create branch ref: %s", ErrInitializeProbeFailed, boundInitializeProbeDetail(trimmed, err))
		}
		if lockPath != "" && !removedStaleLock {
			if info, statErr := os.Stat(lockPath); statErr == nil && time.Since(info.ModTime()) > gitLockStaleAfter {
				removedStaleLock = true
				if removeErr := os.Remove(lockPath); removeErr == nil || os.IsNotExist(removeErr) {
					continue
				}
			}
		}
		if !time.Now().Before(deadline) || ctx.Err() != nil {
			if lockPath != "" {
				var lockAge time.Duration
				if info, statErr := os.Stat(lockPath); statErr == nil {
					lockAge = time.Since(info.ModTime())
				}
				return false, fmt.Errorf("%w: %w", ErrInitializeProbeFailed,
					&GitLockContentionError{LockPath: lockPath, Age: lockAge})
			}
			return false, fmt.Errorf("%w: branch ref stayed locked: %s", ErrInitializeProbeFailed, boundInitializeProbeDetail(trimmed, err))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// runInitializeGit runs one bounded git invocation inside dir with the same
// environment discipline as repository creation: request-scoped git dir
// overrides are dropped so the operation can never be redirected into
// another worktree, optional locks are disabled so probes never block on an
// index lock, and the process is its own group, killed as a group when the
// bound is hit.
func runInitializeGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = initializeEnvironment()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return context.Canceled
	}
	cmd.WaitDelay = time.Second
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return string(out), ctx.Err()
	}
	return string(out), err
}

// initializeEnvironment keeps the server's environment, forces a
// non-interactive git, drops request-scoped git dir overrides that must
// never redirect an initialization into another worktree, and disables
// optional locks so the probes cannot block on a foreign index lock.
func initializeEnvironment() []string {
	env := createEnvironment()
	return append(env, "GIT_OPTIONAL_LOCKS=0")
}

// initializeOutputBound caps the diagnostics carried out of the git layer;
// raw command output is never forwarded unbounded.
const initializeOutputBound = 400

// boundInitializeProbeDetail reports why a probe failed: the bounded command
// output when there is any, otherwise the transport error itself (a failed
// spawn or a killed process otherwise surfaces as an empty, useless
// diagnostic). Still bounded; never the raw unbounded output.
func boundInitializeProbeDetail(out string, err error) string {
	detail := strings.TrimSpace(out)
	if detail == "" && err != nil {
		detail = err.Error()
	}
	if len(detail) > initializeOutputBound {
		detail = detail[:initializeOutputBound]
	}
	return detail
}
