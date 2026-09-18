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

package e2e

import (
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/server"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// The refactor child execution journeys prove the Phase-11 append integration
// end to end on the stack publish fixture: a parent whose roadmap-derived
// stack is published against a bare scp-style origin and the in-test GitHub
// pull store, a refactor child launched through the REST surface whose
// scripted roadmap derives its own multi-layer stack, and the closure
// auto-publish tail that turns the appended layers into chained pull
// requests.
const (
	// refactorJourneyRepo is the feature-side repository name; it matches the
	// scripted phase-plan **Repo:** tag and the config key.
	refactorJourneyRepo = "repoA"
	// refactorJourneyGitHubRepo is the GitHub-side identity served by the
	// bare origin's scp-style remote path and the in-test pull store.
	refactorJourneyGitHubRepo = "repo-a"
)

// refactorJourneyParentRoadmap is the two-phase parent roadmap whose
// `## Pull Requests` table carries two rows, so the parent publishes a
// two-layer stack before the child launches.
const refactorJourneyParentRoadmap = "# Roadmap\n\n" +
	"## Phase 1: Bootstrap\n\n### Goal\nStart the parent stack.\n\n" +
	"## Phase 2: Follow-up\n\n### Goal\nExtend the parent stack.\n\n" +
	"## Pull Requests\n\n" +
	"| # | Title | Phases | Rationale |\n|---|---|---|---|\n" +
	"| 1 | Bootstrap | 1 | Stands alone. |\n" +
	"| 2 | Follow-up | 2 | Builds on bootstrap. |\n"

// refactorJourneySingleParentRoadmap is the single-delivery parent roadmap:
// one phase, one row, one pull request.
const refactorJourneySingleParentRoadmap = "# Roadmap\n\n" +
	"## Phase 1: Parent slice\n\n### Goal\nShip the parent slice.\n\n" +
	"## Pull Requests\n\n" +
	"| # | Title | Phases | Rationale |\n|---|---|---|---|\n" +
	"| 1 | Parent slice | 1 | One phase, one reviewable slice. |\n"

// TestRefactorChildExecutionAndIntegrationJourney records the primary child
// execution state-mutating journey, API-driven end to end on the stack
// publish fixture:
//
//	Parent: created through the manager, real-git setup, scripted two-row
//	roadmap approved through the review decision, two phases driven across
//	the layer boundary, and the roadmap-final auto-publish tail delivering a
//	two-layer stack against the bare origin and the pull store.
//
//	Child: POST refactor through the REST surface with a scripted two-phase
//	roadmap whose `## Pull Requests` table has two rows and an implementer
//	that commits one distinct file per phase, so the child's own boundary
//	split yields a two-layer stack; the append integration creates the
//	parent-facing layer 3 and 4 refs, switches the parent worktree to the new
//	top, persists the appended layers, and the auto-publish tail chains the
//	appended pull requests onto the parent's stack.
func TestRefactorChildExecutionAndIntegrationJourney(t *testing.T) {
	if testing.Short() {
		t.Skip("journey boots real-git setup and scripted provider subprocesses")
	}

	fx := newRefactorAppendJourney(t, journeyPhaseRunnerOptions{
		childRoadmapText:       journeyTwoSliceRoadmapText,
		perPhaseChildFiles:     true,
		echoDescriptionContext: true,
	})

	parent := driveRefactorAppendParent(t, fx, "Journey parent stack", refactorJourneyParentRoadmap, 2, false)

	parentWorkspaceSlug := feature.WorkspaceSlug(parent.Slug, parent.ID)
	wantBranches := []string{
		git.LayerBranchName(parentWorkspaceSlug, 1, "bootstrap"),
		git.LayerBranchName(parentWorkspaceSlug, 2, "follow-up"),
		git.LayerBranchName(parentWorkspaceSlug, 3, "restructure-the-core-flow"),
		git.LayerBranchName(parentWorkspaceSlug, 4, "harden-the-edges"),
	}
	wantTitles := []string{"Bootstrap", "Follow-up", "Restructure the core flow", "Harden the edges"}
	wantSlugs := []string{"bootstrap", "follow-up", "restructure-the-core-flow", "harden-the-edges"}

	// The published parent fixture: two layers, two pull requests, the
	// worktree on the top layer's branch, and both parent refs captured
	// before the child launches so the append can be proven non-destructive.
	if len(parent.Stack) != 2 {
		t.Fatalf("parent stack layers before the child = %d, want 2", len(parent.Stack))
	}
	for i := 0; i < 2; i++ {
		if parent.Stack[i].Position != i+1 || parent.Stack[i].Title != wantTitles[i] ||
			parent.Stack[i].Slug != wantSlugs[i] || parent.Stack[i].Branch != wantBranches[i] {
			t.Fatalf("parent stack layer %d = %+v, want position %d titled %q on %s",
				i, parent.Stack[i], i+1, wantTitles[i], wantBranches[i])
		}
		if parent.Stack[i].Origin != nil {
			t.Fatalf("parent layer %d origin = %+v, want none (roadmap-derived)", i+1, parent.Stack[i].Origin)
		}
	}
	if got := fx.pulls.CreatedCount(refactorJourneyGitHubRepo); got != 2 {
		t.Fatalf("pull requests before the child = %d, want one per parent layer", got)
	}
	parentWorktree := parent.Repos[0].WorktreePath
	if parentWorktree == "" {
		t.Fatalf("parent records no worktree path")
	}
	if parent.Repos[0].Branch != wantBranches[1] {
		t.Fatalf("parent recorded branch = %q, want the top layer's %q", parent.Repos[0].Branch, wantBranches[1])
	}
	if got := journeyGit(t, parentWorktree, "branch", "--show-current"); got != wantBranches[1] {
		t.Fatalf("parent worktree branch = %q, want %q", got, wantBranches[1])
	}
	preLayerTips := []string{
		journeyGit(t, fx.repoA, "rev-parse", "refs/heads/"+wantBranches[0]),
		journeyGit(t, fx.repoA, "rev-parse", "refs/heads/"+wantBranches[1]),
	}
	preLayerCommits := []string{
		journeyGit(t, fx.repoA, "rev-list", "main.."+wantBranches[0]),
		journeyGit(t, fx.repoA, "rev-list", "main.."+wantBranches[1]),
	}

	// ------------------------------------------------------------------
	// Child: two-phase roadmap, two layers appended, auto-publish tail.
	// ------------------------------------------------------------------
	childID := runRefactorAppendChild(t, fx, parent.ID, "Journey rework", 2)
	waitForJourneyStackLayersPublished(t, fx.store, parent.ID, refactorJourneyRepo, 4)

	// ------------------------------------------------------------------
	// Assertions.
	// ------------------------------------------------------------------
	parent, err := fx.mgr.Get(parent.ID)
	if err != nil {
		t.Fatalf("reload parent after the tail: %v", err)
	}
	if len(parent.Stack) != 4 {
		t.Fatalf("parent stack layers after the child = %d, want 4", len(parent.Stack))
	}
	for i := 0; i < 4; i++ {
		layer := parent.Stack[i]
		if layer.Position != i+1 || layer.Title != wantTitles[i] ||
			layer.Slug != wantSlugs[i] || layer.Branch != wantBranches[i] {
			t.Fatalf("parent stack layer %d = %+v, want position %d titled %q on %s",
				i, layer, i+1, wantTitles[i], wantBranches[i])
		}
	}
	// The appended layers record their origin — the child feature and the
	// child's layer position — and carry no parent roadmap phases.
	for i, wantOriginPosition := range []int{1, 2} {
		layer := parent.Stack[2+i]
		if layer.Origin == nil || layer.Origin.SourceFeatureID != childID ||
			layer.Origin.SourceLayerPosition != wantOriginPosition {
			t.Fatalf("appended layer %d origin = %+v, want child %s layer %d",
				layer.Position, layer.Origin, childID, wantOriginPosition)
		}
		if len(layer.Phases) != 0 {
			t.Fatalf("appended layer %d phases = %v, want none", layer.Position, layer.Phases)
		}
	}
	// Layers 1 and 2 keep their refs and commits byte-identical: the append
	// created refs only, and nothing rewrote the parent's existing history
	// locally or on the remote.
	for i := 0; i < 2; i++ {
		if got := journeyGit(t, fx.repoA, "rev-parse", "refs/heads/"+wantBranches[i]); got != preLayerTips[i] {
			t.Fatalf("layer %d ref after the child = %s, want unchanged %s", i+1, got, preLayerTips[i])
		}
		if got := journeyGit(t, fx.repoA, "rev-list", "main.."+wantBranches[i]); got != preLayerCommits[i] {
			t.Fatalf("layer %d commits after the child changed:\n got %s\nwant %s", i+1, got, preLayerCommits[i])
		}
		if got := journeyGit(t, fx.bareA, "rev-parse", "refs/heads/"+wantBranches[i]); got != preLayerTips[i] {
			t.Fatalf("remote layer %d ref after the child = %s, want unchanged %s", i+1, got, preLayerTips[i])
		}
	}
	// The appended layers' per-repository tips equal the child's recorded
	// layer tips, and the parent worktree sits on the new top layer's branch
	// at its candidate.
	child, err := fx.store.Load(childID)
	if err != nil {
		t.Fatalf("load closed child: %v", err)
	}
	if len(child.Stack) != 2 {
		t.Fatalf("child stack layers = %d, want 2", len(child.Stack))
	}
	for i := 0; i < 2; i++ {
		want := child.Stack[i].Repos[refactorJourneyRepo].TipSHA
		if want == "" {
			t.Fatalf("child layer %d records no %s tip", i+1, refactorJourneyRepo)
		}
		if got := parent.Stack[2+i].Repos[refactorJourneyRepo].TipSHA; got != want {
			t.Fatalf("appended layer %d tip = %s, want the child's layer %d tip %s", 3+i, got, i+1, want)
		}
		if got := journeyGit(t, fx.bareA, "rev-parse", "refs/heads/"+wantBranches[2+i]); got != want {
			t.Fatalf("remote appended layer %d tip = %s, want the child's tip %s", 3+i, got, want)
		}
	}
	if parent.Repos[0].Branch != wantBranches[3] {
		t.Fatalf("parent recorded branch = %q, want the appended top layer's %q", parent.Repos[0].Branch, wantBranches[3])
	}
	if got := journeyGit(t, parentWorktree, "branch", "--show-current"); got != wantBranches[3] {
		t.Fatalf("parent worktree branch = %q, want %q", got, wantBranches[3])
	}
	if got := journeyGit(t, parentWorktree, "rev-parse", "HEAD"); got != parent.Stack[3].Repos[refactorJourneyRepo].TipSHA {
		t.Fatalf("parent worktree HEAD = %s, want the appended top layer's tip %s", got, parent.Stack[3].Repos[refactorJourneyRepo].TipSHA)
	}

	// The child's journal: merged, two created refs at positions 3 and 4
	// named with the parent's numbering and the child's slugs, the previous
	// top naming layer 2's branch and tip, and the appended layer
	// definitions with origins recorded once on the journal.
	journal := child.Parent.Transaction
	if journal == nil {
		t.Fatalf("closed child records no transaction journal")
	}
	if journal.Phase != feature.TransactionPhaseMerged {
		t.Fatalf("child journal phase = %q, want merged", journal.Phase)
	}
	if len(journal.AppendedLayers) != 2 {
		t.Fatalf("journal appended layers = %+v, want two", journal.AppendedLayers)
	}
	for i := range journal.AppendedLayers {
		def := journal.AppendedLayers[i]
		if def.Position != 3+i || def.Branch != wantBranches[2+i] || def.Slug != wantSlugs[2+i] ||
			def.Title != wantTitles[2+i] || def.Origin == nil ||
			def.Origin.SourceFeatureID != childID || def.Origin.SourceLayerPosition != i+1 {
			t.Fatalf("journal appended layer %d = %+v, want position %d on %s from child layer %d",
				i, def, 3+i, wantBranches[2+i], i+1)
		}
	}
	entry := journal.EntryByRepo(refactorJourneyRepo)
	if entry == nil {
		t.Fatalf("journal entry for %s missing", refactorJourneyRepo)
	}
	if len(entry.Refs) != 2 {
		t.Fatalf("journal entry refs = %+v, want two created refs", entry.Refs)
	}
	for i := range entry.Refs {
		ref := entry.Refs[i]
		if ref.RefKind() != feature.RepoRefKindCreate {
			t.Fatalf("journal ref %d kind = %q, want create", i, ref.RefKind())
		}
		if ref.Branch != wantBranches[2+i] || ref.Layer != 3+i {
			t.Fatalf("journal ref %d = %+v, want layer %d on %s", i, ref, 3+i, wantBranches[2+i])
		}
		if ref.AnchorSHA != "" {
			t.Fatalf("journal ref %d anchor = %q, want empty (a created ref's anchor is absence)", i, ref.AnchorSHA)
		}
		if ref.CandidateSHA == "" || ref.ObservedSHA != ref.CandidateSHA {
			t.Fatalf("journal ref %d = %+v, want observed at the candidate", i, ref)
		}
	}
	if entry.PreviousTop == nil || entry.PreviousTop.Branch != wantBranches[1] ||
		entry.PreviousTop.Layer != 2 || entry.PreviousTop.TipSHA != preLayerTips[1] {
		t.Fatalf("journal previous top = %+v, want layer 2 on %s at %s",
			entry.PreviousTop, wantBranches[1], preLayerTips[1])
	}
	if entry.ChildHeadSHA == "" || entry.ChildHeadSHA != parent.Stack[3].Repos[refactorJourneyRepo].TipSHA {
		t.Fatalf("journal child head = %q, want the appended top layer's tip %s",
			entry.ChildHeadSHA, parent.Stack[3].Repos[refactorJourneyRepo].TipSHA)
	}

	// The child detail projects the created refs on the wire.
	childDetail := getJourneyJSON(t, fx.srv.URL+"/api/v1/features/"+childID)["feature"].(map[string]any)
	if childDetail["close_outcome"] != feature.ChildCloseOutcomeCompleted {
		t.Fatalf("child detail close_outcome = %v, want completed", childDetail["close_outcome"])
	}
	if active, ok := childDetail["active"].(bool); ok && active {
		t.Fatalf("child detail active = %v, want false/absent (closed)", childDetail["active"])
	}
	tx, _ := childDetail["transaction"].(map[string]any)
	entries, _ := tx["entries"].([]any)
	if len(entries) != 1 {
		t.Fatalf("child detail transaction entries = %#v, want one repository", entries)
	}
	wireRefs, _ := entries[0].(map[string]any)["refs"].([]any)
	if len(wireRefs) != 2 {
		t.Fatalf("child detail transaction refs = %#v, want two", wireRefs)
	}
	for i, raw := range wireRefs {
		ref, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("child detail transaction ref %d = %#v, want an object", i, raw)
		}
		if ref["kind"] != string(feature.RepoRefKindCreate) {
			t.Fatalf("child detail ref %d kind = %v, want create", i, ref["kind"])
		}
		if ref["branch"] != wantBranches[2+i] || ref["layer_position"] != float64(3+i) {
			t.Fatalf("child detail ref %d = %#v, want layer %d on %s", i, ref, 3+i, wantBranches[2+i])
		}
		if anchor, ok := ref["anchor_sha"].(string); ok && anchor != "" {
			t.Fatalf("child detail ref %d anchor = %q, want empty", i, anchor)
		}
		if ref["observed_sha"] != ref["candidate_sha"] {
			t.Fatalf("child detail ref %d = %#v, want observed at the candidate", i, ref)
		}
	}

	// Cleanup removed the disposable child worktree and its ephemeral branch.
	if child.Repos[0].WorktreePath != "" {
		t.Fatalf("child durable worktree path %q not cleared", child.Repos[0].WorktreePath)
	}
	if branches := journeyGit(t, fx.repoA, "branch", "--list", child.Repos[0].Branch); branches != "" {
		t.Fatalf("child branch %s still present after cleanup", child.Repos[0].Branch)
	}

	// The pull store: exactly two new pull requests, in order, layer 3's
	// based on layer 2's branch (the previous top's pull request branch) and
	// layer 4's based on layer 3's branch.
	created := fx.pulls.Created()
	if len(created) != 4 {
		t.Fatalf("created pull requests = %d, want 4 (two parent, two appended)", len(created))
	}
	if created[2].Base != wantBranches[1] || created[3].Base != wantBranches[2] {
		t.Fatalf("appended pull request bases = %q/%q, want %q then %q",
			created[2].Base, created[3].Base, wantBranches[1], wantBranches[2])
	}
	if created[2].Head != wantBranches[2] || created[3].Head != wantBranches[3] {
		t.Fatalf("appended pull request heads = %q/%q, want %q/%q",
			created[2].Head, created[3].Head, wantBranches[2], wantBranches[3])
	}
	if created[2].Title != wantTitles[2] || created[3].Title != wantTitles[3] {
		t.Fatalf("appended pull request titles = %q/%q, want %q/%q",
			created[2].Title, created[3].Title, wantTitles[2], wantTitles[3])
	}

	// Every open pull request body lists all four layers in its stack
	// section, with the top layer marked as this pull request, and the
	// appended bodies' description context names the child feature while the
	// parent layers' bodies name the parent.
	prURLs := make([]string, 4)
	prNumbers := make([]int, 4)
	for i := 0; i < 4; i++ {
		layerEntry := parent.Stack[i].Repos[refactorJourneyRepo]
		if layerEntry.PRURL == "" || layerEntry.PRState != feature.StackPRStateOpen {
			t.Fatalf("layer %d entry = %+v, want an open pull request", i+1, layerEntry)
		}
		prURLs[i] = layerEntry.PRURL
		prNumbers[i] = journeyPRNumber(t, prURLs[i])
	}
	wantTopLine := fmt.Sprintf("4. Harden the edges - [#%d](%s) (open) (this pull request)", prNumbers[3], prURLs[3])
	for i := 0; i < 4; i++ {
		record, ok := fx.pulls.Pull(refactorJourneyGitHubRepo, prNumbers[i])
		if !ok {
			t.Fatalf("pull request %d record missing", prNumbers[i])
		}
		if !strings.Contains(record.Body, "## Stack") {
			t.Fatalf("pull request %d body lacks the stack section:\n%s", prNumbers[i], record.Body)
		}
		for j := 0; j < 4; j++ {
			wantLine := fmt.Sprintf("%d. %s - [#%d](", j+1, wantTitles[j], prNumbers[j])
			if !strings.Contains(record.Body, wantLine) {
				t.Fatalf("pull request %d body lacks the stack line %q:\n%s", prNumbers[i], wantLine, record.Body)
			}
		}
		if !strings.Contains(record.Body, wantTopLine) {
			t.Fatalf("pull request %d body lacks the top layer marker %q:\n%s", prNumbers[i], wantTopLine, record.Body)
		}
		wantContext := "describing Journey parent stack."
		if i >= 2 {
			wantContext = "describing Journey rework."
		}
		if !strings.Contains(record.Body, wantContext) {
			t.Fatalf("pull request %d body lacks the scripted description context %q:\n%s", prNumbers[i], wantContext, record.Body)
		}
	}

	// The read model: the parent is settled Published with no active child,
	// and the repository lists four pushed-up-to-date entries, one per layer.
	parentDetail := getJourneyJSON(t, fx.srv.URL+"/api/v1/features/"+parent.ID)["feature"].(map[string]any)
	if parentDetail["status"] != feature.StatusPublished.String() {
		t.Fatalf("parent detail status = %v, want Published", parentDetail["status"])
	}
	if parentDetail["active_child"] != nil {
		t.Fatalf("parent active_child = %v, want cleared after closure", parentDetail["active_child"])
	}
	repoStatuses, _ := parentDetail["repo_status"].([]any)
	var repoStatus map[string]any
	for _, row := range repoStatuses {
		if r, ok := row.(map[string]any); ok && r["name"] == refactorJourneyRepo {
			repoStatus = r
		}
	}
	if repoStatus == nil {
		t.Fatalf("parent detail repo_status has no %s row: %#v", refactorJourneyRepo, repoStatuses)
	}
	prEntries, _ := repoStatus["pull_requests"].([]any)
	if len(prEntries) != 4 {
		t.Fatalf("repo_status pull_requests = %#v, want one entry per layer", prEntries)
	}
	for i, row := range prEntries {
		entry, ok := row.(map[string]any)
		if !ok {
			t.Fatalf("pull_requests[%d] = %#v, want an object", i, row)
		}
		if entry["position"] != float64(i+1) || entry["state"] != string(feature.StackPRStateOpen) ||
			entry["pushed_up_to_date"] != true || entry["url"] != prURLs[i] {
			t.Fatalf("pull_requests[%d] = %#v, want layer %d open, pushed up to date, at %s",
				i, entry, i+1, prURLs[i])
		}
	}
}

