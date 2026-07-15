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
	"strings"
)

// InitRepository initializes a new git repository in the existing directory
// at path and creates an initial empty commit so HEAD resolves immediately
// (worktree setup requires a born HEAD). The caller owns all path validation
// (containment, emptiness, symlink resolution) — this adapter only performs
// the git operations.
func InitRepository(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("repository directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("repository path %q is not a directory", path)
	}
	cmd := exec.Command("git", "-C", path, "init")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git init: %w: %s", err, strings.TrimSpace(string(out)))
	}
	// The identity is passed explicitly and signing is disabled so the commit
	// never depends on (or is broken by) the user's global git configuration.
	commit := exec.Command("git", "-C", path,
		"-c", "user.name=Agentico",
		"-c", "user.email=agentico@localhost",
		"-c", "commit.gpgsign=false",
		"commit", "--allow-empty", "-m", "Initial commit")
	if out, err := commit.CombinedOutput(); err != nil {
		return fmt.Errorf("git commit: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
