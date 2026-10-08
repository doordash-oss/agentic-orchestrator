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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

func conversationPath(stateDir, id string) string {
	return filepath.Join(stateDir, supervisorDirName, conversationsDir, id)
}

// readConversationRecords reads a retired conversation's transcript from
// disk without disturbing it.
func readConversationRecords(t *testing.T, stateDir, id string) []Record {
	t.Helper()
	store, err := openTranscriptStore(conversationPath(stateDir, id), id, randomID, time.Now)
	if err != nil {
		t.Fatalf("open retired transcript: %v", err)
	}
	defer store.close()
	recs, err := store.after(0)
	if err != nil {
		t.Fatalf("read retired transcript: %v", err)
	}
	return recs
}

func readFileBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

// drain collects a subscription's events until its channel closes.
func drain(t *testing.T, sub *Subscription) []Event {
	t.Helper()
	var out []Event
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev, open := <-sub.Events():
			if !open {
				return out
			}
			out = append(out, ev)
		case <-timeout:
			t.Fatal("subscription was not closed")
		}
	}
}

func TestCoordinator_ResetIdleStartsEmptyConversationAndKeepsTheOldOne(t *testing.T) {
	dir := t.TempDir()
	conv := &fakeConverter{}
	launcher := &fakeLauncher{}
	c := newTestCoordinator(t, dir, launcher, withConverter(conv))
	chooseSettings(t, c)
	completeTurn(t, c, launcher, "cm-1")
	before := c.State()
	oldDir := conversationPath(dir, before.ConversationID)
	transcript := readFileBytes(t, filepath.Join(oldDir, transcriptFileName))
	index := readFileBytes(t, filepath.Join(oldDir, indexFileName))
	settingsFile := readFileBytes(t, filepath.Join(dir, supervisorDirName, settingsFileName))
	sub, err := c.Subscribe(before.HeadSeq, true, before.StreamEpoch)
	if err != nil {
		t.Fatal(err)
	}

	res, err := c.Reset()
	if err != nil {
		t.Fatalf("Reset: %v", err)
	}
	st := res.State
	if res.Result != ResetDone || res.PreviousConversationID != before.ConversationID {
		t.Fatalf("reset result = %s prev %q, want reset %q", res.Result, res.PreviousConversationID, before.ConversationID)
	}
	if st.ConversationID == before.ConversationID || !validConversationID(st.ConversationID) || st.Generation != 0 ||
		st.Lifecycle != LifecycleStopped || st.LastTurnOutcome != OutcomeNone || st.InterruptedBy != InterruptedByNone ||
		st.HeadSeq != 0 || st.Settings != before.Settings || st.NativeSessionID != "" || st.Failure != nil ||
		st.StreamEpoch == "" || st.StreamEpoch == before.StreamEpoch || st.SessionID != "" {
		t.Fatalf("state after reset = %+v (before %+v)", st, before)
	}
	if got := c.State(); got.ConversationID != st.ConversationID {
		t.Fatalf("read model conversation = %q, want %q", got.ConversationID, st.ConversationID)
	}
	if launcher.session(0).IsActive() {
		t.Fatal("reset left the idle process running")
	}
	drain(t, sub)
	if !sub.ConversationReset() {
		t.Fatal("a live subscription was not ended by the reset")
	}
	if stale, _ := c.Subscribe(before.HeadSeq, true, before.StreamEpoch); !stale.Reset {
		t.Fatal("a resume with the old epoch did not reset")
	}

	// The retired conversation is untouched on disk; the pointer and the
	// settings file are the only shared files and only the pointer moved.
	if got := readFileBytes(t, filepath.Join(oldDir, transcriptFileName)); !bytes.Equal(got, transcript) {
		t.Fatal("reset changed the old transcript")
	}
	if got := readFileBytes(t, filepath.Join(oldDir, indexFileName)); !bytes.Equal(got, index) {
		t.Fatal("reset changed the old index")
	}
	if _, err := os.Stat(filepath.Join(oldDir, generationsDirName, "1")); err != nil {
		t.Fatalf("old generation dir: %v", err)
	}
	if got := readFileBytes(t, filepath.Join(dir, supervisorDirName, settingsFileName)); !bytes.Equal(got, settingsFile) {
		t.Fatal("reset rewrote the settings file")
	}
	pointer, _, err := loadConversation(filepath.Join(dir, supervisorDirName))
	if err != nil || pointer.ConversationID != st.ConversationID || pointer.Generation != 0 || pointer.NativeSessionID != "" || pointer.StreamEpoch != st.StreamEpoch {
		t.Fatalf("conversation pointer = %+v %v", pointer, err)
	}
	if turns, _ := loadTurns(filepath.Join(dir, supervisorDirName)); len(turns.TurnIDs) != 0 {
		t.Fatalf("turn-in-flight record after reset = %+v", turns)
	}

	// The next send launches generation 1 of the new conversation with no
	// history and a freshly minted native id.
	live, err := c.Subscribe(0, true, st.StreamEpoch)
	if err != nil || live.Reset {
		t.Fatalf("subscribe to new conversation: %+v %v", live, err)
	}
	sent, err := c.Send(context.Background(), "fresh start", "", "cm-1")
	if err != nil {
		t.Fatal(err)
	}
	if !sent.Launched || sent.Deduplicated || sent.Record.Seq != 1 || sent.Record.ConversationID != st.ConversationID || sent.Record.Generation != 1 {
		t.Fatalf("first send of the new conversation = %+v", sent)
	}
	req := launcher.request(1)
	if req.ConversationID != st.ConversationID || req.Generation != 1 || req.ResumeSessionID != "" || req.SeedHistoryPath != "" {
		t.Fatalf("launch after reset = %+v", req)
	}
	rebuilds := conv.rebuilds()
	if len(rebuilds) != 2 || len(rebuilds[1].Records) != 0 || rebuilds[1].NativeSessionID == "" ||
		rebuilds[1].NativeSessionID == rebuilds[0].NativeSessionID || rebuilds[1].ConversationID != st.ConversationID {
		t.Fatalf("rebuilds = %+v", rebuilds)
	}
	waitFor(t, "live user record", func() bool {
		for {
			select {
			case ev := <-live.Events():
				if ev.Kind == EventRecord && ev.Record.Seq == 1 && ev.Record.ConversationID == st.ConversationID {
					return true
				}
			default:
				return false
			}
		}
	})

	// A restart opens the new conversation (the held turn reads as cut by
	// the shutdown).
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := newTestCoordinator(t, dir, &fakeLauncher{})
	if got := reopened.State(); got.ConversationID != st.ConversationID || got.Generation != 1 || got.HeadSeq < 1 {
		t.Fatalf("state after restart = %+v", got)
	}
}

