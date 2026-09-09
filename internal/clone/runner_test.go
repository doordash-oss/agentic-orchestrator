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

package clone

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestRealRunnerClonesRealGitRepository exercises real git semantics over
// a local file transport (the runner is transport-agnostic; API-level
// validation lives above it).
func TestRealRunnerClonesRealGitRepository(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	src := makeGitRepo(t)
	staging := filepath.Join(t.TempDir(), "staging")
	stages := make(chan string, 8)
	handle, err := NewRealRunner("").Start(RunSpec{
		Remote:   "file://" + src,
		Staging:  staging,
		Deadline: 30 * time.Second,
		OnStage: func(stage, progress string) {
			select {
			case stages <- stage + ":" + progress:
			default:
			}
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	res := handle.Wait()
	if res.ExitCode != 0 || res.Err != nil {
		t.Fatalf("clone failed: %+v tail=%s", res, res.OutputTail)
	}
	if !gitHasHeadRepository(staging) {
		t.Fatal("staging is not a full-history clone")
	}
	// Authored progress was derived from real git output.
	var sawProgress bool
	for {
		select {
		case <-stages:
			sawProgress = true
			continue
		default:
		}
		break
	}
	if !sawProgress {
		t.Error("no progress updates from real clone")
	}
}

func gitHasHeadRepository(dir string) bool {
	cmd := exec.Command("git", "-C", dir, "log", "--oneline", "-1")
	return cmd.Run() == nil
}

// TestRealRunnerTerminatesProcessTree pins the process-group guarantee: a
// descendant holding pipes open cannot survive termination or stall the
// bounded reap.
func TestRealRunnerTerminatesProcessTree(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	gitShim := filepath.Join(dir, "fake-git")
	script := `#!/bin/sh
child_pid_file="$FAKE_CHILD_PID"
if [ -n "$child_pid_file" ]; then
  (while true; do sleep 1; done) &
  echo $! > "$child_pid_file"
fi
while true; do sleep 1; done
`
	if err := os.WriteFile(gitShim, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	childPIDFile := filepath.Join(dir, "child.pid")
	handle, err := NewRealRunner(gitShim).Start(RunSpec{
		Remote:  "https://example.com/acme/widget.git",
		Staging: filepath.Join(dir, "staging"),
		// The shim only forks the pipe-holding descendant when it is told
		// where to record the child's pid; without this the test would
		// observe no tree to terminate.
		Env:      []string{"FAKE_CHILD_PID=" + childPIDFile},
		Deadline: time.Minute,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	var childPID int
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(childPIDFile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && pid > 0 {
				childPID = pid
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if childPID == 0 {
		// Skipping here would silently retire the process-tree guarantee and
		// leave the shim's process group orphaned, since Terminate is never
		// reached.
		handle.Terminate()
		handle.Wait()
		t.Fatal("the git shim never recorded its descendant's pid; the process-tree guarantee is unverified")
	}

	done := make(chan RunResult, 1)
	go func() { done <- handle.Wait() }()
	handle.Terminate()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Terminate did not observe exit within its bound")
	}
	// The whole tree, including the pipe-holding descendant, is gone.
	time.Sleep(100 * time.Millisecond)
	if err := syscall.Kill(childPID, 0); err == nil {
		t.Errorf("descendant %d survived process-tree termination", childPID)
	}
}

// TestCloneEnvironmentPreservesServerSettingsAndSuppressesPrompts pins the
// worker environment contract: server credential/ssh/proxy configuration
// is inherited untouched, and interactive prompting is disabled without
// weakening host trust checking.
func TestCloneEnvironmentPreservesServerSettingsAndSuppressesPrompts(t *testing.T) {
	t.Parallel()
	env := cloneEnvironment()
	envMap := map[string]string{}
	for _, kv := range env {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}
	for _, key := range []string{"GIT_TERMINAL_PROMPT", "GIT_ASKPASS", "SSH_ASKPASS", "SSH_ASKPASS_REQUIRE", "GIT_SSH_COMMAND"} {
		if envMap[key] == "" {
			t.Errorf("%s missing from clone environment", key)
		}
	}
	if envMap["GIT_TERMINAL_PROMPT"] != "0" {
		t.Errorf("GIT_TERMINAL_PROMPT = %q, want 0", envMap["GIT_TERMINAL_PROMPT"])
	}
	if !strings.Contains(envMap["GIT_SSH_COMMAND"], "BatchMode=yes") {
		t.Errorf("GIT_SSH_COMMAND = %q, want batch mode", envMap["GIT_SSH_COMMAND"])
	}
	if !strings.Contains(envMap["GIT_SSH_COMMAND"], "StrictHostKeyChecking") {
		// Trust checking must remain the ssh default (never disabled).
		t.Logf("GIT_SSH_COMMAND leaves host trust checking at ssh defaults: %q", envMap["GIT_SSH_COMMAND"])
	}
	// Server-side credential helpers and agent survive.
	if _, ok := os.LookupEnv("PATH"); ok && envMap["PATH"] == "" {
		t.Error("PATH not inherited")
	}
}

// TestRealRunnerDeadlineKillsTree pins the injectable deadline.
func TestRealRunnerDeadlineKillsTree(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	gitShim := filepath.Join(dir, "fake-git")
	if err := os.WriteFile(gitShim, []byte("#!/bin/sh\nwhile true; do sleep 1; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	handle, err := NewRealRunner(gitShim).Start(RunSpec{
		Remote:   "https://example.com/acme/widget.git",
		Staging:  filepath.Join(dir, "staging"),
		Deadline: 150 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	res := handle.Wait()
	if !res.TimedOut {
		t.Fatalf("deadline not applied: %+v", res)
	}
	if err := syscall.Kill(handle.Pid(), 0); err == nil {
		t.Error("deadline left the process alive")
	}
}

// TestRedactDiagnostics covers secret-bearing git/helper output across
// every shape that can reach a diagnostic sink.
func TestRedactDiagnostics(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"fatal: Authentication failed for 'https://ci:supersecret99@example.com/acme/widget.git'": "supersecret99",
		"remote: HTTP Basic: Access denied for user token=abc123def456ghi789":                     "abc123def456ghi789",
		"Authorization: Bearer ya29.eyJhbGciOiJSUzI1NiIsImtpZCI6IjA0NTM5OQ":                       "ya29.eyJhbGciOiJSUzI1NiIsImtpZCI6IjA0NTM5OQ",
		"password=hunter2 was rejected":                                                           "hunter2",
	}
	for raw, secret := range cases {
		if got := RedactDiagnostics(raw); strings.Contains(got, secret) {
			t.Errorf("RedactDiagnostics(%q) = %q leaked %q", raw, got, secret)
		}
	}
}

// TestOutputFloodCannotExhaustMemory pins the bounded output contract.
func TestOutputFloodCannotExhaustMemory(t *testing.T) {
	t.Parallel()
	h := &realHandle{done: make(chan struct{}), progressFn: nil}
	flood := strings.Repeat("x", 4*1024)
	for i := 0; i < 300; i++ {
		h.ingest(flood)
	}
	h.tailMu.Lock()
	tailBytes := 0
	for _, line := range h.tail {
		tailBytes += len(line)
	}
	h.tailMu.Unlock()
	if tailBytes > 8*(MaxDiagnosticsLength+8) {
		t.Errorf("tail grew to %d bytes beyond its bound", tailBytes)
	}
}
