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

// Package clirun holds small helpers for running CLI subprocesses and
// parsing their version strings. Extracted from the removed `discovery`
// package so provider code still has a home for CLI-version checks after
// model discovery was retired in favor of hardcoded catalogs.
package clirun

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// CommandRunner executes a command and returns its stdout output.
type CommandRunner func(ctx context.Context, name string, args []string, env []string) ([]byte, error)

// DefaultRunner returns a CommandRunner that captures only stdout.
// Stderr is discarded to prevent CLI warnings from corrupting structured output.
func DefaultRunner() CommandRunner {
	return func(ctx context.Context, name string, args []string, env []string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, name, args...)
		if len(env) > 0 {
			cmd.Env = append(os.Environ(), env...)
		}
		return cmd.Output()
	}
}

// CombinedRunner returns a CommandRunner that captures stdout and stderr
// together. Use it for CLIs that report human-readable status (e.g. login
// state) on stderr rather than stdout, where DefaultRunner would drop it.
func CombinedRunner() CommandRunner {
	return func(ctx context.Context, name string, args []string, env []string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, name, args...)
		if len(env) > 0 {
			cmd.Env = append(os.Environ(), env...)
		}
		return cmd.CombinedOutput()
	}
}

var versionRe = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

// ParseVersionOutput extracts a semver string from CLI version output.
// Handles formats like "1.0.20", "claude 1.0.20", "OpenAI Codex v0.116.0".
func ParseVersionOutput(output []byte) (string, error) {
	s := strings.TrimSpace(string(output))
	if s == "" {
		return "", fmt.Errorf("empty version output")
	}
	match := versionRe.FindString(s)
	if match == "" {
		return "", fmt.Errorf("no version found in %q", s)
	}
	return match, nil
}
