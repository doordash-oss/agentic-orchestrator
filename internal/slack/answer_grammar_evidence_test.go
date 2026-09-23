package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/internal/slack/testsupport"
)

const answerGrammarEvidenceID = responderEvidenceFeatureID

type grammarEvidenceSubmission struct {
	Kind      string                   `json:"kind"`
	RequestID string                   `json:"request_id,omitempty"`
	EntryID   string                   `json:"entry_id,omitempty"`
	Indices   []int                    `json:"question_indices,omitempty"`
	Outcome   ports.SlackAnswerOutcome `json:"outcome"`
}

type grammarEvidencePort struct {
	fakeSlackAnswerPort
	mu          sync.Mutex
	submissions []grammarEvidenceSubmission
}

func (p *grammarEvidencePort) AnswerSlackQuestion(a ports.SlackQuestionAnswer) ports.SlackAnswerResult {
	result := p.fakeSlackAnswerPort.AnswerSlackQuestion(a)
	indices := make([]int, 0, len(a.Answers))
	for _, answer := range a.Answers {
		indices = append(indices, answer.Index)
	}
	p.mu.Lock()
	p.submissions = append(p.submissions, grammarEvidenceSubmission{
		Kind: "question", RequestID: a.RequestID, Indices: indices, Outcome: result.Outcome,
	})
	p.mu.Unlock()
	return result
}

func (p *grammarEvidencePort) AnswerSlackHelp(a ports.SlackHelpAnswer) ports.SlackAnswerResult {
	result := p.fakeSlackAnswerPort.AnswerSlackHelp(a)
	p.mu.Lock()
	p.submissions = append(p.submissions, grammarEvidenceSubmission{
		Kind: "help", EntryID: a.EntryIdentity, Outcome: result.Outcome,
	})
	p.mu.Unlock()
	return result
}

func (p *grammarEvidencePort) snapshot() []grammarEvidenceSubmission {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]grammarEvidenceSubmission(nil), p.submissions...)
}

type grammarEvidenceInput struct {
	Identity   string             `json:"identity"`
	Tag        string             `json:"tag"`
	Resolution *postingResolution `json:"resolution,omitempty"`
	Held       bool               `json:"held"`
}

type grammarEvidenceTick struct {
	Name     string                    `json:"name"`
	Pending  []grammarEvidenceInput    `json:"pending"`
	Resolved []grammarEvidenceInput    `json:"resolved"`
	Ledgers  []responderEvidenceLedger `json:"ledgers"`
}

