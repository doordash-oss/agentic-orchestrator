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
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent/roles"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"gopkg.in/yaml.v3"
)

// PlanAttemptMeta records the outcome of a completed planning attempt.
// Written to plan-attempt-NN-meta.yaml in the plan artifact directory.
//
// AxisVerdicts and AxisDigests are populated by multi-validator phase-plan
// runs and allow the per-axis stall tracker (see axisStallState) to
// reconstruct its state after a session restart — without them, the
// on-stack counter resets and a drifting critic can escape the stall cap.
type PlanAttemptMeta struct {
	Attempt        int               `yaml:"attempt"`
	SessionAttempt int               `yaml:"session_attempt,omitempty"` // provider-session retry within this logical attempt
	AgentStatus    string            `yaml:"agent_status"`              // SUCCESS or FAILED
	ReviewStatus   string            `yaml:"review_status"`             // APPROVED, CHANGES_REQUESTED, or empty
	AxisVerdicts   map[string]string `yaml:"axis_verdicts,omitempty"`   // axis (lowercase) -> "APPROVED"|"CHANGES_REQUESTED"|"ERROR"
	AxisDigests    map[string]string `yaml:"axis_digests,omitempty"`    // axis (lowercase) -> sha256 of the frozen section at this attempt
}

// WritePlanAttemptMeta persists a PlanAttemptMeta to the attempt subdirectory.
func WritePlanAttemptMeta(artifactDir string, meta PlanAttemptMeta) error {
	data, err := yaml.Marshal(meta)
	if err != nil {
		return fmt.Errorf("marshaling plan attempt meta: %w", err)
	}
	attemptDir := filepath.Join(artifactDir, fmt.Sprintf("attempt-%02d", meta.Attempt))
	if err := os.MkdirAll(attemptDir, 0o755); err != nil {
		return fmt.Errorf("creating attempt directory: %w", err)
	}
	return os.WriteFile(filepath.Join(attemptDir, "meta.yaml"), data, 0o644)
}

// readPlanAttemptMeta reads a PlanAttemptMeta from the attempt subdirectory.
func readPlanAttemptMeta(artifactDir string, attempt int) (PlanAttemptMeta, error) {
	path := filepath.Join(artifactDir, fmt.Sprintf("attempt-%02d", attempt), "meta.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return PlanAttemptMeta{}, err
	}
	var meta PlanAttemptMeta
	if err := yaml.Unmarshal(data, &meta); err != nil {
		return PlanAttemptMeta{}, err
	}
	return meta, nil
}

// LastAttemptReviewStatus returns the ReviewStatus of the latest completed
// attempt ("APPROVED", "CHANGES_REQUESTED", or "" if none exists or unreadable).
func LastAttemptReviewStatus(artifactDir string) string {
	n := LatestCompletedPlanAttempt(artifactDir)
	if n == 0 {
		return ""
	}
	meta, err := readPlanAttemptMeta(artifactDir, n)
	if err != nil {
		return ""
	}
	return meta.ReviewStatus
}

// LatestCompletedPlanAttempt scans the artifact directory for attempt-NN/meta.yaml
// directories and returns the highest attempt number where the agent succeeded,
// or 0 if none exist. Failed attempts (agent crashed, interrupted, etc.) are not
// counted because they never produced a usable plan artifact — the same attempt
// number should be retried.
//
// Falls back to the legacy plan-attempt-NN-meta.yaml flat-file format for
// backward compatibility with in-flight features.
func LatestCompletedPlanAttempt(artifactDir string) int {
	entries, err := os.ReadDir(artifactDir)
	if err != nil {
		return 0
	}
	latest := 0
	// New format: attempt-NN/ directories with meta.yaml inside.
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		var n int
		if _, err := fmt.Sscanf(e.Name(), "attempt-%d", &n); err == nil && n > latest {
			metaPath := filepath.Join(artifactDir, e.Name(), "meta.yaml")
			data, readErr := os.ReadFile(metaPath)
			if readErr != nil {
				continue
			}
			var meta PlanAttemptMeta
			if yaml.Unmarshal(data, &meta) != nil {
				continue
			}
			if meta.AgentStatus != agentStatusSuccess {
				continue
			}
			latest = n
		}
	}
	if latest > 0 {
		return latest
	}
	// Legacy fallback: plan-attempt-NN-meta.yaml flat files.
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		var n int
		if _, err := fmt.Sscanf(e.Name(), "plan-attempt-%d-meta.yaml", &n); err == nil && n > latest {
			metaPath := filepath.Join(artifactDir, e.Name())
			data, readErr := os.ReadFile(metaPath)
			if readErr != nil {
				continue
			}
			var meta PlanAttemptMeta
			if yaml.Unmarshal(data, &meta) != nil {
				continue
			}
			if meta.AgentStatus != agentStatusSuccess {
				continue
			}
			latest = n
		}
	}
	return latest
}

func latestPlanRevisionFeedbackAttempt(artifactDir string) (int, string) {
	entries, err := os.ReadDir(artifactDir)
	if err != nil {
		return 0, ""
	}
	best := 0
	bestFeedback := ""
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		var attempt int
		if _, err := fmt.Sscanf(entry.Name(), "attempt-%d", &attempt); err != nil || attempt <= best {
			continue
		}
		feedbackPath := filepath.Join(artifactDir, entry.Name(), "validation-feedback.md")
		data, err := os.ReadFile(feedbackPath)
		if err != nil || strings.TrimSpace(string(data)) == "" {
			continue
		}
		meta, err := readPlanAttemptMeta(artifactDir, attempt)
		if err != nil || meta.ReviewStatus != agentStatusChangesRequested {
			continue
		}
		best = attempt
		bestFeedback = strings.TrimSpace(string(data))
	}
	return best, bestFeedback
}

func nextPlanSessionAttempt(artifactDir string, attempt int) int {
	meta, err := readPlanAttemptMeta(artifactDir, attempt)
	if err != nil || meta.AgentStatus != "FAILED" {
		return 1
	}
	if meta.SessionAttempt < 1 {
		return 2
	}
	return meta.SessionAttempt + 1
}

func planAttemptSessionID(base string, sessionAttempt int) string {
	return retrySessionID(base, sessionAttempt)
}

// AxisApproval is a sticky approval recovered from a prior validation attempt.
// Surfaced in revise prompts so the reviser knows which sections other axes
// have already cleared and must not rewrite (per the "Sticky Approval Respect"
// procedure in skills/revise-roadmap/SKILL.md).
//
// ApprovedDigest is a SHA-256 over the concatenated frozen-section content at
// approval time. runValidatorSet compares it against the current plan's digest
// of the same sections; when they match, the axis short-circuits to APPROVED
// without launching a validator — bypassing validator nondeterminism that
// would otherwise flip an already-approved axis back to CHANGES_REQUESTED on
// unchanged bytes. Empty for artifacts written by older binaries; in that
// case the short-circuit is skipped and the validator runs as before.
type AxisApproval struct {
	Axis            string
	FrozenSections  []string
	ApprovedDigest  string
	ApprovedAttempt int
}

// LoadPriorAxisApprovals walks attempt-NN/ subdirectories under artifactDir
// and returns the LATEST axis-approved-<axis>.md per axis (later attempts win).
// Returns nil when no approvals exist. The returned slice is sorted by axis
// name for deterministic prompt output.
//
// File format (written by writeAxisApprovalArtifact):
//
//	# AxisApproved: <axis>
//	Attempt: NN
//
//	## Frozen Sections
//	- <heading>
//	- <heading>
func LoadPriorAxisApprovals(artifactDir string) []AxisApproval {
	entries, err := os.ReadDir(artifactDir)
	if err != nil {
		return nil
	}
	type attemptDir struct {
		n    int
		path string
	}
	var dirs []attemptDir
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		var n int
		if _, err := fmt.Sscanf(e.Name(), "attempt-%d", &n); err == nil {
			dirs = append(dirs, attemptDir{n: n, path: filepath.Join(artifactDir, e.Name())})
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].n < dirs[j].n })

	// Track the most recent verdict per axis from attempt meta files so a
	// later CHANGES_REQUESTED can invalidate an earlier APPROVED sticky.
	// Before this guard, a critic that emitted a `## Sticky Approval`
	// section alongside a CHANGES_REQUESTED verdict (contract violation)
	// would leave a stale approval that kept propagating to the reviser
	// even as the axis rejected on every subsequent attempt.
	type axisState struct {
		approval            AxisApproval
		hasApproval         bool
		latestVerdictAfter  int // highest attempt N where we saw a verdict after the approval was recorded
		latestVerdictStatus string
	}
	state := make(map[string]*axisState)

	for _, d := range dirs {
		// First: record any axis approvals written this attempt.
		files, err := os.ReadDir(d.path)
		if err == nil {
			for _, f := range files {
				if f.IsDir() {
					continue
				}
				name := f.Name()
				if !strings.HasPrefix(name, "axis-approved-") || !strings.HasSuffix(name, ".md") {
					continue
				}
				data, err := os.ReadFile(filepath.Join(d.path, name))
				if err != nil {
					continue
				}
				approval := parseAxisApprovalArtifact(string(data))
				if approval.Axis == "" {
					continue
				}
				s := state[approval.Axis]
				if s == nil {
					s = &axisState{}
					state[approval.Axis] = s
				}
				s.approval = approval
				s.hasApproval = true
				// Reset rejection tracking: an approval at attempt N
				// supersedes any earlier rejection.
				s.latestVerdictAfter = 0
				s.latestVerdictStatus = ""
			}
		}

		// Second: inspect the attempt's meta.yaml for per-axis verdicts.
		// If an axis currently has a sticky approval but this attempt
		// rejected it, invalidate the approval.
		meta, err := readPlanAttemptMeta(artifactDir, d.n)
		if err != nil {
			continue
		}
		for axis, verdict := range meta.AxisVerdicts {
			s := state[axis]
			if s == nil {
				continue
			}
			s.latestVerdictAfter = d.n
			s.latestVerdictStatus = verdict
			if verdict == ReviewChangesRequested.String() {
				// Regression: drop the prior approval so the reviser
				// isn't told to treat frozen sections as no-touch while
				// the same axis is actively asking for changes.
				s.hasApproval = false
				s.approval = AxisApproval{}
			}
		}
	}

	if len(state) == 0 {
		return nil
	}
	out := make([]AxisApproval, 0, len(state))
	for _, s := range state {
		if s.hasApproval {
			out = append(out, s.approval)
		}
	}
	if len(out) == 0 {
		return nil
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Axis < out[j].Axis })
	return out
}

// parseAxisApprovalArtifact reads the `# AxisApproved: <axis>` header, the
// optional `Attempt:` / `Approved-Digest:` lines, and the `## Frozen Sections`
// bulleted list. Tolerant of missing sections — returns the axis with an empty
// FrozenSections when the list is absent, so the revise prompt still surfaces
// "this axis approved" without inventing a frozen list. Approved-Digest is
// optional for backward compatibility with artifacts written before F1.
func parseAxisApprovalArtifact(s string) AxisApproval {
	var a AxisApproval
	lines := strings.Split(s, "\n")
	inList := false
	for _, raw := range lines {
		trimmed := strings.TrimSpace(raw)
		if rest, ok := strings.CutPrefix(trimmed, "# AxisApproved:"); ok {
			a.Axis = strings.TrimSpace(rest)
			continue
		}
		if rest, ok := strings.CutPrefix(trimmed, "Attempt:"); ok {
			var n int
			if _, err := fmt.Sscanf(strings.TrimSpace(rest), "%d", &n); err == nil {
				a.ApprovedAttempt = n
			}
			continue
		}
		if rest, ok := strings.CutPrefix(trimmed, "Approved-Digest:"); ok {
			a.ApprovedDigest = strings.TrimSpace(rest)
			continue
		}
		if trimmed == "## Frozen Sections" {
			inList = true
			continue
		}
		if !inList {
			continue
		}
		if trimmed == "" {
			continue
		}
		rest, ok := strings.CutPrefix(trimmed, "- ")
		if !ok {
			inList = false
			continue
		}
		if h := strings.TrimSpace(rest); h != "" {
			a.FrozenSections = append(a.FrozenSections, h)
		}
	}
	return a
}

