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

package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

func countReviewerBuilds(captured []BuildSessionOpts) int {
	n := 0
	for _, opts := range captured {
		if opts.Model == "reviewer" {
			n++
		}
	}
	return n
}

func newFRLifecycleFeature(t *testing.T, env frLoopTestEnv, id string) (*feature.Store, *feature.Feature, string, string) {
	t.Helper()
	store, f, _ := newFRTestFeature(t, env.stateDir, id, []string{"api"})
	f.Pipeline = feature.PipelineMedium
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}
	artDir := frArtifactDir(env.stateDir, f)
	if err := os.MkdirAll(artDir, 0o755); err != nil {
		t.Fatalf("mkdir artifact: %v", err)
	}
	observeDir := filepath.Join(env.stateDir, "observe")
	if err := os.MkdirAll(filepath.Join(observeDir, f.ID), 0o755); err != nil {
		t.Fatalf("mkdir observe dir: %v", err)
	}
	return store, f, artDir, observeDir
}

// TestRunFeatureFinalReviewLoop_HardProviderFailureFailsFast proves an
// unreachable provider ends Final Review after one round with one session per
// validator: no per-validator retries and no second validation round.
func TestRunFeatureFinalReviewLoop_HardProviderFailureFailsFast(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	env := newFRLoopEnv(t)
	store, f, _, _ := newFRLifecycleFeature(t, env, "fr-provider-down")

	reviewScript := testutil.WriteScript(t, env.scriptsDir, "review-down.sh",
		testutil.JSONLInit+"\n"+
			`echo '{"type":"result","subtype":"error","is_error":true,"session_id":"mock","result":"API Error: Cannot connect to API: Unable to connect"}'`+"\n")
	bs, captured := capturingBuildSessionByModel(map[string]string{"reviewer": reviewScript})

	sm := session.NewManager(make(chan any, 100))
	defer sm.Shutdown()

	result, err := runFeatureFinalReviewLoop(OrchestratorConfig{
		Feature:        f,
		FeatureStore:   store,
		StateDir:       env.stateDir,
		ReviewModel:    "reviewer",
		MaxIterations:  5,
		MaxConsecFails: 3,
		BuildSession:   bs,
		SkillsDir:      filepath.Join(env.stateDir, "skills"),
		GuidelinesDir:  filepath.Join(env.stateDir, "guidelines"),
	}, sm)
	if err != nil {
		t.Fatalf("runFeatureFinalReviewLoop() error = %v", err)
	}
	if result.FinalStatus != "failed" {
		t.Fatalf("FinalStatus = %q, want failed; LastError=%q", result.FinalStatus, result.LastError)
	}
	if result.Iterations != 1 {
		t.Fatalf("Iterations = %d, want 1 (no round restart)", result.Iterations)
	}
	if !strings.Contains(result.LastError, "Cannot connect to API") || !strings.Contains(result.LastError, "retry") {
		t.Fatalf("LastError = %q, want the provider diagnostic and retry remediation", result.LastError)
	}
	if got := countReviewerBuilds(*captured); got != 3 {
		t.Fatalf("reviewer sessions = %d, want 3 (one per validator, no retries)", got)
	}
}

// TestRunFeatureFinalReviewLoop_PauseStopCancelsValidators reproduces
// pause-stop during Final Review: the feature transitions to Interrupted and
// its sessions are stopped. The round must end as interrupted, record the
// sessions as interrupted, and never relaunch -retry-02 validators.
func TestRunFeatureFinalReviewLoop_PauseStopCancelsValidators(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	env := newFRLoopEnv(t)
	store, f, _, observeDir := newFRLifecycleFeature(t, env, "fr-pause-stop")

	reviewScript := testutil.WriteScript(t, env.scriptsDir, "review-slow.sh",
		testutil.JSONLInit+"\nsleep 60\n")
	bs, captured := capturingBuildSessionByModel(map[string]string{"reviewer": reviewScript})

	sm := session.NewManager(make(chan any, 100))
	defer sm.Shutdown()

	type loopOutcome struct {
		result *FeatureFinalReviewResult
		err    error
	}
	done := make(chan loopOutcome, 1)
	go func() {
		result, err := runFeatureFinalReviewLoop(OrchestratorConfig{
			Feature:        f,
			FeatureStore:   store,
			StateDir:       env.stateDir,
			ReviewModel:    "reviewer",
			MaxIterations:  5,
			MaxConsecFails: 3,
			BuildSession:   bs,
			SkillsDir:      filepath.Join(env.stateDir, "skills"),
			GuidelinesDir:  filepath.Join(env.stateDir, "guidelines"),
			Observer:       observerFromModule(t, true, observeDir, "test"),
		}, sm)
		done <- loopOutcome{result, err}
	}()

	deadline := time.Now().Add(10 * time.Second)
	for len(sm.FeatureSessions(f.ID)) < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("validators did not start; sessions=%d", len(sm.FeatureSessions(f.ID)))
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Same order as Orchestrator.interruptFeature: transition first, then
	// stop every feature session.
	if err := store.Modify(f.ID, func(ff *feature.Feature) error {
		return ff.Transition(feature.StatusInterrupted)
	}); err != nil {
		t.Fatalf("transition to interrupted: %v", err)
	}
	for _, s := range sm.FeatureSessions(f.ID) {
		_ = sm.StopSession(s.ID())
	}

	var outcome loopOutcome
	select {
	case outcome = <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("final review loop did not return after pause-stop")
	}
	if outcome.err != nil {
		t.Fatalf("runFeatureFinalReviewLoop() error = %v", outcome.err)
	}
	if outcome.result.FinalStatus != "interrupted" {
		t.Fatalf("FinalStatus = %q, want interrupted; LastError=%q", outcome.result.FinalStatus, outcome.result.LastError)
	}
	if got := countReviewerBuilds(*captured); got != 3 {
		t.Fatalf("reviewer sessions = %d, want 3 (no -retry-02 relaunch after stop)", got)
	}
	for _, s := range sm.FeatureSessions(f.ID) {
		if strings.Contains(s.ID(), "-retry-") {
			t.Errorf("session %s relaunched after stop", s.ID())
		}
		if s.Status() == ports.SessionRunning {
			t.Errorf("session %s still Running after stop", s.ID())
		}
	}

	ended := filterEventsByType(readObserveEvents(t, observeDir, f.ID), "session.ended")
	if len(ended) != 3 {
		t.Fatalf("session.ended events = %d, want 3", len(ended))
	}
	for _, evt := range ended {
		if evt.Status != observe.SessionEndedStatusInterrupted {
			t.Errorf("session %s ended with status %q, want interrupted", evt.SessionID, evt.Status)
		}
	}

	loaded, err := store.Load(f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != feature.StatusInterrupted {
		t.Fatalf("status = %s, want Interrupted", loaded.Status)
	}
	if len(loaded.ValidatorStatuses) != 0 {
		t.Fatalf("validator statuses after stop = %v, want cleared (none running)", loaded.ValidatorStatuses)
	}
}
