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
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

func initCleanlinessRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", "main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("ignored.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "initial")
	return dir
}

func TestInspectCleanlinessCategorizesChanges(t *testing.T) {
	dir := initCleanlinessRepo(t)

	// Staged change.
	if err := os.WriteFile(filepath.Join(dir, "staged.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "-C", dir, "add", "staged.txt")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	// Unstaged change to a tracked file.
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Untracked and ignored files.
	for _, name := range []string{"untracked.txt", "ignored.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	wm := NewWorktreeManager(t.TempDir())
	report, err := wm.InspectCleanliness(dir, 50)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if !report.Dirty() {
		t.Fatal("expected dirty report")
	}
	if len(report.Staged) != 1 || report.Staged[0] != "staged.txt" || report.StagedTotal != 1 {
		t.Errorf("staged = %v total=%d", report.Staged, report.StagedTotal)
	}
	if len(report.Unstaged) != 1 || report.Unstaged[0] != "tracked.txt" || report.UnstagedTotal != 1 {
		t.Errorf("unstaged = %v total=%d", report.Unstaged, report.UnstagedTotal)
	}
	if len(report.Untracked) != 1 || report.Untracked[0] != "untracked.txt" || report.UntrackedTotal != 1 {
		t.Errorf("untracked = %v total=%d (ignored.txt must be absent)", report.Untracked, report.UntrackedTotal)
	}
}

func TestInspectCleanlinessCleanRepo(t *testing.T) {
	dir := initCleanlinessRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "ignored.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wm := NewWorktreeManager(t.TempDir())
	report, err := wm.InspectCleanliness(dir, 50)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if report.Dirty() {
		t.Fatalf("clean repo reported dirty: %+v", report)
	}
}

func TestInspectCleanlinessTruncatesWithTotals(t *testing.T) {
	dir := initCleanlinessRepo(t)
	for i := 0; i < 5; i++ {
		if err := os.WriteFile(filepath.Join(dir, string(rune('a'+i))+".txt"), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	wm := NewWorktreeManager(t.TempDir())
	report, err := wm.InspectCleanliness(dir, 2)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if report.UntrackedTotal != 5 {
		t.Fatalf("untracked total = %d, want 5", report.UntrackedTotal)
	}
	if len(report.Untracked) != 2 {
		t.Fatalf("untracked list = %v, want bounded to 2", report.Untracked)
	}
}

func TestInspectCleanlinessExpandsNestedUntrackedDirs(t *testing.T) {
	dir := initCleanlinessRepo(t)
	files := []string{
		"nested/a.txt",
		"nested/b.txt",
		"nested/deep/c.txt",
		"nested/deep/deeper/d.txt",
		"nested/deep/deeper/e.txt",
	}
	for _, rel := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	wm := NewWorktreeManager(t.TempDir())
	report, err := wm.InspectCleanliness(dir, 50)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if report.UntrackedTotal != len(files) {
		t.Fatalf("untracked total = %d, want %d; list=%v", report.UntrackedTotal, len(files), report.Untracked)
	}
	for _, path := range report.Untracked {
		if len(path) > 0 && path[len(path)-1] == '/' {
			t.Errorf("collapsed directory entry %q in untracked list %v", path, report.Untracked)
		}
	}
	for _, rel := range files {
		if !slices.Contains(report.Untracked, rel) {
			t.Errorf("untracked = %v, missing %q", report.Untracked, rel)
		}
	}
}
