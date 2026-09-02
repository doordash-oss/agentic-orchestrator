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

package observe

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/autoreview"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// readEvents reads all events from events.jsonl for the given featureID.
func readEvents(t *testing.T, stateDir, featureID string) []Event {
	t.Helper()
	eventsPath := filepath.Join(stateDir, featureID, "events.jsonl")
	f, err := os.Open(eventsPath)
	if err != nil {
		t.Fatalf("reading events.jsonl: %v", err)
	}
	defer f.Close()
	var events []Event
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var evt Event
		if err := json.Unmarshal(scanner.Bytes(), &evt); err != nil {
			t.Fatalf("parsing event line: %v", err)
		}
		events = append(events, evt)
	}
	return events
}

func TestObserverPhaseLifecycle(t *testing.T) {
	t.Run("enabled_phase_lifecycle", func(t *testing.T) {
		stateDir := t.TempDir()
		featureID := "abc123def456abcd"
		// Create feature dir
		if err := os.MkdirAll(filepath.Join(stateDir, featureID), 0755); err != nil {
			t.Fatal(err)
		}

		obs := New(true, stateDir, false, "", false, "agentic")

		rootSC := SpanContextForFeature(featureID, "", "", "")
		// Verify derived TraceID is zero-padded to 32 chars
		if len(rootSC.TraceID) != 32 {
			t.Errorf("expected TraceID length 32, got %d", len(rootSC.TraceID))
		}
		if rootSC.TraceID != "0000000000000000abc123def456abcd" {
			t.Errorf("expected zero-padded TraceID, got %s", rootSC.TraceID)
		}

		childSC := rootSC.Child()
		obs.PhaseStarted(childSC, "research")
		obs.PhaseCompleted(childSC, "research", 5*time.Second, nil)

		// Read events.jsonl
		eventsPath := filepath.Join(stateDir, featureID, "events.jsonl")
		f, err := os.Open(eventsPath)
		if err != nil {
			t.Fatalf("expected events.jsonl to exist: %v", err)
		}
		defer f.Close()

		var events []Event
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			var evt Event
			if err := json.Unmarshal(scanner.Bytes(), &evt); err != nil {
				t.Fatalf("invalid JSON line: %v", err)
			}
			events = append(events, evt)
		}
		if err := scanner.Err(); err != nil {
			t.Fatal(err)
		}

		if len(events) != 2 {
			t.Fatalf("expected 2 events, got %d", len(events))
		}

		// Verify first event
		if events[0].EventType != "phase.started" {
			t.Errorf("event[0] type = %q, want phase.started", events[0].EventType)
		}
		if events[0].Phase != "research" {
			t.Errorf("event[0] phase = %q, want research", events[0].Phase)
		}
		if events[0].Status != "started" {
			t.Errorf("event[0] status = %q, want started", events[0].Status)
		}

		// Verify second event
		if events[1].EventType != "phase.completed" {
			t.Errorf("event[1] type = %q, want phase.completed", events[1].EventType)
		}
		if events[1].Status != "completed" {
			t.Errorf("event[1] status = %q, want completed", events[1].Status)
		}
		if events[1].DurationMs != 5000 {
			t.Errorf("event[1] duration_ms = %d, want 5000", events[1].DurationMs)
		}

		// Verify TraceID consistency
		if events[0].TraceID != events[1].TraceID {
			t.Errorf("TraceID mismatch across events")
		}
		// Verify SpanID consistency (same phase span)
		if events[0].SpanID != events[1].SpanID {
			t.Errorf("SpanID mismatch across events")
		}
		// Verify ParentSpanID references root
		if events[0].ParentSpanID != rootSC.SpanID {
			t.Errorf("ParentSpanID = %q, want root SpanID %q", events[0].ParentSpanID, rootSC.SpanID)
		}
		if events[0].FeatureID != featureID {
			t.Errorf("FeatureID = %q, want %q", events[0].FeatureID, featureID)
		}
	})

	t.Run("disabled_produces_no_file", func(t *testing.T) {
		stateDir := t.TempDir()
		featureID := "disabled_feature"
		os.MkdirAll(filepath.Join(stateDir, featureID), 0755)

		obs := New(false, stateDir, false, "", false, "agentic")
		sc := SpanContextForFeature(featureID, "", "", "")
		child := sc.Child()
		obs.PhaseStarted(child, "research")
		obs.PhaseCompleted(child, "research", time.Second, nil)

		eventsPath := filepath.Join(stateDir, featureID, "events.jsonl")
		if _, err := os.Stat(eventsPath); !os.IsNotExist(err) {
			t.Errorf("expected events.jsonl to NOT exist for disabled observer")
		}
	})

	t.Run("nil_observer_no_panic", func(t *testing.T) {
		var obs *Observer
		sc := SpanContext{TraceID: "test", SpanID: "test", FeatureID: "test"}
		// These should not panic
		obs.PhaseStarted(sc, "research")
		obs.PhaseCompleted(sc, "research", time.Second, nil)
		obs.SessionStarted(sc, "research", "sess1", "claude", "opus", "", "", "")
		obs.SessionEnded(sc, "research", "sess1", "", SessionUsage{}, time.Second, nil)
		obs.Shutdown()
	})

	t.Run("phase_failed", func(t *testing.T) {
		stateDir := t.TempDir()
		featureID := "fail_feature"
		os.MkdirAll(filepath.Join(stateDir, featureID), 0755)

		obs := New(true, stateDir, false, "", false, "agentic")
		sc := SpanContextForFeature(featureID, "", "", "").Child()

		obs.PhaseStarted(sc, "implement")
		obs.PhaseCompleted(sc, "implement", 3*time.Second, errors.New("build failed"))

		eventsPath := filepath.Join(stateDir, featureID, "events.jsonl")
		f, err := os.Open(eventsPath)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()

		var events []Event
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			var evt Event
			json.Unmarshal(scanner.Bytes(), &evt)
			events = append(events, evt)
		}

		if len(events) != 2 {
			t.Fatalf("expected 2 events, got %d", len(events))
		}
		if events[1].EventType != "phase.failed" {
			t.Errorf("event[1] type = %q, want phase.failed", events[1].EventType)
		}
		if events[1].Status != "failed" {
			t.Errorf("event[1] status = %q, want failed", events[1].Status)
		}
		if events[1].Error != "build failed" {
			t.Errorf("event[1] error = %q, want 'build failed'", events[1].Error)
		}
	})
}

func TestSpanContext(t *testing.T) {
	t.Run("backward_compat_zero_pad", func(t *testing.T) {
		sc := SpanContextForFeature("abc123def456abcd", "", "", "")
		want := "0000000000000000abc123def456abcd"
		if sc.TraceID != want {
			t.Errorf("TraceID = %q, want %q", sc.TraceID, want)
		}
		if sc.FeatureID != "abc123def456abcd" {
			t.Errorf("FeatureID = %q, want abc123def456abcd", sc.FeatureID)
		}
	})

	t.Run("explicit_trace_id", func(t *testing.T) {
		sc := SpanContextForFeature("abc123def456abcd", "custom_trace_id_32chars_here!!!", "", "")
		if sc.TraceID != "custom_trace_id_32chars_here!!!" {
			t.Errorf("TraceID = %q, want explicit value", sc.TraceID)
		}
	})

	t.Run("child_span_context", func(t *testing.T) {
		root := SpanContextForFeature("feat1", "", "", "")
		child := root.Child()
		if child.ParentSpanID != root.SpanID {
			t.Errorf("child.ParentSpanID = %q, want %q", child.ParentSpanID, root.SpanID)
		}
		if child.TraceID != root.TraceID {
			t.Errorf("child.TraceID = %q, want %q", child.TraceID, root.TraceID)
		}
		if child.SpanID == root.SpanID {
			t.Error("child.SpanID should differ from root.SpanID")
		}

		grandchild := child.Child()
		if grandchild.ParentSpanID != child.SpanID {
			t.Errorf("grandchild.ParentSpanID = %q, want %q", grandchild.ParentSpanID, child.SpanID)
		}
	})

	t.Run("new_span_id_uniqueness", func(t *testing.T) {
		ids := make(map[string]bool)
		for i := 0; i < 100; i++ {
			id := NewSpanID()
			if len(id) != 16 {
				t.Errorf("span ID length = %d, want 16", len(id))
			}
			if ids[id] {
				t.Errorf("duplicate span ID: %s", id)
			}
			ids[id] = true
		}
	})
}

func TestSessionUsageZeroValue(t *testing.T) {
	var su SessionUsage
	if su.TotalCostUSD != 0 {
		t.Errorf("TotalCostUSD = %v, want 0", su.TotalCostUSD)
	}
	if su.InputTokens != 0 {
		t.Errorf("InputTokens = %d, want 0", su.InputTokens)
	}
	if su.OutputTokens != 0 {
		t.Errorf("OutputTokens = %d, want 0", su.OutputTokens)
	}
	if su.CacheReadInputTokens != 0 {
		t.Errorf("CacheReadInputTokens = %d, want 0", su.CacheReadInputTokens)
	}
	if su.CacheCreationInputTokens != 0 {
		t.Errorf("CacheCreationInputTokens = %d, want 0", su.CacheCreationInputTokens)
	}
}

func TestSessionEndedAcceptsUsage(t *testing.T) {
	sc := SpanContext{TraceID: "test", SpanID: "test", FeatureID: "test"}
	usage := SessionUsage{TotalCostUSD: 1.5, InputTokens: 100}

	t.Run("enabled_observer", func(t *testing.T) {
		stateDir := t.TempDir()
		featureID := "session_ended_feat"
		os.MkdirAll(filepath.Join(stateDir, featureID), 0755)
		obs := New(true, stateDir, false, "", false, "agentic")
		obs.SessionEnded(sc, "research", "sess1", "", usage, time.Second, nil)
	})

	t.Run("disabled_observer", func(t *testing.T) {
		obs := New(false, t.TempDir(), false, "", false, "agentic")
		obs.SessionEnded(sc, "research", "sess1", "", usage, time.Second, nil)
	})

	t.Run("nil_observer", func(t *testing.T) {
		var obs *Observer
		obs.SessionEnded(sc, "research", "sess1", "", usage, time.Second, nil)
	})
}

func TestIterationEndedAcceptsUsage(t *testing.T) {
	sc := SpanContext{TraceID: "test", SpanID: "test", FeatureID: "test"}
	usage := SessionUsage{TotalCostUSD: 0.5}

	t.Run("enabled_observer", func(t *testing.T) {
		stateDir := t.TempDir()
		featureID := "iter_ended_feat"
		os.MkdirAll(filepath.Join(stateDir, featureID), 0755)
		obs := New(true, stateDir, false, "", false, "agentic")
		obs.IterationEnded(sc, 1, usage, time.Second, "completed")
	})

	t.Run("disabled_observer", func(t *testing.T) {
		obs := New(false, t.TempDir(), false, "", false, "agentic")
		obs.IterationEnded(sc, 1, usage, time.Second, "completed")
	})

	t.Run("nil_observer", func(t *testing.T) {
		var obs *Observer
		obs.IterationEnded(sc, 1, usage, time.Second, "completed")
	})
}

