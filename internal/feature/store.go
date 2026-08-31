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
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

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
	// mu serializes writes against each other and against the multi-record
	// relationship scans, which hold it shared: those scans read every
	// feature.yaml, so exclusive reads would queue concurrent API reads
	// behind one another and behind every mutation. Single-record reads rely
	// on atomic file commits and take no lock at all.
	mu sync.RWMutex

	// testSaveInterceptor is a test-only hook consulted by saveUnlocked to
	// inject failures. The concrete wiring lives in export_test.go.
	testSaveInterceptor func(f *Feature) error
}

// RelationshipChildren is the complete child-owned relationship view for a
// parent. Active is nil when no child is open. Closed contains every settled
// child in deterministic history order.
type RelationshipChildren struct {
	Active *Feature
	Closed []*Feature
}

// isLegacyProviderBookkeepingDir identifies runtime-owned directories written
// beneath the feature store: provider bookkeeping from older Agentico
// versions plus the server-owned AMA chat session state and upload staging.
// They are not feature
// records and must not participate in feature or relationship scans.
// ErrLegacySchemaVersion marks a record persisted by an older release with an
// explicit lower schema version. Legacy records predate child relationships,
// so relationship scans may skip them; every other read path still refuses
// the record.
var ErrLegacySchemaVersion = errors.New("legacy feature schema version")

