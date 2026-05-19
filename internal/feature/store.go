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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// PartialLoadError is returned by List when some features could not be loaded.
// The caller still receives the features that loaded successfully.
type PartialLoadError struct {
	Warnings []LoadWarning
}

type LoadWarning struct {
	ID  string
	Err error
}

func (e *PartialLoadError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d feature(s) failed to load:", len(e.Warnings))
	for _, w := range e.Warnings {
		fmt.Fprintf(&b, "\n  %s: %v", w.ID, w.Err)
	}
	return b.String()
}

// Unwrap exposes every per-feature warning so `errors.Is` can detect wrapped
// sentinels that surfaced during a multi-feature List call.
func (e *PartialLoadError) Unwrap() []error {
	errs := make([]error, len(e.Warnings))
	for i, w := range e.Warnings {
		errs[i] = w.Err
	}
	return errs
}

func IsPartialLoadError(err error) bool {
	var ple *PartialLoadError
	return errors.As(err, &ple)
}

type Store struct {
	BaseDir string
	mu      sync.Mutex // serializes all read-modify-write cycles
}

func NewStore(baseDir string) *Store {
	return &Store{BaseDir: baseDir}
}

func (s *Store) Save(f *Feature) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.saveUnlocked(f)
}

func (s *Store) Load(id string) (*Feature, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.loadUnlocked(id)
}

// Modify atomically loads a feature, applies a mutation function, and saves.
// The entire load-modify-save cycle is protected by the store mutex, preventing
// concurrent writes from corrupting the YAML file.
func (s *Store) Modify(id string, fn func(f *Feature) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := s.loadUnlocked(id)
	if err != nil {
		return err
	}
	if err := fn(f); err != nil {
		return err
	}
	return s.saveUnlocked(f)
}

