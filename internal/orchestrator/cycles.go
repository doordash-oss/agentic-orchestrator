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

// Package orchestrator owns post-publish tweak/rebase/review-comments/refactor
// cycle lifecycle methods for multi-repo features.
package orchestrator

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// RepoCycleLoopResultInput carries the result of an agent implementation loop
// for a per-repo cycle (tweak / rebase / review-comments / refactor).
type RepoCycleLoopResultInput struct {
	RepoName string
	Result   *agent.LoopResult
	Err      error
}

// StartRepoCycleImplement launches an implementation loop for a per-repo
// post-publish cycle (rebase / tweak / review-comments). The feature stays
// StatusPublished; cycle state is tracked in RepoCycles. planContent is the
// pre-computed plan body: for CycleRebase/CycleTweak it is the "extra" text
// merged into BuildRebasePlan/BuildTweakPlan templates; for
// CycleReviewComments it is the final plan markdown written verbatim. For
// CycleRebase, conflictFiles (when non-empty) selects the
// "rebase-already-in-progress" template in BuildRebasePlan so the agent
// resumes the existing rebase instead of starting one from scratch.
//
// CycleRebase is routed to feature-level RunRebaseLoop: the per-repo entry
// is preserved as the API for callers (TUI), but the loop expands to every
// behind Feature.Repos branch and stamps them atomically. The repoName
// argument is used as a hint to scope conflict files to the right repo; the
// loop autonomously discovers any other behind branches via the Rebaser port.
//
// Ports startRepoCycleImplementCmd (app.go:6659-6784).
func (o *Orchestrator) StartRepoCycleImplement(
	featureID, repoName string,
	cycleType feature.RepoCycleType,
	planContent string,
	conflictFiles ...string,
) (string, error) {
	if cycleType == feature.CycleRebase {
		// The repoName + conflictFiles from the TUI's per-repo conflict callback
		// are folded in via the behind-subset enumerator; planContent is the
		// rebase target ref the TUI resolved before dispatching.
		return o.startFeatureRebase(featureID, repoName, planContent, conflictFiles)
	}

	if cycleType == feature.CycleReviewComments {
		// The repoName from the TUI is purely a hint — the loop aggregates
		// unaddressed comments across every Feature.Repos PR. planContent is
		// ignored; the loop builds the aggregated plan from the per-repo
		// `comments.json` artifacts the TUI already saved before dispatch.
		_ = planContent
		return o.startFeatureReviewComments(featureID, repoName)
	}

	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return "", fmt.Errorf("load feature: %w", err)
	}

	// Find the repo
	var repo *feature.FeatureRepo
	for i := range f.Repos {
		if f.Repos[i].Name == repoName {
			repo = &f.Repos[i]
			break
		}
	}
	if repo == nil {
		return "", fmt.Errorf("repo %q not found in feature", repoName)
	}

	baseDir := o.stateDir()
	workDir := repo.WorktreePath
	if workDir == "" {
		workDir = repo.Path
	}

	// Start the per-repo cycle FIRST to set the count for enumerated dir names
	if err := o.deps.Lifecycle.StartRepoCycle(featureID, repoName, cycleType); err != nil {
		return "", fmt.Errorf("start cycle: %w", err)
	}

	// Re-read to get the cycle count
	f, err = o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return "", fmt.Errorf("reload feature: %w", err)
	}
	var cycleCount int
	if rc, ok := f.RepoCycles[repoName]; ok {
		cycleCount = rc.Count
	}
	cycleDirName := feature.RepoCycleDirName(cycleType, cycleCount)
	cycleBaseDir := filepath.Join(agent.ActiveRunDir(baseDir, f), cycleDirName, repoName)

	// Build plan in the enumerated cycle directory (e.g. tweak-1/<repoName>/).
	// Note: CycleRebase is intercepted at the top of this method and routed
	// to the unified feature-level loop; only Tweak and ReviewComments still
	// use the per-repo cycle subdir under slices 5-7.
	var planDir, planPath string
	switch cycleType {
	case feature.CycleTweak:
		planDir = cycleBaseDir
		_ = os.MkdirAll(planDir, 0o755)
		planPath = filepath.Join(planDir, "tweak-plan.md")
		body := fmt.Sprintf("# Tweak: %s\n\n%s\n", repoName, planContent)
		_ = os.WriteFile(planPath, []byte(body), 0o644)
	case feature.CycleReviewComments:
		planDir = cycleBaseDir
		_ = os.MkdirAll(planDir, 0o755)
		planPath = filepath.Join(planDir, "review-plan.md")
		// planContent is the full plan content for review comments
		_ = os.WriteFile(planPath, []byte(planContent), 0o644)
	}
	_ = o.deps.Lifecycle.SetRepoCyclePlanPath(featureID, repoName, planPath)

	// Build KBInfos for the repo
	kbInfos := o.computeKBInfos(f)

	// Build artifact dir for this cycle (same as plan dir)
	artifactDir := cycleBaseDir
	_ = os.MkdirAll(artifactDir, 0o755)

	pr := o.deps.PhaseRunner
	if pr == nil {
		return "", errors.New("phase runner not configured")
	}

	cfg := agent.ImplementConfig{
		Feature:                    f,
		FeatureStore:               o.deps.Store,
		WorkDir:                    workDir,
		PlanPath:                   planPath,
		KBInfos:                    kbInfos,
		MaxIterations:              f.MaxIterations,
		MaxConsecFails:             3,
		MaxConsecNoProgress:        3,
		ExitCriteria:               f.ExitCriteria,
		Model:                      f.Models.Implementation,
		ReviewModel:                f.Models.Review,
		ArtifactDir:                artifactDir,
		StateDir:                   filepath.Join(baseDir, featureID),
		RepoName:                   repoName,
		DesignArtifactPath:         f.DesignArtifactPath(),
		DangerouslySkipPermissions: pr.DangerouslySkipPermissions,
		PermissionCache:            pr.PermissionCache,
		BuildSession:               pr.BuildSession,
		AskingClause:               pr.AskingClauseForModel(f.Models.Implementation),
		EffortLevel:                f.EffectivePipeline().EffortLevel(),
		SkillsDir:                  pr.SkillsDir,
		GuidelinesDir:              pr.GuidelinesDir,
		SkipIterationReview:        f.EffectivePipeline().ShouldSkipIterationReview(),
		Observer:                   pr.Observer,
	}

	sm := o.deps.Sessions
	o.cycleWG.Go(func() {
		result, loopErr := agent.RunImplementationLoop(cfg, sm)
		if loopErr != nil {
			if result == nil {
				result = &agent.LoopResult{}
			}
			result.FinalStatus = "error"
			result.LastError = loopErr.Error()
		}
		_ = o.HandleRepoCycleLoopDone(featureID, RepoCycleLoopResultInput{
			RepoName: repoName,
			Result:   result,
		})
	})

	// No stable session ID for iteration-based loops; the loop owns the
	// per-iteration IDs via BuildSession.
	return "", nil
}

