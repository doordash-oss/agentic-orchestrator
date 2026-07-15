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

// Package agent — review_comments_loop.go is the unified feature-level
// review-comments cycle loop under SchemaVersionCurrent = 4. The legacy
// per-repo review-comments entry (Orchestrator.StartRepoCycleImplement
// (CycleReviewComments)) is replaced by RunReviewCommentsLoop: a single
// iterative loop that addresses unaddressed PR review comments across
// every `Feature.Repos` PR in one Claude session.
//
// Topology:
//
//   - Triggered when any `Feature.Repos` PR has unaddressed comments.
//     Participating repos = the subset of Feature.Repos with at least one
//     unaddressed comment. Comments are aggregated across all PRs into
//     the implement prompt, each tagged with `repo:` so the agent knows
//     which worktree owns the fix.
//   - Plan-less-aggregating: no guessed project commands are added. The
//     reviewer judges the resolution diff and structured resolution file.
//   - One Claude session per iteration with `--add-dir` for every
//     `Feature.Repos` worktree (and the state dir). The full workspace —
//     not the subset — is mounted because review threads frequently
//     reference cross-repo behavior; restricting the workspace to
//     "repos with comments" would cut off context the agent needs to
//     judge a comment correctly.
//   - Cycle-specific verification — the agent must produce
//     `review-resolutions.json` covering every aggregated comment, with
//     each entry marked addressed or dismissed — lives inside the loop
//     via the cycle-specific exit criteria and the aggregated review
//     plan template.
//   - On success: AtomicPhaseStamp transitions every staged repo's
//     RepoImpl entry to Touched (staged for FR); ActiveCycle clears.
//   - Crash recovery: re-runs the interrupted iteration from scratch
//     with a fresh session. Durable state (progress.md, plan checkmarks,
//     working tree, prior reviewer feedback, prior `addressed-ids.json`)
//     is the resume scaffolding.
package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// ReviewCommentsRepoTarget describes one repo's slice of unaddressed PR
// review comments entering the aggregating cycle. RepoName names the
// repo (must be in Feature.Repos); PRURL is the PR whose comments are
// being addressed; Comments is the list of unaddressed comments fetched
// for that repo (already filtered against `addressed-ids.json`).
type ReviewCommentsRepoTarget struct {
	RepoName string
	PRURL    string
	Mode     string // "auto" or "address_all"
	Comments []ports.ReviewComment
}

// ReviewCommentsLoopConfig holds configuration for the unified
// review-comments cycle loop. Mirrors RebaseLoopConfig in shape but
// carries the per-repo comment slices the orchestrator aggregated from
// the per-repo `comments.json` artifacts the TUI saved.
type ReviewCommentsLoopConfig struct {
	Feature      *feature.Feature
	FeatureStore ports.FeatureStore
	StateDir     string

	// RepoTargets is the resolved subset of Feature.Repos with at least
	// one unaddressed PR comment. The orchestrator computes this by
	// loading the per-repo `comments.json` artifacts saved by the TUI
	// when the user dispatched the cycle. Repos with no unaddressed
	// comments are intentionally NOT in this slice — they are not
	// staged for the AtomicPhaseStamp.
	RepoTargets []ReviewCommentsRepoTarget

	Model               string
	ReviewModel         string
	MaxIterations       int
	MaxConsecFails      int
	MaxConsecNoProgress int

	KBInfos []KBInfo

	DangerouslySkipPermissions bool
	PermissionCache            *permission.Cache
	BuildSession               func(BuildSessionOpts) ([]string, []string, *ports.SessionOpts, error)
	AskingClause               string
	EffortLevel                llm.EffortLevel
	SkillsDir                  string
	GuidelinesDir              string
	Observer                   *observe.Observer
	CommandRunner              ports.CommandRunner

	// RunImplementFn is a test seam: when non-nil, RunReviewCommentsLoop
	// calls this instead of RunImplementationLoop so unit tests can drive
	// the outer state-machine without launching a real Claude session.
	RunImplementFn func(ImplementConfig, ports.SessionManager) (*LoopResult, error)
}

