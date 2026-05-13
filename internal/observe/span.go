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

package observe

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
)

// SpanContext is a plain value struct threaded through lifecycle boundaries.
// TraceID is feature-scoped (32 hex chars). SpanID is per-boundary (16 hex chars).
type SpanContext struct {
	TraceID      string
	SpanID       string
	ParentSpanID string
	FeatureID    string
	FeatureName  string
	// RunNumber is the 1-indexed attempt number of the feature's active run.
	// Zero means "unknown/unset" (e.g. pre-Phase-4 callers, tests that do not
	// opt in). Stamped onto every emitted Event so events.jsonl can be
	// filtered per run downstream.
	RunNumber int
}

// WithRun returns a copy of sc with RunNumber set. Primary production call
// sites chain it after SpanContextForFeature:
//
//	sc := observe.SpanContextForFeature(f.ID, f.TraceID, f.Name, f.FeatureSpanID).WithRun(f.ActiveRun)
//
// Tests that don't care about run numbering omit the chain and get zero.
func (sc SpanContext) WithRun(n int) SpanContext {
	sc.RunNumber = n
	return sc
}

// NewSpanID generates a random 16-char hex span ID.
func NewSpanID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// SpanContextForFeature creates a root SpanContext for a feature.
// If traceID is empty, derives one by zero-padding the featureID to 32 chars.
// If featureSpanID is non-empty, it is reused as the SpanID so that all callers
// produce children of the same feature-level span (the one started by FeatureStarted).
func SpanContextForFeature(featureID, traceID, featureName, featureSpanID string) SpanContext {
	if traceID == "" {
		if len(featureID) >= 32 {
			traceID = featureID[:32]
		} else {
			traceID = strings.Repeat("0", 32-len(featureID)) + featureID
		}
	}
	spanID := featureSpanID
	if spanID == "" {
		spanID = NewSpanID()
	}
	return SpanContext{
		TraceID:     traceID,
		SpanID:      spanID,
		FeatureID:   featureID,
		FeatureName: featureName,
	}
}

// Child creates a child SpanContext with a new SpanID and this context's SpanID as parent.
func (sc SpanContext) Child() SpanContext {
	return SpanContext{
		TraceID:      sc.TraceID,
		SpanID:       NewSpanID(),
		ParentSpanID: sc.SpanID,
		FeatureID:    sc.FeatureID,
		FeatureName:  sc.FeatureName,
		RunNumber:    sc.RunNumber,
	}
}