// HandleRepoCycleLoopDone processes the completion of a per-repo cycle
// implementation. On NEED_USER_INPUT routes through the cycle gate entry so
// the cycle pauses on its persisted artifact instead of failing. On failure
// calls FailRepoCycle; on success routes through per-repo Final Review
// before cycle completion.
//
// Ports handleRepoCycleLoopDone (app.go:6787-6801).
func (o *Orchestrator) HandleRepoCycleLoopDone(
	featureID string,
	input RepoCycleLoopResultInput,
) error {
	if input.Result != nil && input.Result.FinalStatus == "need_user_input" {
		cycleType := cycleTypeForRepo(o, featureID, input.RepoName)
		return o.onRepoCycleNeedUserInput(featureID, input.RepoName, cycleType, input.Result)
	}
	if input.Result == nil || input.Result.FinalStatus != "review_passed" {
		errMsg := "cycle failed"
		if input.Result != nil && input.Result.LastError != "" {
			errMsg = input.Result.LastError
		} else if input.Err != nil {
			errMsg = input.Err.Error()
		}
		if err := o.deps.Lifecycle.FailRepoCycle(featureID, input.RepoName, errMsg); err != nil {
			return fmt.Errorf("fail repo cycle: %w", err)
		}
		return nil
	}

	// Route through the feature-level Final Review before cycle completion.
	// Every Feature.Repos entry is reviewed atomically, just like engine Final
	// Review. The repoName from the loop result is preserved on the orchestrator
	// API surface but the FR loop itself ignores it.
	_ = input.RepoName
	return o.StartCycleFinalReview(featureID)
}

// cycleTypeForRepo returns the persisted cycle type for the given repo, or
// the empty string when the cycle entry cannot be loaded. Used by gate-entry
// routing so the LoopResult's caller does not have to track cycle type
// separately from the persisted RepoCycleState.
func cycleTypeForRepo(o *Orchestrator, featureID, repoName string) feature.RepoCycleType {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil || f == nil {
		return ""
	}
	rc, ok := f.RepoCycles[repoName]
	if !ok || rc == nil {
		return ""
	}
	return rc.Type
}

