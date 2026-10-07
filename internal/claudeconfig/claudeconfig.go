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

// Package claudeconfig resolves the Claude CLI configuration directory and
// the per-workspace projects directory the CLI stores session transcripts
// in. It is the single shared encoder of that layout and imports nothing
// from the rest of the repository.
package claudeconfig

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// EnvConfigDir is the environment variable the Claude CLI reads to relocate
// its configuration directory.
const EnvConfigDir = "CLAUDE_CONFIG_DIR"

// Dir resolves the Claude configuration directory: the value of
// CLAUDE_CONFIG_DIR when set and non-empty, else <home>/.claude. getenv and
// home are injected so callers and tests control the environment.
func Dir(getenv func(string) string, home func() (string, error)) (string, error) {
	if getenv != nil {
		if dir := getenv(EnvConfigDir); dir != "" {
			return dir, nil
		}
	}
	if home == nil {
		return "", errors.New("claude config dir: no home directory resolver")
	}
	h, err := home()
	if err != nil {
		return "", err
	}
	if h == "" {
		return "", errors.New("claude config dir: empty home directory")
	}
	return filepath.Join(h, ".claude"), nil
}

// DefaultDir resolves the Claude configuration directory from the process
// environment.
func DefaultDir() (string, error) {
	return Dir(os.Getenv, os.UserHomeDir)
}

var projectDirReplacer = strings.NewReplacer("/", "-", ".", "-")

// ProjectDirName encodes a working directory into the directory name the
// Claude CLI uses under <config>/projects/: path separators and dots become
// hyphens.
func ProjectDirName(workDir string) string {
	return projectDirReplacer.Replace(workDir)
}

// ResolveWorkDir returns workDir with symlinks resolved, falling back to
// workDir when it cannot be resolved. The Claude CLI names its project
// directory from the physical working directory (/tmp is /private/tmp on
// macOS), so every path derived from a working directory goes through here.
func ResolveWorkDir(workDir string) string {
	if resolved, err := filepath.EvalSymlinks(workDir); err == nil {
		return resolved
	}
	return workDir
}

// ProjectsDir returns <configDir>/projects/<ProjectDirName(workDir)>, the
// directory holding the session JSONL files for workDir, with workDir's
// symlinks resolved.
func ProjectsDir(configDir, workDir string) string {
	return filepath.Join(configDir, "projects", ProjectDirName(ResolveWorkDir(workDir)))
}
