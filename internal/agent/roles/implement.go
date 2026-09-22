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
		// The fix manifest is optional: review-feedback children of a
		// PR-stack parent use it to route changed files to the parent
		// layer that owns them. Every other feature never writes it.
		{
			Name:         "fix_manifest",
			DisplayPath:  "fix-manifest.yaml",
			RootName:     "iteration_dir",
			RelativePath: "fix-manifest.yaml",
			Presence:     ArtifactOptional,
			Description:  "optional manifest assigning this iteration's changed files to their owning parent stack layers",
			Validate:     ValidatorFixManifest,
		},
	},
}

// ImplementRoleSpec returns the RoleSpec-backed implement role.
func ImplementRoleSpec() RoleSpec {
	return CloneRoleSpec(implementRoleSpec)
}

// ImplementUserInput is the data passed to implement.user.tmpl.
type ImplementUserInput struct {
	PlanPath             string
	ExitCriteria         string
	Feedback             string
	PlanRevisionFeedback string
	HelpAnswers          string
	Iteration            int
	// Stack is the parent feature's PR-stack layers, populated only for a
	// review-feedback child whose parent delivers as a stack; empty for
	// top-level features and other child kinds.
	Stack []feature.StackLayer
	// FixManifestPath is the resolved artifact path of the optional
	// fix-manifest.yaml, shown to the implementer alongside the parent
	// stack layers.
	FixManifestPath string
}

// BuildImplementPrompt renders the implement user prompt.
func BuildImplementPrompt(in ImplementUserInput) string {
	return prompts.ImplementUserPrompt(in)
}