// restartPausedRepoCycle dispatches a paused cycle to its cycle-type-specific
// restart seam. The cycle entry's Type is the source of truth; UI hints in
// the decision record are diagnostic only. Loop-based cycles (rebase /
// review-comments) re-enter through restartRepoCycleImplement; refactor
// re-enters through restartRefactorRepoCycle (the gate-only sibling of
// RestartRefactorCycle that bypasses the concurrent-refactor guard and the
// StartRepoCycle re-init so the existing entry is reused in place). Cycle
// Count and PlanPath survive the round-trip on every type. Tweak cycles
// cannot enter this path: tweak is fully interactive and never emits a
// NEED_USER_INPUT gate, so a paused tweak entry is a programming error.
func (o *Orchestrator) restartPausedRepoCycle(featureID, repoName string) error {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return fmt.Errorf("load feature: %w", err)
	}
	rc, ok := f.RepoCycles[repoName]
	if !ok || rc == nil {
		return fmt.Errorf("no cycle entry for repo %q", repoName)
	}
	switch rc.Type {
	case feature.CycleRebase:
		return o.restartPausedFeatureRebase(featureID, repoName)
	case feature.CycleReviewComments:
		_, err := o.restartRepoCycleImplement(featureID, repoName, rc)
		return err
	case feature.CycleRefactor:
		return o.restartRefactorRepoCycle(featureID, repoName, rc)
	default:
		return fmt.Errorf("unsupported cycle type %q for repo %q", rc.Type, repoName)
	}
}

// restartPausedFeatureRebase re-launches the unified rebase loop after an
// answered NEED_USER_INPUT gate. Unlike legacy per-repo cycle restarts, rebase
// owns every behind repo in one flat artifact directory, so resume must route
// through RunRebaseLoop with ResumeExistingCycle rather than the generic
// per-repo RunImplementationLoop wrapper.
func (o *Orchestrator) restartPausedFeatureRebase(featureID, repoName string) error {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return fmt.Errorf("load feature: %w", err)
	}
	behind, err := o.activeRebaseTargets(f)
	if err != nil {
		return err
	}
	if len(behind) == 0 {
		return fmt.Errorf("no active rebase cycles to resume for repo %q", repoName)
	}

	cfg, err := o.rebaseLoopConfigForFeature(f, behind, true)
	if err != nil {
		return err
	}

	sm := o.deps.Sessions
	o.cycleWG.Go(func() {
		runRebaseLoop := o.runRebaseLoopFn
		if runRebaseLoop == nil {
			runRebaseLoop = agent.RunRebaseLoop
		}
		result, loopErr := runRebaseLoop(cfg, sm)
		o.handleFeatureRebaseDone(featureID, behind, result, loopErr)
	})
	return nil
}

func (o *Orchestrator) activeRebaseTargets(f *feature.Feature) ([]agent.RebaseRepoTarget, error) {
	if f == nil {
		return nil, errors.New("feature is nil")
	}
	prURLs := f.PRURLs()
	var out []agent.RebaseRepoTarget
	for i := range f.Repos {
		repo := &f.Repos[i]
		rc, ok := f.RepoCycles[repo.Name]
		if !ok || rc == nil || rc.Type != feature.CycleRebase {
			continue
		}
		switch rc.Status {
		case feature.RepoCycleRunning, feature.RepoCycleNeedUserInput:
		default:
			continue
		}
		target := o.resolveRebaseTarget(f, repo)
		if target == "" {
			return nil, fmt.Errorf("resolve rebase target for repo %q", repo.Name)
		}
		out = append(out, agent.RebaseRepoTarget{
			RepoName:     repo.Name,
			RebaseTarget: target,
			PRURL:        prURLs[repo.Name],
		})
	}
	return out, nil
}

// restartRefactorRepoCycle re-launches a refactor loop for a paused refactor
// cycle. Refactor is feature-level; the gate-only restart path resolves the
// persisted prompt and re-dispatches via startFeatureRefactor. The legacy
// "preserve Count / PlanPath" guard is unnecessary because the flat artifact
// layout (no per-repo subdir) and AtomicPhaseStamp staged-subset semantics make
// a fresh dir on every retry the simpler invariant.
func (o *Orchestrator) restartRefactorRepoCycle(featureID, repoName string, rc *feature.RepoCycleState) error {
	if rc == nil {
		return fmt.Errorf("nil cycle state for repo %q", repoName)
	}
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return fmt.Errorf("load feature: %w", err)
	}
	if f.RefactorPrompt == "" && rc.PlanPath != "" {
		if data, readErr := os.ReadFile(rc.PlanPath); readErr == nil {
			if err := o.deps.Store.Modify(featureID, func(ff *feature.Feature) error {
				ff.RefactorPrompt = string(data)
				return nil
			}); err != nil {
				return fmt.Errorf("restore refactor prompt: %w", err)
			}
			f, err = o.deps.Lifecycle.Get(featureID)
			if err != nil {
				return fmt.Errorf("reload feature: %w", err)
			}
		}
	}
	if f.RefactorPrompt == "" {
		return fmt.Errorf("no refactor prompt available for paused cycle on repo %q", repoName)
	}

	_, err = o.startFeatureRefactor(featureID, repoName, f.RefactorPrompt, RefactorEvidence{})
	return err
}

