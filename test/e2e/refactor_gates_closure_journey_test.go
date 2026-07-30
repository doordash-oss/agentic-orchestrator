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
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/server"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/internal/tui"
	"github.com/doordash-oss/agentic-orchestrator/internal/tui/tuitest"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// TestRefactorLaunchPreservesParentReviewGatesWithMediumChild records a
// Refactor launch where a Large parent carries non-default Inquiry,
// Research, and Design review gates and the wizard selects Medium as the
// independent child pipeline. The submitted paired Review configuration
// must persist verbatim on BOTH records: Medium hides those gates from its
// own profile, but the parent's seeded gates are part of the paired Review
// configuration and must not be normalized through the child pipeline.
func TestRefactorLaunchPreservesParentReviewGatesWithMediumChild(t *testing.T) {
	if testing.Short() {
		t.Skip("journey boots real-git setup and scripted provider subprocesses")
	}

	tmp := t.TempDir()
	stateDir := filepath.Join(tmp, "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}
	wtBaseDir := filepath.Join(tmp, "worktrees")

	repoDir := testutil.InitGitRepo(t)
	testutil.InitBareRemote(t, repoDir)
	journeyGit(t, repoDir, "checkout", "-b", "feature/gates-parent")
	writeJourneyFile(t, repoDir, "base.txt", "v1\n")
	journeyGit(t, repoDir, "add", "base.txt")
	journeyGit(t, repoDir, "commit", "-m", "repoA base commit")
	journeyGit(t, repoDir, "push", "-u", "origin", "feature/gates-parent")

	store := feature.NewStore(stateDir)
	publishable := true
	// Every gate deviates from any pipeline auto-configuration in at least
	// one direction; Inquiry/Design true are exactly the values Medium's
	// gate projection would clear if the launch normalized the paired
	// Review gates through the child's independent pipeline.
	parentCheckpoints := feature.Checkpoints{
		InquiryReview:   true,
		ResearchReview:  false,
		DesignReview:    true,
		RoadmapReview:   false,
		PhasePlanReview: true,
		ManualPublish:   true,
	}
	parent := &feature.Feature{
		ID:            "gates-parent",
		Name:          "Gates Parent",
		Slug:          "gates-parent",
		Status:        feature.StatusPublished,
		CurrentPhase:  feature.PhasePublish,
		Pipeline:      feature.PipelineLarge,
		Created:       time.Now().UTC().Truncate(time.Second),
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
		Checkpoints:   parentCheckpoints,
		Repos: []feature.FeatureRepo{{
			Name:         "repoA",
			Path:         repoDir,
			WorktreePath: repoDir,
			Branch:       "feature/gates-parent",
			BaseBranch:   "main",
			Publishable:  &publishable,
		}},
		RepoStates: map[string]*feature.RepoState{"repoA": {Touched: true}},
	}
	if err := store.Save(parent); err != nil {
		t.Fatalf("Save(parent) error = %v", err)
	}

	wm := git.NewWorktreeManager(wtBaseDir)
	mgr := feature.NewManager(store, config.NewDefault())
	mgr.Worktrees = wm
	mgr.Cleanliness = journeyCleanliness(wm)

	serverEvents := make(chan interface{}, 512)
	stopForwarding := make(chan struct{})
	defer close(stopForwarding)

	sm := session.NewManager(serverEvents)
	t.Cleanup(sm.Shutdown)
	pr := journeyChildPhaseRunner(t, sm, store, stateDir)

	orch := orchestrator.New(orchestrator.Deps{
		Lifecycle:   mgr,
		Store:       store,
		Sessions:    sm,
		Recovery:    session.NewRecoveryAdapter(stateDir, mgr),
		Publisher:   &git.PublishAdapter{},
		Worktrees:   wm,
		PhaseRunner: pr,
		CmdRunner:   pr.CommandRunner,
		Cleanliness: journeyCleanliness(wm),
	}, orchestrator.Hooks{})
	t.Cleanup(func() {
		_ = orch.Shutdown()
		orch.WaitForCycles()
	})
	go func() {
		for {
			select {
			case ev := <-orch.Events():
				select {
				case serverEvents <- ev:
				default:
				}
			case <-stopForwarding:
				return
			}
		}
	}()

	runtimeCfg := config.NewDefault()
	runtimeCfg.WorkspaceRoots = []string{tmp}

	srv := httptest.NewServer(server.NewHandler(server.HandlerOptions{
		Runtime:               server.RuntimeIdentity{RuntimeDir: tmp, StateDir: stateDir},
		Features:              store,
		FeatureStore:          store,
		Freshness:             journeyFreshnessProvider{},
		Config:                runtimeCfg,
		Sessions:              sm,
		Events:                serverEvents,
		Mutations:             &journeyMutationTarget{mgr: mgr, orch: orch},
		Cleanliness:           journeyCleanliness(wm),
		DisableHostValidation: true,
	}))
	t.Cleanup(srv.Close)
	client, err := server.NewClient(server.ClientOptions{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	h, err := tuitest.NewAppHarness(t.Context(), client, tui.APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAppHarness() error = %v", err)
	}
	defer h.Close()
	h.Resize(140, 42)
	if got := h.SelectedFeatureID(); got != parent.ID {
		t.Fatalf("cold boot selected feature = %q, want %q", got, parent.ID)
	}

	// Launch through the wizard: name → description → Pipeline (Medium) →
	// Review → submit.
	h.PressKey(tea.KeyPressMsg{Code: 'F', Text: "F"})
	if !h.WizardActive() {
		t.Fatalf("Shift+F did not open the refactor wizard; status = %q", h.StatusMessage())
	}
	h.Type("Gates Refactor Child")
	h.Press(tea.KeyEnter) // name → description focus
	h.Type("Rework gates preservation")
	h.Press(tea.KeyEnter) // advance: What → Pipeline
	h.Press(tea.KeyUp)    // pipeline options [medium, large, moonshot]; default cursor large → medium
	h.Press(tea.KeyEnter) // advance: Pipeline → Review
	h.PressKey(tea.KeyPressMsg{Code: 'G', Text: "G"})

	childID := h.SelectedFeatureID()
	if childID == "" || childID == parent.ID {
		t.Fatalf("launch did not auto-select the child; selected = %q, status = %q", childID, h.StatusMessage())
	}
	childBody := waitForJourneySetupComplete(t, srv.URL, childID)
	if childBody["status"] != "Created" {
		t.Fatalf("child status = %v, want Created after setup", childBody["status"])
	}

	// Persisted records are authoritative: BOTH records keep the exact
	// parent-seeded gates, and only the child pipeline differs.
	parentRec, err := store.Load(parent.ID)
	if err != nil {
		t.Fatalf("Load(parent) error = %v", err)
	}
	childRec, err := store.Load(childID)
	if err != nil {
		t.Fatalf("Load(child) error = %v", err)
	}
	if parentRec.Checkpoints != parentCheckpoints {
		t.Fatalf("persisted parent checkpoints = %+v, want seeded %+v", parentRec.Checkpoints, parentCheckpoints)
	}
	if childRec.Checkpoints != parentCheckpoints {
		t.Fatalf("persisted child checkpoints = %+v, want seeded paired %+v (Medium must not clear parent review gates)", childRec.Checkpoints, parentCheckpoints)
	}
	if childRec.Pipeline != feature.PipelineMedium {
		t.Fatalf("persisted child pipeline = %q, want independent medium choice", childRec.Pipeline)
	}
	if parentRec.Pipeline != feature.PipelineLarge {
		t.Fatalf("persisted parent pipeline = %q, want unchanged %q", parentRec.Pipeline, feature.PipelineLarge)
	}

	// REST read model cross-check: the served checkpoints on both records
	// match the seeded gates field by field.
	wantWire := map[string]bool{
		"inquiry_review":    parentCheckpoints.InquiryReview,
		"research_review":   parentCheckpoints.ResearchReview,
		"design_review":     parentCheckpoints.DesignReview,
		"roadmap_review":    parentCheckpoints.RoadmapReview,
		"phase_plan_review": parentCheckpoints.PhasePlanReview,
		"manual_publish":    parentCheckpoints.ManualPublish,
	}
	for label, body := range map[string]map[string]any{
		"parent": journeyFeatureBody(srv.URL, parent.ID),
		"child":  journeyFeatureBody(srv.URL, childID),
	} {
		got, _ := body["checkpoints"].(map[string]any)
		if got == nil {
			t.Fatalf("%s feature detail missing checkpoints: %v", label, body)
		}
		for gate, want := range wantWire {
			if got[gate] != want {
				t.Fatalf("%s checkpoints.%s = %v, want parent-seeded %v", label, gate, got[gate], want)
			}
		}
	}
	t.Logf("launch preserved all parent-seeded gates on both persisted records; only the child pipeline changed to medium")
}

// TestRefactorChildClosureSelectsParentAcrossReorder records an ordered
// relationship refresh that closes the selected active child while another
// top-level feature sorts ahead of its parent. The child row must disappear
// from the active projection and selection must fall back specifically to
// the child's parent — never to whichever feature happens to sort first.
func TestRefactorChildClosureSelectsParentAcrossReorder(t *testing.T) {
	if testing.Short() {
		t.Skip("journey boots the real server and TUI model")
	}

	tmp := t.TempDir()
	stateDir := filepath.Join(tmp, "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}
	wtBaseDir := filepath.Join(tmp, "worktrees")

	store := feature.NewStore(stateDir)
	created := time.Now().UTC().Truncate(time.Second)

	// The unrelated top-level feature sorts ahead of the Published parent
	// (ordinary statuses group before published), so it is exactly the row
	// a naive first-feature fallback would wrongly select.
	other := &feature.Feature{
		ID:            "other-feature",
		Name:          "Other Feature",
		Slug:          "other-feature",
		Status:        feature.StatusImplementing,
		CurrentPhase:  feature.PhaseImplement,
		Created:       created.Add(-2 * time.Hour),
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	parent := &feature.Feature{
		ID:            "closure-parent",
		Name:          "Closure Parent",
		Slug:          "closure-parent",
		Status:        feature.StatusPublished,
		CurrentPhase:  feature.PhasePublish,
		Created:       created.Add(-time.Hour),
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	child := &feature.Feature{
		ID:            "closure-child",
		Name:          "Closure Child",
		Slug:          "closure-child",
		Status:        feature.StatusCreated,
		CurrentPhase:  feature.PhasePlan,
		Pipeline:      feature.PipelineMedium,
		Created:       created,
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
		Parent: &feature.ChildRelationship{
			ParentID: parent.ID,
			Kind:     feature.ChildKindRefactor,
		},
	}
	for _, f := range []*feature.Feature{other, parent, child} {
		if err := store.Save(f); err != nil {
			t.Fatalf("Save(%s) error = %v", f.ID, err)
		}
	}

	wm := git.NewWorktreeManager(wtBaseDir)
	mgr := feature.NewManager(store, config.NewDefault())
	mgr.Worktrees = wm
	mgr.Cleanliness = journeyCleanliness(wm)

	serverEvents := make(chan interface{}, 512)
	stopForwarding := make(chan struct{})
	defer close(stopForwarding)

	sm := session.NewManager(serverEvents)
	t.Cleanup(sm.Shutdown)
	pr := journeyChildPhaseRunner(t, sm, store, stateDir)

	orch := orchestrator.New(orchestrator.Deps{
		Lifecycle:   mgr,
		Store:       store,
		Sessions:    sm,
		Recovery:    session.NewRecoveryAdapter(stateDir, mgr),
		Publisher:   &git.PublishAdapter{},
		Worktrees:   wm,
		PhaseRunner: pr,
		CmdRunner:   pr.CommandRunner,
		Cleanliness: journeyCleanliness(wm),
	}, orchestrator.Hooks{})
	t.Cleanup(func() {
		_ = orch.Shutdown()
		orch.WaitForCycles()
	})
	go func() {
		for {
			select {
			case ev := <-orch.Events():
				select {
				case serverEvents <- ev:
				default:
				}
			case <-stopForwarding:
				return
			}
		}
	}()

	runtimeCfg := config.NewDefault()
	runtimeCfg.WorkspaceRoots = []string{tmp}

	srv := httptest.NewServer(server.NewHandler(server.HandlerOptions{
		Runtime:               server.RuntimeIdentity{RuntimeDir: tmp, StateDir: stateDir},
		Features:              store,
		FeatureStore:          store,
		Freshness:             journeyFreshnessProvider{},
		Config:                runtimeCfg,
		Sessions:              sm,
		Events:                serverEvents,
		Mutations:             &journeyMutationTarget{mgr: mgr, orch: orch},
		Cleanliness:           journeyCleanliness(wm),
		DisableHostValidation: true,
	}))
	t.Cleanup(srv.Close)
	client, err := server.NewClient(server.ClientOptions{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	h, err := tuitest.NewAppHarness(t.Context(), client, tui.APIAppOptions{})
	if err != nil {
		t.Fatalf("NewAppHarness() error = %v", err)
	}
	defer h.Close()
	h.Resize(140, 42)

	// The unrelated feature sorts ahead of the parent and owns cold-boot
	// selection; the active child projects as the nested row beneath its
	// Refactoring parent.
	if got := h.SelectedFeatureID(); got != other.ID {
		t.Fatalf("cold boot selected feature = %q, want first-sorted %q", got, other.ID)
	}
	assertViewContains(t, h.View(), "Refactoring", "closure-child", "↳")

	selectedChild := false
	for i := 0; i < 6 && !selectedChild; i++ {
		h.Press(tea.KeyDown)
		if h.SelectedFeatureID() == child.ID {
			selectedChild = true
		}
	}
	if !selectedChild {
		t.Fatalf("could not select the nested active child row; selected = %q", h.SelectedFeatureID())
	}

	// Close the relationship on disk (normal completion), then deliver the
	// same ordered relationship refresh an SSE frame would trigger.
	childRec, err := store.Load(child.ID)
	if err != nil {
		t.Fatalf("Load(child) error = %v", err)
	}
	closedAt := time.Now().UTC()
	childRec.Parent.CloseOutcome = "completed"
	childRec.Parent.ClosedAt = &closedAt
	if err := store.Save(childRec); err != nil {
		t.Fatalf("Save(closed child) error = %v", err)
	}
	h.RefreshRelationship(parent.ID, child.ID)

	if got := h.SelectedFeatureID(); got != parent.ID {
		t.Fatalf("after child closure selected feature = %q, want parent fallback %q (not first-sorted %q)",
			got, parent.ID, other.ID)
	}
	view := ansi.Strip(h.View())
	if !strings.Contains(view, "Refactor History (1)") {
		t.Fatalf("closed child not nested in the collapsed history group:\n%s", view)
	}
	if strings.Contains(view, "closure-child") {
		t.Fatalf("dashboard still lists the closed child outside history:\n%s", view)
	}
	if strings.Contains(view, "Refactoring") {
		t.Fatalf("parent still displays Refactoring after the relationship refresh closed the child:\n%s", view)
	}
	t.Logf("ordered closure refresh moved the child into the collapsed history group and fell back to the parent across the reorder")
}
