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
	"strconv"
	"strings"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
)

const (
	// ChildKindRefactor identifies a child launched as a refactor of its parent.
	ChildKindRefactor = "refactor"
	// ChildKindReviewFeedback identifies a child launched to address selected
	// pull request review feedback on its parent.
	ChildKindReviewFeedback = "review-feedback"
	// ChildKindRebase identifies a child launched to merge-base reconcile
	// behind parent repositories against their resolved targets.
	ChildKindRebase = "rebase"
	// ReviewFeedbackChildName is the fixed display and branch-slug seed for
	// every review-feedback child.
	ReviewFeedbackChildName = "Address review feedback"
	// RebaseChildName is the fixed display and branch-slug seed for every
	// rebase child.
	RebaseChildName = "Rebase feature branches"
	// ReviewFeedbackOutcomesFilename is the well-known JSON file inside the
	// child's active run directory where the agent records one entry per
	// selected comment (disposition + explanation). The integration tail
	// reads this artifact best-effort to build per-comment reply bodies.
	ReviewFeedbackOutcomesFilename = "review-feedback-outcomes.json"
)

// Typed child-launch failures. The server maps each to a stable machine
// code; callers should match with errors.Is / errors.As.
var (
	// ErrReviewFeedbackEmptySelection rejects a review-feedback child launch
	// that has no selected comments.
	ErrReviewFeedbackEmptySelection = errors.New("review feedback selection is empty")
	// ErrReviewFeedbackUnsupportedCommentType rejects a selected comment whose
	// GitHub comment type cannot be routed by the integration tail.
	ErrReviewFeedbackUnsupportedCommentType = errors.New("review feedback comment type is unsupported")
	// ErrReviewFeedbackUnknownRepo rejects a comment tagged with a repository
	// outside the parent feature.
	ErrReviewFeedbackUnknownRepo = errors.New("review feedback repository does not belong to parent")
	// ErrReviewFeedbackRepoHasNoPR rejects a comment whose parent repository
	// does not have a pull request to receive the eventual integration tail.
	ErrReviewFeedbackRepoHasNoPR = errors.New("review feedback repository has no pull request")
	// ErrRefactorParentNotFound: the requested parent feature does not exist.
	ErrRefactorParentNotFound = errors.New("refactor parent feature not found")
	// ErrRefactorParentIsChild: children are one level deep; a child cannot
	// itself be a refactor parent.
	ErrRefactorParentIsChild = errors.New("refactor parent is itself a child feature")
	// ErrRefactorParentStatusIneligible: only Published or CodeReady parents
	// can launch a refactor child.
	ErrRefactorParentStatusIneligible = errors.New("refactor parent status is not eligible")
	// ErrChildExecutionBlocked rejects children whose setup is incomplete or
	// whose execution shape is unsupported.
	ErrChildExecutionBlocked = errors.New("child features are not runnable")
	// ErrParentMutationLocked: the parent has an active child or a discard
	// intent that has not reached safe closure. Only read-only inspection
	// and paired Review editing are allowed.
	ErrParentMutationLocked = errors.New("parent mutation is locked while a child is active")
	// ErrChildMutationRestricted: the child operation is not in the allowed
	// set (ordinary execution controls, input handling, paired Review
	// editing, typed discard).
	ErrChildMutationRestricted = errors.New("child mutation is restricted")
	// ErrChildRelationshipClosed rejects every mutation addressed to a
	// child after its Completed or Discarded outcome becomes durable.
	ErrChildRelationshipClosed = errors.New("child relationship is closed")
	// ErrChildRelationshipInvalid identifies a persisted child relationship
	// whose outcome and close timestamp do not form a valid lifecycle state.
	ErrChildRelationshipInvalid = errors.New("child relationship is invalid")
	// ErrCascadeDeleteNotAvailable: parent Delete is not available while
	// a child is active. A complete recoverable cascade operation is
	// required before parent deletion can be allowed.
	ErrCascadeDeleteNotAvailable = errors.New("cascade delete is not available while a child is active")
	// ErrRebaseTargetResolution: a repository's merge target could not be
	// resolved at rebase child creation. The failing repo is named in the
	// typed error.
	ErrRebaseTargetResolution = errors.New("rebase target resolution failed")
	// ErrRebaseFetchFailed: a repository's remote fetch failed at rebase
	// child creation. The failing repo is named in the typed error.
	ErrRebaseFetchFailed = errors.New("rebase fetch failed")
	// ErrRebaseAlreadyUpToDate: every repository is already up to date with
	// its resolved target, so no rebase child is needed.
	ErrRebaseAlreadyUpToDate = errors.New("rebase feature is already up to date")
)

