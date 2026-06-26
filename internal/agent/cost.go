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
	"fmt"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// SessionCost holds cost and usage data extracted from a session.
type SessionCost struct {
	TotalCostUSD float64
	Usage        llm.Usage
}

// ExtractSessionCost reads cost and accumulated usage from the session.
// Returns zero values if no data is available.
func ExtractSessionCost(sess ports.SessionView) SessionCost {
	if sess == nil {
		return SessionCost{}
	}
	sc := SessionCost{
		Usage: sess.AccumulatedUsage(),
	}
	if sess.Cost() != nil {
		sc.TotalCostUSD = sess.Cost().TotalCostUSD
	}
	return sc
}

// toSessionUsage converts a SessionCost to an observe.SessionUsage.
func toSessionUsage(cost SessionCost) observe.SessionUsage {
	return observe.SessionUsage{
		TotalCostUSD:             cost.TotalCostUSD,
		InputTokens:              cost.Usage.InputTokens,
		OutputTokens:             cost.Usage.OutputTokens,
		CacheReadInputTokens:     cost.Usage.CacheReadInputTokens,
		CacheCreationInputTokens: cost.Usage.CacheCreationInputTokens,
	}
}

// accumulateSessionCostToFeature adds a session's cost to the latest active
// timing key on the feature, falling back to fallbackKey when no active key is
// recorded.
func accumulateSessionCostToFeature(store ports.FeatureStore, featureID, fallbackKey string, cost SessionCost) error {
	return accumulateSessionCost(store, featureID, fallbackKey, cost, true)
}

// accumulateSessionCostToFeatureKey adds a session's cost to key regardless of
// ActiveTimingKey. Use this for phases such as Final Review where the lifecycle
// phase can advance while the old timing key remains preserved for resume UI.
func accumulateSessionCostToFeatureKey(store ports.FeatureStore, featureID, key string, cost SessionCost) error {
	return accumulateSessionCost(store, featureID, key, cost, false)
}

func accumulateSessionCost(store ports.FeatureStore, featureID, fallbackKey string, cost SessionCost, preferActiveKey bool) error {
	if store == nil || strings.TrimSpace(featureID) == "" || cost.TotalCostUSD <= 0 {
		return nil
	}
	return store.Modify(featureID, func(f *feature.Feature) error {
		costKey := ""
		if preferActiveKey {
			costKey = strings.TrimSpace(f.ActiveTimingKey)
		}
		if costKey == "" {
			costKey = strings.TrimSpace(fallbackKey)
		}
		if costKey == "" {
			return nil
		}
		f.AddPhaseCost(costKey, cost.TotalCostUSD)
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
