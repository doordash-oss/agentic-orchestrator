package slack

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/slack/testsupport"
)

func TestSlackAnswerGrammarQuestionParsing(t *testing.T) {
	single := ports.SlackPendingInput{Options: []ports.SlackPendingInputOption{
		{Label: "Focused"}, {Label: "Broad scope"}, {Label: "Other"},
	}}
	for _, tc := range []struct {
		text, answer, decision, hint string
	}{
		{"2", "Broad scope", "option_selected", ""},
		{" BROAD SCOPE!! ", "Broad scope", "option_selected", ""},
		{"none of those", "none of those", "free_text", ""},
		{"7", "", "", "range"},
		{"0", "", "", "range"},
	} {
		got := parseQuestionAnswer(single, tc.text)
		if got.answer != tc.answer || got.decision != tc.decision ||
			(tc.hint != "" && got.hint == "") {
			t.Errorf("single %q = %+v; want %q %q hint %q", tc.text, got, tc.answer, tc.decision, tc.hint)
		}
	}
	multi := single
	multi.MultiSelect = true
	for _, tc := range []struct {
		text     string
		answer   string
		decision string
	}{
		{"1, 2", "Focused, Broad scope", "options_selected"},
		{"2 1 2", "Focused, Broad scope", "options_selected"},
		{"Broad scope, Focused", "Focused, Broad scope", "options_selected"},
		{"1 and maybe 2", "", ""},
	} {
		got := parseQuestionAnswer(multi, tc.text)
		if got.answer != tc.answer || got.decision != tc.decision {
			t.Errorf("multi %q = %+v; want %q %q", tc.text, got, tc.answer, tc.decision)
		}
	}
	if got := parseQuestionAnswer(ports.SlackPendingInput{}, "unstructured answer"); got.answer != "unstructured answer" || got.decision != "free_text" {
		t.Errorf("free text = %+v", got)
	}
	if got := parseQuestionAnswer(single, "x"+string(make([]byte, sectionTextLimit))); got.hint == "" {
		t.Errorf("overlong answer accepted: %+v", got)
	}
	if got := keycapOption("two"); got != 2 {
		t.Errorf("keycap two = %d", got)
	}
}

func TestSlackAnswerGrammarRedactionHeldAndSubmitted(t *testing.T) {
	const (
		token  = "xoxb-admission"
		secret = "xoxb-secondary-credential"
	)
	h, n, port := newPermissionResponderAdmissionFixture(t, nil, nil)
	n.clock.(*gatedDeliveryClock).open()
	h.server.Script("users.info", testsupport.Response{Body: map[string]any{
		"ok": true, "user": map[string]any{"id": "U-SENSITIVE", "profile": map[string]any{
			"display_name": "Ada " + token + " " + secret,
		}},
	}})
	record := n.records["feature-1"]
	record.Pending = []pendingInputRecord{
		{Identity: "question:redact:0", SourceFeatureID: "feature-1", Kind: "question", RequestID: "redact", Tag: "#1", MessageTS: map[string]string{"channel:C-ENG": "100.000002"}},
		{Identity: "question:redact:1", SourceFeatureID: "feature-1", Kind: "question", RequestID: "redact", QuestionIndex: 1, Tag: "#2", MessageTS: map[string]string{"channel:C-ENG": "100.000003"}},
	}
	d := record.Destinations["channel:C-ENG"]
	d.PostingIndex[0].Identity = record.Pending[0].Identity
	d.PostingIndex = append(d.PostingIndex, postingIndexEntry{Identity: record.Pending[1].Identity, MessageTS: "100.000003", Tag: "#2"})
	d.Ledger = append(d.Ledger, "100.000003")
	record.Destinations["channel:C-ENG"] = d
	h.pending.set("feature-1",
		ports.SlackPendingInput{Kind: ports.SlackPendingQuestion, RequestID: "redact", QuestionCount: 2,
			Options: []ports.SlackPendingInputOption{{Label: "Focused " + token + " " + secret}}},
		ports.SlackPendingInput{Kind: ports.SlackPendingQuestion, RequestID: "redact", QuestionIndex: 1, QuestionCount: 2},
	)
	h.server.SeedThread("C-ENG", "100.000001", []testsupport.Message{
		{TS: "100.000001"}, {TS: "100.000002", ThreadTS: "100.000001"},
		{TS: "100.000003", ThreadTS: "100.000001"},
		{TS: "100.000004", ThreadTS: "100.000001", User: "U-ADA", Text: "#1 1"},
	})
	n.responderTick()
	data, err := os.ReadFile(recordPath(h.stateDir, "feature-1"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), token) || strings.Contains(string(data), secret) {
		t.Fatal("held answer leaked credentials")
	}
	if record.Pending[0].HeldAnswer == nil ||
		record.Pending[0].HeldAnswer.Value != "Focused [REDACTED] [REDACTED]" {
		t.Fatalf("held answer = %+v", record.Pending[0].HeldAnswer)
	}
	waitFor(t, time.Second, func() bool { return h.server.CallCount("chat.postMessage") >= 1 })
	h.server.SeedThread("C-ENG", "100.000001", []testsupport.Message{
		{TS: "100.000001"}, {TS: "100.000002", ThreadTS: "100.000001"},
		{TS: "100.000003", ThreadTS: "100.000001"},
		{TS: "100.000004", ThreadTS: "100.000001", User: "U-ADA", Text: "#1 1"},
		{TS: "100.000005", ThreadTS: "100.000001", User: "U-SENSITIVE", Text: "#2 hello " + token + " and " + secret},
	})
	n.responderTick()
	port.mu.Lock()
	defer port.mu.Unlock()
	if len(port.questionAnswers) != 1 ||
		port.questionAnswers[0].Answers[0].Value != "Focused [REDACTED] [REDACTED]" ||
		port.questionAnswers[0].Answers[1].Value != "hello [REDACTED] and [REDACTED]" ||
		!strings.Contains(port.questionAnswers[0].Source.Responder, "Ada [REDACTED] [REDACTED]") ||
		strings.Contains(port.questionAnswers[0].Source.Responder, token) ||
		strings.Contains(port.questionAnswers[0].Source.Responder, secret) {
		t.Fatalf("submitted answer = %+v", port.questionAnswers)
	}
	for _, request := range h.server.Requests("chat.postMessage") {
		if strings.Contains(fieldString(request, "text"), token) ||
			strings.Contains(fieldString(request, "text"), secret) {
			t.Fatal("outbound Slack line leaked credentials")
		}
	}
	if record.Pending[0].HeldAnswer != nil || record.Pending[1].HeldAnswer != nil {
		t.Fatal("held answers survived submission")
	}
}