func TestSlackAnswerGrammarEvidence(t *testing.T) {
	const (
		token  = "xoxp-grammar-evidence-sentinel-727272"
		secret = "xoxb-grammar-secondary-838383"
	)
	h := newNotifierHarness(t, defaultTestSettings(token, testRecipients()...))
	h.seedFeature(answerGrammarEvidenceID, nil)
	h.server.SetOwnUserID(responderEvidenceOwnerID)
	var sequence atomic.Int64
	h.server.SetDefault(func(method string, request testsupport.Request) testsupport.Response {
		switch method {
		case "conversations.open":
			return testsupport.Response{Body: map[string]any{
				"ok": true, "channel": map[string]any{"id": responderEvidenceUserChannel},
			}}
		case "chat.postMessage", "chat.update":
			return testsupport.Response{Body: map[string]any{
				"ok": true, "ts": responderEvidenceTimestamp(sequence.Add(1)),
				"channel": fmt.Sprint(request.Fields["channel"]),
			}}
		}
		return testsupport.Response{Body: map[string]any{"ok": false, "error": "unexpected " + method}}
	})
	h.server.Script("users.info", responderEvidenceUserInfo(
		responderEvidenceOwnerID, "Ada "+token+" "+secret,
	))
	port := &grammarEvidencePort{}
	clock := newManualResponderClock()
	n := NewNotifier(NotifierOptions{
		Settings: h.settings, Store: h.store, StateDir: h.stateDir,
		Observer: h.observer, Pending: h.pending, Answer: port,
		Clock: h.clock, ResponderClock: clock, QueueCapacity: 64,
		NewClient: func(token string) (slackClient, error) {
			return NewClient(token, WithBaseURL(h.server.URL()))
		},
	})
	n.Start()
	t.Cleanup(func() { n.Stop(context.Background()) })
	h.notifier = n
	n.startupDone.Store(true)
	n.DomainEventTap(startedEvent(answerGrammarEvidenceID, feature.PhaseImplement))
	waitFor(t, 10*time.Second, func() bool {
		r, ok := readFeatureRecord(h.stateDir, answerGrammarEvidenceID)
		if !ok || len(r.Destinations) != 2 {
			return false
		}
		for _, d := range r.Destinations {
			if d.RootTS == "" || len(d.Ledger) < 2 {
				return false
			}
		}
		return true
	})
	roots := responderEvidenceRecord(t, h)
	userRoot := roots.Destinations[responderEvidenceUserKey].RootTS
	channelRoot := roots.Destinations[responderEvidenceChannelKey].RootTS
	humans := newResponderEvidenceHumans()
	var ticks []grammarEvidenceTick
	var seeded []string
	pending := []ports.SlackPendingInput{}
	post := func(items ...ports.SlackPendingInput) []pendingInputRecord {
		t.Helper()
		pending = append(pending, items...)
		h.pending.set(answerGrammarEvidenceID, pending...)
		switch items[0].Kind {
		case ports.SlackPendingQuestion:
			n.RuntimeMessageTap(controlRuntimeMessage(answerGrammarEvidenceID, "session-evidence", items[0].RequestID))
		case ports.SlackPendingGate:
			n.DomainEventTap(ports.Event{Type: ports.NeedUserInputRequired, FeatureID: answerGrammarEvidenceID})
		case ports.SlackPendingHelp:
			n.RuntimeMessageTap(session.SDKEventMsg{
				SessionID: "session-evidence", FeatureID: answerGrammarEvidenceID,
				Phase: feature.PhaseImplement, Message: llm.SDKMessage{Type: "assistant"},
			})
		}
		waitFor(t, 10*time.Second, func() bool {
			r := responderEvidenceRecord(t, h)
			for _, item := range items {
				found := false
				for _, posted := range r.Pending {
					if posted.Identity == pendingInputIdentity(item) && len(posted.MessageTS) == 2 {
						found = true
						break
					}
				}
				if !found {
					return false
				}
			}
			return true
		})
		r := responderEvidenceRecord(t, h)
		result := make([]pendingInputRecord, 0, len(items))
		for _, item := range items {
			for _, posted := range r.Pending {
				if posted.Identity == pendingInputIdentity(item) {
					result = append(result, posted)
					break
				}
			}
		}
		return result
	}
	tick := func(name string, wantSubmissions, wantReactions int) {
		t.Helper()
		responderEvidenceSeedThreads(t, h, humans)
		polls := h.server.CallCount("conversations.replies")
		responderEvidenceTickClock(t, clock)
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if h.server.CallCount("conversations.replies") >= polls+2 &&
				len(port.snapshot()) == wantSubmissions &&
				h.server.CallCount("reactions.add") == wantReactions {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		if got := h.server.CallCount("reactions.add"); got != wantReactions {
			t.Fatalf("%s: reactions = %d; want %d; polls = %d; submissions = %+v; record = %+v",
				name, got, wantReactions, h.server.CallCount("conversations.replies"),
				port.snapshot(), responderEvidenceRecord(t, h).Pending)
		}
		if got := len(port.snapshot()); got != wantSubmissions {
			t.Fatalf("%s: submissions = %d; want %d", name, got, wantSubmissions)
		}
		if got := h.server.CallCount("conversations.replies"); got != polls+2 {
			t.Fatalf("%s: polls = %d; want %d", name, got, polls+2)
		}
		// The poll completes before queued confirmation writes. Wait for the
		// worker to flush them before capturing the ledger.
		waitFor(t, 10*time.Second, func() bool { return n.queue.len() == 0 })
		ticks = append(ticks, grammarEvidenceCapture(t, h, name))
	}
	reply := func(channel, root, text, user string) string {
		t.Helper()
		ts := responderEvidenceTimestamp(sequence.Add(1))
		humans.addReply(channel, root, ts, user, text)
		seeded = append(seeded, text)
		return ts
	}
	remove := func(requestID string) {
		t.Helper()
		next := pending[:0:0]
		for _, item := range pending {
			if item.RequestID != requestID {
				next = append(next, item)
			}
		}
		pending = next
		h.pending.set(answerGrammarEvidenceID, pending...)
	}
	question := func(id string, index, count int, options []ports.SlackPendingInputOption, multi bool) ports.SlackPendingInput {
		return ports.SlackPendingInput{
			Kind: ports.SlackPendingQuestion, FeatureID: answerGrammarEvidenceID,
			RequestID: id, QuestionIndex: index, QuestionCount: count,
			Header: "Scope", Question: fmt.Sprintf("Choose scope %d for %s?", index, id),
			Options: options, MultiSelect: multi,
		}
	}
	sensitiveLabel := "Broad scope " + token + " " + secret
	redactedLabel := "Broad scope [REDACTED] [REDACTED]"
	options := []ports.SlackPendingInputOption{{Label: "Focused"}, {Label: sensitiveLabel}, {Label: "Separate"}}
	bundle := post(question("bundle", 0, 2, options, false), question("bundle", 1, 2, options, false))
	humans.addReaction(responderEvidenceUserChannel, userRoot,
		bundle[0].MessageTS[responderEvidenceUserKey], "two", responderEvidenceOwnerID)
	tick("bundle_first_keycap", 0, 0)
	if got := grammarEvidenceCapture(t, h, "check").Pending[0].Held; !got {
		t.Fatal("first bundle answer was not held")
	}
	grammarEvidenceRequireLine(t, h, "1 of 2 answered, still waiting on #2.", 2)
	reply(responderEvidenceChannel, channelRoot, bundle[1].Tag+" 1", responderEvidenceOwnerID)
	tick("bundle_tagged_submission", 1, 1)
	grammarEvidenceRequireAnswers(t, port, "bundle", redactedLabel, "Focused")
	grammarEvidenceRequireLine(t, h, "#1 was answered '"+redactedLabel+"' by <@U-ADA> via Slack.", 2)
	grammarEvidenceRequireLine(t, h, "#2 was answered 'Focused' by <@U-ADA> via Slack.", 2)
	remove("bundle")

	post(question("single-label", 0, 1, options, false))
	reply(responderEvidenceChannel, channelRoot, "7", responderEvidenceOwnerID)
	tick("single_out_of_range", 1, 2)
	if !grammarEvidencePending(responderEvidenceRecord(t, h), pendingInputIdentity(question("single-label", 0, 1, options, false))) {
		t.Fatal("out-of-range reply retired single-select question")
	}
	reply(responderEvidenceChannel, channelRoot, "oversized "+strings.Repeat("x", sectionTextLimit), responderEvidenceOwnerID)
	tick("single_overlong", 1, 3)
	reply(responderEvidenceChannel, channelRoot, strings.Replace(sensitiveLabel, "Broad scope", "bRoAd ScOpE", 1)+"!", responderEvidenceOwnerID)
	tick("single_label", 2, 4)
	grammarEvidenceRequireAnswers(t, port, "single-label", redactedLabel)
	remove("single-label")

	post(question("single-other", 0, 1, options, false))
	reply(responderEvidenceChannel, channelRoot, "split each repository independently", responderEvidenceOwnerID)
	tick("single_off_menu", 3, 5)
	remove("single-other")

	post(question("multi", 0, 1, options, true))
	reply(responderEvidenceChannel, channelRoot, "1 and perhaps 2", responderEvidenceOwnerID)
	tick("multi_malformed", 3, 6)
	if !grammarEvidencePending(responderEvidenceRecord(t, h), pendingInputIdentity(question("multi", 0, 1, options, true))) {
		t.Fatal("malformed list retired multi-select question")
	}
	reply(responderEvidenceChannel, channelRoot, "1, 3", responderEvidenceOwnerID)
	tick("multi_valid", 4, 7)
	grammarEvidenceRequireAnswers(t, port, "multi", "Focused, Separate")
	remove("multi")

	post(question("free", 0, 1, nil, false))
	botTS := reply(responderEvidenceChannel, channelRoot, "deployment summary authored by bot", "U-BOT")
	joinTS := reply(responderEvidenceChannel, channelRoot, "member joined channel", responderEvidenceOwnerID)
	for i := range humans.messages[responderEvidenceChannel] {
		m := &humans.messages[responderEvidenceChannel][i]
		if m.TS == botTS {
			m.BotID = "B-DEPLOY"
		}
		if m.TS == joinTS {
			m.Subtype = "channel_join"
		}
	}
	reply(responderEvidenceChannel, channelRoot, "human free-form response "+token+" "+secret, responderEvidenceOwnerID)
	tick("free_person_only", 5, 8)
	grammarEvidenceRequireAnswers(t, port, "free", "human free-form response [REDACTED] [REDACTED]")
	remove("free")

	gate := ports.SlackPendingInput{
		Kind: ports.SlackPendingGate, FeatureID: answerGrammarEvidenceID,
		GatePath: "/state/gate.yaml", Iteration: 1,
		WaitingSince: time.Date(2026, 9, 22, 18, 0, 0, 0, time.UTC),
		GateSummary:  "Human verification required",
	}
	post(gate)
	reply(responderEvidenceChannel, channelRoot, "acknowledge verification gate", responderEvidenceOwnerID)
	tick("gate_not_answerable", 5, 9)
	if !grammarEvidencePending(responderEvidenceRecord(t, h), pendingInputIdentity(gate)) {
		t.Fatal("gate reply retired informational gate")
	}

	help := ports.SlackPendingInput{
		Kind: ports.SlackPendingHelp, FeatureID: answerGrammarEvidenceID,
		HelpQuestion: "What deployment stage is safe?",
		WaitingSince: time.Date(2026, 9, 22, 18, 1, 0, 0, time.UTC),
	}
	helpItem := post(help)[0]
	secondHelp := help
	secondHelp.HelpQuestion = "Which team should own the follow-up?"
	secondHelp.WaitingSince = secondHelp.WaitingSince.Add(time.Minute)
	secondHelpItem := post(secondHelp)[0]
	reply(responderEvidenceChannel, channelRoot, helpItem.Tag+" use staging environment", responderEvidenceOwnerID)
	tick("help_exact_entry", 6, 10)
	if port.snapshot()[5].EntryID != helpItem.Identity {
		t.Fatalf("help submission entry = %q; want %q", port.snapshot()[5].EntryID, helpItem.Identity)
	}
	if r := responderEvidenceRecord(t, h); !grammarEvidencePending(r, secondHelpItem.Identity) {
		t.Fatalf("second help entry %q disappeared after first was answered", secondHelpItem.Identity)
	}
	reply(responderEvidenceChannel, channelRoot, "#99 no such item", responderEvidenceOwnerID)
	tick("unknown_tag", 6, 11)

	pending = nil
	h.pending.set(answerGrammarEvidenceID)
	responderEvidenceSeedThreads(t, h, humans)
	responderEvidenceTickClock(t, clock)
	waitFor(t, 10*time.Second, func() bool {
		return len(responderEvidenceRecord(t, h).Pending) == 0 && n.queue.len() == 0
	})
	ticks = append(ticks, grammarEvidenceCapture(t, h, "retired"))
	polls := h.server.CallCount("conversations.replies")
	responderEvidenceTickClock(t, clock)
	if got := h.server.CallCount("conversations.replies"); got != polls {
		t.Fatalf("idle tick polls = %d; want %d", got, polls)
	}
	ticks = append(ticks, grammarEvidenceCapture(t, h, "idle_one"))
	responderEvidenceTickClock(t, clock)
	if got := h.server.CallCount("conversations.replies"); got != polls {
		t.Fatalf("second idle tick polls = %d; want %d", got, polls)
	}
	ticks = append(ticks, grammarEvidenceCapture(t, h, "idle_two"))
	if len(ticks) != 15 {
		t.Fatalf("evidence ticks = %d; want 15", len(ticks))
	}
	if got := len(h.observer.ofKind("slack.answer_received")); got != 7 {
		t.Errorf("answer_received events = %d; want 7", got)
	}
	if got := len(h.observer.ofKind("slack.answer_rejected")); got != 5 {
		t.Errorf("answer_rejected events = %d; want 5", got)
	}
	if got := len(port.snapshot()); got != 6 {
		t.Errorf("answer-port submissions = %d; want 6", got)
	}
	grammarEvidenceWrite(t, h, port, ticks, seeded, token, secret)
}

func grammarEvidencePending(record featureRecord, identity string) bool {
	for _, item := range record.Pending {
		if item.Identity == identity && item.Resolution == nil {
			return true
		}
	}
	return false
}

func grammarEvidenceRequireLine(t *testing.T, h *notifierHarness, suffix string, count int) {
	t.Helper()
	got := 0
	for _, request := range h.server.Requests("chat.postMessage") {
		if strings.Contains(fieldString(request, "text"), suffix) {
			got++
		}
	}
	if got != count {
		t.Errorf("chat.postMessage containing %q = %d; want %d", suffix, got, count)
	}
}

func grammarEvidenceRequireAnswers(t *testing.T, p *grammarEvidencePort, requestID string, want ...string) {
	t.Helper()
	p.fakeSlackAnswerPort.mu.Lock()
	defer p.fakeSlackAnswerPort.mu.Unlock()
	for _, answer := range p.questionAnswers {
		if answer.RequestID != requestID {
			continue
		}
		if len(answer.Answers) != len(want) {
			t.Fatalf("%s answers = %d; want %d", requestID, len(answer.Answers), len(want))
		}
		for i, value := range want {
			if answer.Answers[i].Index != i || answer.Answers[i].Value != value ||
				answer.Source.Kind != ports.AnswerSourceSlack {
				t.Errorf("%s answer %d = %#v; want %q with Slack source", requestID, i, answer.Answers[i], value)
			}
		}
		return
	}
	t.Fatalf("no answer for %s", requestID)
}

func grammarEvidenceCapture(t *testing.T, h *notifierHarness, name string) grammarEvidenceTick {
	t.Helper()
	r := responderEvidenceRecord(t, h)
	base := captureResponderEvidenceTick(t, h, name)
	mapInputs := func(items []pendingInputRecord) []grammarEvidenceInput {
		result := make([]grammarEvidenceInput, 0, len(items))
		for _, item := range items {
			result = append(result, grammarEvidenceInput{
				Identity: item.Identity, Tag: item.Tag,
				Resolution: item.Resolution, Held: item.HeldAnswer != nil,
			})
		}
		return result
	}
	return grammarEvidenceTick{
		Name: name, Pending: mapInputs(r.Pending), Resolved: mapInputs(r.Resolved), Ledgers: base.Ledgers,
	}
}

func grammarEvidenceWrite(t *testing.T, h *notifierHarness, p *grammarEvidencePort, ticks []grammarEvidenceTick, seeded []string, secrets ...string) {
	t.Helper()
	events := append([]observe.Event(nil), h.observer.ofKind("slack.answer_received")...)
	events = append(events, h.observer.ofKind("slack.answer_rejected")...)
	transcript := struct {
		Requests    []responderEvidenceRequest  `json:"requests"`
		Submissions []grammarEvidenceSubmission `json:"submissions"`
		Ticks       []grammarEvidenceTick       `json:"ticks"`
		Events      []observe.Event             `json:"events"`
		FinalRecord featureRecord               `json:"final_record"`
	}{
		responderEvidenceRequests(h.server.AllRequests()), p.snapshot(), ticks,
		events, responderEvidenceRecord(t, h),
	}
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(transcript); err != nil {
		t.Fatal(err)
	}
	encoded := output.Bytes()
	for _, secret := range secrets {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("answer grammar transcript contains secret %q", secret)
		}
	}
	for _, text := range seeded {
		quoted, err := json.Marshal(text)
		if err != nil {
			t.Fatal(err)
		}
		// Single-character replies can occur in timestamps or counts; longer
		// authored replies must not occur even inside another field.
		if bytes.Contains(encoded, quoted) || (len(text) > 8 && bytes.Contains(encoded, []byte(text))) {
			t.Fatalf("answer grammar transcript contains seeded reply %q", text)
		}
	}
	for _, marker := range []string{
		"1 of 2", "Broad scope", "Still waiting on", "is answered in Agentico",
		"option_selected", "options_selected", "free_text", "help_sent",
	} {
		if !bytes.Contains(encoded, []byte(marker)) {
			t.Errorf("answer grammar transcript missing %q", marker)
		}
	}
	dir := strings.TrimSpace(os.Getenv("AGENTICO_EVIDENCE_DIR"))
	if dir == "" {
		return
	}
	target := filepath.Join(dir, "behaviors")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "slack-answer-grammar.json"), encoded, 0o644); err != nil {
		t.Fatal(err)
	}
}