// frozenSectionsDigest computes a SHA-256 digest over the concatenated content
// of each heading in frozen, in the given order, keyed by heading name. A
// stable separator prevents ambiguity when a heading's content is empty or
// when sections are reordered. Returns "" when frozen is empty or every
// section resolves to empty content — the caller should treat "" as "do not
// short-circuit" since there is nothing to compare.
func frozenSectionsDigest(planPath string, frozen []string) string {
	if len(frozen) == 0 {
		return ""
	}
	h := sha256.New()
	any := false
	for _, heading := range frozen {
		section := extractPlanSection(planPath, heading)
		h.Write([]byte(heading))
		h.Write([]byte{0})
		h.Write(section)
		h.Write([]byte{0})
		if len(section) > 0 {
			any = true
		}
	}
	if !any {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

const maxPlanValidationAttempts = 10

// DefaultMaxPlanAttempts is the exported default for TUI use (e.g. extending iterations).
const DefaultMaxPlanAttempts = maxPlanValidationAttempts

// PlanLoopConfig holds configuration for the planning loop with validation.
type PlanLoopConfig struct {
	Feature      *feature.Feature
	FeatureStore ports.FeatureStore
	StateDir     string // feature state directory

	ResearchArtifactPath string // historical primary planning artifact path also used by validators
	// PlanningResearchArtifactPath is the standalone research report path
	// injected only into initial roadmap and phase-plan prompts.
	PlanningResearchArtifactPath string
	// DesignArtifactPath is the absolute path to the design design
	// document, if one was produced. Retained for caller compatibility;
	// downstream planning prompts no longer re-inject it.
	DesignArtifactPath string
	QAFilePaths        []string // paths to Q&A files from inquire/research/design
	KBInfos            []KBInfo // repo knowledge base info
	WorkDir            string   // repo working directory
	AdditionalDirs     []string // additional directories for claude --add-dir

	// MaxAttempts overrides the default maxPlanValidationAttempts.
	// When zero, falls back to the hardcoded constant.
	MaxAttempts int

	// DangerouslySkipPermissions enables --dangerously-skip-permissions for
	// interactive planning sessions.
	DangerouslySkipPermissions bool

	// PermissionCache is the shared permission cache for auto-approving
	// previously remembered tool requests. Nil means no caching.
	PermissionCache *permission.Cache

	// RepoName is the repo scope for permission caching. Empty means global
	// scope. When set (e.g. from a refactor loop), planning sessions will
	// scope permission rules to this repo.
	RepoName string

	// BuildSession creates CLI command args, env vars, and session opts
	// by routing through the provider registry.
	BuildSession func(BuildSessionOpts) ([]string, []string, *ports.SessionOpts, error)

	// ArtifactBaseDir overrides the artifact directory computation.
	// When set, RunRoadmapPlanningLoop uses filepath.Join(ArtifactBaseDir, "roadmap")
	// and RunPhasePlanningLoop uses filepath.Join(ArtifactBaseDir, "phase-NN", "plan")
	// instead of deriving paths from StateDir + Feature.ID.
	// Used by RunRefactorLoop to scope artifacts to the per-repo refactor directory.
	ArtifactBaseDir string

	// SessionStartFunc overrides session.Manager.StartSession in tests.
	// When non-nil, called instead of sm.StartSession. Return
	// ports.ErrSessionShuttingDown to exit the loop cleanly.
	SessionStartFunc func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*ports.SessionOpts) (ports.SessionHandle, error)

	// AskingClause is the pre-resolved "Asking Questions" prompt section
	// from the PromptAdapter for the planning model. Set by PhaseRunner
	// before launching the loop.
	AskingClause string

	// EffortLevel is the pipeline-driven effort level passed to providers.
	EffortLevel llm.EffortLevel

	// SkillsDir is the path to the reconciled skills directory on disk.
	// When non-empty, planning loops compose skill-read instructions in the
	// user prompt instead of loading command templates into the system prompt.
	SkillsDir string

	// GuidelinesDir is the path to the reconciled guidelines directory on disk.
	GuidelinesDir string

	// Observer is the observability facade for lifecycle events. Nil = no-op.
	Observer *observe.Observer

	// PhaseSpanCtx is the phase-level span context. Set by the loop function at startup;
	// all child spans (sessions, validation, validators) derive from this parent.
	// Do not set externally — the loop function manages this.
	PhaseSpanCtx observe.SpanContext

	// FinishOrViolateNudge arms the finish-or-violate auto-continuation retry
	// for planner sessions: the session runs in interactive turn mode and, on a
	// deliberate end_turn without the completion marker, is nudged to finish
	// before a protocol violation is recorded. Resolved per-model from the
	// provider capability, so only capability-positive providers opt in.
	FinishOrViolateNudge bool
}

func (cfg PlanLoopConfig) initialPlanningResearchArtifactPath() string {
	if cfg.PlanningResearchArtifactPath != "" {
		return cfg.PlanningResearchArtifactPath
	}
	if cfg.DesignArtifactPath == "" {
		return cfg.ResearchArtifactPath
	}
	return ""
}

// PlanLoopResult represents the outcome of the planning loop.
type PlanLoopResult struct {
	FinalStatus string // "approved", "needs_human_review", "failed", "interrupted", "protocol_violation"
	Iterations  int
	LastError   string
}

// resolveValidatorModel picks the model to use for plan validation.
// Returns the configured review model, falling back to "sonnet" when empty.
func resolveValidatorModel(cfg PlanLoopConfig) string {
	model := cfg.Feature.Models.Review
	if model == "" {
		model = "sonnet"
	}
	return model
}

// sessionStartFunc matches the SessionStartFunc override field on
// PlanLoopConfig/ImplementConfig and the ports.SessionManager.StartSession
// method signature.
type sessionStartFunc = func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*ports.SessionOpts) (ports.SessionHandle, error)

// resolveSessionStartFunc returns override when set, otherwise sm.StartSession.
func resolveSessionStartFunc(override sessionStartFunc, sm ports.SessionManager) sessionStartFunc {
	if override != nil {
		return override
	}
	return sm.StartSession
}

func roadmapPlannerRoleForAttempt(attempt int) Role {
	if attempt <= 1 {
		return RolePlanRoadmapPlanner
	}
	return RolePlanRoadmapReviser
}

func phasePlanPlannerRoleForAttempt(attempt int) Role {
	if attempt <= 1 {
		return RolePlanPhasePlanner
	}
	return RolePlanPhaseReviser
}

// validatorDomain describes a specialized plan validator.
type validatorDomain struct {
	Name     string // human-readable name (e.g. "Architecture")
	Template string // command template name (e.g. "validate-roadmap-architecture")
}

type validationArtifactKind string

const (
	validationArtifactRoadmap   validationArtifactKind = "roadmap"
	validationArtifactPhasePlan validationArtifactKind = "phase-plan"
)

// phasePlanValidatorsForRisk returns the axis validator set for per-phase
// plans. The default axes are structural + scope; high-risk phases add
// security/performance/testing. Phase plans no longer require a Grounding
// table, so grounding is not part of the phase-plan gate.
func phasePlanValidatorsForRisk(risk feature.RiskLevel) []validatorDomain {
	validators := []validatorDomain{
		{Name: "Structural", Template: "validate-phase-plan-structural"},
		{Name: "Scope", Template: "validate-phase-plan-scope"},
	}
	if risk == feature.RiskHigh {
		validators = append(validators,
			validatorDomain{Name: "Security", Template: "validate-plan-security"},
			validatorDomain{Name: "Performance", Template: "validate-plan-performance"},
			validatorDomain{Name: "Testing", Template: "validate-plan-testing"},
		)
	}
	return validators
}

// roadmapValidatorsForRisk deliberately uses a lighter validator set than
// per-phase planning. Roadmaps are strategic decomposition artifacts; detailed
// test strategy and implementation-level quality bars are validated later when
// each phase gets its own plan. High-risk roadmaps still get early security and
// performance review because those concerns can invalidate the overall shape.
func roadmapValidatorsForRisk(risk feature.RiskLevel) []validatorDomain {
	validators := []validatorDomain{
		{Name: "Architecture", Template: "validate-roadmap-architecture"},
		{Name: "Scope", Template: "validate-roadmap-scope"},
	}
	if risk == feature.RiskHigh {
		validators = []validatorDomain{
			{Name: "Architecture", Template: "validate-roadmap-architecture"},
			{Name: "Security", Template: "validate-plan-security"},
			{Name: "Performance", Template: "validate-plan-performance"},
			{Name: "Scope", Template: "validate-roadmap-scope"},
		}
	}
	return validators
}

// runSpecializedPlanValidation runs a single specialized validator against the plan
// using a domain-specific template.
//
// All validators run in interactive mode so the provider protocol handles the
// full lifecycle (handshake → prompt → tool use → result). For providers that
// support print mode (Claude), stdin is closed after the protocol sends the
// prompt since it's a one-shot evaluation. For providers that don't (Codex),
// stdin must stay open because it carries the JSON-RPC channel.
func runSpecializedPlanValidation(cfg PlanLoopConfig, sm ports.SessionManager, attempt int, attemptDir, planArtifactPath string, domain validatorDomain, parentCtx observe.SpanContext) (ReviewStatus, string, ValidatorMarkers, error) {
	return runSpecializedPlanValidationForArtifact(cfg, sm, attempt, attemptDir, planArtifactPath, domain, validationArtifactPhasePlan, planValidationExtras{}, parentCtx)
}

// planValidationExtras carries optional prompt augmentations that only some
// validators consume. Roadmap and single-phase flows pass the zero value;
// per-phase plan validation populates PriorPhasePlanPaths so the Grounding
// critic knows which prior-phase symbols legitimately exist on the current
// worktree.
type planValidationExtras struct {
	PriorPhasePlanPaths []string
}