func TestSlackAnswerGrammarBundleAndHelp(t *testing.T) {
	const root = "100.000001"
	h, n, port := newPermissionResponderAdmissionFixture(t, nil, nil)
	n.clock.(*gatedDeliveryClock).open()
	record := n.records["feature-1"]
	record.Pending = []pendingInputRecord{
		{Identity: "question:ask:0", SourceFeatureID: "feature-1", Kind: "question", RequestID: "ask", Tag: "#1", MessageTS: map[string]string{"channel:C-ENG": "100.000002"}},
		{Identity: "question:ask:1", SourceFeatureID: "feature-1", Kind: "question", RequestID: "ask", QuestionIndex: 1, Tag: "#2", MessageTS: map[string]string{"channel:C-ENG": "100.000003"}},
	}
	d := record.Destinations["channel:C-ENG"]
	d.PostingIndex[0].Identity = "question:ask:0"
	d.PostingIndex = append(d.PostingIndex, postingIndexEntry{Identity: "question:ask:1", Tag: "#2", MessageTS: "100.000003"})
	d.Ledger = append(d.Ledger, "100.000003")
	record.Destinations["channel:C-ENG"] = d
	h.pending.set("feature-1",
		ports.SlackPendingInput{Kind: ports.SlackPendingQuestion, RequestID: "ask", QuestionCount: 2, Options: []ports.SlackPendingInputOption{{Label: "Focused"}, {Label: "Broad scope"}}},
		ports.SlackPendingInput{Kind: ports.SlackPendingQuestion, RequestID: "ask", QuestionCount: 2, QuestionIndex: 1},
	)
	h.server.SeedThread("C-ENG", root, []testsupport.Message{
		{TS: root}, {TS: "100.000002", ThreadTS: root, Text: "question 1"},
		{TS: "100.000003", ThreadTS: root, Text: "question 2"},
		{TS: "100.000004", ThreadTS: root, User: "U-ADA", Text: "#1 2"},
	})
	n.responderTick()
	port.mu.Lock()
	if len(port.questionAnswers) != 0 {
		t.Fatalf("partial bundle submitted: %+v", port.questionAnswers)
	}
	port.mu.Unlock()
	if record.Pending[0].HeldAnswer == nil || record.Pending[0].HeldAnswer.Value != "Broad scope" {
		t.Fatalf("held answer = %+v", record.Pending[0].HeldAnswer)
	}
	waitFor(t, time.Second, func() bool {
		return h.server.CallCount("chat.postMessage") >= 1 && h.server.CallCount("reactions.add") >= 1
	})
	h.server.SeedThread("C-ENG", root, []testsupport.Message{
		{TS: root}, {TS: "100.000002", ThreadTS: root, Text: "question 1"},
		{TS: "100.000003", ThreadTS: root, Text: "question 2"},
		{TS: "100.000004", ThreadTS: root, User: "U-ADA", Text: "#1 2"},
		{TS: "100.000005", ThreadTS: root, User: "U-ADA", Text: "#2 answer two"},
	})
	n.responderTick()
	port.mu.Lock()
	submissions := append([]ports.SlackQuestionAnswer(nil), port.questionAnswers...)
	port.mu.Unlock()
	if len(submissions) != 1 || !reflect.DeepEqual(submissions[0].Answers,
		[]ports.SlackQuestionIndexedAnswer{{Index: 0, Value: "Broad scope"}, {Index: 1, Value: "answer two"}}) {
		t.Fatalf("bundle submissions = %+v", submissions)
	}
	if record.Pending[0].HeldAnswer != nil || record.Pending[1].HeldAnswer != nil {
		t.Fatalf("held answers after submission: %+v", record.Pending)
	}
	waitFor(t, time.Second, func() bool {
		return h.server.CallCount("chat.postMessage") >= 3 &&
			h.server.CallCount("reactions.add") >= 2 &&
			n.queue.len() == 0
	})
	help := ports.SlackPendingInput{Kind: ports.SlackPendingHelp, WaitingSince: time.Now(), HelpQuestion: "Need help"}
	help.FeatureID = "feature-1"
	identity := pendingInputIdentity(help)
	record.Pending = []pendingInputRecord{{Identity: identity, SourceFeatureID: "feature-1", Kind: "help",
		Tag: "#3", MessageTS: map[string]string{"channel:C-ENG": "100.000006"}}}
	d = record.Destinations["channel:C-ENG"]
	d.PostingIndex = append(d.PostingIndex, postingIndexEntry{Identity: identity, Tag: "#3", MessageTS: "100.000006"})
	d.Ledger = append(d.Ledger, "100.000006")
	record.Destinations["channel:C-ENG"] = d
	h.pending.set("feature-1", help)
	h.server.SeedThread("C-ENG", root, []testsupport.Message{
		{TS: root}, {TS: "100.000006", ThreadTS: root, Text: "help"},
		{TS: "100.000007", ThreadTS: root, User: "U-ADA", Text: "use staging"},
	})
	n.responderTick()
	port.mu.Lock()
	defer port.mu.Unlock()
	if len(port.helpAnswers) != 1 || port.helpAnswers[0].EntryIdentity != identity ||
		port.helpAnswers[0].Text != "use staging" {
		t.Fatalf("help submission = %+v", port.helpAnswers)
	}
}