// ReviewCommentsLoopResult is the outcome of a unified review-comments
// cycle.
//
// FinalStatus values:
//   - "review_passed":    every staged repo's comments addressed; the
//     loop's review gate APPROVED. AtomicPhaseStamp transitioned every
//     staged repo to Touched (staged for FR).
//   - "max_iterations":   hit MaxIterations without a passing review.
//     AtomicPhaseStamp wrote failed (LastError set).
//   - "safety_rail":      consecutive-failure / no-progress rail tripped.
//   - "interrupted":      shutdown / feature stopped mid-loop. No atomic
//     stamp; persisted state preserved for restart.
//   - "need_user_input":  iteration emitted NEED_USER_INPUT — cycle pause
//     gate; NeedUserInputPath points to the persisted gate artifact.
//   - "no_op":            no repos with unaddressed comments at entry.
//   - "failed":           dispatch error before iteration began.
//
// Repos is the deduplicated, sorted list of repo names with comments —
// the canonical staged subset the AtomicPhaseStamp wrote to.
type ReviewCommentsLoopResult struct {
	FinalStatus       string
	Iterations        int
	LastError         string
	NeedUserInputPath string
	Repos             []string
}

// RunReviewCommentsLoop drives the unified review-comments cycle. Cwd
// at the feature state dir; --add-dir mounts every Feature.Repos
// worktree (and the state dir). The agent reads the aggregated
// review-comments plan, dispatches per-repo Task fan-out for the
// fixes, runs build/test/lint against each touched repo, force-pushes
// each repo's branch, and writes one combined `review-resolutions.json`
// covering every aggregated comment.
//
// The loop sets `Feature.ActiveCycle = {Type: review-comments,
// Status: running}` at entry and clears it on success.
// ReviewCommentsCount is incremented per invocation so the artifact dir
// layout is `runs/run-N/review-comments-N/iteration-NN/` with no
// per-repo subdir.
func RunReviewCommentsLoop(cfg ReviewCommentsLoopConfig, sm ports.SessionManager) (*ReviewCommentsLoopResult, error) {
	if cfg.Feature == nil {
		return nil, fmt.Errorf("review-comments loop: feature is nil")
	}
	if cfg.FeatureStore == nil {
		return nil, fmt.Errorf("review-comments loop: feature store is nil")
	}

	// Filter the input targets down to those that actually carry at
	// least one comment — the orchestrator may pass a stale slice from
	// a race where the TUI saved an empty `comments.json`.
	staged := make([]ReviewCommentsRepoTarget, 0, len(cfg.RepoTargets))
	for _, t := range cfg.RepoTargets {
		if t.RepoName == "" || len(t.Comments) == 0 {
			continue
		}
		staged = append(staged, t)
	}
	if len(staged) == 0 {
		return &ReviewCommentsLoopResult{FinalStatus: "no_op"}, nil
	}

	// Sorted, deduplicated repo names form the canonical staged subset.
	repoNames := make([]string, 0, len(staged))
	seen := map[string]bool{}
	for _, t := range staged {
		if seen[t.RepoName] {
			continue
		}
		seen[t.RepoName] = true
		repoNames = append(repoNames, t.RepoName)
	}
	sort.Strings(repoNames)

	// Build the cycle workspace. Unlike rebase (subset only), the
	// review-comments cycle mounts the FULL Feature.Repos workspace:
	// review threads frequently reference cross-repo behavior, and the
	// agent may need to read a sibling repo's source to judge a comment
	// correctly even if no edit happens there.
	stateDir := filepath.Join(cfg.StateDir, cfg.Feature.ID)
	workspace, err := BuildWorkspace(cfg.Feature, stateDir)
	if err != nil {
		return nil, fmt.Errorf("review-comments loop: workspace setup: %w", err)
	}

	// Increment ReviewCommentsCount and stamp ActiveCycle = {Type:
	// review-comments, Status: running} in one Modify so a crash between
	// the two cannot leave inconsistent state.
	cycleCount := 0
	if err := cfg.FeatureStore.Modify(cfg.Feature.ID, func(f *feature.Feature) error {
		f.SetReviewCommentsCount(f.ReviewCommentsCount() + 1)
		f.SetActiveCycleType(feature.CycleReviewComments)
		f.ActiveCycle = &feature.CycleState{
			Type:   feature.CycleReviewComments,
			Status: feature.RepoCycleRunning,
			Count:  f.ReviewCommentsCount(),
		}
		cycleCount = f.ReviewCommentsCount()
		return nil
	}); err != nil {
		return nil, fmt.Errorf("review-comments loop: stamping cycle entry: %w", err)
	}

	// Reload so subsequent reads see the new count and ActiveCycle.
	if loaded, lerr := cfg.FeatureStore.Load(cfg.Feature.ID); lerr == nil && loaded != nil {
		cfg.Feature = loaded
	}

	// Flat artifact layout: runs/run-N/review-comments-N/iteration-NN/.
	// No per-repo subdir under the unified flow.
	runDir := ActiveRunDir(cfg.StateDir, cfg.Feature)
	artifactDir := filepath.Join(runDir, fmt.Sprintf("review-comments-%d", cycleCount))
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		return nil, fmt.Errorf("review-comments loop: mkdir %s: %w", artifactDir, err)
	}

	// Persist an empty plan-less testing contract. Agentico does not guess
	// language-specific project commands for review-comment edits.
	contractPath := filepath.Join(artifactDir, "testing-contract.yaml")
	if err := writeReviewCommentsTestingContract(contractPath, repoNames); err != nil {
		return nil, fmt.Errorf("review-comments loop: testing contract: %w", err)
	}

	// Author the aggregated review-comments plan markdown. The plan
	// file lives at the flat artifact dir; the resolutions JSON path is
	// referenced inside the plan so the agent writes one combined
	// resolutions file at the cycle root.
	resolutionsPath := filepath.Join(artifactDir, "review-resolutions.json")
	planPath := filepath.Join(artifactDir, "review-plan.md")
	plan := BuildAggregatedReviewCommentsPlan(staged, resolutionsPath)
	if err := os.WriteFile(planPath, []byte(plan), 0o644); err != nil {
		return nil, fmt.Errorf("review-comments loop: writing plan: %w", err)
	}

	// Mid-flight phase status surfaced to the TUI (mirrors phase-implement
	// / FR / rebase loop conventions).
	setCurrentPhaseStatus(cfg.FeatureStore, cfg.Feature.ID, "addressing-review-comments")
	defer setCurrentPhaseStatus(cfg.FeatureStore, cfg.Feature.ID, "")

	runImpl := RunImplementationLoop
	if cfg.RunImplementFn != nil {
		runImpl = cfg.RunImplementFn
	}

	// Build the inner ImplementConfig. RepoName intentionally LEFT EMPTY
	// — under the unified flow the main agent owns every Feature.Repos
	// worktree in the workspace; per-repo dispatch is handled by the
	// review-comments skill prompt via Task sub-agents.
	implCfg := ImplementConfig{
		Feature:                    cfg.Feature,
		FeatureStore:               cfg.FeatureStore,
		WorkDir:                    workspace.Cwd,
		PlanPath:                   planPath,
		MaxIterations:              cfg.MaxIterations,
		MaxConsecFails:             cfg.MaxConsecFails,
		MaxConsecNoProgress:        cfg.MaxConsecNoProgress,
		ExitCriteria:               reviewCommentsExitCriteria(resolutionsPath),
		Model:                      cfg.Model,
		ReviewModel:                cfg.ReviewModel,
		ArtifactDir:                artifactDir,
		StateDir:                   stateDir,
		RunDir:                     runDir,
		AdditionalDirs:             additionalDirsExcludingStateDir(workspace, stateDir),
		KBInfos:                    cfg.KBInfos,
		DesignArtifactPath:         cfg.Feature.DesignArtifactPath(),
		DangerouslySkipPermissions: cfg.DangerouslySkipPermissions,
		PermissionCache:            cfg.PermissionCache,
		BuildSession:               cfg.BuildSession,
		AskingClause:               cfg.AskingClause,
		EffortLevel:                cfg.EffortLevel,
		SkillsDir:                  cfg.SkillsDir,
		GuidelinesDir:              cfg.GuidelinesDir,
		Observer:                   cfg.Observer,
		CommandRunner:              cfg.CommandRunner,
		// Run the per-iteration review gate so the reviewer can attest
		// that every aggregated comment was either addressed (with code
		// changes) or dismissed (with reasoning), and the build still
		// passes.
		SkipIterationReview: false,
	}

	loopResult, runErr := runImpl(implCfg, sm)

	switch {
	case runErr != nil:
		_ = AtomicPhaseStamp(cfg.FeatureStore, AtomicPhaseStampInput{
			FeatureID: cfg.Feature.ID,
			Repos:     repoNames,
			Outcome:   PhaseOutcomeFailed,
			LastError: runErr.Error(),
		})
		_ = markActiveReviewCommentsCycleFailed(cfg.FeatureStore, cfg.Feature.ID, runErr.Error())
		return &ReviewCommentsLoopResult{
			FinalStatus: "failed",
			LastError:   runErr.Error(),
			Repos:       repoNames,
		}, runErr
	}

	if loopResult == nil {
		_ = AtomicPhaseStamp(cfg.FeatureStore, AtomicPhaseStampInput{
			FeatureID: cfg.Feature.ID,
			Repos:     repoNames,
			Outcome:   PhaseOutcomeFailed,
			LastError: "review-comments loop: nil result from implement loop",
		})
		_ = markActiveReviewCommentsCycleFailed(cfg.FeatureStore, cfg.Feature.ID, "nil result from implement loop")
		return &ReviewCommentsLoopResult{
			FinalStatus: "failed",
			LastError:   "nil result from implement loop",
			Repos:       repoNames,
		}, nil
	}

	switch loopResult.FinalStatus {
	case finalStatusReviewPassed:
		_ = AtomicPhaseStamp(cfg.FeatureStore, AtomicPhaseStampInput{
			FeatureID: cfg.Feature.ID,
			Repos:     repoNames,
			Outcome:   PhaseOutcomeReviewPassed,
		})
		_ = clearActiveCycle(cfg.FeatureStore, cfg.Feature.ID)
		return &ReviewCommentsLoopResult{
			FinalStatus: finalStatusReviewPassed,
			Iterations:  loopResult.Iterations,
			Repos:       repoNames,
		}, nil

	case "interrupted":
		// Persisted state preserved for restart. ActiveCycle stays at
		// "running" so the next start picks up the loop.
		return &ReviewCommentsLoopResult{
			FinalStatus: "interrupted",
			Iterations:  loopResult.Iterations,
			Repos:       repoNames,
		}, nil

	case "need_user_input":
		_ = AtomicPhaseStamp(cfg.FeatureStore, AtomicPhaseStampInput{
			FeatureID: cfg.Feature.ID,
			Repos:     repoNames,
			Outcome:   PhaseOutcomeNeedUserInput,
			GatePath:  loopResult.NeedUserInputPath,
		})
		return &ReviewCommentsLoopResult{
			FinalStatus:       "need_user_input",
			Iterations:        loopResult.Iterations,
			LastError:         loopResult.LastError,
			NeedUserInputPath: loopResult.NeedUserInputPath,
			Repos:             repoNames,
		}, nil

	default:
		// max_iterations / safety_rail / failed all map to a cycle
		// failure: every staged repo transitions to failed (LastError set); the
		// feature stays at its prior cycle entry but ActiveCycle.Status
		// flips to "failed" so the TUI surfaces the cycle row in red.
		_ = AtomicPhaseStamp(cfg.FeatureStore, AtomicPhaseStampInput{
			FeatureID: cfg.Feature.ID,
			Repos:     repoNames,
			Outcome:   PhaseOutcomeFailed,
			LastError: loopResult.LastError,
		})
		_ = markActiveReviewCommentsCycleFailed(cfg.FeatureStore, cfg.Feature.ID, loopResult.LastError)
		return &ReviewCommentsLoopResult{
			FinalStatus: loopResult.FinalStatus,
			Iterations:  loopResult.Iterations,
			LastError:   loopResult.LastError,
			Repos:       repoNames,
		}, nil
	}
}