func runSpecializedPlanValidationForArtifact(cfg PlanLoopConfig, sm ports.SessionManager, attempt int, attemptDir, planArtifactPath string, domain validatorDomain, kind validationArtifactKind, extras planValidationExtras, parentCtx observe.SpanContext) (ReviewStatus, string, ValidatorMarkers, error) {
	// Deterministic pre-check for the Grounding axis. When the plan's
	// `## Grounding` table has mechanically decidable defects (illegal
	// classification, EXISTS row pointing at a missing path, WILL-BE-CREATED
	// row contradicting an already-present file), short-circuit to
	// CHANGES_REQUESTED with a synthesized exhaustive feedback document
	// instead of paying for a 100KB+ LLM transcript that would land on the
	// same verdict. The feedback file path mirrors what the LLM judge would
	// write so downstream code (UI, log readers) is unaffected.
	if domain.Template == "validate-phase-plan-grounding" {
		if shortCircuit, status, fb := runGroundingPreCheck(cfg, attempt, attemptDir, planArtifactPath); shortCircuit {
			return status, fb, ValidatorMarkers{}, nil
		}
	}

	domainLower := strings.ToLower(domain.Name)
	helperIterDir := filepath.Join(attemptDir, "validate-"+domainLower)
	if err := os.MkdirAll(helperIterDir, 0o755); err != nil {
		return ReviewFailed, "", ValidatorMarkers{}, fmt.Errorf("creating %s validator helper directory: %w", domain.Name, err)
	}
	RemovePhaseComplete(helperIterDir)

	feedbackPath := filepath.Join(helperIterDir, fmt.Sprintf("validation-%s-feedback.md", domainLower))
	parentFeedbackPath := filepath.Join(attemptDir, fmt.Sprintf("validation-%s-feedback.md", domainLower))

	validationPrompt := buildSpecializedValidationPromptForArtifact(cfg.Feature, planArtifactPath, cfg.ResearchArtifactPath, cfg.SkillsDir, feedbackPath, domain, kind, extras)
	validatorSpec, ok := PlanValidatorRoleForSkill(domain.Template)
	if !ok {
		return ReviewFailed, "", ValidatorMarkers{}, fmt.Errorf("missing validator RoleSpec for skill %q", domain.Template)
	}

	promptPath := filepath.Join(helperIterDir, fmt.Sprintf("validation-%s-prompt.md", domainLower))
	validatorModel := resolveValidatorModel(cfg)
	logPath := filepath.Join(helperIterDir, fmt.Sprintf("validation-%s-output.txt", domainLower))
	addDirs := cfg.AdditionalDirs
	if len(addDirs) == 0 {
		addDirs = []string{cfg.StateDir}
	}
	reviewID := fmt.Sprintf("%s-planreview-%s-%02d", cfg.Feature.ID, domainLower, attempt)
	helper := &PhaseRunner{
		SessionManager: sm,
		FeatureStore:   cfg.FeatureStore,
		StateDir:       cfg.StateDir,
		SkillsDir:      cfg.SkillsDir,
		GuidelinesDir:  cfg.GuidelinesDir,
		Observer:       cfg.Observer,
		BuildSessionFn: cfg.BuildSession,
	}
	helperResult, err := helper.RunReadOnlyReviewHelper(context.Background(), ReviewHelperConfig{
		SessionID:              reviewID,
		FeatureID:              cfg.Feature.ID,
		Phase:                  feature.PhasePlan,
		ParentSpanCtx:          parentCtx,
		Model:                  validatorModel,
		Prompt:                 validationPrompt,
		PromptPath:             promptPath,
		FeedbackPath:           feedbackPath,
		HelperIterDir:          helperIterDir,
		Role:                   validatorSpec.Role,
		WorkDir:                cfg.WorkDir,
		RepoName:               cfg.RepoName,
		AdditionalDirs:         addDirs,
		LogPath:                logPath,
		SystemPromptPrefix:     "validation-" + domainLower,
		CompletionAskingClause: cfg.AskingClause,
		EffortLevel:            cfg.EffortLevel,
		Kind:                   ports.KindValidator,
		Label:                  domain.Name,
	})
	if err != nil {
		feedback := ""
		markers := ValidatorMarkers{}
		if helperResult != nil {
			feedback = helperResult.Feedback
			markers = helperResult.Markers
		}
		// The helper writes the per-axis feedback file itself under the
		// new handoff protocol. Synthesize a stub when the helper failed
		// before producing one so downstream consumers (revise prompt,
		// log readers) still see a parseable document.
		if _, statErr := os.Stat(feedbackPath); os.IsNotExist(statErr) {
			stub := FormatStructuredReviewFeedback(
				fmt.Sprintf("%s Validation — Helper Failed", domain.Name),
				fmt.Sprintf("- **Critical**: %s validator terminated before writing %s: %v", domain.Name, filepath.Base(feedbackPath), err),
				"",
				ReviewChangesRequested,
			)
			_ = os.WriteFile(feedbackPath, []byte(stub), 0o644)
			feedback = stub
		}
		if strings.TrimSpace(feedback) != "" {
			_ = os.WriteFile(parentFeedbackPath, []byte(feedback), 0o644)
		}
		if markers.AxisApproved != "" {
			writeAxisApprovalArtifact(attemptDir, attempt, markers, planArtifactPath)
		}
		return ReviewFailed, feedback, markers, fmt.Errorf("running %s validation session: %w", domain.Name, err)
	}

	status := helperResult.Status
	feedback := helperResult.Feedback

	markers := helperResult.Markers
	if strings.TrimSpace(feedback) != "" {
		_ = os.WriteFile(parentFeedbackPath, []byte(feedback), 0o644)
	}
	// Persist a sticky approval whenever the critic actually passed the axis,
	// which includes ReviewApproved — the loop exits on both, so both should
	// grant F1 short-circuit rights on the next attempt. Gating on bare
	// ReviewApproved silently dropped nits-tier approvals, defeating F1 for
	// axes that kept landing in the nits tier.
	//
	// Some critics emit a `## Sticky Approval` block even when their
	// `## Verdict` body is `CHANGES_REQUESTED` (violating the SKILL
	// contract). Persisting that anyway would leave a stale approval for
	// an axis that is actively rejecting — forcing the reviser to treat
	// sections it needs to edit as no-touch — so we require IsApproved() here.
	if status.IsApproved() && markers.AxisApproved != "" {
		writeAxisApprovalArtifact(attemptDir, attempt, markers, planArtifactPath)
	}

	return status, feedback, markers, nil
}

// runGroundingPreCheck runs the deterministic Grounding-table check before the
// LLM judge spins up. Returns shortCircuit=true when the check found defects;
// in that case it has already persisted the synthesized feedback to the same
// `validation-grounding-feedback.md` / `validation-grounding-output.txt` paths
// the LLM path would have written, and the caller MUST return the supplied
// status/feedback verbatim — this axis is decided.
//
// Returns shortCircuit=false when the table is mechanically clean (or the
// plan artifact cannot be read, or the worktree is missing). In that case
// the caller proceeds to the LLM judge as normal — the pre-check is a
// pre-filter, not a replacement.
func runGroundingPreCheck(cfg PlanLoopConfig, attempt int, attemptDir, planArtifactPath string) (bool, ReviewStatus, string) {
	if planArtifactPath == "" || cfg.WorkDir == "" {
		return false, ReviewFailed, ""
	}
	// resolveUnifiedWorkDir sets cfg.WorkDir to the parent of every repo's
	// worktree (so the Claude session's cwd can see siblings via --add-dir).
	// Plan-row references like `docs/keybindings.md` are repo-relative, so
	// resolving them against the worktree-parent would mis-locate every row
	// by one directory. Pass each repo's worktree path explicitly along with
	// its name; the gate accepts a bare-path row when the file exists under
	// any root, and binds a `<repo>:<path>`-prefixed row to the named root
	// only (per the multi-repo grounding contract in plan-phase/format.md).
	roots := groundingRootsForFeature(cfg.Feature)
	if len(roots) == 0 {
		// Single-repo non-worktree fallback or feature with no repos: use
		// the legacy single-root behaviour with no name (anonymous root).
		roots = []GroundingRoot{{Worktree: cfg.WorkDir}}
	}
	result := CheckGroundingTableRepos(planArtifactPath, roots)
	if result.OK() {
		return false, ReviewFailed, ""
	}
	headRev, branch := captureWorktreePreflight(cfg.WorkDir)
	feedback := FormatGroundingPreCheckFeedback(result, headRev, branch)
	feedbackPath := filepath.Join(attemptDir, "validation-grounding-feedback.md")
	_ = os.WriteFile(feedbackPath, []byte(feedback), 0o644)
	// Also write the same text to validation-grounding-output.txt so the
	// review log readers / TUI surface a coherent transcript even though no
	// LLM session ran. Including the attempt number keeps grep-friendly with
	// the LLM-written outputs.
	outputPath := filepath.Join(attemptDir, "validation-grounding-output.txt")
	header := fmt.Sprintf("[grounding pre-check] attempt=%d (no LLM session — deterministic gate decided this axis)\n\n", attempt)
	_ = os.WriteFile(outputPath, []byte(header+feedback), 0o644)
	return true, ReviewChangesRequested, feedback
}

// captureWorktreePreflight returns `git rev-parse HEAD` and
// `git branch --show-current` for the worktree, mirroring the Pre-flight
// block the LLM judge produces. Empty strings on any error — the feedback
// formatter handles missing values. Uses ports.CommandRunner so the agent
// package's "no direct exec" invariant (exec_command_regression_test.go) is
// preserved.
func captureWorktreePreflight(workDir string) (string, string) {
	runner := NewExecCommandRunner()
	run := func(args ...string) string {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		out, err := runner.Run(ctx, "git", args, ports.CommandOpts{Dir: workDir})
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	return run("rev-parse", "HEAD"), run("branch", "--show-current")
}

// groundingRootsForFeature returns the named worktree roots the
// deterministic grounding gate should resolve plan-row references against.
// Prefers each repo's WorktreePath (the canonical feature-branch checkout
// under the unified flow) and falls back to Path for non-worktree mode.
// Each root carries the repo Name so the gate can route `<repo>:<path>`
// references (per plan-phase/format.md) to the matching worktree. Returns
// an empty slice when the feature has no repos so callers can supply a
// fallback root.
func groundingRootsForFeature(f *feature.Feature) []GroundingRoot {
	if f == nil {
		return nil
	}
	roots := make([]GroundingRoot, 0, len(f.Repos))
	for _, r := range f.Repos {
		switch {
		case r.WorktreePath != "":
			roots = append(roots, GroundingRoot{Name: r.Name, Worktree: r.WorktreePath})
		case r.Path != "":
			roots = append(roots, GroundingRoot{Name: r.Name, Worktree: r.Path})
		}
	}
	return roots
}

// axisFrozenHeading maps a validator axis name (lowercased) to the plan
// heading whose content that axis is responsible for. Used by axisStallState
// to fingerprint the relevant section across attempts and detect when the
// same axis keeps failing without the plan section changing.
var axisFrozenHeading = map[string]string{
	"structural": "## Tasks",
	"scope":      "## Tasks",
}

// axisStallLimit is the number of consecutive identical-section failures on
// the same axis that cause the phase-planning loop to escalate to
// needs_human_review. Three means: two retries past the first failure with no
// material change — strong evidence the critic is drifting rather than the
// planner producing a new artifact.
const axisStallLimit = 3

// axisStallState tracks how many consecutive attempts each axis has failed
// while the plan section it freezes has not changed. Zero value is ready to
// use. Held on-stack by RunPhasePlanningLoop; not persisted across restarts.
type axisStallState struct {
	// lastDigest is the digest of the frozen section on the most recent
	// attempt that failed for this axis. When the next attempt's digest
	// equals this value, we consider the axis "stalled" for this attempt.
	lastDigest map[string]string
	// count is the current consecutive-stall count per axis.
	count map[string]int
}

// observe updates the tracker with the validator results from one attempt and
// reports whether any axis has now reached axisStallLimit. When stalled is
// true, axis is the offending axis name and count is the current stall count.
//
// Also returns the per-axis verdicts and digests collected for this attempt
// so the caller can persist them in PlanAttemptMeta (enabling replay after
// a session restart via loadAxisStallState).
func (s *axisStallState) observe(attempt int, planPath string, results []ValidatorResult) (stalled bool, axis string, count int, verdicts, digests map[string]string) {
	verdicts = make(map[string]string, len(results))
	digests = make(map[string]string, len(results))
	for _, r := range results {
		axisName := strings.ToLower(r.Domain)
		verdict := r.Status.String()
		if r.Error != nil {
			verdict = "ERROR"
		}
		verdicts[axisName] = verdict

		heading := axisFrozenHeading[axisName]
		if heading != "" {
			if digest := planSectionDigest(planPath, heading); digest != "" {
				digests[axisName] = digest
			}
		}

		if stalled {
			// Already tripped on a previous axis this attempt; keep
			// collecting verdicts/digests for persistence but don't
			// overwrite the returned axis/count.
			continue
		}
		if st, ax, ct := s.observeAxis(axisName, verdict, digests[axisName]); st {
			stalled, axis, count = true, ax, ct
		}
	}
	return stalled, axis, count, verdicts, digests
}

// observeAxis updates the tracker for a single axis and returns whether that
// axis has now reached axisStallLimit. Separated from observe() so
// loadAxisStallState can replay persisted per-attempt state without needing
// the original ValidatorResult values.
func (s *axisStallState) observeAxis(axis, verdict, digest string) (stalled bool, name string, count int) {
	if s.lastDigest == nil {
		s.lastDigest = make(map[string]string)
		s.count = make(map[string]int)
	}
	if verdict != ReviewChangesRequested.String() {
		// Approved or errored axes reset their counter so a subsequent
		// regression starts fresh.
		delete(s.lastDigest, axis)
		delete(s.count, axis)
		return false, "", 0
	}
	if _, known := axisFrozenHeading[axis]; !known {
		// Unknown axis — we can't fingerprint, so skip stall detection
		// rather than risk a false positive.
		return false, "", 0
	}
	if digest == "" {
		// No section to fingerprint — don't count.
		return false, "", 0
	}
	if prev, ok := s.lastDigest[axis]; ok && prev == digest {
		s.count[axis]++
	} else {
		s.count[axis] = 1
	}
	s.lastDigest[axis] = digest
	if s.count[axis] >= axisStallLimit {
		return true, axis, s.count[axis]
	}
	return false, "", 0
}

// loadAxisStallState reconstructs an axisStallState from the per-attempt
// PlanAttemptMeta files persisted under artifactDir. Walks attempts in
// ascending order and replays observeAxis for every axis verdict recorded —
// letting the stall cap survive session restarts. Missing fields (older
// attempts without axis_verdicts / axis_digests) are silently skipped, which
// makes this safe to call on pre-existing feature directories.
func loadAxisStallState(artifactDir string) axisStallState {
	var state axisStallState
	entries, err := os.ReadDir(artifactDir)
	if err != nil {
		return state
	}
	type attemptRef struct {
		n    int
		meta PlanAttemptMeta
	}
	var refs []attemptRef
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		var n int
		if _, err := fmt.Sscanf(e.Name(), "attempt-%d", &n); err != nil {
			continue
		}
		meta, err := readPlanAttemptMeta(artifactDir, n)
		if err != nil {
			continue
		}
		refs = append(refs, attemptRef{n: n, meta: meta})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].n < refs[j].n })
	for _, r := range refs {
		for axis, verdict := range r.meta.AxisVerdicts {
			digest := r.meta.AxisDigests[axis]
			_, _, _ = state.observeAxis(axis, verdict, digest)
		}
	}
	return state
}

