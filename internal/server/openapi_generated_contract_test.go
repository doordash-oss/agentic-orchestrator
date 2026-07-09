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

package server

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/server/serverapi"
)

func TestGeneratedOpenAPIHardeningDTOsMatchServerWireJSON(t *testing.T) {
	at := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	seq := uint64(42)
	resourceVersion := uint64(7)
	epoch := "epoch-1"
	revision := "rev-1"
	summary := "feature updated"
	featureID := "F-42"
	resourceID := "sess-1"
	phase := "implement"
	meta := ResponseMeta{Revision: revision, GeneratedAt: at, AsOfSeq: seq}

	assertSameJSON(t, "response meta",
		meta,
		serverapi.ResponseMeta{Revision: revision, GeneratedAt: at, AsOfSeq: seq},
	)
	assertSameJSON(t, "event envelope",
		SSEEventDTO{
			APIVersion:       APIVersion,
			ID:               "42",
			Seq:              seq,
			Epoch:            epoch,
			Kind:             "session.updated",
			At:               at,
			Resource:         ResourceDTO{Type: "session", ID: resourceID, FeatureID: featureID, Phase: phase},
			ResourceVersion:  resourceVersion,
			Revision:         revision,
			SnapshotRequired: true,
			Summary:          summary,
		},
		serverapi.SSEEvent{
			APIVersion:       APIVersion,
			ID:               "42",
			Seq:              &seq,
			Epoch:            &epoch,
			Kind:             "session.updated",
			At:               at,
			Resource:         serverapi.Resource{Type: "session", ID: &resourceID, FeatureID: &featureID, Phase: &phase},
			ResourceVersion:  &resourceVersion,
			Revision:         &revision,
			SnapshotRequired: true,
			Summary:          &summary,
		},
	)
	assertSameJSON(t, "minimal event envelope",
		SSEEventDTO{
			APIVersion:       APIVersion,
			ID:               "0",
			Kind:             "heartbeat",
			At:               at,
			Resource:         ResourceDTO{Type: "runtime"},
			SnapshotRequired: false,
		},
		serverapi.SSEEvent{
			APIVersion:       APIVersion,
			ID:               "0",
			Kind:             "heartbeat",
			At:               at,
			Resource:         serverapi.Resource{Type: "runtime"},
			SnapshotRequired: false,
		},
	)
	assertSameJSON(t, "session output",
		SessionOutputResponse{
			APIVersion: APIVersion,
			Meta:       meta,
			SessionID:  resourceID,
			Offset:     10,
			NextOffset: 18,
			Size:       18,
			Data:       "chunk",
			Truncated:  false,
			Done:       true,
		},
		serverapi.SessionOutputResponse{
			APIVersion: APIVersion,
			Meta:       &serverapi.ResponseMeta{Revision: revision, GeneratedAt: at, AsOfSeq: seq},
			SessionID:  resourceID,
			Offset:     10,
			NextOffset: 18,
			Size:       18,
			Data:       "chunk",
			Truncated:  false,
			Done:       true,
		},
	)
}

func TestGeneratedOpenAPIParamsMatchClientQueryNames(t *testing.T) {
	transcriptParams := reflect.TypeOf(serverapi.GetSessionTranscriptParams{})
	if _, ok := transcriptParams.FieldByName("Offset"); !ok {
		t.Fatal("generated transcript params missing Offset; spec must match the server/client offset query")
	}
	if _, ok := transcriptParams.FieldByName("Cursor"); ok {
		t.Fatal("generated transcript params still expose retired Cursor query")
	}

	var limit serverapi.Limit = 25
	_ = serverapi.StreamEventsParams{After: uint64Ptr(42), Epoch: stringPtr("epoch-1"), HeartbeatMs: intPtr(1000)}
	_ = serverapi.GetSessionOutputParams{From: int64Ptr(10), Limit: &limit}
	_ = serverapi.StreamSessionOutputParams{From: int64Ptr(18)}
}

func assertSameJSON(t *testing.T, name string, serverValue any, generatedValue any) {
	t.Helper()
	serverJSON, err := json.Marshal(serverValue)
	if err != nil {
		t.Fatalf("%s: marshal server value: %v", name, err)
	}
	generatedJSON, err := json.Marshal(generatedValue)
	if err != nil {
		t.Fatalf("%s: marshal generated value: %v", name, err)
	}
	var serverDecoded any
	if err := json.Unmarshal(serverJSON, &serverDecoded); err != nil {
		t.Fatalf("%s: decode server JSON: %v", name, err)
	}
	var generatedDecoded any
	if err := json.Unmarshal(generatedJSON, &generatedDecoded); err != nil {
		t.Fatalf("%s: decode generated JSON: %v", name, err)
	}
	if !reflect.DeepEqual(serverDecoded, generatedDecoded) {
		t.Fatalf("%s JSON mismatch\nserver:    %s\ngenerated: %s", name, serverJSON, generatedJSON)
	}
}

func uint64Ptr(v uint64) *uint64 { return &v }

func int64Ptr(v int64) *int64 { return &v }

func intPtr(v int) *int { return &v }

func stringPtr(v string) *string { return &v }
