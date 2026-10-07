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
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/workadmission"
)

func TestCoordinator_BootWithNoStateIsStoppedAndLaunchesNothing(t *testing.T) {
	launcher := &fakeLauncher{}
	c := newTestCoordinator(t, t.TempDir(), launcher)
	st := c.State()
	if st.Lifecycle != LifecycleStopped || st.LastTurnOutcome != OutcomeNone || st.HeadSeq != 0 ||
		st.Settings != (Settings{}) || st.SessionID != "" || st.Generation != 0 || st.StartingStep != "" {
		t.Fatalf("boot state = %+v", st)
	}
	if st.ConversationID == "" || st.StreamEpoch == "" {
		t.Fatalf("boot did not allocate a conversation: %+v", st)
	}
	if launcher.launchCount() != 0 {
		t.Fatalf("boot launched %d processes", launcher.launchCount())
	}
}

func TestCoordinator_SettingsAndConversationSurviveRestart(t *testing.T) {
	dir := t.TempDir()
	first := newTestCoordinator(t, dir, &fakeLauncher{})
	if _, err := first.UpdateSettings(Settings{Harness: "claude", Model: "haiku", Effort: "high"}); err != nil {
		t.Fatal(err)
	}
	conversation := first.State().ConversationID
	_ = first.Close()

	second := newTestCoordinator(t, dir, &fakeLauncher{})
	st := second.State()
	if st.Settings != (Settings{Harness: "claude", Model: "haiku", Effort: "high"}) {
		t.Fatalf("settings after restart = %+v", st.Settings)
	}
	if st.ConversationID != conversation {
		t.Fatalf("conversation id changed across restart: %q -> %q", conversation, st.ConversationID)
	}
	if st.Lifecycle != LifecycleStopped {
		t.Fatalf("lifecycle after restart = %s", st.Lifecycle)
	}
}

func TestCoordinator_SettingsLockedWhileProcessExistsAndInvalidChoicesRejected(t *testing.T) {
	launcher := &fakeLauncher{}
	c := newTestCoordinator(t, t.TempDir(), launcher)
	var invalid *SettingsInvalidError
	if _, err := c.UpdateSettings(Settings{Harness: "claude", Model: "missing"}); !errors.As(err, &invalid) {
		t.Fatalf("unknown model err = %v, want SettingsInvalidError", err)
	}
	if _, err := c.UpdateSettings(Settings{Harness: "claude", Model: "haiku", Effort: "bogus"}); !errors.As(err, &invalid) {
		t.Fatalf("unsupported effort err = %v, want SettingsInvalidError", err)
	}
	chooseSettings(t, c)
	if _, err := c.Send(context.Background(), "hi", "", "cm-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.UpdateSettings(Settings{Harness: "claude", Model: "sonnet"}); !errors.Is(err, ErrSettingsLocked) {
		t.Fatalf("settings change with a live process err = %v, want ErrSettingsLocked", err)
	}
	if result, _ := c.End(); result != ActionEnded {
		t.Fatalf("End = %s", result)
	}
	if _, err := c.UpdateSettings(Settings{Harness: "claude", Model: "sonnet"}); err != nil {
		t.Fatalf("settings change after End: %v", err)
	}
}

func TestCoordinator_SendWithoutSettingsIsRefusedAndAppendsNothing(t *testing.T) {
	launcher := &fakeLauncher{}
	c := newTestCoordinator(t, t.TempDir(), launcher)
	if _, err := c.Send(context.Background(), "hi", "", "cm-1"); !errors.Is(err, ErrSettingsRequired) {
		t.Fatalf("err = %v, want ErrSettingsRequired", err)
	}
	if c.State().HeadSeq != 0 || launcher.launchCount() != 0 {
		t.Fatalf("refused send appended or launched: %+v launches=%d", c.State(), launcher.launchCount())
	}
}

