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

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	serverruntime "github.com/doordash-oss/agentic-orchestrator/internal/server"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
)

func TestSlackAnswerGrammarQuestionRealSessionAndRESTRace(t *testing.T) {
	const featureID = "slack-question-feature"
	const requestID = "slack-question-request"
	const original = "A question much longer than a display projection so that the original provider key remains intact?"
	for _, mode := range []string{"slack first", "REST first", "simultaneous"} {
		t.Run(mode, func(t *testing.T) {
			sessions := session.NewManager(make(chan interface{}, 16))
			t.Cleanup(sessions.Shutdown)
			resultPath := filepath.Join(t.TempDir(), "answers.jsonl")
			script := filepath.Join(t.TempDir(), "ask-user-provider.sh")
			input, _ := json.Marshal(map[string]any{"questions": []map[string]any{
				{"question": original}, {"question": "Second?"},
			}})
			provider := fmt.Sprintf(`#!/usr/bin/env bash
printf '%%s\n' '{"type":"system","subtype":"init","session_id":"scripted","model":"test"}'
printf '%%s\n' '{"type":"control_request","request_id":%q,"request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","input":%s}}'
: > %q
while IFS= read -r response; do
  if [[ "$response" == *'"request_id":%q'* ]]; then
    printf '%%s\n' "$response" >> %q
    break
  fi
done
printf '%%s\n' '{"type":"result","subtype":"success","session_id":"scripted","total_cost_usd":0}'
`, requestID, input, resultPath, requestID, resultPath)
			if err := os.WriteFile(script, []byte(provider), 0o755); err != nil {
				t.Fatal(err)
			}
			sess, err := sessions.StartSession("slack-question-session", featureID, feature.PhaseImplement,
				[]string{"bash", script}, filepath.Dir(script), nil, &session.SessionOpts{ProviderName: "scripted"})
			if err != nil {
				t.Fatal(err)
			}
			waitForComposedNeedsInput(t, 5*time.Second, func() bool { return hasPendingRequest(sess, requestID) })
			target := &serverMutationTarget{sessions: sessions}
			var port ports.SlackAnswerPort
			handler := serverruntime.NewHandler(serverruntime.HandlerOptions{
				Sessions: sessions, Mutations: target, DisableHostValidation: true,
				BindSlackAnswerPort: func(p ports.SlackAnswerPort) { port = p },
			})
			answer := ports.SlackQuestionAnswer{SourceFeatureID: featureID, RequestID: requestID,
				Answers: []ports.SlackQuestionIndexedAnswer{{Index: 0, Value: "Focused"}, {Index: 1, Value: "Proceed"}},
				Source:  ports.AnswerSource{Kind: ports.AnswerSourceSlack, Responder: "Ada"}}
			restPayload, _ := json.Marshal(serverruntime.AskUserAnswerRequest{
				RequestID: requestID, Answers: map[string]string{original: "REST", "Second?": "REST"},
			})
			var slackResult ports.SlackAnswerResult
			var restStatus int
			var restBody []byte
			var wg sync.WaitGroup
			wg.Add(1)
			callSlack := func() {
				defer wg.Done()
				slackResult = port.AnswerSlackQuestion(answer)
			}
			callREST := func() {
				defer wg.Done()
				req := httptest.NewRequest(http.MethodPost, "/api/v1/prompts/ask-user/answer", bytes.NewReader(restPayload))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("X-Agentico-Client", "local")
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)
				restStatus, restBody = rec.Code, rec.Body.Bytes()
			}
			switch mode {
			case "slack first":
				go callSlack()
				wg.Wait()
				wg.Add(1)
				go callREST()
			case "REST first":
				go callREST()
				wg.Wait()
				wg.Add(1)
				go callSlack()
			default:
				wg.Add(1)
				go callREST()
				go callSlack()
			}
			wg.Wait()
			if mode == "slack first" {
				if slackResult.Outcome != ports.SlackAnswerAccepted || restStatus != http.StatusConflict {
					t.Fatalf("results: Slack %+v REST %d/%s", slackResult, restStatus, restBody)
				}
			} else if mode == "REST first" && (restStatus != http.StatusOK || slackResult.Outcome != ports.SlackAnswerNoLongerPending) {
				t.Fatalf("results: Slack %+v REST %d/%s", slackResult, restStatus, restBody)
			} else if mode == "simultaneous" &&
				!((slackResult.Outcome == ports.SlackAnswerAccepted && restStatus == http.StatusConflict) ||
					(slackResult.Outcome == ports.SlackAnswerNoLongerPending && restStatus == http.StatusOK)) {
				t.Fatalf("concurrent results: Slack %+v REST %d/%s", slackResult, restStatus, restBody)
			}
			select {
			case <-sess.Done():
			case <-time.After(5 * time.Second):
				t.Fatal("provider did not finish")
			}
			wire, err := os.ReadFile(resultPath)
			if err != nil || len(strings.Split(strings.TrimSpace(string(wire)), "\n")) != 1 {
				t.Fatalf("provider answers = %q, %v", wire, err)
			}
			qa := sess.QALog()
			if len(qa) != 2 || qa[0].Question != original || qa[1].Question != "Second?" {
				t.Fatalf("Q&A pairs = %+v", qa)
			}
			if slackResult.Outcome == ports.SlackAnswerAccepted {
				for _, pair := range qa {
					if pair.Source == nil || pair.Source.Kind != ports.AnswerSourceSlack ||
						pair.Source.Responder != "Ada" {
						t.Fatalf("missing Slack provenance: %+v", pair)
					}
				}
			}
		})
	}
}

