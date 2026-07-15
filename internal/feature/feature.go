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
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
)

type Phase int

const (
	PhaseResearch Phase = iota
	PhasePlan
	PhaseImplement
	PhasePublish
	PhaseReview
	PhaseKnowledgeBase
	PhaseInquire
	PhaseDesign
	// PhaseFinalReview is the deferred end-of-feature review pass that runs
	// once after the last roadmap-phase implement completes. The implement
	// loop's per-iteration review gate handles iteration-level review for
	// profiles that enable it; this phase covers the cross-cutting "final pass
	// over all repos" that used to be inlined inside RunMultiRepoOrchestrator.
	PhaseFinalReview
)

// DesignArtifactKey is the artifact-map key for the Design phase output.
const DesignArtifactKey = "design"

// ResearchArtifactKey is the artifact-map key for the Research phase output.
const ResearchArtifactKey = "research"

// DesignArtifactPath returns the recorded path of the Design artifact for a feature.
func (f *Feature) DesignArtifactPath() string {
	if f == nil || f.Artifacts == nil {
		return ""
	}
	return f.Artifacts[DesignArtifactKey]
}

// ResearchArtifactPath returns the recorded path of the Research artifact for a feature.
func (f *Feature) ResearchArtifactPath() string {
	if f == nil || f.Artifacts == nil {
		return ""
	}
	return f.Artifacts[ResearchArtifactKey]
}

// LogicalOrder returns the execution/display sequence order for the phase.
// This avoids breaking existing serialized Phase values (iota-based) while
// providing the correct ordering for progress bar display and done checks.
func (p Phase) LogicalOrder() int {
	switch p {
	case PhaseKnowledgeBase:
		return 0
	case PhaseInquire:
		return 1
	case PhaseResearch:
		return 2
	case PhaseDesign:
		return 3
	case PhasePlan:
		return 4
	case PhaseImplement:
		return 5
	case PhaseReview, PhaseFinalReview:
		// PhaseReview remains the session/artifact tag for the final-review
		// stage; PhaseFinalReview is the lifecycle CurrentPhase value set when
		// the orchestrator enters the deferred end-of-feature review pass.
		// Both share logical position 6 (between Implement and Publish).
		return 6
	case PhasePublish:
		return 7
	default:
		return 99
	}
}

func (p Phase) String() string {
	switch p {
	case PhaseResearch:
		return "Research"
	case PhasePlan:
		return "Plan"
	case PhaseImplement:
		return "Implement"
	case PhaseReview:
		return "Review"
	case PhaseKnowledgeBase:
		return "Knowledge Base"
	case PhaseInquire:
		return "Inquire"
	case PhaseDesign:
		return "Design"
	case PhaseFinalReview:
		return "Final Review"
	case PhasePublish:
		return "Publish"
	default:
		return fmt.Sprintf("Phase(%d)", int(p))
	}
}

// DirName returns the lowercase directory name used for phase artifacts.
func (p Phase) DirName() string {
	switch p {
	case PhaseResearch:
		return "research"
	case PhasePlan:
		return "plan"
	case PhaseImplement:
		return "implement"
	case PhaseReview:
		return "review"
	case PhaseKnowledgeBase:
		return "knowledgebase"
	case PhaseInquire:
		return "inquire"
	case PhaseDesign:
		return "design"
	case PhaseFinalReview:
		// Share the "review" subdir with PhaseReview so existing artifact
		// resolvers (review_context.go, orchestrator.resolveFinalReviewArtifactDirForRepo)
		// keep working without a parallel phaseN/finalreview tree.
		return "review"
	case PhasePublish:
		return "publish"
	default:
		return fmt.Sprintf("phase%d", int(p))
	}
}

// RequiresGrilling reports whether a phase relies on the [grill-me] directive
// — i.e. whether its prompt expects the agent to interview the user. Used by
// session builders to override the user's Claude Code "auto" default mode,
// which would otherwise inject a "work without stopping for clarifying
// questions" system-reminder that suppresses grilling.
func (p Phase) RequiresGrilling() bool {
	switch p {
	case PhaseInquire, PhaseDesign, PhasePlan:
		return true
	default:
		return false
	}
}

type Inquireness string

const (
	InquirenessNone   Inquireness = "none"
	InquirenessMedium Inquireness = "medium"
	InquirenessHigh   Inquireness = "high"
)

// RepoState carries the minimal per-repo signal orchestration needs:
// whether any phase touched the repo, the optional PR URL, and the most
// recent error message. Persisted on Run.RepoStates.
type RepoState struct {
	Touched   bool   `yaml:"touched,omitempty"`
	PRURL     string `yaml:"pr_url,omitempty"`
	LastError string `yaml:"last_error,omitempty"`

	Freshness string `yaml:"-"`
}

// RepoCycleType identifies the kind of post-publish per-repo cycle.
type RepoCycleType string

const (
	CycleRebase         RepoCycleType = "rebase"
	CycleReviewComments RepoCycleType = "review-comments"
	CycleRefactor       RepoCycleType = "refactor"
)

// Cycle status constants for RepoCycleState.Status.
const (
	RepoCycleRunning       = "running"
	RepoCycleReviewing     = "reviewing"
	RepoCycleNeedUserInput = "need_user_input"
	RepoCycleFailed        = "failed"
	// RepoCycleInterrupted: user quit mid-cycle. Resumable via [r], not failed.
	RepoCycleInterrupted = "interrupted"
)

// RepoCycleState tracks a post-publish cycle (rebase/review-comments) for a single repo.
// The feature stays StatusPublished while per-repo cycles run independently.
type RepoCycleState struct {
	Type      RepoCycleType `yaml:"type"`
	Status    string        `yaml:"status"`          // RepoCycle* constants
	Count     int           `yaml:"count,omitempty"` // Nth rebase for this repo
	PlanPath  string        `yaml:"plan_path,omitempty"`
	LastError string        `yaml:"last_error,omitempty"`
	// Iteration is the iteration number that emitted the active gate. Set
	// when a cycle pauses on a need-user-input gate; preserved across resume
	// so the next iteration is N+1 rather than starting over.
	Iteration int `yaml:"iteration,omitempty"`
	// PendingNeedUserInputPath is the absolute path of the persisted
	// `need-user-input.yaml` artifact when this cycle is paused on a
	// cycle-scoped NEED_USER_INPUT gate. Empty otherwise.
	PendingNeedUserInputPath string `yaml:"pending_need_user_input_path,omitempty"`
}

