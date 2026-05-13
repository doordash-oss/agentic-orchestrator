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
	"os/exec"
	"strings"
	"time"
)

// BranchName returns the full branch name for a feature.
func BranchName(featureSlug string) string {
	return "feature/" + featureSlug
}

// BranchExistsOnRemote checks whether a branch exists on the origin remote.
// Returns false if the remote is unreachable or not configured.
func BranchExistsOnRemote(repoPath, branch string) bool {
	cmd := exec.Command("git", "-C", repoPath, "ls-remote", "--heads", "origin", "refs/heads/"+branch)
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return len(strings.TrimSpace(string(out))) > 0
}

// HasOriginRemote returns true if the repo at repoPath has an "origin" remote configured.
func HasOriginRemote(repoPath string) bool {
	cmd := exec.Command("git", "-C", repoPath, "remote")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(line) == "origin" {
			return true
		}
	}
	return false
}

// CreateBackupBranch creates a backup branch at the current HEAD in the given worktree.
// Returns the branch name. Format: feature/<slug>-pre-rewind-<unix_timestamp>
func CreateBackupBranch(worktreePath, slug string) (string, error) {
	branchName := fmt.Sprintf("feature/%s-pre-rewind-%d", slug, time.Now().Unix())
	cmd := exec.Command("git", "branch", branchName)
	cmd.Dir = worktreePath
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("creating backup branch: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return branchName, nil
}