func TestSessionStartedEmitsEvent(t *testing.T) {
	stateDir := t.TempDir()
	featureID := "session_start_feat"
	os.MkdirAll(filepath.Join(stateDir, featureID), 0755)
	obs := New(true, stateDir, false, "", false, "agentic")

	featureCtx := SpanContextForFeature(featureID, "", "", "")
	phaseCtx := featureCtx.Child()
	sessionCtx := phaseCtx.Child()

	obs.SessionStarted(sessionCtx, "Research", "sess-1", "claude", "opus", "my-repo", "", "")

	events := readEvents(t, stateDir, featureID)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	evt := events[0]
	if evt.EventType != "session.started" {
		t.Errorf("EventType = %q, want %q", evt.EventType, "session.started")
	}
	if evt.Phase != "Research" {
		t.Errorf("Phase = %q, want %q", evt.Phase, "Research")
	}
	if evt.SessionID != "sess-1" {
		t.Errorf("SessionID = %q, want %q", evt.SessionID, "sess-1")
	}
	if evt.RepoName != "my-repo" {
		t.Errorf("RepoName = %q, want %q", evt.RepoName, "my-repo")
	}
	if evt.FeatureID != featureID {
		t.Errorf("FeatureID = %q, want %q", evt.FeatureID, featureID)
	}
	if evt.SpanID != sessionCtx.SpanID {
		t.Errorf("SpanID = %q, want %q", evt.SpanID, sessionCtx.SpanID)
	}
	if evt.ParentSpanID != phaseCtx.SpanID {
		t.Errorf("ParentSpanID = %q, want %q (phaseCtx.SpanID)", evt.ParentSpanID, phaseCtx.SpanID)
	}
	if provider, ok := evt.Data["provider"].(string); !ok || provider != "claude" {
		t.Errorf("Data[provider] = %v, want %q", evt.Data["provider"], "claude")
	}
	if model, ok := evt.Data["model"].(string); !ok || model != "opus" {
		t.Errorf("Data[model] = %v, want %q", evt.Data["model"], "opus")
	}
}

func TestSessionEndedEmitsEventWithUsage(t *testing.T) {
	stateDir := t.TempDir()
	featureID := "session_end_feat"
	os.MkdirAll(filepath.Join(stateDir, featureID), 0755)
	obs := New(true, stateDir, false, "", false, "agentic")

	sc := SpanContextForFeature(featureID, "", "", "").Child().Child()
	usage := SessionUsage{
		TotalCostUSD:             0.05,
		InputTokens:              1000,
		OutputTokens:             500,
		CacheReadInputTokens:     200,
		CacheCreationInputTokens: 100,
	}

	obs.SessionEnded(sc, "research", "sess-1", "my-repo", usage, 5*time.Second, nil)

	events := readEvents(t, stateDir, featureID)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	evt := events[0]
	if evt.EventType != "session.ended" {
		t.Errorf("EventType = %q, want %q", evt.EventType, "session.ended")
	}
	if evt.DurationMs != 5000 {
		t.Errorf("DurationMs = %d, want 5000", evt.DurationMs)
	}
	if evt.Status != "ok" {
		t.Errorf("Status = %q, want %q", evt.Status, "ok")
	}
	if evt.Error != "" {
		t.Errorf("Error = %q, want empty", evt.Error)
	}
	if evt.RepoName != "my-repo" {
		t.Errorf("RepoName = %q, want %q", evt.RepoName, "my-repo")
	}
	// Check usage data
	if cost, ok := evt.Data["total_cost_usd"].(float64); !ok || cost != 0.05 {
		t.Errorf("Data[total_cost_usd] = %v, want 0.05", evt.Data["total_cost_usd"])
	}
	if tokens, ok := evt.Data["input_tokens"].(float64); !ok || int(tokens) != 1000 {
		t.Errorf("Data[input_tokens] = %v, want 1000", evt.Data["input_tokens"])
	}
	if tokens, ok := evt.Data["output_tokens"].(float64); !ok || int(tokens) != 500 {
		t.Errorf("Data[output_tokens] = %v, want 500", evt.Data["output_tokens"])
	}
}

func TestSessionEndedEmitsErrorOnFailure(t *testing.T) {
	stateDir := t.TempDir()
	featureID := "session_err_feat"
	os.MkdirAll(filepath.Join(stateDir, featureID), 0755)
	obs := New(true, stateDir, false, "", false, "agentic")

	sc := SpanContextForFeature(featureID, "", "", "").Child().Child()
	obs.SessionEnded(sc, "research", "sess-1", "", SessionUsage{}, time.Second, errors.New("connection lost"))

	events := readEvents(t, stateDir, featureID)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	evt := events[0]
	if evt.Status != "error" {
		t.Errorf("Status = %q, want %q", evt.Status, "error")
	}
	if evt.Error != "connection lost" {
		t.Errorf("Error = %q, want %q", evt.Error, "connection lost")
	}
}

func TestIterationStartedEmitsEvent(t *testing.T) {
	stateDir := t.TempDir()
	featureID := "iter_start_feat"
	os.MkdirAll(filepath.Join(stateDir, featureID), 0755)
	obs := New(true, stateDir, false, "", false, "agentic")

	sc := SpanContextForFeature(featureID, "", "", "").Child()
	obs.IterationStarted(sc, 3)

	events := readEvents(t, stateDir, featureID)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	evt := events[0]
	if evt.EventType != "iteration.started" {
		t.Errorf("EventType = %q, want %q", evt.EventType, "iteration.started")
	}
	if evt.Iteration != 3 {
		t.Errorf("Iteration = %d, want 3", evt.Iteration)
	}
	if evt.FeatureID != featureID {
		t.Errorf("FeatureID = %q, want %q", evt.FeatureID, featureID)
	}
}

func TestIterationEndedEmitsEventWithUsage(t *testing.T) {
	stateDir := t.TempDir()
	featureID := "iter_end_feat"
	os.MkdirAll(filepath.Join(stateDir, featureID), 0755)
	obs := New(true, stateDir, false, "", false, "agentic")

	sc := SpanContextForFeature(featureID, "", "", "").Child()
	usage := SessionUsage{TotalCostUSD: 0.10, InputTokens: 2000, OutputTokens: 800}
	obs.IterationEnded(sc, 3, usage, 10*time.Second, "SUCCESS")

	events := readEvents(t, stateDir, featureID)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	evt := events[0]
	if evt.EventType != "iteration.ended" {
		t.Errorf("EventType = %q, want %q", evt.EventType, "iteration.ended")
	}
	if evt.Iteration != 3 {
		t.Errorf("Iteration = %d, want 3", evt.Iteration)
	}
	if evt.DurationMs != 10000 {
		t.Errorf("DurationMs = %d, want 10000", evt.DurationMs)
	}
	if evt.Status != "SUCCESS" {
		t.Errorf("Status = %q, want %q", evt.Status, "SUCCESS")
	}
	if cost, ok := evt.Data["total_cost_usd"].(float64); !ok || cost != 0.10 {
		t.Errorf("Data[total_cost_usd] = %v, want 0.10", evt.Data["total_cost_usd"])
	}
}

func TestContextHandoffTriggeredEmitsEvent(t *testing.T) {
	stateDir := t.TempDir()
	featureID := "context_handoff_feat"
	os.MkdirAll(filepath.Join(stateDir, featureID), 0755)
	obs := New(true, stateDir, false, "", false, "agentic")

	sc := SpanContextForFeature(featureID, "", "", "").Child()
	obs.ContextHandoffTriggered(sc, "implement", "s1", "repo-a", "codex", 2, 81, 80, 211000, 258400, 12000)

	events := readEvents(t, stateDir, featureID)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	evt := events[0]
	if evt.EventType != "context.handoff_triggered" {
		t.Errorf("EventType = %q, want context.handoff_triggered", evt.EventType)
	}
	if evt.Iteration != 2 || evt.SessionID != "s1" || evt.RepoName != "repo-a" {
		t.Errorf("event identity mismatch: %+v", evt)
	}
	if got := evt.Data["context_pct"]; got != float64(81) {
		t.Errorf("context_pct = %v, want 81", got)
	}
	if got := evt.Data["threshold_pct"]; got != float64(80) {
		t.Errorf("threshold_pct = %v, want 80", got)
	}
}

func TestContextLargeOutputEmitsEvent(t *testing.T) {
	stateDir := t.TempDir()
	featureID := "context_large_output_feat"
	os.MkdirAll(filepath.Join(stateDir, featureID), 0755)
	obs := New(true, stateDir, false, "", false, "agentic")

	sc := SpanContextForFeature(featureID, "", "", "").Child()
	exitCode := 0
	durationMs := int64(123)
	obs.ContextLargeOutput(sc, "implement", "s1", "repo-a", "codex", 2, "rg -n foo", 21000, 20000, &exitCode, &durationMs)

	events := readEvents(t, stateDir, featureID)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	evt := events[0]
	if evt.EventType != "context.large_output" {
		t.Errorf("EventType = %q, want context.large_output", evt.EventType)
	}
	if got := evt.Data["command"]; got != "rg -n foo" {
		t.Errorf("command = %v, want rg command", got)
	}
	if got := evt.Data["output_chars"]; got != float64(21000) {
		t.Errorf("output_chars = %v, want 21000", got)
	}
}

func TestReviewStartedEmitsEvent(t *testing.T) {
	stateDir := t.TempDir()
	featureID := "review_start_feat"
	os.MkdirAll(filepath.Join(stateDir, featureID), 0755)
	obs := New(true, stateDir, false, "", false, "agentic")

	sc := SpanContextForFeature(featureID, "", "", "").Child()
	obs.ReviewStarted(sc, 2)

	events := readEvents(t, stateDir, featureID)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	evt := events[0]
	if evt.EventType != "review.started" {
		t.Errorf("EventType = %q, want %q", evt.EventType, "review.started")
	}
	if evt.Iteration != 2 {
		t.Errorf("Iteration = %d, want 2", evt.Iteration)
	}
}

func TestReviewCompletedEmitsEvent(t *testing.T) {
	stateDir := t.TempDir()
	featureID := "review_end_feat"
	os.MkdirAll(filepath.Join(stateDir, featureID), 0755)
	obs := New(true, stateDir, false, "", false, "agentic")

	sc := SpanContextForFeature(featureID, "", "", "").Child()
	obs.ReviewCompleted(sc, 2, "APPROVED", 30*time.Second)

	events := readEvents(t, stateDir, featureID)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	evt := events[0]
	if evt.EventType != "review.completed" {
		t.Errorf("EventType = %q, want %q", evt.EventType, "review.completed")
	}
	if evt.Iteration != 2 {
		t.Errorf("Iteration = %d, want 2", evt.Iteration)
	}
	if evt.Status != "APPROVED" {
		t.Errorf("Status = %q, want %q", evt.Status, "APPROVED")
	}
	if evt.DurationMs != 30000 {
		t.Errorf("DurationMs = %d, want 30000", evt.DurationMs)
	}
}