// ChildRelationshipError identifies the child record that violates a
// relationship lifecycle invariant.
type ChildRelationshipError struct {
	ChildID string
	Reason  string
}

func (e *ChildRelationshipError) Error() string {
	return fmt.Sprintf("child %s relationship is invalid: %s", e.ChildID, e.Reason)
}

// Unwrap lets callers classify malformed persisted records without parsing
// the contextual child identifier.
func (e *ChildRelationshipError) Unwrap() error {
	return ErrChildRelationshipInvalid
}

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

// RebaseTargetResolutionError names the repository whose merge target could
// not be resolved at rebase child creation.
type RebaseTargetResolutionError struct {
	Repo string
	Err  error
}

func (e *RebaseTargetResolutionError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("rebase target resolution failed for repo %s: %v", e.Repo, e.Err)
	}
	return fmt.Sprintf("rebase target resolution failed for repo %s", e.Repo)
}

func (e *RebaseTargetResolutionError) Unwrap() error {
	if e.Err != nil {
		return e.Err
	}
	return ErrRebaseTargetResolution
}

// Is supports errors.Is matching against ErrRebaseTargetResolution.
func (e *RebaseTargetResolutionError) Is(target error) bool {
	return target == ErrRebaseTargetResolution
}

// RebaseFetchError names the repository whose remote fetch failed at rebase
// child creation.
type RebaseFetchError struct {
	Repo string
	Err  error
}

func (e *RebaseFetchError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("fetch failed for repo %s: %v", e.Repo, e.Err)
	}
	return fmt.Sprintf("fetch failed for repo %s", e.Repo)
}

func (e *RebaseFetchError) Unwrap() error {
	if e.Err != nil {
		return e.Err
	}
	return ErrRebaseFetchFailed
}

// Is supports errors.Is matching against ErrRebaseFetchFailed.
func (e *RebaseFetchError) Is(target error) bool {
	return target == ErrRebaseFetchFailed
}

// RebaseAlreadyUpToDateError reports that every repository is already up to
// date with its resolved target. Targets carries the per-repo resolved
// targets so the message can name each one.
type RebaseAlreadyUpToDateError struct {
	Targets []RebaseRepoTarget
}

func (e *RebaseAlreadyUpToDateError) Error() string {
	pairs := make([]string, 0, len(e.Targets))
	for _, t := range e.Targets {
		pairs = append(pairs, fmt.Sprintf("%s@%s", t.Repo, t.Target))
	}
	return fmt.Sprintf("feature is already up to date with all targets: %s", strings.Join(pairs, ", "))
}

func (e *RebaseAlreadyUpToDateError) Unwrap() error {
	return ErrRebaseAlreadyUpToDate
}

// Is supports errors.Is matching against ErrRebaseAlreadyUpToDate.
func (e *RebaseAlreadyUpToDateError) Is(target error) bool {
	return target == ErrRebaseAlreadyUpToDate
}

// ChildRepoBase is the exact per-repository provenance captured at launch:
// the full parent HEAD the child worktree must be created at, plus the
// parent branch that tip belonged to.
type ChildRepoBase struct {
	Repo         string `yaml:"repo"`
	SHA          string `yaml:"sha"`
	ParentBranch string `yaml:"parent_branch,omitempty" `
}

