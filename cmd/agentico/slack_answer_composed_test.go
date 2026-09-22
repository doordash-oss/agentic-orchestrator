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
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	serverruntime "github.com/doordash-oss/agentic-orchestrator/internal/server"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
)

func TestSlackAnswerPortPermissionOutcomesUseRealSession(t *testing.T) {
	const (
		featureID = "slack-answer-outcomes"
		sessionID = "slack-answer-outcomes-implement"
	)
	eventCh := make(chan interface{}, 16)
	sessions := session.NewManager(eventCh)
	t.Cleanup(sessions.Shutdown)
	resultPath := filepath.Join(t.TempDir(), "responses.txt")
	script := writeSlackPermissionOutcomeProvider(t, resultPath)
	sess, err := sessions.StartSession(
		sessionID,
		featureID,
		feature.PhaseImplement,
		[]string{"bash", script},
		filepath.Dir(script),
		nil,
		&session.SessionOpts{ProviderName: "scripted"},
	)
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	target := &serverMutationTarget{sessions: sessions}
	var answerPort ports.SlackAnswerPort
	serverruntime.NewHandler(serverruntime.HandlerOptions{
		Sessions:              sessions,
		Mutations:             target,
		BindSlackAnswerPort:   func(port ports.SlackAnswerPort) { answerPort = port },
		DisableHostValidation: true,
	})
	source := ports.AnswerSource{Kind: ports.AnswerSourceSlack, Responder: "Ada"}

	waitForComposedNeedsInput(t, 5*time.Second, func() bool {
		return hasPendingRequest(sess, "permission-allow")
	})
	allow := answerPort.AnswerSlackPermission(ports.SlackPermissionAnswer{
		RequestID:       "permission-allow",
		SourceFeatureID: featureID,
		Decision:        ports.SlackPermissionAllowOnce,
		Source:          source,
	})
	if allow.Outcome != ports.SlackAnswerAccepted {
		t.Fatalf("allow result = %+v; want accepted", allow)
	}
	waitForComposedNeedsInput(t, 5*time.Second, func() bool {
		return hasPendingRequest(sess, "permission-deny")
	})
	deny := answerPort.AnswerSlackPermission(ports.SlackPermissionAnswer{
		RequestID:       "permission-deny",
		SourceFeatureID: featureID,
		Decision:        ports.SlackPermissionDeny,
		Source:          source,
	})
	if deny.Outcome != ports.SlackAnswerAccepted {
		t.Fatalf("deny result = %+v; want accepted", deny)
	}
	waitForComposedNeedsInput(t, 5*time.Second, func() bool {
		return hasPendingRequest(sess, "ask-user")
	})

	for _, answer := range []ports.SlackPermissionAnswer{
		{RequestID: "permission-allow", SourceFeatureID: featureID, Decision: ports.SlackPermissionAllowOnce, Source: source},
		{RequestID: "missing", SourceFeatureID: featureID, Decision: ports.SlackPermissionAllowOnce, Source: source},
	} {
		if got := answerPort.AnswerSlackPermission(answer); got.Outcome != ports.SlackAnswerNoLongerPending {
			t.Fatalf("resolved/unknown result = %+v; want no longer pending", got)
		}
	}
	if got := answerPort.AnswerSlackPermission(ports.SlackPermissionAnswer{
		RequestID:       "ask-user",
		SourceFeatureID: featureID,
		Decision:        ports.SlackPermissionAllowOnce,
		Source:          source,
	}); got.Outcome != ports.SlackAnswerFailed {
		t.Fatalf("ask-user result = %+v; want failed", got)
	}
	if got := answerPort.AnswerSlackPermission(ports.SlackPermissionAnswer{
		RequestID:       "permission-deny",
		SourceFeatureID: featureID,
		Decision:        ports.SlackPermissionDecision("allow_remember"),
		Source:          source,
	}); got.Outcome != ports.SlackAnswerFailed {
		t.Fatalf("unsupported decision result = %+v; want failed", got)
	}

	waitForComposedNeedsInput(t, 5*time.Second, func() bool {
		raw, readErr := os.ReadFile(resultPath)
		return readErr == nil && len(strings.Split(strings.TrimSpace(string(raw)), "\n")) == 2
	})
	raw, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatalf("read provider responses: %v", err)
	}
	if !bytes.Contains(raw, []byte(`"behavior":"allow"`)) ||
		!bytes.Contains(raw, []byte(`"behavior":"deny"`)) {
		t.Fatalf("provider responses = %s; want allow and deny", raw)
	}
}

