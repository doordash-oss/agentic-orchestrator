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

package e2e

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/server"
	"github.com/doordash-oss/agentic-orchestrator/internal/supervisor"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

func compactedRecords(t *testing.T, records []supervisor.Record) (supervisor.Record, supervisor.CheckpointData, supervisor.Record, supervisor.MarkerData) {
	t.Helper()
	var checkpoint, marker supervisor.Record
	var data supervisor.CheckpointData
	var notice supervisor.MarkerData
	for _, record := range records {
		switch record.Kind {
		case supervisor.KindCheckpoint:
			if checkpoint.Seq != 0 {
				t.Fatal("multiple checkpoints")
			}
			checkpoint = record
			if err := json.Unmarshal(record.Data, &data); err != nil {
				t.Fatal(err)
			}
		case supervisor.KindMarker:
			var m supervisor.MarkerData
			if err := json.Unmarshal(record.Data, &m); err != nil {
				t.Fatal(err)
			}
			if m.Marker == supervisor.MarkerCompacted {
				if marker.Seq != 0 {
					t.Fatal("multiple compaction markers")
				}
				marker, notice = record, m
			}
		}
	}
	if checkpoint.Seq == 0 || marker.Seq != checkpoint.Seq+1 {
		t.Fatalf("checkpoint %d marker %d", checkpoint.Seq, marker.Seq)
	}
	return checkpoint, data, marker, notice
}

func TestSupervisorClaudeCompactionCheckpointAndResume(t *testing.T) {
	h := newSupervisorHarness(t, testutil.FakeClaudeInteractiveScriptBody())
	h.chooseSettings()
	h.send(testutil.FakeSupervisorUsageHigh, "usage")
	h.waitLifecycle(server.SupervisorLifecycleIdle)
	if usage := h.state().ContextUsage; usage == nil || usage.Percent < 80 {
		t.Fatalf("high usage = %+v", usage)
	}
	before := h.state().HeadSeq
	h.send(testutil.FakeSupervisorCompact, "compact")
	h.waitLifecycle(server.SupervisorLifecycleIdle)
	_, checkpoint, _, marker := compactedRecords(t, h.durableRecords())
	if checkpoint.CoversThroughSeq != before+1 || checkpoint.Summary == "" || checkpoint.NativeBaseline == nil || checkpoint.NativeBaseline.Harness != "claude" || checkpoint.Trigger != "auto" || checkpoint.PreTokens != 170000 || marker.Summary != checkpoint.Summary {
		t.Fatalf("checkpoint = %+v marker = %+v", checkpoint, marker)
	}
	if usage := h.state().ContextUsage; usage == nil || usage.Percent >= 80 {
		t.Fatalf("usage after compaction = %+v", usage)
	}
	h.do(http.MethodPost, "/api/v1/supervisor/end", map[string]any{}, http.StatusOK, nil)
	if h.state().ContextUsage != nil {
		t.Fatalf("usage after End = %+v", h.state().ContextUsage)
	}
	h.send("continue", "resumed")
	h.waitLifecycle(server.SupervisorLifecycleIdle)
	if reply := lastAssistantText(h.transcript("?limit=500").Items); !strings.HasPrefix(reply, "Resumed after compaction with ") {
		t.Fatalf("resume reply = %q", reply)
	}
	data, err := os.ReadFile(h.nativeSessionFile(h.coord.State().NativeSessionID))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"subtype":"compact_boundary"`) || !strings.Contains(string(data), `"isCompactSummary":true`) || strings.Contains(string(data), testutil.FakeSupervisorUsageHigh) {
		t.Fatalf("rebuilt session = %s", data)
	}
}

func TestSupervisorCodexCompactionCheckpointAndResume(t *testing.T) {
	h := newCodexSupervisorHarness(t, testutil.FakeCodexScript{})
	h.chooseSettings()
	h.send(testutil.FakeCodexUsageHigh, "usage")
	h.waitLifecycle(server.SupervisorLifecycleIdle)
	if usage := h.state().ContextUsage; usage == nil || usage.Percent < 80 {
		t.Fatalf("high usage = %+v", usage)
	}
	h.send(testutil.FakeCodexCompact, "compact")
	h.waitLifecycle(server.SupervisorLifecycleIdle)
	_, checkpoint, _, marker := compactedRecords(t, h.durableRecords())
	if checkpoint.Summary != "" || checkpoint.NativeBaseline == nil || checkpoint.NativeBaseline.Harness != "codex" || marker.Summary != "" {
		t.Fatalf("checkpoint = %+v marker = %+v", checkpoint, marker)
	}
	if usage := h.state().ContextUsage; usage == nil || usage.Percent >= 80 {
		t.Fatalf("usage after compaction = %+v", usage)
	}
	h.do(http.MethodPost, "/api/v1/supervisor/end", map[string]any{}, http.StatusOK, nil)
	h.send("continue", "resumed")
	h.waitLifecycle(server.SupervisorLifecycleIdle)
	if reply := lastAssistantText(h.transcript("?limit=500").Items); !strings.HasPrefix(reply, "Resumed after compaction with ") {
		t.Fatalf("resume reply = %q", reply)
	}
	data, err := os.ReadFile(h.rolloutFor(h.coord.State().NativeSessionID))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"type":"compacted"`) || strings.Contains(string(data), testutil.FakeCodexUsageHigh) {
		t.Fatalf("rebuilt rollout = %s", data)
	}
}

