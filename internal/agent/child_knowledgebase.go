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
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// ChildKBWorkspacePaths holds the resolved paths for a child KB workspace.
type ChildKBWorkspacePaths struct {
	WorkspaceDir string // per-child per-repo workspace directory
	OverlayDir   string // per-parent per-repo overlay directory
	CanonicalDir string // canonical KB directory for this repo
	RepoPath     string // child repo worktree path
	RepoName     string
}

// ResolveChildKBPaths computes the workspace, overlay, and canonical paths for
// a child's KB build for a given repository.
func ResolveChildKBPaths(stateDir string, child *feature.Feature, repo feature.FeatureRepo) ChildKBWorkspacePaths {
	parentID := ""
	if child.Parent != nil {
		parentID = child.Parent.ParentID
	}
	repoPath := repo.WorktreePath
	if repoPath == "" {
		repoPath = repo.Path
	}
	return ChildKBWorkspacePaths{
		WorkspaceDir: feature.ChildKBWorkspaceDir(stateDir, child.ID, repo.Name),
		OverlayDir:   feature.ParentOverlayPath(stateDir, parentID, repo.Name),
		CanonicalDir: KBStateDir(stateDir, repo.Name),
		RepoPath:     repoPath,
		RepoName:     repo.Name,
	}
}

// IsWorkspaceFresh checks if the child KB workspace KB has been built through
// the child repo's current HEAD. Freshness is determined by AnalyzedCommit
// (set only by the completion path), not by SeedBaseCommit (set during seeding).
func IsWorkspaceFresh(ctx context.Context, runner ports.CommandRunner, workspaceDir, repoPath string) bool {
	state, err := feature.LoadWorkspaceState(workspaceDir)
	if err != nil || state == nil {
		return false
	}
	if state.AnalyzedCommit == "" {
		return false
	}
	if _, err := os.Stat(KBPath(workspaceDir)); err != nil {
		return false
	}
	currentCommit, err := GetCurrentCommit(ctx, runner, repoPath)
	if err != nil {
		return false
	}
	return state.AnalyzedCommit == currentCommit
}

// MarkWorkspaceFresh saves the workspace state with the current child HEAD
// commit as the analyzed-through commit. This should only be called by the
// completion path after the KB builder has successfully finished.
func MarkWorkspaceFresh(ctx context.Context, runner ports.CommandRunner, workspaceDir, repoPath string) error {
	commit, err := GetCurrentCommit(ctx, runner, repoPath)
	if err != nil {
		return fmt.Errorf("getting current commit: %w", err)
	}
	existing, err := feature.LoadWorkspaceState(workspaceDir)
	if err != nil {
		return fmt.Errorf("loading workspace state for freshness update: %w", err)
	}
	// Start from a default state and copy provenance from the existing
	// state when present, distinguishing missing state (existing == nil)
	// from corrupt or unreadable state (err != nil, handled above).
	updated := &feature.ChildKBWorkspaceState{
		Source:          feature.WorkspaceSourceFull,
		AnalyzedCommit:  commit,
		LastUpdated:      time.Now(),
	}
	if existing != nil {
		updated.Source = existing.Source
		updated.CanonicalCommit = existing.CanonicalCommit
		updated.ParentHEAD = existing.ParentHEAD
		updated.SeedBaseCommit = existing.SeedBaseCommit
		updated.SeededAt = existing.SeededAt
	}
	return feature.SaveWorkspaceState(workspaceDir, updated)
}

// HasWorkspaceChanges checks whether there are git changes since the last
// workspace KB build. Returns true (assume changes) when state or index.md is
// missing, or on any error.
func HasWorkspaceChanges(ctx context.Context, runner ports.CommandRunner, workspaceDir, repoPath string) (bool, error) {
	state, err := feature.LoadWorkspaceState(workspaceDir)
	if err != nil || state == nil || state.AnalyzedCommit == "" {
		return true, nil
	}
	if _, err := os.Stat(KBPath(workspaceDir)); err != nil {
		return true, nil
	}
	out, err := runner.Run(ctx, "git", []string{"log", "--oneline", state.AnalyzedCommit + "..HEAD"}, ports.CommandOpts{Dir: repoPath})
	if err != nil {
		return true, nil
	}
	if len(string(out)) > 0 {
		return true, nil
	}
	return false, nil
}

