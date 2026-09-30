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
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/slack/testsupport"
)

// evidenceEntry is one request the fake Slack server received, in arrival
// order, with the timestamp the fake echoed back.
type evidenceEntry struct {
	Method     string           `json:"method"`
	Channel    string           `json:"channel"`
	ThreadTS   string           `json:"thread_ts,omitempty"`
	Text       string           `json:"text"`
	Blocks     []map[string]any `json:"blocks,omitempty"`
	ReturnedTS string           `json:"returned_ts,omitempty"`
}

// TestSlackLifecycleEvidence runs the notifier through the real taps for a
// full feature lifecycle — research, design, plan, a two-phase roadmap loop,
// a refactor child step, publish with two pull request links, and completion
// — with a notifier stop and restart against the same state directory midway.
// When AGENTICO_EVIDENCE_DIR is set it writes a JSON transcript of every
// request the fake received in order plus a copy of the final Slack record,
// so a reviewer can confirm one root card per destination, ordered replies,
// in-place edits, no duplicate card after the restart, the child-kind prefix,
// the publish links, and a complete ledger, and can paste the blocks into
// Slack's Block Kit Builder.
func TestSlackLifecycleEvidence(t *testing.T) {
	dir := strings.TrimSpace(os.Getenv("AGENTICO_EVIDENCE_DIR"))
	if dir == "" {
		t.Skip("AGENTICO_EVIDENCE_DIR is not set; the lifecycle evidence transcript is only written for evidence runs")
	}

	harness := newNotifierHarness(t, defaultTestSettings(testToken, testRecipients()...))
	harness.seedFeature("F-1", func(f *feature.Feature) {
		withRoadmap(f)
		f.Status = feature.StatusResearching
		f.CurrentPhase = feature.PhaseResearch
		f.PhaseTimings = map[string]time.Duration{
			"research":     61 * time.Second,
			"design":       59 * time.Second,
			"phase-1-plan": 130 * time.Second,
			"phase-1-impl": 190 * time.Second,
			"phase-2-plan": 100 * time.Second,
			"phase-2-impl": 200 * time.Second,
		}
		f.PhaseCosts = map[string]float64{
			"research":     0.75,
			"design":       1.00,
			"phase-1-plan": 1.24,
			"phase-1-impl": 2.16,
			"phase-2-plan": 0.90,
			"phase-2-impl": 1.10,
		}
	})
	harness.seedFeature("F-2", func(f *feature.Feature) {
		f.Parent = &feature.ChildRelationship{ParentID: "F-1", Kind: feature.ChildKindRefactor}
		f.ActiveTimingKey = "implement"
		f.PhaseTimings = map[string]time.Duration{"implement": 42 * time.Second}
		f.PhaseCosts = map[string]float64{"implement": 1.5}
	})

	var mu sync.Mutex
	var transcript []evidenceEntry
	inner, _ := defaultOKResponder()
	harness.server.SetDefault(func(method string, request testsupport.Request) testsupport.Response {
		response := inner(method, request)
		channel := fieldString(request, "channel")
		if channel == "" {
			channel = fieldString(request, "users")
		}
		returnedTS := ""
		if body, ok := response.Body.(map[string]any); ok {
			if ts, ok := body["ts"].(string); ok {
				returnedTS = ts
			}
		}
		mu.Lock()
		transcript = append(transcript, evidenceEntry{
			Method:     method,
			Channel:    channel,
			ThreadTS:   fieldString(request, "thread_ts"),
			Text:       fieldString(request, "text"),
			Blocks:     requestBlocks(request),
			ReturnedTS: returnedTS,
		})
		mu.Unlock()
		return response
	})

	first := harness.start(0)
	postsPerDestination := 0
	type feed struct {
		event   ports.Event
		prepare func()
	}
	feeds := []feed{
		{event: ports.Event{Type: ports.FeatureStarted, FeatureID: "F-1"}},
		{event: startedEvent("F-1", feature.PhaseResearch)},
		{event: completedEvent("F-1", feature.PhaseResearch)},
		{event: startedEvent("F-1", feature.PhaseDesign), prepare: func() {
			setEvidenceFeatureState(t, harness, feature.PhaseDesign, feature.StatusDesigning, 1, false)
		}},
		{event: completedEvent("F-1", feature.PhaseDesign)},
		{event: startedEvent("F-1", feature.PhasePlan), prepare: func() {
			setEvidenceFeatureState(t, harness, feature.PhasePlan, feature.StatusPlanning, 1, false)
		}},
		{event: completedEvent("F-1", feature.PhasePlan)},
		{event: startedEvent("F-1", feature.PhaseImplement), prepare: func() {
			setEvidenceFeatureState(t, harness, feature.PhaseImplement, feature.StatusImplementing, 1, false)
		}},
		{event: completedEvent("F-1", feature.PhaseImplement)},
	}
	feedAll := func(feeds []feed) {
		t.Helper()
		for _, f := range feeds {
			if f.prepare != nil {
				f.prepare()
			}
			harness.feed(f.event)
			postsPerDestination++
			want := postsPerDestination
			ok := func() bool {
				return len(postsTo(harness.server, "C-ENG")) == want &&
					len(postsTo(harness.server, "D-U-ADA")) == want
			}
			deadline := time.Now().Add(10 * time.Second)
			for !ok() && time.Now().Before(deadline) {
				time.Sleep(2 * time.Millisecond)
			}
			if !ok() {
				t.Fatalf("after %s (%s): C-ENG posts = %d, D-U-ADA posts = %d; want %d each",
					eventTypeName(f.event.Type), f.event.FeatureID,
					len(postsTo(harness.server, "C-ENG")), len(postsTo(harness.server, "D-U-ADA")), want)
			}
		}
	}
	feedAll(feeds)

	// Stop and restart against the same state directory midway through the
	// roadmap loop: the restarted notifier must continue the same thread.
	first.Stop(context.Background())
	second := harness.newNotifier(0)
	second.Start()
	t.Cleanup(func() { second.Stop(context.Background()) })
	harness.notifier = second

	feedAll([]feed{
		{event: startedEvent("F-1", feature.PhasePlan), prepare: func() {
			setEvidenceFeatureState(t, harness, feature.PhasePlan, feature.StatusPlanning, 2, false)
		}},
		{event: completedEvent("F-1", feature.PhasePlan)},
		{event: startedEvent("F-1", feature.PhaseImplement), prepare: func() {
			setEvidenceFeatureState(t, harness, feature.PhaseImplement, feature.StatusImplementing, 2, false)
		}},
		{event: completedEvent("F-1", feature.PhaseImplement)},
	})
	feedAll([]feed{
		{event: startedEvent("F-2", feature.PhaseImplement)},
		{event: completedEvent("F-2", feature.PhaseImplement)},
		{event: ports.Event{Type: ports.FeatureCompleted, FeatureID: "F-2"}},
	})
	feedAll([]feed{
		{event: ports.Event{Type: ports.PublishStarted, FeatureID: "F-1"}, prepare: func() {
			setEvidenceFeatureState(t, harness, feature.PhasePublish, feature.StatusCodeReady, 2, false)
		}},
		{event: ports.Event{Type: ports.PublishCompleted, FeatureID: "F-1"}, prepare: func() {
			setEvidenceFeatureState(t, harness, feature.PhasePublish, feature.StatusPublished, 2, true)
		}},
		{event: ports.Event{Type: ports.FeatureCompleted, FeatureID: "F-1"}, prepare: func() {
			setEvidenceFeatureState(t, harness, feature.PhasePublish, feature.StatusDone, 2, true)
		}},
	})

	// One root card plus eighteen replies per destination, in that order,
	// and a complete ledger that outlived the restart.
	for _, channel := range []string{"D-U-ADA", "C-ENG"} {
		posts := postsTo(harness.server, channel)
		if len(posts) != 19 {
			t.Fatalf("destination %s received %d posts; want one root card plus eighteen replies", channel, len(posts))
		}
		mu.Lock()
		rootTS := ""
		for _, entry := range transcript {
			if entry.Method == "chat.postMessage" && entry.Channel == channel {
				rootTS = entry.ReturnedTS
				break
			}
		}
		mu.Unlock()
		if rootTS == "" {
			t.Fatalf("destination %s root card was never posted", channel)
		}
		if len(requestBlocks(posts[0])) == 0 {
			t.Fatalf("destination %s root card carries no blocks", channel)
		}
		for _, reply := range posts[1:] {
			if fieldString(reply, "thread_ts") != rootTS {
				t.Fatalf("destination %s reply thread_ts = %q; want the root %q",
					channel, fieldString(reply, "thread_ts"), rootTS)
			}
		}
	}
	for _, key := range []string{
		destinationKey("user", "U-ADA"),
		destinationKey("channel", "C-ENG"),
	} {
		waitFor(t, 10*time.Second, func() bool {
			ledger, ok := recordLedger(harness.stateDir, "F-1", key)
			return ok && len(ledger) == 19
		})
	}
	if got := len(harness.server.Requests("chat.update")); got == 0 {
		t.Fatal("the card was never edited in place across the lifecycle")
	}
	mu.Lock()
	rootHadPullRequests := false
	updateHadPullRequests := false
	for _, entry := range transcript {
		if len(entry.Blocks) == 0 {
			continue
		}
		encoded, err := json.Marshal(entry.Blocks)
		if err != nil {
			mu.Unlock()
			t.Fatalf("encode transcript blocks: %v", err)
		}
		hasPullRequests := strings.Contains(string(encoded), "Pull requests")
		if entry.Method == "chat.postMessage" && entry.ThreadTS == "" && hasPullRequests {
			rootHadPullRequests = true
		}
		if entry.Method == "chat.update" && hasPullRequests {
			updateHadPullRequests = true
		}
	}
	mu.Unlock()
	if rootHadPullRequests {
		t.Fatal("the initial root card showed pull requests before publishing")
	}
	if !updateHadPullRequests {
		t.Fatal("no edited root card showed pull requests after publishing")
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create evidence directory: %v", err)
	}
	mu.Lock()
	entries := append([]evidenceEntry(nil), transcript...)
	mu.Unlock()
	encoded, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		t.Fatalf("encode transcript: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "slack-lifecycle-transcript.json"), encoded, 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	record, err := os.ReadFile(filepath.Join(harness.stateDir, "F-1", recordFilename))
	if err != nil {
		t.Fatalf("read final Slack record: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "final-slack-record.yaml"), record, 0o644); err != nil {
		t.Fatalf("write final Slack record: %v", err)
	}
}

func setEvidenceFeatureState(
	t *testing.T,
	harness *notifierHarness,
	phase feature.Phase,
	status feature.Status,
	roadmapPhase int,
	withPullRequests bool,
) {
	t.Helper()
	if err := harness.store.Modify("F-1", func(f *feature.Feature) error {
		f.CurrentPhase = phase
		f.Status = status
		f.CurrentRoadmapPhase = roadmapPhase
		if withPullRequests {
			f.RepoStates = map[string]*feature.RepoState{
				"alpha": {PRURL: "https://github.com/org/alpha/pull/7"},
				"beta":  {PRURL: "https://github.com/org/beta/pull/9"},
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("set evidence feature state: %v", err)
	}
}
