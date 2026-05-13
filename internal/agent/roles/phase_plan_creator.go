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

// RolePlanPhasePlanner is the per-roadmap-phase planner session.
const RolePlanPhasePlanner Role = "plan_phase_planner"

var phasePlanCreatorRoleSpec = RoleSpec{
	Phase:        feature.PhasePlan,
	Role:         RolePlanPhasePlanner,
	SkillName:    "plan-phase",
	UserTemplate: "phase_plan.user",
	OutputRoots: []OutputRootSpec{
		artifactDirOutputRoot("Shared per-phase plan artifact root. The phase plan markdown is written here across attempts."),
		attemptDirOutputRoot("Active phase-plan attempt directory. Debug prompts, attempt metadata, validator output, and phase_complete are written here."),
	},
	MarkerRoot: "attempt_dir",
	Artifacts: []RoleArtifactSpec{
		phasePlanMarkdownRoleArtifact(),
		planAttemptMetaRoleArtifact(),
	},
}

// PhasePlanCreatorRoleSpec returns the RoleSpec-backed phase-plan creation role.
func PhasePlanCreatorRoleSpec() RoleSpec {
	return CloneRoleSpec(phasePlanCreatorRoleSpec)
}

// PhasePlanView projects a roadmap phase for phase-plan prompts.
type PhasePlanView struct {
	Number        int
	Name          string
	Type          string
	Goal          string
	StubsToRetire []string
}

// PhasePlanUserInput is the data passed to phase_plan.user.tmpl.
type PhasePlanUserInput struct {
	Phase       PhasePlanView
	RoadmapPath string

	QAFiles prompts.QAFilesInput

	Inquireness prompts.GrillMeInquirenessInput
}

// BuildPhasePlanPrompt renders the phase-plan creation prompt.
func BuildPhasePlanPrompt(in PhasePlanUserInput) string {
	return prompts.PhasePlanUserPrompt(in)
}
