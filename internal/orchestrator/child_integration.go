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

package orchestrator

import (
	"errors"
	"fmt"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// This file owns the child execution and integration seam: a setup-complete
// Medium single-repository child runs the ordinary Plan/Implement/Review
// pipeline, and a successful final review enters an explicit local-
// integration stage instead of any child delivery path. A durable
// no-fast-forward merge boundary on the recorded parent branch closes the
// child, moves the parent to CodeReady, cleans disposable child resources,
// and only then evaluates the parent's current publish configuration — the
// child's Completed close and the parent's CodeReady transition are durable
// before publication begins, so a failed push or pull request never reopens
// the child or undoes the local merge.

// childHeadReader is the exact-HEAD lookup integration requires; it is
// satisfied by *git.WorktreeManager and asserted structurally so tests can
// substitute fakes without widening ports.WorktreeOperator.
type childHeadReader interface {
	CurrentHeadSHA(worktreePath string) (string, error)
}

// childBranchReader reports the branch checked out in a worktree; satisfied
// by *git.WorktreeManager.
type childBranchReader interface {
	CurrentBranch(worktreePath string) string
}

// childMerger creates the explicit two-parent no-fast-forward merge boundary
// inside the parent worktree; satisfied by *git.WorktreeManager.
type childMerger interface {
	MergeNoFF(worktreePath, ref, message string) error
}

// checkChildExecution is the fail-closed capability gate for child feature
// execution. Queued, setting-up, and failed-setup children stay reachable
// only through RunSetup / RetrySetup; setup-complete children must satisfy
// the supported execution shape (active Medium, exactly one repository); a
// child whose relationship has settled can never replay pipeline phases —
// with allowClosedRestart the gate widens only for a Completed child whose
// impermanent closure tail is genuinely resumable (Restart's sole route
// back into settleChildClosureTail). Feature-lookup failures propagate so
// the gate can never be silently skipped.
func (o *Orchestrator) checkChildExecution(featureID string, allowClosedRestart bool) error {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return fmt.Errorf("loading feature: %w", err)
	}
	if !f.IsChild() {
		return nil
	}
	if !f.IsActiveChild() {
		if allowClosedRestart && f.IntegrationResumable() {
			return nil
		}
		return fmt.Errorf("%w: %s", feature.ErrChildExecutionClosed, featureID)
	}
	if !f.ChildSetupComplete() {
		return fmt.Errorf("%w: %s", feature.ErrChildExecutionBlocked, featureID)
	}
	return f.ChildExecutionCapability()
}

// ErrChildIntegrationRefused marks the durable refusal conditions under
// which integration never mutates the parent: closed relationship, parent
// record or repository mismatch, more than one child repository, or a final
// review that is not durably approved.
var ErrChildIntegrationRefused = errors.New("child integration refused")

// runChildIntegration carries an approved child through local integration,
// closure, cleanup, and the parent's publish decision. Every step is
// idempotent: the anchors and merge HEAD are durable on the child record, so
// a Restart after any failure replays exactly the unfinished steps and never
// reruns Plan, Implement, or an approved Final Review.
//
// Retryable preflight/merge failures are recorded as structured integration
// attention on the child (parent ref untouched) and reported through
// integration events; the function returns nil for those so the completion
// pipeline never marks the child failed. Durable refusals and internal
// persistence errors are returned to the caller.
//
// A child whose relationship is already closed re-enters only the impermanent
// closure tail (cleanup-warning settlement and the publish handoff); the
// merge and closure themselves are never replayed.
func (o *Orchestrator) runChildIntegration(childID string) error {
	child, err := o.deps.Lifecycle.Get(childID)
	if err != nil {
		return fmt.Errorf("load child: %w", err)
	}
	if child == nil || !child.IsChild() {
		return fmt.Errorf("%w: feature is not a child feature", ErrChildIntegrationRefused)
	}
	if child.Parent.CloseOutcome != "" {
		// The relationship is settled: the merge and closure are durable and
		// must never replay. Only a completed relationship with a recorded
		// merge may resume its impermanent closure tail.
		if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted ||
			child.Parent.Integration == nil || child.Parent.Integration.MergeHEAD == "" {
			return fmt.Errorf("%w: child relationship %s already closed (%s)", ErrChildIntegrationRefused, child.ID, child.Parent.CloseOutcome)
		}
		return o.settleChildClosureTail(childID, child.Parent.ParentID)
	}
	if err := validateChildForIntegration(child); err != nil {
		return err
	}
	parent, err := o.deps.Lifecycle.Get(child.Parent.ParentID)
	if err != nil {
		return fmt.Errorf("%w: parent %s unreadable: %v", ErrChildIntegrationRefused, child.Parent.ParentID, err)
	}
	if err := validateParentForIntegration(child, parent); err != nil {
		return err
	}

	childRepo := child.Repos[0]
	childWorktree := childRepo.WorktreePath
	if childWorktree == "" {
		childWorktree = childRepo.Path
	}
	parentRepo := featureRepoByName(parent, childRepo.Name)
	parentWorktree := parentRepo.WorktreePath
	if parentWorktree == "" {
		parentWorktree = parentRepo.Path
	}

	integration := child.Parent.Integration
	if integration == nil || integration.ChildHeadSHA == "" {
		integration, err = o.captureChildIntegrationAnchors(child, parentRepo, childWorktree, parentWorktree)
		if err != nil {
			return err
		}
	}

	if integration.MergeHEAD == "" {
		// Recheck the parent worktree with the same staged, unstaged, and
		// untracked cleanliness contract used at launch, immediately before
		// any merge. A dirty parent leaves its ref unchanged and parks the
		// child at integration attention.
		dirty, err := o.childIntegrationDirty(parentWorktree, parentRepo.Name)
		if err != nil {
			return err
		}
		if len(dirty) > 0 {
			return o.recordChildIntegrationAttention(child, "parent worktree has uncommitted changes", dirty)
		}
		if err := o.mergeChildIntoParent(child, integration, parentWorktree); err != nil {
			return o.recordChildIntegrationAttention(child, err.Error(), nil)
		}
		mergeHEAD, err := o.childHeadSHA(parentWorktree)
		if err != nil {
			return fmt.Errorf("capture parent merge head: %w", err)
		}
		if err := o.deps.Store.Modify(childID, func(f *feature.Feature) error {
			f.Parent.Integration.MergeHEAD = mergeHEAD
			f.Parent.Integration.Phase = feature.ChildIntegrationPhaseMerged
			f.Parent.Integration.Attention = ""
			f.Parent.Integration.Dirty = nil
			return nil
		}); err != nil {
			return fmt.Errorf("record integration merge head: %w", err)
		}
	}

	return o.closeChildAfterMerge(childID, parent.ID)
}

