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

package selfupdate

import (
	"os"
	"path/filepath"
	"testing"
)

// writeExecutableFile writes content with an exact mode (chmod defeats the
// process umask so tests assert exact permission bits).
func writeExecutableFile(t *testing.T, dir, name string, mode os.FileMode, content []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, content, mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
	return path
}

// mustCanonical resolves symlinks so assertions survive symlinked temp roots
// (e.g. /var -> /private/var on macOS).
func mustCanonical(t *testing.T, path string) string {
	t.Helper()
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("canonicalize %s: %v", path, err)
	}
	return canonical
}

func mustDigest(t *testing.T, path string) string {
	t.Helper()
	digest, err := DigestFile(path)
	if err != nil {
		t.Fatalf("digest %s: %v", path, err)
	}
	return digest
}

// installExecutable creates an isolated installed-binary copy and captures
// its identity.
func installExecutable(t *testing.T, content string) Executable {
	t.Helper()
	path := writeExecutableFile(t, t.TempDir(), "agentico", 0o755, []byte(content))
	exec, err := CaptureExecutableAt(path)
	if err != nil {
		t.Fatalf("capture executable: %v", err)
	}
	return exec
}

// replaceFile simulates an external atomic replacement of path.
func replaceFile(t *testing.T, path, content string) {
	t.Helper()
	dir := filepath.Dir(path)
	tmp := writeExecutableFile(t, dir, ".replacement-tmp", 0o755, []byte(content))
	if err := os.Rename(tmp, path); err != nil {
		t.Fatalf("rename replacement over %s: %v", path, err)
	}
}

func TestStatFileRequiresRegularFile(t *testing.T) {
	dir := t.TempDir()

	if _, err := StatFile(dir); err == nil {
		t.Fatal("StatFile on directory: expected error")
	}
	if _, err := StatFile(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("StatFile on missing file: expected error")
	}
	path := writeExecutableFile(t, dir, "regular", 0o644, []byte("x"))
	id, err := StatFile(path)
	if err != nil {
		t.Fatalf("StatFile regular: %v", err)
	}
	if id.Mode != 0o644 || id.Size != 1 {
		t.Fatalf("identity = %+v, want mode 0644 size 1", id)
	}
	if id.UID != os.Geteuid() {
		t.Fatalf("uid = %d, want euid %d", id.UID, os.Geteuid())
	}
}

func TestCaptureExecutableAtRoundTrip(t *testing.T) {
	content := "installed-v1-bytes"
	exec := installExecutable(t, content)

	if exec.Path != mustCanonical(t, exec.Path) {
		t.Fatalf("path %s is not canonical", exec.Path)
	}
	if exec.ID.Mode != 0o755 {
		t.Fatalf("mode = %o, want 755", exec.ID.Mode)
	}
	if exec.ID.Size != int64(len(content)) {
		t.Fatalf("size = %d, want %d", exec.ID.Size, len(content))
	}
	if exec.Digest != mustDigest(t, exec.Path) {
		t.Fatalf("digest %s does not match fresh digest", exec.Digest)
	}
	matches, err := exec.PathStillMatches()
	if err != nil || !matches {
		t.Fatalf("PathStillMatches = (%v, %v), want (true, nil)", matches, err)
	}
}

func TestCaptureExecutableOfRunningBinary(t *testing.T) {
	exec, err := CaptureExecutable()
	if err != nil {
		t.Fatalf("CaptureExecutable: %v", err)
	}
	if exec.Path == "" || exec.Digest == "" || exec.ID.Ino == 0 {
		t.Fatalf("incomplete capture: %+v", exec)
	}
	matches, err := exec.PathStillMatches()
	if err != nil || !matches {
		t.Fatalf("PathStillMatches = (%v, %v), want (true, nil)", matches, err)
	}
}

func TestPathStillMatchesFalseAfterReplacement(t *testing.T) {
	exec := installExecutable(t, "v1")
	replaceFile(t, exec.Path, "v2-replacement")
	matches, err := exec.PathStillMatches()
	if err != nil {
		t.Fatalf("PathStillMatches: %v", err)
	}
	if matches {
		t.Fatal("PathStillMatches = true after inode replacement, want false")
	}
}

func TestPathStillMatchesSurvivesChmod(t *testing.T) {
	exec := installExecutable(t, "v1")
	if err := os.Chmod(exec.Path, 0o700); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	matches, err := exec.PathStillMatches()
	if err != nil || !matches {
		t.Fatalf("PathStillMatches = (%v, %v), want (true, nil): chmod alone must not invalidate identity", matches, err)
	}
}

func TestDigestFileDiffersByContent(t *testing.T) {
	dir := t.TempDir()
	a := writeExecutableFile(t, dir, "a", 0o644, []byte("one"))
	b := writeExecutableFile(t, dir, "b", 0o644, []byte("two"))
	if mustDigest(t, a) == mustDigest(t, b) {
		t.Fatal("distinct contents produced identical digests")
	}
	if _, err := DigestFile(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("DigestFile on missing file: expected error")
	}
}
