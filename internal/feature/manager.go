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
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
)

// ErrDuplicateSlug is returned when a feature with the same slug already exists.
var ErrDuplicateSlug = fmt.Errorf("feature with this slug already exists")

// WorktreeOps is the minimal worktree-management surface the feature
// manager depends on. Defined locally so the feature package stays free of
// any adapter-specific git package. Satisfied by *git.WorktreeManager.
type WorktreeOps interface {
	Create(repoPath, featureSlug, repoName, startPoint string) (string, error)
	Remove(worktreePath string, deleteBranch bool) error
	ResetToBase(worktreePath, baseBranch string) error
	ResetToBaseLocal(worktreePath, baseBranch string) error
	ResetToCommit(worktreePath, commitSHA string) error
}

// BranchOps captures the branch-level git operations the feature manager
// uses during feature creation, rewind, and restart. Satisfied by the
// git.BranchAdapter (structural match on method set).
type BranchOps interface {
	DefaultBranch(repoPath string) (string, error)
	HasOriginRemote(repoPath string) (bool, error)
	BranchName(featureSlug string) string
	BranchExistsOnRemote(repoPath, branch string) (bool, error)
	CurrentBranch(repoPath string) (string, error)
	CreateBackupBranch(worktreePath, slug string) (string, error)
}

// PRCloser abstracts the single git/gh operation the feature manager
// performs against an open pull request (close on rewind). Satisfied by
// *git.PublishAdapter.
type PRCloser interface {
	ClosePR(prURL string) error
}

type Manager struct {
	Store     *Store
	Config    *config.Config
	Worktrees WorktreeOps // optional; nil skips worktree creation
	Branches  BranchOps   // optional; nil skips branch lookups during Create/Rewind
	PRs       PRCloser    // optional; nil skips PR close on rewind

	setupMu    sync.Mutex
	setupLocks map[string]struct{}
}

// SlugExists checks if any existing feature has the given slug.
// Returns the existing feature's name if found, empty string otherwise.
func (m *Manager) SlugExists(slug string) (string, error) {
	features, err := m.Store.List()
	if err != nil && !IsPartialLoadError(err) {
		return "", fmt.Errorf("listing features: %w", err)
	}
	for _, f := range features {
		if f.Slug == slug {
			return f.Name, nil
		}
	}
	return "", nil
}

func NewManager(store *Store, cfg *config.Config) *Manager {
	return &Manager{Store: store, Config: cfg}
}

// resolveRepoBase resolves a repo's default branch and remote availability
// via the injected BranchOps. When Branches is nil (test/minimal
// construction) it falls back to empty base branch and no remote.
func (m *Manager) resolveRepoBase(repoPath string) (baseBranch string, hasRemote bool, err error) {
	if m.Branches == nil {
		return "", false, nil
	}
	baseBranch, _ = m.Branches.DefaultBranch(repoPath)
	hasRemote, _ = m.Branches.HasOriginRemote(repoPath)
	return baseBranch, hasRemote, nil
}

// defaultBranchName returns the local branch name derived from a feature
// slug when no BranchOps is wired in. Kept in sync with git.BranchName.
func defaultBranchName(slug string) string {
	return "feature/" + slug
}

func (m *Manager) branchName(slug string) string {
	if m.Branches != nil {
		return m.Branches.BranchName(slug)
	}
	return defaultBranchName(slug)
}

func branchSlug(branch string) string {
	return strings.TrimPrefix(branch, "feature/")
}

func repoWorkspaceSlug(f *Feature, repo FeatureRepo) string {
	if slug := branchSlug(repo.Branch); slug != "" && slug != repo.Branch {
		return slug
	}
	if f == nil {
		return ""
	}
	return f.WorkspaceSlug()
}

func setupWorkspaceSlug(f *Feature, repo FeatureRepo, task SetupTask) (string, string) {
	if f == nil {
		return "", task.Branch
	}
	qualified := f.WorkspaceSlug()
	qualifiedBranch := defaultBranchName(qualified)
	legacyBranch := defaultBranchName(f.Slug)
	if task.Branch == "" || task.Branch == legacyBranch {
		return qualified, qualifiedBranch
	}
	if slug := branchSlug(task.Branch); slug != "" && slug != task.Branch {
		return slug, task.Branch
	}
	if slug := branchSlug(repo.Branch); slug != "" && slug != repo.Branch {
		return slug, repo.Branch
	}
	return qualified, qualifiedBranch
}

// CreateOptions holds optional parameters for feature creation.
type CreateOptions struct {
	// UseCurrentBranch, when true, creates worktrees from the repo's current
	// HEAD instead of the detected default branch. The BaseBranch field is
	// still set to the default branch for diff/PR purposes.
	//
	// Acts as a global fallback when UseCurrentBranchPerRepo is nil or does
	// not contain an entry for a given repo. UseCurrentBranchPerRepo wins
	// when both are set.
	UseCurrentBranch bool
	// UseCurrentBranchPerRepo overrides UseCurrentBranch on a per-repo basis.
	// Keys are repo names; true means "start from current HEAD"; false means
	// "start from default branch". Repos not in the map fall back to
	// UseCurrentBranch.
	UseCurrentBranchPerRepo map[string]bool
	Checkpoints             Checkpoints
	Attachments             []string // temp attachment file paths
	RiskLevel               RiskLevel
	Pipeline                PipelineProfile
	QueueSetup              bool
}