// PendingUserInputCycle is a flat projection of a paused per-repo cycle gate
// surfaced for TUI/orchestrator routing. Returned by Feature.PendingUserInputCycles.
type PendingUserInputCycle struct {
	RepoName  string
	CycleType RepoCycleType
	GatePath  string
}

// SchemaVersionCurrent is the current durable on-disk schema version stamped
// onto fresh features at Manager.Create time.
const SchemaVersionCurrent = 6

// CycleState tracks the feature-level active post-publish cycle (rebase,
// review-comments, refactor). One cycle is active at a time per
// feature regardless of how many repos it touches.
// Run.RepoCycles persists alongside CycleState as the per-repo TUI rendering
// surface; the unified cycle loops mirror their per-repo entries there so
// existing per-repo badges keep working.
type CycleState struct {
	Type   RepoCycleType `yaml:"type"`
	Status string        `yaml:"status"` // RepoCycle* constants
	Count  int           `yaml:"count,omitempty"`
	// PlanPath is the cycle's plan artifact (refactor/review-comments) when
	// applicable. Empty for plan-less cycles (rebase).
	PlanPath string `yaml:"plan_path,omitempty"`
	// LastError is populated when Status == RepoCycleFailed.
	LastError string `yaml:"last_error,omitempty"`
	// Iteration is the iteration number that emitted the active gate. Set
	// when a cycle pauses on a need-user-input gate; preserved across resume
	// so the next iteration is N+1 rather than starting over.
	Iteration int `yaml:"iteration,omitempty"`
	// PendingNeedUserInputPath is the absolute path of the persisted
	// `need-user-input.yaml` artifact when the cycle is paused on a gate.
	PendingNeedUserInputPath string `yaml:"pending_need_user_input_path,omitempty"`
}

// RiskLevel classifies the blast radius of a feature change.
// Autonomy scales inversely with risk: low-risk changes get lightweight
// validation, high-risk changes get all validators and mandatory human review.
type RiskLevel string

const (
	RiskLow    RiskLevel = "low"
	RiskMedium RiskLevel = "medium"
	RiskHigh   RiskLevel = "high"
)

// Failure types categorize why a feature entered the Failed state.
const (
	FailureSafetyRail        = "safety_rail"
	FailureMaxIterations     = "max_iterations"
	FailureSessionCrash      = "session_crash"
	FailureMissingArtifact   = "missing_artifact"
	FailureProtocolViolation = "protocol_violation"
	FailureInfrastructure    = "infrastructure"
	FailureWorktreeSetup     = "worktree_setup"
	// FailureNeedUserInput marks a feature failure caused by an
	// implement iteration emitting `## Iteration State: NEED_USER_INPUT`
	// in progress.md. The harness treats this as a terminal state today;
	// richer human-in-the-loop recovery is a follow-up.
	FailureNeedUserInput = "need_user_input"
)

type Status int

const (
	StatusCreated Status = iota
	StatusResearching
	StatusPlanReady
	StatusPlanning
	StatusImplementReady
	StatusImplementing
	StatusReviewPassed
	StatusCodeReady
	StatusPublished
	StatusFailed
	StatusInterrupted
	StatusDone
	StatusBuildingKB
	StatusPlanNeedsReview
	StatusInquiring
	StatusInquireReady
	StatusDesignReady
	StatusDesigning
	StatusPromptNeedsReview
	StatusInquiryNeedsReview
	StatusResearchNeedsReview
	StatusDesignNeedsReview
	StatusReviewing
	// StatusNeedUserInput marks a feature paused on a phase-implement
	// need-user-input gate. The unified flow tracks NEED_USER_INPUT at the
	// feature level via Feature.PendingNeedUserInputPath; per-repo pauses
	// are no longer modelled separately. Post-publish cycle-scoped pauses
	// use RepoCycleNeedUserInput on the affected RepoCycleState and keep
	// the parent feature in StatusPublished.
	StatusNeedUserInput
	// StatusFinalReviewing marks a feature in the deferred end-of-feature
	// final review pass. The orchestrator transitions
	// StatusReviewPassed → StatusFinalReviewing on entry and
	// StatusFinalReviewing → StatusCodeReady on success.
	StatusFinalReviewing
	// StatusSettingUpWorktrees marks durable first-run setup before any phase
	// session starts. It is active work, but not a formal pipeline phase.
	StatusSettingUpWorktrees
)

func (s Status) String() string {
	switch s {
	case StatusCreated:
		return "Created"
	case StatusResearching:
		return "Researching"
	case StatusPlanReady:
		return "PlanReady"
	case StatusPlanning:
		return "Planning"
	case StatusImplementReady:
		return "ImplementReady"
	case StatusImplementing:
		return "Implementing"
	case StatusReviewPassed:
		return "ReviewPassed"
	case StatusCodeReady:
		return "CodeReady"
	case StatusPublished:
		return "Published"
	case StatusDone:
		return "Done"
	case StatusFailed:
		return "Failed"
	case StatusInterrupted:
		return "Interrupted"
	case StatusBuildingKB:
		return "BuildingKB"
	case StatusPlanNeedsReview:
		return "PlanNeedsReview"
	case StatusInquiring:
		return "Inquiring"
	case StatusInquireReady:
		return "InquireReady"
	case StatusDesignReady:
		return "DesignReady"
	case StatusDesigning:
		return "Designing"
	case StatusPromptNeedsReview:
		return "PromptNeedsReview"
	case StatusInquiryNeedsReview:
		return "InquiryNeedsReview"
	case StatusResearchNeedsReview:
		return "ResearchNeedsReview"
	case StatusDesignNeedsReview:
		return "DesignNeedsReview"
	case StatusReviewing:
		return "Reviewing"
	case StatusNeedUserInput:
		return "NeedUserInput"
	case StatusFinalReviewing:
		return "FinalReviewing"
	case StatusSettingUpWorktrees:
		return "SettingUpWorktrees"
	default:
		return fmt.Sprintf("Status(%d)", int(s))
	}
}

// IsRunning returns true if this status represents an actively executing phase.
func (s Status) IsRunning() bool {
	return s == StatusResearching || s == StatusPlanning || s == StatusImplementing || s == StatusBuildingKB || s == StatusInquiring || s == StatusDesigning || s == StatusFinalReviewing || s == StatusSettingUpWorktrees
}

// IsNeedsReview returns true if this status represents a pending artifact review.
func (s Status) IsNeedsReview() bool {
	return s == StatusPlanNeedsReview ||
		s == StatusPromptNeedsReview ||
		s == StatusInquiryNeedsReview ||
		s == StatusResearchNeedsReview ||
		s == StatusDesignNeedsReview
}

