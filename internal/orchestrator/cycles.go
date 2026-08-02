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

// Package orchestrator owns post-publish review-comments cycle
// lifecycle methods for multi-repo features.
package orchestrator

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// finalStatusLoopError is the agent.LoopResult.FinalStatus sentinel this
// package sets when RunImplementationLoop returns a non-nil error alongside
// a nil-or-partial result (loop infrastructure failure, not a normal outcome).
const finalStatusLoopError = "error"

// RepoCycleLoopResultInput carries the result of an agent implementation loop
// for a per-repo cycle (rebase / review-comments).
type RepoCycleLoopResultInput struct {
	RepoName string
	Result   *agent.LoopResult
	Err      error
}

// StartRepoCycleImplement launches an implementation loop for a per-repo
// post-publish cycle. Rebase is feature-level only; callers must use
// StartFeatureRebase for that flow.
//
// The cycle state remains persisted per repository even though one loop may
// aggregate work across several repositories.
func (o *Orchestrator) StartRepoCycleImplement(
	featureID, repoName string,
	cycleType feature.RepoCycleType,
	planContent string,
) (string, error) {
	return o.StartRepoCycleImplementWithPreparation(featureID, repoName, cycleType, planContent, nil)
}

// StartRepoCycleImplementWithPreparation runs prepare and starts a per-repo
// cycle under one relationship read lock. Adapters use this boundary when
// cycle preparation mutates durable state that must not land after a
// concurrent child creation wins the relationship write lock.
func (o *Orchestrator) StartRepoCycleImplementWithPreparation(
	featureID, repoName string,
	cycleType feature.RepoCycleType,
	planContent string,
	prepare func() error,
) (string, error) {
	o.relationshipMu.RLock()
	defer o.relationshipMu.RUnlock()
	if err := o.RelationshipGuard(featureID, MutationDelivery); err != nil {
		return "", err
	}
	if prepare != nil {
		if err := prepare(); err != nil {
			return "", err
		}
	}
	return o.startRepoCycleImplementLocked(featureID, repoName, cycleType, planContent)
}

// startRepoCycleImplementLocked starts a per-repo cycle while the caller holds
// the relationship read lock.
func (o *Orchestrator) startRepoCycleImplementLocked(
	featureID, repoName string,
	cycleType feature.RepoCycleType,
	planContent string,
) (string, error) {
	if cycleType == feature.CycleReviewComments {
		// The repoName from the caller is purely a hint — the loop aggregates
		// unaddressed comments across every Feature.Repos PR. planContent is
		// ignored; the loop builds the aggregated plan from the per-repo
		// `comments.json` artifacts already saved before dispatch.
		_ = planContent
		return o.startFeatureReviewComments(featureID, repoName)
	}

	return "", fmt.Errorf("unsupported repo implementation cycle %q", cycleType)
}

// HandleRepoCycleLoopDone processes the completion of a per-repo cycle
// implementation. On NEED_USER_INPUT routes through the cycle gate entry so
// the cycle pauses on its persisted artifact instead of failing. On failure
// calls FailRepoCycle; on success routes through per-repo Final Review
// before cycle completion.
//
// Result handling routes through the persisted cycle type.
func (o *Orchestrator) HandleRepoCycleLoopDone(
	featureID string,
	input RepoCycleLoopResultInput,
) error {
	if input.Result != nil && input.Result.FinalStatus == finalStatusNeedUserInput {
		cycleType := cycleTypeForRepo(o, featureID, input.RepoName)
		return o.onRepoCycleNeedUserInput(featureID, input.RepoName, cycleType, input.Result)
	}
	if input.Result == nil || input.Result.FinalStatus != reviewStatusPassed {
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
// the decision record are diagnostic only. Review-comments re-enters through
// restartRepoCycleImplement. Cycle Count and PlanPath survive the round-trip.
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
	case feature.CycleReviewComments:
		_, err := o.restartRepoCycleImplement(featureID, repoName, rc)
		return err
	default:
		return fmt.Errorf("unsupported cycle type %q for repo %q", rc.Type, repoName)
	}
}

