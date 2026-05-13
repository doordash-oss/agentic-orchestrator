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
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

type contextReadTrackerTestSession struct {
	onToolAllowed func(string, json.RawMessage)
	onFileRead    func(llm.FileReadEvent)
}

func (s *contextReadTrackerTestSession) SetOnToolAllowed(fn func(string, json.RawMessage)) {
	s.onToolAllowed = fn
}

func (s *contextReadTrackerTestSession) SetOnFileRead(fn func(llm.FileReadEvent)) {
	s.onFileRead = fn
}

// readObserveEvents reads and parses all events from events.jsonl for a feature.
func readObserveEvents(t *testing.T, stateDir, featureID string) []observe.Event {
	t.Helper()
	eventsPath := filepath.Join(stateDir, featureID, "events.jsonl")
	f, err := os.Open(eventsPath)
	if err != nil {
		t.Fatalf("reading events.jsonl: %v", err)
	}
	defer f.Close()
	var events []observe.Event
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var evt observe.Event
		if err := json.Unmarshal(scanner.Bytes(), &evt); err != nil {
			t.Fatalf("parsing event line %q: %v", scanner.Text(), err)
		}
		events = append(events, evt)
	}
	return events
}

// filterEventsByType returns events matching the given type.
func filterEventsByType(events []observe.Event, eventType string) []observe.Event {
	var filtered []observe.Event
	for _, e := range events {
		if e.EventType == eventType {
			filtered = append(filtered, e)
		}
	}
	return filtered
}

// assertEventOrder verifies that the events appear in the given type order.
func assertEventOrder(t *testing.T, events []observe.Event, expectedTypes ...string) {
	t.Helper()
	var actualTypes []string
	for _, e := range events {
		actualTypes = append(actualTypes, e.EventType)
	}
	if len(events) < len(expectedTypes) {
		t.Fatalf("expected at least %d events (%v), got %d events (%v)", len(expectedTypes), expectedTypes, len(events), actualTypes)
	}
	idx := 0
	for _, expected := range expectedTypes {
		found := false
		for idx < len(events) {
			if events[idx].EventType == expected {
				found = true
				idx++
				break
			}
			idx++
		}
		if !found {
			t.Errorf("expected event type %q not found in order; actual sequence: %v", expected, actualTypes)
			return
		}
	}
}

func TestContextReadTrackerEmitsProviderReportedGuidelineRead(t *testing.T) {
	observeDir := t.TempDir()
	featureID := "context-read-guideline"
	if err := os.MkdirAll(filepath.Join(observeDir, featureID), 0o755); err != nil {
		t.Fatalf("mkdir feature observe dir: %v", err)
	}
	obs := observe.New(true, observeDir, false, "", false, "agentic-test")
	guidelinesDir := filepath.Join(t.TempDir(), "guidelines")
	sess := &contextReadTrackerTestSession{}
	sc := observe.SpanContextForFeature(featureID, "trace-1", "feature", "span-1").Child()

	tracker := &ContextReadTracker{
		KBBaseDir:     filepath.Join(t.TempDir(), "knowledge-base"),
		SkillsDir:     filepath.Join(t.TempDir(), "skills"),
		GuidelinesDir: guidelinesDir,
		Observer:      obs,
	}
	tracker.Install(sess, sc, feature.PhaseImplement.String(), "session-1")
	if sess.onFileRead == nil {
		t.Fatal("onFileRead callback was not installed")
	}

	exitCode := 0
	guidelinePath := filepath.Join(guidelinesDir, "go", "index.md")
	sess.onFileRead(llm.FileReadEvent{
		FilePath:       guidelinePath,
		Source:         "codex.command_action",
		ProviderItemID: "call_123",
		ExitCode:       &exitCode,
	})

	events := filterEventsByType(readObserveEvents(t, observeDir, featureID), "context.file_read")
	if len(events) != 1 {
		t.Fatalf("context.file_read events = %d, want 1", len(events))
	}
	data := events[0].Data
	if data["category"] != "guideline" {
		t.Errorf("category = %v, want guideline", data["category"])
	}
	if data["file_path"] != guidelinePath {
		t.Errorf("file_path = %v, want %s", data["file_path"], guidelinePath)
	}
	if data["source"] != "codex.command_action" {
		t.Errorf("source = %v", data["source"])
	}
	if data["provider_item_id"] != "call_123" {
		t.Errorf("provider_item_id = %v", data["provider_item_id"])
	}
	if data["exit_code"] != float64(0) {
		t.Errorf("exit_code = %#v, want 0", data["exit_code"])
	}
}

