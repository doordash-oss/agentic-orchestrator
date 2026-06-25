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

//go:build linux

package agent

import (
	"context"
	"os/exec"
	"path/filepath"
	"sync"

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// wrapHelperSandbox runs the helper under bubblewrap, re-binding the worktrees
// base read-only over an otherwise read-write filesystem. Fails open when
// bubblewrap is unavailable or can't create a sandbox here.
func wrapHelperSandbox(command []string, worktreesBase string) ([]string, bool, func()) {
	if !bwrapUsable() {
		return command, false, func() {}
	}
	return wrapWithBwrap(command, worktreesBase), true, func() {}
}

var bwrapUsableOnce struct {
	sync.Once
	ok bool
}

// bwrapUsable reports whether bubblewrap is installed and can create a sandbox
// here (it needs unprivileged user namespaces). Cached: the probe runs once.
func bwrapUsable() bool {
	bwrapUsableOnce.Do(func() {
		if _, err := exec.LookPath("bwrap"); err != nil {
			return
		}
		// Confirm a sandbox can actually be created (user namespaces enabled).
		_, err := NewExecCommandRunner().Run(context.Background(), "bwrap", []string{"--dev-bind", "/", "/", "true"}, ports.CommandOpts{})
		bwrapUsableOnce.ok = err == nil
	})
	return bwrapUsableOnce.ok
}

// wrapWithBwrap binds the whole filesystem read-write, then re-binds the
// worktrees base read-only on top, so every other path (helper artifacts,
// provider state, /tmp) stays writable. --die-with-parent ties the sandbox
// lifetime to the orchestrator.
func wrapWithBwrap(command []string, denyDir string) []string {
	real := denyDir
	if resolved, err := filepath.EvalSymlinks(denyDir); err == nil {
		real = resolved
	}
	args := []string{
		"bwrap",
		"--dev-bind", "/", "/",
		"--ro-bind", real, real,
		"--die-with-parent",
		"--",
	}
	return append(args, command...)
}
