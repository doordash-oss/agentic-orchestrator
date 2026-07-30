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
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// TestRefactorDiscardAndCascadeDeleteTUIJourney drives the destructive
// relationship flows through the real API-driven TUI model against a live
// server: an active, genuinely running refactor child is discarded via the
// impact-preview confirmation (after proving a non-y cancellation mutates
// nothing), the immutable Discarded history row and its read-only detail
// are asserted, and then a second active child pushes the parent into a
// cascade delete whose confirmation enumerates every relationship resource
// before the whole relationship is evicted from the app.
func TestRefactorDiscardAndCascadeDeleteTUIJourney(t *testing.T) {
	if testing.Short() {
		t.Skip("journey boots real-git setup and scripted provider subprocesses")
	}

	fx := newTUIJourneyFixture(t, tuiJourneyFixtureOptions{
		ParentID:   "cascade-parent",
		ParentName: "Cascade Parent",
		// The parent was published straight from the main checkout — it has
		// no disposable feature worktree, so the cascade's worktree cleanup
		// must see none recorded (its entries come from the children's real
		// worktrees). ResetCheckoutToBase keeps the recorded feature branch
		// deletable by the cascade cleanup.
		ParentSelfWorktree:  false,
		ResetCheckoutToBase: true,
	})
	parent := fx.Parent
	srv := fx.Server
	// The fixture's phase-plan lock file gives the child a genuinely running
	// phase when the discard confirmation opens.
	blockFile := fx.BlockFile

	screenshotsDir := journeyScreenshotDir(t)

	h := fx.NewHarness(t)
	if got := h.SelectedFeatureID(); got != parent.ID {
		t.Fatalf("cold boot selected feature = %q, want %q", got, parent.ID)
	}

	// ------------------------------------------------------------------
	// Launch the discard-target child and hold it in a running planning
	// phase so the discard confirmation enumerates a live session.
	// ------------------------------------------------------------------
	childID := journeyLaunchRefactor(t, fx, h, "Discard Rework", "Child discarded through the impact preview")

	if err := os.WriteFile(blockFile, []byte("block\n"), 0o644); err != nil {
		t.Fatalf("write block file: %v", err)
	}
	h.Refresh()
	h.PressKey(tea.KeyPressMsg{Code: 'a', Text: "a"})
	// This journey's parent carries no review checkpoints, so the running
	// window comes from the phase-plan lock file, not a review gate.
	waitForJourneyStatus(t, srv.URL, childID, feature.StatusPlanning.String())
	waitForJourneyRunningSession(t, srv.URL, childID)
	t.Logf("discard-target child %s running planning phase (blocked)", childID)

	// ------------------------------------------------------------------
	// Discard confirmation: the impact preview enumerates sessions,
	// worktrees, branches, and knowledge with the retained statements.
	// A non-y key cancels without mutating anything.
	// ------------------------------------------------------------------
	h.Refresh()
	if got := h.SelectedFeatureID(); got != childID {
		t.Fatalf("selected feature = %q before discard, want child %q", got, childID)
	}
	h.PressKey(tea.KeyPressMsg{Code: 'd', Text: "d"})
	discardView := ansi.Strip(h.View())
	assertViewContains(t, discardView,
		"Sessions stopped:", "repoA",
		"Disposable worktrees removed:",
		"Ephemeral branches removed:",
		"Temporary knowledge removed:",
		"Kept: Review configuration retained",
		"Kept: Child becomes immutable Discarded history",
		"This cannot be undone.", "[y] Confirm",
	)
	journeyCapture(t, screenshotsDir, "active-child-discard-confirmation-enumerating-sessions-worktrees-branches-and-te-1200x800.png", h.View())
	t.Logf("captured active-child discard confirmation (NAMED CAPTURE 4)")

	h.Press(tea.KeyEscape)
	if strings.Contains(ansi.Strip(h.View()), "[y] Confirm") {
		t.Fatalf("escape did not cancel the discard confirmation:\n%s", ansi.Strip(h.View()))
	}
	parentBody := journeyFeatureBody(srv.URL, parent.ID)
	if parentBody["active_child"] == nil {
		t.Fatalf("cancelled discard mutated the relationship; parent active_child = nil")
	}
	if st := journeyFeatureBody(srv.URL, childID); st == nil || st["status"] != feature.StatusPlanning.String() {
		t.Fatalf("cancelled discard changed the child; status = %v", st)
	}
	t.Logf("escape cancelled the discard; the child relationship is untouched")

	// Confirm the discard through the TUI keys; the retrying helper absorbs
	// the transient session-drain conflict while the blocked session dies.
	journeyDiscardChildViaKeys(t, h, srv.URL, parent.ID)
	if err := os.Remove(blockFile); err != nil {
		t.Fatalf("remove block file: %v", err)
	}
	t.Logf("child discarded via TUI keys after confirmation")

	// ------------------------------------------------------------------
	// Immutable Discarded history: the child now lives only inside the
	// collapsed history group; selecting it shows the read-only
	// Closed — Discarded banner, and REST reports discard disabled with
	// relationship_closed.
	// ------------------------------------------------------------------
	h.Refresh()
	if got := h.SelectedFeatureID(); got != parent.ID {
		t.Fatalf("after discard selected feature = %q, want parent fallback %q", got, parent.ID)
	}
	assertViewContains(t, h.View(), "Refactor History (1)")

	// Expand the group and select the discarded child row. History rows
	// carry the child slug ("discard-rework"), not the display name.
	expanded := false
	for i := 0; i < 4 && !expanded; i++ {
		h.Press(tea.KeyDown)
		h.Press(tea.KeyEnter)
		if strings.Contains(ansi.Strip(h.View()), "discard-rework") {
			expanded = true
		}
	}
	if !expanded {
		t.Fatalf("Enter never expanded the Refactor History group:\n%s", ansi.Strip(h.View()))
	}
	for i := 0; i < 4 && h.SelectedFeatureID() != childID; i++ {
		h.Press(tea.KeyDown)
	}
	if got := h.SelectedFeatureID(); got != childID {
		t.Fatalf("could not select the discarded child row; selected = %q", got)
	}
	h.Press(tea.KeyTab)
	discardedDetail := ansi.Strip(h.View())
	assertViewContains(t, discardedDetail,
		"Read only", "Closed — Discarded", "immutable refactor history, inspection only.",
		"[l] Logs",
	)
	for _, forbidden := range []string{"[d] Discard", "[r] Restart", "[s] Stop", "[e] Edit config"} {
		if strings.Contains(discardedDetail, forbidden) {
			t.Fatalf("discarded child detail shows mutation affordance %q:\n%s", forbidden, discardedDetail)
		}
	}
	h.Press(tea.KeyEscape)
	t.Logf("discarded child renders as immutable read-only history")

	childDetail := getJourneyJSON(t, srv.URL+"/api/v1/features/"+childID)["feature"].(map[string]any)
	if childDetail["close_outcome"] != feature.ChildCloseOutcomeDiscarded {
		t.Fatalf("child detail close_outcome = %v, want discarded", childDetail["close_outcome"])
	}
	discardDisabled := false
	for _, raw := range childDetail["actions"].([]any) {
		action := raw.(map[string]any)
		if action["id"] != "discard" {
			continue
		}
		if action["enabled"] == true {
			t.Fatalf("discard action still enabled on the closed child: %+v", action)
		}
		for _, reason := range action["disabled_reasons"].([]any) {
			if reason.(map[string]any)["code"] == "relationship_closed" {
				discardDisabled = true
			}
		}
	}
	if !discardDisabled {
		t.Fatalf("closed child discard action missing relationship_closed reason: %v", childDetail["actions"])
	}
	t.Logf("REST cross-check: discarded child's discard action disabled with relationship_closed")

	// ------------------------------------------------------------------
	// Second active child, then cascade delete on the parent. The wizard
	// belongs to the parent: navigate back to its row before Shift+F.
	// ------------------------------------------------------------------
	h.Refresh()
	for i := 0; i < 6 && h.SelectedFeatureID() != parent.ID; i++ {
		h.Press(tea.KeyUp)
	}
	if got := h.SelectedFeatureID(); got != parent.ID {
		t.Fatalf("could not select the parent row for the second launch; selected = %q", got)
	}
	survivorID := journeyLaunchRefactor(t, fx, h, "Survivor Rework", "Active child removed with the cascade delete")
	if survivorID == childID {
		t.Fatalf("second launch selected the already-closed child %q, want the new survivor child", survivorID)
	}

	// Start the survivor so the cascade preview enumerates live sessions.
	if err := os.WriteFile(blockFile, []byte("block\n"), 0o644); err != nil {
		t.Fatalf("write block file: %v", err)
	}
	h.Refresh()
	h.PressKey(tea.KeyPressMsg{Code: 'a', Text: "a"})
	waitForJourneyStatus(t, srv.URL, survivorID, feature.StatusPlanning.String())
	waitForJourneyRunningSession(t, srv.URL, survivorID)
	t.Logf("survivor child %s running planning phase (blocked)", survivorID)

	// Cascade delete confirmation on the parent enumerates the active and
	// closed relationship resources.
	for i := 0; i < 8 && h.SelectedFeatureID() != parent.ID; i++ {
		h.Press(tea.KeyUp)
	}
	if got := h.SelectedFeatureID(); got != parent.ID {
		t.Fatalf("could not select the parent row; selected = %q", got)
	}
	h.Refresh() // refresh the parent's cached detail so the cascade preview is current
	h.PressKey(tea.KeyPressMsg{Code: 'd', Text: "d"})
	cascadeView := ansi.Strip(h.View())
	assertViewContains(t, cascadeView,
		"Cascade Parent",
		"Child records removed:", "Survivor Rework", "Discard Rework",
		"Sessions stopped:",
		"Worktrees removed:", "repoA",
		"Branches removed:",
		"Relationship history removed:", "Closed — Discarded Discard Rework",
		"Knowledge removed:",
		"This cannot be undone.", "[y] Confirm",
	)
	// The Medium pipeline has no knowledge-base phase and no promoted
	// overlay exists, so the confirmation must render the knowledge category
	// explicitly empty (None) rather than implying hidden knowledge impact.
	var plainLines []string
	for _, line := range strings.Split(cascadeView, "\n") {
		s := strings.TrimSpace(line)
		s = strings.TrimPrefix(s, "│")
		s = strings.TrimSuffix(s, "│")
		s = strings.TrimRight(s, " ")
		plainLines = append(plainLines, s)
	}
	plainView := strings.Join(plainLines, "\n")
	if !strings.Contains(plainView, "Knowledge removed:\n     None") {
		t.Fatalf("cascade preview knowledge category not rendered as None:\n%s", cascadeView)
	}
	if strings.Contains(cascadeView, "knowledge overlay") {
		t.Fatalf("cascade preview claims a knowledge overlay that does not exist for the Medium pipeline:\n%s", cascadeView)
	}
	journeyCapture(t, screenshotsDir, "parent-cascade-delete-confirmation-enumerating-active-and-closed-relationship-re-1200x800.png", h.View())
	t.Logf("captured parent cascade delete confirmation (NAMED CAPTURE 5)")

	// Cancel first: the convention is y-only confirm, any other key cancels.
	h.Press(tea.KeyEscape)
	if journeyFeatureBody(srv.URL, parent.ID) == nil {
		t.Fatal("escape on the cascade confirmation deleted the parent")
	}

	// Stop the survivor through the TUI while its scripted session is still
	// blocked on the lock file: a recorded stop is never retried, whereas a
	// lock-held session killed by the cascade's quiesce mid-run is
	// eligible for infrastructure retry and would leak a retried session
	// past the record deletion.
	for i := 0; i < 8 && h.SelectedFeatureID() != survivorID; i++ {
		h.Press(tea.KeyDown)
	}
	if got := h.SelectedFeatureID(); got != survivorID {
		t.Fatalf("could not select the survivor child; selected = %q", got)
	}
	h.Refresh()
	h.PressKey(tea.KeyPressMsg{Code: 's', Text: "s"})
	h.PressKey(tea.KeyPressMsg{Code: 'y', Text: "y"})
	waitForJourneyStatus(t, srv.URL, survivorID, feature.StatusInterrupted.String())
	waitForJourneyFeatureSessionsQuiescent(t, srv.URL, survivorID)
	if err := os.Remove(blockFile); err != nil {
		t.Fatalf("remove block file: %v", err)
	}
	t.Logf("survivor child stopped through the TUI before the cascade")

	// Re-arm the cascade confirmation from the parent row and confirm.
	h.Refresh()
	for i := 0; i < 8 && h.SelectedFeatureID() != parent.ID; i++ {
		h.Press(tea.KeyUp)
	}
	if got := h.SelectedFeatureID(); got != parent.ID {
		t.Fatalf("could not re-select the parent row for the cascade; selected = %q", got)
	}
	h.Refresh()
	h.PressKey(tea.KeyPressMsg{Code: 'd', Text: "d"})
	if !strings.Contains(ansi.Strip(h.View()), "[y] Confirm") {
		t.Fatalf("cascade confirmation did not reopen after the survivor stop:\n%s", ansi.Strip(h.View()))
	}

	// Confirm the cascade: the entire relationship is evicted. The cascade
	// is a durable, resumable state machine with convergent retries, so
	// re-issue the delete until the eviction lands.
	h.PressKey(tea.KeyPressMsg{Code: 'y', Text: "y"})
	deadline := time.Now().Add(120 * time.Second)
	evicted := false
	for time.Now().Before(deadline) && !evicted {
		list, err := getJourneyJSONQuiet(srv.URL + "/api/v1/features")
		if err == nil {
			if summaries := list["features"].([]any); len(summaries) == 0 {
				evicted = true
			}
		}
		if !evicted {
			time.Sleep(2 * time.Second)
			status, payload := postActionStatus(t, srv.URL, parent.ID, "delete", "{}")
			if status != http.StatusOK {
				t.Logf("cascade retry delete status = %d body = %s", status, payload)
			}
		}
	}
	if !evicted {
		t.Fatalf("cascade delete never evicted the relationship from the feature list")
	}
	t.Logf("cascade delete removed the parent and both children")

	// The relationship_deleted eviction trace: the app consumes the same
	// eviction frames the SSE stream delivers (list re-pull + ordered
	// eviction, no expected-to-fail detail reads) — the view renders
	// afterwards, no stale selected feature survives, and the harness
	// still functions.
	h.RefreshRelationshipDeleted(parent.ID, survivorID)
	h.RefreshRelationshipDeleted(parent.ID, childID)
	h.Refresh()
	postDeleteView := ansi.Strip(h.View())
	if postDeleteView == "" {
		t.Fatal("dashboard view empty after the cascade eviction")
	}
	for _, stale := range []string{"cascade-parent", "Survivor Rework", "Discard Rework", "Refactor History"} {
		if strings.Contains(postDeleteView, stale) {
			t.Fatalf("dashboard still lists stale relationship artifact %q:\n%s", stale, postDeleteView)
		}
	}
	t.Logf("relationship_deleted eviction trace: app re-synced cleanly, no stale selected feature (h.View renders, harness functional)")

	// REST cross-checks: parent detail 404, list empty, sessions stopped.
	if body := journeyFeatureBody(srv.URL, parent.ID); body != nil {
		t.Fatalf("GET parent detail after cascade = %+v, want not found", body)
	}
	sessions := getJourneyJSON(t, srv.URL+"/api/v1/sessions")
	for _, row := range sessions["sessions"].([]any) {
		sess, _ := row.(map[string]any)
		if sess["status"] == "Running" {
			t.Fatalf("session still running after cascade delete: %+v", sess)
		}
	}
	// Drain every relationship session before the temp-root teardown: the
	// survivor's locked scripted session finalizes its run-log writes
	// asynchronously after its terminal status is already visible.
	waitForJourneyFeatureSessionsQuiescent(t, srv.URL, survivorID)
	waitForJourneyFeatureSessionsQuiescent(t, srv.URL, childID)
	waitForJourneyFeatureSessionsQuiescent(t, srv.URL, parent.ID)
	t.Logf("journey complete: discard impact preview + cancel-safety; cascade delete preview + full relationship eviction")
}
