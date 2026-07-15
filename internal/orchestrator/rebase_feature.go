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

// Package orchestrator wires feature-level rebase flows. StartFeatureRebase
// runs the harness across every repo first, then routes conflicts through one
// coordinated smart-rebase loop before final review and publish policy.
package orchestrator

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

var errFeatureRebaseStopped = errors.New("feature rebase stopped")

// StartFeatureRebase starts a feature-level rebase operation and runs the
// pre-agent harness asynchronously across every repo in the feature.
func (o *Orchestrator) StartFeatureRebase(featureID string) error {
	if o.deps.Lifecycle == nil {
		return errors.New("feature lifecycle not configured")
	}
	o.clearFeatureRebaseStopRequest(featureID)
	if err := o.deps.Lifecycle.StartFeatureRebaseOperation(featureID); err != nil {
		return fmt.Errorf("start feature rebase operation: %w", err)
	}
	o.cycleWG.Go(func() {
		o.runFeatureRebase(featureID)
	})
	return nil
}

func (o *Orchestrator) runFeatureRebase(featureID string) {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		_ = o.clearFeatureRebaseOperationIfContinuing(featureID)
		return
	}

	outcomes := make([]HarnessRebaseRepoOutcome, 0, len(f.Repos))
	for _, repo := range f.Repos {
		if ok, _ := o.updateFeatureRebaseRepoIfContinuing(
			featureID,
			repo.Name,
			feature.RebaseRepoStatusChecking,
			feature.RebaseRepoProgress{},
		); !ok {
			return
		}
		if ok, _ := o.updateFeatureRebaseRepoIfContinuing(
			featureID,
			repo.Name,
			feature.RebaseRepoStatusRebasing,
			feature.RebaseRepoProgress{},
		); !ok {
			return
		}

		outcome, ok := o.runHarnessRebaseRepoIfContinuing(featureID, f, repo)
		if !ok {
			return
		}
		progress := feature.RebaseRepoProgress{
			RebaseTarget:  outcome.RebaseTarget,
			ConflictFiles: append([]string(nil), outcome.ConflictFiles...),
			Changed:       outcome.Changed,
		}
		if outcome.Err != nil {
			progress.LastError = outcome.Err.Error()
		}
		if ok, err := o.updateFeatureRebaseRepoIfContinuing(featureID, repo.Name, outcome.Status, progress); !ok {
			return
		} else if err != nil && outcome.Err == nil {
			outcome.Status = feature.RebaseRepoStatusFailed
			outcome.Err = fmt.Errorf("update rebase progress: %w", err)
		}
		outcomes = append(outcomes, outcome)
	}

	if !o.shouldContinueFeatureRebase(featureID) {
		return
	}
	o.finishHarnessRebase(featureID, outcomes)
}

func (o *Orchestrator) finishHarnessRebase(featureID string, outcomes []HarnessRebaseRepoOutcome) {
	if !o.shouldContinueFeatureRebase(featureID) {
		return
	}

	failed := harnessRebaseOutcomesWithStatus(outcomes, feature.RebaseRepoStatusFailed)
	conflicted := harnessRebaseOutcomesWithStatus(outcomes, feature.RebaseRepoStatusConflict)
	if len(failed) > 0 {
		if err := o.runFeatureRebaseStateMutation(featureID, func() error {
			for _, outcome := range failed {
				cause := outcome.Err
				if cause == nil {
					cause = fmt.Errorf("rebase harness failed for repo %s", outcome.RepoName)
				}
				_ = o.RecordRebasePreflightFailure(featureID, outcome.RepoName, cause)
			}
			return nil
		}); errors.Is(err, errFeatureRebaseStopped) {
			return
		}
		if len(conflicted) > 0 {
			return
		}
		_ = o.clearFeatureRebaseOperationIfContinuing(featureID)
		return
	}

	if len(conflicted) > 0 {
		o.startCoordinatedSmartRebase(featureID, outcomes, conflicted)
		return
	}

	changed := harnessRebaseOutcomesWithStatus(outcomes, feature.RebaseRepoStatusChanged)
	if len(changed) > 0 {
		o.runRebaseFinalReviewAndPublishPolicy(featureID, changed)
		return
	}

	_ = o.clearFeatureRebaseOperationIfContinuing(featureID)
}