func isLegacyProviderBookkeepingDir(name string) bool {
	switch name {
	case "opencode", "codex-home", "chat", "uploads":
		return true
	default:
		return false
	}
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

// RelationshipGuardFunc evaluates whether a mutation is allowed given the
// loaded feature and its active child (nil when the feature is a child or no
// active child exists). Return an error to reject the mutation. The feature
// and activeChild are loaded under the store mutex; do not call other Store
// methods from within.
type RelationshipGuardFunc func(f *Feature, activeChild *Feature) error

// ModifyGuarded atomically loads a feature, runs the relationship guard
// function under the store mutex (using the same activeChildOfUnlocked scan
// as CreateChildLocked), and — if the guard passes — applies the mutation
// function and saves. This closes the time-of-check/time-of-use gap between a
// standalone RelationshipGuard call and a subsequent Store.Modify: child
// creation (CreateChildLocked) holds the same mutex, so no child can appear
// between the guard check and the mutation.
func (s *Store) ModifyGuarded(
	id string,
	guard RelationshipGuardFunc,
	fn func(f *Feature) error,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := s.loadUnlocked(id)
	if err != nil {
		return err
	}
	if f.IsChild() && !f.IsActiveChild() {
		return ErrChildRelationshipClosed
	}

	var activeChild *Feature
	if !f.IsChild() {
		activeChild, err = s.activeChildOfUnlocked(id)
		if err != nil {
			return fmt.Errorf("checking active child during guarded modify: %w", err)
		}
	}

	if guard != nil {
		if err := guard(f, activeChild); err != nil {
			return err
		}
	}

	if err := fn(f); err != nil {
		return err
	}
	return s.saveUnlocked(f)
}

// RelationshipChildren returns the unique active child and complete closed
// history derived from child-owned Parent links. It fails closed if any stored
// record cannot be loaded or carries an invalid relationship lifecycle state.
func (s *Store) RelationshipChildren(parentID string) (*RelationshipChildren, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.relationshipChildrenUnlocked(parentID)
}

// CloseChild atomically settles an active child relationship exactly once.
// Retrying the same outcome is a no-op and preserves the original timestamp;
// a different outcome is rejected as a mutation of closed history.
func (s *Store) CloseChild(childID, outcome string, closedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if outcome != ChildCloseOutcomeCompleted && outcome != ChildCloseOutcomeDiscarded {
		return &ChildRelationshipError{ChildID: childID, Reason: fmt.Sprintf("unknown close outcome %q", outcome)}
	}
	if closedAt.IsZero() {
		return &ChildRelationshipError{ChildID: childID, Reason: fmt.Sprintf("%s child has no valid close timestamp", outcome)}
	}
	child, err := s.loadUnlocked(childID)
	if err != nil {
		return err
	}
	if !child.IsChild() {
		return &ChildRelationshipError{ChildID: childID, Reason: "feature is not a child"}
	}
	if err := validateChildRelationship(child); err != nil {
		return err
	}
	if !child.IsActiveChild() {
		if child.Parent.CloseOutcome == outcome {
			return nil
		}
		return ErrChildRelationshipClosed
	}
	child.Parent.CloseOutcome = outcome
	timestamp := closedAt
	child.Parent.ClosedAt = &timestamp
	return s.saveUnlocked(child)
}

// SetClosedChildDiffSummary records the best-effort preserved diff summary of
// a closed child for post-close inspection, bounded at DiffSummaryBudget. It
// no-ops when the feature is not a closed child (the close write owns
// ordering) or the summary is unchanged.
func (s *Store) SetClosedChildDiffSummary(childID, summary string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if summary == "" {
		return nil
	}
	summary = BoundDiffSummary(summary)
	child, err := s.loadUnlocked(childID)
	if err != nil {
		return err
	}
	if !child.IsChild() || child.IsActiveChild() {
		return nil
	}
	if child.Parent.DiffSummary == summary {
		return nil
	}
	child.Parent.DiffSummary = summary
	return s.saveUnlocked(child)
}

func (s *Store) relationshipChildrenUnlocked(parentID string) (*RelationshipChildren, error) {
	result, err := s.validatedRelationshipChildrenUnlocked(parentID)
	if err != nil {
		return nil, err
	}
	sortClosedChildren(result.Closed)
	return result, nil
}

func sortClosedChildren(closed []*Feature) {
	sort.Slice(closed, func(i, j int) bool {
		left, right := closed[i], closed[j]
		if !left.Parent.ClosedAt.Equal(*right.Parent.ClosedAt) {
			return left.Parent.ClosedAt.After(*right.Parent.ClosedAt)
		}
		if !left.Created.Equal(right.Created) {
			return left.Created.After(right.Created)
		}
		return left.ID < right.ID
	})
}

// AllRelationshipChildren classifies every stored child by its parent in a
// single directory pass, enforcing the same fail-closed invariants as
// RelationshipChildren. Parents without children have no map entry.
func (s *Store) AllRelationshipChildren() (map[string]*RelationshipChildren, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entries, err := os.ReadDir(s.BaseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]*RelationshipChildren{}, nil
		}
		return nil, fmt.Errorf("listing features directory: %w", err)
	}
	result := map[string]*RelationshipChildren{}
	for _, entry := range entries {
		if !entry.IsDir() || isLegacyProviderBookkeepingDir(entry.Name()) {
			continue
		}
		child, exists, err := s.loadFeatureForScan(entry.Name())
		if err != nil {
			if errors.Is(err, ErrLegacySchemaVersion) {
				// Legacy records predate child relationships; one stale
				// record must not fail every relationship read.
				log.Printf("feature store: skipping legacy record %q during relationship scan: %v", entry.Name(), err)
				continue
			}
			return nil, fmt.Errorf("classifying feature record %q during relationship scan: %w", entry.Name(), err)
		}
		if !exists {
			continue
		}
		if child.Parent == nil || child.Parent.ParentID == child.ID {
			continue
		}
		if err := validateChildRelationship(child); err != nil {
			return nil, err
		}
		children := result[child.Parent.ParentID]
		if children == nil {
			children = &RelationshipChildren{}
			result[child.Parent.ParentID] = children
		}
		if child.IsActiveChild() {
			if children.Active != nil {
				return nil, &ChildRelationshipError{
					ChildID: child.ID,
					Reason:  fmt.Sprintf("parent %s has multiple active children %s and %s", child.Parent.ParentID, children.Active.ID, child.ID),
				}
			}
			children.Active = child
			continue
		}
		children.Closed = append(children.Closed, child)
	}
	for _, children := range result {
		sortClosedChildren(children.Closed)
	}
	return result, nil
}