// NeedsReviewForPhase returns the NeedsReview status appropriate for the given target phase.
func NeedsReviewForPhase(target Phase) Status {
	switch target {
	case PhaseInquire:
		return StatusPromptNeedsReview
	case PhaseResearch:
		return StatusInquiryNeedsReview
	case PhaseDesign:
		return StatusResearchNeedsReview
	case PhasePlan:
		return StatusDesignNeedsReview
	case PhaseImplement:
		return StatusPlanNeedsReview
	default:
		return StatusCreated
	}
}

// MarshalYAML serializes Status as a string for stable YAML representation.
func (s Status) MarshalYAML() (interface{}, error) {
	return s.String(), nil
}

// UnmarshalYAML deserializes Status from a string or integer YAML value.
// String values are the canonical form; integer values provide backward
// compatibility with older feature.yaml files.
func (s *Status) UnmarshalYAML(unmarshal func(interface{}) error) error {
	stringMap := map[string]Status{
		"Created":             StatusCreated,
		"Researching":         StatusResearching,
		"PlanReady":           StatusPlanReady,
		"Planning":            StatusPlanning,
		"ImplementReady":      StatusImplementReady,
		"Implementing":        StatusImplementing,
		"ReviewPassed":        StatusReviewPassed,
		"CodeReady":           StatusCodeReady,
		"PRReady":             StatusCodeReady, // backward compat
		"Published":           StatusPublished,
		"Failed":              StatusFailed,
		"Interrupted":         StatusInterrupted,
		"Done":                StatusDone,
		"BuildingKB":          StatusBuildingKB,
		"PlanNeedsReview":     StatusPlanNeedsReview,
		"Inquiring":           StatusInquiring,
		"InquireReady":        StatusInquireReady,
		"BrainstormReady":     StatusDesignReady, // legacy pre-design rename
		"DesignReady":         StatusDesignReady,
		"Brainstorming":       StatusDesigning, // legacy pre-design rename
		"Designing":           StatusDesigning,
		"PromptNeedsReview":   StatusPromptNeedsReview,
		"InquiryNeedsReview":  StatusInquiryNeedsReview,
		"ResearchNeedsReview": StatusResearchNeedsReview,
		"DesignNeedsReview":   StatusDesignNeedsReview,
		"Reviewing":           StatusReviewing,
		"NeedUserInput":       StatusNeedUserInput,
		"FinalReviewing":      StatusFinalReviewing,
		"SettingUpWorktrees":  StatusSettingUpWorktrees,
	}

	// Legacy integer mapping. Values 0-11 are unchanged from the original iota.
	legacyIntMap := map[int]Status{
		0: StatusCreated, 1: StatusResearching, 2: StatusPlanReady,
		3: StatusPlanning, 4: StatusImplementReady, 5: StatusImplementing,
		6: StatusReviewPassed, 7: StatusCodeReady, 8: StatusPublished,
		9: StatusFailed, 10: StatusInterrupted,
		12: StatusDone,
	}

	// Try string first (canonical form for new files)
	var str string
	if err := unmarshal(&str); err == nil {
		// Check if it's a named status string
		if v, ok := stringMap[str]; ok {
			*s = v
			return nil
		}
		// Could be a numeric string from YAML — try parsing as int
		var n int
		if _, err := fmt.Sscanf(str, "%d", &n); err == nil {
			if v, ok := legacyIntMap[n]; ok {
				*s = v
				return nil
			}
			return fmt.Errorf("unknown status integer: %d", n)
		}
		return fmt.Errorf("unknown status: %q", str)
	}

	return fmt.Errorf("cannot unmarshal status: expected string or integer")
}

// Checkpoints controls which phase transitions pause for human review.
type Checkpoints struct {
	InquiryReview   bool `yaml:"inquiry_review,omitempty" json:"inquiry_review,omitempty"`
	ResearchReview  bool `yaml:"research_review,omitempty" json:"research_review,omitempty"`
	DesignReview    bool `yaml:"design_review,omitempty" json:"design_review,omitempty"`
	RoadmapReview   bool `yaml:"roadmap_review,omitempty" json:"roadmap_review,omitempty"`
	PhasePlanReview bool `yaml:"phase_plan_review,omitempty" json:"phase_plan_review,omitempty"`
	ManualPublish   bool `yaml:"manual_publish,omitempty" json:"manual_publish,omitempty"`
	DraftPublish    bool `yaml:"draft_publish,omitempty" json:"draft_publish,omitempty"`
}

// HasGateForPhase returns true if a review gate is enabled for the given target phase.
func (c Checkpoints) HasGateForPhase(phase Phase) bool {
	switch phase {
	case PhaseResearch:
		return c.InquiryReview
	case PhaseDesign:
		return c.ResearchReview
	case PhasePlan:
		return c.DesignReview
	case PhaseImplement:
		return c.PhasePlanReview
	default:
		return false
	}
}

// AutoPublish returns true if manual publish is NOT enabled.
func (c Checkpoints) AutoPublish() bool { return !c.ManualPublish }

// ConfigSnapshot bundles the editable per-feature config axes for
// audit-diff and hook transport. Passed by value; fields mirror the
// feature-level fields on Feature.
type ConfigSnapshot struct {
	Models             config.ModelConfig
	Inquireness        Inquireness
	Checkpoints        Checkpoints
	InputNotifications InputNotificationsMode
}

type InputNotificationsMode string

const (
	InputNotificationsDefault InputNotificationsMode = "default"
	InputNotificationsEnabled InputNotificationsMode = "enabled"
	InputNotificationsMuted   InputNotificationsMode = "muted"
)

func NormalizeInputNotificationsMode(mode InputNotificationsMode) InputNotificationsMode {
	switch mode {
	case InputNotificationsEnabled, InputNotificationsMuted:
		return mode
	default:
		return InputNotificationsDefault
	}
}

func PersistInputNotificationsMode(mode InputNotificationsMode) InputNotificationsMode {
	normalized := NormalizeInputNotificationsMode(mode)
	if normalized == InputNotificationsDefault {
		return ""
	}
	return normalized
}

func InputNotificationsModeForMuted(muted bool) InputNotificationsMode {
	if muted {
		return InputNotificationsMuted
	}
	return InputNotificationsEnabled
}

type PermissionRequest struct {
	Tool    string    `yaml:"tool"`
	Args    string    `yaml:"args"`
	Time    time.Time `yaml:"time"`
	Pending bool      `yaml:"pending"`
}

