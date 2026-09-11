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

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
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

// RewindWarningKind identifies the non-fatal rewind failure family.
type RewindWarningKind string

const (
	// RewindWarningPullRequestClose marks a pull request that could not be
	// closed during the rewind.
	RewindWarningPullRequestClose RewindWarningKind = "pull_request_close"
	// RewindWarningBackupBranch marks a backup branch that could not be
	// created during the rewind.
	RewindWarningBackupBranch RewindWarningKind = "backup_branch"
	// RewindWarningWorktreeReset marks a worktree that could not be reset
	// during the rewind.
	RewindWarningWorktreeReset RewindWarningKind = "worktree_reset"
	// RewindWarningStackBranch marks a stack branch step that could not run
	// during the rewind: switching a worktree to its layer's branch, deleting
	// an upper layer's local ref, or renaming the checked-out branch to the
	// provisional layer-1 name.
	RewindWarningStackBranch RewindWarningKind = "stack_branch"
)

// RewindWarning is one typed non-fatal rewind failure: the cause family, the
// repository it happened in, its branch when known, and the raw error. The
// mutation-target boundary classifies it into a canonical rewind warning
// code with the repositories block; no layer renders it as text first.
type RewindWarning struct {
	Kind   RewindWarningKind
	Repo   string
	Branch string
	Err    error
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

	// FeatureSeq is the PersistSeq of the feature save that last wrote this
	// file through saveUnlocked. Run-only writes (seal, fork skeletons,
	// SaveRun paths) leave it at its last stamped value or zero, so a value
	// strictly newer than the feature record's PersistSeq can only mean a
	// reader interleaved a joint save between its feature and run reads.
	FeatureSeq int64 `yaml:"feature_seq,omitempty"`

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

	// Stack records the approved pull-request stack composition (one layer
	// per `## Pull Requests` table row), derived from the roadmap on disk at
	// approval so gate edits are honored. Omitted on runs approved before the
	// table existed; a full rewind to the roadmap phase clears it because
	// planning re-runs and re-persists it at the next approval.
	Stack []StackLayer `yaml:"stack,omitempty"`

	// Artifacts (moved from Feature) — entries are run-relative paths.
	Artifacts map[string]string `yaml:"artifacts,omitempty"`

	// Multi-repo state (moved from Feature). Under SchemaVersionCurrent = 5
	// per-repo orchestration signal lives in RepoStates (Touched, PRURL,
	// LastError); the unified phase-implement loop owns mid-flight state at
	// the feature level (Run.CurrentPhaseStatus).
	RepoStates map[string]*RepoState `yaml:"repo_states,omitempty"`

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

	// Failure is the durable canonical failure record for this run: a
	// catalog code, typed context blocks, and raw diagnostics. No rendered
	// text is persisted; read models render it through the catalog at
	// projection time. Legacy `last_error`/`failure_type` YAML keys from
	// older schemas are ignored on load and never written.
	Failure *errcat.FailureRecord `yaml:"failure,omitempty"`

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
	SessionID         string  `yaml:"session_id"`
	PhaseKey          string  `yaml:"phase_key"`
	ObserverPhase     string  `yaml:"observer_phase,omitempty"`
	RepoName          string  `yaml:"repo_name,omitempty"`
	CostUSD           float64 `yaml:"cost_usd"`
	CostSource        string  `yaml:"cost_source,omitempty"`
	CostCreditsMicros *int64  `yaml:"cost_credits_micros,omitempty"`
}

// IsSealed reports whether this run has been sealed (rewound past).
// A sealed run is immutable: SaveRun panics if called on one.
func (r *Run) IsSealed() bool { return r != nil && r.SealedAt != nil }

// StackPRState is the lifecycle state of the pull request a layer's
// repository entry delivers. None is the state before any pull request
// exists; the empty value loads as absent and means the same.
type StackPRState string

