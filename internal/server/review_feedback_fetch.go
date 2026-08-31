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
	"errors"
	"fmt"
	"net/http"
	"os"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	gitadapter "github.com/doordash-oss/agentic-orchestrator/internal/git"
)

const (
	actionReviewFeedback                         = "review-feedback"
	reviewFeedbackSubactionFetch                 = "fetch"
	reviewFeedbackSubactionSelection             = "selection"
	errCodeReviewFeedbackFetchFailed             = "review_feedback_fetch_failed"
	errCodeReviewFeedbackDraftNotFound           = "review_feedback_draft_not_found"
	errCodeReviewFeedbackRevisionConflict        = "review_feedback_revision_conflict"
	errCodeReviewFeedbackUnknownReference        = "review_feedback_unknown_reference"
	errCodeReviewFeedbackMalformedReference      = "review_feedback_malformed_reference"
	errCodeReviewFeedbackSelectionBounds         = "review_feedback_selection_update_too_large"
	errCodeReviewFeedbackZeroLaunchableSelection = "review_feedback_zero_launchable_selection"
)

// maxReviewFeedbackSelectionUpdates bounds one reference-only selection
// mutation; it comfortably exceeds any realistic visible comment count.
const maxReviewFeedbackSelectionUpdates = 512

type addressedReviewFeedbackIDReader interface {
	LoadAddressedReviewFeedbackIDs(parentID, repoName string) (map[int]bool, error)
}

type reviewFeedbackDraftStore interface {
	LoadReviewFeedbackDraft(parentID string) (*feature.ReviewFeedbackDraft, error)
	SaveReviewFeedbackDraft(parentID string, draft *feature.ReviewFeedbackDraft, expectedRevision int64) error
}

// reviewFeedbackLaunchReceiptReader reports the durable launch receipt of
// the active review-feedback child, when one exists.
type reviewFeedbackLaunchReceiptReader interface {
	ActiveReviewFeedbackLaunchReceipt(parentID string) (*feature.Feature, *feature.ReviewFeedbackLaunchReceipt, error)
}

// reviewFeedbackDraftCleaner atomically deletes a pending draft consumed by a
// durable launch receipt, never a newer committed revision.
type reviewFeedbackDraftCleaner interface {
	DeleteReviewFeedbackDraftIfRevision(parentID string, revision int64) error
}

func reviewFeedbackFetchPath(featureID string) string {
	return featureActionPath(featureID, actionReviewFeedback) + "/" + reviewFeedbackSubactionFetch
}

func reviewFeedbackSelectionPath(featureID string) string {
	return featureActionPath(featureID, actionReviewFeedback) + "/" + reviewFeedbackSubactionSelection
}

