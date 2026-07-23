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

package session

import (
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// startProcessGroup spawns a "sleep 60" process in its own process group
// (mirroring how Session.Start sets Setpgid: true) and returns the PID and
// the exec.Cmd. The caller must call cmd.Wait() after terminating the process
// to reap the zombie (in production, init/launchd does this automatically for
// orphaned processes).
func startProcessGroup(t *testing.T) (int, *exec.Cmd) {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep process: %v", err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() {
		// Best-effort cleanup: kill process group in case test didn't terminate it.
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		_ = cmd.Wait()
	})
	return pid, cmd
}

func TestScanForRecoveryEmpty(t *testing.T) {
	dir := t.TempDir()
	// No PID files — should return empty
	items, err := ScanForRecovery(dir, nil)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("expected 0 items, got %d", len(items))
	}
}

func TestScanForRecoveryWithStale(t *testing.T) {
	dir := t.TempDir()

	// Create a PID file with a non-running PID
	feat1Dir := filepath.Join(dir, "feat-1")
	_ = WritePIDFile(feat1Dir, PIDFile{PID: 999999999, FeatureID: "feat-1", Phase: "implement"})

	items, err := ScanForRecovery(dir, nil)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].ProcessAlive {
		t.Error("expected process to not be alive")
	}
}

func TestCleanupStalePIDFiles(t *testing.T) {
	dir := t.TempDir()

	// Create a stale PID file
	feat1Dir := filepath.Join(dir, "feat-1")
	_ = WritePIDFile(feat1Dir, PIDFile{PID: 999999999, FeatureID: "feat-1"})

	err := cleanupStalePIDFiles(dir)
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	// PID file should be removed
	pids, _ := FindPIDFiles(dir)
	if len(pids) != 0 {
		t.Errorf("expected 0 PID files after cleanup, got %d", len(pids))
	}
}

func TestExecuteRecoveryKill(t *testing.T) {
	dir := t.TempDir()
	store := feature.NewStore(dir)
	cfg := config.NewDefault()
	fm := feature.NewManager(store, cfg)

	// Create a feature in Implementing state
	f := &feature.Feature{
		ID:            "feat-kill",
		Name:          "Kill Test",
		Slug:          "kill-test",
		Status:        feature.StatusImplementing,
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	_ = store.Save(f)

	pidDir := filepath.Join(dir, "feat-kill")
	_ = WritePIDFile(pidDir, PIDFile{PID: 999999999, FeatureID: "feat-kill", Phase: "implement", Dir: pidDir})

	items := []RecoveryItem{
		{
			PIDFile:      PIDFile{PID: 999999999, FeatureID: "feat-kill", Phase: "implement", Dir: pidDir},
			ProcessAlive: false,
			Feature:      f,
		},
	}

	actions := map[string]RecoveryAction{
		"feat-kill": RecoveryKill,
	}

	err := ExecuteRecovery(items, actions, fm)
	if err != nil {
		t.Fatalf("execute recovery: %v", err)
	}

	// Feature should be Interrupted
	updated, _ := fm.Get("feat-kill")
	if updated.Status != feature.StatusInterrupted {
		t.Errorf("status = %v, want Interrupted", updated.Status)
	}

	// PID file should be removed
	pids, _ := FindPIDFiles(dir)
	if len(pids) != 0 {
		t.Errorf("expected 0 PID files after kill, got %d", len(pids))
	}
}

func TestExecuteRecoveryResume(t *testing.T) {
	dir := t.TempDir()
	store := feature.NewStore(dir)
	cfg := config.NewDefault()
	fm := feature.NewManager(store, cfg)

	// Create a feature in Implementing state
	f := &feature.Feature{
		ID:            "feat-resume",
		Name:          "Resume Test",
		Slug:          "resume-test",
		Status:        feature.StatusImplementing,
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	_ = store.Save(f)

	pidDir := filepath.Join(dir, "feat-resume")
	_ = WritePIDFile(pidDir, PIDFile{PID: 999999999, FeatureID: "feat-resume", Phase: "implement", Dir: pidDir})

	items := []RecoveryItem{
		{
			PIDFile:      PIDFile{PID: 999999999, FeatureID: "feat-resume", Phase: "implement", Dir: pidDir},
			ProcessAlive: false,
			Feature:      f,
		},
	}

	actions := map[string]RecoveryAction{
		"feat-resume": RecoveryResume,
	}

	err := ExecuteRecovery(items, actions, fm)
	if err != nil {
		t.Fatalf("execute recovery: %v", err)
	}

	// Feature should be transitioned back to Implementing for restart
	// (via Interrupted → Implementing two-step)
	updated, _ := fm.Get("feat-resume")
	if updated.Status != feature.StatusImplementing {
		t.Errorf("status = %v, want Implementing", updated.Status)
	}

	// PID file should be removed
	pids, _ := FindPIDFiles(dir)
	if len(pids) != 0 {
		t.Errorf("expected 0 PID files after resume, got %d", len(pids))
	}
}

func TestExecuteRecoverySkipDeadProcess(t *testing.T) {
	dir := t.TempDir()

	pidDir := filepath.Join(dir, "feat-skip")
	_ = WritePIDFile(pidDir, PIDFile{PID: 999999999, FeatureID: "feat-skip", Phase: "implement", Dir: pidDir})

	items := []RecoveryItem{
		{
			PIDFile:      PIDFile{PID: 999999999, FeatureID: "feat-skip", Phase: "implement", Dir: pidDir},
			ProcessAlive: false,
		},
	}

	actions := map[string]RecoveryAction{
		"feat-skip": RecoverySkip,
	}

	err := ExecuteRecovery(items, actions, nil)
	if err != nil {
		t.Fatalf("execute recovery: %v", err)
	}

	// PID file should be removed since process is dead
	pids, _ := FindPIDFiles(dir)
	if len(pids) != 0 {
		t.Errorf("expected 0 PID files after skip (dead process), got %d", len(pids))
	}
}

func TestRecoveryActionKey(t *testing.T) {
	tests := []struct {
		name      string
		featureID string
		repoName  string
		want      string
	}{
		{name: "empty repo name", featureID: "feat-1", repoName: "", want: "feat-1"},
		{name: "with repo name", featureID: "feat-1", repoName: "service-a", want: "feat-1:service-a"},
		{name: "different feature and repo", featureID: "feat-2", repoName: "backend", want: "feat-2:backend"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RecoveryActionKey(tt.featureID, tt.repoName)
			if got != tt.want {
				t.Errorf("RecoveryActionKey(%q, %q) = %q, want %q", tt.featureID, tt.repoName, got, tt.want)
			}
		})
	}
}

func TestScanForRecoveryMultipleRepoSessions(t *testing.T) {
	dir := t.TempDir()

	// Create feature dir with two repo-specific PID files
	featDir := filepath.Join(dir, "feat-multi")
	_ = WritePIDFile(featDir, PIDFile{PID: 999999999, FeatureID: "feat-multi", Phase: "implement", RepoName: "service-a"})
	_ = WritePIDFile(featDir, PIDFile{PID: 999999998, FeatureID: "feat-multi", Phase: "implement", RepoName: "service-b"})

	items, err := ScanForRecovery(dir, nil)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}

	// Verify both items have correct RepoName values
	repoNames := map[string]bool{}
	for _, item := range items {
		repoNames[item.RepoName] = true
	}
	if !repoNames["service-a"] {
		t.Error("expected to find recovery item with RepoName=service-a")
	}
	if !repoNames["service-b"] {
		t.Error("expected to find recovery item with RepoName=service-b")
	}
}

