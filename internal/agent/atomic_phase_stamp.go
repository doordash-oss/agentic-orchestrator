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

package agent

import (
	"fmt"
	"sort"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// PhaseOutcome is the verdict the unified phase implementer hands to
// AtomicPhaseStamp. Every phase-declared repo transitions to the same
// outcome in one Modify call so the on-disk state can never reflect a
// partial-phase shipment.
type PhaseOutcome int

const (
	// PhaseOutcomeReviewPassed marks the phase as passing review. Every
	// declared repo is stamped Touched=true with LastError cleared.
	PhaseOutcomeReviewPassed PhaseOutcome = iota
	// PhaseOutcomeFailed marks the phase as failed (safety-rail trip,
	// reviewer rejection after MaxIterations, etc.). Every declared repo
	// is stamped Touched=true with LastError populated.
	PhaseOutcomeFailed
	// PhaseOutcomeNeedUserInput marks the phase as paused on a need-user-input
	// gate. Per-repo state is NOT mutated (the gate lives at the feature
	// level under the unified flow); the function still records the gate
	// path so the harness can surface it.
	PhaseOutcomeNeedUserInput
	// PhaseOutcomeFinalReviewPassed marks the feature-level Final Review
	// as passing. No per-repo state mutation happens (feature-level Status
	// carries the verdict). Optional PR URL writes are mirrored when
	// supplied so RepoStates stays a faithful publish-URL source.
	PhaseOutcomeFinalReviewPassed
)

// AtomicPhaseStampInput is the input to AtomicPhaseStamp. Repos is the
// phase-declared subset (typically PhaseScopeResult.Repos). Outcome
// determines how the per-repo state transitions. LastError is populated
// only on PhaseOutcomeFailed; GatePath only on PhaseOutcomeNeedUserInput.
type AtomicPhaseStampInput struct {
	FeatureID string
	Repos     []string
	Outcome   PhaseOutcome
	LastError string
	GatePath  string
	// PRURLs is an optional per-repo PR URL map applied alongside the
	// state transition. May be nil.
	PRURLs map[string]string
}

// AtomicPhaseStamp transitions every declared repo to the same outcome in
// one FeatureStore.Modify call.
//
// Atomicity guarantees:
//   - Either every declared repo is stamped, or none is (Modify is single-write).
//   - Repos NOT in the declared subset are untouched.
//   - On NEED_USER_INPUT, no per-repo state mutation happens. The gate path
//     is recorded on the feature for the harness to surface.
//
// Ordering: repos are sorted before iteration so the on-disk YAML is stable
// for tests; the resulting state is order-independent.
func AtomicPhaseStamp(store ports.FeatureStore, in AtomicPhaseStampInput) error {
	if store == nil {
		return fmt.Errorf("atomic phase stamp: store is nil")
	}
	if in.FeatureID == "" {
		return fmt.Errorf("atomic phase stamp: feature id is empty")
	}

	repos := append([]string(nil), in.Repos...)
	sort.Strings(repos)

	return store.Modify(in.FeatureID, func(f *feature.Feature) error {
		if f.RepoStates == nil {
			f.RepoStates = make(map[string]*feature.RepoState)
		}
		switch in.Outcome {
		case PhaseOutcomeReviewPassed:
			for _, name := range repos {
				ns := f.RepoStates[name]
				if ns == nil {
					ns = &feature.RepoState{}
					f.RepoStates[name] = ns
				}
				ns.Touched = true
				ns.LastError = ""
				if pr, ok := in.PRURLs[name]; ok && pr != "" {
					ns.PRURL = pr
				}
			}
		case PhaseOutcomeFailed:
			for _, name := range repos {
				ns := f.RepoStates[name]
				if ns == nil {
					ns = &feature.RepoState{}
					f.RepoStates[name] = ns
				}
				ns.Touched = true
				ns.LastError = in.LastError
			}
		case PhaseOutcomeNeedUserInput:
			// Per-repo state is intentionally NOT mutated. The unified-flow
			// gate is feature-level; we still attach the gate path to the
			// feature for the harness to surface.
			f.PendingNeedUserInputPath = in.GatePath
		case PhaseOutcomeFinalReviewPassed:
			// FR pass is feature-level, but stale per-repo errors from an
			// earlier failed FR attempt must not keep rendering as repo
			// failures after the feature has advanced.
			for _, name := range repos {
				ns := f.RepoStates[name]
				if ns != nil {
					ns.LastError = ""
				}
				if pr, ok := in.PRURLs[name]; ok && pr != "" {
					if ns == nil {
						ns = &feature.RepoState{}
						f.RepoStates[name] = ns
					}
					ns.PRURL = pr
				}
			}
		default:
			return fmt.Errorf("atomic phase stamp: unknown outcome %d", in.Outcome)
		}
		return nil
	})
}
