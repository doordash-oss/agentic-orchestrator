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
	"log"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/slack/testsupport"
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

func TestRenderProblemFallbackPreservesCatalogueRecoveryAndEssentialContext(t *testing.T) {
	longRepo := "alpha-" + strings.Repeat("service-", 24)
	longBranch := "feature/" + strings.Repeat("accessible-fallback-", 16)
	longConflictFiles := make([]string, 30)
	for i := range longConflictFiles {
		longConflictFiles[i] = fmt.Sprintf(
			"internal/notifications/integration/conflict_handler_%02d_test.go",
			i,
		)
	}
	tests := []struct {
		name    string
		problem errcat.Error
		feature *feature.Feature
		want    []string
	}{
		{
			name: "session crash",
			problem: errcat.New(
				errcat.SessionCrashed,
				errcat.WithParams(errcat.RunFailureParams{
					Phase: "implement", Iteration: 7, Repositories: []string{longRepo},
				}),
				errcat.WithRepositories(errcat.CodeRepository{Name: longRepo}),
				errcat.WithPhase(errcat.CodePhase{Name: "implement", Iteration: 7}),
				errcat.WithDiagnostics(strings.Repeat(
					"provider stack frame in /tmp/worktrees/alpha/session.log\n",
					30,
				)),
			),
			feature: &feature.Feature{},
			want: []string{
				"Next: Actions: restart. Restart the phase; the session log has the crash details.",
				"Details: repository " + longRepo + " · phase implement (iteration 7)",
				"Code: session_crashed (blocking)",
				"Open Agentico for the full diagnostics.",
			},
		},
		{
			name: "worktree setup",
			problem: errcat.New(
				errcat.WorktreeSetupFailed,
				errcat.WithParams(errcat.SetupFailureParams{
					TaskLabel: "Worktree: alpha", Repositories: []string{"alpha"},
				}),
				errcat.WithRepositories(errcat.CodeRepository{Name: "alpha"}),
				errcat.WithSetupTask(errcat.CodeSetupTask{
					Key: "worktree:alpha", Kind: "worktree", Label: "Worktree: alpha",
				}),
				errcat.WithDiagnostics(strings.Repeat(
					"git worktree add failed after checking repository state; ",
					24,
				)),
			),
			feature: &feature.Feature{},
			want: []string{
				"Next: Actions: setup. Resolve the reported problem in the repository or branch, then retry setup.",
				"Details: repository alpha · setup task Worktree: alpha",
				"Code: worktree_setup_failed (blocking)",
				"Open Agentico for the full diagnostics.",
			},
		},
		{
			name: "publish conflict",
			problem: errcat.New(
				errcat.PublishRebaseConflict,
				errcat.WithParams(errcat.PublishRepoParams{
					Repo: "alpha", Branch: longBranch, RebaseTarget: "main",
				}),
				errcat.WithRepositories(errcat.CodeRepository{
					Name: "alpha", Branch: longBranch, RebaseTarget: "main",
				}),
				errcat.WithDiagnostics(strings.Repeat(
					"CONFLICT in internal/slack/render.go while replaying commit; ",
					24,
				)),
			),
			feature: &feature.Feature{},
			want: []string{
				"Next: Actions: publish. Resolve the conflict in the worktree or run a rebase pass, then retry.",
				"Details: repository alpha (" + longBranch + "); target: main",
				"Code: publish_rebase_conflict (needs your action)",
				"Open Agentico for the full diagnostics.",
			},
		},
		{
			name: "child integration attention",
			problem: errcat.New(
				errcat.IntegrationMergeConflict,
				errcat.WithParams(errcat.IntegrationRepoParams{
					Repositories: []errcat.CodeRepository{{Name: "alpha"}},
				}),
				errcat.WithRepositories(errcat.CodeRepository{
					Name:          "alpha",
					Branch:        "feature/refactor",
					ConflictFiles: longConflictFiles,
				}),
				errcat.WithDiagnostics(strings.Repeat(
					"merge conflict in internal/slack/render.go; ",
					30,
				)),
			),
			feature: &feature.Feature{Parent: &feature.ChildRelationship{
				ParentID: "parent-1", Kind: feature.ChildKindRefactor,
			}},
			want: []string{
				"Refactor: Integration merge conflict",
				"Next: Actions: retry. Resolve the conflict in the pass worktree and retry; the pass re-enters final review if its code changed.",
				"Details: repository alpha (feature/refactor); conflicts: internal/notifications/integration/conflict_handler_00_test.go",
				", ...",
				"Code: integration_merge_conflict (needs your action)",
				"Open Agentico for the full diagnostics.",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, fallback, _ := renderProblem(testToken, tt.problem, tt.feature)
			if len(fallback) > problemFallbackTextLimit {
				t.Errorf(
					"renderProblem() fallback length = %d; want <= %d",
					len(fallback),
					problemFallbackTextLimit,
				)
			}
			for _, want := range tt.want {
				if !strings.Contains(fallback, want) {
					t.Errorf("renderProblem() fallback = %q; want complete meaning %q", fallback, want)
				}
			}
		})
	}
}

