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

package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// TestFeatureLifecycleStateMachine tests feature creation and transitions
// through the full lifecycle: Created → Researching → PlanReady → Planning → ImplementReady → Implementing.
func TestFeatureLifecycleStateMachine(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	stateDir := filepath.Join(tmpDir, "state")
	repoDir := filepath.Join(tmpDir, "repo")
	os.MkdirAll(stateDir, 0o755)
	os.MkdirAll(repoDir, 0o755)

	store := feature.NewStore(stateDir)
	cfg := &config.Config{
		Defaults: config.DefaultsConfig{
			Models: config.ModelConfig{
				Research:       "test-research",
				Planning:       "test-planning",
				Implementation: "test-impl",
				Review:         "test-review",
			},
			ExitCriteria:  "Relevant tests pass",
			MaxIterations: 10,
		},
		Repos: map[string]config.RepoConfig{
			"test-repo": {Path: repoDir},
		},
	}

	fm := feature.NewManager(store, cfg)

	// Step 1: Create feature
	f, err := fm.Create(
		"Test Lifecycle Feature",
		"End-to-end lifecycle test",
		[]string{"test-repo"},
		cfg.Defaults.Models,
		"",
		"",
		nil,
	)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if f.Status != feature.StatusCreated {
		t.Errorf("expected StatusCreated, got %s", f.Status)
	}
	if f.CurrentPhase != feature.PhaseKnowledgeBase {
		t.Errorf("expected PhaseKnowledgeBase, got %s", f.CurrentPhase)
	}

	// Verify persistence
	loaded, err := fm.Get(f.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if loaded.Name != f.Name {
		t.Errorf("expected name %q, got %q", f.Name, loaded.Name)
	}

	// Step 2: Build knowledge base
	if err := fm.StartKnowledgeBase(f.ID); err != nil {
		t.Fatalf("StartKnowledgeBase: %v", err)
	}
	f, _ = fm.Get(f.ID)
	if f.Status != feature.StatusBuildingKB {
		t.Errorf("expected StatusBuildingKB, got %s", f.Status)
	}
	if f.CurrentPhase != feature.PhaseKnowledgeBase {
		t.Errorf("expected PhaseKnowledgeBase, got %s", f.CurrentPhase)
	}

	// Step 2b: Complete KB → back to Created
	if err := fm.CompleteKnowledgeBase(f.ID); err != nil {
		t.Fatalf("CompleteKnowledgeBase: %v", err)
	}
	f, _ = fm.Get(f.ID)
	if f.Status != feature.StatusCreated {
		t.Errorf("expected StatusCreated after KB, got %s", f.Status)
	}

	// Step 3: Start research
	if err := fm.StartResearch(f.ID); err != nil {
		t.Fatalf("StartResearch: %v", err)
	}
	f, _ = fm.Get(f.ID)
	if f.Status != feature.StatusResearching {
		t.Errorf("expected StatusResearching, got %s", f.Status)
	}

	// Step 3: Complete research → DesignReady
	if err := fm.CompleteResearch(f.ID); err != nil {
		t.Fatalf("CompleteResearch: %v", err)
	}
	f, _ = fm.Get(f.ID)
	if f.Status != feature.StatusDesignReady {
		t.Errorf("expected StatusDesignReady, got %s", f.Status)
	}

	// Step 3b: Start and complete design → PlanReady
	if err := fm.StartDesign(f.ID); err != nil {
		t.Fatalf("StartDesign: %v", err)
	}
	if err := fm.CompleteDesign(f.ID); err != nil {
		t.Fatalf("CompleteDesign: %v", err)
	}
	f, _ = fm.Get(f.ID)
	if f.Status != feature.StatusPlanReady {
		t.Errorf("expected StatusPlanReady, got %s", f.Status)
	}

	// Step 4: Start planning
	if err := fm.StartPlanning(f.ID); err != nil {
		t.Fatalf("StartPlanning: %v", err)
	}
	f, _ = fm.Get(f.ID)
	if f.Status != feature.StatusPlanning {
		t.Errorf("expected StatusPlanning, got %s", f.Status)
	}
	if f.CurrentPhase != feature.PhasePlan {
		t.Errorf("expected PhasePlan, got %s", f.CurrentPhase)
	}

	// Step 5: Complete planning → ImplementReady
	if err := fm.CompletePlanning(f.ID); err != nil {
		t.Fatalf("CompletePlanning: %v", err)
	}
	f, _ = fm.Get(f.ID)
	if f.Status != feature.StatusImplementReady {
		t.Errorf("expected StatusImplementReady, got %s", f.Status)
	}

	// Step 6: Start implementation
	if err := fm.StartImplementation(f.ID); err != nil {
		t.Fatalf("StartImplementation: %v", err)
	}
	f, _ = fm.Get(f.ID)
	if f.Status != feature.StatusImplementing {
		t.Errorf("expected StatusImplementing, got %s", f.Status)
	}
	if f.CurrentPhase != feature.PhaseImplement {
		t.Errorf("expected PhaseImplement, got %s", f.CurrentPhase)
	}
	if f.CurrentIteration != 1 {
		t.Errorf("expected CurrentIteration=1, got %d", f.CurrentIteration)
	}

	// Step 7: Complete implementation → ReviewPassed → PRReady
	if err := fm.CompleteImplementation(f.ID); err != nil {
		t.Fatalf("CompleteImplementation: %v", err)
	}
	f, _ = fm.Get(f.ID)
	if f.Status != feature.StatusReviewPassed {
		t.Errorf("expected StatusReviewPassed, got %s", f.Status)
	}

	if err := fm.MarkCodeReady(f.ID); err != nil {
		t.Fatalf("MarkCodeReady: %v", err)
	}
	f, _ = fm.Get(f.ID)
	if f.Status != feature.StatusCodeReady {
		t.Errorf("expected StatusCodeReady, got %s", f.Status)
	}
	if f.CurrentPhase != feature.PhasePublish {
		t.Errorf("expected PhasePublish, got %s", f.CurrentPhase)
	}

	// Step 8: Mark published
	if err := fm.MarkPublished(f.ID, "https://github.com/example/repo/pull/1"); err != nil {
		t.Fatalf("MarkPublished: %v", err)
	}
	f, _ = fm.Get(f.ID)
	if f.Status != feature.StatusPublished {
		t.Errorf("expected StatusPublished, got %s", f.Status)
	}

	// Verify feature list includes our feature
	features, err := fm.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := false
	for _, feat := range features {
		if feat.ID == f.ID {
			found = true
		}
	}
	if !found {
		t.Error("expected feature to appear in list")
	}
}

// TestSessionManagerEventPipeline tests that the session manager correctly
// routes parser events (permissions, help requests, status) through the event channel.
func TestSessionManagerEventPipeline(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()

	// Script emits stream-json init, assistant, and result messages
	initJSON := `{"type":"system","subtype":"init","session_id":"mock-events","model":"test"}`
	assistJSON := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Starting work..."}]}}`
	resultJSON := `{"type":"result","subtype":"success","session_id":"mock-events","total_cost_usd":0.001}`
	script := testutil.WriteScript(t, tmpDir, "events.sh", `
cat >/dev/null &
echo '`+initJSON+`'
echo '`+assistJSON+`'
echo '`+resultJSON+`'
`)

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	sess, err := sm.StartSession(
		"test-events",
		"feat-001",
		feature.PhaseImplement,
		[]string{"bash", script},
		tmpDir,
		nil,
	)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	// Collect events with timeout
	var sdkEvents []session.SDKEventMsg
	var doneMsg *session.SessionDoneMsg
	timeout := time.After(5 * time.Second)

	// Wait for the session to finish first
	select {
	case <-time.After(5 * time.Second):
		t.Fatal("session did not complete within timeout")
	case <-func() chan struct{} {
		ch := make(chan struct{})
		go func() {
			sess.Wait()
			close(ch)
		}()
		return ch
	}():
	}

	// Now drain all events
	drainTimeout := time.After(2 * time.Second)
drainLoop:
	for {
		select {
		case evt := <-eventCh:
			switch e := evt.(type) {
			case session.SDKEventMsg:
				sdkEvents = append(sdkEvents, e)
			case session.SessionDoneMsg:
				doneMsg = &e
			}
		case <-drainTimeout:
			break drainLoop
		default:
			// Retained in integration gate: bounded poll interval for goroutine events.
			time.Sleep(10 * time.Millisecond)
			select {
			case evt := <-eventCh:
				switch e := evt.(type) {
				case session.SDKEventMsg:
					sdkEvents = append(sdkEvents, e)
				case session.SessionDoneMsg:
					doneMsg = &e
				}
			default:
				break drainLoop
			}
		}
	}

	_ = timeout

	// Verify SUCCESS event received (result message with IsSuccess())
	gotSuccess := false
	for _, e := range sdkEvents {
		if e.Message.Result != nil && e.Message.Result.IsSuccess() {
			gotSuccess = true
		}
	}
	if !gotSuccess {
		t.Error("expected at least one SDK result message with IsSuccess()")
	}

	// Verify session done message
	if doneMsg == nil {
		t.Error("expected session done message")
	} else if doneMsg.SessionID != "test-events" {
		t.Errorf("expected done message for 'test-events', got %s", doneMsg.SessionID)
	}

	// Verify message log captured assistant text
	output := sess.MessageLog().AssistantText()
	if !strings.Contains(output, "Starting work") {
		t.Errorf("expected message log to contain 'Starting work', got: %s", output)
	}
}

// TestSessionManagerMultipleSessions tests running multiple sessions concurrently.
func TestSessionManagerMultipleSessions(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()

	// Two scripts that run concurrently, emitting stream-json
	assist1 := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Session 1 running"}]}}`
	assist2 := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Session 2 running"}]}}`
	script1 := testutil.WriteScript(t, tmpDir, "session1.sh",
		testutil.JSONLInit+"\n"+`echo '`+assist1+`'`+"\n"+testutil.JSONLSuccess+"\n")
	script2 := testutil.WriteScript(t, tmpDir, "session2.sh",
		testutil.JSONLInit+"\n"+`echo '`+assist2+`'`+"\n"+testutil.JSONLSuccess+"\n")

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	sess1, err := sm.StartSession("sess-1", "feat-1", feature.PhaseResearch, []string{"bash", script1}, tmpDir, nil)
	if err != nil {
		t.Fatalf("StartSession 1: %v", err)
	}
	sess2, err := sm.StartSession("sess-2", "feat-2", feature.PhaseResearch, []string{"bash", script2}, tmpDir, nil)
	if err != nil {
		t.Fatalf("StartSession 2: %v", err)
	}

	// Wait for both to complete
	done := make(chan struct{})
	go func() {
		sess1.Wait()
		sess2.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("sessions did not complete within timeout")
	}

	// Verify outputs from assistant messages
	out1 := sess1.MessageLog().AssistantText()
	if !strings.Contains(out1, "Session 1 running") {
		t.Errorf("session 1 output missing expected text: %s", out1)
	}

	out2 := sess2.MessageLog().AssistantText()
	if !strings.Contains(out2, "Session 2 running") {
		t.Errorf("session 2 output missing expected text: %s", out2)
	}

	// Verify active sessions returns empty after completion
	active := sm.ActiveSessions()
	if len(active) != 0 {
		t.Errorf("expected 0 active sessions, got %d", len(active))
	}
}

// TestConfigAndFeatureRoundTrip tests that config is properly loaded, repos are used
// to create features, and the feature state is persisted and retrievable.
func TestConfigAndFeatureRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	stateDir := filepath.Join(tmpDir, "state")
	repoDir := filepath.Join(tmpDir, "myrepo")
	os.MkdirAll(repoDir, 0o755)

	// Create and save config
	cfg := config.NewDefault()
	cfg.Repos["myrepo"] = config.RepoConfig{
		Path: repoDir,
	}
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatalf("Save config: %v", err)
	}

	// Load config back
	loaded, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("Load config: %v", err)
	}
	if _, ok := loaded.Repos["myrepo"]; !ok {
		t.Fatal("expected 'myrepo' in loaded config")
	}

	// Create feature manager and a feature
	store := feature.NewStore(stateDir)
	fm := feature.NewManager(store, loaded)

	f, err := fm.Create(
		"Config Round Trip Feature",
		"Validates config → feature creation → persistence",
		[]string{"myrepo"},
		loaded.Defaults.Models,
		"",
		"",
		nil,
	)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Verify feature persisted to disk
	featureDir := filepath.Join(stateDir, f.ID)
	featureFile := filepath.Join(featureDir, "feature.yaml")
	if _, err := os.Stat(featureFile); os.IsNotExist(err) {
		t.Errorf("expected feature.yaml at %s", featureFile)
	}

	// Create a new store pointing to the same directory and verify we can load
	store2 := feature.NewStore(stateDir)
	fm2 := feature.NewManager(store2, loaded)
	f2, err := fm2.Get(f.ID)
	if err != nil {
		t.Fatalf("Get from new store: %v", err)
	}
	if f2.Name != f.Name {
		t.Errorf("expected name %q, got %q", f.Name, f2.Name)
	}
	if f2.Description != f.Description {
		t.Errorf("expected description %q, got %q", f.Description, f2.Description)
	}
}

