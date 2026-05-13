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
	"os"
	"path/filepath"
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
	dir := t.TempDir()
	path := filepath.Join(dir, "progress.md")

	pt := NewProgressTracker()

	// First check with no file — always "progress" (initial)
	made, err := pt.Check(path)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !made {
		t.Error("expected progress on first check")
	}

	// Same empty state — no progress
	made, _ = pt.Check(path)
	if made {
		t.Error("expected no progress on unchanged file")
	}
	if pt.NoProgressCount() != 1 {
		t.Errorf("expected no-progress count 1, got %d", pt.NoProgressCount())
	}

	// Write content — progress
	os.WriteFile(path, []byte("step 1\n"), 0o644)
	made, _ = pt.Check(path)
	if !made {
		t.Error("expected progress after content change")
	}
	if pt.NoProgressCount() != 0 {
		t.Errorf("expected no-progress count reset to 0, got %d", pt.NoProgressCount())
	}
}