// validatedRelationshipChildrenUnlocked discovers the complete relationship
// while enforcing the same fail-closed invariants for every caller. Callers
// may apply a projection-specific ordering only after validation succeeds.
func (s *Store) validatedRelationshipChildrenUnlocked(parentID string) (*RelationshipChildren, error) {
	entries, err := os.ReadDir(s.BaseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return &RelationshipChildren{}, nil
		}
		return nil, fmt.Errorf("listing features directory: %w", err)
	}

	result := &RelationshipChildren{}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == parentID || isLegacyProviderBookkeepingDir(entry.Name()) {
			continue
		}
		child, exists, err := s.loadFeatureForScan(entry.Name())
		if err != nil {
			if errors.Is(err, ErrLegacySchemaVersion) {
				// Legacy records predate child relationships; one stale
				// record must not fail every relationship read.
				log.Printf("feature store: skipping legacy record %q during relationship scan: %v", entry.Name(), err)
				continue
			}
			return nil, fmt.Errorf("classifying feature record %q during relationship scan: %w", entry.Name(), err)
		}
		if !exists {
			continue
		}
		if child.Parent == nil || child.Parent.ParentID != parentID {
			continue
		}
		if err := validateChildRelationship(child); err != nil {
			return nil, err
		}
		if child.IsActiveChild() {
			if result.Active != nil {
				return nil, &ChildRelationshipError{
					ChildID: child.ID,
					Reason:  fmt.Sprintf("parent %s has multiple active children %s and %s", parentID, result.Active.ID, child.ID),
				}
			}
			result.Active = child
			continue
		}
		result.Closed = append(result.Closed, child)
	}
	return result, nil
}

func validateChildRelationship(child *Feature) error {
	outcome := child.Parent.CloseOutcome
	closedAt := child.Parent.ClosedAt
	switch outcome {
	case "":
		if closedAt != nil {
			return &ChildRelationshipError{ChildID: child.ID, Reason: "active child has a close timestamp"}
		}
	case ChildCloseOutcomeCompleted, ChildCloseOutcomeDiscarded:
		if closedAt == nil || closedAt.IsZero() {
			return &ChildRelationshipError{ChildID: child.ID, Reason: fmt.Sprintf("%s child has no valid close timestamp", outcome)}
		}
	default:
		return &ChildRelationshipError{ChildID: child.ID, Reason: fmt.Sprintf("unknown close outcome %q", outcome)}
	}
	return nil
}

// saveUnlocked writes a feature and (if populated and not sealed) its active
// run without acquiring the mutex. Uses atomic write (temp file + rename) for
// both files so readers never see a partial file.
//
// Before writing, it synchronises Feature's transient shadow fields into the
// companion Run so the split-persistence layout stays coherent with call
// sites that keep reading/writing `f.X` directly.
func (s *Store) saveUnlocked(f *Feature) error {
	if s.testSaveInterceptor != nil {
		if err := s.testSaveInterceptor(f); err != nil {
			return err
		}
	}
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

// loadUnlocked reads a feature and its active run from the last committed
// atomic file renames. It intentionally does not acquire s.mu so read-model
// endpoints stay responsive while a long Modify closure is in progress.
func (s *Store) loadUnlocked(id string) (*Feature, error) {
	path := filepath.Join(s.BaseDir, id, "feature.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading feature file: %w", err)
	}
	var header struct {
		SchemaVersion int `yaml:"schema_version"`
	}
	if err := yaml.Unmarshal(data, &header); err != nil {
		return nil, fmt.Errorf("parsing feature file: %w", err)
	}
	if header.SchemaVersion != SchemaVersionCurrent {
		if header.SchemaVersion > 0 && header.SchemaVersion < SchemaVersionCurrent {
			return nil, fmt.Errorf("feature schema version %d, expected %d: %w", header.SchemaVersion, SchemaVersionCurrent, ErrLegacySchemaVersion)
		}
		return nil, fmt.Errorf("feature schema version %d, expected %d", header.SchemaVersion, SchemaVersionCurrent)
	}
	var f Feature
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parsing feature file: %w", err)
	}

	run, err := s.loadRunUnlocked(id, f.ActiveRun)
	if err != nil {
		return nil, fmt.Errorf("loading active run for feature %s: %w", id, err)
	}
	f.SetRun(run)
	return &f, nil
}