func TestSlackAnswerPortPermissionRaceWithRESTUsesRealSession(t *testing.T) {
	const (
		featureID = "slack-answer-race"
		sessionID = "slack-answer-race-implement"
		requestID = "permission-race"
	)

	for attempt := 0; attempt < 12; attempt++ {
		eventCh := make(chan interface{}, 16)
		sessions := session.NewManager(eventCh)
		resultPath := filepath.Join(t.TempDir(), "responses.txt")
		script := writeSlackPermissionRaceProvider(t, requestID, resultPath)
		sess, err := sessions.StartSession(
			sessionID,
			featureID,
			feature.PhaseImplement,
			[]string{"bash", script},
			filepath.Dir(script),
			nil,
			&session.SessionOpts{ProviderName: "scripted"},
		)
		if err != nil {
			sessions.Shutdown()
			t.Fatalf("StartSession() error = %v", err)
		}
		waitForComposedNeedsInput(t, 5*time.Second, func() bool {
			return hasPendingRequest(sess, requestID)
		})

		target := &serverMutationTarget{sessions: sessions}
		var answerPort ports.SlackAnswerPort
		handler := serverruntime.NewHandler(serverruntime.HandlerOptions{
			Sessions:              sessions,
			Mutations:             target,
			BindSlackAnswerPort:   func(port ports.SlackAnswerPort) { answerPort = port },
			DisableHostValidation: true,
		})
		if answerPort == nil {
			sessions.Shutdown()
			t.Fatal("real Slack answer port was not bound")
		}

		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		var slackResult ports.SlackAnswerResult
		var restStatus int
		var restBody []byte
		go func() {
			defer wg.Done()
			<-start
			slackResult = answerPort.AnswerSlackPermission(ports.SlackPermissionAnswer{
				RequestID:       requestID,
				SourceFeatureID: featureID,
				Decision:        ports.SlackPermissionAllowOnce,
				Source: ports.AnswerSource{
					Kind:      ports.AnswerSourceSlack,
					Responder: "Ada",
				},
			})
		}()
		go func() {
			defer wg.Done()
			<-start
			restStatus, restBody = postPermissionAnswer(t, handler, requestID)
		}()
		close(start)
		wg.Wait()

		select {
		case <-sess.Done():
		case <-time.After(5 * time.Second):
			sessions.Shutdown()
			t.Fatal("timed out waiting for scripted permission session")
		}
		sessions.Shutdown()

		responses, err := os.ReadFile(resultPath)
		if err != nil {
			t.Fatalf("read provider responses: %v", err)
		}
		lines := strings.Split(strings.TrimSpace(string(responses)), "\n")
		if len(lines) != 1 {
			t.Fatalf(
				"attempt %d provider response count = %d (%q), Slack result = %+v, REST status/body = %d/%s; want exactly one",
				attempt,
				len(lines),
				responses,
				slackResult,
				restStatus,
				restBody,
			)
		}

		slackWon := slackResult.Outcome == ports.SlackAnswerAccepted
		restWon := restStatus == http.StatusOK
		if slackWon == restWon {
			t.Fatalf(
				"attempt %d Slack result = %+v, REST status/body = %d/%s; want exactly one winner",
				attempt,
				slackResult,
				restStatus,
				restBody,
			)
		}
		if !slackWon && slackResult.Outcome != ports.SlackAnswerNoLongerPending {
			t.Fatalf("attempt %d Slack loser = %+v; want no longer pending", attempt, slackResult)
		}
		if !restWon {
			if restStatus != http.StatusConflict {
				t.Fatalf("attempt %d REST loser status = %d; want 409", attempt, restStatus)
			}
			var response serverruntime.ErrorResponse
			if err := json.Unmarshal(restBody, &response); err != nil {
				t.Fatalf("decode REST loser: %v", err)
			}
			if response.Error.Code != string(errcat.NoLongerPending) {
				t.Fatalf("attempt %d REST loser code = %q; want %q", attempt, response.Error.Code, errcat.NoLongerPending)
			}
		}
	}
}

