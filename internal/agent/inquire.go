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

// BuildInquirePrompt constructs the user message for the Inquire phase.
//
// The prose lives in internal/agent/prompts/templates/inquire.user.tmpl
// (and the partials it references). This function is a thin adapter that
// projects *feature.Feature into the typed input the template expects and
// invokes the renderer.
//
// skillsDir and kbInfos are retained for caller compatibility. The RoleSpec
// system prompt now owns primary skill discovery and Useful Resources.
func BuildInquirePrompt(f *feature.Feature, skillsDir string, kbInfos ...KBInfo) string {
	repos := make([]prompts.RepoView, 0, len(f.Repos))
	for _, r := range f.Repos {
		path := r.Path
		if r.WorktreePath != "" {
			path = r.WorktreePath
		}
		repos = append(repos, prompts.RepoView{Name: r.Name, Path: path})
	}

	in := roles.InquireUserInput{
		Name:         f.Name,
		Description:  f.Description,
		ExitCriteria: f.ExitCriteria,
		Images:       append([]string(nil), f.Images...),
		Attachments:  append([]string(nil), f.Attachments...),
		Repos:        repos,
		Inquireness:  prompts.GrillMeInquirenessInput{Level: string(f.Inquireness)},
	}

	return roles.BuildInquirePrompt(in)
}
