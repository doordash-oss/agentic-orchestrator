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
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
)

func tweakRecoveryItem() session.RecoveryItem {
	return session.RecoveryItem{
		PIDFile: session.PIDFile{
			FeatureID: "test-feat",
			PID:       12345,
			Phase:     feature.PhaseImplement.DirName(),
			Iteration: 1,
		},
		ProcessAlive: false,
		Feature: func() *feature.Feature {
			f := &feature.Feature{ID: "test-feat"}
			f.SetActiveCycleType(feature.CycleTweak)
			return f
		}(),
		RepoName: "",
	}
}

func nonTweakRecoveryItem() session.RecoveryItem {
	return session.RecoveryItem{
		PIDFile: session.PIDFile{
			FeatureID: "test-feat",
			PID:       12345,
			Phase:     feature.PhaseImplement.DirName(),
			Iteration: 1,
		},
		ProcessAlive: false,
		Feature: &feature.Feature{
			ID: "test-feat",
		},
		RepoName: "",
	}
}

func TestRecoveryModel_TweakSession_DefaultActionIsKill(t *testing.T) {
	m := NewRecoveryModel([]session.RecoveryItem{tweakRecoveryItem()})

	key := session.RecoveryActionKey("test-feat", "")
	action := m.Actions()[key]
	if action != session.RecoveryKill {
		t.Errorf("expected default action RecoveryKill (%d) for tweak item, got %d", session.RecoveryKill, action)
	}
}

func TestRecoveryModel_TweakSession_ResumeIgnored(t *testing.T) {
	m := NewRecoveryModel([]session.RecoveryItem{tweakRecoveryItem()})

	m, _ = m.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})

	key := session.RecoveryActionKey("test-feat", "")
	action := m.Actions()[key]
	if action != session.RecoveryKill {
		t.Errorf("expected action RecoveryKill (%d) after resume key on tweak item, got %d", session.RecoveryKill, action)
	}
}

func TestRecoveryModel_TweakSession_SkipIgnored(t *testing.T) {
	m := NewRecoveryModel([]session.RecoveryItem{tweakRecoveryItem()})

	m, _ = m.Update(tea.KeyPressMsg{Code: 's', Text: "s"})

	key := session.RecoveryActionKey("test-feat", "")
	action := m.Actions()[key]
	if action != session.RecoveryKill {
		t.Errorf("expected action RecoveryKill (%d) after skip key on tweak item, got %d", session.RecoveryKill, action)
	}
}

func TestRecoveryModel_TweakSession_KillAllowed(t *testing.T) {
	m := NewRecoveryModel([]session.RecoveryItem{tweakRecoveryItem()})

	m, _ = m.Update(tea.KeyPressMsg{Code: 'k', Text: "k"})

	key := session.RecoveryActionKey("test-feat", "")
	action := m.Actions()[key]
	if action != session.RecoveryKill {
		t.Errorf("expected action RecoveryKill (%d) after kill key on tweak item, got %d", session.RecoveryKill, action)
	}
}

func TestRecoveryModel_NonTweakSession_ResumeAllowed(t *testing.T) {
	m := NewRecoveryModel([]session.RecoveryItem{nonTweakRecoveryItem()})

	m, _ = m.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})

	key := session.RecoveryActionKey("test-feat", "")
	action := m.Actions()[key]
	if action != session.RecoveryResume {
		t.Errorf("expected action RecoveryResume (%d) after resume key on non-tweak item, got %d", session.RecoveryResume, action)
	}
}

func TestRecoveryModel_NonTweakSession_SkipAllowed(t *testing.T) {
	m := NewRecoveryModel([]session.RecoveryItem{nonTweakRecoveryItem()})

	m, _ = m.Update(tea.KeyPressMsg{Code: 's', Text: "s"})

	key := session.RecoveryActionKey("test-feat", "")
	action := m.Actions()[key]
	if action != session.RecoverySkip {
		t.Errorf("expected action RecoverySkip (%d) after skip key on non-tweak item, got %d", session.RecoverySkip, action)
	}
}

// --- Phase 5 Multi-Repo Tweak Recovery Tests ---

func multiRepoTweakRecoveryItem() session.RecoveryItem {
	return session.RecoveryItem{
		PIDFile: session.PIDFile{
			FeatureID: "test-feat-mr",
			PID:       12345,
			Phase:     feature.PhaseImplement.DirName(),
			Iteration: 1,
		},
		ProcessAlive: false,
		Feature: &feature.Feature{
			ID: "test-feat-mr",
			RepoCycles: map[string]*feature.RepoCycleState{
				"api": {Type: feature.CycleTweak, Status: "running"},
			},
		},
		RepoName: "api",
	}
}

