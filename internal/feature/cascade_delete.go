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
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"gopkg.in/yaml.v3"
)

// CascadeDeleteStatus is the convergent externally visible cascade outcome.
type CascadeDeleteStatus string

const (
	CascadeDeleteCompleted         CascadeDeleteStatus = "completed"
	CascadeDeleteCleanupPending    CascadeDeleteStatus = "cleanup_pending"
	CascadeDeleteAttentionRequired CascadeDeleteStatus = "attention_required"
)

// CascadeDeleteStep is the last durable state-machine boundary.
type CascadeDeleteStep string

const (
	CascadeStepIntentPersisted  CascadeDeleteStep = "intent_persisted"
	CascadeStepSessionsQuiesced CascadeDeleteStep = "sessions_quiesced"
	CascadeStepAttentionSettled CascadeDeleteStep = "attention_settled"
	CascadeStepRefsSafe         CascadeDeleteStep = "refs_safe"
	CascadeStepResourcesCleaned CascadeDeleteStep = "resources_cleaned"
	CascadeStepChildrenDeleted  CascadeDeleteStep = "children_deleted"
)

// CascadeResourceKind classifies one disposable resource in the manifest.
type CascadeResourceKind string

const (
	CascadeResourceCopiedInput CascadeResourceKind = "copied_input"
	CascadeResourceWorktree    CascadeResourceKind = "worktree"
	CascadeResourceBranch      CascadeResourceKind = "branch"
	CascadeResourceOverlay     CascadeResourceKind = "overlay"
	CascadeResourceRecord      CascadeResourceKind = "record"
	CascadeResourceKBWorkspace CascadeResourceKind = "kb_workspace"
	CascadeResourcePromotion   CascadeResourceKind = "promotion"
)

// CascadeResource is one independently retryable manifest item.
type CascadeResource struct {
	ID       string              `yaml:"id" json:"id"`
	Kind     CascadeResourceKind `yaml:"kind" json:"kind"`
	OwnerID  string              `yaml:"owner_id" json:"owner_id"`
	Repo     string              `yaml:"repo,omitempty" json:"repo,omitempty"`
	Path     string              `yaml:"path,omitempty" json:"path,omitempty"`
	RepoPath string              `yaml:"repo_path,omitempty" json:"repo_path,omitempty"`
	Branch   string              `yaml:"branch,omitempty" json:"branch,omitempty"`
	Done     bool                `yaml:"done,omitempty" json:"done,omitempty"`
	Error    string              `yaml:"error,omitempty" json:"error,omitempty"`
}

// CascadeRef records enough identity to classify and conditionally restore a
// candidate-bearing parent ref without ever overwriting external movement.
type CascadeRef struct {
	ChildID      string `yaml:"child_id" json:"child_id"`
	Repo         string `yaml:"repo" json:"repo"`
	RepoPath     string `yaml:"repo_path" json:"repo_path"`
	Ref          string `yaml:"ref" json:"ref"`
	AnchorSHA    string `yaml:"anchor_sha" json:"anchor_sha"`
	CandidateSHA string `yaml:"candidate_sha" json:"candidate_sha"`
	ObservedSHA  string `yaml:"observed_sha,omitempty" json:"observed_sha,omitempty"`
	Safe         bool   `yaml:"safe,omitempty" json:"safe,omitempty"`
	Restored     bool   `yaml:"restored,omitempty" json:"restored,omitempty"`
	Diagnostic   string `yaml:"diagnostic,omitempty" json:"diagnostic,omitempty"`
}

// CascadeDiagnostic is an exact, machine-readable cleanup or ref-safety issue.
type CascadeDiagnostic struct {
	Code         string `yaml:"code" json:"code"`
	Message      string `yaml:"message" json:"message"`
	Repo         string `yaml:"repo,omitempty" json:"repo,omitempty"`
	Ref          string `yaml:"ref,omitempty" json:"ref,omitempty"`
	AnchorSHA    string `yaml:"anchor_sha,omitempty" json:"anchor_sha,omitempty"`
	CandidateSHA string `yaml:"candidate_sha,omitempty" json:"candidate_sha,omitempty"`
	ObservedSHA  string `yaml:"observed_sha,omitempty" json:"observed_sha,omitempty"`
}

