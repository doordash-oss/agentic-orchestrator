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
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

func TestReviewSessionRoutesCommitDraftViaREST(t *testing.T) {
	store, f, planPath := seedReviewSessionFeature(t, feature.StatusPlanNeedsReview, nil, "plan", "# Plan\n")
	handler := NewHandler(HandlerOptions{
		Features:              store,
		FeatureStore:          store,
		DisableHostValidation: true,
	})

	created := doReviewSessionJSON[ReviewSessionResponse](t, handler, http.MethodPost, "/api/v1/features/"+f.ID+"/reviews", map[string]any{}, http.StatusOK)
	if created.ReviewID == "" || strings.Contains(mustMarshalJSON(t, created), planPath) {
		t.Fatalf("created review session = %+v, must have id and not leak %q", created, planPath)
	}

	got := doReviewSessionJSON[ReviewSessionResponse](t, handler, http.MethodGet, "/api/v1/features/"+f.ID+"/reviews/"+created.ReviewID, nil, http.StatusOK)
	if got.Text != "# Plan\n" || got.DraftRevision != created.DraftRevision {
		t.Fatalf("GET review session = %+v, want created draft", got)
	}

	saved := doReviewSessionJSON[ReviewSessionResponse](t, handler, http.MethodPut, "/api/v1/features/"+f.ID+"/reviews/"+created.ReviewID+"/draft", ReviewDraftUpdateRequest{
		BaseRevision: created.DraftRevision,
		Text:         "# Edited by REST\n",
	}, http.StatusOK)
	if saved.Text != "# Edited by REST\n" || saved.DraftRevision == created.DraftRevision {
		t.Fatalf("saved review session = %+v, want edited text and new revision", saved)
	}

	decision := doReviewSessionJSON[ReviewSessionDecisionResponse](t, handler, http.MethodPost, "/api/v1/features/"+f.ID+"/reviews/"+created.ReviewID+"/decision", ReviewSessionDecisionRequest{
		Decision:     reviewDecisionProceed,
		BaseRevision: saved.DraftRevision,
	}, http.StatusOK)
	if decision.Result != "submitted" || decision.ReviewID != created.ReviewID {
		t.Fatalf("decision response = %+v, want submitted review id", decision)
	}
	data, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("read canonical artifact: %v", err)
	}
	if string(data) != "# Edited by REST\n" {
		t.Fatalf("canonical artifact = %q, want committed draft", string(data))
	}
}

func TestReviewSessionRoutesCancelLeavesCanonicalArtifact(t *testing.T) {
	store, f, planPath := seedReviewSessionFeature(t, feature.StatusPlanNeedsReview, nil, "plan", "# Plan\n")
	handler := NewHandler(HandlerOptions{
		Features:              store,
		FeatureStore:          store,
		DisableHostValidation: true,
	})

	created := doReviewSessionJSON[ReviewSessionResponse](t, handler, http.MethodPost, "/api/v1/features/"+f.ID+"/reviews", map[string]any{}, http.StatusOK)
	_ = doReviewSessionJSON[ReviewSessionResponse](t, handler, http.MethodPut, "/api/v1/features/"+f.ID+"/reviews/"+created.ReviewID+"/draft", ReviewDraftUpdateRequest{
		BaseRevision: created.DraftRevision,
		Text:         "# Cancelled draft\n",
	}, http.StatusOK)
	cancelled := doReviewSessionJSON[ReviewSessionDecisionResponse](t, handler, http.MethodDelete, "/api/v1/features/"+f.ID+"/reviews/"+created.ReviewID, map[string]any{}, http.StatusOK)
	if cancelled.Result != "cancelled" {
		t.Fatalf("cancel response = %+v, want cancelled", cancelled)
	}
	data, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("read canonical artifact: %v", err)
	}
	if string(data) != "# Plan\n" {
		t.Fatalf("canonical artifact = %q, want original after cancel", string(data))
	}
}

