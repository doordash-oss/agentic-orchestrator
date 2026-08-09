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
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

const agenticRepoName = "agentic"

// TestAttachDropReport_IncludesRunNumber verifies that attachDropObserver
// resolves ActiveRun via ports.FeatureStore when emitting the
// session.critical_message_dropped event. This is the runtime guard that the
// generic Observer.Emit escape hatch carries run context.
func TestAttachDropReport_IncludesRunNumber(t *testing.T) {
	stateDir := t.TempDir()
	const featureID = "drop_run_feat"
	if err := os.MkdirAll(filepath.Join(stateDir, featureID), 0755); err != nil {
		t.Fatal(err)
	}

	obs := observe.New(true, stateDir, false, "", false, agenticRepoName)
	fs := mocks.NewMockFeatureStore()
	fs.LoadFn = func(id string) (*feature.Feature, error) {
		if id != featureID {
			return nil, nil
		}
		return &feature.Feature{ID: id, ActiveRun: 5}, nil
	}

	reporter := newAttachDropObserver(obs, fs)
	if reporter == nil {
		t.Fatal("expected non-nil reporter when observer and store are non-nil")
	}

	reporter.ReportAttachDrop("sess-1", featureID, "implement", "agent.thinking", 5*time.Second)

	f, err := os.Open(filepath.Join(stateDir, featureID, "events.jsonl"))
	if err != nil {
		t.Fatalf("opening events.jsonl: %v", err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	var evt observe.Event
	if !scanner.Scan() {
		t.Fatalf("expected one event line, got none (err=%v)", scanner.Err())
	}
	if err := json.Unmarshal(scanner.Bytes(), &evt); err != nil {
		t.Fatalf("parsing event: %v", err)
	}
	if evt.EventType != "session.critical_message_dropped" {
		t.Errorf("EventType = %q, want session.critical_message_dropped", evt.EventType)
	}
	if evt.RunNumber != 5 {
		t.Errorf("RunNumber = %d, want 5", evt.RunNumber)
	}
	if evt.SessionID != "sess-1" {
		t.Errorf("SessionID = %q, want sess-1", evt.SessionID)
	}
	if evt.FeatureID != featureID {
		t.Errorf("FeatureID = %q, want %s", evt.FeatureID, featureID)
	}
}

// TestNewAttachDropObserver_NilStoreReturnsNil asserts the constructor
// degrades to nil when the feature store is nil, matching the "reporter is
// optional" contract.
func TestNewAttachDropObserver_NilStoreReturnsNil(t *testing.T) {
	obs := observe.New(true, t.TempDir(), false, "", false, agenticRepoName)
	if got := newAttachDropObserver(obs, nil); got != nil {
		t.Errorf("expected nil reporter when fs is nil, got %+v", got)
	}
}