func TestAgentTaskStartedEmitsEvent(t *testing.T) {
	stateDir := t.TempDir()
	featureID := "task_start_feat"
	os.MkdirAll(filepath.Join(stateDir, featureID), 0755)
	obs := New(true, stateDir, false, "", false, "agentic")

	sc := SpanContextForFeature(featureID, "", "", "").Child()
	obs.AgentTaskStarted(sc, "Research", "sess-res", "taskX", "toolu_X",
		"Feature state + schema", "local_agent")

	events := readEvents(t, stateDir, featureID)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	evt := events[0]
	if evt.EventType != "agent.task_started" {
		t.Errorf("EventType = %q, want agent.task_started", evt.EventType)
	}
	if evt.Phase != "Research" {
		t.Errorf("Phase = %q", evt.Phase)
	}
	if evt.SessionID != "sess-res" {
		t.Errorf("SessionID = %q", evt.SessionID)
	}
	if evt.DurationMs != 0 {
		t.Errorf("DurationMs = %d, want 0 (no duration at start)", evt.DurationMs)
	}
	if v, _ := evt.Data["task_id"].(string); v != "taskX" {
		t.Errorf("data.task_id = %v", evt.Data["task_id"])
	}
	if v, _ := evt.Data["task_type"].(string); v != "local_agent" {
		t.Errorf("data.task_type = %v", evt.Data["task_type"])
	}
	if _, hasPrompt := evt.Data["prompt"]; hasPrompt {
		t.Error("data.prompt should NOT be persisted to events.jsonl (payload is large)")
	}
}

func TestAgentTaskProgressEmitsEvent(t *testing.T) {
	stateDir := t.TempDir()
	featureID := "task_prog_feat"
	os.MkdirAll(filepath.Join(stateDir, featureID), 0755)
	obs := New(true, stateDir, false, "", false, "agentic")

	featureCtx := SpanContextForFeature(featureID, "", "", "")
	phaseCtx := featureCtx.Child()
	sessionCtx := phaseCtx.Child()

	obs.AgentTaskProgress(sessionCtx, "Knowledge Base", "sess-kb", "task1", "toolu_1",
		"Reading go.mod", "Read", 202895, 96, 2034491)

	events := readEvents(t, stateDir, featureID)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	evt := events[0]
	if evt.EventType != "agent.task_progress" {
		t.Errorf("EventType = %q, want agent.task_progress", evt.EventType)
	}
	if evt.Phase != "Knowledge Base" {
		t.Errorf("Phase = %q", evt.Phase)
	}
	if evt.SessionID != "sess-kb" {
		t.Errorf("SessionID = %q", evt.SessionID)
	}
	if evt.FeatureID != featureID {
		t.Errorf("FeatureID = %q", evt.FeatureID)
	}
	if evt.DurationMs != 2034491 {
		t.Errorf("DurationMs = %d, want 2034491", evt.DurationMs)
	}
	if v, _ := evt.Data["task_id"].(string); v != "task1" {
		t.Errorf("data.task_id = %v", evt.Data["task_id"])
	}
	if v, _ := evt.Data["tool_use_id"].(string); v != "toolu_1" {
		t.Errorf("data.tool_use_id = %v", evt.Data["tool_use_id"])
	}
	if v, _ := evt.Data["description"].(string); v != "Reading go.mod" {
		t.Errorf("data.description = %v", evt.Data["description"])
	}
	if v, _ := evt.Data["last_tool"].(string); v != "Read" {
		t.Errorf("data.last_tool = %v", evt.Data["last_tool"])
	}
	// JSON numerics round-trip as float64.
	if v, _ := evt.Data["total_tokens"].(float64); v != 202895 {
		t.Errorf("data.total_tokens = %v", evt.Data["total_tokens"])
	}
	if v, _ := evt.Data["tool_uses"].(float64); v != 96 {
		t.Errorf("data.tool_uses = %v", evt.Data["tool_uses"])
	}
}

func TestAgentTaskEndedEmitsEventWithStatus(t *testing.T) {
	stateDir := t.TempDir()
	featureID := "task_end_feat"
	os.MkdirAll(filepath.Join(stateDir, featureID), 0755)
	obs := New(true, stateDir, false, "", false, "agentic")

	sc := SpanContextForFeature(featureID, "", "", "").Child()

	obs.AgentTaskEnded(sc, "Knowledge Base", "sess-kb", "task2", "toolu_2", "completed",
		"Re-run conventions research", 3247, 95, 4624037)

	events := readEvents(t, stateDir, featureID)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	evt := events[0]
	if evt.EventType != "agent.task_ended" {
		t.Errorf("EventType = %q, want agent.task_ended", evt.EventType)
	}
	if evt.Status != "completed" {
		t.Errorf("Status = %q, want completed", evt.Status)
	}
	if evt.DurationMs != 4624037 {
		t.Errorf("DurationMs = %d, want 4624037", evt.DurationMs)
	}
	if v, _ := evt.Data["summary"].(string); v != "Re-run conventions research" {
		t.Errorf("data.summary = %v", evt.Data["summary"])
	}
}

func TestNilObserverMethodsAreNoOps(t *testing.T) {
	dir := t.TempDir()
	var obs *Observer
	sc := SpanContext{TraceID: "test", SpanID: "test", FeatureID: "test"}
	usage := SessionUsage{TotalCostUSD: 1.0}

	// None of these should panic
	obs.SessionStarted(sc, "research", "sess1", "claude", "opus", "repo", "", "")
	obs.SessionEnded(sc, "research", "sess1", "repo", usage, time.Second, nil)
	obs.IterationStarted(sc, 1)
	obs.IterationEnded(sc, 1, usage, time.Second, "done")
	obs.ReviewStarted(sc, 1)
	obs.ReviewCompleted(sc, 1, "APPROVED", time.Second)
	obs.AgentTaskStarted(sc, "kb", "s", "t", "u", "d", "local_agent")
	obs.AgentTaskProgress(sc, "kb", "s", "t", "u", "d", "Read", 1, 1, 1)
	obs.AgentTaskEnded(sc, "kb", "s", "t", "u", "completed", "ok", 1, 1, 1)
	obs.FeatureRewound(sc, RewindEventInput{
		TargetPhase:     feature.PhaseImplement,
		EffectiveTarget: feature.PhaseImplement,
		SourceRun:       1,
		NewRun:          2,
	})

	// No events.jsonl should exist
	eventsPath := filepath.Join(dir, "test", "events.jsonl")
	if _, err := os.Stat(eventsPath); !os.IsNotExist(err) {
		t.Errorf("events.jsonl should not exist for nil observer")
	}
}

func TestDisabledObserverMethodsAreNoOps(t *testing.T) {
	stateDir := t.TempDir()
	featureID := "disabled_feat"
	obs := New(false, stateDir, false, "", false, "agentic")
	sc := SpanContext{TraceID: "test", SpanID: "test", FeatureID: featureID}
	usage := SessionUsage{TotalCostUSD: 1.0}

	obs.SessionStarted(sc, "research", "sess1", "claude", "opus", "repo", "", "")
	obs.SessionEnded(sc, "research", "sess1", "repo", usage, time.Second, nil)
	obs.IterationStarted(sc, 1)
	obs.IterationEnded(sc, 1, usage, time.Second, "done")
	obs.ReviewStarted(sc, 1)
	obs.ReviewCompleted(sc, 1, "APPROVED", time.Second)
	obs.AgentTaskStarted(sc, "kb", "s", "t", "u", "d", "local_agent")
	obs.AgentTaskProgress(sc, "kb", "s", "t", "u", "d", "Read", 1, 1, 1)
	obs.AgentTaskEnded(sc, "kb", "s", "t", "u", "completed", "ok", 1, 1, 1)
	obs.FeatureRewound(sc, RewindEventInput{
		TargetPhase:     feature.PhaseImplement,
		EffectiveTarget: feature.PhaseImplement,
		SourceRun:       1,
		NewRun:          2,
	})

	eventsPath := filepath.Join(stateDir, featureID, "events.jsonl")
	if _, err := os.Stat(eventsPath); !os.IsNotExist(err) {
		t.Errorf("events.jsonl should not exist for disabled observer")
	}
}

func TestFeatureRewoundFullOmitRoadmapPhase(t *testing.T) {
	stateDir := t.TempDir()
	featureID := "rewind_full_feat"
	if err := os.MkdirAll(filepath.Join(stateDir, featureID), 0755); err != nil {
		t.Fatal(err)
	}
	obs := New(true, stateDir, false, "", false, "agentic")
	sc := SpanContextForFeature(featureID, "", "", "").WithRun(2)

	obs.FeatureRewound(sc, RewindEventInput{
		TargetPhase:     feature.PhaseImplement,
		EffectiveTarget: feature.PhaseImplement,
		SourceRun:       1,
		NewRun:          2,
		CarriedPhases:   []string{"inquire", "research", "design", "roadmap", "plan"},
		BackupBranches:  map[string]string{"repo-a": "feature/example-backup"},
	})

	events := readEvents(t, stateDir, featureID)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	evt := events[0]
	if evt.EventType != "feature.rewound" {
		t.Errorf("EventType = %q, want feature.rewound", evt.EventType)
	}
	if evt.RunNumber != 2 {
		t.Errorf("RunNumber = %d, want 2", evt.RunNumber)
	}
	if got := evt.Data["rewind_scope"]; got != "full_phase" {
		t.Errorf("Data[rewind_scope] = %v, want full_phase", got)
	}
	if got := evt.Data["target_phase"]; got != "implement" {
		t.Errorf("Data[target_phase] = %v, want implement", got)
	}
	if got := evt.Data["effective_target_phase"]; got != "implement" {
		t.Errorf("Data[effective_target_phase] = %v, want implement", got)
	}
	if got := int(evt.Data["source_run"].(float64)); got != 1 {
		t.Errorf("Data[source_run] = %d, want 1", got)
	}
	if got := int(evt.Data["new_run"].(float64)); got != 2 {
		t.Errorf("Data[new_run] = %d, want 2", got)
	}
	if _, ok := evt.Data["roadmap_phase"]; ok {
		t.Errorf("full rewind event should omit roadmap_phase, got %v", evt.Data["roadmap_phase"])
	}
}