func TestSlackAnswerGrammarTags(t *testing.T) {
	postings := []postingIndexEntry{{Tag: "#1", MessageTS: "100.000001"}, {Tag: "#12", MessageTS: "100.000004"}}
	for _, tc := range []struct {
		text, ts, tag, answer string
		found                 bool
	}{
		{"#1 allow", "100.000003", "#1", "allow", true},
		{"#1: allow", "100.000003", "#1", "allow", true},
		{"#1. allow", "100.000003", "#1", "allow", true},
		{"#1 - allow", "100.000003", "#1", "allow", true},
		{"#12 yes", "100.000003", "", "yes", false},
		{"#9 yes", "100.000003", "", "yes", false},
		{"#1", "100.000003", "#1", "", false},
	} {
		tag, answer, tagged := splitResponderTag(tc.text)
		if !tagged || answer != tc.answer {
			t.Errorf("split %q = %q %q %t", tc.text, tag, answer, tagged)
			continue
		}
		_, found := taggedPostingBefore(postings, tag, tc.ts)
		if found && answer == "" {
			found = false
		}
		if found != tc.found || (tc.tag != "" && tag != tc.tag) {
			t.Errorf("target %q at %q = %q %t; want %q %t", tc.text, tc.ts, tag, found, tc.tag, tc.found)
		}
	}
	if tag, _, tagged := splitResponderTag("please #1 allow"); tagged || tag != "" {
		t.Errorf("middle tag recognized: %q", tag)
	}
	if got := pendingResponderTags([]pendingInputRecord{{Tag: "#3"}, {Tag: "#1", Resolution: &postingResolution{}}, {Tag: "#2"}}); !reflect.DeepEqual(got, []string{"#2", "#3"}) {
		t.Errorf("pending tags = %v", got)
	}
}

func TestSlackAnswerGrammarHeldAnswerRoundTrip(t *testing.T) {
	dir := t.TempDir()
	record := &featureRecord{Version: recordVersion, Destinations: map[string]destinationRecord{},
		Pending: []pendingInputRecord{{
			Identity: "question:ask:0", Kind: "question", RequestID: "ask",
			HeldAnswer: &heldQuestionAnswer{Value: "Focused", Decision: "option_selected",
				ResponderID: "U-ADA", ResponderName: "Ada"},
		}}}
	if err := persistFeatureRecord(dir, "feature-1", record); err != nil {
		t.Fatal(err)
	}
	reloaded, err := loadFeatureRecord(dir, "feature-1")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(record.Pending[0].HeldAnswer, reloaded.Pending[0].HeldAnswer) {
		t.Fatalf("held answer after restart = %+v; want %+v", reloaded.Pending[0].HeldAnswer, record.Pending[0].HeldAnswer)
	}
	legacy := &featureRecord{Version: recordVersion, Destinations: map[string]destinationRecord{},
		Pending: []pendingInputRecord{{Identity: "question:old:0", Kind: "question"}}}
	if err := persistFeatureRecord(dir, "feature-1", legacy); err != nil {
		t.Fatal(err)
	}
	reloaded, err = loadFeatureRecord(dir, "feature-1")
	if err != nil || reloaded.Pending[0].HeldAnswer != nil {
		t.Fatalf("old record load = %+v, %v", reloaded, err)
	}
}
