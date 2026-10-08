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
	"sync"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// allRecords reads the whole transcript.
func allRecords(t *testing.T, c *Coordinator) []Record {
	t.Helper()
	recs, err := c.store.after(0)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	return recs
}

func markersOf(t *testing.T, recs []Record, marker string) []Record {
	t.Helper()
	var out []Record
	for _, rec := range recs {
		if rec.Kind != KindMarker {
			continue
		}
		var data MarkerData
		if err := json.Unmarshal(rec.Data, &data); err != nil {
			t.Fatalf("decode marker %d: %v", rec.Seq, err)
		}
		if data.Marker == marker {
			out = append(out, rec)
		}
	}
	return out
}

func assertLaunchFailureMarker(t *testing.T, c *Coordinator) {
	t.Helper()
	recs := allRecords(t, c)
	for _, rec := range recs {
		if rec.Kind == KindUser {
			t.Fatalf("a failed launch committed a user record: %+v", rec)
		}
	}
	markers := markersOf(t, recs, MarkerError)
	if len(markers) == 0 {
		t.Fatalf("no error marker after a failed launch: %+v", recs)
	}
	var data MarkerData
	_ = json.Unmarshal(markers[len(markers)-1].Data, &data)
	if data.Code != string(errcat.SupervisorLaunchFailed) || data.Text == "" || markers[len(markers)-1].Visibility != VisibilityDisplayOnly {
		t.Fatalf("error marker = %+v (%s)", data, markers[len(markers)-1].Visibility)
	}
}

// openUnmanaged opens a coordinator the test abandons without Close, the
// way a crashed server leaves it.
func openUnmanaged(t *testing.T, stateDir string, launcher Launcher) *Coordinator {
	t.Helper()
	c, err := New(Options{StateDir: stateDir, Catalog: acceptAllCatalog{}, Launcher: launcher})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestCoordinator_BootAfterMidTurnShutdownMarksTurnInterrupted(t *testing.T) {
	for _, mode := range []string{"closed", "abandoned"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			launcher := &fakeLauncher{}
			first := openUnmanaged(t, dir, launcher)
			chooseSettings(t, first)
			if _, err := first.Send(context.Background(), "hello", "", "cm-1"); err != nil {
				t.Fatal(err)
			}
			launcher.session(0).emit(assistantText("m1", "partial"))
			if mode == "closed" {
				if err := first.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				t.Cleanup(func() { _ = first.Close() })
			}

			second := newTestCoordinator(t, dir, &fakeLauncher{})
			st := second.State()
			if st.Lifecycle != LifecycleStopped || st.LastTurnOutcome != OutcomeInterrupted || st.InterruptedBy != InterruptedByShutdown {
				t.Fatalf("state after restart = %+v", st)
			}
			recs := allRecords(t, second)
			last := recs[len(recs)-1]
			var data MarkerData
			_ = json.Unmarshal(last.Data, &data)
			if last.Kind != KindMarker || data.Marker != MarkerInterrupted || last.TurnID != "g1.t1" || last.Visibility != VisibilityDisplayOnly {
				t.Fatalf("last record = %+v %+v", last, data)
			}
			if got := len(markersOf(t, recs, MarkerInterrupted)); got != 1 {
				t.Fatalf("interrupted markers = %d, want 1", got)
			}

			// A third boot finds the record cleared and appends nothing.
			head := second.State().HeadSeq
			if err := second.Close(); err != nil {
				t.Fatal(err)
			}
			third := newTestCoordinator(t, dir, &fakeLauncher{})
			if got := third.State(); got.HeadSeq != head {
				t.Fatalf("second restart appended records: head %d -> %d", head, got.HeadSeq)
			}
		})
	}
}

func TestCoordinator_BootAfterIdleShutdownAppendsNothing(t *testing.T) {
	dir := t.TempDir()
	launcher := &fakeLauncher{}
	first := openUnmanaged(t, dir, launcher)
	chooseSettings(t, first)
	if _, err := first.Send(context.Background(), "hello", "", "cm-1"); err != nil {
		t.Fatal(err)
	}
	launcher.session(0).emit(assistantText("m1", "done"))
	launcher.session(0).emit(successResult())
	waitLifecycle(t, first, LifecycleIdle)
	head := first.State().HeadSeq
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second := newTestCoordinator(t, dir, &fakeLauncher{})
	if st := second.State(); st.LastTurnOutcome != OutcomeNone || st.InterruptedBy != InterruptedByNone || st.HeadSeq != head {
		t.Fatalf("state after idle restart = %+v (head was %d)", st, head)
	}
}