func TestImplementLoopEmitsFullEventSequence(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// Create observer state directory (where events.jsonl is written)
	observeDir := t.TempDir()
	featureID := "test-observe-impl-001"
	os.MkdirAll(filepath.Join(observeDir, featureID), 0755)
	obs := observe.New(true, observeDir, false, "", false, "agentic")

	// Create feature state directory
	stateDir := t.TempDir()
	store := feature.NewStore(stateDir)

	// Create temp repo directory (WorkDir)
	repoDir := t.TempDir()

	// Create feature with the same pattern as newTestFeature but with custom ID and TraceID
	f := &feature.Feature{
		ID:           featureID,
		Name:         "Test Observe Feature",
		Slug:         "test-observe-feature",
		Description:  "Observability integration test feature",
		Status:       feature.StatusImplementing,
		CurrentPhase: feature.PhaseImplement,
		TraceID:      "trace-test-impl-001",
		Repos: []feature.FeatureRepo{
			{Name: "agentic", Path: repoDir},
		},
		Models: config.ModelConfig{
			Implementation: "test-model",
			Review:         "test-review-model",
		},
		ExitCriteria: "test criteria",
	}
	store.Save(f)

	// Create artifact directory structure matching existing test patterns
	artifactDir := filepath.Join(stateDir, featureID, "phase-impl", "implement", "agentic")
	os.MkdirAll(artifactDir, 0755)

	// Build scripts
	scriptDir := t.TempDir()
	agentScript := testutil.WriteScript(t, scriptDir, "agent.sh",
		testutil.JSONLInit+"\n"+
			testutil.WriteImplementSuccessArtifacts(artifactDir)+"\n"+
			testutil.JSONLSuccess+"\n")

	reviewScript := testutil.WriteScript(t, scriptDir, "review.sh",
		testutil.JSONLInit+"\n"+
			testutil.WriteReviewApproved(artifactDir)+"\n"+
			testutil.JSONLSuccess+"\n")

	// Set up session manager
	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	planPath := writePlanFile(t, artifactDir, "Implement a test feature")

	cfg := ImplementConfig{
		Feature:             f,
		FeatureStore:        store,
		WorkDir:             repoDir,
		PlanPath:            planPath,
		StateDir:            filepath.Join(stateDir, featureID),
		ArtifactDir:         artifactDir,
		RepoName:            "agentic",
		MaxIterations:       3,
		MaxConsecFails:      3,
		MaxConsecNoProgress: 3,
		Model:               "test-model",
		ReviewModel:         "test-review-model",
		BuildSession:        mockBuildSession(agentScript, reviewScript),
		ExitCriteria:        "test criteria",
		Observer:            obs,
	}

	result, err := RunImplementationLoop(cfg, sm)
	if err != nil {
		t.Fatalf("RunImplementationLoop error: %v", err)
	}
	if result.FinalStatus != "review_passed" {
		t.Fatalf("FinalStatus = %q, want %q", result.FinalStatus, "review_passed")
	}

	// Read events from the observer's state directory
	events := readObserveEvents(t, observeDir, featureID)

	// Assert event order: phase > iteration > impl session > review session > iteration end > phase end
	assertEventOrder(t, events,
		"phase.started",
		"iteration.started",
		"session.started",
		"session.ended",
		"review.started",
		"session.started",
		"session.ended",
		"review.completed",
		"iteration.ended",
		"phase.completed",
	)

	// Verify TraceID consistency: all events should carry the feature's TraceID
	for _, evt := range events {
		if evt.TraceID != f.TraceID {
			t.Errorf("event %q has TraceID %q, want %q", evt.EventType, evt.TraceID, f.TraceID)
		}
	}

	// Verify phase.started
	phaseStarted := filterEventsByType(events, "phase.started")
	if len(phaseStarted) != 1 {
		t.Fatalf("expected 1 phase.started event, got %d", len(phaseStarted))
	}

	// Verify phase.completed
	phaseCompleted := filterEventsByType(events, "phase.completed")
	if len(phaseCompleted) != 1 {
		t.Fatalf("expected 1 phase.completed event, got %d", len(phaseCompleted))
	}
	if phaseCompleted[0].Error != "" {
		t.Errorf("phase.completed should have no error for success path, got %q", phaseCompleted[0].Error)
	}

	// Verify iteration events
	iterStarted := filterEventsByType(events, "iteration.started")
	if len(iterStarted) != 1 {
		t.Fatalf("expected 1 iteration.started, got %d", len(iterStarted))
	}
	if iterStarted[0].Iteration != 1 {
		t.Errorf("iteration.started Iteration = %d, want 1", iterStarted[0].Iteration)
	}

	iterEnded := filterEventsByType(events, "iteration.ended")
	if len(iterEnded) != 1 {
		t.Fatalf("expected 1 iteration.ended, got %d", len(iterEnded))
	}
	if iterEnded[0].Status != "review_passed" {
		t.Errorf("iteration.ended Status = %q, want %q", iterEnded[0].Status, "review_passed")
	}

	// Verify review events
	reviewStarted := filterEventsByType(events, "review.started")
	if len(reviewStarted) != 1 {
		t.Fatalf("expected 1 review.started, got %d", len(reviewStarted))
	}
	reviewCompleted := filterEventsByType(events, "review.completed")
	if len(reviewCompleted) != 1 {
		t.Fatalf("expected 1 review.completed, got %d", len(reviewCompleted))
	}
	// ReviewStatus.String() returns "APPROVED"
	if reviewCompleted[0].Status != "APPROVED" {
		t.Errorf("review.completed Status = %q, want %q", reviewCompleted[0].Status, "APPROVED")
	}

	// Verify session events (2 sessions: impl + review)
	sessionStarted := filterEventsByType(events, "session.started")
	if len(sessionStarted) != 2 {
		t.Fatalf("expected 2 session.started events (impl + review), got %d", len(sessionStarted))
	}
	sessionEnded := filterEventsByType(events, "session.ended")
	if len(sessionEnded) != 2 {
		t.Fatalf("expected 2 session.ended events (impl + review), got %d", len(sessionEnded))
	}

	// Verify SpanContext hierarchy: iteration.parentSpanID == phase.spanID
	if iterStarted[0].ParentSpanID != phaseStarted[0].SpanID {
		t.Errorf("iteration.started ParentSpanID = %q, want phase SpanID %q",
			iterStarted[0].ParentSpanID, phaseStarted[0].SpanID)
	}

	// Verify phase.completed SpanID == phase.started SpanID (same span)
	if phaseCompleted[0].SpanID != phaseStarted[0].SpanID {
		t.Errorf("phase.completed SpanID = %q, want same as phase.started SpanID %q",
			phaseCompleted[0].SpanID, phaseStarted[0].SpanID)
	}

	// Verify FeatureID is set on all events
	for _, evt := range events {
		if evt.FeatureID != featureID {
			t.Errorf("event %q has FeatureID %q, want %q", evt.EventType, evt.FeatureID, featureID)
		}
	}

	// Verify that impl session spans are children of the iteration span
	if len(sessionStarted) >= 1 {
		implSession := sessionStarted[0]
		if implSession.ParentSpanID == "" {
			t.Error("impl session.started should have a ParentSpanID")
		}
	}

	// Verify no duplicate span IDs between phase and iteration
	if phaseStarted[0].SpanID == iterStarted[0].SpanID {
		t.Error("phase and iteration should have different SpanIDs")
	}

	// Verify review.started is a child of the iteration
	if len(reviewStarted) >= 1 {
		if reviewStarted[0].ParentSpanID == "" {
			t.Error("review.started should have a ParentSpanID")
		}
	}

	_ = strings.HasSuffix // use strings import
}