type HelpRequest struct {
	Question string    `yaml:"question"`
	Answer   string    `yaml:"answer"`
	Time     time.Time `yaml:"time"`
	Pending  bool      `yaml:"pending"`
}

type FeatureRepo struct {
	Name         string `yaml:"name"`
	Path         string `yaml:"path"`
	WorktreePath string `yaml:"worktree_path"`
	Branch       string `yaml:"branch"`
	BaseBranch   string `yaml:"base_branch,omitempty"`
	Publishable  *bool  `yaml:"publishable,omitempty"` // nil = publishable (backward compat); *false = no origin remote
}

// Feature is the top-level aggregate persisted under
// `<stateDir>/<featureID>/feature.yaml`. Per-attempt state (timings, costs,
// iteration counters, artifacts, repo impl tracking, …) lives on the
// companion Run struct persisted under `runs/run-NNN/run.yaml`. The
// per-attempt fields remain addressable on Feature as transient shadows
// (yaml:"-") so the thousands of call sites that read/write them keep
// compiling; Store synchronises shadows <-> Run on every save/load.
type Feature struct {
	ID          string   `yaml:"id"`
	Name        string   `yaml:"name"`
	Slug        string   `yaml:"slug"`
	Description string   `yaml:"description"`
	Summary     string   `yaml:"summary,omitempty"`
	Images      []string `yaml:"images,omitempty"`
	Attachments []string `yaml:"attachments,omitempty"`
	// Tags is legacy inert metadata retained for feature.yaml load/save
	// compatibility. Runtime prompt construction and skill discovery ignore it.
	Tags         []string  `yaml:"tags,omitempty"`
	Created      time.Time `yaml:"created"`
	Status       Status    `yaml:"status"`
	CurrentPhase Phase     `yaml:"current_phase"`

	Repos                []FeatureRepo       `yaml:"repos"`
	Models               config.ModelConfig  `yaml:"models"`
	ExitCriteria         string              `yaml:"exit_criteria"`
	Inquireness          Inquireness         `yaml:"inquireness"`
	PermissionsQueue     []PermissionRequest `yaml:"permissions_queue"`
	HelpQueue            []HelpRequest       `yaml:"help_queue"`
	MaxIterations        int                 `yaml:"max_iterations,omitempty"`
	MaxPlanIterations    int                 `yaml:"max_plan_iterations,omitempty"`
	RiskLevel            RiskLevel           `yaml:"risk_level,omitempty"`
	Pipeline             PipelineProfile     `yaml:"pipeline,omitempty"`
	PipelineUpgradedFrom PipelineProfile     `yaml:"pipeline_upgraded_from,omitempty"` // original profile before UpgradePipeline; used to enforce KB restart on rewind
	Checkpoints          Checkpoints         `yaml:"checkpoints,omitempty"`
	LastAttachedRepo     string              `yaml:"last_attached_repo,omitempty"` // repo name for attach mode tab restoration
	// InputNotifications overrides global notification behavior for this
	// feature's "waiting for input" alerts. Empty means "use global default".
	InputNotifications InputNotificationsMode `yaml:"input_notifications,omitempty"`

	TraceID       string `yaml:"trace_id,omitempty"`        // observability correlation; derived from ID if absent
	FeatureSpanID string `yaml:"feature_span_id,omitempty"` // persisted feature-level span ID so all phases share a common parent

	// Run bookkeeping. ActiveRun is the run currently being mutated; RunCount
	// is the highest run number on disk. Both are 1-indexed. A freshly-created
	// feature has ActiveRun = 1 and RunCount = 1 after Create completes.
	ActiveRun int `yaml:"active_run"`
	RunCount  int `yaml:"run_count"`

	// SchemaVersion is the durable per-feature on-disk-shape marker. Fresh
	// features are stamped SchemaVersionCurrent at Manager.Create time.
	SchemaVersion int `yaml:"schema_version,omitempty"`

	// PendingNeedUserInputPath is the absolute path of the persisted
	// `need-user-input.yaml` gate artifact when the feature is paused on a
	// single-repo NEED_USER_INPUT gate. Empty otherwise. The unified flow
	// tracks NEED_USER_INPUT at the feature level only.
	PendingNeedUserInputPath string `yaml:"-"`

	// --- Transient per-run shadows (yaml:"-"; NOT persisted in feature.yaml) ---
	// Store.saveUnlocked copies these into the companion Run before writing
	// run.yaml; Store.loadUnlocked copies the loaded Run back into these fields
	// so existing call sites (hundreds of them) can keep reading/writing
	// `f.PhaseTimings` etc. directly while the on-disk layout splits across
	// files.
	StartedAt         *time.Time        `yaml:"-"`
	CurrentIteration  int               `yaml:"-"`
	ReviewingGate     bool              `yaml:"-"`
	ReviewFixing      bool              `yaml:"-"`
	ValidatingPlan    bool              `yaml:"-"`
	ValidatorStatuses map[string]string `yaml:"-"`
	PlanIteration     int               `yaml:"-"`
	ReviewIteration   int               `yaml:"-"`
	Artifacts         map[string]string `yaml:"-"`
	// RepoStates is the per-repo state map. agent.AtomicPhaseStamp writes it
	// in a single FeatureStore.Modify call; orchestration readers consume it
	// via Feature.AllReposPublished / Feature.TouchedRepos. Persisted on
	// Run.RepoStates.
	RepoStates     map[string]*RepoState `yaml:"-"`
	RefactorPrompt string                `yaml:"-"`
	// ActiveCycle is the feature-level active post-publish cycle under
	// SchemaVersionCurrent = 4.
	ActiveCycle *CycleState `yaml:"-"`
	// RebaseOperation is the feature-level transient operation display state
	// for the currently active rebase harness / smart rebase / Final Review.
	// Persisted on Run.RebaseOperation.
	RebaseOperation *RebaseOperationState `yaml:"-"`
	// RepoCycles is the per-repo cycle rendering surface kept for the TUI's
	// existing per-repo badge/spinner paths; the unified cycle loops mirror
	// their per-repo entries here so legacy renderers keep working.
	RepoCycles                      map[string]*RepoCycleState `yaml:"-"`
	LastError                       string                     `yaml:"-"`
	FailureType                     string                     `yaml:"-"`
	KBWaitMessage                   string                     `yaml:"-"`
	ForceKBRebuild                  bool                       `yaml:"-"`
	KBStatus                        map[string]string          `yaml:"-"`
	PhaseTimings                    map[string]time.Duration   `yaml:"-"`
	ActivePhaseStart                *time.Time                 `yaml:"-"`
	ActiveTimingKey                 string                     `yaml:"-"`
	PhaseCosts                      map[string]float64         `yaml:"-"`
	SessionCosts                    []SessionCostRecord        `yaml:"-"`
	PendingReviewPhase              *Phase                     `yaml:"-"`
	PendingRewindReviewRoadmapPhase *int                       `yaml:"-"`
	IsRewind                        bool                       `yaml:"-"`
	// CurrentPhaseStatus tracks the mid-flight phase implement status for the
	// unified phase-implement loop ("implementing", "reviewing", or "" when
	// not in a phase). Replaces the per-repo mid-flight presentation tokens
	// retired in SchemaVersionCurrent = 4.
	CurrentPhaseStatus string `yaml:"-"`

	// Roadmap tracking (transient shadows).
	CurrentRoadmapPhase int    `yaml:"-"`
	TotalRoadmapPhases  int    `yaml:"-"`
	RoadmapPhaseType    string `yaml:"-"`

	// Transient: populated by Store.Load, not serialized. Exposed via Run().
	run *Run `yaml:"-"`
}