func TestFeatureRewoundPartialIncludesRoadmapContext(t *testing.T) {
	stateDir := t.TempDir()
	featureID := "rewind_partial_feat"
	if err := os.MkdirAll(filepath.Join(stateDir, featureID), 0755); err != nil {
		t.Fatal(err)
	}
	obs := New(true, stateDir, false, "", false, "agentic")
	sc := SpanContextForFeature(featureID, "", "", "").WithRun(2)

	obs.FeatureRewound(sc, RewindEventInput{
		TargetPhase:        feature.PhaseImplement,
		EffectiveTarget:    feature.PhaseImplement,
		RoadmapPhase:       2,
		TotalRoadmapPhases: 4,
		SourceRun:          1,
		NewRun:             2,
		CarriedPhases:      []string{"roadmap", "phase-01/plan", "phase-01/implement"},
		BackupBranches:     map[string]string{"repo-a": "feature/example-backup"},
	})

	events := readEvents(t, stateDir, featureID)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	evt := events[0]
	if evt.EventType != "feature.rewound" {
		t.Errorf("EventType = %q, want feature.rewound", evt.EventType)
	}
	if got := evt.Data["rewind_scope"]; got != "partial_roadmap_phase" {
		t.Errorf("Data[rewind_scope] = %v, want partial_roadmap_phase", got)
	}
	if got := int(evt.Data["roadmap_phase"].(float64)); got != 2 {
		t.Errorf("Data[roadmap_phase] = %d, want 2", got)
	}
	if got := evt.Data["preserved_roadmap_phases"]; got != "Phase 1" {
		t.Errorf("Data[preserved_roadmap_phases] = %v, want Phase 1", got)
	}
	if got := evt.Data["redone_roadmap_phase"]; got != "Phase 2" {
		t.Errorf("Data[redone_roadmap_phase] = %v, want Phase 2", got)
	}
	if got := evt.Data["discarded_roadmap_phases"]; got != "Phases 3-4" {
		t.Errorf("Data[discarded_roadmap_phases] = %v, want Phases 3-4", got)
	}
	if _, ok := evt.Data["backup_branches"].(map[string]any); !ok {
		t.Errorf("Data[backup_branches] = %T, want map", evt.Data["backup_branches"])
	}
}

func TestValidationStartedEmitsEvent(t *testing.T) {
	stateDir := t.TempDir()
	featureID := "val_start_feat"
	os.MkdirAll(filepath.Join(stateDir, featureID), 0755)
	obs := New(true, stateDir, false, "", false, "agentic")

	rootSC := SpanContextForFeature(featureID, "", "", "")
	childSC := rootSC.Child()
	obs.ValidationStarted(childSC, "plan", 3)

	events := readEvents(t, stateDir, featureID)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	evt := events[0]
	if evt.EventType != "validation.started" {
		t.Errorf("EventType = %q, want %q", evt.EventType, "validation.started")
	}
	if evt.Phase != "plan" {
		t.Errorf("Phase = %q, want %q", evt.Phase, "plan")
	}
	if evt.FeatureID != featureID {
		t.Errorf("FeatureID = %q, want %q", evt.FeatureID, featureID)
	}
	if evt.SpanID != childSC.SpanID {
		t.Errorf("SpanID = %q, want %q", evt.SpanID, childSC.SpanID)
	}
	if evt.ParentSpanID != rootSC.SpanID {
		t.Errorf("ParentSpanID = %q, want %q", evt.ParentSpanID, rootSC.SpanID)
	}
	if count, ok := evt.Data["validator_count"].(float64); !ok || int(count) != 3 {
		t.Errorf("Data[validator_count] = %v, want 3", evt.Data["validator_count"])
	}
}

func TestValidationCompletedEmitsEvent(t *testing.T) {
	stateDir := t.TempDir()
	featureID := "val_end_feat"
	os.MkdirAll(filepath.Join(stateDir, featureID), 0755)
	obs := New(true, stateDir, false, "", false, "agentic")

	rootSC := SpanContextForFeature(featureID, "", "", "")
	childSC := rootSC.Child()
	obs.ValidationCompleted(childSC, "plan", "APPROVED", 5*time.Second, 3)

	events := readEvents(t, stateDir, featureID)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	evt := events[0]
	if evt.EventType != "validation.completed" {
		t.Errorf("EventType = %q, want %q", evt.EventType, "validation.completed")
	}
	if evt.Phase != "plan" {
		t.Errorf("Phase = %q, want %q", evt.Phase, "plan")
	}
	if evt.Status != "APPROVED" {
		t.Errorf("Status = %q, want %q", evt.Status, "APPROVED")
	}
	if evt.DurationMs != 5000 {
		t.Errorf("DurationMs = %d, want 5000", evt.DurationMs)
	}
	if evt.FeatureID != featureID {
		t.Errorf("FeatureID = %q, want %q", evt.FeatureID, featureID)
	}
	if count, ok := evt.Data["validator_count"].(float64); !ok || int(count) != 3 {
		t.Errorf("Data[validator_count] = %v, want 3", evt.Data["validator_count"])
	}
}

func TestValidatorStartedEmitsEvent(t *testing.T) {
	stateDir := t.TempDir()
	featureID := "vdator_start_feat"
	os.MkdirAll(filepath.Join(stateDir, featureID), 0755)
	obs := New(true, stateDir, false, "", false, "agentic")

	rootSC := SpanContextForFeature(featureID, "", "", "")
	childSC := rootSC.Child()
	obs.ValidatorStarted(childSC, "Architecture")

	events := readEvents(t, stateDir, featureID)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	evt := events[0]
	if evt.EventType != "validator.started" {
		t.Errorf("EventType = %q, want %q", evt.EventType, "validator.started")
	}
	if evt.FeatureID != featureID {
		t.Errorf("FeatureID = %q, want %q", evt.FeatureID, featureID)
	}
	if evt.SpanID != childSC.SpanID {
		t.Errorf("SpanID = %q, want %q", evt.SpanID, childSC.SpanID)
	}
	if evt.ParentSpanID != rootSC.SpanID {
		t.Errorf("ParentSpanID = %q, want %q", evt.ParentSpanID, rootSC.SpanID)
	}
	if name, ok := evt.Data["validator_name"].(string); !ok || name != "Architecture" {
		t.Errorf("Data[validator_name] = %v, want %q", evt.Data["validator_name"], "Architecture")
	}
}

func TestValidatorCompletedEmitsEvent(t *testing.T) {
	stateDir := t.TempDir()
	featureID := "vdator_end_feat"
	os.MkdirAll(filepath.Join(stateDir, featureID), 0755)
	obs := New(true, stateDir, false, "", false, "agentic")

	rootSC := SpanContextForFeature(featureID, "", "", "")
	childSC := rootSC.Child()
	obs.ValidatorCompleted(childSC, "Architecture", "APPROVED", 2*time.Second)

	events := readEvents(t, stateDir, featureID)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	evt := events[0]
	if evt.EventType != "validator.completed" {
		t.Errorf("EventType = %q, want %q", evt.EventType, "validator.completed")
	}
	if evt.Status != "APPROVED" {
		t.Errorf("Status = %q, want %q", evt.Status, "APPROVED")
	}
	if evt.DurationMs != 2000 {
		t.Errorf("DurationMs = %d, want 2000", evt.DurationMs)
	}
	if evt.FeatureID != featureID {
		t.Errorf("FeatureID = %q, want %q", evt.FeatureID, featureID)
	}
	if name, ok := evt.Data["validator_name"].(string); !ok || name != "Architecture" {
		t.Errorf("Data[validator_name] = %v, want %q", evt.Data["validator_name"], "Architecture")
	}
}

func TestNilObserverLifecycleMethodsAreNoOps(t *testing.T) {
	var obs *Observer
	sc := SpanContext{TraceID: "test", SpanID: "test", FeatureID: "test"}

	// None of these should panic
	obs.ValidationStarted(sc, "plan", 3)
	obs.ValidationCompleted(sc, "plan", "APPROVED", 5*time.Second, 3)
	obs.ValidatorStarted(sc, "Architecture")
	obs.ValidatorCompleted(sc, "Architecture", "APPROVED", 2*time.Second)
}

func TestDisabledObserverLifecycleMethodsAreNoOps(t *testing.T) {
	stateDir := t.TempDir()
	featureID := "disabled_phase4_feat"
	obs := New(false, stateDir, false, "", false, "agentic")
	sc := SpanContext{TraceID: "test", SpanID: "test", FeatureID: featureID}

	obs.ValidationStarted(sc, "plan", 3)
	obs.ValidationCompleted(sc, "plan", "APPROVED", 5*time.Second, 3)
	obs.ValidatorStarted(sc, "Architecture")
	obs.ValidatorCompleted(sc, "Architecture", "APPROVED", 2*time.Second)

	eventsPath := filepath.Join(stateDir, featureID, "events.jsonl")
	if _, err := os.Stat(eventsPath); !os.IsNotExist(err) {
		t.Errorf("events.jsonl should not exist for disabled observer")
	}
}

// ── Phase 5 tests ──────────────────────────────────────────────────────

func TestFeatureStartedEmitsEvent(t *testing.T) {
	stateDir := t.TempDir()
	featureID := "feat_started_feat"
	os.MkdirAll(filepath.Join(stateDir, featureID), 0755)
	obs := New(true, stateDir, false, "", false, "agentic")

	sc := SpanContextForFeature(featureID, "", "", "")
	obs.FeatureStarted(sc, "dark-mode", []string{"frontend", "backend"}, "full")

	events := readEvents(t, stateDir, featureID)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	evt := events[0]
	if evt.EventType != "feature.started" {
		t.Errorf("EventType = %q, want %q", evt.EventType, "feature.started")
	}
	if evt.FeatureID != featureID {
		t.Errorf("FeatureID = %q, want %q", evt.FeatureID, featureID)
	}
	if name, ok := evt.Data["name"].(string); !ok || name != "dark-mode" {
		t.Errorf("Data[name] = %v, want %q", evt.Data["name"], "dark-mode")
	}
	// repos is serialized as []any after JSON round-trip
	rawRepos, ok := evt.Data["repos"].([]any)
	if !ok {
		t.Fatalf("Data[repos] type = %T, want []any", evt.Data["repos"])
	}
	if len(rawRepos) != 2 {
		t.Fatalf("Data[repos] length = %d, want 2", len(rawRepos))
	}
	if rawRepos[0] != "frontend" || rawRepos[1] != "backend" {
		t.Errorf("Data[repos] = %v, want [frontend backend]", rawRepos)
	}
	if pipeline, ok := evt.Data["pipeline"].(string); !ok || pipeline != "full" {
		t.Errorf("Data[pipeline] = %v, want %q", evt.Data["pipeline"], "full")
	}
}