func (h *apiHandler) handleReviewFeedbackFetchTrusted(w http.ResponseWriter, r *http.Request, parentID string) {
	var req ReviewFeedbackFetchRequest
	if !decodeMutationJSON(w, r, &req) {
		return
	}
	if len(req) != 0 {
		writeAPIError(w, http.StatusBadRequest, errCodeBadRequest, "review-feedback fetch request must be empty", nil)
		return
	}
	if h.store == nil {
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "feature store unavailable", nil)
		return
	}
	parent, err := h.store.Load(parentID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeAPIError(w, http.StatusNotFound, "not_found", "feature not found", map[string]any{"feature_id": parentID})
			return
		}
		writeAPIError(w, http.StatusInternalServerError, "feature_read_failed", "read feature", map[string]any{"feature_id": parentID})
		return
	}
	addressedReader, ok := h.store.(addressedReviewFeedbackIDReader)
	if !ok {
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "review-feedback addressed-ID store unavailable", nil)
		return
	}
	draftStore, ok := h.store.(reviewFeedbackDraftStore)
	if !ok {
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "review-feedback draft store unavailable", nil)
		return
	}

	fetched := make(map[string][]feature.ReviewFeedbackComment, len(parent.Repos))
	prURLs := parent.PRURLs()
	for _, repo := range parent.Repos {
		prURL := prURLs[repo.Name]
		if prURL == "" {
			continue
		}
		addressed, loadErr := addressedReader.LoadAddressedReviewFeedbackIDs(parent.ID, repo.Name)
		if loadErr != nil {
			writeReviewFeedbackFetchError(w, repo.Name, loadErr)
			return
		}
		repoPath := repo.WorktreePath
		if repoPath == "" {
			repoPath = repo.Path
		}
		comments, fetchErr := gitadapter.FetchPRComments(repoPath, prURL)
		if fetchErr != nil {
			writeReviewFeedbackFetchError(w, repo.Name, fetchErr)
			return
		}
		for _, comment := range comments {
			if addressed[comment.ID] {
				continue
			}
			fetched[repo.Name] = append(fetched[repo.Name], feature.ReviewFeedbackComment{
				Repo:      repo.Name,
				ID:        comment.ID,
				Type:      comment.Type,
				Path:      comment.Path,
				Line:      comment.Line,
				Author:    comment.User.Login,
				Body:      comment.Body,
				DiffHunk:  comment.DiffHunk,
				InReplyTo: comment.InReplyTo,
				CreatedAt: comment.CreatedAt,
			})
		}
	}

	existing, loadErr := draftStore.LoadReviewFeedbackDraft(parent.ID)
	if loadErr != nil {
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "read review feedback draft", nil)
		return
	}
	// Converge a pending draft already consumed by a durable launch receipt:
	// an ordinary refresh must recognize the receipt, finish the draft
	// cleanup an interrupted launch could not, and rebase the view onto the
	// current authoritative feedback instead of relaunching a consumed draft.
	// The marker is either an active review-feedback child's receipt or a
	// still-pending durable creation intent (durable, awaiting roll-forward).
	if existing != nil {
		consumedRevision := int64(-1)
		if intent := parent.PendingChild; intent != nil && intent.Kind == feature.ChildKindReviewFeedback &&
			intent.LaunchReceipt != nil {
			consumedRevision = intent.LaunchReceipt.DraftRevision
		} else if receiptReader, ok := h.store.(reviewFeedbackLaunchReceiptReader); ok {
			_, receipt, receiptErr := receiptReader.ActiveReviewFeedbackLaunchReceipt(parent.ID)
			if receiptErr != nil {
				writeAPIError(w, http.StatusInternalServerError, "internal_error", "read review-feedback launch receipt", nil)
				return
			}
			if receipt != nil {
				consumedRevision = receipt.DraftRevision
			}
		}
		if consumedRevision == existing.Revision {
			if deleter, ok := h.store.(reviewFeedbackDraftCleaner); ok {
				// Compare-and-delete under the store lock: a revision
				// committed after the receipt is never treated as consumed.
				if delErr := deleter.DeleteReviewFeedbackDraftIfRevision(parent.ID, consumedRevision); delErr != nil {
					writeAPIError(w, http.StatusInternalServerError, "internal_error", "delete review feedback draft consumed by launch", nil)
					return
				}
				existing = nil
			}
		}
	}
	expectedRevision := int64(0)
	if existing != nil {
		expectedRevision = existing.Revision
	}
	draft := feature.ReconcileReviewFeedbackDraft(parent, existing, fetched)
	if saveErr := draftStore.SaveReviewFeedbackDraft(parent.ID, draft, expectedRevision); saveErr != nil {
		var conflict *feature.ReviewFeedbackRevisionConflictError
		var consumed *feature.ReviewFeedbackDraftConsumedError
		if errors.As(saveErr, &conflict) || errors.As(saveErr, &consumed) {
			writeAPIError(w, http.StatusConflict, errCodeReviewFeedbackRevisionConflict, saveErr.Error(), nil)
			return
		}
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "save review feedback draft", nil)
		return
	}

	writeJSON(w, http.StatusOK, ReviewFeedbackFetchResponse{
		APIVersion: APIVersion,
		Revision:   int(draft.Revision),
		SnapshotID: draft.SnapshotID,
		Repos:      reviewFeedbackDraftView(parent, draft),
	})
}

