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

package server

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

func TestPendingSlackInputsSplitsQuestionBundle(t *testing.T) {
	t.Parallel()

	session := &fakeSessionView{
		id:        "question-session",
		featureID: fixtureFeatureID,
		phase:     feature.PhasePlan,
		status:    ports.SessionWaitingHelp,
		pending: []*llm.ControlRequestMessage{pendingReadControl(
			"question-request",
			toolNameAskUserQuestion,
			`{"questions":[`+
				`{"header":"Scope","question":"Which scope?","options":[{"label":"Focused","description":"Smallest change","confidence":0.86}]},`+
				`{"header":"Gates","question":"Which gates?","multi_select":true,"options":[{"label":"Race","description":"Run race tests","confidence":0.31}]}`+
				`]}`,
		)},
	}
	h := &apiHandler{sessions: fakeSessionManager{views: []ports.SessionView{session}}}

	got, err := h.PendingSlackInputs(fixtureFeatureID)
	if err != nil {
		t.Fatalf("PendingSlackInputs() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("PendingSlackInputs() length = %d; want 2", len(got))
	}

	first := got[0]
	if first.Kind != ports.SlackPendingQuestion ||
		first.RequestID != "question-request" ||
		first.QuestionIndex != 0 ||
		first.QuestionCount != 2 ||
		first.Header != "Scope" ||
		first.Question != "Which scope?" ||
		first.MultiSelect {
		t.Errorf("PendingSlackInputs()[0] = %+v; want first single-select question", first)
	}
	if len(first.Options) != 1 ||
		first.Options[0].Label != "Focused" ||
		first.Options[0].Description != "Smallest change" ||
		!first.Options[0].HasConfidence ||
		first.Options[0].Confidence != 0.86 {
		t.Errorf("PendingSlackInputs()[0].Options = %+v; want projected option with confidence", first.Options)
	}

	second := got[1]
	if second.Kind != ports.SlackPendingQuestion ||
		second.RequestID != "question-request" ||
		second.QuestionIndex != 1 ||
		second.QuestionCount != 2 ||
		second.Header != "Gates" ||
		second.Question != "Which gates?" ||
		!second.MultiSelect {
		t.Errorf("PendingSlackInputs()[1] = %+v; want second multi-select question", second)
	}
}

func TestPendingSlackInputsProjectsPermissionContext(t *testing.T) {
	t.Parallel()

	waitingSince := time.Date(2026, 9, 22, 10, 30, 0, 0, time.UTC)
	request := pendingReadControl(
		"permission-request",
		toolNameBash,
		`{"command":"go test ./internal/server/...","timeout_ms":120000}`,
	)
	request.WaitingSince = waitingSince
	session := &fakeSessionView{
		id:             "permission-session",
		featureID:      fixtureFeatureID,
		phase:          feature.PhaseImplement,
		repoName:       repoNameSelf,
		permCacheScope: repoNameSelf,
		status:         ports.SessionWaitingPermission,
		pending:        []*llm.ControlRequestMessage{request},
	}
	h := &apiHandler{sessions: fakeSessionManager{views: []ports.SessionView{session}}}

	got, err := h.PendingSlackInputs(fixtureFeatureID)
	if err != nil {
		t.Fatalf("PendingSlackInputs() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("PendingSlackInputs() length = %d; want 1", len(got))
	}

	input := got[0]
	if input.Kind != ports.SlackPendingPermission ||
		input.RequestID != "permission-request" ||
		input.ToolName != toolNameBash ||
		input.Phase != feature.PhaseImplement.String() ||
		input.RepoName != repoNameSelf ||
		!input.WaitingSince.Equal(waitingSince) {
		t.Errorf("PendingSlackInputs()[0] = %+v; want permission context", input)
	}
	if gotCommand, ok := input.Input["command"].(string); !ok || gotCommand != "go test ./internal/server/..." {
		t.Errorf("PendingSlackInputs()[0].Input[command] = %#v; want exact command", input.Input["command"])
	}
	if gotTimeout, ok := input.Input["timeout_ms"].(float64); !ok || gotTimeout != 120000 {
		t.Errorf("PendingSlackInputs()[0].Input[timeout_ms] = %#v; want 120000", input.Input["timeout_ms"])
	}
	if input.RememberPattern == "" {
		t.Error("PendingSlackInputs()[0].RememberPattern is empty; want inferred preview")
	}
}

func TestPendingSlackInputsIncludesStoredHelpOnly(t *testing.T) {
	t.Parallel()

	store, f := seedReadFeature(t)
	storedAt := time.Date(2026, 9, 22, 11, 0, 0, 0, time.UTC)
	f.HelpQueue = []feature.HelpRequest{
		{Question: "How should this migration proceed?", Time: storedAt, Pending: true},
		{Question: "Already answered", Time: storedAt.Add(-time.Minute), Pending: false},
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	sessions := fakeSessionManager{views: []ports.SessionView{
		&fakeSessionView{
			id:           "coordinating-session",
			featureID:    f.ID,
			phase:        feature.PhaseImplement,
			kind:         ports.KindPhase,
			status:       ports.SessionWaitingHelp,
			waitingSince: storedAt.Add(time.Minute),
		},
		&fakeSessionView{
			id:           ChatSessionID,
			featureID:    f.ID,
			kind:         ports.KindChat,
			status:       ports.SessionWaitingHelp,
			waitingSince: storedAt.Add(2 * time.Minute),
		},
	}}
	h := &apiHandler{sessions: sessions, store: store}

	got, err := h.PendingSlackInputs(f.ID)
	if err != nil {
		t.Fatalf("PendingSlackInputs() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("PendingSlackInputs() = %+v; want only the pending stored help entry", got)
	}
	if got[0].Kind != ports.SlackPendingHelp ||
		got[0].HelpQuestion != "How should this migration proceed?" ||
		!got[0].WaitingSince.Equal(storedAt) {
		t.Errorf("PendingSlackInputs()[0] = %+v; want pending stored help entry", got[0])
	}
}

func TestPendingSlackInputsExcludesChatControlRequests(t *testing.T) {
	t.Parallel()

	chat := &fakeSessionView{
		id:        ChatSessionID,
		featureID: fixtureFeatureID,
		kind:      ports.KindChat,
		status:    ports.SessionWaitingPermission,
		pending: []*llm.ControlRequestMessage{
			pendingReadControl("chat-permission", toolNameBash, `{"command":"echo chat"}`),
			pendingReadControl("chat-question", toolNameAskUserQuestion, `{"questions":[{"question":"Chat question?"}]}`),
		},
	}
	h := &apiHandler{sessions: fakeSessionManager{views: []ports.SessionView{chat}}}

	got, err := h.PendingSlackInputs(fixtureFeatureID)
	if err != nil {
		t.Fatalf("PendingSlackInputs() error = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("PendingSlackInputs() = %+v; want no chat controls", got)
	}
}

func TestPendingSlackInputsProjectsVerificationGate(t *testing.T) {
	t.Parallel()

	store, f := seedReadFeature(t)
	waitingSince := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	gatePath := filepath.Join(store.RunDir(f.ID, 1), "phase-06", "implement", agent.NeedUserInputArtifactName)
	if err := agent.WriteNeedUserInputRecord(gatePath, agent.NeedUserInputRecord{
		Summary: "Authentication is required.",
		Questions: []agent.NeedUserInputQuestion{
			{Index: 1, Prompt: "Retry after signing in?"},
			{Index: 2, Prompt: "Waive the blocked check?"},
		},
		Iteration:    3,
		WaitingSince: waitingSince,
		VerificationDecision: &agent.NeedUserVerificationDecision{
			ContractPath:     "testing-contract.yaml",
			ContractRevision: 1,
			ItemIDs:          []string{"slack-integration"},
			AllowedActions:   []string{agent.NeedUserVerificationWaive, agent.NeedUserVerificationRetryAfterAuth},
		},
		Verification: &agent.NeedUserInputVerificationContext{
			Blockers: []agent.NeedUserInputVerificationBlocker{{
				ItemID:      "slack-integration",
				Name:        "Slack integration test",
				RepoName:    repoNameSelf,
				Command:     "go test ./internal/slack/...",
				Reason:      "Okta session expired",
				Remediation: "Sign in and retry",
			}},
		},
	}); err != nil {
		t.Fatalf("WriteNeedUserInputRecord() error = %v", err)
	}
	f.Status = feature.StatusNeedUserInput
	f.CurrentIteration = 3
	f.PendingNeedUserInputPath = gatePath
	if err := store.Save(f); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	h := &apiHandler{store: store}

	got, err := h.PendingSlackInputs(f.ID)
	if err != nil {
		t.Fatalf("PendingSlackInputs() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("PendingSlackInputs() length = %d; want 1", len(got))
	}

	gate := got[0]
	if gate.Kind != ports.SlackPendingGate ||
		gate.FeatureID != f.ID ||
		gate.GatePath != gatePath ||
		gate.Iteration != 3 ||
		!gate.WaitingSince.Equal(waitingSince) ||
		gate.GateSummary != "Authentication is required." {
		t.Errorf("PendingSlackInputs()[0] = %+v; want gate identity and summary", gate)
	}
	if len(gate.GateQuestions) != 2 ||
		gate.GateQuestions[0] != "Retry after signing in?" ||
		gate.GateQuestions[1] != "Waive the blocked check?" {
		t.Errorf("PendingSlackInputs()[0].GateQuestions = %v; want both questions", gate.GateQuestions)
	}
	if len(gate.GateBlockers) != 1 {
		t.Fatalf("PendingSlackInputs()[0].GateBlockers length = %d; want 1", len(gate.GateBlockers))
	}
	blocker := gate.GateBlockers[0]
	if blocker.Name != "Slack integration test" ||
		blocker.RepoName != repoNameSelf ||
		blocker.Command != "go test ./internal/slack/..." ||
		blocker.Reason != "Okta session expired" ||
		blocker.Remediation != "Sign in and retry" {
		t.Errorf("PendingSlackInputs()[0].GateBlockers[0] = %+v; want full blocker projection", blocker)
	}
}
