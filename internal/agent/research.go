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
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent/prompts"
	"github.com/doordash-oss/agentic-orchestrator/internal/agent/roles"
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
	return roles.BuildResearchFromQuestionsPrompt(roles.ResearchFromQuestionsUserInput{
		QuestionsPath: questionsPath,
		Repos:         repos,
	})
}

// preReadFiles reads the content of the given files, truncating each to
// maxLines and stopping once maxTotalChars is reached across all files.
func preReadFiles(repoPath string, files []FileSummary, maxLines, maxTotalChars int) map[string]string {
	result := make(map[string]string)
	totalChars := 0

	for _, fs := range files {
		if totalChars >= maxTotalChars {
			break
		}
		absPath := filepath.Join(repoPath, fs.Path)
		f, err := os.Open(absPath)
		if err != nil {
			continue
		}

		var lines []string
		scanner := bufio.NewScanner(f)
		lineCount := 0
		for scanner.Scan() {
			if lineCount >= maxLines {
				lines = append(lines, fmt.Sprintf("... (%d more lines)", fs.LineCount-lineCount))
				break
			}
			lines = append(lines, scanner.Text())
			lineCount++
		}
		_ = f.Close()

		content := strings.Join(lines, "\n")
		if totalChars+len(content) > maxTotalChars {
			remaining := maxTotalChars - totalChars
			if remaining > 200 {
				content = content[:remaining] + "\n... (truncated)"
			} else {
				continue
			}
		}

		result[fs.Path] = content
		totalChars += len(content)
	}

	return result
}

// RunParallelScouts splits the research query across multiple concurrent Claude
// sessions ("scouts"), each analyzing a cluster of pre-read files. Scout findings
// are concatenated and returned for use in a synthesis step.
func RunParallelScouts(ctx context.Context, query string, index *CodebaseIndex, repoPath string, maxScouts int, runHelper ScoutHelperRunner) string {
	if index == nil || maxScouts <= 0 {
		return ""
	}

	// Fetch more files than single-session (distributed across scouts).
	relevant := FindRelevantFiles(index, query, 12)
	if len(relevant) == 0 {
		return ""
	}

	clusters := clusterFilesByDir(relevant, maxScouts)

	results := make([]string, len(clusters))
	var wg sync.WaitGroup

	for i, cluster := range clusters {
		wg.Add(1)
		go func(idx int, files []FileSummary) {
			defer wg.Done()
			prompt := buildScoutPrompt(query, files, repoPath)
			text, err := runScoutExec(ctx, prompt, runHelper)
			if err != nil {
				results[idx] = fmt.Sprintf("Scout %d error: %v", idx+1, err)
				return
			}
			results[idx] = fmt.Sprintf("## Scout %d Findings\n\n%s", idx+1, text)
		}(i, cluster)
	}

	wg.Wait()

	var b strings.Builder
	for _, r := range results {
		if r != "" {
			b.WriteString(r)
			b.WriteString("\n\n")
		}
	}
	return b.String()
}

// ScoutHelperRunner runs a single bounded scout helper invocation.
type ScoutHelperRunner func(ctx context.Context, prompt string) (*BoundedHelperResult, error)

// clusterFilesByDir groups files by their first directory component and
// distributes the groups round-robin into n buckets. Empty clusters are removed.
func clusterFilesByDir(files []FileSummary, n int) [][]FileSummary {
	if n <= 0 {
		n = 1
	}

	// Group by first path component.
	groups := make(map[string][]FileSummary)
	var order []string
	for _, f := range files {
		key := strings.SplitN(f.Path, "/", 2)[0]
		if _, seen := groups[key]; !seen {
			order = append(order, key)
		}
		groups[key] = append(groups[key], f)
	}

	// Distribute groups round-robin.
	buckets := make([][]FileSummary, n)
	for i, key := range order {
		buckets[i%n] = append(buckets[i%n], groups[key]...)
	}

	// Remove empty clusters.
	var result [][]FileSummary
	for _, b := range buckets {
		if len(b) > 0 {
			result = append(result, b)
		}
	}
	return result
}

// buildScoutPrompt constructs a focused prompt for a single scout session.
//
// The prose lives in internal/agent/prompts/templates/scout.user.tmpl.
func buildScoutPrompt(query string, files []FileSummary, repoPath string) string {
	contents := preReadFiles(repoPath, files, 200, 15000)
	scoutFiles := make([]prompts.ScoutFile, 0, len(files))
	for _, fs := range files {
		content, ok := contents[fs.Path]
		if !ok {
			continue
		}
		scoutFiles = append(scoutFiles, prompts.ScoutFile{
			Path:    fs.Path,
			Purpose: fs.Purpose,
			Content: content,
		})
	}
	return prompts.ScoutUserPrompt(prompts.ScoutUserInput{
		Query: query,
		Files: scoutFiles,
	})
}

// runScoutExec runs a single scout session through the bounded helper path.
func runScoutExec(ctx context.Context, prompt string, runHelper ScoutHelperRunner) (string, error) {
	if runHelper == nil {
		return "", fmt.Errorf("running scout helper: missing helper runner")
	}

	result, err := runHelper(ctx, prompt)
	if err != nil {
		return "", err
	}

	return result.Output, nil
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
