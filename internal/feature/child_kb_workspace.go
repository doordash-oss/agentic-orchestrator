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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// ChildKBWorkspaceDir returns the directory for a child's per-repo KB
// workspace. The workspace is stored as a sibling of the canonical
// knowledge-base directory and the overlays directory, disjoint from both.
// Layout: <stateDir-parent>/child-kb/<childID>/<repoName>
func ChildKBWorkspaceDir(stateDir, childID, repoName string) string {
	return filepath.Join(filepath.Dir(stateDir), "child-kb", childID, repoName)
}

// WorkspaceSource identifies where a child KB workspace was seeded from.
type WorkspaceSource string

const (
	WorkspaceSourceOverlay   WorkspaceSource = "overlay"   // seeded from a valid parent overlay
	WorkspaceSourceCanonical WorkspaceSource = "canonical" // seeded from the latest canonical KB
	WorkspaceSourceFull      WorkspaceSource = "full"      // full build (no valid overlay or canonical baseline)
)

// ChildKBWorkspaceState is persisted as state.json in the workspace directory.
// It records provenance identifying the source, the canonical KB commit used
// as the baseline, the parent HEAD represented by the overlay, the child
// commit the workspace was seeded against, and the commit the KB builder has
// actually analyzed through.
type ChildKBWorkspaceState struct {
	Source          WorkspaceSource `json:"source"`
	CanonicalCommit string          `json:"canonical_commit"`
	ParentHEAD      string          `json:"parent_head"`
	SeedBaseCommit  string          `json:"seed_base_commit"`
	AnalyzedCommit  string          `json:"analyzed_commit"`
	SeededAt        time.Time       `json:"seeded_at"`
	LastUpdated     time.Time       `json:"last_updated"`
}

// OverlayProvenance is persisted as state.json in the overlay directory.
// It records the canonical KB commit used as the baseline and the integrated
// parent HEAD the overlay represents.
type OverlayProvenance struct {
	CanonicalCommit string    `json:"canonical_commit"`
	ParentHEAD      string    `json:"parent_head"`
	Generation      int       `json:"generation"`
	CreatedAt       time.Time `json:"created_at"`
}

// OverlayLockInfo holds information about who holds the overlay lock.
type OverlayLockInfo struct {
	ChildID   string    `json:"child_id"`
	Timestamp time.Time `json:"timestamp"`
}

// ErrOverlayLocked is returned when a parent overlay is locked by another child.
var ErrOverlayLocked = fmt.Errorf("parent overlay is locked by another child")

// OverlayLockPath returns the path to the overlay lock file. The lock lives
// as a sibling of the overlay directory (inside the stable overlay parent
// namespace) rather than inside the overlay itself, so that atomic overlay
// replacement does not destroy the lock and the exclusive-promotion
// guarantee holds across overlay generations.
func OverlayLockPath(overlayDir string) string {
	return overlayDir + ".lock"
}

// OverlayStatePath returns the path to the overlay provenance state file.
func OverlayStatePath(overlayDir string) string {
	return filepath.Join(overlayDir, "state.json")
}

// WorkspaceStatePath returns the path to the workspace state file.
func WorkspaceStatePath(workspaceDir string) string {
	return filepath.Join(workspaceDir, "state.json")
}

// WorkspaceLockPath returns the path to the workspace lock file.
func WorkspaceLockPath(workspaceDir string) string {
	return filepath.Join(workspaceDir, "workspace.lock")
}

