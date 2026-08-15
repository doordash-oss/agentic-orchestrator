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
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// stubGit puts a `git` script on PATH whose body is the given shell snippet,
// and shortens both probe bounds so timeout paths are cheap to exercise.
func stubGit(t *testing.T, body string) {
	t.Helper()
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "git"), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatalf("write git stub: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	prev, prevCleanliness := ProbeTimeout, CleanlinessProbeTimeout
	// Generous enough to absorb the first exec of a freshly written script
	// (a few hundred ms on macOS) while still far below the hangs it bounds.
	ProbeTimeout = time.Second
	CleanlinessProbeTimeout = time.Second
	t.Cleanup(func() {
		ProbeTimeout = prev
		CleanlinessProbeTimeout = prevCleanliness
	})
}

// hangingGitAfter returns a stub body that hangs on the given git subcommand and
// delegates everything else to the real git.
func hangingGitAfter(t *testing.T, subcommand string) string {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("look up git: %v", err)
	}
	return "for arg in \"$@\"; do\n" +
		"  if [ \"$arg\" = \"" + subcommand + "\" ]; then sleep 30; fi\n" +
		"done\n" +
		"exec " + realGit + " \"$@\"\n"
}

func TestRepoFreshnessStatusTimeoutIsUnknown(t *testing.T) {
	repo, _ := testutil.InitPublishReadyGitRepo(t)
	stubGit(t, "sleep 30\n")

	start := time.Now()
	got := RepoFreshness(repo)
	elapsed := time.Since(start)

	if got != FreshnessUnknown {
		t.Errorf("RepoFreshness() = %q, want %q", got, FreshnessUnknown)
	}
	if elapsed > 5*time.Second {
		t.Errorf("RepoFreshness() took %v; want bounded by ProbeTimeout", elapsed)
	}
}

func TestReadOnlyGitCommandsDisableOptionalLocks(t *testing.T) {
	for name, cmd := range map[string]*exec.Cmd{
		"bounded probe": probeGitCmd(context.Background(), "status"),
		"read command":  readGitCmd(t.TempDir(), "status"),
	} {
		found := false
		for _, env := range cmd.Env {
			if env == "GIT_OPTIONAL_LOCKS=0" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s environment does not disable optional locks", name)
		}
	}
}

// A hung upstream lookup must not masquerade as a repo without an upstream.
func TestRepoFreshnessUpstreamTimeoutIsUnknownNotLocalOnly(t *testing.T) {
	repo, _ := testutil.InitPublishReadyGitRepo(t)
	stubGit(t, hangingGitAfter(t, "rev-parse"))

	if got := RepoFreshness(repo); got != FreshnessUnknown {
		t.Errorf("RepoFreshness() = %q, want %q", got, FreshnessUnknown)
	}
}

func TestRepoFreshnessRevListTimeoutIsUnknown(t *testing.T) {
	repo, _ := testutil.InitPublishReadyGitRepo(t)
	stubGit(t, hangingGitAfter(t, "rev-list"))

	if got := RepoFreshness(repo); got != FreshnessUnknown {
		t.Errorf("RepoFreshness() = %q, want %q", got, FreshnessUnknown)
	}
}

func TestInspectCleanlinessTimeoutIsNotClean(t *testing.T) {
	repo := initCleanlinessRepo(t)
	stubGit(t, "sleep 30\n")

	wm := NewWorktreeManager(t.TempDir())
	start := time.Now()
	report, err := wm.InspectCleanliness(repo, 0)
	elapsed := time.Since(start)

	if !errors.Is(err, ErrProbeTimeout) {
		t.Fatalf("InspectCleanliness() error = %v, want ErrProbeTimeout", err)
	}
	if report != nil {
		t.Fatalf("InspectCleanliness() report = %+v, want nil on timeout", report)
	}
	if elapsed > 5*time.Second {
		t.Errorf("InspectCleanliness() took %v; want bounded by ProbeTimeout", elapsed)
	}
}

// The timeout must reap the whole process group, not just the direct child.
func TestProbeTimeoutKillsProcessGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	stubGit(t, "sleep 30 &\necho $! > "+pidFile+"\nwait\n")

	if _, timedOut, err := runProbe("status"); !timedOut {
		t.Fatalf("runProbe() timedOut = false, err = %v; want timeout", err)
	}

	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("read child pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("parse child pid %q: %v", raw, err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("descendant pid %d survived the probe timeout", pid)
}

func TestRunProbeFastPathReturnsOutput(t *testing.T) {
	repo := initCleanlinessRepo(t)

	out, timedOut, err := runProbe("-C", repo, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil || timedOut {
		t.Fatalf("runProbe() err = %v, timedOut = %v", err, timedOut)
	}
	if got := strings.TrimSpace(string(out)); got != "main" {
		t.Errorf("runProbe() = %q, want %q", got, "main")
	}
}

// TestCleanlinessProbeTimeoutExceedsCheapProbeBound guards the bound that
// stranded a real repository: `status --untracked-files=all` walks every
// untracked directory and took over 4s on a multi-gigabyte worktree, so a bound
// shared with the cheap probes made the scan time out on every attempt. The
// cleanliness bound must stay well clear of that observed runtime.
func TestCleanlinessProbeTimeoutExceedsCheapProbeBound(t *testing.T) {
	const observedSlowScan = 5 * time.Second
	if CleanlinessProbeTimeout <= observedSlowScan {
		t.Fatalf("CleanlinessProbeTimeout = %v; want headroom over the %v observed on a large worktree",
			CleanlinessProbeTimeout, observedSlowScan)
	}
	if CleanlinessProbeTimeout <= ProbeTimeout {
		t.Fatalf("CleanlinessProbeTimeout = %v; want more than the cheap-probe bound %v",
			CleanlinessProbeTimeout, ProbeTimeout)
	}
}