// writeReviewCommentsTestingContract persists an empty plan-less contract.
// Agentico does not guess language-specific project commands.
func writeReviewCommentsTestingContract(path string, repos []string) error {
	contract := CompileTestingContractMultiRepo(MultiRepoContractInput{
		Repos:    repos,
		PlanLess: true,
		PlanPath: path,
	})
	if err := WriteTestingContract(path, contract); err != nil {
		return fmt.Errorf("writing review-comments testing contract: %w", err)
	}
	return nil
}

// reviewCommentsExitCriteria returns the cycle's exit criteria. The
// agent reads this from the implement prompt's `## Exit Criteria` section.
func reviewCommentsExitCriteria(resolutionsPath string) string {
	return "Every aggregated PR review comment is either addressed (with " +
		"corresponding code changes) or dismissed (with reasoning), captured " +
		"as one entry per comment in `" + resolutionsPath + "`. " +
		"test, and lint commands pass on every touched repo. Each touched " +
		"repo's feature branch is committed and pushed to its remote PR " +
		"branch (when the feature is publishable). No comment from the " +
		"aggregated work list remains without a resolution entry."
}

// BuildAggregatedReviewCommentsPlan renders a single plan markdown
// covering every staged repo's unaddressed PR comments. Each comment is
// tagged with `repo: <name>` so the agent can dispatch fixes via per-repo
// Task sub-agents while seeing the full cross-PR work list in one
// document.
//
// The resolutions JSON path is shared across every comment — one
// combined `review-resolutions.json` at the cycle root, not per-repo.
// The orchestrator-level reply path reads this file and dispatches per-PR
// replies via the existing Reviewer port.
func BuildAggregatedReviewCommentsPlan(targets []ReviewCommentsRepoTarget, resolutionsPath string) string {
	// Sort targets by repo name for deterministic output.
	sorted := append([]ReviewCommentsRepoTarget(nil), targets...)
	for i := range sorted {
		sorted[i].Comments = append([]ports.ReviewComment(nil), sorted[i].Comments...)
		git.SortReviewCommentsChronologically(sorted[i].Comments)
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].RepoName < sorted[j].RepoName })

	totalComments := 0
	for _, t := range sorted {
		totalComments += len(t.Comments)
	}

	var b strings.Builder
	b.WriteString("# Address Review Comments — Aggregated Plan\n\n")
	b.WriteString("## Overview\n\n")
	b.WriteString(fmt.Sprintf("This cycle aggregates %d unaddressed PR review comment(s) across %d repo(s) into one work list. The cross-PR aggregation is intentional: a single Claude session iterates over every comment, whether the fix is in one repo or several. Per-repo file edits should be dispatched via Task sub-agents prompt-scoped to that repo's worktree.\n\n", totalComments, len(sorted)))

	b.WriteString(standardImplementCycleCommunicationContract())

	b.WriteString("## Repos in this cycle\n\n")
	for _, t := range sorted {
		b.WriteString(fmt.Sprintf("- `%s` — PR %s — %d comment(s)", t.RepoName, t.PRURL, len(t.Comments)))
		if t.Mode != "" && t.Mode != "auto" {
			b.WriteString(fmt.Sprintf(" — mode: %s", t.Mode))
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")

	b.WriteString("## Mode: Agent Decides\n\n")
	b.WriteString("For each review comment listed below, decide whether to:\n")
	b.WriteString("- **Address it**: Make code changes to resolve the feedback.\n")
	b.WriteString("- **Dismiss it**: If the comment is already handled, not applicable, or the current approach is better — explain your reasoning.\n\n")
	b.WriteString("Each comment is tagged with `**Repo:** <name>` so you can route the fix to the right worktree. Dispatch per-repo edits via Task sub-agents named for that repo; cross-repo discussion threads (e.g. a comment on repo A asking about repo B's behavior) may require reading both worktrees before deciding.\n\n")

	// Per-repo sections.
	commentIdx := 0
	for _, t := range sorted {
		b.WriteString(fmt.Sprintf("---\n\n## Repo: `%s`\n\n", t.RepoName))
		b.WriteString(fmt.Sprintf("**Repo:** %s\n\n", t.RepoName))
		b.WriteString(fmt.Sprintf("**PR:** %s\n\n", t.PRURL))
		b.WriteString(fmt.Sprintf("### Review Comments (%d)\n\n", len(t.Comments)))
		for _, c := range t.Comments {
			commentIdx++
			b.WriteString(fmt.Sprintf("#### Comment %d (ID: %d) — `repo: %s`\n", commentIdx, c.ID, t.RepoName))
			b.WriteString(fmt.Sprintf("**File**: `%s`", c.Path))
			if c.Line > 0 {
				b.WriteString(fmt.Sprintf(":%d", c.Line))
			}
			b.WriteString("\n")
			b.WriteString(fmt.Sprintf("**Author**: @%s\n", c.User.Login))
			if c.DiffHunk != "" {
				b.WriteString(fmt.Sprintf("**Context**:\n```diff\n%s\n```\n", c.DiffHunk))
			}
			b.WriteString(fmt.Sprintf("**Comment**:\n> %s\n\n", strings.ReplaceAll(c.Body, "\n", "\n> ")))
		}
	}

	b.WriteString("---\n\n## Resolution Tracking\n\n")
	b.WriteString(fmt.Sprintf("After addressing or deciding on each comment across every repo above, write ONE combined JSON file at:\n`%s`\n\n", resolutionsPath))
	b.WriteString("Format:\n```json\n[\n")
	b.WriteString(`  {"comment_id": 123, "disposition": "addressed", "description": "Fixed error handling"},`)
	b.WriteString("\n")
	b.WriteString(`  {"comment_id": 456, "disposition": "dismissed", "description": "Already handled by existing validation"}`)
	b.WriteString("\n]\n```\n\n")
	b.WriteString("Every comment listed in the per-repo sections above MUST appear exactly once in this file. The orchestrator reads this file post-cycle to dispatch per-PR comment replies and mark addressed comment IDs.\n\n")

	b.WriteString("## Success Criteria\n\n")
	b.WriteString("- Every aggregated comment has a corresponding entry in the resolutions JSON.\n")
	b.WriteString("- Addressed comments have real code changes in the named repo.\n")
	b.WriteString("- Each touched repo's branch is committed and pushed to its PR branch.\n\n")

	b.WriteString("## Important Notes\n\n")
	b.WriteString("- Do NOT create new branches. Work on each repo's existing PR branch.\n")
	b.WriteString("- Do NOT touch any repo NOT listed in the per-repo sections above.\n")
	b.WriteString("- Make targeted changes that directly address the feedback. Don't refactor neighboring code unless the comment requested it.\n")
	b.WriteString("- Cross-repo edits are allowed when a comment in repo A genuinely requires a change in repo B — explain the reasoning in the resolution entry.\n")

	return b.String()
}

// markActiveReviewCommentsCycleFailed flips Feature.ActiveCycle.Status to
// "failed" so the TUI surfaces the cycle row in red. ActiveCycle stays
// populated so the user can see which cycle failed and trigger a restart.
func markActiveReviewCommentsCycleFailed(store ports.FeatureStore, featureID, lastError string) error {
	if store == nil {
		return fmt.Errorf("mark active review-comments cycle failed: store is nil")
	}
	return store.Modify(featureID, func(f *feature.Feature) error {
		if f.ActiveCycle == nil {
			f.ActiveCycle = &feature.CycleState{Type: feature.CycleReviewComments}
		}
		f.ActiveCycle.Status = feature.RepoCycleFailed
		if lastError != "" {
			f.ActiveCycle.LastError = lastError
		}
		return nil
	})
}
