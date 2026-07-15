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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitRepositoryCreatesGitRepo(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "new-repo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if err := InitRepository(dir); err != nil {
		t.Fatalf("InitRepository() error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		t.Fatalf("expected .git after InitRepository: %v", err)
	}
}

func TestInitRepositoryCreatesResolvableHead(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "new-repo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if err := InitRepository(dir); err != nil {
		t.Fatalf("InitRepository() error = %v", err)
	}

	out, err := gitCmd(dir, "rev-parse", "--verify", "HEAD").CombinedOutput()
	if err != nil {
		t.Fatalf("HEAD should resolve after InitRepository: %v: %s",
			err, strings.TrimSpace(string(out)))
	}
	if status := gitOutput(t, dir, "status", "--porcelain"); status != "" {
		t.Fatalf("worktree should be clean after InitRepository; status:\n%s", status)
	}
}

func TestInitRepositoryFailsOnMissingDirectory(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "does-not-exist")
	if err := InitRepository(dir); err == nil {
		t.Fatal("InitRepository() error = nil; want failure for missing directory")
	}
}

func TestInitRepositoryRemovesIncompleteRepositoryAfterCommitFailure(t *testing.T) {
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("look up git: %v", err)
	}

	dir := filepath.Join(t.TempDir(), "new-repo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	binDir := t.TempDir()
	markerPath := filepath.Join(binDir, "commit'failed")
	gitWrapper := filepath.Join(binDir, "git")
	wrapper := "#!/bin/sh\n" +
		"for arg in \"$@\"; do\n" +
		"  if [ \"$arg\" = \"commit\" ] && [ ! -e " + shellQuote(markerPath) + " ]; then\n" +
		"    touch " + shellQuote(markerPath) + "\n" +
		"    exit 1\n" +
		"  fi\n" +
		"done\n" +
		"exec " + shellQuote(realGit) + " \"$@\"\n"
	if err := os.WriteFile(gitWrapper, []byte(wrapper), 0o755); err != nil {
		t.Fatalf("write git wrapper: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if err := InitRepository(dir); err == nil {
		t.Fatal("InitRepository() error = nil; want commit failure")
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); !os.IsNotExist(err) {
		t.Fatalf(".git after failed InitRepository = %v; want removed", err)
	}

	if err := InitRepository(dir); err != nil {
		t.Fatalf("InitRepository() retry error = %v", err)
	}
	if out, err := gitCmd(dir, "rev-parse", "--verify", "HEAD").CombinedOutput(); err != nil {
		t.Fatalf("HEAD should resolve after retry: %v: %s", err, strings.TrimSpace(string(out)))
	}
}

func TestInitRepositoryIgnoresConfiguredHooks(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "new-repo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	hooksPath := t.TempDir()
	hookPath := filepath.Join(hooksPath, "pre-commit")
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write failing hook: %v", err)
	}
	globalConfigPath := filepath.Join(t.TempDir(), "gitconfig")
	config := "[core]\n\thooksPath = " + hooksPath + "\n"
	if err := os.WriteFile(globalConfigPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write global git config: %v", err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", globalConfigPath)

	if err := InitRepository(dir); err != nil {
		t.Fatalf("InitRepository() error with configured hook = %v", err)
	}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
