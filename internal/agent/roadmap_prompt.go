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
	"path/filepath"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent/prompts"
	"github.com/doordash-oss/agentic-orchestrator/internal/agent/roles"
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

// deferralsDueViews returns the typed view of deferrals due this phase, or
// nil when none. Mirrors deferralsDueThisPhaseSection's filter step.
func deferralsDueViews(f *feature.Feature, phase int) []prompts.DeferralView {
	run := f.Run()
	if run == nil {
		return nil
	}
	due := feature.DueForPhase(run.Deferrals, phase)
	if len(due) == 0 {
		return nil
	}
	views := make([]prompts.DeferralView, 0, len(due))
	for _, d := range due {
		views = append(views, prompts.DeferralView{
			ID:              d.ID,
			Description:     d.Description,
			CreatedInPhase:  d.CreatedInPhase,
			CreatedInKind:   d.CreatedInKind,
			DueByPhase:      d.DueByPhase,
			Reason:          d.Reason,
			RedeferralCount: d.RedeferralCount(),
		})
	}
	return views
}

// qaFilePaths are paths to Q&A files from earlier phases.
//
// The prose lives in internal/agent/prompts/templates/roadmap.user.tmpl.
//
// skillsDir and guidelinesDir are retained on the signature for caller
// stability. RoleSpec-backed system prompts now own the primary skill
// directive and useful-resource catalog.
func BuildRoadmapPrompt(f *feature.Feature, skillsDir, guidelinesDir, brainstormArtifactPath string, qaFilePaths []string, kbInfos ...KBInfo) string {
	_ = skillsDir
	_ = guidelinesDir
	_ = kbInfos
	repos := roadmapFeatureViews(f)
	return roles.BuildRoadmapPrompt(roles.RoadmapUserInput{
		Name:                   f.Name,
		Description:            f.EffectiveDescription(),
		Repos:                  repos,
		BrainstormArtifactPath: brainstormArtifactPath,
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
// skillsDir is retained because revision prompts still surface companion
// format-file paths from the reconciled skills directory. The primary skill
// directive lives in the RoleSpec-backed system prompt.
//
// The prose lives in
// internal/agent/prompts/templates/roadmap_revision.user.tmpl.
//
// f is currently unused by the template; retained on the signature for
// caller stability. roadmapPath is similarly unused (the path is implicit
// via previousRoadmapPath).
func BuildRoadmapRevisionPrompt(f *feature.Feature, skillsDir, roadmapPath, previousRoadmapPath, criticFeedback, brainstormArtifactPath string, attempt int, approvals []AxisApproval) string {
	_ = f
	_ = roadmapPath
	_ = brainstormArtifactPath
	return roles.BuildRoadmapRevisionPrompt(roles.RoadmapRevisionUserInput{
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

// BuildPhasePlanPrompt constructs the prompt for per-phase plan creation.
//
// The prose lives in internal/agent/prompts/templates/phase_plan.user.tmpl.
//
// qaFilePaths are paths to upstream Q&A files
// (inquire/research/brainstorm/roadmap qa-answers.md) collected by
// the orchestrator. Phase-Plan re-injects them under a "User Decisions"
// section so the planner reads the user's prior answers verbatim and
// does not re-litigate them. Visual references are intentionally
// omitted — the approved roadmap already incorporates them. Phase-Plan's
// own qa-answers.md is NOT propagated to Implement (Implement reads
// the phase-plan markdown artifact, which already encodes all decisions).
//
// skillsDir, guidelinesDir, and kbInfos are retained on the signature for
// caller stability. RoleSpec-backed system prompts now own the primary skill
// directive and useful-resource catalog.
func BuildPhasePlanPrompt(f *feature.Feature, skillsDir, guidelinesDir, roadmapPath string, phase RoadmapPhase, qaFilePaths []string, kbInfos ...KBInfo) string {
	_ = skillsDir
	_ = guidelinesDir
	_ = kbInfos
	return roles.BuildPhasePlanPrompt(roles.PhasePlanUserInput{
		Phase: roles.PhasePlanView{
			Number:        phase.Number,
			Name:          phase.Name,
			Type:          string(phase.Type),
			Goal:          phase.Goal,
			StubsToRetire: append([]string(nil), phase.StubsToRetire...),
		},
		RoadmapPath: roadmapPath,
		QAFiles: prompts.QAFilesInput{
			Paths:         append([]string(nil), qaFilePaths...),
			Lead:          "Read these Q&A files for important context about their intent and preferences — do not re-ask questions that have already been answered:",
			TrailingBlank: true,
		},
		Inquireness: prompts.GrillMeInquirenessInput{Level: string(f.Inquireness)},
	})
}

// BuildPhasePlanRevisionPrompt constructs the prompt for per-phase plan revision
// after critic feedback. When approvals is non-empty, a "Prior Axis Approvals"
// block is injected so the reviser preserves sticky-approved sections across
// attempts (see skills/revise-phase-plan/SKILL.md, rule 6).
//
// skillsDir is retained because revision prompts still surface companion
// format-file paths from the reconciled skills directory. The primary skill
// directive lives in the RoleSpec-backed system prompt.
//
// The prose lives in
// internal/agent/prompts/templates/phase_plan_revision.user.tmpl.
func BuildPhasePlanRevisionPrompt(f *feature.Feature, skillsDir, phasePlanPath, feedback, brainstormArtifactPath string, phase RoadmapPhase, attempt int, approvals []AxisApproval) string {
	_ = f
	_ = brainstormArtifactPath
	return roles.BuildPhasePlanRevisionPrompt(roles.PhasePlanRevisionUserInput{
		Attempt: attempt,
		Phase: roles.PhasePlanView{
			Number: phase.Number,
			Name:   phase.Name,
			Type:   string(phase.Type),
		},
		Feedback: feedback,
		PriorAxisApprovals: prompts.PriorAxisApprovalsInput{
			ArtifactName: "phase plan",
			Approvals:    axisApprovalViews(approvals),
		},
		PhasePlanPath:       phasePlanPath,
		PhasePlanFormatPath: phasePlanFormatPath(skillsDir),
		Inquireness:         prompts.AutonomousInquirenessInput{},
	})
}

// formatPriorAxisApprovals writes a "Prior Axis Approvals" section into b when
// approvals is non-empty. artifactName distinguishes the wording ("roadmap" vs
// "phase plan"); the skill callout is axis-name-agnostic because both revise
// skills implement the same Sticky Approval Respect procedure.
//
// The literal prose lives in
// internal/agent/prompts/partials/prior_axis_approvals.tmpl.
func formatPriorAxisApprovals(b *strings.Builder, approvals []AxisApproval, artifactName string) {
	if len(approvals) == 0 {
		return
	}
	b.WriteString(prompts.PriorAxisApprovals(prompts.PriorAxisApprovalsInput{
		ArtifactName: artifactName,
		Approvals:    axisApprovalViews(approvals),
	}))
}