func TestCoordinator_ResetDuringHeldTurnInterruptsAndAppliesPendingChange(t *testing.T) {
	dir := t.TempDir()
	launcher := &fakeLauncher{}
	c := newTestCoordinator(t, dir, launcher)
	chooseSettings(t, c)
	if _, err := c.Send(context.Background(), "held", "", "cm-1"); err != nil {
		t.Fatal(err)
	}
	launcher.session(0).emit(llm.SDKMessage{Type: "control_request", ControlRequest: &llm.ControlRequestMessage{
		RequestID: "req-1",
		Request:   llm.ControlRequest{Subtype: "can_use_tool", ToolName: "Bash", Input: json.RawMessage(`{"command":"ls"}`)},
	}})
	waitLifecycle(t, c, LifecycleWaitingPermission)
	if _, err := c.ChangeSettings(SettingsChange{Model: settingValue("sonnet"), RequestID: "change-1", ExpectedGeneration: 1}); err != nil {
		t.Fatal(err)
	}
	before := c.State()
	if before.PendingChange == nil {
		t.Fatalf("change not queued: %+v", before)
	}
	sub, err := c.Subscribe(0, false, "")
	if err != nil {
		t.Fatal(err)
	}

	res, err := c.Reset()
	if err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if res.Result != ResetDone || res.PreviousConversationID != before.ConversationID {
		t.Fatalf("reset = %+v", res)
	}
	if launcher.session(0).Stops() == 0 || launcher.session(0).IsActive() {
		t.Fatal("reset did not stop the held process")
	}
	st := res.State
	if st.ConversationID == before.ConversationID || st.Lifecycle != LifecycleStopped || st.LastTurnOutcome != OutcomeNone ||
		st.Settings.Model != "sonnet" || st.PendingChange != nil || len(st.PendingRequests) != 0 || st.HeadSeq != 0 {
		t.Fatalf("state after reset = %+v", st)
	}
	stateDir := filepath.Join(dir, supervisorDirName)
	if saved, _ := loadSettings(stateDir); saved.Model != "sonnet" {
		t.Fatalf("settings file after reset = %+v", saved)
	}
	if pending, _ := loadPendingChange(stateDir); pending != nil {
		t.Fatalf("pending change survived reset: %+v", pending)
	}
	if turns, _ := loadTurns(stateDir); len(turns.TurnIDs) != 0 {
		t.Fatalf("turn-in-flight record after reset = %+v", turns)
	}

	old := readConversationRecords(t, dir, before.ConversationID)
	interrupted := markersOf(t, old, MarkerInterrupted)
	if len(interrupted) != 1 || interrupted[0].TurnID != "g1.t1" {
		t.Fatalf("interrupted markers in the old transcript = %+v", interrupted)
	}
	if len(markersOf(t, old, MarkerSettingsChanged)) == 0 {
		t.Fatalf("the pending change left no marker in the old transcript: %+v", old)
	}
	var resolved []RequestData
	for _, rec := range old {
		var data RequestData
		if rec.Kind == KindPermission && json.Unmarshal(rec.Data, &data) == nil && data.Stage == StageResolved {
			resolved = append(resolved, data)
		}
	}
	if len(resolved) != 1 || resolved[0].RequestID != "req-1" || resolved[0].Outcome != RequestInterrupted {
		t.Fatalf("request resolutions = %+v", resolved)
	}

	// Subscribers saw the committed settings and the cut turn under the old
	// conversation before their stream was ended for the reset.
	events := drain(t, sub)
	if !sub.ConversationReset() {
		t.Fatal("subscription not ended by the reset")
	}
	sawSettings, sawMarker := false, false
	for _, ev := range events {
		if ev.Kind == EventState && ev.State.ConversationID == before.ConversationID && ev.State.Settings.Model == "sonnet" {
			sawSettings = true
		}
		if ev.Kind == EventRecord && ev.Record.ConversationID == before.ConversationID && ev.Record.Kind == KindMarker && ev.Record.TurnID == "g1.t1" {
			sawMarker = true
		}
		if ev.Kind == EventState && ev.State.ConversationID != before.ConversationID {
			t.Fatalf("an old-conversation subscriber received the new conversation's state: %+v", ev.State)
		}
	}
	if !sawSettings || !sawMarker {
		t.Fatalf("events before reset: settings %v marker %v", sawSettings, sawMarker)
	}
}

