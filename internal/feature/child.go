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
	"reflect"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
)

// ChildKindRefactor identifies a child launched as a refactor of its parent.
const ChildKindRefactor = "refactor"

// Typed refactor-launch failures. The server maps each to a stable machine
// code; callers should match with errors.Is / errors.As.
var (
	// ErrRefactorParentNotFound: the requested parent feature does not exist.
	ErrRefactorParentNotFound = errors.New("refactor parent feature not found")
	// ErrRefactorParentIsChild: children are one level deep; a child cannot
	// itself be a refactor parent.
	ErrRefactorParentIsChild = errors.New("refactor parent is itself a child feature")
	// ErrRefactorParentStatusIneligible: only Published or CodeReady parents
	// can launch a refactor child.
	ErrRefactorParentStatusIneligible = errors.New("refactor parent status is not eligible")
	// ErrChildExecutionBlocked: setup-complete children are deliberately
	// non-runnable; every pipeline start/resume/restart path rejects them.
	ErrChildExecutionBlocked = errors.New("child features are not runnable")
)

// ActiveChildExistsError reports that the parent already has an active child
// (CloseOutcome empty). Exactly one concurrent launch under the same parent
// wins; every loser receives this conflict.
type ActiveChildExistsError struct {
	ParentID string
	ChildID  string
}

func (e *ActiveChildExistsError) Error() string {
	return fmt.Sprintf("parent %s already has an active child %s", e.ParentID, e.ChildID)
}

// RepoDirtyDiagnostics carries the bounded, categorized dirty-worktree
// findings for one parent repository.
type RepoDirtyDiagnostics struct {
	Repo           string   `yaml:"repo" json:"repo"`
	Path           string   `yaml:"path" json:"path"`
	Staged         []string `yaml:"staged,omitempty" json:"staged,omitempty"`
	Unstaged       []string `yaml:"unstaged,omitempty" json:"unstaged,omitempty"`
	Untracked      []string `yaml:"untracked,omitempty" json:"untracked,omitempty"`
	StagedTotal    int      `yaml:"staged_total" json:"staged_total"`
	UnstagedTotal  int      `yaml:"unstaged_total" json:"unstaged_total"`
	UntrackedTotal int      `yaml:"untracked_total" json:"untracked_total"`
}

// ParentWorktreesDirtyError rejects a refactor launch as a whole when any
// parent repository has staged, unstaged, or untracked changes. Ignored
// paths never appear.
type ParentWorktreesDirtyError struct {
	Repos []RepoDirtyDiagnostics
}

func (e *ParentWorktreesDirtyError) Error() string {
	repos := make([]string, 0, len(e.Repos))
	for _, r := range e.Repos {
		repos = append(repos, r.Repo)
	}
	return fmt.Sprintf("parent worktrees are dirty: %v", repos)
}

// DefaultDirtyPathLimit bounds each dirty-path category captured at launch.
const DefaultDirtyPathLimit = 50

// RepoCleanliness is the feature-package view of one repository's worktree
// cleanliness. Wired from the git adapter via CleanlinessOps.
type RepoCleanliness struct {
	Staged         []string
	Unstaged       []string
	Untracked      []string
	StagedTotal    int
	UnstagedTotal  int
	UntrackedTotal int
}

// Dirty reports whether any category carries at least one path.
func (r *RepoCleanliness) Dirty() bool {
	if r == nil {
		return false
	}
	return r.StagedTotal > 0 || r.UnstagedTotal > 0 || r.UntrackedTotal > 0
}

// CleanlinessOps inspects a worktree for staged/unstaged/untracked changes.
// Satisfied by the git adapter (wired in the orchestrator module).
type CleanlinessOps interface {
	InspectCleanliness(worktreePath string, maxPerCategory int) (*RepoCleanliness, error)
}

// CleanlinessFunc adapts a plain function to CleanlinessOps.
type CleanlinessFunc func(worktreePath string, maxPerCategory int) (*RepoCleanliness, error)

// InspectCleanliness implements CleanlinessOps.
func (fn CleanlinessFunc) InspectCleanliness(worktreePath string, maxPerCategory int) (*RepoCleanliness, error) {
	return fn(worktreePath, maxPerCategory)
}

// ChildRepoBase is the exact per-repository provenance captured at launch:
// the full parent HEAD the child worktree must be created at, plus the
// parent branch that tip belonged to.
type ChildRepoBase struct {
	Repo         string `yaml:"repo"`
	SHA          string `yaml:"sha"`
	ParentBranch string `yaml:"parent_branch,omitempty" `
}