// restartRepoCycleImplement re-launches an autonomous cycle implementation
// loop for a paused rebase / review-comments cycle. Reuses the persisted
// PlanPath and Count rather than calling StartRepoCycle (which would
// increment the count and enumerate a new directory). Returns the session
// id (always "" for loop-based cycles) and any launch error.
func (o *Orchestrator) restartRepoCycleImplement(featureID, repoName string, rc *feature.RepoCycleState) (string, error) {
	if rc == nil {
		return "", fmt.Errorf("nil cycle state for repo %q", repoName)
	}
	planPath := rc.PlanPath
	if planPath == "" {
		return "", fmt.Errorf("no plan path on paused cycle for repo %q", repoName)
	}
	planContent, err := os.ReadFile(planPath)
	if err != nil {
		return "", fmt.Errorf("read paused cycle plan: %w", err)
	}

	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return "", fmt.Errorf("load feature: %w", err)
	}
	var repo *feature.FeatureRepo
	for i := range f.Repos {
		if f.Repos[i].Name == repoName {
			repo = &f.Repos[i]
			break
		}
	}
	if repo == nil {
		return "", fmt.Errorf("repo %q not found in feature", repoName)
	}

	baseDir := o.stateDir()
	workDir := repo.WorktreePath
	if workDir == "" {
		workDir = repo.Path
	}

	cycleDirName := feature.RepoCycleDirName(rc.Type, rc.Count)
	cycleBaseDir := filepath.Join(agent.ActiveRunDir(baseDir, f), cycleDirName, repoName)
	_ = os.MkdirAll(cycleBaseDir, 0o755)

	pr := o.deps.PhaseRunner
	if pr == nil {
		return "", errors.New("phase runner not configured")
	}

	kbInfos := o.computeKBInfos(f)
	cfg := agent.ImplementConfig{
		Feature:                    f,
		FeatureStore:               o.deps.Store,
		WorkDir:                    workDir,
		PlanPath:                   planPath,
		KBInfos:                    kbInfos,
		MaxIterations:              f.MaxIterations,
		MaxConsecFails:             3,
		MaxConsecNoProgress:        3,
		ExitCriteria:               f.ExitCriteria,
		Model:                      f.Models.Implementation,
		ReviewModel:                f.Models.Review,
		ArtifactDir:                cycleBaseDir,
		StateDir:                   filepath.Join(baseDir, featureID),
		RepoName:                   repoName,
		DesignArtifactPath:         f.DesignArtifactPath(),
		DangerouslySkipPermissions: pr.DangerouslySkipPermissions,
		PermissionCache:            pr.PermissionCache,
		BuildSession:               pr.BuildSession,
		AskingClause:               pr.AskingClauseForModel(f.Models.Implementation),
		EffortLevel:                f.EffectivePipeline().EffortLevel(),
		SkillsDir:                  pr.SkillsDir,
		GuidelinesDir:              pr.GuidelinesDir,
		SkipIterationReview:        f.EffectivePipeline().ShouldSkipIterationReview(),
		Observer:                   pr.Observer,
	}

	_ = planContent // included in cfg.PlanPath; explicit silencer to keep imports tidy.
	sm := o.deps.Sessions
	go func() {
		result, loopErr := agent.RunImplementationLoop(cfg, sm)
		if loopErr != nil {
			if result == nil {
				result = &agent.LoopResult{}
			}
			result.FinalStatus = "error"
			result.LastError = loopErr.Error()
		}
		_ = o.HandleRepoCycleLoopDone(featureID, RepoCycleLoopResultInput{
			RepoName: repoName,
			Result:   result,
		})
	}()
	return "", nil
}

// StartCycleFinalReview launches a feature-level Final Review for a
// post-publish cycle. Cwd at the feature state dir; --add-dir for every
// Feature.Repos worktree. The reviewer reads the cumulative diff across
// all repos and emits one APPROVED / CHANGES_REQUESTED verdict.
//
// On approval: dispatches per-repo CompleteRepoCycle for every Feature.Repos
// (mirroring the legacy per-repo Final Review's success path). On failure:
// calls FailRepoCycle for every Feature.Repos with an active cycle entry.
//
// Used by the post-tweak modal "y" path. Other callers (legacy
// HandleRepoCycleLoopDone fallthrough for non-rebase/non-review-comments
// cycles) are dead post-slices-4-7 but preserved for safety.
func (o *Orchestrator) StartCycleFinalReview(featureID string) error {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return o.failCycleAcrossRepos(featureID, "load feature: "+err.Error())
	}

	// Mark every active cycle entry as reviewing so the TUI surfaces the
	// FR phase. Mirrors the legacy per-repo MarkRepoCycleReviewing call
	// for every Feature.Repos that has an active cycle.
	for _, repo := range f.Repos {
		if rc, ok := f.RepoCycles[repo.Name]; ok && rc != nil {
			_ = o.deps.Lifecycle.MarkRepoCycleReviewing(featureID, repo.Name)
		}
	}

	pr := o.deps.PhaseRunner
	if pr == nil {
		return o.failCycleAcrossRepos(featureID, "phase runner not configured")
	}

	resultCh, err := pr.RunFeatureCycleFinalReview(f)
	if err != nil {
		return o.failCycleAcrossRepos(featureID, "dispatch final review: "+err.Error())
	}

	o.cycleWG.Go(func() {
		result := <-resultCh
		_ = o.handleCycleFinalReviewDone(featureID, result)
	})

	return nil
}

