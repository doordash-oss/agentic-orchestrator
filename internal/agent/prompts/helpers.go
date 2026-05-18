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

package prompts

// GrillMeInquireness renders the grillme_inquireness partial for a given
// inquireness level. level is the raw "none" / "medium" / "high" string;
// any other value falls through to the medium branch.
func GrillMeInquireness(level string) string {
	return MustRender("grillme_inquireness", GrillMeInquirenessInput{Level: level})
}

// AutonomousInquireness renders the autonomous_inquireness partial used by
// revision phases. The partial takes no inputs; the helper exists so callers
// can drop the rendered string into ad-hoc compositions without spelling
// MustRender at every call site.
func AutonomousInquireness() string {
	return MustRender("autonomous_inquireness", AutonomousInquirenessInput{})
}

// InquireUserPrompt renders the full Inquire-phase user prompt body, minus
// the leading skill instruction (which the shared runInteractivePhase
// helper prepends).
func InquireUserPrompt(in any) string {
	return MustRender("inquire.user", in)
}

// RoleSystemPrompt renders the generic RoleSpec-backed phase system prompt.
func RoleSystemPrompt(in RoleSystemInput) string {
	return MustRender("system", in)
}

// VisualReferences renders the visual_references partial. Returns "" when
// Images is empty so callers can drop the result into a prompt
// unconditionally.
func VisualReferences(in VisualReferencesInput) string {
	if len(in.Images) == 0 {
		return ""
	}
	if in.Label == "" {
		in.Label = "working on this feature"
	}
	return MustRender("visual_references", in)
}

// QAFiles renders the qa_files partial. Returns "" when Paths is empty.
func QAFiles(in QAFilesInput) string {
	if len(in.Paths) == 0 {
		return ""
	}
	return MustRender("qa_files", in)
}

// PriorAxisApprovals renders the prior_axis_approvals partial. Returns ""
// when Approvals is empty.
func PriorAxisApprovals(in PriorAxisApprovalsInput) string {
	if len(in.Approvals) == 0 {
		return ""
	}
	return MustRender("prior_axis_approvals", in)
}

// DeferralsDue renders the deferrals_due partial. Returns "" when Entries
// is empty.
func DeferralsDue(in DeferralsDueInput) string {
	if len(in.Entries) == 0 {
		return ""
	}
	return MustRender("deferrals_due", in)
}

// ResearchFromQuestionsUserPrompt renders the Research-phase user prompt for
// the question-driven path (research_from_questions.user.tmpl).
func ResearchFromQuestionsUserPrompt(in any) string {
	return MustRender("research_from_questions.user", in)
}

// KBBuildUserPrompt renders the KB-build phase user prompt
// (kb_build.user.tmpl).
func KBBuildUserPrompt(in any) string {
	return MustRender("kb_build.user", in)
}

// TweakUserPrompt renders the Tweak-session seed user prompt
// (tweak.user.tmpl).
func TweakUserPrompt(in any) string {
	return MustRender("tweak.user", in)
}

// DesignUserPrompt renders the canonical Design-phase user prompt
// (design.user.tmpl).
func DesignUserPrompt(in any) string {
	return MustRender("design.user", in)
}

// BrainstormUserPrompt renders the legacy Brainstorm-phase user prompt
// (brainstorm.user.tmpl). The template body matches design.user, so this
// helper is the legacy entry point retained for compatibility with callers
// and snapshots that still reference the Brainstorm surface.
func BrainstormUserPrompt(in any) string {
	return MustRender("brainstorm.user", in)
}

// RefactorPlanUserPrompt renders the refactor-plan step user prompt
// (refactor_plan.user.tmpl).
func RefactorPlanUserPrompt(in any) string {
	return MustRender("refactor_plan.user", in)
}

// RoadmapUserPrompt renders the Plan/Roadmap initial-creation user prompt
// (roadmap.user.tmpl).
func RoadmapUserPrompt(in any) string {
	return MustRender("roadmap.user", in)
}

// RoadmapRevisionUserPrompt renders the Plan/Roadmap revision user prompt
// (roadmap_revision.user.tmpl).
func RoadmapRevisionUserPrompt(in any) string {
	return MustRender("roadmap_revision.user", in)
}

// PhasePlanUserPrompt renders the per-phase plan-creation user prompt
// (phase_plan.user.tmpl).
func PhasePlanUserPrompt(in any) string {
	return MustRender("phase_plan.user", in)
}

// PhasePlanRevisionUserPrompt renders the per-phase plan-revision user
// prompt (phase_plan_revision.user.tmpl).
func PhasePlanRevisionUserPrompt(in any) string {
	return MustRender("phase_plan_revision.user", in)
}

// ImplementUserPrompt renders the per-iteration Implement-phase user prompt
// (implement.user.tmpl).
func ImplementUserPrompt(in any) string {
	return MustRender("implement.user", in)
}

// ReviewUserPrompt renders the Review-gate user prompt (review.user.tmpl).
func ReviewUserPrompt(in any) string {
	return MustRender("review.user", in)
}

// SummaryUserPrompt renders the bounded-helper summary-generation prompt
// (summary.user.tmpl).
func SummaryUserPrompt(in SummaryUserInput) string {
	return MustRender("summary.user", in)
}

// PRDescriptionUserPrompt renders the Publish-phase PR-description prompt
// (pr_description.user.tmpl).
func PRDescriptionUserPrompt(in PRDescriptionUserInput) string {
	return MustRender("pr_description.user", in)
}

// FinalFixUserPrompt renders the Final-Review fix-agent user prompt
// (final_fix.user.tmpl).
func FinalFixUserPrompt(in any) string {
	return MustRender("final_fix.user", in)
}

// FinalReviewUserPrompt renders the Final-Review interactive-reviewer
// user prompt (final_review.user.tmpl).
func FinalReviewUserPrompt(in any) string {
	return MustRender("final_review.user", in)
}

// ScoutUserPrompt renders the per-scout subprocess prompt (scout.user.tmpl).
func ScoutUserPrompt(in ScoutUserInput) string {
	return MustRender("scout.user", in)
}

// ValidateSpecializedUserPrompt renders the per-axis specialized-validator
// user prompt (validate_specialized.user.tmpl).
func ValidateSpecializedUserPrompt(in any) string {
	return MustRender("validate_specialized.user", in)
}
