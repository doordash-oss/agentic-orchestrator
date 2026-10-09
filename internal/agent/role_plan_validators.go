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
	"slices"

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
	// RoleValidatePlanSecurity validates security for roadmap or phase-plan artifacts.
	RoleValidatePlanSecurity Role = "validate_plan_security"
	// RoleValidatePlanPerformance validates performance for roadmap or phase-plan artifacts.
	RoleValidatePlanPerformance Role = "validate_plan_performance"
	// RoleValidatePlanTesting validates test coverage for phase-plan artifacts.
	RoleValidatePlanTesting Role = "validate_plan_testing"
)

// planValidatorAxis declares one plan-validator axis. Name is the display
// name shown in feedback and validator status; Axis names the helper
// directory, feedback file, prompt axis label and stall-tracker key. Kinds and
// HighRiskOnly decide which artifact kinds and risk tiers select the axis.
type planValidatorAxis struct {
	Name         string
	Axis         string
	Role         Role
	SkillName    string
	Kinds        []validationArtifactKind
	HighRiskOnly bool
}

var (
	roadmapValidatorKinds   = []validationArtifactKind{validationArtifactRoadmap}
	phasePlanValidatorKinds = []validationArtifactKind{validationArtifactPhasePlan}
	allPlanValidatorKinds   = []validationArtifactKind{validationArtifactRoadmap, validationArtifactPhasePlan}
)

// planValidatorAxes declares every plan-validator axis once. Row order is the
// selection order within each artifact kind, which fixes the order of the
// composed validator feedback.
var planValidatorAxes = []planValidatorAxis{
	{Name: "Architecture", Axis: "architecture", Role: RoleValidateRoadmapArchitecture, SkillName: "validate-roadmap-architecture", Kinds: roadmapValidatorKinds},
	{Name: "Structural", Axis: "structural", Role: RoleValidatePhasePlanStructural, SkillName: "validate-phase-plan-structural", Kinds: phasePlanValidatorKinds},
	{Name: "Scope", Axis: "scope", Role: RoleValidatePhasePlanScope, SkillName: "validate-phase-plan-scope", Kinds: phasePlanValidatorKinds},
	{Name: "Security", Axis: "security", Role: RoleValidatePlanSecurity, SkillName: "validate-plan-security", Kinds: allPlanValidatorKinds, HighRiskOnly: true},
	{Name: "Performance", Axis: "performance", Role: RoleValidatePlanPerformance, SkillName: "validate-plan-performance", Kinds: allPlanValidatorKinds, HighRiskOnly: true},
	{Name: "Scope", Axis: "scope", Role: RoleValidateRoadmapScope, SkillName: "validate-roadmap-scope", Kinds: roadmapValidatorKinds},
	{Name: "Testing", Axis: "testing", Role: RoleValidatePlanTesting, SkillName: "validate-plan-testing", Kinds: phasePlanValidatorKinds, HighRiskOnly: true},
}

// selectPlanValidators returns the axes that validate an artifact kind at a
// risk tier, in table order.
func selectPlanValidators(kind validationArtifactKind, risk feature.RiskLevel) []planValidatorAxis {
	var axes []planValidatorAxis
	for _, axis := range planValidatorAxes {
		if !slices.Contains(axis.Kinds, kind) {
			continue
		}
		if axis.HighRiskOnly && risk != feature.RiskHigh {
			continue
		}
		axes = append(axes, axis)
	}
	return axes
}

func (a planValidatorAxis) helperDirName() string {
	return "validate-" + a.Axis
}

func (a planValidatorAxis) feedbackFileName() string {
	return fmt.Sprintf("validation-%s-feedback.md", a.Axis)
}

var planValidatorRoleSpecs = func() []roleSpec {
	specs := make([]roleSpec, 0, len(planValidatorAxes))
	for _, axis := range planValidatorAxes {
		specs = append(specs, planValidatorRoleSpec(axis))
	}
	return specs
}()

func planValidatorRoleSpec(axis planValidatorAxis) roleSpec {
	return roleSpec{
		Phase:     feature.PhasePlan,
		Role:      axis.Role,
		SkillName: axis.SkillName,
		OutputRoots: []outputRootSpec{
			iterationParentOutputRoot("attempt_dir", "Parent planning attempt directory that owns this validator helper."),
			iterationOutputRoot("helper_dir", "Validator helper artifact directory for this axis."),
		},
		Artifacts: []roleArtifactSpec{{
			Name:         "plan_validator_feedback",
			DisplayPath:  axis.feedbackFileName(),
			RootName:     "helper_dir",
			RelativePath: axis.feedbackFileName(),
			Description:  "structured validation feedback markdown with verdict and findings for this axis",
			Validate:     validateReviewFeedbackArtifact,
		}},
	}
}

// validateSpecializedUserInput is the data passed to
// validate_specialized.user.tmpl.
type validateSpecializedUserInput struct {
	Name             string
	Description      string
	ExitCriteria     string
	AcceptanceClause string
	RiskLevel        string

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
