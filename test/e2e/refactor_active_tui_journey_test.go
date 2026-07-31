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
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/tui/tuitest"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// TestRefactorActiveTUIJourney records the full active TUI refactor journey
// driven through the real TUI model (tui.APIAppModel): every action goes
// through bubbletea key presses; only state inspection reads the REST API.
// Steps: cold boot with refactor-eligible parent → known-dirty Shift+F entry
// presents the focused dirty-worktree remediation immediately (no wizard
// bypass) → external clean + entry retry opens the wizard → a submission-time
// cleanliness race surfaces the remediation overlay with every entered value
// preserved → external clean + overlay retry → child auto-selected nested
// under the "Refactoring" parent → explicit start → roadmap review gate
// through the review overlay → paired Review config edit from the child →
// current-phase restart, stop, resume held by a lock file → discard →
// cold-start re-projection.
func TestRefactorActiveTUIJourney(t *testing.T) {
	if testing.Short() {
		t.Skip("journey boots real-git setup and scripted provider subprocesses")
	}

	// Two parent repositories so the dirty-worktree remediation panel lists
	// both entries with their staged/unstaged/untracked tallies. The parent
	// deliberately carries non-default Review axes (models, effort, inquiry
	// behavior, risk, exit criteria, gates) so the journey can prove the
	// refactor wizard seeds Review from the parent and keeps those values
	// intact when the child pipeline cursor moves.
	parentModels := config.ModelConfig{
		Inquiry:        "opus[200K]",
		Research:       "opus[200K]",
		Planning:       "opus[200K]",
		Implementation: "sonnet[272K]",
		Review:         "opus[200K]",
		Utilities:      "opus[200K]",
		KBBuild:        "opus[200K]",
	}
	parentEffort := config.EffortConfig{
		Inquiry: "low", Research: "medium", Planning: "xhigh",
		Implementation: "auto", Review: "medium", Utilities: "low", KBBuild: "low",
	}
	parentCheckpoints := feature.Checkpoints{
		InquiryReview:   true,
		ResearchReview:  false,
		DesignReview:    true,
		RoadmapReview:   true,
		PhasePlanReview: true,
		ManualPublish:   false,
	}
	fx := newTUIJourneyFixture(t, tuiJourneyFixtureOptions{
		ParentID:           "tui-journey-parent",
		ParentName:         "TUI Journey Parent",
		RepoNames:          []string{"repoA", "repoB"},
		ParentSelfWorktree: true,
		Models:             parentModels,
		Effort:             parentEffort,
		Checkpoints:        parentCheckpoints,
		Inquireness:        feature.InquirenessNone,
		RiskLevel:          feature.RiskHigh,
		ExitCriteria:       "refresh the journey without losing parent context",
	})
	parent := fx.Parent
	srv := fx.Server
	repoDirs := fx.RepoDirs
	// The fixture's phase-plan lock file gives the restart/stop/resume
	// segment a deterministic running-phase window instead of racing the
	// instant scripted completion.
	blockFile := fx.BlockFile
	stateDir := fx.StateDir

	screenshotsDir := journeyScreenshotDir(t)
	childName := "TUI Journey Refactor"

	// ------------------------------------------------------------------
	// Step 1: Cold boot — the parent must be selected and offer the
	// Start Refactor entry point in the dashboard.
	// ------------------------------------------------------------------
	h := fx.NewHarness(t)
	if got := h.SelectedFeatureID(); got != parent.ID {
		t.Fatalf("cold boot selected feature = %q, want %q", got, parent.ID)
	}
	assertViewContains(t, h.View(),
		"[Shift+F] Refactor", "tui-journey-parent",
	)
	t.Logf("cold boot: parent selected, refactor entry point visible")

	// ------------------------------------------------------------------
	// Step 2a: Dirty parent worktrees — Shift+F takes the identical guarded
	// action path and, because the server flags the Refactor action disabled
	// with the dirty_parent reason, presents the focused structured
	// remediation immediately instead of bypassing the guard into a wizard.
	// ------------------------------------------------------------------
	for _, dir := range repoDirs {
		writeJourneyFile(t, dir, "uncommitted.txt", "dirty\n")
	}
	h.Refresh()
	h.PressKey(tea.KeyPressMsg{Code: 'F', Text: "F"})
	if h.WizardActive() {
		t.Fatal("Shift+F bypassed the dirty_parent disabled action into the wizard")
	}
	if !h.RemediationVisible() {
		t.Fatalf("known-dirty Shift+F entry did not open the remediation overlay; status = %q", h.StatusMessage())
	}
	entryView := ansi.Strip(h.View())
	assertViewContains(t, entryView,
		"Refactor Launch Blocked", "repoA", "repoB",
		"Path:", "Staged:", "Unstaged:", "Untracked:",
	)
	if strings.Contains(entryView, "preserved") {
		t.Fatalf("entry-time remediation must not claim preserved wizard values (none were entered):\n%s", entryView)
	}
	assertRemediationRepos(t, h, repoDirs, 1)
	t.Logf("known-dirty entry presented the focused remediation (no wizard bypass)")

	// ------------------------------------------------------------------
	// Step 2b: Clean both worktrees externally and retry from the panel —
	// the entry guard is re-evaluated against the refreshed parent detail
	// and the wizard opens.
	// ------------------------------------------------------------------
	for _, dir := range repoDirs {
		journeyGit(t, dir, "add", "uncommitted.txt")
		journeyGit(t, dir, "commit", "-m", "clean dirty change")
	}
	h.PressKey(tea.KeyPressMsg{Code: 'r', Text: "r"})
	if h.RemediationVisible() {
		t.Fatalf("remediation overlay stayed open after entry retry key; status = %q", h.StatusMessage())
	}
	if !h.WizardActive() {
		t.Fatalf("entry retry did not open the refactor wizard after external cleanup; status = %q", h.StatusMessage())
	}
	assertViewContains(t, h.View(), "Start Refactor")
	t.Logf("entry retry re-evaluated the guard: clean parent opened the wizard")

	// ------------------------------------------------------------------
	// Step 2c: Wizard values entered, then a submission-time cleanliness
	// race (both worktrees dirtied again after entry) surfaces the same
	// remediation overlay on submission — this time with every entered
	// wizard value preserved.
	// ------------------------------------------------------------------
	h.Type(childName)
	h.Press(tea.KeyEnter) // name → description focus
	h.Type("Rework the TUI journey")
	h.Press(tea.KeyEnter) // advance: What → Pipeline (Where skipped in refactor mode)
	h.Press(tea.KeyUp)    // pipeline options [medium, large, moonshot]; default cursor large → medium
	h.Press(tea.KeyEnter) // advance: Pipeline → Review
	for _, dir := range repoDirs {
		writeJourneyFile(t, dir, "race.txt", "dirty again\n")
	}
	h.PressKey(tea.KeyPressMsg{Code: 'G', Text: "G"})

	if !h.RemediationVisible() {
		t.Fatalf("dirty submission did not open the remediation overlay; status = %q", h.StatusMessage())
	}
	if h.WizardActive() {
		t.Fatal("wizard stayed open after blocked submission")
	}
	remediationView := ansi.Strip(h.View())
	// The panel wraps at 72 columns, so long worktree paths render across
	// multiple lines; assert the diagnostics structurally instead of
	// matching wrapped text.
	assertViewContains(t, remediationView,
		"Refactor Launch Blocked", "repoA", "repoB",
		"Path:", "Staged:", "Unstaged:", "Untracked:", "preserved",
	)
	assertRemediationRepos(t, h, repoDirs, 1)
	journeyCapture(t, screenshotsDir, "refactor-wizard-dirty-worktree-remediation-preserving-entered-values-and-listing-1200x800.png", h.View())
	t.Logf("captured dirty-worktree remediation overlay (NAMED CAPTURE 2)")

	// Clean both parent worktrees externally, then retry the identical
	// launch from the remediation panel with its preserved wizard values.
	for _, dir := range repoDirs {
		journeyGit(t, dir, "add", "race.txt")
		journeyGit(t, dir, "commit", "-m", "clean race change")
	}

	// ------------------------------------------------------------------
	// Step 3: Retry through the remediation overlay — the launch succeeds,
	// the model auto-selects the new child nested under the parent, and the
	// parent renders as "Refactoring".
	// ------------------------------------------------------------------
	h.PressKey(tea.KeyPressMsg{Code: 'r', Text: "r"})
	if h.RemediationVisible() {
		t.Fatal("remediation overlay stayed open after retry key")
	}
	childID := h.SelectedFeatureID()
	if childID == "" || childID == parent.ID {
		t.Fatalf("retry did not auto-select the child; selected = %q, status = %q", childID, h.StatusMessage())
	}
	childBody := waitForJourneySetupComplete(t, srv.URL, childID)
	if childBody["status"] != "Created" {
		t.Fatalf("child status = %v, want Created after setup", childBody["status"])
	}
	assertActiveChildState(t, srv.URL, parent.ID, childID, "active", "Created")

	// The journey moved the pipeline cursor (large → medium) inside the
	// wizard; every Review axis seeded from the parent must have survived
	// that independent child pipeline choice on both records.
	launchParentCfg := getJourneyConfig(t, srv.URL, parent.ID)
	launchChildCfg := getJourneyConfig(t, srv.URL, childID)
	for label, current := range map[string]map[string]any{"parent": launchParentCfg, "child": launchChildCfg} {
		models, _ := current["models"].(map[string]any)
		for phase, want := range map[string]string{"planning": parentModels.Planning, "implementation": parentModels.Implementation} {
			if models[phase] != want {
				t.Fatalf("%s models.%s = %v, want parent-seeded %s (pipeline cursor must not overwrite seeded Review values)", label, phase, models[phase], want)
			}
		}
		effort, _ := current["effort"].(map[string]any)
		if effort["planning"] != parentEffort.Planning || effort["implementation"] != parentEffort.Implementation {
			t.Fatalf("%s effort = %v, want parent-seeded planning=%s implementation=%s", label, effort, parentEffort.Planning, parentEffort.Implementation)
		}
		if current["inquireness"] != string(feature.InquirenessNone) {
			t.Fatalf("%s inquireness = %v, want parent-seeded %s", label, current["inquireness"], feature.InquirenessNone)
		}
	}
	if got := launchChildCfg["pipeline"]; got != "medium" {
		t.Fatalf("child pipeline = %v, want medium (the independent child choice made in the wizard)", got)
	}
	launchParentBody := journeyFeatureBody(srv.URL, parent.ID)
	launchChildBody := journeyFeatureBody(srv.URL, childID)
	for label, body := range map[string]map[string]any{"parent": launchParentBody, "child": launchChildBody} {
		if body["risk_level"] != string(feature.RiskHigh) {
			t.Fatalf("%s risk_level = %v, want parent-seeded %s", label, body["risk_level"], feature.RiskHigh)
		}
		if body["exit_criteria"] != parent.ExitCriteria {
			t.Fatalf("%s exit_criteria = %v, want parent-seeded %q", label, body["exit_criteria"], parent.ExitCriteria)
		}
	}
	t.Logf("wizard Review seeding verified: pipeline medium (child choice), Review axes inherited from the parent on both records")

	h.Refresh()
	assertViewContains(t, h.View(),
		"Refactoring", "tui-journey-refac", "↳",
	)
	dashboardView := ansi.Strip(h.View())
	if strings.Contains(dashboardView, "Needs attention") {
		t.Fatalf("dashboard already shows attention before the child started:\n%s", h.View())
	}
	// The backend proves the child is Created (setup done, execution not
	// dispatched); the presentation must not imply a startup in progress and
	// must expose the server-authorized ordinary Start.
	if strings.Contains(dashboardView, "Starting") {
		t.Fatalf("setup-complete child presented as already starting:\n%s", h.View())
	}
	assertViewContains(t, dashboardView, "Ready to start")
	journeyCapture(t, screenshotsDir, "dashboard-after-successful-launch-with-the-new-active-child-selected-and-visibly-1200x800.png", h.View())
	t.Logf("captured dashboard with new active child selected (NAMED CAPTURE 1); childID = %s", childID)

	// ------------------------------------------------------------------
	// Step 4: Explicit start through the TUI's contextual action key, then
	// the roadmap review gate answered through the review overlay.
	// ------------------------------------------------------------------
	h.PressKey(tea.KeyPressMsg{Code: 'a', Text: "a"})
	waitForJourneyGate(t, srv.URL, childID, 0)
	t.Logf("child started, reached roadmap review gate")

	// ------------------------------------------------------------------
	// Step 9 (mid-journey cold start): a second harness instance must
	// project the nested active child eagerly and allow selecting it.
	// ------------------------------------------------------------------
	h2 := fx.NewHarness(t)
	assertViewContains(t, h2.View(), "Refactoring", "tui-journey-refac", "↳")
	selectedCold := false
	for i := 0; i < 6 && !selectedCold; i++ {
		h2.Press(tea.KeyDown)
		if h2.SelectedFeatureID() == childID {
			selectedCold = true
		}
	}
	if !selectedCold {
		t.Fatalf("cold-start harness could not select nested child row; selected = %q", h2.SelectedFeatureID())
	}
	t.Logf("cold start: nested child row visible and selectable on a fresh model")

	h.Refresh()
	journeyReviewProceed(t, h)
	waitForJourneyGate(t, srv.URL, childID, 1)
	assertActiveChildState(t, srv.URL, parent.ID, childID, "active", feature.StatusPlanNeedsReview.String())
	t.Logf("roadmap gate proceeded via review overlay, reached phase-plan review gate")

	// ------------------------------------------------------------------
	// Step 6: Paired Review configuration — `e` on the child opens the
	// paired editor naming both records with the pipeline preserved; the
	// Inquireness axis edit propagates to both records.
	// ------------------------------------------------------------------
	h.Refresh()
	if got := h.SelectedFeatureID(); got != childID {
		t.Fatalf("selected feature = %q before config edit, want child %q", got, childID)
	}
	h.PressKey(tea.KeyPressMsg{Code: 'e', Text: "e"})
	if !h.EditorOpen() {
		t.Fatalf("config editor did not open; status = %q", h.StatusMessage())
	}
	editorView := ansi.Strip(h.View())
	assertViewContains(t, editorView,
		"Paired Review", parent.Name, childName,
		"Pipeline is preserved", "Models", "Behavior", "Gates",
	)
	// The tab strip is Models/Behavior/Gates only — a paired editor has no
	// pipeline selector of its own.
	if strings.Contains(editorView, "▸ Pipeline") || strings.Contains(editorView, "Pipeline: [") {
		t.Fatalf("paired editor unexpectedly renders a pipeline selector:\n%s", editorView)
	}
	journeyCapture(t, screenshotsDir, "paired-review-editor-opened-from-the-child-naming-both-records-and-showing-model-1200x800.png", h.View())
	t.Logf("captured paired review editor (NAMED CAPTURE 4)")

	const wantInquireness = "high"
	h.Press(tea.KeyRight) // tabs: Models → Behavior
	h.PressKey(tea.KeyPressMsg{Code: 'j', Text: "j"})
	setInquireness := false
	for i := 0; i < 3; i++ {
		if strings.Contains(ansi.Strip(h.View()), "▸ "+wantInquireness) {
			setInquireness = true
			break
		}
		h.Press(tea.KeyRight)
	}
	if !setInquireness {
		t.Fatalf("editor inquireness never reached %q:\n%s", wantInquireness, ansi.Strip(h.View()))
	}
	h.Press(tea.KeyEnter) // body → tabs
	h.Press(tea.KeyEnter) // save
	if h.EditorOpen() {
		t.Fatalf("editor stayed open after save; status = %q, save banner:\n%s", h.StatusMessage(), ansi.Strip(h.View()))
	}

	parentCfg := getJourneyConfig(t, srv.URL, parent.ID)
	childCfg := getJourneyConfig(t, srv.URL, childID)
	if parentCfg["inquireness"] != wantInquireness {
		t.Fatalf("parent inquireness = %v, want %s (paired propagation)", parentCfg["inquireness"], wantInquireness)
	}
	if childCfg["inquireness"] != wantInquireness {
		t.Fatalf("child inquireness = %v, want %s", childCfg["inquireness"], wantInquireness)
	}
	t.Logf("paired config edit verified: both parent and child have inquireness=%s", wantInquireness)

	// ------------------------------------------------------------------
	// Step 7: Restart/stop/resume through TUI keys with the phase-plan
	// session held by the lock file, then land back at the gate — an
	// attention state that flags the parent as "Refactoring — Needs
	// attention" while the child detail shows recovery controls.
	// ------------------------------------------------------------------
	h.Refresh()
	if err := os.WriteFile(blockFile, []byte("block\n"), 0o644); err != nil {
		t.Fatalf("write block file: %v", err)
	}
	// Restarting a gate whose plan run already completed replays the
	// persisted approved attempt (LatestCompletedPlanAttempt) and re-parks
	// without building a new session — no running window exists to hold,
	// stop, or resume. Clear the phase-plan attempt artifacts so the
	// restart executes a fresh planning session the lock file can hold.
	if err := os.RemoveAll(filepath.Join(stateDir, childID, "runs", "run-001", "phase-01", "plan")); err != nil {
		t.Fatalf("clear phase-plan attempts: %v", err)
	}
	h.PressKey(tea.KeyPressMsg{Code: 'r', Text: "r"})
	h.PressKey(tea.KeyPressMsg{Code: 'y', Text: "y"})
	waitForJourneyStatus(t, srv.URL, childID, feature.StatusPlanning.String())
	// The Planning status can flicker through a synchronous transition; the
	// blocked window only counts once a phase-plan session is durably
	// running under the lock file.
	time.Sleep(time.Second)
	if st := journeyFeatureBody(srv.URL, childID); st["status"] != feature.StatusPlanning.String() {
		t.Fatalf("child status after restart = %v, want stable %s", st["status"], feature.StatusPlanning)
	}
	assertJourneySessionStatus(t, srv.URL, childID, "Running")
	t.Logf("restart dispatched and phase is running (blocked)")

	h.Refresh()
	h.PressKey(tea.KeyPressMsg{Code: 's', Text: "s"})
	h.PressKey(tea.KeyPressMsg{Code: 'y', Text: "y"})
	waitForJourneyStatus(t, srv.URL, childID, feature.StatusInterrupted.String())
	t.Logf("phase stopped, child interrupted")

	// The status transition lands in the read model before the stop mutation
	// finishes killing the subprocess. Resuming on status alone would race a
	// still-running session: two planning loops would complete concurrently
	// and the duplicate gate marking would fail the child. A real user waits
	// for the stop response; here we wait for the equivalent quiescence.
	waitForJourneyFeatureSessionsQuiescent(t, srv.URL, childID)
	if err := os.Remove(blockFile); err != nil {
		t.Fatalf("remove block file: %v", err)
	}

	h.Refresh()
	h.PressKey(tea.KeyPressMsg{Code: 'a', Text: "a"}) // contextual resume
	waitForJourneyGate(t, srv.URL, childID, 1)
	t.Logf("child resumed and re-parked at the phase-plan review gate")

	h.Refresh()
	h.Press(tea.KeyTab) // focus the detail panel so recovery controls render in the footer
	attentionView := ansi.Strip(h.View())
	assertViewContains(t, attentionView,
		"Refactoring — Needs attention", "tui-journey-refactor",
		"[a] Review", "[r] Restart",
	)
	journeyCapture(t, screenshotsDir, "active-child-detail-in-an-attention-state-with-the-parent-displaying-refactoring-1200x800.png", h.View())
	t.Logf("captured attention-state child detail (NAMED CAPTURE 3)")

	// ------------------------------------------------------------------
	// Step 8: Discard the active child through TUI keys (discard action,
	// then confirm). The discard state machine is resumable, so retry the
	// key sequence while session drain races settle.
	// ------------------------------------------------------------------
	journeyDiscardChildViaKeys(t, h, srv.URL, parent.ID)
	t.Logf("child discarded via TUI keys")

	parentBody := journeyFeatureBody(srv.URL, parent.ID)
	if parentBody["active_child"] != nil {
		t.Fatalf("parent active_child = %v, want nil after discard", parentBody["active_child"])
	}

	// After a child is discarded, the closed-history surface drops it from
	// the ACTIVE projection entirely (selection falls back to the parent, no
	// active child row, no top-level row, no Refactoring parent label); it
	// lives only inside the collapsed Refactor History group.
	h.Refresh()
	if got := h.SelectedFeatureID(); got != parent.ID {
		t.Fatalf("after discard selected feature = %q, want parent fallback %q", got, parent.ID)
	}
	postDiscardView := ansi.Strip(h.View())
	if !strings.Contains(postDiscardView, "Refactor History (1)") {
		t.Fatalf("discarded child not nested in the collapsed history group:\n%s", postDiscardView)
	}
	if strings.Contains(postDiscardView, "tui-journey-refactor") {
		t.Fatalf("dashboard still lists the discarded child outside history:\n%s", postDiscardView)
	}
	if strings.Contains(postDiscardView, "Refactoring") {
		t.Fatalf("parent still displays Refactoring after the child closed:\n%s", postDiscardView)
	}
	t.Logf("post-discard TUI projection verified: active child row gone, parent selected, Refactoring state cleared, history group collapsed")

	list := getJourneyJSON(t, srv.URL+"/api/v1/features")
	summaries := list["features"].([]any)
	if len(summaries) != 1 || summaries[0].(map[string]any)["id"] != parent.ID {
		t.Fatalf("top-level list = %+v, want only the parent after discard", summaries)
	}
	t.Logf("journey complete: dirty remediation → setup → start → progress → paired config → restart/resume → discard")
}

