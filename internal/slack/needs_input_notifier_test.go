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

package slack

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/internal/slack/testsupport"
	"gopkg.in/yaml.v3"
)

func TestSlackNeedsInputEvidence(t *testing.T) {
	const secondSecret = "xoxp-NEEDS-INPUT-987654321"
	harness := newNotifierHarness(t, defaultTestSettings(testToken, testRecipients()...))
	harness.seedFeature("F-1", func(f *feature.Feature) {
		f.Repos = []feature.FeatureRepo{{Name: "agentic-orchestrator"}}
	})
	harness.seedFeature("F-child", func(f *feature.Feature) {
		f.Name = "Refactor child"
		f.Parent = &feature.ChildRelationship{ParentID: "F-1", Kind: feature.ChildKindRefactor}
	})
	permission := ports.SlackPendingInput{
		Kind:         ports.SlackPendingPermission,
		FeatureID:    "F-1",
		RequestID:    "perm-1",
		ToolName:     "Bash",
		Phase:        "implement",
		RepoName:     "agentic-orchestrator",
		WaitingSince: time.Date(2026, 9, 22, 18, 0, 0, 0, time.UTC),
		Input: map[string]any{"command": "cd /workspace/agentic-orchestrator && npm publish --token " +
			testToken + " --registry-token " + secondSecret +
			" && curl https://release-user:release-password@example.test/private && " +
			strings.Repeat("echo release-ready; ", 220)},
	}
	bundle := []ports.SlackPendingInput{
		{
			Kind: ports.SlackPendingQuestion, FeatureID: "F-1", RequestID: "ask-1",
			QuestionIndex: 0, QuestionCount: 2, Header: "Scope",
			Question: "Which scope should preserve " + testToken + "?",
			Options: []ports.SlackPendingInputOption{
				{Label: "Focused", Description: "Only the Slack relay", Confidence: .86, HasConfidence: true},
				{Label: "Broad", Description: "Every notification path", Confidence: .31, HasConfidence: true},
				{Label: "Minimal", Description: "Documentation only", Confidence: .12, HasConfidence: true},
			},
		},
		{
			Kind: ports.SlackPendingQuestion, FeatureID: "F-1", RequestID: "ask-1",
			QuestionIndex: 1, QuestionCount: 2, Header: "Gates", MultiSelect: true,
			Question: "Which verification gates should be enabled?",
			Options: []ports.SlackPendingInputOption{
				{Label: "Fast suite", Description: "Run the normal unit gates", Confidence: .77, HasConfidence: true},
				{Label: "Race suite", Description: "Run concurrency checks", Confidence: .64, HasConfidence: true},
			},
		},
	}
	freeText := ports.SlackPendingInput{
		Kind: ports.SlackPendingQuestion, FeatureID: "F-1", RequestID: "ask-2",
		QuestionIndex: 0, QuestionCount: 1, Header: "Release note",
		Question: "What wording should appear around Authorization: Bearer " + secondSecret + "?",
	}
	gate := ports.SlackPendingInput{
		Kind: ports.SlackPendingGate, FeatureID: "F-1",
		GatePath: "/state/F-1/iteration-02/need-user-input.yaml", Iteration: 2,
		WaitingSince: time.Date(2026, 9, 22, 18, 5, 0, 0, time.UTC),
		GateSummary:  "Two checks require an authenticated Okta session " + testToken + ".",
		GateQuestions: []string{
			"Waive the blocked checks or retry after signing in?",
		},
		GateBlockers: []ports.SlackPendingInputBlocker{
			{Name: "Deployment smoke test", RepoName: "agentic-orchestrator", Command: "agentico verify deploy " + secondSecret, Reason: "Okta session expired", Remediation: "Sign in with https://user:pass@example.test and retry."},
			{Name: "Production API probe", RepoName: "agentic-orchestrator", Command: "agentico verify api", Reason: "Missing production credentials", Remediation: "Refresh credentials and retry."},
		},
	}
	help := ports.SlackPendingInput{
		Kind:         ports.SlackPendingHelp,
		FeatureID:    "F-1",
		HelpQuestion: "Which release owner should review " + testToken + " and the final output?",
		WaitingSince: time.Date(2026, 9, 22, 18, 6, 0, 0, time.UTC),
	}

	harness.pending.set("F-1", permission)
	notifier := harness.start(32)
	var steps []needsInputEvidenceStep

	notifier.RuntimeMessageTap(controlRuntimeMessage("F-1", "session-1", "perm-1"))
	waitFor(t, 10*time.Second, func() bool {
		return threadReplyCount(harness.server.AllRequests()) == 2 &&
			pendingRecordReady(harness.stateDir, "F-1", 1, 2)
	})
	steps = append(steps, captureNeedsInputEvidenceStep(t, harness, "permission"))

	threadCount := threadReplyCount(harness.server.AllRequests())
	notifier.RuntimeMessageTap(controlRuntimeMessage("F-1", "session-1", "perm-1"))
	waitFor(t, 10*time.Second, func() bool { return notifier.queue.len() == 0 })
	if got := threadReplyCount(harness.server.AllRequests()); got != threadCount {
		t.Fatalf("duplicate control request thread posts = %d; want %d", got, threadCount)
	}

	harness.pending.set("F-1", append([]ports.SlackPendingInput{permission}, bundle...)...)
	notifier.RuntimeMessageTap(controlRuntimeMessage("F-1", "session-1", "ask-1"))
	waitFor(t, 10*time.Second, func() bool {
		return threadReplyCount(harness.server.AllRequests()) == 6 &&
			pendingRecordReady(harness.stateDir, "F-1", 3, 2)
	})
	steps = append(steps, captureNeedsInputEvidenceStep(t, harness, "question_bundle"))

	parentItems := append([]ports.SlackPendingInput{permission}, bundle...)
	parentItems = append(parentItems, freeText)
	harness.pending.set("F-1", parentItems...)
	notifier.RuntimeMessageTap(controlRuntimeMessage("F-1", "session-1", "ask-2"))
	waitFor(t, 10*time.Second, func() bool {
		return threadReplyCount(harness.server.AllRequests()) == 8 &&
			pendingRecordReady(harness.stateDir, "F-1", 4, 2)
	})
	steps = append(steps, captureNeedsInputEvidenceStep(t, harness, "free_text_question"))

	parentItems = append(parentItems, gate)
	harness.pending.set("F-1", parentItems...)
	notifier.DomainEventTap(ports.Event{Type: ports.NeedUserInputRequired, FeatureID: "F-1"})
	waitFor(t, 10*time.Second, func() bool {
		return threadReplyCount(harness.server.AllRequests()) == 10 &&
			pendingRecordReady(harness.stateDir, "F-1", 5, 2)
	})
	steps = append(steps, captureNeedsInputEvidenceStep(t, harness, "verification_gate"))

	parentItems = append(parentItems, help)
	harness.pending.set("F-1", parentItems...)
	notifier.RuntimeMessageTap(session.SDKEventMsg{
		SessionID: "session-1", FeatureID: "F-1", Phase: feature.PhaseImplement,
		Message: llm.SDKMessage{Type: "assistant"},
	})
	waitFor(t, 10*time.Second, func() bool {
		return threadReplyCount(harness.server.AllRequests()) == 12 &&
			pendingRecordReady(harness.stateDir, "F-1", 6, 2)
	})
	steps = append(steps, captureNeedsInputEvidenceStep(t, harness, "help_request"))

	childPermission := permission
	childPermission.FeatureID = "F-child"
	childPermission.RequestID = "perm-child"
	childPermission.Input = map[string]any{"command": "go test ./internal/slack -run TestChild " + secondSecret}
	harness.pending.set("F-child", childPermission)
	notifier.RuntimeMessageTap(controlRuntimeMessage("F-child", "child-session", "perm-child"))
	waitFor(t, 10*time.Second, func() bool {
		record, ok := readFeatureRecord(harness.stateDir, "F-1")
		return ok && len(record.Pending) == 7 && record.Pending[6].Tag == "#7" &&
			len(record.Pending[6].MessageTS) == 2 && threadReplyCount(harness.server.AllRequests()) == 14
	})
	steps = append(steps, captureNeedsInputEvidenceStep(t, harness, "child_permission"))

	harness.pending.set("F-1", freeText, gate, help)
	notifier.RuntimeMessageTap(session.SDKEventMsg{
		SessionID: "session-1", FeatureID: "F-1", Phase: feature.PhaseImplement,
		Message: llm.SDKMessage{Type: "assistant"},
	})
	waitFor(t, 10*time.Second, func() bool {
		record, ok := readFeatureRecord(harness.stateDir, "F-1")
		return ok && len(record.Pending) == 4 && record.Pending[0].Tag == "#4" &&
			threadReplyCount(harness.server.AllRequests()) == 14
	})
	steps = append(steps, captureNeedsInputEvidenceStep(t, harness, "partial_retirement"))

	harness.pending.set("F-1")
	harness.pending.set("F-child")
	beforeInterruption := threadReplyCount(harness.server.AllRequests())
	harness.feed(ports.Event{
		Type: ports.FeatureInterrupted, FeatureID: "F-1", Phase: feature.PhaseImplement,
	})
	waitFor(t, 10*time.Second, func() bool {
		record, ok := readFeatureRecord(harness.stateDir, "F-1")
		return ok && len(record.Pending) == 0 &&
			threadReplyCount(harness.server.AllRequests()) == beforeInterruption+2
	})
	steps = append(steps, captureNeedsInputEvidenceStep(t, harness, "interruption"))

	harness.settings.mutate(func(settings *ports.SlackRuntimeSettings) {
		settings.Categories.NeedsInput = false
	})
	permission.RequestID = "perm-2"
	harness.pending.set("F-1", permission)
	notifier.RuntimeMessageTap(controlRuntimeMessage("F-1", "session-1", "perm-2"))
	waitFor(t, 10*time.Second, func() bool {
		record, ok := readFeatureRecord(harness.stateDir, "F-1")
		return ok && len(record.Pending) == 1 && record.Pending[0].Tag == ""
	})
	if got := threadReplyCount(harness.server.AllRequests()); got != beforeInterruption+2 {
		t.Fatalf("Needs input off replies = %d; want %d", got, beforeInterruption+2)
	}
	steps = append(steps, captureNeedsInputEvidenceStep(t, harness, "needs_input_off"))

	harness.settings.mutate(func(settings *ports.SlackRuntimeSettings) {
		settings.Categories.NeedsInput = true
		settings.Categories.Progress = false
	})
	harness.feed(ports.Event{
		Type: ports.PhaseStarted, FeatureID: "F-1", Phase: feature.PhaseImplement,
	})
	waitFor(t, 10*time.Second, func() bool {
		record, ok := readFeatureRecord(harness.stateDir, "F-1")
		return ok && len(record.Pending) == 1 && record.Pending[0].Tag == "#8" &&
			len(record.Pending[0].MessageTS) == 2
	})
	steps = append(steps, captureNeedsInputEvidenceStep(t, harness, "needs_input_reenabled"))

	writeNeedsInputEvidence(t, harness, steps, secondSecret)
}