func TestSetupLifecycleEmitsPrePhaseEventWithDocumentedFields(t *testing.T) {
	stateDir := t.TempDir()
	featureID := "setup_lifecycle_feat"
	if err := os.MkdirAll(filepath.Join(stateDir, featureID), 0o755); err != nil {
		t.Fatal(err)
	}
	obs := New(true, stateDir, false, "", false, "agentic")
	sc := SpanContextForFeature(featureID, "", "", "").WithRun(3)

	obs.SetupLifecycle(sc, feature.SetupEvent{
		Kind:       feature.SetupEventTaskCompleted,
		FeatureID:  featureID,
		RunNumber:  3,
		Attempt:    2,
		LogPath:    "/tmp/state/features/setup_lifecycle_feat/runs/run-001/setup/attempt-02-output.txt",
		TaskKey:    "worktree:api",
		TaskKind:   feature.SetupTaskWorktree,
		TaskStatus: feature.SetupStatusDone,
		Repo:       "api",
		Path:       "/tmp/worktrees/setup-lifecycle/api",
		Branch:     "feature/setup-lifecycle",
	})

	events := readEvents(t, stateDir, featureID)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	evt := events[0]
	if evt.EventType != "setup.progress" {
		t.Fatalf("EventType = %q, want setup.progress", evt.EventType)
	}
	if evt.Phase != "" {
		t.Fatalf("Phase = %q, want empty for setup lifecycle", evt.Phase)
	}
	if evt.RunNumber != 3 {
		t.Fatalf("RunNumber = %d, want 3", evt.RunNumber)
	}
	if evt.RepoName != "api" {
		t.Fatalf("RepoName = %q, want api", evt.RepoName)
	}
	wantData := map[string]any{
		"attempt":      float64(2),
		"setup_log":    "/tmp/state/features/setup_lifecycle_feat/runs/run-001/setup/attempt-02-output.txt",
		"setup_task":   "worktree:api",
		"setup_kind":   "worktree",
		"setup_status": "done",
		"repo_name":    "api",
		"path":         "/tmp/worktrees/setup-lifecycle/api",
		"branch":       "feature/setup-lifecycle",
	}
	for key, want := range wantData {
		if got := evt.Data[key]; got != want {
			t.Fatalf("Data[%s] = %#v, want %#v; data=%v", key, got, want, evt.Data)
		}
	}
	if _, ok := evt.Data["task_key"]; ok {
		t.Fatalf("Data contains legacy task_key key: %v", evt.Data)
	}
	if _, ok := evt.Data["log_path"]; ok {
		t.Fatalf("Data contains legacy log_path key: %v", evt.Data)
	}
}

func TestFeatureCompletedEmitsEvent(t *testing.T) {
	stateDir := t.TempDir()
	featureID := "feat_completed_feat"
	os.MkdirAll(filepath.Join(stateDir, featureID), 0755)
	obs := New(true, stateDir, false, "", false, "agentic")

	sc := SpanContextForFeature(featureID, "", "", "")
	obs.FeatureCompleted(sc, 2.50, 120*time.Second)

	events := readEvents(t, stateDir, featureID)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	evt := events[0]
	if evt.EventType != "feature.completed" {
		t.Errorf("EventType = %q, want %q", evt.EventType, "feature.completed")
	}
	if evt.DurationMs != 120000 {
		t.Errorf("DurationMs = %d, want 120000", evt.DurationMs)
	}
	if cost, ok := evt.Data["total_cost_usd"].(float64); !ok || cost != 2.50 {
		t.Errorf("Data[total_cost_usd] = %v, want 2.50", evt.Data["total_cost_usd"])
	}
}

func TestFeatureFailedEmitsEvent(t *testing.T) {
	stateDir := t.TempDir()
	featureID := "feat_failed_feat"
	os.MkdirAll(filepath.Join(stateDir, featureID), 0755)
	obs := New(true, stateDir, false, "", false, "agentic")

	sc := SpanContextForFeature(featureID, "", "", "")
	obs.FeatureFailed(sc, "infrastructure_failure", "blocking", "session timed out after 30m")

	events := readEvents(t, stateDir, featureID)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	evt := events[0]
	if evt.EventType != "feature.failed" {
		t.Errorf("EventType = %q, want %q", evt.EventType, "feature.failed")
	}
	if evt.Error != "session timed out after 30m" {
		t.Errorf("Error = %q, want %q", evt.Error, "session timed out after 30m")
	}
	if code, ok := evt.Data["error_code"].(string); !ok || code != "infrastructure_failure" {
		t.Errorf("Data[error_code] = %v, want %q", evt.Data["error_code"], "infrastructure_failure")
	}
	if class, ok := evt.Data["error_class"].(string); !ok || class != "blocking" {
		t.Errorf("Data[error_class] = %v, want %q", evt.Data["error_class"], "blocking")
	}
	if _, ok := evt.Data["failure_type"]; ok {
		t.Errorf("Data[failure_type] = %v, want no failure_type key", evt.Data["failure_type"])
	}
}

func TestFeatureInterruptedEmitsEvent(t *testing.T) {
	stateDir := t.TempDir()
	featureID := "feat_interrupted_feat"
	os.MkdirAll(filepath.Join(stateDir, featureID), 0755)
	obs := New(true, stateDir, false, "", false, "agentic")

	sc := SpanContextForFeature(featureID, "", "", "")
	obs.FeatureInterrupted(sc, "implement")

	events := readEvents(t, stateDir, featureID)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	evt := events[0]
	if evt.EventType != "feature.interrupted" {
		t.Errorf("EventType = %q, want %q", evt.EventType, "feature.interrupted")
	}
	if evt.Phase != "implement" {
		t.Errorf("Phase = %q, want %q", evt.Phase, "implement")
	}
}

func TestPermissionRequestedEmitsRepoAndIteration(t *testing.T) {
	stateDir := t.TempDir()
	featureID := "perm_req_feat"
	os.MkdirAll(filepath.Join(stateDir, featureID), 0755)
	obs := New(true, stateDir, false, "", false, "agentic")

	sc := SpanContextForFeature(featureID, "", "", "")

	// Build a toolInput longer than 200 chars to verify truncation
	longInput := ""
	for i := 0; i < 250; i++ {
		longInput += "x"
	}
	obs.PermissionRequested(sc, "sess-1", "my-repo", 3, "Bash", longInput)

	events := readEvents(t, stateDir, featureID)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	evt := events[0]
	if evt.EventType != "permission.requested" {
		t.Errorf("EventType = %q, want %q", evt.EventType, "permission.requested")
	}
	if evt.SessionID != "sess-1" {
		t.Errorf("SessionID = %q, want %q", evt.SessionID, "sess-1")
	}
	if evt.RepoName != "my-repo" {
		t.Errorf("RepoName = %q, want %q", evt.RepoName, "my-repo")
	}
	if evt.Iteration != 3 {
		t.Errorf("Iteration = %d, want 3", evt.Iteration)
	}
	if tn, ok := evt.Data["tool_name"].(string); !ok || tn != "Bash" {
		t.Errorf("Data[tool_name] = %v, want %q", evt.Data["tool_name"], "Bash")
	}
	// Verify truncation to 200 chars
	ti, ok := evt.Data["tool_input"].(string)
	if !ok {
		t.Fatalf("Data[tool_input] type = %T, want string", evt.Data["tool_input"])
	}
	if len(ti) != 200 {
		t.Errorf("Data[tool_input] length = %d, want 200 (truncated)", len(ti))
	}
}

func TestPermissionResolvedEmitsRepoAndIteration(t *testing.T) {
	stateDir := t.TempDir()
	featureID := "perm_res_feat"
	os.MkdirAll(filepath.Join(stateDir, featureID), 0755)
	obs := New(true, stateDir, false, "", false, "agentic")

	sc := SpanContextForFeature(featureID, "", "", "")
	obs.PermissionResolved(sc, "sess-2", "backend", 5, "Write", "allow")

	events := readEvents(t, stateDir, featureID)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	evt := events[0]
	if evt.EventType != "permission.resolved" {
		t.Errorf("EventType = %q, want %q", evt.EventType, "permission.resolved")
	}
	if evt.SessionID != "sess-2" {
		t.Errorf("SessionID = %q, want %q", evt.SessionID, "sess-2")
	}
	if evt.RepoName != "backend" {
		t.Errorf("RepoName = %q, want %q", evt.RepoName, "backend")
	}
	if evt.Iteration != 5 {
		t.Errorf("Iteration = %d, want 5", evt.Iteration)
	}
	if tn, ok := evt.Data["tool_name"].(string); !ok || tn != "Write" {
		t.Errorf("Data[tool_name] = %v, want %q", evt.Data["tool_name"], "Write")
	}
	if dec, ok := evt.Data["decision"].(string); !ok || dec != "allow" {
		t.Errorf("Data[decision] = %v, want %q", evt.Data["decision"], "allow")
	}
}

func TestAutomaticReviewCompletedEmitsTypedBoundedEvent(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	featureID := "automatic_review_event"
	if err := os.MkdirAll(filepath.Join(stateDir, featureID), 0o755); err != nil {
		t.Fatal(err)
	}
	obs := New(true, stateDir, false, "", false, "agentic")
	sc := SpanContextForFeature(featureID, "trace-automatic-review", "", "").WithRun(4).Child()
	persisted := false
	obs.AutomaticReviewCompleted(sc, AutomaticReviewEventInput{
		Phase:     "implement",
		SessionID: "session-1",
		RepoName:  "repo-a",
		Iteration: 2,
		Provider:  "claude",
		Model:     "haiku",
		Outcome:   autoreview.OutcomeAllow,
		Duration:  1250 * time.Millisecond,
		ReviewTiming: autoreview.Timing{
			Launch:      125 * time.Millisecond,
			FirstOutput: 450 * time.Millisecond,
			Completion:  900 * time.Millisecond,
		},
		CommandSummary:      "go\t test ./... token=secret-value \x1b]52;c;clipboard\a",
		StatusPersisted:     &persisted,
		StatusFailureClass:  "append_error",
		StatusFailureReason: "sink unavailable token=secret-value",
	})

	events := readEvents(t, stateDir, featureID)
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	event := events[0]
	if event.EventType != "automatic_review.completed" || event.TraceID != sc.TraceID || event.SpanID != sc.SpanID || event.RunNumber != 4 {
		t.Fatalf("event context = %+v", event)
	}
	if event.SessionID != "session-1" || event.RepoName != "repo-a" || event.Phase != "implement" || event.Iteration != 2 || event.DurationMs != 1250 {
		t.Fatalf("event envelope = %+v", event)
	}
	for key, want := range map[string]any{
		"provider":               "claude",
		"model":                  "haiku",
		"outcome":                "allow",
		"command_summary":        "go test ./... token=[redacted]",
		"review_launch_ms":       float64(125),
		"review_first_output_ms": float64(450),
		"review_completion_ms":   float64(900),
		"status_persisted":       false,
		"status_failure_class":   "append_error",
		"status_failure_reason":  "sink unavailable token=[redacted]",
	} {
		if got := event.Data[key]; got != want {
			t.Errorf("event.Data[%q] = %#v, want %#v", key, got, want)
		}
	}
}

