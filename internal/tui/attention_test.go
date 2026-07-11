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

package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
)

func TestComputeFeatureAttentionPriority(t *testing.T) {
	t.Parallel()

	reviewPhase := feature.PhaseImplement
	designPhase := feature.PhaseDesign
	tests := []struct {
		name        string
		f           *feature.Feature
		sess        session.SessionView
		wantKind    attentionKind
		wantCTA     string
		wantSummary string
		wantRepo    string
	}{
		{
			name:     "nil feature",
			wantKind: attentionNone,
		},
		{
			name: "review gate wins over stale or live permission",
			f: &feature.Feature{
				Status:                   feature.StatusPlanNeedsReview,
				PendingReviewPhase:       &reviewPhase,
				PendingNeedUserInputPath: "/tmp/need-user-input.yaml",
				PermissionsQueue: []feature.PermissionRequest{
					{Tool: toolNameBash, Args: `{"command":"go test ./internal/tui"}`, Pending: true}, //nolint:goconst // shared raw-JSON test fixture; not constant-ized per raw-string-fixture policy
				},
				HelpQueue: []feature.HelpRequest{{Question: "Which branch?", Pending: true}},
			},
			sess:        pendingAttentionSession("perm-review", session.SessionWaitingPermission, pendingPermissionControlRequestForAttention("perm-1", toolNameBash, `{"command":"go test ./internal/tui"}`)),
			wantKind:    attentionReview,
			wantCTA:     "Review",
			wantSummary: "Plan needs review",
		},
		{
			name: "review gate wins over stale or live ask user",
			f: &feature.Feature{
				Status:                   feature.StatusPlanNeedsReview,
				PendingReviewPhase:       &reviewPhase,
				PendingNeedUserInputPath: "/tmp/need-user-input.yaml",
				HelpQueue:                []feature.HelpRequest{{Question: "Pick a path?", Pending: true}},
			},
			sess:        pendingAttentionSession("ask-review", session.SessionWaitingHelp, pendingAskUserControlRequestForAttention(testAskRequestID, "Pick a path?")),
			wantKind:    attentionReview,
			wantCTA:     "Review",
			wantSummary: "Plan needs review",
		},
		{
			name: "review summary names reviewed artifact not next target phase",
			f: &feature.Feature{
				Status:             feature.StatusResearchNeedsReview,
				PendingReviewPhase: &designPhase,
			},
			wantKind:    attentionReview,
			wantCTA:     "Review",
			wantSummary: "Research needs review",
		},
		{
			name: "review gate ignores stale feature help without live session",
			f: &feature.Feature{
				Status:    feature.StatusInquiryNeedsReview,
				HelpQueue: []feature.HelpRequest{{Question: waitingInputHelpMessage, Pending: true}},
			},
			wantKind:    attentionReview,
			wantCTA:     "Review",
			wantSummary: "Inquiry needs review",
		},
		{
			name: "feature need user input answers before review",
			f: &feature.Feature{
				Status:                   feature.StatusNeedUserInput,
				PendingNeedUserInputPath: "/tmp/need-user-input.yaml",
			},
			wantKind:    attentionNeedUserInput,
			wantCTA:     "Answer",
			wantSummary: "Feature-level input gate",
		},
		{
			name: "repo cycle need user input carries target repo",
			f: &feature.Feature{
				Status: feature.StatusPublished,
				RepoCycles: map[string]*feature.RepoCycleState{
					"api": {
						Type:                     feature.CycleRefactor,
						Status:                   feature.RepoCycleNeedUserInput,
						PendingNeedUserInputPath: "/tmp/api/need-user-input.yaml",
					},
				},
			},
			wantKind:    attentionNeedUserInput,
			wantCTA:     "Answer",
			wantSummary: "refactor input gate for api",
			wantRepo:    "api",
		},
		{
			name:        "review gate",
			f:           &feature.Feature{Status: feature.StatusPlanNeedsReview, CurrentRoadmapPhase: 2, TotalRoadmapPhases: 4},
			wantKind:    attentionReview,
			wantCTA:     "Review",
			wantSummary: "Phase 2 plan needs review",
		},
		{
			name:     "created feature watches",
			f:        &feature.Feature{Status: feature.StatusCreated, CurrentPhase: feature.PhaseResearch},
			wantKind: attentionWatch,
			wantCTA:  "Watch",
		},
		{
			name:     "running feature watches",
			f:        &feature.Feature{Status: feature.StatusImplementing, CurrentPhase: feature.PhaseImplement},
			wantKind: attentionWatch,
			wantCTA:  "Watch",
		},
		{
			name: "active post publish cycle watches",
			f: &feature.Feature{
				Status: feature.StatusPublished,
				RepoCycles: map[string]*feature.RepoCycleState{
					"api": {Type: feature.CycleTweak, Status: feature.RepoCycleRunning},
				},
			},
			wantKind: attentionWatch,
			wantCTA:  "Watch",
		},
		{
			name:     "idle published has no contextual action",
			f:        &feature.Feature{Status: feature.StatusPublished},
			wantKind: attentionNone,
		},
		{
			name:     "failed without pending attention has no contextual action",
			f:        &feature.Feature{Status: feature.StatusFailed},
			wantKind: attentionNone,
		},
		{
			name: "interrupted feature ignores stale feature and session queues",
			f: &feature.Feature{
				Status: feature.StatusInterrupted,
				PermissionsQueue: []feature.PermissionRequest{
					{Tool: toolNameBash, Args: `{"command":"go test ./internal/tui"}`, Pending: true},
				},
				HelpQueue: []feature.HelpRequest{{Question: "Which branch?", Pending: true}},
			},
			sess:     pendingAttentionSession("stopped-session", session.SessionWaitingPermission, pendingPermissionControlRequestForAttention("perm-stale", toolNameBash, `{"command":"go test ./internal/tui"}`)),
			wantKind: attentionNone,
		},
		{
			name: "session permission beats feature ask user",
			f: &feature.Feature{
				Status:    feature.StatusImplementing,
				HelpQueue: []feature.HelpRequest{{Question: "Answer before permission?", Pending: true}},
			},
			sess:        pendingAttentionSession("perm-session", session.SessionWaitingPermission, pendingPermissionControlRequestForAttention("perm-1", "Edit", `{"file_path":"internal/tui/dashboard.go"}`)),
			wantKind:    attentionPermission,
			wantCTA:     "Approve",
			wantSummary: "Edit: internal/tui/dashboard.go",
		},
		{
			name:        "session ask user supplies question summary",
			f:           &feature.Feature{Status: feature.StatusImplementing},
			sess:        pendingAttentionSession("ask-session", session.SessionWaitingHelp, pendingAskUserControlRequestForAttention(testAskRequestID, "Which formatter?")),
			wantKind:    attentionAskUser,
			wantCTA:     "Answer",
			wantSummary: "Which formatter?",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := computeFeatureAttention(tt.f, tt.sess)
			if got.Kind != tt.wantKind {
				t.Fatalf("computeFeatureAttention(%s).Kind = %v, want %v", tt.name, got.Kind, tt.wantKind)
			}
			if got.CTALabel != tt.wantCTA {
				t.Errorf("computeFeatureAttention(%s).CTALabel = %q, want %q", tt.name, got.CTALabel, tt.wantCTA)
			}
			if tt.wantSummary != "" && got.Summary != tt.wantSummary {
				t.Errorf("computeFeatureAttention(%s).Summary = %q, want %q", tt.name, got.Summary, tt.wantSummary)
			}
			if got.RepoName != tt.wantRepo {
				t.Errorf("computeFeatureAttention(%s).RepoName = %q, want %q", tt.name, got.RepoName, tt.wantRepo)
			}
		})
	}
}