// planSectionDigest returns a SHA-256 hex digest of the normalized content of
// the Markdown section whose heading exactly matches the given string.
// Returns "" when the file cannot be read or the section is absent.
func planSectionDigest(planPath, heading string) string {
	section := extractPlanSection(planPath, heading)
	if len(section) == 0 {
		return ""
	}
	sum := sha256.Sum256(section)
	return hex.EncodeToString(sum[:])
}

// extractPlanSection returns the byte content of the Markdown section whose
// heading line exactly matches heading (e.g. "## Grounding"). The returned
// bytes start at the line AFTER the heading and end before the next line
// beginning with "## " or "# " (or EOF). Trailing whitespace on each line is
// trimmed before return so cosmetic edits don't perturb the digest.
func extractPlanSection(planPath, heading string) []byte {
	if planPath == "" {
		return nil
	}
	data, err := os.ReadFile(planPath)
	if err != nil {
		return nil
	}
	lines := strings.Split(string(data), "\n")
	var out []string
	inSection := false
	for _, line := range lines {
		if !inSection {
			if strings.TrimRight(line, " \t") == heading {
				inSection = true
			}
			continue
		}
		if strings.HasPrefix(line, "## ") || strings.HasPrefix(line, "# ") {
			break
		}
		out = append(out, strings.TrimRight(line, " \t"))
	}
	if !inSection {
		return nil
	}
	return []byte(strings.Join(out, "\n"))
}

// writeAxisApprovalArtifact persists a sticky-approval marker so later revise
// attempts can load it via LoadPriorAxisApprovals. The filename uses the axis
// name from the skill contract so multiple approved axes coexist within one
// attempt directory.
//
// The Approved-Digest line records a SHA-256 of the frozen-section content as
// of this approval so runValidatorSet can short-circuit re-validation on a
// later attempt when the digest is unchanged. planPath may be empty for
// callers that cannot compute a digest (in which case the line is omitted and
// short-circuiting is disabled for this approval — strictly safer).
func writeAxisApprovalArtifact(attemptDir string, attempt int, markers ValidatorMarkers, planPath string) {
	var b strings.Builder
	fmt.Fprintf(&b, "# AxisApproved: %s\n", markers.AxisApproved)
	fmt.Fprintf(&b, "Attempt: %d\n", attempt)
	if digest := frozenSectionsDigest(planPath, markers.FrozenSections); digest != "" {
		fmt.Fprintf(&b, "Approved-Digest: %s\n", digest)
	}
	b.WriteString("\n## Frozen Sections\n")
	for _, s := range markers.FrozenSections {
		fmt.Fprintf(&b, "- %s\n", s)
	}
	path := filepath.Join(attemptDir, fmt.Sprintf("axis-approved-%s.md", markers.AxisApproved))
	_ = os.WriteFile(path, []byte(b.String()), 0o644)
}

// buildSpecializedValidationPromptForArtifact constructs the prompt for a
// per-axis specialized validator (architecture, scope, security, performance,
// testing, grounding, structural).
//
// skillsDir and feedbackPath are retained on the signature for caller
// stability. The RoleSpec-backed system prompt and validator SKILL.md Output
// Files section now own the skill path and per-axis feedback path.
//
// The prose lives in
// internal/agent/prompts/templates/validate_specialized.user.tmpl.
func buildSpecializedValidationPromptForArtifact(f *feature.Feature, planPath, researchPath, skillsDir, feedbackPath string, domain validatorDomain, kind validationArtifactKind, extras planValidationExtras) string {
	_ = skillsDir
	includePriorPhase := domain.Template == "validate-phase-plan-grounding" && len(extras.PriorPhasePlanPaths) > 0

	return roles.BuildValidateSpecializedPrompt(roles.ValidateSpecializedUserInput{
		Name:                     f.Name,
		Description:              f.EffectiveDescription(),
		ExitCriteria:             f.ExitCriteria,
		RiskLevel:                string(f.RiskLevel),
		DomainName:               domain.Name,
		PlanPath:                 planPath,
		IncludePriorPhaseContext: includePriorPhase,
		PriorPhasePlanPaths:      append([]string(nil), extras.PriorPhasePlanPaths...),
		IsRoadmapKind:            kind == validationArtifactRoadmap,
		ResearchPath:             researchPath,
		FeedbackPath:             feedbackPath,
		AxisLabel:                strings.ToLower(domain.Name),
	})
}

// ValidatorResult holds the outcome of a single specialized validator.
//
// AxisApproved / FrozenSections carry the sticky-approval signal emitted by
// roadmap validators so the next revise attempt can preserve approved-axis
// sections byte-equal. Populated from raw validator output via
// ParseValidatorMarkers.
type ValidatorResult struct {
	Domain         string
	Status         ReviewStatus
	Feedback       string
	Error          error
	AxisApproved   string
	FrozenSections []string
}

// runRoadmapMultiValidatorPlanValidation applies a roadmap-specific validator
// subset so strategic roadmaps are not forced to satisfy the full phase-plan
// quality bar before the per-phase plans exist.
func runRoadmapMultiValidatorPlanValidation(cfg PlanLoopConfig, sm ports.SessionManager, attempt int, attemptDir, planArtifactPath string) (ReviewStatus, string, error) {
	validators := roadmapValidatorsForRisk(cfg.Feature.RiskLevel)
	_, status, feedback, err := runValidatorSet(cfg, sm, attempt, attemptDir, planArtifactPath, validators, validationArtifactRoadmap, planValidationExtras{})
	return status, feedback, err
}

// runPhasePlanMultiValidatorValidation applies the per-phase axis validator set
// (structural + grounding + scope, plus high-risk deep-dive) against a phase
// plan artifact. Each axis writes a `## Sticky Approval` block into its
// per-axis review-feedback file, which runValidatorSet persists to
// attempt-NN/axis-approved-<axis>.md for sticky approval across revise
// iterations.
//
// PriorPhasePlanPaths is forwarded via planValidationExtras so the Grounding
// critic can tell legitimate prior-phase symbols apart from unfounded EXISTS
// claims — without it, the critic falsely fails every reference to a symbol
// committed by an earlier phase on the same branch.
//
// Returns per-axis results alongside the aggregated verdict so the phase
// planning loop can detect per-axis stalls (see detectAxisStall).
func runPhasePlanMultiValidatorValidation(cfg PhasePlanLoopConfig, sm ports.SessionManager, attempt int, attemptDir, planArtifactPath string) ([]ValidatorResult, ReviewStatus, string, error) {
	validators := phasePlanValidatorsForRisk(cfg.Feature.RiskLevel)
	extras := planValidationExtras{PriorPhasePlanPaths: cfg.PriorPhasePlanPaths}
	return runValidatorSet(cfg.PlanLoopConfig, sm, attempt, attemptDir, planArtifactPath, validators, validationArtifactPhasePlan, extras)
}

func runValidatorSet(cfg PlanLoopConfig, sm ports.SessionManager, attempt int, attemptDir, planArtifactPath string, validators []validatorDomain, kind validationArtifactKind, extras planValidationExtras) ([]ValidatorResult, ReviewStatus, string, error) {
	// Validation span — child of phase span
	validationCtx := cfg.PhaseSpanCtx.Child()
	validationStart := time.Now()
	cfg.Observer.ValidationStarted(validationCtx, "plan", len(validators))

	results := make([]ValidatorResult, len(validators))

	// Initialize validator statuses for TUI display
	setValidatorStatuses(cfg, validators, nil)

	// Sticky-approval short-circuit: for each validator whose axis already
	// approved on a prior attempt, compare the stored frozen-section digest
	// against the current plan. When they match, the reviser cannot have
	// changed anything this axis would re-reject, so re-launching the
	// validator only exposes the approval to nondeterminism. Skip it and
	// synthesize an APPROVED result. A missing digest (artifacts written by
	// older binaries, or an empty FrozenSections list) returns "" from
	// frozenSectionsDigest and disables short-circuiting for that axis —
	// strictly safer.
	priorApprovals := make(map[string]AxisApproval, len(validators))
	if parent := filepath.Dir(attemptDir); parent != "" && parent != "." {
		for _, a := range LoadPriorAxisApprovals(parent) {
			priorApprovals[strings.ToLower(a.Axis)] = a
		}
	}

	// Fan out validators in parallel. Each validator is an independent
	// read-only bounded helper session — no shared mutable state except the
	// results slice (indexed writes, no race) and the feature store / observer
	// (both goroutine-safe). Wall-clock ≈ slowest validator instead of sum.
	var wg sync.WaitGroup
	for i, v := range validators {
		axisName := strings.ToLower(v.Name)
		if approval, ok := priorApprovals[axisName]; ok && approval.ApprovedDigest != "" {
			currentDigest := frozenSectionsDigest(planArtifactPath, approval.FrozenSections)
			if currentDigest != "" && currentDigest == approval.ApprovedDigest {
				results[i] = ValidatorResult{
					Domain:         v.Name,
					Status:         ReviewApproved,
					AxisApproved:   approval.Axis,
					FrozenSections: approval.FrozenSections,
				}
				// Observer accounting: emit a zero-duration start/complete pair so
				// span counts stay consistent with the non-cached path.
				skipCtx := validationCtx.Child()
				cfg.Observer.ValidatorStarted(skipCtx, v.Name)
				cfg.Observer.ValidatorCompleted(skipCtx, v.Name, "APPROVED (cached)", 0)
				updateValidatorStatus(cfg, v.Name, ReviewApproved, nil)
				continue
			}
		}
		wg.Add(1)
		go func(i int, v validatorDomain) {
			defer wg.Done()
			// Validator span — child of validation span
			validatorCtx := validationCtx.Child()
			validatorStart := time.Now()
			cfg.Observer.ValidatorStarted(validatorCtx, v.Name)

			status, feedback, markers, err := runSpecializedPlanValidationForArtifact(cfg, sm, attempt, attemptDir, planArtifactPath, v, kind, extras, validatorCtx)
			results[i] = ValidatorResult{
				Domain:         v.Name,
				Status:         status,
				Feedback:       feedback,
				Error:          err,
				AxisApproved:   markers.AxisApproved,
				FrozenSections: markers.FrozenSections,
			}

			// Validator completed
			verdict := status.String()
			if err != nil {
				verdict = "error"
			}
			cfg.Observer.ValidatorCompleted(validatorCtx, v.Name, verdict, time.Since(validatorStart))

			updateValidatorStatus(cfg, v.Name, status, err)
		}(i, v)
	}
	wg.Wait()

	// Clear validator statuses after completion
	clearValidatorStatuses(cfg)

	if isFeatureInterrupted(cfg.FeatureStore, cfg.Feature.ID) {
		cfg.Observer.ValidationCompleted(validationCtx, "plan", "interrupted", time.Since(validationStart), len(validators))
		return results, ReviewFailed, "", fmt.Errorf("feature interrupted during multi-validator validation")
	}

	aggStatus, aggFeedback, aggErr := composeValidatorResults(results, cfg.Feature.RiskLevel)
	for _, result := range results {
		if isProtocolViolationError(result.Error) {
			aggErr = result.Error
			break
		}
	}

	aggVerdict := aggStatus.String()
	if aggErr != nil {
		aggVerdict = "error"
	}
	cfg.Observer.ValidationCompleted(validationCtx, "plan", aggVerdict, time.Since(validationStart), len(validators))

	return results, aggStatus, aggFeedback, aggErr
}

// setValidatorStatuses initializes the validator status map for TUI display.
func setValidatorStatuses(cfg PlanLoopConfig, validators []validatorDomain, results []ValidatorResult) {
	if cfg.FeatureStore == nil {
		return
	}
	_ = cfg.FeatureStore.Modify(cfg.Feature.ID, func(f *feature.Feature) error {
		f.ValidatorStatuses = make(map[string]string, len(validators))
		for _, v := range validators {
			f.ValidatorStatuses[v.Name] = "running"
		}
		return nil
	})
}

