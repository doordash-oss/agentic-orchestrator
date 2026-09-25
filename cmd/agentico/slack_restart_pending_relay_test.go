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

package main

import (
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	serverruntime "github.com/doordash-oss/agentic-orchestrator/internal/server"
)

func TestSlackRestartPendingRelayNotReadyUntilBound(t *testing.T) {
	relay := &slackPendingInputRelay{}
	if got, err := relay.PendingSlackInputs("feature-1"); err == nil {
		t.Fatalf("unbound pending source = %#v, nil; want not-ready error", got)
	}
	store := feature.NewStore(t.TempDir())
	if err := store.Save(&feature.Feature{
		ID: "feature-1", SchemaVersion: feature.SchemaVersionCurrent,
		HelpQueue: []feature.HelpRequest{{
			Question: "What next?", Time: time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC),
			Pending: true,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	serverruntime.NewHandler(serverruntime.HandlerOptions{
		FeatureStore: store, BindSlackPendingInputSource: relay.bind,
	})
	got, err := relay.PendingSlackInputs("feature-1")
	if err != nil || len(got) != 1 || got[0].HelpQuestion != "What next?" ||
		got[0].Kind != ports.SlackPendingHelp {
		t.Fatalf("bound pending source = %#v, %v; want handler result", got, err)
	}
}
