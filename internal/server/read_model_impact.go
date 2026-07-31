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
	"sort"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// Impact-preview category keys are the public contract clients key the
// confirmation UI on. Each kind always ships its full key set; a category
// with nothing to lose carries an empty items array rather than being
// omitted, so the confirmation never implies hidden impact.
const (
	impactCategoryChildren  = "children"
	impactCategorySessions  = "sessions"
	impactCategoryWorktrees = "worktrees"
	impactCategoryBranches  = "branches"
	impactCategoryHistory   = "history"
	impactCategoryKnowledge = "knowledge"
)

// Retained statements for child_discard; parent_cascade_delete keeps nothing.
const (
	impactRetainedReviewConfig   = "Review configuration retained"
	impactRetainedDiscardHistory = "Child becomes immutable Discarded history"
)

// childDiscardImpactPreview projects the discard state machine's impact from
// the child record: every active child session stops, disposable per-repo
// worktrees and ephemeral branches are removed, and the temporary knowledge
// workspace is discarded — while the paired Review configuration and the
// immutable closed (Discarded) child record are retained.
func (h *apiHandler) childDiscardImpactPreview(f *feature.Feature) *ActionImpactPreviewDTO {
	if f == nil || !f.IsChild() || !f.IsActiveChild() {
		return nil
	}
	return &ActionImpactPreviewDTO{
		Kind:    ChildDiscard,
		Subject: ActionImpactSubjectDTO{ID: f.ID, Name: f.Name},
		Categories: []ActionImpactCategoryDTO{
			{Key: impactCategorySessions, Label: "Sessions stopped", Items: h.sessionImpactEntries(f)},
			{Key: impactCategoryWorktrees, Label: "Disposable worktrees removed", Items: worktreeImpactEntries(f)},
			{Key: impactCategoryBranches, Label: "Ephemeral branches removed", Items: branchImpactEntries(f)},
			{Key: impactCategoryKnowledge, Label: "Temporary knowledge removed", Items: h.childKnowledgeImpactEntries(f)},
		},
		Retained: []string{impactRetainedReviewConfig, impactRetainedDiscardHistory},
	}
}

// parentCascadeDeleteImpactPreview projects the durable relationship cascade:
// the parent record and every active or closed child record disappear together
// with the union of their sessions, worktrees, branches, the relationship
// history, and the knowledge overlays/workspaces owned by the relationship.
// It returns nil for an ordinary child-free feature so a plain single-feature
// delete never carries a preview.
func (h *apiHandler) parentCascadeDeleteImpactPreview(f *feature.Feature, children *feature.RelationshipChildren) *ActionImpactPreviewDTO {
	if f == nil || f.IsChild() || children == nil || (children.Active == nil && len(children.Closed) == 0) {
		return nil
	}
	related := append([]*feature.Feature{f}, relationshipRelated(children)...)
	childrenItems := []string{}
	sessions := []string{}
	worktrees := []string{}
	branches := []string{}
	for _, member := range related {
		if member != f {
			childrenItems = append(childrenItems, member.Name)
		}
		sessions = append(sessions, h.sessionImpactEntries(member)...)
		worktrees = append(worktrees, worktreeImpactEntries(member)...)
		branches = append(branches, branchImpactEntries(member)...)
	}
	return &ActionImpactPreviewDTO{
		Kind:    ParentCascadeDelete,
		Subject: ActionImpactSubjectDTO{ID: f.ID, Name: f.Name},
		Categories: []ActionImpactCategoryDTO{
			{Key: impactCategoryChildren, Label: "Child records removed", Items: childrenItems},
			{Key: impactCategorySessions, Label: "Sessions stopped", Items: sessions},
			{Key: impactCategoryWorktrees, Label: "Worktrees removed", Items: worktrees},
			{Key: impactCategoryBranches, Label: "Branches removed", Items: branches},
			{Key: impactCategoryHistory, Label: "Relationship history removed", Items: relationshipHistoryImpactEntries(children)},
			{Key: impactCategoryKnowledge, Label: "Knowledge removed", Items: h.cascadeKnowledgeImpactEntries(f, children)},
		},
		Retained: []string{},
	}
}

// relationshipRelated lists the active child first followed by closed
// history, mirroring the relationship projection order.
func relationshipRelated(children *feature.RelationshipChildren) []*feature.Feature {
	out := make([]*feature.Feature, 0, len(children.Closed)+1)
	if children.Active != nil {
		out = append(out, children.Active)
	}
	out = append(out, children.Closed...)
	return out
}

