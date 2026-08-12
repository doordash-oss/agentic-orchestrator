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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Supported review-feedback comment types mirrored by stable references.
const (
	ReviewFeedbackCommentTypeReview     = "review"
	ReviewFeedbackCommentTypeIssue      = "issue"
	ReviewFeedbackCommentTypeReviewBody = "review_body"
)

// reviewFeedbackDraftFilename is the single durable pending-draft file per
// parent feature. It lives outside run directories (next to the addressed
// comment ledger) so rewinds and run rollover neither resurrect nor erase
// committed selections.
const reviewFeedbackDraftFilename = "draft.json"

// ErrReviewFeedbackDraftNotFound is returned when an operation requires a
// pending draft and none exists for the parent.
var ErrReviewFeedbackDraftNotFound = errors.New("review feedback draft not found")

// ReviewFeedbackRevisionConflictError reports a stale expected revision.
type ReviewFeedbackRevisionConflictError struct {
	ParentID         string
	ExpectedRevision int64
	CurrentRevision  int64
}

func (e *ReviewFeedbackRevisionConflictError) Error() string {
	return fmt.Sprintf("review feedback draft revision conflict for parent %q: expected %d, current %d", e.ParentID, e.ExpectedRevision, e.CurrentRevision)
}

// StableReviewFeedbackRef identifies one review-feedback comment durably:
// repository identity plus supported comment type plus GitHub database
// comment ID. The serialized form is "<repo>:<type>:<id>".
type StableReviewFeedbackRef string

// NewStableReviewFeedbackRef builds a validated stable reference.
func NewStableReviewFeedbackRef(repo, commentType string, id int) (StableReviewFeedbackRef, error) {
	if repo == "" || strings.Contains(repo, ":") {
		return "", fmt.Errorf("invalid repository identity %q", repo)
	}
	switch commentType {
	case ReviewFeedbackCommentTypeReview, ReviewFeedbackCommentTypeIssue, ReviewFeedbackCommentTypeReviewBody:
	default:
		return "", fmt.Errorf("unsupported comment type %q", commentType)
	}
	if id <= 0 {
		return "", fmt.Errorf("invalid comment ID %d", id)
	}
	return StableReviewFeedbackRef(fmt.Sprintf("%s:%s:%d", repo, commentType, id)), nil
}

// ParseStableReviewFeedbackRef strictly validates and splits a serialized
// stable reference. Malformed values never enter durable state.
func ParseStableReviewFeedbackRef(raw string) (repo, commentType string, id int, err error) {
	parts := strings.Split(raw, ":")
	if len(parts) != 3 {
		return "", "", 0, fmt.Errorf("malformed stable reference %q", raw)
	}
	id, err = strconv.Atoi(parts[2])
	if err != nil {
		return "", "", 0, fmt.Errorf("malformed stable reference %q: %v", raw, err)
	}
	if _, err := NewStableReviewFeedbackRef(parts[0], parts[1], id); err != nil {
		return "", "", 0, fmt.Errorf("malformed stable reference %q: %v", raw, err)
	}
	return parts[0], parts[1], id, nil
}

// ReviewFeedbackDraftItem pairs one committed selection with the reviewed
// child-visible content snapshot used to reconcile launch-time changes.
type ReviewFeedbackDraftItem struct {
	StableRef StableReviewFeedbackRef `json:"stable_ref"`
	Selected  bool                    `json:"selected"`
	Comment   ReviewFeedbackComment   `json:"comment"`
}

// ReviewFeedbackDraft is the authoritative, parent-scoped pending draft. At
// most one versioned draft exists per parent feature.
type ReviewFeedbackDraft struct {
	// Revision increments monotonically on every committed change.
	Revision int64 `json:"revision"`
	// SnapshotID identifies the reviewed snapshot (ordered stable
	// references plus reviewed content) the draft was last reconciled from.
	SnapshotID string `json:"snapshot_id"`
	// Items lists ordered visible stable references with their selections.
	Items []ReviewFeedbackDraftItem `json:"items"`
}