// Re-entrancy / crash recovery:
//
//	(a) Create is NOT idempotent on retry. A successful run generates a fresh
//	    feature ID via generateID(), so a second call with the same arguments
//	    creates a second feature directory rather than reattaching to the
//	    first. The duplicate-slug guard short-circuits before any state is
//	    written, so re-issuing Create with the same name (after a successful
//	    first call) returns ErrDuplicateSlug instead of mutating either
//	    feature.
//	(b) On crash before Save returns: nothing is persisted on disk, recovery
//	    is a no-op (the feature simply does not exist). On crash after the
//	    first Save but before image/attachment copies finish, the feature
//	    exists at SchemaVersionCurrent; partial image or attachment state is
//	    bounded (the loop saves after each batch) and the next startup loads
//	    the feature normally — only the unprocessed images/attachments under
//	    the temp directory are lost, which is acceptable since they are
//	    user-provided uploads pending a retry.
func (m *Manager) Create(name, description string, repos []string, models config.ModelConfig, exitCriteria, inquireness string, images []string, opts ...CreateOptions) (*Feature, error) {
	id := generateID()
	slug := Slugify(name)
	workspaceSlug := WorkspaceSlug(slug, id)

	if existingName, err := m.SlugExists(slug); err != nil {
		return nil, fmt.Errorf("checking for duplicates: %w", err)
	} else if existingName != "" {
		return nil, fmt.Errorf("%w: %q (slug: %s)", ErrDuplicateSlug, existingName, slug)
	}

	if exitCriteria == "" {
		exitCriteria = m.Config.Defaults.ExitCriteria
	}

	var opt CreateOptions
	if len(opts) > 0 {
		opt = opts[0]
	}
	if !opt.Pipeline.IsValid() {
		if m.Config.Defaults.Pipeline != "" {
			parsed, err := ParsePipelineProfile(m.Config.Defaults.Pipeline)
			if err != nil {
				return nil, fmt.Errorf("invalid defaults.pipeline in config: %w", err)
			}
			opt.Pipeline = parsed
		} else {
			opt.Pipeline = PipelineMoonshot
		}
	}

	if len(m.Config.WorkspaceRoots) > 0 {
		config.DiscoverReposFromRoots(m.Config)
	}

	var featureRepos []FeatureRepo
	allRepos := config.AllRepos(m.Config)
	for _, repoName := range repos {
		rc, ok := allRepos[repoName]
		if !ok {
			return nil, fmt.Errorf("repo %q not found in config", repoName)
		}
		if strings.TrimSpace(rc.Path) == "" {
			return nil, fmt.Errorf("repo %q has no path configured; add it under workspace_roots or set repos.%s.path", repoName, repoName)
		}
		baseBranch, hasRemote, err := m.resolveRepoBase(rc.Path)
		if err != nil {
			return nil, err
		}
		publishable := &hasRemote
		featureRepos = append(featureRepos, FeatureRepo{
			Name:        repoName,
			Path:        rc.Path,
			BaseBranch:  baseBranch,
			Publishable: publishable,
		})
	}

	// Ensure the branch name doesn't conflict with an existing upstream branch.
	// If it does, append a random 4-char hex suffix and recheck (up to 5 attempts).
	if (opt.QueueSetup || m.Worktrees != nil) && m.Branches != nil {
		baseSlug := slug
		for attempt := 0; attempt < 5; attempt++ {
			workspaceSlug = WorkspaceSlug(slug, id)
			branch := m.Branches.BranchName(workspaceSlug)
			conflict := false
			for _, fr := range featureRepos {
				if fr.Publishable != nil && !*fr.Publishable {
					continue // no remote to check
				}
				exists, _ := m.Branches.BranchExistsOnRemote(fr.Path, branch)
				if exists {
					conflict = true
					break
				}
			}
			if !conflict {
				break
			}
			slug = baseSlug + "-" + randomSuffix()
		}
		workspaceSlug = WorkspaceSlug(slug, id)
	}

	if opt.QueueSetup || m.Worktrees != nil {
		for i := range featureRepos {
			featureRepos[i].Branch = m.branchName(workspaceSlug)
		}
	}

	inq := Inquireness(inquireness)
	if inq == "" {
		inq = Inquireness(m.Config.Defaults.Inquireness)
	}

	now := time.Now()
	status := StatusCreated
	if opt.QueueSetup {
		status = StatusSettingUpWorktrees
	}
	f := &Feature{
		ID:            id,
		Name:          name,
		Slug:          slug,
		Description:   description,
		Created:       now,
		Status:        status,
		CurrentPhase:  opt.Pipeline.FirstPhase(),
		Pipeline:      opt.Pipeline,
		Repos:         featureRepos,
		Models:        models,
		ExitCriteria:  exitCriteria,
		Inquireness:   inq,
		MaxIterations: m.Config.Defaults.MaxIterations,
		Checkpoints:   opt.Checkpoints,
		RiskLevel:     opt.RiskLevel,
		// Feature starts on run-001. Explicit seeding ensures feature.yaml is
		// never persisted with ActiveRun == 0 (which Store.loadUnlocked treats
		// as the pre-runs migration trip wire).
		ActiveRun: 1,
		RunCount:  1,
		// See SchemaVersionCurrent in feature.go for the version history.
		SchemaVersion: SchemaVersionCurrent,
	}
	// Pre-populate the active run with one (empty) RepoState entry per repo
	// so downstream readers can iterate the map deterministically. The
	// per-phase ExecutionPlan is read fresh from disk by the orchestrator
	// (see internal/agent/execution_order.go) and is no longer persisted.
	run := &Run{RunNumber: 1}
	if len(featureRepos) > 0 {
		run.RepoStates = make(map[string]*RepoState, len(featureRepos))
		for _, fr := range featureRepos {
			run.RepoStates[fr.Name] = &RepoState{}
		}
	}
	if opt.QueueSetup {
		run.Setup = NewActiveSetupState(featureRepos, images, opt.Attachments, now, SetupInitOptions{
			UseCurrentBranch:        opt.UseCurrentBranch,
			UseCurrentBranchPerRepo: opt.UseCurrentBranchPerRepo,
		})
	}
	f.SetRun(run)

	if opt.QueueSetup {
		if err := m.Store.Save(f); err != nil {
			return nil, fmt.Errorf("saving feature: %w", err)
		}
		return f, nil
	}

	saved := false

	// Create worktrees for each repo if worktree manager is configured.
	if m.Worktrees != nil {
		for i, fr := range featureRepos {
			startPoint := fr.BaseBranch
			useCurrent := opt.UseCurrentBranch
			if v, ok := opt.UseCurrentBranchPerRepo[fr.Name]; ok {
				useCurrent = v
			}
			if useCurrent {
				startPoint = "" // empty → HEAD in worktree.Create
			}
			wtPath, err := m.Worktrees.Create(fr.Path, workspaceSlug, fr.Name, startPoint)
			if err != nil {
				return nil, fmt.Errorf("creating worktree for %s: %w", fr.Name, err)
			}
			featureRepos[i].WorktreePath = wtPath
			f.Repos[i].WorktreePath = wtPath
		}
		if err := m.Store.Save(f); err != nil {
			return nil, fmt.Errorf("saving feature with worktrees: %w", err)
		}
		saved = true
	}

	// Copy images from temp paths to feature directory
	if len(images) > 0 {
		imagesDir := filepath.Join(m.Store.BaseDir, f.ID, "images")
		if err := os.MkdirAll(imagesDir, 0o755); err != nil {
			return nil, fmt.Errorf("creating images directory: %w", err)
		}
		for i, src := range images {
			dst := filepath.Join(imagesDir, fmt.Sprintf("image-%d.png", i+1))
			if err := copyFile(src, dst); err != nil {
				return nil, fmt.Errorf("copying image %d: %w", i+1, err)
			}
			f.Images = append(f.Images, dst)
		}
		if err := m.Store.Save(f); err != nil {
			return nil, fmt.Errorf("saving feature with images: %w", err)
		}
		saved = true
	}

	// Copy attachments from temp paths to feature directory
	if len(opt.Attachments) > 0 {
		attachDir := filepath.Join(m.Store.BaseDir, f.ID, "attachments")
		if err := os.MkdirAll(attachDir, 0o755); err != nil {
			return nil, fmt.Errorf("creating attachments directory: %w", err)
		}
		for _, src := range opt.Attachments {
			name := filepath.Base(src)
			dst := filepath.Join(attachDir, name)
			if err := copyFile(src, dst); err != nil {
				return nil, fmt.Errorf("copying attachment %s: %w", name, err)
			}
			f.Attachments = append(f.Attachments, dst)
		}
		if err := m.Store.Save(f); err != nil {
			return nil, fmt.Errorf("saving feature with attachments: %w", err)
		}
		saved = true
	}

	if !saved {
		if err := m.Store.Save(f); err != nil {
			return nil, fmt.Errorf("saving feature: %w", err)
		}
	}

	return f, nil
}

func (m *Manager) Get(id string) (*Feature, error) {
	return m.Store.Load(id)
}

func (m *Manager) List() ([]*Feature, error) {
	return m.Store.List()
}

func (m *Manager) Transition(id string, to Status) error {
	return m.Store.Modify(id, func(f *Feature) error {
		return f.Transition(to)
	})
}

// StartInquire transitions a feature to Inquiring status and sets CurrentPhase.
func (m *Manager) StartInquire(featureID string) error {
	return m.Store.Modify(featureID, func(f *Feature) error {
		if err := f.Transition(StatusInquiring); err != nil {
			return err
		}
		f.CurrentPhase = PhaseInquire
		if !isCycleTimingKey(f.ActiveTimingKey) {
			f.ActiveTimingKey = "inquire"
		}
		now := time.Now()
		f.ActivePhaseStart = &now
		if f.StartedAt == nil {
			f.StartedAt = &now
		}
		return nil
	})
}

// CompleteInquire transitions a feature to InquireReady.
func (m *Manager) CompleteInquire(featureID string) error {
	return m.Transition(featureID, StatusInquireReady)
}

// StartDesign transitions a feature to Designing status and sets CurrentPhase.
func (m *Manager) StartDesign(featureID string) error {
	return m.Store.Modify(featureID, func(f *Feature) error {
		if err := f.Transition(StatusDesigning); err != nil {
			return err
		}
		f.CurrentPhase = PhaseDesign
		if !isCycleTimingKey(f.ActiveTimingKey) {
			f.ActiveTimingKey = "design"
		}
		now := time.Now()
		f.ActivePhaseStart = &now
		if f.StartedAt == nil {
			f.StartedAt = &now
		}
		return nil
	})
}

// CompleteDesign transitions a feature to PlanReady.
func (m *Manager) CompleteDesign(featureID string) error {
	return m.Transition(featureID, StatusPlanReady)
}

// StartResearch transitions a feature to Researching status and sets CurrentPhase.
func (m *Manager) StartResearch(featureID string) error {
	return m.Store.Modify(featureID, func(f *Feature) error {
		if err := f.Transition(StatusResearching); err != nil {
			return err
		}
		f.CurrentPhase = PhaseResearch
		if !isCycleTimingKey(f.ActiveTimingKey) {
			f.ActiveTimingKey = "research"
		}
		now := time.Now()
		f.ActivePhaseStart = &now
		if f.StartedAt == nil {
			f.StartedAt = &now
		}
		return nil
	})
}

// CompleteResearch transitions a feature to DesignReady.
func (m *Manager) CompleteResearch(featureID string) error {
	return m.Transition(featureID, StatusDesignReady)
}

// StartKnowledgeBase transitions a feature to BuildingKB status and sets CurrentPhase.
func (m *Manager) StartKnowledgeBase(featureID string) error {
	return m.Store.Modify(featureID, func(f *Feature) error {
		if err := f.Transition(StatusBuildingKB); err != nil {
			return err
		}
		f.CurrentPhase = PhaseKnowledgeBase
		if !isCycleTimingKey(f.ActiveTimingKey) {
			f.ActiveTimingKey = "knowledgebase"
		}
		now := time.Now()
		f.ActivePhaseStart = &now
		if f.StartedAt == nil {
			f.StartedAt = &now
		}
		return nil
	})
}

// CompleteKnowledgeBase transitions a feature from BuildingKB back to Created,
// allowing StartResearch to proceed with the Created → Researching transition.
func (m *Manager) CompleteKnowledgeBase(featureID string) error {
	return m.Transition(featureID, StatusCreated)
}

// InitKBStatus initializes the KBStatus map for all repos in the feature.
func (m *Manager) InitKBStatus(featureID string) error {
	return m.Store.Modify(featureID, func(f *Feature) error {
		f.KBStatus = make(map[string]string)
		for _, repo := range f.Repos {
			f.KBStatus[repo.Name] = "pending"
		}
		return nil
	})
}

// MarkRepoKBCompleted marks a single repo's KB as completed.
func (m *Manager) MarkRepoKBCompleted(featureID, repoName string) error {
	return m.Store.Modify(featureID, func(f *Feature) error {
		if f.KBStatus == nil {
			f.KBStatus = make(map[string]string)
		}
		f.KBStatus[repoName] = "completed"
		return nil
	})
}

// MarkRepoKBFailed marks a single repo's KB as failed with an error message.
func (m *Manager) MarkRepoKBFailed(featureID, repoName, errMsg string) error {
	return m.Store.Modify(featureID, func(f *Feature) error {
		if f.KBStatus == nil {
			f.KBStatus = make(map[string]string)
		}
		f.KBStatus[repoName] = "failed: " + errMsg
		return nil
	})
}