// validateChildForIntegration enforces the durable preconditions under which
// local integration may touch an open child relationship.
func validateChildForIntegration(child *feature.Feature) error {
	if len(child.Repos) != 1 {
		return fmt.Errorf("%w: child %s has %d repositories; multi-repository integration is not available", ErrChildIntegrationRefused, child.ID, len(child.Repos))
	}
	if child.Status != feature.StatusReviewPassed {
		return fmt.Errorf("%w: final review is not durably approved for child %s (status %s)", ErrChildIntegrationRefused, child.ID, child.Status)
	}
	return nil
}

// validateParentForIntegration confirms the persisted relationship still
// describes the parent record and its single shared repository.
func validateParentForIntegration(child, parent *feature.Feature) error {
	if parent == nil || parent.IsChild() {
		return fmt.Errorf("%w: parent record %s no longer matches the relationship", ErrChildIntegrationRefused, child.Parent.ParentID)
	}
	parentRepo := featureRepoByName(parent, child.Repos[0].Name)
	if parentRepo == nil {
		return fmt.Errorf("%w: parent %s no longer has repository %s", ErrChildIntegrationRefused, parent.ID, child.Repos[0].Name)
	}
	if parentRepo.Path != child.Repos[0].Path {
		return fmt.Errorf("%w: repository %s path changed since launch (%s != %s)", ErrChildIntegrationRefused, child.Repos[0].Name, parentRepo.Path, child.Repos[0].Path)
	}
	return nil
}

func featureRepoByName(f *feature.Feature, name string) *feature.FeatureRepo {
	if f == nil {
		return nil
	}
	for i := range f.Repos {
		if f.Repos[i].Name == name {
			return &f.Repos[i]
		}
	}
	return nil
}

// captureChildIntegrationAnchors commits every remaining child change and
// durably records the resulting full child HEAD plus the current parent
// anchor BEFORE the parent branch is touched.
func (o *Orchestrator) captureChildIntegrationAnchors(child *feature.Feature, parentRepo *feature.FeatureRepo, childWorktree, parentWorktree string) (*feature.ChildIntegration, error) {
	if o.deps.Publisher == nil {
		return nil, fmt.Errorf("child integration: publisher not configured")
	}
	childHEAD, err := o.deps.Publisher.CommitAllAndGetHead(childWorktree, fmt.Sprintf("Integration commit for refactor child %s", child.ID))
	if err != nil {
		return nil, fmt.Errorf("commit child changes: %w", err)
	}
	anchor, err := o.childHeadSHA(parentWorktree)
	if err != nil {
		return nil, fmt.Errorf("capture parent anchor: %w", err)
	}
	integration := &feature.ChildIntegration{
		ParentBranch:    parentRepo.Branch,
		ParentAnchorSHA: anchor,
		ChildHeadSHA:    childHEAD,
		Phase:           feature.ChildIntegrationPhasePending,
	}
	if err := o.deps.Store.Modify(child.ID, func(f *feature.Feature) error {
		f.Parent.Integration = integration
		return nil
	}); err != nil {
		return nil, fmt.Errorf("record integration anchors: %w", err)
	}
	return integration, nil
}