func harnessRebaseOutcomesWithStatus(outcomes []HarnessRebaseRepoOutcome, status feature.RebaseRepoStatus) []HarnessRebaseRepoOutcome {
	var filtered []HarnessRebaseRepoOutcome
	for _, outcome := range outcomes {
		if outcome.Status == status {
			filtered = append(filtered, outcome)
		}
	}
	return filtered
}

func (o *Orchestrator) runHarnessRebaseRepoIfContinuing(
	featureID string,
	f *feature.Feature,
	repo feature.FeatureRepo,
) (HarnessRebaseRepoOutcome, bool) {
	var outcome HarnessRebaseRepoOutcome
	if err := o.runFeatureRebaseExternalStep(featureID, func() error {
		outcome = o.runHarnessRebaseRepo(f, repo)
		return nil
	}); errors.Is(err, errFeatureRebaseStopped) {
		return HarnessRebaseRepoOutcome{}, false
	}
	return outcome, true
}

func (o *Orchestrator) updateFeatureRebaseRepoIfContinuing(
	featureID, repoName string,
	status feature.RebaseRepoStatus,
	progress feature.RebaseRepoProgress,
) (bool, error) {
	if err := o.runFeatureRebaseStateMutation(featureID, func() error {
		return o.deps.Lifecycle.UpdateFeatureRebaseRepo(featureID, repoName, status, progress)
	}); err != nil {
		if errors.Is(err, errFeatureRebaseStopped) {
			return false, nil
		}
		return true, err
	}
	return true, nil
}

func (o *Orchestrator) markFeatureRebaseStageIfContinuing(featureID string, stage feature.RebaseStage) (bool, error) {
	if err := o.runFeatureRebaseStateMutation(featureID, func() error {
		return o.deps.Lifecycle.MarkFeatureRebaseStage(featureID, stage)
	}); err != nil {
		if errors.Is(err, errFeatureRebaseStopped) {
			return false, nil
		}
		return true, err
	}
	return true, nil
}

func (o *Orchestrator) startCoordinatedSmartRebase(
	featureID string,
	outcomes []HarnessRebaseRepoOutcome,
	conflicted []HarnessRebaseRepoOutcome,
) {
	if !o.shouldContinueFeatureRebase(featureID) {
		return
	}
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		o.failChangedRebaseRepos(featureID, conflicted, fmt.Errorf("load feature: %w", err))
		_ = o.clearFeatureRebaseOperationIfContinuing(featureID)
		return
	}

	prURLs := f.PRURLs()
	targets := rebaseTargetsFromHarnessOutcomes(prURLs, conflicted)
	if ok, err := o.markFeatureRebaseStageIfContinuing(featureID, feature.RebaseStageSmartRebase); !ok {
		return
	} else if err != nil {
		o.failChangedRebaseRepos(featureID, conflicted, fmt.Errorf("mark smart rebase stage: %w", err))
		_ = o.clearFeatureRebaseOperationIfContinuing(featureID)
		return
	}

	cfg, err := o.rebaseLoopConfigForFeature(f, targets, false)
	if err != nil {
		o.failChangedRebaseRepos(featureID, conflicted, err)
		_ = o.clearFeatureRebaseOperationIfContinuing(featureID)
		return
	}
	beforeFingerprints := o.featureRebaseWorktreeFingerprints(f)

	var (
		result  *agent.RebaseLoopResult
		loopErr error
	)
	if !o.shouldContinueFeatureRebase(featureID) {
		return
	}
	runRebaseLoop := o.runRebaseLoopFn
	if runRebaseLoop == nil {
		runRebaseLoop = agent.RunRebaseLoop
	}
	result, loopErr = runRebaseLoop(cfg, o.deps.Sessions)
	if !o.shouldContinueFeatureRebase(featureID) {
		return
	}
	if loopErr != nil || result == nil || result.FinalStatus != reviewStatusPassed {
		o.failChangedRebaseRepos(featureID, conflicted, smartRebaseFailureCause(result, loopErr))
		_ = o.clearFeatureRebaseOperationIfContinuing(featureID)
		return
	}

	changed := harnessRebaseOutcomesWithStatus(outcomes, feature.RebaseRepoStatusChanged)
	for _, outcome := range conflicted {
		outcome.Status = feature.RebaseRepoStatusChanged
		outcome.Changed = true
		changed = append(changed, outcome)
	}
	contextChanged := o.smartRebaseContextChangedOutcomes(f, targets, changed, beforeFingerprints)
	for _, outcome := range contextChanged {
		ok, err := o.updateFeatureRebaseRepoIfContinuing(
			featureID,
			outcome.RepoName,
			feature.RebaseRepoStatusChanged,
			feature.RebaseRepoProgress{
				RebaseTarget: outcome.RebaseTarget,
				Changed:      true,
			},
		)
		if !ok {
			return
		}
		if err != nil {
			o.failChangedRebaseRepos(featureID, []HarnessRebaseRepoOutcome{outcome}, fmt.Errorf("mark context repo changed: %w", err))
			_ = o.clearFeatureRebaseOperationIfContinuing(featureID)
			return
		}
		changed = append(changed, outcome)
	}
	o.runRebaseFinalReviewAndPublishPolicy(featureID, changed)
}

