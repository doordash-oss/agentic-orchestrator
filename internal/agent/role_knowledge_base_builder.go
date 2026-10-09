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

import "github.com/doordash-oss/agentic-orchestrator/internal/feature"

// RoleKnowledgeBaseBuilder is the per-repo KnowledgeBase builder session.
const RoleKnowledgeBaseBuilder Role = "knowledge_base_builder"

var knowledgeBaseBuilderRoleSpec = roleSpec{
	Phase:     feature.PhaseKnowledgeBase,
	Role:      RoleKnowledgeBaseBuilder,
	SkillName: "build-knowledge-base",
	OutputRoots: []outputRootSpec{
		singleShotPhaseDirOutputRoot("Repository-scoped knowledge-base root. The KB graph entrypoint is written here."),
	},
	Artifacts: []roleArtifactSpec{
		{
			Name:         "knowledge_base_index",
			DisplayPath:  "index.md",
			RootName:     "phase_dir",
			RelativePath: "index.md",
			Description:  "top-level knowledge-base graph index markdown",
			Validate:     validateKnowledgeBaseIndexArtifact,
		},
	},
}

// kbBuildUserInput is the data passed to kb_build.user.tmpl.
type kbBuildUserInput struct {
	RepoName       string
	RepoPath       string
	KBRootDir      string
	KBIndexPath    string
	ExistingKBPath string
	LastCommit     string
}
