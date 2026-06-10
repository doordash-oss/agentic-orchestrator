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
		model   string
		wantOK  bool
		wantIn  float64
		wantOut float64
	}{
		// Direct matches
		{"gpt-5.4", true, 2.50, 15.00},
		{"gpt-5.4[1M]", true, 2.50, 15.00},
		{"gpt-5.4-mini", true, 0.75, 4.50},
		{"gpt-5.4-mini[1M]", true, 0.75, 4.50},
		{"gpt-5.3-codex", true, 1.75, 14.00},

		// Empty model uses the default model rate.
		{"", true, 2.50, 15.00},

		// Unknown gpt-* variants fall back to default
		{"gpt-5.5", true, 2.50, 15.00},
		{"gpt-future-model", true, 2.50, 15.00},

		// Case insensitive
		{"GPT-5.4", true, 2.50, 15.00},
		// Non-matching models
		{"codex", false, 0, 0},
		{"opus", false, 0, 0},
		{"sonnet", false, 0, 0},
		{"unknown", false, 0, 0},
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
			if math.Abs(r.outputPerMToken-tt.wantOut) > 0.001 {
				t.Errorf("outputPerMToken = %f, want %f", r.outputPerMToken, tt.wantOut)
			}
		})
	}
}

func TestComputeCost(t *testing.T) {
	// 1M input + 1M output tokens at gpt-5.4 rates = $2.50 + $15.00 = $17.50
	cost := computeCost("gpt-5.4", 1_000_000, 1_000_000)
	if math.Abs(cost-17.50) > 0.001 {
		t.Errorf("computeCost(gpt-5.4, 1M, 1M) = %f, want 17.50", cost)
	}

	// Empty model should also work (default)
	costEmpty := computeCost("", 1_000_000, 1_000_000)
	if math.Abs(costEmpty-17.50) > 0.001 {
		t.Errorf("computeCost('', 1M, 1M) = %f, want 17.50", costEmpty)
	}

	// Unknown non-gpt model returns 0
	costUnknown := computeCost("opus", 1_000_000, 1_000_000)
	if costUnknown != 0 {
		t.Errorf("computeCost(opus, 1M, 1M) = %f, want 0", costUnknown)
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
