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
	"sync"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/slack/testsupport"
	"gopkg.in/yaml.v3"
)

type problemsEvidenceEntry struct {
	Method         string           `json:"method"`
	Channel        string           `json:"channel"`
	ThreadTS       string           `json:"thread_ts,omitempty"`
	ReplyBroadcast string           `json:"reply_broadcast,omitempty"`
	Text           string           `json:"text"`
	Blocks         []map[string]any `json:"blocks,omitempty"`
	ReturnedTS     string           `json:"returned_ts,omitempty"`
}

func TestSlackProblemsEvidence(t *testing.T) {
	const secondSecret = "xoxp-PROBLEMS-EVIDENCE-987654321"

	harness := newNotifierHarness(t, defaultTestSettings(testToken, testRecipients()...))
	harness.seedFeature("F-1", func(f *feature.Feature) {
		withRoadmap(f)
		f.ActiveRun = 2
		f.RunCount = 2
		f.CurrentPhase = feature.PhasePublish
		f.Status = feature.StatusCodeReady
		f.RepoStates = map[string]*feature.RepoState{
			"alpha": {Touched: true},
			"beta":  {Touched: true},
		}
	})

	var (
		transcriptMu sync.Mutex
		transcript   []problemsEvidenceEntry
	)
	inner, _ := defaultOKResponder()
	harness.server.SetDefault(func(method string, request testsupport.Request) testsupport.Response {
		response := inner(method, request)
		channel := fieldString(request, "channel")
		if channel == "" {
			channel = fieldString(request, "users")
		}
		returnedTS := ""
		if body, ok := response.Body.(map[string]any); ok {
			returnedTS, _ = body["ts"].(string)
		}
		transcriptMu.Lock()
		transcript = append(transcript, problemsEvidenceEntry{
			Method:         method,
			Channel:        channel,
			ThreadTS:       fieldString(request, "thread_ts"),
			ReplyBroadcast: fieldString(request, "reply_broadcast"),
			Text:           fieldString(request, "text"),
			Blocks:         requestBlocks(request),
			ReturnedTS:     returnedTS,
		})
		transcriptMu.Unlock()
		return response
	})

	notifier := harness.start(0)
	harness.feed(ports.Event{Type: ports.FeatureStarted, FeatureID: "F-1"})
	waitForProblemsEvidencePosts(t, harness, 1)
	waitFor(t, 10*time.Second, func() bool {
		return len(harness.server.Requests("chat.update")) >= 2
	})

	setupRecord := &errcat.FailureRecord{
		Code: errcat.WorktreeSetupFailed,
		Context: &errcat.RecordContext{
			Repositories: []errcat.CodeRepository{{Name: "alpha", Branch: "feature/slack"}},
			SetupTask: &errcat.CodeSetupTask{
				Key: "worktree:alpha", Kind: "worktree", Label: "Worktree: alpha",
			},
		},
		Diagnostics: "git worktree add failed for repository alpha",
	}
	setupProblem := errcat.RenderRecord(*setupRecord)
	harness.feed(ports.Event{
		Type: ports.SetupFailed, FeatureID: "F-1",
		SetupTask: "worktree:alpha", RepoName: "alpha", CanonicalError: &setupProblem,
	})
	waitForProblemsEvidencePosts(t, harness, 2)

	publishRecord := &errcat.FailureRecord{
		Code: errcat.PublishRebaseConflict,
		Context: &errcat.RecordContext{Repositories: []errcat.CodeRepository{{
			Name: "alpha", Branch: "feature/slack", RebaseTarget: "main",
		}}},
		Diagnostics: "conflict in alpha while rebasing feature/slack onto main",
	}
	modifyProblemsEvidenceFeature(t, harness, "F-1", func(f *feature.Feature) {
		f.Status = feature.StatusCodeReady
		f.CurrentPhase = feature.PhasePublish
		f.RepoStates["alpha"].Error = publishRecord
	})
	publishProblem := errcat.RenderRecord(*publishRecord)
	updatesBefore := len(harness.server.Requests("chat.update"))
	harness.feed(ports.Event{
		Type: ports.PublishCompleted, FeatureID: "F-1", CanonicalError: &publishProblem,
	})
	waitForProblemsEvidencePosts(t, harness, 3)
	waitForProblemsEvidenceUpdateAll(t, harness, updatesBefore, "Needs your action: Pull-rebase conflict")

	modifyProblemsEvidenceFeature(t, harness, "F-1", func(f *feature.Feature) {
		f.Status = feature.StatusPublished
		f.RepoStates["alpha"].Error = nil
		f.RepoStates["alpha"].PRURL = "https://github.com/org/alpha/pull/42"
	})
	updatesBefore = len(harness.server.Requests("chat.update"))
	harness.feed(ports.Event{Type: ports.PublishCompleted, FeatureID: "F-1"})
	waitForProblemsEvidencePosts(t, harness, 4)
	waitForProblemsEvidenceUpdateAll(t, harness, updatesBefore, "*Status:* Published", "Pull requests", "alpha")

	modifyProblemsEvidenceFeature(t, harness, "F-1", func(f *feature.Feature) {
		f.Status = feature.StatusInterrupted
	})
	updatesBefore = len(harness.server.Requests("chat.update"))
	harness.feed(ports.Event{Type: ports.FeatureInterrupted, FeatureID: "F-1"})
	waitForProblemsEvidencePosts(t, harness, 5)
	waitForProblemsEvidenceUpdateAll(t, harness, updatesBefore, "*Status:* Interrupted")

	modifyProblemsEvidenceFeature(t, harness, "F-1", func(f *feature.Feature) {
		f.Status = feature.StatusPlanning
		f.CurrentPhase = feature.PhasePlan
		f.CurrentRoadmapPhase = 2
		f.TotalRoadmapPhases = 2
	})
	updatesBefore = len(harness.server.Requests("chat.update"))
	harness.feed(ports.Event{Type: ports.FeatureRewound, FeatureID: "F-1", Phase: feature.PhasePlan})
	waitForProblemsEvidencePosts(t, harness, 6)
	waitForProblemsEvidenceUpdateAll(t, harness, updatesBefore, "*Phase:* Plan", "*Status:* Planning")
	postRewind := []ports.Event{
		startedEvent("F-1", feature.PhaseResearch),
		completedEvent("F-1", feature.PhaseResearch),
		startedEvent("F-1", feature.PhaseDesign),
		completedEvent("F-1", feature.PhaseDesign),
		startedEvent("F-1", feature.PhaseKnowledgeBase),
		completedEvent("F-1", feature.PhaseKnowledgeBase),
		startedEvent("F-1", feature.PhaseInquire),
		completedEvent("F-1", feature.PhaseInquire),
		startedEvent("F-1", feature.PhasePlan),
		completedEvent("F-1", feature.PhasePlan),
	}
	for i, event := range postRewind[:5] {
		harness.feed(event)
		waitForProblemsEvidencePosts(t, harness, 7+i)
	}
	notifier.Stop(context.Background())
	notifier = harness.newNotifier(0)
	notifier.Start()
	t.Cleanup(func() { notifier.Stop(context.Background()) })
	harness.notifier = notifier
	for i, event := range postRewind[5:] {
		harness.feed(event)
		waitForProblemsEvidencePosts(t, harness, 12+i)
	}

	attentionRecord := &errcat.FailureRecord{
		Code: errcat.IntegrationMergeConflict,
		Context: &errcat.RecordContext{Repositories: []errcat.CodeRepository{{
			Name: "alpha", Branch: "feature/refactor", ConflictFiles: []string{"internal/slack/render.go"},
		}}},
		Diagnostics: "merge conflict in internal/slack/render.go",
	}
	harness.seedFeature("F-2", func(f *feature.Feature) {
		f.Parent = &feature.ChildRelationship{
			ParentID: "F-1",
			Kind:     feature.ChildKindRefactor,
			Transaction: &feature.TransactionJournal{
				Phase:     feature.TransactionPhaseAttention,
				Attention: attentionRecord,
			},
		}
	})
	attentionProblem := errcat.RenderRecord(*attentionRecord)
	updatesBefore = len(harness.server.Requests("chat.update"))
	harness.feed(ports.Event{
		Type:           ports.RelationshipIntegrationChanged,
		FeatureID:      "F-2",
		ParentID:       "F-1",
		ChildID:        "F-2",
		CanonicalError: &attentionProblem,
	})
	waitForProblemsEvidencePosts(t, harness, 17)
	waitForProblemsEvidenceUpdateAll(t, harness, updatesBefore, "Needs your action: Integration merge conflict")
	if recordExists(harness.stateDir, "F-2") {
		t.Fatal("refactor child received its own Slack record")
	}

	modifyProblemsEvidenceFeature(t, harness, "F-2", func(f *feature.Feature) {
		f.Parent.Transaction.Phase = feature.TransactionPhaseMerged
		f.Parent.Transaction.Attention = nil
	})
	warning := errcat.New(
		errcat.RebaseAlreadyUpToDate,
		errcat.WithRepositories(errcat.CodeRepository{Name: "alpha"}),
	)
	updatesBefore = len(harness.server.Requests("chat.update"))
	harness.feed(ports.Event{
		Type: ports.FeatureFailed, FeatureID: "F-1", CanonicalError: &warning,
	})
	waitForProblemsEvidenceUpdate(t, harness, updatesBefore, "*Status:* Planning")
	assertProblemsEvidencePostCount(t, harness, 17)

	updatesBefore = len(harness.server.Requests("chat.update"))
	harness.feed(ports.Event{
		Type: ports.PhaseCompleted, FeatureID: "F-1", Phase: feature.PhasePlan,
		Error: errString("phase failed before the terminal event"),
	})
	waitForProblemsEvidenceUpdate(t, harness, updatesBefore, "*Status:* Planning")
	assertProblemsEvidencePostCount(t, harness, 17)

	diagnostics := "repository alpha path /tmp/worktrees/alpha exit 17 configured=" +
		testToken + " echoed=" + secondSecret + "\n" +
		strings.Repeat("provider stack frame in /tmp/worktrees/alpha/internal/slack/notifier.go\n", 120)
	failureRecord := &errcat.FailureRecord{
		Code: errcat.SessionCrashed,
		Context: &errcat.RecordContext{
			Repositories: []errcat.CodeRepository{{Name: "alpha"}},
			Phase:        &errcat.CodePhase{Name: "implement", Iteration: 1},
			Command:      &errcat.CodeCommand{ExitCode: 17, LogPaths: []string{"/tmp/worktrees/alpha/session.log"}},
		},
		Diagnostics: diagnostics,
	}
	modifyProblemsEvidenceFeature(t, harness, "F-1", func(f *feature.Feature) {
		f.Status = feature.StatusFailed
		f.CurrentPhase = feature.PhaseImplement
		f.Run().Failure = failureRecord
	})
	blockingProblem := errcat.RenderRecord(*failureRecord)
	updatesBefore = len(harness.server.Requests("chat.update"))
	harness.feed(ports.Event{
		Type: ports.FeatureFailed, FeatureID: "F-1", CanonicalError: &blockingProblem,
	})
	waitForProblemsEvidencePosts(t, harness, 18)
	waitForProblemsEvidenceUpdateAll(t, harness, updatesBefore, "Failed: Session crashed")
	waitFor(t, 10*time.Second, func() bool {
		for _, channel := range []string{"D-U-ADA", "C-ENG"} {
			posts := postsTo(harness.server, channel)
			if len(posts) != 18 {
				return false
			}
			last, err := json.Marshal(posts[len(posts)-1].Fields)
			if err != nil ||
				!strings.Contains(string(last), "[REDACTED]") ||
				!strings.Contains(string(last), "repository alpha") ||
				!strings.Contains(string(last), "/tmp/worktrees/alpha") ||
				!strings.Contains(string(last), "Diagnostics were shortened") {
				return false
			}
		}
		return true
	})

	for _, key := range []string{
		destinationKey("user", "U-ADA"),
		destinationKey("channel", "C-ENG"),
	} {
		waitFor(t, 10*time.Second, func() bool {
			ledger, ok := recordLedger(harness.stateDir, "F-1", key)
			return ok && len(ledger) == 18
		})
	}
	notifier.Stop(context.Background())

	transcriptMu.Lock()
	entries := append([]problemsEvidenceEntry(nil), transcript...)
	transcriptMu.Unlock()
	record, err := os.ReadFile(recordPath(harness.stateDir, "F-1"))
	if err != nil {
		t.Fatalf("read final Slack record: %v", err)
	}
	assertProblemsEvidenceTranscript(t, entries, record, testToken, secondSecret)

	dir := strings.TrimSpace(os.Getenv("AGENTICO_EVIDENCE_DIR"))
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create evidence directory: %v", err)
	}
	encoded, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		t.Fatalf("encode Problems transcript: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "slack-problems-transcript.json"), encoded, 0o644); err != nil {
		t.Fatalf("write Problems transcript: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "slack-problems-record.yaml"), record, 0o644); err != nil {
		t.Fatalf("write Problems record: %v", err)
	}
}

