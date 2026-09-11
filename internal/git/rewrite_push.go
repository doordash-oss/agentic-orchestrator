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

// PushLayerBranch delivers a layer branch that is not necessarily checked out
// in the repository at repoPath. It pushes the named local ref — never HEAD —
// because worktrees share refs with the main repository, so a worktree sitting
// on a higher layer branch can still publish a lower layer. localSHA is the
// commit the caller wants on the remote; lastPushedSHA is the last SHA
// Agentico itself pushed for the branch, empty when it was never pushed.
//
// Delivery decision order: an absent remote branch is created with a plain
// push; a remote tip that is an ancestor of localSHA (including an equal tip)
// is plain-pushed; a remote tip equal to lastPushedSHA is force-with-lease
// pushed pinned to that SHA — the remote holds exactly what Agentico pushed,
// so no further proof is required; anything else must pass the
// redundant-merge proof over the remote-only commits before a lease push
// pinned to the inspected SHA. A failed proof reports
// RewritePushRemoteDiverged and a rejected lease RewritePushRemoteChanged.
// The returned SHA is the commit now sitting on the remote.
func PushLayerBranch(repoPath, branch, localSHA, lastPushedSHA string) (string, error) {
	return pushLayerBranch(repoPath, branch, localSHA, lastPushedSHA, nil)
}