func TestSlackAnswerPortReviewUsesRealSessionServiceAndOrchestrator(t *testing.T) {
	const featureID = "slack-review-composed"
	runtimeDir := t.TempDir()
	cfg := config.NewDefault()
	repoPath := filepath.Join(runtimeDir, "repo")
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		t.Fatalf("create repo path: %v", err)
	}
	cfg.Repos["repo"] = config.RepoConfig{Path: repoPath}
	store := feature.NewStore(filepath.Join(runtimeDir, "features"))
	manager := feature.NewManager(store, cfg)
	targetPhase := feature.PhaseImplement
	planPath := filepath.Join(store.RunDir(featureID, 1), "plan", "plan.md")
	if err := os.MkdirAll(filepath.Dir(planPath), 0o755); err != nil {
		t.Fatalf("create plan dir: %v", err)
	}
	if err := os.WriteFile(planPath, []byte("# Plan\n\n## Tasks\n\n### Task 1: Ship\n\n**Repo:** repo\n"), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	f := &feature.Feature{
		ID:                 featureID,
		Name:               "Slack review composed",
		Slug:               featureID,
		Status:             feature.StatusPlanNeedsReview,
		CurrentPhase:       feature.PhasePlan,
		PendingReviewPhase: &targetPhase,
		ActiveRun:          1,
		RunCount:           1,
		Pipeline:           feature.PipelineMedium,
		SchemaVersion:      feature.SchemaVersionCurrent,
		Repos:              []feature.FeatureRepo{{Name: "repo", Path: repoPath}},
		Artifacts:          map[string]string{"plan": planPath},
	}
	f.SetRun(&feature.Run{
		RunNumber:          1,
		PendingReviewPhase: &targetPhase,
		Artifacts:          f.Artifacts,
	})
	if err := store.Save(f); err != nil {
		t.Fatalf("save review feature: %v", err)
	}

	implementationResults := make(chan *agent.OrchestratorResult)
	var dispatchedFeature, dispatchedPlan string
	orch := orchestrator.New(orchestrator.Deps{Lifecycle: manager, Store: store}, orchestrator.Hooks{})
	orch.SetRunMultiRepoImplFn(func(f *feature.Feature, planPath string, _ ...agent.KBInfo) (chan *agent.OrchestratorResult, error) {
		dispatchedFeature = f.ID
		dispatchedPlan = planPath
		return implementationResults, nil
	})
	t.Cleanup(func() { close(implementationResults) })

	mutations := &serverMutationTarget{orch: orch, store: store}
	var answerPort ports.SlackAnswerPort
	var pendingSource ports.SlackPendingInputSource
	serverruntime.NewHandler(serverruntime.HandlerOptions{
		Features:                    store,
		FeatureStore:                store,
		Mutations:                   mutations,
		BindSlackAnswerPort:         func(port ports.SlackAnswerPort) { answerPort = port },
		BindSlackPendingInputSource: func(source ports.SlackPendingInputSource) { pendingSource = source },
		DisableHostValidation:       true,
	})
	if answerPort == nil || pendingSource == nil {
		t.Fatalf("bound ports = answer:%T pending:%T; want both", answerPort, pendingSource)
	}
	pending, err := pendingSource.PendingSlackInputs(featureID)
	if err != nil {
		t.Fatalf("PendingSlackInputs() error = %v", err)
	}
	if len(pending) != 1 || pending[0].ReviewID == "" || pending[0].SourceRevision == "" {
		t.Fatalf("pending review = %+v; want one identified review", pending)
	}

	result := answerPort.ApproveSlackReview(ports.SlackReviewApproval{
		SourceFeatureID: featureID,
		ReviewID:        pending[0].ReviewID,
		SourceRevision:  pending[0].SourceRevision,
		Source: ports.AnswerSource{
			Kind:      ports.AnswerSourceSlack,
			Responder: "Ada",
		},
	})
	if result.Outcome != ports.SlackAnswerAccepted || result.Cause != nil {
		t.Fatalf("ApproveSlackReview() = %+v; want accepted", result)
	}
	if dispatchedFeature != featureID || dispatchedPlan != planPath {
		t.Fatalf("implementation dispatch = %q/%q; want %q/%q", dispatchedFeature, dispatchedPlan, featureID, planPath)
	}
	updated, err := store.Load(featureID)
	if err != nil {
		t.Fatalf("load reviewed feature: %v", err)
	}
	if updated.Status != feature.StatusImplementing || updated.PendingReviewPhase != nil {
		t.Fatalf("reviewed feature = status %s pending %v; want implementing with gate cleared", updated.Status, updated.PendingReviewPhase)
	}

	second := answerPort.ApproveSlackReview(ports.SlackReviewApproval{
		SourceFeatureID: featureID,
		ReviewID:        pending[0].ReviewID,
		SourceRevision:  pending[0].SourceRevision,
		Source:          ports.AnswerSource{Kind: ports.AnswerSourceSlack, Responder: "Ada"},
	})
	if second.Outcome != ports.SlackAnswerNoLongerPending {
		t.Fatalf("second ApproveSlackReview() = %+v; want no longer pending", second)
	}
}