// saveUnlocked writes a feature and (if populated and not sealed) its active
// run without acquiring the mutex. Uses atomic write (temp file + rename) for
// both files so readers never see a partial file.
//
// Before writing, it synchronises Feature's transient shadow fields into the
// companion Run so the split-persistence layout stays coherent with call
// sites that keep reading/writing `f.X` directly.
func (s *Store) saveUnlocked(f *Feature) error {
	dir := filepath.Join(s.BaseDir, f.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating feature directory: %w", err)
	}
	// Ensure the active run pointer exists and mirror the shadow fields into
	// it before marshalling. Create() is responsible for bumping ActiveRun to
	// 1 the very first time; we defensively seed it here too.
	if f.ActiveRun == 0 {
		f.ActiveRun = 1
	}
	if f.RunCount < f.ActiveRun {
		f.RunCount = f.ActiveRun
	}
	if f.run == nil {
		f.run = &Run{RunNumber: f.ActiveRun}
	} else if f.run.RunNumber == 0 {
		f.run.RunNumber = f.ActiveRun
	}
	if !f.run.IsSealed() {
		f.syncShadowsToRun()
	}
	data, err := yaml.Marshal(f)
	if err != nil {
		return fmt.Errorf("marshaling feature: %w", err)
	}

	// Atomic write: write to a unique temp file, then rename.
	// os.Rename is atomic on POSIX, so readers never see a partial file.
	// Using os.CreateTemp avoids corruption if two goroutines (even from
	// different Store instances) race into saveUnlocked concurrently — a
	// fixed temp name like "feature.yaml.tmp" allows interleaved writes
	// where the shorter one leaves trailing bytes from the longer one.
	path := filepath.Join(dir, "feature.yaml")
	tmp, err := os.CreateTemp(dir, "feature-*.yaml.tmp")
	if err != nil {
		return fmt.Errorf("creating feature temp file: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("writing feature temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("closing feature temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("renaming feature file: %w", err)
	}

	// Persist the active run alongside the feature, except when the active
	// run has been sealed — sealed runs are immutable by contract.
	if f.run != nil && !f.run.IsSealed() {
		if err := s.saveRunUnlocked(f.ID, f.run); err != nil {
			return fmt.Errorf("saving active run: %w", err)
		}
	}
	return nil
}

// saveRunUnlocked writes a run.yaml atomically. Callers must hold s.mu.
func (s *Store) saveRunUnlocked(featureID string, r *Run) error {
	if r == nil {
		return fmt.Errorf("nil run")
	}
	if r.RunNumber <= 0 {
		return fmt.Errorf("invalid run number %d", r.RunNumber)
	}
	dir := s.runDirUnlocked(featureID, r.RunNumber)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating run directory: %w", err)
	}
	data, err := yaml.Marshal(r)
	if err != nil {
		return fmt.Errorf("marshaling run: %w", err)
	}
	path := filepath.Join(dir, "run.yaml")
	tmp, err := os.CreateTemp(dir, "run-*.yaml.tmp")
	if err != nil {
		return fmt.Errorf("creating run temp file: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("writing run temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("closing run temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("renaming run file: %w", err)
	}
	return nil
}

// RunDir returns the absolute directory for a specific run of a feature.
// Pure path assembly — no mutex required. Exposed for callers inside
// internal/feature/ that need to address a specific run number (e.g., the
// carry-forward helpers in carry_forward.go). Not added to ports.FeatureStore
// because no call-site outside internal/feature/ needs it.
func (s *Store) RunDir(featureID string, runNumber int) string {
	return s.runDirUnlocked(featureID, runNumber)
}

// runDirUnlocked returns the absolute run directory for a (featureID,
// runNumber) pair. No mutex requirement (pure path join).
func (s *Store) runDirUnlocked(featureID string, runNumber int) string {
	return filepath.Join(s.BaseDir, featureID, "runs", RunDirName(runNumber))
}

// loadUnlocked reads a feature and its active run.
// Callers must hold s.mu.
func (s *Store) loadUnlocked(id string) (*Feature, error) {
	path := filepath.Join(s.BaseDir, id, "feature.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading feature file: %w", err)
	}
	var f Feature
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parsing feature file: %w", err)
	}

	run, err := s.loadRunUnlocked(id, f.ActiveRun)
	if err != nil {
		return nil, fmt.Errorf("loading active run for feature %s: %w", id, err)
	}
	f.run = run
	f.syncRunToShadows()
	return &f, nil
}

// loadRunUnlocked reads the run.yaml for a specific run number.
// Callers must hold s.mu.
func (s *Store) loadRunUnlocked(featureID string, runNumber int) (*Run, error) {
	path := filepath.Join(s.runDirUnlocked(featureID, runNumber), "run.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading run file: %w", err)
	}
	var r Run
	if err := yaml.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("parsing run file: %w", err)
	}
	normalizeLegacyArtifactAliases(r.Artifacts)
	return &r, nil
}

func (s *Store) List() ([]*Feature, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := os.ReadDir(s.BaseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("listing features directory: %w", err)
	}
	var features []*Feature
	var warnings []LoadWarning
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		f, err := s.loadUnlocked(e.Name())
		if err != nil {
			warnings = append(warnings, LoadWarning{ID: e.Name(), Err: err})
			continue
		}
		features = append(features, f)
	}
	if len(warnings) > 0 {
		return features, &PartialLoadError{Warnings: warnings}
	}
	return features, nil
}

func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	dir := filepath.Join(s.BaseDir, id)
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("deleting feature directory: %w", err)
	}
	return nil
}

// CreateRun writes a fresh run.yaml for a new run. RunNumber must be set.
// Fails if the run file already exists. Atomic temp-file+rename.
func (s *Store) CreateRun(featureID string, r *Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if r == nil {
		return fmt.Errorf("nil run")
	}
	if r.RunNumber <= 0 {
		return fmt.Errorf("invalid run number %d", r.RunNumber)
	}
	path := filepath.Join(s.runDirUnlocked(featureID, r.RunNumber), "run.yaml")
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("run %d already exists for feature %s", r.RunNumber, featureID)
	}
	return s.saveRunUnlocked(featureID, r)
}

// LoadRun reads a specific run's run.yaml.
func (s *Store) LoadRun(featureID string, runNumber int) (*Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.loadRunUnlocked(featureID, runNumber)
}