func TestRenderProblemFallbackAbbreviatesConflictFilesAtItemBoundary(t *testing.T) {
	conflictFiles := make([]string, 30)
	for i := range conflictFiles {
		conflictFiles[i] = fmt.Sprintf(
			"internal/notifications/integration/conflict_handler_%02d_test.go",
			i,
		)
	}
	problem := errcat.New(
		errcat.IntegrationMergeConflict,
		errcat.WithRepositories(errcat.CodeRepository{
			Name:          "alpha",
			Branch:        "feature/refactor",
			ConflictFiles: conflictFiles,
		}),
		errcat.WithDiagnostics("Merge conflict in the pass worktree."),
	)
	child := &feature.Feature{Parent: &feature.ChildRelationship{
		ParentID: "parent-1", Kind: feature.ChildKindRefactor,
	}}

	_, fallback, _ := renderProblem(testToken, problem, child)

	for _, want := range []string{
		"Next: Actions: retry. Resolve the conflict in the pass worktree and retry; the pass re-enters final review if its code changed.",
		"Details: repository alpha (feature/refactor); conflicts: internal/notifications/integration/conflict_handler_00_test.go",
		", ...",
		"Code: integration_merge_conflict (needs your action)",
		"Open Agentico for the full diagnostics.",
	} {
		if !strings.Contains(fallback, want) {
			t.Errorf("renderProblem() fallback = %q; want %q", fallback, want)
		}
	}
	if len(fallback) > problemFallbackTextLimit {
		t.Errorf(
			"renderProblem() fallback length = %d; want <= %d",
			len(fallback),
			problemFallbackTextLimit,
		)
	}
}

func TestAbbreviateFallbackTextPrefersCompleteSentence(t *testing.T) {
	text := "The provider exited with status 17. " +
		"Secondary stack details continue through several frames and helper calls."
	got := abbreviateFallbackText(text, 80)
	want := "The provider exited with status 17..."
	if got != want {
		t.Errorf("abbreviateFallbackText() = %q; want %q", got, want)
	}
}

