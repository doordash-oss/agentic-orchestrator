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
	"path/filepath"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent/prompts"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// RoleImplementer is the phase implementer session.
const RoleImplementer Role = "implementer"

var implementRoleSpec = RoleSpec{
	Phase:        feature.PhaseImplement,
	Role:         RoleImplementer,
	SkillName:    "implement",
	UserTemplate: "implement.user",
	Required:     []feature.Phase{feature.PhasePlan},
	OutputRoots: []OutputRootSpec{
		{
			Name:        "phase_dir",
			Description: "Phase-level implement artifact root shared across iterations.",
			ResolvePath: func(rt RoleRuntime) string {
				return filepath.Dir(rt.IterationDir)
			},
		},
		{
			Name:        "iteration_dir",
			Description: "Active iteration artifact directory.",
			ResolvePath: func(rt RoleRuntime) string {
				return rt.IterationDir
			},
		},
	},
	MarkerRoot: "iteration_dir",
	Artifacts: []RoleArtifactSpec{
		{
			Name:         "progress",
			DisplayPath:  "progress.md",
			RootName:     "phase_dir",
			RelativePath: "progress.md",
			Presence:     ArtifactRequired,
			Description:  "structured progress markdown with iteration handoff, deferrals, and iteration state",
			Validate:     ValidatorProgress,
		},
		{
			Name:         "need_user_input",
			DisplayPath:  "need-user-input.yaml",
			RootName:     "iteration_dir",
			RelativePath: "need-user-input.yaml",
			Presence:     ArtifactConditional,
			Condition:    "required when progress.md reports NEED_USER_INPUT",
			Description:  "YAML gate file containing the structured user questions needed before the next iteration",
			When:         ConditionProgressNeedUserInput,
			Validate:     ValidatorNeedUserInput,
		},
	},
}

// ImplementRoleSpec returns the RoleSpec-backed implement role.
func ImplementRoleSpec() RoleSpec {
	return CloneRoleSpec(implementRoleSpec)
}

// ImplementUserInput is the data passed to implement.user.tmpl.
type ImplementUserInput struct {
	VisualReferences      prompts.VisualReferencesInput
	PlanPath              string
	ExitCriteria          string
	Feedback              string
	PlanRevisionFeedback  string
	HelpAnswers           string
	PriorUserInputAnswers string
	Iteration             int
}

// BuildImplementPrompt renders the implement user prompt.
func BuildImplementPrompt(in ImplementUserInput) string {
	return prompts.ImplementUserPrompt(in)
}