func TestReviewSessionServiceCreateDoesNotExposeSourcePath(t *testing.T) {
	store, f, planPath := seedReviewSessionFeature(t, feature.StatusPlanNeedsReview, nil, "plan", "# Plan\n")
	service := newReviewSessionService(store, nil)

	resp, err := service.Create(f.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if resp.FeatureID != f.ID || resp.RunNumber != 1 || resp.ArtifactID != "plan" {
		t.Fatalf("response identity = %+v, want feature/run/artifact", resp)
	}
	if resp.ReviewMode != reviewModePlan || resp.TargetPhase != feature.PhaseImplement.DirName() {
		t.Fatalf("review target = mode %q phase %q", resp.ReviewMode, resp.TargetPhase)
	}
	if resp.Text != "# Plan\n" {
		t.Fatalf("Text = %q, want plan content", resp.Text)
	}
	if resp.DraftRevision == "" || resp.SourceRevision == "" {
		t.Fatalf("revisions missing: %+v", resp)
	}
	if resp.CanIterate != true {
		t.Fatalf("CanIterate = false, want true for plan review")
	}
	if strings.Contains(mustMarshalJSON(t, resp), planPath) {
		t.Fatalf("review session response leaked source path %q: %+v", planPath, resp)
	}
}

func TestReviewSessionServiceCreateUsesFeatureRootDescriptionReviewForRewindToInquire(t *testing.T) {
	store := feature.NewStore(t.TempDir())
	target := feature.PhaseInquire
	f := &feature.Feature{
		ID:           "feat-rewind-description-review",
		Name:         "Rewind description review",
		Status:       feature.StatusPromptNeedsReview,
		CurrentPhase: feature.PhaseKnowledgeBase,
		ActiveRun:    1,
		RunCount:     1,
		Pipeline:     feature.PipelineMoonshot,
	}
	f.SetRun(&feature.Run{
		RunNumber:          1,
		PendingReviewPhase: &target,
		IsRewind:           true,
		Artifacts:          map[string]string{},
	})
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}
	descPath := filepath.Join(store.BaseDir, f.ID, "description-review.md")
	if err := os.WriteFile(descPath, []byte("edited prompt\n"), 0o644); err != nil {
		t.Fatalf("write description-review.md: %v", err)
	}
	service := newReviewSessionService(store, nil)

	resp, err := service.Create(f.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if resp.ReviewMode != reviewModeRewind || resp.TargetPhase != feature.PhaseInquire.DirName() {
		t.Fatalf("review target = mode %q phase %q, want rewind inquire", resp.ReviewMode, resp.TargetPhase)
	}
	if resp.ArtifactID != descriptionReviewArtifact {
		t.Fatalf("ArtifactID = %q, want %q", resp.ArtifactID, descriptionReviewArtifact)
	}
	if resp.Text != "edited prompt\n" {
		t.Fatalf("Text = %q, want description review content", resp.Text)
	}
	if resp.CanIterate {
		t.Fatalf("CanIterate = true, want false for rewind review")
	}
}

func TestReviewSessionServiceCreateSuppressesIterateForApprovedPlanAttempt(t *testing.T) {
	store, f, planPath := seedReviewSessionFeature(t, feature.StatusPlanNeedsReview, nil, "plan", "# Plan\n")
	if err := agent.WritePlanAttemptMeta(filepath.Dir(planPath), agent.PlanAttemptMeta{
		Attempt:      1,
		AgentStatus:  "SUCCESS",
		ReviewStatus: agent.ReviewApproved.String(),
	}); err != nil {
		t.Fatalf("write plan attempt meta: %v", err)
	}
	service := newReviewSessionService(store, nil)

	resp, err := service.Create(f.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if resp.CanIterate {
		t.Fatalf("CanIterate = true, want false for approved plan checkpoint review")
	}
}

func TestReviewSessionServiceCreateRefreshesExistingDraftCanIterate(t *testing.T) {
	store, f, planPath := seedReviewSessionFeature(t, feature.StatusPlanNeedsReview, nil, "plan", "# Plan\n")
	service := newReviewSessionService(store, nil)
	initial, err := service.Create(f.ID)
	if err != nil {
		t.Fatalf("Create initial: %v", err)
	}
	if !initial.CanIterate {
		t.Fatalf("initial CanIterate = false, want true before approved attempt metadata exists")
	}
	if err := agent.WritePlanAttemptMeta(filepath.Dir(planPath), agent.PlanAttemptMeta{
		Attempt:      1,
		AgentStatus:  "SUCCESS",
		ReviewStatus: agent.ReviewApproved.String(),
	}); err != nil {
		t.Fatalf("write plan attempt meta: %v", err)
	}

	reopened, err := service.Create(f.ID)
	if err != nil {
		t.Fatalf("Create reopened: %v", err)
	}

	if reopened.ReviewID != initial.ReviewID {
		t.Fatalf("ReviewID = %q, want existing deterministic review %q", reopened.ReviewID, initial.ReviewID)
	}
	if reopened.CanIterate {
		t.Fatalf("CanIterate = true, want false after approved plan metadata appears")
	}
	got, err := service.Get(f.ID, reopened.ReviewID)
	if err != nil {
		t.Fatalf("Get reopened: %v", err)
	}
	if got.CanIterate {
		t.Fatalf("persisted CanIterate = true, want false after reopening approved plan review")
	}
}

func TestReviewSessionServiceSaveDraftRejectsStaleRevision(t *testing.T) {
	store, f, _ := seedReviewSessionFeature(t, feature.StatusPlanNeedsReview, nil, "plan", "# Plan\n")
	service := newReviewSessionService(store, nil)
	resp, err := service.Create(f.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	updated, err := service.SaveDraft(f.ID, resp.ReviewID, ReviewDraftUpdateRequest{
		BaseRevision: resp.DraftRevision,
		Text:         "# Edited\n",
	})
	if err != nil {
		t.Fatalf("SaveDraft current revision: %v", err)
	}
	if updated.Text != "# Edited\n" || updated.DraftRevision == resp.DraftRevision {
		t.Fatalf("updated draft = %+v, want new text and revision", updated)
	}

	_, err = service.SaveDraft(f.ID, resp.ReviewID, ReviewDraftUpdateRequest{
		BaseRevision: resp.DraftRevision,
		Text:         "# Stale\n",
	})
	var conflict *ActionConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("SaveDraft stale err = %T %v, want ActionConflictError", err, err)
	}
}

func TestReviewSessionServiceDecisionCommitsDraftBeforeDelegate(t *testing.T) {
	store, f, planPath := seedReviewSessionFeature(t, feature.StatusPlanNeedsReview, nil, "plan", "# Plan\n")
	var delegated bool
	service := newReviewSessionService(store, func(featureID string, req ReviewDecisionRequest) (ReviewDecisionResponse, error) {
		delegated = true
		if featureID != f.ID {
			t.Fatalf("delegate featureID = %q, want %q", featureID, f.ID)
		}
		if req.Decision != reviewDecisionProceed || req.Phase != feature.PhaseImplement.DirName() {
			t.Fatalf("delegate request = %+v, want proceed implement", req)
		}
		data, err := os.ReadFile(planPath)
		if err != nil {
			t.Fatalf("delegate read canonical artifact: %v", err)
		}
		if string(data) != "# Edited\n" {
			t.Fatalf("canonical content before delegate = %q, want edited draft", string(data))
		}
		return ReviewDecisionResponse{FeatureID: featureID, Decision: req.Decision, Result: "submitted"}, nil
	})
	resp, err := service.Create(f.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	updated, err := service.SaveDraft(f.ID, resp.ReviewID, ReviewDraftUpdateRequest{
		BaseRevision: resp.DraftRevision,
		Text:         "# Edited\n",
	})
	if err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	decision, err := service.SubmitDecision(f.ID, resp.ReviewID, ReviewSessionDecisionRequest{
		Decision:     reviewDecisionProceed,
		BaseRevision: updated.DraftRevision,
	})
	if err != nil {
		t.Fatalf("SubmitDecision: %v", err)
	}
	if !delegated {
		t.Fatal("review decision delegate was not called")
	}
	if decision.FeatureID != f.ID || decision.ReviewID != resp.ReviewID || decision.Result != "submitted" {
		t.Fatalf("decision response = %+v, want submitted response", decision)
	}
}

func seedReviewSessionFeature(t *testing.T, status feature.Status, pending *feature.Phase, artifactID, body string) (*feature.Store, *feature.Feature, string) {
	t.Helper()
	store := feature.NewStore(t.TempDir())
	f := &feature.Feature{
		ID:                 "feat-review-session",
		Name:               "Review session",
		Status:             status,
		CurrentPhase:       feature.PhasePlan,
		ActiveRun:          1,
		RunCount:           1,
		PendingReviewPhase: pending,
		Artifacts:          map[string]string{},
	}
	runDir := store.RunDir(f.ID, 1)
	artifactPath := filepath.Join(runDir, artifactID, artifactID+".md")
	if err := os.MkdirAll(filepath.Dir(artifactPath), 0o755); err != nil {
		t.Fatalf("mkdir artifact dir: %v", err)
	}
	if err := os.WriteFile(artifactPath, []byte(body), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	f.Artifacts[artifactID] = artifactPath
	f.SetRun(&feature.Run{
		RunNumber:          1,
		Artifacts:          f.Artifacts,
		PendingReviewPhase: pending,
	})
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}
	return store, f, artifactPath
}

func doReviewSessionJSON[T any](t *testing.T, handler http.Handler, method, path string, body any, wantStatus int) T {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode request: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet {
		req.Header.Set("X-Agentico-Client", trustedClientHeaderValue)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		t.Fatalf("%s %s status = %d, want %d", method, path, resp.StatusCode, wantStatus)
	}
	var out T
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return out
}
