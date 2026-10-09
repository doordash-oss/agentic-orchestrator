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
	"path/filepath"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent/prompts"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// roadmapFormatPath returns the absolute path to the roadmap output-format
// companion file shipped alongside the create-roadmap skill, or "" when
// skillsDir is unset. The reviser reads this file in addition to its own
// SKILL.md so the cross-skill `../create-roadmap/format.md` reference does
// not have to be resolved by relative-path inference at runtime.
func roadmapFormatPath(skillsDir string) string {
	if skillsDir == "" {
		return ""
	}
	return filepath.Join(skillsDir, "create-roadmap", "format.md")
}

// phasePlanFormatPath returns the absolute path to the per-phase plan
// output-format companion file shipped alongside the plan-phase skill,
// or "" when skillsDir is unset. Mirrors roadmapFormatPath: the
// revise-phase-plan skill reads this file in addition to its own
// SKILL.md so the cross-skill `../plan-phase/format.md` reference does
// not have to be resolved by relative-path inference at runtime.
func phasePlanFormatPath(skillsDir string) string {
	if skillsDir == "" {
		return ""
	}
	return filepath.Join(skillsDir, "plan-phase", "format.md")
}

// roadmapFeatureViews projects a feature's repos for the roadmap-family
// templates. Returned slice is an independent copy.
func roadmapFeatureViews(f *feature.Feature) []prompts.RepoView {
	repos := make([]prompts.RepoView, 0, len(f.Repos))
	for _, r := range f.Repos {
		path := r.Path
		if r.WorktreePath != "" {
			path = r.WorktreePath
		}
		repos = append(repos, prompts.RepoView{Name: r.Name, Path: path})
	}
	return repos
}

// axisApprovalViews converts []AxisApproval to []prompts.AxisApprovalView.
func axisApprovalViews(approvals []AxisApproval) []prompts.AxisApprovalView {
	views := make([]prompts.AxisApprovalView, 0, len(approvals))
	for _, a := range approvals {
		views = append(views, prompts.AxisApprovalView{
			Axis:           a.Axis,
			FrozenSections: a.FrozenSections,
		})
	}
	return views
}

// refactorPassForkPoint names a refactor child's fork-point commits, one
// "repo @ sha" clause per repository. Empty for top-level features. Planning
// and design receive it as spec-grounding context: intent language like
// "current" or "as they are" refers to this state, and the produced artifact
// must resolve such language into absolute end-state statements.
func refactorPassForkPoint(f *feature.Feature) string {
	if f == nil || !f.IsChild() {
		return ""
	}
	var parts []string
	for _, r := range f.Repos {
		if sha := f.BaseSHA(r.Name); sha != "" {
			parts = append(parts, fmt.Sprintf("%s @ %s", r.Name, sha))
		}
	}
	return strings.Join(parts, ", ")
}

// BuildRoadmapPromptWithResearch constructs the initial roadmap prompt with
// the approved design artifact plus the research artifact as grounding context.
//
// The prose lives in internal/agent/prompts/templates/roadmap.user.tmpl.
func BuildRoadmapPromptWithResearch(f *feature.Feature, designArtifactPath, researchArtifactPath string, qaFilePaths []string) string {
	repos := roadmapFeatureViews(f)
	return prompts.MustRender(prompts.TemplateRoadmapUser, roadmapUserInput{
		Name:                  f.Name,
		Description:           f.Description,
		ExitCriteria:          f.ExitCriteria,
		Repos:                 repos,
		RefactorPassForkPoint: refactorPassForkPoint(f),
		DesignArtifactPath:    designArtifactPath,
		ResearchArtifactPath:  researchArtifactPath,
		VisualReferences: prompts.VisualReferencesInput{
			Images: append([]string(nil), f.Images...),
			Label:  "producing the roadmap",
		},
		QAFiles: prompts.QAFilesInput{
			Paths:         append([]string(nil), qaFilePaths...),
			Lead:          "Read these Q&A files for important context about their intent and preferences — do not re-ask questions that have already been answered:",
			TrailingBlank: true,
		},
		MultiRepo:   len(repos) > 1,
		Inquireness: prompts.GrillMeInquirenessInput{Level: string(f.Inquireness)},
	})
}