func TestSlackAnswerGrammarHelpExactQueueAndConcurrentReplies(t *testing.T) {
	store, _, f := newMutationTestFeature(t, "help exact entry", feature.CreateOptions{}, feature.StatusImplementing, feature.PhaseImplement)
	now := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	if err := store.Modify(f.ID, func(current *feature.Feature) error {
		current.HelpQueue = []feature.HelpRequest{
			{Question: "First?", Time: now, Pending: true},
			{Question: "Second?", Time: now.Add(time.Second), Pending: true},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	active := &mutationTargetSessionView{id: "live-session", featureID: f.ID, active: true}
	target := &serverMutationTarget{
		store: store, sessions: &mutationTargetSessionManager{sessions: []ports.SessionView{active}},
	}
	var port ports.SlackAnswerPort
	serverruntime.NewHandler(serverruntime.HandlerOptions{
		FeatureStore: store, Mutations: target,
		BindSlackAnswerPort: func(p ports.SlackAnswerPort) { port = p },
	})
	source := ports.AnswerSource{Kind: ports.AnswerSourceSlack, Responder: "Ada"}
	second := ports.SlackHelpAnswer{SourceFeatureID: f.ID,
		EntryIdentity: ports.SlackHelpEntryIdentity(f.ID, now.Add(time.Second), "Second?"),
		Text:          "Use staging", Source: source}
	if got := port.AnswerSlackHelp(second); got.Outcome != ports.SlackAnswerAccepted {
		t.Fatalf("second entry = %+v", got)
	}
	loaded, err := store.Load(f.ID)
	if err != nil || !loaded.HelpQueue[0].Pending || loaded.HelpQueue[1].Pending ||
		loaded.HelpQueue[1].Answer != second.Text {
		t.Fatalf("queue after second answer = %+v, %v", loaded.HelpQueue, err)
	}
	if len(active.sentMessages) != 0 {
		t.Fatalf("exact help answer sent to active session: %+v", active.sentMessages)
	}
	if got := port.AnswerSlackHelp(second); got.Outcome != ports.SlackAnswerNoLongerPending {
		t.Fatalf("repeated entry = %+v", got)
	}
	missing := second
	missing.EntryIdentity = "help:missing"
	if got := port.AnswerSlackHelp(missing); got.Outcome != ports.SlackAnswerNoLongerPending {
		t.Fatalf("unknown entry = %+v", got)
	}
	first := second
	first.EntryIdentity = ports.SlackHelpEntryIdentity(f.ID, now, "First?")
	var wg sync.WaitGroup
	results := make([]ports.SlackAnswerResult, 2)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = port.AnswerSlackHelp(first)
		}(i)
	}
	wg.Wait()
	counts := map[ports.SlackAnswerOutcome]int{}
	for _, result := range results {
		counts[result.Outcome]++
	}
	if counts[ports.SlackAnswerAccepted] != 1 || counts[ports.SlackAnswerNoLongerPending] != 1 {
		t.Fatalf("concurrent help outcomes = %+v", results)
	}
}
