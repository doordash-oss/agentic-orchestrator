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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// createCommandTimeout bounds each git invocation of a repository creation.
// Creation is two fast local commands; the bound exists so a wedged git can
// never hold an API request or a worker indefinitely.
const createCommandTimeout = 60 * time.Second

// CreateRepository builds a fresh repository inside dir: exactly one empty
// initial commit on the main branch, authored and committed with Agentico's
// explicit identity, with no remote and no push. The caller owns all path
// validation and isolation (dir is staged, hidden and ownership-marked
// before this runs); this adapter only performs the bounded git operations.
//
// Every setting that could change the promised result is explicit on the
// command line: an empty template overrides a global init.templateDir,
// --initial-branch pins main over a global init.defaultBranch, and the
// commit's identity, signing and hooks are forced so no inherited global
// configuration can alter the author, add a signature, run hooks or change
// the message. The result is verified (branch and HEAD) before success is
// reported, so hostile configuration fails loudly instead of publishing a
// different repository.
func CreateRepository(ctx context.Context, dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("repository directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("repository path %q is not a directory", dir)
	}
	if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
		return fmt.Errorf("repository path %q already contains git metadata", dir)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect git metadata: %w", err)
	}

	// The template lives outside the repository directory so the copy step
	// can never import its own contents; it is empty by construction.
	templatePath, err := os.MkdirTemp("", "agentico-create-template-")
	if err != nil {
		return fmt.Errorf("create git template directory: %w", err)
	}
	defer os.RemoveAll(templatePath)

	ctx, cancel := context.WithTimeout(ctx, createCommandTimeout)
	defer cancel()

	if out, err := runCreateGit(ctx, dir, "init", "--template="+templatePath, "--initial-branch=main"); err != nil {
		return fmt.Errorf("git init: %w: %s", err, strings.TrimSpace(out))
	}
	// The identity, signing, hooks and template are explicit so the synthetic
	// commit is unaffected by a user's global Git configuration.
	if out, err := runCreateGit(ctx, dir,
		"-c", "user.name="+AgenticoName,
		"-c", "user.email="+agenticoCommitIdentity(),
		"-c", "commit.gpgsign=false",
		"-c", "core.hooksPath=/dev/null",
		"commit", "--allow-empty", "-m", "Initial commit"); err != nil {
		return fmt.Errorf("git commit: %w: %s", err, strings.TrimSpace(out))
	}
	// Verify the promised shape before the caller publishes anything: a
	// hostile template or configuration that changed the branch or left HEAD
	// unborn turns into a loud failure here.
	branchOut, err := runCreateGit(ctx, dir, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return fmt.Errorf("verify HEAD: %w: %s", err, strings.TrimSpace(branchOut))
	}
	if strings.TrimSpace(branchOut) != "main" {
		return fmt.Errorf("initial branch is %q, not main", strings.TrimSpace(branchOut))
	}
	remotesOut, err := runCreateGit(ctx, dir, "remote")
	if err != nil {
		return fmt.Errorf("verify remotes: %w: %s", err, strings.TrimSpace(remotesOut))
	}
	if strings.TrimSpace(remotesOut) != "" {
		return fmt.Errorf("created repository unexpectedly has remotes")
	}
	return nil
}

// runCreateGit runs one bounded git invocation inside dir. The process is
// its own group and is killed as a group on context cancellation, so a
// wedged git never outlives its bound.
func runCreateGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = createEnvironment()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return context.Canceled
	}
	cmd.WaitDelay = time.Second
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// createEnvironment keeps the server's environment but forces a
// non-interactive git and drops request-scoped git dir overrides that must
// never redirect a creation into another worktree.
func createEnvironment() []string {
	env := os.Environ()
	env = append(env, "GIT_TERMINAL_PROMPT=0")
	drop := map[string]bool{
		"GIT_DIR":                          true,
		"GIT_WORK_TREE":                    true,
		"GIT_INDEX_FILE":                   true,
		"GIT_OBJECT_DIRECTORY":             true,
		"GIT_ALTERNATE_OBJECT_DIRECTORIES": true,
	}
	filtered := make([]string, 0, len(env))
	for _, entry := range env {
		name, _, _ := strings.Cut(entry, "=")
		if drop[name] {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

// agenticoCommitIdentity is the committer/author email for server-created
// synthetic commits. It deliberately matches the existing initialization
// identity so every Agentico-created repository shares one identity story.
func agenticoCommitIdentity() string {
	return "agentico@localhost"
}
