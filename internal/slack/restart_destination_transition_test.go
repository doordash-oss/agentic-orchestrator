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

package slack

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/slack/testsupport"
)

func TestSlackRestartSweepReenabledSameRecipientsBootstrapsNewFeature(t *testing.T) {
	h := newNotifierHarness(t, defaultTestSettings("xoxb-restart", testRecipients()[1]))
	n := h.newNotifier(0)
	n.Start()
	t.Cleanup(func() { n.Stop(context.Background()) })
	n.SignalReady()
	waitFor(t, time.Second, func() bool { return n.startupDone.Load() })

	h.settings.mutate(func(s *ports.SlackRuntimeSettings) { s.Enabled = false })
	n.sweepDestinations()
	const id = "feature-while-disabled"
	h.seedFeature(id, nil)
	artifact := []byte("# Phase plan\nReview this plan.\n")
	h.pending.set(id, ports.SlackPendingInput{
		FeatureID: id, Kind: ports.SlackPendingReview, ReviewID: "review-1",
		ReviewMode: "plan", TargetPhase: "implement", SourceRevision: "sha256:review-1",
		ArtifactID: "phase-plan", ArtifactFilename: "phase-plan.md",
		ArtifactBytes: artifact, ArtifactSize: int64(len(artifact)), RunNumber: 1,
	})
	n.sweepDestinations()
	if got := h.server.AllRequests(); len(got) != 0 {
		t.Fatalf("disabled sweep made Slack requests: %#v", got)
	}
	if _, err := os.Stat(recordPath(h.stateDir, id)); !os.IsNotExist(err) {
		t.Fatalf("disabled sweep wrote a record: stat error = %v", err)
	}

	h.server.Script("files.completeUploadExternal", testsupport.Response{Body: map[string]any{
		"ok": true, "files": []any{map[string]any{
			"id": "F00000001", "shares": map[string]any{"public": map[string]any{
				"C-ENG": []any{map[string]any{"ts": "1758499200.000120"}},
			}},
		}},
	}})
	h.settings.mutate(func(s *ports.SlackRuntimeSettings) { s.Enabled = true })
	n.sweepDestinations()
	record, err := loadFeatureRecord(h.stateDir, id)
	if err != nil {
		t.Fatal(err)
	}
	const key = "channel:C-ENG"
	if record.Destinations[key].RootTS == "" || len(record.Pending) != 1 ||
		record.Pending[0].Tag != "#1" ||
		record.Pending[0].SourceRevision != "sha256:review-1" ||
		record.Pending[0].MessageTS[key] == "" {
		t.Fatalf("bootstrap record = %#v; want root and tagged review with revision and message timestamp", record)
	}
	posts := h.server.Requests("chat.postMessage")
	if len(posts) != 2 || fieldString(posts[0], "thread_ts") != "" ||
		fieldString(posts[1], "thread_ts") != record.Destinations[key].RootTS {
		t.Fatalf("bootstrap posts = %#v; want one root and one review thread item", posts)
	}
	if got := h.server.CallCount("files.getUploadURLExternal"); got != 1 {
		t.Fatalf("review artifact uploads = %d; want 1", got)
	}

	waitFor(t, time.Second, func() bool { return h.server.CallCount("chat.update") == 1 })
	before := len(h.server.AllRequests())
	n.sweepDestinations()
	if got := len(h.server.AllRequests()); got != before {
		t.Fatalf("unchanged next tick made %d more Slack requests", got-before)
	}
}
