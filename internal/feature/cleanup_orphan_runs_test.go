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

package feature

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// writeRunYAML writes a run.yaml directly to disk without going through
// Store.SaveRun, so tests can fabricate sealed / committing / ordinary run
// states that Store.SaveRun would refuse or rewrite.
func writeRunYAML(t *testing.T, baseDir, featureID string, r *Run) {
	t.Helper()
	dir := filepath.Join(baseDir, featureID, "runs", RunDirName(r.RunNumber))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	data, err := yaml.Marshal(r)
	if err != nil {
		t.Fatalf("marshal run-%d: %v", r.RunNumber, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "run.yaml"), data, 0o644); err != nil {
		t.Fatalf("write run-%d yaml: %v", r.RunNumber, err)
	}
}

// writeFeatureYAMLRaw writes feature.yaml directly, bypassing store.Save's
// defensive ActiveRun seeding and syncShadowsToRun side effects. Used by
// the cleanup tests to fabricate states like "ActiveRun points at a run
// that was deleted out from under it". Stamps SchemaVersion=2 by default
// so the strict-schema Load guard accepts the fixture; pass an explicit
// non-zero SchemaVersion to override.
func writeFeatureYAMLRaw(t *testing.T, baseDir string, f *Feature) {
	t.Helper()
	if f.SchemaVersion == 0 {
		f.SchemaVersion = SchemaVersionCurrent
	}
	dir := filepath.Join(baseDir, f.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	data, err := yaml.Marshal(f)
	if err != nil {
		t.Fatalf("marshal feature: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "feature.yaml"), data, 0o644); err != nil {
		t.Fatalf("write feature.yaml: %v", err)
	}
}

// readFeatureYAMLRaw reads feature.yaml directly (no run.yaml load), useful
// for asserting on reconciled ActiveRun/RunCount values post-cleanup without
// tripping loadUnlocked's active-run load (which would fail if cleanup
// deleted the active run).
func readFeatureYAMLRaw(t *testing.T, baseDir, id string) *Feature {
	t.Helper()
	path := filepath.Join(baseDir, id, "feature.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read feature.yaml: %v", err)
	}
	var f Feature
	if err := yaml.Unmarshal(data, &f); err != nil {
		t.Fatalf("unmarshal feature.yaml: %v", err)
	}
	return &f
}

func TestStore_CleanupOrphanRuns(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	tests := []struct {
		name        string
		id          string
		setup       func(t *testing.T, dir string, store *Store, id string)
		wantDeleted []int
		assert      func(t *testing.T, dir string, store *Store, id string)
	}{
		{
			name: "deletes run higher than ActiveRun",
			id:   "feat-stale-future",
			setup: func(t *testing.T, dir string, store *Store, id string) {
				f := &Feature{
					ID:            id,
					Name:          "Stale Future Test",
					Slug:          "stale-future",
					Description:   "test",
					Created:       time.Now(),
					Status:        StatusCreated,
					CurrentPhase:  PhaseInquire,
					SchemaVersion: SchemaVersionCurrent,
				}
				if err := store.Save(f); err != nil {
					t.Fatalf("save: %v", err)
				}
				if err := store.CreateRun(id, &Run{RunNumber: 2, CarriedFromRun: 1}); err != nil {
					t.Fatalf("create stale run-002: %v", err)
				}
			},
			wantDeleted: []int{2},
			assert: func(t *testing.T, dir string, store *Store, id string) {
				assertRunMissing(t, dir, id, 2)
				assertRunPresent(t, dir, id, 1)
				reloaded, err := store.Load(id)
				if err != nil {
					t.Fatalf("reload: %v", err)
				}
				assertFeatureRunNumbers(t, reloaded.ActiveRun, reloaded.RunCount, 1, 1)
			},
		},
		{
			name: "deletes committing skeleton and rolls back ActiveRun",
			id:   "feat-committing",
			setup: func(t *testing.T, dir string, store *Store, id string) {
				now := time.Now()
				sealedTarget := PhaseResearch
				writeFeatureYAMLRaw(t, dir, &Feature{
					ID:        id,
					Name:      "Committing Test",
					Slug:      "committing-test",
					Status:    StatusCreated,
					ActiveRun: 2,
					RunCount:  2,
				})
				writeRunYAML(t, dir, id, &Run{
					RunNumber:    1,
					SealedAt:     &now,
					SealReason:   SealReasonRewind,
					RewindTarget: &sealedTarget,
				})
				writeRunYAML(t, dir, id, &Run{
					RunNumber:      2,
					CarriedFromRun: 1,
					Committing:     true,
				})
			},
			wantDeleted: []int{2},
			assert: func(t *testing.T, dir string, store *Store, id string) {
				assertRunMissing(t, dir, id, 2)
				assertRunPresent(t, dir, id, 1)
				post := readFeatureYAMLRaw(t, dir, id)
				assertFeatureRunNumbers(t, post.ActiveRun, post.RunCount, 1, 1)

				deleted2, err2 := store.CleanupOrphanRuns(id)
				if err2 != nil {
					t.Fatalf("second CleanupOrphanRuns: %v", err2)
				}
				if deleted2 != nil {
					t.Errorf("second call deleted = %v, want nil", deleted2)
				}

				reloaded, err := store.Load(id)
				if err != nil {
					t.Fatalf("reload: %v", err)
				}
				assertFeatureRunNumbers(t, reloaded.ActiveRun, reloaded.RunCount, 1, 1)
				if reloaded.Run() == nil || !reloaded.Run().IsSealed() {
					t.Error("reloaded Run() is not sealed; want sealed run-001")
				}
			},
		},
		{
			name: "preserves non-committing active run",
			id:   "feat-non-committing",
			setup: func(t *testing.T, dir string, store *Store, id string) {
				now := time.Now()
				writeFeatureYAMLRaw(t, dir, &Feature{
					ID:        id,
					Name:      "Non-committing Test",
					Slug:      "non-committing",
					Status:    StatusCreated,
					ActiveRun: 2,
					RunCount:  2,
				})
				writeRunYAML(t, dir, id, &Run{
					RunNumber:  1,
					SealedAt:   &now,
					SealReason: SealReasonRewind,
				})
				writeRunYAML(t, dir, id, &Run{RunNumber: 2})
			},
			wantDeleted: nil,
			assert: func(t *testing.T, dir string, store *Store, id string) {
				assertRunPresent(t, dir, id, 2)
				post := readFeatureYAMLRaw(t, dir, id)
				assertFeatureRunNumbers(t, post.ActiveRun, post.RunCount, 2, 2)
			},
		},
		{
			name: "preserves sealed historical run and active run",
			id:   "feat-sealed-preserve",
			setup: func(t *testing.T, dir string, store *Store, id string) {
				now := time.Now()
				sealedTarget := PhasePlan
				writeFeatureYAMLRaw(t, dir, &Feature{
					ID:        id,
					Name:      "Sealed Preserve Test",
					Slug:      "sealed-preserve",
					Status:    StatusCreated,
					ActiveRun: 2,
					RunCount:  2,
				})
				writeRunYAML(t, dir, id, &Run{
					RunNumber:    1,
					SealedAt:     &now,
					SealReason:   SealReasonRewind,
					RewindTarget: &sealedTarget,
				})
				writeRunYAML(t, dir, id, &Run{RunNumber: 2})
			},
			wantDeleted: nil,
			assert: func(t *testing.T, dir string, store *Store, id string) {
				assertRunPresent(t, dir, id, 1)
				assertRunPresent(t, dir, id, 2)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			store := NewStore(dir)
			tt.setup(t, dir, store, tt.id)

			deleted, err := store.CleanupOrphanRuns(tt.id)
			if err != nil {
				t.Fatalf("CleanupOrphanRuns: %v", err)
			}
			if !intSlicesEqual(deleted, tt.wantDeleted) {
				t.Errorf("CleanupOrphanRuns() deleted = %v, want %v", deleted, tt.wantDeleted)
			}
			tt.assert(t, dir, store, tt.id)
		})
	}
}

func assertRunPresent(t *testing.T, dir, id string, runNumber int) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, id, "runs", RunDirName(runNumber), "run.yaml")); err != nil {
		t.Errorf("run-%03d run.yaml missing: %v", runNumber, err)
	}
}