// handleCycleFinalReviewDone processes the completion of a feature-level
// post-cycle Final Review.
//
// On review_passed: routes by the cycle's type. For CycleTweak the handler
// emits a SINGLE TweakReviewApproved event so the TUI dispatches one
// feature-level completeTweakFinishCmd (commit + pull-rebase + push across
// every Feature.Repos). Other cycle types iterate per-repo CompleteRepoCycle
// for backward compatibility with the legacy fallthrough path (rebase /
// review-comments / refactor are routed via their unified loops post
// slices-4-7, so this branch is rarely exercised in production).
//
// On failure: marks every active cycle entry failed.
func (o *Orchestrator) handleCycleFinalReviewDone(
	featureID string,
	result *agent.FeatureFinalReviewResult,
) error {
	if result == nil || result.FinalStatus != "review_passed" {
		errMsg := "cycle review failed"
		if result != nil && result.LastError != "" {
			errMsg = result.LastError
		}
		return o.failCycleAcrossRepos(featureID, errMsg)
	}

	// Review approved — route by the active cycle type. Tweak emits a
	// single feature-level TweakReviewApproved event; the legacy
	// per-repo-cycle path (rebase / review-comments / refactor) iterates
	// CompleteRepoCycle for backward compatibility, but the unified
	// loops own those paths post slices-4-7.
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return fmt.Errorf("load feature post-cycle FR: %w", err)
	}

	cycleType := dominantCycleTypeForFeature(f)
	if cycleType == feature.CycleTweak {
		// Single event — TUI's OrchTweakReviewApprovedMsg handler dispatches
		// a single feature-level completeTweakFinishCmd that commits +
		// pull-rebases + pushes every Feature.Repos atomically.
		o.emitEvent(ports.Event{
			Type:      ports.TweakReviewApproved,
			FeatureID: featureID,
		})
		return nil
	}

	// Compatibility branch for legacy callers that still complete non-tweak
	// cycles through HandleRepoCycleLoopDone.
	for _, repo := range f.Repos {
		rc, ok := f.RepoCycles[repo.Name]
		if !ok || rc == nil {
			continue
		}
		if err := o.CompleteRepoCycle(featureID, repo.Name); err != nil {
			return err
		}
	}
	return nil
}

// dominantCycleTypeForFeature returns the cycle type the feature is
// currently in, sourced from Feature.ActiveCycleType when set, else from
// the first per-repo cycle entry. Returns "" when no cycle is active.
func dominantCycleTypeForFeature(f *feature.Feature) feature.RepoCycleType {
	if f == nil {
		return ""
	}
	if t := f.ActiveCycleType(); t != "" {
		return t
	}
	for _, rc := range f.RepoCycles {
		if rc != nil && rc.Type != "" {
			return rc.Type
		}
	}
	return ""
}

// failCycleAcrossRepos marks every Feature.Repos cycle entry failed with
// the provided error message. Used when the feature-level FR cannot
// dispatch (load feature, missing phase runner, etc.) so the legacy
// per-repo TUI surface (RepoCycles[name].LastError != "") clears.
func (o *Orchestrator) failCycleAcrossRepos(featureID, errMsg string) error {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return fmt.Errorf("load feature: %w", err)
	}
	for _, repo := range f.Repos {
		if rc, ok := f.RepoCycles[repo.Name]; ok && rc != nil {
			_ = o.deps.Lifecycle.FailRepoCycle(featureID, repo.Name, errMsg)
		}
	}
	return nil
}

