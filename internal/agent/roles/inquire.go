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

// RoleInquirer is the single-shot Inquire session.
const RoleInquirer Role = "inquirer"

var inquirerRoleSpec = RoleSpec{
	Phase:        feature.PhaseInquire,
	Role:         RoleInquirer,
	SkillName:    "inquire",
	UserTemplate: "inquire.user",
	OutputRoots: []OutputRootSpec{
		singleShotPhaseDirOutputRoot("Inquire phase artifact directory."),
	},
	MarkerRoot: "phase_dir",
	Artifacts: []RoleArtifactSpec{
		phaseMarkdownRoleArtifact("inquire markdown artifact"),
	},
}

// InquirerRoleSpec returns the RoleSpec-backed inquire role.
func InquirerRoleSpec() RoleSpec {
	return CloneRoleSpec(inquirerRoleSpec)
}

// InquireUserInput is the data passed to inquire.user.tmpl.
type InquireUserInput struct {
	Name        string
	Description string
	Images      []string
	Attachments []string
	Repos       []prompts.RepoView

	Inquireness prompts.GrillMeInquirenessInput
}

// BuildInquirePrompt renders the inquirer user prompt.
func BuildInquirePrompt(in InquireUserInput) string {
	return prompts.InquireUserPrompt(in)
}
