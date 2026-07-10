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
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/server/serverapi"
)

func TestGeneratedOpenAPIHardeningDTOsMatchServerWireJSON(t *testing.T) {
	at := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	seq := uint64(42)
	resourceVersion := uint64(7)
	epoch := "epoch-1"
	revision := "rev-1"
	summary := "feature updated"
	featureID := "F-42"
	resourceID := "sess-1"
	phase := "implement"
	meta := ResponseMeta{Revision: revision, GeneratedAt: at, AsOfSeq: seq}

	assertSameJSON(t, "response meta",
		meta,
		serverapi.ResponseMeta{Revision: revision, GeneratedAt: at, AsOfSeq: seq},
	)
	assertSameJSON(t, "event envelope",
		SSEEventDTO{
			APIVersion:       APIVersion,
			ID:               "42",
			Seq:              seq,
			Epoch:            epoch,
			Kind:             "session.updated",
			At:               at,
			Resource:         ResourceDTO{Type: "session", ID: resourceID, FeatureID: featureID, Phase: phase},
			ResourceVersion:  resourceVersion,
			Revision:         revision,
			SnapshotRequired: true,
			Summary:          summary,
		},
		serverapi.SSEEvent{
			APIVersion:       APIVersion,
			ID:               "42",
			Seq:              &seq,
			Epoch:            &epoch,
			Kind:             "session.updated",
			At:               at,
			Resource:         serverapi.Resource{Type: "session", ID: &resourceID, FeatureID: &featureID, Phase: &phase},
			ResourceVersion:  &resourceVersion,
			Revision:         &revision,
			SnapshotRequired: true,
			Summary:          &summary,
		},
	)
	assertSameJSON(t, "minimal event envelope",
		SSEEventDTO{
			APIVersion:       APIVersion,
			ID:               "0",
			Kind:             "heartbeat",
			At:               at,
			Resource:         ResourceDTO{Type: "runtime"},
			SnapshotRequired: false,
		},
		serverapi.SSEEvent{
			APIVersion:       APIVersion,
			ID:               "0",
			Kind:             "heartbeat",
			At:               at,
			Resource:         serverapi.Resource{Type: "runtime"},
			SnapshotRequired: false,
		},
	)
	assertSameJSON(t, "session output",
		SessionOutputResponse{
			APIVersion: APIVersion,
			Meta:       meta,
			SessionID:  resourceID,
			Offset:     10,
			NextOffset: 18,
			Size:       18,
			Data:       "chunk",
			Truncated:  false,
			Done:       true,
		},
		serverapi.SessionOutputResponse{
			APIVersion: APIVersion,
			Meta:       &serverapi.ResponseMeta{Revision: revision, GeneratedAt: at, AsOfSeq: seq},
			SessionID:  resourceID,
			Offset:     10,
			NextOffset: 18,
			Size:       18,
			Data:       "chunk",
			Truncated:  false,
			Done:       true,
		},
	)

	warning := WarningDTO{Code: "stale_cycle", FeatureID: featureID, Message: "cycle is stale"}
	genWarning := serverapi.Warning{Code: "stale_cycle", FeatureID: &featureID, Message: "cycle is stale"}

	featureSummary := FeatureSummary{
		ID:           featureID,
		Name:         "Client cutover",
		Slug:         "client-cutover",
		Status:       "in_progress",
		CurrentPhase: phase,
		Cycle:        &CycleDTO{Type: "fix", Status: "open", Count: 2, Iteration: 1},
		ActiveRun:    3,
		RunCount:     4,
		Repos:        []string{"repo-a", "repo-b"},
		CreatedAt:    at,
		Checkpoints: CheckpointsDTO{
			InquiryReview: true, ResearchReview: false, DesignReview: true,
			RoadmapReview: false, PhasePlanReview: true, ManualPublish: false, DraftPublish: true,
		},
		Progress: FeatureProgress{
			CurrentIteration: 2, CurrentRoadmapPhase: 1, TotalRoadmapPhases: 5, CurrentPhaseStatus: "active",
		},
		Warnings: []WarningDTO{warning},
	}
	genSummary := serverapi.FeatureSummary{
		ID:           featureID,
		Name:         "Client cutover",
		Slug:         "client-cutover",
		Status:       "in_progress",
		CurrentPhase: phase,
		Cycle:        &serverapi.Cycle{Type: stringPtr("fix"), Status: stringPtr("open"), Count: intPtr(2), Iteration: intPtr(1)},
		ActiveRun:    3,
		RunCount:     4,
		Repos:        []string{"repo-a", "repo-b"},
		CreatedAt:    at,
		Checkpoints: serverapi.Checkpoints{
			InquiryReview: true, ResearchReview: false, DesignReview: true,
			RoadmapReview: false, PhasePlanReview: true, ManualPublish: false, DraftPublish: true,
		},
		Progress: serverapi.FeatureProgress{
			CurrentIteration: intPtr(2), CurrentRoadmapPhase: intPtr(1), TotalRoadmapPhases: intPtr(5), CurrentPhaseStatus: stringPtr("active"),
		},
		Warnings: &[]serverapi.Warning{genWarning},
	}

	assertSameJSON(t, "feature list response",
		FeatureListResponse{APIVersion: APIVersion, Meta: meta, Features: []FeatureSummary{featureSummary}, Warnings: []WarningDTO{warning}},
		serverapi.FeatureListResponse{APIVersion: APIVersion, Meta: &serverapi.ResponseMeta{Revision: revision, GeneratedAt: at, AsOfSeq: seq}, Features: []serverapi.FeatureSummary{genSummary}, Warnings: &[]serverapi.Warning{genWarning}},
	)

	activeRun := RunSummaryDTO{
		RunNumber:     5,
		StartedAt:     &at,
		CurrentPhase:  phase,
		PhaseStatus:   "running",
		Iteration:     3,
		RoadmapPhase:  2,
		RoadmapTotal:  6,
		ArtifactCount: 7,
	}
	genActiveRun := serverapi.RunSummary{
		RunNumber:     5,
		StartedAt:     &at,
		CurrentPhase:  stringPtr(phase),
		PhaseStatus:   stringPtr("running"),
		Iteration:     intPtr(3),
		RoadmapPhase:  intPtr(2),
		RoadmapTotal:  intPtr(6),
		ArtifactCount: 7,
	}

	repoStatus := RepoStatusDTO{Name: "repo-a", Freshness: "fresh", Touched: true, Publishable: true, PRURL: "https://example.test/pr/1"}
	genRepoStatus := serverapi.RepoStatus{Name: "repo-a", Freshness: stringPtr("fresh"), Touched: true, Publishable: true, PrURL: stringPtr("https://example.test/pr/1")}

	action := ActionDTO{
		ID:      "publish",
		Enabled: true,
		Scope:   ActionScopeDTO{Type: "feature"},
		RequiredInputs: []ActionInputDTO{
			{Name: "message", Kind: "text", Required: true},
		},
		DisabledReasons: []ActionDisabledReasonDTO{
			{Code: "gate_open", Message: "review gate is open"},
		},
	}
	genAction := serverapi.Action{
		ID:      "publish",
		Enabled: true,
		Scope:   serverapi.ActionScope{Type: "feature"},
		RequiredInputs: []serverapi.ActionInput{
			{Name: "message", Kind: "text", Required: true},
		},
		DisabledReasons: &[]serverapi.ActionDisabledReason{
			{Code: "gate_open", Message: "review gate is open"},
		},
	}

	// FeatureDetailDTO embeds FeatureSummary and separately declares its own
	// Cycle field (read_model.go always sets detail.Cycle explicitly); Go's
	// JSON encoder promotes the shallower (outer) field and drops the
	// embedded one, so the outer Cycle set below is what actually reaches
	// the wire.
	detailCycle := CycleDTO{Type: "rewind", Status: "open", Count: 1, Iteration: 4}
	featureDetail := FeatureDetailDTO{
		FeatureSummary:  featureSummary,
		Description:     "desc",
		Summary:         "sum",
		Pipeline:        "standard",
		Models:          config.ModelConfig{Inquiry: "gpt", Research: "gpt", Planning: "gpt", Implementation: "gpt", Review: "gpt", Utilities: "gpt", KBBuild: "gpt"},
		Cycle:           &detailCycle,
		ActiveRun:       &activeRun,
		HistoricalRuns:  []RunSummaryDTO{activeRun},
		RepoStatus:      []RepoStatusDTO{repoStatus},
		Timing:          TimingDTO{TotalSeconds: 120, ByPhase: map[string]int64{"implement": 90}},
		Cost:            CostDTO{TotalUSD: 1.5, ByPhase: map[string]float64{"implement": 1.5}},
		ReviewGate:      ReviewGateDTO{ReviewingGate: true, ReviewFixing: false, ValidatingPlan: false, ValidatorStatuses: map[string]string{"design": "approved"}},
		Failure:         &FailureDTO{Type: "timeout", Message: "session timed out"},
		NeedUserInput:   &NeedInputGateDTO{FeatureID: featureID, Open: true, Scope: "feature", Iteration: 3},
		Actions:         []ActionDTO{action},
		Revision:        revision,
		CacheRevalidate: "always",
	}
	genFeatureDetail := serverapi.FeatureDetail{
		// Fields promoted from the embedded FeatureSummary DTO — the
		// generated type flattens allOf branches, so these live directly
		// on FeatureDetail rather than on a nested FeatureSummary value.
		ID:              genSummary.ID,
		Name:            genSummary.Name,
		Slug:            genSummary.Slug,
		Status:          genSummary.Status,
		CurrentPhase:    genSummary.CurrentPhase,
		ActiveRun:       genSummary.ActiveRun,
		RunCount:        genSummary.RunCount,
		Repos:           genSummary.Repos,
		CreatedAt:       genSummary.CreatedAt,
		Checkpoints:     genSummary.Checkpoints,
		Progress:        genSummary.Progress,
		Warnings:        genSummary.Warnings,
		Cycle:           &serverapi.Cycle{Type: stringPtr("rewind"), Status: stringPtr("open"), Count: intPtr(1), Iteration: intPtr(4)},
		Description:     stringPtr("desc"),
		Summary:         stringPtr("sum"),
		Pipeline:        stringPtr("standard"),
		Models:          serverapi.ModelDefaults{Inquiry: stringPtr("gpt"), Research: stringPtr("gpt"), Planning: stringPtr("gpt"), Implementation: stringPtr("gpt"), Review: stringPtr("gpt"), Utilities: stringPtr("gpt"), KbBuild: stringPtr("gpt")},
		ActiveRunDetail: &genActiveRun,
		HistoricalRuns:  []serverapi.RunSummary{genActiveRun},
		RepoStatus:      []serverapi.RepoStatus{genRepoStatus},
		Timing:          serverapi.Timing{TotalSeconds: 120, ByPhase: map[string]int64{"implement": 90}},
		Cost:            serverapi.Cost{TotalUsd: 1.5, ByPhase: map[string]float64{"implement": 1.5}},
		ReviewGate:      serverapi.ReviewGate{ReviewingGate: true, ReviewFixing: false, ValidatingPlan: false, ValidatorStatuses: &map[string]string{"design": "approved"}},
		Failure:         &serverapi.Failure{Type: stringPtr("timeout"), Message: stringPtr("session timed out")},
		NeedUserInput:   &serverapi.NeedUserInputGate{FeatureID: stringPtr(featureID), Open: true, Scope: stringPtr("feature"), Iteration: intPtr(3)},
		Actions:         []serverapi.Action{genAction},
		Revision:        revision,
		CacheRevalidate: "always",
	}

	assertSameJSON(t, "feature detail response",
		FeatureDetailResponse{APIVersion: APIVersion, Meta: meta, Feature: featureDetail},
		serverapi.FeatureDetailResponse{APIVersion: APIVersion, Meta: &serverapi.ResponseMeta{Revision: revision, GeneratedAt: at, AsOfSeq: seq}, Feature: genFeatureDetail},
	)

	sessionSummary := SessionSummaryDTO{
		ID:         resourceID,
		FeatureID:  featureID,
		Phase:      phase,
		Repo:       "repo-a",
		Kind:       "implementation",
		Label:      "Implement",
		Provider:   "codex",
		Model:      "gpt",
		Status:     "running",
		TurnState:  "agent",
		StartedAt:  at,
		Iteration:  2,
		ContextPct: 40,
		Usage:      UsageDTO{InputTokens: 10, OutputTokens: 20, CostUSD: 0.5},
	}
	genSessionSummary := serverapi.SessionSummary{
		ID:                resourceID,
		FeatureID:         featureID,
		Phase:             phase,
		Repo:              stringPtr("repo-a"),
		Kind:              "implementation",
		Label:             stringPtr("Implement"),
		Provider:          stringPtr("codex"),
		Model:             stringPtr("gpt"),
		Status:            "running",
		TurnState:         stringPtr("agent"),
		StartedAt:         at,
		Iteration:         intPtr(2),
		ContextPercentage: intPtr(40),
		Usage:             serverapi.Usage{InputTokens: intPtr(10), OutputTokens: intPtr(20), CostUsd: float64Ptr(0.5)},
	}

	assertSameJSON(t, "session list response",
		SessionListResponse{APIVersion: APIVersion, Meta: meta, Sessions: []SessionSummaryDTO{sessionSummary}},
		serverapi.SessionListResponse{APIVersion: APIVersion, Meta: &serverapi.ResponseMeta{Revision: revision, GeneratedAt: at, AsOfSeq: seq}, Sessions: []serverapi.SessionSummary{genSessionSummary}},
	)

	control := ControlRequestDTO{RequestID: "req-1", SessionID: resourceID, ToolName: "bash", Status: "pending"}
	genControl := serverapi.ControlRequest{RequestID: "req-1", SessionID: stringPtr(resourceID), ToolName: "bash", Status: "pending"}

	sessionDetail := SessionDetailDTO{
		SessionSummaryDTO: sessionSummary,
		TranscriptCursor:  CursorDTO{Total: 10, Start: 0, End: 10},
		PendingControls:   []ControlRequestDTO{control},
		InitialPrompt:     "do the thing",
		CanAttach:         true,
		LogAvailable:      true,
	}
	genSessionDetail := serverapi.SessionDetail{
		// Fields promoted from the embedded SessionSummary DTO — flattened
		// by the generator the same way FeatureDetail flattens FeatureSummary.
		ID:                genSessionSummary.ID,
		FeatureID:         genSessionSummary.FeatureID,
		Phase:             genSessionSummary.Phase,
		Repo:              genSessionSummary.Repo,
		Kind:              genSessionSummary.Kind,
		Label:             genSessionSummary.Label,
		Provider:          genSessionSummary.Provider,
		Model:             genSessionSummary.Model,
		Status:            genSessionSummary.Status,
		TurnState:         genSessionSummary.TurnState,
		StartedAt:         genSessionSummary.StartedAt,
		Iteration:         genSessionSummary.Iteration,
		ContextPercentage: genSessionSummary.ContextPercentage,
		Usage:             genSessionSummary.Usage,
		TranscriptCursor:  serverapi.Cursor{Total: 10, Start: 0, End: 10},
		PendingControls:   []serverapi.ControlRequest{genControl},
		InitialPrompt:     stringPtr("do the thing"),
		CanAttach:         true,
		LogAvailable:      true,
	}

	assertSameJSON(t, "session detail response",
		SessionDetailResponse{APIVersion: APIVersion, Meta: meta, Session: sessionDetail},
		serverapi.SessionDetailResponse{APIVersion: APIVersion, Meta: &serverapi.ResponseMeta{Revision: revision, GeneratedAt: at, AsOfSeq: seq}, Session: genSessionDetail},
	)
}

