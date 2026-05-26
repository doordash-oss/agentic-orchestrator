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

// Package orchestrator — need_user_input.go owns the need-user-input gate
// lifecycle for feature-scoped (single-repo), repo-scoped (multi-repo), and
// cycle-scoped (post-publish) flows. Cycle-scoped pauses keep the parent
// feature in StatusPublished and only mutate the affected RepoCycleState.
package orchestrator

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// onSingleRepoNeedUserInput converts an implement loop result with
// FinalStatus == "need_user_input" into a paused gate-entry transition.
// Persists the gate path on the run, transitions the feature into
// StatusNeedUserInput, and emits a NeedUserInputRequired event. The
// caller (onSingleRepoImplementDone) MUST NOT emit PhaseCompleted —
// the implement phase is paused, not done.
func (o *Orchestrator) onSingleRepoNeedUserInput(featureID string, result *agent.LoopResult) error {
	summary := strings.TrimSpace(result.LastError)
	if summary == "" {
		summary = "implementation iteration emitted NEED_USER_INPUT without a description"
	}

	if err := o.deps.Store.Modify(featureID, func(f *feature.Feature) error {
		f.PendingNeedUserInputPath = result.NeedUserInputPath
		if f.Status == feature.StatusImplementing {
			if err := f.Transition(feature.StatusNeedUserInput); err != nil {
				return fmt.Errorf("transition to StatusNeedUserInput: %w", err)
			}
		}
		return nil
	}); err != nil {
		return err
	}

	_ = o.deps.Lifecycle.ClearAddressingReviews(featureID)

	o.emitEventBlocking(ports.Event{
		Type:      ports.NeedUserInputRequired,
		FeatureID: featureID,
		Phase:     feature.PhaseImplement,
		Message:   summary,
	})
	return nil
}

// HandleNeedUserInputDecision processes a gate decision ("resume" or "abort").
// Routing:
//   - non-empty RepoName + matching RepoCycleState[RepoName].Status ==
//     RepoCycleNeedUserInput → cycle-scoped gate (post-publish).
//   - otherwise → feature-scoped gate. The TUI may forward a stale RepoName
//     from its repo-tab focus context for a phase-implement pause; the
//     feature-level handler ignores it. Phase-implement NEED_USER_INPUT is
//     always feature-scoped through Feature.PendingNeedUserInputPath.
//
// Feature-scoped resume re-dispatches the entire implement phase;
// cycle-scoped resume relaunches only the affected paused cycle through its
// original starter. Abort routes through the shared markFailedWithEvent
// path (feature-scoped) or FailRepoCycle (cycle-scoped); the parent feature
// stays Published when a cycle is aborted.
func (o *Orchestrator) HandleNeedUserInputDecision(featureID string, d NeedUserInputDecision) error {
	if d.RepoName != "" {
		f, err := o.deps.Lifecycle.Get(featureID)
		if err == nil && f != nil {
			if rc, ok := f.RepoCycles[d.RepoName]; ok && rc != nil &&
				rc.Status == feature.RepoCycleNeedUserInput {
				return o.handleRepoCycleNeedUserInputDecision(featureID, d)
			}
		}
	}
	return o.handleFeatureNeedUserInputDecision(featureID, d)
}

// onRepoCycleNeedUserInput converts a cycle implementation loop result with
// FinalStatus == "need_user_input" into a paused cycle gate. Persists the
// gate path / iteration / summary on the affected RepoCycleState, keeps the
// parent feature in StatusPublished, and emits NeedUserInputRequired.
func (o *Orchestrator) onRepoCycleNeedUserInput(
	featureID, repoName string,
	cycleType feature.RepoCycleType,
	result *agent.LoopResult,
) error {
	if result == nil {
		return errors.New("nil loop result for cycle gate entry")
	}
	summary := strings.TrimSpace(result.LastError)
	if summary == "" {
		summary = "post-publish cycle iteration emitted NEED_USER_INPUT without a description"
	}
	if err := o.deps.Store.Modify(featureID, func(f *feature.Feature) error {
		if f.RepoCycles == nil {
			f.RepoCycles = make(map[string]*feature.RepoCycleState)
		}
		rc, ok := f.RepoCycles[repoName]
		if !ok || rc == nil {
			rc = &feature.RepoCycleState{Type: cycleType}
			f.RepoCycles[repoName] = rc
		} else if cycleType != "" && rc.Type == "" {
			rc.Type = cycleType
		}
		rc.Status = feature.RepoCycleNeedUserInput
		rc.Iteration = result.Iterations
		rc.LastError = summary
		rc.PendingNeedUserInputPath = result.NeedUserInputPath
		return nil
	}); err != nil {
		return err
	}

	prefix := summary
	if repoName != "" {
		prefix = fmt.Sprintf("[%s] %s", repoName, summary)
	}
	o.emitEventBlocking(ports.Event{
		Type:      ports.NeedUserInputRequired,
		FeatureID: featureID,
		Phase:     feature.PhaseImplement,
		Message:   prefix,
	})
	return nil
}

