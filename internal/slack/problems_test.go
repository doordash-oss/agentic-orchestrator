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
	"encoding/json"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

func TestRenderProblemCarriesCanonicalShapeAndBoundsDiagnostics(t *testing.T) {
	problem := errcat.New(
		errcat.WorktreeSetupFailed,
		errcat.WithParams(errcat.SetupFailureParams{
			TaskLabel:    "Worktree: api & <#ops>",
			Repositories: []string{"api & <#ops>"},
		}),
		errcat.WithSetupTask(errcat.CodeSetupTask{
			Key: "worktree:api", Kind: "worktree", Label: "Worktree: api & <#ops>",
		}),
		errcat.WithRepositories(errcat.CodeRepository{Name: "api & <#ops>"}),
		errcat.WithDiagnostics(strings.Repeat("failed <@U123> & ", 400)),
	)
	blocks, fallback, code := renderProblem("", problem, &feature.Feature{})
	encoded, err := json.Marshal(blocks)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, want := range []string{
		"🛑", "Worktree setup failed", "What to do", "Actions: setup",
		"setup task", "Code: worktree_setup_failed", "Class: blocking",
		"Diagnostics were shortened", "Open Agentico",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("renderProblem() blocks missing %q: %s", want, text)
		}
	}
	var renderedText strings.Builder
	for _, block := range blocks {
		switch value := block.(type) {
		case sectionBlock:
			if value.Text != nil {
				renderedText.WriteString(value.Text.Text)
			}
		case contextBlock:
			for _, element := range value.Elements {
				renderedText.WriteString(element.Text)
			}
		}
	}
	if strings.Contains(renderedText.String(), "<@U123>") ||
		!strings.Contains(renderedText.String(), "&lt;@U123&gt;") {
		t.Errorf("renderProblem() did not escape mention-like diagnostics")
	}
	for _, block := range blocks {
		if section, ok := block.(sectionBlock); ok && section.Text != nil &&
			len(section.Text.Text) > sectionTextLimit {
			t.Errorf("renderProblem() section length = %d; want <= %d", len(section.Text.Text), sectionTextLimit)
		}
	}
	if fallback == "" || code != string(errcat.WorktreeSetupFailed) {
		t.Errorf("renderProblem() fallback/code = %q/%q", fallback, code)
	}
}

func TestRenderProblemNeedsActionChildAndShortDiagnostics(t *testing.T) {
	problem := errcat.New(
		errcat.PublishRebaseConflict,
		errcat.WithRepositories(errcat.CodeRepository{Name: "alpha", Branch: "feature/slack"}),
		errcat.WithDiagnostics("git rebase exited 1"),
	)
	child := &feature.Feature{Parent: &feature.ChildRelationship{
		ParentID: "parent-1", Kind: feature.ChildKindRefactor,
	}}
	blocks, fallback, _ := renderProblem("", problem, child)
	encoded, err := json.Marshal(blocks)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, want := range []string{"🚧", "Refactor:", "needs your action", "git rebase exited 1"} {
		if !strings.Contains(text, want) {
			t.Errorf("renderProblem(needs action child) missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, "Diagnostics were shortened") {
		t.Errorf("short diagnostics unexpectedly carry truncation note: %s", text)
	}
	if !strings.Contains(fallback, "Refactor:") {
		t.Errorf("fallback = %q; want child prefix", fallback)
	}
}

func TestRenderProblemRedactsBeforeDiagnosticsTruncation(t *testing.T) {
	secret := "xoxb-TRUNCATION-SECRET-123456789"
	for _, diagnostics := range []string{
		strings.Repeat("a", sectionTextLimit-40) + secret,
		strings.Repeat("b", sectionTextLimit-8) + secret + " tail",
	} {
		problem := errcat.New(errcat.SessionCrashed, errcat.WithDiagnostics(diagnostics))
		blocks, fallback, _ := renderProblem(secret, problem, &feature.Feature{})
		encoded, err := json.Marshal(blocks)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), secret) || strings.Contains(fallback, secret) {
			t.Fatalf("renderProblem() leaked secret around truncation point")
		}
		if strings.Contains(string(encoded), "TRUNCATION") {
			t.Fatalf("renderProblem() left a recognizable secret fragment around truncation point")
		}
	}
}

func TestRenderProblemBoundsCombinedTitleAndSummary(t *testing.T) {
	problem := errcat.Error{
		Code:    errcat.InternalError,
		Class:   errcat.ClassBlocking,
		Title:   strings.Repeat("title ", 600),
		Summary: strings.Repeat("summary ", 600),
	}
	blocks, _, _ := renderProblem("", problem, &feature.Feature{})
	first, ok := blocks[0].(sectionBlock)
	if !ok || first.Text == nil {
		t.Fatalf("renderProblem() first block = %#v; want section text", blocks[0])
	}
	if got := len(first.Text.Text); got > sectionTextLimit {
		t.Fatalf("renderProblem() title/summary length = %d; want <= %d", got, sectionTextLimit)
	}
}

