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
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// childRecordMissFailer wraps the real feature store and, once armed, fails
// exactly one Modify on the child: the closure-tail update that clears the
// removed disposable-worktree path from the child record. This is a
// deterministic non-fatal failure AFTER the disposable worktree cleanup —
// the worktree directory is already gone from disk, the merge stays durable,
// and the closure tail records the residual-record cleanup warning on the
// transaction journal, giving the journey a real post-cleanup warning to
// inspect. Reconciliation never re-runs cleanup inside a journey, so the
// recorded warning is stable for inspection.
type childRecordMissFailer struct {
	*feature.Store
	childID string
	armed   atomic.Bool
	failed  atomic.Bool
}

func (s *childRecordMissFailer) Modify(id string, fn func(*feature.Feature) error) error {
	if s.armed.Load() && id == s.childID && s.failed.CompareAndSwap(false, true) {
		return errors.New("simulated worktree record clear failure")
	}
	return s.Store.Modify(id, fn)
}

// removeRefArmer wraps the real worktree manager and arms the record-clear
// failer on the first successful disposable-worktree removal, so the very
// next child Modify — the closure tail's path-clearing update — observes the
// deterministic record failure while the directory is already gone. The
// child id is assigned once the journey launches the child.
type removeRefArmer struct {
	*git.WorktreeManager
	failer *childRecordMissFailer
}

func (w *removeRefArmer) RemoveRef(worktreePath, mainRepo, branch string) error {
	err := w.WorktreeManager.RemoveRef(worktreePath, mainRepo, branch)
	if err == nil {
		w.failer.armed.Store(true)
	}
	return err
}