// restartRepoCycleImplement re-launches an autonomous cycle implementation
// loop for a paused review-comments cycle. Reuses the persisted PlanPath and
// Count rather than calling StartRepoCycle (which would increment the count
// and enumerate a new directory). Returns the session id (always "" for
// loop-based cycles) and any launch error.
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

	pr := o.deps.PhaseRunner
	if pr == nil {
		return "", errors.New("phase runner not configured")
	}
	_ = os.MkdirAll(cycleBaseDir, 0o755)

	kbInfos := o.computeKBInfos(f)
	implModel := f.Models.Implementation
	pipelineEffort := f.EffectivePipeline().EffortLevel()
	effortCaps := pr.EffortCapabilitiesForModel(implModel)
	effectiveEffort, effortSource := llm.ResolveEffortFromString(f.Effort.Implementation, effortCaps, pipelineEffort)
	if llm.EffortDrifted(llm.EffortLevel(f.Effort.Implementation), effortCaps) {
		log.Printf("feature %s: implementation effort %q is not supported by model %q; falling back to Auto (%s)",
			f.ID, f.Effort.Implementation, implModel, string(pipelineEffort))
	}
	reviewEffort, reviewEffortSource := pr.ResolveSecondaryEffort(f, llm.PhaseReview, f.Models.Review)
	cfg := agent.ImplementConfig{
		Feature:                    f,
		FeatureStore:               o.deps.Store,
		WorkDir:                    workDir,
		PlanPath:                   planPath,
		KBInfos:                    kbInfos,
		MaxIterations:              f.MaxIterations,
		MaxConsecFails:             3,
		MaxConsecNoProgress:        3,
		ExitCriteria:               restartRepoCycleExitCriteria(rc.Type, planPath),
		Model:                      implModel,
		ReviewModel:                f.Models.Review,
		ResolveSessionConfig:       pr.SessionRuntimeConfigResolver(featureID),
		ArtifactDir:                cycleBaseDir,
		StateDir:                   filepath.Join(baseDir, featureID),
		RunDir:                     agent.ActiveRunDir(baseDir, f),
		RepoName:                   repoName,
		DesignArtifactPath:         f.DesignArtifactPath(),
		DangerouslySkipPermissions: pr.DangerouslySkipPermissions,
		PermissionCache:            pr.PermissionCache,
		CommandRunner:              pr.CommandRunner,
		BuildSession:               pr.BuildSession,
		AskingClause:               pr.AskingClauseForModel(f.Models.Implementation),
		EffortLevel:                pipelineEffort,
		EffectiveEffort:            effectiveEffort,
		EffortSource:               effortSource,
		ReviewEffectiveEffort:      reviewEffort,
		ReviewEffortSource:         reviewEffortSource,
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
			result.FinalStatus = finalStatusLoopError
			result.LastError = loopErr.Error()
		}
		_ = o.HandleRepoCycleLoopDone(featureID, RepoCycleLoopResultInput{
			RepoName: repoName,
			Result:   result,
		})
	}()
	return "", nil
}

// restartRepoCycleExitCriteria reuses the same synthesized exit criteria
// the live cycle driver computed, so a restarted cycle implementer keeps
// judging against "resolve every aggregated comment" rather than the
// feature's raw ExitCriteria. planPath is the persisted cycle plan; the
// resolutions file lives alongside it in the same artifact directory.
func restartRepoCycleExitCriteria(cycleType feature.RepoCycleType, planPath string) string {
	switch cycleType {
	case feature.CycleReviewComments:
		resolutionsPath := agent.ReviewCommentsResolutionsPath(filepath.Dir(planPath))
		return agent.ReviewCommentsExitCriteria(resolutionsPath)
	default:
		return ""
	}
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
// Other callers (legacy HandleRepoCycleLoopDone fallthrough for
// non-rebase/non-review-comments cycles) are dead post-slices-4-7 but
// preserved for safety.
func (o *Orchestrator) StartCycleFinalReview(featureID string) error {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return o.failCycleAcrossRepos(featureID, "load feature: "+err.Error())
	}

	// Mark every active cycle entry as reviewing so clients surface the final
	// review phase. Preserve the per-repo MarkRepoCycleReviewing contract
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
// On review_passed: iterates per-repo CompleteRepoCycle for backward
// compatibility with the legacy fallthrough path (rebase / review-comments
// are routed via their unified loops post slices-4-7, so this
// branch is rarely exercised in production).
//
// On failure: marks every active cycle entry failed.
func (o *Orchestrator) handleCycleFinalReviewDone(
	featureID string,
	result *agent.FeatureFinalReviewResult,
) error {
	if result == nil || result.FinalStatus != reviewStatusPassed {
		errMsg := "cycle review failed"
		if result != nil && result.LastError != "" {
			errMsg = result.LastError
		}
		return o.failCycleAcrossRepos(featureID, errMsg)
	}

	// Review approved — the legacy per-repo-cycle path (rebase /
	// review-comments) iterates CompleteRepoCycle for backward
	// compatibility, but the unified loops own those paths post slices-4-7.
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return fmt.Errorf("load feature post-cycle FR: %w", err)
	}

	// Compatibility branch for legacy callers that still complete cycles
	// through HandleRepoCycleLoopDone.
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

// failCycleAcrossRepos marks every Feature.Repos cycle entry failed with
// the provided error message. Used when the feature-level FR cannot
// dispatch (load feature, missing phase runner, etc.) so the per-repo API
// surface (RepoCycles[name].LastError != "") clears.
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
// to the cycle-type-specific finalization. Rebase completion is owned by the
// feature-level rebase publish policy.
//
// Review-comment and refactor cycles use their own finalization policies.
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

	workDir := repoWorkDir(*repo)
	branch := repoBranch(f, *repo)

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

	}

	return fmt.Errorf("unsupported repo cycle type %q for repo %q", cycleType, repoName)
}

