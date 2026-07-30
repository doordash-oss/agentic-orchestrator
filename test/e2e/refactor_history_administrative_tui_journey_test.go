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
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// TestRefactorHistoryAdministrativeTUIJourney drives the closed-child
// history surface through the real API-driven TUI model against a live
// server: two sequential refactor children are launched, executed, and
// integrated through TUI keys (with child B additionally exercising the
// restart/stop/resume path). The journey then expands the "Refactor History
// (N)" group via keyboard, asserts newest-first order, inspects the
// read-only closed-child detail, collapses back to the parent, and proves
// the typed active-child lock reason appears on the status line when a
// disabled parent mutation is pressed. REST cross-checks pin the
// server-authoritative child_history ordering and the top-level projection.
func TestRefactorHistoryAdministrativeTUIJourney(t *testing.T) {
	if testing.Short() {
		t.Skip("journey boots real-git setup and scripted provider subprocesses")
	}

	fx := newTUIJourneyFixture(t, tuiJourneyFixtureOptions{
		ParentID:           "history-parent",
		ParentName:         "History Parent",
		ParentSelfWorktree: true,
	})
	parent := fx.Parent
	srv := fx.Server
	store := fx.Store
	// The fixture's phase-plan lock file gives the restart segment of
	// child B a deterministic running window.
	blockFile := fx.BlockFile

	screenshotsDir := journeyScreenshotDir(t)

	h := fx.NewHarness(t)
	if got := h.SelectedFeatureID(); got != parent.ID {
		t.Fatalf("cold boot selected feature = %q, want %q", got, parent.ID)
	}

	// launchRefactor drives one child through the TUI wizard and returns
	// once setup parked it at Created.
	launchRefactor := func(name, desc string) string {
		t.Helper()
		return journeyLaunchRefactor(t, fx, h, name, desc)
	}

	// runChildToClosed starts the child through the TUI and waits for the
	// relationship to durably close. This journey's parent carries no
	// review checkpoints, so the scripted pipeline runs straight through;
	// the running-window segments gate on the lock file instead.
	runChildToClosed := func(childID string) {
		t.Helper()
		journeyRunChildToClosed(t, fx, h, childID)
	}

	// ------------------------------------------------------------------
	// Child A: full lifecycle to Closed — Completed.
	// ------------------------------------------------------------------
	childA := launchRefactor("Alpha Rework", "First sequential refactor pass")
	runChildToClosed(childA)
	// Mirror the lifecycle.updated SSE bundle: the closure updates the
	// parent and the child detail as one ordered refresh.
	h.RefreshRelationship(parent.ID, childA)
	assertActiveChildAbsent := func() {
		if body := journeyFeatureBody(srv.URL, parent.ID); body["active_child"] != nil {
			t.Fatalf("parent active_child = %v, want nil", body["active_child"])
		}
	}
	assertActiveChildAbsent()
	t.Logf("child A (%s) closed Completed", childA)

	// ------------------------------------------------------------------
	// Child B: launched sequentially after A closed; while B is active the
	// dashboard shows the "Refactoring" parent with A's collapsed history
	// group beneath.
	// ------------------------------------------------------------------
	h.Refresh()
	if got := h.SelectedFeatureID(); got != parent.ID {
		t.Fatalf("after child A closure selected feature = %q, want parent fallback %q", got, parent.ID)
	}
	childB := launchRefactor("Bravo Rework", "Second sequential refactor pass")
	h.Refresh()
	assertViewContains(t, h.View(), "Refactoring", "Refactor History (1)")
	dashActive := ansi.Strip(h.View())
	if strings.Contains(dashActive, childA) {
		t.Fatalf("collapsed history row leaked child A id into the dashboard:\n%s", dashActive)
	}
	journeyCapture(t, screenshotsDir, "parent-with-active-child-always-visible-and-its-closed-refactor-history-group-co-1200x800.png", h.View())
	t.Logf("captured active-parent-with-collapsed-history (NAMED CAPTURE 1)")

	// Start B with the phase-plan lock file in place so the running window
	// is deterministic: the scripted session parks on the lock file and the
	// child stays durably Planning.
	if err := os.WriteFile(blockFile, []byte("block\n"), 0o644); err != nil {
		t.Fatalf("write block file: %v", err)
	}
	h.PressKey(tea.KeyPressMsg{Code: 'a', Text: "a"})
	waitForJourneyStatus(t, srv.URL, childB, feature.StatusPlanning.String())
	waitForJourneyRunningSession(t, srv.URL, childB)
	t.Logf("child B (%s) started; planning phase running (blocked)", childB)

	// ------------------------------------------------------------------
	// Child B restart segment: restart through the TUI while the phase is
	// running and prove the replacement phase-plan session is durably
	// running under the lock file again.
	// ------------------------------------------------------------------
	h.PressKey(tea.KeyPressMsg{Code: 'r', Text: "r"})
	h.PressKey(tea.KeyPressMsg{Code: 'y', Text: "y"})
	waitForJourneyStatus(t, srv.URL, childB, feature.StatusPlanning.String())
	time.Sleep(time.Second)
	if st := journeyFeatureBody(srv.URL, childB); st["status"] != feature.StatusPlanning.String() {
		t.Fatalf("child B status after restart = %v, want stable %s", st["status"], feature.StatusPlanning)
	}
	waitForJourneyRunningSession(t, srv.URL, childB)
	t.Logf("child B restarted through the TUI; planning phase running (blocked)")

	// ------------------------------------------------------------------
	// Disabled parent mutation: while child B is running, Restart ('r') on
	// the parent must surface the typed active-child lock reason in the
	// status line without opening a confirmation.
	// ------------------------------------------------------------------
	h.Refresh()
	for i := 0; i < 6 && h.SelectedFeatureID() != parent.ID; i++ {
		h.Press(tea.KeyUp)
	}
	if got := h.SelectedFeatureID(); got != parent.ID {
		t.Fatalf("could not select parent row; selected = %q", got)
	}
	h.Refresh() // refresh the parent's cached detail so the action catalog is current
	h.PressKey(tea.KeyPressMsg{Code: 'r', Text: "r"})
	if status := h.StatusMessage(); !strings.Contains(status, "parent mutations are locked while a child is active") {
		t.Fatalf("status after 'r' on locked parent = %q, want the typed active-child lock reason", status)
	} else if !strings.Contains(status, "Restart —") {
		t.Fatalf("status = %q, want the Restart kind prefix", status)
	}
	if strings.Contains(ansi.Strip(h.View()), "[y] Confirm") {
		t.Fatalf("disabled parent restart opened a confirmation:\n%s", ansi.Strip(h.View()))
	}
	journeyCapture(t, screenshotsDir, "disabled-parent-mutation-showing-the-precise-active-child-lock-reason-in-the-sta-1200x800.png", h.View())
	t.Logf("captured disabled parent mutation lock reason (NAMED CAPTURE 6)")

	// Back to child B: stop the blocked phase, resume after clearing the
	// block, and complete it through the second gate.
	for i := 0; i < 6 && h.SelectedFeatureID() != childB; i++ {
		h.Press(tea.KeyDown)
	}
	if got := h.SelectedFeatureID(); got != childB {
		t.Fatalf("could not re-select child B; selected = %q", got)
	}
	h.Refresh()
	h.PressKey(tea.KeyPressMsg{Code: 's', Text: "s"})
	h.PressKey(tea.KeyPressMsg{Code: 'y', Text: "y"})
	waitForJourneyStatus(t, srv.URL, childB, feature.StatusInterrupted.String())
	waitForJourneyFeatureSessionsQuiescent(t, srv.URL, childB)
	if err := os.Remove(blockFile); err != nil {
		t.Fatalf("remove block file: %v", err)
	}
	h.Refresh()
	h.PressKey(tea.KeyPressMsg{Code: 'a', Text: "a"}) // contextual resume
	waitForJourneyChildClosed(t, srv.URL, store, childB)
	h.RefreshRelationship(parent.ID, childB)
	assertActiveChildAbsent()
	t.Logf("child B (%s) closed Completed", childB)

	// ------------------------------------------------------------------
	// Expand the history group via keyboard: navigate to the
	// "Refactor History (2)" group row and press Enter. Newest-first order
	// puts B above A; selecting B shows the read-only closed detail.
	// ------------------------------------------------------------------
	h.Refresh()
	if got := h.SelectedFeatureID(); got != parent.ID {
		t.Fatalf("after child B closure selected feature = %q, want parent fallback %q", got, parent.ID)
	}
	assertViewContains(t, h.View(), "Refactor History (2)")

	// From the parent row the group row sits two slots beneath (parent, no
	// active child, group). Navigate until Enter expands the group.
	journeyExpandHistoryGroup(t, h, parent.ID, "alpha-rework")
	expandedView := ansi.Strip(h.View())
	assertViewContains(t, expandedView, "Refactor History (2)", "bravo-rework", "alpha-rework", "Completed")
	if strings.Index(expandedView, "bravo-rework") > strings.Index(expandedView, "alpha-rework") {
		t.Fatalf("expanded history not newest-first (B must render above A):\n%s", expandedView)
	}
	t.Logf("history group expanded newest-first through the keyboard")

	for i := 0; i < 4 && h.SelectedFeatureID() != childB; i++ {
		h.Press(tea.KeyDown)
	}
	if got := h.SelectedFeatureID(); got != childB {
		t.Fatalf("could not select closed child B row; selected = %q\n%s", got, expandedView)
	}
	journeyCapture(t, screenshotsDir, "refactor-history-expanded-newest-first-with-a-completed-closed-child-selected-da-1200x800.png", h.View())
	t.Logf("captured expanded history with completed child selected (NAMED CAPTURE 2)")

	// Focus the detail panel so the closed child's inspection-only footer
	// renders, then capture the read-only banner surface. A settled child
	// keeps exactly the inspection affordances — Logs, Diff, Back — and
	// never a workspace-config or mutation hint.
	h.Press(tea.KeyTab)
	closedDetailView := ansi.Strip(h.View())
	assertViewContains(t, closedDetailView,
		"Read only", "Closed — Completed", "immutable refactor history, inspection only.",
	)
	closedFooter := journeyHintLine(t, closedDetailView, "[l] Logs")
	for _, want := range []string{"[l] Logs", "[v] Diff", "[←/esc] Back"} {
		if !strings.Contains(closedFooter, want) {
			t.Fatalf("closed child footer missing inspection hint %q in: %s", want, closedFooter)
		}
	}
	for _, forbidden := range []string{
		"Workspace Config", "Publish", "Merge", "Discard", "Rewind", "Delete", "Resume", "Start", "Stop",
		"[e] Edit config", "[Shift+F] Refactor",
	} {
		if strings.Contains(closedFooter, forbidden) {
			t.Fatalf("closed child footer shows mutation affordance %q in: %s", forbidden, closedFooter)
		}
	}
	// No mutation copy may appear anywhere on the closed-child detail
	// surface — the Rewind & Upgrade lightbulb panel stays suppressed even
	// though the API action catalog still explains why mutations are
	// unavailable.
	for _, forbidden := range []string{"Rewind & Upgrade", "Press ctrl+r", "ctrl+r", "Workspace Config"} {
		if strings.Contains(closedDetailView, forbidden) {
			t.Fatalf("closed child detail surface shows mutation affordance %q in:\n%s", forbidden, closedDetailView)
		}
	}
	journeyCapture(t, screenshotsDir, "full-closed-child-detail-showing-the-closed-outcome-banner-and-inspection-only-f-1200x800.png", h.View())
	t.Logf("captured closed-child read-only detail (NAMED CAPTURE 3)")

	// Collapse the group: selection returns to the parent. From the closed
	// child row the group anchor sits exactly one row up.
	h.Press(tea.KeyEscape) // focus back to the list panel
	h.Press(tea.KeyUp)     // closed child B row → group anchor row
	h.Press(tea.KeyEnter)  // toggle the group row under the cursor
	if strings.Contains(ansi.Strip(h.View()), "alpha-rework") {
		t.Fatalf("Refactor History group still expanded after collapse:\n%s", ansi.Strip(h.View()))
	}
	if got := h.SelectedFeatureID(); got != parent.ID {
		t.Fatalf("after collapse selected feature = %q, want parent %q", got, parent.ID)
	}
	t.Logf("collapsing the history group returned selection to the parent")

	// ------------------------------------------------------------------
	// REST cross-checks: child_history is newest-first A+B on the parent,
	// and closed children never surface top-level.
	// ------------------------------------------------------------------
	parentBody := journeyFeatureBody(srv.URL, parent.ID)
	history, ok := parentBody["child_history"].([]any)
	if !ok || len(history) != 2 {
		t.Fatalf("parent child_history = %v, want two entries", parentBody["child_history"])
	}
	if history[0].(map[string]any)["id"] != childB || history[1].(map[string]any)["id"] != childA {
		t.Fatalf("child_history order = %v, %v, want newest-first %s, %s",
			history[0].(map[string]any)["id"], history[1].(map[string]any)["id"], childB, childA)
	}
	list := getJourneyJSON(t, srv.URL+"/api/v1/features")
	summaries := list["features"].([]any)
	if len(summaries) != 1 || summaries[0].(map[string]any)["id"] != parent.ID {
		t.Fatalf("top-level list = %+v, want only the parent (closed children stay nested)", summaries)
	}
	t.Logf("journey complete: sequential children, history group navigation, read-only closed detail, locked-mutation status")
}

// waitForJourneyRunningSession polls until the feature has at least one
// durably running session. Session creation trails the status transition —
// the roadmap phase's own session completes before the phase-plan session
// even exists — so a one-shot check races ahead of the session row.
func waitForJourneyRunningSession(t *testing.T, baseURL, featureID string) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		sessions, err := getJourneyJSONQuiet(baseURL + "/api/v1/sessions")
		if err == nil && sessions != nil {
			for _, row := range sessions["sessions"].([]any) {
				sess, _ := row.(map[string]any)
				if sess["feature_id"] == featureID && sess["status"] == "Running" {
					return
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	assertJourneySessionStatus(t, baseURL, featureID, "Running") // fail with the full last snapshot
}
