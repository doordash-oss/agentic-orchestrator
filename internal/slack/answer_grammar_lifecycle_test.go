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
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/slack/testsupport"
)

const grammarRoot = "100.000001"

type grammarLifecycle struct {
	h        *notifierHarness
	n        *Notifier
	port     *fakeSlackAnswerPort
	messages []testsupport.Message
	inputs   []ports.SlackPendingInput
}

func newGrammarLifecycle(t *testing.T, count int) *grammarLifecycle {
	t.Helper()
	h, n, port := newPermissionResponderAdmissionFixture(t, nil, nil)
	t.Cleanup(func() { n.Stop(context.Background()) })
	n.clock.(*gatedDeliveryClock).open()
	g := &grammarLifecycle{h: h, n: n, port: port, messages: []testsupport.Message{{TS: grammarRoot}}}
	record := n.records["feature-1"]
	record.Pending = nil
	d := record.Destinations["channel:C-ENG"]
	d.PostingIndex = nil
	d.Ledger = []string{grammarRoot}
	for i := range count {
		ts := fmt.Sprintf("100.%06d", i+2)
		identity := fmt.Sprintf("question:ask:%d", i)
		tag := fmt.Sprintf("#%d", i+1)
		record.Pending = append(record.Pending, pendingInputRecord{
			Identity: identity, SourceFeatureID: "feature-1", Kind: "question",
			RequestID: "ask", QuestionIndex: i, Tag: tag,
			MessageTS: map[string]string{"channel:C-ENG": ts},
		})
		d.PostingIndex = append(d.PostingIndex, postingIndexEntry{Identity: identity, MessageTS: ts, Tag: tag})
		d.Ledger = append(d.Ledger, ts)
		g.messages = append(g.messages, testsupport.Message{TS: ts, ThreadTS: grammarRoot, Text: "question"})
		g.inputs = append(g.inputs, ports.SlackPendingInput{
			Kind: ports.SlackPendingQuestion, RequestID: "ask", QuestionIndex: i, QuestionCount: count,
		})
	}
	record.Destinations["channel:C-ENG"] = d
	g.sync()
	return g
}

func (g *grammarLifecycle) sync() {
	g.h.pending.set("feature-1", g.inputs...)
	g.h.server.SeedThread("C-ENG", grammarRoot, g.messages)
}

func (g *grammarLifecycle) reply(ts int, tag, text, user string) {
	g.messages = append(g.messages, testsupport.Message{
		TS: fmt.Sprintf("100.%06d", ts), ThreadTS: grammarRoot, User: user,
		Text: tag + " " + text,
	})
	g.sync()
	g.n.responderTick()
}

func (g *grammarLifecycle) submissions() []ports.SlackQuestionAnswer {
	g.port.mu.Lock()
	defer g.port.mu.Unlock()
	return append([]ports.SlackQuestionAnswer(nil), g.port.questionAnswers...)
}

func (g *grammarLifecycle) helpSubmissions() []ports.SlackHelpAnswer {
	g.port.mu.Lock()
	defer g.port.mu.Unlock()
	return append([]ports.SlackHelpAnswer(nil), g.port.helpAnswers...)
}

func newGrammarHelpLifecycle(t *testing.T) *grammarLifecycle {
	t.Helper()
	g := newGrammarLifecycle(t, 1)
	help := ports.SlackPendingInput{
		FeatureID: "feature-1", Kind: ports.SlackPendingHelp,
		WaitingSince: time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC),
		HelpQuestion: "Need a hand",
	}
	identity := pendingInputIdentity(help)
	record := g.n.records["feature-1"]
	record.Pending[0].Identity = identity
	record.Pending[0].Kind = "help"
	record.Pending[0].RequestID = ""
	d := record.Destinations["channel:C-ENG"]
	d.PostingIndex[0].Identity = identity
	record.Destinations["channel:C-ENG"] = d
	g.inputs = []ports.SlackPendingInput{help}
	g.sync()
	return g
}

func (g *grammarLifecycle) drain(t *testing.T) {
	t.Helper()
	waitFor(t, 2*time.Second, func() bool { return g.n.queue.len() == 0 })
}