// CascadeDeleteIntent is the durable operation journal stored beside the
// parent's feature.yaml. Its manifest is immutable after creation; progress,
// diagnostics, and per-resource completion are updated atomically.
type CascadeDeleteIntent struct {
	OperationID string              `yaml:"operation_id" json:"operation_id"`
	ParentID    string              `yaml:"parent_id" json:"parent_id"`
	RequestedAt time.Time           `yaml:"requested_at" json:"requested_at"`
	Status      CascadeDeleteStatus `yaml:"status" json:"status"`
	Step        CascadeDeleteStep   `yaml:"step" json:"step"`
	ChildIDs    []string            `yaml:"child_ids" json:"child_ids"`
	Resources   []CascadeResource   `yaml:"resources" json:"resources"`
	Refs        []CascadeRef        `yaml:"refs,omitempty" json:"refs,omitempty"`
	Diagnostics []CascadeDiagnostic `yaml:"diagnostics,omitempty" json:"diagnostics,omitempty"`
}

// CascadeDeleteResult is returned by every request, including retries.
type CascadeDeleteResult struct {
	OperationID string              `json:"operation_id"`
	ParentID    string              `json:"feature_id"`
	Status      CascadeDeleteStatus `json:"status"`
	Diagnostics []CascadeDiagnostic `json:"diagnostics,omitempty"`
}

// ParentOverlayPath returns the stable parent/repository overlay namespace.
// Cascade cleanup owns this path even when the overlay has not been seeded.
func ParentOverlayPath(stateDir, parentID, repoName string) string {
	return filepath.Join(filepath.Dir(stateDir), "overlays", parentID, repoName)
}

// CanonicalPath resolves symlinks in path so journaled paths and later guard
// comparisons agree on one spelling of the state root. When a component does
// not exist it resolves the deepest existing ancestor and rejoins the rest;
// unresolvable paths are returned cleaned.
func CanonicalPath(path string) string {
	clean := filepath.Clean(path)
	current := clean
	var suffix []string
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return filepath.Clean(resolved)
		}
		if !os.IsNotExist(err) {
			return clean
		}
		parent := filepath.Dir(current)
		if parent == current {
			return clean
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}

func (s *Store) cascadeDeletePath(parentID string) string {
	return filepath.Join(s.BaseDir, parentID, "cascade-delete.yaml")
}