func TestCoordinator_BootResolvesUnansweredRequestsAsInterrupted(t *testing.T) {
	dir := t.TempDir()
	launcher := &fakeLauncher{}
	first := openUnmanaged(t, dir, launcher)
	t.Cleanup(func() { _ = first.Close() })
	chooseSettings(t, first)
	if _, err := first.Send(context.Background(), "run it", "", "cm-1"); err != nil {
		t.Fatal(err)
	}
	launcher.session(0).emit(llm.SDKMessage{Type: "control_request", ControlRequest: &llm.ControlRequestMessage{
		RequestID: "req-1",
		Request:   llm.ControlRequest{Subtype: "can_use_tool", ToolName: "Bash", Input: json.RawMessage(`{"command":"ls"}`)},
	}})
	waitLifecycle(t, first, LifecycleWaitingPermission)

	second := newTestCoordinator(t, dir, &fakeLauncher{})
	st := second.State()
	if len(st.PendingRequests) != 0 || st.Lifecycle != LifecycleStopped {
		t.Fatalf("state after restart = %+v", st)
	}
	var resolved []RequestData
	for _, rec := range allRecords(t, second) {
		if rec.Kind != KindPermission {
			continue
		}
		var data RequestData
		_ = json.Unmarshal(rec.Data, &data)
		if data.Stage == StageResolved {
			if rec.TurnID != "g1.t1" {
				t.Fatalf("resolution turn = %q", rec.TurnID)
			}
			resolved = append(resolved, data)
		}
	}
	if len(resolved) != 1 || resolved[0].Outcome != RequestInterrupted || resolved[0].RequestID != "req-1" || resolved[0].ToolName != "Bash" {
		t.Fatalf("resolutions = %+v", resolved)
	}
}

func TestCoordinator_RestartKeepsTheGenerationFence(t *testing.T) {
	dir := t.TempDir()
	launcher := &fakeLauncher{}
	first := openUnmanaged(t, dir, launcher)
	chooseSettings(t, first)
	if _, err := first.Send(context.Background(), "hello", "", "cm-1"); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second := newTestCoordinator(t, dir, &fakeLauncher{})
	path := filepath.Join(second.store.dir, transcriptFileName)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = second.store.appendRecord(Record{Generation: 0, Kind: KindAssistant, Visibility: VisibilityContent})
	if !errors.Is(err, ErrRetiredGeneration) {
		t.Fatalf("append from generation 0 = %v, want ErrRetiredGeneration", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("a rejected append changed the transcript file")
	}
}

func TestCoordinator_StopAndGraceMarkInterruptedByUser(t *testing.T) {
	t.Run("stop", func(t *testing.T) {
		launcher := &fakeLauncher{}
		c := newTestCoordinator(t, t.TempDir(), launcher)
		chooseSettings(t, c)
		if _, err := c.Send(context.Background(), "hello", "", "cm-1"); err != nil {
			t.Fatal(err)
		}
		c.Interrupt()
		launcher.session(0).emit(successResult())
		st := waitLifecycle(t, c, LifecycleIdle)
		if st.LastTurnOutcome != OutcomeInterrupted || st.InterruptedBy != InterruptedByUser {
			t.Fatalf("state after stop = %+v", st)
		}
	})
	t.Run("grace", func(t *testing.T) {
		launcher := &fakeLauncher{}
		c := newTestCoordinator(t, t.TempDir(), launcher)
		chooseSettings(t, c)
		if _, err := c.Send(context.Background(), "hello", "", "cm-1"); err != nil {
			t.Fatal(err)
		}
		c.Interrupt()
		st := waitLifecycle(t, c, LifecycleStopped)
		if st.LastTurnOutcome != OutcomeInterrupted || st.InterruptedBy != InterruptedByUser {
			t.Fatalf("state after grace = %+v", st)
		}
	})
}

// fakeConverter records rebuilds and answers with a scripted result.
type fakeConverter struct {
	mu     sync.Mutex
	inputs []RebuildInput
	err    error
}

func (f *fakeConverter) Harness() string { return "claude" }

func (f *fakeConverter) Rebuild(_ context.Context, in RebuildInput) (RebuildResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inputs = append(f.inputs, in)
	if f.err != nil {
		return RebuildResult{}, f.err
	}
	for _, rec := range in.Records {
		if rec.Kind == KindUser {
			return RebuildResult{Resume: true, SessionID: in.NativeSessionID, Path: "/rebuilt/" + in.NativeSessionID + ".jsonl"}, nil
		}
	}
	return RebuildResult{SessionID: in.NativeSessionID}, nil
}

func (f *fakeConverter) rebuilds() []RebuildInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]RebuildInput(nil), f.inputs...)
}

