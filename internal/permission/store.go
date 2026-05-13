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
	"fmt"
	"log"
	"os"
	"path/filepath"
)

// Store handles persistence of permission rules to JSON files.
// Files are stored under BaseDir: one "global.json" and one "<repoName>.json" per repo.
type Store struct {
	BaseDir string
}

func NewStore(baseDir string) *Store {
	return &Store{BaseDir: baseDir}
}

// persistedRule is the on-disk JSON representation (no RepoName — inferred from filename).
type persistedRule struct {
	ToolPattern string `json:"tool_pattern"`
	Effect      string `json:"effect"`
}

type persistedFile struct {
	Rules []persistedRule `json:"rules"`
}

// globalScope is the on-disk filename for the global (repo-independent)
// permission file: ~/.agentic-workflow/permissions/global.json.
const globalScope = "global"

// repoGlobalScope is the escaped on-disk filename for a repo literally named
// "global", preventing collision with the real global scope file.
const repoGlobalScope = "_repo_global"

// scopeFor returns the filename scope for a repo name.
// "" → "global", "global" → "_repo_global", otherwise the repo name itself.
func scopeFor(repoName string) string {
	if repoName == "" {
		return globalScope
	}
	if repoName == globalScope {
		return repoGlobalScope
	}
	return repoName
}

// Load reads rules from the scope's JSON file.
// Returns nil, nil if file doesn't exist.
// Malformed JSON → logs warning, returns nil, nil.
func (s *Store) Load(scope string) ([]Rule, error) {
	path := filepath.Join(s.BaseDir, scope+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading permission file %s: %w", path, err)
	}
	if len(data) == 0 {
		return nil, nil
	}
	var pf persistedFile
	if err := json.Unmarshal(data, &pf); err != nil {
		log.Printf("warning: corrupt permission file %s: %v", path, err)
		return nil, nil
	}
	// Convert to Rule, setting RepoName from scope (reverse scopeFor mapping)
	repoName := ""
	if scope == repoGlobalScope {
		repoName = globalScope // reverse the escape: "_repo_global" → "global"
	} else if scope != globalScope {
		repoName = scope
	}
	rules := make([]Rule, len(pf.Rules))
	for i, pr := range pf.Rules {
		rules[i] = Rule{
			ToolPattern: pr.ToolPattern,
			Effect:      pr.Effect,
			RepoName:    repoName,
		}
	}
	return rules, nil
}

// Save writes rules atomically using os.CreateTemp + os.Rename pattern.
func (s *Store) Save(scope string, rules []Rule) error {
	if err := os.MkdirAll(s.BaseDir, 0o755); err != nil {
		return fmt.Errorf("creating permissions dir: %w", err)
	}
	pf := persistedFile{
		Rules: make([]persistedRule, len(rules)),
	}
	for i, r := range rules {
		pf.Rules[i] = persistedRule{
			ToolPattern: r.ToolPattern,
			Effect:      r.Effect,
		}
	}
	data, err := json.MarshalIndent(pf, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling permissions: %w", err)
	}

	// Atomic write: temp file + rename
	tmp, err := os.CreateTemp(s.BaseDir, ".perm-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("writing temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("closing temp file: %w", err)
	}

	destPath := filepath.Join(s.BaseDir, scope+".json")
	if err := os.Rename(tmpPath, destPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("renaming temp file: %w", err)
	}
	return nil
}

// EnsureGlobalDefaults ensures every rule in defaultGlobalRules() exists in
// the on-disk global.json. Missing defaults are appended; user-added rules
// are preserved. On first run the file is created from scratch.
func (s *Store) EnsureGlobalDefaults() error {
	defaults := defaultGlobalRules()

	existing, err := s.Load(globalScope)
	if err != nil {
		return fmt.Errorf("loading global permissions: %w", err)
	}

	// Build a set of existing patterns+effects for fast lookup.
	type key struct{ pattern, effect string }
	seen := make(map[key]struct{}, len(existing))
	for _, r := range existing {
		seen[key{r.ToolPattern, r.Effect}] = struct{}{}
	}

	// Append any missing defaults.
	merged := existing
	for _, d := range defaults {
		if _, ok := seen[key{d.ToolPattern, d.Effect}]; !ok {
			merged = append(merged, d)
		}
	}

	if len(merged) == len(existing) {
		return nil // nothing new
	}

	if err := s.Save(globalScope, merged); err != nil {
		return fmt.Errorf("saving global permissions: %w", err)
	}
	return nil
}

// AppendRule loads existing rules, appends a new one (skip if duplicate), saves.
// Deduplication: match on ToolPattern + Effect.
func (s *Store) AppendRule(scope string, rule Rule) error {
	existing, err := s.Load(scope)
	if err != nil {
		return err
	}
	for _, r := range existing {
		if r.ToolPattern == rule.ToolPattern && r.Effect == rule.Effect {
			return nil // already exists
		}
	}
	existing = append(existing, rule)
	return s.Save(scope, existing)
}
