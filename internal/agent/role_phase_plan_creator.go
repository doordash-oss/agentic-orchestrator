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

// RolePlanPhasePlanner is the per-roadmap-phase planner session.
const RolePlanPhasePlanner Role = "plan_phase_planner"

var phasePlanCreatorRoleSpec = roleSpec{
	Phase:     feature.PhasePlan,
	Role:      RolePlanPhasePlanner,
	SkillName: "plan-phase",
	OutputRoots: []outputRootSpec{
		artifactDirOutputRoot("Shared per-phase plan artifact root. The phase plan markdown is written here across attempts."),
		attemptDirOutputRoot("Active phase-plan attempt directory. Debug prompts, attempt metadata, and validator output are written here; the harness records its completion receipt here after validation."),
	},
	Artifacts: []roleArtifactSpec{
		phasePlanMarkdownRoleArtifact(),
		planAttemptMetaRoleArtifact(),
	},
	ReadOnlyOutsideRoots: true,
}

// phasePlanView projects a roadmap phase for phase-plan prompts.
type phasePlanView struct {
	Number        int
	Name          string
	Type          string
	Goal          string
	StubsToRetire []string
}

// phasePlanUserInput is the data passed to phase_plan.user.tmpl.
type phasePlanUserInput struct {
	Phase                phasePlanView
	RoadmapPath          string
	ResearchArtifactPath string

	QAFiles prompts.QAFilesInput

	Inquireness prompts.GrillMeInquirenessInput

	AutomatedVerificationOnly bool
}