// ChildRelationship is the child-owned link back to its launch parent. The
// child alone persists the parent identifier; parent-to-child lookups and
// active-child classification are derived by scanning stored feature
// records — no duplicated parent pointer or child list exists.
type ChildRelationship struct {
	ParentID string `yaml:"parent_id"`
	Kind     string `yaml:"kind"`
	// CloseOutcome records how the relationship ended; empty means the child
	// is still active, and the parent cannot launch another child.
	CloseOutcome string `yaml:"close_outcome,omitempty"`
	// ClosedAt is the relationship close timestamp, set with CloseOutcome.
	ClosedAt *time.Time `yaml:"closed_at,omitempty"`
	// Integration is the durable single-repository integration record. It is
	// populated when a successful final review hands the child into local
	// integration instead of delivery, and updated across restart retries.
	Integration *ChildIntegration `yaml:"integration,omitempty"`
	// Bases captures the exact parent tip per repository at launch time.
	Bases []ChildRepoBase `yaml:"bases,omitempty"`
}

// IsChild reports whether the feature was launched as a child of another.
func (f *Feature) IsChild() bool {
	return f != nil && f.Parent != nil && f.Parent.ParentID != ""
}

// IsActiveChild reports whether the feature is a child whose relationship
// has not been closed.
func (f *Feature) IsActiveChild() bool {
	return f.IsChild() && f.Parent.CloseOutcome == ""
}

// IntegrationResumable reports whether a Restart should replay some part of
// the child's durable integration record instead of rerunning pipeline
// phases. It is true for an active child whose integration reached any
// recorded phase (anchors captured, attention, or merged-but-unclosed), and
// for a completed child whose impermanent closure tail is unfinished:
// cleanup never settled, so the disposable worktree path or a cleanup
// warning is still durable on the record.
func (f *Feature) IntegrationResumable() bool {
	if !f.IsChild() || f.Parent.Integration == nil || f.Parent.Integration.Phase == "" {
		return false
	}
	if f.IsActiveChild() {
		return true
	}
	if f.Parent.CloseOutcome != ChildCloseOutcomeCompleted || f.Parent.Integration.MergeHEAD == "" {
		return false
	}
	return f.Parent.Integration.CleanupWarning != "" || f.ChildWorktreePending()
}

// ChildWorktreePending reports whether the disposable child worktree path is
// still recorded, meaning cleanup has not durably completed.
func (f *Feature) ChildWorktreePending() bool {
	if f == nil || len(f.Repos) == 0 {
		return false
	}
	return f.Repos[0].WorktreePath != ""
}

// BaseSHA returns the captured launch tip for the named repository, or "".
func (f *Feature) BaseSHA(repoName string) string {
	if f == nil || f.Parent == nil {
		return ""
	}
	for _, b := range f.Parent.Bases {
		if b.Repo == repoName {
			return b.SHA
		}
	}
	return ""
}

// ChildCreationIntent is the durable record of an in-flight child creation.
// It contains everything needed to finish creation after a crash: the full
// child feature specification (with the selected review configuration) and
// the queued setup intent. Persisted on the parent's Feature.PendingChild
// before the child is materialized; startup reconciliation rolls an
// interrupted creation forward exactly once from this record.
type ChildCreationIntent struct {
	ChildID   string    `yaml:"child_id"`
	Kind      string    `yaml:"kind"`
	CreatedAt time.Time `yaml:"created_at"`
	// Child is the complete child feature specification. Transient run
	// shadows are empty here; the queued setup intent is carried separately
	// in Setup because Run state persists in run.yaml, not feature.yaml.
	Child Feature `yaml:"child"`
	// Setup is the queued durable setup intent (worktrees at exact captured
	// tips plus copied child inputs) for the child's first run.
	Setup *SetupState `yaml:"setup,omitempty"`
}

// RefactorChildSpec is the complete refactor launch brief submitted through
// the wizard: What, Pipeline, and Review configuration plus optional new
// copied inputs. Repository and base selection are deliberately absent —
// children inherit every parent repository.
type RefactorChildSpec struct {
	Name        string
	Description string
	// Images and Attachments are the newly supplied child inputs; parent
	// inputs are never implicitly inherited.
	Images      []string
	Attachments []string
	// Pipeline is the child's independent pipeline profile. Empty inherits
	// the parent's profile.
	Pipeline PipelineProfile
	// Checkpoints is the submitted Review configuration, committed
	// consistently to both parent and child.
	Checkpoints Checkpoints
	// Effort and Models: zero values inherit the parent's settings.
	Effort config.EffortConfig
	Models config.ModelConfig
	// RiskLevel and ExitCriteria: empty inherits the parent's settings.
	RiskLevel    RiskLevel
	ExitCriteria string
	// Inquireness is the submitted inquiry behavior; empty inherits the
	// parent's setting.
	Inquireness Inquireness
}