// LoadOverlayProvenance reads the overlay provenance from disk.
// Returns nil if not found.
func LoadOverlayProvenance(overlayDir string) (*OverlayProvenance, error) {
	data, err := os.ReadFile(OverlayStatePath(overlayDir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading overlay provenance: %w", err)
	}
	var prov OverlayProvenance
	if err := json.Unmarshal(data, &prov); err != nil {
		return nil, fmt.Errorf("parsing overlay provenance: %w", err)
	}
	return &prov, nil
}

// ParentOverlayExists reports whether a durable promoted overlay exists for
// the parent repository. The overlay namespace is reserved speculatively by
// cascade manifests, so only provenance stamped by a completed promotion
// proves an overlay is real.
func ParentOverlayExists(stateDir, parentID, repoName string) bool {
	prov, err := LoadOverlayProvenance(ParentOverlayPath(stateDir, parentID, repoName))
	return err == nil && prov != nil
}

// SaveOverlayProvenance atomically writes overlay provenance to disk.
func SaveOverlayProvenance(overlayDir string, prov *OverlayProvenance) error {
	if err := os.MkdirAll(overlayDir, 0o755); err != nil {
		return fmt.Errorf("creating overlay dir: %w", err)
	}
	data, err := json.MarshalIndent(prov, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling overlay provenance: %w", err)
	}
	path := OverlayStatePath(overlayDir)
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return fmt.Errorf("writing temp overlay provenance: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("renaming overlay provenance: %w", err)
	}
	return nil
}

// LoadWorkspaceState reads the child KB workspace state from disk.
// Returns nil if not found.
func LoadWorkspaceState(workspaceDir string) (*ChildKBWorkspaceState, error) {
	data, err := os.ReadFile(WorkspaceStatePath(workspaceDir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading workspace state: %w", err)
	}
	var state ChildKBWorkspaceState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parsing workspace state: %w", err)
	}
	return &state, nil
}

// SaveWorkspaceState atomically writes the child KB workspace state to disk.
func SaveWorkspaceState(workspaceDir string, state *ChildKBWorkspaceState) error {
	if err := os.MkdirAll(workspaceDir, 0o755); err != nil {
		return fmt.Errorf("creating workspace dir: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling workspace state: %w", err)
	}
	path := WorkspaceStatePath(workspaceDir)
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return fmt.Errorf("writing temp workspace state: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("renaming workspace state: %w", err)
	}
	return nil
}

// AcquireOverlayLock attempts to create a lock file for the overlay directory.
// Returns true if the lock was acquired, false if another child holds it.
// The lock is reentrant: if the same childID already holds it, the timestamp
// is refreshed and the call succeeds.
//
// The operation fails closed: any error reading, parsing, writing, or closing
// the lock payload is returned with context instead of being treated as an
// unlocked overlay, so a corrupt or unwritable lock can never admit a second
// child while a promotion is in flight.
func AcquireOverlayLock(overlayDir, childID string) (bool, error) {
	lockPath := OverlayLockPath(overlayDir)
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return false, fmt.Errorf("creating overlay lock dir: %w", err)
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if os.IsExist(err) {
			data, readErr := os.ReadFile(lockPath)
			if readErr != nil {
				return false, fmt.Errorf("reading existing overlay lock %s: %w", lockPath, readErr)
			}
			var existing OverlayLockInfo
			if err := json.Unmarshal(data, &existing); err != nil {
				return false, fmt.Errorf("parsing existing overlay lock %s: %w", lockPath, err)
			}
			if existing.ChildID != childID {
				return false, nil
			}
			existing.Timestamp = time.Now()
			refreshed, err := json.Marshal(existing)
			if err != nil {
				return false, fmt.Errorf("marshaling refreshed overlay lock %s: %w", lockPath, err)
			}
			if err := os.WriteFile(lockPath, refreshed, 0o644); err != nil {
				return false, fmt.Errorf("refreshing overlay lock %s: %w", lockPath, err)
			}
			return true, nil
		}
		return false, fmt.Errorf("creating overlay lock %s: %w", lockPath, err)
	}
	info := OverlayLockInfo{ChildID: childID, Timestamp: time.Now()}
	data, err := json.Marshal(info)
	if err != nil {
		_ = f.Close()
		_ = os.Remove(lockPath)
		return false, fmt.Errorf("marshaling overlay lock payload: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(lockPath)
		return false, fmt.Errorf("writing overlay lock %s: %w", lockPath, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(lockPath)
		return false, fmt.Errorf("closing overlay lock %s: %w", lockPath, err)
	}
	return true, nil
}

// ReadOverlayLockOwner returns the child ID of the current overlay lock
// holder. It returns "" only when no lock file exists; an unreadable or
// corrupt lock payload is returned as an error so callers fail closed
// instead of treating the overlay as unlocked.
func ReadOverlayLockOwner(overlayDir string) (string, error) {
	data, err := os.ReadFile(OverlayLockPath(overlayDir))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("reading overlay lock: %w", err)
	}
	var info OverlayLockInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return "", fmt.Errorf("parsing overlay lock: %w", err)
	}
	return info.ChildID, nil
}

// ReleaseOverlayLock removes the overlay lock file only if it belongs to the
// given childID. If childID is empty, the lock is forcibly removed (stale
// cleanup). The operation fails closed: an unreadable or corrupt lock payload
// returns a contextual error rather than silently deleting another child's
// potentially valid lock.
func ReleaseOverlayLock(overlayDir, childID string) error {
	lockPath := OverlayLockPath(overlayDir)
	if childID == "" {
		if err := os.Remove(lockPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("removing overlay lock %s: %w", lockPath, err)
		}
		return nil
	}
	data, err := os.ReadFile(lockPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("reading overlay lock %s: %w", lockPath, err)
	}
	var info OverlayLockInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return fmt.Errorf("parsing overlay lock %s: %w", lockPath, err)
	}
	if info.ChildID != childID {
		return nil
	}
	if err := os.Remove(lockPath); err != nil {
		return fmt.Errorf("removing overlay lock %s: %w", lockPath, err)
	}
	return nil
}

