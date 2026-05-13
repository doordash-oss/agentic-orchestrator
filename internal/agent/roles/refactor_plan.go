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

// RoleRefactorPlanStep is the planning session that writes a refactor-plan.md
// before the refactor implementation loop starts.
const RoleRefactorPlanStep Role = "refactor_plan_step"

var refactorPlanRoleSpec = RoleSpec{
	Phase:        feature.PhasePlan,
	Role:         RoleRefactorPlanStep,
	SkillName:    "refactor",
	UserTemplate: "refactor_plan.user",
	OutputRoots: []OutputRootSpec{
		singleShotPhaseDirOutputRoot("Flat refactor cycle artifact directory."),
	},
	MarkerRoot: "phase_dir",
	Artifacts: []RoleArtifactSpec{
		{
			Name:         "refactor_plan_markdown",
			DisplayPath:  "refactor-plan.md",
			RootName:     "phase_dir",
			RelativePath: "refactor-plan.md",
			Presence:     ArtifactRequired,
			Description:  "refactor plan markdown with phase-plan-style tasks and per-repo tags",
			ResolvePath: func(rt RoleRuntime, _ RoleArtifactSpec) string {
				if path := newestPlanMarkdownArtifact(rt.IterationDir); path != "" {
					return path
				}
				return filepath.Join(rt.IterationDir, "refactor-plan.md")
			},
			Validate: ValidatorRefactorPlanMarkdown,
		},
	},
}

// RefactorPlanRoleSpec returns the RoleSpec-backed refactor-plan role.
func RefactorPlanRoleSpec() RoleSpec {
	return CloneRoleSpec(refactorPlanRoleSpec)
}

// RefactorPlanUserInput is the data passed to refactor_plan.user.tmpl.
type RefactorPlanUserInput struct {
	Request        string
	FeatureContext string
	Repos          []prompts.RepoView
}

// BuildRefactorPlanPrompt renders the refactor-plan prompt.
func BuildRefactorPlanPrompt(in RefactorPlanUserInput) string {
	return prompts.RefactorPlanUserPrompt(in)
}