// CreateRefactorChild atomically launches a refactor child under the given
// parent. The parent must be a top-level Published or CodeReady feature with
// no active child and clean worktrees in every repository; one dirty
// repository prevents all child state, branch, and worktree creation. On
// success the relationship and queued setup intent are durable and the
// returned child is ready for asynchronous setup.
func (m *Manager) CreateRefactorChild(parentID string, spec RefactorChildSpec) (*Feature, error) {
	parent, err := m.Store.Load(parentID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrRefactorParentNotFound, parentID)
		}
		return nil, fmt.Errorf("loading parent feature: %w", err)
	}
	if err := validateRefactorParent(parent, nil); err != nil {
		return nil, err
	}

	// Preflight every parent repository BEFORE any child or intent is
	// written: reject the launch as a whole on any dirty worktree and
	// capture every full parent HEAD.
	bases, err := m.preflightRefactorParent(parent)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	child, err := m.Store.CreateChildLocked(parentID, func(lockedParent *Feature, activeChild *Feature) (*Feature, *ChildCreationIntent, error) {
		if err := validateRefactorParent(lockedParent, activeChild); err != nil {
			return nil, nil, err
		}
		child := m.buildRefactorChild(lockedParent, spec, bases, now)
		// The submitted Review configuration (risk, models, role effort,
		// inquiry behavior, review gates, exit criteria) is resolved once —
		// in buildRefactorChild, with unspecified fields inheriting the
		// parent's current values — and committed consistently to BOTH
		// records under the creation lock. Copying the resolved values back
		// onto the parent keeps a launch selecting non-default Review
		// settings from leaving the two records inconsistent, while
		// inheriting fields rewrite an identical value. Only the pipeline
		// identity stays child-specific; the parent's pipeline is untouched.
		lockedParent.Checkpoints = child.Checkpoints
		lockedParent.Models = child.Models
		lockedParent.Effort = child.Effort
		lockedParent.RiskLevel = child.RiskLevel
		lockedParent.ExitCriteria = child.ExitCriteria
		lockedParent.Inquireness = child.Inquireness
		intent := &ChildCreationIntent{
			ChildID:   child.ID,
			Kind:      ChildKindRefactor,
			CreatedAt: now,
			Child:     *child,
			Setup:     child.Run().Setup,
		}
		return child, intent, nil
	})
	if err != nil {
		return nil, err
	}
	return child, nil
}

// validateRefactorParent enforces the launch rules against the parent. When
// activeChild is non-nil the caller already knows an active child exists.
func validateRefactorParent(parent *Feature, activeChild *Feature) error {
	if parent.IsChild() {
		return fmt.Errorf("%w: %s", ErrRefactorParentIsChild, parent.ID)
	}
	if parent.Status != StatusPublished && parent.Status != StatusCodeReady {
		return fmt.Errorf("%w: %s is %s", ErrRefactorParentStatusIneligible, parent.ID, parent.Status)
	}
	if activeChild != nil {
		return &ActiveChildExistsError{ParentID: parent.ID, ChildID: activeChild.ID}
	}
	return nil
}

// preflightRefactorParent inspects every parent worktree for dirty state and
// captures each repository's full HEAD. Any dirty repository rejects the
// whole launch with categorized, bounded diagnostics.
func (m *Manager) preflightRefactorParent(parent *Feature) ([]ChildRepoBase, error) {
	// Dirty-worktree inspection is a mandatory safety check for child
	// launches: a missing adapter must fail the launch explicitly, like
	// exact-HEAD capture, not silently skip the check.
	if m.Cleanliness == nil {
		return nil, fmt.Errorf("cleanliness inspection is not configured")
	}
	var bases []ChildRepoBase
	var dirty []RepoDirtyDiagnostics
	for _, repo := range parent.Repos {
		path := repo.WorktreePath
		if path == "" {
			path = repo.Path
		}
		report, err := m.Cleanliness.InspectCleanliness(path, DefaultDirtyPathLimit)
		if err != nil {
			return nil, fmt.Errorf("inspecting parent worktree %s: %w", repo.Name, err)
		}
		if report.Dirty() {
			dirty = append(dirty, RepoDirtyDiagnostics{
				Repo:           repo.Name,
				Path:           path,
				Staged:         report.Staged,
				Unstaged:       report.Unstaged,
				Untracked:      report.Untracked,
				StagedTotal:    report.StagedTotal,
				UnstagedTotal:  report.UnstagedTotal,
				UntrackedTotal: report.UntrackedTotal,
			})
			continue
		}
		sha, err := m.resolveHeadSHA(path)
		if err != nil {
			return nil, fmt.Errorf("capturing HEAD of parent repo %s: %w", repo.Name, err)
		}
		bases = append(bases, ChildRepoBase{Repo: repo.Name, SHA: sha, ParentBranch: repo.Branch})
	}
	if len(dirty) > 0 {
		return nil, &ParentWorktreesDirtyError{Repos: dirty}
	}
	return bases, nil
}

