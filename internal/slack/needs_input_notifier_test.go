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
	settings := defaultTestSettings(testToken, testRecipients()...)
	harness := newNotifierHarness(t, settings)
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
			testToken + " --registry-token " + secondSecret + " && " + strings.Repeat("echo release-ready; ", 220)},
	}
	parentItems := []ports.SlackPendingInput{
		permission,
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
		{
			Kind: ports.SlackPendingQuestion, FeatureID: "F-1", RequestID: "ask-2",
			QuestionIndex: 0, QuestionCount: 1, Header: "Release note",
			Question: "What wording should appear in the release note?",
		},
		{
			Kind: ports.SlackPendingGate, FeatureID: "F-1",
			GatePath: "/state/F-1/iteration-02/need-user-input.yaml", Iteration: 2,
			WaitingSince: time.Date(2026, 9, 22, 18, 5, 0, 0, time.UTC),
			GateSummary:  "Two checks require an authenticated Okta session.",
			GateQuestions: []string{
				"Waive the blocked checks or retry after signing in?",
			},
			GateBlockers: []ports.SlackPendingInputBlocker{
				{Name: "Deployment smoke test", RepoName: "agentic-orchestrator", Command: "agentico verify deploy", Reason: "Okta session expired", Remediation: "Sign in to Okta and retry."},
				{Name: "Production API probe", RepoName: "agentic-orchestrator", Command: "agentico verify api", Reason: "Missing production credentials", Remediation: "Refresh credentials and retry."},
			},
		},
		{
			Kind: ports.SlackPendingHelp, FeatureID: "F-1",
			HelpQuestion: "Which release owner should review the final output?",
			WaitingSince: time.Date(2026, 9, 22, 18, 6, 0, 0, time.UTC),
		},
	}
	harness.pending.set("F-1", parentItems...)
	notifier := harness.start(32)

	notifier.RuntimeMessageTap(controlRuntimeMessage("F-1", "session-1", "perm-1"))
	waitFor(t, 10*time.Second, func() bool {
		return threadReplyCount(harness.server.AllRequests()) == 12 &&
			pendingRecordReady(harness.stateDir, "F-1", 6, 2) &&
			harness.server.CallCount("chat.update") >= 2
	})

	requestCount := len(harness.server.AllRequests())
	notifier.RuntimeMessageTap(controlRuntimeMessage("F-1", "session-1", "perm-1"))
	waitFor(t, 10*time.Second, func() bool { return notifier.queue.len() == 0 })
	if got := len(harness.server.AllRequests()); got != requestCount {
		t.Fatalf("duplicate control request calls = %d; want %d", got, requestCount)
	}

	childPermission := permission
	childPermission.FeatureID = "F-child"
	childPermission.RequestID = "perm-child"
	childPermission.Input = map[string]any{"command": "go test ./internal/slack -run TestChild"}
	harness.pending.set("F-child", childPermission)
	notifier.RuntimeMessageTap(controlRuntimeMessage("F-child", "child-session", "perm-child"))
	waitFor(t, 10*time.Second, func() bool {
		record, ok := readFeatureRecord(harness.stateDir, "F-1")
		return ok && len(record.Pending) == 7 && record.Pending[6].Tag == "#7" &&
			len(record.Pending[6].MessageTS) == 2 && threadReplyCount(harness.server.AllRequests()) == 14
	})

	harness.pending.set("F-1")
	harness.pending.set("F-child")
	notifier.RuntimeMessageTap(session.SDKEventMsg{
		SessionID: "session-1", FeatureID: "F-1", Phase: feature.PhaseImplement,
		Message: llm.SDKMessage{Type: "assistant"},
	})
	waitFor(t, 10*time.Second, func() bool {
		record, ok := readFeatureRecord(harness.stateDir, "F-1")
		return ok && len(record.Pending) == 0
	})
	afterRetirementThreads := threadReplyCount(harness.server.AllRequests())

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
	if got := threadReplyCount(harness.server.AllRequests()); got != afterRetirementThreads {
		t.Fatalf("Needs input off replies = %d; want %d", got, afterRetirementThreads)
	}

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

	writeNeedsInputEvidence(t, harness, secondSecret)
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

func writeNeedsInputEvidence(t *testing.T, harness *notifierHarness, secondSecret string) {
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
		Requests []testsupport.Request `json:"requests"`
		Events   []observe.Event       `json:"events"`
		Record   featureRecord         `json:"record"`
	}{
		Requests: harness.server.AllRequests(),
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
