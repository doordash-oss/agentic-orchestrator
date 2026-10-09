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

import "strings"

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

// AutoReviewUserPrompt renders the hidden automatic Bash safety-review prompt.
func AutoReviewUserPrompt(in AutoReviewUserInput) string {
	return MustRender("autoreview.user", in)
}

// AutoReviewFormatRetryPrompt reminds a fresh reviewer to obey the output contract.
func AutoReviewFormatRetryPrompt() string {
	return MustRender("autoreview.format_retry", nil)
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

// DesignUserPrompt renders the canonical Design-phase user prompt
// (design.user.tmpl).
func DesignUserPrompt(in any) string {
	return MustRender("design.user", in)
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

// ImplementationReviewAxisUserPrompt renders the per-axis implementation
// review prompt (implementation_review_axis.user.tmpl).
func ImplementationReviewAxisUserPrompt(in any) string {
	return strings.TrimSpace(MustRender("implementation_review_axis.user", in)) + "\n"
}

// SupervisorSystemPrompt renders the supervisor conversation's launch-channel
// system prompt (supervisor.system.tmpl).
func SupervisorSystemPrompt(in SupervisorSystemInput) string {
	return strings.TrimSpace(MustRender("supervisor.system", in)) + "\n"
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

// ValidateSpecializedUserPrompt renders the per-axis specialized-validator
// user prompt (validate_specialized.user.tmpl).
func ValidateSpecializedUserPrompt(in any) string {
	return MustRender("validate_specialized.user", in)
}
