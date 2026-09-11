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
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
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

func TestReviewSessionReadAndValidationAreSideEffectFree(t *testing.T) {
	store, f, _ := seedReviewSessionFeature(t, feature.StatusPlanNeedsReview, nil, "plan", "# Phase plan\n\n## Tasks\n\n### Task 1: Keep it safe\n")
	handler := NewHandler(HandlerOptions{
		Features:              store,
		FeatureStore:          store,
		DisableHostValidation: true,
	})

	created := doReviewSessionJSON[ReviewSessionResponse](t, handler, http.MethodPost, "/api/v1/features/"+f.ID+"/reviews", map[string]any{}, http.StatusOK)
	read := doReviewSessionJSON[ReviewSessionResponse](t, handler, http.MethodGet, "/api/v1/features/"+f.ID+"/reviews", nil, http.StatusOK)
	if read.Text != created.Text || read.DraftRevision != created.DraftRevision {
		t.Fatalf("GET review = %+v, want unchanged session %+v", read, created)
	}

	invalid := doReviewSessionJSON[ReviewDraftValidationResponse](t, handler, http.MethodPost, "/api/v1/features/"+f.ID+"/reviews/"+created.ReviewID+"/validate", ReviewDraftValidationRequest{Text: "# Phase plan\n"}, http.StatusOK)
	if invalid.Applicable != true || invalid.Valid || invalid.Revision != textRevision([]byte("# Phase plan\n")) || len(invalid.Findings) == 0 {
		t.Fatalf("invalid validation = %+v, want applicable failed result with revision and findings", invalid)
	}

	valid := doReviewSessionJSON[ReviewDraftValidationResponse](t, handler, http.MethodPost, "/api/v1/features/"+f.ID+"/reviews/"+created.ReviewID+"/validate", ReviewDraftValidationRequest{Text: created.Text}, http.StatusOK)
	if !valid.Applicable || !valid.Valid || valid.Revision != created.DraftRevision || len(valid.Findings) != 0 {
		t.Fatalf("valid validation = %+v, want passing result for unchanged draft", valid)
	}
	if valid.Findings == nil {
		t.Fatalf("valid validation findings = nil, want empty array for strict clients")
	}

	readAfter := doReviewSessionJSON[ReviewSessionResponse](t, handler, http.MethodGet, "/api/v1/features/"+f.ID+"/reviews", nil, http.StatusOK)
	if readAfter.Text != created.Text || readAfter.DraftRevision != created.DraftRevision {
		t.Fatalf("GET review after validation = %+v, want unchanged session %+v", readAfter, created)
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
		ID:            "feat-rewind-description-review",
		Name:          "Rewind description review",
		Status:        feature.StatusPromptNeedsReview,
		CurrentPhase:  feature.PhaseKnowledgeBase,
		ActiveRun:     1,
		RunCount:      1,
		Pipeline:      feature.PipelineMoonshot,
		SchemaVersion: feature.SchemaVersionCurrent,
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

func TestReviewSessionServiceSaveDraftSerializesRevisionCheckAndWrite(t *testing.T) {
	store, f, _ := seedReviewSessionFeature(t, feature.StatusPlanNeedsReview, nil, "plan", "# Plan\n")
	locks := newReviewSessionLockSet()
	service := newReviewSessionService(store, nil, locks)
	secondService := newReviewSessionService(store, nil, locks)
	resp, err := service.Create(f.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Widen the old race window after the revision check. Without the session
	// lock, concurrent writers can all pass the comparison before any writes.
	service.now = func() time.Time {
		time.Sleep(20 * time.Millisecond)
		return time.Now().UTC()
	}
	secondService.now = service.now

	const writers = 12
	start := make(chan struct{})
	results := make(chan error, writers)
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			writer := service
			if i%2 == 1 {
				writer = secondService
			}
			_, err := writer.SaveDraft(f.ID, resp.ReviewID, ReviewDraftUpdateRequest{
				BaseRevision: resp.DraftRevision,
				Text:         fmt.Sprintf("# Writer %d\n", i),
			})
			results <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)

	succeeded := 0
	conflicted := 0
	for err := range results {
		if err == nil {
			succeeded++
			continue
		}
		var conflict *ActionConflictError
		if errors.As(err, &conflict) {
			conflicted++
			continue
		}
		t.Fatalf("SaveDraft concurrent error = %T %v", err, err)
	}
	if succeeded != 1 || conflicted != writers-1 {
		t.Fatalf("concurrent results = %d success, %d conflicts; want 1 and %d", succeeded, conflicted, writers-1)
	}

	meta, draftPath, _, err := service.loadMetaForFeature(f.ID, resp.ReviewID)
	if err != nil {
		t.Fatalf("load final review metadata: %v", err)
	}
	draft, err := os.ReadFile(draftPath)
	if err != nil {
		t.Fatalf("read final draft: %v", err)
	}
	if meta.DraftRevision != textRevision(draft) {
		t.Fatalf("metadata revision = %q, draft revision = %q", meta.DraftRevision, textRevision(draft))
	}
}

func TestReviewSessionServiceDecisionCommitsDraftBeforeDelegate(t *testing.T) {
	store, f, planPath := seedReviewSessionFeature(t, feature.StatusPlanNeedsReview, nil, "plan", "# Plan\n")
	var delegated bool
	service := newReviewSessionService(store, func(featureID string, req ReviewDecisionRequest) error {
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
		return nil
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
		SchemaVersion:      feature.SchemaVersionCurrent,
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

const roadmapReviewValidText = `# Roadmap

## Phase 1: Skeleton

### Goal

Ship the skeleton.

## Pull Requests

| # | Title | Phases | Rationale |
|---|---|---|---|
| 1 | Skeleton | 1 | One phase, one reviewable slice. |
`

const roadmapReviewInvalidText = `# Roadmap

## Phase 1: Skeleton

### Goal

Ship the skeleton.
`

const roadmapReviewSingleTwoRowText = `# Roadmap

## Phase 1: Skeleton

### Goal

Ship the skeleton.

## Phase 2: Polish

### Goal

Polish it.

## Pull Requests

| # | Title | Phases | Rationale |
|---|---|---|---|
| 1 | Skeleton | 1 | First slice. |
| 2 | Polish | 2 | Second slice. |
`

const roadmapReviewSingleOneRowText = `# Roadmap

## Phase 1: Skeleton

### Goal

Ship the skeleton.

## Phase 2: Polish

### Goal

Polish it.

## Pull Requests

| # | Title | Phases | Rationale |
|---|---|---|---|
| 1 | Whole feature | 1-2 | Delivered as one pull request. |
`

// seedSingleDeliveryRoadmapReviewFeature seeds a roadmap-review feature
// whose delivery mode is single: its `## Pull Requests` table must carry
// exactly one row covering every phase.
func seedSingleDeliveryRoadmapReviewFeature(t *testing.T, body string) (*feature.Store, *feature.Feature, string) {
	t.Helper()
	store, f, roadmapPath := seedReviewSessionFeature(t, feature.StatusPlanNeedsReview, nil, "roadmap", body)
	f.DeliveryMode = feature.DeliveryModeSingle
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature with single delivery mode: %v", err)
	}
	return store, f, roadmapPath
}

func TestReviewSessionRoadmapDraftValidationSingleDelivery(t *testing.T) {
	store, f, _ := seedSingleDeliveryRoadmapReviewFeature(t, roadmapReviewSingleOneRowText)
	handler := NewHandler(HandlerOptions{
		Features:              store,
		FeatureStore:          store,
		DisableHostValidation: true,
	})

	created := doReviewSessionJSON[ReviewSessionResponse](t, handler, http.MethodPost, "/api/v1/features/"+f.ID+"/reviews", map[string]any{}, http.StatusOK)
	if created.ArtifactID != "roadmap" {
		t.Fatalf("created review session artifact = %q, want roadmap", created.ArtifactID)
	}

	twoRow := doReviewSessionJSON[ReviewDraftValidationResponse](t, handler, http.MethodPost, "/api/v1/features/"+f.ID+"/reviews/"+created.ReviewID+"/validate", ReviewDraftValidationRequest{Text: roadmapReviewSingleTwoRowText}, http.StatusOK)
	if !twoRow.Applicable || twoRow.Valid || len(twoRow.Findings) != 1 {
		t.Fatalf("two-row single-delivery validation = %+v, want applicable failed result with one finding", twoRow)
	}
	if twoRow.Findings[0].Code != "pull_requests_table" {
		t.Fatalf("two-row finding code = %q, want pull_requests_table", twoRow.Findings[0].Code)
	}
	if !strings.Contains(twoRow.Findings[0].Message, "## Pull Requests") || !strings.Contains(twoRow.Findings[0].Message, "single pull request") {
		t.Fatalf("two-row finding = %+v, want a problem naming the section and the single-pull-request delivery", twoRow.Findings[0])
	}

	oneRow := doReviewSessionJSON[ReviewDraftValidationResponse](t, handler, http.MethodPost, "/api/v1/features/"+f.ID+"/reviews/"+created.ReviewID+"/validate", ReviewDraftValidationRequest{Text: roadmapReviewSingleOneRowText}, http.StatusOK)
	if !oneRow.Applicable || !oneRow.Valid || len(oneRow.Findings) != 0 {
		t.Fatalf("one-row single-delivery validation = %+v, want applicable passing result without findings", oneRow)
	}
}

func TestReviewSessionServiceRoadmapProceedSingleDeliveryTwoRowBlocked(t *testing.T) {
	store, f, roadmapPath := seedSingleDeliveryRoadmapReviewFeature(t, roadmapReviewSingleOneRowText)
	var delegated bool
	service := newReviewSessionService(store, func(featureID string, req ReviewDecisionRequest) error {
		delegated = true
		return nil
	})
	created, err := service.Create(f.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	saved, err := service.SaveDraft(f.ID, created.ReviewID, ReviewDraftUpdateRequest{
		BaseRevision: created.DraftRevision,
		Text:         roadmapReviewSingleTwoRowText,
	})
	if err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	_, err = service.SubmitDecision(f.ID, created.ReviewID, ReviewSessionDecisionRequest{
		Decision:     reviewDecisionProceed,
		BaseRevision: saved.DraftRevision,
	})
	if err == nil {
		t.Fatal("SubmitDecision proceed on a two-row single-delivery draft must fail")
	}
	var conflict *ActionConflictError
	if !errors.As(err, &conflict) || conflict.Code != errcat.RoadmapPullRequestsInvalid {
		t.Fatalf("SubmitDecision error = %v, want action conflict with the roadmap pull-requests code", err)
	}
	if !strings.Contains(conflict.Detail, "## Pull Requests") || !strings.Contains(conflict.Detail, "single pull request") {
		t.Fatalf("conflict detail = %q, want the single-delivery table problem", conflict.Detail)
	}
	if delegated {
		t.Fatal("review decision delegate must not be invoked for a two-row single-delivery draft")
	}
	canonical, err := os.ReadFile(roadmapPath)
	if err != nil {
		t.Fatalf("read canonical roadmap: %v", err)
	}
	if string(canonical) != roadmapReviewSingleOneRowText {
		t.Fatalf("canonical roadmap = %q, want the previous content untouched", canonical)
	}
}

func TestReviewSessionServiceRoadmapIterateNotBlockedBySingleDelivery(t *testing.T) {
	store, f, roadmapPath := seedSingleDeliveryRoadmapReviewFeature(t, roadmapReviewSingleOneRowText)
	var delegated bool
	service := newReviewSessionService(store, func(featureID string, req ReviewDecisionRequest) error {
		delegated = true
		return nil
	})
	created, err := service.Create(f.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	saved, err := service.SaveDraft(f.ID, created.ReviewID, ReviewDraftUpdateRequest{
		BaseRevision: created.DraftRevision,
		Text:         roadmapReviewSingleTwoRowText,
	})
	if err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	if _, err := service.SubmitDecision(f.ID, created.ReviewID, ReviewSessionDecisionRequest{
		Decision:     reviewDecisionIterate,
		BaseRevision: saved.DraftRevision,
	}); err != nil {
		t.Fatalf("SubmitDecision iterate on a two-row single-delivery draft must not be blocked: %v", err)
	}
	if !delegated {
		t.Fatal("review decision delegate must be invoked for iterate")
	}
	canonical, err := os.ReadFile(roadmapPath)
	if err != nil {
		t.Fatalf("read canonical roadmap: %v", err)
	}
	if string(canonical) != roadmapReviewSingleTwoRowText {
		t.Fatalf("canonical roadmap = %q, want the committed draft", canonical)
	}
}

func TestReviewSessionRoadmapDraftValidation(t *testing.T) {
	store, f, _ := seedReviewSessionFeature(t, feature.StatusPlanNeedsReview, nil, "roadmap", roadmapReviewValidText)
	handler := NewHandler(HandlerOptions{
		Features:              store,
		FeatureStore:          store,
		DisableHostValidation: true,
	})

	created := doReviewSessionJSON[ReviewSessionResponse](t, handler, http.MethodPost, "/api/v1/features/"+f.ID+"/reviews", map[string]any{}, http.StatusOK)
	if created.ArtifactID != "roadmap" {
		t.Fatalf("created review session artifact = %q, want roadmap", created.ArtifactID)
	}

	invalid := doReviewSessionJSON[ReviewDraftValidationResponse](t, handler, http.MethodPost, "/api/v1/features/"+f.ID+"/reviews/"+created.ReviewID+"/validate", ReviewDraftValidationRequest{Text: roadmapReviewInvalidText}, http.StatusOK)
	if !invalid.Applicable || invalid.Valid || len(invalid.Findings) == 0 {
		t.Fatalf("invalid roadmap validation = %+v, want applicable failed result with findings", invalid)
	}
	if !strings.Contains(invalid.Findings[0].Message, "## Pull Requests") {
		t.Fatalf("first finding = %+v, want a problem naming the section", invalid.Findings[0])
	}

	valid := doReviewSessionJSON[ReviewDraftValidationResponse](t, handler, http.MethodPost, "/api/v1/features/"+f.ID+"/reviews/"+created.ReviewID+"/validate", ReviewDraftValidationRequest{Text: created.Text}, http.StatusOK)
	if !valid.Applicable || !valid.Valid || len(valid.Findings) != 0 {
		t.Fatalf("valid roadmap validation = %+v, want applicable passing result without findings", valid)
	}
}

func TestReviewSessionServiceRoadmapProceedInvalidTableBlocked(t *testing.T) {
	store, f, roadmapPath := seedReviewSessionFeature(t, feature.StatusPlanNeedsReview, nil, "roadmap", roadmapReviewValidText)
	var delegated bool
	service := newReviewSessionService(store, func(featureID string, req ReviewDecisionRequest) error {
		delegated = true
		return nil
	})
	created, err := service.Create(f.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	saved, err := service.SaveDraft(f.ID, created.ReviewID, ReviewDraftUpdateRequest{
		BaseRevision: created.DraftRevision,
		Text:         roadmapReviewInvalidText,
	})
	if err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	_, err = service.SubmitDecision(f.ID, created.ReviewID, ReviewSessionDecisionRequest{
		Decision:     reviewDecisionProceed,
		BaseRevision: saved.DraftRevision,
	})
	if err == nil {
		t.Fatal("SubmitDecision proceed on invalid table must fail")
	}
	var conflict *ActionConflictError
	if !errors.As(err, &conflict) || conflict.Code != errcat.RoadmapPullRequestsInvalid {
		t.Fatalf("SubmitDecision error = %v, want action conflict with the roadmap pull-requests code", err)
	}
	if !strings.Contains(conflict.Detail, "## Pull Requests") {
		t.Fatalf("conflict detail = %q, want the table problems", conflict.Detail)
	}
	if delegated {
		t.Fatal("review decision delegate must not be invoked for an invalid roadmap table")
	}
	canonical, err := os.ReadFile(roadmapPath)
	if err != nil {
		t.Fatalf("read canonical roadmap: %v", err)
	}
	if string(canonical) != roadmapReviewValidText {
		t.Fatalf("canonical roadmap = %q, want the previous content untouched", canonical)
	}

	// The session stays readable and editable at the same draft revision.
	read, err := service.Read(f.ID)
	if err != nil {
		t.Fatalf("Read after blocked decision: %v", err)
	}
	if read.DraftRevision != saved.DraftRevision || read.Text != roadmapReviewInvalidText {
		t.Fatalf("read session = %+v, want the saved draft at its revision", read)
	}
	edited, err := service.SaveDraft(f.ID, created.ReviewID, ReviewDraftUpdateRequest{
		BaseRevision: saved.DraftRevision,
		Text:         roadmapReviewValidText,
	})
	if err != nil {
		t.Fatalf("SaveDraft after blocked decision: %v", err)
	}
	if edited.DraftRevision == saved.DraftRevision {
		t.Fatal("saving a fixed draft must move the revision")
	}
}

func TestReviewSessionServiceRoadmapProceedValidCommitsDraft(t *testing.T) {
	store, f, roadmapPath := seedReviewSessionFeature(t, feature.StatusPlanNeedsReview, nil, "roadmap", roadmapReviewValidText)
	var delegated bool
	var delegatedRoadmap bool
	service := newReviewSessionService(store, func(featureID string, req ReviewDecisionRequest) error {
		delegated = true
		delegatedRoadmap = req.Roadmap
		return nil
	})
	created, err := service.Create(f.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	saved, err := service.SaveDraft(f.ID, created.ReviewID, ReviewDraftUpdateRequest{
		BaseRevision: created.DraftRevision,
		Text:         strings.Replace(roadmapReviewValidText, "One phase, one reviewable slice.", "Edited at the gate.", 1),
	})
	if err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	if _, err := service.SubmitDecision(f.ID, created.ReviewID, ReviewSessionDecisionRequest{
		Decision:     reviewDecisionProceed,
		BaseRevision: saved.DraftRevision,
	}); err != nil {
		t.Fatalf("SubmitDecision proceed on valid table: %v", err)
	}
	if !delegated || !delegatedRoadmap {
		t.Fatal("review decision delegate must be invoked with the roadmap flag")
	}
	canonical, err := os.ReadFile(roadmapPath)
	if err != nil {
		t.Fatalf("read canonical roadmap: %v", err)
	}
	if !strings.Contains(string(canonical), "Edited at the gate.") {
		t.Fatalf("canonical roadmap = %q, want the committed draft", canonical)
	}
}

func TestReviewSessionServiceRoadmapIterateNeverBlockedByTable(t *testing.T) {
	store, f, roadmapPath := seedReviewSessionFeature(t, feature.StatusPlanNeedsReview, nil, "roadmap", roadmapReviewValidText)
	var delegated bool
	service := newReviewSessionService(store, func(featureID string, req ReviewDecisionRequest) error {
		delegated = true
		return nil
	})
	created, err := service.Create(f.ID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	saved, err := service.SaveDraft(f.ID, created.ReviewID, ReviewDraftUpdateRequest{
		BaseRevision: created.DraftRevision,
		Text:         roadmapReviewInvalidText,
	})
	if err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	if _, err := service.SubmitDecision(f.ID, created.ReviewID, ReviewSessionDecisionRequest{
		Decision:     reviewDecisionIterate,
		BaseRevision: saved.DraftRevision,
	}); err != nil {
		t.Fatalf("SubmitDecision iterate on invalid table must not be blocked: %v", err)
	}
	if !delegated {
		t.Fatal("review decision delegate must be invoked for iterate")
	}
	canonical, err := os.ReadFile(roadmapPath)
	if err != nil {
		t.Fatalf("read canonical roadmap: %v", err)
	}
	if string(canonical) != roadmapReviewInvalidText {
		t.Fatalf("canonical roadmap = %q, want the committed draft", canonical)
	}
}

func TestReviewSessionRoadmapProceedInvalidTableRendersCanonicalError(t *testing.T) {
	store, f, _ := seedReviewSessionFeature(t, feature.StatusPlanNeedsReview, nil, "roadmap", roadmapReviewValidText)
	handler := NewHandler(HandlerOptions{
		Features:              store,
		FeatureStore:          store,
		DisableHostValidation: true,
	})
	created := doReviewSessionJSON[ReviewSessionResponse](t, handler, http.MethodPost, "/api/v1/features/"+f.ID+"/reviews", map[string]any{}, http.StatusOK)
	saved := doReviewSessionJSON[ReviewSessionResponse](t, handler, http.MethodPut, "/api/v1/features/"+f.ID+"/reviews/"+created.ReviewID+"/draft", ReviewDraftUpdateRequest{
		BaseRevision: created.DraftRevision,
		Text:         roadmapReviewInvalidText,
	}, http.StatusOK)

	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(ReviewSessionDecisionRequest{
		Decision:     reviewDecisionProceed,
		BaseRevision: saved.DraftRevision,
	}); err != nil {
		t.Fatalf("encode request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/features/"+f.ID+"/reviews/"+created.ReviewID+"/decision", &buf)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agentico-Client", trustedClientHeaderValue)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("decision status = %d, want %d", resp.StatusCode, http.StatusConflict)
	}
	var envelope ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	if envelope.Error.Code != string(errcat.RoadmapPullRequestsInvalid) {
		t.Fatalf("error code = %q, want %q", envelope.Error.Code, errcat.RoadmapPullRequestsInvalid)
	}
	if envelope.Error.Class != ErrorClass(errcat.ClassBlocking) {
		t.Fatalf("error class = %q, want %q", envelope.Error.Class, errcat.ClassBlocking)
	}
	if !strings.Contains(envelope.Error.Diagnostics, "## Pull Requests") {
		t.Fatalf("error diagnostics = %q, want the table problems", envelope.Error.Diagnostics)
	}
}