func withConverter(conv Converter) func(*Options) {
	return func(o *Options) { o.Converters = map[string]Converter{conv.Harness(): conv} }
}

// completeTurn sends one message on a fresh generation and finishes it.
func completeTurn(t *testing.T, c *Coordinator, launcher *fakeLauncher, cmid string) {
	t.Helper()
	if _, err := c.Send(context.Background(), "message "+cmid, "", cmid); err != nil {
		t.Fatalf("send %s: %v", cmid, err)
	}
	sess := launcher.session(launcher.launchCount() - 1)
	sess.emit(assistantText("a-"+cmid, "reply "+cmid))
	sess.emit(successResult())
	waitLifecycle(t, c, LifecycleIdle)
}

func TestCoordinator_RebuildResumesWithStableNativeID(t *testing.T) {
	conv := &fakeConverter{}
	launcher := &fakeLauncher{}
	c := newTestCoordinator(t, t.TempDir(), launcher, withConverter(conv))
	chooseSettings(t, c)
	sub, err := c.Subscribe(0, false, "")
	if err != nil {
		t.Fatal(err)
	}
	for i, cmid := range []string{"cm-1", "cm-2", "cm-3"} {
		completeTurn(t, c, launcher, cmid)
		if i < 2 {
			c.End()
		}
	}
	rebuilds := conv.rebuilds()
	if len(rebuilds) != 3 {
		t.Fatalf("rebuilds = %d, want 3", len(rebuilds))
	}
	native := rebuilds[0].NativeSessionID
	if native == "" {
		t.Fatal("no native session id was minted")
	}
	for i, in := range rebuilds {
		if in.NativeSessionID != native {
			t.Fatalf("rebuild %d native id = %q, want %q", i, in.NativeSessionID, native)
		}
	}
	if got := launcher.request(0).ResumeSessionID; got != "" {
		t.Fatalf("first launch with no history resumed %q", got)
	}
	for i := 1; i < 3; i++ {
		if got := launcher.request(i).ResumeSessionID; got != native {
			t.Fatalf("launch %d resume = %q, want %q", i, got, native)
		}
	}
	var steps []StartingStep
	c.Unsubscribe(sub)
	for ev := range sub.Events() {
		if ev.Kind == EventState && ev.State.Lifecycle == LifecycleStarting && (len(steps) == 0 || steps[len(steps)-1] != ev.State.StartingStep) {
			steps = append(steps, ev.State.StartingStep)
		}
	}
	if len(steps) < 3 || steps[0] != StepRebuilding || steps[1] != StepLaunching || steps[2] != StepHandshake {
		t.Fatalf("starting steps = %v", steps)
	}
}

func TestCoordinator_ConversionErrorLaunchesFreshWithMarker(t *testing.T) {
	conv := &fakeConverter{err: &ConversionError{Seq: 3, Reason: "unmappable block"}}
	launcher := &fakeLauncher{}
	c := newTestCoordinator(t, t.TempDir(), launcher, withConverter(conv))
	chooseSettings(t, c)
	completeTurn(t, c, launcher, "cm-1")
	if got := launcher.request(0).ResumeSessionID; got != "" {
		t.Fatalf("resume = %q after a conversion error", got)
	}
	if got := len(markersOf(t, allRecords(t, c), MarkerHistoryNotRestored)); got != 1 {
		t.Fatalf("history_not_restored markers = %d, want 1", got)
	}
	if st := c.State(); st.LastTurnOutcome != OutcomeCompleted {
		t.Fatalf("state = %+v", st)
	}
}

