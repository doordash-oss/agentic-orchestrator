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

// RoleDesignReviser revises a Design artifact after automated critic feedback.
const RoleDesignReviser Role = "design_reviser"

var designReviserRoleSpec = RoleSpec{
	Phase:        feature.PhaseDesign,
	Role:         RoleDesignReviser,
	SkillName:    "revise-design",
	UserTemplate: "design_revision.user",
	Required:     []feature.Phase{feature.PhaseResearch},
	OutputRoots: []OutputRootSpec{
		artifactDirOutputRoot("Shared Design artifact root. Revisions update the design and optional mockup bundle here across attempts."),
		attemptDirOutputRoot("Active Design revision attempt directory. Debug prompts, attempt metadata, validator output, and phase_complete are written here."),
	},
	MarkerRoot: "attempt_dir",
	Artifacts: []RoleArtifactSpec{
		designMarkdownRoleArtifact(),
		designMockupManifestRoleArtifact(),
		planAttemptMetaRoleArtifact(),
	},
	ReadOnlyOutsideRoots: true,
}

// DesignReviserRoleSpec returns the Design revision RoleSpec.
func DesignReviserRoleSpec() RoleSpec {
	return CloneRoleSpec(designReviserRoleSpec)
}

// DesignRevisionUserInput is the data passed to design_revision.user.tmpl.
type DesignRevisionUserInput struct {
	Attempt            int
	CriticFeedback     string
	PreviousDesignPath string
	MockupManifestPath string
	Inquireness        prompts.AutonomousInquirenessInput
}

// BuildDesignRevisionPrompt renders the Design revision prompt.
func BuildDesignRevisionPrompt(in DesignRevisionUserInput) string {
	return prompts.DesignRevisionUserPrompt(in)
}