// DispatchRepoCycle is the top-level entry point for launching any per-repo
// post-publish cycle. It routes to StartRepoCycleImplement (which runs an
// autonomous implementation loop).
//
// Callers should use this method rather than dispatching
// directly so cycle-type routing stays in one place.
func (o *Orchestrator) DispatchRepoCycle(
	featureID, repoName string,
	cycleType feature.RepoCycleType,
	planContent string,
) (string, error) {
	switch cycleType {
	case feature.CycleReviewComments:
		return o.StartRepoCycleImplement(featureID, repoName, cycleType, planContent)
	default:
		return "", fmt.Errorf("unknown cycle type %q", cycleType)
	}
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
	if prURL == "" {
		return errors.New("review-comments PR URL is empty")
	}

	baseDir := o.stateDir()
	data, loadErr := agent.LoadReviewCommentsForRepo(baseDir, f, repoName)
	if loadErr != nil {
		return fmt.Errorf("load review comments: %w", loadErr)
	}

	commitSHA, _ := git.LatestCommitSHA(worktreeDir)
	resolutions, resErr := agent.LoadReviewResolutionsForRepo(baseDir, f, repoName)

	resMap := make(map[int]agent.ReviewResolution)
	if resErr == nil {
		for _, r := range resolutions {
			resMap[r.CommentID] = r
		}
	}

	var replyErrs []string
	var addressedIDs []int
	addressedReviewComments := make(map[int]bool)
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
			if err := git.ReplyToIssueComment(repo.Path, prURL, body); err != nil {
				replyErrs = append(replyErrs, fmt.Sprintf("issue comment %d: %v", c.ID, err))
			} else {
				addressedIDs = append(addressedIDs, c.ID)
			}
		case ports.CommentTypeReviewBody:
			body := fmt.Sprintf("Re: [review](%s#pullrequestreview-%d) by @%s\n\n%s",
				prURL, c.ID, c.User.Login, reply)
			if err := git.ReplyToIssueComment(repo.Path, prURL, body); err != nil {
				replyErrs = append(replyErrs, fmt.Sprintf("review body %d: %v", c.ID, err))
			} else {
				addressedIDs = append(addressedIDs, c.ID)
			}
		default:
			if err := git.ReplyToPRComment(repo.Path, prURL, c.ID, reply); err != nil {
				replyErrs = append(replyErrs, fmt.Sprintf("comment %d: %v", c.ID, err))
			} else {
				addressedIDs = append(addressedIDs, c.ID)
				addressedReviewComments[c.ID] = true
			}
		}
	}

	threadMap, threadErr := git.FetchReviewThreadMap(repo.Path, prURL)
	if threadErr == nil {
		for _, c := range data.Comments {
			if !addressedReviewComments[c.ID] {
				continue
			}
			if threadID, ok := threadMap[c.ID]; ok {
				_ = git.ResolveReviewThread(repo.Path, threadID)
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
