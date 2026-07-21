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

import "github.com/doordash-oss/agentic-orchestrator/internal/feature"

var roleSpecs = append(
	append([]RoleSpec{
		implementRoleSpec,
		roadmapCreatorRoleSpec,
		roadmapReviserRoleSpec,
		phasePlanCreatorRoleSpec,
		phasePlanReviserRoleSpec,
		knowledgeBaseBuilderRoleSpec,
		inquirerRoleSpec,
		researcherRoleSpec,
		designerRoleSpec,
		refactorPlanRoleSpec,
		finalReviewFixerRoleSpec,
	}, planValidatorRoleSpecs...),
	implementationReviewAxisRoleSpecs...,
)

// All returns every canonical RoleSpec declaration.
func All() []RoleSpec {
	out := make([]RoleSpec, 0, len(roleSpecs))
	for _, spec := range roleSpecs {
		out = append(out, CloneRoleSpec(spec))
	}
	return out
}

// SkillOutputRoleSpecs returns the roles whose SKILL.md files carry generated
// Output Files sections.
func SkillOutputRoleSpecs() []RoleSpec {
	seen := map[string]bool{}
	out := make([]RoleSpec, 0, len(roleSpecs))
	for _, spec := range roleSpecs {
		if spec.SkillName == "" || seen[spec.SkillName] {
			continue
		}
		seen[spec.SkillName] = true
		out = append(out, CloneRoleSpec(spec))
	}
	return out
}

// Lookup returns the RoleSpec for a phase and role.
func Lookup(phase feature.Phase, role Role) (RoleSpec, bool) {
	for _, spec := range roleSpecs {
		if spec.Phase == phase && spec.Role == role {
			return CloneRoleSpec(spec), true
		}
	}
	return RoleSpec{}, false
}