// IsOverlayLockStale checks if the overlay lock file is stale.
// A lock is stale if it is corrupt or older than the given timeout.
func IsOverlayLockStale(overlayDir string, timeout time.Duration) bool {
	data, err := os.ReadFile(OverlayLockPath(overlayDir))
	if err != nil {
		return false
	}
	var info OverlayLockInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return true
	}
	return time.Since(info.Timestamp) > timeout
}

// PromotionPhase is the aggregate phase of the promotion journal.
type PromotionPhase string

const (
	PromotionPhasePending   PromotionPhase = "pending"
	PromotionPhasePromoting PromotionPhase = "promoting"
	PromotionPhasePromoted  PromotionPhase = "promoted"
)

// PromotionEntry is the per-repository entry in the promotion journal.
type PromotionEntry struct {
	Repo            string `yaml:"repo"`
	ChildCommit     string `yaml:"child_commit"`
	MergeHEAD       string `yaml:"merge_head"`
	CanonicalCommit string `yaml:"canonical_commit"`
	OverlayPath     string `yaml:"overlay_path"`
	Done            bool   `yaml:"done,omitempty"`
	Error           string `yaml:"error,omitempty"`
}

// PromotionJournal is the durable operation journal stored beside the child's
// feature.yaml at <stateDir>/<childID>/promotion.yaml. It records the
// per-repository promotion of child KB workspaces to parent overlays.
type PromotionJournal struct {
	ChildID  string           `yaml:"child_id"`
	ParentID string           `yaml:"parent_id"`
	Phase    PromotionPhase   `yaml:"phase"`
	Entries  []PromotionEntry `yaml:"entries"`
	// LocksReleased flips true only after every overlay lock recorded in
	// Entries has verifiably been released. A promoted journal without this
	// flag is not complete: a failed final release leaves the phase at
	// promoted with locks still held, and the promotion owner must retry
	// the release before any consumer treats the journal as settled.
	LocksReleased bool      `yaml:"locks_released,omitempty"`
	CreatedAt     time.Time `yaml:"created_at"`
}

// AllPromoted reports whether every per-repo entry is done.
func (p *PromotionJournal) AllPromoted() bool {
	if p == nil || len(p.Entries) == 0 {
		return false
	}
	for i := range p.Entries {
		if !p.Entries[i].Done {
			return false
		}
	}
	return true
}

// EntryByRepo returns a pointer to the promotion entry for the named repo, or nil.
func (p *PromotionJournal) EntryByRepo(repoName string) *PromotionEntry {
	if p == nil {
		return nil
	}
	for i := range p.Entries {
		if p.Entries[i].Repo == repoName {
			return &p.Entries[i]
		}
	}
	return nil
}

// PromotionPath returns the path to the promotion journal file.
func (s *Store) PromotionPath(childID string) string {
	return filepath.Join(s.BaseDir, childID, "promotion.yaml")
}

// LoadPromotion returns the current durable promotion journal, or nil if absent.
func (s *Store) LoadPromotion(childID string) (*PromotionJournal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadPromotionUnlocked(childID)
}

func (s *Store) loadPromotionUnlocked(childID string) (*PromotionJournal, error) {
	data, err := os.ReadFile(s.PromotionPath(childID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading promotion journal: %w", err)
	}
	var journal PromotionJournal
	if err := yaml.Unmarshal(data, &journal); err != nil {
		return nil, fmt.Errorf("unmarshaling promotion journal: %w", err)
	}
	return &journal, nil
}

// SavePromotion atomically persists the promotion journal.
func (s *Store) SavePromotion(childID string, journal *PromotionJournal) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if journal == nil || journal.ChildID != childID {
		return fmt.Errorf("promotion child mismatch")
	}
	return s.savePromotionUnlocked(journal)
}

func (s *Store) savePromotionUnlocked(journal *PromotionJournal) error {
	path := s.PromotionPath(journal.ChildID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating promotion directory: %w", err)
	}
	data, err := yaml.Marshal(journal)
	if err != nil {
		return fmt.Errorf("marshaling promotion journal: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "promotion-*.yaml.tmp")
	if err != nil {
		return fmt.Errorf("creating promotion temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing promotion journal: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing promotion journal: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("committing promotion journal: %w", err)
	}
	return nil
}

// DeletePromotion removes the promotion journal file.
func (s *Store) DeletePromotion(childID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return os.Remove(s.PromotionPath(childID))
}