func TestCoordinator_ConcurrentSendsOnStoppedConversationStartOneProcessInArrivalOrder(t *testing.T) {
	gate := make(chan struct{})
	launcher := &fakeLauncher{gate: gate}
	c := newTestCoordinator(t, t.TempDir(), launcher)
	chooseSettings(t, c)

	const senders = 12
	type outcome struct {
		res SendResult
		err error
	}
	results := make([]outcome, senders)
	var wg sync.WaitGroup
	for i := range senders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := c.Send(context.Background(), fmt.Sprintf("msg-%d", i), "", fmt.Sprintf("cm-%d", i))
			results[i] = outcome{res, err}
		}()
	}
	// A repeated client message id while the launch is in flight joins
	// without a second record.
	wg.Add(1)
	go func() {
		defer wg.Done()
		waitFor(t, "first joiner", func() bool { return joinerCount(c) > 0 })
		if _, err := c.Send(context.Background(), "msg-0", "", "cm-0"); err != nil {
			t.Errorf("duplicate joiner: %v", err)
		}
	}()
	waitFor(t, "all joiners", func() bool { return joinerCount(c) == senders+1 })
	if st := c.State(); st.Lifecycle != LifecycleStarting || st.StartingStep != StepLaunching {
		t.Fatalf("state while launching = %s/%s", st.Lifecycle, st.StartingStep)
	}
	close(gate)
	wg.Wait()

	launched := 0
	for i, r := range results {
		if r.err != nil {
			t.Fatalf("send %d: %v", i, r.err)
		}
		if r.res.Launched {
			launched++
		}
	}
	if launcher.launchCount() != 1 || launched != 1 {
		t.Fatalf("launches = %d, launched flags = %d; want exactly one", launcher.launchCount(), launched)
	}
	page, err := c.Transcript(PageQuery{HasAfter: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != senders {
		t.Fatalf("user records = %d, want %d (one per client message id)", len(page.Items), senders)
	}
	var committed []string
	for _, rec := range page.Items {
		var data UserData
		_ = json.Unmarshal(rec.Data, &data)
		committed = append(committed, data.Text)
		if rec.Kind != KindUser || rec.Generation != 1 || rec.TurnID == "" {
			t.Fatalf("user record = %+v", rec)
		}
	}
	if got, want := strings.Join(launcher.session(0).Sent(), ","), strings.Join(committed, ","); got != want {
		t.Fatalf("delivery order %q differs from commit order %q", got, want)
	}
	if st := c.State(); st.Lifecycle != LifecycleRunning || st.SessionID != SessionID(st.ConversationID, 1) {
		t.Fatalf("state after joined launch = %+v", st)
	}
}

func joinerCount(c *Coordinator) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.launch == nil {
		return 0
	}
	return len(c.launch.joiners)
}

