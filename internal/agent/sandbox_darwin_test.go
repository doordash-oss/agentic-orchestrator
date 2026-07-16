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
	"os"
	"path/filepath"
	"strings"
	"testing"

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