func TestSlackProblemsRedaction(t *testing.T) {
	const secondSecret = "xoxp-SECONDSECRET-123456789"
	var logs bytes.Buffer
	previousLogOutput := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previousLogOutput) })

	harness := newNotifierHarness(t, defaultTestSettings(
		testToken,
		ports.SlackRecipient{Kind: ports.SlackRecipientChannel, ID: "C-ENG", DisplayName: "#eng"},
	))
	harness.seedFeature("F-1", nil)
	harness.start(0)
	harness.feed(ports.Event{Type: ports.FeatureStarted, FeatureID: "F-1"})
	waitFor(t, 10*time.Second, func() bool {
		return len(postsTo(harness.server, "C-ENG")) == 1
	})

	diagnostics := "repo alpha path /tmp/worktree exit 17 " + testToken +
		" " + secondSecret + " Authorization: Bearer header-secret" +
		" https://user:password@example.test/repo"
	problem := errcat.Error{
		Code:    errcat.SessionCrashed,
		Class:   errcat.ClassBlocking,
		Title:   "Session crashed " + testToken,
		Summary: "Provider failed " + secondSecret,
		Remediation: &errcat.Remediation{
			Hint:    "Retry without Authorization: Bearer another-secret",
			Actions: []string{"restart"},
		},
		Context: &errcat.Context{
			Repositories: []errcat.CodeRepository{{Name: "alpha", Branch: "feature/" + testToken}},
			Command:      &errcat.CodeCommand{ExitCode: 17, LogPaths: []string{"/tmp/" + secondSecret}},
		},
		Diagnostics: diagnostics,
	}
	harness.feed(ports.Event{
		Type: ports.FeatureFailed, FeatureID: "F-1", CanonicalError: &problem,
	})
	waitFor(t, 10*time.Second, func() bool {
		return len(postsTo(harness.server, "C-ENG")) == 2
	})
	waitFor(t, 10*time.Second, func() bool {
		return len(harness.server.Requests("chat.update")) >= 2
	})
	harness.feed(ports.Event{
		Type: ports.FeatureFailed, FeatureID: "F-1",
		Message: "fallback repo alpha " + testToken + " " + secondSecret,
	})
	waitFor(t, 10*time.Second, func() bool {
		return len(postsTo(harness.server, "C-ENG")) == 3
	})

	encoded, err := json.Marshal(harness.server.AllRequests())
	if err != nil {
		t.Fatal(err)
	}
	body := string(encoded)
	for _, secret := range []string{testToken, secondSecret, "header-secret", "another-secret", "user:password"} {
		if strings.Contains(body, secret) {
			t.Fatalf("Slack request leaked %q: %s", secret, body)
		}
	}
	for _, want := range []string{"[REDACTED]", "repo alpha", "/tmp/worktree", "exit 17"} {
		if !strings.Contains(body, want) {
			t.Errorf("Slack request missing non-sensitive detail %q: %s", want, body)
		}
	}
	record, err := os.ReadFile(recordPath(harness.stateDir, "F-1"))
	if err != nil {
		t.Fatal(err)
	}
	observed, err := json.Marshal(harness.observer.all())
	if err != nil {
		t.Fatal(err)
	}
	for label, value := range map[string]string{
		"logs":          logs.String(),
		"record":        string(record),
		"observability": string(observed),
	} {
		for _, secret := range []string{testToken, secondSecret, "header-secret", "another-secret", "user:password"} {
			if strings.Contains(value, secret) {
				t.Fatalf("%s leaked %q: %s", label, secret, value)
			}
		}
	}
	posts := postsTo(harness.server, "C-ENG")
	if fieldString(posts[1], "reply_broadcast") != "true" {
		t.Errorf("Problems channel reply_broadcast = %q; want true", fieldString(posts[1], "reply_broadcast"))
	}
	events := harness.observer.ofKind("slack.message_posted")
	if len(events) < 2 {
		t.Fatal("missing slack.message_posted event")
	}
	firstProblem := events[len(events)-2]
	if firstProblem.Data["item_kind"] != "problems" ||
		firstProblem.Data["error_code"] != string(errcat.SessionCrashed) {
		t.Errorf("message event = %#v; want problems and canonical code", firstProblem.Data)
	}
	eventJSON, _ := json.Marshal(events)
	if strings.Contains(string(eventJSON), problem.Title) || strings.Contains(string(eventJSON), diagnostics) {
		t.Errorf("message event leaked rendered text: %s", eventJSON)
	}
}