func TestCoordinator_ResetDuringStartingCancelsTheLaunch(t *testing.T) {
	gate := make(chan struct{})
	launcher := &fakeLauncher{gate: gate}
	c := newTestCoordinator(t, t.TempDir(), launcher)
	chooseSettings(t, c)
	done := make(chan error, 1)
	go func() { _, err := c.Send(context.Background(), "first", "", "cm-1"); done <- err }()
	before := waitLifecycle(t, c, LifecycleStarting)

	res, err := c.Reset()
	if err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if res.Result != ResetDone || res.PreviousConversationID != before.ConversationID ||
		res.State.Lifecycle != LifecycleStopped || res.State.ConversationID == before.ConversationID || res.State.HeadSeq != 0 {
		t.Fatalf("reset during starting = %+v", res)
	}
	close(gate)
	var launch *LaunchFailedError
	if err := <-done; !errors.As(err, &launch) {
		t.Fatalf("send joined to the cancelled launch = %v", err)
	}
	waitFor(t, "cancelled launch stopped", func() bool {
		launcher.mu.Lock()
		started := len(launcher.sessions) == 1
		launcher.mu.Unlock()
		return started && launcher.launchCount() == 1 && launcher.session(0).Stops() == 1
	})
	if st := c.State(); st.HeadSeq != 0 || st.Lifecycle != LifecycleStopped || st.Generation != 0 || st.LastTurnOutcome != OutcomeNone {
		t.Fatalf("state after the cancelled launch settled = %+v", st)
	}
}

func TestCoordinator_ResetOnEmptyConversationIsANoop(t *testing.T) {
	dir := t.TempDir()
	c := newTestCoordinator(t, dir, &fakeLauncher{})
	chooseSettings(t, c)
	before := c.State()
	sub, err := c.Subscribe(0, false, "")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Unsubscribe(sub)
	res, err := c.Reset()
	if err != nil {
		t.Fatal(err)
	}
	if res.Result != ResetNoop || res.PreviousConversationID != before.ConversationID ||
		res.State.ConversationID != before.ConversationID || res.State.StreamEpoch != before.StreamEpoch {
		t.Fatalf("noop reset = %+v", res)
	}
	entries, err := os.ReadDir(filepath.Join(dir, supervisorDirName, conversationsDir))
	if err != nil || len(entries) != 1 || entries[0].Name() != before.ConversationID {
		t.Fatalf("conversation dirs after noop = %v %v", entries, err)
	}
	if sub.ConversationReset() {
		t.Fatal("a noop reset ended the subscription")
	}
	select {
	case _, open := <-sub.Events():
		if !open {
			t.Fatal("a noop reset closed the subscription")
		}
	default:
	}
}
