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

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
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
	cmd := []string{"helper", "run"}
	got, sandboxed, cleanup := maybeWrapHelperSandbox(cmd, true, "/Users/x/.agentic-workflow/features")
	defer cleanup()
	if !sandboxed {
		t.Fatalf("sandboxed=false, want true on darwin")
	}
	if len(got) < 4 || got[0] != sandboxExecPath || got[1] != "-f" || got[len(got)-2] != "helper" || got[len(got)-1] != "run" {
		t.Fatalf("wrapped argv = %v, want [sandbox-exec -f <profile> helper run]", got)
	}
	if _, err := os.Stat(got[2]); err != nil {
		t.Fatalf("sandbox profile %q not written: %v", got[2], err)
	}
}

func TestWriteRestrictedSandboxProfile_DeniesWritesOutsideRoots(t *testing.T) {
	prof := writeRestrictedSandboxProfile([]string{"/Users/x/worktree"})
	for _, want := range []string{
		"(allow default)",
		"(deny file-write*)",
		`(allow file-write* (subpath "/dev"))`,
		`(allow file-write* (subpath "/Users/x/worktree"))`,
	} {
		if !strings.Contains(prof, want) {
			t.Errorf("profile missing %q:\n%s", want, prof)
		}
	}
}

func TestWrapVerificationSandbox_BlocksWritesOutsideRoots(t *testing.T) {
	testutil.RequireSandboxExec(t)
	if !sandboxExecAvailable() {
		t.Skip("sandbox-exec not available on this host")
	}
	writable := t.TempDir()
	protected := t.TempDir()

	run := func(command string) error {
		argv, sandboxed, cleanup := wrapVerificationSandbox([]string{"/bin/sh", "-c", command}, []string{writable})
		defer cleanup()
		if !sandboxed {
			t.Fatal("sandboxed = false, want true")
		}
		_, err := NewExecCommandRunner().Run(context.Background(), argv[0], argv[1:], ports.CommandOpts{})
		return err
	}

	if err := run("touch " + filepath.Join(writable, "inside")); err != nil {
		t.Fatalf("write inside writable root failed: %v", err)
	}
	// protected shares the temp base with writable, but only writable was
	// allow-listed — the deny-by-default must still block it.
	if err := run("touch " + filepath.Join(protected, "outside")); err == nil {
		t.Fatalf("write outside writable roots succeeded, want denial")
	}
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

func TestWrapSandboxesPassThroughWhenProbeFails(t *testing.T) {
	stubSandboxUnavailable(t, "nested")
	command := []string{"/bin/echo", "hi"}
	if got, sandboxed, cleanup := wrapVerificationSandbox(command, []string{t.TempDir()}); sandboxed || got[0] != "/bin/echo" {
		t.Fatalf("wrapVerificationSandbox = (%v, %v); want passthrough", got, sandboxed)
	} else {
		cleanup()
	}
	if got, sandboxed, cleanup := maybeWrapHelperSandbox(command, true, filepath.Join(t.TempDir(), "state")); sandboxed || got[0] != "/bin/echo" {
		t.Fatalf("maybeWrapHelperSandbox = (%v, %v); want passthrough", got, sandboxed)
	} else {
		cleanup()
	}
}
