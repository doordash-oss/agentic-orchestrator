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

package feature

import (
	"fmt"
	"time"
)

// SealReason identifies why a run was sealed. Only rewind seals runs today;
// kept as a typed string so future callers (completed/failed/interrupted,
// per-user-decision currently out of scope) can extend without a YAML break.
type SealReason string

const (
	// SealReasonRewind is recorded when a rewind forks a fresh run off a
	// sealed one. Today this is the only seal cause.
	SealReasonRewind SealReason = "rewind"
)

// RewindRequest describes a durable rewind target. RoadmapPhase is optional:
// zero means a full phase rewind, while a positive value targets a roadmap
// phase within the Implement phase.
type RewindRequest struct {
	TargetPhase  Phase
	RoadmapPhase int
}

// VerificationItemStatus is one harness-executed testing-contract command's
// live progress state ("pending", "running", or a terminal classification)
// during the deterministic verification substep.
type VerificationItemStatus struct {
	Name  string `yaml:"name" json:"name"`
	State string `yaml:"state" json:"state"`
}

// Run captures one attempt at the pipeline for a feature. Every per-attempt
// field that used to live on Feature now lives here. Run is persisted at
// `<stateDir>/<featureID>/runs/run-<zero-padded-number>/run.yaml`. Loaded
// eagerly by Store.Load via Feature.run; mutated through Store.Modify.
type Run struct {
	RunNumber int        `yaml:"run_number"`
	StartedAt *time.Time `yaml:"started_at,omitempty"`

	// Setup tracks first-run preparation before any phase session starts.
	Setup *SetupState `yaml:"setup,omitempty"`

	// Sealing (set only by rewind; absent/zero on active runs).
	SealedAt     *time.Time `yaml:"sealed_at,omitempty"`
	SealReason   SealReason `yaml:"seal_reason,omitempty"`
	RewindTarget *Phase     `yaml:"rewind_target,omitempty"`
	// RewindRoadmapPhase records the selected roadmap phase only when this
	// sealed run was forked by a partial Implement rewind.
	RewindRoadmapPhase *int `yaml:"rewind_roadmap_phase,omitempty"`

	// Provenance — written on a fresh fork. CarriedPhases is the list of
	// phase dir-names actually copied from the sealed predecessor.
	CarriedFromRun int      `yaml:"carried_from_run,omitempty"`
	CarriedPhases  []string `yaml:"carried_phases,omitempty"`

	// Backup branches recorded at seal time, per-repo.
	BackupBranches map[string]string `yaml:"backup_branches,omitempty"`

	// Committing is set to true on a freshly-forked run BEFORE its
	// carry-forward copy runs, and cleared back to false AFTER the copy
	// completes and the new run's artifact map is populated. Store.
	// CleanupOrphanRuns observes this flag at startup: a run with
	// Committing:true is treated as an orphan from a crashed rewind and
	// deleted. Sealed runs by invariant never have Committing:true.
	Committing bool `yaml:"committing,omitempty"`

	// Timings/costs (moved from Feature).
	PhaseTimings     map[string]time.Duration `yaml:"phase_timings,omitempty"`
	PhaseCosts       map[string]float64       `yaml:"phase_costs,omitempty"`
	SessionCosts     []SessionCostRecord      `yaml:"session_costs,omitempty"`
	ActivePhaseStart *time.Time               `yaml:"active_phase_start,omitempty"`
	ActiveTimingKey  string                   `yaml:"active_timing_key,omitempty"`

	// Iteration counters (moved from Feature).
	CurrentIteration int `yaml:"current_iteration,omitempty"`
	PlanIteration    int `yaml:"plan_iteration,omitempty"`
	ReviewIteration  int `yaml:"review_iteration,omitempty"`
	RebaseCount      int `yaml:"rebase_count,omitempty"`
	// ReviewCommentsCount is the run-level review-comments cycle counter.
	// Incremented per RunReviewCommentsLoop invocation. Each invocation gets a
	// flat artifact dir at runs/run-N/review-comments-N/iteration-NN/ with no
	// per-repo subdir.
	ReviewCommentsCount int `yaml:"review_comments_count,omitempty"`

	// Roadmap progress (moved from Feature).
	CurrentRoadmapPhase int    `yaml:"current_roadmap_phase,omitempty"`
	TotalRoadmapPhases  int    `yaml:"total_roadmap_phases,omitempty"`
	RoadmapPhaseType    string `yaml:"roadmap_phase_type,omitempty"`

	// RoadmapPhaseCommitAnchors records the full per-repo HEAD SHA at each
	// completed roadmap phase boundary. The outer key is the roadmap phase
	// number; the inner key is FeatureRepo.Name.
	RoadmapPhaseCommitAnchors map[int]map[string]string `yaml:"roadmap_phase_commit_anchors,omitempty"`
	// RoadmapPhaseFrontendByPhase records whether each roadmap phase plan was
	// marked as frontend work. Missing phases default to false.
	RoadmapPhaseFrontendByPhase map[int]bool `yaml:"roadmap_phase_frontend,omitempty"`

	// Cycle state (moved from Feature).
	// ActiveCycleType is the feature-level cycle type marker. Kept alongside
	// ActiveCycle for the desktop app's per-repo rendering surface (RepoCycles map
	// below) which still consults the legacy field.
	ActiveCycleType RepoCycleType `yaml:"active_cycle_type,omitempty"`
	// ActiveCycle is the feature-level active post-publish cycle under
	// SchemaVersionCurrent = 4.
	ActiveCycle *CycleState `yaml:"active_cycle,omitempty"`

	// Artifacts (moved from Feature) — entries are run-relative paths.
	Artifacts map[string]string `yaml:"artifacts,omitempty"`

	// Multi-repo state (moved from Feature). Under SchemaVersionCurrent = 5
	// per-repo orchestration signal lives in RepoStates (Touched, PRURL,
	// LastError); the unified phase-implement loop owns mid-flight state at
	// the feature level (Run.CurrentPhaseStatus). RepoCycles is the per-repo
	// cycle rendering surface read by the desktop app; the unified cycle loops mirror
	// their per-repo entries here so existing desktop app badges keep working.
	RepoCycles map[string]*RepoCycleState `yaml:"repo_cycles,omitempty"`
	RepoStates map[string]*RepoState      `yaml:"repo_states,omitempty"`
	// RebaseOperation tracks transient feature-level rebase progress while a
	// harness, smart rebase, or rebase-triggered Final Review is active. It is
	// cleared on successful/no-op settlement and retained only when actionable
	// failure/conflict state remains useful to render.
	RebaseOperation *RebaseOperationState `yaml:"rebase_operation,omitempty"`

	// CurrentPhaseStatus is the mid-flight phase-implement status for the
	// unified flow ("implementing", "reviewing", "verifying", or "" when not
	// in a phase).
	CurrentPhaseStatus string `yaml:"current_phase_status,omitempty"`

	// Publish (moved from Feature).
	PRURL string `yaml:"pr_url,omitempty"`

	// Plan validation + gate state (moved from Feature).
	ValidatingPlan    bool              `yaml:"validating_plan,omitempty"`
	ValidatorStatuses map[string]string `yaml:"validator_statuses,omitempty"`
	// VerificationItems is the ordered live progress of harness-executed
	// testing-contract commands while CurrentPhaseStatus is "verifying".
	VerificationItems  []VerificationItemStatus `yaml:"verification_items,omitempty"`
	ReviewingGate      bool                     `yaml:"reviewing_gate,omitempty"`
	ReviewFixing       bool                     `yaml:"review_fixing,omitempty"`
	AddressingReviews  bool                     `yaml:"addressing_reviews,omitempty"`
	PendingReviewPhase *Phase                   `yaml:"pending_review_phase,omitempty"`
	// PendingRewindReviewRoadmapPhase is set only while a partial rewind to
	// Implement is waiting for human review of the selected roadmap phase plan.
	// It is deliberately separate from CurrentRoadmapPhase so the dashboard can
	// display phase progress while the review lifecycle still knows the user has
	// not proceeded.
	PendingRewindReviewRoadmapPhase *int `yaml:"pending_rewind_review_roadmap_phase,omitempty"`
	IsRewind                        bool `yaml:"is_rewind,omitempty"`

	// MaxPlanIterations is a per-run ceiling. The feature-level
	// MaxPlanIterations is a user-set config ceiling; this per-run counter
	// tracks the reset-on-phase-boundary limit that the plan loop consults.
	MaxPlanIterations int `yaml:"max_plan_iterations,omitempty"`

	// Error state (moved from Feature).
	LastError   string `yaml:"last_error,omitempty"`
	FailureType string `yaml:"failure_type,omitempty"`

	// KB transient flags (moved from Feature — actual KB data lives in a
	// sibling knowledge-base/ directory outside the feature dir, untouched).
	KBWaitMessage  string            `yaml:"kb_wait_message,omitempty"`
	ForceKBRebuild bool              `yaml:"force_kb_rebuild,omitempty"`
	KBStatus       map[string]string `yaml:"kb_status,omitempty"`

	// Deferrals is the cross-phase work ledger. Entries are added when
	// a phase's plan or implement output declares that specific work
	// should land in a later phase; they are carried forward into the
	// target phase's prompts and the implement Report Integrity Gate
	// refuses SUCCESS while a due-this-phase entry remains open. See
	// internal/feature/deferral.go for the lifecycle.
	Deferrals []Deferral `yaml:"deferrals,omitempty"`

	// PendingNeedUserInputPath is the absolute path of the persisted
	// `need-user-input.yaml` gate artifact when the feature is paused on a
	// single-repo NEED_USER_INPUT gate. Empty when no gate is open. Multi-repo
	// runs persist the per-repo gate path on RepoImplState instead.
	PendingNeedUserInputPath string `yaml:"pending_need_user_input_path,omitempty"`
}