// sessionImpactEntries names the sessions a discard or cascade delete stops
// for one feature. Live session views are authoritative; without a session
// manager the durable per-run session ledger describes the sessions the
// feature has accounted instead of showing a misleadingly empty category.
func (h *apiHandler) sessionImpactEntries(f *feature.Feature) []string {
	out := []string{}
	if h.sessions != nil {
		views := append([]ports.SessionView(nil), h.sessions.FeatureSessions(f.ID)...)
		sort.SliceStable(views, func(i, j int) bool { return views[i].ID() < views[j].ID() })
		for _, sess := range views {
			if sess == nil || !sess.IsActive() {
				continue
			}
			label := strings.TrimSpace(sess.Label())
			if label == "" {
				label = strings.TrimSpace(sess.Phase().String())
			}
			out = append(out, label+" ("+sess.ID()+")")
		}
		return out
	}
	for _, rec := range f.SessionCosts {
		out = append(out, rec.PhaseKey+" ("+rec.SessionID+")")
	}
	return out
}

// worktreeImpactEntries mirrors the cascade/discard worktree resources: the
// disposable worktree path recorded per repository.
func worktreeImpactEntries(f *feature.Feature) []string {
	out := []string{}
	for _, repo := range f.Repos {
		if repo.WorktreePath == "" {
			continue
		}
		out = append(out, repo.WorktreePath+" (repo "+repo.Name+")")
	}
	return out
}

// branchImpactEntries mirrors the cascade/discard branch resources: the
// ephemeral branch recorded per repository.
func branchImpactEntries(f *feature.Feature) []string {
	out := []string{}
	for _, repo := range f.Repos {
		if repo.Branch == "" {
			continue
		}
		out = append(out, repo.Branch+" (repo "+repo.Name+")")
	}
	return out
}

// childKnowledgeImpactEntries mirrors DiscardChild's cleanup tail: the
// temporary per-repo knowledge workspace exists only when the pipeline has a
// knowledge-base phase AND durable workspace state proves the workspace was
// actually seeded — otherwise the category stays empty so absent impact
// renders as None.
func (h *apiHandler) childKnowledgeImpactEntries(f *feature.Feature) []string {
	out := []string{}
	if !f.EffectivePipeline().HasPhase(feature.PhaseKnowledgeBase) {
		return out
	}
	baseDir := h.knowledgeStateDir()
	if baseDir == "" {
		return out
	}
	for _, repo := range f.Repos {
		state, err := feature.LoadWorkspaceState(feature.ChildKBWorkspaceDir(baseDir, f.ID, repo.Name))
		if err != nil || state == nil {
			continue
		}
		out = append(out, "temporary knowledge workspace for repo "+repo.Name+" (child "+f.ID+")")
	}
	return out
}

// cascadeKnowledgeImpactEntries mirrors the cascade manifest: each child
// knowledge workspace plus the stable per-repo parent overlay the relationship
// promoted knowledge into. Overlay entries are emitted only when durable
// on-disk provenance proves a promotion completed — the overlay path is a
// namespace the cascade reserves speculatively, so a Medium-pipeline
// relationship (no knowledge-base phase) has no overlay to lose and renders
// an explicitly empty category.
func (h *apiHandler) cascadeKnowledgeImpactEntries(f *feature.Feature, children *feature.RelationshipChildren) []string {
	out := []string{}
	baseDir := h.knowledgeStateDir()
	for _, repo := range f.Repos {
		if baseDir == "" || !feature.ParentOverlayExists(baseDir, f.ID, repo.Name) {
			continue
		}
		out = append(out, "knowledge overlay for repo "+repo.Name+" (parent "+f.ID+")")
	}
	for _, child := range relationshipRelated(children) {
		out = append(out, h.childKnowledgeImpactEntries(child)...)
	}
	return out
}

// knowledgeStateDir returns the feature state base directory so the impact
// preview can verify knowledge resources against durable on-disk state. It is
// empty when the configured store cannot be located, in which case
// uncertain knowledge resources are never overstated as present.
func (h *apiHandler) knowledgeStateDir() string {
	if s, ok := h.store.(*feature.Store); ok && s != nil && s.BaseDir != "" {
		return s.BaseDir
	}
	if s, ok := h.features.(*feature.Store); ok && s != nil {
		return s.BaseDir
	}
	return ""
}

// relationshipHistoryImpactEntries names the closed-child history records the
// cascade removes, using the same outcome labels the relationship projection
// renders.
func relationshipHistoryImpactEntries(children *feature.RelationshipChildren) []string {
	out := []string{}
	for _, child := range children.Closed {
		if child == nil || child.Parent == nil {
			continue
		}
		label := "Closed"
		switch child.Parent.CloseOutcome {
		case feature.ChildCloseOutcomeCompleted:
			label = "Closed — Completed"
		case feature.ChildCloseOutcomeDiscarded:
			label = "Closed — Discarded"
		}
		out = append(out, label+" "+child.Name)
	}
	return out
}

// attachImpactPreview sets the structured preview on one action in place,
// leaving every other action untouched.
func attachImpactPreview(actions []ActionDTO, actionID string, preview *ActionImpactPreviewDTO) {
	if preview == nil {
		return
	}
	for i := range actions {
		if actions[i].ID == actionID {
			actions[i].ImpactPreview = preview
			return
		}
	}
}
