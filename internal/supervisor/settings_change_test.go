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
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

func settingValue(s string) *string { return &s }

func TestHarnessChangeIdleDefaultsAndRollback(t *testing.T) {
	launcher := &fakeLauncher{}
	c := newTestCoordinator(t, t.TempDir(), launcher)
	chooseSettings(t, c)
	if _, err := c.Send(context.Background(), "first", "", "cm-switch"); err != nil {
		t.Fatal(err)
	}
	launcher.session(0).emit(successResult())
	waitLifecycle(t, c, LifecycleIdle)
	previousID := c.State().NativeSessionID
	st, err := c.ChangeSettings(SettingsChange{Harness: settingValue("codex"), RequestID: "switch", ExpectedGeneration: 1})
	if err != nil {
		t.Fatal(err)
	}
	if st.Lifecycle != LifecycleStopped || st.Settings != (Settings{Harness: "codex", Model: "old"}) || st.NativeSessionID == "" || st.NativeSessionID == previousID {
		t.Fatalf("switch state: %+v", st)
	}
	if launcher.session(0).Stops() != 1 {
		t.Fatal("old session not stopped")
	}
	records := allRecords(t, c)
	markers := markersOf(t, records, MarkerHarnessChange)
	if len(markers) != 1 || len(markersOf(t, records, MarkerSettingsChanged)) != 0 {
		t.Fatal("incorrect switch markers")
	}
	var marker MarkerData
	if err := json.Unmarshal(markers[0].Data, &marker); err != nil {
		t.Fatal(err)
	}
	if marker.Text != "Switched to Codex · old" || marker.FromHarness != "claude" || marker.ToHarness != "codex" {
		t.Fatalf("switch marker: %+v", marker)
	}
	notes := 0
	for _, rec := range records {
		if rec.Kind != KindNote {
			continue
		}
		var note NoteData
		if err := json.Unmarshal(rec.Data, &note); err != nil {
			t.Fatal(err)
		}
		if rec.Visibility != VisibilityModelOnly || !strings.Contains(note.Text, "from Claude to Codex") || !strings.Contains(note.Text, "including tool calls") {
			t.Fatalf("switch note: %+v", note)
		}
		notes++
	}
	if notes != 1 {
		t.Fatalf("switch notes: %d", notes)
	}
	launcher.failNext = 1
	if _, err := c.Send(context.Background(), "second", "", "cm-switch-2"); err == nil {
		t.Fatal("expected launch failure")
	}
	st = c.State()
	if st.Settings.Harness != "claude" || st.NativeSessionID != previousID || st.Failure == nil || st.Failure.AttemptedSettings == nil || st.Failure.AttemptedSettings.Harness != "codex" {
		t.Fatalf("rollback state: %+v", st)
	}
}

func TestHarnessChangeWithoutHistoryClearsNativeID(t *testing.T) {
	c := newTestCoordinator(t, t.TempDir(), &fakeLauncher{})
	chooseSettings(t, c)
	st, err := c.ChangeSettings(SettingsChange{Harness: settingValue("codex"), Model: settingValue("new"), Effort: settingValue("high"), RequestID: "switch-empty", ExpectedGeneration: 0})
	if err != nil {
		t.Fatal(err)
	}
	if st.Settings != (Settings{Harness: "codex", Model: "new", Effort: "high"}) || st.NativeSessionID != "" {
		t.Fatalf("empty switch: %+v", st)
	}
}

func TestHarnessChangeNeverUpdatesInPlace(t *testing.T) {
	for _, source := range []string{"codex", "opencode"} {
		t.Run(source, func(t *testing.T) {
			launcher := &fakeLauncher{}
			c := newTestCoordinator(t, t.TempDir(), launcher)
			if _, err := c.UpdateSettings(Settings{Harness: source, Model: "old"}); err != nil {
				t.Fatal(err)
			}
			if _, err := c.Send(context.Background(), "first", "", "cm-foreign"); err != nil {
				t.Fatal(err)
			}
			launcher.session(0).emit(successResult())
			waitLifecycle(t, c, LifecycleIdle)
			if _, err := c.ChangeSettings(SettingsChange{Harness: settingValue("claude"), RequestID: "switch", ExpectedGeneration: 1}); err != nil {
				t.Fatal(err)
			}
			sess := launcher.session(0)
			sess.mu.Lock()
			updates := sess.settingsUpdates
			sess.mu.Unlock()
			if updates != 0 || sess.Stops() != 1 || c.State().Lifecycle != LifecycleStopped {
				t.Fatalf("updates=%d, stops=%d, state=%+v", updates, sess.Stops(), c.State())
			}
		})
	}
}

