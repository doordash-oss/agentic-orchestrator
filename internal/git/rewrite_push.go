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
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// RewritePushErrorKind identifies a safety refusal while replacing a remote
// branch after its local history was rewritten.
type RewritePushErrorKind string

const (
	// RewritePushRemoteDiverged means the inspected remote contains work that
	// cannot be proven redundant with the rewritten local history.
	RewritePushRemoteDiverged RewritePushErrorKind = "remote_diverged"
	// RewritePushRemoteChanged means the remote moved after inspection and the
	// explicit lease correctly rejected the push.
	RewritePushRemoteChanged RewritePushErrorKind = "remote_changed"
)

// RewritePushError reports a safety refusal without exposing raw Git output.
// Err retains bounded command or classification detail for errors.Is/As.
type RewritePushError struct {
	Kind              RewritePushErrorKind
	Branch            string
	RemoteOnlyCommits int
	Err               error
}

func (e *RewritePushError) Error() string {
	switch e.Kind {
	case RewritePushRemoteDiverged:
		return fmt.Sprintf("remote branch %q contains %d commit(s) that cannot be safely replaced", e.Branch, e.RemoteOnlyCommits)
	case RewritePushRemoteChanged:
		return fmt.Sprintf("remote branch %q changed while the rewritten push was prepared", e.Branch)
	default:
		return fmt.Sprintf("rewritten push for remote branch %q was refused", e.Branch)
	}
}

func (e *RewritePushError) Unwrap() error {
	return e.Err
}

// PushRewrittenBranch replaces a remote branch only when any remote-only
// commits are provably redundant merges of history already present in HEAD.
func PushRewrittenBranch(worktreePath, branch string) error {
	return pushRewrittenBranch(worktreePath, branch, nil)
}

func pushRewrittenBranch(worktreePath, branch string, beforePush func()) error {
	inspectionRef, err := newRewriteInspectionRef()
	if err != nil {
		return fmt.Errorf("creating rewritten-push inspection ref: %w", err)
	}
	defer func() {
		_ = exec.Command("git", "-C", worktreePath, "update-ref", "-d", inspectionRef).Run()
	}()

	remoteRef := "refs/heads/" + branch
	if err := runRewritePushGit(worktreePath, "fetching remote branch for rewritten-push inspection",
		"fetch", "--no-tags", "origin", remoteRef+":"+inspectionRef); err != nil {
		return err
	}

	inspectedSHABytes, err := rewritePushGitOutput(worktreePath, "resolving rewritten-push inspection ref",
		"rev-parse", "--verify", inspectionRef+"^{commit}")
	if err != nil {
		return err
	}
	inspectedSHA := strings.TrimSpace(string(inspectedSHABytes))
	if inspectedSHA == "" {
		return errors.New("resolving rewritten-push inspection ref: empty commit SHA")
	}

	remoteIsAncestor, err := rewritePushIsAncestor(worktreePath, inspectedSHA, "HEAD")
	if err != nil {
		return fmt.Errorf("checking whether rewritten push is a fast-forward: %w", err)
	}
	if remoteIsAncestor {
		return Push(worktreePath, branch)
	}

	remoteOnlyOutput, err := rewritePushProofOutput(worktreePath, "enumerating remote-only commits",
		"rev-list", inspectionRef, "^HEAD")
	if err != nil {
		return err
	}
	remoteOnlyCommits := strings.Fields(string(remoteOnlyOutput))
	for _, commitSHA := range remoteOnlyCommits {
		if err := proveRemoteOnlyMergeIsRedundant(worktreePath, commitSHA); err != nil {
			return &RewritePushError{
				Kind:              RewritePushRemoteDiverged,
				Branch:            branch,
				RemoteOnlyCommits: len(remoteOnlyCommits),
				Err:               err,
			}
		}
	}

	if beforePush != nil {
		beforePush()
	}

	pushCmd := exec.Command("git", "-C", worktreePath,
		"push", "--force-with-lease="+remoteRef+":"+inspectedSHA,
		"-u", "origin", "HEAD:"+remoteRef)
	if err := pushCmd.Run(); err != nil {
		pushErr := fmt.Errorf("git push with explicit expected-SHA lease: %w", err)
		observedSHA, observeErr := rewritePushRemoteSHA(worktreePath, remoteRef)
		if observeErr != nil {
			return fmt.Errorf("pushing rewritten branch and checking remote state: %w", errors.Join(pushErr, observeErr))
		}
		if observedSHA != inspectedSHA {
			return &RewritePushError{
				Kind:              RewritePushRemoteChanged,
				Branch:            branch,
				RemoteOnlyCommits: len(remoteOnlyCommits),
				Err:               pushErr,
			}
		}
		return fmt.Errorf("pushing rewritten branch: %w", pushErr)
	}

	return nil
}