func smartRebaseFailureCause(result *agent.RebaseLoopResult, loopErr error) error {
	if loopErr != nil {
		return loopErr
	}
	if result == nil {
		return errors.New("smart rebase failed without a result")
	}
	if result.LastError != "" {
		return errors.New(result.LastError)
	}
	if result.NeedUserInputPath != "" {
		return fmt.Errorf("smart rebase stopped for need_user_input: %s", result.NeedUserInputPath)
	}
	if result.FinalStatus != "" {
		return fmt.Errorf("smart rebase finished with status %s", result.FinalStatus)
	}
	return errors.New("smart rebase failed")
}

func (o *Orchestrator) featureRebaseWorktreeFingerprints(f *feature.Feature) map[string]string {
	out := make(map[string]string, len(f.Repos))
	for _, repo := range f.Repos {
		out[repo.Name] = o.rebaseWorktreeFingerprint(repo)
	}
	return out
}

func (o *Orchestrator) smartRebaseContextChangedOutcomes(
	f *feature.Feature,
	targets []agent.RebaseRepoTarget,
	alreadyChanged []HarnessRebaseRepoOutcome,
	before map[string]string,
) []HarnessRebaseRepoOutcome {
	if f == nil {
		return nil
	}
	targeted := make(map[string]bool, len(targets))
	for _, target := range targets {
		targeted[target.RepoName] = true
	}
	for _, outcome := range alreadyChanged {
		targeted[outcome.RepoName] = true
	}
	var changed []HarnessRebaseRepoOutcome
	for _, repo := range f.Repos {
		if targeted[repo.Name] {
			continue
		}
		beforeFingerprint, ok := before[repo.Name]
		if !ok {
			continue
		}
		if after := o.rebaseWorktreeFingerprint(repo); after != beforeFingerprint {
			outcome := o.harnessRebaseOutcomeForRepo(f, repo)
			outcome.Status = feature.RebaseRepoStatusChanged
			outcome.Changed = true
			changed = append(changed, outcome)
		}
	}
	return changed
}

// repoWorkDir returns repo's worktree path, falling back to its base path.
func repoWorkDir(repo feature.FeatureRepo) string {
	if repo.WorktreePath != "" {
		return repo.WorktreePath
	}
	return repo.Path
}

// repoBranch returns repo's branch, falling back to "feature/<slug>".
func repoBranch(f *feature.Feature, repo feature.FeatureRepo) string {
	if repo.Branch != "" {
		return repo.Branch
	}
	return "feature/" + f.Slug
}