// AllKBsCompleted returns true if all repos in the feature have KBStatus == "completed".
// Returns true if KBStatus is nil or empty (backward compat: no tracking = done).
func (m *Manager) AllKBsCompleted(featureID string) (bool, error) {
	f, err := m.Store.Load(featureID)
	if err != nil {
		return false, err
	}
	if len(f.KBStatus) == 0 {
		return true, nil
	}
	for _, status := range f.KBStatus {
		if status != "completed" {
			return false, nil
		}
	}
	return true, nil
}

// StartPlanning transitions a feature to Planning status and sets CurrentPhase.
func (m *Manager) StartPlanning(featureID string) error {
	return m.Store.Modify(featureID, func(f *Feature) error {
		if err := f.Transition(StatusPlanning); err != nil {
			return err
		}
		f.CurrentPhase = PhasePlan
		f.ValidatingPlan = false
		f.ValidatorStatuses = nil
		// Accumulate any in-flight time under the old key before switching.
		// Without this, an interrupt → restart cycle for a roadmap-phase plan
		// would lose the in-flight bucket because Transition only auto-
		// accumulates when leaving a running state, and the FROM state on
		// restart (Interrupted/Failed/PlanReady) is non-running.
		f.accumulateActiveTime()
		if !isCycleTimingKey(f.ActiveTimingKey) {
			if f.CurrentRoadmapPhase > 0 {
				f.ActiveTimingKey = fmt.Sprintf("phase-%d-plan", f.CurrentRoadmapPhase)
			} else {
				f.ActiveTimingKey = "plan"
			}
		}
		now := time.Now()
		f.ActivePhaseStart = &now
		if f.StartedAt == nil {
			f.StartedAt = &now
		}
		return nil
	})
}

// CompletePlanning transitions a feature to ImplementReady.
func (m *Manager) CompletePlanning(featureID string) error {
	return m.Transition(featureID, StatusImplementReady)
}

// NeedsPlanReview transitions a feature to StatusPlanNeedsReview.
func (m *Manager) NeedsPlanReview(featureID string) error {
	return m.Store.Modify(featureID, func(f *Feature) error {
		if err := f.Transition(StatusPlanNeedsReview); err != nil {
			return err
		}
		clearPendingFeatureAttention(f)
		return nil
	})
}

func clearPendingFeatureAttention(f *Feature) {
	for i := range f.HelpQueue {
		if f.HelpQueue[i].Pending {
			f.HelpQueue[i].Pending = false
		}
	}
	for i := range f.PermissionsQueue {
		if f.PermissionsQueue[i].Pending {
			f.PermissionsQueue[i].Pending = false
		}
	}
}

// StartImplementation transitions a feature to Implementing and updates iteration count.
func (m *Manager) StartImplementation(featureID string) error {
	return m.Store.Modify(featureID, func(f *Feature) error {
		if err := f.Transition(StatusImplementing); err != nil {
			return err
		}
		f.CurrentPhase = PhaseImplement
		f.CurrentIteration = 1
		// Use existing cycle key (rebase-N, review-comments) if set,
		// or default to "implement" (or "phase-N-impl" for roadmap phases).
		// This preserves cycle keys across interrupt/fail → resume transitions.
		// Accumulate any in-flight time under the old key before switching.
		f.accumulateActiveTime()
		if !isImplementTimingKey(f.ActiveTimingKey) {
			if f.CurrentRoadmapPhase > 0 {
				f.ActiveTimingKey = fmt.Sprintf("phase-%d-impl", f.CurrentRoadmapPhase)
			} else {
				f.ActiveTimingKey = "implement"
			}
		}
		now := time.Now()
		f.ActivePhaseStart = &now
		if f.StartedAt == nil {
			f.StartedAt = &now
		}
		return nil
	})
}

// UpdateIteration updates the current iteration counter on a feature.
func (m *Manager) UpdateIteration(featureID string, iteration int) error {
	return m.Store.Modify(featureID, func(f *Feature) error {
		f.CurrentIteration = iteration
		return nil
	})
}

// CompleteImplementation transitions a feature to ReviewPassed.
func (m *Manager) CompleteImplementation(featureID string) error {
	return m.Transition(featureID, StatusReviewPassed)
}

// MarkCodeReady transitions a feature to CodeReady and sets CurrentPhase to Publish.
func (m *Manager) MarkCodeReady(featureID string) error {
	return m.Store.Modify(featureID, func(f *Feature) error {
		if f.Status == StatusFailed || f.HasTerminalFailure() {
			return fmt.Errorf("cannot mark code ready for feature %s with terminal failure", featureID)
		}
		if err := f.Transition(StatusCodeReady); err != nil {
			return err
		}
		f.CurrentPhase = PhasePublish
		return nil
	})
}

// MarkFinalReviewReady transitions a feature into the deferred end-of-feature
// Final Review pass: status becomes StatusFinalReviewing and CurrentPhase
// becomes PhaseFinalReview. Called by the orchestrator after the last roadmap-
// phase implement returns all_passed.
func (m *Manager) MarkFinalReviewReady(featureID string) error {
	return m.Store.Modify(featureID, func(f *Feature) error {
		if err := f.Transition(StatusFinalReviewing); err != nil {
			return err
		}
		f.CurrentPhase = PhaseFinalReview
		f.accumulateActiveTime()
		f.ActiveTimingKey = PhaseFinalReview.DirName()
		now := time.Now()
		f.ActivePhaseStart = &now
		if f.StartedAt == nil {
			f.StartedAt = &now
		}
		return nil
	})
}

// MarkPublished transitions a feature to Published and stores the PR URL.
// Publishable features must pass a non-empty prURL; otherwise the transition
// is refused so a stale "Published with no PR" state is unreachable.
func (m *Manager) MarkPublished(featureID, prURL string) error {
	return m.Store.Modify(featureID, func(f *Feature) error {
		if f.IsPublishable() && prURL == "" {
			return fmt.Errorf("MarkPublished: PR URL required for publishable feature %s", featureID)
		}
		if err := f.Transition(StatusPublished); err != nil {
			return err
		}
		f.CurrentPhase = PhasePublish
		f.SetPRURL(prURL)
		return nil
	})
}

// MarkDone transitions a feature directly to Done (for unpublished features
// that skip the publish phase).
func (m *Manager) MarkDone(featureID string) error {
	return m.Store.Modify(featureID, func(f *Feature) error {
		return f.Transition(StatusDone)
	})
}

// CompleteRefactor clears the refactor state after a successful refactor cycle.
func (m *Manager) CompleteRefactor(featureID string) error {
	return m.Store.Modify(featureID, func(f *Feature) error {
		f.RefactorPrompt = ""
		f.SetActiveCycleType("")
		return nil
	})
}

// ReturnToPublished transitions a CodeReady feature back to Published, preserving the existing PR URL.
// A publishable feature must already have a PR URL recorded (either at the
// feature level or on any repo); otherwise the transition is refused so we
// never promote to Published without a real PR on record.
//
// Re-entrancy / crash recovery:
//
//	(a) Idempotent on retry. The mutation is gated by f.Transition, which is
//	    a no-op when the feature is already StatusPublished, and the trailing
//	    field assignments (CurrentPhase, ActiveCycleType) are
//	    overwrite-with-the-same-value semantics on a republished feature.
//	    Re-issuing this call against a feature lacking a PR URL keeps
//	    returning the same guard error rather than corrupting state.
//	(b) On crash before Store.Modify returns: the modify wrapper writes
//	    feature.yaml atomically (rename), so persisted state is either the
//	    pre-call snapshot or the fully transitioned snapshot — never a
//	    half-written file. Recovery is the normal startup load.
func (m *Manager) ReturnToPublished(featureID string) error {
	return m.Store.Modify(featureID, func(f *Feature) error {
		if f.IsPublishable() && len(f.PRURLs()) == 0 {
			return fmt.Errorf("ReturnToPublished: publishable feature %s has no PR URL", featureID)
		}
		if err := f.Transition(StatusPublished); err != nil {
			return err
		}
		f.CurrentPhase = PhasePublish
		f.SetActiveCycleType("")
		return nil
	})
}

// StartAddressingReviews transitions a Published feature back to ImplementReady
// for addressing PR review comments.
func (m *Manager) StartAddressingReviews(featureID string) error {
	return m.Store.Modify(featureID, func(f *Feature) error {
		if err := f.Transition(StatusImplementReady); err != nil {
			return err
		}
		f.SetAddressingReviews(true)
		f.SetActiveCycleType(CycleReviewComments)
		f.CurrentPhase = PhaseImplement
		// Pre-set timing key — StartImplementation will use this
		f.ActiveTimingKey = "review-comments"
		return nil
	})
}

// ClearAddressingReviews clears the review-comments flow flag.
func (m *Manager) ClearAddressingReviews(featureID string) error {
	return m.Store.Modify(featureID, func(f *Feature) error {
		f.SetAddressingReviews(false)
		f.SetActiveCycleType("")
		return nil
	})
}

