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
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

const additionalSessionCostTimeout = 2 * time.Second

// sessionCost holds cost and usage data extracted from a session.
type sessionCost struct {
	TotalCostUSD float64
	Usage        llm.Usage
}

type additionalSessionCostProvider interface {
	AdditionalSessionCost(context.Context) (llm.SessionCostAdjustment, error)
}

// sessionCostMetadata identifies the single session behind an accounted cost.
type sessionCostMetadata struct {
	SessionID     string
	ObserverPhase string
	RepoName      string
	// RunNumber is the run that hosted the session; zero means the active run.
	RunNumber int
}

var errSessionRunNotActive = errors.New("session run is no longer active")

// sessionRunNumber returns the run a session was started in, or zero.
func sessionRunNumber(sess ports.SessionView) int {
	if s, ok := sess.(interface{ RunNumber() int }); ok {
		return s.RunNumber()
	}
	return 0
}

// resultInterruptedByStop reports whether the session's latest Result answers
// an Agentico-initiated stop.
func resultInterruptedByStop(sess ports.SessionView) bool {
	if s, ok := sess.(interface{ ResultInterruptedByStop() bool }); ok {
		return s.ResultInterruptedByStop()
	}
	return false
}

// extractSessionCost reads cost and accumulated usage from the session.
// Returns zero values if no data is available.
func extractSessionCost(sess ports.SessionView) sessionCost {
	if sess == nil {
		return sessionCost{}
	}
	sc := sessionCost{
		Usage: sess.AccumulatedUsage(),
	}
	sc.TotalCostUSD = sc.Usage.CostUSD
	if result := sess.Cost(); result != nil && sc.Usage.CostSource == "" {
		sc.TotalCostUSD = result.TotalCostUSD
	}
	if provider, ok := sess.(llm.SessionUsageReconciler); ok {
		ctx, cancel := context.WithTimeout(context.Background(), additionalSessionCostTimeout)
		usage := provider.ReconciledUsage(ctx)
		cancel()
		if usage != nil {
			sc.Usage = *usage
			sc.TotalCostUSD = usage.CostUSD
		}
	}
	if provider, ok := sess.(additionalSessionCostProvider); ok {
		ctx, cancel := context.WithTimeout(context.Background(), additionalSessionCostTimeout)
		defer cancel()
		adjustment, err := provider.AdditionalSessionCost(ctx)
		if err != nil {
			log.Printf("session cost: additional provider cost unavailable for %s: %v", sess.ID(), err)
		} else {
			applySessionCostAdjustment(&sc, adjustment)
		}
	}
	// Tokens without any reported price, e.g. a turn stopped before its
	// Result, must not read as a free session.
	if sc.TotalCostUSD == 0 && sc.Usage.CostSource == "" && sess.Cost() == nil && usageHasTokens(sc.Usage) {
		sc.Usage.CostSource = observe.CostSourceUnavailable
	}
	return sc
}

func usageHasTokens(u llm.Usage) bool {
	return u.InputTokens+u.OutputTokens+u.CacheReadInputTokens+u.CacheCreationInputTokens > 0
}

func applySessionCostAdjustment(cost *sessionCost, adjustment llm.SessionCostAdjustment) {
	if cost == nil {
		return
	}
	cost.TotalCostUSD += adjustment.TotalCostUSD
	cost.Usage.InputTokens += adjustment.Usage.InputTokens
	cost.Usage.OutputTokens += adjustment.Usage.OutputTokens
	cost.Usage.CacheReadInputTokens += adjustment.Usage.CacheReadInputTokens
	cost.Usage.CacheCreationInputTokens += adjustment.Usage.CacheCreationInputTokens
}

// toSessionUsage converts a SessionCost to an observe.SessionUsage.
func toSessionUsage(cost sessionCost, sessions ...ports.SessionView) observe.SessionUsage {
	usage := observe.SessionUsage{
		TotalCostUSD:             cost.TotalCostUSD,
		CostSource:               cost.Usage.CostSource,
		CostCreditsMicros:        cost.Usage.CostCreditsMicros,
		InputTokens:              cost.Usage.InputTokens,
		OutputTokens:             cost.Usage.OutputTokens,
		CacheReadInputTokens:     cost.Usage.CacheReadInputTokens,
		CacheCreationInputTokens: cost.Usage.CacheCreationInputTokens,
		ContextWindow:            cost.Usage.ContextWindow,
		ContextTotalTokens:       cost.Usage.ContextTotalTokens,
	}
	if len(sessions) == 0 || sessions[0] == nil {
		return usage
	}
	sess := sessions[0]
	usage.Provider = sess.ProviderName()
	usage.Model = sess.Model()
	usage.Effort = string(sess.EffectiveEffort())
	if result := sess.Cost(); result != nil {
		usage.ResultSubtype = result.Subtype
		usage.StopReason = result.StopReason
		usage.Truncated = result.IsTurnTruncated()
		usage.APIDurationMS = result.DurationAPI
		usage.Turns = result.NumTurns
		usage.Outcome = "success"
		if result.IsError || result.Subtype != "success" {
			usage.Outcome = "error"
			if resultInterruptedByStop(sess) {
				usage.Outcome = observe.SessionEndedStatusInterrupted
			}
		}
		if len(result.ModelUsage) > 0 {
			usage.PerModelContextWindows = make(map[string]int, len(result.ModelUsage))
			for model, entry := range result.ModelUsage {
				usage.PerModelContextWindows[model] = entry.ContextWindow
			}
		}
	}
	return usage
}

