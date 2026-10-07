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
	"encoding/json"
	"sync"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// selfAssigningConverter is a converter for a harness that mints its own
// session ids, as Codex does.
type selfAssigningConverter struct{ fakeConverter }

func (*selfAssigningConverter) Harness() string               { return "codex" }
func (*selfAssigningConverter) HarnessAssignsSessionID() bool { return true }

// scriptedThreads answers each launch with the next scripted thread id and
// resume outcome.
type scriptedThreads struct {
	mu    sync.Mutex
	steps []struct{ id, outcome string }
}

func (s *scriptedThreads) next(req LaunchRequest) *llm.SystemInitMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	step := s.steps[0]
	s.steps = s.steps[1:]
	return &llm.SystemInitMessage{SessionID: step.id, Model: "gpt-effective", ResumeOutcome: step.outcome}
}

func TestCoordinator_AdoptsHarnessAssignedNativeID(t *testing.T) {
	conv := &selfAssigningConverter{}
	threads := &scriptedThreads{steps: []struct{ id, outcome string }{
		{"th-1", ""},
		{"th-1", llm.ResumeOutcomeResumed},
		{"th-2", llm.ResumeOutcomeFallback},
		{"th-2", llm.ResumeOutcomeResumed},
	}}
	launcher := &fakeLauncher{init: threads.next}
	dir := t.TempDir()
	c := newTestCoordinator(t, dir, launcher, withConverter(conv))
	if _, err := c.UpdateSettings(Settings{Harness: "codex", Model: "gpt-x", Effort: "high"}); err != nil {
		t.Fatal(err)
	}
	sub, err := c.Subscribe(0, false, "")
	if err != nil {
		t.Fatal(err)
	}

	completeTurn(t, c, launcher, "cm-1")
	if n := len(conv.rebuilds()); n != 0 {
		t.Fatalf("first launch rebuilt %d times; Codex cannot resume without a rollout", n)
	}
	if got := launcher.request(0).ResumeSessionID; got != "" {
		t.Fatalf("first launch resumed %q", got)
	}
	if st := c.State(); st.NativeSessionID != "th-1" || st.EffectiveModel != "gpt-effective" {
		t.Fatalf("state after first launch = native %q model %q", st.NativeSessionID, st.EffectiveModel)
	}
	c.Unsubscribe(sub)
	for ev := range sub.Events() {
		if ev.Kind == EventState && ev.State.StartingStep == StepRebuilding {
			t.Fatal("a launch with nothing to rebuild reported the rebuilding step")
		}
	}

	c.End()
	completeTurn(t, c, launcher, "cm-2")
	rebuilds := conv.rebuilds()
	if len(rebuilds) != 1 || rebuilds[0].NativeSessionID != "th-1" || rebuilds[0].Model != "gpt-x" || rebuilds[0].Effort != "high" {
		t.Fatalf("second launch rebuilds = %+v", rebuilds)
	}
	if got := launcher.request(1).ResumeSessionID; got != "th-1" {
		t.Fatalf("second launch resumed %q, want th-1", got)
	}
	if got := c.State().NativeSessionID; got != "th-1" {
		t.Fatalf("native id after resume = %q", got)
	}
	if got := markersOf(t, allRecords(t, c), MarkerHistoryNotRestored); len(got) != 0 {
		t.Fatalf("a successful resume left %d markers", len(got))
	}

	c.End()
	completeTurn(t, c, launcher, "cm-3")
	if got := c.State().NativeSessionID; got != "th-2" {
		t.Fatalf("native id after fallback = %q, want the fresh thread", got)
	}
	markers := markersOf(t, allRecords(t, c), MarkerHistoryNotRestored)
	if len(markers) != 1 {
		t.Fatalf("history_not_restored markers = %d, want 1", len(markers))
	}
	var data MarkerData
	_ = json.Unmarshal(markers[0].Data, &data)
	if data.Text != "Codex could not read the restored thread; continuing on a fresh thread" || markers[0].Visibility != VisibilityDisplayOnly {
		t.Fatalf("fallback marker = %+v", data)
	}

	c.End()
	completeTurn(t, c, launcher, "cm-4")
	if got := launcher.request(3).ResumeSessionID; got != "th-2" {
		t.Fatalf("launch after fallback resumed %q, want th-2", got)
	}
	_ = c.Close()

	// The adopted id is durable.
	reopened := newTestCoordinator(t, dir, &fakeLauncher{}, withConverter(conv))
	if got := reopened.State().NativeSessionID; got != "th-2" {
		t.Fatalf("native id after restart = %q", got)
	}
}