func (o *Orchestrator) harnessRebaseOutcomeForRepo(f *feature.Feature, repo feature.FeatureRepo) HarnessRebaseRepoOutcome {
	return HarnessRebaseRepoOutcome{
		RepoName:     repo.Name,
		RebaseTarget: o.resolveRebaseTarget(f, &repo),
		Publishable:  repo.Publishable == nil || *repo.Publishable,
		WorktreePath: repoWorkDir(repo),
		Branch:       repoBranch(f, repo),
	}
}

func (o *Orchestrator) rebaseWorktreeFingerprint(repo feature.FeatureRepo) string {
	fn := o.worktreeFingerprintFn
	if fn == nil {
		fn = gitWorktreeFingerprint
	}
	fingerprint, err := fn(repoWorkDir(repo))
	if err != nil {
		return "error:" + err.Error()
	}
	return fingerprint
}

func gitWorktreeFingerprint(worktreePath string) (string, error) {
	if strings.TrimSpace(worktreePath) == "" {
		return "", errors.New("empty worktree path")
	}
	head, err := exec.Command("git", "-C", worktreePath, "rev-parse", "HEAD").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("rev-parse HEAD: %s: %w", strings.TrimSpace(string(head)), err)
	}
	status, err := exec.Command("git", "-C", worktreePath, "status", "--porcelain=v1").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git status: %s: %w", strings.TrimSpace(string(status)), err)
	}
	diff, err := exec.Command("git", "-C", worktreePath, "diff", "--binary", "HEAD", "--").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git diff: %s: %w", strings.TrimSpace(string(diff)), err)
	}
	return strings.TrimSpace(string(head)) + "\n" + string(status) + "\n" + string(diff), nil
}

func rebaseTargetsFromHarnessOutcomes(
	prURLs map[string]string,
	outcomes []HarnessRebaseRepoOutcome,
) []agent.RebaseRepoTarget {
	targets := make([]agent.RebaseRepoTarget, 0, len(outcomes))
	for _, outcome := range outcomes {
		targets = append(targets, agent.RebaseRepoTarget{
			RepoName:      outcome.RepoName,
			RebaseTarget:  outcome.RebaseTarget,
			ConflictFiles: append([]string(nil), outcome.ConflictFiles...),
			PRURL:         prURLs[outcome.RepoName],
		})
	}
	return targets
}

func featureRepoNamesInOrder(f *feature.Feature) []string {
	if f == nil {
		return nil
	}
	names := make([]string, 0, len(f.Repos))
	seen := map[string]bool{}
	for _, repo := range f.Repos {
		if repo.Name == "" || seen[repo.Name] {
			continue
		}
		seen[repo.Name] = true
		names = append(names, repo.Name)
	}
	return names
}

func (o *Orchestrator) rebaseLoopConfigForFeature(
	f *feature.Feature,
	behind []agent.RebaseRepoTarget,
	resumeExistingCycle bool,
) (agent.RebaseLoopConfig, error) {
	pr := o.deps.PhaseRunner
	if pr == nil {
		return agent.RebaseLoopConfig{}, errors.New("phase runner not configured")
	}
	return agent.RebaseLoopConfig{
		Feature:                    f,
		FeatureStore:               o.deps.Store,
		StateDir:                   o.stateDir(),
		BehindRepos:                behind,
		WorkspaceRepos:             featureRepoNamesInOrder(f),
		Model:                      f.Models.Implementation,
		ReviewModel:                f.Models.Review,
		MaxIterations:              f.MaxIterations,
		MaxConsecFails:             3,
		MaxConsecNoProgress:        3,
		KBInfos:                    o.computeKBInfos(f),
		DangerouslySkipPermissions: pr.DangerouslySkipPermissions,
		PermissionCache:            pr.PermissionCache,
		BuildSession:               pr.BuildSession,
		AskingClause:               pr.AskingClauseForModel(f.Models.Implementation),
		EffortLevel:                f.EffectivePipeline().EffortLevel(),
		SkillsDir:                  pr.SkillsDir,
		GuidelinesDir:              pr.GuidelinesDir,
		Observer:                   pr.Observer,
		CommandRunner:              pr.CommandRunner,
		SessionStartFunc:           o.featureRebaseSessionStartFunc(f),
		ResumeExistingCycle:        resumeExistingCycle,
	}, nil
}

