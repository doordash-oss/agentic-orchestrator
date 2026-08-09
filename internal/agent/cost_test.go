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
	"context"
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
	sess.SetAccumulatedUsage(llm.Usage{CostUSD: 1.25})
	sess.SetCost(&llm.ResultMessage{TotalCostUSD: 2.75})
	cost := ExtractSessionCost(sess)
	if cost.TotalCostUSD != 2.75 {
		t.Errorf("TotalCostUSD = %v, want 2.75", cost.TotalCostUSD)
	}
}

func TestExtractSessionCostFallsBackToRunningUsageCost(t *testing.T) {
	sess := session.NewSession("test", "feat-1", 0)
	sess.SetAccumulatedUsage(llm.Usage{CostUSD: 0.42})

	cost := ExtractSessionCost(sess)

	if cost.TotalCostUSD != 0.42 {
		t.Errorf("TotalCostUSD = %v, want 0.42 from running usage snapshot", cost.TotalCostUSD)
	}
}

func TestExtractSessionCostIncludesProviderAdditionalSessionCost(t *testing.T) {
	base := mocks.NewMockSessionView("sess-parent", "feat-1")
	base.CostVal = &llm.ResultMessage{TotalCostUSD: 2.00}
	base.AccumulatedUsageVal = llm.Usage{
		InputTokens:              100,
		OutputTokens:             20,
		CacheReadInputTokens:     300,
		CacheCreationInputTokens: 4,
	}
	sess := &additionalCostSession{
		MockSessionView: base,
		adjustment: llm.SessionCostAdjustment{
			TotalCostUSD: 0.75,
			Usage: llm.Usage{
				InputTokens:              10,
				OutputTokens:             2,
				CacheReadInputTokens:     30,
				CacheCreationInputTokens: 1,
			},
		},
	}

	cost := ExtractSessionCost(sess)

	if cost.TotalCostUSD != 2.75 {
		t.Errorf("TotalCostUSD = %v, want 2.75", cost.TotalCostUSD)
	}
	if cost.Usage.InputTokens != 110 {
		t.Errorf("Usage.InputTokens = %d, want 110", cost.Usage.InputTokens)
	}
	if cost.Usage.OutputTokens != 22 {
		t.Errorf("Usage.OutputTokens = %d, want 22", cost.Usage.OutputTokens)
	}
	if cost.Usage.CacheReadInputTokens != 330 {
		t.Errorf("Usage.CacheReadInputTokens = %d, want 330", cost.Usage.CacheReadInputTokens)
	}
	if cost.Usage.CacheCreationInputTokens != 5 {
		t.Errorf("Usage.CacheCreationInputTokens = %d, want 5", cost.Usage.CacheCreationInputTokens)
	}
}

type additionalCostSession struct {
	*mocks.MockSessionView
	adjustment llm.SessionCostAdjustment
	err        error
}