func (m *Manager) StartFeatureRebaseOperation(featureID string) error {
	return m.Store.Modify(featureID, func(f *Feature) error {
		if hasActiveNonRebaseCycle(f) {
			return fmt.Errorf("cannot start rebase operation while %s cycle is active", activeNonRebaseCycleType(f))
		}
		if hasActiveRebaseOperation(f) {
			return fmt.Errorf("rebase operation already active")
		}
		now := time.Now()
		f.SetActiveCycleType(CycleRebase)
		f.ActiveCycle = &CycleState{Type: CycleRebase, Status: RepoCycleRunning, Count: f.RebaseCount() + 1}
		f.RebaseOperation = &RebaseOperationState{
			Stage:     RebaseStageHarness,
			StartedAt: now,
			UpdatedAt: now,
			Repos:     map[string]*RebaseRepoProgress{},
		}
		return nil
	})
}

func (m *Manager) MarkFeatureRebaseStage(featureID string, stage RebaseStage) error {
	return m.Store.Modify(featureID, func(f *Feature) error {
		if hasActiveNonRebaseCycle(f) {
			return fmt.Errorf("cannot mark rebase stage while %s cycle is active", activeNonRebaseCycleType(f))
		}
		if !hasActiveRebaseOperation(f) {
			return fmt.Errorf("no active rebase operation")
		}
		now := time.Now()
		if f.RebaseOperation == nil {
			f.RebaseOperation = &RebaseOperationState{StartedAt: now, Repos: map[string]*RebaseRepoProgress{}}
		}
		f.RebaseOperation.Stage = stage
		f.RebaseOperation.UpdatedAt = now
		if f.ActiveCycle == nil {
			f.ActiveCycle = &CycleState{Type: CycleRebase, Status: RepoCycleRunning, Count: f.RebaseCount() + 1}
		} else {
			f.ActiveCycle.Type = CycleRebase
		}
		if stage == RebaseStageFinalReview {
			f.ActiveCycle.Status = RepoCycleReviewing
		}
		f.SetActiveCycleType(CycleRebase)
		return nil
	})
}

func (m *Manager) UpdateFeatureRebaseRepo(featureID, repoName string, status RebaseRepoStatus, progress RebaseRepoProgress) error {
	return m.Store.Modify(featureID, func(f *Feature) error {
		if hasActiveNonRebaseCycle(f) {
			return fmt.Errorf("cannot update rebase operation while %s cycle is active", activeNonRebaseCycleType(f))
		}
		if f.RebaseOperation == nil {
			return fmt.Errorf("no active rebase operation")
		}
		if f.RebaseOperation.Repos == nil {
			f.RebaseOperation.Repos = map[string]*RebaseRepoProgress{}
		}
		progress.Status = status
		progress.ConflictFiles = append([]string(nil), progress.ConflictFiles...)
		f.RebaseOperation.Repos[repoName] = &progress
		f.RebaseOperation.UpdatedAt = time.Now()
		return nil
	})
}

func (m *Manager) ClearFeatureRebaseOperation(featureID string) error {
	return m.Store.Modify(featureID, func(f *Feature) error {
		f.RebaseOperation = nil
		if f.ActiveCycle != nil && f.ActiveCycle.Type == CycleRebase {
			f.ActiveCycle = nil
		}
		if f.ActiveCycleType() == CycleRebase {
			f.SetActiveCycleType("")
		}
		return nil
	})
}

func hasActiveNonRebaseCycle(f *Feature) bool {
	if f.ActiveCycle != nil && f.ActiveCycle.Type != CycleRebase {
		return true
	}
	activeType := f.ActiveCycleType()
	return activeType != "" && activeType != CycleRebase
}

func hasActiveRebaseOperation(f *Feature) bool {
	if f.RebaseOperation != nil {
		return true
	}
	if f.ActiveCycle != nil && f.ActiveCycle.Type == CycleRebase {
		return true
	}
	return f.ActiveCycleType() == CycleRebase
}

func activeNonRebaseCycleType(f *Feature) RepoCycleType {
	if f.ActiveCycle != nil && f.ActiveCycle.Type != CycleRebase {
		return f.ActiveCycle.Type
	}
	return f.ActiveCycleType()
}

// StartRepoCycle starts a per-repo post-publish cycle.
// The feature stays StatusPublished; only the per-repo cycle state is set.
func (m *Manager) StartRepoCycle(featureID, repoName string, cycleType RepoCycleType) error {
	return m.Store.Modify(featureID, func(f *Feature) error {
		if cycleType == CycleRebase {
			return fmt.Errorf("per-repo rebase cycles are not supported; use StartFeatureRebaseOperation")
		}
		if f.RepoCycles == nil {
			f.RepoCycles = make(map[string]*RepoCycleState)
		}
		if existing, ok := f.RepoCycles[repoName]; ok && (existing.Status == RepoCycleRunning || existing.Status == RepoCycleReviewing || existing.Status == RepoCycleNeedUserInput) {
			return fmt.Errorf("%s is already running a %s cycle", repoName, existing.Type)
		}

		f.RepoCycles[repoName] = &RepoCycleState{
			Type:   cycleType,
			Status: RepoCycleRunning,
			Count:  1,
		}
		return nil
	})
}

// CompleteRepoCycle removes the per-repo cycle entry on success.
func (m *Manager) CompleteRepoCycle(featureID, repoName string) error {
	return m.Store.Modify(featureID, func(f *Feature) error {
		delete(f.RepoCycles, repoName)
		if len(f.RepoCycles) == 0 {
			f.RepoCycles = nil
		}
		return nil
	})
}

// RemoveRepoCycle removes a per-repo cycle entry without recording failure.
// Used when cleaning up stale cycle entries that cannot be restarted.
func (m *Manager) RemoveRepoCycle(featureID, repoName string) error {
	return m.Store.Modify(featureID, func(f *Feature) error {
		delete(f.RepoCycles, repoName)
		if len(f.RepoCycles) == 0 {
			f.RepoCycles = nil
		}
		return nil
	})
}

// FailRepoCycle marks a per-repo cycle as failed and clears any paused
// gate state so post-publish abort cannot leave dangling gate pointers.
// For refactor cycles, also clears the feature-level RefactorPrompt so the
// next refactor attempt starts from a clean slate.
func (m *Manager) FailRepoCycle(featureID, repoName, errMsg string) error {
	return m.Store.Modify(featureID, func(f *Feature) error {
		if f.RepoCycles == nil {
			return nil
		}
		rc, ok := f.RepoCycles[repoName]
		if !ok {
			return nil
		}
		rc.Status = RepoCycleFailed
		rc.LastError = errMsg
		rc.PendingNeedUserInputPath = ""
		if rc.Type == CycleRefactor {
			f.RefactorPrompt = ""
		}
		return nil
	})
}

// MarkRepoCycleReviewing transitions a repo's active cycle to the
// "reviewing" status, indicating the Final Review is in progress.
func (m *Manager) MarkRepoCycleReviewing(featureID, repoName string) error {
	return m.Store.Modify(featureID, func(f *Feature) error {
		if f.RepoCycles == nil {
			return fmt.Errorf("no active cycles")
		}
		rc, ok := f.RepoCycles[repoName]
		if !ok {
			return fmt.Errorf("no active cycle for repo %q", repoName)
		}
		rc.Status = RepoCycleReviewing
		return nil
	})
}

// HasActiveRepoCycles returns true if any repo has a running, reviewing, or
// need-user-input-paused cycle. Paused cycles count as active so post-publish
// gating reflects outstanding work waiting on user input.
func (m *Manager) HasActiveRepoCycles(featureID string) (bool, error) {
	f, err := m.Get(featureID)
	if err != nil {
		return false, err
	}
	for _, rc := range f.RepoCycles {
		if rc == nil {
			continue
		}
		switch rc.Status {
		case RepoCycleRunning, RepoCycleReviewing, RepoCycleNeedUserInput:
			return true, nil
		}
	}
	return false, nil
}

// ClearRepoCycles removes all per-repo cycle state (used by stop-all).
func (m *Manager) ClearRepoCycles(featureID string) error {
	return m.Store.Modify(featureID, func(f *Feature) error {
		f.RepoCycles = nil
		return nil
	})
}

// SetRepoCyclePlanPath records the plan artifact path for a per-repo cycle.
func (m *Manager) SetRepoCyclePlanPath(featureID, repoName, planPath string) error {
	return m.Store.Modify(featureID, func(f *Feature) error {
		if f.RepoCycles == nil {
			return fmt.Errorf("no active cycles")
		}
		if rc, ok := f.RepoCycles[repoName]; ok {
			rc.PlanPath = planPath
		}
		return nil
	})
}