func TestAutomaticReviewCompletedNilAndDisabledAreNoOps(t *testing.T) {
	t.Parallel()

	var nilObserver *Observer
	nilObserver.AutomaticReviewCompleted(SpanContext{}, AutomaticReviewEventInput{})
	disabled := New(false, t.TempDir(), false, "", false, "agentic")
	disabled.AutomaticReviewCompleted(SpanContext{}, AutomaticReviewEventInput{})
}

func TestAutomaticReviewUnavailableEmitsBoundedOperatorEvent(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	featureID := "automatic_review_unavailable"
	if err := os.MkdirAll(filepath.Join(stateDir, featureID), 0o755); err != nil {
		t.Fatal(err)
	}
	obs := New(true, stateDir, false, "", false, "agentic")
	sc := SpanContextForFeature(featureID, "trace-automatic-review", "", "").WithRun(2).Child()
	obs.AutomaticReviewUnavailable(sc, AutomaticReviewUnavailableEventInput{
		Phase:     "implement",
		SessionID: "session-1",
		RepoName:  "repo-a",
		Iteration: 3,
		Scope:     "circuit_breaker",
		Reason:    "reviewer unavailable token=secret-value",
	})

	events := readEvents(t, stateDir, featureID)
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	event := events[0]
	if event.EventType != "automatic_review.unavailable" || event.Status != "unavailable" {
		t.Fatalf("event = %+v, want automatic_review.unavailable", event)
	}
	if event.SessionID != "session-1" || event.RepoName != "repo-a" || event.Phase != "implement" || event.Iteration != 3 || event.RunNumber != 2 {
		t.Fatalf("event envelope = %+v", event)
	}
	if got := event.Data["scope"]; got != "circuit_breaker" {
		t.Errorf("event.Data[scope] = %#v, want circuit_breaker", got)
	}
	if got := event.Data["reason"]; got != "reviewer unavailable token=[redacted]" {
		t.Errorf("event.Data[reason] = %#v, want bounded redaction", got)
	}
}

func TestQuestionAskedEmitsRepoAndIteration(t *testing.T) {
	stateDir := t.TempDir()
	featureID := "q_asked_feat"
	os.MkdirAll(filepath.Join(stateDir, featureID), 0755)
	obs := New(true, stateDir, false, "", false, "agentic")

	sc := SpanContextForFeature(featureID, "", "", "")
	obs.QuestionAsked(sc, "sess-3", "api-service", 2, "Which database to use?")

	events := readEvents(t, stateDir, featureID)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	evt := events[0]
	if evt.EventType != "question.asked" {
		t.Errorf("EventType = %q, want %q", evt.EventType, "question.asked")
	}
	if evt.SessionID != "sess-3" {
		t.Errorf("SessionID = %q, want %q", evt.SessionID, "sess-3")
	}
	if evt.RepoName != "api-service" {
		t.Errorf("RepoName = %q, want %q", evt.RepoName, "api-service")
	}
	if evt.Iteration != 2 {
		t.Errorf("Iteration = %d, want 2", evt.Iteration)
	}
	if q, ok := evt.Data["question"].(string); !ok || q != "Which database to use?" {
		t.Errorf("Data[question] = %v, want %q", evt.Data["question"], "Which database to use?")
	}
}

func TestQuestionAnsweredEmitsRepoAndIteration(t *testing.T) {
	stateDir := t.TempDir()
	featureID := "q_answered_feat"
	os.MkdirAll(filepath.Join(stateDir, featureID), 0755)
	obs := New(true, stateDir, false, "", false, "agentic")

	sc := SpanContextForFeature(featureID, "", "", "")
	obs.QuestionAnswered(sc, "sess-3", "api-service", 2, "Which database to use?", "PostgreSQL")

	events := readEvents(t, stateDir, featureID)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	evt := events[0]
	if evt.EventType != "question.answered" {
		t.Errorf("EventType = %q, want %q", evt.EventType, "question.answered")
	}
	if evt.SessionID != "sess-3" {
		t.Errorf("SessionID = %q, want %q", evt.SessionID, "sess-3")
	}
	if evt.RepoName != "api-service" {
		t.Errorf("RepoName = %q, want %q", evt.RepoName, "api-service")
	}
	if evt.Iteration != 2 {
		t.Errorf("Iteration = %d, want 2", evt.Iteration)
	}
	if q, ok := evt.Data["question"].(string); !ok || q != "Which database to use?" {
		t.Errorf("Data[question] = %v, want %q", evt.Data["question"], "Which database to use?")
	}
	if a, ok := evt.Data["answer"].(string); !ok || a != "PostgreSQL" {
		t.Errorf("Data[answer] = %v, want %q", evt.Data["answer"], "PostgreSQL")
	}
}

func TestQuestionAnsweredEmitsAutoPickMetadata(t *testing.T) {
	stateDir := t.TempDir()
	featureID := "q_answered_autopick_feat"
	os.MkdirAll(filepath.Join(stateDir, featureID), 0755)
	obs := New(true, stateDir, false, "", false, "agentic")

	sc := SpanContextForFeature(featureID, "", "", "")
	obs.QuestionAnswered(sc, "sess-3", "api-service", 2, "Which scope?", "Repository-first (Recommended)", QuestionAnsweredMetadata{
		AutoPicked: true,
		Confidence: 0.81,
	})

	events := readEvents(t, stateDir, featureID)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	evt := events[0]
	if got, ok := evt.Data["auto_picked"].(bool); !ok || !got {
		t.Errorf("Data[auto_picked] = %v, want true", evt.Data["auto_picked"])
	}
	if got, ok := evt.Data["confidence"].(float64); !ok || got != 0.81 {
		t.Errorf("Data[confidence] = %v, want 0.81", evt.Data["confidence"])
	}
}

func TestRecoveryScannedEmitsEvent(t *testing.T) {
	stateDir := t.TempDir()
	featureID := "recovery_scan_feat"
	os.MkdirAll(filepath.Join(stateDir, featureID), 0755)
	obs := New(true, stateDir, false, "", false, "agentic")

	sc := SpanContextForFeature(featureID, "", "", "")
	obs.RecoveryScanned(sc, 10, 7)

	events := readEvents(t, stateDir, featureID)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	evt := events[0]
	if evt.EventType != "recovery.scanned" {
		t.Errorf("EventType = %q, want %q", evt.EventType, "recovery.scanned")
	}
	if total, ok := evt.Data["total_items"].(float64); !ok || int(total) != 10 {
		t.Errorf("Data[total_items] = %v, want 10", evt.Data["total_items"])
	}
	if alive, ok := evt.Data["alive_items"].(float64); !ok || int(alive) != 7 {
		t.Errorf("Data[alive_items] = %v, want 7", evt.Data["alive_items"])
	}
}

func TestRecoveryActionEmitsEvent(t *testing.T) {
	stateDir := t.TempDir()
	featureID := "recovery_action_feat"
	os.MkdirAll(filepath.Join(stateDir, featureID), 0755)
	obs := New(true, stateDir, false, "", false, "agentic")

	sc := SpanContextForFeature(featureID, "", "", "")
	obs.RecoveryAction(sc, "restart", "implement", true)

	events := readEvents(t, stateDir, featureID)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	evt := events[0]
	if evt.EventType != "recovery.action" {
		t.Errorf("EventType = %q, want %q", evt.EventType, "recovery.action")
	}
	if evt.Phase != "implement" {
		t.Errorf("Phase = %q, want %q", evt.Phase, "implement")
	}
	if action, ok := evt.Data["action"].(string); !ok || action != "restart" {
		t.Errorf("Data[action] = %v, want %q", evt.Data["action"], "restart")
	}
	if alive, ok := evt.Data["process_alive"].(bool); !ok || alive != true {
		t.Errorf("Data[process_alive] = %v, want true", evt.Data["process_alive"])
	}
}

func TestNilObserverPermissionMethodsAreNoOps(t *testing.T) {
	var obs *Observer
	sc := SpanContext{TraceID: "test", SpanID: "test", FeatureID: "test"}

	// None of these should panic
	obs.FeatureStarted(sc, "feat", []string{"repo"}, "full")
	obs.FeatureCompleted(sc, 1.0, time.Minute)
	obs.FeatureFailed(sc, "session_crashed", "blocking", "error msg")
	obs.FeatureInterrupted(sc, "implement")
	obs.PermissionRequested(sc, "sess1", "repo", 1, "Bash", "input")
	obs.PermissionResolved(sc, "sess1", "repo", 1, "Bash", "allow")
	obs.QuestionAsked(sc, "sess1", "repo", 1, "question?")
	obs.QuestionAnswered(sc, "sess1", "repo", 1, "question?", "answer")
	obs.RecoveryScanned(sc, 5, 3)
	obs.RecoveryAction(sc, "restart", "plan", false)
}

func TestDisabledObserverPermissionMethodsAreNoOps(t *testing.T) {
	stateDir := t.TempDir()
	featureID := "disabled_phase5_feat"
	obs := New(false, stateDir, false, "", false, "agentic")
	sc := SpanContext{TraceID: "test", SpanID: "test", FeatureID: featureID}

	obs.FeatureStarted(sc, "feat", []string{"repo"}, "full")
	obs.FeatureCompleted(sc, 1.0, time.Minute)
	obs.FeatureFailed(sc, "session_crashed", "blocking", "error msg")
	obs.FeatureInterrupted(sc, "implement")
	obs.PermissionRequested(sc, "sess1", "repo", 1, "Bash", "input")
	obs.PermissionResolved(sc, "sess1", "repo", 1, "Bash", "allow")
	obs.QuestionAsked(sc, "sess1", "repo", 1, "question?")
	obs.QuestionAnswered(sc, "sess1", "repo", 1, "question?", "answer")
	obs.RecoveryScanned(sc, 5, 3)
	obs.RecoveryAction(sc, "restart", "plan", false)

	eventsPath := filepath.Join(stateDir, featureID, "events.jsonl")
	if _, err := os.Stat(eventsPath); !os.IsNotExist(err) {
		t.Errorf("events.jsonl should not exist for disabled observer")
	}
}

func TestActivePhaseSpanContextReturnsStoredSpan(t *testing.T) {
	stateDir := t.TempDir()
	featureID := "feat-active"
	os.MkdirAll(filepath.Join(stateDir, featureID), 0755)
	obs := New(true, stateDir, false, "", false, "")
	sc := SpanContextForFeature(featureID, "trace-active", "", "").Child()
	obs.PhaseStarted(sc, "inquire")

	got, ok := obs.ActivePhaseSpanContext(featureID)
	if !ok {
		t.Fatal("expected ActivePhaseSpanContext to return stored span")
	}
	if got.SpanID != sc.SpanID {
		t.Errorf("SpanID = %q, want %q", got.SpanID, sc.SpanID)
	}
	if got.TraceID != sc.TraceID {
		t.Errorf("TraceID = %q, want %q", got.TraceID, sc.TraceID)
	}
}

