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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Store handles persistence of permission rules to JSON files.
// Files are stored under BaseDir: one "global.json" and one "<repoName>.json" per repo.
type Store struct {
	BaseDir string
	mu      sync.Mutex
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

// encodedScopePrefix marks repo names that are not safe as a single filename.
const encodedScopePrefix = "_repo_b64_"

// scopeFor returns the filename scope for a repo name.
// "" → "global", "global" → "_repo_global", otherwise a safe filename scope.
func scopeFor(repoName string) string {
	if repoName == "" {
		return globalScope
	}
	if repoName == globalScope {
		return repoGlobalScope
	}
	if isPortableScopeName(repoName) {
		return repoName
	}
	return encodedScopePrefix + base64.RawURLEncoding.EncodeToString([]byte(repoName))
}

func repoNameForScope(scope string) string {
	switch {
	case scope == globalScope:
		return ""
	case scope == repoGlobalScope:
		return globalScope
	case strings.HasPrefix(scope, encodedScopePrefix):
		decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(scope, encodedScopePrefix))
		if err == nil {
			return string(decoded)
		}
	}
	return scope
}

func isPortableScopeName(scope string) bool {
	if scope == "" || scope == "." || scope == ".." {
		return false
	}
	for _, r := range scope {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '.', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

func (s *Store) scopePath(scope string) (string, error) {
	if !isPortableScopeName(scope) {
		return "", fmt.Errorf("invalid permission scope filename %q", scope)
	}
	path := filepath.Join(s.BaseDir, scope+".json")
	rel, err := filepath.Rel(s.BaseDir, path)
	if err != nil {
		return "", fmt.Errorf("resolve permission scope path: %w", err)
	}
	if rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." || filepath.IsAbs(rel) {
		return "", fmt.Errorf("permission scope %q escapes permissions dir", scope)
	}
	return path, nil
}

// Load reads rules from the scope's JSON file.
// Returns nil, nil if file doesn't exist.
// Malformed JSON → logs warning, returns nil, nil.
func (s *Store) Load(scope string) ([]Rule, error) {
	path, err := s.scopePath(scope)
	if err != nil {
		return nil, err
	}
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
	// Convert to Rule, setting RepoName from scope (reverse scopeFor mapping).
	repoName := repoNameForScope(scope)
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
	destPath, err := s.scopePath(scope)
	if err != nil {
		return err
	}
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

	if err := os.Rename(tmpPath, destPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("renaming temp file: %w", err)
	}
	return nil
}

// ruleExists reports whether rules already contains a rule matching pattern
// and effect. If repoName is provided, the match also requires RepoName to be
// equal; otherwise RepoName is ignored (callers checking within a single
// scope file, where every rule already shares the same RepoName, don't need
// it).
func ruleExists(rules []Rule, pattern, effect string, repoName ...string) bool {
	for _, r := range rules {
		if r.ToolPattern != pattern || r.Effect != effect {
			continue
		}
		if len(repoName) > 0 && r.RepoName != repoName[0] {
			continue
		}
		return true
	}
	return false
}

// EnsureGlobalDefaults ensures every rule in defaultGlobalRules() exists in
// the on-disk global.json. Missing defaults are appended; user-added rules
// are preserved. On first run the file is created from scratch.
func (s *Store) EnsureGlobalDefaults() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	defaults := defaultGlobalRules()

	existing, err := s.Load(globalScope)
	if err != nil {
		return fmt.Errorf("loading global permissions: %w", err)
	}

	// Append any missing defaults.
	merged := existing
	for _, d := range defaults {
		if !ruleExists(existing, d.ToolPattern, d.Effect) {
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
// Deduplication: match on ToolPattern + Effect within the exact scope.
func (s *Store) AppendRule(scope string, rule Rule) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	existing, err := s.Load(scope)
	if err != nil {
		return false, err
	}
	if ruleExists(existing, rule.ToolPattern, rule.Effect) {
		return false, nil // already exists
	}
	existing = append(existing, rule)
	if err := s.Save(scope, existing); err != nil {
		return false, err
	}
	return true, nil
}