func TestRunInquireEmitsPhaseAndSessionEvents(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	observeDir := t.TempDir()
	featureID := "test-observe-inquire-001"
	os.MkdirAll(filepath.Join(observeDir, featureID), 0755)
	obs := observe.New(true, observeDir, false, "", false, "agentic")

	stateDir := t.TempDir()
	store := feature.NewStore(stateDir)
	repoDir := t.TempDir()

	f := &feature.Feature{
		ID:           featureID,
		Name:         "Test Inquire Observe",
		Slug:         "test-inquire-observe",
		Description:  "Test observability for inquire phase",
		Status:       feature.StatusResearching,
		CurrentPhase: feature.PhaseInquire,
		TraceID:      "trace-test-inquire-001",
		Repos:        []feature.FeatureRepo{{Name: "testrepo", Path: repoDir}},
		Models:       config.ModelConfig{Research: "test-model"},
		ExitCriteria: "test criteria",
	}
	store.Save(f)

	// Create PID dir so session can write PID file
	os.MkdirAll(filepath.Join(stateDir, featureID), 0755)

	// Create a simple agent script that emits init + success and exits.
	// The sleep ensures the process is still alive when the manager sends
	// the initialize handshake on stdin.
	scriptDir := t.TempDir()
	agentScript := testutil.WriteScript(t, scriptDir, "agent.sh",
		testutil.JSONLInit+"\nsleep 0.1\n"+testutil.JSONLSuccess+"\n")

	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)

	pr := &PhaseRunner{
		SessionManager: sm,
		FeatureStore:   store,
		StateDir:       stateDir,
		Observer:       obs,
		BuildSessionFn: func(opts BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
			return []string{"bash", agentScript}, nil, &session.SessionOpts{
				PIDDir:   opts.PIDDir,
				RepoName: opts.RepoName,
			}, nil
		},
	}

	sessionID, err := pr.RunInquire(f)
	if err != nil {
		t.Fatalf("RunInquire error: %v", err)
	}
	if sessionID == "" {
		t.Fatal("expected non-empty session ID")
	}

	// Wait for the async session to complete
	sv := sm.GetSession(sessionID)
	if sv != nil {
		sv.Wait()
	}
	sm.Shutdown()

	// Read events
	events := readObserveEvents(t, observeDir, featureID)

	// Assert event order: phase.started -> session.started -> session.ended
	assertEventOrder(t, events, "phase.started", "session.started", "session.ended")

	// Verify no phase.completed (deferred to Phase 5 for async phases)
	if len(filterEventsByType(events, "phase.completed")) != 0 {
		t.Error("phase.completed should NOT be emitted for async interactive phases")
	}

	// Verify TraceID consistency
	for _, evt := range events {
		if evt.TraceID != f.TraceID {
			t.Errorf("event %q has TraceID %q, want %q", evt.EventType, evt.TraceID, f.TraceID)
		}
	}

	// Verify FeatureID
	for _, evt := range events {
		if evt.FeatureID != featureID {
			t.Errorf("event %q has FeatureID %q, want %q", evt.EventType, evt.FeatureID, featureID)
		}
	}

	// Verify SpanContext hierarchy: session.started parentSpanID == phase.started spanID
	phaseStarted := filterEventsByType(events, "phase.started")
	if len(phaseStarted) != 1 {
		t.Fatalf("expected 1 phase.started, got %d", len(phaseStarted))
	}
	sessionStarted := filterEventsByType(events, "session.started")
	if len(sessionStarted) != 1 {
		t.Fatalf("expected 1 session.started, got %d", len(sessionStarted))
	}
	if sessionStarted[0].ParentSpanID != phaseStarted[0].SpanID {
		t.Errorf("session.started ParentSpanID = %q, want phase SpanID %q",
			sessionStarted[0].ParentSpanID, phaseStarted[0].SpanID)
	}

	// Verify Phase field
	if phaseStarted[0].Phase != "Inquire" {
		t.Errorf("phase.started Phase = %q, want %q", phaseStarted[0].Phase, "Inquire")
	}
}

