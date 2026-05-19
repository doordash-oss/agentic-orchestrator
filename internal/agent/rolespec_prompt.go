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
	"path/filepath"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent/prompts"
)

// BuildImplementSystemPromptInput carries runtime values for the
// RoleSpec-backed implement system prompt.
type BuildImplementSystemPromptInput struct {
	IterationDir  string
	SkillsDir     string
	GuidelinesDir string
	KBInfos       []KBInfo
	AskingClause  string
}

// BuildRoleSystemPromptInput carries runtime values for a RoleSpec-backed
// system prompt.
type BuildRoleSystemPromptInput struct {
	Spec          RoleSpec
	IterationDir  string
	SkillsDir     string
	GuidelinesDir string
	KBInfos       []KBInfo
	Model         string
	AskingClause  string
}

// BuildImplementSystemPrompt renders the RoleSpec-backed system prompt for
// one implement iteration.
func BuildImplementSystemPrompt(in BuildImplementSystemPromptInput) string {
	return BuildRoleSystemPrompt(BuildRoleSystemPromptInput{
		Spec:          ImplementRoleSpec(),
		IterationDir:  in.IterationDir,
		SkillsDir:     in.SkillsDir,
		GuidelinesDir: in.GuidelinesDir,
		KBInfos:       in.KBInfos,
		AskingClause:  in.AskingClause,
	})
}

// ApplyRoleSandbox declares the role's sandbox policy on the
// BuildSessionOpts. For roles that mark themselves ReadOnlyOutsideRoots
// it sets:
//
//   - WritableRoots: every resolved output root path (the only locations
//     the agent should be able to write to).
//   - ReadOnlyOutsideRoots: forwarded to the LLM provider so it can apply
//     provider-specific enforcement (Claude's --disallowedTools list,
//     codex's sandbox cwd resolution).
//
// This is a no-op for roles whose contract permits writing outside their
// declared output roots (Implement, validators, refactor, KB-builder,
// review), so call sites can invoke it unconditionally before handing the
// opts to BuildSession.
func ApplyRoleSandbox(opts *BuildSessionOpts, spec RoleSpec, iterDir string) {
	if opts == nil || !spec.ReadOnlyOutsideRoots {
		return
	}
	rt := RoleRuntime{IterationDir: iterDir}
	paths := spec.OutputRootPaths(rt)
	roots := make([]string, 0, len(spec.OutputRoots))
	seen := make(map[string]bool, len(spec.OutputRoots))
	for _, root := range spec.OutputRoots {
		p := paths[root.Name]
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		roots = append(roots, p)
	}
	opts.WritableRoots = roots
	opts.ReadOnlyOutsideRoots = true
}

// BuildRoleSystemPrompt renders the generic RoleSpec-backed system prompt.
func BuildRoleSystemPrompt(in BuildRoleSystemPromptInput) string {
	spec := in.Spec
	rt := RoleRuntime{IterationDir: in.IterationDir}
	roots := spec.OutputRootPaths(rt)
	rootViews := make([]prompts.OutputRootView, 0, len(spec.OutputRoots))
	for _, root := range spec.OutputRoots {
		rootViews = append(rootViews, prompts.OutputRootView{
			Name:        root.Name,
			Path:        roots[root.Name],
			Description: root.Description,
		})
	}

	skillPath := ""
	if in.SkillsDir != "" && spec.SkillName != "" {
		skillPath = filepath.Join(in.SkillsDir, spec.SkillName, "SKILL.md")
	}

	askingClause := in.AskingClause
	if askingClause == "" && spec.AskingClauseFor != nil {
		askingClause = spec.AskingClauseFor(in.Model)
	}

	return prompts.RoleSystemPrompt(prompts.RoleSystemInput{
		OutputRoots:  rootViews,
		MarkerPath:   spec.MarkerPath(rt),
		SkillPath:    skillPath,
		Preflight:    buildPreflightInput(spec.Phase, in.SkillsDir, in.KBInfos, in.GuidelinesDir),
		AskingClause: askingClause,
	})
}
