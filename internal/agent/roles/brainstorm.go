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
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// RoleBrainstormer is the legacy single-shot Design session identifier.
// New code should use RoleDesigner; this constant is kept so older callers,
// persisted sessions, and tests that still resolve the role by its legacy
// name continue to look up a valid contract.
const RoleBrainstormer Role = "brainstormer"

// brainstormerRoleSpec is the legacy alias RoleSpec for the pre-rename
// Brainstorm role. It validates the same phase-markdown artifact in the same
// on-disk subdirectory; only the Design-facing identity differs.
var brainstormerRoleSpec = RoleSpec{
	Phase:        feature.PhaseBrainstorm,
	Role:         RoleBrainstormer,
	SkillName:    "brainstorm",
	UserTemplate: "brainstorm.user",
	Required:     []feature.Phase{feature.PhaseResearch},
	OutputRoots: []OutputRootSpec{
		singleShotPhaseDirOutputRoot("Brainstorm phase artifact directory."),
	},
	MarkerRoot: "phase_dir",
	Artifacts: []RoleArtifactSpec{
		phaseMarkdownRoleArtifact("brainstorm markdown artifact"),
	},
}

// BrainstormerRoleSpec returns the legacy Brainstorm RoleSpec. New code
// should call DesignerRoleSpec instead; this remains as a compatibility
// wrapper so older lookups still resolve a valid contract.
func BrainstormerRoleSpec() RoleSpec {
	return CloneRoleSpec(brainstormerRoleSpec)
}

// BrainstormUserInput is the legacy data shape passed to brainstorm.user.tmpl.
// It mirrors DesignUserInput so the canonical builder can render the same
// effective prompt; new code should use DesignUserInput directly.
type BrainstormUserInput = DesignUserInput

// BuildBrainstormPrompt renders the legacy brainstorm user prompt. It
// delegates to BuildDesignPrompt so the two prompt surfaces stay in lockstep
// while the legacy template/file path is retained for compatibility tests
// that assert the older surface still exists.
func BuildBrainstormPrompt(in BrainstormUserInput) string {
	return BuildDesignPrompt(in)
}
