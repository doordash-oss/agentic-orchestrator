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

package permission

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// ClaudeSettings is the structure of a Claude CLI settings file.
type ClaudeSettings struct {
	Permissions struct {
		Allow []string `json:"allow"`
		Deny  []string `json:"deny"`
	} `json:"permissions"`
}

// ImportRepoSettings reads .claude/settings.json and .claude/settings.local.json
// from repoPath, converts patterns, and persists to the per-repo cache file.
// Safe to call multiple times (idempotent — deduplicates with existing rules).
// Missing or malformed files are silently skipped.
func ImportRepoSettings(repoPath, repoName string, store *Store) error {
	if store == nil {
		return nil
	}
	scope := scopeFor(repoName)

	// Load existing rules from disk for deduplication
	existing, err := store.Load(scope)
	if err != nil {
		return err
	}

	// Collect new rules from both settings files
	var newRules []Rule
	for _, filename := range []string{"settings.json", "settings.local.json"} {
		path := filepath.Join(repoPath, ".claude", filename)
		rules, err := readSettingsFile(path, repoName)
		if err != nil {
			log.Printf("warning: reading %s: %v", path, err)
			continue
		}
		newRules = append(newRules, rules...)
	}

	if len(newRules) == 0 {
		return nil
	}

	// Merge: add new rules that don't already exist
	merged := make([]Rule, len(existing))
	copy(merged, existing)
	for _, nr := range newRules {
		if !hasRule(merged, nr) {
			merged = append(merged, nr)
		}
	}

	// Only save if we actually added something
	if len(merged) > len(existing) {
		return store.Save(scope, merged)
	}
	return nil
}

// readSettingsFile reads a single Claude settings file and returns permission rules.
func readSettingsFile(path, repoName string) ([]Rule, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var settings ClaudeSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, err
	}

	var rules []Rule
	for _, pattern := range settings.Permissions.Allow {
		rules = append(rules, Rule{
			ToolPattern: normalizePattern(pattern),
			Effect:      "allow",
			RepoName:    repoName,
		})
	}
	for _, pattern := range settings.Permissions.Deny {
		rules = append(rules, Rule{
			ToolPattern: normalizePattern(pattern),
			Effect:      "deny",
			RepoName:    repoName,
		})
	}
	return rules, nil
}

// hasRule checks if a rule already exists in the slice (by ToolPattern + Effect).
func hasRule(rules []Rule, r Rule) bool {
	for _, existing := range rules {
		if existing.ToolPattern == r.ToolPattern && existing.Effect == r.Effect {
			return true
		}
	}
	return false
}

// normalizePattern converts Claude CLI pattern format to our internal format.
// Specifically converts Bash colon-wildcard ":*)" to space-wildcard " *)".
//
//	"Bash(go:*)"         → "Bash(go *)"
//	"Bash(go vet:*)"     → "Bash(go vet *)"
//	"Bash(done)"         → "Bash(done)"          (no ":*" suffix, unchanged)
//	"WebSearch"           → "WebSearch"            (no parens, unchanged)
//	"WebFetch(domain:x)"  → "WebFetch(domain:x)"  (not ending in ":*)", unchanged)
func normalizePattern(pattern string) string {
	if strings.HasSuffix(pattern, ":*)") {
		return pattern[:len(pattern)-3] + " *)"
	}
	return pattern
}
