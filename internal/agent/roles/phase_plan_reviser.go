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
	"github.com/doordash-oss/agentic-orchestrator/internal/agent/prompts"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// RolePlanPhaseReviser is the per-roadmap-phase plan revision session.
const RolePlanPhaseReviser Role = "plan_phase_reviser"

var phasePlanReviserRoleSpec = RoleSpec{
	Phase:        feature.PhasePlan,
	Role:         RolePlanPhaseReviser,
	SkillName:    "revise-phase-plan",
	UserTemplate: "phase_plan_revision.user",
	OutputRoots: []OutputRootSpec{
		artifactDirOutputRoot("Shared per-phase plan artifact root. Revisions update the phase plan markdown here across attempts."),
		attemptDirOutputRoot("Active phase-plan revision attempt directory. Debug prompts, attempt metadata, validator output, and phase_complete are written here."),
	},
	MarkerRoot: "attempt_dir",
	Artifacts: []RoleArtifactSpec{
		phasePlanMarkdownRoleArtifact(),
		planAttemptMetaRoleArtifact(),
	},
	ReadOnlyOutsideRoots: true,
}

// PhasePlanReviserRoleSpec returns the RoleSpec-backed phase-plan revision role.
func PhasePlanReviserRoleSpec() RoleSpec {
	return CloneRoleSpec(phasePlanReviserRoleSpec)
}

// PhasePlanRevisionUserInput is the data passed to phase_plan_revision.user.tmpl.
type PhasePlanRevisionUserInput struct {
	Attempt int
	Phase   PhasePlanView

	Feedback string

	PriorAxisApprovals prompts.PriorAxisApprovalsInput
	PhasePlanPath      string

	PhasePlanFormatPath string

	Inquireness prompts.AutonomousInquirenessInput
}

// BuildPhasePlanRevisionPrompt renders the phase-plan revision prompt.
func BuildPhasePlanRevisionPrompt(in PhasePlanRevisionUserInput) string {
	return prompts.PhasePlanRevisionUserPrompt(in)
}