func TestExecuteRecoveryKillMultiRepo(t *testing.T) {
	dir := t.TempDir()
	store := feature.NewStore(dir)
	cfg := config.NewDefault()
	fm := feature.NewManager(store, cfg)

	f := &feature.Feature{
		ID:            "feat-mr-kill",
		Name:          "Multi Repo Kill",
		Slug:          "mr-kill",
		Status:        feature.StatusImplementing,
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	_ = store.Save(f)

	pidDir := filepath.Join(dir, "feat-mr-kill")
	// Write two repo PID files
	_ = WritePIDFile(pidDir, PIDFile{PID: 999999999, FeatureID: "feat-mr-kill", Phase: "implement", Dir: pidDir, RepoName: "service-a"})
	_ = WritePIDFile(pidDir, PIDFile{PID: 999999998, FeatureID: "feat-mr-kill", Phase: "implement", Dir: pidDir, RepoName: "service-b"})

	// Kill only service-a
	items := []RecoveryItem{
		{
			PIDFile:      PIDFile{PID: 999999999, FeatureID: "feat-mr-kill", Phase: "implement", Dir: pidDir, RepoName: "service-a"},
			ProcessAlive: false,
			Feature:      f,
			RepoName:     "service-a",
		},
	}

	actions := map[string]RecoveryAction{
		RecoveryActionKey("feat-mr-kill", "service-a"): RecoveryKill,
	}

	err := ExecuteRecovery(items, actions, fm)
	if err != nil {
		t.Fatalf("execute recovery: %v", err)
	}

	// service-a PID file should be removed, service-b should still exist
	pids, _ := FindPIDFiles(dir)
	if len(pids) != 1 {
		t.Fatalf("expected 1 PID file remaining, got %d", len(pids))
	}
	if pids[0].RepoName != "service-b" {
		t.Errorf("remaining PID file RepoName = %q, want %q", pids[0].RepoName, "service-b")
	}
}

func TestExecuteRecoveryKillLiveProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("process-backed recovery kill extended regression")
	}

	pid, cmd := startProcessGroup(t)

	dir := t.TempDir()
	store := feature.NewStore(dir)
	cfg := config.NewDefault()
	fm := feature.NewManager(store, cfg)

	f := &feature.Feature{
		ID:            "feat-live-kill",
		Name:          "Live Kill Test",
		Slug:          "live-kill",
		Status:        feature.StatusImplementing,
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	_ = store.Save(f)

	pidDir := filepath.Join(dir, "feat-live-kill")
	_ = WritePIDFile(pidDir, PIDFile{PID: pid, FeatureID: "feat-live-kill", Phase: "implement", Dir: pidDir})

	items := []RecoveryItem{
		{
			PIDFile:      PIDFile{PID: pid, FeatureID: "feat-live-kill", Phase: "implement", Dir: pidDir},
			ProcessAlive: true,
			Feature:      f,
		},
	}

	actions := map[string]RecoveryAction{
		"feat-live-kill": RecoveryKill,
	}

	err := ExecuteRecovery(items, actions, fm)
	if err != nil {
		t.Fatalf("execute recovery: %v", err)
	}

	// Reap the child (in production, init/launchd does this for orphaned processes).
	_ = cmd.Wait()

	// Process must be dead after ExecuteRecovery returns.
	if isProcessRunning(pid) {
		t.Error("process should be dead after RecoveryKill, but it is still running")
	}

	// Feature should be Interrupted.
	updated, _ := fm.Get("feat-live-kill")
	if updated.Status != feature.StatusInterrupted {
		t.Errorf("status = %v, want Interrupted", updated.Status)
	}

	// PID file should be removed.
	pids, _ := FindPIDFiles(dir)
	if len(pids) != 0 {
		t.Errorf("expected 0 PID files after kill, got %d", len(pids))
	}
}