// TestClosedRefactorInspectionAfterCleanupTUIJourney exercises the packaged
// behavioral contract of the closed-refactor inspection surface: after a
// refactor child closes Completed with its disposable worktree successfully
// removed from disk (recording one residual-record cleanup warning), the
// settled child opens its logs, cycles through its retained artifacts,
// inspects repository context and cleanup warnings, and opens its preserved
// read-only diff — all through the real API-driven TUI model against a live
// server.
func TestClosedRefactorInspectionAfterCleanupTUIJourney(t *testing.T) {
	if testing.Short() {
		t.Skip("journey boots real-git setup and scripted provider subprocesses")
	}

	failer := &childRecordMissFailer{}
	fx := newTUIJourneyFixture(t, tuiJourneyFixtureOptions{
		ParentID:           "closed-inspection-parent",
		ParentName:         "Closed Inspection Parent",
		ParentSelfWorktree: true,
		WrapWorktrees: func(wm *git.WorktreeManager) ports.WorktreeOperator {
			return &removeRefArmer{WorktreeManager: wm, failer: failer}
		},
		WrapOrchestratorStore: func(s *feature.Store) ports.FeatureStore {
			failer.Store = s
			return failer
		},
	})
	parent := fx.Parent
	srv := fx.Server
	screenshotsDir := journeyScreenshotDir(t)

	h := fx.NewHarness(t)
	if got := h.SelectedFeatureID(); got != parent.ID {
		t.Fatalf("cold boot selected feature = %q, want %q", got, parent.ID)
	}

	// ------------------------------------------------------------------
	// Step 1: Launch a refactor child and run it to Closed — Completed
	// with its disposable worktree successfully removed and one recorded
	// cleanup warning. The scripted implement kernel leaves a real changed
	// file in the child worktree (preserved as the close-time DiffSummary);
	// the closure tail removes the worktree from disk, then the record
	// update clearing its path fails exactly once, leaving the residual
	// recorded path plus the warning while the directory itself is gone.
	// ------------------------------------------------------------------
	childID := journeyLaunchRefactor(t, fx, h, "Gamma Rework",
		"Child closed Completed after disposable worktree cleanup")
	failer.childID = childID
	h.Refresh()
	h.PressKey(tea.KeyPressMsg{Code: 'a', Text: "a"})
	waitForJourneyChildClosed(t, srv.URL, fx.Store, childID)
	h.RefreshRelationship(parent.ID, childID)
	if !failer.failed.Load() {
		t.Fatal("record-clear failure never triggered; the journey seed is not deterministic")
	}
	t.Logf("child %s closed Completed; disposable worktree removed, residual record path retained (recorded warning)", childID)

	// ------------------------------------------------------------------
	// Step 2: REST post-close cross-checks — the parent's child history
	// carries the Completed outcome, the preserved diff, and the cleanup
	// warning; the disposable worktree directory is gone from disk while
	// the failed record update leaves its path recorded on the child.
	// ------------------------------------------------------------------
	childBody := journeyFeatureBody(srv.URL, childID)
	if childBody == nil || childBody["close_outcome"] != feature.ChildCloseOutcomeCompleted {
		t.Fatalf("child close_outcome = %v, want %s", childBody, feature.ChildCloseOutcomeCompleted)
	}
	parentBody := journeyFeatureBody(srv.URL, parent.ID)
	history, ok := parentBody["child_history"].([]any)
	if !ok || len(history) != 1 {
		t.Fatalf("parent child_history = %v, want one entry", parentBody["child_history"])
	}
	entry, _ := history[0].(map[string]any)
	if entry["id"] != childID {
		t.Fatalf("child_history entry id = %v, want %s", entry["id"], childID)
	}
	if entry["relationship_state"] != feature.ChildCloseOutcomeCompleted {
		t.Fatalf("child_history relationship_state = %v, want completed", entry["relationship_state"])
	}
	const diffMarker = "+child work"
	diffSummary, _ := entry["diff_summary"].(string)
	if !strings.Contains(diffSummary, "child-output.txt") || !strings.Contains(diffSummary, diffMarker) {
		t.Fatalf("child_history diff_summary missing the child's changed file:\n%s", diffSummary)
	}
	warningRows, _ := entry["cleanup_warnings"].([]any)
	if len(warningRows) != 1 {
		t.Fatalf("child_history cleanup_warnings = %v, want exactly one recorded warning", entry["cleanup_warnings"])
	}
	warningRow, _ := warningRows[0].(map[string]any)
	warningMessage, _ := warningRow["message"].(string)
	if warningRow["repo"] != "repoA" || !strings.Contains(warningMessage, "simulated worktree record clear failure") {
		t.Fatalf("cleanup warning = %+v, want repoA with the simulated record-clear failure", warningRow)
	}
	closedChild, err := fx.Store.Load(childID)
	if err != nil {
		t.Fatalf("Load %s: %v", childID, err)
	}
	worktreePath := closedChild.Repos[0].WorktreePath
	if worktreePath == "" {
		t.Fatal("failed record update must retain the recorded child worktree path")
	}
	if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
		t.Fatalf("disposable child worktree must be removed from disk after cleanup; stat = %v", err)
	}
	t.Logf("REST cross-checks verified: Completed outcome, preserved diff, cleanup warning, disposable worktree removed from disk")

	// The scripted implement kernel does not write agent session logs, so seed
	// the retained run logs a real pipeline produces; inspection must cycle
	// them from immutable history.
	logsDir := filepath.Join(fx.Store.RunDir(childID, 1), "logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		t.Fatalf("mkdir run logs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(logsDir, "session.log"), []byte("session log retained for inspection\n"), 0o644); err != nil {
		t.Fatalf("write session log: %v", err)
	}
	if err := os.WriteFile(filepath.Join(logsDir, "phase.log"), []byte("phase log retained for inspection\n"), 0o644); err != nil {
		t.Fatalf("write phase log: %v", err)
	}

	// ------------------------------------------------------------------
	// Step 3: Expand the collapsed history group through the keyboard and
	// select the closed child: the read-only banner, the inspection-only
	// footer, the repository context, and the Cleanup warnings section.
	// ------------------------------------------------------------------
	h.Refresh()
	assertViewContains(t, h.View(), "Refactor History (1)")
	journeyExpandHistoryGroup(t, h, parent.ID, "gamma-rework")
	for i := 0; i < 4 && h.SelectedFeatureID() != childID; i++ {
		h.Press(tea.KeyDown)
	}
	if got := h.SelectedFeatureID(); got != childID {
		t.Fatalf("could not select the closed child row; selected = %q", got)
	}
	h.Refresh() // pull the closed child's detail and run-content snapshots

	h.Press(tea.KeyTab) // focus the detail panel so the inspection footer renders
	closedDetail := ansi.Strip(h.View())
	assertViewContains(t, closedDetail,
		"Read only", "Closed — Completed", "immutable refactor history, inspection only.",
		"Cleanup warnings:", "repoA:",
		"Repo Status", "repoA",
	)
	closedFooter := journeyHintLine(t, closedDetail, "[l] Logs")
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
	// surface — the Rewind & Upgrade lightbulb panel stays suppressed.
	for _, forbidden := range []string{"Rewind & Upgrade", "Press ctrl+r", "ctrl+r", "Workspace Config"} {
		if strings.Contains(closedDetail, forbidden) {
			t.Fatalf("closed child detail surface shows mutation affordance %q in:\n%s", forbidden, closedDetail)
		}
	}
	journeyCapture(t, screenshotsDir, "closed-child-repository-context-and-cleanup-warnings-1200x800.png", h.View())
	t.Logf("closed child renders read-only banner, inspection-only footer, repo context, and the cleanup warning (NAMED CAPTURE 1)")

	// ------------------------------------------------------------------
	// Step 4: Logs — the content panel opens on the first log and 'l'
	// cycles through the retained selectable logs.
	// ------------------------------------------------------------------
	seenLogs := map[string]bool{}
	for i := 0; i < 3; i++ {
		h.PressKey(tea.KeyPressMsg{Code: 'l', Text: "l"})
		if id := journeyContentLogID(ansi.Strip(h.View())); id != "" {
			seenLogs[id] = true
		}
	}
	if len(seenLogs) < 2 {
		t.Fatalf("log cycling never moved past one log; seen = %v:\n%s", seenLogs, ansi.Strip(h.View()))
	}
	t.Logf("logs cycled through the retained selectable logs: %v", seenLogs)

	// ------------------------------------------------------------------
	// Step 5: Artifacts — ']' cycles the retained run artifacts; the
	// selected artifact name must change.
	// ------------------------------------------------------------------
	seenArtifacts := map[string]bool{}
	for i := 0; i < 2; i++ {
		h.PressKey(tea.KeyPressMsg{Code: ']', Text: "]"})
		if id := journeyContentArtifactID(ansi.Strip(h.View())); id != "" {
			seenArtifacts[id] = true
		}
	}
	if len(seenArtifacts) < 2 {
		t.Fatalf("artifact cycling never changed the selected artifact; seen = %v, status = %q:\n%s",
			seenArtifacts, h.StatusMessage(), ansi.Strip(h.View()))
	}
	t.Logf("artifacts cycled through the retained run artifacts: %v", seenArtifacts)

	// ------------------------------------------------------------------
	// Step 6: Preserved diff — 'v' opens the read-only "Preserved diff"
	// viewport captured at close time, with the child's changed file.
	// ------------------------------------------------------------------
	h.Press(tea.KeyEscape) // close the content panel first
	h.PressKey(tea.KeyPressMsg{Code: 'v', Text: "v"})
	diffView := ansi.Strip(h.View())
	assertViewContains(t, diffView, "Preserved diff", diffMarker)
	journeyCapture(t, screenshotsDir, "closed-child-preserved-diff-inspection-after-cleanup-1200x800.png", h.View())
	h.Press(tea.KeyEscape) // back to the dashboard
	if strings.Contains(ansi.Strip(h.View()), "Preserved diff") {
		t.Fatal("escape did not close the preserved diff viewport")
	}
	t.Logf("preserved read-only diff opened with the close-time snapshot and closed with escape")

	t.Logf("journey complete: inspection after successful disposable-worktree cleanup — logs, retained artifacts, repository context + cleanup warnings, preserved read-only diff")
}

// journeyContentLogID extracts the id from the content panel's
// "  Log <id>  ..." header line, or "" when the panel is closed.
func journeyContentLogID(view string) string {
	return journeyContentHeaderID(view, "Log ")
}

// journeyContentArtifactID extracts the id from the content panel's
// "  Artifact <id>  ..." header line, or "" when none is selected.
func journeyContentArtifactID(view string) string {
	return journeyContentHeaderID(view, "Artifact ")
}

// journeyContentHeaderID finds the first content-header row with the given
// marker, skipping the panel's box border.
func journeyContentHeaderID(view, marker string) string {
	for _, line := range strings.Split(view, "\n") {
		rest := strings.TrimPrefix(strings.TrimSpace(line), "│")
		rest = strings.TrimSpace(rest)
		if rest == marker[:len(marker)-1] {
			continue
		}
		if strings.HasPrefix(rest, marker) {
			fields := strings.Fields(rest)
			if len(fields) >= 2 {
				return fields[1]
			}
		}
	}
	return ""
}