func TestCoordinator_TurnStreamsDeltasCommitsAssistantAndReturnsIdle(t *testing.T) {
	launcher := &fakeLauncher{}
	c := newTestCoordinator(t, t.TempDir(), launcher)
	chooseSettings(t, c)
	sub, err := c.Subscribe(0, false, "")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Unsubscribe(sub)

	res, err := c.Send(context.Background(), "hello", "", "cm-1")
	if err != nil || !res.Launched || res.Record.Kind != KindUser {
		t.Fatalf("send = %+v, %v", res, err)
	}
	if _, err := c.Send(context.Background(), "again", "", "cm-2"); !errors.Is(err, ErrTurnActive) {
		t.Fatalf("send while running err = %v, want ErrTurnActive", err)
	}
	sess := launcher.session(0)
	sess.emit(llm.SDKMessage{Type: "stream_event", StreamMessageID: "msg_1"})
	sess.emit(llm.SDKMessage{Type: "stream_event", StreamDeltaType: "text", StreamDeltaText: "Hel"})
	sess.emit(llm.SDKMessage{Type: "stream_event", StreamDeltaType: "text", StreamDeltaText: "lo!"})
	sess.emit(assistantText("msg_1", "Hello!"))
	sess.emit(successResult())

	var deltas []Delta
	var assistant *Record
	var lastState *State
	timeout := time.After(5 * time.Second)
	for assistant == nil || lastState == nil || lastState.Lifecycle != LifecycleIdle {
		select {
		case ev := <-sub.Events():
			switch ev.Kind {
			case EventDelta:
				deltas = append(deltas, *ev.Delta)
			case EventRecord:
				if ev.Record.Kind == KindAssistant {
					assistant = ev.Record
				}
			case EventState:
				lastState = ev.State
			}
		case <-timeout:
			t.Fatalf("timed out: deltas=%v assistant=%v state=%+v", deltas, assistant, lastState)
		}
	}
	if len(deltas) != 2 || deltas[0].StreamMessageID != "msg_1" || deltas[1].ChunkIndex != 1 || deltas[0].TurnID != res.Record.TurnID {
		t.Fatalf("deltas = %+v", deltas)
	}
	if assistant.StreamMessageID != "msg_1" || assistant.TurnID != res.Record.TurnID {
		t.Fatalf("assistant record = %+v", assistant)
	}
	if lastState.LastTurnOutcome != OutcomeCompleted || lastState.EffectiveModel != "haiku-effective" {
		t.Fatalf("idle state = %+v", lastState)
	}
	page, _ := c.Transcript(PageQuery{})
	kinds := []RecordKind{}
	for _, rec := range page.Items {
		kinds = append(kinds, rec.Kind)
	}
	if fmt.Sprint(kinds) != "[user assistant]" {
		t.Fatalf("transcript kinds = %v (deltas must not persist)", kinds)
	}
	// The idle process is reused by the next send.
	if res, err := c.Send(context.Background(), "second", "", "cm-3"); err != nil || res.Launched {
		t.Fatalf("second send = %+v, %v", res, err)
	}
	if launcher.launchCount() != 1 || fmt.Sprint(sess.Sent()) != "[hello second]" {
		t.Fatalf("launches=%d sent=%v", launcher.launchCount(), sess.Sent())
	}
}

func TestCoordinator_RepeatedClientMessageIDReturnsCommittedRecord(t *testing.T) {
	launcher := &fakeLauncher{}
	c := newTestCoordinator(t, t.TempDir(), launcher)
	chooseSettings(t, c)
	first, err := c.Send(context.Background(), "hello", "", "cm-1")
	if err != nil {
		t.Fatal(err)
	}
	again, err := c.Send(context.Background(), "hello", "", "cm-1")
	if err != nil || again.Record.Seq != first.Record.Seq || again.Launched {
		t.Fatalf("repeat = %+v, %v", again, err)
	}
	if c.State().HeadSeq != 1 || len(launcher.session(0).Sent()) != 1 {
		t.Fatalf("repeat appended or redelivered: head=%d sent=%v", c.State().HeadSeq, launcher.session(0).Sent())
	}
}

func TestCoordinator_RelaunchUsesNewGenerationAndRejectsRetiredOutput(t *testing.T) {
	launcher := &fakeLauncher{}
	c := newTestCoordinator(t, t.TempDir(), launcher)
	chooseSettings(t, c)
	if _, err := c.Send(context.Background(), "one", "", "cm-1"); err != nil {
		t.Fatal(err)
	}
	launcher.session(0).emit(successResult())
	waitLifecycle(t, c, LifecycleIdle)
	c.End()
	if _, err := c.Send(context.Background(), "two", "", "cm-2"); err != nil {
		t.Fatal(err)
	}
	st := c.State()
	if st.Generation != 2 || st.SessionID != SessionID(st.ConversationID, 2) {
		t.Fatalf("relaunch state = %+v", st)
	}
	if !strings.HasPrefix(st.SessionID, "__supervisor__.") || strings.Contains(st.SessionID, ":") {
		t.Fatalf("manager id %q is not the dotted supervisor form", st.SessionID)
	}
	for i := range 2 {
		req := launcher.request(i)
		if req.Generation != int64(i+1) || !strings.HasSuffix(req.PIDDir, fmt.Sprintf("generations/%d", i+1)) {
			t.Fatalf("launch %d = %+v", i, req)
		}
	}
	head := c.State().HeadSeq
	// Output replayed from the retired first generation lands nowhere.
	launcher.session(0).emit(assistantText("late", "late reply"))
	launcher.session(0).emit(successResult())
	if got := c.State(); got.HeadSeq != head || got.Lifecycle != LifecycleRunning {
		t.Fatalf("retired output changed the conversation: %+v", got)
	}
}

