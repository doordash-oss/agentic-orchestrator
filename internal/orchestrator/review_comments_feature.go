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

// Package orchestrator wires the feature-level review-comments cycle. One call
// to startFeatureReviewComments aggregates the per-repo `comments.json`
// artifacts the TUI saved across every Feature.Repos PR into one work list,
// launches agent.RunReviewCommentsLoop, and translates the loop outcome into
// the per-repo lifecycle surface used by downstream listeners.
package orchestrator

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// startFeatureReviewComments launches the unified review-comments cycle.
// Aggregates unaddressed PR comments across every Feature.Repos PR (by
// reading the per-repo `comments.json` artifacts the TUI saved when the
// user dispatched the cycle), builds the agent.ReviewCommentsLoopConfig,
// and runs RunReviewCommentsLoop in a background goroutine tracked by
// cycleWG.
//
// The hintRepoName argument is the repo the TUI initially dispatched
// from (the user picked one in the comments view). Under the unified
// flow this is purely a hint — the loop aggregates every Feature.Repos
// PR with unaddressed comments, including the hint repo. When no other
// repo has saved comments, the cycle degenerates to a single-repo cycle
// with the hint repo as the only staged target.
//
// Returns "" + nil on successful dispatch (no stable session ID; the
// inner loop owns per-iteration IDs). Returns ("", err) on dispatch
// failure (no comments found, store error, etc.).
func (o *Orchestrator) startFeatureReviewComments(
	featureID, hintRepoName string,
) (string, error) {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return "", fmt.Errorf("load feature: %w", err)
	}

	pr := o.deps.PhaseRunner
	if pr == nil {
		return "", errors.New("phase runner not configured")
	}

	// Aggregate comments across every Feature.Repos PR. Each repo's
	// per-repo comments.json was saved by the TUI before dispatch
	// (startReviewCommentsRepoCycleFromView). If a repo has no saved
	// file or zero comments, it is silently skipped — only repos with
	// at least one unaddressed comment enter the staged subset.
	targets := o.aggregateReviewCommentTargets(f)
	if len(targets) == 0 {
		// Nothing to address. Surface as a clean error to the caller so
		// the TUI can clear its "starting" status.
		return "", fmt.Errorf("review-comments: no unaddressed comments found for feature %s", featureID)
	}

	// Open per-repo cycle entries for every staged repo so existing TUI
	// rendering paths (RepoCycles[name].Status == "running") light up while
	// the feature-level review-comments loop runs.
	for _, t := range targets {
		_ = o.deps.Lifecycle.StartRepoCycle(featureID, t.RepoName, feature.CycleReviewComments)
	}
	// Reload the feature so per-repo cycle Counts reflect the
	// StartRepoCycle writes.
	f, err = o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return "", fmt.Errorf("reload feature: %w", err)
	}

	// Stage the aggregated review plan synchronously so the TUI / tests
	// can read it immediately after this method returns. The async
	// loop's plan write below will overwrite with identical content —
	// the operation is idempotent.
	if err := o.stageReviewCommentsPlanArtifacts(f, targets); err != nil {
		return "", fmt.Errorf("stage review-comments plan: %w", err)
	}

	cfg := agent.ReviewCommentsLoopConfig{
		Feature:                    f,
		FeatureStore:               o.deps.Store,
		StateDir:                   o.stateDir(),
		RepoTargets:                targets,
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
	}
	_ = hintRepoName // hint diagnostic only; the loop aggregates every Feature.Repos PR.

	sm := o.deps.Sessions
	o.cycleWG.Go(func() {
		result, loopErr := agent.RunReviewCommentsLoop(cfg, sm)
		o.handleFeatureReviewCommentsDone(featureID, targets, result, loopErr)
	})

	return "", nil
}

// aggregateReviewCommentTargets walks every Feature.Repos entry and
// loads the per-repo `comments.json` saved by the TUI. Repos with no
// saved file or zero unaddressed comments are silently skipped.
//
// The returned slice carries one ReviewCommentsRepoTarget per repo with
// at least one unaddressed comment, sorted by repo name for
// deterministic plan output. Each target's PRURL is read from the
// repo's RepoStates entry; mode is the per-repo mode the TUI saved.
func (o *Orchestrator) aggregateReviewCommentTargets(
	f *feature.Feature,
) []agent.ReviewCommentsRepoTarget {
	stateDir := o.stateDir()
	prURLs := f.PRURLs()

	var out []agent.ReviewCommentsRepoTarget
	for i := range f.Repos {
		repo := &f.Repos[i]
		data, loadErr := agent.LoadReviewCommentsForRepo(stateDir, f, repo.Name)
		if loadErr != nil || data == nil {
			continue
		}
		// Filter against any prior addressed-IDs ledger so re-dispatch
		// after a partial cycle does not re-address comments that were
		// already replied to.
		addressed, _ := agent.LoadAddressedIDsForRepo(stateDir, f, repo.Name)
		filtered := make([]ports.ReviewComment, 0, len(data.Comments))
		for _, c := range data.Comments {
			if !addressed[c.ID] {
				filtered = append(filtered, c)
			}
		}
		if len(filtered) == 0 {
			continue
		}
		out = append(out, agent.ReviewCommentsRepoTarget{
			RepoName: repo.Name,
			PRURL:    prURLs[repo.Name],
			Mode:     data.Mode,
			Comments: filtered,
		})
	}
	return out
}

