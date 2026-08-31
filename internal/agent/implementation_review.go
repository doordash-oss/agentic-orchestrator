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
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

type implementationReviewGate string

const implementationReviewGatePerPhase implementationReviewGate = "per_phase"
const implementationReviewGateFinal implementationReviewGate = "final"

// reviewHelperEffortFromImpl returns the effort level to pass to an
// implementation review helper's BuildSessionOpts: the resolved Review-role
// effort when set, otherwise the implementation effort level (preserving the
// pre-coupling fallback for callers that did not resolve Review effort).
func reviewHelperEffortFromImpl(cfg ImplementConfig) llm.EffortLevel {
	if cfg.ReviewEffectiveEffort != "" {
		return cfg.ReviewEffectiveEffort
	}
	if cfg.EffectiveEffort != "" {
		return cfg.EffectiveEffort
	}
	return cfg.EffortLevel
}

type implementationReviewGateMembership struct {
	Gate    implementationReviewGate
	Order   int
	Applies func(implementationReviewAxisSelection) bool
}

type implementationReviewExecutionPosture string

const (
	implementationReviewPostureReadOnly implementationReviewExecutionPosture = "read_only"
	implementationReviewPostureLiveRun  implementationReviewExecutionPosture = "live_run"
)

type implementationReviewAxis struct {
	Name             string
	ShortName        string
	SkillName        string
	Role             Role
	ExecutionPosture implementationReviewExecutionPosture
	ModelOverride    string
	Memberships      []implementationReviewGateMembership
}

type implementationReviewAxisSelection struct {
	Profile              feature.PipelineProfile
	CurrentPhaseFrontend bool
	AnyPhaseFrontend     bool
}

var implementationReviewAxisRegistry = []implementationReviewAxis{
	{
		Name:             "Craft",
		ShortName:        "Craft",
		SkillName:        "review-implementation-craft",
		Role:             RoleImplementationReviewCraft,
		ExecutionPosture: implementationReviewPostureReadOnly,
		Memberships: []implementationReviewGateMembership{
			{Gate: implementationReviewGatePerPhase, Order: 10, Applies: moonshotImplementationReviewAxis},
			{Gate: implementationReviewGateFinal, Order: 10, Applies: allImplementationReviewProfiles},
		},
	},
	{
		Name:             "Functionality/Evidence",
		ShortName:        "Func",
		SkillName:        "review-implementation-functionality-evidence",
		Role:             RoleImplementationReviewFunctionalityEvidence,
		ExecutionPosture: implementationReviewPostureReadOnly,
		Memberships: []implementationReviewGateMembership{
			{Gate: implementationReviewGatePerPhase, Order: 20, Applies: moonshotImplementationReviewAxis},
		},
	},
	{
		Name:             "Cleanliness",
		ShortName:        "Clean",
		SkillName:        "review-implementation-cleanliness",
		Role:             RoleImplementationReviewCleanliness,
		ExecutionPosture: implementationReviewPostureReadOnly,
		Memberships: []implementationReviewGateMembership{
			{Gate: implementationReviewGatePerPhase, Order: 30, Applies: moonshotImplementationReviewAxis},
			{Gate: implementationReviewGateFinal, Order: 20, Applies: allImplementationReviewProfiles},
		},
	},
	{
		Name:             "QA",
		ShortName:        "QA",
		SkillName:        "review-implementation-qa",
		Role:             RoleImplementationReviewQA,
		ExecutionPosture: implementationReviewPostureLiveRun,
		Memberships: []implementationReviewGateMembership{
			{Gate: implementationReviewGateFinal, Order: 30, Applies: allImplementationReviewProfiles},
		},
	},
	{
		Name:             "Design",
		ShortName:        "Design",
		SkillName:        "review-implementation-design",
		Role:             RoleImplementationReviewDesign,
		ExecutionPosture: implementationReviewPostureLiveRun,
		Memberships: []implementationReviewGateMembership{
			{Gate: implementationReviewGatePerPhase, Order: 40, Applies: moonshotFrontendImplementationReviewAxis},
			{Gate: implementationReviewGateFinal, Order: 40, Applies: frontendFinalImplementationReviewAxis},
		},
	},
}

func moonshotImplementationReviewAxis(selection implementationReviewAxisSelection) bool {
	return selection.Profile == feature.PipelineMoonshot
}

func moonshotFrontendImplementationReviewAxis(selection implementationReviewAxisSelection) bool {
	return selection.Profile == feature.PipelineMoonshot && selection.CurrentPhaseFrontend
}

