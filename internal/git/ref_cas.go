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
	"strings"
)

// RefCASMismatchError is returned when a compare-and-swap ref update fails
// because the ref's current SHA does not match the expected old SHA.
type RefCASMismatchError struct {
	Ref      string
	Expected string
	Observed string
}

func (e *RefCASMismatchError) Error() string {
	return fmt.Sprintf("ref %s: expected %s, observed %s", e.Ref, e.Expected, e.Observed)
}

// ReadRefSHA returns the full SHA of the named ref (e.g. "refs/heads/main"
// or "main") in the given repo path, or an error if the ref does not resolve.
func ReadRefSHA(repoPath, ref string) (string, error) {
	cmd := readGitCmd(repoPath, "rev-parse", "--verify", "--quiet", ref)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("rev-parse %s in %s: %w", ref, repoPath, err)
	}
	sha := strings.TrimSpace(string(out))
	if sha == "" {
		return "", fmt.Errorf("ref %s does not resolve in %s", ref, repoPath)
	}
	return sha, nil
}

// UpdateRefCAS performs a compare-and-swap ref update: the ref is updated
// to newSHA only if its current value equals oldSHA. If the current value
// differs, an *RefCASMismatchError is returned with the observed SHA. The
// update is atomic (git update-ref is atomic on the ref file).
//
// The ref should be a full ref path (e.g. "refs/heads/main") or a short name
// that git can resolve. The repoPath is the main repository path (not a
// worktree), since ref updates operate on the shared object database.
func UpdateRefCAS(repoPath, ref, oldSHA, newSHA string) error {
	// First read the current ref to detect CAS mismatch early and capture
	// the observed SHA for diagnostics.
	current, err := ReadRefSHA(repoPath, ref)
	if err != nil {
		return fmt.Errorf("reading ref %s before CAS update: %w", ref, err)
	}
	if current != oldSHA {
		return &RefCASMismatchError{Ref: ref, Expected: oldSHA, Observed: current}
	}
	// Use git update-ref with the old value for atomicity: if another
	// process moves the ref between our read and the update, git itself
	// will reject the update.
	stdin := fmt.Sprintf("update %s %s %s\n", ref, newSHA, oldSHA)
	out, err := runGitMutationWithLockRetryStdin(repoPath, stdin, "update-ref", "--stdin")
	if err != nil {
		// Re-read to capture the observed SHA for diagnostics.
		observed, _ := ReadRefSHA(repoPath, ref)
		if observed != oldSHA {
			return &RefCASMismatchError{Ref: ref, Expected: oldSHA, Observed: observed}
		}
		return fmt.Errorf("update-ref %s: %s: %w", ref, strings.TrimSpace(string(out)), err)
	}
	return nil
}

// RefUpdate is one compare-and-swap ref update inside a transaction: the ref
// moves to NewSHA only if it currently sits at OldSHA.
type RefUpdate struct {
	Ref    string
	OldSHA string
	NewSHA string
}

// UpdateRefsTransaction atomically updates several refs of one repository:
// every update is applied in a single `update-ref --stdin` batch
// (start, one update per ref, prepare, commit) through the lock-retrying
// runner, so either every ref moves or none does. A branch checked out in a
// linked worktree is updated like any other ref; the helper never touches
// worktrees. An empty update list is a successful no-op.
//
// On a compare-and-swap mismatch the returned error is a
// *RefCASMismatchError naming the ref that was observed at which SHA; refs
// are re-read after a failed batch for diagnostics, like the single-ref
// helper.
func UpdateRefsTransaction(repoPath string, updates []RefUpdate) error {
	if len(updates) == 0 {
		return nil
	}
	// Pre-read every ref so a mismatch is detected before any ref moves and
	// the observed SHA is captured for diagnostics.
	for _, u := range updates {
		current, err := ReadRefSHA(repoPath, u.Ref)
		if err != nil {
			return fmt.Errorf("reading ref %s before transaction: %w", u.Ref, err)
		}
		if current != u.OldSHA {
			return &RefCASMismatchError{Ref: u.Ref, Expected: u.OldSHA, Observed: current}
		}
	}
	var stdin strings.Builder
	stdin.WriteString("start\n")
	for _, u := range updates {
		fmt.Fprintf(&stdin, "update %s %s %s\n", u.Ref, u.NewSHA, u.OldSHA)
	}
	stdin.WriteString("prepare\ncommit\n")
	out, err := runGitMutationWithLockRetryStdin(repoPath, stdin.String(), "update-ref", "--stdin")
	if err != nil {
		// The transaction protocol leaves no ref moved on failure, but a ref
		// may have moved underneath the batch; re-read for diagnostics.
		for _, u := range updates {
			observed, _ := ReadRefSHA(repoPath, u.Ref)
			if observed != u.OldSHA {
				return &RefCASMismatchError{Ref: u.Ref, Expected: u.OldSHA, Observed: observed}
			}
		}
		return fmt.Errorf("update-ref transaction: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// RefSHA returns the full SHA of the named ref in the given repo path.
func (m *WorktreeManager) RefSHA(repoPath, ref string) (string, error) {
	return ReadRefSHA(repoPath, ref)
}

// UpdateRef performs a compare-and-swap ref update on the given repo.
func (m *WorktreeManager) UpdateRef(repoPath, ref, oldSHA, newSHA string) error {
	return UpdateRefCAS(repoPath, ref, oldSHA, newSHA)
}

// UpdateRefsTransaction performs an atomic multi-ref compare-and-swap update
// on the given repo.
func (m *WorktreeManager) UpdateRefsTransaction(repoPath string, updates []RefUpdate) error {
	return UpdateRefsTransaction(repoPath, updates)
}
