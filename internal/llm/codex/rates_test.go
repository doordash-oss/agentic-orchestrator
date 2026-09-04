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
	"math"
	"testing"
)

func TestLookupRate(t *testing.T) {
	tests := []struct {
		model          string
		wantOK         bool
		wantIn         float64
		wantCached     float64
		wantWrite      float64
		wantOut        float64
		wantLongIn     float64
		wantLongCached float64
		wantLongWrite  float64
		wantLongOut    float64
	}{
		{"gpt-6-astra", true, 10, 1, 12.5, 50, 20, 2, 25, 75},
		{"GPT-6-ASTRA[872K]", true, 10, 1, 12.5, 50, 20, 2, 25, 75},
		// GPT-5.6 family, including the generic alias.
		{"gpt-5.6", true, 4.00, 0.40, 5.00, 20.00, 8.00, 0.80, 10.00, 30.00},
		{"gpt-5.6[1M]", true, 4.00, 0.40, 5.00, 20.00, 8.00, 0.80, 10.00, 30.00},
		{"gpt-5.6-sol", true, 4.00, 0.40, 5.00, 20.00, 8.00, 0.80, 10.00, 30.00},
		{"gpt-5.6-terra", true, 2.00, 0.20, 2.50, 12.00, 4.00, 0.40, 5.00, 18.00},
		{"gpt-5.6-luna", true, 0.20, 0.02, 0.25, 1.20, 0.40, 0.04, 0.50, 1.80},

		// Direct matches
		{"gpt-5.5", true, 5.00, 0.50, 0, 30.00, 10.00, 1.00, 0, 45.00},
		{"gpt-5.5[1M]", true, 5.00, 0.50, 0, 30.00, 10.00, 1.00, 0, 45.00},
		{"gpt-5.4", true, 2.50, 0.25, 0, 15.00, 5.00, 0.50, 0, 22.50},
		{"gpt-5.4[1M]", true, 2.50, 0.25, 0, 15.00, 5.00, 0.50, 0, 22.50},
		{"gpt-5.4-mini", true, 0.75, 0.075, 0, 4.50, 0, 0, 0, 0},
		{"gpt-5.4-mini[1M]", true, 0.75, 0.075, 0, 4.50, 0, 0, 0, 0},
		{"gpt-5.3-codex", true, 1.75, 0.175, 0, 14.00, 0, 0, 0, 0},

		// Empty model uses the default model rate.
		{"", true, 2.50, 0.25, 0, 15.00, 5.00, 0.50, 0, 22.50},

		// Unknown models must not inherit an unrelated model's price.
		{"gpt-future-model", false, 0, 0, 0, 0, 0, 0, 0, 0},

		// Case insensitive
		{"GPT-5.6-LUNA", true, 0.20, 0.02, 0.25, 1.20, 0.40, 0.04, 0.50, 1.80},
		// Non-matching models
		{"codex", false, 0, 0, 0, 0, 0, 0, 0, 0},
		{"opus", false, 0, 0, 0, 0, 0, 0, 0, 0},
		{"sonnet", false, 0, 0, 0, 0, 0, 0, 0, 0},
		{"unknown", false, 0, 0, 0, 0, 0, 0, 0, 0},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			r, ok := lookupRate(tt.model)
			if ok != tt.wantOK {
				t.Fatalf("lookupRate(%q) ok = %v, want %v", tt.model, ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if math.Abs(r.inputPerMToken-tt.wantIn) > 0.001 {
				t.Errorf("inputPerMToken = %f, want %f", r.inputPerMToken, tt.wantIn)
			}
			if math.Abs(r.cachedInputPerMToken-tt.wantCached) > 0.001 {
				t.Errorf("cachedInputPerMToken = %f, want %f", r.cachedInputPerMToken, tt.wantCached)
			}
			if math.Abs(r.cacheWritePerMToken-tt.wantWrite) > 0.001 {
				t.Errorf("cacheWritePerMToken = %f, want %f", r.cacheWritePerMToken, tt.wantWrite)
			}
			if math.Abs(r.outputPerMToken-tt.wantOut) > 0.001 {
				t.Errorf("outputPerMToken = %f, want %f", r.outputPerMToken, tt.wantOut)
			}
			if math.Abs(r.longInputPerMToken-tt.wantLongIn) > 0.001 {
				t.Errorf("longInputPerMToken = %f, want %f", r.longInputPerMToken, tt.wantLongIn)
			}
			if math.Abs(r.longCachedInputPerMToken-tt.wantLongCached) > 0.001 {
				t.Errorf("longCachedInputPerMToken = %f, want %f", r.longCachedInputPerMToken, tt.wantLongCached)
			}
			if math.Abs(r.longCacheWritePerMToken-tt.wantLongWrite) > 0.001 {
				t.Errorf("longCacheWritePerMToken = %f, want %f", r.longCacheWritePerMToken, tt.wantLongWrite)
			}
			if math.Abs(r.longOutputPerMToken-tt.wantLongOut) > 0.001 {
				t.Errorf("longOutputPerMToken = %f, want %f", r.longOutputPerMToken, tt.wantLongOut)
			}
		})
	}
}

