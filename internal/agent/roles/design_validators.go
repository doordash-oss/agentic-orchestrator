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

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

const (
	// RoleValidateDesignIntegrity validates objective alignment, coherence, and implementation readiness.
	RoleValidateDesignIntegrity Role = "validate_design_integrity"
	// RoleValidateDesignVisual validates an applicable UI mockup bundle.
	RoleValidateDesignVisual Role = "validate_design_visual"
)

var designValidatorRoleSpecs = []RoleSpec{
	designValidatorRoleSpec(RoleValidateDesignIntegrity, "validate-design-integrity", "integrity"),
	designValidatorRoleSpec(RoleValidateDesignVisual, "validate-design-visual", "visual"),
}

// DesignValidatorRoleSpecs returns the Design critic RoleSpecs.
func DesignValidatorRoleSpecs() []RoleSpec {
	out := make([]RoleSpec, 0, len(designValidatorRoleSpecs))
	for _, spec := range designValidatorRoleSpecs {
		out = append(out, CloneRoleSpec(spec))
	}
	return out
}

// DesignValidatorRoleForSkill returns the critic RoleSpec for a Design critic skill.
func DesignValidatorRoleForSkill(skillName string) (RoleSpec, bool) {
	for _, spec := range designValidatorRoleSpecs {
		if spec.SkillName == skillName {
			return CloneRoleSpec(spec), true
		}
	}
	return RoleSpec{}, false
}

func designValidatorRoleSpec(role Role, skillName, axis string) RoleSpec {
	return reviewFeedbackAxisRoleSpec(reviewFeedbackAxisRoleSpecConfig{
		Phase:        feature.PhaseDesign,
		Role:         role,
		SkillName:    skillName,
		UserTemplate: "validate_specialized.user",
		OutputRoots: []OutputRootSpec{
			validatorAttemptDirOutputRoot(),
			validatorHelperDirOutputRoot(),
		},
		MarkerRoot: "helper_dir",
		Artifact: RoleArtifactSpec{
			Name:         "design_validator_feedback",
			DisplayPath:  fmt.Sprintf("validation-%s-feedback.md", axis),
			RootName:     "helper_dir",
			RelativePath: fmt.Sprintf("validation-%s-feedback.md", axis),
			Presence:     ArtifactRequired,
			Description:  "structured Design validation feedback with findings, suggestions, and verdict",
			Validate:     ValidatorReviewFeedback,
		},
		ReadOnlyOutsideRoots: true,
	})
}
