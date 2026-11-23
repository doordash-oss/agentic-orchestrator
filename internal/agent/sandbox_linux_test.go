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
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

func TestWrapWithBwrap_RebindsWorktreesReadOnly(t *testing.T) {
	wt := "/home/x/.agentic-workflow/worktrees"
	got := wrapWithBwrap([]string{"helper", "run"}, wt)
	want := []string{
		"bwrap",
		"--dev-bind", "/", "/",
		"--ro-bind", wt, wt,
		"--die-with-parent",
		"--",
		"helper", "run",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("wrapWithBwrap argv =\n%v\nwant\n%v", got, want)
	}
}

func helperSandboxExpected() bool { return bwrapUsable() }

func helperSandboxDenyDirs(t *testing.T, command []string) ([]string, bool) {
	t.Helper()
	if len(command) == 0 || command[0] != "bwrap" {
		return nil, false
	}
	var dirs []string
	for i := 0; i+2 < len(command) && command[i] != "--"; i++ {
		if command[i] == "--ro-bind" {
			dirs = append(dirs, command[i+1])
		}
	}
	return dirs, true
}

func TestMaybeWrapHelperSandbox_BwrapNamesWorktreesDir(t *testing.T) {
	if !bwrapUsable() {
		t.Skip("bwrap not usable on this host")
	}
	stateRoot := filepath.Join(t.TempDir(), "features")
	wt := feature.WorktreesDir(stateRoot)
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatalf("mkdir worktrees: %v", err)
	}
	got, sandboxed, _, cleanup := maybeWrapHelperSandbox([]string{"helper"}, true, stateRoot)
	defer cleanup()
	real, err := filepath.EvalSymlinks(wt)
	if err != nil {
		t.Fatalf("resolve worktrees dir: %v", err)
	}
	if dirs, ok := helperSandboxDenyDirs(t, got); !sandboxed || !ok || !reflect.DeepEqual(dirs, []string{real}) {
		t.Fatalf("wrapped argv = %v (sandboxed=%v), want --ro-bind %s", got, sandboxed, real)
	}
}