func TestActivePhaseSpanContextClearedAfterPhaseCompleted(t *testing.T) {
	stateDir := t.TempDir()
	featureID := "feat-clear"
	os.MkdirAll(filepath.Join(stateDir, featureID), 0755)
	obs := New(true, stateDir, false, "", false, "")
	sc := SpanContextForFeature(featureID, "trace-clear", "", "").Child()
	obs.PhaseStarted(sc, "research")
	obs.PhaseCompleted(sc, "research", 0, nil)

	_, ok := obs.ActivePhaseSpanContext(featureID)
	if ok {
		t.Error("expected ActivePhaseSpanContext to return false after PhaseCompleted")
	}
}

func TestActivePhaseSpanContextNilObserver(t *testing.T) {
	var obs *Observer
	_, ok := obs.ActivePhaseSpanContext("feat-nil")
	if ok {
		t.Error("expected false for nil observer")
	}
}

func TestSessionStartedCreatesOTelSpan(t *testing.T) {
	stateDir := t.TempDir()
	featureID := "otel_session_feat"
	os.MkdirAll(filepath.Join(stateDir, featureID), 0755)

	obs := &Observer{
		emitter: NewEmitter(stateDir),
		otel:    newOtelBridge(true, "", false, "agentic-test"),
		enabled: true,
	}

	sc := SpanContextForFeature(featureID, "", "", "").Child()
	obs.SessionStarted(sc, "implement", "sess-1", "claude", "opus", "my-repo", "", "")

	obs.otel.mu.Lock()
	spanCount := len(obs.otel.spans)
	obs.otel.mu.Unlock()
	if spanCount != 1 {
		t.Errorf("expected 1 active OTel span after SessionStarted, got %d", spanCount)
	}

	obs.SessionEnded(sc, "implement", "sess-1", "my-repo", SessionUsage{}, time.Second, nil)

	obs.otel.mu.Lock()
	spanCount = len(obs.otel.spans)
	obs.otel.mu.Unlock()
	if spanCount != 0 {
		t.Errorf("expected 0 active OTel spans after SessionEnded, got %d", spanCount)
	}
}

func TestIterationStartedCreatesOTelSpan(t *testing.T) {
	stateDir := t.TempDir()
	featureID := "otel_iter_feat"
	os.MkdirAll(filepath.Join(stateDir, featureID), 0755)

	obs := &Observer{
		emitter: NewEmitter(stateDir),
		otel:    newOtelBridge(true, "", false, "agentic-test"),
		enabled: true,
	}

	sc := SpanContextForFeature(featureID, "", "", "").Child()
	obs.IterationStarted(sc, 1)

	obs.otel.mu.Lock()
	spanCount := len(obs.otel.spans)
	obs.otel.mu.Unlock()
	if spanCount != 1 {
		t.Errorf("expected 1 active OTel span after IterationStarted, got %d", spanCount)
	}

	obs.IterationEnded(sc, 1, SessionUsage{}, time.Second, "completed")

	obs.otel.mu.Lock()
	spanCount = len(obs.otel.spans)
	obs.otel.mu.Unlock()
	if spanCount != 0 {
		t.Errorf("expected 0 active OTel spans after IterationEnded, got %d", spanCount)
	}
}

func TestReviewStartedCreatesOTelSpan(t *testing.T) {
	stateDir := t.TempDir()
	featureID := "otel_review_feat"
	os.MkdirAll(filepath.Join(stateDir, featureID), 0755)

	obs := &Observer{
		emitter: NewEmitter(stateDir),
		otel:    newOtelBridge(true, "", false, "agentic-test"),
		enabled: true,
	}

	sc := SpanContextForFeature(featureID, "", "", "").Child()
	obs.ReviewStarted(sc, 1)

	obs.otel.mu.Lock()
	spanCount := len(obs.otel.spans)
	obs.otel.mu.Unlock()
	if spanCount != 1 {
		t.Errorf("expected 1 active OTel span after ReviewStarted, got %d", spanCount)
	}

	obs.ReviewCompleted(sc, 1, "approved", 2*time.Second)

	obs.otel.mu.Lock()
	spanCount = len(obs.otel.spans)
	obs.otel.mu.Unlock()
	if spanCount != 0 {
		t.Errorf("expected 0 active OTel spans after ReviewCompleted, got %d", spanCount)
	}
}

func TestFeatureStartedCreatesOTelSpan(t *testing.T) {
	stateDir := t.TempDir()
	featureID := "otel_feature_feat"
	os.MkdirAll(filepath.Join(stateDir, featureID), 0755)

	obs := &Observer{
		emitter: NewEmitter(stateDir),
		otel:    newOtelBridge(true, "", false, "agentic-test"),
		enabled: true,
	}

	sc := SpanContextForFeature(featureID, "", "", "")
	obs.FeatureStarted(sc, "my feature", []string{"repo-a"}, "large")

	obs.otel.mu.Lock()
	spanCount := len(obs.otel.spans)
	obs.otel.mu.Unlock()
	if spanCount != 1 {
		t.Errorf("expected 1 active OTel span after FeatureStarted, got %d", spanCount)
	}

	obs.FeatureCompleted(sc, 1.23, 5*time.Minute)

	obs.otel.mu.Lock()
	spanCount = len(obs.otel.spans)
	obs.otel.mu.Unlock()
	if spanCount != 0 {
		t.Errorf("expected 0 active OTel spans after FeatureCompleted, got %d", spanCount)
	}
}

func TestFeatureFailedEndsOTelSpan(t *testing.T) {
	stateDir := t.TempDir()
	featureID := "otel_feature_fail"
	os.MkdirAll(filepath.Join(stateDir, featureID), 0755)

	obs := &Observer{
		emitter: NewEmitter(stateDir),
		otel:    newOtelBridge(true, "", false, "agentic-test"),
		enabled: true,
	}

	sc := SpanContextForFeature(featureID, "", "", "")
	obs.FeatureStarted(sc, "failing feature", []string{"repo-a"}, "large")
	obs.FeatureFailed(sc, "infrastructure_failure", "blocking", "something broke")

	obs.otel.mu.Lock()
	spanCount := len(obs.otel.spans)
	obs.otel.mu.Unlock()
	if spanCount != 0 {
		t.Errorf("expected 0 active OTel spans after FeatureFailed, got %d", spanCount)
	}
}

func TestFeatureInterruptedEndsOTelSpan(t *testing.T) {
	stateDir := t.TempDir()
	featureID := "otel_feature_int"
	os.MkdirAll(filepath.Join(stateDir, featureID), 0755)

	obs := &Observer{
		emitter: NewEmitter(stateDir),
		otel:    newOtelBridge(true, "", false, "agentic-test"),
		enabled: true,
	}

	sc := SpanContextForFeature(featureID, "", "", "")
	obs.FeatureStarted(sc, "interrupted feature", []string{"repo-a"}, "large")
	obs.FeatureInterrupted(sc, "implement")

	obs.otel.mu.Lock()
	spanCount := len(obs.otel.spans)
	obs.otel.mu.Unlock()
	if spanCount != 0 {
		t.Errorf("expected 0 active OTel spans after FeatureInterrupted, got %d", spanCount)
	}
}

func TestPermissionAddsOTelSpanEvent(t *testing.T) {
	stateDir := t.TempDir()
	featureID := "otel_perm_feat"
	os.MkdirAll(filepath.Join(stateDir, featureID), 0755)

	obs := &Observer{
		emitter: NewEmitter(stateDir),
		otel:    newOtelBridge(true, "", false, "agentic-test"),
		enabled: true,
	}

	featureCtx := SpanContextForFeature(featureID, "", "", "")
	sessionCtx := featureCtx.Child()

	// Start a session span so AddSpanEvent has a target
	obs.SessionStarted(sessionCtx, "implement", "sess-1", "claude", "opus", "", "", "")

	// Permission events should add span events to the session span (no panic, no new span)
	obs.otel.mu.Lock()
	spanCount := len(obs.otel.spans)
	obs.otel.mu.Unlock()
	if spanCount != 1 {
		t.Fatalf("expected 1 span before permission, got %d", spanCount)
	}

	permCtx := sessionCtx.Child()
	obs.PermissionRequested(permCtx, "sess-1", "my-repo", 1, "Bash", "ls -la")
	obs.PermissionResolved(permCtx, "sess-1", "my-repo", 1, "Bash", "allow")

	// No new spans should be created — events are added to the existing session span
	obs.otel.mu.Lock()
	spanCount = len(obs.otel.spans)
	obs.otel.mu.Unlock()
	if spanCount != 1 {
		t.Errorf("expected still 1 span after permission events, got %d", spanCount)
	}
}

// TestEmit_IncludesRunNumber asserts every typed Observer emit method stamps
// Event.RunNumber from the supplied SpanContext's RunNumber. One call per
// emit method; adding a new emit method requires a matching row here so the
// regression guard for "every emit path is run-number aware" stays current.
func TestEmit_IncludesRunNumber(t *testing.T) {
	stateDir := t.TempDir()
	featureID := "run_number_emit_feat"
	if err := os.MkdirAll(filepath.Join(stateDir, featureID), 0755); err != nil {
		t.Fatal(err)
	}
	obs := New(true, stateDir, false, "", false, "agentic")

	const wantRun = 7
	sc := SpanContextForFeature(featureID, "", "", "").WithRun(wantRun)

	usage := SessionUsage{TotalCostUSD: 1.0, InputTokens: 10, OutputTokens: 5}
	obs.PhaseStarted(sc, "inquire")
	obs.PhaseCompleted(sc, "inquire", time.Second, nil)
	obs.SessionStarted(sc, "inquire", "s1", "claude", "opus", "repo-a", "", "")
	obs.SessionEnded(sc, "inquire", "s1", "repo-a", usage, time.Second, nil)
	obs.IterationStarted(sc, 1)
	obs.IterationEnded(sc, 1, usage, time.Second, "done")
	obs.ReviewStarted(sc, 1)
	obs.ReviewCompleted(sc, 1, "APPROVED", time.Second)
	obs.ValidationStarted(sc, "plan", 2)
	obs.ValidationCompleted(sc, "plan", "APPROVED", time.Second, 2)
	obs.ValidatorStarted(sc, "critic-architecture")
	obs.ValidatorCompleted(sc, "critic-architecture", "APPROVED", time.Second)
	obs.FeatureStarted(sc, "my-feature", []string{"repo-a"}, "large")
	obs.FeatureCompleted(sc, 0.5, time.Second)
	obs.FeatureFailed(sc, "infrastructure_failure", "blocking", "boom")
	obs.SetupLifecycle(sc, feature.SetupEvent{Kind: feature.SetupEventStarted, FeatureID: featureID, Attempt: 1})
	obs.FeatureInterrupted(sc, "implement")
	obs.PermissionRequested(sc, "s1", "repo-a", 1, "Edit", "preview")
	obs.PermissionResolved(sc, "s1", "repo-a", 1, "Edit", "allow")
	obs.QuestionAsked(sc, "s1", "repo-a", 1, "ready?")
	obs.QuestionAnswered(sc, "s1", "repo-a", 1, "ready?", "yes")
	obs.RecoveryScanned(sc, 2, 1)
	obs.RecoveryAction(sc, "resume", "implement", true)
	obs.ContextFileRead(sc, "implement", "s1", "kb", "/path/to/file.md")
	obs.ContextHandoffTriggered(sc, "implement", "s1", "repo-a", "codex", 1, 81, 80, 211000, 258400, 12000)
	exitCode := 0
	durationMs := int64(123)
	obs.ContextLargeOutput(sc, "implement", "s1", "repo-a", "codex", 1, "rg -n foo", 21000, 20000, &exitCode, &durationMs)
	obs.AgentTaskStarted(sc, "implement", "s1", "task1", "use1", "desc", "local_agent")
	obs.AgentTaskProgress(sc, "implement", "s1", "task1", "use1", "desc", "Read", 10, 2, 500)
	obs.AgentTaskEnded(sc, "implement", "s1", "task1", "use1", "completed", "ok", 10, 2, 500)
	obs.FeatureRewound(sc, RewindEventInput{
		TargetPhase:     feature.PhaseImplement,
		EffectiveTarget: feature.PhaseImplement,
		SourceRun:       6,
		NewRun:          7,
	})

	events := readEvents(t, stateDir, featureID)
	if len(events) != 30 {
		t.Fatalf("expected 30 events, got %d", len(events))
	}
	for i, evt := range events {
		if evt.RunNumber != wantRun {
			t.Errorf("event[%d] (%s) RunNumber = %d, want %d",
				i, evt.EventType, evt.RunNumber, wantRun)
		}
	}
}

