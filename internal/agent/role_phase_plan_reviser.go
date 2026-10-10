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
	"github.com/doordash-oss/agentic-orchestrator/internal/agent/prompts"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// RolePlanPhaseReviser is the per-roadmap-phase plan revision session.
const RolePlanPhaseReviser Role = "plan_phase_reviser"

var phasePlanReviserRoleSpec = roleSpec{
	Phase:     feature.PhasePlan,
	Role:      RolePlanPhaseReviser,
	SkillName: "revise-phase-plan",
	OutputRoots: []outputRootSpec{
		artifactDirOutputRoot("Shared per-phase plan artifact root. Revisions update the phase plan markdown here across attempts."),
		attemptDirOutputRoot("Active phase-plan revision attempt directory. Debug prompts, attempt metadata, and validator output are written here; the harness records its completion receipt here after validation."),
	},
	Artifacts: []roleArtifactSpec{
		phasePlanMarkdownRoleArtifact(),
		planAttemptMetaRoleArtifact(),
	},
	ReadOnlyOutsideRoots: true,
}

// phasePlanRevisionUserInput is the data passed to phase_plan_revision.user.tmpl.
type phasePlanRevisionUserInput struct {
	Attempt int
	Phase   phasePlanView

	Feedback string

	PriorAxisApprovals prompts.PriorAxisApprovalsInput
	PhasePlanPath      string

	PhasePlanFormatPath string

	Inquireness prompts.AutonomousInquirenessInput

	AutomatedVerificationOnly bool
}
