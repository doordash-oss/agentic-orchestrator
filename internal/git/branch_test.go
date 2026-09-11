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
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

type scriptedBranchProbeRunner struct {
	mu      sync.Mutex
	results []BranchProbeCommandResult
	args    [][]string
}

func (r *scriptedBranchProbeRunner) Run(_ context.Context, _ string, args []string, _ int) BranchProbeCommandResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.args = append(r.args, append([]string(nil), args...))
	if len(r.results) == 0 {
		return BranchProbeCommandResult{ExitCode: -1, Err: errors.New("unexpected probe")}
	}
	result := r.results[0]
	r.results = r.results[1:]
	return result
}

func TestLayerBranchName(t *testing.T) {
	t.Parallel()

	got := LayerBranchName("fix-query-a1b2c3d4", 1, "fix-query")
	if got != "feature/fix-query-a1b2c3d4/1-fix-query" {
		t.Errorf("LayerBranchName = %q, want %q", got, "feature/fix-query-a1b2c3d4/1-fix-query")
	}
	if got := LayerBranchName("fix-query-a1b2c3d4", 2, "cleanup"); got != "feature/fix-query-a1b2c3d4/2-cleanup" {
		t.Errorf("LayerBranchName = %q, want %q", got, "feature/fix-query-a1b2c3d4/2-cleanup")
	}
}

func TestProbeRemoteBranchDistinguishesCollisionAndAbsence(t *testing.T) {
	t.Parallel()

	localDir, bareDir := testutil.InitPublishReadyGitRepo(t)
	testutil.CreateBranch(t, localDir, "feature/existing-branch")
	testutil.CommitFile(t, localDir, "feature.txt", "feature\n", "feature commit")
	testutil.SimulatePush(t, localDir, bareDir, "feature/existing-branch", "feature/existing-branch")

	t.Run("existing branch returns true", func(t *testing.T) {
		result := ProbeRemoteBranch(context.Background(), localDir, "feature/existing-branch", BranchProbeOptions{OperationTimeout: time.Second})
		if result.State != BranchProbeCollision {
			t.Fatalf("state = %q, want %q (diagnostics=%q)", result.State, BranchProbeCollision, result.Diagnostics)
		}
	})

	t.Run("non-existing branch returns false", func(t *testing.T) {
		result := ProbeRemoteBranch(context.Background(), localDir, "feature/nonexistent", BranchProbeOptions{OperationTimeout: time.Second})
		if result.State != BranchProbeAbsent {
			t.Fatalf("state = %q, want %q (diagnostics=%q)", result.State, BranchProbeAbsent, result.Diagnostics)
		}
	})
}