// CompleteRepoCycle finalizes a per-repo post-publish cycle by dispatching
// to the cycle-type-specific finalization (commit + push/force-push +
// CompleteRepoCycle/CompleteRefactor). All failures call FailRepoCycle.
//
// Ports completeRebaseRepoCycleCmd (app.go:6870-6901),
// completeTweakRepoCycleCmd (app.go:6904-6941), and
// completeReviewCommentsRepoCycleCmd (app.go:6944-6982).
func (o *Orchestrator) CompleteRepoCycle(featureID, repoName string) error {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return fmt.Errorf("load feature: %w", err)
	}

	var repo *feature.FeatureRepo
	for i := range f.Repos {
		if f.Repos[i].Name == repoName {
			repo = &f.Repos[i]
			break
		}
	}
	if repo == nil {
		return fmt.Errorf("repo %q not found in feature", repoName)
	}

	// Determine the active cycle type for this repo.
	var cycleType feature.RepoCycleType
	if rc, ok := f.RepoCycles[repoName]; ok && rc != nil {
		cycleType = rc.Type
	}

	workDir := repo.WorktreePath
	if workDir == "" {
		workDir = repo.Path
	}
	branch := repo.Branch
	if branch == "" {
		branch = "feature/" + f.Slug
	}

	// Publisher/Rebaser may be nil in unit tests that exercise the lifecycle
	// plumbing without wiring git adapters; skip the commit/rebase/push when
	// absent so the cycle still completes cleanly.
	commitAll := func(msg string) {
		if o.deps.Publisher != nil {
			_ = o.deps.Publisher.CommitAll(workDir, msg)
		}
	}
	pullRebase := func() {
		if o.deps.Rebaser != nil {
			if prr := o.deps.Rebaser.PullRebase(workDir, branch); prr.Outcome != ports.PullRebaseSuccess {
				_ = prr.Err
			}
		}
	}
	push := func() error {
		if o.deps.Publisher == nil {
			return nil
		}
		return o.deps.Publisher.Push(workDir, branch)
	}

	switch cycleType {
	case feature.CycleRebase:
		// Guard: refuse to complete the rebase cycle if the worktree is still
		// mid-rebase. The agent may have resolved files but never run
		// `git rebase --continue`, in which case the branch pointer is still
		// stale; force-pushing now would push the pre-rebase head and silently
		// leave the PR in its unmerged state.
		if o.deps.Rebaser != nil {
			if inProgress, _ := o.deps.Rebaser.RebaseInProgress(workDir); inProgress {
				_ = o.deps.Lifecycle.FailRepoCycle(featureID, repoName, ErrRebaseIncomplete.Error())
				return fmt.Errorf("complete repo cycle: %w", ErrRebaseIncomplete)
			}
		}
		// Commit any leftover conflict resolutions, then force-push.
		commitAll("Resolve rebase conflicts")
		if o.deps.Rebaser != nil {
			if err := o.deps.Rebaser.ForcePush(workDir, branch); err != nil {
				_ = o.deps.Lifecycle.FailRepoCycle(featureID, repoName, "force push: "+err.Error())
				return fmt.Errorf("force push: %w", err)
			}
		}
		// The feature stays StatusPublished — post-publish rebase cycle state
		// lives in RepoCycles[repoName] and is cleared by CompleteRepoCycle.
		// Promoting the feature to StatusCodeReady here would regress
		// published-only flows (review-comments, subsequent cycles) and
		// contradicts the unified per-repo cycle model.
		if err := o.deps.Lifecycle.CompleteRepoCycle(featureID, repoName); err != nil {
			return fmt.Errorf("complete repo cycle: %w", err)
		}
		return nil

	case feature.CycleReviewComments:
		commitAll("Address review comments")
		pullRebase()
		if err := push(); err != nil {
			_ = o.deps.Lifecycle.FailRepoCycle(featureID, repoName, "push: "+err.Error())
			return fmt.Errorf("push: %w", err)
		}
		if err := o.replyToSavedReviewComments(f, *repo, workDir, reviewCommentsPRURL(f, repoName), repoName); err != nil {
			_ = o.deps.Lifecycle.FailRepoCycle(featureID, repoName, "review-comments: "+err.Error())
			return fmt.Errorf("review-comments: %w", err)
		}
		if err := o.deps.Lifecycle.CompleteRepoCycle(featureID, repoName); err != nil {
			return fmt.Errorf("complete repo cycle: %w", err)
		}
		return nil

	case feature.CycleTweak:
		// FR approved for a tweak cycle. The TUI's tweak-finish command
		// owns the commit + pull-rebase + push + state-transition chain
		// (with proper rebase-conflict UX via PublishConflictError →
		// RebaseResultMsg). Calling that chain inline here would surface
		// PublishConflictError through surfaceDispatchCompletionError and
		// mark the feature Failed, regressing the conflict-resolution UX.
		// Emit TweakReviewApproved with the per-repo name so the TUI's
		// OrchTweakReviewApprovedMsg handler can route to the right repo.
		o.emitEvent(ports.Event{
			Type:      ports.TweakReviewApproved,
			FeatureID: featureID,
			Message:   repoName,
		})
		return nil

	case feature.CycleRefactor:
		return o.CompleteRefactorRepoCycle(featureID, repoName)
	}

	// Unknown cycle type — best-effort cycle completion.
	if err := o.deps.Lifecycle.CompleteRepoCycle(featureID, repoName); err != nil {
		return fmt.Errorf("complete repo cycle: %w", err)
	}
	return nil
}

