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

// MergeBaseSHA resolves the merge base of a and b in the repository at
// repoPath. It returns an error when either argument is empty or git cannot
// resolve a merge base (unrelated histories), so callers treat an
// unresolvable base as a hard failure rather than silently substituting one
// side.
func MergeBaseSHA(repoPath, a, b string) (string, error) {
	if a == "" || b == "" {
		return "", fmt.Errorf("merge-base requires two commits (got %q, %q)", a, b)
	}
	cmd := readGitCmd(repoPath, "merge-base", a, b)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("merge-base %s %s in %s: %w", a, b, repoPath, err)
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 || fields[0] == "" {
		return "", fmt.Errorf("merge-base %s %s in %s resolved no commit", a, b, repoPath)
	}
	return fields[0], nil
}

// IsAncestor reports whether ancestor is an ancestor of descendant in the
// repository at repoPath. It shells out to
// `git merge-base --is-ancestor <ancestor> <descendant>`, which exits 0 when
// the relationship holds and non-zero otherwise.
//
// The primitive is conservative: it returns false on any git error, unknown
// commit, or when either argument is empty. Callers that need to distinguish
// "definitely not an ancestor" from "git could not answer" should run their
// own command; this boolean is for safety gates where a false result simply
// withholds a positive assertion.
func IsAncestor(repoPath, ancestor, descendant string) bool {
	if ancestor == "" || descendant == "" {
		return false
	}
	cmd := readGitCmd(repoPath, "merge-base", "--is-ancestor", ancestor, descendant)
	if err := cmd.Run(); err != nil {
		return false
	}
	return true
}

// HasCommitsBeyond reports whether tipSHA carries commits past cutPointSHA.
// It is false when the tip is an ancestor of the cut point — including a tip
// equal to the cut point — and when either SHA is empty, because an unnamed
// tip has nothing to deliver. A git failure reports true: IsAncestor answers
// false when it cannot decide, and an indeterminate range must not let a
// layer be silently skipped as empty.
func HasCommitsBeyond(repoPath, tipSHA, cutPointSHA string) bool {
	if tipSHA == "" || cutPointSHA == "" {
		return false
	}
	return !IsAncestor(repoPath, tipSHA, cutPointSHA)
}

// CheckAncestor is IsAncestor for callers that must distinguish "not an
// ancestor" (exit status 1) from git being unable to answer, such as an
// unknown commit or an unreadable repository.
func CheckAncestor(repoPath, ancestor, descendant string) (bool, error) {
	if ancestor == "" || descendant == "" {
		return false, errors.New("merge-base --is-ancestor: empty commit")
	}
	cmd := readGitCmd(repoPath, "merge-base", "--is-ancestor", ancestor, descendant)
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

// IsAncestor reports whether ancestor reaches descendant in the given repo,
// failing rather than guessing when git cannot answer.
func (m *WorktreeManager) IsAncestor(repoPath, ancestor, descendant string) (bool, error) {
	return CheckAncestor(repoPath, ancestor, descendant)
}
