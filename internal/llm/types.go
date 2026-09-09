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
// Advertised pricing is optional metadata; runtime accounting remains provider-owned.
type ModelInfo struct {
	Capabilities *ModelCapabilities `yaml:"capabilities,omitempty" json:"capabilities,omitempty"`
	Cost         *ModelCost         `yaml:"cost,omitempty" json:"cost,omitempty"`
	// EffortVariants contains the exact provider options for each supported level.
	// It is persisted in the internal catalog cache, never exposed through REST.
	EffortVariants map[EffortLevel]map[string]any `yaml:"effort_variants,omitempty" json:"effort_variants,omitempty"`

	ID            string   `yaml:"id"`                // Selectable model name, e.g. "opus[200K]", "gpt-5.4[1M]"
	DisplayName   string   `yaml:"display_name"`      // Human-readable name
	ContextWindow int      `yaml:"context_window"`    // Max tokens
	Aliases       []string `yaml:"aliases,omitempty"` // Alternative names, e.g. ["opus", "claude-opus-4-8"]
	Category      string   `yaml:"category"`          // "cheap", "balanced", "capable"
	// EffortCapabilities is the ordered list of semantically distinct effort
	// levels the provider can honor for this model, from lowest to highest.
	// Empty means the model has no explicit effort control and is Auto-only.
	// Variants with identical option maps are collapsed so the API does not
	// advertise duplicate settings. Distinct high and max variants stay distinct.
	EffortCapabilities []EffortLevel `yaml:"effort_capabilities,omitempty" json:"effort_capabilities,omitempty"`
}

// ModelCapabilities records technical support, never inferred model quality.
// Nil fields mean unreported; an explicit false can exclude an incompatible model.
type ModelCapabilities struct {
	ToolCall   *bool `yaml:"tool_call,omitempty" json:"tool_call,omitempty"`
	TextOutput *bool `yaml:"text_output,omitempty" json:"text_output,omitempty"`
	Reasoning  *bool `yaml:"reasoning,omitempty" json:"reasoning,omitempty"`
}

// ModelCost records advertised USD per million tokens. Nil means unknown.
type ModelCost struct {
	Input  float64 `yaml:"input" json:"input"`
	Output float64 `yaml:"output" json:"output"`
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

// ParseModelContextWindow returns the token window encoded in a trailing
// [<number>K] or [<number>M] selector suffix. It returns 0 when no valid context
// suffix is present.
func ParseModelContextWindow(model string) int {
	if !strings.HasSuffix(model, "]") {
		return 0
	}
	idx := strings.LastIndex(model, "[")
	if idx <= 0 {
		return 0
	}
	label := strings.TrimSpace(model[idx+1 : len(model)-1])
	if label == "" {
		return 0
	}
	multiplier := 0
	switch suffix := strings.ToUpper(label[len(label)-1:]); suffix {
	case "K":
		multiplier = 1_000
	case "M":
		multiplier = 1_000_000
	default:
		return 0
	}
	value, err := strconv.ParseFloat(strings.TrimSpace(label[:len(label)-1]), 64)
	if err != nil || value <= 0 {
		return 0
	}
	return int(value*float64(multiplier) + 0.5)
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