func TestRunResearchEmitsPhaseAndSessionEvents(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	observeDir := t.TempDir()
	featureID := "test-observe-research-001"
	os.MkdirAll(filepath.Join(observeDir, featureID), 0755)
	obs := observe.New(true, observeDir, false, "", false, "agentic")

	stateDir := t.TempDir()
	store := feature.NewStore(stateDir)
	repoDir := t.TempDir()

	f := &feature.Feature{
		ID:           featureID,
		Name:         "Test Research Observe",
		Slug:         "test-research-observe",
		Description:  "Test observability for research phase",
		Status:       feature.StatusResearching,
		CurrentPhase: feature.PhaseResearch,
		TraceID:      "trace-test-research-001",
		Repos:        []feature.FeatureRepo{{Name: "testrepo", Path: repoDir}},
		Models:       config.ModelConfig{Research: "test-model"},
		ExitCriteria: "test criteria",
	}
	store.Save(f)

	// Create PID dir so session can write PID file
	os.MkdirAll(filepath.Join(stateDir, featureID), 0755)

	// Create a simple agent script that emits init + success and exits.
	// The sleep ensures the process is still alive when the manager sends
	// the initialize handshake on stdin.
	scriptDir := t.TempDir()
	agentScript := testutil.WriteScript(t, scriptDir, "agent.sh",
		testutil.JSONLInit+"\nsleep 0.1\n"+testutil.JSONLSuccess+"\n")

	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)

	pr := &PhaseRunner{
		SessionManager: sm,
		FeatureStore:   store,
		StateDir:       stateDir,
		Observer:       obs,
		BuildSessionFn: func(opts BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
			return []string{"bash", agentScript}, nil, &session.SessionOpts{
				PIDDir:   opts.PIDDir,
				RepoName: opts.RepoName,
			}, nil
		},
	}

	questionsPath := filepath.Join(t.TempDir(), "questions.md")
	if err := os.WriteFile(questionsPath, []byte("# Questions\n- Q1?\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(questionsPath): %v", err)
	}

	sessionID, err := pr.RunResearchFromQuestions(f, questionsPath)
	if err != nil {
		t.Fatalf("RunResearchFromQuestions error: %v", err)
	}
	if sessionID == "" {
		t.Fatal("expected non-empty session ID")
	}

	// Wait for the async session to complete
	sv := sm.GetSession(sessionID)
	if sv != nil {
		sv.Wait()
	}
	sm.Shutdown()

	// Read events
	events := readObserveEvents(t, observeDir, featureID)

	// Assert event order: phase.started -> session.started -> session.ended
	assertEventOrder(t, events, "phase.started", "session.started", "session.ended")

	// Verify no phase.completed (deferred to Phase 5 for async phases)
	if len(filterEventsByType(events, "phase.completed")) != 0 {
		t.Error("phase.completed should NOT be emitted for async interactive phases")
	}

	// Verify TraceID consistency
	for _, evt := range events {
		if evt.TraceID != f.TraceID {
			t.Errorf("event %q has TraceID %q, want %q", evt.EventType, evt.TraceID, f.TraceID)
		}
	}

	// Verify FeatureID
	for _, evt := range events {
		if evt.FeatureID != featureID {
			t.Errorf("event %q has FeatureID %q, want %q", evt.EventType, evt.FeatureID, featureID)
		}
	}

	// Verify SpanContext hierarchy: session.started parentSpanID == phase.started spanID
	phaseStarted := filterEventsByType(events, "phase.started")
	if len(phaseStarted) != 1 {
		t.Fatalf("expected 1 phase.started, got %d", len(phaseStarted))
	}
	sessionStarted := filterEventsByType(events, "session.started")
	if len(sessionStarted) != 1 {
		t.Fatalf("expected 1 session.started, got %d", len(sessionStarted))
	}
	if sessionStarted[0].ParentSpanID != phaseStarted[0].SpanID {
		t.Errorf("session.started ParentSpanID = %q, want phase SpanID %q",
			sessionStarted[0].ParentSpanID, phaseStarted[0].SpanID)
	}

	// Verify Phase field
	if phaseStarted[0].Phase != "Research" {
		t.Errorf("phase.started Phase = %q, want %q", phaseStarted[0].Phase, "Research")
	}
}