func TestSupervisorOpenCodeContextUsageHigh(t *testing.T) {
	h := newOpenCodeSupervisorHarness(t, testutil.FakeOpenCodeScript{})
	h.chooseSettings()
	h.send(testutil.FakeOpenCodeUsageHigh, "usage")
	h.waitLifecycle(server.SupervisorLifecycleIdle)
	if usage := h.state().ContextUsage; usage == nil || usage.Percent < 80 {
		t.Fatalf("high usage = %+v", usage)
	}
}

func TestSupervisorClaudeCheckpointSwitchesAsSummary(t *testing.T) {
	for _, destination := range []string{"codex", "opencode"} {
		t.Run(destination, func(t *testing.T) {
			h := newCrossHarness(t, "claude")
			h.send("The earlier fact is blue.", "fact")
			h.waitLifecycle(server.SupervisorLifecycleIdle)
			h.send(testutil.FakeSupervisorCompact, "compact")
			h.waitLifecycle(server.SupervisorLifecycleIdle)
			h.switchTo("switch", destination, nil, nil)
			h.send("continue", "continue")
			h.waitLifecycle(server.SupervisorLifecycleIdle)
			var rebuilt string
			if destination == "codex" {
				data, err := os.ReadFile(h.rolloutFor(h.coord.State().NativeSessionID))
				if err != nil {
					t.Fatal(err)
				}
				rebuilt = string(data)
			} else {
				seeds := testutil.FakeOpenCodeSeeds(t, h.scripts["opencode"])
				if len(seeds) != 1 {
					t.Fatalf("seeds = %+v", seeds)
				}
				rebuilt = seeds[0].Text
			}
			if !strings.Contains(rebuilt, "Agentico note: Summary of the earlier conversation") || !strings.Contains(rebuilt, "Summary of SUPERVISOR_COMPACT") || strings.Contains(rebuilt, "The earlier fact is blue.") {
				t.Fatalf("%s rebuilt history = %s", destination, rebuilt)
			}
		})
	}
}

func TestSupervisorOpaqueCodexCheckpointSwitchesWithFullHistory(t *testing.T) {
	h := newCrossHarness(t, "codex")
	h.send("The earlier fact is blue.", "fact")
	h.waitLifecycle(server.SupervisorLifecycleIdle)
	h.send(testutil.FakeCodexCompact, "compact")
	h.waitLifecycle(server.SupervisorLifecycleIdle)
	h.switchTo("switch", "claude", nil, nil)
	h.send("continue", "continue")
	h.waitLifecycle(server.SupervisorLifecycleIdle)
	data, err := os.ReadFile(h.nativeSessionFile(h.coord.State().NativeSessionID))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "The earlier fact is blue.") || strings.Contains(string(data), `"isCompactSummary":true`) {
		t.Fatalf("Claude rebuilt history = %s", data)
	}
}

func TestSupervisorClaudeCompactionWithoutSummaryKeepsFullHistory(t *testing.T) {
	h := newSupervisorHarness(t, testutil.FakeClaudeInteractiveScriptBody())
	h.chooseSettings()
	h.send("The earlier fact is blue.", "fact")
	h.waitLifecycle(server.SupervisorLifecycleIdle)
	h.send(testutil.FakeSupervisorCompactExit, "compact-exit")
	h.waitRecord("unavailable checkpoint marker", func(record server.SupervisorRecord) bool {
		return record.Marker != nil && record.Marker.Marker == server.SupervisorMarkerCompacted && record.Marker.Code == "checkpoint_unavailable"
	})
	for _, record := range h.durableRecords() {
		if record.Kind == supervisor.KindCheckpoint {
			t.Fatal("unexpected checkpoint after missing summary")
		}
	}
	h.send("continue", "continue")
	h.waitLifecycle(server.SupervisorLifecycleIdle)
	data, err := os.ReadFile(h.nativeSessionFile(h.coord.State().NativeSessionID))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "The earlier fact is blue.") {
		t.Fatalf("rebuild lost full history: %s", data)
	}
}
