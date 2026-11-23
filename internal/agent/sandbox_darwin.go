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
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// sandboxExecPath is the macOS process sandbox launcher.
const sandboxExecPath = "/usr/bin/sandbox-exec"

// wrapHelperSandbox runs the helper under sandbox-exec with an SBPL profile that
// denies writes under the worktrees base. Fails open when sandbox-exec is
// unavailable or the profile can't be written.
func wrapHelperSandbox(command []string, worktreesBase string) ([]string, bool, func()) {
	noop := func() {}
	if !sandboxExecAvailable() {
		return command, false, noop
	}
	wrapped, cleanup, err := wrapWithSandboxExec(command, []string{worktreesBase})
	if err != nil {
		return command, false, noop
	}
	return wrapped, true, cleanup
}

func sandboxExecAvailable() bool {
	return !SandboxAvailability().Unavailable
}

// sandboxProbeTimeout bounds the functional sandbox-exec probe.
const sandboxProbeTimeout = 2 * time.Second

func platformSandboxProbe() SandboxStatus {
	return probeSandboxExec(NewExecCommandRunner())
}

// probeSandboxExec applies a trivial allow-all profile to /usr/bin/true. It
// proves sandbox-exec exists and can apply a profile: macOS refuses to nest
// seatbelt sandboxes, so under an outer sandbox (hardened CI, Agentico
// validating itself) sandbox-exec exits 71 with "sandbox_apply: Operation
// not permitted" for every command it would wrap.
func probeSandboxExec(runner ports.CommandRunner) SandboxStatus {
	ctx, cancel := context.WithTimeout(context.Background(), sandboxProbeTimeout)
	defer cancel()
	var stderr bytes.Buffer
	_, err := runner.Run(ctx, sandboxExecPath, []string{"-p", "(version 1)(allow default)", "/usr/bin/true"},
		ports.CommandOpts{Stderr: &stderr})
	if err == nil {
		return SandboxStatus{}
	}
	detail := strings.TrimSpace(stderr.String())
	switch {
	case errors.Is(err, fs.ErrNotExist) || errors.Is(err, exec.ErrNotFound):
		return SandboxStatus{Unavailable: true, Reason: sandboxExecPath + " is not installed"}
	case sandboxExitCode(err) == 71 && strings.Contains(strings.ToLower(detail), "sandbox_apply"):
		return SandboxStatus{Unavailable: true,
			Reason: "nested sandbox-exec is not permitted (Agentico already runs inside a sandbox): " + detail}
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return SandboxStatus{Unavailable: true, Reason: "sandbox-exec probe timed out after " + sandboxProbeTimeout.String()}
	}
	reason := "sandbox-exec probe failed: " + err.Error()
	if detail != "" {
		reason += ": " + detail
	}
	return SandboxStatus{Unavailable: true, Reason: reason}
}

// readOnlySandboxProfile builds an SBPL profile that allows everything by
// default but denies file writes under each deny dir (resolved to its real
// path). A worktree write fails as an ordinary shell error the model absorbs.
func readOnlySandboxProfile(denyDirs []string) string {
	var b strings.Builder
	b.WriteString("(version 1)\n(allow default)\n")
	for _, d := range denyDirs {
		real := filepath.Clean(d)
		if resolved, err := filepath.EvalSymlinks(d); err == nil {
			real = resolved
		}
		esc := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(real)
		fmt.Fprintf(&b, "(deny file-write* (subpath \"%s\"))\n", esc)
	}
	return b.String()
}

func wrapWithSandboxExec(command, denyDirs []string) (wrapped []string, cleanup func(), err error) {
	f, err := os.CreateTemp("", "agentico-helper-ro-*.sb")
	if err != nil {
		return nil, nil, fmt.Errorf("creating sandbox profile: %w", err)
	}
	profilePath := f.Name()
	if _, werr := f.WriteString(readOnlySandboxProfile(denyDirs)); werr != nil {
		_ = f.Close()
		_ = os.Remove(profilePath)
		return nil, nil, fmt.Errorf("writing sandbox profile: %w", werr)
	}
	if cerr := f.Close(); cerr != nil {
		_ = os.Remove(profilePath)
		return nil, nil, cerr
	}
	wrapped = append([]string{sandboxExecPath, "-f", profilePath}, command...)
	return wrapped, func() { _ = os.Remove(profilePath) }, nil
}

func sandboxExitCode(err error) int {
	var exitCoder interface{ ExitCode() int }
	if errors.As(err, &exitCoder) {
		return exitCoder.ExitCode()
	}
	return -1
}