// IsAncestor checks whether ancestorCommit is an ancestor of descendantCommit
// in the given repo path. Returns false on any error.
func IsAncestor(ctx context.Context, runner ports.CommandRunner, repoPath, ancestorCommit, descendantCommit string) bool {
	if ancestorCommit == "" || descendantCommit == "" {
		return false
	}
	_, err := runner.Run(ctx, "git", []string{"merge-base", "--is-ancestor", ancestorCommit, descendantCommit}, ports.CommandOpts{Dir: repoPath})
	return err == nil
}

// SeedWorkspaceFromOverlay copies a valid parent overlay into the child workspace
// directory. The overlay must be provenance-valid: its canonical commit must
// still match the canonical KB state, and its parent HEAD must be an ancestor
// of the child snapshot.
func SeedWorkspaceFromOverlay(ctx context.Context, runner ports.CommandRunner, paths ChildKBWorkspacePaths, childCommit string) error {
	overlayProv, err := feature.LoadOverlayProvenance(paths.OverlayDir)
	if err != nil {
		return fmt.Errorf("loading overlay provenance: %w", err)
	}
	if overlayProv == nil {
		return errors.New("overlay provenance missing")
	}
	// Validate canonical provenance: the overlay's canonical commit must match
	// the current canonical KB state.
	canonState, err := LoadKBState(paths.CanonicalDir)
	if err != nil {
		return fmt.Errorf("loading canonical KB state: %w", err)
	}
	if canonState == nil || canonState.HeadCommit != overlayProv.CanonicalCommit {
		return errors.New("overlay canonical provenance is stale")
	}
	// Validate the overlay's parent HEAD is an ancestor of the child snapshot.
	if !IsAncestor(ctx, runner, paths.RepoPath, overlayProv.ParentHEAD, childCommit) {
		return errors.New("overlay parent HEAD is not an ancestor of child snapshot")
	}
	// Copy the overlay into the workspace.
	if err := os.MkdirAll(paths.WorkspaceDir, 0o755); err != nil {
		return fmt.Errorf("creating workspace dir: %w", err)
	}
	if err := copyTree(paths.OverlayDir, paths.WorkspaceDir); err != nil {
		return fmt.Errorf("copying overlay to workspace: %w", err)
	}
	// Record workspace provenance. SeedBaseCommit is the child HEAD at seed
	// time; AnalyzedCommit is left empty because the builder has not yet run.
	now := time.Now()
	return feature.SaveWorkspaceState(paths.WorkspaceDir, &feature.ChildKBWorkspaceState{
		Source:          feature.WorkspaceSourceOverlay,
		CanonicalCommit: overlayProv.CanonicalCommit,
		ParentHEAD:      overlayProv.ParentHEAD,
		SeedBaseCommit:  childCommit,
		SeededAt:        now,
		LastUpdated:     now,
	})
}

// SeedWorkspaceFromCanonical copies the latest canonical KB into the child
// workspace directory and records the canonical commit as the baseline. Only
// the canonical-to-child commit delta needs to be analyzed afterward.
func SeedWorkspaceFromCanonical(ctx context.Context, runner ports.CommandRunner, paths ChildKBWorkspacePaths, childCommit string) error {
	canonState, err := LoadKBState(paths.CanonicalDir)
	if err != nil {
		return fmt.Errorf("loading canonical KB state: %w", err)
	}
	if canonState == nil || canonState.HeadCommit == "" {
		return errors.New("canonical KB state missing")
	}
	// The canonical commit must be an ancestor of the child snapshot.
	if !IsAncestor(ctx, runner, paths.RepoPath, canonState.HeadCommit, childCommit) {
		return errors.New("canonical commit is not an ancestor of child snapshot")
	}
	if err := os.MkdirAll(paths.WorkspaceDir, 0o755); err != nil {
		return fmt.Errorf("creating workspace dir: %w", err)
	}
	if err := copyTree(paths.CanonicalDir, paths.WorkspaceDir); err != nil {
		return fmt.Errorf("copying canonical KB to workspace: %w", err)
	}
	now := time.Now()
	return feature.SaveWorkspaceState(paths.WorkspaceDir, &feature.ChildKBWorkspaceState{
		Source:          feature.WorkspaceSourceCanonical,
		CanonicalCommit: canonState.HeadCommit,
		ParentHEAD:      "",
		SeedBaseCommit:  childCommit,
		SeededAt:        now,
		LastUpdated:     now,
	})
}