// RebaseRepoTarget records the resolved merge target for a repository at
// rebase child creation time. The child never re-resolves or fetches; it
// reads the creation-time decision from the persisted relationship.
type RebaseRepoTarget struct {
	Repo   string `yaml:"repo" json:"repo"`
	Target string `yaml:"target" json:"target"`
	// Ref is the tracking ref used for the behind check: "origin/<target>"
	// for publishable repos, or "<target>" for local-only repos.
	Ref string `yaml:"ref,omitempty" json:"ref,omitempty"`
	// Publishable records whether the repo was publishable at creation,
	// determining whether behind-ness used the remote-tracking ref.
	Publishable bool `yaml:"publishable" json:"publishable"`
	// TargetSHA is the full commit SHA the target ref resolved to at the
	// creation-time fetch, captured immediately after behind-ness was
	// computed against the same ref. The mechanical integration gate reads
	// this SHA (never re-resolving the ref) so a target that moves after
	// creation does not change what the gate checks.
	TargetSHA string `yaml:"target_sha,omitempty" json:"target_sha,omitempty"`
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
	// Transaction is the ordered per-repository transaction journal for
	// child-to-parent integration. It is the sole integration record,
	// used for both single-repository and multi-repository children.
	Transaction *TransactionJournal `yaml:"transaction,omitempty"`
	// DiffSummary is the preserved read-only diff captured at close time,
	// before disposable worktrees and ephemeral branches are removed. It is
	// bounded at DiffSummaryBudget (oversized diffs are truncated with a
	// marker) and empty when no diff was preserved.
	DiffSummary string `yaml:"diff_summary,omitempty" json:"diff_summary,omitempty"`
	// LaunchReceipt is the durable review-feedback launch receipt pairing
	// this child with the committed draft revision, gate choice, and
	// reconciliation counts it was launched from. Only set for
	// review-feedback children launched from a pending draft.
	LaunchReceipt *ReviewFeedbackLaunchReceipt `yaml:"launch_receipt,omitempty" json:"launch_receipt,omitempty"`
	// Bases captures the exact parent tip per repository at launch time.
	Bases []ChildRepoBase `yaml:"bases,omitempty"`
	// RebaseTargets captures the resolved per-repo merge targets at rebase
	// child creation time. Only set for rebase children.
	RebaseTargets []RebaseRepoTarget `yaml:"rebase_targets,omitempty" json:"rebase_targets,omitempty"`
	// RebaseBehind captures the set of repositories that were behind their
	// resolved target at rebase child creation time. Only set for rebase
	// children.
	RebaseBehind []string `yaml:"rebase_behind,omitempty" json:"rebase_behind,omitempty"`
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

// IntegrationResumable reports whether an active child's durable integration
// record can resume instead of rerunning pipeline phases. Closed cleanup tails
// are owned exclusively by automatic reconciliation and are never executable
// through Restart.
func (f *Feature) IntegrationResumable() bool {
	if !f.IsActiveChild() {
		return false
	}
	if f.Parent.Transaction == nil || f.Parent.Transaction.Phase == "" {
		return false
	}
	return true
}

// AnyChildWorktreePending reports whether any child repository's disposable
// worktree path is still recorded, meaning per-repo cleanup has not durably
// completed for at least one repository.
func (f *Feature) AnyChildWorktreePending() bool {
	if f == nil {
		return false
	}
	for i := range f.Repos {
		if f.Repos[i].WorktreePath != "" {
			return true
		}
	}
	return false
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

// RebaseTargetForRepo returns the persisted resolved target for the named
// repository on a rebase child, or the zero value if not found.
func (f *Feature) RebaseTargetForRepo(repoName string) (RebaseRepoTarget, bool) {
	if f == nil || f.Parent == nil {
		return RebaseRepoTarget{}, false
	}
	for _, t := range f.Parent.RebaseTargets {
		if t.Repo == repoName {
			return t, true
		}
	}
	return RebaseRepoTarget{}, false
}

// IsRebaseBehindRepo reports whether the named repository was behind its
// resolved target at rebase child creation time.
func (f *Feature) IsRebaseBehindRepo(repoName string) bool {
	if f == nil || f.Parent == nil {
		return false
	}
	for _, r := range f.Parent.RebaseBehind {
		if r == repoName {
			return true
		}
	}
	return false
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
	// LaunchReceipt is the durable review-feedback launch receipt captured
	// before materialization, so an interrupted draft-based launch replays
	// the original child and counts instead of creating a second child.
	// Only set for ChildKindReviewFeedback intents launched from a pending
	// draft. Never carries renderer-authored comment content.
	LaunchReceipt *ReviewFeedbackLaunchReceipt `yaml:"launch_receipt,omitempty"`
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

// ReviewFeedbackComment is the complete selected GitHub comment payload that
// a review-feedback child needs for planning and later reply routing.
type ReviewFeedbackComment struct {
	Repo      string `yaml:"repo" json:"repo"`
	ID        int    `yaml:"id" json:"id"`
	Type      string `yaml:"type" json:"type"`
	Path      string `yaml:"path,omitempty" json:"path,omitempty"`
	Line      int    `yaml:"line,omitempty" json:"line,omitempty"`
	Author    string `yaml:"author,omitempty" json:"author,omitempty"`
	Body      string `yaml:"body,omitempty" json:"body,omitempty"`
	DiffHunk  string `yaml:"diff_hunk,omitempty" json:"diff_hunk,omitempty"`
	InReplyTo int    `yaml:"in_reply_to_id,omitempty" json:"in_reply_to_id,omitempty"`
	CreatedAt string `yaml:"created_at,omitempty" json:"created_at,omitempty"`
}

// ReviewFeedbackChildSpec carries the only launch-time choices supported by
// review-feedback children: the selected comment payloads and the coupled
// roadmap/phase-plan gate. A nil gate inherits the parent's Roadmap gate.
type ReviewFeedbackChildSpec struct {
	Comments    []ReviewFeedbackComment
	GateEnabled *bool
	// Receipt, when set, pins creation to the launch receipt computed from
	// the committed pending draft: creation durably stamps it on both the
	// creation intent and the child relationship, and under the creation
	// lock the pending draft's revision must still match Receipt's draft
	// revision — serializing a concurrent selection commit as a typed
	// revision conflict instead of launching from a consumed snapshot.
	Receipt *ReviewFeedbackLaunchReceipt
}

// RebaseChildSpec carries the creation-time resolved per-repo targets and
// behind set produced by the orchestrator preflight. The feature manager
// persists them on the child relationship so integration and future phases
// read the creation-time decision rather than re-resolving.
type RebaseChildSpec struct {
	// Bases are the captured parent tip SHAs per repo (fork-point pinning).
	Bases []ChildRepoBase
	// Targets are the resolved per-repo merge targets.
	Targets []RebaseRepoTarget
	// Behind is the set of repo names that were behind their resolved target.
	Behind []string
}

// CreateReviewFeedbackChild atomically launches a fixed-Medium child for the
// selected pull request comments. Validation that depends only on the spec is
// completed before loading or writing any feature state.
func (m *Manager) CreateReviewFeedbackChild(parentID string, spec ReviewFeedbackChildSpec) (*Feature, error) {
	if err := validateReviewFeedbackSpec(spec); err != nil {
		return nil, err
	}
	parent, err := m.Store.Load(parentID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrRefactorParentNotFound, parentID)
		}
		return nil, fmt.Errorf("loading parent feature: %w", err)
	}
	if err := ValidateRefactorParent(parent, nil); err != nil {
		return nil, err
	}
	if err := validateReviewFeedbackCommentRepos(parent, spec.Comments); err != nil {
		return nil, err
	}
	bases, err := m.preflightRefactorParent(parent)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	child, err := m.Store.CreateChildLocked(parentID, func(lockedParent *Feature, activeChild *Feature) (*Feature, *ChildCreationIntent, error) {
		if err := ValidateRefactorParent(lockedParent, activeChild); err != nil {
			return nil, nil, err
		}
		if err := validateReviewFeedbackCommentRepos(lockedParent, spec.Comments); err != nil {
			return nil, nil, err
		}
		if spec.Receipt != nil {
			// Re-verify the committed draft revision under the creation lock:
			// a selection that committed since the launch validated the draft
			// must conflict here rather than launch from a consumed snapshot.
			draft, err := m.Store.LoadReviewFeedbackDraft(lockedParent.ID)
			if err != nil {
				return nil, nil, err
			}
			currentRevision := int64(0)
			if draft != nil {
				currentRevision = draft.Revision
			}
			if draft == nil || currentRevision != spec.Receipt.DraftRevision {
				return nil, nil, &ReviewFeedbackRevisionConflictError{ParentID: lockedParent.ID, ExpectedRevision: spec.Receipt.DraftRevision, CurrentRevision: currentRevision}
			}
		}
		gate := lockedParent.Checkpoints.RoadmapReview
		if spec.GateEnabled != nil {
			gate = *spec.GateEnabled
		}
		checkpoints := lockedParent.Checkpoints
		checkpoints.RoadmapReview = gate
		checkpoints.PhasePlanReview = gate
		child := m.buildRefactorChild(lockedParent, RefactorChildSpec{
			Name:         ReviewFeedbackChildName,
			Description:  reviewFeedbackDescription(lockedParent, spec.Comments),
			Pipeline:     PipelineMedium,
			Checkpoints:  checkpoints,
			ExitCriteria: reviewFeedbackExitCriteria(spec.Comments),
		}, bases, now)
		child.Parent.Kind = ChildKindReviewFeedback
		child.Parent.LaunchReceipt = copyReviewFeedbackLaunchReceipt(spec.Receipt)
		child.ReviewFeedback = append([]ReviewFeedbackComment(nil), spec.Comments...)

		// Exit criteria is machine-generated per review-feedback pass from the
		// selected comments and must not pair back to the parent, so save and
		// restore it around applyResolvedReviewConfig.
		savedExitCriteria := lockedParent.ExitCriteria
		applyResolvedReviewConfig(lockedParent, child)
		lockedParent.ExitCriteria = savedExitCriteria
		intent := &ChildCreationIntent{
			ChildID:       child.ID,
			Kind:          ChildKindReviewFeedback,
			CreatedAt:     now,
			Child:         *child,
			Setup:         child.Run().Setup,
			LaunchReceipt: copyReviewFeedbackLaunchReceipt(spec.Receipt),
		}
		return child, intent, nil
	})
	if err != nil {
		return nil, err
	}
	return child, nil
}

// CreateRebaseChild atomically launches a fixed-Medium child that
// merge-reconciles behind parent repositories against their resolved targets.
// The orchestrator preflight resolves targets, fetches, and computes
// behind-ness before calling this method; the spec carries the results so
// the child relationship persists the creation-time decision. The parent
// must be a top-level Published or CodeReady feature with no active child
// and clean worktrees.
func (m *Manager) CreateRebaseChild(parentID string, spec RebaseChildSpec) (*Feature, error) {
	if len(spec.Behind) == 0 {
		return nil, &RebaseAlreadyUpToDateError{Targets: spec.Targets}
	}
	parent, err := m.Store.Load(parentID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrRefactorParentNotFound, parentID)
		}
		return nil, fmt.Errorf("loading parent feature: %w", err)
	}
	if err := ValidateRefactorParent(parent, nil); err != nil {
		return nil, err
	}

	bases := spec.Bases
	if len(bases) == 0 {
		bases, err = m.preflightRefactorParent(parent)
		if err != nil {
			return nil, err
		}
	}

	now := time.Now()
	child, err := m.Store.CreateChildLocked(parentID, func(lockedParent *Feature, activeChild *Feature) (*Feature, *ChildCreationIntent, error) {
		if err := ValidateRefactorParent(lockedParent, activeChild); err != nil {
			return nil, nil, err
		}
		child := m.buildRefactorChild(lockedParent, RefactorChildSpec{
			Name:         RebaseChildName,
			Description:  rebaseDescription(lockedParent, spec.Targets, spec.Behind),
			Pipeline:     PipelineMedium,
			Checkpoints:  lockedParent.Checkpoints,
			ExitCriteria: rebaseExitCriteria(spec.Targets, spec.Behind),
		}, bases, now)
		child.Parent.Kind = ChildKindRebase
		child.Parent.RebaseTargets = append([]RebaseRepoTarget(nil), spec.Targets...)
		child.Parent.RebaseBehind = append([]string(nil), spec.Behind...)

		savedExitCriteria := lockedParent.ExitCriteria
		applyResolvedReviewConfig(lockedParent, child)
		lockedParent.ExitCriteria = savedExitCriteria
		intent := &ChildCreationIntent{
			ChildID:   child.ID,
			Kind:      ChildKindRebase,
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

// rebaseDescription generates the deterministic, machine-authored description
// for a rebase child. It names each behind repo with its exact persisted
// resolved target ref, instructs the pipeline to merge (never rebase) the
// target into the working branch, adapt the feature's code to the base's new
// APIs where they collide, keep trivial clean merges minimal, leave
// up-to-date repos untouched, and never push to any remote.
func rebaseDescription(parent *Feature, targets []RebaseRepoTarget, behind []string) string {
	behindSet := make(map[string]bool, len(behind))
	for _, r := range behind {
		behindSet[r] = true
	}
	targetByRepo := make(map[string]RebaseRepoTarget, len(targets))
	for _, t := range targets {
		targetByRepo[t.Repo] = t
	}

	var sb strings.Builder
	sb.WriteString("This pass merge-reconciles the feature's branches against their resolved base targets.\n")
	sb.WriteString("\n## Instructions\n\n")
	sb.WriteString("- For each behind repository listed below, merge the resolved target into the repository's working branch using `git merge` (never rebase, no history rewrite).\n")
	sb.WriteString("- Adapt the feature's code to the base's new APIs where they collide with the feature's changes.\n")
	sb.WriteString("- Keep trivial clean merges minimal: do not opportunistically refactor or reformat code that merges cleanly.\n")
	sb.WriteString("- Leave up-to-date repositories completely untouched. Do not merge, rebase, or modify them in any way.\n")
	sb.WriteString("- Never push to any remote. All work is local; integration lands through the parent merge transaction.\n")
	sb.WriteString("- Do not fetch from any remote. The target refs were fetched at creation time and are already available in the shared object store.\n")

	for _, repo := range parent.Repos {
		if !behindSet[repo.Name] {
			continue
		}
		t := targetByRepo[repo.Name]
		sb.WriteString("\n## Repository: ")
		sb.WriteString(repo.Name)
		sb.WriteString("\n\nResolved target ref: ")
		sb.WriteString(t.Ref)
		sb.WriteString(" (branch: ")
		sb.WriteString(t.Target)
		sb.WriteString(")\n\n")
		sb.WriteString("Merge `")
		sb.WriteString(t.Ref)
		sb.WriteString("` into the working branch `")
		sb.WriteString(repo.Branch)
		sb.WriteString("`. Resolve any conflicts by adapting the feature's code to the base's new APIs. Keep the merge commit (do not squash).\n")
	}
	return sb.String()
}

// rebaseExitCriteria generates deterministic exit criteria for a rebase child.
// It states the per-behind-repo git-level completion facts the final review
// can check semantically, plus the invariants that up-to-date repos are
// unchanged and nothing was pushed.
func rebaseExitCriteria(targets []RebaseRepoTarget, behind []string) string {
	behindSet := make(map[string]bool, len(behind))
	for _, r := range behind {
		behindSet[r] = true
	}
	targetByRepo := make(map[string]RebaseRepoTarget, len(targets))
	for _, t := range targets {
		targetByRepo[t.Repo] = t
	}

	var sb strings.Builder
	sb.WriteString("This pass is complete when all of the following git-level completion facts hold for every behind repository:\n")
	for _, r := range behind {
		t := targetByRepo[r]
		sb.WriteString("\n### Repository: ")
		sb.WriteString(r)
		sb.WriteString("\n\n- The resolved target commit (`")
		sb.WriteString(t.Ref)
		sb.WriteString("`) is an ancestor of the working-branch head (`git merge-base --is-ancestor ")
		sb.WriteString(t.Ref)
		sb.WriteString(" HEAD` succeeds).\n")
		sb.WriteString("- No merge is in progress (no `rebase-merge` or `rebase-apply` directory, no `MERGE_HEAD`).\n")
		sb.WriteString("- No conflict markers remain in any tracked file (content scan for literal `<<<<<<<`, `=======`, `>>>>>>>` sequences is empty).\n")
		sb.WriteString("- The worktree is clean (`git status --porcelain` is empty).\n")
	}
	sb.WriteString("\n## Invariants\n\n")
	sb.WriteString("- Up-to-date repositories (not listed above) are completely unchanged: their worktree HEAD, branch, and content are byte-identical to the creation-time fork point.\n")
	sb.WriteString("- Nothing was pushed to any remote at any stage. All work is local.\n")
	return sb.String()
}

func validateReviewFeedbackSpec(spec ReviewFeedbackChildSpec) error {
	if len(spec.Comments) == 0 {
		return ErrReviewFeedbackEmptySelection
	}
	for _, comment := range spec.Comments {
		switch comment.Type {
		case git.CommentTypeReview, git.CommentTypeIssue, git.CommentTypeReviewBody:
		default:
			return fmt.Errorf("%w: comment %d has type %q", ErrReviewFeedbackUnsupportedCommentType, comment.ID, comment.Type)
		}
	}
	return nil
}

func validateReviewFeedbackCommentRepos(parent *Feature, comments []ReviewFeedbackComment) error {
	parentRepos := make(map[string]struct{}, len(parent.Repos))
	for _, repo := range parent.Repos {
		parentRepos[repo.Name] = struct{}{}
	}
	for _, comment := range comments {
		if _, ok := parentRepos[comment.Repo]; !ok {
			return fmt.Errorf("%w: %q", ErrReviewFeedbackUnknownRepo, comment.Repo)
		}
		state := parent.RepoStates[comment.Repo]
		if state == nil || strings.TrimSpace(state.PRURL) == "" {
			return fmt.Errorf("%w: %q", ErrReviewFeedbackRepoHasNoPR, comment.Repo)
		}
	}
	return nil
}

func reviewFeedbackDescription(parent *Feature, comments []ReviewFeedbackComment) string {
	prURLs := make(map[string]string, len(parent.RepoStates))
	for repo, state := range parent.RepoStates {
		if state != nil {
			prURLs[repo] = state.PRURL
		}
	}
	commentsByRepo := make(map[string][]ReviewFeedbackComment)
	repoOrder := make([]string, 0)
	for _, comment := range comments {
		if _, ok := commentsByRepo[comment.Repo]; !ok {
			repoOrder = append(repoOrder, comment.Repo)
		}
		commentsByRepo[comment.Repo] = append(commentsByRepo[comment.Repo], comment)
	}

	var description strings.Builder
	description.WriteString("This pass addresses selected pull request review feedback.\n")
	for _, repo := range repoOrder {
		description.WriteString("\n## Repository: ")
		description.WriteString(repo)
		description.WriteString("\n\nPull request: ")
		description.WriteString(prURLs[repo])
		description.WriteString("\n")
		for _, comment := range commentsByRepo[repo] {
			description.WriteString("\n### Comment ")
			description.WriteString(strconv.Itoa(comment.ID))
			description.WriteString(" (")
			description.WriteString(comment.Type)
			description.WriteString(")\n")
			if comment.Path != "" {
				description.WriteString("\nFile: ")
				description.WriteString(comment.Path)
				if comment.Line > 0 {
					description.WriteString(":")
					description.WriteString(strconv.Itoa(comment.Line))
				}
				description.WriteString("\n")
			}
			description.WriteString("\nAuthor: ")
			description.WriteString(comment.Author)
			description.WriteString("\n\nBody:\n")
			description.WriteString(comment.Body)
			description.WriteString("\n")
			if comment.DiffHunk != "" {
				description.WriteString("\nDiff hunk:\n```diff\n")
				description.WriteString(comment.DiffHunk)
				description.WriteString("\n```\n")
			}
		}
	}
	return description.String()
}

// reviewFeedbackExitCriteria generates deterministic exit criteria for a
// review-feedback child. Instead of inheriting the parent's exit criteria,
// the child is instructed to address or dismiss each selected comment with
// reasoning and to record one entry per selected comment — comment ID,
// disposition (addressed/dismissed), and a short explanation — in a JSON
// file at a fixed, well-known path inside the child's active run directory.
// Generation is byte-stable for the same selection.
func reviewFeedbackExitCriteria(comments []ReviewFeedbackComment) string {
	var sb strings.Builder
	sb.WriteString("This pass addresses selected pull request review feedback.\n")
	sb.WriteString("\nFor each selected comment listed below, address the feedback in the code or dismiss it with a short written justification. After all comments are handled, record the outcome of each one in a JSON file named `")
	sb.WriteString(ReviewFeedbackOutcomesFilename)
	sb.WriteString("` at the root of the active run directory.\n")
	sb.WriteString("\nThe JSON file must be an array of objects, one per selected comment, each with these fields:\n")
	sb.WriteString("- `id` (integer): the GitHub comment ID\n")
	sb.WriteString("- `disposition` (string): `\"addressed\"` if the code was changed to resolve the comment, or `\"dismissed\"` if the comment was intentionally not acted on\n")
	sb.WriteString("- `explanation` (string): a one-sentence summary of what was done or why it was dismissed\n")
	sb.WriteString("\nSelected comments to handle:\n")
	for _, c := range comments {
		fmt.Fprintf(&sb, "- Comment %d (%s, repo %s)", c.ID, c.Type, c.Repo)
		if c.Path != "" {
			fmt.Fprintf(&sb, " in %s", c.Path)
			if c.Line > 0 {
				fmt.Fprintf(&sb, ":%d", c.Line)
			}
		}
		sb.WriteString("\n")
	}
	sb.WriteString("\nEvery selected comment must have exactly one entry in the outcomes file. The pass is complete when all comments are handled and the outcomes file is written.")
	return sb.String()
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
	if err := ValidateRefactorParent(parent, nil); err != nil {
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
		if err := ValidateRefactorParent(lockedParent, activeChild); err != nil {
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
		applyResolvedReviewConfig(lockedParent, child)
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

// applyResolvedReviewConfig preserves the paired-config invariant at child
// creation: every shared review axis is committed to both records under the
// store's creation lock, while the child-specific pipeline remains separate.
func applyResolvedReviewConfig(parent, child *Feature) {
	parent.Checkpoints = child.Checkpoints
	parent.Models = child.Models
	parent.Effort = child.Effort
	parent.RiskLevel = child.RiskLevel
	parent.ExitCriteria = child.ExitCriteria
	parent.Inquireness = child.Inquireness
}

// ValidateRefactorParent enforces the launch rules against the parent. When
// activeChild is non-nil the caller already knows an active child exists.
func ValidateRefactorParent(parent *Feature, activeChild *Feature) error {
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
	if m.Worktrees == nil {
		return nil, fmt.Errorf("cleanliness inspection is not configured")
	}
	var bases []ChildRepoBase
	var dirty []RepoDirtyDiagnostics
	for _, repo := range parent.Repos {
		path := repo.WorktreePath
		if path == "" {
			path = repo.Path
		}
		report, err := m.Worktrees.InspectCleanliness(path, DefaultDirtyPathLimit)
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

// resolveHeadSHA captures the full HEAD of a worktree.
func (m *Manager) resolveHeadSHA(path string) (string, error) {
	if m.Worktrees == nil {
		return "", fmt.Errorf("exact head capture is not configured")
	}
	return m.Worktrees.CurrentHeadSHA(path)
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
	branch := git.BranchName(workspaceSlug)

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