func TestRunKBForRepoEmitsEventsWithRepoName(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	observeDir := t.TempDir()
	featureID := "test-observe-kb-001"
	os.MkdirAll(filepath.Join(observeDir, featureID), 0755)
	obs := observe.New(true, observeDir, false, "", false, "agentic")

	// stateDir must be structured so KBStateDir resolves correctly:
	// KBStateDir(stateDir, repoName) = filepath.Dir(stateDir) + "/knowledge-base/" + repoName
	// So stateDir must be something like tmpDir/features
	tmpBase := t.TempDir()
	stateDir := filepath.Join(tmpBase, "features")
	os.MkdirAll(stateDir, 0755)
	store := feature.NewStore(stateDir)
	repoDir := t.TempDir()

	f := &feature.Feature{
		ID:           featureID,
		Name:         "Test KB Observe",
		Slug:         "test-kb-observe",
		Description:  "Test observability for KB phase",
		Status:       feature.StatusResearching,
		CurrentPhase: feature.PhaseKnowledgeBase,
		TraceID:      "trace-test-kb-001",
		Repos:        []feature.FeatureRepo{{Name: "myrepo", Path: repoDir}},
		Models:       config.ModelConfig{KBBuild: "test-kb-model"},
		ExitCriteria: "test criteria",
	}
	store.Save(f)

	// Create PID dir so session can write PID file
	os.MkdirAll(filepath.Join(stateDir, featureID), 0755)

	repo := feature.FeatureRepo{Name: "myrepo", Path: repoDir}

	// The sleep ensures the process is still alive when the manager sends
	// the initialize handshake on stdin.
	scriptDir := t.TempDir()
	agentScript := testutil.WriteScript(t, scriptDir, "agent.sh",
		testutil.JSONLInit+"\nsleep 0.1\n"+testutil.JSONLSuccess+"\n")

	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)

	pr := &PhaseRunner{
		SessionManager: sm,
		FeatureStore:   store,
		StateDir:       stateDir,
		Observer:       obs,
		BuildSessionFn: func(opts BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
			return []string{"bash", agentScript}, nil, &session.SessionOpts{
				PIDDir:   opts.PIDDir,
				RepoName: opts.RepoName,
			}, nil
		},
	}

	sessionID, err := pr.RunKnowledgeBaseForRepo(f, repo)
	if err != nil {
		t.Fatalf("RunKnowledgeBaseForRepo error: %v", err)
	}
	if sessionID == "" {
		t.Fatal("expected non-empty session ID")
	}

	// Wait for async session
	sv := sm.GetSession(sessionID)
	if sv != nil {
		sv.Wait()
	}
	sm.Shutdown()

	// Read events
	events := readObserveEvents(t, observeDir, featureID)

	// Assert event order
	assertEventOrder(t, events, "phase.started", "session.started", "session.ended")

	// No phase.completed for async phases
	if len(filterEventsByType(events, "phase.completed")) != 0 {
		t.Error("phase.completed should NOT be emitted for async KB phase")
	}

	// Verify TraceID consistency
	for _, evt := range events {
		if evt.TraceID != f.TraceID {
			t.Errorf("event %q has TraceID %q, want %q", evt.EventType, evt.TraceID, f.TraceID)
		}
	}

	// Verify FeatureID
	for _, evt := range events {
		if evt.FeatureID != featureID {
			t.Errorf("event %q has FeatureID %q, want %q", evt.EventType, evt.FeatureID, featureID)
		}
	}

	// Verify session.started has RepoName set
	sessionStarted := filterEventsByType(events, "session.started")
	if len(sessionStarted) != 1 {
		t.Fatalf("expected 1 session.started, got %d", len(sessionStarted))
	}
	if sessionStarted[0].RepoName != "myrepo" {
		t.Errorf("session.started RepoName = %q, want %q", sessionStarted[0].RepoName, "myrepo")
	}

	// Verify session.ended also has RepoName
	sessionEnded := filterEventsByType(events, "session.ended")
	if len(sessionEnded) != 1 {
		t.Fatalf("expected 1 session.ended, got %d", len(sessionEnded))
	}
	if sessionEnded[0].RepoName != "myrepo" {
		t.Errorf("session.ended RepoName = %q, want %q", sessionEnded[0].RepoName, "myrepo")
	}

	// Verify Phase field
	phaseStarted := filterEventsByType(events, "phase.started")
	if len(phaseStarted) != 1 {
		t.Fatalf("expected 1 phase.started, got %d", len(phaseStarted))
	}
	if phaseStarted[0].Phase != "Knowledge Base" {
		t.Errorf("phase.started Phase = %q, want %q", phaseStarted[0].Phase, "Knowledge Base")
	}

	// Verify SpanContext hierarchy
	if sessionStarted[0].ParentSpanID != phaseStarted[0].SpanID {
		t.Errorf("session.started ParentSpanID = %q, want phase SpanID %q",
			sessionStarted[0].ParentSpanID, phaseStarted[0].SpanID)
	}

	// Verify KB lock was released (cleanup func ran)
	kbDir := KBStateDir(stateDir, repo.Name)
	lockPath := KBLockPath(kbDir)
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Error("KB lock should have been released after session ended")
	}
}

