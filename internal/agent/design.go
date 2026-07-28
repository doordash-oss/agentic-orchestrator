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
	"github.com/doordash-oss/agentic-orchestrator/internal/agent/roles"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// BuildDesignPrompt constructs the user message for the Design phase. The
// agent receives research output + original ticket and produces a design doc.
// The prose lives in internal/agent/prompts/templates/design.user.tmpl.
//
// skillsDir, guidelinesDir, and kbInfos are retained for caller compatibility.
// The RoleSpec system prompt now owns primary skill discovery and Useful Resources.
func BuildDesignPrompt(f *feature.Feature, skillsDir, guidelinesDir, researchArtifactPath string, qaFilePaths []string, kbInfos ...KBInfo) string {
	_, _, _ = skillsDir, guidelinesDir, kbInfos
	repos, images, attachments := researchFeatureViews(f)

	return roles.BuildDesignPrompt(roles.DesignUserInput{
		Name:                 f.Name,
		Description:          f.EffectiveDescription(),
		Images:               images,
		Attachments:          attachments,
		Repos:                repos,
		MultiRepo:            len(repos) > 1,
		ResearchArtifactPath: researchArtifactPath,
		QAFiles: prompts.QAFilesInput{
			Paths: append([]string(nil), qaFilePaths...),
			Lead:  "Read these Q&A files for important context about their intent and preferences:",
		},
		Inquireness: prompts.GrillMeInquirenessInput{Level: string(f.Inquireness)},
	})
}

// BuildDesignRevisionPrompt constructs the autonomous revision prompt used
// after one or both Design critics request changes.
func BuildDesignRevisionPrompt(previousDesignPath, mockupManifestPath, criticFeedback string, attempt int) string {
	return roles.BuildDesignRevisionPrompt(roles.DesignRevisionUserInput{
		Attempt:            attempt,
		CriticFeedback:     criticFeedback,
		PreviousDesignPath: previousDesignPath,
		MockupManifestPath: mockupManifestPath,
		Inquireness:        prompts.AutonomousInquirenessInput{},
	})
}
