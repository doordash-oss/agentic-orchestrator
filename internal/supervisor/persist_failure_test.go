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
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTurnWriteFailureFailsOutcome(t *testing.T) {
	launcher := &fakeLauncher{}
	c := newTestCoordinator(t, t.TempDir(), launcher)
	chooseSettings(t, c)
	if _, err := c.Send(context.Background(), "first", "", "cm-1"); err != nil {
		t.Fatal(err)
	}
	// A closed transcript file makes every later append fail in WriteAt.
	c.store.mu.Lock()
	_ = c.store.file.Close()
	c.store.mu.Unlock()
	sess := launcher.session(0)
	sess.emit(assistantText("msg-1", "lost"))
	sess.emit(successResult())
	st := waitLifecycle(t, c, LifecycleIdle)
	if st.LastTurnOutcome != OutcomeFailed {
		t.Fatalf("outcome = %s, want failed", st.LastTurnOutcome)
	}
	failure := st.PersistFailure
	if failure == nil || failure.Op != "append assistant record" || failure.TurnID != "g1.t1" || failure.Generation != 1 || failure.ConversationID != st.ConversationID {
		t.Fatalf("persist failure: %+v", failure)
	}
	if !errors.Is(failure, os.ErrClosed) {
		t.Fatalf("persist failure cause: %v", failure.Err)
	}
}

func TestPersistFailureOutlivesLaterSuccessfulTurn(t *testing.T) {
	launcher := &fakeLauncher{}
	c := newTestCoordinator(t, t.TempDir(), launcher)
	chooseSettings(t, c)
	if _, err := c.Send(context.Background(), "first", "", "cm-1"); err != nil {
		t.Fatal(err)
	}
	c.store.mu.Lock()
	_ = c.store.file.Close()
	c.store.mu.Unlock()
	sess := launcher.session(0)
	sess.emit(assistantText("msg-1", "lost"))
	sess.emit(successResult())
	waitLifecycle(t, c, LifecycleIdle)
	// Storage recovers; the next turn commits but cannot restore the lost reply.
	c.store.mu.Lock()
	f, err := os.OpenFile(filepath.Join(c.store.dir, transcriptFileName), os.O_RDWR, 0o644)
	if err != nil {
		c.store.mu.Unlock()
		t.Fatal(err)
	}
	c.store.file = f
	c.store.mu.Unlock()
	if _, err := c.Send(context.Background(), "second", "", "cm-2"); err != nil {
		t.Fatal(err)
	}
	sess.emit(assistantText("msg-2", "kept"))
	sess.emit(successResult())
	var st State
	waitFor(t, "completed second turn", func() bool {
		st = c.State()
		return st.Lifecycle == LifecycleIdle && st.LastTurnOutcome == OutcomeCompleted
	})
	if st.PersistFailure == nil || st.PersistFailure.TurnID != "g1.t1" {
		t.Fatalf("persist failure after a later successful turn: %+v", st.PersistFailure)
	}
	if st := c.AcknowledgePersistFailure(); st.PersistFailure != nil {
		t.Fatalf("acknowledged persist failure: %+v", st.PersistFailure)
	}
}

func TestRetiredGenerationOutputIsNotAWriteFailure(t *testing.T) {
	launcher := &fakeLauncher{}
	c := newTestCoordinator(t, t.TempDir(), launcher)
	chooseSettings(t, c)
	if _, err := c.Send(context.Background(), "first", "", "cm-1"); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	err := c.appendProviderLocked(c.conv.Generation-1, KindAssistant, ContentData{}, "stale", "")
	c.mu.Unlock()
	if err != nil {
		t.Fatalf("retired generation append: %v", err)
	}
	launcher.session(0).emit(successResult())
	if st := waitLifecycle(t, c, LifecycleIdle); st.LastTurnOutcome != OutcomeCompleted || st.PersistFailure != nil {
		t.Fatalf("state: %+v", st)
	}
}

func TestPendingChangeFailureKeepsChange(t *testing.T) {
	dir := t.TempDir()
	launcher := &fakeLauncher{}
	c := newTestCoordinator(t, dir, launcher)
	chooseSettings(t, c)
	if _, err := c.Send(context.Background(), "held", "", "cm-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ChangeSettings(SettingsChange{Model: settingValue("sonnet"), RequestID: "change-1", ExpectedGeneration: 1}); err != nil {
		t.Fatal(err)
	}
	// A non-empty directory at the pending-change path makes every rewrite
	// of the record fail.
	path := filepath.Join(dir, supervisorDirName, pendingChangeFileName)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(path, "block"), 0o755); err != nil {
		t.Fatal(err)
	}
	sess := launcher.session(0)
	sess.emit(successResult())
	waitFor(t, "change failure", func() bool { return c.State().PersistFailure != nil })
	st := waitLifecycle(t, c, LifecycleIdle)
	if st.PendingChange == nil || st.PendingChange.RequestID != "change-1" || st.Settings.Model != "haiku" || st.LastTurnOutcome != OutcomeCompleted {
		t.Fatalf("failed change state: %+v", st)
	}
	if !strings.Contains(st.PersistFailure.Op, "change-1") || st.PersistFailure.Generation != 1 {
		t.Fatalf("persist failure: %+v", st.PersistFailure)
	}
	if sess.Stops() != 0 {
		t.Fatal("failed change stopped the session")
	}
	if len(markersOf(t, allRecords(t, c), MarkerError)) != 1 {
		t.Fatal("missing error marker")
	}
	// Once the record can be written again the kept change applies at the
	// next turn end.
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Send(context.Background(), "again", "", "cm-2"); err != nil {
		t.Fatal(err)
	}
	sess.emit(successResult())
	st = waitLifecycle(t, c, LifecycleStopped)
	if st.Settings.Model != "sonnet" || st.PendingChange != nil || st.PersistFailure != nil {
		t.Fatalf("retried change state: %+v", st)
	}
}
