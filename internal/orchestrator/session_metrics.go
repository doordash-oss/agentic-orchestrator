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

package orchestrator

import (
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// attachDropObserver adapts *observe.Observer to session.AttachDropReporter.
// It keeps the session package free of an observe import while still
// letting us surface attachCh drop events to events.jsonl for
// dashboards / watchdogs.
//
// A nil observer is the no-op default — a drop on a session that has no
// observer wired (unit tests, recovery-only paths) simply logs via the
// existing log.Printf at the drop site and does not write an event.
type attachDropObserver struct {
	obs *observe.Observer
	fs  ports.FeatureStore // used to resolve ActiveRun at emit time
}

// newAttachDropObserver returns nil when either collaborator is nil so
// Manager.SetAttachDropReporter leaves the session untouched — preserving
// the "reporter is optional" contract. A nil feature store is treated the
// same as a nil observer because we cannot stamp the run number without it
// and surfacing the drop with RunNumber:0 post-Phase-4 would be misleading.
func newAttachDropObserver(obs *observe.Observer, fs ports.FeatureStore) *attachDropObserver {
	if obs == nil || fs == nil {
		return nil
	}
	return &attachDropObserver{obs: obs, fs: fs}
}

// ReportAttachDrop emits a session.critical_message_dropped event to the
// observer's JSONL stream. Errors are intentionally swallowed — the
// log.Printf at the drop site is the user-facing signal; this metric is
// strictly additive.
//
// The active run number is resolved at emit time via the feature store. A
// failed lookup (deleted feature, race) leaves RunNumber at zero and the
// event still emits, matching how today's drop metric degrades.
func (a *attachDropObserver) ReportAttachDrop(sessionID, featureID, phase, msgType string, timeout time.Duration) {
	if a == nil || a.obs == nil {
		return
	}
	activeRun := 0
	if a.fs != nil {
		if f, err := a.fs.Load(featureID); err == nil && f != nil {
			activeRun = f.ActiveRun
		}
	}
	_ = a.obs.Emit(observe.Event{
		Timestamp: time.Now(),
		EventType: "session.critical_message_dropped",
		FeatureID: featureID,
		SessionID: sessionID,
		Phase:     phase,
		RunNumber: activeRun,
		Data: map[string]any{
			"msg_type":   msgType,
			"timeout_ms": timeout.Milliseconds(),
		},
	})
}
