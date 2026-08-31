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

import "time"

// Event is the JSONL envelope for all observability events.
type Event struct {
	Timestamp    time.Time      `json:"timestamp"`
	TraceID      string         `json:"trace_id"`
	SpanID       string         `json:"span_id"`
	ParentSpanID string         `json:"parent_span_id,omitempty"`
	EventType    string         `json:"event_type"`
	Phase        string         `json:"phase,omitempty"`
	Status       string         `json:"status,omitempty"`
	FeatureID    string         `json:"feature_id"`
	SessionID    string         `json:"session_id,omitempty"`
	RepoName     string         `json:"repo_name,omitempty"`
	Iteration    int            `json:"iteration,omitempty"`
	DurationMs   int64          `json:"duration_ms,omitempty"`
	Error        string         `json:"error,omitempty"`
	Data         map[string]any `json:"data,omitempty"`
	// RunNumber is the 1-indexed attempt number of the feature's active run at
	// emit time. Zero (via omitempty-missing) means the event lacks run context
	// or was emitted from a code path that has no run context.
	RunNumber int `json:"run_number,omitempty"`
}