func TestCoordinator_LaunchFailureCommitsNothingAndNextSendRelaunches(t *testing.T) {
	for _, mode := range []string{"launch error", "exit before handshake"} {
		t.Run(mode, func(t *testing.T) {
			launcher := &fakeLauncher{}
			if mode == "launch error" {
				launcher.failNext = 1
			} else {
				launcher.exitBeforeHandshake = true
			}
			c := newTestCoordinator(t, t.TempDir(), launcher)
			chooseSettings(t, c)
			_, err := c.Send(context.Background(), "hello", "", "cm-1")
			var failed *LaunchFailedError
			if !errors.As(err, &failed) {
				t.Fatalf("err = %v, want LaunchFailedError", err)
			}
			st := c.State()
			if st.Lifecycle != LifecycleFailed || st.SessionID != "" || st.Failure == nil {
				t.Fatalf("state after failure = %+v", st)
			}
			assertLaunchFailureMarker(t, c)
			launcher.mu.Lock()
			launcher.exitBeforeHandshake = false
			launcher.mu.Unlock()
			res, err := c.Send(context.Background(), "hello", "", "cm-1")
			if err != nil || !res.Launched || res.Record.Generation != 2 {
				t.Fatalf("retry send = %+v, %v", res, err)
			}
			if st := c.State(); st.Failure != nil {
				t.Fatalf("failure survived the retry: %+v", st.Failure)
			}
		})
	}
}

func TestCoordinator_HandshakeTimeoutFailsLaunch(t *testing.T) {
	launcher := &fakeLauncher{silent: true}
	c := newTestCoordinator(t, t.TempDir(), launcher, func(o *Options) { o.HandshakeTimeout = 50 * time.Millisecond })
	chooseSettings(t, c)
	var failed *LaunchFailedError
	if _, err := c.Send(context.Background(), "hello", "", "cm-1"); !errors.As(err, &failed) {
		t.Fatalf("err = %v, want LaunchFailedError", err)
	}
	if launcher.session(0).Stops() == 0 {
		t.Fatal("a silent harness must be stopped after the handshake timeout")
	}
}

func TestCoordinator_UncleanExitStopsWithFailedOutcome(t *testing.T) {
	launcher := &fakeLauncher{}
	c := newTestCoordinator(t, t.TempDir(), launcher)
	chooseSettings(t, c)
	if _, err := c.Send(context.Background(), "hello", "", "cm-1"); err != nil {
		t.Fatal(err)
	}
	launcher.session(0).exit(ports.SessionFailed)
	st := waitLifecycle(t, c, LifecycleStopped)
	if st.LastTurnOutcome != OutcomeFailed || st.SessionID != "" {
		t.Fatalf("state after crash = %+v", st)
	}
}