// WorkspaceSlug is the stable slug used for generated feature branches and
// worktree directories. It keeps the human-readable slug visible while
// qualifying it with the persisted feature ID, avoiding local branch
// collisions across separate state dirs or abandoned setup attempts.
func (f *Feature) WorkspaceSlug() string {
	if f == nil {
		return ""
	}
	return WorkspaceSlug(f.Slug, f.ID)
}

// WorkspaceSlug joins a human-readable slug with the feature ID unless it is
// already qualified.
func WorkspaceSlug(slug, id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return slug
	}
	if slug == "" {
		return id
	}
	if strings.HasSuffix(slug, "-"+id) {
		return slug
	}
	return slug + "-" + id
}

// Run returns the feature's active run. Callers should treat the result as
// always non-nil for a feature loaded via Store.Load. In tests that
// construct a Feature directly without a run, Run lazily creates one mirrored
// against the current shadow fields; subsequent mutations to either the run
// or the shadow fields are reconciled via f.syncShadowsToRun() on save.
func (f *Feature) Run() *Run {
	if f.run == nil {
		rn := f.ActiveRun
		if rn == 0 {
			rn = 1
		}
		f.run = &Run{RunNumber: rn}
		f.syncShadowsToRun()
	}
	return f.run
}

// SetRun installs the given run as the feature's active run in memory.
// Used by Store.Load / Store.SealAndForkRun. Also synchronises the shadow
// fields on Feature to match the new run.
func (f *Feature) SetRun(r *Run) {
	f.run = r
	if r != nil {
		f.syncRunToShadows()
		f.reconcileTerminalRunFailure()
	}
}

// syncShadowsToRun copies the transient shadow fields on Feature into the
// active run. Callers (Store.saveUnlocked and the lazy Run() accessor) must
// hold the semantic right to mutate f.run.
func (f *Feature) syncShadowsToRun() {
	if f.run == nil {
		return
	}
	normalizeLegacyArtifactAliases(f.Artifacts)
	r := f.run
	r.StartedAt = f.StartedAt
	r.CurrentIteration = f.CurrentIteration
	r.ReviewingGate = f.ReviewingGate
	r.ReviewFixing = f.ReviewFixing
	r.ValidatingPlan = f.ValidatingPlan
	r.ValidatorStatuses = f.ValidatorStatuses
	r.PlanIteration = f.PlanIteration
	r.ReviewIteration = f.ReviewIteration
	r.Artifacts = f.Artifacts
	r.RepoStates = f.RepoStates
	r.RefactorPrompt = f.RefactorPrompt
	r.RepoCycles = f.RepoCycles
	r.LastError = f.LastError
	r.FailureType = f.FailureType
	r.KBWaitMessage = f.KBWaitMessage
	r.ForceKBRebuild = f.ForceKBRebuild
	r.KBStatus = f.KBStatus
	r.PhaseTimings = f.PhaseTimings
	r.ActivePhaseStart = f.ActivePhaseStart
	r.ActiveTimingKey = f.ActiveTimingKey
	r.PhaseCosts = f.PhaseCosts
	r.SessionCosts = f.SessionCosts
	r.ActiveCycle = f.ActiveCycle
	r.RebaseOperation = f.RebaseOperation
	r.PendingReviewPhase = f.PendingReviewPhase
	r.PendingRewindReviewRoadmapPhase = f.PendingRewindReviewRoadmapPhase
	r.IsRewind = f.IsRewind
	r.CurrentRoadmapPhase = f.CurrentRoadmapPhase
	r.TotalRoadmapPhases = f.TotalRoadmapPhases
	r.RoadmapPhaseType = f.RoadmapPhaseType
	r.MaxPlanIterations = f.MaxPlanIterations
	r.PendingNeedUserInputPath = f.PendingNeedUserInputPath
	r.CurrentPhaseStatus = f.CurrentPhaseStatus
}

// syncRunToShadows copies run-scoped fields from f.run into Feature's shadow
// fields so existing call sites that read `f.X` see post-load/post-seal state.
func (f *Feature) syncRunToShadows() {
	if f.run == nil {
		return
	}
	r := f.run
	normalizeLegacyArtifactAliases(r.Artifacts)
	f.StartedAt = r.StartedAt
	f.CurrentIteration = r.CurrentIteration
	f.ReviewingGate = r.ReviewingGate
	f.ReviewFixing = r.ReviewFixing
	f.ValidatingPlan = r.ValidatingPlan
	f.ValidatorStatuses = r.ValidatorStatuses
	f.PlanIteration = r.PlanIteration
	f.ReviewIteration = r.ReviewIteration
	f.Artifacts = r.Artifacts
	f.RepoStates = r.RepoStates
	f.RefactorPrompt = r.RefactorPrompt
	f.RepoCycles = r.RepoCycles
	f.LastError = r.LastError
	f.FailureType = r.FailureType
	f.KBWaitMessage = r.KBWaitMessage
	f.ForceKBRebuild = r.ForceKBRebuild
	f.KBStatus = r.KBStatus
	f.PhaseTimings = r.PhaseTimings
	f.ActivePhaseStart = r.ActivePhaseStart
	f.ActiveTimingKey = r.ActiveTimingKey
	f.PhaseCosts = r.PhaseCosts
	f.SessionCosts = r.SessionCosts
	f.ActiveCycle = r.ActiveCycle
	f.RebaseOperation = r.RebaseOperation
	f.PendingReviewPhase = r.PendingReviewPhase
	f.PendingRewindReviewRoadmapPhase = r.PendingRewindReviewRoadmapPhase
	f.IsRewind = r.IsRewind
	f.CurrentRoadmapPhase = r.CurrentRoadmapPhase
	f.TotalRoadmapPhases = r.TotalRoadmapPhases
	f.RoadmapPhaseType = r.RoadmapPhaseType
	f.MaxPlanIterations = r.MaxPlanIterations
	f.PendingNeedUserInputPath = r.PendingNeedUserInputPath
	f.CurrentPhaseStatus = r.CurrentPhaseStatus
}