func pushLayerBranch(repoPath, branch, localSHA, lastPushedSHA string, beforePush func()) (string, error) {
	if strings.TrimSpace(localSHA) == "" {
		return "", fmt.Errorf("layer push for branch %q requires the local SHA to deliver", branch)
	}
	remoteRef := "refs/heads/" + branch
	_, remoteExists, err := rewritePushRemoteBranchState(repoPath, remoteRef)
	if err != nil {
		return "", err
	}
	if !remoteExists {
		if beforePush != nil {
			beforePush()
		}
		pushErr := Push(repoPath, branch)
		if pushErr == nil {
			return rewritePushDeliveredSHA(repoPath, remoteRef)
		}
		// Distinguish a concurrent creation from an ordinary push failure. The
		// probe is diagnostic only: no result retries or force-pushes.
		_, appeared, probeErr := rewritePushRemoteBranchState(repoPath, remoteRef)
		if probeErr != nil {
			return "", fmt.Errorf("creating remote branch and checking rejected push: %w", errors.Join(pushErr, probeErr))
		}
		if appeared {
			return "", &RewritePushError{
				Kind:   RewritePushRemoteChanged,
				Branch: branch,
				Err:    sanitizeOrdinaryPushError(pushErr),
			}
		}
		return "", pushErr
	}

	inspectionRef, err := newRewriteInspectionRef()
	if err != nil {
		return "", fmt.Errorf("creating rewritten-push inspection ref: %w", err)
	}
	defer func() {
		_ = exec.Command("git", "-C", repoPath, "update-ref", "-d", inspectionRef).Run()
	}()

	if err := runRewritePushGit(repoPath, "fetching remote branch for rewritten-push inspection",
		"fetch", "--no-tags", "origin", remoteRef+":"+inspectionRef); err != nil {
		return "", err
	}

	inspectedSHABytes, err := rewritePushGitOutput(repoPath, "resolving rewritten-push inspection ref",
		"rev-parse", "--verify", inspectionRef+"^{commit}")
	if err != nil {
		return "", err
	}
	inspectedSHA := strings.TrimSpace(string(inspectedSHABytes))
	if inspectedSHA == "" {
		return "", errors.New("resolving rewritten-push inspection ref: empty commit SHA")
	}

	remoteIsAncestor, err := rewritePushIsAncestor(repoPath, inspectedSHA, localSHA)
	if err != nil {
		return "", fmt.Errorf("checking whether rewritten push is a fast-forward: %w", err)
	}
	if remoteIsAncestor {
		if err := Push(repoPath, branch); err != nil {
			return "", err
		}
		return rewritePushDeliveredSHA(repoPath, remoteRef)
	}

	if lastPushedSHA != "" && inspectedSHA == lastPushedSHA {
		// The remote holds exactly what Agentico pushed, so a lease pinned to
		// that SHA proves ownership without any further redundancy proof.
		if beforePush != nil {
			beforePush()
		}
		return leasePushLayerBranch(repoPath, branch, remoteRef, inspectedSHA, 0)
	}

	remoteOnlyOutput, err := rewritePushProofOutput(repoPath, "enumerating remote-only commits",
		"rev-list", inspectionRef, "^"+localSHA)
	if err != nil {
		return "", err
	}
	remoteOnlyCommits := strings.Fields(string(remoteOnlyOutput))
	for _, commitSHA := range remoteOnlyCommits {
		if err := proveRemoteOnlyMergeIsRedundant(repoPath, commitSHA, localSHA); err != nil {
			return "", &RewritePushError{
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
	return leasePushLayerBranch(repoPath, branch, remoteRef, inspectedSHA, len(remoteOnlyCommits))
}

// leasePushLayerBranch force-pushes the named local ref with an explicit
// expected-SHA lease. A rejection is classified against the observed remote
// tip so a concurrent move reports RewritePushRemoteChanged.
func leasePushLayerBranch(repoPath, branch, remoteRef, expectedSHA string, remoteOnlyCommits int) (string, error) {
	pushCmd := exec.Command("git", "-C", repoPath,
		"push", "--force-with-lease="+remoteRef+":"+expectedSHA,
		"-u", "origin", remoteRef+":"+remoteRef)
	if err := pushCmd.Run(); err != nil {
		pushErr := fmt.Errorf("git push with explicit expected-SHA lease: %w", err)
		observedSHA, observeErr := rewritePushRemoteSHA(repoPath, remoteRef)
		if observeErr != nil {
			return "", fmt.Errorf("pushing layer branch and checking remote state: %w", errors.Join(pushErr, observeErr))
		}
		if observedSHA != expectedSHA {
			return "", &RewritePushError{
				Kind:              RewritePushRemoteChanged,
				Branch:            branch,
				RemoteOnlyCommits: remoteOnlyCommits,
				Err:               pushErr,
			}
		}
		return "", fmt.Errorf("pushing layer branch: %w", pushErr)
	}
	return rewritePushDeliveredSHA(repoPath, remoteRef)
}

// rewritePushDeliveredSHA resolves the local ref that was just pushed. A
// successful push of refs/heads/<branch> leaves the remote holding exactly
// this commit, so it is the SHA that now sits on the remote.
func rewritePushDeliveredSHA(repoPath, ref string) (string, error) {
	out, err := rewritePushGitOutput(repoPath, "resolving pushed local ref",
		"rev-parse", "--verify", ref)
	if err != nil {
		return "", err
	}
	sha := strings.TrimSpace(string(out))
	if sha == "" {
		return "", errors.New("resolving pushed local ref: empty commit SHA")
	}
	return sha, nil
}

func sanitizeOrdinaryPushError(err error) error {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return fmt.Errorf("git push creating remote branch: %w", exitErr)
	}
	return errors.New("git push creating remote branch failed")
}

func newRewriteInspectionRef() (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	return "refs/agentico/publish-inspection/" + hex.EncodeToString(nonce[:]), nil
}

func proveRemoteOnlyMergeIsRedundant(repoPath, commitSHA, localSHA string) error {
	parentsOutput, err := rewritePushProofOutput(repoPath, "reading remote-only commit parents",
		"rev-list", "--parents", "-n", "1", commitSHA)
	if err != nil {
		return err
	}
	parents := strings.Fields(string(parentsOutput))
	if len(parents) < 3 {
		return errors.New("remote-only commit is not a merge with at least two parents")
	}

	for _, parentSHA := range parents[1:] {
		isAncestor, err := rewritePushIsAncestor(repoPath, parentSHA, localSHA)
		if err != nil {
			return fmt.Errorf("checking remote-only merge parent: %w", err)
		}
		if !isAncestor {
			return errors.New("remote-only merge parent is not an ancestor of the local layer tip")
		}
	}

	remergeDiff, err := rewritePushProofOutput(repoPath, "checking remote-only merge resolution",
		"show", "--remerge-diff", "--format=", "--no-ext-diff", commitSHA)
	if err != nil {
		return err
	}
	if len(remergeDiff) != 0 {
		return errors.New("remote-only merge has a unique merge resolution")
	}
	return nil
}

func rewritePushIsAncestor(repoPath, ancestor, descendant string) (bool, error) {
	cmd := rewritePushProofCommand(repoPath, "merge-base", "--is-ancestor", ancestor, descendant)
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

func rewritePushProofOutput(repoPath, operation string, args ...string) ([]byte, error) {
	cmd := rewritePushProofCommand(repoPath, args...)
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
func rewritePushProofCommand(repoPath string, args ...string) *exec.Cmd {
	cmd := exec.Command("git", append([]string{"--no-replace-objects", "-C", repoPath}, args...)...)
	cmd.Env = append(cmd.Environ(),
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_GRAFT_FILE="+os.DevNull,
	)
	return cmd
}

func rewritePushRemoteSHA(repoPath, remoteRef string) (string, error) {
	sha, _, err := rewritePushRemoteBranchState(repoPath, remoteRef)
	return sha, err
}

// rewritePushRemoteBranchState uses ls-remote's documented exit code 2 for an
// exact-ref miss. Every other command failure is operational, never absence.
func rewritePushRemoteBranchState(repoPath, remoteRef string) (string, bool, error) {
	cmd := exec.Command("git", "-C", repoPath, "ls-remote", "--exit-code", "origin", remoteRef)
	cmd.Stderr = io.Discard
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 2 {
			return "", false, nil
		}
		return "", false, fmt.Errorf("checking exact remote branch: %w", err)
	}
	fields := strings.Fields(string(out))
	if len(fields) != 2 || fields[1] != remoteRef {
		return "", false, errors.New("checking exact remote branch: unexpected ls-remote response")
	}
	return fields[0], true, nil
}

func runRewritePushGit(repoPath, operation string, args ...string) error {
	_, err := rewritePushGitOutput(repoPath, operation, args...)
	return err
}

func rewritePushGitOutput(repoPath, operation string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", repoPath}, args...)...)
	// Prevent os/exec.Output from retaining raw stderr in *exec.ExitError.
	// Callers receive only the fixed operation label and the process failure.
	cmd.Stderr = io.Discard
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", operation, err)
	}
	return out, nil
}