func newRewriteInspectionRef() (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	return "refs/agentico/publish-inspection/" + hex.EncodeToString(nonce[:]), nil
}

func proveRemoteOnlyMergeIsRedundant(worktreePath, commitSHA string) error {
	parentsOutput, err := rewritePushProofOutput(worktreePath, "reading remote-only commit parents",
		"rev-list", "--parents", "-n", "1", commitSHA)
	if err != nil {
		return err
	}
	parents := strings.Fields(string(parentsOutput))
	if len(parents) < 3 {
		return errors.New("remote-only commit is not a merge with at least two parents")
	}

	for _, parentSHA := range parents[1:] {
		isAncestor, err := rewritePushIsAncestor(worktreePath, parentSHA, "HEAD")
		if err != nil {
			return fmt.Errorf("checking remote-only merge parent: %w", err)
		}
		if !isAncestor {
			return errors.New("remote-only merge parent is not an ancestor of local HEAD")
		}
	}

	remergeDiff, err := rewritePushProofOutput(worktreePath, "checking remote-only merge resolution",
		"show", "--remerge-diff", "--format=", "--no-ext-diff", commitSHA)
	if err != nil {
		return err
	}
	if len(remergeDiff) != 0 {
		return errors.New("remote-only merge has a unique merge resolution")
	}
	return nil
}

func rewritePushIsAncestor(worktreePath, ancestor, descendant string) (bool, error) {
	cmd := rewritePushProofCommand(worktreePath, "merge-base", "--is-ancestor", ancestor, descendant)
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("git merge-base --is-ancestor: %w", err)
}

func rewritePushProofOutput(worktreePath, operation string, args ...string) ([]byte, error) {
	cmd := rewritePushProofCommand(worktreePath, args...)
	cmd.Stderr = io.Discard
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", operation, err)
	}
	return out, nil
}

// rewritePushProofCommand reads the repository's real object graph. Both
// replace refs and the legacy graft file are local, untrusted overlays that
// could otherwise make ordinary remote work appear to be a redundant merge.
func rewritePushProofCommand(worktreePath string, args ...string) *exec.Cmd {
	cmd := exec.Command("git", append([]string{"--no-replace-objects", "-C", worktreePath}, args...)...)
	cmd.Env = append(cmd.Environ(),
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_GRAFT_FILE="+os.DevNull,
	)
	return cmd
}

func rewritePushRemoteSHA(worktreePath, remoteRef string) (string, error) {
	out, err := rewritePushGitOutput(worktreePath, "checking remote branch after rejected lease",
		"ls-remote", "origin", remoteRef)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return "", nil
	}
	if len(fields) != 2 || fields[1] != remoteRef {
		return "", errors.New("checking remote branch after rejected lease: unexpected ls-remote response")
	}
	return fields[0], nil
}

func runRewritePushGit(worktreePath, operation string, args ...string) error {
	_, err := rewritePushGitOutput(worktreePath, operation, args...)
	return err
}

func rewritePushGitOutput(worktreePath, operation string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", worktreePath}, args...)...)
	// Prevent os/exec.Output from retaining raw stderr in *exec.ExitError.
	// Callers receive only the fixed operation label and the process failure.
	cmd.Stderr = io.Discard
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", operation, err)
	}
	return out, nil
}