// updateValidatorStatus updates a single validator's status for TUI display.
func updateValidatorStatus(cfg PlanLoopConfig, domain string, status ReviewStatus, err error) {
	if cfg.FeatureStore == nil {
		return
	}
	_ = cfg.FeatureStore.Modify(cfg.Feature.ID, func(f *feature.Feature) error {
		if f.ValidatorStatuses == nil {
			f.ValidatorStatuses = make(map[string]string)
		}
		if err != nil {
			f.ValidatorStatuses[domain] = "error"
		} else {
			f.ValidatorStatuses[domain] = status.String()
		}
		return nil
	})
}

// clearValidatorStatuses removes validator statuses after validation completes.
func clearValidatorStatuses(cfg PlanLoopConfig) {
	if cfg.FeatureStore == nil {
		return
	}
	_ = cfg.FeatureStore.Modify(cfg.Feature.ID, func(f *feature.Feature) error {
		f.ValidatorStatuses = nil
		return nil
	})
}

// composeValidatorResults merges individual validator results into a single
// verdict and combined feedback string. The summary table gives reviewers an
// at-a-glance view; detailed findings follow per domain.
//
// Verdict rules:
//   - Any validator ERROR or CHANGES_REQUESTED → overall CHANGES_REQUESTED.
//   - All validators APPROVED → overall APPROVED.
func composeValidatorResults(results []ValidatorResult, risk feature.RiskLevel) (ReviewStatus, string, error) {
	var b strings.Builder
	hasError := false
	approvedCount := 0
	nitsCount := 0
	changesCount := 0

	// Summary table
	b.WriteString("# Multi-Validator Plan Review\n\n")
	b.WriteString("| Validator | Weight | Verdict |\n")
	b.WriteString("|-----------|--------|---------|\n")
	for _, r := range results {
		weight := validatorWeight(r.Domain)
		verdict := r.Status.String()
		switch {
		case r.Error != nil:
			verdict = "ERROR"
			hasError = true
		case r.Status == ReviewApproved:
			approvedCount++
		case r.Status == ReviewChangesRequested:
			changesCount++
		}
		b.WriteString(fmt.Sprintf("| %s | %s | %s |\n", r.Domain, weight, verdict))
	}
	b.WriteString("\n")

	var overallStatus ReviewStatus
	var overall string
	switch {
	case hasError || changesCount > 0 || approvedCount < len(results):
		overall = agentStatusChangesRequested
		overallStatus = ReviewChangesRequested
	default:
		overall = agentStatusApproved
		overallStatus = ReviewApproved
	}
	b.WriteString(fmt.Sprintf("**Overall: %s** (%d/%d validators approved", overall, approvedCount, len(results)))
	if nitsCount > 0 {
		b.WriteString(fmt.Sprintf(", %d with nits", nitsCount))
	}
	b.WriteString(")\n\n")

	if risk == feature.RiskHigh {
		b.WriteString("> **HIGH-RISK**: Senior review is required regardless of validator results.\n\n")
	}

	// Detailed findings per validator. Nits are surfaced even on the
	// approved-with-nits path so downstream readers can see what was
	// intentionally deferred past the loop exit.
	for _, r := range results {
		if r.Error != nil {
			b.WriteString(fmt.Sprintf("## %s Validator — ERROR\n\n%v\n\n", r.Domain, r.Error))
			continue
		}
		if r.Status == ReviewChangesRequested && r.Feedback != "" {
			b.WriteString(fmt.Sprintf("## %s Validator — CHANGES REQUESTED\n\n%s\n\n", r.Domain, r.Feedback))
		}
	}

	return overallStatus, b.String(), nil
}

// validatorWeight returns the display weight for a validator domain.
func validatorWeight(domain string) string {
	switch domain {
	case "Architecture":
		return "30%"
	case "Security":
		return "30%"
	case "Performance":
		return "20%"
	case "Testing":
		return "20%"
	default:
		return "—"
	}
}

// PhasePlanLoopConfig extends PlanLoopConfig with phase-specific fields.
type PhasePlanLoopConfig struct {
	PlanLoopConfig                   // embed existing config
	RoadmapPath         string       // path to approved roadmap
	Phase               RoadmapPhase // current phase info
	PriorPhasePlanPaths []string     // approved plan paths from completed phases
}

