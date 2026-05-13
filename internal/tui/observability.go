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

package tui

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
)

// ObservabilityContext is the TUI-local trace context passed to observer
// adapters. Keeping this local avoids importing the concrete observe package
// into the fast TUI test package.
type ObservabilityContext struct {
	TraceID      string
	SpanID       string
	ParentSpanID string
	FeatureID    string
	FeatureName  string
	RunNumber    int
}

func (sc ObservabilityContext) WithRun(n int) ObservabilityContext {
	sc.RunNumber = n
	return sc
}

func (sc ObservabilityContext) Child() ObservabilityContext {
	return ObservabilityContext{
		TraceID:      sc.TraceID,
		SpanID:       newTUISpanID(),
		ParentSpanID: sc.SpanID,
		FeatureID:    sc.FeatureID,
		FeatureName:  sc.FeatureName,
		RunNumber:    sc.RunNumber,
	}
}

// Observer is the TUI-local observability sink.
type Observer interface {
	PermissionRequested(ObservabilityContext, string, string, int, string, string)
	PermissionResolved(ObservabilityContext, string, string, int, string, string)
	QuestionAsked(ObservabilityContext, string, string, int, string)
	QuestionAnswered(ObservabilityContext, string, string, int, string, string)
}

func spanContextForFeature(featureID, traceID, featureName, featureSpanID string) ObservabilityContext {
	if traceID == "" {
		if len(featureID) >= 32 {
			traceID = featureID[:32]
		} else {
			traceID = strings.Repeat("0", 32-len(featureID)) + featureID
		}
	}
	spanID := featureSpanID
	if spanID == "" {
		spanID = newTUISpanID()
	}
	return ObservabilityContext{
		TraceID:     traceID,
		SpanID:      spanID,
		FeatureID:   featureID,
		FeatureName: featureName,
	}
}

func newTUISpanID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