func (o *Orchestrator) featureRebaseSessionStartFunc(f *feature.Feature) func(string, string, feature.Phase, []string, string, []string, ...*ports.SessionOpts) (ports.SessionHandle, error) {
	if f == nil || f.RebaseOperation == nil || o.deps.Sessions == nil {
		return nil
	}
	featureID := f.ID
	sm := o.deps.Sessions
	return func(
		id, featureIDArg string,
		phase feature.Phase,
		command []string,
		workdir string,
		env []string,
		opts ...*ports.SessionOpts,
	) (ports.SessionHandle, error) {
		var handle ports.SessionHandle
		err := o.runFeatureRebaseExternalStep(featureID, func() error {
			var startErr error
			handle, startErr = sm.StartSession(id, featureIDArg, phase, command, workdir, env, opts...)
			return startErr
		})
		if errors.Is(err, errFeatureRebaseStopped) {
			return nil, ports.ErrSessionShuttingDown
		}
		return handle, err
	}
}

func (o *Orchestrator) runRebaseFinalReviewAndPublishPolicy(
	featureID string,
	changed []HarnessRebaseRepoOutcome,
) {
	if !o.shouldContinueFeatureRebase(featureID) {
		return
	}
	if ok, err := o.markFeatureRebaseStageIfContinuing(featureID, feature.RebaseStageFinalReview); !ok {
		return
	} else if err != nil {
		if !o.shouldContinueFeatureRebase(featureID) {
			return
		}
		o.failChangedRebaseRepos(featureID, changed, fmt.Errorf("mark rebase final review stage: %w", err))
		_ = o.clearFeatureRebaseOperationIfContinuing(featureID)
		return
	}

	originalStatus, err := o.prepareRebaseFinalReview(featureID)
	if err != nil {
		if errors.Is(err, errFeatureRebaseStopped) {
			return
		}
		o.failChangedRebaseRepos(featureID, changed, fmt.Errorf("prepare rebase final review: %w", err))
		_ = o.clearFeatureRebaseOperationIfContinuing(featureID)
		return
	}

	if err := o.runDeferredFinalReviewForRebase(featureID); err != nil {
		if errors.Is(err, errFeatureRebaseStopped) || errors.Is(err, errFinalReviewInterrupted) {
			return
		}
		if errors.Is(err, errPlanRevisionDispatched) {
			if clearErr := o.clearFeatureRebaseOperationIfContinuing(featureID); clearErr != nil {
				o.failChangedRebaseRepos(featureID, changed, fmt.Errorf("clear rebase operation after plan revision dispatch: %w", clearErr))
			}
			return
		}
		o.failChangedRebaseRepos(featureID, changed, fmt.Errorf("rebase final review failed: %w", err))
		_ = o.clearFeatureRebaseOperationIfContinuing(featureID)
		return
	}

	if !o.shouldContinueFeatureRebase(featureID) {
		return
	}
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		o.failChangedRebaseRepos(featureID, changed, fmt.Errorf("reload after rebase final review: %w", err))
		_ = o.clearFeatureRebaseOperationIfContinuing(featureID)
		return
	}
	if !featureRebaseCanContinue(f) {
		return
	}

	autoPush := originalStatus == feature.StatusPublished && f.Checkpoints.AutoPublish()
	if autoPush && o.runRebaseAutoPushForcePush(featureID, changed) {
		return
	}

	if err := o.runFeatureRebaseStateMutation(featureID, func() error {
		return o.deps.Lifecycle.MarkCodeReady(featureID)
	}); errors.Is(err, errFeatureRebaseStopped) {
		return
	} else if err != nil {
		o.failChangedRebaseRepos(featureID, changed, fmt.Errorf("mark code ready after rebase final review: %w", err))
		_ = o.clearFeatureRebaseOperationIfContinuing(featureID)
		return
	}
	if autoPush {
		if err := o.runFeatureRebaseStateMutation(featureID, func() error {
			return o.deps.Lifecycle.ReturnToPublished(featureID)
		}); errors.Is(err, errFeatureRebaseStopped) {
			return
		} else if err != nil {
			o.failChangedRebaseRepos(featureID, changed, fmt.Errorf("return to published after rebase push: %w", err))
			_ = o.clearFeatureRebaseOperationIfContinuing(featureID)
			return
		}
	}
	if err := o.runFeatureRebaseStateMutation(featureID, func() error {
		return o.clearRebaseSuccessState(featureID, changed)
	}); errors.Is(err, errFeatureRebaseStopped) {
		return
	} else if err != nil {
		o.failChangedRebaseRepos(featureID, changed, err)
		_ = o.clearFeatureRebaseOperationIfContinuing(featureID)
	}
}