// RunRoadmapPlanningLoop creates and validates a roadmap.
// Uses roadmap-specific templates and prompts.
func RunRoadmapPlanningLoop(cfg PlanLoopConfig, sm ports.SessionManager) (result *PlanLoopResult, retErr error) {
	// Phase lifecycle
	featureCtx := observe.SpanContextForFeature(cfg.Feature.ID, cfg.Feature.TraceID, cfg.Feature.Name, cfg.Feature.FeatureSpanID).WithRun(cfg.Feature.ActiveRun)
	cfg.PhaseSpanCtx = featureCtx.Child()
	phaseStart := time.Now()
	cfg.Observer.PhaseStarted(cfg.PhaseSpanCtx, "plan")
	defer func() {
		var phaseErr error
		if retErr != nil {
			phaseErr = retErr
		} else if result != nil && result.FinalStatus != "approved" {
			phaseErr = fmt.Errorf("%s", result.FinalStatus)
		}
		cfg.Observer.PhaseCompleted(cfg.PhaseSpanCtx, "plan", time.Since(phaseStart), phaseErr)
	}()

	var artifactDir string
	if cfg.ArtifactBaseDir != "" {
		artifactDir = filepath.Join(cfg.ArtifactBaseDir, "roadmap")
	} else {
		baseDir := filepath.Join(ActiveRunDir(cfg.StateDir, cfg.Feature), cfg.Feature.RefactorPrefix())
		artifactDir = filepath.Join(baseDir, "roadmap")
	}
	_ = os.MkdirAll(artifactDir, 0o755)

	maxAttempts := cfg.MaxAttempts
	if maxAttempts == 0 {
		maxAttempts = maxPlanValidationAttempts
	}

	startAttempt := LatestCompletedPlanAttempt(artifactDir)
	resumeValidation := false
	var criticFeedback string
	feedbackAttempt, feedback := latestPlanRevisionFeedbackAttempt(artifactDir)
	if feedbackAttempt > startAttempt {
		startAttempt = feedbackAttempt
		criticFeedback = feedback
	} else if startAttempt > 0 {
		meta, err := readPlanAttemptMeta(artifactDir, startAttempt)
		if err == nil {
			if meta.ReviewStatus == agentStatusApproved {
				return &PlanLoopResult{FinalStatus: "approved", Iterations: startAttempt}, nil
			}
			if meta.ReviewStatus == "VALIDATION_PENDING" {
				resumeValidation = true
			}
			if meta.ReviewStatus == agentStatusChangesRequested {
				feedbackPath := filepath.Join(artifactDir, fmt.Sprintf("attempt-%02d", startAttempt), "validation-feedback.md")
				if data, readErr := os.ReadFile(feedbackPath); readErr == nil {
					criticFeedback = strings.TrimSpace(string(data))
				}
			}
		}
	} else if feedbackAttempt > 0 {
		startAttempt = feedbackAttempt
		criticFeedback = feedback
	}

	// When resuming validation, re-enter the loop at the pending attempt
	// instead of starting a new planning session.
	loopStart := startAttempt + 1
	if resumeValidation {
		loopStart = startAttempt
	}

roadmapAttemptLoop:
	for attempt := loopStart; attempt <= maxAttempts; attempt++ {
		if isFeatureInterrupted(cfg.FeatureStore, cfg.Feature.ID) {
			return &PlanLoopResult{FinalStatus: "interrupted", Iterations: attempt - 1}, nil
		}

		// Create per-attempt directory for debug prompts, logs, and validation files.
		attemptDir := filepath.Join(artifactDir, fmt.Sprintf("attempt-%02d", attempt))
		if err := os.MkdirAll(attemptDir, 0o755); err != nil {
			return nil, fmt.Errorf("creating attempt directory: %w", err)
		}

		if cfg.FeatureStore != nil {
			_ = cfg.FeatureStore.Modify(cfg.Feature.ID, func(f *feature.Feature) error {
				f.PlanIteration = attempt
				return nil
			})
		}

		// Skip Phase A when resuming from interrupted validation —
		// the plan artifact already exists, just re-run validators.
		if resumeValidation {
			resumeValidation = false
		} else {
			var prompt string
			var plannerSpec RoleSpec
			if attempt == 1 {
				prompt = BuildRoadmapPromptWithResearch(cfg.Feature, cfg.SkillsDir, cfg.GuidelinesDir, cfg.DesignArtifactPath, cfg.initialPlanningResearchArtifactPath(), cfg.QAFilePaths, cfg.KBInfos...)
				plannerSpec = RoadmapCreatorRoleSpec()
			} else {
				prevRoadmapPath := resolvePlanArtifactPath(cfg.FeatureStore, cfg.Feature.ID, artifactDir)
				approvals := LoadPriorAxisApprovals(artifactDir)
				prompt = BuildRoadmapRevisionPrompt(cfg.Feature, cfg.SkillsDir, prevRoadmapPath, prevRoadmapPath, criticFeedback, cfg.DesignArtifactPath, attempt, approvals)
				plannerSpec = RoadmapReviserRoleSpec()
			}

			systemPrompt := BuildRoleSystemPrompt(BuildRoleSystemPromptInput{
				Spec:          plannerSpec,
				IterationDir:  attemptDir,
				SkillsDir:     cfg.SkillsDir,
				GuidelinesDir: cfg.GuidelinesDir,
				KBInfos:       cfg.KBInfos,
				AskingClause:  cfg.AskingClause,
			})
			pidDir := filepath.Join(cfg.StateDir, cfg.Feature.ID)

			addDirs := cfg.AdditionalDirs
			if len(addDirs) == 0 {
				addDirs = []string{cfg.StateDir}
			}
			sessionAttempt := nextPlanSessionAttempt(artifactDir, attempt)
			for {
				// Clear stale phase_complete BEFORE spawning the script: otherwise
				// the agent may write the marker before we get here, and we'd
				// delete it.
				RemovePhaseComplete(attemptDir)
				cmd, env, sessOpts, err := cfg.BuildSession(BuildSessionOpts{
					Model:                          cfg.Feature.Models.Planning,
					Prompt:                         prompt,
					SystemPrompt:                   systemPrompt,
					AdditionalDirs:                 addDirs,
					AgentNames:                     explorationAgentNames(),
					PIDDir:                         pidDir,
					PermHandler:                    permHandlerFor(cfg.DangerouslySkipPermissions, cfg.PermissionCache, cfg.RepoName),
					WorkDir:                        cfg.WorkDir,
					EffortLevel:                    cfg.EffortLevel,
					Phase:                          feature.PhasePlan,
					SystemPromptHasUsefulResources: true,
					MarkerPath:                     filepath.Join(attemptDir, PhaseCompleteFile),
				})
				if err != nil {
					return nil, fmt.Errorf("building roadmap session (attempt %d): %w", attempt, err)
				}
				sessOpts = enableTruncatedTurnAutoResume(sessOpts)
				if cfg.FinishOrViolateNudge {
					sessOpts.TurnMode = ports.TurnModeInteractive
				}
				WriteDebugPrompts(attemptDir, sessOpts.DebugSystemPrompt, prompt)
				sessOpts.PermCacheScope = cfg.RepoName

				sessionID := planAttemptSessionID(fmt.Sprintf("%s-roadmap-%02d", cfg.Feature.ID, attempt), sessionAttempt)
				planSessionCtx := cfg.PhaseSpanCtx.Child()
				if attempt == 1 {
					sessOpts.AskUserAutoPick = askUserAutoPickConfig(
						cfg.FeatureStore,
						cfg.Observer,
						cfg.Feature,
						ports.AskUserAutoPickPurposeRoadmapCreator,
						planSessionCtx,
						sessionID,
						cfg.RepoName,
						0,
					)
				}
				startSession := resolveSessionStartFunc(cfg.SessionStartFunc, sm)
				sess, err := startSession(sessionID, cfg.Feature.ID, feature.PhasePlan, cmd, cfg.WorkDir, env, sessOpts)
				if err != nil {
					if errors.Is(err, ports.ErrSessionShuttingDown) {
						return &PlanLoopResult{FinalStatus: "interrupted", Iterations: attempt - 1}, nil
					}
					return nil, fmt.Errorf("starting roadmap session (attempt %d): %w", attempt, err)
				}

				providerName := ""
				if sessOpts != nil {
					providerName = sessOpts.ProviderName
				}
				cfg.Observer.SessionStarted(planSessionCtx, "plan", sessionID, providerName, cfg.Feature.Models.Planning, cfg.RepoName)
				(&ContextReadTracker{
					KBBaseDir:     filepath.Join(filepath.Dir(cfg.StateDir), "knowledge-base"),
					SkillsDir:     cfg.SkillsDir,
					GuidelinesDir: cfg.GuidelinesDir,
					Observer:      cfg.Observer,
				}).Install(sess, planSessionCtx, "plan", sessionID)
				sessionStart := time.Now()

				logPath := filepath.Join(attemptDir, "output.txt")
				logFile, err := os.Create(logPath)
				if err == nil {
					sess.SetLogFile(logFile)
				}

				agentStatus := waitForStatusDetailed(sess, sm, sessionID, waitForStatusOptions{
					ReadyCheck: func() bool {
						if HasPhaseComplete(attemptDir) {
							sess.SetHasUnansweredQuestion(false)
							return true
						}
						return false
					},
					FinishOrViolateNudge: cfg.FinishOrViolateNudge,
					MissingArtifacts:     []string{"roadmap.md"},
				}).Status

				cost := ExtractSessionCost(sess)
				if cfg.FeatureStore != nil && cost.TotalCostUSD > 0 {
					_ = cfg.FeatureStore.Modify(cfg.Feature.ID, func(f *feature.Feature) error {
						f.RecordSessionCost(feature.SessionCostRecord{
							SessionID:     sessionID,
							PhaseKey:      "plan",
							ObserverPhase: "plan",
							RepoName:      cfg.RepoName,
							CostUSD:       cost.TotalCostUSD,
						})
						return nil
					})
				}

				outcome, sessionResult, newCriticFeedback, newSessionAttempt := handleCompletedPlanSession(
					cfg, planSessionCtx, sessionID, sess, cost, sessionStart, agentStatus, sm,
					attempt, maxAttempts, sessionAttempt, plannerSpec, attemptDir, artifactDir, logPath,
				)
				switch outcome {
				case planOutcomeReturn:
					return sessionResult, nil
				case planOutcomeContinueAttempt:
					criticFeedback = newCriticFeedback
					continue roadmapAttemptLoop
				case planOutcomeRetrySession:
					sessionAttempt = newSessionAttempt
					continue
				case planOutcomeFailedNoRetry:
					return &PlanLoopResult{FinalStatus: "failed", Iterations: attempt, LastError: "roadmap session did not complete successfully"}, nil
				}
				if attempt == 1 {
					if _, err := WriteQAFile(sess.QALog(), artifactDir); err != nil {
						return nil, fmt.Errorf("writing roadmap qa file: %w", err)
					}
				}

				// Write intermediate meta so a restart resumes at validation
				// instead of re-running the planning session.
				meta := PlanAttemptMeta{
					Attempt:      attempt,
					AgentStatus:  agentStatusSuccess,
					ReviewStatus: "VALIDATION_PENDING",
				}
				if sessionAttempt > 1 {
					meta.SessionAttempt = sessionAttempt
				}
				_ = WritePlanAttemptMeta(artifactDir, meta)
				break
			}
		} // end else (!resumeValidation)

		plannerRole := roadmapPlannerRoleForAttempt(attempt)
		outcome, violations, validateErr := Validate(feature.PhasePlan, plannerRole, attemptDir)
		if validateErr != nil {
			return nil, fmt.Errorf("validating roadmap planner contract (attempt %d): %w", attempt, validateErr)
		}
		if !outcome.OK {
			lastErr := formatProtocolViolationError(plannerRole, attemptDir, violations)
			criticFeedback = formatPlanContractViolationFeedback(plannerRole, violations)
			_ = os.WriteFile(filepath.Join(attemptDir, "validation-feedback.md"), []byte(criticFeedback), 0o644)
			_ = WritePlanAttemptMeta(artifactDir, PlanAttemptMeta{
				Attempt:      attempt,
				AgentStatus:  agentStatusSuccess,
				ReviewStatus: agentStatusChangesRequested,
			})
			if attempt >= maxAttempts {
				return &PlanLoopResult{FinalStatus: BoundedHelperStatusProtocolViolation, Iterations: attempt, LastError: lastErr}, nil
			}
			continue
		}

		// Re-read feature state to pick up mid-loop profile upgrades
		if cfg.FeatureStore != nil {
			if fresh, loadErr := cfg.FeatureStore.Load(cfg.Feature.ID); loadErr == nil {
				cfg.Feature = fresh
			}
		}

		if cfg.Feature.EffectivePipeline().ShouldSkipPlanValidation() {
			// Medium: skip critics, auto-approve
			planArtifactPath := resolvePlanArtifactPath(cfg.FeatureStore, cfg.Feature.ID, artifactDir)
			_ = WritePlanAttemptMeta(artifactDir, PlanAttemptMeta{Attempt: attempt, AgentStatus: agentStatusSuccess, ReviewStatus: agentStatusApproved})
			if cfg.FeatureStore != nil && planArtifactPath != "" {
				_ = cfg.FeatureStore.Modify(cfg.Feature.ID, func(f *feature.Feature) error {
					if f.Artifacts == nil {
						f.Artifacts = make(map[string]string)
					}
					f.Artifacts["roadmap"] = planArtifactPath
					return nil
				})
			}
			return &PlanLoopResult{
				FinalStatus: "approved",
				Iterations:  attempt,
			}, nil
		}

		setValidatingPlan(cfg, true)

		planArtifactPath := resolvePlanArtifactPath(cfg.FeatureStore, cfg.Feature.ID, artifactDir)
		var reviewStatus ReviewStatus
		var feedback string
		var reviewErr error
		reviewStatus, feedback, reviewErr = runRoadmapMultiValidatorPlanValidation(cfg, sm, attempt, attemptDir, planArtifactPath)

		setValidatingPlan(cfg, false)

		if isFeatureInterrupted(cfg.FeatureStore, cfg.Feature.ID) {
			return &PlanLoopResult{FinalStatus: "interrupted", Iterations: attempt}, nil
		}

		if reviewErr != nil {
			if isProtocolViolationError(reviewErr) {
				_ = WritePlanAttemptMeta(artifactDir, PlanAttemptMeta{Attempt: attempt, AgentStatus: agentStatusSuccess, ReviewStatus: agentStatusChangesRequested})
				criticFeedback = feedback
				if strings.TrimSpace(criticFeedback) == "" {
					criticFeedback = fmt.Sprintf("Roadmap validation failed: %v", reviewErr)
				}
				_ = os.WriteFile(filepath.Join(attemptDir, "validation-feedback.md"), []byte(criticFeedback), 0o644)
				if attempt >= maxAttempts {
					return &PlanLoopResult{FinalStatus: BoundedHelperStatusProtocolViolation, Iterations: attempt, LastError: reviewErr.Error()}, nil
				}
				continue
			}
			_ = WritePlanAttemptMeta(artifactDir, PlanAttemptMeta{Attempt: attempt, AgentStatus: agentStatusSuccess, ReviewStatus: "FAILED"})
			criticFeedback = fmt.Sprintf("Roadmap validation failed: %v", reviewErr)
			_ = os.WriteFile(filepath.Join(attemptDir, "validation-feedback.md"), []byte(criticFeedback), 0o644)
			continue
		}

		switch reviewStatus {
		case ReviewApproved:
			_ = WritePlanAttemptMeta(artifactDir, PlanAttemptMeta{Attempt: attempt, AgentStatus: agentStatusSuccess, ReviewStatus: reviewStatus.String()})
			if cfg.FeatureStore != nil && planArtifactPath != "" {
				_ = cfg.FeatureStore.Modify(cfg.Feature.ID, func(f *feature.Feature) error {
					if f.Artifacts == nil {
						f.Artifacts = make(map[string]string)
					}
					f.Artifacts["roadmap"] = planArtifactPath
					return nil
				})
			}
			return &PlanLoopResult{FinalStatus: "approved", Iterations: attempt}, nil
		case ReviewChangesRequested:
			_ = WritePlanAttemptMeta(artifactDir, PlanAttemptMeta{Attempt: attempt, AgentStatus: agentStatusSuccess, ReviewStatus: agentStatusChangesRequested})
			criticFeedback = feedback
			// Persist combined feedback so it survives resume across restarts.
			_ = os.WriteFile(filepath.Join(attemptDir, "validation-feedback.md"), []byte(feedback), 0o644)
			continue
		default:
			_ = WritePlanAttemptMeta(artifactDir, PlanAttemptMeta{Attempt: attempt, AgentStatus: agentStatusSuccess, ReviewStatus: "UNKNOWN"})
			criticFeedback = "Roadmap validation produced no clear result."
			_ = os.WriteFile(filepath.Join(attemptDir, "validation-feedback.md"), []byte(criticFeedback), 0o644)
			continue
		}
	}

	planArtifactPath := resolvePlanArtifactPath(cfg.FeatureStore, cfg.Feature.ID, artifactDir)
	if cfg.FeatureStore != nil && planArtifactPath != "" {
		_ = cfg.FeatureStore.Modify(cfg.Feature.ID, func(f *feature.Feature) error {
			if f.Artifacts == nil {
				f.Artifacts = make(map[string]string)
			}
			f.Artifacts["roadmap"] = planArtifactPath
			return nil
		})
	}

	return &PlanLoopResult{
		FinalStatus: "needs_human_review",
		Iterations:  maxAttempts,
		LastError:   fmt.Sprintf("roadmap not approved after %d attempts", maxAttempts),
	}, nil
}

