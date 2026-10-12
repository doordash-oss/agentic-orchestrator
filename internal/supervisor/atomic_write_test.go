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

package supervisor

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func skipIfNoPermBits(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are not enforced on Windows")
	}
}

func statPerm(t *testing.T, path string) fs.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

// assertDirPerm compares against a probe directory created with the same
// mode, because MkdirAll applies the process umask.
func assertDirPerm(t *testing.T, dir string, mode fs.FileMode) {
	t.Helper()
	probe := filepath.Join(t.TempDir(), "probe")
	if err := os.MkdirAll(probe, mode); err != nil {
		t.Fatal(err)
	}
	if got, want := statPerm(t, dir), statPerm(t, probe); got != want {
		t.Fatalf("dir %s mode = %o, want the MkdirAll(%o) result %o", dir, got, mode, want)
	}
}

func assertNoTempLeft(t *testing.T, dir, base string) {
	t.Helper()
	left, err := filepath.Glob(filepath.Join(dir, "."+base+".tmp-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Fatalf("leftover temp files: %v", left)
	}
}

func TestWriteFileAtomicConverterModeSet(t *testing.T) {
	skipIfNoPermBits(t)
	root := t.TempDir()
	path := filepath.Join(root, "projects", "-work-repo-app", "session.jsonl")
	if err := WriteFileAtomic(path, []byte("line\n"), 0o700, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "line\n" {
		t.Fatalf("file content = %q, %v", got, err)
	}
	assertDirPerm(t, filepath.Dir(path), 0o700)
	if perm := statPerm(t, path); perm != 0o600 {
		t.Fatalf("file mode = %o, want 0600 (chmod'ed before the write)", perm)
	}
	assertNoTempLeft(t, filepath.Dir(path), "session.jsonl")
}

func TestWriteFileAtomicPersistenceModeSet(t *testing.T) {
	skipIfNoPermBits(t)
	root := t.TempDir()
	path := filepath.Join(root, "nested", "settings.json")
	if err := WriteFileAtomic(path, []byte("{}\n"), 0o755, 0); err != nil {
		t.Fatal(err)
	}
	assertDirPerm(t, filepath.Dir(path), 0o755)
	// No chmod: the file keeps the temp file's own 0600.
	if perm := statPerm(t, path); perm != 0o600 {
		t.Fatalf("file mode = %o, want 0600 (never chmod'ed)", perm)
	}
	assertNoTempLeft(t, filepath.Dir(path), "settings.json")
}

func TestWriteFileAtomicReplacesExistingFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "state.json")
	if err := os.WriteFile(path, []byte("stale longer content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(path, []byte("new\n"), 0o755, 0); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "new\n" {
		t.Fatalf("file content = %q, %v", got, err)
	}
	assertNoTempLeft(t, root, "state.json")
}

func TestWriteFileAtomicCleansUpTempFileOnFailedRename(t *testing.T) {
	dir := t.TempDir()
	// A directory standing where the file should land makes the rename
	// fail after the temp file was written, fsynced and closed.
	target := filepath.Join(dir, "state.json")
	if err := os.MkdirAll(filepath.Join(target, "occupied"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(target, []byte("x"), 0o755, 0o600); err == nil {
		t.Fatal("rename onto a directory succeeded, want failure")
	}
	assertNoTempLeft(t, dir, "state.json")
	if entries, err := os.ReadDir(target); err != nil || len(entries) != 1 || entries[0].Name() != "occupied" {
		t.Fatalf("blocked target changed: %v, %v", entries, err)
	}
}