// SeedWorkspaceFull creates an empty workspace directory for a full KB build.
// This is used when neither a valid overlay nor a valid canonical baseline exists.
func SeedWorkspaceFull(paths ChildKBWorkspacePaths, childCommit string) error {
	if err := os.MkdirAll(paths.WorkspaceDir, 0o755); err != nil {
		return fmt.Errorf("creating workspace dir: %w", err)
	}
	now := time.Now()
	return feature.SaveWorkspaceState(paths.WorkspaceDir, &feature.ChildKBWorkspaceState{
		Source:         feature.WorkspaceSourceFull,
		SeedBaseCommit: childCommit,
		SeededAt:       now,
		LastUpdated:    now,
	})
}

// SeedChildKBWorkspace seeds a child KB workspace from the best available
// source. It tries the parent overlay first (when provenance-valid), then the
// canonical KB (when its commit is an ancestor of the child snapshot), and
// falls back to a full build only when neither is valid.
func SeedChildKBWorkspace(ctx context.Context, runner ports.CommandRunner, paths ChildKBWorkspacePaths) error {
	// Get the current child HEAD commit.
	childCommit, err := GetCurrentCommit(ctx, runner, paths.RepoPath)
	if err != nil {
		return fmt.Errorf("getting child HEAD: %w", err)
	}

	// If the overlay directory exists and is locked by another child (e.g.,
	// a pending promotion), block all seeding paths — both overlay and
	// canonical. A later child must wait for the preceding promotion to
	// complete so it seeds from the newest overlay generation rather than
	// a stale canonical baseline.
	if owner := feature.ReadOverlayLockOwner(paths.OverlayDir); owner != "" {
		return fmt.Errorf("overlay for repo %s is locked by child %s: %w", paths.RepoName, owner, feature.ErrOverlayLocked)
	}

	// Try seeding from the parent overlay first.
	if _, statErr := os.Stat(paths.OverlayDir); statErr == nil {
		if overlayProv, _ := feature.LoadOverlayProvenance(paths.OverlayDir); overlayProv != nil {
			canonState, _ := LoadKBState(paths.CanonicalDir)
			if canonState != nil && canonState.HeadCommit == overlayProv.CanonicalCommit &&
				IsAncestor(ctx, runner, paths.RepoPath, overlayProv.ParentHEAD, childCommit) {
				if err := SeedWorkspaceFromOverlay(ctx, runner, paths, childCommit); err != nil {
					return fmt.Errorf("seeding from overlay: %w", err)
				}
				return nil
			}
		}
	}

	// Try seeding from the canonical KB.
	if canonState, _ := LoadKBState(paths.CanonicalDir); canonState != nil && canonState.HeadCommit != "" {
		if IsAncestor(ctx, runner, paths.RepoPath, canonState.HeadCommit, childCommit) {
			if _, err := os.Stat(KBPath(paths.CanonicalDir)); err == nil {
				if err := SeedWorkspaceFromCanonical(ctx, runner, paths, childCommit); err != nil {
					return fmt.Errorf("seeding from canonical: %w", err)
				}
				return nil
			}
		}
	}

	// Fall back to a full build.
	return SeedWorkspaceFull(paths, childCommit)
}

