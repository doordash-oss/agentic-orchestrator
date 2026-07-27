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

package e2e

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/server"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

const (
	automaticReviewJourneyFeatureID = "a11aa11aa11aa11a"
	automaticReviewJourneySessionID = "session-automatic-review"
	automaticReviewJourneyCommand   = "curl https://example.com/artifact"
)

// TestAutomaticReviewFreshAllowOwningSessionJourney proves that the owning
// session persists approval provenance before returning the native permission
// response and exposes the same indexed record through every read surface.
func TestAutomaticReviewFreshAllowOwningSessionJourney(t *testing.T) {
	if testing.Short() {
		t.Skip("journey launches deterministic provider subprocesses")
	}

	stateDir := t.TempDir()
	workDir := t.TempDir()
	store := feature.NewStore(stateDir)
	if err := store.Save(&feature.Feature{
		ID:           automaticReviewJourneyFeatureID,
		Name:         "Automatic review evidence",
		Slug:         "automatic-review-evidence",
		Created:      time.Now(),
		Status:       feature.StatusImplementing,
		CurrentPhase: feature.PhaseImplement,
	}); err != nil {
		t.Fatalf("save feature: %v", err)
	}

	observer := observe.New(true, stateDir, false, "", false, "agentic-test")
	phaseContext := observe.SpanContextForFeature(automaticReviewJourneyFeatureID, "", "", "").Child()
	observer.PhaseStarted(phaseContext, "implement")
	opts := automaticReviewJourneySessionOpts(t, store, workDir, observer, testutil.FakeClaudeAllowScriptBody())

	manager := session.NewManager(make(chan interface{}, 32))
	t.Cleanup(manager.Shutdown)
	releasePath := filepath.Join(t.TempDir(), "release")
	responsePath := filepath.Join(t.TempDir(), "permission-response.json")
	script := automaticReviewOwningSessionScript(t)
	sess, err := manager.StartSession(
		automaticReviewJourneySessionID,
		automaticReviewJourneyFeatureID,
		feature.PhaseImplement,
		[]string{"sh", script, releasePath, responsePath},
		workDir,
		nil,
		opts,
	)
	if err != nil {
		t.Fatalf("StartSession() error: %v", err)
	}
	if err := os.WriteFile(releasePath, []byte("run"), 0o600); err != nil {
		t.Fatalf("release owning session: %v", err)
	}
	waitForAutomaticReviewSession(t, sess)

	response, err := os.ReadFile(responsePath)
	if err != nil {
		t.Fatalf("read native permission response: %v", err)
	}
	if !strings.Contains(string(response), `"behavior":"allow"`) ||
		!strings.Contains(string(response), automaticReviewJourneyCommand) {
		t.Fatalf("native permission response = %s, want allow with original input", response)
	}

	wantStatus := permission.AutomaticReviewStatusLine(automaticReviewJourneyCommand)
	rawStatusIndex, rawBashIndex := automaticReviewRawOrder(t, sess.MessageLog().Messages(), wantStatus)
	if rawStatusIndex+1 != rawBashIndex {
		t.Fatalf("durable order = status %d, Bash %d; want adjacent status before activity", rawStatusIndex, rawBashIndex)
	}

	attached, err := manager.Attach(automaticReviewJourneySessionID)
	if err != nil {
		t.Fatalf("Attach() error: %v", err)
	}
	attachStatusIndex, attachBashIndex := automaticReviewRawOrder(t, attached.MessageLog().Messages(), wantStatus)
	if attachStatusIndex != rawStatusIndex || attachBashIndex != rawBashIndex {
		t.Fatalf("attach order = (%d,%d), want durable (%d,%d)", attachStatusIndex, attachBashIndex, rawStatusIndex, rawBashIndex)
	}

	httpServer := httptest.NewServer(server.NewHandler(server.HandlerOptions{
		FeatureStore:          store,
		Sessions:              manager,
		DisableHostValidation: true,
	}))
	t.Cleanup(httpServer.Close)
	client, err := server.NewClient(server.ClientOptions{BaseURL: httpServer.URL})
	if err != nil {
		t.Fatalf("NewClient() error: %v", err)
	}

	preview, err := client.LivePreview(t.Context(), automaticReviewJourneyFeatureID)
	if err != nil {
		t.Fatalf("LivePreview() error: %v", err)
	}
	previewStatusIndex, previewBashIndex := automaticReviewDTOOrder(t, preview.Transcript, wantStatus)

	transcript, err := client.Transcript(t.Context(), automaticReviewJourneySessionID, server.CursorQuery{Limit: 100})
	if err != nil {
		t.Fatalf("Transcript() error: %v", err)
	}
	transcriptStatusIndex, transcriptBashIndex := automaticReviewDTOOrder(t, transcript.Messages, wantStatus)

	records, streamErrors := client.SubscribeSessionOutput(t.Context(), automaticReviewJourneySessionID, server.SessionOutputStreamOptions{})
	var streamed []server.TranscriptMessageDTO
	for record := range records {
		streamed = append(streamed, record.Message)
	}
	if streamErr := <-streamErrors; streamErr != nil {
		t.Fatalf("SubscribeSessionOutput() error: %v", streamErr)
	}
	streamStatusIndex, streamBashIndex := automaticReviewDTOOrder(t, streamed, wantStatus)

	for surface, got := range map[string][2]int{
		"live preview": {previewStatusIndex, previewBashIndex},
		"transcript":   {transcriptStatusIndex, transcriptBashIndex},
		"SSE":          {streamStatusIndex, streamBashIndex},
	} {
		if got != [2]int{rawStatusIndex, rawBashIndex} {
			t.Fatalf("%s order = %v, want durable %v", surface, got, [2]int{rawStatusIndex, rawBashIndex})
		}
	}

	events := readAutomaticReviewEvents(t, stateDir, automaticReviewJourneyFeatureID)
	if strings.Count(events, `"event_type":"automatic_review.completed"`) != 1 ||
		!strings.Contains(events, `"status_persisted":true`) {
		t.Fatalf("automatic-review events do not prove one persisted leader allow:\n%s", events)
	}
}