func TestComputeFeatureAttentionNormalizesLegacyPendingHelpCopy(t *testing.T) {
	t.Parallel()

	f := &feature.Feature{
		Status: feature.StatusImplementing,
		HelpQueue: []feature.HelpRequest{
			{Question: "Agent is waiting for input — attach with 'a' to respond", Pending: true},
			{Question: waitingInputHelpMessage, Pending: true},
		},
	}

	got := computeFeatureAttention(f, nil)
	if got.Kind != attentionAskUser {
		t.Fatalf("computeFeatureAttention().Kind = %v, want %v", got.Kind, attentionAskUser)
	}
	if got.Summary != waitingInputHelpMessage {
		t.Errorf("computeFeatureAttention().Summary = %q, want %q", got.Summary, waitingInputHelpMessage)
	}
	if strings.Contains(strings.ToLower(got.Summary), "attach") {
		t.Errorf("computeFeatureAttention().Summary contains retired copy: %q", got.Summary)
	}
}

func TestComputeFeatureAttentionNormalizesLegacyAPIErrorHelpCopy(t *testing.T) {
	t.Parallel()

	f := &feature.Feature{
		Status: feature.StatusImplementing,
		HelpQueue: []feature.HelpRequest{
			{Question: "API error: rate limit exceeded (429) — attach with 'a' to respond", Pending: true},
		},
	}

	got := computeFeatureAttention(f, nil)
	const want = "API error: rate limit exceeded (429) — press 'a' to answer"
	if got.Kind != attentionAskUser {
		t.Fatalf("computeFeatureAttention().Kind = %v, want %v", got.Kind, attentionAskUser)
	}
	if got.Summary != want {
		t.Errorf("computeFeatureAttention().Summary = %q, want %q", got.Summary, want)
	}
	if strings.Contains(strings.ToLower(got.Summary), "attach") {
		t.Errorf("computeFeatureAttention().Summary contains retired copy: %q", got.Summary)
	}
}

func pendingAttentionSession(id string, status session.SessionStatus, cr *llm.ControlRequestMessage) session.SessionView {
	sess := session.NewSession(id, "feat-attention", feature.PhaseImplement)
	sess.SetStatus(status)
	if cr != nil {
		sess.SetLastControlRequest(cr)
	}
	return sess
}

func pendingPermissionControlRequestForAttention(requestID, toolName, input string) *llm.ControlRequestMessage {
	return &llm.ControlRequestMessage{
		RequestID: requestID,
		Request: llm.ControlRequest{
			Subtype:  controlRequestSubtypeCanUseTool,
			ToolName: toolName,
			Input:    json.RawMessage(input),
		},
	}
}

func pendingAskUserControlRequestForAttention(requestID, question string) *llm.ControlRequestMessage {
	payload := map[string]any{
		"questions": []map[string]any{
			{"question": question},
		},
	}
	raw, _ := json.Marshal(payload)
	return &llm.ControlRequestMessage{
		RequestID: requestID,
		Request: llm.ControlRequest{
			Subtype:  controlRequestSubtypeCanUseTool,
			ToolName: toolNameAskUserQuestion,
			Input:    raw,
		},
	}
}