// loadFeatureForScan loads one directory entry while treating a missing
// feature.yaml as an absent record. Delete removes the complete feature
// directory, but a concurrent scan or a late artifact from an older runtime
// can still expose a directory after the authoritative record is gone.
//
// The second existence check deliberately distinguishes that case from a
// present feature.yaml whose active run is missing: the latter is a corrupt
// record and must continue to fail closed.
func (s *Store) loadFeatureForScan(id string) (*Feature, bool, error) {
	f, err := s.loadUnlocked(id)
	if err == nil {
		return f, true, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}
	if _, statErr := os.Stat(filepath.Join(s.BaseDir, id, "feature.yaml")); errors.Is(statErr, os.ErrNotExist) {
		return nil, false, nil
	}
	return nil, false, err
}

// loadRunUnlocked reads the run.yaml for a specific run number. It does not
// acquire s.mu; run writes are committed by atomic rename.
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
	return &r, nil
}

func (s *Store) List() ([]*Feature, error) {
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
		if !e.IsDir() || isLegacyProviderBookkeepingDir(e.Name()) {
			continue
		}
		f, exists, err := s.loadFeatureForScan(e.Name())
		if err != nil {
			warnings = append(warnings, LoadWarning{ID: e.Name(), Err: err})
			continue
		}
		if !exists {
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
	if err := removeAllResilient(dir); err != nil {
		return fmt.Errorf("deleting feature directory: %w", err)
	}
	return nil
}

// removeAllResilient removes dir even when it contains read-only directories
// such as module caches captured in run artifacts. os.RemoveAll can delete
// feature.yaml before encountering one of those directories, leaving a path
// that looks like a corrupt feature on the next scan. Restore owner access to
// directories after the first failure, then retry the complete removal.
func removeAllResilient(dir string) error {
	if err := os.RemoveAll(dir); err == nil {
		return nil
	}
	_ = filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || !entry.IsDir() {
			return nil
		}
		if info, infoErr := entry.Info(); infoErr == nil && info.Mode().Perm()&0o700 != 0o700 {
			_ = os.Chmod(path, info.Mode().Perm()|0o700)
		}
		return nil
	})
	return os.RemoveAll(dir)
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
	return s.loadRunUnlocked(featureID, runNumber)
}

