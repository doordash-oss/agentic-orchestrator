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
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
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
	if _, err := os.Lstat(filepath.Join(path, ".git")); err == nil {
		return fmt.Errorf("repository path %q already contains git metadata", path)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("inspect git metadata: %w", err)
	}

	stagingPath, err := os.MkdirTemp(filepath.Dir(path), ".agentico-git-init-")
	if err != nil {
		return fmt.Errorf("create git staging directory: %w", err)
	}
	defer os.RemoveAll(stagingPath)

	templatePath := filepath.Join(stagingPath, "template")
	if err := os.Mkdir(templatePath, 0o755); err != nil {
		return fmt.Errorf("create git template directory: %w", err)
	}
	cmd := exec.Command("git", "-C", stagingPath, "init", "--template="+templatePath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git init: %w: %s", err, strings.TrimSpace(string(out)))
	}
	// The identity, signing, hooks, and template are explicit so the synthetic
	// commit is unaffected by a user's global Git configuration.
	commit := exec.Command("git", "-C", stagingPath,
		"-c", "user.name=Agentico",
		"-c", "user.email=agentico@localhost",
		"-c", "commit.gpgsign=false",
		"-c", "core.hooksPath=/dev/null",
		"commit", "--allow-empty", "-m", "Initial commit")
	if out, err := commit.CombinedOutput(); err != nil {
		return fmt.Errorf("git commit: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if err := os.Rename(filepath.Join(stagingPath, ".git"), filepath.Join(path, ".git")); err != nil {
		return fmt.Errorf("publish initialized repository: %w", err)
	}
	return nil
}