// AdvanceRoadmapPhase increments the current roadmap phase and transitions
// the feature back to StatusPlanning for the next phase's plan creation.
// Called by TUI when a phase's implementation review passes and more phases remain.
func (m *Manager) AdvanceRoadmapPhase(featureID string) error {
	return m.Store.Modify(featureID, func(f *Feature) error {
		// When called after roadmap approval, the feature is already in StatusPlanning
		// (no self-transition needed). When called after a phase review passes, the
		// feature is in StatusReviewPassed and needs a real transition.
		if f.Status != StatusPlanning {
			if err := f.Transition(StatusPlanning); err != nil {
				return err
			}
		}
		f.CurrentRoadmapPhase++
		// Determine phase type
		f.RoadmapPhaseType = roadmapPhaseType(f.CurrentRoadmapPhase, f.TotalRoadmapPhases)
		f.CurrentPhase = PhasePlan
		f.CurrentIteration = 0
		f.PlanIteration = 0
		f.MaxPlanIterations = 0 // reset per-phase; loop uses config default
		f.ValidatingPlan = false
		f.ValidatorStatuses = nil
		// Clear the active plan artifact so startImplementationCmd re-resolves
		// for the new phase instead of reusing a stale rebase plan.
		delete(f.Artifacts, "plan")
		// RepoStates persists across phases. Touched is monotonic — once
		// any phase touches a repo, the flag stays true for the lifetime
		// of the feature. The per-phase ExecutionPlan is read fresh from
		// disk by the orchestrator and is no longer persisted.
		// Accumulate any in-flight time under the old key before switching to
		// the next phase-N-plan bucket. When AdvanceRoadmapPhase is called
		// after roadmap approval the feature stays in StatusPlanning (running),
		// so the Transition above is skipped and would not auto-accumulate;
		// without this call the strategic plan's elapsed time is silently
		// overwritten when ActivePhaseStart is reset below.
		f.accumulateActiveTime()
		if !isCycleTimingKey(f.ActiveTimingKey) {
			f.ActiveTimingKey = fmt.Sprintf("phase-%d-plan", f.CurrentRoadmapPhase)
		}
		now := time.Now()
		f.ActivePhaseStart = &now
		return nil
	})
}

// StartRoadmapPhaseImplementation marks a phase plan as complete and
// transitions to StatusImplementReady for the current roadmap phase.
func (m *Manager) StartRoadmapPhaseImplementation(featureID string) error {
	return m.Transition(featureID, StatusImplementReady)
}

// CompleteRoadmap marks the final roadmap phase as complete and
// transitions to StatusCodeReady.
func (m *Manager) CompleteRoadmap(featureID string) error {
	return m.Store.Modify(featureID, func(f *Feature) error {
		if err := f.Transition(StatusCodeReady); err != nil {
			return err
		}
		f.CurrentPhase = PhasePublish
		return nil
	})
}

// RecordRoadmapPhaseCommitAnchors persists per-repo full HEAD SHAs for a
// completed roadmap phase.
func (m *Manager) RecordRoadmapPhaseCommitAnchors(featureID string, phase int, anchors map[string]string) error {
	if phase <= 0 {
		return fmt.Errorf("invalid roadmap phase %d", phase)
	}
	return m.Store.Modify(featureID, func(f *Feature) error {
		if len(anchors) == 0 {
			return nil
		}
		r := f.Run()
		if r.RoadmapPhaseCommitAnchors == nil {
			r.RoadmapPhaseCommitAnchors = make(map[int]map[string]string)
		}
		copied := make(map[string]string, len(anchors))
		for repo, sha := range anchors {
			if sha == "" {
				continue
			}
			copied[repo] = sha
		}
		if len(copied) == 0 {
			return nil
		}
		r.RoadmapPhaseCommitAnchors[phase] = copied
		return nil
	})
}

// RecreateWorktree re-creates a worktree for a feature from its existing branch.
func (m *Manager) RecreateWorktree(featureID string) error {
	if m.Worktrees == nil {
		return fmt.Errorf("worktree manager not configured")
	}
	return m.Store.Modify(featureID, func(f *Feature) error {
		for i, repo := range f.Repos {
			if repo.WorktreePath != "" {
				continue // already has a worktree
			}
			if repo.Branch == "" {
				return fmt.Errorf("repo %s has no branch to recreate from", repo.Name)
			}
			wtPath, err := m.Worktrees.Create(repo.Path, repoWorkspaceSlug(f, repo), repo.Name, repo.Branch)
			if err != nil {
				return fmt.Errorf("recreating worktree for %s: %w", repo.Name, err)
			}
			f.Repos[i].WorktreePath = wtPath
		}
		return nil
	})
}

// CleanWorktree removes a completed feature's worktree but keeps the branch.
func (m *Manager) CleanWorktree(featureID string) error {
	if m.Worktrees == nil {
		return fmt.Errorf("worktree manager not configured")
	}
	// Load outside Modify since worktree removal is an external side effect
	// that should not be retried if Save fails.
	f, err := m.Store.Load(featureID)
	if err != nil {
		return fmt.Errorf("loading feature: %w", err)
	}
	for _, repo := range f.Repos {
		if repo.WorktreePath != "" {
			if err := m.Worktrees.Remove(repo.WorktreePath, false); err != nil {
				return fmt.Errorf("removing worktree for %s: %w", repo.Name, err)
			}
		}
	}
	return m.Store.Modify(featureID, func(f *Feature) error {
		for i := range f.Repos {
			f.Repos[i].WorktreePath = ""
		}
		return nil
	})
}

// MarkFailed transitions a feature to Failed with error context.
func (m *Manager) MarkFailed(featureID, failureType, lastError string) error {
	return m.Store.Modify(featureID, func(f *Feature) error {
		if err := f.Transition(StatusFailed); err != nil {
			return err
		}
		f.FailureType = failureType
		f.LastError = lastError
		return nil
	})
}

// RestartFromBeginning resets a feature back to its first pipeline phase by
// sealing the active run and forking a fresh one. It uses `RewindToPhase` plus
// a follow-up `ForceKBRebuild = true` mutation so sealed runs are preserved
// intact under `runs/run-NNN/`.
//
// Pipeline first-phase mapping: PipelineMoonshot/Large first phase is
// PhaseKnowledgeBase, but RewindToPhase rejects that target directly. We
// rewind to PhaseInquire (the first user-rewindable phase for those
// profiles) and rely on ForceKBRebuild to trigger KB rebuild on the next
// run. Medium features rewind to PhasePlan.
//
// Callers that used to rely on "silently discard worktree reset errors" now
// see warnings through `RewindToPhase`'s return; the current single call
// site discards them, matching prior behaviour in spirit.
func (m *Manager) RestartFromBeginning(featureID string) error {
	f, err := m.Store.Load(featureID)
	if err != nil {
		return fmt.Errorf("loading feature: %w", err)
	}
	rewindTarget := firstRewindablePhase(f.EffectivePipeline())
	if _, _, err := m.RewindToPhase(featureID, rewindTarget); err != nil {
		return fmt.Errorf("restarting from beginning: %w", err)
	}
	// Preserve legacy `ForceKBRebuild` semantics: a restart always wants KB
	// to rebuild. The active run is now the freshly-forked one.
	return m.Store.Modify(featureID, func(f *Feature) error {
		f.ForceKBRebuild = true
		return nil
	})
}

// firstRewindablePhase maps a pipeline profile to the first phase
// RewindToPhase will accept as a target. Moonshot/Large: PhaseInquire
// (KB rebuild happens via ForceKBRebuild on the new run). Medium: PhasePlan.
func firstRewindablePhase(p PipelineProfile) Phase {
	if p == PipelineMedium {
		return PhasePlan
	}
	return PhaseInquire
}

// PhasesFromOnwards returns all phases from targetPhase through PhaseImplement in logical order.
func PhasesFromOnwards(target Phase) []Phase {
	allPhases := []Phase{PhaseInquire, PhaseResearch, PhaseDesign, PhasePlan, PhaseImplement}
	var result []Phase
	found := false
	for _, p := range allPhases {
		if p == target {
			found = true
		}
		if found {
			result = append(result, p)
		}
	}
	return result
}

// phaseBeforeTarget returns the phase that precedes the target in logical order.
func phaseBeforeTarget(target Phase) Phase {
	switch target {
	case PhaseInquire:
		return PhaseKnowledgeBase
	case PhaseResearch:
		return PhaseInquire
	case PhaseDesign:
		return PhaseResearch
	case PhasePlan:
		return PhaseDesign
	case PhaseImplement:
		return PhasePlan
	default:
		return PhaseKnowledgeBase
	}
}

// completedPhaseFor determines the furthest phase completed by f based on its
// current status. ok is false when the status has no completed phase (e.g.
// StatusCreated, StatusBuildingKB).
func completedPhaseFor(f *Feature) (completedUpTo Phase, ok bool) {
	switch f.Status {
	case StatusInquiring, StatusInquireReady, StatusPromptNeedsReview:
		completedUpTo = PhaseInquire
	case StatusResearching, StatusDesignReady, StatusInquiryNeedsReview:
		completedUpTo = PhaseResearch
	case StatusDesigning, StatusPlanReady, StatusResearchNeedsReview:
		completedUpTo = PhaseDesign
	case StatusDesignNeedsReview:
		completedUpTo = PhaseDesign
	case StatusPlanNeedsReview:
		completedUpTo = PhasePlan
		if f.PendingReviewPhase != nil && f.IsRewind && *f.PendingReviewPhase == PhaseImplement {
			completedUpTo = PhaseImplement
		}
	case StatusPlanning, StatusImplementReady:
		completedUpTo = PhasePlan
	case StatusImplementing, StatusReviewPassed, StatusCodeReady, StatusPublished, StatusDone:
		completedUpTo = PhaseImplement
	case StatusFailed, StatusInterrupted:
		// For failed/interrupted, use CurrentPhase to determine how far we got
		completedUpTo = f.CurrentPhase
		if completedUpTo == PhaseReview {
			completedUpTo = PhaseImplement
		}
	default:
		return 0, false // StatusCreated, StatusBuildingKB, etc.
	}
	return completedUpTo, true
}

// RewindablePhases returns the phases a feature can rewind to, based on current status.
func RewindablePhases(f *Feature) []Phase {
	completedUpTo, ok := completedPhaseFor(f)
	if !ok {
		return nil
	}

	allPhases := []Phase{PhaseInquire, PhaseResearch, PhaseDesign, PhasePlan, PhaseImplement}
	var result []Phase
	for _, p := range allPhases {
		if p.LogicalOrder() > completedUpTo.LogicalOrder() {
			break
		}
		result = append(result, p)
	}
	return result
}