// TestRefactorChildExecutionSingleParentJourney drives the same append pass
// on a `single` parent: a one-layer stack published as one pull request, a
// child whose single-row roadmap yields one layer, and a closure tail that
// appends one layer and one new pull request based on the original pull
// request's branch, with stack sections in both bodies.
func TestRefactorChildExecutionSingleParentJourney(t *testing.T) {
	if testing.Short() {
		t.Skip("journey boots real-git setup and scripted provider subprocesses")
	}

	fx := newRefactorAppendJourney(t, journeyPhaseRunnerOptions{
		perPhaseChildFiles:     true,
		echoDescriptionContext: true,
	})

	parent := driveRefactorAppendParent(t, fx, "Single parent", refactorJourneySingleParentRoadmap, 1, true)

	parentWorkspaceSlug := feature.WorkspaceSlug(parent.Slug, parent.ID)
	wantBranches := []string{
		git.LayerBranchName(parentWorkspaceSlug, 1, "parent-slice"),
		git.LayerBranchName(parentWorkspaceSlug, 2, "child-integration-slice"),
	}
	wantTitles := []string{"Parent slice", "Child integration slice"}

	if len(parent.Stack) != 1 {
		t.Fatalf("parent stack layers before the child = %d, want 1", len(parent.Stack))
	}
	if parent.Stack[0].Position != 1 || parent.Stack[0].Title != wantTitles[0] ||
		parent.Stack[0].Slug != "parent-slice" || parent.Stack[0].Branch != wantBranches[0] {
		t.Fatalf("parent stack layer 1 = %+v, want Parent slice on %s", parent.Stack[0], wantBranches[0])
	}
	if got := fx.pulls.CreatedCount(refactorJourneyGitHubRepo); got != 1 {
		t.Fatalf("pull requests before the child = %d, want the single parent layer", got)
	}
	parentWorktree := parent.Repos[0].WorktreePath
	if parentWorktree == "" {
		t.Fatalf("parent records no worktree path")
	}
	preLayer1Tip := journeyGit(t, fx.repoA, "rev-parse", "refs/heads/"+wantBranches[0])
	preLayer1Commits := journeyGit(t, fx.repoA, "rev-list", "main.."+wantBranches[0])

	// ------------------------------------------------------------------
	// Child: single-row roadmap, one layer appended, auto-publish tail.
	// ------------------------------------------------------------------
	childID := runRefactorAppendChild(t, fx, parent.ID, "Single rework", 1)
	waitForJourneyStackLayersPublished(t, fx.store, parent.ID, refactorJourneyRepo, 2)

	// ------------------------------------------------------------------
	// Assertions.
	// ------------------------------------------------------------------
	parent, err := fx.mgr.Get(parent.ID)
	if err != nil {
		t.Fatalf("reload parent after the tail: %v", err)
	}
	if len(parent.Stack) != 2 {
		t.Fatalf("parent stack layers after the child = %d, want 2", len(parent.Stack))
	}
	for i := 0; i < 2; i++ {
		layer := parent.Stack[i]
		if layer.Position != i+1 || layer.Title != wantTitles[i] || layer.Branch != wantBranches[i] {
			t.Fatalf("parent stack layer %d = %+v, want position %d titled %q on %s",
				i, layer, i+1, wantTitles[i], wantBranches[i])
		}
	}
	if origin := parent.Stack[1].Origin; origin == nil || origin.SourceFeatureID != childID || origin.SourceLayerPosition != 1 {
		t.Fatalf("appended layer 2 origin = %+v, want child %s layer 1", origin, childID)
	}
	if got := journeyGit(t, fx.repoA, "rev-parse", "refs/heads/"+wantBranches[0]); got != preLayer1Tip {
		t.Fatalf("layer 1 ref after the child = %s, want unchanged %s", got, preLayer1Tip)
	}
	if got := journeyGit(t, fx.repoA, "rev-list", "main.."+wantBranches[0]); got != preLayer1Commits {
		t.Fatalf("layer 1 commits after the child changed:\n got %s\nwant %s", got, preLayer1Commits)
	}
	child, err := fx.store.Load(childID)
	if err != nil {
		t.Fatalf("load closed child: %v", err)
	}
	if len(child.Stack) != 1 {
		t.Fatalf("child stack layers = %d, want 1", len(child.Stack))
	}
	wantTip := child.Stack[0].Repos[refactorJourneyRepo].TipSHA
	if wantTip == "" {
		t.Fatalf("child layer 1 records no %s tip", refactorJourneyRepo)
	}
	if got := parent.Stack[1].Repos[refactorJourneyRepo].TipSHA; got != wantTip {
		t.Fatalf("appended layer 2 tip = %s, want the child's layer 1 tip %s", got, wantTip)
	}
	if parent.Repos[0].Branch != wantBranches[1] {
		t.Fatalf("parent recorded branch = %q, want the appended layer's %q", parent.Repos[0].Branch, wantBranches[1])
	}
	if got := journeyGit(t, parentWorktree, "branch", "--show-current"); got != wantBranches[1] {
		t.Fatalf("parent worktree branch = %q, want %q", got, wantBranches[1])
	}

	// One new pull request, based on the original pull request's branch.
	created := fx.pulls.Created()
	if len(created) != 2 {
		t.Fatalf("created pull requests = %d, want 2 (one parent, one appended)", len(created))
	}
	if created[1].Base != wantBranches[0] || created[1].Head != wantBranches[1] || created[1].Title != wantTitles[1] {
		t.Fatalf("appended pull request = %+v, want %q on %s based on %s",
			created[1], wantTitles[1], wantBranches[1], wantBranches[0])
	}

	// Both pull request bodies carry the two-layer stack section, and the
	// appended body's description context names the child feature.
	prURLs := make([]string, 2)
	prNumbers := make([]int, 2)
	for i := 0; i < 2; i++ {
		layerEntry := parent.Stack[i].Repos[refactorJourneyRepo]
		if layerEntry.PRURL == "" || layerEntry.PRState != feature.StackPRStateOpen {
			t.Fatalf("layer %d entry = %+v, want an open pull request", i+1, layerEntry)
		}
		prURLs[i] = layerEntry.PRURL
		prNumbers[i] = journeyPRNumber(t, prURLs[i])
	}
	wantTopLine := fmt.Sprintf("2. Child integration slice - [#%d](%s) (open) (this pull request)", prNumbers[1], prURLs[1])
	for i := 0; i < 2; i++ {
		record, ok := fx.pulls.Pull(refactorJourneyGitHubRepo, prNumbers[i])
		if !ok {
			t.Fatalf("pull request %d record missing", prNumbers[i])
		}
		if !strings.Contains(record.Body, "## Stack") {
			t.Fatalf("pull request %d body lacks the stack section:\n%s", prNumbers[i], record.Body)
		}
		for j := 0; j < 2; j++ {
			wantLine := fmt.Sprintf("%d. %s - [#%d](", j+1, wantTitles[j], prNumbers[j])
			if !strings.Contains(record.Body, wantLine) {
				t.Fatalf("pull request %d body lacks the stack line %q:\n%s", prNumbers[i], wantLine, record.Body)
			}
		}
		if !strings.Contains(record.Body, wantTopLine) {
			t.Fatalf("pull request %d body lacks the top layer marker %q:\n%s", prNumbers[i], wantTopLine, record.Body)
		}
		wantContext := "describing Single parent."
		if i == 1 {
			wantContext = "describing Single rework."
		}
		if !strings.Contains(record.Body, wantContext) {
			t.Fatalf("pull request %d body lacks the scripted description context %q:\n%s", prNumbers[i], wantContext, record.Body)
		}
	}

	// The read model lists two pushed-up-to-date entries.
	parentDetail := getJourneyJSON(t, fx.srv.URL+"/api/v1/features/"+parent.ID)["feature"].(map[string]any)
	if parentDetail["status"] != feature.StatusPublished.String() {
		t.Fatalf("parent detail status = %v, want Published", parentDetail["status"])
	}
	repoStatuses, _ := parentDetail["repo_status"].([]any)
	var repoStatus map[string]any
	for _, row := range repoStatuses {
		if r, ok := row.(map[string]any); ok && r["name"] == refactorJourneyRepo {
			repoStatus = r
		}
	}
	if repoStatus == nil {
		t.Fatalf("parent detail repo_status has no %s row: %#v", refactorJourneyRepo, repoStatuses)
	}
	prEntries, _ := repoStatus["pull_requests"].([]any)
	if len(prEntries) != 2 {
		t.Fatalf("repo_status pull_requests = %#v, want one entry per layer", prEntries)
	}
	for i, row := range prEntries {
		entry, ok := row.(map[string]any)
		if !ok {
			t.Fatalf("pull_requests[%d] = %#v, want an object", i, row)
		}
		if entry["position"] != float64(i+1) || entry["state"] != string(feature.StackPRStateOpen) ||
			entry["pushed_up_to_date"] != true || entry["url"] != prURLs[i] {
			t.Fatalf("pull_requests[%d] = %#v, want layer %d open, pushed up to date, at %s",
				i, entry, i+1, prURLs[i])
		}
	}
}