// HasTerminalFailure reports whether the active run carries terminal failure
// context. These fields are run-scoped shadows, so a feature in a successful
// terminal status with either value set is inconsistent and should be treated
// as failed by status projections and recovery paths.
func (f *Feature) HasTerminalFailure() bool {
	if f == nil {
		return false
	}
	return strings.TrimSpace(f.FailureType) != "" || strings.TrimSpace(f.LastError) != ""
}

func (f *Feature) reconcileTerminalRunFailure() {
	if !f.HasTerminalFailure() {
		return
	}
	switch f.Status {
	case StatusCodeReady, StatusPublished, StatusDone:
		f.Status = StatusFailed
		if f.CurrentPhase == PhasePublish && looksLikeFinalReviewFailure(f.LastError) {
			f.CurrentPhase = PhaseFinalReview
		}
	}
}

func looksLikeFinalReviewFailure(msg string) bool {
	msg = strings.ToLower(msg)
	return strings.Contains(msg, "final_review") || strings.Contains(msg, "final review")
}

func normalizeLegacyArtifactAliases(artifacts map[string]string) {
	if artifacts == nil {
		return
	}
	if strings.TrimSpace(artifacts["design"]) != "" {
		return
	}
	legacy := strings.TrimSpace(artifacts["brainstorm"])
	if legacy == "" {
		return
	}
	artifacts["design"] = legacyDesignArtifactPath(legacy)
}