func TestCoordinator_PermissionRequestSurfacesAndAnswerResumesTurn(t *testing.T) {
	launcher := &fakeLauncher{}
	c := newTestCoordinator(t, t.TempDir(), launcher)
	chooseSettings(t, c)
	sub, _ := c.Subscribe(0, false, "")
	defer c.Unsubscribe(sub)
	if _, err := c.Send(context.Background(), "run ls", "", "cm-1"); err != nil {
		t.Fatal(err)
	}
	sess := launcher.session(0)
	req := &llm.ControlRequestMessage{Type: "control_request", RequestID: "req-1", Request: llm.ControlRequest{
		Subtype: "can_use_tool", ToolName: "Bash", Input: json.RawMessage(`{"command":"ls"}`),
	}}
	sess.emit(llm.SDKMessage{Type: "control_request", ControlRequest: req})

	sawRequest := false
	timeout := time.After(5 * time.Second)
	for {
		var ev Event
		select {
		case ev = <-sub.Events():
		case <-timeout:
			t.Fatal("timed out waiting for the request and waiting state")
		}
		if ev.Kind == EventRequest && ev.Request.RequestID == "req-1" {
			sawRequest = true
		}
		if ev.Kind == EventState && ev.State.Lifecycle == LifecycleWaitingPermission {
			if !sawRequest {
				t.Fatal("state change arrived before the request event")
			}
			if len(ev.State.PendingRequests) != 1 || ev.State.PendingRequests[0].RequestID != "req-1" {
				t.Fatalf("pending = %+v", ev.State.PendingRequests)
			}
			break
		}
	}
	if !c.Busy() {
		t.Fatal("a supervisor waiting on permission counts as active work")
	}
	sess.answer(ports.ControlAnswer{RequestID: "req-1", ToolName: "Bash", Allowed: true})
	st := c.State()
	if st.Lifecycle != LifecycleRunning || len(st.PendingRequests) != 0 {
		t.Fatalf("state after answer = %+v", st)
	}
	page, _ := c.Transcript(PageQuery{})
	var stages []string
	for _, rec := range page.Items {
		if rec.Kind == KindPermission {
			var data RequestData
			_ = json.Unmarshal(rec.Data, &data)
			if rec.Visibility != VisibilityDisplayOnly {
				t.Fatalf("permission record visibility = %s", rec.Visibility)
			}
			stages = append(stages, data.Stage+"/"+data.Outcome)
		}
	}
	if fmt.Sprint(stages) != "[requested/pending resolved/allowed]" {
		t.Fatalf("permission records = %v", stages)
	}
	// A second request left unanswered is cleared when the turn ends.
	sess.emit(llm.SDKMessage{Type: "control_request", ControlRequest: &llm.ControlRequestMessage{RequestID: "req-2", Request: llm.ControlRequest{ToolName: "AskUserQuestion"}}})
	if got := c.State().Lifecycle; got != LifecycleWaitingQuestion {
		t.Fatalf("question lifecycle = %s", got)
	}
	sess.emit(successResult())
	if st := c.State(); st.Lifecycle != LifecycleIdle || len(st.PendingRequests) != 0 {
		t.Fatalf("turn end did not clear pending: %+v", st)
	}
}

func TestCoordinator_InterruptReturnsAtOnceAndIdlesOnlyAfterResult(t *testing.T) {
	launcher := &fakeLauncher{}
	c := newTestCoordinator(t, t.TempDir(), launcher, func(o *Options) { o.InterruptGrace = time.Minute })
	chooseSettings(t, c)
	if _, err := c.Send(context.Background(), "long task", "", "cm-1"); err != nil {
		t.Fatal(err)
	}
	sess := launcher.session(0)
	sess.emit(llm.SDKMessage{Type: "control_request", ControlRequest: &llm.ControlRequestMessage{RequestID: "req-1", Request: llm.ControlRequest{ToolName: "Bash"}}})
	result, st := c.Interrupt()
	if result != ActionAccepted || sess.Interrupts() != 1 {
		t.Fatalf("interrupt = %s, interrupts = %d", result, sess.Interrupts())
	}
	if st.Lifecycle == LifecycleIdle || len(st.PendingRequests) != 0 {
		t.Fatalf("state right after interrupt = %+v (must not idle yet; pending cleared)", st)
	}
	sess.emit(llm.SDKMessage{Type: "result", Result: &llm.ResultMessage{Subtype: "error_during_execution", IsError: true}})
	st = c.State()
	if st.Lifecycle != LifecycleIdle || st.LastTurnOutcome != OutcomeInterrupted || st.SessionID == "" {
		t.Fatalf("state after interrupted result = %+v", st)
	}
}

func TestCoordinator_InterruptIgnoredTerminatesAfterGrace(t *testing.T) {
	launcher := &fakeLauncher{}
	c := newTestCoordinator(t, t.TempDir(), launcher, func(o *Options) { o.InterruptGrace = 50 * time.Millisecond })
	chooseSettings(t, c)
	if _, err := c.Send(context.Background(), "stubborn", "", "cm-1"); err != nil {
		t.Fatal(err)
	}
	c.Interrupt()
	st := waitLifecycle(t, c, LifecycleStopped)
	if st.LastTurnOutcome != OutcomeInterrupted || launcher.session(0).Stops() == 0 {
		t.Fatalf("state after ignored interrupt = %+v stops=%d", st, launcher.session(0).Stops())
	}
}

