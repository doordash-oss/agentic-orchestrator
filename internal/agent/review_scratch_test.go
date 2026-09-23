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

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

func TestPruneIdleFeatureReviewScratch_SkipsFeaturesThatMayOwnAReview(t *testing.T) {
	t.Parallel()
	store := feature.NewStore(t.TempDir())
	fm := feature.NewManager(store, nil)

	tests := []struct {
		id        string
		status    feature.Status
		busy      bool
		wantPrune bool
	}{
		{id: "done", status: feature.StatusDone, wantPrune: true},
		{id: "published", status: feature.StatusPublished, wantPrune: true},
		{id: "final-reviewing", status: feature.StatusFinalReviewing},
		{id: "implementing", status: feature.StatusImplementing},
		{id: "reviewing", status: feature.StatusReviewing},
		{id: "orphan", status: feature.StatusCodeReady, busy: true},
	}
	var busyIDs []string
	for _, tt := range tests {
		f := &feature.Feature{ID: tt.id, Name: tt.id, Slug: tt.id, Status: tt.status, SchemaVersion: feature.SchemaVersionCurrent}
		if err := store.Save(f); err != nil {
			t.Fatalf("Save(%s) error = %v", tt.id, err)
		}
		helperDir := reviewScratchHelperDir(store, tt.id)
		for _, dir := range []string{"evidence", "tmp", "build-cache"} {
			if err := os.MkdirAll(filepath.Join(helperDir, dir), 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
		}
		if err := os.WriteFile(filepath.Join(helperDir, "review-prompt.md"), []byte("x"), 0o644); err != nil {
			t.Fatalf("write prompt: %v", err)
		}
		if tt.busy {
			busyIDs = append(busyIDs, tt.id)
		}
	}

	PruneIdleFeatureReviewScratch(fm, busyIDs)

	for _, tt := range tests {
		helperDir := reviewScratchHelperDir(store, tt.id)
		_, err := os.Stat(filepath.Join(helperDir, "tmp"))
		if pruned := os.IsNotExist(err); pruned != tt.wantPrune {
			t.Errorf("%s: tmp pruned = %v (stat err %v), want %v", tt.id, pruned, err, tt.wantPrune)
		}
		if _, err := os.Stat(filepath.Join(helperDir, "evidence")); err != nil {
			t.Errorf("%s: evidence stat = %v; want kept", tt.id, err)
		}
	}
}

func reviewScratchHelperDir(store *feature.Store, id string) string {
	return filepath.Join(store.BaseDir, id, "runs", "run-001", "review", "iteration-01", "qa")
}

func TestPruneLiveRunReviewScratch_RemovesCacheAndTempButKeepsEvidence(t *testing.T) {
	t.Parallel()
	featureDir := t.TempDir()
	helperDir := filepath.Join(featureDir, "runs", "run-001", "review", "iteration-02", "qa")
	// A tmp dir outside a live-run helper layout is not scratch.
	otherTmp := filepath.Join(featureDir, "runs", "run-001", "implement", "tmp")
	modDir := filepath.Join(helperDir, "build-cache", "go-mod", "example.com", "m@v1.0.0")
	for _, dir := range []string{
		filepath.Join(helperDir, "evidence"),
		filepath.Join(helperDir, "tmp", "playwright"),
		modDir,
		otherTmp,
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	for _, path := range []string{
		filepath.Join(helperDir, "review-prompt.md"),
		filepath.Join(helperDir, "review-feedback.md"),
		filepath.Join(helperDir, "evidence", "home.png"),
		filepath.Join(helperDir, "tmp", "playwright", "trace.zip"),
		filepath.Join(modDir, "go.mod"),
		filepath.Join(otherTmp, "keep.txt"),
	} {
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	if err := os.Chmod(modDir, 0o555); err != nil {
		t.Fatalf("chmod module cache: %v", err)
	}

	if err := PruneLiveRunReviewScratch(featureDir); err != nil {
		t.Fatalf("PruneLiveRunReviewScratch() error = %v", err)
	}

	for _, gone := range []string{filepath.Join(helperDir, "build-cache"), filepath.Join(helperDir, "tmp")} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("%s stat = %v; want removed", gone, err)
		}
	}
	for _, kept := range []string{
		filepath.Join(helperDir, "review-feedback.md"),
		filepath.Join(helperDir, "evidence", "home.png"),
		filepath.Join(otherTmp, "keep.txt"),
	} {
		if _, err := os.Stat(kept); err != nil {
			t.Errorf("%s stat = %v; want kept", kept, err)
		}
	}
	if err := PruneLiveRunReviewScratch(filepath.Join(featureDir, "missing")); err != nil {
		t.Errorf("PruneLiveRunReviewScratch(missing) error = %v; want nil", err)
	}
}