func modifyProblemsEvidenceFeature(
	t *testing.T,
	harness *notifierHarness,
	featureID string,
	modify func(*feature.Feature),
) {
	t.Helper()
	if err := harness.store.Modify(featureID, func(f *feature.Feature) error {
		modify(f)
		return nil
	}); err != nil {
		t.Fatalf("modify evidence feature %s: %v", featureID, err)
	}
}

func waitForProblemsEvidencePosts(t *testing.T, harness *notifierHarness, want int) {
	t.Helper()
	waitFor(t, 10*time.Second, func() bool {
		return len(postsTo(harness.server, "D-U-ADA")) == want &&
			len(postsTo(harness.server, "C-ENG")) == want
	})
}

func assertProblemsEvidencePostCount(t *testing.T, harness *notifierHarness, want int) {
	t.Helper()
	time.Sleep(20 * time.Millisecond)
	for _, channel := range []string{"D-U-ADA", "C-ENG"} {
		if got := len(postsTo(harness.server, channel)); got != want {
			t.Fatalf("posts to %s = %d; want %d", channel, got, want)
		}
	}
}

func waitForProblemsEvidenceUpdate(
	t *testing.T,
	harness *notifierHarness,
	after int,
	want ...string,
) {
	t.Helper()
	waitFor(t, 10*time.Second, func() bool {
		updates := harness.server.Requests("chat.update")
		for _, update := range updates[after:] {
			encoded, err := json.Marshal(update.Fields)
			if err != nil {
				continue
			}
			text := string(encoded)
			matches := true
			for _, value := range want {
				if !strings.Contains(text, value) {
					matches = false
					break
				}
			}
			if matches {
				return true
			}
		}
		return false
	})
}