func float64Ptr(v float64) *float64 { return &v }

func TestGeneratedOpenAPIParamsMatchClientQueryNames(t *testing.T) {
	transcriptParams := reflect.TypeOf(serverapi.GetSessionTranscriptParams{})
	if _, ok := transcriptParams.FieldByName("Offset"); !ok {
		t.Fatal("generated transcript params missing Offset; spec must match the server/client offset query")
	}
	if _, ok := transcriptParams.FieldByName("Cursor"); ok {
		t.Fatal("generated transcript params still expose retired Cursor query")
	}

	var limit serverapi.Limit = 25
	_ = serverapi.StreamEventsParams{After: uint64Ptr(42), Epoch: stringPtr("epoch-1"), HeartbeatMs: intPtr(1000)}
	_ = serverapi.GetSessionOutputParams{From: int64Ptr(10), Limit: &limit}
	_ = serverapi.StreamSessionOutputParams{From: int64Ptr(18)}
}

func assertSameJSON(t *testing.T, name string, serverValue any, generatedValue any) {
	t.Helper()
	serverJSON, err := json.Marshal(serverValue)
	if err != nil {
		t.Fatalf("%s: marshal server value: %v", name, err)
	}
	generatedJSON, err := json.Marshal(generatedValue)
	if err != nil {
		t.Fatalf("%s: marshal generated value: %v", name, err)
	}
	var serverDecoded any
	if err := json.Unmarshal(serverJSON, &serverDecoded); err != nil {
		t.Fatalf("%s: decode server JSON: %v", name, err)
	}
	var generatedDecoded any
	if err := json.Unmarshal(generatedJSON, &generatedDecoded); err != nil {
		t.Fatalf("%s: decode generated JSON: %v", name, err)
	}
	if !reflect.DeepEqual(serverDecoded, generatedDecoded) {
		t.Fatalf("%s JSON mismatch\nserver:    %s\ngenerated: %s", name, serverJSON, generatedJSON)
	}
}

func uint64Ptr(v uint64) *uint64 { return &v }

func int64Ptr(v int64) *int64 { return &v }

func intPtr(v int) *int { return &v }

func stringPtr(v string) *string { return &v }
