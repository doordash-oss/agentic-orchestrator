package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/slack/testsupport"
)

type restartEvidenceTranscript struct {
	Requests    []responderEvidenceRequest    `json:"requests"`
	Submissions []responderEvidenceSubmission `json:"submissions"`
	Before      *featureRecord                `json:"before_restart"`
	After       *featureRecord                `json:"after_restart"`
	Events      []observe.Event               `json:"events"`
}

func TestSlackRestartEvidence(t *testing.T) {
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
	artifact := []byte("# Phase plan\n\n## Tasks\n\n### Task 1: Verify restart\n")
	artifactPath := filepath.Join(t.TempDir(), "phase-plan.md")
	if err := os.WriteFile(artifactPath, artifact, 0o644); err != nil {
		t.Fatal(err)
	}
	keys := []string{userKey, channelKey}
	roots := []string{"1758499200.000001", "1758499200.000011"}
	channels := []string{"D-U-ADA", "C-ENG"}
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
		h.server.SeedThread(channels[i], root, []testsupport.Message{
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
		})
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
		[]testsupport.Message{
			{TS: roots[1], ThreadTS: roots[1], User: "U-ADA"},
			{TS: restartEvidenceTS(1, 2), ThreadTS: roots[1], User: "U-ADA", Text: "#1 permission"},
			{TS: restartEvidenceTS(1, 3), ThreadTS: roots[1], User: "U-ADA", Text: "#2 review"},
			{TS: restartEvidenceTS(1, 5), ThreadTS: roots[1], User: "U-ADA", Text: "Earlier confirmation"},
		}, testsupport.Message{
			TS: replyTS, ThreadTS: roots[1], User: "U-GRACE", Text: replyText,
		},
	))
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
		return second.startupDone.Load() &&
			restartEvidencePostCount(h.server, "#1 is no longer pending.") == 2 &&
			restartEvidencePostCount(h.server, "#3 was resolved in Agentico.") == 1
	})
	clock.tick(t)
	waitFor(t, 5*time.Second, func() bool {
		return len(answer.all()) == 1 && h.server.CallCount("reactions.add") > 0 &&
			restartEvidencePostCount(h.server, "#2 was approved by <@U-GRACE> via Slack.") == 2 &&
			len(h.observer.ofKind("slack.answer_received")) == 1
	})
	if submission := answer.all()[0]; submission.ReviewID != reviewID ||
		submission.SourceRevision != revision || submission.Outcome != ports.SlackAnswerAccepted {
		t.Fatalf("downtime reply submission = %+v", submission)
	}
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
		return err == nil && current.Destinations["channel:C-OPS"].RootTS != ""
	})
	after, err := loadFeatureRecord(h.stateDir, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Pending) != 1 || after.Pending[0].Tag != "#2" ||
		after.Resolved[0].closureOwed(channelKey) ||
		restartEvidenceRootCount(h.server) != 1 ||
		h.server.CallCount("files.getUploadURLExternal") != 1 ||
		after.Pending[0].MessageTS["channel:C-OPS"] == "" {
		t.Fatalf("restart state: pending=%+v resolved=%+v root posts=%d",
			after.Pending, after.Resolved, restartEvidenceRootCount(h.server))
	}
	if restartEvidencePostCount(h.server, "#1 is no longer pending.") != 2 ||
		restartEvidencePostCount(h.server, "#3 was resolved in Agentico.") != 1 ||
		len(answer.all()) != 1 {
		t.Fatal("restart duplicated a closure or submitted the downtime reply twice")
	}
	transcript := restartEvidenceTranscript{
		Requests:    responderEvidenceRequests(h.server.AllRequests()),
		Submissions: answer.all(), Before: before, After: after, Events: h.observer.all(),
	}
	encoded, err := json.MarshalIndent(transcript, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{token, other, replyText} {
		if bytes.Contains(encoded, []byte(forbidden)) {
			t.Fatalf("restart transcript retained sensitive/seeded reply text %q", forbidden)
		}
	}
	if dir := strings.TrimSpace(os.Getenv("AGENTICO_EVIDENCE_DIR")); dir != "" {
		path := filepath.Join(dir, "behaviors", "slack-restart.json")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(encoded, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
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