// DispatchRepoCycle is the top-level entry point for launching any per-repo
// post-publish cycle. It routes tweak cycles to StartTweak (which
// handles the interactive Bubble Tea attach path) and all other cycle types
// to StartRepoCycleImplement (which runs an autonomous implementation loop).
//
// Callers (including the TUI) should use this method rather than dispatching
// directly so cycle-type routing stays in one place.
func (o *Orchestrator) DispatchRepoCycle(
	featureID, repoName string,
	cycleType feature.RepoCycleType,
	planContent string,
) (string, error) {
	switch cycleType {
	case feature.CycleTweak:
		// Tweak is feature-level. The repoName argument is preserved on the
		// DispatchRepoCycle signature for backward compatibility with legacy
		// callers but is not threaded into StartTweak — the session mounts every
		// Feature.Repos worktree.
		_ = repoName
		return o.StartTweak(featureID)
	case feature.CycleRebase, feature.CycleReviewComments:
		return o.StartRepoCycleImplement(featureID, repoName, cycleType, planContent)
	case feature.CycleRefactor:
		// Refactor cycles are launched via StartRefactorCycle — DispatchRepoCycle
		// treats the planContent as the refactor prompt for convenience.
		return o.StartRefactorCycle(featureID, repoName, planContent)
	default:
		return "", fmt.Errorf("unknown cycle type %q", cycleType)
	}
}

// StartRefactorCycle launches the feature-level refactor cycle. The per-repo
// entry shape is preserved for the TUI (Manager.StartRefactor /
// app.startRefactorCmd) — repoName is treated as a hint, the loop mounts every
// Feature.Repos worktree, and the refactor-plan step's `**Repo:** <name>` tags
// determine the staged subset.
//
// Validates StatusCodeReady/StatusPublished, blocks concurrent refactor cycles
// on the same feature, and dispatches the loop in a background goroutine. The
// inner loop owns RefactorCount increment, ActiveCycle stamping, and
// the refactor-plan + iterative implement state machine. Result routing
// flows through handleFeatureRefactorDone, which mirrors the legacy
// HandleRefactorCycleLoopDone surface so per-repo TUI rendering keeps working.
//
// Ports startRefactorCmd (app.go).
func (o *Orchestrator) StartRefactorCycle(
	featureID, repoName, prompt string,
	evidence ...RefactorEvidence,
) (string, error) {
	return o.startFeatureRefactor(featureID, repoName, prompt, mergeRefactorEvidence(evidence...))
}

// RestartRefactorCycle re-launches a refactor loop for a stale cycle. Unlike
// StartRefactorCycle, it does NOT increment RefactorCount — it reuses the
// existing refactor directory and count from the prior attempt.
//
// Ports restartRepoCycleRefactorCmd (app.go:7092-7184).
func (o *Orchestrator) RestartRefactorCycle(
	featureID, repoName, prompt string,
	evidence ...RefactorEvidence,
) (string, error) {
	// Refactor is feature-level, so "restart" semantics collapse with "start":
	// the loop increments and stages a new dir per invocation. The legacy
	// "reuse refactor-N count" behavior is unnecessary because the flat artifact
	// layout (no per-repo subdir) and AtomicPhaseStamp staged-subset semantics
	// make a fresh dir on every retry the simpler invariant.
	return o.startFeatureRefactor(featureID, repoName, prompt, mergeRefactorEvidence(evidence...))
}

// CompleteRefactorRepoCycle finalizes a per-repo refactor cycle: commits
// any refactor changes, pull-rebases, pushes, clears RefactorPrompt, and
// completes the cycle. Failures call FailRepoCycle.
//
// Ports completeRefactorRepoCycleCmd (app.go:7209-7252).
func (o *Orchestrator) CompleteRefactorRepoCycle(featureID, repoName string) error {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return fmt.Errorf("load feature: %w", err)
	}

	var repo *feature.FeatureRepo
	for i := range f.Repos {
		if f.Repos[i].Name == repoName {
			repo = &f.Repos[i]
			break
		}
	}
	if repo == nil {
		return fmt.Errorf("repo %q not found in feature", repoName)
	}

	workDir := repo.WorktreePath
	if workDir == "" {
		workDir = repo.Path
	}
	branch := repo.Branch
	if branch == "" {
		branch = "feature/" + f.Slug
	}

	// Publisher/Rebaser may be nil in unit tests that exercise the lifecycle
	// plumbing without wiring git adapters; skip the commit/push when absent
	// so the refactor-cycle finalisation still clears state and marks the
	// cycle complete.
	if o.deps.Publisher != nil {
		_ = o.deps.Publisher.CommitAll(workDir, "Apply refactor changes")
	}

	if o.deps.Rebaser != nil {
		if prr := o.deps.Rebaser.PullRebase(workDir, branch); prr.Outcome != ports.PullRebaseSuccess {
			// Log-equivalent: swallow failure like TUI logPhaseError path.
			_ = prr.Err
		}
	}

	if o.deps.Publisher != nil {
		if err := o.deps.Publisher.Push(workDir, branch); err != nil {
			_ = o.deps.Lifecycle.FailRepoCycle(featureID, repoName, "push: "+err.Error())
			return fmt.Errorf("push: %w", err)
		}
	}

	// Clear refactor state and complete the cycle.
	_ = o.deps.Store.Modify(featureID, func(ff *feature.Feature) error {
		ff.RefactorPrompt = ""
		return nil
	})
	if err := o.deps.Lifecycle.CompleteRepoCycle(featureID, repoName); err != nil {
		return fmt.Errorf("complete repo cycle: %w", err)
	}
	return nil
}