const (
	StackPRStateNone   StackPRState = "none"
	StackPRStateOpen   StackPRState = "open"
	StackPRStateMerged StackPRState = "merged"
	StackPRStateClosed StackPRState = "closed"
)

// StackRepoEntry is one repository's state inside one stack layer: the tip
// SHA the layer boundary snapshotted, the last SHA pushed for the layer's
// pull request, and that pull request's URL and state. All fields persist
// with omit-empty semantics; a layer persisted before per-repository
// entries existed loads with the map absent, and a repository untouched by
// the layer records a tip equal to the layer below (or the base start
// point for layer 1), which is how "no pull request here" is represented.
type StackRepoEntry struct {
	TipSHA        string       `yaml:"tip_sha,omitempty" json:"tip_sha,omitempty"`
	LastPushedSHA string       `yaml:"last_pushed_sha,omitempty" json:"last_pushed_sha,omitempty"`
	PRURL         string       `yaml:"pr_url,omitempty" json:"pr_url,omitempty"`
	PRState       StackPRState `yaml:"pr_state,omitempty" json:"pr_state,omitempty"`
}

// StackLayer is one pull-request layer of a feature's delivery stack,
// derived from one `## Pull Requests` table row of the approved roadmap.
// Branch is the layer's shared branch name feature/<slug>-<id>/<k>-<layer-
// slug>, filled in at roadmap approval from the workspace slug, the
// position, and the layer slug; a run persisted before that fill loads with
// it omitted. Later roadmap phases read it through the run accessors to
// create the next layer's branch rather than recomputing the name. Repos
// holds the per-repository entries the layer boundaries fill in: each
// repository's layer tip (a boundary snapshot — the checked-out branch ref
// stays authoritative between boundaries), last pushed SHA, and pull
// request URL and state.
type StackLayer struct {
	Position int                       `yaml:"position" json:"position"`
	Title    string                    `yaml:"title,omitempty" json:"title,omitempty"`
	Slug     string                    `yaml:"slug,omitempty" json:"slug,omitempty"`
	Phases   []int                     `yaml:"phases,omitempty" json:"phases,omitempty"`
	Branch   string                    `yaml:"branch,omitempty" json:"branch,omitempty"`
	Repos    map[string]StackRepoEntry `yaml:"repos,omitempty" json:"repos,omitempty"`
}

// CopyStackLayers returns a deep copy of stack so a forked run and the
// sealed run it came from never share backing arrays or maps.
func CopyStackLayers(stack []StackLayer) []StackLayer {
	if stack == nil {
		return nil
	}
	out := make([]StackLayer, len(stack))
	for i, layer := range stack {
		out[i] = layer
		out[i].Phases = append([]int(nil), layer.Phases...)
		if layer.Repos != nil {
			repos := make(map[string]StackRepoEntry, len(layer.Repos))
			for name, entry := range layer.Repos {
				repos[name] = entry
			}
			out[i].Repos = repos
		}
	}
	return out
}

// CopyStackLayersForPartialRewind deep-copies stack for a partial rewind to a
// phase of the layer at layerPosition: every layer definition (position,
// title, slug, phases, branch) is kept, while the per-repository entries of
// the target layer and every layer above are cleared — the worktrees were
// reset, so the next layer boundary re-records them against the new tips.
// Layers below the target layer keep their entries untouched.
func CopyStackLayersForPartialRewind(stack []StackLayer, layerPosition int) []StackLayer {
	out := CopyStackLayers(stack)
	for i := range out {
		if layerPosition > 0 && out[i].Position >= layerPosition {
			out[i].Repos = nil
		}
	}
	return out
}

// AccumulateActiveTime moves elapsed time from ActivePhaseStart into
// PhaseTimings under the ActiveTimingKey, then clears ActivePhaseStart.
// ActiveTimingKey is intentionally preserved so rebase cycle keys survive
// interrupt/fail transitions and are available when
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