// SaveRun writes a run.yaml. Panics if r.IsSealed() — a sealed run is
// immutable by contract. Atomic temp-file+rename.
func (s *Store) SaveRun(featureID string, r *Run) error {
	if r == nil {
		return fmt.Errorf("nil run")
	}
	if r.IsSealed() {
		panic(fmt.Sprintf("feature.Store.SaveRun: run %d of feature %s is sealed; sealed runs are immutable", r.RunNumber, featureID))
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.saveRunUnlocked(featureID, r)
}

// SealAndForkRun atomically seals the active run and forks a fresh one under
// a single Store.mu hold. Sequence:
//  1. Load feature + active run.
//  2. Call seal(oldRun) — caller mutates seal fields (SealedAt, SealReason,
//     RewindTarget, BackupBranches). Error aborts without writing.
//  3. saveRunUnlocked(oldRun) — sealed run committed to disk (overwrite on
//     idempotent re-seal).
//  4. Call fork(oldRun) — returns the SKELETON new Run (RunNumber =
//     oldRun.RunNumber+1, Committing:true). Fork must NOT do carry-forward IO.
//  5. saveRunUnlocked(newRun) — committing:true skeleton on disk.
//  6. Call populate(oldRun, newRun) (if non-nil) — performs the carry-forward
//     copy and populates CarriedPhases/Artifacts on the skeleton. Errors
//     leave the committing:true skeleton on disk for CleanupOrphanRuns.
//  7. newRun.Committing = false; saveRunUnlocked(newRun) — final write.
//  8. f.ActiveRun = newRun.RunNumber; f.RunCount = newRun.RunNumber;
//     f.run = newRun.
//  9. saveUnlocked(f) — feature.yaml updated with new ActiveRun/RunCount.
//
// Crash recovery: a crash between steps 5 and 7 leaves runs/run-(N+1)/
// with committing:true, which CleanupOrphanRuns deletes on next startup.
// A crash between steps 7 and 9 leaves runs/run-(N+1)/ with committing:false
// but ActiveRun still at N; CleanupOrphanRuns deletes it via the
// run_number > ActiveRun predicate.
//
// Idempotent re-seal: if the old run is already sealed (a prior rewind
// crashed between seal and bump; CleanupOrphanRuns removed the orphan new
// run; ActiveRun still points at the sealed old run), the seal closure is
// allowed to overwrite the seal fields. The shadow-sync step is skipped —
// a sealed run's per-attempt state is immutable by contract.
//
// Returns the updated *Feature (run field populated with newRun).
func (s *Store) SealAndForkRun(
	featureID string,
	seal func(oldRun *Run) error,
	fork func(oldRun *Run) (*Run, error),
	populate func(oldRun, newRun *Run) error,
) (*Feature, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := s.loadUnlocked(featureID)
	if err != nil {
		return nil, fmt.Errorf("loading feature for seal+fork: %w", err)
	}
	oldRun := f.run
	if oldRun == nil {
		return nil, fmt.Errorf("feature %s has no active run loaded", featureID)
	}
	// Idempotent re-seal: skip shadow sync when the old run is already sealed
	// (sealed runs are immutable by contract). A first-time seal captures any
	// pending shadow mutations onto the run before the seal closure stamps
	// provenance.
	if !oldRun.IsSealed() {
		f.syncShadowsToRun()
	}
	if err := seal(oldRun); err != nil {
		return nil, fmt.Errorf("seal closure: %w", err)
	}
	if !oldRun.IsSealed() {
		return nil, fmt.Errorf("seal closure did not set SealedAt on run %d", oldRun.RunNumber)
	}
	// Step 3: persist the sealed run directly (bypassing saveUnlocked's
	// sealed-skip guard because we explicitly want to record the seal).
	// On idempotent re-seal this simply overwrites the existing run.yaml.
	if err := s.saveRunUnlocked(featureID, oldRun); err != nil {
		return nil, fmt.Errorf("saving sealed run: %w", err)
	}

	// Step 4: fork returns a skeleton newRun with Committing:true. The
	// skeleton is persisted to disk BEFORE populate runs so a crash during
	// populate leaves a clearly-marked orphan for CleanupOrphanRuns.
	newRun, err := fork(oldRun)
	if err != nil {
		return nil, fmt.Errorf("fork closure: %w", err)
	}
	if newRun == nil {
		return nil, fmt.Errorf("fork closure returned nil run")
	}
	if newRun.RunNumber != oldRun.RunNumber+1 {
		return nil, fmt.Errorf("fork closure produced run %d (expected %d)", newRun.RunNumber, oldRun.RunNumber+1)
	}
	if !newRun.Committing {
		return nil, fmt.Errorf("fork closure must set Committing:true on run %d", newRun.RunNumber)
	}
	// Step 5: persist the skeleton with Committing:true BEFORE populate.
	// A crash between here and step 7 leaves run-(N+1) on disk with
	// committing:true; CleanupOrphanRuns deletes it on next startup.
	if err := s.saveRunUnlocked(featureID, newRun); err != nil {
		return nil, fmt.Errorf("saving committing skeleton: %w", err)
	}

	// Step 6: populate (carry-forward copy + artifact-map rewrite). Errors
	// leave the committing:true skeleton on disk for CleanupOrphanRuns to
	// sweep; ActiveRun is not bumped.
	if populate != nil {
		if err := populate(oldRun, newRun); err != nil {
			return nil, fmt.Errorf("populate closure: %w", err)
		}
	}

	// Step 7: clear the committing flag and persist the final run. A crash
	// between here and the feature.yaml bump (step 9) leaves run-(N+1) with
	// committing:false but ActiveRun still at N; CleanupOrphanRuns deletes
	// it via the run_number > ActiveRun predicate.
	newRun.Committing = false
	if err := s.saveRunUnlocked(featureID, newRun); err != nil {
		return nil, fmt.Errorf("saving committed run: %w", err)
	}

	f.ActiveRun = newRun.RunNumber
	f.RunCount = newRun.RunNumber
	f.run = newRun
	// Mirror the fresh run into Feature's shadow fields so callers that keep
	// reading f.X after the seal+fork see the new-run state (mostly zeroes).
	f.syncRunToShadows()
	if err := s.saveUnlocked(f); err != nil {
		return nil, fmt.Errorf("saving feature after seal+fork: %w", err)
	}
	return f, nil
}

// CleanupOrphanRuns reconciles a feature's runs directory against feature.yaml.
// Deletes any run-NNN subdirectory whose run_number > feature.ActiveRun (a
// stale fork from a rewind that crashed before bumping active_run) or whose
// loadable run.yaml has Committing:true (a skeleton from a rewind that
// crashed during carry-forward). Sealed runs with run_number <= ActiveRun
// are preserved verbatim (sealed-run immutability).
//
// After deletions, if max(run_number on disk) < f.ActiveRun (cleanup removed
// the run ActiveRun pointed at), rolls f.ActiveRun and f.RunCount back to
// max(run_number on disk) and rewrites feature.yaml via
// writeFeatureYAMLUnlocked. This enforces the startup-reconciliation
// invariant "RunCount == max(run_number on disk) == ActiveRun" after
// cleanup completes. Rollback is only downward — cleanup never promotes a
// committed-but-unbumped run to active status (indistinguishable from a
// manually-abandoned fork).
//
// Returns the sorted list of deleted run numbers (for logging / events) plus
// any aggregated deletion / reconciliation error.
//
// Invoked once per feature by Orchestrator.ScanRecovery at startup. Safe to
// call multiple times; subsequent calls with no orphans and no divergence
// are no-ops (no feature.yaml write).
func (s *Store) CleanupOrphanRuns(id string) ([]int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Read feature.yaml directly — we need ActiveRun even when loadUnlocked
	// would fail (e.g., the active run's YAML is missing after manual
	// intervention or a mid-populate crash). yaml.Unmarshal into a local
	// struct sidesteps the shadow-field synchronisation that loadUnlocked
	// performs; the loaded f has run == nil, and we deliberately never write
	// run.yaml from this method.
	featurePath := filepath.Join(s.BaseDir, id, "feature.yaml")
	data, err := os.ReadFile(featurePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading feature file: %w", err)
	}
	var f Feature
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parsing feature file: %w", err)
	}
	runsDir := filepath.Join(s.BaseDir, id, "runs")
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading runs dir: %w", err)
	}

	var deleted []int
	var errs []error
	maxOnDisk := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		n, ok := parseRunDirName(e.Name())
		if !ok {
			continue
		}
		orphan := false
		if n > f.ActiveRun {
			// Stale future run: the fork+populate lifecycle completed but
			// the feature.yaml bump never landed.
			orphan = true
		} else {
			// run_number <= ActiveRun — check for committing:true. A missing
			// or unparseable run.yaml is NOT considered an orphan by this
			// predicate; the first predicate (run_number > ActiveRun) is
			// authoritative for bump-failure, and committing:true is the
			// auxiliary signal for mid-populate crashes.
			runYAML := filepath.Join(runsDir, e.Name(), "run.yaml")
			if runData, rerr := os.ReadFile(runYAML); rerr == nil {
				var r Run
				if yerr := yaml.Unmarshal(runData, &r); yerr == nil && r.Committing {
					orphan = true
				}
			}
		}
		if !orphan {
			if n > maxOnDisk {
				maxOnDisk = n
			}
			continue
		}
		runPath := filepath.Join(runsDir, e.Name())
		if err := os.RemoveAll(runPath); err != nil {
			// Preserve in max-on-disk — the directory is still on disk.
			if n > maxOnDisk {
				maxOnDisk = n
			}
			errs = append(errs, fmt.Errorf("removing orphan run %s: %w", e.Name(), err))
			continue
		}
		deleted = append(deleted, n)
	}
	sort.Ints(deleted)

	// Startup-reconciliation invariant: RunCount == max(run_number on disk)
	// == ActiveRun. Only rewrite feature.yaml when we need to roll something
	// DOWN — cleanup never raises ActiveRun above its pre-cleanup value.
	// maxOnDisk == 0 would mean every directory was deleted AND there were
	// no preserved runs, which can happen only for a pathological state we
	// do not attempt to recover from (leaves feature.yaml untouched).
	if maxOnDisk > 0 && (f.ActiveRun > maxOnDisk || f.RunCount > maxOnDisk) {
		f.ActiveRun = maxOnDisk
		f.RunCount = maxOnDisk
		if err := s.writeFeatureYAMLUnlocked(id, &f); err != nil {
			errs = append(errs, fmt.Errorf("reconciling feature.yaml: %w", err))
		}
	}

	if len(errs) > 0 {
		return deleted, errors.Join(errs...)
	}
	return deleted, nil
}

