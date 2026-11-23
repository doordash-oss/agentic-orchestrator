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

//go:build darwin

package agent

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

func TestReadOnlySandboxProfile_DeniesWorktreeWritesAllowsRest(t *testing.T) {
	prof := readOnlySandboxProfile([]string{"/Users/x/.agentic-workflow/worktrees"})
	if !strings.Contains(prof, "(allow default)") {
		t.Errorf("profile missing (allow default):\n%s", prof)
	}
	if !strings.Contains(prof, `(deny file-write* (subpath "/Users/x/.agentic-workflow/worktrees"))`) {
		t.Errorf("profile missing worktree deny line:\n%s", prof)
	}
}

func TestMaybeWrapHelperSandbox_EnabledWrapsWithSandboxExec(t *testing.T) {
	if !sandboxExecAvailable() {
		t.Skip("sandbox-exec not available on this host")
	}
	stateRoot := filepath.Join(t.TempDir(), "features")
	wt := feature.WorktreesDir(stateRoot)
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatalf("mkdir worktrees: %v", err)
	}
	cmd := []string{"helper", "run"}
	got, sandboxed, _, cleanup := maybeWrapHelperSandbox(cmd, true, stateRoot)
	defer cleanup()
	if !sandboxed {
		t.Fatalf("sandboxed=false, want true on darwin")
	}
	if len(got) < 4 || got[0] != sandboxExecPath || got[1] != "-f" || got[len(got)-2] != "helper" || got[len(got)-1] != "run" {
		t.Fatalf("wrapped argv = %v, want [sandbox-exec -f <profile> helper run]", got)
	}
	real, err := filepath.EvalSymlinks(wt)
	if err != nil {
		t.Fatalf("resolve worktrees dir: %v", err)
	}
	if dirs, _ := helperSandboxDenyDirs(t, got); len(dirs) != 1 || dirs[0] != real {
		t.Fatalf("profile deny dirs = %v, want [%s]", dirs, real)
	}
}

func helperSandboxExpected() bool { return sandboxExecAvailable() }

func helperSandboxDenyDirs(t *testing.T, command []string) ([]string, bool) {
	t.Helper()
	if len(command) < 3 || command[0] != sandboxExecPath || command[1] != "-f" {
		return nil, false
	}
	profile, err := os.ReadFile(command[2])
	if err != nil {
		t.Fatalf("read sandbox profile: %v", err)
	}
	var dirs []string
	for _, line := range strings.Split(string(profile), "\n") {
		if _, rest, ok := strings.Cut(line, `(deny file-write* (subpath "`); ok {
			dirs = append(dirs, strings.TrimSuffix(rest, `"))`))
		}
	}
	return dirs, true
}

type sandboxProbeRunner struct {
	stderr string
	err    error
	name   string
	args   []string
}

func (r *sandboxProbeRunner) Run(_ context.Context, name string, args []string, opts ports.CommandOpts) ([]byte, error) {
	r.name, r.args = name, args
	if opts.Stderr != nil {
		_, _ = opts.Stderr.Write([]byte(r.stderr))
	}
	return nil, r.err
}

type exitCodeError int

func (e exitCodeError) Error() string { return fmt.Sprintf("exit status %d", int(e)) }
func (e exitCodeError) ExitCode() int { return int(e) }

func TestProbeSandboxExecClassifiesOutcomes(t *testing.T) {
	cases := []struct {
		name        string
		runner      *sandboxProbeRunner
		unavailable bool
		reason      string
	}{
		{"available", &sandboxProbeRunner{}, false, ""},
		{"missing binary", &sandboxProbeRunner{err: &fs.PathError{Op: "fork/exec", Path: sandboxExecPath, Err: fs.ErrNotExist}}, true, "is not installed"},
		{"nested", &sandboxProbeRunner{err: exitCodeError(71), stderr: "sandbox-exec: sandbox_apply: Operation not permitted\n"}, true, "nested sandbox-exec is not permitted"},
		{"exit 71 other", &sandboxProbeRunner{err: exitCodeError(71), stderr: "boom"}, true, "sandbox-exec probe failed: exit status 71: boom"},
		{"other", &sandboxProbeRunner{err: exitCodeError(1)}, true, "sandbox-exec probe failed: exit status 1"},
	}
	for _, tc := range cases {
		got := probeSandboxExec(tc.runner)
		if got.Unavailable != tc.unavailable || !strings.Contains(got.Reason, tc.reason) {
			t.Errorf("%s: probe = %+v; want unavailable=%v reason containing %q", tc.name, got, tc.unavailable, tc.reason)
		}
		if tc.runner.name != sandboxExecPath || strings.Join(tc.runner.args, " ") != "-p (version 1)(allow default) /usr/bin/true" {
			t.Errorf("%s: probe ran %s %v", tc.name, tc.runner.name, tc.runner.args)
		}
	}
}

func TestWrapHelperSandboxPassesThroughWhenProbeFails(t *testing.T) {
	stubSandboxUnavailable(t, "nested")
	command := []string{"/bin/echo", "hi"}
	stateRoot := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(feature.WorktreesDir(stateRoot), 0o755); err != nil {
		t.Fatalf("mkdir worktrees: %v", err)
	}
	if got, sandboxed, reason, cleanup := maybeWrapHelperSandbox(command, true, stateRoot); sandboxed || got[0] != "/bin/echo" || reason != "nested" {
		t.Fatalf("maybeWrapHelperSandbox = (%v, %v, %q); want passthrough with the probe reason", got, sandboxed, reason)
	} else {
		cleanup()
	}
}
