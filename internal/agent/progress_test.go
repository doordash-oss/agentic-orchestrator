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

package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestFingerprint(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "progress.md")

	// Non-existent file returns empty
	fp, err := Fingerprint(path)
	if err != nil {
		t.Fatalf("fingerprint non-existent: %v", err)
	}
	if fp != "" {
		t.Errorf("expected empty fingerprint for non-existent file, got %s", fp)
	}

	// Write content
	os.WriteFile(path, []byte("step 1 done\n"), 0o644)
	fp1, err := Fingerprint(path)
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	if fp1 == "" {
		t.Error("expected non-empty fingerprint")
	}

	// Same content = same fingerprint
	fp2, _ := Fingerprint(path)
	if fp1 != fp2 {
		t.Error("expected same fingerprint for same content")
	}

	// Different content = different fingerprint
	os.WriteFile(path, []byte("step 2 done\n"), 0o644)
	fp3, _ := Fingerprint(path)
	if fp1 == fp3 {
		t.Error("expected different fingerprint for different content")
	}
}

func TestProgressTracker(t *testing.T) {
	pt := NewProgressTracker()

	made := pt.ObserveVerifiedOutcome(3)
	if !made {
		t.Fatal("first verified outcome should establish progress baseline")
	}
	made = pt.ObserveVerifiedOutcome(3)
	if made {
		t.Fatal("same blocker count should not count as progress")
	}
	if pt.NoProgressCount() != 1 {
		t.Fatalf("NoProgressCount = %d, want 1", pt.NoProgressCount())
	}
	made = pt.ObserveVerifiedOutcome(2)
	if !made {
		t.Fatal("lower blocker count should count as progress")
	}
	if pt.NoProgressCount() != 0 {
		t.Fatalf("NoProgressCount = %d, want reset to 0", pt.NoProgressCount())
	}
	made = pt.ObserveVerifiedOutcome(3)
	if made {
		t.Fatal("regressing blocker count should not count as progress")
	}
	made = pt.ObserveUnverifiedOutcome()
	if made {
		t.Fatal("unverified outcome should not count as progress")
	}
	if pt.NoProgressCount() != 2 {
		t.Fatalf("NoProgressCount = %d, want 2", pt.NoProgressCount())
	}
}

func TestProgressTrackerRetryOutcome(t *testing.T) {
	pt := NewProgressTracker()

	if !pt.ObserveRetryOutcome("n1", "w1") {
		t.Fatal("first RETRY observation should establish a baseline")
	}
	if pt.ObserveRetryOutcome("n1", "w1") {
		t.Fatal("RETRY with unchanged narrative and worktree should not count as progress")
	}
	if pt.NoProgressCount() != 1 {
		t.Fatalf("NoProgressCount = %d, want 1", pt.NoProgressCount())
	}
	if !pt.ObserveRetryOutcome("n1", "w2") {
		t.Fatal("RETRY with changed worktree should count as progress")
	}
	if pt.NoProgressCount() != 1 {
		t.Fatalf("NoProgressCount = %d, want 1 (progressing RETRY must not reset the verified-outcome counter)", pt.NoProgressCount())
	}
	if !pt.ObserveRetryOutcome("n2", "w2") {
		t.Fatal("RETRY with changed narrative should count as progress")
	}
	// Empty fingerprints (unreadable progress.md, git failure) must not
	// disarm the rail: treated as unchanged.
	if pt.ObserveRetryOutcome("", "") {
		t.Fatal("RETRY with no fingerprint signal should not count as progress")
	}
	if pt.ObserveRetryOutcome("", "") {
		t.Fatal("repeated signal-less RETRY should keep counting no-progress")
	}
	if pt.NoProgressCount() != 3 {
		t.Fatalf("NoProgressCount = %d, want 3", pt.NoProgressCount())
	}
}

func TestWorktreeStateFingerprint(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := t.TempDir()
	for _, args := range [][]string{
		{"init", repo},
		{"-C", repo, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "--allow-empty", "-m", "init"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}

	fp1 := WorktreeStateFingerprint(context.Background(), nil, []string{repo})
	if fp1 == "" {
		t.Fatal("expected a fingerprint for a valid repo")
	}
	if fp2 := WorktreeStateFingerprint(context.Background(), nil, []string{repo}); fp2 != fp1 {
		t.Fatal("fingerprint should be stable for an unchanged worktree")
	}

	// A new (untracked) file must change the fingerprint.
	if err := os.WriteFile(filepath.Join(repo, "new.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	fp3 := WorktreeStateFingerprint(context.Background(), nil, []string{repo})
	if fp3 == fp1 {
		t.Fatal("fingerprint should change when an untracked file is added")
	}

	// Editing the still-untracked file must also change it (invisible to
	// `git diff HEAD`, covered by the stat pass).
	if err := os.WriteFile(filepath.Join(repo, "new.txt"), []byte("ab"), 0o644); err != nil {
		t.Fatal(err)
	}
	if fp4 := WorktreeStateFingerprint(context.Background(), nil, []string{repo}); fp4 == fp3 {
		t.Fatal("fingerprint should change when an untracked file is edited")
	}

	// A non-repo path yields no signal.
	if fp := WorktreeStateFingerprint(context.Background(), nil, []string{t.TempDir()}); fp != "" {
		t.Fatalf("WorktreeStateFingerprint(non-repo) = %q, want empty", fp)
	}
}

func TestCountBlockingReviewFindings(t *testing.T) {
	feedback := strings.Join([]string{
		"- **Critical**: first",
		"- **[High]**: second",
		"- **Medium**: ignored",
		"* **high**: third",
	}, "\n")
	if got := CountBlockingReviewFindings(feedback); got != 3 {
		t.Fatalf("CountBlockingReviewFindings() = %d, want 3", got)
	}
}