func TestSlackNeedsInputRedaction(t *testing.T) {
	const secondSecret = "xoxp-NEEDS-INPUT-REDACTION-123456"
	item := ports.SlackPendingInput{
		Kind:      ports.SlackPendingQuestion,
		Header:    "Scope " + testToken,
		Question:  "Keep wording around " + secondSecret + " Authorization: Bearer header-secret?",
		RequestID: "ask-1",
		Options: []ports.SlackPendingInputOption{{
			Label: "Repository", Description: "Use https://user:pass@example.test/repo",
		}},
	}
	blocks, fallback := renderPendingInput(testToken, "#1", item, nil)
	raw, err := json.Marshal(struct {
		Blocks   []Block
		Fallback string
	}{blocks, fallback})
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, secret := range []string{testToken, secondSecret, "header-secret", "user:pass"} {
		if strings.Contains(text, secret) {
			t.Fatalf("rendered needs-input payload leaked %q: %s", secret, text)
		}
	}
	if !strings.Contains(text, "[REDACTED]") {
		t.Fatalf("rendered payload lacks redaction marker: %s", text)
	}
}

func TestSlackNeedsInputRestartKeepsTagsAndDoesNotRepost(t *testing.T) {
	harness := newNotifierHarness(t, defaultTestSettings(testToken, testRecipients()[1]))
	harness.seedFeature("F-1", nil)
	first := ports.SlackPendingInput{
		Kind: ports.SlackPendingPermission, FeatureID: "F-1", RequestID: "perm-1",
		ToolName: "Bash", Phase: "implement", RepoName: "agentic-orchestrator",
		Input: map[string]any{"command": "npm publish"},
	}
	harness.pending.set("F-1", first)
	notifier := harness.newNotifier(16)
	notifier.Start()
	notifier.RuntimeMessageTap(controlRuntimeMessage("F-1", "session-1", "perm-1"))
	waitFor(t, 10*time.Second, func() bool {
		record, ok := readFeatureRecord(harness.stateDir, "F-1")
		return ok && record.TagCounter == 1 && len(record.Pending) == 1 &&
			record.Pending[0].Tag == "#1" && len(record.Pending[0].MessageTS) == 1
	})
	notifier.Stop(context.Background())
	firstPosts := threadReplyCount(harness.server.AllRequests())

	second := first
	second.RequestID = "perm-2"
	second.Input = map[string]any{"command": "npm dist-tag add"}
	harness.pending.set("F-1", first, second)
	restarted := harness.newNotifier(16)
	restarted.Start()
	t.Cleanup(func() { restarted.Stop(context.Background()) })
	restarted.RuntimeMessageTap(controlRuntimeMessage("F-1", "session-2", "perm-2"))
	waitFor(t, 10*time.Second, func() bool {
		record, ok := readFeatureRecord(harness.stateDir, "F-1")
		return ok && record.TagCounter == 2 && len(record.Pending) == 2 &&
			record.Pending[0].Tag == "#1" && record.Pending[1].Tag == "#2" &&
			len(record.Pending[1].MessageTS) == 1
	})
	if got := threadReplyCount(harness.server.AllRequests()); got != firstPosts+1 {
		t.Fatalf("restart thread replies = %d; want %d with only #2 newly posted", got, firstPosts+1)
	}
}