func reviewFeedbackDraftPath(baseDir, parentID string) string {
	return filepath.Join(baseDir, parentID, "review-feedback", reviewFeedbackDraftFilename)
}

// LoadReviewFeedbackDraft reads the pending draft for a parent. It returns
// (nil, nil) when no draft exists.
func (s *Store) LoadReviewFeedbackDraft(parentID string) (*ReviewFeedbackDraft, error) {
	data, err := os.ReadFile(reviewFeedbackDraftPath(s.BaseDir, parentID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read review feedback draft for parent %q: %w", parentID, err)
	}
	var draft ReviewFeedbackDraft
	if err := json.Unmarshal(data, &draft); err != nil {
		return nil, fmt.Errorf("parse review feedback draft for parent %q: %w", parentID, err)
	}
	return &draft, nil
}

// SaveReviewFeedbackDraft atomically persists the draft. When
// expectedRevision is positive the stored draft must match it, enforcing
// optimistic-revision compare-and-swap; when zero the draft must not already
// exist. The write uses the repository's temp-file-plus-rename convention
// under the store lock.
func (s *Store) SaveReviewFeedbackDraft(parentID string, draft *ReviewFeedbackDraft, expectedRevision int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, err := s.LoadReviewFeedbackDraft(parentID)
	if err != nil {
		return err
	}
	current := int64(0)
	if existing != nil {
		current = existing.Revision
	}
	if expectedRevision > 0 && current != expectedRevision {
		return &ReviewFeedbackRevisionConflictError{ParentID: parentID, ExpectedRevision: expectedRevision, CurrentRevision: current}
	}
	if expectedRevision == 0 && existing != nil {
		return &ReviewFeedbackRevisionConflictError{ParentID: parentID, ExpectedRevision: expectedRevision, CurrentRevision: current}
	}
	encoded, err := json.Marshal(draft)
	if err != nil {
		return fmt.Errorf("encode review feedback draft for parent %q: %w", parentID, err)
	}
	dir := filepath.Join(s.BaseDir, parentID, "review-feedback")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create review feedback draft dir for parent %q: %w", parentID, err)
	}
	tmp, err := os.CreateTemp(dir, ".draft-*.json.tmp")
	if err != nil {
		return fmt.Errorf("create review feedback draft temp file for parent %q: %w", parentID, err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(encoded); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("write review feedback draft for parent %q: %w", parentID, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("close review feedback draft for parent %q: %w", parentID, err)
	}
	if err := os.Rename(tmpName, reviewFeedbackDraftPath(s.BaseDir, parentID)); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("commit review feedback draft for parent %q: %w", parentID, err)
	}
	return nil
}

// DeleteReviewFeedbackDraft removes the pending draft. It is a no-op when no
// draft exists so launch recovery stays idempotent.
func (s *Store) DeleteReviewFeedbackDraft(parentID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deleteReviewFeedbackDraftUnlocked(parentID)
}

// DeleteReviewFeedbackDraftIfRevision removes the pending draft only when
// its committed revision still equals revision, so launch replay and
// recovery cleanup consume exactly the draft a launch receipt recorded and
// never delete a newer draft acknowledged afterward. It is a no-op when no
// draft exists or the stored revision differs.
func (s *Store) DeleteReviewFeedbackDraftIfRevision(parentID string, revision int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deleteReviewFeedbackDraftIfRevisionUnlocked(parentID, revision)
}

// deleteReviewFeedbackDraftIfRevisionUnlocked is the caller-holds-the-lock
// variant of DeleteReviewFeedbackDraftIfRevision.
func (s *Store) deleteReviewFeedbackDraftIfRevisionUnlocked(parentID string, revision int64) error {
	existing, err := s.LoadReviewFeedbackDraft(parentID)
	if err != nil {
		return err
	}
	if existing == nil || existing.Revision != revision {
		return nil
	}
	return s.deleteReviewFeedbackDraftUnlocked(parentID)
}

// deleteReviewFeedbackDraftUnlocked removes the pending draft while the
// caller already holds the store lock. It is a no-op when no draft exists.
func (s *Store) deleteReviewFeedbackDraftUnlocked(parentID string) error {
	err := os.Remove(reviewFeedbackDraftPath(s.BaseDir, parentID))
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete review feedback draft for parent %q: %w", parentID, err)
	}
	return nil
}

