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

package roles

import (
	"fmt"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent/prompts"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

const (
	// RoleValidateRoadmapArchitecture validates roadmap architecture.
	RoleValidateRoadmapArchitecture Role = "validate_roadmap_architecture"
	// RoleValidateRoadmapScope validates roadmap scope.
	RoleValidateRoadmapScope Role = "validate_roadmap_scope"
	// RoleValidatePhasePlanStructural validates phase-plan structure.
	RoleValidatePhasePlanStructural Role = "validate_phase_plan_structural"
	// RoleValidatePhasePlanScope validates phase-plan scope.
	RoleValidatePhasePlanScope Role = "validate_phase_plan_scope"
	// RoleValidatePhasePlanGrounding validates phase-plan grounding.
	RoleValidatePhasePlanGrounding Role = "validate_phase_plan_grounding"
	// RoleValidatePlanSecurity validates security for roadmap or phase-plan artifacts.
	RoleValidatePlanSecurity Role = "validate_plan_security"
	// RoleValidatePlanPerformance validates performance for roadmap or phase-plan artifacts.
	RoleValidatePlanPerformance Role = "validate_plan_performance"
	// RoleValidatePlanTesting validates test coverage for phase-plan artifacts.
	RoleValidatePlanTesting Role = "validate_plan_testing"
)

var planValidatorRoleSpecs = []RoleSpec{
	planValidatorRoleSpec(RoleValidateRoadmapArchitecture, "validate-roadmap-architecture", "architecture"),
	planValidatorRoleSpec(RoleValidateRoadmapScope, "validate-roadmap-scope", "scope"),
	planValidatorRoleSpec(RoleValidatePhasePlanStructural, "validate-phase-plan-structural", "structural"),
	planValidatorRoleSpec(RoleValidatePhasePlanScope, "validate-phase-plan-scope", "scope"),
	planValidatorRoleSpec(RoleValidatePhasePlanGrounding, "validate-phase-plan-grounding", "grounding"),
	planValidatorRoleSpec(RoleValidatePlanSecurity, "validate-plan-security", "security"),
	planValidatorRoleSpec(RoleValidatePlanPerformance, "validate-plan-performance", "performance"),
	planValidatorRoleSpec(RoleValidatePlanTesting, "validate-plan-testing", "testing"),
}

// PlanValidatorRoleSpecs returns the RoleSpec-backed per-axis validator roles.
func PlanValidatorRoleSpecs() []RoleSpec {
	out := make([]RoleSpec, 0, len(planValidatorRoleSpecs))
	for _, spec := range planValidatorRoleSpecs {
		out = append(out, CloneRoleSpec(spec))
	}
	return out
}

// PlanValidatorRoleForSkill returns the validator RoleSpec for a validator
// skill name such as "validate-roadmap-architecture".
func PlanValidatorRoleForSkill(skillName string) (RoleSpec, bool) {
	for _, spec := range planValidatorRoleSpecs {
		if spec.SkillName == skillName {
			return CloneRoleSpec(spec), true
		}
	}
	return RoleSpec{}, false
}

func planValidatorRoleSpec(role Role, skillName, axis string) RoleSpec {
	return reviewFeedbackAxisRoleSpec(reviewFeedbackAxisRoleSpecConfig{
		Phase:        feature.PhasePlan,
		Role:         role,
		SkillName:    skillName,
		UserTemplate: "validate_specialized.user",
		OutputRoots: []OutputRootSpec{
			validatorAttemptDirOutputRoot(),
			validatorHelperDirOutputRoot(),
		},
		Artifact: RoleArtifactSpec{
			Name:         "plan_validator_feedback",
			DisplayPath:  fmt.Sprintf("validation-%s-feedback.md", axis),
			RootName:     "helper_dir",
			RelativePath: fmt.Sprintf("validation-%s-feedback.md", axis),
			Presence:     ArtifactRequired,
			Description:  "structured validation feedback markdown with verdict and findings for this axis",
			Validate:     ValidatorReviewFeedback,
		},
	})
}

// ValidateSpecializedUserInput is the data passed to
// validate_specialized.user.tmpl.
type ValidateSpecializedUserInput struct {
	Name         string
	Description  string
	ExitCriteria string
	RiskLevel    string

	DomainName string
	PlanPath   string

	IncludePriorPhaseContext bool
	PriorPhasePlanPaths      []string

	IsRoadmapKind bool

	ResearchPath string

	FeedbackPath string
	AxisLabel    string

	AutomatedVerificationOnly bool
}

// BuildValidateSpecializedPrompt renders a per-axis validation prompt.
func BuildValidateSpecializedPrompt(in ValidateSpecializedUserInput) string {
	return prompts.ValidateSpecializedUserPrompt(in)
}
