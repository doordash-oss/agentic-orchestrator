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

// Package agent — orchestrator.go is the unified-flow phase implement and
// Final Review entry point under SchemaVersionCurrent = 4. The legacy
// per-stage goroutine fan-out (per-repo Implement loops sequenced via
// ExecutionPlan stages) is gone. The new shape:
//
//   - RunMultiRepoOrchestrator (and its thin alias RunMultiRepoImplementation)
//     invokes RunPhaseImplementLoop once per call. The loop owns the entire
//     phase across every repo declared in Feature.Repos.
//   - RunMultiRepoFinalReview invokes RunFeatureFinalReviewLoop, the unified
//     feature-level FR session — one Claude session reads the cumulative
//     diff across every Feature.Repos worktree.
package agent

import (
	"sort"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// OrchestratorConfig holds configuration for the multi-repo orchestrator.
// Under SchemaVersionCurrent = 4 the per-stage ExecutionPlan and resume
// metadata are gone — the unified phase-implement loop derives its repo set
// from PhaseScope (per-Task `**Repo:** <name>` tags) and recovers from
// crashes by re-running the interrupted unit from scratch.
type OrchestratorConfig struct {
	Feature      *feature.Feature
	FeatureStore ports.FeatureStore
	PlanPath     string
	StateDir     string
	Config       *config.Config

	// Resolved model and loop-limit values. These should be populated by the
	// caller (RunMultiRepoImplementation) using the same resolution logic as
	// the single-repo path (feature-level overrides → config defaults → fallbacks).
	Model                string
	ReviewModel          string
	ResolveSessionConfig func(llm.PhaseRole) (SessionRuntimeConfig, error)
	MaxIterations        int
	MaxConsecFails       int
	MaxConsecNoProgress  int

	KBInfos []KBInfo

	// RunImplementFn, when set, overrides the default RunImplementationLoop
	// call. This allows tests to capture the ImplementConfig without running
	// real sessions.
	RunImplementFn func(ImplementConfig, ports.SessionManager) (*LoopResult, error)

	// RunFinalReviewFn, when set, overrides the default Final Review dispatch
	// for the feature-level FR loop. Production wires this to nil so the
	// default RunFeatureFinalReviewLoop fires; tests inject a stub that
	// captures the OrchestratorConfig without launching real sessions.
	RunFinalReviewFn func(OrchestratorConfig, ports.SessionManager) (*FeatureFinalReviewResult, error)

	DangerouslySkipPermissions bool
	PermissionCache            *permission.Cache
	CommandRunner              ports.CommandRunner

	// BuildSession creates CLI command args, env vars, and session opts
	// by routing through the provider registry. Passed through to ImplementConfig.
	BuildSession func(BuildSessionOpts) ([]string, []string, *ports.SessionOpts, error)

	// AskingClause is the pre-resolved "Asking Questions" prompt section
	// from the PromptAdapter for the implementation model. Passed through
	// to ImplementConfig.
	AskingClause string

	// EffortLevel is the pipeline-driven effort level passed to providers.
	EffortLevel llm.EffortLevel

	// EffectiveEffort is the resolved provider-safe effort level for this
	// launch. When non-empty, it overrides EffortLevel in BuildSessionOpts so
	// the provider command receives the capability-resolved level. Empty means
	// no effort resolution was performed and EffortLevel is used directly.
	// In the implementation path this carries the Implementation-role effort;
	// in the final review path it carries the Review-role effort. Prefer the
	// role-specific fields below for new code.
	EffectiveEffort llm.EffortLevel
	// EffortSource records whether EffectiveEffort was derived from the
	// pipeline (auto) or an explicit user configuration (explicit).
	EffortSource llm.EffortSource

	// ImplEffectiveEffort is the resolved Implementation-role effort for fix
	// agents and implementation workers. When non-empty, it overrides
	// EffortLevel in the fix agent's BuildSessionOpts. Empty falls back to
	// EffectiveEffort then EffortLevel.
	ImplEffectiveEffort llm.EffortLevel
	// ImplEffortSource records whether ImplEffectiveEffort was auto-derived
	// or explicitly configured.
	ImplEffortSource llm.EffortSource

	// ReviewEffectiveEffort is the resolved Review-role effort for review
	// axes and validators. When non-empty, it overrides EffortLevel in the
	// review helper's BuildSessionOpts. Empty falls back to EffectiveEffort
	// then EffortLevel.
	ReviewEffectiveEffort llm.EffortLevel
	// ReviewEffortSource records whether ReviewEffectiveEffort was
	// auto-derived or explicitly configured.
	ReviewEffortSource llm.EffortSource

	// SkillsDir is the path to the reconciled skills directory on disk.
	SkillsDir string

	// GuidelinesDir is the path to the reconciled guidelines directory on disk.
	GuidelinesDir string

	// Observer is the observability facade for lifecycle events. Nil = no-op.
	Observer *observe.Observer

	// OnVerificationProgress is called after each persisted harness
	// verification status transition so API clients can refresh live state.
	OnVerificationProgress func(featureID string)

	// RoundCommitHook, when non-nil, is invoked by the phase-implement and
	// final-review loops once per implementation/fix round, right after the
	// round's session ends and before the review gate. The orchestrator
	// layer owns the git commit; nil disables per-round commits.
	RoundCommitHook RoundCommitHook
}

func resolveOrchestratorSessionConfig(cfg OrchestratorConfig, role llm.PhaseRole) (SessionRuntimeConfig, error) {
	if cfg.ResolveSessionConfig != nil {
		return cfg.ResolveSessionConfig(role)
	}
	if role == llm.PhaseReview {
		return SessionRuntimeConfig{
			Model:           cfg.ReviewModel,
			EffectiveEffort: cfg.ReviewEffectiveEffort,
			EffortSource:    cfg.ReviewEffortSource,
			AskingClause:    cfg.AskingClause,
		}, nil
	}
	return SessionRuntimeConfig{
		Model:           cfg.Model,
		EffectiveEffort: cfg.ImplEffectiveEffort,
		EffortSource:    cfg.ImplEffortSource,
		AskingClause:    cfg.AskingClause,
	}, nil
}

// OrchestratorResult is the aggregate outcome of multi-repo implementation.
type OrchestratorResult struct {
	FinalStatus  string            // "all_passed" | "awaiting_final_review" | "failed" | "need_user_input" | "plan_revision_required" | "interrupted"
	RepoStatuses map[string]string // per-repo inner loop FinalStatus (e.g., "max_iterations")
	FailedRepos  []string
	// PausedRepos lists repos that ended this run paused on a need-user-input
	// gate. Under the unified flow the gate is feature-scoped, so PausedRepos
	// carries the phase-declared repo subset when FinalStatus == "need_user_input".
	PausedRepos []string
	// NeedUserInputPath is the absolute path to the persisted gate artifact
	// when FinalStatus == "need_user_input". The orchestrator stores this on
	// Feature.PendingNeedUserInputPath so the resume handler
	// can read the questionnaire and answers.
	NeedUserInputPath string
	LastError         string
	// PlanRevisionFeedback carries phase-plan repair requirements when
	// FinalStatus == "plan_revision_required". Final Review must run its fix
	// leg instead of returning this status.
	PlanRevisionFeedback string
}

// RunMultiRepoOrchestrator drives the unified phase-implement loop. Under
// SchemaVersionCurrent = 4 the entire phase runs as a single Claude session
// owning every repo in Feature.Repos; per-stage goroutine fan-out is gone.
//
// The orchestrator deliberately ends at "every phase-declared repo at
// Touched (staged for FR)" — Final Review fires once at end of feature
// via RunMultiRepoFinalReview, not per-phase.
//
// Returns "awaiting_final_review" when the implement loop succeeds,
// "failed" / "need_user_input" / "interrupted" otherwise.
func RunMultiRepoOrchestrator(cfg OrchestratorConfig, sm ports.SessionManager) (*OrchestratorResult, error) {
	implResult, err := RunPhaseImplementLoop(cfg, sm)
	if err != nil {
		return nil, err
	}
	if implResult.FinalStatus != finalStatusReviewPassed {
		return phaseLoopResultToOrchestratorResult(cfg, implResult), nil
	}
	return &OrchestratorResult{FinalStatus: "awaiting_final_review"}, nil
}

// RunMultiRepoImplementation runs the unified phase-implement loop without
// Final Review. Used by completion.go for per-phase implementation under
// the "FR fires once at feature end" design.
func RunMultiRepoImplementation(cfg OrchestratorConfig, sm ports.SessionManager) (*OrchestratorResult, error) {
	return RunMultiRepoOrchestrator(cfg, sm)
}

// phaseLoopResultToOrchestratorResult translates the unified
// PhaseImplementLoopResult into the legacy OrchestratorResult shape callers
// expect.
func phaseLoopResultToOrchestratorResult(_ OrchestratorConfig, r *PhaseImplementLoopResult) *OrchestratorResult {
	switch r.FinalStatus {
	case "interrupted":
		return &OrchestratorResult{FinalStatus: "interrupted"}
	case "need_user_input":
		return &OrchestratorResult{
			FinalStatus:       "need_user_input",
			PausedRepos:       append([]string(nil), r.PhaseRepos...),
			NeedUserInputPath: r.NeedUserInputPath,
			LastError:         r.LastError,
		}
	case "plan_revision_required":
		return &OrchestratorResult{
			FinalStatus:          "plan_revision_required",
			RepoStatuses:         phaseRepoStatuses(r.PhaseRepos, r.FinalStatus),
			PlanRevisionFeedback: r.PlanRevisionFeedback,
		}
	default:
		return &OrchestratorResult{
			FinalStatus:  "failed",
			RepoStatuses: phaseRepoStatuses(r.PhaseRepos, r.FinalStatus),
			FailedRepos:  append([]string(nil), r.PhaseRepos...),
			LastError:    r.LastError,
		}
	}
}

func phaseRepoStatuses(repos []string, status string) map[string]string {
	statuses := make(map[string]string, len(repos))
	for _, repo := range repos {
		statuses[repo] = status
	}
	return statuses
}

// RunMultiRepoFinalReview runs the unified feature-level Final Review pass.
// Called by completion.go after the last roadmap-phase implement returns
// "awaiting_final_review".
//
// Cwd at the active run dir; --add-dir for every Feature.Repos
// worktree. The reviewer reads the cumulative diff across all repos and
// emits one APPROVED / CHANGES_REQUESTED verdict. FR atomicity: every
// repo at Touched (staged for FR) transitions to review_passed
// (success) or failed (LastError set) (max_iterations / safety_rail) together.
func RunMultiRepoFinalReview(cfg OrchestratorConfig, sm ports.SessionManager) (*OrchestratorResult, error) {
	runFR := RunFeatureFinalReviewLoop
	if cfg.RunFinalReviewFn != nil {
		runFR = cfg.RunFinalReviewFn
	}

	frResult, err := runFR(cfg, sm)
	if err != nil {
		// FR dispatch error — every staged repo is already stamped
		// failed (LastError set) by RunFeatureFinalReviewLoop's atomic stamp.
		return &OrchestratorResult{
			FinalStatus: "failed",
			FailedRepos: append([]string(nil), frResultRepos(frResult)...),
			LastError:   err.Error(),
		}, nil
	}

	switch frResult.FinalStatus {
	case finalStatusReviewPassed:
		return &OrchestratorResult{FinalStatus: "all_passed"}, nil
	case "interrupted":
		return &OrchestratorResult{FinalStatus: "interrupted"}, nil
	case "plan_revision_required":
		// Final Review must keep fixes inside the final-review loop. Older
		// injected/fake review runners may still return this status; surface
		// it as a failure instead of reopening planning.
		return &OrchestratorResult{
			FinalStatus:  "failed",
			RepoStatuses: finalReviewRepoStatuses(frResult),
			FailedRepos:  append([]string(nil), frResultRepos(frResult)...),
			LastError:    "final review requested unsupported phase-plan revision",
		}, nil
	default:
		return &OrchestratorResult{
			FinalStatus:  "failed",
			RepoStatuses: finalReviewRepoStatuses(frResult),
			FailedRepos:  append([]string(nil), frResultRepos(frResult)...),
			LastError:    frResult.LastError,
		}, nil
	}
}

func finalReviewRepoStatuses(result *FeatureFinalReviewResult) map[string]string {
	repos := frResultRepos(result)
	statuses := make(map[string]string, len(repos))
	if result == nil {
		return statuses
	}
	for _, repo := range repos {
		statuses[repo] = result.FinalStatus
	}
	return statuses
}

// frResultRepos extracts the staged repo set from a FeatureFinalReviewResult,
// safely handling a nil result.
func frResultRepos(r *FeatureFinalReviewResult) []string {
	if r == nil {
		return nil
	}
	out := append([]string(nil), r.Repos...)
	sort.Strings(out)
	return out
}

// findRepo looks up a FeatureRepo by name.
func findRepo(f *feature.Feature, name string) *feature.FeatureRepo {
	for i := range f.Repos {
		if f.Repos[i].Name == name {
			return &f.Repos[i]
		}
	}
	return nil
}