func frontendFinalImplementationReviewAxis(selection implementationReviewAxisSelection) bool {
	return selection.AnyPhaseFrontend
}

func allImplementationReviewProfiles(implementationReviewAxisSelection) bool {
	return true
}

func implementationReviewAxesForGate(gate implementationReviewGate, selection implementationReviewAxisSelection) []implementationReviewAxis {
	var axes []implementationReviewAxis
	for _, axis := range implementationReviewAxisRegistry {
		for _, membership := range axis.Memberships {
			if membership.Gate != gate {
				continue
			}
			if membership.Applies != nil && !membership.Applies(selection) {
				continue
			}
			selected := axis
			selected.Memberships = []implementationReviewGateMembership{membership}
			axes = append(axes, selected)
		}
	}
	sort.SliceStable(axes, func(i, j int) bool {
		return axes[i].Memberships[0].Order < axes[j].Memberships[0].Order
	})
	return axes
}

type reviewAxisResult = multiAxisReviewResult

func runImplementationReviewAxes(cfg ImplementConfig, sm ports.SessionManager, iteration int, iterDir, reviewDir string, reviewCtx observe.SpanContext, input implementationReviewInput) (ReviewStatus, string, error) {
	profile := feature.PipelineMoonshot
	var currentPhaseFrontend bool
	var anyPhaseFrontend bool
	if cfg.Feature != nil {
		profile = cfg.Feature.EffectivePipeline()
		currentPhaseFrontend = cfg.Feature.RoadmapPhaseFrontend(cfg.Feature.CurrentRoadmapPhase)
		anyPhaseFrontend = cfg.Feature.AnyRoadmapPhaseFrontend()
	}
	axes := implementationReviewAxesForGate(implementationReviewGatePerPhase, implementationReviewAxisSelection{
		Profile:              profile,
		CurrentPhaseFrontend: currentPhaseFrontend,
		AnyPhaseFrontend:     anyPhaseFrontend,
	})
	if len(axes) == 0 {
		feedback := FormatStructuredReviewFeedback("Implementation Review", "", "", ReviewApproved)
		return ReviewApproved, feedback, nil
	}

	setImplementationReviewAxisStatuses(cfg, axes)
	defer clearImplementationReviewAxisStatuses(cfg)

	validationCtx := reviewCtx.Child()
	validationStart := time.Now()
	cfg.Observer.ValidationStarted(validationCtx, "implementation_review", len(axes))

	results := make([]reviewAxisResult, len(axes))
	runMultiAxisReviews(len(axes), func(i int) {
		axis := axes[i]
		axisCtx := validationCtx.Child()
		axisStart := time.Now()
		cfg.Observer.ValidatorStarted(axisCtx, axis.Name)

		status, feedback, err := runImplementationReviewAxis(cfg, sm, iteration, iterDir, reviewDir, axis, axisCtx, input)
		results[i] = reviewAxisResult{Axis: axis.Name, Status: status, Feedback: feedback, Error: err}

		verdict := status.String()
		if err != nil {
			verdict = "error"
		}
		cfg.Observer.ValidatorCompleted(axisCtx, axis.Name, verdict, time.Since(axisStart))
		updateImplementationReviewAxisStatus(cfg, axis.Name, status, err)
	})

	status, feedback, err := composeImplementationReviewFeedback(results, len(axes))
	verdict := status.String()
	if err != nil {
		verdict = "error"
	}
	cfg.Observer.ValidationCompleted(validationCtx, "implementation_review", verdict, time.Since(validationStart), len(axes))
	return status, feedback, err
}

type implementationReviewInput struct {
	ProgressPath           string
	ContractPath           string
	VerificationReportPath string
	RequiredVerification   []RequiredVerificationItem
	KnownCaveatsGateResult ReportGateResult
}