// refactorAppendJourney bundles the stack-publish fixture the appended-layer
// journeys share: a bare scp-style origin, the in-test GitHub pull store, the
// scripted child phase runner, and the lifecycle/execution orchestrator pair
// (roadmap approval and mid-flight phases run on the lifecycle orchestrator
// without a PhaseRunner; the execution orchestrator owns sessions, the child
// pass, and every publish tail).
type refactorAppendJourney struct {
	t         *testing.T
	stateDir  string
	repoA     string
	bareA     string
	pulls     *testutil.FakePullStore
	cfg       *config.Config
	store     *feature.Store
	mgr       *feature.Manager
	lifecycle *orchestrator.Orchestrator
	exec      *orchestrator.Orchestrator
	srv       *httptest.Server
	client    *server.Client
}

// newRefactorAppendJourney builds the fixture; opts configure the scripted
// child phase runner shared by the child pass and every description session.
func newRefactorAppendJourney(t *testing.T, opts journeyPhaseRunnerOptions) *refactorAppendJourney {
	t.Helper()
	tmp := t.TempDir()
	stateDir := filepath.Join(tmp, "state")
	wtBaseDir := filepath.Join(tmp, "worktrees")
	remotesDir := filepath.Join(tmp, "remotes")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}

	// The bare origin's directory name parses as a git@localhost:acme
	// scp-style remote, so the production origin resolution reads
	// host/owner/repo identity from a remote that never leaves the test
	// machine while pull requests are served by the in-test GitHub fake.
	repoA := testutil.InitGitRepo(t)
	bareA := stackJourneyBareOrigin(t, remotesDir, repoA, refactorJourneyGitHubRepo)

	fake := testutil.InstallFakeGitHubAPI(t)
	pulls := testutil.NewFakePullStore("acme")
	pulls.Install(t, fake, refactorJourneyGitHubRepo)

	cfg := config.NewDefault()
	cfg.Repos[refactorJourneyRepo] = config.RepoConfig{Path: repoA}

	store := feature.NewStore(stateDir)
	wm := git.NewWorktreeManager(wtBaseDir)
	mgr := feature.NewManager(store, cfg)
	mgr.Worktrees = wm

	serverEvents := make(chan interface{}, 512)
	sm := session.NewManager(serverEvents)
	t.Cleanup(sm.Shutdown)
	pr := journeyChildPhaseRunnerWithOpts(t, sm, store, stateDir, opts)

	lifecycleOrch := orchestrator.New(orchestrator.Deps{
		Lifecycle: mgr,
		Store:     store,
		Worktrees: wm,
	}, orchestrator.Hooks{})
	t.Cleanup(func() {
		_ = lifecycleOrch.Shutdown()
		lifecycleOrch.WaitForCycles()
	})
	execOrch := orchestrator.New(orchestrator.Deps{
		Lifecycle:   mgr,
		Store:       store,
		Worktrees:   wm,
		Sessions:    sm,
		PhaseRunner: pr,
		CmdRunner:   pr.CommandRunner,
	}, orchestrator.Hooks{})
	t.Cleanup(func() {
		_ = execOrch.Shutdown()
		execOrch.WaitForCycles()
	})
	finalReviewStub := func(*feature.Feature, ...agent.KBInfo) (chan *agent.OrchestratorResult, error) {
		ch := make(chan *agent.OrchestratorResult, 1)
		ch <- &agent.OrchestratorResult{FinalStatus: "all_passed"}
		return ch, nil
	}
	lifecycleOrch.SetRunMultiRepoFinalReviewFn(finalReviewStub)
	execOrch.SetRunMultiRepoFinalReviewFn(finalReviewStub)

	stopForwarding := make(chan struct{})
	t.Cleanup(func() { close(stopForwarding) })
	go func() {
		for {
			select {
			case ev := <-execOrch.Events():
				select {
				case serverEvents <- ev:
				default:
				}
			case <-stopForwarding:
				return
			}
		}
	}()

	srv := httptest.NewServer(server.NewHandler(server.HandlerOptions{
		Runtime:               server.RuntimeIdentity{RuntimeDir: tmp, StateDir: stateDir},
		Features:              store,
		FeatureStore:          store,
		Events:                serverEvents,
		Mutations:             &journeyMutationTarget{mgr: mgr, orch: execOrch},
		DisableHostValidation: true,
	}))
	t.Cleanup(srv.Close)
	client, err := server.NewClient(server.ClientOptions{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("server.NewClient() error = %v", err)
	}
	return &refactorAppendJourney{
		t:         t,
		stateDir:  stateDir,
		repoA:     repoA,
		bareA:     bareA,
		pulls:     pulls,
		cfg:       cfg,
		store:     store,
		mgr:       mgr,
		lifecycle: lifecycleOrch,
		exec:      execOrch,
		srv:       srv,
		client:    client,
	}
}