func legacyDesignArtifactPath(path string) string {
	if filepath.IsAbs(path) || strings.ContainsAny(path, `/\`) {
		return path
	}
	return filepath.Join("brainstorm", path)
}

// validTransitions maps each status to the set of statuses it can transition to.
var validTransitions = map[Status][]Status{
	StatusCreated:             {StatusInquiring, StatusResearching, StatusBuildingKB, StatusPlanReady, StatusFailed},
	StatusResearching:         {StatusDesignReady, StatusPlanReady, StatusFailed, StatusInterrupted},
	StatusBuildingKB:          {StatusCreated, StatusFailed, StatusInterrupted},
	StatusInquiring:           {StatusInquireReady, StatusFailed, StatusInterrupted},
	StatusInquireReady:        {StatusResearching, StatusFailed},
	StatusDesignReady:         {StatusDesigning, StatusFailed},
	StatusDesigning:           {StatusPlanReady, StatusFailed, StatusInterrupted},
	StatusPlanReady:           {StatusPlanning, StatusFailed},
	StatusPlanning:            {StatusImplementReady, StatusPlanNeedsReview, StatusFailed, StatusInterrupted},
	StatusImplementReady:      {StatusImplementing, StatusFailed},
	StatusImplementing:        {StatusReviewPassed, StatusImplementReady, StatusNeedUserInput, StatusFailed, StatusInterrupted},
	StatusNeedUserInput:       {StatusImplementing, StatusFailed, StatusInterrupted},
	StatusReviewPassed:        {StatusCodeReady, StatusDone, StatusImplementing, StatusImplementReady, StatusFailed, StatusPlanning, StatusReviewing, StatusFinalReviewing},
	StatusFinalReviewing:      {StatusCodeReady, StatusReviewPassed, StatusFailed, StatusInterrupted},
	StatusReviewing:           {StatusCodeReady, StatusFailed, StatusInterrupted},
	StatusCodeReady:           {StatusPublished, StatusDone, StatusFailed, StatusImplementReady, StatusInquiring},
	StatusPublished:           {StatusDone, StatusFailed, StatusImplementReady, StatusInquiring, StatusPlanReady},
	StatusDone:                {},
	StatusFailed:              {StatusCreated, StatusBuildingKB, StatusInquiring, StatusResearching, StatusDesigning, StatusImplementReady, StatusCodeReady},
	StatusInterrupted:         {StatusBuildingKB, StatusInquiring, StatusResearching, StatusDesigning, StatusPlanning, StatusImplementing, StatusFinalReviewing, StatusFailed},
	StatusPlanNeedsReview:     {StatusPlanning, StatusImplementReady, StatusFailed},
	StatusPromptNeedsReview:   {StatusInquiring, StatusFailed},
	StatusInquiryNeedsReview:  {StatusResearching, StatusFailed},
	StatusResearchNeedsReview: {StatusDesigning, StatusFailed},
	StatusDesignNeedsReview:   {StatusPlanning, StatusFailed},
	StatusSettingUpWorktrees:  {StatusCreated, StatusFailed},
}

// accumulateActiveTime moves elapsed time from ActivePhaseStart into
// PhaseTimings under the ActiveTimingKey, then clears ActivePhaseStart.
// ActiveTimingKey is intentionally preserved so cycle keys (rebase-N,
// review-comments) survive interrupt/fail transitions and are available when
// the phase is resumed.
func (f *Feature) accumulateActiveTime() {
	if f.ActivePhaseStart == nil || f.ActiveTimingKey == "" {
		return
	}
	elapsed := time.Since(*f.ActivePhaseStart)
	if f.PhaseTimings == nil {
		f.PhaseTimings = make(map[string]time.Duration)
	}
	f.PhaseTimings[f.ActiveTimingKey] += elapsed
	f.ActivePhaseStart = nil
}

// PRURL returns the feature's primary PR URL. Source of truth is the
// per-repo RepoStates[name].PRURL map; this accessor returns the first
// non-empty entry in feature.Repos order, falling back to the run-level
// shadow for legacy fixtures that haven't yet seeded RepoStates.
func (f *Feature) PRURL() string {
	if f == nil {
		return ""
	}
	for _, repo := range f.Repos {
		if state, ok := f.RepoStates[repo.Name]; ok && state != nil && state.PRURL != "" {
			return state.PRURL
		}
	}
	return f.Run().PRURL
}

// SetPRURL updates the run-level PR URL shadow. New code should set
// per-repo PR URLs via RepoStates[name].PRURL; this setter exists so the
// publish path can record a feature-level URL until per-repo wiring is
// complete on every caller.
func (f *Feature) SetPRURL(url string) {
	f.Run().PRURL = url
}

// RebaseCount returns the run-level rebase counter. The design calls
// for per-repo cycle counts; until RepoCycleState carries historical
// counts, this delegates to the run.
func (f *Feature) RebaseCount() int { return f.Run().RebaseCount }

// SetRebaseCount sets the run-level rebase counter.
func (f *Feature) SetRebaseCount(n int) { f.Run().RebaseCount = n }

// RefactorCount returns the run-level refactor counter.
func (f *Feature) RefactorCount() int { return f.Run().RefactorCount }

// SetRefactorCount sets the run-level refactor counter.
func (f *Feature) SetRefactorCount(n int) { f.Run().RefactorCount = n }

// ReviewCommentsCount returns the run-level review-comments cycle counter.
func (f *Feature) ReviewCommentsCount() int { return f.Run().ReviewCommentsCount }

// SetReviewCommentsCount sets the run-level review-comments cycle counter.
func (f *Feature) SetReviewCommentsCount(n int) { f.Run().ReviewCommentsCount = n }

// AddressingReviews returns whether the feature is currently addressing PR
// review comments. Source of truth is the run-level flag; per-repo
// detection is available via RepoCycles[name].Type == CycleReviewComments.
func (f *Feature) AddressingReviews() bool { return f.Run().AddressingReviews }

// SetAddressingReviews sets the run-level addressing-reviews flag.
func (f *Feature) SetAddressingReviews(v bool) { f.Run().AddressingReviews = v }

// ActiveCycleType returns the feature's active post-publish cycle type.
// For per-repo cycle inspection, callers should consult RepoCycles[name].Type
// directly; this accessor preserves the feature-level view used by recovery
// and TUI rendering paths.
func (f *Feature) ActiveCycleType() RepoCycleType { return f.Run().ActiveCycleType }

// SetActiveCycleType sets the feature-level active cycle type.
func (f *Feature) SetActiveCycleType(t RepoCycleType) { f.Run().ActiveCycleType = t }

// IsRefactoring returns true when the feature is in an active refactor cycle.
func (f *Feature) IsRefactoring() bool {
	return f.RefactorPrompt != ""
}

// RefactorPrefix returns the artifact directory prefix for the current
// refactor cycle. Returns empty string when not refactoring.
func (f *Feature) RefactorPrefix() string {
	if f.RefactorCount() > 0 && f.RefactorPrompt != "" {
		return fmt.Sprintf("refactor-%d", f.RefactorCount())
	}
	return ""
}

// CyclePrefix returns the artifact directory prefix for the current
// post-publish cycle (rebase, or review-comments).
// Returns empty string when no cycle is active.
func (f *Feature) CyclePrefix() string {
	switch f.ActiveCycleType() {
	case CycleRebase:
		if f.RebaseCount() > 0 {
			return fmt.Sprintf("rebase-%d", f.RebaseCount())
		}
		return "rebase"
	case CycleReviewComments:
		if f.ReviewCommentsCount() > 0 {
			return fmt.Sprintf("review-comments-%d", f.ReviewCommentsCount())
		}
		return "review-comments"
	}
	return ""
}

// RepoCycleDirName returns the enumerated directory name for a per-repo cycle.
// e.g., "rebase-1", "review-comments-3", or the unenumerated
// fallback when count <= 0.
func RepoCycleDirName(cycleType RepoCycleType, count int) string {
	switch cycleType {
	case CycleRebase:
		if count > 0 {
			return fmt.Sprintf("rebase-%d", count)
		}
	case CycleReviewComments:
		if count > 0 {
			return fmt.Sprintf("review-comments-%d", count)
		}
	case CycleRefactor:
		if count > 0 {
			return fmt.Sprintf("refactor-%d", count)
		}
	}
	return string(cycleType)
}

// EffectivePipeline returns the feature's pipeline profile, defaulting to
// PipelineMoonshot when the field is empty (backward compatibility with
// features created before pipeline profiles existed).
func (f *Feature) EffectivePipeline() PipelineProfile {
	if f.Pipeline == "" {
		return PipelineMoonshot
	}
	return f.Pipeline
}

// HasActiveRepoCycles reports whether any repo has a running, reviewing, or
// need-user-input-paused cycle. Mirrors the predicate used by
// tui.hasActiveRepoCycles and by Manager.HasActiveRepoCycles so callers on a
// loaded *Feature do not need to round-trip through the store. Paused
// (need_user_input) cycles count as active because the feature still has
// outstanding post-publish work waiting on the user.
func (f *Feature) HasActiveRepoCycles() bool {
	for _, rc := range f.RepoCycles {
		if rc == nil {
			continue
		}
		switch rc.Status {
		case RepoCycleRunning, RepoCycleReviewing, RepoCycleNeedUserInput:
			return true
		}
	}
	return false
}

// IsPublishable returns true when ALL repos have an origin remote.
// A nil Publishable pointer (pre-existing features) is treated as publishable
// for backward compatibility.
func (f *Feature) IsPublishable() bool {
	for _, r := range f.Repos {
		if r.Publishable != nil && !*r.Publishable {
			return false
		}
	}
	return true
}

// EffectivePhases returns the pipeline phases for this feature, excluding
// PhasePublish when the feature is not publishable.
func (f *Feature) EffectivePhases() []Phase {
	phases := PhasesForProfile(f.EffectivePipeline())
	if !f.IsPublishable() {
		filtered := make([]Phase, 0, len(phases)-1)
		for _, p := range phases {
			if p != PhasePublish {
				filtered = append(filtered, p)
			}
		}
		return filtered
	}
	return phases
}

// EffectiveDescription returns the combined description (original + refactor prompt)
// during an active refactor, or the original description otherwise.
func (f *Feature) EffectiveDescription() string {
	if f.RefactorPrompt != "" {
		return f.Description + "\n\n## Refactor Request\n\n" + f.RefactorPrompt
	}
	return f.Description
}

// isCycleTimingKey returns true if the key is a multi-phase cycle key
// (e.g. refactor-N) that should not be overwritten by individual phase starters.
func isCycleTimingKey(key string) bool {
	return strings.HasPrefix(key, "refactor-")
}

// isImplementTimingKey returns true if the key belongs to the implement phase
// (initial implementation or any cycle variant).
func isImplementTimingKey(key string) bool {
	return key == "implement" ||
		strings.HasPrefix(key, "rebase-") ||
		strings.HasPrefix(key, "refactor-") ||
		strings.HasSuffix(key, "-impl") ||
		key == "review-comments" ||
		strings.HasPrefix(key, "review-comments-")
}

// TotalRuntime returns the total active runtime for the feature.
// This is the sum of all accumulated phase timings plus any currently
// running phase time. For legacy features without PhaseTimings, falls
// back to time.Since(StartedAt).
func (f *Feature) TotalRuntime() time.Duration {
	if len(f.PhaseTimings) > 0 || f.ActivePhaseStart != nil {
		var total time.Duration
		for _, d := range f.PhaseTimings {
			total += d
		}
		if f.ActivePhaseStart != nil {
			total += time.Since(*f.ActivePhaseStart)
		}
		return total
	}
	if f.StartedAt != nil {
		return time.Since(*f.StartedAt)
	}
	return 0
}

// PhaseRuntime returns the runtime for a specific phase/cycle timing key.
// If the key matches the currently active phase, includes live elapsed time.
func (f *Feature) PhaseRuntime(key string) time.Duration {
	d := f.PhaseTimings[key]
	if f.ActiveTimingKey == key && f.ActivePhaseStart != nil {
		d += time.Since(*f.ActivePhaseStart)
	}
	return d
}

// TotalCost returns the total accumulated cost for the feature across all phases.
func (f *Feature) TotalCost() float64 {
	var total float64
	for _, c := range f.PhaseCosts {
		total += c
	}
	return total
}

// PhaseCost returns the accumulated cost for a specific phase/cycle key.
func (f *Feature) PhaseCost(key string) float64 {
	return f.PhaseCosts[key]
}

// AddPhaseCost adds a cost amount to the given phase/cycle key.
func (f *Feature) AddPhaseCost(key string, cost float64) {
	if cost <= 0 {
		return
	}
	if f.PhaseCosts == nil {
		f.PhaseCosts = make(map[string]float64)
	}
	f.PhaseCosts[key] += cost
}

// RecordSessionCost appends one session-level ledger row and updates the
// phase-level aggregate used by existing dashboard and summary readers.
func (f *Feature) RecordSessionCost(record SessionCostRecord) {
	record.PhaseKey = strings.TrimSpace(record.PhaseKey)
	if record.CostUSD <= 0 || record.PhaseKey == "" {
		return
	}
	record.SessionID = strings.TrimSpace(record.SessionID)
	record.ObserverPhase = strings.TrimSpace(record.ObserverPhase)
	record.RepoName = strings.TrimSpace(record.RepoName)
	f.AddPhaseCost(record.PhaseKey, record.CostUSD)
	f.SessionCosts = append(f.SessionCosts, record)
}

func (f *Feature) Transition(to Status) error {
	allowed, ok := validTransitions[f.Status]
	if !ok {
		return fmt.Errorf("no transitions defined from %s", f.Status)
	}
	for _, s := range allowed {
		if s == to {
			// Accumulate time when leaving a running state for a non-running state
			if f.Status.IsRunning() && !to.IsRunning() {
				f.accumulateActiveTime()
			}
			f.Status = to
			return nil
		}
	}
	return fmt.Errorf("invalid transition from %s to %s", f.Status, to)
}

// PendingUserInputCycles returns the post-publish per-repo cycles currently
// paused on a cycle-scoped NEED_USER_INPUT gate, sorted by repo name. A cycle
// is reported only when its RepoCycleState carries both the paused status and
// a non-empty gate artifact path.
func (f *Feature) PendingUserInputCycles() []PendingUserInputCycle {
	if f == nil || len(f.RepoCycles) == 0 {
		return nil
	}
	var out []PendingUserInputCycle
	for repoName, rc := range f.RepoCycles {
		if rc == nil || rc.Status != RepoCycleNeedUserInput || rc.PendingNeedUserInputPath == "" {
			continue
		}
		out = append(out, PendingUserInputCycle{
			RepoName:  repoName,
			CycleType: rc.Type,
			GatePath:  rc.PendingNeedUserInputPath,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RepoName < out[j].RepoName })
	return out
}

// AllReposPublished returns true when every repo declared on f.Repos has
// either never been touched by a phase (Touched=false → no work to publish)
// or has a non-empty PR URL recorded. Returns false when f or f.Repos is
// empty so callers cannot mistake an unconfigured feature for a published
// one.
func (f *Feature) AllReposPublished() bool {
	if f == nil || len(f.Repos) == 0 {
		return false
	}
	for _, repo := range f.Repos {
		st := f.RepoStates[repo.Name]
		if st == nil {
			continue
		}
		if st.Touched && st.PRURL == "" {
			return false
		}
	}
	return true
}

// TouchedRepos returns the names of repos whose RepoState carries Touched=true,
// sorted lexicographically for deterministic iteration. Replaces
// reposAwaitingFinalReview as the FR-staged-subset reader under the new shape.
func (f *Feature) TouchedRepos() []string {
	if f == nil || len(f.RepoStates) == 0 {
		return nil
	}
	names := make([]string, 0, len(f.RepoStates))
	for name, st := range f.RepoStates {
		if st != nil && st.Touched {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// FirstRepoPRURL returns the PR URL of the first repo (by Repos order) that has one.
// Returns empty string if no repo has a PR URL.
func (f *Feature) FirstRepoPRURL() string {
	for _, repo := range f.Repos {
		if state, ok := f.RepoStates[repo.Name]; ok && state != nil && state.PRURL != "" {
			return state.PRURL
		}
	}
	return ""
}

// IsReviewing reports whether the feature is in a Final Review state. Under
// SchemaVersionCurrent = 4 the mid-flight per-repo "final reviewing" state is
// gone; the feature-level Status (StatusFinalReviewing) is the only signal.
// Kept as a method for backward-compat call sites; cycle paths slated for
// slices 3-7 will retire it alongside their migration.
func (f *Feature) IsReviewing() bool {
	if f == nil {
		return false
	}
	return f.Status == StatusFinalReviewing
}

// PRURLs returns the per-repo PR URL map. Each entry's value is the URL
// from RepoStates[name].PRURL when populated. Repos with no PR URL are
// omitted.
func (f *Feature) PRURLs() map[string]string {
	if f == nil {
		return nil
	}
	out := make(map[string]string, len(f.Repos))
	for _, repo := range f.Repos {
		if state, ok := f.RepoStates[repo.Name]; ok && state != nil && state.PRURL != "" {
			out[repo.Name] = state.PRURL
		}
	}
	return out
}