func TestProbeBranchPrefixChecksLocalBeforeUnavailableOrigin(t *testing.T) {
	t.Parallel()

	t.Run("local collision avoids remote probe", func(t *testing.T) {
		runner := &scriptedBranchProbeRunner{results: []BranchProbeCommandResult{
			{ExitCode: 0, Stdout: "refs/heads/feature/name-a1b2c3d4/2-anything\n"},
		}}
		result, err := ProbeBranchPrefix(context.Background(), []BranchProbeRepository{{Name: "repo-a", Path: "/repo-a", ProbeOrigin: true}}, "name-a1b2c3d4", BranchProbeOptions{Runner: runner})
		if err != nil {
			t.Fatalf("ProbeBranchPrefix() error = %v", err)
		}
		if result.State != BranchProbeCollision || len(runner.args) != 1 || !strings.Contains(strings.Join(runner.args[0], " "), "for-each-ref") {
			t.Fatalf("result = %+v, args = %v; want local collision from one for-each-ref", result, runner.args)
		}
	})

	t.Run("local listing failure is a hard error", func(t *testing.T) {
		runner := &scriptedBranchProbeRunner{results: []BranchProbeCommandResult{
			{ExitCode: 128, Diagnostics: "fatal: not a git repository", Err: errors.New("exit status 128")},
		}}
		_, err := ProbeBranchPrefix(context.Background(), []BranchProbeRepository{{Name: "repo-a", Path: "/repo-a"}}, "name-a1b2c3d4", BranchProbeOptions{Runner: runner})
		if err == nil || !strings.Contains(err.Error(), "repo-a") {
			t.Fatalf("ProbeBranchPrefix() error = %v, want a hard error naming the repository", err)
		}
	})

	t.Run("unavailable origin preserves locally unique candidate", func(t *testing.T) {
		runner := &scriptedBranchProbeRunner{results: []BranchProbeCommandResult{
			{ExitCode: 0, Stdout: ""},
			{ExitCode: 128, Diagnostics: "fatal: unable to access 'https://user:secret@example.test/repo': offline\x1b[31m", Err: errors.New("exit status 128")},
		}}
		result, err := ProbeBranchPrefix(context.Background(), []BranchProbeRepository{{Name: "repo-a", Path: "/repo-a", ProbeOrigin: true}}, "name-a1b2c3d4", BranchProbeOptions{Runner: runner})
		if err != nil {
			t.Fatalf("ProbeBranchPrefix() error = %v", err)
		}
		if result.State != BranchProbeUnavailable || len(result.Warnings) != 1 {
			t.Fatalf("result = %+v, want unavailable with one warning", result)
		}
		if got := result.Warnings[0].Branch; got != "feature/name-a1b2c3d4" {
			t.Fatalf("warning branch = %q, want the probed prefix", got)
		}
		if got := result.Warnings[0].Diagnostics; strings.Contains(got, "secret") || strings.ContainsRune(got, '\x1b') || !strings.Contains(got, "[redacted]") {
			t.Fatalf("diagnostics = %q, want credentials and controls redacted", got)
		}
	})
}

func TestProbeBranchPrefixDetectsRealLocalBranches(t *testing.T) {
	t.Parallel()
	repoDir := testutil.InitGitRepo(t)
	// A stale layer branch under the candidate prefix collides, and so does
	// the flat name, because git refuses nested refs while the flat ref
	// exists. An unrelated branch sharing only the prefix text does not.
	testutil.CreateBranch(t, repoDir, "feature/local-collision-a1b2c3d4/2-anything")
	testutil.CreateBranch(t, repoDir, "feature/flat-collision-a1b2c3d4")
	testutil.CreateBranch(t, repoDir, "feature/local-collision-a1b2c3d4-other")
	repos := []BranchProbeRepository{{Name: "repo-a", Path: repoDir}}

	result, err := ProbeBranchPrefix(context.Background(), repos, "local-collision-a1b2c3d4", BranchProbeOptions{OperationTimeout: time.Second})
	if err != nil {
		t.Fatalf("ProbeBranchPrefix() error = %v", err)
	}
	if result.State != BranchProbeCollision {
		t.Fatalf("state = %q, want collision from the nested local branch", result.State)
	}

	result, err = ProbeBranchPrefix(context.Background(), repos, "flat-collision-a1b2c3d4", BranchProbeOptions{OperationTimeout: time.Second})
	if err != nil {
		t.Fatalf("ProbeBranchPrefix(flat) error = %v", err)
	}
	if result.State != BranchProbeCollision {
		t.Fatalf("flat state = %q, want collision from the flat local branch", result.State)
	}

	result, err = ProbeBranchPrefix(context.Background(), repos, "unrelated-a1b2c3d4", BranchProbeOptions{OperationTimeout: time.Second})
	if err != nil {
		t.Fatalf("ProbeBranchPrefix(unrelated) error = %v", err)
	}
	if result.State != BranchProbeAbsent {
		t.Fatalf("unrelated state = %q, want absent", result.State)
	}
}