func writeSlackPermissionOutcomeProvider(t *testing.T, resultPath string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "permission-outcome-provider.sh")
	script := fmt.Sprintf(`#!/usr/bin/env bash
printf '%%s\n' '{"type":"system","subtype":"init","session_id":"scripted","model":"test"}'
printf '%%s\n' '{"type":"control_request","request_id":"permission-allow","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"echo allow"}}}'
: > %q
while IFS= read -r response; do
  if [[ "$response" == *'"request_id":"permission-allow"'* ]]; then
    printf '%%s\n' "$response" >> %q
    break
  fi
done
printf '%%s\n' '{"type":"control_request","request_id":"permission-deny","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"echo deny"}}}'
while IFS= read -r response; do
  if [[ "$response" == *'"request_id":"permission-deny"'* ]]; then
    printf '%%s\n' "$response" >> %q
    break
  fi
done
printf '%%s\n' '{"type":"control_request","request_id":"ask-user","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","input":{"questions":[{"header":"Choice","question":"Continue?","options":[{"label":"Yes","description":"Continue."},{"label":"No","description":"Stop."}] }]}}}'
sleep 30
`, resultPath, resultPath, resultPath)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write permission outcome provider: %v", err)
	}
	return path
}

type blockingReviewMutationTarget struct {
	*serverMutationTarget
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (t *blockingReviewMutationTarget) ReviewDecision(featureID string, req serverruntime.ReviewDecisionRequest) error {
	t.once.Do(func() {
		close(t.entered)
		<-t.release
	})
	return t.serverMutationTarget.ReviewDecision(featureID, req)
}

func TestSlackAnswerPortReviewRaceWithRESTUsesRealOrchestrator(t *testing.T) {
	for _, slackFirst := range []bool{true, false} {
		name := "REST wins"
		if slackFirst {
			name = "Slack wins"
		}
		t.Run(name, func(t *testing.T) {
			const featureID = "slack-review-race"
			runtimeDir := t.TempDir()
			cfg := config.NewDefault()
			repoPath := filepath.Join(runtimeDir, "repo")
			if err := os.MkdirAll(repoPath, 0o755); err != nil {
				t.Fatalf("create repo path: %v", err)
			}
			cfg.Repos["repo"] = config.RepoConfig{Path: repoPath}
			store := feature.NewStore(filepath.Join(runtimeDir, "features"))
			manager := feature.NewManager(store, cfg)
			targetPhase := feature.PhaseImplement
			planPath := filepath.Join(store.RunDir(featureID, 1), "plan", "plan.md")
			if err := os.MkdirAll(filepath.Dir(planPath), 0o755); err != nil {
				t.Fatalf("create plan dir: %v", err)
			}
			if err := os.WriteFile(planPath, []byte("# Plan\n\n## Tasks\n\n### Task 1: Ship\n\n**Repo:** repo\n"), 0o644); err != nil {
				t.Fatalf("write plan: %v", err)
			}
			f := &feature.Feature{
				ID:                 featureID,
				Name:               "Slack review race",
				Slug:               featureID,
				Status:             feature.StatusPlanNeedsReview,
				CurrentPhase:       feature.PhasePlan,
				PendingReviewPhase: &targetPhase,
				ActiveRun:          1,
				RunCount:           1,
				Pipeline:           feature.PipelineMedium,
				SchemaVersion:      feature.SchemaVersionCurrent,
				Repos:              []feature.FeatureRepo{{Name: "repo", Path: repoPath}},
				Artifacts:          map[string]string{"plan": planPath},
			}
			f.SetRun(&feature.Run{
				RunNumber:          1,
				PendingReviewPhase: &targetPhase,
				Artifacts:          f.Artifacts,
			})
			if err := store.Save(f); err != nil {
				t.Fatalf("save review feature: %v", err)
			}

			implementationResults := make(chan *agent.OrchestratorResult)
			dispatches := 0
			orch := orchestrator.New(orchestrator.Deps{Lifecycle: manager, Store: store}, orchestrator.Hooks{})
			orch.SetRunMultiRepoImplFn(func(*feature.Feature, string, ...agent.KBInfo) (chan *agent.OrchestratorResult, error) {
				dispatches++
				return implementationResults, nil
			})
			t.Cleanup(func() { close(implementationResults) })

			target := &blockingReviewMutationTarget{
				serverMutationTarget: &serverMutationTarget{orch: orch, store: store},
				entered:              make(chan struct{}),
				release:              make(chan struct{}),
			}
			var answerPort ports.SlackAnswerPort
			var pendingSource ports.SlackPendingInputSource
			handler := serverruntime.NewHandler(serverruntime.HandlerOptions{
				Features:                    store,
				FeatureStore:                store,
				Mutations:                   target,
				BindSlackAnswerPort:         func(port ports.SlackAnswerPort) { answerPort = port },
				BindSlackPendingInputSource: func(source ports.SlackPendingInputSource) { pendingSource = source },
				DisableHostValidation:       true,
			})
			pending, err := pendingSource.PendingSlackInputs(featureID)
			if err != nil || len(pending) != 1 {
				t.Fatalf("PendingSlackInputs() = %+v, %v; want one review", pending, err)
			}
			if status, raw := createReviewSession(t, handler, featureID); status != http.StatusOK {
				t.Fatalf("create review session status/body = %d/%s; want 200", status, raw)
			}
			approval := ports.SlackReviewApproval{
				SourceFeatureID: featureID,
				ReviewID:        pending[0].ReviewID,
				SourceRevision:  pending[0].SourceRevision,
				Source:          ports.AnswerSource{Kind: ports.AnswerSourceSlack, Responder: "Ada"},
			}

			var slackResult ports.SlackAnswerResult
			var restStatus int
			var restBody []byte
			firstStarted := make(chan struct{})
			secondStarted := make(chan struct{})
			var wg sync.WaitGroup
			wg.Add(2)
			slackCall := func(started chan struct{}) {
				defer wg.Done()
				close(started)
				slackResult = answerPort.ApproveSlackReview(approval)
			}
			restCall := func(started chan struct{}) {
				defer wg.Done()
				close(started)
				restStatus, restBody = postReviewDecision(t, handler, featureID, approval)
			}
			if slackFirst {
				go slackCall(firstStarted)
			} else {
				go restCall(firstStarted)
			}
			<-firstStarted
			select {
			case <-target.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("first review decision did not reach mutation target")
			}
			if slackFirst {
				go restCall(secondStarted)
			} else {
				go slackCall(secondStarted)
			}
			<-secondStarted
			close(target.release)
			wg.Wait()

			if dispatches != 1 {
				t.Fatalf("review dispatches = %d; want exactly one", dispatches)
			}
			if slackFirst {
				if slackResult.Outcome != ports.SlackAnswerAccepted || restStatus != http.StatusConflict {
					t.Fatalf("Slack-first results = Slack %+v, REST %d/%s", slackResult, restStatus, restBody)
				}
				assertNoLongerPendingResponse(t, restBody)
			} else {
				if restStatus != http.StatusOK || slackResult.Outcome != ports.SlackAnswerNoLongerPending {
					t.Fatalf("REST-first results = REST %d/%s, Slack %+v", restStatus, restBody, slackResult)
				}
			}
		})
	}
}

func writeSlackPermissionRaceProvider(t *testing.T, requestID, resultPath string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "permission-race-provider.sh")
	script := fmt.Sprintf(`#!/usr/bin/env bash
printf '%%s\n' '{"type":"system","subtype":"init","session_id":"scripted","model":"test"}'
printf '%%s\n' '{"type":"control_request","request_id":%q,"request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"echo race"}}}'
: > %q
while IFS= read -r response; do
  if [[ "$response" == *'"request_id":%q'* ]]; then
    printf '%%s\n' "$response" >> %q
    break
  fi
done
if IFS= read -r -t 0.25 response && [[ "$response" == *'"request_id":%q'* ]]; then
  printf '%%s\n' "$response" >> %q
fi
printf '%%s\n' '{"type":"result","subtype":"success","session_id":"scripted","total_cost_usd":0}'
`, requestID, resultPath, requestID, resultPath, requestID, resultPath)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write permission race provider: %v", err)
	}
	return path
}

