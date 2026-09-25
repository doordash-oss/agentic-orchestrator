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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/slack/testsupport"
)

type restartEvidenceTranscript struct {
	Requests       []responderEvidenceRequest    `json:"requests"`
	Submissions    []responderEvidenceSubmission `json:"submissions"`
	Before         *featureRecord                `json:"before_restart"`
	After          *featureRecord                `json:"after_restart"`
	Ticks          []restartEvidenceTick         `json:"ticks"`
	EchoedClosures []string                      `json:"echoed_closure_timestamps"`
	Events         []observe.Event               `json:"events"`
	ClockOmissions string                        `json:"clock_omissions"`
	EventOrder     string                        `json:"event_presentation"`
}

type restartEvidenceTick struct {
	Name           string `json:"name"`
	Polls          int    `json:"polls"`
	Submissions    int    `json:"submissions"`
	LivePending    int    `json:"live_pending"`
	TrackedPending int    `json:"tracked_pending"`
}

func TestSlackRestartEvidence(t *testing.T) {
	var captures [2][]byte
	for i := range captures {
		t.Run(fmt.Sprintf("capture_%d", i+1), func(t *testing.T) {
			captures[i] = runSlackRestartEvidence(t)
		})
	}
	if !bytes.Equal(captures[0], captures[1]) {
		index := 0
		for index < len(captures[0]) && index < len(captures[1]) &&
			captures[0][index] == captures[1][index] {
			index++
		}
		start := max(0, index-60)
		t.Fatalf("restart transcript differs at byte %d: first=%q second=%q",
			index, captures[0][start:min(len(captures[0]), index+120)],
			captures[1][start:min(len(captures[1]), index+120)])
	}
	if dir := strings.TrimSpace(os.Getenv("AGENTICO_EVIDENCE_DIR")); dir != "" {
		path := filepath.Join(dir, "behaviors", "slack-restart.json")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, captures[0], 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func runSlackRestartEvidence(t *testing.T) []byte {
	t.Helper()
	const (
		id         = "F-restart-evidence"
		token      = "xoxp-restart-evidence-sentinel"
		other      = "xoxb-restart-second-secret"
		reviewID   = "review-1"
		revision   = "sha256:review-evidence"
		replyText  = " APPROVE!!! "
		replyTS    = "1758499200.000090"
		userKey    = "user:U-ADA"
		channelKey = "channel:C-ENG"
	)
	h := newNotifierHarness(t, defaultTestSettings(token, testRecipients()...))
	h.seedFeature(id, nil)
	h.server.SetOwnUserID("U-ADA")
	h.server.SetDefault(restartEvidenceSlackAPI(token, other))
	h.server.SetRequestOrder([]testsupport.OrderedRequest{
		{Method: "chat.update", Channel: "D-U-ADA", TextPrefix: "Slack pane polish — Implementing — Waiting on you: #1"},
		{Method: "chat.update", Channel: "C-ENG", TextPrefix: "Slack pane polish — Implementing — Waiting on you: #1"},
		{Method: "chat.postMessage", Channel: "D-U-ADA", TextPrefix: "#1 is no longer pending."},
		{Method: "chat.postMessage", Channel: "C-ENG", TextPrefix: "#3 was resolved in Agentico."},
		{Method: "chat.update", Channel: "D-U-ADA", TextPrefix: "Slack pane polish — Implementing — Waiting on you: #2"},
		{Method: "chat.postMessage", Channel: "C-ENG", TextPrefix: "#1 is no longer pending."},
		{Method: "chat.update", Channel: "C-ENG", TextPrefix: "Slack pane polish — Implementing — Waiting on you: #2"},
		{Method: "chat.postMessage", Channel: "C-OPS", TextPrefix: "Slack pane polish"},
		{Method: "chat.postMessage", Channel: "C-OPS", TextPrefix: "#2 Review:"},
		{Method: "chat.update", Channel: "C-OPS", TextPrefix: "Slack pane polish"},
		{Method: "reactions.add", Channel: "C-ENG"},
		{Method: "chat.postMessage", Channel: "D-U-ADA", TextPrefix: "#2 was approved"},
		{Method: "chat.postMessage", Channel: "C-ENG", TextPrefix: "#2 was approved"},
		{Method: "chat.postMessage", Channel: "C-OPS", TextPrefix: "#2 was approved"},
		{Method: "chat.update", Channel: "D-U-ADA", TextPrefix: "Slack pane polish — Implementing"},
		{Method: "chat.update", Channel: "C-ENG", TextPrefix: "Slack pane polish — Implementing"},
		{Method: "chat.update", Channel: "C-OPS", TextPrefix: "Slack pane polish — Implementing"},
	})
	artifact := []byte("# Phase plan\n\n## Tasks\n\n### Task 1: Verify restart\n")
	artifactPath := filepath.Join(t.TempDir(), "phase-plan.md")
	if err := os.WriteFile(artifactPath, artifact, 0o644); err != nil {
		t.Fatal(err)
	}
	keys := []string{userKey, channelKey}
	roots := []string{"1758499200.000001", "1758499200.000011"}
	channels := []string{"D-U-ADA", "C-ENG"}
	seeded := make(map[string][]testsupport.Message)
	record := &featureRecord{Version: recordVersion, TagCounter: 3,
		Destinations: map[string]destinationRecord{},
	}
	for i, key := range keys {
		root := roots[i]
		permissionTS := restartEvidenceTS(i, 2)
		reviewTS := restartEvidenceTS(i, 3)
		confirmationTS := restartEvidenceTS(i, 5)
		record.Destinations[key] = destinationRecord{
			Kind: strings.Split(key, ":")[0], SlackID: strings.Split(key, ":")[1],
			ChannelID: channels[i], RootTS: root, DisplayName: []string{"Ada", "#eng"}[i],
			Ledger: []string{root, restartEvidenceTS(i, 1), permissionTS, reviewTS,
				restartEvidenceTS(i, 4), confirmationTS},
			IntegrationReactions: []reactionLedgerEntry{
				{MessageTS: confirmationTS, Name: "white_check_mark"},
				{MessageTS: confirmationTS, Name: "question"},
			},
			PostingIndex: []postingIndexEntry{
				{Identity: "permission:permission-1", MessageTS: permissionTS, Tag: "#1"},
				{Identity: "review:" + reviewID + ":" + revision, MessageTS: reviewTS, Tag: "#2"},
			},
		}
		seeded[channels[i]] = []testsupport.Message{
			{TS: root, ThreadTS: root, User: "U-ADA", Text: "Root card"},
			{TS: restartEvidenceTS(i, 1), ThreadTS: root, User: "U-ADA", Text: "Progress"},
			{TS: permissionTS, ThreadTS: root, User: "U-ADA", Text: "#1 permission"},
			{TS: reviewTS, ThreadTS: root, User: "U-ADA", Text: "#2 review"},
			{TS: restartEvidenceTS(i, 4), ThreadTS: root, User: "U-ADA", Text: "Progress"},
			{TS: confirmationTS, ThreadTS: root, User: "U-ADA", Text: "Earlier confirmation",
				Reactions: []testsupport.Reaction{
					{Name: "white_check_mark", Count: 1, Users: []string{"U-ADA"}},
					{Name: "question", Count: 1, Users: []string{"U-ADA"}},
				}},
		}
		h.server.SeedThread(channels[i], root, seeded[channels[i]])
	}
	record.Pending = []pendingInputRecord{
		{Identity: "permission:permission-1", SourceFeatureID: id,
			Kind: string(ports.SlackPendingPermission), RequestID: "permission-1",
			Tag: "#1", MessageTS: map[string]string{
				userKey: restartEvidenceTS(0, 2), channelKey: restartEvidenceTS(1, 2),
			}},
		{Identity: "review:" + reviewID + ":" + revision, SourceFeatureID: id,
			Kind: string(ports.SlackPendingReview), ReviewID: reviewID,
			ReviewMode: "plan", TargetPhase: "implement", SourceRevision: revision,
			ArtifactID: "plan-artifact", RunNumber: 1, Tag: "#2",
			MessageTS: map[string]string{
				userKey: restartEvidenceTS(0, 3), channelKey: restartEvidenceTS(1, 3),
			}},
	}
	if err := persistFeatureRecord(h.stateDir, id, record); err != nil {
		t.Fatal(err)
	}
	h.pending.set(id, ports.SlackPendingInput{
		Kind: ports.SlackPendingPermission, FeatureID: id, RequestID: "permission-1",
	}, ports.SlackPendingInput{
		Kind: ports.SlackPendingReview, FeatureID: id, ReviewID: reviewID,
		ReviewMode: "plan", TargetPhase: "implement", SourceRevision: revision,
		ArtifactID: "plan-artifact", ArtifactPath: artifactPath,
		ArtifactFilename: "phase-plan.md", ArtifactBytes: artifact,
		ArtifactSize: int64(len(artifact)), RunNumber: 1,
	})
	// The first notifier is stopped while the permission is still live.
	first := h.newNotifier(16)
	first.Start()
	first.SetServerName("Restart evidence")
	first.SignalReady()
	waitFor(t, 5*time.Second, func() bool { return first.startupDone.Load() })
	first.Stop(context.Background())

	owed, err := loadFeatureRecord(h.stateDir, id)
	if err != nil {
		t.Fatal(err)
	}
	owed.Resolved = []pendingInputRecord{{
		Identity: "help:earlier", SourceFeatureID: id, Kind: string(ports.SlackPendingHelp),
		Tag: "#3", MessageTS: map[string]string{
			userKey: restartEvidenceTS(0, 4), channelKey: restartEvidenceTS(1, 4),
		},
		Resolution: &postingResolution{Kind: resolutionAgentico, ClosureAcknowledged: map[string]bool{userKey: true}},
	}}
	if err := persistFeatureRecord(h.stateDir, id, owed); err != nil {
		t.Fatal(err)
	}
	before, err := loadFeatureRecord(h.stateDir, id)
	if err != nil {
		t.Fatal(err)
	}
	h.pending.set(id, ports.SlackPendingInput{
		Kind: ports.SlackPendingReview, FeatureID: id, ReviewID: reviewID,
		ReviewMode: "plan", TargetPhase: "implement", SourceRevision: revision,
		ArtifactID: "plan-artifact", ArtifactPath: artifactPath,
		ArtifactFilename: "phase-plan.md", ArtifactBytes: artifact,
		ArtifactSize: int64(len(artifact)), RunNumber: 1,
	})
	// Seeded text is never copied into the transcript; the fake serves it only to polling.
	h.server.SeedThread("C-ENG", roots[1], append(
		seeded["C-ENG"], testsupport.Message{
			TS: replyTS, ThreadTS: roots[1], User: "U-GRACE", Text: replyText,
		},
	))
	h.server.EchoPostedMessages(true)
	answer := &responderEvidenceAnswerPort{}
	clock := newManualResponderClock()
	second := NewNotifier(NotifierOptions{
		Settings: h.settings, Store: h.store, StateDir: h.stateDir,
		Observer: h.observer, Pending: h.pending, Answer: answer,
		Clock: h.clock, ResponderClock: clock, QueueCapacity: 16,
		NewClient: func(token string) (slackClient, error) {
			return NewClient(token, WithBaseURL(h.server.URL()))
		},
	})
	second.Start()
	t.Cleanup(func() { second.Stop(context.Background()) })
	second.SetServerName("Restart evidence")
	second.SignalReady()
	waitFor(t, 5*time.Second, func() bool {
		return second.startupDone.Load()
	})
	if permission, owed := restartEvidencePostCount(h.server, "#1 is no longer pending."),
		restartEvidencePostCount(h.server, "#3 was resolved in Agentico."); permission != 2 || owed != 1 {
		t.Fatalf("startup closures: permission=%d owed=%d requests=%+v",
			permission, owed, responderEvidenceRequests(h.server.AllRequests()))
	}
	echoedClosures := restartEvidenceEchoedClosures(t, h.server, channels, roots)
	ticks := []restartEvidenceTick{restartEvidenceSnapshot(t, h, answer, "startup")}
	h.settings.mutate(func(s *ports.SlackRuntimeSettings) {
		s.Recipients = append(s.Recipients, ports.SlackRecipient{
			Kind: ports.SlackRecipientChannel, ID: "C-OPS", TypedText: "#ops", DisplayName: "#ops",
		})
	})
	h.server.Script("files.completeUploadExternal", testsupport.Response{Body: map[string]any{
		"ok": true, "files": []any{map[string]any{
			"id": "F00000001", "shares": map[string]any{"public": map[string]any{
				"C-OPS": []any{map[string]any{"ts": "1758499200.000120"}},
			}},
		}},
	}})
	second.sweepDestinations()
	waitFor(t, 5*time.Second, func() bool {
		current, err := loadFeatureRecord(h.stateDir, id)
		return err == nil && current.Destinations["channel:C-OPS"].RootTS != "" &&
			len(current.Pending) == 1 &&
			current.Pending[0].MessageTS["channel:C-OPS"] != "" &&
			h.server.OrderedRequestsRemaining() == 7
	})
	current, err := loadFeatureRecord(h.stateDir, id)
	if err != nil {
		t.Fatal(err)
	}
	ops := current.Destinations["channel:C-OPS"]
	h.server.SeedThread("C-OPS", ops.RootTS, []testsupport.Message{
		{TS: ops.RootTS, ThreadTS: ops.RootTS, User: "U-ADA", Text: "Current card"},
		{TS: current.Pending[0].MessageTS["channel:C-OPS"], ThreadTS: ops.RootTS,
			User: "U-ADA", Subtype: "file_share", Text: "#2 review"},
	})
	if len(answer.all()) != 0 || len(current.Pending) != 1 ||
		restartEvidenceRootCount(h.server) != 1 ||
		h.server.CallCount("files.getUploadURLExternal") != 1 {
		t.Fatalf("bootstrap must happen while review is pending: pending=%+v submissions=%+v",
			current.Pending, answer.all())
	}
	ticks = append(ticks, restartEvidenceSnapshot(t, h, answer, "bootstrapped_pending"))
	clock.tick(t)
	waitFor(t, 5*time.Second, func() bool {
		return len(answer.all()) == 1 && h.server.CallCount("reactions.add") == 1 &&
			restartEvidencePostCount(h.server, "#2 was approved by <@U-GRACE> via Slack.") == 3 &&
			len(h.observer.ofKind("slack.answer_received")) == 1
	})
	if submission := answer.all()[0]; submission.ReviewID != reviewID ||
		submission.SourceRevision != revision || submission.Outcome != ports.SlackAnswerAccepted {
		t.Fatalf("downtime reply submission = %+v", submission)
	}
	polls := h.server.CallCount("conversations.replies")
	if polls != 3 || len(h.observer.ofKind("slack.answer_rejected")) != 0 {
		t.Fatalf("first poll: polls=%d rejected=%+v", polls, h.observer.ofKind("slack.answer_rejected"))
	}
	restartEvidenceTickDone(t, clock)
	ticks = append(ticks, restartEvidenceSnapshot(t, h, answer, "active_downtime_reply_accepted"))
	h.pending.set(id)
	if live, err := h.pending.PendingSlackInputs(id); err != nil || len(live) != 0 {
		t.Fatalf("accepted gate still live: pending=%+v error=%v", live, err)
	}
	second.DomainEventTap(ports.Event{Type: ports.ReviewRequired, FeatureID: id})
	waitFor(t, 5*time.Second, func() bool {
		current, err := loadFeatureRecord(h.stateDir, id)
		return err == nil && len(current.Pending) == 0 &&
			restartEvidenceCardCount(h.server, "Slack pane polish — Implementing") >= 3
	})
	for _, channel := range []string{"D-U-ADA", "C-ENG", "C-OPS"} {
		restartEvidenceAssertCurrentCard(t, h.server, channel)
	}
	if got := restartEvidenceCardCount(h.server, "Slack pane polish — Implementing"); got != 3 {
		t.Fatalf("post-acceptance card refreshes = %d; want one per destination", got)
	}
	ticks = append(ticks, restartEvidenceSnapshot(t, h, answer, "post_acceptance_reconciled"))
	clock.tick(t)
	restartEvidenceTickDone(t, clock)
	if got := h.server.CallCount("conversations.replies"); got != polls {
		t.Fatalf("polled after resolution: %d; want %d", got, polls)
	}
	ticks = append(ticks, restartEvidenceSnapshot(t, h, answer, "resolved_idle"))
	clock.tick(t)
	restartEvidenceTickDone(t, clock)
	if got := h.server.CallCount("conversations.replies"); got != polls {
		t.Fatalf("polled on second idle tick: %d; want %d", got, polls)
	}
	if len(answer.all()) != 1 || len(h.observer.ofKind("slack.answer_received")) != 1 ||
		len(h.observer.ofKind("slack.answer_rejected")) != 0 ||
		h.server.CallCount("reactions.add") != 1 {
		t.Fatal("idle ticks judged an echo or the downtime reply twice")
	}
	ticks = append(ticks, restartEvidenceSnapshot(t, h, answer, "idle_repeat"))
	after, err := loadFeatureRecord(h.stateDir, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Pending) != 0 || restartEvidenceHasOwedClosure(after, channelKey) ||
		restartEvidenceRootCount(h.server) != 1 ||
		h.server.CallCount("files.getUploadURLExternal") != 1 {
		t.Fatalf("restart state: pending=%+v resolved=%+v root posts=%d",
			after.Pending, after.Resolved, restartEvidenceRootCount(h.server))
	}
	if restartEvidencePostCount(h.server, "#1 is no longer pending.") != 2 ||
		restartEvidencePostCount(h.server, "#3 was resolved in Agentico.") != 1 ||
		restartEvidencePostCount(h.server, "#2 was resolved in Agentico.") != 0 ||
		len(answer.all()) != 1 || len(h.observer.ofKind("slack.answer_received")) != 1 {
		t.Fatal("restart duplicated a closure or submitted the downtime reply twice")
	}
	if remaining := h.server.OrderedRequestsRemaining(); remaining != 0 {
		t.Fatalf("request arrival schedule left %d writes unseen", remaining)
	}
	transcript := restartEvidenceTranscript{
		Requests:    responderEvidenceRequests(h.server.AllRequests()),
		Submissions: answer.all(), Before: before, After: after, Ticks: ticks,
		EchoedClosures: echoedClosures, Events: h.observer.all(),
		ClockOmissions: "event timestamps and record write times omitted; Block Kit update epochs normalized",
		EventOrder:     "events grouped by payload; requests retain actual arrival order",
	}
	encoded := restartEvidenceJSON(t, transcript)
	for _, forbidden := range []string{token, other, replyText} {
		if bytes.Contains(encoded, []byte(forbidden)) {
			t.Fatalf("restart transcript retained sensitive/seeded reply text %q", forbidden)
		}
	}
	return encoded
}

func restartEvidenceHasOwedClosure(record *featureRecord, destination string) bool {
	for _, resolved := range record.Resolved {
		if resolved.closureOwed(destination) {
			return true
		}
	}
	return false
}

func restartEvidenceCardCount(server *testsupport.Server, prefix string) int {
	count := 0
	for _, request := range server.Requests("chat.update") {
		if strings.HasPrefix(fieldString(request, "text"), prefix) &&
			!strings.Contains(fieldString(request, "text"), "Waiting on you:") {
			count++
		}
	}
	return count
}

func restartEvidenceAssertCurrentCard(t *testing.T, server *testsupport.Server, channel string) {
	t.Helper()
	var last string
	for _, request := range server.Requests("chat.update") {
		if fieldString(request, "channel") == channel {
			last = fieldString(request, "text")
		}
	}
	if !strings.HasPrefix(last, "Slack pane polish — Implementing") ||
		strings.Contains(last, "Waiting on you:") {
		t.Fatalf("%s final card still asks for review: %q", channel, last)
	}
}

var restartEvidenceDate = regexp.MustCompile(`<!date\^[0-9]+`)

func restartEvidenceJSON(t *testing.T, transcript restartEvidenceTranscript) []byte {
	t.Helper()
	raw, err := json.Marshal(transcript)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	restartEvidenceOmitClockFields(document)
	events, _ := document["events"].([]any)
	for _, value := range events {
		delete(value.(map[string]any), "timestamp")
	}
	// The request list stays in actual arrival order; events have no request
	// index, so present equal-kind emissions in a stable order.
	sort.SliceStable(events, func(i, j int) bool {
		left, _ := json.Marshal(events[i])
		right, _ := json.Marshal(events[j])
		return bytes.Compare(left, right) < 0
	})
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(document); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func restartEvidenceOmitClockFields(value any) {
	switch node := value.(type) {
	case map[string]any:
		delete(node, "PostedAt")
		delete(node, "ResolvedAt")
		for key, child := range node {
			if key == "text" {
				if text, ok := child.(string); ok {
					node[key] = restartEvidenceDate.ReplaceAllString(text, "<!date^0")
				}
				continue
			}
			restartEvidenceOmitClockFields(child)
		}
	case []any:
		for _, child := range node {
			restartEvidenceOmitClockFields(child)
		}
	}
}

func restartEvidenceEchoedClosures(
	t *testing.T, server *testsupport.Server, channels, roots []string,
) []string {
	t.Helper()
	var echoed []string
	for i, channel := range channels {
		messages := server.ThreadMessages(channel, roots[i])
		for _, request := range server.Requests("chat.postMessage") {
			if fieldString(request, "channel") != channel {
				continue
			}
			text := fieldString(request, "text")
			if text != "#1 is no longer pending." && text != "#3 was resolved in Agentico." {
				continue
			}
			found := false
			for _, message := range messages {
				if message.TS == request.ReturnedTS && message.User == "U-ADA" &&
					message.Text == text {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("posted closure %q at %s absent from %s replies",
					text, request.ReturnedTS, channel)
			}
			echoed = append(echoed, request.ReturnedTS)
		}
	}
	if len(echoed) != 3 {
		t.Fatalf("echoed closures = %v; want three", echoed)
	}
	return echoed
}

func restartEvidenceTickDone(t *testing.T, clock *manualResponderClock) {
	t.Helper()
	waitFor(t, 5*time.Second, func() bool { return len(clock.sleeps) == 1 })
}

func restartEvidenceSnapshot(
	t *testing.T, h *notifierHarness, answer *responderEvidenceAnswerPort, name string,
) restartEvidenceTick {
	t.Helper()
	record, err := loadFeatureRecord(h.stateDir, "F-restart-evidence")
	if err != nil {
		t.Fatal(err)
	}
	live, err := h.pending.PendingSlackInputs("F-restart-evidence")
	if err != nil {
		t.Fatal(err)
	}
	return restartEvidenceTick{
		Name: name, Polls: h.server.CallCount("conversations.replies"),
		Submissions: len(answer.all()), LivePending: len(live),
		TrackedPending: len(record.Pending),
	}
}

func restartEvidenceTS(destination, index int) string {
	return "1758499200." + []string{
		[]string{"000001", "000002", "000003", "000004", "000005", "000006"}[index],
		[]string{"000011", "000012", "000013", "000014", "000015", "000016"}[index],
	}[destination]
}

func restartEvidenceSlackAPI(token, other string) func(string, testsupport.Request) testsupport.Response {
	var sequence atomic.Int64
	return func(method string, request testsupport.Request) testsupport.Response {
		switch method {
		case "users.info":
			return responderEvidenceUserInfo("U-GRACE", "Grace Hopper "+token+" "+other)
		case "conversations.open":
			return testsupport.Response{Body: map[string]any{
				"ok": true, "channel": map[string]any{"id": "D-U-ADA"},
			}}
		case "chat.postMessage", "chat.update":
			return testsupport.Response{Body: map[string]any{
				"ok": true, "channel": fieldString(request, "channel"),
				"ts": restartEvidenceResponseTS(sequence.Add(1)),
			}}
		default:
			return testsupport.Response{Body: map[string]any{"ok": false, "error": "unexpected " + method}}
		}
	}
}

func restartEvidenceResponseTS(index int64) string {
	return fmt.Sprintf("1758499200.%06d", index+100)
}

func restartEvidencePostCount(server *testsupport.Server, text string) int {
	count := 0
	for _, request := range server.Requests("chat.postMessage") {
		if fieldString(request, "text") == text {
			count++
		}
	}
	return count
}

func restartEvidenceRootCount(server *testsupport.Server) int {
	count := 0
	for _, request := range server.Requests("chat.postMessage") {
		if fieldString(request, "thread_ts") == "" {
			count++
		}
	}
	return count
}
