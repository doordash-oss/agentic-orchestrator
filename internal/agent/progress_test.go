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
