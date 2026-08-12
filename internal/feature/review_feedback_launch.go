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

// ReviewFeedbackLaunchReceipt is the compact durable record pairing one
// review-feedback child with the committed draft it was launched from: the
// committed draft revision, the requested gate choice, and the reconciliation
// counts computed from current GitHub data. It never carries comment
// content. The receipt is stamped on the durable ChildCreationIntent (so an
// interrupted creation replays) and on the created child's relationship (so a
// repeated launch or ordinary refresh against the active child returns the
// original result without a second GitHub resolution or child creation).
type ReviewFeedbackLaunchReceipt struct {
	// DraftRevision is the committed pending-draft revision the launch
	// consumed.
	DraftRevision int64 `yaml:"draft_revision" json:"draft_revision"`
	// Gate is the requested gate override; nil records the inherit choice.
	Gate *bool `yaml:"gate,omitempty" json:"gate,omitempty"`
	// Changed counts selected reviewed references still present at launch
	// whose child-visible content changed since the reviewed snapshot.
	Changed int `yaml:"changed" json:"changed"`
	// Omitted counts selected reviewed references deleted before launch.
	Omitted int `yaml:"omitted" json:"omitted"`
	// Deferred counts comments first observed after the reviewed snapshot.
	Deferred int `yaml:"deferred" json:"deferred"`
}

// copyReviewFeedbackLaunchReceipt deep-copies a receipt for durable stamping.
func copyReviewFeedbackLaunchReceipt(receipt *ReviewFeedbackLaunchReceipt) *ReviewFeedbackLaunchReceipt {
	if receipt == nil {
		return nil
	}
	copied := *receipt
	if receipt.Gate != nil {
		gate := *receipt.Gate
		copied.Gate = &gate
	}
	return &copied
}

// reviewFeedbackGateEqual compares gate choices nil-sensitively.
func reviewFeedbackGateEqual(a, b *bool) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// ReviewFeedbackLaunchResult reports the created (or replayed) child and the
// reconciliation counts computed from current GitHub data.
type ReviewFeedbackLaunchResult struct {
	Child    *Feature
	Changed  int
	Omitted  int
	Deferred int
	// Replayed reports that the result came from the durable launch receipt
	// of an already-pending or already-active review-feedback child instead
	// of a fresh GitHub resolution and child creation. Callers must not
	// re-dispatch child-created/setup side effects for a replay: the child
	// was already announced and its setup intent already queued.
	Replayed bool
}

// LaunchReviewFeedbackChildFromDraft commits the pending draft: it validates
// the expected revision, re-resolves current GitHub content for the selected
// reviewed references, and creates the child from that current content. The
// draft stays durable until the creation intent commits; the stamped intent
// is the consumption marker, and the pinned draft revision is deleted only
// afterward, so every failure or interruption before that commit leaves the
// acknowledged draft fully intact.
//
// Launch is idempotent: the durable creation intent and the created child's
// relationship carry a ReviewFeedbackLaunchReceipt. Repeating the launch for
// the same parent, committed revision, and gate choice replays the receipt —
// the original child ID and counts are returned without a second GitHub
// resolution or child creation, and any pending draft cleanup from an
// interrupted first attempt is completed. A different revision, a mismatched
// gate, or an unrelated active child returns the existing typed
// conflict/preflight failure without mutating the child or the draft.
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

	// A durable in-flight creation intent wins over everything else: replay
	// it when the launch matches, conflict when it does not.
	if result, handled, err := m.replayPendingReviewFeedbackLaunch(parent, expectedRevision, gate); handled {
		return result, err
	}
	// An active review-feedback child with a matching receipt replays; a
	// stale or mismatched one conflicts before any GitHub resolution.
	if result, handled, err := m.replayActiveReviewFeedbackLaunch(parent, expectedRevision, gate); handled {
		return result, err
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

	receipt := &ReviewFeedbackLaunchReceipt{
		DraftRevision: draft.Revision,
		Gate:          gate,
		Changed:       changed,
		Omitted:       omitted,
		Deferred:      deferred,
	}
	child, err := m.CreateReviewFeedbackChild(parentID, ReviewFeedbackChildSpec{
		Comments:    launchable,
		GateEnabled: gate,
		Receipt:     receipt,
	})
	if err != nil {
		// A concurrent launch may have committed its own receipt after the
		// pre-creation replay checks: replay that durable receipt instead of
		// surfacing the active-child conflict, when it matches.
		var activeChildErr *ActiveChildExistsError
		if errors.As(err, &activeChildErr) {
			if result, handled, replayErr := m.replayActiveReviewFeedbackLaunch(parent, expectedRevision, gate); handled {
				return result, replayErr
			}
		}
		return nil, err
	}
	// Durable child creation committed; Store.CreateChildLocked already
	// consumed the pinned draft revision atomically with the intent clearing,
	// so no separate cleanup window exists here.
	return &ReviewFeedbackLaunchResult{Child: child, Changed: changed, Omitted: omitted, Deferred: deferred}, nil
}

