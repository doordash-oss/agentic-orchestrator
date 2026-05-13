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

package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

func setupRoadmapRewindFeature(t *testing.T, app AppModel, fm *feature.Manager, total, current int, anchors map[int]map[string]string) *feature.Feature {
	t.Helper()
	f, err := fm.Create("roadmap-rewind", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil, feature.CreateOptions{
		Pipeline: feature.PipelineLarge,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	roadmapDir := filepath.Join(agent.ActiveRunDir(fm.Store.BaseDir, f), "roadmap")
	if err := os.MkdirAll(roadmapDir, 0o755); err != nil {
		t.Fatalf("mkdir roadmap: %v", err)
	}
	roadmapPath := filepath.Join(roadmapDir, "roadmap.md")
	names := []string{"Bootstrap", "Preferences", "Polish"}
	roadmapLines := []string{"# Roadmap", ""}
	for i := 1; i <= total; i++ {
		name := fmt.Sprintf("Phase %d", i)
		if i <= len(names) {
			name = names[i-1]
		}
		roadmapLines = append(roadmapLines,
			fmt.Sprintf("## Phase %d/%d: %s", i, total, name),
			"### Goal",
			fmt.Sprintf("Complete roadmap phase %d.", i),
			"",
		)
	}
	roadmap := strings.Join(roadmapLines, "\n")
	if err := os.WriteFile(roadmapPath, []byte(roadmap), 0o644); err != nil {
		t.Fatalf("write roadmap: %v", err)
	}

	if err := fm.Store.Modify(f.ID, func(f *feature.Feature) error {
		f.Status = feature.StatusImplementing
		f.CurrentPhase = feature.PhaseImplement
		f.CurrentRoadmapPhase = current
		f.TotalRoadmapPhases = total
		f.RoadmapPhaseType = "tdd-fill-in"
		f.Artifacts = map[string]string{"roadmap": filepath.Join("roadmap", "roadmap.md")}
		f.Repos[0].WorktreePath = filepath.Join(t.TempDir(), "worktree")
		if err := os.MkdirAll(f.Repos[0].WorktreePath, 0o755); err != nil {
			return err
		}
		f.Run().RoadmapPhaseCommitAnchors = anchors
		return nil
	}); err != nil {
		t.Fatalf("modify: %v", err)
	}

	f, err = app.featureManager.Get(f.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	return f
}

func findRewindChoice(t *testing.T, choices []feature.RewindChoice, phase feature.Phase) int {
	t.Helper()
	for i, choice := range choices {
		if choice.Phase == phase {
			return i
		}
	}
	t.Fatalf("choice %s not found in %v", phase, choices)
	return -1
}

func pressKey(app AppModel, msg tea.KeyPressMsg) AppModel {
	model, _ := app.Update(msg)
	return model.(AppModel)
}

func TestRoadmapPhasePickerModal_RendersMetadataStatusEffectsAndAnchorWarning(t *testing.T) {
	app, fm := newTestAppModel(t)
	f := setupRoadmapRewindFeature(t, app, fm, 3, 2, map[int]map[string]string{
		1: {"test-repo": "1111111111111111111111111111111111111111"},
	})

	rows := app.buildRoadmapRewindRows(f)
	app.rewindPhasePickerActive = true
	app.rewindPhasePickerRows = rows
	app.rewindPhasePickerCursor = 1
	app.width = 100

	output := app.rewindPhasePickerModal()
	checks := []string{
		"Choose Roadmap Phase",
		"Phase 1/3: Bootstrap",
		"tracer-bullet",
		"completed",
		"Preserve: none; redo: Phase 1; discard: Phases 2-3",
		"Phase 2/3: Preferences",
		"tdd-fill-in",
		"current",
		"Preserve: Phase 1; redo: Phase 2; discard: Phase 3",
		"Phase 3/3: Polish",
		"pending",
		"reset boundary unavailable",
	}
	for _, want := range checks {
		if !strings.Contains(output, want) {
			t.Errorf("rewindPhasePickerModal() missing %q in:\n%s", want, output)
		}
	}
}

func TestRewindMenuEnterImplementMultiPhaseOpensPickerAndEscRestoresMenu(t *testing.T) {
	app, fm := newTestAppModel(t)
	f := setupRoadmapRewindFeature(t, app, fm, 3, 2, map[int]map[string]string{
		1: {"test-repo": "1111111111111111111111111111111111111111"},
	})
	choices := feature.RewindChoicesForFeature(f)
	implementIdx := findRewindChoice(t, choices, feature.PhaseImplement)

	app.rewindMenuActive = true
	app.rewindMenuFeatureID = f.ID
	app.rewindMenuChoices = choices
	app.rewindMenuCursor = implementIdx

	app = pressKey(app, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !app.rewindPhasePickerActive {
		t.Fatal("Implement rewind on a multi-phase roadmap should open the phase picker")
	}
	if app.rewindConfirmActive {
		t.Fatal("phase picker should open before confirmation")
	}

	app = pressKey(app, tea.KeyPressMsg{Code: tea.KeyEsc})
	if !app.rewindMenuActive {
		t.Fatal("Esc from phase picker should return to the rewind menu")
	}
	if app.rewindMenuCursor != implementIdx {
		t.Errorf("rewindMenuCursor = %d, want %d", app.rewindMenuCursor, implementIdx)
	}
}

func TestRewindMenuModal_ImplementMultiPhaseShowsChoosePhaseLabel(t *testing.T) {
	app, fm := newTestAppModel(t)
	f := setupRoadmapRewindFeature(t, app, fm, 3, 2, map[int]map[string]string{
		1: {"test-repo": "1111111111111111111111111111111111111111"},
	})
	choices := feature.RewindChoicesForFeature(f)
	implementIdx := findRewindChoice(t, choices, feature.PhaseImplement)

	app.rewindMenuActive = true
	app.rewindMenuFeatureID = f.ID
	app.rewindMenuChoices = choices
	app.rewindMenuCursor = implementIdx
	app.width = 100

	output := app.rewindMenuModal()
	if !strings.Contains(output, "Choose Implement roadmap phase") {
		t.Fatalf("rewindMenuModal() missing multi-phase choose label in:\n%s", output)
	}
}

func TestRewindMenuEnterImplementSinglePhaseSkipsPicker(t *testing.T) {
	app, fm := newTestAppModel(t)
	f := setupRoadmapRewindFeature(t, app, fm, 1, 1, nil)
	choices := feature.RewindChoicesForFeature(f)
	implementIdx := findRewindChoice(t, choices, feature.PhaseImplement)

	app.rewindMenuActive = true
	app.rewindMenuFeatureID = f.ID
	app.rewindMenuChoices = choices
	app.rewindMenuCursor = implementIdx

	app = pressKey(app, tea.KeyPressMsg{Code: tea.KeyEnter})
	if app.rewindPhasePickerActive {
		t.Fatal("single-phase roadmap should not open the phase picker")
	}
	if !app.rewindConfirmActive {
		t.Fatal("single-phase roadmap should continue to full Implement confirmation")
	}
	if app.rewindConfirmRoadmapPhase != 0 {
		t.Errorf("rewindConfirmRoadmapPhase = %d, want 0 for full Implement rewind", app.rewindConfirmRoadmapPhase)
	}
}

func TestRoadmapPhasePickerEnterAnchorBackedOpensPartialConfirmation(t *testing.T) {
	app, fm := newTestAppModel(t)
	f := setupRoadmapRewindFeature(t, app, fm, 3, 2, map[int]map[string]string{
		1: {"test-repo": "1111111111111111111111111111111111111111"},
	})

	app.openRoadmapPhasePicker(f.ID)
	app.rewindPhasePickerCursor = 1
	app = pressKey(app, tea.KeyPressMsg{Code: tea.KeyEnter})

	if !app.rewindConfirmActive {
		t.Fatal("anchor-backed phase should open partial rewind confirmation")
	}
	if app.rewindConfirmRoadmapPhase != 2 {
		t.Errorf("rewindConfirmRoadmapPhase = %d, want 2", app.rewindConfirmRoadmapPhase)
	}
	if app.rewindPartialUnavailableActive {
		t.Fatal("anchor-backed phase should not open fallback warning")
	}
}

func TestRoadmapPhasePickerEnterAnchorMissingOpensFallbackWarningAndFullConfirmation(t *testing.T) {
	app, fm := newTestAppModel(t)
	f := setupRoadmapRewindFeature(t, app, fm, 3, 2, map[int]map[string]string{
		1: {"test-repo": "1111111111111111111111111111111111111111"},
	})

	app.openRoadmapPhasePicker(f.ID)
	app.rewindPhasePickerCursor = 2
	app = pressKey(app, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !app.rewindPartialUnavailableActive {
		t.Fatal("anchor-missing phase should open the fallback warning")
	}
	if app.rewindConfirmActive {
		t.Fatal("anchor-missing phase should not open confirmation directly")
	}

	app = pressKey(app, tea.KeyPressMsg{Code: 'f', Text: "f"})
	if !app.rewindConfirmActive {
		t.Fatal("explicit fallback should open full Implement confirmation")
	}
	if app.rewindConfirmRoadmapPhase != 0 {
		t.Errorf("rewindConfirmRoadmapPhase = %d, want 0 for full fallback", app.rewindConfirmRoadmapPhase)
	}
	if app.rewindConfirmPhase != feature.PhaseImplement {
		t.Errorf("rewindConfirmPhase = %s, want Implement", app.rewindConfirmPhase)
	}
}

func TestRewindConfirmModal_PartialRoadmapImpactAndPRURLs(t *testing.T) {
	app, fm := newTestAppModel(t)
	f := setupRoadmapRewindFeature(t, app, fm, 3, 2, map[int]map[string]string{
		1: {"test-repo": "1111111111111111111111111111111111111111"},
	})
	if err := fm.Store.Modify(f.ID, func(f *feature.Feature) error {
		f.RepoStates = map[string]*feature.RepoState{
			"test-repo": repoStatePR("https://github.com/example/repo/pull/7"),
		}
		return nil
	}); err != nil {
		t.Fatalf("modify pr: %v", err)
	}
	f, err := fm.Get(f.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}

	app.openPartialRewindConfirmation(f.ID, app.buildRoadmapRewindRows(f)[1])
	output := app.rewindConfirmModal()
	checks := []string{
		"Rewind Implement to roadmap Phase 2",
		"Keep: Phase 1",
		"Redo: Phase 2 (Preferences)",
		"Discard: Phase 3",
		"Reset boundary: end of roadmap Phase 1",
		"https://github.com/example/repo/pull/7",
		"A backup branch will be created",
	}
	for _, want := range checks {
		if !strings.Contains(output, want) {
			t.Errorf("rewindConfirmModal() missing %q in:\n%s", want, output)
		}
	}
}

func TestRewindCmdPartialDispatchesRoadmapPhaseOnlyForPartial(t *testing.T) {
	fake := newFakeOrch()
	app := AppModel{orchestrator: fake}

	partialMsg := app.rewindCmd("feat-1", feature.PhaseImplement, 2)()
	if _, ok := partialMsg.(RewindDoneMsg); !ok {
		t.Fatalf("partial rewindCmd returned %T, want RewindDoneMsg", partialMsg)
	}
	if !slices.Contains(fake.lifecycleCalls, "RewindWithRequest:feat-1:implement:2") {
		t.Fatalf("partial rewind did not dispatch selected roadmap phase, calls=%v", fake.lifecycleCalls)
	}

	fullMsg := app.rewindCmd("feat-2", feature.PhaseImplement, 0)()
	if _, ok := fullMsg.(RewindDoneMsg); !ok {
		t.Fatalf("full rewindCmd returned %T, want RewindDoneMsg", fullMsg)
	}
	if !slices.Contains(fake.lifecycleCalls, "RewindToPhase:feat-2:implement") {
		t.Fatalf("full rewind did not preserve phase-only dispatch, calls=%v", fake.lifecycleCalls)
	}
}

func TestRoadmapPhasePickerFallbackRowsWhenRoadmapCannotParse(t *testing.T) {
	app, fm := newTestAppModel(t)
	f := setupRoadmapRewindFeature(t, app, fm, 3, 2, nil)
	badRoadmap := filepath.Join(agent.ActiveRunDir(fm.Store.BaseDir, f), "roadmap", "roadmap.md")
	if err := os.WriteFile(badRoadmap, []byte("# no parseable phase headings"), 0o644); err != nil {
		t.Fatalf("write bad roadmap: %v", err)
	}
	f, err := fm.Get(f.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}

	rows := app.buildRoadmapRewindRows(f)
	if len(rows) != 3 {
		t.Fatalf("len(rows) = %d, want 3", len(rows))
	}
	if got, want := rows[1].Title, "Phase 2"; got != want {
		t.Errorf("fallback row title = %q, want %q", got, want)
	}
	if got, want := fmt.Sprint(rows[1].Number), "2"; got != want {
		t.Errorf("fallback row number = %q, want %q", got, want)
	}
}