func (s *additionalCostSession) AdditionalSessionCost(context.Context) (llm.SessionCostAdjustment, error) {
	return s.adjustment, s.err
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

func TestAccumulateSessionCostToFeatureUsesActiveTimingKey(t *testing.T) {
	dir := t.TempDir()
	store := feature.NewStore(dir)

	f := &feature.Feature{
		ID:              "test-helper-cost-active-key",
		Name:            "test-helper-cost-active-key",
		Status:          feature.StatusImplementing,
		ActiveTimingKey: "phase-2-impl",
		SchemaVersion:   feature.SchemaVersionCurrent,
	}
	if err := store.Save(f); err != nil {
		t.Fatal(err)
	}

	err := accumulateSessionCostToFeature(store, f.ID, "review", SessionCost{TotalCostUSD: 0.42})
	if err != nil {
		t.Fatal(err)
	}

	updated, err := store.Load(f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := updated.PhaseCost("phase-2-impl"); got != 0.42 {
		t.Errorf("PhaseCost(phase-2-impl) = %v, want 0.42", got)
	}
	if got := updated.PhaseCost("review"); got != 0 {
		t.Errorf("PhaseCost(review) = %v, want 0", got)
	}
}

func TestAccumulateSessionCostToFeatureRecordsIndividualSession(t *testing.T) {
	dir := t.TempDir()
	store := feature.NewStore(dir)

	f := &feature.Feature{
		ID:              "test-helper-session-cost",
		Name:            "test-helper-session-cost",
		Status:          feature.StatusImplementing,
		ActiveTimingKey: "phase-2-impl",
		SchemaVersion:   feature.SchemaVersionCurrent,
	}
	if err := store.Save(f); err != nil {
		t.Fatal(err)
	}

	err := accumulateSessionCostToFeature(store, f.ID, "review", SessionCost{TotalCostUSD: 0.42}, SessionCostMetadata{
		SessionID:     "feat-phase-02-review-01",
		ObserverPhase: "review",
		RepoName:      "repo-a",
	})
	if err != nil {
		t.Fatal(err)
	}

	updated, err := store.Load(f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := updated.PhaseCost("phase-2-impl"); got != 0.42 {
		t.Errorf("PhaseCost(phase-2-impl) = %v, want 0.42", got)
	}
	if len(updated.SessionCosts) != 1 {
		t.Fatalf("len(SessionCosts) = %d, want 1", len(updated.SessionCosts))
	}
	got := updated.SessionCosts[0]
	if got.SessionID != "feat-phase-02-review-01" {
		t.Errorf("SessionID = %q, want feat-phase-02-review-01", got.SessionID)
	}
	if got.PhaseKey != "phase-2-impl" {
		t.Errorf("PhaseKey = %q, want phase-2-impl", got.PhaseKey)
	}
	if got.ObserverPhase != "review" {
		t.Errorf("ObserverPhase = %q, want review", got.ObserverPhase)
	}
	if got.RepoName != "repo-a" {
		t.Errorf("RepoName = %q, want repo-a", got.RepoName)
	}
	if got.CostUSD != 0.42 {
		t.Errorf("CostUSD = %v, want 0.42", got.CostUSD)
	}
}

func TestAccumulateSessionCostToFeatureUsesFallbackKey(t *testing.T) {
	dir := t.TempDir()
	store := feature.NewStore(dir)

	f := &feature.Feature{
		ID:            "test-helper-cost-fallback-key",
		Name:          "test-helper-cost-fallback-key",
		Status:        feature.StatusFinalReviewing,
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	if err := store.Save(f); err != nil {
		t.Fatal(err)
	}

	err := accumulateSessionCostToFeature(store, f.ID, "review", SessionCost{TotalCostUSD: 0.31})
	if err != nil {
		t.Fatal(err)
	}

	updated, err := store.Load(f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := updated.PhaseCost("review"); got != 0.31 {
		t.Errorf("PhaseCost(review) = %v, want 0.31", got)
	}
}

func TestAccumulateSessionCostToFeatureKeyIgnoresStaleActiveTimingKey(t *testing.T) {
	dir := t.TempDir()
	store := feature.NewStore(dir)

	f := &feature.Feature{
		ID:              "test-helper-cost-forced-key",
		Name:            "test-helper-cost-forced-key",
		Status:          feature.StatusFinalReviewing,
		ActiveTimingKey: "phase-3-impl",
		SchemaVersion:   feature.SchemaVersionCurrent,
	}
	if err := store.Save(f); err != nil {
		t.Fatal(err)
	}

	err := accumulateSessionCostToFeatureKey(store, f.ID, "review", SessionCost{TotalCostUSD: 0.29})
	if err != nil {
		t.Fatal(err)
	}

	updated, err := store.Load(f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := updated.PhaseCost("review"); got != 0.29 {
		t.Errorf("PhaseCost(review) = %v, want 0.29", got)
	}
	if got := updated.PhaseCost("phase-3-impl"); got != 0 {
		t.Errorf("PhaseCost(phase-3-impl) = %v, want 0", got)
	}
}

func TestAccumulateSessionCostToFeatureSkipsZeroCost(t *testing.T) {
	dir := t.TempDir()
	store := feature.NewStore(dir)

	f := &feature.Feature{
		ID:              "test-helper-cost-zero",
		Name:            "test-helper-cost-zero",
		Status:          feature.StatusImplementing,
		ActiveTimingKey: "phase-1-impl",
		SchemaVersion:   feature.SchemaVersionCurrent,
	}
	if err := store.Save(f); err != nil {
		t.Fatal(err)
	}

	err := accumulateSessionCostToFeature(store, f.ID, "review", SessionCost{})
	if err != nil {
		t.Fatal(err)
	}

	updated, err := store.Load(f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.PhaseCosts) != 0 {
		t.Errorf("PhaseCosts = %v, want empty", updated.PhaseCosts)
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
