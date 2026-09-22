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
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/internal/slack"
	"github.com/doordash-oss/agentic-orchestrator/internal/slack/testsupport"
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

func TestPendingSlackInputsPreservesRawQuestionData(t *testing.T) {
	t.Parallel()

	longQuestion := strings.Repeat("question", 600)
	longDescription := strings.Repeat("description", 500)
	input, err := json.Marshal(map[string]any{
		"questions": []map[string]any{{
			"header":   "Deployment scope",
			"question": longQuestion,
			"options": []map[string]any{{
				"label":       "Focused",
				"description": longDescription,
			}},
		}},
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	request := pendingReadControl("question-request", toolNameAskUserQuestion, string(input))
	session := &fakeSessionView{
		id:        "question-session",
		featureID: fixtureFeatureID,
		phase:     feature.PhasePlan,
		status:    ports.SessionWaitingHelp,
		pending:   []*llm.ControlRequestMessage{request},
	}
	h := &apiHandler{sessions: fakeSessionManager{views: []ports.SessionView{session}}}

	got, err := h.PendingSlackInputs(fixtureFeatureID)
	if err != nil {
		t.Fatalf("PendingSlackInputs() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("PendingSlackInputs() length = %d; want 1", len(got))
	}
	if got[0].Question != longQuestion {
		t.Errorf("PendingSlackInputs()[0].Question length = %d; want %d", len(got[0].Question), len(longQuestion))
	}
	if len(got[0].Options) != 1 || got[0].Options[0].Description != longDescription {
		t.Errorf("PendingSlackInputs()[0].Options = %+v; want raw option description", got[0].Options)
	}

	dto := controlRequestDTO(session, request)
	if len(dto.Questions) != 1 || len(dto.Questions[0].Question) != askUserQuestionDisplayLimit+3 {
		t.Errorf("controlRequestDTO().Questions = %+v; want existing question display cap", dto.Questions)
	}
	if len(dto.Questions[0].Options) != 1 ||
		len(dto.Questions[0].Options[0].Description) != askUserOptionDescriptionDisplayLimit+3 {
		t.Errorf("controlRequestDTO().Questions[0].Options = %+v; want existing option display cap", dto.Questions[0].Options)
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

func TestPendingSlackInputsPreservesRawPermissionInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
	}{
		{
			name:    "fitting Bash command beyond API display limit",
			command: strings.Repeat("x", 2500),
		},
		{
			name: "credential URL crossing API display limit",
			command: "curl https://" +
				strings.Repeat("u", 1980) +
				":password@example.test/private",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input, err := json.Marshal(map[string]any{"command": tt.command})
			if err != nil {
				t.Fatalf("json.Marshal() error = %v", err)
			}
			request := pendingReadControl("permission-request", toolNameBash, string(input))
			session := &fakeSessionView{
				id:        "permission-session",
				featureID: fixtureFeatureID,
				phase:     feature.PhaseImplement,
				repoName:  repoNameSelf,
				status:    ports.SessionWaitingPermission,
				pending:   []*llm.ControlRequestMessage{request},
			}
			h := &apiHandler{sessions: fakeSessionManager{views: []ports.SessionView{session}}}

			got, err := h.PendingSlackInputs(fixtureFeatureID)
			if err != nil {
				t.Fatalf("PendingSlackInputs() error = %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("PendingSlackInputs() length = %d; want 1", len(got))
			}
			gotCommand, ok := got[0].Input["command"].(string)
			if !ok {
				t.Fatalf("PendingSlackInputs()[0].Input[command] = %#v; want string", got[0].Input["command"])
			}
			if gotCommand != tt.command {
				t.Errorf("PendingSlackInputs()[0].Input[command] length = %d; want exact %d-byte command", len(gotCommand), len(tt.command))
			}

			dtoCommand, ok := controlRequestDTO(session, request).Input["command"].(string)
			if !ok {
				t.Fatalf("controlRequestDTO().Input[command] = %#v; want string", controlRequestDTO(session, request).Input["command"])
			}
			if len(dtoCommand) != 2003 || !strings.HasSuffix(dtoCommand, "...") {
				t.Errorf("controlRequestDTO().Input[command] = %d bytes; want existing 2000-byte display cap plus ellipsis", len(dtoCommand))
			}
		})
	}
}

func TestPendingSlackInputsRealPortPreservesThenRedactsPermissionInput(t *testing.T) {
	const token = "xoxb-server-port-test-token"

	fakeSlack := testsupport.New(t)
	var responseCounter atomic.Int64
	fakeSlack.SetDefault(func(method string, request testsupport.Request) testsupport.Response {
		switch method {
		case "chat.postMessage", "chat.update":
			n := responseCounter.Add(1)
			return testsupport.Response{Body: map[string]any{
				"ok":      true,
				"ts":      fmt.Sprintf("1758499200.%06d", n),
				"channel": fmt.Sprint(request.Fields["channel"]),
			}}
		default:
			return testsupport.Response{Body: map[string]any{"ok": false, "error": "unexpected " + method}}
		}
	})
	t.Setenv(slack.EnvSlackAPIBase, fakeSlack.URL())

	fittingCommand := strings.Repeat("x", 2500)
	credentialCommand := "curl https://" +
		strings.Repeat("u", 1980) +
		":password@example.test/private"
	fittingInput, err := json.Marshal(map[string]any{"command": fittingCommand})
	if err != nil {
		t.Fatalf("json.Marshal() fitting command error = %v", err)
	}
	credentialInput, err := json.Marshal(map[string]any{"command": credentialCommand})
	if err != nil {
		t.Fatalf("json.Marshal() credential command error = %v", err)
	}
	sessionView := &fakeSessionView{
		id:        "permission-session",
		featureID: fixtureFeatureID,
		phase:     feature.PhaseImplement,
		repoName:  repoNameSelf,
		status:    ports.SessionWaitingPermission,
		pending: []*llm.ControlRequestMessage{
			pendingReadControl("fitting-command", toolNameBash, string(fittingInput)),
			pendingReadControl("credential-command", toolNameBash, string(credentialInput)),
		},
	}

	stateDir := t.TempDir()
	store := feature.NewStore(stateDir)
	if err := store.Save(&feature.Feature{
		ID:            fixtureFeatureID,
		Name:          "Slack pending input projection",
		Slug:          "slack-pending-input-projection",
		Description:   "server port regression fixture",
		Created:       time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC),
		Status:        feature.StatusImplementing,
		CurrentPhase:  feature.PhaseImplement,
		SchemaVersion: feature.SchemaVersionCurrent,
		Pipeline:      feature.PipelineMoonshot,
		Repos:         []feature.FeatureRepo{{Name: repoNameSelf}},
	}); err != nil {
		t.Fatalf("Save() feature error = %v", err)
	}
	source := &apiHandler{
		sessions: fakeSessionManager{views: []ports.SessionView{sessionView}},
		store:    store,
	}
	notifier := slack.NewNotifier(slack.NotifierOptions{
		Settings: slackPendingInputTestSettings{settings: ports.SlackRuntimeSettings{
			Enabled: true,
			Token:   token,
			Recipients: []ports.SlackRecipient{{
				TypedText: "#eng", Kind: ports.SlackRecipientChannel, ID: "C-ENG", DisplayName: "#eng",
			}},
			Categories: ports.SlackCategoryDefaults{Progress: true, NeedsInput: true, Problems: true},
		}},
		Store:    store,
		StateDir: stateDir,
		Pending:  source,
	})
	notifier.SetServerName("Local agent")
	notifier.Start()
	t.Cleanup(func() { notifier.Stop(context.Background()) })

	notifier.RuntimeMessageTap(session.SDKEventMsg{
		SessionID: "permission-session",
		FeatureID: fixtureFeatureID,
		Phase:     feature.PhaseImplement,
		Message: llm.SDKMessage{
			Type: "control_request",
			ControlRequest: &llm.ControlRequestMessage{
				Type:      "control_request",
				RequestID: "fitting-command",
				Request:   llm.ControlRequest{Subtype: "can_use_tool", ToolName: toolNameBash},
			},
		},
	})

	var threadPosts []testsupport.Request
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		threadPosts = threadSlackPosts(fakeSlack.Requests("chat.postMessage"))
		if len(threadPosts) == 2 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if len(threadPosts) != 2 {
		t.Fatalf("thread chat.postMessage calls = %d; want 2", len(threadPosts))
	}

	var fittingPayload, credentialPayload string
	for _, post := range threadPosts {
		blocks := fmt.Sprint(post.Fields["blocks"])
		switch {
		case strings.Contains(blocks, fittingCommand):
			fittingPayload = blocks
		case strings.Contains(blocks, "curl ") && strings.Contains(blocks, "[REDACTED]"):
			credentialPayload = blocks
		}
	}
	if fittingPayload == "" {
		t.Error("Slack blocks omitted the complete 2500-byte Bash command")
	}
	if credentialPayload == "" {
		t.Fatalf("Slack blocks omitted the credential command's surrounding text or redaction marker: %+v", threadPosts)
	}
	if strings.Contains(credentialPayload, "password") || strings.Contains(credentialPayload, strings.Repeat("u", 1980)) {
		t.Errorf("Slack blocks leaked URL userinfo: %s", credentialPayload)
	}
	if !strings.Contains(credentialPayload, "[REDACTED]") {
		t.Errorf("Slack blocks lack credential redaction marker: %s", credentialPayload)
	}
}

type slackPendingInputTestSettings struct {
	settings ports.SlackRuntimeSettings
}

func (s slackPendingInputTestSettings) SlackSettings() ports.SlackRuntimeSettings {
	return s.settings
}

func threadSlackPosts(requests []testsupport.Request) []testsupport.Request {
	var posts []testsupport.Request
	for _, request := range requests {
		threadTS, ok := request.Fields["thread_ts"]
		if ok && fmt.Sprint(threadTS) != "" {
			posts = append(posts, request)
		}
	}
	return posts
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

func TestPendingSlackInputsPreservesRawVerificationGateText(t *testing.T) {
	t.Parallel()

	store, f := seedReadFeature(t)
	gatePath := filepath.Join(store.RunDir(f.ID, 1), "phase-06", "implement", agent.NeedUserInputArtifactName)
	credentialURL := func(user, password string, padding int) string {
		return "https://" + user + ":" + password + strings.Repeat("x", padding) + "@example.test/private"
	}
	summary := "Summary " + credentialURL("alice", "summary-password", agent.NeedUserInputVerificationContextTextMaxLength)
	question := "Open " + credentialURL("alice", "question-password", 700)
	name := "Name " + credentialURL("alice", "name-password", agent.NeedUserInputVerificationContextTextMaxLength)
	repoName := "Repo " + credentialURL("alice", "repo-password", agent.NeedUserInputVerificationRepoNameMaxLength)
	command := "curl " + credentialURL("alice", "command-password", agent.NeedUserInputVerificationContextTextMaxLength)
	reason := "Reason " + credentialURL("alice", "reason-password", agent.NeedUserInputVerificationContextTextMaxLength)
	remediation := "Remediation " + credentialURL("alice", "remediation-password", agent.NeedUserInputVerificationContextTextMaxLength)

	questions := make([]agent.NeedUserInputQuestion, agent.NeedUserInputGateMaxQuestions)
	questions[0] = agent.NeedUserInputQuestion{Index: 1, Prompt: question}
	for i := 1; i < len(questions); i++ {
		questions[i] = agent.NeedUserInputQuestion{Index: i + 1, Prompt: fmt.Sprintf("Question %d", i+1)}
	}
	if err := agent.WriteNeedUserInputRecord(gatePath, agent.NeedUserInputRecord{
		Summary:   summary,
		Questions: questions,
		Iteration: 3,
		VerificationDecision: &agent.NeedUserVerificationDecision{
			ContractPath:     "testing-contract.yaml",
			ContractRevision: 1,
			ItemIDs:          []string{"slack-integration"},
			AllowedActions:   []string{agent.NeedUserVerificationWaive},
		},
		Verification: &agent.NeedUserInputVerificationContext{
			Blockers: []agent.NeedUserInputVerificationBlocker{{
				ItemID:      "slack-integration",
				Name:        name,
				RepoName:    repoName,
				Command:     command,
				Reason:      reason,
				Remediation: remediation,
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
	if gate.GateSummary != summary {
		t.Errorf("PendingSlackInputs()[0].GateSummary length = %d; want raw length %d", len(gate.GateSummary), len(summary))
	}
	if len(gate.GateQuestions) != len(questions) || gate.GateQuestions[0] != question {
		t.Errorf("PendingSlackInputs()[0].GateQuestions first length = %d; want raw length %d across %d questions", len(gate.GateQuestions[0]), len(question), len(questions))
	}
	if len(gate.GateBlockers) != 1 {
		t.Fatalf("PendingSlackInputs()[0].GateBlockers length = %d; want 1", len(gate.GateBlockers))
	}
	blocker := gate.GateBlockers[0]
	if blocker.Name != name ||
		blocker.RepoName != repoName ||
		blocker.Command != command ||
		blocker.Reason != reason ||
		blocker.Remediation != remediation {
		t.Errorf(
			"PendingSlackInputs()[0].GateBlockers[0] lengths = name:%d repo:%d command:%d reason:%d remediation:%d; want raw lengths %d, %d, %d, %d, %d",
			len(blocker.Name), len(blocker.RepoName), len(blocker.Command), len(blocker.Reason), len(blocker.Remediation),
			len(name), len(repoName), len(command), len(reason), len(remediation),
		)
	}

	dto := needUserInputGateDTO(
		f.ID, entityFeature, "", f.CurrentIteration, f.InputNotifications, f.PendingNeedUserInputPath,
	)
	if dto.Summary == summary {
		t.Error("needUserInputGateDTO().Summary retained raw over-limit text; want existing API display bound")
	}
	if len(dto.Questions) != len(questions) || dto.Questions[0].Prompt == question {
		t.Errorf("needUserInputGateDTO().Questions retained raw boundary-crossing prompt; want existing aggregate display bound")
	}
}

func TestPendingSlackInputsRealPortRedactsVerificationGateBeforeSlackTruncation(t *testing.T) {
	const token = "xoxb-server-gate-port-test-token"

	fakeSlack := testsupport.New(t)
	var responseCounter atomic.Int64
	fakeSlack.SetDefault(func(method string, request testsupport.Request) testsupport.Response {
		switch method {
		case "chat.postMessage", "chat.update":
			n := responseCounter.Add(1)
			return testsupport.Response{Body: map[string]any{
				"ok":      true,
				"ts":      fmt.Sprintf("1758499200.%06d", n),
				"channel": fmt.Sprint(request.Fields["channel"]),
			}}
		default:
			return testsupport.Response{Body: map[string]any{"ok": false, "error": "unexpected " + method}}
		}
	})
	t.Setenv(slack.EnvSlackAPIBase, fakeSlack.URL())

	store, f := seedReadFeature(t)
	gatePath := filepath.Join(store.RunDir(f.ID, 1), "phase-06", "implement", agent.NeedUserInputArtifactName)
	credentialQuestion := "Open https://alice:gate-password" +
		strings.Repeat("x", 700) +
		"@example.test/private"
	questions := make([]agent.NeedUserInputQuestion, agent.NeedUserInputGateMaxQuestions)
	questions[0] = agent.NeedUserInputQuestion{Index: 1, Prompt: credentialQuestion}
	for i := 1; i < len(questions); i++ {
		questions[i] = agent.NeedUserInputQuestion{Index: i + 1, Prompt: fmt.Sprintf("Question %d", i+1)}
	}
	if err := agent.WriteNeedUserInputRecord(gatePath, agent.NeedUserInputRecord{
		Summary:   "Authentication is required.",
		Questions: questions,
		Iteration: 3,
		VerificationDecision: &agent.NeedUserVerificationDecision{
			ContractPath:     "testing-contract.yaml",
			ContractRevision: 1,
			ItemIDs:          []string{"slack-integration"},
			AllowedActions:   []string{agent.NeedUserVerificationRetryAfterAuth},
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

	source := &apiHandler{store: store}
	pending, err := source.PendingSlackInputs(f.ID)
	if err != nil {
		t.Fatalf("PendingSlackInputs() error = %v", err)
	}
	if len(pending) != 1 || len(pending[0].GateQuestions) != len(questions) {
		t.Fatalf("PendingSlackInputs() = %+v; want one gate with %d questions", pending, len(questions))
	}
	if pending[0].GateQuestions[0] != credentialQuestion {
		t.Fatalf(
			"PendingSlackInputs()[0].GateQuestions[0] length = %d; want raw length %d for Slack scrubbing",
			len(pending[0].GateQuestions[0]),
			len(credentialQuestion),
		)
	}

	notifier := slack.NewNotifier(slack.NotifierOptions{
		Settings: slackPendingInputTestSettings{settings: ports.SlackRuntimeSettings{
			Enabled: true,
			Token:   token,
			Recipients: []ports.SlackRecipient{{
				TypedText: "#eng", Kind: ports.SlackRecipientChannel, ID: "C-ENG", DisplayName: "#eng",
			}},
			Categories: ports.SlackCategoryDefaults{Progress: true, NeedsInput: true, Problems: true},
		}},
		Store:    store,
		StateDir: store.BaseDir,
		Pending:  source,
	})
	notifier.SetServerName("Local agent")
	notifier.Start()
	t.Cleanup(func() { notifier.Stop(context.Background()) })

	notifier.DomainEventTap(ports.Event{Type: ports.NeedUserInputRequired, FeatureID: f.ID})

	var threadPosts []testsupport.Request
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		threadPosts = threadSlackPosts(fakeSlack.Requests("chat.postMessage"))
		if len(threadPosts) == 1 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if len(threadPosts) != 1 {
		t.Fatalf("thread chat.postMessage calls = %d; want 1", len(threadPosts))
	}

	for field, payload := range map[string]string{
		"blocks":   fmt.Sprint(threadPosts[0].Fields["blocks"]),
		"fallback": fmt.Sprint(threadPosts[0].Fields["text"]),
	} {
		for _, fragment := range []string{"alice:", "gate-password", strings.Repeat("x", 64)} {
			if strings.Contains(payload, fragment) {
				t.Errorf("Slack %s leaked credential fragment %q", field, fragment)
			}
		}
		if !strings.Contains(payload, "Open https://[REDACTED]@example.test/private") {
			t.Errorf("Slack %s = %q; want surrounding question text and redaction marker", field, payload)
		}
	}
}