func TestTerminateProcessGroup(t *testing.T) {
	// parallel-exempt: process-group cleanup representative for the fast suite.
	pid, cmd := startProcessGroup(t)

	// Confirm the process is alive before termination.
	if !isProcessRunning(pid) {
		t.Fatal("process should be alive before terminateProcessGroup")
	}

	start := time.Now()
	terminateProcessGroup(pid)
	elapsed := time.Since(start)

	// Reap the child (in production, init/launchd does this for orphaned processes).
	_ = cmd.Wait()

	// Should have returned quickly (well under the 5s SIGTERM deadline).
	if elapsed > 3*time.Second {
		t.Errorf("terminateProcessGroup took %v; expected < 3s for a cooperating process", elapsed)
	}

	// Process must be dead.
	if isProcessRunning(pid) {
		t.Error("process should be dead after terminateProcessGroup")
	}
}

// TestTerminateProcessGroup_ChildOutlivesLeader verifies that terminateProcessGroup
// waits for ALL members of the process group to die, not just the leader. This is
// a regression test for the case where the leader exits first and a child in the
// same process group outlives it.
func TestTerminateProcessGroup_ChildOutlivesLeader(t *testing.T) {
	if testing.Short() {
		t.Skip("process-group child-lifetime extended regression")
	}

	// Start a shell in its own process group that spawns a background child
	// and then immediately exits. The child (sleep 60) inherits the PGID and
	// stays alive after the leader (sh) exits.
	cmd := exec.Command("sh", "-c", "sleep 60 & exit 0")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start process group: %v", err)
	}
	pgid := cmd.Process.Pid

	t.Cleanup(func() {
		// Best-effort: kill any surviving group members if the test fails.
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		// cmd.Wait() already called below; no-op if already reaped.
	})

	// Wait for the leader (sh) to exit. The child (sleep 60) is still alive.
	if err := cmd.Wait(); err != nil {
		t.Fatalf("leader did not exit cleanly: %v", err)
	}

	// Leader is dead.
	if isProcessRunning(pgid) {
		t.Fatal("leader should be dead after cmd.Wait()")
	}

	// Child must still be alive in the same process group.
	if err := syscall.Kill(-pgid, 0); err != nil {
		t.Fatalf("expected process group to have live members, but kill(-pgid, 0) returned: %v", err)
	}

	// Terminate the process group — must wait for the child too, not just the leader.
	start := time.Now()
	terminateProcessGroup(pgid)
	elapsed := time.Since(start)

	// Should return quickly (well under the 5s SIGTERM deadline).
	if elapsed > 3*time.Second {
		t.Errorf("terminateProcessGroup took %v; expected < 3s", elapsed)
	}

	// Entire process group must be dead — no orphaned children.
	if err := syscall.Kill(-pgid, 0); err == nil {
		t.Error("process group should be dead after terminateProcessGroup, but kill(-pgid, 0) succeeded")
	}
}

func TestCleanupStalePIDFilesMultiRepo(t *testing.T) {
	dir := t.TempDir()

	// Create stale PID files for two repos
	featDir := filepath.Join(dir, "feat-cleanup")
	_ = WritePIDFile(featDir, PIDFile{PID: 999999999, FeatureID: "feat-cleanup", RepoName: "service-a"})
	_ = WritePIDFile(featDir, PIDFile{PID: 999999998, FeatureID: "feat-cleanup", RepoName: "service-b"})

	err := cleanupStalePIDFiles(dir)
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	// Both PID files should be removed
	pids, _ := FindPIDFiles(dir)
	if len(pids) != 0 {
		t.Errorf("expected 0 PID files after cleanup, got %d", len(pids))
	}
}