// SessionCostRecord is one accounted LLM session within a run. PhaseCosts is
// the phase-level aggregate used by dashboards; this slice preserves the
// session-level ledger behind that aggregate.
type SessionCostRecord struct {
	SessionID     string  `yaml:"session_id"`
	PhaseKey      string  `yaml:"phase_key"`
	ObserverPhase string  `yaml:"observer_phase,omitempty"`
	RepoName      string  `yaml:"repo_name,omitempty"`
	CostUSD       float64 `yaml:"cost_usd"`
}

// IsSealed reports whether this run has been sealed (rewound past).
// A sealed run is immutable: SaveRun panics if called on one.
func (r *Run) IsSealed() bool { return r != nil && r.SealedAt != nil }

// AccumulateActiveTime moves elapsed time from ActivePhaseStart into
// PhaseTimings under the ActiveTimingKey, then clears ActivePhaseStart.
// ActiveTimingKey is intentionally preserved so cycle keys (rebase-N,
// review-comments) survive interrupt/fail transitions and are available when
// the phase is resumed.
func (r *Run) AccumulateActiveTime() {
	if r == nil || r.ActivePhaseStart == nil || r.ActiveTimingKey == "" {
		return
	}
	elapsed := time.Since(*r.ActivePhaseStart)
	if r.PhaseTimings == nil {
		r.PhaseTimings = make(map[string]time.Duration)
	}
	r.PhaseTimings[r.ActiveTimingKey] += elapsed
	r.ActivePhaseStart = nil
}

