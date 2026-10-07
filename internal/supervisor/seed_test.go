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
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// seedingConverter is a converter for a harness that receives history as a
// seed file, as OpenCode does. It writes nothing; the result names a seed
// path under the conversation directory when the history has a user record.
type seedingConverter struct {
	mu     sync.Mutex
	inputs []RebuildInput
	err    error
}

func (*seedingConverter) Harness() string    { return "opencode" }
func (*seedingConverter) SeedsHistory() bool { return true }

func (s *seedingConverter) Rebuild(_ context.Context, in RebuildInput) (RebuildResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inputs = append(s.inputs, in)
	if s.err != nil {
		return RebuildResult{}, s.err
	}
	for _, rec := range in.Records {
		if rec.Kind == KindUser {
			return RebuildResult{Resume: true, Path: filepath.Join(in.ConversationDir, "seed.txt")}, nil
		}
	}
	return RebuildResult{}, nil
}

func (s *seedingConverter) setErr(err error) {
	s.mu.Lock()
	s.err = err
	s.mu.Unlock()
}

func (s *seedingConverter) rebuilds() []RebuildInput {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]RebuildInput(nil), s.inputs...)
}

// liveSessionIDs reports a fresh live ACP session id on every launch, as the
// OpenCode adapter's init message does.
func liveSessionIDs() func(LaunchRequest) *llm.SystemInitMessage {
	var mu sync.Mutex
	n := 0
	return func(LaunchRequest) *llm.SystemInitMessage {
		mu.Lock()
		defer mu.Unlock()
		n++
		return &llm.SystemInitMessage{SessionID: fmt.Sprintf("ses_live%d", n), Model: "opencode-effective"}
	}
}

func chooseOpenCode(t *testing.T, c *Coordinator) {
	t.Helper()
	if _, err := c.UpdateSettings(Settings{Harness: "opencode", Model: "vendor/model"}); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
}

// startingSteps drains a closed subscription and lists the distinct
// starting steps it reported, in order.
func startingSteps(c *Coordinator, sub *Subscription) []StartingStep {
	c.Unsubscribe(sub)
	var steps []StartingStep
	for ev := range sub.Events() {
		if ev.Kind == EventState && ev.State.Lifecycle == LifecycleStarting && (len(steps) == 0 || steps[len(steps)-1] != ev.State.StartingStep) {
			steps = append(steps, ev.State.StartingStep)
		}
	}
	return steps
}

func TestCoordinator_SeedingHarnessSeedsWithoutNativeID(t *testing.T) {
	conv := &seedingConverter{}
	launcher := &fakeLauncher{init: liveSessionIDs()}
	dir := t.TempDir()
	c := newTestCoordinator(t, dir, launcher, withConverter(conv))
	chooseOpenCode(t, c)

	// A conversation with no content launches with no seed and reports no
	// rebuilding step.
	sub, err := c.Subscribe(0, false, "")
	if err != nil {
		t.Fatal(err)
	}
	completeTurn(t, c, launcher, "cm-1")
	if n := len(conv.rebuilds()); n != 0 {
		t.Fatalf("a launch with no history converted %d times", n)
	}
	if req := launcher.request(0); req.SeedHistoryPath != "" || req.ResumeSessionID != "" {
		t.Fatalf("first launch request seed %q resume %q", req.SeedHistoryPath, req.ResumeSessionID)
	}
	for _, step := range startingSteps(c, sub) {
		if step == StepRebuilding {
			t.Fatal("a launch with nothing to seed reported the rebuilding step")
		}
	}
	if st := c.State(); st.NativeSessionID != "" || st.EffectiveModel != "opencode-effective" {
		t.Fatalf("state after first launch = native %q model %q; the live ACP id must not be adopted", st.NativeSessionID, st.EffectiveModel)
	}

	// Every later generation seeds from the transcript under the
	// conversation directory, with no native id minted or adopted.
	for gen := 2; gen <= 3; gen++ {
		c.End()
		sub, err := c.Subscribe(0, false, "")
		if err != nil {
			t.Fatal(err)
		}
		completeTurn(t, c, launcher, fmt.Sprintf("cm-%d", gen))
		steps := startingSteps(c, sub)
		if len(steps) < 3 || steps[0] != StepRebuilding || steps[1] != StepLaunching || steps[2] != StepHandshake {
			t.Fatalf("generation %d starting steps = %v", gen, steps)
		}
		rebuilds := conv.rebuilds()
		if len(rebuilds) != gen-1 {
			t.Fatalf("generation %d: rebuilds = %d", gen, len(rebuilds))
		}
		in := rebuilds[gen-2]
		if in.NativeSessionID != "" || in.ConversationDir != c.store.dir || in.ConversationDir == "" || in.Model != "vendor/model" {
			t.Fatalf("generation %d rebuild input = native %q dir %q model %q", gen, in.NativeSessionID, in.ConversationDir, in.Model)
		}
		req := launcher.request(gen - 1)
		if want := filepath.Join(c.store.dir, "seed.txt"); req.SeedHistoryPath != want || req.ResumeSessionID != "" {
			t.Fatalf("generation %d launch seed %q resume %q, want seed %q and no resume", gen, req.SeedHistoryPath, req.ResumeSessionID, want)
		}
		if got := c.State().NativeSessionID; got != "" {
			t.Fatalf("generation %d adopted native id %q", gen, got)
		}
	}
	if got := markersOf(t, allRecords(t, c), MarkerHistoryNotRestored); len(got) != 0 {
		t.Fatalf("seeding left %d history_not_restored markers", len(got))
	}
	_ = c.Close()

	reopened := newTestCoordinator(t, dir, &fakeLauncher{}, withConverter(conv))
	if got := reopened.State().NativeSessionID; got != "" {
		t.Fatalf("native id after restart = %q, want empty", got)
	}
}