// driveRefactorAppendParent creates one parent, approves its scripted roadmap
// through the lifecycle orchestrator, and drives its phases to completion —
// one distinct commit per phase — with the final phase's completion and the
// auto-publish tail on the execution orchestrator. The parent ends Published
// with one open pull request per roadmap row; single selects the
// single-delivery mode.
func driveRefactorAppendParent(t *testing.T, fx *refactorAppendJourney, name, roadmap string, phases int, single bool) *feature.Feature {
	t.Helper()
	createOpts := feature.CreateOptions{QueueSetup: true}
	if single {
		createOpts.DeliveryMode = feature.DeliveryModeSingle
	}
	f, err := fx.mgr.Create(name, "refactor append journey parent", []string{refactorJourneyRepo},
		fx.cfg.Defaults.Models, "", "", nil, createOpts)
	if err != nil {
		t.Fatalf("create parent %s: %v", name, err)
	}
	if err := fx.mgr.RunSetup(f.ID); err != nil {
		t.Fatalf("run setup %s: %v", name, err)
	}
	roadmapDir := filepath.Join(fx.stateDir, f.ID, "runs", "run-001", "roadmap")
	if err := os.MkdirAll(roadmapDir, 0o755); err != nil {
		t.Fatalf("mkdir roadmap: %v", err)
	}
	roadmapPath := filepath.Join(roadmapDir, "roadmap.md")
	if err := os.WriteFile(roadmapPath, []byte(roadmap), 0o644); err != nil {
		t.Fatalf("write roadmap: %v", err)
	}
	planGate := feature.PhasePlan
	if err := fx.store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.Status = feature.StatusPlanNeedsReview
		ff.PendingReviewPhase = &planGate
		ff.Artifacts = map[string]string{"roadmap": roadmapPath}
		return nil
	}); err != nil {
		t.Fatalf("seed roadmap gate: %v", err)
	}
	if err := fx.lifecycle.HandleReviewDecision(f.ID, orchestrator.ReviewDecision{
		Decision: "proceed",
		Roadmap:  true,
	}); err != nil {
		t.Fatalf("HandleReviewDecision: %v", err)
	}

	worktreeFor := func() string {
		t.Helper()
		ff, err := fx.mgr.Get(f.ID)
		if err != nil {
			t.Fatalf("get parent: %v", err)
		}
		return ff.Repos[0].WorktreePath
	}
	startImplementing := func(phase int) {
		t.Helper()
		if err := fx.store.Modify(f.ID, func(ff *feature.Feature) error {
			ff.Status = feature.StatusImplementing
			ff.CurrentPhase = feature.PhaseImplement
			ff.CurrentRoadmapPhase = phase
			ff.Checkpoints.ManualPublish = false
			if ff.RepoStates == nil {
				ff.RepoStates = map[string]*feature.RepoState{}
			}
			state, ok := ff.RepoStates[refactorJourneyRepo]
			if !ok {
				state = &feature.RepoState{}
				ff.RepoStates[refactorJourneyRepo] = state
			}
			state.Touched = true
			return nil
		}); err != nil {
			t.Fatalf("seed implementing phase %d: %v", phase, err)
		}
	}
	completePhaseOn := func(orch *orchestrator.Orchestrator, phase int) {
		t.Helper()
		if err := orch.HandlePhaseCompletion(f.ID, orchestrator.PhaseCompletionInput{
			Phase:           feature.PhaseImplement,
			MultiRepoResult: &agent.OrchestratorResult{FinalStatus: "all_passed"},
		}); err != nil {
			t.Fatalf("HandlePhaseCompletion phase %d: %v", phase, err)
		}
	}

	for phase := 1; phase <= phases; phase++ {
		startImplementing(phase)
		testutil.CommitFile(t, worktreeFor(), fmt.Sprintf("parent-phase-%d.txt", phase),
			fmt.Sprintf("parent phase %d work\n", phase), fmt.Sprintf("parent phase %d", phase))
		if phase == phases {
			// The final phase's completion, Final Review, and the
			// auto-publish tail share the execution orchestrator's
			// PhaseRunner so description sessions dispatch.
			completePhaseOn(fx.exec, phase)
		} else {
			completePhaseOn(fx.lifecycle, phase)
		}
	}

	deadline := time.Now().Add(90 * time.Second)
	for {
		ff, err := fx.mgr.Get(f.ID)
		if err != nil {
			t.Fatalf("get parent: %v", err)
		}
		if ff.Status == feature.StatusPublished {
			return ff
		}
		if time.Now().After(deadline) {
			t.Fatalf("parent %s status = %v, want Published before the deadline (checkpoints %+v)", name, ff.Status, ff.Checkpoints)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// runRefactorAppendChild launches one refactor child through the REST surface
// and drives it through setup, the roadmap review gate, one phase-plan review
// gate per roadmap phase (the scripted implementer commits one distinct file
// per phase, so the boundary split yields one child layer per roadmap row),
// and the transactional append integration, returning the child id once the
// relationship is durably Completed and the closure cleanup has settled.
func runRefactorAppendChild(t *testing.T, fx *refactorAppendJourney, parentID, name string, roadmapPhases int) string {
	t.Helper()

	resp, err := fx.client.RefactorFeature(t.Context(), parentID, server.RefactorFeatureRequest{
		Name:     name,
		Pipeline: feature.PipelineMedium,
		Checkpoints: feature.Checkpoints{
			RoadmapReview:   true,
			PhasePlanReview: true,
			ManualPublish:   false,
		},
	})
	if err != nil {
		t.Fatalf("RefactorFeature() error = %v", err)
	}
	childID := resp.FeatureID
	if childID == "" {
		t.Fatalf("RefactorFeature() = %+v; want child id", resp)
	}

	childBody := waitForJourneySetupComplete(t, fx.srv.URL, childID)
	if childBody["status"] != feature.StatusCreated.String() {
		t.Fatalf("child durable status = %v, want Created (parked after setup)", childBody["status"])
	}

	postAction(t, fx.srv.URL, childID, "start", `{}`)

	waitForJourneyGate(t, fx.srv.URL, childID, 0)
	postReviewSessionProceed(t, fx.srv.URL, childID)
	for phase := 1; phase <= roadmapPhases; phase++ {
		waitForJourneyGate(t, fx.srv.URL, childID, float64(phase))
		postReviewSessionProceed(t, fx.srv.URL, childID)
	}
	waitForJourneyChildClosed(t, fx.srv.URL, fx.store, childID)
	return childID
}