// TestAutomaticReviewIntentionalSilenceJourneys proves that memoized,
// serialized-cache, and unreviewable request paths add no status or event. An
// unavailable reviewer is the exception at session scope: it emits one build
// notice, but still produces no per-request review attempt.
func TestAutomaticReviewIntentionalSilenceJourneys(t *testing.T) {
	if testing.Short() {
		t.Skip("journey launches deterministic provider subprocesses")
	}

	stateDir := t.TempDir()
	workDir := t.TempDir()
	store := feature.NewStore(stateDir)
	if err := os.MkdirAll(filepath.Join(stateDir, automaticReviewJourneyFeatureID), 0o755); err != nil {
		t.Fatalf("create feature event directory: %v", err)
	}
	observer := observe.New(true, stateDir, false, "", false, "agentic-test")
	slowAllow := testutil.FakeClaudeInitLines() +
		"sleep 0.2\n" +
		"printf '%s\\n' '{\"type\":\"assistant\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"ALLOW\"}]}}'\n" +
		"printf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\"}'\n"
	handler := automaticReviewJourneySessionOpts(t, store, workDir, observer, slowAllow).PermHandler

	var statusCalls atomic.Int32
	request := func(command string) ports.ToolPermissionRequest {
		return ports.ToolPermissionRequest{
			Ctx:              t.Context(),
			ToolName:         "Bash",
			Input:            `{"command":"` + command + `"}`,
			FeatureID:        automaticReviewJourneyFeatureID,
			LogicalSessionID: automaticReviewJourneySessionID,
			Phase:            feature.PhaseImplement,
			AppendStatus: func(string) error {
				statusCalls.Add(1)
				return nil
			},
		}
	}

	if got, err := handler.CanUseTool(request("curl https://example.com/agent")); err != nil || got.Behavior != permission.DecisionAllow {
		t.Fatalf("fresh cache leader = %+v, %v; want allow", got, err)
	}
	if got, err := handler.CanUseTool(request("curl https://example.com/agent")); err != nil || got.Behavior != permission.DecisionAllow {
		t.Fatalf("cache hit = %+v, %v; want allow", got, err)
	}
	if statusCalls.Load() != 1 || automaticReviewEventCount(t, stateDir) != 1 {
		t.Fatalf("cache hit added side effects: statuses=%d events=%d", statusCalls.Load(), automaticReviewEventCount(t, stateDir))
	}

	start := make(chan struct{})
	results := make(chan ports.PermissionDecision, 2)
	errs := make(chan error, 2)
	var callers sync.WaitGroup
	for range 2 {
		callers.Add(1)
		go func() {
			defer callers.Done()
			<-start
			got, err := handler.CanUseTool(request("curl https://example.com/server"))
			results <- got
			errs <- err
		}()
	}
	close(start)
	callers.Wait()
	close(results)
	close(errs)
	for result := range results {
		if result.Behavior != permission.DecisionAllow {
			t.Fatalf("serialized cached result = %+v, want allow", result)
		}
	}
	for err := range errs {
		if err != nil {
			t.Fatalf("serialized cached error: %v", err)
		}
	}
	if statusCalls.Load() != 2 || automaticReviewEventCount(t, stateDir) != 2 {
		t.Fatalf("follower added side effects: statuses=%d events=%d", statusCalls.Load(), automaticReviewEventCount(t, stateDir))
	}

	overLength := strings.Repeat("x", permission.GuardrailMaxCommandLen+1)
	if got, err := handler.CanUseTool(request(overLength)); err != nil || got.Behavior != "" {
		t.Fatalf("unreviewable result = %+v, %v; want human deferral", got, err)
	}
	if statusCalls.Load() != 2 || automaticReviewEventCount(t, stateDir) != 2 {
		t.Fatalf("unreviewable request added side effects: statuses=%d events=%d", statusCalls.Load(), automaticReviewEventCount(t, stateDir))
	}

	assertAutomaticReviewUnavailableSessionNotice(t)
}