// RewindChoice represents a phase the user can rewind to, with optional escalation info.
type RewindChoice struct {
	Phase         Phase
	EscalatesTo   PipelineProfile // empty if no escalation needed
	OverridePhase Phase           // non-zero if the actual rewind target differs (e.g. KB Build)
}

// RewindChoicesForFeature computes rewind choices for phases within the current pipeline.
// Only phases that belong to the feature's current pipeline profile are included.
// Pipeline upgrades are handled separately via UpgradePipeline.
func RewindChoicesForFeature(f *Feature) []RewindChoice {
	// Determine completedUpTo using the same logic as RewindablePhases
	completedUpTo, ok := completedPhaseFor(f)
	if !ok {
		return nil
	}

	profile := f.EffectivePipeline()
	allPhases := []Phase{PhaseInquire, PhaseResearch, PhaseDesign, PhasePlan, PhaseImplement}
	var choices []RewindChoice
	for _, p := range allPhases {
		if p.LogicalOrder() > completedUpTo.LogicalOrder() {
			break
		}
		// Only include phases that belong to the current pipeline
		if !profile.HasPhase(p) {
			continue
		}
		choice := RewindChoice{Phase: p}
		if f.PipelineUpgradedFrom == PipelineMedium && !PipelineMedium.HasPhase(p) {
			// Feature was upgraded from medium without rewinding; KB was never built.
			// Pre-plan phases still require KB Build on rewind.
			choice.OverridePhase = PhaseKnowledgeBase
		}
		choices = append(choices, choice)
	}
	return choices
}

type partialRewindPlan struct {
	enabled      bool
	roadmapPhase int
	resetAnchors map[string]string
}

func (m *Manager) validatePartialRewindRequest(f *Feature, request RewindRequest) (partialRewindPlan, error) {
	if request.RoadmapPhase == 0 {
		return partialRewindPlan{}, nil
	}
	if request.TargetPhase != PhaseImplement {
		return partialRewindPlan{}, fmt.Errorf("roadmap phase rewind is only valid for Implement targets")
	}
	if f.TotalRoadmapPhases <= 1 {
		return partialRewindPlan{}, fmt.Errorf("roadmap phase rewind requires a multi-phase roadmap run")
	}
	if request.RoadmapPhase < 1 || request.RoadmapPhase > f.TotalRoadmapPhases {
		return partialRewindPlan{}, fmt.Errorf("roadmap phase %d out of range 1..%d", request.RoadmapPhase, f.TotalRoadmapPhases)
	}
	partial := partialRewindPlan{enabled: true, roadmapPhase: request.RoadmapPhase}
	if request.RoadmapPhase == 1 {
		return partial, nil
	}
	previousPhase := request.RoadmapPhase - 1
	anchors := f.Run().RoadmapPhaseCommitAnchors[previousPhase]
	if len(anchors) == 0 {
		return partialRewindPlan{}, fmt.Errorf("missing commit anchor for roadmap phase %d", previousPhase)
	}
	partial.resetAnchors = make(map[string]string)
	for _, repo := range f.Repos {
		if repo.WorktreePath == "" {
			continue
		}
		sha := anchors[repo.Name]
		if sha == "" {
			return partialRewindPlan{}, fmt.Errorf("missing commit anchor for roadmap phase %d repo %s", previousPhase, repo.Name)
		}
		partial.resetAnchors[repo.Name] = sha
	}
	return partial, nil
}

func roadmapPhaseType(phase, total int) string {
	if total == 1 {
		return "collapsed"
	}
	if phase == 1 {
		return "tracer-bullet"
	}
	return "tdd-fill-in"
}

// RewindToPhase resets a feature to just before the target phase, sealing the
// active run and forking a fresh one. The sealed run's directory tree
// (`runs/run-NNN/...`) is preserved verbatim — no destructive cleanup. Non-fatal
// warnings (PR close failure, backup branch failure, worktree reset failure)
// are returned alongside a nil error so the caller can surface them without
// aborting the rewind.
//
// Re-entrancy / crash recovery:
//
//	(a) The function is NOT re-entrant in the strict sense; calling it twice in
//	    quick succession on the same featureID could race on PR close and
//	    worktree reset. The Store.SealAndForkRun step is mutex-guarded and
//	    idempotent on a sealed run (it errors out instead of double-sealing).
//	    The PR close loop now iterates f.PRURLs() (legacy f.PRURL shadow +
//	    per-repo RepoStates[name].PRURL aggregated by repo name) and treats
//	    failures as non-fatal warnings, so a partial pass on retry simply
//	    closes whatever PRs are still open without re-closing already-closed
//	    PRs in any new way (gh pr close on a closed PR returns an error that
//	    surfaces as a warning, identical to the prior behavior).
//	(b) If the process crashes mid-call, persisted state recovery depends on
//	    where the crash hits: pre-seal leaves the active run unchanged; between
//	    seal-write and feature.yaml-bump leaves a sealed run on disk with
//	    ActiveRun still pointing at it (ScanRecovery will reconcile).
//	    A crash mid PR-close loop leaves the in-memory feature unchanged
//	    on disk because the loop has no Store.Modify side effect — only
//	    external PR state on GitHub may have advanced for the closed
//	    subset; the next rewind simply re-issues close calls and warns on
//	    the already-closed entries.
func (m *Manager) RewindToPhase(featureID string, targetPhase Phase) (warnings []string, effectiveTarget Phase, err error) {
	return m.RewindWithRequest(featureID, RewindRequest{TargetPhase: targetPhase})
}

