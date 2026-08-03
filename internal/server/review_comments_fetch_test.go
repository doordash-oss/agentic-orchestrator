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
	"net/http"
	"strings"
	"testing"
)

type reviewCommentsFetchFailTarget struct {
	MutationTarget
}

func (t *reviewCommentsFetchFailTarget) FetchReviewComments(featureID string, req ReviewCommentsFetchRequest) (ReviewCommentsFetchResponse, error) {
	return ReviewCommentsFetchResponse{}, errors.New("decoding GitHub response for repos/o/r/pulls/1/comments: invalid character '/'")
}

func TestReviewCommentsFetchSurfacesUnderlyingError(t *testing.T) {
	t.Parallel()
	handler := NewHandler(HandlerOptions{
		Mutations:             &reviewCommentsFetchFailTarget{},
		DisableHostValidation: true,
	})
	w := postTrustedJSON(handler, "/api/v1/features/feat-1/actions/review-comments/fetch", map[string]any{
		"repo": "repo-a",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s; want 400", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "invalid character") {
		t.Fatalf("body = %s; want underlying error detail surfaced", w.Body.String())
	}
}
