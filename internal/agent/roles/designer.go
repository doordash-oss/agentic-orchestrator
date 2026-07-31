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

// RoleDesigner is the canonical single-shot Design session. Legacy callers
// that still spell the role as "designer" resolve through
// RoleDesigner; both roles share the same artifact validation behavior.
const RoleDesigner Role = "designer"

var designerRoleSpec = RoleSpec{
	Phase:        feature.PhaseDesign,
	Role:         RoleDesigner,
	SkillName:    "design",
	UserTemplate: "design.user",
	Required:     []feature.Phase{feature.PhaseResearch},
	OutputRoots: []OutputRootSpec{
		artifactDirOutputRoot("Shared Design artifact root. The approved design and optional mockup bundle are updated here across attempts."),
		attemptDirOutputRoot("Active Design attempt directory. Debug prompts, attempt metadata, validator output, and phase_complete are written here."),
	},
	MarkerRoot: "attempt_dir",
	Artifacts: []RoleArtifactSpec{
		designMarkdownRoleArtifact(),
		designMockupManifestRoleArtifact(),
		planAttemptMetaRoleArtifact(),
	},
	ReadOnlyOutsideRoots: true,
}

// DesignerRoleSpec returns the canonical Design RoleSpec.
func DesignerRoleSpec() RoleSpec {
	return CloneRoleSpec(designerRoleSpec)
}

// DesignUserInput is the data passed to design.user.tmpl. Shape matches
// DesignUserInput so callers can migrate without churn; the legacy
// builder delegates here.
type DesignUserInput struct {
	Name        string
	Description string
	Images      []string
	Attachments []string
	Repos       []prompts.RepoView

	MultiRepo            bool
	ResearchArtifactPath string
	DecisionLedgerPath   string

	QAFiles     prompts.QAFilesInput
	Inquireness prompts.GrillMeInquirenessInput
}

// BuildDesignPrompt renders the canonical Design user prompt.
func BuildDesignPrompt(in DesignUserInput) string {
	return prompts.DesignUserPrompt(in)
}
