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
	"path/filepath"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// RoleImplementer is the phase implementer session.
const RoleImplementer Role = "implementer"

var implementRoleSpec = roleSpec{
	Phase:     feature.PhaseImplement,
	Role:      RoleImplementer,
	SkillName: "implement",
	OutputRoots: []outputRootSpec{
		{
			Name:        "phase_dir",
			Description: "Phase-level implement artifact root shared across iterations.",
			ResolvePath: func(rt roleRuntime) string {
				return filepath.Dir(rt.IterationDir)
			},
		},
		{
			Name:        "iteration_dir",
			Description: "Active iteration artifact directory.",
			ResolvePath: func(rt roleRuntime) string {
				return rt.IterationDir
			},
		},
	},
	Artifacts: []roleArtifactSpec{
		{
			Name:         "progress",
			DisplayPath:  "progress.md",
			RootName:     "phase_dir",
			RelativePath: "progress.md",
			Description:  "structured progress markdown with iteration handoff, deferrals, and iteration state",
			Validate:     validateProgressArtifact,
		},
	},
	IterationState: true,
}

// implementUserInput is the data passed to implement.user.tmpl.
type implementUserInput struct {
	PlanPath             string
	ExitCriteria         string
	Feedback             string
	PlanRevisionFeedback string
	HelpAnswers          string
	Iteration            int
}