func TestCoordinator_SeedConversionErrorLaunchesWithoutSeed(t *testing.T) {
	conv := &seedingConverter{}
	launcher := &fakeLauncher{init: liveSessionIDs()}
	c := newTestCoordinator(t, t.TempDir(), launcher, withConverter(conv))
	chooseOpenCode(t, c)
	completeTurn(t, c, launcher, "cm-1")
	c.End()

	conv.setErr(&ConversionError{Seq: 2, Reason: "unmappable block"})
	completeTurn(t, c, launcher, "cm-2")
	if req := launcher.request(1); req.SeedHistoryPath != "" || req.ResumeSessionID != "" {
		t.Fatalf("launch after a conversion error carried seed %q resume %q", req.SeedHistoryPath, req.ResumeSessionID)
	}
	if got := len(markersOf(t, allRecords(t, c), MarkerHistoryNotRestored)); got != 1 {
		t.Fatalf("history_not_restored markers = %d, want 1", got)
	}
	if st := c.State(); st.LastTurnOutcome != OutcomeCompleted || st.NativeSessionID != "" {
		t.Fatalf("state = %+v", st)
	}
}

func TestCoordinator_SeedWriteFailureFailsLaunchAndRetrySeeds(t *testing.T) {
	conv := &seedingConverter{}
	launcher := &fakeLauncher{init: liveSessionIDs()}
	c := newTestCoordinator(t, t.TempDir(), launcher, withConverter(conv))
	chooseOpenCode(t, c)
	completeTurn(t, c, launcher, "cm-1")
	c.End()
	before := len(allRecords(t, c))

	conv.setErr(errors.New("permission denied"))
	var failed *LaunchFailedError
	if _, err := c.Send(context.Background(), "hello", "", "cm-2"); !errors.As(err, &failed) {
		t.Fatalf("err = %v, want LaunchFailedError", err)
	}
	if n := launcher.launchCount(); n != 1 {
		t.Fatalf("launches = %d; the process started after a seed write failure", n)
	}
	st := c.State()
	if st.Lifecycle != LifecycleFailed || st.Failure == nil || st.Settings.Harness != "opencode" || st.Settings.Model != "vendor/model" {
		t.Fatalf("state = %+v", st)
	}
	recs := allRecords(t, c)
	if len(recs) != before+1 || len(markersOf(t, recs, MarkerError)) != 1 {
		t.Fatalf("failed seed launch appended %d records, want only the error marker", len(recs)-before)
	}

	conv.setErr(nil)
	completeTurn(t, c, launcher, "cm-3")
	if req := launcher.request(1); req.SeedHistoryPath == "" || req.ResumeSessionID != "" {
		t.Fatalf("retry launch seed %q resume %q", req.SeedHistoryPath, req.ResumeSessionID)
	}
	if st := c.State(); st.NativeSessionID != "" || st.Lifecycle != LifecycleIdle {
		t.Fatalf("state after retry = %+v", st)
	}
}

// seedingSelfAssigning also claims harness-assigned ids; seeding wins.
type seedingSelfAssigning struct{ seedingConverter }

func (*seedingSelfAssigning) HarnessAssignsSessionID() bool { return true }

func TestCoordinator_SeedingWinsOverHarnessAssignedIDs(t *testing.T) {
	conv := &seedingSelfAssigning{}
	launcher := &fakeLauncher{init: liveSessionIDs()}
	c := newTestCoordinator(t, t.TempDir(), launcher, withConverter(conv))
	chooseOpenCode(t, c)
	completeTurn(t, c, launcher, "cm-1")
	c.End()
	completeTurn(t, c, launcher, "cm-2")
	if got := c.State().NativeSessionID; got != "" {
		t.Fatalf("a seeding converter adopted live session id %q", got)
	}
	if req := launcher.request(1); req.SeedHistoryPath == "" || req.ResumeSessionID != "" {
		t.Fatalf("second launch seed %q resume %q", req.SeedHistoryPath, req.ResumeSessionID)
	}
}
