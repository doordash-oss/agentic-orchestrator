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
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent/roles"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// KBState tracks the state of a repo-scoped knowledge base.
type KBState struct {
	HeadCommit  string    `json:"head_commit"`
	LastUpdated time.Time `json:"last_updated"`
	Version     int       `json:"version"`
}

// KBStateDir returns the directory for a repo's knowledge base.
// The KB is stored outside the per-feature directory since it is repo-scoped.
func KBStateDir(stateDir, repoName string) string {
	return filepath.Join(filepath.Dir(stateDir), "knowledge-base", repoName)
}

var validKBSessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,200}$`)

// BuildKBSessionID returns a desktop- and URL-safe session ID for a per-repo
// KB build. It preserves the legacy "<featureID>-kb-<repoName>" form when the
// repository name is already safe. Qualified discovery keys and other names
// containing unsafe characters use a readable slug plus a collision-resistant
// hash; callers must use the session's RepoName metadata as canonical identity.
func BuildKBSessionID(featureID, repoName string) string {
	legacyID := fmt.Sprintf("%s-kb-%s", featureID, repoName)
	if validKBSessionIDPattern.MatchString(legacyID) {
		return legacyID
	}

	safeFeatureID := feature.Slugify(featureID)
	if safeFeatureID == "" {
		safeFeatureID = "feature"
	}
	safeRepoName := feature.Slugify(repoName)
	if safeRepoName == "" {
		safeRepoName = "repo"
	}
	digest := sha256.Sum256([]byte(repoName))
	return fmt.Sprintf("%s-kbv2-%s-%x", safeFeatureID, safeRepoName, digest[:6])
}

// RepoNameFromKBSession extracts the repo name from a per-repo KB session ID.
// Format: "<featureID>-kb-<repoName>" → "<repoName>". Returns "" when the
// session ID does not use that legacy, reversible format. New encoded IDs keep
// the canonical repository name in session metadata instead.
func RepoNameFromKBSession(sessionID string) string {
	idx := strings.Index(sessionID, "-kb-")
	if idx < 0 {
		return ""
	}
	return sessionID[idx+4:]
}

// KBPath returns the path to the knowledge base index document.
func KBPath(kbDir string) string {
	return filepath.Join(kbDir, "index.md")
}

// kbStandardCategories lists the standard KB category subdirectories created
// by the build-knowledge-base skill. Pre-creating them avoids Bash mkdir
// permission prompts during the KB session.
var kbStandardCategories = []string{
	"architecture",
	"conventions",
	"api-surface",
	"dependencies",
	"verification",
}

// KBRootDir returns the root directory of the knowledge base graph.
// Downstream agents browse this directory for category subdirectories.
func KBRootDir(kbDir string) string {
	return kbDir
}

// KBInfo holds paths for a repo's knowledge base graph.
type KBInfo struct {
	Name      string // repo name this KB belongs to (rendered in Useful Resources)
	IndexPath string // absolute path to the top-level index.md
	RootDir   string // absolute path to the KB root directory (for browsing)
}

// KBLockPath returns the path to the knowledge base lock file.
func KBLockPath(kbDir string) string {
	return filepath.Join(kbDir, "kb.lock")
}

// LoadKBState reads the KB state from disk. Returns nil if not found.
func LoadKBState(kbDir string) (*KBState, error) {
	data, err := os.ReadFile(filepath.Join(kbDir, "state.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading KB state: %w", err)
	}
	var state KBState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parsing KB state: %w", err)
	}
	return &state, nil
}

// SaveKBState atomically writes KB state to disk.
func SaveKBState(kbDir string, state *KBState) error {
	if err := os.MkdirAll(kbDir, 0o755); err != nil {
		return fmt.Errorf("creating KB dir: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling KB state: %w", err)
	}
	path := filepath.Join(kbDir, "state.json")
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return fmt.Errorf("writing temp KB state: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("renaming KB state: %w", err)
	}
	return nil
}

// KBLockInfo holds information about who holds the KB lock.
type KBLockInfo struct {
	FeatureID string    `json:"feature_id"`
	Timestamp time.Time `json:"timestamp"`
}

var ErrKBLocked = errors.New("knowledge base is already being built")

var kbLockCoordinator sync.Map // map[string]*sync.Mutex

func kbLockMutex(kbDir string) *sync.Mutex {
	key, err := filepath.Abs(kbDir)
	if err != nil {
		key = kbDir
	}
	mu, _ := kbLockCoordinator.LoadOrStore(key, &sync.Mutex{})
	return mu.(*sync.Mutex)
}

// AcquireKBLock attempts to create a lock file for the KB directory.
// Returns true if the lock was acquired, false if another feature holds it.
// The lock is reentrant: if the same featureID already holds it, the
// timestamp is refreshed and the call succeeds.
func AcquireKBLock(kbDir, featureID string) (bool, error) {
	return AcquireKBLockWithStatus(kbDir, featureID, nil)
}

// AcquireKBLockWithStatus attempts to create a lock file, reclaiming an
// existing lock only when the owner can be proven stale by statusFn, when the
// lock is corrupt, or when no statusFn is available and the timestamp exceeds
// the age fallback. Reclamation and acquisition are serialized per KB path so
// concurrent contenders cannot both steal the same lock.
func AcquireKBLockWithStatus(kbDir, featureID string, statusFn FeatureStatusFunc) (bool, error) {
	mu := kbLockMutex(kbDir)
	mu.Lock()
	defer mu.Unlock()

	if err := os.MkdirAll(kbDir, 0o755); err != nil {
		return false, fmt.Errorf("creating KB dir: %w", err)
	}
	lockPath := KBLockPath(kbDir)
	return acquireKBLockLocked(lockPath, featureID, statusFn)
}

func acquireKBLockLocked(lockPath, featureID string, statusFn FeatureStatusFunc) (bool, error) {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if os.IsExist(err) {
			// Reentrant: if we already own the lock, refresh and succeed
			data, readErr := os.ReadFile(lockPath)
			if readErr == nil {
				var existing KBLockInfo
				if json.Unmarshal(data, &existing) == nil && existing.FeatureID == featureID {
					existing.Timestamp = time.Now()
					refreshed, _ := json.Marshal(existing)
					_ = os.WriteFile(lockPath, refreshed, 0o644)
					return true, nil
				}
			}
			if isKBLockStaleData(data, readErr, statusFn) {
				if removeErr := os.Remove(lockPath); removeErr != nil && !os.IsNotExist(removeErr) {
					return false, fmt.Errorf("removing stale lock file: %w", removeErr)
				}
				f, err = os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
				if err != nil {
					if os.IsExist(err) {
						return false, nil
					}
					return false, fmt.Errorf("creating lock file after stale cleanup: %w", err)
				}
				defer func() { _ = f.Close() }()
				info := KBLockInfo{FeatureID: featureID, Timestamp: time.Now()}
				refreshed, _ := json.Marshal(info)
				_, _ = f.Write(refreshed)
				return true, nil
			}
			return false, nil
		}
		return false, fmt.Errorf("creating lock file: %w", err)
	}
	defer func() { _ = f.Close() }()

	info := KBLockInfo{FeatureID: featureID, Timestamp: time.Now()}
	data, _ := json.Marshal(info)
	_, _ = f.Write(data)
	return true, nil
}

// ReadKBLockOwner returns the feature ID of the current lock holder, or "" if
// the lock file is missing or unreadable. Used by startKB to attribute
// ErrKBLocked to a specific waiting feature without taking the lock itself.
func ReadKBLockOwner(kbDir string) string {
	data, err := os.ReadFile(KBLockPath(kbDir))
	if err != nil {
		return ""
	}
	var info KBLockInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return ""
	}
	return info.FeatureID
}

// ReleaseKBLock removes the lock file only if it belongs to the given featureID.
// If featureID is empty, the lock is forcibly removed (for stale lock cleanup).
func ReleaseKBLock(kbDir, featureID string) error {
	_, err := ReleaseKBLockIfOwned(kbDir, featureID)
	return err
}

// ReleaseKBLockIfOwned removes the lock file only if it belongs to featureID
// and reports whether a lock was actually removed. The owner check and remove
// are serialized with acquisition so cleanup cannot remove a replacement lock.
func ReleaseKBLockIfOwned(kbDir, featureID string) (bool, error) {
	mu := kbLockMutex(kbDir)
	mu.Lock()
	defer mu.Unlock()

	lockPath := KBLockPath(kbDir)
	if featureID == "" {
		if err := os.Remove(lockPath); err != nil {
			if os.IsNotExist(err) {
				return false, nil
			}
			return false, err
		}
		return true, nil
	}
	data, err := os.ReadFile(lockPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("reading lock file: %w", err)
	}
	var info KBLockInfo
	if err := json.Unmarshal(data, &info); err != nil {
		// Corrupt lock — remove it
		if err := os.Remove(lockPath); err != nil && !os.IsNotExist(err) {
			return false, err
		}
		return true, nil
	}
	if info.FeatureID != featureID {
		return false, nil // not our lock
	}
	if err := os.Remove(lockPath); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// FeatureStatusFunc is a callback that returns a feature's current status.
// Returns the status and true if found, or zero value and false if not found.
type FeatureStatusFunc func(featureID string) (int, bool)

// IsKBLockStale checks if the lock file is stale. A lock is stale if:
// - The lock file is corrupt
// - The lock owner feature no longer exists or is not in StatusBuildingKB
// - The lock is older than 30 minutes and no statusFn is available
// The statusFn parameter looks up a feature's status; if nil, only time-based
// staleness is checked. A confirmed BuildingKB owner is authoritative even
// when the timestamp is old because the timestamp is not a heartbeat.
func IsKBLockStale(kbDir string, statusFn FeatureStatusFunc) bool {
	mu := kbLockMutex(kbDir)
	mu.Lock()
	defer mu.Unlock()

	lockPath := KBLockPath(kbDir)
	data, err := os.ReadFile(lockPath)
	return isKBLockStaleData(data, err, statusFn)
}

func isKBLockStaleData(data []byte, readErr error, statusFn FeatureStatusFunc) bool {
	if readErr != nil {
		return false
	}
	var info KBLockInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return true // corrupt lock is stale
	}
	if statusFn != nil {
		if info.FeatureID == "" {
			return true
		}
		status, found := statusFn(info.FeatureID)
		if !found {
			return true // owner feature doesn't exist
		}
		if status != int(feature.StatusBuildingKB) {
			return true // owner is no longer building KB
		}
		return false
	}
	return time.Since(info.Timestamp) > 30*time.Minute
}

// IsKBFresh checks if the KB is up-to-date with the repo's current HEAD.
// Requires both a matching commit in state.json AND an existing index.md file.
func IsKBFresh(ctx context.Context, runner ports.CommandRunner, kbDir, repoPath string) bool {
	state, err := LoadKBState(kbDir)
	if err != nil || state == nil {
		return false
	}
	// Verify the KB document actually exists (not just a stale state from a failed run)
	if _, err := os.Stat(KBPath(kbDir)); err != nil {
		return false
	}
	currentCommit, err := GetCurrentCommit(ctx, runner, repoPath)
	if err != nil {
		return false
	}
	return state.HeadCommit == currentCommit
}

// MarkKBFresh saves the KB state with the current HEAD commit.
// Should only be called after confirmed successful KB generation.
func MarkKBFresh(ctx context.Context, runner ports.CommandRunner, kbDir, repoPath string) error {
	commit, err := GetCurrentCommit(ctx, runner, repoPath)
	if err != nil {
		return fmt.Errorf("getting current commit: %w", err)
	}
	return SaveKBState(kbDir, &KBState{
		HeadCommit:  commit,
		LastUpdated: time.Now(),
		Version:     1,
	})
}

// GetCurrentCommit returns the current HEAD commit hash for the given repo.
func GetCurrentCommit(ctx context.Context, runner ports.CommandRunner, repoPath string) (string, error) {
	out, err := runner.Run(ctx, "git", []string{"rev-parse", "HEAD"}, ports.CommandOpts{Dir: repoPath})
	if err != nil {
		return "", fmt.Errorf("git rev-parse HEAD: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// HasKBChanges checks whether there are actual git changes since the last KB build.
// Returns true (assume changes) when state or index.md is missing, or on any error.
// Returns false only when state.json and index.md exist and git log shows no commits
// between the stored HEAD and the current HEAD.
func HasKBChanges(ctx context.Context, runner ports.CommandRunner, kbDir, repoPath string) (bool, error) {
	state, err := LoadKBState(kbDir)
	if err != nil || state == nil || state.HeadCommit == "" {
		return true, nil // can't determine, assume changes
	}
	if _, err := os.Stat(KBPath(kbDir)); err != nil {
		return true, nil // no index.md, need full build
	}
	// Check for commits between stored HEAD and current HEAD
	out, err := runner.Run(ctx, "git", []string{"log", "--oneline", state.HeadCommit + "..HEAD"}, ports.CommandOpts{Dir: repoPath})
	if err != nil {
		return true, nil // git error, assume changes
	}
	if len(strings.TrimSpace(string(out))) > 0 {
		return true, nil
	}
	return false, nil
}

// BuildKBPrompt constructs the user prompt for the KB agent.
// For full builds (no existing KB), it instructs the agent to analyze the entire repo.
// For incremental updates (existing KB + last commit), it provides context for a targeted update.
//
// The prose lives in internal/agent/prompts/templates/kb_build.user.tmpl.
func BuildKBPrompt(repoName, repoPath, kbDir string, existingKBPath string, lastCommit string) string {
	return roles.BuildKBBuildPrompt(roles.KBBuildUserInput{
		RepoName:       repoName,
		RepoPath:       repoPath,
		KBRootDir:      KBRootDir(kbDir),
		KBIndexPath:    KBPath(kbDir),
		ExistingKBPath: existingKBPath,
		LastCommit:     lastCommit,
	})
}