func assertRunMissing(t *testing.T, dir, id string, runNumber int) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, id, "runs", RunDirName(runNumber))); !os.IsNotExist(err) {
		t.Errorf("run-%03d still exists: %v", runNumber, err)
	}
}

func assertFeatureRunNumbers(t *testing.T, gotActive, gotCount, wantActive, wantCount int) {
	t.Helper()
	if gotActive != wantActive || gotCount != wantCount {
		t.Errorf("ActiveRun/RunCount = %d/%d, want %d/%d", gotActive, gotCount, wantActive, wantCount)
	}
}

func intSlicesEqual(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestStore_CleanupOrphanRuns_EdgeCases(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	if testing.Short() {
		t.Skip("extended cleanup edge-case matrix; representative orphan cleanup remains in short mode")
	}

	t.Run("no_runs_dir", func(t *testing.T) {
		dir := t.TempDir()
		store := NewStore(dir)
		id := "feat-no-runs"
		// feature.yaml exists but no runs/ directory.
		writeFeatureYAMLRaw(t, dir, &Feature{
			ID:        id,
			Name:      "No Runs Test",
			Slug:      "no-runs",
			Status:    StatusCreated,
			ActiveRun: 1,
			RunCount:  1,
		})
		deleted, err := store.CleanupOrphanRuns(id)
		if err != nil {
			t.Fatalf("CleanupOrphanRuns: %v", err)
		}
		if deleted != nil {
			t.Errorf("deleted = %v, want nil", deleted)
		}
	})

	t.Run("no_feature_yaml", func(t *testing.T) {
		dir := t.TempDir()
		store := NewStore(dir)
		// Nothing on disk at all.
		deleted, err := store.CleanupOrphanRuns("does-not-exist")
		if err != nil {
			t.Fatalf("CleanupOrphanRuns: %v", err)
		}
		if deleted != nil {
			t.Errorf("deleted = %v, want nil", deleted)
		}
	})

	t.Run("pre_runs_layout_no_op", func(t *testing.T) {
		dir := t.TempDir()
		store := NewStore(dir)
		id := "feat-pre-runs"
		// ActiveRun:0 is the pre-migration signal.
		writeFeatureYAMLRaw(t, dir, &Feature{
			ID:        id,
			Name:      "Pre-runs Test",
			Slug:      "pre-runs",
			Status:    StatusCreated,
			ActiveRun: 0,
			RunCount:  0,
		})
		deleted, err := store.CleanupOrphanRuns(id)
		if err != nil {
			t.Fatalf("CleanupOrphanRuns: %v", err)
		}
		if deleted != nil {
			t.Errorf("deleted = %v, want nil (pre-runs no-op)", deleted)
		}
	})

	t.Run("unparseable_run_dir_name", func(t *testing.T) {
		dir := t.TempDir()
		store := NewStore(dir)
		id := "feat-bad-dirname"
		writeFeatureYAMLRaw(t, dir, &Feature{
			ID:        id,
			Name:      "Bad Dirname Test",
			Slug:      "bad-dirname",
			Status:    StatusCreated,
			ActiveRun: 1,
			RunCount:  1,
		})
		writeRunYAML(t, dir, id, &Run{RunNumber: 1})
		// Create a bogus run-abc dir.
		bogusDir := filepath.Join(dir, id, "runs", "run-abc")
		if err := os.MkdirAll(bogusDir, 0o755); err != nil {
			t.Fatalf("mkdir run-abc: %v", err)
		}
		deleted, err := store.CleanupOrphanRuns(id)
		if err != nil {
			t.Fatalf("CleanupOrphanRuns: %v", err)
		}
		if deleted != nil {
			t.Errorf("deleted = %v, want nil (unparseable ignored)", deleted)
		}
		// Bogus dir still present (ignored, not deleted).
		if _, err := os.Stat(bogusDir); err != nil {
			t.Errorf("run-abc deleted: %v", err)
		}
	})

	t.Run("unparseable_run_yaml_high_number_deleted", func(t *testing.T) {
		dir := t.TempDir()
		store := NewStore(dir)
		id := "feat-bad-yaml"
		writeFeatureYAMLRaw(t, dir, &Feature{
			ID:        id,
			Name:      "Bad YAML Test",
			Slug:      "bad-yaml",
			Status:    StatusCreated,
			ActiveRun: 2,
			RunCount:  2,
		})
		writeRunYAML(t, dir, id, &Run{RunNumber: 1, SealedAt: ptrTime(time.Now())})
		writeRunYAML(t, dir, id, &Run{RunNumber: 2})
		// Write a run-003 dir with corrupt YAML; since 3 > ActiveRun (2),
		// it's deleted by the first predicate regardless of YAML parse failure.
		corruptDir := filepath.Join(dir, id, "runs", "run-003")
		if err := os.MkdirAll(corruptDir, 0o755); err != nil {
			t.Fatalf("mkdir run-003: %v", err)
		}
		if err := os.WriteFile(filepath.Join(corruptDir, "run.yaml"), []byte("not: valid: yaml: at: all"), 0o644); err != nil {
			t.Fatalf("write corrupt run-003 yaml: %v", err)
		}
		deleted, err := store.CleanupOrphanRuns(id)
		if err != nil {
			t.Fatalf("CleanupOrphanRuns: %v", err)
		}
		if len(deleted) != 1 || deleted[0] != 3 {
			t.Errorf("deleted = %v, want [3]", deleted)
		}
	})

	t.Run("multiple_orphans", func(t *testing.T) {
		dir := t.TempDir()
		store := NewStore(dir)
		id := "feat-multi-orphan"
		writeFeatureYAMLRaw(t, dir, &Feature{
			ID:        id,
			Name:      "Multi Orphan Test",
			Slug:      "multi-orphan",
			Status:    StatusCreated,
			ActiveRun: 2,
			RunCount:  2,
		})
		writeRunYAML(t, dir, id, &Run{RunNumber: 1, SealedAt: ptrTime(time.Now())})
		writeRunYAML(t, dir, id, &Run{RunNumber: 2})
		writeRunYAML(t, dir, id, &Run{RunNumber: 3, Committing: true})
		writeRunYAML(t, dir, id, &Run{RunNumber: 4})

		deleted, err := store.CleanupOrphanRuns(id)
		if err != nil {
			t.Fatalf("CleanupOrphanRuns: %v", err)
		}
		if len(deleted) != 2 || deleted[0] != 3 || deleted[1] != 4 {
			t.Errorf("deleted = %v, want [3, 4] sorted", deleted)
		}
		// run-001 and run-002 preserved.
		if _, err := os.Stat(filepath.Join(dir, id, "runs", "run-001")); err != nil {
			t.Errorf("run-001 deleted: %v", err)
		}
		if _, err := os.Stat(filepath.Join(dir, id, "runs", "run-002")); err != nil {
			t.Errorf("run-002 deleted: %v", err)
		}
		// feature.yaml NOT rolled back (max-on-disk = 2 = ActiveRun).
		post := readFeatureYAMLRaw(t, dir, id)
		if post.ActiveRun != 2 || post.RunCount != 2 {
			t.Errorf("ActiveRun/RunCount = %d/%d, want 2/2 (no rollback)", post.ActiveRun, post.RunCount)
		}
	})

	t.Run("remove_all_error_aggregated", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("chmod-based permission error injection unreliable on Windows")
		}
		// On macOS / Linux, chmod 0o000 on a run dir's parent makes
		// os.RemoveAll fail on the inner entries.
		dir := t.TempDir()
		store := NewStore(dir)
		id := "feat-remove-err"
		writeFeatureYAMLRaw(t, dir, &Feature{
			ID:        id,
			Name:      "Remove Err Test",
			Slug:      "remove-err",
			Status:    StatusCreated,
			ActiveRun: 2,
			RunCount:  2,
		})
		writeRunYAML(t, dir, id, &Run{RunNumber: 1, SealedAt: ptrTime(time.Now())})
		writeRunYAML(t, dir, id, &Run{RunNumber: 2})
		writeRunYAML(t, dir, id, &Run{RunNumber: 3})

		// Make run-003 immutable. Root on CI will still delete it — skip in
		// that case.
		if os.Geteuid() == 0 {
			t.Skip("running as root; permission-based error injection ineffective")
		}
		run003Dir := filepath.Join(dir, id, "runs", "run-003")
		// Put an unreadable sub-dir inside run-003 so RemoveAll fails when
		// walking into it.
		inner := filepath.Join(run003Dir, "inner")
		if err := os.MkdirAll(inner, 0o755); err != nil {
			t.Fatalf("mkdir inner: %v", err)
		}
		if err := os.WriteFile(filepath.Join(inner, "file"), []byte("x"), 0o644); err != nil {
			t.Fatalf("write inner/file: %v", err)
		}
		// chmod the inner dir to no-perms so RemoveAll cannot delete its
		// contents.
		if err := os.Chmod(inner, 0o000); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		defer func() { _ = os.Chmod(inner, 0o755) }()

		deleted, err := store.CleanupOrphanRuns(id)
		if err == nil {
			// Some filesystems (tmpfs, docker overlay) may still allow removal
			// even with 0o000 permissions. If so, just assert deletion and move on.
			if len(deleted) != 1 || deleted[0] != 3 {
				t.Fatalf("deleted = %v, want [3] when no error", deleted)
			}
			return
		}
		// With error: run-003 is NOT in deleted (still on disk); error
		// wraps the removal failure for run-003.
		for _, d := range deleted {
			if d == 3 {
				t.Errorf("run-003 in deleted=%v despite removal failure", deleted)
			}
		}
		if !strings.Contains(err.Error(), "run-003") {
			t.Errorf("error = %q, want contains %q", err.Error(), "run-003")
		}
	})

	t.Run("reconciles_active_run_rollback_only_downward", func(t *testing.T) {
		dir := t.TempDir()
		store := NewStore(dir)
		id := "feat-rollback-down"
		// ActiveRun:2, but run-002 has committing:true (crashed skeleton).
		writeFeatureYAMLRaw(t, dir, &Feature{
			ID:        id,
			Name:      "Rollback Down Test",
			Slug:      "rollback-down",
			Status:    StatusCreated,
			ActiveRun: 2,
			RunCount:  2,
		})
		writeRunYAML(t, dir, id, &Run{RunNumber: 1, SealedAt: ptrTime(time.Now())})
		writeRunYAML(t, dir, id, &Run{RunNumber: 2, Committing: true})

		deleted, err := store.CleanupOrphanRuns(id)
		if err != nil {
			t.Fatalf("CleanupOrphanRuns: %v", err)
		}
		if len(deleted) != 1 || deleted[0] != 2 {
			t.Errorf("deleted = %v, want [2]", deleted)
		}
		post := readFeatureYAMLRaw(t, dir, id)
		if post.ActiveRun != 1 || post.RunCount != 1 {
			t.Errorf("reconciled ActiveRun/RunCount = %d/%d, want 1/1", post.ActiveRun, post.RunCount)
		}
	})

	t.Run("does_not_roll_forward_when_higher_run_exists_and_is_preserved", func(t *testing.T) {
		dir := t.TempDir()
		store := NewStore(dir)
		id := "feat-no-forward-roll"
		writeFeatureYAMLRaw(t, dir, &Feature{
			ID:        id,
			Name:      "No Forward Roll Test",
			Slug:      "no-forward-roll",
			Status:    StatusCreated,
			ActiveRun: 2,
			RunCount:  2,
		})
		writeRunYAML(t, dir, id, &Run{RunNumber: 1, SealedAt: ptrTime(time.Now())})
		writeRunYAML(t, dir, id, &Run{RunNumber: 2})
		// run-003 committed, no committing flag, but run_number > ActiveRun.
		writeRunYAML(t, dir, id, &Run{RunNumber: 3})

		deleted, err := store.CleanupOrphanRuns(id)
		if err != nil {
			t.Fatalf("CleanupOrphanRuns: %v", err)
		}
		if len(deleted) != 1 || deleted[0] != 3 {
			t.Errorf("deleted = %v, want [3]", deleted)
		}
		post := readFeatureYAMLRaw(t, dir, id)
		if post.ActiveRun != 2 || post.RunCount != 2 {
			t.Errorf("ActiveRun/RunCount = %d/%d, want 2/2 (no forward promotion)", post.ActiveRun, post.RunCount)
		}
	})

	t.Run("already_consistent_no_write", func(t *testing.T) {
		dir := t.TempDir()
		store := NewStore(dir)
		id := "feat-consistent"
		writeFeatureYAMLRaw(t, dir, &Feature{
			ID:        id,
			Name:      "Consistent Test",
			Slug:      "consistent",
			Status:    StatusCreated,
			ActiveRun: 2,
			RunCount:  2,
		})
		writeRunYAML(t, dir, id, &Run{RunNumber: 1, SealedAt: ptrTime(time.Now())})
		writeRunYAML(t, dir, id, &Run{RunNumber: 2})

		featurePath := filepath.Join(dir, id, "feature.yaml")
		before, err := os.Stat(featurePath)
		if err != nil {
			t.Fatalf("stat before: %v", err)
		}
		// Sleep >1s so mtime resolution can distinguish; then run cleanup.
		time.Sleep(1100 * time.Millisecond)

		deleted, err := store.CleanupOrphanRuns(id)
		if err != nil {
			t.Fatalf("CleanupOrphanRuns: %v", err)
		}
		if deleted != nil {
			t.Errorf("deleted = %v, want nil", deleted)
		}
		after, err := os.Stat(featurePath)
		if err != nil {
			t.Fatalf("stat after: %v", err)
		}
		if !before.ModTime().Equal(after.ModTime()) {
			t.Errorf("feature.yaml mtime changed: %v -> %v (cleanup should be a no-op)",
				before.ModTime(), after.ModTime())
		}
	})
}

// ptrTime returns a pointer to a time.Time value, for use in struct literals.
func ptrTime(t time.Time) *time.Time { return &t }