func TestComputeCost(t *testing.T) {
	// 1M input + 1M output tokens use gpt-5.4 long-context rates.
	cost := computeCost("gpt-5.4", 1_000_000, 0, 0, 1_000_000)
	if math.Abs(cost-27.50) > 0.001 {
		t.Errorf("computeCost(gpt-5.4, 1M, 1M) = %f, want 27.50", cost)
	}

	// 600K uncached input + 400K cached input + 100K output at gpt-5.5 rates =
	// $6.00 + $0.40 + $4.50 = $10.90 at long-context rates.
	costCached := computeCost("gpt-5.5", 1_000_000, 400_000, 0, 100_000)
	if math.Abs(costCached-10.90) > 0.001 {
		t.Errorf("computeCost(gpt-5.5, 1M, 400K, 100K) = %f, want 10.90", costCached)
	}

	// Cached input is bounded by total input, matching the billing model that
	// treats cached tokens as a discounted subset of input tokens.
	costClamped := computeCost("gpt-5.5", 100_000, 200_000, 0, 0)
	if math.Abs(costClamped-0.05) > 0.001 {
		t.Errorf("computeCost(gpt-5.5, 100K, 200K, 0) = %f, want 0.05", costClamped)
	}

	// Empty model should also work (default)
	costEmpty := computeCost("", 1_000_000, 0, 0, 1_000_000)
	if math.Abs(costEmpty-27.50) > 0.001 {
		t.Errorf("computeCost('', 1M, 1M) = %f, want 27.50", costEmpty)
	}

	// Unknown non-gpt model returns 0
	costUnknown := computeCost("opus", 1_000_000, 0, 0, 1_000_000)
	if costUnknown != 0 {
		t.Errorf("computeCost(opus, 1M, 1M) = %f, want 0", costUnknown)
	}

	// Cache reads and writes replace full-price input tokens.
	costCacheWrite := computeCost("gpt-5.6-luna", 200_000, 50_000, 50_000, 10_000)
	if math.Abs(costCacheWrite-0.0455) > 0.001 {
		t.Errorf("computeCost(gpt-5.6-luna, cache write) = %f, want 0.0455", costCacheWrite)
	}
	// Models without separate cache-write pricing keep those tokens at the
	// ordinary input rate instead of treating them as free.
	costNoWriteRate := computeCost("gpt-5.5", 100_000, 0, 50_000, 0)
	if math.Abs(costNoWriteRate-0.50) > 0.001 {
		t.Errorf("computeCost(gpt-5.5, unsupported cache write) = %f, want 0.50", costNoWriteRate)
	}

	// The threshold itself remains short-context pricing; only larger requests
	// use the 2x input and 1.5x output rates.
	costAtThreshold := computeCost("gpt-5.6-luna", 272_000, 0, 0, 1_000)
	if math.Abs(costAtThreshold-0.0556) > 0.001 {
		t.Errorf("computeCost(gpt-5.6-luna, threshold) = %f, want 0.0556", costAtThreshold)
	}
	costAboveThreshold := computeCost("gpt-5.6-luna", 273_000, 0, 0, 1_000)
	if math.Abs(costAboveThreshold-0.111) > 0.001 {
		t.Errorf("computeCost(gpt-5.6-luna, long context) = %f, want 0.111", costAboveThreshold)
	}
}

func TestProviderComputeCost(t *testing.T) {
	p := &Provider{}

	// The provider name is not a model alias.
	cost := p.ComputeCost("codex", 1_000_000, 1_000_000)
	if cost != 0 {
		t.Errorf("Provider.ComputeCost(codex, 1M, 1M) = %f, want 0", cost)
	}

	// gpt-5.4-mini rates
	costMini := p.ComputeCost("gpt-5.4-mini", 1_000_000, 1_000_000)
	expected := 0.75 + 4.50
	if math.Abs(costMini-expected) > 0.001 {
		t.Errorf("Provider.ComputeCost(gpt-5.4-mini, 1M, 1M) = %f, want %f", costMini, expected)
	}
}

func TestAstraCostBoundaryAndServiceTiers(t *testing.T) {
	for _, tc := range []struct {
		tier  string
		input int
		want  float64
	}{
		{"default", 272_000, 2.77},
		{"default", 273_000, 5.535},
		{"fast", 273_000, 11.07},
		{"priority", 273_000, 11.07},
		{"flex", 273_000, 2.7675},
	} {
		got := computeCostForServiceTier("gpt-6-astra[872K]", tc.tier, tc.input, 0, 0, 1_000, tc.input)
		if math.Abs(got-tc.want) > 0.00000001 {
			t.Errorf("%s/%d = %v, want %v", tc.tier, tc.input, got, tc.want)
		}
	}
}