func TestNotifierProblemsGateAndDirectMessageBroadcast(t *testing.T) {
	settings := defaultTestSettings(testToken, testRecipients()...)
	settings.Categories.Progress = false
	harness := newNotifierHarness(t, settings)
	harness.seedFeature("F-1", nil)
	harness.start(0)
	harness.feed(ports.Event{Type: ports.FeatureStarted, FeatureID: "F-1"})
	waitFor(t, 10*time.Second, func() bool {
		return len(postsTo(harness.server, "C-ENG")) == 1 &&
			len(postsTo(harness.server, "D-U-ADA")) == 1
	})

	problem := errcat.New(errcat.PublishRebaseConflict,
		errcat.WithRepositories(errcat.CodeRepository{Name: "alpha"}),
		errcat.WithDiagnostics("conflict in alpha"),
	)
	harness.feed(ports.Event{Type: ports.PublishCompleted, FeatureID: "F-1", CanonicalError: &problem})
	waitFor(t, 10*time.Second, func() bool {
		return len(postsTo(harness.server, "C-ENG")) == 2 &&
			len(postsTo(harness.server, "D-U-ADA")) == 2
	})
	channelReply := postsTo(harness.server, "C-ENG")[1]
	dmReply := postsTo(harness.server, "D-U-ADA")[1]
	if fieldString(channelReply, "reply_broadcast") != "true" {
		t.Errorf("channel Problems broadcast = %q; want true", fieldString(channelReply, "reply_broadcast"))
	}
	if _, ok := dmReply.Fields["reply_broadcast"]; ok {
		t.Errorf("DM Problems request has reply_broadcast: %#v", dmReply.Fields)
	}

	harness.settings.mutate(func(s *ports.SlackRuntimeSettings) { s.Categories.Problems = false })
	before := len(harness.server.Requests("chat.postMessage"))
	harness.feed(ports.Event{Type: ports.FeatureFailed, FeatureID: "F-1", CanonicalError: &problem})
	waitFor(t, 10*time.Second, func() bool {
		return len(harness.server.Requests("chat.update")) >= 4
	})
	if got := len(harness.server.Requests("chat.postMessage")); got != before {
		t.Errorf("Problems-off posts = %d; want %d", got, before)
	}
}

func TestNotifierProblemFallbackAndWarningSuppression(t *testing.T) {
	harness := newNotifierHarness(t, defaultTestSettings(
		testToken,
		ports.SlackRecipient{Kind: ports.SlackRecipientChannel, ID: "C-ENG", DisplayName: "#eng"},
	))
	harness.seedFeature("F-1", nil)
	harness.start(0)
	harness.feed(ports.Event{Type: ports.FeatureStarted, FeatureID: "F-1"})
	waitFor(t, 10*time.Second, func() bool {
		return len(postsTo(harness.server, "C-ENG")) == 1
	})

	harness.feed(ports.Event{
		Type: ports.FeatureFailed, FeatureID: "F-1",
		Message: "provider exited while reading /tmp/alpha",
	})
	waitFor(t, 10*time.Second, func() bool {
		return len(postsTo(harness.server, "C-ENG")) == 2
	})
	fallbackPost := postsTo(harness.server, "C-ENG")[1]
	body, _ := json.Marshal(fallbackPost.Fields)
	if !strings.Contains(string(body), "internal_error") ||
		!strings.Contains(string(body), "/tmp/alpha") {
		t.Errorf("fallback Problem = %s; want internal error and event diagnostics", body)
	}

	warning := errcat.New(errcat.RebaseAlreadyUpToDate)
	beforePosts := len(harness.server.Requests("chat.postMessage"))
	beforeDrops := len(harness.observer.ofKind("slack.event_dropped"))
	beforeUpdates := len(harness.server.Requests("chat.update"))
	harness.feed(ports.Event{
		Type: ports.FeatureFailed, FeatureID: "F-1", CanonicalError: &warning,
	})
	waitFor(t, 10*time.Second, func() bool {
		return len(harness.server.Requests("chat.update")) > beforeUpdates
	})
	if got := len(harness.server.Requests("chat.postMessage")); got != beforePosts {
		t.Errorf("warning posts = %d; want %d", got, beforePosts)
	}
	if got := len(harness.observer.ofKind("slack.event_dropped")); got != beforeDrops {
		t.Errorf("warning drop events = %d; want %d", got, beforeDrops)
	}
}