func (o *Orchestrator) recordRepoCycleNeedUserInput(
	featureID, repoName string,
	cycleType feature.RepoCycleType,
	gate *agent.LoopResult,
) {
	if err := o.onRepoCycleNeedUserInput(featureID, repoName, cycleType, gate); err != nil {
		o.failRepoCycleGatePersistence(featureID, repoName,
			fmt.Errorf("%s: persist need-user-input gate for repo %q: %w", cycleType, repoName, err))
	}
}

func (o *Orchestrator) failRepoCycleGatePersistence(featureID, repoName string, cause error) {
	if cause == nil {
		return
	}
	msg := cause.Error()
	if err := o.deps.Lifecycle.FailRepoCycle(featureID, repoName, msg); err != nil {
		o.emitEventBlocking(ports.Event{
			Type:      ports.PhaseCompleted,
			FeatureID: featureID,
			Phase:     feature.PhaseImplement,
			Error:     fmt.Errorf("%s (also failed to mark repo cycle failed: %w)", msg, err),
			Message:   msg,
		})
	}
}

// handleRepoCycleNeedUserInputDecision handles a cycle-scoped need-user-input
// decision. Resume validates answers, clears the paused gate fields while
// preserving cycle Count / Type / artifact anchors, then dispatches to the
// cycle-type-specific restart seam through restartPausedRepoCycle. Abort
// fails only the affected cycle (FailRepoCycle clears the gate fields and
// the refactor prompt when applicable); the parent feature stays Published
// and sibling cycles keep running.
func (o *Orchestrator) handleRepoCycleNeedUserInputDecision(featureID string, d NeedUserInputDecision) error {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return fmt.Errorf("load feature: %w", err)
	}
	rc, ok := f.RepoCycles[d.RepoName]
	if !ok || rc == nil {
		return fmt.Errorf("repo %q not found in repo_cycles for feature %s", d.RepoName, featureID)
	}
	if rc.Status != feature.RepoCycleNeedUserInput {
		return fmt.Errorf("cycle on repo %q is not paused (status=%s)", d.RepoName, rc.Status)
	}
	gatePath := rc.PendingNeedUserInputPath
	if gatePath == "" {
		return fmt.Errorf("no pending need-user-input gate path on cycle for repo %q", d.RepoName)
	}

	switch d.Decision {
	case "resume":
		rec, err := agent.ReadNeedUserInputRecord(gatePath)
		if err != nil {
			return fmt.Errorf("read gate artifact: %w", err)
		}
		if !rec.AllAnswered() {
			return errors.New("cannot resume: every question must have a non-empty answer before resume")
		}
		resumeRepos := []string{d.RepoName}
		if rc.Type == feature.CycleRebase {
			resumeRepos = repoCycleGateRepos(f, feature.CycleRebase, gatePath, d.RepoName)
		}
		// Clear the paused-gate fields but preserve Count, PlanPath, and
		// Type so the restart seam reuses the existing cycle record. Set
		// status back to Running so HasActiveRepoCycles still treats the
		// cycle as in-flight while the restart launches.
		if err := o.deps.Store.Modify(featureID, func(ff *feature.Feature) error {
			for _, name := range resumeRepos {
				cycle, ok := ff.RepoCycles[name]
				if !ok || cycle == nil {
					return fmt.Errorf("repo %q vanished from repo_cycles mid-resume", name)
				}
				cycle.Status = feature.RepoCycleRunning
				cycle.PendingNeedUserInputPath = ""
				cycle.LastError = ""
			}
			if ff.PendingNeedUserInputPath == gatePath {
				ff.PendingNeedUserInputPath = ""
			}
			return nil
		}); err != nil {
			return fmt.Errorf("clear cycle gate: %w", err)
		}
		if err := o.restartPausedRepoCycle(featureID, d.RepoName); err != nil {
			// Roll back: restore the paused gate so the user can retry or
			// abort from the same paused state.
			if rbErr := o.deps.Store.Modify(featureID, func(ff *feature.Feature) error {
				for _, name := range resumeRepos {
					cycle, ok := ff.RepoCycles[name]
					if !ok || cycle == nil {
						return fmt.Errorf("repo %q vanished during rollback", name)
					}
					cycle.Status = feature.RepoCycleNeedUserInput
					cycle.PendingNeedUserInputPath = gatePath
				}
				return nil
			}); rbErr != nil {
				return fmt.Errorf("relaunch cycle: %w (rollback to gate also failed: %v)", err, rbErr)
			}
			return fmt.Errorf("relaunch cycle: %w", err)
		}
		return nil
	case "abort":
		summary := ""
		if rec, err := agent.ReadNeedUserInputRecord(gatePath); err == nil {
			summary = rec.Summary
		}
		if summary == "" {
			summary = fmt.Sprintf("user aborted at need-user-input gate for cycle on repo %s", d.RepoName)
		}
		abortRepos := []string{d.RepoName}
		if rc.Type == feature.CycleRebase {
			abortRepos = repoCycleGateRepos(f, feature.CycleRebase, gatePath, d.RepoName)
		}
		// FailRepoCycle clears the gate path and, for refactor cycles, the
		// feature-level RefactorPrompt. The parent feature stays Published.
		for _, name := range abortRepos {
			if err := o.deps.Lifecycle.FailRepoCycle(featureID, name, summary); err != nil {
				return err
			}
		}
		_ = o.deps.Store.Modify(featureID, func(ff *feature.Feature) error {
			if ff.PendingNeedUserInputPath == gatePath {
				ff.PendingNeedUserInputPath = ""
			}
			return nil
		})
		return nil
	default:
		return fmt.Errorf("unknown need-user-input decision %q (want resume|abort)", d.Decision)
	}
}