// hasRunningRefactor returns true if any repo on the feature has an active
// CycleRefactor cycle — running, reviewing, or paused on a NEED_USER_INPUT
// gate. Paused refactor cycles still own the shared feature-level
// RefactorPrompt/RefactorCount, so they must remain exclusive the same way
// as the running/reviewing states. Mirrors the TUI's hasRunningRefactorCycle
// helper at app.go:7503.
func hasRunningRefactor(f *feature.Feature) bool {
	if f == nil {
		return false
	}
	for _, rc := range f.RepoCycles {
		if rc == nil {
			continue
		}
		if rc.Type != feature.CycleRefactor {
			continue
		}
		switch rc.Status {
		case feature.RepoCycleRunning, feature.RepoCycleReviewing, feature.RepoCycleNeedUserInput:
			return true
		}
	}
	return false
}

func reviewCommentsPRURL(f *feature.Feature, repoName string) string {
	urls := f.PRURLs()
	if repoName != "" {
		return urls[repoName]
	}
	if len(f.Repos) == 1 {
		return urls[f.Repos[0].Name]
	}
	return ""
}

func (o *Orchestrator) replyToSavedReviewComments(
	f *feature.Feature,
	repo feature.FeatureRepo,
	worktreeDir, prURL, repoName string,
) error {
	if o.deps.Reviewer == nil {
		return errors.New("review-comments completion requires Reviewer adapter")
	}
	if prURL == "" {
		return errors.New("review-comments PR URL is empty")
	}

	baseDir := o.stateDir()
	data, loadErr := agent.LoadReviewCommentsForRepo(baseDir, f, repoName)
	if loadErr != nil {
		return fmt.Errorf("load review comments: %w", loadErr)
	}

	commitSHA, _ := o.deps.Reviewer.LatestCommitSHA(worktreeDir)
	resolutions, resErr := agent.LoadReviewResolutionsForRepo(baseDir, f, repoName)

	resMap := make(map[int]agent.ReviewResolution)
	if resErr == nil {
		for _, r := range resolutions {
			resMap[r.CommentID] = r
		}
	}

	var replyErrs []string
	var addressedIDs []int
	for _, c := range data.Comments {
		var reply string
		if res, ok := resMap[c.ID]; ok {
			switch res.Disposition {
			case "dismissed":
				reply = fmt.Sprintf("Dismissed: %s", res.Description)
			default:
				if commitSHA != "" {
					reply = fmt.Sprintf("Addressed in %s — %s", commitSHA, res.Description)
				} else {
					reply = fmt.Sprintf("Addressed — %s", res.Description)
				}
			}
		} else if commitSHA != "" {
			reply = fmt.Sprintf("Addressed in %s", commitSHA)
		} else {
			reply = "Addressed"
		}

		switch c.Type {
		case ports.CommentTypeIssue:
			body := fmt.Sprintf("Re: [comment](%s#issuecomment-%d) by @%s\n\n%s",
				prURL, c.ID, c.User.Login, reply)
			if err := o.deps.Reviewer.ReplyToIssueComment(repo.Path, prURL, body); err != nil {
				replyErrs = append(replyErrs, fmt.Sprintf("issue comment %d: %v", c.ID, err))
			} else {
				addressedIDs = append(addressedIDs, c.ID)
			}
		case ports.CommentTypeReviewBody:
			body := fmt.Sprintf("Re: [review](%s#pullrequestreview-%d) by @%s\n\n%s",
				prURL, c.ID, c.User.Login, reply)
			if err := o.deps.Reviewer.ReplyToIssueComment(repo.Path, prURL, body); err != nil {
				replyErrs = append(replyErrs, fmt.Sprintf("review body %d: %v", c.ID, err))
			} else {
				addressedIDs = append(addressedIDs, c.ID)
			}
		default:
			if err := o.deps.Reviewer.ReplyToPRComment(repo.Path, prURL, c.ID, reply); err != nil {
				replyErrs = append(replyErrs, fmt.Sprintf("comment %d: %v", c.ID, err))
			} else {
				addressedIDs = append(addressedIDs, c.ID)
			}
		}
	}

	threadMap, threadErr := o.deps.Reviewer.FetchReviewThreadMap(repo.Path, prURL)
	if threadErr == nil {
		for _, c := range data.Comments {
			if c.Type != ports.CommentTypeReview {
				continue
			}
			if threadID, ok := threadMap[c.ID]; ok {
				_ = o.deps.Reviewer.ResolveReviewThread(repo.Path, threadID)
			}
		}
	}

	if len(replyErrs) > 0 {
		return fmt.Errorf("failed to reply to %d comments: %s", len(replyErrs), strings.Join(replyErrs, "; "))
	}
	if len(addressedIDs) > 0 {
		_ = agent.SaveAddressedIDsForRepo(baseDir, f, repoName, addressedIDs)
	}
	return nil
}
