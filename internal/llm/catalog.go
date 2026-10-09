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

package llm

import (
	"encoding/json"
	"os/exec"
	"strings"
	"sync"
)

// Catalog is an embeddable provider model catalog: the stored entries,
// an optional default-entries callback, and the lock guarding them. Providers
// embed it (by value) to satisfy the catalog surface of LLMProvider;
// OpenCode overrides matching and context windows where its rules differ.
type Catalog struct {
	mu      sync.RWMutex
	entries []ModelInfo
	// Defaults supplies entries whenever the stored catalog is empty
	// (including after SetModelCatalog(nil)). Nil means no defaults:
	// reads return nil and matching sees no entries.
	Defaults func() []ModelInfo

	// binary is the resolved CLI executable name DetectCLI probes in PATH.
	binary string
}

// NewCatalog returns an embeddable catalog bound to the CLI binary name
// used by DetectCLI (binary, e.g. "claude" — providers pass their resolved
// override) and the optional defaults callback.
func NewCatalog(binary string, defaults func() []ModelInfo) Catalog {
	return Catalog{binary: binary, Defaults: defaults}
}

// LookupModel returns the first catalog entry whose ID or an alias equals
// model case-insensitively. ok is false when no entry matches.
func LookupModel(catalog []ModelInfo, model string) (ModelInfo, bool) {
	for _, entry := range catalog {
		if strings.EqualFold(entry.ID, model) {
			return entry, true
		}
		for _, alias := range entry.Aliases {
			if strings.EqualFold(alias, model) {
				return entry, true
			}
		}
	}
	return ModelInfo{}, false
}

// effectiveCatalog returns the stored entries, or the defaults callback's
// entries when nothing is stored (including after SetModelCatalog(nil)). A
// nil callback yields no entries, so a defaults-less provider never
// fabricates a catalog.
func (c *Catalog) effectiveCatalog() []ModelInfo {
	c.mu.RLock()
	entries := c.entries
	c.mu.RUnlock()
	if len(entries) > 0 {
		return entries
	}
	if c.Defaults != nil {
		return c.Defaults()
	}
	return nil
}

// DetectCLI reports whether the provider's CLI binary is available in PATH.
func (c *Catalog) DetectCLI() bool {
	_, err := exec.LookPath(c.binary)
	return err == nil
}

// MatchesModel reports whether model names an entry in the effective catalog
// by its ID or an alias, case-insensitively. The model string is matched as
// given: surrounding whitespace is not trimmed.
func (c *Catalog) MatchesModel(model string) bool {
	_, ok := LookupModel(c.effectiveCatalog(), model)
	return ok
}

// AvailableModels returns the IDs of the effective catalog in catalog order.
func (c *Catalog) AvailableModels() []string {
	cat := c.effectiveCatalog()
	ids := make([]string, len(cat))
	for i, m := range cat {
		ids[i] = m.ID
	}
	return ids
}

// ContextWindowForModel returns the context window of the first
// effective-catalog entry matching model by ID or alias whose window is
// positive; entries without a usable window are skipped. Zero means unknown.
func (c *Catalog) ContextWindowForModel(model string) int {
	cat := c.effectiveCatalog()
	withWindow := make([]ModelInfo, 0, len(cat))
	for _, entry := range cat {
		if entry.ContextWindow > 0 {
			withWindow = append(withWindow, entry)
		}
	}
	if entry, ok := LookupModel(withWindow, model); ok {
		return entry.ContextWindow
	}
	return 0
}

// SetModelCatalog stores a deep copy of the discovered catalog. An empty (or
// nil) catalog empties the stored entries, so reads fall back to Defaults.
func (c *Catalog) SetModelCatalog(models []ModelInfo) {
	c.mu.Lock()
	c.entries = cloneCatalog(models)
	c.mu.Unlock()
}

// ModelCatalog returns a deep copy of the effective catalog so callers can
// never mutate shared state.
func (c *Catalog) ModelCatalog() []ModelInfo {
	return cloneCatalog(c.effectiveCatalog())
}

// cloneCatalog deep-copies catalog metadata. Catalog metadata is JSON-shaped;
// deep copies keep nested variant options isolated across refreshes, callers,
// and concurrently built sessions.
func cloneCatalog(models []ModelInfo) []ModelInfo {
	data, err := json.Marshal(models)
	if err != nil {
		return nil
	}
	var result []ModelInfo
	if json.Unmarshal(data, &result) != nil {
		return nil
	}
	return result
}