// childIntegrationDirty returns categorized parent-worktree diagnostics. A
// missing or failing inspection fails closed: integration must never merge
// into an unverified parent.
func (o *Orchestrator) childIntegrationDirty(parentWorktree, repoName string) ([]feature.RepoDirtyDiagnostics, error) {
	if o.deps.Cleanliness == nil {
		return nil, fmt.Errorf("child integration: cleanliness inspection is not configured")
	}
	report, err := o.deps.Cleanliness.InspectCleanliness(parentWorktree, feature.DefaultDirtyPathLimit)
	if err != nil {
		return nil, fmt.Errorf("inspecting parent worktree %s: %w", repoName, err)
	}
	if !report.Dirty() {
		return nil, nil
	}
	return []feature.RepoDirtyDiagnostics{{
		Repo:           repoName,
		Path:           parentWorktree,
		Staged:         report.Staged,
		Unstaged:       report.Unstaged,
		Untracked:      report.Untracked,
		StagedTotal:    report.StagedTotal,
		UnstagedTotal:  report.UnstagedTotal,
		UntrackedTotal: report.UntrackedTotal,
	}}, nil
}

// mergeChildIntoParent creates the explicit no-fast-forward boundary on the
// recorded parent branch. The parent worktree must still have the recorded
// parent branch checked out, and the merge applies the recorded child head
// SHA — never the mutable child branch name — so the durable attempt always
// describes exactly the commits integrated. The merge helper aborts any
// recorded in-progress merge, so the parent ref stays at its pre-integration
// anchor on failure.
func (o *Orchestrator) mergeChildIntoParent(child *feature.Feature, integration *feature.ChildIntegration, parentWorktree string) error {
	branches, ok := o.deps.Worktrees.(childBranchReader)
	if !ok {
		return fmt.Errorf("child integration: branch inspection is not configured")
	}
	merger, ok := o.deps.Worktrees.(childMerger)
	if !ok {
		return fmt.Errorf("child integration: merge capability is not configured")
	}
	if current := branches.CurrentBranch(parentWorktree); current != integration.ParentBranch {
		return fmt.Errorf("parent worktree %s has branch %q checked out; integration requires the recorded parent branch %q", parentWorktree, current, integration.ParentBranch)
	}
	message := fmt.Sprintf("Merge refactor child %s into %s", child.ID, integration.ParentBranch)
	if err := merger.MergeNoFF(parentWorktree, integration.ChildHeadSHA, message); err != nil {
		return fmt.Errorf("merge child head %s into %s: %w", integration.ChildHeadSHA, integration.ParentBranch, err)
	}
	return nil
}

// recordChildIntegrationAttention parks the child at the retryable
// integration boundary with structured diagnostics and notifies consumers.
// The child stays active and approved; replay is Restart's job.
func (o *Orchestrator) recordChildIntegrationAttention(child *feature.Feature, attention string, dirty []feature.RepoDirtyDiagnostics) error {
	if err := o.deps.Store.Modify(child.ID, func(f *feature.Feature) error {
		if f.Parent.Integration == nil {
			f.Parent.Integration = &feature.ChildIntegration{}
		}
		f.Parent.Integration.Phase = feature.ChildIntegrationPhaseAttention
		f.Parent.Integration.Attention = attention
		f.Parent.Integration.Dirty = dirty
		f.LastError = attention
		return nil
	}); err != nil {
		return fmt.Errorf("record integration attention: %w", err)
	}
	o.emitEvent(ports.Event{
		Type:             ports.RepoStatusChanged,
		FeatureID:        child.ID,
		RelatedFeatureID: child.Parent.ParentID,
		RepoName:         child.Repos[0].Name,
		Message:          "child integration needs attention: " + attention,
	})
	return nil
}

// closeChildAfterMerge performs the recoverable relationship transition: the
// parent moves to CodeReady first so a failure there leaves the child open
// and the whole transition retryable, then the child's Completed outcome and
// close timestamp are persisted. Every step is idempotent; repeated entry is
// a no-op for finished steps.
func (o *Orchestrator) closeChildAfterMerge(childID, parentID string) error {
	parent, err := o.deps.Lifecycle.Get(parentID)
	if err != nil {
		return fmt.Errorf("reload parent: %w", err)
	}
	if parent.Status != feature.StatusCodeReady {
		if err := o.deps.Lifecycle.MarkCodeReady(parentID); err != nil {
			return fmt.Errorf("mark parent code ready: %w", err)
		}
	}

	now := time.Now()
	if err := o.deps.Store.Modify(childID, func(f *feature.Feature) error {
		if f.Parent.CloseOutcome != "" {
			return nil
		}
		f.Parent.CloseOutcome = feature.ChildCloseOutcomeCompleted
		f.Parent.ClosedAt = &now
		f.LastError = ""
		return nil
	}); err != nil {
		return fmt.Errorf("close child relationship: %w", err)
	}

	return o.settleChildClosureTail(childID, parentID)
}

