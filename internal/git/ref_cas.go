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
	"errors"
	"fmt"
	"os/exec"
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

// refAbsentObservation is the readable representation of "the ref does not
// exist" in RefCASMismatchError diagnostics, for both the expected and the
// observed side of a mismatch.
const refAbsentObservation = "absent"

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

// ReadRefSHAOrAbsent reads the named ref like ReadRefSHA but distinguishes a
// ref that does not exist from a read that failed: a missing ref returns an
// empty SHA with absent=true and no error, while only a genuine git failure
// is an error. Exit code 1 from rev-parse --verify --quiet proves the ref
// does not resolve; any other failure fails closed.
func ReadRefSHAOrAbsent(repoPath, ref string) (sha string, absent bool, err error) {
	cmd := readGitCmd(repoPath, "rev-parse", "--verify", "--quiet", ref)
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return "", true, nil
		}
		return "", false, fmt.Errorf("rev-parse %s in %s: %w", ref, repoPath, err)
	}
	sha = strings.TrimSpace(string(out))
	if sha == "" {
		return "", false, fmt.Errorf("ref %s does not resolve in %s", ref, repoPath)
	}
	return sha, false, nil
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

// RefUpdate is one compare-and-swap ref update inside a transaction. The
// old/new SHA pair selects the operation: an empty OldSHA means the ref must
// be absent and is created at NewSHA; an empty NewSHA means the ref is
// deleted, expecting it to sit at OldSHA; equal non-empty SHAs verify the ref
// is at that SHA without moving it; any other non-empty pair moves the ref
// from OldSHA to NewSHA.
type RefUpdate struct {
	Ref    string
	OldSHA string
	NewSHA string
}

// refUpdateKind is the update-ref --stdin command a RefUpdate translates to.
type refUpdateKind int

const (
	refUpdateCreate refUpdateKind = iota
	refUpdateDelete
	refUpdateVerify
	refUpdateMove
)

// classifyRefUpdate maps a RefUpdate's old/new SHA pair onto its transaction
// command. An update with neither SHA is degenerate — neither a create nor a
// delete — and is refused before git runs.
func classifyRefUpdate(u RefUpdate) (refUpdateKind, error) {
	switch {
	case u.OldSHA == "" && u.NewSHA == "":
		return 0, fmt.Errorf("ref %s: update has neither an old SHA nor a new SHA", u.Ref)
	case u.OldSHA == "":
		return refUpdateCreate, nil
	case u.NewSHA == "":
		return refUpdateDelete, nil
	case u.OldSHA == u.NewSHA:
		return refUpdateVerify, nil
	default:
		return refUpdateMove, nil
	}
}

// UpdateRefsTransaction atomically applies several ref operations to one
// repository: every operation is applied in a single `update-ref --stdin`
// batch (start, one command per ref, prepare, commit) through the
// lock-retrying runner, so either every ref moves or none does. Alongside
// plain compare-and-swap moves, a RefUpdate with an empty OldSHA creates a
// ref that must be absent, an empty NewSHA deletes a ref that must still be
// at OldSHA, and equal old and new SHAs verify a ref without moving it. A
// branch checked out in a linked worktree is updated like any other ref; the
// helper never touches worktrees. An empty update list is a successful no-op.
//
// On a compare-and-swap mismatch the returned error is a
// *RefCASMismatchError naming the ref that was observed at which SHA; refs
// are re-read after a failed batch for diagnostics, like the single-ref
// helper.
func UpdateRefsTransaction(repoPath string, updates []RefUpdate) error {
	if len(updates) == 0 {
		return nil
	}
	kinds := make([]refUpdateKind, len(updates))
	// Pre-read every ref so a mismatch is detected before any ref moves and
	// the observed SHA is captured for diagnostics. A create expects the ref
	// absent; every other kind expects it present at OldSHA.
	for i, u := range updates {
		kind, err := classifyRefUpdate(u)
		if err != nil {
			return err
		}
		kinds[i] = kind
		current, absent, err := ReadRefSHAOrAbsent(repoPath, u.Ref)
		if err != nil {
			return fmt.Errorf("reading ref %s before transaction: %w", u.Ref, err)
		}
		if kind == refUpdateCreate {
			if !absent {
				return &RefCASMismatchError{Ref: u.Ref, Expected: refAbsentObservation, Observed: current}
			}
			continue
		}
		if absent {
			return &RefCASMismatchError{Ref: u.Ref, Expected: u.OldSHA, Observed: refAbsentObservation}
		}
		if current != u.OldSHA {
			return &RefCASMismatchError{Ref: u.Ref, Expected: u.OldSHA, Observed: current}
		}
	}
	var stdin strings.Builder
	stdin.WriteString("start\n")
	for i, u := range updates {
		switch kinds[i] {
		case refUpdateCreate:
			fmt.Fprintf(&stdin, "create %s %s\n", u.Ref, u.NewSHA)
		case refUpdateDelete:
			fmt.Fprintf(&stdin, "delete %s %s\n", u.Ref, u.OldSHA)
		case refUpdateVerify:
			fmt.Fprintf(&stdin, "verify %s %s\n", u.Ref, u.OldSHA)
		default:
			fmt.Fprintf(&stdin, "update %s %s %s\n", u.Ref, u.NewSHA, u.OldSHA)
		}
	}
	stdin.WriteString("prepare\ncommit\n")
	out, err := runGitMutationWithLockRetryStdin(repoPath, stdin.String(), "update-ref", "--stdin")
	if err != nil {
		// The transaction protocol leaves no ref moved on failure, but a ref
		// may have moved underneath the batch; re-read for diagnostics. A
		// create line whose ref is still absent is the expected, unchanged
		// state and is never reported as a mismatch.
		for i, u := range updates {
			observed, absent, _ := ReadRefSHAOrAbsent(repoPath, u.Ref)
			if kinds[i] == refUpdateCreate {
				if !absent {
					return &RefCASMismatchError{Ref: u.Ref, Expected: refAbsentObservation, Observed: observed}
				}
				continue
			}
			if absent {
				return &RefCASMismatchError{Ref: u.Ref, Expected: u.OldSHA, Observed: refAbsentObservation}
			}
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

// RefSHAOrAbsent reads the named ref like RefSHA but reports a missing ref
// as absent=true with an empty SHA and no error, so callers can distinguish
// "does not exist yet" from a failed read.
func (m *WorktreeManager) RefSHAOrAbsent(repoPath, ref string) (string, bool, error) {
	return ReadRefSHAOrAbsent(repoPath, ref)
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