// TestSessionWithLogFile tests that session output is correctly written to a log file.
func TestSessionWithLogFile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()

	assist1 := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Line 1: hello"}]}}`
	assist2 := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Line 2: world"}]}}`
	script := testutil.WriteScript(t, tmpDir, "log-test.sh",
		testutil.JSONLInit+"\n"+`echo '`+assist1+`'`+"\n"+`echo '`+assist2+`'`+"\n"+testutil.JSONLSuccess+"\n")

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	logPath := filepath.Join(tmpDir, "output.txt")
	sess, err := sm.StartSession(
		"log-test",
		"feat-log",
		feature.PhaseResearch,
		[]string{"bash", script},
		tmpDir,
		nil,
		&session.SessionOpts{LogPath: logPath},
	)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	// Wait for completion
	done := make(chan struct{})
	go func() {
		sess.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("session did not complete within timeout")
	}

	// Verify log file contains JSONL output
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("Read log file: %v", err)
	}
	logStr := string(logData)
	if !strings.Contains(logStr, "Line 1: hello") {
		t.Errorf("expected log to contain 'Line 1: hello' in JSON, got: %s", logStr)
	}
	if !strings.Contains(logStr, `"subtype":"success"`) {
		t.Errorf("expected log to contain result message, got: %s", logStr)
	}

	// Verify message log captured assistant text
	assistText := sess.MessageLog().AssistantText()
	if !strings.Contains(assistText, "Line 2: world") {
		t.Errorf("expected message log to contain 'Line 2: world', got: %s", assistText)
	}
}