// settleChildClosureTail is the impermanent (fully retryable) end of the
// integration boundary: cleanup disposable child resources, durably record
// the cleanup outcome, and only then hand delivery back to the parent's
// current publish configuration. It is safe to re-enter on a closed child —
// the merge and closure are never touched here. Any storage failure is
// returned so callers never observe a settled closure tail whose durable
// state was not written.
func (o *Orchestrator) settleChildClosureTail(childID, parentID string) error {
	child, err := o.deps.Lifecycle.Get(childID)
	if err != nil {
		return fmt.Errorf("reload child: %w", err)
	}

	// Cleanup failure records a warning and never reopens the child or
	// undoes the merge; the retry path clears it idempotently.
	warning := o.cleanupChildResources(child)
	if err := o.recordCleanupWarning(childID, warning); err != nil {
		return fmt.Errorf("record cleanup warning: %w", err)
	}
	o.emitEvent(ports.Event{
		Type:             ports.RepoStatusChanged,
		FeatureID:        childID,
		RelatedFeatureID: parentID,
		Message:          "refactor child integrated and closed",
	})

	// Hand delivery back to the parent: manual publish stays CodeReady with
	// its ordinary flow; auto-publish reuses the normal per-repository
	// publication path. Push or pull-request failure leaves the parent
	// CodeReady and the child Completed.
	parent, err := o.deps.Lifecycle.Get(parentID)
	if err != nil {
		return fmt.Errorf("reload parent: %w", err)
	}
	if parent.IsPublishable() && parent.Checkpoints.AutoPublish() {
		if err := o.Publish(parentID); err != nil {
			o.emitEvent(ports.Event{
				Type:      ports.RepoStatusChanged,
				FeatureID: parentID,
				Message:   "parent auto-publish after child integration failed: " + err.Error(),
			})
		}
	}
	return nil
}

// recordCleanupWarning durably records the outcome of a cleanup pass (empty
// clears a previous warning) so a cleanup failure stays visible and retryable
// across restarts.
func (o *Orchestrator) recordCleanupWarning(childID, warning string) error {
	return o.deps.Store.Modify(childID, func(f *feature.Feature) error {
		if f.Parent.Integration != nil {
			f.Parent.Integration.CleanupWarning = warning
		}
		return nil
	})
}

// worktreeRefRemover carries the recorded main-repository and branch
// identity into cleanup, so a retried removal still reaches the ephemeral
// branch even after an earlier partial removal deregistered the worktree.
type worktreeRefRemover interface {
	RemoveRef(worktreePath, mainRepo, branch string) error
}

// cleanupChildResources removes the disposable child worktree and ephemeral
// branch once merge and closure are durable. It is idempotent: a cleared
// WorktreePath means the cleanup already succeeded, and the path stays
// recorded until both the worktree and the branch are confirmed gone so a
// retry can converge. The parent branch and the integrated commits are
// never touched.
func (o *Orchestrator) cleanupChildResources(child *feature.Feature) string {
	repo := child.Repos[0]
	if repo.WorktreePath == "" {
		return ""
	}
	if o.deps.Worktrees == nil {
		return "worktree cleanup is not configured"
	}
	var err error
	if rr, ok := o.deps.Worktrees.(worktreeRefRemover); ok {
		err = rr.RemoveRef(repo.WorktreePath, repo.Path, repo.Branch)
	} else {
		err = o.deps.Worktrees.Remove(repo.WorktreePath, true)
	}
	if err != nil {
		return fmt.Sprintf("removing child worktree %s: %v", repo.WorktreePath, err)
	}
	if err := o.deps.Store.Modify(child.ID, func(f *feature.Feature) error {
		for i := range f.Repos {
			f.Repos[i].WorktreePath = ""
		}
		return nil
	}); err != nil {
		return fmt.Sprintf("clearing child worktree path: %v", err)
	}
	return ""
}

// childHeadSHA reads the full HEAD of a worktree through the structural
// head-reader capability; absent capability fails closed.
func (o *Orchestrator) childHeadSHA(worktreePath string) (string, error) {
	reader, ok := o.deps.Worktrees.(childHeadReader)
	if !ok {
		return "", fmt.Errorf("child integration: exact head capture is not configured")
	}
	return reader.CurrentHeadSHA(worktreePath)
}