func assertAutomaticReviewUnavailableSessionNotice(t *testing.T) {
	t.Helper()
	stateDir := t.TempDir()
	workDir := t.TempDir()
	store := feature.NewStore(stateDir)
	if err := os.MkdirAll(filepath.Join(stateDir, automaticReviewJourneyFeatureID), 0o755); err != nil {
		t.Fatalf("create unavailable-review event directory: %v", err)
	}

	providerScript := testutil.WriteFakeClaudeScript(t,
		testutil.FakeClaudeInitLines()+
			"printf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"session_id\":\"provider-session\"}'\n",
	)
	registry := llm.NewRegistry()
	registry.Register(unavailableReviewFakeClaude{FakeClaudeProvider: testutil.FakeClaudeProvider{Script: providerScript}})

	cfg := config.NewDefault()
	cfg.Defaults.AutomaticReviewEnabled = true
	observer := observe.New(true, stateDir, false, "", false, "agentic-test")
	runner := agent.NewPhaseRunner(nil, store, store.BaseDir)
	runner.Registry = registry
	runner.Config = cfg
	runner.Observer = observer
	cmd, env, opts, err := runner.BuildSession(agent.BuildSessionOpts{
		Model:       "haiku",
		Prompt:      "exercise unavailable automatic review",
		PermHandler: permission.Guarded(&permission.AcceptEditsHandler{}),
		WorkDir:     workDir,
		Phase:       feature.PhaseImplement,
	})
	if err != nil {
		t.Fatalf("BuildSession() unavailable reviewer error: %v", err)
	}

	manager := session.NewManager(nil)
	t.Cleanup(manager.Shutdown)
	sess, err := manager.StartSession(
		automaticReviewJourneySessionID+"-unavailable",
		automaticReviewJourneyFeatureID,
		feature.PhaseImplement,
		cmd,
		workDir,
		env,
		opts,
	)
	if err != nil {
		t.Fatalf("StartSession() unavailable reviewer error: %v", err)
	}
	waitForAutomaticReviewSession(t, sess)

	const noticePrefix = "Automatic review enabled but no reviewer available:"
	notices := 0
	for _, msg := range sess.MessageLog().Messages() {
		if msg.Status != nil && strings.HasPrefix(msg.Status.Message, noticePrefix) {
			notices++
		}
	}
	if notices != 1 {
		t.Fatalf("unavailable-review session notices = %d, want one", notices)
	}
	events := readAutomaticReviewEvents(t, stateDir, automaticReviewJourneyFeatureID)
	if strings.Count(events, `"event_type":"automatic_review.unavailable"`) != 1 ||
		strings.Contains(events, `"event_type":"automatic_review.completed"`) {
		t.Fatalf("unavailable-review events = %s, want one build notice and no request attempt", events)
	}
}

