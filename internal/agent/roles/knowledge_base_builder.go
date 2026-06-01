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

// RoleKnowledgeBaseBuilder is the per-repo KnowledgeBase builder session.
const RoleKnowledgeBaseBuilder Role = "knowledge_base_builder"

var knowledgeBaseBuilderRoleSpec = RoleSpec{
	Phase:        feature.PhaseKnowledgeBase,
	Role:         RoleKnowledgeBaseBuilder,
	SkillName:    "build-knowledge-base",
	UserTemplate: "kb_build.user",
	OutputRoots: []OutputRootSpec{
		singleShotPhaseDirOutputRoot("Repository-scoped knowledge-base root. The KB graph entrypoint is written here."),
		iterationDirOutputRoot("Active Knowledge Base loop iteration directory for harness handoff artifacts."),
	},
	MarkerRoot: "iteration_dir",
	Artifacts: []RoleArtifactSpec{
		{
			Name:         "knowledge_base_index",
			DisplayPath:  "index.md",
			RootName:     "phase_dir",
			RelativePath: "index.md",
			Presence:     ArtifactRequired,
			Description:  "top-level knowledge-base graph index markdown",
			Validate:     ValidatorKnowledgeBaseIndex,
		},
		kbProgressHandoffRoleArtifact(),
		knowledgeBaseIterationMetaRoleArtifact(),
	},
}

// KnowledgeBaseBuilderRoleSpec returns the RoleSpec-backed knowledge-base
// builder role.
func KnowledgeBaseBuilderRoleSpec() RoleSpec {
	return CloneRoleSpec(knowledgeBaseBuilderRoleSpec)
}

// KBBuildUserInput is the data passed to kb_build.user.tmpl.
type KBBuildUserInput struct {
	RepoName       string
	RepoPath       string
	KBRootDir      string
	KBIndexPath    string
	ExistingKBPath string
	LastCommit     string
}

// BuildKBBuildPrompt renders the knowledge-base builder prompt.
func BuildKBBuildPrompt(in KBBuildUserInput) string {
	return prompts.KBBuildUserPrompt(in)
}