// SetRoadmapPhaseFrontend records whether a roadmap phase contains frontend
// work. Non-positive phases are ignored because roadmap phases are 1-indexed.
func (r *Run) SetRoadmapPhaseFrontend(phase int, frontend bool) {
	if r == nil || phase <= 0 {
		return
	}
	if r.RoadmapPhaseFrontendByPhase == nil {
		r.RoadmapPhaseFrontendByPhase = make(map[int]bool)
	}
	r.RoadmapPhaseFrontendByPhase[phase] = frontend
}

// RoadmapPhaseFrontend reports whether a roadmap phase was recorded as
// frontend. Missing phases default to false.
func (r *Run) RoadmapPhaseFrontend(phase int) bool {
	if r == nil || phase <= 0 {
		return false
	}
	if r.RoadmapPhaseFrontendByPhase != nil {
		if frontend, ok := r.RoadmapPhaseFrontendByPhase[phase]; ok {
			return frontend
		}
	}
	return false
}

// AnyRoadmapPhaseFrontend reports whether any recorded roadmap phase is
// frontend.
func (r *Run) AnyRoadmapPhaseFrontend() bool {
	if r == nil {
		return false
	}
	for _, frontend := range r.RoadmapPhaseFrontendByPhase {
		if frontend {
			return true
		}
	}
	return false
}

// CyclePrefix returns the artifact directory prefix for the current
// post-publish cycle (rebase, or review-comments). Returns empty
// string when no cycle is active.
func (r *Run) CyclePrefix() string {
	if r == nil {
		return ""
	}
	switch r.ActiveCycleType {
	case CycleRebase:
		if r.RebaseCount > 0 {
			return fmt.Sprintf("rebase-%d", r.RebaseCount)
		}
		return "rebase"
	case CycleReviewComments:
		if r.ReviewCommentsCount > 0 {
			return fmt.Sprintf("review-comments-%d", r.ReviewCommentsCount)
		}
		return "review-comments"
	}
	return ""
}

// TotalRuntime returns the total active runtime for the run.
// This is the sum of all accumulated phase timings plus any currently
// running phase time.
func (r *Run) TotalRuntime() time.Duration {
	if r == nil {
		return 0
	}
	var total time.Duration
	for _, d := range r.PhaseTimings {
		total += d
	}
	if r.ActivePhaseStart != nil {
		total += time.Since(*r.ActivePhaseStart)
	}
	return total
}

// PhaseRuntime returns the runtime for a specific phase/cycle timing key.
// If the key matches the currently active phase, includes live elapsed time.
func (r *Run) PhaseRuntime(key string) time.Duration {
	if r == nil {
		return 0
	}
	d := r.PhaseTimings[key]
	if r.ActiveTimingKey == key && r.ActivePhaseStart != nil {
		d += time.Since(*r.ActivePhaseStart)
	}
	return d
}

// TotalCost returns the total accumulated cost for the run across all phases.
func (r *Run) TotalCost() float64 {
	if r == nil {
		return 0
	}
	var total float64
	for _, c := range r.PhaseCosts {
		total += c
	}
	return total
}

// PhaseCost returns the accumulated cost for a specific phase/cycle key.
func (r *Run) PhaseCost(key string) float64 {
	if r == nil {
		return 0
	}
	return r.PhaseCosts[key]
}

// AddPhaseCost adds a cost amount to the given phase/cycle key.
func (r *Run) AddPhaseCost(key string, cost float64) {
	if r == nil || cost <= 0 {
		return
	}
	if r.PhaseCosts == nil {
		r.PhaseCosts = make(map[string]float64)
	}
	r.PhaseCosts[key] += cost
}

// RunDirName returns the zero-padded run directory name, e.g. "run-001".
// Exported so agent path helpers and the Store can share one source of truth.
func RunDirName(n int) string {
	return fmt.Sprintf("run-%03d", n)
}
