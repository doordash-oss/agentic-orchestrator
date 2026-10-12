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

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"go.uber.org/fx"
)

const agenticRepoName = "agentic"

// observerFromModuleForTest builds an enabled JSONL observer through the fx
// module (the production construction path) with the given state dir and
// service name.
func observerFromModuleForTest(t *testing.T, stateDir, serviceName string) *observe.Observer {
	t.Helper()
	cfg := config.NewDefault()
	cfg.Observability.OTelServiceName = serviceName
	var obs *observe.Observer
	app := fx.New(
		fx.Supply(cfg, fx.Annotate(stateDir, fx.ResultTags(`name:"stateDir"`))),
		observe.Module,
		fx.Populate(&obs),
		fx.NopLogger,
	)
	if err := app.Err(); err != nil {
		t.Fatalf("build observer: %v", err)
	}
	return obs
}

// TestAttachDropReport_IncludesRunNumber verifies that attachDropObserver
// resolves ActiveRun via the feature store when emitting the
// session.critical_message_dropped event. This is the runtime guard that the
// generic Observer.Emit escape hatch carries run context.
func TestAttachDropReport_IncludesRunNumber(t *testing.T) {
	stateDir := t.TempDir()
	const featureID = "drop_run_feat"
	if err := os.MkdirAll(filepath.Join(stateDir, featureID), 0755); err != nil {
		t.Fatal(err)
	}

	obs := observerFromModuleForTest(t, stateDir, agenticRepoName)
	fs := feature.NewStore(stateDir)
	if err := fs.Save(&feature.Feature{ID: featureID, ActiveRun: 5, SchemaVersion: feature.SchemaVersionCurrent}); err != nil {
		t.Fatal(err)
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