func TestImplementLoopMultiIterationEmitsUniqueSpans(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	observeDir := t.TempDir()
	featureID := "test-observe-multi-iter-001"
	os.MkdirAll(filepath.Join(observeDir, featureID), 0755)
	obs := observe.New(true, observeDir, false, "", false, "agentic")

	stateDir := t.TempDir()
	store := feature.NewStore(stateDir)
	repoDir := t.TempDir()

	f := &feature.Feature{
		ID:           featureID,
		Name:         "Test Multi Iteration Observe",
		Slug:         "test-multi-iter-observe",
		Description:  "Test multi-iteration observability",
		Status:       feature.StatusImplementing,
		CurrentPhase: feature.PhaseImplement,
		TraceID:      "trace-test-multi-iter-001",
		Repos:        []feature.FeatureRepo{{Name: "agentic", Path: repoDir}},
		Models: config.ModelConfig{
			Implementation: "test-model",
			Review:         "test-review-model",
		},
		ExitCriteria: "test criteria",
	}
	store.Save(f)

	artifactDir := filepath.Join(stateDir, featureID, "phase-impl", "implement", "agentic")
	os.MkdirAll(artifactDir, 0755)

	// Agent script: first call exits without result (→ FAILED/retry),
	// second call writes phase_complete and emits success.
	progressFile := filepath.Join(repoDir, "progress.md")
	scriptDir := t.TempDir()
	agentScript := testutil.WriteScript(t, scriptDir, "agent.sh", `
PROGRESS_FILE="`+progressFile+`"
`+testutil.JSONLInit+`
if [ ! -f "$PROGRESS_FILE" ]; then
    echo "# Progress" > "$PROGRESS_FILE"
    echo "Step 1 done" >> "$PROGRESS_FILE"
else
    echo "Step 2 done" >> "$PROGRESS_FILE"
    `+testutil.WriteImplementSuccessArtifacts(artifactDir)+`
    `+testutil.JSONLSuccess+`
fi
`)

	reviewScript := testutil.WriteScript(t, scriptDir, "review.sh",
		testutil.JSONLInit+"\n"+testutil.WriteReviewApproved(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")

	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	planPath := writePlanFile(t, artifactDir, "Implement a multi-iteration test feature")

	cfg := ImplementConfig{
		Feature:             f,
		FeatureStore:        store,
		WorkDir:             repoDir,
		PlanPath:            planPath,
		StateDir:            filepath.Join(stateDir, featureID),
		ArtifactDir:         artifactDir,
		RepoName:            "agentic",
		MaxIterations:       5,
		MaxConsecFails:      5,
		MaxConsecNoProgress: 5,
		Model:               "test-model",
		ReviewModel:         "test-review-model",
		BuildSession:        mockBuildSession(agentScript, reviewScript),
		ExitCriteria:        "test criteria",
		Observer:            obs,
	}

	result, err := RunImplementationLoop(cfg, sm)
	if err != nil {
		t.Fatalf("RunImplementationLoop error: %v", err)
	}
	if result.FinalStatus != "review_passed" {
		t.Fatalf("FinalStatus = %q, want %q", result.FinalStatus, "review_passed")
	}
	if result.Iterations != 2 {
		t.Fatalf("Iterations = %d, want 2", result.Iterations)
	}

	events := readObserveEvents(t, observeDir, featureID)

	// Should have 2 iteration.started and 2 iteration.ended events
	iterStarted := filterEventsByType(events, "iteration.started")
	if len(iterStarted) != 2 {
		t.Fatalf("expected 2 iteration.started events, got %d", len(iterStarted))
	}
	iterEnded := filterEventsByType(events, "iteration.ended")
	if len(iterEnded) != 2 {
		t.Fatalf("expected 2 iteration.ended events, got %d", len(iterEnded))
	}

	// Verify iteration numbers
	if iterStarted[0].Iteration != 1 {
		t.Errorf("first iteration.started Iteration = %d, want 1", iterStarted[0].Iteration)
	}
	if iterStarted[1].Iteration != 2 {
		t.Errorf("second iteration.started Iteration = %d, want 2", iterStarted[1].Iteration)
	}

	// Verify unique SpanIDs for each iteration
	if iterStarted[0].SpanID == iterStarted[1].SpanID {
		t.Error("iterations should have unique SpanIDs")
	}

	// Verify iteration 1 ended with non-success status (FAILED or similar)
	if iterEnded[0].Status == "review_passed" {
		t.Errorf("iteration 1 should not have status review_passed, got %q", iterEnded[0].Status)
	}
	// Verify iteration 2 ended with review_passed
	if iterEnded[1].Status != "review_passed" {
		t.Errorf("iteration 2 Status = %q, want %q", iterEnded[1].Status, "review_passed")
	}

	// Each iteration should have its own session.started/ended pair
	sessionStarted := filterEventsByType(events, "session.started")
	sessionEnded := filterEventsByType(events, "session.ended")
	// Iteration 1: 1 impl session; Iteration 2: 1 impl session + 1 review session = 3 total
	if len(sessionStarted) != 3 {
		t.Errorf("expected 3 session.started events (2 impl + 1 review), got %d", len(sessionStarted))
	}
	if len(sessionEnded) != 3 {
		t.Errorf("expected 3 session.ended events, got %d", len(sessionEnded))
	}

	// Review events should only appear once (iteration 2)
	reviewStarted := filterEventsByType(events, "review.started")
	if len(reviewStarted) != 1 {
		t.Errorf("expected 1 review.started event, got %d", len(reviewStarted))
	}

	// Verify phase.completed emitted once at the end
	phaseCompleted := filterEventsByType(events, "phase.completed")
	if len(phaseCompleted) != 1 {
		t.Fatalf("expected 1 phase.completed, got %d", len(phaseCompleted))
	}
	if phaseCompleted[0].Error != "" {
		t.Errorf("phase.completed should have no error for success path, got %q", phaseCompleted[0].Error)
	}

	// Both iteration spans are children of the same phase span
	phaseStarted := filterEventsByType(events, "phase.started")
	if len(phaseStarted) != 1 {
		t.Fatalf("expected 1 phase.started, got %d", len(phaseStarted))
	}
	if iterStarted[0].ParentSpanID != phaseStarted[0].SpanID {
		t.Errorf("iteration 1 ParentSpanID = %q, want phase SpanID %q",
			iterStarted[0].ParentSpanID, phaseStarted[0].SpanID)
	}
	if iterStarted[1].ParentSpanID != phaseStarted[0].SpanID {
		t.Errorf("iteration 2 ParentSpanID = %q, want phase SpanID %q",
			iterStarted[1].ParentSpanID, phaseStarted[0].SpanID)
	}
}

func TestImplementLoopFailurePathEmitsPhaseCompleted(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	observeDir := t.TempDir()
	featureID := "test-observe-failure-001"
	os.MkdirAll(filepath.Join(observeDir, featureID), 0755)
	obs := observe.New(true, observeDir, false, "", false, "agentic")

	stateDir := t.TempDir()
	store := feature.NewStore(stateDir)
	repoDir := t.TempDir()

	f := &feature.Feature{
		ID:           featureID,
		Name:         "Test Failure Observe",
		Slug:         "test-failure-observe",
		Description:  "Test failure path observability",
		Status:       feature.StatusImplementing,
		CurrentPhase: feature.PhaseImplement,
		TraceID:      "trace-test-failure-001",
		Repos:        []feature.FeatureRepo{{Name: "agentic", Path: repoDir}},
		Models: config.ModelConfig{
			Implementation: "test-model",
			Review:         "test-review-model",
		},
		ExitCriteria: "test criteria",
	}
	store.Save(f)

	artifactDir := filepath.Join(stateDir, featureID, "phase-impl", "implement", "agentic")
	os.MkdirAll(artifactDir, 0755)

	// Agent script: exits with error (no result message → FAILED)
	scriptDir := t.TempDir()
	agentScript := testutil.WriteScript(t, scriptDir, "agent.sh",
		testutil.JSONLInit+"\n"+`exit 1`+"\n")

	// Review script should never be called (agent always fails before review)
	reviewScript := testutil.WriteScript(t, scriptDir, "review.sh",
		testutil.JSONLInit+"\n"+testutil.WriteReviewApproved(artifactDir)+"\n"+testutil.JSONLSuccess+"\n")

	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	planPath := writePlanFile(t, artifactDir, "Some plan")

	cfg := ImplementConfig{
		Feature:             f,
		FeatureStore:        store,
		WorkDir:             repoDir,
		PlanPath:            planPath,
		StateDir:            filepath.Join(stateDir, featureID),
		ArtifactDir:         artifactDir,
		RepoName:            "agentic",
		MaxIterations:       10,
		MaxConsecFails:      2, // Trigger safety rail after 2 failures
		MaxConsecNoProgress: 10,
		Model:               "test-model",
		ReviewModel:         "test-review-model",
		BuildSession:        mockBuildSession(agentScript, reviewScript),
		ExitCriteria:        "test criteria",
		Observer:            obs,
	}

	result, err := RunImplementationLoop(cfg, sm)
	if err != nil {
		t.Fatalf("RunImplementationLoop error: %v", err)
	}
	if result.FinalStatus != "safety_rail" {
		t.Fatalf("FinalStatus = %q, want %q", result.FinalStatus, "safety_rail")
	}
	if result.Iterations != 2 {
		t.Fatalf("Iterations = %d, want 2", result.Iterations)
	}

	events := readObserveEvents(t, observeDir, featureID)

	// Verify event order includes phase.started and phase.failed
	// PhaseCompleted emits "phase.failed" when err != nil
	assertEventOrder(t, events, "phase.started", "iteration.started", "phase.failed")

	// Verify 2 iterations ran
	iterStarted := filterEventsByType(events, "iteration.started")
	if len(iterStarted) != 2 {
		t.Fatalf("expected 2 iteration.started events, got %d", len(iterStarted))
	}
	iterEnded := filterEventsByType(events, "iteration.ended")
	if len(iterEnded) != 2 {
		t.Fatalf("expected 2 iteration.ended events, got %d", len(iterEnded))
	}

	// Both iterations ended with non-success status
	for idx, ie := range iterEnded {
		if ie.Status == "review_passed" {
			t.Errorf("iteration %d should not have status review_passed", idx+1)
		}
	}

	// No review events should exist (agent never succeeded to reach review)
	reviewStarted := filterEventsByType(events, "review.started")
	if len(reviewStarted) != 0 {
		t.Errorf("expected 0 review.started events (agent always failed), got %d", len(reviewStarted))
	}

	// PhaseCompleted emits "phase.failed" (not "phase.completed") when err != nil
	phaseFailed := filterEventsByType(events, "phase.failed")
	if len(phaseFailed) != 1 {
		t.Fatalf("expected 1 phase.failed, got %d", len(phaseFailed))
	}
	if phaseFailed[0].Error == "" {
		t.Error("phase.failed should have error set for failure path")
	}
	if phaseFailed[0].Status != "failed" {
		t.Errorf("phase.failed Status = %q, want %q", phaseFailed[0].Status, "failed")
	}

	// Verify session.ended events exist for each iteration's impl session
	sessionEnded := filterEventsByType(events, "session.ended")
	if len(sessionEnded) != 2 {
		t.Errorf("expected 2 session.ended events (1 per failed iteration), got %d", len(sessionEnded))
	}

	// TraceID consistency
	for _, evt := range events {
		if evt.TraceID != f.TraceID {
			t.Errorf("event %q has TraceID %q, want %q", evt.EventType, evt.TraceID, f.TraceID)
		}
	}
}
