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
	"path/filepath"
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
//   - otherwise → feature-scoped gate. The desktop app may forward a stale RepoName
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

// reposSharingGate returns every repo whose RepoCycleState is paused on
// the given gatePath. The unified review-comments and refactor loops
// persist the same PendingNeedUserInputPath on every staged repo, so this
// identifies all participants in a shared multi-repo gate. Repos paused
// on a different gate or not paused at all are excluded.
func reposSharingGate(f *feature.Feature, gatePath string) []string {
	if f == nil || gatePath == "" {
		return nil
	}
	var out []string
	for _, repo := range f.Repos {
		rc, ok := f.RepoCycles[repo.Name]
		if !ok || rc == nil {
			continue
		}
		if rc.Status == feature.RepoCycleNeedUserInput && rc.PendingNeedUserInputPath == gatePath {
			out = append(out, repo.Name)
		}
	}
	return out
}

// handleRepoCycleNeedUserInputDecision handles a cycle-scoped need-user-input
// decision. A shared multi-repo gate (review-comments / refactor) pauses every
// participating repo on the same PendingNeedUserInputPath; resume must clear
// ALL repos sharing that gate and relaunch one aggregate cycle, while abort
// fails exactly those repos. Sibling repos paused on a DIFFERENT gate are
// untouched. The parent feature stays StatusPublished throughout.
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

	// Collect every repo that shares this gate path. The unified
	// review-comments and refactor loops persist the same
	// PendingNeedUserInputPath on every staged repo when the loop pauses,
	// so this is the set of participants in the shared gate.
	gateRepos := reposSharingGate(f, gatePath)
	if len(gateRepos) == 0 {
		gateRepos = []string{d.RepoName}
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
		if err := o.applyTrustedVerificationDecision(featureID, gatePath, rec); err != nil {
			return err
		}
		// Clear the paused-gate fields on ALL repos sharing the gate,
		// preserving Count, PlanPath, and Type so the restart seam
		// reuses the existing cycle record. Set status back to Running
		// so HasActiveRepoCycles still treats the cycle as in-flight
		// while the restart launches.
		if err := o.deps.Store.Modify(featureID, func(ff *feature.Feature) error {
			for _, name := range gateRepos {
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
		// Relaunch one aggregate cycle. restartPausedRepoCycle dispatches
		// based on the persisted cycle type; for a shared gate the first
		// gate repo's cycle type is representative (all participants share
		// the same cycle dispatch).
		if err := o.restartPausedRepoCycle(featureID, gateRepos[0]); err != nil {
			// Roll back: restore the paused gate on ALL sharing repos so
			// the user can retry or abort from the same paused state.
			if rbErr := o.deps.Store.Modify(featureID, func(ff *feature.Feature) error {
				for _, name := range gateRepos {
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
		// FailRepoCycle clears the gate path and, for refactor cycles, the
		// feature-level RefactorPrompt. Fail every repo that shares the
		// gate; the parent feature stays Published and siblings on other
		// gates keep running.
		for _, name := range gateRepos {
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

// handleFeatureNeedUserInputDecision implements the original single-repo /
// feature-scoped gate flow. Resume validates answers, clears the pending
// gate pointer, transitions the feature back to StatusImplementing, and
// re-dispatches the paused implementation. Harness verification capability
// gates resume the same implementation iteration; agent-authored decision
// gates resume implementation work in the next iteration.
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
		if err := o.applyTrustedVerificationDecision(featureID, gatePath, rec); err != nil {
			return err
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

func (o *Orchestrator) applyTrustedVerificationDecision(featureID, gatePath string, rec agent.NeedUserInputRecord) error {
	if rec.VerificationDecision == nil {
		return nil
	}
	contractPath := filepath.Clean(strings.TrimSpace(rec.VerificationDecision.ContractPath))
	stateRoot, err := filepath.Abs(o.stateDir())
	if err != nil {
		return fmt.Errorf("resolve state root for verification decision: %w", err)
	}
	absContract, err := filepath.Abs(contractPath)
	if err != nil {
		return fmt.Errorf("resolve verification contract path: %w", err)
	}
	rel, err := filepath.Rel(stateRoot, absContract)
	if err != nil {
		return fmt.Errorf("relate verification contract %q to state root %q: %w", absContract, stateRoot, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("verification decision contract %q is outside state root %q", absContract, stateRoot)
	}
	if filepath.Base(absContract) != "testing-contract.yaml" {
		return fmt.Errorf("verification decision contract has unexpected filename %q", filepath.Base(absContract))
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) == 0 || parts[0] != featureID {
		return fmt.Errorf("verification decision contract %q is not scoped to feature %q", absContract, featureID)
	}
	if filepath.Base(filepath.Clean(gatePath)) != agent.NeedUserInputArtifactName {
		return fmt.Errorf("verification decision came from a non-canonical gate artifact")
	}
	return agent.ApplyNeedUserVerificationDecision(rec)
}