// ListRuns enumerates every run directory on disk for a feature and returns
// the parseable run numbers sorted ascending. It reads the filesystem rather
// than deriving from ActiveRun/RunCount so it survives active-run gaps left
// by crash recovery and reports run numbers above 999 without lexicographic
// truncation. A missing runs directory yields an empty slice and no error.
// Callers paginate/newest-first by reversing the result.
func (s *Store) ListRuns(featureID string) ([]int, error) {
	runsDir := filepath.Join(s.BaseDir, featureID, "runs")
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("listing runs directory: %w", err)
	}
	var runNumbers []int
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		n, ok := parseRunDirName(e.Name())
		if !ok {
			continue
		}
		runNumbers = append(runNumbers, n)
	}
	sort.Ints(runNumbers)
	return runNumbers, nil
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
		if err := removeAllResilient(runPath); err != nil {
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

// CreateChildLocked atomically validates and persists a parent-child
// relationship under a single Store.mu hold. Sequence:
//  1. Load the parent plus any active child (derived by scanning stored
//     feature records for child-owned Parent links).
//  2. Call fn(parent, activeChild) — nil activeChild when none exists. fn
//     validates the launch, mutates the parent (e.g. applying the submitted
//     Review configuration), and returns the child plus its durable
//     creation intent. Error aborts without writing anything.
//  3. Persist the parent WITH the pending intent stamped (durable intent).
//  4. Persist the child (feature.yaml + first run.yaml, atomically).
//  5. For a review-feedback intent carrying a launch receipt, delete the
//     pinned draft revision the receipt consumed — still under this lock,
//     so the consumption marker never lapses between the intent clearing
//     and the draft cleanup.
//  6. Clear the intent and persist the parent again.
//
// A crash between steps 3 and 6 leaves the intent on the parent;
// ReconcilePendingChildCreations rolls the creation forward exactly once.
// Concurrent launches under the same parent serialize on Store.mu, so
// exactly one winner observes activeChild == nil.
//
// The fn closure must NOT call any other Store method (mu is not
// reentrant).
func (s *Store) CreateChildLocked(
	parentID string,
	fn func(parent *Feature, activeChild *Feature) (child *Feature, intent *ChildCreationIntent, err error),
) (*Feature, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	parent, err := s.loadUnlocked(parentID)
	if err != nil {
		return nil, err
	}
	activeChild, err := s.activeChildOfUnlocked(parentID)
	if err != nil {
		return nil, err
	}
	child, intent, err := fn(parent, activeChild)
	if err != nil {
		return nil, err
	}
	if child == nil || intent == nil {
		return nil, fmt.Errorf("create-child closure must return a child and an intent")
	}
	if child.ID == "" || intent.ChildID != child.ID {
		return nil, fmt.Errorf("create-child intent %q does not match child %q", intent.ChildID, child.ID)
	}

	parent.PendingChild = intent
	if err := s.saveUnlocked(parent); err != nil {
		return nil, fmt.Errorf("saving parent with child intent: %w", err)
	}
	if err := s.saveUnlocked(child); err != nil {
		return nil, fmt.Errorf("saving child: %w", err)
	}
	// Consume the draft revision a review-feedback launch receipt pinned
	// before clearing the durable intent: the pinned deletion rides the same
	// lock acquisition as the intent clearing, so no selection save can
	// commit in a window where neither the pending intent nor the pinned
	// draft marks the revision as consumed. The delete is pinned to the
	// receipt's revision so a newer committed draft is never removed. A
	// failure here leaves the durable intent in place so reconciliation
	// rolls both the child and the cleanup forward.
	if intent.Kind == ChildKindReviewFeedback && intent.LaunchReceipt != nil {
		if err := s.deleteReviewFeedbackDraftIfRevisionUnlocked(parentID, intent.LaunchReceipt.DraftRevision); err != nil {
			return nil, fmt.Errorf("consuming review feedback draft for parent %q: %w", parentID, err)
		}
	}
	parent.PendingChild = nil
	if err := s.saveUnlocked(parent); err != nil {
		return nil, fmt.Errorf("clearing child intent on parent: %w", err)
	}
	return child, nil
}

// activeChildOfUnlocked scans stored feature records for the child that
// links back to parentID with an empty close outcome. Callers hold s.mu.
func (s *Store) activeChildOfUnlocked(parentID string) (*Feature, error) {
	children, err := s.relationshipChildrenUnlocked(parentID)
	if err != nil {
		return nil, err
	}
	return children.Active, nil
}

// ReconcilePendingChildCreations rolls interrupted child creations forward
// exactly once. For every feature carrying a PendingChild intent: when the
// child already loads cleanly the intent is simply cleared (creation
// finished before the crash); otherwise the child is rebuilt from the
// durable intent — preserving the selected parent configuration captured at
// launch — and then the intent is cleared. Returns the parent IDs whose
// intents were resolved. Idempotent; a second run finds no intents.
//
// The intent scan reads each parent's feature.yaml directly: the durable
// intent is owned by the parent's feature record, so unrelated run state
// must never gate (or silently swallow) startup recovery. An unreadable
// feature record is surfaced as a contextual load error instead of being
// skipped, because skipping could let a submitted child remain neither
// materialized nor diagnosed. Intents are cleared by rewriting feature.yaml
// alone, so a parent whose active run.yaml is missing still reconciles.
//
// Intended to run at startup before abandoned-setup reconciliation so a
// rebuilt child (left in SettingUpWorktrees with a running setup intent) is
// then marked retryable-with-diagnostics by ReconcileAbandonedSetups in the
// correct order.
func (s *Store) ReconcilePendingChildCreations() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := os.ReadDir(s.BaseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("listing features directory: %w", err)
	}
	var reconciled []string
	var errs []error
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.BaseDir, e.Name(), "feature.yaml"))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			errs = append(errs, fmt.Errorf("reading feature record %q during pending-child scan: %w", e.Name(), err))
			continue
		}
		var parent Feature
		if err := yaml.Unmarshal(data, &parent); err != nil {
			errs = append(errs, fmt.Errorf("parsing feature record %q during pending-child scan: %w", e.Name(), err))
			continue
		}
		if parent.PendingChild == nil {
			continue
		}
		intent := parent.PendingChild
		if _, err := s.loadUnlocked(intent.ChildID); err != nil {
			child := intent.Child
			if child.ID == "" || child.ID != intent.ChildID {
				errs = append(errs, fmt.Errorf("parent %s: child intent %q is incomplete; cannot roll forward", parent.ID, intent.ChildID))
				continue
			}
			run := &Run{RunNumber: 1, Setup: intent.Setup}
			if len(child.Repos) > 0 {
				run.RepoStates = make(map[string]*RepoState, len(child.Repos))
				for _, fr := range child.Repos {
					run.RepoStates[fr.Name] = &RepoState{}
				}
			}
			child.ActiveRun = 1
			child.RunCount = 1
			child.SetRun(run)
			if err := s.saveUnlocked(&child); err != nil {
				errs = append(errs, fmt.Errorf("parent %s: rebuilding child %s: %w", parent.ID, intent.ChildID, err))
				continue
			}
		}
		// A review-feedback launch that reached the durable intent already
		// consumed the pending draft: converge the draft cleanup an
		// interrupted launch could not finish before durably clearing the
		// intent, matching the CreateChildLocked ordering. Clearing the
		// intent first would leave neither the pending intent nor the pinned
		// draft marking the revision as consumed after a cleanup failure or
		// crash, letting a later SaveReviewFeedbackDraft acknowledge edits
		// to a stranded consumed draft behind the active child. The cleanup
		// is pinned to the receipt's draft revision so a newer committed
		// draft is never deleted, and it is idempotent so a crash between
		// the deletion and the intent clearing retries safely.
		if intent.Kind == ChildKindReviewFeedback && intent.LaunchReceipt != nil {
			if err := s.deleteReviewFeedbackDraftIfRevisionUnlocked(parent.ID, intent.LaunchReceipt.DraftRevision); err != nil {
				errs = append(errs, fmt.Errorf("parent %s: deleting review-feedback draft consumed by launch: %w", parent.ID, err))
				continue
			}
		}
		parent.PendingChild = nil
		if err := s.writeFeatureYAMLUnlocked(parent.ID, &parent); err != nil {
			errs = append(errs, fmt.Errorf("clearing child intent on parent %s: %w", parent.ID, err))
			continue
		}
		reconciled = append(reconciled, parent.ID)
	}
	if len(errs) > 0 {
		return reconciled, errors.Join(errs...)
	}
	return reconciled, nil
}

// writeFeatureYAMLUnlocked writes ONLY feature.yaml atomically via temp-file
// + rename (matching saveUnlocked's pattern). Unlike saveUnlocked, it does
// NOT call syncShadowsToRun, does NOT fabricate a missing f.run, and does
// NOT persist the active run's run.yaml — those side effects are unsafe
// when f was unmarshaled directly from feature.yaml (no shadows populated,
// no run loaded). Used by CleanupOrphanRuns to reconcile ActiveRun / RunCount
// and by ReconcilePendingChildCreations to clear a pending-child intent,
// without risking a stale shadow value leaking into feature.yaml or a
// fabricated Run overwriting a sealed run.yaml. Callers hold s.mu.
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

// parseRunDirName parses canonical run directory names into their numeric run
// number. Returns (0, false) for names the current writer would not emit.
func parseRunDirName(name string) (int, bool) {
	rest, ok := strings.CutPrefix(name, "run-")
	if !ok || rest == "" {
		return 0, false
	}
	n, err := strconv.Atoi(rest)
	if err != nil || n <= 0 {
		return 0, false
	}
	if RunDirName(n) != name {
		return 0, false
	}
	return n, true
}