func TestRecovery_MultiRepoTweak_KillOnly(t *testing.T) {
	item := multiRepoTweakRecoveryItem()
	m := NewRecoveryModel([]session.RecoveryItem{item})

	actionKey := session.RecoveryActionKey("test-feat-mr", "api")

	// Default should be kill
	action := m.Actions()[actionKey]
	if action != session.RecoveryKill {
		t.Errorf("default action = %d, want RecoveryKill (%d)", action, session.RecoveryKill)
	}

	// Resume should be ignored — still kill
	m, _ = m.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	action = m.Actions()[actionKey]
	if action != session.RecoveryKill {
		t.Errorf("after 'r': action = %d, want RecoveryKill (%d)", action, session.RecoveryKill)
	}

	// Skip should be ignored — still kill
	m, _ = m.Update(tea.KeyPressMsg{Code: 's', Text: "s"})
	action = m.Actions()[actionKey]
	if action != session.RecoveryKill {
		t.Errorf("after 's': action = %d, want RecoveryKill (%d)", action, session.RecoveryKill)
	}
}

// TestRecoveryModel_Items_ReturnsCapturedSnapshot verifies RecoveryModel
// surfaces the items it was constructed with verbatim. updateRecovery relies
// on this snapshot semantics so a concurrent rescan does not silently swap
// the item set while the user is inspecting the view (iteration 12 reviewer
// finding #2).
func TestRecoveryModel_Items_ReturnsCapturedSnapshot(t *testing.T) {
	items := []session.RecoveryItem{tweakRecoveryItem(), nonTweakRecoveryItem()}
	m := NewRecoveryModel(items)

	got := m.Items()
	if len(got) != len(items) {
		t.Fatalf("Items() returned %d items, want %d", len(got), len(items))
	}
	for i := range items {
		if got[i].PIDFile.FeatureID != items[i].PIDFile.FeatureID {
			t.Errorf("Items()[%d].FeatureID = %q, want %q", i, got[i].PIDFile.FeatureID, items[i].PIDFile.FeatureID)
		}
	}
}

func TestRecoveryItem_RepoSuffix_HiddenForSingleRepoFeature(t *testing.T) {
	item := session.RecoveryItem{
		PIDFile: session.PIDFile{
			FeatureID: "feat-1",
			PID:       12345,
			Phase:     feature.PhaseImplement.DirName(),
			Iteration: 1,
		},
		Feature: &feature.Feature{
			ID:    "feat-1",
			Repos: []feature.FeatureRepo{{Name: "repo-a"}},
		},
		RepoName: "repo-a",
	}
	m := NewRecoveryModel([]session.RecoveryItem{item})
	view := m.View()
	if strings.Contains(view, "(repo: repo-a)") {
		t.Errorf("View() = %q, did not expect (repo: repo-a) suffix for single-repo feature", view)
	}
}

func TestRecoveryItem_RepoSuffix_RenderedForMultiRepoFeature(t *testing.T) {
	item := session.RecoveryItem{
		PIDFile: session.PIDFile{
			FeatureID: "feat-1",
			PID:       12345,
			Phase:     feature.PhaseImplement.DirName(),
			Iteration: 1,
		},
		Feature: &feature.Feature{
			ID:    "feat-1",
			Repos: []feature.FeatureRepo{{Name: "repo-a"}, {Name: "repo-b"}},
		},
		RepoName: "repo-a",
	}
	m := NewRecoveryModel([]session.RecoveryItem{item})
	view := m.View()
	if !strings.Contains(view, "(repo: repo-a)") {
		t.Errorf("View() = %q, want (repo: repo-a) suffix for multi-repo feature", view)
	}
}

func TestRecoveryItem_RepoSuffix_HiddenWhenRepoNameEmpty(t *testing.T) {
	item := session.RecoveryItem{
		PIDFile: session.PIDFile{
			FeatureID: "feat-1",
			PID:       12345,
			Phase:     feature.PhaseImplement.DirName(),
			Iteration: 1,
		},
		Feature: &feature.Feature{
			ID:    "feat-1",
			Repos: []feature.FeatureRepo{{Name: "repo-a"}, {Name: "repo-b"}},
		},
		RepoName: "",
	}
	m := NewRecoveryModel([]session.RecoveryItem{item})
	view := m.View()
	if strings.Contains(view, "(repo:") {
		t.Errorf("View() = %q, did not expect any (repo: ...) suffix when RepoName empty", view)
	}
}