// HeadSHAReader is the exact-HEAD lookup capability the launch and setup
// retry paths require: launch captures every parent's exact tip and retry
// validates worktree reuse against the persisted exact base. It is satisfied
// by *git.WorktreeManager; wiring that omits it fails the launch or retry
// loudly instead of silently degrading exact-base guarantees.
type HeadSHAReader interface {
	CurrentHeadSHA(worktreePath string) (string, error)
}

// headSHAReader returns the configured exact-HEAD lookup or an error naming
// the missing mandatory capability.
func (m *Manager) headSHAReader() (HeadSHAReader, error) {
	resolver, ok := m.Worktrees.(HeadSHAReader)
	if !ok {
		return nil, fmt.Errorf("exact head capture is not configured")
	}
	return resolver, nil
}

// resolveHeadSHA captures the full HEAD of a worktree through the HeadSHAReader
// capability.
func (m *Manager) resolveHeadSHA(path string) (string, error) {
	resolver, err := m.headSHAReader()
	if err != nil {
		return "", err
	}
	return resolver.CurrentHeadSHA(path)
}

// buildRefactorChild materializes the child aggregate: it inherits every
// parent repository in order (canonical path, publishability, base branch)
// with unique child branch identities and exact-tip provenance, and queues
// the durable setup intent (exact-SHA worktrees plus copied child inputs).
// Callers must hold the store lock (see Store.CreateChildLocked).
func (m *Manager) buildRefactorChild(parent *Feature, spec RefactorChildSpec, bases []ChildRepoBase, now time.Time) *Feature {
	id := generateID()
	slug := Slugify(spec.Name)
	workspaceSlug := WorkspaceSlug(slug, id)
	branch := m.branchName(workspaceSlug)

	childRepos := make([]FeatureRepo, 0, len(parent.Repos))
	for _, pr := range parent.Repos {
		childRepos = append(childRepos, FeatureRepo{
			Name:        pr.Name,
			Path:        pr.Path,
			Branch:      branch,
			BaseBranch:  pr.BaseBranch,
			Publishable: pr.Publishable,
		})
	}

	pipeline := spec.Pipeline
	if !pipeline.IsValid() {
		pipeline = parent.EffectivePipeline()
	}
	effort := spec.Effort
	if reflect.DeepEqual(effort, config.EffortConfig{}) {
		effort = parent.Effort
	}
	models := spec.Models
	if reflect.DeepEqual(models, config.ModelConfig{}) {
		models = parent.Models
	}
	risk := spec.RiskLevel
	if risk == "" {
		risk = parent.RiskLevel
	}
	exitCriteria := spec.ExitCriteria
	if exitCriteria == "" {
		exitCriteria = parent.ExitCriteria
	}
	inquireness := spec.Inquireness
	if inquireness == "" {
		inquireness = parent.Inquireness
	}

	exactStart := make(map[string]string, len(bases))
	for _, b := range bases {
		exactStart[b.Repo] = b.SHA
	}

	child := &Feature{
		ID:           id,
		Name:         spec.Name,
		Slug:         slug,
		Description:  spec.Description,
		Created:      now,
		Status:       StatusSettingUpWorktrees,
		CurrentPhase: pipeline.FirstPhase(),
		Pipeline:     pipeline,
		Repos:        childRepos,
		Models:       models,
		Effort:       effort,
		ExitCriteria: exitCriteria,
		Inquireness:  inquireness,
		Checkpoints:  spec.Checkpoints,
		RiskLevel:    risk,
		Parent: &ChildRelationship{
			ParentID: parent.ID,
			Kind:     ChildKindRefactor,
			Bases:    bases,
		},
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: SchemaVersionCurrent,
	}
	run := &Run{RunNumber: 1}
	if len(childRepos) > 0 {
		run.RepoStates = make(map[string]*RepoState, len(childRepos))
		for _, fr := range childRepos {
			run.RepoStates[fr.Name] = &RepoState{}
		}
	}
	run.Setup = NewActiveSetupState(childRepos, spec.Images, spec.Attachments, now, SetupInitOptions{
		ExactStartPointPerRepo: exactStart,
	})
	child.SetRun(run)
	return child
}