func controlRuntimeMessage(featureID, sessionID, requestID string) session.SDKEventMsg {
	return session.SDKEventMsg{
		SessionID: sessionID,
		FeatureID: featureID,
		Phase:     feature.PhaseImplement,
		Message: llm.SDKMessage{Type: "control_request", ControlRequest: &llm.ControlRequestMessage{
			Type:      "control_request",
			RequestID: requestID,
			Request:   llm.ControlRequest{Subtype: "can_use_tool", ToolName: "Bash"},
		}},
	}
}

func threadReplyCount(requests []testsupport.Request) int {
	count := 0
	for _, request := range requests {
		if strings.HasSuffix(request.Path, "/chat.postMessage") &&
			fieldString(request, "thread_ts") != "" {
			count++
		}
	}
	return count
}

func pendingRecordReady(stateDir, featureID string, count, timestamps int) bool {
	record, ok := readFeatureRecord(stateDir, featureID)
	if !ok || len(record.Pending) != count {
		return false
	}
	for _, item := range record.Pending {
		if len(item.MessageTS) != timestamps {
			return false
		}
	}
	return true
}

func readFeatureRecord(stateDir, featureID string) (featureRecord, bool) {
	data, err := os.ReadFile(recordPath(stateDir, featureID))
	if err != nil {
		return featureRecord{}, false
	}
	var record featureRecord
	if yaml.Unmarshal(data, &record) != nil {
		return featureRecord{}, false
	}
	return record, true
}

