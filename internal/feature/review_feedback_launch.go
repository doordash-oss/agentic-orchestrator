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

package feature

import (
	"errors"
	"fmt"
	"os"

	gitadapter "github.com/doordash-oss/agentic-orchestrator/internal/git"
)

// fetchPRCommentsFunc is substitutable in tests that cannot reach GitHub.
var fetchPRCommentsFunc = gitadapter.FetchPRComments

// ReviewFeedbackZeroLaunchableSelectionError reports a launch whose
// reconciliation left no selected reviewed reference still present. No child
// is created and the pending draft is preserved.
type ReviewFeedbackZeroLaunchableSelectionError struct {
	ParentID string
}

func (e *ReviewFeedbackZeroLaunchableSelectionError) Error() string {
	return fmt.Sprintf("review feedback launch for parent %q has no launchable selected comment", e.ParentID)
}

// ReviewFeedbackLaunchResult reports the created child and the reconciliation
// counts computed from current GitHub data.
type ReviewFeedbackLaunchResult struct {
	Child    *Feature
	Changed  int
	Omitted  int
	Deferred int
}

// LaunchReviewFeedbackChildFromDraft commits the pending draft: it validates
// the expected revision, re-resolves current GitHub content for the selected
// reviewed references, creates the child from that current content, and only
// then clears the draft. Every failure before durable child creation leaves
// the draft intact.
func (m *Manager) LaunchReviewFeedbackChildFromDraft(parentID string, expectedRevision int64, gate *bool) (*ReviewFeedbackLaunchResult, error) {
	parent, err := m.Store.Load(parentID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrRefactorParentNotFound, parentID)
		}
		return nil, fmt.Errorf("loading parent feature: %w", err)
	}
	if err := ValidateRefactorParent(parent, nil); err != nil {
		return nil, err
	}
	draft, err := m.Store.LoadReviewFeedbackDraft(parentID)
	if err != nil {
		return nil, err
	}
	if draft == nil {
		return nil, fmt.Errorf("%w: %s", ErrReviewFeedbackDraftNotFound, parentID)
	}
	if draft.Revision != expectedRevision {
		return nil, &ReviewFeedbackRevisionConflictError{ParentID: parentID, ExpectedRevision: expectedRevision, CurrentRevision: draft.Revision}
	}

	current, err := m.resolveCurrentReviewFeedback(parent)
	if err != nil {
		return nil, err
	}

	launchable := make([]ReviewFeedbackComment, 0, len(draft.Items))
	changed, omitted := 0, 0
	snapshotRefs := make(map[StableReviewFeedbackRef]bool, len(draft.Items))
	for _, item := range draft.Items {
		snapshotRefs[item.StableRef] = true
		if !item.Selected {
			continue
		}
		fresh, present := current[item.StableRef]
		if !present {
			omitted++
			continue
		}
		if reviewFeedbackCommentChanged(item.Comment, fresh) {
			changed++
		}
		launchable = append(launchable, fresh)
	}
	deferred := 0
	for ref := range current {
		if !snapshotRefs[ref] {
			deferred++
		}
	}
	if len(launchable) == 0 {
		return nil, &ReviewFeedbackZeroLaunchableSelectionError{ParentID: parentID}
	}

	child, err := m.CreateReviewFeedbackChild(parentID, ReviewFeedbackChildSpec{
		Comments:    launchable,
		GateEnabled: gate,
	})
	if err != nil {
		return nil, err
	}
	// Durable child creation succeeded; only now may the pending draft be
	// cleared.
	if err := m.Store.DeleteReviewFeedbackDraft(parentID); err != nil {
		return nil, err
	}
	return &ReviewFeedbackLaunchResult{Child: child, Changed: changed, Omitted: omitted, Deferred: deferred}, nil
}

// resolveCurrentReviewFeedback re-fetches GitHub for every parent repository
// with a PR URL and indexes the currently unaddressed comments by stable
// reference. Repositories without a PR URL contribute nothing.
func (m *Manager) resolveCurrentReviewFeedback(parent *Feature) (map[StableReviewFeedbackRef]ReviewFeedbackComment, error) {
	resolver := m.Store
	prURLs := parent.PRURLs()
	current := make(map[StableReviewFeedbackRef]ReviewFeedbackComment)
	for _, repo := range parent.Repos {
		prURL := prURLs[repo.Name]
		if prURL == "" {
			continue
		}
		addressed, err := resolver.LoadAddressedReviewFeedbackIDs(parent.ID, repo.Name)
		if err != nil {
			return nil, fmt.Errorf("load addressed review-feedback IDs for repo %q: %w", repo.Name, err)
		}
		repoPath := repo.WorktreePath
		if repoPath == "" {
			repoPath = repo.Path
		}
		fetched, err := fetchPRCommentsFunc(repoPath, prURL)
		if err != nil {
			return nil, fmt.Errorf("fetch review feedback for repo %q: %w", repo.Name, err)
		}
		for _, comment := range fetched {
			if addressed[comment.ID] {
				continue
			}
			ref, err := NewStableReviewFeedbackRef(repo.Name, comment.Type, comment.ID)
			if err != nil {
				continue // unsupported types never contribute to launch
			}
			current[ref] = ReviewFeedbackComment{
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
			}
		}
	}
	return current, nil
}

// reviewFeedbackCommentChanged reports whether any child-visible field
// (body, diff hunk, path, line, or author) differs between the reviewed
// snapshot and the current server-fetched content.
func reviewFeedbackCommentChanged(snapshot, fresh ReviewFeedbackComment) bool {
	return snapshot.Body != fresh.Body ||
		snapshot.DiffHunk != fresh.DiffHunk ||
		snapshot.Path != fresh.Path ||
		snapshot.Line != fresh.Line ||
		snapshot.Author != fresh.Author
}