// ReviewFeedbackSnapshotID derives the reviewed-snapshot identity from the
// ordered stable references and their reviewed child-visible content.
func ReviewFeedbackSnapshotID(items []ReviewFeedbackDraftItem) string {
	h := sha256.New()
	for _, item := range items {
		c := item.Comment
		fmt.Fprintf(h, "%s\x00%s\x00%d\x00%s\x00%d\x00%s\x00%s\x00%s\x00%d\x00%s\x00",
			item.StableRef, c.Repo, c.ID, c.Path, c.Line, c.Author, c.Body, c.DiffHunk, c.InReplyTo, c.CreatedAt)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// SortReviewFeedbackDraftItems orders comments chronologically (oldest
// first) with the stable reference as the deterministic tie-breaker.
func SortReviewFeedbackDraftItems(items []ReviewFeedbackDraftItem) {
	sort.SliceStable(items, func(i, j int) bool {
		ci, cj := items[i].Comment, items[j].Comment
		if ci.CreatedAt != cj.CreatedAt {
			return ci.CreatedAt < cj.CreatedAt
		}
		return items[i].StableRef < items[j].StableRef
	})
}

// ReconcileReviewFeedbackDraft folds a fresh unaddressed snapshot into the
// parent's pending draft. A first fetch selects every visible reference;
// later fetches retain selections for known references, select newly
// observed references, and prune references no longer present. Items are
// grouped in the parent's stable repository order and ordered oldest-first
// within each repository. The addressed ledger is never consulted here:
// `fetched` is already pre-filtered by the caller.
func ReconcileReviewFeedbackDraft(parent *Feature, existing *ReviewFeedbackDraft, fetched map[string][]ReviewFeedbackComment) *ReviewFeedbackDraft {
	selections := make(map[StableReviewFeedbackRef]bool)
	revision := int64(0)
	if existing != nil {
		revision = existing.Revision
		for _, item := range existing.Items {
			selections[item.StableRef] = item.Selected
		}
	}
	items := make([]ReviewFeedbackDraftItem, 0)
	for _, repo := range parent.Repos {
		comments := fetched[repo.Name]
		repoItems := make([]ReviewFeedbackDraftItem, 0, len(comments))
		for _, comment := range comments {
			ref, err := NewStableReviewFeedbackRef(repo.Name, comment.Type, comment.ID)
			if err != nil {
				continue // unsupported types never enter durable state
			}
			selected, known := selections[ref]
			if !known {
				selected = true
			}
			repoItems = append(repoItems, ReviewFeedbackDraftItem{StableRef: ref, Selected: selected, Comment: comment})
		}
		SortReviewFeedbackDraftItems(repoItems)
		items = append(items, repoItems...)
	}
	return &ReviewFeedbackDraft{
		Revision:   revision + 1,
		SnapshotID: ReviewFeedbackSnapshotID(items),
		Items:      items,
	}
}

// ApplyReviewFeedbackSelection changes committed selections for known stable
// references. Unknown references are rejected without partially applying the
// batch.
func ApplyReviewFeedbackSelection(draft *ReviewFeedbackDraft, updates map[StableReviewFeedbackRef]bool) error {
	byRef := make(map[StableReviewFeedbackRef]bool, len(draft.Items))
	for _, item := range draft.Items {
		byRef[item.StableRef] = true
	}
	for ref := range updates {
		if !byRef[ref] {
			return fmt.Errorf("%w: %s", ErrReviewFeedbackUnknownReference, ref)
		}
	}
	for i := range draft.Items {
		if selected, ok := updates[draft.Items[i].StableRef]; ok {
			draft.Items[i].Selected = selected
		}
	}
	return nil
}

// ErrReviewFeedbackUnknownReference marks updates referencing stable
// references outside the pending draft.
var ErrReviewFeedbackUnknownReference = errors.New("review feedback unknown reference")
