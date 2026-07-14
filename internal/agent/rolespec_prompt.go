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
	"fmt"
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
	Frontend      bool
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
	// RequiredSkillNames lists utility skills the role must read and apply,
	// rather than merely advertising them as optional resources.
	RequiredSkillNames []string
	// SuppressSubagents omits the sub-agent calling-convention clause. Set for
	// bounded helpers, which run with no configured sub-agents. Defaults false
	// so every other session keeps the clause.
	SuppressSubagents bool
}

// BuildImplementSystemPrompt renders the RoleSpec-backed system prompt for
// one implement iteration.
func BuildImplementSystemPrompt(in BuildImplementSystemPromptInput) string {
	requiredSkillNames := []string(nil)
	if in.Frontend {
		requiredSkillNames = []string{"frontend-design"}
	}
	return BuildRoleSystemPrompt(BuildRoleSystemPromptInput{
		Spec:               ImplementRoleSpec(),
		IterationDir:       in.IterationDir,
		SkillsDir:          in.SkillsDir,
		GuidelinesDir:      in.GuidelinesDir,
		KBInfos:            in.KBInfos,
		AskingClause:       in.AskingClause,
		RequiredSkillNames: requiredSkillNames,
	})
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

	requiredSkills := resolveSkillViews(in.RequiredSkillNames, in.SkillsDir)
	return prompts.RoleSystemPrompt(prompts.RoleSystemInput{
		OutputRoots:          rootViews,
		MarkerPath:           spec.MarkerPath(rt),
		SkillPath:            skillPath,
		RequiredSkills:       requiredSkills,
		ArtifactPreflight:    artifactPreflightCommand(spec, in.IterationDir),
		Preflight:            buildPreflightInput(spec.Phase, in.SkillsDir, in.KBInfos, in.GuidelinesDir, in.RequiredSkillNames...),
		ReadOnlyOutsideRoots: spec.ReadOnlyOutsideRoots,
		SubagentsAvailable:   !in.SuppressSubagents,
		AskingClause:         askingClause,
	})
}

func artifactPreflightCommand(spec RoleSpec, iterationDir string) string {
	if spec.NoOp || spec.Role == "" || iterationDir == "" {
		return ""
	}
	return fmt.Sprintf(`"$AGENTICO_BIN" validate-artifacts --phase %s --role %s --dir %q`,
		spec.Phase.DirName(),
		string(spec.Role),
		iterationDir,
	)
}
