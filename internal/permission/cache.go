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
	"log"
	"sync"
)

// Cache holds permission rules in memory, protected by a mutex.
// A single Cache instance is shared across all sessions in the process.
type Cache struct {
	mu    sync.RWMutex
	rules []Rule
	store *Store // nil = no persistence (in-memory only, for tests)
}

// NewCache creates an empty permission cache.
// If store is non-nil, rules are persisted to disk.
func NewCache(store *Store) *Cache {
	return &Cache{store: store}
}

// StoreRef returns the underlying Store, or nil if no persistence is configured.
func (c *Cache) StoreRef() *Store {
	return c.store
}

// Check looks up a matching rule for the given tool request.
// Returns the matching rule and true if found, or zero Rule and false if not.
// Deny rules are checked first (deny wins over allow).
func (c *Cache) Check(toolName, toolInput, repoName string) (Rule, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	// First pass: deny rules (global + repo-scoped)
	for _, r := range c.rules {
		if r.Effect == "deny" && (r.RepoName == "" || r.RepoName == repoName) {
			if r.Match(toolName, toolInput) {
				return r, true
			}
		}
	}
	// Second pass: allow rules
	for _, r := range c.rules {
		if r.Effect == "allow" && (r.RepoName == "" || r.RepoName == repoName) {
			if r.Match(toolName, toolInput) {
				return r, true
			}
		}
	}
	return Rule{}, false
}

// RememberAllow adds an allow rule to the cache and persists to disk.
func (c *Cache) RememberAllow(toolName, toolInput, repoName string) {
	pattern := InferBashPattern(toolName, toolInput)
	rule := Rule{ToolPattern: pattern, Effect: "allow", RepoName: repoName}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rules = append(c.rules, rule)
	if c.store != nil {
		// Best-effort persist — log but don't block on error
		if err := c.store.AppendRule(scopeFor(repoName), rule); err != nil {
			log.Printf("warning: persisting permission rule: %v", err)
		}
	}
}

// Rules returns a snapshot of all rules (for testing).
func (c *Cache) Rules() []Rule {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]Rule, len(c.rules))
	copy(out, c.rules)
	return out
}

// LoadAndMerge loads rules from disk and merges them into the cache.
// Always loads global.json. If repoName != "", also loads <repoName>.json.
func (c *Cache) LoadAndMerge(repoName string) {
	if c.store == nil {
		return
	}
	var diskRules []Rule
	if globalRules, err := c.store.Load(globalScope); err == nil && len(globalRules) > 0 {
		diskRules = append(diskRules, globalRules...)
	}
	if repoName != "" {
		if repoRules, err := c.store.Load(scopeFor(repoName)); err == nil && len(repoRules) > 0 {
			diskRules = append(diskRules, repoRules...)
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, dr := range diskRules {
		if !c.hasRuleLocked(dr) {
			c.rules = append(c.rules, dr)
		}
	}
}

// hasRuleLocked checks if a rule already exists in the cache. Must be called with mu held.
func (c *Cache) hasRuleLocked(r Rule) bool {
	for _, existing := range c.rules {
		if existing.ToolPattern == r.ToolPattern && existing.Effect == r.Effect && existing.RepoName == r.RepoName {
			return true
		}
	}
	return false
}
