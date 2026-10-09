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
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent/prompts"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// researchFeatureViews projects a feature's repos, images, and attachments
// into the typed views the research/inquire/design templates expect.
// Returned slices are independent copies so callers can mutate them safely.
func researchFeatureViews(f *feature.Feature) (repos []prompts.RepoView, images []string, attachments []string) {
	repos = make([]prompts.RepoView, 0, len(f.Repos))
	for _, r := range f.Repos {
		path := r.Path
		if r.WorktreePath != "" {
			path = r.WorktreePath
		}
		repos = append(repos, prompts.RepoView{Name: r.Name, Path: path})
	}
	images = append([]string(nil), f.Images...)
	attachments = append([]string(nil), f.Attachments...)
	return
}

// BuildResearchFromQuestionsPrompt constructs the research prompt using
// questions from the Inquire phase. The questions file is the only research
// driver — the feature description is intentionally not surfaced here so
// ticket intent does not leak into research framing.
//
// skillsDir and kbInfos are retained for caller compatibility. The RoleSpec
// system prompt now owns primary skill discovery and Useful Resources.
//
// The prose lives in
// internal/agent/prompts/templates/research_from_questions.user.tmpl.
func BuildResearchFromQuestionsPrompt(f *feature.Feature, skillsDir, questionsPath string, kbInfos ...KBInfo) string {
	repos, _, _ := researchFeatureViews(f)
	return prompts.ResearchFromQuestionsUserPrompt(researchFromQuestionsUserInput{
		QuestionsPath: questionsPath,
		Repos:         repos,
	})
}

// collectEnvExcludes gathers env var prefixes to strip from all providers.
func collectEnvExcludes(providers []llm.LLMProvider) []string {
	seen := make(map[string]bool)
	var result []string
	for _, p := range providers {
		for _, v := range p.EnvVarsToExclude() {
			if !seen[v] {
				seen[v] = true
				result = append(result, v)
			}
		}
	}
	return result
}

// filterEnvMulti returns a copy of env without entries matching any of the given key prefixes.
func filterEnvMulti(env []string, excludes []string) []string {
	if len(excludes) == 0 {
		return env
	}
	result := make([]string, 0, len(env))
	for _, e := range env {
		excluded := false
		for _, ex := range excludes {
			if strings.HasPrefix(e, ex+"=") {
				excluded = true
				break
			}
		}
		if !excluded {
			result = append(result, e)
		}
	}
	return result
}
