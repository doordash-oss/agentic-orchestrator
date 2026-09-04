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
// Source: https://developers.openai.com/api/docs/pricing (verified 2026-09-04; standard API estimates, not ChatGPT credit prices)
var modelRates = map[string]tokenRate{
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

// lookupRate resolves a model string to its token rate.
// It strips context-window suffixes. Unknown models have no price; substituting
// another model would produce a misleading estimate.
func lookupRate(model string) (tokenRate, bool) {
	m := strings.ToLower(llm.StripModelContextWindow(model))

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
		switch strings.ToLower(llm.StripModelContextWindow(model)) {
		case "gpt-6-astra", "gpt-5.6", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna":
			return cost * 2
		}
	}
	return cost
}