// BeginCascadeDelete atomically snapshots the complete relationship and
// resource manifest before returning. Repeated calls return the first intent.
func (s *Store) BeginCascadeDelete(parentID string, now time.Time) (*CascadeDeleteIntent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if intent, err := s.loadCascadeDeleteUnlocked(parentID); err == nil {
		return intent, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	parent, err := s.loadUnlocked(parentID)
	if err != nil {
		return nil, fmt.Errorf("loading cascade parent: %w", err)
	}
	if parent.IsChild() {
		if err := validateChildRelationship(parent); err != nil {
			return nil, err
		}
		if !parent.IsActiveChild() {
			return nil, fmt.Errorf("%w: delete is not permitted on closed child %s", ErrChildRelationshipClosed, parent.ID)
		}
		return nil, fmt.Errorf("%w: delete is not permitted on child %s", ErrChildMutationRestricted, parent.ID)
	}

	relationship, err := s.validatedRelationshipChildrenUnlocked(parentID)
	if err != nil {
		return nil, fmt.Errorf("validating cascade relationships: %w", err)
	}
	children := append([]*Feature(nil), relationship.Closed...)
	if relationship.Active != nil {
		children = append(children, relationship.Active)
	}
	sort.Slice(children, func(i, j int) bool { return children[i].ID < children[j].ID })

	intent := &CascadeDeleteIntent{
		OperationID: "cascade:" + parentID,
		ParentID:    parentID, RequestedAt: now.UTC(),
		Status: CascadeDeleteCleanupPending, Step: CascadeStepIntentPersisted,
	}
	for _, child := range children {
		intent.ChildIDs = append(intent.ChildIDs, child.ID)
	}
	stateDir := CanonicalPath(s.BaseDir)
	for _, child := range children {
		appendFeatureCascadeManifest(intent, stateDir, child, false)
		appendCascadeRefs(intent, parent, child)
	}
	appendFeatureCascadeManifest(intent, stateDir, parent, true)

	if err := s.saveCascadeDeleteUnlocked(intent); err != nil {
		return nil, err
	}
	return intent, nil
}

func appendFeatureCascadeManifest(intent *CascadeDeleteIntent, stateDir string, f *Feature, parent bool) {
	if setup := f.Run().Setup; setup != nil {
		keys := make([]string, 0, len(setup.Tasks))
		for key := range setup.Tasks {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			task := setup.Tasks[key]
			if (task.Kind == SetupTaskImage || task.Kind == SetupTaskAttachment) && task.Path != "" {
				intent.Resources = append(intent.Resources, CascadeResource{
					ID: "copy:" + f.ID + ":" + key, Kind: CascadeResourceCopiedInput,
					OwnerID: f.ID, Path: CanonicalPath(task.Path),
				})
			}
		}
	}
	for _, repo := range f.Repos {
		if repo.WorktreePath != "" {
			intent.Resources = append(intent.Resources, CascadeResource{
				ID: "worktree:" + f.ID + ":" + repo.Name, Kind: CascadeResourceWorktree,
				OwnerID: f.ID, Repo: repo.Name, Path: CanonicalPath(repo.WorktreePath),
				RepoPath: repo.Path, Branch: repo.Branch,
			})
		}
		if repo.Branch != "" {
			intent.Resources = append(intent.Resources, CascadeResource{
				ID: "branch:" + f.ID + ":" + repo.Name, Kind: CascadeResourceBranch,
				OwnerID: f.ID, Repo: repo.Name, RepoPath: repo.Path, Branch: repo.Branch,
			})
		}
		if parent {
			intent.Resources = append(intent.Resources, CascadeResource{
				ID: "overlay:" + f.ID + ":" + repo.Name, Kind: CascadeResourceOverlay,
				OwnerID: f.ID, Repo: repo.Name, Path: ParentOverlayPath(stateDir, f.ID, repo.Name),
			})
		} else {
			// Child KB workspaces are disposable resources that cascade
			// cleanup must remove.
			intent.Resources = append(intent.Resources, CascadeResource{
				ID: "kb-workspace:" + f.ID + ":" + repo.Name, Kind: CascadeResourceKBWorkspace,
				OwnerID: f.ID, Repo: repo.Name, Path: ChildKBWorkspaceDir(stateDir, f.ID, repo.Name),
			})
		}
	}
	// Children may have a promotion journal file.
	if !parent {
		intent.Resources = append(intent.Resources, CascadeResource{
			ID: "promotion:" + f.ID, Kind: CascadeResourcePromotion,
			OwnerID: f.ID, Path: filepath.Join(stateDir, f.ID, "promotion.yaml"),
		})
	}
	intent.Resources = append(intent.Resources, CascadeResource{
		ID: "record:" + f.ID, Kind: CascadeResourceRecord, OwnerID: f.ID,
		Path: filepath.Join(stateDir, f.ID),
	})
}

func appendCascadeRefs(intent *CascadeDeleteIntent, parent, child *Feature) {
	if child.Parent == nil || child.Parent.Transaction == nil {
		return
	}
	for _, entry := range child.Parent.Transaction.Entries {
		if entry.CandidateSHA == "" {
			continue
		}
		repoPath := ""
		for _, repo := range parent.Repos {
			if repo.Name == entry.Repo {
				repoPath = repo.Path
				break
			}
		}
		anchor := entry.ExpectedRefSHA
		if anchor == "" {
			anchor = entry.ParentAnchorSHA
		}
		intent.Refs = append(intent.Refs, CascadeRef{
			ChildID: child.ID, Repo: entry.Repo, RepoPath: repoPath,
			Ref: "refs/heads/" + entry.ParentBranch, AnchorSHA: anchor,
			CandidateSHA: entry.CandidateSHA,
		})
	}
}

// LoadCascadeDelete returns the current durable cascade journal.
func (s *Store) LoadCascadeDelete(parentID string) (*CascadeDeleteIntent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadCascadeDeleteUnlocked(parentID)
}

func (s *Store) loadCascadeDeleteUnlocked(parentID string) (*CascadeDeleteIntent, error) {
	data, err := os.ReadFile(s.cascadeDeletePath(parentID))
	if err != nil {
		return nil, err
	}
	var intent CascadeDeleteIntent
	if err := yaml.Unmarshal(data, &intent); err != nil {
		return nil, fmt.Errorf("unmarshaling cascade delete: %w", err)
	}
	return &intent, nil
}

// SaveCascadeDelete atomically persists cascade progress.
func (s *Store) SaveCascadeDelete(parentID string, intent *CascadeDeleteIntent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if intent == nil || intent.ParentID != parentID {
		return fmt.Errorf("cascade parent mismatch")
	}
	return s.saveCascadeDeleteUnlocked(intent)
}

func (s *Store) saveCascadeDeleteUnlocked(intent *CascadeDeleteIntent) error {
	path := s.cascadeDeletePath(intent.ParentID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating cascade directory: %w", err)
	}
	data, err := yaml.Marshal(intent)
	if err != nil {
		return fmt.Errorf("marshaling cascade delete: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "cascade-*.yaml.tmp")
	if err != nil {
		return fmt.Errorf("creating cascade temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing cascade delete: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing cascade delete: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("committing cascade delete: %w", err)
	}
	return nil
}
