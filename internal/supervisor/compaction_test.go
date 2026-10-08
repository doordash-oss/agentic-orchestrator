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

package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

type capturingConverter struct {
	selfAssigningConverter
	payload json.RawMessage
	err     error
	gate    <-chan struct{}
}

func (c *capturingConverter) CaptureCompaction(ctx context.Context, _ string) (json.RawMessage, error) {
	if c.gate != nil {
		select {
		case <-c.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return c.payload, c.err
}

func codexCompactionCoordinator(t *testing.T, converter *capturingConverter) (*Coordinator, *fakeLauncher) {
	t.Helper()
	launcher := &fakeLauncher{init: func(LaunchRequest) *llm.SystemInitMessage {
		return &llm.SystemInitMessage{SessionID: "codex-thread", Model: "gpt-effective"}
	}}
	c := newTestCoordinator(t, t.TempDir(), launcher, func(o *Options) {
		o.Converters = map[string]Converter{"codex": converter}
		o.CompactionCaptureTimeout = 50 * time.Millisecond
	})
	if _, err := c.UpdateSettings(Settings{Harness: "codex", Model: "gpt-5"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Send(context.Background(), "before", "", "codex-compact"); err != nil {
		t.Fatal(err)
	}
	return c, launcher
}

func TestCodexCompactionCaptureCommitsOrMarksUnavailable(t *testing.T) {
	for _, tc := range []struct {
		name           string
		payload        json.RawMessage
		err            error
		wantCheckpoint bool
	}{
		{"success", json.RawMessage(`{"window_id":"win-1","replacement_history":[]}`), nil, true},
		{"error", nil, errors.New("rollout read failed"), false},
		{"timeout", nil, context.DeadlineExceeded, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			converter := &capturingConverter{payload: tc.payload, err: tc.err}
			c, launcher := codexCompactionCoordinator(t, converter)
			launcher.session(0).emit(llm.SDKMessage{Type: "system", Compact: &llm.CompactBoundaryMessage{ItemID: "compact-1"}})
			waitFor(t, "compaction record", func() bool { return len(markersOf(t, allRecords(t, c), MarkerCompacted)) == 1 })
			recs := allRecords(t, c)
			checkpoints := 0
			for _, rec := range recs {
				if rec.Kind == KindCheckpoint {
					checkpoints++
					var data CheckpointData
					if err := json.Unmarshal(rec.Data, &data); err != nil {
						t.Fatal(err)
					}
					if data.Summary != "" || data.NativeBaseline == nil || string(data.NativeBaseline.Payload) != string(tc.payload) {
						t.Fatalf("checkpoint = %+v", data)
					}
				}
			}
			if tc.wantCheckpoint && checkpoints != 1 || !tc.wantCheckpoint && checkpoints != 0 {
				t.Fatalf("checkpoints = %d", checkpoints)
			}
			var marker MarkerData
			if err := json.Unmarshal(recs[len(recs)-1].Data, &marker); err != nil {
				t.Fatal(err)
			}
			if tc.wantCheckpoint && marker.Code != "" || !tc.wantCheckpoint && marker.Code != "checkpoint_unavailable" {
				t.Fatalf("marker = %+v", marker)
			}
		})
	}
}

func TestClaudeCompactionCommitsCheckpointThenMarker(t *testing.T) {
	launcher := &fakeLauncher{}
	c := newTestCoordinator(t, t.TempDir(), launcher)
	chooseSettings(t, c)
	if _, err := c.Send(context.Background(), "before", "", "compact-before"); err != nil {
		t.Fatal(err)
	}
	s := launcher.session(0)
	head := c.State().HeadSeq
	s.emit(llm.SDKMessage{Type: "system", Subtype: "compact_boundary", Compact: &llm.CompactBoundaryMessage{Trigger: "auto", PreTokens: 123456}})
	s.emit(llm.SDKMessage{Type: "user", User: &llm.UserMessage{IsCompactSummary: true, Message: llm.ConversationMsg{Content: []llm.ContentBlock{{Type: "text", Text: "earlier fact"}}}}})
	recs := allRecords(t, c)
	if len(recs) != 3 || recs[1].Kind != KindCheckpoint || recs[2].Kind != KindMarker {
		t.Fatalf("records = %+v", recs)
	}
	var checkpoint CheckpointData
	if err := json.Unmarshal(recs[1].Data, &checkpoint); err != nil {
		t.Fatal(err)
	}
	if checkpoint.CoversThroughSeq != head || checkpoint.Summary != "earlier fact" || checkpoint.NativeBaseline == nil || checkpoint.NativeBaseline.Harness != "claude" || checkpoint.Reason != "native_auto" || checkpoint.Model != "haiku" || checkpoint.Trigger != "auto" || checkpoint.PreTokens != 123456 {
		t.Fatalf("checkpoint = %+v", checkpoint)
	}
	if recs[1].Visibility != VisibilityModelOnly || !c.store.hasContent() {
		t.Fatalf("checkpoint visibility/content = %s/%v", recs[1].Visibility, c.store.hasContent())
	}
	var marker MarkerData
	if err := json.Unmarshal(recs[2].Data, &marker); err != nil {
		t.Fatal(err)
	}
	if marker.Marker != MarkerCompacted || marker.Summary != "earlier fact" {
		t.Fatalf("marker = %+v", marker)
	}
}

func TestClaudeCompactionWithoutSummaryMarksUnavailable(t *testing.T) {
	launcher := &fakeLauncher{}
	c := newTestCoordinator(t, t.TempDir(), launcher)
	chooseSettings(t, c)
	if _, err := c.Send(context.Background(), "before", "", "compact-missing"); err != nil {
		t.Fatal(err)
	}
	s := launcher.session(0)
	s.emit(llm.SDKMessage{Type: "system", Compact: &llm.CompactBoundaryMessage{Trigger: "auto"}})
	s.emit(llm.SDKMessage{Type: "result", Result: &llm.ResultMessage{}})
	recs := allRecords(t, c)
	if len(recs) != 2 || recs[1].Kind != KindMarker {
		t.Fatalf("records = %+v", recs)
	}
	var marker MarkerData
	if err := json.Unmarshal(recs[1].Data, &marker); err != nil {
		t.Fatal(err)
	}
	if marker.Marker != MarkerCompacted || marker.Code != "checkpoint_unavailable" {
		t.Fatalf("marker = %+v", marker)
	}
}

func TestContextUsageFollowsSessionAndClearsOnEnd(t *testing.T) {
	launcher := &fakeLauncher{}
	c := newTestCoordinator(t, t.TempDir(), launcher)
	chooseSettings(t, c)
	if c.State().ContextUsage != nil {
		t.Fatal("boot context usage should be null")
	}
	if _, err := c.Send(context.Background(), "hello", "", "context-usage"); err != nil {
		t.Fatal(err)
	}
	s := launcher.session(0)
	s.ContextPercentageVal = 42
	s.LatestUsageVal = &llm.Usage{InputTokens: 42000, ContextWindow: 100000}
	s.emit(llm.SDKMessage{Type: "usage_update", UsageUpdate: s.LatestUsageVal})
	if got := c.State().ContextUsage; got == nil || *got != (ContextUsage{Percent: 42, UsedTokens: 42000, WindowTokens: 100000}) {
		t.Fatalf("context usage = %+v", got)
	}
	c.End()
	if c.State().ContextUsage != nil {
		t.Fatal("end did not clear context usage")
	}
}

func TestCheckpointAloneCountsAsHistoryContent(t *testing.T) {
	dir := t.TempDir()
	store, err := openTranscriptStore(dir, "checkpoint-only", func() string { return "id" }, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.close() })
	store.setGeneration(1)
	payload, err := json.Marshal(CheckpointData{CoversThroughSeq: 0, Summary: "earlier conversation", Reason: "native_auto", Model: "haiku"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.appendRecord(Record{Generation: 1, Kind: KindCheckpoint, Visibility: VisibilityModelOnly, Data: payload}); err != nil {
		t.Fatal(err)
	}
	if !store.hasContent() {
		t.Fatal("checkpoint-only transcript did not count as history")
	}
}
