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
	actionReviewFeedback             = "review-feedback"
	reviewFeedbackSubactionFetch     = "fetch"
	errCodeReviewFeedbackFetchFailed = "review_feedback_fetch_failed"
)

type addressedReviewFeedbackIDReader interface {
	LoadAddressedReviewFeedbackIDs(parentID, repoName string) (map[int]bool, error)
}

func reviewFeedbackFetchPath(featureID string) string {
	return featureActionPath(featureID, actionReviewFeedback) + "/" + reviewFeedbackSubactionFetch
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

	groups := make([]ReviewFeedbackRepoComments, 0, len(parent.Repos))
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
		fetched, fetchErr := gitadapter.FetchPRComments(repoPath, prURL)
		if fetchErr != nil {
			writeReviewFeedbackFetchError(w, repo.Name, fetchErr)
			return
		}
		comments := make([]feature.ReviewFeedbackComment, 0, len(fetched))
		for _, comment := range fetched {
			if addressed[comment.ID] {
				continue
			}
			comments = append(comments, feature.ReviewFeedbackComment{
				Repo:      repo.Name,
				ID:        comment.ID,
				Type:      comment.Type,
				Path:      comment.Path,
				Line:      comment.Line,
				Author:    comment.User.Login,
				Body:      comment.Body,
				DiffHunk:  comment.DiffHunk,
				InReplyTo: comment.InReplyTo,
			})
		}
		if len(comments) == 0 {
			continue
		}
		groups = append(groups, ReviewFeedbackRepoComments{
			Repo:     repo.Name,
			PrURL:    prURL,
			Comments: comments,
		})
	}

	writeJSON(w, http.StatusOK, ReviewFeedbackFetchResponse{
		APIVersion: APIVersion,
		Repos:      groups,
	})
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