func createReviewSession(t *testing.T, handler http.Handler, featureID string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/features/"+featureID+"/reviews",
		bytes.NewReader([]byte("{}")),
	)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agentico-Client", "local")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	resp := rec.Result()
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read create review response: %v", err)
	}
	return resp.StatusCode, raw
}

func postReviewDecision(
	t *testing.T,
	handler http.Handler,
	featureID string,
	approval ports.SlackReviewApproval,
) (int, []byte) {
	t.Helper()
	body, err := json.Marshal(serverruntime.ReviewSessionDecisionRequest{
		Decision:     "proceed",
		BaseRevision: approval.SourceRevision,
	})
	if err != nil {
		t.Fatalf("marshal review decision: %v", err)
	}
	path := "/api/v1/features/" + featureID + "/reviews/" + approval.ReviewID + "/decision"
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agentico-Client", "local")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	resp := rec.Result()
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read review response: %v", err)
	}
	return resp.StatusCode, raw
}

func assertNoLongerPendingResponse(t *testing.T, raw []byte) {
	t.Helper()
	var response serverruntime.ErrorResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatalf("decode conflict response: %v", err)
	}
	if response.Error.Code != string(errcat.NoLongerPending) {
		t.Fatalf("conflict code = %q; want %q", response.Error.Code, errcat.NoLongerPending)
	}
}

func postPermissionAnswer(t *testing.T, handler http.Handler, requestID string) (int, []byte) {
	t.Helper()
	body, err := json.Marshal(serverruntime.PermissionAnswerRequest{
		RequestID: requestID,
		Decision:  permission.DecisionAllowOnce,
	})
	if err != nil {
		t.Fatalf("marshal permission answer: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/permissions/answer", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agentico-Client", "local")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	resp := rec.Result()
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read permission response: %v", err)
	}
	return resp.StatusCode, raw
}