// journeyScreenshotDir resolves the screenshot output root. Visual captures
// are an explicit opt-in: REFACTOR_SCREENSHOT_DIR names a durable output
// root (used when producing committed evidence); without it the journey is a
// pure behavioral test and its captures go to a throwaway temp directory, so
// an ordinary E2E run never writes into the worktree.
func journeyScreenshotDir(t *testing.T) string {
	t.Helper()
	outDir := os.Getenv("REFACTOR_SCREENSHOT_DIR")
	if outDir == "" {
		outDir = t.TempDir()
	}
	screenshotsDir := filepath.Join(outDir, "screenshots")
	if err := os.MkdirAll(screenshotsDir, 0o755); err != nil {
		t.Fatalf("mkdir screenshots: %v", err)
	}
	return screenshotsDir
}

// journeyCapture renders one named 1200x800 capture via the shared headless
// renderer. Capture is optional for the behavioral journey: when no renderer
// is available and no explicit output directory was requested, the capture
// is skipped with a log line; an opted-in evidence run fails loudly instead.
func journeyCapture(t *testing.T, screenshotsDir, name, view string) {
	t.Helper()
	if _, err := testutil.RendererPath(); err != nil && os.Getenv("REFACTOR_SCREENSHOT_DIR") == "" {
		t.Logf("capture %s skipped: %v", name, err)
		return
	}
	path := filepath.Join(screenshotsDir, name)
	if err := testutil.RenderTerminalPNG(view, path, 1200, 800); err != nil {
		t.Fatalf("capture %s: %v", name, err)
	}
	// Visual regression assertion: the AGENTICO wordmark, both panel
	// borders, the status line, and the global Ask/Help cluster must render
	// fully inside the 1200x800 bitmap for every named state — ink touching
	// the viewport edge is exactly what a clipped capture looks like.
	if err := testutil.AssertCaptureUncropped(path); err != nil {
		t.Fatalf("capture %s: %v", name, err)
	}
	t.Logf("wrote %s", path)
}

