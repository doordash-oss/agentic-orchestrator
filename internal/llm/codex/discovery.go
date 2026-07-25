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

package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/clirun"
)

type codexModelCatalogPayload struct {
	Models []codexDiscoveredModel `json:"models"`
}

type codexDiscoveredModel struct {
	Slug             string `json:"slug"`
	DisplayName      string `json:"display_name"`
	Description      string `json:"description"`
	Visibility       string `json:"visibility"`
	SupportedInAPI   *bool  `json:"supported_in_api"`
	ContextWindow    int    `json:"context_window"`
	MaxContextWindow int    `json:"max_context_window"`
}

// DiscoverModelCatalog refreshes Codex's local model catalog via
// `codex debug models`. The command refreshes from Codex's remote catalog by
// default; if that path fails, we fall back to the catalog bundled in the CLI.
func (p *Provider) DiscoverModelCatalog(ctx context.Context) ([]llm.ModelInfo, error) {
	return p.DiscoverModelCatalogWithProgress(ctx, nil)
}

// DiscoverModelCatalogWithProgress refreshes Codex's model catalog and reports
// each parsed visible model before returning the final catalog.
func (p *Provider) DiscoverModelCatalogWithProgress(ctx context.Context, report llm.ModelDiscoveryReporter) ([]llm.ModelInfo, error) {
	runner := p.runner
	if runner == nil {
		runner = clirun.DefaultRunner()
	}
	binary := p.cliBinary()

	out, err := runner(ctx, binary, []string{"debug", "models"}, nil)
	if err == nil {
		models, parseErr := parseCodexModelCatalogWithProgress(out, report)
		if parseErr == nil {
			return models, nil
		}
		err = parseErr
	}

	bundled, bundledErr := runner(ctx, binary, []string{"debug", "models", "--bundled"}, nil)
	if bundledErr != nil {
		return nil, fmt.Errorf("running codex debug models: %w; bundled fallback: %v", err, bundledErr)
	}
	models, parseErr := parseCodexModelCatalogWithProgress(bundled, report)
	if parseErr != nil {
		return nil, fmt.Errorf("parsing codex bundled model catalog after live catalog failure (%v): %w", err, parseErr)
	}
	return models, nil
}

func parseCodexModelCatalog(out []byte) ([]llm.ModelInfo, error) {
	return parseCodexModelCatalogWithProgress(out, nil)
}

func parseCodexModelCatalogWithProgress(out []byte, report llm.ModelDiscoveryReporter) ([]llm.ModelInfo, error) {
	var payload codexModelCatalogPayload
	if err := json.Unmarshal(out, &payload); err != nil {
		return nil, fmt.Errorf("parse codex model catalog JSON: %w", err)
	}

	models := make([]llm.ModelInfo, 0, len(payload.Models))
	seen := make(map[string]bool, len(payload.Models))
	for _, raw := range payload.Models {
		id := strings.TrimSpace(raw.Slug)
		if id == "" || seen[id] {
			continue
		}
		if raw.Visibility != "" && raw.Visibility != "list" {
			continue
		}
		if raw.SupportedInAPI != nil && !*raw.SupportedInAPI {
			continue
		}

		displayName := strings.TrimSpace(raw.DisplayName)
		if displayName == "" {
			displayName = codexDisplayNameFromSlug(id)
		}

		windows := codexContextWindowOptions(raw.ContextWindow, raw.MaxContextWindow)
		for i, window := range windows {
			info := llm.ModelInfo{
				ID:                 id,
				DisplayName:        displayName,
				ContextWindow:      window,
				Category:           codexCategoryForDiscoveredModel(id),
				EffortCapabilities: []llm.EffortLevel{llm.EffortLow, llm.EffortMedium, llm.EffortHigh, llm.EffortXHigh, llm.EffortMax},
			}
			if label := llm.ContextWindowLabel(window); label != "" {
				info.ID = llm.ModelWithContextWindow(id, window)
				info.DisplayName = displayName + " (" + label + ")"
			}
			if i == 0 {
				info.Aliases = llm.AppendUniqueAlias(info.Aliases, info.ID, id)
			}
			models = append(models, info)
			if report != nil {
				report(info)
			}
		}
		seen[id] = true
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("codex model catalog contained no visible API-supported models")
	}
	return models, nil
}

func codexContextWindowOptions(contextWindow, maxContextWindow int) []int {
	if contextWindow <= 0 {
		if maxContextWindow > 0 {
			return []int{maxContextWindow}
		}
		return []int{0}
	}
	if maxContextWindow > 0 && maxContextWindow != contextWindow {
		return []int{contextWindow, maxContextWindow}
	}
	return []int{contextWindow}
}

func codexDisplayNameFromSlug(slug string) string {
	rawParts := strings.Split(slug, "-")
	parts := make([]string, 0, len(rawParts))
	if len(rawParts) >= 2 && strings.EqualFold(rawParts[0], "gpt") {
		parts = append(parts, "GPT-"+rawParts[1])
		rawParts = rawParts[2:]
	}
	for _, raw := range rawParts {
		if raw != "" {
			parts = append(parts, raw)
		}
	}
	for i, part := range parts {
		if strings.EqualFold(part, "gpt") {
			parts[i] = "GPT"
			continue
		}
		if len(part) > 0 {
			parts[i] = strings.ToUpper(part[:1]) + part[1:]
		}
	}
	return strings.Join(parts, " ")
}

func codexCategoryForDiscoveredModel(id string) string {
	lower := strings.ToLower(id)
	switch {
	case strings.Contains(lower, "nano"):
		return "cheap"
	case strings.Contains(lower, "mini"):
		return "balanced"
	case lower == "gpt-5.4":
		return "balanced"
	case lower == "gpt-5.3-codex":
		return "balanced"
	case strings.HasPrefix(lower, "gpt-5."):
		return "capable"
	case strings.Contains(lower, "codex"):
		return "balanced"
	default:
		return ""
	}
}