// accumulateSessionCostToFeature adds a session's cost to the latest active
// timing key on the feature, falling back to fallbackKey when no active key is
// recorded.
func accumulateSessionCostToFeature(store FeatureStore, featureID, fallbackKey string, cost sessionCost, metadata ...sessionCostMetadata) error {
	return accumulateSessionCost(store, featureID, fallbackKey, cost, true, firstSessionCostMetadata(metadata))
}

// accumulateSessionCostToFeatureKey adds a session's cost to key regardless of
// ActiveTimingKey. Use this for phases such as Final Review where the lifecycle
// phase can advance while the old timing key remains preserved for resume UI.
func accumulateSessionCostToFeatureKey(store FeatureStore, featureID, key string, cost sessionCost, metadata ...sessionCostMetadata) error {
	return accumulateSessionCost(store, featureID, key, cost, false, firstSessionCostMetadata(metadata))
}

func firstSessionCostMetadata(metadata []sessionCostMetadata) sessionCostMetadata {
	if len(metadata) == 0 {
		return sessionCostMetadata{}
	}
	return metadata[0]
}

func accumulateSessionCost(store FeatureStore, featureID, fallbackKey string, cost sessionCost, preferActiveKey bool, metadata sessionCostMetadata) error {
	record := feature.SessionCostRecord{
		SessionID:                metadata.SessionID,
		ObserverPhase:            metadata.ObserverPhase,
		RepoName:                 metadata.RepoName,
		CostUSD:                  cost.TotalCostUSD,
		CostSource:               cost.Usage.CostSource,
		CostCreditsMicros:        cost.Usage.CostCreditsMicros,
		InputTokens:              cost.Usage.InputTokens,
		OutputTokens:             cost.Usage.OutputTokens,
		CacheReadInputTokens:     cost.Usage.CacheReadInputTokens,
		CacheCreationInputTokens: cost.Usage.CacheCreationInputTokens,
	}
	if store == nil || strings.TrimSpace(featureID) == "" || !record.HasUsage() {
		return nil
	}
	return recordSessionCost(store, featureID, metadata.RunNumber, record, fallbackKey, preferActiveKey)
}

// recordSessionCost lands record in runNumber even when a rewind has since
// sealed that run and forked a new active one.
func recordSessionCost(store FeatureStore, featureID string, runNumber int, record feature.SessionCostRecord, fallbackKey string, preferActiveKey bool) error {
	phaseKey := func(activeKey string) string {
		if key := strings.TrimSpace(activeKey); preferActiveKey && key != "" {
			return key
		}
		return strings.TrimSpace(fallbackKey)
	}
	err := store.Modify(featureID, func(f *feature.Feature) error {
		if runNumber > 0 && f.ActiveRun != runNumber {
			return errSessionRunNotActive
		}
		record.PhaseKey = phaseKey(f.ActiveTimingKey)
		f.RecordSessionCost(record)
		return nil
	})
	if !errors.Is(err, errSessionRunNotActive) {
		return err
	}
	runs, ok := store.(sealedRunStore)
	if !ok {
		return nil
	}
	return runs.ModifySealedRun(featureID, runNumber, func(r *feature.Run) error {
		record.PhaseKey = phaseKey(r.ActiveTimingKey)
		r.RecordSessionCost(record)
		return nil
	})
}

// sessionErrFromStatus returns an error if the session ended in a failed state,
// nil otherwise. Used to map session status to the error parameter of
// observer.SessionEnded.
func sessionErrFromStatus(sess ports.SessionView) error {
	if sess == nil {
		return nil
	}
	if sess.Status() == ports.SessionFailed {
		detail := sess.ErrorDetail()
		if detail != "" {
			return fmt.Errorf("session failed: %s", detail)
		}
		return fmt.Errorf("session failed")
	}
	return nil
}
