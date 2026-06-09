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
	"strconv"
	"strings"
)

// ModelInfo represents discovered metadata for a single model.
// Pricing is handled at runtime by each provider's rate table (e.g. codex/rates.go),
// not stored in discovery metadata.
type ModelInfo struct {
	ID            string   `yaml:"id"`                // Selectable model name, e.g. "opus[200K]", "gpt-5.4[1M]"
	DisplayName   string   `yaml:"display_name"`      // Human-readable name
	ContextWindow int      `yaml:"context_window"`    // Max tokens
	Aliases       []string `yaml:"aliases,omitempty"` // Alternative names, e.g. ["opus", "claude-opus-4-8"]
	Category      string   `yaml:"category"`          // "cheap", "balanced", "capable"
}

// ContextWindowLabel formats a token window for compact model IDs.
// Sub-million windows use K (e.g. 272K); million-scale windows use M
// (e.g. 1M, 1.05M).
func ContextWindowLabel(tokens int) string {
	if tokens <= 0 {
		return ""
	}
	if tokens < 1_000_000 {
		return strconv.Itoa((tokens+500)/1000) + "K"
	}
	if tokens%1_000_000 == 0 {
		return strconv.Itoa(tokens/1_000_000) + "M"
	}
	label := strconv.FormatFloat(float64(tokens)/1_000_000, 'f', 2, 64)
	label = strings.TrimRight(strings.TrimRight(label, "0"), ".")
	return label + "M"
}

// ModelWithContextWindow returns a display/selectable model ID with an
// explicit context-window suffix.
func ModelWithContextWindow(model string, tokens int) string {
	label := ContextWindowLabel(tokens)
	if strings.TrimSpace(model) == "" || label == "" {
		return model
	}
	return model + "[" + label + "]"
}

// StripModelContextWindow removes a trailing [<window>] selector suffix.
func StripModelContextWindow(model string) string {
	if !strings.HasSuffix(model, "]") {
		return model
	}
	if idx := strings.LastIndex(model, "["); idx > 0 {
		return model[:idx]
	}
	return model
}

// AppendUniqueAlias returns aliases with alias appended if not already present
// case-insensitively and not equal to the canonical ID.
func AppendUniqueAlias(aliases []string, id, alias string) []string {
	alias = strings.TrimSpace(alias)
	if alias == "" || strings.EqualFold(alias, id) {
		return aliases
	}
	for _, existing := range aliases {
		if strings.EqualFold(existing, alias) {
			return aliases
		}
	}
	return append(aliases, alias)
}

// CatalogEnricher is implemented by providers whose model catalog can be
// overridden at runtime, either from provider CLI discovery or from tests.
type CatalogEnricher interface {
	SetModelCatalog(models []ModelInfo)
}

// categoryRank maps category strings to a sort rank (higher = more capable).
var categoryRank = map[string]int{
	"capable":  3,
	"balanced": 2,
	"cheap":    1,
}

// TopModelIDs returns the IDs of the top n models from catalog, ranked by category.
// Models with unknown categories are ranked lowest. If catalog has <= n entries,
// all IDs are returned in category order.
func TopModelIDs(catalog []ModelInfo, n int) []string {
	if len(catalog) <= n {
		ids := make([]string, len(catalog))
		for i, m := range catalog {
			ids[i] = m.ID
		}
		return ids
	}

	// Sort a copy by category rank descending, preserving original order as tiebreaker.
	type ranked struct {
		rank int
		idx  int
		id   string
	}
	items := make([]ranked, len(catalog))
	for i, m := range catalog {
		items[i] = ranked{rank: categoryRank[m.Category], idx: i, id: m.ID}
	}
	// Simple selection: pick top n by rank (stable by original index).
	for i := range n {
		best := i
		for j := i + 1; j < len(items); j++ {
			if items[j].rank > items[best].rank ||
				(items[j].rank == items[best].rank && items[j].idx < items[best].idx) {
				best = j
			}
		}
		items[i], items[best] = items[best], items[i]
	}

	ids := make([]string, n)
	for i := range n {
		ids[i] = items[i].id
	}
	return ids
}

// ModelsByCategory returns all models matching the given category.
func ModelsByCategory(catalog []ModelInfo, category string) []ModelInfo {
	var out []ModelInfo
	for _, m := range catalog {
		if m.Category == category {
			out = append(out, m)
		}
	}
	return out
}

// MostCapableFrom returns the most capable model from the catalog.
// Selection: highest categoryRank, ties broken by largest ContextWindow.
// Returns false if the catalog is empty.
func MostCapableFrom(catalog []ModelInfo) (ModelInfo, bool) {
	if len(catalog) == 0 {
		return ModelInfo{}, false
	}
	best := catalog[0]
	bestRank := categoryRank[best.Category]
	for _, m := range catalog[1:] {
		r := categoryRank[m.Category]
		if r > bestRank || (r == bestRank && m.ContextWindow > best.ContextWindow) {
			best = m
			bestRank = r
		}
	}
	return best, true
}

// CheapestFrom returns the cheapest (lowest category rank) model.
// Skips models with unranked categories. Returns false if no ranked models exist.
func CheapestFrom(catalog []ModelInfo) (ModelInfo, bool) {
	var best ModelInfo
	bestRank := 0
	found := false
	for _, m := range catalog {
		r := categoryRank[m.Category]
		if r == 0 {
			continue
		}
		if !found || r < bestRank {
			best = m
			bestRank = r
			found = true
		}
	}
	return best, found
}

// BalancedFrom returns a balanced-category model from the catalog.
// If no model has category "balanced", returns the model closest to rank 2.
// Returns false if the catalog is empty or all models are unranked.
func BalancedFrom(catalog []ModelInfo) (ModelInfo, bool) {
	// First try exact match
	for _, m := range catalog {
		if m.Category == "balanced" {
			return m, true
		}
	}
	// Fallback: closest to rank 2
	var best ModelInfo
	bestDist := -1
	found := false
	for _, m := range catalog {
		r := categoryRank[m.Category]
		if r == 0 {
			continue
		}
		dist := r - 2
		if dist < 0 {
			dist = -dist
		}
		if !found || dist < bestDist {
			best = m
			bestDist = dist
			found = true
		}
	}
	return best, found
}

// LargestContextFrom returns the model with the largest context window.
// Ties are broken by higher category rank.
// Returns false if the catalog is empty.
func LargestContextFrom(catalog []ModelInfo) (ModelInfo, bool) {
	if len(catalog) == 0 {
		return ModelInfo{}, false
	}
	best := catalog[0]
	for _, m := range catalog[1:] {
		if m.ContextWindow > best.ContextWindow ||
			(m.ContextWindow == best.ContextWindow && categoryRank[m.Category] > categoryRank[best.Category]) {
			best = m
		}
	}
	return best, true
}
