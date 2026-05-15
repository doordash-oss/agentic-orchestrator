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

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// StartMultiRepoImplementation is the single entry point for launching the
// unified phase-implement loop. It validates feature state, resolves the plan
// path, runs PhaseScope to derive the phase-declared repo subset, clears any
// stale per-repo error state on that subset (so the loop starts fresh
// against known state), invokes the engine via the runMultiRepoImplFn seam,
// and spawns a goroutine that routes cycle-terminal results back through
// HandlePhaseCompletion. Crash recovery re-runs the interrupted unit from
// scratch with a fresh Claude session; durable state on disk is the resume
// scaffolding.
func (o *Orchestrator) StartMultiRepoImplementation(featureID string) error {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return fmt.Errorf("load feature: %w", err)
	}
	if f.Status != feature.StatusImplementing {
		return fmt.Errorf("feature %s not in implementing state (status=%s)", featureID, f.Status)
	}

	planPath := ""
	if f.Artifacts != nil {
		planPath = f.Artifacts["plan"]
	}
	if planPath == "" {
		return errors.New("no plan artifact; cannot start multi-repo implementation")
	}

	// Run PhaseScope to derive the phase-declared repo subset and validate
	// the plan structure. PhaseScope replaces LoadExecutionPlan +
	// ParseExecutionOrder + ValidateExecutionOrder. Soft-fall-back to every
	// Feature.Repos entry when the plan has no ## Tasks section yet
	// (placeholder plans land here during early-phase wiring; the actual
	// phase loop revalidates).
	repoNames := repoSubsetForPhaseStart(f, planPath)

	// Clear any stale per-repo error state on the phase-declared subset so
	// the unified loop starts against known state. Touched flags are
	// monotonic and intentionally preserved across phase entries.
	if err := o.deps.Lifecycle.RetryPhase(featureID, repoNames); err != nil {
		return fmt.Errorf("reset phase repos: %w", err)
	}

	kbInfos := o.computeKBInfos(f)

	runFn := o.runMultiRepoImplFn
	if runFn == nil {
		return errors.New("runMultiRepoImplFn not configured")
	}
	resultCh, err := runFn(f, planPath, kbInfos...)
	if err != nil {
		return fmt.Errorf("run multi-repo implementation: %w", err)
	}

	go o.dispatchMultiRepoResults(featureID, resultCh)
	return nil
}

// dispatchMultiRepoResults reads the engine's result channel and routes
// terminal values to HandlePhaseCompletion. Intermediate observations are
// ignored. The loop exits after the first terminal value because the
// production engine sends exactly one value on a buffered-1 channel and never
// closes it.
func (o *Orchestrator) dispatchMultiRepoResults(featureID string, resultCh <-chan *agent.OrchestratorResult) {
	for res := range resultCh {
		if res == nil {
			continue
		}
		switch res.FinalStatus {
		case "all_passed", "awaiting_final_review", "failed", "need_user_input", "plan_revision_required", "interrupted":
			if err := o.HandlePhaseCompletion(featureID, PhaseCompletionInput{
				Phase:           feature.PhaseImplement,
				MultiRepoResult: res,
			}); err != nil {
				o.surfaceDispatchCompletionError(featureID, err)
			}
			return
		}
	}
}

// repoSubsetForPhaseStart returns the phase-declared repo subset to reset
// before launching the unified phase-implement loop. Tries PhaseScope first;
// falls back to every Feature.Repos entry on validation failure (typical for
// placeholder plans during early-phase wiring). The phase-implement loop
// revalidates via PhaseScope before launching its first iteration.
func repoSubsetForPhaseStart(f *feature.Feature, planPath string) []string {
	if scope, err := agent.PhaseScope(f, planPath); err == nil && scope.ScopeOK() && len(scope.Repos) > 0 {
		return scope.Repos
	}
	repos := make([]string, 0, len(f.Repos))
	for _, r := range f.Repos {
		repos = append(repos, r.Name)
	}
	return repos
}

// surfaceDispatchCompletionError is invoked from the multi-repo dispatch
// goroutine when HandlePhaseCompletion returns an error. It tries to
// transition the feature to Failed via markFailedWithEvent (which also
// emits FeatureFailed). If that transition itself errors, it falls back to
// emitting FeatureFailed directly so observers still see the terminal
// signal. Either way, the goroutine exits — there is no retry loop.
func (o *Orchestrator) surfaceDispatchCompletionError(featureID string, cause error) {
	// User-initiated Stop during Final Review surfaces as
	// errFinalReviewInterrupted. The InterruptFeature path already
	// transitioned the feature to StatusInterrupted and emitted
	// FeatureInterrupted; overwriting that with a Failed transition would
	// surface a spurious "Failure Info — final review interrupted" panel
	// for what is a clean user-driven stop. onMultiReposPassed already
	// short-circuits when it sees the sentinel, so reaching this point
	// with the sentinel as cause is defensive — but cheap to guard.
	if errors.Is(cause, errFinalReviewInterrupted) {
		return
	}
	var publishConflict *PublishConflictError
	if errors.As(cause, &publishConflict) {
		// Publish already emitted PublishCompleted with the structured conflict;
		// the TUI owns routing that into the rebase-resolution cycle.
		return
	}
	errMsg := fmt.Sprintf("handle phase completion: %v", cause)
	if markErr := o.markFailedWithEvent(featureID, feature.FailureInfrastructure, errMsg); markErr != nil {
		o.emitEventBlocking(ports.Event{
			Type:      ports.FeatureFailed,
			FeatureID: featureID,
			Message:   errMsg,
			Error:     cause,
		})
	}
}