func TestSlackAnswerGrammarLifecycle(t *testing.T) {
	t.Run("first answer wins across duplicate reply and reaction", func(t *testing.T) {
		g := newGrammarLifecycle(t, 2)
		g.inputs[0].Options = []ports.SlackPendingInputOption{{Label: "First"}, {Label: "Second"}}
		g.sync()
		g.reply(10, "#1", "2", "U-ADA")
		g.drain(t)
		g.messages[1].Reactions = []testsupport.Reaction{{Name: "one", Count: 1, Users: []string{"U-BOB"}}}
		g.reply(11, "#1", "1", "U-BOB")
		g.drain(t)
		if got := g.n.records["feature-1"].Pending[0].HeldAnswer; got == nil ||
			got.Value != "Second" || got.ResponderID != "U-ADA" {
			t.Fatalf("held first answer = %+v; want Second by U-ADA", got)
		}
		if got := g.submissions(); len(got) != 0 {
			t.Fatalf("duplicate submitted partial bundle: %+v", got)
		}
		waitFor(t, time.Second, func() bool {
			lines := g.h.server.Requests("chat.postMessage")
			var duplicates int
			for _, line := range lines {
				if strings.Contains(fieldString(line, "text"), "#1 was already answered by <@U-ADA>") {
					duplicates++
				}
			}
			return duplicates == 2
		})
		g.reply(12, "#2", "final", "U-BOB")
		got := g.submissions()
		want := []ports.SlackQuestionIndexedAnswer{{Index: 0, Value: "Second"}, {Index: 1, Value: "final"}}
		if len(got) != 1 || !reflect.DeepEqual(got[0].Answers, want) {
			t.Fatalf("submissions = %+v; want one with %+v", got, want)
		}
	})

	t.Run("restart reloads held answer before submission", func(t *testing.T) {
		g := newGrammarLifecycle(t, 2)
		g.reply(10, "#1", "original", "U-ADA")
		g.drain(t)
		reloaded, err := loadFeatureRecord(g.h.stateDir, "feature-1")
		if err != nil {
			t.Fatal(err)
		}
		if reloaded.Pending[0].HeldAnswer == nil || reloaded.Pending[0].HeldAnswer.Value != "original" {
			t.Fatalf("reloaded held answer = %+v", reloaded.Pending[0].HeldAnswer)
		}
		restarted := NewNotifier(NotifierOptions{
			Settings: g.h.settings, Store: g.h.store, StateDir: g.h.stateDir,
			Observer: g.h.observer, Pending: g.h.pending, Answer: g.port,
			Clock: g.n.clock, ResponderClock: g.n.clock,
			NewClient: func(token string) (slackClient, error) {
				return NewClient(token, WithBaseURL(g.h.server.URL()))
			},
		})
		t.Cleanup(func() { restarted.Stop(context.Background()) })
		restarted.records["feature-1"] = reloaded
		g.n = restarted
		g.reply(11, "#2", "after restart", "U-BOB")
		got := g.submissions()
		want := []ports.SlackQuestionIndexedAnswer{{Index: 0, Value: "original"}, {Index: 1, Value: "after restart"}}
		if len(got) != 1 || !reflect.DeepEqual(got[0].Answers, want) {
			t.Fatalf("restarted submissions = %+v; want %+v", got, want)
		}
		if reloaded.Pending[0].HeldAnswer != nil || reloaded.Pending[1].HeldAnswer != nil {
			t.Fatalf("held answers survived submission: %+v", reloaded.Pending)
		}
	})

	t.Run("failed completion needs a fresh answer", func(t *testing.T) {
		g := newGrammarLifecycle(t, 2)
		g.port.questionResults = []ports.SlackAnswerResult{
			{Outcome: ports.SlackAnswerFailed, Cause: errors.New("temporary mutation failure")},
			{Outcome: ports.SlackAnswerAccepted},
		}
		g.reply(10, "#1", "held", "U-ADA")
		g.drain(t)
		g.reply(11, "#2", "failed", "U-BOB")
		if got := g.submissions(); len(got) != 1 {
			t.Fatalf("failed submission count = %d; want 1", len(got))
		}
		record := g.n.records["feature-1"]
		if record.Pending[0].HeldAnswer == nil || record.Pending[0].HeldAnswer.Value != "held" ||
			record.Pending[1].HeldAnswer != nil || record.Pending[1].Resolution != nil {
			t.Fatalf("pending after failed submission = %+v", record.Pending)
		}
		g.drain(t)
		g.n.responderTick()
		if got := g.submissions(); len(got) != 1 {
			t.Fatalf("failed reply resubmitted: %+v", got)
		}
		g.reply(12, "#2", "fresh", "U-CAROL")
		got := g.submissions()
		if len(got) != 2 || !reflect.DeepEqual(got[1].Answers,
			[]ports.SlackQuestionIndexedAnswer{{Index: 0, Value: "held"}, {Index: 1, Value: "fresh"}}) {
			t.Fatalf("fresh submission = %+v", got)
		}
	})

	t.Run("no longer pending drops held answer", func(t *testing.T) {
		g := newGrammarLifecycle(t, 2)
		g.port.questionResults = []ports.SlackAnswerResult{{Outcome: ports.SlackAnswerNoLongerPending}}
		g.reply(10, "#1", "held", "U-ADA")
		g.drain(t)
		g.reply(11, "#2", "late", "U-BOB")
		record := g.n.records["feature-1"]
		for i, item := range record.Pending {
			if item.HeldAnswer != nil || item.Resolution == nil || item.Resolution.Kind != resolutionAgentico {
				t.Errorf("pending[%d] = %+v; want Agentico resolution without held answer", i, item)
			}
		}
		if got := g.submissions(); len(got) != 1 {
			t.Fatalf("submissions = %+v; want one no-longer-pending result", got)
		}
		persisted, err := loadFeatureRecord(g.h.stateDir, "feature-1")
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range persisted.Pending {
			if item.HeldAnswer != nil {
				t.Fatalf("no-longer-pending persisted held answer: %+v", item)
			}
		}
	})

	for _, tc := range []struct {
		name       string
		event      ports.EventType
		resolution string
	}{
		{"app resolution", ports.SessionOutput, resolutionAgentico},
		{"interruption", ports.FeatureInterrupted, resolutionCleared},
		{"rewind", ports.FeatureRewound, resolutionCleared},
	} {
		t.Run("abandon held answer on "+tc.name, func(t *testing.T) {
			g := newGrammarLifecycle(t, 2)
			g.reply(10, "#1", "held answer", "U-ADA")
			g.drain(t)
			g.inputs = nil
			g.sync()
			owner, err := g.n.store.Load("feature-1")
			if err != nil {
				t.Fatal(err)
			}
			ev := ports.Event{Type: tc.event, FeatureID: owner.ID}
			g.n.processItemFor(g.h.settings.SlackSettings(), owner, owner,
				g.n.records[owner.ID], queueItem{kind: eventItemKind(ev), event: ev})
			record := g.n.records[owner.ID]
			if len(record.Pending) != 0 {
				t.Fatalf("pending after %s = %+v; want no items", tc.name, record.Pending)
			}
			for _, item := range record.Resolved {
				if item.HeldAnswer != nil || item.Resolution == nil || item.Resolution.Kind != tc.resolution {
					t.Errorf("resolved after %s = %+v; want %s without held answer", tc.name, item, tc.resolution)
				}
			}
			persisted, err := loadFeatureRecord(g.h.stateDir, owner.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range append(persisted.Pending, persisted.Resolved...) {
				if item.HeldAnswer != nil {
					t.Fatalf("persisted held answer after %s: %+v", tc.name, item)
				}
			}
			if got := g.submissions(); len(got) != 0 {
				t.Fatalf("abandoned answer submitted after %s: %+v", tc.name, got)
			}
		})
	}

	t.Run("more than nine questions collect and submit once", func(t *testing.T) {
		g := newGrammarLifecycle(t, 10)
		for i := range 10 {
			g.reply(i+100, fmt.Sprintf("#%d", i+1), fmt.Sprintf("answer-%d", i+1), "U-ADA")
			if i < 9 {
				if got := g.submissions(); len(got) != 0 {
					t.Fatalf("submitted after %d of 10: %+v", i+1, got)
				}
				if held := g.n.records["feature-1"].Pending[i].HeldAnswer; held == nil {
					t.Fatalf("question %d not held", i+1)
				}
			}
			g.drain(t)
		}
		got := g.submissions()
		if len(got) != 1 || len(got[0].Answers) != 10 {
			t.Fatalf("bundle submissions = %+v; want one with ten answers", got)
		}
		for i, answer := range got[0].Answers {
			if answer.Index != i || answer.Value != fmt.Sprintf("answer-%d", i+1) {
				t.Errorf("answer[%d] = %+v", i, answer)
			}
			if g.n.records["feature-1"].Pending[i].HeldAnswer != nil {
				t.Errorf("held answer[%d] survived submission", i)
			}
		}
	})

	t.Run("failed help needs fresh reply", func(t *testing.T) {
		g := newGrammarHelpLifecycle(t)
		g.port.helpResults = []ports.SlackAnswerResult{
			{Outcome: ports.SlackAnswerFailed, Cause: errors.New("temporary help failure")},
			{Outcome: ports.SlackAnswerAccepted},
		}
		g.reply(10, "#1", "first attempt", "U-ADA")
		if item := g.n.records["feature-1"].Pending[0]; item.Resolution != nil || item.HeldAnswer != nil {
			t.Fatalf("failed help changed pending item: %+v", item)
		}
		g.drain(t)
		g.n.responderTick()
		if got := g.helpSubmissions(); len(got) != 1 {
			t.Fatalf("help retried without fresh reply: %+v", got)
		}
		g.reply(11, "#1", "fresh attempt", "U-BOB")
		got := g.helpSubmissions()
		if len(got) != 2 || got[1].Text != "fresh attempt" ||
			got[0].EntryIdentity != got[1].EntryIdentity ||
			g.n.records["feature-1"].Pending[0].Resolution == nil {
			t.Fatalf("help after fresh reply = %+v", got)
		}
	})

	t.Run("no longer pending help retires item", func(t *testing.T) {
		g := newGrammarHelpLifecycle(t)
		g.port.helpResults = []ports.SlackAnswerResult{{Outcome: ports.SlackAnswerNoLongerPending}}
		g.reply(10, "#1", "too late", "U-ADA")
		item := g.n.records["feature-1"].Pending[0]
		if item.HeldAnswer != nil || item.Resolution == nil || item.Resolution.Kind != resolutionAgentico {
			t.Fatalf("no-longer-pending help = %+v; want Agentico resolution", item)
		}
		if got := g.helpSubmissions(); len(got) != 1 || got[0].EntryIdentity != item.Identity {
			t.Fatalf("help submissions = %+v; want exact entry", got)
		}
	})
}