// TestAutomaticReviewSideEffectFailureJourney proves status and observer sink
// failures cannot change the native allow response or prevent state cleanup.
func TestAutomaticReviewSideEffectFailureJourney(t *testing.T) {
	if testing.Short() {
		t.Skip("journey launches deterministic provider subprocesses")
	}

	stateDir := t.TempDir()
	workDir := t.TempDir()
	store := feature.NewStore(stateDir)
	brokenObserverRoot := filepath.Join(t.TempDir(), "observer-root")
	if err := os.WriteFile(brokenObserverRoot, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("create broken observer root: %v", err)
	}
	observer := observe.New(true, brokenObserverRoot, false, "", false, "agentic-test")
	opts := automaticReviewJourneySessionOpts(t, store, workDir, observer, testutil.FakeClaudeAllowScriptBody())
	failingHandler := &automaticReviewStatusFailingHandler{inner: opts.PermHandler}
	opts.PermHandler = failingHandler

	manager := session.NewManager(nil)
	t.Cleanup(manager.Shutdown)
	releasePath := filepath.Join(t.TempDir(), "release")
	responsePath := filepath.Join(t.TempDir(), "permission-response.json")
	sess, err := manager.StartSession(
		automaticReviewJourneySessionID,
		automaticReviewJourneyFeatureID,
		feature.PhaseImplement,
		[]string{"sh", automaticReviewOwningSessionScript(t), releasePath, responsePath},
		workDir,
		nil,
		opts,
	)
	if err != nil {
		t.Fatalf("StartSession() error: %v", err)
	}
	if err := os.WriteFile(releasePath, []byte("run"), 0o600); err != nil {
		t.Fatalf("release owning session: %v", err)
	}
	waitForAutomaticReviewSession(t, sess)

	response, err := os.ReadFile(responsePath)
	if err != nil {
		t.Fatalf("read native permission response: %v", err)
	}
	if strings.Count(string(response), `"behavior":"allow"`) != 1 {
		t.Fatalf("native callback response = %s, want one allow", response)
	}
	for _, msg := range sess.MessageLog().Messages() {
		if msg.Status != nil {
			t.Fatalf("failed status unexpectedly persisted: %+v", msg.Status)
		}
	}
	if !automaticReviewHasBashActivity(sess.MessageLog().Messages()) {
		t.Fatal("native Bash activity did not continue after side-effect failures")
	}

	got, err := failingHandler.CanUseTool(ports.ToolPermissionRequest{
		Ctx:          context.Background(),
		ToolName:     "Bash",
		Input:        `{"command":"curl https://example.com/artifact"}`,
		AppendStatus: func(string) error { return errors.New("status sink unavailable") },
	})
	if err != nil || got.Behavior != "" {
		t.Fatalf("post-session handler = %+v, %v; want disposed human deferral", got, err)
	}
}

type automaticReviewStatusFailingHandler struct {
	inner ports.PermissionHandler
}

func (h *automaticReviewStatusFailingHandler) CanUseTool(req ports.ToolPermissionRequest) (ports.PermissionDecision, error) {
	req.AppendStatus = func(string) error {
		return errors.New("status sink unavailable token=private")
	}
	return h.inner.CanUseTool(req)
}

func (h *automaticReviewStatusFailingHandler) Dispose() {
	if disposable, ok := h.inner.(interface{ Dispose() }); ok {
		disposable.Dispose()
	}
}