// assertRemediationRepos fails unless the open remediation overlay carries
// exactly the dirty repositories in repoDirs, each with its authoritative
// path and the expected untracked tally.
func assertRemediationRepos(t *testing.T, h *tuitest.AppHarness, repoDirs map[string]string, wantUntracked int) {
	t.Helper()
	dirtyRepos := h.RemediationRepos()
	if len(dirtyRepos) != len(repoDirs) {
		t.Fatalf("remediation repos = %+v, want one entry per dirty repository", dirtyRepos)
	}
	for _, repo := range dirtyRepos {
		if repo.Path != repoDirs[repo.Name] {
			t.Fatalf("remediation repo %s path = %q, want %q", repo.Name, repo.Path, repoDirs[repo.Name])
		}
		if repo.Untracked != wantUntracked {
			t.Fatalf("remediation repo %s untracked = %d, want %d", repo.Name, repo.Untracked, wantUntracked)
		}
	}
}

// assertViewContains fails unless the ANSI-stripped view carries every
// marker.
func assertViewContains(t *testing.T, view string, markers ...string) {
	t.Helper()
	plain := ansi.Strip(view)
	for _, marker := range markers {
		if !strings.Contains(plain, marker) {
			t.Fatalf("view missing %q:\n%s", marker, plain)
		}
	}
}