type needsInputEvidenceStep struct {
	Name             string                      `json:"name"`
	RequestCount     int                         `json:"request_count"`
	ThreadReplyCount int                         `json:"thread_reply_count"`
	TagCounter       int                         `json:"tag_counter"`
	Pending          []needsInputEvidencePending `json:"pending"`
}

type needsInputEvidencePending struct {
	Identity          string            `json:"identity"`
	SourceFeatureID   string            `json:"source_feature_id"`
	Kind              string            `json:"kind"`
	RequestID         string            `json:"request_id,omitempty"`
	QuestionIndex     int               `json:"question_index,omitempty"`
	Tag               string            `json:"tag,omitempty"`
	MessageTimestamps map[string]string `json:"message_timestamps,omitempty"`
}

type needsInputEvidenceRequest struct {
	Method            string `json:"method"`
	Channel           string `json:"channel,omitempty"`
	ThreadTimestamp   string `json:"thread_timestamp,omitempty"`
	ReplyBroadcast    any    `json:"reply_broadcast,omitempty"`
	FallbackText      string `json:"fallback_text,omitempty"`
	Blocks            any    `json:"blocks,omitempty"`
	ReturnedTimestamp string `json:"returned_timestamp,omitempty"`
}

func captureNeedsInputEvidenceStep(
	t *testing.T,
	harness *notifierHarness,
	name string,
) needsInputEvidenceStep {
	t.Helper()
	record, ok := readFeatureRecord(harness.stateDir, "F-1")
	if !ok {
		t.Fatalf("read Slack record after %s", name)
	}
	requests := harness.server.AllRequests()
	pending := make([]needsInputEvidencePending, 0, len(record.Pending))
	for _, item := range record.Pending {
		pending = append(pending, needsInputEvidencePending{
			Identity:          item.Identity,
			SourceFeatureID:   item.SourceFeatureID,
			Kind:              item.Kind,
			RequestID:         item.RequestID,
			QuestionIndex:     item.QuestionIndex,
			Tag:               item.Tag,
			MessageTimestamps: item.MessageTS,
		})
	}
	return needsInputEvidenceStep{
		Name:             name,
		RequestCount:     len(requests),
		ThreadReplyCount: threadReplyCount(requests),
		TagCounter:       record.TagCounter,
		Pending:          pending,
	}
}