// replayPendingReviewFeedbackLaunch handles a parent carrying a durable
// review-feedback creation intent: a matching launch rolls the interrupted
// creation forward exactly once and replays the receipt; a mismatched one
// returns a typed conflict without mutating anything.
func (m *Manager) replayPendingReviewFeedbackLaunch(parent *Feature, expectedRevision int64, gate *bool) (result *ReviewFeedbackLaunchResult, handled bool, err error) {
	intent := parent.PendingChild
	if intent == nil || intent.Kind != ChildKindReviewFeedback || intent.LaunchReceipt == nil {
		return nil, false, nil
	}
	receipt := intent.LaunchReceipt
	if receipt.DraftRevision != expectedRevision {
		return nil, true, &ReviewFeedbackRevisionConflictError{ParentID: parent.ID, ExpectedRevision: expectedRevision, CurrentRevision: receipt.DraftRevision}
	}
	if !reviewFeedbackGateEqual(receipt.Gate, gate) {
		return nil, true, &ActiveChildExistsError{ParentID: parent.ID, ChildID: intent.ChildID}
	}
	if _, err := m.Store.ReconcilePendingChildCreations(); err != nil {
		return nil, true, fmt.Errorf("reconciling pending review-feedback child creation: %w", err)
	}
	child, err := m.Store.Load(intent.ChildID)
	if err != nil {
		return nil, true, fmt.Errorf("loading replayed review-feedback child: %w", err)
	}
	// Finish the draft cleanup the interrupted launch could not: only the
	// draft the receipt consumed may be deleted, never a newer revision.
	if err := m.Store.DeleteReviewFeedbackDraftIfRevision(parent.ID, receipt.DraftRevision); err != nil {
		return nil, true, err
	}
	return &ReviewFeedbackLaunchResult{
		Child: child, Changed: receipt.Changed, Omitted: receipt.Omitted, Deferred: receipt.Deferred, Replayed: true,
	}, true, nil
}

// replayActiveReviewFeedbackLaunch handles a parent whose active child is a
// review-feedback child carrying a launch receipt: a matching launch replays
// the receipt and idempotently finishes pending draft cleanup; a mismatched
// one returns a typed conflict without mutating the child or the draft.
func (m *Manager) replayActiveReviewFeedbackLaunch(parent *Feature, expectedRevision int64, gate *bool) (result *ReviewFeedbackLaunchResult, handled bool, err error) {
	child, receipt, err := m.Store.ActiveReviewFeedbackLaunchReceipt(parent.ID)
	if err != nil {
		return nil, true, fmt.Errorf("scanning active review-feedback child: %w", err)
	}
	if receipt == nil {
		return nil, false, nil
	}
	if receipt.DraftRevision != expectedRevision {
		return nil, true, &ReviewFeedbackRevisionConflictError{ParentID: parent.ID, ExpectedRevision: expectedRevision, CurrentRevision: receipt.DraftRevision}
	}
	if !reviewFeedbackGateEqual(receipt.Gate, gate) {
		return nil, true, &ActiveChildExistsError{ParentID: parent.ID, ChildID: child.ID}
	}
	// Idempotently finish the draft cleanup of the original launch: only the
	// revision the receipt consumed may be deleted; a newer draft belongs to
	// a later editing session and must survive.
	if err := m.Store.DeleteReviewFeedbackDraftIfRevision(parent.ID, receipt.DraftRevision); err != nil {
		return nil, true, err
	}
	return &ReviewFeedbackLaunchResult{
		Child: child, Changed: receipt.Changed, Omitted: receipt.Omitted, Deferred: receipt.Deferred, Replayed: true,
	}, true, nil
}

// ActiveReviewFeedbackLaunchReceipt returns the parent's active
// review-feedback child and its durable launch receipt. Both are nil when no
// active review-feedback child exists or when the active child predates
// launch receipts (an unrelated active child never claims a receipt).
func (s *Store) ActiveReviewFeedbackLaunchReceipt(parentID string) (*Feature, *ReviewFeedbackLaunchReceipt, error) {
	children, err := s.RelationshipChildren(parentID)
	if err != nil {
		return nil, nil, err
	}
	active := children.Active
	if active == nil || active.Parent == nil {
		return nil, nil, nil
	}
	if active.Parent.Kind != ChildKindReviewFeedback || active.Parent.LaunchReceipt == nil {
		return nil, nil, nil
	}
	return active, active.Parent.LaunchReceipt, nil
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
