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
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

func TestSSEMapsShutdownDomainEventToMetadataOnlyRuntimeDTO(t *testing.T) {
	t.Parallel()

	b := newEventBrokerWithOptions(eventBrokerOptions{Epoch: testEventEpoch, ReplayLimit: 8})
	ch := b.subscribe()
	defer b.unsubscribe(ch)
	b.publish(eventDTOFromRuntime(ports.Event{
		Type:    ports.RuntimeShutdownStarted,
		Message: "private-token /tmp/agentico-runtime/shutdown.log",
	}))
	dto := <-ch

	if dto.APIVersion != APIVersion {
		t.Fatalf("api_version = %q; want %q", dto.APIVersion, APIVersion)
	}
	if dto.ID != "1" || dto.Seq != 1 || dto.Epoch != testEventEpoch {
		t.Fatalf("envelope = id %q seq %d epoch %q; want id/seq 1 and epoch-test", dto.ID, dto.Seq, dto.Epoch)
	}
	if dto.Kind != sseEventShutdownUpdated {
		t.Fatalf("kind = %q; want shutdown.updated", dto.Kind)
	}
	if dto.Resource.Type != resourceTypeRuntime {
		t.Fatalf("resource.type = %q; want runtime", dto.Resource.Type)
	}
	if dto.Resource.ID != "" || dto.Resource.FeatureID != "" || dto.Resource.Phase != "" {
		t.Fatalf("resource = %+v; want runtime metadata only", dto.Resource)
	}
	if dto.At.IsZero() {
		t.Fatal("at is zero; want shutdown timestamp")
	}
	if dto.At.Location() != time.UTC {
		t.Fatalf("at location = %v; want UTC", dto.At.Location())
	}
	if !dto.SnapshotRequired {
		t.Fatal("snapshot_required = false; want true")
	}
	if dto.Revision == "" {
		t.Fatal("revision is empty; want runtime revision for reconnect diagnostics")
	}

	raw, err := json.Marshal(dto)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if strings.Contains(string(raw), "private-token") || strings.Contains(string(raw), "/tmp/agentico-runtime") {
		t.Fatalf("shutdown SSE DTO leaks unsafe source detail: %s", raw)
	}
}