func TestCoordinator_EndKeepsTranscriptAndSettings(t *testing.T) {
	launcher := &fakeLauncher{}
	c := newTestCoordinator(t, t.TempDir(), launcher)
	chooseSettings(t, c)
	if result, _ := c.End(); result != ActionNotActive {
		t.Fatalf("End with no process = %s", result)
	}
	if _, err := c.Send(context.Background(), "hello", "", "cm-1"); err != nil {
		t.Fatal(err)
	}
	launcher.session(0).emit(assistantText("m", "hi"))
	launcher.session(0).emit(successResult())
	result, st := c.End()
	if result != ActionEnded || st.Lifecycle != LifecycleStopped || st.HeadSeq != 2 || !st.Settings.Complete() {
		t.Fatalf("End = %s %+v", result, st)
	}
	if launcher.session(0).IsActive() {
		t.Fatal("End must stop the process")
	}
	page, _ := c.Transcript(PageQuery{})
	if len(page.Items) != 2 {
		t.Fatalf("transcript after End has %d records", len(page.Items))
	}
}

func TestCoordinator_ClosedAdmissionRefusesLaunchAndBusyTracksLifecycle(t *testing.T) {
	admission := workadmission.New(workadmission.Options{})
	gate := make(chan struct{})
	launcher := &fakeLauncher{gate: gate}
	c := newTestCoordinator(t, t.TempDir(), launcher, func(o *Options) { o.Admission = admission })
	chooseSettings(t, c)
	if c.Busy() {
		t.Fatal("stopped supervisor is not busy")
	}
	done := make(chan error, 1)
	go func() {
		_, err := c.Send(context.Background(), "hello", "", "cm-1")
		done <- err
	}()
	waitFor(t, "starting", func() bool { return c.State().Lifecycle == LifecycleStarting })
	if !c.Busy() {
		t.Fatal("starting supervisor is busy")
	}
	if total, per := admission.Held(); total != 1 || per[workadmission.CategorySupervisor] != 1 {
		t.Fatalf("launch reservation = %d %v", total, per)
	}
	close(gate)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if total, _ := admission.Held(); total != 0 {
		t.Fatalf("launch reservation not released after registration: %d", total)
	}
	launcher.session(0).emit(successResult())
	waitLifecycle(t, c, LifecycleIdle)
	if c.Busy() {
		t.Fatal("idle supervisor is not busy")
	}
	c.End()
	if !admission.CloseIfQuiesced() {
		t.Fatal("admission did not close")
	}
	_, err := c.Send(context.Background(), "again", "", "cm-2")
	if _, ok := workadmission.AsClosed(err); !ok {
		t.Fatalf("send during closed admission err = %v, want ClosedError", err)
	}
	if launcher.launchCount() != 1 || c.State().Lifecycle != LifecycleStopped {
		t.Fatalf("closed admission launched: %d %+v", launcher.launchCount(), c.State())
	}
}

func TestCoordinator_SubscribeReplaysAfterCursorAndResetsOnBadCursor(t *testing.T) {
	launcher := &fakeLauncher{}
	c := newTestCoordinator(t, t.TempDir(), launcher)
	chooseSettings(t, c)
	if _, err := c.Send(context.Background(), "one", "", "cm-1"); err != nil {
		t.Fatal(err)
	}
	launcher.session(0).emit(assistantText("a", "reply one"))
	launcher.session(0).emit(successResult())
	epoch := c.State().StreamEpoch

	sub, err := c.Subscribe(1, true, epoch)
	if err != nil {
		t.Fatal(err)
	}
	if sub.Reset || len(sub.Replay) != 1 || sub.Replay[0].Seq != 2 {
		t.Fatalf("replay after 1 = reset %v %+v", sub.Reset, sub.Replay)
	}
	c.Unsubscribe(sub)
	for name, q := range map[string]struct {
		after int64
		epoch string
	}{
		"beyond head": {after: 9, epoch: epoch},
		"stale epoch": {after: 1, epoch: "stale"},
	} {
		sub, err := c.Subscribe(q.after, true, q.epoch)
		if err != nil {
			t.Fatal(err)
		}
		if !sub.Reset {
			t.Fatalf("%s: subscription not reset", name)
		}
	}
}