func (m *Manager) RewindWithRequest(featureID string, request RewindRequest) (warnings []string, effectiveTarget Phase, err error) {
	targetPhase := request.TargetPhase
	f, err := m.Store.Load(featureID)
	if err != nil {
		return nil, 0, fmt.Errorf("loading feature: %w", err)
	}

	// Validate: target phase must be rewindable from the current state.
	// Use RewindChoicesForFeature so escalation targets are also valid.
	choices := RewindChoicesForFeature(f)
	validTarget := false
	for _, c := range choices {
		if c.Phase == targetPhase {
			validTarget = true
			break
		}
	}
	if !validTarget {
		if targetPhase == PhaseKnowledgeBase {
			return nil, 0, fmt.Errorf("cannot rewind to knowledge base phase")
		}
		return nil, 0, fmt.Errorf("cannot rewind to %s from current state %s", targetPhase, f.Status)
	}

	// Compute effective target (may differ if KB was never built)
	effectiveTarget = targetPhase
	if f.PipelineUpgradedFrom == PipelineMedium && !PipelineMedium.HasPhase(targetPhase) {
		// Feature was upgraded from medium via UpgradePipeline without rewinding.
		// Pre-plan phases still need KB Build because KB was never completed.
		effectiveTarget = PhaseKnowledgeBase
	}

	partial, err := m.validatePartialRewindRequest(f, request)
	if err != nil {
		return nil, 0, err
	}

	var warns []string

	// Mark the feature as interrupted so any running phase goroutine
	// (e.g. the implement loop) detects the rewind and stops writing
	// artifacts before we seal.
	if err := m.Store.Modify(featureID, func(f *Feature) error {
		f.Status = StatusInterrupted
		return nil
	}); err != nil {
		return nil, 0, fmt.Errorf("marking feature interrupted for rewind: %w", err)
	}
	// Give running goroutines a moment to observe the interrupted status.
	time.Sleep(500 * time.Millisecond)

	// Close every PR on record (skip for unpublishable features — no PR exists).
	// f.PRURLs() aggregates the legacy f.PRURL shadow and per-repo
	// RepoStates[name].PRURL into a single map keyed by repo name, so this
	// loop covers both single-repo legacy features and multi-repo features
	// without double-closing.
	if f.IsPublishable() && m.PRs != nil {
		for repoName, url := range f.PRURLs() {
			if url == "" {
				continue
			}
			if err := m.PRs.ClosePR(url); err != nil {
				warns = append(warns, fmt.Sprintf("failed to close PR for %s: %v", repoName, err))
			}
		}
	}

	// Create backup branch if rewinding past Implement and worktree has work.
	// Aggregate per-repo backup-branch names into a map for seal recording.
	// Per-repo failures warn but do not abort — rewind continues so the user
	// can still reach an uncorrupted new run.
	backupBranches := map[string]string{}
	if targetPhase.LogicalOrder() <= PhaseImplement.LogicalOrder() && m.Branches != nil {
		for _, repo := range f.Repos {
			if repo.WorktreePath == "" {
				continue
			}
			branchName, err := m.Branches.CreateBackupBranch(repo.WorktreePath, f.Slug)
			if err != nil {
				warns = append(warns, fmt.Sprintf("failed to create backup branch for %s: %v", repo.Name, err))
				continue
			}
			if branchName != "" {
				backupBranches[repo.Name] = branchName
			}
		}
	}

	// Reset worktree if rewinding past Implement
	if targetPhase.LogicalOrder() <= PhaseImplement.LogicalOrder() {
		if m.Worktrees != nil {
			for _, repo := range f.Repos {
				if repo.WorktreePath == "" {
					continue
				}
				var resetErr error
				if partial.enabled && partial.roadmapPhase > 1 {
					resetErr = m.Worktrees.ResetToCommit(repo.WorktreePath, partial.resetAnchors[repo.Name])
				} else if repo.BaseBranch != "" && repo.Publishable != nil && !*repo.Publishable {
					resetErr = m.Worktrees.ResetToBaseLocal(repo.WorktreePath, repo.BaseBranch)
				} else if repo.BaseBranch != "" {
					resetErr = m.Worktrees.ResetToBase(repo.WorktreePath, repo.BaseBranch)
				}
				if resetErr != nil {
					warns = append(warns, fmt.Sprintf("failed to reset worktree: %v", resetErr))
				}
			}
		}
	}

	// Write description-review.md for the rewind review session whenever the
	// target phase is the first phase of the feature's pipeline. Stays at
	// feature root (NOT inside a run dir) — it is a transient rewind-review
	// input, not a run artifact.
	if targetPhase == PhaseInquire ||
		(targetPhase == PhasePlan && f.EffectivePipeline() == PipelineMedium) {
		descPath := filepath.Join(m.Store.BaseDir, featureID, "description-review.md")
		_ = os.WriteFile(descPath, []byte(f.Description), 0o644)
	}

	// Seal + fork. The sealed run's `runs/run-NNN/` subtree is preserved
	// untouched on disk; the new run receives deep-copies of the carried
	// phase directories (per carryForwardDirs matrix) before its YAML lands.
	//
	// Re-entrancy: SealAndForkRun supports idempotent re-seal. A second call
	// after a crash-between-seal-and-bump observes oldRun.IsSealed(), lets
	// the seal closure re-stamp the seal fields, skips shadow sync (sealed
	// runs are immutable), rewrites the skeleton, populates, and commits.
	// Crash recovery: the fork closure returns a skeleton with
	// Committing:true that SealAndForkRun persists BEFORE running populate.
	// A crash during populate leaves runs/run-(N+1)/ on disk with
	// committing:true; a crash after populate but before ActiveRun is bumped
	// leaves runs/run-(N+1)/ with committing:false while ActiveRun still
	// points at run-N. Store.CleanupOrphanRuns — invoked once per feature
	// by Orchestrator.ScanRecovery at startup — deletes both shapes
	// (committing:true OR run_number > ActiveRun) and rolls
	// ActiveRun/RunCount back to the highest sealed run on disk if needed.
	//
	// Compute source/destination run directory paths BEFORE SealAndForkRun
	// runs. Active run is still run-N; the new run will be run-(N+1).
	currentRunNumber := f.ActiveRun
	sealedRunDir := m.Store.RunDir(featureID, currentRunNumber)
	newRunDir := m.Store.RunDir(featureID, currentRunNumber+1)

	tp := targetPhase
	updated, sealErr := m.Store.SealAndForkRun(featureID,
		func(oldRun *Run) error {
			now := time.Now()
			oldRun.SealedAt = &now
			oldRun.SealReason = SealReasonRewind
			oldRun.RewindTarget = &tp
			if partial.enabled {
				roadmapPhase := partial.roadmapPhase
				oldRun.RewindRoadmapPhase = &roadmapPhase
			} else {
				oldRun.RewindRoadmapPhase = nil
			}
			oldRun.BackupBranches = backupBranches
			return nil
		},
		// fork returns a skeleton new run with Committing:true. The Store
		// persists the skeleton to disk BEFORE calling populate, so a crash
		// during populate leaves a clearly-marked orphan on disk for
		// CleanupOrphanRuns to sweep.
		func(oldRun *Run) (*Run, error) {
			return &Run{
				RunNumber:      oldRun.RunNumber + 1,
				CarriedFromRun: oldRun.RunNumber,
				Committing:     true,
			}, nil
		},
		// populate performs the carry-forward copy + artifact-map rewrite
		// on the already-persisted skeleton. The Store clears Committing and
		// re-persists newRun after populate returns.
		func(oldRun, newRun *Run) error {
			// Static matrix per target; empty for PhaseInquire.
			dirs := append([]string(nil), carryForwardDirs(targetPhase)...)

			// For rewind-to-PhaseImplement, also carry every phase-NN/plan/
			// directory that exists in the sealed run.
			//
			// Pipeline-variant guard: this `targetPhase == PhaseImplement`
			// branch is reached by Medium, Large, AND Moonshot — it is a
			// dynamic-discovery scope (not a pipeline gate). Medium features
			// produce no `phase-NN/` subdirs, so `discoverCarriedPhasePlanDirs`
			// returns nil and only the static `plan`/`roadmap` entries (above)
			// may carry. Large/Moonshot populate `phase-NN/plan/` and have
			// no top-level `plan/` dir; both are listed in `dirs` but
			// `copyRunArtifactsForward` silently skips missing sources. All
			// three profiles thus reach the same path with pipeline-specific
			// behavior produced by the on-disk contents of the sealed run.
			if targetPhase == PhaseImplement {
				if partial.enabled {
					phaseHistory, err := discoverPartialImplementCarryForward(sealedRunDir, partial.roadmapPhase)
					if err != nil {
						return fmt.Errorf("discovering partial roadmap phase history: %w", err)
					}
					dirs = append(dirs, phaseHistory...)
				} else {
					phaseDirs, err := discoverCarriedPhasePlanDirs(sealedRunDir)
					if err != nil {
						return fmt.Errorf("discovering phase-NN plan dirs: %w", err)
					}
					dirs = append(dirs, phaseDirs...)
					phaseFiles, err := discoverCarriedPhaseFiles(sealedRunDir)
					if err != nil {
						return fmt.Errorf("discovering phase-root history files: %w", err)
					}
					dirs = append(dirs, phaseFiles...)
				}
			}

			// Deep-copy carried directories from sealed to new run. On error
			// the skeleton (committing:true) is left on disk for
			// CleanupOrphanRuns; ActiveRun is not bumped.
			if err := copyRunArtifactsForward(sealedRunDir, newRunDir, dirs); err != nil {
				return err
			}

			newRun.CarriedPhases = dirs
			// sealedRunDir is captured above from the pre-seal f.ActiveRun
			// and passed so carryForwardArtifactsMap can strip the run-001
			// prefix from absolute values, producing run-relative entries
			// per the roadmap's State Model.
			newRun.Artifacts = carryForwardArtifactsMapForRequest(oldRun.Artifacts, targetPhase, sealedRunDir, partial.roadmapPhase)
			newRun.RoadmapPhaseFrontendByPhase = carryForwardRoadmapPhaseFrontend(oldRun.RoadmapPhaseFrontendByPhase, targetPhase, partial.roadmapPhase)
			carryForwardCostLedgers(oldRun, newRun, targetPhase, partial.roadmapPhase)
			if partial.enabled {
				var err error
				newRun.RoadmapPhaseFrontendByPhase, err = restoreTargetPhaseFrontendFromCarriedPlan(
					newRun.RoadmapPhaseFrontendByPhase,
					newRunDir,
					partial.roadmapPhase,
				)
				if err != nil {
					return err
				}
				newRun.CurrentRoadmapPhase = partial.roadmapPhase
				newRun.TotalRoadmapPhases = oldRun.TotalRoadmapPhases
				newRun.RoadmapPhaseType = roadmapPhaseType(partial.roadmapPhase, oldRun.TotalRoadmapPhases)
				newRun.RoadmapPhaseCommitAnchors = carryForwardRoadmapPhaseCommitAnchors(oldRun.RoadmapPhaseCommitAnchors, partial.roadmapPhase)
				pendingRoadmapPhase := partial.roadmapPhase
				newRun.PendingRewindReviewRoadmapPhase = &pendingRoadmapPhase
			}
			return nil
		},
	)
	if sealErr != nil {
		return warns, effectiveTarget, fmt.Errorf("sealing and forking run: %w", sealErr)
	}

	// Stamp the new active run's lifecycle state. SealAndForkRun has already
	// bumped ActiveRun/RunCount and zeroed shadow fields via syncRunToShadows.
	modifyErr := m.Store.Modify(featureID, func(f *Feature) error {
		if effectiveTarget == PhaseKnowledgeBase {
			f.PipelineUpgradedFrom = ""
			f.Status = StatusCreated
			f.CurrentPhase = PhaseKnowledgeBase
			f.PendingReviewPhase = nil
			f.PendingRewindReviewRoadmapPhase = nil
			f.IsRewind = false
			return nil
		}
		f.Status = NeedsReviewForPhase(targetPhase)
		f.PendingReviewPhase = &tp
		clearPendingFeatureAttention(f)
		if partial.enabled {
			pendingRoadmapPhase := partial.roadmapPhase
			f.PendingRewindReviewRoadmapPhase = &pendingRoadmapPhase
		} else {
			f.PendingRewindReviewRoadmapPhase = nil
		}
		f.IsRewind = true
		f.CurrentPhase = phaseBeforeTarget(targetPhase)
		return nil
	})
	_ = updated
	return warns, effectiveTarget, modifyErr
}

// carryForwardDirs returns the static phase-dir names to deep-copy from the
// sealed run into the fresh run when rewinding to `target`. For
// PhaseImplement, the dynamic phase-NN/plan subdirectories are discovered at
// fork time via discoverCarriedPhasePlanDirs (they depend on what is on disk
// in the sealed run, not on a static list).
func carryForwardDirs(target Phase) []string {
	switch target {
	case PhaseResearch:
		return []string{"inquire"}
	case PhaseDesign:
		return []string{"inquire", "research"}
	case PhasePlan:
		return []string{"inquire", "research", "design"}
	case PhaseImplement:
		return []string{"inquire", "research", "design", "roadmap", "plan"}
	case PhaseFinalReview:
		// Rewinding into the deferred end-of-feature Final Review pass
		// preserves every upstream artifact dir AND the implement output so
		// the FR pass can re-run against the prior implementation. Per-phase
		// plan dirs are picked up dynamically by discoverCarriedPhasePlanDirs.
		return []string{"inquire", "research", "design", "roadmap", "plan", "implement"}
	}
	return nil
}