func needsInputEvidenceRequests(requests []testsupport.Request) []needsInputEvidenceRequest {
	result := make([]needsInputEvidenceRequest, 0, len(requests))
	for _, request := range requests {
		result = append(result, needsInputEvidenceRequest{
			Method:            strings.TrimPrefix(request.Path, "/api/"),
			Channel:           fieldString(request, "channel"),
			ThreadTimestamp:   fieldString(request, "thread_ts"),
			ReplyBroadcast:    request.Fields["reply_broadcast"],
			FallbackText:      fieldString(request, "text"),
			Blocks:            request.Fields["blocks"],
			ReturnedTimestamp: request.ReturnedTS,
		})
	}
	return result
}

func writeNeedsInputEvidence(
	t *testing.T,
	harness *notifierHarness,
	steps []needsInputEvidenceStep,
	secondSecret string,
) {
	t.Helper()
	dir := os.Getenv("AGENTICO_EVIDENCE_DIR")
	if dir == "" {
		return
	}
	record, ok := readFeatureRecord(harness.stateDir, "F-1")
	if !ok {
		t.Fatal("read final Slack record for evidence")
	}
	payload := struct {
		Requests []needsInputEvidenceRequest `json:"requests"`
		Steps    []needsInputEvidenceStep    `json:"steps"`
		Events   []observe.Event             `json:"events"`
		Record   featureRecord               `json:"record"`
	}{
		Requests: needsInputEvidenceRequests(harness.server.AllRequests()),
		Steps:    steps,
		Events:   harness.observer.all(),
		Record:   record,
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{testToken, secondSecret} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("evidence transcript leaked %q", secret)
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "slack-needs-input-transcript.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}