func TestCoordinator_RebuildWriteFailureFailsLaunch(t *testing.T) {
	conv := &fakeConverter{err: errors.New("permission denied")}
	launcher := &fakeLauncher{}
	c := newTestCoordinator(t, t.TempDir(), launcher, withConverter(conv))
	chooseSettings(t, c)
	var failed *LaunchFailedError
	if _, err := c.Send(context.Background(), "hello", "", "cm-1"); !errors.As(err, &failed) {
		t.Fatalf("err = %v, want LaunchFailedError", err)
	}
	if launcher.launchCount() != 0 {
		t.Fatal("the process launched after a rebuild write failure")
	}
	st := c.State()
	if st.Lifecycle != LifecycleFailed || st.Failure == nil || st.Settings.Harness != "claude" {
		t.Fatalf("state = %+v", st)
	}
	assertLaunchFailureMarker(t, c)
}

func TestCoordinator_HarnessWithoutConverterSkipsRebuild(t *testing.T) {
	launcher := &fakeLauncher{}
	c := newTestCoordinator(t, t.TempDir(), launcher, func(o *Options) {
		o.Converters = map[string]Converter{"codex": &fakeConverter{}}
	})
	chooseSettings(t, c)
	sub, err := c.Subscribe(0, false, "")
	if err != nil {
		t.Fatal(err)
	}
	completeTurn(t, c, launcher, "cm-1")
	c.Unsubscribe(sub)
	for ev := range sub.Events() {
		if ev.Kind == EventState && ev.State.StartingStep == StepRebuilding {
			t.Fatal("a harness without a converter reported the rebuilding step")
		}
	}
}

func TestCoordinator_FailureClearsOnRestartButMarkerRemains(t *testing.T) {
	dir := t.TempDir()
	launcher := &fakeLauncher{failNext: 1}
	first := openUnmanaged(t, dir, launcher)
	chooseSettings(t, first)
	if _, err := first.Send(context.Background(), "hello", "", "cm-1"); err == nil {
		t.Fatal("scripted launch failure did not fail")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second := newTestCoordinator(t, dir, launcher)
	if st := second.State(); st.Failure != nil || st.Lifecycle != LifecycleStopped {
		t.Fatalf("state after restart = %+v", st)
	}
	assertLaunchFailureMarker(t, second)
}

func TestCoordinator_PermissionModeRestrictedByPolicy(t *testing.T) {
	for _, mode := range []string{"plan", "default"} {
		t.Run(mode, func(t *testing.T) {
			launcher := &fakeLauncher{silent: true}
			c := newTestCoordinator(t, t.TempDir(), launcher)
			chooseSettings(t, c)
			done := make(chan error, 1)
			go func() {
				_, err := c.Send(context.Background(), "hello", "", "cm-1")
				done <- err
			}()
			waitFor(t, "launch", func() bool {
				launcher.mu.Lock()
				defer launcher.mu.Unlock()
				return len(launcher.sessions) == 1
			})
			sess := launcher.session(0)
			sess.emit(llm.SDKMessage{Type: "system", Subtype: "init", Init: &llm.SystemInitMessage{Model: "haiku", PermissionMode: mode}})
			sess.emit(llm.SDKMessage{Type: "system", Subtype: "init", Init: &llm.SystemInitMessage{Model: "haiku", PermissionMode: mode}})
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			st := c.State()
			markers := markersOf(t, allRecords(t, c), MarkerPermissionRestricted)
			wantRestricted := mode != "default"
			if st.PermissionMode.Requested != "default" || st.PermissionMode.Effective != mode || st.PermissionMode.RestrictedByPolicy != wantRestricted {
				t.Fatalf("permission mode = %+v", st.PermissionMode)
			}
			if wantRestricted && len(markers) != 1 || !wantRestricted && len(markers) != 0 {
				t.Fatalf("permission_restricted markers = %d", len(markers))
			}
		})
	}
}