func TestNotifierInterruptedAndRewoundUseProgressThread(t *testing.T) {
	harness := newNotifierHarness(t, defaultTestSettings(
		testToken,
		ports.SlackRecipient{Kind: ports.SlackRecipientChannel, ID: "C-ENG", DisplayName: "#eng"},
	))
	harness.seedFeature("F-1", func(f *feature.Feature) {
		f.Status = feature.StatusInterrupted
		f.ActiveRun = 3
		f.RunCount = 3
		f.CurrentPhase = feature.PhasePlan
		f.CurrentRoadmapPhase = 2
		f.TotalRoadmapPhases = 3
	})
	harness.start(0)
	harness.feed(ports.Event{Type: ports.FeatureInterrupted, FeatureID: "F-1"})
	harness.feed(ports.Event{Type: ports.FeatureRewound, FeatureID: "F-1", Phase: feature.PhasePlan})
	waitFor(t, 10*time.Second, func() bool {
		return len(postsTo(harness.server, "C-ENG")) == 3
	})
	posts := postsTo(harness.server, "C-ENG")
	if !strings.Contains(fieldString(posts[1], "text"), "Interrupted") {
		t.Errorf("interrupted line = %q", fieldString(posts[1], "text"))
	}
	for _, want := range []string{"Plan", "roadmap phase 2", "run 3"} {
		if !strings.Contains(fieldString(posts[2], "text"), want) {
			t.Errorf("rewound line = %q; missing %q", fieldString(posts[2], "text"), want)
		}
	}
	rootTS := fieldString(posts[0], "ts")
	_ = rootTS
	for _, post := range posts[1:] {
		if fieldString(post, "reply_broadcast") != "" {
			t.Errorf("Progress reply broadcast unexpectedly set: %#v", post.Fields)
		}
	}
}

func TestCardStatusErrorPrecedence(t *testing.T) {
	f := &feature.Feature{
		Status: feature.StatusCodeReady,
		Repos:  []feature.FeatureRepo{{Name: "alpha"}},
		RepoStates: map[string]*feature.RepoState{"alpha": {
			Error: &errcat.FailureRecord{Code: errcat.PublishRebaseConflict},
		}},
	}
	if got := cardStatus(f, nil); got != "Needs your action: Pull-rebase conflict" {
		t.Errorf("cardStatus(publish) = %q", got)
	}
	f.Run().Failure = &errcat.FailureRecord{Code: errcat.SessionCrashed}
	if got := cardStatus(f, nil); got != "Failed: Session crashed" {
		t.Errorf("cardStatus(blocking and publish) = %q", got)
	}
	f.Run().Failure = nil
	f.RepoStates["alpha"].Error = nil
	if got := cardStatus(f, nil); got != "Code ready" {
		t.Errorf("cardStatus(clear) = %q", got)
	}

	child := &feature.Feature{
		ID: "child-1",
		Parent: &feature.ChildRelationship{
			ParentID: "parent-1",
			Kind:     feature.ChildKindRefactor,
			Transaction: &feature.TransactionJournal{
				Phase: feature.TransactionPhaseAttention,
				Attention: &errcat.FailureRecord{
					Code: errcat.IntegrationMergeConflict,
				},
			},
		},
	}
	if got := cardStatus(f, child); got != "Needs your action: Integration merge conflict" {
		t.Errorf("cardStatus(active child attention) = %q", got)
	}
	child.Parent.Transaction.Attention = nil
	if got := cardStatus(f, child); got != "Code ready" {
		t.Errorf("cardStatus(cleared child attention) = %q", got)
	}
}

func TestEventItemKindProtectsProblemsAndLifecycleEdges(t *testing.T) {
	problem := errcat.New(errcat.InternalError)
	tests := []struct {
		name string
		ev   ports.Event
		want itemKind
	}{
		{"feature failed", ports.Event{Type: ports.FeatureFailed}, kindProblems},
		{"setup failed", ports.Event{Type: ports.SetupFailed}, kindProblems},
		{"publish failed", ports.Event{Type: ports.PublishCompleted, CanonicalError: &problem}, kindProblems},
		{"integration attention", ports.Event{Type: ports.RelationshipIntegrationChanged, CanonicalError: &problem}, kindProblems},
		{"interrupted", ports.Event{Type: ports.FeatureInterrupted}, kindLifecycle},
		{"rewound", ports.Event{Type: ports.FeatureRewound}, kindLifecycle},
		{"ordinary progress", ports.Event{Type: ports.PhaseStarted}, kindProgress},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := eventItemKind(tt.ev); got != tt.want {
				t.Errorf("eventItemKind(%s) = %v; want %v", tt.name, got, tt.want)
			}
			if tt.want != kindProgress && !eventItemKind(tt.ev).protected() {
				t.Errorf("eventItemKind(%s) is not protected", tt.name)
			}
		})
	}
}