// stageReviewCommentsPlanArtifacts writes the aggregated review plan +
// resolutions path placeholder synchronously so the orchestrator's
// caller can read it before the async loop runs. The loop overwrites
// with the same content; we reuse the agent helper so the format stays
// in one place.
//
// The artifact dir is `runs/run-N/review-comments-N+1/` — the loop
// bumps ReviewCommentsCount inside its own goroutine, so this method
// computes the next dir name by speculating one-ahead. Speculation is
// safe because ReviewCommentsCount is monotonic and only the loop
// bumps it; no other code path can race ahead of us between this stage
// write and the loop's bump.
func (o *Orchestrator) stageReviewCommentsPlanArtifacts(
	f *feature.Feature,
	targets []agent.ReviewCommentsRepoTarget,
) error {
	nextDir := filepath.Join(
		agent.ActiveRunDir(o.stateDir(), f),
		fmt.Sprintf("review-comments-%d", f.ReviewCommentsCount()+1),
	)
	if err := os.MkdirAll(nextDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", nextDir, err)
	}
	resolutionsPath := filepath.Join(nextDir, "review-resolutions.json")
	planPath := filepath.Join(nextDir, "review-plan.md")
	plan := agent.BuildAggregatedReviewCommentsPlan(targets, resolutionsPath)
	if err := os.WriteFile(planPath, []byte(plan), 0o644); err != nil {
		return fmt.Errorf("write review-plan.md: %w", err)
	}
	return nil
}

// handleFeatureReviewCommentsDone routes the unified review-comments
// loop's result back into the per-repo legacy plumbing so the TUI's
// existing event chain keeps working. On success: commit + push each
// staged repo's branch, dispatch comment replies via the Reviewer port,
// then clear each per-repo cycle entry. On failure: per-repo
// FailRepoCycle records the error.
func (o *Orchestrator) handleFeatureReviewCommentsDone(
	featureID string,
	targets []agent.ReviewCommentsRepoTarget,
	result *agent.ReviewCommentsLoopResult,
	loopErr error,
) {
	if loopErr != nil || result == nil {
		errMsg := "review-comments: dispatch failed"
		if loopErr != nil {
			errMsg = "review-comments: " + loopErr.Error()
		}
		for _, t := range targets {
			_ = o.deps.Lifecycle.FailRepoCycle(featureID, t.RepoName, errMsg)
		}
		return
	}

	switch result.FinalStatus {
	case "review_passed":
		// Cycle complete: commit + push each touched repo's branch,
		// then walk the aggregated resolutions and dispatch per-PR
		// replies. The legacy per-repo CompleteRepoCycle path runs
		// the same chain (commitAll + pullRebase + push +
		// replyToSavedReviewComments); under the unified flow we
		// invoke that completion per staged repo so the existing
		// per-PR reply path stays intact.
		for _, t := range targets {
			if err := o.completeReviewCommentsRepoFinalize(featureID, t.RepoName); err != nil {
				_ = o.deps.Lifecycle.FailRepoCycle(featureID, t.RepoName, err.Error())
				continue
			}
		}
		// Clear any stale LastError on each repo whose comments were
		// addressed; PR URLs and Touched flags are unchanged.
		_ = o.deps.Store.Modify(featureID, func(ff *feature.Feature) error {
			for _, t := range targets {
				if st, ok := ff.RepoStates[t.RepoName]; ok && st != nil {
					st.LastError = ""
				}
			}
			return nil
		})
		for _, t := range targets {
			_ = o.deps.Lifecycle.CompleteRepoCycle(featureID, t.RepoName)
		}
		return

	case "interrupted", "no_op":
		// Persisted state preserved for restart. Leave per-repo
		// cycle entries in place; the harness recovery / next user
		// action handles them.
		return

	default:
		// max_iterations / safety_rail / failed: surface error per
		// repo so the TUI can present the failed cycle.
		errMsg := "review-comments: " + result.FinalStatus
		if result.LastError != "" {
			errMsg = "review-comments: " + result.LastError
		}
		for _, t := range targets {
			_ = o.deps.Lifecycle.FailRepoCycle(featureID, t.RepoName, errMsg)
		}
		return
	}
}

// completeReviewCommentsRepoFinalize runs the post-loop finalisation
// for a single staged repo: commit + pull-rebase + push, then dispatch
// the per-PR reply chain via replyToSavedReviewComments. This mirrors
// the CycleReviewComments branch of CompleteRepoCycle so per-PR
// reply behavior is preserved under the unified flow without
// duplicating that logic.
func (o *Orchestrator) completeReviewCommentsRepoFinalize(featureID, repoName string) error {
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

	if o.deps.Publisher != nil {
		_ = o.deps.Publisher.CommitAll(workDir, "Address review comments")
	}
	if o.deps.Rebaser != nil {
		_ = o.deps.Rebaser.PullRebase(workDir, branch)
	}
	if o.deps.Publisher != nil {
		if err := o.deps.Publisher.Push(workDir, branch); err != nil {
			return fmt.Errorf("push: %w", err)
		}
	}
	prURL := reviewCommentsPRURL(f, repoName)
	if prURL == "" || o.deps.Reviewer == nil {
		return nil
	}
	if err := o.replyToSavedReviewComments(f, *repo, workDir, prURL, repoName); err != nil {
		return fmt.Errorf("review-comments reply: %w", err)
	}
	return nil
}