// TestEmit_ZeroRunNumber_OmittedInJSONL asserts that when SpanContext.RunNumber
// is zero (pre-migration callers or tests that do not opt in) the emitted
// JSONL line has NO "run_number" key — omitempty preserves byte-compat with
// consumers that do not supply run context.
func TestEmit_ZeroRunNumber_OmittedInJSONL(t *testing.T) {
	stateDir := t.TempDir()
	featureID := "zero_run_omit_feat"
	if err := os.MkdirAll(filepath.Join(stateDir, featureID), 0755); err != nil {
		t.Fatal(err)
	}
	obs := New(true, stateDir, false, "", false, "agentic")

	scZero := SpanContextForFeature(featureID, "", "", "") // RunNumber = 0
	scThree := scZero.WithRun(3)
	obs.PhaseStarted(scZero, "inquire")
	obs.PhaseStarted(scThree, "research")

	// Read raw lines so we can assert on byte shape.
	f, err := os.Open(filepath.Join(stateDir, featureID, "events.jsonl"))
	if err != nil {
		t.Fatalf("opening events.jsonl: %v", err)
	}
	defer f.Close()
	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}
	if strings.Contains(lines[0], `"run_number"`) {
		t.Errorf("line[0] should not contain run_number, got: %s", lines[0])
	}
	if !strings.Contains(lines[1], `"run_number":3`) {
		t.Errorf("line[1] should contain \"run_number\":3, got: %s", lines[1])
	}
}

// TestSpanContext_WithRunPropagatesThroughChild asserts WithRun's value
// semantics and that Child() preserves RunNumber into the derived span.
func TestSpanContext_WithRunPropagatesThroughChild(t *testing.T) {
	root := SpanContextForFeature("f", "", "", "").WithRun(4)
	if root.RunNumber != 4 {
		t.Fatalf("root.RunNumber = %d, want 4", root.RunNumber)
	}
	child := root.Child()
	if child.RunNumber != 4 {
		t.Errorf("child.RunNumber = %d, want 4", child.RunNumber)
	}
	grand := child.Child()
	if grand.RunNumber != 4 {
		t.Errorf("grand.RunNumber = %d, want 4", grand.RunNumber)
	}

	// WithRun returns a modified copy and does not mutate the receiver.
	modified := root.WithRun(9)
	if modified.RunNumber != 9 {
		t.Errorf("modified.RunNumber = %d, want 9", modified.RunNumber)
	}
	if root.RunNumber != 4 {
		t.Errorf("root.RunNumber mutated by WithRun: got %d, want 4", root.RunNumber)
	}
}

// TestObserve_Disabled_StillNoOpForAllEmitters extends the disabled-observer
// regression: a disabled Observer writes no events.jsonl AND no
// observe-summary.yaml even when typed emitters, the Emit escape hatch,
// and WriteFeatureSummary are all invoked.
func TestObserve_Disabled_StillNoOpForAllEmitters(t *testing.T) {
	stateDir := t.TempDir()
	featureID := "disabled_phase4"
	if err := os.MkdirAll(filepath.Join(stateDir, featureID), 0755); err != nil {
		t.Fatal(err)
	}
	obs := New(false, stateDir, false, "", false, "agentic")
	sc := SpanContextForFeature(featureID, "", "", "").WithRun(5)

	obs.PhaseStarted(sc, "implement")
	obs.PhaseCompleted(sc, "implement", time.Second, nil)
	obs.FeatureStarted(sc, "feat", []string{"repo"}, "large")
	if err := obs.Emit(Event{EventType: "x", FeatureID: featureID, RunNumber: 5}); err != nil {
		t.Errorf("Emit on disabled observer returned error: %v", err)
	}
	if err := obs.WriteFeatureSummary(FeatureSummaryInput{
		FeatureID:  featureID,
		FeatureDir: filepath.Join(stateDir, featureID),
		ActiveRun:  5,
	}); err != nil {
		t.Errorf("WriteFeatureSummary on disabled observer returned error: %v", err)
	}

	eventsPath := filepath.Join(stateDir, featureID, "events.jsonl")
	if _, err := os.Stat(eventsPath); !os.IsNotExist(err) {
		t.Errorf("events.jsonl should not exist for disabled observer")
	}
	summaryPath := filepath.Join(stateDir, featureID, "observe-summary.yaml")
	if _, err := os.Stat(summaryPath); !os.IsNotExist(err) {
		t.Errorf("observe-summary.yaml should not exist for disabled observer")
	}
}

func TestObserver_ConfigChanged_WritesEventsJSONL(t *testing.T) {
	stateDir := t.TempDir()
	featureID := "cfg_changed_feat"
	os.MkdirAll(filepath.Join(stateDir, featureID), 0755)
	obs := New(true, stateDir, false, "", false, "agentic")

	sc := SpanContextForFeature(featureID, "", "", "").WithRun(3)

	before := feature.ConfigSnapshot{
		Models:      config.ModelConfig{Research: "old-research", Planning: "old-planning"},
		Inquireness: feature.InquirenessMedium,
		Checkpoints: feature.Checkpoints{RoadmapReview: true, PhasePlanReview: true},
	}
	after := feature.ConfigSnapshot{
		Models:      config.ModelConfig{Research: "new-research", Planning: "new-planning"},
		Inquireness: feature.InquirenessHigh,
		Checkpoints: feature.Checkpoints{InquiryReview: true, ManualPublish: true},
	}

	obs.ConfigChanged(sc, before, after)

	events := readEvents(t, stateDir, featureID)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	evt := events[0]
	if evt.EventType != "feature.config_changed" {
		t.Errorf("EventType = %q, want %q", evt.EventType, "feature.config_changed")
	}
	if evt.FeatureID != featureID {
		t.Errorf("FeatureID = %q, want %q", evt.FeatureID, featureID)
	}
	if evt.RunNumber != 3 {
		t.Errorf("RunNumber = %d, want 3", evt.RunNumber)
	}

	beforeMap, ok := evt.Data["before"].(map[string]any)
	if !ok {
		t.Fatalf("Data[before] type = %T, want map[string]any", evt.Data["before"])
	}
	if beforeMap["inquireness"] != "medium" {
		t.Errorf("before.inquireness = %v, want medium", beforeMap["inquireness"])
	}
	afterMap, ok := evt.Data["after"].(map[string]any)
	if !ok {
		t.Fatalf("Data[after] type = %T, want map[string]any", evt.Data["after"])
	}
	if afterMap["inquireness"] != "high" {
		t.Errorf("after.inquireness = %v, want high", afterMap["inquireness"])
	}

	beforeModels := beforeMap["models"].(map[string]any)
	if beforeModels["research"] != "old-research" {
		t.Errorf("before.models.research = %v, want old-research", beforeModels["research"])
	}
	afterModels := afterMap["models"].(map[string]any)
	if afterModels["research"] != "new-research" {
		t.Errorf("after.models.research = %v, want new-research", afterModels["research"])
	}

	beforeCheckpoints := beforeMap["checkpoints"].(map[string]any)
	if beforeCheckpoints["roadmap_review"] != true {
		t.Errorf("before.checkpoints.roadmap_review = %v, want true", beforeCheckpoints["roadmap_review"])
	}
	if beforeCheckpoints["phase_plan_review"] != true {
		t.Errorf("before.checkpoints.phase_plan_review = %v, want true", beforeCheckpoints["phase_plan_review"])
	}
	afterCheckpoints := afterMap["checkpoints"].(map[string]any)
	if afterCheckpoints["inquiry_review"] != true {
		t.Errorf("after.checkpoints.inquiry_review = %v, want true", afterCheckpoints["inquiry_review"])
	}
	if afterCheckpoints["manual_publish"] != true {
		t.Errorf("after.checkpoints.manual_publish = %v, want true", afterCheckpoints["manual_publish"])
	}
	if afterCheckpoints["roadmap_review"] != false {
		t.Errorf("after.checkpoints.roadmap_review = %v, want false", afterCheckpoints["roadmap_review"])
	}
	if afterCheckpoints["phase_plan_review"] != false {
		t.Errorf("after.checkpoints.phase_plan_review = %v, want false", afterCheckpoints["phase_plan_review"])
	}
}

func TestObserver_ConfigChanged_NilAndDisabled(t *testing.T) {
	sc := SpanContext{TraceID: "t", SpanID: "s", FeatureID: "f"}
	snap := feature.ConfigSnapshot{}

	var nilObs *Observer
	// Must not panic on nil receiver.
	nilObs.ConfigChanged(sc, snap, snap)

	stateDir := t.TempDir()
	featureID := "cfg_changed_disabled"
	disabled := New(false, stateDir, false, "", false, "agentic")
	disabled.ConfigChanged(sc, snap, snap)

	eventsPath := filepath.Join(stateDir, featureID, "events.jsonl")
	if _, err := os.Stat(eventsPath); !os.IsNotExist(err) {
		t.Errorf("events.jsonl should not exist for disabled observer")
	}
}