func TestProbeBranchPrefixProbesOriginForPublishableRepositories(t *testing.T) {
	t.Parallel()
	localDir, bareDir := testutil.InitPublishReadyGitRepo(t)
	seedRemoteOnlyBranch := func(slug, fileName string) {
		t.Helper()
		branch := "feature/" + slug
		testutil.CreateBranch(t, localDir, branch)
		testutil.CommitFile(t, localDir, fileName, slug+"\n", slug+" commit")
		testutil.SimulatePush(t, localDir, bareDir, branch, branch)
		// The collision must come from the origin alone: drop the local ref.
		// CreateBranch checks the branch out, so move back to main first.
		runGit(t, localDir, "checkout", "main")
		runGit(t, localDir, "branch", "-D", branch)
	}
	seedRemoteOnlyBranch("remote-collision-a1b2c3d4/2-stale-layer", "stale-layer.txt")
	seedRemoteOnlyBranch("remote-flat-a1b2c3d4", "flat.txt")
	testutil.CreateBranch(t, localDir, "feature/unrelated-a1b2c3d4-other")

	t.Run("remote branch under the prefix collides with no local match", func(t *testing.T) {
		repos := []BranchProbeRepository{{Name: "repo-a", Path: localDir, ProbeOrigin: true}}
		result, err := ProbeBranchPrefix(context.Background(), repos, "remote-collision-a1b2c3d4", BranchProbeOptions{OperationTimeout: 5 * time.Second})
		if err != nil {
			t.Fatalf("ProbeBranchPrefix() error = %v", err)
		}
		if result.State != BranchProbeCollision {
			t.Fatalf("state = %q, want collision from the remote nested branch", result.State)
		}
	})

	t.Run("remote flat branch collides with no local match", func(t *testing.T) {
		repos := []BranchProbeRepository{{Name: "repo-a", Path: localDir, ProbeOrigin: true}}
		result, err := ProbeBranchPrefix(context.Background(), repos, "remote-flat-a1b2c3d4", BranchProbeOptions{OperationTimeout: 5 * time.Second})
		if err != nil {
			t.Fatalf("ProbeBranchPrefix() error = %v", err)
		}
		if result.State != BranchProbeCollision {
			t.Fatalf("state = %q, want collision from the remote flat branch", result.State)
		}
	})

	t.Run("local-only repository skips the origin probe", func(t *testing.T) {
		repos := []BranchProbeRepository{{Name: "repo-a", Path: localDir, ProbeOrigin: false}}
		result, err := ProbeBranchPrefix(context.Background(), repos, "remote-collision-a1b2c3d4", BranchProbeOptions{OperationTimeout: 5 * time.Second})
		if err != nil {
			t.Fatalf("ProbeBranchPrefix() error = %v", err)
		}
		if result.State != BranchProbeAbsent {
			t.Fatalf("state = %q, want absent (origin not probed)", result.State)
		}
	})

	t.Run("unreachable origin yields unavailable with a sanitized warning", func(t *testing.T) {
		// A real checkout with no origin remote: the local listing succeeds
		// and the remote probe fails without proving absence.
		repoDir := testutil.InitGitRepo(t)
		repos := []BranchProbeRepository{{Name: "repo-a", Path: repoDir, ProbeOrigin: true}}
		result, err := ProbeBranchPrefix(context.Background(), repos, "any-a1b2c3d4", BranchProbeOptions{OperationTimeout: 5 * time.Second})
		if err != nil {
			t.Fatalf("ProbeBranchPrefix() error = %v", err)
		}
		if result.State != BranchProbeUnavailable || len(result.Warnings) != 1 {
			t.Fatalf("result = %+v, want unavailable with one warning", result)
		}
		if warning := result.Warnings[0]; warning.Repository != "repo-a" {
			t.Fatalf("warning repository = %q, want repo-a", warning.Repository)
		}
	})
}