// runRebaseAutoPushForcePush force-pushes each publishable rebased repo when
// auto-push applies. It handles its own failure bookkeeping; the caller
// should return immediately when stop is true.
func (o *Orchestrator) runRebaseAutoPushForcePush(featureID string, changed []HarnessRebaseRepoOutcome) (stop bool) {
	if o.deps.Rebaser == nil {
		o.failChangedRebaseRepos(featureID, changed, errors.New("rebase operator not configured for force push"))
		_ = o.clearFeatureRebaseOperationIfContinuing(featureID)
		return true
	}
	if o.deps.Publisher == nil {
		o.failChangedRebaseRepos(featureID, changed, errors.New("publisher not configured for committing rebased repos"))
		_ = o.clearFeatureRebaseOperationIfContinuing(featureID)
		return true
	}
	for _, outcome := range changed {
		if !outcome.Publishable {
			continue
		}
		err := o.runFeatureRebaseExternalStep(featureID, func() error {
			if err := o.commitRebaseOutcomeIfDirty(outcome); err != nil {
				return err
			}
			return o.deps.Rebaser.ForcePush(outcome.WorktreePath, outcome.Branch)
		})
		switch {
		case errors.Is(err, errFeatureRebaseStopped):
			return true
		case err != nil:
			o.failChangedRebaseRepos(featureID, []HarnessRebaseRepoOutcome{outcome},
				fmt.Errorf("force push rebased repo %s: %w", outcome.RepoName, err))
			_ = o.clearFeatureRebaseOperationIfContinuing(featureID)
			return true
		}
	}
	return false
}

func (o *Orchestrator) commitRebaseOutcomeIfDirty(outcome HarnessRebaseRepoOutcome) error {
	hasChanges, err := o.deps.Publisher.HasUncommittedChanges(outcome.WorktreePath)
	if err != nil {
		return fmt.Errorf("check uncommitted rebased repo %s: %w", outcome.RepoName, err)
	}
	if !hasChanges {
		return nil
	}
	if err := o.deps.Publisher.CommitAll(outcome.WorktreePath, "Resolve rebase changes"); err != nil {
		return fmt.Errorf("commit rebased repo %s: %w", outcome.RepoName, err)
	}
	return nil
}

func (o *Orchestrator) runDeferredFinalReviewForRebase(featureID string) error {
	var (
		resultCh chan *agent.OrchestratorResult
		err      error
	)

	err = o.runFeatureRebaseStateMutation(featureID, func() error {
		resultCh, err = o.startDeferredFinalReview(featureID)
		return err
	})
	if err != nil {
		return err
	}

	res, ok := <-resultCh
	return o.runFeatureRebaseStateMutation(featureID, func() error {
		return o.finishDeferredFinalReviewResult(featureID, res, ok)
	})
}

func (o *Orchestrator) shouldContinueFeatureRebase(featureID string) bool {
	if o.featureRebaseStopRequested(featureID) {
		return false
	}
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil || f == nil {
		return false
	}
	return featureRebaseCanContinue(f)
}

