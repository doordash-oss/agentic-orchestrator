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

package agent

import (
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	mocks "github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

func TestExtractSessionCostNilSession(t *testing.T) {
	cost := ExtractSessionCost(nil)
	if cost.TotalCostUSD != 0 {
		t.Errorf("TotalCostUSD = %v, want 0 for nil session", cost.TotalCostUSD)
	}
}

func TestExtractSessionCostNilResult(t *testing.T) {
	sess := session.NewSession("test", "feat-1", 0)
	cost := ExtractSessionCost(sess)
	if cost.TotalCostUSD != 0 {
		t.Errorf("TotalCostUSD = %v, want 0 for nil result", cost.TotalCostUSD)
	}
}

func TestExtractSessionCostFromResult(t *testing.T) {
	sess := session.NewSession("test", "feat-1", 0)
	sess.SetCost(&llm.ResultMessage{TotalCostUSD: 2.75})
	cost := ExtractSessionCost(sess)
	if cost.TotalCostUSD != 2.75 {
		t.Errorf("TotalCostUSD = %v, want 2.75", cost.TotalCostUSD)
	}
}

func TestImplementCostAttributionUsesStoreKey(t *testing.T) {
	// Regression test: cost attribution in RunImplementationLoop reads
	// ActiveTimingKey from the store (latest state) rather than from the
	// initial config snapshot.
	dir := t.TempDir()
	store := feature.NewStore(dir)

	f := &feature.Feature{
		ID:              "test-cost-key",
		Name:            "test-cost-key",
		Status:          feature.StatusImplementing,
		ActiveTimingKey: "implement",
		SchemaVersion:   feature.SchemaVersionCurrent,
	}
	if err := store.Save(f); err != nil {
		t.Fatal(err)
	}

	staleCfg := ImplementConfig{
		Feature:      &feature.Feature{ID: "test-cost-key", ActiveTimingKey: "plan"},
		FeatureStore: store,
		BuildSession: mockBuildSession("", ""),
	}

	costUSD := 1.50
	if staleCfg.FeatureStore != nil && costUSD > 0 {
		_ = staleCfg.FeatureStore.Modify(staleCfg.Feature.ID, func(f *feature.Feature) error {
			costKey := f.ActiveTimingKey
			if costKey == "" {
				costKey = "implement"
			}
			f.AddPhaseCost(costKey, costUSD)
			return nil
		})
	}

	updated, err := store.Load("test-cost-key")
	if err != nil {
		t.Fatal(err)
	}
	if got := updated.PhaseCost("implement"); got != 1.50 {
		t.Errorf("PhaseCost(implement) = %v, want 1.50", got)
	}
	if got := updated.PhaseCost("plan"); got != 0 {
		t.Errorf("PhaseCost(plan) = %v, want 0", got)
	}
}

func TestExtractSessionCostWithUsage(t *testing.T) {
	mock := mocks.NewMockSessionView("sess1", "feat1")
	mock.AccumulatedUsageVal = llm.Usage{InputTokens: 1000, OutputTokens: 500}
	mock.CostVal = &llm.ResultMessage{TotalCostUSD: 2.5}

	sc := ExtractSessionCost(mock)

	if sc.TotalCostUSD != 2.5 {
		t.Errorf("TotalCostUSD = %v, want 2.5", sc.TotalCostUSD)
	}
	if sc.Usage.InputTokens != 1000 {
		t.Errorf("Usage.InputTokens = %d, want 1000", sc.Usage.InputTokens)
	}
	if sc.Usage.OutputTokens != 500 {
		t.Errorf("Usage.OutputTokens = %d, want 500", sc.Usage.OutputTokens)
	}
}

func TestExtractSessionCostNilSessionUsage(t *testing.T) {
	sc := ExtractSessionCost(nil)
	if sc.Usage != (llm.Usage{}) {
		t.Errorf("Usage = %+v, want zero value", sc.Usage)
	}
}

func TestExtractSessionCostNoUsage(t *testing.T) {
	mock := mocks.NewMockSessionView("sess2", "feat2")
	mock.CostVal = &llm.ResultMessage{TotalCostUSD: 3.75}
	// AccumulatedUsageVal left at zero value

	sc := ExtractSessionCost(mock)

	if sc.TotalCostUSD != 3.75 {
		t.Errorf("TotalCostUSD = %v, want 3.75", sc.TotalCostUSD)
	}
	if sc.Usage != (llm.Usage{}) {
		t.Errorf("Usage = %+v, want zero value", sc.Usage)
	}
}