func runImplementationReviewAxis(cfg ImplementConfig, sm ports.SessionManager, iteration int, iterDir, reviewDir string, axis implementationReviewAxis, parentCtx observe.SpanContext, input implementationReviewInput) (ReviewStatus, string, error) {
	axisSlug := implementationReviewAxisSlug(axis.Name)
	axisDir := filepath.Join(reviewDir, axisSlug)
	if err := os.MkdirAll(axisDir, 0o755); err != nil {
		return ReviewFailed, "", fmt.Errorf("creating %s implementation review helper directory: %w", axis.Name, err)
	}
	feedbackPath := filepath.Join(axisDir, "review-feedback.md")
	// Stop/restart resume: an axis that already completed its verdict for
	// this iteration is reused instead of re-running the helper. Helper
	// Failure stubs never receive a harness completion receipt, so they are
	// never reused.
	if HasCommittedPhaseOutcome(axisDir, feature.PhaseReview, axis.Role) {
		if cached, err := ParseReviewFeedback(feedbackPath); err == nil && len(cached.ProtocolViolations) == 0 {
			return cached.Verdict, cached.Body, nil
		}
	}
	RemoveCompletionReceipt(axisDir)
	intent := resolvePromptIntent(cfg.Feature)
	reviewPrompt := BuildImplementationReviewAxisPromptWithOpts(ImplementationReviewAxisPromptOpts{
		Gate:                   implementationReviewGatePerPhase,
		AxisLabel:              axis.Name,
		FeatureDescription:     intent.Description,
		DesignArtifactPath:     designArtifactPathForImplementationReview(cfg.Feature),
		LiveRunAxis:            axis.ExecutionPosture == implementationReviewPostureLiveRun,
		RefactorPassForkPoint:  refactorPassForkPoint(cfg.Feature),
		PlanPath:               cfg.PlanPath,
		ExitCriteria:           cfg.ExitCriteria,
		ProgressPath:           input.ProgressPath,
		IterDir:                iterDir,
		ContractPath:           input.ContractPath,
		VerificationReportPath: input.VerificationReportPath,
		Iteration:              iteration,
		RequiredVerification:   input.RequiredVerification,
		RoadmapPath:            cfg.RoadmapPath,
		PhaseType:              cfg.PhaseType,
		FeedbackPath:           feedbackPath,
	})
	if cfg.Feature != nil {
		if block := visualReferencesSection(cfg.Feature.Images, "reviewing this iteration"); block != "" {
			reviewPrompt = block + reviewPrompt
		}
	}
	if cfg.Feature != nil {
		if run := cfg.Feature.Run(); run != nil {
			currentPhase := cfg.Feature.CurrentRoadmapPhase
			if block := deferralsDueThisPhaseSection(run.Deferrals, currentPhase, deferralPromptKindReview, cfg.RepoName); block != "" {
				reviewPrompt = block + reviewPrompt
			}
		}
	}
	if addendum := KnownCaveatsReviewAddendum(input.KnownCaveatsGateResult); addendum != "" {
		reviewPrompt = addendum + "\n\n" + reviewPrompt
	}

	reviewID := implementationReviewSessionID(cfg, axisSlug, iteration)
	model := cfg.ReviewModel
	if axis.ModelOverride != "" {
		model = axis.ModelOverride
	}
	helper := &PhaseRunner{
		SessionManager:   sm,
		FeatureStore:     cfg.FeatureStore,
		Registry:         cfg.Registry,
		StateDir:         cfg.StateDir,
		SkillsDir:        cfg.SkillsDir,
		GuidelinesDir:    cfg.GuidelinesDir,
		Observer:         cfg.Observer,
		OnFeatureResumed: cfg.OnFeatureResumed,
		BuildSessionFn:   cfg.BuildSession,
	}
	featureID := ""
	if cfg.Feature != nil {
		featureID = cfg.Feature.ID
	}
	helperCfg := ReviewHelperConfig{
		SessionID:              reviewID,
		FeatureID:              featureID,
		Phase:                  feature.PhaseReview,
		ParentSpanCtx:          parentCtx,
		Model:                  model,
		Prompt:                 reviewPrompt,
		PromptPath:             filepath.Join(axisDir, "review-prompt.md"),
		FeedbackPath:           feedbackPath,
		HelperIterDir:          axisDir,
		Role:                   axis.Role,
		WorkDir:                cfg.WorkDir,
		RepoName:               cfg.RepoName,
		AdditionalDirs:         cfg.AdditionalDirs,
		LogPath:                filepath.Join(axisDir, "review-output.txt"),
		SystemPromptPrefix:     "implementation-review-" + axisSlug,
		CompletionAskingClause: cfg.AskingClause,
		EffortLevel:            reviewHelperEffortFromImpl(cfg),
		EffectiveEffort:        cfg.ReviewEffectiveEffort,
		EffortSource:           cfg.ReviewEffortSource,
		Kind:                   ports.KindValidator,
		Label:                  axis.Name,
		ResumeFeature:          cfg.Feature,
		ResumeParent: ResumeParentContext{
			PhaseKey:  implementResumePhaseKey(cfg),
			Iteration: iteration,
		},
		ResumeChildKey:     axisSlug,
		ResumePhaseContext: fmt.Sprintf("You were mid the %s implementation-review axis for iteration %d.", axis.Name, iteration),
	}
	var helperResult *ReviewHelperResult
	var err error
	switch axis.ExecutionPosture {
	case implementationReviewPostureLiveRun:
		helperResult, err = helper.RunLiveRunReviewHelper(context.Background(), helperCfg)
	default:
		helperResult, err = helper.RunReadOnlyReviewHelper(context.Background(), helperCfg)
	}
	if err != nil {
		feedback := ""
		if helperResult != nil {
			feedback = helperResult.Feedback
		}
		if _, statErr := os.Stat(feedbackPath); os.IsNotExist(statErr) {
			stub := FormatStructuredReviewFeedback(
				fmt.Sprintf("%s Implementation Review — Helper Failed", axis.Name),
				fmt.Sprintf("- **Critical**: %s implementation review axis terminated before writing review-feedback.md: %v", axis.Name, err),
				"",
				ReviewChangesRequested,
			)
			_ = os.WriteFile(feedbackPath, []byte(stub), 0o644)
			feedback = stub
		}
		return ReviewChangesRequested, feedback, err
	}
	return helperResult.Status, helperResult.Feedback, nil
}