func repoCycleGateRepos(f *feature.Feature, cycleType feature.RepoCycleType, gatePath, fallback string) []string {
	var names []string
	if f != nil {
		for name, rc := range f.RepoCycles {
			if rc == nil ||
				rc.Type != cycleType ||
				rc.Status != feature.RepoCycleNeedUserInput ||
				rc.PendingNeedUserInputPath != gatePath {
				continue
			}
			names = append(names, name)
		}
	}
	if len(names) == 0 && fallback != "" {
		names = append(names, fallback)
	}
	sort.Strings(names)
	return names
}

// handleFeatureNeedUserInputDecision implements the original single-repo /
// feature-scoped gate flow. Resume validates answers, clears the pending
// gate pointer, transitions the feature back to StatusImplementing, and
// re-dispatches the implement phase so the next iteration becomes N+1.
// Abort routes through markFailedWithEvent(FailureNeedUserInput).
func (o *Orchestrator) handleFeatureNeedUserInputDecision(featureID string, d NeedUserInputDecision) error {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return fmt.Errorf("load feature: %w", err)
	}
	if f.Status != feature.StatusNeedUserInput {
		return fmt.Errorf("feature %s is not paused on a need-user-input gate (status=%s)", featureID, f.Status)
	}
	gatePath := f.PendingNeedUserInputPath
	if gatePath == "" {
		return errors.New("no pending need-user-input gate path on feature")
	}

	switch d.Decision {
	case "resume":
		rec, err := agent.ReadNeedUserInputRecord(gatePath)
		if err != nil {
			return fmt.Errorf("read gate artifact: %w", err)
		}
		if !rec.AllAnswered() {
			return errors.New("cannot resume: every question must have a non-empty answer before resume")
		}
		if err := o.deps.Store.Modify(featureID, func(ff *feature.Feature) error {
			ff.PendingNeedUserInputPath = ""
			return ff.Transition(feature.StatusImplementing)
		}); err != nil {
			return fmt.Errorf("clear gate + transition implementing: %w", err)
		}
		// Re-dispatch the implement phase. startImplement has multiple
		// synchronous failure exits before the engine actually launches
		// (plan resolution, repo-impl init, multi-repo dispatch, etc.) —
		// if any of those fail after the transition above, the feature
		// would be stranded in StatusImplementing with no pending gate
		// path, losing both the questionnaire and any retry/abort affordance.
		// Roll the transition back so the user can retry or abort from
		// the same paused state.
		if _, _, err := o.startPhase(featureID, feature.PhaseImplement); err != nil {
			if rbErr := o.deps.Store.Modify(featureID, func(ff *feature.Feature) error {
				ff.PendingNeedUserInputPath = gatePath
				if ff.Status == feature.StatusImplementing {
					if tErr := ff.Transition(feature.StatusNeedUserInput); tErr != nil {
						return tErr
					}
				}
				return nil
			}); rbErr != nil {
				return fmt.Errorf("dispatch implement: %w (rollback to gate also failed: %v)", err, rbErr)
			}
			return fmt.Errorf("dispatch implement: %w", err)
		}
		return nil
	case "abort":
		summary := ""
		if rec, err := agent.ReadNeedUserInputRecord(gatePath); err == nil {
			summary = rec.Summary
		}
		if err := o.deps.Store.Modify(featureID, func(ff *feature.Feature) error {
			ff.PendingNeedUserInputPath = ""
			return nil
		}); err != nil {
			return fmt.Errorf("clear gate path: %w", err)
		}
		errMsg := summary
		if errMsg == "" {
			errMsg = "user aborted at need-user-input gate"
		}
		// Abort is the gate's terminal exit — the implement phase ends in
		// failure, so emit PhaseCompleted(err) before markFailedWithEvent.
		// This matches every other orchestrator failure handler
		// (onKBCompleted on KB failure, onArtifactPhaseCompleted on session
		// crash, onPlanLoopDone on missing result) so downstream observers
		// that listen for PhaseCompleted as the implement-phase boundary
		// (observe-summary, lifecycle hooks) see a consistent end-of-phase
		// signal across every terminal failure mode. Gate ENTRY (the pause
		// itself) deliberately suppresses PhaseCompleted because the phase
		// is paused, not terminal — see onSingleRepoNeedUserInput.
		o.emitPhaseCompleted(featureID, feature.PhaseImplement, errors.New(errMsg))
		return o.markFailedWithEvent(featureID, feature.FailureNeedUserInput, errMsg)
	default:
		return fmt.Errorf("unknown need-user-input decision %q (want resume|abort)", d.Decision)
	}
}
