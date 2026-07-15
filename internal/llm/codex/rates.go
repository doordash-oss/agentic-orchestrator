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
// Source: https://developers.openai.com/api/docs/pricing (July 2026)
var modelRates = map[string]tokenRate{
	"gpt-5.6": {
		inputPerMToken: 5.00, cachedInputPerMToken: 0.50, cacheWritePerMToken: 6.25, outputPerMToken: 30.00,
		longInputPerMToken: 10.00, longCachedInputPerMToken: 1.00, longCacheWritePerMToken: 12.50, longOutputPerMToken: 45.00,
	},
	"gpt-5.6-sol": {
		inputPerMToken: 5.00, cachedInputPerMToken: 0.50, cacheWritePerMToken: 6.25, outputPerMToken: 30.00,
		longInputPerMToken: 10.00, longCachedInputPerMToken: 1.00, longCacheWritePerMToken: 12.50, longOutputPerMToken: 45.00,
	},
	"gpt-5.6-terra": {
		inputPerMToken: 2.50, cachedInputPerMToken: 0.25, cacheWritePerMToken: 3.125, outputPerMToken: 15.00,
		longInputPerMToken: 5.00, longCachedInputPerMToken: 0.50, longCacheWritePerMToken: 6.25, longOutputPerMToken: 22.50,
	},
	"gpt-5.6-luna": {
		inputPerMToken: 1.00, cachedInputPerMToken: 0.10, cacheWritePerMToken: 1.25, outputPerMToken: 6.00,
		longInputPerMToken: 2.00, longCachedInputPerMToken: 0.20, longCacheWritePerMToken: 2.50, longOutputPerMToken: 9.00,
	},
	"gpt-5.5":       {inputPerMToken: 5.00, cachedInputPerMToken: 0.50, outputPerMToken: 30.00, longInputPerMToken: 10.00, longCachedInputPerMToken: 1.00, longOutputPerMToken: 45.00},
	"gpt-5.4":       {inputPerMToken: 2.50, cachedInputPerMToken: 0.25, outputPerMToken: 15.00, longInputPerMToken: 5.00, longCachedInputPerMToken: 0.50, longOutputPerMToken: 22.50},
	"gpt-5.4-mini":  {inputPerMToken: 0.75, cachedInputPerMToken: 0.075, outputPerMToken: 4.50},
	"gpt-5.3-codex": {inputPerMToken: 1.75, cachedInputPerMToken: 0.175, outputPerMToken: 14.00},
}

// OpenAI applies long-context rates when a request exceeds 272K input tokens.
const longContextInputThreshold = 272_000

// defaultModel is the fallback when a model string doesn't match any rate entry.
const defaultModel = "gpt-5.4"

// lookupRate resolves a model string to its token rate.
// It strips explicit context-window suffixes and falls back to the default
// model rate for unrecognized gpt-* variants.
func lookupRate(model string) (tokenRate, bool) {
	m := strings.ToLower(llm.StripModelContextWindow(model))

	// Direct match first.
	if r, ok := modelRates[m]; ok {
		return r, true
	}

	if m == "" {
		return modelRates[defaultModel], true
	}

	// Any gpt-* variant we don't have specific rates for:
	// fall back to the default model rate so cost is never silently zero.
	if strings.HasPrefix(m, "gpt-") {
		return modelRates[defaultModel], true
	}

	return tokenRate{}, false
}
