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

// RoleResearcher is the Research blocking-loop session role.
const RoleResearcher Role = "researcher"

var researcherRoleSpec = RoleSpec{
	Phase:        feature.PhaseResearch,
	Role:         RoleResearcher,
	SkillName:    "research-codebase",
	UserTemplate: "research_from_questions.user",
	Required:     []feature.Phase{feature.PhaseInquire},
	OutputRoots: []OutputRootSpec{
		artifactDirOutputRoot("Shared Research artifact root. research.md is written here and edited in place across iterations."),
		singleShotPhaseDirOutputRoot("Active Research iteration directory. research-progress.md, meta.yaml, and phase_complete are written here."),
	},
	MarkerRoot: "phase_dir",
	Artifacts: []RoleArtifactSpec{
		persistentMarkdownRoleArtifact("research_markdown_artifact", "research markdown artifact", "research.md"),
		researchProgressHandoffRoleArtifact(),
		iterationMetaRoleArtifact(),
	},
}

// ResearcherRoleSpec returns the RoleSpec-backed research role.
func ResearcherRoleSpec() RoleSpec {
	return CloneRoleSpec(researcherRoleSpec)
}

// ResearchFromQuestionsUserInput is the data passed to
// research_from_questions.user.tmpl.
type ResearchFromQuestionsUserInput struct {
	QuestionsPath string
	Repos         []prompts.RepoView
}

// BuildResearchFromQuestionsPrompt renders the question-driven research prompt.
func BuildResearchFromQuestionsPrompt(in ResearchFromQuestionsUserInput) string {
	return prompts.ResearchFromQuestionsUserPrompt(in)
}