func (o *Orchestrator) featureRebaseStopRequested(featureID string) bool {
	rebaseControl := o.featureRebaseControl(featureID)
	rebaseControl.mu.Lock()
	defer rebaseControl.mu.Unlock()
	return rebaseControl.stopping
}

func (o *Orchestrator) clearFeatureRebaseStopRequest(featureID string) {
	rebaseControl := o.featureRebaseControl(featureID)
	rebaseControl.mu.Lock()
	defer rebaseControl.mu.Unlock()
	rebaseControl.stopping = false
}

func (o *Orchestrator) clearFeatureRebaseOperationIfContinuing(featureID string) error {
	return o.runFeatureRebaseStateMutation(featureID, func() error {
		return o.deps.Lifecycle.ClearFeatureRebaseOperation(featureID)
	})
}

func (o *Orchestrator) runFeatureRebaseStateMutation(featureID string, fn func() error) error {
	rebaseControl := o.featureRebaseControl(featureID)
	rebaseControl.mu.Lock()
	defer rebaseControl.mu.Unlock()

	if rebaseControl.stopping || !o.featureRebaseStateCanContinue(featureID) {
		return errFeatureRebaseStopped
	}
	return fn()
}

func (o *Orchestrator) runFeatureRebaseExternalStep(featureID string, fn func() error) error {
	rebaseControl := o.featureRebaseControl(featureID)
	rebaseControl.mu.Lock()
	if rebaseControl.stopping || !o.featureRebaseStateCanContinue(featureID) {
		rebaseControl.mu.Unlock()
		return errFeatureRebaseStopped
	}
	rebaseControl.active++
	rebaseControl.mu.Unlock()

	defer func() {
		rebaseControl.mu.Lock()
		rebaseControl.active--
		if rebaseControl.active == 0 {
			rebaseControl.cond.Broadcast()
		}
		rebaseControl.mu.Unlock()
	}()
	return fn()
}

func (o *Orchestrator) featureRebaseStateCanContinue(featureID string) bool {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil || f == nil {
		return false
	}
	return featureRebaseCanContinue(f)
}

func featureRebaseCanContinue(f *feature.Feature) bool {
	if f == nil || f.Status == feature.StatusInterrupted || f.RebaseOperation == nil {
		return false
	}
	if f.ActiveCycle != nil && featureRebaseActiveCycleType(f) == feature.CycleRebase &&
		f.ActiveCycle.Status == feature.RepoCycleInterrupted {
		return false
	}
	return true
}

func featureRebaseActiveCycleType(f *feature.Feature) feature.RepoCycleType {
	if f == nil || f.ActiveCycle == nil {
		return ""
	}
	if f.ActiveCycle.Type != "" {
		return f.ActiveCycle.Type
	}
	return f.ActiveCycleType()
}

func markInterruptedRebaseActiveCycle(f *feature.Feature) {
	if f == nil || f.ActiveCycle == nil || featureRebaseActiveCycleType(f) != feature.CycleRebase {
		return
	}
	f.ActiveCycle.Status = feature.RepoCycleInterrupted
	f.ActiveCycle.LastError = ""
}

func (o *Orchestrator) prepareRebaseFinalReview(featureID string) (feature.Status, error) {
	if o.deps.Store == nil {
		return feature.StatusCreated, errors.New("feature store not configured")
	}
	var originalStatus feature.Status
	if err := o.runFeatureRebaseStateMutation(featureID, func() error {
		return o.deps.Store.Modify(featureID, func(f *feature.Feature) error {
			if !featureRebaseCanContinue(f) {
				markInterruptedRebaseActiveCycle(f)
				return errFeatureRebaseStopped
			}
			originalStatus = f.Status
			f.Status = feature.StatusReviewPassed
			f.CurrentPhase = feature.PhaseImplement
			return nil
		})
	}); err != nil {
		return feature.StatusCreated, err
	}
	return originalStatus, nil
}

