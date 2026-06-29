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
//
// Cycle paths (rebase / review-comments / refactor / tweak) still consult
// some of this package's helpers. They will migrate to their own unified
// loop functions in slices 4-7.
package agent

import (
	"fmt"
	"path/filepath"
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
	Model               string
	ReviewModel         string
	MaxIterations       int
	MaxConsecFails      int
	MaxConsecNoProgress int

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

	// BuildSession creates CLI command args, env vars, and session opts
	// by routing through the provider registry. Passed through to ImplementConfig.
	BuildSession func(BuildSessionOpts) ([]string, []string, *ports.SessionOpts, error)

	// AskingClause is the pre-resolved "Asking Questions" prompt section
	// from the PromptAdapter for the implementation model. Passed through
	// to ImplementConfig.
	AskingClause string

	// EffortLevel is the pipeline-driven effort level passed to providers.
	EffortLevel llm.EffortLevel

	// SkillsDir is the path to the reconciled skills directory on disk.
	SkillsDir string

	// GuidelinesDir is the path to the reconciled guidelines directory on disk.
	GuidelinesDir string

	// FinishOrViolateNudge arms the finish-or-violate auto-continuation retry
	// for the feature-level Final Review sessions (review + fix legs). Resolved
	// per-model from the provider capability, so only capability-positive
	// providers opt in.
	FinishOrViolateNudge bool

	// Observer is the observability facade for lifecycle events. Nil = no-op.
	Observer *observe.Observer
}

// OrchestratorResult is the aggregate outcome of multi-repo implementation.
type OrchestratorResult struct {
	FinalStatus  string            // "all_passed" | "awaiting_final_review" | "failed" | "need_user_input" | "plan_revision_required" | "interrupted"
	RepoStatuses map[string]string // per-repo inner loop FinalStatus (e.g., "max_iterations")
	FailedRepos  []string
	// PausedRepos lists repos that ended this cycle paused on a need-user-input
	// gate. Under the unified flow the gate is feature-scoped, so PausedRepos
	// carries the phase-declared repo subset when FinalStatus == "need_user_input".
	PausedRepos []string
	// NeedUserInputPath is the absolute path to the persisted gate artifact
	// when FinalStatus == "need_user_input". The orchestrator stores this on
	// Feature.PendingNeedUserInputPath so the resume/abort decision handler
	// can read the questionnaire and answers.
	NeedUserInputPath string
	LastError         string
	// PlanRevisionFeedback carries implementation-review missing-evidence
	// requirements when FinalStatus == "plan_revision_required". Final Review
	// must run its fix leg instead of returning this status.
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
	if implResult.FinalStatus != "review_passed" {
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
// Cwd at the feature state dir; --add-dir for every Feature.Repos
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
	case "review_passed":
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

// resolveImplementArtifactDirForRepo returns the per-iteration Implement
// artifact directory for a feature/repo within the active run. The
// `<repoName>` segment is preserved so the per-repo cycle paths
// (rebase / refactor / review-comments) keep their existing layout until
// slices 4-7 migrate them. The unified phase-implement loop emits artifacts
// at the phase level (no per-repo subdir) via resolvePhaseArtifactDir.
func resolveImplementArtifactDirForRepo(f *feature.Feature, runDir, repoName string) string {
	if cyclePrefix := f.CyclePrefix(); cyclePrefix != "" {
		return filepath.Join(runDir, cyclePrefix, "implement", repoName)
	}
	base := filepath.Join(runDir, f.RefactorPrefix())
	if f.CurrentRoadmapPhase > 0 {
		return filepath.Join(base, fmt.Sprintf("phase-%02d", f.CurrentRoadmapPhase), "implement", repoName)
	}
	return filepath.Join(base, "implement", repoName)
}