// BuildRoadmapRevisionPrompt constructs the prompt for roadmap revision
// after critic feedback. When approvals is non-empty, a "Prior Axis Approvals"
// block is injected so the reviser can apply the Sticky Approval Respect
// procedure (documented in skills/revise-roadmap/SKILL.md).
//
// skillsDir locates the companion format file in the reconciled skills
// directory. The primary skill directive lives in the RoleSpec-backed system
// prompt.
//
// The prose lives in
// internal/agent/prompts/templates/roadmap_revision.user.tmpl.
func BuildRoadmapRevisionPrompt(skillsDir, previousRoadmapPath, criticFeedback string, attempt int, approvals []AxisApproval) string {
	return prompts.MustRender(prompts.TemplateRoadmapRevisionUser, roadmapRevisionUserInput{
		Attempt:        attempt,
		CriticFeedback: criticFeedback,
		PriorAxisApprovals: prompts.PriorAxisApprovalsInput{
			ArtifactName: "roadmap",
			Approvals:    axisApprovalViews(approvals),
		},
		PreviousRoadmapPath: previousRoadmapPath,
		RoadmapFormatPath:   roadmapFormatPath(skillsDir),
		Inquireness:         prompts.AutonomousInquirenessInput{},
	})
}

// BuildPhasePlanPromptWithResearch constructs the initial per-phase plan prompt
// with the approved roadmap plus the research artifact as grounding context.
//
// The prose lives in internal/agent/prompts/templates/phase_plan.user.tmpl.
//
// qaFilePaths are paths to upstream Q&A files
// (inquire/research/design/roadmap qa-answers.md) collected by
// the orchestrator. Phase-Plan re-injects them under a "User Decisions"
// section so the planner reads the user's prior answers verbatim and
// does not re-litigate them. Visual references are intentionally
// omitted — the approved roadmap already incorporates them. Phase-Plan's
// own qa-answers.md is NOT propagated to Implement (Implement reads
// the phase-plan markdown artifact, which already encodes all decisions).
func BuildPhasePlanPromptWithResearch(f *feature.Feature, roadmapPath, researchArtifactPath string, phase RoadmapPhase, qaFilePaths []string) string {
	return prompts.MustRender(prompts.TemplatePhasePlanUser, phasePlanUserInput{
		Phase: phasePlanView{
			Number:        phase.Number,
			Name:          phase.Name,
			Type:          string(phase.Type),
			Goal:          phase.Goal,
			StubsToRetire: append([]string(nil), phase.StubsToRetire...),
		},
		RoadmapPath:          roadmapPath,
		ResearchArtifactPath: researchArtifactPath,
		QAFiles: prompts.QAFilesInput{
			Paths:         append([]string(nil), qaFilePaths...),
			Lead:          "Read these Q&A files for important context about their intent and preferences — do not re-ask questions that have already been answered:",
			TrailingBlank: true,
		},
		Inquireness:               prompts.GrillMeInquirenessInput{Level: string(f.Inquireness)},
		AutomatedVerificationOnly: !f.EffectivePipeline().ShouldContractAgentEvidence(),
	})
}

// BuildPhasePlanRevisionPrompt constructs the prompt for per-phase plan revision
// after critic feedback. When approvals is non-empty, a "Prior Axis Approvals"
// block is injected so the reviser preserves sticky-approved sections across
// attempts (see skills/revise-phase-plan/SKILL.md, rule 6).
//
// skillsDir locates the companion format file in the reconciled skills
// directory. The primary skill directive lives in the RoleSpec-backed system
// prompt.
//
// The prose lives in
// internal/agent/prompts/templates/phase_plan_revision.user.tmpl.
func BuildPhasePlanRevisionPrompt(f *feature.Feature, skillsDir, phasePlanPath, feedback string, phase RoadmapPhase, attempt int, approvals []AxisApproval) string {
	return prompts.MustRender(prompts.TemplatePhasePlanRevisionUser, phasePlanRevisionUserInput{
		Attempt: attempt,
		Phase: phasePlanView{
			Number: phase.Number,
			Name:   phase.Name,
			Type:   string(phase.Type),
		},
		Feedback: feedback,
		PriorAxisApprovals: prompts.PriorAxisApprovalsInput{
			ArtifactName: "phase plan",
			Approvals:    axisApprovalViews(approvals),
		},
		PhasePlanPath:             phasePlanPath,
		PhasePlanFormatPath:       phasePlanFormatPath(skillsDir),
		Inquireness:               prompts.AutonomousInquirenessInput{},
		AutomatedVerificationOnly: !f.EffectivePipeline().ShouldContractAgentEvidence(),
	})
}