func implementationReviewSessionID(cfg ImplementConfig, axisSlug string, iteration int) string {
	featureID := "feature"
	phasePart := ""
	if cfg.Feature != nil {
		featureID = cfg.Feature.ID
		if cfg.Feature.CurrentRoadmapPhase > 0 {
			phasePart = fmt.Sprintf("-phase-%02d", cfg.Feature.CurrentRoadmapPhase)
		}
	}
	return fmt.Sprintf("%s%s-implementation-review-%s-%02d", featureID, phasePart, axisSlug, iteration)
}

func designArtifactPathForImplementationReview(f *feature.Feature) string {
	if f == nil {
		return ""
	}
	return f.DesignArtifactPath()
}

func implementationReviewAxisSlug(name string) string {
	slug := strings.ToLower(name)
	slug = strings.ReplaceAll(slug, "/", "-")
	slug = strings.ReplaceAll(slug, " ", "-")
	return slug
}

func setImplementationReviewAxisStatuses(cfg ImplementConfig, axes []implementationReviewAxis) {
	if cfg.Feature == nil {
		return
	}
	names := make([]string, 0, len(axes))
	for _, axis := range axes {
		names = append(names, axis.Name)
	}
	setMultiAxisValidatorStatuses(cfg.FeatureStore, cfg.Feature.ID, names)
}

func updateImplementationReviewAxisStatus(cfg ImplementConfig, axisName string, status ReviewStatus, err error) {
	if cfg.Feature == nil {
		return
	}
	updateMultiAxisValidatorStatus(cfg.FeatureStore, cfg.Feature.ID, axisName, status, err)
}

func clearImplementationReviewAxisStatuses(cfg ImplementConfig) {
	if cfg.Feature == nil {
		return
	}
	clearMultiAxisValidatorStatuses(cfg.FeatureStore, cfg.Feature.ID)
}

func composeImplementationReviewFeedback(results []reviewAxisResult, selectedCount int) (ReviewStatus, string, error) {
	return composeMultiAxisReviewFeedback("Multi-Axis Implementation Review", results, selectedCount)
}

func composeMultiAxisReviewFeedback(title string, results []reviewAxisResult, selectedCount int) (ReviewStatus, string, error) {
	status := strictMultiAxisReviewStatus(results, selectedCount)
	firstErr := firstMultiAxisReviewError(results)
	var findings strings.Builder
	var suggestions strings.Builder
	for _, result := range results {
		fmt.Fprintf(&findings, "### %s\n", result.Axis)
		axisFindings := strings.TrimSpace(extractMarkdownSection(result.Feedback, "## Findings"))
		if result.Error != nil {
			fmt.Fprintf(&findings, "- **Critical**: %s axis failed: %v\n\n", result.Axis, result.Error)
		} else if axisFindings == "" {
			findings.WriteString("- (none)\n\n")
		} else {
			fmt.Fprintf(&findings, "%s\n\n", axisFindings)
		}

		fmt.Fprintf(&suggestions, "### %s\n", result.Axis)
		axisSuggestions := strings.TrimSpace(extractMarkdownSection(result.Feedback, "## Suggestions"))
		if axisSuggestions == "" {
			suggestions.WriteString("- (none)\n\n")
		} else {
			fmt.Fprintf(&suggestions, "%s\n\n", axisSuggestions)
		}
	}
	return status, FormatStructuredReviewFeedback(
		title,
		strings.TrimRight(findings.String(), "\n"),
		strings.TrimRight(suggestions.String(), "\n"),
		status,
	), firstErr
}
