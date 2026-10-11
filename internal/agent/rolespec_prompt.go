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

// implementSystemPromptInput carries runtime values for the
// RoleSpec-backed implement system prompt.
type implementSystemPromptInput struct {
	IterationDir   string
	SkillsDir      string
	GuidelinesDir  string
	KBInfos        []KBInfo
	AskingClause   string
	CompletionTool string
	Frontend       bool
}

// roleSystemPromptInput carries runtime values for a RoleSpec-backed
// system prompt.
type roleSystemPromptInput struct {
	Spec           roleSpec
	IterationDir   string
	SkillsDir      string
	GuidelinesDir  string
	KBInfos        []KBInfo
	AskingClause   string
	CompletionTool string
	// RequiredSkillNames lists utility skills the role must read and apply,
	// rather than merely advertising them as optional resources.
	RequiredSkillNames []string
	// RequiredSkillConditions optionally scopes a required skill's mandate
	// (keyed by skill name): the skill is mandatory only when the clause
	// applies to the iteration's work.
	RequiredSkillConditions map[string]string
	// SuppressSubagents omits the sub-agent calling-convention clause. Set for
	// bounded helpers, which run with no configured sub-agents. Defaults false
	// so every other session keeps the clause.
	SuppressSubagents bool
}

// buildImplementSystemPrompt renders the RoleSpec-backed system prompt for
// one implement iteration.
func buildImplementSystemPrompt(in implementSystemPromptInput) string {
	requiredSkillNames := []string(nil)
	var requiredSkillConditions map[string]string
	if in.Frontend {
		requiredSkillNames = []string{"frontend-design"}
		// Later iterations are often mechanical fix-up loops where the skill
		// adds no value; scope the read to visual work and let the design
		// review axis backstop misapplication.
		requiredSkillConditions = map[string]string{
			"frontend-design": "this iteration creates new UI or visually reshapes existing UI (components, layout, styling, motion); skip for mechanical fixes, test repair, refactors, or other non-visual changes",
		}
	}
	return buildRoleSystemPrompt(roleSystemPromptInput{
		Spec:                    implementRoleSpec,
		IterationDir:            in.IterationDir,
		SkillsDir:               in.SkillsDir,
		GuidelinesDir:           in.GuidelinesDir,
		KBInfos:                 in.KBInfos,
		AskingClause:            in.AskingClause,
		CompletionTool:          in.CompletionTool,
		RequiredSkillNames:      requiredSkillNames,
		RequiredSkillConditions: requiredSkillConditions,
	})
}

// buildRoleSystemPrompt renders the generic RoleSpec-backed system prompt.
func buildRoleSystemPrompt(in roleSystemPromptInput) string {
	spec := in.Spec
	rt := roleRuntime{IterationDir: in.IterationDir}
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

	requiredSkills := resolveSkillViews(in.RequiredSkillNames, in.SkillsDir)
	for i := range requiredSkills {
		requiredSkills[i].Condition = in.RequiredSkillConditions[requiredSkills[i].Name]
	}
	return prompts.RoleSystemPrompt(prompts.RoleSystemInput{
		OutputRoots:          rootViews,
		SkillPath:            skillPath,
		RequiredSkills:       requiredSkills,
		ArtifactPreflight:    artifactPreflightCommand(spec, in.IterationDir),
		Preflight:            buildPreflightInput(spec.Phase, in.SkillsDir, in.KBInfos, in.GuidelinesDir, in.RequiredSkillNames...),
		ReadOnlyOutsideRoots: spec.ReadOnlyOutsideRoots,
		SubagentsAvailable:   !in.SuppressSubagents,
		RetryOutcomeAllowed:  spec.IterationState,
		AskingClause:         in.AskingClause,
		CompletionTool:       in.CompletionTool,
	})
}

func artifactPreflightCommand(spec roleSpec, iterationDir string) string {
	if spec.Role == "" || iterationDir == "" {
		return ""
	}
	return fmt.Sprintf(`"$AGENTICO_BIN" validate-artifacts --phase %s --role %s --dir %q`,
		spec.Phase.DirName(),
		string(spec.Role),
		iterationDir,
	)
}