// RunPhasePlanningLoop creates and validates a per-phase plan.
func RunPhasePlanningLoop(cfg PhasePlanLoopConfig, sm ports.SessionManager) (result *PlanLoopResult, retErr error) {
	// Phase lifecycle
	featureCtx := observe.SpanContextForFeature(cfg.Feature.ID, cfg.Feature.TraceID, cfg.Feature.Name, cfg.Feature.FeatureSpanID).WithRun(cfg.Feature.ActiveRun)
	cfg.PlanLoopConfig.PhaseSpanCtx = featureCtx.Child()
	phaseStart := time.Now()
	cfg.Observer.PhaseStarted(cfg.PlanLoopConfig.PhaseSpanCtx, "plan")
	defer func() {
		var phaseErr error
		if retErr != nil {
			phaseErr = retErr
		} else if result != nil && result.FinalStatus != "approved" {
			phaseErr = fmt.Errorf("%s", result.FinalStatus)
		}
		cfg.Observer.PhaseCompleted(cfg.PlanLoopConfig.PhaseSpanCtx, "plan", time.Since(phaseStart), phaseErr)
	}()

	var artifactDir string
	if cfg.ArtifactBaseDir != "" {
		artifactDir = filepath.Join(cfg.ArtifactBaseDir, fmt.Sprintf("phase-%02d", cfg.Phase.Number), "plan")
	} else {
		baseDir := filepath.Join(ActiveRunDir(cfg.StateDir, cfg.Feature), cfg.Feature.RefactorPrefix())
		artifactDir = filepath.Join(baseDir, fmt.Sprintf("phase-%02d", cfg.Phase.Number), "plan")
	}
	_ = os.MkdirAll(artifactDir, 0o755)

	maxAttempts := cfg.MaxAttempts
	if maxAttempts == 0 {
		maxAttempts = 2
	}

	startAttempt := LatestCompletedPlanAttempt(artifactDir)
	resumeValidation := false
	var criticFeedback string
	feedbackAttempt, feedback := latestPlanRevisionFeedbackAttempt(artifactDir)
	if feedbackAttempt > startAttempt {
		startAttempt = feedbackAttempt
		criticFeedback = feedback
	} else if startAttempt > 0 {
		meta, err := readPlanAttemptMeta(artifactDir, startAttempt)
		if err == nil {
			if meta.ReviewStatus == agentStatusApproved {
				return &PlanLoopResult{FinalStatus: "approved", Iterations: startAttempt}, nil
			}
			if meta.ReviewStatus == "VALIDATION_PENDING" {
				resumeValidation = true
			}
			if meta.ReviewStatus == agentStatusChangesRequested {
				feedbackPath := filepath.Join(artifactDir, fmt.Sprintf("attempt-%02d", startAttempt), "validation-feedback.md")
				if data, readErr := os.ReadFile(feedbackPath); readErr == nil {
					criticFeedback = strings.TrimSpace(string(data))
				}
			}
		}
	} else if feedbackAttempt > 0 {
		startAttempt = feedbackAttempt
		criticFeedback = feedback
	}

	// When resuming validation, re-enter the loop at the pending attempt
	// instead of starting a new planning session.
	loopStart := startAttempt + 1
	if resumeValidation {
		loopStart = startAttempt
	}

	// Per-axis stall tracker. Reconstructed from persisted PlanAttemptMeta
	// files so the counter survives session restarts — without this, a
	// drifting critic could escape the stall cap simply by crashing or
	// being interrupted after attempt N-1.
	axisStallTracker := loadAxisStallState(artifactDir)

phasePlanAttemptLoop:
	for attempt := loopStart; attempt <= maxAttempts; attempt++ {
		if isFeatureInterrupted(cfg.FeatureStore, cfg.Feature.ID) {
			return &PlanLoopResult{FinalStatus: "interrupted", Iterations: attempt - 1}, nil
		}

		// Create per-attempt directory for debug prompts, logs, and validation files.
		attemptDir := filepath.Join(artifactDir, fmt.Sprintf("attempt-%02d", attempt))
		if err := os.MkdirAll(attemptDir, 0o755); err != nil {
			return nil, fmt.Errorf("creating attempt directory: %w", err)
		}

		if cfg.FeatureStore != nil {
			_ = cfg.FeatureStore.Modify(cfg.Feature.ID, func(f *feature.Feature) error {
				f.PlanIteration = attempt
				return nil
			})
		}

		// Skip Phase A when resuming from interrupted validation —
		// the plan artifact already exists, just re-run validators.
		if resumeValidation {
			resumeValidation = false
		} else {
			var prompt string
			var plannerSpec RoleSpec
			if attempt == 1 {
				prompt = BuildPhasePlanPromptWithResearch(cfg.Feature, cfg.SkillsDir, cfg.GuidelinesDir, cfg.RoadmapPath, cfg.initialPlanningResearchArtifactPath(), cfg.Phase, cfg.QAFilePaths, cfg.KBInfos...)
				plannerSpec = PhasePlanCreatorRoleSpec()
			} else {
				prevPlanPath := resolvePlanArtifactPath(cfg.FeatureStore, cfg.Feature.ID, artifactDir)
				approvals := LoadPriorAxisApprovals(artifactDir)
				prompt = BuildPhasePlanRevisionPrompt(cfg.Feature, cfg.SkillsDir, prevPlanPath, criticFeedback, cfg.DesignArtifactPath, cfg.Phase, attempt, approvals)
				plannerSpec = PhasePlanReviserRoleSpec()
			}

			systemPrompt := BuildRoleSystemPrompt(BuildRoleSystemPromptInput{
				Spec:          plannerSpec,
				IterationDir:  attemptDir,
				SkillsDir:     cfg.SkillsDir,
				GuidelinesDir: cfg.GuidelinesDir,
				KBInfos:       cfg.KBInfos,
				AskingClause:  cfg.AskingClause,
			})
			pidDir := filepath.Join(cfg.StateDir, cfg.Feature.ID)

			addDirs := cfg.AdditionalDirs
			if len(addDirs) == 0 {
				addDirs = []string{cfg.StateDir}
			}
			sessionAttempt := nextPlanSessionAttempt(artifactDir, attempt)
			for {
				// Clear stale phase_complete BEFORE spawning the script: otherwise
				// the agent may write the marker before we get here, and we'd
				// delete it.
				RemovePhaseComplete(attemptDir)
				cmd, env, sessOpts, err := cfg.BuildSession(BuildSessionOpts{
					Model:                          cfg.Feature.Models.Planning,
					Prompt:                         prompt,
					SystemPrompt:                   systemPrompt,
					AdditionalDirs:                 addDirs,
					AgentNames:                     explorationAgentNames(),
					PIDDir:                         pidDir,
					PermHandler:                    permHandlerFor(cfg.DangerouslySkipPermissions, cfg.PermissionCache, cfg.RepoName),
					WorkDir:                        cfg.WorkDir,
					EffortLevel:                    cfg.EffortLevel,
					Phase:                          feature.PhasePlan,
					SystemPromptHasUsefulResources: true,
					MarkerPath:                     filepath.Join(attemptDir, PhaseCompleteFile),
				})
				if err != nil {
					return nil, fmt.Errorf("building phase plan session (attempt %d): %w", attempt, err)
				}
				sessOpts = enableTruncatedTurnAutoResume(sessOpts)
				if cfg.FinishOrViolateNudge {
					sessOpts.TurnMode = ports.TurnModeInteractive
				}
				WriteDebugPrompts(attemptDir, sessOpts.DebugSystemPrompt, prompt)
				sessOpts.PermCacheScope = cfg.RepoName

				sessionID := planAttemptSessionID(fmt.Sprintf("%s-phase-%02d-plan-%02d", cfg.Feature.ID, cfg.Phase.Number, attempt), sessionAttempt)
				planSessionCtx := cfg.PlanLoopConfig.PhaseSpanCtx.Child()
				if attempt == 1 {
					sessOpts.AskUserAutoPick = askUserAutoPickConfig(
						cfg.FeatureStore,
						cfg.Observer,
						cfg.Feature,
						ports.AskUserAutoPickPurposePhasePlanCreator,
						planSessionCtx,
						sessionID,
						cfg.RepoName,
						0,
					)
				}
				startSession := resolveSessionStartFunc(cfg.SessionStartFunc, sm)
				sess, err := startSession(sessionID, cfg.Feature.ID, feature.PhasePlan, cmd, cfg.WorkDir, env, sessOpts)
				if err != nil {
					if errors.Is(err, ports.ErrSessionShuttingDown) {
						return &PlanLoopResult{FinalStatus: "interrupted", Iterations: attempt - 1}, nil
					}
					return nil, fmt.Errorf("starting phase plan session (attempt %d): %w", attempt, err)
				}

				providerName := ""
				if sessOpts != nil {
					providerName = sessOpts.ProviderName
				}
				cfg.Observer.SessionStarted(planSessionCtx, "plan", sessionID, providerName, cfg.Feature.Models.Planning, cfg.RepoName)
				(&ContextReadTracker{
					KBBaseDir:     filepath.Join(filepath.Dir(cfg.StateDir), "knowledge-base"),
					SkillsDir:     cfg.SkillsDir,
					GuidelinesDir: cfg.GuidelinesDir,
					Observer:      cfg.Observer,
				}).Install(sess, planSessionCtx, "plan", sessionID)
				sessionStart := time.Now()

				logPath := filepath.Join(attemptDir, "output.txt")
				logFile, err := os.Create(logPath)
				if err == nil {
					sess.SetLogFile(logFile)
				}

				agentStatus := waitForStatusDetailed(sess, sm, sessionID, waitForStatusOptions{
					ReadyCheck: func() bool {
						if HasPhaseComplete(attemptDir) {
							sess.SetHasUnansweredQuestion(false)
							return true
						}
						return false
					},
					FinishOrViolateNudge: cfg.FinishOrViolateNudge,
					MissingArtifacts:     []string{"plan.md"},
				}).Status

				cost := ExtractSessionCost(sess)
				if cfg.FeatureStore != nil && cost.TotalCostUSD > 0 {
					costKey := fmt.Sprintf("phase-%d-plan", cfg.Phase.Number)
					_ = cfg.FeatureStore.Modify(cfg.Feature.ID, func(f *feature.Feature) error {
						f.RecordSessionCost(feature.SessionCostRecord{
							SessionID:     sessionID,
							PhaseKey:      costKey,
							ObserverPhase: "plan",
							RepoName:      cfg.RepoName,
							CostUSD:       cost.TotalCostUSD,
						})
						return nil
					})
				}

				outcome, sessionResult, newCriticFeedback, newSessionAttempt := handleCompletedPlanSession(
					cfg.PlanLoopConfig, planSessionCtx, sessionID, sess, cost, sessionStart, agentStatus, sm,
					attempt, maxAttempts, sessionAttempt, plannerSpec, attemptDir, artifactDir, logPath,
				)
				switch outcome {
				case planOutcomeReturn:
					return sessionResult, nil
				case planOutcomeContinueAttempt:
					criticFeedback = newCriticFeedback
					continue phasePlanAttemptLoop
				case planOutcomeRetrySession:
					sessionAttempt = newSessionAttempt
					continue
				case planOutcomeFailedNoRetry:
					return &PlanLoopResult{FinalStatus: "failed", Iterations: attempt, LastError: "phase plan session did not complete"}, nil
				}
				if attempt == 1 {
					if _, err := WriteQAFile(sess.QALog(), artifactDir); err != nil {
						return nil, fmt.Errorf("writing phase plan qa file: %w", err)
					}
				}

				// Write intermediate meta so a restart resumes at validation
				// instead of re-running the planning session.
				meta := PlanAttemptMeta{
					Attempt:      attempt,
					AgentStatus:  agentStatusSuccess,
					ReviewStatus: "VALIDATION_PENDING",
				}
				if sessionAttempt > 1 {
					meta.SessionAttempt = sessionAttempt
				}
				_ = WritePlanAttemptMeta(artifactDir, meta)
				break
			}
		} // end else (!resumeValidation)

		plannerRole := phasePlanPlannerRoleForAttempt(attempt)
		outcome, violations, validateErr := Validate(feature.PhasePlan, plannerRole, attemptDir)
		if validateErr != nil {
			return nil, fmt.Errorf("validating phase planner contract (attempt %d): %w", attempt, validateErr)
		}
		if !outcome.OK {
			lastErr := formatProtocolViolationError(plannerRole, attemptDir, violations)
			criticFeedback = formatPlanContractViolationFeedback(plannerRole, violations)
			_ = os.WriteFile(filepath.Join(attemptDir, "validation-feedback.md"), []byte(criticFeedback), 0o644)
			_ = WritePlanAttemptMeta(artifactDir, PlanAttemptMeta{
				Attempt:      attempt,
				AgentStatus:  agentStatusSuccess,
				ReviewStatus: agentStatusChangesRequested,
			})
			if attempt >= maxAttempts {
				return &PlanLoopResult{FinalStatus: BoundedHelperStatusProtocolViolation, Iterations: attempt, LastError: lastErr}, nil
			}
			continue
		}

		// Re-read feature state to pick up mid-loop profile upgrades
		if cfg.FeatureStore != nil {
			if fresh, loadErr := cfg.FeatureStore.Load(cfg.Feature.ID); loadErr == nil {
				cfg.Feature = fresh
			}
		}

		// Medium features skip plan critics entirely.
		if cfg.Feature.EffectivePipeline().ShouldSkipPlanValidation() {
			planArtifactPath := resolvePlanArtifactPath(cfg.FeatureStore, cfg.Feature.ID, artifactDir)
			_ = WritePlanAttemptMeta(artifactDir, PlanAttemptMeta{Attempt: attempt, AgentStatus: agentStatusSuccess, ReviewStatus: agentStatusApproved})
			if cfg.FeatureStore != nil && planArtifactPath != "" {
				artifactKey := fmt.Sprintf("phase-%d-plan", cfg.Phase.Number)
				_ = cfg.FeatureStore.Modify(cfg.Feature.ID, func(f *feature.Feature) error {
					if f.Artifacts == nil {
						f.Artifacts = make(map[string]string)
					}
					f.Artifacts[artifactKey] = planArtifactPath
					return nil
				})
			}
			return &PlanLoopResult{FinalStatus: "approved", Iterations: attempt}, nil
		}

		setValidatingPlan(cfg.PlanLoopConfig, true)

		planArtifactPath := resolvePlanArtifactPath(cfg.FeatureStore, cfg.Feature.ID, artifactDir)
		var reviewStatus ReviewStatus
		var feedback string
		var reviewErr error
		var perAxisResults []ValidatorResult
		perAxisResults, reviewStatus, feedback, reviewErr = runPhasePlanMultiValidatorValidation(cfg, sm, attempt, attemptDir, planArtifactPath)

		setValidatingPlan(cfg.PlanLoopConfig, false)

		if isFeatureInterrupted(cfg.FeatureStore, cfg.Feature.ID) {
			return &PlanLoopResult{FinalStatus: "interrupted", Iterations: attempt}, nil
		}

		if reviewErr != nil {
			if isProtocolViolationError(reviewErr) {
				_, _, _, axisVerdicts, axisDigests := axisStallTracker.observe(attempt, planArtifactPath, perAxisResults)
				_ = WritePlanAttemptMeta(artifactDir, PlanAttemptMeta{
					Attempt:      attempt,
					AgentStatus:  agentStatusSuccess,
					ReviewStatus: agentStatusChangesRequested,
					AxisVerdicts: axisVerdicts,
					AxisDigests:  axisDigests,
				})
				criticFeedback = feedback
				if strings.TrimSpace(criticFeedback) == "" {
					criticFeedback = fmt.Sprintf("Phase plan validation failed: %v", reviewErr)
				}
				_ = os.WriteFile(filepath.Join(attemptDir, "validation-feedback.md"), []byte(criticFeedback), 0o644)
				if attempt >= maxAttempts {
					return &PlanLoopResult{FinalStatus: BoundedHelperStatusProtocolViolation, Iterations: attempt, LastError: reviewErr.Error()}, nil
				}
				continue
			}
			_ = WritePlanAttemptMeta(artifactDir, PlanAttemptMeta{Attempt: attempt, AgentStatus: agentStatusSuccess, ReviewStatus: "FAILED"})
			criticFeedback = fmt.Sprintf("Phase plan validation failed: %v", reviewErr)
			_ = os.WriteFile(filepath.Join(attemptDir, "validation-feedback.md"), []byte(criticFeedback), 0o644)
			continue
		}

		switch reviewStatus {
		case ReviewApproved:
			_ = WritePlanAttemptMeta(artifactDir, PlanAttemptMeta{Attempt: attempt, AgentStatus: agentStatusSuccess, ReviewStatus: reviewStatus.String()})
			if cfg.FeatureStore != nil && planArtifactPath != "" {
				artifactKey := fmt.Sprintf("phase-%d-plan", cfg.Phase.Number)
				_ = cfg.FeatureStore.Modify(cfg.Feature.ID, func(f *feature.Feature) error {
					if f.Artifacts == nil {
						f.Artifacts = make(map[string]string)
					}
					f.Artifacts[artifactKey] = planArtifactPath
					return nil
				})
			}
			return &PlanLoopResult{FinalStatus: "approved", Iterations: attempt}, nil
		case ReviewChangesRequested:
			// Per-axis stall detection: if any axis keeps failing across
			// consecutive attempts without the plan section it freezes
			// changing, escalate to needs_human_review rather than burning
			// through max_plan_iterations on a drifting critic. We also
			// capture per-axis verdicts and digests here so the tracker can
			// be rebuilt after a session restart.
			stalled, stallAxis, stallCount, axisVerdicts, axisDigests := axisStallTracker.observe(attempt, planArtifactPath, perAxisResults)
			_ = WritePlanAttemptMeta(artifactDir, PlanAttemptMeta{
				Attempt:      attempt,
				AgentStatus:  agentStatusSuccess,
				ReviewStatus: agentStatusChangesRequested,
				AxisVerdicts: axisVerdicts,
				AxisDigests:  axisDigests,
			})
			criticFeedback = feedback
			// Persist combined feedback so it survives resume across restarts.
			_ = os.WriteFile(filepath.Join(attemptDir, "validation-feedback.md"), []byte(feedback), 0o644)

			if stalled {
				return &PlanLoopResult{
					FinalStatus: "needs_human_review",
					Iterations:  attempt,
					LastError: fmt.Sprintf("axis %q failed %d consecutive attempts with unchanged %s section — escalating for human review",
						stallAxis, stallCount, axisFrozenHeading[stallAxis]),
				}, nil
			}
			continue
		default:
			_ = WritePlanAttemptMeta(artifactDir, PlanAttemptMeta{Attempt: attempt, AgentStatus: agentStatusSuccess, ReviewStatus: "UNKNOWN"})
			criticFeedback = "Phase plan validation produced no clear result."
			_ = os.WriteFile(filepath.Join(attemptDir, "validation-feedback.md"), []byte(criticFeedback), 0o644)
			continue
		}
	}

	planArtifactPath := resolvePlanArtifactPath(cfg.FeatureStore, cfg.Feature.ID, artifactDir)
	if cfg.FeatureStore != nil && planArtifactPath != "" {
		artifactKey := fmt.Sprintf("phase-%d-plan", cfg.Phase.Number)
		_ = cfg.FeatureStore.Modify(cfg.Feature.ID, func(f *feature.Feature) error {
			if f.Artifacts == nil {
				f.Artifacts = make(map[string]string)
			}
			f.Artifacts[artifactKey] = planArtifactPath
			return nil
		})
	}

	return &PlanLoopResult{
		FinalStatus: "needs_human_review",
		Iterations:  maxAttempts,
		LastError:   fmt.Sprintf("phase plan not approved after %d attempts", maxAttempts),
	}, nil
}