func automaticReviewJourneySessionOpts(
	t *testing.T,
	store *feature.Store,
	workDir string,
	observer *observe.Observer,
	reviewerScript string,
) *ports.SessionOpts {
	t.Helper()
	cfg := config.NewDefault()
	cfg.Defaults.AutomaticReviewEnabled = true
	runner := agent.NewPhaseRunner(nil, store, store.BaseDir)
	runner.Registry = fakeRegistry(t, reviewerScript)
	runner.Config = cfg
	runner.Observer = observer
	_, _, opts, err := runner.BuildSession(agent.BuildSessionOpts{
		Model:       "haiku",
		Prompt:      "exercise automatic review",
		PermHandler: permission.Guarded(&permission.AcceptEditsHandler{}),
		WorkDir:     workDir,
		Phase:       feature.PhaseImplement,
	})
	if err != nil {
		t.Fatalf("BuildSession() error: %v", err)
	}
	opts.Protocol = nil
	opts.ResultShutdownGrace = 50 * time.Millisecond
	return opts
}

func automaticReviewOwningSessionScript(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "owning-session.sh")
	body := `#!/bin/sh
head -n 2 >/dev/null
while [ ! -f "$1" ]; do sleep 0.01; done
printf '%s\n' '{"type":"system","subtype":"init","session_id":"provider-session","model":"haiku[200K]"}'
printf '%s\n' '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Checking the command."}]}}'
printf '%s\n' '{"type":"control_request","request_id":"permission-1","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"curl https://example.com/artifact"}}}'
IFS= read -r response
printf '%s\n' "$response" >"$2"
printf '%s\n' '{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu-approved","name":"Bash","input":{"command":"curl https://example.com/artifact"}}]}}'
printf '%s\n' '{"type":"result","subtype":"success","session_id":"provider-session"}'
`
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write owning session script: %v", err)
	}
	return path
}

func waitForAutomaticReviewSession(t *testing.T, sess ports.SessionView) {
	t.Helper()
	select {
	case <-sess.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for owning session")
	}
	if sess.Status() != ports.SessionDone {
		t.Fatalf("owning session status = %s, want done; detail=%s", sess.Status(), sess.ErrorDetail())
	}
}

func automaticReviewRawOrder(t *testing.T, messages []llm.SDKMessage, wantStatus string) (int, int) {
	t.Helper()
	statusIndex, bashIndex := -1, -1
	for i, msg := range messages {
		if msg.Status != nil && msg.Status.Message == wantStatus {
			statusIndex = i
		}
		if automaticReviewHasBashActivity([]llm.SDKMessage{msg}) {
			bashIndex = i
		}
	}
	if statusIndex < 0 || bashIndex < 0 || statusIndex >= bashIndex {
		t.Fatalf("message order = status %d, Bash %d; messages=%+v", statusIndex, bashIndex, messages)
	}
	return statusIndex, bashIndex
}

func automaticReviewDTOOrder(t *testing.T, messages []server.TranscriptMessageDTO, wantStatus string) (int, int) {
	t.Helper()
	statusIndex, bashIndex := -1, -1
	for _, msg := range messages {
		if msg.Type == "status" && msg.Text == wantStatus {
			statusIndex = msg.Index
		}
		if msg.Type == "tool_use" && msg.Tool == "Bash" {
			bashIndex = msg.Index
		}
	}
	if statusIndex < 0 || bashIndex < 0 || statusIndex >= bashIndex {
		t.Fatalf("transcript order = status %d, Bash %d; messages=%+v", statusIndex, bashIndex, messages)
	}
	return statusIndex, bashIndex
}

func automaticReviewHasBashActivity(messages []llm.SDKMessage) bool {
	for _, msg := range messages {
		if msg.Assistant == nil {
			continue
		}
		for _, block := range msg.Assistant.Message.Content {
			if block.IsToolUse() && block.Name == "Bash" {
				return true
			}
		}
	}
	return false
}

func readAutomaticReviewEvents(t *testing.T, stateDir, featureID string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(stateDir, featureID, "events.jsonl"))
	if err != nil {
		t.Fatalf("read automatic-review events: %v", err)
	}
	return string(data)
}

func automaticReviewEventCount(t *testing.T, stateDir string) int {
	t.Helper()
	return strings.Count(
		readAutomaticReviewEvents(t, stateDir, automaticReviewJourneyFeatureID),
		`"event_type":"automatic_review.completed"`,
	)
}
