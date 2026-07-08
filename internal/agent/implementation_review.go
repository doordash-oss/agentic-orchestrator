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
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

type implementationReviewGate string

const implementationReviewGatePerPhase implementationReviewGate = "per_phase"

type implementationReviewAxis struct {
	Name          string
	ShortName     string
	SkillName     string
	Gate          implementationReviewGate
	Order         int
	Role          Role
	ReadOnly      bool
	ModelOverride string
	Applies       func(feature.PipelineProfile) bool
}

var implementationReviewAxisRegistry = []implementationReviewAxis{
	{
		Name:      "Craft",
		ShortName: "Craft",
		SkillName: "review-implementation-craft",
		Gate:      implementationReviewGatePerPhase,
		Order:     10,
		Role:      RoleImplementationReviewCraft,
		ReadOnly:  true,
		Applies:   moonshotImplementationReviewAxis,
	},
	{
		Name:      "Functionality/Evidence",
		ShortName: "Func",
		SkillName: "review-implementation-functionality-evidence",
		Gate:      implementationReviewGatePerPhase,
		Order:     20,
		Role:      RoleImplementationReviewFunctionalityEvidence,
		ReadOnly:  true,
		Applies:   moonshotImplementationReviewAxis,
	},
	{
		Name:      "Cleanliness",
		ShortName: "Clean",
		SkillName: "review-implementation-cleanliness",
		Gate:      implementationReviewGatePerPhase,
		Order:     30,
		Role:      RoleImplementationReviewCleanliness,
		ReadOnly:  true,
		Applies:   moonshotImplementationReviewAxis,
	},
}

func moonshotImplementationReviewAxis(profile feature.PipelineProfile) bool {
	return profile == feature.PipelineMoonshot
}

func implementationReviewAxesForGate(gate implementationReviewGate, profile feature.PipelineProfile) []implementationReviewAxis {
	var axes []implementationReviewAxis
	for _, axis := range implementationReviewAxisRegistry {
		if axis.Gate != gate {
			continue
		}
		if axis.Applies != nil && !axis.Applies(profile) {
			continue
		}
		axes = append(axes, axis)
	}
	sort.SliceStable(axes, func(i, j int) bool {
		return axes[i].Order < axes[j].Order
	})
	return axes
}

type reviewAxisResult = multiAxisReviewResult

func runImplementationReviewAxes(cfg ImplementConfig, sm ports.SessionManager, iteration int, iterDir, reviewDir string, reviewCtx observe.SpanContext, input implementationReviewInput) (ReviewStatus, string, error) {
	profile := feature.PipelineMoonshot
	if cfg.Feature != nil {
		profile = cfg.Feature.EffectivePipeline()
	}
	axes := implementationReviewAxesForGate(implementationReviewGatePerPhase, profile)
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
	RemovePhaseComplete(axisDir)

	feedbackPath := filepath.Join(axisDir, "review-feedback.md")
	reviewPrompt := BuildImplementationReviewAxisPrompt(
		cfg.PlanPath,
		cfg.ExitCriteria,
		input.ProgressPath,
		iterDir,
		input.ContractPath,
		input.VerificationReportPath,
		iteration,
		input.RequiredVerification,
		cfg.RoadmapPath,
		cfg.PhaseType,
		feedbackPath,
		axis.Name,
	)
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
		SessionManager: sm,
		FeatureStore:   cfg.FeatureStore,
		StateDir:       cfg.StateDir,
		SkillsDir:      cfg.SkillsDir,
		GuidelinesDir:  cfg.GuidelinesDir,
		Observer:       cfg.Observer,
		BuildSessionFn: cfg.BuildSession,
	}
	featureID := ""
	if cfg.Feature != nil {
		featureID = cfg.Feature.ID
	}
	helperResult, err := helper.RunReadOnlyReviewHelper(context.Background(), ReviewHelperConfig{
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
		EffortLevel:            cfg.EffortLevel,
		Kind:                   ports.KindValidator,
		Label:                  axis.Name,
	})
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
		"Multi-Axis Implementation Review",
		strings.TrimRight(findings.String(), "\n"),
		strings.TrimRight(suggestions.String(), "\n"),
		status,
	), firstErr
}