// TestPIDFileLifecycle tests that PID files are created during a session and cleaned up after.
func TestPIDFileLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	pidDir := filepath.Join(tmpDir, "pids")
	os.MkdirAll(pidDir, 0o755)

	script := testutil.WriteScript(t, tmpDir, "pid-test.sh",
		testutil.JSONLInit+"\n"+`sleep 0.2`+"\n"+testutil.JSONLSuccess+"\n")

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()

	sess, err := sm.StartSession(
		"pid-test",
		"feat-pid",
		feature.PhaseImplement,
		[]string{"bash", script},
		tmpDir,
		nil,
		&session.SessionOpts{PIDDir: pidDir, Iteration: 1, RepoName: "pid-repo"},
	)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	// Retained in integration gate: PID file creation is owned by a child process.
	time.Sleep(100 * time.Millisecond)

	// Verify PID file exists during execution
	pidFile := filepath.Join(pidDir, "session-pid-repo.pid")
	if _, err := os.Stat(pidFile); os.IsNotExist(err) {
		t.Error("expected PID file to exist during session execution")
	}

	// Wait for completion
	done := make(chan struct{})
	go func() {
		sess.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("session did not complete within timeout")
	}

	// Retained in integration gate: PID file cleanup is owned by a child process.
	time.Sleep(100 * time.Millisecond)

	// Verify PID file is removed after session
	if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
		t.Error("expected PID file to be removed after session completion")
	}
}