// assertJourneySessionStatus fails unless the feature has at least one
// session row in the wanted status.
func assertJourneySessionStatus(t *testing.T, baseURL, featureID, wantStatus string) {
	t.Helper()
	sessions, err := getJourneyJSONQuiet(baseURL + "/api/v1/sessions")
	if err != nil {
		t.Fatalf("GET sessions: %v", err)
	}
	for _, row := range sessions["sessions"].([]any) {
		sess, _ := row.(map[string]any)
		if sess["feature_id"] == featureID && sess["status"] == wantStatus {
			return
		}
	}
	t.Fatalf("no session for feature %s in status %q: %v", featureID, wantStatus, sessions["sessions"])
}

// assertActiveChildState verifies the ordered parent↔child relationship
// projection through the REST read model.
func assertActiveChildState(t *testing.T, baseURL, parentID, wantChildID, wantState, wantStatus string) {
	t.Helper()
	parentBody := journeyFeatureBody(baseURL, parentID)
	if parentBody == nil {
		t.Fatalf("parent %s detail missing", parentID)
	}
	activeChild, ok := parentBody["active_child"].(map[string]any)
	if !ok || activeChild == nil {
		t.Fatalf("parent active_child = %v, want child %s", parentBody["active_child"], wantChildID)
	}
	if activeChild["id"] != wantChildID {
		t.Fatalf("parent active_child id = %v, want %s", activeChild["id"], wantChildID)
	}
	if activeChild["relationship_state"] != wantState {
		t.Fatalf("parent active_child relationship_state = %v, want %s", activeChild["relationship_state"], wantState)
	}
	if activeChild["status"] != wantStatus {
		t.Fatalf("parent active_child status = %v, want %s", activeChild["status"], wantStatus)
	}
	childBody := journeyFeatureBody(baseURL, wantChildID)
	if childBody == nil || childBody["status"] != wantStatus {
		t.Fatalf("child %s status = %v, want %s", wantChildID, childBody["status"], wantStatus)
	}
}

