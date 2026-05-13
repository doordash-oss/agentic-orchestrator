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

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
)

// RepoSuggestionResult holds the output of a repo suggestion attempt.
type RepoSuggestionResult struct {
	Suggested []string // repos suggested by CNB classifier
	AtMention []string // repos matched via @-mention paths
	Err       error    // non-nil if suggestion failed (AtMention may still be valid)
}

// RepoSuggester uses the trained CNB classifier to suggest repos.
type RepoSuggester struct {
	repos      map[string]config.RepoConfig
	classifier *ClassifierIndex
}

// NewRepoSuggester creates a suggester backed by the CNB classifier.
// If classifier is nil, only @-mention suggestions are returned.
func NewRepoSuggester(repos map[string]config.RepoConfig, classifier *ClassifierIndex) *RepoSuggester {
	return &RepoSuggester{
		repos:      repos,
		classifier: classifier,
	}
}

// UpdateRepos replaces the internal repo map so the suggester uses fresh
// discovery results after workspace roots change.
func (s *RepoSuggester) UpdateRepos(repos map[string]config.RepoConfig) {
	s.repos = repos
}

// Suggest analyzes the feature name+description and returns suggested repo names.
// It combines two tiers:
// 1. Deterministic @-mention extraction from the description (instant)
// 2. CNB classifier prediction (instant)
func (s *RepoSuggester) Suggest(name, description string, repoPaths map[string]string) RepoSuggestionResult {
	result := RepoSuggestionResult{}

	// Tier 1: Extract @-mention repos deterministically
	result.AtMention = ExtractAtMentionRepos(description, repoPaths)

	// Skip further analysis if 0-1 repos or no classifier
	if len(s.repos) <= 1 || s.classifier == nil {
		return result
	}

	// Tier 2: CNB classifier prediction
	suggested := s.classifier.Predict(name, description, s.repos)
	result.Suggested = filterValidRepos(suggested, s.repos)
	return result
}

// ExtractAtMentionRepos scans the description for @/path/... patterns and
// matches them against known repo paths. Returns deduplicated repo names.
func ExtractAtMentionRepos(description string, repoPaths map[string]string) []string {
	if description == "" || len(repoPaths) == 0 {
		return nil
	}

	seen := make(map[string]bool)
	var matched []string

	// Find all @/ patterns in the description
	for i := 0; i < len(description)-1; i++ {
		if description[i] != '@' {
			continue
		}
		// Extract the path after @
		rest := description[i+1:]
		end := strings.IndexAny(rest, " \t\n\r")
		if end < 0 {
			end = len(rest)
		}
		path := rest[:end]
		if path == "" {
			continue
		}

		// Match against repo paths
		for name, repoPath := range repoPaths {
			if repoPath != "" && strings.HasPrefix(path, repoPath) &&
				(len(path) == len(repoPath) || path[len(repoPath)] == '/') &&
				!seen[name] {
				seen[name] = true
				matched = append(matched, name)
			}
		}

		// Match the most specific repo name that is a prefix of the path.
		// For overlapping names like "rootA" and "rootA/myrepo", only the
		// longest match is used so that @rootA/myrepo/file.go resolves to
		// "rootA/myrepo" and not both repos.
		bestName := ""
		for name := range repoPaths {
			if strings.HasPrefix(path, name) &&
				(len(path) == len(name) || path[len(name)] == '/') &&
				len(name) > len(bestName) {
				bestName = name
			}
		}
		if bestName != "" && !seen[bestName] {
			seen[bestName] = true
			matched = append(matched, bestName)
		}
	}

	return matched
}

// filterValidRepos returns only repo names that exist in the valid set.
func filterValidRepos(suggested []string, valid map[string]config.RepoConfig) []string {
	var filtered []string
	for _, name := range suggested {
		if _, ok := valid[name]; ok {
			filtered = append(filtered, name)
		}
	}
	return filtered
}