// handleReviewFeedbackSelectionTrusted commits a bounded, reference-only
// selection update against the pending draft's optimistic revision. The
// acknowledged response is always the next authoritative draft view.
func (h *apiHandler) handleReviewFeedbackSelectionTrusted(w http.ResponseWriter, r *http.Request, parentID string) {
	var req ReviewFeedbackSelectionRequest
	if !decodeMutationJSON(w, r, &req) {
		return
	}
	if len(req.Updates) == 0 || len(req.Updates) > maxReviewFeedbackSelectionUpdates {
		writeAPIError(w, http.StatusBadRequest, errCodeReviewFeedbackSelectionBounds,
			fmt.Sprintf("selection updates must contain between 1 and %d stable references", maxReviewFeedbackSelectionUpdates), nil)
		return
	}
	if h.store == nil {
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "feature store unavailable", nil)
		return
	}
	parent, err := h.store.Load(parentID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeAPIError(w, http.StatusNotFound, "not_found", "feature not found", map[string]any{"feature_id": parentID})
			return
		}
		writeAPIError(w, http.StatusInternalServerError, "feature_read_failed", "read feature", map[string]any{"feature_id": parentID})
		return
	}
	draftStore, ok := h.store.(reviewFeedbackDraftStore)
	if !ok {
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "review-feedback draft store unavailable", nil)
		return
	}

	updates := make(map[feature.StableReviewFeedbackRef]bool, len(req.Updates))
	for _, update := range req.Updates {
		ref := feature.StableReviewFeedbackRef(update.StableRef)
		if _, _, _, parseErr := feature.ParseStableReviewFeedbackRef(update.StableRef); parseErr != nil {
			writeAPIError(w, http.StatusBadRequest, errCodeReviewFeedbackMalformedReference, parseErr.Error(), nil)
			return
		}
		repo, _, _, _ := feature.ParseStableReviewFeedbackRef(update.StableRef)
		known := false
		for _, parentRepo := range parent.Repos {
			if parentRepo.Name == repo {
				known = true
				break
			}
		}
		if !known {
			writeAPIError(w, http.StatusBadRequest, errCodeReviewFeedbackUnknownReference,
				fmt.Sprintf("reference %q does not belong to a repository of feature %q", update.StableRef, parentID), nil)
			return
		}
		updates[ref] = update.Selected
	}

	draft, loadErr := draftStore.LoadReviewFeedbackDraft(parent.ID)
	if loadErr != nil {
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "read review feedback draft", nil)
		return
	}
	if draft == nil {
		writeAPIError(w, http.StatusBadRequest, errCodeReviewFeedbackDraftNotFound, "no pending review feedback draft; fetch first", nil)
		return
	}
	if draft.Revision != int64(req.ExpectedRevision) {
		writeAPIError(w, http.StatusConflict, errCodeReviewFeedbackRevisionConflict,
			fmt.Sprintf("expected revision %d but draft is at revision %d", req.ExpectedRevision, draft.Revision), nil)
		return
	}
	if applyErr := feature.ApplyReviewFeedbackSelection(draft, updates); applyErr != nil {
		writeAPIError(w, http.StatusBadRequest, errCodeReviewFeedbackUnknownReference, applyErr.Error(), nil)
		return
	}
	draft.Revision++
	draft.SnapshotID = feature.ReviewFeedbackSnapshotID(draft.Items)
	if saveErr := draftStore.SaveReviewFeedbackDraft(parent.ID, draft, draft.Revision-1); saveErr != nil {
		var conflict *feature.ReviewFeedbackRevisionConflictError
		var consumed *feature.ReviewFeedbackDraftConsumedError
		if errors.As(saveErr, &conflict) || errors.As(saveErr, &consumed) {
			writeAPIError(w, http.StatusConflict, errCodeReviewFeedbackRevisionConflict, saveErr.Error(), nil)
			return
		}
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "save review feedback draft", nil)
		return
	}

	writeJSON(w, http.StatusOK, ReviewFeedbackSelectionResponse{
		APIVersion: APIVersion,
		Revision:   int(draft.Revision),
		Repos:      reviewFeedbackDraftView(parent, draft),
	})
}

// reviewFeedbackDraftView renders the durable draft as the revisioned
// wire view, preserving the parent's stable repository order and the
// oldest-first comment order established by reconciliation.
func reviewFeedbackDraftView(parent *feature.Feature, draft *feature.ReviewFeedbackDraft) []ReviewFeedbackRepoComments {
	prURLs := parent.PRURLs()
	byRepo := make(map[string][]ReviewFeedbackDraftComment, len(parent.Repos))
	for _, item := range draft.Items {
		c := item.Comment
		byRepo[c.Repo] = append(byRepo[c.Repo], ReviewFeedbackDraftComment{
			StableRef:   string(item.StableRef),
			Selected:    item.Selected,
			Repo:        c.Repo,
			ID:          c.ID,
			Type:        ReviewFeedbackDraftCommentType(c.Type),
			Path:        c.Path,
			Line:        c.Line,
			Author:      c.Author,
			Body:        c.Body,
			DiffHunk:    c.DiffHunk,
			InReplyToID: c.InReplyTo,
			CreatedAt:   c.CreatedAt,
		})
	}
	groups := make([]ReviewFeedbackRepoComments, 0, len(parent.Repos))
	for _, repo := range parent.Repos {
		comments := byRepo[repo.Name]
		if len(comments) == 0 {
			continue
		}
		groups = append(groups, ReviewFeedbackRepoComments{
			Repo:     repo.Name,
			PrURL:    prURLs[repo.Name],
			Comments: comments,
		})
	}
	return groups
}

func writeReviewFeedbackFetchError(w http.ResponseWriter, repoName string, err error) {
	writeAPIError(
		w,
		http.StatusBadGateway,
		errCodeReviewFeedbackFetchFailed,
		fmt.Sprintf("fetch review feedback for repo %q: %v", repoName, err),
		map[string]any{"repo": repoName},
	)
}