// copyTree copies all regular files from src to dst, preserving directory
// structure. It skips lock files, state files that are specific to the source
// (overlay.lock, kb.lock), and temp files.
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		// Skip the root itself.
		if rel == "." {
			return nil
		}
		// Skip lock files and source-specific state files.
		base := filepath.Base(path)
		if base == "kb.lock" || base == "overlay.lock" || base == "workspace.lock" ||
			base == "phase_complete" || base == "output.txt" {
			return nil
		}
		// Skip the canonical KB's state.json (we write our own workspace state).
		if base == "state.json" {
			return nil
		}
		// Skip protocol retry sidecars.
		if len(base) > 16 && base[:16] == ".protocol-retry-" {
			return nil
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		if info.Mode()&os.ModeSymlink != 0 {
			linkTarget, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(linkTarget, target)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		return copyFile(path, target, info.Mode())
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()
	_, err = io.Copy(out, in)
	return err
}

// StageWorkspaceToOverlay builds the new overlay in a temp directory adjacent
// to the target without swapping it into place. The caller must later call
// CommitStagedOverlay to atomically replace the existing overlay. This split
// allows all repos in a multi-repository promotion to stage first, then commit
// together so readers never observe a partially promoted overlay set.
func StageWorkspaceToOverlay(workspaceDir, overlayDir, mergeHEAD, canonicalCommit string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(overlayDir), 0o755); err != nil {
		return "", fmt.Errorf("creating overlay parent dir: %w", err)
	}
	tmpDir := overlayDir + ".promoting"
	_ = os.RemoveAll(tmpDir)
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return "", fmt.Errorf("creating promotion temp dir: %w", err)
	}
	if err := copyTree(workspaceDir, tmpDir); err != nil {
		_ = os.RemoveAll(tmpDir)
		return "", fmt.Errorf("copying workspace to promotion temp: %w", err)
	}
	existingProv, _ := feature.LoadOverlayProvenance(overlayDir)
	generation := 1
	if existingProv != nil && existingProv.Generation >= generation {
		generation = existingProv.Generation + 1
	}
	now := time.Now()
	prov := &feature.OverlayProvenance{
		CanonicalCommit: canonicalCommit,
		ParentHEAD:      mergeHEAD,
		Generation:      generation,
		CreatedAt:       now,
	}
	if err := feature.SaveOverlayProvenance(tmpDir, prov); err != nil {
		_ = os.RemoveAll(tmpDir)
		return "", fmt.Errorf("writing overlay provenance: %w", err)
	}
	return tmpDir, nil
}

// CommitStagedOverlay atomically replaces the existing overlay with the staged
// temp directory via rename. The caller should re-acquire the overlay lock
// after a successful commit because the lock file lives inside the overlay
// directory and is destroyed during the rename.
func CommitStagedOverlay(tmpDir, overlayDir string) error {
	backupDir := overlayDir + ".old"
	_ = os.RemoveAll(backupDir)
	if _, err := os.Stat(overlayDir); err == nil {
		if err := os.Rename(overlayDir, backupDir); err != nil {
			_ = os.RemoveAll(tmpDir)
			return fmt.Errorf("renaming old overlay: %w", err)
		}
	}
	if err := os.Rename(tmpDir, overlayDir); err != nil {
		if _, backErr := os.Stat(backupDir); backErr == nil {
			_ = os.Rename(backupDir, overlayDir)
		}
		_ = os.RemoveAll(tmpDir)
		return fmt.Errorf("renaming promotion temp to overlay: %w", err)
	}
	_ = os.RemoveAll(backupDir)
	return nil
}

// PromoteWorkspaceToOverlay atomically replaces the parent overlay with the
// child's refreshed workspace, stamping the resulting parent merge HEAD and
// canonical commit. This is a convenience wrapper that stages and commits in
// one call. Multi-repository promotion should use StageWorkspaceToOverlay +
// CommitStagedOverlay to keep all overlays locked until the entire vector
// is committed.
func PromoteWorkspaceToOverlay(workspaceDir, overlayDir, mergeHEAD, canonicalCommit string) error {
	tmpDir, err := StageWorkspaceToOverlay(workspaceDir, overlayDir, mergeHEAD, canonicalCommit)
	if err != nil {
		return err
	}
	return CommitStagedOverlay(tmpDir, overlayDir)
}

// RemoveWorkspace removes a child KB workspace directory.
func RemoveWorkspace(workspaceDir string) error {
	return os.RemoveAll(workspaceDir)
}

// RemoveOverlay removes a parent overlay directory.
func RemoveOverlay(overlayDir string) error {
	_ = os.RemoveAll(overlayDir + ".old")
	_ = os.RemoveAll(overlayDir + ".promoting")
	return os.RemoveAll(overlayDir)
}

// WorkspaceKBInfo builds a KBInfo pointing at the child's disposable workspace
// instead of the canonical KB. Used to pass the workspace as read-only context
// to downstream roles.
func WorkspaceKBInfo(repoName, workspaceDir string) KBInfo {
	return KBInfo{
		Name:      repoName,
		IndexPath: KBPath(workspaceDir),
		RootDir:   workspaceDir,
	}
}

// CanonicalKBCommit returns the current canonical KB head commit, or "" if
// the canonical KB is missing or unreadable.
func CanonicalKBCommit(canonicalDir string) string {
	state, err := LoadKBState(canonicalDir)
	if err != nil || state == nil {
		return ""
	}
	return state.HeadCommit
}