// planSessionOutcome tells a planning attempt loop what control-flow action
// to take after handleCompletedPlanSession processes a just-finished session.
// Go can't `continue` a caller's labeled loop from inside a called function,
// so the caller performs the actual continue/return itself based on this value.
type planSessionOutcome int

const (
	// planOutcomeProceed means the session succeeded; the caller continues
	// past the failure-handling branch as normal.
	planOutcomeProceed planSessionOutcome = iota
	// planOutcomeReturn means the caller should `return result, nil` immediately.
	planOutcomeReturn
	// planOutcomeContinueAttempt means the caller should `continue` its labeled
	// attempt loop (e.g. `continue roadmapAttemptLoop`), after applying newCriticFeedback.
	planOutcomeContinueAttempt
	// planOutcomeRetrySession means the caller should set sessionAttempt to
	// newSessionAttempt and `continue` its inner session-retry loop.
	planOutcomeRetrySession
	// planOutcomeFailedNoRetry means the caller should return its own
	// "failed" PlanLoopResult, using a caller-specific LastError message.
	planOutcomeFailedNoRetry
)

// handleCompletedPlanSession records session-end telemetry and the session
// log, then decides what a planning attempt loop should do next based on
// agentStatus. It is shared by RunRoadmapPlanningLoop and RunPhasePlanningLoop,
// which are otherwise identical in this bit of post-session handling except
// for which labeled loop they continue and the wording of their final error.
func handleCompletedPlanSession(
	cfg PlanLoopConfig,
	planSessionCtx observe.SpanContext,
	sessionID string,
	sess ports.SessionHandle,
	cost SessionCost,
	sessionStart time.Time,
	agentStatus string,
	sm ports.SessionManager,
	attempt, maxAttempts, sessionAttempt int,
	plannerSpec RoleSpec,
	attemptDir, artifactDir, logPath string,
) (outcome planSessionOutcome, result *PlanLoopResult, newCriticFeedback string, newSessionAttempt int) {
	cfg.Observer.SessionEnded(planSessionCtx, "plan", sessionID, cfg.RepoName,
		toSessionUsage(cost), time.Since(sessionStart), sessionErrFromAgentStatus(agentStatus))

	output := sess.MessageLog().Text()
	_ = os.WriteFile(logPath, []byte(output), 0o644)

	if agentStatus == agentStatusSuccess {
		return planOutcomeProceed, nil, "", sessionAttempt
	}

	// Graceful shutdown: see implement.go:377 for the rationale.
	if agentStatus == "FAILED" && sm != nil && sm.IsShuttingDown() {
		return planOutcomeReturn, &PlanLoopResult{FinalStatus: "interrupted", Iterations: attempt - 1}, "", sessionAttempt
	}
	if agentStatus == agentStatusMissingMarker {
		violations := missingPhaseCompleteViolations()
		lastErr := formatProtocolViolationError(plannerSpec.Role, attemptDir, violations)
		criticFeedback := formatPlanContractViolationFeedback(plannerSpec.Role, violations)
		_ = os.WriteFile(filepath.Join(attemptDir, "validation-feedback.md"), []byte(criticFeedback), 0o644)
		_ = WritePlanAttemptMeta(artifactDir, PlanAttemptMeta{
			Attempt:      attempt,
			AgentStatus:  agentStatusSuccess,
			ReviewStatus: agentStatusChangesRequested,
		})
		if attempt >= maxAttempts {
			return planOutcomeReturn, &PlanLoopResult{FinalStatus: BoundedHelperStatusProtocolViolation, Iterations: attempt, LastError: lastErr}, criticFeedback, sessionAttempt
		}
		return planOutcomeContinueAttempt, nil, criticFeedback, sessionAttempt
	}
	_ = WritePlanAttemptMeta(artifactDir, PlanAttemptMeta{
		Attempt:        attempt,
		SessionAttempt: sessionAttempt,
		AgentStatus:    "FAILED",
	})
	if shouldRetryPlanInfrastructureSession(agentStatus, sess, cost, time.Since(sessionStart), sessionAttempt) {
		return planOutcomeRetrySession, nil, "", sessionAttempt + 1
	}
	return planOutcomeFailedNoRetry, nil, "", sessionAttempt
}

// setValidatingPlan persists the ValidatingPlan flag on the feature.
func setValidatingPlan(cfg PlanLoopConfig, validating bool) {
	if cfg.FeatureStore == nil {
		return
	}
	_ = cfg.FeatureStore.Modify(cfg.Feature.ID, func(f *feature.Feature) error {
		f.ValidatingPlan = validating
		return nil
	})
}

func formatPlanContractViolationFeedback(role Role, violations []ProtocolViolation) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Planner contract violation for %s.\n\n", role)
	for _, v := range violations {
		fmt.Fprintf(&b, "- %s: %s\n", v.Artifact, v.Reason)
	}
	return strings.TrimSpace(b.String()) + "\n"
}

func missingPhaseCompleteViolations() []ProtocolViolation {
	return []ProtocolViolation{{
		Artifact: PhaseCompleteFile,
		Reason:   "agent completed successfully but did not write phase_complete",
	}}
}

// resolvePlanArtifactPath reads the plan artifact path from the feature store.
// Falls back to globbing the artifact directory for common plan filenames.
func resolvePlanArtifactPath(store ports.FeatureStore, featureID, artifactDir string) string {
	if store != nil {
		if f, err := store.Load(featureID); err == nil && f.Artifacts != nil {
			if p := f.Artifacts["plan"]; p != "" {
				// Try as absolute path
				if filepath.IsAbs(p) {
					if _, err := os.Stat(p); err == nil {
						return p
					}
				}
				// Try relative to artifact dir
				candidate := filepath.Join(artifactDir, p)
				if _, err := os.Stat(candidate); err == nil {
					return candidate
				}
			}
		}
	}

	// Fallback: look for common plan filenames, preferring the most recently modified.
	// Exclude non-artifact files (debug prompts, validation feedback, logs).
	var bestPath string
	var bestModTime int64

	matches, _ := filepath.Glob(filepath.Join(artifactDir, "*.md"))
	for _, m := range matches {
		if IsArtifactExcluded(filepath.Base(m)) {
			continue
		}
		info, err := os.Stat(m)
		if err != nil {
			continue
		}
		if mt := info.ModTime().UnixNano(); bestPath == "" || mt > bestModTime {
			bestPath = m
			bestModTime = mt
		}
	}
	return bestPath
}

// PhaseCompleteFile is the signal file the agent writes to indicate it has
// finished its work. The presence of this file is the universal completion
// signal across all phases. If the agent ends its turn without writing it,
// the session stays alive (the agent likely has a question or hit an issue).
const PhaseCompleteFile = "phase_complete"

// HasPhaseComplete returns true if the phase_complete signal file exists in dir.
func HasPhaseComplete(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, PhaseCompleteFile))
	return err == nil
}

// RemovePhaseComplete removes the phase_complete signal file if it exists.
// Called at the start of each phase/iteration to avoid stale signals.
func RemovePhaseComplete(dir string) {
	_ = os.Remove(filepath.Join(dir, PhaseCompleteFile))
}

// IsArtifactExcluded returns true if the filename should be excluded from
// artifact resolution fallback. These are debug/infrastructure files.
func IsArtifactExcluded(name string) bool {
	lower := strings.ToLower(name)
	switch {
	case lower == "system-prompt.md" || lower == "user-prompt.md":
		return true
	case strings.HasPrefix(lower, "validation-"):
		return true
	case strings.HasPrefix(lower, "debug-"):
		return true
	case lower == "error.log" || lower == "output.txt":
		return true
	case strings.HasSuffix(lower, "-prompt.md"):
		return true
	case lower == "qa-answers.md" || lower == ProtocolRetrySidecarFile:
		return true
	case strings.HasPrefix(lower, ".protocol-retry-") && strings.HasSuffix(lower, ".yaml"):
		return true
	default:
		return false
	}
}

// sessionErrFromAgentStatus returns an error if the agent status indicates failure.
func sessionErrFromAgentStatus(agentStatus string) error {
	if agentStatus != agentStatusSuccess {
		return fmt.Errorf("agent status: %s", agentStatus)
	}
	return nil
}