func TestNonInteractiveGitEnvPreservesServerConfiguration(t *testing.T) {
	t.Parallel()
	input := []string{
		"HOME=/server/home",
		"HTTPS_PROXY=http://proxy.example.test",
		"GIT_CONFIG_GLOBAL=/server/gitconfig",
		"GIT_TERMINAL_PROMPT=1",
		"GIT_ASKPASS=/server/askpass",
		"GIT_SSH_COMMAND=ssh -F /server/ssh-config -i /server/key",
	}
	got := nonInteractiveGitEnv(input)

	for _, preserved := range input[:3] {
		if !containsExactString(got, preserved) {
			t.Errorf("environment does not preserve %q: %v", preserved, got)
		}
	}
	for _, key := range []string{"GIT_TERMINAL_PROMPT", "GIT_ASKPASS", "GIT_SSH_COMMAND"} {
		if countEnvironmentKey(got, key) != 1 {
			t.Errorf("%s entries = %d, want exactly one authoritative value: %v", key, countEnvironmentKey(got, key), got)
		}
	}
	if value := environmentValue(got, "GIT_TERMINAL_PROMPT"); value != "0" {
		t.Errorf("GIT_TERMINAL_PROMPT = %q, want 0", value)
	}
	if value := environmentValue(got, "GIT_SSH_COMMAND"); !strings.Contains(value, "-F /server/ssh-config") || !strings.Contains(value, "-i /server/key") || !strings.Contains(value, "BatchMode=yes") {
		t.Errorf("GIT_SSH_COMMAND = %q, want server config/identity plus BatchMode", value)
	}
}

func TestExecBranchProbeRunnerCancellationKillsProcessTree(t *testing.T) {
	binDir := t.TempDir()
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	script := "#!/bin/sh\nsleep 60 &\necho $! > \"$BRANCH_PROBE_CHILD_PID\"\nwait\n"
	if err := os.WriteFile(filepath.Join(binDir, "git"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake git: %v", err)
	}
	t.Setenv("BRANCH_PROBE_CHILD_PID", pidFile)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	started := make(chan struct{})
	results := make(chan BranchProbeResult, 1)
	go func() {
		results <- ProbeRemoteBranch(ctx, t.TempDir(), "feature/hung-helper", BranchProbeOptions{
			OperationTimeout: 30 * time.Second,
			Runner: ExecBranchProbeRunner{
				Executable: filepath.Join(binDir, "git"),
				afterStart: func() { close(started) },
			},
		})
	}()
	select {
	case <-started:
	case <-time.After(15 * time.Second):
		t.Fatal("probe process did not start")
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(pidFile); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("helper process did not publish its pid")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	result := <-results
	if result.State != BranchProbeUnavailable {
		t.Fatalf("result = %+v, want unavailable cancellation", result)
	}
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("read helper pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("parse helper pid: %v", err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for syscall.Kill(pid, 0) == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("helper process %d survived timed-out probe: %v", pid, err)
	}
}

func TestExecBranchProbeRunnerDrainsAndBoundsDiagnostics(t *testing.T) {
	binDir := t.TempDir()
	scriptPath := filepath.Join(binDir, "git")
	script := "#!/bin/sh\nyes X | head -c 1048576 >&2\nexit 128\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake git: %v", err)
	}

	result := ProbeRemoteBranch(context.Background(), t.TempDir(), "feature/noisy-helper", BranchProbeOptions{
		OperationTimeout: 2 * time.Second,
		DiagnosticLimit:  64,
		Runner:           ExecBranchProbeRunner{Executable: scriptPath},
	})
	if result.State != BranchProbeUnavailable {
		t.Fatalf("state = %q, want unavailable", result.State)
	}
	if len(result.Diagnostics) == 0 || len(result.Diagnostics) > 64 {
		t.Fatalf("diagnostics length = %d, want 1..64", len(result.Diagnostics))
	}
}

func containsExactString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func countEnvironmentKey(env []string, key string) int {
	count := 0
	for _, value := range env {
		if strings.HasPrefix(value, key+"=") {
			count++
		}
	}
	return count
}

func environmentValue(env []string, key string) string {
	for _, value := range env {
		if strings.HasPrefix(value, key+"=") {
			return strings.TrimPrefix(value, key+"=")
		}
	}
	return ""
}