func (o *Orchestrator) failChangedRebaseRepos(
	featureID string,
	changed []HarnessRebaseRepoOutcome,
	cause error,
) {
	if cause == nil {
		cause = errors.New("rebase failed")
	}
	errMsg := cause.Error()
	_ = o.runFeatureRebaseStateMutation(featureID, func() error {
		for _, outcome := range changed {
			_ = o.deps.Lifecycle.UpdateFeatureRebaseRepo(
				featureID,
				outcome.RepoName,
				feature.RebaseRepoStatusFailed,
				feature.RebaseRepoProgress{
					RebaseTarget:  outcome.RebaseTarget,
					ConflictFiles: append([]string(nil), outcome.ConflictFiles...),
					LastError:     errMsg,
					Changed:       outcome.Changed,
				},
			)
			_ = o.deps.Lifecycle.FailRepoImplementation(featureID, outcome.RepoName, errMsg)
		}
		return nil
	})
}

func (o *Orchestrator) clearRebaseSuccessState(
	featureID string,
	changed []HarnessRebaseRepoOutcome,
) error {
	if o.deps.Store != nil {
		if err := o.deps.Store.Modify(featureID, func(f *feature.Feature) error {
			if f.RepoStates == nil {
				f.RepoStates = map[string]*feature.RepoState{}
			}
			for _, outcome := range changed {
				state := f.RepoStates[outcome.RepoName]
				if state == nil {
					state = &feature.RepoState{}
					f.RepoStates[outcome.RepoName] = state
				}
				state.Touched = true
				state.LastError = ""
			}
			return nil
		}); err != nil {
			return fmt.Errorf("clear successful rebase repo errors: %w", err)
		}
	}
	if err := o.deps.Lifecycle.ClearFeatureRebaseOperation(featureID); err != nil {
		return fmt.Errorf("clear successful rebase operation: %w", err)
	}
	return nil
}

// handleFeatureCycleDone implements the routing shared by
// handleFeatureRefactorDone and handleFeatureReviewCommentsDone: the unified
// review-comments/refactor loops share the
// same FinalStatus vocabulary and non-success handling. The caller reduces
// its concrete *XLoopResult to the primitive fields below; errPrefix labels
// failure messages (e.g. "review-comments"). onPassed/onNeedUserInput/onFailure carry
// the logic that genuinely differs per cycle type (success finalisation,
// which repos get a need-user-input gate and how, extra cleanup on
// failure); onNeedUserInput always receives a gate with Iterations set.
func (o *Orchestrator) handleFeatureCycleDone(
	featureID string,
	repoNames []string,
	errPrefix string,
	dispatchFailed bool,
	dispatchErr error,
	finalStatus string,
	iterations int,
	lastError string,
	needUserInputPath string,
	onPassed func(),
	onNeedUserInput func(gate *agent.LoopResult),
	onFailure func(),
) {
	if dispatchFailed {
		errMsg := errPrefix + ": dispatch failed"
		if dispatchErr != nil {
			errMsg = errPrefix + ": " + dispatchErr.Error()
		}
		for _, name := range repoNames {
			_ = o.deps.Lifecycle.FailRepoCycle(featureID, name, errMsg)
		}
		if onFailure != nil {
			onFailure()
		}
		return
	}

	switch finalStatus {
	case reviewStatusPassed:
		onPassed()

	case finalStatusInterrupted, finalStatusNoOp:
		// Interrupted: persisted state preserved for restart. No-op:
		// nothing to do. Either way, leave the per-repo cycle entries
		// in place; the harness recovery / next user action handles
		// them.

	case finalStatusNeedUserInput:
		onNeedUserInput(&agent.LoopResult{
			FinalStatus:       finalStatusNeedUserInput,
			Iterations:        iterations,
			LastError:         lastError,
			NeedUserInputPath: needUserInputPath,
		})

	default:
		// max_iterations / safety_rail / failed: surface error per repo
		// so the TUI can present the failed cycle.
		errMsg := errPrefix + ": " + finalStatus
		if lastError != "" {
			errMsg = errPrefix + ": " + lastError
		}
		for _, name := range repoNames {
			_ = o.deps.Lifecycle.FailRepoCycle(featureID, name, errMsg)
		}
		if onFailure != nil {
			onFailure()
		}
	}
}