func TestCoordinator_HiddenContextReachesHarnessOnLaunchAndLiveSendsButNotTranscript(t *testing.T) {
	gate := make(chan struct{})
	launcher := &fakeLauncher{gate: gate}
	c := newTestCoordinator(t, t.TempDir(), launcher)
	chooseSettings(t, c)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, err := c.Send(context.Background(), "explain the failure", "BUNDLE-LAUNCH", "cm-1"); err != nil {
			t.Errorf("initiator: %v", err)
		}
	}()
	waitFor(t, "initiator", func() bool { return joinerCount(c) == 1 })
	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, err := c.Send(context.Background(), "and this one", "BUNDLE-JOINED", "cm-2"); err != nil {
			t.Errorf("joiner: %v", err)
		}
	}()
	waitFor(t, "joiner", func() bool { return joinerCount(c) == 2 })
	close(gate)
	wg.Wait()

	sess := launcher.session(0)
	if got := fmt.Sprint(sess.Sent()); got != "[explain the failure and this one]" {
		t.Fatalf("launch deliveries = %s", got)
	}
	if got := fmt.Sprint(sess.Hidden()); got != "[BUNDLE-LAUNCH BUNDLE-JOINED]" {
		t.Fatalf("launch hidden context = %s", got)
	}
	sess.emit(successResult())
	sess.emit(successResult())
	waitLifecycle(t, c, LifecycleIdle)

	if _, err := c.Send(context.Background(), "follow up", "BUNDLE-LIVE", "cm-3"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Send(context.Background(), "follow up", "BUNDLE-LIVE", "cm-3"); err != nil {
		t.Fatalf("repeat: %v", err)
	}
	if got := fmt.Sprint(sess.Hidden()); got != "[BUNDLE-LAUNCH BUNDLE-JOINED BUNDLE-LIVE]" {
		t.Fatalf("hidden context after live send = %s", got)
	}
	page, err := c.Transcript(PageQuery{})
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, rec := range page.Items {
		if strings.Contains(string(rec.Data), "BUNDLE") {
			t.Fatalf("hidden context reached the transcript: %s", rec.Data)
		}
		var data UserData
		_ = json.Unmarshal(rec.Data, &data)
		texts = append(texts, data.Text)
	}
	if got := strings.Join(texts, "|"); got != "explain the failure|and this one|follow up" {
		t.Fatalf("committed user texts = %q", got)
	}
}

func TestCoordinator_HiddenContextOnIncapableSessionIsRefusedBeforeCommit(t *testing.T) {
	launcher := &fakeLauncher{plain: true}
	c := newTestCoordinator(t, t.TempDir(), launcher)
	chooseSettings(t, c)

	if _, err := c.Send(context.Background(), "explain", "BUNDLE", "cm-1"); !errors.Is(err, ErrHiddenContextUnsupported) {
		t.Fatalf("launch send err = %v, want ErrHiddenContextUnsupported", err)
	}
	waitLifecycle(t, c, LifecycleIdle)
	if head := c.State().HeadSeq; head != 0 {
		t.Fatalf("refused launch send appended: head=%d", head)
	}
	if _, err := c.Send(context.Background(), "explain", "BUNDLE", "cm-2"); !errors.Is(err, ErrHiddenContextUnsupported) {
		t.Fatalf("live send err = %v, want ErrHiddenContextUnsupported", err)
	}
	st := c.State()
	if st.HeadSeq != 0 || st.Lifecycle != LifecycleIdle || len(launcher.session(0).Sent()) != 0 {
		t.Fatalf("refused live send changed state: %+v sent=%v", st, launcher.session(0).Sent())
	}
}
