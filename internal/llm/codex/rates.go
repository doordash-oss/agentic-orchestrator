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
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// tokenRate holds per-million-token pricing for a model.
type tokenRate struct {
	inputPerMToken           float64
	cachedInputPerMToken     float64
	cacheWritePerMToken      float64
	outputPerMToken          float64
	longInputPerMToken       float64
	longCachedInputPerMToken float64
	longCacheWritePerMToken  float64
	longOutputPerMToken      float64
}

// modelRates maps canonical model names to their token pricing.
// Source: https://developers.openai.com/api/docs/pricing (verified 2026-11-18; standard API estimates, not ChatGPT credit prices)
// Cache writes use the 30-minute write price.
var modelRates = map[string]tokenRate{
	"gpt-6.2-astra": {
		inputPerMToken: 8, cachedInputPerMToken: 0.4, cacheWritePerMToken: 10, outputPerMToken: 40,
		longInputPerMToken: 16, longCachedInputPerMToken: 0.8, longCacheWritePerMToken: 20, longOutputPerMToken: 60,
	},
	"gpt-6.2-sol": {
		inputPerMToken: 2, cachedInputPerMToken: 0.1, cacheWritePerMToken: 2.5, outputPerMToken: 10,
		longInputPerMToken: 4, longCachedInputPerMToken: 0.2, longCacheWritePerMToken: 5, longOutputPerMToken: 15,
	},
	"gpt-6.1-sol": {
		inputPerMToken: 2, cachedInputPerMToken: 0.1, cacheWritePerMToken: 2.5, outputPerMToken: 10,
		longInputPerMToken: 4, longCachedInputPerMToken: 0.2, longCacheWritePerMToken: 5, longOutputPerMToken: 15,
	},
	"gpt-6.1-terra": {
		inputPerMToken: 1, cachedInputPerMToken: 0.05, cacheWritePerMToken: 1.25, outputPerMToken: 5,
		longInputPerMToken: 2, longCachedInputPerMToken: 0.1, longCacheWritePerMToken: 2.5, longOutputPerMToken: 7.5,
	},
	"gpt-6-sol": {
		inputPerMToken: 2, cachedInputPerMToken: 0.2, cacheWritePerMToken: 2.5, outputPerMToken: 10,
		longInputPerMToken: 4, longCachedInputPerMToken: 0.4, longCacheWritePerMToken: 5, longOutputPerMToken: 15,
	},
	"gpt-6-luna": {
		inputPerMToken: 0.1, cachedInputPerMToken: 0.01, cacheWritePerMToken: 0.125, outputPerMToken: 0.5,
		longInputPerMToken: 0.2, longCachedInputPerMToken: 0.02, longCacheWritePerMToken: 0.25, longOutputPerMToken: 0.75,
	},
	"gpt-6-astra": {
		inputPerMToken: 10, cachedInputPerMToken: 1, cacheWritePerMToken: 12.5, outputPerMToken: 50,
		longInputPerMToken: 20, longCachedInputPerMToken: 2, longCacheWritePerMToken: 25, longOutputPerMToken: 75,
	},
	"gpt-5.6": {
		inputPerMToken: 4, cachedInputPerMToken: 0.4, cacheWritePerMToken: 5, outputPerMToken: 20,
		longInputPerMToken: 8, longCachedInputPerMToken: 0.8, longCacheWritePerMToken: 10, longOutputPerMToken: 30,
	},
	"gpt-5.6-sol": {
		inputPerMToken: 4, cachedInputPerMToken: 0.4, cacheWritePerMToken: 5, outputPerMToken: 20,
		longInputPerMToken: 8, longCachedInputPerMToken: 0.8, longCacheWritePerMToken: 10, longOutputPerMToken: 30,
	},
	"gpt-5.6-terra": {
		inputPerMToken: 2, cachedInputPerMToken: 0.2, cacheWritePerMToken: 2.5, outputPerMToken: 12,
		longInputPerMToken: 4, longCachedInputPerMToken: 0.4, longCacheWritePerMToken: 5, longOutputPerMToken: 18,
	},
	"gpt-5.6-luna": {
		inputPerMToken: 0.2, cachedInputPerMToken: 0.02, cacheWritePerMToken: 0.25, outputPerMToken: 1.2,
		longInputPerMToken: 0.4, longCachedInputPerMToken: 0.04, longCacheWritePerMToken: 0.5, longOutputPerMToken: 1.8,
	},
	"gpt-5.5":       {inputPerMToken: 5.00, cachedInputPerMToken: 0.50, outputPerMToken: 30.00, longInputPerMToken: 10.00, longCachedInputPerMToken: 1.00, longOutputPerMToken: 45.00},
	"gpt-5.4":       {inputPerMToken: 2.50, cachedInputPerMToken: 0.25, outputPerMToken: 15.00, longInputPerMToken: 5.00, longCachedInputPerMToken: 0.50, longOutputPerMToken: 22.50},
	"gpt-5.4-mini":  {inputPerMToken: 0.75, cachedInputPerMToken: 0.075, outputPerMToken: 4.50},
	"gpt-5.3-codex": {inputPerMToken: 1.75, cachedInputPerMToken: 0.175, outputPerMToken: 14.00},
}

// OpenAI applies long-context rates when a request exceeds 272K input tokens.
const longContextInputThreshold = 272_000

// defaultModel is the model requested when no model is configured.
const defaultModel = "gpt-5.4"

// rateModelName strips the context-window suffix and any gateway route prefix
// (for example "@route/gpt-6.2-sol[500K]" prices as "gpt-6.2-sol").
func rateModelName(model string) string {
	m := strings.ToLower(llm.StripModelContextWindow(model))
	if idx := strings.LastIndex(m, "/"); idx >= 0 {
		m = m[idx+1:]
	}
	return m
}

// lookupRate resolves a model string to its token rate. Unknown models have no
// price; substituting another model would produce a misleading estimate.
func lookupRate(model string) (tokenRate, bool) {
	m := rateModelName(model)

	// Direct match first.
	if r, ok := modelRates[m]; ok {
		return r, true
	}

	if m == "" {
		return modelRates[defaultModel], true
	}

	return tokenRate{}, false
}

// computeCostForServiceTier applies known API tier prices to the live fallback.
// Provider billing reconciliation remains authoritative for account-specific
// credits, promotions, and tiers not represented by this rate card.
func computeCostForServiceTier(model, tier string, input, cached, writes, output, contextInput int) float64 {
	cost := computeCostForContext(model, input, cached, writes, output, contextInput)
	switch strings.ToLower(tier) {
	case "flex", "batch":
		return cost * 0.5
	case "fast", "priority":
		if m := rateModelName(model); strings.HasPrefix(m, "gpt-6") || strings.HasPrefix(m, "gpt-5.6") {
			return cost * 2
		}
	}
	return cost
}