func TestRenderProblemRedactsBeforeDiagnosticsTruncation(t *testing.T) {
	secret := "xoxb-TRUNCATION-SECRET-123456789"
	const note = "\n_Diagnostics were shortened. Open Agentico for the full text._"
	bodyBudget := sectionTextLimit - len("```\n\n```") - len(note)
	tests := []struct {
		name        string
		secretStart int
	}{
		{name: "beyond final content cut", secretStart: bodyBudget + 8},
		{name: "straddles final content cut", secretStart: bodyBudget - len(secret)/2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			diagnostics := strings.Repeat("a", tt.secretStart) + secret + strings.Repeat("z", 160)
			secretEnd := tt.secretStart + len(secret)
			if tt.name == "straddles final content cut" &&
				!(tt.secretStart < bodyBudget && secretEnd > bodyBudget) {
				t.Fatalf("secret range [%d,%d) does not straddle diagnostics budget %d",
					tt.secretStart, secretEnd, bodyBudget)
			}
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
			if !strings.Contains(string(encoded), "Diagnostics were shortened") {
				t.Fatalf("renderProblem() did not exercise the final diagnostics cut")
			}
		})
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
	const digestSecret = "DIGEST_REVIEW_SENTINEL"
	const arraySecret = "ARRAY_REVIEW_SENTINEL"
	const semicolonSecret = "SEMICOLON_REVIEW_SENTINEL"
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
		" https://user:password@example.test/repo\n" +
		"Authorization: Digest username=\"operator\", response=\"" + digestSecret +
		"\"\nrepo gamma path /tmp/digest exit 29\n" +
		"{\"Authorization\":[\"Bearer " + arraySecret +
		"\"],\"path\":\"/tmp/array\",\"exit\":31}\n" +
		"Authorization: Digest username=\"ops;bot\", response=\"" + semicolonSecret +
		"\"; repo delta path /tmp/semicolon exit 37"
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
		Message: "fallback repo beta " + testToken + " " + secondSecret +
			" Authorization: Digest username=\"operator\", response=\"" + digestSecret +
			"\"\npath /tmp/fallback exit 23\n" +
			"{\"Authorization\":[\"Bearer " + arraySecret +
			"\"],\"path\":\"/tmp/fallback-array\",\"exit\":41}\n" +
			"Authorization: Digest username=\"ops;bot\", response=\"" + semicolonSecret +
			"\"; repo epsilon path /tmp/fallback-semicolon exit 43",
	})
	waitFor(t, 10*time.Second, func() bool {
		return len(postsTo(harness.server, "C-ENG")) == 3
	})
	waitFor(t, 10*time.Second, func() bool {
		problems := 0
		for _, event := range harness.observer.ofKind("slack.message_posted") {
			if event.Data["item_kind"] == "problems" {
				problems++
			}
		}
		return problems == 2
	})

	encoded, err := json.Marshal(harness.server.AllRequests())
	if err != nil {
		t.Fatal(err)
	}
	body := string(encoded)
	for _, secret := range []string{
		testToken, secondSecret, digestSecret, arraySecret, semicolonSecret, "operator", "ops;bot",
		"header-secret", "another-secret", "user:password",
	} {
		if strings.Contains(body, secret) {
			t.Fatalf("Slack request leaked %q: %s", secret, body)
		}
	}
	for _, want := range []string{
		"[REDACTED]",
		"repo alpha", "/tmp/worktree", "exit 17",
		"repo gamma", "/tmp/digest", "exit 29",
		"repo beta", "/tmp/fallback", "exit 23",
		"/tmp/array", "31", "repo delta", "/tmp/semicolon", "exit 37",
		"/tmp/fallback-array", "41", "repo epsilon", "/tmp/fallback-semicolon", "exit 43",
	} {
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
		for _, secret := range []string{
			testToken, secondSecret, digestSecret, arraySecret, semicolonSecret, "operator", "ops;bot",
			"header-secret", "another-secret", "user:password",
		} {
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
	notifier := harness.start(0)
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
	// The event's queue reservation clears once every destination has
	// handled its delivery, so the suppressed reply would be posted by now.
	waitFor(t, 10*time.Second, func() bool { return notifier.queue.len() == 0 })
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
	first := harness.start(0)
	harness.feed(ports.Event{Type: ports.FeatureInterrupted, FeatureID: "F-1"})
	harness.feed(ports.Event{Type: ports.FeatureRewound, FeatureID: "F-1", Phase: feature.PhasePlan})
	waitFor(t, 10*time.Second, func() bool {
		return len(postsTo(harness.server, "C-ENG")) == 3
	})
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
	for i, ev := range postRewind[:5] {
		harness.feed(ev)
		want := 4 + i
		waitFor(t, 10*time.Second, func() bool {
			return len(postsTo(harness.server, "C-ENG")) == want
		})
	}
	first.Stop(context.Background())
	second := harness.newNotifier(0)
	second.Start()
	t.Cleanup(func() { second.Stop(context.Background()) })
	harness.notifier = second
	second.SignalReady()
	waitFor(t, time.Second, func() bool { return second.startupDone.Load() })
	for i, ev := range postRewind[5:] {
		harness.feed(ev)
		want := 9 + i
		waitFor(t, 10*time.Second, func() bool {
			return len(postsTo(harness.server, "C-ENG")) == want
		})
	}

	posts := postsTo(harness.server, "C-ENG")
	if !strings.Contains(fieldString(posts[1], "text"), "Interrupted") {
		t.Errorf("interrupted line = %q", fieldString(posts[1], "text"))
	}
	for _, want := range []string{"Plan", "roadmap phase 2", "run 3"} {
		if !strings.Contains(fieldString(posts[2], "text"), want) {
			t.Errorf("rewound line = %q; missing %q", fieldString(posts[2], "text"), want)
		}
	}
	key := destinationKey("channel", "C-ENG")
	waitFor(t, 10*time.Second, func() bool {
		ledger, ok := recordLedger(harness.stateDir, "F-1", key)
		return ok && len(ledger) == 13
	})
	destination := recordDestinations(t, harness.stateDir, "F-1")[key]
	rootTS := destination.RootTS
	if rootTS == "" {
		t.Fatal("persisted destination has no root timestamp")
	}
	if got := len(posts); got != 13 {
		t.Fatalf("posts = %d; want one root, interruption, rewind, and ten later lifecycle replies", got)
	}
	for _, post := range posts[1:] {
		if got := fieldString(post, "thread_ts"); got != rootTS {
			t.Errorf("reply thread_ts = %q; want persisted root %q", got, rootTS)
		}
		if fieldString(post, "reply_broadcast") != "" {
			t.Errorf("Progress reply broadcast unexpectedly set: %#v", post.Fields)
		}
	}
	if got := len(destination.Ledger); got != 13 {
		t.Fatalf("persisted ledger entries = %d; want all thirteen posts across notifier reconstruction", got)
	}
}

func TestNotifierRewindOutsideImplementationLoopOmitsRoadmapPhase(t *testing.T) {
	harness := newNotifierHarness(t, defaultTestSettings(
		testToken,
		ports.SlackRecipient{Kind: ports.SlackRecipientChannel, ID: "C-ENG", DisplayName: "#eng"},
	))
	harness.seedFeature("F-1", func(f *feature.Feature) {
		f.ActiveRun = 5
		f.RunCount = 5
		f.CurrentPhase = feature.PhaseResearch
		f.CurrentRoadmapPhase = 3
		f.TotalRoadmapPhases = 4
	})
	harness.start(0)
	harness.feed(ports.Event{Type: ports.FeatureStarted, FeatureID: "F-1"})
	waitFor(t, 10*time.Second, func() bool {
		return len(postsTo(harness.server, "C-ENG")) == 1
	})
	harness.feed(ports.Event{
		Type: ports.FeatureRewound, FeatureID: "F-1", Phase: feature.PhaseResearch,
	})
	waitFor(t, 10*time.Second, func() bool {
		return len(postsTo(harness.server, "C-ENG")) == 2
	})

	posts := postsTo(harness.server, "C-ENG")
	text := fieldString(posts[1], "text")
	for _, want := range []string{"Rewound to Research", "run 5"} {
		if !strings.Contains(text, want) {
			t.Errorf("rewound line = %q; missing %q", text, want)
		}
	}
	if strings.Contains(text, "roadmap phase") {
		t.Errorf("rewound line = %q; roadmap wording should be absent outside implementation loop", text)
	}
	key := destinationKey("channel", "C-ENG")
	waitFor(t, 10*time.Second, func() bool {
		ledger, ok := recordLedger(harness.stateDir, "F-1", key)
		return ok && len(ledger) == 2
	})
	destination := recordDestinations(t, harness.stateDir, "F-1")[key]
	if got := fieldString(posts[1], "thread_ts"); got != destination.RootTS {
		t.Errorf("rewound thread_ts = %q; want persisted root %q", got, destination.RootTS)
	}
	if got := len(destination.Ledger); got != 2 {
		t.Errorf("persisted ledger entries = %d; want root and rewind", got)
	}
}

func TestNotifierChildInterruptionUsesParentThread(t *testing.T) {
	harness := newNotifierHarness(t, defaultTestSettings(
		testToken,
		ports.SlackRecipient{Kind: ports.SlackRecipientChannel, ID: "C-ENG", DisplayName: "#eng"},
	))
	harness.seedFeature("F-1", func(f *feature.Feature) { withRoadmap(f) })
	harness.seedFeature("F-2", func(f *feature.Feature) {
		f.Status = feature.StatusInterrupted
		f.Parent = &feature.ChildRelationship{ParentID: "F-1", Kind: feature.ChildKindRefactor}
	})
	harness.start(0)
	harness.feed(ports.Event{Type: ports.FeatureInterrupted, FeatureID: "F-2"})
	waitFor(t, 10*time.Second, func() bool {
		return len(postsTo(harness.server, "C-ENG")) == 2
	})

	posts := postsTo(harness.server, "C-ENG")
	reply := posts[1]
	for _, want := range []string{"Refactor:", "Interrupted"} {
		if !strings.Contains(fieldString(reply, "text"), want) {
			t.Errorf("child interruption = %q; missing %q", fieldString(reply, "text"), want)
		}
	}
	parent := recordDestinations(t, harness.stateDir, "F-1")[destinationKey("channel", "C-ENG")]
	if got := fieldString(reply, "thread_ts"); got != parent.RootTS {
		t.Errorf("child interruption thread_ts = %q; want parent root %q", got, parent.RootTS)
	}
	if recordExists(harness.stateDir, "F-2") {
		t.Fatal("child interruption created a child Slack record")
	}
}

func TestNotifierLifecycleEdgesRespectProgressAndConfigurationGates(t *testing.T) {
	t.Run("Progress off refreshes the card without thread replies", func(t *testing.T) {
		settings := defaultTestSettings(
			testToken,
			ports.SlackRecipient{Kind: ports.SlackRecipientChannel, ID: "C-ENG", DisplayName: "#eng"},
		)
		harness := newNotifierHarness(t, settings)
		harness.seedFeature("F-1", func(f *feature.Feature) {
			f.ActiveRun = 2
			f.RunCount = 2
		})
		harness.start(0)
		harness.feed(ports.Event{Type: ports.FeatureStarted, FeatureID: "F-1"})
		waitFor(t, 10*time.Second, func() bool {
			return len(postsTo(harness.server, "C-ENG")) == 1 &&
				len(harness.server.Requests("chat.update")) >= 1
		})
		harness.settings.mutate(func(s *ports.SlackRuntimeSettings) {
			s.Categories.Progress = false
		})

		modifyProblemsEvidenceFeature(t, harness, "F-1", func(f *feature.Feature) {
			f.Status = feature.StatusInterrupted
		})
		updatesBefore := len(harness.server.Requests("chat.update"))
		harness.feed(ports.Event{Type: ports.FeatureInterrupted, FeatureID: "F-1"})
		waitForProblemsEvidenceUpdate(t, harness, updatesBefore, "*Status:* Interrupted")
		if got := len(postsTo(harness.server, "C-ENG")); got != 1 {
			t.Fatalf("Progress-off interruption posts = %d; want only the root card", got)
		}

		modifyProblemsEvidenceFeature(t, harness, "F-1", func(f *feature.Feature) {
			f.Status = feature.StatusPlanning
			f.CurrentPhase = feature.PhasePlan
		})
		updatesBefore = len(harness.server.Requests("chat.update"))
		harness.feed(ports.Event{Type: ports.FeatureRewound, FeatureID: "F-1", Phase: feature.PhasePlan})
		waitForProblemsEvidenceUpdate(t, harness, updatesBefore, "*Phase:* Plan", "*Status:* Planning")
		if got := len(postsTo(harness.server, "C-ENG")); got != 1 {
			t.Fatalf("Progress-off rewind posts = %d; want only the root card", got)
		}
	})

	for _, tc := range []struct {
		name   string
		mutate func(*ports.SlackRuntimeSettings)
	}{
		{name: "Slack disabled", mutate: func(s *ports.SlackRuntimeSettings) { s.Enabled = false }},
		{name: "tokenless", mutate: func(s *ports.SlackRuntimeSettings) { s.Token = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			settings := defaultTestSettings(
				testToken,
				ports.SlackRecipient{Kind: ports.SlackRecipientChannel, ID: "C-ENG", DisplayName: "#eng"},
			)
			tc.mutate(&settings)
			harness := newNotifierHarness(t, settings)
			harness.seedFeature("F-1", func(f *feature.Feature) {
				f.Status = feature.StatusInterrupted
				f.ActiveRun = 2
				f.RunCount = 2
			})
			harness.start(0)
			harness.feed(ports.Event{Type: ports.FeatureInterrupted, FeatureID: "F-1"})
			harness.feed(ports.Event{Type: ports.FeatureRewound, FeatureID: "F-1", Phase: feature.PhasePlan})
			time.Sleep(50 * time.Millisecond)
			if got := len(harness.server.AllRequests()); got != 0 {
				t.Fatalf("requests = %d; want zero", got)
			}
			if recordExists(harness.stateDir, "F-1") {
				t.Fatal("unusable Slack configuration wrote a record")
			}
		})
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

	setupFailure := &errcat.FailureRecord{
		Code: errcat.WorktreeSetupFailed,
		Context: &errcat.RecordContext{SetupTask: &errcat.CodeSetupTask{
			Key: "worktree:alpha", Kind: "worktree", Label: "Worktree: alpha",
		}},
	}
	f.Run().Failure = setupFailure
	f.Run().Setup = &feature.SetupState{Tasks: map[string]feature.SetupTask{
		"worktree:alpha": {
			Key: "worktree:alpha", Kind: feature.SetupTaskWorktree, Label: "Worktree: alpha",
			Repo: "alpha", Status: feature.SetupStatusFailed, Error: setupFailure,
		},
	}}
	if got := cardStatus(f, nil); got != "Failed: Worktree setup failed" {
		t.Errorf("cardStatus(failed setup task) = %q", got)
	}

	f.Run().Setup = nil
	f.Run().Failure = &errcat.FailureRecord{Code: errcat.RebaseAlreadyUpToDate}
	if got := cardStatus(f, nil); got != "Code ready" {
		t.Errorf("cardStatus(warning only) = %q", got)
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

func TestNotifierProtectedProblemsAndLifecycleEdgesSurviveProgressOverflow(t *testing.T) {
	problem := errcat.New(
		errcat.SessionCrashed,
		errcat.WithDiagnostics("provider exited while running alpha"),
	)
	tests := []struct {
		name          string
		event         ports.Event
		wantText      string
		wantItemKind  string
		wantErrorCode string
	}{
		{
			name: "Problems",
			event: ports.Event{
				Type: ports.FeatureFailed, FeatureID: "F-1", CanonicalError: &problem,
			},
			wantText:      "Session crashed",
			wantItemKind:  "problems",
			wantErrorCode: string(errcat.SessionCrashed),
		},
		{
			name:         "interrupted",
			event:        ports.Event{Type: ports.FeatureInterrupted, FeatureID: "F-1"},
			wantText:     "Interrupted",
			wantItemKind: "progress",
		},
		{
			name:         "rewound",
			event:        ports.Event{Type: ports.FeatureRewound, FeatureID: "F-1", Phase: feature.PhasePlan},
			wantText:     "Rewound to Plan",
			wantItemKind: "progress",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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

			inner, _ := defaultOKResponder()
			updateEntered := make(chan struct{})
			releaseUpdate := make(chan struct{})
			var blockOnce sync.Once
			var releaseOnce sync.Once
			t.Cleanup(func() { releaseOnce.Do(func() { close(releaseUpdate) }) })
			harness.server.SetDefault(func(method string, request testsupport.Request) testsupport.Response {
				if method == "chat.update" {
					blockOnce.Do(func() {
						close(updateEntered)
						<-releaseUpdate
					})
				}
				return inner(method, request)
			})

			notifier := harness.start(3)
			harness.feed(ports.Event{Type: ports.FeatureStarted, FeatureID: "F-1"})
			select {
			case <-updateEntered:
			case <-time.After(10 * time.Second):
				t.Fatal("worker did not reach the held card update")
			}

			for _, phase := range []feature.Phase{
				feature.PhaseResearch,
				feature.PhaseDesign,
				feature.PhaseInquire,
			} {
				harness.feed(startedEvent("F-1", phase))
			}
			waitFor(t, 10*time.Second, func() bool { return notifier.queue.len() == 3 })
			harness.feed(tt.event)
			waitFor(t, 10*time.Second, func() bool {
				return len(harness.observer.ofKind("slack.event_dropped")) == 1
			})
			releaseOnce.Do(func() { close(releaseUpdate) })

			waitFor(t, 10*time.Second, func() bool {
				for _, post := range postsTo(harness.server, "C-ENG") {
					if strings.Contains(fieldString(post, "text"), tt.wantText) {
						return true
					}
				}
				return false
			})
			waitFor(t, 10*time.Second, func() bool { return notifier.queue.len() == 0 })

			dropped := harness.observer.ofKind("slack.event_dropped")
			if dropped[0].Data["event_type"] != "phase.started" ||
				dropped[0].Data["reason"] != "queue_overflow" {
				t.Fatalf("drop event = %#v; want evicted phase.started Progress", dropped[0].Data)
			}
			for _, post := range postsTo(harness.server, "C-ENG") {
				if strings.Contains(fieldString(post, "text"), "Research started") {
					t.Fatal("evicted oldest Progress item was delivered")
				}
			}
			posted := harness.observer.ofKind("slack.message_posted")
			if len(posted) == 0 {
				t.Fatal("missing slack.message_posted event")
			}
			last := posted[len(posted)-1]
			if last.Data["item_kind"] != tt.wantItemKind {
				t.Fatalf("last delivered item kind = %v; want %s", last.Data["item_kind"], tt.wantItemKind)
			}
			if tt.wantErrorCode != "" && last.Data["error_code"] != tt.wantErrorCode {
				t.Fatalf("last delivered error code = %v; want %s",
					last.Data["error_code"], tt.wantErrorCode)
			}
		})
	}
}