func waitForProblemsEvidenceUpdateAll(
	t *testing.T,
	harness *notifierHarness,
	after int,
	want ...string,
) {
	t.Helper()
	waitFor(t, 10*time.Second, func() bool {
		found := map[string]bool{"C-ENG": false, "D-U-ADA": false}
		updates := harness.server.Requests("chat.update")
		for _, update := range updates[after:] {
			encoded, err := json.Marshal(update.Fields)
			if err != nil {
				continue
			}
			text := string(encoded)
			matches := true
			for _, value := range want {
				if !strings.Contains(text, value) {
					matches = false
					break
				}
			}
			if matches {
				found[fieldString(update, "channel")] = true
			}
		}
		return found["C-ENG"] && found["D-U-ADA"]
	})
}

func assertProblemsEvidenceTranscript(
	t *testing.T,
	entries []problemsEvidenceEntry,
	record []byte,
	secrets ...string,
) {
	t.Helper()
	encoded, err := json.Marshal(entries)
	if err != nil {
		t.Fatalf("encode transcript for assertions: %v", err)
	}
	for _, secret := range secrets {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("Problems transcript leaked secret %q", secret)
		}
		if strings.Contains(string(record), secret) {
			t.Fatalf("Problems record leaked secret %q", secret)
		}
	}
	transcriptText := string(encoded)
	for _, want := range []string{
		"Published",
		"Worktree setup failed",
		"Interrupted",
		"Rewound to Plan for roadmap phase 2 in run 2",
		"Research started",
		"Knowledge Base completed",
		"Plan completed",
		"Refactor: Integration merge conflict",
		"Failed: Session crashed",
	} {
		if !strings.Contains(transcriptText, want) {
			t.Fatalf("Problems transcript missing %q", want)
		}
	}
	for _, suppressed := range []string{"Already up to date", "phase failed before the terminal event"} {
		if strings.Contains(transcriptText, suppressed) {
			t.Fatalf("Problems transcript contains suppressed text %q", suppressed)
		}
	}

	roots := map[string]string{}
	postTimestamps := map[string][]string{}
	problemReplies := map[string]int{}
	for _, entry := range entries {
		if entry.Method != "chat.postMessage" {
			continue
		}
		postTimestamps[entry.Channel] = append(postTimestamps[entry.Channel], entry.ReturnedTS)
		blockJSON, err := json.Marshal(entry.Blocks)
		if err != nil {
			t.Fatalf("encode transcript blocks: %v", err)
		}
		isProblem := strings.Contains(string(blockJSON), "Code:") &&
			strings.Contains(string(blockJSON), "Class:")
		if entry.ThreadTS == "" {
			if roots[entry.Channel] != "" {
				t.Fatalf("destination %s received duplicate root cards", entry.Channel)
			}
			roots[entry.Channel] = entry.ReturnedTS
			if entry.ReplyBroadcast != "" {
				t.Fatalf("root card to %s set reply_broadcast=%q", entry.Channel, entry.ReplyBroadcast)
			}
			continue
		}
		if entry.ThreadTS != roots[entry.Channel] {
			t.Fatalf("reply to %s used thread_ts %q; want root %q",
				entry.Channel, entry.ThreadTS, roots[entry.Channel])
		}
		if isProblem {
			problemReplies[entry.Channel]++
		}
		wantBroadcast := isProblem && entry.Channel == "C-ENG"
		if got := entry.ReplyBroadcast == "true"; got != wantBroadcast {
			t.Fatalf("reply to %s broadcast=%t for %q; want %t",
				entry.Channel, got, entry.Text, wantBroadcast)
		}
	}
	for _, channel := range []string{"D-U-ADA", "C-ENG"} {
		if roots[channel] == "" {
			t.Fatalf("destination %s has no root card", channel)
		}
		if got := len(postTimestamps[channel]); got != 18 {
			t.Fatalf("destination %s posts = %d; want one root and seventeen replies", channel, got)
		}
		if got := problemReplies[channel]; got != 4 {
			t.Fatalf("destination %s Problems replies = %d; want four", channel, got)
		}
	}

	var persisted featureRecord
	if err := yaml.Unmarshal(record, &persisted); err != nil {
		t.Fatalf("decode final Problems record: %v", err)
	}
	for channel, key := range map[string]string{
		"D-U-ADA": destinationKey("user", "U-ADA"),
		"C-ENG":   destinationKey("channel", "C-ENG"),
	} {
		entry, ok := persisted.Destinations[key]
		if !ok {
			t.Fatalf("record missing destination %s", key)
		}
		want := postTimestamps[channel]
		if len(entry.Ledger) != len(want) {
			t.Fatalf("ledger for %s = %d entries; want %d", key, len(entry.Ledger), len(want))
		}
		for i := range want {
			if entry.Ledger[i] != want[i] {
				t.Fatalf("ledger for %s at %d = %q; want posted timestamp %q",
					key, i, entry.Ledger[i], want[i])
			}
		}
	}
}
