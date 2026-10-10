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

// RoleInquirer is the single-shot Inquire session.
const RoleInquirer Role = "inquirer"

var inquirerRoleSpec = roleSpec{
	Phase:     feature.PhaseInquire,
	Role:      RoleInquirer,
	SkillName: "inquire",
	OutputRoots: []outputRootSpec{
		singleShotPhaseDirOutputRoot("Inquire phase artifact directory."),
	},
	Artifacts: []roleArtifactSpec{
		phaseMarkdownRoleArtifact("inquire markdown artifact", validateInquiryQuestionsArtifact),
	},
	ReadOnlyOutsideRoots: true,
}

// inquireUserInput is the data passed to inquire.user.tmpl.
type inquireUserInput struct {
	Name         string
	Description  string
	ExitCriteria string
	Images       []string
	Attachments  []string
	Repos        []prompts.RepoView

	Inquireness prompts.GrillMeInquirenessInput
}