// writeFeatureYAMLUnlocked writes ONLY feature.yaml atomically via temp-file
// + rename (matching saveUnlocked's pattern). Unlike saveUnlocked, it does
// NOT call syncShadowsToRun, does NOT fabricate a missing f.run, and does
// NOT persist the active run's run.yaml — those side effects are unsafe
// when f was unmarshaled directly from feature.yaml (no shadows populated,
// no run loaded). Used by CleanupOrphanRuns to reconcile ActiveRun / RunCount
// after deletions without risking a stale shadow value leaking into
// feature.yaml or a fabricated Run overwriting a sealed run.yaml. Callers
// hold s.mu.
func (s *Store) writeFeatureYAMLUnlocked(id string, f *Feature) error {
	dir := filepath.Join(s.BaseDir, id)
	data, err := yaml.Marshal(f)
	if err != nil {
		return fmt.Errorf("marshaling feature: %w", err)
	}
	path := filepath.Join(dir, "feature.yaml")
	tmp, err := os.CreateTemp(dir, "feature-*.yaml.tmp")
	if err != nil {
		return fmt.Errorf("creating feature temp file: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("writing feature temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("closing feature temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("renaming feature file: %w", err)
	}
	return nil
}

// parseRunDirName parses "run-NNN" directory names into their numeric run
// number. Accepts any positive-integer suffix so numeric sort order (not
// lexicographic) works for run-1000+. Returns (0, false) for unparseable
// names.
func parseRunDirName(name string) (int, bool) {
	rest, ok := strings.CutPrefix(name, "run-")
	if !ok || rest == "" {
		return 0, false
	}
	n, err := strconv.Atoi(rest)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}