// UpgradePipeline escalates a feature's pipeline profile without rewinding.
// Gates are reset to the new profile's defaults. The current phase continues.
func (m *Manager) UpgradePipeline(featureID string, newProfile PipelineProfile) error {
	f, err := m.Store.Load(featureID)
	if err != nil {
		return fmt.Errorf("loading feature: %w", err)
	}
	current := f.EffectivePipeline()
	profileOrder := map[PipelineProfile]int{
		PipelineMedium: 0, PipelineLarge: 1, PipelineMoonshot: 2,
	}
	if profileOrder[newProfile] <= profileOrder[current] {
		return fmt.Errorf("cannot upgrade from %s to %s: must be a higher profile", current, newProfile)
	}
	return m.Store.Modify(featureID, func(f *Feature) error {
		// Record the original profile so KB restart can be enforced on rewind.
		// Only set once: medium→large→moonshot preserves the medium origin.
		if f.PipelineUpgradedFrom == "" {
			f.PipelineUpgradedFrom = current
		}
		f.Pipeline = newProfile
		f.Checkpoints = DefaultCheckpointsForProfile(newProfile)
		return nil
	})
}

// Delete removes a feature, cleaning up its worktrees and stored data.
func (m *Manager) Delete(featureID string) error {
	f, err := m.Store.Load(featureID)
	if err != nil {
		return fmt.Errorf("loading feature: %w", err)
	}

	if m.Worktrees != nil {
		seen := make(map[string]bool)
		for _, repo := range f.Repos {
			if repo.WorktreePath != "" {
				seen[repo.WorktreePath] = true
				_ = m.Worktrees.Remove(repo.WorktreePath, true)
			}
		}
		if setup := f.Run().Setup; setup != nil {
			for _, task := range setup.Tasks {
				if task.Kind != SetupTaskWorktree || task.Path == "" || seen[task.Path] {
					continue
				}
				seen[task.Path] = true
				_ = m.Worktrees.Remove(task.Path, true)
			}
		}
	}

	return m.Store.Delete(featureID)
}

// EnsureWorktree creates a worktree for a feature if one doesn't exist.
// Uses the feature's BaseBranch as the start point, which for sequential
// children points to the predecessor's branch.
func (m *Manager) EnsureWorktree(featureID string) error {
	if m.Worktrees == nil {
		return nil
	}
	return m.Store.Modify(featureID, func(f *Feature) error {
		if len(f.Repos) == 0 {
			return fmt.Errorf("feature has no repos")
		}
		repo := f.Repos[0]
		if repo.WorktreePath != "" {
			return nil // already has worktree
		}
		startPoint := repo.BaseBranch
		if startPoint == "" {
			startPoint = "HEAD"
		}
		workspaceSlug := repoWorkspaceSlug(f, repo)
		wtPath, err := m.Worktrees.Create(repo.Path, workspaceSlug, repo.Name, startPoint)
		if err != nil {
			return fmt.Errorf("creating worktree: %w", err)
		}
		f.Repos[0].WorktreePath = wtPath
		if f.Repos[0].Branch == "" {
			f.Repos[0].Branch = m.branchName(workspaceSlug)
		}
		return nil
	})
}

func generateID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func randomSuffix() string {
	b := make([]byte, 2)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Slugify converts a feature name to a valid branch suffix.
func Slugify(name string) string {
	s := strings.ToLower(name)
	var result []byte
	prevHyphen := false
	for _, c := range s {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			result = append(result, byte(c))
			prevHyphen = false
		} else if !prevHyphen && len(result) > 0 {
			result = append(result, '-')
			prevHyphen = true
		}
	}
	// Trim trailing hyphen
	if len(result) > 0 && result[len(result)-1] == '-' {
		result = result[:len(result)-1]
	}
	// Truncate to 40 chars
	if len(result) > 40 {
		result = result[:40]
		if result[len(result)-1] == '-' {
			result = result[:len(result)-1]
		}
	}
	return string(result)
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("reading %s: %w", src, err)
	}
	return os.WriteFile(dst, data, 0o644)
}

// InitRepoImpl ensures every repo in f.Repos has an entry in f.RepoStates
// and prunes entries for repos that are no longer part of the feature.
// Existing per-repo state (Touched, PRURL, LastError) survives: durable
// progress set by prior iterations must not be clobbered on restart,
// otherwise the engine cannot short-circuit and redoes approved work.
//
// Callers that genuinely want a full reset (e.g., RewindToPhase) must nil
// out f.RepoStates first; this function then populates fresh empty entries.
func (m *Manager) InitRepoImpl(featureID string) error {
	return m.Store.Modify(featureID, func(f *Feature) error {
		if f.RepoStates == nil {
			f.RepoStates = make(map[string]*RepoState, len(f.Repos))
		}
		want := make(map[string]struct{}, len(f.Repos))
		for _, r := range f.Repos {
			want[r.Name] = struct{}{}
			if _, ok := f.RepoStates[r.Name]; !ok {
				f.RepoStates[r.Name] = &RepoState{}
			}
		}
		for name := range f.RepoStates {
			if _, ok := want[name]; !ok {
				delete(f.RepoStates, name)
			}
		}
		return nil
	})
}

// SetRepoPublished updates a repo's implementation state after successful publish.
// Sets Touched=true, PRURL, and clears LastError.
func (m *Manager) SetRepoPublished(featureID, repoName, prURL string) error {
	return m.Store.Modify(featureID, func(f *Feature) error {
		if f.RepoStates == nil {
			f.RepoStates = make(map[string]*RepoState)
		}
		state, ok := f.RepoStates[repoName]
		if !ok || state == nil {
			state = &RepoState{}
			f.RepoStates[repoName] = state
		}
		state.Touched = true
		state.PRURL = prURL
		state.LastError = ""
		return nil
	})
}

// SetRepoPublishError records a publish error on a repo's state.
func (m *Manager) SetRepoPublishError(featureID, repoName, errMsg string) error {
	return m.Store.Modify(featureID, func(f *Feature) error {
		if f.RepoStates == nil {
			return nil
		}
		if state, ok := f.RepoStates[repoName]; ok && state != nil {
			state.LastError = errMsg
		}
		return nil
	})
}

// TryCompletePublish checks if all repos have published and transitions the feature
// to CodeReady → Published if so. Returns true if the feature was fully published.
func (m *Manager) TryCompletePublish(featureID string) (bool, error) {
	f, err := m.Get(featureID)
	if err != nil {
		return false, err
	}
	if !f.AllReposPublished() {
		return false, nil
	}
	// Only transition if feature is at ReviewPassed or CodeReady
	if f.Status != StatusReviewPassed && f.Status != StatusCodeReady {
		return false, nil
	}
	if f.Status == StatusReviewPassed {
		if err := m.MarkCodeReady(featureID); err != nil {
			return false, err
		}
	}
	prURL := f.FirstRepoPRURL()
	if err := m.MarkPublished(featureID, prURL); err != nil {
		return false, err
	}
	return true, nil
}

// RetryPhase clears any feature-level error/gate state so the unified
// phase-implement loop can re-run the active phase from iteration 1.
// Per-repo Touched flags are monotonic and intentionally preserved —
// RetryPhase only resets the cross-cutting LastError on phase-declared
// repos so the next pass starts clean.
//
// Caller responsibilities: identify the phase-declared repo subset (typically
// agent.PhaseScopeResult.Repos for the active phase plan); transition the
// feature back to a startable status (StatusImplementReady / StatusImplementing)
// after this call so the orchestrator can re-launch the phase loop.
func (m *Manager) RetryPhase(featureID string, repoNames []string) error {
	return m.Store.Modify(featureID, func(f *Feature) error {
		if f.RepoStates == nil {
			f.RepoStates = make(map[string]*RepoState, len(repoNames))
		}
		for _, name := range repoNames {
			state, ok := f.RepoStates[name]
			if !ok || state == nil {
				f.RepoStates[name] = &RepoState{}
				continue
			}
			state.LastError = ""
		}
		f.LastError = ""
		f.FailureType = ""
		f.PendingNeedUserInputPath = ""
		f.CurrentPhaseStatus = ""
		return nil
	})
}

// FailRepoImplementation marks a single repo's state as failed by recording
// the error message on RepoStates. Phase-atomic failures land via
// agent.AtomicPhaseStamp(PhaseOutcomeFailed); this helper survives for
// cycle-cleanup callers that fail one repo outside the phase-stamp path.
func (m *Manager) FailRepoImplementation(featureID, repoName, errMsg string) error {
	return m.Store.Modify(featureID, func(f *Feature) error {
		if f.RepoStates == nil {
			return fmt.Errorf("no repo_states for feature %q", featureID)
		}
		state, ok := f.RepoStates[repoName]
		if !ok || state == nil {
			state = &RepoState{}
			f.RepoStates[repoName] = state
		}
		state.Touched = true
		state.LastError = errMsg
		return nil
	})
}