// journeyReviewProceed answers the currently open review gate with
// "proceed" through the review overlay: open it with the contextual key,
// reveal the decision menu, pick the "Proceed ..." entry by name, confirm.
func journeyReviewProceed(t *testing.T, h *tuitest.AppHarness) {
	t.Helper()
	h.PressKey(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if !h.ReviewOpen() {
		t.Fatalf("review overlay did not open; status = %q", h.StatusMessage())
	}
	for i := 0; i < 3 && !h.ReviewMenuOpen(); i++ {
		h.Press(tea.KeyEscape)
	}
	if !h.ReviewMenuOpen() {
		t.Fatal("review decision menu did not open after esc")
	}
	labels := h.ReviewMenuLabels()
	proceedIdx := -1
	for i, label := range labels {
		if strings.HasPrefix(label, "Proceed") {
			proceedIdx = i
			break
		}
	}
	if proceedIdx < 0 {
		t.Fatalf("review menu labels = %v, want a Proceed entry", labels)
	}
	for i := 0; i < proceedIdx; i++ {
		h.PressKey(tea.KeyPressMsg{Code: 'j', Text: "j"})
	}
	h.Press(tea.KeyEnter)
	if h.ReviewOpen() {
		t.Fatalf("review overlay stayed open after proceed decision; status = %q", h.StatusMessage())
	}
}

// journeyDiscardChildViaKeys discards the active child through the TUI's
// discard action and its confirm dialog, retrying while the discard state
// machine reports transient session-drain conflicts.
func journeyDiscardChildViaKeys(t *testing.T, h *tuitest.AppHarness, baseURL, parentID string) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		parentBody := journeyFeatureBody(baseURL, parentID)
		if parentBody != nil && parentBody["active_child"] == nil {
			return
		}
		h.PressKey(tea.KeyPressMsg{Code: 'd', Text: "d"})
		h.PressKey(tea.KeyPressMsg{Code: 'y', Text: "y"})
		h.Refresh()
		time.Sleep(200 * time.Millisecond)
	}
	parentBody := journeyFeatureBody(baseURL, parentID)
	t.Fatalf("child never discarded via TUI keys; parent active_child = %v, status = %q",
		parentBody["active_child"], h.StatusMessage())
}