func TestInitialHarnessChoiceSurvivesLaunchFailure(t *testing.T) {
	launcher := &fakeLauncher{failNext: 1}
	c := newTestCoordinator(t, t.TempDir(), launcher)
	if _, err := c.ChangeSettings(SettingsChange{Harness: settingValue("claude"), Model: settingValue("haiku"), RequestID: "initial", ExpectedGeneration: 0}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Send(context.Background(), "first", "", "cm-initial"); err == nil {
		t.Fatal("expected launch failure")
	}
	if st := c.State(); st.Settings != (Settings{Harness: "claude", Model: "haiku"}) || st.Failure == nil || st.Failure.AttemptedSettings != nil {
		t.Fatalf("initial settings after failure: %+v", st)
	}
}

func TestHarnessChangePendingAppliesAtBoot(t *testing.T) {
	dir := t.TempDir()
	c := openUnmanaged(t, dir, &fakeLauncher{})
	defer c.Close()
	chooseSettings(t, c)
	if _, err := c.Send(context.Background(), "held", "", "cm-boot-switch"); err != nil {
		t.Fatal(err)
	}
	st, err := c.ChangeSettings(SettingsChange{Harness: settingValue("codex"), RequestID: "switch-boot", ExpectedGeneration: 1})
	if err != nil {
		t.Fatal(err)
	}
	if st.PendingChange == nil || st.PendingChange.Kind != "harness" || st.Settings.Harness != "claude" {
		t.Fatalf("pending switch: %+v", st)
	}
	boot := openUnmanaged(t, dir, &fakeLauncher{})
	defer boot.Close()
	st = boot.State()
	if st.Settings.Harness != "codex" || st.Settings.Model != "old" || st.PendingChange != nil || st.NativeSessionID == "" {
		t.Fatalf("boot switch: %+v", st)
	}
	records := allRecords(t, boot)
	if len(markersOf(t, records, MarkerInterrupted)) != 1 || len(markersOf(t, records, MarkerHarnessChange)) != 1 {
		t.Fatalf("boot markers: %+v", records)
	}
}

func TestSettingsChangeIdleRelaunchAndRollback(t *testing.T) {
	launcher := &fakeLauncher{}
	c := newTestCoordinator(t, t.TempDir(), launcher)
	chooseSettings(t, c)
	if _, err := c.Send(context.Background(), "first", "", "cm-1"); err != nil {
		t.Fatal(err)
	}
	launcher.session(0).emit(successResult())
	waitLifecycle(t, c, LifecycleIdle)
	st, err := c.ChangeSettings(SettingsChange{Model: settingValue("sonnet"), Effort: settingValue("high"), RequestID: "change-1", ExpectedGeneration: 1})
	if err != nil {
		t.Fatal(err)
	}
	if st.Lifecycle != LifecycleStopped || st.Settings.Model != "sonnet" || st.Generation != 1 || st.PendingChange != nil {
		t.Fatalf("changed state: %+v", st)
	}
	if launcher.session(0).Stops() != 1 {
		t.Fatalf("session stops = %d", launcher.session(0).Stops())
	}
	if len(markersOf(t, allRecords(t, c), MarkerSettingsChanged)) != 2 {
		t.Fatal("missing changed markers for model and effort")
	}
	if _, err := c.ChangeSettings(SettingsChange{Model: settingValue("haiku"), RequestID: "change-1", ExpectedGeneration: 1}); err != nil {
		t.Fatal(err)
	}
	if c.State().Settings.Model != "sonnet" {
		t.Fatal("repeated id changed settings")
	}
	launcher.failNext = 1
	if _, err := c.Send(context.Background(), "second", "", "cm-2"); err == nil {
		t.Fatal("expected failed relaunch")
	}
	if st := c.State(); st.Settings.Model != "haiku" || st.Lifecycle != LifecycleFailed {
		t.Fatalf("rollback state: %+v", st)
	}
	if len(markersOf(t, allRecords(t, c), MarkerSettingsReverted)) != 1 {
		t.Fatal("missing reverted marker")
	}
}

func TestSettingsChangePendingCancelAndBoot(t *testing.T) {
	dir := t.TempDir()
	launcher := &fakeLauncher{}
	c := openUnmanaged(t, dir, launcher)
	defer c.Close()
	chooseSettings(t, c)
	if _, err := c.Send(context.Background(), "held", "", "cm-1"); err != nil {
		t.Fatal(err)
	}
	st, err := c.ChangeSettings(SettingsChange{Model: settingValue("sonnet"), RequestID: "change-1", ExpectedGeneration: 1})
	if err != nil {
		t.Fatal(err)
	}
	if st.PendingChange == nil || st.Settings.Model != "haiku" {
		t.Fatalf("pending state: %+v", st)
	}
	var conflict *ChangePendingError
	if _, err := c.ChangeSettings(SettingsChange{Model: settingValue("other"), RequestID: "change-2", ExpectedGeneration: 1}); !errors.As(err, &conflict) || conflict.RequestID != "change-1" {
		t.Fatalf("pending conflict: %v", err)
	}
	if _, err := c.CancelPendingChange("wrong"); !errors.Is(err, ErrPendingChangeNotFound) {
		t.Fatalf("cancel wrong: %v", err)
	}
	if _, err := c.CancelPendingChange("change-1"); err != nil {
		t.Fatal(err)
	}
	launcher.session(0).emit(llm.SDKMessage{Type: "result", Result: &llm.ResultMessage{}})
	waitLifecycle(t, c, LifecycleIdle)
	if c.State().Settings.Model != "haiku" {
		t.Fatal("cancelled change applied")
	}
	if _, err := c.ChangeSettings(SettingsChange{Model: settingValue("sonnet"), RequestID: "change-3", ExpectedGeneration: 1}); err != nil {
		t.Fatal(err)
	}
	// The idle change has already committed; queue a second change during a new turn.
	if _, err := c.Send(context.Background(), "held again", "", "cm-2"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ChangeSettings(SettingsChange{Model: settingValue("haiku"), RequestID: "change-4", ExpectedGeneration: 2}); err != nil {
		t.Fatal(err)
	}
	boot := openUnmanaged(t, dir, &fakeLauncher{})
	defer boot.Close()
	if got := boot.State(); got.Settings.Model != "haiku" || got.PendingChange != nil || got.LastTurnOutcome != OutcomeInterrupted {
		t.Fatalf("boot state: %+v", got)
	}
	records := allRecords(t, boot)
	if len(markersOf(t, records, MarkerInterrupted)) == 0 || len(markersOf(t, records, MarkerSettingsChanged)) != 2 {
		t.Fatalf("boot markers: %+v", records)
	}
}

func TestSettingsChangeStartingQueuesUntilResult(t *testing.T) {
	gate := make(chan struct{})
	launcher := &fakeLauncher{gate: gate}
	c := newTestCoordinator(t, t.TempDir(), launcher)
	chooseSettings(t, c)
	done := make(chan error, 1)
	go func() { _, err := c.Send(context.Background(), "first", "", "cm-1"); done <- err }()
	waitLifecycle(t, c, LifecycleStarting)
	st, err := c.ChangeSettings(SettingsChange{Model: settingValue("sonnet"), RequestID: "change-start", ExpectedGeneration: 1})
	if err != nil {
		t.Fatal(err)
	}
	if st.PendingChange == nil || st.Settings.Model != "haiku" {
		t.Fatalf("starting state: %+v", st)
	}
	close(gate)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	launcher.session(0).emit(successResult())
	waitLifecycle(t, c, LifecycleStopped)
	if st := c.State(); st.Settings.Model != "sonnet" || st.PendingChange != nil {
		t.Fatalf("applied state: %+v", st)
	}
}

func TestSettingsChangeVersionAndValidation(t *testing.T) {
	c := newTestCoordinator(t, t.TempDir(), &fakeLauncher{})
	chooseSettings(t, c)
	var stale *StaleGenerationError
	if _, err := c.ChangeSettings(SettingsChange{Model: settingValue("sonnet"), RequestID: "stale", ExpectedGeneration: 3}); !errors.As(err, &stale) || stale.Current != 0 {
		t.Fatalf("stale error: %v", err)
	}
	var invalid *SettingsInvalidError
	if _, err := c.ChangeSettings(SettingsChange{Model: settingValue("missing"), RequestID: "invalid", ExpectedGeneration: 0}); !errors.As(err, &invalid) {
		t.Fatalf("invalid target: %v", err)
	}
	if _, err := c.ChangeSettings(SettingsChange{RequestID: "noop", ExpectedGeneration: 0}); err != nil {
		t.Fatal(err)
	}
	if c.State().HeadSeq != 0 {
		t.Fatal("no-op wrote a transcript record")
	}
}

func TestSettingsChangeRefusalKeepsRequestIdempotent(t *testing.T) {
	launcher := &fakeLauncher{}
	c := newTestCoordinator(t, t.TempDir(), launcher)
	if _, err := c.UpdateSettings(Settings{Harness: "codex", Model: "old", Effort: "low"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Send(context.Background(), "first", "", "cm-1"); err != nil {
		t.Fatal(err)
	}
	launcher.session(0).emit(successResult())
	waitLifecycle(t, c, LifecycleIdle)
	sess := launcher.session(0)
	sess.mu.Lock()
	sess.settingsError = errors.New("scripted refusal")
	sess.mu.Unlock()
	change := SettingsChange{Model: settingValue("new"), RequestID: "refused", ExpectedGeneration: 1}
	if _, err := c.ChangeSettings(change); err != nil {
		t.Fatal(err)
	}
	if st := c.State(); st.Settings.Model != "old" || st.Lifecycle != LifecycleIdle {
		t.Fatalf("refused state: %+v", st)
	}
	if _, err := c.ChangeSettings(change); err != nil {
		t.Fatal(err)
	}
	sess.mu.Lock()
	updates := sess.settingsUpdates
	sess.mu.Unlock()
	if updates != 1 {
		t.Fatalf("settings updates = %d, want one", updates)
	}
	if len(markersOf(t, allRecords(t, c), MarkerSettingsReverted)) != 1 {
		t.Fatal("missing refusal marker")
	}
}
